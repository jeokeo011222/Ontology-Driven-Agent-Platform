import { useEffect, useState } from 'react';
import { Badge, Button, Dialog, Field, Input, PageHeader, Select, Textarea } from '@/components/ui';
import { ConfirmDialog } from '@/components/confirm-dialog';
import { DataTable, type DataTableColumn } from '@/components/data-table';
import { ErrorState } from '@/components/error-state';
import { PermissionGate } from '@/components/permission-gate';
import { versionStatusBadge } from '@/components/status-badge';
import { ListFilterBar, FILTER_ALL_OPTION } from '@/components/list-filter-bar';
import { useCursorPagination } from '@/hooks/use-cursor-pagination';
import type { SkillInfo, ToolInfo } from '@/types';
import {
  useActiveToolOptions,
  useCreateSkill,
  useCreateSkillVersion,
  useDeleteSkill,
  usePublishSkill,
  useSkills,
  useUpdateSkill,
  type CreateSkillBody,
  type SkillPublishResult,
} from '../api';
import { Icon } from '@/components/icon';

/**
 * 技能管理 `/admin/skills`（Skill 作为一等 registry 资源）
 *
 * 字段口径：contracts/openapi.yaml#/components/schemas/Skill（即
 * `GET /api/v1/skills` 的真实返回，internal/api/store/skill.go）。
 *
 * Agent 经 dependency_manifest.skills 依赖 skill_key；计划编译时取**最新已发布**
 * 版本：prompt_fragment 以「## 技能：{name}」小节追加进 Agent 提示词，
 * tool_bindings 并入本次运行可用工具集合，技能版本 digest 纳入版本锁定
 * （技能版本变更 → 计划缓存失效）。因此：
 *   - 元数据（名称/说明/业务域/状态）走 PATCH 编辑；
 *   - 提示词片段 / 工具绑定 / Schema / 版本号属于**版本内容**，只能通过
 *     「新增版本」（POST /skills/{id}/versions，创建 DRAFT）+「发布」变更，
 *     与后端不可变版本模型一致；
 *   - 只有最新版本为 DRAFT 的技能才可发布（PUBLISHED 状态下发布按钮禁用，
 *     服务端重复发布也返回 409 fail-closed）。
 *
 * 内置技能（is_builtin=true）：平台自身能力，标「内置」徽标且不可修改——
 * 本页隐藏编辑/新增版本/发布/删除入口，后端对 PATCH/DELETE/版本变更一律 409。
 * 工具绑定选项来自真实 `GET /api/v1/tools`（status=ACTIVE），禁止硬编码。
 */

/** 列表筛选状态词表（registry.skills.status 的真实取值集合，10 行/页）。 */
const STATUS_OPTIONS = [
  FILTER_ALL_OPTION,
  { value: 'ACTIVE', label: '启用' },
  { value: 'INACTIVE', label: '停用' },
  { value: 'DELETED', label: '已删除' },
];

/** 编辑弹窗的状态选项（PATCH 仅接受 ACTIVE/INACTIVE；DELETED 只能走删除）。 */
const EDIT_STATUS_OPTIONS = [
  { value: 'ACTIVE', label: '启用' },
  { value: 'INACTIVE', label: '停用' },
];

