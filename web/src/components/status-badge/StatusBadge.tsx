import { Icon } from '@/components/icon';
import { cn } from '@/lib/cn';
import type { StatusMeta } from './status-maps';

export interface StatusBadgeProps {
  meta: StatusMeta;
  /** 维度前缀，如 "Run" / "ERP 操作" / "步骤"；用于避免把 Run 终态误读为业务成功 */
  dimension?: string;
  /** 覆盖展示标签（默认取 meta.label） */
  label?: string;
  className?: string;
}

/**
 * 状态徽标：颜色 + 文字 + 图标 三重表达（§3.2 结尾）。
 * 颜色不是唯一表达方式，色盲/单色环境下仍可通过图标与文字辨识。
 */
export function StatusBadge({ meta, dimension, label, className }: StatusBadgeProps) {
  const text = label ?? meta.label;
  const full = dimension ? `${dimension}：${text}` : text;
  return (
    <span
      className={cn('status-badge', `status-badge--${meta.tone}`, className)}
      title={`${full} — ${meta.description}`}
      role="status"
    >
      <span className="status-badge__icon" aria-hidden="true">
        <Icon name={meta.icon} size={14} />
      </span>
      {dimension ? <span className="sr-only">{dimension}</span> : null}
      <span>{text}</span>
    </span>
  );
}
