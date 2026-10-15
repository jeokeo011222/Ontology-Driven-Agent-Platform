package harness

// TransitionFacts 是状态机判定所需的外部事实快照（第六篇 §14.2）。
//
// 为什么需要它（化解"纯函数 vs 需查外部状态"的矛盾，§14.1/§14.2）：
// 纯函数状态机无法自行查询外部状态——是否存在在途 ERP 操作、租约是否
// 有效、重试预算是否耗尽、取消收敛期限是否已过——而这些条件恰恰决定
// 取消、完成、过期等高风险转换的合法性。解法：由应用服务在同一个事务
// 内计算事实，随事件一并传入状态机并持久化（facts_digest）。状态机因此
// 保持可测试的纯函数性质，同时避免"无条件取消"这类缺陷；任何转换都可
// 重放为"状态 + 事件 + 事实"三元组，供审计与恢复复盘（§14.2 工程约束 2）。
//
// facts 不满足守卫时转换必须被拒绝，并记录 guard_rejected 审计事件
// （§14.2 守卫条件总则）。
type TransitionFacts struct {
	// HasInflightExternalOps 存在 DISPATCHED/EXECUTING/UNKNOWN 的 ERP
	// 操作（第七篇 §7）。为真时禁止直达终态，必须先对账
	// （§14.2 ‖/¶ 守卫与 RunExpired 守卫）。
	HasInflightExternalOps bool

	// HasPendingApproval 审批解决后仍有未决审批层级（多级审批）。
	// ApprovalResolved 后为真表示继续留在 WAITING_APPROVAL。
	HasPendingApproval bool

	// CancelRequested 取消已请求且尚未收敛（持久化 cancel_requested_at）。
	// 即 §14.2 原始 facts.Cancelling 的用户侧等价字段。
	CancelRequested bool

	// CancelConvergenceDeadlinePassed 取消收敛期限已过
	// （cancel_convergence_deadline，§14.2.1 第 4 条）。
	CancelConvergenceDeadlinePassed bool

	// ReconciliationPending 仍有未完成的对账（外部结果未知，§12）。
	ReconciliationPending bool

	// ManualReviewRequested 仍有未解决的人工介入请求。
	ManualReviewRequested bool

	// LeaseValid 事件提交方持有有效租约（§6.3 fencing 的域层表达：
	// 无有效租约不得记录步骤推进，数据库层条件 UPDATE 为最终 fencing）。
	LeaseValid bool

	// AttemptsExhausted 重试/恢复预算耗尽（attempt_count >= max_attempts，
	// 即 §14.2 原始 facts.RetryBudgetLeft 的取反）。
	AttemptsExhausted bool

	// DeadlinePassed 任务总期限 deadline_at 已过（§15.3/§17.1）。
	DeadlinePassed bool
}
