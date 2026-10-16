package handlers

// Chat Sessions（迁移 000046）的 HTTP 端点。
//
// 路由（由 router.go 装配，位于 // Run 资源 之后）：
//   POST   /api/v1/chat/sessions                          → CreateChatSession
//   GET    /api/v1/chat/sessions                          → ListChatSessions
//   GET    /api/v1/chat/sessions/{id}                     → GetChatSession
//   PATCH  /api/v1/chat/sessions/{id}                     → UpdateChatSession
//   DELETE /api/v1/chat/sessions/{id}                     → DeleteChatSession
//   POST   /api/v1/chat/sessions/{id}/archive             → ArchiveChatSession
//   POST   /api/v1/chat/sessions/{id}/messages            → AddChatMessage
//   GET    /api/v1/chat/sessions/{id}/messages            → ListChatMessages
//
// 关键约定：
//   * 所有端点走 Auth + RequireIdentity 链，subject_id 从信任身份取，
//     绝不接受请求体覆盖（第十六篇 §5.4）；
//   * 创建/更新/归档/删除四条写路径必须同时落审计（recordAudit）+
//     发布 SSE 事件（publish）+ 回写 Idempotency-Key 头（echoIdempotency）；
//   * 审计写入失败不阻断业务（best-effort，handlers.go §recordAudit 注释）；
//   * Store 未装配时所有端点返回 UNAVAILABLE（fail-closed，绝不伪造假成功）。

import (
	"net/http"

	"odap/internal/api/sse"
	"odap/internal/api/store"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/tenantctx"
)

// --- 请求体 DTO ---

// createChatSessionRequest 是 POST /chat/sessions 的请求体。
//
// subject_id 不在请求体内——由 handler 从身份上下文提取，防止客户端伪造。
type createChatSessionRequest struct {
	Title      string `json:"title"`
	OntologyID string `json:"ontology_id,omitempty"`
}

// updateChatSessionRequest 是 PATCH /chat/sessions/{id} 的请求体。
type updateChatSessionRequest struct {
	Title string `json:"title"`
}

// addChatMessageRequest 是 POST /chat/sessions/{id}/messages 的请求体。
type addChatMessageRequest struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	RunID   string `json:"run_id,omitempty"`
}

// --- 公共前置 ---

// requireChatDeps 检查身份 + Store 可用性（chat 模块唯一依赖）。
func (h *Handler) requireChatDeps(w http.ResponseWriter, r *http.Request) (tenantctx.Identity, bool) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return tenantctx.Identity{}, false
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配，聊天会话不可用"))
		return tenantctx.Identity{}, false
	}
	return id, true
}

// ---------------------------------------------------------------------------
// POST /api/v1/chat/sessions
// ---------------------------------------------------------------------------

func (h *Handler) CreateChatSession(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireChatDeps(w, r)
	if !ok {
		return
	}
	var req createChatSessionRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	if req.Title == "" {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "title 不能为空"))
		return
	}

	var ontologyID ids.ID
	if req.OntologyID != "" {
		var err error
		ontologyID, err = ids.Parse(req.OntologyID)
		if err != nil {
			h.fail(w, r, errors.E(errors.CodeInvalidArgument, "ontology_id 必须是 UUID"))
			return
		}
	}

	echoIdempotency(w, r)
	cs, err := h.deps.Store.CreateChatSession(r.Context(), id.SubjectID, ontologyID, req.Title)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	h.recordAudit(r, id, "ChatSessionCreated", "chat_session.create", "SUCCESS",
		"chat_session", cs.ID, ids.ID{},
		map[string]any{"subject_id": id.SubjectID.String(), "ontology_id": cs.OntologyID})
	h.publish(id.TenantID.String(), "chat_session.created", sse.Normal,
		map[string]any{"session_id": cs.ID, "subject_id": id.SubjectID.String()})

	h.writeJSON(w, http.StatusCreated, cs)
}

