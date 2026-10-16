package handlers

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"odap/internal/action"
	"odap/internal/api/httperr"
	"odap/internal/api/sse"
	"odap/internal/api/store"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/runstate"
	"odap/internal/platform/tenantctx"
)

// ---------------------------------------------------------------------------
// A2A Server 网关（模块 C「Server 发布」面）
//
// 端点（路由装配由 cmd/api 负责）：
//   GET  /.well-known/agent.json  Agent Card：运行期实时装配（registry skills 汇总），
//                                 url 按请求 Host 构造，绝不落静态数据
//   POST /api/v1/a2a              JSON-RPC 2.0 单端点
//
// JSON-RPC 方法白名单（Google Agent2Agent 协议要点）：
//   message/send 与 tasks/send       同步：创建 Run 并等待终态（120s 超时，
//                                    超时返回当前状态 + task_id 供 tasks/get 轮询）
//   tasks/sendSubscribe              创建 Run 后以 SSE 按任务 id 过滤推流至终态
//   tasks/get / tasks/list           Run 详情映射 / keyset 分页列表
//   tasks/cancel                     映射 Run 取消（harness 状态机）
//
// 任务状态映射：Run 17 态收敛为 A2A 4 态（submitted/working/completed/failed/canceled）。
// 错误码遵循 JSON-RPC 2.0 规范：-32700 解析错误 / -32600 无效请求 / -32601 方法
// 不存在 / -32602 无效参数 / -32603 内部错误；业务错误统一 -32000 带中文 message。
// 每次调用过审计；任务创建/取消等状态变更语义额外发布 SSE。
// ---------------------------------------------------------------------------

// a2aSyncWaitTimeout 是 message/send 同步等待终态的上限；超时返回当前状态
// + task_id，由调用方改走 tasks/get 轮询（与 Run 的异步执行语义对齐：
// Agent 驱动 Run 由 Worker 进程推进，API 进程只能等待）。
const a2aSyncWaitTimeout = 120 * time.Second

// a2aSyncPollInterval 是同步等待的轮询间隔。
const a2aSyncPollInterval = 500 * time.Millisecond

// a2aStreamPollInterval 是 sendSubscribe 流内的状态轮询间隔。
const a2aStreamPollInterval = time.Second

// a2aObjectiveLimit 与本体审查的 objective 上限一致（消息文本即业务目标）。
const a2aObjectiveLimit = 2000

// JSON-RPC 2.0 规范错误码 + 业务错误码。
const (
	a2aCodeParseError     = -32700
	a2aCodeInvalidRequest = -32600
	a2aCodeMethodNotFound = -32601
	a2aCodeInvalidParams  = -32602
	a2aCodeInternalError  = -32603
	a2aCodeServerError    = -32000
)

// a2aJSONRPC 是协议固定版本。
const a2aJSONRPC = "2.0"

// a2aMethods 是 JSON-RPC 方法白名单（不在白名单一律 -32601）。
var a2aMethods = map[string]bool{
	"message/send":        true,
	"tasks/send":          true,
	"tasks/sendSubscribe": true,
	"tasks/get":           true,
	"tasks/list":          true,
	"tasks/cancel":        true,
}

// ---------------------------------------------------------------------------
// Agent Card（GET /.well-known/agent.json）
// ---------------------------------------------------------------------------

// AgentCard 处理 GET /.well-known/agent.json：实时装配本平台的 A2A Agent Card。
//
// 卡片内容与租户绑定（skills 汇总自 registry），因此要求已认证租户身份；
// name=平台名、url=按请求 Host 构造（尊重 X-Forwarded-Proto 反代头）、
// capabilities.streaming=true、skills 为 registry 中 status 正常的 Agent
// （内置+用户）各一条 + 内置技能 enterprise-knowledge-qa（若存在），
// 默认输入/输出模式为文本与 JSON。
func (h *Handler) AgentCard(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	skills, err := h.deps.Store.A2AAgentCardSkills(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, a2aAgentCardOf("ODAP", h.deps.Version, a2aSelfBaseURL(r), skills))
}

// a2aSelfBaseURL 按请求 Host 构造对外基地址（反代场景尊重 X-Forwarded-Proto）。
func a2aSelfBaseURL(r *http.Request) string {
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	return scheme + "://" + r.Host
}

