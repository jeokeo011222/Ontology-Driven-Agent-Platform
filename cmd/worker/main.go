// cmd/worker 是 ODAP 平台执行 Worker 进程入口。
//
// 职责（第六篇 §6.2/§6.3）：按四类调度队列（执行/恢复/对账/取消收敛）原子领取
// Run，经 harness.Machine 在租约 fencing 下推进状态，并通过 transactional
// outbox 发布事件。
//
// 环境变量：ODAP_DATABASE_URL（或 DATABASE_URL）、WORKER_CONCURRENCY（默认 4）、
// WORKER_LEASE_SECONDS（默认 30）、LOG_LEVEL。
//
// 说明：本进程是"真实调度骨架"——ClaimNext / Machine.Apply / Publisher.RunOnce
// 均为真实调用（非 sleep 桩）；具体的步骤执行（模型调用、工具执行、ERP 网关）
// 由后续按 Run 状态分派的执行器补齐，本骨架以 LeaseAcquired 簿记事件证明
// 状态机与租约写入链路可用。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/execution/outbox"
	"odap/internal/harness"
	"odap/internal/platform/clock"
	"odap/internal/platform/ids"
	"odap/internal/platform/logging"
	"odap/internal/platform/pgxutil"
	"odap/internal/platform/runstate"
	"odap/internal/platform/tenantctx"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		os.Exit(1)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func run() error {
	logger := logging.New(env("LOG_LEVEL", "info"))

	dbURL := env("ODAP_DATABASE_URL", env("DATABASE_URL", ""))
	if dbURL == "" {
		return fmt.Errorf("ODAP_DATABASE_URL (or DATABASE_URL) is required")
	}
	concurrency := envInt("WORKER_CONCURRENCY", 4)
	if concurrency < 1 {
		concurrency = 1
	}
	leaseSeconds := envInt("WORKER_LEASE_SECONDS", 30)
	if leaseSeconds < 1 {
		leaseSeconds = 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := pgxutil.Connect(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	clk := clock.RealClock{}
	host, _ := os.Hostname()
	base := fmt.Sprintf("worker:%s:%d", host, os.Getpid())

	logger.Info("worker started",
		"concurrency", concurrency, "lease_seconds", leaseSeconds, "owner", base)

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		owner := fmt.Sprintf("%s:%d", base, i)
		go func(owner string) {
			defer wg.Done()
			workerLoop(ctx, pool, clk, owner, time.Duration(leaseSeconds)*time.Second, logger)
		}(owner)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		publisherLoop(ctx, pool, clk, base+":pub", logger)
	}()

	<-ctx.Done()
	logger.Info("shutdown signal received, waiting for workers")
	wg.Wait()
	logger.Info("worker stopped")
	return nil
}

// workerLoop 是单个 Worker 的调度循环：四队列依次领取 → 状态机簿记 → 续租。
func workerLoop(ctx context.Context, pool *pgxpool.Pool, clk clock.Clock, owner string, leaseTTL time.Duration, logger *slog.Logger) {
	sched := &harness.Scheduler{Pool: pool, Clock: clk, Owner: owner, LeaseTTL: leaseTTL}
	machine := &harness.Machine{Pool: pool, Clock: clk}

	// 四队列优先级：取消收敛 > 对账 > 恢复 > 正常（避免取消/对账饥饿）。
	classes := []runstate.SchedulingClass{
		runstate.ClassCancelling,
		runstate.ClassReconciliation,
		runstate.ClassRecovery,
		runstate.ClassNormal,
	}

	backoff := 250 * time.Millisecond
	const maxBackoff = 5 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}
		claimedAny := false
		tenants, err := activeTenantIDs(ctx, pool)
		if err != nil {
			logger.Warn("list tenants failed", "owner", owner, "error", err)
			sleepCtx(ctx, maxBackoff)
			continue
		}
		for _, tenantID := range tenants {
			// 周期性回收过期租约（第六篇 §14.2.3）。
			if _, err := sched.ReleaseExpiredLeases(ctx, tenantID); err != nil {
				logger.Warn("release expired leases failed", "tenant", tenantID.String(), "error", err)
			}
			for _, class := range classes {
				claimed, err := sched.ClaimNext(ctx, tenantID, class)
				if err != nil {
					logger.Error("claim next failed", "tenant", tenantID.String(), "class", string(class), "error", err)
					continue
				}
				if claimed == nil {
					continue
				}
				claimedAny = true
				advanceRun(ctx, machine, sched, owner, claimed, logger)
				break // 本轮该租户已领取一个任务，进入下一租户
			}
		}
		if claimedAny {
			backoff = 250 * time.Millisecond
			continue
		}
		sleepCtx(ctx, backoff)
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// advanceRun 在租约 fencing 下对已领取 Run 做状态簿记，并续租。
func advanceRun(ctx context.Context, machine *harness.Machine, sched *harness.Scheduler,
	owner string, claimed *harness.ClaimedRun, logger *slog.Logger) {
	// 后台 Worker 必须携带可信租户上下文（第十六篇 §5.4）。
	runCtx := tenantctx.WithIdentity(ctx, tenantctx.System(claimed.TenantID))

	// LeaseAcquired 是可租用状态下的自环簿记（第六篇 §14.2）：真实驱动一次
	// "读状态 → 纯函数判定 → 条件 UPDATE（fencing）"写入。
	facts := harness.TransitionFacts{LeaseValid: true}
	if err := machine.Apply(runCtx, claimed.RunID, harness.EventLeaseAcquired, facts); err != nil {
		logger.Warn("apply lease-acquired transition failed",
			"run_id", claimed.RunID.String(), "status", claimed.Status, "error", err)
		return
	}
	if _, err := sched.RenewLease(runCtx, claimed.TenantID, claimed.RunID, owner, claimed.LeaseEpoch); err != nil {
		logger.Warn("renew lease failed; stop advancing",
			"run_id", claimed.RunID.String(), "error", err)
		return
	}
	logger.Info("claimed run",
		"run_id", claimed.RunID.String(), "status", claimed.Status,
		"tenant", claimed.TenantID.String(), "epoch", claimed.LeaseEpoch,
		"attempt", claimed.AttemptCount)
}

// publisherLoop 周期性把 outbox 事件发布到 Sink（第十篇 §5.4 两段式）。
func publisherLoop(ctx context.Context, pool *pgxpool.Pool, clk clock.Clock, owner string, logger *slog.Logger) {
	pub := &outbox.Publisher{
		Pool:  pool,
		Clock: clk,
		Sink:  logSink{logger: logger},
		Owner: owner,
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tenants, err := activeTenantIDs(ctx, pool)
			if err != nil {
				logger.Warn("publisher: list tenants failed", "error", err)
				continue
			}
			for _, tenantID := range tenants {
				if n, err := pub.RunOnce(ctx, tenantID); err != nil {
					logger.Warn("publisher: run once failed", "tenant", tenantID.String(), "error", err)
				} else if n > 0 {
					logger.Debug("published outbox events", "tenant", tenantID.String(), "count", n)
				}
				if _, err := pub.ReclaimExpired(ctx, tenantID); err != nil {
					logger.Warn("publisher: reclaim expired failed", "tenant", tenantID.String(), "error", err)
				}
			}
		}
	}
}

// logSink 是消息代理的 MVP 占位实现：记录事件并接受之。
//
// 生产环境应替换为真实 Broker 生产者（第十篇）；此处保留不丢事件的语义，
// 且 Publisher.RunOnce 的领取/标记流程是真实的（事件会置为 SENT）。
type logSink struct{ logger *slog.Logger }

// Publish 实现 outbox.Sink。
func (s logSink) Publish(_ context.Context, ev outbox.Event) error {
	if s.logger != nil {
		payload := json.RawMessage(ev.Payload)
		s.logger.Debug("publish outbox event",
			"event_id", ev.EventID.String(),
			"type", ev.EventType,
			"aggregate_seq", ev.AggregateSeq,
			"payload", string(payload))
	}
	return nil
}

// activeTenantIDs 列出 ACTIVE 租户（iam.tenants 无 RLS）。
func activeTenantIDs(ctx context.Context, pool *pgxpool.Pool) ([]ids.ID, error) {
	rows, err := pool.Query(ctx, `SELECT id::text FROM iam.tenants WHERE status = 'ACTIVE'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ids.ID
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		id, err := ids.Parse(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// sleepCtx 可被取消的退避等待。
func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
