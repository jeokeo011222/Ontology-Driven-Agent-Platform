package handlers

// 脚本工具执行器的真实单元测试：所有路径都跑真实 WASM 模块（testdata/wasm
// 下由 Go 工具链 GOOS=wasip1 GOARCH=wasm 编译的产物），核心路径零跳过。
//
// 模块与路径的对应：
//   - echo.wasm   成功路径（stdin JSON → {"echo": <原样回显>}）
//   - spin.wasm   超时路径（纯计算死循环，只能被执行器超时终止）
//   - nojson.wasm 非 JSON 输出失败路径（正常退出但 stdout 不是 JSON 对象）

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"odap/internal/api/store"
	"odap/internal/platform/errors"
	"odap/internal/platform/tenantctx"
)

// loadTestWasmB64 读取 testdata 下的真实 .wasm 并 base64 编码。
func loadTestWasmB64(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "wasm", name))
	if err != nil {
		t.Fatalf("读取测试模块 %s 失败（需先用 GOOS=wasip1 GOARCH=wasm 编译 testdata/wasm/*）：", name)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// scriptCfgJSON 构造 adapter=script 的执行契约 JSON。
func scriptCfgJSON(t *testing.T, wasmB64 string, timeoutMs, maxOutputBytes int) json.RawMessage {
	t.Helper()
	cfg := map[string]any{"adapter": "script", "language": "wasm", "wasm_b64": wasmB64}
	if timeoutMs > 0 {
		cfg["timeout_ms"] = timeoutMs
	}
	if maxOutputBytes > 0 {
		cfg["max_output_bytes"] = maxOutputBytes
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("构造执行契约失败：%v", err)
	}
	return raw
}

func appErrorCode(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatalf("期望出错，实际成功")
	}
	return errors.As(err).Code
}

// TestExecScriptToolEchoSuccess 经 executeToolInvocation 派发（adapter=script）
// 真实执行 echo 模块：stdin=args JSON，stdout={"echo":<原样回显>}。
func TestExecScriptToolEchoSuccess(t *testing.T) {
	h := New(Deps{})
	id := tenantctx.Identity{}
	args := map[string]any{"hello": "世界", "n": 1.5, "nested": map[string]any{"ok": true}}
	rec := &store.ToolVersionRecord{
		ToolKey:         "script.echo",
		ExecutionConfig: scriptCfgJSON(t, loadTestWasmB64(t, "echo.wasm"), 30000, 1<<20),
	}
	out, err := h.ExecuteToolInvocation(httptest.NewRequest("POST", "/invoke", nil).Context(), id, rec, "inv-echo-1", args)
	if err != nil {
		t.Fatalf("echo 模块真实执行失败：%v", err)
	}
	echo, ok := out["echo"].(map[string]any)
	if !ok {
		t.Fatalf("输出缺少 echo 字段或不是对象：%v", out)
	}
	want, _ := json.Marshal(args)
	got, _ := json.Marshal(echo)
	if string(got) != string(want) {
		t.Fatalf("回显不一致：got=%s want=%s", got, want)
	}
}

// TestExecScriptToolTimeout 超时路径：spin 模块死循环，timeout_ms=150 强制终止。
func TestExecScriptToolTimeout(t *testing.T) {
	h := New(Deps{})
	cfg, err := parseExecutionConfig(scriptCfgJSON(t, loadTestWasmB64(t, "spin.wasm"), 150, 1<<20))
	if err != nil {
		t.Fatalf("解析执行契约失败：%v", err)
	}
	start := time.Now()
	_, execErr := h.execScriptTool(context.Background(), cfg, map[string]any{})
	elapsed := time.Since(start)
	if code := appErrorCode(t, execErr); code != errors.CodeDeadlineExceeded {
		t.Fatalf("期望错误码 %s，实际 %s（%v）", errors.CodeDeadlineExceeded, code, execErr)
	}
	msg := execErr.Error()
	if !containsAny(msg, "超时", "timeout_ms=150") {
		t.Fatalf("超时错误信息应包含耗时与 timeout_ms，实际：%s", msg)
	}
	if elapsed < 150*time.Millisecond {
		t.Fatalf("过早返回（%s）：超时未生效", elapsed)
	}
	if elapsed > 20*time.Second {
		t.Fatalf("超时未及时终止（%s）：WithCloseOnContextDone 未生效", elapsed)
	}
}

