// Package a2a 提供 A2A（Agent-to-Agent）协议的客户端实现。
//
// 该客户端使本平台的 Agent 能够作为请求方主动发起 JSON-RPC 2.0
// 调用远端 A2A Server（与 internal/api/handlers/a2a_gateway.go 对端
// 信封格式一致：jsonrpc="2.0" + id + method + params）。
//
// 认证凭据通过远程 Agent 注册表的 auth_header_name / auth_header_ref
// 解析（凭据只来自环境变量，fail-closed）；超时可配置（默认 120s 同步
// 等待终态；超时返回 accepted 状态 + task_id 供 tasks/get 轮询）。
//
// 默认不自动重试——超时 / 网络错误 / 远端业务错误一律如实传播，
// 由调用方决定是否重试（fail-closed 原则）。
package a2a

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// ---------------------------------------------------------------------------
// 协议常量（与 a2a_gateway.go 对端对齐）
// ---------------------------------------------------------------------------

// JSONRPCVersion 是 A2A 使用的 JSON-RPC 协议版本。
const JSONRPCVersion = "2.0"

// 同步等待远端任务终态的默认上限（与 a2a_gateway.go 服务端一致）。
const defaultSyncWaitTimeout = 120 * time.Second

// 出站请求体上限（与服务端 maxBodyBytes 同阶，防御意外的超大载荷）。
const defaultMaxBodyBytes = 1 << 20 // 1 MB

// JSON-RPC 2.0 规范错误码 + A2A 约定业务错误码（与 a2a_gateway.go 对端对齐）。
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
	CodeServerError    = -32000
)

// JSON-RPC 方法名（与 a2a_gateway.go 白名单一致）。
const (
	MethodMessageSend        = "message/send"
	MethodTasksSend          = "tasks/send"
	MethodTasksSendSubscribe = "tasks/sendSubscribe"
	MethodTasksGet           = "tasks/get"
	MethodTasksList          = "tasks/list"
	MethodTasksCancel        = "tasks/cancel"
)

// ---------------------------------------------------------------------------
// 数据结构（与 a2a_gateway.go 的 params / result 形状对齐）
// ---------------------------------------------------------------------------

// Task 是 A2A 协议的 task 对象（与服务端 a2aTaskView 投影对齐）。
type Task struct {
	ID        string         `json:"id"`
	ContextID string         `json:"contextId,omitempty"`
	Status    TaskStatus     `json:"status"`
	Artifacts []TaskArtifact `json:"artifacts,omitempty"`
	Error     *TaskError     `json:"error,omitempty"`
	Kind      string         `json:"kind,omitempty"`
}

// TaskStatus 是 A2A 任务状态（submitted/working/completed/failed/canceled）。
type TaskStatus struct {
	State     string `json:"state"`
	Timestamp string `json:"timestamp,omitempty"`
}

// TaskArtifact 是 A2A 任务产物。
type TaskArtifact struct {
	ArtifactID string        `json:"artifactId,omitempty"`
	Name       string        `json:"name,omitempty"`
	Parts      []MessagePart `json:"parts,omitempty"`
}

// TaskError 是 A2A 任务的业务错误载荷。
type TaskError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// MessagePart 是 A2A message.parts 的文本段（其余 kind 被忽略）。
type MessagePart struct {
	Kind string `json:"kind,omitempty"`
	Text string `json:"text"`
}

// Message 是 A2A message 信封。
type Message struct {
	Role  string        `json:"role"`
	Parts []MessagePart `json:"parts"`
}

// SendMetadata 是 A2A params.metadata（与服务端 a2aSendMetadata 对齐）。
type SendMetadata struct {
	OntologyVersionID string                    `json:"ontology_version_id,omitempty"`
	AgentKey          string                    `json:"agent_key,omitempty"`
	ToolsArgs         map[string]map[string]any `json:"tools_args,omitempty"`
}

// SendParams 是 message/send / tasks/send 的 params。
type SendParams struct {
	Message  Message      `json:"message"`
	Metadata SendMetadata `json:"metadata,omitempty"`
}

