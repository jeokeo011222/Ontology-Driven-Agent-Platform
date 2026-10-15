import { useMutation, useQuery } from '@tanstack/react-query';
import { apiPost, fetchList } from '@/api/client';
import { queryKeys } from '@/api/query-keys';
import { newIdempotencyKey } from '@/lib/http';
import type { OntologySummary, Run } from '@/types';

/**
 * AI 业务助手（§4.2）。契约：`POST /api/v1/runs`（contracts/03 §2）。
 *
 * **本体驱动**：助手不是"通用聊天机器人"，而是"按某个本体的规则审查业务目标"。
 * 因此请求体必须携带 `ontology_id`——服务端据此取该本体的形状约束（SHACL）执行
 * 真实校验，再结合模型理解给出结论。没有本体就没有可执行的规则，助手也就无从判
 * 定"合规/不合规"。
 *
 * 计划的权威内容由 Run 资源承载（`plan` / `review_conclusion` 随 POST 响应返回，
 * 详细过程见 `/runs/{id}/steps`）；前端不自行生成或推断计划内容。
 */

export interface CreateRunInput {
  /** 本次审查依据的本体 id（必填）。 */
  ontology_id: string;
  objective: string;
  business_type?: string;
}

export function useCreateRun() {
  return useMutation({
    mutationFn: (input: CreateRunInput) =>
      apiPost<Run>(
        '/runs',
        {
          ontology_id: input.ontology_id,
          objective: input.objective,
          business_type: input.business_type,
        },
        { idempotencyKey: newIdempotencyKey('run-create') },
      ),
  });
}

/**
 * 可用本体清单（`GET /api/v1/workspace/ontologies`）。
 *
 * 口径与服务端一致：**管理端设计了多少本体，这里就有多少**——包含尚未发布的
 * DRAFT 版本所属的本体。取 limit=200（契约上限）而不是默认 10：需求明确要求
 * "管理端有多少就显示多少"，若沿用默认页大小，第 11 个之后的本体会静默消失，
 * 用户会以为系统丢了数据。
 */
export function useWorkspaceOntologies() {
  return useQuery({
    queryKey: queryKeys.workspace.ontologies(),
    queryFn: () => fetchList<OntologySummary>('/workspace/ontologies', { limit: 200 }),
  });
}
