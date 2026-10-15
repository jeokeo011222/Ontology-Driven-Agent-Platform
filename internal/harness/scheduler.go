package harness

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/platform/clock"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/runstate"
)

// DefaultLeaseTTL 是默认租约时长。第六篇 §6.2 示例为 30 秒；长工具调用
// （LLM 推理、ERP 提交）可能超过该时长，Worker 必须在阻塞点前调用
// RenewLease 续期，续期失败即视为失去所有权并立即停止推进。
const DefaultLeaseTTL = 30 * time.Second

// Scheduler 是任务租约调度器（第六篇 §6.2）：按四类队列原子领取可调度 Run，
// 支持租约续期与过期回收。
//
// 每个 Scheduler 实例代表一个 Worker，Owner 是其唯一标识，写入
// lease_owner 用于归属校验与 fencing（§6.3）。
type Scheduler struct {
	Pool  *pgxpool.Pool
	Clock clock.Clock
	// Owner 是 Worker 实例标识（建议为 pod 名或 host:pid），非空。
	Owner string
	// LeaseTTL 是租约时长；<=0 时使用 DefaultLeaseTTL。
	LeaseTTL time.Duration
}

// ClaimedRun 是一次成功领取的 Run 及其租约信息。
type ClaimedRun struct {
	RunID          ids.ID
	TenantID       ids.ID
	Status         RunState
	LeaseOwner     string
	LeaseEpoch     int64
	LeaseExpiresAt time.Time
	AttemptCount   int
	MaxAttempts    int
	Priority       int
}

// queueSpec 描述一类调度队列的可领取状态集与排序键。
type queueSpec struct {
	statuses []string
	// orderBy 是队列内排序子句。对账队列按 last_reconciliation_at 扫描，
	// 其余队列按 priority DESC, available_at ASC（第六篇 §6.2）。
	orderBy string
}

// queueFor 把调度类映射为可领取状态集与排序键（第六篇 §6.2 四队列分治，
// 状态集来自 runstate.SchedulingClassOf，即 §3.1 的"是否可调度"列）。
func queueFor(class runstate.SchedulingClass) (queueSpec, error) {
	switch class {
	case runstate.ClassNormal:
		// 执行队列：新建（CREATED）、构建上下文（CONTEXT_BUILDING）、
		// 待规划/执行（READY）、执行中丢失租约后重领（RUNNING）。
		return queueSpec{
			statuses: []string{
				runstate.Created, runstate.ContextBuilding,
				runstate.Ready, runstate.Running,
			},
			orderBy: "priority DESC, available_at ASC",
		}, nil
	case runstate.ClassRecovery:
		// 恢复队列：仅恢复工作（第六篇 §3.1）。
		return queueSpec{
			statuses: []string{runstate.Recovering},
			orderBy:  "priority DESC, available_at ASC",
		}, nil
	case runstate.ClassReconciliation:
		// 对账队列：按 last_reconciliation_at 独立扫描，避免饥饿。
		return queueSpec{
			statuses: []string{runstate.RequiresReconciliation},
			orderBy:  "last_reconciliation_at ASC NULLS FIRST, available_at ASC",
		}, nil
	case runstate.ClassCancelling:
		// 取消收敛队列：取消请求优先推进，不再按 priority。
		return queueSpec{
			statuses: []string{runstate.Cancelling},
			orderBy:  "cancel_convergence_deadline ASC NULLS FIRST, available_at ASC",
		}, nil
	default:
		return queueSpec{}, errors.Ef(errors.CodeValidationFailed,
			"harness: unknown scheduling class %q", class)
	}
}

