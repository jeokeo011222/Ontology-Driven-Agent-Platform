-- ODAP 平台迁移 000014：修复 Inbox 分区缺失 + 补齐 execution 租户隔离
--
-- 缺陷一（阻断性）：000008 把 execution.consumer_inbox 声明为
-- PARTITION BY RANGE (received_at)，但从未创建任何分区。Postgres 对分区表
-- 的插入在找不到匹配分区时会直接报
--   ERROR: no partition of relation "consumer_inbox" found for row
-- 即 Inbox 完全无法写入。本迁移补齐按月分区，并创建 DEFAULT 分区作为兜底，
-- 保证任何时间戳的事件都能落盘而不是让消费者失败。
--
-- 缺陷二（隔离缺口）：000011 只对 execution.task_outbox 与
-- execution.event_dead_letters 启用了租户 RLS，execution.consumer_inbox 与
-- execution.consumer_inbox_dedup 被遗漏。两者都有 tenant_id 列，且承载
-- 跨租户的事件处理记录，必须一并纳入隔离。
--   说明：execution.aggregate_seq_counters 无 tenant_id 列（主键为
--   aggregate_type + aggregate_id），无法启用行级策略；它只保存单调计数器、
--   不含业务载荷，且 aggregate_id 为 UUID，跨租户撞键在实际上不可行。
--   该表的隔离由「aggregate_id 只能来自可信上下文」这一调用约定保证，
--   此处显式记录该残余风险（见设计书评审报告 §遗留）。

-- ---------- 缺陷一：创建 Inbox 分区 ----------

-- 覆盖 2026-10 至 2027-12 的按月分区（MVP 与试点期），
-- 由分区裁剪保证查询走单分区扫描。
DO $$
DECLARE
    start_month DATE := DATE '2026-10-01';
    end_month   DATE := DATE '2028-01-01';
    cur         DATE := start_month;
    part_name   TEXT;
BEGIN
    WHILE cur < end_month LOOP
        part_name := 'consumer_inbox_' || to_char(cur, 'YYYY_MM');
        IF NOT EXISTS (
            SELECT 1 FROM pg_class c
            JOIN pg_namespace n ON n.oid = c.relnamespace
            WHERE n.nspname = 'execution' AND c.relname = part_name
        ) THEN
            EXECUTE format(
                'CREATE TABLE execution.%I PARTITION OF execution.consumer_inbox
                    FOR VALUES FROM (%L) TO (%L)',
                part_name, cur, cur + INTERVAL '1 month'
            );
        END IF;
        cur := cur + INTERVAL '1 month';
    END LOOP;

    -- DEFAULT 分区：兜底，保证范围外时间戳（时钟漂移、回放历史事件）
    -- 仍能落盘。写入 DEFAULT 意味着分区维护滞后，应由运维监控告警，
    -- 而不是让消费者写失败丢事件。
    IF NOT EXISTS (
        SELECT 1 FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = 'execution' AND c.relname = 'consumer_inbox_default'
    ) THEN
        EXECUTE 'CREATE TABLE execution.consumer_inbox_default
                    PARTITION OF execution.consumer_inbox DEFAULT';
    END IF;
END $$;

-- ---------- 缺陷二：补齐租户隔离 ----------

SELECT app.apply_tenant_rls('execution', 'consumer_inbox');
SELECT app.apply_tenant_rls('execution', 'consumer_inbox_dedup');

COMMENT ON TABLE execution.aggregate_seq_counters IS
    '无 tenant_id 列，无法启用 RLS；隔离依赖调用方只能传入可信上下文中的 aggregate_id（残余风险已记录）';
