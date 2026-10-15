package handlers

import (
	"net/http"

	"odap/internal/api/store"
	"odap/internal/llm"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// registerModelRevisionRequest 是
// POST /api/v1/admin/model-endpoints/{endpoint_id}/models 的请求体。
//
// 一次登记同时写入**能力档案**（按内容寻址，llm.capability_profiles）与**模型版本**
// （llm.model_revisions）。二者缺一，端点都不会被 Model Registry 装配，自动生成
// 端点会一直以「模型网关未装配」fail-closed（见 internal/llm/registry.go Load 的
// 排除原因："该端点尚未登记任何模型版本"）。
//
// capabilities 用嵌套结构体承载（而非 json.RawMessage）：decodeJSON 已启用
// DisallowUnknownFields，因此拼错字段名会被直接拒绝，而不会像 map 那样
// 静默取零值、把端点登记成"不支持原生 schema"。该结构体字段与
// llm.ProviderCapabilities 一一对应。
type registerModelRevisionRequest struct {
	ModelID            string                  `json:"model_id"`
	RevisionLabel      string                  `json:"revision_label,omitempty"`
	CapabilityRevision string                  `json:"capability_revision"`
	CapabilityVerified bool                    `json:"capability_verified,omitempty"`
	Capabilities       registerCapabilitiesArg `json:"capabilities"`
}

// registerCapabilitiesArg 是能力声明的请求形态（snake_case，与档案落库字段一致）。
type registerCapabilitiesArg struct {
	NativeSchema     bool `json:"native_schema"`
	SupportsSeed     bool `json:"supports_seed"`
	SupportsTools    bool `json:"supports_tools"`
	MaxContextTokens int  `json:"max_context_tokens"`
}

// ListModelRevisions 处理 GET /api/v1/admin/model-endpoints/{endpoint_id}/models。
func (h *Handler) ListModelRevisions(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	endpointID := r.PathValue("endpoint_id")
	rows, err := h.deps.Store.ListModelRevisions(r.Context(), endpointID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"endpoint_id": endpointID,
		"models":      rows,
	})
}

// RegisterModelRevision 处理 POST /api/v1/admin/model-endpoints/{endpoint_id}/models。
func (h *Handler) RegisterModelRevision(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	endpointID := r.PathValue("endpoint_id")
	var req registerModelRevisionRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	if req.Capabilities.MaxContextTokens <= 0 {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument,
			"capabilities.max_context_tokens 必须为正整数：为 0 会被装配期排除、不参与路由"))
		return
	}
	echoIdempotency(w, r)

	out, err := h.deps.Store.RegisterModelRevision(r.Context(), store.RegisterModelRevisionInput{
		EndpointID:         endpointID,
		ModelID:            req.ModelID,
		RevisionLabel:      req.RevisionLabel,
		CapabilityRevision: req.CapabilityRevision,
		CapabilityVerified: req.CapabilityVerified,
		Capabilities: llm.ProviderCapabilities{
			NativeSchema:     req.Capabilities.NativeSchema,
			SupportsSeed:     req.Capabilities.SupportsSeed,
			SupportsTools:    req.Capabilities.SupportsTools,
			MaxContextTokens: req.Capabilities.MaxContextTokens,
		},
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "ModelRevisionRegistered", "model_revision.create", "SUCCESS",
		"model_endpoint", endpointID, ids.ID{},
		map[string]any{
			"endpoint_id":         endpointID,
			"model_revision_id":   out["model_revision_id"],
			"model_id":            req.ModelID,
			"revision_label":      req.RevisionLabel,
			"capability_digest":   out["capability_digest"],
			"capability_revision": req.CapabilityRevision,
			"capability_status":   out["capability_status"],
			"idempotency_key":     idempotencyKey(r),
		})
	h.writeJSON(w, http.StatusCreated, out)
}

// DeleteModelRevision 处理
// DELETE /api/v1/admin/model-endpoints/{endpoint_id}/models/{model_revision_id}。
func (h *Handler) DeleteModelRevision(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	endpointID := r.PathValue("endpoint_id")
	modelRevisionID := r.PathValue("model_revision_id")
	echoIdempotency(w, r)
	if err := h.deps.Store.DeleteModelRevision(r.Context(), endpointID, modelRevisionID); err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "ModelRevisionDeleted", "model_revision.delete", "SUCCESS",
		"model_endpoint", endpointID, ids.ID{},
		map[string]any{
			"model_revision_id": modelRevisionID,
			"idempotency_key":   idempotencyKey(r),
		})
	w.WriteHeader(http.StatusNoContent)
}
