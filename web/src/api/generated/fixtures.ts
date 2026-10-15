/**
 * api/generated/fixtures.ts —— 静态样例数据（仅演示/构建用）
 *
 * 启用方式：环境变量 VITE_USE_FIXTURES=1（默认关闭，走真实请求）。
 * 重要约束：
 *   1. 本文件不改变任何契约路径与响应结构——所有数据通过同一 client 层按
 *      contracts/03 的路径与 `{items, next_cursor}` / 错误结构返回。
 *   2. 样例数据仅用于“无后端时的 UI 演示”，不得作为业务真值来源；
 *      页面上的状态仍由后端状态驱动（§4.1 功能要求 3）。
 *   3. 不得包含任何密钥、凭据明文（§13.3）。
 */

import type {
  AdminOverview,
  AgentInfo,
  ApprovalDetail,
  ApprovalSummary,
  AsyncTask,
  AuditEvent,
  BusinessApp,
  Connector,
  CursorPage,
  DataSource,
  ErpOperation,
  EvaluationRun,
  OntologyDetail,
  OntologySummary,
  OpsAlert,
  PlanTemplate,
  Policy,
  Release,
  Role,
  RoleBinding,
  Run,
  RunStep,
  ToolInfo,
  WorkspaceSummary,
} from '@/types';

/* ============================ 工具 ============================ */

const NOW = Date.now();
const iso = (minutesAgo: number): string => new Date(NOW - minutesAgo * 60_000).toISOString();

export type HttpMethod = 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';
export type QueryValue = string | number | boolean | undefined | null;

export interface FixtureRequest {
  method: HttpMethod;
  /** 已去掉 /api/v1 前缀的路径，如 /runs/run-1001 */
  path: string;
  query?: Record<string, QueryValue>;
  body?: unknown;
}

export type FixtureResolution = { hit: true; data: unknown } | { hit: false };

/* ============================ 样例数据 ============================ */

const ACTOR_LI = { subject_id: 'u_li', display_name: '李明', org_path: '财务部/费用组' };
const ACTOR_WANG = { subject_id: 'u_wang', display_name: '王芳', org_path: '财务部' };
const ACTOR_ADMIN = { subject_id: 'u_admin', display_name: '系统管理员', org_path: '平台/运维' };

export const FIXTURE_RUNS: Run[] = [
  {
    id: 'run-1001',
    objective: '检查报销单 claim-1001，核对凭证、费用类别与预算，并在符合制度时提交审批',
    status: 'WAITING_APPROVAL',
    business_type: 'expense_claim_review',
    subject_id: ACTOR_LI.subject_id,
    ontology_version: 'finance-ontology@1.2.0',
    plan_template_version: 'expense-review@3',
    progress: 60,
    created_at: iso(95),
    updated_at: iso(12),
    waiting_reason: '等待财务主管审批（审批单 apv-1）',
    resource_refs: ['claim-1001'],
  },
  {
    id: 'run-1004',
    objective: '对已审批的付款指令执行付款并核验回执',
    status: 'REQUIRES_RECONCILIATION',
    business_type: 'payment_execute',
    subject_id: ACTOR_LI.subject_id,
    ontology_version: 'finance-ontology@1.2.0',
    plan_template_version: 'payment-exec@2',
    progress: 80,
    created_at: iso(200),
    updated_at: iso(30),
    waiting_reason: '外部付款操作结果未知，等待对账确认（reconciliation rec_9）',
    resource_refs: ['payment-77'],
  },
  {
    id: 'run-1002',
    objective: '审查采购合同 contract-2201 的条款风险与授权范围',
    status: 'RUNNING',
    business_type: 'contract_review',
    subject_id: ACTOR_WANG.subject_id,
    ontology_version: 'procurement-ontology@0.9.1',
    plan_template_version: 'contract-review@1',
    progress: 35,
    created_at: iso(24),
    updated_at: iso(1),
    resource_refs: ['contract-2201'],
  },
  {
    id: 'run-1003',
    objective: '核对报销单 claim-0998 的费用类别并提交审批',
    status: 'COMPLETED',
    business_type: 'expense_claim_review',
    subject_id: ACTOR_LI.subject_id,
    ontology_version: 'finance-ontology@1.2.0',
    progress: 100,
    created_at: iso(300),
    updated_at: iso(240),
    resource_refs: ['claim-0998'],
  },
  {
    id: 'run-1005',
    objective: '供应商 supplier-501 的准入资质校验',
    status: 'FAILED',
    business_type: 'supplier_onboarding',
    subject_id: ACTOR_WANG.subject_id,
    ontology_version: 'procurement-ontology@0.9.1',
    progress: 45,
    created_at: iso(420),
    updated_at: iso(410),
    waiting_reason: '资质文件缺失且无法自动补齐',
    resource_refs: ['supplier-501'],
  },
  {
    id: 'run-1006',
    objective: '查询企业差旅制度的适用范围',
    status: 'CANCELLED',
    business_type: 'knowledge_qa',
    subject_id: ACTOR_LI.subject_id,
    progress: 20,
    created_at: iso(90),
    updated_at: iso(88),
    resource_refs: [],
  },
  {
    id: 'run-1007',
    objective: '同步供应商主数据并处理冲突项',
    status: 'MANUAL_REVIEW',
    business_type: 'data_sync',
    subject_id: ACTOR_ADMIN.subject_id,
    progress: 70,
    created_at: iso(600),
    updated_at: iso(560),
    waiting_reason: '三个来源对同一供应商主键给出冲突值，需人工判定',
    resource_refs: ['supplier_master'],
  },
  {
    id: 'run-1008',
    objective: '月度费用预算执行情况分析',
    status: 'CREATED',
    business_type: 'analysis',
    subject_id: ACTOR_LI.subject_id,
    progress: 0,
    created_at: iso(2),
    updated_at: iso(2),
    resource_refs: [],
  },
];

export const FIXTURE_RUN_STEPS: Record<string, RunStep[]> = {
  'run-1001': [
    {
      id: 'st-1',
      run_id: 'run-1001',
      seq: 1,
      name: '读取报销单及关联业务数据',
      kind: 'READ_OBJECT',
      status: 'SUCCEEDED',
      rationale: '按对象 ID claim-1001 读取报销单、费用明细与提交人信息',
      ontology_version: 'finance-ontology@1.2.0',
      fact_refs: ['expense_claim:claim-1001', 'employee:u_li'],
      started_at: iso(94),
      finished_at: iso(93),
    },
    {
      id: 'st-2',
      run_id: 'run-1001',
      seq: 2,
      name: '检查本体约束、凭证完整性与费用规则',
      kind: 'ONTOLOGY_VALIDATE',
      status: 'SUCCEEDED',
      rationale: 'SHACL 校验通过 12 条约束；凭证 3 张齐全；命中规则 expense.category.allowed',
      ontology_version: 'finance-ontology@1.2.0',
      rule_refs: ['expense.category.allowed@2', 'expense.receipt.required@1'],
      fact_refs: ['receipt:rc-1', 'receipt:rc-2', 'receipt:rc-3'],
      started_at: iso(93),
      finished_at: iso(90),
    },
    {
      id: 'st-3',
      run_id: 'run-1001',
      seq: 3,
      name: '核验当前用户权限与所需审批',
      kind: 'PERMISSION_CHECK',
      status: 'SUCCEEDED',
      rationale: '本体与业务规则均通过；策略 finance.expense.submit 判定为 approval_required',
      rule_refs: ['finance.expense.submit@1.0.0'],
      started_at: iso(90),
      finished_at: iso(89),
    },
    {
      id: 'st-4',
      run_id: 'run-1001',
      seq: 4,
      name: '提交审批并核验结果',
      kind: 'ERP_OPERATION',
      status: 'WAITING_APPROVAL',
      rationale: '已生成审批单 apv-1，等待财务主管审批；通过后才执行外部提交',
      rule_refs: ['finance.expense.submit@1.0.0'],
      started_at: iso(89),
    },
    {
      id: 'st-5',
      run_id: 'run-1001',
      seq: 5,
      name: '生成证据包',
      kind: 'EVIDENCE',
      status: 'PENDING',
      rationale: '等待外部操作完成后生成完整证据包',
    },
  ],
  'run-1004': [
    {
      id: 'st-41',
      run_id: 'run-1004',
      seq: 1,
      name: '读取已审批付款指令',
      kind: 'READ_OBJECT',
      status: 'SUCCEEDED',
      ontology_version: 'finance-ontology@1.2.0',
      started_at: iso(199),
      finished_at: iso(198),
    },
    {
      id: 'st-42',
      run_id: 'run-1004',
      seq: 2,
      name: '权限与策略检查',
      kind: 'PERMISSION_CHECK',
      status: 'SUCCEEDED',
      rationale: '审批单 apv-9 已通过，payload_digest 与当前参数一致',
      rule_refs: ['finance.payment.execute@1.1.0'],
      started_at: iso(198),
      finished_at: iso(197),
    },
    {
      id: 'st-43',
      run_id: 'run-1004',
      seq: 3,
      name: '执行外部付款操作',
      kind: 'ERP_OPERATION',
      status: 'UNKNOWN',
      rationale: '请求已派发（幂等键 pay-77），但在超时窗口内未取得回执',
      tool_id: 'erp.payment.create',
      erp_operation_id: 'erp-op-77',
      started_at: iso(197),
      error_code: 'ERP_OPERATION_UNKNOWN',
      error_message: '外部付款操作结果尚未确认，已转入对账',
    },
    {
      id: 'st-44',
      run_id: 'run-1004',
      seq: 4,
      name: '结果对账',
      kind: 'RECONCILE',
      status: 'RECONCILING',
      rationale: '按幂等键 pay-77 查询远端真实状态（对账 rec_9）',
      started_at: iso(180),
    },
  ],
};

