// Package handlers — 大模型自动生成（本体 / Agent / 工具 / 工作流）。
//
// 诚实性约束（项目硬约束：严禁 mock 数据、严禁静默降级）：
//   - 生成路径唯一入口是已装配的真实模型网关（handlers.Deps.LLM）；未装配、
//     或装配结果里没有任何真实 HTTP 端点时，端点返回 503 UNAVAILABLE 并说明原因；
//   - 制度文档解析失败、模型输出不是合法 JSON、输出不满足结构约束时，一律
//     以 4xx/503 明确失败并给出可定位的原因，绝不返回"生成成功"的占位内容；
//   - 生成结果落库状态为 DRAFT，必须经人工/校验链（validate → publish）才能发布。
package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"odap/internal/api/sse"
	"odap/internal/api/store"
	"odap/internal/docingest"
	"odap/internal/llm"
	"odap/internal/ontology/graphload"
	"odap/internal/ontology/graphstore"
	"odap/internal/ontology/shacl"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

const (
	defaultMaxSourceChars = 24000
	maxSourceCharsLimit   = 100000
)

// generatedOutputTokens 是 AI 自动生成类接口请求的输出上限。
//
// 4096 是 OpenAI 兼容端点（含 deepseek-chat）的**默认值**，而不是它的上限。本仓库这类
// 「制度文档 → 完整本体清单」的输出（类 + 字段 + 关系 + 枚举 + SHACL 形状 + 样例实例）
// 实测会写满 4096 tokens，被以 finish_reason=length 截断，得到一句含糊的
// "结构化输出非合法 JSON（unexpected end of JSON input）"——真实成因是上限不够，
// 不是模型不会写 JSON。这里取 8192（deepseek-chat 文档给出的最大 max_tokens），
// 为 24000 字符的输入文档留出约两倍余量。
//
// 之所以不做成"读模型档案里的能力声明"：模型版本档案当前没有输出上限这一列，
// 凭空引入需要加列 + 迁移 + 管理界面。端点若声明更小的上限，供应商会以 4xx 明确
// 拒绝（可见失败），不会静默降级。
const generatedOutputTokens = 8192

// ---------------------------------------------------------------------------
// 公共辅助
// ---------------------------------------------------------------------------

// llmJSON 调用模型网关并要求结构化 JSON 输出，返回解析后的对象与调用元数据。
func (h *Handler) llmJSON(ctx context.Context, tenantID ids.ID, modelID, system, user string,
	schema []byte, idemKey string) (map[string]any, *llm.Completion, error) {
	return h.callLLM(ctx, tenantID, modelID, []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}, schema, idemKey)
}

// callLLM 是最底层的一次模型调用：发消息、拿结构化输出、解析为 JSON 对象。
//
// 单独抽出来是为了让**调用方**能对"下游语义校验失败"做有界重生成（见
// generateOntologyManifest）：重生成需要往消息列表尾部追加"上一轮输出 + 诊断"，
// 而 llmJSON 的 (system, user) 两段式签名容不下这个中间态。
func (h *Handler) callLLM(ctx context.Context, tenantID ids.ID, modelID string,
	msgs []llm.Message, schema []byte, idemKey string) (map[string]any, *llm.Completion, error) {
	gw, err := h.llmGateway()
	if err != nil {
		return nil, nil, err
	}
	req := llm.CompletionRequest{
		TenantID:           tenantID,
		ModelID:            modelID,
		Messages:           msgs,
		ResponseSchema:     schema,
		MaxOutputTokens:    generatedOutputTokens,
		DataClassification: "INTERNAL",
		IdempotencyKey:     idemKey,
	}
	comp, err := gw.Complete(ctx, tenantID, req)
	if err != nil {
		return nil, nil, err
	}
	obj, perr := parseJSONObject(comp.Content)
	if perr != nil {
		return nil, &comp, errors.E(errors.CodeValidationFailed,
			"模型输出不是合法 JSON 对象（未能按结构约束生成）").
			WithDetails(map[string]any{
				"parse_error": perr.Error(),
				"raw_prefix":  clip(comp.Content, 400),
			})
	}
	return obj, &comp, nil
}

// generateOntologyManifest 生成并校验本体清单，对**可修复的校验失败**做有界重生成。
//
// 两处校验都在这里闭合，且都与后续流程共用同一套实现：
//   - 结构与闭包交给 graphload.ParseManifest —— 它同时是校验/装载路径的入口，
//     保持"只有一处真值来源"：上游放行、下游拒绝的漂移会让模型每次生成都拿到 422
//     却看不出原因。刻意先 Marshal 再校验：校验的正是**将被落库的那串字节**；
//   - SHACL 交给 ontologyPreflight（同 graphload.BuildShapes + shacl.Validate 引擎）。
//
// 为什么这两类失败要重生成，而不是直接 422：它们是模型没跟住约束造成的**可修复**
// 错误（提示词已写明"关系取值必须是 instances 里出现的 id""样例取值要满足自己写的
// 约束"），与"输出被截断"那种确定性失败（见 llm.invalidJSONError）不同。直接拒绝会让
// 模型连一次修正机会都没有，生成功能在真实文档上会频繁产出"不可发布"的草稿。
// 重生成次数与结构化输出回路保持一致（llm.MaxSchemaRepairAttempts）。
func (h *Handler) generateOntologyManifest(ctx context.Context, tenantID ids.ID, modelID, ontologyKey, userText, idemKey string) (
	[]byte, *graphload.Manifest, *llm.Completion, error) {
	schema := []byte(ontologyManifestSchema)
	msgs := []llm.Message{
		{Role: "system", Content: ontologySystemPrompt},
		{Role: "user", Content: userText},
	}
	var lastErr error
	for attempt := 0; attempt <= llm.MaxSchemaRepairAttempts; attempt++ {
		obj, comp, err := h.callLLM(ctx, tenantID, modelID, msgs, schema, repairIdempotencyKey(idemKey, attempt))
		if err != nil {
			return nil, nil, nil, err
		}
		raw, merr := json.Marshal(obj)
		if merr != nil {
			return nil, nil, nil, errors.Wrap(merr, errors.CodeInternal, "序列化本体清单失败")
		}
		var instruction string
		if parsed, perr := graphload.ParseManifest(raw); perr != nil {
			instruction = closureRepairPrompt(perr)
			lastErr = perr
		} else {
			problems, ferr := ontologyPreflight(ctx, tenantID, ontologyKey, parsed)
			if ferr != nil {
				return nil, nil, nil, ferr
			}
			if len(problems) == 0 {
				return raw, parsed, comp, nil
			}
			lastErr = errors.E(errors.CodeValidationFailed,
				"生成的清单未通过 SHACL 预检（用清单自带的 shapes 校验自带的样例实例）："+strings.Join(problems, "；")).
				WithDetails(map[string]any{"problems": problems})
			instruction = preflightRepairPrompt(problems)
		}
		if attempt == llm.MaxSchemaRepairAttempts {
			break
		}
		msgs = append(msgs,
			llm.Message{Role: "assistant", Content: comp.Content},
			llm.Message{Role: "user", Content: instruction})
	}
	return nil, nil, nil, lastErr
}

