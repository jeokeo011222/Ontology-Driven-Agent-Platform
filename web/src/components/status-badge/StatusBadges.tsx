import {
  ERP_OPERATION_STATUS_META,
  RUN_STATUS_META,
  STEP_STATUS_META,
  TOOL_CALL_STATUS_META,
} from './status-maps';
import { resolveStatusMeta } from './safe-meta';
import { StatusBadge } from './StatusBadge';
import type { ErpOperationStatus, RunStatus, StepStatus, ToolCallStatus } from '@/types';

/**
 * 说明（自查矩阵缺陷 #19）：
 *   以下四个徽标此前直接索引 `*_STATUS_META[status]`。类型上 status 被窄化为联合类型，
 *   看似安全，但**运行期取值来自网络**——服务端枚举扩展、契约漂移或字段改名都会
 *   让索引返回 undefined，随即在取 `.tone`/`.label` 时抛 TypeError 白屏。
 *   现统一走 resolveStatusMeta：未命中时返回"未知状态"占位（warning + help 图标），
 *   既不崩溃也不伪装成某个已知状态。
 */

/**
 * Run 状态徽标。
 * 始终带 "Run" 维度前缀：明确这是"任务执行状态"，
 * 从而避免把 COMPLETED 误读为"业务操作已成功"（contracts/01 §5.1）。
 */
export function RunStatusBadge({ status, className }: { status: RunStatus; className?: string }) {
  return (
    <StatusBadge
      meta={resolveStatusMeta(RUN_STATUS_META, status, 'Run 状态徽标')}
      dimension="Run"
      className={className}
    />
  );
}

/** ERP 操作状态徽标（业务侧外部操作的权威状态） */
export function ErpOperationStatusBadge({
  status,
  className,
}: {
  status: ErpOperationStatus;
  className?: string;
}) {
  return (
    <StatusBadge
      meta={resolveStatusMeta(ERP_OPERATION_STATUS_META, status, 'ERP 操作状态徽标')}
      dimension="ERP 操作"
      className={className}
    />
  );
}

/** 步骤状态徽标（Run 内部执行步骤，非业务状态） */
export function StepStatusBadge({ status, className }: { status: StepStatus; className?: string }) {
  return (
    <StatusBadge
      meta={resolveStatusMeta(STEP_STATUS_META, status, '步骤状态徽标')}
      dimension="步骤"
      className={className}
    />
  );
}

/** 工具调用状态徽标 */
export function ToolCallStatusBadge({ status, className }: { status: ToolCallStatus; className?: string }) {
  return (
    <StatusBadge
      meta={resolveStatusMeta(TOOL_CALL_STATUS_META, status, '工具调用状态徽标')}
      dimension="工具调用"
      className={className}
    />
  );
}
