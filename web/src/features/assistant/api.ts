import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  apiDelete,
  apiGet,
  apiPatch,
  apiPost,
  fetchList,
} from '@/api/client';
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

// =============================================================================
// 聊天会话（迁移 000046：多轮对话持久化）
// =============================================================================
//
// 契约（与后端 internal/api/store/chat.go 严格对应）：
//   POST   /api/v1/chat/sessions                       → CreateChatSession
//   GET    /api/v1/chat/sessions                       → ListChatSessions
//   GET    /api/v1/chat/sessions/{id}                  → GetChatSession
//   PATCH  /api/v1/chat/sessions/{id}                  → UpdateChatSession
//   DELETE /api/v1/chat/sessions/{id}                  → DeleteChatSession
//   POST   /api/v1/chat/sessions/{id}/archive          → ArchiveChatSession
//   POST   /api/v1/chat/sessions/{id}/messages         → AddChatMessage
//   GET    /api/v1/chat/sessions/{id}/messages         → ListChatMessages

/** app.chat_sessions 行。 */
export interface ChatSession {
  id: string;
  title: string;
  ontology_id?: string;
  tenant_id: string;
  subject_id: string;
  created_at: string;
  updated_at: string;
  archived_at?: string;
  status: 'ACTIVE' | 'ARCHIVED';
}

/** app.chat_messages 行。 */
export interface ChatMessage {
  id: string;
  session_id: string;
  role: 'user' | 'assistant' | 'system';
  content: string;
  run_id?: string;
  created_at: string;
}

/** POST /chat/sessions 请求体。subject_id 不在前端请求体内——后端从身份上下文提取。 */
export interface CreateChatSessionInput {
  title: string;
  ontology_id?: string;
}

/** PATCH /chat/sessions/{id} 请求体。目前只开放 title 修改。 */
export interface UpdateChatSessionInput {
  title: string;
}

/** POST /chat/sessions/{id}/messages 请求体。 */
export interface AddChatMessageInput {
  role: 'user' | 'assistant' | 'system';
  content: string;
  run_id?: string;
}

// --- 查询 hooks ---

/** GET /chat/sessions（当前用户的会话，默认只返回 ACTIVE；status='ARCHIVED' 时返回归档）。 */
export function useChatSessions(status?: string) {
  return useQuery({
    queryKey: queryKeys.chat.sessions(status),
    queryFn: () => fetchList<ChatSession>('/chat/sessions', { status, limit: 10 }),
  });
}

/** GET /chat/sessions/{id}（详情）。 */
export function useChatSession(id: string | null) {
  return useQuery({
    queryKey: queryKeys.chat.session(id ?? ''),
    enabled: Boolean(id),
    queryFn: () => apiGet<ChatSession>(`/chat/sessions/${encodeURIComponent(String(id))}`),
  });
}

/** GET /chat/sessions/{id}/messages（created_at ASC cursor 分页，默认 50/页覆盖典型多轮对话）。 */
export function useChatMessages(sessionId: string | null) {
  return useQuery({
    queryKey: queryKeys.chat.messages(sessionId ?? ''),
    enabled: Boolean(sessionId),
    queryFn: () => fetchList<ChatMessage>(`/chat/sessions/${encodeURIComponent(String(sessionId))}/messages`, { limit: 50 }),
  });
}

// --- 写操作 mutation hooks ---

export function useCreateChatSession() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateChatSessionInput) =>
      apiPost<ChatSession>('/chat/sessions', input, {
        idempotencyKey: newIdempotencyKey('chat-session-create'),
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.chat.sessions() });
    },
  });
}

export function useUpdateChatSession() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, title }: { id: string; title: string }) =>
      apiPatch<ChatSession>(`/chat/sessions/${encodeURIComponent(id)}`, { title }, {
        idempotencyKey: newIdempotencyKey(`chat-session-update-${id}`),
      }),
    onSuccess: (_data, vars) => {
      void qc.invalidateQueries({ queryKey: queryKeys.chat.sessions() });
      void qc.invalidateQueries({ queryKey: queryKeys.chat.session(vars.id) });
    },
  });
}

export function useArchiveChatSession() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiPost<ChatSession>(`/chat/sessions/${encodeURIComponent(id)}/archive`, undefined, {
        idempotencyKey: newIdempotencyKey(`chat-session-archive-${id}`),
      }),
    onSuccess: (_data, id) => {
      void qc.invalidateQueries({ queryKey: queryKeys.chat.sessions() });
      void qc.invalidateQueries({ queryKey: queryKeys.chat.session(id) });
    },
  });
}

export function useDeleteChatSession() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiDelete<null>(`/chat/sessions/${encodeURIComponent(id)}`, {
        idempotencyKey: newIdempotencyKey(`chat-session-delete-${id}`),
      }),
    onSuccess: (_data, id) => {
      void qc.invalidateQueries({ queryKey: queryKeys.chat.sessions() });
      void qc.invalidateQueries({ queryKey: queryKeys.chat.session(id) });
      void qc.invalidateQueries({ queryKey: queryKeys.chat.messages(id) });
    },
  });
}

export function useAddChatMessage() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ sessionId, input }: { sessionId: string; input: AddChatMessageInput }) =>
      apiPost<null>(`/chat/sessions/${encodeURIComponent(sessionId)}/messages`, input, {
        idempotencyKey: newIdempotencyKey(`chat-msg-add-${sessionId}-${Date.now()}`),
      }),
    onSuccess: (_data, vars) => {
      void qc.invalidateQueries({ queryKey: queryKeys.chat.messages(vars.sessionId) });
    },
  });
}
