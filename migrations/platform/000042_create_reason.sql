-- ODAP 平台迁移 000042：推理引擎（模块 B「Go 自研规则推理 + gRPC 契约」）
-- 依据：实施计划「四大模块」之模块 B。
-- 要点：
--   * 内置规则（OWL 2 RL / RDFS 子集）内置于代码（internal/reasoning/rules_builtin.go），
--     本表只存用户自定义规则 + 代码内置规则的启停开关行（is_builtin=true 不可改删）
--   * 推理运行记录：派生三元组经 GraphStore 写回图存储（fact_kind='derived'），
--     本表承载"哪次运行、对哪个图、用了哪些规则、派生了多少"的审计轨迹

CREATE SCHEMA IF NOT EXISTS reason;
-- USAGE 与 CREATE 缺一不可（见 000041 注释）。
GRANT USAGE, CREATE ON SCHEMA reason TO odap_app;

CREATE TABLE reason.rules (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    rule_key TEXT NOT NULL,
    name TEXT NOT NULL,
    -- OWL2RL/RDFS 内置规则集的启停行也登记于此（body 为空，body 由代码承载）
    kind TEXT NOT NULL CHECK (kind IN ('OWL2RL', 'RDFS', 'CUSTOM')),
    -- 自定义规则体：premises/conclusion 三元组模式（subject/predicate/object，
    -- 空串表示变量）。DisallowUnknownFields 校验在服务层
    body JSONB NOT NULL DEFAULT '{}'::jsonb,
    description TEXT NOT NULL DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    is_builtin BOOLEAN NOT NULL DEFAULT FALSE,
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, rule_key)
);

CREATE TABLE reason.inference_runs (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    graph_iri TEXT NOT NULL,
    mode TEXT NOT NULL CHECK (mode IN ('FORWARD', 'BACKWARD', 'INCREMENTAL')),
    rule_keys TEXT[] NOT NULL DEFAULT '{}',
    input_triple_count BIGINT NOT NULL DEFAULT 0,
    derived_triple_count BIGINT NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'RUNNING'
        CHECK (status IN ('RUNNING', 'SUCCEEDED', 'FAILED')),
    error TEXT NOT NULL DEFAULT '',
    duration_ms BIGINT NOT NULL DEFAULT 0,
    triggered_by UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_inference_runs_tenant ON reason.inference_runs (tenant_id, created_at DESC);

SELECT app.apply_tenant_rls('reason', 'rules');
SELECT app.apply_tenant_rls('reason', 'inference_runs');

GRANT SELECT, INSERT, UPDATE, DELETE ON reason.rules, reason.inference_runs TO odap_app;
