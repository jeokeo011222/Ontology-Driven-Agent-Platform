import type { ReactNode } from 'react';
import { useEffect, useMemo, useState } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { shouldRetryQuery } from '@/api/errors';
import { getSessionToken, subscribeSessionToken } from '@/api/client';
import { fetchSession } from '@/features/auth/api';
import {
  SessionContext,
  type EnvName,
  type SessionUser,
  type SessionValue,
  type WorkspaceKind,
} from '@/hooks/useSession';

/**
 * app/providers.tsx —— 全局 Provider 与数据层配置
 *
 * §10.3（选型已冻结）：TanStack Query 负责服务端数据；React Hook Form + Zod 负责表单；
 * **不引入 Redux 等全局业务 Store** —— 服务端状态的所有权在服务端，前端只做投影与缓存。
 *
 * 去 mock 说明：不再硬编码 DEMO_USER / DEMO_PERMISSIONS，启动时从 localStorage 读 Bearer token，
 * 若存在则调 GET /api/v1/auth/session 拉取真实用户投影 + 权限数组（auth_store.go 的
 * GetSessionProjection 展开 RBAC role_bindings → permissions）。
 */

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: shouldRetryQuery,
      staleTime: 30_000,
      gcTime: 5 * 60_000,
      refetchOnWindowFocus: true,
      refetchOnReconnect: true,
    },
    mutations: {
      retry: false,
    },
  },
});

export interface ProvidersProps {
  children: ReactNode;
}

export function Providers({ children }: ProvidersProps) {
  const [workspace, setWorkspace] = useState<WorkspaceKind>('business');

  // --- 会话引导：从 Bearer token → 真实会话投影 ---
  const [sessionUser, setSessionUser] = useState<SessionUser | null>(null);
  const [sessionPermissions, setSessionPermissions] = useState<string[]>([]);
  const [sessionLoading, setSessionLoading] = useState(true);
  const [token, setToken] = useState<string | null>(() => getSessionToken());

  // token 变化订阅：登录 / 注册 / 登出都会改写 token（api/client.ts 的 setSessionToken）。
  // 先同步置为"加载中"，再重新拉取会话投影。
  //
  // 顺序很关键：登录成功后 login() 先写 token、随后 navigate('/workspace')。若不在写入
  // 的瞬间就把 isLoading 置真，守卫在两次渲染之间会看到"有 token、但没有用户投影"，
  // 于是立刻 Navigate 回 /login —— 用户看到的就是"点了登录毫无反应"。
  useEffect(
    () =>
      subscribeSessionToken((next) => {
        setSessionLoading(true);
        setToken(next);
      }),
    [],
  );

  useEffect(() => {
    if (!token) {
      // 无 token → 停在未登录态（RequireAuth 会跳 /login）
      setSessionUser(null);
      setSessionPermissions([]);
      setSessionLoading(false);
      return;
    }
    let cancelled = false;
    fetchSession()
      .then((proj) => {
        if (cancelled) return;
        setSessionUser({
          subject_id: proj.subject_id,
          display_name: proj.display_name,
          org_path: proj.org_path,
          tenant_id: proj.tenant_id,
          tenant_name: proj.tenant_name,
        });
        setSessionPermissions(Array.isArray(proj.permissions) ? proj.permissions : []);
      })
      .catch(() => {
        // 会话已过期或服务端不可用 → 清空投影并保持未登录态
        // （前端无 action，交给 RequireAuth 跳 /login）
        if (cancelled) return;
        setSessionUser(null);
        setSessionPermissions([]);
      })
      .finally(() => {
        if (!cancelled) setSessionLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [token]);

  const env: EnvName = (import.meta.env.VITE_ENV_NAME as EnvName | undefined) ?? 'dev';

  const sessionValue = useMemo<SessionValue>(
    () => ({
      user: sessionUser ?? {
        subject_id: '',
        display_name: '',
        tenant_id: '',
        tenant_name: '',
      },
      permissions: sessionPermissions,
      env,
      workspace,
      isLoading: sessionLoading,
      switchTenant: (_id: string) => {
        queryClient.clear();
      },
      setWorkspace,
    }),
    [sessionUser, sessionPermissions, env, workspace, sessionLoading],
  );

  return (
    <QueryClientProvider client={queryClient}>
      <SessionContext.Provider value={sessionValue}>{children}</SessionContext.Provider>
    </QueryClientProvider>
  );
}
