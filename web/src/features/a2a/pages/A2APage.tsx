import { useEffect, useState } from 'react';
import { Badge, Button, Dialog, Field, Input, PageHeader, Tabs, TabPanel, Textarea, type SelectOption } from '@/components/ui';
import { ConfirmDialog } from '@/components/confirm-dialog';
import { DataTable, type DataTableColumn } from '@/components/data-table';
import { ListFilterBar, FILTER_ALL_OPTION } from '@/components/list-filter-bar';
import { ErrorState } from '@/components/error-state';
import { useCursorPagination } from '@/hooks/use-cursor-pagination';
import { formatDateTime } from '@/lib/format';
import {
  useA2ATasks,
  useAgentCard,
  useCreateRemoteAgent,
  useDeleteRemoteAgent,
  useProbeRemoteAgent,
  useRemoteAgents,
  useUpdateRemoteAgent,
  type A2AAgentCard,
  type A2ARemoteAgent,
  type A2ATaskView,
} from '../api';

/**
 * A2A 协同 `/admin/a2a`（模块 C）。
 *
 * 页签一「平台卡片」：本平台作为 A2A Server 对外发布的 Agent Card
 * （GET /.well-known/agent.json，由 registry 实时装配，不落静态文件）。
 * 页签二「远程智能体」：外部 A2A Agent 注册表 CRUD + 连接测试
 * （fail-closed：拉取对方 Agent Card 失败明确报错）。
 * 页签三「A2A 任务」：经 JSON-RPC 网关 tasks/list 查看映射到平台 Run 的任务。
 */

const STATUS_OPTIONS: SelectOption[] = [
  FILTER_ALL_OPTION,
  { value: 'ACTIVE', label: '启用' },
  { value: 'DISABLED', label: '停用' },
];

const AGENT_STATUS_TONE: Record<string, 'success' | 'neutral'> = {
  ACTIVE: 'success',
  DISABLED: 'neutral',
};
const AGENT_STATUS_LABEL: Record<string, string> = {
  ACTIVE: '启用',
  DISABLED: '停用',
};

const TASK_STATE_TONE: Record<string, 'success' | 'error' | 'warning' | 'neutral' | 'info'> = {
  completed: 'success',
  failed: 'error',
  canceled: 'neutral',
  working: 'warning',
  'input-required': 'warning',
  submitted: 'info',
  accepted: 'info',
};
const TASK_STATE_LABEL: Record<string, string> = {
  completed: '已完成',
  failed: '失败',
  canceled: '已取消',
  working: '执行中',
  'input-required': '等待输入',
  submitted: '已提交',
  accepted: '已受理',
  unknown: '未知',
};

function taskStateBadge(state: string) {
  const tone = TASK_STATE_TONE[state] ?? 'neutral';
  const label = TASK_STATE_LABEL[state] ?? state;
  return <Badge tone={tone} title={`A2A 任务状态：${state}`}>{label}</Badge>;
}

