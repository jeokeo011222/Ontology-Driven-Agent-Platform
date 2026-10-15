/**
 * types/domain.ts —— ODAP 领域类型（前端投影）
 *
 * 状态词表唯一真源：docs/contracts/01-全局状态注册表.md
 *   本文件的状态联合类型必须与 contracts/01 逐项一致；新增状态须走 ADR 并同步本文件。
 * 约束（contracts/01 §5.1）：任务状态 ≠ 业务状态；Run 终态不得翻译为“业务已成功”。
 * 说明：契约生成版本位于 api/generated/types.ts（由 contracts/openapi.yaml 生成）。
 *   本文件承载前端投影所需的稳定领域模型，字段命名与契约保持一致（snake_case）。
 */

/* ============================ 状态词表（contracts/01 §1–§4） ============================ */

/** contracts/01 §1 —— agent_runs.status，共 17 个状态 */
export const RUN_STATUSES = [
  'CREATED',
  'CONTEXT_BUILDING',
  'READY',
  'PLANNING',
  'PLAN_VALIDATING',
  'RUNNING',
  'WAITING_INPUT',
  'WAITING_APPROVAL',
  'PAUSED',
  'RECOVERING',
  'CANCELLING',
  'REQUIRES_RECONCILIATION',
  'MANUAL_REVIEW',
  'COMPLETED',
  'FAILED',
  'CANCELLED',
  'EXPIRED',
] as const;
export type RunStatus = (typeof RUN_STATUSES)[number];

/** contracts/01 §1 —— 终态（COMPLETED 亦为终态，但不代表业务已成功） */
export const RUN_TERMINAL_STATUSES: readonly RunStatus[] = [
  'COMPLETED',
  'FAILED',
  'CANCELLED',
  'EXPIRED',
];

/** contracts/01 §2 —— run_steps.status */
export const STEP_STATUSES = [
  'PENDING',
  'READY',
  'RUNNING',
  'RETRY_WAIT',
  'WAITING_INPUT',
  'WAITING_APPROVAL',
  'SUCCEEDED',
  'FAILED',
  'CANCELLED',
  'SKIPPED',
  'UNKNOWN',
  'RECONCILING',
] as const;
export type StepStatus = (typeof STEP_STATUSES)[number];

/** contracts/01 §3 —— tool_calls.status，共 12 个 */
export const TOOL_CALL_STATUSES = [
  'PENDING',
  'VALIDATING',
  'REJECTED',
  'WAITING_APPROVAL',
  'READY',
  'DISPATCHED',
  'EXECUTING',
  'SUCCEEDED',
  'FAILED',
  'UNKNOWN',
  'RECONCILING',
  'MANUAL_REVIEW',
] as const;
export type ToolCallStatus = (typeof TOOL_CALL_STATUSES)[number];

/** contracts/01 §4 —— erp_operations.status，共 9 个 */
export const ERP_OPERATION_STATUSES = [
  'PENDING',
  'DISPATCHED',
  'EXECUTING',
  'SUCCEEDED',
  'FAILED',
  'UNKNOWN',
  'RECONCILING',
  'MANUAL_REVIEW',
  'COMPENSATED',
] as const;
export type ErpOperationStatus = (typeof ERP_OPERATION_STATUSES)[number];

/* ============================ 通用投影 ============================ */

export interface Actor {
  subject_id: string;
  display_name: string;
  org_path?: string;
}

export type RiskLevel = 'low' | 'medium' | 'high' | 'critical';

/* ============================ Run / Step ============================ */

export interface Run {
  id: string;
  /** 业务目标（本体驱动时为用户提交的自然语言目标；Agent 驱动时为空串） */
  objective: string;
  status: RunStatus;
  business_type?: string;
  subject_id?: string;
  /** 执行时锁定的本体版本号（contracts/openapi.yaml Run.ontology_version） */
  ontology_version?: string;
  /** Agent 驱动时的 Agent 版本 id；本体驱动时不出现 */
  agent_version_id?: string;
  /** 结构化执行计划（本体驱动时由服务端按本体真实结构生成，source=ontology） */
  plan?: OntologyReviewPlan;
  /** 本体审查结论（服务端确定性合成；SHACL 不合规不会被模型判断覆盖） */
  review_conclusion?: OntologyReviewConclusion;
  progress?: number;
  created_at: string;
  updated_at: string;
  /**
   * 当前等待原因（WAITING_INPUT / WAITING_APPROVAL / REQUIRES_RECONCILIATION 时给出）。
   *
   * 注意：服务端 `Run` 视图当前**不返回**该字段（runtime.agent_runs 无对应列）。
   * 保留声明是为了让既有页面的受保护渲染（`run.waiting_reason ? ... : null`）继续
   * 类型安全；等待态的真正解释由 `review_conclusion.reasons` 承载。
   */
  waiting_reason?: string;
  /** 计划模板版本（同上：服务端当前不返回，保留既有声明）。 */
  plan_template_version?: string;
  /** 关联的业务对象引用，如 claim-1001 */
  resource_refs?: string[];
}

