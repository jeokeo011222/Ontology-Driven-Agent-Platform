package erp

import (
	"context"
	"sync"
	"testing"
	"time"

	"odap/internal/platform/clock"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

var (
	testTenant = ids.MustParse("11111111-1111-1111-1111-111111111111")
	testRun    = ids.MustParse("22222222-2222-2222-2222-222222222222")
	testStep   = ids.MustParse("33333333-3333-3333-3333-333333333333")
)

// ── 内存 OperationStore（单测注入）──────────────────────────────────────────

type memStore struct {
	mu       sync.Mutex
	ops      map[string]*Operation
	byKey    map[string]string
	receipts []*Receipt
}

func newMemStore() *memStore {
	return &memStore{ops: map[string]*Operation{}, byKey: map[string]string{}}
}

func memKey(tenantID ids.ID, externalSystem, key string) string {
	return tenantID.String() + "|" + externalSystem + "|" + key
}

func (m *memStore) Insert(_ context.Context, tenantID ids.ID, op *Operation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := memKey(tenantID, op.ExternalSystem, op.IdempotencyKey)
	if _, ok := m.byKey[k]; ok {
		return ErrIdempotencyExists
	}
	if _, ok := m.ops[op.ID.String()]; ok {
		return errors.E(errors.CodeConflict, "erp: 操作主键冲突")
	}
	cp := copyOp(op)
	cp.TenantID = tenantID
	m.ops[op.ID.String()] = cp
	m.byKey[k] = op.ID.String()
	return nil
}

func (m *memStore) GetByID(_ context.Context, tenantID ids.ID, operationID ids.ID) (*Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	op, ok := m.ops[operationID.String()]
	if !ok || op.TenantID != tenantID {
		return nil, errors.Ef(errors.CodeNotFound, "erp: 操作 %s 不存在", operationID)
	}
	return copyOp(op), nil
}

func (m *memStore) ListByRun(_ context.Context, tenantID ids.ID, runID ids.ID) ([]*Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Operation
	for _, op := range m.ops {
		if op.TenantID == tenantID && op.RunID == runID {
			out = append(out, copyOp(op))
		}
	}
	return out, nil
}

func (m *memStore) GetByIdempotencyKey(_ context.Context, tenantID ids.ID, externalSystem, key string) (*Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.byKey[memKey(tenantID, externalSystem, key)]
	if !ok {
		return nil, errors.Ef(errors.CodeNotFound, "erp: 幂等键 %s 无对应操作", key)
	}
	op, ok := m.ops[id]
	if !ok {
		return nil, errors.Ef(errors.CodeNotFound, "erp: 幂等键 %s 无对应操作", key)
	}
	return copyOp(op), nil
}

func (m *memStore) UpdateStatus(_ context.Context, tenantID ids.ID, operationID ids.ID, tr StatusTransition) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	op, ok := m.ops[operationID.String()]
	if !ok || op.TenantID != tenantID {
		return false, nil
	}
	if len(tr.From) > 0 && !contains(tr.From, op.Status) {
		return false, nil
	}
	op.Status = tr.To
	if tr.SetDispatched {
		at := tr.At
		op.DispatchedAt = &at
	}
	if tr.SetFinished {
		at := tr.At
		op.FinishedAt = &at
	}
	return true, nil
}

func (m *memStore) AppendReceipt(_ context.Context, tenantID ids.ID, r *Receipt) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *r
	cp.TenantID = tenantID
	m.receipts = append(m.receipts, &cp)
	return nil
}

func (m *memStore) CountReceipts(_ context.Context, tenantID ids.ID, operationID ids.ID, receiptType string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.receipts {
		if r.TenantID == tenantID && r.OperationID == operationID && r.ReceiptType == receiptType {
			n++
		}
	}
	return n, nil
}

func (m *memStore) LatestReceipt(_ context.Context, tenantID ids.ID, operationID ids.ID, receiptType string) (*Receipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest *Receipt
	for _, r := range m.receipts {
		if r.TenantID == tenantID && r.OperationID == operationID && r.ReceiptType == receiptType {
			latest = r
		}
	}
	if latest == nil {
		return nil, errors.Ef(errors.CodeNotFound, "erp: 操作 %s 无 %s 回执", operationID, receiptType)
	}
	cp := *latest
	return &cp, nil
}