/** 平台 Agent Card 预览页签。 */
function AgentCardPanel() {
  const card = useAgentCard();

  if (card.isLoading) {
    return <p className="text-sm muted">正在获取 Agent Card…</p>;
  }
  if (card.error) {
    return <ErrorState error={card.error} onRetry={() => void card.refetch()} />;
  }
  const c = card.data as A2AAgentCard;
  const skills = c.skills ?? [];

  return (
    <div className="stack stack--tight">
      <section>
        <h3 style={{ margin: 0, fontSize: 14 }}>基本信息</h3>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 4, marginTop: 8 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span style={{ fontWeight: 600 }}>{c.name ?? '—'}</span>
            {c.version ? <Badge tone="info">v{c.version}</Badge> : null}
            {c.protocol_version ? <Badge tone="neutral">协议 {c.protocol_version}</Badge> : null}
          </div>
          {c.description ? <p className="text-sm" style={{ margin: 0 }}>{c.description}</p> : null}
          {c.url ? (
            <p className="text-xs mono muted" style={{ margin: 0, overflowWrap: 'anywhere' }}>
              服务端点 {c.url}
            </p>
          ) : null}
        </div>
      </section>
      <section>
        <h3 style={{ margin: 0, fontSize: 14 }}>技能（skills，{skills.length}）</h3>
        <p className="text-xs muted" style={{ margin: '4px 0 8px' }}>
          由内置技能、平台 Agent 与用户自建技能实时汇总，外部 A2A 客户端据此发现平台能力。
        </p>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          {skills.map((s, i) => (
            <div key={s.id ?? i} style={{ border: '1px solid var(--color-border)', borderRadius: 8, padding: 10 }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                <span style={{ fontWeight: 500 }}>{s.name ?? s.id ?? '—'}</span>
                {s.id ? <span className="text-xs mono muted">{s.id}</span> : null}
              </div>
              {s.description ? <p className="text-sm" style={{ margin: '6px 0 0' }}>{s.description}</p> : null}
            </div>
          ))}
        </div>
      </section>
      <section>
        <h3 style={{ margin: 0, fontSize: 14 }}>能力声明（capabilities）</h3>
        <pre className="mono text-xs" style={{ background: 'var(--color-surface-muted, #f6f6f6)', borderRadius: 8, padding: 12, overflowX: 'auto', margin: '8px 0 0' }}>
{JSON.stringify(c.capabilities ?? {}, null, 2)}
        </pre>
      </section>
    </div>
  );
}

/** 注册/编辑远程智能体弹窗（凭据仅环境变量引用名，明文绝不落库）。 */
function RemoteAgentDialog({
  open, onClose, editing,
}: { open: boolean; onClose: () => void; editing: A2ARemoteAgent | null }) {
  const create = useCreateRemoteAgent();
  const update = useUpdateRemoteAgent();
  const [form, setForm] = useState({
    name: '', base_url: '', auth_header_name: '', auth_header_ref: '', tool_whitelist: '',
  });
  const [localErr, setLocalErr] = useState<string | null>(null);

  // 打开时回填编辑目标。
  useEffect(() => {
    if (open) {
      setForm({
        name: editing?.name ?? '',
        base_url: editing?.base_url ?? '',
        auth_header_name: editing?.auth_header_name ?? '',
        auth_header_ref: editing?.auth_header_ref ?? '',
        tool_whitelist: (editing?.tool_whitelist ?? []).join(', '),
      });
      setLocalErr(null);
    }
  }, [open, editing]);

  const close = () => {
    if (create.isPending || update.isPending) return;
    onClose();
  };

  const submit = () => {
    setLocalErr(null);
    if (!form.name.trim() || !form.base_url.trim()) {
      setLocalErr('名称与 Base URL 必填。');
      return;
    }
    if (!/^https?:\/\//i.test(form.base_url.trim())) {
      setLocalErr('Base URL 必须以 http:// 或 https:// 开头。');
      return;
    }
    const whitelist = form.tool_whitelist.split(/[,，]/).map((s) => s.trim()).filter(Boolean);
    if (editing) {
      update.mutate(
        {
          id: editing.id,
          body: {
            name: form.name.trim(),
            base_url: form.base_url.trim(),
            auth_header_name: form.auth_header_name.trim() || null,
            auth_header_ref: form.auth_header_ref.trim() || null,
            tool_whitelist: whitelist,
          },
        },
        { onSuccess: close },
      );
    } else {
      create.mutate(
        {
          name: form.name.trim(),
          base_url: form.base_url.trim(),
          ...(form.auth_header_name.trim() ? { auth_header_name: form.auth_header_name.trim() } : {}),
          ...(form.auth_header_ref.trim() ? { auth_header_ref: form.auth_header_ref.trim() } : {}),
          tool_whitelist: whitelist,
        },
        { onSuccess: close },
      );
    }
  };

  const pending = create.isPending || update.isPending;
  const error = create.error ?? update.error;

  return (
    <Dialog open={open} onClose={close} title={editing ? '编辑远程智能体' : '注册远程智能体'} wide>
      <form className="dialog-form" onSubmit={(e) => { e.preventDefault(); submit(); }}>
        <Field label="名称" required>
          <Input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} required />
        </Field>
        <Field label="Base URL" required hint="对方 A2A Server 的根地址，连接测试将拉取其 /.well-known/agent.json。">
          <Input value={form.base_url} placeholder="https://a2a.example.com"
            onChange={(e) => setForm({ ...form, base_url: e.target.value })} required />
        </Field>
        <Field label="鉴权头名称" hint="如 Authorization / X-API-Key；留空表示对方不要求鉴权。">
          <Input value={form.auth_header_name}
            onChange={(e) => setForm({ ...form, auth_header_name: e.target.value })} />
        </Field>
        <Field
          label="鉴权头值（环境变量引用）"
          hint="只填环境变量名（如 A2A_PARTNER_TOKEN），明文凭据绝不落库；运行时由服务端从环境读取。">
          <Input value={form.auth_header_ref} className="mono"
            onChange={(e) => setForm({ ...form, auth_header_ref: e.target.value })} />
        </Field>
        <Field label="工具白名单" hint="逗号分隔的工具/技能 ID；留空表示不限制。">
          <Textarea value={form.tool_whitelist} rows={2}
            onChange={(e) => setForm({ ...form, tool_whitelist: e.target.value })} />
        </Field>
        <div className="dialog-actions">
          <Button type="button" variant="ghost" onClick={close} disabled={pending}>取消</Button>
          <Button type="submit" variant="primary" loading={pending}>
            {editing ? '保存' : '注册'}
          </Button>
        </div>
        {localErr ? <p className="text-sm" style={{ color: 'var(--color-error)' }}>{localErr}</p> : null}
        {error ? (
          <p className="text-sm" style={{ color: 'var(--color-error)' }}>
            提交失败：{(error as Error).message}
          </p>
        ) : null}
      </form>
    </Dialog>
  );
}