/** contracts/openapi.yaml Run.review_conclusion —— 本体审查结论。 */
export interface OntologyReviewConclusion {
  decision: OntologyReviewDecision;
  /** SHACL 引擎的权威判定（false 时 decision 必为 NEED_INFO） */
  conforms: boolean;
  reasons: string[];
  /** 模型的原始判断，仅用于展示「模型怎么说」；不得据此判定合规 */
  model_decision?: string;
  rule_checks?: Array<{
    shape_id: string;
    outcome: 'SATISFIED' | 'VIOLATED' | 'INCONCLUSIVE';
    evidence?: string;
  }>;
  shape_findings?: Array<{
    shape_id?: string;
    focus_node?: string;
    path?: string;
    severity?: string;
    component?: string;
    message?: string;
    digest?: string;
  }>;
  graph_iri?: string;
  ontology?: { id?: string; key?: string; name?: string; version?: string; version_id?: string };
  facts?: Array<{ class: string; id: string; properties?: Record<string, unknown> }>;
}

/** 审查结论判定（contracts/openapi.yaml 无枚举约束，取值由服务端确定）。 */
export type OntologyReviewDecision = 'APPROVE' | 'REJECT' | 'NEED_INFO' | 'NEED_APPROVAL';

/** contracts/openapi.yaml Run.plan —— 本体驱动审查的结构化执行计划。 */
export interface OntologyReviewPlan {
  source: string;
  classes?: string[];
  shape_ids?: string[];
  focus_nodes?: number;
  steps?: Array<{ kind: string; name: string }>;
}

export interface RunStep {
  id: string;
  run_id: string;
  seq: number;
  name: string;
  kind: string;
  status: StepStatus;
  /** §4.6 判断依据：本体版本、规则命中、事实来源 */
  rationale?: string;
  ontology_version?: string;
  rule_refs?: string[];
  fact_refs?: string[];
  tool_id?: string;
  started_at?: string;
  finished_at?: string;
  error_code?: string;
  error_message?: string;
  erp_operation_id?: string;
}

/** contracts/01 §5.1：Run 与 ERP Operation 状态可合法地不同，UI 必须并列展示。 */
export interface ErpOperation {
  id: string;
  run_id?: string;
  status: ErpOperationStatus;
  action: string;
  resource_type: string;
  resource_id: string;
  idempotency_key_ref?: string;
  dispatched_at?: string;
  updated_at: string;
  /** ERP 回执（可能为空，UNKNOWN 时无回执） */
  receipt?: {
    receipt_id: string;
    received_at: string;
    summary?: string;
  };
  reconciliation_id?: string;
}

/* ============================ 任务（异步长任务 §10.6.3） ============================ */

export type AsyncTaskState = 'queued' | 'running' | 'succeeded' | 'failed' | 'unknown';

export interface AsyncTask<T = unknown> {
  id: string;
  state: AsyncTaskState;
  /** 进度百分比 0-100，NORMAL 级事件可丢，仅作展示 */
  progress?: number;
  /** 结果未知时的对账/确认入口 */
  reconciliation_id?: string;
  result?: T;
  error_code?: string;
  message?: string;
  created_at: string;
  updated_at: string;
}

/* ============================ 审批 ============================ */

export type ApprovalStatus =
  | 'PENDING'
  | 'APPROVED'
  | 'REJECTED'
  | 'RETURNED'
  | 'EXPIRED'
  | 'CANCELLED';

export interface ApprovalSummary {
  id: string;
  run_id: string;
  title: string;
  status: ApprovalStatus;
  risk_level: RiskLevel;
  resource_type: string;
  resource_id: string;
  requester: Actor;
  created_at: string;
  /** §4.4：审批状态与 ERP 执行状态必须分别呈现 */
  erp_operation_status?: ErpOperationStatus;
}

export interface ApprovalDetail extends ApprovalSummary {
  /** 本次具体操作与预期业务影响 */
  operation: {
    action: string;
    description: string;
    expected_impact: string;
    reversible: boolean;
  };
  /** 关键输入参数及其版本 */
  parameters: Record<string, string | number | boolean | null>;
  parameter_version: string;
  /** 参数摘要的不可变版本标识（§9.3 审批摘要绑定） */
  payload_digest: { algo: string; value: string; bound_at: string };
  /** Agent 建议与结构化理由 */
  agent_recommendation?: { decision: string; rationale: string };
  /** 适用规则、事实来源与异常项 */
  rules: { rule_id: string; version: string; outcome: string; evidence_ref?: string }[];
  exceptions: string[];
  /** 授权范围与审批链 */
  authorization_scope: string[];
  approval_chain: { step: number; approver: Actor; state: string }[];
  required_approvals: number;
  granted_approvals: number;
}

export interface ApprovalDecisionInput {
  decision: 'APPROVE' | 'REJECT' | 'RETURN';
  comment?: string;
  /** 审批通过授权执行的范围说明（用于二次确认展示） */
  confirm_scope?: string;
}

/* ============================ 本体（Ontology） ============================ */

