import type { IconName } from '@/components/icon';
import type { MetricTone } from '@/components/ui';
import { formatDateTime } from '@/lib/format';
import type { WorkspaceSummary } from '@/types';

/**
 * features/workspace/view-model.ts —— 契约 → 视图模型
 *
 * 为什么单独一层：
 *   `/api/v1/workspace/summary` 返回的是**契约结构**（runs_by_status / terminal_runs /
 *   pending_approvals / reconciliation_pending / unknown_external_operations），
 *   而页面要展示的是四个业务口径的指标卡。把换算写在页面里会出现两个问题：
 *     1. 口径散落在 JSX 里，改一个口径要读整页；
 *     2. 契约字段一旦缺失（例如后端灰度期），页面直接崩，而不是退化为 0。
 *   因此这里做纯函数换算：**所有字段都做 ?? 0 兜底**，未知状态码不参与口径，
 *   保证端点字段演进而页面不白屏。
 *
 * 口径定义（与 §4.1 业务工作台首页一致，"待我处理/运行中/已完成/需关注"）：
 *   - 待我处理 = pending_approvals
 *   - 运行中   = runs_by_status.RUNNING
 *   - 已完成   = runs_by_status.COMPLETED（注意：终态合计是 terminal_runs，两者不同）
 *   - 需关注   = 失败 Run + 待对账 + 外部操作结果未知
 */

export interface WorkspaceMetric {
  key: string;
  label: string;
  value: number;
  hint: string;
  to: string;
  icon: IconName;
  tone: MetricTone;
  /** 额外修饰类（如 metric--attention，把数值染成警示色） */
  className?: string;
}

export interface WorkspaceView {
  metrics: WorkspaceMetric[];
  /** 统计口径说明，展示在"概览"区标题右侧 */
  scopeLabel: string;
}

function statusCount(summary: WorkspaceSummary, status: string): number {
  return summary.runs_by_status?.[status] ?? 0;
}

export function toWorkspaceView(summary: WorkspaceSummary): WorkspaceView {
  const running = statusCount(summary, 'RUNNING') + statusCount(summary, 'RECONCILING');
  const completed = statusCount(summary, 'COMPLETED');
  const failed = statusCount(summary, 'FAILED');
  const pendingApprovals = summary.pending_approvals ?? 0;
  const reconciliation = summary.reconciliation_pending ?? 0;
  const unknownOps = summary.unknown_external_operations ?? 0;
  const attention = failed + reconciliation + unknownOps;

  return {
    metrics: [
      {
        key: 'pending',
        label: '待我处理',
        value: pendingApprovals,
        hint: '等待我决策的审批事项',
        to: '/approvals',
        icon: 'inbox',
        tone: 'brand',
      },
      {
        key: 'running',
        label: '运行中',
        value: running,
        hint: 'CREATED / RUNNING / RECONCILING 等非终态 Run',
        to: '/tasks?status=RUNNING',
        icon: 'play',
        tone: 'info',
      },
      {
        key: 'completed',
        label: '已完成',
        value: completed,
        hint: `Run 终态，不代表业务操作已成功（全部终态 ${summary.terminal_runs ?? 0} 个）`,
        to: '/tasks?status=COMPLETED',
        icon: 'circleCheck',
        tone: 'success',
      },
      {
        key: 'attention',
        label: '需关注',
        value: attention,
        hint: `失败 ${failed} · 待对账 ${reconciliation} · 结果未知 ${unknownOps}`,
        to: '/tasks?view=attention',
        icon: 'alertTriangle',
        tone: 'warning',
        // 四个指标里唯一需要"立刻看"的一个：数值用警示色强调
        className: 'metric--attention',
      },
    ],
    scopeLabel:
      summary.generated_at != null
        ? `统计截至 ${formatDateTime(summary.generated_at)} · 可见 Run 共 ${summary.runs_total ?? 0} 个`
        : `可见 Run 共 ${summary.runs_total ?? 0} 个`,
  };
}