// maxPreflightDiagnostics 是单次回灌给模型的预检发现项条数上限。
//
// 有上限是因为修复回路把发现项作为消息追加：几十条 finding 会让输入显著变长、
// 更容易撞输出上限（而截断是确定性失败，修不了）。优先列阻断级，够了就停。
const maxPreflightDiagnostics = 8

// ontologyPreflight 用**清单自带的** shapes 校验**清单自带的**样例实例，返回阻断性问题。
//
// 为什么要在生成阶段就查：AI 生成的本体最容易出的问题不是结构，而是**自相矛盾**——
// 模型自己写了 sh:pattern="^[0-9]{6}[0-9]{4}[A-Za-z]{2}[0-9]{3}$"，又给样例实例填了一个
// 匹配不上的取值。这类问题要到人工点「执行验证」才暴露，而那时的结论是 REJECTED、
// 版本不可发布，用户只能手工改数据或整份重新生成。把同一套校验器提前跑一遍、失败就把
// 发现项回灌重生成，产出的 DRAFT 才真正"开箱可校验、可发布"。
//
// 刻意用内存图存储（graphstore.NewMemStore）而不是部署的 GraphStore：此刻版本尚未落库、
// 没有 version_id，也就没有合法的 graphIRI；往共享图库写一份用完就丢的三元组只会污染图库。
// 校验引擎与形状构建逻辑完全复用，差异仅在存储后端。
//
// 这是预检、不是校验：这里**不记录任何校验结论**。校验结论的唯一权威仍是显式的
// /ontologies/{id}/validate（它会重新装载、跑反假绿探针并原子落库 VALID/REJECTED）。
// 预检只负责把"注定会被拒的清单"挡在生成阶段，不代替、也不会掩盖那一步。
func ontologyPreflight(ctx context.Context, tenantID ids.ID, ontologyKey string,
	parsed *graphload.Manifest) ([]string, error) {
	ns, err := graphload.NewNamespace(ontologyKey)
	if err != nil {
		return nil, err
	}
	// 与 /validate 的判定一致：两条约束通道（shapes 骨架 + constraints 业务约束）
	// 全空时没有可执行的约束，"通过"是无意义的。
	if len(parsed.Shapes) == 0 && len(parsed.Constraints) == 0 {
		return []string{"清单没有定义任何可执行约束（shapes 与 constraints 均为空），校验会以「未定义形状」被拒；请为关键类补上 shapes，或把文档中的硬约束写进 constraints"}, nil
	}
	shapes, err := parsed.BuildShapes(ns)
	if err != nil {
		return nil, err
	}
	tbox, err := parsed.TBox(ns)
	if err != nil {
		return nil, err
	}
	abox, err := parsed.ABox(ns)
	if err != nil {
		return nil, err
	}
	mem := graphstore.NewMemStore()
	const graphIRI = "urn:odap:ontgen:preflight"
	if _, err := mem.EnsureGraph(ctx, tenantID, graphIRI, "INSTANCE", ""); err != nil {
		return nil, err
	}
	if _, err := mem.InsertTriples(ctx, tenantID, graphIRI, append(tbox, abox...)); err != nil {
		return nil, err
	}
	// 反假绿（与 /validate 同一条判据）：没有命中 targetClass 的实例时，SHACL 按规范
	// 返回 Conforms=true 且无发现项 —— 与"数据全部合规"在返回值上完全一致。
	focus, err := graphload.CountTypedNodes(ctx, mem, tenantID, graphIRI, parsed.TargetClassIRIs(ns))
	if err != nil {
		return nil, err
	}
	if focus == 0 {
		return []string{"图中没有属于目标类的实例：SHACL 会返回「通过」，但那只是没数据可查；请让 instances 覆盖 shapes 的 target_class（至少各一条）"}, nil
	}
	report, err := shacl.Validate(mem, tenantID, graphIRI, shapes)
	if err != nil {
		return nil, err
	}
	if report.Conforms {
		return nil, nil
	}
	// 阻断级（Violation）排前面：回灌条数有上限，必须优先列最要命的。
	problems := make([]string, 0, len(report.Results))
	for _, res := range report.Results {
		if res.Severity == string(shacl.SeverityViolation) {
			problems = append(problems, describeFinding(res))
		}
	}
	for _, res := range report.Results {
		if res.Severity != string(shacl.SeverityViolation) {
			problems = append(problems, describeFinding(res))
		}
	}
	if len(problems) > maxPreflightDiagnostics {
		problems = problems[:maxPreflightDiagnostics]
	}
	return problems, nil
}

