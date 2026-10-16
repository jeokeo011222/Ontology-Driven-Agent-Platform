package handlers

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"io"
	"net/http"

	"odap/internal/api/sse"
	"odap/internal/api/store"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/tools"
)

// createToolRequest 是 POST /api/v1/tools 的请求体。
type createToolRequest struct {
	ToolKey      string          `json:"tool_key"`
	Description  string          `json:"description,omitempty"`
	Version      string          `json:"version,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema"`
	OutputSchema json.RawMessage `json:"output_schema"`
	Capabilities []string        `json:"capabilities,omitempty"`
	EffectClass  string          `json:"effect_class,omitempty"`
	// ExecutionConfig 是首个版本的执行契约（adapter=script 时为 WASM 脚本工具）。
	// 仅用户自建工具可携带；内置工具由数据集 seed 登记，不经本接口（store 层
	// ensureToolMutable 对内置工具的修改一律 409）。
	ExecutionConfig json.RawMessage `json:"execution_config,omitempty"`
}

// maxToolCreateBodyBytes 是新建工具的请求体上限：普通 JSON 端点为 1MB
// （maxBodyBytes），但 adapter=script 允许携带 base64 编码的 .wasm 模块
// （8MB 模块 → 约 10.7MB base64），放宽到 16MB，其余字段开销忽略不计。
const maxToolCreateBodyBytes int64 = 16 << 20

// decodeJSONLimit 与 decodeJSON 相同，但允许调用方指定请求体上限
// （供携带大体积二进制载荷的端点使用）。
func decodeJSONLimit(r *http.Request, dst any, maxBytes int64) error {
	if r.Body == nil {
		return errors.E(errors.CodeInvalidArgument, "请求体不能为空")
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if stderrors.As(err, &maxErr) {
			return errors.E(errors.CodeInvalidArgument, "请求体过大")
		}
		return errors.E(errors.CodeInvalidArgument, "请求体不是合法 JSON 或包含未知字段")
	}
	return nil
}