export const FIXTURE_ERP_OPERATIONS: ErpOperation[] = [
  {
    id: 'erp-op-77',
    run_id: 'run-1004',
    status: 'UNKNOWN',
    action: 'CREATE_PAYMENT',
    resource_type: 'Payment',
    resource_id: 'payment-77',
    idempotency_key_ref: 'pay-77',
    dispatched_at: iso(197),
    updated_at: iso(180),
    reconciliation_id: 'rec_9',
  },
  {
    id: 'erp-op-30',
    run_id: 'run-1003',
    status: 'SUCCEEDED',
    action: 'SUBMIT_FOR_APPROVAL',
    resource_type: 'ExpenseClaim',
    resource_id: 'claim-0998',
    idempotency_key_ref: 'claim-0998-v2',
    dispatched_at: iso(260),
    updated_at: iso(255),
    receipt: { receipt_id: 'rcpt-88121', received_at: iso(255), summary: '远端受理成功' },
  },
];

export const FIXTURE_APPROVALS: ApprovalSummary[] = [
  {
    id: 'apv-1',
    run_id: 'run-1001',
    title: '报销单 claim-1001 提交审批',
    status: 'PENDING',
    risk_level: 'high',
    resource_type: 'ExpenseClaim',
    resource_id: 'claim-1001',
    requester: ACTOR_LI,
    created_at: iso(89),
    erp_operation_status: undefined,
  },
  {
    id: 'apv-2',
    run_id: 'run-1002',
    title: '采购合同 contract-2201 条款变更',
    status: 'PENDING',
    risk_level: 'medium',
    resource_type: 'Contract',
    resource_id: 'contract-2201',
    requester: ACTOR_WANG,
    created_at: iso(20),
  },
  {
    id: 'apv-9',
    run_id: 'run-1004',
    title: '付款指令 payment-77 执行授权',
    status: 'APPROVED',
    risk_level: 'critical',
    resource_type: 'Payment',
    resource_id: 'payment-77',
    requester: ACTOR_LI,
    created_at: iso(210),
    erp_operation_status: 'UNKNOWN',
  },
  {
    id: 'apv-7',
    run_id: 'run-1005',
    title: '供应商 supplier-501 准入例外放行',
    status: 'REJECTED',
    risk_level: 'medium',
    resource_type: 'Supplier',
    resource_id: 'supplier-501',
    requester: ACTOR_WANG,
    created_at: iso(430),
  },
];

export const FIXTURE_APPROVAL_DETAILS: Record<string, ApprovalDetail> = {
  'apv-1': {
    ...FIXTURE_APPROVALS[0],
    operation: {
      action: 'SUBMIT_FOR_APPROVAL',
      description: '将报销单 claim-1001 提交至 ERP 审批流',
      expected_impact: '单据状态由“草稿”变为“审批中”，不可自动撤回，需人工撤销',
      reversible: true,
    },
    parameters: {
      claim_id: 'claim-1001',
      amount: 12800.5,
      currency: 'CNY',
      expense_category: '差旅费',
      cost_center: 'CC-1002',
      receipt_count: 3,
    },
    parameter_version: 'params@3',
    payload_digest: {
      algo: 'sha256',
      value: '9f2c1d7ab4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f',
      bound_at: iso(89),
    },
    agent_recommendation: {
      decision: '建议通过',
      rationale: '凭证齐全、费用类别在允许范围内、预算充足；唯一异常为单笔金额超过 10000 需主管审批',
    },
    rules: [
      { rule_id: 'expense.category.allowed', version: '2', outcome: 'PASS', evidence_ref: 'rule-run-1001-1' },
      { rule_id: 'expense.receipt.required', version: '1', outcome: 'PASS', evidence_ref: 'rule-run-1001-2' },
      { rule_id: 'expense.amount.approval_threshold', version: '3', outcome: 'REQUIRE_APPROVAL', evidence_ref: 'rule-run-1001-3' },
    ],
    exceptions: ['单笔金额 12800.50 超过 10000 的自动执行阈值'],
    authorization_scope: ['tenant:current', 'org:财务部', 'resource_type:ExpenseClaim', 'action:SUBMIT_FOR_APPROVAL'],
    approval_chain: [
      { step: 1, approver: ACTOR_WANG, state: 'PENDING' },
      { step: 2, approver: { subject_id: 'u_cfo', display_name: '赵强', org_path: '财务部/管理层' }, state: 'WAITING' },
    ],
    required_approvals: 2,
    granted_approvals: 0,
  },
  'apv-9': {
    ...FIXTURE_APPROVALS[2],
    operation: {
      action: 'CREATE_PAYMENT',
      description: '向供应商 supplier-501 支付 320000.00 CNY',
      expected_impact: '产生实际资金支出，属不可逆财务操作',
      reversible: false,
    },
    parameters: {
      payment_id: 'payment-77',
      payee: 'supplier-501',
      amount: 320000,
      currency: 'CNY',
      scheduled_date: '2026-10-14',
    },
    parameter_version: 'params@1',
    payload_digest: {
      algo: 'sha256',
      value: '3c7f9a1b2d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8',
      bound_at: iso(208),
    },
    agent_recommendation: { decision: '建议通过', rationale: '合同与验收单齐备，付款计划与合同分期一致' },
    rules: [{ rule_id: 'finance.payment.execute', version: '1.1.0', outcome: 'REQUIRE_APPROVAL', evidence_ref: 'rule-run-1004-1' }],
    exceptions: [],
    authorization_scope: ['tenant:current', 'org:财务部', 'resource_type:Payment', 'action:CREATE_PAYMENT'],
    approval_chain: [
      { step: 1, approver: ACTOR_WANG, state: 'APPROVED' },
      { step: 2, approver: { subject_id: 'u_cfo', display_name: '赵强', org_path: '财务部/管理层' }, state: 'APPROVED' },
    ],
    required_approvals: 2,
    granted_approvals: 2,
  },
};

/**
 * 本体样例数据。
 *
 * 字段口径必须与 contracts/openapi.yaml#/components/schemas/Ontology 及真实
 * 接口（GET /api/v1/ontologies）**逐字段一致**：不存在 state/version/based_on/
 * updated_at/owner；版本生命周期在 `latest_version_status`，取值来自
 * migrations/platform/000004 的 ontology_versions.status CHECK 约束。
 *
 * 历史教训（自查矩阵缺陷 #19）：本文件此前按前端自造的 state/version/owner
 * 构造数据，导致页面只"对 fixture 绿、对真实接口崩"——样例数据偏离契约会
 * 把契约漂移掩盖成"已验证通过"。
 */
