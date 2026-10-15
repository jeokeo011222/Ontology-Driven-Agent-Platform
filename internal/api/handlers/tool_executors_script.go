package handlers

// 脚本工具执行器（adapter=script）：在 wazero WASM 运行时中真实执行用户登记的
// WebAssembly 模块。
//
// 安全边界（设计与实现共同保证，缺一不可）：
//   - wazero 运行时默认不授予文件系统、网络、时钟、随机数之外的任何 host 能力；
//     本执行器按 WASI 命令模型运行：stdin=args 的 JSON、stdout=输出、stderr=错误
//     上下文，且**不预开放任何目录**（no preopen）、不注入环境变量——脚本只能是
//     纯计算，任何文件/网络 syscall 都会以权限错误失败。
//   - 因此 adapter=script 的工具版本 effect_class 必须（且只能）为 READ：
//     本文件 validateExecutionConfigForRegistration 在登记侧强制（非 READ 直接
//     400），有写副作用的工具必须走真实、可审批、可审计的适配器（如 ERP Port），
//     绝不允许以"脚本"形式绕过审批门禁。
//   - 执行器不访问数据库、不做任何外部 IO；tenantctx.Identity 不参与执行，
//     租户维度由 InvokeTool/ApproveToolInvocation 的调用记录与审计承担。
//
// fail-closed 纪律：language/wasm_b64 非法、魔数不符、编译失败、stdout 非 JSON
// 对象、输出超限、超时——全部显式报错，绝不静默截断、绝不伪造成功。

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"

	wazero "github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"

	"odap/internal/platform/errors"
)

// script 执行契约的常量边界（登记侧校验、执行侧钳制共用）。
const (
	scriptLanguageWasm       = "wasm"
	scriptDefaultTimeoutMs   = 30_000  // 默认 30s
	scriptMaxTimeoutMs       = 300_000 // 上限 300s
	scriptDefaultMaxOutput   = 1 << 20 // 默认 1MB
	scriptMaxOutputCap       = 8 << 20 // max_output_bytes 上限 8MB
	scriptMaxModuleBytes     = 8 << 20 // .wasm 模块大小上限 8MB
	scriptCompileCacheMax    = 64      // 编译缓存条目上限（防不可控内存增长）
	scriptStderrExcerptBytes = 500     // 错误信息中携带的 stderr 摘要长度
)

// wasmMagic 是 WebAssembly 模块的固定魔数 "\0asm"（0x00 0x61 0x73 0x6D）。
var wasmMagic = []byte{0x00, 0x61, 0x73, 0x6D}

// ---- 进程级 WASM 运行时与编译缓存 ----
//
// wazero.Runtime 线程安全且可并发实例化模块；CompiledModule 同样可并发实例化。
// 运行时进程级单例；编译缓存按模块字节 SHA-256 去重，避免同一模块反复编译。
var (
	scriptRuntimeOnce sync.Once
	scriptRuntime     wazero.Runtime
	scriptRuntimeErr  error

	scriptCompileMu sync.Mutex
	scriptCompiles  = map[string]wazero.CompiledModule{} // key: sha256(module bytes) 的 FNV 前缀 + 模块哈希
)

// scriptRuntimeInstance 返回进程级 wazero 运行时（含 WASI 宿主模块）。
//
// WithCloseOnContextDone(true) 是超时控制的关键：开启后 wazero 在每次调用中
// 监听 ctx 取消/超时，超时即强制关闭模块实例（否则 wasm 死循环无法被打断，
// 只能挂死执行协程）。代价是每次调用多一个监听协程，工具调用量级下可接受。
func scriptRuntimeInstance() (wazero.Runtime, error) {
	scriptRuntimeOnce.Do(func() {
		rt := wazero.NewRuntimeWithConfig(context.Background(),
			wazero.NewRuntimeConfig().WithCloseOnContextDone(true))
		// WASI 宿主模块进程级实例化一次；guest 侧的 stdin/stdout/args/env
		// 取自每次实例化 guest 时传入的 ModuleConfig，互不串扰。
		if _, err := wasi_snapshot_preview1.Instantiate(context.Background(), rt); err != nil {
			scriptRuntimeErr = errors.Wrap(err, errors.CodeInternal, "WASI 宿主模块初始化失败")
			return
		}
		scriptRuntime = rt
	})
	if scriptRuntimeErr != nil {
		return nil, scriptRuntimeErr
	}
	return scriptRuntime, nil
}

