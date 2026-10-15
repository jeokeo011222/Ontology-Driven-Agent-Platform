import type { ReactNode } from 'react';
import { StateView } from '@/components/error-state';

export interface EmptyStateProps {
  title?: string;
  description?: ReactNode;
  actions?: ReactNode;
  inline?: boolean;
}

/** 空数据态（§10.5） */
export function EmptyState({ title, description, actions, inline }: EmptyStateProps) {
  return <StateView kind="empty" title={title} description={description} actions={actions} inline={inline} />;
}
