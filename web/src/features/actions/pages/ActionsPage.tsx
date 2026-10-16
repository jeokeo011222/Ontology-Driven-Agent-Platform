import { useEffect, useState } from 'react';
import { Badge, Button, Drawer, Field, Input, PageHeader, Select, type SelectOption } from '@/components/ui';
import { DataTable, type DataTableColumn } from '@/components/data-table';
import { ListFilterBar } from '@/components/list-filter-bar';
import { ErrorState } from '@/components/error-state';
import { useCursorPagination } from '@/hooks/use-cursor-pagination';
import { formatDateTime, formatNumber } from '@/lib/format';
import {
  ACTION_OUTCOME_LABEL,
  ACTION_TYPE_LABEL,
  fetchProvTurtle,
  useAction,
  useActions,
  useSimilarActions,
  type ActionDetail,
  type ActionRecord,
  type SimilarActionItem,
} from '../api';

/**
 * 动作管理 `/admin/actions`（模块 D：本体 Action 管理）。
 *
 * 五类一等动作节点（QUERY/UPDATE/INFER/ACTION/TOOL）由平台各执行链路
 * 真实发生时写入（Run 终态/工具调用/推理运行/知识摄取/A2A 任务），
 * 每条动作在图存储开独立 PROV-O 命名图（prov:{tenant}:{action_id}）。
 * 详情抽屉提供溯源三元组、PROV-O Turtle 导出与 pgvector 相似决策检索。
 */

const TYPE_OPTIONS: SelectOption[] = [
  { value: '', label: '全部类型' },
  { value: 'QUERY', label: '查询 QUERY' },
  { value: 'UPDATE', label: '更新 UPDATE' },
  { value: 'INFER', label: '推理 INFER' },
  { value: 'ACTION', label: '动作 ACTION' },
  { value: 'TOOL', label: '工具调用 TOOL' },
];
const OUTCOME_OPTIONS: SelectOption[] = [
  { value: '', label: '全部结论' },
  { value: 'SUCCESS', label: '成功' },
  { value: 'ERROR', label: '错误' },
  { value: 'TIMEOUT', label: '超时' },
  { value: 'CANCELED', label: '已取消' },
];
const ACTOR_KIND_OPTIONS: SelectOption[] = [
  { value: '', label: '全部来源' },
  { value: 'USER', label: '用户' },
  { value: 'WORKER', label: '工作进程' },
  { value: 'A2A', label: 'A2A' },
  { value: 'SYSTEM', label: '系统' },
];

const TYPE_TONE: Record<string, 'info' | 'brand' | 'neutral' | 'warning' | 'success'> = {
  QUERY: 'info',
  UPDATE: 'warning',
  INFER: 'brand',
  ACTION: 'success',
  TOOL: 'neutral',
};

const OUTCOME_TONE: Record<string, 'success' | 'error' | 'neutral'> = {
  SUCCESS: 'success',
  ERROR: 'error',
  TIMEOUT: 'error',
  CANCELED: 'neutral',
};

/** 日期（yyyy-MM-dd）→ RFC3339（当日 00:00 / 23:59:59，本地时区）。 */
function dateToRFC3339(date: string, endOfDay = false): string | undefined {
  if (!date) return undefined;
  const d = new Date(`${date}T${endOfDay ? '23:59:59' : '00:00:00'}`);
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString();
}

/** PROV-O 常用谓词缩写（展示用，不改变原值）。 */
function provPredicateLabel(p: string): string {
  const map: Record<string, string> = {
    'http://www.w3.org/ns/prov#atTime': '发生时间',
    'http://www.w3.org/ns/prov#used': '使用了',
    'http://www.w3.org/ns/prov#wasGeneratedBy': '生成于',
    'http://www.w3.org/ns/prov#wasAssociatedWith': '关联主体',
    'http://www.w3.org/ns/prov#Activity': '活动',
    'http://www.w3.org/ns/prov#Agent': '主体',
    'http://www.w3.org/ns/prov#Entity': '实体',
  };
  return map[p] ?? p;
}

