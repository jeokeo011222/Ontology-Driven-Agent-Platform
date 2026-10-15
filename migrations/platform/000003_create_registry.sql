-- ODAP 平台迁移 000003：registry schema（插件/智能体/工具注册表）
-- 依据：ADR-002（第四篇一统模型为基线 + 第二篇 agents/tools 表合并）
-- 要点：plugins 全局 + plugin_kind 五类（AGENT/ONTOLOGY/TOOL/POLICY/MODEL_PROVIDER）；
--       plugin_installations 保留升级历史（UNIQUE (tenant_id, plugin_id, plugin_version_id)）；
--       平台级插件 tenant_id 为 NULL，复合外键按 MATCH SIMPLE 语义跳过空值。

CREATE SCHEMA IF NOT EXISTS registry;

CREATE TABLE registry.plugins (
    id UUID PRIMARY KEY,
    tenant_id UUID,
    plugin_key TEXT NOT NULL,
    plugin_kind TEXT NOT NULL
        CHECK (plugin_kind IN (
            'AGENT', 'ONTOLOGY', 'TOOL', 'POLICY', 'MODEL_PROVIDER'
        )),
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    owner_org_id UUID,
    status TEXT NOT NULL DEFAULT 'ACTIVE'
        CHECK (status IN ('ACTIVE', 'DISABLED', 'ARCHIVED')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, plugin_key),
    FOREIGN KEY (tenant_id, owner_org_id)
        REFERENCES iam.organizations(tenant_id, id)
);

-- 平台级插件（tenant_id IS NULL）的全局键唯一性
CREATE UNIQUE INDEX idx_plugins_global_key
    ON registry.plugins(plugin_key)
    WHERE tenant_id IS NULL;

CREATE TABLE registry.plugin_versions (
    id UUID PRIMARY KEY,
    tenant_id UUID,
    plugin_id UUID NOT NULL
        REFERENCES registry.plugins(id),
    version TEXT NOT NULL,
    manifest JSONB NOT NULL,
    artifact_uri TEXT,
    artifact_digest TEXT NOT NULL,
    signature_ref TEXT,
    status TEXT NOT NULL DEFAULT 'CREATED'
        CHECK (status IN (
            'CREATED', 'VALIDATING', 'TESTING', 'STAGED',
            'APPROVED', 'PUBLISHED', 'DEPRECATED',
            'DISABLED', 'REJECTED'
        )),
    created_by UUID NOT NULL REFERENCES iam.users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,

    UNIQUE (tenant_id, id),
    UNIQUE (plugin_id, version),
    FOREIGN KEY (tenant_id, plugin_id)
        REFERENCES registry.plugins(tenant_id, id)
);

CREATE INDEX idx_plugin_versions_status
    ON registry.plugin_versions(tenant_id, status);

CREATE TABLE registry.plugin_dependencies (
    id UUID PRIMARY KEY,
    plugin_version_id UUID NOT NULL
        REFERENCES registry.plugin_versions(id),
    dependency_type TEXT NOT NULL CHECK (
        dependency_type IN ('plugin', 'ontology', 'tool', 'policy', 'service')
    ),
    dependency_id TEXT NOT NULL,
    version_constraint TEXT NOT NULL,
    optional BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (plugin_version_id, dependency_type, dependency_id)
);

CREATE TABLE registry.plugin_artifacts (
    id UUID PRIMARY KEY,
    plugin_version_id UUID NOT NULL
        REFERENCES registry.plugin_versions(id),
    artifact_kind TEXT NOT NULL CHECK (
        artifact_kind IN (
            'container_image', 'wasm_module', 'bundle',
            'manifest', 'schema', 'test_report', 'sbom'
        )
    ),
    digest TEXT NOT NULL,
    storage_uri TEXT NOT NULL,
    media_type TEXT NOT NULL,
    size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (plugin_version_id, artifact_kind, digest)
);

