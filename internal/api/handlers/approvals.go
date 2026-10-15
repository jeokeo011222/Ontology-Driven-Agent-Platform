package handlers

import (
	"net/http"
	"strings"

	"odap/internal/api/sse"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// ListApprovals 处理 GET /api/v1/approvals。
func (h *Handler) ListApprovals(w http.ResponseWriter, r *http.Request) {
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
	res, err := h.deps.Store.ListApprovals(r.Context(), r.URL.Query().Get("status"), limit, r.URL.Query().Get("cursor"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, res)
}

// GetApproval 处理 GET /api/v1/approvals/{id}。
func (h *Handler) GetApproval(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	approvalID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out, err := h.deps.Store.GetApproval(r.Context(), approvalID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, out)
}

// decideApprovalRequest 是 POST /api/v1/approvals/{id}/decision 的请求体。
type decideApprovalRequest struct {
	Decision string `json:"decision"`
	Comment  string `json:"comment,omitempty"`
}

// DecideApproval 处理 POST /api/v1/approvals/{id}/decision。
//
// 支持 If-Match（payload_digest）作为乐观并发前置条件，避免基于过期快照决策；
// 不匹配返回 409 CONFLICT。禁止自我审批由持久层强制。
func (h *Handler) DecideApproval(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	approvalID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var req decideApprovalRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	expected := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), "\"")
	if expected == "*" {
		expected = ""
	}
	out, err := h.deps.Store.DecideApproval(r.Context(), approvalID, req.Decision, req.Comment, expected)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	outcome := "SUCCESS"
	if req.Decision == "REJECT" {
		outcome = "DENIED"
	}
	h.recordAudit(r, id, "ApprovalDecided", "approval.decide", outcome, "approval", approvalID.String(), ids.ID{},
		map[string]any{"decision": req.Decision, "idempotency_key": idempotencyKey(r)})
	// 审批决策是安全关键事件：CRITICAL，背压下不丢弃。
	h.publish(id.TenantID.String(), "ApprovalDecided", sse.Critical,
		map[string]any{"approval_id": approvalID.String(), "decision": req.Decision})
	h.writeJSON(w, http.StatusOK, out)
}

// approvalNotFound 统一 NOT_FOUND 语义（保留用于未来扩展）。
func approvalNotFound() error {
	return errors.E(errors.CodeNotFound, "审批不存在或不在可见范围")
}
