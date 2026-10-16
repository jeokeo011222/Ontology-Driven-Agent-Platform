package handlers

import (
	"net/http"

	"odap/internal/mcp"
)

// ---------------------------------------------------------------------------
// MCP（Model Context Protocol）端点
// ---------------------------------------------------------------------------
//
// StreamableHTTPServer 同时处理：
//   - GET  /api/v1/mcp：客户端建立 SSE 会话并接收 server-initiated notifications
//   - POST /api/v1/mcp：客户端发送 JSON-RPC 请求（initialize / tools/list / tools/call …）
//
// 两个方法必须挂在**同一个** handler 上，由 StreamableHTTPServer 内部按请求方法
// 分流。认证链由 router.go 统一挂载（两条路由都 Public=false），MCP server
// handler 从 request.Context() 里读取 tenantctx.Identity 以做租户隔离。

// MCPHandler 是 MCP 端点的轻量包装。
type MCPHandler struct {
	server *mcp.MCPServer
}

// NewMCPHandler 构造 MCP 端点处理器。
func NewMCPHandler(server *mcp.MCPServer) *MCPHandler {
	return &MCPHandler{server: server}
}

// ServeMCP 同时响应 GET 和 POST /api/v1/mcp。
func (h *MCPHandler) ServeMCP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.server == nil {
		http.Error(w, `{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"MCP server 未装配"}}`,
			http.StatusServiceUnavailable)
		return
	}
	h.server.Handler().ServeHTTP(w, r)
}