// a2aAgentCardOf 装配 Agent Card（纯函数，便于测试形状）。
func a2aAgentCardOf(name, version, selfBaseURL string, skills []map[string]any) map[string]any {
	if skills == nil {
		skills = []map[string]any{}
	}
	return map[string]any{
		"name":        name,
		"description": "ODAP 企业本体智能平台 A2A Server：提供智能体任务编排、本体规则审查与企业知识问答能力",
		"url":         selfBaseURL + "/api/v1/a2a",
		"version":     version,
		"capabilities": map[string]any{
			"streaming": true,
		},
		"skills":             skills,
		"defaultInputModes":  []string{"text/plain", "application/json"},
		"defaultOutputModes": []string{"text/plain", "application/json"},
	}
}

// ---------------------------------------------------------------------------
// JSON-RPC 2.0 信封（解析与错误写出）
// ---------------------------------------------------------------------------

// a2aRPCError 是 JSON-RPC error 对象的内存形态。
type a2aRPCError struct {
	Code    int
	Message string
	Data    map[string]any
}

// a2aRequest 是校验通过后的 JSON-RPC 请求。
type a2aRequest struct {
	Method string
	ID     json.RawMessage
	Params json.RawMessage
}

// validA2AID 报告 id 是否为合法的数字或字符串（null/对象/数组/布尔均拒绝）。
func validA2AID(raw json.RawMessage) bool {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return false
	}
	switch v.(type) {
	case string, float64:
		return true
	default:
		return false
	}
}

// parseA2AEnvelope 解析并校验 JSON-RPC 2.0 信封（纯函数）：
//   - 非法 JSON → -32700；jsonrpc/method/id 不合规或批量请求 → -32600；
//   - 校验通过返回方法名、id 与 params（缺省 "{}"）。
func parseA2AEnvelope(raw []byte) (*a2aRequest, *a2aRPCError) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, &a2aRPCError{a2aCodeInvalidRequest, "请求体不能为空", nil}
	}
	if trimmed[0] == '[' {
		return nil, &a2aRPCError{a2aCodeInvalidRequest, "不支持 JSON-RPC 批量请求，仅接受单个请求对象", nil}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, &a2aRPCError{a2aCodeParseError, "请求体不是合法 JSON（JSON-RPC 解析错误）", nil}
	}
	if v, ok := fields["jsonrpc"]; !ok || string(v) != `"2.0"` {
		return nil, &a2aRPCError{a2aCodeInvalidRequest, `jsonrpc 必须为 "2.0"`, nil}
	}
	var method string
	if rawMethod, ok := fields["method"]; !ok || json.Unmarshal(rawMethod, &method) != nil || method == "" {
		return nil, &a2aRPCError{a2aCodeInvalidRequest, "method 必填且必须是字符串", nil}
	}
	id, ok := fields["id"]
	if !ok || !validA2AID(id) {
		return nil, &a2aRPCError{a2aCodeInvalidRequest, "id 必填且必须是数字或字符串", nil}
	}
	params, ok := fields["params"]
	if !ok || len(params) == 0 {
		params = json.RawMessage("{}")
	}
	return &a2aRequest{Method: method, ID: id, Params: params}, nil
}

// writeA2AResult 写出 JSON-RPC 成功响应（HTTP 200；协议错误也走 200，
// 由 error.code 承载语义，便于 A2A 客户端统一按信封解析）。
func writeA2AResult(w http.ResponseWriter, id json.RawMessage, result any) {
	httperr.WriteJSON(w, http.StatusOK, map[string]any{"jsonrpc": a2aJSONRPC, "id": id, "result": result})
}

// writeA2AError 写出 JSON-RPC 错误响应。
func writeA2AError(w http.ResponseWriter, id json.RawMessage, rpcErr *a2aRPCError) {
	errObj := map[string]any{"code": rpcErr.Code, "message": rpcErr.Message}
	if rpcErr.Data != nil {
		errObj["data"] = rpcErr.Data
	}
	httperr.WriteJSON(w, http.StatusOK, map[string]any{"jsonrpc": a2aJSONRPC, "id": id, "error": errObj})
}

// a2aRPCErrorOf 把内部错误映射为 JSON-RPC 错误：
// 参数类（INVALID_ARGUMENT）→ -32602；其余已登记业务错误 → -32000 带中文 message
// （错误消息由 platform/errors 统一脱敏，不含底层 Cause）；未知错误 → -32603。
func a2aRPCErrorOf(err error) *a2aRPCError {
	var ae *errors.AppError
	if stderrors.As(err, &ae) {
		if ae.Code == errors.CodeInvalidArgument {
			return &a2aRPCError{a2aCodeInvalidParams, ae.Message, nil}
		}
		return &a2aRPCError{a2aCodeServerError, ae.Message, nil}
	}
	return &a2aRPCError{a2aCodeInternalError, "内部错误", nil}
}

