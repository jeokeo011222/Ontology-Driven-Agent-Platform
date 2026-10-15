import type { ReactNode } from 'react';
import { Icon, Spinner, type IconName } from '@/components/icon';
import { cn } from '@/lib/cn';

/**
 * components/error-state/StateView.tsx —— 统一页面状态（§10.5 十种）
 *
 * 十种状态必须由共享组件实现，避免每个页面各自发明提示文字与交互模式：
 *   loading / error / empty / partial / forbidden / stale /
 *   submitting / unknown / success / failure
 *
 * 其中 unknown 表示“操作结果未知”，文案必须是“结果待确认”，
 * 既不能写成失败，更不能写成成功（§9.1 / §13.3 发布门禁）。
 */
export type PageStateKind =
  | 'loading'
  | 'error'
  | 'empty'
  | 'partial'
  | 'forbidden'
  | 'stale'
  | 'submitting'
  | 'unknown'
  | 'success'
  | 'failure';

export interface StateViewProps {
  kind: PageStateKind;
  title?: string;
  description?: ReactNode;
  actions?: ReactNode;
  /** 内联模式：用于卡片内部，不额外描边 */
  inline?: boolean;
  className?: string;
}

interface StatePreset {
  /** 自研 SVG 图标名（颜色之外的第二重表达） */
  icon: IconName;
  title: string;
  description: string;
  modifier?: string;
  /** 是否由 aria-live 播报（状态变化需被辅助技术感知，§12.2） */
  live: 'polite' | 'assertive' | 'off';
}

const PRESETS: Record<PageStateKind, StatePreset> = {
  loading: {
    icon: 'loader',
    title: '加载中',
    description: '正在获取数据，请稍候。',
    live: 'polite',
  },
  error: {
    icon: 'alertOctagon',
    title: '加载失败',
    description: '数据加载失败，可重试；若持续失败请附请求 ID 联系管理员。',
    modifier: 'state--error',
    live: 'assertive',
  },
  empty: {
    icon: 'inbox',
    title: '暂无数据',
    description: '当前筛选条件下没有记录。',
    live: 'polite',
  },
  partial: {
    icon: 'alertTriangle',
    title: '部分数据加载失败',
    description: '已展示可用部分；部分数据获取失败，重试后可能补全。',
    modifier: 'state--warning',
    live: 'polite',
  },
  forbidden: {
    icon: 'lock',
    title: '权限不足',
    description: '当前身份无权查看该内容。可申请权限或联系管理员；该访问尝试会被服务端记录。',
    modifier: 'state--forbidden',
    live: 'assertive',
  },
  stale: {
    icon: 'clock',
    title: '数据可能已过期',
    description: '展示的是缓存数据，服务端版本可能已变化。请刷新以获取最新状态。',
    modifier: 'state--warning',
    live: 'polite',
  },
  submitting: {
    icon: 'loader',
    title: '正在提交',
    description: '请求已发出，正在等待服务端确认。请勿重复提交。',
    live: 'polite',
  },
  unknown: {
    icon: 'arrowLeftRight',
    title: '结果待确认',
    description:
      '平台尚未取得可信结果。请通过对账确认最终状态，切勿按失败处理，也不要重复提交非幂等操作。',
    modifier: 'state--unknown',
    live: 'assertive',
  },
  success: {
    icon: 'circleCheck',
    title: '操作已提交',
    description: '服务端已确认本次操作受理。',
    modifier: 'state--success',
    live: 'polite',
  },
  failure: {
    icon: 'circleX',
    title: '操作失败',
    description: '操作未成功；请按下方指引恢复后重试。',
    modifier: 'state--error',
    live: 'assertive',
  },
};

export function StateView({ kind, title, description, actions, inline, className }: StateViewProps) {
  const preset = PRESETS[kind];
  return (
    <div
      className={cn('state', inline && 'state--inline', preset.modifier, className)}
      role={kind === 'loading' ? 'status' : 'alert'}
      aria-live={preset.live === 'off' ? undefined : preset.live}
    >
      {kind === 'loading' ? (
        <Spinner size={24} />
      ) : (
        <span className="state__icon" aria-hidden="true">
          <Icon name={preset.icon} size={22} />
        </span>
      )}
      <p className="state__title">{title ?? preset.title}</p>
      <p className="state__desc">{description ?? preset.description}</p>
      {actions ? <div className="state__actions">{actions}</div> : null}
    </div>
  );
}

/* ============================ 便捷封装（页面按需复用） ============================ */

export function LoadingState({ label, inline }: { label?: string; inline?: boolean }) {
  return <StateView kind="loading" title={label} inline={inline} />;
}

export function EmptyState({
  title,
  description,
  actions,
  inline,
}: {
  title?: string;
  description?: ReactNode;
  actions?: ReactNode;
  inline?: boolean;
}) {
  return <StateView kind="empty" title={title} description={description} actions={actions} inline={inline} />;
}

export function PartialState({ actions, inline }: { actions?: ReactNode; inline?: boolean }) {
  return <StateView kind="partial" actions={actions} inline={inline} />;
}

export function ForbiddenState({ actions, description }: { actions?: ReactNode; description?: ReactNode }) {
  return <StateView kind="forbidden" actions={actions} description={description} />;
}

export function StaleState({ actions }: { actions?: ReactNode }) {
  return <StateView kind="stale" actions={actions} />;
}

export function SubmittingState({ label }: { label?: string }) {
  return <StateView kind="submitting" title={label} />;
}

export function UnknownOutcomeState({
  description,
  actions,
}: {
  description?: ReactNode;
  actions?: ReactNode;
}) {
  return <StateView kind="unknown" description={description} actions={actions} />;
}

export function SuccessState({ title, actions }: { title?: string; actions?: ReactNode }) {
  return <StateView kind="success" title={title} actions={actions} />;
}

export function FailureState({
  title,
  description,
  actions,
}: {
  title?: string;
  description?: ReactNode;
  actions?: ReactNode;
}) {
  return <StateView kind="failure" title={title} description={description} actions={actions} />;
}