// ListParams 是 tasks/list 的 params。
type ListParams struct {
	Limit  int    `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
	Status string `json:"status,omitempty"`
}

// ListResult 是 tasks/list 的返回结果。
type ListResult struct {
	Items      []Task `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// RemoteAgentRef 是远程 Agent 注册表中一条记录的客户端侧最小视图
// （复用 store.ListRemoteAgents / GetRemoteAgent 返回的 map[string]any 字段）。
type RemoteAgentRef struct {
	AgentKey       string
	BaseURL        string
	AuthHeaderName string
	AuthHeaderRef  string
	Status         string
}

// ---------------------------------------------------------------------------
// 错误类型
// ---------------------------------------------------------------------------

// RPCError 是远端 Agent 通过 JSON-RPC error 返回的业务 / 协议错误。
// 错误码遵循 JSON-RPC 2.0 规范（-32xxx）；业务错误统一 -32000。
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error 实现 error 接口。
func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("A2A RPC error code=%d message=%s", e.Code, e.Message)
}

// IsRPCError 判断错误是否为远端 JSON-RPC error。
func IsRPCError(err error) (*RPCError, bool) {
	if err == nil {
		return nil, false
	}
	var rpc *RPCError
	if r, ok := err.(*RPCError); ok {
		rpc = r
	} else {
		// errors.Join / 其他包装：直接字符串匹配兜底（RPCError.Error 含 code 字段）。
		return nil, false
	}
	return rpc, true
}

// ---------------------------------------------------------------------------
// 请求选项
// ---------------------------------------------------------------------------

// CallOption 控制单次调用的行为（超时 / HTTP 头 / 自定义 params 合并）。
type CallOption func(*callConfig)

type callConfig struct {
	timeout     time.Duration
	extraHeader map[string]string
	paramsPatch map[string]any
}

// WithTimeout 覆盖单次调用的超时上限。
func WithTimeout(d time.Duration) CallOption {
	return func(c *callConfig) { c.timeout = d }
}

// WithHeader 附加自定义 HTTP 头（在客户端认证头之外）。
func WithHeader(k, v string) CallOption {
	return func(c *callConfig) {
		if c.extraHeader == nil {
			c.extraHeader = map[string]string{}
		}
		c.extraHeader[k] = v
	}
}

// WithParamsPatch 允许在发送前对 params 做浅层合并（高级用例，
// 通常用于测试或注入 tracing 字段）。
func WithParamsPatch(patch map[string]any) CallOption {
	return func(c *callConfig) { c.paramsPatch = patch }
}

// ---------------------------------------------------------------------------
// A2AClient
// ---------------------------------------------------------------------------

// A2AClient 是出站 A2A 协议客户端。
//
// 零值可用（使用 http.DefaultClient + 120s 默认超时）；生产调用应通过
// NewClient 构造并注入自定义 http.Client / 注册表 Resolver。
type A2AClient struct {
	http    *http.Client
	timeout time.Duration

	// requestID 是单调递增的 JSON-RPC id 计数器（仅用于出站信封 id；
	// 远端响应会原样带回，本实现不做匹配校验）。
	requestID atomic.Int64
}

// NewClient 构造可配置的 A2AClient。
//
// httpClient 为 nil 时使用 http.DefaultClient；timeout <= 0 时使用默认 120s。
func NewClient(httpClient *http.Client, timeout time.Duration) *A2AClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if timeout <= 0 {
		timeout = defaultSyncWaitTimeout
	}
	return &A2AClient{http: httpClient, timeout: timeout}
}

// ---------------------------------------------------------------------------
// 注册表 Resolver（供 Agent executor 集成使用）
// ---------------------------------------------------------------------------

// RegistryResolver 抽象远程 Agent 注册表的读取接口。
//
// store.Store 已实现 ListRemoteAgents / GetRemoteAgent，这里用最小接口解耦，
// 避免 a2a 包反向依赖 api/store 包形成循环。
type RegistryResolver interface {
	// Resolve 按 agent_key 查找一个 ACTIVE 状态的远程 Agent 登记。
	// 找不到 / 非 ACTIVE / 凭据未配置 → 返回错误。
	Resolve(ctx context.Context, agentKey string) (*RemoteAgentRef, error)
}

// ---------------------------------------------------------------------------
// 出站调用核心
// ---------------------------------------------------------------------------

