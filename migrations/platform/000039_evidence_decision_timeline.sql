-- 000039: 证据记录的时间轴三列（决策一等节点模型的落点，借鉴竞品决策审计设计）
--
-- 借鉴点（docs/onto-mind竞品借鉴评估报告.md §缺口⑧）：
--   一次智能体运行 = 一个 task；运行内的每次工具调用 = 一个步骤决策。
--   根记录 tool_name 为空、步骤记录 tool_name 非空——一个 GROUP BY 就能还原
--   「任务 → 步骤时间轴」，列表查询默认排除步骤记录防淹没。
--
-- 与竞品的差异（刻意收窄）：
--   decision_type 采用其五分类词表（QUERY/UPDATE/INFER/ACTION/TOOL），
--   但 PROV-O 溯源命名图**不在本迁移落地**：当前生产还没有决策写入方
--   （internal/agent 尚未接线），先建图写路径只会再造一个"实现完备、
--   生产 import 为 0"的悬空包——那正是装配可达性门禁要拦的缺陷类。
--   等 agent 运行时接线时一并补图侧写入（见任务 #150 备注）。

ALTER TABLE audit.evidence_records
    ADD COLUMN IF NOT EXISTS decision_type TEXT,
    ADD COLUMN IF NOT EXISTS task_id TEXT,
    ADD COLUMN IF NOT EXISTS tool_name TEXT;

-- 词表收口：五分类之外一律拒绝（fail-closed，与状态词表门禁同一纪律）。
-- 置空合法：存量行与"暂未分类"的记录不强行归类。
ALTER TABLE audit.evidence_records
    DROP CONSTRAINT IF EXISTS ck_evidence_decision_type;
ALTER TABLE audit.evidence_records
    ADD CONSTRAINT ck_evidence_decision_type
    CHECK (decision_type IS NULL OR decision_type IN
        ('QUERY', 'UPDATE', 'INFER', 'ACTION', 'TOOL'));

-- 任务流时间轴查询的支撑索引：按租户 + 任务取时间序。
CREATE INDEX IF NOT EXISTS idx_evidence_task_flow
    ON audit.evidence_records(tenant_id, task_id, created_at)
    WHERE task_id IS NOT NULL;
