// Package runstate 是全局 Run 状态词表的 Go 绑定。
// 权威词表见 docs/contracts/01-全局状态注册表.md §1；本包只放常量与
// 分类谓词，状态转换矩阵以第六篇 §14.2 为权威，由 internal/harness 实现。
package runstate

// Run 状态全集（17 态，contracts/01 §1.1）。
const (
	Created                = "CREATED"
	ContextBuilding        = "CONTEXT_BUILDING"
	Ready                  = "READY"
	Planning               = "PLANNING"
	PlanValidating         = "PLAN_VALIDATING"
	Running                = "RUNNING"
	WaitingInput           = "WAITING_INPUT"
	WaitingApproval        = "WAITING_APPROVAL"
	Paused                 = "PAUSED"
	Recovering             = "RECOVERING"
	Cancelling             = "CANCELLING"
	RequiresReconciliation = "REQUIRES_RECONCILIATION"
	ManualReview           = "MANUAL_REVIEW"
	Completed              = "COMPLETED"
	Failed                 = "FAILED"
	Cancelled              = "CANCELLED"
	Expired                = "EXPIRED"
)

// All 返回全部合法 Run 状态（顺序即注册表顺序）。
var All = []string{
	Created, ContextBuilding, Ready, Planning, PlanValidating, Running,
	WaitingInput, WaitingApproval, Paused, Recovering, Cancelling,
	RequiresReconciliation, ManualReview, Completed, Failed, Cancelled, Expired,
}

// SchedulingClass 表示调度队列分治（第六篇 §6.2）。
type SchedulingClass string

const (
	ClassRecovery       SchedulingClass = "RECOVERY"
	ClassReconciliation SchedulingClass = "RECONCILIATION"
	ClassCancelling     SchedulingClass = "CANCELLING"
	ClassNormal         SchedulingClass = "NORMAL"
)

// SchedulingClassOf 返回状态对应的调度类；不可调度状态返回空。
//
// 可调度集合严格对齐第六篇 §3.1「Run 状态定义」表的"是否可调度"列：
//   - ClassNormal（执行队列）：CREATED / CONTEXT_BUILDING / READY / RUNNING；
//   - ClassRecovery（恢复队列）：RECOVERING（"仅恢复工作可调度"）；
//   - ClassCancelling（取消收敛队列）：CANCELLING（"仅取消收敛工作可调度"）；
//   - ClassReconciliation（对账队列）：REQUIRES_RECONCILIATION（"仅对账工作可调度"）。
//
// PLANNING 与 PLAN_VALIDATING 在 §3.1 标记为"否"（规划/校验调用正在进行，
// 由持租约的 Worker 继续推进），因此**不进入**领取队列；其租约过期由恢复
// 扫描驱动 → RECOVERING（第六篇 §14.2.3），再由恢复队列接管。
//
// 注意：CREATED / CONTEXT_BUILDING 必须可调度——新建 Run 默认落在 CREATED，
// 若不可领取则无人能把 ContextBuildStarted 推进下去，Run 将永久卡死。
func SchedulingClassOf(state string) SchedulingClass {
	switch state {
	case Recovering:
		return ClassRecovery
	case RequiresReconciliation:
		return ClassReconciliation
	case Cancelling:
		return ClassCancelling
	case Created, ContextBuilding, Ready, Running:
		return ClassNormal
	default:
		return ""
	}
}

// IsTerminal 报告状态是否为终态（不再接受任何转换）。
func IsTerminal(state string) bool {
	switch state {
	case Completed, Failed, Cancelled, Expired:
		return true
	default:
		return false
	}
}

// IsWaiting 报告状态是否为等待态（有超时升级链，第六篇 §15.4）。
func IsWaiting(state string) bool {
	switch state {
	case WaitingInput, WaitingApproval:
		return true
	default:
		return false
	}
}

// Valid 报告是否为已登记状态。
func Valid(state string) bool {
	for _, s := range All {
		if s == state {
			return true
		}
	}
	return false
}
