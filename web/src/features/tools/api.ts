import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiDelete, apiPatch, apiPost, fetchList, listFilterQuery, type ListQueryFilters } from '@/api/client';
import { queryKeys } from '@/api/query-keys';
import { newIdempotencyKey } from '@/lib/http';
import type { EffectClass, ToolInfo } from '@/types';

/** Tool Registry（§7.2）。契约：`/api/v1/tools`、`/api/v1/tools/{id}/invoke`（contracts/03 §2）。 */
export function useTools(filters: ListQueryFilters = {}) {
  return useQuery({
    queryKey: [...queryKeys.tools.list(), filters],
    queryFn: () => fetchList<ToolInfo>('/tools', listFilterQuery(filters)),
  });
}

/**
 * 脚本执行契约（execution_config，adapter=script）。
 *
 * WASM 脚本在服务端 wazero 运行时内纯计算执行：无网络、无文件访问、固定只读，
 * 因此 effect_class 必须为 READ（服务端登记校验强制）。wasm_b64 是 .wasm 模块的
 * base64 编码（解码后上限 8MB）。
 */
export interface ScriptExecutionConfig {
  adapter: 'script';
  language: 'wasm';
  wasm_b64: string;
  /** 运行超时毫秒，缺省 30000，上限 300000。 */
  timeout_ms?: number;
  /** stdout 输出上限字节，缺省 1MB，上限 8MB；超限直接报错（不静默截断）。 */
  max_output_bytes?: number;
}

/**
 * `POST /api/v1/tools` 的请求体。
 *
 * `input_schema` / `output_schema` **必填且必须是合法 JSON**：服务端
 * `store.CreateTool` 以 `json.Valid` 校验，缺省（nil）会被拒绝为
 * INVALID_ARGUMENT。因此调用方必须始终传入合法 JSON（至少 `{}`）。
 *
 * `execution_config` 可选：携带 adapter=script 的执行契约时，首个版本直接以
 * PUBLISHED 登记（平台无工具版本发布链路，InvokeTool 只放行 PUBLISHED）。
 */
export interface CreateToolBody {
  tool_key: string;
  description?: string;
  version?: string;
  input_schema: unknown;
  output_schema: unknown;
  capabilities?: string[];
  effect_class?: EffectClass;
  execution_config?: ScriptExecutionConfig;
}

/** `PATCH /api/v1/tools/{id}` 的请求体（tool_key 与副作用等级不可变）。 */
export interface UpdateToolBody {
  description?: string;
}

function invalidate(qc: ReturnType<typeof useQueryClient>) {
  void qc.invalidateQueries({ queryKey: queryKeys.tools.list() });
}

export function useCreateTool() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateToolBody) =>
      apiPost<ToolInfo>('/tools', body, {
        idempotencyKey: newIdempotencyKey(`tool-create-${body.tool_key}`),
      }),
    onSuccess: () => invalidate(qc),
  });
}

export function useUpdateTool() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: UpdateToolBody }) =>
      apiPatch<ToolInfo>(`/tools/${encodeURIComponent(id)}`, body, {
        idempotencyKey: newIdempotencyKey(`tool-update-${id}`),
      }),
    onSuccess: () => invalidate(qc),
  });
}

/** 软删除（status → DELETED）。 */
export function useDeleteTool() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiDelete<void>(`/tools/${encodeURIComponent(id)}`, {
        idempotencyKey: newIdempotencyKey(`tool-delete-${id}`),
      }),
    onSuccess: () => invalidate(qc),
  });
}

/** `POST /api/v1/tools/{id}/invoke` 的请求体：待校验的参数对象。 */
export interface InvokeToolBody {
  args: Record<string, unknown>;
}

/**
 * 工具调用记录行（runtime.tool_invocations，GET /tools/{id}/invocations）。
 */
export interface ToolInvocation {
  id: string;
  tool_id: string;
  tool_version_id: string;
  tool_key: string;
  effect_class: string;
  status: string;
  input_payload: string | null;
  output_payload: string | null;
  error_code: string | null;
  error_summary: string | null;
  requested_by: string;
  approved_by: string | null;
  approval_decided_at: string | null;
  started_at: string | null;
  finished_at: string | null;
  created_at: string;
  updated_at: string;
}

/** `POST /tools/{id}/invoke` 响应：READ/INTERNAL_WRITE 同步返回终态记录；EXTERNAL_WRITE 返回 202 待审批。 */
export interface InvokeToolResponse {
  invocation: ToolInvocation;
  message?: string;
}

/**
 * 工具测试调用（真实执行链）。
 *
 * READ / INTERNAL_WRITE：服务端真实执行（ERP Port / docingest / SHACL / 模型网关 /
 * 审计链）并落调用记录，返回 200 + 终态记录（SUCCEEDED/FAILED 与真实输出）。
 * EXTERNAL_WRITE：返回 202 + PENDING_APPROVAL 记录，需经审批端点确认后才执行。
 * 失败如实落库并返回，绝不伪造成功。
 */
export function useInvokeTool() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: InvokeToolBody }) =>
      apiPost<InvokeToolResponse>(`/tools/${encodeURIComponent(id)}/invoke`, body, {
        idempotencyKey: newIdempotencyKey(`tool-invoke-${id}`),
      }),
    onSuccess: (_data, variables) => {
      void qc.invalidateQueries({ queryKey: ['tools', variables.id, 'invocations'] });
    },
  });
}

function invalidateInvocations(qc: ReturnType<typeof useQueryClient>, toolId: string) {
  void qc.invalidateQueries({ queryKey: ['tools', toolId, 'invocations'] });
}

/** 审批确认：PENDING_APPROVAL → APPROVED → EXECUTING → SUCCEEDED/FAILED（CAS，绝不重复执行）。 */
export function useApproveToolInvocation() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ toolId, invId, comment }: { toolId: string; invId: string; comment?: string }) =>
      apiPost<InvokeToolResponse>(
        `/tools/${encodeURIComponent(toolId)}/invocations/${encodeURIComponent(invId)}/approve`,
        { comment: comment ?? '' },
        { idempotencyKey: newIdempotencyKey(`tool-inv-approve-${invId}`) },
      ),
    onSuccess: (_d, v) => invalidateInvocations(qc, v.toolId),
  });
}

/** 审批驳回：PENDING_APPROVAL → REJECTED（永不执行）。 */
export function useRejectToolInvocation() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ toolId, invId, comment }: { toolId: string; invId: string; comment?: string }) =>
      apiPost<InvokeToolResponse>(
        `/tools/${encodeURIComponent(toolId)}/invocations/${encodeURIComponent(invId)}/reject`,
        { comment: comment ?? '' },
        { idempotencyKey: newIdempotencyKey(`tool-inv-reject-${invId}`) },
      ),
    onSuccess: (_d, v) => invalidateInvocations(qc, v.toolId),
  });
}

/** `POST /api/v1/tools/generate` 的请求体（读需求/制度文档 → 大模型 → DRAFT）。 */
export interface GenerateToolBody {
  instructions?: string;
  source_path?: string;
  tool_key?: string;
  model_id?: string;
  max_source_chars?: number;
}

export interface GenerateToolResult {
  id: string;
  tool_key: string;
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

export function useGenerateTool() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: GenerateToolBody) =>
      apiPost<GenerateToolResult>('/tools/generate', body, {
        idempotencyKey: newIdempotencyKey(`tool-generate-${body.source_path ?? body.tool_key ?? 'instructions'}`),
      }),
    onSuccess: () => invalidate(qc),
  });
}