/** 工具绑定多选（真实 ACTIVE 工具列表，滚动区 + 复选框）。 */
function ToolBindingPicker({
  options,
  selected,
  onChange,
  loading,
}: {
  options: ToolInfo[];
  selected: string[];
  onChange: (next: string[]) => void;
  loading: boolean;
}) {
  const toggle = (toolKey: string) => {
    onChange(selected.includes(toolKey) ? selected.filter((k) => k !== toolKey) : [...selected, toolKey]);
  };
  if (loading) {
    return <p className="text-sm muted">正在加载可用工具…</p>;
  }
  if (options.length === 0) {
    return <p className="text-sm muted">暂无 ACTIVE 工具可绑定。请先在「工具管理」登记并启用工具。</p>;
  }
  return (
    <div
      role="group"
      aria-label="工具绑定"
      style={{
        display: 'flex',
        flexDirection: 'column',
        gap: 4,
        maxHeight: 168,
        overflowY: 'auto',
        border: '1px solid var(--color-border)',
        borderRadius: 8,
        padding: 8,
      }}>
      {options.map((tool) => (
        <label key={tool.tool_key} style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}>
          <input
            type="checkbox"
            checked={selected.includes(tool.tool_key)}
            onChange={() => toggle(tool.tool_key)}
          />
          <span className="mono text-sm">{tool.tool_key}</span>
          <span className="text-xs muted">{tool.description}</span>
        </label>
      ))}
    </div>
  );
}

