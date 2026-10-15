// Package outbox 实现事务性发件箱（Transactional Outbox）。
//
// 权威来源：docs/第十篇：任务事件总线、异步消息与分布式可靠执行详细设计.md
//   - §5.3 每聚合一行计数器：UPDATE ... RETURNING 行锁分配 aggregate_seq，
//     与聚合状态更新处于同一数据库事务；明确不用 SEQUENCE（回滚留空洞）
//     与 SELECT max(seq)+1（并发冲突）。
//   - §5.4 Publisher 两段式：先在短事务内领取并置 DISPATCHING，再在事务外
//     调用消息代理，最后用短事务标记 SENT / FAILED；空转指数退避初始 100ms、
//     倍数 2、上限 5s，批量上限 100 条/批。
//   - §17 超过最大尝试次数的事件置 DEAD，转入死信处理。
//
// 一致性边界：Outbox 保证"业务状态与待发布事件不会只提交一边"，不保证
// 只发布一次；下游按事件 ID 去重（§6）。重复发布不视为数据损坏。
package outbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/platform/clock"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
)

// 发件箱运行常量（第十篇 §5.4 / §10.2 / §17）。
const (
	// DefaultBatchSize 是 Publisher 单批领取的默认上限（§5.4：100 条/批）。
	DefaultBatchSize = 100
	// MaxBatchSize 是单批领取的硬上限，任何配置都不得超过它（§5.4）。
	MaxBatchSize = 100
	// MaxAttempts 是单条事件的最大发布尝试次数，超过即置 DEAD（§5.4 / §17）。
	MaxAttempts = 5

	// ClaimTTL 是 DISPATCHING 领取租约时长（§5.4 示例为 30 秒）。
	ClaimTTL = 30 * time.Second

	backoffInitial = 100 * time.Millisecond
	backoffFactor  = 2
	backoffCap     = 5 * time.Second
)

// Event 是一条待发件箱事件（与 execution.task_outbox 行对应）。
type Event struct {
	EventID       ids.ID
	TenantID      ids.ID
	AggregateType string
	AggregateID   ids.ID
	AggregateSeq  int64
	EventType     string
	SchemaVersion string
	Payload       json.RawMessage
	PayloadDigest string
	OccurredAt    time.Time
}

// digestOf 计算载荷摘要：SHA-256 十六进制小写。
func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Backoff 返回第 attempt 次尝试（1 起）失败后的退避时长。
// 初始 100ms，倍数 2，上限 5s（§5.4 空转/失败退避；§10.2 指数退避）。
// 导出以便测试断言与调用方复用。attempt < 1 视为 1。
func Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := backoffInitial
	for i := 1; i < attempt; i++ {
		if d >= backoffCap {
			return backoffCap
		}
		d *= backoffFactor
	}
	if d > backoffCap {
		d = backoffCap
	}
	return d
}

// normalizeBatch 把配置批量收敛到 [1, MaxBatchSize]，<=0 取默认值。
func normalizeBatch(n int) int {
	if n <= 0 {
		return DefaultBatchSize
	}
	if n > MaxBatchSize {
		return MaxBatchSize
	}
	return n
}

// asAppError 直接判定错误是否已是平台 AppError（避免二次包装丢失错误码）。
func asAppError(err error) (*errors.AppError, bool) {
	ae, ok := err.(*errors.AppError)
	return ae, ok
}

// wrapInternal 仅在错误尚未携带稳定错误码时包装为 INTERNAL。
func wrapInternal(err error, msg string) error {
	if err == nil {
		return nil
	}
	if _, ok := asAppError(err); ok {
		return err
	}
	return errors.Wrap(err, errors.CodeInternal, msg)
}

// inTenantTx 在租户事务（SET LOCAL app.tenant_id）中执行 fn 并提交。
// execution.task_outbox / consumer_inbox 等表启用了 FORCE RLS，读写必须
// 显式注入租户上下文，否则行级策略会过滤掉全部行或拒绝写入。
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

// ---------------------------------------------------------------------------
// Writer：在调用方业务事务内追加事件（§5.3）
// ---------------------------------------------------------------------------

