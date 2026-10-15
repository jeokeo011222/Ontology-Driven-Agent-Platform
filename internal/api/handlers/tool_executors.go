package handlers

// 工具真实执行器（需求 3：工具管理全部按实际业务执行，严禁 mock）。
//
// 派发模型：每个工具版本的 execution_config.adapter 决定由哪个执行器执行，
// 执行器一律对接**平台真实能力**——ERP Port（mockerp 演示 ERP / 租户 NC65 连接）、
// 文档提取引擎（docingest）、SHACL 引擎、本体清单装载器、受治理的模型网关、
// 审计哈希链。没有真实后端的适配器（mock-ocr / mock-budget）与依赖 Run 上下文
// 的能力（evidence）fail-closed 返回 UNAVAILABLE 并说明原因，绝不伪造输出。
//
// execution_config 的字段名与 datasets/*/tools.json 严格一致：
// object_type / operation / index / formats / fail_closed / requires_approval /
// idempotency_key_field。
//
// 事务纪律：执行器不做任何数据库事务管理——状态推进由 InvokeTool /
// ApproveToolInvocation 以短事务 CAS 完成（见 store/tool_invocations.go），
// 真实远端调用一律发生在事务之外。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"odap/internal/api/store"
	"odap/internal/docingest"
	"odap/internal/erp"
	"odap/internal/llm"
	"odap/internal/ontology/graphload"
	"odap/internal/ontology/shacl"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/tenantctx"
)

// executionConfig 是 tool_versions.execution_config 的投影（datasets/*/tools.json）。
type executionConfig struct {
	Adapter             string   `json:"adapter"`               // 执行器派发键
	ObjectType          string   `json:"object_type"`           // ERP 对象类型（mock-erp / nc65 / mock-budget）
	Operation           string   `json:"operation"`             // 工具内操作（get/query/update/search/extract/...）
	Index               string   `json:"index"`                 // 检索索引名（mock-retriever）
	Formats             []string `json:"formats"`               // 允许的文档扩展名（docingest）
	FailClosed          bool     `json:"fail_closed"`           // 远端失败时不降级（nc65）
	RequiresApproval    bool     `json:"requires_approval"`     // 外部写需审批（mock-erp update）
	IdempotencyKeyField string   `json:"idempotency_key_field"` // 幂等键取值字段
	// ---- script 适配器（WASM 脚本工具，见 tool_executors_script.go）----
	Language       string `json:"language"`         // 脚本语言，仅支持 "wasm"
	WasmB64        string `json:"wasm_b64"`         // base64 编码的 .wasm 模块
	TimeoutMs      int    `json:"timeout_ms"`       // 运行超时毫秒（缺省 30000，上限 300000）
	MaxOutputBytes int    `json:"max_output_bytes"` // stdout 上限字节（缺省 1MB，上限 8MB）
}

// parseExecutionConfig 解析执行契约；缺失或非法直接报错（fail-closed：
// 没有执行契约的工具不可能被真实执行，静默跳过只会产出假成功）。
func parseExecutionConfig(raw json.RawMessage) (executionConfig, error) {
	var cfg executionConfig
	if len(raw) == 0 {
		return cfg, errors.E(errors.CodeUnavailable,
			"该工具版本没有 execution_config（执行契约）：无法确定由哪个执行器执行，拒绝调用")
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, errors.E(errors.CodeInternal, "工具 execution_config 不是合法 JSON："+err.Error())
	}
	if cfg.Adapter == "" {
		return cfg, errors.E(errors.CodeUnavailable, "工具 execution_config 缺少 adapter 字段，无法派发执行器")
	}
	return cfg, nil
}

