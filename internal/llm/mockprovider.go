package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"

	"odap/internal/platform/errors"
)

// MockResponse 是一次可编程响应：Completion 与 Err 二选一。
type MockResponse struct {
	Completion Completion
	Err        error
}

// MockProvider 是确定性、可编程的 Provider，供测试与离线评估使用：
//
//   - 确定性：未配置响应队列时，相同请求 → 相同响应（由规范化请求摘要派生，
//     第九篇 §12.2"非确定性模型推理不应假定相同输入必得相同结果"，此处的
//     确定性仅用于测试可复现）；
//   - 可编程响应队列：按调用次序逐条出队，用于注入特定输出；
//   - 可注入超时 / 输出非法 / 限流等错误（配合错误码对齐 contracts/02）。
type MockProvider struct {
	ProviderName string
	Caps         ProviderCapabilities

	mu    sync.Mutex
	queue []MockResponse
	idx   int
	calls []CompletionRequest
}

// NewMockProvider 构造 Mock Provider。
func NewMockProvider(name string, caps ProviderCapabilities) *MockProvider {
	return &MockProvider{ProviderName: name, Caps: caps}
}

// Name 实现 Provider。
func (m *MockProvider) Name() string { return m.ProviderName }

// Capabilities 实现 Provider。
func (m *MockProvider) Capabilities() ProviderCapabilities { return m.Caps }

// Push 追加一条可编程响应，返回自身以支持链式调用。
func (m *MockProvider) Push(r MockResponse) *MockProvider {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queue = append(m.queue, r)
	return m
}

// PushContent 追加一条成功响应（内容为 text）。
func (m *MockProvider) PushContent(text string) *MockProvider {
	return m.Push(MockResponse{Completion: Completion{Content: text, FinishReason: "stop"}})
}

// PushError 追加一条错误响应。
func (m *MockProvider) PushError(err error) *MockProvider {
	return m.Push(MockResponse{Err: err})
}

// Calls 返回收到过的全部请求快照（按调用次序）。
func (m *MockProvider) Calls() []CompletionRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]CompletionRequest, len(m.calls))
	copy(out, m.calls)
	return out
}

// CallCount 返回调用次数。
func (m *MockProvider) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// Complete 实现 Provider。
func (m *MockProvider) Complete(ctx context.Context, req CompletionRequest) (Completion, error) {
	m.mu.Lock()
	m.calls = append(m.calls, req)
	var resp MockResponse
	hasResp := m.idx < len(m.queue)
	if hasResp {
		resp = m.queue[m.idx]
		m.idx++
	}
	m.mu.Unlock()

	if hasResp {
		return resp.Completion, resp.Err
	}
	return deterministicCompletion(req, m.Caps), nil
}

// deterministicCompletion 由规范化请求摘要派生出稳定响应。
//
// 当端点声明 NativeSchema 且请求要求结构化输出时，进一步合成一个满足
// ResponseSchema 的最小实例。只有当端点声明原生 Schema 能力时才这样做：
// 声明为 generate-then-validate 的端点必须"自由生成"，其输出的不合法
// 正是驱动 §9.1 受限重生成回路的输入 —— 若这里也直接合成合法输出，
// 两种能力声明的区分就形同虚设，修复回路也永远不会被触发。
func deterministicCompletion(req CompletionRequest, caps ProviderCapabilities) Completion {
	h := sha256.New()
	h.Write([]byte("model=" + req.ModelID + "\x00"))
	h.Write([]byte("class=" + req.DataClassification + "\x00"))
	for _, msg := range req.Messages {
		h.Write([]byte(msg.Role))
		h.Write([]byte{0})
		h.Write([]byte(msg.Content))
		h.Write([]byte{0})
	}
	h.Write([]byte("schema="))
	h.Write(canonicalJSON(req.ResponseSchema))
	sum := hex.EncodeToString(h.Sum(nil))

	content := "mock:" + sum[:16]
	if caps.NativeSchema && len(req.ResponseSchema) > 0 {
		if v, ok := schemaSample(req.ResponseSchema); ok {
			if b, err := json.Marshal(v); err == nil {
				content = string(b)
			}
		}
	}

	in := 0
	for _, msg := range req.Messages {
		in += len(msg.Content)/4 + 1
	}
	out := req.MaxOutputTokens
	if out <= 0 {
		out = len(content) / 4
	}
	return Completion{
		Content:       content,
		InputTokens:   in,
		OutputTokens:  out,
		ModelRevision: "mock-rev-" + sum[:8],
		FinishReason:  "stop",
	}
}

// canonicalJSON 对 JSON 做键序规范化；非法 JSON 原样返回。
func canonicalJSON(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	b, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return b
}

// MockTimeoutError 构造一个模型超时错误（CodeModelTimeout）。
func MockTimeoutError() error {
	return errors.E(errors.CodeModelTimeout, "mock: 模拟模型调用超时")
}

// MockRateLimitedError 构造一个限流错误（CodeRateLimited）。
func MockRateLimitedError() error {
	return errors.E(errors.CodeRateLimited, "mock: 模拟供应商限流")
}

// MockProviderError 构造一个供应商错误（CodeModelProviderError）。
func MockProviderError() error {
	return errors.E(errors.CodeModelProviderError, "mock: 模拟供应商错误")
}

// MockInvalidOutputError 构造一个输出非法错误（CodeModelOutputInvalid）。
func MockInvalidOutputError(msg string) error {
	return errors.Ef(errors.CodeModelOutputInvalid, "mock: %s", msg)
}
