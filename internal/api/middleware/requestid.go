// Package middleware 提供 HTTP 层横切关注点：请求关联 ID、panic 恢复、
// 身份解析、租户上下文注入、结构化日志、CORS 与限流。
//
// 安全约束（第十六篇 §5.4）：租户与主体身份只能来自经过验证的认证上下文，
// 禁止从请求体或查询参数读取；缺失身份一律 fail-closed。
package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

type ctxKey int

const keyRequestID ctxKey = iota

// HeaderRequestID 是透传/回写请求关联 ID 的约定头（小写用于读取）。
const HeaderRequestID = "X-Request-Id"

// RequestIDFromContext 读取请求关联 ID；缺失返回空串。
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(keyRequestID).(string); ok {
		return v
	}
	return ""
}

// WithRequestID 把请求关联 ID 注入 context。
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyRequestID, id)
}

// newRequestID 生成 req_<16hex> 形式的关联 ID。
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "req_unknown"
	}
	return "req_" + hex.EncodeToString(b[:])
}

// RequestID 生成或透传请求关联 ID：写入 context 并回写响应头。
// 透传外部 ID 时做长度与字符白名单校验，避免注入到日志/响应头。
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := sanitizeRequestID(r.Header.Get(HeaderRequestID))
		if rid == "" {
			rid = newRequestID()
		}
		w.Header().Set(HeaderRequestID, rid)
		ctx := WithRequestID(r.Context(), rid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// sanitizeRequestID 只接受 [A-Za-z0-9._-] 且长度 <= 64 的透传值。
func sanitizeRequestID(v string) string {
	if v == "" || len(v) > 64 {
		return ""
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '_', c == '-':
		default:
			return ""
		}
	}
	return v
}
