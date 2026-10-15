import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiDelete, apiPatch, apiPost, fetchList, listFilterQuery, type ListQueryFilters } from '@/api/client';
import { queryKeys } from '@/api/query-keys';
import { newIdempotencyKey } from '@/lib/http';
import type { AgentInfo } from '@/types';

/** Agent 注册与发布（§7.1）。契约：`/api/v1/agents`、`/api/v1/agents/{id}/publish`（contracts/03 §2）。 */
export function useAgents(filters: ListQueryFilters = {}) {
  return useQuery({
    queryKey: [...queryKeys.agents.list(), filters],
    queryFn: () => fetchList<AgentInfo>('/agents', listFilterQuery(filters)),
  });
}

/**
 * `POST /api/v1/agents` 的请求体。
 *
 * `input_schema` / `output_schema` **必填且必须是合法 JSON**：服务端
 * `store.CreateAgent` 以 `json.Valid` 校验，缺省（nil）会被拒绝为
 * INVALID_ARGUMENT。因此调用方必须始终传入合法 JSON（至少 `{}`）。
 */
export interface CreateAgentBody {
  agent_key: string;
  domain: string;
  description?: string;
  version?: string;
  input_schema: unknown;
  output_schema: unknown;
}

/** `PATCH /api/v1/agents/{id}` 的请求体（agent_key 不可变，版本升级走 publish）。 */
export interface UpdateAgentBody {
  description?: string;
  domain?: string;
}

function invalidate(qc: ReturnType<typeof useQueryClient>) {
  void qc.invalidateQueries({ queryKey: queryKeys.agents.list() });
}

export function useCreateAgent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateAgentBody) =>
      apiPost<AgentInfo>('/agents', body, {
        idempotencyKey: newIdempotencyKey(`agent-create-${body.agent_key}`),
      }),
    onSuccess: () => invalidate(qc),
  });
}

export function useUpdateAgent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: UpdateAgentBody }) =>
      apiPatch<AgentInfo>(`/agents/${encodeURIComponent(id)}`, body, {
        idempotencyKey: newIdempotencyKey(`agent-update-${id}`),
      }),
    onSuccess: () => invalidate(qc),
  });
}

/** 软删除（status → DELETED）：保留审计链，不做物理删除。 */
export function useDeleteAgent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiDelete<void>(`/agents/${encodeURIComponent(id)}`, {
        idempotencyKey: newIdempotencyKey(`agent-delete-${id}`),
      }),
    onSuccess: () => invalidate(qc),
  });
}

/**
 * 发布（同步 200，**不返回 202 任务引用**）。
 *
 * 权威：contracts/openapi.yaml `/api/v1/agents/{id}/publish` 的 200 响应
 *   （`{agent_id, version_id, status}`），实现见 `Store.PublishAgent`。
 * 门禁只放行 status ∈ {DRAFT, VALIDATING, APPROVED} 的最新版本，其余返回 409。
 */
export interface AgentPublishResult {
  agent_id: string;
  version_id: string;
  status: string;
}

export function usePublishAgent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiPost<AgentPublishResult>(`/agents/${encodeURIComponent(id)}/publish`, undefined, {
        idempotencyKey: newIdempotencyKey(`agent-publish-${id}`),
      }),
    onSuccess: () => invalidate(qc),
  });
}

/** `POST /api/v1/agents/generate` 的请求体（读需求/制度文档 → 大模型 → DRAFT）。 */
export interface GenerateAgentBody {
  instructions?: string;
  source_path?: string;
  agent_key?: string;
  domain?: string;
  model_id?: string;
  max_source_chars?: number;
}

export interface GenerateAgentResult {
  id: string;
  agent_key: string;
  domain?: string;
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

/**
 * 由需求说明 / 制度文档自动生成 Agent 草稿（大模型 → DRAFT）。
 * 未装配真实 HTTP 模型端点时服务端返回 503 并说明原因，前端如实展示，不做本地兜底。
 */
export function useGenerateAgent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: GenerateAgentBody) =>
      apiPost<GenerateAgentResult>('/agents/generate', body, {
        idempotencyKey: newIdempotencyKey(`agent-generate-${body.source_path ?? body.agent_key ?? 'instructions'}`),
      }),
    onSuccess: () => invalidate(qc),
  });
}
