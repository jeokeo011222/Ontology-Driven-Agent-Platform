import { useEffect, useState } from 'react';
import { Badge, Button, Dialog, Field, Input, PageHeader, Select, Tabs, TabPanel, Textarea, type SelectOption } from '@/components/ui';
import { ConfirmDialog } from '@/components/confirm-dialog';
import { DataTable, type DataTableColumn } from '@/components/data-table';
import { ListFilterBar } from '@/components/list-filter-bar';
import { Icon } from '@/components/icon';
import { useCursorPagination } from '@/hooks/use-cursor-pagination';
import { formatDateTime, formatDuration } from '@/lib/format';
import {
  useCreateReasoningRule,
  useCreateReasoningRun,
  useDeleteReasoningRule,
  useReasoningOntologyOptions,
  useReasoningRules,
  useReasoningRuns,
  useUpdateReasoningRule,
  type CreateReasoningRuleBody,
  type ReasoningRule,
  type ReasoningRun,
  type ReasoningRunResult,
} from '../api';

/**
 * 推理引擎 `/admin/reasoning`（模块 B）。
 *
 * 页签一「规则」：内置规则（RDFS/OWL2RL，代码内置 + DB 启停行）仅可启停，
 * 编辑/删除服务端一律 409；自定义规则（CUSTOM）可注册/编辑/删除。
 * 页签二「推理运行」：对指定本体版本的图执行前向/后向/增量推理，同步返回
 * 终态（派生三元组数 / 证明路径），派生三元组以 fact_kind='derived' 写回图。
 *
 * 数据口径：contracts/openapi.yaml /reasoning/*（internal/api/store/reason.go）。
 */

const KIND_OPTIONS: SelectOption[] = [
  { value: '', label: '全部类别' },
  { value: 'RDFS', label: 'RDFS' },
  { value: 'OWL2RL', label: 'OWL 2 RL' },
  { value: 'CUSTOM', label: '自定义' },
];

const KIND_TONE: Record<string, 'info' | 'brand' | 'neutral'> = {
  RDFS: 'info',
  OWL2RL: 'brand',
  CUSTOM: 'neutral',
};
const KIND_LABEL: Record<string, string> = {
  RDFS: 'RDFS',
  OWL2RL: 'OWL 2 RL',
  CUSTOM: '自定义',
};

const RUN_STATUS_TONE: Record<string, 'success' | 'error'> = {
  SUCCEEDED: 'success',
  FAILED: 'error',
};
const RUN_STATUS_LABEL: Record<string, string> = {
  SUCCEEDED: '成功',
  FAILED: '失败',
};
const RUN_MODE_LABEL: Record<string, string> = {
  FORWARD: '前向链',
  BACKWARD: '后向链',
  INCREMENTAL: '增量更新',
};

/** 三元组模式 → 紧凑展示（空串=匿名变量，渲染为 *）。 */
function patternText(p: { subject: string; predicate: string; object: string }): string {
  return `(${p.subject || '*'} ${p.predicate || '*'} ${p.object || '*'})`;
}

/** 规则体的紧凑展示：premises → conclusion（body 是 JSON 文本）。 */
function ruleBodySummary(body: string): string {
  try {
    const parsed = JSON.parse(body) as {
      premises?: { subject: string; predicate: string; object: string }[];
      conclusion?: { subject: string; predicate: string; object: string };
    };
    const premises = (parsed.premises ?? []).map(patternText).join(' ∧ ');
    return `${premises || '—'} ⇒ ${parsed.conclusion ? patternText(parsed.conclusion) : '—'}`;
  } catch {
    return '—';
  }
}

