package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"odap/internal/platform/errors"
)

// ProbeRequest 是"录入前连通性检查"的输入。
//
// 与 EndpointBinding 的关键差别：凭据以**明文**出现在请求里（APIKey 或
// 环境变量引用名解析出的值），因为调用方需要它去真实试调一次。这个明文
// 只在本次调用内存在，**绝不落库、绝不写账本、绝不进审计**。
type ProbeRequest struct {
	DeploymentType string
	BaseURL        string
	CredentialRef  string
	APIKey         string
	ModelID        string
}

// ProbeResult 是一次探测的结果。
//
// 失败不返回 Go error，而是以 OK=false + ErrorCode/Message 结构化表达：
// 探测的"失败"（密钥错、地址错、模型名错）是**预期内的诊断结果**，
// 不是需要 panic 或 500 的程序异常。
type ProbeResult struct {
	OK         bool   `json:"ok"`
	LatencyMS  int64  `json:"latency_ms"`
	StatusCode int    `json:"status_code"`
	ErrorCode  string `json:"error_code,omitempty"`
	Message    string `json:"message"`
	Snippet    string `json:"vendor_error_snippet,omitempty"`
	Preview    string `json:"preview,omitempty"`
}

// probeMaxSnippet 是供应商错误回显的最大长度（回显前会二次脱敏）。
const probeMaxSnippet = 200

// probeTimeout 是单次探测的上限：录入前检查不该让用户等太久。
const probeTimeout = 20 * time.Second

// secretTokenRe 匹配形如 sk-xxxx / api_key=xxxx 的令牌串，用于二次脱敏。
var secretTokenRe = regexp.MustCompile(`(?i)(bearer\s+|sk[-_a-z0-9]{0,3})[a-z0-9_\-\.]{8,}`)

// Probe 用**当前表单参数**真实试调一次模型端点，不产生任何持久化副作用。
//
// 为什么不经 Gateway：Gateway 会走限流/预算/缓存/账本，探测是"录入前的
// 连通性检查"而非业务推理；写账本会污染对账与预算，故这里直接调用适配器。
func Probe(ctx context.Context, req ProbeRequest, getenv func(string) string) (ProbeResult, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	dep := strings.ToUpper(strings.TrimSpace(req.DeploymentType))
	if dep == "" {
		dep = "HTTP"
	}
	switch dep {
	case "MOCK":
		// MOCK 部署形态已下线：不再探测进程内确定性桩（它只会"永远通过"，
		// 给出与真实连通性无关的假绿灯）。
		return ProbeResult{
			ErrorCode: errors.CodeInvalidArgument,
			Message:   "deployment_type=MOCK 已下线，不再支持探测；请改用真实 HTTP 端点",
		}, nil
	case "HTTP":
		return probeHTTP(ctx, req, getenv)
	default:
		return ProbeResult{
			ErrorCode: errors.CodeInvalidArgument,
			Message:   "deployment_type 仅支持 HTTP，无法探测：" + dep,
		}, nil
	}
}

// probeHTTP 对真实 HTTP 端点发一次最小请求。
//
// 直接构造请求（而非复用 HTTPProvider）是为了拿到**状态码与响应体**，
// 好给出"401 密钥被拒 / 404 地址错 / 超时"这类具体诊断。
func probeHTTP(ctx context.Context, req ProbeRequest, getenv func(string) string) (ProbeResult, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		return ProbeResult{
			ErrorCode: errors.CodeInvalidArgument,
			Message:   "base_url 必填且须以 http:// 或 https:// 开头",
		}, nil
	}
	secret := req.APIKey
	if secret == "" {
		ref := strings.TrimSpace(req.CredentialRef)
		if ref == "" {
			return ProbeResult{
				ErrorCode: errors.CodeInvalidArgument,
				Message:   "须提供 api_key 或 credential_ref 之一作为探测凭据",
			}, nil
		}
		secret = getenv(ref)
		if secret == "" {
			return ProbeResult{
				ErrorCode: errors.CodeInvalidArgument,
				Message:   "环境变量 " + ref + " 未配置或为空，无法探测",
			}, nil
		}
	}

	// 未指定模型时退化为「列模型」探测：GET {base_url}/models 足以验证
	// 地址可达性与密钥有效性，且不要求用户先知道一个具体模型名（录入窗口
	// 尚未登记模型版本，此时猜一个模型名会把 400 误报成"配置错误"）。
	if strings.TrimSpace(req.ModelID) == "" {
		return probeModels(ctx, baseURL, secret)
	}

	body := map[string]any{
		"model":      strings.TrimSpace(req.ModelID),
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens": 1,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return ProbeResult{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return ProbeResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+secret)

	start := time.Now()
	client := &http.Client{Timeout: probeTimeout}
	resp, err := client.Do(httpReq)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return ProbeResult{
			LatencyMS: latency,
			ErrorCode: errors.CodeUnavailable,
			Message:   "无法连接端点（网络不可达 / DNS 失败 / 超时）：" + redact(err.Error(), secret),
		}, nil
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))

	if resp.StatusCode == http.StatusOK {
		var parsed struct {
			Model   string `json:"model"`
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		preview := ""
		if json.Unmarshal(raw, &parsed) == nil && len(parsed.Choices) > 0 {
			preview = truncate(parsed.Choices[0].Message.Content, probeMaxSnippet)
		}
		return ProbeResult{
			OK:         true,
			LatencyMS:  latency,
			StatusCode: resp.StatusCode,
			Message:    "端点可用，鉴权与寻址均正常",
			Preview:    preview,
		}, nil
	}

	code, msg := probeStatusToError(resp.StatusCode)
	return ProbeResult{
		LatencyMS:  latency,
		StatusCode: resp.StatusCode,
		ErrorCode:  code,
		Message:    msg,
		Snippet:    truncate(redact(strings.TrimSpace(string(raw)), secret), probeMaxSnippet),
	}, nil
}

