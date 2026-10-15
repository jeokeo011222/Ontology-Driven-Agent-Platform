//go:build faultinjection

// Package faultinjection 承载故障注入测试（第十六篇 §10.2「故障注入测试」）。
//
// 本节重点：提交后响应丢失、重复消息、Worker 崩溃、权限撤销、版本冲突、证据服务故障。
// 本文件覆盖其中的**重复消息**：依赖真实 PostgreSQL 的 Inbox 去重键，
// 因此带 faultinjection tag 运行，且需要 ODAP_DATABASE_URL。
//
// 为什么必须用真实库而不是内存假实现：去重语义**完全由唯一约束与
// INSERT ... ON CONFLICT 承担**，内存实现无法证明这一点；
// 而"重复消息导致非幂等操作被执行两次"是 §10.3 的发布硬门禁之一。
package faultinjection

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/execution/inbox"
	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
)

type countingHandler struct {
	mu    sync.Mutex
	calls int
	fail  bool
}

func (h *countingHandler) Handle(_ context.Context, _ inbox.Event) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++
	if h.fail {
		return fmt.Errorf("注入的处理失败")
	}
	return nil
}

func (h *countingHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls
}

// connect 建立 owner 与 app 两条连接；缺环境时按约定跳过。
func connect(t *testing.T) (owner, app *pgxpool.Pool) {
	t.Helper()
	appDSN := os.Getenv("ODAP_DATABASE_URL")
	if appDSN == "" {
		t.Skip("ODAP_DATABASE_URL 未设置，跳过故障注入测试")
	}
	ownerDSN := os.Getenv("ODAP_MIGRATE_URL")
	if ownerDSN == "" {
		ownerDSN = appDSN
	}
	ctx := context.Background()
	var err error
	owner, err = pgxutil.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("连接 owner 库失败: %v", err)
	}
	app, err = pgxutil.Connect(ctx, appDSN)
	if err != nil {
		owner.Close()
		t.Fatalf("连接 app 库失败: %v", err)
	}
	t.Cleanup(func() { owner.Close(); app.Close() })
	return owner, app
}

// testTenant 返回一个可用租户 ID；若库中无租户则临时建一个，并注册清理。
func testTenant(t *testing.T, ctx context.Context, owner *pgxpool.Pool) ids.ID {
	t.Helper()
	var idStr string
	if err := owner.QueryRow(ctx,
		`SELECT id::text FROM iam.tenants ORDER BY created_at LIMIT 1`).Scan(&idStr); err == nil {
		if id, perr := ids.Parse(idStr); perr == nil {
			return id
		}
	}
	id := ids.New()
	key := fmt.Sprintf("faultinject-%d", time.Now().UnixNano())
	if _, err := owner.Exec(ctx,
		`INSERT INTO iam.tenants (id, tenant_key, name, status) VALUES ($1,$2,$3,'ACTIVE')`,
		id.String(), key, "故障注入测试租户"); err != nil {
		t.Fatalf("创建测试租户失败: %v", err)
	}
	t.Cleanup(func() {
		_, _ = owner.Exec(ctx, `DELETE FROM iam.tenants WHERE id = $1`, id.String())
	})
	return id
}

func cleanupConsumer(t *testing.T, ctx context.Context, owner *pgxpool.Pool, name string) {
	t.Cleanup(func() {
		_, _ = owner.Exec(ctx, `DELETE FROM execution.consumer_inbox_dedup WHERE consumer_name = $1`, name)
		_, _ = owner.Exec(ctx, `DELETE FROM execution.consumer_inbox WHERE consumer_name = $1`, name)
		_, _ = owner.Exec(ctx, `DELETE FROM execution.event_dead_letters WHERE consumer_name = $1`, name)
	})
}

func newEvent(tenantID ids.ID) inbox.Event {
	return inbox.Event{
		EventID:       ids.New(),
		TenantID:      tenantID,
		EventType:     "RunCreated",
		Payload:       json.RawMessage(`{"source":"fault-injection"}`),
		PayloadDigest: "sha256:fault-injection-fixture",
		OccurredAt:    time.Now().UTC(),
	}
}

// TestDuplicateDeliveryIsHandledExactlyOnce 同一事件投递两次，处理器只能被调用一次。
func TestDuplicateDeliveryIsHandledExactlyOnce(t *testing.T) {
	ctx := context.Background()
	owner, app := connect(t)
	tenantID := testTenant(t, ctx, owner)

	name := fmt.Sprintf("faultinject-dup-%d", time.Now().UnixNano())
	cleanupConsumer(t, ctx, owner, name)

	h := &countingHandler{}
	c := &inbox.Consumer{Pool: app, Name: name, Handler: h}
	ev := newEvent(tenantID)

	if err := c.Process(ctx, ev); err != nil {
		t.Fatalf("首次投递失败: %v", err)
	}
	if err := c.Process(ctx, ev); err != nil {
		t.Fatalf("重复投递应被幂等短路而非报错，实际: %v", err)
	}
	if got := h.count(); got != 1 {
		t.Fatalf("处理器被调用 %d 次，期望 1 次——重复消息导致了重复的非幂等操作（§10.3 硬门禁）", got)
	}
}

// TestFailedDeliveryBecomesDeadLetterAndStaysShortCircuited 处理失败进入死信，
// 再次投递仍被去重短路（"失败即终态"的 MVP 语义，避免无界重试打爆下游）。
func TestFailedDeliveryBecomesDeadLetterAndStaysShortCircuited(t *testing.T) {
	ctx := context.Background()
	owner, app := connect(t)
	tenantID := testTenant(t, ctx, owner)

	name := fmt.Sprintf("faultinject-dlq-%d", time.Now().UnixNano())
	cleanupConsumer(t, ctx, owner, name)

	h := &countingHandler{fail: true}
	c := &inbox.Consumer{Pool: app, Name: name, Handler: h}
	ev := newEvent(tenantID)

	if err := c.Process(ctx, ev); err == nil {
		t.Fatal("处理失败时 Process 应返回错误（不得把失败报告为成功，§10.3 硬门禁）")
	}
	if got := h.count(); got != 1 {
		t.Fatalf("处理器调用次数 = %d，期望 1", got)
	}

	var dlq int
	if err := owner.QueryRow(ctx,
		`SELECT count(*) FROM execution.event_dead_letters WHERE consumer_name = $1 AND event_id = $2`,
		name, ev.EventID.String()).Scan(&dlq); err != nil {
		t.Fatalf("查询死信表失败: %v", err)
	}
	if dlq != 1 {
		t.Fatalf("死信登记数 = %d，期望 1（失败事件必须可被人工处置）", dlq)
	}

	// 再次投递：去重键已占位，应短路返回 nil 且不再调用处理器。
	if err := c.Process(ctx, ev); err != nil {
		t.Fatalf("重复投递失败事件应被短路，实际返回: %v", err)
	}
	if got := h.count(); got != 1 {
		t.Fatalf("重复投递导致处理器重复执行：调用次数 = %d，期望 1", got)
	}
}
