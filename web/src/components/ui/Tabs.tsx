import type { ReactNode } from 'react';
import { cn } from '@/lib/cn';

export interface TabItem {
  id: string;
  label: ReactNode;
  badge?: ReactNode;
}

export interface TabsProps {
  items: TabItem[];
  value: string;
  onChange: (id: string) => void;
  className?: string;
  /** 无障碍标签 */
  ariaLabel?: string;
}

/** 标签页导航（§3.3 复杂页面优先采用分区和标签页） */
export function Tabs({ items, value, onChange, className, ariaLabel }: TabsProps) {
  return (
    <div className={cn('tabs', className)} role="tablist" aria-label={ariaLabel}>
      {items.map((item) => {
        const selected = item.id === value;
        return (
          <button
            key={item.id}
            type="button"
            role="tab"
            id={`tab-${item.id}`}
            aria-selected={selected}
            aria-controls={`tabpanel-${item.id}`}
            className="tab"
            onClick={() => onChange(item.id)}
          >
            {item.label}
            {item.badge ? <span style={{ marginLeft: 6 }}>{item.badge}</span> : null}
          </button>
        );
      })}
    </div>
  );
}

export interface TabPanelProps {
  id: string;
  active: boolean;
  children: ReactNode;
}

export function TabPanel({ id, active, children }: TabPanelProps) {
  if (!active) return null;
  return (
    <div className="tab-panel" role="tabpanel" id={`tabpanel-${id}`} aria-labelledby={`tab-${id}`}>
      {children}
    </div>
  );
}
