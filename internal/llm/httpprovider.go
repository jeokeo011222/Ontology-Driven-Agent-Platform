package llm

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"odap/internal/platform/errors"
)

// HTTPProvider 是"OpenAI 兼容 /chat/completions"协议的端点适配器。
//
// 定位（第九篇 §4.3 供应商接入策略）：这是**唯一的**协议细节落点。
// Provider 接口只暴露平台能力，业务 Agent 不接触供应商字段名；
// 换供应商只需要新增一个适配器，不需要改 Gateway、Router 或 Agent。
//
// 治理边界（第九篇 §2.1）：本适配器不做重试、不切换端点、不降级模型 ——
// 那些属于 Gateway 的治理决策。适配器只做两件事：把统一请求翻译为供应商请求，
// 把供应商响应/错误翻译回统一结构与已登记错误码。
type HTTPProvider struct {
	name    string
	baseURL string
	apiKey  string
	caps    ProviderCapabilities
	// modelID 是该端点登记的模型名（EndpointBinding.ModelID），作为出站 model
	// 字段的默认值。调用方未点名模型时（Router 按能力择优），必须用它填充，
	// 否则会向供应商发出 model=""，被以 400 拒绝。
	modelID string

	client *http.Client
}

// DefaultHTTPTimeout 是单次推理调用的默认上限（5 分钟）。
// 注意：真正的超时治理在 Gateway/调用链上游（按 Run 的 deadline 下发 ctx），
// 这里的值只是"适配器层不会无限悬挂"的兜底。结构化生成（如本体清单，8192
// tokens 输出）实测耗时可达数分钟，60s 的旧默认会在读取响应体阶段超时——
// 这正是"生成失败：llm: 读取供应商响应失败"的根因。
// 可用环境变量 ODAP_LLM_HTTP_TIMEOUT（Go Duration 格式）覆盖。
const DefaultHTTPTimeout = 300 * time.Second

// EnvHTTPTimeout 是适配器层兜底超时的环境变量名（Go Duration 格式，如 5m）。
const EnvHTTPTimeout = "ODAP_LLM_HTTP_TIMEOUT"

// resolveHTTPTimeout 从环境解析兜底超时；非法配置返回错误，绝不静默回落默认值。
func resolveHTTPTimeout(getenv func(string) string) (time.Duration, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	raw := strings.TrimSpace(getenv(EnvHTTPTimeout))
	if raw == "" {
		return DefaultHTTPTimeout, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, errors.Ef(errors.CodeInternal,
			"llm: 环境变量 %s=%q 不是合法的正时长（Go Duration 格式，如 5m）", EnvHTTPTimeout, raw)
	}
	return d, nil
}

// NewHTTPProvider 构造 HTTP 适配器。baseURL 形如 https://host/v1（不含 /chat/completions）。
//
// modelID 是该端点登记的模型名，作为出站 model 字段的默认值（调用方未点名时使用）。
// 兜底超时取 ODAP_LLM_HTTP_TIMEOUT（未设置时为 DefaultHTTPTimeout）；配置非法时
// 返回错误——装配期失败优于运行期静默降级。
func NewHTTPProvider(name, baseURL, apiKey, modelID string, caps ProviderCapabilities) (*HTTPProvider, error) {
	timeout, err := resolveHTTPTimeout(nil)
	if err != nil {
		return nil, err
	}
	return &HTTPProvider{
		name:    name,
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		caps:    caps,
		modelID: modelID,
		client:  &http.Client{Timeout: timeout},
	}, nil
}

// Name 实现 Provider。
func (p *HTTPProvider) Name() string { return p.name }

// Capabilities 实现 Provider。
func (p *HTTPProvider) Capabilities() ProviderCapabilities { return p.caps }

