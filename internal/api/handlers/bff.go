package handlers

import (
	"net/http"
)

// 本文件实现第十七篇 §11.1 的前端聚合端点（BFF）。这些端点由 BFF 聚合层
// 实现并登记进 contracts/openapi.yaml（contracts/03 §4 裁决）。所有聚合都在
// 服务端完成，前端不做跨资源拼装，且均受租户隔离约束。

// WorkspaceSummary 处理 GET /api/v1/workspace/summary。
func (h *Handler) WorkspaceSummary(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	out, err := h.deps.Store.WorkspaceSummary(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, out)
}

// Apps 处理 GET /api/v1/apps。
func (h *Handler) Apps(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	out, err := h.deps.Store.Apps(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, out)
}

// WorkspaceOntologies 处理 GET /api/v1/workspace/ontologies。
//
// 业务工作台需要「管理端设计了哪些本体」这一份清单来让用户选择（需求 1a）。
// 这里刻意与管理端列表口径一致（含 DRAFT/REJECTED 等未发布版本）：业务助手的
// 价值是「按本体规则审查」，用户必须先能选中本体；如果只暴露已发布的子集，
// 界面上会出现「管理端有 5 个本体、工作台只看到 2 个」——用户会认为系统丢数据。
// 直接复用 ListOntologies 的实现，避免两处口径各自演化。
func (h *Handler) WorkspaceOntologies(w http.ResponseWriter, r *http.Request) {
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
	out, err := h.deps.Store.ListOntologies(r.Context(), limit, r.URL.Query().Get("cursor"), r.URL.Query().Get("status"), r.URL.Query().Get("q"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, out)
}

// AdminOverview 处理 GET /api/v1/admin/overview。
func (h *Handler) AdminOverview(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	out, err := h.deps.Store.AdminOverview(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, out)
}

// AdminRoles 处理 GET /api/v1/admin/roles（cursor 分页 + 可选关键字）。
func (h *Handler) AdminRoles(w http.ResponseWriter, r *http.Request) {
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
	res, err := h.deps.Store.AdminRoles(r.Context(), limit,
		r.URL.Query().Get("cursor"), r.URL.Query().Get("q"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, res)
}

// AdminRoleBindings 处理 GET /api/v1/admin/role-bindings（cursor 分页 + 可选关键字）。
func (h *Handler) AdminRoleBindings(w http.ResponseWriter, r *http.Request) {
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
	res, err := h.deps.Store.AdminRoleBindings(r.Context(), limit,
		r.URL.Query().Get("cursor"), r.URL.Query().Get("q"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, res)
}

// AdminModelProviders 处理 GET /api/v1/admin/model-providers。
func (h *Handler) AdminModelProviders(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	out, err := h.deps.Store.AdminModelProviders(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, out)
}

// AdminConnectors 处理 GET /api/v1/admin/connectors（cursor 分页 + 可选关键字）。
func (h *Handler) AdminConnectors(w http.ResponseWriter, r *http.Request) {
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
	res, err := h.deps.Store.AdminConnectors(r.Context(), limit,
		r.URL.Query().Get("cursor"), r.URL.Query().Get("q"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, res)
}

// OpsAlerts 处理 GET /api/v1/ops/alerts。
func (h *Handler) OpsAlerts(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	out, err := h.deps.Store.OpsAlerts(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, out)
}

// Releases 处理 GET /api/v1/releases（cursor 分页 + 可选关键字）。
// 真源是 release_mgmt.releases（发布实体，000040 起）；实现见 handlers/releases.go。
