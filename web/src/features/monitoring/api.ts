import { useQuery } from '@tanstack/react-query';
import { apiGet, fetchList } from '@/api/client';
import { queryKeys } from '@/api/query-keys';
import type { AdminOverview, OpsAlert } from '@/types';

/**
 * 运行监控（§5.3 管理概览 / §8.6 任务队列与执行资源）。
 * 契约：`/api/v1/admin/overview`（BFF 聚合端点，底层 `/api/v1/runs`、`/api/v1/ops/alerts`）、
 *       `/api/v1/ops/alerts`（权威来源第十一篇）。
 */

export function useAdminOverview() {
  return useQuery({
    queryKey: queryKeys.admin.overview(),
    queryFn: () => apiGet<AdminOverview>('/admin/overview'),
  });
}

export function useOpsAlerts() {
  return useQuery({
    queryKey: queryKeys.monitoring.alerts(),
    queryFn: () => fetchList<OpsAlert>('/ops/alerts', { limit: 50 }),
  });
}
