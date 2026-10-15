package harness

import (
	"testing"

	"odap/internal/platform/errors"
	"odap/internal/platform/runstate"
)

// facts 画像：表驱动用例按守卫分支选取。
var (
	factsNone      = TransitionFacts{}
	factsLease     = TransitionFacts{LeaseValid: true}
	factsInflight  = TransitionFacts{HasInflightExternalOps: true}
	factsExhausted = TransitionFacts{AttemptsExhausted: true}
	factsDeadline  = TransitionFacts{DeadlinePassed: true}
	factsPending   = TransitionFacts{HasPendingApproval: true}
	factsCancel    = TransitionFacts{CancelRequested: true}
	factsCancelDD  = TransitionFacts{CancelConvergenceDeadlinePassed: true}
	factsReconPend = TransitionFacts{ReconciliationPending: true}
	factsManualReq = TransitionFacts{ManualReviewRequested: true}
)

// legalCases 覆盖 rules 表中每一条合法转换至少一例（含守卫分支），
// 由 TestNextStateLegalTransitions 与 TestEveryRuleHasTestCase 共同断言。
var legalCases = []struct {
	name  string
	from  RunState
	event RunEventType
	facts TransitionFacts
	want  RunState
}{
	// ---- CREATED（铁律①） ----
	{"CREATED+ContextBuildStarted", runstate.Created, EventContextBuildStarted, factsNone, runstate.ContextBuilding},
	{"CREATED+CancellationRequested", runstate.Created, EventCancellationRequested, factsNone, runstate.Cancelling},
	{"CREATED+RunExpired", runstate.Created, EventRunExpired, factsNone, runstate.Expired},
	{"CREATED+RunExpired+在途操作", runstate.Created, EventRunExpired, factsInflight, runstate.RequiresReconciliation},
	{"CREATED+LeaseAcquired", runstate.Created, EventLeaseAcquired, factsNone, runstate.Created},
	{"CREATED+LeaseLost", runstate.Created, EventLeaseLost, factsNone, runstate.Created},

	// ---- CONTEXT_BUILDING ----
	{"CONTEXT_BUILDING+ContextBuildSucceeded", runstate.ContextBuilding, EventContextBuildSucceeded, factsNone, runstate.Ready},
	{"CONTEXT_BUILDING+ContextBuildFailed", runstate.ContextBuilding, EventContextBuildFailed, factsNone, runstate.Failed},
	{"CONTEXT_BUILDING+RunPaused", runstate.ContextBuilding, EventRunPaused, factsNone, runstate.Paused},
	{"CONTEXT_BUILDING+CancellationRequested", runstate.ContextBuilding, EventCancellationRequested, factsNone, runstate.Cancelling},
	{"CONTEXT_BUILDING+RecoveryStarted", runstate.ContextBuilding, EventRecoveryStarted, factsNone, runstate.Recovering},
	{"CONTEXT_BUILDING+ExternalOperationUnknown", runstate.ContextBuilding, EventExternalOperationUnknown, factsNone, runstate.RequiresReconciliation},
	{"CONTEXT_BUILDING+RunFailed", runstate.ContextBuilding, EventRunFailed, factsNone, runstate.Failed},
	{"CONTEXT_BUILDING+RunExpired", runstate.ContextBuilding, EventRunExpired, factsNone, runstate.Expired},
	{"CONTEXT_BUILDING+RunExpired+在途操作", runstate.ContextBuilding, EventRunExpired, factsInflight, runstate.RequiresReconciliation},
	{"CONTEXT_BUILDING+LeaseAcquired", runstate.ContextBuilding, EventLeaseAcquired, factsNone, runstate.ContextBuilding},
	{"CONTEXT_BUILDING+LeaseLost", runstate.ContextBuilding, EventLeaseLost, factsNone, runstate.ContextBuilding},

	// ---- READY ----
	{"READY+PlanGenerated", runstate.Ready, EventPlanGenerated, factsNone, runstate.Planning},
	{"READY+RunPaused", runstate.Ready, EventRunPaused, factsNone, runstate.Paused},
	{"READY+CancellationRequested", runstate.Ready, EventCancellationRequested, factsNone, runstate.Cancelling},
	{"READY+RecoveryStarted", runstate.Ready, EventRecoveryStarted, factsNone, runstate.Recovering},
	{"READY+RunFailed", runstate.Ready, EventRunFailed, factsNone, runstate.Failed},
	{"READY+RunExpired", runstate.Ready, EventRunExpired, factsNone, runstate.Expired},
	{"READY+DataRefreshed", runstate.Ready, EventDataRefreshed, factsNone, runstate.Ready},
	{"READY+LeaseAcquired", runstate.Ready, EventLeaseAcquired, factsNone, runstate.Ready},
	{"READY+LeaseLost", runstate.Ready, EventLeaseLost, factsNone, runstate.Ready},

	// ---- PLANNING ----
	{"PLANNING+PlanValidationSucceeded", runstate.Planning, EventPlanValidationSucceeded, factsNone, runstate.PlanValidating},
	{"PLANNING+RunPaused", runstate.Planning, EventRunPaused, factsNone, runstate.Paused},
	{"PLANNING+CancellationRequested", runstate.Planning, EventCancellationRequested, factsNone, runstate.Cancelling},
	{"PLANNING+RecoveryStarted", runstate.Planning, EventRecoveryStarted, factsNone, runstate.Recovering},
	{"PLANNING+RunFailed", runstate.Planning, EventRunFailed, factsNone, runstate.Failed},
	{"PLANNING+RunExpired", runstate.Planning, EventRunExpired, factsNone, runstate.Expired},

	// ---- PLAN_VALIDATING ----
	{"PLAN_VALIDATING+PlanValidationSucceeded", runstate.PlanValidating, EventPlanValidationSucceeded, factsNone, runstate.Running},
	{"PLAN_VALIDATING+PlanValidationFailed+预算未尽", runstate.PlanValidating, EventPlanValidationFailed, factsNone, runstate.Ready},
	{"PLAN_VALIDATING+PlanValidationFailed+预算耗尽", runstate.PlanValidating, EventPlanValidationFailed, factsExhausted, runstate.Failed},
	{"PLAN_VALIDATING+RunPaused", runstate.PlanValidating, EventRunPaused, factsNone, runstate.Paused},
	{"PLAN_VALIDATING+CancellationRequested", runstate.PlanValidating, EventCancellationRequested, factsNone, runstate.Cancelling},
	{"PLAN_VALIDATING+RecoveryStarted", runstate.PlanValidating, EventRecoveryStarted, factsNone, runstate.Recovering},
	{"PLAN_VALIDATING+RunFailed", runstate.PlanValidating, EventRunFailed, factsNone, runstate.Failed},
	{"PLAN_VALIDATING+RunExpired", runstate.PlanValidating, EventRunExpired, factsNone, runstate.Expired},

	// ---- RUNNING ----
	{"RUNNING+StepStarted+有效租约", runstate.Running, EventStepStarted, factsLease, runstate.Running},
	{"RUNNING+StepSucceeded+有效租约", runstate.Running, EventStepSucceeded, factsLease, runstate.Running},
	{"RUNNING+StepFailed+有效租约", runstate.Running, EventStepFailed, factsLease, runstate.Running},
	{"RUNNING+InputRequested", runstate.Running, EventInputRequested, factsNone, runstate.WaitingInput},
	{"RUNNING+ApprovalRequested", runstate.Running, EventApprovalRequested, factsNone, runstate.WaitingApproval},
	{"RUNNING+RunPaused", runstate.Running, EventRunPaused, factsNone, runstate.Paused},
	{"RUNNING+CancellationRequested", runstate.Running, EventCancellationRequested, factsNone, runstate.Cancelling},
	{"RUNNING+RecoveryStarted", runstate.Running, EventRecoveryStarted, factsNone, runstate.Recovering},
	{"RUNNING+ExternalOperationUnknown", runstate.Running, EventExternalOperationUnknown, factsNone, runstate.RequiresReconciliation},
	{"RUNNING+ManualInterventionRequired", runstate.Running, EventManualInterventionRequired, factsNone, runstate.ManualReview},
	{"RUNNING+RunCompleted", runstate.Running, EventRunCompleted, factsNone, runstate.Completed},
	{"RUNNING+RunFailed", runstate.Running, EventRunFailed, factsNone, runstate.Failed},
	{"RUNNING+RunExpired", runstate.Running, EventRunExpired, factsNone, runstate.Expired},
	{"RUNNING+RunExpired+在途操作", runstate.Running, EventRunExpired, factsInflight, runstate.RequiresReconciliation},
	{"RUNNING+DataRefreshed", runstate.Running, EventDataRefreshed, factsNone, runstate.Running},
	{"RUNNING+LeaseAcquired", runstate.Running, EventLeaseAcquired, factsNone, runstate.Running},
	{"RUNNING+LeaseLost", runstate.Running, EventLeaseLost, factsNone, runstate.Recovering},

	// ---- WAITING_INPUT ----
	{"WAITING_INPUT+InputReceived", runstate.WaitingInput, EventInputReceived, factsNone, runstate.Running},
	{"WAITING_INPUT+TimeoutEscalated+默认升级", runstate.WaitingInput, EventTimeoutEscalated, factsNone, runstate.ManualReview},
	{"WAITING_INPUT+TimeoutEscalated+预算耗尽", runstate.WaitingInput, EventTimeoutEscalated, factsExhausted, runstate.Expired},
	{"WAITING_INPUT+TimeoutEscalated+在途操作", runstate.WaitingInput, EventTimeoutEscalated, factsInflight, runstate.RequiresReconciliation},
	{"WAITING_INPUT+RunPaused", runstate.WaitingInput, EventRunPaused, factsNone, runstate.Paused},
	{"WAITING_INPUT+CancellationRequested", runstate.WaitingInput, EventCancellationRequested, factsNone, runstate.Cancelling},
	{"WAITING_INPUT+RecoveryStarted", runstate.WaitingInput, EventRecoveryStarted, factsNone, runstate.Recovering},
	{"WAITING_INPUT+ExternalOperationUnknown", runstate.WaitingInput, EventExternalOperationUnknown, factsNone, runstate.RequiresReconciliation},
	{"WAITING_INPUT+ManualInterventionRequired", runstate.WaitingInput, EventManualInterventionRequired, factsNone, runstate.ManualReview},
	{"WAITING_INPUT+RunFailed", runstate.WaitingInput, EventRunFailed, factsNone, runstate.Failed},
	{"WAITING_INPUT+RunFailed+在途操作", runstate.WaitingInput, EventRunFailed, factsInflight, runstate.RequiresReconciliation},
	{"WAITING_INPUT+RunExpired", runstate.WaitingInput, EventRunExpired, factsNone, runstate.Expired},

	// ---- WAITING_APPROVAL ----
	{"WAITING_APPROVAL+ApprovalResolved", runstate.WaitingApproval, EventApprovalResolved, factsNone, runstate.Running},
	{"WAITING_APPROVAL+ApprovalResolved+多级审批", runstate.WaitingApproval, EventApprovalResolved, factsPending, runstate.WaitingApproval},
	{"WAITING_APPROVAL+TimeoutEscalated+默认升级", runstate.WaitingApproval, EventTimeoutEscalated, factsNone, runstate.ManualReview},
	{"WAITING_APPROVAL+TimeoutEscalated+预算耗尽", runstate.WaitingApproval, EventTimeoutEscalated, factsExhausted, runstate.Expired},
	{"WAITING_APPROVAL+TimeoutEscalated+在途操作", runstate.WaitingApproval, EventTimeoutEscalated, factsInflight, runstate.RequiresReconciliation},
	{"WAITING_APPROVAL+RunPaused", runstate.WaitingApproval, EventRunPaused, factsNone, runstate.Paused},
	{"WAITING_APPROVAL+CancellationRequested", runstate.WaitingApproval, EventCancellationRequested, factsNone, runstate.Cancelling},
	{"WAITING_APPROVAL+RecoveryStarted", runstate.WaitingApproval, EventRecoveryStarted, factsNone, runstate.Recovering},
	{"WAITING_APPROVAL+ExternalOperationUnknown", runstate.WaitingApproval, EventExternalOperationUnknown, factsNone, runstate.RequiresReconciliation},
	{"WAITING_APPROVAL+ManualInterventionRequired", runstate.WaitingApproval, EventManualInterventionRequired, factsNone, runstate.ManualReview},
	{"WAITING_APPROVAL+RunFailed", runstate.WaitingApproval, EventRunFailed, factsNone, runstate.Failed},
	{"WAITING_APPROVAL+RunFailed+在途操作", runstate.WaitingApproval, EventRunFailed, factsInflight, runstate.RequiresReconciliation},
	{"WAITING_APPROVAL+RunExpired", runstate.WaitingApproval, EventRunExpired, factsNone, runstate.Expired},

	// ---- PAUSED ----
	{"PAUSED+RunResumed", runstate.Paused, EventRunResumed, factsNone, runstate.Running},
	{"PAUSED+CancellationRequested", runstate.Paused, EventCancellationRequested, factsNone, runstate.Cancelling},
	{"PAUSED+RecoveryStarted", runstate.Paused, EventRecoveryStarted, factsNone, runstate.Recovering},
	{"PAUSED+ManualInterventionRequired", runstate.Paused, EventManualInterventionRequired, factsNone, runstate.ManualReview},
	{"PAUSED+RunFailed", runstate.Paused, EventRunFailed, factsNone, runstate.Failed},
	{"PAUSED+RunExpired", runstate.Paused, EventRunExpired, factsNone, runstate.Expired},

	// ---- RECOVERING ----
	{"RECOVERING+RunPaused", runstate.Recovering, EventRunPaused, factsNone, runstate.Paused},
	{"RECOVERING+CancellationRequested", runstate.Recovering, EventCancellationRequested, factsNone, runstate.Cancelling},
	{"RECOVERING+RecoverySucceeded", runstate.Recovering, EventRecoverySucceeded, factsNone, runstate.Running},
	{"RECOVERING+ExternalOperationUnknown", runstate.Recovering, EventExternalOperationUnknown, factsNone, runstate.RequiresReconciliation},
	{"RECOVERING+ManualInterventionRequired", runstate.Recovering, EventManualInterventionRequired, factsNone, runstate.ManualReview},
	{"RECOVERING+RunCompleted", runstate.Recovering, EventRunCompleted, factsNone, runstate.Completed},
	{"RECOVERING+RunFailed+预算未尽", runstate.Recovering, EventRunFailed, factsNone, runstate.Recovering},
	{"RECOVERING+RunFailed+预算耗尽", runstate.Recovering, EventRunFailed, factsExhausted, runstate.Failed},
	{"RECOVERING+LeaseAcquired", runstate.Recovering, EventLeaseAcquired, factsNone, runstate.Recovering},
	{"RECOVERING+LeaseLost", runstate.Recovering, EventLeaseLost, factsNone, runstate.Recovering},

	// ---- CANCELLING ----
	{"CANCELLING+RunPaused", runstate.Cancelling, EventRunPaused, factsNone, runstate.Paused},
	{"CANCELLING+CancellationRequested+幂等", runstate.Cancelling, EventCancellationRequested, factsNone, runstate.Cancelling},
	{"CANCELLING+CancellationConverged+无在途", runstate.Cancelling, EventCancellationConverged, factsNone, runstate.Cancelled},
	{"CANCELLING+CancellationConverged+有在途", runstate.Cancelling, EventCancellationConverged, factsInflight, runstate.RequiresReconciliation},
	{"CANCELLING+ExternalOperationUnknown", runstate.Cancelling, EventExternalOperationUnknown, factsNone, runstate.RequiresReconciliation},
	{"CANCELLING+ReconciliationSucceeded+取消中", runstate.Cancelling, EventReconciliationSucceeded, factsCancel, runstate.Cancelled},
	{"CANCELLING+ReconciliationFailed", runstate.Cancelling, EventReconciliationFailed, factsNone, runstate.ManualReview},
	{"CANCELLING+ManualInterventionRequired", runstate.Cancelling, EventManualInterventionRequired, factsNone, runstate.ManualReview},
	{"CANCELLING+ManualReviewResolved", runstate.Cancelling, EventManualReviewResolved, factsNone, runstate.ManualReview},
	{"CANCELLING+TimeoutEscalated+收敛超期", runstate.Cancelling, EventTimeoutEscalated, factsCancelDD, runstate.ManualReview},
	{"CANCELLING+LeaseAcquired", runstate.Cancelling, EventLeaseAcquired, factsNone, runstate.Cancelling},
	{"CANCELLING+LeaseLost", runstate.Cancelling, EventLeaseLost, factsNone, runstate.Cancelling},

	// ---- REQUIRES_RECONCILIATION ----
	{"REQUIRES_RECONCILIATION+CancellationRequested", runstate.RequiresReconciliation, EventCancellationRequested, factsNone, runstate.Cancelling},
	{"REQUIRES_RECONCILIATION+RecoveryStarted", runstate.RequiresReconciliation, EventRecoveryStarted, factsNone, runstate.Recovering},
	{"REQUIRES_RECONCILIATION+ReconciliationSucceeded", runstate.RequiresReconciliation, EventReconciliationSucceeded, factsNone, runstate.Running},
	{"REQUIRES_RECONCILIATION+ReconciliationSucceeded+取消中", runstate.RequiresReconciliation, EventReconciliationSucceeded, factsCancel, runstate.Cancelled},
	{"REQUIRES_RECONCILIATION+ReconciliationFailed", runstate.RequiresReconciliation, EventReconciliationFailed, factsNone, runstate.ManualReview},
	{"REQUIRES_RECONCILIATION+ManualInterventionRequired", runstate.RequiresReconciliation, EventManualInterventionRequired, factsNone, runstate.ManualReview},
	{"REQUIRES_RECONCILIATION+RunCompleted", runstate.RequiresReconciliation, EventRunCompleted, factsNone, runstate.Completed},
	{"REQUIRES_RECONCILIATION+RunFailed", runstate.RequiresReconciliation, EventRunFailed, factsNone, runstate.Failed},
	{"REQUIRES_RECONCILIATION+LeaseAcquired", runstate.RequiresReconciliation, EventLeaseAcquired, factsNone, runstate.RequiresReconciliation},
	{"REQUIRES_RECONCILIATION+LeaseLost", runstate.RequiresReconciliation, EventLeaseLost, factsNone, runstate.RequiresReconciliation},

	// ---- MANUAL_REVIEW ----
	{"MANUAL_REVIEW+CancellationRequested", runstate.ManualReview, EventCancellationRequested, factsNone, runstate.Cancelling},
	{"MANUAL_REVIEW+ManualReviewResolved+默认恢复", runstate.ManualReview, EventManualReviewResolved, factsNone, runstate.Running},
	{"MANUAL_REVIEW+ManualReviewResolved+取消中", runstate.ManualReview, EventManualReviewResolved, factsCancel, runstate.Cancelled},
	{"MANUAL_REVIEW+ManualReviewResolved+超总期限", runstate.ManualReview, EventManualReviewResolved, factsDeadline, runstate.Failed},
}

