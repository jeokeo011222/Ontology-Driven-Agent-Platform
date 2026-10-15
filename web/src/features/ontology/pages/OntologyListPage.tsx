import { useEffect, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Badge, Button, Dialog, Field, Input, PageHeader, Select, TabPanel, Tabs, Textarea } from '@/components/ui';
import { DataTable, type DataTableColumn } from '@/components/data-table';
import { ConfirmDialog } from '@/components/confirm-dialog';
import { ModelSelect } from '@/components/model-select';
import { PermissionGate } from '@/components/permission-gate';
import { ListFilterBar, FILTER_ALL_OPTION } from '@/components/list-filter-bar';
import { useCursorPagination } from '@/hooks/use-cursor-pagination';
import { apiGet } from '@/api/client';
import type { OntologySummary } from '@/types';
import { useGenerateOntology, useOntologies, useUpdateOntology } from '../api';
import { ontologyStatusBadge, ontologyVersionBadge } from '../state';
import { OntologyCategoriesPanel } from './OntologyCategoriesPanel';
import { OntologyArtifactsDrawer } from './OntologyArtifactsDrawer';

interface CategoryOption {
  id: string;
  category_code: string;
  name: string;
  status: string;
}

const GRANULARITY_OPTIONS = [
  { value: 'DOMAIN', label: 'DOMAIN（业务域级）' },
  { value: 'ENTITY', label: 'ENTITY（实体级）' },
];

/**
 * 列表筛选状态词表（ontology.ontologies.status 的真实取值集合，10 行/页）。
 * 不含 DELETED：列表 SQL 无条件排除软删除条目（`o.status <> 'DELETED'`），
 * 放一个永远筛不出结果的选项属于假筛选。
 */
const STATUS_OPTIONS = [
  FILTER_ALL_OPTION,
  { value: 'ACTIVE', label: '启用' },
  { value: 'INACTIVE', label: '已停用' },
  { value: 'ARCHIVED', label: '已归档' },
];

/** 本体级是否处于启用态（仅 ACTIVE 显示「停用」，其余显示「启用」）。 */
const isRowActive = (item: OntologySummary) => item.status === 'ACTIVE';

const EMPTY_GEN = {
  source_path: '',
  ontology_key: '',
  name: '',
  domain: '',
  granularity: 'DOMAIN',
  description: '',
  category_id: '',
  model_id: '',
  max_source_chars: '',
};

/**
 * MVP 页面 4a：本体工作台列表 `/admin/ontologies`（§6.1 / §14.3）
 *
 * 字段口径以 contracts/openapi.yaml#/components/schemas/Ontology 为准：
 *   版本状态取 `latest_version_status`（版本生命周期），
 *   本体状态取 `status`（本体级启用/归档），
 *   版本号取 `latest_version` —— **不存在** `state` / `version` / `owner` /
 *   `updated_at` 字段（历史缺陷 #19：按不存在的字段索引状态词表导致白屏）。
 *
 * 页面分两个页签：`本体列表` 与 `业务领域分类`（原「本体分类」独立页面已并入此处，
 * 导航归入「设计与执行」分组）。分类 CRUD 见 OntologyCategoriesPanel。
 *
 * AI 自动生成（§6.2）：**上传本地制度文档**（.docx/.doc/.xlsx/.md）或改用服务端
 * 路径（高级项），调用已装配的真实模型端点生成结构化本体清单并落库为 DRAFT。
 * 未装配真实模型端点时服务端返回 503 并在响应体说明，前端如实展示该原因，
 * **不做本地兜底或占位生成**。
 */
