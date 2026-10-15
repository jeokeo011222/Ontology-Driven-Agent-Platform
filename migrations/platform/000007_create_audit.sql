-- ODAP 平台迁移 000007：audit schema（审计事件哈希链 + 证据记录）
-- 依据：第二篇 §8、第十一篇 §9（四层完整性：事件摘要→段链→检查点→签名归档）
-- 防篡改：应用角色只授 INSERT+SELECT；UPDATE/DELETE 由触发器拒绝

CREATE SCHEMA IF NOT EXISTS audit;

CREATE TABLE audit.audit_events (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES iam.tenants(id),
    event_type TEXT NOT NULL,
    actor_type TEXT NOT NULL,
    actor_id TEXT,
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    action TEXT NOT NULL,
    outcome TEXT NOT NULL,
    request_id TEXT,
    run_id UUID,
    policy_refs JSONB NOT NULL DEFAULT '[]'::jsonb,
    event_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    event_digest TEXT NOT NULL,
    segment_id TEXT NOT NULL,
    seq_no BIGINT NOT NULL,
    prev_digest TEXT,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, segment_id, seq_no)
);

CREATE INDEX idx_audit_events_tenant_time
    ON audit.audit_events(tenant_id, occurred_at DESC);

CREATE INDEX idx_audit_events_resource
    ON audit.audit_events(
        tenant_id, resource_type, resource_id, occurred_at DESC
    );

CREATE INDEX idx_audit_events_run
    ON audit.audit_events(tenant_id, run_id, occurred_at);

-- 防篡改触发器：任何角色（含表 owner 在应用连接中）都不得 UPDATE/DELETE 审计事件
CREATE OR REPLACE FUNCTION audit.reject_audit_event_mutation()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION
        'audit: audit_events is append-only (attempted % on event %)',
        TG_OP, OLD.id;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_audit_events_append_only
    BEFORE UPDATE OR DELETE ON audit.audit_events
    FOR EACH ROW
    EXECUTE FUNCTION audit.reject_audit_event_mutation();

CREATE TABLE audit.evidence_records (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES iam.tenants(id),
    run_id UUID,
    step_id UUID,
    evidence_type TEXT NOT NULL,
    source_type TEXT NOT NULL,
    source_ref TEXT,
    content_digest TEXT NOT NULL,
    storage_uri TEXT,
    summary TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    FOREIGN KEY (tenant_id, run_id)
        REFERENCES runtime.agent_runs(tenant_id, id),
    FOREIGN KEY (tenant_id, step_id)
        REFERENCES runtime.run_steps(tenant_id, id)
);

CREATE INDEX idx_evidence_run
    ON audit.evidence_records(tenant_id, run_id, created_at);

-- 段序号计数器：每租户每段一行，行锁分配，禁止空洞回填（第二篇 §8.1 要点 1）
CREATE TABLE audit.segment_seq_counters (
    tenant_id UUID NOT NULL,
    segment_id TEXT NOT NULL,
    next_seq BIGINT NOT NULL DEFAULT 1,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, segment_id)
);