/**
 * 版本生命周期状态 —— **本体版本**子集。
 *
 * 权威真源：migrations/platform/000004_create_ontology.sql
 *   ontology.ontology_versions.status CHECK IN (
 *     'DRAFT','VALIDATING','VALID','REJECTED','APPROVED','PUBLISHED','DEPRECATED','DISABLED')
 *
 * ⚠️ 平台词表缺口（自查矩阵缺陷 #19 关联项）：
 *   docs/contracts/01-全局状态注册表.md 只收录 Run/Step/ToolCall/ERP 四个词表，
 *   **未收录版本生命周期**；该词表实际只散落在各迁移的 CHECK 约束中，且各资源
 *   子集互不一致（见 REGISTRY_VERSION_STATUSES 注释）。
 *   第十七篇 §6.3 的 6 个中文状态是设计示意，与实现枚举并不一一对应。
 *
 * 历史教训（缺陷 #19 根因）：本文件此前定义的是 6 个**小写**自造别名
 *   （draft/validating/validated/pending_publish/published/deprecated）并挂在
 *   `OntologySummary.state` 上；后端既不返回 `state` 也不使用这套取值——
 *   前端词表与后端枚举完全错位，列表页直接抛 TypeError 白屏。
 */
export const ONTOLOGY_VERSION_STATUSES = [
  'DRAFT',
  'VALIDATING',
  'VALID',
  'REJECTED',
  'APPROVED',
  'PUBLISHED',
  'DEPRECATED',
  'DISABLED',
] as const;
export type OntologyVersionStatus = (typeof ONTOLOGY_VERSION_STATUSES)[number];

/**
 * 版本生命周期状态 —— **Agent / Tool 版本**子集。
 *
 * 权威真源：migrations/platform/000003_create_registry.sql
 *   registry.agent_versions.status 与 registry.tool_versions.status CHECK IN (
 *     'DRAFT','VALIDATING','APPROVED','PUBLISHED','DEPRECATED','DISABLED')
 * 注意：比本体版本**少 `VALID` 与 `REJECTED`** —— 同一平台内版本词表并不统一。
 */
export const REGISTRY_VERSION_STATUSES = [
  'DRAFT',
  'VALIDATING',
  'APPROVED',
  'PUBLISHED',
  'DEPRECATED',
  'DISABLED',
] as const;
export type RegistryVersionStatus = (typeof REGISTRY_VERSION_STATUSES)[number];

/**
 * 本体列表项。
 *
 * 权威：contracts/openapi.yaml#/components/schemas/Ontology（字段逐一对齐）；
 *   对应 SQL：internal/api/store/ontology_policy.go 的 ListOntologies。
 * 注意：**不存在** state/version/based_on/updated_at/owner 字段。
 */
export interface OntologySummary {
  id: string;
  ontology_key: string;
  name: string;
  domain: string;
  /** 粒度，如 DOMAIN */
  granularity: string;
  description: string;
  /** 本体级状态（ontology.ontologies.status，当前实现写入 ACTIVE） */
  status: string;
  /** 最新版本主键；无版本时为 null */
  latest_version_id: string | null;
  /** 最新版本号；无版本时为 null */
  latest_version: string | null;
  /** 最新版本状态；无版本时为 null */
  latest_version_status: OntologyVersionStatus | null;
}

export interface OntologyClass {
  iri: string;
  label: string;
  description?: string;
  relations: { predicate: string; target: string; label: string }[];
}

export interface ShaclFinding {
  id: string;
  severity: 'info' | 'warning' | 'violation';
  /** 中文级别标签（提示 / 警告 / 违规） */
  level: string;
  /** 稳定的机器可读代码：SHACL 组件名（MinCount / Pattern / Datatype …）。
   *  不能靠 message 做分组与统计——文案一改统计立刻崩。 */
  code: string;
  message: string;
  /** 违规实例的 IRI；针对整个类的约束时该字段**不存在**（不是空串）。 */
  focus_node?: string;
  /** 违规属性的谓词 IRI；无具体路径时该字段**不存在**。 */
  path?: string;
  /** 约束级别决定是否阻断发布（§6.2）；severity=violation 时为 true。 */
  blocking: boolean;
  /** 发现项的跨轮次稳定身份（SHA-256 hex）。id 是随机 UUID、validation_run_id 每轮
   *  都变，唯有该摘要在轮次之间稳定，供"上轮 3 条、本轮 2 条"的回归对比使用。
   *  迁移前写入的旧行无形状身份、无法复算，摘要为空时该字段**不存在**。 */
  finding_digest?: string;
}

/**
 * 本体详情。
 *
 * 权威：`GET /api/v1/ontologies/{id}`（已登记于 contracts/openapi.yaml 并在
 *   internal/api/router.go 注册）。历史缺陷 #20「详情端点缺失」已修复，
 *   详情页对此给出的降级说明随之作废。
 *
 * `findings` 是最近一次校验发现项（`ontology.validation_findings` 的投影），
 *   后端保证**恒为数组**——无违规时为空数组而非缺失字段，页面可直接 `.filter()`。
 */
