import { useMutation, useQuery } from '@tanstack/react-query';
import { API_BASE, apiGet, apiPost, fetchList, getSessionToken } from '@/api/client';
import { queryKeys } from '@/api/query-keys';

/**
 * 本体 Action 管理（模块 D）：QUERY/UPDATE/INFER/ACTION/TOOL 五类动作
 * 一等节点 + PROV-O 溯源命名图 + pgvector 相似决策检索。
 *
 * 契约（contracts/openapi.yaml /actions/*）：
 *   GET  /api/v1/actions                    动作列表（type/outcome/actor_kind/from/to 筛选 + cursor 10/页）
 *   GET  /api/v1/actions/{id}               详情（含 PROV-O 三元组 + 关联 Run）
 *   POST /api/v1/actions/{id}/similar       pgvector 余弦 topK 相似动作检索
 *   GET  /api/v1/actions/{id}/prov.ttl      PROV-O Turtle 导出（text/turtle）
 */

export const ACTION_TYPE_LABEL: Record<string, string> = {
  QUERY: '查询',
  UPDATE: '更新',
  INFER: '推理',
  ACTION: '动作',
  TOOL: '工具调用',
};

export const ACTION_OUTCOME_LABEL: Record<string, string> = {
  SUCCESS: '成功',
  ERROR: '错误',
  TIMEOUT: '超时',
  CANCELED: '已取消',
};

/** action.actions 行（列表口径）。 */
export interface ActionRecord {
  id: string;
  run_id?: string;
  type: 'QUERY' | 'UPDATE' | 'INFER' | 'ACTION' | 'TOOL' | string;
  title: string;
  summary?: string;
  input_digest?: string;
  output_digest?: string;
  outcome: string;
  prov_graph_id?: string;
  occurred_at: string;
  actor?: string;
  actor_kind?: string;
  has_embedding?: boolean;
}

/** PROV-O 词法项（图存储词法三元组的节点：IRI / 字面值 / 空白节点）。 */
export interface ProvTerm {
  kind: 'IRI' | 'LITERAL' | 'BNODE' | string;
  value: string;
  datatype?: string;
  language_tag?: string;
}

/** 详情响应：动作行 + PROV-O 三元组 + 关联 Run 概要。 */
export interface ActionDetail extends ActionRecord {
  prov_triples?: { subject: ProvTerm; predicate: ProvTerm; object: ProvTerm }[];
  run?: {
    id: string;
    status?: string;
    agent_key?: string;
    created_at?: string;
  } | null;
}

/** POST /actions/{id}/similar 请求体。 */
export interface SimilarActionsBody {
  top_k?: number;
}

export interface SimilarActionItem {
  id: string;
  type: string;
  title: string;
  outcome: string;
  occurred_at: string;
  distance?: number;
  similarity?: number;
}

export interface SimilarActionsResult {
  results: SimilarActionItem[];
}

export type ActionListFilters = {
  type?: string;
  outcome?: string;
  actor_kind?: string;
  from?: string;
  to?: string;
  q?: string;
};

export function useActions(filters: ActionListFilters = {}) {
  return useQuery({
    queryKey: [queryKeys.actions.list(), filters],
    // 动作列表的筛选参数是 type/outcome/actor_kind/from/to（不是通用 status），
    // 服务端 /actions 不认 status；空值不下发。
    queryFn: () =>
      fetchList<ActionRecord>('/actions', {
        limit: 10,
        q: filters.q || undefined,
        type: filters.type || undefined,
        outcome: filters.outcome || undefined,
        actor_kind: filters.actor_kind || undefined,
        from: filters.from || undefined,
        to: filters.to || undefined,
      }),
  });
}

export function useAction(id: string | null) {
  return useQuery({
    queryKey: queryKeys.actions.detail(id ?? ''),
    enabled: Boolean(id),
    queryFn: () => apiGet<ActionDetail>(`/actions/${encodeURIComponent(String(id))}`),
  });
}

export function useSimilarActions(id: string | null) {
  return useMutation({
    mutationFn: (topK: number) =>
      apiPost<SimilarActionsResult>(`/actions/${encodeURIComponent(String(id))}/similar`, {
        top_k: topK,
      }),
  });
}

/**
 * PROV-O Turtle 导出（GET /actions/{id}/prov.ttl，text/turtle）。
 *
 * 该端点返回非 JSON 载荷，不能走 apiGet（会按 JSON 解析）；直接 fetch
 * 携带 Bearer Token 下载文本。
 */
export async function fetchProvTurtle(id: string): Promise<string> {
  const headers: Record<string, string> = { Accept: 'text/turtle' };
  const token = getSessionToken();
  if (token) headers.Authorization = `Bearer ${token}`;
  const res = await fetch(`${API_BASE}/api/v1/actions/${encodeURIComponent(id)}/prov.ttl`, {
    headers,
    credentials: 'same-origin',
  });
  if (!res.ok) {
    throw new Error(`导出 PROV-O 失败（HTTP ${res.status}）`);
  }
  return res.text();
}
