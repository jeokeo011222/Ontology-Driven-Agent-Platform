package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"odap/internal/a2a"
	"odap/internal/authorization"
	"odap/internal/erp"
	"odap/internal/harness"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/tools"
)

// AuthzDecider 是授权判定入口；生产实现为 *authorization.Engine。
type AuthzDecider interface {
	Decide(ctx context.Context, req authorization.Request) (authorization.Decision, error)
}

// StateMachine 是 Run 状态推进的唯一入口（生产实现为 *harness.Machine）。
// 任何组件都不得直接写 agent_runs.status（第六篇 §3.3）；Executor 只允许经此接口推进。
type StateMachine interface {
	Apply(ctx context.Context, runID ids.ID, event harness.RunEventType, facts harness.TransitionFacts) error
}

// ToolResolver 解析工具版本（生产实现可包装 tools.Registry）。
type ToolResolver interface {
	Resolve(ctx context.Context, tenantID ids.ID, toolKey string) (*tools.ToolVersion, error)
}

// ReadExecutor 是只读执行器（第十四篇 §17.2）。
//
// 契约与强制约束：
//   - 只服务 READ/COMPUTE 类工具；调用方（Executor）强制 EffectClass 必须为 READ，否则拒绝；
//   - 沙箱隔离（独立受控进程或 WASM：非 root + seccomp + cgroup）、资源限额
//     （CPU/内存/文件描述符/墙钟）与出口白名单由具体实现保证；本包只定义契约；
//   - 只读执行器不持有任何 ERP/数据库长期凭据。
type ReadExecutor interface {
	InvokeRead(ctx context.Context, tenantID ids.ID, toolVersionID ids.ID, args map[string]any) (json.RawMessage, error)
}

// WriteIntent 描述一次写意图（第十四篇 §17.2 WriteRequest 的工程化子集）。
type WriteIntent struct {
	ToolVersionID ids.ID
	ToolKey       string

	ExternalSystem string
	AdapterKey     string
	ObjectType     string
	ObjectID       string
	Action         string

	Payload        map[string]any
	IdempotencyKey string

	ApprovalID      string
	AuthzDecisionID string

	RunID  ids.ID
	StepID ids.ID
}

// WriteGateway 是唯一外部写路径（第十四篇 §17.2 / 第七篇 §7）。
//
// 契约与强制约束：
//   - 只服务 INTERNAL_WRITE/EXTERNAL_WRITE 类工具；Planner、Orchestrator、只读执行器
//     均不得直连外部系统；
//   - 凭据由服务端注入（沙箱内代码与只读执行器不接触长期凭据），按工具签发短时 token；
//   - ERP 写经第七篇 Operation Gateway，EXTERNAL_WRITE 的操作 ID、幂等键、授权与
//     审批引用在进入网关前必须已验证。
type WriteGateway interface {
	PrepareWrite(ctx context.Context, tenantID ids.ID, in WriteIntent) (*erp.Operation, error)
	DispatchWrite(ctx context.Context, tenantID ids.ID, operationID ids.ID) (*erp.OperationResult, error)
}

// 节点执行结果状态（引用 contracts/01 词表语义）。
const (
	NodeStatusSucceeded       = "SUCCEEDED"
	NodeStatusFailed          = "FAILED"
	NodeStatusWaitingApproval = "WAITING_APPROVAL"
	NodeStatusUnknown         = "UNKNOWN"
)

// NodeResult 是单节点执行结果。
type NodeResult struct {
	NodeID       string
	Status       string
	Output       json.RawMessage
	EvidenceRefs []string
	OperationID  ids.ID
	ReceiptRef   string
	ErrorCode    string
}

// StepRecord 是步骤持久化记录。
type StepRecord struct {
	StepID       ids.ID
	RunID        ids.ID
	NodeID       string
	Status       string
	Output       json.RawMessage
	EvidenceRefs []string
}

