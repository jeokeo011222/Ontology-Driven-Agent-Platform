import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiGet, apiPost, fetchList } from '@/api/client';
import { queryKeys, type ApprovalListFilters } from '@/api/query-keys';
import { newIdempotencyKey } from '@/lib/http';
import type { ApprovalDecisionInput, ApprovalDetail, ApprovalSummary } from '@/types';

/** 审批中心（§4.4）。契约：`/api/v1/approvals`、`/api/v1/approvals/{id}`（contracts/03 §2） */

export function useApprovals(filters: ApprovalListFilters) {
  return useQuery({
    queryKey: queryKeys.approvals.list(filters),
    queryFn: () =>
      fetchList<ApprovalSummary>('/approvals', {
        status: filters.status,
        limit: filters.limit ?? 20,
        cursor: filters.cursor ?? undefined,
      }),
  });
}

export function useApproval(id: string | undefined) {
  return useQuery({
    queryKey: queryKeys.approvals.detail(id ?? ''),
    queryFn: () => apiGet<ApprovalDetail>(`/approvals/${encodeURIComponent(String(id))}`),
    enabled: Boolean(id),
  });
}

export interface DecisionVariables extends ApprovalDecisionInput {
  /**
   * 乐观并发（§10.6.4）：审批提交强制 If-Match，
   * 值为审批摘要的不可变版本标识（parameter_version 或 ETag）。
   */
  ifMatch: string;
}

/**
 * 提交审批决策。
 * 契约：`POST /api/v1/approvals/{id}/decision`（子资源 POST，contracts/03 §1.3）。
 * 必须携带 Idempotency-Key：幂等最终由服务端保证，前端禁用按钮只是体验优化。
 */
export function useApprovalDecision(approvalId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (variables: DecisionVariables) =>
      apiPost<{ approval_id: string; status: string; erp_operation_status?: string }>(
        `/approvals/${encodeURIComponent(approvalId)}/decision`,
        {
          decision: variables.decision,
          comment: variables.comment,
          confirm_scope: variables.confirm_scope,
        },
        {
          idempotencyKey: newIdempotencyKey(`approval-${approvalId}`),
          ifMatch: variables.ifMatch,
        },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.approvals.all() });
      // 审批通过后 Run 与外部操作状态会变化，一并失效
      void queryClient.invalidateQueries({ queryKey: ['runs'] });
    },
  });
}
