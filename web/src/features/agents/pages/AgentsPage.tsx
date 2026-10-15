import { useEffect, useState } from 'react';
import { Badge, Button, Dialog, Field, Input, PageHeader, Textarea } from '@/components/ui';
import { ConfirmDialog } from '@/components/confirm-dialog';
import { ModelSelect } from '@/components/model-select';
import { DataTable, type DataTableColumn } from '@/components/data-table';
import { ErrorState } from '@/components/error-state';
import { PermissionGate } from '@/components/permission-gate';
import { versionStatusBadge } from '@/components/status-badge';
import { ListFilterBar, FILTER_ALL_OPTION } from '@/components/list-filter-bar';
import { useCursorPagination } from '@/hooks/use-cursor-pagination';
import type { AgentInfo } from '@/types';
import {
  useAgents,
  useCreateAgent,
  useDeleteAgent,
  useGenerateAgent,
  usePublishAgent,
  useUpdateAgent,
} from '../api';
import { Icon } from '@/components/icon';

/**
 * Agent 管理 `/admin/agents`（§7.1）
 *
 * 字段口径：contracts/openapi.yaml#/components/schemas/Agent（即
 * `GET /api/v1/agents` 的真实返回），**不是**设计稿中的完整 Agent 视图。
 *
 * 已实现的列表字段：agent_key / domain / description / status /
 *   latest_version / latest_version_status。
 * 尚未由列表接口提供的 §7.1 要求字段（模型策略、可用工具、关联本体与工作流、
 *   最近评估结果）——这些数据存在于 `registry.agent_versions` 的
 *   dependency_manifest / prompt_template 中，但列表接口未做投影。
 *   按不存在字段渲染只会得到空白或在 `.map` 处崩溃，故此处**不渲染**，
 *   并在页脚显式声明缺口（自查矩阵缺陷 #19 关联项）。
 *
 * 插件式设计（§7.1）：Agent 是插件式设计资产——新建 / 编辑 / 删除（软删，
 * 保留审计链）/ AI 自动生成 / 发布版本，全部对接后端已实现的 REST 端点。
 * AI 自动生成依赖已装配的真实模型端点；未装配时服务端返回 503 并说明原因，
 * 前端如实展示，不做本地兜底或假生成。
 *
 * 内置智能体（is_builtin=true）：平台自身能力——AI 自动生成本体 / Agent /
 * 工具 / 工作流、本体校验（SHACL）、运行编排（Harness）、ERP 操作对账、
 * 模型调用治理网关——随数据集登记在册，标「内置」徽标且**不可修改**：
 * 本页隐藏编辑 / 发布 / 删除入口，后端对 PATCH / DELETE / publish 一律 409。
 * 用户自建 Agent（is_builtin=false）行为不变，可编辑、发布与软删除。
 */
/** 列表筛选状态词表（registry.agents.status 的真实取值集合，10 行/页）。 */
const STATUS_OPTIONS = [
  FILTER_ALL_OPTION,
  { value: 'ACTIVE', label: '启用' },
  { value: 'DISABLED', label: '停用' },
  { value: 'DELETED', label: '已删除' },
];

