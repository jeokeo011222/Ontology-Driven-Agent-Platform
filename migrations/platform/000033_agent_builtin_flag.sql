-- ODAP 平台迁移 000033：给 Agent 补「内置」标识
--
-- 背景：
--   平台的自身能力（AI 自动生成企业本体 / Agent / 工具 / 工作流、本体 SHACL 校验、
--   运行编排、ERP 操作对账、模型调用治理网关）此前只以 HTTP 能力存在，Agent 管理页
--   只展示用户自建 Agent，无法表达"系统内置了哪些智能体"。
--
-- 本迁移新增 is_builtin 列，把这层身份落到 registry.agents 上：
--   * is_builtin = true  —— 平台内置智能体，登记在册但**不可修改**
--     （PATCH / DELETE / publish 一律 409 fail-closed，避免绕过界面直接改）；
--   * is_builtin = false —— 用户自建 Agent，行为不变（可编辑 / 发布 / 软删除）。
--
-- 为何用**独立布尔列**而不是复用 status：
--   status 承载的是生命周期（ACTIVE / DELETED），把"内置"塞进同一列会让
--   ListAgents 的 `status <> 'DELETED'` 过滤与前端状态词表同时失真。
--   身份与生命周期是两个正交维度（对照 governance.roles.is_system_role 的既有做法）。
--
-- 本迁移**只做加法**：不改既有列、不改既有行语义（既有 Agent 一律 false），
-- 可重复执行（迁移重放安全）。
-- ============================================================

ALTER TABLE registry.agents
    ADD COLUMN IF NOT EXISTS is_builtin BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN registry.agents.is_builtin IS
    '是否为平台内置智能体：true 表示系统自身能力（AI 自动生成 / 本体校验 / 运行编排 / ERP 对账 / 模型治理），'
    '登记在 Agent 管理页但不可修改（PATCH/DELETE/publish 一律 409）；false 为用户自建 Agent。';