// executeToolInvocation 执行一次工具调用并返回真实输出。
//
// 参数：invID 用于 ERP 写操作的幂等/审计关联；args 已通过 input_schema 校验；
// approval 凭据（__approval_id / __authorization_decision_id）仅由审批确认链路注入。
func (h *Handler) executeToolInvocation(
	r *http.Request,
	id tenantctx.Identity,
	rec *store.ToolVersionRecord,
	invID string,
	args map[string]any,
) (map[string]any, error) {
	cfg, err := parseExecutionConfig(rec.ExecutionConfig)
	if err != nil {
		return nil, err
	}
	ctx := r.Context()

	switch cfg.Adapter {
	case "mock-erp", "nc65":
		return h.execErpTool(r, id, cfg, invID, args)
	case "docingest":
		return h.execDocingestTool(cfg, args)
	case "shacl":
		return h.execShaclTool(ctx, id, args)
	case "graphload":
		return h.execGraphloadTool(args)
	case "llm-gateway":
		return h.execLLMTool(ctx, id, invID, args)
	case "mock-retriever":
		return h.execRetrieverTool(cfg, args)
	case "ledger", "audit-chain":
		return h.execAuditTool(r, id, rec, invID, args)
	case "script":
		// WASM 脚本工具：wazero 内纯计算执行，无任何 host 能力（文件/网络），
		// 固定只读；登记侧已强制 effect_class=READ（tool_executors_script.go）。
		return h.execScriptTool(ctx, cfg, args)
	case "mock-ocr":
		return nil, errors.Ef(errors.CodeUnavailable,
			"工具 %s 需要 OCR 后端（adapter=mock-ocr），当前平台未接入真实 OCR 服务；"+
				"接入真实 OCR 适配器后此工具才可执行", rec.ToolKey)
	case "mock-budget":
		return nil, errors.Ef(errors.CodeUnavailable,
			"工具 %s 需要预算系统数据源（adapter=mock-budget），当前平台未接入真实预算系统；"+
				"接入后此工具才可执行", rec.ToolKey)
	case "evidence":
		return nil, errors.E(errors.CodeUnavailable,
			"证据锚定（adapter=evidence）依赖 Run 上下文（证据包按 Run 组织），"+
				"工具页测试调用没有 Run 归属，无法执行；请通过业务任务链路产生证据包")
	default:
		return nil, errors.Ef(errors.CodeUnavailable,
			"未知的工具执行适配器 %q：平台没有该执行器，拒绝伪造执行结果", cfg.Adapter)
	}
}