/** 远程智能体注册表页签。 */
function RemoteAgentsPanel() {
  const [search, setSearch] = useState('');
  const [status, setStatus] = useState('');
  const pager = useCursorPagination();
  const query = useRemoteAgents({ q: search, status });
  const probe = useProbeRemoteAgent();
  const update = useUpdateRemoteAgent();
  const remove = useDeleteRemoteAgent();

  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<A2ARemoteAgent | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<A2ARemoteAgent | null>(null);
  const [probeResult, setProbeResult] = useState<{ id: string; ok: boolean; message: string } | null>(null);

  useEffect(() => { pager.reset(); }, [search, status, pager.reset]);

  const columns: DataTableColumn<A2ARemoteAgent>[] = [
    {
      key: 'agent',
      header: '远程智能体',
      render: (a) => (
        <div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
            <span style={{ fontWeight: 500 }}>{a.name}</span>
            <Badge tone={AGENT_STATUS_TONE[a.status] ?? 'neutral'}>
              {AGENT_STATUS_LABEL[a.status] ?? a.status}
            </Badge>
          </div>
          <div className="text-xs mono muted" style={{ marginTop: 4, overflowWrap: 'anywhere' }}>
            {a.base_url}
          </div>
          {a.auth_header_ref ? (
            <div className="text-xs muted" style={{ marginTop: 4 }}>
              鉴权 {a.auth_header_name || 'Header'} ← 环境变量 <span className="mono">{a.auth_header_ref}</span>
            </div>
          ) : null}
        </div>
      ),
    },
    {
      key: 'probe',
      header: '最近探测',
      render: (a) => (
        <div>
          <div className="text-sm">{a.last_probe_at ? formatDateTime(a.last_probe_at) : '从未探测'}</div>
          {a.last_probe_error ? (
            <div className="text-xs" style={{ color: 'var(--color-error)', marginTop: 4, overflowWrap: 'anywhere' }}>
              {a.last_probe_error}
            </div>
          ) : null}
          {probeResult && probeResult.id === a.id ? (
            <div className="text-xs" style={{ marginTop: 4, color: probeResult.ok ? 'var(--color-success)' : 'var(--color-error)' }}>
              {probeResult.message}
            </div>
          ) : null}
        </div>
      ),
    },
    {
      key: 'updated_at',
      header: '更新时间',
      tight: true,
      render: (a) => <span className="text-sm">{formatDateTime(a.updated_at)}</span>,
    },
  ];

  return (
    <>
      <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 8 }}>
        <Button variant="primary" icon="plus" onClick={() => { setEditing(null); setDialogOpen(true); }}>
          注册远程智能体
        </Button>
      </div>
      <ListFilterBar
        searchValue={search}
        onSearchChange={setSearch}
        searchPlaceholder="搜索名称 / Base URL…"
        statusValue={status}
        onStatusChange={setStatus}
        statusOptions={STATUS_OPTIONS}
      />
      <DataTable
        caption="远程智能体注册表"
        columns={columns}
        rows={query.data?.items ?? []}
        rowKey={(a) => a.id}
        loading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        emptyTitle="暂无远程智能体"
        emptyDescription="注册外部 A2A Agent 后可参与跨平台任务协同；注册时服务端会做 fail-closed 连通性探测。"
        nextCursor={query.data?.next_cursor ?? null}
        hasPrevPage={pager.hasPrevPage}
        onPrevPage={pager.goPrev}
        onNextPage={() => pager.goNext(query.data?.next_cursor ?? null)}
        rowActions={(a) => (
          <>
            <Button
              variant="ghost" size="sm"
              loading={probe.isPending && probe.variables === a.id}
              onClick={() =>
                probe.mutate(a.id, {
                  onSuccess: (r) => {
                    const card = r.agent_card;
                    setProbeResult({
                      id: a.id,
                      ok: true,
                      message: card
                        ? `连通正常：${card.name}${card.version ? ` v${card.version}` : ''}`
                        : '连通正常（对方未返回卡片内容）',
                    });
                  },
                  onError: (e) => setProbeResult({ id: a.id, ok: false, message: (e as Error).message }),
                })
              }>
              连接测试
            </Button>
            <Button
              variant="ghost" size="sm" iconOnly
              icon={a.status === 'ACTIVE' ? 'pause' : 'play'}
              aria-label={a.status === 'ACTIVE' ? '停用' : '启用'}
              title={a.status === 'ACTIVE' ? '停用（不参与任务派发）' : '启用'}
              loading={update.isPending && update.variables?.id === a.id}
              onClick={() =>
                update.mutate({
                  id: a.id,
                  body: { status: a.status === 'ACTIVE' ? 'DISABLED' : 'ACTIVE' },
                })
              }
            />
            <Button
              variant="ghost" size="sm" iconOnly icon="ban"
              aria-label="删除远程智能体"
              title="删除注册"
              loading={remove.isPending && remove.variables === a.id}
              onClick={() => setDeleteTarget(a)}
            />
            <Button variant="ghost" size="sm" onClick={() => { setEditing(a); setDialogOpen(true); }}>
              编辑
            </Button>
          </>
        )}
      />
      <RemoteAgentDialog open={dialogOpen} onClose={() => setDialogOpen(false)} editing={editing} />
      <ConfirmDialog
        open={deleteTarget !== null}
        title="删除远程智能体"
        confirmLabel="删除"
        danger
        target={deleteTarget ? <span className="mono">{deleteTarget.name}</span> : null}
        impact="删除后该远程智能体不再出现在注册表，也无法再向其派发 A2A 任务。"
        scope="当前租户"
        reversible={false}
        requiresApproval={false}
        verification="删除后列表中该条目消失；审计事件可在审计与证据页核对。"
        confirmPhrase={deleteTarget?.name}
        busy={remove.isPending}
        error={remove.error}
        onCancel={() => setDeleteTarget(null)}
        onConfirm={() => {
          if (deleteTarget) remove.mutate(deleteTarget.id, { onSuccess: () => setDeleteTarget(null) });
        }}
      />
    </>
  );
}