export interface OntologyDetail extends OntologySummary {
  classes: OntologyClass[];
  findings: ShaclFinding[];
  release_notes?: string;
}

/**
 * `POST /api/v1/ontologies/{id}/validate` 的**同步**响应。
 *
 * ⚠️ 该端点不返回 202 + 任务引用：校验在一次请求内完成
 *   （装载 → 反假绿探针 → SHACL → 落库 → 状态流转），结论直接在本对象里。
 *   把返回类型声明为 `AsyncTask` 会让页面去轮询一个不存在的任务号。
 *
 * 权威：contracts/openapi.yaml#/components/schemas/OntologyValidationReport
 */
export interface OntologyValidationReport {
  ontology_id: string;
  version_id: string;
  version?: string;
  /** 版本在状态机中的位置，决定「发布」按钮是否可用。注意与 `run_status` 区分。 */
  version_status: 'DRAFT' | 'VALIDATING' | 'VALID' | 'REJECTED';
  /** SHACL 结论。为 false 时 findings 中必有至少一条 blocking=true。 */
  conforms: boolean;
  validation_run_id?: string;
  /** 本次校验运行的结论（PASSED/FAILED），与 `version_status` 是两个维度的信息。 */
  run_status: 'PASSED' | 'FAILED';
  validator_version?: string;
  findings: ShaclFinding[];
  graph?: {
    graph_iri: string;
    target_classes: string[];
    /** 命中 sh:targetClass 的实例数；为 0 时端点返回 422（反假绿探针）。 */
    focus_nodes: number;
    tbox_triples: number;
    abox_triples: number;
  };
  /** 各检查项结论。null 表示该项**未执行**（不是"执行了且不通过"）。 */
  checks?: {
    syntax_valid: boolean;
    shacl_conformant: boolean;
    owl_consistent: boolean | null;
    business_rules_valid: boolean | null;
  };
}

/** 一份产物文本及其媒体类型（`GET /api/v1/ontologies/{id}/artifacts`）。 */
export interface OntologyArtifactText {
  /** 媒体类型：Turtle 为 `text/turtle`，OWL 为 `application/rdf+xml`。 */
  format: string;
  content: string;
}

/**
 * 图上的节点类型（契约枚举，界面据此选样式）：
 *   - class：本体里的类；
 *   - instance：样例实例；
 *   - shape：SHACL 形状；
 *   - constraint：形状下的属性形状（约束项）。
 */
export type OntologyGraphNodeKind = 'class' | 'instance' | 'shape' | 'constraint';

/**
 * 图上的边类型：
 *   - relation：对象属性（类与类之间，或实例与实例之间）；
 *   - type：实例到其类（rdf:type）；
 *   - target：形状到目标类（sh:targetClass）；
 *   - property：形状到约束项（sh:property）。
 */
export type OntologyGraphEdgeKind = 'relation' | 'type' | 'target' | 'property';

/**
 * 图上的一个节点。`id` 是词项 IRI，与源码页签里的写法一致，可逐条对照。
 *
 * 权威：contracts/openapi.yaml#/components/schemas/OntologyGraphNode
 */
export interface OntologyGraphNode {
  id: string;
  kind: OntologyGraphNodeKind;
  /** 节点标题：优先中文标签，缺失时回落词项局部名。 */
  label: string;
  /** 词项局部名；与 label 相同时服务端省略该字段。 */
  subtitle?: string;
  /** 节点内的要点行（类的属性、实例的属性取值、约束项…），顺序稳定。 */
  details?: string[];
}

/**
 * 图上的一条边。
 *
 * 权威：contracts/openapi.yaml#/components/schemas/OntologyGraphEdge
 */
export interface OntologyGraphEdge {
  from: string;
  to: string;
  kind: OntologyGraphEdgeKind;
  /** 边上的谓词简称，如 `claimType` / `sh:targetClass`。 */
  label: string;
}

/** 一张自包含的图：节点与边。边两端的 ID 必然出现在 nodes 里。 */
export interface OntologyGraph {
  nodes: OntologyGraphNode[];
  edges: OntologyGraphEdge[];
}

/**
 * `GET /api/v1/ontologies/{id}/artifacts` 的响应：本体最新版本的三份产物文本与两张图。
 *
 * 由 manifest 派生，**不要求先跑校验**，因此 DRAFT 版本同样可见。三份文本与
 * 校验时实际装载进命名图、实际求值的形状逐条一致（服务端复用同一份投影实现）——
 * 图也来自同一份投影，因此图上的每个节点、每条边都能在源码页签里找到对应语句。
 *
 * 权威：contracts/openapi.yaml#/components/schemas/OntologyArtifacts
 */
