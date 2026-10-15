/**
 * lib/format.ts —— 展示层格式化
 */

const DATE_TIME: Intl.DateTimeFormatOptions = {
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
};

/** 绝对时间：2026-10-13 10:32 */
export function formatDateTime(iso?: string | null): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return d.toLocaleString('zh-CN', DATE_TIME).replace(/\//g, '-');
}

/** 仅日期：2026-10-13 */
export function formatDate(iso?: string | null): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return d.toLocaleDateString('zh-CN').replace(/\//g, '-');
}

/** 仅时间：10:32 */
export function formatTime(iso?: string | null): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' });
}

/** 相对时间：3 分钟前 */
export function formatRelative(iso?: string | null, now = Date.now()): string {
  if (!iso) return '—';
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return '—';
  const diff = t - now;
  const abs = Math.abs(diff);
  const units: [Intl.RelativeTimeFormatUnit, number][] = [
    ['second', 1000],
    ['minute', 60_000],
    ['hour', 3_600_000],
    ['day', 86_400_000],
    ['month', 2_592_000_000],
    ['year', 31_536_000_000],
  ];
  const rtf = new Intl.RelativeTimeFormat('zh-CN', { numeric: 'auto' });
  let chosen: [Intl.RelativeTimeFormatUnit, number] = units[0];
  for (const u of units) {
    if (abs >= u[1]) chosen = u;
  }
  return rtf.format(Math.round(diff / chosen[1]), chosen[0]);
}

/** 时长（毫秒）→ 1.2s / 3m 4s */
export function formatDuration(ms?: number | null): string {
  if (ms === undefined || ms === null || Number.isNaN(ms) || ms < 0) return '—';
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(1)}s`;
  const m = Math.floor(s / 60);
  const rest = Math.round(s % 60);
  if (m < 60) return `${m}m ${rest}s`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m`;
}

/** 数字千分位 */
export function formatNumber(value?: number | null): string {
  if (value === undefined || value === null || Number.isNaN(value)) return '—';
  return value.toLocaleString('zh-CN');
}

/** 百分比：0.9234 → 92.3% */
export function formatPercent(value?: number | null, fractionDigits = 1): string {
  if (value === undefined || value === null || Number.isNaN(value)) return '—';
  return `${(value * 100).toFixed(fractionDigits)}%`;
}

/** 截断长哈希/ID：保留首尾，用于列表展示（完整值仍可复制） */
export function truncateMiddle(value: string, head = 8, tail = 6): string {
  if (value.length <= head + tail + 1) return value;
  return `${value.slice(0, head)}…${value.slice(-tail)}`;
}

/** 键值对 → 可读的参数摘要列表（审批页参数展示） */
export function formatParameterValue(value: string | number | boolean | null): string {
  if (value === null) return '—';
  if (typeof value === 'boolean') return value ? '是' : '否';
  return String(value);
}
