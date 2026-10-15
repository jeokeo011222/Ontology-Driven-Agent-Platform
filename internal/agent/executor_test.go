package agent

import (
	"context"
	"encoding/json"
	"testing"

	"odap/internal/authorization"
	"odap/internal/erp"
	"odap/internal/harness"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/tools"
)

// ── 测试替身 ────────────────────────────────────────────────────────────────

type fakeRead struct {
	calls    int
	lastArgs map[string]any
	lastTV   ids.ID
	out      json.RawMessage
	err      error
}

func (f *fakeRead) InvokeRead(ctx context.Context, tenantID, toolVersionID ids.ID, args map[string]any) (json.RawMessage, error) {
	f.calls++
	f.lastArgs = args
	f.lastTV = toolVersionID
	if f.err != nil {
		return nil, f.err
	}
	return f.out, nil
}

type fakeWrite struct {
	prepares   int
	dispatches int
	op         *erp.Operation
	result     *erp.OperationResult
	prepErr    error
	dispErr    error
}

func (f *fakeWrite) PrepareWrite(ctx context.Context, tenantID ids.ID, in WriteIntent) (*erp.Operation, error) {
	f.prepares++
	if f.prepErr != nil {
		return nil, f.prepErr
	}
	return f.op, nil
}

func (f *fakeWrite) DispatchWrite(ctx context.Context, tenantID, operationID ids.ID) (*erp.OperationResult, error) {
	f.dispatches++
	if f.dispErr != nil {
		return nil, f.dispErr
	}
	return f.result, nil
}

type fakeAuthz struct {
	decision authorization.Decision
	err      error
	lastReq  authorization.Request
}

func (f *fakeAuthz) Decide(ctx context.Context, req authorization.Request) (authorization.Decision, error) {
	f.lastReq = req
	if f.err != nil {
		return authorization.Decision{}, f.err
	}
	return f.decision, nil
}

type fakeSM struct {
	events []harness.RunEventType
	err    error
}

func (f *fakeSM) Apply(ctx context.Context, runID ids.ID, event harness.RunEventType, facts harness.TransitionFacts) error {
	f.events = append(f.events, event)
	return f.err
}

type fakeTools struct {
	tv  *tools.ToolVersion
	err error
}

func (f *fakeTools) Resolve(ctx context.Context, tenantID ids.ID, toolKey string) (*tools.ToolVersion, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.tv, nil
}

func hasEvent(events []harness.RunEventType, want harness.RunEventType) bool {
	for _, e := range events {
		if e == want {
			return true
		}
	}
	return false
}

func allowDecision() authorization.Decision {
	return authorization.Decision{Effect: authorization.EffectAllow, ReasonCode: "TEST_ALLOW"}
}

func readToolVersion(effect string) *tools.ToolVersion {
	return &tools.ToolVersion{
		ID:           ids.New(),
		ToolKey:      "semantic.query.expenses",
		EffectClass:  effect,
		InputSchema:  json.RawMessage(`{"type":"object","required":["q"],"properties":{"q":{"type":"string"}}}`),
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	}
}

func writeToolVersion() *tools.ToolVersion {
	return &tools.ToolVersion{
		ID:          ids.New(),
		ToolKey:     "erp.expense.submit",
		EffectClass: EffectExternalWrite,
		InputSchema: json.RawMessage(`{"type":"object","required":["external_system","object_type","action"],"properties":{
			"external_system":{"type":"string"},"object_type":{"type":"string"},"action":{"type":"string"},
			"object_id":{"type":"string"},"adapter_key":{"type":"string"}}}`),
	}
}

func baseExecutor(sm *fakeSM, authz *fakeAuthz, tv *tools.ToolVersion) *Executor {
	return &Executor{
		Runs:      sm,
		Store:     NewMemoryStepStore(),
		Authz:     authz,
		Tools:     &fakeTools{tv: tv},
		SubjectID: ids.New(),
		Scopes:    []string{authorization.ScopeTenant},
	}
}

// ── 测试 ────────────────────────────────────────────────────────────────────

// TestExecuteNodeReadTool 验证只读工具经只读执行器调用，且状态推进经状态机。
func TestExecuteNodeReadTool(t *testing.T) {
	sm := &fakeSM{}
	authz := &fakeAuthz{decision: allowDecision()}
	tv := readToolVersion(EffectRead)
	e := baseExecutor(sm, authz, tv)
	read := &fakeRead{out: json.RawMessage(`{"ok":true}`)}
	e.Read = read

	tenant, runID := ids.New(), ids.New()
	node := Node{ID: "q", Kind: NodeReadTool, ToolKey: tv.ToolKey, Params: json.RawMessage(`{"q":"hello"}`), EvidenceRefs: []string{"ev-1"}}
	res, err := e.ExecuteNode(context.Background(), tenant, runID, node, harness.TransitionFacts{LeaseValid: true})
	if err != nil {
		t.Fatalf("ExecuteNode: %v", err)
	}
	if res.Status != NodeStatusSucceeded {
		t.Fatalf("status=%s", res.Status)
	}
	if read.calls != 1 || read.lastTV != tv.ID {
		t.Fatalf("只读执行器调用次数=%d tv=%s", read.calls, read.lastTV)
	}
	if read.lastArgs["q"] != "hello" {
		t.Fatalf("入参未透传：%v", read.lastArgs)
	}
	if !hasEvent(sm.events, harness.EventStepStarted) || !hasEvent(sm.events, harness.EventStepSucceeded) {
		t.Fatalf("状态推进事件缺失：%v", sm.events)
	}
}

