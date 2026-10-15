package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"odap/internal/api/middleware"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/tenantctx"
)

// --- Auth endpoints（公开路由，不走 Auth 中间件）---

// RegisterRequest 注册请求。
type RegisterRequest struct {
	OrgCode     string `json:"org_code"`
	LoginName   string `json:"login_name"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Password    string `json:"password"`
}

// LoginRequest 登录请求。
type LoginRequest struct {
	LoginName string `json:"login_name"`
	Password  string `json:"password"`
}

// SessionResponse 登录/注册成功后的会话响应（含 token）。
type SessionResponse struct {
	SessionToken     string    `json:"session_token"`
	RefreshToken     string    `json:"refresh_token,omitempty"`
	ExpiresAt        time.Time `json:"expires_at"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
	SessionID        string    `json:"session_id"`
	TenantID         string    `json:"tenant_id"`
	SubjectID        string    `json:"subject_id"`
}

const (
	sessionTTL        = 2 * time.Hour
	sessionRefreshTTL = 7 * 24 * time.Hour
)

// setSessionCookie 下发会话 Cookie，使 EventSource（无法自定义 Authorization 头）
// 也能通过浏览器自动携带的 HttpOnly Cookie 完成实时通道鉴权。
//
// 与 middleware.SessionResolver 的 Cookie 兜底（middleware.SessionCookie）配对：
// Bearer 优先、Cookie 兜底，二者承载同一 v1.<tenant>.<session>.<version> 令牌。
func (h *Handler) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     middleware.SessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.deps.SecureCookies,
	})
}

// clearSessionCookie 让会话 Cookie 立即失效（登出时与撤销会话一并执行）。
func (h *Handler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     middleware.SessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.deps.SecureCookies,
	})
}

// Register 新用户注册（基于组织 org_code）。
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	var req RegisterRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, errors.E(errors.CodeUnavailable, "数据库连接池未就绪"))
		return
	}
	tid, err := h.deps.Store.TenantIDFromOrgCode(r.Context(), req.OrgCode)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	_, uid, _, err := h.deps.Store.RegisterUser(r.Context(), req.OrgCode, req.LoginName, req.DisplayName, req.Email, req.Password)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	resp, err := h.issueSession(r, tid, uid)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.setSessionCookie(w, resp.SessionToken)
	writeJSON(w, http.StatusCreated, resp)
}

// Login 密码登录。
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, errors.E(errors.CodeUnavailable, "数据库连接池未就绪"))
		return
	}
	tenantID, userID, _, err := h.deps.Store.VerifyPassword(r.Context(), req.LoginName, req.Password)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	resp, err := h.issueSession(r, tenantID, userID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.setSessionCookie(w, resp.SessionToken)
	writeJSON(w, http.StatusOK, resp)
}

// Logout 注销：撤销当前会话。需要已认证（走 Auth 链）。
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// 撤销目标必须是**会话标识**（iam.sessions.id），而不是请求关联 ID。
	// 开发态直连（无会话头）时 SessionID 为零值，此处诚实拒绝而不是伪造成功。
	if id.SessionID.IsZero() {
		h.fail(w, r, errors.E(errors.CodeUnauthenticated, "缺少会话标识"))
		return
	}
	sid := id.SessionID
	if err := h.deps.Store.RevokeSessionByID(r.Context(), id.TenantID, sid); err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "UserLogout", "iam.user.logout", "SUCCESS", "session", sid.String(), ids.ID{}, map[string]any{})
	h.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// issueSession 签发会话并组装 token 响应。
func (h *Handler) issueSession(r *http.Request, tenantID, userID ids.ID) (*SessionResponse, error) {
	if h.deps.SessionService == nil {
		return nil, errors.E(errors.CodeUnavailable, "会话签发服务未就绪")
	}
	sess, err := h.deps.SessionService.Issue(r.Context(), tenantID, userID, "HUMAN", "PASSWORD", sessionTTL, sessionRefreshTTL)
	if err != nil {
		return nil, err
	}
	token := fmt.Sprintf("v1.%s.%s.%d", sess.TenantID, sess.SessionID, sess.SubjectVersion)
	return &SessionResponse{
		SessionToken:     token,
		RefreshToken:     sess.RefreshPlain,
		ExpiresAt:        sess.ExpiresAt,
		RefreshExpiresAt: sess.RefreshExpiresAt,
		SessionID:        sess.SessionID,
		TenantID:         sess.TenantID,
		SubjectID:        sess.SubjectID,
	}, nil
}

