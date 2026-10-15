import type { ReactNode } from 'react';
import { cn } from '@/lib/cn';

export interface TableProps {
  children: ReactNode;
  className?: string;
  /** 无障碍标题（供屏幕阅读器） */
  caption?: string;
}

/** 表格容器（§12.3 表格设计规范：服务端分页、可复制 ID、固定重要列） */
export function Table({ children, className, caption }: TableProps) {
  return (
    <div className="table-wrap">
      <table className={cn('table', className)}>
        {caption ? <caption className="sr-only">{caption}</caption> : null}
        {children}
      </table>
    </div>
  );
}

export function TableHead({ children }: { children: ReactNode }) {
  return <thead>{children}</thead>;
}

export function TableBody({ children }: { children: ReactNode }) {
  return <tbody>{children}</tbody>;
}

export function TableRow({ children }: { children: ReactNode }) {
  return <tr>{children}</tr>;
}

export interface TableCellProps {
  children?: ReactNode;
  /** 表头单元格 */
  head?: boolean;
  scope?: 'col' | 'row';
  className?: string;
  colSpan?: number;
}

export function TableCell({ children, head, scope, className, colSpan }: TableCellProps) {
  if (head) {
    return (
      <th scope={scope ?? 'col'} className={className} colSpan={colSpan}>
        {children}
      </th>
    );
  }
  return (
    <td className={className} colSpan={colSpan}>
      {children}
    </td>
  );
}

/** 表格内的空态行（避免表格区域塌陷） */
export function TableEmptyRow({ colSpan, children }: { colSpan: number; children: ReactNode }) {
  return (
    <tr>
      <td className="table__empty" colSpan={colSpan}>
        {children}
      </td>
    </tr>
  );
}
