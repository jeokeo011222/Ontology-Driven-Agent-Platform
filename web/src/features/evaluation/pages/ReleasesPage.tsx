import { useEffect, useState } from 'react';
import { Button, PageHeader } from '@/components/ui';
import { ConfirmDialog } from '@/components/confirm-dialog';
import { DataTable, type DataTableColumn } from '@/components/data-table';
import { ErrorState } from '@/components/error-state';
import { ListFilterBar } from '@/components/list-filter-bar';
import { PermissionGate } from '@/components/permission-gate';
import { useCursorPagination } from '@/hooks/use-cursor-pagination';
import { formatDateTime } from '@/lib/format';
import type { BadgeTone } from '@/components/ui';
import { MetaBadge } from '@/components/status-badge';
import type { Release } from '@/types';
import { usePromoteRelease, useReleases, useRollbackRelease } from '../api';
import { Icon } from '@/components/icon';

const STATE_META: Record<Release['state'], { label: string; tone: BadgeTone }> = {
  draft: { label: '草稿', tone: 'neutral' },
  candidate: { label: '候选', tone: 'info' },
  promoted: { label: '已发布', tone: 'success' },
  rolled_back: { label: '已回滚', tone: 'warning' },
};

const GATE_META: Record<Release['gate_state'], { label: string; tone: BadgeTone }> = {
  passed: { label: '门禁通过', tone: 'success' },
  failed: { label: '门禁未通过', tone: 'error' },
  pending: { label: '门禁待定', tone: 'warning' },
};

/**
 * 页面：插件与版本发布 `/admin/releases`（§2.1 导航 / 第十五篇 §16.4 / §11.1）
 *
 * 说明：第十七篇 §10.1 的 feature 目录清单未单设 releases 目录；
 * 发布与质量门禁同属评估域（第十五篇），因此本页归入 features/evaluation。
 *
 * 硬性要求：工作流/计划发布必须经过校验与质量门禁；回滚恢复到明确的已发布版本并评估依赖任务。
 */
