package harness

import (
	"odap/internal/platform/errors"
	"odap/internal/platform/runstate"
)

// RunState 是 Run 状态。类型别名直接绑定 platform/runstate 的词表常量，
// 杜绝状态词表分叉（contracts/01：状态词表唯一真源，各层只允许引用）。
type RunState = string

// rule 是一条转换规则：(from, event) 命中后由 resolve 依据 facts 计算
// 目标状态；resolve 为 nil 表示无条件转换至 to。
//
// resolve 返回 (next, true) 表示放行；返回 (_, false) 表示守卫拒绝——
// 调用方必须按 guard_rejected 记录审计事件，且不得落盘（§14.2 守卫总则）。
type rule struct {
	from    RunState
	event   RunEventType
	to      RunState // 主目标（矩阵格目标，TransitionTable 展示用）
	resolve func(TransitionFacts) (RunState, bool)
	guard   string // 守卫说明（文档与 CI 核对用）
}

// nextOf 计算规则在 facts 下的目标状态。
func (r rule) nextOf(f TransitionFacts) (RunState, bool) {
	if r.resolve == nil {
		return r.to, true
	}
	return r.resolve(f)
}

// 复用守卫说明（TransitionTable 的 GuardDesc 来源）。
const (
	// guardCancelEntry 取消统一入口（铁律③）：任意非终态 + CancellationRequested → CANCELLING。
	guardCancelEntry = "取消统一入口（铁律③）：任意非终态均可进入 CANCELLING，不要求在途操作为空"
	// guardCancelEntryIdempotent CANCELLING 内重复取消请求（铁律③：任意非终态，含 CANCELLING 自身）。
	guardCancelEntryIdempotent = "取消统一入口（铁律③）：CANCELLING 内重复取消请求为幂等自环"
	// guardRunExpired RunExpired 守卫（§14.2）：过期不得跳过对账。
	guardRunExpired = "HasInflightExternalOps 为真时先转 REQUIRES_RECONCILIATION（§14.2：过期不得直达终态，定时器只能先发 ExternalOperationUnknown）"
	// guardPlanValidationFailed †‡ 合并守卫（用户事件集无 PlanRegenerationRequested）。
	guardPlanValidationFailed = "AttemptsExhausted 时 FAILED（† RetryBudgetLeft=false）；否则 READY 重新规划（‡，PlanRegenerationRequested 折叠进本守卫）"
	// guardStepLease 步骤推进的租约守卫（§6.3 域层 fencing）。
	guardStepLease = "LeaseValid 必须为真（§6.3：无有效租约不得记录步骤推进；DB 层条件 UPDATE 为最终 fencing）"
	// guardStepFailed § 守卫：Run 层保持 RUNNING，重试语义在 Step 状态机。
	guardStepFailed = "§：Run 层保持 RUNNING（重试语义在 Step 状态机处理，仅当应用服务判定不可继续才发 RunFailed）；另需 LeaseValid"
	// guardRunCompleted ¶ 守卫（用户 facts 未含 AllStepsTerminal 等字段）。
	guardRunCompleted = "HasInflightExternalOps 必须为假；AllStepsTerminal/HasUnknownToolCalls/OutputContractValidated 由应用服务发事件前校验（§14.2 ¶，RunCompleted 必须由经过检查的应用服务生成，§14.2.3）"
	// guardWaitingTimeout 等待态超时升级（铁律② + ‖ 守卫）。
	guardWaitingTimeout = "HasInflightExternalOps 先转 REQUIRES_RECONCILIATION（§14.2 ‖：超时不得直达终态）；AttemptsExhausted 转 EXPIRED；否则 MANUAL_REVIEW（铁律②）"
	// guardWaitingFailed 等待态失败（§14.2 ‖ 守卫）。
	guardWaitingFailed = "HasInflightExternalOps 为真时先转 REQUIRES_RECONCILIATION（§14.2 ‖：禁止直达 FAILED）"
	// guardApprovalResolved 批准/拒绝由 facts 区分。
	guardApprovalResolved = "HasPendingApproval 为真时保持 WAITING_APPROVAL（多级审批尚有未决层级）；批准/拒绝由 facts 区分——拒绝路径由应用服务发 RunFailed（§14.2 ‖）"
	// guardRunResumed facts 无 ResumeState 字段的补全决策。
	guardRunResumed = "facts 未含 ResumeState 字段，统一恢复至 RUNNING；应用服务如需回到其他状态，应在恢复路径中另行处理"
	// guardRecoveryFailed 铁律⑤（用户事件集无 RecoveryFailed，失败经 RunFailed 表达）。
	guardRecoveryFailed = "AttemptsExhausted 时 FAILED；否则保持 RECOVERING 等待恢复重试（铁律⑤；用户事件集无 RecoveryFailed，恢复失败经 RunFailed 表达）"
	// guardRecoverySucceeded 铁律⑤。
	guardRecoverySucceeded = "铁律⑤：RECOVERING + RecoverySucceeded → RUNNING"
	// guardCancelConverged 铁律③：离开 CANCELLING 必须在途检查。
	guardCancelConverged = "HasInflightExternalOps 为真时转 REQUIRES_RECONCILIATION（铁律③：离开 CANCELLING 必须经过在途操作检查）"
	// guardCancellingRecon §14.2 ‖注：对账收敛于取消。
	guardCancellingRecon = "仅 CancelRequested 为真时合法（§14.2 ‖注：ReconciliationSucceeded → CANCELLED 仅在 facts.Cancelling 为真时合法）；否则守卫拒绝"
	// guardCancelTimeout 铁律③：取消收敛超时升级。
	guardCancelTimeout = "CancelConvergenceDeadlinePassed 为真时升级 MANUAL_REVIEW 并告警（铁律③ / §14.2.1 第 4 条）；否则守卫拒绝"
	// guardCancellingManualReview 矩阵原样（CANCELLING + ManualReviewResolved）。
	guardCancellingManualReview = "矩阵原样：取消收敛期间的人工复核解决仍留 MANUAL_REVIEW（自环）"
	// guardReconSucceeded §14.2 ‖注 + ¶ 分支缺失说明。
	guardReconSucceeded = "CancelRequested 为真时 → CANCELLED（§14.2 ‖注：对账收敛于取消）；¶ 的 COMPLETED 分支因 facts 缺少 AllStepsTerminal 等字段，由应用服务在 RUNNING 续跑后另发 RunCompleted 表达"
	// guardManualReviewResolved 三出口由 facts 选择（文档为 RUNNING/FAILED/CANCELLED）。
	guardManualReviewResolved = "ManualReviewRequested/ReconciliationPending 为真时守卫拒绝（仍有未决事项）；CancelRequested → CANCELLED；DeadlinePassed → FAILED（超过总期限不得恢复推进）；否则 RUNNING"
	// guardLeaseBookkeep 租约簿记（自环）。
	guardLeaseBookkeep = "租约簿记事件：仅可租用状态（SchedulingClassOf 非空，§6.2 四队列）合法，自环不改变状态"
	// guardLeaseLostIdle 非 RUNNING 的租约丢失（簿记自环）。
	guardLeaseLostIdle = "非 RUNNING 的可租用状态租约丢失仅簿记（自环），由调度器重新领取；另要求 LeaseValid 为假（租约仍有效不得宣称为丢失）"
	// guardLeaseLostRunning RUNNING 租约丢失必须恢复。
	guardLeaseLostRunning = "RUNNING 租约丢失必须转 RECOVERING（§14.2.3 租约过期扫描 → RecoveryStarted）；另要求 LeaseValid 为假"
	// guardDataRefreshed 数据刷新簿记（自环）。
	guardDataRefreshed = "数据刷新簿记事件（自环）：Worker 刷新上下文/依赖摘要后继续推进；依赖漂移检测见 checkpoint.VerifyCheckpoint"
	// guardExternalUnknown 铁律④。
	guardExternalUnknown = "铁律④：RUNNING 发现结果未知的外部操作必须转 REQUIRES_RECONCILIATION"
	// guardReconFailed 铁律④。
	guardReconFailed = "铁律④：REQUIRES_RECONCILIATION + ReconciliationFailed → MANUAL_REVIEW"
)

