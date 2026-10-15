// Package handlers — 配置类资源完整 CRUD handler。
//
// 补齐各资源在 agents.go / ontologies.go / plugins.go / tools.go /
// policies.go / model_providers.go 中仅实现的 List/Create 之外的
// GetOne / Update / Delete 处理方法。
package handlers

import (
	"net/http"

	"odap/internal/api/sse"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

/* =========================================================================
 * Agents — GET /api/v1/agents/{id}
 *         PATCH /api/v1/agents/{id}
 *         DELETE /api/v1/agents/{id}
 * ========================================================================= */

// GetAgent 处理 GET /api/v1/agents/{id}。
func (h *Handler) GetAgent(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	id, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out, err := h.deps.Store.GetAgent(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, out)
}

type updateAgentRequest struct {
	Description string `json:"description,omitempty"`
	Domain      string `json:"domain,omitempty"`
}

// UpdateAgent 处理 PATCH /api/v1/agents/{id}。
func (h *Handler) UpdateAgent(w http.ResponseWriter, r *http.Request) {
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
	var req updateAgentRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	out, err := h.deps.Store.UpdateAgent(r.Context(), agentID, req.Description, req.Domain)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "AgentUpdated", "agent.update", "SUCCESS", "agent",
		agentID.String(), ids.ID{}, map[string]any{"idempotency_key": idempotencyKey(r)})
	h.writeJSON(w, http.StatusOK, out)
}

// DeleteAgent 处理 DELETE /api/v1/agents/{id}（软删除）。
func (h *Handler) DeleteAgent(w http.ResponseWriter, r *http.Request) {
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
	echoIdempotency(w, r)
	if err := h.deps.Store.DeleteAgent(r.Context(), agentID); err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "AgentDeleted", "agent.delete", "SUCCESS", "agent",
		agentID.String(), ids.ID{}, map[string]any{"idempotency_key": idempotencyKey(r)})
	h.publish(id.TenantID.String(), "AgentDeleted", sse.Important, map[string]any{"agent_id": agentID.String()})
	w.WriteHeader(http.StatusNoContent)
}

/* =========================================================================
 * Ontologies — GET /api/v1/ontologies/{id}
 *              PATCH /api/v1/ontologies/{id}
 *              DELETE /api/v1/ontologies/{id}
 * ========================================================================= */

// GetOntology 处理 GET /api/v1/ontologies/{id}。
func (h *Handler) GetOntology(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	id, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out, err := h.deps.Store.GetOntology(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// 「类与关系」由服务端的 manifest 派生后随详情一起返回：页面需要的是
	// {iri,label,description,relations}，那是 manifest 的一种投影而非存储形态。
	// 派生点与校验、产物端点一致，三处对"类与关系"的理解不会漂移。
	// manifest 解析不了必须报错而不是给空数组——否则"清单坏了"会被显示成"尚未定义"。
	ontologyKey, ok := out["ontology_key"].(string)
	if !ok {
		h.fail(w, r, errors.E(errors.CodeInternal, "本体详情缺少 ontology_key，无法派生类与关系"))
		return
	}
	classes, err := ontologyClassesFromManifest(out["manifest"], ontologyKey)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out["classes"] = classes
	h.writeJSON(w, http.StatusOK, out)
}

type updateOntologyRequest struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	// Status 为启停状态：ACTIVE=启用、INACTIVE=停用；省略则不改动。
	Status string `json:"status,omitempty"`
}

// UpdateOntology 处理 PATCH /api/v1/ontologies/{id}。
func (h *Handler) UpdateOntology(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	ontologyID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var req updateOntologyRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	out, err := h.deps.Store.UpdateOntology(r.Context(), ontologyID, req.Name, req.Description, req.Status)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "OntologyUpdated", "ontology.update", "SUCCESS", "ontology",
		ontologyID.String(), ids.ID{}, map[string]any{"status": req.Status,
			"idempotency_key": idempotencyKey(r)})
	h.writeJSON(w, http.StatusOK, out)
}

