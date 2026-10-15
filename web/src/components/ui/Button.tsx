import type { ButtonHTMLAttributes, ReactNode } from 'react';
import { Icon, Spinner, type IconName } from '@/components/icon';
import { cn } from '@/lib/cn';

export type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger';
export type ButtonSize = 'sm' | 'md' | 'lg';

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  /** 提交中：禁用并显示进度（§9.4 防止重复提交，幂等最终由服务端保证） */
  loading?: boolean;
  block?: boolean;
  /** 前置图标：让"重试/刷新/导出"等动词一眼可辨，避免纯文字按钮难以扫读 */
  icon?: IconName;
  /** 后置图标，如"查看全部 →" */
  trailingIcon?: IconName;
  /** 仅图标按钮：必须同时提供 aria-label，否则读屏无法识别 */
  iconOnly?: boolean;
  children?: ReactNode;
}

/** 图标随按钮尺寸缩放，保证小按钮里图标不显臃肿 */
const ICON_SIZE: Record<ButtonSize, number> = { sm: 14, md: 16, lg: 18 };

/**
 * 按钮基础件。
 * §9.4：危险操作使用 variant="danger"，与普通导航链接视觉权重不同。
 */
export function Button({
  variant = 'secondary',
  size = 'md',
  loading = false,
  block = false,
  icon,
  trailingIcon,
  iconOnly = false,
  className,
  disabled,
  children,
  type = 'button',
  ...rest
}: ButtonProps) {
  const sizeClass = size === 'md' ? null : `btn--${size}`;
  return (
    <button
      type={type}
      className={cn(
        'btn',
        `btn--${variant}`,
        sizeClass,
        iconOnly && 'btn--icon',
        block && 'btn--block',
        className,
      )}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      {...rest}
    >
      {loading ? (
        <Spinner size={ICON_SIZE[size]} />
      ) : icon ? (
        <Icon name={icon} size={ICON_SIZE[size]} />
      ) : null}
      {iconOnly ? null : children}
      {!loading && !iconOnly && trailingIcon ? (
        <Icon name={trailingIcon} size={ICON_SIZE[size]} />
      ) : null}
    </button>
  );
}
