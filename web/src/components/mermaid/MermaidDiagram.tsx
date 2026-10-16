import { useEffect, useId, useRef, useState } from 'react';
import mermaid from 'mermaid';

/**
 * 通用 Mermaid 图表渲染组件（Mermaid v10+ 新 API）。
 *
 * 使用 `mermaid.run({ nodes })` 进行渲染，不再使用已废弃的全局 `mermaid.render()`。
 * 每个实例自带唯一 id，避免 SVG 内部 id 冲突。
 *
 * 支持的图表类型：flowchart / sequenceDiagram / classDiagram / stateDiagram /
 * erDiagram / gantt / pie / journey 等。
 *
 * @example
 * ```tsx
 * <MermaidDiagram code={`
 *   flowchart TD
 *     A[开始] --> B{判断}
 *     B -->|是| C[执行]
 *     B -->|否| D[结束]
 * `} />
 * ```
 */

export interface MermaidDiagramProps {
  /** Mermaid 图表代码（字符串）。 */
  code: string;
  /** 可选的自定义 DOM id；不传则自动生成唯一 id。 */
  id?: string;
  /** 额外的 className，用于自定义容器样式。 */
  className?: string;
}

let initialized = false;

/**
 * 初始化 mermaid（全局只需执行一次）。
 * 延迟到第一次使用时再初始化，避免 bundle 加载即产生副作用。
 */
function ensureInitialized() {
  if (initialized) return;
  mermaid.initialize({
    startOnLoad: false,
    securityLevel: 'strict',
    theme: 'default',
  });
  initialized = true;
}

export function MermaidDiagram({ code, id, className }: MermaidDiagramProps) {
  // useId 生成的 id 是 :r1: 这种格式，里面有冒号，不能直接用作 DOM id。
  // 去掉非字母数字字符，得到合法的 id。
  const autoId = useId().replace(/[^a-zA-Z0-9]/g, '');
  const nodeId = id ?? `mermaid-${autoId}`;

  const containerRef = useRef<HTMLDivElement | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;

    ensureInitialized();

    // 每次 code 变化时重置：清空容器并写入新代码文本
    container.textContent = code;
    delete (container as HTMLElement).dataset.processed;
    setError(null);

    let cancelled = false;
    mermaid
      .run({ nodes: [container], suppressErrors: true })
      .then(() => {
        if (!cancelled) setError(null);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        const message =
          err instanceof Error ? err.message : typeof err === 'string' ? err : 'Mermaid 渲染失败';
        setError(message);
      });

    return () => {
      cancelled = true;
    };
  }, [code]);

  if (error) {
    return (
      <div
        id={nodeId}
        className={`mermaid-diagram mermaid-diagram--error ${className ?? ''}`}
        role="alert"
      >
        <div className="mermaid-diagram__error-title">Mermaid 渲染失败</div>
        <pre className="mermaid-diagram__error-detail">{error}</pre>
        <pre className="mermaid-diagram__error-code">{code}</pre>
      </div>
    );
  }

  return (
    <div
      id={nodeId}
      ref={containerRef}
      className={`mermaid-diagram mermaid ${className ?? ''}`}
      // data-processed 是 mermaid.run 用来识别"已渲染过"的标记。
      // 我们在 effect 里主动删除它，确保每次都重新渲染。
    >
      {/* mermaid 代码作为纯文本放在这里，mermaid.run 会自动替换为 SVG */}
      {code}
    </div>
  );
}
