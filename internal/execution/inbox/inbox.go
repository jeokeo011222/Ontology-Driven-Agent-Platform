// Package inbox 实现消费者幂等（Inbox 模式）。
//
// 权威来源：docs/第十篇 §6（消费者幂等：把重复投递变成安全的重复处理）、
// §6.2（Inbox 增长治理：dedup 表承载唯一约束、分区表不承载全局唯一）、
// §6.3（正确的消费事务）、§17（死信处置）。
//
// 可靠性模型（§6）：至少一次投递 + 幂等消费，而不是依赖消息代理提供
// "恰好一次"。重复投递是正常现象，必须被安全地吸收。
//
// 去重与增长治理（§6.2）：唯一约束落在不分区的 execution.consumer_inbox_dedup
// （(consumer_name, event_id) 主键，插入冲突即判重）；按 received_at 分区的
// execution.consumer_inbox 只保存处理记录与诊断，不承载全局唯一约束。
package inbox

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/platform/clock"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
)

// Event 是一条投递到消费者的入站事件。
type Event struct {
	EventID       ids.ID
	TenantID      ids.ID
	EventType     string
	Payload       json.RawMessage
	PayloadDigest string
	OccurredAt    time.Time
}

// Handler 处理一条事件。返回 error 表示处理失败，事件将登记死信。
type Handler interface {
	Handle(ctx context.Context, ev Event) error
}

// inboxStore 抽象 Inbox 的持久化原语。生产实现 pgInboxStore 通过 pgx 访问
// execution.* 表；单元测试使用内存实现，从而不依赖真实数据库。
type inboxStore interface {
	// dedupInsert 抢注去重键；返回 true 表示本次是首次处理（未见过该事件）。
	dedupInsert(ctx context.Context, consumerName string, ev Event) (bool, error)
	// insertReceived 写入处理记录并置 RECEIVED。
	insertReceived(ctx context.Context, consumerName string, ev Event) error
	// markProcessed 标记处理成功。
	markProcessed(ctx context.Context, consumerName string, ev Event, at time.Time) error
	// markFailed 标记处理失败并记录错误。
	markFailed(ctx context.Context, consumerName string, ev Event, errMsg string) error
	// upsertDeadLetter 登记（或累加）死信；明文 payload 不入死信表。
	upsertDeadLetter(ctx context.Context, consumerName string, ev Event, reason string) error
}

// Consumer 是一个幂等消费者实例。
type Consumer struct {
	Pool    *pgxpool.Pool
	Clock   clock.Clock
	Name    string
	Handler Handler

	store inboxStore // 测试注入点；nil 时使用 PG 实现
}

// resolveStore 返回当前使用的持久化实现（PG 或内存测试实现）。
func (c *Consumer) resolveStore() (inboxStore, error) {
	if c.store != nil {
		return c.store, nil
	}
	if c.Pool == nil {
		return nil, errors.E(errors.CodeInternal, "inbox: consumer has no pool")
	}
	return &pgInboxStore{pool: c.Pool}, nil
}

// Process 幂等处理一条事件（§6.2 / §6.3 的 MVP 落地）。
//
// 步骤：
//  1. 抢注去重键：INSERT ... ON CONFLICT DO NOTHING 到 consumer_inbox_dedup；
//     RowsAffected()==0 表示该 (consumer_name, event_id) 已处理过 → 幂等短路返回 nil。
//  2. INSERT consumer_inbox（status='RECEIVED'）。
//  3. 调用 Handler.Handle：
//     成功 → UPDATE status='PROCESSED', processed_at=now()；
//     失败 → UPDATE status='FAILED', last_error=...，并向 execution.event_dead_letters
//     登记 (payload_ref="inbox://<consumer>/<event_id>", payload_digest)，
//     明文 payload 不入死信表（§17.2）。
//
// 事务边界说明（§6.3）：去重键与处理记录各自持久化，Handler 在事务之外执行，
// 因此本实现为"失败即终态"的 MVP 语义——失败事件进入死信等待人工处置（§17），
// 再次投递会被去重键短路。需要自动重试的消费者应在 Handler 内部实现有界重试。
func (c *Consumer) Process(ctx context.Context, ev Event) error {
	if c == nil {
		return errors.E(errors.CodeInternal, "inbox: nil consumer")
	}
	if c.Name == "" {
		return errors.E(errors.CodeInternal, "inbox: consumer name must not be empty")
	}
	if c.Handler == nil {
		return errors.E(errors.CodeInternal, "inbox: consumer handler must not be nil")
	}
	if ev.EventID.IsZero() {
		return errors.E(errors.CodeValidationFailed, "inbox: eventID must not be zero")
	}
	if ev.TenantID.IsZero() {
		return errors.E(errors.CodeValidationFailed, "inbox: tenantID must not be zero")
	}
	st, err := c.resolveStore()
	if err != nil {
		return err
	}

	// ① 抢注去重键。
	first, err := st.dedupInsert(ctx, c.Name, ev)
	if err != nil {
		return wrapInternal(err, "inbox: dedup insert")
	}
	if !first {
		// 已处理过：幂等短路，不再调用 Handler。
		return nil
	}

	// ② 写入处理记录 RECEIVED。
	if err := st.insertReceived(ctx, c.Name, ev); err != nil {
		return wrapInternal(err, "inbox: insert received")
	}

	// ③ 调用业务处理器。
	handleErr := c.Handler.Handle(ctx, ev)
	if handleErr == nil {
		clk := c.Clock
		if clk == nil {
			clk = clock.RealClock{}
		}
		if err := st.markProcessed(ctx, c.Name, ev, clk.Now()); err != nil {
			return wrapInternal(err, "inbox: mark processed")
		}
		return nil
	}

	// 处理失败：标记 FAILED 并登记死信（明文不上库）。
	if err := st.markFailed(ctx, c.Name, ev, handleErr.Error()); err != nil {
		return wrapInternal(err, "inbox: mark failed")
	}
	if err := st.upsertDeadLetter(ctx, c.Name, ev, handleErr.Error()); err != nil {
		return wrapInternal(err, "inbox: upsert dead letter")
	}
	return wrapInternal(handleErr, "inbox: handler failed")
}