// TestNextStateLegalTransitions 逐例断言合法转换的目标状态。
func TestNextStateLegalTransitions(t *testing.T) {
	for _, c := range legalCases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NextState(c.from, c.event, c.facts)
			if err != nil {
				t.Fatalf("NextState(%s, %s) unexpected error: %v", c.from, c.event, err)
			}
			if got != c.want {
				t.Fatalf("NextState(%s, %s) = %s, want %s", c.from, c.event, got, c.want)
			}
		})
	}
}

// TestEveryRuleHasTestCase 断言 rules 表每条规则至少有一个合法用例，
// 且每个用例都指向真实存在的规则（矩阵与测试双向一致，§14.2 工程约束 1）。
//
// 注意：legalCases 允许对同一 (from,event) 提供多个 facts 分支用例
// （如等待态超时升级、取消收敛的在途/无在途分支），因此不能断言
// "用例数 == 唯一 (from,event) 数"。
func TestEveryRuleHasTestCase(t *testing.T) {
	covered := make(map[string]bool, len(legalCases))
	for _, c := range legalCases {
		covered[string(c.from)+"|"+string(c.event)] = true
	}
	ruleKeys := make(map[string]bool, len(rules))
	for _, r := range rules {
		ruleKeys[string(r.from)+"|"+string(r.event)] = true
	}
	for _, r := range rules {
		key := string(r.from) + "|" + string(r.event)
		if !covered[key] {
			t.Errorf("rule %s has no legal test case", key)
		}
	}
	for _, c := range legalCases {
		key := string(c.from) + "|" + string(c.event)
		if !ruleKeys[key] {
			t.Errorf("legal case %q references non-existent rule %s", c.name, key)
		}
	}
}