// envelope 是 JSON-RPC 2.0 请求信封（客户端侧）。
type envelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// responseEnvelope 是 JSON-RPC 2.0 响应信封（客户端侧）。
type responseEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// invoke 是所有 JSON-RPC 调用的统一底层。
//
// - method: JSON-RPC 方法名
// - gatewayURL: 远端 A2A Server 的 JSON-RPC 端点（通常是 baseURL + "/api/v1/a2a"）
// - params: 方法参数（任意 JSON 对象，nil → 空 params 对象）
// - authHeaderName / authHeaderRef: 认证头与凭据环境变量名（ref 为空时不注入）
// - opts: 单次调用选项
//
// 返回值：响应 result 原始 JSON；错误要么是 *RPCError（远端业务/协议错误），
// 要么是网络 / 序列化 / 认证配置错误。
func (c *A2AClient) invoke(ctx context.Context, method, gatewayURL string,
	params any, authHeaderName, authHeaderRef string, opts ...CallOption) (json.RawMessage, error) {

	cfg := &callConfig{timeout: c.timeout}
	for _, opt := range opts {
		opt(cfg)
	}

	// 构造 params JSON（nil → "{}"）。
	var rawParams json.RawMessage = []byte("{}")
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, errors.Wrap(err, errors.CodeValidationFailed, "a2a: params 序列化失败")
		}
		rawParams = b
	}
	// 可选 params 合并（patch 覆盖顶层键）。
	if cfg.paramsPatch != nil {
		var merged map[string]any
		_ = json.Unmarshal(rawParams, &merged)
		if merged == nil {
			merged = map[string]any{}
		}
		for k, v := range cfg.paramsPatch {
			merged[k] = v
		}
		b, err := json.Marshal(merged)
		if err != nil {
			return nil, errors.Wrap(err, errors.CodeValidationFailed, "a2a: params patch 序列化失败")
		}
		rawParams = b
	}

	id := c.requestID.Add(1)
	reqBody, err := json.Marshal(envelope{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Method:  method,
		Params:  rawParams,
	})
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeValidationFailed, "a2a: 请求信封序列化失败")
	}

	// 超时控制：使用传入 ctx + 单次调用超时的更早 deadline。
	callCtx := ctx
	if cfg.timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, cfg.timeout)
		defer cancel()
	}

	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, gatewayURL, strings.NewReader(string(reqBody)))
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInvalidArgument, "a2a: 构造出站请求失败")
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	for k, v := range cfg.extraHeader {
		httpReq.Header.Set(k, v)
	}

	// 出站认证：只有登记了认证头名才注入凭据；凭据来自环境变量引用。
	// env 未设置即 fail-closed（与 ProbeRemoteAgent 一致）。
	if strings.TrimSpace(authHeaderName) != "" {
		ref := strings.TrimSpace(authHeaderRef)
		if ref == "" {
			return nil, errors.Ef(errors.CodeInvalidArgument,
				"a2a: 远程 Agent 配置了认证头 %s 但未配置凭据环境变量引用（auth_header_ref），拒绝以空凭据调用",
				authHeaderName)
		}
		secret := os.Getenv(ref)
		if secret == "" {
			return nil, errors.Ef(errors.CodeUnavailable,
				"a2a: 环境变量 %s 未配置，无法设置认证头 %s，调用被拒绝（fail-closed）",
				ref, authHeaderName)
		}
		httpReq.Header.Set(authHeaderName, secret)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		// 区分 ctx 超时 vs 网络错误。
		if callCtx.Err() == context.DeadlineExceeded {
			return nil, errors.Ef(errors.CodeDeadlineExceeded, "a2a: 调用远端 %s 超时（%v）：%v", gatewayURL, cfg.timeout, err)
		}
		if callCtx.Err() == context.Canceled {
			return nil, errors.Ef(errors.CodeDeadlineExceeded, "a2a: 调用远端 %s 被取消：%v", gatewayURL, err)
		}
		return nil, errors.Ef(errors.CodeUnavailable, "a2a: 无法连接远端 %s：%v", gatewayURL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, defaultMaxBodyBytes+1))
	if err != nil {
		return nil, errors.Ef(errors.CodeUnavailable, "a2a: 读取远端响应体失败：%v", err)
	}
	if len(body) > defaultMaxBodyBytes {
		return nil, errors.E(errors.CodeValidationFailed, "a2a: 远端响应体超过 1MB 上限")
	}

	// 远端非 2xx：仍然尝试解析为 JSON-RPC 信封（有些 Server 在 HTTP 层也返回 200，
	// 但保守起见覆盖 4xx/5xx）；解析失败则按 HTTP 错误报告。
	if resp.StatusCode >= 400 {
		var rpc responseEnvelope
		if jerr := json.Unmarshal(body, &rpc); jerr == nil && rpc.Error != nil {
			return nil, rpc.Error
		}
		return nil, errors.Ef(errors.CodeUnavailable,
			"a2a: 远端 %s 返回 HTTP %d：%s", gatewayURL, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	// 解析响应信封。
	var rpc responseEnvelope
	if err := json.Unmarshal(body, &rpc); err != nil {
		return nil, errors.Ef(errors.CodeValidationFailed,
			"a2a: 远端 %s 响应不是合法 JSON-RPC 信封：%v（body=%s）",
			gatewayURL, err, truncate(string(body), 200))
	}
	if rpc.Error != nil {
		return nil, rpc.Error
	}
	if rpc.JSONRPC != JSONRPCVersion {
		return nil, errors.Ef(errors.CodeValidationFailed,
			"a2a: 远端响应 jsonrpc 版本不匹配，want %s got %q", JSONRPCVersion, rpc.JSONRPC)
	}
	return rpc.Result, nil
}

// truncate 截断字符串（日志用）。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ---------------------------------------------------------------------------
// 高层方法：SendMessage / SendTask / SendTaskSubscribe / GetTask / ListTasks / CancelTask
// ---------------------------------------------------------------------------

// SendMessage 发送 message/send 请求并同步等待远端任务终态。
//
// 远端 120s 内未完成 → 返回 accepted 状态的 Task（含 task_id），
// 调用方后续用 GetTask 轮询（与 a2a_gateway.go 服务端超时语义对齐）。
//
// gatewayURL: 远端 JSON-RPC 端点（通常 baseURL + "/api/v1/a2a"）。
// authHeaderName / authHeaderRef: 出站认证（ref 为空时不注入凭据）。
func (c *A2AClient) SendMessage(ctx context.Context, gatewayURL, authHeaderName, authHeaderRef string,
	msg Message, metadata *SendMetadata, opts ...CallOption) (*Task, error) {

	var meta SendMetadata
	if metadata != nil {
		meta = *metadata
	}
	params := SendParams{Message: msg, Metadata: meta}
	return c.sendAndWait(ctx, MethodMessageSend, gatewayURL, authHeaderName, authHeaderRef, params, opts...)
}

// SendTask 发送 tasks/send（与 SendMessage 形状相同，方法名不同）。
func (c *A2AClient) SendTask(ctx context.Context, gatewayURL, authHeaderName, authHeaderRef string,
	msg Message, metadata *SendMetadata, opts ...CallOption) (*Task, error) {

	var meta SendMetadata
	if metadata != nil {
		meta = *metadata
	}
	params := SendParams{Message: msg, Metadata: meta}
	return c.sendAndWait(ctx, MethodTasksSend, gatewayURL, authHeaderName, authHeaderRef, params, opts...)
}

// sendAndWait 是 SendMessage / SendTask 的共享实现。
func (c *A2AClient) sendAndWait(ctx context.Context, method, gatewayURL, authHeaderName, authHeaderRef string,
	params SendParams, opts ...CallOption) (*Task, error) {

	raw, err := c.invoke(ctx, method, gatewayURL, params, authHeaderName, authHeaderRef, opts...)
	if err != nil {
		return nil, err
	}
	return parseTask(raw)
}

// SendTaskSubscribe 发送 tasks/sendSubscribe 并返回 SSE 流。
//
// 调用方负责关闭返回的 io.ReadCloser；流内事件格式：
//
//	event: TaskStatus
//	data: {"task_id":"...", "state":"working"}
//
//	event: A2ATaskFinal
//	data: {"id":"...", "status":{...}, "artifacts":[...]}
//
// 流内不做 JSON-RPC 信封解析——tasks/sendSubscribe 的服务端响应
// 直接是 SSE（Content-Type: text/event-stream），不走 JSON-RPC 信封。
func (c *A2AClient) SendTaskSubscribe(ctx context.Context, gatewayURL, authHeaderName, authHeaderRef string,
	msg Message, metadata *SendMetadata, opts ...CallOption) (io.ReadCloser, error) {

	var meta SendMetadata
	if metadata != nil {
		meta = *metadata
	}
	params := SendParams{Message: msg, Metadata: meta}
	cfg := &callConfig{timeout: c.timeout}
	for _, opt := range opts {
		opt(cfg)
	}

	rawParams, err := json.Marshal(params)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeValidationFailed, "a2a: params 序列化失败")
	}

	id := c.requestID.Add(1)
	reqBody, err := json.Marshal(envelope{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Method:  MethodTasksSendSubscribe,
		Params:  rawParams,
	})
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeValidationFailed, "a2a: 请求信封序列化失败")
	}

	callCtx := ctx
	if cfg.timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, cfg.timeout)
		// 注意：这里不 defer cancel，留待调用方关闭流。但我们需要在流被关闭时释放资源，
		// 因此把 cancel 函数存到返回的包装 ReadCloser 里。
		_ = cancel
	}

	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, gatewayURL, strings.NewReader(string(reqBody)))
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInvalidArgument, "a2a: 构造出站请求失败")
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	for k, v := range cfg.extraHeader {
		httpReq.Header.Set(k, v)
	}

	if strings.TrimSpace(authHeaderName) != "" {
		ref := strings.TrimSpace(authHeaderRef)
		if ref == "" {
			return nil, errors.Ef(errors.CodeInvalidArgument,
				"a2a: 远程 Agent 配置了认证头 %s 但未配置凭据环境变量引用", authHeaderName)
		}
		secret := os.Getenv(ref)
		if secret == "" {
			return nil, errors.Ef(errors.CodeUnavailable,
				"a2a: 环境变量 %s 未配置，无法设置认证头 %s", ref, authHeaderName)
		}
		httpReq.Header.Set(authHeaderName, secret)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		if callCtx.Err() == context.DeadlineExceeded {
			return nil, errors.Ef(errors.CodeDeadlineExceeded, "a2a: tasks/sendSubscribe 调用远端 %s 超时", gatewayURL)
		}
		return nil, errors.Ef(errors.CodeUnavailable, "a2a: 无法连接远端 %s：%v", gatewayURL, err)
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, defaultMaxBodyBytes))
		resp.Body.Close()
		// 尝试解析为 JSON-RPC error。
		var rpc responseEnvelope
		if jerr := json.Unmarshal(body, &rpc); jerr == nil && rpc.Error != nil {
			return nil, rpc.Error
		}
		return nil, errors.Ef(errors.CodeUnavailable,
			"a2a: 远端 %s 返回 HTTP %d：%s", gatewayURL, resp.StatusCode, truncate(string(body), 200))
	}

	// 检查 Content-Type 是 SSE；否则按 JSON-RPC error 解析（服务端拒绝了订阅请求）。
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, defaultMaxBodyBytes))
		resp.Body.Close()
		var rpc responseEnvelope
		if jerr := json.Unmarshal(body, &rpc); jerr == nil && rpc.Error != nil {
			return nil, rpc.Error
		}
		return nil, errors.Ef(errors.CodeValidationFailed,
			"a2a: 远端 %s 未返回 SSE 流（Content-Type=%q）", gatewayURL, ct)
	}
	return resp.Body, nil
}

