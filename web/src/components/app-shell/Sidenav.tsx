import { NavLink } from 'react-router-dom';
import { Icon, type IconName } from '@/components/icon';

export interface SidenavItem {
  to: string;
  label: string;
  icon: IconName;
  /** 右侧计数（如待办数）。undefined/0 不渲染，避免出现无意义的"0" */
  badge?: number;
  /** 精确匹配：父级路径项（如 /admin）需要，否则子路由会误判为选中 */
  end?: boolean;
}

export interface SidenavGroup {
  title: string;
  icon?: IconName;
  items: SidenavItem[];
}

export interface SidenavProps {
  groups: SidenavGroup[];
  /** 折叠态：仅保留图标，靠 title 提供可读名称 */
  collapsed: boolean;
  /** 导航区域无障碍名称 */
  label: string;
}

/**
 * 侧边导航。
 *
 * 相对原实现的改进（这是"界面简陋"最直观的部分）：
 *   1. 每项增加图标——纯文字导航在 14 项管理菜单下需要逐行阅读才能定位；
 *   2. 选中项同时用"浅底 + 品牌色 + 左侧标识条"三重表达（§3.2 颜色不是唯一表达）；
 *   3. 分组标题带图标，形成信息架构层级；
 *   4. 支持折叠，窄屏自动退化为横向布局（见 globals.css §12.1）。
 */
export function Sidenav({ groups, collapsed, label }: SidenavProps) {
  return (
    <aside className="sidenav" aria-label={label}>
      {groups.map((group) => (
        <div className="sidenav__group" key={group.title}>
          <p className="sidenav__section-title">
            {group.icon ? <Icon name={group.icon} size={13} className="sidenav__section-icon" /> : null}
            {group.title}
          </p>
          {group.items.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              end={item.end}
              className="sidenav__link"
              title={collapsed ? item.label : undefined}
            >
              <Icon name={item.icon} size={18} className="sidenav__link-icon" />
              <span className="sidenav__link-label">{item.label}</span>
              {item.badge ? <span className="sidenav__link-badge">{item.badge}</span> : null}
            </NavLink>
          ))}
        </div>
      ))}
    </aside>
  );
}
