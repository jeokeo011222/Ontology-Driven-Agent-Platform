package llm

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
)

// 用量来源（第九篇 §11.2/§16.2：区分供应商报告值、平台估算值与未知值）。
const (
	UsageSourceProviderReported = "PROVIDER_REPORTED"
	UsageSourceEstimated        = "ESTIMATED"
)

// 结算状态（第九篇 §11.2：失败调用可按上浮系数自动结算为待核对）。
const (
	SettlementSettled                = "SETTLED"
	SettlementPendingProviderConfirm = "PENDING_PROVIDER_CONFIRM"
)

// DefaultReserveFactor 是失败调用可能仍计费时的预算预留上浮系数（§11.2 初始建议 1.2）。
const DefaultReserveFactor = 1.2

// Usage 是一次推理调用的用量账本条目（第九篇 §16.1 llm.usage_ledger）。
type Usage struct {
	RequestID        string
	RunID            ids.ID
	StepID           ids.ID
	EndpointID       ids.ID
	ModelRevisionID  ids.ID
	InputTokens      int64
	OutputTokens     int64
	Source           string
	EstimatedCost    float64
	Currency         string
	SettlementStatus string
}

// usageStore 抽象账本持久化（PG 实现 + 内存测试实现）。
type usageStore interface {
	// insert 幂等写入；同一 (tenant, request_id) 重复写入返回 inserted=false。
	insert(ctx context.Context, tenantID ids.ID, u Usage) (bool, error)
	// sumCost 汇总区间内的估算费用与条目数。
	sumCost(ctx context.Context, tenantID ids.ID, from, to time.Time) (float64, int64, error)
}

// UsageLedger 是 LLM 用量账本（第九篇 §11/§16）。
//
// 重试不重复结算：llm.usage_ledger 的 UNIQUE(tenant_id, request_id) 配合
// INSERT ... ON CONFLICT DO NOTHING 保证同一逻辑结算只落一条。
type UsageLedger struct {
	Pool *pgxpool.Pool

	store usageStore
}

// NewUsageLedger 构造使用 PostgreSQL 账本的 UsageLedger。
func NewUsageLedger(pool *pgxpool.Pool) *UsageLedger { return &UsageLedger{Pool: pool} }

func (l *UsageLedger) storeFor() (usageStore, error) {
	if l == nil {
		return nil, errors.E(errors.CodeInternal, "llm: usage ledger 为空")
	}
	if l.store != nil {
		return l.store, nil
	}
	if l.Pool == nil {
		return nil, errors.E(errors.CodeInternal, "llm: usage ledger 未配置连接池")
	}
	l.store = pgUsageStore{pool: l.Pool}
	return l.store, nil
}

// Record 写入一条用量条目；重复 request_id 不重复结算。
func (l *UsageLedger) Record(ctx context.Context, tenantID ids.ID, u Usage) error {
	if tenantID.IsZero() {
		return errors.E(errors.CodeValidationFailed, "llm: 账本租户不能为空")
	}
	if u.RequestID == "" {
		return errors.E(errors.CodeValidationFailed, "llm: usage request_id 不能为空")
	}
	st, err := l.storeFor()
	if err != nil {
		return err
	}
	_, err = st.insert(ctx, tenantID, u)
	return err
}

// SumCost 汇总 [from, to) 区间内的估算费用与条目数，用于月度对账与预算。
func (l *UsageLedger) SumCost(ctx context.Context, tenantID ids.ID, from, to time.Time) (float64, int64, error) {
	if tenantID.IsZero() {
		return 0, 0, errors.E(errors.CodeValidationFailed, "llm: 账本租户不能为空")
	}
	st, err := l.storeFor()
	if err != nil {
		return 0, 0, err
	}
	return st.sumCost(ctx, tenantID, from, to)
}