export interface OntologyArtifacts {
  ontology_id: string;
  ontology_key: string;
  version_id: string;
  version: string;
  /** 由 (ontology_key, version_id) 确定性派生的数据命名图 IRI。 */
  graph_iri: string;
  rdf: OntologyArtifactText;
  owl: OntologyArtifactText;
  shacl: OntologyArtifactText;
  /**
   * 供界面画图的结构化节点/边。
   *
   * `ontology` 与 `ontology_tbox_only` 是同一份投影的两种口径（后者只含类与类之间的关系），
   * 服务端**两份都返回**是刻意的：界面上的「显示实例」开关只需在两者间切换，不必再发请求，
   * 前端也就不需要为"图上该有什么"另写一份过滤逻辑。
   */
  graph: {
    ontology: OntologyGraph;
    ontology_tbox_only: OntologyGraph;
    shapes: OntologyGraph;
  };
  /**
   * 规模信息，全部取自 manifest。
   *
   * **没有**焦点节点计数：那是"图里实际有多少实例"的运行时属性，图未装载时会得到 0
   * 并被误读为"没有实例"，因此服务端不给这个数字。
   */
  counts: {
    tbox_triples: number;
    abox_triples: number;
    shapes: number;
    target_classes: number;
    instances: number;
  };
}

/**
 * `POST /api/v1/ontologies/{id}/publish` 的**同步**响应。
 *
 * 权威：contracts/openapi.yaml 该端点 200 响应；实现见
 *   `Store.PublishOntology`（ontology_policy.go）。
 *
 * 发布门禁只放行 status ∈ {VALID, APPROVED} 的版本；其余返回 409，
 * 前端必须把 409 展示为"尚未通过校验"，而不是泛化失败。
 */
export interface OntologyPublishResult {
  ontology_id: string;
  version_id: string;
  environment: string;
  status: 'PUBLISHED';
}

/* ============================ 平台设置 ============================ */

/**
 * 模型供应商（平台级，无租户维度）。
 * 权威：contracts/openapi.yaml#/components/schemas/ModelProvider
 *   对应实现：`llm.providers`。
 *
 * ⚠️ 历史缺陷 #22（与 #19 同类）：此前声明为
 *   `{ id, name, protocol, endpoint, model_id, state, credential_configured, … }`，
 *   而实现返回 `{ provider_id, display_name, provider_type, status, created_at }`
 *   —— **端口/模型/凭据都不在供应商这一层**（它们分别在 `llm.endpoints` 与
 *   `llm.model_revisions`），且既有 `status`（ACTIVE…）而非 `state`（unconfigured…）。
 */
export interface ModelProviderSummary {
  provider_id: string;
  display_name: string;
  /** 供应商类型；当前仅 OPENAI_COMPATIBLE（MOCK 部署形态已下线） */
  provider_type: string;
  status: string;
  created_at: string;
}

/**
 * 模型端点（供应商的部署实例）。
 * 权威：contracts/openapi.yaml#/components/schemas/ModelEndpoint
 *   对应实现：`llm.endpoints`（含 000019 的 `base_url`、000029 的密文凭据列）。
 *
 * 凭据安全：后端**绝不返回密钥明文**，也不返回 `credential_ref` 引用名原文；
 *   `credential_masked` 是打码值（如 sk-****6e22），`credential_source` 说明来源枚举。
 */
export interface ModelEndpointSummary {
  endpoint_id: string;
  provider_id: string;
  endpoint_name: string;
  /** 部署形态；当前仅 HTTP（MOCK 部署形态已下线） */
  deployment_type: string;
  region_code: string | null;
  base_url: string | null;
  status: string;
  /** 打码后的凭据（如 sk-****6e22）；无凭据为空串 */
  credential_masked: string;
  /** 凭据来源：CIPHERTEXT（界面录入加密）/ ENV_REF（环境变量引用）/ NONE */
  credential_source: 'CIPHERTEXT' | 'ENV_REF' | 'NONE';
  has_credential: boolean;
  /** 已登记的模型 ID（逗号分隔）；为空表示"尚无模型版本，不参与路由" */
  model_ids: string;
  created_at: string;
}

/**
 * `GET /api/v1/admin/model-providers` 的响应。
 *
 * ⚠️ 缺陷 #22 的直接原因：该端点是**聚合对象**（两个并行清单），
 *   **不是** cursor 分页列表。前端此前用 `fetchList<ModelProvider>()`（即解析
 *   `{items, next_cursor}`）去读它 → `items` 恒为 undefined → 页面在
 *   已有 2 个供应商的情况下显示"尚未配置模型提供方"（**静默假空**）。
 */
export interface ModelProvidersResponse {
  providers: ModelProviderSummary[];
  endpoints: ModelEndpointSummary[];
}

export interface Connector {
  id: string;
  name: string;
  type: string;
  endpoint: string;
  auth_method: string;
  credential_configured: boolean;
  read_only: boolean;
  resource_scope: string[];
  mapping_version?: string;
  health: 'healthy' | 'degraded' | 'down' | 'unknown';
  last_sync_at?: string;
  last_error?: string;
}

/**
 * 角色。权威：contracts/openapi.yaml#/components/schemas/Role
 *
 * ⚠️ 历史缺陷 #21（与 #19 同类）：此前声明为
 *   `{ id, name, description?, scope: 'platform'|'tenant'|…, permissions: string[] }`，
 *   而契约与实现**都没有** `scope` 与 `permissions` 字段
 *   （角色↔权限的映射在服务端授权层，未在列表接口投影）。
 *   按不存在字段渲染会得到空白；`SCOPE_LABEL[item.scope]` 取到 undefined 即白屏。
 */