func describeFinding(res shacl.Result) string {
	return res.Component + "（路径 " + res.Path + "，焦点 " + res.FocusNode + "）：" + res.Message
}

// preflightRepairPrompt 把 SHACL 预检发现项转成给模型的修复指令。
//
// 两句重点：①样例取值必须满足你自己写的约束（这正是最常见的自相矛盾）；
// ②宁可放宽约束，也不要保留一个把合法数据判成违规的过严约束。
func preflightRepairPrompt(problems []string) string {
	return "上一轮清单未通过 SHACL 校验（用你自己写的 shapes 校验你写的样例实例）：\n- " +
		strings.Join(problems, "\n- ") +
		"\n请修正后重新输出**完整**的 JSON 对象（结构与字段不变，只改有问题的地方）。" +
		"要点：instances 的属性取值必须满足你在 shapes 里写的约束（pattern、长度、datatype、数值范围、minCount 等）；" +
		"如果你写的 pattern / 长度 / 范围连自己给的样例都过不了，请把它改成文档确实支持的宽松形式，" +
		"不要保留一个把合法数据判成违规的过严约束。"
}

// repairIdempotencyKey 为第 attempt 次（0-based）调用派生不同的幂等键。
//
// 必须不同：幂等/缓存层可能把上一轮的坏结果原样还回来，那样重生成形同虚设。
// 派生只追加 ASCII 后缀，保持键值符合 HTTP 头规范。
func repairIdempotencyKey(base string, attempt int) string {
	if attempt == 0 {
		return base
	}
	return base + "-r" + strconv.Itoa(attempt)
}

// closureRepairPrompt 把闭包校验诊断转成给模型的修复指令（回灌为追加消息）。
func closureRepairPrompt(err error) string {
	msg := "上一轮输出未通过闭包校验，整份清单被拒绝：" + err.Error()
	if ae := errors.As(err); ae != nil && ae.Details != nil {
		if p, ok := ae.Details["pointer"].(string); ok && p != "" {
			msg += "（出错位置：" + p + "）"
		}
	}
	return msg + "\n请修正后重新输出**完整**的 JSON 对象（结构与字段不变，只改有问题的地方）。" +
		"要点：①每条关系必须从其 from_class 对应的实例出发（主体实例的类 = from_class），" +
		"且取值目标实例的类必须等于该关系的 to_class；若你在反向使用某条关系（方向写反），" +
		"请交换 relations 里的 from_class/to_class 声明，或把关系放到正确一侧的实例上；" +
		"②每个关系取值都必须是 instances 里真实存在的 id，" +
		"若某条关系在 instances 里找不到匹配的目标实例，就补上该目标实例，或干脆去掉这条关系，" +
		"绝不要保留指向不存在实例的引用。"
}

