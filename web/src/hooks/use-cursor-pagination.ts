import { useCallback, useRef, useState } from 'react';

/**
 * useCursorPagination —— cursor 分页的「上一页 / 下一页」状态机。
 *
 * 为什么需要它：后端列表一律是 cursor 分页（contracts/03 §1.4，禁止 offset
 * 深分页），响应只给出 `next_cursor`，**没有上一页游标**。要支持回退，只能由
 * 前端保存已走过的游标栈（上一页 = 栈顶弹出后的那个游标）。
 *
 * 约定：
 *   - 每页固定交给后端 limit（管理控制台列表统一 10 行/页）；
 *   - 筛选条件变化时必须调用 `reset()`，否则会带着旧页游标去请求新筛选的结果，
 *     表现为"翻到第 2 页再改关键字，结果集从中间开始"——这是一种假结果。
 *
 * 复用于管理控制台各列表页，避免每个页面各写一份游标栈。
 */
export interface CursorPagination {
  /** 当前页游标；首页为 null（不下发 cursor 参数）。 */
  cursor: string | null;
  /** 是否能回到上一页。 */
  hasPrevPage: boolean;
  /** 进入下一页（nextCursor 为空表示已是末页，忽略）。 */
  goNext: (nextCursor: string | null) => void;
  /** 回到上一页。 */
  goPrev: () => void;
  /** 回到首页并清空游标栈（筛选条件变化时调用）。 */
  reset: () => void;
}

export function useCursorPagination(): CursorPagination {
  const [cursor, setCursor] = useState<string | null>(null);
  const [stack, setStack] = useState<Array<string | null>>([]);
  // 回退时需要"当前游标"，用 ref 持有以免把 cursor 塞进 goNext 的依赖里。
  const cursorRef = useRef<string | null>(null);
  cursorRef.current = cursor;

  const goNext = useCallback((nextCursor: string | null) => {
    if (!nextCursor) return;
    setStack((prev) => [...prev, cursorRef.current]);
    setCursor(nextCursor);
  }, []);

  const goPrev = useCallback(() => {
    setStack((prev) => {
      if (prev.length === 0) return prev;
      setCursor(prev[prev.length - 1] ?? null);
      return prev.slice(0, -1);
    });
  }, []);

  const reset = useCallback(() => {
    setStack([]);
    setCursor(null);
  }, []);

  return { cursor, hasPrevPage: stack.length > 0, goNext, goPrev, reset };
}