// ---------------------------------------------------------------------------
// JSON-RPC 方法分发
// ---------------------------------------------------------------------------

// A2AGateway 处理 POST /api/v1/a2a（JSON-RPC 2.0 单端点）。
func (h *Handler) A2AGateway(w http.ResponseWriter, r *http.Request) {
	identity, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || len(body) > maxBodyBytes {
		writeA2AError(w, nil, &a2aRPCError{a2aCodeInvalidRequest, "请求体缺失、过大或读取失败", nil})
		return
	}
	req, rpcErr := parseA2AEnvelope(body)
	if rpcErr != nil {
		h.recordAudit(r, identity, "A2AGatewayCall", "a2a.envelope", "REJECTED", "", "", ids.ID{},
			map[string]any{"error": rpcErr.Message})
		writeA2AError(w, nil, rpcErr)
		return
	}
	if !a2aMethods[req.Method] {
		h.recordAudit(r, identity, "A2AGatewayCall", "a2a.envelope", "REJECTED", "", "", ids.ID{},
			map[string]any{"method": req.Method, "error": "method 不在白名单"})
		writeA2AError(w, req.ID, &a2aRPCError{a2aCodeMethodNotFound,
			"method 不在 A2A 网关白名单内：" + req.Method, nil})
		return
	}
	if h.deps.Store == nil {
		writeA2AError(w, req.ID, &a2aRPCError{a2aCodeServerError, "持久化适配器未装配，A2A 网关不可用", nil})
		return
	}
	switch req.Method {
	case "message/send", "tasks/send":
		h.a2aSend(w, r, identity, req, false)
	case "tasks/sendSubscribe":
		h.a2aSend(w, r, identity, req, true)
	case "tasks/get":
		h.a2aGet(w, r, identity, req)
	case "tasks/list":
		h.a2aList(w, r, identity, req)
	case "tasks/cancel":
		h.a2aCancel(w, r, identity, req)
	default:
		writeA2AError(w, req.ID, &a2aRPCError{a2aCodeMethodNotFound, "method 不在 A2A 网关白名单内：" + req.Method, nil})
	}
}

// a2aGatewayAudit 是网关调用的统一审计入口（每次调用必过）。
func (h *Handler) a2aGatewayAudit(r *http.Request, identity tenantctx.Identity, method, outcome, resID string, runID ids.ID, extra map[string]any) {
	payload := map[string]any{"method": method}
	for k, v := range extra {
		payload[k] = v
	}
	h.recordAudit(r, identity, "A2AGatewayCall", "a2a."+method, outcome, "run", resID, runID, payload)
}

// a2aFailRPC 把内部错误映射为 JSON-RPC 错误写出，并审计 FAILURE。
func (h *Handler) a2aFailRPC(w http.ResponseWriter, r *http.Request, identity tenantctx.Identity,
	req *a2aRequest, err error) {
	rpcErr := a2aRPCErrorOf(err)
	h.a2aGatewayAudit(r, identity, req.Method, "FAILURE", "", ids.ID{},
		map[string]any{"error": rpcErr.Message})
	writeA2AError(w, req.ID, rpcErr)
}

// ---------------------------------------------------------------------------
// message/send / tasks/send / tasks/sendSubscribe
// ---------------------------------------------------------------------------

// a2aTextPart 是 A2A message.parts 中的文本段（其余 kind 被忽略）。
type a2aTextPart struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// a2aMessage 是 A2A message 信封。
type a2aMessage struct {
	Role  string        `json:"role"`
	Parts []a2aTextPart `json:"parts"`
}

// a2aSendMetadata 是 A2A params.metadata。
type a2aSendMetadata struct {
	// OntologyVersionID 可选：把任务绑定到某个本体版本（Run 的本体绑定列）。
	OntologyVersionID string `json:"ontology_version_id"`
	// AgentKey 可选：目标智能体键；缺省用内置运行编排智能体。
	AgentKey string `json:"agent_key"`
	// ToolsArgs 可选：按工具键显式供给 READ 工具入参（tools_args[tool_key]）。
	// 直跑链的 executeDeclaredReadTools 按 Agent 依赖清单声明的只读工具逐个
	// 取这里的入参做 input_schema 强校验；未供给或缺 required 字段将如实失败，
	// 绝不静默编造参数。
	ToolsArgs map[string]map[string]any `json:"tools_args,omitempty"`
}