/** A2A 任务页签（JSON-RPC tasks/list）。 */
function TasksPanel() {
  const tasks = useA2ATasks();

  const columns: DataTableColumn<A2ATaskView>[] = [
    {
      key: 'task',
      header: '任务（Run）',
      render: (t) => (
        <div>
          <span className="mono copy-id">{t.id}</span>
          {t.context_id ? (
            <div className="text-xs mono muted" style={{ marginTop: 4 }}>{t.context_id}</div>
          ) : null}
        </div>
      ),
    },
    {
      key: 'status',
      header: '状态',
      tight: true,
      render: (t) => taskStateBadge(t.status?.state ?? 'unknown'),
    },
    {
      key: 'message',
      header: '消息',
      render: (t) => (
        <span className="text-sm" style={{ overflowWrap: 'anywhere' }}>
          {t.status?.message || '—'}
        </span>
      ),
    },
    {
      key: 'updated_at',
      header: '更新时间',
      tight: true,
      render: (t) => <span className="text-sm">{t.updated_at ? formatDateTime(t.updated_at) : '—'}</span>,
    },
  ];

  if (tasks.isLoading) return <p className="text-sm muted">正在获取 A2A 任务…</p>;
  if (tasks.error) return <ErrorState error={tasks.error} onRetry={() => void tasks.refetch()} />;

  const data = tasks.data;
  return (
    <div className="stack stack--tight">
      <p className="text-xs muted" style={{ margin: 0 }}>
        经 JSON-RPC 2.0 网关（POST /api/v1/a2a）tasks/list 查询；任务 ID 即平台 Run ID，状态机与平台 Run 收敛链路一致。每 10 秒自动刷新。
      </p>
      {(data?.tasks ?? []).length === 0 ? (
        <p className="text-sm muted">暂无 A2A 任务。</p>
      ) : (
        <DataTable
          caption="A2A 任务列表"
          columns={columns}
          rows={data?.tasks ?? []}
          rowKey={(t) => t.id}
          loading={false}
          error={null}
          onRetry={() => void tasks.refetch()}
          emptyTitle="暂无 A2A 任务"
          emptyDescription="外部 A2A 客户端经 tasks/send 发起的任务会出现在这里。"
        />
      )}
    </div>
  );
}

export function A2APage() {
  const [tab, setTab] = useState('card');
  return (
    <div className="page page--wide">
      <PageHeader
        icon="link"
        title="A2A 协同"
        subtitle="平台作为 A2A Server 对外发布 Agent Card（能力实时装配），并提供 JSON-RPC 2.0 任务接口与 SSE 流式推送；远程智能体注册表管理外部协同方，连通性探测 fail-closed、凭据仅环境变量引用。"
      />
      <Tabs
        ariaLabel="A2A 协同页签"
        value={tab}
        onChange={setTab}
        items={[
          { id: 'card', label: '平台卡片' },
          { id: 'agents', label: '远程智能体' },
          { id: 'tasks', label: 'A2A 任务' },
        ]}
      />
      <TabPanel id="card" active={tab === 'card'}>
        <AgentCardPanel />
      </TabPanel>
      <TabPanel id="agents" active={tab === 'agents'}>
        <RemoteAgentsPanel />
      </TabPanel>
      <TabPanel id="tasks" active={tab === 'tasks'}>
        <TasksPanel />
      </TabPanel>
    </div>
  );
}
