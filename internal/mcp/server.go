package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/mark3labs/mcp-go/server"
)

// MCPServer 是 ODAP 对 mcp-go 的领域封装。
type MCPServer struct {
	deps Deps
	log  *slog.Logger

	mcp  *server.MCPServer
	http *server.StreamableHTTPServer
}

// NewMCPServer 构造 MCP 服务端。
func NewMCPServer(d Deps, log *slog.Logger) *MCPServer {
	if log == nil {
		log = slog.Default()
	}
	if d.Version == "" {
		d.Version = "0.1.0"
	}

	mcpSrv := server.NewMCPServer(
		"odap",
		d.Version,
		server.WithToolCapabilities(true),
	)

	s := &MCPServer{deps: d, log: log, mcp: mcpSrv}
	s.registerTools()

	httpSrv := server.NewStreamableHTTPServer(mcpSrv,
		server.WithDisableLocalhostProtection(true),
	)
	s.http = httpSrv
	return s
}

// Handler 返回可直接挂载到 http.ServeMux 的处理器。
func (s *MCPServer) Handler() http.Handler {
	return s.http
}

// StartHTTP 在指定 addr 上启动 MCP HTTP 服务器（自管理 http.Server）。
func (s *MCPServer) StartHTTP(addr string) (*http.Server, error) {
	srv := &http.Server{
		Addr:    addr,
		Handler: s.http,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.log.Error("MCP HTTP server error", "addr", addr, "error", err)
		}
	}()
	return srv, nil
}

// Shutdown 优雅关闭。
func (s *MCPServer) Shutdown(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return nil
}

// ServerInfo 返回 MCP server 基本信息。
func (s *MCPServer) ServerInfo() string {
	return fmt.Sprintf("odap-mcp %s", s.deps.Version)
}