/** 新建自定义规则弹窗（kind 固定 CUSTOM；服务端严格校验字段白名单）。 */
function CreateRuleDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const create = useCreateReasoningRule();
  const [form, setForm] = useState({
    rule_key: '', name: '', description: '',
    premises: '', conclusion: '',
  });
  const [localErr, setLocalErr] = useState<string | null>(null);

  const close = () => {
    if (create.isPending) return;
    setForm({ rule_key: '', name: '', description: '', premises: '', conclusion: '' });
    setLocalErr(null);
    onClose();
  };

  /** 前提/结论的行格式：subject<TAB或空格多列>predicate object；每行一条。 */
  const parsePatterns = (
    text: string, count: 'one' | 'many',
  ): { ok: true; value: { subject: string; predicate: string; object: string }[] } | { ok: false; error: string } => {
    const lines = text.split('\n').map((l) => l.trim()).filter(Boolean);
    if (lines.length === 0) return { ok: false, error: '至少提供一行三元组模式。' };
    if (count === 'one' && lines.length !== 1) return { ok: false, error: '结论只能有一行三元组模式。' };
    const out: { subject: string; predicate: string; object: string }[] = [];
    for (const line of lines) {
      const parts = line.split(/\s+/);
      if (parts.length !== 3) {
        return { ok: false, error: `「${line}」不是三列（主语 谓语 宾语，空串变量用 *）。` };
      }
      const [subject, predicate, object] = parts;
      out.push({
        subject: subject === '*' ? '' : subject,
        predicate: predicate === '*' ? '' : predicate,
        object: object === '*' ? '' : object,
      });
    }
    return { ok: true, value: out };
  };

  const submit = () => {
    setLocalErr(null);
    if (!form.rule_key.trim() || !form.name.trim()) {
      setLocalErr('rule_key 与名称必填。');
      return;
    }
    const premises = parsePatterns(form.premises, 'many');
    if (!premises.ok) { setLocalErr(premises.error); return; }
    const conclusion = parsePatterns(form.conclusion, 'one');
    if (!conclusion.ok) { setLocalErr(conclusion.error); return; }
    const body: CreateReasoningRuleBody = {
      rule_key: form.rule_key.trim(),
      name: form.name.trim(),
      kind: 'CUSTOM',
      premises: premises.value,
      conclusion: conclusion.value[0],
      ...(form.description.trim() ? { description: form.description.trim() } : {}),
    };
    create.mutate(body, { onSuccess: close });
  };

  return (
    <Dialog open={open} onClose={close} title="注册自定义规则" wide>
      <form className="dialog-form" onSubmit={(e) => { e.preventDefault(); submit(); }}>
        <Field label="规则键（rule_key）" required hint="英文小写短横线命名，租户内唯一；与内置规则键冲突会被 409 拒绝。">
          <Input value={form.rule_key} placeholder="如 invoice-approval-chain"
            onChange={(e) => setForm({ ...form, rule_key: e.target.value })} required />
        </Field>
        <Field label="名称" required>
          <Input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} required />
        </Field>
        <Field label="说明">
          <Textarea value={form.description} rows={2}
            onChange={(e) => setForm({ ...form, description: e.target.value })} />
        </Field>
        <Field
          label="前提（premises）"
          required
          hint="每行一条「主语 谓语 宾语」，空格分隔；变量以 ?x 表示（匿名变量用 *）。词法约定：带引号为字面值，其余按 IRI 解析。">
          <Textarea value={form.premises} rows={3} className="mono"
            placeholder={'?s http://example.org/ approves ?o\n?s http://example.org/ level ?l'}
            onChange={(e) => setForm({ ...form, premises: e.target.value })} />
        </Field>
        <Field label="结论（conclusion）" required hint="仅一行三元组模式。">
          <Textarea value={form.conclusion} rows={2} className="mono"
            placeholder="?s http://example.org/ senior ?o"
            onChange={(e) => setForm({ ...form, conclusion: e.target.value })} />
        </Field>
        <div className="dialog-actions">
          <Button type="button" variant="ghost" onClick={close} disabled={create.isPending}>取消</Button>
          <Button type="submit" variant="primary" disabled={create.isPending}>
            {create.isPending ? '注册中…' : '注册规则'}
          </Button>
        </div>
        {localErr ? <p className="text-sm" style={{ color: 'var(--color-error)' }}>{localErr}</p> : null}
        {create.error ? (
          <p className="text-sm" style={{ color: 'var(--color-error)' }}>
            注册失败：{(create.error as Error).message}
          </p>
        ) : null}
      </form>
    </Dialog>
  );
}