// execErpTool 经统一 ERP Port 真实读取/查询/写入 ERP。
//
// mock-erp → env 装配的演示 ERP（mockerp 容器是真实 HTTP 服务）；
// nc65     → 租户 DB 连接（erp_connections）优先 → env 传统装配显式回落。
// object_type / operation 优先取 args（nc65 工具由调用方指定），回落执行契约。
func (h *Handler) execErpTool(
	r *http.Request,
	id tenantctx.Identity,
	cfg executionConfig,
	invID string,
	args map[string]any,
) (map[string]any, error) {
	objectType := firstNonEmpty(strArg(args, "object_type"), cfg.ObjectType)
	if objectType == "" {
		return nil, errors.E(errors.CodeUnavailable,
			"工具执行契约缺少 object_type（ERP 对象类型），且调用参数未提供，无法派发 ERP 调用")
	}
	port, connID, source, err := h.resolveErpPortForAdapter(r, cfg.Adapter)
	if err != nil {
		return nil, err
	}
	tenantID := id.TenantID.String()
	op := firstNonEmpty(strArg(args, "operation"), cfg.Operation)

	switch op {
	case "get":
		objectID := firstNonEmpty(
			strArg(args, "id"), strArg(args, "object_id"),
			strArg(args, "claim_id"), strArg(args, "expense_id"))
		if objectID == "" {
			return nil, errors.E(errors.CodeInvalidArgument,
				"ERP 单对象读取需要对象 id（args.id / object_id / expense_id）")
		}
		res, err := port.GetObject(r.Context(), erp.GetObjectRequest{
			TenantID:     tenantID,
			ConnectionID: erp.ConnectionID(connID),
			ObjectType:   objectType,
			ObjectID:     erp.ObjectID(objectID),
			Fields:       strSliceArg(args, "fields"),
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"object_type":    res.Object.ObjectType,
			"object_id":      res.Object.ObjectID,
			"fields":         res.Object.Fields,
			"data_quality":   res.Object.DataQuality,
			"source_version": res.Object.SourceVersion,
			"observed_at":    res.Object.ObservedAt,
			"erp_source":     source,
		}, nil
	case "query":
		filter := erp.FilterExpression{}
		if f, ok := args["filter"].(map[string]any); ok {
			filter.Field, _ = f["field"].(string)
			filter.Op, _ = f["op"].(string)
			filter.Value = f["value"]
		}
		switch {
		case filter.Field == "" && strArg(args, "tax_id") != "":
			filter = erp.FilterExpression{Field: "tax_id", Op: "eq", Value: strArg(args, "tax_id")}
		case filter.Field == "" && strArg(args, "vendor_name") != "":
			filter = erp.FilterExpression{Field: "vendor_name", Op: "contains", Value: strArg(args, "vendor_name")}
		case filter.Field == "" && strArg(args, "keyword") != "":
			filter = erp.FilterExpression{Field: "keyword", Op: "contains", Value: strArg(args, "keyword")}
		}
		pageSize := 20
		if ps, ok := args["page_size"].(float64); ok && ps > 0 && ps <= 200 {
			pageSize = int(ps)
		}
		res, err := port.QueryObjects(r.Context(), erp.QueryObjectsRequest{
			TenantID:     tenantID,
			ConnectionID: erp.ConnectionID(connID),
			ObjectType:   objectType,
			Filter:       filter,
			Fields:       strSliceArg(args, "fields"),
			PageSize:     pageSize,
			Cursor:       strArg(args, "cursor"),
		})
		if err != nil {
			return nil, err
		}
		items := make([]map[string]any, 0, len(res.Items))
		for _, it := range res.Items {
			items = append(items, map[string]any{
				"object_type":  it.ObjectType,
				"object_id":    it.ObjectID,
				"fields":       it.Fields,
				"data_quality": it.DataQuality,
			})
		}
		return map[string]any{
			"object_type": objectType,
			"items":       items,
			"next_cursor": res.NextCursor,
			"has_more":    res.HasMore,
			"erp_source":  source,
		}, nil
	case "update":
		// EXTERNAL_WRITE 的真实执行只出现在审批通过后的确认链路；
		// 写操作必须携带审批凭据，无门禁直调一律拒绝。
		approvalID := strArg(args, "__approval_id")
		decisionID := strArg(args, "__authorization_decision_id")
		if approvalID == "" || decisionID == "" {
			return nil, errors.E(errors.CodeInvalidArgument,
				"ERP 写操作必须携带审批凭据（审批 id 与授权决策 id）：写操作不允许无门禁直调")
		}
		payload, _ := args["payload"].(map[string]any)
		if payload == nil {
			// 该工具的 input_schema 直接把业务字段放在顶层（如 erp.expense.decide
			// 的 expense_id/decision/comment），此时整个 args 就是 payload。
			payload = map[string]any{}
			for k, v := range args {
				if strings.HasPrefix(k, "__") {
					continue
				}
				payload[k] = v
			}
		}
		targetID := firstNonEmpty(
			strArg(args, "id"), strArg(args, "object_id"),
			strArg(args, "expense_id"), strArg(args, "claim_id"))
		if targetID == "" {
			return nil, errors.E(errors.CodeInvalidArgument, "ERP 写操作缺少目标对象 id")
		}
		payloadJSON, _ := json.Marshal(payload)
		res, err := port.Execute(r.Context(), erp.ExecuteRequest{
			TenantID:                tenantID,
			ConnectionID:            erp.ConnectionID(connID),
			OperationID:             erp.OperationID("toolinv-" + invID),
			OperationType:           firstNonEmpty(strArg(args, "operation_type"), "update"),
			TargetObjectID:          erp.ObjectID(targetID),
			Payload:                 payload,
			PayloadDigest:           sha256Hex(payloadJSON),
			IdempotencyKey:          "toolinv:" + invID,
			AuthorizationDecisionID: decisionID,
			ApprovalID:              approvalID,
			TraceID:                 invID,
		})
		if err != nil {
			return nil, err
		}
		if res.Error != nil {
			return nil, errors.Ef(errors.CodeERPBusinessRejected,
				"ERP 返回业务拒绝：%s %s", res.Error.Code, res.Error.Message)
		}
		return map[string]any{
			"operation_id":   string(res.OperationID),
			"status":         string(res.Status),
			"remote_ref":     res.RemoteRef,
			"receipt_id":     res.ReceiptID,
			"payload_digest": res.PayloadDigest,
			"erp_source":     source,
		}, nil
	default:
		return nil, errors.Ef(errors.CodeUnavailable,
			"工具操作 %q 不受支持（ERP 执行器允许 get/query/update）", op)
	}
}

// resolveErpPortForAdapter 按执行适配器解析 ERP Port（来源显式标注，绝不混合）。
func (h *Handler) resolveErpPortForAdapter(r *http.Request, adapter string) (erp.Port, string, string, error) {
	if adapter == "nc65" {
		return h.resolveErpPort(r, "nc65")
	}
	// mock-erp：演示 ERP 模拟器（mockerp 容器是真实 HTTP 服务），只经 env 装配。
	if h.deps.ERP == nil {
		return nil, "", "", errors.E(errors.CodeUnavailable,
			"演示 ERP（mockerp）未装配：API 进程缺少 MOCK_ERP_BASE_URL 等环境变量")
	}
	return h.deps.ERP, h.deps.ERPConnectionID, "env", nil
}

// execDocingestTool 真实解析文档（word/excel/text；扩展名受执行契约 formats 白名单约束）。
func (h *Handler) execDocingestTool(cfg executionConfig, args map[string]any) (map[string]any, error) {
	root := h.docsRoot()
	rel := firstNonEmpty(strArg(args, "source_path"), strArg(args, "path"))
	if rel == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "文档类工具需要 args.source_path（文档根目录内的相对路径）")
	}
	clean := filepath.Clean(rel)
	if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return nil, errors.E(errors.CodeInvalidArgument, "source_path 必须是文档根目录内的相对路径")
	}
	if len(cfg.Formats) > 0 {
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(clean)), ".")
		allowed := false
		for _, f := range cfg.Formats {
			if strings.EqualFold(strings.TrimSpace(f), ext) {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, errors.Ef(errors.CodeInvalidArgument,
				"该工具只接受 %v 格式文档，收到 .%s", cfg.Formats, ext)
		}
	}
	full := filepath.Join(root, clean)
	info, err := os.Stat(full)
	if err != nil {
		return nil, errors.Ef(errors.CodeNotFound, "文档不存在：%s（文档根目录内）", rel)
	}
	if info.IsDir() {
		return nil, errors.E(errors.CodeInvalidArgument, "source_path 指向目录，需要具体文档文件")
	}
	doc, err := docingest.Extract(full)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "文档解析失败（真实解析，不降级）")
	}
	const textCap = 20000
	text := doc.Text
	truncated := false
	if len(text) > textCap {
		text = text[:textCap]
		truncated = true
	}
	sheets := make([]map[string]any, 0, len(doc.Sheets))
	for _, s := range doc.Sheets {
		sheets = append(sheets, map[string]any{"name": s.Name, "row_count": len(s.Rows)})
	}
	return map[string]any{
		"source_path": rel,
		"format":      doc.Kind,
		"char_count":  len(doc.Text),
		"text":        text,
		"truncated":   truncated,
		"sheets":      sheets,
		"warnings":    doc.Warnings,
	}, nil
}

