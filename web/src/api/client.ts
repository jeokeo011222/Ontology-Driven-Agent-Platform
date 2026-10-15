/**
 * api/client.ts —— 统一 API Client
 *
 * 权威：
 *   contracts/03 §1.1 统一前缀 /api/v1；§1.4 cursor 分页；§1.6 Idempotency-Key；
 *   §1.7 错误响应；§1.8 长时操作；§1.9 实时通道。
 *   contracts/02 §4 错误响应结构（{error:{code,base_code,message,retryable,details},request_id}）。
 *   第十七篇 §10.6.4 乐观并发（If-Match / 409）。
 *
 * 安全约束（§13.3 发布门禁）：
 *   - 不在前端日志打印密钥或敏感字段；本模块不输出任何请求体/头部日志。
 *   - 凭据一律以会话 Cookie（HttpOnly）承载，前端不缓存真实密钥。
 */

import { API_PREFIX, type ApiErrorEnvelope, type CursorPage, type RequestOptions } from '@/types';
import { ApiError, TRANSPORT_ERROR_CODE } from './errors';
import { buildQueryString, isAbortError, joinUrl } from '@/lib/http';
import { resolveFixture, type HttpMethod, type QueryValue } from './generated/fixtures';

/** 后端基址：默认空 → 同源 + vite proxy（vite.config.ts 已配置 /api → http://localhost:8080） */
export const API_BASE = (import.meta.env.VITE_API_BASE ?? '').replace(/\/+$/, '');

/** 前端存储：会话令牌（Bearer 模式，后端 SessionResolver 优先读取） */
const TOKEN_KEY = 'odap_token';

/** 读取会话令牌（从 localStorage） */
export function getSessionToken(): string | null {
  if (typeof window === 'undefined') return null;
  return window.localStorage.getItem(TOKEN_KEY);
}

type SessionTokenListener = (token: string | null) => void;
const sessionTokenListeners = new Set<SessionTokenListener>();

/**
 * 订阅会话令牌变化（登录 / 注册 / 登出后触发）。
 *
 * 为什么需要：token 存在 localStorage 里，React 数据层（app/providers.tsx）无法
 * 直接观察到这次写入。登录成功后若不重新拉取会话投影，路由守卫会看到
 * "有 token、但没有用户投影"，并把用户立刻弹回 /login —— 表现为"点了登录毫无反应"。
 * 这里用一个极小的发布订阅把写入事件暴露出来，无需引入全局 Store。
 */
export function subscribeSessionToken(listener: SessionTokenListener): () => void {
  sessionTokenListeners.add(listener);
  return () => {
    sessionTokenListeners.delete(listener);
  };
}

/** 持久化会话令牌（登录/注册成功后调用） */
export function setSessionToken(token: string | null): void {
  if (typeof window === 'undefined') return;
  if (token === null) {
    window.localStorage.removeItem(TOKEN_KEY);
  } else {
    window.localStorage.setItem(TOKEN_KEY, token);
  }
  for (const listener of sessionTokenListeners) listener(token);
}

/**
 * 为 "1" 时使用静态样例数据（仅演示/构建用，不改变契约路径）。
 *
 * 生产门禁：即使构建时误设 VITE_USE_FIXTURES=1，生产构建（import.meta.env.PROD）
 * 也强制关闭样例数据 —— 严禁任何 mock/伪造数据进入生产产物。
 */
export const USE_FIXTURES = import.meta.env.PROD ? false : import.meta.env.VITE_USE_FIXTURES === '1';

/** 统一实时通道端点（contracts/03 §1.9） */
export const STREAM_PATH = import.meta.env.VITE_STREAM_PATH ?? `${API_PREFIX}/stream`;

/** 拼接完整 URL（含 /api/v1 前缀与查询串） */
export function buildUrl(path: string, query?: Record<string, QueryValue>): string {
  return `${joinUrl(API_BASE, `${API_PREFIX}${path}`)}${buildQueryString(query)}`;
}

/** SSE 连接地址（EventSource 无法自定义请求头，鉴权走 HttpOnly 会话 Cookie） */
export function streamUrl(): string {
  return joinUrl(API_BASE, STREAM_PATH);
}

/* ============================ 状态码 → 基础码兜底映射 ============================ */

function baseCodeForStatus(status: number): string {
  switch (status) {
    case 400:
      return 'INVALID_ARGUMENT';
    case 401:
      return 'UNAUTHENTICATED';
    case 403:
      return 'PERMISSION_DENIED';
    case 404:
      return 'NOT_FOUND';
    case 409:
    case 412:
      return 'CONFLICT';
    case 422:
      return 'VALIDATION_FAILED';
    case 429:
      return 'RATE_LIMITED';
    case 500:
      return 'INTERNAL';
    case 503:
      return 'UNAVAILABLE';
    case 504:
      return 'DEADLINE_EXCEEDED';
    default:
      return status >= 500 ? 'INTERNAL' : 'INVALID_ARGUMENT';
  }
}

