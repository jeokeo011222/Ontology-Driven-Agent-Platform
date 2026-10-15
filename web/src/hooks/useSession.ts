import { createContext, useContext } from 'react';

/**
 * hooks/useSession.ts —— 用户会话（最小投影）
 *
 * §10.3：用户会话只存当前身份的最小投影，**不存权限全集**；
 * 权限判断以服务端实时响应为准。前端可见性控制只是体验优化，
 * 安全边界永远是服务端（§11.2 第三层）。
 */

export type WorkspaceKind = 'business' | 'admin';
export type EnvName = 'dev' | 'staging' | 'prod';

export interface SessionUser {
  subject_id: string;
  display_name: string;
  org_path?: string;
  tenant_id: string;
  tenant_name: string;
}

export interface SessionValue {
  user: SessionUser;
  /** 当前身份的最小权限投影（用于 UI 可见性），非权威授权结果 */
  permissions: string[];
  env: EnvName;
  /** 当前工作空间 */
  workspace: WorkspaceKind;
  isLoading: boolean;
  /** 切换租户（切换后必须清空 Query 缓存与 SSE 订阅，§13.2） */
  switchTenant?: (tenantId: string) => void;
  setWorkspace?: (workspace: WorkspaceKind) => void;
}

export const SessionContext = createContext<SessionValue | null>(null);

export function useSession(): SessionValue {
  const value = useContext(SessionContext);
  if (!value) {
    throw new Error('useSession 必须在 SessionProvider 内使用');
  }
  return value;
}
