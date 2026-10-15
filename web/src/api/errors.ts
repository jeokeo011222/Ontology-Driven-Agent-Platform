/**
 * api/errors.ts —— 统一错误码映射与错误呈现
 *
 * 权威：docs/contracts/02-统一错误码注册表.md（12 基础码 + 域码）
 *
 * 关键安全语义（contracts/02 §1.3）：`EXECUTION_UNKNOWN` 是安全码，
 * 表示“平台未获得可信结果”，**永远不是失败**，驱动对账而非重试。
 * 因此 `EXECUTION_UNKNOWN` / `ERP_OPERATION_UNKNOWN` 的文案必须是
 * “结果待确认，走对账”，绝不可写成失败，更不可写成成功（§13.3 发布门禁）。
 */

/* ============================ 基础码（12 个，contracts/02 §2） ============================ */

export const BASE_CODES = [
  'INVALID_ARGUMENT',
  'UNAUTHENTICATED',
  'PERMISSION_DENIED',
  'NOT_FOUND',
  'CONFLICT',
  'VALIDATION_FAILED',
  'RATE_LIMITED',
  'UNAVAILABLE',
  'RESOURCE_EXHAUSTED',
  'DEADLINE_EXCEEDED',
  'EXECUTION_UNKNOWN',
  'INTERNAL',
] as const;
export type BaseCode = (typeof BASE_CODES)[number];

/** 未知/本地传输错误的内部基础码（不进入契约，仅前端内部使用） */
export const TRANSPORT_ERROR_CODE = 'TRANSPORT_ERROR';

/* ============================ 域码 → 基础码（contracts/02 §3） ============================ */

export interface DomainCodeSpec {
  base: BaseCode;
  title: string;
  guidance: string;
}

