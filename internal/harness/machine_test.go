package harness

import (
	"context"
	"sync"
	"testing"
	"time"

	"odap/internal/platform/clock"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/runstate"
	"odap/internal/platform/tenantctx"
)

// 编译期保证两种实现都满足 RunStore。
var (
	_ RunStore = pgRunStore{}
	_ RunStore = (*memStore)(nil)
)

// memStore 是内存 RunStore：复现 PG 实现的"加锁复读 → 校验快照 → 条件写"
// 语义，用于在不连接真实数据库的情况下测试 fencing 与非法转换路径
// （第六篇 §19.2 故障注入 9：两个 Worker 同时尝试推进同一个 Run）。
type memStore struct {
	mu        sync.Mutex
	exists    bool
	row       RunRow
	lastEvent RunEventType
	lastTo    RunState
	lastNow   time.Time
	lastField runFieldUpdates
	applied   int

	// afterGet 在 Get 返回快照后、Apply 之前执行，测试用它注入并发接管。
	afterGet func()
}

func (s *memStore) Get(ctx context.Context, runID ids.ID) (RunRow, error) {
	s.mu.Lock()
	if !s.exists {
		s.mu.Unlock()
		return RunRow{}, errors.E(errors.CodeNotFound, "harness: run not found")
	}
	row := s.row
	s.mu.Unlock()
	if s.afterGet != nil {
		s.afterGet()
	}
	return row, nil
}

func (s *memStore) Apply(ctx context.Context, runID ids.ID, from RunState, fromEpoch int64, to RunState, event RunEventType, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.exists {
		return false, nil
	}
	// 复读：快照已失效（另一个 Worker 接管或状态已变）→ fencing 拒绝。
	if s.row.Status != from || s.row.LeaseEpoch != fromEpoch {
		return false, nil
	}
	s.row.Status = to
	s.lastEvent = event
	s.lastTo = to
	s.lastNow = now
	s.lastField = fieldUpdatesFor(from, to, event, now)
	s.applied++
	return true, nil
}

// testMachine 构造挂载内存 store 的 Machine（Pool 为 nil，不得触库）。
func testMachine(store *memStore) *Machine {
	return &Machine{Clock: &clock.FakeClock{Current: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}, store: store}
}

// testCtx 注入测试租户身份。
func testCtx() context.Context {
	return tenantctx.WithIdentity(context.Background(), tenantctx.System(ids.MustParse("11111111-1111-4111-8111-111111111111")))
}