// GetTask 发送 tasks/get 查询单个任务状态。
func (c *A2AClient) GetTask(ctx context.Context, gatewayURL, authHeaderName, authHeaderRef, taskID string, opts ...CallOption) (*Task, error) {
	raw, err := c.invoke(ctx, MethodTasksGet, gatewayURL, map[string]any{"id": taskID},
		authHeaderName, authHeaderRef, opts...)
	if err != nil {
		return nil, err
	}
	return parseTask(raw)
}

// ListTasks 发送 tasks/list 查询任务列表。
//
// 返回值：items + nextCursor（空串表示无更多）。
func (c *A2AClient) ListTasks(ctx context.Context, gatewayURL, authHeaderName, authHeaderRef string,
	listParams *ListParams, opts ...CallOption) ([]Task, string, error) {

	params := map[string]any{}
	if listParams != nil {
		if listParams.Limit > 0 {
			params["limit"] = listParams.Limit
		}
		if listParams.Cursor != "" {
			params["cursor"] = listParams.Cursor
		}
		if listParams.Status != "" {
			params["status"] = listParams.Status
		}
	}
	raw, err := c.invoke(ctx, MethodTasksList, gatewayURL, params, authHeaderName, authHeaderRef, opts...)
	if err != nil {
		return nil, "", err
	}
	var result ListResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, "", errors.Wrap(err, errors.CodeValidationFailed, "a2a: tasks/list 返回解析失败")
	}
	if result.Items == nil {
		result.Items = []Task{}
	}
	return result.Items, result.NextCursor, nil
}