func (m *memStore) lenOps() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.ops)
}

func copyOp(o *Operation) *Operation {
	cp := *o
	if o.PayloadSnapshot != nil {
		cp.PayloadSnapshot = clonePayload(o.PayloadSnapshot)
	}
	return &cp
}

// ── fake Port / Resolver / Authz / Approvals ───────────────────────────────

type fakePort struct {
	mu           sync.Mutex
	caps         Capabilities
	execCalls    int
	execRequests []ExecuteRequest
	execResult   OperationResult
	execErr      error
	opCalls      int
	opResult     OperationResult
	opErr        error
}

func (p *fakePort) Capabilities(_ context.Context, _ ConnectionID) (Capabilities, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.caps, nil
}

func (p *fakePort) GetObject(context.Context, GetObjectRequest) (ObjectResult, error) {
	return ObjectResult{}, nil
}

func (p *fakePort) QueryObjects(context.Context, QueryObjectsRequest) (QueryResult, error) {
	return QueryResult{}, nil
}

func (p *fakePort) Execute(_ context.Context, req ExecuteRequest) (OperationResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.execCalls++
	p.execRequests = append(p.execRequests, req)
	return p.execResult, p.execErr
}

func (p *fakePort) GetOperation(context.Context, GetOperationRequest) (OperationResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.opCalls++
	return p.opResult, p.opErr
}

func (p *fakePort) Health(context.Context, ConnectionID) (HealthResult, error) {
	return HealthResult{Healthy: true, Status: HealthHealthy}, nil
}

func (p *fakePort) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.execCalls
}

type fakeResolver struct {
	mu       sync.Mutex
	port     Port
	resolve  int
	resolveE error
}

func (r *fakeResolver) Resolve(context.Context, ids.ID, string) (Port, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resolve++
	return r.port, r.resolveE
}

type fakeAuthz struct {
	decision AuthzDecision
	err      error
}

func (a *fakeAuthz) Check(context.Context, AuthzRequest) (AuthzDecision, error) {
	return a.decision, a.err
}

type fakeApprovals struct {
	approved map[string]bool
	err      error
}

func (a *fakeApprovals) IsApproved(_ context.Context, _ ids.ID, approvalID string) (bool, error) {
	if a.err != nil {
		return false, a.err
	}
	return a.approved[approvalID], nil
}

// ── 测试脚手架 ──────────────────────────────────────────────────────────────

func newHarness() (*OperationGateway, *memStore, *fakePort, *fakeAuthz, *fakeApprovals) {
	store := newMemStore()
	port := &fakePort{caps: Capabilities{ConnectionID: "erp-mock", IdempotencyLevel: IdempotencyLevelL1}}
	authz := &fakeAuthz{decision: AuthzDecision{Effect: AuthzEffectAllow, DecisionID: "dec-1"}}
	approvals := &fakeApprovals{approved: map[string]bool{}}
	gw := &OperationGateway{
		Clock:     &clock.FakeClock{Current: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)},
		Ports:     &fakeResolver{port: port},
		Authz:     authz,
		Approvals: approvals,
		Store:     store,
	}
	return gw, store, port, authz, approvals
}

func baseInput(payload map[string]any) PrepareInput {
	return PrepareInput{
		TenantID:        testTenant,
		RunID:           testRun,
		StepID:          testStep,
		ExternalSystem:  "erp-mock",
		AdapterKey:      "com.odap.adapter.mock-erp",
		ObjectType:      "ExpenseClaim",
		ObjectID:        "claim-001",
		Action:          "expense.submit",
		OperationType:   "expense.submit",
		Payload:         payload,
		ExpectedVersion: "1",
	}
}

func codeOf(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatalf("期望得到错误，实际为 nil")
	}
	return errors.As(err).Code
}

// ── 用例 ────────────────────────────────────────────────────────────────────

