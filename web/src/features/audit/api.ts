import { useQuery } from '@tanstack/react-query';
import { fetchList } from '@/api/client';
import { queryKeys, type AuditListFilters } from '@/api/query-keys';
import type { AuditEvent } from '@/types';

/**
 * 审计中心（第十一篇）。契约：`GET /api/v1/audit-events`（contracts/03 §2）。
 * 过滤用查询参数：`?resource_type=&outcome=&actor_id=`，cursor 分页。
 */
export function useAuditEvents(filters: AuditListFilters) {
  return useQuery({
    queryKey: queryKeys.audit.list(filters),
    queryFn: () =>
      fetchList<AuditEvent>('/audit-events', {
        resource_type: filters.resource_type,
        outcome: filters.outcome,
        actor_id: filters.actor_id,
        limit: filters.limit ?? 10,
        cursor: filters.cursor ?? undefined,
      }),
  });
}