// TestTerminalStatesRejectAllEvents 终态不接受任何事件（铁律⑦）。
func TestTerminalStatesRejectAllEvents(t *testing.T) {
	terminals := []RunState{runstate.Completed, runstate.Failed, runstate.Cancelled, runstate.Expired}
	for _, st := range terminals {
		for _, ev := range allEvents {
			got, err := NextState(st, ev, factsLease)
			if err == nil {
				t.Fatalf("NextState(%s, %s) = %s, want error (terminal state has no outgoing edge)", st, ev, got)
			}
			if code := errors.As(err).Code; code != errors.CodeExecInvalidTransition {
				t.Fatalf("NextState(%s, %s) error code = %s, want %s", st, ev, code, errors.CodeExecInvalidTransition)
			}
			if got != st {
				t.Fatalf("NextState(%s, %s) returned state %s, want unchanged %s", st, ev, got, st)
			}
		}
	}
}

// TestHistoricalVocabularyRejected 历史词表（QUEUED 等）不在 17 态集合内，
// 传入必须报错（contracts/01 §1.1 只读映射）。
func TestHistoricalVocabularyRejected(t *testing.T) {
	historical := []string{
		"QUEUED",                 // 第一篇/第二篇旧词，映射 READY
		"CONTEXT_READY",          // 总书 §7.1 旧词，映射 READY
		"VALIDATING",             // 总书 §7.1 旧词，映射 PLAN_VALIDATING
		"UNKNOWN_OUTCOME",        // 第十四篇旧词，等价 REQUIRES_RECONCILIATION
		"WAITING_RECONCILIATION", // 第十四篇旧词，等价 REQUIRES_RECONCILIATION
		"TO_RECONCILE",           // contracts/01 §5.2 禁止的同义反复
		"running",                // 大小写不合法
		"",                       // 空状态
	}
	for _, st := range historical {
		got, err := NextState(st, EventContextBuildStarted, factsNone)
		if err == nil {
			t.Fatalf("NextState(%q, ContextBuildStarted) = %s, want error", st, got)
		}
		if code := errors.As(err).Code; code != errors.CodeExecInvalidTransition {
			t.Fatalf("NextState(%q) error code = %s, want %s", st, code, errors.CodeExecInvalidTransition)
		}
	}
}