// DeleteOntology 处理 DELETE /api/v1/ontologies/{id}（软删除）。
func (h *Handler) DeleteOntology(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	ontologyID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	if err := h.deps.Store.DeleteOntology(r.Context(), ontologyID); err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "OntologyDeleted", "ontology.delete", "SUCCESS", "ontology",
		ontologyID.String(), ids.ID{}, map[string]any{"idempotency_key": idempotencyKey(r)})
	h.publish(id.TenantID.String(), "OntologyDeleted", sse.Important, map[string]any{"ontology_id": ontologyID.String()})
	w.WriteHeader(http.StatusNoContent)
}

/* =========================================================================
 * Plugins — GET /api/v1/plugins/{id}
 *           PATCH /api/v1/plugins/{id}
 *           DELETE /api/v1/plugins/{id}
 * ========================================================================= */

// GetPlugin 处理 GET /api/v1/plugins/{id}。
func (h *Handler) GetPlugin(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	id, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out, err := h.deps.Store.GetPlugin(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, out)
}

type updatePluginRequest struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// UpdatePlugin 处理 PATCH /api/v1/plugins/{id}。
func (h *Handler) UpdatePlugin(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	pluginID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var req updatePluginRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	out, err := h.deps.Store.UpdatePlugin(r.Context(), pluginID, req.Name, req.Description)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "PluginUpdated", "plugin.update", "SUCCESS", "plugin",
		pluginID.String(), ids.ID{}, map[string]any{"idempotency_key": idempotencyKey(r)})
	h.writeJSON(w, http.StatusOK, out)
}

// DeletePlugin 处理 DELETE /api/v1/plugins/{id}（软删除 + 同步禁用安装记录）。
func (h *Handler) DeletePlugin(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	pluginID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	if err := h.deps.Store.DeletePlugin(r.Context(), pluginID); err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "PluginDeleted", "plugin.delete", "SUCCESS", "plugin",
		pluginID.String(), ids.ID{}, map[string]any{"idempotency_key": idempotencyKey(r)})
	h.publish(id.TenantID.String(), "PluginDeleted", sse.Important, map[string]any{"plugin_id": pluginID.String()})
	w.WriteHeader(http.StatusNoContent)
}

/* =========================================================================
 * Tools — GET /api/v1/tools/{id}
 *         PATCH /api/v1/tools/{id}
 *         DELETE /api/v1/tools/{id}
 * ========================================================================= */

// GetTool 处理 GET /api/v1/tools/{id}。
func (h *Handler) GetTool(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	id, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out, err := h.deps.Store.GetTool(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, out)
}

type updateToolRequest struct {
	Description string `json:"description,omitempty"`
}

// UpdateTool 处理 PATCH /api/v1/tools/{id}。
func (h *Handler) UpdateTool(w http.ResponseWriter, r *http.Request) {
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
	var req updateToolRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	out, err := h.deps.Store.UpdateTool(r.Context(), toolID, req.Description)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "ToolUpdated", "tool.update", "SUCCESS", "tool",
		toolID.String(), ids.ID{}, map[string]any{"idempotency_key": idempotencyKey(r)})
	h.writeJSON(w, http.StatusOK, out)
}

// DeleteTool 处理 DELETE /api/v1/tools/{id}（软删除）。
func (h *Handler) DeleteTool(w http.ResponseWriter, r *http.Request) {
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
	echoIdempotency(w, r)
	if err := h.deps.Store.DeleteTool(r.Context(), toolID); err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "ToolDeleted", "tool.delete", "SUCCESS", "tool",
		toolID.String(), ids.ID{}, map[string]any{"idempotency_key": idempotencyKey(r)})
	h.publish(id.TenantID.String(), "ToolDeleted", sse.Important, map[string]any{"tool_id": toolID.String()})
	w.WriteHeader(http.StatusNoContent)
}

