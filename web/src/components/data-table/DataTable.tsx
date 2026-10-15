import type { ReactNode } from 'react';
import { Button, Table, TableBody, TableCell, TableEmptyRow, TableHead, TableRow } from '@/components/ui';
import { EmptyState } from '@/components/empty-state';
import { ErrorState } from '@/components/error-state';
import { LoadingState } from '@/components/loading-state';
import { cn } from '@/lib/cn';

export interface DataTableColumn<T> {
  key: string;
  header: ReactNode;
  render: (row: T) => ReactNode;
  /** 固定宽度的紧凑列（如状态、时间、操作） */
  tight?: boolean;
  width?: string;
}

export interface DataTableProps<T> {
  columns: DataTableColumn<T>[];
  rows: T[];
  rowKey: (row: T) => string;
  caption?: string;
  /** 数据状态驱动（§10.5） */
  loading?: boolean;
  error?: unknown;
  onRetry?: () => void;
  emptyTitle?: string;
  emptyDescription?: ReactNode;
  emptyActions?: ReactNode;
  /** cursor 分页（contracts/03 §1.4） */
  nextCursor?: string | null;
  onLoadMore?: () => void;
  loadingMore?: boolean;
  /** 上一页 / 下一页（由调用方用游标栈维护 hasPrevPage；见 useCursorPagination）。 */
  hasPrevPage?: boolean;
  onPrevPage?: () => void;
  onNextPage?: () => void;
  /** 行级操作列 */
  rowActions?: (row: T) => ReactNode;
  rowActionsHeader?: ReactNode;
  className?: string;
}

/**
 * 数据表格（§12.3 表格设计规范）。
 * - 服务端分页/排序/筛选（cursor 分页，禁止 offset 深分页）
 * - 未收到数据时展示加载/错误/空态，不渲染空表头假象
 */
export function DataTable<T>({
  columns,
  rows,
  rowKey,
  caption,
  loading,
  error,
  onRetry,
  emptyTitle,
  emptyDescription,
  emptyActions,
  nextCursor,
  onLoadMore,
  loadingMore,
  hasPrevPage,
  onPrevPage,
  onNextPage,
  rowActions,
  rowActionsHeader,
  className,
}: DataTableProps<T>) {
  if (loading) {
    return <LoadingState label="正在加载列表…" />;
  }

  if (error) {
    return <ErrorState error={error} onRetry={onRetry} compact />;
  }

  const columnCount = columns.length + (rowActions ? 1 : 0);

  return (
    <div className={cn('stack stack--tight', className)}>
      <Table caption={caption}>
        <TableHead>
          <TableRow>
            {columns.map((col) => (
              <TableCell key={col.key} head className={col.tight ? 'table__cell--tight' : undefined}>
                {col.header}
              </TableCell>
            ))}
            {rowActions ? <TableCell head className="table__cell--tight">{rowActionsHeader ?? '操作'}</TableCell> : null}
          </TableRow>
        </TableHead>
        <TableBody>
          {rows.length === 0 ? (
            <TableEmptyRow colSpan={columnCount}>
              <EmptyState title={emptyTitle} description={emptyDescription} actions={emptyActions} inline />
            </TableEmptyRow>
          ) : (
            rows.map((row) => (
              <TableRow key={rowKey(row)}>
                {columns.map((col) => (
                  <TableCell key={col.key} className={col.tight ? 'table__cell--tight' : undefined}>
                    {col.render(row)}
                  </TableCell>
                ))}
                {rowActions ? (
                  <TableCell className="table__cell--tight">
                    <div className="row">{rowActions(row)}</div>
                  </TableCell>
                ) : null}
              </TableRow>
            ))
          )}
        </TableBody>
      </Table>

      <div className="pagination">
        <span>
          共 {rows.length} 条{nextCursor ? '（还有更多）' : ''}
        </span>
        <div className="row">
          {hasPrevPage && onPrevPage ? (
            <Button size="sm" variant="secondary" onClick={onPrevPage}>
              上一页
            </Button>
          ) : null}
          {nextCursor && onNextPage ? (
            <Button size="sm" variant="secondary" onClick={onNextPage}>
              下一页
            </Button>
          ) : null}
          {nextCursor && onLoadMore ? (
            <Button size="sm" variant="secondary" onClick={onLoadMore} loading={loadingMore} icon="plus">
              加载更多
            </Button>
          ) : null}
        </div>
      </div>
    </div>
  );
}