export function AgentsPage() {
  const [search, setSearch] = useState('');
  const [status, setStatus] = useState('');
  const pager = useCursorPagination();
  const query = useAgents({ q: search, status, cursor: pager.cursor ?? undefined });
  const publish = usePublishAgent();
  const create = useCreateAgent();
  const update = useUpdateAgent();
  const remove = useDeleteAgent();
  const generate = useGenerateAgent();

  const [createOpen, setCreateOpen] = useState(false);
  const [createForm, setCreateForm] = useState({
    agent_key: '',
    domain: '',
    description: '',
    version: '',
    input_schema: '{}',
    output_schema: '{}',
  });
  const [createErr, setCreateErr] = useState<string | null>(null);

  const [editTarget, setEditTarget] = useState<AgentInfo | null>(null);
  const [editForm, setEditForm] = useState({ domain: '', description: '' });

  const [deleteTarget, setDeleteTarget] = useState<AgentInfo | null>(null);

  const [genOpen, setGenOpen] = useState(false);
  const [genForm, setGenForm] = useState({
    instructions: '',
    source_path: '',
    agent_key: '',
    domain: '',
    model_id: '',
    max_source_chars: '',
  });

  const generated = generate.data;

  // 筛选条件变化必须回到首页：否则会带着旧页游标去请求新筛选结果（假结果）。
  useEffect(() => {
    pager.reset();
  }, [search, status, pager.reset]);

  const openEdit = (item: AgentInfo) => {
    setEditTarget(item);
    setEditForm({ domain: item.domain ?? '', description: item.description ?? '' });
    update.reset();
  };

  const submitCreate = () => {
    setCreateErr(null);
    if (!createForm.agent_key.trim() || !createForm.domain.trim()) {
      setCreateErr('agent_key 与 domain 必填。');
      return;
    }
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
        agent_key: createForm.agent_key.trim(),
        domain: createForm.domain.trim(),
        input_schema: inputSchema,
        output_schema: outputSchema,
        ...(createForm.description.trim() ? { description: createForm.description.trim() } : {}),
        ...(createForm.version.trim() ? { version: createForm.version.trim() } : {}),
      },
      {
        onSuccess: () => {
          setCreateOpen(false);
          setCreateForm({ agent_key: '', domain: '', description: '', version: '', input_schema: '{}', output_schema: '{}' });
        },
      },
    );
  };

  const submitEdit = () => {
    if (!editTarget) return;
    update.mutate(
      {
        id: editTarget.id,
        body: {
          ...(editForm.domain.trim() ? { domain: editForm.domain.trim() } : {}),
          ...(editForm.description.trim() ? { description: editForm.description.trim() } : {}),
        },
      },
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
        ...(genForm.agent_key.trim() ? { agent_key: genForm.agent_key.trim() } : {}),
        ...(genForm.domain.trim() ? { domain: genForm.domain.trim() } : {}),
        ...(genForm.model_id.trim() ? { model_id: genForm.model_id.trim() } : {}),
        ...(Number.isFinite(chars) && chars > 0 ? { max_source_chars: chars } : {}),
      },
      {
        onSuccess: () => {
          setGenOpen(false);
          setGenForm({ instructions: '', source_path: '', agent_key: '', domain: '', model_id: '', max_source_chars: '' });
        },
      },
    );
  };

  const columns: DataTableColumn<AgentInfo>[] = [
    {
      key: 'agent_key',
      header: 'Agent',
      render: (item) => (
        <div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span style={{ fontWeight: 500 }} className="mono">
              {item.agent_key}
            </span>
            {item.is_builtin ? (
              <Badge tone="info" title="平台内置智能体：由平台随数据集登记，仅展示，不可修改">
                内置
              </Badge>
            ) : null}
          </div>
          <div className="text-xs muted" style={{ marginTop: 4 }}>
            <span className="copy-id">{item.id}</span> · 业务域 {item.domain}
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
      key: 'latest_version_status',
      header: '发布状态',
      tight: true,
      render: (item) => {
        const meta = versionStatusBadge(item.latest_version_status, 'Agent 列表.发布状态');
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
      render: (item) =>
        item.is_builtin ? (
          <span
            className="muted text-sm"
            title="平台内置智能体：仅登记展示，不允许编辑 / 发布 / 删除（服务端同样返回 409）">
            内置 · 不可修改
          </span>
        ) : item.status === 'DELETED' ? (
          <span className="muted text-sm">已删除</span>
        ) : (
          <div className="row">
            <PermissionGate permission="admin.agent.write">
              <Button variant="ghost" size="sm" icon="pencil" onClick={() => openEdit(item)}>
                编辑
              </Button>
            </PermissionGate>
            <PermissionGate permission="admin.agent.publish">
              <Button
                variant="primary"
                size="sm"
                disabled={item.latest_version_status === 'PUBLISHED'}
                loading={publish.isPending && publish.variables === item.id}
                onClick={() => publish.mutate(item.id)}
                icon="rocket">
                发布
              </Button>
            </PermissionGate>
            <PermissionGate permission="admin.agent.write">
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
        ),
    },
  ];

  return (
    <div className="page page--wide">
      <PageHeader
        icon="bot"
        title="Agent 管理"
        subtitle="Prompt 编辑器必须与运行时真正使用的模板版本关联；发布必须经过校验与质量门禁，未通过门禁的候选版本不得进入生产。"
        actions={
          <PermissionGate permission="admin.agent.write">
            <div className="row">
              <Button variant="secondary" icon="sparkles" onClick={() => setGenOpen(true)}>
                AI 自动生成
              </Button>
              <Button variant="primary" icon="plus" onClick={() => setCreateOpen(true)}>
                新建 Agent
              </Button>
            </div>
          </PermissionGate>
        }
      />

      {publish.isError ? (
        <div style={{ marginBottom: 16 }}>
          <ErrorState error={publish.error} compact />
        </div>
      ) : null}
      {publish.isSuccess ? (
        <div className="banner banner--info" style={{ marginBottom: 16 }} role="status">
          <span className="banner__icon" aria-hidden="true">
            <Icon name="play" size={16} />
          </span>
          <span>
            Agent 已发布：状态 <strong>{publish.data?.status ?? 'PUBLISHED'}</strong>，版本{' '}
            <span className="mono copy-id">{publish.data?.version_id ?? '—'}</span>。
            {' '}该接口为同步返回，发布结论已在本次响应中确定，无需再跟踪任务。
          </span>
        </div>
      ) : null}

      {generated ? (
        <div className="banner banner--neutral" style={{ marginBottom: 16 }} role="status">
          <span>
            已由需求 / 制度文档生成草稿 Agent <strong>{generated.agent_key}</strong>
            （<span className="mono copy-id">{generated.id}</span>）。
            {generated.generation
              ? ` 源文件 ${generated.generation.source_files?.length ?? 0} 份、${generated.generation.source_chars ?? 0} 字符${
                  generated.generation.truncated ? '（已截断）' : ''
                }。`
              : null}
            请完成校验后发布。
          </span>
        </div>
      ) : null}

      <ListFilterBar
        searchValue={search}
        onSearchChange={setSearch}
        searchPlaceholder="搜索 Agent 键 / 业务域 / 说明…"
        statusValue={status}
        onStatusChange={setStatus}
        statusOptions={STATUS_OPTIONS}
      />

      <DataTable
        caption="Agent 列表"
        columns={columns}
        rows={query.data?.items ?? []}
        rowKey={(item) => item.id}
        loading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        emptyTitle="暂无 Agent"
        emptyDescription="可通过「新建 Agent」或「AI 自动生成」创建 Agent 资产。"
        nextCursor={query.data?.next_cursor ?? null}
        hasPrevPage={pager.hasPrevPage}
        onPrevPage={pager.goPrev}
        onNextPage={() => pager.goNext(query.data?.next_cursor ?? null)}
      />

      <div className="banner banner--warning section" style={{ marginTop: 16 }} role="status">
        <span className="banner__icon" aria-hidden="true">
          <Icon name="alertTriangle" size={16} />
        </span>
        <span>
          <strong>部分字段暂缺：</strong>模型策略、可用工具、关联本体与工作流、最近评估结果
          尚未由后端接口投影返回，本页不展示这些列（不用占位假数据填充）。
        </span>
      </div>

      {/* 新建 Agent */}
      <Dialog open={createOpen} onClose={() => setCreateOpen(false)} title="新建 Agent">
        <form
          className="dialog-form"
          onSubmit={(e) => {
            e.preventDefault();
            submitCreate();
          }}>
          <Field label="Agent 键（agent_key）" required hint="英文小写短横线命名，创建后不可变。">
            <Input
              value={createForm.agent_key}
              placeholder="如 expense-audit"
              onChange={(e) => setCreateForm({ ...createForm, agent_key: e.target.value })}
              required
            />
          </Field>
          <Field label="业务域（domain）" required hint="如 finance / contract / hr。">
            <Input value={createForm.domain} onChange={(e) => setCreateForm({ ...createForm, domain: e.target.value })} required />
          </Field>
          <Field label="版本（version）" hint="留空由服务端生成首个 DRAFT 版本。">
            <Input value={createForm.version} onChange={(e) => setCreateForm({ ...createForm, version: e.target.value })} />
          </Field>
          <Field label="说明">
            <Textarea value={createForm.description} onChange={(e) => setCreateForm({ ...createForm, description: e.target.value })} />
          </Field>
          <Field label="输入 Schema（input_schema，JSON）" required hint="必填；描述该 Agent 的输入结构，至少为 {}。">
            <Textarea
              value={createForm.input_schema}
              onChange={(e) => setCreateForm({ ...createForm, input_schema: e.target.value })}
              rows={4}
            />
          </Field>
          <Field label="输出 Schema（output_schema，JSON）" required hint="必填；描述该 Agent 的输出结构，至少为 {}。">
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

      {/* 编辑 Agent */}
      <Dialog open={editTarget !== null} onClose={() => setEditTarget(null)} title="编辑 Agent">
        <form
          className="dialog-form"
          onSubmit={(e) => {
            e.preventDefault();
            submitEdit();
          }}>
          <Field label="Agent 键（agent_key）" hint="不可变：版本升级请通过发布创建新版本。">
            <Input value={editTarget?.agent_key ?? ''} readOnly disabled />
          </Field>
          <Field label="业务域（domain）">
            <Input value={editForm.domain} onChange={(e) => setEditForm({ ...editForm, domain: e.target.value })} />
          </Field>
          <Field label="说明">
            <Textarea value={editForm.description} onChange={(e) => setEditForm({ ...editForm, description: e.target.value })} />
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

      {/* 删除 Agent（软删除） */}
      <ConfirmDialog
        open={deleteTarget !== null}
        title="删除 Agent"
        confirmLabel="删除 Agent"
        danger
        target={
          deleteTarget ? (
            <>
              <span className="mono">{deleteTarget.agent_key}</span>（<span className="copy-id">{deleteTarget.id}</span>）
            </>
          ) : null
        }
        impact="软删除（status → DELETED）：资产不再出现在列表与选择器中，但审计链完整保留。"
        scope="当前租户"
        reversible={false}
        requiresApproval={false}
        verification="删除后在列表确认该 Agent 消失；审计事件 AgentDeleted 可在审计与证据页核对。"
        confirmPhrase={deleteTarget?.agent_key}
        busy={remove.isPending}
        error={remove.error}
        onCancel={() => setDeleteTarget(null)}
        onConfirm={() => {
          if (deleteTarget) remove.mutate(deleteTarget.id, { onSuccess: () => setDeleteTarget(null) });
        }}
      />

      {/* AI 自动生成 Agent */}
      <Dialog open={genOpen} onClose={() => setGenOpen(false)} title="AI 自动生成 Agent" wide>
        <form
          className="dialog-form"
          onSubmit={(e) => {
            e.preventDefault();
            submitGen();
          }}>
          <Field label="需求说明（instructions）" hint="与制度文档路径至少填一项。">
            <Textarea
              value={genForm.instructions}
              placeholder="如：根据费用报销制度，设计一个核对报销单与发票一致性的 Agent"
              onChange={(e) => setGenForm({ ...genForm, instructions: e.target.value })}
            />
          </Field>
          <Field
            label="制度文档路径（source_path）"
            hint="相对制度文档根目录（默认 docs/资料）的文件或目录，如 费用报销制度；越出根目录会被拒绝。">
            <Input
              value={genForm.source_path}
              placeholder="如 费用报销制度 或 合同管理制度/合同管理细则.docx"
              onChange={(e) => setGenForm({ ...genForm, source_path: e.target.value })}
            />
          </Field>
          <Field label="指定 Agent 键（agent_key）" hint="留空由模型生成。">
            <Input value={genForm.agent_key} onChange={(e) => setGenForm({ ...genForm, agent_key: e.target.value })} />
          </Field>
          <Field label="业务域（domain）" hint="留空由模型生成。">
            <Input value={genForm.domain} onChange={(e) => setGenForm({ ...genForm, domain: e.target.value })} />
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
        </form>
      </Dialog>
    </div>
  );
}
