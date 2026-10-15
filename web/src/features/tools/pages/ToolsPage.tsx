import { useEffect, useState } from 'react';
import { Badge, Button, Dialog, Field, Input, PageHeader, Select, Textarea } from '@/components/ui';
import { ConfirmDialog } from '@/components/confirm-dialog';
import { ModelSelect } from '@/components/model-select';
import { DataTable, type DataTableColumn } from '@/components/data-table';
import { ErrorState } from '@/components/error-state';
import { PermissionGate } from '@/components/permission-gate';
import { MetaBadge, versionStatusBadge, type BadgeMeta } from '@/components/status-badge';
import { ListFilterBar, FILTER_ALL_OPTION } from '@/components/list-filter-bar';
import { useCursorPagination } from '@/hooks/use-cursor-pagination';
import { EFFECT_CLASSES, type EffectClass, type ToolInfo } from '@/types';
import {
  useApproveToolInvocation,
  useCreateTool,
  useDeleteTool,
  useGenerateTool,
  useInvokeTool,
  useRejectToolInvocation,
  useToolInvocations,
  useTools,
  useUpdateTool,
  type InvokeToolResponse,
  type ToolInvocation,
} from '../api';
import { Icon } from '@/components/icon';

/**
 * 工具**副作用等级**词表 + 风险语义色。
 *
 * 真源：migrations/platform/000003_create_registry.sql
 *   registry.tool_versions.effect_class CHECK IN ('READ','EXTERNAL_WRITE','INTERNAL_WRITE')
 * 这是后端真实提供的能力边界字段，替代设计稿中列表接口并不返回的 `risk_level`
 * （见自查矩阵缺陷 #19 关联项）。
 */
const EFFECT_META: Record<EffectClass, BadgeMeta> = {
  READ: { label: '只读', tone: 'neutral', title: '只读：不产生任何写入副作用，可安全重试' },
  INTERNAL_WRITE: {
    label: '内部写入',
    tone: 'warning',
    title: '内部写入：只改动平台内数据（如审计台账），不触达外部系统',
  },
  EXTERNAL_WRITE: {
    label: '外部写入',
    tone: 'error',
    title: '外部写入：会触达外部系统，必须经人工复核门禁与幂等键保护',
  },
};

const EFFECT_OPTIONS = EFFECT_CLASSES.map((value) => ({ value, label: value }));

/** 列表筛选状态词表（registry.tools.status 的真实取值集合，10 行/页）。 */
const STATUS_OPTIONS = [
  FILTER_ALL_OPTION,
  { value: 'ACTIVE', label: '启用' },
  { value: 'DISABLED', label: '停用' },
  { value: 'DELETED', label: '已删除' },
];

/** 工具调用状态词表（runtime.tool_invocations.status 的真实取值集合）。 */
const INVOCATION_STATUS_META: Record<string, BadgeMeta> = {
  PENDING_APPROVAL: { label: '待审批', tone: 'warning', title: '外部写入需人工审批确认后才会真实执行' },
  APPROVED: { label: '已批准', tone: 'info', title: '审批已通过，即将进入执行' },
  EXECUTING: { label: '执行中', tone: 'info', title: '执行器正在真实执行' },
  SUCCEEDED: { label: '成功', tone: 'success', title: '真实执行成功，输出已落库' },
  FAILED: { label: '失败', tone: 'error', title: '真实执行失败，错误信息已如实落库' },
  REJECTED: { label: '已驳回', tone: 'neutral', title: '审批驳回：该调用永不执行' },
};

const INVOCATION_STATUS_OPTIONS = [
  { value: '', label: '全部状态' },
  { value: 'PENDING_APPROVAL', label: '待审批' },
  { value: 'APPROVED', label: '已批准' },
  { value: 'EXECUTING', label: '执行中' },
  { value: 'SUCCEEDED', label: '成功' },
  { value: 'FAILED', label: '失败' },
  { value: 'REJECTED', label: '已驳回' },
];

function invocationBadge(status: string) {
  const meta = INVOCATION_STATUS_META[status] ?? { label: status, tone: 'neutral' as const, title: status };
  return (
    <Badge tone={meta.tone} title={meta.title}>
      {meta.label}
    </Badge>
  );
}

