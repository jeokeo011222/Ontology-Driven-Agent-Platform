import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiDelete, apiPatch, apiPost, fetchList, listFilterQuery, type ListQueryFilters } from '@/api/client';
import { queryKeys } from '@/api/query-keys';
import { newIdempotencyKey } from '@/lib/http';
import type { PlanTemplate, WorkflowInfo } from '@/types';

/**
 * 工作流与 Harness（§7.3）。
 *
 * 两类资产并存：
 *   - **插件式工作流**（本文件 useWorkflows 等）：`/api/v1/workflows` —— registry.workflows
 *     定义表 + 不可变版本表，workflow 以 plugin_kind='WORKFLOW' 登记为插件资产，
 *     与 Agent / Tool 完全同构（新建 / 编辑 / 删除 / AI 生成 / 发布）。
 *   - **计划模板资产**（usePlanTemplates）：契约裁决（contracts/03 §4）下
 *     `workflows` 的模板资产路径 `/api/v1/plan-templates`，执行一律走 `/api/v1/runs`。
 */

export function useWorkflows(filters: ListQueryFilters = {}) {
  return useQuery({
    queryKey: [...queryKeys.workflows.list(), filters],
    queryFn: () => fetchList<WorkflowInfo>('/workflows', listFilterQuery(filters)),
  });
}

/** `POST /api/v1/workflows` 的请求体（definition 为编排文档 JSON）。 */
export interface CreateWorkflowBody {
  workflow_key: string;
  name: string;
  description?: string;
  version?: string;
  definition: unknown;
  input_schema?: unknown;
  output_schema?: unknown;
}

/** `PATCH /api/v1/workflows/{id}` 的请求体（workflow_key 不可变）。 */
export interface UpdateWorkflowBody {
  name?: string;
  description?: string;
}

function invalidate(qc: ReturnType<typeof useQueryClient>) {
  void qc.invalidateQueries({ queryKey: queryKeys.workflows.list() });
}

export function useCreateWorkflow() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateWorkflowBody) =>
      apiPost<WorkflowInfo>('/workflows', body, {
        idempotencyKey: newIdempotencyKey(`workflow-create-${body.workflow_key}`),
      }),
    onSuccess: () => invalidate(qc),
  });
}

export function useUpdateWorkflow() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: UpdateWorkflowBody }) =>
      apiPatch<WorkflowInfo>(`/workflows/${encodeURIComponent(id)}`, body, {
        idempotencyKey: newIdempotencyKey(`workflow-update-${id}`),
      }),
    onSuccess: () => invalidate(qc),
  });
}

/** 软删除（status → DELETED）。 */
export function useDeleteWorkflow() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiDelete<void>(`/workflows/${encodeURIComponent(id)}`, {
        idempotencyKey: newIdempotencyKey(`workflow-delete-${id}`),
      }),
    onSuccess: () => invalidate(qc),
  });
}

/**
 * 发布：绑定不可变版本；已发布版本不得原地修改。
 *
 * 同步 200（**不返回 202 任务引用**）。权威：contracts/openapi.yaml
 *   `/api/v1/workflows/{id}/publish` 的 200 响应（`{workflow_id, version_id, status}`），
 *   实现见 `Store.PublishWorkflow`。
 */
export interface WorkflowPublishResult {
  workflow_id: string;
  version_id: string;
  status: string;
}

export function usePublishWorkflow() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiPost<WorkflowPublishResult>(`/workflows/${encodeURIComponent(id)}/publish`, undefined, {
        idempotencyKey: newIdempotencyKey(`workflow-publish-${id}`),
      }),
    onSuccess: () => invalidate(qc),
  });
}

/** `POST /api/v1/workflows/generate` 的请求体（读需求/制度文档 → 大模型 → DRAFT）。 */
export interface GenerateWorkflowBody {
  instructions?: string;
  source_path?: string;
  workflow_key?: string;
  model_id?: string;
  max_source_chars?: number;
}

export interface GenerateWorkflowResult {
  id: string;
  workflow_key: string;
  name?: string;
  status?: string;
  generation?: {
    source_path?: string;
    source_files?: { path: string; kind: string; chars: number }[];
    source_chars?: number;
    truncated?: boolean;
    warnings?: string[];
    model?: Record<string, unknown>;
  };
}

export function useGenerateWorkflow() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: GenerateWorkflowBody) =>
      apiPost<GenerateWorkflowResult>('/workflows/generate', body, {
        idempotencyKey: newIdempotencyKey(
          `workflow-generate-${body.source_path ?? body.workflow_key ?? 'instructions'}`,
        ),
      }),
    onSuccess: () => invalidate(qc),
  });
}

/** 计划模板资产（`/api/v1/plan-templates`，cursor 分页，10 行/页）。 */
export function usePlanTemplates(cursor?: string) {
  return useQuery({
    queryKey: [...queryKeys.planTemplates.list(), cursor ?? ''],
    queryFn: () => fetchList<PlanTemplate>('/plan-templates', { limit: 10, cursor: cursor || undefined }),
  });
}