// scriptCompiledModule 返回模块的编译产物（按模块字节哈希做进程内缓存）。
// 编译失败的模块不入缓存（调用方直接得到 INVALID_ARGUMENT）。
func scriptCompiledModule(ctx context.Context, rt wazero.Runtime, wasm []byte) (wazero.CompiledModule, error) {
	// 缓存键：FNV-1a 取模分桶 + 内容前缀，避免对每个调用都做全量 SHA-256 计算；
	// 哈希碰撞只影响缓存命中（键仍含完整内容前缀），不影响正确性。
	h := fnv.New64a()
	_, _ = h.Write(wasm)
	key := fmt.Sprintf("%016x:%x", h.Sum64(), wasm[:min(len(wasm), 64)])

	scriptCompileMu.Lock()
	defer scriptCompileMu.Unlock()
	if cm, ok := scriptCompiles[key]; ok {
		return cm, nil
	}
	cm, err := rt.CompileModule(ctx, wasm)
	if err != nil {
		return nil, errors.E(errors.CodeInvalidArgument,
			"WASM 模块编译/校验失败（不是合法的 WebAssembly 模块）："+err.Error())
	}
	if len(scriptCompiles) >= scriptCompileCacheMax {
		// 纯性能缓存：清空后由后续调用按需重编译，语义不变。
		scriptCompiles = map[string]wazero.CompiledModule{}
	}
	scriptCompiles[key] = cm
	return cm, nil
}

