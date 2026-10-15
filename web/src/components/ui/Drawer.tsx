import type { ReactNode } from 'react';
import { useEffect, useRef } from 'react';
import { Icon } from '@/components/icon';
import { cn } from '@/lib/cn';

export interface DrawerProps {
  open: boolean;
  title: ReactNode;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  /** 全宽模式：抽屉占满视口。用于承载"一次要看很多行"的内容（如本体产物文本）。 */
  wide?: boolean;
  /**
   * 页头上的附加操作（放在关闭按钮左侧）。
   *
   * 为什么不直接挂进 children：页头用的是 `justify-content: space-between`，
   * 直接塞第三项会把标题与附加操作各推到一侧、关闭按钮落在正中，视觉上像点错。
   * 因此这里把它与关闭按钮一起包进 `.dialog__header-actions`，页头始终只有两端。
   */
  headerExtra?: ReactNode;
}

/**
 * 右侧抽屉基础件。
 *
 * 与 Dialog 共用同一套交互约定：Esc 关闭、点击遮罩关闭、打开时焦点移入面板。
 * 差别只在定位与宽度——抽屉贴右侧、占满视口高度、更宽，用于承载
 * "逐条查看长文本"这类在小对话框里读不下去的内容。
 *
 * 页头/内容/页脚直接复用 `.dialog__*` 这组模态外壳样式（它们本身就是纵向 flex 布局：
 * 内容区是唯一滚动区、页脚固定），避免把同一份布局规则复制成两份后各自漂移。
 *
 * onClose 通过 ref 读取：它每次渲染都是新引用，若放进依赖数组，效果会在**每次输入**
 * 后重跑并把焦点抢回面板，表现为"只能输入一个字符"（见 Dialog.tsx 的同款说明）。
 */
export function Drawer({ open, title, onClose, children, footer, wide, headerExtra }: DrawerProps) {
  const panelRef = useRef<HTMLDivElement>(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  useEffect(() => {
    if (!open) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onCloseRef.current();
    };
    document.addEventListener('keydown', onKeyDown);
    panelRef.current?.focus();
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [open]);

  if (!open) return null;

  return (
    <div
      className="drawer-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div
        ref={panelRef}
        className={cn('drawer', wide && 'drawer--wide')}
        role="dialog"
        aria-modal="true"
        aria-label={typeof title === 'string' ? title : undefined}
        tabIndex={-1}
      >
        <header className="dialog__header">
          <h2 className="dialog__title">{title}</h2>
          <div className="dialog__header-actions">
            {headerExtra}
            <button type="button" className="dialog__close" onClick={onClose} aria-label="关闭抽屉">
              <Icon name="x" size={16} />
            </button>
          </div>
        </header>
        <div className="dialog__body">{children}</div>
        {footer ? <footer className="dialog__footer">{footer}</footer> : null}
      </div>
    </div>
  );
}