// leasableStates 是可被调度器领取租约的状态集合（SchedulingClassOf 非空，
// §6.2 四队列：normal/recovery/reconcile/cancel）。
var leasableStates = func() []RunState {
	var out []RunState
	for _, s := range runstate.All {
		if runstate.SchedulingClassOf(s) != "" {
			out = append(out, s)
		}
	}
	return out
}()

// leaseRules 是租约簿记转换（§6.2/§6.3）：LeaseAcquired 自环；LeaseLost
// 从 RUNNING 转 RECOVERING（中断的活动执行必须恢复），其余可租用状态自环
// （等待重新领取，避免 facts 缺少 ResumeState 时恢复后跳错状态）。
var leaseRules = func() []rule {
	var out []rule
	for _, s := range leasableStates {
		out = append(out, rule{from: s, event: EventLeaseAcquired, to: s, guard: guardLeaseBookkeep})
		if s == runstate.Running {
			out = append(out, rule{
				from: s, event: EventLeaseLost, to: runstate.Recovering,
				resolve: resolveLeaseLost(runstate.Recovering), guard: guardLeaseLostRunning,
			})
			continue
		}
		out = append(out, rule{
			from: s, event: EventLeaseLost, to: s,
			resolve: resolveLeaseLost(s), guard: guardLeaseLostIdle,
		})
	}
	return out
}()

