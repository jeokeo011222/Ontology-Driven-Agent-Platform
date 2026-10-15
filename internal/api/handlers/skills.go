package handlers

import (
	"encoding/json"
	"net/http"

	"odap/internal/api/sse"
	"odap/internal/api/store"
	"odap/internal/platform/ids"
)

// createSkillRequest 是 POST /api/v1/skills 的请求体。
type createSkillRequest struct {
	SkillKey       string          `json:"skill_key"`
	Name           string          `json:"name"`
	Domain         string          `json:"domain,omitempty"`
	Description    string          `json:"description,omitempty"`
	Version        string          `json:"version,omitempty"`
	PromptFragment string          `json:"prompt_fragment,omitempty"`
	ToolBindings   []string        `json:"tool_bindings,omitempty"`
	InputSchema    json.RawMessage `json:"input_schema"`
	OutputSchema   json.RawMessage `json:"output_schema"`
}

// ListSkills 处理 GET /api/v1/skills（支持 ?status= 与 ?q= 过滤）。
func (h *Handler) ListSkills(w http.ResponseWriter, r *http.Request) {
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
	res, err := h.deps.Store.ListSkills(r.Context(), limit,
		r.URL.Query().Get("cursor"), r.URL.Query().Get("status"), r.URL.Query().Get("q"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, res)
}

// CreateSkill 处理 POST /api/v1/skills（含首个 DRAFT 版本）。
func (h *Handler) CreateSkill(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	var req createSkillRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	out, err := h.deps.Store.CreateSkill(r.Context(), store.CreateSkillInput{
		SkillKey:       req.SkillKey,
		Name:           req.Name,
		Domain:         req.Domain,
		Description:    req.Description,
		Version:        req.Version,
		PromptFragment: req.PromptFragment,
		ToolBindings:   req.ToolBindings,
		InputSchema:    req.InputSchema,
		OutputSchema:   req.OutputSchema,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "SkillCreated", "skill.create", "SUCCESS", "skill",
		stringify(out["id"]), ids.ID{}, map[string]any{"skill_key": req.SkillKey, "idempotency_key": idempotencyKey(r)})
	h.writeJSON(w, http.StatusCreated, out)
}

// GetSkill 处理 GET /api/v1/skills/{id}。
func (h *Handler) GetSkill(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	skillID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out, err := h.deps.Store.GetSkill(r.Context(), skillID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, out)
}

// updateSkillRequest 是 PATCH /api/v1/skills/{id} 的请求体（PATCH 语义：空值不改动）。
type updateSkillRequest struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Domain      string `json:"domain,omitempty"`
	Status      string `json:"status,omitempty"`
}

// UpdateSkill 处理 PATCH /api/v1/skills/{id}。
func (h *Handler) UpdateSkill(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	skillID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var req updateSkillRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	out, err := h.deps.Store.UpdateSkill(r.Context(), skillID, req.Name, req.Description, req.Domain, req.Status)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "SkillUpdated", "skill.update", "SUCCESS", "skill", skillID.String(), ids.ID{},
		map[string]any{"skill_key": mapStr(out["skill_key"]), "idempotency_key": idempotencyKey(r)})
	h.writeJSON(w, http.StatusOK, out)
}

// DeleteSkill 处理 DELETE /api/v1/skills/{id}（软删除）。
func (h *Handler) DeleteSkill(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	skillID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	if err := h.deps.Store.DeleteSkill(r.Context(), skillID); err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "SkillDeleted", "skill.delete", "SUCCESS", "skill", skillID.String(), ids.ID{},
		map[string]any{"idempotency_key": idempotencyKey(r)})
	h.publish(id.TenantID.String(), "SkillDeleted", sse.Important, map[string]any{"skill_id": skillID.String()})
	h.writeJSON(w, http.StatusOK, map[string]any{"id": skillID.String(), "status": "DELETED"})
}

// PublishSkill 处理 POST /api/v1/skills/{id}/publish（最新版本 DRAFT → PUBLISHED）。
//
// 发布门禁 fail-closed：最新版本不存在或已发布（非 DRAFT）一律 409，
// 绝不静默放行 —— 未发布的技能版本不得被 Agent 依赖。
func (h *Handler) PublishSkill(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	skillID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	out, err := h.deps.Store.PublishSkill(r.Context(), skillID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "SkillPublished", "skill.publish", "SUCCESS", "skill", skillID.String(), ids.ID{},
		map[string]any{"version_id": mapStr(out["version_id"]), "idempotency_key": idempotencyKey(r)})
	h.publish(id.TenantID.String(), "SkillPublished", sse.Important, map[string]any{"skill_id": skillID.String()})
	h.writeJSON(w, http.StatusOK, out)
}

// ListSkillVersions 处理 GET /api/v1/skills/{id}/versions。
func (h *Handler) ListSkillVersions(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	skillID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	res, err := h.deps.Store.ListSkillVersions(r.Context(), skillID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, res)
}

// skillVersionRequest 是 POST /api/v1/skills/{id}/versions 的请求体
// （为技能追加一个 DRAFT 版本；发布仍走 /publish）。
type skillVersionRequest struct {
	Version        string          `json:"version"`
	PromptFragment string          `json:"prompt_fragment,omitempty"`
	ToolBindings   []string        `json:"tool_bindings,omitempty"`
	InputSchema    json.RawMessage `json:"input_schema"`
	OutputSchema   json.RawMessage `json:"output_schema"`
}

// CreateSkillVersion 处理 POST /api/v1/skills/{id}/versions。
// 工具绑定校验 fail-closed（每个 tool_key 必须指向 ACTIVE 工具）；内置技能 409。
func (h *Handler) CreateSkillVersion(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	skillID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var req skillVersionRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	out, err := h.deps.Store.CreateSkillVersion(r.Context(), skillID, store.CreateSkillVersionInput{
		Version:        req.Version,
		PromptFragment: req.PromptFragment,
		ToolBindings:   req.ToolBindings,
		InputSchema:    req.InputSchema,
		OutputSchema:   req.OutputSchema,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "SkillVersionCreated", "skill.version.create", "SUCCESS", "skill", skillID.String(), ids.ID{},
		map[string]any{"version_id": mapStr(out["version_id"]), "version": mapStr(out["version"]), "idempotency_key": idempotencyKey(r)})
	h.writeJSON(w, http.StatusCreated, out)
}
