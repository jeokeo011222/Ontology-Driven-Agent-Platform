import type { ReactNode } from 'react';
import { Icon, type IconName } from '@/components/icon';
import { cn } from '@/lib/cn';

export interface PageHeaderProps {
  title: ReactNode;
  subtitle?: ReactNode;
  /** 页面图标：放在标题左侧的浅底方块中，形成页面级识别标记 */
  icon?: IconName;
  /** 主操作区（按钮组） */
  actions?: ReactNode;
  /** 面包屑等前置信息 */
  breadcrumbs?: ReactNode;
  className?: string;
}

/**
 * 页面头部：统一"面包屑 + 图标 + 标题 + 副标题 + 主操作"的排版与间距，
 * 避免各页面自行拼接导致标题层级与间距不一致（§3.3 字体、间距与布局）。
 */
export function PageHeader({ title, subtitle, icon, actions, breadcrumbs, className }: PageHeaderProps) {
  return (
    <header className={cn('page__header', className)}>
      <div style={{ minWidth: 0 }}>
        {breadcrumbs}
        <div className="page__heading">
          {icon ? (
            <span className="page__heading-icon" aria-hidden="true">
              <Icon name={icon} size={20} />
            </span>
          ) : null}
          <div style={{ minWidth: 0 }}>
            <h1 className="page__title">{title}</h1>
            {subtitle ? <p className="page__subtitle">{subtitle}</p> : null}
          </div>
        </div>
      </div>
      {actions ? <div className="page__actions">{actions}</div> : null}
    </header>
  );
}