export interface Role {
  id: string;
  role_key: string;
  name: string;
  description: string;
  is_system_role: boolean;
  created_at: string;
}

/**
 * 角色绑定的**范围类型**。
 * 权威：contracts/openapi.yaml#/components/schemas/RoleBinding.scope_type 枚举
 */
export const ROLE_SCOPE_TYPES = [
  'TENANT',
  'ORG',
  'ORG_SUBTREE',
  'SELF',
  'ASSIGNED',
  'EXPLICIT',
] as const;
export type RoleScopeType = (typeof ROLE_SCOPE_TYPES)[number];

/**
 * 角色绑定。权威：contracts/openapi.yaml#/components/schemas/RoleBinding
 *
 * ⚠️ 历史缺陷 #21：此前声明为 `{ id, subject: Actor, role_id, scope, state, … }`，
 *   而契约与实现返回的是**扁平结构**：主体信息直接是 `login_name`/`display_name`，
 *   角色是 `role_key`/`role_name`，范围是 `scope_type`，且**无 `state` 字段**。
 *   页面 `item.subject.display_name` 即抛
 *   "Cannot read properties of undefined (reading 'display_name')" → 整页白屏。
 */
export interface RoleBinding {
  id: string;
  role_key: string;
  role_name: string;
  scope_type: RoleScopeType;
  scope_org_id: string | null;
  login_name: string;
  display_name: string;
  valid_from: string;
  valid_until: string | null;
}

export type PolicyExecutionMode =
  | 'auto'
  | 'user_confirm'
  | 'approval_required'
  | 'dual_approval'
  | 'forbidden';

export interface Policy {
  id: string;
  name: string;
  version: string;
  resource_type: string;
  action: string;
  risk_level: RiskLevel;
  execution_mode: PolicyExecutionMode;
  state: 'draft' | 'validated' | 'published' | 'deprecated';
  updated_at: string;
}

/* ============================ Agent / Tool / Workflow ============================ */

/**
 * Agent 列表项。
 *
 * 权威：contracts/openapi.yaml#/components/schemas/Agent（字段逐一对齐）；
 *   对应 SQL：internal/api/store/catalog.go 的 ListAgents。
 *
 * ⚠️ 设计-实现差异（自查矩阵缺陷 #19 关联）：§7.1 要求列表展示"业务职责 /
 *   模型策略 / 可用工具 / 关联本体与工作流 / 最近评估"，但实现的
 *   `GET /api/v1/agents` **不返回**这些字段（它们存在于 agent_versions 的
 *   dependency_manifest / prompt_template 中，尚未在列表接口投影）。
 *   故此处不再声明这些字段——按不存在字段渲染只会得到空白，或在 `.map` 处崩溃。
 *   待接口补齐投影后再恢复。
 */
export interface AgentInfo {
  id: string;
  agent_key: string;
  domain: string;
  description: string;
  /** Agent 级状态（registry.agents.status） */
  status: string;
  /**
   * 是否为平台内置智能体。
   *
   * 内置项（AI 自动生成本体 / Agent / 工具 / 工作流、本体校验、运行编排、
   * ERP 对账、模型调用治理）由平台随数据集登记，**不可修改**：前端应隐藏
   * 编辑/发布/删除入口，后端对 PATCH/DELETE/publish 一律返回 409。
   */
  is_builtin: boolean;
  latest_version_id: string | null;
  latest_version: string | null;
  latest_version_status: RegistryVersionStatus | null;
}

/**
 * 工具列表项。
 *
 * 权威：contracts/openapi.yaml#/components/schemas/Tool（字段逐一对齐）；
 *   对应 SQL：internal/api/store/catalog.go 的 ListTools。
 *
 * 说明：设计的 `risk_level` / `permission_required` / `idempotent` /
 *   `timeout_ms` 在列表接口中不返回；而实现提供了 `effect_class`
 *   （READ / INTERNAL_WRITE / EXTERNAL_WRITE，来自 DB CHECK 约束）——
 *   这是**真实可用的副作用等级**，作为"能力边界与风险"的权威投影呈现
 *   （优于按不存在的 risk_level 渲染）。
 */
export interface ToolInfo {
  id: string;
  tool_key: string;
  description: string;
  /** 工具级状态（registry.tools.status） */
  status: string;
  /**
   * 是否为平台内置工具。
   *
   * 内置项（文档读取 / 本体校验 / 图谱装载 / 模型调用治理网关 / ERP 适配 /
   * 审计链 / 证据哈希链）由平台随数据集登记，**不可修改**：前端应隐藏
   * 编辑/删除入口，后端对 PATCH/DELETE 一律返回 409。
   */
  is_builtin: boolean;
  /** 副作用等级（registry.tool_versions.effect_class） */
  effect_class: EffectClass | null;
  latest_version_id: string | null;
  latest_version: string | null;
  latest_version_status: RegistryVersionStatus | null;
}

