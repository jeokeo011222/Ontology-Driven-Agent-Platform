-- ODAP 平台迁移 000010：planning / evaluation / llm / simulation / release_mgmt schema
-- 依据：第十四篇 §15（计划三表）、第十五篇 §15（评估）、第九篇 §16（LLM 账本）

CREATE SCHEMA IF NOT EXISTS planning;

CREATE TABLE planning.plans (
    plan_id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    intent_id UUID NOT NULL,
    domain TEXT NOT NULL,
    status TEXT NOT NULL,
    created_by TEXT NOT NULL,
    version INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_plans_tenant_status_time
    ON planning.plans (tenant_id, status, created_at DESC);

-- 计划修订版不可变：实质变化创建新修订版，不覆盖（第十四篇 §15.1）
CREATE TABLE planning.plan_revisions (
    plan_revision_id UUID PRIMARY KEY,
    plan_id UUID NOT NULL REFERENCES planning.plans(plan_id),
    revision_number INTEGER NOT NULL,
    plan_ir JSONB NOT NULL,
    plan_digest TEXT NOT NULL,
    ontology_digest TEXT NOT NULL,
    action_contract_digest TEXT NOT NULL,
    tool_binding_digest TEXT NOT NULL,
    policy_version TEXT NOT NULL,
    validation_status TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (plan_id, revision_number)
);

CREATE TABLE planning.validation_runs (
    validation_run_id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    plan_revision_id UUID NOT NULL
        REFERENCES planning.plan_revisions(plan_revision_id),
    status TEXT NOT NULL,
    validator_version TEXT NOT NULL,
    findings JSONB NOT NULL DEFAULT '[]',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE SCHEMA IF NOT EXISTS evaluation;

-- datasets.tenant_id 可空：NULL=平台级数据集（所有租户可读，平台管理员维护）
CREATE TABLE evaluation.datasets (
    dataset_id UUID PRIMARY KEY,
    tenant_id UUID,
    name TEXT NOT NULL,
    domain TEXT NOT NULL,
    visibility TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE evaluation.dataset_revisions (
    dataset_revision_id UUID PRIMARY KEY,
    dataset_id UUID NOT NULL
        REFERENCES evaluation.datasets(dataset_id),
    revision_number INTEGER NOT NULL,
    manifest JSONB NOT NULL,
    content_digest TEXT NOT NULL,
    status TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (dataset_id, revision_number)
);

CREATE TABLE evaluation.cases (
    case_id UUID PRIMARY KEY,
    dataset_revision_id UUID NOT NULL
        REFERENCES evaluation.dataset_revisions(dataset_revision_id),
    case_key TEXT NOT NULL,
    scenario_type TEXT NOT NULL,
    input_ref TEXT NOT NULL,
    expected_ref TEXT NOT NULL,
    tags TEXT[] NOT NULL DEFAULT '{}',
    severity TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (dataset_revision_id, case_key)
);

CREATE TABLE evaluation.runs (
    evaluation_run_id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    subject_type TEXT NOT NULL,
    subject_id TEXT NOT NULL,
    subject_revision TEXT NOT NULL,
    dataset_revision_id UUID NOT NULL
        REFERENCES evaluation.dataset_revisions(dataset_revision_id),
    suite_revision TEXT NOT NULL,
    environment_digest TEXT NOT NULL,
    status TEXT NOT NULL,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_evaluation_runs_subject
    ON evaluation.runs (
        tenant_id,
        subject_type,
        subject_id,
        created_at DESC
    );

-- 案例排除：CHECK 强制双人审批（第十五篇 §5.2.2 防作弊）
CREATE TABLE evaluation.case_exclusions (
    exclusion_id UUID PRIMARY KEY,
    evaluation_run_id UUID NOT NULL
        REFERENCES evaluation.runs(evaluation_run_id),
    case_id UUID NOT NULL
        REFERENCES evaluation.cases(case_id),
    reason TEXT NOT NULL,
    requested_by TEXT NOT NULL,
    approved_by TEXT NOT NULL,
    evidence_ref TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CHECK (requested_by <> approved_by)
);

CREATE TABLE evaluation.case_runs (
    case_run_id UUID PRIMARY KEY,
    evaluation_run_id UUID NOT NULL
        REFERENCES evaluation.runs(evaluation_run_id),
    case_id UUID NOT NULL
        REFERENCES evaluation.cases(case_id),
    status TEXT NOT NULL,
    assertion_summary JSONB NOT NULL DEFAULT '{}',
    metrics JSONB NOT NULL DEFAULT '{}',
    evidence_bundle_id UUID,
    error_code TEXT,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ
);

CREATE INDEX idx_case_runs_evaluation_status
    ON evaluation.case_runs (
        evaluation_run_id,
        status
    );

-- 指标注册表：metric_key 唯一真源，防"同名不同义"（第十五篇 §15.7）
CREATE TABLE evaluation.metric_registry (
    metric_key TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    definition TEXT NOT NULL,
    unit TEXT NOT NULL,
    dimension TEXT NOT NULL,
    numerator TEXT NOT NULL,
    denominator TEXT NOT NULL,
    owner TEXT NOT NULL,
    registry_version INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE evaluation.metric_results (
    metric_result_id UUID PRIMARY KEY,
    evaluation_run_id UUID NOT NULL
        REFERENCES evaluation.runs(evaluation_run_id),
    metric_key TEXT NOT NULL
        REFERENCES evaluation.metric_registry(metric_key),
    metric_value NUMERIC,
    sample_count BIGINT,
    metric_metadata JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE SCHEMA IF NOT EXISTS llm;

CREATE TABLE llm.providers (
    provider_id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    provider_type TEXT NOT NULL,
    status TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE llm.endpoints (
    endpoint_id UUID PRIMARY KEY,
    provider_id TEXT NOT NULL
        REFERENCES llm.providers(provider_id),
    endpoint_name TEXT NOT NULL,
    deployment_type TEXT NOT NULL,
    region_code TEXT,
    credential_ref TEXT,
    status TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE llm.model_revisions (
    model_revision_id UUID PRIMARY KEY,
    endpoint_id UUID NOT NULL
        REFERENCES llm.endpoints(endpoint_id),
    model_id TEXT NOT NULL,
    revision_label TEXT,
    capability_digest TEXT NOT NULL,
    pricing_profile_id TEXT,
    status TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 用量账本：UNIQUE(tenant_id, request_id) 保证重试不重复结算（第九篇 §16.2）
CREATE TABLE llm.usage_ledger (
    usage_id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    run_id UUID NOT NULL,
    step_id UUID,
    request_id TEXT NOT NULL,
    endpoint_id UUID NOT NULL,
    model_revision_id UUID,
    input_tokens BIGINT,
    output_tokens BIGINT,
    usage_source TEXT NOT NULL,
    estimated_cost NUMERIC(20, 8),
    currency CHAR(3),
    settlement_status TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, request_id)
);

CREATE INDEX idx_llm_usage_tenant_time
    ON llm.usage_ledger (tenant_id, created_at);

CREATE INDEX idx_llm_usage_run
    ON llm.usage_ledger (tenant_id, run_id);

CREATE SCHEMA IF NOT EXISTS simulation;

CREATE TABLE simulation.scenarios (
    scenario_id UUID PRIMARY KEY,
    tenant_id UUID,
    name TEXT NOT NULL,
    scenario_type TEXT NOT NULL,
    scenario_revision INTEGER NOT NULL,
    manifest JSONB NOT NULL,
    content_digest TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (name, scenario_revision)
);

CREATE TABLE simulation.fault_events (
    fault_event_id UUID PRIMARY KEY,
    simulation_run_id UUID NOT NULL,
    fault_type TEXT NOT NULL,
    target_component TEXT NOT NULL,
    trigger_condition JSONB NOT NULL,
    observed_effect JSONB,
    status TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE SCHEMA IF NOT EXISTS release_mgmt;

-- 质量门禁决策：不能被普通开发账号覆盖（第十五篇 §15.6）
CREATE TABLE release_mgmt.quality_gate_decisions (
    decision_id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    evaluation_run_id UUID NOT NULL
        REFERENCES evaluation.runs(evaluation_run_id),
    policy_revision TEXT NOT NULL,
    decision TEXT NOT NULL,
    findings JSONB NOT NULL DEFAULT '[]',
    reviewer_id TEXT,
    decided_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