// claimSQL 是原子领取语句（第六篇 §6.2）。
//
// 说明：
//   - 租户过滤（tenant_id = $2）不可省略：runtime.agent_runs 启用了 FORCE RLS，
//     且应用角色非超级用户，任何跨租户全局扫描都会被行级策略过滤或拒绝；
//   - status = ANY($3) 按队列传入状态集，四类队列互不混用；
//   - NOT (cancel_requested_at IS NOT NULL AND status <> 'CANCELLING')：已请求取消
//     的任务不再进入正常/恢复队列，只能由取消收敛队列推进（避免取消被"续跑"绕过）；
//   - FOR UPDATE SKIP LOCKED 解决领取时的行锁竞争；lease_epoch 递增提供
//     后续写入的 fencing token（§6.3），防止已过期租约的旧 Worker 覆写结果。
const claimSQL = `
WITH candidate AS (
    SELECT id
      FROM runtime.agent_runs
     WHERE tenant_id = $2
       AND status = ANY($3::text[])
       AND available_at <= $5
       AND (lease_expires_at IS NULL OR lease_expires_at < $5)
       AND NOT (cancel_requested_at IS NOT NULL AND status <> 'CANCELLING')
     ORDER BY ` + `%s` + `
     FOR UPDATE SKIP LOCKED
     LIMIT 1
)
UPDATE runtime.agent_runs r
   SET lease_owner = $1,
       lease_expires_at = $5 + ($4::int * interval '1 second'),
       lease_epoch = r.lease_epoch + 1,
       attempt_count = r.attempt_count + 1
  FROM candidate c
 WHERE r.id = c.id
RETURNING r.id, r.tenant_id, r.status, r.lease_owner, r.lease_epoch,
          r.lease_expires_at, r.attempt_count, r.max_attempts, r.priority`

// ClaimNext 在指定租户的目标队列中原子领取一个可调度 Run。
//
// 返回 (nil, nil) 表示该队列当前无可用任务（非错误，调用方可退避后重试）。
// 领取成功后 lease_epoch 已递增、attempt_count 已累加，租约归属本 Scheduler.Owner。
func (s *Scheduler) ClaimNext(ctx context.Context, tenantID ids.ID, class runstate.SchedulingClass) (*ClaimedRun, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.E(errors.CodeInternal, "harness: scheduler has no pool")
	}
	if tenantID.IsZero() {
		return nil, errors.E(errors.CodeValidationFailed, "harness: tenantID must not be zero")
	}
	if s.Owner == "" {
		return nil, errors.E(errors.CodeInternal, "harness: scheduler owner must not be empty")
	}
	spec, err := queueFor(class)
	if err != nil {
		return nil, err
	}

	clk := s.Clock
	if clk == nil {
		clk = clock.RealClock{}
	}
	ttl := s.LeaseTTL
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}
	now := clk.Now()
	sql := formatClaimSQL(spec)
	var out *ClaimedRun
	err = inTenantTx(ctx, s.Pool, tenantID, func(tx pgx.Tx) error {
		var (
			idStr  string
			tenStr string
			row    ClaimedRun
		)
		err := tx.QueryRow(ctx, sql,
			s.Owner, tenantID.String(), spec.statuses, int(ttl.Seconds()), now,
		).Scan(&idStr, &tenStr, &row.Status, &row.LeaseOwner, &row.LeaseEpoch,
			&row.LeaseExpiresAt, &row.AttemptCount, &row.MaxAttempts, &row.Priority)
		if err != nil {
			if err == pgx.ErrNoRows {
				return nil // 队列为空，非错误
			}
			return err
		}
		if row.RunID, err = ids.Parse(idStr); err != nil {
			return err
		}
		if row.TenantID, err = ids.Parse(tenStr); err != nil {
			return err
		}
		out = &row
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "harness: claim next run")
	}
	return out, nil
}