// StepStore 抽象步骤持久化（PG 实现 + 内存测试实现）。
type StepStore interface {
	GetStep(ctx context.Context, tenantID, runID ids.ID, nodeID string) (*StepRecord, error)
	PutStep(ctx context.Context, tenantID ids.ID, rec StepRecord) error
}

// MemoryStepStore 是 StepStore 的内存实现，便于测试。
type MemoryStepStore struct {
	mu    sync.Mutex
	steps map[string]StepRecord
}

// NewMemoryStepStore 构造内存步骤存储。
func NewMemoryStepStore() *MemoryStepStore {
	return &MemoryStepStore{steps: map[string]StepRecord{}}
}

func stepKey(tenantID, runID ids.ID, nodeID string) string {
	return tenantID.String() + "|" + runID.String() + "|" + nodeID
}

// GetStep 读取步骤；不存在返回 CodeNotFound。
func (s *MemoryStepStore) GetStep(ctx context.Context, tenantID, runID ids.ID, nodeID string) (*StepRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.steps[stepKey(tenantID, runID, nodeID)]
	if !ok {
		return nil, errors.E(errors.CodeNotFound, "agent: 步骤不存在")
	}
	return &rec, nil
}

// PutStep 写入步骤。
func (s *MemoryStepStore) PutStep(ctx context.Context, tenantID ids.ID, rec StepRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steps[stepKey(tenantID, rec.RunID, rec.NodeID)] = rec
	return nil
}

// Executor 是步骤执行器与工具执行入口（第十四篇 §11.2 节点执行协议 / §17.2 入口拆分）。
type Executor struct {
	Runs  StateMachine
	Store StepStore
	Authz AuthzDecider
	Tools ToolResolver
	Read  ReadExecutor
	Write WriteGateway

	// A2A 是出站 A2A 协议客户端（可选：为 nil 时拒绝远程 Agent 调用）。
	// 当 NodeToolCall 的 tool_key 能在 RemoteAgents 解析为远程 Agent 登记时，
	// 执行器会走 A2A Client 主动调用远端 Agent。
	A2A *a2a.A2AClient
	// RemoteAgents 是远程 Agent 注册表 Resolver（可选：为 nil 或 Resolve 失败时，
	// NodeToolCall 走原有本地工具路径 / 回退到 READ 执行器）。
	RemoteAgents a2a.RegistryResolver

	// SubjectID 是执行主体（Agent 版本/服务身份），用于授权请求；缺失则 fail-closed。
	SubjectID ids.ID
	// Scopes 是执行主体的数据范围（第三篇 §5.3）。
	Scopes []string
	// Environment 是授权环境（默认 PRODUCTION）。
	Environment string
}

// 编译期断言：生产实现满足接口（非测试代码即可证明契约兼容）。
var (
	_ StateMachine = (*harness.Machine)(nil)
	_ AuthzDecider = (*authorization.Engine)(nil)
)