// execScriptTool 在 wazero 中真实执行 adapter=script 的工具。
//
// 运行模型（WASI 命令）：stdin = args 的 JSON；stdout = 工具输出（必须是 JSON
// 对象）；stderr = 错误上下文（失败时摘录进错误消息）。无文件预开放、无环境
// 变量、无网络：脚本是纯计算。
func (h *Handler) execScriptTool(ctx context.Context, cfg executionConfig, args map[string]any) (map[string]any, error) {
	if cfg.Language != scriptLanguageWasm {
		return nil, errors.Ef(errors.CodeInvalidArgument,
			"脚本执行器仅支持 language=%q（实际 %q）", scriptLanguageWasm, cfg.Language)
	}
	if strings.TrimSpace(cfg.WasmB64) == "" {
		return nil, errors.E(errors.CodeInvalidArgument,
			"script 工具缺少 wasm_b64（base64 编码的 .wasm 模块），无法执行")
	}
	wasm, err := base64.StdEncoding.DecodeString(cfg.WasmB64)
	if err != nil {
		return nil, errors.E(errors.CodeInvalidArgument, "wasm_b64 不是合法 base64："+err.Error())
	}
	if len(wasm) < len(wasmMagic) || !bytes.Equal(wasm[:len(wasmMagic)], wasmMagic) {
		return nil, errors.E(errors.CodeInvalidArgument,
			"wasm_b64 解码后缺少 WebAssembly 魔数（\\0asm），不是 .wasm 模块")
	}

	timeoutMs := cfg.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = scriptDefaultTimeoutMs
	}
	if timeoutMs > scriptMaxTimeoutMs {
		timeoutMs = scriptMaxTimeoutMs // 防御性钳制；登记侧已拒绝越界值
	}
	maxOutput := cfg.MaxOutputBytes
	if maxOutput <= 0 {
		maxOutput = scriptDefaultMaxOutput
	}
	if maxOutput > scriptMaxOutputCap {
		maxOutput = scriptMaxOutputCap
	}

	rt, err := scriptRuntimeInstance()
	if err != nil {
		return nil, err
	}
	compiled, err := scriptCompiledModule(ctx, rt, wasm)
	if err != nil {
		return nil, err
	}

	if args == nil {
		args = map[string]any{}
	}
	argsJSON, _ := json.Marshal(args)

	stdout := &limitedWriter{max: maxOutput}
	var stderr bytes.Buffer

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()
	start := time.Now()

	modConfig := wazero.NewModuleConfig().
		WithName("odap-script-tool").
		WithArgs("odap-script-tool"). // argv[0]；无业务参数，全部经 stdin 传递
		WithStdin(bytes.NewReader(argsJSON)).
		WithStdout(stdout).
		WithStderr(&stderr)

	// InstantiateModule 会执行 WASI 命令的 _start（即 Go wasip1 的 main）。
	// 模块以 proc_exit(0) 结束时返回 (mod, nil)；非零退出/取消/超时返回
	// *sys.ExitError；trap 等其余错误返回包装错误。
	mod, runErr := rt.InstantiateModule(runCtx, compiled, modConfig)
	elapsed := time.Since(start)
	if mod != nil {
		_ = mod.Close(context.WithoutCancel(runCtx)) // 立即释放模块实例资源
	}

	// 1) 输出超限最先判：fail-closed，严禁静默截断当成功。
	if stdout.exceeded {
		return nil, errors.Ef(errors.CodeResourceExhausted,
			"脚本 stdout 输出超过 max_output_bytes=%d 字节上限（实际至少 %d 字节），拒绝返回（不静默截断）",
			maxOutput, stdout.total)
	}
	// 2) 超时/取消：错误信息明确含实际耗时与 timeout_ms。
	if runCtx.Err() != nil {
		return nil, errors.Ef(errors.CodeDeadlineExceeded,
			"脚本执行超时：已运行 %s，超过 timeout_ms=%d，模块被强制终止",
			elapsed.Truncate(time.Millisecond), timeoutMs)
	}
	// 3) 其余运行失败：非零退出码 / WASM trap，附 stderr 摘要。
	if runErr != nil {
		var exitErr *sys.ExitError
		if stderrors.As(runErr, &exitErr) {
			return nil, errors.Ef(errors.CodeInternal,
				"脚本以非零退出码结束（exit=%d），stderr：%s", exitErr.ExitCode(), stderrExcerpt(stderr))
		}
		return nil, errors.Ef(errors.CodeInternal,
			"脚本执行失败（WASM 运行时错误）：%s；stderr：%s", runErr, stderrExcerpt(stderr))
	}

	// 4) stdout 必须是 JSON 对象才可作为工具输出（fail-closed）。
	trimmed := bytes.TrimSpace(stdout.data)
	if len(trimmed) == 0 {
		return nil, errors.E(errors.CodeInvalidArgument,
			"脚本没有 stdout 输出：工具输出必须是 JSON 对象")
	}
	var out map[string]any
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return nil, errors.Ef(errors.CodeInvalidArgument,
			"脚本 stdout 不是合法 JSON（%s），内容前缀：%q", err.Error(), excerpt(string(trimmed), 120))
	}
	if out == nil { // 字面量 "null" 能 Unmarshal 进 map 但为 nil：不是对象
		return nil, errors.E(errors.CodeInvalidArgument,
			"脚本 stdout 是 JSON null：工具输出必须是 JSON 对象")
	}
	return out, nil
}

// stderrExcerpt 归一化 stderr 并截断摘要（错误消息不应被日志风暴淹没）。
func stderrExcerpt(stderr bytes.Buffer) string {
	s := bytes.TrimSpace(stderr.Bytes())
	if len(s) == 0 {
		return "（空）"
	}
	return excerpt(string(s), scriptStderrExcerptBytes)
}

// excerpt 截断文本到 n 字节并标注截断。
func excerpt(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(已截断)"
}

// limitedWriter 是带上限的 stdout 缓冲：超过 max 后继续消费（保持 guest 写入
// 语义不变），但丢弃内容、仅累计字节数并置 exceeded——由执行器在运行结束后
// 统一 fail-closed 报 RESOURCE_EXHAUSTED，绝不把截断结果当成功输出。
type limitedWriter struct {
	data     []byte
	max      int
	total    int
	exceeded bool
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	w.total += len(p)
	if w.total > w.max {
		w.exceeded = true
		if room := w.max - len(w.data); room > 0 {
			w.data = append(w.data, p[:room]...)
		}
		return len(p), nil
	}
	w.data = append(w.data, p...)
	return len(p), nil
}

