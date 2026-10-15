/**
 * api/query-keys.ts —— TanStack Query key 集中管理（§10.3）
 *
 * 规则：
 *   1. 所有 query-key 只能在本文件声明，页面/feature 不得自造 key。
 *   2. 缓存失效按“资源 + 版本”维度显式声明；不可变配置版本号进入 key，
 *      版本变化即自然失效重取（§10.3 表格）。
 *   3. 切换租户后必须整体清除缓存（§13.2），由 providers 层在租户变更时
 *      queryClient.clear() 完成。
 */

export interface RunListFilters {
  status?: string;
  business_type?: string;
  q?: string;
  order?: string;
  limit?: number;
  cursor?: string | null;
}

export interface ApprovalListFilters {
  status?: string;
  limit?: number;
  cursor?: string | null;
}

export interface AuditListFilters {
  resource_type?: string;
  outcome?: string;
  actor_id?: string;
  limit?: number;
  cursor?: string | null;
}

export const queryKeys = {
  /* 会话 */
  session: {
    me: () => ['session', 'me'] as const,
    permissions: () => ['session', 'permissions'] as const,
  },

  /* 业务工作台（BFF 聚合端点） */
  workspace: {
    summary: () => ['workspace', 'summary'] as const,
    /**
     * 工作台可用本体清单（`GET /api/v1/workspace/ontologies`）。
     *
     * 与 `ontologies.list()` 分开分桶：管理端列表与工作台清单的请求参数（limit）
     * 不同，共用 key 会让「管理端加载了 50 条」被工作台当成已有缓存直接复用。
     */
    ontologies: () => ['workspace', 'ontologies'] as const,
  },
  apps: {
    list: () => ['apps', 'list'] as const,
  },

  /* Run（唯一执行资源） */
  runs: {
    all: () => ['runs'] as const,
    list: (filters: RunListFilters) => ['runs', 'list', filters] as const,
    detail: (runId: string) => ['runs', 'detail', runId] as const,
    steps: (runId: string) => ['runs', 'steps', runId] as const,
    erpOperations: (runId: string) => ['runs', 'erp-operations', runId] as const,
  },

  /* 审批 */
  approvals: {
    all: () => ['approvals'] as const,
    list: (filters: ApprovalListFilters) => ['approvals', 'list', filters] as const,
    detail: (id: string) => ['approvals', 'detail', id] as const,
  },

  /* 异步任务（长任务，§10.6.3） */
  tasks: {
    detail: (taskId: string) => ['tasks', 'detail', taskId] as const,
  },

  /* 本体（版本进入 key，天然失效） */
  ontologies: {
    all: () => ['ontologies'] as const,
    list: () => ['ontologies', 'list'] as const,
    detail: (id: string, version?: string) => ['ontologies', 'detail', id, version ?? 'current'] as const,
    /* 产物文本（RDF/OWL/SHACL）。按本体分桶：换一个本体就是另一份内容。 */
    artifacts: (id: string) => ['ontologies', 'artifacts', id] as const,
  },

  /* 平台设置 */
  modelProviders: {
    list: () => ['model-providers', 'list'] as const,
    detail: (id: string) => ['model-providers', 'detail', id] as const,
  },
  modelRevisions: {
    list: (endpointId: string) => ['model-revisions', 'list', endpointId] as const,
  },
  connectors: {
    list: () => ['connectors', 'list'] as const,
    detail: (id: string) => ['connectors', 'detail', id] as const,
  },
  identity: {
    roles: () => ['identity', 'roles'] as const,
    roleBindings: () => ['identity', 'role-bindings'] as const,
    permissions: () => ['identity', 'permissions'] as const,
    subjects: () => ['identity', 'subjects'] as const,
  },
  policies: {
    list: () => ['policies', 'list'] as const,
    versions: (id: string) => ['policies', 'versions', id] as const,
  },

  /* 设计资产 */
  agents: { list: () => ['agents', 'list'] as const, detail: (id: string) => ['agents', 'detail', id] as const },
  tools: { list: () => ['tools', 'list'] as const },
  workflows: { list: () => ['workflows', 'list'] as const },
  planTemplates: { list: () => ['plan-templates', 'list'] as const },
  dataSources: { list: () => ['data-sources', 'list'] as const },

  /* 治理 */
  monitoring: { alerts: () => ['monitoring', 'alerts'] as const },
  audit: {
    list: (filters: AuditListFilters) => ['audit', 'list', filters] as const,
    detail: (id: string) => ['audit', 'detail', id] as const,
  },
  evaluation: {
    list: () => ['evaluation', 'list'] as const,
    detail: (id: string) => ['evaluation', 'detail', id] as const,
  },
  releases: { list: () => ['releases', 'list'] as const },

  /* 管理概览（BFF 聚合端点） */
  admin: {
    overview: () => ['admin', 'overview'] as const,
  },
} as const;