// ExecuteNode 执行单个计划节点（第十四篇 §17.2）。
//
// 流程：
//  1. 授权检查（Authz.Decide，非 ALLOW → CodePermissionDenied；REQUIRE_APPROVAL → CodeApprovalRequired）；
//  2. READ_TOOL → Read.InvokeRead；WRITE_TOOL → 必须经 Write.PrepareWrite（绝不直接调用 Port），
//     由审批门禁决定是否 Dispatch；
//  3. 状态推进一律经 harness.Machine.Apply（禁止直接写 status）；
//  4. 工具入参先经 tools.ValidateArgs 校验（schema 来自解析到的工具版本）。
func (e *Executor) ExecuteNode(ctx context.Context, tenantID, runID ids.ID, node Node, facts harness.TransitionFacts) (NodeResult, error) {
	if tenantID.IsZero() || runID.IsZero() {
		return NodeResult{}, errors.E(errors.CodeValidationFailed, "agent: tenantID 与 runID 不能为空")
	}
	if !node.Kind.Valid() {
		return NodeResult{}, errors.Ef(errors.CodeValidationFailed, "agent: 节点 %s 的 kind=%q 未登记", node.ID, node.Kind)
	}

	// 解析工具版本（工具类节点）。
	var tv *tools.ToolVersion
	if isToolNode(node.Kind) {
		if e.Tools == nil {
			return NodeResult{}, errors.E(errors.CodeInternal, "agent: 工具节点缺少 ToolResolver")
		}
		resolved, err := e.Tools.Resolve(ctx, tenantID, node.ToolKey)
		if err != nil {
			return NodeResult{}, err
		}
		tv = resolved
	}

	// ① 授权检查（fail-closed）。
	decision, err := e.authorize(ctx, tenantID, runID, node, tv)
	if err != nil {
		return NodeResult{}, err
	}
	switch decision.Effect {
	case authorization.EffectAllow:
		// 放行。
	case authorization.EffectRequireApproval:
		return NodeResult{NodeID: node.ID, Status: NodeStatusWaitingApproval, ErrorCode: errors.CodeApprovalRequired},
			errors.Ef(errors.CodeApprovalRequired, "agent: 节点 %s 需要人工审批（决策 %s）", node.ID, decision.ReasonCode)
	default:
		return NodeResult{}, errors.Ef(errors.CodePermissionDenied,
			"agent: 节点 %s 授权拒绝（effect=%s reason=%s）", node.ID, decision.Effect, decision.ReasonCode)
	}

	switch node.Kind {
	case NodeReadTool:
		return e.executeRead(ctx, tenantID, runID, node, tv, facts)
	case NodeWriteTool:
		return e.executeWrite(ctx, tenantID, runID, node, tv, facts, decision)
	case NodeToolCall:
		// 优先尝试远程 Agent 调用（A2A），否则回退到本地工具路径（READ 执行器）。
		return e.executeToolCall(ctx, tenantID, runID, node, tv, facts)
	case NodeApprovalGate:
		if err := e.advance(ctx, runID, harness.EventApprovalRequested, facts); err != nil {
			return NodeResult{}, err
		}
		return NodeResult{NodeID: node.ID, Status: NodeStatusWaitingApproval, ErrorCode: errors.CodeApprovalRequired}, nil
	default:
		// 非工具节点（START/END/NOOP/LLM_CALL/DECISION/...）：仅推进状态。
		if err := e.advance(ctx, runID, harness.EventStepSucceeded, facts); err != nil {
			return NodeResult{}, err
		}
		return NodeResult{NodeID: node.ID, Status: NodeStatusSucceeded}, nil
	}
}

// executeRead 执行只读工具（第十四篇 §17.2 ReadOnlyExecutor）。
func (e *Executor) executeRead(ctx context.Context, tenantID, runID ids.ID, node Node, tv *tools.ToolVersion, facts harness.TransitionFacts) (NodeResult, error) {
	if e.Read == nil {
		return NodeResult{}, errors.E(errors.CodeInternal, "agent: 未配置只读执行器")
	}
	// 强制只读：写工具不得经只读路径执行（否则即"绕过写网关"）。
	if tv.EffectClass != EffectRead {
		return NodeResult{}, errors.Ef(errors.CodePermissionDenied,
			"agent: 只读执行器拒绝非 READ 工具（tool=%s effect_class=%s）", tv.ToolKey, tv.EffectClass)
	}
	args, err := parseNodeArgs(node)
	if err != nil {
		return NodeResult{}, err
	}
	if err := tools.ValidateArgs(tv.InputSchema, args); err != nil {
		return NodeResult{}, err
	}
	if err := e.advance(ctx, runID, harness.EventStepStarted, facts); err != nil {
		return NodeResult{}, err
	}
	out, err := e.Read.InvokeRead(ctx, tenantID, tv.ID, args)
	if err != nil {
		_ = e.advance(ctx, runID, harness.EventStepFailed, facts)
		return NodeResult{NodeID: node.ID, Status: NodeStatusFailed}, err
	}
	if err := e.advance(ctx, runID, harness.EventStepSucceeded, facts); err != nil {
		return NodeResult{}, err
	}
	res := NodeResult{
		NodeID:       node.ID,
		Status:       NodeStatusSucceeded,
		Output:       out,
		EvidenceRefs: node.EvidenceRefs,
	}
	_ = e.saveStep(ctx, tenantID, runID, res)
	return res, nil
}