/** 发起推理运行弹窗：选本体版本 + 模式 + 高级参数。 */
function CreateRunDialog({
  open, onClose, onFinished,
}: { open: boolean; onClose: () => void; onFinished: (result: ReasoningRunResult) => void }) {
  const run = useCreateReasoningRun();
  const ontologyOptions = useReasoningOntologyOptions();
  const [versionId, setVersionId] = useState('');
  const [mode, setMode] = useState<'FORWARD' | 'BACKWARD' | 'INCREMENTAL'>('FORWARD');
  const [maxIterations, setMaxIterations] = useState('');
  const [goalSubject, setGoalSubject] = useState('');
  const [goalPredicate, setGoalPredicate] = useState('');
  const [goalObject, setGoalObject] = useState('');
  const [addedTriples, setAddedTriples] = useState('');
  const [localErr, setLocalErr] = useState<string | null>(null);

  const close = () => {
    if (run.isPending) return;
    setVersionId(''); setMode('FORWARD'); setMaxIterations('');
    setGoalSubject(''); setGoalPredicate(''); setGoalObject(''); setAddedTriples('');
    setLocalErr(null);
    onClose();
  };

  const submit = () => {
    setLocalErr(null);
    if (!versionId) {
      setLocalErr('请选择目标本体版本。');
      return;
    }
    const body: Parameters<typeof run.mutate>[0] = { ontology_version_id: versionId, mode };
    const iter = Number(maxIterations);
    if (maxIterations.trim() !== '') {
      if (!Number.isInteger(iter) || iter <= 0) {
        setLocalErr('最大迭代次数必须为正整数。');
        return;
      }
      body.max_iterations = iter;
    }
    if (mode === 'BACKWARD') {
      if (!goalPredicate.trim()) {
        setLocalErr('后向链必须给出推理目标（goal），谓语不能为空。');
        return;
      }
      body.goal = {
        subject: goalSubject.trim() === '*' ? '' : goalSubject.trim(),
        predicate: goalPredicate.trim() === '*' ? '' : goalPredicate.trim(),
        object: goalObject.trim() === '*' ? '' : goalObject.trim(),
      };
    }
    if (mode === 'INCREMENTAL') {
      const lines = addedTriples.split('\n').map((l) => l.trim()).filter(Boolean);
      if (lines.length === 0) {
        setLocalErr('增量更新必须提供至少一条新增三元组（主语 谓语 宾语）。');
        return;
      }
      const triples: { subject: string; predicate: string; object: string }[] = [];
      for (const line of lines) {
        const parts = line.split(/\s+/);
        if (parts.length !== 3) {
          setLocalErr(`「${line}」不是三列（主语 谓语 宾语）。`);
          return;
        }
        triples.push({ subject: parts[0], predicate: parts[1], object: parts[2] });
      }
      body.added_triples = triples;
    }
    run.mutate(body, {
      onSuccess: (result) => { onFinished(result); close(); },
    });
  };

  const ontologies = ontologyOptions.data?.items ?? [];

  return (
    <Dialog open={open} onClose={close} title="发起推理运行" wide>
      <form className="dialog-form" onSubmit={(e) => { e.preventDefault(); submit(); }}>
        <Field label="目标本体版本" required hint="推理作用于该本体版本对应的命名图（TBox + ABox）。">
          <Select
            value={versionId}
            onChange={(e) => setVersionId(e.target.value)}
            options={[
              { value: '', label: '请选择本体版本…' },
              ...ontologies
                .filter((o) => o.latest_version_id)
                .map((o) => ({
                  value: o.latest_version_id as string,
                  label: `${o.name}（${o.latest_version ?? '—'}）`,
                })),
            ]}
          />
        </Field>
        <Field label="推理模式" required hint="前向链：事实驱动推至不动点；后向链：目标驱动返回证明路径；增量：仅对新增三元组重求值。">
          <Select
            value={mode}
            onChange={(e) => setMode(e.target.value as 'FORWARD' | 'BACKWARD' | 'INCREMENTAL')}
            options={[
              { value: 'FORWARD', label: '前向链（FORWARD）' },
              { value: 'BACKWARD', label: '后向链（BACKWARD）' },
              { value: 'INCREMENTAL', label: '增量更新（INCREMENTAL）' },
            ]}
          />
        </Field>
        {mode === 'BACKWARD' ? (
          <Field label="推理目标（goal）" required hint="三列「主语 谓语 宾语」，任意位置可用 * 表示变量（谓语必须给出）。">
            <div style={{ display: 'flex', gap: 8 }}>
              <Input value={goalSubject} placeholder="主语（* 为变量）" onChange={(e) => setGoalSubject(e.target.value)} />
              <Input value={goalPredicate} placeholder="谓语" onChange={(e) => setGoalPredicate(e.target.value)} />
              <Input value={goalObject} placeholder="宾语（* 为变量）" onChange={(e) => setGoalObject(e.target.value)} />
            </div>
          </Field>
        ) : null}
        {mode === 'INCREMENTAL' ? (
          <Field label="新增三元组（added_triples）" required hint="每行一条「主语 谓语 宾语」；词法约定同规则前提。">
            <Textarea value={addedTriples} rows={3} className="mono"
              onChange={(e) => setAddedTriples(e.target.value)} />
          </Field>
        ) : null}
        <Field label="最大迭代次数（可选）" hint="仅前向链生效；留空使用服务端默认上限。">
          <Input value={maxIterations} inputMode="numeric"
            onChange={(e) => setMaxIterations(e.target.value)} />
        </Field>
        <div className="dialog-actions">
          <Button type="button" variant="ghost" onClick={close} disabled={run.isPending}>取消</Button>
          <Button type="submit" variant="primary" icon="play" loading={run.isPending}>
            {run.isPending ? '推理中…' : '运行推理'}
          </Button>
        </div>
        {localErr ? <p className="text-sm" style={{ color: 'var(--color-error)' }}>{localErr}</p> : null}
        {run.error ? (
          <p className="text-sm" style={{ color: 'var(--color-error)' }}>
            运行失败：{(run.error as Error).message}
          </p>
        ) : null}
      </form>
    </Dialog>
  );
}

