package harness

import (
	"context"
	stderrors "errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/platform/clock"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
	"odap/internal/platform/runstate"
	"odap/internal/platform/tenantctx"
)

// errStaleSnapshot 表示 Apply 复读时发现 (status, lease_epoch) 已与快照
// 不符——旧 Worker 的写入被 fencing 拒绝（第六篇 §6.3）。
var errStaleSnapshot = stderrors.New("harness: stale run snapshot (fencing)")

// Machine 把纯函数状态机（NextState）与 PostgreSQL 持久化封装为应用服务
// 入口（第六篇 §14.3）：同一入口完成"读状态 → 判定 → 条件写回"，写回带
// 租约代次 fencing。禁止 Agent 或 Tool 直接更新 agent_runs.status（§3.3）。
//
// store 为 nil 时惰性使用 pgRunStore{Pool}；测试可注入内存 RunStore，
// 从而在不连接真实数据库的情况下覆盖 fencing 与非法转换路径。
type Machine struct {
	Pool  *pgxpool.Pool
	Clock clock.Clock

	store RunStore
}

// RunRow 是 Machine 判定所需的 Run 持久化快照。
type RunRow struct {
	Status     RunState
	LeaseEpoch int64
}

// RunStore 是 Machine 的持久化依赖抽象（PG 实现 + 内存测试实现）。
//
// Get 读取当前状态与租约代次，喂给纯函数状态机。
// Apply 在持久层原子完成"加锁复读 → 校验快照 → 条件 UPDATE"；返回 false
// 表示复读发现 (status, lease_epoch) 已与快照不符（旧 Worker 写入被
// fencing 拒绝，§6.3），Machine 将其翻译为 CodeExecLeaseLost。
type RunStore interface {
	Get(ctx context.Context, runID ids.ID) (RunRow, error)
	Apply(ctx context.Context, runID ids.ID, from RunState, fromEpoch int64, to RunState, event RunEventType, now time.Time) (bool, error)
}

// Apply 执行一次状态转换（第六篇 §13.3 原子状态转换 + §6.3 fencing）。
//
// 流程：租户上下文校验（fail-closed）→ Get 读快照 → NextState 纯函数判定
// → Apply 事务内加锁复读并条件 UPDATE。旧 Worker 的写被拒时返回
// CodeExecLeaseLost，调用方必须停止推进并重新读取状态（§6.3）。
func (m *Machine) Apply(ctx context.Context, runID ids.ID, event RunEventType, facts TransitionFacts) error {
	if runID.IsZero() {
		return errors.E(errors.CodeValidationFailed, "harness: runID must not be zero")
	}
	if _, err := tenantFromContext(ctx); err != nil {
		return err
	}
	if m.Clock == nil {
		m.Clock = clock.RealClock{}
	}
	if m.store == nil {
		if m.Pool == nil {
			return errors.E(errors.CodeInternal, "harness: machine has neither store nor pool")
		}
		m.store = pgRunStore{pool: m.Pool}
	}

	// 1. 读当前状态与租约代次（喂给纯函数状态机）。
	row, err := m.store.Get(ctx, runID)
	if err != nil {
		return err
	}

	// 2. 纯函数状态机判定（不触库、不调外部服务）。
	next, err := NextState(row.Status, event, facts)
	if err != nil {
		return err
	}

	// 3. 条件写回（fencing：旧 Worker 写被拒 → CodeExecLeaseLost）。
	applied, err := m.store.Apply(ctx, runID, row.Status, row.LeaseEpoch, next, event, m.Clock.Now())
	if err != nil {
		return err
	}
	if !applied {
		return errors.E(errors.CodeExecLeaseLost,
			"harness: run lease epoch or status changed; stale worker write rejected")
	}
	return nil
}

// runFieldUpdates 描述一次转换需同步维护的 Run 字段（nil = 保持原值）。
type runFieldUpdates struct {
	AvailableAt          *time.Time
	CancelRequestedAt    *time.Time
	LastReconciliationAt *time.Time
	StartedAt            *time.Time
	FinishedAt           *time.Time
	ErrorCode            *string
	ErrorSummary         *string
}

// fieldUpdatesFor 计算状态转换的附属字段维护：
//   - available_at：进入可调度状态（SchedulingClassOf 非空）时置 now，
//     使任务立即具备领取资格（§6.2）；
//   - cancel_requested_at：进入 CANCELLING 时置 now（§14.2.1 第 1 条）；
//   - last_reconciliation_at：进入/离开对账时置 now，作为对账队列游标
//     （§6.2 对账队列按 last_reconciliation_at 独立扫描领取）；
//   - started_at：首次离开 CREATED 时置 now；
//   - finished_at/error_*：进入终态/FAILED 时置 now 与失败事件名。
func fieldUpdatesFor(from, to RunState, event RunEventType, now time.Time) runFieldUpdates {
	var u runFieldUpdates
	if runstate.SchedulingClassOf(to) != "" {
		u.AvailableAt = &now
	}
	if to == runstate.Cancelling {
		u.CancelRequestedAt = &now
	}
	if to == runstate.RequiresReconciliation ||
		event == EventReconciliationSucceeded || event == EventReconciliationFailed {
		u.LastReconciliationAt = &now
	}
	if from == runstate.Created {
		u.StartedAt = &now
	}
	if runstate.IsTerminal(to) {
		u.FinishedAt = &now
	}
	if to == runstate.Failed {
		code := string(event)
		summary := "运行在事件 " + string(event) + " 下进入 FAILED"
		u.ErrorCode = &code
		u.ErrorSummary = &summary
	}
	return u
}

