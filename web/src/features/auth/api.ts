import { apiGet, apiPost, apiRequest } from '@/api/client';
import { setSessionToken } from '@/api/client';

export interface RegisterRequest {
  org_code: string;
  login_name: string;
  display_name: string;
  email?: string;
  password: string;
}

export interface LoginRequest {
  login_name: string;
  password: string;
}

export interface SessionResponse {
  session_token: string;
  refresh_token?: string;
  expires_at: string;
  refresh_expires_at: string;
  session_id: string;
  tenant_id: string;
  subject_id: string;
}

/** 注册新用户（基于组织 org_code），成功后持久化 token */
export async function register(data: RegisterRequest): Promise<SessionResponse> {
  const resp = await apiPost<SessionResponse>('/auth/register', data);
  setSessionToken(resp.session_token);
  return resp;
}

/** 密码登录，成功后持久化 token */
export async function login(data: LoginRequest): Promise<SessionResponse> {
  const resp = await apiPost<SessionResponse>('/auth/login', data);
  setSessionToken(resp.session_token);
  return resp;
}

/** 注销（清除 token + 调用后端撤销会话） */
export async function logout(): Promise<void> {
  try {
    await apiRequest('POST', '/auth/logout');
  } finally {
    setSessionToken(null);
  }
}

/** 后端 GET /api/v1/auth/session 返回的会话投影 */
export interface SessionProjection {
  subject_id: string;
  login_name: string;
  display_name: string;
  email: string;
  tenant_id: string;
  tenant_name: string;
  org_path: string;
  permissions: string[];
}

/**
 * 拉取当前 Bearer token 对应的会话投影。
 * 前端启动时 / 登录成功后 / token 刷新后调用一次。
 */
export async function fetchSession(): Promise<SessionProjection> {
  return apiGet<SessionProjection>('/auth/session');
}