export const FIXTURE_ONTOLOGIES: OntologySummary[] = [
  {
    id: 'f0000000-0000-4000-8000-000000000001',
    ontology_key: 'finance-core',
    name: '财务核心本体',
    domain: 'FINANCE',
    granularity: 'DOMAIN',
    description: '报销垂直切片的核心本体：报销单、员工、预算、部门及其关系（第五篇 §4）',
    status: 'ACTIVE',
    latest_version_id: 'f0000000-0000-4000-8000-000000000002',
    latest_version: '0.1.0',
    latest_version_status: 'PUBLISHED',
  },
  {
    id: 'onto-procurement',
    ontology_key: 'procurement-core',
    name: '采购核心本体',
    domain: 'PROCUREMENT',
    granularity: 'DOMAIN',
    description: '采购合同、供应商与收货单的语义模型（样例数据）',
    status: 'ACTIVE',
    latest_version_id: 'onto-procurement-v1',
    latest_version: '0.9.1',
    latest_version_status: 'VALID',
  },
  {
    id: 'onto-hr',
    ontology_key: 'hr-core',
    name: '人力核心本体',
    domain: 'HR',
    granularity: 'DOMAIN',
    description: '员工、组织与岗位的语义模型（样例数据）',
    status: 'ACTIVE',
    latest_version_id: 'onto-hr-v1',
    latest_version: '0.4.0',
    latest_version_status: 'VALIDATING',
  },
];

export const FIXTURE_ONTOLOGY_DETAILS: Record<string, OntologyDetail> = {
  'f0000000-0000-4000-8000-000000000001': {
    ...FIXTURE_ONTOLOGIES[0],
    release_notes: '新增付款指令实体与授权约束；调整费用类别枚举',
    classes: [
      {
        iri: 'odap:ExpenseClaim',
        label: '报销单',
        description: '企业业务对象：员工报销单据',
        relations: [
          { predicate: 'submittedBy', target: 'odap:Employee', label: '提交人关系' },
          { predicate: 'expenseCategory', target: 'odap:ExpenseCategory', label: '费用类别关系' },
          { predicate: 'hasReceipt', target: 'odap:Receipt', label: '凭证关联' },
        ],
      },
      {
        iri: 'odap:Payment',
        label: '付款指令',
        description: '对外付款的执行对象',
        relations: [{ predicate: 'payee', target: 'odap:Supplier', label: '收款方' }],
      },
      {
        iri: 'odap:Employee',
        label: '员工',
        relations: [{ predicate: 'belongsToOrg', target: 'odap:Organization', label: '所属组织' }],
      },
    ],
    findings: [
      {
        // 字段与"真实投影"逐项对齐（internal/api/store/ontology_validation.go 的
        // queryLatestFindings）：severity/level 是中文级别标签，code 是 SHACL 组件名，
        // path 是按类限定的谓词 IRI。占位数据若不同构，会把错误的形状教给调用方。
        id: 'finding-1',
        severity: 'warning',
        level: '警告',
        code: 'MinCount',
        message: '属性 odap:ExpenseClaim.submittedBy 的出现次数 0 少于 sh:minCount=1',
        focus_node: 'odap:ExpenseClaim#test-3',
        path: 'odap:ExpenseClaim.submittedBy',
        blocking: false,
      },
      {
        id: 'finding-2',
        severity: 'info',
        level: '提示',
        code: 'Class',
        message: '取值 odap:Org-42 不是 sh:class=odap:Organization 的实例（信息级，不阻断发布）',
        blocking: false,
      },
    ],
  },
  'onto-procurement': {
    ...FIXTURE_ONTOLOGIES[1],
    classes: [
      {
        iri: 'odap:Contract',
        label: '采购合同',
        relations: [{ predicate: 'signedBy', target: 'odap:Supplier', label: '签约供应商' }],
      },
    ],
    findings: [],
  },
  'onto-hr': {
    ...FIXTURE_ONTOLOGIES[2],
    classes: [{ iri: 'odap:Employee', label: '员工', relations: [] }],
    findings: [
      {
        id: 'finding-3',
        severity: 'violation',
        level: '违规',
        code: 'Pattern',
        message: '取值 "E-12" 不匹配 sh:pattern="^EMP-[0-9]{4}$"',
        focus_node: 'odap:Employee#E-12',
        path: 'odap:Employee.employeeId',
        blocking: true,
      },
    ],
  },
};

/**
 * 模型供应商与端点 —— **无 fixture**。
 * 本页数据必须来自真实 API（`GET /api/v1/admin/model-providers`）。
 * USE_FIXTURES 模式下若该端点无 handler，会返回空对象 `{ providers: [], endpoints: [] }`，
 * 页面将显示"尚未配置模型供应商"空态——fail-closed，与后端返回空一致。
 */

export const FIXTURE_CONNECTORS: Connector[] = [
  {
    id: 'conn-erp-fin',
    name: '财务 ERP（生产）',
    type: 'erp-rest',
    endpoint: 'https://erp.example.com/api',
    auth_method: 'oauth2-client-credentials',
    credential_configured: true,
    read_only: false,
    resource_scope: ['ExpenseClaim', 'Payment', 'Supplier'],
    mapping_version: 'finance-mapping@2.1.0',
    health: 'healthy',
    last_sync_at: iso(5),
  },
  {
    id: 'conn-erp-scm',
    name: '采购 ERP',
    type: 'erp-rest',
    endpoint: 'https://scm.example.com/api',
    auth_method: 'mtls',
    credential_configured: true,
    read_only: true,
    resource_scope: ['Contract', 'Supplier'],
    mapping_version: 'procurement-mapping@0.9.0',
    health: 'degraded',
    last_sync_at: iso(240),
    last_error: '最近一次同步存在 2 条字段映射失败（类型转换）',
  },
  {
    id: 'conn-hr',
    name: 'HR 系统',
    type: 'sql',
    endpoint: 'jdbc:postgresql://hr-db:5432/hr',
    auth_method: 'username-password',
    credential_configured: true,
    read_only: true,
    resource_scope: ['Employee', 'Organization'],
    health: 'unknown',
  },
];

/**
 * 角色样例数据。
 * 字段口径 = contracts/openapi.yaml#/components/schemas/Role（**无 scope / permissions**）。
 * 首条与 `iam.roles` 注入后的真实库内容一致，可直接对照。
 */
export const FIXTURE_ROLES: Role[] = [
  {
    id: '44444444-4444-4444-8444-444444444444',
    role_key: 'finance-operator',
    name: '财务操作员',
    description: '演示角色：可创建/取消运行、审批、发布本体、查审计',
    is_system_role: true,
    created_at: iso(1440),
  },
  {
    id: 'role-ontology-designer',
    role_key: 'ontology-designer',
    name: '本体设计者',
    description: '维护本体、映射与验证',
    is_system_role: false,
    created_at: iso(2880),
  },
  {
    id: 'role-auditor',
    role_key: 'auditor',
    name: '审计员',
    description: '查看审计与证据，不可执行写操作',
    is_system_role: false,
    created_at: iso(4320),
  },
];

/**
 * 角色绑定样例数据。
 * 字段口径 = contracts/openapi.yaml#/components/schemas/RoleBinding：
 *   **扁平结构**（login_name / display_name 直接在顶层），范围是 scope_type，
 *   且**无 state 字段**（首条与真实库内容一致）。
 */
export const FIXTURE_ROLE_BINDINGS: RoleBinding[] = [
  {
    id: '66666666-6666-4666-8666-666666666666',
    role_key: 'finance-operator',
    role_name: '财务操作员',
    scope_type: 'TENANT',
    scope_org_id: null,
    login_name: 'demo.admin',
    display_name: '演示管理员',
    valid_from: iso(60),
    valid_until: null,
  },
  {
    id: 'rb-2',
    role_key: 'auditor',
    role_name: '审计员',
    scope_type: 'ORG_SUBTREE',
    scope_org_id: '33333333-3333-4333-8333-333333333333',
    login_name: 'ext.auditor',
    display_name: '外部审计顾问',
    valid_from: iso(20000),
    valid_until: iso(-60 * 24 * 10), // 已过期的授权窗口（不再展示派生"状态"，仅展示有效期）
  },
  {
    id: 'rb-3',
    role_key: 'ontology-designer',
    role_name: '本体设计者',
    scope_type: 'EXPLICIT',
    scope_org_id: null,
    login_name: 'demo.admin',
    display_name: '演示管理员',
    valid_from: iso(3000),
    valid_until: null,
  },
];

