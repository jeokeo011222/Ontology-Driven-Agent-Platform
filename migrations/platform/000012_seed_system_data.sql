-- ODAP 平台迁移 000012：系统种子数据（幂等）
-- 依据：第二篇 §11.2（系统内置权限目录、最小管理员角色、默认 Mock ERP Adapter 注册）
-- 注意：不创建任何默认管理员用户与密码（首个管理员经一次性初始化命令或企业 IdP 建立）

-- 系统权限目录（全局共享，resource_type.action 命名）
INSERT INTO governance.permissions (id, permission_key, resource_type, action, description)
VALUES
    (gen_random_uuid(), 'run.create',       'run',       'create', '创建智能体运行任务'),
    (gen_random_uuid(), 'run.read',         'run',       'read',   '查询运行任务与步骤'),
    (gen_random_uuid(), 'run.cancel',       'run',       'cancel', '请求取消运行任务'),
    (gen_random_uuid(), 'approval.decide',  'approval',  'decide', '审批决策'),
    (gen_random_uuid(), 'plugin.register',  'plugin',    'register', '注册插件版本'),
    (gen_random_uuid(), 'plugin.publish',   'plugin',    'publish',  '发布插件版本'),
    (gen_random_uuid(), 'plugin.install',   'plugin',    'install',  '安装/升级插件'),
    (gen_random_uuid(), 'ontology.publish', 'ontology',  'publish',  '发布本体版本'),
    (gen_random_uuid(), 'policy.publish',   'policy',    'publish',  '发布授权策略版本'),
    (gen_random_uuid(), 'evaluation.run',   'evaluation','run',      '执行评估运行'),
    (gen_random_uuid(), 'release.promote',  'release',   'promote',  '推进候选发布'),
    (gen_random_uuid(), 'audit.read',       'audit',     'read',     '查询审计事件与证据')
ON CONFLICT (permission_key) DO NOTHING;

-- 指标注册表种子（第十五篇 §15.7：exclusion_rate 与 business_risk_coverage V1.0 即注册）
INSERT INTO evaluation.metric_registry (
    metric_key, display_name, definition, unit, dimension,
    numerator, denominator, owner
)
VALUES
    (
        'exclusion_rate',
        '案例排除率',
        '被双人审批排除的评估案例数占案例总数的比例；防成功率分母作弊',
        'ratio', 'evaluation_integrity',
        'COUNT(case_exclusions WHERE evaluation_run_id = :run)',
        'COUNT(evaluation.cases WHERE dataset_revision_id = :dataset)',
        'platform-quality'
    ),
    (
        'business_risk_coverage',
        '业务风险覆盖率',
        '被至少一个评估案例覆盖的业务风险项占已登记风险项的比例',
        'ratio', 'evaluation_coverage',
        'COUNT(DISTINCT risk_id covered by cases)',
        'COUNT(risk_registry entries in scope)',
        'platform-quality'
    ),
    (
        'task_success_rate',
        '任务成功率',
        '达到预期终态且无硬门禁违规的评估运行占比',
        'ratio', 'functional',
        'COUNT(case_runs WHERE status = ''SUCCEEDED'' AND gate_blocked = false)',
        'COUNT(case_runs WHERE status IN (''SUCCEEDED'',''FAILED''))',
        'platform-quality'
    )
ON CONFLICT (metric_key) DO NOTHING;

-- 语义图：默认数据图命名（按租户在初始化时插入具体行，此处仅预留类型说明）
-- graph.named_graphs 行由租户初始化流程创建（graph_type: ONTOLOGY | INSTANCE | DERIVED）。
