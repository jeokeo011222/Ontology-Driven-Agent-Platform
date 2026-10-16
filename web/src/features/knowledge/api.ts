import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  apiDelete,
  apiGet,
  apiPost,
  apiUpload,
  fetchList,
  listFilterQuery,
  type ListQueryFilters,
} from '@/api/client';
import { queryKeys } from '@/api/query-keys';
import { newIdempotencyKey } from '@/lib/http';
import type { OntologySummary } from '@/types';

/**
 * 知识问答本体（模块 A）：文档上传 → 同步解析分块 → EMBED/EXTRACT 后台任务。
 *
 * 契约（contracts/openapi.yaml）：
 *   POST   /api/v1/knowledge/documents        multipart 上传（file + 可选 target_ontology_version_id）
 *   GET    /api/v1/knowledge/documents        cursor 10/页 + ?q=
 *   GET    /api/v1/knowledge/documents/{id}   详情（chunks 预览 + jobs）
 *   POST   /api/v1/knowledge/jobs/{id}/retry  仅 FAILED 可重试
 *   DELETE /api/v1/knowledge/documents/{id}   软删文档（分块/任务物理删）
 */

/** kb.ingest_jobs 行（摄取任务：EMBED 向量嵌入 / EXTRACT 本体知识抽取）。 */
export interface KnowledgeJob {
  id: string;
  kind: 'EMBED' | 'EXTRACT';
  target_ontology_version_id?: string;
  embedding_model?: string;
  status: 'PENDING' | 'RUNNING' | 'SUCCEEDED' | 'FAILED' | string;
  attempt: number;
  max_attempts: number;
  progress_done: number;
  progress_total: number;
  stage?: string;
  error?: string;
  lease_owner?: string;
  next_run_at?: string;
  created_at: string;
  updated_at: string;
}

/** kb.chunks 行（分块预览，前 50 条）。 */
export interface KnowledgeChunk {
  id: string;
  chunk_index: number;
  content: string;
  has_embedding: boolean;
  embedding_model?: string;
  created_at: string;
}

/** kb.documents 行。 */
export interface KnowledgeDocument {
  id: string;
  title: string;
  source_path: string;
  file_kind: 'pdf' | 'docx' | 'xlsx' | 'md' | 'txt' | string;
  size_bytes: number;
  sha256: string;
  chunk_count: number;
  status: 'READY' | 'FAILED' | 'DELETED' | string;
  error?: string;
  uploaded_by: string;
  created_at: string;
  updated_at: string;
}

/** GET /documents/{id} 详情响应：文档行 + chunks 预览 + 摄取任务列表。 */
export interface KnowledgeDocumentDetail extends KnowledgeDocument {
  chunks?: KnowledgeChunk[];
  jobs?: KnowledgeJob[];
}

/** POST /knowledge/documents 上传响应（document + 未指明本体版本时的 explain）。 */
export interface KnowledgeUploadResult {
  document: KnowledgeDocument;
  explain?: string;
}

export function useKnowledgeDocuments(filters: ListQueryFilters = {}) {
  return useQuery({
    queryKey: [...queryKeys.knowledge.documents(), filters],
    queryFn: () => fetchList<KnowledgeDocument>('/knowledge/documents', listFilterQuery(filters)),
  });
}

export function useKnowledgeDocument(id: string | null) {
  return useQuery({
    queryKey: queryKeys.knowledge.detail(id ?? ''),
    enabled: Boolean(id),
    // 有任务在跑时轮询（PENDING/RUNNING 任一存在即 5s 轮询），终态停止。
    refetchInterval: (query) => {
      const jobs = query.state.data?.jobs ?? [];
      const pending = jobs.some((j) => j.status === 'PENDING' || j.status === 'RUNNING');
      return pending ? 5000 : false;
    },
    queryFn: () => apiGet<KnowledgeDocumentDetail>(`/knowledge/documents/${encodeURIComponent(String(id))}`),
  });
}

/** 目标本体版本选项：复用 GET /ontologies（真实列表，200 行/页覆盖选择场景）。 */
export function useOntologyVersionOptions() {
  return useQuery({
    queryKey: [...queryKeys.ontologies.list(), { purpose: 'kb-target', limit: 200 }],
    queryFn: () => fetchList<OntologySummary>('/ontologies', listFilterQuery({}, 200)),
  });
}

/**
 * 上传知识文档（multipart：file + 可选 target_ontology_version_id）。
 *
 * 未指明目标本体版本时服务端只建 EMBED 任务（向量检索可用），跳过知识抽取，
 * 响应带 explain 说明。白名单 pdf/docx/xlsx/md/txt，50MB 上限；扫描版 PDF
 * 会被服务端明确拒绝（415/4xx 结构化错误，不静默入库）。
 */
export function useUploadKnowledgeDocument() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ file, targetOntologyVersionId }: { file: File; targetOntologyVersionId?: string }) => {
      const form = new FormData();
      form.append('file', file);
      if (targetOntologyVersionId) form.append('target_ontology_version_id', targetOntologyVersionId);
      return apiUpload<KnowledgeUploadResult>('/knowledge/documents', form, {
        // 文件名可能含中文：scope 经 FNV 摘要净化（lib/http），保证键值合法且唯一。
        idempotencyKey: newIdempotencyKey(`kb-upload-${file.name}-${file.size}`),
      });
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.knowledge.documents() });
    },
  });
}

export function useRetryKnowledgeJob() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (jobId: string) =>
      apiPost<KnowledgeJob>(`/knowledge/jobs/${encodeURIComponent(jobId)}/retry`, undefined, {
        idempotencyKey: newIdempotencyKey(`kb-job-retry-${jobId}`),
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.knowledge.documents() });
    },
  });
}

export function useDeleteKnowledgeDocument() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiDelete<{ id: string; status: string }>(`/knowledge/documents/${encodeURIComponent(id)}`, {
        idempotencyKey: newIdempotencyKey(`kb-doc-delete-${id}`),
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.knowledge.documents() });
    },
  });
}

/** 字节数 → 可读大小（KB/MB）。 */
export function formatBytes(n?: number | null): string {
  if (n === undefined || n === null || Number.isNaN(n) || n < 0) return '—';
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(2)} MB`;
}
