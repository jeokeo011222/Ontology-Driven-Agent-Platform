/**
 * lib/cn.ts —— 类名拼接（原生 CSS 方案下的最小工具，不引入 classnames 依赖）
 */
export function cn(...parts: Array<string | false | null | undefined>): string {
  return parts.filter(Boolean).join(' ');
}
