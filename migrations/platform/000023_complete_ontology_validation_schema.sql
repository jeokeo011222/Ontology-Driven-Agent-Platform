-- ODAP 平台迁移 000023：补齐 ontology 校验记录的结构缺口
--
-- 依据：第五篇 §5.3「验证记录与问题明细」、§11.1「发布前置条件」
--       （"平台应通过 validation_profile 定义每种本体的必需检查"）。
--
-- 缺陷（由「设计书 DDL ↔ 迁移 000004 逐列核对」暴露）：
--   000004 建了 ontology.validation_runs，但相对设计书缺三样东西：
--     A. validation_profile —— 决定"这种本体必须过哪些检查"的配置键；
--        没有它就无法按 §11.1 区分"纯术语本体"与"带实例数据的本体"的
--        必需检查集合，发布门禁只能一刀切。
--     B. summary_json —— 校验结论的结构化摘要（设计书列为 NOT NULL）。
--     C. created_at  —— 记录创建时间（设计书列为 NOT NULL DEFAULT now()）。
--   并且整张 ontology.validation_findings（问题明细）表**完全不存在**，
--   导致校验只能给出 PASS/FAIL 而不能给出"哪条约束、哪个焦点节点、哪条路径"
--   ——这正是 SHACL 报告最有价值的部分，也是前端展示校验失败原因的唯一数据源。
--
-- 本迁移只做加法：不改任何既有列、不改任何既有行的语义。
--   - 新增列一律带默认值，对既有行安全回填；
--   - 新增表按仓内既有 RLS 约定（app.apply_tenant_rls）加租户隔离。
--
-- 可重复执行（迁移重放安全）。

-- ============================================================================
-- A/B/C. ontology.validation_runs 补列
-- ============================================================================
ALTER TABLE ontology.validation_runs
    ADD COLUMN IF NOT EXISTS validation_profile TEXT NOT NULL DEFAULT 'default',
    ADD COLUMN IF NOT EXISTS summary_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now();

COMMENT ON COLUMN ontology.validation_runs.validation_profile IS
    '校验配置键（第五篇 §11.1）：决定这类本体必须通过哪些检查，而非无条件执行固定规则集';
COMMENT ON COLUMN ontology.validation_runs.summary_json IS
    '校验结论结构化摘要（第五篇 §5.3）；明细见 ontology.validation_findings';

-- ============================================================================
-- D. ontology.validation_findings —— 校验问题明细（第五篇 §5.3）
-- ============================================================================
CREATE TABLE IF NOT EXISTS ontology.validation_findings (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    validation_run_id UUID NOT NULL,
    severity TEXT NOT NULL
        CHECK (severity IN ('INFO', 'WARNING', 'ERROR', 'CRITICAL')),
    finding_code TEXT NOT NULL,
    focus_node TEXT,
    path TEXT,
    source_reference JSONB NOT NULL DEFAULT '{}'::jsonb,
    message TEXT NOT NULL,
    details_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    FOREIGN KEY (tenant_id, validation_run_id)
        REFERENCES ontology.validation_runs(tenant_id, id)
);

-- 注：severity 取值采用仓内既有约定的大写枚举（与 validation_runs.status、
-- ontology_versions.status 等保持一致），语义等价于第五篇 §5.3 的小写枚举。

CREATE INDEX IF NOT EXISTS idx_ontology_findings_run_severity
    ON ontology.validation_findings (tenant_id, validation_run_id, severity);

-- ============================================================================
-- E. RLS：与仓内其余租户业务表一致（000011/000016 的 app.apply_tenant_rls）
-- ============================================================================
SELECT app.apply_tenant_rls('ontology', 'validation_findings');

COMMENT ON TABLE ontology.validation_findings IS
    'RLS: ontology.validation_findings_tenant_isolation（000023 补齐；tenant_id NOT NULL）。'
    '校验问题明细：SHACL 报告的 focus_node / path / severity 落点（第五篇 §5.3）。';
