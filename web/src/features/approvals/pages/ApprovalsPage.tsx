import { useEffect, useMemo } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { Badge, Field, PageHeader, Select } from '@/components/ui';
import { DataTable, type DataTableColumn } from '@/components/data-table';
import { RiskBadge } from '@/components/status-badge';
import { useCursorPagination } from '@/hooks/use-cursor-pagination';
import { formatDateTime, formatRelative } from '@/lib/format';
import type { ApprovalStatus, ApprovalSummary } from '@/types';
import { useApprovals } from '../api';

const STATUS_OPTIONS: { value: ApprovalStatus; label: string }[] = [
  { value: 'PENDING', label: '待审批' },
  { value: 'APPROVED', label: '已通过' },
  { value: 'REJECTED', label: '已拒绝' },
  { value: 'RETURNED', label: '已退回' },
  { value: 'EXPIRED', label: '已过期' },
  { value: 'CANCELLED', label: '已取消' },
];

const STATUS_TONE: Record<ApprovalStatus, 'neutral' | 'warning' | 'success' | 'error' | 'info'> = {
  PENDING: 'warning',
  APPROVED: 'success',
  REJECTED: 'error',
  RETURNED: 'info',
  EXPIRED: 'neutral',
  CANCELLED: 'neutral',
};

const STATUS_LABEL: Record<ApprovalStatus, string> = {
  PENDING: '待审批',
  APPROVED: '已通过',
  REJECTED: '已拒绝',
  RETURNED: '已退回',
  EXPIRED: '已过期',
  CANCELLED: '已取消',
};

/**
 * MVP 页面 3a：审批中心列表 `/approvals`（§4.4 / §14.3）
 *
 * 关键语义：列表同时展示「审批状态」与「ERP 执行状态」两列，
 * 因为审批通过 ≠ 外部操作已成功。
 */
export function ApprovalsPage() {
  const [params, setParams] = useSearchParams();
  const status = params.get('status') ?? 'PENDING';
  const pager = useCursorPagination();

  const filters = useMemo(
    () => ({ status: status || undefined, limit: 10, cursor: pager.cursor ?? undefined }),
    [status, pager.cursor],
  );
  const approvalsQuery = useApprovals(filters);

  // 筛选变化必须回到首页：否则会带着旧页游标请求新筛选结果（假结果）。
  useEffect(() => {
    pager.reset();
  }, [status, pager.reset]);

  const columns: DataTableColumn<ApprovalSummary>[] = [
    {
      key: 'title',
      header: '审批事项',
      render: (item) => (
        <div>
          <Link to={`/approvals/${item.id}`} style={{ fontWeight: 500 }}>
            {item.title}
          </Link>
          <div className="text-xs muted" style={{ marginTop: 4 }}>
            <span className="copy-id">{item.id}</span> · 关联任务 <span className="copy-id">{item.run_id}</span>
          </div>
        </div>
      ),
    },
    {
      key: 'resource',
      header: '业务对象',
      tight: true,
      render: (item) => (
        <span className="text-sm">
          {item.resource_type}
          <br />
          <span className="copy-id">{item.resource_id}</span>
        </span>
      ),
    },
    {
      key: 'requester',
      header: '申请人',
      tight: true,
      render: (item) => (
        <span className="text-sm">
          {item.requester.display_name}
          {item.requester.org_path ? (
            <>
              <br />
              <span className="text-xs muted">{item.requester.org_path}</span>
            </>
          ) : null}
        </span>
      ),
    },
    {
      key: 'risk',
      header: '风险',
      tight: true,
      render: (item) => <RiskBadge level={item.risk_level} />,
    },
    {
      key: 'status',
      header: '审批状态',
      tight: true,
      render: (item) => <Badge tone={STATUS_TONE[item.status]}>{STATUS_LABEL[item.status]}</Badge>,
    },
    {
      key: 'erp',
      header: 'ERP 执行状态',
      tight: true,
      render: (item) =>
        item.erp_operation_status ? (
          <Badge tone={item.erp_operation_status === 'SUCCEEDED' ? 'success' : item.erp_operation_status === 'FAILED' ? 'error' : 'warning'}>
            {item.erp_operation_status}
          </Badge>
        ) : (
          <span className="text-xs muted">—</span>
        ),
    },
    {
      key: 'created_at',
      header: '创建时间',
      tight: true,
      render: (item) => (
        <span className="text-sm">
          {formatDateTime(item.created_at)}
          <br />
          <span className="text-xs muted">{formatRelative(item.created_at)}</span>
        </span>
      ),
    },
  ];

  return (
    <div className="page page--wide">
      <PageHeader
        icon="award"
        title="审批中心"
        subtitle="审批通过不等于外部操作已成功。「审批状态」与「ERP 执行状态」分别呈现，必要时以外部操作状态为准。"
      />

      <div className="filters">
        <Field label="审批状态" htmlFor="approval-status">
          <Select
            id="approval-status"
            value={status}
            options={STATUS_OPTIONS}
            placeholder="全部"
            onChange={(e) => {
              const next = new URLSearchParams(params);
              if (e.target.value) next.set('status', e.target.value);
              else next.delete('status');
              setParams(next, { replace: true });
            }}
          />
        </Field>
      </div>

      <DataTable
        caption="待审批列表"
        columns={columns}
        rows={approvalsQuery.data?.items ?? []}
        rowKey={(item) => item.id}
        loading={approvalsQuery.isLoading}
        error={approvalsQuery.error}
        onRetry={() => void approvalsQuery.refetch()}
        emptyTitle="暂无审批事项"
        emptyDescription="当前筛选条件下没有需要处理的审批。"
        nextCursor={approvalsQuery.data?.next_cursor ?? null}
        hasPrevPage={pager.hasPrevPage}
        onPrevPage={pager.goPrev}
        onNextPage={() => pager.goNext(approvalsQuery.data?.next_cursor ?? null)}
      />
    </div>
  );
}
