package erp

// 远端幂等能力分级（第七篇 §8.3）。
const (
	// IdempotencyLevelL1 远端原生支持幂等键且经契约验证，可按契约安全重试。
	IdempotencyLevelL1 = "L1"
	// IdempotencyLevelL2 远端不支持原生幂等，但可可靠查询操作结果，需先查询确认。
	IdempotencyLevelL2 = "L2"
	// IdempotencyLevelL3 既不支持幂等也无法可靠查询结果，未知结果时禁止自动重发。
	IdempotencyLevelL3 = "L3"
)

// 重试判定使用的上次尝试结局分类。
const (
	// OutcomeSucceeded 上次尝试已成功（无需重试）。
	OutcomeSucceeded = "SUCCEEDED"
	// OutcomeRetryableError 上次尝试以明确的、可重试的远端错误结束。
	OutcomeRetryableError = "RETRYABLE_ERROR"
	// OutcomeNotExecuted 已确认远端未执行该操作。
	OutcomeNotExecuted = "NOT_EXECUTED"
	// OutcomeUnknown 当前无法确认远端是否已执行（结果未知）。
	OutcomeUnknown = "UNKNOWN"
	// OutcomeRejected 操作被远端业务明确拒绝。
	OutcomeRejected = "REJECTED"
	// OutcomeTerminalFailure 不可重试的终态失败。
	OutcomeTerminalFailure = "TERMINAL_FAILURE"
)

// NormalizeLevel 把能力声明的幂等级别规范化为 L1/L2/L3；
// 空值或非法值按 L3 处理（fail-closed：不确认的能力一律视为最保守）。
func NormalizeLevel(level string) string {
	switch level {
	case IdempotencyLevelL1, IdempotencyLevelL2, IdempotencyLevelL3:
		return level
	default:
		return IdempotencyLevelL3
	}
}

// CanAutoRetry 判定在当前幂等能力级别下，针对上次结局是否允许自动重发。
//
//   - L1：远端原生幂等且已验证，可按契约安全重试；
//   - L2：只能"先查询确认再决定"——仅当已确认未执行时才允许自动重试；
//   - L3：结果未知时禁止自动重发，转人工对账（返回 false）。
//
// 关键安全约束：UNKNOWN（结果未知）在 L2/L3 下永不自动重发。
func CanAutoRetry(level string, outcome string) bool {
	switch NormalizeLevel(level) {
	case IdempotencyLevelL1:
		switch outcome {
		case OutcomeRetryableError, OutcomeNotExecuted, OutcomeUnknown:
			return true
		default:
			return false
		}
	case IdempotencyLevelL2:
		return outcome == OutcomeNotExecuted
	default: // L3
		return false
	}
}
