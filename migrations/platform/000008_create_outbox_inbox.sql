-- ODAP 平台迁移 000008：execution schema（Outbox/Inbox/序号计数器/死信）
-- 依据：第十篇 §5.3-§5.5、§6.2、§17（含 P0-8 修复：每聚合计数器行锁分配、Publisher 两段式）

CREATE SCHEMA IF NOT EXISTS execution;

CREATE TABLE execution.task_outbox (
    event_id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,

    aggregate_type TEXT NOT NULL,
    aggregate_id UUID NOT NULL,
    aggregate_seq BIGINT NOT NULL,

    event_type TEXT NOT NULL,
    schema_version TEXT NOT NULL,
    payload JSONB NOT NULL,
    payload_digest TEXT NOT NULL,

    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    status TEXT NOT NULL DEFAULT 'PENDING',
    attempt_count INT NOT NULL DEFAULT 0,
    published_at TIMESTAMPTZ,

    claimed_by TEXT,
    claim_until TIMESTAMPTZ,
    last_error_code TEXT,
    last_error_at TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (
        tenant_id,
        aggregate_type,
        aggregate_id,
        aggregate_seq
    ),

    CHECK (status IN (
        'PENDING',
        'DISPATCHING',
        'SENT',
        'FAILED',
        'DEAD'
    ))
);

CREATE INDEX idx_outbox_pending
    ON execution.task_outbox (available_at, created_at)
    WHERE status = 'PENDING';

CREATE INDEX idx_outbox_expired_claim
    ON execution.task_outbox (claim_until)
    WHERE status = 'DISPATCHING';

-- aggregate_seq 每聚合一行计数器：UPDATE ... RETURNING 行锁分配，与状态更新同事务。
-- 明确不用 SEQUENCE（回滚留空洞）与 SELECT max+1（并发冲突）。
CREATE TABLE execution.aggregate_seq_counters (
    aggregate_type TEXT NOT NULL,
    aggregate_id UUID NOT NULL,
    next_seq BIGINT NOT NULL DEFAULT 1,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (aggregate_type, aggregate_id)
);

-- Inbox 去重键表：不分区，主键即全局唯一约束（第十篇 §6.2 增长治理）
CREATE TABLE execution.consumer_inbox_dedup (
    consumer_name TEXT NOT NULL,
    event_id UUID NOT NULL,
    tenant_id UUID NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (consumer_name, event_id)
);

-- Inbox 处理记录表：按 received_at 分区，不承载全局唯一约束（去重由 dedup 表保证）
CREATE TABLE execution.consumer_inbox (
    consumer_name TEXT NOT NULL,
    event_id UUID NOT NULL,
    tenant_id UUID NOT NULL,

    payload_digest TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'RECEIVED',

    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at TIMESTAMPTZ,
    last_error TEXT,

    CHECK (status IN (
        'RECEIVED',
        'PROCESSED',
        'FAILED'
    ))
) PARTITION BY RANGE (received_at);

-- 死信表（第十篇 §17）：payload_ref 指向受控存储，明文不入库
CREATE TABLE execution.event_dead_letters (
    dead_letter_id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    consumer_name TEXT NOT NULL,
    event_id UUID NOT NULL,
    event_type TEXT NOT NULL,
    payload_ref TEXT NOT NULL,
    payload_digest TEXT NOT NULL,
    failure_reason TEXT NOT NULL,
    attempt_count INT NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'PENDING'
        CHECK (status IN ('PENDING', 'INVESTIGATING', 'RESOLVED', 'DISCARDED')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at TIMESTAMPTZ,

    UNIQUE (consumer_name, event_id)
);

CREATE INDEX idx_dead_letters_status
    ON execution.event_dead_letters(tenant_id, status, created_at);