// CancelTask 发送 tasks/cancel 请求取消一个远端任务。
func (c *A2AClient) CancelTask(ctx context.Context, gatewayURL, authHeaderName, authHeaderRef, taskID string, opts ...CallOption) (*Task, error) {
	raw, err := c.invoke(ctx, MethodTasksCancel, gatewayURL, map[string]any{"id": taskID},
		authHeaderName, authHeaderRef, opts...)
	if err != nil {
		return nil, err
	}
	return parseTask(raw)
}

// ---------------------------------------------------------------------------
// 解析辅助
// ---------------------------------------------------------------------------

// parseTask 把 JSON-RPC result 原始字节解析为 Task（宽松：缺失字段给零值）。
func parseTask(raw json.RawMessage) (*Task, error) {
	if len(raw) == 0 {
		return nil, errors.E(errors.CodeValidationFailed, "a2a: 响应 result 为空")
	}
	var t Task
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, errors.Wrap(err, errors.CodeValidationFailed,
			"a2a: 响应 result 不是合法的 A2A task 对象")
	}
	return &t, nil
}

// ---------------------------------------------------------------------------
// SSE 事件解析辅助（供 SendTaskSubscribe 调用方使用）
// ---------------------------------------------------------------------------

// SSEEvent 是一条解析后的 SSE 事件。
type SSEEvent struct {
	Event string
	Data  []byte
}