// executeWrite 执行写工具：必须经写网关（第十四篇 §17.2 WriteGateway / 第七篇 §7）。
func (e *Executor) executeWrite(ctx context.Context, tenantID, runID ids.ID, node Node, tv *tools.ToolVersion, facts harness.TransitionFacts, decision authorization.Decision) (NodeResult, error) {
	if e.Write == nil {
		// 未配置写网关时拒绝：绝不允许直连 Port 的替代路径。
		return NodeResult{}, errors.E(errors.CodeInternal,
			"agent: 写节点必须经写网关执行，当前未配置写网关（拒绝直连外部系统）")
	}
	if tv.EffectClass != EffectExternalWrite {
		return NodeResult{}, errors.Ef(errors.CodeValidationFailed,
			"agent: 写节点 %s 的工具 effect_class=%s，必须为 EXTERNAL_WRITE", node.ID, tv.EffectClass)
	}
	args, err := parseNodeArgs(node)
	if err != nil {
		return NodeResult{}, err
	}
	if err := tools.ValidateArgs(tv.InputSchema, args); err != nil {
		return NodeResult{}, err
	}

	in := WriteIntent{
		ToolVersionID:   tv.ID,
		ToolKey:         tv.ToolKey,
		ExternalSystem:  stringAttr(args, "external_system"),
		AdapterKey:      stringAttr(args, "adapter_key"),
		ObjectType:      stringAttr(args, "object_type"),
		ObjectID:        stringAttr(args, "object_id"),
		Action:          stringAttr(args, "action"),
		Payload:         args,
		IdempotencyKey:  stringAttr(args, "idempotency_key"),
		ApprovalID:      stringAttr(args, "approval_id"),
		AuthzDecisionID: decision.FactsDigest,
		RunID:           runID,
	}

	if err := e.advance(ctx, runID, harness.EventStepStarted, facts); err != nil {
		return NodeResult{}, err
	}
	op, err := e.Write.PrepareWrite(ctx, tenantID, in)
	if err != nil {
		_ = e.advance(ctx, runID, harness.EventStepFailed, facts)
		return NodeResult{NodeID: node.ID, Status: NodeStatusFailed}, err
	}
	// 未获审批：暂停等待审批，绝不提前派发（第七篇 §7.1）。
	if op.Status == erp.LedgerWaitingApproval {
		if err := e.advance(ctx, runID, harness.EventApprovalRequested, facts); err != nil {
			return NodeResult{}, err
		}
		res := NodeResult{NodeID: node.ID, Status: NodeStatusWaitingApproval, OperationID: op.ID, ErrorCode: errors.CodeApprovalRequired}
		_ = e.saveStep(ctx, tenantID, runID, res)
		return res, nil
	}

	result, err := e.Write.DispatchWrite(ctx, tenantID, op.ID)
	if err != nil {
		_ = e.advance(ctx, runID, harness.EventStepFailed, facts)
		return NodeResult{NodeID: node.ID, Status: NodeStatusFailed, OperationID: op.ID}, err
	}

	res := NodeResult{NodeID: node.ID, OperationID: op.ID}
	switch result.Status {
	case erp.StatusSucceeded:
		res.Status = NodeStatusSucceeded
		res.ReceiptRef = result.ReceiptID
		if err := e.advance(ctx, runID, harness.EventStepSucceeded, facts); err != nil {
			return NodeResult{}, err
		}
	case erp.StatusUnknown, erp.StatusReconciling, erp.StatusAccepted, erp.StatusInProgress:
		// 结果未确认是一等状态，绝不静默当成功（第七篇 §3.4）。
		res.Status = NodeStatusUnknown
		if err := e.advance(ctx, runID, harness.EventExternalOperationUnknown, facts); err != nil {
			return NodeResult{}, err
		}
	default:
		res.Status = NodeStatusFailed
		if result.Error != nil {
			res.ErrorCode = result.Error.Code
		}
		if err := e.advance(ctx, runID, harness.EventStepFailed, facts); err != nil {
			return NodeResult{}, err
		}
	}
	_ = e.saveStep(ctx, tenantID, runID, res)
	return res, nil
}