// rules 是转换矩阵的表驱动实现，与第六篇 §14.2 矩阵一一对应
// （§14.2 工程约束 1：矩阵新增一行而实现未登记时测试必须失败）。
//
// 矩阵为权威；用户事件集（31 个）与文档 §3.3 原始清单的差异及补全决策
// 见 events.go 头部注释与各条 guard 说明。
var rules = append([]rule{
	// ---- CREATED（铁律①：只接受 ContextBuildStarted 起步；矩阵另含取消与过期出口） ----
	{from: runstate.Created, event: EventContextBuildStarted, to: runstate.ContextBuilding},
	{from: runstate.Created, event: EventCancellationRequested, to: runstate.Cancelling, guard: guardCancelEntry},
	{from: runstate.Created, event: EventRunExpired, to: runstate.Expired, resolve: resolveRunExpired, guard: guardRunExpired},

	// ---- CONTEXT_BUILDING ----
	{from: runstate.ContextBuilding, event: EventContextBuildSucceeded, to: runstate.Ready},
	{from: runstate.ContextBuilding, event: EventContextBuildFailed, to: runstate.Failed},
	{from: runstate.ContextBuilding, event: EventRunPaused, to: runstate.Paused},
	{from: runstate.ContextBuilding, event: EventCancellationRequested, to: runstate.Cancelling, guard: guardCancelEntry},
	{from: runstate.ContextBuilding, event: EventRecoveryStarted, to: runstate.Recovering},
	{from: runstate.ContextBuilding, event: EventExternalOperationUnknown, to: runstate.RequiresReconciliation},
	{from: runstate.ContextBuilding, event: EventRunFailed, to: runstate.Failed},
	{from: runstate.ContextBuilding, event: EventRunExpired, to: runstate.Expired, resolve: resolveRunExpired, guard: guardRunExpired},

	// ---- READY ----
	{from: runstate.Ready, event: EventPlanGenerated, to: runstate.Planning},
	{from: runstate.Ready, event: EventRunPaused, to: runstate.Paused},
	{from: runstate.Ready, event: EventCancellationRequested, to: runstate.Cancelling, guard: guardCancelEntry},
	{from: runstate.Ready, event: EventRecoveryStarted, to: runstate.Recovering},
	{from: runstate.Ready, event: EventRunFailed, to: runstate.Failed},
	{from: runstate.Ready, event: EventRunExpired, to: runstate.Expired, resolve: resolveRunExpired, guard: guardRunExpired},
	{from: runstate.Ready, event: EventDataRefreshed, to: runstate.Ready, guard: guardDataRefreshed},

	// ---- PLANNING ----
	{from: runstate.Planning, event: EventPlanValidationSucceeded, to: runstate.PlanValidating},
	{from: runstate.Planning, event: EventRunPaused, to: runstate.Paused},
	{from: runstate.Planning, event: EventCancellationRequested, to: runstate.Cancelling, guard: guardCancelEntry},
	{from: runstate.Planning, event: EventRecoveryStarted, to: runstate.Recovering},
	{from: runstate.Planning, event: EventRunFailed, to: runstate.Failed},
	{from: runstate.Planning, event: EventRunExpired, to: runstate.Expired, resolve: resolveRunExpired, guard: guardRunExpired},

	// ---- PLAN_VALIDATING ----
	{from: runstate.PlanValidating, event: EventPlanValidationSucceeded, to: runstate.Running},
	{from: runstate.PlanValidating, event: EventPlanValidationFailed, to: runstate.Ready, resolve: resolvePlanValidationFailed, guard: guardPlanValidationFailed},
	{from: runstate.PlanValidating, event: EventRunPaused, to: runstate.Paused},
	{from: runstate.PlanValidating, event: EventCancellationRequested, to: runstate.Cancelling, guard: guardCancelEntry},
	{from: runstate.PlanValidating, event: EventRecoveryStarted, to: runstate.Recovering},
	{from: runstate.PlanValidating, event: EventRunFailed, to: runstate.Failed},
	{from: runstate.PlanValidating, event: EventRunExpired, to: runstate.Expired, resolve: resolveRunExpired, guard: guardRunExpired},

	// ---- RUNNING ----
	{from: runstate.Running, event: EventStepStarted, to: runstate.Running, resolve: resolveStepProgress, guard: guardStepLease},
	{from: runstate.Running, event: EventStepSucceeded, to: runstate.Running, resolve: resolveStepProgress, guard: guardStepLease},
	{from: runstate.Running, event: EventStepFailed, to: runstate.Running, resolve: resolveStepProgress, guard: guardStepFailed},
	{from: runstate.Running, event: EventInputRequested, to: runstate.WaitingInput},
	{from: runstate.Running, event: EventApprovalRequested, to: runstate.WaitingApproval},
	{from: runstate.Running, event: EventRunPaused, to: runstate.Paused},
	{from: runstate.Running, event: EventCancellationRequested, to: runstate.Cancelling, guard: guardCancelEntry},
	{from: runstate.Running, event: EventRecoveryStarted, to: runstate.Recovering},
	{from: runstate.Running, event: EventExternalOperationUnknown, to: runstate.RequiresReconciliation, guard: guardExternalUnknown},
	{from: runstate.Running, event: EventManualInterventionRequired, to: runstate.ManualReview},
	{from: runstate.Running, event: EventRunCompleted, to: runstate.Completed, resolve: resolveRunCompleted, guard: guardRunCompleted},
	{from: runstate.Running, event: EventRunFailed, to: runstate.Failed},
	{from: runstate.Running, event: EventRunExpired, to: runstate.Expired, resolve: resolveRunExpired, guard: guardRunExpired},
	{from: runstate.Running, event: EventDataRefreshed, to: runstate.Running, guard: guardDataRefreshed},

	// ---- WAITING_INPUT ----
	{from: runstate.WaitingInput, event: EventInputReceived, to: runstate.Running},
	{from: runstate.WaitingInput, event: EventTimeoutEscalated, to: runstate.ManualReview, resolve: resolveWaitingTimeout, guard: guardWaitingTimeout},
	{from: runstate.WaitingInput, event: EventRunPaused, to: runstate.Paused},
	{from: runstate.WaitingInput, event: EventCancellationRequested, to: runstate.Cancelling, guard: guardCancelEntry},
	{from: runstate.WaitingInput, event: EventRecoveryStarted, to: runstate.Recovering},
	{from: runstate.WaitingInput, event: EventExternalOperationUnknown, to: runstate.RequiresReconciliation},
	{from: runstate.WaitingInput, event: EventManualInterventionRequired, to: runstate.ManualReview},
	{from: runstate.WaitingInput, event: EventRunFailed, to: runstate.Failed, resolve: resolveWaitingFailed, guard: guardWaitingFailed},
	{from: runstate.WaitingInput, event: EventRunExpired, to: runstate.Expired, resolve: resolveRunExpired, guard: guardRunExpired},

	// ---- WAITING_APPROVAL ----
	{from: runstate.WaitingApproval, event: EventApprovalResolved, to: runstate.Running, resolve: resolveApprovalResolved, guard: guardApprovalResolved},
	{from: runstate.WaitingApproval, event: EventTimeoutEscalated, to: runstate.ManualReview, resolve: resolveWaitingTimeout, guard: guardWaitingTimeout},
	{from: runstate.WaitingApproval, event: EventRunPaused, to: runstate.Paused},
	{from: runstate.WaitingApproval, event: EventCancellationRequested, to: runstate.Cancelling, guard: guardCancelEntry},
	{from: runstate.WaitingApproval, event: EventRecoveryStarted, to: runstate.Recovering},
	{from: runstate.WaitingApproval, event: EventExternalOperationUnknown, to: runstate.RequiresReconciliation},
	{from: runstate.WaitingApproval, event: EventManualInterventionRequired, to: runstate.ManualReview},
	{from: runstate.WaitingApproval, event: EventRunFailed, to: runstate.Failed, resolve: resolveWaitingFailed, guard: guardWaitingFailed},
	{from: runstate.WaitingApproval, event: EventRunExpired, to: runstate.Expired, resolve: resolveRunExpired, guard: guardRunExpired},

	// ---- PAUSED ----
	{from: runstate.Paused, event: EventRunResumed, to: runstate.Running, guard: guardRunResumed},
	{from: runstate.Paused, event: EventCancellationRequested, to: runstate.Cancelling, guard: guardCancelEntry},
	{from: runstate.Paused, event: EventRecoveryStarted, to: runstate.Recovering},
	{from: runstate.Paused, event: EventManualInterventionRequired, to: runstate.ManualReview},
	{from: runstate.Paused, event: EventRunFailed, to: runstate.Failed},
	{from: runstate.Paused, event: EventRunExpired, to: runstate.Expired, resolve: resolveRunExpired, guard: guardRunExpired},

	// ---- RECOVERING ----
	{from: runstate.Recovering, event: EventRunPaused, to: runstate.Paused},
	{from: runstate.Recovering, event: EventCancellationRequested, to: runstate.Cancelling, guard: guardCancelEntry},
	{from: runstate.Recovering, event: EventRecoverySucceeded, to: runstate.Running, guard: guardRecoverySucceeded},
	{from: runstate.Recovering, event: EventExternalOperationUnknown, to: runstate.RequiresReconciliation},
	{from: runstate.Recovering, event: EventManualInterventionRequired, to: runstate.ManualReview},
	{from: runstate.Recovering, event: EventRunCompleted, to: runstate.Completed, resolve: resolveRunCompleted, guard: guardRunCompleted},
	{from: runstate.Recovering, event: EventRunFailed, to: runstate.Recovering, resolve: resolveRecoveryFailed, guard: guardRecoveryFailed},

	// ---- CANCELLING ----
	{from: runstate.Cancelling, event: EventRunPaused, to: runstate.Paused},
	{from: runstate.Cancelling, event: EventCancellationRequested, to: runstate.Cancelling, guard: guardCancelEntryIdempotent},
	{from: runstate.Cancelling, event: EventCancellationConverged, to: runstate.Cancelled, resolve: resolveCancelConverged, guard: guardCancelConverged},
	{from: runstate.Cancelling, event: EventExternalOperationUnknown, to: runstate.RequiresReconciliation},
	{from: runstate.Cancelling, event: EventReconciliationSucceeded, to: runstate.Cancelled, resolve: resolveCancellingReconSucceeded, guard: guardCancellingRecon},
	{from: runstate.Cancelling, event: EventReconciliationFailed, to: runstate.ManualReview},
	{from: runstate.Cancelling, event: EventManualInterventionRequired, to: runstate.ManualReview},
	{from: runstate.Cancelling, event: EventManualReviewResolved, to: runstate.ManualReview, guard: guardCancellingManualReview},
	{from: runstate.Cancelling, event: EventTimeoutEscalated, to: runstate.ManualReview, resolve: resolveCancelTimeout, guard: guardCancelTimeout},

	// ---- REQUIRES_RECONCILIATION ----
	{from: runstate.RequiresReconciliation, event: EventCancellationRequested, to: runstate.Cancelling, guard: guardCancelEntry},
	{from: runstate.RequiresReconciliation, event: EventRecoveryStarted, to: runstate.Recovering},
	{from: runstate.RequiresReconciliation, event: EventReconciliationSucceeded, to: runstate.Running, resolve: resolveReconSucceeded, guard: guardReconSucceeded},
	{from: runstate.RequiresReconciliation, event: EventReconciliationFailed, to: runstate.ManualReview, guard: guardReconFailed},
	{from: runstate.RequiresReconciliation, event: EventManualInterventionRequired, to: runstate.ManualReview},
	{from: runstate.RequiresReconciliation, event: EventRunCompleted, to: runstate.Completed, resolve: resolveRunCompleted, guard: guardRunCompleted},
	{from: runstate.RequiresReconciliation, event: EventRunFailed, to: runstate.Failed},

	// ---- MANUAL_REVIEW ----
	{from: runstate.ManualReview, event: EventCancellationRequested, to: runstate.Cancelling, guard: guardCancelEntry},
	{from: runstate.ManualReview, event: EventManualReviewResolved, to: runstate.Running, resolve: resolveManualReviewResolved, guard: guardManualReviewResolved},
}, leaseRules...)