export const FIXTURE_POLICIES: Policy[] = [
  {
    id: 'finance.expense.submit',
    name: '报销单提交审批',
    version: '1.0.0',
    resource_type: 'ExpenseClaim',
    action: 'SUBMIT_FOR_APPROVAL',
    risk_level: 'medium',
    execution_mode: 'approval_required',
    state: 'published',
    updated_at: iso(2000),
  },
  {
    id: 'finance.payment.execute',
    name: '对外付款执行',
    version: '1.1.0',
    resource_type: 'Payment',
    action: 'CREATE_PAYMENT',
    risk_level: 'critical',
    execution_mode: 'dual_approval',
    state: 'published',
    updated_at: iso(900),
  },
  {
    id: 'procurement.contract.approve',
    name: '采购合同条款变更',
    version: '0.3.0',
    resource_type: 'Contract',
    action: 'UPDATE_TERMS',
    risk_level: 'high',
    execution_mode: 'approval_required',
    state: 'draft',
    updated_at: iso(120),
  },
  {
    id: 'hr.employee.read',
    name: '员工信息读取',
    version: '2.0.0',
    resource_type: 'Employee',
    action: 'READ',
    risk_level: 'low',
    execution_mode: 'auto',
    state: 'published',
    updated_at: iso(5000),
  },
];

/**
 * Agent 样例数据。
 * 字段口径 = contracts/openapi.yaml#/components/schemas/Agent，取值与
 * `datasets/finance-demo/agents.json` 注入后的真实库内容一致（可直接对照）。
 */
export const FIXTURE_AGENTS: AgentInfo[] = [
  {
    id: '99999999-9999-4999-8999-999999999999',
    agent_key: 'finance-expense-auditor',
    domain: 'FINANCE',
    description: '费用报销审核助手：读取报销单、校验制度合规性、给出审批建议（不直接写 ERP）',
    status: 'ACTIVE',
    is_builtin: false,
    latest_version_id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',
    latest_version: '0.1.0',
    latest_version_status: 'PUBLISHED',
  },
  // 平台内置智能体（is_builtin=true）：登记在册但不可修改，与 agents.json 一致。
  {
    id: 'b0000001-0000-4000-8000-000000000001',
    agent_key: 'ontology-generator',
    domain: 'PLATFORM',
    description: 'AI 自动生成企业本体：读取制度文档，产出结构化本体清单（类 / 关系 / 枚举 / SHACL 形状 / 样例实例），落库为 DRAFT。',
    status: 'ACTIVE',
    is_builtin: true,
    latest_version_id: 'e0000001-0000-4000-8000-000000000001',
    latest_version: '0.1.0',
    latest_version_status: 'PUBLISHED',
  },
  {
    id: 'b0000002-0000-4000-8000-000000000002',
    agent_key: 'agent-generator',
    domain: 'PLATFORM',
    description: 'AI 自动生成 Agent：依据本体与需求描述产出 Agent 定义、输入/输出 Schema 与首个草稿版本。',
    status: 'ACTIVE',
    is_builtin: true,
    latest_version_id: 'e0000002-0000-4000-8000-000000000002',
    latest_version: '0.1.0',
    latest_version_status: 'PUBLISHED',
  },
  {
    id: 'b0000003-0000-4000-8000-000000000003',
    agent_key: 'tool-generator',
    domain: 'PLATFORM',
    description: 'AI 自动生成工具：依据需求描述产出工具定义、参数 Schema 与效果等级（READ / EXTERNAL_WRITE / INTERNAL_WRITE）。',
    status: 'ACTIVE',
    is_builtin: true,
    latest_version_id: 'e0000003-0000-4000-8000-000000000003',
    latest_version: '0.1.0',
    latest_version_status: 'PUBLISHED',
  },
  {
    id: 'b0000004-0000-4000-8000-000000000004',
    agent_key: 'workflow-generator',
    domain: 'PLATFORM',
    description: 'AI 自动生成工作流：依据需求描述产出工作流节点、连线与执行顺序定义。',
    status: 'ACTIVE',
    is_builtin: true,
    latest_version_id: 'e0000004-0000-4000-8000-000000000004',
    latest_version: '0.1.0',
    latest_version_status: 'PUBLISHED',
  },
  {
    id: 'b0000005-0000-4000-8000-000000000005',
    agent_key: 'ontology-validator',
    domain: 'PLATFORM',
    description: '本体校验（SHACL）：对本体实例图执行形状校验，产出跨轮次稳定的发现项，作为发布门禁依据。',
    status: 'ACTIVE',
    is_builtin: true,
    latest_version_id: 'e0000005-0000-4000-8000-000000000005',
    latest_version: '0.1.0',
    latest_version_status: 'PUBLISHED',
  },
  {
    id: 'b0000006-0000-4000-8000-000000000006',
    agent_key: 'run-orchestrator',
    domain: 'PLATFORM',
    description: '运行编排（Harness）：驱动 Run 状态机推进、步骤调度与工具调用编排，终态与业务状态严格分离。',
    status: 'ACTIVE',
    is_builtin: true,
    latest_version_id: 'e0000006-0000-4000-8000-000000000006',
    latest_version: '0.1.0',
    latest_version_status: 'PUBLISHED',
  },
  {
    id: 'b0000007-0000-4000-8000-000000000007',
    agent_key: 'erp-reconciler',
    domain: 'PLATFORM',
    description: 'ERP 操作对账：对回执未知 / 失败的外部写操作执行对账，分流至人工复核，绝不静默当作成功。',
    status: 'ACTIVE',
    is_builtin: true,
    latest_version_id: 'e0000007-0000-4000-8000-000000000007',
    latest_version: '0.1.0',
    latest_version_status: 'PUBLISHED',
  },
  {
    id: 'b0000008-0000-4000-8000-000000000008',
    agent_key: 'llm-governance',
    domain: 'PLATFORM',
    description: '模型调用治理网关：统一限流、预算、缓存、路由与结构化输出校验，所有模型调用必须经此链路。',
    status: 'ACTIVE',
    is_builtin: true,
    latest_version_id: 'e0000008-0000-4000-8000-000000000008',
    latest_version: '0.1.0',
    latest_version_status: 'PUBLISHED',
  },
];

/**
 * 工具样例数据。
 * 字段口径 = contracts/openapi.yaml#/components/schemas/Tool；取值与
 * `datasets/finance-demo/tools.json` 注入后的真实库内容一致
 * （7 个用户工具 + 9 个平台内置工具，覆盖 3 类副作用）。
 */
