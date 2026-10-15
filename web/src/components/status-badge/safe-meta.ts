/**
 * components/status-badge/safe-meta.ts —— 状态映射的**安全访问器**
 *
 * 为什么必须存在（实测缺陷 #19）：
 *   状态词表声明为 `Record<K, Meta>`，TS 只能保证"已声明的键"在**类型**上完备，
 *   无法保证运行期取到的值落在词表内。当服务端枚举扩展、契约漂移、或字段改名后
 *   拿到 `undefined` 时，`META[value].tone` 会抛
 *     TypeError: Cannot read properties of undefined (reading 'tone')
 *   令整个页面白屏。实测：本体 / Agent / 工具 三个**有数据**的列表页同时中招
 *   （后端返回 `status` + `latest_version_status`，前端却按 `state` 索引词表）。
 *
 * 设计约束（§3.2「颜色不是唯一的状态表达方式」+ 诚实呈现原则）：
 *   1. **不得崩溃** —— 任何取值（含 undefined / null / 未知字符串）都必须能渲染出徽标；
 *   2. **不得静默伪装** —— 未知取值绝不能悄悄回落成某个已知状态（例如伪装成"草稿"），
 *      否则会把"契约漂移"掩盖成一条看似正常的业务结论；
 *   3. **必须显式可见** —— 未知取值渲染为"未知状态"占位（文字 + 图标 + 语义色），
 *      并携带原始取值；开发模式下同时点名具体取值与调用位置，便于定位。
 *
 * 语义色选择：未知取值用 `warning`（不是 `neutral`）。理由：词表外取值属于
 * "需要人确认"的完整性告警，而非某个正常的中性状态；用中性色会与合规的
 * 中性状态混淆，违反约束 3 的"显式可见"。
 */

import type { BadgeTone } from '@/components/ui';
import type { StatusMeta } from './status-maps';

/** 徽标最小元数据（label + 语义色 + 可选悬停说明） */
export interface BadgeMeta {
  label: string;
  tone: BadgeTone;
  /** 悬停说明；未知取值时由安全访问器补齐（写明原始取值） */
  title?: string;
}

/** 未知取值的徽标占位（label + 语义色 + 文字三重表达） */
export const UNKNOWN_BADGE_META: BadgeMeta = { label: '未知状态', tone: 'warning' };

/** 未知取值的完整状态占位（含图标与说明，可直接交给 StatusBadge） */
export const UNKNOWN_STATUS_META: StatusMeta = {
  label: '未知状态',
  tone: 'warning',
  icon: 'help',
  description:
    '该取值不在前端状态词表内（可能为契约漂移，或服务端枚举已扩展）。请以契约真源核对，不要按已成功或已失败解读。',
};

/** 已告警过的 (调用位置, 取值) 组合，避免同类日志刷屏 */
const warned = new Set<string>();

/** 把任意取值渲染成可读片段，用于告警与悬停说明 */
function describeKey(key: unknown): string {
  if (key === undefined) return 'undefined（字段缺失或已改名）';
  if (key === null) return 'null（字段为 null）';
  if (typeof key === 'string') return key === '' ? '空字符串' : `"${key}"`;
  if (typeof key === 'number' || typeof key === 'boolean') return String(key);
  try {
    return JSON.stringify(key) ?? String(key);
  } catch {
    return String(key);
  }
}

function unknownTitle(context: string, key: unknown): string {
  return `状态取值 ${describeKey(key)} 不在前端词表内（${context}）。可能为契约漂移或服务端枚举已扩展，请核对契约真源。`;
}

function warnUnknown(context: string, key: unknown): void {
  // 仅开发模式输出，且同一 (位置, 取值) 只告警一次
  if (!import.meta.env.DEV) return;
  const tag = `${context}\u0000${String(key)}`;
  if (warned.has(tag)) return;
  warned.add(tag);
  console.warn(
    `[status-meta] ${context} 收到词表外的状态取值 ${describeKey(key)}；已渲染为"未知状态"占位。` +
      '请核对契约真源（docs/contracts/01-全局状态注册表.md / contracts/openapi.yaml）。',
  );
}

/**
 * 只接受**自有字符串键**命中，避免 `__proto__` / `constructor` / `toString`
 * 之类原型链键把函数对象当成元数据返回。
 */
function ownLookup<T>(map: Record<string, T>, key: unknown): T | undefined {
  if (typeof key !== 'string' || key.length === 0) return undefined;
  if (!Object.prototype.hasOwnProperty.call(map, key)) return undefined;
  return map[key] ?? undefined;
}

/**
 * 安全读取「标签 + 语义色」类词表。
 *
 * 用法：`const m = resolveBadgeMeta(ONTOLOGY_VERSION_STATUS_META, item.latest_version_status, '本体列表');`
 * 之后 `m.tone` / `m.label` / `m.title` 恒可用——不再有 `META[x].tone` 抛错的可能。
 */
export function resolveBadgeMeta<T extends BadgeMeta>(
  map: Record<string, T>,
  key: unknown,
  context: string,
): BadgeMeta {
  const hit = ownLookup(map, key);
  if (hit) return hit;
  warnUnknown(context, key);
  return { ...UNKNOWN_BADGE_META, title: unknownTitle(context, key) };
}

/**
 * 安全读取完整状态词表（label + 语义色 + 图标 + 说明）。
 *
 * 未命中时返回的 StatusMeta 会把原始取值写进 `description`，
 * 因此 StatusBadge 的 `title` 悬停提示会直接暴露问题取值。
 */
export function resolveStatusMeta<T extends StatusMeta>(
  map: Record<string, T>,
  key: unknown,
  context: string,
): StatusMeta {
  const hit = ownLookup(map, key);
  if (hit) return hit;
  warnUnknown(context, key);
  return { ...UNKNOWN_STATUS_META, description: unknownTitle(context, key) };
}

/** 该取值是否命中词表（需要按"未知"做额外提示时使用） */
export function isKnownKey<T>(map: Record<string, T>, key: unknown): boolean {
  return ownLookup(map, key) !== undefined;
}

/** 供测试复位告警去重表（仅测试使用） */
export function __resetUnknownWarnings(): void {
  warned.clear();
}