// ---------------------------------------------------------------------------
// GET /api/v1/chat/sessions
// ---------------------------------------------------------------------------

func (h *Handler) ListChatSessions(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireChatDeps(w, r)
	if !ok {
		return
	}
	limit, err := limitParam(r, 10)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && status != "ACTIVE" && status != "ARCHIVED" {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "status 必须是 ACTIVE 或 ARCHIVED"))
		return
	}

	items, next, err := h.deps.Store.ListChatSessions(r.Context(), store.ChatSessionListFilter{
		SubjectID: id.SubjectID,
		Status:    status,
		Limit:     limit,
		Cursor:    r.URL.Query().Get("cursor"),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if items == nil {
		items = []store.ChatSession{}
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

// ---------------------------------------------------------------------------
// GET /api/v1/chat/sessions/{id}
// ---------------------------------------------------------------------------

func (h *Handler) GetChatSession(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireChatDeps(w, r)
	if !ok {
		return
	}
	sessionID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}

	cs, err := h.deps.Store.GetChatSession(r.Context(), sessionID)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// 归属校验：会话必须属于当前 subject（RLS 只隔离了租户，没有隔离 subject，
	// 因此应用层必须二次校验，防止同租户下用户 A 读取用户 B 的会话）。
	if cs.SubjectID != id.SubjectID.String() {
		h.fail(w, r, errors.E(errors.CodePermissionDenied, "无权访问该聊天会话"))
		return
	}

	h.writeJSON(w, http.StatusOK, cs)
}

// ---------------------------------------------------------------------------
// PATCH /api/v1/chat/sessions/{id}
// ---------------------------------------------------------------------------

func (h *Handler) UpdateChatSession(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireChatDeps(w, r)
	if !ok {
		return
	}
	sessionID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var req updateChatSessionRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	if req.Title == "" {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "title 不能为空"))
		return
	}

	// 归属校验（同 GetChatSession：RLS 只管租户，subject 归属由应用层保障）。
	cs, err := h.deps.Store.GetChatSession(r.Context(), sessionID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if cs.SubjectID != id.SubjectID.String() {
		h.fail(w, r, errors.E(errors.CodePermissionDenied, "无权修改该聊天会话"))
		return
	}

	echoIdempotency(w, r)
	if err := h.deps.Store.UpdateChatSession(r.Context(), sessionID, req.Title); err != nil {
		h.fail(w, r, err)
		return
	}

	h.recordAudit(r, id, "ChatSessionUpdated", "chat_session.update", "SUCCESS",
		"chat_session", sessionID.String(), ids.ID{},
		map[string]any{"title": req.Title})
	h.publish(id.TenantID.String(), "chat_session.updated", sse.Normal,
		map[string]any{"session_id": sessionID.String()})

	// 返回更新后的完整对象，供前端刷新视图。
	updated, err := h.deps.Store.GetChatSession(r.Context(), sessionID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, updated)
}

// ---------------------------------------------------------------------------
// DELETE /api/v1/chat/sessions/{id}
// ---------------------------------------------------------------------------

func (h *Handler) DeleteChatSession(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireChatDeps(w, r)
	if !ok {
		return
	}
	sessionID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// 归属校验。
	cs, err := h.deps.Store.GetChatSession(r.Context(), sessionID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if cs.SubjectID != id.SubjectID.String() {
		h.fail(w, r, errors.E(errors.CodePermissionDenied, "无权删除该聊天会话"))
		return
	}

	echoIdempotency(w, r)
	if err := h.deps.Store.DeleteChatSession(r.Context(), sessionID); err != nil {
		h.fail(w, r, err)
		return
	}

	h.recordAudit(r, id, "ChatSessionDeleted", "chat_session.delete", "SUCCESS",
		"chat_session", sessionID.String(), ids.ID{}, nil)
	h.publish(id.TenantID.String(), "chat_session.deleted", sse.Normal,
		map[string]any{"session_id": sessionID.String()})

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// POST /api/v1/chat/sessions/{id}/archive
// ---------------------------------------------------------------------------

func (h *Handler) ArchiveChatSession(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireChatDeps(w, r)
	if !ok {
		return
	}
	sessionID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// 归属校验。
	cs, err := h.deps.Store.GetChatSession(r.Context(), sessionID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if cs.SubjectID != id.SubjectID.String() {
		h.fail(w, r, errors.E(errors.CodePermissionDenied, "无权归档该聊天会话"))
		return
	}

	echoIdempotency(w, r)
	if err := h.deps.Store.ArchiveChatSession(r.Context(), sessionID); err != nil {
		h.fail(w, r, err)
		return
	}

	h.recordAudit(r, id, "ChatSessionArchived", "chat_session.archive", "SUCCESS",
		"chat_session", sessionID.String(), ids.ID{}, nil)
	h.publish(id.TenantID.String(), "chat_session.archived", sse.Normal,
		map[string]any{"session_id": sessionID.String()})

	// 返回更新后的会话（status=ARCHIVED, archived_at 已填）。
	updated, err := h.deps.Store.GetChatSession(r.Context(), sessionID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, updated)
}

// ---------------------------------------------------------------------------
// POST /api/v1/chat/sessions/{id}/messages
// ---------------------------------------------------------------------------

func (h *Handler) AddChatMessage(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireChatDeps(w, r)
	if !ok {
		return
	}
	sessionID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var req addChatMessageRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}

	if req.Role != "user" && req.Role != "assistant" && req.Role != "system" {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "role 必须是 user / assistant / system"))
		return
	}
	if req.Content == "" {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "content 不能为空"))
		return
	}

	var runID ids.ID
	if req.RunID != "" {
		runID, err = ids.Parse(req.RunID)
		if err != nil {
			h.fail(w, r, errors.E(errors.CodeInvalidArgument, "run_id 必须是 UUID"))
			return
		}
	}

	// 归属校验：消息只能写入自己的会话。
	cs, err := h.deps.Store.GetChatSession(r.Context(), sessionID)
	if err != nil {
		if errors.As(err).Code == errors.CodeNotFound {
			h.fail(w, r, errors.E(errors.CodeNotFound, "聊天会话不存在"))
			return
		}
		h.fail(w, r, err)
		return
	}
	if cs.SubjectID != id.SubjectID.String() {
		h.fail(w, r, errors.E(errors.CodePermissionDenied, "无权向该会话写入消息"))
		return
	}

	echoIdempotency(w, r)
	if err := h.deps.Store.AddChatMessage(r.Context(), sessionID, req.Role, req.Content, runID); err != nil {
		h.fail(w, r, err)
		return
	}

	h.recordAudit(r, id, "ChatMessageAdded", "chat_message.create", "SUCCESS",
		"chat_session", sessionID.String(), runID,
		map[string]any{"role": req.Role, "run_id": runID.String()})
	h.publish(id.TenantID.String(), "chat_message.added", sse.Normal,
		map[string]any{"session_id": sessionID.String(), "role": req.Role})

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// GET /api/v1/chat/sessions/{id}/messages
// ---------------------------------------------------------------------------

func (h *Handler) ListChatMessages(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireChatDeps(w, r)
	if !ok {
		return
	}
	sessionID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	limit, err := limitParam(r, 10)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// 归属校验（同 AddChatMessage）。
	cs, err := h.deps.Store.GetChatSession(r.Context(), sessionID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if cs.SubjectID != id.SubjectID.String() {
		h.fail(w, r, errors.E(errors.CodePermissionDenied, "无权读取该会话的消息"))
		return
	}

	items, next, err := h.deps.Store.ListChatMessages(r.Context(), store.ChatMessageListFilter{
		SessionID: sessionID,
		Limit:     limit,
		Cursor:    r.URL.Query().Get("cursor"),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if items == nil {
		items = []store.ChatMessage{}
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}
