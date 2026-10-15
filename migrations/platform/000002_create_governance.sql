-- ODAP 平台迁移 000002：governance schema（RBAC/ABAC/策略版本）
-- 依据：第二篇 §3、第三篇 §5.3（scope 六种数据范围）、§6.3（JSON DSL 锁定）

CREATE SCHEMA IF NOT EXISTS governance;

CREATE TABLE governance.roles (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES iam.tenants(id),
    role_key TEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    is_system_role BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, role_key)
);

-- 权限目录为全局共享定义（系统级），不按租户隔离
CREATE TABLE governance.permissions (
    id UUID PRIMARY KEY,
    permission_key TEXT NOT NULL UNIQUE,
    resource_type TEXT NOT NULL,
    action TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT ''
);

CREATE TABLE governance.role_permissions (
    tenant_id UUID NOT NULL,
    role_id UUID NOT NULL,
    permission_id UUID NOT NULL
        REFERENCES governance.permissions(id),
    PRIMARY KEY (tenant_id, role_id, permission_id),
    FOREIGN KEY (tenant_id, role_id)
        REFERENCES governance.roles(tenant_id, id)
);

-- 角色绑定：scope_type 覆盖第三篇 §5.3 六种数据范围
-- （第二篇原文仅 TENANT/ORG，第三篇 P1 修复补齐 ORG_SUBTREE/SELF/ASSIGNED/EXPLICIT）
CREATE TABLE governance.role_bindings (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    membership_id UUID NOT NULL,
    role_id UUID NOT NULL,
    scope_type TEXT NOT NULL DEFAULT 'ORG'
        CHECK (scope_type IN (
            'TENANT', 'ORG', 'ORG_SUBTREE',
            'SELF', 'ASSIGNED', 'EXPLICIT'
        )),
    scope_org_id UUID,
    scope_resource_refs JSONB,
    valid_from TIMESTAMPTZ NOT NULL DEFAULT now(),
    valid_until TIMESTAMPTZ,
    granted_by UUID NOT NULL REFERENCES iam.users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    FOREIGN KEY (tenant_id, membership_id)
        REFERENCES iam.memberships(tenant_id, id),
    FOREIGN KEY (tenant_id, role_id)
        REFERENCES governance.roles(tenant_id, id),
    FOREIGN KEY (tenant_id, scope_org_id)
        REFERENCES iam.organizations(tenant_id, id),

    CHECK (valid_until IS NULL OR valid_until > valid_from),
    CHECK (
        (scope_type = 'TENANT' AND scope_org_id IS NULL)
        OR (scope_type IN ('ORG', 'ORG_SUBTREE') AND scope_org_id IS NOT NULL)
        OR (scope_type IN ('SELF', 'ASSIGNED') AND scope_org_id IS NULL)
        OR (scope_type = 'EXPLICIT' AND scope_resource_refs IS NOT NULL)
    )
);

CREATE INDEX idx_role_bindings_membership
    ON governance.role_bindings(tenant_id, membership_id);

CREATE INDEX idx_role_bindings_scope_org
    ON governance.role_bindings(tenant_id, scope_org_id)
    WHERE scope_org_id IS NOT NULL;

-- 策略版本：policy_language 受 CHECK 约束锁定 V1.0 取值（第三篇 §6.3：JSON DSL 子集）
CREATE TABLE governance.policy_versions (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES iam.tenants(id),
    policy_key TEXT NOT NULL,
    version TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'DRAFT'
        CHECK (status IN (
            'DRAFT', 'VALIDATING', 'PUBLISHED',
            'DISABLED', 'RETIRED'
        )),
    policy_language TEXT NOT NULL
        CHECK (policy_language IN ('JSON_DSL_V1', 'OPA_REGO_V1')),
    policy_document JSONB NOT NULL,
    content_digest TEXT NOT NULL,
    created_by UUID NOT NULL REFERENCES iam.users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, policy_key, version)
);

CREATE INDEX idx_policy_versions_lookup
    ON governance.policy_versions(
        tenant_id, policy_key, status
    );
