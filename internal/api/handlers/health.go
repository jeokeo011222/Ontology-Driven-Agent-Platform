package handlers

import (
	"net/http"
	"time"
)

// Healthz 处理 GET /healthz（存活探针：进程存活即 200，不触达依赖）。
func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	h.writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"service": "api",
		"version": h.deps.Version,
		"time":    time.Now().UTC(),
	})
}

// Readyz 处理 GET /readyz（就绪探针：探测数据库等依赖）。
//
// 未配置探针时返回 503，而不是伪造 200（诚实性约束）。
func (h *Handler) Readyz(w http.ResponseWriter, r *http.Request) {
	if h.deps.ReadyFunc == nil {
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "unavailable",
			"checks": map[string]any{"probe": "not_configured"},
		})
		return
	}
	if err := h.deps.ReadyFunc(r.Context()); err != nil {
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "unavailable",
			"checks": map[string]any{"database": "unavailable"},
		})
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"checks": map[string]any{"database": "ok"},
	})
}