// parseJSONObject 从模型文本中解析 JSON 对象（容忍 ```json 代码围栏与前后噪声）。
func parseJSONObject(s string) (map[string]any, error) {
	t := strings.TrimSpace(s)
	if i := strings.Index(t, "```"); i >= 0 {
		rest := t[i+3:]
		rest = strings.TrimPrefix(rest, "json")
		if j := strings.Index(rest, "```"); j >= 0 {
			rest = rest[:j]
		}
		t = strings.TrimSpace(rest)
	}
	// 退一步：截取第一个 '{' 到最后一个 '}'。
	if l, r := strings.Index(t, "{"), strings.LastIndex(t, "}"); l >= 0 && r > l {
		t = t[l : r+1]
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(t), &obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// completionMeta 把模型调用元数据（可观测性）投影为响应字段。
func completionMeta(comp *llm.Completion) map[string]any {
	if comp == nil {
		return map[string]any{}
	}
	return map[string]any{
		"model_revision": comp.ModelRevision,
		"input_tokens":   comp.InputTokens,
		"output_tokens":  comp.OutputTokens,
		"cache_hit":      comp.CacheHit,
		"cache_layer":    comp.CacheLayer,
		"finish_reason":  comp.FinishReason,
	}
}

// clip 截断字符串用于错误详情（不影响业务数据）。
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// corpusFile 是参与生成的一份源文档。
type corpusFile struct {
	Path  string // 相对 DocsRoot 的路径
	Kind  string
	Chars int
}

// docCorpus 是一次文档语料读取的结果。
type docCorpus struct {
	Files     []corpusFile
	Text      string
	Chars     int
	Truncated bool
	Warnings  []string
}

// resolveDocPath 把请求中的相对路径解析为 DocsRoot 之内的绝对路径（禁止越界）。
func (h *Handler) resolveDocPath(rel string) (string, string, error) {
	clean := filepath.Clean(strings.TrimSpace(rel))
	if clean == "" || clean == "." {
		return "", "", errors.E(errors.CodeInvalidArgument, "source_path 不能为空")
	}
	if filepath.IsAbs(clean) {
		return "", "", errors.E(errors.CodeInvalidArgument, "source_path 必须是相对路径（相对制度文档根目录）")
	}
	root, err := filepath.Abs(h.docsRoot())
	if err != nil {
		return "", "", errors.Wrap(err, errors.CodeInternal, "制度文档根目录不可解析")
	}
	full := filepath.Join(root, clean)
	relative, err := filepath.Rel(root, full)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", errors.E(errors.CodeInvalidArgument, "source_path 越出制度文档根目录")
	}
	return full, filepath.ToSlash(relative), nil
}

// supportedDocExt 是参与语料抽取的扩展名。
var supportedDocExt = map[string]bool{
	".docx": true, ".xlsx": true, ".doc": true,
	".txt": true, ".md": true, ".csv": true, ".json": true,
}

// supportedUploadExt 是允许**上传**的制度文档扩展名（word / excel / md）。
//
// 与 supportedDocExt 刻意不同：需求限定为 word、excel 或 MD，故不含
// .txt/.csv/.json。.xls 不在 docingest 的支持范围内，接收后必然在抽取阶段
// 失败，因此前置拒绝，避免"上传成功但生成失败"的错位体验。
var supportedUploadExt = map[string]bool{
	".docx": true, ".doc": true, ".xlsx": true, ".md": true,
}

// clampMaxChars 归一化源字符上限（<=0 取默认，超过上限取下限）。
func clampMaxChars(maxChars int) int {
	if maxChars <= 0 {
		return defaultMaxSourceChars
	}
	if maxChars > maxSourceCharsLimit {
		return maxSourceCharsLimit
	}
	return maxChars
}

// sanitizeUploadName 校验上传文件类型并净化文件名（取 Base、统一分隔符）。
//
// 单独成一步：类型校验属于**请求形态校验**，必须在解析请求体时立刻完成，
// 不该等到"网关是否可用"之后才判定；净化（取 Base、统一分隔符）避免路径穿越。
func sanitizeUploadName(fh *multipart.FileHeader) (string, error) {
	orig := filepath.Base(strings.ReplaceAll(fh.Filename, "\\", "/"))
	ext := strings.ToLower(filepath.Ext(orig))
	if !supportedUploadExt[ext] {
		return "", errors.Ef(errors.CodeInvalidArgument,
			"不支持的文件类型 %s（仅支持 .docx/.doc/.xlsx/.md）", ext)
	}
	return orig, nil
}

// persistUpload 把一个（已经过类型校验的）上传文件落盘到 uploadsRoot 下，返回绝对路径。
//
// 名字 = <uuid>_<orig>：uuid 前缀避免同名覆盖与目录内碰撞。
// 刻意放在网关可用性校验之后调用——被拒的请求不该在磁盘上留下孤儿文件。
func (h *Handler) persistUpload(fh *multipart.FileHeader, orig string) (full string, err error) {
	root, err := filepath.Abs(h.uploadsRoot())
	if err != nil {
		return "", errors.Wrap(err, errors.CodeInternal, "上传目录不可解析")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", errors.Wrap(err, errors.CodeInternal, "上传目录不可创建")
	}
	full = filepath.Join(root, ids.New().String()+"_"+orig)
	dst, err := os.Create(full)
	if err != nil {
		return "", errors.Wrap(err, errors.CodeInternal, "上传文件写入失败")
	}
	defer dst.Close()
	src, err := fh.Open()
	if err != nil {
		return "", errors.Wrap(err, errors.CodeInternal, "上传文件读取失败")
	}
	defer src.Close()
	if _, err := io.Copy(dst, src); err != nil {
		return "", errors.Wrap(err, errors.CodeInternal, "上传文件保存失败")
	}
	return full, nil
}

// readCorpus 读取 source_path（文件或目录），抽取纯文本并按 maxChars 截断。
func (h *Handler) readCorpus(sourcePath string, maxChars int) (*docCorpus, error) {
	full, rel, err := h.resolveDocPath(sourcePath)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(full)
	if err != nil {
		return nil, errors.Ef(errors.CodeNotFound, "制度文档不存在：%s", rel)
	}

	paths := []string{full}
	rels := []string{rel}
	if info.IsDir() {
		paths, rels = nil, nil
		walkErr := filepath.WalkDir(full, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if supportedDocExt[strings.ToLower(filepath.Ext(p))] {
				r, rerr := filepath.Rel(filepath.Dir(full), p)
				if rerr != nil {
					return rerr
				}
				paths = append(paths, p)
				rels = append(rels, filepath.ToSlash(r))
			}
			return nil
		})
		if walkErr != nil {
			return nil, errors.Wrap(walkErr, errors.CodeInternal, "遍历制度文档目录失败")
		}
		sort.Strings(paths)
		sort.Strings(rels)
		if len(paths) == 0 {
			return nil, errors.Ef(errors.CodeNotFound, "目录内没有可解析的制度文档：%s", rel)
		}
	}

	return h.corpusFromPaths(paths, rels, maxChars)
}

// corpusFromPaths 由既定的绝对路径/展示名集合抽取语料，并按 maxChars 截断。
//
// 服务端路径（readCorpus）与上传文件（saveUpload → 这里）共用同一条
// 「抽取 → 拼接 → 截断」链路，避免两条实现漂移。
func (h *Handler) corpusFromPaths(paths, rels []string, maxChars int) (*docCorpus, error) {
	maxChars = clampMaxChars(maxChars)

	corpus := &docCorpus{}
	var b strings.Builder
	for i, p := range paths {
		doc, perr := docingest.Extract(p)
		name := rels[i]
		if perr != nil {
			corpus.Warnings = append(corpus.Warnings,
				"跳过无法解析的文件 "+name+"："+perr.Error())
			continue
		}
		text := strings.TrimSpace(doc.Text)
		corpus.Files = append(corpus.Files, corpusFile{Path: name, Kind: doc.Kind, Chars: len([]rune(text))})
		corpus.Warnings = append(corpus.Warnings, doc.Warnings...)
		if text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("===== 文档：")
		b.WriteString(name)
		b.WriteString(" =====\n")
		b.WriteString(text)
	}
	if len(corpus.Files) == 0 {
		return nil, errors.E(errors.CodeUnavailable,
			"所有源文件均无法解析为文本（见 warnings），无可用于生成的语料").
			WithDetails(map[string]any{"warnings": corpus.Warnings})
	}
	corpus.Text = b.String()
	corpus.Chars = len([]rune(corpus.Text))
	if corpus.Chars > maxChars {
		corpus.Text = string([]rune(corpus.Text)[:maxChars])
		corpus.Truncated = true
	}
	return corpus, nil
}