export function OntologyListPage() {
  const [search, setSearch] = useState('');
  const [status, setStatus] = useState('');
  const pager = useCursorPagination();
  const query = useOntologies({ q: search, status, cursor: pager.cursor ?? undefined });
  const generate = useGenerateOntology();
  const updateOntology = useUpdateOntology();
  const [tab, setTab] = useState('ontologies');
  const [genOpen, setGenOpen] = useState(false);
  const [genForm, setGenForm] = useState({ ...EMPTY_GEN });
  const [sourceFile, setSourceFile] = useState<File | null>(null);
  const [showAdvancedPath, setShowAdvancedPath] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);
  /** 抽屉当前查看的本体；null 表示抽屉关闭。 */
  const [artifactTarget, setArtifactTarget] = useState<OntologySummary | null>(null);
  /** 启停确认对话框当前作用的本体；null 表示对话框关闭。 */
  const [toggleTarget, setToggleTarget] = useState<OntologySummary | null>(null);

  /** 当前确认对话框是把本体停用（目标为 ACTIVE）还是重新启用。 */
  const isDisabling = toggleTarget?.status === 'ACTIVE';

  /**
   * 启停只改本体级 status，不动版本生命周期：停用后本体仍留在列表中（可再启用），
   * 但不再用于新的运行。服务端对非法 status 直接 400，前端只发 ACTIVE / INACTIVE。
   */
  const openToggle = (item: OntologySummary) => {
    updateOntology.reset();
    setToggleTarget(item);
  };

  const confirmToggle = () => {
    if (!toggleTarget) return;
    updateOntology.mutate(
      { id: toggleTarget.id, status: isDisabling ? 'INACTIVE' : 'ACTIVE' },
      { onSuccess: () => setToggleTarget(null) },
    );
  };

  /**
   * 清空已选文件。
   *
   * 原生 `<input type="file">` 的显示值不受 React 控制：只把 state 置空，控件上
   * 仍会显示旧文件名，与「已改用服务端路径」的界面状态自相矛盾。必须同时把
   * 控件自身的 value 重置掉。
   */
  const clearPickedFile = () => {
    setSourceFile(null);
    if (fileInputRef.current) fileInputRef.current.value = '';
  };

  const categoriesQuery = useQuery<{ items: CategoryOption[] }>({
    queryKey: ['admin', 'ontology-categories'],
    queryFn: () => apiGet('/admin/ontology-categories'),
    enabled: genOpen,
  });

  const closeGen = () => {
    setGenOpen(false);
    setGenForm({ ...EMPTY_GEN });
    clearPickedFile();
    setShowAdvancedPath(false);
    generate.reset();
  };

  const submitGen = () => {
    const chars = Number(genForm.max_source_chars);
    const body = {
      // 二选一：上传文件优先，否则用服务端路径（高级项）。
      ...(sourceFile ? { source_file: sourceFile } : { source_path: genForm.source_path.trim() }),
      ...(genForm.name.trim() ? { name: genForm.name.trim() } : {}),
      // 业务领域由所选分类的 category_code 派生（不允许手工录入）。
      ...(genForm.domain.trim() ? { domain: genForm.domain.trim() } : {}),
      granularity: genForm.granularity,
      ...(genForm.description.trim() ? { description: genForm.description.trim() } : {}),
      ...(genForm.category_id ? { category_id: genForm.category_id } : {}),
      ...(genForm.model_id.trim() ? { model_id: genForm.model_id.trim() } : {}),
      ...(Number.isFinite(chars) && chars > 0 ? { max_source_chars: chars } : {}),
    };
    generate.mutate(body);
  };

  const canSubmit = Boolean(sourceFile) || genForm.source_path.trim().length > 0;

  const columns: DataTableColumn<OntologySummary>[] = [
    {
      key: 'name',
      header: '本体包',
      render: (item) => (
        <div>
          <Link to={`/admin/ontologies/${item.id}`} style={{ fontWeight: 500 }}>
            {item.name}
          </Link>
          <div className="text-xs muted" style={{ marginTop: 4 }}>
            <span className="copy-id">{item.ontology_key}</span> · 业务域 {item.domain} · 粒度{' '}
            {item.granularity}
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
      header: '最新版本状态',
      tight: true,
      render: (item) => {
        const meta = ontologyVersionBadge(item.latest_version_status, '本体列表.最新版本状态');
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
      key: 'status',
      header: '本体状态',
      tight: true,
      render: (item) => {
        const active = isRowActive(item);
        const meta = ontologyStatusBadge(item.status, '本体列表.本体状态');
        const title =
          item.status === 'ACTIVE'
            ? '启用中：点击停用该本体，停用后不再用于新的运行'
            : item.status === 'ARCHIVED'
              ? '已归档：点击启用该本体'
              : '已停用：点击启用该本体';
        return (
          <PermissionGate
            permission="admin.ontology.write"
            fallback={
              <Badge tone={meta.tone} title={meta.title}>
                {meta.label}
              </Badge>
            }>
            <Button
              size="sm"
              variant="ghost"
              iconOnly
              icon={active ? 'pause' : 'play'}
              aria-label={title}
              title={title}
              style={active ? { color: 'var(--color-success)' } : undefined}
              onClick={() => openToggle(item)}
            />
          </PermissionGate>
        );
      },
    },
    {
      key: 'actions',
      header: '操作',
      tight: true,
      render: (item) => (
        <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
          {/* 行内直接开抽屉看知识图谱与产物原文，不必先进工作台：产物由 manifest 派生，
              对草稿版本同样可见。 */}
          <Button size="sm" variant="ghost" onClick={() => setArtifactTarget(item)}>
            查看知识图谱
          </Button>
          <Link className="btn btn--secondary btn--sm" to={`/admin/ontologies/${item.id}`}>
            打开工作台
          </Link>
        </div>
      ),
    },
  ];

  const generated = generate.data;

  // 筛选条件变化必须回到首页：否则会带着旧页游标去请求新筛选结果（假结果）。
  useEffect(() => {
    pager.reset();
  }, [search, status, pager.reset]);

  return (
    <div className="page page--wide">
      <PageHeader
        icon="ontology"
        title="企业本体"
        subtitle="编辑草稿不会影响生产运行中的版本；发布前必须完成结构校验、SHACL 验证、语义回归与依赖兼容性检查。"
        actions={
          <PermissionGate permission="admin.ontology.write">
            <Button variant="primary" icon="sparkles" onClick={() => setGenOpen(true)}>
              AI 自动生成
            </Button>
          </PermissionGate>
        }
      />

      <Tabs
        ariaLabel="企业本体页面分区"
        value={tab}
        onChange={setTab}
        items={[
          { id: 'ontologies', label: '本体列表' },
          { id: 'categories', label: '业务领域分类' },
        ]}
      />

      <TabPanel id="ontologies" active={tab === 'ontologies'}>
        {generated ? (
          <div className="banner banner--neutral" role="status">
            <span>
              已由制度文档生成草稿本体 <strong>{generated.name ?? generated.ontology_key}</strong>
              （<span className="mono copy-id">{generated.id}</span>）。
              {generated.generation
                ? ` 源文件 ${generated.generation.source_files?.length ?? 0} 份、${generated.generation.source_chars} 字符${
                    generated.generation.truncated ? '（已截断）' : ''
                  }。`
                : null}
              请到工作台完成校验与发布。
            </span>
          </div>
        ) : null}

        <ListFilterBar
          searchValue={search}
          onSearchChange={setSearch}
          searchPlaceholder="搜索本体键 / 名称 / 说明…"
          statusValue={status}
          onStatusChange={setStatus}
          statusOptions={STATUS_OPTIONS}
        />

        <DataTable
          caption="本体包列表"
          columns={columns}
          rows={query.data?.items ?? []}
          rowKey={(item) => item.id}
          loading={query.isLoading}
          error={query.error}
          onRetry={() => void query.refetch()}
          emptyTitle="暂无本体包"
          emptyDescription="可通过网关导入或创建新的本体草稿。"
          nextCursor={query.data?.next_cursor ?? null}
          hasPrevPage={pager.hasPrevPage}
          onPrevPage={pager.goPrev}
          onNextPage={() => pager.goNext(query.data?.next_cursor ?? null)}
        />
      </TabPanel>

      <TabPanel id="categories" active={tab === 'categories'}>
        <OntologyCategoriesPanel />
      </TabPanel>

      <Dialog open={genOpen} onClose={closeGen} title="AI 自动生成本体" wide>
        <form
          className="dialog-form"
          onSubmit={(e) => {
            e.preventDefault();
            submitGen();
          }}>
          <Field
            label="制度文档"
            required
            hint="从本地选择 word / excel / MD 文档（.docx / .doc / .xlsx / .md）。">
            <input
              type="file"
              ref={fileInputRef}
              accept=".docx,.doc,.xlsx,.md"
              onChange={(e) => {
                const picked = e.target.files?.[0] ?? null;
                setSourceFile(picked);
                // 与「服务端路径」互斥：选了文件即清空路径。
                if (picked) setGenForm({ ...genForm, source_path: '' });
              }}
            />
          </Field>
          {sourceFile ? (
            <p className="text-xs muted">
              已选文件：{sourceFile.name}（{Math.max(1, Math.round(sourceFile.size / 1024))} KB）
            </p>
          ) : null}

          <div>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => {
                // 两种来源严格互斥：展开「服务端路径」时放弃已选文件（含控件显示值），
                // 收起时清空路径，避免出现"文件名与路径同时可见"的矛盾状态。
                if (showAdvancedPath) setGenForm({ ...genForm, source_path: '' });
                else clearPickedFile();
                setShowAdvancedPath(!showAdvancedPath);
              }}>
              {showAdvancedPath ? '收起：服务端路径' : '高级：改用服务端路径'}
            </Button>
          </div>
          {showAdvancedPath ? (
            <Field
              label="制度文档路径（source_path）"
              hint="相对制度文档根目录（默认 docs/资料）的文件或目录，如 费用报销制度；越出根目录会被拒绝。">
              <Input
                value={genForm.source_path}
                placeholder="如 费用报销制度 或 合同管理制度/合同管理细则.docx"
                onChange={(e) => {
                  setGenForm({ ...genForm, source_path: e.target.value });
                  // 与上传文件互斥：输入路径即清空已选文件（含控件显示值）。
                  if (e.target.value) clearPickedFile();
                }}
              />
            </Field>
          ) : null}

          <Field label="业务领域" hint="读取业务领域分类字典；如需新增请到「业务领域分类」页签。">
            <Select
              value={genForm.category_id}
              placeholder={categoriesQuery.isLoading ? '加载中…' : '不指定（默认 general）'}
              options={(categoriesQuery.data?.items ?? [])
                .filter((c) => c.status !== 'INACTIVE')
                .map((c) => ({ value: c.id, label: `${c.name}（${c.category_code}）` }))}
              onChange={(e) => {
                const picked = (categoriesQuery.data?.items ?? []).find((c) => c.id === e.target.value);
                setGenForm({ ...genForm, category_id: e.target.value, domain: picked?.category_code ?? '' });
              }}
            />
          </Field>
          <Field label="本体键（ontology_key）" hint="留空则由文件名/路径推导。">
            <Input
              value={genForm.ontology_key}
              onChange={(e) => setGenForm({ ...genForm, ontology_key: e.target.value })}
            />
          </Field>
          <Field label="名称">
            <Input value={genForm.name} onChange={(e) => setGenForm({ ...genForm, name: e.target.value })} />
          </Field>
          <Field label="粒度（granularity）">
            <Select
              value={genForm.granularity}
              options={GRANULARITY_OPTIONS}
              onChange={(e) => setGenForm({ ...genForm, granularity: e.target.value })}
            />
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
          <Field label="说明">
            <Textarea
              value={genForm.description}
              onChange={(e) => setGenForm({ ...genForm, description: e.target.value })}
            />
          </Field>
          <div className="dialog-actions">
            <Button type="button" variant="ghost" onClick={closeGen}>
              取消
            </Button>
            <Button type="submit" variant="primary" disabled={generate.isPending || !canSubmit}>
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

      {/* key 随本体切换：抽屉内部状态（选中页签、复制反馈、全宽与检索）必须归零，
          否则换一个本体会沿用上一个本体的页签与"已复制"提示。 */}
      <OntologyArtifactsDrawer
        key={artifactTarget?.id ?? 'none'}
        ontologyId={artifactTarget?.id ?? null}
        ontologyName={artifactTarget?.name ?? ''}
        onClose={() => setArtifactTarget(null)}
      />

      <ConfirmDialog
        open={toggleTarget !== null}
        title={isDisabling ? '停用本体' : '启用本体'}
        confirmLabel={isDisabling ? '停用本体' : '启用本体'}
        danger={false}
        target={
          toggleTarget ? (
            <>
              {toggleTarget.name}（<span className="mono copy-id">{toggleTarget.ontology_key}</span>）
            </>
          ) : null
        }
        impact={
          isDisabling
            ? '该本体不再用于新的运行；已发布版本与既有审计链不受影响。本体仍保留在列表中，可随时重新启用。'
            : '该本体重新参与新的运行。'
        }
        scope="当前租户"
        reversible
        requiresApproval={false}
        verification="操作完成后，列表「本体状态」列会立即变更。"
        busy={updateOntology.isPending}
        error={updateOntology.error}
        onConfirm={confirmToggle}
        onCancel={() => setToggleTarget(null)}
      />
    </div>
  );
}
