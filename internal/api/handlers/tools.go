package handlers

import (
	"encoding/json"
	stderrors "errors"
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
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	out, err := h.deps.Store.CreateTool(r.Context(), store.CreateToolInput{
		ToolKey:      req.ToolKey,
		Description:  req.Description,
		Version:      req.Version,
		InputSchema:  req.InputSchema,
		OutputSchema: req.OutputSchema,
		Capabilities: req.Capabilities,
		EffectClass:  req.EffectClass,
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

// InvokeTool 处理 POST /api/v1/tools/{id}/invoke —— 工具测试调用的真实执行链。
//
// 效果类别决定流程（fail-closed：无审批不落外部系统）：
//   - READ / INTERNAL_WRITE：参数校验 → 建调用记录（EXECUTING）→ 真实执行器 →
//     CAS 推进 SUCCEEDED / FAILED → 审计+SSE，返回 200 + 调用记录（含真实输出）。
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
	rec, err := h.deps.Store.LatestToolVersion(r.Context(), toolID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if rec.Status != "" && rec.Status != "PUBLISHED" {
		h.recordAudit(r, id, "ToolInvokeRejected", "tool.invoke", "DENIED", "tool", toolID.String(), ids.ID{},
			map[string]any{"tool_key": rec.ToolKey, "reason": "version_not_published", "status": rec.Status})
		h.fail(w, r, errors.Ef(errors.CodeConflict, "工具版本状态为 %s（仅 PUBLISHED 可调用）", rec.Status))
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

	out, execErr := h.executeToolInvocation(r, id, rec, invID, req.Args)
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
		map[string]any{"tool_key": rec.ToolKey, "invocation_id": invID})
	h.publish(id.TenantID.String(), "ToolInvocationUpdated", sse.Important,
		map[string]any{"invocation_id": invID, "tool_id": toolID.String(), "status": store.InvocationSucceeded})
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
	rec, err := h.deps.Store.LatestToolVersion(r.Context(), toolID)
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

	out, execErr := h.executeToolInvocation(r, id, rec, invID.String(), args)
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
