-- ODAP 平台迁移 000038：技能注册表（registry.skills / registry.skill_versions）
--
-- 背景：Skill 作为一等 registry 资源。Agent 版本通过 dependency_manifest.skills
-- 声明依赖的技能键列表（与 tools 同构）；计划编译时对每个技能取**已发布**版本：
--   - prompt_fragment 作为「## 技能：{name}」小节追加进 Agent 提示词；
--   - tool_bindings（tool_key 字符串数组）并入本次运行可用工具集合；
--   - 技能版本 digest 纳入 VersionLock.ToolBindingDigest（技能变更 → 计划缓存失效）。
--
-- 语义：
--   registry.skills.status          ACTIVE / INACTIVE / DELETED（软删，与 tools 同构）
--   registry.skills.is_builtin      内置技能标记（默认 false；内置项 Update/Delete/版本变更一律 409，
--                                   拦截在 store 层 ensureSkillMutable，与 agents/tools 同口径）
--   registry.skill_versions.status  DRAFT / PUBLISHED（仅这两个状态：技能版本只有草稿与发布，
--                                   发布即对外可绑定；与 tool_versions 的多状态机不同，
--                                   技能无 VALIDATING/APPROVED 等中间门禁）
--   tool_bindings                   JSONB 字符串数组（registry.tools.tool_key 白名单），
--                                   写入时由应用层校验每个 key 存在且 ACTIVE（fail-closed）
--
-- 不写入任何种子技能（内置技能暂不提供，is_builtin 恒为用户自建缺省 false）。

-- =====================================================================
-- 1. 技能定义（registry.skills，与 registry.tools 同构）
-- =====================================================================
CREATE TABLE registry.skills (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    skill_key TEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    domain TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'ACTIVE'
        CHECK (status IN ('ACTIVE', 'INACTIVE', 'DELETED')),
    is_builtin BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, skill_key)
);

COMMENT ON TABLE registry.skills IS
    '技能定义：Agent 通过 dependency_manifest.skills 引用 skill_key；计划编译时取已发布版本展开提示词片段与工具绑定';

-- =====================================================================
-- 2. 技能版本（registry.skill_versions，参照 tool_versions 的约束与索引风格）
--    版本不可变：状态只允许 DRAFT → PUBLISHED 单向迁移（应用层 CAS 保证），
--    content_digest 覆盖 prompt_fragment / tool_bindings / 双 Schema，
--    供计划编译纳入 VersionLock（版本锁定摘要）。
-- =====================================================================
CREATE TABLE registry.skill_versions (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    skill_id UUID NOT NULL,
    version TEXT NOT NULL,
    prompt_fragment TEXT NOT NULL DEFAULT '',
    tool_bindings JSONB NOT NULL DEFAULT '[]'::jsonb,
    input_schema JSONB NOT NULL DEFAULT '{}'::jsonb,
    output_schema JSONB NOT NULL DEFAULT '{}'::jsonb,
    content_digest TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'DRAFT'
        CHECK (status IN ('DRAFT', 'PUBLISHED')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, skill_id, version),
    FOREIGN KEY (tenant_id, skill_id)
        REFERENCES registry.skills(tenant_id, id)
);

CREATE INDEX idx_skill_versions_status
    ON registry.skill_versions(tenant_id, skill_id, status);

-- 运行时按 (skill_key, PUBLISHED) 批量取已发布版本（agent 计划编译的热路径）
CREATE INDEX idx_skill_versions_published
    ON registry.skill_versions(skill_id, status)
    WHERE status = 'PUBLISHED';

-- =====================================================================
-- 3. RLS 行级安全（照抄 000011 对 registry.agents / registry.tools 的租户隔离写法）
-- =====================================================================
SELECT app.apply_tenant_rls('registry', 'skills');
SELECT app.apply_tenant_rls('registry', 'skill_versions');

-- =====================================================================
-- 4. 运行时角色授权（odap_app；000011 的 ALTER DEFAULT PRIVILEGES 已覆盖
--    新表，这里显式 GRANT 一次以保证与迁移执行账号无关的确定性）
-- =====================================================================
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE registry.skills TO odap_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE registry.skill_versions TO odap_app;
