//go:build integration

package audit

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
)

// TestRecorderAppendAndVerify 连续 Append 3 条后 Verify 通过。
// 需要 ODAP_DATABASE_URL（odap_app）且库中至少存在一个租户（audit_events 外键）。
func TestRecorderAppendAndVerify(t *testing.T) {
	dsn := os.Getenv("ODAP_DATABASE_URL")
	if dsn == "" {
		t.Skip("ODAP_DATABASE_URL 未设置，跳过 PG 集成测试")
	}
	ctx := context.Background()
	pool, err := pgxutil.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	defer pool.Close()

	// 复用 iam.tenants 中任一租户（FK 约束要求存在）。
	var tenantStr string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM iam.tenants ORDER BY created_at LIMIT 1`).Scan(&tenantStr); err != nil {
		t.Skipf("无可用租户，跳过: %v", err)
	}
	tenantID, err := ids.Parse(tenantStr)
	if err != nil {
		t.Fatalf("解析租户失败: %v", err)
	}

	segment := "it-" + ids.New().String()
	r := &Recorder{Pool: pool}
	for i := 0; i < 3; i++ {
		ev, err := r.Append(ctx, Event{
			TenantID:     tenantID,
			EventType:    "IntegrationProbe",
			ActorType:    "SYSTEM",
			ResourceType: "test",
			ResourceID:   "probe",
			Action:       "append",
			Outcome:      "SUCCESS",
			SegmentID:    segment,
			EventPayload: json.RawMessage(`{"i":1}`),
			OccurredAt:   time.Now().UTC(),
		})
		if err != nil {
			t.Fatalf("第 %d 次 Append 失败: %v", i+1, err)
		}
		if ev.SeqNo != int64(i+1) {
			t.Fatalf("seq_no = %d, 期望 %d", ev.SeqNo, i+1)
		}
	}
	if err := r.Verify(ctx, tenantID, segment); err != nil {
		t.Fatalf("Verify 应通过，实际: %v", err)
	}
}

// TestAuditEventsUpdateRejected 断言 append-only：直接 UPDATE 必须被拒绝
// （权限 REVOKE 或 BEFORE UPDATE 触发器）。
func TestAuditEventsUpdateRejected(t *testing.T) {
	dsn := os.Getenv("ODAP_DATABASE_URL")
	if dsn == "" {
		t.Skip("ODAP_DATABASE_URL 未设置，跳过 PG 集成测试")
	}
	ctx := context.Background()
	pool, err := pgxutil.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	defer pool.Close()

	_, err = pool.Exec(ctx, `UPDATE audit.audit_events SET outcome = outcome WHERE false`)
	if err == nil {
		t.Fatal("UPDATE audit.audit_events 应被拒绝（append-only），实际成功")
	}
}
