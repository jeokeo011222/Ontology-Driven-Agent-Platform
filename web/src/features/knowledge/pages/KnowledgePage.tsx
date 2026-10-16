import { useEffect, useRef, useState } from 'react';
import { Badge, Button, Dialog, Field, PageHeader, Select } from '@/components/ui';
import { Drawer } from '@/components/ui';
import { ConfirmDialog } from '@/components/confirm-dialog';
import { DataTable, type DataTableColumn } from '@/components/data-table';
import { ListFilterBar } from '@/components/list-filter-bar';
import { ErrorState } from '@/components/error-state';
import { Icon } from '@/components/icon';
import { useCursorPagination } from '@/hooks/use-cursor-pagination';
import { formatDateTime } from '@/lib/format';
import {
  formatBytes,
  useDeleteKnowledgeDocument,
  useKnowledgeDocument,
  useKnowledgeDocuments,
  useOntologyVersionOptions,
  useRetryKnowledgeJob,
  useUploadKnowledgeDocument,
  type KnowledgeDocument,
  type KnowledgeDocumentDetail,
  type KnowledgeJob,
} from '../api';

/**
 * 知识库 `/admin/knowledge`（模块 A：知识问答本体）。
 *
 * 数据口径：contracts/openapi.yaml /knowledge/*（internal/api/store/knowledge.go）。
 * 上传 → 同步解析分块落库 → EMBED（向量检索）/EXTRACT（本体抽取）后台任务；
 * 任务进度在详情抽屉内轮询（5s，仅存在 QUEUED/RUNNING 任务时）。
 *
 * 严禁 mock：列表/详情/任务全部来自真实端点；嵌入模型未配置时上传会被
 * 服务端 400 明确拒绝（页面如实透出错误，不伪装成功）。
 */

const DOC_STATUS_TONE: Record<string, 'success' | 'error' | 'neutral' | 'warning'> = {
  READY: 'success',
  FAILED: 'error',
  DELETED: 'neutral',
};
const DOC_STATUS_LABEL: Record<string, string> = {
  READY: '就绪',
  FAILED: '失败',
  DELETED: '已删除',
};
const JOB_STATUS_TONE: Record<string, 'success' | 'error' | 'neutral' | 'warning'> = {
  PENDING: 'warning',
  RUNNING: 'warning',
  SUCCEEDED: 'success',
  FAILED: 'error',
};
const JOB_STATUS_LABEL: Record<string, string> = {
  PENDING: '排队中',
  RUNNING: '执行中',
  SUCCEEDED: '已完成',
  FAILED: '失败',
};
const JOB_KIND_LABEL: Record<string, string> = {
  EMBED: '向量嵌入',
  EXTRACT: '知识抽取',
};
const FILE_KIND_LABEL: Record<string, string> = {
  pdf: 'PDF',
  docx: 'Word',
  xlsx: 'Excel',
  md: 'Markdown',
  txt: '文本',
};

/** 文档状态徽标（未知状态原样展示，不编造映射）。 */
function docStatusBadge(status: string) {
  const tone = DOC_STATUS_TONE[status] ?? 'neutral';
  const label = DOC_STATUS_LABEL[status] ?? status;
  return <Badge tone={tone} title={`文档状态：${status}`}>{label}</Badge>;
}

