import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiDelete, apiPatch, apiPost, fetchList, listFilterQuery, type ListQueryFilters } from '@/api/client';
import { queryKeys } from '@/api/query-keys';
import { newIdempotencyKey } from '@/lib/http';
import type { Connector } from '@/types';

/**
 * 与ERP集成（§8.3 + ERP 字典化）：
 *   1. ERP 系统类型字典（integration.erp_systems，全局只读）：默认用友NC65；
 *      未实现适配器的类型（金蝶星空云 / SAP）在前端明确标注并禁用创建。
 *   2. 租户级 ERP 连接配置（integration.erp_connections）：完整创建/编辑/
 *      删除/测试流程；凭据密文优先落库，界面只回显打码值。
 *
 * 安全约束：测试连接用用户录入的参数直连真实探针（nc65 Health），不落库、
 * 不产生业务写入；凭据明文只在本请求内存中存活，绝不回显。
 */

export interface ErpSystem {
  system_code: string;
  display_name: string;
  adapter_key: string;
  implemented: boolean;
  is_default: boolean;
  sort_no: number;
  description: string;
}

export interface ErpConnection {
  id: string;
  system_code: string;
  system_name: string;
  name: string;
  base_url: string;
  credential_ref?: string;
  credential_masked: string;
  credential_source: 'CIPHERTEXT' | 'ENV_REF' | 'NONE';
  has_credential: boolean;
  status: 'ACTIVE' | 'DISABLED' | 'DELETED';
  created_by: string;
  created_at: string;
  updated_at: string;
}

export interface ErpConnectionCredential {
  usercode?: string;
  password?: string;
  api_key?: string;
  token?: string;
}

export interface ErpConnectionInput {
  system_code: string;
  name: string;
  base_url: string;
  credential_ref?: string;
  credential?: ErpConnectionCredential | null;
  status?: string;
}

export function useErpSystems() {
  return useQuery({
    queryKey: ['admin', 'erp-systems'],
    queryFn: () => fetchList<ErpSystem>('/admin/erp-systems', { limit: 50 }),
  });
}

export interface ErpConnectionFilters {
  status?: string;
  system_code?: string;
  q?: string;
}

export function useErpConnections(filters: ErpConnectionFilters) {
  return useQuery({
    queryKey: [...queryKeys.connectors.list(), 'erp-connections', filters],
    queryFn: () =>
      fetchList<ErpConnection>('/admin/erp-connections', {
        limit: 10,
        status: filters.status || undefined,
        system_code: filters.system_code || undefined,
        q: filters.q || undefined,
      }),
  });
}

function credentialPayload(
  credential: ErpConnectionCredential | null,
  credentialRef: string,
): Record<string, unknown> {
  // 凭据双路径（与模型端点同规则）：录入凭据 → 密文优先；否则环境变量引用。
  const hasCred =
    credential != null &&
    Object.values(credential).some((v) => typeof v === 'string' && v !== '');
  if (hasCred) return { credential };
  if (credentialRef.trim() !== '') return { credential_ref: credentialRef.trim() };
  return {};
}

export function useCreateErpConnection() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: ErpConnectionInput) =>
      apiPost<ErpConnection>('/admin/erp-connections', {
        system_code: input.system_code,
        name: input.name,
        base_url: input.base_url,
        ...credentialPayload(input.credential ?? null, input.credential_ref ?? ''),
      }, { idempotencyKey: newIdempotencyKey(`erp-connection-create:${input.name}`) }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['admin', 'erp-connections'] });
      void queryClient.invalidateQueries({ queryKey: queryKeys.connectors.list() });
    },
  });
}

export interface ErpConnectionUpdateInput {
  id: string;
  name?: string;
  base_url?: string;
  credential_ref?: string;
  // 三态：undefined = 不改；对象含非空字段 = 替换；空对象 = 清空（须保留 ref）。
  credential?: ErpConnectionCredential | null;
  status?: string;
}

export function useUpdateErpConnection() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ...body }: ErpConnectionUpdateInput) =>
      apiPatch<ErpConnection>(
        `/admin/erp-connections/${encodeURIComponent(id)}`,
        body,
        { idempotencyKey: newIdempotencyKey(`erp-connection-update:${id}`) },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['admin', 'erp-connections'] });
    },
  });
}

export function useDeleteErpConnection() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiDelete<{ id: string; status: string }>(
        `/admin/erp-connections/${encodeURIComponent(id)}`,
        { idempotencyKey: newIdempotencyKey(`erp-connection-delete:${id}`) },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['admin', 'erp-connections'] });
    },
  });
}

export interface ErpConnectionTestInput {
  system_code: string;
  base_url: string;
  credential?: ErpConnectionCredential;
}

export interface ErpConnectionTestResult {
  reachable: boolean;
  healthy?: boolean;
  status?: string;
  message: string;
}

export function useTestErpConnection() {
  return useMutation({
    mutationFn: (input: ErpConnectionTestInput) =>
      apiPost<ErpConnectionTestResult>(
        '/admin/erp-connections/test',
        {
          system_code: input.system_code,
          base_url: input.base_url,
          credential: input.credential ?? undefined,
        },
        // 测试不入库、不产生业务写入，幂等键仅用于响应头回显与审计留痕。
        { idempotencyKey: newIdempotencyKey(`erp-connection-test:${input.base_url}`) },
      ),
  });
}

export function useConnectors(filters: ListQueryFilters = {}) {
  return useQuery({
    queryKey: [...queryKeys.connectors.list(), filters],
    queryFn: () => fetchList<Connector>('/admin/connectors', listFilterQuery(filters)),
  });
}

export function useTestConnector() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiPost<{ ok: boolean; detail?: string }>(
        `/admin/connectors/${encodeURIComponent(id)}/test`,
        { probe: 'predefined-safe-probe' },
        { idempotencyKey: newIdempotencyKey(`connector-test-${id}`) },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.connectors.list() });
    },
  });
}
