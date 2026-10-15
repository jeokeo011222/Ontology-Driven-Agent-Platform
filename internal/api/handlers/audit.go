package handlers

import (
	"net/http"
	"time"

	"odap/internal/api/store"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// ListAuditEvents 处理 GET /api/v1/audit-events（第十一篇）。
//
// 过滤参数：run_id / resource_type / action / event_type / outcome /
// occurred_after / limit / cursor。仅返回本租户事件（RLS + 显式租户事务）。
//
// outcome 为自由文本等值过滤（不枚举校验）：审计事件表的 outcome 列无 CHECK
// 约束，实际写入 SUCCESS / DENIED / REJECTED / ERROR 等取值，契约亦声明为
// 自由 string。此处只做原样匹配，不静默改写或丢弃用户给定的过滤条件。
func (h *Handler) ListAuditEvents(w http.ResponseWriter, r *http.Request) {
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
	q := r.URL.Query()
	filter := store.AuditFilter{
		ResourceType: q.Get("resource_type"),
		Action:       q.Get("action"),
		EventType:    q.Get("event_type"),
		Outcome:      q.Get("outcome"),
		ActorID:      q.Get("actor_id"),
		Limit:        limit,
		Cursor:       q.Get("cursor"),
	}
	if raw := q.Get("run_id"); raw != "" {
		runID, err := ids.Parse(raw)
		if err != nil {
			h.fail(w, r, errors.E(errors.CodeInvalidArgument, "run_id 必须是 UUID"))
			return
		}
		filter.RunID = &runID
	}
	if raw := q.Get("occurred_after"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			h.fail(w, r, errors.E(errors.CodeInvalidArgument, "occurred_after 必须是 RFC3339 时间"))
			return
		}
		filter.OccurredAfter = &t
	}
	res, err := h.deps.Store.ListAuditEvents(r.Context(), filter)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, res)
}
