import { useQuery } from '@tanstack/react-query';
import { fetchList } from '@/api/client';
import { queryKeys } from '@/api/query-keys';
import type { BusinessApp } from '@/types';

/** 业务应用（§4.5）。契约：`/api/v1/apps` —— BFF 聚合端点（contracts/03 §4 裁决）。 */
export function useBusinessApps() {
  return useQuery({
    queryKey: queryKeys.apps.list(),
    queryFn: () => fetchList<BusinessApp>('/apps', { limit: 50 }),
  });
}
