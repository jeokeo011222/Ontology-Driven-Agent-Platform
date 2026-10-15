import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiDelete, apiPatch, apiPost, fetchList, listFilterQuery, type ListQueryFilters } from '@/api/client';
import { queryKeys } from '@/api/query-keys';
import { newIdempotencyKey } from '@/lib/http';
import type { SkillInfo, ToolInfo } from '@/types';

/**
 * Skill Registry（技能作为一等 registry 资源）。
 * 契约：`/api/v1/skills`、`/api/v1/skills/{id}`、`/api/v1/skills/{id}/publish`。
 * Agent 经 dependency_manifest.skills 依赖 skill_key；服务端 fail-closed：
 * 技能不存在 / 没有 PUBLISHED 版本 → 404。
 */
export function useSkills(filters: ListQueryFilters = {}) {
  return useQuery({
    queryKey: [...queryKeys.skills.list(), filters],
    queryFn: () => fetchList<SkillInfo>('/skills', listFilterQuery(filters)),
  });
}

/**
 * 「绑定工具」多选的真实数据源：GET /api/v1/tools 中 status=ACTIVE 的条目
 * （200 行/页，覆盖选项列表场景）。禁止硬编码工具清单。
 */
export function useActiveToolOptions() {
  return useQuery({
    queryKey: [...queryKeys.tools.list(), { status: 'ACTIVE', purpose: 'skill-binding', limit: 200 }],
    queryFn: () => fetchList<ToolInfo>('/tools', listFilterQuery({ status: 'ACTIVE' }, 200)),
  });
}

/**
 * 「绑定技能」多选的真实数据源（AgentsPage 用）：GET /api/v1/skills 中
 * 最新版本已发布的技能（200 行/页）。只有 PUBLISHED 版本才能被 Agent 依赖，
 * 未发布技能会在创建/编辑 Agent 时被服务端 404 拒绝。
 */
export function useSkillOptions() {
  return useQuery({
    queryKey: [...queryKeys.skills.list(), { purpose: 'agent-binding', limit: 200 }],
    queryFn: () =>
      fetchList<SkillInfo>('/skills', listFilterQuery({}, 200)).then((res) => ({
        ...res,
        items: res.items.filter((s) => s.latest_version_status === 'PUBLISHED'),
      })),
  });
}

/**
 * `POST /api/v1/skills` 的请求体（含首个 DRAFT 版本）。
 *
 * `input_schema` / `output_schema` 必须是合法 JSON（服务端 `json.Valid` 强校验，
 * 缺省被拒绝为 INVALID_ARGUMENT）：调用方必须传入至少 `{}`。
 * `tool_bindings` 的每个 tool_key 必须指向 registry.tools 中 ACTIVE 的工具，
 * 否则服务端 404/409（拒绝悬空引用）。
 */
export interface CreateSkillBody {
  skill_key: string;
  name: string;
  domain?: string;
  description?: string;
  version?: string;
  prompt_fragment?: string;
  tool_bindings?: string[];
  input_schema: unknown;
  output_schema: unknown;
}

/** `PATCH /api/v1/skills/{id}` 的请求体（PATCH 语义：字段缺省不改动；skill_key 不可变）。 */
export interface UpdateSkillBody {
  name?: string;
  description?: string;
  domain?: string;
  status?: 'ACTIVE' | 'INACTIVE';
}

function invalidate(qc: ReturnType<typeof useQueryClient>) {
  void qc.invalidateQueries({ queryKey: queryKeys.skills.list() });
}

export function useCreateSkill() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateSkillBody) =>
      apiPost<SkillInfo>('/skills', body, {
        idempotencyKey: newIdempotencyKey(`skill-create-${body.skill_key}`),
      }),
    onSuccess: () => invalidate(qc),
  });
}

export function useUpdateSkill() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: UpdateSkillBody }) =>
      apiPatch<SkillInfo>(`/skills/${encodeURIComponent(id)}`, body, {
        idempotencyKey: newIdempotencyKey(`skill-update-${id}`),
      }),
    onSuccess: () => invalidate(qc),
  });
}

/** 软删除（status → DELETED）：保留审计链；内置技能一律 409。 */
export function useDeleteSkill() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiDelete<{ id: string; status: string }>(`/skills/${encodeURIComponent(id)}`, {
        idempotencyKey: newIdempotencyKey(`skill-delete-${id}`),
      }),
    onSuccess: () => invalidate(qc),
  });
}

/**
 * `POST /api/v1/skills/{id}/versions` 的请求体（追加 DRAFT 版本；发布仍走 /publish）。
 * `input_schema` / `output_schema` 必须是合法 JSON（服务端 `json.Valid` 强校验）。
 * `tool_bindings` 每个 tool_key 必须指向 ACTIVE 工具，否则服务端 404/409。
 */
export interface CreateSkillVersionBody {
  version: string;
  prompt_fragment?: string;
  tool_bindings?: string[];
  input_schema: unknown;
  output_schema: unknown;
}

export function useCreateSkillVersion() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: CreateSkillVersionBody }) =>
      apiPost<{ skill_id: string; version_id: string; version: string; status: string }>(
        `/skills/${encodeURIComponent(id)}/versions`,
        body,
        { idempotencyKey: newIdempotencyKey(`skill-version-${id}-${body.version}`) },
      ),
    onSuccess: () => invalidate(qc),
  });
}

/**
 * 发布最新版本（DRAFT → PUBLISHED，同步 200）。
 * 权威：contracts/openapi.yaml `/api/v1/skills/{id}/publish` 的 200 响应
 *   （`{skill_id, version_id, status}`）。缺失/非法状态一律 409（fail-closed）。
 */
export interface SkillPublishResult {
  skill_id: string;
  version_id: string;
  status: string;
}

export function usePublishSkill() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiPost<SkillPublishResult>(`/skills/${encodeURIComponent(id)}/publish`, undefined, {
        idempotencyKey: newIdempotencyKey(`skill-publish-${id}`),
      }),
    onSuccess: () => invalidate(qc),
  });
}
