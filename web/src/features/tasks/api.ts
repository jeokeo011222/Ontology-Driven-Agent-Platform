import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiGet, apiPost, fetchList } from '@/api/client';
import { queryKeys, type RunListFilters } from '@/api/query-keys';
import { newIdempotencyKey } from '@/lib/http';
import type { ErpOperation, Run, RunStep } from '@/types';

/**
 * 我的任务（§4.3）。契约：`/api/v1/runs`（contracts/03 §2，唯一执行资源）。
 * 过滤用查询参数 `?status=&business_type=&q=&order=`，cursor 分页。
 */

export function useRuns(filters: RunListFilters) {
  return useQuery({
    queryKey: queryKeys.runs.list(filters),
    queryFn: () =>
      fetchList<Run>('/runs', {
        status: filters.status,
        business_type: filters.business_type,
        q: filters.q,
        order: filters.order,
        limit: filters.limit ?? 20,
        cursor: filters.cursor ?? undefined,
      }),
  });
}

/**
 * `GET /api/v1/runs/{id}` 的响应：Run + 步骤 + 版本锁依赖。
 *
 * 权威：contracts/openapi.yaml 该端点（`{run, steps, dependencies}`）。
 * 此前这里声明的返回类型是裸 `Run`，页面直接读 `data.objective`／`data.status`
 * ——两个字段都不在响应顶层，于是任务详情页的标题与状态永远是空的（潜伏的哑弹）。
 */
export interface RunDetail {
  run: Run;
  steps: RunStep[];
  dependencies: unknown[];
}

export function useRun(runId: string | undefined) {
  return useQuery({
    queryKey: queryKeys.runs.detail(runId ?? ''),
    queryFn: async () => {
      const detail = await apiGet<RunDetail>(`/runs/${encodeURIComponent(String(runId))}`);
      return detail.run;
    },
    enabled: Boolean(runId),
  });
}

export function useRunSteps(runId: string | undefined) {
  return useQuery({
    queryKey: queryKeys.runs.steps(runId ?? ''),
    queryFn: () => fetchList<RunStep>(`/runs/${encodeURIComponent(String(runId))}/steps`, { limit: 200 }),
    enabled: Boolean(runId),
  });
}

export function useRunErpOperations(runId: string | undefined) {
  return useQuery({
    queryKey: queryKeys.runs.erpOperations(runId ?? ''),
    queryFn: () => fetchList<ErpOperation>(`/runs/${encodeURIComponent(String(runId))}/erp-operations`, { limit: 50 }),
    enabled: Boolean(runId),
  });
}

/**
 * `POST /api/v1/runs/{id}/cancel` 的 **202** 响应。
 *
 * 权威：contracts/openapi.yaml 该端点 202 响应（`{id, status}`），实现见
 *   `Handler.CancelRun`（runs.go，`http.StatusAccepted`）。
 *
 * ⚠️ 此前这里声明的是内联 `{ accepted: boolean }` —— 一个**既不来自契约、也不来自
 *   实现**的类型：服务端根本不会返回 `accepted` 字段，因此任何读它的调用方都会拿到
 *   `undefined`（falsy），把"取消已受理"渲染成"未受理"。当前调用方恰好没读返回值，
 *   所以它一直是颗潜伏的哑弹，而非立即可见的故障。
 */
export interface CancelRunAccepted {
  id: string;
  /** 取消受理后的 Run 状态；契约语义应为 CANCELLING（contracts/01 §1）。 */
  status: string;
}

/** 请求取消（进入 CANCELLING，等待在途外部操作收敛，contracts/01 §1） */
export function useCancelRun(runId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (reason: string) =>
      apiPost<CancelRunAccepted>(
        `/runs/${encodeURIComponent(runId)}/cancel`,
        { reason },
        { idempotencyKey: newIdempotencyKey(`cancel-${runId}`) },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.runs.all() });
    },
  });
}

/**
 * `POST /api/v1/runs/{id}/reconcile` 的 **202** 任务引用。
 *
 * 权威：contracts/openapi.yaml 该端点 202 响应
 *   （`{task_id, kind, status, status_url}`），实现见 `Handler.ReconcileRun`。
 *
 * ⚠️ 此前声明的 `{ accepted: boolean; reconciliation_id?: string }` 两个字段服务端
 *   都不返回；正确的引用字段名是 `task_id` / `status_url`。
 */
export interface ReconcileRunAccepted {
  task_id: string;
  kind: string;
  status: string;
  /** 任务状态查询地址（形如 `/api/v1/tasks/{task_id}`）。 */
  status_url: string;
}

/** Run 级批量对账编排（contracts/03 §2） */
export function useReconcileRun(runId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () =>
      apiPost<ReconcileRunAccepted>(
        `/runs/${encodeURIComponent(runId)}/reconcile`,
        undefined,
        { idempotencyKey: newIdempotencyKey(`reconcile-${runId}`) },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.runs.all() });
    },
  });
}
