/**
 * lib/http.ts —— HTTP 传输层通用工具（无业务语义）
 */

/** 拼接 base 与路径，保证单斜杠 */
export function joinUrl(base: string, path: string): string {
  const b = base.replace(/\/+$/, '');
  const p = path.startsWith('/') ? path : `/${path}`;
  return `${b}${p}`;
}

export type QueryValue = string | number | boolean | undefined | null;

/** 构造查询串；空值被忽略（contracts/03 §1.5 过滤用查询参数） */
export function buildQueryString(query?: Record<string, QueryValue>): string {
  if (!query) return '';
  const sp = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === null || value === '') continue;
    sp.append(key, String(value));
  }
  const s = sp.toString();
  return s ? `?${s}` : '';
}

/**
 * 把 scope 归一化为**可安全放入 HTTP 头值**的形式。
 *
 * 头值在 Fetch/WebIDL 中是 `ByteString`：任何码点 > 0xFF 都会让 `Headers`
 * 构造抛 `TypeError`，而该异常从 `fetch()` 内部抛出——请求**根本不会发出**，
 * 前端只能看到"网络请求失败或超时，未取得服务端响应"。调用方常把中文文件名
 * 或中文路径拼进 scope（如 `采购管理制度.docx`），因此这里必须净化。
 *
 * 净化规则：剔除 ASCII 可见字符以外的字符（可读前缀保留），再追加原文的
 * FNV-1a 摘要——否则"采购制度.docx"与"合同制度.docx"这类不同 scope 会
 * 归一化成同一个键，破坏幂等语义（同一输入才应复用同一个键）。
 */
function toHeaderSafe(scope: string): string {
  if (/^[\x20-\x7E]*$/.test(scope)) return scope;
  const cleaned = scope.replace(/[^\x20-\x7E]/g, '');
  return `${cleaned.slice(0, 64)}-h${hash32(scope)}`;
}

/** FNV-1a 32 位摘要（仅用于让净化后的键保持唯一，不承载安全语义）。 */
function hash32(value: string): string {
  let h = 0x811c9dc5;
  for (let i = 0; i < value.length; i += 1) {
    h ^= value.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return (h >>> 0).toString(36);
}

/** 生成 Idempotency-Key（回不回显完整哈希由服务端策略决定，前端只提供引用） */
export function newIdempotencyKey(scope = 'ui'): string {
  const rand = Math.random().toString(36).slice(2, 10);
  return `${toHeaderSafe(scope)}-${Date.now().toString(36)}-${rand}`;
}

export function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(new DOMException('Aborted', 'AbortError'));
      return;
    }
    const timer = setTimeout(() => {
      signal?.removeEventListener('abort', onAbort);
      resolve();
    }, ms);
    const onAbort = () => {
      clearTimeout(timer);
      reject(new DOMException('Aborted', 'AbortError'));
    };
    signal?.addEventListener('abort', onAbort, { once: true });
  });
}

export function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === 'AbortError';
}

/**
 * 指数退避 + 抖动（§10.6.1 轮询降级路径：1s → 2s → 4s → 上限 30s）
 */
export function backoffDelay(attempt: number, capMs = 30_000, baseMs = 1000): number {
  const exp = Math.min(capMs, baseMs * 2 ** Math.max(0, attempt - 1));
  const jitter = Math.random() * exp * 0.25; // 抖动 ≤25%，避免惊群
  return Math.round(exp + jitter);
}

/** 敏感字段/头部（§13.3：密钥或敏感数据不得泄露到浏览器日志） */
export const SENSITIVE_KEYS = [
  'authorization',
  'cookie',
  'set-cookie',
  'idempotency-key',
  'if-match',
  'api_key',
  'apikey',
  'api-key',
  'secret',
  'password',
  'token',
  'credential',
  'private_key',
] as const;

const MASK = '***redacted***';

/** 供调试使用的脱敏器：任何可能进入日志的对象都应先经过它 */
export function sanitizeForLog(value: unknown, depth = 0): unknown {
  if (depth > 4) return MASK;
  if (value === null || value === undefined) return value;
  if (Array.isArray(value)) return value.map((v) => sanitizeForLog(v, depth + 1));
  if (typeof value === 'object') {
    const out: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      out[k] = SENSITIVE_KEYS.includes(k.toLowerCase() as (typeof SENSITIVE_KEYS)[number])
        ? MASK
        : sanitizeForLog(v, depth + 1);
    }
    return out;
  }
  return value;
}