export const FIXTURE_TOOLS: ToolInfo[] = [
  {
    id: 'dddddddd-dddd-4ddd-8ddd-dddddddddddd',
    tool_key: 'erp.expense.query',
    description: '查询 ERP 费用单据（只读）',
    status: 'ACTIVE',
    is_builtin: false,
    effect_class: 'READ',
    latest_version_id: 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee',
    latest_version: '0.1.0',
    latest_version_status: 'PUBLISHED',
  },
  {
    id: 'd0000002-0000-4000-8000-000000000002',
    tool_key: 'erp.vendor.lookup',
    description: '按纳税人识别号查询供应商主数据，用于核验发票抬头真伪',
    status: 'ACTIVE',
    is_builtin: false,
    effect_class: 'READ',
    latest_version_id: 'e0000002-0000-4000-8000-000000000002',
    latest_version: '0.1.0',
    latest_version_status: 'PUBLISHED',
  },
  {
    id: 'd0000003-0000-4000-8000-000000000003',
    tool_key: 'kb.policy.search',
    description: '在企业制度知识库中检索条款原文，为审批结论提供可引用依据',
    status: 'ACTIVE',
    is_builtin: false,
    effect_class: 'READ',
    latest_version_id: 'e0000003-0000-4000-8000-000000000003',
    latest_version: '0.1.0',
    latest_version_status: 'PUBLISHED',
  },
  {
    id: 'd0000004-0000-4000-8000-000000000004',
    tool_key: 'doc.receipt.extract',
    description: '从票据影像中抽取结构化字段（发票号、金额、开票日期、销方）',
    status: 'ACTIVE',
    is_builtin: false,
    effect_class: 'READ',
    latest_version_id: 'e0000004-0000-4000-8000-000000000004',
    latest_version: '0.1.0',
    latest_version_status: 'PUBLISHED',
  },
  {
    id: 'd0000005-0000-4000-8000-000000000005',
    tool_key: 'budget.check',
    description: '查询成本中心预算余额与占用情况，判断本次报销是否超额',
    status: 'ACTIVE',
    is_builtin: false,
    effect_class: 'READ',
    latest_version_id: 'e0000005-0000-4000-8000-000000000005',
    latest_version: '0.1.0',
    latest_version_status: 'PUBLISHED',
  },
  {
    id: 'd0000006-0000-4000-8000-000000000006',
    tool_key: 'erp.expense.decide',
    description: '将审批结论回写 ERP（外部写入：需人工复核门禁后才可执行）',
    status: 'ACTIVE',
    is_builtin: false,
    effect_class: 'EXTERNAL_WRITE',
    latest_version_id: 'e0000006-0000-4000-8000-000000000006',
    latest_version: '0.1.0',
    latest_version_status: 'PUBLISHED',
  },
  {
    id: 'd0000007-0000-4000-8000-000000000007',
    tool_key: 'audit.ledger.append',
    description: '向内部审计台账追加一条审核留痕（内部写入，不触达外部系统）',
    status: 'ACTIVE',
    is_builtin: false,
    effect_class: 'INTERNAL_WRITE',
    latest_version_id: 'e0000007-0000-4000-8000-000000000007',
    latest_version: '0.1.0',
    latest_version_status: 'PUBLISHED',
  },
  // ---- 平台内置工具（is_builtin: true，登记展示、不可编辑/删除）----
  {
    id: 'd0000010-0000-4000-8000-000000000010',
    tool_key: 'doc.word.read',
    description: '读取 Word 文档（.docx / .doc）并抽取为纯文本 —— 平台内置：与 AI 自动生成功能读取制度文档使用同一条链路',
    status: 'ACTIVE',
    is_builtin: true,
    effect_class: 'READ',
    latest_version_id: 'e0000010-0000-4000-8000-000000000010',
    latest_version: '0.1.0',
    latest_version_status: 'PUBLISHED',
  },
  {
    id: 'd0000011-0000-4000-8000-000000000011',
    tool_key: 'doc.excel.read',
    description: '读取 Excel 工作簿（.xlsx）并抽取为纯文本表格（按工作表逐行展开）',
    status: 'ACTIVE',
    is_builtin: true,
    effect_class: 'READ',
    latest_version_id: 'e0000011-0000-4000-8000-000000000011',
    latest_version: '0.1.0',
    latest_version_status: 'PUBLISHED',
  },
  {
    id: 'd0000012-0000-4000-8000-000000000012',
    tool_key: 'doc.text.read',
    description: '读取纯文本 / Markdown / CSV / JSON 文档并抽取为纯文本 —— 平台内置的通用文本读取入口',
    status: 'ACTIVE',
    is_builtin: true,
    effect_class: 'READ',
    latest_version_id: 'e0000012-0000-4000-8000-000000000012',
    latest_version: '0.1.0',
    latest_version_status: 'PUBLISHED',
  },
  {
    id: 'd0000013-0000-4000-8000-000000000013',
    tool_key: 'ontology.shacl.validate',
    description: '对本体形状约束（SHACL）执行确定性校验，产出可跨轮次复现、可去重的 finding 摘要',
    status: 'ACTIVE',
    is_builtin: true,
    effect_class: 'READ',
    latest_version_id: 'e0000013-0000-4000-8000-000000000013',
    latest_version: '0.1.0',
    latest_version_status: 'PUBLISHED',
  },
  {
    id: 'd0000014-0000-4000-8000-000000000014',
    tool_key: 'ontology.graph.load',
    description: '装载本体清单（classes / relations / shapes）并执行闭包校验：关系必须指向真实存在的实例',
    status: 'ACTIVE',
    is_builtin: true,
    effect_class: 'READ',
    latest_version_id: 'e0000014-0000-4000-8000-000000000014',
    latest_version: '0.1.0',
    latest_version_status: 'PUBLISHED',
  },
  {
    id: 'd0000015-0000-4000-8000-000000000015',
    tool_key: 'llm.chat.complete',
    description: '经模型调用治理链（限流 / 预算 / 缓存 / 路由 / 结构化输出）调用大模型补全，结果写入用量账本',
    status: 'ACTIVE',
    is_builtin: true,
    effect_class: 'EXTERNAL_WRITE',
    latest_version_id: 'e0000015-0000-4000-8000-000000000015',
    latest_version: '0.1.0',
    latest_version_status: 'PUBLISHED',
  },
  {
    id: 'd0000016-0000-4000-8000-000000000016',
    tool_key: 'erp.nc65.operation',
    description: '通过用友 NC65 适配器执行 ERP 对象操作；未配置映射的对象或命令一律 fail-closed 返回 UNAVAILABLE',
    status: 'ACTIVE',
    is_builtin: true,
    effect_class: 'EXTERNAL_WRITE',
    latest_version_id: 'e0000016-0000-4000-8000-000000000016',
    latest_version: '0.1.0',
    latest_version_status: 'PUBLISHED',
  },
  {
    id: 'd0000017-0000-4000-8000-000000000017',
    tool_key: 'audit.chain.append',
    description: '向平台审计哈希链追加一条留痕（前序摘要入链），链式结构使篡改可被检测',
    status: 'ACTIVE',
    is_builtin: true,
    effect_class: 'INTERNAL_WRITE',
    latest_version_id: 'e0000017-0000-4000-8000-000000000017',
    latest_version: '0.1.0',
    latest_version_status: 'PUBLISHED',
  },
  {
    id: 'd0000018-0000-4000-8000-000000000018',
    tool_key: 'evidence.hash.anchor',
    description: '为运行证据包计算内容摘要并锚定，支持逐条校验与篡改检测',
    status: 'ACTIVE',
    is_builtin: true,
    effect_class: 'INTERNAL_WRITE',
    latest_version_id: 'e0000018-0000-4000-8000-000000000018',
    latest_version: '0.1.0',
    latest_version_status: 'PUBLISHED',
  },
];

/**
 * 工作流模板：与库中真实行一致（planning.plans.status='TEMPLATE' + 最新修订），
 * 来源 datasets/finance-demo/plans.json。夹具必须与契约/真实接口同源，
 * 否则会变成一台生产假阴性的机器（缺陷 #19/#24）。
 */
export const FIXTURE_PLAN_TEMPLATES: PlanTemplate[] = [
  {
    id: 'f0000000-0000-4000-8000-000000000003',
    name: '报销审核工作流（FINANCE）',
    domain: 'FINANCE',
    version: 1,
    state: 'VALIDATED',
    node_kinds: [
      'READ_OBJECT',
      'ONTOLOGY_VALIDATE',
      'RULE_EVALUATE',
      'APPROVAL',
      'ERP_OPERATION',
      'RECONCILE',
    ],
    ontology_ref: 'finance-core',
    revision_id: 'f0000000-0000-4000-8000-000000000004',
    updated_at: iso(3000),
  },
];

