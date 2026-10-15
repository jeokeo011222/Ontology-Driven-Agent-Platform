import type { ReactNode } from 'react';
import { usePermissions, type PermissionMode } from '@/hooks/usePermissions';

export interface PermissionGateProps {
  /** 需要的权限（点号规范，如 'ontology.publish'） */
  permission: string | string[];
  mode?: PermissionMode;
  /** 无权时渲染的内容，默认不渲染任何内容 */
  fallback?: ReactNode;
  /** 无权时渲染一行说明（默认关闭，避免页面噪声） */
  showHint?: boolean;
  children: ReactNode;
}

/**
 * 第二层权限：组件 / 字段级可见性（§11.2）。
 *
 * ⚠️ 这**不是**安全边界。隐藏按钮只是交互优化；
 * 服务端必须对每个 API 与业务操作重新校验权限（第三层）。
 * 因此本组件不做任何“越权保护”承诺，也不会阻止用户直接调用 API。
 */
export function PermissionGate({
  permission,
  mode = 'all',
  fallback,
  showHint = false,
  children,
}: PermissionGateProps) {
  const { check } = usePermissions();

  if (check(permission, mode)) {
    return <>{children}</>;
  }

  if (fallback !== undefined) {
    return <>{fallback}</>;
  }

  if (showHint) {
    return (
      <span className="text-xs muted" title="当前身份无此操作权限；安全校验在服务端">
        （无权限）
      </span>
    );
  }

  return null;
}
