import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiGet, apiPatch, apiPost, apiDelete } from '@/api/client';
import { queryKeys } from '@/api/query-keys';
import { newIdempotencyKey } from '@/lib/http';
import type { ModelProvidersResponse } from '@/types';

/**
 * 模型提供方与 LLM Gateway 设置（§8.2）。
 *
 * 契约：`GET /api/v1/admin/model-providers`
 *   权威：contracts/openapi.yaml#/components/schemas/ModelProviders
 *   —— 返回 **`{ providers, endpoints }` 聚合对象**，不是 cursor 分页列表。
 *
 * ⚠️ 缺陷 #22：本文件此前用 `fetchList<ModelProvider>`（解析 `{items,next_cursor}`）
 *   读这个聚合端点 → `items` 恒 undefined → 页面在**已有 2 个供应商**的情况下
 *   显示"尚未配置模型提供方"。**有数据却显示空态，比报错更难被发现。**
 *
 * 安全约束：凭据**只入不出**。写入可提供 `credential_ref`（环境变量引用名）或
 *   `api_key`（明文密钥，服务端 AES-256-GCM 加密落库）；响应与审计**绝不回显明文**，
 *   列表只返回打码值（`credential_masked`，如 sk-****6e22）与来源枚举（`credential_source`）。
 */
export function useModelProviders() {
  return useQuery({
    queryKey: queryKeys.modelProviders.list(),
    queryFn: () => apiGet<ModelProvidersResponse>('/admin/model-providers'),
  });
}

/** 新建模型供应商（含首个端点）的请求体 —— 契约 CreateModelProviderRequest。 */
export interface CreateModelProviderInput {
  /**
   * 可选；留空由 display_name 派生。**派生只在无损映射时成立**：
   * 仅小写字母/数字/连字符。显示名含中文等非 ASCII 字符时服务端拒之
   * （400），故此时必须显式提供（规则见 ./schema.ts 的 deriveProviderId）。
   */
  provider_id?: string;
  display_name: string;
  provider_type: 'OPENAI_COMPATIBLE';
  status?: 'ACTIVE' | 'DISABLED';
  endpoint: {
    endpoint_name: string;
    deployment_type: 'HTTP';
    region_code?: string;
    /** deployment_type=HTTP 时必填，须以 http(s):// 开头 */
    base_url?: string;
    /** 环境变量引用名（如 ODAP_LLM_API_KEY）；与 api_key 二选一，不是密钥本身 */
    credential_ref?: string;
    /** 明文密钥（只入不出）：与 credential_ref 二选一，服务端加密落库 */
    api_key?: string;
    status?: 'ACTIVE' | 'DISABLED';
  };
}

/** 契约 CreatedModelProvider。provider_id 为 TEXT 主键（非 uuid）。 */
export interface CreatedModelProvider {
  provider_id: string;
  endpoint_id: string;
}

/**
 * 新建模型供应商 —— `POST /api/v1/admin/model-providers`（缺陷 #25）。
 *
 * 服务端在**单事务**内同时创建供应商与其首个端点：只建供应商会留下一个
 * "永远无法被调用"的空壳（地址与凭据引用都在端点上），故 `endpoint` 是必填。
 */
export function useCreateModelProvider() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateModelProviderInput) =>
      apiPost<CreatedModelProvider>('/admin/model-providers', input, {
        idempotencyKey: newIdempotencyKey('model-provider-create'),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.modelProviders.list() });
    },
  });
}

// ---- 供应商编辑 / 删除 ----

export interface UpdateModelProviderInput {
  display_name?: string;
  status?: 'ACTIVE' | 'DISABLED';
}

/** PATCH /api/v1/admin/model-providers/{id} —— 白名单更新 display_name / status。 */
export function useUpdateModelProvider() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ providerID, input }: { providerID: string; input: UpdateModelProviderInput }) =>
      apiPatch(`/admin/model-providers/${encodeURIComponent(providerID)}`, input, {
        idempotencyKey: newIdempotencyKey('model-provider-update'),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.modelProviders.list() });
    },
  });
}

/** DELETE /api/v1/admin/model-providers/{id} —— 级联删除端点。 */
export function useDeleteModelProvider() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (providerID: string) =>
      apiDelete(`/admin/model-providers/${encodeURIComponent(providerID)}`, {
        idempotencyKey: newIdempotencyKey('model-provider-delete'),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.modelProviders.list() });
    },
  });
}

// ---- 端点 CRUD ----

export interface AddModelEndpointInput {
  endpoint_name: string;
  deployment_type: 'HTTP';
  region_code?: string;
  base_url?: string;
  credential_ref?: string;
  api_key?: string;
  status?: 'ACTIVE' | 'DISABLED';
}

/** POST /api/v1/admin/model-providers/{id}/endpoints —— 给已有供应商加端点。 */
export function useAddModelEndpoint() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ providerID, input }: { providerID: string; input: AddModelEndpointInput }) =>
      apiPost(`/admin/model-providers/${encodeURIComponent(providerID)}/endpoints`, input, {
        idempotencyKey: newIdempotencyKey('model-endpoint-add'),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.modelProviders.list() });
    },
  });
}

export interface UpdateModelEndpointInput {
  endpoint_name?: string;
  deployment_type?: 'HTTP';
  region_code?: string;
  base_url?: string;
  credential_ref?: string;
  /**
   * 明文密钥（只入不出），**三态**：省略 = 不改动；非空 = 替换为新密文；
   * 显式空串 = 清空密文（此后 HTTP 端点须保留非空 credential_ref）。
   */
  api_key?: string;
  status?: 'ACTIVE' | 'DISABLED';
}

