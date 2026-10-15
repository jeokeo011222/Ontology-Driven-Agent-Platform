-- ODAP 平台迁移 000005：runtime schema（执行域核心表）
-- 依据：第二篇 §6（含 P0-4 authorization_decisions、P0-5 17 态+租约、agent_run_dependencies）
-- 权威：docs/contracts/01-全局状态注册表.md（Run 17 态、Step 12 态、ToolCall 12 态）

CREATE SCHEMA IF NOT EXISTS runtime;

CREATE TABLE runtime.agent_runs (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    requested_by UUID NOT NULL REFERENCES iam.users(id),
    agent_version_id UUID NOT NULL,
    status TEXT NOT NULL DEFAULT 'CREATED'
        CHECK (status IN (
            'CREATED', 'CONTEXT_BUILDING', 'READY',
            'PLANNING', 'PLAN_VALIDATING', 'RUNNING',
            'WAITING_INPUT', 'WAITING_APPROVAL', 'PAUSED',
            'RECOVERING', 'CANCELLING',
            'REQUIRES_RECONCILIATION', 'MANUAL_REVIEW',
            'COMPLETED', 'FAILED', 'CANCELLED', 'EXPIRED'
        )),
    input_payload JSONB NOT NULL,
    input_digest TEXT NOT NULL,
    output_payload JSONB,
    output_digest TEXT,
    context_manifest JSONB NOT NULL DEFAULT '{}'::jsonb,
    correlation_id UUID NOT NULL,
    attempt_count INTEGER NOT NULL DEFAULT 0
        CHECK (attempt_count >= 0),
    max_attempts INTEGER NOT NULL DEFAULT 3
        CHECK (max_attempts >= 1),
    priority INTEGER NOT NULL DEFAULT 100
        CHECK (priority >= 0),
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deadline_at TIMESTAMPTZ,
    cancel_requested_at TIMESTAMPTZ,
    cancel_convergence_deadline TIMESTAMPTZ,
    last_reconciliation_at TIMESTAMPTZ,
    lease_owner TEXT,
    lease_expires_at TIMESTAMPTZ,
    lease_epoch BIGINT NOT NULL DEFAULT 0
        CHECK (lease_epoch >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    error_code TEXT,
    error_summary TEXT,

    UNIQUE (tenant_id, id),
    FOREIGN KEY (tenant_id, agent_version_id)
        REFERENCES registry.agent_versions(tenant_id, id)
);

-- 四队列分治（第六篇 §6.2）：执行/恢复/对账/取消收敛互不饥饿
CREATE INDEX idx_agent_runs_queue
    ON runtime.agent_runs(status, priority DESC, available_at ASC)
    WHERE status IN ('CREATED', 'READY', 'RECOVERING');

CREATE INDEX idx_agent_runs_cancel_queue
    ON runtime.agent_runs(available_at ASC)
    WHERE status = 'CANCELLING';

CREATE INDEX idx_agent_runs_reconcile_queue
    ON runtime.agent_runs(last_reconciliation_at ASC NULLS FIRST)
    WHERE status = 'REQUIRES_RECONCILIATION';

CREATE INDEX idx_agent_runs_deadline
    ON runtime.agent_runs(deadline_at ASC)
    WHERE finished_at IS NULL;

CREATE INDEX idx_agent_runs_actor
    ON runtime.agent_runs(tenant_id, requested_by, created_at DESC);

CREATE INDEX idx_agent_runs_status
    ON runtime.agent_runs(tenant_id, status, created_at DESC);

-- RunVersionLock 数据库落点（ADR-002）：权威依赖以本表为准，context_manifest 仅为可读摘要
CREATE TABLE runtime.agent_run_dependencies (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    run_id UUID NOT NULL,
    dep_type TEXT NOT NULL
        CHECK (dep_type IN (
            'AGENT_VERSION', 'ONTOLOGY_VERSION', 'TOOL_VERSION',
            'POLICY_VERSION', 'MODEL_CONFIG', 'ERP_CONNECTION',
            'ERP_MAPPING_VERSION', 'PLAN_TEMPLATE'
        )),
    dep_id UUID NOT NULL,
    dep_version TEXT NOT NULL,
    dep_digest TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, run_id, dep_type, dep_id),
    FOREIGN KEY (tenant_id, run_id)
        REFERENCES runtime.agent_runs(tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX idx_run_deps_run
    ON runtime.agent_run_dependencies(tenant_id, run_id);

CREATE TABLE runtime.run_steps (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    run_id UUID NOT NULL,
    step_no INTEGER NOT NULL CHECK (step_no >= 0),
    step_type TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'PENDING'
        CHECK (status IN (
            'PENDING', 'READY', 'RUNNING', 'RETRY_WAIT',
            'WAITING_INPUT', 'WAITING_APPROVAL',
            'SUCCEEDED', 'FAILED', 'SKIPPED', 'CANCELLED',
            'UNKNOWN', 'RECONCILING'
        )),
    input_digest TEXT,
    output_digest TEXT,
    result_payload JSONB,
    evidence_refs JSONB NOT NULL DEFAULT '[]'::jsonb,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    error_code TEXT,
    error_summary TEXT,

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, run_id, step_no),
    FOREIGN KEY (tenant_id, run_id)
        REFERENCES runtime.agent_runs(tenant_id, id)
);

CREATE INDEX idx_run_steps_run
    ON runtime.run_steps(tenant_id, run_id, step_no);

-- 授权决策持久化（P0-4 修复：tool_calls.authorization_decision_id 的引用目标）
CREATE TABLE runtime.authorization_decisions (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    decision_id TEXT NOT NULL,
    subject_type TEXT NOT NULL
        CHECK (subject_type IN ('USER', 'AGENT', 'SERVICE', 'SYSTEM')),
    subject_id UUID NOT NULL,
    on_behalf_of UUID,
    delegation_chain JSONB NOT NULL DEFAULT '[]'::jsonb,
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT,
    environment TEXT NOT NULL
        CHECK (environment IN ('DEVELOPMENT', 'STAGING', 'PRODUCTION')),
    request_snapshot JSONB NOT NULL,
    effect TEXT NOT NULL
        CHECK (effect IN ('ALLOW', 'DENY', 'REQUIRE_APPROVAL')),
    reason_code TEXT NOT NULL,
    obligations JSONB NOT NULL DEFAULT '[]'::jsonb,
    policy_versions JSONB NOT NULL DEFAULT '[]'::jsonb,
    risk_level TEXT,
    facts_digest TEXT NOT NULL,
    evaluated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    latency_ms INTEGER,

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, decision_id)
);