// --- Admin Users CRUD（走 Auth 链，管理员操作）---

type AdminCreateUserRequest struct {
	LoginName      string `json:"login_name"`
	DisplayName    string `json:"display_name"`
	Email          string `json:"email"`
	Password       string `json:"password"`
	OrgCode        string `json:"org_code"`
	MembershipType string `json:"membership_type"`
}

// AdminListUsers GET /api/v1/admin/users
func (h *Handler) AdminListUsers(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	limit, _ := limitParam(r, 10)
	cursor := r.URL.Query().Get("cursor")
	items, next, err := h.deps.Store.ListUsersCursor(r.Context(), id.TenantID, cursor, limit, r.URL.Query().Get("status"), r.URL.Query().Get("q"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

// AdminCreateUser POST /api/v1/admin/users
func (h *Handler) AdminCreateUser(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var req AdminCreateUserRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	if req.MembershipType == "" {
		req.MembershipType = "EMPLOYEE"
	}
	userID, membershipID, err := h.deps.Store.AdminCreateUser(r.Context(), id.TenantID, req.LoginName, req.DisplayName, req.Email, req.Password, req.OrgCode, req.MembershipType)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "AdminUserCreated", "iam.admin.user.create", "SUCCESS",
		"user", userID.String(), ids.ID{}, map[string]any{"membership_id": membershipID.String()})
	writeJSON(w, http.StatusCreated, map[string]any{"user_id": userID.String(), "membership_id": membershipID.String()})
}

// AdminGetUser GET /api/v1/admin/users/{id}
func (h *Handler) AdminGetUser(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	uid, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	item, err := h.deps.Store.GetUserWithMemberships(r.Context(), id.TenantID, uid)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// AdminUpdateUser PATCH /api/v1/admin/users/{id}
func (h *Handler) AdminUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	uid, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var body map[string]any
	if err := decodeOptionalJSON(r, &body); err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.deps.Store.UpdateUser(r.Context(), id.TenantID, uid, body); err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "AdminUserUpdated", "iam.admin.user.update", "SUCCESS", "user", uid.String(), ids.ID{}, body)
	w.WriteHeader(http.StatusNoContent)
}

// AdminDeleteUser DELETE /api/v1/admin/users/{id}
func (h *Handler) AdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	uid, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.deps.Store.DisableUser(r.Context(), id.TenantID, uid); err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "AdminUserDisabled", "iam.admin.user.delete", "SUCCESS", "user", uid.String(), ids.ID{}, map[string]any{})
	w.WriteHeader(http.StatusNoContent)
}

// --- Admin Organizations CRUD ---

// AdminListOrgs GET /api/v1/admin/organizations（cursor 分页，默认 10 行 + ?q=）
func (h *Handler) AdminListOrgs(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	limit, err := limitParam(r, 10)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	res, err := h.deps.Store.ListOrganizations(r.Context(), id.TenantID, limit, r.URL.Query().Get("cursor"), r.URL.Query().Get("q"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": res.Items, "next_cursor": res.NextCursor})
}

type AdminCreateOrgRequest struct {
	OrgCode  string `json:"org_code"`
	Name     string `json:"name"`
	OrgType  string `json:"org_type"`
	ParentID string `json:"parent_id"`
}

// AdminCreateOrg POST /api/v1/admin/organizations
func (h *Handler) AdminCreateOrg(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var req AdminCreateOrgRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	var parentID ids.ID
	if req.ParentID != "" {
		if parentID, err = ids.Parse(req.ParentID); err != nil {
			h.fail(w, r, errors.E(errors.CodeInvalidArgument, "parent_id 非法"))
			return
		}
	}
	oid, err := h.deps.Store.CreateOrganization(r.Context(), id.TenantID, parentID, req.OrgCode, req.Name, req.OrgType)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "AdminOrgCreated", "iam.admin.org.create", "SUCCESS", "org", oid.String(), ids.ID{}, map[string]any{"org_code": req.OrgCode})
	writeJSON(w, http.StatusCreated, map[string]any{"org_id": oid.String()})
}

// AdminUpdateOrg PATCH /api/v1/admin/organizations/{id}
func (h *Handler) AdminUpdateOrg(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	oid, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var body map[string]any
	if err := decodeOptionalJSON(r, &body); err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.deps.Store.UpdateOrganization(r.Context(), id.TenantID, oid, body); err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "AdminOrgUpdated", "iam.admin.org.update", "SUCCESS", "org", oid.String(), ids.ID{}, body)
	w.WriteHeader(http.StatusNoContent)
}

