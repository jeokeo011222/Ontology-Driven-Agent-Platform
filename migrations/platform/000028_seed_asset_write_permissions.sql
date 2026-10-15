-- ODAP 平台迁移 000028：设计资产（Agent / 工具 / 工作流）写权限目录补齐（幂等）
--
-- 依据：第三篇 §5.1 权限命名规范 <domain>.<resource>.<action>；
--       第二篇 §11.2（系统内置权限目录 / 最小化系统管理员角色）。
--
-- 背景：000024 只登记了 Agent 的 read/publish、工具的 read、工作流的 read，
-- 缺少**写**权限键。于是「Agent 管理 / 工具管理 / 工作流与 Harness」的
-- 新建、编辑、删除、AI 自动生成等写操作在前端无对应权限键可用，
-- 只能整页只读（插件式资产管理等于没实现）。
--
-- 本迁移把三类设计资产的写权限补齐到全局权限目录。权限键只登记定义，
-- 具体授予由租户角色（seed 的 platform-admin）通过 governance.role_permissions
-- 完成（数据集 datasets/finance-demo/dataset.json）。
--
-- 说明：permissions 表是全局共享定义（000002 注释），不按租户隔离、无 RLS，
-- 因此本迁移无需租户上下文。

INSERT INTO governance.permissions (id, permission_key, resource_type, action, description)
VALUES
    (gen_random_uuid(), 'admin.agent.write',    'admin.agent',    'write', '维护智能体配置（新建 / 编辑 / 删除 / 自动生成）'),
    (gen_random_uuid(), 'admin.tool.write',     'admin.tool',     'write', '维护工具配置（新建 / 编辑 / 删除 / 自动生成 / 测试调用）'),
    (gen_random_uuid(), 'admin.workflow.write', 'admin.workflow', 'write', '维护工作流配置（新建 / 编辑 / 删除 / 自动生成 / 发布）')
ON CONFLICT (permission_key) DO NOTHING;
