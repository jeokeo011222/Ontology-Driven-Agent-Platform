package middleware

import (
	"net/http"
	"strings"
)

// CORSConfig 描述跨域策略。开发环境允许 http://localhost:5173（Vite 前端）。
type CORSConfig struct {
	// AllowedOrigins 是精确匹配的来源白名单（不含通配符）。
	AllowedOrigins []string
	// AllowCredentials 是否允许携带 Cookie（会话认证路径需要）。
	AllowCredentials bool
}

// DefaultDevCORS 返回开发环境默认策略。
func DefaultDevCORS() CORSConfig {
	return CORSConfig{
		AllowedOrigins:   []string{"http://localhost:5173"},
		AllowCredentials: true,
	}
}

// CORS 处理跨域与预检请求。仅允许白名单来源，绝不回显任意 Origin。
func CORS(cfg CORSConfig) func(http.Handler) http.Handler {
	allow := make(map[string]bool, len(cfg.AllowedOrigins))
	for _, o := range cfg.AllowedOrigins {
		allow[strings.ToLower(strings.TrimRight(o, "/"))] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && allow[strings.ToLower(strings.TrimRight(origin, "/"))] {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Add("Vary", "Origin")
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, If-Match, X-Request-Id, Last-Event-ID")
				h.Set("Access-Control-Expose-Headers", "X-Request-Id, ETag, Retry-After")
				if cfg.AllowCredentials {
					h.Set("Access-Control-Allow-Credentials", "true")
				}
				h.Set("Access-Control-Max-Age", "600")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
