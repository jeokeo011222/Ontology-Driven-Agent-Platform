import type { ReactNode } from 'react';
import { useEffect, useRef } from 'react';
import { cn } from '@/lib/cn';
import { Icon } from '@/components/icon';

export interface DialogProps {
  open: boolean;
  title: ReactNode;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  wide?: boolean;
}

/**
 * 对话框基础件。
 * §12.2：主要操作可键盘完成；Esc 关闭；打开时焦点移入对话框。
 */
export function Dialog({ open, title, onClose, children, footer, wide }: DialogProps) {
  const panelRef = useRef<HTMLDivElement>(null);
  // onClose 每次渲染都是新引用。若把它放进依赖数组，效果会在**每次输入**后重跑，
  // 把焦点从输入框抢回对话框面板，表现为"只能输入一个字符 / 无法录入"。
  // 因此只在 open 变化时聚焦并挂键盘监听，onClose 通过 ref 读取最新值。
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
      className="dialog-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div
        ref={panelRef}
        className={cn('dialog', wide && 'dialog--wide')}
        role="dialog"
        aria-modal="true"
        aria-label={typeof title === 'string' ? title : undefined}
        tabIndex={-1}
      >
        <header className="dialog__header">
          <h2 className="dialog__title">{title}</h2>
          <button type="button" className="dialog__close" onClick={onClose} aria-label="关闭对话框">
            <Icon name="x" size={16} />
          </button>
        </header>
        <div className="dialog__body">{children}</div>
        {footer ? <footer className="dialog__footer">{footer}</footer> : null}
      </div>
    </div>
  );
}