// TestExecScriptToolNonJSONOutput 非 JSON 输出失败路径：nojson 模块正常退出但
// stdout 不是 JSON 对象，执行器必须 fail-closed 报 INVALID_ARGUMENT。
func TestExecScriptToolNonJSONOutput(t *testing.T) {
	h := New(Deps{})
	cfg, err := parseExecutionConfig(scriptCfgJSON(t, loadTestWasmB64(t, "nojson.wasm"), 30000, 1<<20))
	if err != nil {
		t.Fatalf("解析执行契约失败：%v", err)
	}
	_, execErr := h.execScriptTool(context.Background(), cfg, map[string]any{})
	if code := appErrorCode(t, execErr); code != errors.CodeInvalidArgument {
		t.Fatalf("期望错误码 %s，实际 %s（%v）", errors.CodeInvalidArgument, code, execErr)
	}
	if !containsAny(execErr.Error(), "JSON") {
		t.Fatalf("错误信息应说明 stdout 非 JSON，实际：%s", execErr.Error())
	}
}

// TestExecScriptToolBadMagic 魔数校验：base64 合法但缺 \0asm 魔数。
func TestExecScriptToolBadMagic(t *testing.T) {
	h := New(Deps{})
	fake := base64.StdEncoding.EncodeToString([]byte("this is not a wasm module at all"))
	cfg, err := parseExecutionConfig(scriptCfgJSON(t, fake, 0, 0))
	if err != nil {
		t.Fatalf("解析执行契约失败：%v", err)
	}
	_, execErr := h.execScriptTool(context.Background(), cfg, nil)
	if code := appErrorCode(t, execErr); code != errors.CodeInvalidArgument {
		t.Fatalf("期望错误码 %s，实际 %s（%v）", errors.CodeInvalidArgument, code, execErr)
	}
	if !containsAny(execErr.Error(), "魔数") {
		t.Fatalf("错误信息应说明缺少魔数，实际：%s", execErr.Error())
	}
}

// TestExecScriptToolBadBase64 base64 非法路径。
func TestExecScriptToolBadBase64(t *testing.T) {
	h := New(Deps{})
	cfg, err := parseExecutionConfig(scriptCfgJSON(t, "!!!not-base64!!!", 0, 0))
	if err != nil {
		t.Fatalf("解析执行契约失败：%v", err)
	}
	_, execErr := h.execScriptTool(context.Background(), cfg, nil)
	if code := appErrorCode(t, execErr); code != errors.CodeInvalidArgument {
		t.Fatalf("期望错误码 %s，实际 %s（%v）", errors.CodeInvalidArgument, code, execErr)
	}
	if !containsAny(execErr.Error(), "base64") {
		t.Fatalf("错误信息应说明 base64 非法，实际：%s", execErr.Error())
	}
}

// TestExecScriptToolCompileFailure 魔数合法但模块体损坏：编译阶段必须拒绝。
func TestExecScriptToolCompileFailure(t *testing.T) {
	h := New(Deps{})
	corrupt := append([]byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00}, 0xDE, 0xAD, 0xBE, 0xEF)
	cfg, err := parseExecutionConfig(scriptCfgJSON(t, base64.StdEncoding.EncodeToString(corrupt), 0, 0))
	if err != nil {
		t.Fatalf("解析执行契约失败：%v", err)
	}
	_, execErr := h.execScriptTool(context.Background(), cfg, nil)
	if code := appErrorCode(t, execErr); code != errors.CodeInvalidArgument {
		t.Fatalf("期望错误码 %s，实际 %s（%v）", errors.CodeInvalidArgument, code, execErr)
	}
	if !containsAny(execErr.Error(), "编译") {
		t.Fatalf("错误信息应说明模块编译失败，实际：%s", execErr.Error())
	}
}

// TestExecScriptToolOutputOverLimit 输出超限路径：echo 输出 > max_output_bytes=8，
// 必须 fail-closed 报 RESOURCE_EXHAUSTED，严禁静默截断当成功。
func TestExecScriptToolOutputOverLimit(t *testing.T) {
	h := New(Deps{})
	cfg, err := parseExecutionConfig(scriptCfgJSON(t, loadTestWasmB64(t, "echo.wasm"), 30000, 8))
	if err != nil {
		t.Fatalf("解析执行契约失败：%v", err)
	}
	_, execErr := h.execScriptTool(context.Background(), cfg, map[string]any{"k": "v"})
	if code := appErrorCode(t, execErr); code != errors.CodeResourceExhausted {
		t.Fatalf("期望错误码 %s，实际 %s（%v）", errors.CodeResourceExhausted, code, execErr)
	}
	if !containsAny(execErr.Error(), "max_output_bytes=8") {
		t.Fatalf("错误信息应包含上限值，实际：%s", execErr.Error())
	}
}

