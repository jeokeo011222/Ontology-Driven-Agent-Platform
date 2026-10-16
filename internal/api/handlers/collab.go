package handlers

import (
	"log/slog"
	"net/http"

	"odap/internal/api/middleware"
	"odap/internal/collab"

	"github.com/gorilla/websocket"
)

// OntologyCollabWS 是本体协同编辑 WebSocket 端点。
// 客户端为 y-websocket provider：二进制帧原样中继，CRDT 同步由客户端自身完成。
func (h *Handler) OntologyCollabWS(w http.ResponseWriter, r *http.Request) {
	ontologyID := r.PathValue("ontologyId")
	if ontologyID == "" {
		http.Error(w, "ontologyId required", http.StatusBadRequest)
		return
	}
	var userID string
	if id, err := middleware.IdentityFromContext(r.Context()); err == nil {
		userID = id.SubjectID.String()
	}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		if h.deps.Logger != nil {
			h.deps.Logger.Warn("collab ws upgrade failed", "err", err)
		} else {
			slog.Warn("collab ws upgrade failed", "err", err)
		}
		return
	}
	client := collab.NewClient(ontologyID, userID, ws)
	if client == nil {
		_ = ws.Close()
		return
	}
	go client.ReadLoop()
	go client.WriteLoop()
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool { return true }, // CORS middleware 前置过滤
}
