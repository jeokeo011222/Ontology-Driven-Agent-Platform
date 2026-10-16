import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiDelete, apiGet, apiPatch, apiPost, fetchList, listFilterQuery } from '@/api/client';
import { queryKeys } from '@/api/query-keys';
import { newIdempotencyKey } from '@/lib/http';
import type { OntologySummary } from '@/types';

/**
 * 推理引擎（模块 B）：Go 自研前向/后向/增量推理 + gRPC 契约（语义对齐 OxiRS）。
 *
 * 契约（contracts/openapi.yaml /reasoning/*）：
 *   GET/POST    /api/v1/reasoning/rules       规则列表（cursor 10/页 + kind 筛选）/ 注册 CUSTOM 规则
 *   GET         /api/v1/reasoning/rules/{id}
 *   PATCH       /api/v1/reasoning/rules/{id}  内置规则仅可启停（name/description 409）
 *   DELETE      /api/v1/reasoning/rules/{id}  内置规则 409
 *   POST        /api/v1/reasoning/runs        对指定本体版本图执行推理（同步返回终态）
 *   GET         /api/v1/reasoning/runs        运行记录（cursor 10/页）
 */

/** reason.rules 行（body 为 premises/conclusion 三元组模式的 JSON 文本）。 */
export interface ReasoningRule {
  id: string;
  rule_key: string;
  name: string;
  kind: 'RDFS' | 'OWL2RL' | 'CUSTOM' | string;
  body: string;
  description?: string;
  enabled: boolean;
  is_builtin: boolean;
  created_by?: string;
  created_at: string;
  updated_at: string;
}

/** 三元组模式（空串=匿名变量，"?x"=具名变量）。 */
export interface ReasoningTriplePattern {
  subject: string;
  predicate: string;
  object: string;
}

/** 词法三元组（`"lex"` 字面值 / `_:b` 空白节点 / 其余 IRI）。 */
export interface ReasoningLexicalTriple {
  subject: string;
  predicate: string;
  object: string;
}

/** POST /reasoning/rules 请求体（服务端 DisallowUnknownFields 严格校验）。 */
export interface CreateReasoningRuleBody {
  rule_key: string;
  name: string;
  kind: 'CUSTOM';
  premises: ReasoningTriplePattern[];
  conclusion: ReasoningTriplePattern;
  description?: string;
}

/** POST /reasoning/runs 请求体。 */
export interface CreateReasoningRunBody {
  ontology_version_id: string;
  mode: 'FORWARD' | 'BACKWARD' | 'INCREMENTAL';
  rule_keys?: string[];
  max_iterations?: number;
  goal?: ReasoningTriplePattern;
  added_triples?: ReasoningLexicalTriple[];
}

/** reason.inference_runs 行。 */
export interface ReasoningRun {
  id: string;
  graph_iri: string;
  mode: 'FORWARD' | 'BACKWARD' | 'INCREMENTAL' | string;
  rule_keys?: string;
  input_triple_count: number;
  derived_triple_count: number;
  status: 'SUCCEEDED' | 'FAILED' | string;
  error?: string;
  duration_ms: number;
  triggered_by?: string;
  created_at: string;
}

/** POST /reasoning/runs 响应：运行记录 + 模式相关结果字段。 */
export interface ReasoningRunResult {
  run: ReasoningRun;
  derived_triple_count?: number;
  iterations?: number;
  duration_ms?: number;
  added_triples?: ReasoningLexicalTriple[];
  inserted_count?: number;
  derived_triples?: ReasoningLexicalTriple[];
  proved?: boolean;
  proof_trace?: unknown;
  bindings?: unknown;
}

export function useReasoningRules(filters: { q?: string; kind?: string } = {}) {
  return useQuery({
    queryKey: [queryKeys.reasoning.rules(), filters],
    queryFn: () =>
      fetchList<ReasoningRule>('/reasoning/rules', {
        limit: 10,
        q: filters.q || undefined,
        // 服务端筛选参数名是 kind；空串不下发 = 全部。
        kind: filters.kind || undefined,
      }),
  });
}

/** 目标本体版本选项（推理运行需要指定本体版本 → 图 IRI）。 */
export function useReasoningOntologyOptions() {
  return useQuery({
    queryKey: [...queryKeys.ontologies.list(), { purpose: 'reasoning-run', limit: 200 }],
    queryFn: () => fetchList<OntologySummary>('/ontologies', listFilterQuery({}, 200)),
  });
}

export function useCreateReasoningRule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateReasoningRuleBody) =>
      apiPost<ReasoningRule>('/reasoning/rules', body, {
        idempotencyKey: newIdempotencyKey(`reasoning-rule-create-${body.rule_key}`),
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.reasoning.rules() });
    },
  });
}

/** PATCH 语义：字段缺省不改动；内置规则仅允许 enabled。 */
export interface ReasoningRulePatch {
  enabled?: boolean;
  name?: string;
  description?: string;
}

export function useUpdateReasoningRule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: ReasoningRulePatch }) =>
      apiPatch<ReasoningRule>(`/reasoning/rules/${encodeURIComponent(id)}`, body, {
        idempotencyKey: newIdempotencyKey(`reasoning-rule-update-${id}`),
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.reasoning.rules() });
    },
  });
}

export function useDeleteReasoningRule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiDelete<{ id: string; deleted: boolean }>(`/reasoning/rules/${encodeURIComponent(id)}`, {
        idempotencyKey: newIdempotencyKey(`reasoning-rule-delete-${id}`),
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.reasoning.rules() });
    },
  });
}

export function useReasoningRuns() {
  return useQuery({
    queryKey: [...queryKeys.reasoning.runs(), 'page'],
    queryFn: () => fetchList<ReasoningRun>('/reasoning/runs', listFilterQuery()),
  });
}

export function useCreateReasoningRun() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateReasoningRunBody) =>
      apiPost<ReasoningRunResult>('/reasoning/runs', body, {
        idempotencyKey: newIdempotencyKey(`reasoning-run-${body.ontology_version_id}-${body.mode}`),
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.reasoning.runs() });
    },
  });
}

export function useReasoningRule(id: string | null) {
  return useQuery({
    queryKey: [...queryKeys.reasoning.rules(), 'detail', id],
    enabled: Boolean(id),
    queryFn: () => apiGet<ReasoningRule>(`/reasoning/rules/${encodeURIComponent(String(id))}`),
  });
}
