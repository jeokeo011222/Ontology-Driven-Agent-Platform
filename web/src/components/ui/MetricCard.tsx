import type { ReactNode } from 'react';
import { Icon, type IconName } from '@/components/icon';
import { cn } from '@/lib/cn';

export type MetricTone = 'brand' | 'neutral' | 'info' | 'success' | 'warning' | 'error';

export interface MetricCardProps {
  label: string;
  value: ReactNode;
  /** 一句话说明指标口径；涉及 Run 终态时必须写明"不代表业务已成功"（§4.1） */
  hint?: ReactNode;
  /** 指标图标：为数字提供语义线索，避免一排纯数字难以区分 */
  icon?: IconName;
  tone?: MetricTone;
  /** 提供后渲染为可点击卡片（跳转到对应筛选列表），并显示行动提示 */
  onClick?: () => void;
  actionLabel?: string;
  className?: string;
}

/**
 * 指标卡：数值 + 语义图标 + 口径说明（§4.1 业务工作台首页）。
 *
 * 设计取舍：
 *   - 数值使用 --font-size-metric(32px) 并启用等宽数字，形成首屏视觉锚点；
 *   - 图标只在图标容器上着色（浅底 + 语义色），数值本身保持中性墨色，
 *     避免"颜色成为唯一状态表达"（§3.2）；
 *   - 仅在"需关注"类指标上用危险色强调数值，其余保持冷静。
 */
export function MetricCard({
  label,
  value,
  hint,
  icon,
  tone = 'brand',
  onClick,
  actionLabel = '查看',
  className,
}: MetricCardProps) {
  const content = (
    <>
      <span className="metric__top">
        <span className="metric__label">{label}</span>
        {icon ? (
          <span className={cn('metric__icon', `metric__icon--${tone}`)} aria-hidden="true">
            <Icon name={icon} size={18} />
          </span>
        ) : null}
      </span>
      <span className="metric__value">{value}</span>
      {hint ? <span className="metric__hint">{hint}</span> : null}
      {onClick ? (
        <span className="metric__action">
          {actionLabel}
          <Icon name="arrowRight" size={12} />
        </span>
      ) : null}
    </>
  );

  if (onClick) {
    return (
      <button type="button" className={cn('metric metric--link', className)} onClick={onClick}>
        {content}
      </button>
    );
  }

  return <div className={cn('metric', className)}>{content}</div>;
}
