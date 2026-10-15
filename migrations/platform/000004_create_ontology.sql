-- ODAP 平台迁移 000004：ontology schema（本体包/版本/制品/依赖/验证/发布）
-- 依据：第二篇 §5

CREATE SCHEMA IF NOT EXISTS ontology;

CREATE TABLE ontology.ontologies (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES iam.tenants(id),
    ontology_key TEXT NOT NULL,
    name TEXT NOT NULL,
    domain TEXT NOT NULL,
    granularity TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    owner_org_id UUID,
    status TEXT NOT NULL DEFAULT 'ACTIVE',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, ontology_key),
    FOREIGN KEY (tenant_id, owner_org_id)
        REFERENCES iam.organizations(tenant_id, id)
);

CREATE TABLE ontology.ontology_versions (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    ontology_id UUID NOT NULL,
    version TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'DRAFT'
        CHECK (status IN (
            'DRAFT', 'VALIDATING', 'VALID',
            'REJECTED', 'APPROVED', 'PUBLISHED',
            'DEPRECATED', 'DISABLED'
        )),
    manifest JSONB NOT NULL,
    content_digest TEXT NOT NULL,
    semantic_digest TEXT,
    created_by UUID NOT NULL REFERENCES iam.users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, ontology_id, version),
    FOREIGN KEY (tenant_id, ontology_id)
        REFERENCES ontology.ontologies(tenant_id, id)
);

CREATE INDEX idx_ontology_versions_status
    ON ontology.ontology_versions(
        tenant_id, ontology_id, status
    );

CREATE TABLE ontology.ontology_artifacts (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    ontology_version_id UUID NOT NULL,
    artifact_type TEXT NOT NULL
        CHECK (artifact_type IN (
            'OWL', 'RDF', 'SHACL', 'MAPPING',
            'RULES', 'TESTS', 'EVIDENCE', 'DOCUMENT'
        )),
    logical_path TEXT NOT NULL,
    media_type TEXT NOT NULL,
    storage_uri TEXT NOT NULL,
    content_digest TEXT NOT NULL,
    size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, ontology_version_id, logical_path),
    FOREIGN KEY (tenant_id, ontology_version_id)
        REFERENCES ontology.ontology_versions(tenant_id, id)
);

-- 依赖图：外键不足以保证合法（可能成环），发布流程必须检查依赖闭包与循环导入
CREATE TABLE ontology.ontology_dependencies (
    tenant_id UUID NOT NULL,
    source_version_id UUID NOT NULL,
    target_version_id UUID NOT NULL,
    dependency_type TEXT NOT NULL
        CHECK (dependency_type IN (
            'IMPORTS', 'EXTENDS', 'MAPS_TO', 'CONSTRAINS'
        )),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (
        tenant_id, source_version_id,
        target_version_id, dependency_type
    ),

    FOREIGN KEY (tenant_id, source_version_id)
        REFERENCES ontology.ontology_versions(tenant_id, id),
    FOREIGN KEY (tenant_id, target_version_id)
        REFERENCES ontology.ontology_versions(tenant_id, id),

    CHECK (source_version_id <> target_version_id)
);

CREATE TABLE ontology.validation_runs (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    ontology_version_id UUID NOT NULL,
    validator_version TEXT NOT NULL,
    input_digest TEXT NOT NULL,
    status TEXT NOT NULL
        CHECK (status IN (
            'RUNNING', 'PASSED', 'FAILED', 'ERROR'
        )),
    syntax_valid BOOLEAN,
    owl_consistent BOOLEAN,
    shacl_conformant BOOLEAN,
    business_rules_valid BOOLEAN,
    report_uri TEXT,
    report_digest TEXT,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,

    UNIQUE (tenant_id, id),
    FOREIGN KEY (tenant_id, ontology_version_id)
        REFERENCES ontology.ontology_versions(tenant_id, id)
);

-- 发布记录：区分"曾经发布过"与"当前生产正在使用哪个版本"（第二篇 §5.5）
CREATE TABLE ontology.publications (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    ontology_id UUID NOT NULL,
    ontology_version_id UUID NOT NULL,
    environment TEXT NOT NULL
        CHECK (environment IN ('DEVELOPMENT', 'STAGING', 'PRODUCTION')),
    published_by UUID NOT NULL REFERENCES iam.users(id),
    published_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ,
    rollback_from_publication_id UUID,

    UNIQUE (tenant_id, id),
    FOREIGN KEY (tenant_id, ontology_id)
        REFERENCES ontology.ontologies(tenant_id, id),
    FOREIGN KEY (tenant_id, ontology_version_id)
        REFERENCES ontology.ontology_versions(tenant_id, id)
);

-- 同一环境同一本体同时只有一条有效发布指针
CREATE UNIQUE INDEX idx_ontology_publications_active
    ON ontology.publications (tenant_id, ontology_id, environment)
    WHERE revoked_at IS NULL;