/** PATCH /api/v1/admin/model-endpoints/{endpoint_id} —— 白名单更新。 */
export function useUpdateModelEndpoint() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ endpointID, input }: { endpointID: string; input: UpdateModelEndpointInput }) =>
      apiPatch(`/admin/model-endpoints/${endpointID}`, input, {
        idempotencyKey: newIdempotencyKey('model-endpoint-update'),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.modelProviders.list() });
    },
  });
}

/**
 * DELETE /api/v1/admin/model-endpoints/{endpoint_id}
 * —— 带下游保护（model_revisions / usage_ledger 引用），
 *    不满足任一条件返回 409 Conflict + 明确原因。
 *    （已移除"供应商必须剩余其他端点"规则：零端点供应商会被装配期忽略。）
 */
export function useDeleteModelEndpoint() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (endpointID: string) =>
      apiDelete(`/admin/model-endpoints/${endpointID}`, {
        idempotencyKey: newIdempotencyKey('model-endpoint-delete'),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.modelProviders.list() });
    },
  });
}

// ---- 录入前连通性探测（「测试连接」）----

/** 契约 ProbeModelProviderRequest。api_key 只入不出，仅用于本次探测。 */
export interface ProbeModelProviderInput {
  deployment_type: 'HTTP';
  base_url?: string;
  credential_ref?: string;
  api_key?: string;
  /** 探测用模型名。指定时服务端调用一次聊天补全（max_tokens=1）验证模型可用。 */
  model_id?: string;
  /**
   * 指名一个**已落库**的端点。未提供 api_key / credential_ref 时，服务端改用该
   * 端点已存凭据探测——「编辑接入」的「模型测试」据此免去重新粘贴明文密钥。
   */
  endpoint_id?: string;
}

/** 契约 ProbeResult。失败以 ok=false + message 表达，而非抛错。 */
export interface ProbeResult {
  ok: boolean;
  latency_ms: number;
  status_code: number;
  error_code?: string;
  message: string;
  vendor_error_snippet?: string;
  preview?: string;
}

/**
 * POST /api/v1/admin/model-providers/probe —— 用刚填的参数真实试调一次。
 *
 * 服务端不落库、不写账本、不记密钥；因此**不传 Idempotency-Key**
 * （无写副作用，天然幂等，用户会反复点）。
 */
export function useProbeModelProvider() {
  return useMutation({
    mutationFn: (input: ProbeModelProviderInput) =>
      apiPost<{ probe: ProbeResult }>('/admin/model-providers/probe', input),
  });
}

// ---- 模型版本登记（让端点真正参与路由）----

/**
 * 能力声明（契约 ModelCapabilities，snake_case，与 llm.capability_profiles 落库字段一致）。
 * `max_context_tokens` 必须为正整数，否则该模型在装配期被排除。
 */
export interface ModelCapabilities {
  native_schema: boolean;
  supports_seed: boolean;
  supports_tools: boolean;
  /** 向量嵌入能力（/embeddings）：知识库文档嵌入（kb.ingest）路由必需。 */
  embeddings?: boolean;
  max_context_tokens: number;
}

/** 契约 ModelRevision。 */
export interface ModelRevision {
  model_revision_id: string;
  endpoint_id: string;
  model_id: string;
  revision_label: string;
  capability_digest: string;
  capability_revision: string;
  capability_status: string;
  status: string;
  capabilities?: ModelCapabilities;
  created_at?: string;
}

/** GET /api/v1/admin/model-endpoints/{endpoint_id}/models —— 空列表表示尚未登记，端点不参与路由。 */
export function useModelRevisions(endpointID: string | null) {
  return useQuery({
    queryKey: queryKeys.modelRevisions.list(endpointID ?? ''),
    enabled: endpointID !== null && endpointID !== '',
    queryFn: () =>
      apiGet<{ endpoint_id: string; models: ModelRevision[] }>(
        `/admin/model-endpoints/${endpointID}/models`,
      ),
  });
}

/**
 * 登记模型版本的请求体（契约 RegisterModelRevisionRequest）。
 * 一次登记同时写入内容寻址的能力档案与模型版本——这是端点参与路由的唯一路径。
 */
export interface RegisterModelRevisionInput {
  model_id: string;
  revision_label?: string;
  capability_revision: string;
  capability_verified?: boolean;
  capabilities: ModelCapabilities;
}

/** POST /api/v1/admin/model-endpoints/{endpoint_id}/models */
export function useRegisterModelRevision(endpointID: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: RegisterModelRevisionInput) =>
      apiPost(`/admin/model-endpoints/${endpointID}/models`, input, {
        idempotencyKey: newIdempotencyKey(`model-revision-${endpointID}`),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.modelProviders.list() });
      void queryClient.invalidateQueries({ queryKey: queryKeys.modelRevisions.list(endpointID) });
    },
  });
}

/**
 * DELETE /api/v1/admin/model-endpoints/{endpoint_id}/models/{model_revision_id}
 * —— 带下游保护：已被 llm.usage_ledger 引用（已真实调用过）时返回 409。
 */
export function useDeleteModelRevision(endpointID: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (modelRevisionID: string) =>
      apiDelete(`/admin/model-endpoints/${endpointID}/models/${modelRevisionID}`, {
        idempotencyKey: newIdempotencyKey(`model-revision-del-${modelRevisionID}`),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.modelProviders.list() });
      void queryClient.invalidateQueries({ queryKey: queryKeys.modelRevisions.list(endpointID) });
    },
  });
}
