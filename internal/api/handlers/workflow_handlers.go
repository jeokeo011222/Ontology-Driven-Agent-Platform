// Package handlers — 插件式工作流（plugin_kind='WORKFLOW'）HTTP 处理器。
//
// 与 agents.go / tools 完全同构：列表走 cursor 分页（默认 10 行），
// 创建时同时登记插件与插件版本，版本升级通过 publish 发布。
// 工作流定义（definition）是编排文档（节点/边/触发器）的 JSON。
package handlers

import (
	"encoding/json"
	"net/http"

	"odap/internal/api/sse"
	"odap/internal/api/store"
	"odap/internal/platform/ids"
)

// createWorkflowRequest 是 POST /api/v1/workflows 的请求体。
type createWorkflowRequest struct {
	WorkflowKey  string          `json:"workflow_key"`
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	Version      string          `json:"version,omitempty"`
	Definition   json.RawMessage `json:"definition"`
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
}

// ListWorkflows 处理 GET /api/v1/workflows。
func (h *Handler) ListWorkflows(w http.ResponseWriter, r *http.Request) {
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
	res, err := h.deps.Store.ListWorkflows(r.Context(), limit, r.URL.Query().Get("cursor"), r.URL.Query().Get("status"), r.URL.Query().Get("q"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, res)
}

// CreateWorkflow 处理 POST /api/v1/workflows。
func (h *Handler) CreateWorkflow(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	var req createWorkflowRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	out, err := h.deps.Store.CreateWorkflow(r.Context(), store.CreateWorkflowInput{
		WorkflowKey:  req.WorkflowKey,
		Name:         req.Name,
		Description:  req.Description,
		Version:      req.Version,
		Definition:   req.Definition,
		InputSchema:  req.InputSchema,
		OutputSchema: req.OutputSchema,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "WorkflowCreated", "workflow.create", "SUCCESS", "workflow",
		stringify(out["id"]), ids.ID{}, map[string]any{"workflow_key": req.WorkflowKey, "idempotency_key": idempotencyKey(r)})
	h.publish(id.TenantID.String(), "WorkflowCreated", sse.Important,
		map[string]any{"workflow_id": stringify(out["id"])})
	h.writeJSON(w, http.StatusCreated, out)
}

// GetWorkflow 处理 GET /api/v1/workflows/{id}。
func (h *Handler) GetWorkflow(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	workflowID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out, err := h.deps.Store.GetWorkflow(r.Context(), workflowID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, out)
}

// updateWorkflowRequest 是 PATCH /api/v1/workflows/{id} 的请求体。
type updateWorkflowRequest struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// UpdateWorkflow 处理 PATCH /api/v1/workflows/{id}。
func (h *Handler) UpdateWorkflow(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	workflowID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var req updateWorkflowRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	out, err := h.deps.Store.UpdateWorkflow(r.Context(), workflowID, req.Name, req.Description)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "WorkflowUpdated", "workflow.update", "SUCCESS", "workflow",
		workflowID.String(), ids.ID{}, map[string]any{"idempotency_key": idempotencyKey(r)})
	h.writeJSON(w, http.StatusOK, out)
}

// DeleteWorkflow 处理 DELETE /api/v1/workflows/{id}（软删除）。
func (h *Handler) DeleteWorkflow(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	workflowID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	if err := h.deps.Store.DeleteWorkflow(r.Context(), workflowID); err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "WorkflowDeleted", "workflow.delete", "SUCCESS", "workflow",
		workflowID.String(), ids.ID{}, map[string]any{"idempotency_key": idempotencyKey(r)})
	h.publish(id.TenantID.String(), "WorkflowDeleted", sse.Important, map[string]any{"workflow_id": workflowID.String()})
	w.WriteHeader(http.StatusNoContent)
}

// publishWorkflowRequest 是 POST /api/v1/workflows/{id}/publish 的请求体。
type publishWorkflowRequest struct {
	Version string `json:"version,omitempty"`
}

// PublishWorkflow 处理 POST /api/v1/workflows/{id}/publish。
func (h *Handler) PublishWorkflow(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	workflowID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var req publishWorkflowRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	out, err := h.deps.Store.PublishWorkflow(r.Context(), workflowID, req.Version)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "WorkflowPublished", "workflow.publish", "SUCCESS", "workflow",
		workflowID.String(), ids.ID{}, map[string]any{"version": req.Version, "idempotency_key": idempotencyKey(r)})
	h.publish(id.TenantID.String(), "WorkflowPublished", sse.Important, map[string]any{"workflow_id": workflowID.String()})
	h.writeJSON(w, http.StatusOK, out)
}
