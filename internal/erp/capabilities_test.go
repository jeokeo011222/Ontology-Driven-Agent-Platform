package erp

import "testing"

func TestCanAutoRetry(t *testing.T) {
	tests := []struct {
		name    string
		level   string
		outcome string
		want    bool
	}{
		// L1：远端原生幂等且已验证，可按契约安全重试。
		{"L1 可重试错误", IdempotencyLevelL1, OutcomeRetryableError, true},
		{"L1 确认未执行", IdempotencyLevelL1, OutcomeNotExecuted, true},
		{"L1 结果未知仍可同键重放", IdempotencyLevelL1, OutcomeUnknown, true},
		{"L1 已成功不重试", IdempotencyLevelL1, OutcomeSucceeded, false},
		{"L1 被拒绝不重试", IdempotencyLevelL1, OutcomeRejected, false},

		// L2：必须先查询确认；仅"已确认未执行"可自动重试。
		{"L2 仅确认未执行可重试", IdempotencyLevelL2, OutcomeNotExecuted, true},
		{"L2 结果未知禁止自动重发", IdempotencyLevelL2, OutcomeUnknown, false},
		{"L2 可重试错误也需先确认", IdempotencyLevelL2, OutcomeRetryableError, false},

		// L3：未知结果禁止自动重发，转人工对账。
		{"L3 未知禁止重发", IdempotencyLevelL3, OutcomeUnknown, false},
		{"L3 确认未执行也禁止", IdempotencyLevelL3, OutcomeNotExecuted, false},
		{"L3 可重试错误也禁止", IdempotencyLevelL3, OutcomeRetryableError, false},

		// 未声明能力按 L3 处理（fail-closed）。
		{"空级别按 L3 处理", "", OutcomeNotExecuted, false},
		{"非法级别按 L3 处理", "L9", OutcomeRetryableError, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanAutoRetry(tc.level, tc.outcome); got != tc.want {
				t.Fatalf("CanAutoRetry(%q, %q) = %v, want %v", tc.level, tc.outcome, got, tc.want)
			}
		})
	}
}

func TestNormalizeLevel(t *testing.T) {
	for in, want := range map[string]string{
		"L1": IdempotencyLevelL1,
		"L2": IdempotencyLevelL2,
		"L3": IdempotencyLevelL3,
		"":   IdempotencyLevelL3,
		"l1": IdempotencyLevelL3,
	} {
		if got := NormalizeLevel(in); got != want {
			t.Fatalf("NormalizeLevel(%q) = %q, want %q", in, got, want)
		}
	}
}
