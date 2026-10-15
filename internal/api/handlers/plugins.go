package handlers

import (
	"encoding/json"
	"net/http"

	"odap/internal/api/sse"
	"odap/internal/api/store"
	"odap/internal/platform/ids"
)

// createPluginRequest 是 POST /api/v1/plugins 的请求体。
type createPluginRequest struct {
	PluginKey   string          `json:"plugin_key"`
	PluginKind  string          `json:"plugin_kind"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Version     string          `json:"version,omitempty"`
	Manifest    json.RawMessage `json:"manifest"`
}

// ListPlugins 处理 GET /api/v1/plugins。
func (h *Handler) ListPlugins(w http.ResponseWriter, r *http.Request) {
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
	res, err := h.deps.Store.ListPlugins(r.Context(), limit, r.URL.Query().Get("cursor"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, res)
}

// CreatePlugin 处理 POST /api/v1/plugins。
func (h *Handler) CreatePlugin(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	var req createPluginRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	out, err := h.deps.Store.CreatePlugin(r.Context(), store.CreatePluginInput{
		PluginKey:   req.PluginKey,
		PluginKind:  req.PluginKind,
		Name:        req.Name,
		Description: req.Description,
		Version:     req.Version,
		Manifest:    req.Manifest,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "PluginRegistered", "plugin.register", "SUCCESS", "plugin",
		stringify(out["id"]), ids.ID{}, map[string]any{"plugin_key": req.PluginKey, "idempotency_key": idempotencyKey(r)})
	h.writeJSON(w, http.StatusCreated, out)
}

// enablePluginRequest 是 POST /api/v1/plugins/{id}/enable 的请求体。
type enablePluginRequest struct {
	Config json.RawMessage `json:"config,omitempty"`
}

// EnablePlugin 处理 POST /api/v1/plugins/{id}/enable（租户启用）。
func (h *Handler) EnablePlugin(w http.ResponseWriter, r *http.Request) {
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
	var req enablePluginRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	out, err := h.deps.Store.EnablePlugin(r.Context(), pluginID, req.Config)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "PluginEnabled", "plugin.install", "SUCCESS", "plugin", pluginID.String(), ids.ID{},
		map[string]any{"idempotency_key": idempotencyKey(r)})
	h.publish(id.TenantID.String(), "PluginEnabled", sse.Important, map[string]any{"plugin_id": pluginID.String()})
	h.writeJSON(w, http.StatusOK, out)
}