function prettyJSON(raw: string | null): string {
  if (!raw) return '';
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
}

/**
 * 工具管理 `/admin/tools`（§7.2）
 *
 * 字段口径：contracts/openapi.yaml#/components/schemas/Tool（即
 * `GET /api/v1/tools` 的真实返回）。
 *
 * §7.2 硬性要求：强调能力边界与风险，而不是只展示工具名称。
 *
 * 真实执行链（严禁 mock）：测试调用走 `POST /api/v1/tools/{id}/invoke`，
 * 服务端按 execution_config.adapter 派发真实执行器（ERP Port / docingest /
 * SHACL / 模型治理网关 / 审计哈希链），结果如实落 runtime.tool_invocations。
 *   - READ / INTERNAL_WRITE：同步返回 200 + 终态记录（含真实输出或真实错误）；
 *   - EXTERNAL_WRITE：返回 202 + 待审批记录，需「审批确认」后才真实执行
 *     （审批驳回则永不执行）。
 * 调用记录区支持 10 行/页 + 状态筛选 + 审批操作（approval.decide 权限门禁）。
 *
 * 插件式设计（§7.2）：工具是插件式设计资产——新建 / 编辑 / 删除（软删）/
 * AI 自动生成 / 测试调用，全部对接后端已实现的 REST 端点。
 */