// ---- 守卫解析函数 ----

// resolveRunExpired 过期转换：有在途外部操作时不得直达终态（§14.2 RunExpired 守卫）。
func resolveRunExpired(f TransitionFacts) (RunState, bool) {
	if f.HasInflightExternalOps {
		return runstate.RequiresReconciliation, true
	}
	return runstate.Expired, true
}

// resolvePlanValidationFailed †‡：预算耗尽判失败，否则回退 READY 重新规划。
func resolvePlanValidationFailed(f TransitionFacts) (RunState, bool) {
	if f.AttemptsExhausted {
		return runstate.Failed, true
	}
	return runstate.Ready, true
}

// resolveStepProgress 步骤推进：无有效租约不得记录（§6.3 域层 fencing）。
func resolveStepProgress(f TransitionFacts) (RunState, bool) {
	if !f.LeaseValid {
		return runstate.Running, false
	}
	return runstate.Running, true
}

// resolveRunCompleted ¶：存在在途外部操作时禁止完成。
func resolveRunCompleted(f TransitionFacts) (RunState, bool) {
	if f.HasInflightExternalOps {
		return runstate.Running, false
	}
	return runstate.Completed, true
}

// resolveWaitingTimeout 等待态超时升级（铁律② + ‖ 守卫）。
func resolveWaitingTimeout(f TransitionFacts) (RunState, bool) {
	if f.HasInflightExternalOps {
		return runstate.RequiresReconciliation, true
	}
	if f.AttemptsExhausted {
		return runstate.Expired, true
	}
	return runstate.ManualReview, true
}