// deriveKey 由源路径推导一个 ASCII 安全的业务键；无法推导时用路径摘要保证稳定唯一。
func deriveKey(sourcePath string) string {
	base := filepath.Base(filepath.Clean(sourcePath))
	base = strings.TrimSuffix(base, filepath.Ext(base))
	var s strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(base) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			s.WriteRune(r)
			prevDash = false
		} else if !prevDash && s.Len() > 0 {
			s.WriteByte('-')
			prevDash = true
		}
	}
	key := strings.Trim(s.String(), "-")
	if key != "" {
		return key
	}
	sum := sha256.Sum256([]byte(filepath.ToSlash(sourcePath)))
	return "auto-" + hex.EncodeToString(sum[:4])
}

// ---------------------------------------------------------------------------
// 本体自动生成
// ---------------------------------------------------------------------------

// ontologyManifestSchema 是要求模型输出的本体清单结构（JSON Schema）。
//
// 形状与实例都用**结构化 JSON** 表达，不要求模型输出 Turtle/OWL 文本：
//   - 下游可执行形式本就是结构化的（shacl.Validate 收 []Shape、
//     graphstore.InsertTriples 收 []Triple），仓内也没有 RDF 解析器；
//   - 让模型手工拼 RDF 文本，等于把"前缀声明、IRI 转义、字面量标签、集合展开"
//     这些纯语法负担交给它，错误的种类与数量都会显著上升，而且出错后只能靠
//     解析器报错定位。用结构化 JSON 时，JSON Schema 直接把约束写在契约里。
const ontologyManifestSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["summary", "classes", "shapes", "instances"],
  "properties": {
    "summary": {"type": "string"},
    "classes": {
      "type": "array",
      "maxItems": 8,
      "items": {
        "type": "object", "additionalProperties": false,
        "required": ["name", "label", "description"],
        "properties": {
          "name": {"type": "string"},
          "label": {"type": "string"},
          "description": {"type": "string"},
          "attributes": {
            "type": "array",
            "maxItems": 8,
            "items": {
              "type": "object", "additionalProperties": false,
              "required": ["name", "type"],
              "properties": {
                "name": {"type": "string"},
                "type": {"type": "string",
                  "enum": ["string", "number", "integer", "boolean", "date", "datetime", "enum"]},
                "description": {"type": "string"},
                "required": {"type": "boolean"}
              }
            }
          }
        }
      }
    },
    "relations": {
      "type": "array",
      "maxItems": 12,
      "items": {
        "type": "object", "additionalProperties": false,
        "required": ["name", "from_class", "to_class"],
        "properties": {
          "name": {"type": "string"},
          "from_class": {"type": "string"},
          "to_class": {"type": "string"},
          "cardinality": {"type": "string"},
          "description": {"type": "string"}
        }
      }
    },
    "enumerations": {
      "type": "array",
      "maxItems": 6,
      "items": {
        "type": "object", "additionalProperties": false,
        "required": ["name", "values"],
        "properties": {
          "name": {"type": "string"},
          "values": {"type": "array", "maxItems": 12, "items": {"type": "string"}}
        }
      }
    },
    "shapes": {
      "type": "array",
      "maxItems": 8,
      "items": {
        "type": "object", "additionalProperties": false,
        "required": ["id", "target_class", "properties"],
        "properties": {
          "id": {"type": "string"},
          "target_class": {"type": "string"},
          "severity": {"type": "string", "enum": ["Violation", "Warning", "Info"]},
          "properties": {
            "type": "array",
            "maxItems": 8,
            "items": {
              "type": "object", "additionalProperties": false,
              "required": ["path"],
              "properties": {
                "path": {"type": "string"},
                "min_count": {"type": "integer"},
                "max_count": {"type": "integer"},
                "datatype": {"type": "string",
                  "enum": ["string", "decimal", "integer", "boolean", "date", "dateTime"]},
                "class": {"type": "string"},
                "node_kind": {"type": "string", "enum": ["IRI", "BlankNode", "Literal"]},
                "in": {"type": "array", "items": {"type": "string"}},
                "min_inclusive": {"type": "number"},
                "max_inclusive": {"type": "number"},
                "min_exclusive": {"type": "number"},
                "max_exclusive": {"type": "number"},
                "pattern": {"type": "string"},
                "min_length": {"type": "integer"},
                "max_length": {"type": "integer"},
                "severity": {"type": "string", "enum": ["Violation", "Warning", "Info"]}
              }
            }
          }
        }
      }
    },
    "constraints": {
      "type": "array",
      "maxItems": 24,
      "items": {
        "type": "object", "additionalProperties": false,
        "required": ["class", "path"],
        "properties": {
          "class": {"type": "string"},
          "path": {"type": "string"},
          "severity": {"type": "string", "enum": ["Violation", "Warning", "Info"]},
          "min_count": {"type": "integer"},
          "max_count": {"type": "integer"},
          "in": {"type": "array", "items": {"type": "string"}},
          "min_inclusive": {"type": "number"},
          "max_inclusive": {"type": "number"},
          "min_exclusive": {"type": "number"},
          "max_exclusive": {"type": "number"},
          "pattern": {"type": "string"},
          "min_length": {"type": "integer"},
          "max_length": {"type": "integer"}
        }
      }
    },
    "instances": {
      "type": "array",
      "maxItems": 10,
      "items": {
        "type": "object", "additionalProperties": false,
        "required": ["class", "id", "properties"],
        "properties": {
          "class": {"type": "string"},
          "id": {"type": "string"},
          "properties": {"type": "object", "additionalProperties": true}
        }
      }
    }
  }
}`

const ontologySystemPrompt = `你是企业本体建模与 SHACL 约束设计专家。用户会给出制度文档正文，请从中抽取业务本体，并为它写出一套**可执行的约束**与**样例实例**。

