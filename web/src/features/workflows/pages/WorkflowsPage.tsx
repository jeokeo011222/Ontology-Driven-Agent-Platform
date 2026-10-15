import { useEffect, useState } from 'react';
import { Badge, Button, Dialog, Field, Input, PageHeader, Textarea } from '@/components/ui';
import { ConfirmDialog } from '@/components/confirm-dialog';
import { ModelSelect } from '@/components/model-select';
import { DataTable, type DataTableColumn } from '@/components/data-table';
import { ErrorState } from '@/components/error-state';
import { PermissionGate } from '@/components/permission-gate';
import { MetaBadge, versionStatusBadge, type BadgeMeta } from '@/components/status-badge';
import { ListFilterBar, FILTER_ALL_OPTION } from '@/components/list-filter-bar';
import { useCursorPagination } from '@/hooks/use-cursor-pagination';
import { formatDateTime } from '@/lib/format';
import type { PlanTemplate, WorkflowInfo } from '@/types';
import {
  useCreateWorkflow,
  useDeleteWorkflow,
  useGenerateWorkflow,
  usePlanTemplates,
  usePublishWorkflow,
  useUpdateWorkflow,
  useWorkflows,
} from '../api';
import { Icon } from '@/components/icon';

/**
 * 工作流模板生命周期词表（契约 PlanTemplateState）。
 * 由后端按最新修订的 validation_status 归一为大写枚举：
 * VALID→VALIDATED、INVALID→REJECTED、其余→DRAFT。
 */
const STATE_META: Record<PlanTemplate['state'], BadgeMeta> = {
  DRAFT: { label: '草稿', tone: 'neutral' },
  VALIDATED: { label: '已验证', tone: 'success' },
  REJECTED: { label: '已驳回', tone: 'error' },
};

/** §7.3 工作流节点类型（结构化节点编辑器，而非自由拖拽编排器） */
const NODE_KIND_LABEL: Record<string, string> = {
  READ_OBJECT: '读取业务对象',
  SEMANTIC_QUERY: '语义查询',
  ONTOLOGY_VALIDATE: '本体验证',
  RULE_EVALUATE: '业务规则评估',
  AGENT_PLAN: 'Agent 规划',
  TOOL_CALL: '工具调用',
  PERMISSION_CHECK: '权限检查',
  APPROVAL: '人工审批',
  ERP_OPERATION: 'ERP 操作',
  RECONCILE: '结果核验/对账',
  EVIDENCE: '证据生成',
  BRANCH: '条件分支',
  FAILURE_HANDLING: '失败处理与人工接管',
};

const DEFAULT_DEFINITION = '{\n  "nodes": [\n    { "id": "start", "type": "start", "name": "开始" },\n    { "id": "end", "type": "end", "name": "结束" }\n  ],\n  "edges": [\n    { "from": "start", "to": "end" }\n  ]\n}';

/**
 * 工作流与 Harness `/admin/workflows`（§7.3）
 *
 * 要求：首期采用结构化节点编辑器 + 表单配置；
 * 工作流发布采用**不可变版本**，对已发布工作流的修改必须创建新版本并经过测试与门禁，
 * 不能直接修改正在运行的版本定义。
 * 已冻结/已发布的工作流在 UI 中只读，编辑入口禁用并引导「创建新版本」（§13.2）。
 *
 * 两类资产：
 *   1）**插件式工作流**（`/api/v1/workflows`，registry.workflows）——与 Agent / Tool 完全
 *      同构：新建 / 编辑 / 删除（软删）/ AI 自动生成 / 发布。workflow 以
 *      plugin_kind='WORKFLOW' 登记为插件资产（§7.3 插件式设计）。
 *      其中 `is_builtin=true` 的为**平台内置工作流**（Harness 运行编排 / 在途操作对账 /
 *      本体版本生命周期），由平台随数据集登记，仅展示、不可修改；后端对内置项的
 *      PATCH / DELETE / 发布一律返回 409。
 *   2）**计划模板资产**（`/api/v1/plan-templates`）——模板资产与执行计划共用
 *      planning.plans / planning.plan_revisions，以 status='TEMPLATE' 判别；
 *      实际执行一律走 `/api/v1/runs`。
 */
