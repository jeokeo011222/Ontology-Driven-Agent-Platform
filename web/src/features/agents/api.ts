import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiDelete, apiGet, apiPatch, apiPost, fetchList, listFilterQuery, type ListQueryFilters } from '@/api/client';
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
  /**
   * 绑定的技能键列表（写入 dependency_manifest.skills）。服务端 fail-closed：
   * 任一 skill_key 不存在或没有 PUBLISHED 版本 → 404，整单拒绝。
   */
  skills?: string[];
}

/** `PATCH /api/v1/agents/{id}` 的请求体（agent_key 不可变，版本升级走 publish）。 */
export interface UpdateAgentBody {
  description?: string;
  domain?: string;
  /**
   * 绑定技能的**显式替换**语义：提供时整体替换最新版本的
   * dependency_manifest.skills（[] 表示清空绑定）；缺省不改动。
   */
  skills?: string[];
}

function invalidate(qc: ReturnType<typeof useQueryClient>) {
  void qc.invalidateQueries({ queryKey: queryKeys.agents.list() });
}

/**
 * Agent 详情（`GET /api/v1/agents/{id}`）。列表接口不投影 dependency_manifest，
 * 编辑弹窗的「绑定技能」初始值必须取自详情的 manifest.skills（真实数据源）。
 */
export interface AgentDetail extends AgentInfo {
  /** dependency_manifest JSON 字符串（服务端 ::text 投影），形如 {"skills":[...]} */
  dependency_manifest: string | null;
}

/** 从详情的 dependency_manifest 解析已绑定技能键列表（解析失败按空处理，不臆造）。 */
export function parseAgentSkills(detail: AgentDetail | undefined | null): string[] {
  if (!detail?.dependency_manifest) return [];
  try {
    const manifest = JSON.parse(detail.dependency_manifest) as { skills?: unknown };
    return Array.isArray(manifest.skills)
      ? manifest.skills.filter((s): s is string => typeof s === 'string')
      : [];
  } catch {
    return [];
  }
}

export function useAgentDetail(id: string | null) {
  return useQuery({
    queryKey: [...queryKeys.agents.detail(id ?? ''), id],
    queryFn: () => apiGet<AgentDetail>(`/agents/${encodeURIComponent(id ?? '')}`),
    enabled: id !== null,
  });
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