// RenewLease 续期租约，返回新的到期时间。
//
// 必须携带领取时的 lease_epoch：行不属于 (owner, epoch) 时返回
// CodeExecLeaseLost，调用方必须立即停止推进并放弃本次执行（§6.2/§6.3）。
func (s *Scheduler) RenewLease(ctx context.Context, tenantID, runID ids.ID, owner string, epoch int64) (time.Time, error) {
	if s == nil || s.Pool == nil {
		return time.Time{}, errors.E(errors.CodeInternal, "harness: scheduler has no pool")
	}
	if tenantID.IsZero() || runID.IsZero() {
		return time.Time{}, errors.E(errors.CodeValidationFailed, "harness: tenantID and runID must not be zero")
	}
	clk := s.Clock
	if clk == nil {
		clk = clock.RealClock{}
	}
	ttl := s.LeaseTTL
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}
	now := clk.Now()
	expiry := now.Add(ttl)

	var renewed bool
	err := inTenantTx(ctx, s.Pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE runtime.agent_runs
			    SET lease_expires_at = $1
			  WHERE id = $2 AND tenant_id = $3
			    AND lease_owner = $4 AND lease_epoch = $5
			    AND finished_at IS NULL`,
			expiry, runID.String(), tenantID.String(), owner, epoch)
		if err != nil {
			return err
		}
		renewed = tag.RowsAffected() == 1
		return nil
	})
	if err != nil {
		return time.Time{}, errors.Wrap(err, errors.CodeInternal, "harness: renew lease")
	}
	if !renewed {
		return time.Time{}, errors.E(errors.CodeExecLeaseLost,
			"harness: lease not held by this worker/epoch; stop advancing")
	}
	return expiry, nil
}

// expiredRecoverySQL 把"租约已过期且正处于执行中状态"的 Run 转入恢复：
// 这些状态的 Worker 已失效，不能继续推进，但可能已产生副作用，必须经
// RECOVERING 复核外部操作与检查点（第六篇 §14.2.3 租约过期扫描 →
// RecoveryStarted）。此处的批量 UPDATE 即该扫描的持久化落点。
const expiredRecoverySQL = `
UPDATE runtime.agent_runs
   SET status = 'RECOVERING',
       lease_owner = NULL,
       lease_expires_at = NULL,
       lease_epoch = lease_epoch + 1,
       available_at = $2
 WHERE tenant_id = $1
   AND status = ANY($3::text[])
   AND lease_expires_at IS NOT NULL
   AND lease_expires_at < $2`

// expiredReclaimSQL 清理"租约已过期但状态本身即可重新领取"的 Run：
// 仅释放租约并递增 epoch（使旧 Worker 立即失去写入权），状态不变。
const expiredReclaimSQL = `
UPDATE runtime.agent_runs
   SET lease_owner = NULL,
       lease_expires_at = NULL,
       lease_epoch = lease_epoch + 1
 WHERE tenant_id = $1
   AND status = ANY($3::text[])
   AND lease_expires_at IS NOT NULL
   AND lease_expires_at < $2`

// ReleaseExpiredLeases 回收指定租户下所有已过期租约，返回受影响行数。
//
// 两条路径（第六篇 §14.2.3）：
//   - PLANNING / PLAN_VALIDATING 为"调用进行中"状态（§3.1 不可调度），其租约
//     过期说明 Worker 已崩溃，必须转 RECOVERING 交由恢复队列接管；
//   - CREATED / CONTEXT_BUILDING / READY / RUNNING / RECOVERING / CANCELLING /
//     REQUIRES_RECONCILIATION 状态本身可被重新领取，仅释放租约即可。
//
// 两种路径都递增 lease_epoch，令失去所有权的旧 Worker 的任何后续写入被
// fencing 拒绝（§6.3），而非静默覆盖新持有者的结果。
func (s *Scheduler) ReleaseExpiredLeases(ctx context.Context, tenantID ids.ID) (int64, error) {
	if s == nil || s.Pool == nil {
		return 0, errors.E(errors.CodeInternal, "harness: scheduler has no pool")
	}
	if tenantID.IsZero() {
		return 0, errors.E(errors.CodeValidationFailed, "harness: tenantID must not be zero")
	}
	clk := s.Clock
	if clk == nil {
		clk = clock.RealClock{}
	}
	now := clk.Now()

	// 执行中状态（不可调度）→ RECOVERING。
	inFlight := []string{runstate.Planning, runstate.PlanValidating}
	// 可重新领取状态（§3.1"SchedulingClassOf 非空"）。
	reclaimable := []string{
		runstate.Created, runstate.ContextBuilding, runstate.Ready, runstate.Running,
		runstate.Recovering, runstate.Cancelling, runstate.RequiresReconciliation,
	}

	var total int64
	err := inTenantTx(ctx, s.Pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, expiredRecoverySQL, tenantID.String(), now, inFlight)
		if err != nil {
			return err
		}
		total += tag.RowsAffected()
		tag, err = tx.Exec(ctx, expiredReclaimSQL, tenantID.String(), now, reclaimable)
		if err != nil {
			return err
		}
		total += tag.RowsAffected()
		return nil
	})
	if err != nil {
		return 0, errors.Wrap(err, errors.CodeInternal, "harness: release expired leases")
	}
	return total, nil
}

// formatClaimSQL 把队列排序键拼入领取 SQL（排序键仅来自 queueFor 的白名单，
// 不含外部输入，无注入风险）。
func formatClaimSQL(spec queueSpec) string {
	return fmt.Sprintf(claimSQL, spec.orderBy)
}