// TestApplyHappyPath 正常转换：状态落盘 + 附属字段维护。
func TestApplyHappyPath(t *testing.T) {
	store := &memStore{exists: true, row: RunRow{Status: runstate.Created, LeaseEpoch: 3}}
	m := testMachine(store)
	if err := m.Apply(testCtx(), ids.New(), EventContextBuildStarted, TransitionFacts{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if store.row.Status != runstate.ContextBuilding {
		t.Fatalf("status = %s, want CONTEXT_BUILDING", store.row.Status)
	}
	if store.lastEvent != EventContextBuildStarted || store.lastTo != runstate.ContextBuilding {
		t.Fatalf("lastEvent=%s lastTo=%s", store.lastEvent, store.lastTo)
	}
	if store.applied != 1 {
		t.Fatalf("applied = %d, want 1", store.applied)
	}
	// CONTEXT_BUILDING 可调度（第六篇 §3.1）：available_at 必须置为时钟当前时间。
	if store.lastField.AvailableAt == nil || !store.lastField.AvailableAt.Equal(m.Clock.Now()) {
		t.Fatalf("AvailableAt = %v, want %v", store.lastField.AvailableAt, m.Clock.Now())
	}
	if store.lastField.FinishedAt != nil {
		t.Fatalf("FinishedAt = %v, want nil for non-terminal", store.lastField.FinishedAt)
	}
}

// TestApplyNonSchedulableTargetKeepsAvailableAt 目标状态不可调度时不得改写
// available_at（第六篇 §3.1：PLANNING 的规划调用由持租约 Worker 继续推进，
// 不进入领取队列，available_at 无意义）。
func TestApplyNonSchedulableTargetKeepsAvailableAt(t *testing.T) {
	store := &memStore{exists: true, row: RunRow{Status: runstate.Ready, LeaseEpoch: 3}}
	m := testMachine(store)
	if err := m.Apply(testCtx(), ids.New(), EventPlanGenerated, TransitionFacts{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if store.row.Status != runstate.Planning {
		t.Fatalf("status = %s, want PLANNING", store.row.Status)
	}
	if store.lastField.AvailableAt != nil {
		t.Fatalf("AvailableAt = %v, want nil for non-schedulable target", *store.lastField.AvailableAt)
	}
}

// TestApplyInvalidTransition 非法转换不得落盘（§13.3：不能插入虚假成功事件）。
func TestApplyInvalidTransition(t *testing.T) {
	cases := []struct {
		name  string
		row   RunRow
		event RunEventType
	}{
		{"终态不接受事件", RunRow{Status: runstate.Completed, LeaseEpoch: 7}, EventStepStarted},
		{"非法组合", RunRow{Status: runstate.Ready, LeaseEpoch: 7}, EventStepStarted},
		{"历史词表状态", RunRow{Status: "QUEUED", LeaseEpoch: 7}, EventContextBuildStarted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := &memStore{exists: true, row: c.row}
			m := testMachine(store)
			err := m.Apply(testCtx(), ids.New(), c.event, TransitionFacts{LeaseValid: true})
			if err == nil {
				t.Fatalf("Apply succeeded, want invalid transition error")
			}
			if code := errors.As(err).Code; code != errors.CodeExecInvalidTransition {
				t.Fatalf("error code = %s, want %s", code, errors.CodeExecInvalidTransition)
			}
			if store.applied != 0 {
				t.Fatalf("store applied %d times, want 0", store.applied)
			}
			if store.row.Status != c.row.Status {
				t.Fatalf("status mutated to %s, want unchanged %s", store.row.Status, c.row.Status)
			}
		})
	}
}

// TestApplyGuardRejected 守卫拒绝同样不得落盘。
func TestApplyGuardRejected(t *testing.T) {
	store := &memStore{exists: true, row: RunRow{Status: runstate.Cancelling, LeaseEpoch: 2}}
	m := testMachine(store)
	// CANCELLING + ReconciliationSucceeded 但 CancelRequested 为假 → 守卫拒绝。
	err := m.Apply(testCtx(), ids.New(), EventReconciliationSucceeded, TransitionFacts{})
	if err == nil {
		t.Fatalf("Apply succeeded, want guard rejection")
	}
	if code := errors.As(err).Code; code != errors.CodeExecInvalidTransition {
		t.Fatalf("error code = %s, want %s", code, errors.CodeExecInvalidTransition)
	}
	if store.applied != 0 || store.row.Status != runstate.Cancelling {
		t.Fatalf("store mutated: applied=%d status=%s", store.applied, store.row.Status)
	}
}

// TestApplyFencingRejectsStaleWorker 旧 Worker 写入被拒（§6.3 fencing）。
// 场景：Get 读到 (READY, 3) 后，Worker B 接管（epoch=4, RUNNING），
// 本 Worker 的 Apply 复读发现快照失效 → CodeExecLeaseLost，且不得覆盖 B 的状态。
func TestApplyFencingRejectsStaleWorker(t *testing.T) {
	store := &memStore{exists: true, row: RunRow{Status: runstate.Ready, LeaseEpoch: 3}}
	store.afterGet = func() {
		store.mu.Lock()
		defer store.mu.Unlock()
		store.row = RunRow{Status: runstate.Running, LeaseEpoch: 4}
	}
	m := testMachine(store)
	err := m.Apply(testCtx(), ids.New(), EventPlanGenerated, TransitionFacts{})
	if err == nil {
		t.Fatalf("Apply succeeded, want lease lost")
	}
	if code := errors.As(err).Code; code != errors.CodeExecLeaseLost {
		t.Fatalf("error code = %s, want %s", code, errors.CodeExecLeaseLost)
	}
	if store.row.Status != runstate.Running || store.row.LeaseEpoch != 4 {
		t.Fatalf("stale worker overwrote row: %+v", store.row)
	}
	if store.applied != 0 {
		t.Fatalf("applied = %d, want 0", store.applied)
	}
}

// TestApplyFencingStatusDrift 状态漂移（epoch 未变）同样被 fencing 拒绝。
func TestApplyFencingStatusDrift(t *testing.T) {
	store := &memStore{exists: true, row: RunRow{Status: runstate.Running, LeaseEpoch: 9}}
	store.afterGet = func() {
		store.mu.Lock()
		defer store.mu.Unlock()
		store.row = RunRow{Status: runstate.WaitingApproval, LeaseEpoch: 9}
	}
	m := testMachine(store)
	err := m.Apply(testCtx(), ids.New(), EventStepSucceeded, TransitionFacts{LeaseValid: true})
	if err == nil || errors.As(err).Code != errors.CodeExecLeaseLost {
		t.Fatalf("err = %v, want EXEC_LEASE_LOST", err)
	}
	if store.row.Status != runstate.WaitingApproval {
		t.Fatalf("status = %s, want WAITING_APPROVAL", store.row.Status)
	}
}

// TestApplyRunNotFound Run 不存在 → CodeNotFound。
func TestApplyRunNotFound(t *testing.T) {
	m := testMachine(&memStore{exists: false})
	err := m.Apply(testCtx(), ids.New(), EventPlanGenerated, TransitionFacts{})
	if err == nil || errors.As(err).Code != errors.CodeNotFound {
		t.Fatalf("err = %v, want NOT_FOUND", err)
	}
}

// TestApplyMissingIdentity 缺少可信租户上下文 → fail-closed 拒绝。
func TestApplyMissingIdentity(t *testing.T) {
	store := &memStore{exists: true, row: RunRow{Status: runstate.Ready, LeaseEpoch: 1}}
	m := testMachine(store)
	err := m.Apply(context.Background(), ids.New(), EventPlanGenerated, TransitionFacts{})
	if err == nil || errors.As(err).Code != errors.CodePermissionDenied {
		t.Fatalf("err = %v, want PERMISSION_DENIED", err)
	}
	if store.applied != 0 {
		t.Fatalf("applied = %d, want 0", store.applied)
	}
}

// TestApplyRejectsZeroRunID 零 ID 直接拒绝（不触库）。
func TestApplyRejectsZeroRunID(t *testing.T) {
	m := testMachine(&memStore{exists: true})
	err := m.Apply(testCtx(), ids.ID{}, EventPlanGenerated, TransitionFacts{})
	if err == nil || errors.As(err).Code != errors.CodeValidationFailed {
		t.Fatalf("err = %v, want VALIDATION_FAILED", err)
	}
}

// TestApplyFieldMaintenance 关键附属字段维护：取消/对账/终态/失败。
func TestApplyFieldMaintenance(t *testing.T) {
	cases := []struct {
		name       string
		row        RunRow
		event      RunEventType
		facts      TransitionFacts
		want       RunState
		wantCancel bool
		wantRecon  bool
		wantFinish bool
		wantErr    bool
	}{
		{
			name: "取消请求置 cancel_requested_at", row: RunRow{Status: runstate.Running, LeaseEpoch: 5},
			event: EventCancellationRequested, want: runstate.Cancelling, wantCancel: true,
		},
		{
			name: "发现未知外部操作置 last_reconciliation_at", row: RunRow{Status: runstate.Running, LeaseEpoch: 5},
			event: EventExternalOperationUnknown, want: runstate.RequiresReconciliation, wantRecon: true,
		},
		{
			name: "完成置 finished_at", row: RunRow{Status: runstate.Running, LeaseEpoch: 5},
			event: EventRunCompleted, want: runstate.Completed, wantFinish: true,
		},
		{
			name: "失败置 finished_at 与 error_code", row: RunRow{Status: runstate.ContextBuilding, LeaseEpoch: 5},
			event: EventContextBuildFailed, want: runstate.Failed, wantFinish: true, wantErr: true,
		},
		{
			name: "租约丢失转恢复", row: RunRow{Status: runstate.Running, LeaseEpoch: 5},
			event: EventLeaseLost, want: runstate.Recovering,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := &memStore{exists: true, row: c.row}
			m := testMachine(store)
			if err := m.Apply(testCtx(), ids.New(), c.event, c.facts); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if store.row.Status != c.want {
				t.Fatalf("status = %s, want %s", store.row.Status, c.want)
			}
			f := store.lastField
			if got := f.CancelRequestedAt != nil; got != c.wantCancel {
				t.Fatalf("CancelRequestedAt set = %v, want %v", got, c.wantCancel)
			}
			if got := f.LastReconciliationAt != nil; got != c.wantRecon {
				t.Fatalf("LastReconciliationAt set = %v, want %v", got, c.wantRecon)
			}
			if got := f.FinishedAt != nil; got != c.wantFinish {
				t.Fatalf("FinishedAt set = %v, want %v", got, c.wantFinish)
			}
			if c.wantErr {
				if f.ErrorCode == nil || *f.ErrorCode != string(c.event) {
					t.Fatalf("ErrorCode = %v, want %s", f.ErrorCode, c.event)
				}
				if f.ErrorSummary == nil || *f.ErrorSummary == "" {
					t.Fatalf("ErrorSummary = %v, want non-empty", f.ErrorSummary)
				}
			} else if f.ErrorCode != nil {
				t.Fatalf("ErrorCode = %v, want nil", *f.ErrorCode)
			}
		})
	}
}

// TestApplyStartedAtOnFirstTransition 首次离开 CREATED 时置 started_at。
func TestApplyStartedAtOnFirstTransition(t *testing.T) {
	store := &memStore{exists: true, row: RunRow{Status: runstate.Created, LeaseEpoch: 0}}
	m := testMachine(store)
	if err := m.Apply(testCtx(), ids.New(), EventContextBuildStarted, TransitionFacts{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if store.lastField.StartedAt == nil || !store.lastField.StartedAt.Equal(m.Clock.Now()) {
		t.Fatalf("StartedAt = %v, want %v", store.lastField.StartedAt, m.Clock.Now())
	}
	// 非首次转换不再设置 started_at。
	store2 := &memStore{exists: true, row: RunRow{Status: runstate.Ready, LeaseEpoch: 1}}
	m2 := testMachine(store2)
	if err := m2.Apply(testCtx(), ids.New(), EventPlanGenerated, TransitionFacts{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if store2.lastField.StartedAt != nil {
		t.Fatalf("StartedAt = %v, want nil for non-first transition", *store2.lastField.StartedAt)
	}
}
