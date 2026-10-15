import type { CSSProperties, ReactNode } from 'react';
import { Icon, type IconName } from '@/components/icon';
import { cn } from '@/lib/cn';

export interface CardProps {
  children: ReactNode;
  className?: string;
  /** 表格/列表类内容使用 flush 去掉内边距 */
  flush?: boolean;
  style?: CSSProperties;
}

export function Card({ children, className, style }: CardProps) {
  return (
    <section className={cn('card', className)} style={style}>
      {children}
    </section>
  );
}

export interface CardHeaderProps {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  /** 标题前图标：为卡片主题提供快速视觉锚点 */
  icon?: IconName;
  id?: string;
}

export function CardHeader({ title, description, actions, icon, id }: CardHeaderProps) {
  return (
    <header className="card__header">
      <div className="card__heading">
        {icon ? (
          <span className="card__icon" aria-hidden="true">
            <Icon name={icon} size={18} />
          </span>
        ) : null}
        <div>
          <h2 className="card__title" id={id}>
            {title}
          </h2>
          {description ? <p className="card__description">{description}</p> : null}
        </div>
      </div>
      {actions ? <div className="row">{actions}</div> : null}
    </header>
  );
}

export function CardBody({ children, flush, className }: CardProps) {
  return <div className={cn('card__body', flush && 'card__body--flush', className)}>{children}</div>;
}

export function CardFooter({ children, className }: { children: ReactNode; className?: string }) {
  return <footer className={cn('card__footer', className)}>{children}</footer>;
}
