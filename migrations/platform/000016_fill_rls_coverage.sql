-- ODAP 平台迁移 000016：补齐 RLS 逐表审查的覆盖缺口
--
-- 依据：第二篇 §9.2「所有需要隔离的业务表都要逐表审查，不能只保护 agent_runs」。
--
-- 缺陷（由全新库迁移回放 + information_schema 逐表核对暴露）：
-- 迁移 000011 的 RLS 覆盖清单只写了
--   「iam / governance / registry / ontology / runtime / workflow / integration / audit / evidence」，
-- 但 000009（graph）、000010（planning / evaluation / simulation / release_mgmt）建的表
-- 中有若干**明确带 tenant_id NOT NULL 的租户业务表**被整段漏掉，且 000011 中没有任何
-- 「刻意排除」的说明。这些表对应用账号 odap_app 完全可读写，构成纵向越权风险，
-- 与 §9.2「逐表审查」的要求不符。
--
-- 本迁移只做「补覆盖」，不改变任何既有列与既有策略：
--   A. 标准租户策略（tenant_id = app.current_tenant_id()），用于 tenant_id NOT NULL 的租户表：
--        planning.plans / planning.validation_runs / graph.named_graphs
--        release_mgmt.quality_gate_decisions
--   B. 平台级容忍策略（读允许 NULL=平台级，写必须归属当前租户），用于 tenant_id 可空的表：
--        evaluation.datasets / simulation.scenarios
--   C. 其余无 tenant_id 的子表：**逐表明示结论**（见文末 §C），保持依赖父表访问路径，
--      不静默留白。
--
-- 可重复执行（迁移重放安全）：先 DROP POLICY IF EXISTS 再 CREATE POLICY。
-- 超级用户（odap）不受 RLS 约束，故迁移与种子数据不受影响；
-- 应用账号 odap_app 具备 NOSUPERUSER + NOBYPASSRLS，策略对其生效。

-- ============================================================================
-- A. 标准租户策略：复用 000011 定义的 app.apply_tenant_rls(schema, table)
-- ============================================================================
SELECT app.apply_tenant_rls('planning', 'plans');
SELECT app.apply_tenant_rls('planning', 'validation_runs');
SELECT app.apply_tenant_rls('graph', 'named_graphs');
SELECT app.apply_tenant_rls('release_mgmt', 'quality_gate_decisions');

COMMENT ON TABLE planning.plans IS
    'RLS: planning.plans_tenant_isolation（000016 补齐；tenant_id NOT NULL）';
COMMENT ON TABLE planning.validation_runs IS
    'RLS: planning.validation_runs_tenant_isolation（000016 补齐；tenant_id NOT NULL）';
COMMENT ON TABLE graph.named_graphs IS
    'RLS: graph.named_graphs_tenant_isolation（000016 补齐）。注：graph.triples 按 tenant_id LIST 分区做结构性隔离，named_graphs 是租户图谱目录，需 RLS。';
COMMENT ON TABLE release_mgmt.quality_gate_decisions IS
    'RLS: release_mgmt.quality_gate_decisions_tenant_isolation（000016 补齐；tenant_id NOT NULL）';

-- ============================================================================
-- B. 平台级容忍策略：tenant_id 可空，NULL 表示平台级共享数据
--    （见 000010 注释：datasets.tenant_id 可空，NULL=平台级数据集，所有租户可读）
--    USING     ：读允许「本租户」或「平台级(NULL)」
--    WITH CHECK：写只允许归属当前租户，禁止租户创建/改写平台级共享行
-- ============================================================================
CREATE OR REPLACE FUNCTION app.apply_tenant_rls_nullable(schema_name TEXT, table_name TEXT)
RETURNS VOID AS $$
BEGIN
    EXECUTE format(
        'ALTER TABLE %I.%I ENABLE ROW LEVEL SECURITY', schema_name, table_name
    );
    EXECUTE format(
        'ALTER TABLE %I.%I FORCE ROW LEVEL SECURITY', schema_name, table_name
    );
    EXECUTE format(
        'DROP POLICY IF EXISTS %I ON %I.%I',
        table_name || '_tenant_isolation', schema_name, table_name
    );
    EXECUTE format(
        'CREATE POLICY %I ON %I.%I
            USING (tenant_id IS NULL OR tenant_id = app.current_tenant_id())
            WITH CHECK (tenant_id = app.current_tenant_id())',
        table_name || '_tenant_isolation', schema_name, table_name
    );
