import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiDelete, apiPost, fetchList, listFilterQuery, type ListQueryFilters } from '@/api/client';
import { queryKeys } from '@/api/query-keys';
import { newIdempotencyKey } from '@/lib/http';
import type { Policy, Role, RoleBinding } from '@/types';

/**
 * 策略与审批设置（§8.4）+ 用户、角色与权限（§8.5）。
 *
 * 契约：
 *   `/api/v1/policies`、`/api/v1/policies/{id}/versions`（contracts/03 §2）
 *   `/api/v1/admin/roles`、`/api/v1/admin/role-bindings`、`/api/v1/admin/subjects/{id}/disable`（第三篇 §14）
 *
 * 重要区分（§8.4）：
 *   审批策略 = 什么情况下需要人类授权；
 *   权限策略 = 当前主体是否有资格执行；
 *   业务规则 = 业务对象是否满足操作条件；
 *   系统执行策略 = 如何处理超时、重试、对账与恢复。
 * 四者不能合并为一个 LLM 判断，也不能用单个开关统一控制。
 */

/* -------------------- 策略 -------------------- */

export function usePolicies(filters: ListQueryFilters = {}) {
  return useQuery({
    queryKey: [...queryKeys.policies.list(), filters],
    queryFn: () => fetchList<Policy>('/policies', listFilterQuery(filters)),
  });
}

/** 已发布策略不可原地修改：修改必须创建新版本并经过校验与门禁（§8.4） */
export function useCreatePolicyVersion() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (policyId: string) =>
      apiPost<{ id: string; version: string; based_on: string; status: string }>(
        `/policies/${encodeURIComponent(policyId)}/versions`,
        { based_on: 'current' },
        { idempotencyKey: newIdempotencyKey(`policy-version-${policyId}`) },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.policies.list() });
    },
  });
}

/* -------------------- 身份与权限 -------------------- */

export function useRoles(filters: ListQueryFilters = {}) {
  return useQuery({
    queryKey: [...queryKeys.identity.roles(), filters],
    queryFn: () => fetchList<Role>('/admin/roles', listFilterQuery(filters)),
  });
}

export function useRoleBindings(filters: ListQueryFilters = {}) {
  return useQuery({
    queryKey: [...queryKeys.identity.roleBindings(), filters],
    queryFn: () => fetchList<RoleBinding>('/admin/role-bindings', listFilterQuery(filters)),
  });
}

/** 权限目录条目（`GET /api/v1/admin/permissions`）。 */
export interface PermissionItem {
  permission_key: string;
  resource_type: string;
  action: string;
  description: string;
}

/** 全局权限目录：供"新建角色"时选择真实权限键（禁止填写目录外的伪权限）。 */
export function usePermissions() {
  return useQuery({
    queryKey: queryKeys.identity.permissions(),
    queryFn: () => fetchList<PermissionItem>('/admin/permissions', { limit: 200 }),
  });
}

/** 新建自定义角色（人工重建）。平台只内置系统管理员，其余角色在此创建。 */
export function useCreateRole() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: { role_key: string; name: string; description?: string; permissions?: string[] }) =>
      apiPost<Role>('/admin/roles', body, {
        idempotencyKey: newIdempotencyKey(`role-create-${body.role_key}`),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.identity.roles() });
    },
  });
}

/** 删除自定义角色；系统内置角色或被绑定引用的角色会被后端以 409 拒绝。 */
export function useDeleteRole() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiDelete<void>(`/admin/roles/${encodeURIComponent(id)}`, {
        idempotencyKey: newIdempotencyKey(`role-delete-${id}`),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.identity.roles() });
    },
  });
}

/** 停用主体（权限撤销）——高风险操作，需二次确认并留审计（§9.2 / §8.5） */
export function useDisableSubject() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (subjectId: string) =>
      apiPost<{ ok: boolean }>(
        `/admin/subjects/${encodeURIComponent(subjectId)}/disable`,
        { reason: '管理员在工作台停用' },
        { idempotencyKey: newIdempotencyKey(`subject-disable-${subjectId}`) },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.identity.roleBindings() });
    },
  });
}
