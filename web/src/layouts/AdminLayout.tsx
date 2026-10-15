import { useState } from 'react';
import { Outlet } from 'react-router-dom';
import { useQueryClient } from '@tanstack/react-query';
import {
  BrandLogo,
  CollapseToggle,
  Sidenav,
  UserMenu,
  WorkspaceSwitch,
  type SidenavGroup,
} from '@/components/app-shell';
import { RealtimeBanner } from '@/components/error-state';
import { Icon } from '@/components/icon';
import { cn } from '@/lib/cn';
import { useSession } from '@/hooks/useSession';
import { useSSE } from '@/hooks/useSSE';
import { useTheme } from '@/hooks/useTheme';

const ENV_LABEL: Record<string, string> = {
  dev: '开发环境',
  staging: '预发布环境',
  prod: '生产环境',
};

/**
 * §2.1 平台管理控制台顶层导航。
 * 分组与设计书一致；每项补齐图标，使 14 项菜单可被"图标 + 文字"双重扫读。
 */
const NAV_GROUPS: SidenavGroup[] = [
  {
    title: '平台管理',
    icon: 'settings',
    items: [
      { to: '/admin', label: '管理概览', icon: 'gauge', end: true },
      { to: '/admin/organizations', label: '组织管理', icon: 'building' },
      { to: '/admin/users', label: '用户管理', icon: 'user' },
      { to: '/admin/identity', label: '身份与权限', icon: 'shieldCheck' },
    ],
  },
  {
    title: '设计与执行',
    icon: 'hammer',
    items: [
      // 「企业本体」页面内已并入「业务领域分类」分区（页内页签），
      // 故此处不再有独立的分类导航项。
      { to: '/admin/ontologies', label: '企业本体', icon: 'ontology' },
      { to: '/admin/agents', label: 'Agent 管理', icon: 'bot' },
      { to: '/admin/skills', label: '技能管理', icon: 'sparkles' },
      { to: '/admin/tools', label: '工具管理', icon: 'wrench' },
      { to: '/admin/workflows', label: '工作流与 Harness', icon: 'workflow' },
    ],
  },
  {
    title: '模型与集成',
    icon: 'plug',
    items: [
      { to: '/admin/models', label: '模型与推理', icon: 'cpu' },
      { to: '/admin/connectors', label: '与ERP集成', icon: 'link' },
      { to: '/admin/data-sources', label: '数据接入与映射', icon: 'database' },
    ],
  },
  {
    title: '治理与质量',
    icon: 'shieldCheck',
    items: [
      { to: '/admin/policies', label: '策略与审批', icon: 'scale' },
      { to: '/admin/monitoring', label: '运行监控', icon: 'activity' },
      { to: '/admin/audit', label: '审计与证据', icon: 'fileSearch' },
      { to: '/admin/evaluation', label: '评估与质量门禁', icon: 'clipboardCheck' },
      { to: '/admin/releases', label: '插件与版本发布', icon: 'package' },
    ],
  },
];

/**
 * layouts/AdminLayout.tsx —— 平台管理控制台外壳
 * 视觉：表格、配置面板、版本差异与状态信息，高信息密度（§3.1 平台管理端）。
 */
export function AdminLayout() {
  const session = useSession();
  const queryClient = useQueryClient();
  const [collapsed, setCollapsed] = useState(false);
  const { theme, toggleTheme } = useTheme();

  const sse = useSSE({
    enabled: true,
    onFallbackPoll: () => {
      void queryClient.invalidateQueries({ queryKey: ['admin'] });
      void queryClient.invalidateQueries({ queryKey: ['monitoring'] });
      void queryClient.invalidateQueries({ queryKey: ['audit'] });
    },
  });

  const envLabel = ENV_LABEL[session.env] ?? session.env;

  return (
    <div className="app-shell app-shell--split">
      <header className="topbar">
        <BrandLogo name="ODAP Console" sub="Ontology-Driven Agent Platform" />

        <WorkspaceSwitch />

        <div className="topbar__spacer" />

        <div className="topbar__utils">
          <span
            className={cn('topbar__env', `topbar__env--${session.env}`)}
            title="当前环境标识：生产环境标识始终明显可见"
          >
            {envLabel}
          </span>
          <span className="chip topbar__item--tenant" title="当前租户">
            <Icon name="building" size={14} className="chip__icon" />
            {session.user.tenant_name}
          </span>
          <CollapseToggle collapsed={collapsed} onToggle={() => setCollapsed((v) => !v)} />
          <button
            type="button"
            className="icon-btn"
            onClick={toggleTheme}
            aria-label={theme === 'dark' ? '切换到亮色模式' : '切换到暗色模式'}
            title={theme === 'dark' ? '切换到亮色模式' : '切换到暗色模式'}
          >
            <Icon name={theme === 'dark' ? 'sun' : 'moon'} size={16} />
          </button>
          <UserMenu envLabel={envLabel} />
        </div>
      </header>

      <div className={cn('workspace-body', collapsed && 'workspace-body--collapsed')}>
        <Sidenav groups={NAV_GROUPS} collapsed={collapsed} label="管理控制台导航" />

        <main className="main-region" id="main-content">
          <a className="skip-link" href="#main-content">
            跳到主要内容
          </a>
          <RealtimeBanner
            degraded={sse.degraded}
            connected={sse.connected}
            error={sse.error}
            failureCount={sse.failureCount}
            lastEventId={sse.lastEventId}
            onReconnect={sse.reconnect}
          />
          <Outlet />
        </main>
      </div>
    </div>
  );
}
