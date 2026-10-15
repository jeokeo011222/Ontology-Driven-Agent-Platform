import type { ReactNode } from 'react';
import { Navigate, createBrowserRouter } from 'react-router-dom';
import { RequireAuth, RequirePermission, RouteNotFound } from './route-guards';
import { BusinessLayout } from '@/layouts/BusinessLayout';
import { AdminLayout } from '@/layouts/AdminLayout';
import { WorkspacePage } from '@/features/workspace/pages/WorkspacePage';
import { AssistantPage } from '@/features/assistant/pages/AssistantPage';
import { TasksPage } from '@/features/tasks/pages/TasksPage';
import { TaskDetailPage } from '@/features/tasks/pages/TaskDetailPage';
import { ApprovalsPage } from '@/features/approvals/pages/ApprovalsPage';
import { ApprovalDetailPage } from '@/features/approvals/pages/ApprovalDetailPage';
import { BusinessAppsPage } from '@/features/business-apps/pages/BusinessAppsPage';
import { AdminOverviewPage } from '@/features/monitoring/pages/AdminOverviewPage';
import { IdentityPage } from '@/features/policies/pages/IdentityPage';
import { OntologyListPage } from '@/features/ontology/pages/OntologyListPage';
import { OntologyDetailPage } from '@/features/ontology/pages/OntologyDetailPage';
import { DataMappingPage } from '@/features/data-mapping/pages/DataMappingPage';
import { AgentsPage } from '@/features/agents/pages/AgentsPage';
import { ToolsPage } from '@/features/tools/pages/ToolsPage';
import { WorkflowsPage } from '@/features/workflows/pages/WorkflowsPage';
import { ModelsPage } from '@/features/llm-providers/pages/ModelsPage';
import { ConnectorsPage } from '@/features/erp-connectors/pages/ConnectorsPage';
import { PoliciesPage } from '@/features/policies/pages/PoliciesPage';
import { MonitoringPage } from '@/features/monitoring/pages/MonitoringPage';
import { AuditPage } from '@/features/audit/pages/AuditPage';
import { EvaluationPage } from '@/features/evaluation/pages/EvaluationPage';
import { ReleasesPage } from '@/features/evaluation/pages/ReleasesPage';
import { LoginPage } from '@/features/auth/pages/LoginPage';
import { RegisterPage } from '@/features/auth/pages/RegisterPage';
import { UsersPage } from '@/features/admin/pages/UsersPage';
import { OrgsPage } from '@/features/admin/pages/OrgsPage';

/**
 * app/router.tsx —— 路由表（§11.1 全部登记）
 *
 * 每个路由都经过第一层权限守卫（RequirePermission）。
 * 守卫只是体验层：服务端仍会对每个目标路由背后的 API 再次授权（§11.2 第三层）。
 *
 * 权限键命名统一遵循第三篇 §5.1：<domain>.<resource>.<action>（点号分隔），
 * 与治理权限目录（migrations/platform/000012 + 000024）及会话投影返回的
 * permissions 数组保持一致。此前的冒号风格（如 workspace:view）与目录键永不匹配，
 * 会把所有页面判为「权限不足」。
 */
function guarded(element: ReactNode, permission: string | string[]): ReactNode {
  return <RequirePermission permission={permission}>{element}</RequirePermission>;
}

export const router = createBrowserRouter([
  // 根路径重定向到业务工作台
  { path: '/', element: <Navigate to="/workspace" replace /> },

  // ---------------- 业务用户工作台 ----------------
  {
    element: (
      <RequireAuth>
        <BusinessLayout />
      </RequireAuth>
    ),
    children: [
      { path: 'workspace', element: guarded(<WorkspacePage />, 'workspace.view') },
      { path: 'assistant', element: guarded(<AssistantPage />, 'workspace.view') },
      { path: 'tasks', element: guarded(<TasksPage />, 'run.read') },
      { path: 'tasks/:runId', element: guarded(<TaskDetailPage />, 'run.read') },
      { path: 'approvals', element: guarded(<ApprovalsPage />, 'approval.read') },
      { path: 'approvals/:id', element: guarded(<ApprovalDetailPage />, 'approval.read') },
      { path: 'apps', element: guarded(<BusinessAppsPage />, 'app.read') },
    ],
  },

  // ---------------- 平台管理控制台 ----------------
  {
    path: '/admin',
    element: (
      <RequireAuth>
        <AdminLayout />
      </RequireAuth>
    ),
    children: [
      { index: true, element: guarded(<AdminOverviewPage />, 'admin.console') },
      { path: 'identity', element: guarded(<IdentityPage />, 'admin.identity.read') },
      { path: 'ontologies', element: guarded(<OntologyListPage />, 'admin.ontology.read') },
      { path: 'ontologies/:id', element: guarded(<OntologyDetailPage />, 'admin.ontology.read') },
      { path: 'data-sources', element: guarded(<DataMappingPage />, 'admin.data-source.read') },
      { path: 'agents', element: guarded(<AgentsPage />, 'admin.agent.read') },
      { path: 'tools', element: guarded(<ToolsPage />, 'admin.tool.read') },
      { path: 'workflows', element: guarded(<WorkflowsPage />, 'admin.workflow.read') },
      { path: 'models', element: guarded(<ModelsPage />, 'admin.model.read') },
      { path: 'connectors', element: guarded(<ConnectorsPage />, 'admin.connector.read') },
      { path: 'policies', element: guarded(<PoliciesPage />, 'admin.policy.read') },
      { path: 'monitoring', element: guarded(<MonitoringPage />, 'admin.monitoring.read') },
      { path: 'audit', element: guarded(<AuditPage />, 'admin.audit.read') },
      { path: 'evaluation', element: guarded(<EvaluationPage />, 'admin.evaluation.read') },
      { path: 'releases', element: guarded(<ReleasesPage />, 'admin.release.read') },
      { path: 'users', element: guarded(<UsersPage />, 'admin.identity.write') },
      { path: 'organizations', element: guarded(<OrgsPage />, 'admin.identity.write') },
    ],
  },

  // 公开认证路由（不走 RequireAuth）
  { path: '/login', element: <LoginPage /> },
  { path: '/register', element: <RegisterPage /> },

  { path: '*', element: <RouteNotFound /> },
]);
