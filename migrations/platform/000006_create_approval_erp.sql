-- ODAP 平台迁移 000006：workflow 审批 + integration ERP 操作账本
-- 依据：第二篇 §7（Approval / ERPOperation / ERPReceipt 三者严格区分）

CREATE SCHEMA IF NOT EXISTS workflow;

CREATE TABLE workflow.approvals (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES iam.tenants(id),
    run_id UUID,
    requested_by UUID NOT NULL REFERENCES iam.users(id),
    approval_type TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'PENDING'
        CHECK (status IN (
            'PENDING', 'APPROVED', 'REJECTED',
            'CANCELLED', 'EXPIRED', 'INVALIDATED'
        )),
    subject TEXT NOT NULL,
    payload_snapshot JSONB NOT NULL,
    payload_digest TEXT NOT NULL,
    policy_version_refs JSONB NOT NULL DEFAULT '[]'::jsonb,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at TIMESTAMPTZ,

    UNIQUE (tenant_id, id),
    FOREIGN KEY (tenant_id, run_id)
        REFERENCES runtime.agent_runs(tenant_id, id)
);

CREATE INDEX idx_approvals_pending
    ON workflow.approvals(tenant_id, created_at)
    WHERE status = 'PENDING';

-- 审批动作追加式记录：不覆盖历史决定；禁止自我审批由服务层强制
CREATE TABLE workflow.approval_actions (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    approval_id UUID NOT NULL,
    actor_id UUID NOT NULL REFERENCES iam.users(id),
    decision TEXT NOT NULL
        CHECK (decision IN ('APPROVE', 'REJECT')),
    comment TEXT NOT NULL DEFAULT '',
    payload_digest TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    FOREIGN KEY (tenant_id, approval_id)
        REFERENCES workflow.approvals(tenant_id, id)
);

CREATE SCHEMA IF NOT EXISTS integration;

CREATE TABLE integration.erp_operations (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES iam.tenants(id),
    run_id UUID,
    tool_call_id UUID,
    adapter_key TEXT NOT NULL,
    external_system TEXT NOT NULL,
    object_type TEXT NOT NULL,
    object_id TEXT NOT NULL,
    action TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    expected_version TEXT,
    payload_snapshot JSONB NOT NULL,
    payload_digest TEXT NOT NULL,
    approval_id UUID,
    status TEXT NOT NULL DEFAULT 'PENDING'
        CHECK (status IN (
            'PENDING', 'VALIDATING', 'REJECTED',
            'WAITING_APPROVAL', 'READY', 'DISPATCHED',
            'EXECUTING', 'SUCCEEDED', 'FAILED',
            'UNKNOWN', 'RECONCILING', 'MANUAL_REVIEW'
        )),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    dispatched_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, external_system, idempotency_key),
    FOREIGN KEY (tenant_id, run_id)
        REFERENCES runtime.agent_runs(tenant_id, id),
    FOREIGN KEY (tenant_id, tool_call_id)
        REFERENCES runtime.tool_calls(tenant_id, id),
    FOREIGN KEY (tenant_id, approval_id)
        REFERENCES workflow.approvals(tenant_id, id)
);

CREATE INDEX idx_erp_operations_reconcile
    ON integration.erp_operations(status, created_at)
    WHERE status IN (
        'DISPATCHED', 'EXECUTING', 'UNKNOWN', 'RECONCILING'
    );

-- 回执：一次操作可有多条回执（提交/查询/对账），不允许对 operation_id 设唯一约束
CREATE TABLE integration.erp_receipts (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    operation_id UUID NOT NULL,
    receipt_type TEXT NOT NULL,
    external_operation_id TEXT,
    external_object_version TEXT,
    result_status TEXT NOT NULL,
    response_digest TEXT,
    response_payload JSONB,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    FOREIGN KEY (tenant_id, operation_id)
        REFERENCES integration.erp_operations(tenant_id, id)
);

CREATE INDEX idx_erp_receipts_operation
    ON integration.erp_receipts(tenant_id, operation_id, received_at);
