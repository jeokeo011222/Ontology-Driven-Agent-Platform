-- ODAP 平台迁移 000011：RLS 行级安全策略 + 审计哈希链段序号保障
-- 依据：第二篇 §9（RLS 是纵深防御，不替代应用层授权）
-- 前提：应用数据库角色不是超级用户、无 BYPASSRLS；业务查询仍显式带 tenant_id。
-- app.tenant_id 由 pgxutil.WithTenantTx 以 SET LOCAL（事务级）注入，连接池复用无泄漏。

CREATE SCHEMA IF NOT EXISTS app;

CREATE OR REPLACE FUNCTION app.current_tenant_id()
RETURNS UUID AS $$
    SELECT NULLIF(current_setting('app.tenant_id', true), '')::uuid
$$ LANGUAGE sql STABLE;

-- 通用租户隔离策略（USING + WITH CHECK 同式，防读越权也防写越权）
-- 可重复执行：先 DROP 再 CREATE（迁移重放安全）
CREATE OR REPLACE FUNCTION app.apply_tenant_rls(schema_name TEXT, table_name TEXT)
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
            USING (tenant_id = app.current_tenant_id())
            WITH CHECK (tenant_id = app.current_tenant_id())',
        table_name || '_tenant_isolation', schema_name, table_name
    );
END;
$$ LANGUAGE plpgsql;

-- iam / governance / registry / ontology / runtime / workflow / integration / audit / evidence
SELECT app.apply_tenant_rls('iam', 'organizations');
SELECT app.apply_tenant_rls('iam', 'memberships');
SELECT app.apply_tenant_rls('iam', 'sessions');
SELECT app.apply_tenant_rls('iam', 'org_closure');
SELECT app.apply_tenant_rls('governance', 'roles');
SELECT app.apply_tenant_rls('governance', 'role_bindings');
SELECT app.apply_tenant_rls('governance', 'policy_versions');
SELECT app.apply_tenant_rls('registry', 'plugin_installations');
SELECT app.apply_tenant_rls('registry', 'agents');
SELECT app.apply_tenant_rls('registry', 'agent_versions');
SELECT app.apply_tenant_rls('registry', 'tools');
SELECT app.apply_tenant_rls('registry', 'tool_versions');
SELECT app.apply_tenant_rls('ontology', 'ontologies');
SELECT app.apply_tenant_rls('ontology', 'ontology_versions');
SELECT app.apply_tenant_rls('ontology', 'ontology_artifacts');
SELECT app.apply_tenant_rls('ontology', 'ontology_dependencies');
SELECT app.apply_tenant_rls('ontology', 'validation_runs');
SELECT app.apply_tenant_rls('ontology', 'publications');
SELECT app.apply_tenant_rls('runtime', 'agent_runs');
SELECT app.apply_tenant_rls('runtime', 'agent_run_dependencies');
SELECT app.apply_tenant_rls('runtime', 'run_steps');
SELECT app.apply_tenant_rls('runtime', 'authorization_decisions');
SELECT app.apply_tenant_rls('runtime', 'tool_calls');
SELECT app.apply_tenant_rls('runtime', 'checkpoints');
SELECT app.apply_tenant_rls('workflow', 'approvals');
SELECT app.apply_tenant_rls('workflow', 'approval_actions');
SELECT app.apply_tenant_rls('integration', 'erp_operations');
SELECT app.apply_tenant_rls('integration', 'erp_receipts');
SELECT app.apply_tenant_rls('audit', 'audit_events');
SELECT app.apply_tenant_rls('audit', 'evidence_records');
SELECT app.apply_tenant_rls('execution', 'task_outbox');
SELECT app.apply_tenant_rls('execution', 'event_dead_letters');
SELECT app.apply_tenant_rls('evaluation', 'runs');
SELECT app.apply_tenant_rls('llm', 'usage_ledger');

-- 平台级可见表：plugins/plugin_versions 的 tenant_id 可为 NULL（平台级插件全局共享），
-- 策略放行 NULL 行，租户行仍严格隔离（ADR-002 平台级/租户级双轨）
ALTER TABLE registry.plugins ENABLE ROW LEVEL SECURITY;
ALTER TABLE registry.plugins FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS plugins_tenant_isolation ON registry.plugins;
CREATE POLICY plugins_tenant_isolation ON registry.plugins
    USING (tenant_id IS NULL OR tenant_id = app.current_tenant_id())
    WITH CHECK (tenant_id IS NULL OR tenant_id = app.current_tenant_id());

