import { useState } from 'react';
import {
  Badge,
  Button,
  Card,
  CardBody,
  CardHeader,
  PageHeader,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
} from '@/components/ui';
import { DataTable, type DataTableColumn } from '@/components/data-table';
import { LoadingState } from '@/components/loading-state';
import { MetaBadge, resolveBadgeMeta } from '@/components/status-badge';
import { useCursorPagination } from '@/hooks/use-cursor-pagination';
import { resolveTaskPhase } from '@/hooks/useTask';
import { formatDateTime } from '@/lib/format';
import type { BadgeTone } from '@/components/ui';
import type { EvaluationRun } from '@/types';
import { useEvaluations } from '../api';

const GATE_META: Record<EvaluationRun['quality_gate'], { label: string; tone: BadgeTone }> = {
  passed: { label: '门禁通过', tone: 'success' },
  failed: { label: '门禁未通过', tone: 'error' },
  pending: { label: '门禁待定', tone: 'warning' },
};

/**
 * MVP 页面 6b：评估与质量门禁 `/admin/evaluation`（§14.3 / 第十五篇）
 *
 * 要求：质量门禁结果必须可见；未通过门禁的候选版本不得进入生产。
 * 任务状态区分「排队中/执行中/已完成/失败/结果待确认」，202 不得渲染成成功。
 */
export function EvaluationPage() {
  const pager = useCursorPagination();
  const query = useEvaluations({ cursor: pager.cursor ?? undefined });
  const [selectedId, setSelectedId] = useState<string | null>(null);

  const selected = (query.data?.items ?? []).find((e) => e.id === selectedId) ?? null;

  const columns: DataTableColumn<EvaluationRun>[] = [
    {
      key: 'name',
      header: '评估任务',
      render: (item) => (
        <div>
          <div style={{ fontWeight: 500 }}>{item.name}</div>
          <div className="text-xs muted" style={{ marginTop: 4 }}>
            <span className="copy-id">{item.id}</span> · 目标 <span className="mono">{item.target_ref}</span>
          </div>
        </div>
      ),
    },
    {
      key: 'state',
      header: '执行状态',
      tight: true,
      render: (item) => {
        // 安全读取：运行期取值来自网络，词表未命中时渲染"未知状态"而非抛错（缺陷 #19）
        const phase = resolveTaskPhase(item.state);
        return (
          <Badge
            tone={
              !phase
                ? 'warning'
                : phase.succeeded
                  ? 'success'
                  : phase.unknownOutcome
                    ? 'warning'
                    : item.state === 'failed'
                      ? 'error'
                      : 'info'
            }
            title={phase ? undefined : `评估任务状态取值 ${String(item.state)} 不在前端词表内`}
          >
            {phase?.label ?? '未知状态'}
          </Badge>
        );
      },
    },
    {
      key: 'gate',
      header: '质量门禁',
      tight: true,
      render: (item) => <MetaBadge map={GATE_META} value={item.quality_gate} context="评估质量门禁" />,
    },
    {
      key: 'created',
      header: '创建时间',
      tight: true,
      render: (item) => (
        <span className="text-sm">
          {formatDateTime(item.created_at)}
          <br />
          <span className="text-xs muted">完成 {formatDateTime(item.finished_at)}</span>
        </span>
      ),
    },
    {
      key: 'actions',
      header: '操作',
      tight: true,
      render: (item) => (
        <Button variant="secondary" size="sm" onClick={() => setSelectedId(item.id)} icon="arrowRight">
          查看指标
        </Button>
      ),
    },
  ];

  return (
    <div className="page page--wide">
      <PageHeader
        icon="clipboardCheck"
        title="评估与质量门禁"
        subtitle="评估任务（含仿真与回归）的结果与质量门禁决定候选版本能否进入生产；门禁未通过不得放行。"
      />

      <DataTable
        caption="评估任务列表"
        columns={columns}
        rows={query.data?.items ?? []}
        rowKey={(item) => item.id}
        loading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        emptyTitle="暂无评估任务"
        emptyDescription="评估任务由发布流程或人工触发。"
        nextCursor={query.data?.next_cursor ?? null}
        hasPrevPage={pager.hasPrevPage}
        onPrevPage={pager.goPrev}
        onNextPage={() => pager.goNext(query.data?.next_cursor ?? null)}
      />

      {selected ? (
        <Card className="section" style={{ marginTop: 16 }}>
          <CardHeader
            title={`评估指标 — ${selected.name}`}
            description={`目标 ${selected.target_ref}；门禁结论：${resolveBadgeMeta(GATE_META, selected.quality_gate, '评估详情.门禁结论').label}`}
            actions={
              <Button variant="ghost" size="sm" onClick={() => setSelectedId(null)} icon="x">
                关闭
              </Button>
            }
          />
          <CardBody flush>
            {!selected.metrics || selected.metrics.length === 0 ? (
              <div style={{ padding: 16 }}>
                <LoadingState inline label="该评估尚未产出指标" />
              </div>
            ) : (
              <Table caption="评估指标">
                <TableHead>
                  <TableRow>
                    <TableCell head>指标</TableCell>
                    <TableCell head>数值</TableCell>
                    <TableCell head>阈值</TableCell>
                    <TableCell head>结论</TableCell>
                  </TableRow>
                </TableHead>
                <TableBody>
                  {selected.metrics.map((m) => (
                    <TableRow key={m.name}>
                      <TableCell>{m.name}</TableCell>
                      <TableCell className="mono">{m.value}</TableCell>
                      <TableCell className="mono">{m.threshold ?? '—'}</TableCell>
                      <TableCell>
                        <Badge tone={m.passed ? 'success' : 'error'}>{m.passed ? '通过' : '未通过'}</Badge>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </CardBody>
        </Card>
      ) : null}
    </div>
  );
}