// wrapInternal 仅在错误尚未携带稳定错误码时包装为 INTERNAL。
func wrapInternal(err error, msg string) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*errors.AppError); ok {
		return err
	}
	return errors.Wrap(err, errors.CodeInternal, msg)
}

// inTenantTx 在租户事务（SET LOCAL app.tenant_id）中执行 fn 并提交。
// execution.* 表启用了 FORCE RLS，读写必须显式注入租户上下文。
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

// payloadRef 构造受控载荷引用（§17.2）：明文 payload 不入死信表，只存引用与摘要。
func payloadRef(consumerName string, ev Event) string {
	return fmt.Sprintf("inbox://%s/%s", consumerName, ev.EventID.String())
}

// pgInboxStore 是 inboxStore 的 PostgreSQL 实现。
type pgInboxStore struct {
	pool *pgxpool.Pool
}

// dedupSQL 抢注去重键：ON CONFLICT DO NOTHING，冲突即已处理过（§6.2）。
const dedupSQL = `
INSERT INTO execution.consumer_inbox_dedup (consumer_name, event_id, tenant_id)
VALUES ($1, $2, $3)
ON CONFLICT (consumer_name, event_id) DO NOTHING`

func (s *pgInboxStore) dedupInsert(ctx context.Context, consumerName string, ev Event) (bool, error) {
	inserted := false
	err := inTenantTx(ctx, s.pool, ev.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, dedupSQL, consumerName, ev.EventID.String(), ev.TenantID.String())
		if err != nil {
			return err
		}
		inserted = tag.RowsAffected() == 1
		return nil
	})
	return inserted, err
}

// insertReceivedSQL 写入处理记录（分区表按 received_at 落分区，DEFAULT 兜底）。
const insertReceivedSQL = `
INSERT INTO execution.consumer_inbox
    (consumer_name, event_id, tenant_id, payload_digest, status)
VALUES ($1, $2, $3, $4, 'RECEIVED')`

func (s *pgInboxStore) insertReceived(ctx context.Context, consumerName string, ev Event) error {
	return inTenantTx(ctx, s.pool, ev.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, insertReceivedSQL,
			consumerName, ev.EventID.String(), ev.TenantID.String(), ev.PayloadDigest)
		return err
	})
}

const markProcessedSQL = `
UPDATE execution.consumer_inbox
   SET status = 'PROCESSED',
       processed_at = $3,
       last_error = NULL
 WHERE consumer_name = $1
   AND event_id = $2`

func (s *pgInboxStore) markProcessed(ctx context.Context, consumerName string, ev Event, at time.Time) error {
	return inTenantTx(ctx, s.pool, ev.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, markProcessedSQL, consumerName, ev.EventID.String(), at)
		return err
	})
}

const markFailedSQL = `
UPDATE execution.consumer_inbox
   SET status = 'FAILED',
       last_error = $3
 WHERE consumer_name = $1
   AND event_id = $2`

func (s *pgInboxStore) markFailed(ctx context.Context, consumerName string, ev Event, errMsg string) error {
	return inTenantTx(ctx, s.pool, ev.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, markFailedSQL, consumerName, ev.EventID.String(), errMsg)
		return err
	})
}

// upsertDeadLetterSQL 登记死信；重复登记按 (consumer_name, event_id) 唯一约束
// 累加 attempt_count 并刷新失败原因，不违反唯一约束（§17）。
const upsertDeadLetterSQL = `
INSERT INTO execution.event_dead_letters
    (dead_letter_id, tenant_id, consumer_name, event_id, event_type,
     payload_ref, payload_digest, failure_reason, attempt_count, status)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 1, 'PENDING')
ON CONFLICT (consumer_name, event_id)
DO UPDATE SET attempt_count = execution.event_dead_letters.attempt_count + 1,
              failure_reason = EXCLUDED.failure_reason`

func (s *pgInboxStore) upsertDeadLetter(ctx context.Context, consumerName string, ev Event, reason string) error {
	return inTenantTx(ctx, s.pool, ev.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, upsertDeadLetterSQL,
			ids.New().String(), ev.TenantID.String(), consumerName, ev.EventID.String(),
			ev.EventType, payloadRef(consumerName, ev), ev.PayloadDigest, reason)
		return err
	})
}
