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
import { useWorkspaceSummary } from '@/features/workspace/api';
import { useTheme } from '@/hooks/useTheme';

const ENV_LABEL: Record<string, string> = {
  dev: '开发环境',
  staging: '预发布环境',
  prod: '生产环境',
};

/**
 * layouts/BusinessLayout.tsx —— 业务用户工作台外壳（§2.1 顶层导航）
 * 视觉：浅色背景、低视觉噪声、明确的主操作（§3.1 业务用户端）。
 */
export function BusinessLayout() {
  const session = useSession();
  const queryClient = useQueryClient();
  const [collapsed, setCollapsed] = useState(false);
  const { theme, toggleTheme } = useTheme();

  const sse = useSSE({
    enabled: true,
    onFallbackPoll: () => {
      // §10.6.2：降级时主动刷新关键资源
      void queryClient.invalidateQueries({ queryKey: ['runs'] });
      void queryClient.invalidateQueries({ queryKey: ['approvals'] });
      void queryClient.invalidateQueries({ queryKey: ['workspace'] });
    },
  });

  // 侧栏"审批中心"待办计数：复用 BFF 聚合端点，与工作台共享同一份缓存；
  // 计数仅统计当前用户有权处理的事项（服务端保证），失败时静默不显示。
  // 字段名以 contracts/openapi.yaml 的 WorkspaceSummary 为准（pending_approvals）。
  const summary = useWorkspaceSummary();
  const pendingCount = summary.data?.pending_approvals;

  const groups: SidenavGroup[] = [
    {
      title: '业务工作台',
      icon: 'dashboard',
      items: [
        { to: '/workspace', label: '工作台', icon: 'dashboard' },
        { to: '/assistant', label: 'AI 业务助手', icon: 'sparkles' },
        { to: '/tasks', label: '我的任务', icon: 'tasks' },
        { to: '/approvals', label: '审批中心', icon: 'award', badge: pendingCount },
        { to: '/apps', label: '业务应用', icon: 'apps' },
      ],
    },
  ];

  const envLabel = ENV_LABEL[session.env] ?? session.env;

  return (
    <div className="app-shell app-shell--split">
      <header className="topbar">
        <BrandLogo name="ODAP" sub="企业本体驱动智能体平台" />

        <WorkspaceSwitch />

        <div className="topbar__spacer" />

        <div className="topbar__utils">
          <span
            className={cn('topbar__env', `topbar__env--${session.env}`)}
            title="当前环境标识"
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
        <Sidenav groups={groups} collapsed={collapsed} label="业务工作台导航" />

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