/**
 * 本地兜底映射：把 HTTP 状态码映射为 (域码, 基础码)。
 *
 * 仅在服务端**未返回**可解析的错误信封时使用（有信封时一律以服务端为准）。
 * 域码决定 UI 文案，可细化到具体 HTTP 语义；基础码必须落在契约的 12 个基础码内。
 */
function localErrorCodes(status: number): { code: string; baseCode: string } {
  // 405 Method Not Allowed 必须单独处理。
  //   "路径存在、方法未注册"是**前后端接口不一致（我方缺陷）**，不是用户输入问题。
  //   若沿用 default 的 INVALID_ARGUMENT，UI 会显示"请求参数不合法 / 请修正输入后
  //   重新提交"——把我们的问题说成用户的问题（与缺陷 #11/#20/#24 同一类误导）。
  //   基础码取 INVALID_ARGUMENT（4xx 唯一勉强贴合的类别），真正的语义由域码承载。
  if (status === 405) return { code: 'METHOD_NOT_ALLOWED', baseCode: 'INVALID_ARGUMENT' };
  const base = baseCodeForStatus(status);
  return { code: base, baseCode: base };
}

function defaultMessageForStatus(status: number): string {
  switch (status) {
    case 401:
      return '身份未认证或已失效，请重新登录。';
    case 403:
      return '当前身份无权执行该操作。';
    case 404:
      return '资源不存在或不在可见范围内。';
    case 405:
      return '接口未提供该操作（HTTP 405 方法不允许）。';
    case 409:
      return '数据已被他人修改，请重新加载后重试。';
    case 429:
      return '请求过于频繁，请稍后重试。';
    case 504:
      return '请求超时，若涉及外部操作，结果可能未知，请核对后重试。';
    default:
      return `请求失败（HTTP ${status}）。`;
  }
}

function defaultRetryableForStatus(status: number): boolean {
  return status === 429 || status === 503 || status === 504 || status >= 500;
}

function isErrorEnvelope(value: unknown): value is ApiErrorEnvelope {
  if (!value || typeof value !== 'object') return false;
  const maybe = value as { error?: unknown };
  return !!maybe.error && typeof maybe.error === 'object';
}

function toApiError(status: number, payload: unknown): ApiError {
  const local = localErrorCodes(status);
  if (isErrorEnvelope(payload)) {
    const e = payload.error;
    return new ApiError({
      code: e.code || local.code,
      baseCode: e.base_code || local.baseCode,
      message: e.message || defaultMessageForStatus(status),
      retryable: typeof e.retryable === 'boolean' ? e.retryable : defaultRetryableForStatus(status),
      status,
      requestId: payload.request_id,
      details: e.details,
    });
  }

  return new ApiError({
    code: local.code,
    baseCode: local.baseCode,
    message: defaultMessageForStatus(status),
    retryable: defaultRetryableForStatus(status),
    status,
  });
}

/* ============================ 核心请求 ============================ */

async function execute<T>(
  method: HttpMethod,
  path: string,
  body: unknown,
  options: RequestOptions = {},
): Promise<T> {
  // 演示模式：走同一契约路径的静态样例数据
  if (USE_FIXTURES) {
    const resolution = resolveFixture({ method, path, query: options.query, body });
    if (!resolution.hit) {
      throw new ApiError({
        code: 'NOT_FOUND',
        baseCode: 'NOT_FOUND',
        message: `演示数据未覆盖该端点：${method} ${API_PREFIX}${path}`,
        retryable: false,
        status: 404,
      });
    }
    return resolution.data as T;
  }

  const headers: Record<string, string> = { Accept: 'application/json', ...options.headers };
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  // 自动注入 Bearer token（登录/注册端点的调用方可通过 options.headers 覆盖）
  const token = getSessionToken();
  if (token && !options.headers?.Authorization && !options.headers?.authorization) {
    headers['Authorization'] = `Bearer ${token}`;
  }
  // contracts/03 §1.6 —— 写操作幂等键
  if (options.idempotencyKey) headers['Idempotency-Key'] = options.idempotencyKey;
  // §10.6.4 —— 乐观并发
  if (options.ifMatch) headers['If-Match'] = options.ifMatch;
  if (options.ifNoneMatch) headers['If-None-Match'] = options.ifNoneMatch;

  const isWrite = method !== 'GET';

  let response: Response;
  try {
    response = await fetch(buildUrl(path, options.query), {
      method,
      headers,
      credentials: 'same-origin', // 会话 Cookie 由浏览器自动携带（§10.6.1）
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: options.signal,
    });
  } catch (error) {
    if (isAbortError(error)) throw error;
    // §9.4：网络超时后应显示“结果待确认”，不得自动重发非幂等操作
    throw new ApiError({
      code: TRANSPORT_ERROR_CODE,
      baseCode: 'UNAVAILABLE',
      message: '网络请求失败或超时，未取得服务端响应。',
      retryable: true,
      status: 0,
      transport: true,
      unknownOutcomeHint: isWrite,
    });
  }

  if (response.status === 204) return undefined as T;

  const text = await response.text();
  let payload: unknown = null;
  if (text.length > 0) {
    try {
      payload = JSON.parse(text);
    } catch {
      payload = { raw: text };
    }
  }

  if (!response.ok) {
    throw toApiError(response.status, payload);
  }

  return payload as T;
}