export const DOMAIN_CODES: Record<string, DomainCodeSpec> = {
  /* §3.1 通用/API 域 */

  /**
   * 本地合成域码（服务端**未返回**错误信封时由 client.ts 兜底生成）。
   *
   * HTTP 405 的语义是"路径存在、但该方法未注册"——这是**前后端接口不一致
   * （我方缺陷）**，与用户输入无关。修复前它落进 `default` 兜底被当成
   * `INVALID_ARGUMENT`，UI 于是显示"请求参数不合法 / 请修正输入后重新提交"，
   * **把我们的问题说成用户的问题**（与缺陷 #11/#20/#24 同一类误导）。
   * 也因此它不可重试：改输入、重试都不会成功，只有补齐端点才行。
   */
  METHOD_NOT_ALLOWED: {
    base: 'INVALID_ARGUMENT',
    title: '该操作不被接口支持',
    guidance:
      '接口未提供此操作（HTTP 405 方法不允许），属前后端接口不一致，与你的输入无关；修改输入或重试都不会成功，需由开发方补齐该端点。',
  },
  REQUEST_VALIDATION_FAILED: {
    base: 'INVALID_ARGUMENT',
    title: '请求格式错误',
    guidance: '请检查输入格式后重新提交；修正输入不需要重试。',
  },
  ONTOLOGY_VALIDATION_FAILED: {
    base: 'VALIDATION_FAILED',
    title: '本体校验未通过',
    guidance: '请查看 SHACL 验证发现项，修正结构、约束或数据后再发布。',
  },

  /* §3.2 授权域 */
  AUTHZ_PERMISSION_DENIED: {
    base: 'PERMISSION_DENIED',
    title: '权限不足',
    guidance: '当前身份无权执行该操作。可申请权限或联系管理员；该拒绝已记入审计。',
  },
  AUTHZ_POLICY_EVALUATION_ERROR: {
    base: 'INTERNAL',
    title: '策略求值失败',
    guidance: '策略求值器内部错误，默认拒绝（不会降级为允许）。请稍后重试或联系管理员。',
  },
  AUTHZ_APPROVAL_REQUIRED: {
    base: 'PERMISSION_DENIED',
    title: '需要人工审批',
    guidance: '该操作未获直接授权，需先发起审批并等待通过后再执行。',
  },
  AUTHZ_SCOPE_VIOLATION: {
    base: 'PERMISSION_DENIED',
    title: '超出数据范围',
    guidance: '请求越过当前租户/组织/数据范围。请确认作用域，越界请求已被拒绝。',
  },

  /* §3.3 插件域 */
  PLUGIN_INVALID_INPUT: { base: 'INVALID_ARGUMENT', title: '插件输入无效', guidance: '修正输入后重试，不自动重试。' },
  PLUGIN_UNSUPPORTED_OPERATION: { base: 'INVALID_ARGUMENT', title: '插件不支持该操作', guidance: '该能力未由当前插件实现，请更换插件或调整调用。' },
  PLUGIN_PERMISSION_DENIED: { base: 'PERMISSION_DENIED', title: '插件权限不足', guidance: '插件调用被授权拒绝，已记审计；请申请权限。' },
  PLUGIN_DEPENDENCY_UNAVAILABLE: { base: 'UNAVAILABLE', title: '插件依赖不可用', guidance: '依赖服务暂不可用，可按退避策略重试。' },
  PLUGIN_RATE_LIMITED: { base: 'RATE_LIMITED', title: '插件限流', guidance: '请遵循退避策略稍后重试。' },
  PLUGIN_DEADLINE_EXCEEDED: { base: 'DEADLINE_EXCEEDED', title: '插件超时', guidance: '存在副作用时结果未知，请走对账确认，不要盲目重发。' },
  PLUGIN_RESOURCE_EXHAUSTED: { base: 'RESOURCE_EXHAUSTED', title: '插件资源耗尽', guidance: '配额或容量不足，请勿立即无限重试。' },
  PLUGIN_INTERNAL_ERROR: { base: 'INTERNAL', title: '插件内部错误', guidance: '可有限重试或隔离该插件，并关注告警。' },
  PLUGIN_RESULT_INVALID: { base: 'VALIDATION_FAILED', title: '插件结果不合法', guidance: '结果已被隔离并记录诊断，请检查契约后重试。' },

  /* §3.4 模型域 */
  MODEL_DATA_POLICY_DENIED: {
    base: 'PERMISSION_DENIED',
    title: '数据分级禁止发送该模型',
    guidance: '该数据不得发送至当前模型；不能通过更换未批准模型规避，请调整数据策略。',
  },
  MODEL_BUDGET_EXCEEDED: {
    base: 'RESOURCE_EXHAUSTED',
    title: '模型预算不足',
    guidance: '已停止新增模型调用。请调整预算或缩小请求范围。',
  },
  MODEL_CAPABILITY_UNAVAILABLE: { base: 'UNAVAILABLE', title: '无合格模型可用', guidance: '当前无满足能力要求的模型，请调整模型策略。' },
  MODEL_OUTPUT_INVALID: { base: 'VALIDATION_FAILED', title: '模型输出未通过校验', guidance: '受限重生成仍失败，请调整 Prompt/契约或换用其他模型。' },
  MODEL_PROVIDER_UNAVAILABLE: { base: 'UNAVAILABLE', title: '模型供应商不可用', guidance: '供应商不可用或熔断中，可按退避重试或切换已批准模型。' },

  /* §3.5 总线域 */
  BUS_CAPACITY_EXCEEDED: { base: 'RESOURCE_EXHAUSTED', title: '总线容量已满', guidance: '队列或预算已满，过载时不会伪造成功；请降低速率或扩容。' },
  BUS_DUPLICATE_EVENT: { base: 'CONFLICT', title: '重复事件（幂等命中）', guidance: 'Inbox 去重命中，属幂等成功路径，通常无需处理。' },
  BUS_SEQUENCE_GAP: { base: 'CONFLICT', title: '事件序号缺口', guidance: '缺口超过等待窗口，已触发恢复重读。' },

  /* §3.6 ERP 域 */
  ERP_OPERATION_UNKNOWN: {
    base: 'EXECUTION_UNKNOWN',
    title: '结果待确认（ERP 操作未确认）',
    guidance: '已派发但未取得可信结果。请通过对账确认，禁止盲目重发相同的非幂等操作。',
  },
  ERP_RECEIPT_INVALID: { base: 'VALIDATION_FAILED', title: 'ERP 回执不合法', guidance: '回执格式或签名校验失败，结果不可信，请走对账。' },
  ERP_IDEMPOTENCY_CONFLICT: { base: 'CONFLICT', title: '幂等键冲突', guidance: '同一幂等键对应不同 payload，请重新核对请求内容。' },
  ERP_ADAPTER_UNAVAILABLE: { base: 'UNAVAILABLE', title: 'ERP 连接不可用', guidance: '按连接器能力分级处理；有副作用时需先对账再决定是否重发。' },
  ERP_BUSINESS_REJECTED: { base: 'VALIDATION_FAILED', title: 'ERP 业务规则拒绝', guidance: '远端业务规则拒绝，属业务结果而非平台故障；请修正业务数据。' },
};