// a2aSendParams 是 message/send / tasks/send 的 params。
type a2aSendParams struct {
	Message  a2aMessage      `json:"message"`
	Metadata a2aSendMetadata `json:"metadata"`
}

// parseA2ASendParams 解析 send 类 params 并抽取消息文本（纯函数）。
// 只有 kind="text"（或省略 kind）的文本段参与拼接；没有任何文本段即 -32602。
func parseA2ASendParams(raw json.RawMessage) (*a2aSendParams, string, *a2aRPCError) {
	if len(raw) == 0 {
		return nil, "", &a2aRPCError{a2aCodeInvalidParams, "params 必填", nil}
	}
	var p a2aSendParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, "", &a2aRPCError{a2aCodeInvalidParams,
			"params 必须是对象：{message:{role,parts:[{kind:\"text\",text}]}, metadata?{ontology_version_id,agent_key,tools_args}}（tools_args 每个值必须是对象）", nil}
	}
	if len(p.Message.Parts) == 0 {
		return nil, "", &a2aRPCError{a2aCodeInvalidParams, "message.parts 必填（至少一个 kind=\"text\" 的文本段）", nil}
	}
	texts := make([]string, 0, len(p.Message.Parts))
	for _, part := range p.Message.Parts {
		if part.Kind == "" || part.Kind == "text" {
			texts = append(texts, part.Text)
		}
	}
	text := strings.TrimSpace(strings.Join(texts, "\n"))
	if text == "" {
		return nil, "", &a2aRPCError{a2aCodeInvalidParams,
			"message.parts 必须包含至少一个 kind=\"text\" 的非空文本段", nil}
	}
	if len([]rune(text)) > a2aObjectiveLimit {
		return nil, "", &a2aRPCError{a2aCodeInvalidParams,
			"message 文本过长（上限 2000 字），请拆分任务后重试", nil}
	}
	return &p, text, nil
}

// a2aSend 实现 message/send、tasks/send（同步等终态）与 tasks/sendSubscribe（SSE 流）。
func (h *Handler) a2aSend(w http.ResponseWriter, r *http.Request, identity tenantctx.Identity, req *a2aRequest, stream bool) {
	ctx := r.Context()
	params, text, rpcErr := parseA2ASendParams(req.Params)
	if rpcErr != nil {
		h.a2aGatewayAudit(r, identity, req.Method, "REJECTED", "", ids.ID{}, map[string]any{"error": rpcErr.Message})
		writeA2AError(w, req.ID, rpcErr)
		return
	}

	// agent_key → 最新已发布 AgentVersionID（fail-closed）。
	// 缺省路由绝不落在 run-orchestrator 等编排型 Agent 上——它们没有
	// prompt_template.system，消息会在 render_prompt 阶段被拒绝；改为解析
	// 首个「已发布且带系统提示词」的业务 Agent（确定性取最旧），一个都没有
	// 则明确报错。
	agentKey := strings.TrimSpace(params.Metadata.AgentKey)
	var versionID ids.ID
	var err error
	if agentKey == "" {
		agentKey, versionID, err = h.deps.Store.DefaultChatAgentVersion(ctx)
	} else {
		versionID, err = h.deps.Store.LatestPublishedAgentVersion(ctx, agentKey)
	}
	if err != nil {
		h.a2aFailRPC(w, r, identity, req, err)
		return
	}
	// 可选的本体版本绑定：给了就必须是合法 UUID（否则 -32602）。
	var ontologyVersionID ids.ID
	if strings.TrimSpace(params.Metadata.OntologyVersionID) != "" {
		ov, perr := ids.Parse(params.Metadata.OntologyVersionID)
		if perr != nil {
			rpcErr := &a2aRPCError{a2aCodeInvalidParams, "metadata.ontology_version_id 必须是 UUID", nil}
			h.a2aGatewayAudit(r, identity, req.Method, "REJECTED", "", ids.ID{}, map[string]any{"error": rpcErr.Message})
			writeA2AError(w, req.ID, rpcErr)
			return
		}
		ontologyVersionID = ov
	}

	// 完整 A2A 消息入 input_payload，保证「外部调用」在 Run 侧可追溯。
	// metadata.tools_args 同步透传为 input_payload.tools_args：A2A 调用方
	// 由此显式供给依赖清单声明工具的入参（worker 直跑链的取参约定）。
	inputPayloadMap := map[string]any{
		"a2a":      map[string]any{"message": params.Message, "metadata": params.Metadata},
		"via":      "a2a",
		"rpc_mode": map[bool]string{true: "sendSubscribe", false: req.Method}[stream],
	}
	if len(params.Metadata.ToolsArgs) > 0 {
		inputPayloadMap["tools_args"] = params.Metadata.ToolsArgs
	}
	inputPayload, _ := json.Marshal(inputPayloadMap)
	run, err := h.deps.Store.CreateRun(ctx, store.CreateRunInput{
		RequestedBy:       identity.SubjectID,
		AgentVersionID:    versionID,
		OntologyVersionID: ontologyVersionID,
		Objective:         text,
		InputPayload:      inputPayload,
		InputDigest:       digestHex(inputPayload),
		Priority:          100,
		MaxAttempts:       3,
	})
	if err != nil {
		h.a2aFailRPC(w, r, identity, req, err)
		return
	}
	runID, _ := ids.Parse(run.ID)
	h.a2aGatewayAudit(r, identity, req.Method, "SUCCESS", run.ID, runID,
		map[string]any{"task_id": run.ID, "agent_key": agentKey, "stream": stream})
	// 与 CreateRun 处理器同源同形的任务创建事件（SSE 订阅方按 run_id 过滤）。
	h.publish(identity.TenantID.String(), "RunCreated", sse.Normal,
		map[string]any{"run_id": run.ID, "status": run.Status})
	// 模块 D：A2A 任务受理动作留痕（旁路观测；终态由 worker 的 Run 留痕承接）。
	h.RecordActionForFlow(ctx, identity.TenantID, action.Input{
		Type:      action.TypeAction,
		Title:     "A2A 任务受理：" + agentKey,
		Summary:   text,
		RunID:     runID,
		ActorKind: action.ActorA2A,
		ActorName: identity.SubjectID.String(),
	})

	if stream {
		if h.deps.Events == nil {
			writeA2AError(w, req.ID, &a2aRPCError{a2aCodeServerError,
				"实时事件总线未装配，无法订阅任务流；可改用 message/send 轮询", nil})
			return
		}
		// 响应头此时才写：SSE 响应取代 JSON-RPC result（流本身承载任务状态）。
		h.a2aStreamTask(w, r, identity, runID)
		return
	}

	run = h.a2aWaitTerminal(ctx, runID)
	writeA2AResult(w, req.ID, a2aTaskView(run))
}

