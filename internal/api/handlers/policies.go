package handlers

import (
	"encoding/json"
	"net/http"

	"odap/internal/api/store"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// createPolicyRequest 是 POST /api/v1/policies 的请求体。
type createPolicyRequest struct {
	PolicyKey      string          `json:"policy_key"`
	Version        string          `json:"version"`
	PolicyLanguage string          `json:"policy_language,omitempty"`
	PolicyDocument json.RawMessage `json:"policy_document"`
}

// ListPolicies 处理 GET /api/v1/policies。
func (h *Handler) ListPolicies(w http.ResponseWriter, r *http.Request) {
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
	res, err := h.deps.Store.ListPolicies(r.Context(), limit, r.URL.Query().Get("cursor"), r.URL.Query().Get("status"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, res)
}

// CreatePolicy 处理 POST /api/v1/policies（经 authorization.ParsePolicy 真实校验）。
func (h *Handler) CreatePolicy(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	var req createPolicyRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	out, err := h.deps.Store.CreatePolicy(r.Context(), store.CreatePolicyInput{
		PolicyKey:      req.PolicyKey,
		Version:        req.Version,
		PolicyLanguage: req.PolicyLanguage,
		PolicyDocument: req.PolicyDocument,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "PolicyVersionCreated", "policy.create", "SUCCESS", "policy",
		stringify(out["id"]), ids.ID{}, map[string]any{"policy_key": req.PolicyKey, "idempotency_key": idempotencyKey(r)})
	h.writeJSON(w, http.StatusCreated, out)
}

// ListPolicyVersions 处理 GET /api/v1/policies/{id}/versions。
//
// 契约中该路径参数语义为 policy_key（策略版本管理以 policy_key + version
// 为主键，见 governance.policy_versions）。
func (h *Handler) ListPolicyVersions(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	key := r.PathValue("id")
	if key == "" {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "policy_key 不能为空"))
		return
	}
	res, err := h.deps.Store.ListPolicyVersions(r.Context(), key)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, res)
}
