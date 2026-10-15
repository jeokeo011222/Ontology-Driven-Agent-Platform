// Package logging 封装 slog，统一 JSON 结构化日志与请求关联 ID。
package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"odap/internal/platform/tenantctx"
)

// New 按级别创建 JSON Logger。
func New(level string) *slog.Logger {
	lv := slog.LevelInfo
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lv}))
}

// WithContext 把请求关联 ID 注入日志字段。
func WithContext(ctx context.Context, logger *slog.Logger) *slog.Logger {
	if id, ok := tenantctx.FromContext(ctx); ok && id.RequestID != "" {
		return logger.With(slog.String("request_id", id.RequestID))
	}
	return logger
}