// ① 同键不同摘要 → IDEMPOTENCY_CONFLICT。
//
// 注意：§8.1 把 payload_digest 绑定进幂等键，因此"同键不同摘要"不会由
// "改参数重试"产生（那会派生出新键）；它只可能来自键被复用、跨版本派生
// 或账本被篡改等异常路径。这里直接植入一条同键但摘要不同的记录，验证
// 账本必须 fail-closed：拒绝把同一键绑定到不同的操作内容（第七篇 §8.2）。
func TestPrepareSameKeyDifferentDigestConflicts(t *testing.T) {
	ctx := context.Background()
	gw, store, _, _, _ := newHarness()
	payload := map[string]any{"amount": 1280.50}

	digest, err := PayloadDigest(payload)
	if err != nil {
		t.Fatalf("计算摘要失败：%v", err)
	}
	key := IdempotencyKey(testTenant, IntentParts{
		RunID:          testRun,
		StepID:         testStep,
		OperationType:  "expense.submit",
		TargetObjectID: "claim-001",
		PayloadDigest:  digest,
	})
	if err := store.Insert(ctx, testTenant, &Operation{
		ID:             ids.New(),
		TenantID:       testTenant,
		ExternalSystem: "erp-mock",
		AdapterKey:     "com.odap.adapter.mock-erp",
		ObjectType:     "ExpenseClaim",
		ObjectID:       "claim-001",
		Action:         "expense.submit",
		IdempotencyKey: key,
		PayloadDigest:  "tampered-digest",
		Status:         LedgerReady,
		CreatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("植入既有记录失败：%v", err)
	}

	_, err = gw.Prepare(ctx, baseInput(payload))
	if code := codeOf(t, err); code != errors.CodeIdempotencyConflict {
		t.Fatalf("期望 IDEMPOTENCY_CONFLICT，得到 %s", code)
	}
	if store.lenOps() != 1 {
		t.Fatalf("冲突请求不得新建操作行，当前 %d 行", store.lenOps())
	}
}

// ② 同键同摘要 → 返回已有操作，账本不新增行。
func TestPrepareSameKeySameDigestReturnsExisting(t *testing.T) {
	ctx := context.Background()
	gw, store, _, _, _ := newHarness()
	payload := map[string]any{"amount": 1280.50, "currency": "CNY"}

	first, err := gw.Prepare(ctx, baseInput(payload))
	if err != nil {
		t.Fatalf("首次 Prepare 失败：%v", err)
	}
	second, err := gw.Prepare(ctx, baseInput(payload))
	if err != nil {
		t.Fatalf("重复 Prepare 失败：%v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("同键同摘要应返回同一操作：%s vs %s", first.ID, second.ID)
	}
	if store.lenOps() != 1 {
		t.Fatalf("幂等重放不得新增操作行，当前 %d 行", store.lenOps())
	}
}

// ③ 审批未通过 → 状态 WAITING_APPROVAL，且绝不调用 Port。
func TestPrepareWaitingApprovalDoesNotCallPort(t *testing.T) {
	ctx := context.Background()
	gw, store, port, authz, _ := newHarness()
	authz.decision = AuthzDecision{Effect: AuthzEffectRequireApproval, DecisionID: "dec-approval"}

	op, err := gw.Prepare(ctx, baseInput(map[string]any{"amount": 1280.50}))
	if err != nil {
		t.Fatalf("Prepare 失败：%v", err)
	}
	if op.Status != LedgerWaitingApproval {
		t.Fatalf("期望 WAITING_APPROVAL，得到 %s", op.Status)
	}
	if port.calls() != 0 {
		t.Fatalf("待审批操作不得提前调用 Adapter，实际调用 %d 次", port.calls())
	}
	if store.lenOps() != 1 {
		t.Fatalf("待审批操作必须落库，当前 %d 行", store.lenOps())
	}
}

// ③b 授权拒绝 → PERMISSION_DENIED。
func TestPrepareAuthzDenied(t *testing.T) {
	ctx := context.Background()
	gw, _, port, authz, _ := newHarness()
	authz.decision = AuthzDecision{Effect: AuthzEffectDeny, DecisionID: "dec-deny"}

	_, err := gw.Prepare(ctx, baseInput(map[string]any{"amount": 100.0}))
	if code := codeOf(t, err); code != errors.CodePermissionDenied {
		t.Fatalf("期望 PERMISSION_DENIED，得到 %s", code)
	}
	if port.calls() != 0 {
		t.Fatalf("被拒操作不得调用 Adapter，实际 %d 次", port.calls())
	}
}

// dispatchToUnknown 是 ④/⑤/⑥/⑦/⑧ 的公共前序：Prepare 后派发一个远端超时。
func dispatchToUnknown(t *testing.T, gw *OperationGateway, port *fakePort) *Operation {
	t.Helper()
	ctx := context.Background()
	port.execErr = errors.E(errors.CodeERPAdapterError, "远端调用超时")
	op, err := gw.Prepare(ctx, baseInput(map[string]any{"amount": 1280.50}))
	if err != nil {
		t.Fatalf("Prepare 失败：%v", err)
	}
	res, err := gw.Dispatch(ctx, testTenant, op.ID)
	if err != nil {
		t.Fatalf("Dispatch 失败：%v", err)
	}
	if res.Status != StatusUnknown {
		t.Fatalf("远端超时必须落 UNKNOWN，得到 %s", res.Status)
	}
	return op
}

// ④ Dispatch 远端超时 → UNKNOWN + 提交回执留痕。
func TestDispatchRemoteTimeoutYieldsUnknown(t *testing.T) {
	ctx := context.Background()
	gw, store, port, _, _ := newHarness()
	op := dispatchToUnknown(t, gw, port)

	got, err := gw.GetOperation(ctx, testTenant, op.ID)
	if err != nil {
		t.Fatalf("GetOperation 失败：%v", err)
	}
	if got.Status != LedgerUnknown {
		t.Fatalf("账本状态应为 UNKNOWN，得到 %s", got.Status)
	}
	if got.DispatchedAt == nil {
		t.Fatal("派发时间未写入")
	}
	n, _ := store.CountReceipts(ctx, testTenant, op.ID, ReceiptSubmit)
	if n != 1 {
		t.Fatalf("提交回执应为 1 条，得到 %d", n)
	}
}

// ⑤ Reconcile 确认成功 → SUCCEEDED + 对账回执。
func TestReconcileConfirmSuccess(t *testing.T) {
	ctx := context.Background()
	gw, store, port, _, _ := newHarness()
	op := dispatchToUnknown(t, gw, port)

	port.execErr = nil
	port.opResult = OperationResult{Status: StatusSucceeded, RemoteRef: "remote-op-1", ResultVersion: "2"}

	res, err := gw.Reconcile(ctx, testTenant, op.ID)
	if err != nil {
		t.Fatalf("Reconcile 失败：%v", err)
	}
	if res.Status != StatusSucceeded {
		t.Fatalf("期望 SUCCEEDED，得到 %s", res.Status)
	}
	got, _ := gw.GetOperation(ctx, testTenant, op.ID)
	if got.Status != LedgerSucceeded || got.FinishedAt == nil {
		t.Fatalf("账本应为 SUCCEEDED 且写 finished_at，得到 %s finished=%v", got.Status, got.FinishedAt)
	}
	n, _ := store.CountReceipts(ctx, testTenant, op.ID, ReceiptReconcile)
	if n != 1 {
		t.Fatalf("对账回执应为 1 条，得到 %d", n)
	}
}

// ⑥ Reconcile 确认未执行 → FAILED（可重试）。
func TestReconcileConfirmNotExecuted(t *testing.T) {
	ctx := context.Background()
	gw, _, port, _, _ := newHarness()
	op := dispatchToUnknown(t, gw, port)

	port.opResult = OperationResult{Status: StatusFailed}

	res, err := gw.Reconcile(ctx, testTenant, op.ID)
	if err != nil {
		t.Fatalf("Reconcile 失败：%v", err)
	}
	if res.Status != StatusFailed {
		t.Fatalf("期望 FAILED，得到 %s", res.Status)
	}
	// L1 下"确认未执行"可安全重试。
	if !res.Retryable {
		t.Fatal("L1 下确认未执行应标记可重试")
	}
	got, _ := gw.GetOperation(ctx, testTenant, op.ID)
	if got.Status != LedgerFailed {
		t.Fatalf("账本应为 FAILED，得到 %s", got.Status)
	}
}

// ⑦ 连续未知超阈值 → MANUAL_REVIEW。
func TestReconcileEscalatesToManualReview(t *testing.T) {
	ctx := context.Background()
	gw, store, port, _, _ := newHarness()
	gw.ReconcileThreshold = 5
	op := dispatchToUnknown(t, gw, port)

	port.opResult = OperationResult{Status: StatusUnknown}
	for i := 0; i < 5; i++ {
		if _, err := gw.Reconcile(ctx, testTenant, op.ID); err != nil {
			t.Fatalf("第 %d 次 Reconcile 失败：%v", i+1, err)
		}
	}
	got, _ := gw.GetOperation(ctx, testTenant, op.ID)
	if got.Status != LedgerManualReview {
		t.Fatalf("超过阈值应转 MANUAL_REVIEW，得到 %s", got.Status)
	}
	n, _ := store.CountReceipts(ctx, testTenant, op.ID, ReceiptReconcile)
	if n != 5 {
		t.Fatalf("对账回执应为 5 条，得到 %d", n)
	}
}

// ⑧ L3 能力下 UNKNOWN 后禁止自动重发。
func TestL3UnknownBlocksAutoRedispatch(t *testing.T) {
	ctx := context.Background()
	gw, _, port, _, _ := newHarness()
	port.caps.IdempotencyLevel = IdempotencyLevelL3
	op := dispatchToUnknown(t, gw, port)

	_, err := gw.Dispatch(ctx, testTenant, op.ID)
	if code := codeOf(t, err); code != errors.CodeExecInvalidTransition {
		t.Fatalf("期望 EXEC_INVALID_TRANSITION，得到 %s", code)
	}
	if port.calls() != 1 {
		t.Fatalf("L3 下 UNKNOWN 后不得再次调用 Port.Execute，实际 %d 次", port.calls())
	}
}

// L1 能力下 UNKNOWN 允许同键重放（对照 ⑧）。
func TestL1UnknownAllowsRedispatch(t *testing.T) {
	ctx := context.Background()
	gw, _, port, _, _ := newHarness()
	op := dispatchToUnknown(t, gw, port)

	port.execErr = nil
	port.execResult = OperationResult{Status: StatusSucceeded}
	if _, err := gw.Dispatch(ctx, testTenant, op.ID); err != nil {
		t.Fatalf("L1 下 UNKNOWN 应允许同键重放，得到错误：%v", err)
	}
	if port.calls() != 2 {
		t.Fatalf("L1 重放应再次调用 Port.Execute，实际 %d 次", port.calls())
	}
}

// 受控过滤算子白名单：非白名单算子在校验时返回 VALIDATION_FAILED。
func TestFilterExpressionWhitelist(t *testing.T) {
	if err := (FilterExpression{Field: "amount", Op: "gt", Value: 100}).Validate(); err != nil {
		t.Fatalf("白名单算子被误拒：%v", err)
	}
	bad := FilterExpression{Field: "amount", Op: "drop_table"}
	if code := codeOf(t, bad.Validate()); code != errors.CodeValidationFailed {
		t.Fatalf("非白名单算子应返回 VALIDATION_FAILED，得到 %s", code)
	}
	req := QueryObjectsRequest{
		ObjectType: "ExpenseClaim",
		Filter:     FilterExpression{Field: "title", Op: "regex", Value: ".*"},
	}
	if code := codeOf(t, req.Validate()); code != errors.CodeValidationFailed {
		t.Fatalf("查询校验应拦截非白名单算子，得到 %s", code)
	}
}

// expected_version 参与规范化摘要：相同 payload 不同版本 → 不同键（同键不同摘要 → 冲突）。
func TestPayloadDigestCanonical(t *testing.T) {
	a, err := PayloadDigest(map[string]any{"b": 2, "a": 1})
	if err != nil {
		t.Fatalf("PayloadDigest 失败：%v", err)
	}
	b, err := PayloadDigest(map[string]any{"a": 1, "b": 2})
	if err != nil {
		t.Fatalf("PayloadDigest 失败：%v", err)
	}
	if a != b {
		t.Fatalf("map 键序不应影响摘要：%s vs %s", a, b)
	}
	c, _ := PayloadDigest(map[string]any{"b": 2, "a": 1.0})
	if a != c {
		t.Fatal("数字 1 与 1.0 应规范化后摘要一致")
	}
	d, _ := PayloadDigest(map[string]any{"b": 2, "a": 2})
	if a == d {
		t.Fatal("不同 payload 摘要不应相同")
	}
}
