-- ODAP 平台迁移 000001：iam schema（租户/组织/用户/成员关系/会话）
-- 依据：第二篇 §2、第三篇 §4.4（sessions）、§5.3（org_closure）
-- 规范：主键 UUID；租户业务表带 tenant_id；时间统一 timestamptz；复合外键防跨租户关联

CREATE SCHEMA IF NOT EXISTS iam;

CREATE TABLE iam.tenants (
    id UUID PRIMARY KEY,
    tenant_key TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'ACTIVE'
        CHECK (status IN ('ACTIVE', 'SUSPENDED', 'ARCHIVED')),
    settings JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE iam.organizations (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES iam.tenants(id),
    parent_id UUID,
    org_code TEXT NOT NULL,
    name TEXT NOT NULL,
    org_type TEXT NOT NULL DEFAULT 'DEPARTMENT',
    status TEXT NOT NULL DEFAULT 'ACTIVE'
        CHECK (status IN ('ACTIVE', 'INACTIVE')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, org_code),
    FOREIGN KEY (tenant_id, parent_id)
        REFERENCES iam.organizations(tenant_id, id)
);

CREATE INDEX idx_organizations_parent
    ON iam.organizations(tenant_id, parent_id);

CREATE TABLE iam.users (
    id UUID PRIMARY KEY,
    login_name TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    email TEXT,
    identity_provider TEXT,
    external_subject TEXT,
    -- subject_version：停用/解绑/权限变更时递增，使旧会话令牌全部失配（第三篇 §4.4 撤销传播核心机制）
    subject_version BIGINT NOT NULL DEFAULT 1 CHECK (subject_version >= 1),
    status TEXT NOT NULL DEFAULT 'ACTIVE'
        CHECK (status IN ('ACTIVE', 'DISABLED', 'LOCKED')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE iam.memberships (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES iam.tenants(id),
    user_id UUID NOT NULL REFERENCES iam.users(id),
    org_id UUID NOT NULL,
    membership_type TEXT NOT NULL DEFAULT 'EMPLOYEE',
    status TEXT NOT NULL DEFAULT 'ACTIVE'
        CHECK (status IN ('ACTIVE', 'INACTIVE')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, user_id, org_id),
    FOREIGN KEY (tenant_id, org_id)
        REFERENCES iam.organizations(tenant_id, id)
);

CREATE INDEX idx_memberships_user
    ON iam.memberships(tenant_id, user_id);

-- 会话表（第三篇 §4.4）：短期 TTL + 刷新令牌轮换 + subject_version 撤销传播
CREATE TABLE iam.sessions (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    subject_id UUID NOT NULL,
    subject_type TEXT NOT NULL CHECK (
        subject_type IN ('HUMAN', 'SERVICE', 'AGENT', 'SYSTEM')
    ),
    subject_version BIGINT NOT NULL DEFAULT 1,
    auth_method TEXT NOT NULL,
    issued_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_active_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    revoke_reason TEXT,
    refresh_token_hash TEXT NOT NULL,
    refresh_expires_at TIMESTAMPTZ NOT NULL,
    refresh_rotated_at TIMESTAMPTZ,

    CHECK (expires_at > issued_at),
    CHECK (revoked_at IS NULL OR revoked_at >= issued_at)
);

CREATE INDEX idx_sessions_subject_active
    ON iam.sessions(tenant_id, subject_id, subject_version)
    WHERE revoked_at IS NULL;

-- 组织闭包表（第三篇 §5.3）：支撑 ORG_SUBTREE 传递展开，每组织对自身 depth=0
CREATE TABLE iam.org_closure (
    tenant_id UUID NOT NULL,
    ancestor_id UUID NOT NULL,
    descendant_id UUID NOT NULL,
    depth INTEGER NOT NULL CHECK (depth >= 0),
    PRIMARY KEY (tenant_id, ancestor_id, descendant_id),

    FOREIGN KEY (tenant_id, ancestor_id)
        REFERENCES iam.organizations(tenant_id, id),
    FOREIGN KEY (tenant_id, descendant_id)
        REFERENCES iam.organizations(tenant_id, id)
);

CREATE INDEX idx_org_closure_descendant
    ON iam.org_closure(tenant_id, descendant_id);