/** 上传弹窗：本地选择文件（md/txt/pdf/docx/xlsx）+ 可选目标本体版本。 */
function UploadDialog({
  open, onClose,
}: { open: boolean; onClose: () => void }) {
  const upload = useUploadKnowledgeDocument();
  const ontologyOptions = useOntologyVersionOptions();
  const [fileName, setFileName] = useState('');
  const [fileSize, setFileSize] = useState(0);
  const [targetVersion, setTargetVersion] = useState('');
  const [picked, setPicked] = useState<File | null>(null);
  const [localErr, setLocalErr] = useState<string | null>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const reset = () => {
    setFileName('');
    setFileSize(0);
    setPicked(null);
    setTargetVersion('');
    setLocalErr(null);
    if (fileInputRef.current) fileInputRef.current.value = '';
  };

  const close = () => {
    if (upload.isPending) return;
    reset();
    onClose();
  };

  const pickFile = (f: File | null) => {
    setLocalErr(null);
    setPicked(f);
    setFileName(f?.name ?? '');
    setFileSize(f?.size ?? 0);
  };

  const submit = () => {
    setLocalErr(null);
    if (!picked) {
      setLocalErr('请先选择要上传的文档（md / txt / pdf / docx / xlsx）。');
      return;
    }
    upload.mutate(
      { file: picked, targetOntologyVersionId: targetVersion || undefined },
      { onSuccess: () => { reset(); onClose(); } },
    );
  };

  const ontologies = ontologyOptions.data?.items ?? [];
  const lastResult = upload.data;

  return (
    <Dialog open={open} onClose={close} title="上传知识文档" wide>
      <div className="dialog-form">
        <Field
          label="文档文件"
          required
          hint="支持 md / txt / pdf（文本型）/ docx / xlsx，50MB 上限；扫描版 PDF 无文本层会被明确拒绝，请转存为 docx/md 后上传。">
          <input
            ref={fileInputRef}
            type="file"
            accept=".md,.txt,.pdf,.docx,.xlsx"
            onChange={(e) => pickFile(e.target.files?.[0] ?? null)}
          />
        </Field>
        {picked ? (
          <p className="text-xs muted">
            已选择：<span className="mono">{fileName}</span>（{formatBytes(fileSize)}）
          </p>
        ) : null}
        <Field
          label="目标本体版本（可选）"
          hint="选择后除向量嵌入外还将执行知识抽取：按该本体版本的类/关系约束抽取三元组入图；不选则只做向量嵌入（检索可用）。">
          <Select
            value={targetVersion}
            onChange={(e) => setTargetVersion(e.target.value)}
            options={[
              { value: '', label: '不指定（仅向量嵌入）' },
              ...ontologies
                .filter((o) => o.latest_version_id)
                .map((o) => ({
                  value: o.latest_version_id as string,
                  label: `${o.name}（${o.latest_version ?? '—'}）`,
                })),
            ]}
          />
        </Field>
        {upload.isPending ? <p className="text-sm muted">正在上传并解析分块，请稍候…</p> : null}
        {localErr ? (
          <p className="text-sm" style={{ color: 'var(--color-error)' }}>{localErr}</p>
        ) : null}
        {upload.error ? (
          <div className="banner banner--warning" role="alert">
            <Icon name="alertTriangle" size={16} />
            <span>上传失败：{(upload.error as Error).message}</span>
          </div>
        ) : null}
        {lastResult ? (
          <div className="banner banner--success" role="status">
            <Icon name="circleCheck" size={16} />
            <span>
              已创建文档 <span className="mono">{lastResult.document.title}</span>，分块 {lastResult.document.chunk_count} 条，
              后台任务已排队。
              {lastResult.explain ? ` ${lastResult.explain}` : ''}
            </span>
          </div>
        ) : null}
        <div className="dialog-actions">
          <Button type="button" variant="ghost" onClick={close} disabled={upload.isPending}>取消</Button>
          <Button type="button" variant="primary" icon="upload" loading={upload.isPending} onClick={submit}>
            上传
          </Button>
        </div>
      </div>
    </Dialog>
  );
}

