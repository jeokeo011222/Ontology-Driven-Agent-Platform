package handlers

import (
	"encoding/json"
	"net/http"

	"odap/internal/api/sse"
	"odap/internal/api/store"
	"odap/internal/platform/ids"
)

// createAgentRequest 是 POST /api/v1/agents 的请求体。
type createAgentRequest struct {
	AgentKey     string          `json:"agent_key"`
	Domain       string          `json:"domain"`
	Description  string          `json:"description,omitempty"`
	Version      string          `json:"version,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema"`
	OutputSchema json.RawMessage `json:"output_schema"`
	// Skills 是绑定的技能键列表（写入 dependency_manifest.skills，
	// 引用校验 fail-closed：技能必须存在且有 PUBLISHED 版本）。
	Skills []string `json:"skills,omitempty"`
}

// ListAgents 处理 GET /api/v1/agents。
func (h *Handler) ListAgents(w http.ResponseWriter, r *http.Request) {
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
	res, err := h.deps.Store.ListAgents(r.Context(), limit, r.URL.Query().Get("cursor"), r.URL.Query().Get("status"), r.URL.Query().Get("q"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, res)
}

// CreateAgent 处理 POST /api/v1/agents。
func (h *Handler) CreateAgent(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	var req createAgentRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	out, err := h.deps.Store.CreateAgent(r.Context(), store.CreateAgentInput{
		AgentKey:     req.AgentKey,
		Domain:       req.Domain,
		Description:  req.Description,
		Version:      req.Version,
		InputSchema:  req.InputSchema,
		OutputSchema: req.OutputSchema,
		Skills:       req.Skills,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "AgentCreated", "agent.create", "SUCCESS", "agent",
		stringify(out["id"]), ids.ID{}, map[string]any{"agent_key": req.AgentKey, "idempotency_key": idempotencyKey(r)})
	h.writeJSON(w, http.StatusCreated, out)
}

// publishAgentRequest 是 POST /api/v1/agents/{id}/publish 的请求体。
type publishAgentRequest struct {
	Version string `json:"version,omitempty"`
}

// PublishAgent 处理 POST /api/v1/agents/{id}/publish。
func (h *Handler) PublishAgent(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	agentID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var req publishAgentRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	out, err := h.deps.Store.PublishAgent(r.Context(), agentID, req.Version)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "AgentPublished", "agent.publish", "SUCCESS", "agent", agentID.String(), ids.ID{},
		map[string]any{"version": req.Version, "idempotency_key": idempotencyKey(r)})
	h.publish(id.TenantID.String(), "AgentPublished", sse.Important, map[string]any{"agent_id": agentID.String()})
	h.writeJSON(w, http.StatusOK, out)
}

// stringify 安全地把 any 转为字符串（用于审计资源 ID）。
func stringify(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
