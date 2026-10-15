import { useCallback, useEffect, useRef } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { apiGet } from '@/api/client';
import { queryKeys } from '@/api/query-keys';
import { backoffDelay } from '@/lib/http';
import type { AsyncTask } from '@/types';

/**
 * hooks/useTask.ts —— 长任务跟踪（§10.6.3）
 *
 * 契约（contracts/03 §1.8）：
 *   创建类操作同步返回 202 + 任务引用，客户端通过 GET /api/v1/tasks/{id}
 *   以**退避轮询**跟踪（1s → 2s → 4s → 上限 30s），不得对运行资源无限轮询。
 *
 * 硬性要求：
 *   - 必须区分“排队中 / 执行中 / 已完成 / 失败 / 结果待确认”
 *   - **不得把 202 渲染成成功**：202 只表示“已受理”，终态以 tasks 接口为准
 *   - 任务到达终态后失效相关 Query 缓存，页面自然重取最新数据
 *   - state === 'unknown'（结果待确认）时停止轮询，引导走对账
 */

export type TaskPhase = 'queued' | 'running' | 'succeeded' | 'failed' | 'unknown';

export interface TaskPhaseMeta {
  label: string;
  /** 是否已到终态（不再轮询） */
  terminal: boolean;
  /** 是否为“结果待确认”（需对账，不是失败也不是成功） */
  unknownOutcome: boolean;
  /** 是否可视为“已完成”（仅 queued/running 之外的成功态） */
  succeeded: boolean;
}

export const TASK_PHASE_META: Record<TaskPhase, TaskPhaseMeta> = {
  queued: { label: '排队中', terminal: false, unknownOutcome: false, succeeded: false },
  running: { label: '执行中', terminal: false, unknownOutcome: false, succeeded: false },
  succeeded: { label: '已完成', terminal: true, unknownOutcome: false, succeeded: true },
  failed: { label: '失败', terminal: true, unknownOutcome: false, succeeded: false },
  unknown: { label: '结果待确认', terminal: true, unknownOutcome: true, succeeded: false },
};

/**
 * 安全读取任务阶段元数据（**永不抛错**）。
 *
 * 取值来自网络（GET /api/v1/tasks/{id} 的 state），前端无法在编译期保证它落在
 * 词表内。此前直接 `TASK_PHASE_META[dataState]` 在词表外取值时得到 undefined，
 * 紧随其后的 `meta.terminal` 即抛 TypeError（与缺陷 #19 同类）。
 *
 * 返回 undefined 表示"取值未知"：调用方必须按"未知"处理，**不得视为已完成**
 * （§10.6.3：不得把 202 渲染成成功），也不得停止对账引导。
 */
export function resolveTaskPhase(state: unknown): TaskPhaseMeta | undefined {
  if (typeof state !== 'string' || state.length === 0) return undefined;
  if (!Object.prototype.hasOwnProperty.call(TASK_PHASE_META, state)) return undefined;
  return TASK_PHASE_META[state as TaskPhase];
}

export interface UseTaskResult {
  task: AsyncTask | undefined;
  phase: TaskPhase | undefined;
  phaseMeta: TaskPhaseMeta | undefined;
  isLoading: boolean;
  error: unknown;
  /** 是否已受理（202 语义：已受理 ≠ 成功） */
  accepted: boolean;
  /** 立即重新拉取 */
  refresh: () => void;
}

export interface UseTaskOptions {
  /** 到达终态后的回调（用于页面提示或将 unknown 引导到对账） */
  onTerminal?: (task: AsyncTask) => void;
  /** 是否在终态时失效相关 Query 缓存，默认 true */
  invalidateOnTerminal?: boolean;
}

export function useTask(taskId: string | null | undefined, options: UseTaskOptions = {}): UseTaskResult {
  const queryClient = useQueryClient();
  const { onTerminal, invalidateOnTerminal = true } = options;
  const onTerminalRef = useRef(onTerminal);
  onTerminalRef.current = onTerminal;

  const enabled = Boolean(taskId);

  const query = useQuery<AsyncTask>({
    queryKey: queryKeys.tasks.detail(taskId ?? ''),
    queryFn: () => apiGet<AsyncTask>(`/tasks/${encodeURIComponent(String(taskId))}`),
    enabled,
    refetchInterval: (q) => {
      const data = q.state.data;
      if (!data) return 1000;
      // 终态停止轮询；unknown 亦停止轮询（转为显式对账业务状态）
      if (data.state === 'succeeded' || data.state === 'failed' || data.state === 'unknown') return false;
      return backoffDelay(q.state.dataUpdateCount, 30_000);
    },
    refetchIntervalInBackground: false,
  });

  const data = query.data;
  const dataState = data?.state;

  useEffect(() => {
    if (!data || !dataState) return;
    const meta = resolveTaskPhase(dataState);
    if (!meta || !meta.terminal) return;
    if (invalidateOnTerminal) {
      void queryClient.invalidateQueries({ queryKey: ['runs'] });
      void queryClient.invalidateQueries({ queryKey: ['ontologies'] });
      void queryClient.invalidateQueries({ queryKey: ['agents'] });
      void queryClient.invalidateQueries({ queryKey: ['evaluations'] });
      void queryClient.invalidateQueries({ queryKey: ['releases'] });
      void queryClient.invalidateQueries({ queryKey: ['approvals'] });
    }
    onTerminalRef.current?.(data);
  }, [data, dataState, invalidateOnTerminal, queryClient]);

  const refresh = useCallback(() => {
    void query.refetch();
  }, [query]);

  return {
    task: data,
    phase: dataState,
    phaseMeta: resolveTaskPhase(dataState),
    isLoading: query.isLoading,
    error: query.error,
    accepted: Boolean(data),
    refresh,
  };
}