// TestUnregisteredEventRejected 未登记事件（含文档旧事件名）一律拒绝。
func TestUnregisteredEventRejected(t *testing.T) {
	unregistered := []RunEventType{
		"PlanRegenerationRequested", // 第六篇 §3.3 旧事件，已折叠进守卫
		"ApprovalRejected",          // 第六篇 §3.3 旧事件，拒绝走 RunFailed
		"InputDeadlineExceeded",     // 旧事件，已合并为 TimeoutEscalated
		"ApprovalExpired",           // 旧事件，已合并为 TimeoutEscalated
		"CancelConverged",           // 旧命名，现为 CancellationConverged
		"RecoveryCompleted",         // 旧命名，现为 RecoverySucceeded
		"RunCreated",                // §3.3 清单中的创建事件，不参与状态转换
		"BogusEvent",
		"",
	}
	for _, ev := range unregistered {
		if validEvent(ev) {
			t.Fatalf("validEvent(%q) = true, want false", ev)
		}
		_, err := NextState(runstate.Running, ev, factsLease)
		if err == nil {
			t.Fatalf("NextState(RUNNING, %q) succeeded, want error", ev)
		}
		if code := errors.As(err).Code; code != errors.CodeExecInvalidTransition {
			t.Fatalf("NextState(RUNNING, %q) error code = %s, want %s", ev, code, errors.CodeExecInvalidTransition)
		}
	}
}

