import { cn } from '@/lib/cn';
import { ICONS, type IconName } from './icons';

export type { IconName };

export interface IconProps {
  name: IconName;
  /** 像素尺寸，默认 16（与 14px 正文视觉等重）；状态徽标用 14 */
  size?: number;
  /** 线宽；默认 1.75，密集小尺寸可降到 1.6 保持清晰 */
  strokeWidth?: number;
  className?: string;
  /**
   * 提供 title 时图标进入无障碍树（role="img" + <title>），用于"图标是唯一表达"的场景；
   * 省略则 aria-hidden，避免读屏重复播报与相邻文字相同的信息。
   */
  title?: string;
}

/**
 * components/icon/Icon.tsx —— 统一图标出口
 *
 * 统一注入：视图框 24×24、currentColor 描边、圆角端点、默认 aria-hidden。
 * 因此每个图标定义只需写几何，尺寸/线宽/颜色/可访问性全部集中在这里，
 * 不会出现"有的图标 11px 有的 14px、粗细不一"的简陋感。
 */
export function Icon({ name, size = 16, strokeWidth = 1.75, className, title }: IconProps) {
  return (
    <svg
      className={cn('icon', className)}
      viewBox="0 0 24 24"
      width={size}
      height={size}
      fill="none"
      stroke="currentColor"
      strokeWidth={strokeWidth}
      strokeLinecap="round"
      strokeLinejoin="round"
      role={title ? 'img' : undefined}
      aria-hidden={title ? undefined : true}
      focusable="false"
    >
      {title ? <title>{title}</title> : null}
      {ICONS[name]}
    </svg>
  );
}

/** 加载指示：替代字符型转圈，保证与图标同线宽同风格 */
export function Spinner({ size = 16, className, title }: { size?: number; className?: string; title?: string }) {
  return <Icon name="loader" size={size} className={cn('icon--spin', className)} title={title} />;
}