ALTER TABLE registry.plugin_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE registry.plugin_versions FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS plugin_versions_tenant_isolation ON registry.plugin_versions;
CREATE POLICY plugin_versions_tenant_isolation ON registry.plugin_versions
    USING (tenant_id IS NULL OR tenant_id = app.current_tenant_id())
    WITH CHECK (tenant_id IS NULL OR tenant_id = app.current_tenant_id());

-- 无 tenant_id 列的评估子表：经父表 evaluation.runs 的 EXISTS 策略隔离
-- （唯一约束/外键由父表承载，RLS 作为第二道防线）
ALTER TABLE evaluation.case_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE evaluation.case_runs FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS case_runs_tenant_isolation ON evaluation.case_runs;
CREATE POLICY case_runs_tenant_isolation ON evaluation.case_runs
    USING (
        EXISTS (
            SELECT 1 FROM evaluation.runs r
            WHERE r.evaluation_run_id = evaluation.case_runs.evaluation_run_id
              AND r.tenant_id = app.current_tenant_id()
        )
    );

ALTER TABLE evaluation.case_exclusions ENABLE ROW LEVEL SECURITY;
ALTER TABLE evaluation.case_exclusions FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS case_exclusions_tenant_isolation ON evaluation.case_exclusions;
CREATE POLICY case_exclusions_tenant_isolation ON evaluation.case_exclusions
    USING (
        EXISTS (
            SELECT 1 FROM evaluation.runs r
            WHERE r.evaluation_run_id = evaluation.case_exclusions.evaluation_run_id
              AND r.tenant_id = app.current_tenant_id()
        )
    );

-- 审计哈希链段序号分配函数：每租户每段一行锁分配，禁止空洞回填（第二篇 §8.1 要点 1）
CREATE OR REPLACE FUNCTION audit.next_segment_seq(p_tenant_id UUID, p_segment_id TEXT)
RETURNS BIGINT AS $$
DECLARE
    v_seq BIGINT;
BEGIN
    INSERT INTO audit.segment_seq_counters (tenant_id, segment_id, next_seq)
    VALUES (p_tenant_id, p_segment_id, 2)
    ON CONFLICT (tenant_id, segment_id)
    DO UPDATE SET next_seq = audit.segment_seq_counters.next_seq + 1,
                  updated_at = now()
    RETURNING next_seq - 1 INTO v_seq;
    RETURN v_seq;
END;
$$ LANGUAGE plpgsql;

-- ============================================================
-- 应用数据库角色（第二篇 §9.2 RLS 安全前提）
-- 应用角色必须：非超级用户、无 BYPASSRLS。owner/migrate 账号只用于迁移，
-- 运行时连接（api/worker）一律使用 odap_app。
-- ============================================================
DO $$
DECLARE s TEXT;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'odap_app') THEN
        CREATE ROLE odap_app LOGIN NOBYPASSRLS NOSUPERUSER;
    END IF;

    FOR s IN
        SELECT schema_name FROM information_schema.schemata
        WHERE schema_name NOT IN (
            'pg_catalog', 'information_schema', 'pg_toast',
            'pg_temp_1', 'pg_toast_temp_1'
        )
    LOOP
        EXECUTE format('GRANT USAGE ON SCHEMA %I TO odap_app', s);
        EXECUTE format(
            'GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %I TO odap_app', s
        );
        EXECUTE format(
            'GRANT USAGE ON ALL SEQUENCES IN SCHEMA %I TO odap_app', s
        );
        -- 后续迁移新建的表/序列自动授权
        EXECUTE format(
            'ALTER DEFAULT PRIVILEGES IN SCHEMA %I
                GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO odap_app', s
        );
        EXECUTE format(
            'ALTER DEFAULT PRIVILEGES IN SCHEMA %I
                GRANT USAGE ON SEQUENCES TO odap_app', s
        );
    END LOOP;

    -- 审计表权限收紧：只授 INSERT + SELECT（第二篇 §8.1 要点 4，
    -- 与 append-only 触发器构成双保险；修正/归档由受控运维流程完成）
    REVOKE UPDATE, DELETE ON audit.audit_events FROM odap_app;
    REVOKE UPDATE, DELETE ON audit.evidence_records FROM odap_app;
END $$;