// probeModels 用 GET {base_url}/models 做一次最小鉴权/寻址探测。
//
// 这是 OpenAI 兼容接口的通用能力（DeepSeek、百炼、Ollama 等均支持），
// 不消耗推理配额，也不需要预先知道模型名。回显只取模型名清单的前若干个。
func probeModels(ctx context.Context, baseURL, secret string) (ProbeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return ProbeResult{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+secret)

	start := time.Now()
	client := &http.Client{Timeout: probeTimeout}
	resp, err := client.Do(httpReq)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return ProbeResult{
			LatencyMS: latency,
			ErrorCode: errors.CodeUnavailable,
			Message:   "无法连接端点（网络不可达 / DNS 失败 / 超时）：" + redact(err.Error(), secret),
		}, nil
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if resp.StatusCode != http.StatusOK {
		code, msg := probeStatusToError(resp.StatusCode)
		return ProbeResult{
			LatencyMS:  latency,
			StatusCode: resp.StatusCode,
			ErrorCode:  code,
			Message:    msg,
			Snippet:    truncate(redact(strings.TrimSpace(string(raw)), secret), probeMaxSnippet),
		}, nil
	}

	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	preview := ""
	if json.Unmarshal(raw, &parsed) == nil {
		ids := make([]string, 0, len(parsed.Data))
		for i, m := range parsed.Data {
			if i >= 5 {
				break
			}
			if m.ID != "" {
				ids = append(ids, m.ID)
			}
		}
		preview = strings.Join(ids, ", ")
	}
	return ProbeResult{
		OK:         true,
		LatencyMS:  latency,
		StatusCode: resp.StatusCode,
		Message:    "端点可用，鉴权与寻址均正常（已列取模型清单）",
		Preview:    truncate(preview, probeMaxSnippet),
	}, nil
}

// probeStatusToError 把供应商 HTTP 状态码映射为具体、可执行的诊断。
//
// 与 HTTPProvider.statusToError 语义一致，但面向"配置是否正确"给出更直接的指引，
// 因为探测的使用场景就是"用户刚填完参数，想知道哪里填错了"。
func probeStatusToError(status int) (code, message string) {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return errors.CodeInvalidArgument, "密钥被端点拒绝（HTTP " + itoa(status) + "）：api-key 无效或无该模型权限"
	case status == http.StatusNotFound:
		return errors.CodeInvalidArgument, "路径不存在（HTTP 404）：base_url 可能缺少 /v1 之类的版本段"
	case status == http.StatusBadRequest:
		return errors.CodeInvalidArgument, "请求被拒（HTTP 400）：model_id 或请求格式不被端点接受"
	case status == http.StatusTooManyRequests:
		return errors.CodeRateLimited, "端点限流（HTTP 429）：请稍后重试或检查配额"
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return errors.CodeModelTimeout, "端点超时（HTTP " + itoa(status) + "）"
	case status >= 500:
		return errors.CodeModelProviderError, "端点服务端错误（HTTP " + itoa(status) + "）"
	default:
		return errors.CodeModelProviderError, "端点调用失败（HTTP " + itoa(status) + "）"
	}
}

// redact 二次脱敏：先按已知密钥做精确替换，再用正则兜底其它令牌样字符串。
func redact(s, secret string) string {
	if secret != "" {
		s = strings.ReplaceAll(s, secret, "****")
	}
	return secretTokenRe.ReplaceAllString(s, "[REDACTED]")
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
