import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiGet, apiPost, fetchList, listFilterQuery, type ListQueryFilters } from '@/api/client';
import { queryKeys } from '@/api/query-keys';
import { newIdempotencyKey } from '@/lib/http';
import type { EvaluationRun, Release } from '@/types';

/**
 * 评估中心与发布管理（第十五篇 / §11.1）。
 * 契约：`/api/v1/evaluations`、`/api/v1/evaluations/{id}`（contracts/03 §2/§4 裁决）
 *       `/api/v1/releases`、`/api/v1/releases/{id}/promote`、`/api/v1/releases/{id}/rollback`（第十五篇 §16.4）
 */

export function useEvaluations(filters: ListQueryFilters = {}) {
  return useQuery({
    queryKey: [...queryKeys.evaluation.list(), filters],
    queryFn: () => fetchList<EvaluationRun>('/evaluations', listFilterQuery(filters)),
  });
}

export function useEvaluation(id: string | undefined) {
  return useQuery({
    queryKey: queryKeys.evaluation.detail(id ?? ''),
    queryFn: () => apiGet<EvaluationRun>(`/evaluations/${encodeURIComponent(String(id))}`),
    enabled: Boolean(id),
  });
}

export function useReleases(filters: ListQueryFilters = {}) {
  return useQuery({
    queryKey: [...queryKeys.releases.list(), filters],
    queryFn: () => fetchList<Release>('/releases', listFilterQuery(filters)),
  });
}

/** 发布推进：必须经过质量门禁（gate_state=passed）才能提升到生产 */
export function usePromoteRelease() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiPost<Release>(`/releases/${encodeURIComponent(id)}/promote`, undefined, {
        idempotencyKey: newIdempotencyKey(`release-promote-${id}`),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.releases.list() });
    },
  });
}

/** 回滚：恢复到明确的已发布版本，并评估依赖该版本的任务是否需要重跑 */
export function useRollbackRelease() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (variables: { id: string; targetVersion: string }) =>
      apiPost<Release>(
        `/releases/${encodeURIComponent(variables.id)}/rollback`,
        { target_version: variables.targetVersion },
        { idempotencyKey: newIdempotencyKey(`release-rollback-${variables.id}`) },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.releases.list() });
    },
  });
}
