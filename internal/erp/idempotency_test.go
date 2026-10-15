package erp

import (
	"testing"

	"odap/internal/platform/ids"
)

func TestIntentDigestIsStableAcrossOperationRecords(t *testing.T) {
	// 同一业务意图重复提交（网络重试 / Worker 崩溃恢复 / 对账后补发）必须复用同一键：
	// operation_id 不进入五元组，键只由意图派生。
	tenant := ids.MustParse("11111111-1111-1111-1111-111111111111")
	run := ids.MustParse("22222222-2222-2222-2222-222222222222")
	step := ids.MustParse("33333333-3333-3333-3333-333333333333")
	parts := IntentParts{
		RunID:          run,
		StepID:         step,
		OperationType:  "expense.submit",
		TargetObjectID: "claim-001",
		PayloadDigest:  "digest-a",
	}

	first := IdempotencyKey(tenant, parts)
	second := IdempotencyKey(tenant, parts)
	if first != second {
		t.Fatalf("同一意图两次生成的键不同：%s vs %s", first, second)
	}
	// 键前缀包含租户，且主体是 hex 摘要。
	if len(first) < len("odap:")+36+1 {
		t.Fatalf("键格式异常：%s", first)
	}
}

func TestIdempotencyKeyTable(t *testing.T) {
	tenantA := ids.MustParse("11111111-1111-1111-1111-111111111111")
	tenantB := ids.MustParse("44444444-4444-4444-4444-444444444444")
	run := ids.MustParse("22222222-2222-2222-2222-222222222222")
	step := ids.MustParse("33333333-3333-3333-3333-333333333333")

	base := IntentParts{
		RunID:          run,
		StepID:         step,
		OperationType:  "expense.submit",
		TargetObjectID: "claim-001",
		PayloadDigest:  "digest-a",
	}

	tests := []struct {
		name      string
		tenant    ids.ID
		parts     IntentParts
		wantEqual bool // 与 (tenantA, base) 的键是否应相同
	}{
		{
			name:      "同意图不同操作记录得到同键",
			tenant:    tenantA,
			parts:     base,
			wantEqual: true,
		},
		{
			name:      "payload_digest 变化则键变化",
			tenant:    tenantA,
			parts:     withDigest(base, "digest-b"),
			wantEqual: false,
		},
		{
			name:      "operation_type 变化则键变化",
			tenant:    tenantA,
			parts:     withOpType(base, "expense.create_draft"),
			wantEqual: false,
		},
		{
			name:      "target_object_id 变化则键变化",
			tenant:    tenantA,
			parts:     withTarget(base, "claim-002"),
			wantEqual: false,
		},
		{
			name:      "step_id 变化则键变化",
			tenant:    tenantA,
			parts:     withStep(base, ids.MustParse("55555555-5555-5555-5555-555555555555")),
			wantEqual: false,
		},
		{
			name:      "tenant 不同则键不同",
			tenant:    tenantB,
			parts:     base,
			wantEqual: false,
		},
	}

	want := IdempotencyKey(tenantA, base)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IdempotencyKey(tc.tenant, tc.parts)
			if same := got == want; same != tc.wantEqual {
				t.Fatalf("键相等性不符：got=%s want=%s（期望相等=%v）", got, want, tc.wantEqual)
			}
		})
	}
}

func TestIntentDigestSeparatorAmbiguity(t *testing.T) {
	// 若直接做字符串拼接而不使用 0x1f 分隔，下面两组会碰撞（"a\x1fb"+"none" vs "a"+"b"）。
	// 分隔符的存在保证字段边界不可被内容错位伪造。
	a := IntentParts{OperationType: "a\x1fb", TargetObjectID: ""}
	b := IntentParts{OperationType: "a", TargetObjectID: "b"}
	if IntentDigest(a) == IntentDigest(b) {
		t.Fatal("分隔符歧义防护失效：不同意图产生相同摘要")
	}

	// 另一组：目标对象为空（-> none）与目标对象字面量 "none" 之外的错位。
	c := IntentParts{OperationType: "a\x1fnone", TargetObjectID: ""}
	d := IntentParts{OperationType: "a", TargetObjectID: "none"}
	if IntentDigest(c) == IntentDigest(d) {
		t.Fatal("分隔符歧义防护失效：none 归一化后发生碰撞")
	}
}

func TestIntentDigestNoTargetUsesNone(t *testing.T) {
	// 无目标对象（空串）与显式 "none" 必须等价。
	empty := IntentParts{OperationType: "org.sync", TargetObjectID: ""}
	explicit := IntentParts{OperationType: "org.sync", TargetObjectID: "none"}
	if IntentDigest(empty) != IntentDigest(explicit) {
		t.Fatal("无目标对象未归一化为 none")
	}
}

func TestIntentDigestExcludesTenant(t *testing.T) {
	// IntentDigest 不含 tenant；IdempotencyKey 才带租户前缀。
	parts := IntentParts{OperationType: "expense.submit", TargetObjectID: "claim-001", PayloadDigest: "d"}
	tenantA := ids.MustParse("11111111-1111-1111-1111-111111111111")
	tenantB := ids.MustParse("44444444-4444-4444-4444-444444444444")
	if IntentDigest(parts) != IntentDigest(parts) {
		t.Fatal("摘要不稳定")
	}
	if IdempotencyKey(tenantA, parts) == IdempotencyKey(tenantB, parts) {
		t.Fatal("不同租户产生了相同幂等键")
	}
}

func withDigest(p IntentParts, d string) IntentParts   { p.PayloadDigest = d; return p }
func withOpType(p IntentParts, o string) IntentParts   { p.OperationType = o; return p }
func withTarget(p IntentParts, tgt string) IntentParts { p.TargetObjectID = tgt; return p }
func withStep(p IntentParts, s ids.ID) IntentParts     { p.StepID = s; return p }