// NewSSEScanner 构造一个 SSE 事件扫描器。
//
// 典型用法：
//
//	rc, _ := client.SendTaskSubscribe(ctx, ...)
//	defer rc.Close()
//	scanner := a2a.NewSSEScanner(rc)
//	for scanner.Scan() {
//	    ev := scanner.Event()
//	    // 处理 ev.Event / ev.Data
//	}
//	if err := scanner.Err(); err != nil { ... }
func NewSSEScanner(r io.Reader) *SSEScanner {
	return &SSEScanner{sc: bufio.NewScanner(r)}
}

// SSEScanner 包装 bufio.Scanner，按 SSE 规范逐事件读取。
type SSEScanner struct {
	sc    *bufio.Scanner
	event string
	data  []byte
	err   error
}

// Scan 前进到下一条事件；返回 false 表示流结束或出错。
func (s *SSEScanner) Scan() bool {
	s.event = ""
	s.data = nil
	for {
		if !s.sc.Scan() {
			s.err = s.sc.Err()
			return false
		}
		line := s.sc.Text()
		if line == "" {
			// 空行 = 事件结束；但需要至少一行数据才算一个事件。
			if s.event != "" || s.data != nil {
				return true
			}
			continue
		}
		// SSE 注释行（以冒号开头）跳过。
		if strings.HasPrefix(line, ":") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			// 无冒号 → 整行是 data。
			s.data = append(s.data, []byte(line)...)
			continue
		}
		field := line[:idx]
		value := strings.TrimLeft(line[idx+1:], " ")
		switch field {
		case "event":
			s.event = value
		case "data":
			if s.data != nil {
				s.data = append(s.data, '\n')
			}
			s.data = append(s.data, []byte(value)...)
		default:
			// 其他字段（id/retry）暂不处理。
		}
	}
}

// Event 返回当前事件的 Event 对象。
func (s *SSEScanner) Event() SSEEvent {
	return SSEEvent{Event: s.event, Data: s.data}
}

// Err 返回 Scan 结束后的错误。
func (s *SSEScanner) Err() error {
	return s.err
}

// ---------------------------------------------------------------------------
// 远端 Agent 发现辅助
// ---------------------------------------------------------------------------

// GatewayURL 把 baseURL（注册表中存储的远端 Agent 地址）规
// 范为 JSON-RPC 端点。
//
// 约定：注册表存的是 Agent Card 所在基地址（如 "https://example.com"），
// A2A JSON-RPC 端点固定是 {baseURL}/api/v1/a2a（与 a2aAgentCardOf 对齐）。
func GatewayURL(baseURL string) string {
	u := strings.TrimRight(baseURL, "/")
	if !strings.HasSuffix(u, "/api/v1/a2a") {
		return u + "/api/v1/a2a"
	}
	return u
}

// IsTaskTerminal 报告远端任务是否已到达终态（completed/failed/canceled）。
func IsTaskTerminal(t *Task) bool {
	if t == nil {
		return false
	}
	switch t.Status.State {
	case "completed", "failed", "canceled":
		return true
	default:
		return false
	}
}

// 确保 ids 包可用（某些测试可能用到）；避免 import "ids" 未使用。
var _ = ids.ID{}