════════════════════════════════════════
🔴 铁律速查（最高频致命错误，先扫一遍再动手）
════════════════════════════════════════
① 只提取文档中实际出现的内容。禁止编造、假设、或从示例中复制任何类/属性/约束。文档里没有的概念绝不能出现在输出中。
② 每一项数量上限同时写在 JSON Schema 的 maxItems 里，超限整份作废。宁可只覆盖最重要的对象与约束，也不要为"齐全"超限；少而准优于多而杂。
③ relations 是**顶层数组**（与 classes 平级），不是写在某个类里的字段。
④ 关系取值必须是 instances 里真实存在的 id，且目标实例的 class 必须等于该关系的 to_class；关系的主体实例必须属于 from_class（方向写反 = 整份被拒）。
⑤ 属性取值必须与 attributes 声明的 type 相符（数值写数字，不要写带单位的字符串）。
⑥ 样例取值必须满足你自己在 shapes/constraints 里写的约束——你没有机会事后改数据。若某条约束连你自己的样例都过不了，就把它改成文档确实支持的宽松形式。
⑦ 只使用 Schema 里声明过的字段，多写一个字段整份清单作废。attributes/relations/enumerations/constraints/instances 的条目一律不写 label。
⑧ 输出必须是符合给定 JSON Schema 的**单个 JSON 对象**，第一个字符是 {，最后一个字符是 }，不要任何解释性文字。

════════════════════════════════════════
🔴 阶段零：文档盘点——提取之前必须先完成
════════════════════════════════════════
在开始提取之前，先通读全文并在内部完成盘点（防止"处理了前几段就输出、漏掉后半份文档"）：
1. 列出文档的章节/制度条目，逐节统计其中出现的业务对象与硬性规则数量（逐条数，不要估算）；
2. 确认每个制度文件/章节里有多少条"数值区间 / 枚举 / 长度 / 必填 / 正则"类硬约束；
3. 提取过程中随时对照盘点清单：每完成一节标记已完成，输出前核对"盘点总数 == 实际提取数"。

════════════════════════════════════════
🔴 阶段零.5：语义融合草稿——输出 JSON 前必须先完成
════════════════════════════════════════
无论输入是单一制度还是多份文档，都必须先在内部产出一份融合草稿，再据此落笔；禁止跳过融合、凭直觉边读边抽。草稿必须包含：
1. 实体清单：每个 class 对应文档的哪些章节；同义实体是否归并（"报销单"与"费用报销单"是同一个类还是两个）；
2. 属性消歧：跨类同名字段（如多处出现的"名称""编号"）各自属于哪个 class；
3. 关系清单：每对类之间文档指明了什么关联，谁是 from 谁是 to；
4. 对照检查：草稿里的每个 class/属性/关系/约束，是否都能在文档中找到依据（无依据即删）。

════════════════════════════════════════
字段要求
════════════════════════════════════════
1. classes：覆盖文档中出现的关键业务对象（如单据、事项、角色、标准），label 用中文，name 用英文；
   最多 8 个，优先选取文档中反复出现、构成单据或事项主干的对象，description 用一句短语（不超过 30 字）。
2. attributes：该类的关键字段，type 取 string/number/integer/boolean/date/datetime/enum 之一，
   每个类最多 8 个（只保留关键字段）、description 用一句短语（不超过 30 字）；
   required 表示文档是否要求该字段必填。**不要把所有字段都写成必填**：只对文档明确要求、
   缺失即违规的字段设 required=true，否则会给合规数据产生大量无意义告警，让人不再信任门禁。
3. relations：顶层数组；from_class/to_class 必须出现在 classes.name 中；最多 12 条。
4. enumerations：覆盖文档中明确列举的取值集合（如费用类别、单据状态），最多 6 个，每个最多 12 个取值。
5. shapes：你为该本体写下的 SHACL **形状骨架**，每个形状针对一个类（target_class），
   每个形状必须给出唯一 id（英文与连字符，如 expense-claim-shape；不得以 auto- 开头，该前缀保留给系统），
   其 properties[].path 只能是「该类的某个属性名」或「relations 里的某个关系名」，不许写 IRI。
   每个类最多一个形状、共不超过 8 个；每个形状最多 8 条 properties（挑最关键、最值得约束的字段）。
   可用约束：min_count / max_count（出现次数）、datatype（string/decimal/integer/boolean/date/dateTime）、
   class（取值必须是某个类的实例，用于关系）、node_kind（IRI/Literal）、in（取值必须属于给定集合）、
   min_inclusive / max_inclusive / min_exclusive / max_exclusive（数值区间）、pattern（正则，作用于字段文本）、
   min_length / max_length（文本长度）、severity（Violation 阻断发布，Warning 只提示）。
6. constraints：**业务硬约束通道**——把文档中逐字可验证的硬约束单独列出（每条 {class, path, ...}）。
   path 是该类的属性名或关系名；可用字段：min_count / max_count / in / min_inclusive / max_inclusive /
   min_exclusive / max_exclusive / pattern / min_length / max_length / severity，最多 24 条。
   硬性规则：
   - 只写文档中**明确、可验证**的约束（文档写了"金额不超过 100 万元"才写 max_inclusive=1000000）；
     模糊表述（"金额应合理"）不要写——写出来只会产生无法追溯的违规；
   - 类型必须相容：数值区间（min/max_inclusive 等）只能用于 number/integer 属性；
     pattern / min_length / max_length / in 只能用于文本属性；关系只接受 min_count/max_count；
   - 数值属性不要用 pattern 或 in（用数值区间）；文本属性不要用数值区间；
   - min_count/max_count 与 shapes 里的重复没关系：约束通道的值会覆盖形状骨架的同名字段，
     当文档对某字段有比骨架更强的要求时，把更强的值写在 constraints 里。