/** PROV-O Turtle 文本下载。 */
function downloadProvTurtle(id: string, onError: (msg: string) => void) {
  fetchProvTurtle(id)
    .then((text) => {
      const blob = new Blob([text], { type: 'text/turtle' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `prov-${id}.ttl`;
      a.click();
      URL.revokeObjectURL(url);
    })
    .catch((e: unknown) => onError((e as Error).message));
}

/** 相似决策检索面板（pgvector 余弦 topK）。 */
function SimilarPanel({ actionId }: { actionId: string }) {
  const similar = useSimilarActions(actionId);
  const [items, setItems] = useState<SimilarActionItem[]>([]);
  const [ran, setRan] = useState(false);

  const run = () => {
    similar.mutate(5, {
      onSuccess: (res) => {
        setItems(res.results ?? []);
        setRan(true);
      },
      onError: () => setRan(true),
    });
  };

  return (
    <section>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
        <h3 style={{ margin: 0, fontSize: 14 }}>相似动作检索</h3>
        <Button size="sm" variant="secondary" icon="search" loading={similar.isPending} onClick={run}>
          检索相似决策
        </Button>
      </div>
      <p className="text-xs muted" style={{ margin: '4px 0 8px' }}>
        基于动作摘要向量（pgvector 余弦）返回历史上最相近的决策；未生成向量的动作（embedding 补算失败或尚未完成）检索会明确报错，不做静默兜底。
      </p>
      {similar.error ? (
        <p className="text-sm" style={{ color: 'var(--color-error)' }}>
          检索失败：{(similar.error as Error).message}
        </p>
      ) : null}
      {ran && !similar.error ? (
        items.length === 0 ? (
          <p className="text-sm muted">没有找到相似动作。</p>
        ) : (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            {items.map((s) => (
              <div key={s.id} style={{ border: '1px solid var(--color-border)', borderRadius: 8, padding: 10 }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
                  <Badge tone={TYPE_TONE[s.type] ?? 'neutral'}>{ACTION_TYPE_LABEL[s.type] ?? s.type}</Badge>
                  <span className="text-sm" style={{ fontWeight: 500 }}>{s.title}</span>
                  <Badge tone={OUTCOME_TONE[s.outcome] ?? 'neutral'}>
                    {ACTION_OUTCOME_LABEL[s.outcome] ?? s.outcome}
                  </Badge>
                  {typeof s.similarity === 'number' ? (
                    <span className="text-xs mono muted">相似度 {formatNumber(s.similarity)}</span>
                  ) : null}
                  {typeof s.distance === 'number' && typeof s.similarity !== 'number' ? (
                    <span className="text-xs mono muted">距离 {formatNumber(s.distance)}</span>
                  ) : null}
                </div>
                <div className="text-xs muted" style={{ marginTop: 4 }}>
                  {formatDateTime(s.occurred_at)}
                </div>
              </div>
            ))}
          </div>
        )
      ) : null}
    </section>
  );
}

/** 动作详情抽屉：元信息 + PROV-O 溯源三元组 + Turtle 导出 + 相似检索。 */
function ActionDrawer({ actionId, onClose }: { actionId: string | null; onClose: () => void }) {
  const detail = useAction(actionId);
  const [provError, setProvError] = useState<string | null>(null);
  const [turtle, setTurtle] = useState<string | null>(null);
  const d: ActionDetail | undefined = detail.data;

  useEffect(() => {
    // 切换目标动作时清空上一次的导出结果。
    setTurtle(null);
    setProvError(null);
  }, [actionId]);

  const run = detail.data?.run;

  return (
    <Drawer open={actionId !== null} onClose={onClose} title="动作详情 · 溯源链" wide>
      {detail.isLoading || !d ? (
        <p className="text-sm muted">正在加载动作详情…</p>
      ) : (
        <div className="stack stack--tight">
          {detail.error ? <ErrorState error={detail.error} onRetry={() => void detail.refetch()} compact /> : null}
          <section>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
              <Badge tone={TYPE_TONE[d.type] ?? 'neutral'}>{ACTION_TYPE_LABEL[d.type] ?? d.type}</Badge>
              <Badge tone={OUTCOME_TONE[d.outcome] ?? 'neutral'}>
                {ACTION_OUTCOME_LABEL[d.outcome] ?? d.outcome}
              </Badge>
              <span style={{ fontWeight: 600 }}>{d.title}</span>
            </div>
            {d.summary ? <p className="text-sm" style={{ margin: '8px 0 0' }}>{d.summary}</p> : null}
            <div style={{ display: 'flex', flexDirection: 'column', gap: 4, marginTop: 8 }}>
              <div className="text-xs muted">
                发生时间 {formatDateTime(d.occurred_at)}
                {d.actor ? ` · 主体 ${d.actor}` : ''}
                {d.actor_kind ? `（${d.actor_kind}）` : ''}
              </div>
              <div className="text-xs muted mono" style={{ overflowWrap: 'anywhere' }}>
                动作 ID {d.id}
              </div>
              {d.run_id ? (
                <div className="text-xs muted">
                  关联 Run <span className="mono copy-id">{d.run_id}</span>
                  {run?.status ? ` · ${run.status}` : ''}
                </div>
              ) : null}
              {d.input_digest ? (
                <div className="text-xs muted mono" style={{ overflowWrap: 'anywhere' }}>
                  输入摘要 {d.input_digest}
                </div>
              ) : null}
              {d.output_digest ? (
                <div className="text-xs muted mono" style={{ overflowWrap: 'anywhere' }}>
                  输出摘要 {d.output_digest}
                </div>
              ) : null}
              {!d.has_embedding ? (
                <div className="text-xs" style={{ color: 'var(--color-warning)' }}>
                  该动作尚未生成检索向量（嵌入异步补算中或补算失败），相似决策检索暂不可用。
                </div>
              ) : null}
            </div>
          </section>

          <section>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
              <h3 style={{ margin: 0, fontSize: 14 }}>PROV-O 溯源（命名图 prov:{'{tenant}'}:{'{action}'}）</h3>
              <Button
                size="sm" variant="secondary" icon="download"
                onClick={() => downloadProvTurtle(d.id, setProvError)}>
                导出 Turtle
              </Button>
            </div>
            {provError ? (
              <p className="text-sm" style={{ color: 'var(--color-error)' }}>{provError}</p>
            ) : null}
            {(d.prov_triples ?? []).length === 0 ? (
              <p className="text-sm muted" style={{ marginTop: 8 }}>该动作没有溯源三元组。</p>
            ) : (
              <div style={{ marginTop: 8, display: 'flex', flexDirection: 'column', gap: 4 }}>
                {d.prov_triples?.map((t, i) => (
                  <div
                    key={`${t.subject?.value}-${t.predicate?.value}-${i}`}
                    style={{ display: 'flex', gap: 8, alignItems: 'baseline' }}>
                    <span className="text-xs mono" style={{ minWidth: 90, color: 'var(--color-text-muted)' }}>
                      {provPredicateLabel(t.predicate?.value ?? '')}
                    </span>
                    <span className="text-xs mono" style={{ overflowWrap: 'anywhere' }}>
                      {t.subject?.value} → {t.object?.value}
                      {t.object?.kind === 'LITERAL' && t.object?.datatype ? (
                        <span className="muted">（{t.object.datatype}）</span>
                      ) : null}
                    </span>
                  </div>
                ))}
              </div>
            )}
            {turtle ? (
              <pre className="mono text-xs" style={{ background: 'var(--color-surface-muted, #f6f6f6)', borderRadius: 8, padding: 12, overflowX: 'auto', marginTop: 12 }}>
{turtle}
              </pre>
            ) : null}
          </section>

          <SimilarPanel actionId={d.id} />
        </div>
      )}
    </Drawer>
  );
}

export function ActionsPage() {
  const [search, setSearch] = useState('');
  const [type, setType] = useState('');
  const [outcome, setOutcome] = useState('');
  const [actorKind, setActorKind] = useState('');
  const [fromDate, setFromDate] = useState('');
  const [toDate, setToDate] = useState('');

  const pager = useCursorPagination();
  const query = useActions({
    q: search,
    type,
    outcome,
    actor_kind: actorKind,
    from: dateToRFC3339(fromDate),
    to: dateToRFC3339(toDate, true),
  });

  const [detailId, setDetailId] = useState<string | null>(null);

  useEffect(() => { pager.reset(); }, [search, type, outcome, actorKind, fromDate, toDate, pager.reset]);

  const columns: DataTableColumn<ActionRecord>[] = [
    {
      key: 'action',
      header: '动作',
      render: (a) => (
        <div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
            <Badge tone={TYPE_TONE[a.type] ?? 'neutral'} title={`动作类型：${a.type}`}>
              {ACTION_TYPE_LABEL[a.type] ?? a.type}
            </Badge>
            <span style={{ fontWeight: 500 }}>{a.title}</span>
          </div>
          {a.summary ? (
            <div className="text-xs muted" style={{ marginTop: 4, overflowWrap: 'anywhere' }}>
              {a.summary.length > 120 ? `${a.summary.slice(0, 120)}…` : a.summary}
            </div>
          ) : null}
        </div>
      ),
    },
    {
      key: 'outcome',
      header: '结论',
      tight: true,
      render: (a) => (
        <Badge tone={OUTCOME_TONE[a.outcome] ?? 'neutral'} title={`动作结论：${a.outcome}`}>
          {ACTION_OUTCOME_LABEL[a.outcome] ?? a.outcome}
        </Badge>
      ),
    },
    {
      key: 'actor',
      header: '主体',
      tight: true,
      render: (a) => (
        <span className="text-sm">
          {a.actor || '—'}
          {a.actor_kind ? <span className="text-xs muted"> {a.actor_kind}</span> : null}
        </span>
      ),
    },
    {
      key: 'occurred_at',
      header: '发生时间',
      tight: true,
      render: (a) => <span className="text-sm">{formatDateTime(a.occurred_at)}</span>,
    },
  ];

  return (
    <div className="page page--wide">
      <PageHeader
        icon="history"
        title="动作管理"
        subtitle="QUERY / UPDATE / INFER / ACTION / TOOL 五类动作一等节点：平台各执行链路真实发生时落账，PROV-O 溯源独立命名图可导出，历史决策可按语义向量检索相似先例。"
      />

      <ListFilterBar
        searchValue={search}
        onSearchChange={setSearch}
        searchPlaceholder="搜索动作标题 / 摘要…"
        statusValue={outcome}
        onStatusChange={setOutcome}
        statusOptions={OUTCOME_OPTIONS}
        extraFilters={
          <>
            <div style={{ minWidth: 150 }}>
              <Select value={type} onChange={(e) => setType(e.target.value)} options={TYPE_OPTIONS} aria-label="动作类型筛选" />
            </div>
            <div style={{ minWidth: 130 }}>
              <Select value={actorKind} onChange={(e) => setActorKind(e.target.value)} options={ACTOR_KIND_OPTIONS} aria-label="主体来源筛选" />
            </div>
            <div style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
              <Field label="从">
                <Input type="date" value={fromDate} onChange={(e) => setFromDate(e.target.value)} />
              </Field>
              <Field label="至">
                <Input type="date" value={toDate} onChange={(e) => setToDate(e.target.value)} />
              </Field>
            </div>
          </>
        }
      />

      <DataTable
        caption="动作列表"
        columns={columns}
        rows={query.data?.items ?? []}
        rowKey={(a) => a.id}
        loading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        emptyTitle="暂无动作"
        emptyDescription="动作由平台执行链路真实写入：Run 终态、工具调用、推理运行、知识摄取与 A2A 任务终态都会在这里留痕。"
        nextCursor={query.data?.next_cursor ?? null}
        hasPrevPage={pager.hasPrevPage}
        onPrevPage={pager.goPrev}
        onNextPage={() => pager.goNext(query.data?.next_cursor ?? null)}
        rowActions={(a) => (
          <>
            <Button variant="ghost" size="sm" onClick={() => setDetailId(a.id)}>
              溯源详情
            </Button>
          </>
        )}
      />

      <ActionDrawer actionId={detailId} onClose={() => setDetailId(null)} />
    </div>
  );
}
