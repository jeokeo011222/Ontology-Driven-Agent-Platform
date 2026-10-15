import { useEffect, useMemo, useRef } from 'react';
import { cn } from '@/lib/cn';
import { highlightDocument, type ArtifactLanguage, type Token, type TokenKind } from '@/lib/highlight';

/** 片段类别：着色类别，外加检索命中（命中优先于着色）。 */
type PieceKind = TokenKind | 'match';

interface Piece {
  kind: PieceKind;
  text: string;
  /** 命中序号（按文档顺序，0 基）。非命中片段为 undefined。 */
  matchIndex?: number;
}

interface MarkedLine {
  pieces: Piece[];
}

export interface CodeViewProps {
  content: string;
  language: ArtifactLanguage;
  /** 检索词（大小写不敏感，首尾空白忽略）。空串表示不检索。 */
  query?: string;
  /** 当前命中序号（0 基，按文档顺序）。越界会收敛到最近的合法值。 */
  activeMatch?: number;
  /**
   * 总命中数回调。
   *
   * 为什么由这里回报而不是让调用方自己数一遍：命中的判定（大小写、整行匹配）只在本
   * 文件里定义一次，调用方另数一遍迟早与高亮结果不一致，出现"显示 3/5 却只标了 2 处"。
   */
  onMatchCount?: (total: number) => void;
  className?: string;
}

/**
 * 带行号与着色的只读代码视图。
 *
 * 三条不可让步的约定：
 *   1. **文本保真**：着色在 lib/highlight 里已做逐字节校验，这里只负责切分命中，
 *      切分只按字符区间切、不改字，因此界面上看到的与接口返回的逐字相同。
 *   2. **长度与真实一致**：命中计数只在切分里算一次并经 onMatchCount 回报，
 *      调用方据此显示"第 n / 共 m 个"，不另起一套计数。
 *   3. **软换行而非横向滚动**：长 IRI 换行显示，行号仍对齐到逻辑行的首行；
 *      若改成横向滚动，抽屉里会出现"内容区 + 代码区"两条横向滚动条，反而更难读。
 */
export function CodeView({
  content,
  language,
  query = '',
  activeMatch = 0,
  onMatchCount,
  className,
}: CodeViewProps) {
  const lines = useMemo(() => highlightDocument(content, language), [content, language]);
  const marked = useMemo(() => markMatches(lines, query), [lines, query]);

  // 回调只经 ref 读取：调用方通常写成内联箭头函数，每次渲染都是新引用，
  // 放进依赖数组会让本效果在每次渲染后重跑（同 Drawer.tsx 的 onClose 说明）。
  const onMatchCountRef = useRef(onMatchCount);
  onMatchCountRef.current = onMatchCount;
  useEffect(() => {
    onMatchCountRef.current?.(marked.total);
  }, [marked.total]);

  const rootRef = useRef<HTMLDivElement>(null);
  const current = marked.total > 0 ? Math.min(Math.max(activeMatch, 0), marked.total - 1) : -1;

  // 把当前命中滚到可视区中间。依赖里带 query：改检索词时即使序号没变，
  // 也要重新定位到新的第一处命中，否则看到的是上一次的位置。
  useEffect(() => {
    if (current < 0) return;
    rootRef.current?.querySelector<HTMLElement>('[data-match-current="true"]')?.scrollIntoView({
      block: 'center',
    });
  }, [current, marked.total, query]);

  return (
    <div ref={rootRef} className={cn('code-block', 'code-view', className)}>
      {marked.lines.map((line, lineIndex) => (
        <div className="code-view__line" key={lineIndex}>
          <span className="code-view__no" aria-hidden="true">
            {lineIndex + 1}
          </span>
          <span className="code-view__code">
            {line.pieces.map((piece, pieceIndex) =>
              piece.matchIndex === undefined ? (
                <span className={cn('code-view__t', `code-view__t--${piece.kind}`)} key={pieceIndex}>
                  {piece.text}
                </span>
              ) : (
                <mark
                  key={pieceIndex}
                  data-match-current={piece.matchIndex === current ? 'true' : undefined}
                  className={cn('code-view__match', piece.matchIndex === current && 'code-view__match--current')}
                >
                  {piece.text}
                </mark>
              ),
            )}
          </span>
        </div>
      ))}
    </div>
  );
}

/**
 * 在已分词的文本上叠加检索标记。
 *
 * 命中在**整行文本**上查找，而不是逐个 token 查找：像 `"1200.5"^^<http://…>` 这种
 * 横跨字符串与标点的串，若按 token 找就会搜不到——表现为"库里有的串在界面上搜不出来"。
 * 找到命中区间后再把区间切回 token 上，颜色与高亮因此可以共存。
 *
 * 跨 token 的命中用 pending 记录剩余区间，保证一处命中被切成多段时也只算一个序号。
 */
function markMatches(lines: Token[][], query: string): { lines: MarkedLine[]; total: number } {
  const needle = query.trim().toLowerCase();
  if (needle === '') {
    return {
      lines: lines.map((tokens) => ({ pieces: tokens.map((token) => ({ kind: token.kind, text: token.text })) })),
      total: 0,
    };
  }

  let total = 0;
  const marked = lines.map((tokens) => {
    const text = tokens.map((token) => token.text).join('');
    const haystack = text.toLowerCase();
    const hits: { start: number; end: number }[] = [];
    for (let from = 0; ; ) {
      const at = haystack.indexOf(needle, from);
      if (at === -1) break;
      hits.push({ start: at, end: at + needle.length });
      // 非重叠推进：从命中末尾继续找，避免 "aa" 在 "aaa" 里算成两处重叠命中。
      from = at + needle.length;
    }

    const pieces: Piece[] = [];
    const push = (piece: Piece) => {
      const last = pieces[pieces.length - 1];
      if (last && last.kind === piece.kind && piece.matchIndex === undefined && last.matchIndex === undefined) {
        last.text += piece.text;
        return;
      }
      pieces.push(piece);
    };

    let offset = 0;
    let hitCursor = 0;
    let pending: { end: number; index: number } | null = null;

    for (const token of tokens) {
      const tokenStart = offset;
      const tokenEnd = offset + token.text.length;
      let cursor = tokenStart;

      while (cursor < tokenEnd) {
        if (pending) {
          const end = Math.min(pending.end, tokenEnd);
          push({ kind: 'match', text: text.slice(cursor, end), matchIndex: pending.index });
          cursor = end;
          if (end === pending.end) pending = null;
          continue;
        }
        // 丢掉已经整体落在游标左侧的命中（它们已在上一个 token 里画完）。
        while (hitCursor < hits.length) {
          const candidate = hits[hitCursor];
          if (!candidate || candidate.end > cursor) break;
          hitCursor += 1;
        }
        const hit = hits[hitCursor];
        if (!hit || hit.start >= tokenEnd) {
          push({ kind: token.kind, text: text.slice(cursor, tokenEnd) });
          cursor = tokenEnd;
          continue;
        }
        if (hit.start > cursor) {
          push({ kind: token.kind, text: text.slice(cursor, hit.start) });
          cursor = hit.start;
          continue;
        }
        const index = total;
        total += 1;
        const end = Math.min(hit.end, tokenEnd);
        push({ kind: 'match', text: text.slice(cursor, end), matchIndex: index });
        cursor = end;
        hitCursor += 1;
        if (end < hit.end) pending = { end: hit.end, index };
      }

      offset = tokenEnd;
    }

    return { pieces };
  });

  return { lines: marked, total };
}