// writerStore 抽象"分配聚合序号 + 写入发件箱行"两个持久化原语。
// 生产实现 pgWriterStore 通过 pgx 作用于调用方事务；单元测试使用内存实现，
// 从而不依赖真实数据库。tx 为调用方业务事务，内存实现可忽略之。
type writerStore interface {
	allocSeq(ctx context.Context, tx pgx.Tx, aggregateType string, aggregateID ids.ID) (int64, error)
	insertTx(ctx context.Context, tx pgx.Tx, ev Event) error
}

// Writer 在调用方业务事务内追加发件箱事件。
type Writer struct {
	Pool *pgxpool.Pool

	store writerStore // 测试注入点；nil 时使用 PG 实现
}

// store 返回当前使用的持久化实现（PG 或内存测试实现）。
func (w *Writer) resolveStore() (writerStore, error) {
	if w.store != nil {
		return w.store, nil
	}
	if w.Pool == nil {
		return nil, errors.E(errors.CodeInternal, "outbox: writer has no pool")
	}
	return &pgWriterStore{}, nil
}

// AppendTx 在调用方已有的业务事务内追加事件（§5.3）。
//
// 步骤：
//  1. 领取 aggregate_seq：INSERT ... ON CONFLICT DO UPDATE ... RETURNING
//     next_seq - 1，行锁在事务提交前持续持有，同一聚合内不会出现两个并发
//     事务领到相同序号；状态更新回滚时计数器同步回滚，不留空洞。
//  2. 以领取到的 seq 写入 execution.task_outbox（status 默认 PENDING）。
//
// 返回带 seq 的事件。TenantID 必须非零，SQL 依赖 RLS 会话上下文。
func (w *Writer) AppendTx(ctx context.Context, tx pgx.Tx, ev Event) (Event, error) {
	if w == nil {
		return ev, errors.E(errors.CodeInternal, "outbox: nil writer")
	}
	if err := validateEvent(ev); err != nil {
		return ev, err
	}
	ev = fillDefaults(ev)
	st, err := w.resolveStore()
	if err != nil {
		return ev, err
	}
	seq, err := st.allocSeq(ctx, tx, ev.AggregateType, ev.AggregateID)
	if err != nil {
		return ev, wrapInternal(err, "outbox: allocate aggregate_seq")
	}
	ev.AggregateSeq = seq
	if err := st.insertTx(ctx, tx, ev); err != nil {
		return ev, wrapInternal(err, "outbox: insert outbox event")
	}
	return ev, nil
}

// Append 是 AppendTx 的便捷包装：自行开启租户事务后写入。
func (w *Writer) Append(ctx context.Context, tenantID ids.ID, ev Event) (Event, error) {
	if w == nil || w.Pool == nil {
		return ev, errors.E(errors.CodeInternal, "outbox: writer has no pool")
	}
	if tenantID.IsZero() {
		return ev, errors.E(errors.CodeValidationFailed, "outbox: tenantID must not be zero")
	}
	ev.TenantID = tenantID
	var out Event
	err := inTenantTx(ctx, w.Pool, tenantID, func(tx pgx.Tx) error {
		var e error
		out, e = w.AppendTx(ctx, tx, ev)
		return e
	})
	if err != nil {
		return ev, wrapInternal(err, "outbox: append")
	}
	return out, nil
}

// validateEvent 校验事件必填字段，缺失按 fail-closed 拒绝。
func validateEvent(ev Event) error {
	if ev.TenantID.IsZero() {
		return errors.E(errors.CodeValidationFailed, "outbox: tenantID must not be zero")
	}
	if ev.AggregateType == "" {
		return errors.E(errors.CodeValidationFailed, "outbox: aggregateType must not be empty")
	}
	if ev.AggregateID.IsZero() {
		return errors.E(errors.CodeValidationFailed, "outbox: aggregateID must not be zero")
	}
	if ev.EventType == "" {
		return errors.E(errors.CodeValidationFailed, "outbox: eventType must not be empty")
	}
	return nil
}

