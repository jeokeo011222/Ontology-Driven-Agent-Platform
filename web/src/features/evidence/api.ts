import { useQuery } from '@tanstack/react-query';
import { fetchList } from '@/api/client';
import { queryKeys, type AuditListFilters } from '@/api/query-keys';
import type { AuditEvent } from '@/types';

/**
 * 证据视图（§4.6 证据包标签页 / §8.7 审计、证据与数据保留）。
 *
 * 证据条目从**已登记的审计端点**投影得到（`GET /api/v1/audit-events`），
 * 前端不为页面自行创建重复业务端点（§11.1 裁决）。
 * 证据包的哈希链、访问权限与保留期限是三个不同概念（§8.7）：
 *   哈希只能帮助检测内容变化，不能证明内容在生成时即为真实，
 *   也不能防止拥有足够底层权限的攻击者改写全部数据。
 */
export function useEvidenceEvents(filters: AuditListFilters) {
  return useQuery({
    queryKey: [...queryKeys.audit.list(filters), 'evidence'] as const,
    queryFn: () =>
      fetchList<AuditEvent>('/audit-events', {
        resource_type: filters.resource_type,
        limit: filters.limit ?? 10,
        cursor: filters.cursor ?? undefined,
      }),
  });
}
