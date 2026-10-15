import { useEffect, useState } from 'react';
import { Badge, Field, Input, PageHeader, Select, TabPanel, Tabs } from '@/components/ui';
import { DataTable, type DataTableColumn } from '@/components/data-table';
import { useCursorPagination } from '@/hooks/use-cursor-pagination';
import { formatDateTime } from '@/lib/format';
import { MetaBadge } from '@/components/status-badge';
import type { AuditEvent } from '@/types';
import { EvidencePage } from '@/features/evidence/pages/EvidencePage';
import { verifyChain } from '@/features/evidence/hash-chain';
import { useAuditEvents } from '../api';
import { AUDIT_OUTCOME_META, AUDIT_OUTCOME_OPTIONS } from '../outcome';

/**
 * MVP 页面 6a：审计中心 `/admin/audit`（§14.3 审计与评估 / 第十一篇）
 *
 * 要求：审计事件可见哈希链；拒绝类事件必须可追溯到决策 ID 与策略版本（contracts/02 §2）。
 */
export function AuditPage() {
  const [tab, setTab] = useState('events');
  const [resourceType, setResourceType] = useState('');
  const [resourceDraft, setResourceDraft] = useState('');
  const [outcome, setOutcome] = useState('');
  const [actorId, setActorId] = useState('');
  const [actorDraft, setActorDraft] = useState('');
  const pager = useCursorPagination();

  const query = useAuditEvents({
    resource_type: resourceType || undefined,
    outcome: outcome || undefined,
    actor_id: actorId || undefined,
    limit: 10,
    cursor: pager.cursor ?? undefined,
  });

  // 筛选变化必须回到首页：否则会带着旧页游标请求新筛选结果（假结果）。
  useEffect(() => {
    pager.reset();
  }, [resourceType, outcome, actorId, pager.reset]);

  const events = query.data?.items ?? [];
  const chain = verifyChain(events);

  const columns: DataTableColumn<AuditEvent>[] = [
    {
      key: 'event',
      header: '审计事件',
      render: (item) => (
        <div>
          <div style={{ fontWeight: 500 }} className="mono">
            {item.action}
          </div>
          <div className="text-xs muted" style={{ marginTop: 4 }}>
            <span className="copy-id">{item.id}</span> · {item.resource_type} /{' '}
            <span className="copy-id">{item.resource_id}</span>
          </div>
        </div>
      ),
    },
    {
      key: 'actor',
      header: '主体',
      tight: true,
      render: (item) => (
        <span className="text-sm">
          {item.actor_display_name || item.actor_login_name || item.actor_type || '—'}
          <br />
          <span className="copy-id text-xs">{item.actor_id ?? '—'}</span>
        </span>
      ),
    },
    {
      key: 'outcome',
      header: '结论',
      tight: true,
      render: (item) => <MetaBadge map={AUDIT_OUTCOME_META} value={item.outcome} context="审计事件结果" />,
    },
    {
      key: 'decision',
      header: '决策 ID / 策略版本',
      tight: true,
      // 非授权决策类事件（配置类 CRUD 等）没有决策 ID 与策略版本；此时输出单个
      // 「—」而不是两行「—」，避免整列被无信息量的占位符填满。
      render: (item) =>
        item.decision_id == null && item.policy_version == null ? (
          <span className="muted">—</span>
        ) : (
          <span className="text-xs">
            <span className="copy-id">{item.decision_id ?? '—'}</span>
            <br />
            <span className="mono muted">{item.policy_version ?? '—'}</span>
          </span>
        ),
    },
    {
      key: 'hash',
      header: '哈希链',
      render: (item) => (
        <span className="hash-chain">
          <span className="hash-chain__hash" title={item.hash}>
            hash {item.hash ? `${item.hash.slice(0, 12)}…` : '—'}
          </span>
          <span className="hash-chain__link" title={item.prev_hash ?? undefined}>
            prev {item.prev_hash ? `${item.prev_hash.slice(0, 12)}…` : '—'}
          </span>
        </span>
      ),
    },
    {
      key: 'occurred_at',
      header: '时间',
      tight: true,
      render: (item) => <span className="text-sm">{formatDateTime(item.occurred_at)}</span>,
    },
  ];

  return (
    <div className="page page--wide">
      <PageHeader
        icon="fileSearch"
        title="审计与证据"
        subtitle="审计事件记录授权决策（含决策 ID 与策略版本）与执行留痕；证据包由审计事件投影得到，哈希链用于检测内容变化。"
        actions={
          <>
            {tab === 'events' ? (
              <Badge tone={chain.linked ? 'success' : 'error'}>
                {chain.linked ? `哈希链连续（已核对 ${chain.checked} 条）` : `哈希链断链：${chain.brokenAt}`}
              </Badge>
            ) : null}
          </>
        }
      />

      <Tabs
        items={[
          { id: 'events', label: '审计事件' },
          { id: 'evidence', label: '证据包' },
        ]}
        value={tab}
        onChange={setTab}
        ariaLabel="审计与证据分区"
      />

      <TabPanel id="events" active={tab === 'events'}>
        <div className="filters">
          <Field label="资源类型" htmlFor="audit-resource">
            <Input
              id="audit-resource"
              value={resourceDraft}
              placeholder="如 ExpenseClaim"
              onChange={(e) => setResourceDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') setResourceType(resourceDraft);
              }}
            />
          </Field>
          <Field label="结论" htmlFor="audit-outcome">
            <Select
              id="audit-outcome"
              value={outcome}
              options={AUDIT_OUTCOME_OPTIONS}
              placeholder="全部"
              onChange={(e) => setOutcome(e.target.value)}
            />
          </Field>
          <Field label="主体 ID" htmlFor="audit-actor">
            <Input
              id="audit-actor"
              value={actorDraft}
              placeholder="如 u_li"
              onChange={(e) => setActorDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') setActorId(actorDraft);
              }}
            />
          </Field>
          <div className="filters__spacer" />
          <div className="row">
            <button
              type="button"
              className="btn btn--secondary btn--sm"
              onClick={() => {
                setResourceType(resourceDraft);
                setActorId(actorDraft);
              }}
            >
              应用筛选
            </button>
          </div>
        </div>

        <DataTable
          caption="审计事件列表"
          columns={columns}
          rows={events}
          rowKey={(item) => item.id}
          loading={query.isLoading}
          error={query.error}
          onRetry={() => void query.refetch()}
          emptyTitle="暂无审计事件"
          nextCursor={query.data?.next_cursor ?? null}
          hasPrevPage={pager.hasPrevPage}
          onPrevPage={pager.goPrev}
          onNextPage={() => pager.goNext(query.data?.next_cursor ?? null)}
        />
      </TabPanel>

      <TabPanel id="evidence" active={tab === 'evidence'}>
        <EvidencePage />
      </TabPanel>
    </div>
  );
}
