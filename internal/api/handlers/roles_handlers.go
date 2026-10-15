package handlers

import (
	"net/http"

	"odap/internal/platform/ids"
)

// 本文件实现「身份与权限」所需的角色管理写操作（第十七篇 §8.5）：
//   - POST   /api/v1/admin/roles            新建自定义角色（人工重建）
//   - DELETE /api/v1/admin/roles/{id}       删除自定义角色（内置/被引用 → 409）
//   - GET    /api/v1/admin/permissions      读取全局权限目录（供角色创建选择真实权限键）
//
// 平台只内置一个系统管理员角色；其余角色均由管理员在此人工创建，符合
// "只有系统管理员是内置，其他都是人工重建" 的口径。

// AdminCreateRole 处理 POST /api/v1/admin/roles。
func (h *Handler) AdminCreateRole(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	var body struct {
		RoleKey     string   `json:"role_key"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Permissions []string `json:"permissions"`
	}
	if err := decodeJSON(r, &body); err != nil {
		h.fail(w, r, err)
		return
	}
	item, err := h.deps.Store.CreateRole(r.Context(), body.RoleKey, body.Name, body.Description, body.Permissions)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	roleID, _ := item["id"].(string)
	h.recordAudit(r, id, "AdminRoleCreated", "iam.admin.role.create", "SUCCESS",
		"role", roleID, ids.ID{}, map[string]any{"role_key": body.RoleKey})
	writeJSON(w, http.StatusCreated, item)
}

// AdminDeleteRole 处理 DELETE /api/v1/admin/roles/{id}。
func (h *Handler) AdminDeleteRole(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	rid, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.deps.Store.DeleteRole(r.Context(), rid); err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "AdminRoleDeleted", "iam.admin.role.delete", "SUCCESS",
		"role", rid.String(), ids.ID{}, map[string]any{})
	w.WriteHeader(http.StatusNoContent)
}

// AdminPermissions 处理 GET /api/v1/admin/permissions。
func (h *Handler) AdminPermissions(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	out, err := h.deps.Store.ListPermissions(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, out)
}