7. instances：用于验证上面这套约束的**样例实例**，2～10 条（不超过 10 条）：
   class 必须是 classes 中的类；id 用英文与连字符；properties 的键只能是该类的属性名或关系名；
   关系取值写目标实例的 id（该 id 必须也出现在 instances 里，且其 class 必须等于该关系的 to_class）；
   属性取值必须与 attributes 中声明的 type 相符。这是**闭包校验**（引用不存在 = 整份被拒），
   也是**自洽校验**（样例必须满足你自己的 shapes 与 constraints）。

════════════════════════════════════════
🔴 输出前强制自检（逐条确认后再输出）
════════════════════════════════════════
1. 盘点核对：阶段零盘点的每一条对象与硬约束都已提取，无中间截断、无整段遗漏；
2. 关系闭包：每条关系的主体实例属于 from_class、目标实例存在且属于 to_class；
3. 自洽：每个样例取值都满足你自己写的 shapes 与 constraints（pattern/长度/区间/枚举/minCount）；
4. 约束可满足：没有 min>max、空数值区间、类型不相容的约束（这类约束会在解析期被直接拒绝）；
5. 字段纪律：没有多写任何未声明字段；条目数都在上限内；
6. 输出形态：单个 JSON 对象，无解释文字、无 markdown 围栏。

