/**
 * types/api.ts —— API 传输层类型
 * 权威：docs/contracts/02 §4（错误响应结构）、docs/contracts/03 §1.4（cursor 分页）、
 *       §1.6（Idempotency-Key）、§1.7（错误响应）、§1.8（长时操作）
 */

/** contracts/03 §1.4 —— 列表统一 cursor 式分页，禁止 offset 深分页 */
export interface CursorPage<T> {
  items: T[];
  next_cursor: string | null;
}

/** contracts/02 §4 —— 统一错误响应结构（message/details 必须脱敏） */
export interface ApiErrorBody {
  code: string;
  base_code: string;
  message: string;
  retryable: boolean;
  details?: Record<string, unknown>;
}

export interface ApiErrorEnvelope {
  error: ApiErrorBody;
  request_id: string;
}

/** contracts/03 §1.1 —— 统一前缀 */
export const API_PREFIX = '/api/v1';

/** 请求可选项：写操作需支持幂等键与乐观并发（§10.6.4） */
export interface RequestOptions {
  /** contracts/03 §1.6 —— 透传为 Idempotency-Key 请求头 */
  idempotencyKey?: string;
  /** §10.6.4 —— 透传为 If-Match 请求头（ETag 或 version） */
  ifMatch?: string;
  /** 透传为 If-None-Match（条件读） */
  ifNoneMatch?: string;
  signal?: AbortSignal;
  /** 查询参数；undefined / null 会被忽略 */
  query?: Record<string, string | number | boolean | undefined | null>;
  headers?: Record<string, string>;
}

/** 可重试判定结果（依据契约 retryable，§D） */
export interface RetryGuidance {
  retryable: boolean;
  /** 面向用户的下一步指引（中文，脱敏） */
  guidance: string;
  /** 建议退避毫秒（若适用） */
  backoffMs?: number;
}