// a2aWaitTerminal 同步等待 Run 到达终态（120s 上限）。
// 返回到达终态的 Run；超时/读失败/客户端断开时返回按当前状态回读的 Run
// （调用方据此返回 accepted 视图供 tasks/get 轮询）。
func (h *Handler) a2aWaitTerminal(ctx context.Context, runID ids.ID) *store.Run {
	deadline := time.NewTimer(a2aSyncWaitTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(a2aSyncPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return h.a2aRunBestEffort(ctx, runID)
		case <-deadline.C:
			return h.a2aRunBestEffort(ctx, runID)
		case <-ticker.C:
			status, err := h.deps.Store.RunExists(ctx, runID)
			if err != nil {
				continue // 瞬时读失败：下一轮重试，直至超时上限
			}
			if !runstate.IsTerminal(status) {
				continue
			}
			if run, err := h.deps.Store.GetRun(ctx, runID); err == nil {
				return run
			}
		}
	}
}

// a2aRunBestEffort 尽力回读 Run 当前状态；完全读不到时返回仅含 id 的占位
// （状态如实标注 CREATED→submitted，绝不编造终态）。
func (h *Handler) a2aRunBestEffort(ctx context.Context, runID ids.ID) *store.Run {
	if run, err := h.deps.Store.GetRun(ctx, runID); err == nil {
		return run
	}
	return &store.Run{ID: runID.String(), Status: runstate.Created}
}