// TestGuardRejections 守卫不满足时返回 CodeExecInvalidTransition 且状态不变。
func TestGuardRejections(t *testing.T) {
	cases := []struct {
		name  string
		from  RunState
		event RunEventType
		facts TransitionFacts
	}{
		{"步骤推进无有效租约", runstate.Running, EventStepStarted, factsNone},
		{"步骤成功无有效租约", runstate.Running, EventStepSucceeded, factsNone},
		{"步骤失败无有效租约", runstate.Running, EventStepFailed, factsNone},
		{"完成时存在在途操作", runstate.Running, EventRunCompleted, factsInflight},
		{"对账收敛但未请求取消", runstate.Cancelling, EventReconciliationSucceeded, factsNone},
		{"取消收敛超时但期限未过", runstate.Cancelling, EventTimeoutEscalated, factsNone},
		{"人工复核解决时仍有未决介入", runstate.ManualReview, EventManualReviewResolved, factsManualReq},
		{"人工复核解决时对账未决", runstate.ManualReview, EventManualReviewResolved, factsReconPend},
		{"租约仍有效不得宣称为丢失", runstate.Running, EventLeaseLost, factsLease},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NextState(c.from, c.event, c.facts)
			if err == nil {
				t.Fatalf("NextState(%s, %s) = %s, want guard rejection", c.from, c.event, got)
			}
			if code := errors.As(err).Code; code != errors.CodeExecInvalidTransition {
				t.Fatalf("error code = %s, want %s", code, errors.CodeExecInvalidTransition)
			}
			if got != c.from {
				t.Fatalf("state changed to %s on guard rejection, want unchanged %s", got, c.from)
			}
		})
	}
}

