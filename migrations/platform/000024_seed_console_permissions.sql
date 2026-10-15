-- ODAP 平台迁移 000024：控制台权限目录补齐（幂等）
--
-- 依据：第三篇 §5.1 权限命名规范 <domain>.<resource>.<action>；
--       第二篇 §11.2（系统内置权限目录 / 最小化系统管理员角色）；
--       实现符合性评测清单「admin:data-source:read 权限键仅见前端路由守卫，
--       未在 Go/SQL 权限种子中登记」。
--
-- 背景：000012 只登记了 12 个**业务操作**权限（run.create / approval.decide / …）。
-- 但前端路由守卫（web/src/app/router.tsx）与权限门（PermissionGate）还引用了
-- workspace / app / admin.* 等入口级权限键——这些键此前**在目录中根本不存在**，
-- 于是 role_permissions 写入时被 `WHERE permission_key = ANY($2)` 静默跳过，
-- 任何角色都拿不到它们，所有页面恒定判为「权限不足」。
--
-- 本迁移把这些键按设计书的点号规范补齐到全局权限目录（命名与 000012 的
-- run.create 等同构）。权限键只登记定义，具体授予由租户角色（seed 的
-- platform-admin）通过 governance.role_permissions 完成。
--
-- 说明：permissions 表是全局共享定义（000002 注释），不按租户隔离、无 RLS，
-- 因此本迁移无需租户上下文。

INSERT INTO governance.permissions (id, permission_key, resource_type, action, description)
VALUES
    -- 业务工作台入口
    (gen_random_uuid(), 'workspace.view',          'workspace',        'view',      '查看业务工作台与智能助手'),
    (gen_random_uuid(), 'app.read',                'app',              'read',      '查看业务应用目录'),
    (gen_random_uuid(), 'approval.read',           'approval',         'read',      '查看审批列表与详情'),
    (gen_random_uuid(), 'run.reconcile',           'run',              'reconcile', '对账与修复运行任务'),

    -- 平台管理控制台入口
    (gen_random_uuid(), 'admin.console',           'admin',            'console',   '进入平台管理控制台'),

    -- 身份与权限
    (gen_random_uuid(), 'admin.identity.read',     'admin.identity',   'read',      '查看身份、角色与角色绑定'),
    (gen_random_uuid(), 'admin.identity.write',    'admin.identity',   'write',     '维护用户、组织与角色绑定'),

    -- 本体
    (gen_random_uuid(), 'admin.ontology.read',     'admin.ontology',   'read',      '查看本体配置与版本'),
    (gen_random_uuid(), 'admin.ontology.write',    'admin.ontology',   'write',     '维护本体分类'),
    (gen_random_uuid(), 'admin.ontology.validate', 'admin.ontology',   'validate',  '执行本体校验'),
    (gen_random_uuid(), 'admin.ontology.publish',  'admin.ontology',   'publish',   '发布本体版本'),

    -- 数据源映射
    (gen_random_uuid(), 'admin.data-source.read',  'admin.data-source', 'read',     '查看数据源映射'),

    -- 智能体
    (gen_random_uuid(), 'admin.agent.read',        'admin.agent',      'read',      '查看智能体配置'),
    (gen_random_uuid(), 'admin.agent.publish',     'admin.agent',      'publish',   '发布智能体版本'),

    -- 工具
    (gen_random_uuid(), 'admin.tool.read',         'admin.tool',       'read',      '查看工具配置'),

    -- 工作流
    (gen_random_uuid(), 'admin.workflow.read',     'admin.workflow',   'read',      '查看工作流配置'),

    -- 模型与推理
    (gen_random_uuid(), 'admin.model.read',        'admin.model',      'read',      '查看模型供应商与端点'),
    (gen_random_uuid(), 'admin.model.write',       'admin.model',      'write',     '维护模型供应商与端点'),

    -- ERP 连接器
    (gen_random_uuid(), 'admin.connector.read',    'admin.connector',  'read',      '查看 ERP 连接器能力'),
    (gen_random_uuid(), 'admin.connector.write',   'admin.connector',  'write',     '维护 ERP 连接器'),

    -- 授权策略
    (gen_random_uuid(), 'admin.policy.read',       'admin.policy',     'read',      '查看授权策略与版本'),
    (gen_random_uuid(), 'admin.policy.write',      'admin.policy',     'write',     '维护授权策略'),

    -- 运行监控
    (gen_random_uuid(), 'admin.monitoring.read',   'admin.monitoring', 'read',      '查看运行监控与告警'),

    -- 审计
    (gen_random_uuid(), 'admin.audit.read',        'admin.audit',      'read',      '查看审计事件与证据'),

    -- 评估与发布
    (gen_random_uuid(), 'admin.evaluation.read',   'admin.evaluation', 'read',      '查看评估运行与指标'),
    (gen_random_uuid(), 'admin.release.read',      'admin.release',    'read',      '查看候选发布'),
    (gen_random_uuid(), 'admin.release.promote',   'admin.release',    'promote',   '推进候选发布')
ON CONFLICT (permission_key) DO NOTHING;
