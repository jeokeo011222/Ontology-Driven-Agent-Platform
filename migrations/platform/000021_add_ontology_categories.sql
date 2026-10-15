-- ODAP 平台迁移 000021：本体分类（ontology.categories）
-- 依据：需求 #1 本体按类别分组，支持多层级 parent_id；类别被本体引用时不允许删除，只能停用。

-- =====================================================================
-- 1. 分类表
-- =====================================================================
CREATE TABLE ontology.categories (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES iam.tenants(id),
    parent_id UUID,                            -- 自引用，null = 根分类
    category_code TEXT NOT NULL,               -- 租户内业务唯一标识，用于接口
    name TEXT NOT NULL,                        -- 展示名
    description TEXT NOT NULL DEFAULT '',
    sort_order INTEGER NOT NULL DEFAULT 0,     -- 同层级排序
    status TEXT NOT NULL DEFAULT 'ACTIVE'
        CHECK (status IN ('ACTIVE', 'INACTIVE')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, category_code),
    FOREIGN KEY (tenant_id, parent_id)
        REFERENCES ontology.categories(tenant_id, id)
);

CREATE INDEX idx_ontology_categories_parent
    ON ontology.categories(tenant_id, parent_id, sort_order);

-- =====================================================================
-- 2. 本体表挂 category
-- =====================================================================
ALTER TABLE ontology.ontologies
    ADD COLUMN category_id UUID;

ALTER TABLE ontology.ontologies
    ADD CONSTRAINT fk_ontologies_category
        FOREIGN KEY (tenant_id, category_id)
        REFERENCES ontology.categories(tenant_id, id);

CREATE INDEX idx_ontology_ontologies_category
    ON ontology.ontologies(tenant_id, category_id);

-- =====================================================================
-- 3. 种子数据：默认根分类（各租户注册时补齐）
--    种子只插入 tenant 级共享的系统根分类，具体租户级分类由管理员在控制台创建。
-- =====================================================================
