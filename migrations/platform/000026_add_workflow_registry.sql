-- ODAP 平台迁移 000026：插件式工作流注册表（registry.workflows / registry.workflow_versions）
-- 依据：需求 #3 —— Agent、工具、工作流统一采用插件式（plugin + plugin_version 承载清单），
--       可通过 AI 自动生成清单后落库。此前 registry.plugins.plugin_kind 缺 'WORKFLOW'，
--       且没有工作流定义与版本表，工作流无法作为插件资产登记。
-- 规范：与 registry.agents / registry.tools 完全同构（plugin_id 外键 + 版本不可变由 000018 的
--       触发器统一保证），保证三类资产在列表/发布/校验路径上一致。

-- =====================================================================
-- 1. 扩展 plugin_kind 枚举：新增 WORKFLOW
--    PostgreSQL 对列内联 CHECK 的默认命名为 <table>_<column>_check。
-- =====================================================================
ALTER TABLE registry.plugins
    DROP CONSTRAINT IF EXISTS plugins_plugin_kind_check;

ALTER TABLE registry.plugins
    ADD CONSTRAINT plugins_plugin_kind_check
        CHECK (plugin_kind IN (
            'AGENT', 'ONTOLOGY', 'TOOL', 'POLICY', 'MODEL_PROVIDER', 'WORKFLOW'
        ));

-- =====================================================================
-- 2. 工作流定义（与 registry.agents 同构）
-- =====================================================================
CREATE TABLE registry.workflows (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    plugin_id UUID NOT NULL,
    workflow_key TEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'ACTIVE'
        CHECK (status IN ('ACTIVE', 'DISABLED', 'DELETED')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, workflow_key),
    FOREIGN KEY (tenant_id, plugin_id)
        REFERENCES registry.plugins(tenant_id, id)
);

-- =====================================================================
-- 3. 工作流版本（与 registry.agent_versions 同构）
--    definition：工作流编排文档（节点/边/触发器），JSON，结构由 Harness 解释。
--    input_schema / output_schema：工作流对外契约（校验输入输出）。
-- =====================================================================
CREATE TABLE registry.workflow_versions (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    workflow_id UUID NOT NULL,
    plugin_version_id UUID NOT NULL,
    version TEXT NOT NULL,
    definition JSONB NOT NULL,
    input_schema JSONB NOT NULL DEFAULT '{}'::jsonb,
    output_schema JSONB NOT NULL DEFAULT '{}'::jsonb,
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
    UNIQUE (tenant_id, workflow_id, version),
    FOREIGN KEY (tenant_id, workflow_id)
        REFERENCES registry.workflows(tenant_id, id),
    FOREIGN KEY (tenant_id, plugin_version_id)
        REFERENCES registry.plugin_versions(tenant_id, id)
);

CREATE INDEX idx_workflow_versions_status
    ON registry.workflow_versions(tenant_id, workflow_id, status);