export const FIXTURE_DATA_SOURCES: DataSource[] = [
  { id: 'ds-fin-claim', name: '财务-报销单', connector_id: 'conn-erp-fin', mapping_version: 'finance-mapping@2.1.0', freshness: 'fresh', last_sync_at: iso(5) },
  { id: 'ds-scm-supplier', name: '采购-供应商主数据', connector_id: 'conn-erp-scm', mapping_version: 'procurement-mapping@0.9.0', freshness: 'stale', last_sync_at: iso(240), issues: ['2 条字段类型转换失败', '1 个来源字段为空'] },
  { id: 'ds-hr-org', name: '人力-组织架构', connector_id: 'conn-hr', freshness: 'unknown', issues: ['尚未运行首次同步'] },
];

export const FIXTURE_ALERTS: OpsAlert[] = [
  { id: 'alert-1', severity: 'CRITICAL', title: '审计哈希链校验失败（tenant:current）', source: 'audit-chain-verifier', state: 'open', created_at: iso(30) },
  { id: 'alert-2', severity: 'IMPORTANT', title: '采购 ERP 连接器健康度降级', source: 'connector-health', state: 'acknowledged', created_at: iso(240) },
  { id: 'alert-3', severity: 'NORMAL', title: '模型调用预算使用已达 68%', source: 'llm-gateway', state: 'open', created_at: iso(90) },
];

export const FIXTURE_AUDIT_EVENTS: AuditEvent[] = [
  {
    id: 'ae-1006',
    occurred_at: iso(12),
    actor_id: ACTOR_LI.subject_id,
    actor_type: 'HUMAN',
    actor_display_name: ACTOR_LI.display_name,
    action: 'approval.request',
    resource_type: 'Approval',
    resource_id: 'apv-1',
    outcome: 'REQUIRE_APPROVAL',
    decision_id: 'dec-3001',
    policy_version: 'finance.expense.submit@1.0.0',
    hash: 'a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90',
    prev_hash: '0000000000000000000000000000000000000000000000000000000000000000',
    request_id: 'req_456',
  },
  {
    id: 'ae-1005',
    occurred_at: iso(180),
    actor_id: ACTOR_LI.subject_id,
    actor_type: 'HUMAN',
    actor_display_name: ACTOR_LI.display_name,
    action: 'erp.operation.dispatch',
    resource_type: 'Payment',
    resource_id: 'payment-77',
    outcome: 'ALLOW',
    decision_id: 'dec-2999',
    policy_version: 'finance.payment.execute@1.1.0',
    hash: 'b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1',
    prev_hash: 'a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90',
    request_id: 'req_455',
  },
  {
    id: 'ae-1004',
    occurred_at: iso(210),
    actor_id: ACTOR_WANG.subject_id,
    actor_type: 'HUMAN',
    actor_display_name: ACTOR_WANG.display_name,
    action: 'approval.decision',
    resource_type: 'Approval',
    resource_id: 'apv-9',
    outcome: 'ALLOW',
    decision_id: 'dec-2998',
    policy_version: 'finance.payment.execute@1.1.0',
    hash: 'c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2',
    prev_hash: 'b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1',
    request_id: 'req_450',
  },
  {
    id: 'ae-1003',
    occurred_at: iso(430),
    actor_id: ACTOR_WANG.subject_id,
    actor_type: 'HUMAN',
    actor_display_name: ACTOR_WANG.display_name,
    action: 'approval.decision',
    resource_type: 'Approval',
    resource_id: 'apv-7',
    outcome: 'DENY',
    decision_id: 'dec-2990',
    policy_version: 'procurement.supplier.onboard@0.2.0',
    hash: 'd4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3',
    prev_hash: 'c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2',
    request_id: 'req_431',
  },
  {
    id: 'ae-1002',
    occurred_at: iso(2600),
    actor_id: ACTOR_ADMIN.subject_id,
    actor_type: 'HUMAN',
    actor_display_name: ACTOR_ADMIN.display_name,
    action: 'ontology.publish',
    resource_type: 'Ontology',
    resource_id: 'onto-finance',
    outcome: 'ALLOW',
    decision_id: 'dec-2980',
    policy_version: 'platform.ontology.publish@1.0.0',
    hash: 'e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4',
    prev_hash: 'd4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3',
    request_id: 'req_200',
  },
  {
    id: 'ae-1001',
    occurred_at: iso(3000),
    actor_id: 'u_unknown',
    actor_type: 'HUMAN',
    actor_display_name: '未知主体',
    action: 'graph.cross_tenant.read',
    resource_type: 'Graph',
    resource_id: 'tenant-other',
    outcome: 'DENY',
    decision_id: 'dec-2975',
    policy_version: 'platform.tenant.isolation@1.0.0',
    hash: 'f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5',
    prev_hash: 'e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4',
    request_id: 'req_101',
  },
];

export const FIXTURE_EVALUATIONS: EvaluationRun[] = [
  {
    id: 'eval-1',
    name: '费用审核 Agent 回归评估',
    target_ref: 'agent-expense@2.3.0',
    state: 'succeeded',
    quality_gate: 'passed',
    metrics: [
      { name: '准确率', value: 0.94, threshold: 0.9, passed: true },
      { name: '审批判定一致性', value: 0.97, threshold: 0.95, passed: true },
      { name: '平均耗时', value: 2.4, threshold: 5, passed: true },
    ],
    created_at: iso(600),
    finished_at: iso(590),
  },
  {
    id: 'eval-2',
    name: '合同审查 Agent 仿真',
    target_ref: 'agent-contract@1.1.0',
    state: 'succeeded',
    quality_gate: 'failed',
    metrics: [
      { name: '风险识别召回', value: 0.71, threshold: 0.85, passed: false },
      { name: '误报率', value: 0.18, threshold: 0.1, passed: false },
    ],
    created_at: iso(300),
    finished_at: iso(280),
  },
  {
    id: 'eval-3',
    name: '本体语义回归（finance 1.3.0 草稿）',
    target_ref: 'onto-finance@1.3.0',
    state: 'running',
    quality_gate: 'pending',
    created_at: iso(10),
  },
];

export const FIXTURE_RELEASES: Release[] = [
  {
    id: 'rel-1',
    artifact_type: 'agent',
    artifact_id: 'agent-expense',
    version: '2.3.0',
    state: 'promoted',
    gate_state: 'passed',
    environment: 'prod',
    promoted_at: iso(590),
  },
  {
    id: 'rel-2',
    artifact_type: 'agent',
    artifact_id: 'agent-contract',
    version: '1.1.0',
    state: 'candidate',
    gate_state: 'failed',
    environment: 'staging',
  },
  {
    id: 'rel-3',
    artifact_type: 'ontology',
    artifact_id: 'onto-finance',
    version: '1.3.0',
    state: 'draft',
    gate_state: 'pending',
  },
];

// 演示态夹具的口径与真实 `/apps` 端点一致：业务应用 = 企业本体。
export const FIXTURE_APPS: BusinessApp[] = [
  {
    id: '11111111-1111-4111-8111-111111111111',
    ontology_key: 'finance-expense',
    name: '费用报销本体',
    domain: 'finance.expense',
    description: '核对报销单、检查凭证与预算并提交审批',
    status: 'ACTIVE',
    latest_version: '2.3.0',
    latest_version_status: 'PUBLISHED',
  },
  {
    id: '22222222-2222-4222-8222-222222222222',
    ontology_key: 'procurement-contract',
    name: '采购合同本体',
    domain: 'procurement.contract',
    description: '审查采购合同条款风险与授权范围',
    status: 'ACTIVE',
    latest_version: '1.1.0',
    latest_version_status: 'PUBLISHED',
  },
  {
    id: '33333333-3333-4333-8333-333333333333',
    ontology_key: 'finance-payment',
    name: '付款执行本体',
    domain: 'finance.payment',
    description: '执行已授权付款并核验 ERP 回执',
    status: 'DRAFT',
    latest_version: '1.0.0',
    latest_version_status: 'CANDIDATE',
  },
  {
    id: '44444444-4444-4444-8444-444444444444',
    ontology_key: 'enterprise-knowledge',
    name: '企业知识本体',
    domain: 'knowledge.qa',
    description: '基于本体与企业文档的受控问答',
    status: 'ACTIVE',
    latest_version: null,
    latest_version_status: null,
  },
  {
    id: '55555555-5555-4555-8555-555555555555',
    ontology_key: 'business-analytics',
    name: '经营分析本体',
    domain: 'analytics.operations',
    description: '基于语义指标的经营分析',
    status: 'ARCHIVED',
    latest_version: '0.9.0',
    latest_version_status: 'PUBLISHED',
  },
];

