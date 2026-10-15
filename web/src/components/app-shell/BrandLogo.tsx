import { NavLink } from 'react-router-dom';
import { Icon, type IconName } from '@/components/icon';

export interface BrandLogoProps {
  /** 产品名（业务端/管理端可用不同称谓） */
  name: string;
  /** 副标题 */
  sub: string;
}

/**
 * 品牌标识：图标徽记 + 名称 + 副标题。
 *
 * 原实现直接用文字 "ODAP" 充当 logo，缺少品牌识别度；此处改为徽记 + 双行文字，
 * 徽记内用"三点连边"图形呼应产品定位（本体驱动：节点 + 关系）。
 */
export function BrandLogo({ name, sub }: BrandLogoProps) {
  return (
    <div className="topbar__brand">
      <span className="brand-logo" aria-hidden="true">
        <svg
          width="19"
          height="19"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth={1.9}
          strokeLinecap="round"
          strokeLinejoin="round"
          focusable="false"
        >
          <circle cx="12" cy="5.2" r="2.6" />
          <circle cx="5.2" cy="18" r="2.6" />
          <circle cx="18.8" cy="18" r="2.6" />
          <path d="M10.1 7.3 6.6 15.7" />
          <path d="M13.9 7.3l3.5 8.4" />
          <path d="M7.8 18h8.4" />
        </svg>
      </span>
      <span className="brand-text">
        <span className="brand-text__name">{name}</span>
        <span className="brand-text__sub">{sub}</span>
      </span>
    </div>
  );
}

const WORKSPACES: { to: string; icon: IconName; label: string }[] = [
  { to: '/workspace', icon: 'dashboard', label: '业务工作台' },
  { to: '/admin', icon: 'settings', label: '管理控制台' },
];

/** 工作空间切换（§2.1：统一入口下划分两个工作台） */
export function WorkspaceSwitch() {
  return (
    <nav className="nav-switch" aria-label="工作空间切换">
      {WORKSPACES.map((w) => (
        <NavLink key={w.to} className="nav-switch__link" to={w.to}>
          <Icon name={w.icon} size={15} />
          {w.label}
        </NavLink>
      ))}
    </nav>
  );
}
