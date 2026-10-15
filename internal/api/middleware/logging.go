package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"odap/internal/platform/tenantctx"
)

// statusRecorder 记录响应状态码与写出字节数（不缓存响应体）。
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Flush 支持 SSE（http.Flusher 透传）。
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Logging 输出结构化访问日志。
//
// 脱敏约束（第十六篇 §6.2）：只记录方法、路径（不含查询串，避免令牌/敏感
// 过滤参数进入日志）、状态码、字节数、耗时、request_id 与 tenant_id；
// 绝不记录 Authorization / Cookie / 请求体。
func Logging(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if logger == nil {
				next.ServeHTTP(w, r)
				return
			}
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)

			attrs := []any{
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Int("bytes", rec.bytes),
				slog.Duration("duration", time.Since(start)),
				slog.String("request_id", RequestIDFromContext(r.Context())),
			}
			if id, ok := tenantctx.FromContext(r.Context()); ok && !id.TenantID.IsZero() {
				attrs = append(attrs, slog.String("tenant_id", id.TenantID.String()))
			}
			switch {
			case rec.status >= 500:
				logger.Error("http request", attrs...)
			case rec.status >= 400:
				logger.Warn("http request", attrs...)
			default:
				logger.Info("http request", attrs...)
			}
		})
	}
}
