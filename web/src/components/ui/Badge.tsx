import type { ReactNode } from 'react';
import { cn } from '@/lib/cn';

export type BadgeTone = 'neutral' | 'brand' | 'success' | 'warning' | 'error' | 'info';

export interface BadgeProps {
  tone?: BadgeTone;
  children: ReactNode;
  className?: string;
  title?: string;
}

/** 徽标：语义色 + 文字（颜色不作为唯一表达，§3.2） */
export function Badge({ tone = 'neutral', children, className, title }: BadgeProps) {
  return (
    <span className={cn('badge', `badge--${tone}`, className)} title={title}>
      {children}
    </span>
  );
}