/* =========================================================================
 * Policies — PATCH /api/v1/policies/{key}/versions/{version}  (状态更新)
 *            DELETE /api/v1/policies/{key}/versions/{version}  (草稿删除)
 *
 * 策略版本以 (policy_key, version) 为主键，不适用 UUID path 参数。
 * 版本详情查询已由 GET /api/v1/policies/{id}/versions 提供（policy_key 作 id）。
 * ========================================================================= */

type updatePolicyStatusRequest struct {
	Status string `json:"status"`
}

// UpdatePolicyStatus 处理 PATCH /api/v1/policies/{key}/versions/{version}。
func (h *Handler) UpdatePolicyStatus(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	policyKey := r.PathValue("key")
	version := r.PathValue("version")
	if policyKey == "" || version == "" {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "policy_key 与 version 必填"))
		return
	}
	var req updatePolicyStatusRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	out, err := h.deps.Store.UpdatePolicyStatus(r.Context(), policyKey, version, req.Status)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "PolicyStatusUpdated", "policy.update", "SUCCESS", "policy",
		policyKey, ids.ID{}, map[string]any{"version": version, "status": req.Status,
			"idempotency_key": idempotencyKey(r)})
	h.writeJSON(w, http.StatusOK, out)
}

// DeletePolicyVersion 处理 DELETE /api/v1/policies/{key}/versions/{version}。
func (h *Handler) DeletePolicyVersion(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	policyKey := r.PathValue("key")
	version := r.PathValue("version")
	if policyKey == "" || version == "" {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "policy_key 与 version 必填"))
		return
	}
	echoIdempotency(w, r)
	if err := h.deps.Store.DeletePolicyVersion(r.Context(), policyKey, version); err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "PolicyVersionDeleted", "policy.delete", "SUCCESS", "policy",
		policyKey, ids.ID{}, map[string]any{"version": version, "idempotency_key": idempotencyKey(r)})
	w.WriteHeader(http.StatusNoContent)
}

/* =========================================================================
 * ModelProviders — GET /api/v1/admin/model-providers/{id}
 *                  PATCH /api/v1/admin/model-providers/{id}
 *                  DELETE /api/v1/admin/model-providers/{id}
 *
 * 模型供应商 provider_id 为 TEXT 主键（llm 命名空间，平台级无 RLS），
 * 不走 TenantTx，但 handler 仍强制身份。
 * ========================================================================= */

// GetModelProvider 处理 GET /api/v1/admin/model-providers/{id}。
func (h *Handler) GetModelProvider(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	out, err := h.deps.Store.GetModelProvider(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, out)
}

type updateModelProviderRequest struct {
	DisplayName string `json:"display_name,omitempty"`
	Status      string `json:"status,omitempty"`
}

// UpdateModelProvider 处理 PATCH /api/v1/admin/model-providers/{id}。
func (h *Handler) UpdateModelProvider(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	providerID := r.PathValue("id")
	var req updateModelProviderRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	out, err := h.deps.Store.UpdateModelProvider(r.Context(), providerID, req.DisplayName, req.Status)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "ModelProviderUpdated", "model_provider.update", "SUCCESS",
		"model_provider", providerID, ids.ID{},
		map[string]any{"display_name": req.DisplayName, "status": req.Status,
			"idempotency_key": idempotencyKey(r)})
	h.writeJSON(w, http.StatusOK, out)
}

// DeleteModelProvider 处理 DELETE /api/v1/admin/model-providers/{id}。
func (h *Handler) DeleteModelProvider(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	providerID := r.PathValue("id")
	echoIdempotency(w, r)
	if err := h.deps.Store.DeleteModelProvider(r.Context(), providerID); err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "ModelProviderDeleted", "model_provider.delete", "SUCCESS",
		"model_provider", providerID, ids.ID{},
		map[string]any{"idempotency_key": idempotencyKey(r)})
	w.WriteHeader(http.StatusNoContent)
}