// fillDefaults 补齐可选字段：事件 ID、载荷摘要、schema 版本、发生时间与载荷。
func fillDefaults(ev Event) Event {
	if ev.EventID.IsZero() {
		ev.EventID = ids.New()
	}
	if len(ev.Payload) == 0 {
		ev.Payload = json.RawMessage("{}")
	}
	if ev.PayloadDigest == "" {
		ev.PayloadDigest = digestOf(ev.Payload)
	}
	if ev.SchemaVersion == "" {
		ev.SchemaVersion = "v1"
	}
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = time.Now().UTC()
	}
	return ev
}

// pgWriterStore 是 writerStore 的 PostgreSQL 实现（作用于调用方事务）。
type pgWriterStore struct{}

// allocSeqSQL 领取聚合序号（§5.3）：先 INSERT 占位 next_seq=2 返回 1，
// 冲突时行锁自增并返回旧值 next_seq-1。禁止 SEQUENCE 与 MAX+1。
const allocSeqSQL = `
INSERT INTO execution.aggregate_seq_counters (aggregate_type, aggregate_id, next_seq)
VALUES ($1, $2, 2)
ON CONFLICT (aggregate_type, aggregate_id)
DO UPDATE SET next_seq = execution.aggregate_seq_counters.next_seq + 1,
              updated_at = now()
RETURNING next_seq - 1`

func (pgWriterStore) allocSeq(ctx context.Context, tx pgx.Tx, aggregateType string, aggregateID ids.ID) (int64, error) {
	var seq int64
	if err := tx.QueryRow(ctx, allocSeqSQL, aggregateType, aggregateID.String()).Scan(&seq); err != nil {
		return 0, err
	}
	return seq, nil
}

// insertOutboxSQL 写入发件箱事件行，status 默认 PENDING，available_at 默认 now()。
const insertOutboxSQL = `
INSERT INTO execution.task_outbox
    (event_id, tenant_id, aggregate_type, aggregate_id, aggregate_seq,
     event_type, schema_version, payload, payload_digest, occurred_at,
     status, attempt_count)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'PENDING', 0)`

func (pgWriterStore) insertTx(ctx context.Context, tx pgx.Tx, ev Event) error {
	_, err := tx.Exec(ctx, insertOutboxSQL,
		ev.EventID.String(), ev.TenantID.String(), ev.AggregateType,
		ev.AggregateID.String(), ev.AggregateSeq, ev.EventType,
		ev.SchemaVersion, string(ev.Payload), ev.PayloadDigest, ev.OccurredAt)
	return err
}

// ---------------------------------------------------------------------------
// Publisher：两段式并发领取与发布（§5.4）
// ---------------------------------------------------------------------------

// Sink 是消息代理的抽象；Publish 返回 nil 表示代理已接受该事件。
type Sink interface {
	Publish(ctx context.Context, ev Event) error
}

// claimedEvent 是一条领取结果及其已累计的尝试次数（内部类型，不污染 Event 形状）。
type claimedEvent struct {
	Ev           Event
	AttemptCount int
}

// dispatchStore 抽象 Publisher 的持久化原语。
// 生产实现 pgDispatchStore 通过 pgx 访问 execution.task_outbox；
// 单元测试使用内存实现，从而不依赖真实数据库。
type dispatchStore interface {
	claim(ctx context.Context, tenantID ids.ID, owner string, batch int, now time.Time, ttl time.Duration) ([]claimedEvent, error)
	markSent(ctx context.Context, tenantID ids.ID, eventIDs []ids.ID, at time.Time) error
	markFailed(ctx context.Context, tenantID ids.ID, eventID ids.ID, errCode string, at, nextAvailable time.Time, dead bool) error
	reclaimExpired(ctx context.Context, tenantID ids.ID, now time.Time, maxAttempts int) (int, error)
}

// Publisher 领取并发布发件箱事件（两段式，§5.4）。
type Publisher struct {
	Pool      *pgxpool.Pool
	Clock     clock.Clock
	Sink      Sink
	Owner     string
	BatchSize int

	store dispatchStore // 测试注入点；nil 时使用 PG 实现
}

// resolveStore 返回当前使用的持久化实现（PG 或内存测试实现）。
func (p *Publisher) resolveStore() (dispatchStore, error) {
	if p.store != nil {
		return p.store, nil
	}
	if p.Pool == nil {
		return nil, errors.E(errors.CodeInternal, "outbox: publisher has no pool")
	}
	return &pgDispatchStore{pool: p.Pool}, nil
}