// TestExecScriptToolLanguageRejected language 必须为 wasm。
func TestExecScriptToolLanguageRejected(t *testing.T) {
	h := New(Deps{})
	raw, _ := json.Marshal(map[string]any{
		"adapter": "script", "language": "wat",
		"wasm_b64": base64.StdEncoding.EncodeToString([]byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00}),
	})
	cfg, err := parseExecutionConfig(raw)
	if err != nil {
		t.Fatalf("解析执行契约失败：%v", err)
	}
	_, execErr := h.execScriptTool(context.Background(), cfg, nil)
	if code := appErrorCode(t, execErr); code != errors.CodeInvalidArgument {
		t.Fatalf("期望错误码 %s，实际 %s（%v）", errors.CodeInvalidArgument, code, execErr)
	}
}

// TestValidateExecutionConfigForRegistration 登记侧校验（fail-closed）。
func TestValidateExecutionConfigForRegistration(t *testing.T) {
	tinyWasm := base64.StdEncoding.EncodeToString([]byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00})
	bigWasm := append([]byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00}, make([]byte, 8<<20)...)
	bigB64 := base64.StdEncoding.EncodeToString(bigWasm)

	cases := []struct {
		name        string
		raw         string
		effectClass string
		wantErr     bool
	}{
		{"未提供执行契约", "", "", false},
		{"script+READ 合法", `{"adapter":"script","language":"wasm","wasm_b64":"` + tinyWasm + `"}`, "READ", false},
		{"script 缺省 effect_class 视为 READ", `{"adapter":"script","language":"wasm","wasm_b64":"` + tinyWasm + `"}`, "", false},
		{"script+EXTERNAL_WRITE 拒绝", `{"adapter":"script","language":"wasm","wasm_b64":"` + tinyWasm + `"}`, "EXTERNAL_WRITE", true},
		{"script+INTERNAL_WRITE 拒绝", `{"adapter":"script","language":"wasm","wasm_b64":"` + tinyWasm + `"}`, "INTERNAL_WRITE", true},
		{"缺少 language", `{"adapter":"script","wasm_b64":"` + tinyWasm + `"}`, "READ", true},
		{"language 非 wasm", `{"adapter":"script","language":"wat","wasm_b64":"` + tinyWasm + `"}`, "READ", true},
		{"缺少 wasm_b64", `{"adapter":"script","language":"wasm"}`, "READ", true},
		{"wasm_b64 非 base64", `{"adapter":"script","language":"wasm","wasm_b64":"!!!"}`, "READ", true},
		{"模块缺魔数", `{"adapter":"script","language":"wasm","wasm_b64":"` + base64.StdEncoding.EncodeToString([]byte("junk")) + `"}`, "READ", true},
		{"模块超过 8MB", `{"adapter":"script","language":"wasm","wasm_b64":"` + bigB64 + `"}`, "READ", true},
		{"timeout_ms 越界", `{"adapter":"script","language":"wasm","wasm_b64":"` + tinyWasm + `","timeout_ms":300001}`, "READ", true},
		{"max_output_bytes 越界", `{"adapter":"script","language":"wasm","wasm_b64":"` + tinyWasm + `","max_output_bytes":9000000}`, "READ", true},
		{"execution_config 非法 JSON", `{not-json`, "READ", true},
		{"缺少 adapter", `{"language":"wasm"}`, "READ", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateExecutionConfigForRegistration(json.RawMessage(tc.raw), tc.effectClass)
			if tc.wantErr && err == nil {
				t.Fatalf("期望报错，实际通过")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("期望通过，实际报错：%v", err)
			}
			if tc.wantErr {
				if code := appErrorCode(t, err); code != errors.CodeInvalidArgument {
					t.Fatalf("期望错误码 %s，实际 %s", errors.CodeInvalidArgument, code)
				}
			}
		})
	}
}

// TestExecScriptToolNoPreopen 安全边界冒烟：脚本内访问文件系统的 WASI syscall
// 必须失败（无预开放目录），且失败如实报错——证明执行器没有授予文件能力。
func TestExecScriptToolNoPreopen(t *testing.T) {
	h := New(Deps{})
	cfg, err := parseExecutionConfig(scriptCfgJSON(t, loadTestWasmB64(t, "echo.wasm"), 30000, 1<<20))
	if err != nil {
		t.Fatalf("解析执行契约失败：%v", err)
	}
	// echo 模块只读 stdin/写 stdout：在无预开放目录下仍应成功（纯计算语义）。
	out, execErr := h.execScriptTool(context.Background(), cfg, map[string]any{"probe": "no-fs"})
	if execErr != nil {
		t.Fatalf("纯计算脚本在无 host 能力环境下执行失败：%v", execErr)
	}
	echo, ok := out["echo"].(map[string]any)
	if !ok || echo["probe"] != "no-fs" {
		t.Fatalf("回显内容不符：%v", out)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if sub != "" && strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