export function ToolsPage() {
  const [search, setSearch] = useState('');
  const [status, setStatus] = useState('');
  const pager = useCursorPagination();
  const query = useTools({ q: search, status, cursor: pager.cursor ?? undefined });
  const create = useCreateTool();
  const update = useUpdateTool();
  const remove = useDeleteTool();
  const generate = useGenerateTool();
  const invoke = useInvokeTool();

  const [createOpen, setCreateOpen] = useState(false);
  const [createForm, setCreateForm] = useState({
    tool_key: '',
    description: '',
    version: '',
    effect_class: 'READ' as EffectClass,
    capabilities: '',
    input_schema: '{}',
    output_schema: '{}',
  });
  const [createErr, setCreateErr] = useState<string | null>(null);

  const [editTarget, setEditTarget] = useState<ToolInfo | null>(null);
  const [editDesc, setEditDesc] = useState('');

  const [deleteTarget, setDeleteTarget] = useState<ToolInfo | null>(null);

  const [genOpen, setGenOpen] = useState(false);
  const [genForm, setGenForm] = useState({
    instructions: '',
    source_path: '',
    tool_key: '',
    model_id: '',
    max_source_chars: '',
  });

  const [invokeTarget, setInvokeTarget] = useState<ToolInfo | null>(null);
  const [invokeArgs, setInvokeArgs] = useState('{}');
  const [invokeErr, setInvokeErr] = useState<string | null>(null);

  // 调用记录区（10 行/页 + 状态筛选 + cursor 翻页 + 审批操作）。
  const [invToolId, setInvToolId] = useState('');
  const [invStatus, setInvStatus] = useState('');
  const [invCursor, setInvCursor] = useState<string | null>(null);
  const [invCursorStack, setInvCursorStack] = useState<(string | null)[]>([]);
  const [decisionTarget, setDecisionTarget] = useState<{ inv: ToolInvocation; approve: boolean } | null>(null);

  const invocationsQuery = useToolInvocations(invToolId || null, { status: invStatus, cursor: invCursor ?? undefined });
  const approveInv = useApproveToolInvocation();
  const rejectInv = useRejectToolInvocation();

  const resetInvPagination = () => {
    setInvCursor(null);
    setInvCursorStack([]);
  };

  const generated = generate.data;

  // 筛选条件变化必须回到首页：否则会带着旧页游标去请求新筛选结果（假结果）。
  useEffect(() => {
    pager.reset();
  }, [search, status, pager.reset]);

  const openEdit = (item: ToolInfo) => {
    setEditTarget(item);
    setEditDesc(item.description ?? '');
    update.reset();
  };

  const submitCreate = () => {
    setCreateErr(null);
    if (!createForm.tool_key.trim()) {
      setCreateErr('tool_key 必填。');
      return;
    }
    const caps = createForm.capabilities
      .split(',')
      .map((c) => c.trim())
      .filter(Boolean);
    // 服务端以 json.Valid 强校验 input_schema / output_schema：本地先解析，
    // 传入合法 JSON（缺省 nil 会被服务端拒绝为 INVALID_ARGUMENT）。
    let inputSchema: unknown;
    let outputSchema: unknown;
    try {
      inputSchema = JSON.parse(createForm.input_schema.trim() || '{}');
      outputSchema = JSON.parse(createForm.output_schema.trim() || '{}');
    } catch (e) {
      setCreateErr(`输入/输出 Schema 不是合法 JSON：${(e as Error).message}`);
      return;
    }
    create.mutate(
      {
        tool_key: createForm.tool_key.trim(),
        effect_class: createForm.effect_class,
        input_schema: inputSchema,
        output_schema: outputSchema,
        ...(createForm.description.trim() ? { description: createForm.description.trim() } : {}),
        ...(createForm.version.trim() ? { version: createForm.version.trim() } : {}),
        ...(caps.length > 0 ? { capabilities: caps } : {}),
      },
      {
        onSuccess: () => {
          setCreateOpen(false);
          setCreateForm({
            tool_key: '',
            description: '',
            version: '',
            effect_class: 'READ',
            capabilities: '',
            input_schema: '{}',
            output_schema: '{}',
          });
        },
      },
    );
  };

  const submitEdit = () => {
    if (!editTarget) return;
    update.mutate(
      { id: editTarget.id, body: { ...(editDesc.trim() ? { description: editDesc.trim() } : {}) } },
      { onSuccess: () => setEditTarget(null) },
    );
  };

  const submitGen = () => {
    const chars = Number(genForm.max_source_chars);
    const instructions = genForm.instructions.trim();
    const sourcePath = genForm.source_path.trim();
    if (!instructions && !sourcePath) return;
    generate.mutate(
      {
        ...(instructions ? { instructions } : {}),
        ...(sourcePath ? { source_path: sourcePath } : {}),
        ...(genForm.tool_key.trim() ? { tool_key: genForm.tool_key.trim() } : {}),
        ...(genForm.model_id.trim() ? { model_id: genForm.model_id.trim() } : {}),
        ...(Number.isFinite(chars) && chars > 0 ? { max_source_chars: chars } : {}),
      },
      {
        onSuccess: () => {
          setGenOpen(false);
          setGenForm({ instructions: '', source_path: '', tool_key: '', model_id: '', max_source_chars: '' });
        },
      },
    );
  };

  const submitInvoke = () => {
    if (!invokeTarget) return;
    setInvokeErr(null);
    let args: Record<string, unknown>;
    try {
      const parsed = JSON.parse(invokeArgs || '{}');
      if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
        throw new Error('参数必须是 JSON 对象');
      }
      args = parsed as Record<string, unknown>;
    } catch (e) {
      setInvokeErr(`参数不是合法 JSON 对象：${(e as Error).message}`);
      return;
    }
    invoke.mutate({ id: invokeTarget.id, body: { args } });
  };

  const fmtTime = (raw: string | null) => (raw ? new Date(raw).toLocaleString() : '—');

  const invocationColumns: DataTableColumn<ToolInvocation>[] = [
    {
      key: 'created_at',
      header: '发起时间',
      tight: true,
      render: (inv) => (
        <div>
          <div className="text-sm">{fmtTime(inv.created_at)}</div>
          <div className="text-xs muted mono copy-id">{inv.id}</div>
        </div>
      ),
    },
    {
      key: 'tool_key',
      header: '工具',
      tight: true,
      render: (inv) => <span className="mono text-sm">{inv.tool_key}</span>,
    },
    {
      key: 'effect_class',
      header: '副作用',
      tight: true,
      render: (inv) => <MetaBadge map={EFFECT_META} value={inv.effect_class} context="调用记录.副作用等级" />,
    },
    {
      key: 'status',
      header: '状态',
      tight: true,
      render: (inv) => invocationBadge(inv.status),
    },
    {
      key: 'result',
      header: '输出 / 错误',
      render: (inv) => {
        if (inv.status === 'SUCCEEDED' && inv.output_payload) {
          return (
            <details>
              <summary className="text-sm" style={{ cursor: 'pointer' }}>
                查看真实输出
              </summary>
              <pre className="mono text-xs" style={{ whiteSpace: 'pre-wrap', maxHeight: 240, overflow: 'auto' }}>
                {prettyJSON(inv.output_payload)}
              </pre>
            </details>
          );
        }
        if (inv.status === 'FAILED') {
          return (
            <div>
              <Badge tone="error">{inv.error_code ?? 'ERROR'}</Badge>
              <div className="text-xs" style={{ marginTop: 4 }}>
                {inv.error_summary ?? '—'}
              </div>
            </div>
          );
        }
        if (inv.status === 'PENDING_APPROVAL') {
          return <span className="text-sm muted">等待审批确认后真实执行</span>;
        }
        if (inv.status === 'REJECTED') {
          return <span className="text-sm muted">审批驳回{inv.error_summary ? `：${inv.error_summary}` : ''}</span>;
        }
        return <span className="text-sm muted">—</span>;
      },
    },
    {
      key: 'approve',
      header: '审批',
      tight: true,
      render: (inv) => {
        if (inv.status !== 'PENDING_APPROVAL') return <span className="muted text-sm">—</span>;
        return (
          <PermissionGate permission="approval.decide">
            <div className="row" style={{ gap: 4 }}>
              <Button
                variant="primary"
                size="sm"
                icon="check"
                loading={approveInv.isPending && approveInv.variables?.invId === inv.id}
                onClick={() => {
                  setDecisionTarget({ inv, approve: true });
                }}>
                批准
              </Button>
              <Button
                variant="ghost"
                size="sm"
                icon="ban"
                loading={rejectInv.isPending && rejectInv.variables?.invId === inv.id}
                onClick={() => {
                  setDecisionTarget({ inv, approve: false });
                }}>
                驳回
              </Button>
            </div>
          </PermissionGate>
        );
      },
    },
  ];

  const columns: DataTableColumn<ToolInfo>[] = [
    {
      key: 'tool_key',
      header: '工具',
      render: (item) => (
        <div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span style={{ fontWeight: 500 }} className="mono">
              {item.tool_key}
            </span>
            {item.is_builtin ? (
              <Badge tone="info" title="平台内置工具：由平台随数据集登记，仅展示，不可编辑或删除">
                内置
              </Badge>
            ) : null}
          </div>
          <div className="text-xs muted" style={{ marginTop: 4 }}>
            <span className="copy-id">{item.id}</span>
          </div>
          {item.description ? (
            <div className="text-xs muted" style={{ marginTop: 4 }}>
              {item.description}
            </div>
          ) : null}
        </div>
      ),
    },
    {
      key: 'effect_class',
      header: '副作用等级',
      tight: true,
      render: (item) => <MetaBadge map={EFFECT_META} value={item.effect_class} context="工具副作用等级" />,
    },
    {
      key: 'latest_version_status',
      header: '版本状态',
      tight: true,
      render: (item) => {
        const meta = versionStatusBadge(item.latest_version_status, '工具列表.版本状态');
        return (
          <Badge tone={meta.tone} title={meta.title}>
            {meta.label}
          </Badge>
        );
      },
    },
    {
      key: 'latest_version',
      header: '最新版本',
      tight: true,
      render: (item) => <span className="mono">{item.latest_version ?? '—'}</span>,
    },
    {
      key: 'actions',
      header: '操作',
      tight: true,
      render: (item) => {
        // 平台内置工具（文档读取 / 本体校验 / 图谱装载 / 模型调用治理网关 /
        // ERP 适配 / 审计链 / 证据哈希链）：仅登记展示，不允许编辑或删除。
        // 后端 store 层同样拦截（PATCH/DELETE 一律 409），不只靠这里隐藏按钮。
        if (item.is_builtin) {
          return (
            <span
              className="muted text-sm"
              title="平台内置工具：仅登记展示，不允许编辑 / 删除（服务端同样返回 409）">
              内置 · 不可修改
            </span>
          );
        }
        if (item.status === 'DELETED') return <span className="muted text-sm">已删除</span>;
        const isExternalWrite = item.effect_class === 'EXTERNAL_WRITE';
        return (
          <div className="row">
            <PermissionGate permission="admin.tool.write">
              <Button variant="ghost" size="sm" icon="pencil" onClick={() => openEdit(item)}>
                编辑
              </Button>
            </PermissionGate>
            <PermissionGate permission="admin.tool.write">
              <Button
                variant="secondary"
                size="sm"
                icon="play"
                title={
                  isExternalWrite
                    ? '外部写入工具：发起后进入待审批状态，审批确认后才真实执行'
                    : '真实执行（结果如实落调用记录）'
                }
                onClick={() => {
                  setInvokeTarget(item);
                  setInvokeArgs('{}');
                  setInvokeErr(null);
                  invoke.reset();
                  setInvToolId(item.id);
                  resetInvPagination();
                }}>
                测试调用
              </Button>
            </PermissionGate>
            <PermissionGate permission="admin.tool.write">
              <Button
                variant="ghost"
                size="sm"
                icon="ban"
                loading={remove.isPending && remove.variables === item.id}
                onClick={() => setDeleteTarget(item)}>
                删除
              </Button>
            </PermissionGate>
          </div>
        );
      },
    },
  ];

  return (
    <div className="page page--wide">
      <PageHeader
        icon="wrench"
        title="工具管理"
        subtitle="Tool Registry 强调能力边界与风险：输入/输出 Schema、副作用等级、版本与校验状态。"
        actions={
          <PermissionGate permission="admin.tool.write">
            <div className="row">
              <Button variant="secondary" icon="sparkles" onClick={() => setGenOpen(true)}>
                AI 自动生成
              </Button>
              <Button variant="primary" icon="plus" onClick={() => setCreateOpen(true)}>
                新建工具
              </Button>
            </div>
          </PermissionGate>
        }
      />

      <div className="banner banner--info" style={{ marginBottom: 16 }} role="status">
        <span className="banner__icon" aria-hidden="true">
          <Icon name="info" size={16} />
        </span>
        <span>
          测试调用为<strong>真实执行</strong>：服务端按工具执行契约派发真实执行器（ERP 端口 / 文档解析 / SHACL
          校验 / 模型治理网关 / 审计哈希链），结果如实写入调用记录。外部写入（EXTERNAL_WRITE）工具需
          <strong>审批确认</strong>后才会触达外部系统；无真实后端的能力（OCR / 预算系统）明确报不可用，不降级、不造假。
        </span>
      </div>

      <ListFilterBar
        searchValue={search}
        onSearchChange={setSearch}
        searchPlaceholder="搜索工具键 / 说明…"
        statusValue={status}
        onStatusChange={setStatus}
        statusOptions={STATUS_OPTIONS}
      />

      <DataTable
        caption="工具列表"
        columns={columns}
        rows={query.data?.items ?? []}
        rowKey={(item) => item.id}
        loading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        emptyTitle="暂无工具"
        emptyDescription="可通过「新建工具」或「AI 自动生成」创建工具资产。"
        nextCursor={query.data?.next_cursor ?? null}
        hasPrevPage={pager.hasPrevPage}
        onPrevPage={pager.goPrev}
        onNextPage={() => pager.goNext(query.data?.next_cursor ?? null)}
      />

      {/* 调用记录：10 行/页 + 状态筛选 + 审批操作 */}
      <section className="section" style={{ marginTop: 24 }}>
        <div className="row" style={{ justifyContent: 'space-between', marginBottom: 12 }}>
          <h2 className="text-md" style={{ margin: 0 }}>
            调用记录
          </h2>
          <div className="row" style={{ gap: 8 }}>
            <Select
              aria-label="选择工具"
              value={invToolId}
              options={[
                { value: '', label: '选择工具…' },
                ...(query.data?.items ?? []).map((t) => ({ value: t.id, label: t.tool_key })),
              ]}
              onChange={(e) => {
                setInvToolId(e.target.value);
                resetInvPagination();
              }}
              style={{ minWidth: 220 }}
            />
            <Select
              aria-label="按状态筛选"
              value={invStatus}
              options={INVOCATION_STATUS_OPTIONS}
              onChange={(e) => {
                setInvStatus(e.target.value);
                resetInvPagination();
              }}
              style={{ minWidth: 140 }}
            />
          </div>
        </div>
        {invToolId ? (
          <>
            <DataTable
              caption="工具调用记录"
              columns={invocationColumns}
              rows={invocationsQuery.data?.items ?? []}
              rowKey={(inv) => inv.id}
              loading={invocationsQuery.isLoading}
              error={invocationsQuery.error}
              onRetry={() => void invocationsQuery.refetch()}
              emptyTitle="该工具暂无调用记录"
            />
            <div className="row section" style={{ gap: 8 }}>
              <Button
                variant="ghost"
                size="sm"
                disabled={invCursorStack.length === 0}
                onClick={() => {
                  const stack = [...invCursorStack];
                  const prev = stack.pop() ?? null;
                  setInvCursorStack(stack);
                  setInvCursor(prev);
                }}>
                上一页
              </Button>
              <Button
                variant="ghost"
                size="sm"
                disabled={!invocationsQuery.data?.next_cursor}
                onClick={() => {
                  const cur = invocationsQuery.data?.next_cursor;
                  if (!cur) return;
                  setInvCursorStack([...invCursorStack, invCursor]);
                  setInvCursor(cur);
                }}>
                下一页
              </Button>
              <span className="text-xs muted">
                每页 10 行；记录含真实输出与真实错误，审批操作受 approval.decide 权限控制。
              </span>
            </div>
          </>
        ) : (
          <p className="text-sm muted">请选择一个工具以查看其真实调用记录（待审批的外部写入可在此审批）。</p>
        )}
      </section>

      {/* 新建工具 */}
      <Dialog open={createOpen} onClose={() => setCreateOpen(false)} title="新建工具">
        <form
          className="dialog-form"
          onSubmit={(e) => {
            e.preventDefault();
            submitCreate();
          }}>
          <Field label="工具键（tool_key）" required hint="英文小写短横线命名，创建后不可变。">
            <Input
              value={createForm.tool_key}
              placeholder="如 erp-expense-query"
              onChange={(e) => setCreateForm({ ...createForm, tool_key: e.target.value })}
              required
            />
          </Field>
          <Field label="副作用等级（effect_class）" required hint="创建后不可变；决定能力边界与风险。">
            <Select
              value={createForm.effect_class}
              options={EFFECT_OPTIONS}
              onChange={(e) => setCreateForm({ ...createForm, effect_class: e.target.value as EffectClass })}
            />
          </Field>
          <Field label="说明">
            <Textarea value={createForm.description} onChange={(e) => setCreateForm({ ...createForm, description: e.target.value })} />
          </Field>
          <Field label="能力标签（capabilities）" hint="逗号分隔，如 erp,query。">
            <Input value={createForm.capabilities} onChange={(e) => setCreateForm({ ...createForm, capabilities: e.target.value })} />
          </Field>
          <Field label="版本（version）" hint="留空由服务端生成首个版本。">
            <Input value={createForm.version} onChange={(e) => setCreateForm({ ...createForm, version: e.target.value })} />
          </Field>
          <Field label="输入 Schema（input_schema，JSON）" required hint="必填；描述入参结构，测试调用与真实执行均以此校验，至少为 {}。">
            <Textarea
              value={createForm.input_schema}
              onChange={(e) => setCreateForm({ ...createForm, input_schema: e.target.value })}
              rows={4}
            />
          </Field>
          <Field label="输出 Schema（output_schema，JSON）" required hint="必填；描述返回结构，至少为 {}。">
            <Textarea
              value={createForm.output_schema}
              onChange={(e) => setCreateForm({ ...createForm, output_schema: e.target.value })}
              rows={4}
            />
          </Field>
          <div className="dialog-actions">
            <Button type="button" variant="ghost" onClick={() => setCreateOpen(false)}>
              取消
            </Button>
            <Button type="submit" variant="primary" disabled={create.isPending}>
              {create.isPending ? '创建中…' : '创建'}
            </Button>
          </div>
          {createErr ? (
            <p className="text-sm" style={{ color: 'var(--color-error)' }}>
              {createErr}
            </p>
          ) : null}
          {create.error ? (
            <p className="text-sm" style={{ color: 'var(--color-error)' }}>
              创建失败：{(create.error as Error).message}
            </p>
          ) : null}
        </form>
      </Dialog>

      {/* 编辑工具 */}
      <Dialog open={editTarget !== null} onClose={() => setEditTarget(null)} title="编辑工具">
        <form
          className="dialog-form"
          onSubmit={(e) => {
            e.preventDefault();
            submitEdit();
          }}>
          <Field label="工具键（tool_key）" hint="不可变：能力标识与副作用等级由版本决定。">
            <Input value={editTarget?.tool_key ?? ''} readOnly disabled />
          </Field>
          <Field label="副作用等级">
            <Input value={editTarget?.effect_class ?? '—'} readOnly disabled />
          </Field>
          <Field label="说明">
            <Textarea value={editDesc} onChange={(e) => setEditDesc(e.target.value)} />
          </Field>
          <div className="dialog-actions">
            <Button type="button" variant="ghost" onClick={() => setEditTarget(null)}>
              取消
            </Button>
            <Button type="submit" variant="primary" disabled={update.isPending}>
              {update.isPending ? '保存中…' : '保存'}
            </Button>
          </div>
          {update.error ? (
            <p className="text-sm" style={{ color: 'var(--color-error)' }}>
              保存失败：{(update.error as Error).message}
            </p>
          ) : null}
        </form>
      </Dialog>

      {/* 删除工具（软删除） */}
      <ConfirmDialog
        open={deleteTarget !== null}
        title="删除工具"
        confirmLabel="删除工具"
        danger
        target={
          deleteTarget ? (
            <>
              <span className="mono">{deleteTarget.tool_key}</span>（<span className="copy-id">{deleteTarget.id}</span>）
            </>
          ) : null
        }
        impact="软删除（status → DELETED）：工具不再出现在列表与选择器中，但审计链完整保留。"
        scope="当前租户"
        reversible={false}
        requiresApproval={false}
        verification="删除后在列表确认该工具消失；审计事件 ToolDeleted 可在审计与证据页核对。"
        confirmPhrase={deleteTarget?.tool_key}
        busy={remove.isPending}
        error={remove.error}
        onCancel={() => setDeleteTarget(null)}
        onConfirm={() => {
          if (deleteTarget) remove.mutate(deleteTarget.id, { onSuccess: () => setDeleteTarget(null) });
        }}
      />

      {/* AI 自动生成工具 */}
      <Dialog open={genOpen} onClose={() => setGenOpen(false)} title="AI 自动生成工具" wide>
        <form
          className="dialog-form"
          onSubmit={(e) => {
            e.preventDefault();
            submitGen();
          }}>
          <Field label="需求说明（instructions）" hint="与制度文档路径至少填一项。">
            <Textarea
              value={genForm.instructions}
              placeholder="如：从 ERP 按费用单号查询报销单详情与发票状态"
              onChange={(e) => setGenForm({ ...genForm, instructions: e.target.value })}
            />
          </Field>
          <Field
            label="制度文档路径（source_path）"
            hint="相对制度文档根目录（默认 docs/资料）的文件或目录，如 费用报销制度；越出根目录会被拒绝。">
            <Input
              value={genForm.source_path}
              placeholder="如 费用报销制度"
              onChange={(e) => setGenForm({ ...genForm, source_path: e.target.value })}
            />
          </Field>
          <Field label="指定工具键（tool_key）" hint="留空由模型生成。">
            <Input value={genForm.tool_key} onChange={(e) => setGenForm({ ...genForm, tool_key: e.target.value })} />
          </Field>
          <Field label="指定模型" hint="从「模型与推理」页登记的供应商端点中选择；留空则按能力择优。">
            <ModelSelect value={genForm.model_id} onChange={(v) => setGenForm({ ...genForm, model_id: v })} />
          </Field>
          <Field label="源字符上限（max_source_chars）" hint="可选；超限部分会被截断并标注。">
            <Input
              type="number"
              min={1}
              value={genForm.max_source_chars}
              onChange={(e) => setGenForm({ ...genForm, max_source_chars: e.target.value })}
            />
          </Field>
          <div className="dialog-actions">
            <Button type="button" variant="ghost" onClick={() => setGenOpen(false)}>
              取消
            </Button>
            <Button type="submit" variant="primary" disabled={generate.isPending || (!genForm.instructions.trim() && !genForm.source_path.trim())}>
              {generate.isPending ? '生成中…' : '生成草稿'}
            </Button>
          </div>
          {generate.error ? (
            <p className="text-sm" style={{ color: 'var(--color-error)' }}>
              生成失败：{(generate.error as Error).message}
            </p>
          ) : null}
          {generated ? (
            <p className="text-sm muted">
              已生成草稿工具 <span className="mono">{generated.tool_key}</span>（<span className="copy-id">{generated.id}</span>）。
            </p>
          ) : null}
        </form>
      </Dialog>

      {/* 测试调用（真实执行链：READ/INTERNAL_WRITE 同步返回；EXTERNAL_WRITE 转待审批） */}
      <Dialog open={invokeTarget !== null} onClose={() => setInvokeTarget(null)} title="测试调用工具" wide>
        <form
          className="dialog-form"
          onSubmit={(e) => {
            e.preventDefault();
            submitInvoke();
          }}>
          <Field
            label="工具"
            hint={
              invokeTarget?.effect_class === 'EXTERNAL_WRITE'
                ? '外部写入工具：发起后进入待审批，审批确认后才真实执行。'
                : '真实执行：结果与错误如实写入调用记录。'
            }>
            <Input value={invokeTarget?.tool_key ?? ''} readOnly disabled />
          </Field>
          <Field label="调用参数（args，JSON 对象）" hint="参数须符合该工具的 input_schema，由服务端校验。">
            <Textarea value={invokeArgs} onChange={(e) => setInvokeArgs(e.target.value)} />
          </Field>
          <div className="dialog-actions">
            <Button type="button" variant="ghost" onClick={() => setInvokeTarget(null)}>
              关闭
            </Button>
            <Button type="submit" variant="primary" disabled={invoke.isPending}>
              {invoke.isPending ? '调用中…' : '发起测试调用'}
            </Button>
          </div>
          {invokeErr ? (
            <p className="text-sm" style={{ color: 'var(--color-error)' }}>
              {invokeErr}
            </p>
          ) : null}
          {invoke.error ? <ErrorState error={invoke.error} compact /> : null}
          {invoke.isSuccess && invoke.data ? (
            (() => {
              const res = invoke.data as InvokeToolResponse;
              const inv = res.invocation;
              return (
                <div style={{ marginTop: 8 }}>
                  <div className="row" style={{ gap: 8 }}>
                    {invocationBadge(inv.status)}
                    <span className="text-sm muted">调用记录 {inv.id}</span>
                  </div>
                  {res.message ? <p className="text-sm muted" style={{ marginTop: 4 }}>{res.message}</p> : null}
                  {inv.status === 'FAILED' ? (
                    <p className="text-sm" style={{ color: 'var(--color-error)', marginTop: 4 }}>
                      {inv.error_code ?? 'ERROR'}：{inv.error_summary ?? '—'}
                    </p>
                  ) : null}
                  {inv.output_payload ? (
                    <pre className="mono text-xs" style={{ whiteSpace: 'pre-wrap', maxHeight: 260, overflow: 'auto' }}>
                      {prettyJSON(inv.output_payload)}
                    </pre>
                  ) : null}
                  <p className="text-xs muted" style={{ marginTop: 4 }}>
                    完整调用记录见下方「调用记录」区块。
                  </p>
                </div>
              );
            })()
          ) : null}
        </form>
      </Dialog>

      {/* 审批确认 / 驳回（EXTERNAL_WRITE 调用的门禁动作） */}
      <ConfirmDialog
        open={decisionTarget !== null}
        title={decisionTarget?.approve ? '批准外部写入' : '驳回外部写入'}
        confirmLabel={decisionTarget?.approve ? '批准并执行' : '确认驳回'}
        target={decisionTarget ? `工具 ${decisionTarget.inv.tool_key} 的调用记录 ${decisionTarget.inv.id}` : ''}
        impact={
          decisionTarget?.approve
            ? '批准后服务端将携带审批凭据真实执行该调用并触达外部系统（CAS 状态守卫，不会重复执行）。'
            : '驳回后该调用永不执行；驳回原因将写入调用记录与审计。'
        }
        onCancel={() => setDecisionTarget(null)}
        onConfirm={() => {
          if (!decisionTarget) return;
          const { inv, approve } = decisionTarget;
          if (approve) {
            approveInv.mutate({ toolId: inv.tool_id, invId: inv.id });
          } else {
            rejectInv.mutate({ toolId: inv.tool_id, invId: inv.id });
          }
          setDecisionTarget(null);
        }}
      />
    </div>
  );
}
