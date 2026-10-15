import { useEffect, useState } from 'react';
import { Badge, Button, Card, CardBody, CardHeader, Field, Input, Table, TableBody, TableCell, TableHead, TableRow } from '@/components/ui';
import { DataTable, type DataTableColumn } from '@/components/data-table';
import { ErrorState } from '@/components/error-state';
import { PermissionGate } from '@/components/permission-gate';
import { MetaBadge } from '@/components/status-badge';
import { useCursorPagination } from '@/hooks/use-cursor-pagination';
import { formatDateTime, truncateMiddle } from '@/lib/format';
import type { AuditEvent } from '@/types';
import { useEvidenceEvents } from '../api';
import { verifyChain } from '../hash-chain';
import { Icon } from '@/components/icon';
import { AUDIT_OUTCOME_META } from '@/features/audit/outcome';

/**
 * 证据包视图（§4.6 证据包标签页 / §8.7）。
 * 展示证据目录、版本清单、摘要校验（哈希链）与下载权限。
 * 由 `/admin/audit` 页的「证据包」标签页嵌入使用；第十七篇 §10.1 未单设证据路由。
 */
export function EvidencePage() {
  const [resourceType, setResourceType] = useState('');
  const [draft, setDraft] = useState('');
  const [selected, setSelected] = useState<AuditEvent | null>(null);
  const pager = useCursorPagination();

  const query = useEvidenceEvents({
    resource_type: resourceType || undefined,
    limit: 10,
    cursor: pager.cursor ?? undefined,
  });

  // 筛选变化必须回到首页：否则会带着旧页游标请求新筛选结果（假结果）。
  useEffect(() => {
    pager.reset();
  }, [resourceType, pager.reset]);

  const events = query.data?.items ?? [];
  const chain = verifyChain(events);

  const columns: DataTableColumn<AuditEvent>[] = [
    {
      key: 'id',
      header: '证据条目',
      render: (item) => (
        <div>
          <div style={{ fontWeight: 500 }} className="mono">
            {item.id}
          </div>
          <div className="text-xs muted" style={{ marginTop: 4 }}>
            {item.action} · {item.resource_type} / <span className="copy-id">{item.resource_id}</span>
          </div>
        </div>
      ),
    },
    {
      key: 'hash',
      header: '摘要（hash）',
      render: (item) => (
        <span className="hash-chain">
          <span className="hash-chain__hash" title={item.hash}>
            {item.hash ? truncateMiddle(item.hash, 10, 8) : '—'}
          </span>
          <span className="hash-chain__link">
            ← 前序 {item.prev_hash ? truncateMiddle(item.prev_hash, 10, 8) : '—'}
          </span>
        </span>
      ),
    },
    { key: 'at', header: '时间', tight: true, render: (item) => <span className="text-sm">{formatDateTime(item.occurred_at)}</span> },
    {
      key: 'actions',
      header: '操作',
      tight: true,
      render: (item) => (
        <Button variant="secondary" size="sm" onClick={() => setSelected(item)} icon="arrowRight">
          查看
        </Button>
      ),
    },
  ];

  return (
    <div className="stack">
      <div className="banner banner--neutral">
        <span className="banner__icon" aria-hidden="true">
          <Icon name="flag" size={16} />
        </span>
        <span>
          证据完整性、访问权限与保留期限是三个不同概念。哈希用于检测内容变化，
          但不能证明内容在生成时即为真实，也不能防止具备底层权限者改写全部数据（§8.7）。
        </span>
      </div>

      <div className="filters">
        <Field label="按资源类型过滤" htmlFor="evidence-resource">
          <Input
            id="evidence-resource"
            value={draft}
            placeholder="如 ExpenseClaim / Payment"
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') setResourceType(draft);
            }}
          />
        </Field>
        <div className="filters__spacer" />
        <div className="row">
          <Button variant="secondary" size="sm" onClick={() => setResourceType(draft)} icon="check">
            应用
          </Button>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              setDraft('');
              setResourceType('');
            }}
            icon="x">
            清除
          </Button>
          <PermissionGate permission="audit.read">
            <Button variant="secondary" size="sm" disabled title="导出前服务端会重新校验数据访问权限" icon="download">
              导出证据包
            </Button>
          </PermissionGate>
        </div>
      </div>

      <Card>
        <CardHeader
          title="哈希链校验（前端局部检查）"
          description="按服务端返回顺序核对相邻条目的 prev_hash 与 hash 是否连续；该检查不构成完整性担保。"
        />
        <CardBody>
          {chain.linked ? (
            <Badge tone="success">连续：已核对 {chain.checked} 条</Badge>
          ) : (
            <Badge tone="error">
              断链：首个断点 {chain.brokenAt}（已核对 {chain.checked} 条）
            </Badge>
          )}
          <p className="text-xs muted" style={{ marginTop: 8 }}>
            断链可能表示归档截断、分页边界或数据被改写；请以服务端审计校验器的结论为准，并关注是否已产生安全告警。
          </p>
        </CardBody>
      </Card>

      {query.error ? <ErrorState error={query.error} onRetry={() => void query.refetch()} /> : null}

      <DataTable
        caption="证据条目列表"
        columns={columns}
        rows={events}
        rowKey={(item) => item.id}
        loading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        emptyTitle="暂无证据条目"
        nextCursor={query.data?.next_cursor ?? null}
        hasPrevPage={pager.hasPrevPage}
        onPrevPage={pager.goPrev}
        onNextPage={() => pager.goNext(query.data?.next_cursor ?? null)}
      />

      {selected ? (
        <Card>
          <CardHeader
            title="证据版本清单"
            description={`条目 ${selected.id}`}
            actions={
              <Button variant="ghost" size="sm" onClick={() => setSelected(null)} icon="x">
                关闭
              </Button>
            }
          />
          <CardBody>
            <Table caption="证据条目详情">
              <TableHead>
                <TableRow>
                  <TableCell head>字段</TableCell>
                  <TableCell head>值</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                <TableRow>
                  <TableCell>决策 ID</TableCell>
                  <TableCell>
                    <span className="copy-id">{selected.decision_id ?? '—'}</span>
                  </TableCell>
                </TableRow>
                <TableRow>
                  <TableCell>策略版本</TableCell>
                  <TableCell className="mono">{selected.policy_version ?? '—'}</TableCell>
                </TableRow>
                <TableRow>
                  <TableCell>请求 ID</TableCell>
                  <TableCell>
                    <span className="copy-id">{selected.request_id ?? '—'}</span>
                  </TableCell>
                </TableRow>
                <TableRow>
                  <TableCell>本条目 hash</TableCell>
                  <TableCell className="mono">{selected.hash}</TableCell>
                </TableRow>
                <TableRow>
                  <TableCell>前序 hash</TableCell>
                  <TableCell className="mono">{selected.prev_hash}</TableCell>
                </TableRow>
                <TableRow>
                  <TableCell>主体</TableCell>
                  <TableCell>
                    {selected.actor_display_name || selected.actor_login_name || selected.actor_type || '—'}（
                    <span className="copy-id">{selected.actor_id ?? '—'}</span>）
                  </TableCell>
                </TableRow>
                <TableRow>
                  <TableCell>结论</TableCell>
                  <TableCell>
                    <MetaBadge map={AUDIT_OUTCOME_META} value={selected.outcome} context="证据条目.结论" />
                  </TableCell>
                </TableRow>
              </TableBody>
            </Table>
            <p className="text-xs muted" style={{ marginTop: 12 }}>
              下载权限：证据导出需具备导出权限；导出前服务端会重新校验数据访问权限（§12.3 / §8.7）。
            </p>
          </CardBody>
        </Card>
      ) : null}
    </div>
  );
}