// authorize 构造授权请求并判定；未配置判定器或缺少主体身份一律 fail-closed。
func (e *Executor) authorize(ctx context.Context, tenantID, runID ids.ID, node Node, tv *tools.ToolVersion) (authorization.Decision, error) {
	if e.Authz == nil {
		return authorization.Decision{}, errors.E(errors.CodePermissionDenied, "agent: 未配置授权判定器，拒绝执行")
	}
	if e.SubjectID.IsZero() {
		return authorization.Decision{}, errors.E(errors.CodePermissionDenied, "agent: 缺少执行主体身份，拒绝执行")
	}
	attrs := map[string]any{"run_id": runID.String()}
	resourceID := node.ToolKey
	if tv != nil {
		attrs["effect_class"] = tv.EffectClass
		if resourceID == "" {
			resourceID = tv.ToolKey
		}
	}
	req := authorization.Request{
		SubjectID:    e.SubjectID,
		SubjectType:  "AGENT",
		Action:       string(node.Kind),
		ResourceType: "TOOL",
		ResourceID:   resourceID,
		Attributes:   attrs,
		Scopes:       e.Scopes,
		Environment:  e.environment(),
	}
	decision, err := e.Authz.Decide(ctx, req)
	if err != nil {
		return authorization.Decision{}, errors.Wrap(err, errors.CodePermissionDenied, "agent: 授权判定失败")
	}
	return decision, nil
}

// advance 经状态机推进 Run 状态（禁止直接写 status）。
func (e *Executor) advance(ctx context.Context, runID ids.ID, event harness.RunEventType, facts harness.TransitionFacts) error {
	if e.Runs == nil {
		return errors.E(errors.CodeInternal, "agent: 未配置状态机，禁止直接写 status")
	}
	return e.Runs.Apply(ctx, runID, event, facts)
}

func (e *Executor) saveStep(ctx context.Context, tenantID, runID ids.ID, res NodeResult) error {
	if e.Store == nil {
		return nil
	}
	return e.Store.PutStep(ctx, tenantID, StepRecord{
		StepID:       ids.New(),
		RunID:        runID,
		NodeID:       res.NodeID,
		Status:       res.Status,
		Output:       res.Output,
		EvidenceRefs: res.EvidenceRefs,
	})
}

func (e *Executor) environment() string {
	if e.Environment != "" {
		return e.Environment
	}
	return authorization.EnvProduction
}

// isToolNode 报告节点是否需要解析工具版本。
func isToolNode(kind NodeKind) bool {
	switch kind {
	case NodeToolCall, NodeReadTool, NodeWriteTool, NodeSemanticQuery:
		return true
	default:
		return false
	}
}

// parseNodeArgs 解析节点参数为对象。
func parseNodeArgs(node Node) (map[string]any, error) {
	if len(node.Params) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(node.Params, &m); err != nil {
		return nil, errors.Wrap(err, errors.CodeValidationFailed, "agent: 节点参数非合法 JSON 对象")
	}
	return m, nil
}