// execShaclTool 用平台真实 SHACL 引擎对本体版本执行校验（装载 TBox+ABox 后求值）。
func (h *Handler) execShaclTool(ctx context.Context, id tenantctx.Identity, args map[string]any) (map[string]any, error) {
	if h.deps.GraphStore == nil {
		return nil, errors.E(errors.CodeUnavailable,
			"图存储未装配：SHACL 校验需要真实装载本体与实例后求值，无法离线给出结论")
	}
	verID, err := ids.Parse(strArg(args, "ontology_version_id"))
	if err != nil || verID.IsZero() {
		return nil, errors.E(errors.CodeInvalidArgument, "需要 args.ontology_version_id（本体版本 id）")
	}
	snap, err := h.deps.Store.OntologyVersionSnapshot(ctx, verID)
	if err != nil {
		return nil, err
	}
	ns, err := graphload.NewNamespace(snap.OntologyKey)
	if err != nil {
		return nil, err
	}
	parsed, err := graphload.ParseManifest(snap.Manifest)
	if err != nil {
		return nil, errors.E(errors.CodeValidationFailed, "本体清单解析失败："+err.Error())
	}
	if len(parsed.Shapes) == 0 {
		return nil, errors.E(errors.CodeValidationFailed, "该本体版本未定义 SHACL 形状，无可执行约束")
	}
	shapes, err := parsed.BuildShapes(ns)
	if err != nil {
		return nil, err
	}
	graphIRI, err := graphload.GraphIRI(snap.OntologyKey, snap.VersionID)
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
	if _, err := h.deps.GraphStore.InsertTriples(ctx, id.TenantID, graphIRI, append(tbox, abox...)); err != nil {
		return nil, err
	}
	report, err := shacl.Validate(h.deps.GraphStore, id.TenantID, graphIRI, shapes)
	if err != nil {
		return nil, err
	}
	findings := make([]map[string]any, 0, len(report.Results))
	for i, res := range report.Results {
		if i >= 20 {
			break
		}
		findings = append(findings, map[string]any{
			"shape_id":       res.ShapeID,
			"focus_node":     res.FocusNode,
			"path":           res.Path,
			"component":      res.Component,
			"severity":       res.Severity,
			"message":        res.Message,
			"finding_digest": findingDigest(res),
		})
	}
	return map[string]any{
		"ontology_version_id": snap.VersionID,
		"graph_iri":           graphIRI,
		"conforms":            report.Conforms,
		"violation_count":     len(report.Results),
		"findings":            findings,
	}, nil
}