/**
 * 工具副作用等级。
 * 权威：migrations/platform/000003_create_registry.sql
 *   registry.tool_versions.effect_class CHECK IN ('READ','EXTERNAL_WRITE','INTERNAL_WRITE')
 */
export const EFFECT_CLASSES = ['READ', 'INTERNAL_WRITE', 'EXTERNAL_WRITE'] as const;
export type EffectClass = (typeof EFFECT_CLASSES)[number];

/**
 * 插件式工作流资产（registry.workflows + registry.workflow_versions）。
 *
 * 字段口径：`GET /api/v1/workflows` 的真实返回（internal/api/store/workflow_store.go
 * 的 ListWorkflows）。与 Agent / Tool 完全同构：定义表 + 不可变版本表，
 * workflow 本身以 plugin_kind='WORKFLOW' 登记为插件资产（§7.3 插件式设计）。
 */
export interface WorkflowInfo {
  id: string;
  workflow_key: string;
  name: string;
  description: string;
  /** 工作流级状态（registry.workflows.status） */
  status: string;
  /**
   * 是否为平台内置工作流。
   *
   * 内置项（Harness 运行编排 / 在途操作对账 / 本体版本生命周期等）由平台随
   * 数据集登记，**不可修改**：前端应隐藏编辑/发布/删除入口，后端对
   * PATCH/DELETE/publish 一律返回 409。
   */
  is_builtin: boolean;
  latest_version_id: string | null;
  latest_version: string | null;
  latest_version_status: RegistryVersionStatus | null;
}

/** 工作流模板生命周期（契约 PlanTemplateState）。 */
export const PLAN_TEMPLATE_STATES = ['DRAFT', 'VALIDATED', 'REJECTED'] as const;
export type PlanTemplateState = (typeof PLAN_TEMPLATE_STATES)[number];

/**
 * 工作流模板资产（契约 PlanTemplate）。
 *
 * 模板与执行计划**共用** planning.plans / planning.plan_revisions，以
 * `status = 'TEMPLATE'` 为判别值区分"可复用模板"与"执行实例"。
 * name / ontology_ref / 步骤定义存放在 plan_ir（出自数据集 plans.json）。
 *
 * 注意：本类型此前是前端自造的（小写 state、`version: string`、可空 ontology_ref），
 * 与后端返回无一处对得上——同 #19/#21/#22 是同一个根因（缺陷 #24）。
 */
export interface PlanTemplate {
  id: string;
  name: string;
  domain: string;
  /** 最新修订号（模板的不可变版本） */
  version: number;
  state: PlanTemplateState;
  /** 由 plan_ir.steps[].kind 展开（第十七篇 §7.3 节点词表） */
  node_kinds: string[];
  /** 模板锁定的本体键 */
  ontology_ref: string | null;
  /** 最新修订 ID */
  revision_id: string;
  updated_at: string;
}

export interface DataSource {
  id: string;
  name: string;
  connector_id: string;
  mapping_version?: string;
  freshness: 'fresh' | 'stale' | 'unknown';
  last_sync_at?: string;
  issues?: string[];
}

/* ============================ 运行监控 / 审计 / 评估 / 发布 ============================ */

export interface OpsAlert {
  id: string;
  severity: 'CRITICAL' | 'IMPORTANT' | 'NORMAL';
  title: string;
  source: string;
  state: 'open' | 'acknowledged' | 'resolved';
  created_at: string;
}

/**
 * 审计事件（`GET /api/v1/audit-events` 的真实投影）。
 *
 * 字段口径修正：实现返回的是**扁平结构**，并非设计稿里的 `actor` 嵌套对象。
 * 事件表没有 `decision_id` / `policy_version` 列；哈希链来自
 * `event_digest`（投影为 `hash`）与 `prev_digest`（投影为 `prev_hash`）。
 * 主体展示名由服务端 LEFT JOIN `iam.users` 反查得到（`actor_display_name`）。
 * 早期前端按 `item.actor.display_name` 与 `item.hash.slice(...)` 渲染，
 * 字段不存在即抛 "Cannot read properties of undefined (reading 'display_name')"
 * 导致审计页整页白屏 —— 现按真实字段逐项渲染。
 */
export interface AuditEvent {
  id: string;
  occurred_at: string;
  event_type?: string;
  actor_type?: string;
  /** 主体 ID（文本列，可能为空或非 UUID）。 */
  actor_id?: string;
  /** 主体展示名：服务端反查 iam.users；查不到时为空串。 */
  actor_display_name?: string;
  actor_login_name?: string;
  action: string;
  resource_type: string;
  resource_id: string;
  /**
   * 事件结论。**自由文本**（契约 contracts/openapi.yaml 亦声明为 `type: string`）：
   * 审计事件表的 outcome 列没有 CHECK 约束，后端实际写入
   * SUCCESS / DENIED / REJECTED / ERROR 等取值。
   * 早期前端把它收窄成 `'ALLOW' | 'DENY' | 'REQUIRE_APPROVAL' | 'ERROR'`（设计稿的
   * 授权决策词表），与真实写入值不匹配，导致几乎每行都渲染为"未知状态"徽标。
   */
  outcome: string;
  request_id?: string;
  run_id?: string;
  segment_id?: string;
  seq_no?: number;
  /**
   * 决策 ID / 策略版本：**当前列表接口未投影**（审计事件表无对应列）。
   * 保留为可选字段以便页面以 "—" 显式展示缺口，不填充假数据。
   */
  decision_id?: string;
  policy_version?: string;
  /** §8.7 哈希链：prev_hash → 本事件 hash，用于完整性校验。 */
  hash: string;
  /** 段内首条事件的 prev_digest 可能为 NULL。 */
  prev_hash?: string | null;
}