// a2aStreamTask 以 SSE 返回任务进度：按 task id 过滤实时事件（防串流），
// 并按秒轮询状态补发 TaskStatus，直至终态或 120s 上限。
func (h *Handler) a2aStreamTask(w http.ResponseWriter, r *http.Request, identity tenantctx.Identity, runID ids.ID) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeA2AError(w, nil, &a2aRPCError{a2aCodeInternalError, "服务端不支持流式响应", nil})
		return
	}
	sub, err := h.deps.Events.Subscribe(identity.TenantID.String(), "")
	if err != nil {
		writeA2AError(w, nil, &a2aRPCError{a2aCodeServerError, "订阅实时事件失败：" + err.Error(), nil})
		return
	}
	defer h.deps.Events.Close(sub)

	hdr := w.Header()
	hdr.Set("Content-Type", "text/event-stream; charset=utf-8")
	hdr.Set("Cache-Control", "no-cache, no-transform")
	hdr.Set("Connection", "keep-alive")
	hdr.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	writeA2ASSE(w, "TaskStatus", map[string]any{
		"task_id": runID.String(), "state": a2aTaskStateOf(runstate.Created)})
	flusher.Flush()

	hb := time.NewTicker(sseHeartbeat)
	defer hb.Stop()
	poll := time.NewTicker(a2aStreamPollInterval)
	defer poll.Stop()
	deadline := time.NewTimer(a2aSyncWaitTimeout)
	defer deadline.Stop()
	last := runstate.Created
	for {
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			// 与 message/send 相同的等待上限：超时结束流，客户端改用 tasks/get 轮询。
			writeA2ASSE(w, "TaskStatus", map[string]any{
				"task_id": runID.String(), "state": a2aTaskStateOf(last),
				"note": "等待终态超时，流已结束；可继续用 tasks/get 轮询"})
			flusher.Flush()
			return
		case ev, ok := <-sub.C():
			if !ok {
				return // 订阅被服务端强制关闭（慢消费者溢出）
			}
			// 防串流：仅透传载荷 run_id 等于本任务的事件，其余一律丢弃。
			if !a2aEventForTask(ev, runID.String()) {
				continue
			}
			writeA2ASSE(w, "A2ATaskEvent", map[string]any{
				"task_id":     runID.String(),
				"event_type":  ev.Type,
				"occurred_at": ev.OccurredAt.UTC().Format(time.RFC3339),
				"payload":     json.RawMessage(ev.Data),
			})
			flusher.Flush()
		case <-poll.C:
			status, err := h.deps.Store.RunExists(r.Context(), runID)
			if err != nil {
				continue // 瞬时读失败：下一轮重试
			}
			if status == last {
				continue
			}
			writeA2ASSE(w, "TaskStatus", map[string]any{
				"task_id": runID.String(), "state": a2aTaskStateOf(status)})
			flusher.Flush()
			last = status
			if runstate.IsTerminal(status) {
				if run, gerr := h.deps.Store.GetRun(r.Context(), runID); gerr == nil {
					writeA2ASSE(w, "A2ATaskFinal", a2aTaskView(run))
				} else {
					writeA2ASSE(w, "A2ATaskFinal", map[string]any{
						"task_id": runID.String(),
						"status":  map[string]any{"state": a2aTaskStateOf(status)},
					})
				}
				flusher.Flush()
				return
			}
		case <-hb.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

// a2aEventForTask 报告实时事件是否属于指定任务（载荷 run_id 匹配）。
// 载荷缺失 run_id 或解析失败的事件一律不透传（宁可少发，不可串流）。
func a2aEventForTask(ev sse.Event, taskID string) bool {
	var payload map[string]any
	if err := json.Unmarshal(ev.Data, &payload); err != nil {
		return false
	}
	return payloadString(payload, "run_id") == taskID
}

// writeA2ASSE 写出一条具名 SSE 事件。
func writeA2ASSE(w io.Writer, eventType string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		data = []byte("null")
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, data)
}

// ---------------------------------------------------------------------------
// tasks/get / tasks/list / tasks/cancel
// ---------------------------------------------------------------------------

// a2aTaskRefParams 是按 id 引用任务的 params（id 与 task_id 均接受）。
type a2aTaskRefParams struct {
	ID     string `json:"id"`
	TaskID string `json:"task_id"`
}

// parseA2ATaskRef 解析任务引用（Run id）。
func parseA2ATaskRef(raw json.RawMessage) (ids.ID, *a2aRPCError) {
	var p a2aTaskRefParams
	if len(raw) == 0 || json.Unmarshal(raw, &p) != nil {
		return ids.ID{}, &a2aRPCError{a2aCodeInvalidParams, "params 必须是对象：{id: \"<任务 id>\"}", nil}
	}
	ref := strings.TrimSpace(p.ID)
	if ref == "" {
		ref = strings.TrimSpace(p.TaskID)
	}
	if ref == "" {
		return ids.ID{}, &a2aRPCError{a2aCodeInvalidParams, "params.id 必填（任务 id，即 Run id）", nil}
	}
	runID, err := ids.Parse(ref)
	if err != nil {
		return ids.ID{}, &a2aRPCError{a2aCodeInvalidParams, "params.id 必须是 UUID", nil}
	}
	return runID, nil
}

