import { useMemo } from 'react';
import { useSession } from './useSession';

/**
 * hooks/usePermissions.ts —— 第二层权限：组件/字段可见性（§11.2）
 *
 * ⚠️ 安全边界说明（第三层）：
 *   路由守卫与权限门仅用于改善交互体验（避免用户看到无权操作并触发 403）。
 *   真正的授权在服务端对每个 API、对象、字段与业务操作重新校验（§11.2 第三层）。
 *   即使用户通过浏览器直接构造请求，也不能绕过后端授权。
 */

export type PermissionMode = 'all' | 'any';

/** 权限匹配：支持整串通配 '*'（授予 '*' 即拥有全部权限） */
export function matchPermission(granted: string, required: string): boolean {
  if (granted === '*') return true;
  const g = granted.split(':');
  const r = required.split(':');
  for (let i = 0; i < g.length; i += 1) {
    if (g[i] === '*') return true;
    if (i >= r.length) return false;
    if (g[i] !== r[i]) return false;
  }
  return g.length === r.length;
}

export function hasPermission(permissions: string[], required: string): boolean {
  return permissions.some((granted) => matchPermission(granted, required));
}

export interface UsePermissionsResult {
  /** 当前身份的最小权限投影 */
  permissions: string[];
  isLoading: boolean;
  /** 判断单一权限 */
  can: (required: string) => boolean;
  /** 全部满足 / 任一满足 */
  canAll: (required: string[]) => boolean;
  canAny: (required: string[]) => boolean;
  /** 组合判断 */
  check: (required: string | string[], mode?: PermissionMode) => boolean;
}

export function usePermissions(): UsePermissionsResult {
  const { permissions, isLoading } = useSession();

  return useMemo<UsePermissionsResult>(() => {
    const can = (required: string) => hasPermission(permissions, required);
    const canAny = (required: string[]) => required.some((r) => hasPermission(permissions, r));
    const canAll = (required: string[]) => required.every((r) => hasPermission(permissions, r));
    const check = (required: string | string[], mode: PermissionMode = 'all') => {
      if (typeof required === 'string') return can(required);
      if (required.length === 0) return true;
      return mode === 'any' ? canAny(required) : canAll(required);
    };
    return { permissions, isLoading, can, canAny, canAll, check };
  }, [permissions, isLoading]);
}
