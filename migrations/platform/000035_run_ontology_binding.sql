-- ODAP 平台迁移 000035：Run 与本体绑定（业务工作台本体化）
--
-- 背景：业务工作台此前以 Agent 为中心——AI 业务助手提交的 POST /runs 要求
-- agent_version_id（UUID，NOT NULL），既没有「选中本体」的位置，也无法记录
-- 「基于本体规则审查业务请求」的结论与依据，导致任务详情无从展示本体处理过程。
--
-- 本迁移做三件事，且都只增不减（不改既有语义）：
--   1. 让 agent_version_id 可空：本体驱动 Run 不涉及任何 Agent（审查规则来自本体，
--      不是来自某个 Agent 的提示词）。外键对 NULL 天然放行，无需改动约束。
--   2. 新增 objective / business_type：保留用户的自然语言业务目标，它是本次审查的
--      输入，必须原样可追溯；不塞进 input_payload（后者语义是「执行入参」，混入
--      自然语言会让下游契约与摘要失真）。
--   3. 新增 ontology_version_id / plan / review_conclusion：记录本次审查所依据的
--      本体版本、由本体派生的结构化执行计划、以及最终结论。三者都以「版本锁」
--      方式对齐（依赖明细另见 runtime.agent_run_dependencies 的 ONTOLOGY_VERSION 行）。
--
-- 诚实性约束（fail-closed）：
--   - review_conclusion 由服务端依据真实执行的 SHACL 结论**确定性合成**，
--     模型给出的判断不得覆盖 SHACL 的「不合规」结论（见 handlers/ontology_review.go）；
--   - 任何一列都允许为 NULL，但 NULL 只表示「本次流程未走到/不适用」，
--     绝不表示「查过且通过」——不以默认值把缺失伪装成正常。

ALTER TABLE runtime.agent_runs
    ALTER COLUMN agent_version_id DROP NOT NULL;

ALTER TABLE runtime.agent_runs
    ADD COLUMN IF NOT EXISTS objective TEXT,
    ADD COLUMN IF NOT EXISTS business_type TEXT,
    ADD COLUMN IF NOT EXISTS ontology_version_id UUID,
    ADD COLUMN IF NOT EXISTS plan JSONB,
    ADD COLUMN IF NOT EXISTS review_conclusion JSONB;

COMMENT ON COLUMN runtime.agent_runs.agent_version_id IS
    '执行该 Run 的 Agent 版本；本体驱动的审查 Run 为空（审查规则来自本体而非 Agent）';
COMMENT ON COLUMN runtime.agent_runs.objective IS
    '用户以自然语言提交的业务目标原文（本体审查的输入），创建后不可变用于追溯';
COMMENT ON COLUMN runtime.agent_runs.business_type IS
    '可选的业务类型标注（用户输入，仅作检索与统计维度，不参与规则判定）';
COMMENT ON COLUMN runtime.agent_runs.ontology_version_id IS
    '本次审查所依据的本体版本 id；为空表示该 Run 不由本体驱动';
COMMENT ON COLUMN runtime.agent_runs.plan IS
    '由本体规则派生的结构化执行计划（真实生成，非前端硬编码）';
COMMENT ON COLUMN runtime.agent_runs.review_conclusion IS
    '本体规则审查结论（服务端依据真实 SHACL 结果确定性合成，模型判断不得覆盖不合规结论）';

-- 按本体版本回查「哪些 Run 用了这个本体」是运维与审计的常规诉求
-- （本体下线前的影响面评估），没有索引只能全表扫描。
CREATE INDEX IF NOT EXISTS idx_agent_runs_ontology
    ON runtime.agent_runs(tenant_id, ontology_version_id)
    WHERE ontology_version_id IS NOT NULL;