-- 安装记录：升级历史保留（第四篇 §5.5 P1 修复：状态机 ACTIVE/UPGRADING/ROLLED_BACK/DISABLED）
CREATE TABLE registry.plugin_installations (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    plugin_id UUID NOT NULL
        REFERENCES registry.plugins(id),
    plugin_version_id UUID NOT NULL
        REFERENCES registry.plugin_versions(id),
    installation_state TEXT NOT NULL CHECK (
        installation_state IN (
            'ACTIVE', 'UPGRADING', 'ROLLED_BACK', 'DISABLED'
        )
    ),
    config_json JSONB NOT NULL DEFAULT '{}',
    installed_by UUID NOT NULL,
    installed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, plugin_id, plugin_version_id)
);

-- 每个 (tenant_id, plugin_id) 同时最多一行生效安装
CREATE UNIQUE INDEX idx_installations_single_active
    ON registry.plugin_installations (tenant_id, plugin_id)
    WHERE installation_state = 'ACTIVE';

CREATE TABLE registry.plugin_validation_runs (
    id UUID PRIMARY KEY,
    plugin_version_id UUID NOT NULL
        REFERENCES registry.plugin_versions(id),
    validation_stage TEXT NOT NULL,
    result TEXT NOT NULL CHECK (
        result IN ('PASSED', 'FAILED', 'WARNING', 'CANCELLED')
    ),
    report_digest TEXT,
    report_uri TEXT,
    details_json JSONB NOT NULL DEFAULT '{}',
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ
);

CREATE TABLE registry.plugin_reviews (
    id UUID PRIMARY KEY,
    plugin_version_id UUID NOT NULL
        REFERENCES registry.plugin_versions(id),
    reviewer_id UUID NOT NULL,
    decision TEXT NOT NULL CHECK (
        decision IN ('APPROVED', 'REJECTED', 'CHANGES_REQUESTED')
    ),
    reason TEXT NOT NULL,
    reviewed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE registry.plugin_capability_declarations (
    id UUID PRIMARY KEY,
    tenant_id UUID,
    plugin_version_id UUID NOT NULL,
    capability_name TEXT NOT NULL,
    capability_type TEXT NOT NULL CHECK (
        capability_type IN ('consume', 'provide')
    ),
    description TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (plugin_version_id, capability_name, capability_type),
    FOREIGN KEY (plugin_version_id)
        REFERENCES registry.plugin_versions(id),
    FOREIGN KEY (tenant_id, plugin_version_id)
        REFERENCES registry.plugin_versions(tenant_id, id)
);

CREATE INDEX idx_capability_declarations_name
    ON registry.plugin_capability_declarations (capability_name);

CREATE TABLE registry.plugin_signatures (
    id UUID PRIMARY KEY,
    tenant_id UUID,
    plugin_version_id UUID NOT NULL,
    artifact_kind TEXT NOT NULL CHECK (
        artifact_kind IN (
            'container_image', 'wasm_module', 'bundle',
            'manifest', 'schema', 'test_report', 'sbom'
        )
    ),
    artifact_digest TEXT NOT NULL,
    signature_algorithm TEXT NOT NULL CHECK (
        signature_algorithm IN (
            'ed25519', 'ecdsa-p256-sha256', 'rsa-pss-sha256'
        )
    ),
    key_id TEXT NOT NULL,
    signature TEXT NOT NULL,
    signer_id UUID NOT NULL,
    signed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    verified_at TIMESTAMPTZ,

    UNIQUE (plugin_version_id, artifact_kind, artifact_digest, key_id),
    FOREIGN KEY (plugin_version_id)
        REFERENCES registry.plugin_versions(id),
    FOREIGN KEY (tenant_id, plugin_version_id)
        REFERENCES registry.plugin_versions(tenant_id, id)
);

CREATE TABLE registry.plugin_publications (
    id UUID PRIMARY KEY,
    tenant_id UUID,
    plugin_id UUID NOT NULL,
    plugin_version_id UUID NOT NULL,
    environment TEXT NOT NULL CHECK (
        environment IN ('DEVELOPMENT', 'STAGING', 'PRODUCTION')
    ),
    published_by UUID NOT NULL,
    published_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ,

    FOREIGN KEY (plugin_id)
        REFERENCES registry.plugins(id),
    FOREIGN KEY (plugin_version_id)
        REFERENCES registry.plugin_versions(id),
    FOREIGN KEY (tenant_id, plugin_id)
        REFERENCES registry.plugins(tenant_id, id)
);

-- 同一版本在同一环境同时只允许一条有效发布
CREATE UNIQUE INDEX idx_plugin_publications_active
    ON registry.plugin_publications (plugin_version_id, environment)
    WHERE revoked_at IS NULL;

-- 智能体定义与版本（第二篇 §4.2；运行时固定 agent_version_id 而非 agent_key）
CREATE TABLE registry.agents (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    plugin_id UUID NOT NULL,
    agent_key TEXT NOT NULL,
    domain TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'ACTIVE',

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, agent_key),
    FOREIGN KEY (tenant_id, plugin_id)
        REFERENCES registry.plugins(tenant_id, id)
);

