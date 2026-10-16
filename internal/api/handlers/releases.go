package handlers

import (
	"net/http"

	"odap/internal/api/sse"
	"odap/internal/platform/ids"
)

// promoteReleaseRequest 是 POST /api/v1/releases/{id}/promote 的请求体（可为空）。
type promoteReleaseRequest struct {
	Environment string `json:"environment,omitempty"`
}

// rollbackReleaseRequest 是 POST /api/v1/releases/{id}/rollback 的请求体。
type rollbackReleaseRequest struct {
	TargetVersion string `json:"target_version,omitempty"`
}

// Releases 处理 GET /api/v1/releases —— 发布实体列表（真源 release_mgmt.releases）。
//
// 形状与前端 Release 类型对齐（state/gate_state/artifact），取代旧的
// quality_gate_decisions 视图（形状脱节曾让 promote/rollback 无从操作）。
func (h *Handler) Releases(w http.ResponseWriter, r *http.Request) {
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
	res, err := h.deps.Store.ListReleases(r.Context(), limit,
		r.URL.Query().Get("cursor"), r.URL.Query().Get("q"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, res)
}

// PromoteRelease 处理 POST /api/v1/releases/{id}/promote（§8.5 发布门禁）。
//
// 状态机 CAS：仅 gate_state=passed 的 candidate 可发布（条件 UPDATE 在数据库层
// 守卫，并发下不会重复发布）；门禁未过或状态不允许 → 409 并说明当前状态。
// 成功返回发布后的完整 Release（前端直接回填列表）。
func (h *Handler) PromoteRelease(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	releaseID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var req promoteReleaseRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	out, err := h.deps.Store.PromoteRelease(r.Context(), id.TenantID, releaseID, req.Environment)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "ReleasePromoted", "release.promote", "SUCCESS",
		"release", releaseID.String(), ids.ID{},
		map[string]any{"release_id": releaseID.String(),
			"artifact_id": out["artifact_id"], "version": out["version"],
			"idempotency_key": idempotencyKey(r)})
	h.publish(id.TenantID.String(), "ReleaseUpdated", sse.Important,
		map[string]any{"release_id": releaseID.String(), "state": out["state"]})
	h.writeJSON(w, http.StatusOK, out)
}

// RollbackRelease 处理 POST /api/v1/releases/{id}/rollback。
//
// 仅 promoted 状态可回滚（fail-closed）。target_version 对齐前端语义：
// 页面固定传 'previous_published'，服务端原样留痕进响应（rollback_target），
// 供后续"依赖该版本的运行是否需重跑"评估链路消费。
func (h *Handler) RollbackRelease(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	releaseID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var req rollbackReleaseRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	out, err := h.deps.Store.RollbackRelease(r.Context(), id.TenantID, releaseID, req.TargetVersion)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "ReleaseRolledBack", "release.rollback", "SUCCESS",
		"release", releaseID.String(), ids.ID{},
		map[string]any{"release_id": releaseID.String(),
			"artifact_id": out["artifact_id"], "version": out["version"],
			"rollback_target": req.TargetVersion, "idempotency_key": idempotencyKey(r)})
	h.publish(id.TenantID.String(), "ReleaseUpdated", sse.Important,
		map[string]any{"release_id": releaseID.String(), "state": out["state"]})
	h.writeJSON(w, http.StatusOK, out)
}
