// Package mcp 暴露 ODAP 平台能力为 MCP（Model Context Protocol）服务端。
//
// 设计要点：
//   - 领域逻辑集中在本包；HTTP 端点在 internal/api/handlers/mcp.go；
//   - 依赖通过最小接口注入（本文件），避免反向导入 store/llm/graphstore 等
//     具体实现包，保持 internal/mcp 对领域包的**单向依赖**；
//   - 所有 tool 的 input schema 严格校验，fail-closed。
//
// MCP Transport：使用 mcp-go 的 StreamableHTTPServer（HTTP transport 是 MCP 1.0
// 标准传输，GET 返回 SSE、POST 接受 JSON-RPC），由 handlers/mcp.go 挂载到
// /api/v1/mcp 并受认证链保护。
package mcp

import (
	"context"

	"odap/internal/ontology/graphstore"
	"odap/internal/platform/ids"
)

// ---------------------------------------------------------------------------
// 依赖接口（从具体实现反向抽出，避免循环依赖）
// ---------------------------------------------------------------------------

// OntologyLister 是本体列表查询依赖面。
type OntologyLister interface {
	ListPublishedOntologies(ctx context.Context, tenantID ids.ID, limit int) ([]map[string]any, error)
}

// GraphReader 是语义图读取依赖面。
type GraphReader interface {
	Match(ctx context.Context, tenantID ids.ID, p graphstore.Pattern) ([]graphstore.Triple, error)
}

// KnowledgeSearcher 是知识库检索依赖面。
//
// SearchVector 接收自然语言 queryText（适配器内部完成 query → embedding → pgvector 余弦检索）；
// embeddingModel 为空时适配器应退回 Deps.DefaultKBEmbeddingModel。未配置嵌入模型时严格
// fail-closed（SearchVector 返回 UNAVAILABLE），不伪造假数据。
type KnowledgeSearcher interface {
	SearchVector(ctx context.Context, tenantID ids.ID, embeddingModel, queryText string, topK int) ([]VectorHit, error)
	SearchTerms(ctx context.Context, tenantID ids.ID, terms []string, topK int) ([]VectorHit, error)
}

// VectorHit 是知识检索单条命中。
type VectorHit struct {
	DocumentID string
	DocTitle   string
	ChunkIndex int
	Content    string
	Score      float64
}

// AgentRunner 是 Agent 运行依赖面。
type AgentRunner interface {
	InvokeAgent(ctx context.Context, tenantID ids.ID, agentKey string, input map[string]any) (runID string, err error)
}

// Deps 是 MCPServer 的全部依赖。所有字段都可以为 nil；nil 时对应 tool
// 会在调用时返回 UNAVAILABLE（fail-closed，绝不静默成功）。
type Deps struct {
	Version string

	Ontology OntologyLister
	Graph    GraphReader
	KB       KnowledgeSearcher
	Agent    AgentRunner

	// DefaultKBEmbeddingModel 是知识库向量检索默认模型。
	DefaultKBEmbeddingModel string
}