// RunOnce 执行一轮领取-发布-标记，返回本轮成功发布的事件数。
//
// 两段式（§5.4）：
//   - 第一段：短事务领取至多 BatchSize 条 PENDING 事件并置 DISPATCHING；
//   - 第二段：事务外逐条调用 Sink.Publish，单条失败不阻断其余；
//   - 第三段：短事务标记成功为 SENT，失败为 FAILED（超过上限则 DEAD）并写回退避。
func (p *Publisher) RunOnce(ctx context.Context, tenantID ids.ID) (int, error) {
	if p == nil {
		return 0, errors.E(errors.CodeInternal, "outbox: nil publisher")
	}
	if tenantID.IsZero() {
		return 0, errors.E(errors.CodeValidationFailed, "outbox: tenantID must not be zero")
	}
	if p.Owner == "" {
		return 0, errors.E(errors.CodeInternal, "outbox: publisher owner must not be empty")
	}
	if p.Sink == nil {
		return 0, errors.E(errors.CodeInternal, "outbox: publisher sink must not be nil")
	}
	st, err := p.resolveStore()
	if err != nil {
		return 0, err
	}
	clk := p.Clock
	if clk == nil {
		clk = clock.RealClock{}
	}
	now := clk.Now()
	claimed, err := st.claim(ctx, tenantID, p.Owner, normalizeBatch(p.BatchSize), now, ClaimTTL)
	if err != nil {
		return 0, wrapInternal(err, "outbox: claim pending events")
	}

	sent := 0
	for _, c := range claimed {
		pubErr := p.Sink.Publish(ctx, c.Ev)
		newAttempt := c.AttemptCount + 1
		if pubErr == nil {
			if err := st.markSent(ctx, tenantID, []ids.ID{c.Ev.EventID}, now); err != nil {
				return sent, wrapInternal(err, "outbox: mark sent")
			}
			sent++
			continue
		}
		code := errors.CodeInternal
		if ae, ok := asAppError(pubErr); ok {
			code = ae.Code
		}
		dead := newAttempt > MaxAttempts
		next := now.Add(Backoff(newAttempt))
		if err := st.markFailed(ctx, tenantID, c.Ev.EventID, code, now, next, dead); err != nil {
			return sent, wrapInternal(err, "outbox: mark failed")
		}
	}
	return sent, nil
}

// ReclaimExpired 回收 claim_until 已过期的 DISPATCHING 事件（§5.4）：
// 领取后进程崩溃会留下 DISPATCHING 残行；按 attempt_count 判定回 PENDING
// 或置 DEAD，避免事件永久卡死在 DISPATCHING。
func (p *Publisher) ReclaimExpired(ctx context.Context, tenantID ids.ID) (int, error) {
	if p == nil {
		return 0, errors.E(errors.CodeInternal, "outbox: nil publisher")
	}
	if tenantID.IsZero() {
		return 0, errors.E(errors.CodeValidationFailed, "outbox: tenantID must not be zero")
	}
	st, err := p.resolveStore()
	if err != nil {
		return 0, err
	}
	clk := p.Clock
	if clk == nil {
		clk = clock.RealClock{}
	}
	n, err := st.reclaimExpired(ctx, tenantID, clk.Now(), MaxAttempts)
	if err != nil {
		return 0, wrapInternal(err, "outbox: reclaim expired claims")
	}
	return n, nil
}

// pgDispatchStore 是 dispatchStore 的 PostgreSQL 实现。
type pgDispatchStore struct {
	pool *pgxpool.Pool
}

// claimSQL 领取待发布事件（§5.4 第一段）：FOR UPDATE SKIP LOCKED 使多个
// Publisher 领取互不相同的行；attempt_count 的累加放在标记阶段完成，
// 保证每条事件每次尝试只记一次。
const claimSQL = `
WITH picked AS (
    SELECT event_id
      FROM execution.task_outbox
     WHERE tenant_id = $1
       AND status = 'PENDING'
       AND available_at <= $4
     ORDER BY available_at, created_at
     FOR UPDATE SKIP LOCKED
     LIMIT $3
)
UPDATE execution.task_outbox AS o
   SET status = 'DISPATCHING',
       claimed_by = $2,
       claim_until = $4 + ($5::bigint * interval '1 millisecond')
  FROM picked
 WHERE o.event_id = picked.event_id
RETURNING o.event_id, o.tenant_id, o.aggregate_type, o.aggregate_id,
          o.aggregate_seq, o.event_type, o.schema_version, o.payload,
          o.payload_digest, o.occurred_at, o.attempt_count`