export function SkillsPage() {
  const [search, setSearch] = useState('');
  const [status, setStatus] = useState('');
  const pager = useCursorPagination();
  const query = useSkills({ q: search, status, cursor: pager.cursor ?? undefined });
  const create = useCreateSkill();
  const createVersion = useCreateSkillVersion();
  const update = useUpdateSkill();
  const remove = useDeleteSkill();
  const publish = usePublishSkill();
  // 工具绑定选项：真实 GET /tools 的 ACTIVE 条目（200 行/页，覆盖选择器场景）。
  const toolOptions = useActiveToolOptions();

  const [createOpen, setCreateOpen] = useState(false);
  const [createForm, setCreateForm] = useState({
    skill_key: '',
    name: '',
    domain: '',
    description: '',
    version: '',
    prompt_fragment: '',
    input_schema: '{}',
    output_schema: '{}',
  });
  const [createBindings, setCreateBindings] = useState<string[]>([]);
  const [createErr, setCreateErr] = useState<string | null>(null);

  const [editTarget, setEditTarget] = useState<SkillInfo | null>(null);
  const [editForm, setEditForm] = useState({ name: '', description: '', domain: '', status: 'ACTIVE' });

  const [versionTarget, setVersionTarget] = useState<SkillInfo | null>(null);
  const [versionForm, setVersionForm] = useState({
    version: '',
    prompt_fragment: '',
    input_schema: '{}',
    output_schema: '{}',
  });
  const [versionBindings, setVersionBindings] = useState<string[]>([]);
  const [versionErr, setVersionErr] = useState<string | null>(null);

  const [deleteTarget, setDeleteTarget] = useState<SkillInfo | null>(null);

  // 筛选条件变化必须回到首页：否则会带着旧页游标去请求新筛选结果（假结果）。
  useEffect(() => {
    pager.reset();
  }, [search, status, pager.reset]);

  const openEdit = (item: SkillInfo) => {
    setEditTarget(item);
    setEditForm({
      name: item.name ?? '',
      description: item.description ?? '',
      domain: item.domain ?? '',
      status: item.status === 'INACTIVE' ? 'INACTIVE' : 'ACTIVE',
    });
    update.reset();
  };

  const openVersion = (item: SkillInfo) => {
    setVersionTarget(item);
    setVersionForm({ version: '', prompt_fragment: item.prompt_fragment ?? '', input_schema: '{}', output_schema: '{}' });
    setVersionBindings(item.tool_bindings ?? []);
    setVersionErr(null);
  };

  /** 客户端先 parse 校验 JSON（服务端 json.Valid 强校验，至少 {}）。 */
  const parseSchemas = (input: string, output: string): { input: unknown; output: unknown } | string => {
    try {
      return { input: JSON.parse(input.trim() || '{}'), output: JSON.parse(output.trim() || '{}') };
    } catch (e) {
      return `输入/输出 Schema 不是合法 JSON：${(e as Error).message}`;
    }
  };

  const submitCreate = () => {
    setCreateErr(null);
    if (!createForm.skill_key.trim() || !createForm.name.trim()) {
      setCreateErr('skill_key 与名称必填。');
      return;
    }
    const schemas = parseSchemas(createForm.input_schema, createForm.output_schema);
    if (typeof schemas === 'string') {
      setCreateErr(schemas);
      return;
    }
    const body: CreateSkillBody = {
      skill_key: createForm.skill_key.trim(),
      name: createForm.name.trim(),
      input_schema: schemas.input,
      output_schema: schemas.output,
      ...(createForm.domain.trim() ? { domain: createForm.domain.trim() } : {}),
      ...(createForm.description.trim() ? { description: createForm.description.trim() } : {}),
      ...(createForm.version.trim() ? { version: createForm.version.trim() } : {}),
      ...(createForm.prompt_fragment.trim() ? { prompt_fragment: createForm.prompt_fragment } : {}),
      ...(createBindings.length ? { tool_bindings: createBindings } : {}),
    };
    create.mutate(body, {
      onSuccess: () => {
        setCreateOpen(false);
        setCreateForm({ skill_key: '', name: '', domain: '', description: '', version: '', prompt_fragment: '', input_schema: '{}', output_schema: '{}' });
        setCreateBindings([]);
      },
    });
  };

  const submitEdit = () => {
    if (!editTarget) return;
    update.mutate(
      {
        id: editTarget.id,
        body: {
          name: editForm.name.trim(),
          description: editForm.description,
          domain: editForm.domain.trim(),
          status: editForm.status === 'INACTIVE' ? 'INACTIVE' : 'ACTIVE',
        },
      },
      { onSuccess: () => setEditTarget(null) },
    );
  };

  const submitVersion = () => {
    if (!versionTarget) return;
    setVersionErr(null);
    if (!versionForm.version.trim()) {
      setVersionErr('版本号必填。');
      return;
    }
    const schemas = parseSchemas(versionForm.input_schema, versionForm.output_schema);
    if (typeof schemas === 'string') {
      setVersionErr(schemas);
      return;
    }
    createVersion.mutate(
      { id: versionTarget.id, body: {
        version: versionForm.version.trim(),
        input_schema: schemas.input,
        output_schema: schemas.output,
        ...(versionForm.prompt_fragment.trim() ? { prompt_fragment: versionForm.prompt_fragment } : {}),
        ...(versionBindings.length ? { tool_bindings: versionBindings } : {}),
      } },
      { onSuccess: () => setVersionTarget(null) },
    );
  };

  const columns: DataTableColumn<SkillInfo>[] = [
    {
      key: 'skill_key',
      header: '技能',
      render: (item) => (
        <div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span style={{ fontWeight: 500 }} className="mono">
              {item.skill_key}
            </span>
            {item.is_builtin ? (
              <Badge tone="info" title="平台内置技能：由平台登记，仅展示，不可修改">
                内置
              </Badge>
            ) : null}
          </div>
          <div className="text-xs muted" style={{ marginTop: 4 }}>
            <span className="copy-id">{item.id}</span> · 业务域 {item.domain || '—'}
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
      key: 'status',
      header: '状态',
      tight: true,
      render: (item) => (
        <Badge
          tone={item.status === 'ACTIVE' ? 'success' : item.status === 'DELETED' ? 'neutral' : 'warning'}
          title={`技能状态：${item.status}`}>
          {item.status === 'ACTIVE' ? '启用' : item.status === 'INACTIVE' ? '停用' : '已删除'}
        </Badge>
      ),
    },
    {
      key: 'latest_version_status',
      header: '版本状态',
      tight: true,
      render: (item) => {
        const meta = versionStatusBadge(item.latest_version_status, '技能列表.版本状态');
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
      key: 'tool_bindings',
      header: '工具绑定',
      render: (item) => {
        const keys = item.tool_bindings ?? [];
        if (keys.length === 0) return <span className="muted text-sm">—</span>;
        return (
          <div className="text-xs mono" style={{ maxWidth: 280, overflowWrap: 'anywhere' }}>
            {keys.join('、')}
          </div>
        );
      },
    },
    {
      key: 'actions',
      header: '操作',
      tight: true,
      render: (item) =>
        item.is_builtin ? (
          <span
            className="muted text-sm"
            title="平台内置技能：仅登记展示，不允许编辑 / 发布 / 删除（服务端同样返回 409）">
            内置 · 不可修改
          </span>
        ) : item.status === 'DELETED' ? (
          <span className="muted text-sm">已删除</span>
        ) : (
          <div className="row" style={{ gap: 4 }}>
            <PermissionGate permission="admin.agent.write">
              <Button
                variant="ghost"
                size="sm"
                iconOnly
                icon="pencil"
                aria-label="编辑"
                title="编辑名称 / 说明 / 业务域 / 状态"
                onClick={() => openEdit(item)}
              />
            </PermissionGate>
            <PermissionGate permission="admin.agent.write">
              <Button
                variant="ghost"
                size="sm"
                iconOnly
                icon="plus"
                aria-label="新增版本"
                title="新增版本（提示词片段 / 工具绑定 / Schema），创建为草稿"
                onClick={() => openVersion(item)}
              />
            </PermissionGate>
            <PermissionGate permission="admin.agent.publish">
              <Button
                variant="ghost"
                size="sm"
                iconOnly
                icon="rocket"
                aria-label={item.latest_version_status === 'PUBLISHED' ? '已发布' : '发布'}
                title={
                  item.latest_version_status === 'PUBLISHED'
                    ? '最新版本已发布'
                    : '发布最新草稿版本（发布后才能被 Agent 依赖）'
                }
                disabled={item.latest_version_status === 'PUBLISHED'}
                loading={publish.isPending && publish.variables === item.id}
                onClick={() => publish.mutate(item.id)}
              />
            </PermissionGate>
            <PermissionGate permission="admin.agent.write">
              <Button
                variant="ghost"
                size="sm"
                iconOnly
                icon="ban"
                aria-label="删除"
                title="删除（软删除，审计链完整保留）"
                loading={remove.isPending && remove.variables === item.id}
                onClick={() => setDeleteTarget(item)}
              />
            </PermissionGate>
          </div>
        ),
    },
  ];

  const toolList = toolOptions.data?.items ?? [];

  return (
    <div className="page page--wide">
      <PageHeader
        icon="sparkles"
        title="技能管理"
        subtitle="技能是一等 registry 资源：Agent 通过依赖清单绑定技能；计划编译时展开技能提示词片段并并入工具白名单，技能版本变更会使既有计划缓存失效。"
        actions={
          <PermissionGate permission="admin.agent.write">
            <Button variant="primary" icon="plus" onClick={() => setCreateOpen(true)}>
              新建技能
            </Button>
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
            技能已发布：版本 <span className="mono copy-id">{(publish.data as SkillPublishResult | undefined)?.version_id ?? '—'}</span>
            。发布后该技能即可被 Agent 的依赖清单引用（计划编译按已发布版本展开）。
          </span>
        </div>
      ) : null}

      <ListFilterBar
        searchValue={search}
        onSearchChange={setSearch}
        searchPlaceholder="搜索技能键 / 名称 / 说明…"
        statusValue={status}
        onStatusChange={setStatus}
        statusOptions={STATUS_OPTIONS}
      />

      <DataTable
        caption="技能列表"
        columns={columns}
        rows={query.data?.items ?? []}
        rowKey={(item) => item.id}
        loading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        emptyTitle="暂无技能"
        emptyDescription="可通过「新建技能」创建技能资产：技能把提示词片段与一组工具打包，供 Agent 绑定复用。"
        nextCursor={query.data?.next_cursor ?? null}
        hasPrevPage={pager.hasPrevPage}
        onPrevPage={pager.goPrev}
        onNextPage={() => pager.goNext(query.data?.next_cursor ?? null)}
      />

      {/* 新建技能（含首个 DRAFT 版本） */}
      <Dialog open={createOpen} onClose={() => setCreateOpen(false)} title="新建技能" wide>
        <form
          className="dialog-form"
          onSubmit={(e) => {
            e.preventDefault();
            submitCreate();
          }}>
          <Field label="技能键（skill_key）" required hint="英文小写短横线命名，租户内唯一，创建后不可变。">
            <Input
              value={createForm.skill_key}
              placeholder="如 invoice-compliance"
              onChange={(e) => setCreateForm({ ...createForm, skill_key: e.target.value })}
              required
            />
          </Field>
          <Field label="名称" required>
            <Input value={createForm.name} onChange={(e) => setCreateForm({ ...createForm, name: e.target.value })} required />
          </Field>
          <Field label="业务域（domain）" hint="如 finance / contract / hr。">
            <Input value={createForm.domain} onChange={(e) => setCreateForm({ ...createForm, domain: e.target.value })} />
          </Field>
          <Field label="版本（version）" hint="留空由服务端生成首个 DRAFT 版本（0.1.0）。">
            <Input value={createForm.version} onChange={(e) => setCreateForm({ ...createForm, version: e.target.value })} />
          </Field>
          <Field label="说明">
            <Textarea value={createForm.description} onChange={(e) => setCreateForm({ ...createForm, description: e.target.value })} />
          </Field>
          <Field label="提示词片段（prompt_fragment）" hint="计划编译时以「## 技能：{名称}」小节追加进 Agent 提示词。">
            <Textarea
              value={createForm.prompt_fragment}
              onChange={(e) => setCreateForm({ ...createForm, prompt_fragment: e.target.value })}
              rows={4}
            />
          </Field>
          <Field
            label="工具绑定（tool_bindings）"
            hint="该技能运行时并入可用工具集合；仅可选 ACTIVE 工具（真实列表），引用不存在的工具会被服务端 404 拒绝。">
            <ToolBindingPicker
              options={toolList}
              selected={createBindings}
              onChange={setCreateBindings}
              loading={toolOptions.isLoading}
            />
          </Field>
          <Field label="输入 Schema（input_schema，JSON）" required hint="必填；至少为 {}。">
            <Textarea
              value={createForm.input_schema}
              onChange={(e) => setCreateForm({ ...createForm, input_schema: e.target.value })}
              rows={3}
            />
          </Field>
          <Field label="输出 Schema（output_schema，JSON）" required hint="必填；至少为 {}。">
            <Textarea
              value={createForm.output_schema}
              onChange={(e) => setCreateForm({ ...createForm, output_schema: e.target.value })}
              rows={3}
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

      {/* 编辑技能元数据（PATCH 语义；版本内容走「新增版本」） */}
      <Dialog open={editTarget !== null} onClose={() => setEditTarget(null)} title="编辑技能">
        <form
          className="dialog-form"
          onSubmit={(e) => {
            e.preventDefault();
            submitEdit();
          }}>
          <Field label="技能键（skill_key）" hint="不可变：内容变更请新增版本。">
            <Input value={editTarget?.skill_key ?? ''} readOnly disabled />
          </Field>
          <Field label="名称">
            <Input value={editForm.name} onChange={(e) => setEditForm({ ...editForm, name: e.target.value })} />
          </Field>
          <Field label="业务域（domain）">
            <Input value={editForm.domain} onChange={(e) => setEditForm({ ...editForm, domain: e.target.value })} />
          </Field>
          <Field label="说明">
            <Textarea value={editForm.description} onChange={(e) => setEditForm({ ...editForm, description: e.target.value })} />
          </Field>
          <Field label="状态" hint="停用后技能不再出现在 Agent 绑定选择器；已绑定的引用不受影响。">
            <Select
              value={editForm.status}
              options={EDIT_STATUS_OPTIONS}
              onChange={(e) => setEditForm({ ...editForm, status: e.target.value })}
            />
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

      {/* 新增版本（DRAFT；发布走列表上的发布按钮） */}
      <Dialog open={versionTarget !== null} onClose={() => setVersionTarget(null)} title="新增技能版本" wide>
        <form
          className="dialog-form"
          onSubmit={(e) => {
            e.preventDefault();
            submitVersion();
          }}>
          <Field label="技能键（skill_key）" hint="不可变。">
            <Input value={versionTarget?.skill_key ?? ''} readOnly disabled />
          </Field>
          <Field label="版本（version）" required hint="不可与已有版本重复；创建为草稿，需发布后才能被 Agent 依赖。">
            <Input value={versionForm.version} onChange={(e) => setVersionForm({ ...versionForm, version: e.target.value })} required />
          </Field>
          <Field label="提示词片段（prompt_fragment）" hint="留空继承无片段；计划编译时追加进 Agent 提示词。">
            <Textarea
              value={versionForm.prompt_fragment}
              onChange={(e) => setVersionForm({ ...versionForm, prompt_fragment: e.target.value })}
              rows={4}
            />
          </Field>
          <Field label="工具绑定（tool_bindings）" hint="整体替换该版本的绑定；仅可选 ACTIVE 工具。">
            <ToolBindingPicker
              options={toolList}
              selected={versionBindings}
              onChange={setVersionBindings}
              loading={toolOptions.isLoading}
            />
          </Field>
          <Field label="输入 Schema（input_schema，JSON）" required hint="必填；至少为 {}。">
            <Textarea
              value={versionForm.input_schema}
              onChange={(e) => setVersionForm({ ...versionForm, input_schema: e.target.value })}
              rows={3}
            />
          </Field>
          <Field label="输出 Schema（output_schema，JSON）" required hint="必填；至少为 {}。">
            <Textarea
              value={versionForm.output_schema}
              onChange={(e) => setVersionForm({ ...versionForm, output_schema: e.target.value })}
              rows={3}
            />
          </Field>
          <div className="dialog-actions">
            <Button type="button" variant="ghost" onClick={() => setVersionTarget(null)}>
              取消
            </Button>
            <Button type="submit" variant="primary" disabled={createVersion.isPending}>
              {createVersion.isPending ? '创建中…' : '创建草稿版本'}
            </Button>
          </div>
          {versionErr ? (
            <p className="text-sm" style={{ color: 'var(--color-error)' }}>
              {versionErr}
            </p>
          ) : null}
          {createVersion.error && versionTarget !== null ? (
            <p className="text-sm" style={{ color: 'var(--color-error)' }}>
              创建失败：{(createVersion.error as Error).message}
            </p>
          ) : null}
        </form>
      </Dialog>

      {/* 删除技能（软删除） */}
      <ConfirmDialog
        open={deleteTarget !== null}
        title="删除技能"
        confirmLabel="删除技能"
        danger
        target={
          deleteTarget ? (
            <>
              <span className="mono">{deleteTarget.skill_key}</span>（<span className="copy-id">{deleteTarget.id}</span>）
            </>
          ) : null
        }
        impact="软删除（status → DELETED）：技能不再出现在列表与 Agent 绑定选择器中；已绑定该技能的 Agent 在计划编译时会因取不到已发布版本而失败，请先解除绑定。"
        scope="当前租户"
        reversible={false}
        requiresApproval={false}
        verification="删除后在列表确认该技能消失；审计事件 SkillDeleted 可在审计与证据页核对。"
        confirmPhrase={deleteTarget?.skill_key}
        busy={remove.isPending}
        error={remove.error}
        onCancel={() => setDeleteTarget(null)}
        onConfirm={() => {
          if (deleteTarget) remove.mutate(deleteTarget.id, { onSuccess: () => setDeleteTarget(null) });
        }}
      />
    </div>
  );
}
