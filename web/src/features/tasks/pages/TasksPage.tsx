import { useMemo, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { Button, Field, Input, PageHeader, Select } from '@/components/ui';
import { DataTable, type DataTableColumn } from '@/components/data-table';
import { RunStatusBadge, RUN_STATUS_FILTER_OPTIONS } from '@/components/status-badge';
import { formatDateTime, formatRelative } from '@/lib/format';
import type { Run, RunStatus } from '@/types';
import { useRuns } from '../api';
import { Icon } from '@/components/icon';

/** 需关注视图包含的状态（§4.3：不能把“等待 ERP 对账”显示为普通“运行中”） */
const ATTENTION_STATUSES: RunStatus[] = ['REQUIRES_RECONCILIATION', 'FAILED', 'MANUAL_REVIEW'];

/**
 * MVP 页面：我的任务 `/tasks`（§4.3 / §14.3）
 *
 * 支持按状态、业务类型、发起时间筛选；按任务 ID 或业务对象编号搜索；
 * 查看执行进度、最近更新时间与当前等待原因；权限允许时取消尚未执行的任务。
 */
export function TasksPage() {
  const [params, setParams] = useSearchParams();
  const status = params.get('status') ?? '';
  const view = params.get('view') ?? '';
  const businessType = params.get('business_type') ?? '';
  const q = params.get('q') ?? '';

  const [searchDraft, setSearchDraft] = useState(q);

  const filters = useMemo(
    () => ({
      status: status || undefined,
      business_type: businessType || undefined,
      q: q || undefined,
      order: '-updated_at',
      limit: 50,
    }),
    [status, businessType, q],
  );

  const runsQuery = useRuns(filters);

  const rows = useMemo(() => {
    const items = runsQuery.data?.items ?? [];
    if (view === 'attention') {
      return items.filter((r) => ATTENTION_STATUSES.includes(r.status));
    }
    return items;
  }, [runsQuery.data, view]);

  const updateParam = (key: string, value: string) => {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    if (key === 'status') next.delete('view');
    setParams(next, { replace: true });
  };

  const columns: DataTableColumn<Run>[] = [
    {
      key: 'objective',
      header: '任务',
      render: (run) => (
        <div>
          <Link to={`/tasks/${run.id}`} style={{ fontWeight: 500 }}>
            {run.objective}
          </Link>
          <div className="text-xs muted" style={{ marginTop: 4 }}>
            <span className="copy-id">{run.id}</span>
            {run.business_type ? <> · {run.business_type}</> : null}
            {run.resource_refs?.length ? <> · 业务对象 {run.resource_refs.join('、')}</> : null}
          </div>
          {run.waiting_reason ? (
            <div className="text-xs" style={{ marginTop: 4, color: 'var(--color-warning)' }}>
              当前等待：{run.waiting_reason}
            </div>
          ) : null}
        </div>
      ),
    },
    {
      key: 'status',
      header: '状态',
      tight: true,
      render: (run) => <RunStatusBadge status={run.status} />,
    },
    {
      key: 'progress',
      header: '进度',
      tight: true,
      render: (run) => <span className="mono">{run.progress ?? 0}%</span>,
    },
    {
      key: 'updated_at',
      header: '最近更新',
      tight: true,
      render: (run) => (
        <span className="text-sm">
          {formatDateTime(run.updated_at)}
          <br />
          <span className="text-xs muted">{formatRelative(run.updated_at)}</span>
        </span>
      ),
    },
    {
      key: 'actions',
      header: '操作',
      tight: true,
      render: (run) => (
        <div className="row">
          <Link className="btn btn--secondary btn--sm" to={`/tasks/${run.id}`}>
            查看详情
          </Link>
          {run.status === 'REQUIRES_RECONCILIATION' ? (
            <Link className="btn btn--ghost btn--sm" to={`/tasks/${run.id}?tab=reconcile`}>
              对账
            </Link>
          ) : null}
        </div>
      ),
    },
  ];

  return (
    <div className="page page--wide">
      <PageHeader
        icon="tasks"
        title="我的任务"
        subtitle="任务状态由后端状态驱动；「结果待确认」表示外部操作结果未知，需通过对账确认，既不是失败也不是成功。"
        actions={
          <>
            <Button variant="secondary" size="sm" onClick={() => void runsQuery.refetch()} icon="refresh">
              刷新
            </Button>
          </>
        }
      />

      <div className="filters">
        <Field label="状态" htmlFor="filter-status">
          <Select
            id="filter-status"
            value={status}
            options={RUN_STATUS_FILTER_OPTIONS}
            placeholder="全部状态"
            onChange={(e) => updateParam('status', e.target.value)}
          />
        </Field>
        <Field label="业务类型" htmlFor="filter-business-type">
          <Input
            id="filter-business-type"
            value={businessType}
            placeholder="如 expense_claim_review"
            onChange={(e) => updateParam('business_type', e.target.value)}
          />
        </Field>
        <Field label="搜索（任务 ID / 业务对象）" htmlFor="filter-q">
          <Input
            id="filter-q"
            value={searchDraft}
            placeholder="run-1001 或 claim-1001"
            onChange={(e) => setSearchDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') updateParam('q', searchDraft);
            }}
          />
        </Field>
        <div className="filters__spacer" />
        <div className="row">
          <Button variant="secondary" size="sm" onClick={() => updateParam('q', searchDraft)} icon="search">
            搜索
          </Button>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              setSearchDraft('');
              setParams(new URLSearchParams(), { replace: true });
            }}
            icon="x">
            清除筛选
          </Button>
        </div>
      </div>

      {view === 'attention' ? (
        <div className="banner banner--warning" role="status" style={{ marginBottom: 16 }}>
          <span className="banner__icon" aria-hidden="true">
            <Icon name="alertTriangle" size={16} />
          </span>
          <span>
            当前为「需关注」视图，仅展示结果待确认、失败与需人工复核的任务。这些状态必须区别对待，不能合并为统一的“异常”。
          </span>
        </div>
      ) : null}

      <DataTable
        caption="任务列表"
        columns={columns}
        rows={rows}
        rowKey={(run) => run.id}
        loading={runsQuery.isLoading}
        error={runsQuery.error}
        onRetry={() => void runsQuery.refetch()}
        emptyTitle="暂无任务"
        emptyDescription="调整筛选条件，或从 AI 业务助手发起一个新任务。"
        nextCursor={runsQuery.data?.next_cursor ?? null}
      />
    </div>
  );
}