// resolveWaitingFailed 等待态失败（§14.2 ‖ 守卫）。
func resolveWaitingFailed(f TransitionFacts) (RunState, bool) {
	if f.HasInflightExternalOps {
		return runstate.RequiresReconciliation, true
	}
	return runstate.Failed, true
}

// resolveApprovalResolved 批准/拒绝由 facts 区分：仍有未决审批层级则继续等待。
func resolveApprovalResolved(f TransitionFacts) (RunState, bool) {
	if f.HasPendingApproval {
		return runstate.WaitingApproval, true
	}
	return runstate.Running, true
}

// resolveRecoveryFailed 铁律⑤：预算耗尽判失败，否则保持恢复。
func resolveRecoveryFailed(f TransitionFacts) (RunState, bool) {
	if f.AttemptsExhausted {
		return runstate.Failed, true
	}
	return runstate.Recovering, true
}

// resolveCancelConverged 铁律③：离开 CANCELLING 必须经过在途操作检查。
func resolveCancelConverged(f TransitionFacts) (RunState, bool) {
	if f.HasInflightExternalOps {
		return runstate.RequiresReconciliation, true
	}
	return runstate.Cancelled, true
}

// resolveCancellingReconSucceeded §14.2 ‖注：对账收敛于取消需 CancelRequested 为真。
func resolveCancellingReconSucceeded(f TransitionFacts) (RunState, bool) {
	if !f.CancelRequested {
		return runstate.Cancelling, false
	}
	return runstate.Cancelled, true
}

