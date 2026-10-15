import { useQuery } from '@tanstack/react-query';
import { apiGet, fetchList } from '@/api/client';
import { queryKeys } from '@/api/query-keys';
import type { Run, WorkspaceSummary } from '@/types';

/**
 * 业务工作台聚合端点（BFF）。
 * 契约：`/api/v1/workspace/summary` —— 由前端 BFF 聚合层实现，
 * 底层组合 `/api/v1/runs`、`/api/v1/approvals` 等（contracts/03 §4 裁决）。
 *
 * 注意：契约里的 WorkspaceSummary **不含最近任务列表**，因此"最近任务"必须
 * 单独取 `/api/v1/runs`（见 useRecentRuns），不要把两件事混成一个端点。
 */
export function useWorkspaceSummary() {
  return useQuery({
    queryKey: queryKeys.workspace.summary(),
    queryFn: () => apiGet<WorkspaceSummary>('/workspace/summary'),
  });
}

/** 工作台"最近任务"：复用唯一执行资源列表端点，按更新时间倒序取前 N 条。 */
export function useRecentRuns(limit = 5) {
  return useQuery({
    queryKey: queryKeys.runs.list({ order: '-updated_at', limit }),
    queryFn: () => fetchList<Run>('/runs', { order: '-updated_at', limit }),
  });
}
