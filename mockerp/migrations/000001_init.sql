-- Mock ERP 迁移 000001：报销业务域独立 Schema
-- 依据：第十六篇 §2.3（首个业务域：员工报销审核与提交审批）
-- 权威边界：本库是 Mock ERP 业务事实的权威来源，与 ODAP 平台库物理分离（决策 B）

CREATE SCHEMA IF NOT EXISTS erp;

CREATE TABLE erp.employees (
    id UUID PRIMARY KEY,
    employee_no TEXT NOT NULL UNIQUE,
    full_name TEXT NOT NULL,
    department_code TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'ACTIVE'
        CHECK (status IN ('ACTIVE', 'SUSPENDED', 'TERMINATED')),
    cost_center_code TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE erp.departments (
    id UUID PRIMARY KEY,
    department_code TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    parent_code TEXT,
    budget_amount NUMERIC(18, 2) NOT NULL DEFAULT 0 CHECK (budget_amount >= 0),
    budget_period TEXT NOT NULL DEFAULT '2026-H2',
    budget_observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE erp.expense_claims (
    id UUID PRIMARY KEY,
    claim_no TEXT NOT NULL UNIQUE,
    employee_id UUID NOT NULL REFERENCES erp.employees(id),
    department_code TEXT NOT NULL,
    expense_category TEXT NOT NULL,
    amount NUMERIC(18, 2) NOT NULL CHECK (amount > 0),
    currency CHAR(3) NOT NULL DEFAULT 'CNY',
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'DRAFT'
        CHECK (status IN (
            'DRAFT', 'SUBMITTED', 'PENDING_APPROVAL',
            'APPROVED', 'REJECTED', 'PAID', 'CANCELLED'
        )),
    has_receipt BOOLEAN NOT NULL DEFAULT FALSE,
    receipt_ref TEXT,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version >= 1),
    submitted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_expense_claims_employee
    ON erp.expense_claims(employee_id, created_at DESC);

CREATE INDEX idx_expense_claims_status
    ON erp.expense_claims(status, updated_at DESC);

-- 操作流水：Mock ERP 侧记录每次写操作（与平台 integration.erp_operations 对账用）
CREATE TABLE erp.operation_log (
    id UUID PRIMARY KEY,
    idempotency_key TEXT NOT NULL UNIQUE,
    operation TEXT NOT NULL,
    object_type TEXT NOT NULL,
    object_id TEXT NOT NULL,
    request_digest TEXT NOT NULL,
    response_status TEXT NOT NULL,
    response_payload JSONB,
    external_operation_id TEXT NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_operation_log_object
    ON erp.operation_log(object_type, object_id, received_at DESC);
