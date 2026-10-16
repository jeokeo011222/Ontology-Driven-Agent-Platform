// Package llm 实现模型供应商抽象层与 LLM Gateway：统一调用协议、能力声明、
// 结构化输出修复回路、三层语义缓存、令牌桶限流、用量账本与预算治理。
//
// 权威来源：
//   - docs/第九篇 §2/§3（统一协议与能力声明）、§6（模型选择与质量评分/影子路由）、
//     §9（结构化输出与校验失败修复回路）、§11（成本治理与用量账本）、
//     §12（语义缓存三层 + cache_hit 降权 + 高敏感禁用）、
//     §14（错误模型/限流/降级）、§16（usage_ledger DDL）
//   - docs/contracts/02（错误码唯一真源）
//
// 设计边界：本包不承载任何供应商 HTTP 协议细节；Provider 只暴露平台能力
// （第九篇 §3.3：统一协议，业务 Agent 不需要知道供应商字段名）。
package llm

import (
	"context"
	"encoding/json"

	"odap/internal/platform/ids"
)

// Message 是规范化的模型消息（第九篇 §3.1 messages）。
type Message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall 是模型提出的工具调用请求。
// 注意（第九篇 §9.4）：模型原生工具调用只是生成结构化请求，不构成执行权限。
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolSpec 是本次调用对模型可见的工具声明（第九篇 §3.1 tool_definitions）。
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// CompletionRequest 是统一的内部调用协议（第九篇 §3.1 的工程化子集）。
//
// TenantID/StepID 等身份字段必须来自可信的服务端运行环境，不能让用户或模型
// 通过请求体指定其他租户（第九篇 §3.1）。
type CompletionRequest struct {
	TenantID ids.ID
	RunID    ids.ID
	StepID   ids.ID

	ModelID  string
	Messages []Message
	Tools    []ToolSpec

	Temperature     *float64
	Seed            *int64
	MaxOutputTokens int

	// ResponseSchema 非空时要求结构化输出；空则表示普通文本输出。
	ResponseSchema json.RawMessage
	// DataClassification 是输入数据的敏感等级（第九篇 §8.1/§12.3）。
	DataClassification string
	// IdempotencyKey 用于账本去重（第九篇 §16.2：重试不重复结算）。
	IdempotencyKey string
}

// Completion 是统一的内部响应。
type Completion struct {
	Content       string
	ToolCalls     []ToolCall
	InputTokens   int
	OutputTokens  int
	ModelRevision string
	CacheHit      bool
	FinishReason  string
	Raw           json.RawMessage

	// CacheLayer 是命中层级（L1/L2/L3）。第九篇 §12.3 关键约束 2 要求命中响应
	// 必须回传命中层级，供证据链区分"复用结果"与"本次新证据"。
	CacheLayer string
	// OriginalRequestID 是命中条目的原始 request_id（第九篇 §12.3 关键约束 2）。
	OriginalRequestID string
}

// FinishLength 是 OpenAI 兼容端点表示"因达到输出上限而截断"的 finish_reason 取值。
//
// 单独定义它是因为这个取值会改变**控制流**：截断是确定性失败，按要求重生成一次只会
// 以同样方式再截断（修复回路还会把上一轮输出回灌，输入更长、更容易撞上限），必须
// 快速失败而不是白跑两次修复回路。
const FinishLength = "length"

// ProviderCapabilities 是端点能力声明（第九篇 §4.1/§9.1）。
//
// NativeSchema 区分两种结构化输出能力（第九篇 §9.1）：
//   - true：端点原生支持严格 Schema 约束生成（native schema）；
//   - false：只能自由生成后本地校验（generate-then-validate），必须走修复回路。
//
// Embeddings 声明端点支持向量嵌入（POST {base_url}/embeddings）：知识库摄取
// （kb.chunks.embedding）与相似检索都依赖它。该能力只经 Gateway.Embed 的
// 点名路由使用——向量维度与训练模型强绑定，**绝不允许**像补全那样按能力
// 择优回落到别的模型（维度不符会产生静默垃圾数据）。
type ProviderCapabilities struct {
	NativeSchema     bool
	SupportsSeed     bool
	SupportsTools    bool
	Embeddings       bool
	MaxContextTokens int
}

// EmbeddingRequest 是统一的向量嵌入调用请求。
//
// 与 CompletionRequest 同样受治理链约束：TenantID 必须来自可信服务端环境；
// ModelID 必须点名已登记且声明 Embeddings 能力的模型（不点名、或点名了
// 无该能力的模型，一律 UNAVAILABLE 失败，不做任何回落）。
type EmbeddingRequest struct {
	TenantID ids.ID
	ModelID  string
	// Texts 是待嵌入的文本批次（单批条数上限由调用方控制，网关不做分批）。
	Texts []string
	// IdempotencyKey 用于账本去重（第九篇 §16.2：重试不重复结算）。
	IdempotencyKey string
}

// EmbeddingResponse 是统一的向量嵌入响应。
type EmbeddingResponse struct {
	// Vectors 与请求 Texts 一一对应（顺序保持）。
	Vectors [][]float32
	// Dimensions 是本次响应的向量维度（所有向量必须一致；不一致视为供应商错误）。
	Dimensions int
	// InputTokens 来自供应商 usage（embeddings 只有输入侧计费）。
	InputTokens int
	// ModelRevision 是供应商回显的实际模型名。
	ModelRevision string
	Raw           json.RawMessage
}

// EmbeddingProvider 是支持向量嵌入的 Provider 可选扩展接口。
//
// 刻意不并入 Provider 主接口：补全与嵌入是两条独立能力线，把 Embed 塞进
// 主接口会强迫所有适配器（包括测试桩）实现无意义的方法。实现方按需声明，
// 网关在路由命中后做类型断言——未实现即 UNAVAILABLE 明确失败，绝不静默。
type EmbeddingProvider interface {
	Embed(ctx context.Context, req EmbeddingRequest) (EmbeddingResponse, error)
}

// Provider 是平台能力抽象，而非供应商协议细节（第九篇 §3.2/§4.1）。
// 接口中不应直接暴露供应商原生响应结构，否则统一协议会失去意义。
type Provider interface {
	Name() string
	Capabilities() ProviderCapabilities
	Complete(ctx context.Context, req CompletionRequest) (Completion, error)
}
