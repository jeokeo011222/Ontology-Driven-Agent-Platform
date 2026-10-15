-- ODAP 平台迁移 000034：registry.tools / registry.workflows 增加内置（is_builtin）标志
-- 依据：需求 —— 把系统内置能力（文档读取、本体校验、图谱装载、模型调用治理网关、
--       ERP 适配、审计链、证据哈希链、Harness 运行编排等）以「内置」方式登记在
--       工具管理页 / 工作流与 Harness 页，仅展示、不可修改；与 registry.agents.is_builtin
--       （000033）同构，形成三类插件式资产一致的「内置」口径。
--
-- 语义：
--   is_builtin = true   平台自身能力，由数据集随平台版本登记；PATCH / DELETE / publish
--                       一律 409（在 store 层拦截，不依赖前端隐藏按钮）。
--   is_builtin = false  用户自建资产（缺省值）。
--
-- 与生命周期 status（ACTIVE / DELETED）正交：内置项同样是 ACTIVE 的正常资产，
-- 软删除后仍保留审计链；内置标记不因删除而改变。

-- =====================================================================
-- 1. 工具管理（registry.tools）
-- =====================================================================
ALTER TABLE registry.tools
    ADD COLUMN IF NOT EXISTS is_builtin BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN registry.tools.is_builtin IS
    '是否为平台内置工具：true 表示系统自身能力（文档读取 / 本体校验 / 图谱装载 / 模型调用治理网关 / ERP 适配 / 审计链 / 证据哈希链），登记在工具管理页但不可编辑或删除（PATCH/DELETE 一律 409）。false 为用户自建工具。';

CREATE INDEX IF NOT EXISTS idx_tools_builtin
    ON registry.tools(tenant_id, is_builtin);

-- =====================================================================
-- 2. 工作流与 Harness（registry.workflows）
-- =====================================================================
ALTER TABLE registry.workflows
    ADD COLUMN IF NOT EXISTS is_builtin BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN registry.workflows.is_builtin IS
    '是否为平台内置工作流：true 表示平台自身编排能力（如 Harness 运行编排 / 对账收敛 / 本体生命周期），登记在工作流与 Harness 页但不可编辑、发布或删除（PATCH/DELETE/publish 一律 409）。false 为用户自建工作流。';

CREATE INDEX IF NOT EXISTS idx_workflows_builtin
    ON registry.workflows(tenant_id, is_builtin);