// a2aGet 实现 tasks/get：Run 详情映射为 task 视图。
func (h *Handler) a2aGet(w http.ResponseWriter, r *http.Request, identity tenantctx.Identity, req *a2aRequest) {
	runID, rpcErr := parseA2ATaskRef(req.Params)
	if rpcErr != nil {
		h.a2aGatewayAudit(r, identity, req.Method, "REJECTED", "", ids.ID{}, map[string]any{"error": rpcErr.Message})
		writeA2AError(w, req.ID, rpcErr)
		return
	}
	run, err := h.deps.Store.GetRun(r.Context(), runID)
	if err != nil {
		h.a2aFailRPC(w, r, identity, req, err)
		return
	}
	h.a2aGatewayAudit(r, identity, req.Method, "SUCCESS", run.ID, runID, nil)
	writeA2AResult(w, req.ID, a2aTaskView(run))
}

// a2aListParams 是 tasks/list 的 params（cursor 分页，默认 10，沿 Run keyset）。
type a2aListParams struct {
	Limit  int    `json:"limit"`
	Cursor string `json:"cursor"`
	// Status 可选：接受平台 Run 状态（如 COMPLETED），或 A2A 状态
	// submitted/completed/failed/canceled（working 对应多个平台状态，不接受）。
	Status string `json:"status"`
}

// a2aStatusFilter 把 A2A 状态或平台 Run 状态收敛为 Run 列表过滤值。
func a2aStatusFilter(s string) (string, *a2aRPCError) {
	if s == "" {
		return "", nil
	}
	if runstate.Valid(s) {
		return s, nil
	}
	switch s {
	case "submitted":
		return runstate.Created, nil
	case "completed":
		return runstate.Completed, nil
	case "failed":
		return runstate.Failed, nil
	case "canceled":
		return runstate.Cancelled, nil
	default:
		return "", &a2aRPCError{a2aCodeInvalidParams,
			"status 不是已登记的 Run 状态或 A2A 任务状态（working 对应多个平台状态，请改用平台状态过滤）", nil}
	}
}

// a2aList 实现 tasks/list：Run 列表按 created_at 降序 keyset 分页映射为 task 视图。
func (h *Handler) a2aList(w http.ResponseWriter, r *http.Request, identity tenantctx.Identity, req *a2aRequest) {
	var p a2aListParams
	if len(req.Params) > 0 && json.Unmarshal(req.Params, &p) != nil {
		writeA2AError(w, req.ID, &a2aRPCError{a2aCodeInvalidParams, "params 必须是对象：{limit?, cursor?, status?}", nil})
		return
	}
	limit, err := store.NormalizeLimit(p.Limit, 10)
	if err != nil {
		writeA2AError(w, req.ID, &a2aRPCError{a2aCodeInvalidParams, err.Error(), nil})
		return
	}
	status, rpcErr := a2aStatusFilter(p.Status)
	if rpcErr != nil {
		h.a2aGatewayAudit(r, identity, req.Method, "REJECTED", "", ids.ID{}, map[string]any{"error": rpcErr.Message})
		writeA2AError(w, req.ID, rpcErr)
		return
	}
	runs, next, err := h.deps.Store.ListRuns(r.Context(), store.RunListFilter{
		Status: status, Cursor: p.Cursor, Limit: limit,
	})
	if err != nil {
		h.a2aFailRPC(w, r, identity, req, err)
		return
	}
	items := make([]map[string]any, 0, len(runs))
	for i := range runs {
		items = append(items, a2aTaskView(&runs[i]))
	}
	h.a2aGatewayAudit(r, identity, req.Method, "SUCCESS", "", ids.ID{},
		map[string]any{"count": len(items)})
	writeA2AResult(w, req.ID, map[string]any{"items": items, "next_cursor": next})
}