/* ============================ ApiError ============================ */

export interface ApiErrorInit {
  code: string;
  baseCode: string;
  message: string;
  retryable: boolean;
  status: number;
  requestId?: string;
  details?: Record<string, unknown>;
  /** 是否为本地传输失败（网络中断/超时），写操作此时结果未确认（§9.4） */
  transport?: boolean;
  /** 写操作遇到传输失败 → 结果可能未知 */
  unknownOutcomeHint?: boolean;
}

export class ApiError extends Error {
  readonly code: string;
  readonly baseCode: string;
  readonly retryable: boolean;
  readonly status: number;
  readonly requestId: string;
  readonly details?: Record<string, unknown>;
  readonly transport: boolean;
  readonly unknownOutcomeHint: boolean;

  constructor(init: ApiErrorInit) {
    super(init.message);
    this.name = 'ApiError';
    this.code = init.code;
    this.baseCode = init.baseCode;
    this.retryable = init.retryable;
    this.status = init.status;
    this.requestId = init.requestId ?? '';
    this.details = init.details;
    this.transport = init.transport ?? false;
    this.unknownOutcomeHint = init.unknownOutcomeHint ?? false;
  }

  /** §10.6.4 —— 409 视为“数据已被他人修改”，表单层需重新拉取并保留差异 */
  get isConflict(): boolean {
    return this.status === 409 || this.baseCode === 'CONFLICT';
  }

  /**
   * 是否为**乐观并发**版本冲突（§10.6.4 ETag / If-Match）。
   *
   * 只有携带 If-Match、且服务端版本前置条件不满足的 409 才是“数据已被他人修改”：
   * 该分支服务端会回带 `details.current_version`
   * （internal/api/handlers/handlers.go 的 ifMatch）。
   *
   * 与之相对，409 还承载**业务前置条件冲突**——删除保护就是典型：
   * “端点已关联模型版本档案”“端点已产生用量账本”“供应商最后一个端点”
   * （internal/api/store/model_endpoints.go、config_crud.go）。这类冲突**不带**
   * current_version，且是确定性拒绝：重新加载、重试都不会成功。若把它也渲染成
   * “已被他人修改 / 重新加载后重试”，就是把确定性的业务拒绝说成并发问题，
   * 会诱导用户朝错误方向反复重试。
   */
  get isVersionConflict(): boolean {
    return this.isConflict && this.details?.current_version != null;
  }

  /** 授权拒绝（含范围越界、需审批），供权限门与提示条分流 */
  get isForbidden(): boolean {
    return this.status === 403 || this.baseCode === 'PERMISSION_DENIED';
  }

  get isUnauthenticated(): boolean {
    return this.status === 401 || this.baseCode === 'UNAUTHENTICATED';
  }