// chatRequest 是 OpenAI 兼容协议的请求体（只包含平台会下发的字段）。
type chatRequest struct {
	Model          string           `json:"model"`
	Messages       []chatMessage    `json:"messages"`
	Temperature    *float64         `json:"temperature,omitempty"`
	Seed           *int64           `json:"seed,omitempty"`
	MaxTokens      int              `json:"max_tokens,omitempty"`
	ResponseFormat map[string]any   `json:"response_format,omitempty"`
	Tools          []map[string]any `json:"tools,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Complete 实现 Provider。
func (p *HTTPProvider) Complete(ctx context.Context, req CompletionRequest) (Completion, error) {
	// 出站模型名：调用方点名则用点名的，否则用该端点登记的模型名。
	// Router 在调用方未点名时按能力择优，此时 req.ModelID 为空；若原样下发会导致
	// 供应商收到 model="" 而拒绝请求（表现为"网关已装配但调用失败"）。
	model := p.modelID
	if strings.TrimSpace(req.ModelID) != "" {
		model = req.ModelID
	}
	body := chatRequest{
		Model:       model,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxOutputTokens,
	}
	if p.caps.SupportsSeed {
		body.Seed = req.Seed
	}
	for _, m := range req.Messages {
		body.Messages = append(body.Messages, chatMessage{Role: m.Role, Content: m.Content})
	}
	// 只有声明了原生 Schema 能力才下发严格 Schema 约束；否则走平台侧
	// generate-then-validate + 受限重生成（第九篇 §9.1）。
	if p.caps.NativeSchema && len(req.ResponseSchema) > 0 {
		body.ResponseFormat = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "odap_output",
				"strict": true,
				"schema": json.RawMessage(req.ResponseSchema),
			},
		}
	}
	if len(req.Tools) > 0 {
		if !p.caps.SupportsTools {
			// 能力不匹配属于装配/路由缺陷：明确报错，绝不静默丢弃工具声明
			// （丢掉了模型就"不知道"有工具可用，行为会与调用方预期不同）。
			return Completion{}, errors.Ef(errors.CodeUnavailable,
				"llm: 端点 %s 未声明工具调用能力，拒绝丢弃工具声明", p.name)
		}
		for _, t := range req.Tools {
			fn := map[string]any{"name": t.Name}
			if t.Description != "" {
				fn["description"] = t.Description
			}
			if len(t.InputSchema) > 0 {
				fn["parameters"] = json.RawMessage(t.InputSchema)
			}
			body.Tools = append(body.Tools, map[string]any{"type": "function", "function": fn})
		}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return Completion{}, errors.Wrap(err, errors.CodeInternal, "llm: 编码供应商请求失败")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Completion{}, errors.Wrap(err, errors.CodeInternal, "llm: 构造供应商请求失败")
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		// 超时/取消交给 Gateway 的 classifyProviderError 归类为 MODEL_TIMEOUT。
		return Completion{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		if stderrors.Is(err, context.DeadlineExceeded) {
			return Completion{}, errors.Ef(errors.CodeModelTimeout,
				"llm: 读取供应商响应超时（生成大输出可能超出适配器兜底上限，可用 %s 调整）", EnvHTTPTimeout)
		}
		return Completion{}, errors.Wrap(err, errors.CodeModelProviderError, "llm: 读取供应商响应失败")
	}
	if resp.StatusCode != http.StatusOK {
		return Completion{}, statusToError(p.name, resp.StatusCode, raw)
	}

	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Completion{}, errors.Wrap(err, errors.CodeModelProviderError,
			"llm: 供应商响应不是合法 JSON")
	}
	if len(parsed.Choices) == 0 {
		return Completion{}, errors.Ef(errors.CodeModelProviderError,
			"llm: 供应商响应不含任何 choice（model=%s）", req.ModelID)
	}
	ch := parsed.Choices[0]
	comp := Completion{
		Content:       ch.Message.Content,
		InputTokens:   parsed.Usage.PromptTokens,
		OutputTokens:  parsed.Usage.CompletionTokens,
		ModelRevision: parsed.Model,
		FinishReason:  ch.FinishReason,
		Raw:           raw,
	}
	for _, tc := range ch.Message.ToolCalls {
		comp.ToolCalls = append(comp.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	return comp, nil
}

// statusToError 把 HTTP 状态码映射为已登记错误码（第九篇 §14.1 / contracts/02）。
//
// 映射必须显式：把 429 当成普通失败会让上游限流治理（重试/退避/切换端点）
// 失去依据；把 401 当成供应商错误则会把"配置错了"误报成"对方挂了"。
func statusToError(endpoint string, status int, body []byte) error {
	detail := strings.TrimSpace(string(body))
	if len(detail) > 512 {
		detail = detail[:512] + "…"
	}
	switch {
	case status == http.StatusTooManyRequests:
		return errors.Ef(errors.CodeRateLimited, "llm: 端点 %s 返回 429（%s）", endpoint, detail)
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		// 凭据问题不是"供应商故障"，而是装配缺陷：归为 UNAVAILABLE 而非 PROVIDER_ERROR，
		// 以免被重试策略当成瞬时故障反复重试。
		return errors.Ef(errors.CodeUnavailable, "llm: 端点 %s 凭据被拒（HTTP %d）", endpoint, status)
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return errors.Ef(errors.CodeModelTimeout, "llm: 端点 %s 超时（HTTP %d）", endpoint, status)
	case status >= 500:
		return errors.Ef(errors.CodeModelProviderError, "llm: 端点 %s 服务端错误（HTTP %d：%s）", endpoint, status, detail)
	default:
		return errors.Ef(errors.CodeModelProviderError, "llm: 端点 %s 调用失败（HTTP %d：%s）", endpoint, status, detail)
	}
}

// embeddingRequest 是 OpenAI 兼容 /embeddings 协议的请求体。
type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingResponse struct {
	Model string `json:"model"`
	Data  []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
	} `json:"usage"`
}

// Embed 实现 EmbeddingProvider（OpenAI 兼容 POST {base_url}/embeddings）。
//
// 与 Complete 同样的治理边界：不重试、不换端点、不降级模型。维度校验在
// Gateway.Embed 统一做（对照登记维度 fail-closed），这里只保证响应形状：
// 供应商返回 data 为空、索引错乱或维度不一致，一律显式失败。
func (p *HTTPProvider) Embed(ctx context.Context, req EmbeddingRequest) (EmbeddingResponse, error) {
	model := p.modelID
	if strings.TrimSpace(req.ModelID) != "" {
		model = req.ModelID
	}
	if len(req.Texts) == 0 {
		return EmbeddingResponse{}, errors.E(errors.CodeInvalidArgument, "llm: 嵌入请求不含任何文本")
	}
	payload, err := json.Marshal(embeddingRequest{Model: model, Input: req.Texts})
	if err != nil {
		return EmbeddingResponse{}, errors.Wrap(err, errors.CodeInternal, "llm: 编码嵌入请求失败")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/embeddings", bytes.NewReader(payload))
	if err != nil {
		return EmbeddingResponse{}, errors.Wrap(err, errors.CodeInternal, "llm: 构造嵌入请求失败")
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return EmbeddingResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		if stderrors.Is(err, context.DeadlineExceeded) {
			return EmbeddingResponse{}, errors.Ef(errors.CodeModelTimeout,
				"llm: 读取嵌入响应超时（可用 %s 调整）", EnvHTTPTimeout)
		}
		return EmbeddingResponse{}, errors.Wrap(err, errors.CodeModelProviderError, "llm: 读取嵌入响应失败")
	}
	if resp.StatusCode != http.StatusOK {
		return EmbeddingResponse{}, statusToError(p.name, resp.StatusCode, raw)
	}
	var parsed embeddingResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return EmbeddingResponse{}, errors.Wrap(err, errors.CodeModelProviderError,
			"llm: 嵌入响应不是合法 JSON")
	}
	if len(parsed.Data) != len(req.Texts) {
		return EmbeddingResponse{}, errors.Ef(errors.CodeModelProviderError,
			"llm: 嵌入响应条数不匹配（请求 %d 条，供应商返回 %d 条）", len(req.Texts), len(parsed.Data))
	}
	out := EmbeddingResponse{
		Vectors:       make([][]float32, len(req.Texts)),
		InputTokens:   parsed.Usage.PromptTokens,
		ModelRevision: parsed.Model,
		Raw:           raw,
	}
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(out.Vectors) {
			return EmbeddingResponse{}, errors.Ef(errors.CodeModelProviderError,
				"llm: 嵌入响应索引越界（index=%d）", d.Index)
		}
		if len(d.Embedding) == 0 {
			return EmbeddingResponse{}, errors.Ef(errors.CodeModelProviderError,
				"llm: 嵌入响应第 %d 条为空向量", d.Index)
		}
		if out.Dimensions == 0 {
			out.Dimensions = len(d.Embedding)
		}
		if len(d.Embedding) != out.Dimensions {
			return EmbeddingResponse{}, errors.Ef(errors.CodeModelProviderError,
				"llm: 嵌入响应维度不一致（第 %d 条 %d 维，此前 %d 维）", d.Index, len(d.Embedding), out.Dimensions)
		}
		out.Vectors[d.Index] = d.Embedding
	}
	return out, nil
}

// 编译期断言：HTTPProvider 满足 Provider 接口。
var _ Provider = (*HTTPProvider)(nil)