/** 列表筛选状态词表（registry.workflows.status 的真实取值集合，10 行/页）。 */
const STATUS_OPTIONS = [
  FILTER_ALL_OPTION,
  { value: 'ACTIVE', label: '启用' },
  { value: 'DISABLED', label: '停用' },
  { value: 'DELETED', label: '已删除' },
];

export function WorkflowsPage() {
  const [search, setSearch] = useState('');
  const [status, setStatus] = useState('');
  const pager = useCursorPagination();
  const tmplPager = useCursorPagination();
  const query = useWorkflows({ q: search, status, cursor: pager.cursor ?? undefined });
  const templates = usePlanTemplates(tmplPager.cursor ?? undefined);
  const create = useCreateWorkflow();
  const update = useUpdateWorkflow();
  const remove = useDeleteWorkflow();
  const publish = usePublishWorkflow();
  const generate = useGenerateWorkflow();

  const [createOpen, setCreateOpen] = useState(false);
  const [createForm, setCreateForm] = useState({
    workflow_key: '',
    name: '',
    description: '',
    version: '',
    definition: DEFAULT_DEFINITION,
  });
  const [createErr, setCreateErr] = useState<string | null>(null);

  const [editTarget, setEditTarget] = useState<WorkflowInfo | null>(null);
  const [editForm, setEditForm] = useState({ name: '', description: '' });

  const [deleteTarget, setDeleteTarget] = useState<WorkflowInfo | null>(null);

  const [genOpen, setGenOpen] = useState(false);
  const [genForm, setGenForm] = useState({
    instructions: '',
    source_path: '',
    workflow_key: '',
    model_id: '',
    max_source_chars: '',
  });

  const generated = generate.data;

  // 筛选条件变化必须回到首页：否则会带着旧页游标去请求新筛选结果（假结果）。
  useEffect(() => {
    pager.reset();
  }, [search, status, pager.reset]);

  const openEdit = (item: WorkflowInfo) => {
    setEditTarget(item);
    setEditForm({ name: item.name ?? '', description: item.description ?? '' });
    update.reset();
  };

  const submitCreate = () => {
    setCreateErr(null);
    if (!createForm.workflow_key.trim() || !createForm.name.trim()) {
      setCreateErr('workflow_key 与 name 必填。');
      return;
    }
    let definition: unknown;
    try {
      definition = JSON.parse(createForm.definition);
    } catch (e) {
      setCreateErr(`definition 不是合法 JSON：${(e as Error).message}`);
      return;
    }
    create.mutate(
      {
        workflow_key: createForm.workflow_key.trim(),
        name: createForm.name.trim(),
        definition,
        ...(createForm.description.trim() ? { description: createForm.description.trim() } : {}),
        ...(createForm.version.trim() ? { version: createForm.version.trim() } : {}),
      },
      {
        onSuccess: () => {
          setCreateOpen(false);
          setCreateForm({ workflow_key: '', name: '', description: '', version: '', definition: DEFAULT_DEFINITION });
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
          ...(editForm.name.trim() ? { name: editForm.name.trim() } : {}),
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
        ...(genForm.workflow_key.trim() ? { workflow_key: genForm.workflow_key.trim() } : {}),
        ...(genForm.model_id.trim() ? { model_id: genForm.model_id.trim() } : {}),
        ...(Number.isFinite(chars) && chars > 0 ? { max_source_chars: chars } : {}),
      },
      {
        onSuccess: () => {
          setGenOpen(false);
          setGenForm({ instructions: '', source_path: '', workflow_key: '', model_id: '', max_source_chars: '' });
        },
      },
    );
  };

  const workflowColumns: DataTableColumn<WorkflowInfo>[] = [
    {
      key: 'name',
      header: '工作流（插件式）',
      render: (item) => (
        <div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span style={{ fontWeight: 500 }}>{item.name}</span>
            {item.is_builtin ? (
              <Badge tone="info" title="平台内置工作流：由平台随数据集登记，仅展示，不可编辑 / 发布 / 删除">
                内置
              </Badge>
            ) : null}
          </div>
          <div className="text-xs muted" style={{ marginTop: 4 }}>
            <span className="mono">{item.workflow_key}</span> · <span className="copy-id">{item.id}</span>
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
        const meta = versionStatusBadge(item.latest_version_status, '工作流列表.发布状态');
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
        // 平台内置工作流（Harness 运行编排 / 在途操作对账 / 本体版本生命周期）：
        // 仅登记展示，不允许编辑、发布或删除。后端 store 层同样拦截
        // （PATCH/DELETE/publish 一律 409），不只靠这里隐藏按钮。
        if (item.is_builtin) {
          return (
            <span
              className="muted text-sm"
              title="平台内置工作流：仅登记展示，不允许编辑 / 发布 / 删除（服务端同样返回 409）">
              内置 · 不可修改
            </span>
          );
        }
        if (item.status === 'DELETED') return <span className="muted text-sm">已删除</span>;
        // 修订一经通过校验即为不可变版本：UI 只读，编辑入口禁用并引导「创建新版本」（§13.2）。
        const locked = item.latest_version_status === 'PUBLISHED';
        return (
          <div className="row">
            <PermissionGate permission="admin.workflow.write">
              <Button
                variant="ghost"
                size="sm"
                icon="pencil"
                disabled={locked}
                title={locked ? '最新版本已发布（不可变）：请创建新版本' : undefined}
                onClick={() => openEdit(item)}>
                编辑
              </Button>
            </PermissionGate>
            <PermissionGate permission="admin.workflow.write">
              <Button
                variant="primary"
                size="sm"
                icon="rocket"
                disabled={locked}
                loading={publish.isPending && publish.variables === item.id}
                onClick={() => publish.mutate(item.id)}>
                发布
              </Button>
            </PermissionGate>
            <PermissionGate permission="admin.workflow.write">
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

  const templateColumns: DataTableColumn<PlanTemplate>[] = [
    {
      key: 'name',
      header: '计划模板（工作流）',
      render: (item) => (
        <div>
          <div style={{ fontWeight: 500 }}>{item.name}</div>
          <div className="text-xs muted" style={{ marginTop: 4 }}>
            <span className="copy-id">{item.id}</span> · <span className="mono">{item.domain}</span> · 修订{' '}
            <span className="mono">r{item.version}</span>
            {item.ontology_ref ? (
              <>
                {' '}
                · 锁定本体 <span className="mono">{item.ontology_ref}</span>
              </>
            ) : null}
          </div>
        </div>
      ),
    },
    {
      key: 'nodes',
      header: '节点类型',
      // 原先把每个节点类型渲染成一枚 `copy-id` 样式的标签，与真正的标识符外观混同、
      // 且七节点工作流会横向堆满一屏；改为一行顿号分隔的普通文本，信息不丢失但不再抢占视觉。
      render: (item) => (
        <span className="text-sm muted">
          {item.node_kinds.length === 0
            ? '—'
            : item.node_kinds.map((kind) => NODE_KIND_LABEL[kind] ?? kind).join('、')}
        </span>
      ),
    },
    {
      key: 'state',
      header: '状态',
      tight: true,
      render: (item) => <MetaBadge map={STATE_META} value={item.state} context="工作流模板状态" />,
    },
    {
      key: 'updated',
      header: '最近更新',
      tight: true,
      render: (item) => <span className="text-sm">{formatDateTime(item.updated_at)}</span>,
    },
    {
      key: 'actions',
      header: '操作',
      tight: true,
      render: (item) => {
        // 修订一经通过校验即为不可变版本：UI 只读（§13.2）。
        const locked = item.state === 'VALIDATED';
        return (
          <div className="row">
            <button
              type="button"
              className="btn btn--secondary btn--sm"
              disabled={locked}
              title={locked ? '最新修订已通过校验（不可变）：请创建新版本' : undefined}
            >
              编辑节点
            </button>
            <button type="button" className="btn btn--ghost btn--sm">
              {locked ? '创建新版本' : '发布'}
            </button>
          </div>
        );
      },
    },
  ];

  return (
    <div className="page page--wide">
      <PageHeader
        icon="workflow"
        title="工作流与 Harness"
        subtitle={
          <>
            插件式工作流走 <span className="mono">/api/v1/workflows</span>（不可变版本 + 软删）；计划模板资产走{' '}
            <span className="mono">/api/v1/plan-templates</span>。实际执行一律走{' '}
            <span className="mono">/api/v1/runs</span>。
          </>
        }
        actions={
          <PermissionGate permission="admin.workflow.write">
            <div className="row">
              <Button variant="secondary" icon="sparkles" onClick={() => setGenOpen(true)}>
                AI 自动生成
              </Button>
              <Button variant="primary" icon="plus" onClick={() => setCreateOpen(true)}>
                新建工作流
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
            工作流已发布：状态 <strong>{publish.data?.status ?? 'PUBLISHED'}</strong>，版本{' '}
            <span className="mono copy-id">{publish.data?.version_id ?? '—'}</span>。
            {' '}该接口为同步返回，发布结论已在本次响应中确定，无需再跟踪任务。
          </span>
        </div>
      ) : null}

      {generated ? (
        <div className="banner banner--neutral" style={{ marginBottom: 16 }} role="status">
          <span>
            已由需求 / 制度文档生成草稿工作流 <strong>{generated.name ?? generated.workflow_key}</strong>
            （<span className="mono copy-id">{generated.id}</span>）。请完成校验后发布。
          </span>
        </div>
      ) : null}

      <div className="banner banner--warning" style={{ marginBottom: 16 }} role="status">
        <span className="banner__icon" aria-hidden="true">
          <Icon name="alertTriangle" size={16} />
        </span>
        <span>
          已发布的工作流在 UI 中只读，编辑入口禁用并引导「创建新版本」——禁止原地修改运行中的版本定义
          （§13.2 验收清单）。
        </span>
      </div>

      <ListFilterBar
        searchValue={search}
        onSearchChange={setSearch}
        searchPlaceholder="搜索工作流键 / 名称 / 说明…"
        statusValue={status}
        onStatusChange={setStatus}
        statusOptions={STATUS_OPTIONS}
      />

      <DataTable
        caption="插件式工作流列表"
        columns={workflowColumns}
        rows={query.data?.items ?? []}
        rowKey={(item) => item.id}
        loading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        emptyTitle="暂无工作流"
        emptyDescription="可通过「新建工作流」或「AI 自动生成」创建插件式工作流资产。"
        nextCursor={query.data?.next_cursor ?? null}
        hasPrevPage={pager.hasPrevPage}
        onPrevPage={pager.goPrev}
        onNextPage={() => pager.goNext(query.data?.next_cursor ?? null)}
      />

      <h3 className="section" style={{ marginTop: 24 }}>
        计划模板资产
      </h3>
      <DataTable
        caption="计划模板列表"
        columns={templateColumns}
        rows={templates.data?.items ?? []}
        rowKey={(item) => item.id}
        loading={templates.isLoading}
        error={templates.error}
        onRetry={() => void templates.refetch()}
        emptyTitle="暂无工作流模板"
        emptyDescription="模板资产由数据集种子注入（datasets/finance-demo/plans.json）。"
        nextCursor={templates.data?.next_cursor ?? null}
        hasPrevPage={tmplPager.hasPrevPage}
        onPrevPage={tmplPager.goPrev}
        onNextPage={() => tmplPager.goNext(templates.data?.next_cursor ?? null)}
      />

      {/* 新建工作流 */}
      <Dialog open={createOpen} onClose={() => setCreateOpen(false)} title="新建工作流" wide>
        <form
          className="dialog-form"
          onSubmit={(e) => {
            e.preventDefault();
            submitCreate();
          }}>
          <Field label="工作流键（workflow_key）" required hint="英文小写短横线命名，创建后不可变。">
            <Input
              value={createForm.workflow_key}
              placeholder="如 expense-reimburse-approval"
              onChange={(e) => setCreateForm({ ...createForm, workflow_key: e.target.value })}
              required
            />
          </Field>
          <Field label="名称（name）" required>
            <Input value={createForm.name} onChange={(e) => setCreateForm({ ...createForm, name: e.target.value })} required />
          </Field>
          <Field label="说明">
            <Textarea value={createForm.description} onChange={(e) => setCreateForm({ ...createForm, description: e.target.value })} />
          </Field>
          <Field label="版本（version）" hint="留空由服务端生成首个 DRAFT 版本。">
            <Input value={createForm.version} onChange={(e) => setCreateForm({ ...createForm, version: e.target.value })} />
          </Field>
          <Field
            label="编排定义（definition，JSON）"
            required
            hint="含 nodes / edges；节点必须从 start 可达并最终抵达 end。">
            <Textarea
              value={createForm.definition}
              onChange={(e) => setCreateForm({ ...createForm, definition: e.target.value })}
              rows={10}
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

      {/* 编辑工作流 */}
      <Dialog open={editTarget !== null} onClose={() => setEditTarget(null)} title="编辑工作流">
        <form
          className="dialog-form"
          onSubmit={(e) => {
            e.preventDefault();
            submitEdit();
          }}>
          <Field label="工作流键（workflow_key）" hint="不可变：编排变化请通过发布创建新版本。">
            <Input value={editTarget?.workflow_key ?? ''} readOnly disabled />
          </Field>
          <Field label="名称（name）">
            <Input value={editForm.name} onChange={(e) => setEditForm({ ...editForm, name: e.target.value })} />
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

      {/* 删除工作流（软删除） */}
      <ConfirmDialog
        open={deleteTarget !== null}
        title="删除工作流"
        confirmLabel="删除工作流"
        danger
        target={
          deleteTarget ? (
            <>
              <span className="mono">{deleteTarget.workflow_key}</span>（<span className="copy-id">{deleteTarget.id}</span>）
            </>
          ) : null
        }
        impact="软删除（status → DELETED）：工作流不再出现在列表与选择器中，但审计链完整保留。"
        scope="当前租户"
        reversible={false}
        requiresApproval={false}
        verification="删除后在列表确认该工作流消失；审计事件 WorkflowDeleted 可在审计与证据页核对。"
        confirmPhrase={deleteTarget?.workflow_key}
        busy={remove.isPending}
        error={remove.error}
        onCancel={() => setDeleteTarget(null)}
        onConfirm={() => {
          if (deleteTarget) remove.mutate(deleteTarget.id, { onSuccess: () => setDeleteTarget(null) });
        }}
      />

      {/* AI 自动生成工作流 */}
      <Dialog open={genOpen} onClose={() => setGenOpen(false)} title="AI 自动生成工作流" wide>
        <form
          className="dialog-form"
          onSubmit={(e) => {
            e.preventDefault();
            submitGen();
          }}>
          <Field label="需求说明（instructions）" hint="与制度文档路径至少填一项。">
            <Textarea
              value={genForm.instructions}
              placeholder="如：根据费用报销制度，设计一个报销单审批与对账工作流"
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
          <Field label="指定工作流键（workflow_key）" hint="留空由模型生成。">
            <Input value={genForm.workflow_key} onChange={(e) => setGenForm({ ...genForm, workflow_key: e.target.value })} />
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