END;
$$ LANGUAGE plpgsql;

SELECT app.apply_tenant_rls_nullable('evaluation', 'datasets');
SELECT app.apply_tenant_rls_nullable('simulation', 'scenarios');

COMMENT ON TABLE evaluation.datasets IS
    'RLS: evaluation.datasets_tenant_isolation（000016 补齐，平台级容忍：NULL=平台级可读、写入必须归属当前租户）';
COMMENT ON TABLE simulation.scenarios IS
    'RLS: simulation.scenarios_tenant_isolation（000016 补齐，平台级容忍：NULL=平台级可读、写入必须归属当前租户）';

-- ============================================================================
-- C. 无 tenant_id 的表：逐表明示隔离结论（不静默留白，符合 §9.2「逐表审查」）
-- ============================================================================
COMMENT ON TABLE planning.plan_revisions IS
    '隔离结论（000016 逐表审查）：无 tenant_id，经 plan_id 归属 planning.plans；'
    '不得脱离父表单独查询/导出，访问路径统一走 plans 过滤。此处不单独建策略，'
    '因 pgx 侧统一以 plans 为主表关联读取。';
COMMENT ON TABLE evaluation.dataset_revisions IS
    '隔离结论（000016）：无 tenant_id，经 dataset_id 归属 evaluation.datasets；'
    '平台级数据集的修订对所有租户可见，故不建独立策略，统一经 datasets 访问。';
COMMENT ON TABLE evaluation.cases IS
    '隔离结论（000016）：无 tenant_id，经 dataset_id 归属 evaluation.datasets；统一经 datasets 访问。';
COMMENT ON TABLE evaluation.metric_results IS
    '隔离结论（000016）：无 tenant_id，经 run_id 归属 evaluation.runs（runs 已有 RLS）；统一经 runs 访问。';
COMMENT ON TABLE simulation.fault_events IS
    '隔离结论（000016）：无 tenant_id，经 scenario_id 归属 simulation.scenarios；统一经 scenarios 访问。';
COMMENT ON TABLE governance.role_permissions IS
    '隔离结论（000016）：角色-权限映射表，父表 governance.roles 已有 RLS；映射行随角色可见性收敛。';
COMMENT ON TABLE registry.plugin_artifacts IS
    '隔离结论（000016）：插件制品为平台级资产（租户启用状态在 registry.plugin_installations，已有 RLS）。';
COMMENT ON TABLE registry.plugin_signatures IS
    '隔离结论（000016）：插件签名为平台级资产，同 plugin_artifacts。';
COMMENT ON TABLE registry.plugin_publications IS
    '隔离结论（000016）：插件发布记录为平台级资产，同 plugin_artifacts。';
COMMENT ON TABLE registry.plugin_capability_declarations IS
    '隔离结论（000016）：插件能力声明为平台级资产，同 plugin_artifacts。';
COMMENT ON TABLE audit.segment_seq_counters IS
    '隔离结论（000016）：审计哈希链的段序号分配器（行锁分配，必须全局单调），非租户业务数据，'
    'tenant_id 仅用于分段命名；不建 RLS 以免破坏全局序号分配。';
COMMENT ON TABLE execution.aggregate_seq_counters IS
    '隔离结论（000016）：Outbox 每聚合序号分配器（行锁分配），非租户业务数据。';

-- 分区表：父表 execution.consumer_inbox / execution.consumer_inbox_dedup 已有 RLS（000014），
-- 应用一律经父表访问，PostgreSQL 会套用父表策略；分区不单独建策略（裸建会造成默认拒绝）。
COMMENT ON TABLE execution.consumer_inbox IS
    'RLS 由父表策略提供（000014）；分区仅供物理存储，应用不得按分区名直接访问。';