// BudgetGuard 是预算守卫（第九篇 §11.1/§11.2）。
//
// Check 在调用前判定：已用费用 + 预计费用 × 预留上浮系数 是否超过上限。
// 上浮系数处理"失败调用仍可能计费"的情况（§11.2 失败调用自动结算规则）。
type BudgetGuard struct {
	Ledger *UsageLedger

	// Limit 是周期内预算上限（币种由账本条目决定，调用方保持一致）。
	Limit float64
	// From/To 是结算区间；To 为零值时表示到当前时刻。
	From time.Time
	To   time.Time

	// ReserveFactor 是预留上浮系数，<=0 时取 DefaultReserveFactor。
	ReserveFactor float64
}

// Check 判定是否允许发起一次预计成本为 estimated 的调用；超预算返回 CodeBudgetExceeded。
func (g *BudgetGuard) Check(ctx context.Context, tenantID ids.ID, estimated float64) error {
	if g == nil || g.Ledger == nil {
		return nil
	}
	if g.Limit <= 0 {
		return errors.E(errors.CodeBudgetExceeded, "llm: 预算上限为 0，拒绝新增调用")
	}
	to := g.To
	if to.IsZero() {
		to = time.Now().UTC().Add(time.Second)
	}
	spent, _, err := g.Ledger.SumCost(ctx, tenantID, g.From, to)
	if err != nil {
		return err
	}
	factor := g.ReserveFactor
	if factor <= 0 {
		factor = DefaultReserveFactor
	}
	reserved := estimated * factor
	if spent+reserved > g.Limit {
		return errors.Ef(errors.CodeBudgetExceeded,
			"llm: 预算超限（已用 %.6f + 预留 %.6f（含上浮系数 %.2f）> 上限 %.6f）",
			spent, reserved, factor, g.Limit)
	}
	return nil
}

// pgUsageStore 是 usageStore 的 PostgreSQL 实现（RLS：llm.usage_ledger）。
type pgUsageStore struct{ pool *pgxpool.Pool }

const usageInsertSQL = `
INSERT INTO llm.usage_ledger (
    usage_id, tenant_id, run_id, step_id, request_id,
    endpoint_id, model_revision_id, input_tokens, output_tokens,
    usage_source, estimated_cost, currency, settlement_status
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::numeric,$12,$13)
ON CONFLICT (tenant_id, request_id) DO NOTHING`

func (s pgUsageStore) insert(ctx context.Context, tenantID ids.ID, u Usage) (bool, error) {
	inserted := false
	err := inTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, usageInsertSQL,
			ids.New().String(),
			tenantID.String(),
			u.RunID.String(),
			nullableID(u.StepID),
			u.RequestID,
			nullableID(u.EndpointID),
			nullableID(u.ModelRevisionID),
			u.InputTokens,
			u.OutputTokens,
			u.Source,
			u.EstimatedCost,
			u.Currency,
			u.SettlementStatus,
		)
		if err != nil {
			return err
		}
		inserted = tag.RowsAffected() == 1
		return nil
	})
	if err != nil {
		return false, errors.Wrap(err, errors.CodeInternal, "llm: 写入用量账本失败")
	}
	return inserted, nil
}

const usageSumSQL = `
SELECT COALESCE(SUM(estimated_cost), 0)::float8, COUNT(*)
  FROM llm.usage_ledger
 WHERE tenant_id = $1 AND created_at >= $2 AND created_at < $3`

func (s pgUsageStore) sumCost(ctx context.Context, tenantID ids.ID, from, to time.Time) (float64, int64, error) {
	var (
		total float64
		count int64
	)
	err := inTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, usageSumSQL, tenantID.String(), from, to).Scan(&total, &count)
	})
	if err != nil {
		return 0, 0, errors.Wrap(err, errors.CodeInternal, "llm: 汇总用量账本失败")
	}
	return total, count, nil
}

// nullableID 把零值 ID 转为 NULL（step_id / model_revision_id 可为空）。
func nullableID(id ids.ID) any {
	if id.IsZero() {
		return nil
	}
	return id.String()
}

// inTenantTx 在租户事务（SET LOCAL app.tenant_id）中执行 fn 并提交。
// llm.usage_ledger 启用 FORCE RLS，读写必须显式注入租户上下文。
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
