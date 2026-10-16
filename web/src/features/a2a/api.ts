import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { API_BASE, apiDelete, apiPatch, apiPost, fetchList, listFilterQuery, getSessionToken } from '@/api/client';
import { queryKeys } from '@/api/query-keys';
import { newIdempotencyKey } from '@/lib/http';

/**
 * A2A 协同（模块 C）：平台作为 A2A Server 发布 Agent Card + JSON-RPC 2.0
 * 任务接口；外部 A2A Agent 注册表管理与连通性探测。
 *
 * 契约（contracts/openapi.yaml /a2a/*）：
 *   GET    /.well-known/agent.json               Agent Card（认证链内，协议标准路径）
 *   GET    /api/v1/a2a/remote-agents             远程 Agent 注册表（cursor 10/页）
 *   POST   /api/v1/a2a/remote-agents             注册（fail-closed 连通性探测）
 *   PATCH  /api/v1/a2a/remote-agents/{id}        更新（auth_header_ref 仅环境变量引用，明文不落库）
 *   DELETE /api/v1/a2a/remote-agents/{id}
 *   POST   /api/v1/a2a/remote-agents/{id}/probe  连接测试 = 拉取对方 /.well-known/agent.json
 *   POST   /api/v1/a2a                           JSON-RPC 2.0 网关（tasks/list 等）
 */

/** Agent Card（平台自发布 + 远程 Agent 探测共用结构子集）。 */
export interface A2AAgentCard {
  name: string;
  description?: string;
  url?: string;
  version?: string;
  protocol_version?: string;
  capabilities?: Record<string, unknown>;
  skills?: { id?: string; name?: string; description?: string }[];
  [key: string]: unknown;
}

/** a2a.remote_agents 行（凭据仅环境变量引用名，绝无明文）。 */
export interface A2ARemoteAgent {
  id: string;
  name: string;
  base_url: string;
  auth_header_name?: string;
  auth_header_ref?: string;
  capabilities?: Record<string, unknown>;
  tool_whitelist?: string[] | null;
  status: 'ACTIVE' | 'DISABLED' | string;
  last_probe_at?: string;
  last_probe_error?: string;
  created_at: string;
  updated_at: string;
}

/** POST /a2a/remote-agents 请求体（服务端 DisallowUnknownFields）。 */
export interface A2ARemoteAgentCreate {
  name: string;
  base_url: string;
  auth_header_name?: string;
  auth_header_ref?: string;
  capabilities?: Record<string, unknown>;
  tool_whitelist?: string[];
}

export interface A2ARemoteAgentPatch {
  name?: string;
  base_url?: string;
  auth_header_name?: string | null;
  auth_header_ref?: string | null;
  capabilities?: Record<string, unknown>;
  tool_whitelist?: string[] | null;
  status?: 'ACTIVE' | 'DISABLED';
}

/** A2A 任务视图（JSON-RPC 网关 tasks/list 返回的 run 映射）。 */
export interface A2ATaskView {
  id: string;
  context_id?: string;
  status: { state: string; message?: string };
  artifacts?: unknown[];
  created_at?: string;
  updated_at?: string;
}

/** 探测结果（POST /{id}/probe）。 */
export interface A2AProbeResult {
  id: string;
  status: string;
  last_probe_at?: string;
  last_probe_error?: string;
  agent_card?: A2AAgentCard | null;
}

export function useRemoteAgents(filters: { q?: string; status?: string } = {}) {
  return useQuery({
    queryKey: [queryKeys.a2a.remoteAgents(), filters],
    queryFn: () =>
      fetchList<A2ARemoteAgent>('/a2a/remote-agents', listFilterQuery(filters)),
  });
}

/**
 * 拉取平台 Agent Card（GET /.well-known/agent.json）。
 *
 * 该路径不在 /api/v1 前缀下（协议标准路径），不能走 apiGet（会拼错 URL）；
 * 开发环境由 vite 代理、生产由 nginx 反代到 API 服务。认证沿用 Bearer Token
 * + 会话 Cookie（该端点在认证链内，未登录直接 401——如实展示，不伪装）。
 */
export async function fetchAgentCard(): Promise<A2AAgentCard> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  const token = getSessionToken();
  if (token) headers.Authorization = `Bearer ${token}`;
  const res = await fetch(`${API_BASE}/.well-known/agent.json`, {
    headers,
    credentials: 'same-origin',
  });
  if (!res.ok) {
    throw new Error(`获取 Agent Card 失败（HTTP ${res.status}）`);
  }
  return (await res.json()) as A2AAgentCard;
}

export function useAgentCard(enabled = true) {
  return useQuery({
    queryKey: queryKeys.a2a.agentCard(),
    enabled,
    queryFn: fetchAgentCard,
    staleTime: 30_000,
    retry: 1,
  });
}

export function useCreateRemoteAgent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: A2ARemoteAgentCreate) =>
      apiPost<A2ARemoteAgent>('/a2a/remote-agents', body, {
        idempotencyKey: newIdempotencyKey(`a2a-agent-create-${body.name}`),
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.a2a.remoteAgents() });
    },
  });
}

export function useUpdateRemoteAgent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: A2ARemoteAgentPatch }) =>
      apiPatch<A2ARemoteAgent>(`/a2a/remote-agents/${encodeURIComponent(id)}`, body, {
        idempotencyKey: newIdempotencyKey(`a2a-agent-update-${id}`),
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.a2a.remoteAgents() });
    },
  });
}

export function useDeleteRemoteAgent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiDelete<{ id: string; deleted: boolean }>(`/a2a/remote-agents/${encodeURIComponent(id)}`, {
        idempotencyKey: newIdempotencyKey(`a2a-agent-delete-${id}`),
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.a2a.remoteAgents() });
    },
  });
}

/** 连接测试：拉取对方 Agent Card，fail-closed（拉取失败明确报错不降级）。 */
export function useProbeRemoteAgent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiPost<A2AProbeResult>(`/a2a/remote-agents/${encodeURIComponent(id)}/probe`, undefined, {
        idempotencyKey: newIdempotencyKey(`a2a-agent-probe-${id}`),
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.a2a.remoteAgents() });
    },
  });
}

/** JSON-RPC 2.0 网关调用（tasks/list / tasks/get / message/send 等）。 */
export async function a2aRpc<T>(method: string, params: Record<string, unknown> | undefined): Promise<T> {
  const payload = {
    jsonrpc: '2.0',
    id: `web-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
    method,
    ...(params !== undefined ? { params } : {}),
  };
  const res = await apiPost<{ result?: T; error?: { code: number; message: string } }>(
    '/a2a',
    payload,
    { idempotencyKey: newIdempotencyKey(`a2a-rpc-${method}`) },
  );
  if (res.error) {
    throw new Error(`JSON-RPC ${method} 失败（${res.error.code}）：${res.error.message}`);
  }
  return res.result as T;
}

/** A2A 任务列表（tasks/list，cursor 透传）。 */
export function useA2ATasks(enabled = true) {
  return useQuery({
    queryKey: queryKeys.a2a.tasks(),
    enabled,
    queryFn: () =>
      a2aRpc<{ tasks: A2ATaskView[]; next_cursor?: string | null }>('tasks/list', { limit: 50 }),
    refetchInterval: 10_000,
  });
}