CREATE TABLE registry.agent_versions (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    agent_id UUID NOT NULL,
    plugin_version_id UUID NOT NULL,
    version TEXT NOT NULL,
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    prompt_template JSONB NOT NULL DEFAULT '{}'::jsonb,
    input_schema JSONB NOT NULL,
    output_schema JSONB NOT NULL,
    dependency_manifest JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'DRAFT'
        CHECK (status IN (
            'DRAFT', 'VALIDATING', 'APPROVED',
            'PUBLISHED', 'DEPRECATED', 'DISABLED'
        )),
    content_digest TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, agent_id, version),
    FOREIGN KEY (tenant_id, agent_id)
        REFERENCES registry.agents(tenant_id, id),
    FOREIGN KEY (tenant_id, plugin_version_id)
        REFERENCES registry.plugin_versions(tenant_id, id)
);

-- 工具定义与版本（第二篇 §4.3；capabilities 只是声明，不等于授权）
CREATE TABLE registry.tools (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    plugin_id UUID NOT NULL,
    tool_key TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'ACTIVE',

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, tool_key),
    FOREIGN KEY (tenant_id, plugin_id)
        REFERENCES registry.plugins(tenant_id, id)
);

CREATE TABLE registry.tool_versions (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    tool_id UUID NOT NULL,
    plugin_version_id UUID NOT NULL,
    version TEXT NOT NULL,
    input_schema JSONB NOT NULL,
    output_schema JSONB NOT NULL,
    capabilities TEXT[] NOT NULL DEFAULT '{}',
    effect_class TEXT NOT NULL DEFAULT 'READ'
        CHECK (effect_class IN ('READ', 'EXTERNAL_WRITE', 'INTERNAL_WRITE')),
    execution_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    content_digest TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'DRAFT'
        CHECK (status IN (
            'DRAFT', 'VALIDATING', 'APPROVED',
            'PUBLISHED', 'DEPRECATED', 'DISABLED'
        )),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, tool_id, version),
    FOREIGN KEY (tenant_id, tool_id)
        REFERENCES registry.tools(tenant_id, id),
    FOREIGN KEY (tenant_id, plugin_version_id)
        REFERENCES registry.plugin_versions(tenant_id, id)
);

-- 已发布版本不可变：应用角色只读，UPDATE/DELETE 由触发器拒绝（第四篇 P2 修复）
CREATE OR REPLACE FUNCTION registry.reject_published_version_mutation()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.status IN ('PUBLISHED', 'DEPRECATED', 'DISABLED', 'REJECTED') THEN
        RAISE EXCEPTION
            'registry: published plugin_version % is immutable (status=%)',
            OLD.id, OLD.status;
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_plugin_versions_immutable
    BEFORE UPDATE OR DELETE ON registry.plugin_versions
    FOR EACH ROW
    EXECUTE FUNCTION registry.reject_published_version_mutation();
