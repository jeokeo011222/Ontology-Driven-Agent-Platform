// Package harness 实现 ODAP 执行引擎（Harness）：Run 状态机、租约调度、
// 检查点与恢复的纯领域逻辑及其 PostgreSQL 持久化。
//
// 权威来源：
//   - 状态词表（17 态）：docs/contracts/01-全局状态注册表.md §1
//   - 事件清单与转换矩阵：docs/第六篇 §3.3 / §14.2
//   - 调度四队列与 fencing：docs/第六篇 §6.2 / §6.3
//
// 核心设计约束（第六篇 §14.1/§14.2）：状态机是纯函数组件，不直接调用
// 模型、工具或 ERP；取消、完成、过期等判定所需的外部事实由应用服务在
// 同一事务内预先计算，经 TransitionFacts 传入（见 facts.go）。状态更新
// 必须经 Machine.Apply 统一完成，禁止 Agent 或 Tool 直接写
// agent_runs.status（§3.3）。
package harness

// RunEventType 是驱动 Run 状态转换的领域事件类型。
// 事件名不是状态名，两层词表分开维护（contracts/01 §5.4）。
type RunEventType string

// Run 事件全集（31 个，与 docs/contracts/01 事件表及第六篇 §3.3 对齐）。
//
// 与第六篇 §3.3/§14.1 原始清单的差异（本包为权威落地，差异均有出处）：
//   - CancelConverged 统一命名为 CancellationConverged；
//   - RecoveryCompleted 统一命名为 RecoverySucceeded；
//   - InputDeadlineExceeded / ApprovalExpired 合并为 TimeoutEscalated
//     （等待态超时统一入口，见 state.go 铁律②注释）；
//   - PlanRegenerationRequested 不在本事件集：其语义（‡ 守卫）折叠进
//     PlanValidationFailed 的守卫分支；
//   - ApprovalRejected 不在本事件集：审批拒绝路径由应用服务发 RunFailed
//     表达（§14.2 ‖ 守卫）。
//
// Run 事件常量全集。新增事件必须同步更新：本常量集、allEvents、
// state.go 转换矩阵与契约测试（§3.3 一致性要求：
// "事件清单 = 常量集合 = 矩阵覆盖集合"）。
const (
	EventContextBuildStarted        RunEventType = "ContextBuildStarted"
	EventContextBuildSucceeded      RunEventType = "ContextBuildSucceeded"
	EventContextBuildFailed         RunEventType = "ContextBuildFailed"
	EventPlanGenerated              RunEventType = "PlanGenerated"
	EventPlanValidationSucceeded    RunEventType = "PlanValidationSucceeded"
	EventPlanValidationFailed       RunEventType = "PlanValidationFailed"
	EventStepStarted                RunEventType = "StepStarted"
	EventStepSucceeded              RunEventType = "StepSucceeded"
	EventStepFailed                 RunEventType = "StepFailed"
	EventInputRequested             RunEventType = "InputRequested"
	EventInputReceived              RunEventType = "InputReceived"
	EventApprovalRequested          RunEventType = "ApprovalRequested"
	EventApprovalResolved           RunEventType = "ApprovalResolved"
	EventRunPaused                  RunEventType = "RunPaused"
	EventRunResumed                 RunEventType = "RunResumed"
	EventCancellationRequested      RunEventType = "CancellationRequested"
	EventCancellationConverged      RunEventType = "CancellationConverged"
	EventExternalOperationUnknown   RunEventType = "ExternalOperationUnknown"
	EventReconciliationSucceeded    RunEventType = "ReconciliationSucceeded"
	EventReconciliationFailed       RunEventType = "ReconciliationFailed"
	EventManualInterventionRequired RunEventType = "ManualInterventionRequired"
	EventManualReviewResolved       RunEventType = "ManualReviewResolved"
	EventRunCompleted               RunEventType = "RunCompleted"
	EventRunFailed                  RunEventType = "RunFailed"
	EventRunExpired                 RunEventType = "RunExpired"
	EventRecoveryStarted            RunEventType = "RecoveryStarted"
	EventRecoverySucceeded          RunEventType = "RecoverySucceeded"
	EventLeaseAcquired              RunEventType = "LeaseAcquired"
	EventLeaseLost                  RunEventType = "LeaseLost"
	EventTimeoutEscalated           RunEventType = "TimeoutEscalated"
	EventDataRefreshed              RunEventType = "DataRefreshed"
)

// allEvents 是事件全集（顺序即注册顺序，契约测试按此断言数量与唯一性）。
var allEvents = []RunEventType{
	EventContextBuildStarted,
	EventContextBuildSucceeded,
	EventContextBuildFailed,
	EventPlanGenerated,
	EventPlanValidationSucceeded,
	EventPlanValidationFailed,
	EventStepStarted,
	EventStepSucceeded,
	EventStepFailed,
	EventInputRequested,
	EventInputReceived,
	EventApprovalRequested,
	EventApprovalResolved,
	EventRunPaused,
	EventRunResumed,
	EventCancellationRequested,
	EventCancellationConverged,
	EventExternalOperationUnknown,
	EventReconciliationSucceeded,
	EventReconciliationFailed,
	EventManualInterventionRequired,
	EventManualReviewResolved,
	EventRunCompleted,
	EventRunFailed,
	EventRunExpired,
	EventRecoveryStarted,
	EventRecoverySucceeded,
	EventLeaseAcquired,
	EventLeaseLost,
	EventTimeoutEscalated,
	EventDataRefreshed,
}

// AllEvents 返回全部合法 Run 事件名（31 个）。
// 权威来源：docs/contracts/01 事件表 / 第六篇 §3.3；契约测试断言
// "事件清单 = 常量集合 = 矩阵覆盖集合"三者一致（§19.1）。
func AllEvents() []string {
	out := make([]string, len(allEvents))
	for i, e := range allEvents {
		out[i] = string(e)
	}
	return out
}

// validEvent 报告事件是否为已登记事件（未登记事件一律按非法转换拒绝）。
func validEvent(e RunEventType) bool {
	for _, x := range allEvents {
		if x == e {
			return true
		}
	}
	return false
}
