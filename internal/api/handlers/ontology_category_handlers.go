package handlers

import (
	"net/http"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// --- Ontology Categories（管理员权限 admin:ontology:write）---

// ListOntologyCategories 处理 GET /api/v1/admin/ontology-categories。
func (h *Handler) ListOntologyCategories(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	res, err := h.deps.Store.ListOntologyCategories(r.Context(), id.TenantID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"items": res})
}

// CreateOntologyCategoryRequest 是新建本体分类的请求体。
type CreateOntologyCategoryRequest struct {
	ParentID     string `json:"parent_id,omitempty"`
	CategoryCode string `json:"category_code"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	SortOrder    int    `json:"sort_order"`
}

// CreateOntologyCategory 处理 POST /api/v1/admin/ontology-categories。
func (h *Handler) CreateOntologyCategory(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	var req CreateOntologyCategoryRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)

	var parentID ids.ID
	if req.ParentID != "" {
		parentID, err = ids.Parse(req.ParentID)
		if err != nil {
			h.fail(w, r, errors.E(errors.CodeInvalidArgument, "parent_id 非法"))
			return
		}
	}

	newID, err := h.deps.Store.CreateOntologyCategory(r.Context(), id.TenantID, parentID,
		req.CategoryCode, req.Name, req.Description, req.SortOrder)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	h.recordAudit(r, id, "OntologyCategoryCreated", "ontology.category.create", "SUCCESS",
		"ontology_category", newID.String(), ids.ID{},
		map[string]any{"category_code": req.CategoryCode, "idempotency_key": idempotencyKey(r)})
	h.writeJSON(w, http.StatusCreated, map[string]any{"id": newID.String()})
}

// UpdateOntologyCategory 处理 PATCH /api/v1/admin/ontology-categories/{id}。
// 更新字段白名单：name / description / sort_order / status。
func (h *Handler) UpdateOntologyCategory(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	catID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var body map[string]any
	if err := decodeOptionalJSON(r, &body); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)

	// 白名单过滤
	allowed := map[string]bool{"name": true, "description": true, "sort_order": true, "status": true}
	updates := map[string]any{}
	for k, v := range body {
		if allowed[k] {
			updates[k] = v
		}
	}
	if len(updates) == 0 {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "没有可更新字段"))
		return
	}
	// sort_order 转 int（JSON 数字默认 float64）
	if v, ok := updates["sort_order"].(float64); ok {
		updates["sort_order"] = int(v)
	}

	if err := h.deps.Store.UpdateOntologyCategory(r.Context(), id.TenantID, catID, updates); err != nil {
		h.fail(w, r, err)
		return
	}

	h.recordAudit(r, id, "OntologyCategoryUpdated", "ontology.category.update", "SUCCESS",
		"ontology_category", catID.String(), ids.ID{},
		map[string]any{"updated_fields": jsonKeys(updates), "idempotency_key": idempotencyKey(r)})
	w.WriteHeader(http.StatusNoContent)
}

// DeleteOntologyCategory 处理 DELETE /api/v1/admin/ontology-categories/{id}。
// 有本体引用或有子分类时返回 409 CONFLICT。
func (h *Handler) DeleteOntologyCategory(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	catID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}

	if err := h.deps.Store.DeleteOntologyCategory(r.Context(), id.TenantID, catID); err != nil {
		h.fail(w, r, err)
		return
	}

	h.recordAudit(r, id, "OntologyCategoryDeleted", "ontology.category.delete", "SUCCESS",
		"ontology_category", catID.String(), ids.ID{}, map[string]any{})
	w.WriteHeader(http.StatusNoContent)
}

func jsonKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