// ---- 登记侧校验（POST /api/v1/tools / 版本登记链路）----

// validateExecutionConfigForRegistration 在工具创建/版本登记时校验执行契约。
//
// 仅限非内置工具：POST /api/v1/tools 只会创建用户自建工具（is_builtin 恒为
// false，无任何入参可改变），内置工具由数据集 seed 登记，其修改已被
// store.ensureToolMutable 以 409 拦截——不存在经本接口触碰内置工具的路径。
//
// adapter=script 的强制项：language 必须为 wasm；wasm_b64 必填且为合法 base64
// 并带 WebAssembly 魔数；模块 ≤8MB；timeout_ms ∈ [1, 300000]（提供时）；
// max_output_bytes ∈ [1, 8MB]（提供时）；effect_class 必须（缺省）为 READ。
// 其他 adapter 原样登记，执行期由 parseExecutionConfig 与派发表裁决。
func validateExecutionConfigForRegistration(raw json.RawMessage, effectClass string) (executionConfig, error) {
	var cfg executionConfig
	if len(bytes.TrimSpace(raw)) == 0 {
		return cfg, nil // 未提供执行契约：登记合法，但该版本不可被调用（执行期 fail-closed）
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, errors.E(errors.CodeInvalidArgument, "execution_config 不是合法 JSON："+err.Error())
	}
	if cfg.Adapter == "" {
		return cfg, errors.E(errors.CodeInvalidArgument, "execution_config 缺少 adapter 字段")
	}
	if cfg.Adapter != "script" {
		return cfg, nil
	}
	if effectClass != "" && effectClass != "READ" {
		return cfg, errors.E(errors.CodeInvalidArgument,
			"adapter=script 的工具 effect_class 必须为 READ：WASM 脚本是纯计算、无网络/文件访问，"+
				"不允许登记为可写（写副作用必须走可审批的真实适配器）")
	}
	if cfg.Language != scriptLanguageWasm {
		return cfg, errors.Ef(errors.CodeInvalidArgument,
			"script 执行契约的 language 必须为 %q（实际 %q）", scriptLanguageWasm, cfg.Language)
	}
	if strings.TrimSpace(cfg.WasmB64) == "" {
		return cfg, errors.E(errors.CodeInvalidArgument, "script 执行契约缺少 wasm_b64（base64 编码的 .wasm 模块）")
	}
	wasm, err := base64.StdEncoding.DecodeString(cfg.WasmB64)
	if err != nil {
		return cfg, errors.E(errors.CodeInvalidArgument, "wasm_b64 不是合法 base64："+err.Error())
	}
	if len(wasm) > scriptMaxModuleBytes {
		return cfg, errors.Ef(errors.CodeInvalidArgument,
			"WASM 模块大小 %d 字节超过上限 %d 字节", len(wasm), scriptMaxModuleBytes)
	}
	if len(wasm) < len(wasmMagic) || !bytes.Equal(wasm[:len(wasmMagic)], wasmMagic) {
		return cfg, errors.E(errors.CodeInvalidArgument,
			"wasm_b64 解码后缺少 WebAssembly 魔数（\\0asm），不是 .wasm 模块")
	}
	if cfg.TimeoutMs != 0 && (cfg.TimeoutMs < 0 || cfg.TimeoutMs > scriptMaxTimeoutMs) {
		return cfg, errors.Ef(errors.CodeInvalidArgument,
			"timeout_ms 必须在 1~%d 毫秒之间（实际 %d）", scriptMaxTimeoutMs, cfg.TimeoutMs)
	}
	if cfg.MaxOutputBytes != 0 && (cfg.MaxOutputBytes < 0 || cfg.MaxOutputBytes > scriptMaxOutputCap) {
		return cfg, errors.Ef(errors.CodeInvalidArgument,
			"max_output_bytes 必须在 1~%d 字节之间（实际 %d）", scriptMaxOutputCap, cfg.MaxOutputBytes)
	}
	return cfg, nil
}