// TestTransitionTableConsistency TransitionTable 与 rules 一一对应，
// 且覆盖全部 17 态与 31 事件（contracts/01 §6.2 一致性门禁）。
func TestTransitionTableConsistency(t *testing.T) {
	table := TransitionTable()
	if len(table) != len(rules) {
		t.Fatalf("TransitionTable has %d rows, rules has %d", len(table), len(rules))
	}
	seenRule := make(map[string]bool, len(table))
	stateSeen := make(map[string]bool, len(runstate.All))
	eventSeen := make(map[string]bool, len(allEvents))
	for i, tr := range table {
		if tr.From != rules[i].from || tr.Event != string(rules[i].event) || tr.To != rules[i].to || tr.GuardDesc != rules[i].guard {
			t.Fatalf("row %d mismatch: %+v vs rule %+v", i, tr, rules[i])
		}
		if !runstate.Valid(tr.From) {
			t.Errorf("row %d: From %q is not a registered state", i, tr.From)
		}
		if !runstate.Valid(tr.To) {
			t.Errorf("row %d: To %q is not a registered state", i, tr.To)
		}
		if !validEvent(RunEventType(tr.Event)) {
			t.Errorf("row %d: Event %q is not a registered event", i, tr.Event)
		}
		key := tr.From + "|" + tr.Event
		if seenRule[key] {
			t.Errorf("duplicate transition row: %s", key)
		}
		seenRule[key] = true
		stateSeen[tr.From] = true
		stateSeen[tr.To] = true
		eventSeen[tr.Event] = true
	}
	for _, s := range runstate.All {
		if !stateSeen[s] {
			t.Errorf("state %s never appears in transition table (contracts/01 §6.2)", s)
		}
	}
	for _, e := range allEvents {
		if !eventSeen[string(e)] {
			t.Errorf("event %s never appears in transition table (§3.3: 事件清单 = 矩阵覆盖集合)", e)
		}
	}
	// 无守卫规则在零 facts 下必须直接放行至主目标。
	for _, r := range rules {
		if r.resolve != nil {
			continue
		}
		got, err := NextState(r.from, r.event, factsNone)
		if err != nil {
			t.Errorf("unguarded rule %s|%s rejected with zero facts: %v", r.from, r.event, err)
			continue
		}
		if got != r.to {
			t.Errorf("unguarded rule %s|%s zero-facts target = %s, want %s", r.from, r.event, got, r.to)
		}
	}
}

