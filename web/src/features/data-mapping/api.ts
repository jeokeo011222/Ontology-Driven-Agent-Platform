import { useQuery } from '@tanstack/react-query';
import { fetchList } from '@/api/client';
import { queryKeys } from '@/api/query-keys';
import type { DataSource } from '@/types';

/**
 * `/api/v1/data-sources` **既未登记进 contracts/openapi.yaml，后端也没有该路由**。
 *
 * 直接发请求会拿到 404，而前端把 404 统一渲染成"资源不存在或不可见"——
 * 一个会误导运维（像是权限问题）的错误。所以这里显式关闭请求，
 * 由页面给出"端点尚未登记"的明确状态。
 *
 * 解除条件（两个都要满足）：
 *   1. 端点登记进 contracts/openapi.yaml 并通过 tools/contract-lint；
 *   2. 后端实现对应 handler 与存储查询。
 * 之后把下面的开关改为 true 即可，页面无需其他改动。
 */
export const DATA_SOURCES_ENDPOINT_AVAILABLE = false;

/**
 * 数据接入与语义映射（§6.4）。权威来源第十二篇。
 */
export function useDataSources() {
  return useQuery({
    queryKey: queryKeys.dataSources.list(),
    queryFn: () => fetchList<DataSource>('/data-sources', { limit: 50 }),
    enabled: DATA_SOURCES_ENDPOINT_AVAILABLE,
  });
}