// findingDigest 与校验管线同一规则：shape_id/focus_node/path/component/severity
// 元组的 SHA-256（0x1f 分隔），保证跨轮次可对比、可去重。
func findingDigest(res shacl.Result) string {
	join := strings.Join([]string{res.ShapeID, res.FocusNode, res.Path, res.Component, res.Severity}, "\x1f")
	return sha256Hex([]byte(join))
}

// execGraphloadTool 用平台清单解析器真实解析 manifest（结构与闭包校验）。
func (h *Handler) execGraphloadTool(args map[string]any) (map[string]any, error) {
	manifestJSON, err := json.Marshal(args["manifest"])
	if err != nil || len(manifestJSON) == 0 || string(manifestJSON) == "null" {
		return nil, errors.E(errors.CodeInvalidArgument, "需要 args.manifest（本体清单 JSON 对象）")
	}
	parsed, err := graphload.ParseManifest(manifestJSON)
	if err != nil {
		return nil, errors.E(errors.CodeValidationFailed, "清单解析失败："+err.Error())
	}
	return map[string]any{
		"valid":          true,
		"class_count":    len(parsed.Classes),
		"relation_count": len(parsed.Relations),
		"shape_count":    len(parsed.Shapes),
		"summary":        parsed.Summary,
	}, nil
}

// execLLMTool 经受治理的模型网关真实调用大模型（限流+预算+缓存+路由+账本）。
func (h *Handler) execLLMTool(ctx context.Context, id tenantctx.Identity, invID string, args map[string]any) (map[string]any, error) {
	gw, err := h.llmGateway()
	if err != nil {
		return nil, err
	}
	modelID := strArg(args, "model_id")
	if modelID == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "需要 args.model_id（已登记且参与路由的模型名）")
	}
	prompt := strArg(args, "prompt")
	if prompt == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "需要 args.prompt（模型输入）")
	}
	req := llm.CompletionRequest{
		TenantID:        id.TenantID,
		ModelID:         modelID,
		Messages:        []llm.Message{{Role: "user", Content: prompt}},
		MaxOutputTokens: 1024,
		IdempotencyKey:  "toolinv:" + invID,
	}
	if mt, ok := args["max_output_tokens"].(float64); ok && mt > 0 && mt <= 8192 {
		req.MaxOutputTokens = int(mt)
	}
	if schema, ok := args["json_schema"].(map[string]any); ok && len(schema) > 0 {
		raw, merr := json.Marshal(schema)
		if merr == nil {
			req.ResponseSchema = raw
		}
	}
	completion, err := gw.Complete(ctx, id.TenantID, req)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"model_revision": completion.ModelRevision,
		"finish_reason":  completion.FinishReason,
		"content":        completion.Content,
		"input_tokens":   completion.InputTokens,
		"output_tokens":  completion.OutputTokens,
		"cache_hit":      completion.CacheHit,
	}, nil
}