// TestExecuteNodeWriteViaEnabledReadPath 写工具经只读执行器被拒（防止绕过写网关）。
func TestExecuteNodeWriteToolRejectedByReadPath(t *testing.T) {
	sm := &fakeSM{}
	authz := &fakeAuthz{decision: allowDecision()}
	tv := writeToolVersion() // effect=EXTERNAL_WRITE
	e := baseExecutor(sm, authz, tv)
	read := &fakeRead{}
	e.Read = read

	node := Node{ID: "w", Kind: NodeReadTool, ToolKey: tv.ToolKey, Params: json.RawMessage(`{"q":"x"}`)}
	_, err := e.ExecuteNode(context.Background(), ids.New(), ids.New(), node, harness.TransitionFacts{})
	if errors.As(err).Code != errors.CodePermissionDenied {
		t.Fatalf("应拒绝非 READ 工具经只读路径，got %v", err)
	}
	if read.calls != 0 {
		t.Fatalf("只读执行器不应被调用")
	}
}

// TestExecuteNodeWriteToolWithoutGateway 写工具未经写网关直接调用被拒。
func TestExecuteNodeWriteToolWithoutGateway(t *testing.T) {
	sm := &fakeSM{}
	authz := &fakeAuthz{decision: allowDecision()}
	tv := writeToolVersion()
	e := baseExecutor(sm, authz, tv) // Write 为空
	read := &fakeRead{}
	e.Read = read

	node := Node{ID: "w", Kind: NodeWriteTool, ToolKey: tv.ToolKey, EffectClass: EffectExternalWrite,
		Params: json.RawMessage(`{"external_system":"mock","object_type":"ExpenseClaim","action":"submit"}`)}
	_, err := e.ExecuteNode(context.Background(), ids.New(), ids.New(), node, harness.TransitionFacts{})
	if err == nil {
		t.Fatalf("未配置写网关时应拒绝")
	}
	if errors.As(err).Code != errors.CodeInternal {
		t.Fatalf("错误码=%s, want INTERNAL", errors.As(err).Code)
	}
	if read.calls != 0 {
		t.Fatalf("写节点不得走只读执行器")
	}
}

// TestExecuteNodeWriteToolViaGateway 写节点经写网关 Prepare + Dispatch。
func TestExecuteNodeWriteToolViaGateway(t *testing.T) {
	sm := &fakeSM{}
	authz := &fakeAuthz{decision: allowDecision()}
	tv := writeToolVersion()
	e := baseExecutor(sm, authz, tv)
	opID := ids.New()
	w := &fakeWrite{
		op:     &erp.Operation{ID: opID, Status: erp.LedgerReady},
		result: &erp.OperationResult{Status: erp.StatusSucceeded, ReceiptID: "receipt-1"},
	}
	e.Write = w

	node := Node{ID: "w", Kind: NodeWriteTool, ToolKey: tv.ToolKey, EffectClass: EffectExternalWrite,
		Params: json.RawMessage(`{"external_system":"mock","object_type":"ExpenseClaim","action":"submit","object_id":"e1"}`)}
	res, err := e.ExecuteNode(context.Background(), ids.New(), ids.New(), node, harness.TransitionFacts{LeaseValid: true})
	if err != nil {
		t.Fatalf("ExecuteNode: %v", err)
	}
	if w.prepares != 1 || w.dispatches != 1 {
		t.Fatalf("写网关 Prepare=%d Dispatch=%d, want 1/1", w.prepares, w.dispatches)
	}
	if res.Status != NodeStatusSucceeded || res.OperationID != opID || res.ReceiptRef != "receipt-1" {
		t.Fatalf("res=%+v", res)
	}
	if !hasEvent(sm.events, harness.EventStepSucceeded) {
		t.Fatalf("状态推进事件缺失：%v", sm.events)
	}
}

// TestExecuteNodeWriteToolWaitingApproval 未获审批不派发（第七篇 §7.1）。
func TestExecuteNodeWriteToolWaitingApproval(t *testing.T) {
	sm := &fakeSM{}
	authz := &fakeAuthz{decision: allowDecision()}
	tv := writeToolVersion()
	e := baseExecutor(sm, authz, tv)
	w := &fakeWrite{op: &erp.Operation{ID: ids.New(), Status: erp.LedgerWaitingApproval}}
	e.Write = w

	node := Node{ID: "w", Kind: NodeWriteTool, ToolKey: tv.ToolKey, EffectClass: EffectExternalWrite,
		Params: json.RawMessage(`{"external_system":"mock","object_type":"ExpenseClaim","action":"submit"}`)}
	res, err := e.ExecuteNode(context.Background(), ids.New(), ids.New(), node, harness.TransitionFacts{})
	if err != nil {
		t.Fatalf("ExecuteNode: %v", err)
	}
	if w.prepares != 1 || w.dispatches != 0 {
		t.Fatalf("待审批不应派发：Prepare=%d Dispatch=%d", w.prepares, w.dispatches)
	}
	if res.Status != NodeStatusWaitingApproval {
		t.Fatalf("status=%s", res.Status)
	}
	if !hasEvent(sm.events, harness.EventApprovalRequested) {
		t.Fatalf("应推进至审批事件：%v", sm.events)
	}
}