// resolveCancelTimeout 铁律③：取消收敛超时且期限已过才升级人工复核。
func resolveCancelTimeout(f TransitionFacts) (RunState, bool) {
	if !f.CancelConvergenceDeadlinePassed {
		return runstate.Cancelling, false
	}
	return runstate.ManualReview, true
}

// resolveReconSucceeded 铁律④ + ‖注：对账成功按取消请求决定去向。
func resolveReconSucceeded(f TransitionFacts) (RunState, bool) {
	if f.CancelRequested {
		return runstate.Cancelled, true
	}
	return runstate.Running, true
}

// resolveManualReviewResolved 三出口（RUNNING/FAILED/CANCELLED）由 facts 选择。
func resolveManualReviewResolved(f TransitionFacts) (RunState, bool) {
	if f.ManualReviewRequested || f.ReconciliationPending {
		return runstate.ManualReview, false
	}
	if f.CancelRequested {
		return runstate.Cancelled, true
	}
	if f.DeadlinePassed {
		return runstate.Failed, true
	}
	return runstate.Running, true
}

// resolveLeaseLost 租约丢失：租约仍有效时不得宣称为丢失。
func resolveLeaseLost(to RunState) func(TransitionFacts) (RunState, bool) {
	return func(f TransitionFacts) (RunState, bool) {
		if f.LeaseValid {
			return to, false
		}
		return to, true
	}
}