export function ReasoningPage() {
  const [tab, setTab] = useState('rules');
  const [search, setSearch] = useState('');
  const [kind, setKind] = useState('');
  const pager = useCursorPagination();
  const rulesQuery = useReasoningRules({ q: search, kind });
  const runsPager = useCursorPagination();
  const runsQuery = useReasoningRuns();
  const updateRule = useUpdateReasoningRule();
  const deleteRule = useDeleteReasoningRule();

  const [createRuleOpen, setCreateRuleOpen] = useState(false);
  const [createRunOpen, setCreateRunOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<ReasoningRule | null>(null);
  const [lastRun, setLastRun] = useState<ReasoningRunResult | null>(null);
  const [ruleToggleError, setRuleToggleError] = useState<string | null>(null);

  useEffect(() => { pager.reset(); }, [search, kind, pager.reset]);

  const ruleColumns: DataTableColumn<ReasoningRule>[] = [
    {
      key: 'rule',
      header: '规则',
      render: (rule) => (
        <div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
            <span className="mono" style={{ fontWeight: 500 }}>{rule.rule_key}</span>
            <Badge tone={KIND_TONE[rule.kind] ?? 'neutral'} title={`规则类别：${rule.kind}`}>
              {KIND_LABEL[rule.kind] ?? rule.kind}
            </Badge>
            {rule.is_builtin ? (
              <Badge tone="info" title="平台内置规则：由代码登记，仅可启停，不可修改或删除">内置</Badge>
            ) : null}
          </div>
          <div className="text-sm" style={{ marginTop: 4 }}>{rule.name}</div>
          <div className="text-xs mono muted" style={{ marginTop: 4, overflowWrap: 'anywhere' }}>
            {ruleBodySummary(rule.body)}
          </div>
          {rule.description ? (
            <div className="text-xs muted" style={{ marginTop: 4 }}>{rule.description}</div>
          ) : null}
        </div>
      ),
    },
    {
      key: 'enabled',
      header: '状态',
      tight: true,
      render: (rule) => (
        <Badge tone={rule.enabled ? 'success' : 'neutral'} title={rule.enabled ? '已启用' : '已停用'}>
          {rule.enabled ? '启用' : '停用'}
        </Badge>
      ),
    },
  ];

  const runColumns: DataTableColumn<ReasoningRun>[] = [
    {
      key: 'mode',
      header: '运行',
      render: (r) => (
        <div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <Badge tone="info">{RUN_MODE_LABEL[r.mode] ?? r.mode}</Badge>
            <span className="mono copy-id">{r.id}</span>
          </div>
          <div className="text-xs muted mono" style={{ marginTop: 4, overflowWrap: 'anywhere' }}>
            图 {r.graph_iri}
          </div>
        </div>
      ),
    },
    {
      key: 'counts',
      header: '输入 → 派生',
      tight: true,
      render: (r) => (
        <span className="mono text-sm">
          {r.input_triple_count} → {r.derived_triple_count}
        </span>
      ),
    },
    {
      key: 'duration',
      header: '耗时',
      tight: true,
      render: (r) => <span className="text-sm">{formatDuration(r.duration_ms)}</span>,
    },
    {
      key: 'status',
      header: '状态',
      tight: true,
      render: (r) => (
        <Badge tone={RUN_STATUS_TONE[r.status] ?? 'neutral'} title={r.error ?? r.status}>
          {RUN_STATUS_LABEL[r.status] ?? r.status}
        </Badge>
      ),
    },
    {
      key: 'created_at',
      header: '时间',
      tight: true,
      render: (r) => <span className="text-sm">{formatDateTime(r.created_at)}</span>,
    },
  ];

  return (
    <div className="page page--wide">
      <PageHeader
        icon="arrowLeftRight"
        title="推理引擎"
        subtitle="基于本体图的规则推理（RDFS / OWL 2 RL 子集 + 自定义规则）：前向链推至不动点、后向链返回证明路径、增量更新仅对新增事实重求值；派生三元组以 fact_kind=derived 写回图，与断言事实严格隔离。"
        actions={
          <>
            <Button variant="secondary" icon="plus" onClick={() => setCreateRunOpen(true)}>
              发起推理
            </Button>
            <Button variant="primary" icon="plus" onClick={() => setCreateRuleOpen(true)}>
              注册规则
            </Button>
          </>
        }
      />

      {ruleToggleError ? (
        <div className="banner banner--warning" style={{ marginBottom: 16 }} role="alert">
          <Icon name="alertTriangle" size={16} />
          <span>{ruleToggleError}</span>
        </div>
      ) : null}

      {lastRun ? (
        <div className={lastRun.run.status === 'SUCCEEDED' ? 'banner banner--success' : 'banner banner--warning'}
          style={{ marginBottom: 16 }} role="status">
          <Icon name={lastRun.run.status === 'SUCCEEDED' ? 'circleCheck' : 'alertTriangle'} size={16} />
          <span>
            推理运行 {RUN_MODE_LABEL[lastRun.run.mode] ?? lastRun.run.mode}{' '}
            {lastRun.run.status === 'SUCCEEDED' ? '完成' : `失败：${lastRun.run.error ?? '未知错误'}`}
            {lastRun.run.status === 'SUCCEEDED' ? (
              <>
                ：输入 {lastRun.run.input_triple_count} 条
                {typeof lastRun.derived_triple_count === 'number' ? `，派生 ${lastRun.derived_triple_count} 条` : ''}
                {typeof lastRun.proved === 'boolean' ? `，目标${lastRun.proved ? '已证明' : '无法证明'}` : ''}
                ，耗时 {formatDuration(lastRun.duration_ms ?? lastRun.run.duration_ms)}。派生三元组已写回图（fact_kind=derived）。
              </>
            ) : null}
          </span>
        </div>
      ) : null}

      <Tabs
        ariaLabel="推理引擎页签"
        value={tab}
        onChange={setTab}
        items={[
          { id: 'rules', label: '规则' },
          { id: 'runs', label: '推理运行' },
        ]}
      />

      <TabPanel id="rules" active={tab === 'rules'}>
        <ListFilterBar
          searchValue={search}
          onSearchChange={setSearch}
          searchPlaceholder="搜索规则键 / 名称…"
          statusValue={kind}
          onStatusChange={setKind}
          statusOptions={KIND_OPTIONS}
        />
        <DataTable
          caption="推理规则列表"
          columns={ruleColumns}
          rows={rulesQuery.data?.items ?? []}
          rowKey={(rule) => rule.id}
          loading={rulesQuery.isLoading}
          error={rulesQuery.error}
          onRetry={() => void rulesQuery.refetch()}
          emptyTitle="暂无规则"
          emptyDescription="平台内置 RDFS / OWL 2 RL 规则集开箱可用；也可注册面向业务的自定义规则。"
          nextCursor={rulesQuery.data?.next_cursor ?? null}
          hasPrevPage={pager.hasPrevPage}
          onPrevPage={pager.goPrev}
          onNextPage={() => pager.goNext(rulesQuery.data?.next_cursor ?? null)}
          rowActions={(rule) => (
            <>
              <Button
                variant="ghost" size="sm" iconOnly
                icon={rule.enabled ? 'pause' : 'play'}
                aria-label={rule.enabled ? '停用' : '启用'}
                title={rule.enabled ? '停用该规则（推理运行时跳过）' : '启用该规则'}
                loading={updateRule.isPending && updateRule.variables?.id === rule.id}
                onClick={() => {
                  setRuleToggleError(null);
                  updateRule.mutate(
                    { id: rule.id, body: { enabled: !rule.enabled } },
                    { onError: (e) => setRuleToggleError(`规则启停失败：${(e as Error).message}`) },
                  );
                }}
              />
              {!rule.is_builtin ? (
                <>
                  <Button
                    variant="ghost" size="sm" iconOnly icon="ban"
                    aria-label="删除规则"
                    title="删除该自定义规则"
                    loading={deleteRule.isPending && deleteRule.variables === rule.id}
                    onClick={() => setDeleteTarget(rule)}
                  />
                </>
              ) : (
                <span className="muted text-sm" title="内置规则仅可启停（服务端对编辑/删除返回 409）">
                  不可修改
                </span>
              )}
            </>
          )}
        />
      </TabPanel>

      <TabPanel id="runs" active={tab === 'runs'}>
        <DataTable
          caption="推理运行记录"
          columns={runColumns}
          rows={runsQuery.data?.items ?? []}
          rowKey={(r) => r.id}
          loading={runsQuery.isLoading}
          error={runsQuery.error}
          onRetry={() => void runsQuery.refetch()}
          emptyTitle="暂无推理运行"
          emptyDescription="点击「发起推理」对某个本体版本的图执行规则推理。"
          nextCursor={runsQuery.data?.next_cursor ?? null}
          hasPrevPage={runsPager.hasPrevPage}
          onPrevPage={runsPager.goPrev}
          onNextPage={() => runsPager.goNext(runsQuery.data?.next_cursor ?? null)}
        />
      </TabPanel>

      <CreateRuleDialog open={createRuleOpen} onClose={() => setCreateRuleOpen(false)} />
      <CreateRunDialog open={createRunOpen} onClose={() => setCreateRunOpen(false)} onFinished={setLastRun} />

      <ConfirmDialog
        open={deleteTarget !== null}
        title="删除推理规则"
        confirmLabel="删除规则"
        danger
        target={deleteTarget ? <span className="mono">{deleteTarget.rule_key}</span> : null}
        impact="删除后该规则不再参与后续推理运行；已写入图的派生三元组不受影响（如需清理请基于图视图核查）。"
        scope="当前租户"
        reversible={false}
        requiresApproval={false}
        verification="删除后规则列表中该规则消失；审计事件 ReasoningRuleDeleted 可在审计与证据页核对。"
        confirmPhrase={deleteTarget?.rule_key}
        busy={deleteRule.isPending}
        error={deleteRule.error}
        onCancel={() => setDeleteTarget(null)}
        onConfirm={() => {
          if (deleteTarget) deleteRule.mutate(deleteTarget.id, { onSuccess: () => setDeleteTarget(null) });
        }}
      />
    </div>
  );
}