// a2aCancel 实现 tasks/cancel：映射 Run 取消（harness 状态机，经取消收敛队列）。
func (h *Handler) a2aCancel(w http.ResponseWriter, r *http.Request, identity tenantctx.Identity, req *a2aRequest) {
	runID, rpcErr := parseA2ATaskRef(req.Params)
	if rpcErr != nil {
		h.a2aGatewayAudit(r, identity, req.Method, "REJECTED", "", ids.ID{}, map[string]any{"error": rpcErr.Message})
		writeA2AError(w, req.ID, rpcErr)
		return
	}
	status, err := h.deps.Store.RunExists(r.Context(), runID)
	if err != nil {
		h.a2aFailRPC(w, r, identity, req, err)
		return
	}
	if runstate.IsTerminal(status) {
		rpcErr := &a2aRPCError{a2aCodeServerError, "任务已处于终态，无法取消", map[string]any{"state": a2aTaskStateOf(status)}}
		h.a2aGatewayAudit(r, identity, req.Method, "REJECTED", runID.String(), runID,
			map[string]any{"previous_status": status, "error": "已终态"})
		writeA2AError(w, req.ID, rpcErr)
		return
	}
	if h.deps.Machine == nil {
		writeA2AError(w, req.ID, &a2aRPCError{a2aCodeServerError, "执行引擎未装配，无法受理取消", nil})
		return
	}
	// 与 POST /runs/{id}/cancel 同一链路：任意非终态统一进入 CANCELLING。
	if err := h.deps.Machine.Apply(r.Context(), runID, eventCancellationRequested, noFacts()); err != nil {
		h.a2aFailRPC(w, r, identity, req, err)
		return
	}
	if err := h.appendOutbox(r, identity.TenantID, "CancellationRequested", runID,
		map[string]any{"run_id": runID.String()}); err != nil && h.deps.Logger != nil {
		h.deps.Logger.Error("A2A 取消请求已受理，但 Outbox 事件入队失败（需补投）",
			"run_id", runID.String(), "error", err)
	}
	h.a2aGatewayAudit(r, identity, req.Method, "SUCCESS", runID.String(), runID,
		map[string]any{"previous_status": status})
	h.publish(identity.TenantID.String(), "CancellationRequested", sse.Critical,
		map[string]any{"run_id": runID.String()})
	run := h.a2aRunBestEffort(r.Context(), runID)
	writeA2AResult(w, req.ID, a2aTaskView(run))
}

// ---------------------------------------------------------------------------
// 任务视图映射（Run 17 态 → A2A 4 态）
// ---------------------------------------------------------------------------

// a2aTaskStateOf 把平台 Run 状态映射为 A2A 任务状态（纯函数，表驱动测试锁定）：
//   - CREATED → submitted（已受理未执行）；
//   - 其余非终态（执行/等待/取消收敛/对账/人工复核）→ working；
//   - COMPLETED → completed；FAILED / EXPIRED → failed；CANCELLED → canceled。
func a2aTaskStateOf(runStatus string) string {
	switch runStatus {
	case runstate.Created:
		return "submitted"
	case runstate.Completed:
		return "completed"
	case runstate.Failed, runstate.Expired:
		return "failed"
	case runstate.Cancelled:
		return "canceled"
	default:
		return "working"
	}
}

// a2aTaskView 把 Run 投影为 A2A task 对象（kind/id/contextId/status/artifacts）。
func a2aTaskView(run *store.Run) map[string]any {
	status := map[string]any{"state": a2aTaskStateOf(run.Status)}
	if ts := latestTimestamp(run.CreatedAt, run.StartedAt, run.FinishedAt); !ts.IsZero() {
		status["timestamp"] = ts.UTC().Format(time.RFC3339)
	}
	task := map[string]any{
		"kind":      "task",
		"id":        run.ID,
		"contextId": run.CorrelationID,
		"status":    status,
		"artifacts": a2aArtifactsOf(run),
	}
	if run.ErrorCode != nil || run.ErrorSummary != nil {
		errObj := map[string]any{}
		if run.ErrorCode != nil {
			errObj["code"] = *run.ErrorCode
		}
		if run.ErrorSummary != nil {
			errObj["message"] = *run.ErrorSummary
		}
		task["error"] = errObj
	}
	return task
}

// a2aArtifactsOf 投影运行输出：优先 output_payload，本体审查 Run 回退
// review_conclusion；两者皆空（如尚未产出）返回空数组而非编造。
func a2aArtifactsOf(run *store.Run) []map[string]any {
	text := ""
	if len(run.OutputPayload) > 0 {
		text = string(run.OutputPayload)
	} else if len(run.ReviewConclusion) > 0 {
		text = string(run.ReviewConclusion)
	}
	if strings.TrimSpace(text) == "" {
		return []map[string]any{}
	}
	return []map[string]any{{
		"artifactId": run.ID + "-output",
		"name":       "运行输出",
		"parts":      []map[string]any{{"kind": "text", "text": text}},
	}}
}