// TestAllEventsVocabulary 事件词表：31 个、非空、唯一。
func TestAllEventsVocabulary(t *testing.T) {
	events := AllEvents()
	if len(events) != 31 {
		t.Fatalf("AllEvents has %d events, want 31", len(events))
	}
	seen := make(map[string]bool, len(events))
	for _, e := range events {
		if e == "" {
			t.Fatalf("AllEvents contains empty event name")
		}
		if seen[e] {
			t.Fatalf("AllEvents contains duplicate %q", e)
		}
		seen[e] = true
		if !validEvent(RunEventType(e)) {
			t.Fatalf("AllEvents entry %q not valid", e)
		}
	}
	if len(allEvents) != len(events) {
		t.Fatalf("allEvents has %d entries, AllEvents returns %d", len(allEvents), len(events))
	}
}

// TestLeaseRulesAlignWithSchedulableStates 租约簿记规则与可调度状态集一致。
func TestLeaseRulesAlignWithSchedulableStates(t *testing.T) {
	if len(leasableStates) != 7 {
		t.Fatalf("leasableStates has %d states, want 7 (normal 4 + recovery/reconcile/cancel 各 1)", len(leasableStates))
	}
	for _, s := range leasableStates {
		if runstate.SchedulingClassOf(s) == "" {
			t.Fatalf("state %s in leasableStates but not schedulable", s)
		}
		if _, err := NextState(s, EventLeaseAcquired, factsNone); err != nil {
			t.Fatalf("LeaseAcquired from schedulable state %s rejected: %v", s, err)
		}
	}
	// 不可调度状态不得领取/丢失租约（第六篇 §3.1：PLANNING/PLAN_VALIDATING 为"否"，
	// 等待态、暂停态与人工复核态均不可被 Worker 领取）。
	for _, s := range []RunState{runstate.Planning, runstate.PlanValidating, runstate.WaitingInput, runstate.WaitingApproval, runstate.Paused, runstate.ManualReview} {
		if _, err := NextState(s, EventLeaseAcquired, factsNone); err == nil {
			t.Fatalf("LeaseAcquired from non-schedulable state %s accepted, want error", s)
		}
		if _, err := NextState(s, EventLeaseLost, factsNone); err == nil {
			t.Fatalf("LeaseLost from non-schedulable state %s accepted, want error", s)
		}
	}
}