// pgRunStore 是 RunStore 的 PostgreSQL 实现（odap_app 角色，RLS 生效）。
// 所有语句都在租户事务（SET LOCAL app.tenant_id）内执行，事务结束自动
// 清理会话变量，杜绝连接池上下文泄漏（第十六篇 §5.4）。
type pgRunStore struct{ pool *pgxpool.Pool }

// Get 读取 Run 的当前状态与租约代次。
func (s pgRunStore) Get(ctx context.Context, runID ids.ID) (RunRow, error) {
	tenantID, err := tenantFromContext(ctx)
	if err != nil {
		return RunRow{}, err
	}
	var row RunRow
	err = inTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT status, lease_epoch
			   FROM runtime.agent_runs
			  WHERE id = $1 AND tenant_id = $2`,
			runID.String(), tenantID.String()).Scan(&row.Status, &row.LeaseEpoch)
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return RunRow{}, errors.E(errors.CodeNotFound, "harness: run not found")
		}
		return RunRow{}, errors.Wrap(err, errors.CodeInternal, "harness: load run")
	}
	return row, nil
}

// Apply 在单个事务内完成原子状态转换（§13.3 + §6.3）：
//
//	BEGIN;
//	SELECT status, lease_epoch FROM runtime.agent_runs
//	 WHERE id = $1 AND tenant_id = $2 FOR UPDATE;   -- 加锁复读
//	-- 复读结果与 Machine 快照不一致 → ROLLBACK，返回 false（fencing）
//	UPDATE runtime.agent_runs
//	   SET status = $1, available_at = ..., cancel_requested_at = ...,
//	       last_reconciliation_at = ..., started_at = ..., finished_at = ...,
//	       error_code = ..., error_summary = ...
//	 WHERE id = $9 AND tenant_id = $10 AND lease_epoch = $11 AND status = $12;
//	-- 影响行数为 0 → ROLLBACK，返回 false（fencing，双保险）
//	COMMIT;
//
// 只有条件 UPDATE 成功，才允许调用方继续插入事件与 Outbox（§13.3）。
func (s pgRunStore) Apply(ctx context.Context, runID ids.ID, from RunState, fromEpoch int64, to RunState, event RunEventType, now time.Time) (bool, error) {
	tenantID, err := tenantFromContext(ctx)
	if err != nil {
		return false, err
	}
	u := fieldUpdatesFor(from, to, event, now)
	err = inTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		// 事务内加锁复读：确认 Machine 的快照仍然有效。
		var (
			curStatus RunState
			curEpoch  int64
		)
		if err := tx.QueryRow(ctx,
			`SELECT status, lease_epoch
			   FROM runtime.agent_runs
			  WHERE id = $1 AND tenant_id = $2
			  FOR UPDATE`,
			runID.String(), tenantID.String()).Scan(&curStatus, &curEpoch); err != nil {
			return err // pgx.ErrNoRows：行已消失，按 fencing 失败处理
		}
		if curStatus != from || curEpoch != fromEpoch {
			return errStaleSnapshot
		}
		tag, err := tx.Exec(ctx,
			`UPDATE runtime.agent_runs
			    SET status = $1,
			        available_at = COALESCE($2::timestamptz, available_at),
			        cancel_requested_at = COALESCE($3::timestamptz, cancel_requested_at),
			        last_reconciliation_at = COALESCE($4::timestamptz, last_reconciliation_at),
			        started_at = COALESCE($5::timestamptz, started_at),
			        finished_at = COALESCE($6::timestamptz, finished_at),
			        error_code = COALESCE($7::text, error_code),
			        error_summary = COALESCE($8::text, error_summary)
			  WHERE id = $9 AND tenant_id = $10 AND lease_epoch = $11 AND status = $12`,
			to, u.AvailableAt, u.CancelRequestedAt, u.LastReconciliationAt, u.StartedAt, u.FinishedAt,
			u.ErrorCode, u.ErrorSummary, runID.String(), tenantID.String(), fromEpoch, from)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errStaleSnapshot
		}
		return nil
	})
	switch {
	case err == nil:
		return true, nil
	case err == errStaleSnapshot, err == pgx.ErrNoRows:
		return false, nil
	default:
		return false, errors.Wrap(err, errors.CodeInternal, "harness: apply transition")
	}
}

// tenantFromContext 从可信上下文解析租户 ID。
// 安全约束（第十六篇 §5.4）：租户身份只能来自经过验证的认证上下文，
// 禁止从请求体或查询参数读取；缺失按 fail-closed 拒绝。
func tenantFromContext(ctx context.Context) (ids.ID, error) {
	id, ok := tenantctx.FromContext(ctx)
	if !ok || id.TenantID.IsZero() {
		return ids.ID{}, errors.E(errors.CodePermissionDenied, "harness: missing tenant identity in context")
	}
	return id.TenantID, nil
}

// inTenantTx 在租户事务（SET LOCAL app.tenant_id）中执行 fn 并提交。
// RLS 表（runtime.agent_runs 等）的读写必须显式注入租户上下文，
// 否则行级策略会过滤掉全部行或拒绝写入。
func inTenantTx(ctx context.Context, pool *pgxpool.Pool, tenantID ids.ID, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := pgxutil.WithTenantTx(ctx, tx, tenantID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