// stringAttr 读取字符串属性。
func stringAttr(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// ---------------------------------------------------------------------------
// NodeToolCall 执行：A2A 远程 Agent 调用 ↔ 本地 READ 回退
// ---------------------------------------------------------------------------

// executeToolCall 执行 NodeToolCall 节点。
//
// 路由逻辑：
//  1. A2A 客户端与 RemoteAgents Resolver 均已装配，且 tool_key 能在 Resolver
//     解析出 ACTIVE 状态的远程 Agent → 走 A2A Client 主动调用（SendMessage 同步等待）；
//  2. 否则 → 回退到本地 READ 执行器，要求工具 EffectClass=READ（与 executeRead 同形）。
//
// 不自动重试远端调用（fail-closed 原则）：超时 / 网络错误 / 远端业务错误一律如实传播。
func (e *Executor) executeToolCall(ctx context.Context, tenantID, runID ids.ID, node Node, tv *tools.ToolVersion, facts harness.TransitionFacts) (NodeResult, error) {
	if e.A2A != nil && e.RemoteAgents != nil && strings.TrimSpace(node.ToolKey) != "" {
		ref, err := e.RemoteAgents.Resolve(ctx, node.ToolKey)
		if err == nil && ref != nil {
			// 解析到远程 Agent → 走 A2A Client。
			return e.executeRemoteAgent(ctx, tenantID, runID, node, ref, facts)
		}
		// Resolve 失败不视为硬错误——可能 tool_key 本来就是本地工具，继续回退到本地路径。
	}

	// 本地路径：复用 executeRead 的契约（强制 READ 效果类 + schema 校验）。
	return e.executeRead(ctx, tenantID, runID, node, tv, facts)
}

// executeRemoteAgent 通过 A2A Client 主动调用远端 Agent。
//
// 入参约定：node.Params 中 text / objective 字段作为 A2A message.text 发送；
// 其余字段按 SendParams.params 传入 metadata。远端 task 进入终态后返回
// artifacts 合成的 JSON 输出，超时则返回 accepted 状态 + task_id（供调用方轮询）。
func (e *Executor) executeRemoteAgent(ctx context.Context, tenantID, runID ids.ID, node Node, ref *a2a.RemoteAgentRef, facts harness.TransitionFacts) (NodeResult, error) {
	args, err := parseNodeArgs(node)
	if err != nil {
		return NodeResult{}, err
	}

	text := firstString(args, "text", "objective", "message")
	if text == "" {
		return NodeResult{}, errors.E(errors.CodeValidationFailed,
			"agent: 远程 Agent 调用节点缺少 text / objective / message 参数")
	}

	metadata := &a2a.SendMetadata{}
	if v := stringAttr(args, "agent_key"); v != "" {
		metadata.AgentKey = v
	}
	if v := stringAttr(args, "ontology_version_id"); v != "" {
		metadata.OntologyVersionID = v
	}

	if err := e.advance(ctx, runID, harness.EventStepStarted, facts); err != nil {
		return NodeResult{}, err
	}

	task, err := e.A2A.SendMessage(ctx,
		a2a.GatewayURL(ref.BaseURL),
		ref.AuthHeaderName, ref.AuthHeaderRef,
		a2a.Message{Role: "user", Parts: []a2a.MessagePart{{Text: text}}},
		metadata)
	if err != nil {
		_ = e.advance(ctx, runID, harness.EventStepFailed, facts)
		return NodeResult{NodeID: node.ID, Status: NodeStatusFailed}, err
	}

	// 合成 JSON 输出（task 对象整体序列化，便于上游统一消费）。
	out, _ := json.Marshal(task)
	res := NodeResult{
		NodeID:       node.ID,
		Status:       NodeStatusSucceeded,
		Output:       out,
		EvidenceRefs: append([]string{}, node.EvidenceRefs...),
	}
	if !a2a.IsTaskTerminal(task) {
		// 远端未在同步等待窗口内完成 → 状态标注 UNKNOWN，由上游决定是否轮询。
		res.Status = NodeStatusUnknown
		res.ErrorCode = "A2A_TASK_PENDING"
	}
	if err := e.advance(ctx, runID, harness.EventStepSucceeded, facts); err != nil {
		return NodeResult{}, err
	}
	_ = e.saveStep(ctx, tenantID, runID, res)
	return res, nil
}

// firstString 按 key 顺序取 args 中第一个非空字符串。
func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