CREATE INDEX idx_authz_decisions_subject
    ON runtime.authorization_decisions(tenant_id, subject_id, evaluated_at DESC);

CREATE INDEX idx_authz_decisions_resource
    ON runtime.authorization_decisions(tenant_id, resource_type, resource_id, evaluated_at DESC);

CREATE TABLE runtime.tool_calls (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    run_id UUID NOT NULL,
    step_id UUID,
    tool_version_id UUID NOT NULL,
    call_no INTEGER NOT NULL CHECK (call_no >= 0),
    status TEXT NOT NULL DEFAULT 'PENDING'
        CHECK (status IN (
            'PENDING', 'VALIDATING', 'REJECTED',
            'WAITING_APPROVAL', 'READY', 'DISPATCHED',
            'EXECUTING', 'SUCCEEDED', 'FAILED',
            'UNKNOWN', 'RECONCILING', 'MANUAL_REVIEW'
        )),
    idempotency_key TEXT NOT NULL,
    input_digest TEXT NOT NULL,
    input_payload JSONB,
    output_digest TEXT,
    output_payload JSONB,
    authorization_decision_id UUID,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    error_code TEXT,
    base_error_code TEXT,

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, run_id, call_no),
    UNIQUE (tenant_id, idempotency_key),
    FOREIGN KEY (tenant_id, run_id)
        REFERENCES runtime.agent_runs(tenant_id, id),
    FOREIGN KEY (tenant_id, step_id)
        REFERENCES runtime.run_steps(tenant_id, id),
    FOREIGN KEY (tenant_id, tool_version_id)
        REFERENCES registry.tool_versions(tenant_id, id),
    FOREIGN KEY (tenant_id, authorization_decision_id)
        REFERENCES runtime.authorization_decisions(tenant_id, id)
);

CREATE INDEX idx_tool_calls_run
    ON runtime.tool_calls(tenant_id, run_id, call_no);

CREATE INDEX idx_tool_calls_recovery
    ON runtime.tool_calls(status, started_at)
    WHERE status IN (
        'DISPATCHED', 'EXECUTING', 'UNKNOWN', 'RECONCILING'
    );

CREATE TABLE runtime.checkpoints (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    run_id UUID NOT NULL,
    checkpoint_no BIGINT NOT NULL CHECK (checkpoint_no >= 0),
    resume_step_no INTEGER NOT NULL CHECK (resume_step_no >= 0),
    state_payload JSONB NOT NULL,
    state_digest TEXT NOT NULL,
    dependency_digest TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, run_id, checkpoint_no),
    FOREIGN KEY (tenant_id, run_id)
        REFERENCES runtime.agent_runs(tenant_id, id)
);

CREATE INDEX idx_checkpoints_latest
    ON runtime.checkpoints(tenant_id, run_id, checkpoint_no DESC);