输出的骨架固定如下（键名必须与此完全一致：不能多一个、不能少一个，只把取值换成文档中的真实内容。
注意 attributes 的键只有 name/type/description/required，relations 是顶层数组、其元素没有 label 字段，
constraints 可省略——但文档中存在明确硬约束时强烈建议输出）：
{"summary":"…","classes":[{"name":"…","label":"…","description":"…","attributes":[{"name":"…","type":"string","description":"…","required":true}]}],"relations":[{"name":"…","from_class":"…","to_class":"…","cardinality":"1..*","description":"…"}],"enumerations":[{"name":"…","values":["…"]}],"shapes":[{"id":"…","target_class":"…","severity":"Violation","properties":[{"path":"…","min_count":1,"datatype":"string"}]}],"constraints":[{"class":"…","path":"…","max_inclusive":1000000}],"instances":[{"class":"…","id":"…","properties":{"…":"…"}}]}`

// GenerateOntologyRequest 是 POST /api/v1/ontologies/generate 的请求体。
//
// 两种提交形式（**二选一**，同时提供或都不提供一律 400）：
//   - application/json：字段 source_path（相对制度文档根目录的路径），必填；
//   - multipart/form-data：字段 file（本地文档，.docx/.doc/.xlsx/.md）。
//
// 字段名在两种形式下保持一致，便于同一套解析逻辑复用。
type GenerateOntologyRequest struct {
	SourcePath     string `json:"source_path"`
	OntologyKey    string `json:"ontology_key,omitempty"`
	Name           string `json:"name,omitempty"`
	Domain         string `json:"domain,omitempty"`
	Granularity    string `json:"granularity,omitempty"`
	Description    string `json:"description,omitempty"`
	CategoryID     string `json:"category_id,omitempty"`
	ModelID        string `json:"model_id,omitempty"`
	MaxSourceChars int    `json:"max_source_chars,omitempty"`
}

// genRequestFromForm 从 multipart 表单读生成参数（字段名与 JSON 键一致）。
//
// 表单字段一律是字符串，因此 max_source_chars 需要显式解析；非法值直接
// 400 而非静默忽略（诚实性约束：不把无效输入悄悄当作 0）。
func genRequestFromForm(r *http.Request) (GenerateOntologyRequest, error) {
	req := GenerateOntologyRequest{
		SourcePath:  strings.TrimSpace(r.FormValue("source_path")),
		OntologyKey: strings.TrimSpace(r.FormValue("ontology_key")),
		Name:        strings.TrimSpace(r.FormValue("name")),
		Domain:      strings.TrimSpace(r.FormValue("domain")),
		Granularity: strings.TrimSpace(r.FormValue("granularity")),
		Description: r.FormValue("description"),
		CategoryID:  strings.TrimSpace(r.FormValue("category_id")),
		ModelID:     strings.TrimSpace(r.FormValue("model_id")),
	}
	if raw := strings.TrimSpace(r.FormValue("max_source_chars")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return req, errors.E(errors.CodeInvalidArgument, "max_source_chars 必须是非负整数")
		}
		req.MaxSourceChars = n
	}
	return req, nil
}

// GenerateOntology 处理 POST /api/v1/ontologies/generate。
func (h *Handler) GenerateOntology(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}

	// 按 Content-Type 分流：multipart 走上传（file），其余走 JSON（source_path）。
	//
	// 请求形态校验（表单不合法 / 扩展名不支持 / file 与 source_path 二选一）刻意
	// 排在「模型网关是否装配」之前：畸形请求无论后端是否具备模型能力，都应如实回
	// 400，而不是被 503 掩盖成"依赖缺失"。文件落盘（persistUpload）则相反，放在
	// 网关校验之后——被拒的请求不该在磁盘上留下孤儿文件。
	var req GenerateOntologyRequest
	var uploadFile *multipart.FileHeader
	var uploadName string
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			h.fail(w, r, errors.E(errors.CodeInvalidArgument,
				"上传表单不合法或请求体过大（上限 32 MiB）"))
			return
		}
		req, err = genRequestFromForm(r)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		if files := r.MultipartForm.File["file"]; len(files) > 0 {
			uploadFile = files[0]
			uploadName, err = sanitizeUploadName(uploadFile)
			if err != nil {
				h.fail(w, r, err)
				return
			}
		}
	} else {
		if err := decodeJSON(r, &req); err != nil {
			h.fail(w, r, err)
			return
		}
	}
	echoIdempotency(w, r)

	// 二选一强校验（fail-closed，绝不静默取其一）。
	hasFile := uploadFile != nil
	hasPath := strings.TrimSpace(req.SourcePath) != ""
	switch {
	case hasFile && hasPath:
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "不能同时提供上传文件与 source_path"))
		return
	case !hasFile && !hasPath:
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "必须提供上传文件（file）或 source_path"))
		return
	}

	// 业务领域：由所选的业务领域分类派生（不允许自由文本与字典漂移）。
	domain := strings.TrimSpace(req.Domain)
	if cid := strings.TrimSpace(req.CategoryID); cid != "" {
		catID, perr := ids.Parse(cid)
		if perr != nil {
			h.fail(w, r, errors.E(errors.CodeInvalidArgument, "category_id 不是合法 UUID"))
			return
		}
		item, gerr := h.deps.Store.GetOntologyCategory(r.Context(), id.TenantID, catID)
		if gerr != nil {
			h.fail(w, r, gerr) // 分类不存在 → 404（不静默当作未归类）
			return
		}
		code, _ := item["category_code"].(string)
		if domain != "" && domain != code {
			h.fail(w, r, errors.Ef(errors.CodeInvalidArgument,
				"业务领域 %q 与所选业务领域分类的编码 %q 不一致", domain, code))
			return
		}
		domain = code
	}

	if _, err := h.llmGateway(); err != nil {
		h.fail(w, r, err)
		return
	}

	// 语料：两种来源共用下游链路。上传文件此刻才落盘（此前的校验均已通过）。
	var corpus *docCorpus
	sourceRef := strings.TrimSpace(req.SourcePath)
	if hasFile {
		sourceRef = uploadName
		uploadPath, perr := h.persistUpload(uploadFile, uploadName)
		if perr != nil {
			h.fail(w, r, perr)
			return
		}
		corpus, err = h.corpusFromPaths([]string{uploadPath}, []string{uploadName}, req.MaxSourceChars)
	} else {
		corpus, err = h.readCorpus(req.SourcePath, req.MaxSourceChars)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}

	ontologyKey := strings.TrimSpace(req.OntologyKey)
	if ontologyKey == "" {
		ontologyKey = deriveKey(sourceRef)
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = ontologyKey
	}
	if domain == "" {
		domain = "general"
	}
	granularity := strings.TrimSpace(req.Granularity)
	if granularity == "" {
		granularity = "DOMAIN"
	}

	// 结构与闭包校验统一交给 graphload.ParseManifest（见 generateOntologyManifest），
	// 且闭包失败会先做有界重生成，避免"模型一处引用没对上"就直接 422。
	manifest, parsed, comp, err := h.generateOntologyManifest(r.Context(), id.TenantID, req.ModelID,
		ontologyKey, "制度文档如下，请生成本体清单：\n"+corpus.Text, idempotencyKey(r))
	if err != nil {
		// 只有"模型产出了内容但闭包校验没过"才属业务拒绝；网关不可用/超时等
		// 基础设施失败不在此列（与改动前一致）。
		if errors.As(err).Code == errors.CodeValidationFailed {
			h.recordAudit(r, id, "OntologyGenerationRejected", "ontology.generate", "DENIED", "ontology", ontologyKey, ids.ID{},
				map[string]any{"source_path": sourceRef, "reason": "manifest_invalid"})
		}
		h.fail(w, r, err)
		return
	}

	out, err := h.deps.Store.CreateOntology(r.Context(), store.CreateOntologyInput{
		OntologyKey: ontologyKey,
		Name:        name,
		Domain:      domain,
		Granularity: granularity,
		Description: req.Description,
		Manifest:    manifest,
		CategoryID:  strings.TrimSpace(req.CategoryID),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}

	h.recordAudit(r, id, "OntologyGenerated", "ontology.generate", "SUCCESS", "ontology",
		stringify(out["id"]), ids.ID{}, map[string]any{
			"ontology_key":    ontologyKey,
			"source_path":     sourceRef,
			"uploaded_file":   uploadName, // 仅上传模式非空；路径模式为空串
			"source_files":    corpus.Files,
			"source_chars":    corpus.Chars,
			"truncated":       corpus.Truncated,
			"shacl_shapes":    len(parsed.Shapes),
			"constraints":     len(parsed.Constraints),
			"instances":       parsed.InstanceCount(),
			"model":           modelRevisionOf(comp),
			"idempotency_key": idempotencyKey(r),
		})
	h.publish(id.TenantID.String(), "OntologyGenerated", sse.Important,
		map[string]any{"ontology_id": stringify(out["id"]), "ontology_key": ontologyKey})

	out["generation"] = map[string]any{
		"source_path":  sourceRef,
		"source_files": corpus.Files,
		"source_chars": corpus.Chars,
		"truncated":    corpus.Truncated,
		"warnings":     corpus.Warnings,
		"model":        completionMeta(comp),
		// 明确回报"生成了几条形状、几条业务约束、几条样例实例"：这是调用方判断
		// "这份本体是否真的自带可执行规则"的唯一依据——两条约束通道全空的本体，
		// 后续校验会以 422 明示"未定义形状"，而不是悄悄报通过。
		"shacl_shapes": len(parsed.Shapes),
		"constraints":  len(parsed.Constraints),
		"instances":    parsed.InstanceCount(),
	}
	h.writeJSON(w, http.StatusCreated, out)
}

// modelRevisionOf 取模型修订号（审计用；缺失返回空串）。
func modelRevisionOf(comp *llm.Completion) string {
	if comp == nil {
		return ""
	}
	return comp.ModelRevision
}
