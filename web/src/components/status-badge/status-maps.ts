/**
 * components/status-badge/status-maps.ts —— 状态 → 中文标签 + 语义色 + 图标
 *
 * 权威：docs/contracts/01-全局状态注册表.md（状态唯一真源）
 *       第十七篇 §3.2（颜色不能是唯一的状态表达方式：必须颜色 + 文字 + 图标）
 *             §11.2（前端标签是投影，不是状态；派生逻辑在服务端）
 *
 * 关键约束：
 *   - Run 共 17 个状态全覆盖（含终态）；类型为 Record<RunStatus, StatusMeta>，
 *     漏一个即编译失败（contracts/01 §6.4 门禁“前端标签映射表覆盖全部 17 个状态”）。
 *   - COMPLETED 是 Run 终态，**不代表业务操作已成功**（contracts/01 §5.1）：
 *     meta.notBusinessSuccess = true，UI 必须同时呈现 ERP 操作维度。
 *   - REQUIRES_RECONCILIATION 显示“结果待确认”，绝不可显示为成功或失败。
 */

import type { IconName } from '@/components/icon';
import type { BadgeTone } from '@/components/ui';
import type {
  ErpOperationStatus,
  RunStatus,
  StepStatus,
  ToolCallStatus,
} from '@/types';

export interface StatusMeta {
  /** 中文标签（投影自 contracts/01，不是状态本身） */
  label: string;
  /** 语义色 */
  tone: BadgeTone;
  /** 图标名（自研 SVG 图标集；颜色之外的第二重表达，§3.2） */
  icon: IconName;
  /** 一句话说明（悬停/辅助技术可读） */
  description: string;
  /** true：该状态不得被解读为“业务已成功”，必须并列业务维度（contracts/01 §5.1） */
  notBusinessSuccess?: boolean;
}

/* ============================ Run：17 个状态全覆盖 ============================ */

export const RUN_STATUS_META: Record<RunStatus, StatusMeta> = {
  CREATED: {
    label: '已创建',
    tone: 'neutral',
    icon: 'circle',
    description: '任务已创建，等待初始化',
  },
  CONTEXT_BUILDING: {
    label: '构建上下文中',
    tone: 'info',
    icon: 'loader',
    description: '正在构建运行上下文',
  },
  READY: {
    label: '待运行',
    tone: 'neutral',
    icon: 'circleDot',
    description: '上下文已固定，可规划或执行',
  },
  PLANNING: {
    label: '规划中',
    tone: 'info',
    icon: 'sparkles',
    description: 'Agent 正在生成执行计划',
  },
  PLAN_VALIDATING: {
    label: '计划校验中',
    tone: 'info',
    icon: 'loader',
    description: '计划结构、依赖与风险校验中',
  },
  RUNNING: {
    label: '运行中',
    tone: 'info',
    icon: 'play',
    description: '正在执行步骤',
  },
  WAITING_INPUT: {
    label: '待补充信息',
    tone: 'warning',
    icon: 'pencil',
    description: '等待用户补充信息',
  },
  WAITING_APPROVAL: {
    label: '待审批',
    tone: 'warning',
    icon: 'userCheck',
    description: '等待人工审批',
  },
  PAUSED: {
    label: '已暂停',
    tone: 'neutral',
    icon: 'pause',
    description: '用户或管理员主动暂停',
  },
  RECOVERING: {
    label: '恢复中',
    tone: 'info',
    icon: 'rotateCw',
    description: '正在恢复任务或核对外部结果',
  },
  CANCELLING: {
    label: '取消中',
    tone: 'warning',
    icon: 'rotateCcw',
    description: '取消已受理，等待在途外部操作收敛',
  },
  REQUIRES_RECONCILIATION: {
    label: '结果待确认',
    tone: 'warning',
    icon: 'arrowLeftRight',
    description: '存在结果未知的外部操作，需通过对账确认（待对账）',
  },
  MANUAL_REVIEW: {
    label: '需人工复核',
    tone: 'warning',
    icon: 'flag',
    description: '需人工介入才能安全继续',
  },
  COMPLETED: {
    label: '已完成',
    tone: 'success',
    icon: 'circleCheck',
    description: '任务按定义成功完成（Run 终态）；不代表业务操作已成功',
    notBusinessSuccess: true,
  },
  FAILED: {
    label: '已失败',
    tone: 'error',
    icon: 'circleX',
    description: '任务终止且无法继续',
  },
  CANCELLED: {
    label: '已取消',
    tone: 'neutral',
    icon: 'ban',
    description: '取消完成且无未处理执行风险',
  },
  EXPIRED: {
    label: '已过期',
    tone: 'neutral',
    icon: 'hourglass',
    description: '超过允许的生命周期',
  },
};

/* ============================ Step：contracts/01 §2 ============================ */

