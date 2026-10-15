import { Icon } from '@/components/icon';

export interface CollapseToggleProps {
  collapsed: boolean;
  onToggle: () => void;
}

/** 侧边导航折叠开关：宽屏时给主工作区让出空间 */
export function CollapseToggle({ collapsed, onToggle }: CollapseToggleProps) {
  return (
    <button
      type="button"
      className="icon-btn"
      onClick={onToggle}
      aria-pressed={collapsed}
      aria-label={collapsed ? '展开侧边导航' : '收起侧边导航'}
      title={collapsed ? '展开侧边导航' : '收起侧边导航'}
    >
      <Icon name="panelLeft" size={17} />
    </button>
  );
}