/** 单条摄取任务行（进度 + 重试）。 */
function JobRow({ job, onRetried }: { job: KnowledgeJob; onRetried: () => void }) {
  const retry = useRetryKnowledgeJob();
  const done = job.progress_total > 0 ? Math.round((job.progress_done / job.progress_total) * 100) : null;
  const failed = job.status === 'FAILED';
  return (
    <div style={{ borderBottom: '1px solid var(--border-subtle, #eee)', padding: '10px 0' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
        <Badge tone="info">{JOB_KIND_LABEL[job.kind] ?? job.kind}</Badge>
        <Badge tone={JOB_STATUS_TONE[job.status] ?? 'neutral'}>
          {JOB_STATUS_LABEL[job.status] ?? job.status}
        </Badge>
        <span className="text-xs muted">
          第 {job.attempt}/{job.max_attempts} 次尝试{job.stage ? ` · ${job.stage}` : ''}
          {done !== null ? ` · 进度 ${done}%（${job.progress_done}/${job.progress_total}）` : ''}
        </span>
        <span style={{ marginLeft: 'auto' }}>
          {failed ? (
            <Button
              size="sm" variant="secondary"
              loading={retry.isPending}
              onClick={() => retry.mutate(job.id, { onSuccess: onRetried })}>
              重试
            </Button>
          ) : null}
        </span>
      </div>
      {job.error ? (
        <p className="text-xs" style={{ color: 'var(--color-error)', marginTop: 6, overflowWrap: 'anywhere' }}>
          {job.error}
        </p>
      ) : null}
      {retry.error ? (
        <p className="text-xs" style={{ color: 'var(--color-error)', marginTop: 6 }}>
          重试失败：{(retry.error as Error).message}
        </p>
      ) : null}
    </div>
  );
}

/** 文档详情抽屉：元信息 + 摄取任务（含重试）+ 分块预览。 */
function DocumentDrawer({
  docId, onClose,
}: { docId: string | null; onClose: () => void }) {
  const detail = useKnowledgeDocument(docId);
  const d: KnowledgeDocumentDetail | undefined = detail.data;

  return (
    <Drawer open={docId !== null} onClose={onClose} title="文档详情" wide>
      {detail.isLoading || !d ? (
        <p className="text-sm muted">正在加载文档详情…</p>
      ) : (
        <div className="stack stack--tight">
          {detail.error ? <ErrorState error={detail.error} onRetry={() => void detail.refetch()} compact /> : null}
          <section>
            <h3 style={{ margin: 0, fontSize: 14 }}>基本信息</h3>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 4, marginTop: 8 }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                <span style={{ fontWeight: 600 }}>{d.title}</span>
                {docStatusBadge(d.status)}
              </div>
              <div className="text-xs muted">
                类型 {FILE_KIND_LABEL[d.file_kind] ?? d.file_kind} · 大小 {formatBytes(d.size_bytes)} · 分块 {d.chunk_count} 条
              </div>
              <div className="text-xs muted mono" style={{ overflowWrap: 'anywhere' }}>
                来源 {d.source_path || '—'} · SHA-256 {d.sha256?.slice(0, 16)}…
              </div>
              <div className="text-xs muted">
                上传于 {formatDateTime(d.created_at)} · 最近更新 {formatDateTime(d.updated_at)}
              </div>
              {d.error ? (
                <p className="text-sm" style={{ color: 'var(--color-error)' }}>{d.error}</p>
              ) : null}
            </div>
          </section>

          <section>
            <h3 style={{ margin: 0, fontSize: 14 }}>摄取任务</h3>
            <p className="text-xs muted" style={{ margin: '4px 0 8px' }}>
              EMBED 为分块生成向量（检索用）；EXTRACT 按目标本体约束抽取三元组入知识图谱。
            </p>
            {(d.jobs ?? []).length === 0 ? (
              <p className="text-sm muted">该文档没有摄取任务。</p>
            ) : (
              d.jobs?.map((job) => <JobRow key={job.id} job={job} onRetried={() => void detail.refetch()} />)
            )}
          </section>

          <section>
            <h3 style={{ margin: 0, fontSize: 14 }}>分块预览（前 {(d.chunks ?? []).length} 条）</h3>
            <div style={{ marginTop: 8, display: 'flex', flexDirection: 'column', gap: 8 }}>
              {(d.chunks ?? []).map((c) => (
                <div key={c.id} style={{ border: '1px solid var(--color-border)', borderRadius: 8, padding: 10 }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                    <span className="text-xs mono muted">分块 #{c.chunk_index}</span>
                    <Badge tone={c.has_embedding ? 'success' : 'neutral'} title={c.has_embedding ? '已生成向量' : '尚未生成向量'}>
                      {c.has_embedding ? '已嵌入' : '未嵌入'}
                    </Badge>
                  </div>
                  <p className="text-sm" style={{ marginTop: 6, whiteSpace: 'pre-wrap', margin: '6px 0 0' }}>
                    {c.content.length > 400 ? `${c.content.slice(0, 400)}…` : c.content}
                  </p>
                </div>
              ))}
            </div>
          </section>
        </div>
      )}
    </Drawer>
  );
}

export function KnowledgePage() {
  const [search, setSearch] = useState('');
  const pager = useCursorPagination();
  const query = useKnowledgeDocuments({ q: search, cursor: pager.cursor ?? undefined });
  const remove = useDeleteKnowledgeDocument();

  const [uploadOpen, setUploadOpen] = useState(false);
  const [detailId, setDetailId] = useState<string | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<KnowledgeDocument | null>(null);

  // 筛选变化必须回首页（否则带着旧页游标请求新筛选结果是假结果）。
  useEffect(() => {
    pager.reset();
  }, [search, pager.reset]);

  const columns: DataTableColumn<KnowledgeDocument>[] = [
    {
      key: 'title',
      header: '文档',
      render: (doc) => (
        <div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span style={{ fontWeight: 500 }}>{doc.title}</span>
            <Badge tone="info" title="文件类型">{FILE_KIND_LABEL[doc.file_kind] ?? doc.file_kind}</Badge>
          </div>
          <div className="text-xs muted" style={{ marginTop: 4, overflowWrap: 'anywhere' }}>
            {doc.source_path || '—'} · {formatBytes(doc.size_bytes)}
          </div>
        </div>
      ),
    },
    {
      key: 'chunk_count',
      header: '分块',
      tight: true,
      render: (doc) => <span className="mono">{doc.chunk_count}</span>,
    },
    {
      key: 'status',
      header: '状态',
      tight: true,
      render: (doc) => docStatusBadge(doc.status),
    },
    {
      key: 'created_at',
      header: '上传时间',
      tight: true,
      render: (doc) => <span className="text-sm">{formatDateTime(doc.created_at)}</span>,
    },
  ];

  return (
    <div className="page page--wide">
      <PageHeader
        icon="fileText"
        title="知识库"
        subtitle="企业文档上传后同步解析分块：向量嵌入支撑混合检索问答；指定目标本体版本时按本体约束抽取三元组入知识图谱，全过程可重试、可追溯。"
        actions={
          <Button variant="primary" icon="upload" onClick={() => setUploadOpen(true)}>
            上传文档
          </Button>
        }
      />

      {remove.isError ? (
        <div style={{ marginBottom: 16 }}>
          <ErrorState error={remove.error} onRetry={() => remove.reset()} compact />
        </div>
      ) : null}

      <ListFilterBar
        searchValue={search}
        onSearchChange={setSearch}
        searchPlaceholder="搜索文档标题 / 来源路径…"
      />

      <DataTable
        caption="知识文档列表"
        columns={columns}
        rows={query.data?.items ?? []}
        rowKey={(doc) => doc.id}
        loading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        emptyTitle="暂无文档"
        emptyDescription="上传企业制度文档（md / txt / pdf / docx / xlsx）后，平台将解析分块并生成向量索引，供知识问答混合检索引用。"
        nextCursor={query.data?.next_cursor ?? null}
        hasPrevPage={pager.hasPrevPage}
        onPrevPage={pager.goPrev}
        onNextPage={() => pager.goNext(query.data?.next_cursor ?? null)}
        rowActions={(doc) =>
          doc.status === 'DELETED' ? (
            <span className="muted text-sm">已删除</span>
          ) : (
            <>
              <Button variant="ghost" size="sm" onClick={() => setDetailId(doc.id)}>
                详情
              </Button>
              <Button
                variant="ghost" size="sm" iconOnly icon="ban"
                aria-label="删除文档"
                title="删除（软删除文档，分块与任务一并清理）"
                loading={remove.isPending && remove.variables === doc.id}
                onClick={() => setDeleteTarget(doc)}
              />
            </>
          )
        }
      />

      <UploadDialog open={uploadOpen} onClose={() => setUploadOpen(false)} />
      <DocumentDrawer docId={detailId} onClose={() => setDetailId(null)} />

      <ConfirmDialog
        open={deleteTarget !== null}
        title="删除知识文档"
        confirmLabel="删除文档"
        danger
        target={
          deleteTarget ? (
            <span className="mono">{deleteTarget.title}</span>
          ) : null
        }
        impact="软删除文档本体（保留审计追溯），分块与摄取任务将被物理清理；基于该文档的检索引用随之失效。"
        scope="当前租户"
        reversible={false}
        requiresApproval={false}
        verification="删除后列表中该文档消失；审计事件 knowledge.document_deleted 可在审计与证据页核对。"
        confirmPhrase={deleteTarget?.title}
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