  get isNotFound(): boolean {
    return this.status === 404 || this.baseCode === 'NOT_FOUND';
  }

  /** 结果未知（安全码）——永远不是失败，走对账 */
  get isUnknownOutcome(): boolean {
    return this.baseCode === 'EXECUTION_UNKNOWN';
  }
}

/* ============================ 错误呈现 ============================ */

export type ErrorTone = 'error' | 'warning' | 'info';

export interface ErrorPresentation {
  /** 面向用户的中文标题 */
  title: string;
  /** 面向用户的说明（已脱敏，来自服务端 message 或本地兜底） */
  message: string;
  /** 可执行的下一步指引 */
  guidance: string;
  tone: ErrorTone;
  /** 是否可重试（依据契约 retryable） */
  retryable: boolean;
  /** 是否属于“结果未知”，UI 必须显示“结果待确认”与对账入口 */
  unknownOutcome: boolean;
  /** 是否为权限不足 */
  forbidden: boolean;
  /**
   * 是否为**乐观并发**版本冲突（§10.6.4）。只有它为 true 时，错误态才会附带
   * “已在别处修改 / 重新加载后重试”的引导；业务前置条件冲突（确定性拒绝）
   * 不应带该引导，故此处为 false。
   */
  conflict: boolean;
  code: string;
  baseCode: string;
  requestId: string;
}

const BASE_FALLBACK: Record<BaseCode, { title: string; guidance: string }> = {
  INVALID_ARGUMENT: { title: '请求参数不合法', guidance: '请修正输入后重新提交，此类错误不会因重试而成功。' },
  UNAUTHENTICATED: { title: '身份未认证或已失效', guidance: '请重新登录后再试。' },
  PERMISSION_DENIED: { title: '权限不足', guidance: '当前身份无权执行该操作，可申请权限或联系管理员。' },
  NOT_FOUND: { title: '资源不存在或不可见', guidance: '请确认资源标识与访问范围（出于安全，不区分“不存在”与“无权限”）。' },
  CONFLICT: { title: '数据已被他人修改', guidance: '请重新加载最新版本，保留你已填写的差异内容，确认后重试。' },
  VALIDATION_FAILED: { title: '校验未通过', guidance: '请根据提示修正内容后重试。' },
  RATE_LIMITED: { title: '请求过于频繁', guidance: '请按退避策略（尊重 Retry-After）稍后重试。' },
  UNAVAILABLE: { title: '依赖服务暂不可用', guidance: '请稍后按退避策略重试；若为写操作且可能已派发，请先对账确认结果。' },
  RESOURCE_EXHAUSTED: { title: '配额或容量不足', guidance: '请调整配额/预算或稍后重试，不要无限重试。' },
  DEADLINE_EXCEEDED: { title: '操作超时', guidance: '若操作可能已产生外部副作用，请走对账确认结果，不要盲目重发。' },
  EXECUTION_UNKNOWN: {
    title: '结果待确认',
    guidance: '平台尚未取得可信结果。请通过对账确认最终状态，禁止盲目重发非幂等操作。',
  },
  INTERNAL: { title: '平台内部错误', guidance: '可有限重试；若持续失败请联系管理员并附上请求 ID。' },
};

/**
 * 非并发冲突的 409 文案（业务前置条件冲突）。
 *
 * 契约里 409 CONFLICT 被 §10.6.4 定义为“数据已被他人修改”，但服务端同样用它承载
 * **确定性**的业务拒绝（删除保护等，见 ApiError.isVersionConflict 注释）。这类
 * 冲突没有 If-Match 语义，也永远不会因为“重新加载 + 重试”而成功，必须换一套
 * 不误导的话术，并把重心落在服务端给出的具体原因上。
 */
const PRECONDITION_CONFLICT: { title: string; guidance: string } = {
  title: '操作被拒绝：存在依赖或状态约束',
  guidance:
    '这是服务端依据数据依赖 / 状态给出的确定性拒绝（并非他人并发修改），重新加载或重试都不会成功；请按上方原因先解除依赖，或改用「停用」等替代操作。',
};

