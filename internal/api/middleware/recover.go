package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"odap/internal/api/httperr"
	"odap/internal/platform/errors"
)

// Recover 捕获下游 panic，记录完整堆栈到服务端日志，但**只**向客户端返回
// 脱敏的 500 INTERNAL（绝不回显堆栈或内部错误文本）。
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					rid := RequestIDFromContext(r.Context())
					if logger != nil {
						logger.Error("panic recovered",
							slog.String("request_id", rid),
							slog.String("method", r.Method),
							slog.String("path", r.URL.Path),
							slog.Any("panic", rec),
							slog.String("stack", string(debug.Stack())),
						)
					}
					httperr.Write(w, rid, errors.E(errors.CodeInternal, "内部错误"))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