// NextState 计算 (current, event) 在 facts 下的目标状态（纯函数，不触库）。
//
// 覆盖 17 态 × 31 事件的合法转换（权威：第六篇 §14.2 矩阵；补全铁律见
// 各条 guard 说明与文件末尾注释）。非法 (state, event) 组合返回
// CodeExecInvalidTransition；守卫不满足同样返回 CodeExecInvalidTransition
// （guard rejected），调用方不得落盘。
//
// 终态（COMPLETED/FAILED/CANCELLED/EXPIRED）不接受任何事件；重新发起必须
// 创建新 Run 并通过幂等键关联旧 Run（§14.2 工程约束 3）。
func NextState(current RunState, event RunEventType, facts TransitionFacts) (RunState, error) {
	// 未登记状态（含 QUEUED 等历史词表，contracts/01 §1.1）、未登记事件、
	// 终态（铁律⑦）一律拒绝。
	if !runstate.Valid(current) || !validEvent(event) || runstate.IsTerminal(current) {
		return current, errors.Ef(errors.CodeExecInvalidTransition, "state=%s event=%s", current, event)
	}
	for _, r := range rules {
		if r.from == current && r.event == event {
			next, ok := r.nextOf(facts)
			if !ok {
				return current, errors.Ef(errors.CodeExecInvalidTransition,
					"state=%s event=%s guard rejected: %s", current, event, r.guard)
			}
			return next, nil
		}
	}
	return current, errors.Ef(errors.CodeExecInvalidTransition, "state=%s event=%s", current, event)
}

// Transition 是转换矩阵的文档/CI 视图，与 rules 一一对应。
type Transition struct {
	From      string
	Event     string
	To        string
	GuardDesc string
}

// TransitionTable 返回全部合法转换（含守卫说明），供文档核对与 CI 断言
// （§14.2 工程约束 1：transitions 表与矩阵必须一一对应）。
//
// To 为矩阵主目标（零 facts 下的默认落点）；条件分支见 GuardDesc。
func TransitionTable() []Transition {
	out := make([]Transition, 0, len(rules))
	for _, r := range rules {
		out = append(out, Transition{
			From:      r.from,
			Event:     string(r.event),
			To:        r.to,
			GuardDesc: r.guard,
		})
	}
	return out
}
