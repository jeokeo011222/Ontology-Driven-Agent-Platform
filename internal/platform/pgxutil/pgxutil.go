// Package pgxutil 封装 PostgreSQL 连接池与租户上下文注入（RLS 会话变量）。
package pgxutil

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/platform/ids"
)

// Connect 创建连接池并做一次连通性检查。
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("pgxutil: parse url: %w", err)
	}
	cfg.MaxConns = 20
	cfg.MinConns = 2
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pgxutil: connect: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgxutil: ping: %w", err)
	}
	return pool, nil
}

// setTenantSQL 在每个连接上设置 RLS 会话变量（connect 时执行一次）。
const setTenantSQL = "SELECT set_config('app.tenant_id', $1, false)"

// WithTenant 返回绑定了租户会话变量的 context（连接级，配合连接复用安全）。
// 说明：pgx 连接池会复用连接，因此这里使用 AfterConnect 之外的显式方式——
// 每次借用连接后由调用方通过 SetTenantOnConn 设置；事务内优先使用
// WithTenantTx（SET LOCAL，事务结束自动清理，杜绝上下文泄漏，
// 第十六篇 §5.4 连接池上下文泄漏条款）。
func SetTenantOnConn(ctx context.Context, conn *pgx.Conn, tenantID ids.ID) error {
	_, err := conn.Exec(ctx, setTenantSQL, tenantID.String())
	return err
}

// WithTenantTx 在事务内设置租户上下文（SET LOCAL 语义，事务结束自动失效）。
func WithTenantTx(ctx context.Context, tx pgx.Tx, tenantID ids.ID) error {
	_, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantID.String())
	return err
}

// TenantTx 在"带租户上下文"的事务内执行 fn：
// 开启事务 → 注入 app.tenant_id（SET LOCAL）→ fn → 提交。
//
// 这是平台层**唯一**的租户事务包装实现。HTTP 中间件（api/middleware）与数据装配
// （internal/seed）都委托到此处，而不是各自复制一份：历史 L1 缺陷的根因正是
// 出现了第二份注入实现，且写的是另一个 GUC（app.current_tenant_id），
// 使 RLS 策略函数返回 NULL —— 租户隔离静默失效（详见实现符合性评测清单 L1）。
//
// tenantID 由调用方显式传入而非从 ctx 解析：平台层因此不需要知道身份上下文的形状，
// 各调用方保留自己的 fail-closed 解析逻辑。
func TenantTx(ctx context.Context, pool *pgxpool.Pool, tenantID ids.ID, fn func(pgx.Tx) error) error {
	if pool == nil {
		return fmt.Errorf("pgxutil: 数据库连接池未就绪")
	}
	if tenantID.IsZero() {
		return fmt.Errorf("pgxutil: 缺少租户身份")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pgxutil: 开启事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := WithTenantTx(ctx, tx, tenantID); err != nil {
		return fmt.Errorf("pgxutil: 注入租户上下文失败: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pgxutil: 提交事务失败: %w", err)
	}
	return nil
}