// ListTools 处理 GET /api/v1/tools（支持 ?effect_class= 过滤）。
func (h *Handler) ListTools(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	limit, err := limitParam(r, 10)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	res, err := h.deps.Store.ListTools(r.Context(), limit, r.URL.Query().Get("cursor"), r.URL.Query().Get("effect_class"), r.URL.Query().Get("status"), r.URL.Query().Get("q"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, res)
}

// CreateTool 处理 POST /api/v1/tools。
func (h *Handler) CreateTool(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	var req createToolRequest
	if err := decodeJSONLimit(r, &req, maxToolCreateBodyBytes); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	// 执行契约登记校验（fail-closed）：adapter=script 时强制 language=wasm、
	// wasm_b64 合法（base64+魔数）、模块 ≤8MB、timeout/max_output 在界内、
	// effect_class 必须为 READ（纯计算固定只读）。
	if _, err := validateExecutionConfigForRegistration(req.ExecutionConfig, req.EffectClass); err != nil {
		h.fail(w, r, err)
		return
	}
	out, err := h.deps.Store.CreateTool(r.Context(), store.CreateToolInput{
		ToolKey:         req.ToolKey,
		Description:     req.Description,
		Version:         req.Version,
		InputSchema:     req.InputSchema,
		OutputSchema:    req.OutputSchema,
		Capabilities:    req.Capabilities,
		EffectClass:     req.EffectClass,
		ExecutionConfig: req.ExecutionConfig,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "ToolRegistered", "tool.register", "SUCCESS", "tool",
		stringify(out["id"]), ids.ID{}, map[string]any{"tool_key": req.ToolKey, "idempotency_key": idempotencyKey(r)})
	h.writeJSON(w, http.StatusCreated, out)
}

// invokeToolRequest 是 POST /api/v1/tools/{id}/invoke 的请求体。
type invokeToolRequest struct {
	Args map[string]any `json:"args"`
}

// InvocableToolRecord 经 registry.CatalogService 解析工具的**最新已发布版本**，
// 并适配为执行器所需的 ToolVersionRecord（导出给 worker 步进执行器复用）。
//
// 版本解析的真值来源是 registry 包（目录域），不再是 store 的直查 SQL——
// 两套解析并存曾让"取最新"与"取最新已发布"两种语义各写一份，其中一份
// 会被工具自己的 DRAFT 版本挡住可用的 PUBLISHED 版本（已在 resolver 修复）。
func (h *Handler) InvocableToolRecord(ctx context.Context, tenantID ids.ID, toolID ids.ID) (*store.ToolVersionRecord, error) {
	if h.deps.Catalog == nil {
		return nil, unavailable("工具目录解析器未装配（registry.CatalogService）")
	}
	inv, err := h.deps.Catalog.ResolveLatestPublishedTool(ctx, tenantID, toolID)
	if err != nil {
		return nil, err
	}
	rec := &store.ToolVersionRecord{
		ToolID:      toolID.String(),
		ToolKey:     inv.Tool.ToolKey,
		VersionID:   inv.Version.ID.String(),
		Version:     inv.Version.Version,
		EffectClass: inv.Version.EffectClass,
		Status:      inv.Version.Status,
	}
	if len(inv.Version.InputSchema) > 0 {
		rec.InputSchema = json.RawMessage(inv.Version.InputSchema)
	}
	if len(inv.Version.OutputSchema) > 0 {
		rec.OutputSchema = json.RawMessage(inv.Version.OutputSchema)
	}
	if len(inv.Version.ExecutionConfig) > 0 {
		rec.ExecutionConfig = json.RawMessage(inv.Version.ExecutionConfig)
	}
	return rec, nil
}

// InvokeTool 处理 POST /api/v1/tools/{id}/invoke —— 工具测试调用的真实执行链。
//
// 效果类别决定流程（fail-closed：无审批不落外部系统）：
//   - READ / INTERNAL_WRITE：参数校验 → 建调用记录（EXECUTING）→ 真实执行器 →
//     CAS 推进 SUCCEEDED / FAILED → 审计+SSE，返回 200 + 调用记录（含真实输出）。
//     READ 类先查结果缓存（TTL 内同参直接复用，调用记录照常落库、审计标注 cache_hit）。
//   - EXTERNAL_WRITE：参数校验 → 建调用记录（PENDING_APPROVAL）→ 审计+SSE，
//     返回 202 + 调用 id；真实执行只发生在 POST .../invocations/{inv}/approve
//     确认链路（带 CAS 状态守卫，绝不重复执行）。
//
// 执行结果如实落库：FAILED 记录真实 error_code/error_summary，绝不伪造成功。
func (h *Handler) InvokeTool(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	toolID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var req invokeToolRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	rec, err := h.InvocableToolRecord(r.Context(), id.TenantID, toolID)
	if err != nil {
		h.recordAudit(r, id, "ToolInvokeRejected", "tool.invoke", "DENIED", "tool", toolID.String(), ids.ID{},
			map[string]any{"reason": "resolve_failed", "detail": clip(err.Error(), 200)})
		h.fail(w, r, err)
		return
	}
	if err := tools.ValidateArgs(rec.InputSchema, req.Args); err != nil {
		h.recordAudit(r, id, "ToolInvokeRejected", "tool.invoke", "DENIED", "tool", toolID.String(), ids.ID{},
			map[string]any{"tool_key": rec.ToolKey, "reason": "args_invalid"})
		h.fail(w, r, err)
		return
	}
	if req.Args == nil {
		req.Args = map[string]any{}
	}
	inputJSON, _ := json.Marshal(req.Args)

	// EXTERNAL_WRITE：先落 PENDING_APPROVAL，等待人工审批确认后执行。
	if rec.EffectClass == "EXTERNAL_WRITE" {
		inv, err := h.deps.Store.CreateToolInvocation(r.Context(), store.CreateToolInvocationInput{
			ToolID: toolID, ToolVersionID: parseIDOrZero(rec.VersionID),
			ToolKey: rec.ToolKey, EffectClass: rec.EffectClass,
			Status: store.InvocationPendingApproval, InputPayload: inputJSON, RequestedBy: id.SubjectID.String(),
		})
		if err != nil {
			h.fail(w, r, err)
			return
		}
		invID := mapStr(inv["id"])
		h.recordAudit(r, id, "ToolInvocationPendingApproval", "tool.invoke", "SUCCESS", "tool", toolID.String(), ids.ID{},
			map[string]any{"tool_key": rec.ToolKey, "invocation_id": invID, "effect_class": rec.EffectClass})
		h.publish(id.TenantID.String(), "ToolInvocationUpdated", sse.Important,
			map[string]any{"invocation_id": invID, "tool_id": toolID.String(), "status": store.InvocationPendingApproval})
		h.writeJSON(w, http.StatusAccepted, map[string]any{
			"invocation": inv,
			"message":    "该工具为外部写入（EXTERNAL_WRITE）：已进入待审批状态，需具有审批权限者确认后才会真实执行",
		})
		return
	}

	// READ / INTERNAL_WRITE：同步真实执行。
	inv, err := h.deps.Store.CreateToolInvocation(r.Context(), store.CreateToolInvocationInput{
		ToolID: toolID, ToolVersionID: parseIDOrZero(rec.VersionID),
		ToolKey: rec.ToolKey, EffectClass: rec.EffectClass,
		Status: store.InvocationExecuting, InputPayload: inputJSON, RequestedBy: id.SubjectID.String(),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	invID := mapStr(inv["id"])
	invIDParsed := parseIDOrZero(invID)
	h.recordAudit(r, id, "ToolInvocationStarted", "tool.invoke", "SUCCESS", "tool", toolID.String(), ids.ID{},
		map[string]any{"tool_key": rec.ToolKey, "invocation_id": invID})

	// READ 类先查只读结果缓存：命中时跳过真实执行（调用记录照常推进到
	// SUCCEEDED，耗时接近 0 是命中缓存的真实表现，审计标注 cache_hit）。
	var (
		out      map[string]any
		execErr  error
		cacheHit bool
	)
	if rec.EffectClass == "READ" {
		if cached, ok := h.deps.ToolCache.Get(rec.ToolKey, rec.Version, req.Args); ok {
			out, cacheHit = cached, true
		}
	}
	if !cacheHit {
		out, execErr = h.ExecuteToolInvocation(r.Context(), id, rec, invID, req.Args)
	}
	if execErr != nil {
		code, summary := errorCodeParts(execErr)
		updated, terr := h.deps.Store.TransitionToolInvocation(r.Context(), store.TransitionToolInvocationInput{
			InvID: invIDParsed, From: store.InvocationExecuting, To: store.InvocationFailed,
			ErrorCode: &code, ErrorSum: &summary, FinishClock: true,
		})
		if terr != nil {
			// 状态推进失败（被并发推进）：如实报告，绝不覆盖他人结论。
			h.fail(w, r, terr)
			return
		}
		h.recordAudit(r, id, "ToolInvocationFailed", "tool.invoke", "ERROR", "tool", toolID.String(), ids.ID{},
			map[string]any{"tool_key": rec.ToolKey, "invocation_id": invID, "error_code": code, "error_summary": summary})
		h.publish(id.TenantID.String(), "ToolInvocationUpdated", sse.Important,
			map[string]any{"invocation_id": invID, "tool_id": toolID.String(), "status": store.InvocationFailed, "error_code": code})
		h.writeJSON(w, http.StatusOK, map[string]any{"invocation": updated})
		return
	}
	// 成功后维护缓存：READ 放入；INTERNAL_WRITE 整体失效（外部/内部状态可能
	// 已被本次写入改变，粗粒度失效是唯一不漏的选择）。
	if h.deps.ToolCache != nil {
		switch rec.EffectClass {
		case "READ":
			h.deps.ToolCache.Put(rec.ToolKey, rec.Version, req.Args, out)
		case "INTERNAL_WRITE":
			h.deps.ToolCache.InvalidateAll()
		}
	}
	outJSON, _ := json.Marshal(out)
	updated, terr := h.deps.Store.TransitionToolInvocation(r.Context(), store.TransitionToolInvocationInput{
		InvID: invIDParsed, From: store.InvocationExecuting, To: store.InvocationSucceeded,
		Output: outJSON, FinishClock: true,
	})
	if terr != nil {
		h.fail(w, r, terr)
		return
	}
	h.recordAudit(r, id, "ToolInvocationSucceeded", "tool.invoke", "SUCCESS", "tool", toolID.String(), ids.ID{},
		map[string]any{"tool_key": rec.ToolKey, "invocation_id": invID, "cache_hit": cacheHit})
	h.publish(id.TenantID.String(), "ToolInvocationUpdated", sse.Important,
		map[string]any{"invocation_id": invID, "tool_id": toolID.String(), "status": store.InvocationSucceeded, "cache_hit": cacheHit})
	h.writeJSON(w, http.StatusOK, map[string]any{"invocation": updated})
}

// ListToolInvocations 处理 GET /api/v1/tools/{id}/invocations（调用记录，cursor 分页 + ?status=）。
func (h *Handler) ListToolInvocations(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	toolID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	limit, err := limitParam(r, 10)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	res, err := h.deps.Store.ListToolInvocations(r.Context(), limit,
		r.URL.Query().Get("cursor"), toolID.String(), r.URL.Query().Get("status"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, res)
}

// decideToolInvocationRequest 是审批确认/驳回的请求体。
type decideToolInvocationRequest struct {
	Comment string `json:"comment,omitempty"`
}

// ApproveToolInvocation 处理 POST /api/v1/tools/{id}/invocations/{inv_id}/approve。
//
// 审批门禁的确认侧：PENDING_APPROVAL → APPROVED（CAS，记录审批人与时间）→
// EXECUTING（CAS + 开始计时）→ 真实执行器（携带审批凭据）→ SUCCEEDED / FAILED。
// 全程 CAS 状态守卫：重复审批 / 已驳回 / 已终态的记录会得到 409，绝不重复执行。
func (h *Handler) ApproveToolInvocation(w http.ResponseWriter, r *http.Request) {
	h.decideToolInvocation(w, r, true)
}

// RejectToolInvocation 处理 POST /api/v1/tools/{id}/invocations/{inv_id}/reject。
// PENDING_APPROVAL → REJECTED（CAS）；驳回后永不执行。
func (h *Handler) RejectToolInvocation(w http.ResponseWriter, r *http.Request) {
	h.decideToolInvocation(w, r, false)
}

func (h *Handler) decideToolInvocation(w http.ResponseWriter, r *http.Request, approve bool) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	toolID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	invID, err := h.pathID(r, "inv_id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var req decideToolInvocationRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)

	inv, err := h.deps.Store.GetToolInvocation(r.Context(), invID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if mapStr(inv["tool_id"]) != toolID.String() {
		h.fail(w, r, errors.E(errors.CodeNotFound, "该调用记录不属于此工具"))
		return
	}
	if status := mapStr(inv["status"]); status != store.InvocationPendingApproval {
		h.fail(w, r, errors.Ef(errors.CodeConflict,
			"调用记录状态为 %s（仅 PENDING_APPROVAL 可审批），已被他人处理", status))
		return
	}
	rec, err := h.InvocableToolRecord(r.Context(), id.TenantID, toolID)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// 驳回：PENDING_APPROVAL → REJECTED，永不执行。
	if !approve {
		now := h.now()
		updated, terr := h.deps.Store.TransitionToolInvocation(r.Context(), store.TransitionToolInvocationInput{
			InvID: invID, From: store.InvocationPendingApproval, To: store.InvocationRejected,
			ApprovedBy: &[]string{id.SubjectID.String()}[0], DecidedAt: &now,
			ErrorSum: &[]string{"审批驳回" + commentSuffix(req.Comment)}[0],
		})
		if terr != nil {
			h.fail(w, r, terr)
			return
		}
		h.recordAudit(r, id, "ToolInvocationRejected", "tool.approve", "DENIED", "tool", toolID.String(), ids.ID{},
			map[string]any{"tool_key": rec.ToolKey, "invocation_id": invID.String(), "comment": req.Comment})
		h.publish(id.TenantID.String(), "ToolInvocationUpdated", sse.Important,
			map[string]any{"invocation_id": invID.String(), "tool_id": toolID.String(), "status": store.InvocationRejected})
		h.writeJSON(w, http.StatusOK, map[string]any{"invocation": updated})
		return
	}

	// 批准：PENDING_APPROVAL → APPROVED（记录审批人）→ EXECUTING → 真实执行。
	now := h.now()
	if _, terr := h.deps.Store.TransitionToolInvocation(r.Context(), store.TransitionToolInvocationInput{
		InvID: invID, From: store.InvocationPendingApproval, To: store.InvocationApproved,
		ApprovedBy: &[]string{id.SubjectID.String()}[0], DecidedAt: &now,
	}); terr != nil {
		h.fail(w, r, terr)
		return
	}
	h.recordAudit(r, id, "ToolInvocationApproved", "tool.approve", "SUCCESS", "tool", toolID.String(), ids.ID{},
		map[string]any{"tool_key": rec.ToolKey, "invocation_id": invID.String(), "comment": req.Comment})
	h.publish(id.TenantID.String(), "ToolInvocationUpdated", sse.Important,
		map[string]any{"invocation_id": invID.String(), "tool_id": toolID.String(), "status": store.InvocationApproved})

	if _, terr := h.deps.Store.TransitionToolInvocation(r.Context(), store.TransitionToolInvocationInput{
		InvID: invID, From: store.InvocationApproved, To: store.InvocationExecuting, StartClock: true,
	}); terr != nil {
		h.fail(w, r, terr)
		return
	}

	// 注入审批凭据（__ 前缀字段仅服务端可写；输入已在 invoke 时通过 Schema 校验）。
	var args map[string]any
	if raw := mapStr(inv["input_payload"]); raw != "" {
		_ = json.Unmarshal([]byte(raw), &args)
	}
	if args == nil {
		args = map[string]any{}
	}
	args["__approval_id"] = invID.String()
	args["__authorization_decision_id"] = "manual-approve:" + id.SubjectID.String()

	out, execErr := h.ExecuteToolInvocation(r.Context(), id, rec, invID.String(), args)
	if execErr != nil {
		code, summary := errorCodeParts(execErr)
		updated, terr := h.deps.Store.TransitionToolInvocation(r.Context(), store.TransitionToolInvocationInput{
			InvID: invID, From: store.InvocationExecuting, To: store.InvocationFailed,
			ErrorCode: &code, ErrorSum: &summary, FinishClock: true,
		})
		if terr != nil {
			h.fail(w, r, terr)
			return
		}
		h.recordAudit(r, id, "ToolInvocationFailed", "tool.invoke", "ERROR", "tool", toolID.String(), ids.ID{},
			map[string]any{"tool_key": rec.ToolKey, "invocation_id": invID.String(), "error_code": code, "error_summary": summary})
		h.publish(id.TenantID.String(), "ToolInvocationUpdated", sse.Important,
			map[string]any{"invocation_id": invID.String(), "tool_id": toolID.String(), "status": store.InvocationFailed, "error_code": code})
		h.writeJSON(w, http.StatusOK, map[string]any{"invocation": updated})
		return
	}
	outJSON, _ := json.Marshal(out)
	// EXTERNAL_WRITE 执行成功：整体失效只读缓存（写入可能已改变外部/内部状态，
	// 任何 READ 工具的旧结果都不再可信）。
	if h.deps.ToolCache != nil {
		h.deps.ToolCache.InvalidateAll()
	}
	updated, terr := h.deps.Store.TransitionToolInvocation(r.Context(), store.TransitionToolInvocationInput{
		InvID: invID, From: store.InvocationExecuting, To: store.InvocationSucceeded,
		Output: outJSON, FinishClock: true,
	})
	if terr != nil {
		h.fail(w, r, terr)
		return
	}
	h.recordAudit(r, id, "ToolInvocationSucceeded", "tool.invoke", "SUCCESS", "tool", toolID.String(), ids.ID{},
		map[string]any{"tool_key": rec.ToolKey, "invocation_id": invID.String()})
	h.publish(id.TenantID.String(), "ToolInvocationUpdated", sse.Important,
		map[string]any{"invocation_id": invID.String(), "tool_id": toolID.String(), "status": store.InvocationSucceeded})
	h.writeJSON(w, http.StatusOK, map[string]any{"invocation": updated})
}

func commentSuffix(comment string) string {
	if comment == "" {
		return ""
	}
	return "：" + comment
}

// errorCodeParts 从平台错误中提取 (error_code, error_summary) 供调用记录落库。
func errorCodeParts(err error) (string, string) {
	var ae *errors.AppError
	if stderrors.As(err, &ae) {
		return ae.Code, ae.Message
	}
	return errors.CodeInternal, err.Error()
}