// AdminDeleteOrg DELETE /api/v1/admin/organizations/{id} —— 软删除（INACTIVE）
func (h *Handler) AdminDeleteOrg(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	oid, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.deps.Store.DisableOrganization(r.Context(), id.TenantID, oid); err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "AdminOrgDisabled", "iam.admin.org.delete", "SUCCESS", "org", oid.String(), ids.ID{}, map[string]any{})
	w.WriteHeader(http.StatusNoContent)
}

// --- Admin Role Bindings ---

type AdminBindRoleRequest struct {
	MembershipID string  `json:"membership_id"`
	RoleID       string  `json:"role_id"`
	ScopeType    string  `json:"scope_type"`
	ScopeOrgID   *string `json:"scope_org_id"`
}

// AdminBindRole POST /api/v1/admin/role-bindings
func (h *Handler) AdminBindRole(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var req AdminBindRoleRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	mshipID, err := ids.Parse(req.MembershipID)
	if err != nil {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "membership_id 非法"))
		return
	}
	roleID, err := ids.Parse(req.RoleID)
	if err != nil {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "role_id 非法"))
		return
	}
	var scopeOrg *ids.ID
	if req.ScopeOrgID != nil && *req.ScopeOrgID != "" {
		v, perr := ids.Parse(*req.ScopeOrgID)
		if perr != nil {
			h.fail(w, r, errors.E(errors.CodeInvalidArgument, "scope_org_id 非法"))
			return
		}
		scopeOrg = &v
	}
	// granted_by 是 NOT NULL 外键（REFERENCES iam.users(id)），必须传**调用者主体**
	// 而非租户 ID，否则会同时触发"绑定参数个数不符"与"外键违例"两类错误。
	bid, err := h.deps.Store.BindRole(r.Context(), id.TenantID, mshipID, roleID, req.ScopeType, scopeOrg, id.SubjectID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "AdminRoleBound", "iam.admin.role.bind", "SUCCESS",
		"role_binding", bid.String(), ids.ID{}, map[string]any{"membership_id": mshipID.String()})
	writeJSON(w, http.StatusCreated, map[string]any{"binding_id": bid.String()})
}

// AdminUnbindRole DELETE /api/v1/admin/role-bindings/{id}
func (h *Handler) AdminUnbindRole(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	bid, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.deps.Store.UnbindRole(r.Context(), id.TenantID, bid); err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "AdminRoleUnbound", "iam.admin.role.unbind", "SUCCESS", "role_binding", bid.String(), ids.ID{}, map[string]any{})
	w.WriteHeader(http.StatusNoContent)
}

// GetCurrentSession 返回当前 Bearer token 对应的用户投影 + 权限数组。
// 用途：前端启动时调用一次（或 401 后重登录），把返回的投影写入 SessionContext，
//
//	不再依赖任何 DEMO_USER / fixtures / 硬编码权限。
func (h *Handler) GetCurrentSession(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	proj, err := h.deps.Store.GetSessionProjection(r.Context(), id.TenantID, id.SubjectID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// 会话引导（Cookie 镜像）：前端启动时必调本端点一次。把 Bearer 令牌镜像为
	// HttpOnly 会话 Cookie，使**既有登录态**（token 在 localStorage、尚未走过本次
	// 登录）也能让 EventSource 凭 Cookie 完成实时通道鉴权，无需用户重新登录。
	if tok := sessionTokenFromRequest(r); tok != "" {
		h.setSessionCookie(w, tok)
	}
	h.writeJSON(w, http.StatusOK, proj)
}

// sessionTokenFromRequest 提取请求中的会话令牌：优先 Bearer，其次既有会话 Cookie。
func sessionTokenFromRequest(r *http.Request) string {
	if h := r.Header.Get("Authorization"); len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	if c, err := r.Cookie(middleware.SessionCookie); err == nil {
		return c.Value
	}
	return ""
}

// --- JSON response helper ---

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

var _ = tenantctx.Identity{}