func (s *pgDispatchStore) claim(ctx context.Context, tenantID ids.ID, owner string, batch int, now time.Time, ttl time.Duration) ([]claimedEvent, error) {
	var out []claimedEvent
	err := inTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, claimSQL, tenantID.String(), owner, batch, now, ttl.Milliseconds())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				c        claimedEvent
				idStr    string
				tenStr   string
				aggIDStr string
				payload  string
			)
			if err := rows.Scan(&idStr, &tenStr, &c.Ev.AggregateType, &aggIDStr,
				&c.Ev.AggregateSeq, &c.Ev.EventType, &c.Ev.SchemaVersion,
				&payload, &c.Ev.PayloadDigest, &c.Ev.OccurredAt, &c.AttemptCount); err != nil {
				return err
			}
			if c.Ev.EventID, err = ids.Parse(idStr); err != nil {
				return err
			}
			if c.Ev.TenantID, err = ids.Parse(tenStr); err != nil {
				return err
			}
			if c.Ev.AggregateID, err = ids.Parse(aggIDStr); err != nil {
				return err
			}
			c.Ev.Payload = json.RawMessage(payload)
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// markSentSQL 标记事件已成功发布（§5.4 第三段）。
const markSentSQL = `
UPDATE execution.task_outbox
   SET status = 'SENT',
       published_at = $3,
       attempt_count = attempt_count + 1
 WHERE tenant_id = $1
   AND event_id = ANY($2)
   AND status = 'DISPATCHING'`

func (s *pgDispatchStore) markSent(ctx context.Context, tenantID ids.ID, eventIDs []ids.ID, at time.Time) error {
	if len(eventIDs) == 0 {
		return nil
	}
	strs := make([]string, len(eventIDs))
	for i, id := range eventIDs {
		strs[i] = id.String()
	}
	return inTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, markSentSQL, tenantID.String(), strs, at)
		return err
	})
}

// markFailedSQL 标记发布失败，累加尝试次数、记录错误码与退避后的可用时间；
// dead 为真时置 DEAD（§5.4 / §17 超过最大尝试次数转入死信）。
const markFailedSQL = `
UPDATE execution.task_outbox
   SET status = $3,
       attempt_count = attempt_count + 1,
       last_error_code = $4,
       last_error_at = $5,
       available_at = $6,
       claimed_by = NULL,
       claim_until = NULL
 WHERE tenant_id = $1
   AND event_id = $2`

func (s *pgDispatchStore) markFailed(ctx context.Context, tenantID ids.ID, eventID ids.ID, errCode string, at, nextAvailable time.Time, dead bool) error {
	status := "FAILED"
	if dead {
		status = "DEAD"
	}
	return inTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, markFailedSQL, tenantID.String(), eventID.String(), status, errCode, at, nextAvailable)
		return err
	})
}

// reclaimSQL 回收过期 DISPATCHING（§5.4）：attempt_count 已达上限的置 DEAD，
// 其余回 PENDING 并立即可再次领取。
const reclaimSQL = `
UPDATE execution.task_outbox
   SET status = CASE WHEN attempt_count >= $3 THEN 'DEAD' ELSE 'PENDING' END,
       claimed_by = NULL,
       claim_until = NULL,
       available_at = CASE WHEN attempt_count >= $3 THEN available_at ELSE $2 END
 WHERE tenant_id = $1
   AND status = 'DISPATCHING'
   AND claim_until < $2`

func (s *pgDispatchStore) reclaimExpired(ctx context.Context, tenantID ids.ID, now time.Time, maxAttempts int) (int, error) {
	var n int
	err := inTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, reclaimSQL, tenantID.String(), now, maxAttempts)
		if err != nil {
			return err
		}
		n = int(tag.RowsAffected())
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}