/**
 * 演示态：本体驱动审查的 Run（`POST /runs` 响应）。
 *
 * 结构与真实服务端返回**一一对应**：`plan` 由服务端按本体结构生成，
 * `review_conclusion` 由服务端依据真实 SHACL 结果合成。夹具里保留
 * `conforms=false` 的情形，是为了让演示态也能看到「引擎判不合规时结论不能是
 * 通过」这条硬规则，而不是只展示绿灯路径。
 */
export function buildOntologyRunFixture(): Run {
  const now = iso(0);
  return {
    id: `run-${Math.floor(Math.random() * 9000) + 1000}`,
    objective: '检查报销单 claim-1001，核对凭证、费用类别与预算，并在符合制度时提交审批',
    status: 'MANUAL_REVIEW',
    business_type: 'expense_claim_review',
    subject_id: ACTOR_LI.subject_id,
    ontology_version: '1.2.0',
    progress: 50,
    created_at: now,
    updated_at: now,
    resource_refs: ['claim-1001'],
    plan: {
      source: 'ontology',
      classes: ['ExpenseClaim', 'Invoice', 'Budget'],
      shape_ids: ['ExpenseClaimShape'],
      focus_nodes: 1,
      steps: [
        { kind: 'ONTOLOGY_CONTEXT_BUILD', name: '装载本体上下文' },
        { kind: 'ONTOLOGY_FACT_EXTRACT', name: '抽取业务事实' },
        { kind: 'ONTOLOGY_GRAPH_LOAD', name: '装载业务事实数据图' },
        { kind: 'ONTOLOGY_SHACL_VALIDATE', name: '执行形状约束校验' },
        { kind: 'ONTOLOGY_RULE_REVIEW', name: '按规则复核' },
        { kind: 'ONTOLOGY_CONCLUSION', name: '合成审查结论' },
      ],
    },
    review_conclusion: {
      decision: 'NEED_INFO',
      conforms: false,
      model_decision: 'APPROVE',
      reasons: [
        '报销单缺少发票号（invoiceNo），无法确认真实性',
        'SHACL 引擎判定不合规：ExpenseClaimShape.invoiceNo 至少需要 1 个值（minCount）',
      ],
      rule_checks: [
        { shape_id: 'ExpenseClaimShape', outcome: 'VIOLATED', evidence: 'claim-1001 缺少 invoiceNo' },
        { shape_id: 'BudgetShape', outcome: 'SATISFIED', evidence: '费用类别 budget=差旅，剩余额度充足' },
      ],
      shape_findings: [
        {
          shape_id: 'ExpenseClaimShape',
          focus_node: 'https://odap.example/ont/finance-expense#inst/claim-1001',
          path: 'https://odap.example/ont/finance-expense#ExpenseClaim.invoiceNo',
          severity: 'VIOLATION',
          component: 'MinCountConstraintComponent',
          message: '属性 invoiceNo 的值个数少于要求的最小值 1',
          digest: 'a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90',
        },
      ],
      graph_iri: 'https://odap.example/ont/finance-expense/1.2.0',
      ontology: {
        id: '11111111-1111-4111-8111-111111111111',
        key: 'finance-expense',
        name: '费用报销本体',
        version: '1.2.0',
      },
      facts: [
        { class: 'ExpenseClaim', id: 'claim-1001' },
        { class: 'Budget', id: 'budget-travel-2026' },
      ],
    },
  };
}

export function buildWorkspaceSummary(): WorkspaceSummary {
  const visible = FIXTURE_RUNS;
  // 结构与 contracts/openapi.yaml 的 WorkspaceSummary 一一对应；最近任务不再由本端点提供。
  const byStatus: Record<string, number> = {};
  for (const run of visible) {
    byStatus[run.status] = (byStatus[run.status] ?? 0) + 1;
  }
  const terminal = ['COMPLETED', 'FAILED', 'CANCELLED', 'EXPIRED'];
  return {
    runs_by_status: byStatus,
    runs_total: visible.length,
    terminal_runs: visible.filter((r) => terminal.includes(r.status)).length,
    pending_approvals: 2,
    reconciliation_pending: visible.filter((r) => r.status === 'REQUIRES_RECONCILIATION').length,
    unknown_external_operations: visible.filter((r) => r.status === 'MANUAL_REVIEW').length,
    generated_at: new Date().toISOString(),
  };
}

export const FIXTURE_ADMIN_OVERVIEW: AdminOverview = {
  counts: {
    roles: 6,
    role_bindings: 24,
    policy_versions: 11,
    agents: 8,
    tools: 17,
    ontologies: 4,
    active_installations: 5,
    evaluation_runs: 12,
    audit_events: 2048,
  },
  platform_users: 37,
  generated_at: new Date().toISOString(),
  system_health: [
    { component: 'API', state: 'healthy' },
    { component: 'Worker', state: 'healthy' },
    { component: '数据库', state: 'healthy' },
    { component: '模型提供方', state: 'degraded', detail: '外部供应商 A 认证失败' },
    { component: '外部连接器', state: 'degraded', detail: '采购 ERP 映射失败 2 条' },
  ],
  business: {
    total_runs: 1284,
    success_rate: 0.912,
    failures: 37,
    pending_approvals: 2,
    pending_reconciliations: 1,
  },
  semantic: {
    ontology_validation_failures: 1,
    mapping_issues: 3,
    data_freshness: new Date(Date.now() - 12 * 60 * 1000).toISOString(),
    semantic_regressions: 1,
  },
  governance: {
    recent_policy_changes: 2,
    failed_gates: 1,
    open_security_alerts: 1,
    pending_releases: 1,
  },
};

export const FIXTURE_TASKS: AsyncTask[] = [
  { id: 'task-1', state: 'running', progress: 62, created_at: iso(20), updated_at: iso(1) },
  {
    id: 'task-2',
    state: 'unknown',
    reconciliation_id: 'rec_9',
    created_at: iso(200),
    updated_at: iso(30),
    message: '外部操作结果尚未确认，等待对账',
  },
  { id: 'task-3', state: 'succeeded', progress: 100, created_at: iso(600), updated_at: iso(590) },
];

/* ============================ 路由 ============================ */

type Handler = (req: FixtureRequest) => unknown;

interface RouteDef {
  method: HttpMethod;
  pattern: RegExp;
  handler: Handler;
}

const byId = (path: string): string => decodeURIComponent(path.replace(/^\/+/, ''));

function runsHandler(req: FixtureRequest): unknown {
  const status = req.query?.status ? String(req.query.status) : undefined;
  const q = req.query?.q ? String(req.query.q).toLowerCase() : undefined;
  let items = FIXTURE_RUNS;
  if (status) items = items.filter((r) => r.status === status);
  if (q) {
    items = items.filter(
      (r) => r.id.toLowerCase().includes(q) || r.objective.toLowerCase().includes(q) || (r.resource_refs ?? []).some((x) => x.toLowerCase().includes(q)),
    );
  }
  return { items, next_cursor: null };
}

function approvalsHandler(req: FixtureRequest): unknown {
  const status = req.query?.status ? String(req.query.status) : undefined;
  const items = status ? FIXTURE_APPROVALS.filter((a) => a.status === status) : FIXTURE_APPROVALS;
  return { items, next_cursor: null };
}

