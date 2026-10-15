package middleware

import (
	"context"
	"net/http"
	"strings"

	"odap/internal/api/httperr"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/tenantctx"
)

// SessionCookie 是平台会话 Cookie 名。
const SessionCookie = "odap_session"

// IdentityResolver 从请求解析可信身份。
type IdentityResolver interface {
	Resolve(ctx context.Context, r *http.Request) (tenantctx.Identity, error)
}

// SessionValidator 是 identity.SessionService 的最小依赖面（避免 middleware
// 直接耦合 identity 包的具体实现）。
type SessionValidator interface {
	Validate(ctx context.Context, sessionID ids.ID, subjectVersion int64) (tenantctx.Identity, error)
}

// SessionResolver 把 Bearer/Cookie 中的会话引用解析为身份。
//
// 令牌格式（契约登记于 contracts/openapi.yaml securitySchemes）：
//
//	v1.<tenant_uuid>.<session_uuid>.<subject_version>
//
// 首选 Authorization: Bearer，其次平台会话 Cookie。
type SessionResolver struct {
	Validator SessionValidator
}

// Resolve 实现 IdentityResolver。
func (r SessionResolver) Resolve(ctx context.Context, req *http.Request) (tenantctx.Identity, error) {
	if r.Validator == nil {
		return tenantctx.Identity{}, errors.E(errors.CodeInternal, "auth: session validator not configured")
	}
	token := bearerToken(req)
	if token == "" {
		if c, err := req.Cookie(SessionCookie); err == nil {
			token = c.Value
		}
	}
	if token == "" {
		return tenantctx.Identity{}, errors.E(errors.CodeUnauthenticated, "缺少身份凭据")
	}
	tenantID, sessionID, subjectVersion, err := parseSessionToken(token)
	if err != nil {
		return tenantctx.Identity{}, err
	}
	// Validate 内部依赖租户上下文（RLS 会话变量），先注入可信租户。
	ctx = tenantctx.WithIdentity(ctx, tenantctx.Identity{TenantID: tenantID})
	id, err := r.Validator.Validate(ctx, sessionID, subjectVersion)
	if err != nil {
		return tenantctx.Identity{}, err
	}
	// RequestID 只承载 HTTP 请求关联 ID；会话标识由 Validate 通过 SessionID 回传，
	// 这里**不得**再把它覆盖掉（旧实现正是用请求 ID 覆盖了会话 ID，使 logout 失效）。
	id.RequestID = RequestIDFromContext(ctx)
	if id.SessionID.IsZero() {
		// 纵深防御：校验器若未回传会话标识，用令牌中已解析出的会话 ID 补齐，
		// 保证 logout 一定拿得到"撤销哪一个会话"。
		id.SessionID = sessionID
	}
	return id, nil
}

// bearerToken 提取 Bearer 令牌。
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	const prefix = "bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

// parseSessionToken 解析 v1.<tenant>.<session>.<version>。
func parseSessionToken(token string) (ids.ID, ids.ID, int64, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != "v1" {
		return ids.ID{}, ids.ID{}, 0, errors.E(errors.CodeUnauthenticated, "身份凭据格式不合法")
	}
	tenantID, err := ids.Parse(parts[1])
	if err != nil {
		return ids.ID{}, ids.ID{}, 0, errors.E(errors.CodeUnauthenticated, "身份凭据租户标识不合法")
	}
	sessionID, err := ids.Parse(parts[2])
	if err != nil {
		return ids.ID{}, ids.ID{}, 0, errors.E(errors.CodeUnauthenticated, "身份凭据会话标识不合法")
	}
	var version int64
	for _, c := range parts[3] {
		if c < '0' || c > '9' {
			return ids.ID{}, ids.ID{}, 0, errors.E(errors.CodeUnauthenticated, "身份凭据版本号不合法")
		}
		version = version*10 + int64(c-'0')
	}
	if version < 1 {
		return ids.ID{}, ids.ID{}, 0, errors.E(errors.CodeUnauthenticated, "身份凭据版本号不合法")
	}
	return tenantID, sessionID, version, nil
}

// DevResolver 是开发环境身份解析器：从调试头读取租户/主体/范围。
// 仅在 APP_ENV=development 时由 cmd/api 装配；生产环境禁止使用。
type DevResolver struct{}

// 开发调试头（仅开发环境生效）。
const (
	HeaderDevTenant  = "X-Odap-Dev-Tenant"
	HeaderDevSubject = "X-Odap-Dev-Subject"
	HeaderDevScopes  = "X-Odap-Dev-Scopes"
	// HeaderDevSession 可选：开发态若要演练注销（DELETE 会话），
	// 用该头显式指定 iam.sessions.id。缺省表示"无会话上下文的直连身份"，
	// 此时 /api/v1/auth/logout 会诚实返回 401（缺少会话标识）而非伪造成功。
	HeaderDevSession = "X-Odap-Dev-Session"
)

// Resolve 实现 IdentityResolver。
func (DevResolver) Resolve(ctx context.Context, req *http.Request) (tenantctx.Identity, error) {
	tenantID, err := ids.Parse(req.Header.Get(HeaderDevTenant))
	if err != nil {
		return tenantctx.Identity{}, errors.E(errors.CodeUnauthenticated, "开发模式缺少合法租户标识")
	}
	subjectID, err := ids.Parse(req.Header.Get(HeaderDevSubject))
	if err != nil {
		return tenantctx.Identity{}, errors.E(errors.CodeUnauthenticated, "开发模式缺少合法主体标识")
	}
	var scopes []string
	if raw := req.Header.Get(HeaderDevScopes); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				scopes = append(scopes, s)
			}
		}
	}
	var sessionID ids.ID
	if raw := strings.TrimSpace(req.Header.Get(HeaderDevSession)); raw != "" {
		parsed, perr := ids.Parse(raw)
		if perr != nil {
			return tenantctx.Identity{}, errors.E(errors.CodeUnauthenticated, "开发模式会话标识不合法")
		}
		sessionID = parsed
	}
	return tenantctx.Identity{
		TenantID:  tenantID,
		SubjectID: subjectID,
		SessionID: sessionID,
		RequestID: RequestIDFromContext(ctx),
		Scopes:    scopes,
	}, nil
}

// Auth 解析身份并注入 context；失败返回 401 UNAUTHENTICATED。
func Auth(resolver IdentityResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rid := RequestIDFromContext(r.Context())
			if resolver == nil {
				httperr.WriteCode(w, rid, errors.CodeUnavailable, "认证组件未配置")
				return
			}
			id, err := resolver.Resolve(r.Context(), r)
			if err != nil {
				httperr.Write(w, rid, asUnauthenticated(err))
				return
			}
			ctx := tenantctx.WithIdentity(r.Context(), id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// asUnauthenticated 把认证失败统一投影为 UNAUTHENTICATED，避免泄漏
// 底层存储错误（例如数据库不可用）到客户端。
func asUnauthenticated(err error) error {
	if ae := errors.As(err); ae != nil {
		switch ae.Code {
		case errors.CodeUnauthenticated, errors.CodeIAMSessionRevoked,
			errors.CodeIAMTenantInactive, errors.CodePermissionDenied:
			return ae
		}
	}
	return errors.E(errors.CodeUnauthenticated, "身份校验失败")
}