export function ReleasesPage() {
  const [search, setSearch] = useState('');
  const pager = useCursorPagination();
  const query = useReleases({ q: search || undefined, cursor: pager.cursor ?? undefined });
  const promote = usePromoteRelease();
  const rollback = useRollbackRelease();
  const [promoteTarget, setPromoteTarget] = useState<Release | null>(null);
  const [rollbackTarget, setRollbackTarget] = useState<Release | null>(null);

  // 筛选条件变化必须回到首页：否则会带着旧页游标去请求新筛选结果（假结果）。
  useEffect(() => {
    pager.reset();
  }, [search, pager.reset]);

  const columns: DataTableColumn<Release>[] = [
    {
      key: 'artifact',
      header: '制品',
      render: (item) => (
        <div>
          <div style={{ fontWeight: 500 }}>
            {item.artifact_type} · <span className="mono">{item.artifact_id}</span>
          </div>
          <div className="text-xs muted" style={{ marginTop: 4 }}>
            <span className="copy-id">{item.id}</span> · 版本 <span className="mono">{item.version}</span>
          </div>
        </div>
      ),
    },
    { key: 'state', header: '发布状态', tight: true, render: (item) => <MetaBadge map={STATE_META} value={item.state} context="发布状态" /> },
    { key: 'gate', header: '质量门禁', tight: true, render: (item) => <MetaBadge map={GATE_META} value={item.gate_state} context="发布质量门禁" /> },
    { key: 'env', header: '环境', tight: true, render: (item) => <span className="text-sm">{item.environment ?? '—'}</span> },
    { key: 'promoted', header: '发布时间', tight: true, render: (item) => <span className="text-sm">{formatDateTime(item.promoted_at)}</span> },
    {
      key: 'actions',
      header: '操作',
      tight: true,
      render: (item) => (
        <PermissionGate permission="admin.release.promote">
          <div className="row">
            <Button
              variant="primary"
              size="sm"
              disabled={item.state === 'promoted' || item.gate_state !== 'passed'}
              title={item.gate_state !== 'passed' ? '质量门禁未通过，禁止发布' : undefined}
              onClick={() => setPromoteTarget(item)}
              icon="rocket">
              发布到生产
            </Button>
            <Button
              variant="danger"
              size="sm"
              disabled={item.state !== 'promoted'}
              onClick={() => setRollbackTarget(item)}
              icon="undo">
              回滚
            </Button>
          </div>
        </PermissionGate>
      ),
    },
  ];

  return (
    <div className="page page--wide">
      <PageHeader
        icon="package"
        title="插件与版本发布"
        subtitle="发布必须经过校验与质量门禁；已发布版本不可原地修改，修改须创建新版本。回滚恢复到明确的已发布版本。"
      />

      {promote.isError ? (
        <div style={{ marginBottom: 16 }}>
          <ErrorState error={promote.error} compact />
        </div>
      ) : null}
      {rollback.isError ? (
        <div style={{ marginBottom: 16 }}>
          <ErrorState error={rollback.error} compact />
        </div>
      ) : null}
      {promote.isSuccess || rollback.isSuccess ? (
        <div className="banner banner--info" style={{ marginBottom: 16 }} role="status">
          <span className="banner__icon" aria-hidden="true">
            <Icon name="play" size={16} />
          </span>
          <span>
            发布操作已受理。最终生效状态以服务端返回的发布记录为准；若涉及外部系统，请同时核对相关任务与外部操作状态。
          </span>
        </div>
      ) : null}

      <ListFilterBar
        searchValue={search}
        onSearchChange={setSearch}
        searchPlaceholder="搜索 decision / policy_revision…"
      />

      <DataTable
        caption="发布记录列表"
        columns={columns}
        rows={query.data?.items ?? []}
        rowKey={(item) => item.id}
        loading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        emptyTitle="暂无发布记录"
        nextCursor={query.data?.next_cursor ?? null}
        hasPrevPage={pager.hasPrevPage}
        onPrevPage={pager.goPrev}
        onNextPage={() => pager.goNext(query.data?.next_cursor ?? null)}
      />

      <ConfirmDialog
        open={promoteTarget !== null}
        title="发布到生产环境"
        confirmLabel="发布到生产环境"
        danger
        target={
          promoteTarget ? (
            <>
              {promoteTarget.artifact_type} · <span className="mono">{promoteTarget.artifact_id}</span> 版本{' '}
              <span className="mono">{promoteTarget.version}</span>
            </>
          ) : null
        }
        impact="发布后新启动的任务将使用该版本；运行中的任务锁定其原版本，不会静默改变语义上下文。"
        scope="生产环境"
        reversible
        requiresApproval={false}
        verification="发布后在发布记录确认状态为「已发布」，并核对质量门禁与评估结果；必要时可回滚到明确的已发布版本。"
        confirmPhrase={promoteTarget?.version}
        busy={promote.isPending}
        error={promote.error}
        onCancel={() => setPromoteTarget(null)}
        onConfirm={() => {
          if (promoteTarget) {
            promote.mutate(promoteTarget.id, { onSuccess: () => setPromoteTarget(null) });
          }
        }}
      />

      <ConfirmDialog
        open={rollbackTarget !== null}
        title="回滚发布版本"
        confirmLabel="执行回滚"
        danger
        target={
          rollbackTarget ? (
            <>
              {rollbackTarget.artifact_type} · <span className="mono">{rollbackTarget.artifact_id}</span> 当前版本{' '}
              <span className="mono">{rollbackTarget.version}</span>
            </>
          ) : null
        }
        impact="回滚将恢复到上一个明确的已发布版本。依赖当前版本且正在运行的任务不会自动回滚，需评估是否需要重跑。"
        scope="生产环境"
        reversible={false}
        requiresApproval
        verification="回滚后在发布记录确认状态为「已回滚」，并核对依赖该版本的任务清单是否受语义变化影响。"
        confirmPhrase={rollbackTarget?.artifact_id}
        busy={rollback.isPending}
        error={rollback.error}
        onCancel={() => setRollbackTarget(null)}
        onConfirm={() => {
          if (rollbackTarget) {
            rollback.mutate(
              { id: rollbackTarget.id, targetVersion: 'previous_published' },
              { onSuccess: () => setRollbackTarget(null) },
            );
          }
        }}
      />
    </div>
  );
}
