import { Badge } from '@/components/ui';
import { resolveBadgeMeta, type BadgeMeta } from './safe-meta';

export interface MetaBadgeProps<T extends BadgeMeta> {
  /** 状态词表（Record<枚举, {label, tone}>） */
  map: Record<string, T>;
  /** 待解析取值（来自网络，可能是 undefined / null / 词表外字符串） */
  value: unknown;
  /** 调用位置（用于开发期告警与悬停说明），例如 "本体列表.版本状态" */
  context: string;
  className?: string;
}

/**
 * 由「状态词表 + 取值」直接渲染徽标，**永不抛错**。
 *
 * 这是对 `META[x].tone` 写法的替代（自查矩阵缺陷 #19：直接索引在取值未知时
 * 抛 TypeError 导致整页白屏）。未命中词表时渲染"未知状态"占位（warning 语义色 +
 * 原始取值写入 title + 开发模式控制台点名），既不崩溃也不伪装成已知状态。
 *
 * 用法：`<MetaBadge map={STATE_META} value={item.state} context="Agent 发布状态" />`
 */
export function MetaBadge<T extends BadgeMeta>({ map, value, context, className }: MetaBadgeProps<T>) {
  const meta = resolveBadgeMeta(map, value, context);
  return (
    <Badge tone={meta.tone} title={meta.title} className={className}>
      {meta.label}
    </Badge>
  );
}
