import { useEffect, useRef, useState } from 'react';
import { Icon } from '@/components/icon';
import { useSession } from '@/hooks/useSession';

/**
 * 顶栏用户菜单。
 *
 * 为什么需要它：原顶栏把"租户 · 用户名 · 组织路径"平铺成一行纯文本，
 * 身份信息没有层级、也不可展开查看主体 ID（§12.3 业务 ID 需可复制）。
 * 这里收敛为头像 + 姓名，点击展开身份明细，减少顶栏视觉噪声（§3.1 业务端克制）。
 *
 * 注意：菜单内展示的是**当前会话的最小投影**，不是权限全集；
 * 权威授权始终由服务端实时判定（§10.3 / §11.2 第三层）。
 */
export function UserMenu({ envLabel }: { envLabel: string }) {
  const session = useSession();
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(event.target as Node)) setOpen(false);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setOpen(false);
    };
    document.addEventListener('mousedown', onPointerDown);
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('mousedown', onPointerDown);
      document.removeEventListener('keydown', onKeyDown);
    };
  }, [open]);

  const initial = session.user.display_name.slice(0, 1);

  return (
    <div className="user-menu" ref={rootRef}>
      <button
        type="button"
        className="user-menu__trigger"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        <span className="avatar" aria-hidden="true">
          {initial}
        </span>
        <span className="user-menu__trigger-name">{session.user.display_name}</span>
        <Icon name="chevronDown" size={14} className="user-menu__trigger-chevron" />
      </button>

      {open ? (
        <div className="user-menu__panel" role="menu">
          <div className="user-menu__identity">
            <span className="avatar" aria-hidden="true">
              {initial}
            </span>
            <div style={{ minWidth: 0 }}>
              <div className="user-menu__identity-name">{session.user.display_name}</div>
              <div className="user-menu__identity-meta">{session.user.org_path ?? '未分配组织'}</div>
            </div>
          </div>

          <div className="divider" style={{ margin: '4px 0' }} />

          <div className="user-menu__row">
            <Icon name="building" size={15} className="user-menu__row-icon" />
            租户
            <span className="user-menu__row-value">{session.user.tenant_name}</span>
          </div>
          <div className="user-menu__row">
            <Icon name="info" size={15} className="user-menu__row-icon" />
            环境
            <span className="user-menu__row-value">{envLabel}</span>
          </div>
          <div className="user-menu__row">
            <Icon name="link" size={15} className="user-menu__row-icon" />
            主体 ID
            <span className="user-menu__row-value mono">{session.user.subject_id}</span>
          </div>

          <p className="user-menu__note">
            身份与权限以服务端实时判定为准，此处仅为当前会话的最小投影。
          </p>
        </div>
      ) : null}
    </div>
  );
}