// execRetrieverTool 对制度文档根目录做真实词项检索（打分排序，非假召回）。
func (h *Handler) execRetrieverTool(cfg executionConfig, args map[string]any) (map[string]any, error) {
	root := h.docsRoot()
	query := strings.TrimSpace(strArg(args, "query"))
	if query == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "需要 args.query（检索词）")
	}
	topK := 5
	if tk, ok := args["top_k"].(float64); ok && tk > 0 && tk <= 20 {
		topK = int(tk)
	}
	terms := tokenize(query)

	type hit struct {
		path  string
		score int
		snip  string
	}
	hits := []hit{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".md" && ext != ".txt" {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		content := string(data)
		score := 0
		first := -1
		for _, t := range terms {
			idx := 0
			for {
				i := strings.Index(content[idx:], t)
				if i < 0 {
					break
				}
				score++
				if first < 0 {
					first = idx + i
				}
				idx += i + len(t)
			}
		}
		if score == 0 {
			return nil
		}
		snippet := content
		start := first - 60
		if start < 0 {
			start = 0
		}
		end := first + 200
		if end > len(snippet) {
			end = len(snippet)
		}
		snippet = snippet[start:end]
		rel, _ := filepath.Rel(root, path)
		hits = append(hits, hit{path: filepath.ToSlash(rel), score: score, snip: snippet})
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "检索语料读取失败")
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if len(hits) > topK {
		hits = hits[:topK]
	}
	items := make([]map[string]any, 0, len(hits))
	for _, it := range hits {
		items = append(items, map[string]any{"path": it.path, "score": it.score, "snippet": it.snip})
	}
	return map[string]any{"query": query, "index": cfg.Index, "hits": items}, nil
}

// execAuditTool 经审计记录器向哈希链追加真实审计事件。
func (h *Handler) execAuditTool(
	r *http.Request,
	id tenantctx.Identity,
	rec *store.ToolVersionRecord,
	invID string,
	args map[string]any,
) (map[string]any, error) {
	action := firstNonEmpty(strArg(args, "action"), "tool.invoke.audit")
	resType := firstNonEmpty(strArg(args, "resource_type"), "audit")
	target := firstNonEmpty(strArg(args, "resource"), strArg(args, "resource_id"), invID)
	runID := ids.ID{}
	if raw := strArg(args, "run_id"); raw != "" {
		if parsed, perr := ids.Parse(raw); perr == nil {
			runID = parsed
		} else {
			return nil, errors.E(errors.CodeInvalidArgument, "args.run_id 不是合法 id")
		}
	}
	h.recordAudit(r, id, "ToolAuditAppend", action, "SUCCESS", resType, target, runID,
		map[string]any{
			"tool_key":      rec.ToolKey,
			"invocation_id": invID,
			"note":          firstNonEmpty(strArg(args, "note"), "工具测试调用产生的审计追加（哈希链）"),
		})
	return map[string]any{
		"appended":      true,
		"action":        action,
		"resource_type": resType,
		"resource_id":   target,
		"chain":         "audit_events（哈希链追加）",
		"invocation_id": invID,
	}, nil
}

// ---- 小工具 ----

func strArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}

func strSliceArg(args map[string]any, key string) []string {
	raw, ok := args[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, it := range raw {
		s, _ := it.(string)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func tokenize(q string) []string {
	parts := strings.FieldsFunc(q, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '，' || r == '、' || r == '；' || r == '。'
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