const ROUTES: RouteDef[] = [
  { method: 'GET', pattern: /^\/workspace\/summary$/, handler: () => buildWorkspaceSummary() },
  // 工作台可用本体清单：与 `/ontologies` 同源（口径一致，管理端有多少这里就有多少）。
  { method: 'GET', pattern: /^\/workspace\/ontologies$/, handler: () => ({ items: FIXTURE_ONTOLOGIES, next_cursor: null }) },
  { method: 'GET', pattern: /^\/admin\/overview$/, handler: () => FIXTURE_ADMIN_OVERVIEW },
  { method: 'GET', pattern: /^\/runs$/, handler: runsHandler },
  {
    method: 'GET',
    pattern: /^\/runs\/([^/]+)$/,
    handler: (req) => {
      const m = /^\/runs\/([^/]+)$/.exec(req.path)!;
      const id = byId(m[1]);
      const run = FIXTURE_RUNS.find((r) => r.id === id);
      // 与真实端点同形：{ run, steps, dependencies }。返回裸 Run 会让页面读
      // `data.run.objective` 时拿到 undefined，标题永远是空的。
      if (!run) return null;
      return { run, steps: FIXTURE_RUN_STEPS[id] ?? [], dependencies: [] };
    },
  },
  {
    method: 'GET',
    pattern: /^\/runs\/([^/]+)\/steps$/,
    handler: (req) => {
      const m = /^\/runs\/([^/]+)\/steps$/.exec(req.path)!;
      return { items: FIXTURE_RUN_STEPS[byId(m[1])] ?? [], next_cursor: null };
    },
  },
  {
    method: 'GET',
    pattern: /^\/runs\/([^/]+)\/erp-operations$/,
    handler: (req) => {
      const m = /^\/runs\/([^/]+)\/erp-operations$/.exec(req.path)!;
      return { items: FIXTURE_ERP_OPERATIONS.filter((o) => o.run_id === byId(m[1])), next_cursor: null };
    },
  },
  {
    method: 'POST',
    pattern: /^\/runs$/,
    handler: () => buildOntologyRunFixture(),
  },
  { method: 'POST', pattern: /^\/runs\/([^/]+)\/cancel$/, handler: () => ({ accepted: true }) },
  { method: 'POST', pattern: /^\/runs\/([^/]+)\/reconcile$/, handler: () => ({ accepted: true, reconciliation_id: 'rec_9' }) },
  { method: 'GET', pattern: /^\/approvals$/, handler: approvalsHandler },
  {
    method: 'GET',
    pattern: /^\/approvals\/([^/]+)$/,
    handler: (req) => {
      const m = /^\/approvals\/([^/]+)$/.exec(req.path)!;
      const id = byId(m[1]);
      return FIXTURE_APPROVAL_DETAILS[id] ?? FIXTURE_APPROVALS.find((a) => a.id === id) ?? null;
    },
  },
  {
    method: 'POST',
    pattern: /^\/approvals\/([^/]+)\/decision$/,
    handler: (req) => {
      const m = /^\/approvals\/([^/]+)\/decision$/.exec(req.path)!;
      const body = (req.body ?? {}) as { decision?: string };
      // 注意：审批通过 ≠ ERP 操作成功（§4.4）。此处仅返回审批结论。
      return { approval_id: byId(m[1]), status: body.decision === 'APPROVE' ? 'APPROVED' : body.decision === 'REJECT' ? 'REJECTED' : 'RETURNED', erp_operation_status: 'PENDING' };
    },
  },
  { method: 'GET', pattern: /^\/ontologies$/, handler: () => ({ items: FIXTURE_ONTOLOGIES, next_cursor: null }) },
  {
    method: 'GET',
    pattern: /^\/ontologies\/([^/]+)$/,
    handler: (req) => {
      const m = /^\/ontologies\/([^/]+)$/.exec(req.path)!;
      const id = byId(m[1]);
      return FIXTURE_ONTOLOGY_DETAILS[id] ?? FIXTURE_ONTOLOGIES.find((o) => o.id === id) ?? null;
    },
  },
  { method: 'POST', pattern: /^\/ontologies\/([^/]+)\/validate$/, handler: () => ({ task_id: 'task-onto-1', state: 'running' }) },
  { method: 'POST', pattern: /^\/ontologies\/([^/]+)\/publish$/, handler: () => ({ task_id: 'task-onto-2', state: 'queued' }) },
  // 聚合对象（{providers, endpoints}），**不是** {items} 分页列表（缺陷 #22）。
  // POST 新建 / 验证两端点在后端与契约中均不存在，故样例层同样不提供——
  // 否则 fixture 模式会假装这两个能力可用（与"fixtures 不得偏离契约"同一条约束）。
  // GET /admin/model-providers 已移除 fixture handler —— 模型与推理页必须读真实后端；
  // USE_FIXTURES 模式下该端点返回空对象 { providers: [], endpoints: [] }。
  { method: 'GET', pattern: /^\/admin\/connectors$/, handler: () => ({ items: FIXTURE_CONNECTORS, next_cursor: null }) },
  { method: 'POST', pattern: /^\/admin\/connectors\/([^/]+)\/test$/, handler: () => ({ ok: true, detail: '预定义安全探测通过' }) },
  { method: 'GET', pattern: /^\/admin\/roles$/, handler: () => ({ items: FIXTURE_ROLES, next_cursor: null }) },
  { method: 'GET', pattern: /^\/admin\/role-bindings$/, handler: () => ({ items: FIXTURE_ROLE_BINDINGS, next_cursor: null }) },
  { method: 'POST', pattern: /^\/admin\/subjects\/([^/]+)\/disable$/, handler: () => ({ ok: true }) },
  { method: 'GET', pattern: /^\/policies$/, handler: () => ({ items: FIXTURE_POLICIES, next_cursor: null }) },
  { method: 'POST', pattern: /^\/policies\/([^/]+)\/versions$/, handler: () => ({ id: byId('x'), version: 'next', state: 'draft' }) },
  { method: 'GET', pattern: /^\/agents$/, handler: () => ({ items: FIXTURE_AGENTS, next_cursor: null }) },
  { method: 'POST', pattern: /^\/agents\/([^/]+)\/publish$/, handler: () => ({ task_id: 'task-agent-1', state: 'queued' }) },
  { method: 'GET', pattern: /^\/tools$/, handler: () => ({ items: FIXTURE_TOOLS, next_cursor: null }) },
  { method: 'GET', pattern: /^\/plan-templates$/, handler: () => ({ items: FIXTURE_PLAN_TEMPLATES, next_cursor: null }) },
  { method: 'GET', pattern: /^\/data-sources$/, handler: () => ({ items: FIXTURE_DATA_SOURCES, next_cursor: null }) },
  { method: 'GET', pattern: /^\/ops\/alerts$/, handler: () => ({ items: FIXTURE_ALERTS, next_cursor: null }) },
  { method: 'GET', pattern: /^\/audit-events$/, handler: () => ({ items: FIXTURE_AUDIT_EVENTS, next_cursor: null }) },
  { method: 'GET', pattern: /^\/evaluations$/, handler: () => ({ items: FIXTURE_EVALUATIONS, next_cursor: null }) },
  {
    method: 'GET',
    pattern: /^\/evaluations\/([^/]+)$/,
    handler: (req) => {
      const m = /^\/evaluations\/([^/]+)$/.exec(req.path)!;
      return FIXTURE_EVALUATIONS.find((e) => e.id === byId(m[1])) ?? null;
    },
  },
  { method: 'GET', pattern: /^\/releases$/, handler: () => ({ items: FIXTURE_RELEASES, next_cursor: null }) },
  { method: 'GET', pattern: /^\/apps$/, handler: () => ({ items: FIXTURE_APPS, next_cursor: null }) },
  {
    method: 'GET',
    pattern: /^\/tasks\/([^/]+)$/,
    handler: (req) => {
      const m = /^\/tasks\/([^/]+)$/.exec(req.path)!;
      return FIXTURE_TASKS.find((t) => t.id === byId(m[1])) ?? FIXTURE_TASKS[1];
    },
  },
  { method: 'GET', pattern: /^\/erp\/capabilities$/, handler: () => ({ items: [{ connector_id: 'conn-erp-fin', actions: ['READ', 'SUBMIT_FOR_APPROVAL', 'CREATE_PAYMENT'] }], next_cursor: null }) },
];

export function resolveFixture(req: FixtureRequest): FixtureResolution {
  for (const route of ROUTES) {
    if (route.method !== req.method) continue;
    if (!route.pattern.test(req.path)) continue;
    return { hit: true, data: route.handler(req) };
  }
  return { hit: false };
}

/** 仅用于类型对齐断言（保证样例结构符合领域契约） */
export type FixturePage<T> = CursorPage<T>;
