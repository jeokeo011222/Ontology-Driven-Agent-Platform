import type { ReactNode } from 'react';
import { Navigate, useLocation } from 'react-router-dom';
import { Button } from '@/components/ui';
import { StateView } from '@/components/error-state';
import { usePermissions, type PermissionMode } from '@/hooks/usePermissions';
import { useSession } from '@/hooks/useSession';
import { getSessionToken } from '@/api/client';

/**
 * app/route-guards.tsx —— 第一层权限：路由访问控制（§11.2）
 *
 * ⚠️ 安全边界声明（第三层）：
 *   前端路由守卫与权限门**只用于改善交互体验**——避免未授权用户进入页面后
 *   看到一堆 403。真正的授权边界在服务端：每个 API、对象、字段和业务操作
 *   都会重新校验权限（§11.2 第三层）。
 *   用户直接输入管理端 URL 或构造请求，都必须由服务端拒绝——
 *   前端隐藏路由不构成任何安全保证。
 */

export interface RequireAuthProps {
  children: ReactNode;
}

/**
 * 认证守卫。
 * 当前会话由 providers.tsx 的 SessionProvider 提供（后端认证模块接入后，
 * 此处将改为读取服务端会话投影，未认证时跳转登录）。
 */
export function RequireAuth({ children }: RequireAuthProps) {
  const session = useSession();
  const location = useLocation();

  if (session.isLoading) {
    return <StateView kind="loading" title="正在加载会话" />;
  }

  // 后端认证模块已接入：无 Bearer token 即未登录，跳 /login
  if (!getSessionToken()) {
    return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  }

  // 会话投影缺失（token 过期/401 场景）也引导重新登录
  if (!session.user?.subject_id) {
    return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  }

  return <>{children}</>;
}

export interface RequirePermissionProps {
  permission: string | string[];
  mode?: PermissionMode;
  children: ReactNode;
  /** 无权限时自定义渲染（默认渲染权限不足提示） */
  fallback?: ReactNode;
}

/**
 * 权限守卫（第一层）。
 * 注意：这是体验层判断，服务端仍会对目标路由背后的每个 API 再次授权。
 */
export function RequirePermission({ permission, mode = 'all', children, fallback }: RequirePermissionProps) {
  const { check, isLoading } = usePermissions();

  if (isLoading) {
    return <StateView kind="loading" title="正在校验访问权限" />;
  }

  if (!check(permission, mode)) {
    if (fallback !== undefined) return <>{fallback}</>;
    return <RouteForbidden permission={permission} />;
  }

  return <>{children}</>;
}

/** 权限不足页面（§10.5 forbidden 状态） */
export function RouteForbidden({
  permission,
  onRequest,
}: {
  permission?: string | string[];
  onRequest?: () => void;
}) {
  const required = Array.isArray(permission) ? permission.join('、') : permission;
  return (
    <div className="page">
      <StateView
        kind="forbidden"
        description={
          <>
            当前身份无权访问该页面。
            {required ? (
              <>
                {' '}
                所需权限：<span className="mono">{required}</span>。
              </>
            ) : null}
            <br />
            你可以申请权限或联系管理员；服务端已记录本次访问尝试（不会因前端隐藏而豁免校验）。
          </>
        }
        actions={
          onRequest ? (
            <Button variant="primary" size="sm" onClick={onRequest} icon="lock">
              申请权限
            </Button>
          ) : null
        }
      />
    </div>
  );
}

/** 未匹配路由（§10.5 空态语义） */
export function RouteNotFound() {
  const location = useLocation();
  return (
    <div className="page">
      <StateView
        kind="empty"
        title="页面不存在"
        description={
          <>
            未找到与 <span className="mono">{location.pathname}</span> 匹配的页面。
          </>
        }
      />
    </div>
  );
}