export const STEP_STATUS_META: Record<StepStatus, StatusMeta> = {
  PENDING: { label: '未就绪', tone: 'neutral', icon: 'circle', description: '依赖未满足' },
  READY: { label: '就绪', tone: 'neutral', icon: 'circleDot', description: '依赖已满足' },
  RUNNING: { label: '执行中', tone: 'info', icon: 'play', description: '步骤执行中' },
  RETRY_WAIT: { label: '等待重试', tone: 'warning', icon: 'rotateCw', description: '等待符合策略的重试' },
  WAITING_INPUT: { label: '待补充信息', tone: 'warning', icon: 'pencil', description: '等待用户输入' },
  WAITING_APPROVAL: { label: '待审批', tone: 'warning', icon: 'userCheck', description: '等待审批' },
  SUCCEEDED: { label: '成功', tone: 'success', icon: 'circleCheck', description: '步骤执行成功' },
  FAILED: { label: '失败', tone: 'error', icon: 'circleX', description: '步骤执行失败' },
  CANCELLED: { label: '已取消', tone: 'neutral', icon: 'ban', description: '步骤已取消' },
  SKIPPED: { label: '已跳过', tone: 'neutral', icon: 'skipForward', description: '步骤被跳过' },
  UNKNOWN: {
    label: '结果待确认',
    tone: 'warning',
    icon: 'arrowLeftRight',
    description: '结果暂时无法确定，需对账',
  },
  RECONCILING: { label: '对账中', tone: 'warning', icon: 'rotateCw', description: '正在对账确认结果' },
};

/* ============================ Tool Call：contracts/01 §3 ============================ */

export const TOOL_CALL_STATUS_META: Record<ToolCallStatus, StatusMeta> = {
  PENDING: { label: '待处理', tone: 'neutral', icon: 'circle', description: '工具调用待处理' },
  VALIDATING: { label: '校验中', tone: 'info', icon: 'loader', description: '参数与权限校验中' },
  REJECTED: { label: '已拒绝', tone: 'error', icon: 'shieldX', description: '校验未通过，被拒绝' },
  WAITING_APPROVAL: { label: '待审批', tone: 'warning', icon: 'userCheck', description: '等待审批后调用' },
  READY: { label: '待派发', tone: 'neutral', icon: 'circleDot', description: '已就绪，等待派发' },
  DISPATCHED: { label: '已派发', tone: 'info', icon: 'send', description: '已派发至执行方' },
  EXECUTING: { label: '执行中', tone: 'info', icon: 'play', description: '工具执行中' },
  SUCCEEDED: { label: '成功', tone: 'success', icon: 'circleCheck', description: '工具调用成功' },
  FAILED: { label: '失败', tone: 'error', icon: 'circleX', description: '工具调用失败' },
  UNKNOWN: {
    label: '结果待确认',
    tone: 'warning',
    icon: 'arrowLeftRight',
    description: '结果暂时无法确定，需对账',
  },
  RECONCILING: { label: '对账中', tone: 'warning', icon: 'rotateCw', description: '正在对账确认结果' },
  MANUAL_REVIEW: { label: '需人工复核', tone: 'warning', icon: 'flag', description: '需人工复核' },
};

/* ============================ ERP Operation：contracts/01 §4 ============================ */

export const ERP_OPERATION_STATUS_META: Record<ErpOperationStatus, StatusMeta> = {
  PENDING: { label: '待派发', tone: 'neutral', icon: 'circle', description: '外部操作尚未派发' },
  DISPATCHED: { label: '已派发', tone: 'info', icon: 'send', description: '已派发至外部系统' },
  EXECUTING: { label: '执行中', tone: 'info', icon: 'play', description: '外部系统正在执行' },
  SUCCEEDED: {
    label: '已确认成功',
    tone: 'success',
    icon: 'circleCheck',
    description: '外部系统已返回可信成功回执',
  },
  FAILED: { label: '已失败', tone: 'error', icon: 'circleX', description: '外部操作明确失败' },
  UNKNOWN: {
    label: '结果待确认',
    tone: 'warning',
    icon: 'arrowLeftRight',
    description: '已派发但未取得可信结果，需对账确认（既非失败也非成功）',
  },
  RECONCILING: { label: '对账中', tone: 'warning', icon: 'rotateCw', description: '正在按幂等键对账' },
  MANUAL_REVIEW: { label: '需人工复核', tone: 'warning', icon: 'flag', description: '对账未能自动判定，需人工处理' },
  COMPENSATED: { label: '已补偿', tone: 'info', icon: 'undo', description: '已通过补偿操作回滚' },
};

/* ============================ 辅助导出 ============================ */

/** Run 状态筛选下拉（§4.3 推荐状态标签，取值以 contracts/01 为准） */
export const RUN_STATUS_FILTER_OPTIONS: { value: RunStatus; label: string }[] = (
  Object.keys(RUN_STATUS_META) as RunStatus[]
).map((value) => ({ value, label: RUN_STATUS_META[value].label }));

/** 是否为“结果待确认”类状态（需要引导用户去对账） */
export function needsReconciliation(runStatus: RunStatus, erpStatus?: ErpOperationStatus | null): boolean {
  if (runStatus === 'REQUIRES_RECONCILIATION') return true;
  return erpStatus === 'UNKNOWN' || erpStatus === 'RECONCILING';
}

/** 业务侧“已确认成功”的唯一判据：ERP 操作 SUCCEEDED（Run 终态不构成判据） */
export function isBusinessConfirmedSuccess(erpStatus?: ErpOperationStatus | null): boolean {
  return erpStatus === 'SUCCEEDED';
}