// TestExecuteNodeAuthorization 授权拒绝 / 需审批的处置。
func TestExecuteNodeAuthorization(t *testing.T) {
	tv := readToolVersion(EffectRead)
	node := Node{ID: "q", Kind: NodeReadTool, ToolKey: tv.ToolKey, Params: json.RawMessage(`{"q":"x"}`)}

	t.Run("授权拒绝返回 PERMISSION_DENIED", func(t *testing.T) {
		sm := &fakeSM{}
		authz := &fakeAuthz{decision: authorization.Decision{Effect: authorization.EffectDeny, ReasonCode: "POLICY"}}
		e := baseExecutor(sm, authz, tv)
		read := &fakeRead{}
		e.Read = read
		_, err := e.ExecuteNode(context.Background(), ids.New(), ids.New(), node, harness.TransitionFacts{})
		if errors.As(err).Code != errors.CodePermissionDenied {
			t.Fatalf("应返回 PERMISSION_DENIED，got %v", err)
		}
		if read.calls != 0 {
			t.Fatalf("授权拒绝不得执行工具")
		}
	})

	t.Run("需审批返回 APPROVAL_REQUIRED", func(t *testing.T) {
		sm := &fakeSM{}
		authz := &fakeAuthz{decision: authorization.Decision{Effect: authorization.EffectRequireApproval, ReasonCode: "SELF_APPROVAL"}}
		e := baseExecutor(sm, authz, tv)
		e.Read = &fakeRead{}
		res, err := e.ExecuteNode(context.Background(), ids.New(), ids.New(), node, harness.TransitionFacts{})
		if errors.As(err).Code != errors.CodeApprovalRequired {
			t.Fatalf("应返回 APPROVAL_REQUIRED，got %v", err)
		}
		if res.Status != NodeStatusWaitingApproval || res.ErrorCode != errors.CodeApprovalRequired {
			t.Fatalf("res=%+v", res)
		}
	})

	t.Run("缺少执行主体 fail-closed", func(t *testing.T) {
		sm := &fakeSM{}
		e := baseExecutor(sm, &fakeAuthz{decision: allowDecision()}, tv)
		e.SubjectID = ids.ID{}
		e.Read = &fakeRead{}
		if _, err := e.ExecuteNode(context.Background(), ids.New(), ids.New(), node, harness.TransitionFacts{}); errors.As(err).Code != errors.CodePermissionDenied {
			t.Fatalf("应 fail-closed 返回 PERMISSION_DENIED，got %v", err)
		}
	})
}

// TestExecuteNodeArgsValidation 入参校验失败时不得调用执行器。
func TestExecuteNodeArgsValidation(t *testing.T) {
	sm := &fakeSM{}
	authz := &fakeAuthz{decision: allowDecision()}
	tv := readToolVersion(EffectRead)
	e := baseExecutor(sm, authz, tv)
	read := &fakeRead{}
	e.Read = read

	node := Node{ID: "q", Kind: NodeReadTool, ToolKey: tv.ToolKey, Params: json.RawMessage(`{}`)} // 缺 required q
	_, err := e.ExecuteNode(context.Background(), ids.New(), ids.New(), node, harness.TransitionFacts{})
	if errors.As(err).Code != errors.CodeValidationFailed {
		t.Fatalf("应返回 VALIDATION_FAILED，got %v", err)
	}
	if read.calls != 0 {
		t.Fatalf("入参非法不得调用执行器")
	}
}

// TestExecuteNodeStateFailedEvent 只读执行失败时推进失败事件。
func TestExecuteNodeStateFailedEvent(t *testing.T) {
	sm := &fakeSM{}
	authz := &fakeAuthz{decision: allowDecision()}
	tv := readToolVersion(EffectRead)
	e := baseExecutor(sm, authz, tv)
	e.Read = &fakeRead{err: errors.E(errors.CodeUnavailable, "boom")}

	node := Node{ID: "q", Kind: NodeReadTool, ToolKey: tv.ToolKey, Params: json.RawMessage(`{"q":"x"}`)}
	if _, err := e.ExecuteNode(context.Background(), ids.New(), ids.New(), node, harness.TransitionFacts{}); err == nil {
		t.Fatalf("应返回错误")
	}
	if !hasEvent(sm.events, harness.EventStepFailed) {
		t.Fatalf("应推进失败事件：%v", sm.events)
	}
}
