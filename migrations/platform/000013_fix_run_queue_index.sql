-- ODAP 平台迁移 000013：修正执行队列部分索引的可调度状态集
--
-- 背景：000005 建立的 idx_agent_runs_queue 覆盖 ('CREATED','READY','RECOVERING')，
-- 与第六篇 §3.1「Run 状态定义」的"是否可调度"列不一致：
--   - RUNNING 属于执行队列（执行中租约过期后需被重新领取）；
--   - CONTEXT_BUILDING 可调度（Worker 领取后推进上下文构建）；
--   - PLANNING / PLAN_VALIDATING 标记为"否"，不进入领取队列，其租约过期
--     由恢复扫描转 RECOVERING（第六篇 §14.2.3），再由恢复队列接管。
-- 若索引与调度器的 status = ANY(...) 集合不一致，Postgres 将无法使用该
-- 部分索引，队列领取在数据量增大后退化为全表扫描。
--
-- 采用追加迁移而非修改 000005：已应用的迁移脚本不可变。

DROP INDEX IF EXISTS runtime.idx_agent_runs_queue;

CREATE INDEX idx_agent_runs_queue
    ON runtime.agent_runs(status, priority DESC, available_at ASC)
    WHERE status IN (
        'CREATED', 'CONTEXT_BUILDING', 'READY', 'RUNNING', 'RECOVERING'
    );

-- 恢复扫描专用：按租约到期时间定位需要回收的任务（第六篇 §14.2.3）。
CREATE INDEX IF NOT EXISTS idx_agent_runs_lease_expiry
    ON runtime.agent_runs(tenant_id, lease_expires_at)
    WHERE lease_expires_at IS NOT NULL;

COMMENT ON INDEX runtime.idx_agent_runs_queue IS
    '执行/恢复队列领取索引；可调度状态集见第六篇 §3.1 与 contracts/01 §1.1';