/** 依据 ApiError（或任意异常）生成统一的用户可读呈现 */
export function describeError(error: unknown): ErrorPresentation {
  if (error instanceof ApiError) {
    // 结果未知（安全码）：文案必须是“结果待确认”，既非失败也非成功
    if (error.isUnknownOutcome || error.unknownOutcomeHint) {
      const spec = DOMAIN_CODES[error.code];
      return {
        title: spec?.title ?? BASE_FALLBACK.EXECUTION_UNKNOWN.title,
        message: error.message || '操作结果尚未确认，已可转入对账。',
        guidance: spec?.guidance ?? BASE_FALLBACK.EXECUTION_UNKNOWN.guidance,
        tone: 'warning',
        retryable: false,
        unknownOutcome: true,
        forbidden: false,
        conflict: false,
        code: error.code,
        baseCode: error.baseCode,
        requestId: error.requestId,
      };
    }

    const domain = DOMAIN_CODES[error.code];
    const base = (BASE_CODES as readonly string[]).includes(error.baseCode)
      ? BASE_FALLBACK[error.baseCode as BaseCode]
      : undefined;

    let title = domain?.title ?? base?.title ?? '请求失败';
    let guidance = domain?.guidance ?? base?.guidance ?? '请稍后重试；若持续失败请联系管理员。';

    // 409 分流：只有 If-Match 版本冲突才用“数据已被他人修改”话术；业务前置条件
    // 冲突是确定性拒绝，改用 PRECONDITION_CONFLICT，避免诱导用户反复重试。
    // （已登记域码的冲突——如幂等键冲突——交由域码自带文案负责。）
    const versionConflict = error.isVersionConflict;
    if (!domain && error.isConflict && !versionConflict) {
      title = PRECONDITION_CONFLICT.title;
      guidance = PRECONDITION_CONFLICT.guidance;
    }

    let tone: ErrorTone = 'error';
    if (error.isForbidden || error.isUnauthenticated) tone = 'warning';
    else if (error.isConflict) tone = 'warning';

    return {
      title,
      message: error.message || title,
      guidance,
      tone,
      retryable: error.retryable,
      unknownOutcome: false,
      forbidden: error.isForbidden,
      conflict: versionConflict,
      code: error.code,
      baseCode: error.baseCode,
      requestId: error.requestId,
    };
  }

  return {
    title: '无法连接服务',
    message: error instanceof Error ? error.message : '发生未知错误。',
    guidance: '请检查网络连接后重试；若本次为写操作，其结果可能未知，请先核对业务对象当前状态。',
    tone: 'error',
    retryable: true,
    unknownOutcome: false,
    forbidden: false,
    conflict: false,
    code: TRANSPORT_ERROR_CODE,
    baseCode: 'UNAVAILABLE',
    requestId: '',
  };
}

/** 依据契约 retryable 字段给出重试指引（§D 要求） */
export function retryGuidance(error: ApiError): { retryable: boolean; guidance: string; backoffMs?: number } {
  const p = describeError(error);
  if (p.unknownOutcome) {
    return { retryable: false, guidance: '结果未知，请走对账确认，不要自动重发。' };
  }
  if (!p.retryable) {
    return { retryable: false, guidance: p.guidance };
  }
  const backoffMs = error.baseCode === 'RATE_LIMITED' ? 2000 : 1000;
  return { retryable: true, guidance: p.guidance, backoffMs };
}

/** 便捷判断：404/403/422 等不应触发 react-query 自动重试 */
export function shouldRetryQuery(failureCount: number, error: unknown): boolean {
  if (failureCount >= 2) return false;
  if (error instanceof ApiError) {
    if (!error.retryable) return false;
    if (error.status >= 400 && error.status < 500) return false;
  }
  return true;
}