export interface EvaluationRun {
  id: string;
  name: string;
  target_ref: string;
  state: AsyncTaskState;
  quality_gate: 'passed' | 'failed' | 'pending';
  metrics?: { name: string; value: number; threshold?: number; passed: boolean }[];
  created_at: string;
  finished_at?: string;
}

export interface Release {
  id: string;
  artifact_type: string;
  artifact_id: string;
  version: string;
  state: 'draft' | 'candidate' | 'promoted' | 'rolled_back';
  gate_state: 'passed' | 'failed' | 'pending';
  environment?: string;
  promoted_at?: string;
}

/* ============================ 工作台聚合（BFF） ============================ */

/**
 * `/api/v1/workspace/summary` 响应（**严格对齐 contracts/openapi.yaml 的 WorkspaceSummary**）。
 *
 * 审计修正：此前这里写的是 `pending_count / running_count / completed_count /
 * attention_count / period / recent_runs` —— 一套与契约**完全不符**的手写结构。
 * 由于 `apiGet<T>()` 是未校验的类型断言，编译期不会报错，运行时 metrics 全部取到
 * undefined、`recent_runs.length` 直接抛 TypeError 导致工作台白屏。
 * 契约是唯一真源（contracts/03 §4）：改前端对齐契约，而不是改契约迁就前端。
 *
 * "最近任务"不再由本端点提供（契约里没有该字段），改由 `/api/v1/runs?limit=5` 单独取。
 */
export interface WorkspaceSummary {
  runs_by_status: Record<string, number>;
  runs_total: number;
  /** 处于终态的 Run 数（含已完成/失败/取消/过期）——终态不等于业务操作成功 */
  terminal_runs: number;
  pending_approvals: number;
  reconciliation_pending: number;
  unknown_external_operations: number;
  generated_at?: string;
}

/**
 * `/api/v1/apps` 响应项（**严格对齐 contracts/openapi.yaml 的 App**）。
 *
 * 审计修正：此前这里是 `name / icon / org_scope / business_objects / task_types /
 * ontology_ref / agent_ref / plan_template_ref / allowed` —— 一组契约里不存在的字段。
 * 列表为空时页面"看起来正常"，一旦有数据 `app.org_scope.join()` 立刻抛异常。
 * 契约是唯一真源：改前端对齐契约。
 * 图标不再由服务端给字符串（原实现把 '¥' / '§' 这类字符直接渲染），
 * 改为按 `domain` 在前端图标集中选择，保证与全站图标同线宽同尺寸。
 */
/**
 * 业务应用视图（contracts/openapi.yaml `App`）。
 *
 * 口径 = **企业本体**：业务应用的实质是「一组业务对象 + 一组规则」，即一个本体。
 * 此前投影为 Agent 列表，会让「业务应用」页把 Agent 当成应用来展示——用户看到的
 * 是「有哪些智能体」，而不是「有哪些可用的业务规则」。
 */
export interface BusinessApp {
  id: string;
  ontology_key: string;
  name: string;
  domain: string;
  description?: string;
  status: string;
  latest_version?: string | null;
  latest_version_status?: string | null;
}

export interface AdminOverview {
  /** 平台资源计数（服务端聚合，按租户 RLS 过滤） */
  counts: {
    roles: number;
    role_bindings: number;
    policy_versions: number;
    agents: number;
    tools: number;
    ontologies: number;
    active_installations: number;
    evaluation_runs: number;
    audit_events: number;
  };
  /** 平台级用户数（iam.users 无 RLS，仅计数） */
  platform_users?: number;
  generated_at?: string;
  system_health: { component: string; state: 'healthy' | 'degraded' | 'down'; detail?: string }[];
  business: {
    total_runs: number;
    /** COMPLETED / 终态 Run；无终态 Run 时为 0 */
    success_rate: number;
    failures: number;
    pending_approvals: number;
    pending_reconciliations: number;
  };
  semantic: {
    ontology_validation_failures: number;
    /** 已持久化的映射问题数；数据映射未接入平台时为 null（渲染「未接入」，绝非 0） */
    mapping_issues: number | null;
    /** 最近一条审计事件时间（RFC3339）或"无数据" */
    data_freshness: string;
    semantic_regressions: number;
  };
  governance: {
    recent_policy_changes: number;
    failed_gates: number;
    open_security_alerts: number;
    pending_releases: number;
  };
}