/* ============================ 公共 API ============================ */

export function apiRequest<T>(
  method: HttpMethod,
  path: string,
  body?: unknown,
  options?: RequestOptions,
): Promise<T> {
  return execute<T>(method, path, body, options);
}

export function apiGet<T>(path: string, options?: RequestOptions): Promise<T> {
  return execute<T>('GET', path, undefined, options);
}

export function apiPost<T>(path: string, body?: unknown, options?: RequestOptions): Promise<T> {
  return execute<T>('POST', path, body, options);
}

export function apiPut<T>(path: string, body?: unknown, options?: RequestOptions): Promise<T> {
  return execute<T>('PUT', path, body, options);
}

export function apiPatch<T>(path: string, body?: unknown, options?: RequestOptions): Promise<T> {
  return execute<T>('PATCH', path, body, options);
}

export function apiDelete<T>(path: string, options?: RequestOptions): Promise<T> {
  return execute<T>('DELETE', path, undefined, options);
}

/**
 * multipart 上传（FormData）。
 *
 * 与 execute 同样注入 Bearer 与 Idempotency-Key，但**不**设置 Content-Type：
 * 必须让浏览器自动带上 multipart 边界，手写 Content-Type 会丢掉 boundary
 * 导致服务端解析失败。刻意不走 fixtures —— 上传无法用静态样例覆盖。
 */
export async function apiUpload<T>(
  path: string,
  form: FormData,
  options: RequestOptions = {},
): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json', ...options.headers };
  // 自动注入 Bearer token（与 execute 一致）
  const token = getSessionToken();
  if (token && !options.headers?.Authorization && !options.headers?.authorization) {
    headers['Authorization'] = `Bearer ${token}`;
  }
  // contracts/03 §1.6 —— 写操作幂等键
  if (options.idempotencyKey) headers['Idempotency-Key'] = options.idempotencyKey;

  let response: Response;
  try {
    response = await fetch(buildUrl(path, options.query), {
      method: 'POST',
      headers,
      credentials: 'same-origin',
      body: form,
      signal: options.signal,
    });
  } catch (error) {
    if (isAbortError(error)) throw error;
    throw new ApiError({
      code: TRANSPORT_ERROR_CODE,
      baseCode: 'UNAVAILABLE',
      message: '网络请求失败或超时，未取得服务端响应。',
      retryable: true,
      status: 0,
      transport: true,
      unknownOutcomeHint: true,
    });
  }

  if (response.status === 204) return undefined as T;

  const text = await response.text();
  let payload: unknown = null;
  if (text.length > 0) {
    try {
      payload = JSON.parse(text);
    } catch {
      payload = { raw: text };
    }
  }

  if (!response.ok) {
    throw toApiError(response.status, payload);
  }

  return payload as T;
}

/** contracts/03 §1.4 —— 列表统一 cursor 分页：{items, next_cursor} */
export function fetchCursorPage<T>(path: string, options?: RequestOptions): Promise<CursorPage<T>> {
  return apiGet<CursorPage<T>>(path, options);
}

/** 便利方法：列表查询（携带 limit/cursor/过滤参数） */
export function fetchList<T>(
  path: string,
  query: Record<string, QueryValue>,
  options?: RequestOptions,
): Promise<CursorPage<T>> {
  return apiGet<CursorPage<T>>(path, { ...options, query });
}

/**
 * 管理控制台列表页的统一筛选口径（要求：全部 10 行/页 + 支持筛选、查询）。
 *
 * 关键字一律走服务端 `?q=`（ILIKE 检索）：前端本地过滤在 cursor 翻页时
 * 只会过滤当前页，用户会看到"数据凭空消失"，属于假查询。
 */
export interface ListQueryFilters {
  status?: string;
  q?: string;
  cursor?: string;
}

/** 把 ListQueryFilters 折算为 fetchList 的 query 参数（空串一律不下发）。 */
export function listFilterQuery(filters: ListQueryFilters = {}, limit = 10): Record<string, QueryValue> {
  return {
    limit,
    status: filters.status || undefined,
    q: filters.q || undefined,
    cursor: filters.cursor || undefined,
  };
}
