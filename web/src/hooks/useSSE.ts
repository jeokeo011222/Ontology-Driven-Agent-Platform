import { useCallback, useEffect, useRef, useState } from 'react';
import { streamUrl } from '@/api/client';
import { backoffDelay } from '@/lib/http';

/**
 * hooks/useSSE.ts —— 统一实时通道（§10.6）
 *
 * 契约：`GET /api/v1/stream`（contracts/03 §1.9）
 *   - 鉴权：EventSource 无法自定义请求头 → 会话 Cookie（HttpOnly）由浏览器携带；
 *     禁止在 URL query 中传长期令牌。
 *   - 断线重连：浏览器自动携带 `Last-Event-ID` 续传（本 hook 记录并展示最近事件 ID）。
 *   - 事件分级：CRITICAL / IMPORTANT / NORMAL；CRITICAL 与 IMPORTANT 保证投递，
 *     NORMAL 在背压时可丢弃（由随后的快照事件兜底）。
 *   - 降级：SSE 不可用（代理不支持 / 连接持续失败）时降级为**指数退避 + 抖动**轮询
 *     （1s → 2s → 4s → 上限 30s），UI 必须明确提示“实时通道降级”。
 */

export type SSELevel = 'CRITICAL' | 'IMPORTANT' | 'NORMAL';

export interface SSEEvent {
  /** 事件 ID（用于 Last-Event-ID 续传展示） */
  id: string | null;
  /** 事件类型名 */
  type: string;
  level: SSELevel;
  data: unknown;
  receivedAt: number;
}

export interface UseSSEOptions {
  enabled?: boolean;
  /** 降级为轮询时调用：应立即重取一次关键数据 */
  onFallbackPoll?: () => void;
  /** 收到事件时回调 */
  onEvent?: (event: SSEEvent) => void;
  /** 内存中保留的最大事件数（默认 50） */
  maxEvents?: number;
}

export interface UseSSEResult {
  /** 实时通道已连接 */
  connected: boolean;
  /** 已降级为轮询（UI 必须提示“实时通道降级”） */
  degraded: boolean;
  lastEventId: string | null;
  events: SSEEvent[];
  error: string | null;
  failureCount: number;
  /** 手动重建连接 */
  reconnect: () => void;
}

/** 订阅的服务端命名事件（未命名的默认事件走 onmessage） */
const NAMED_EVENTS = [
  'snapshot',
  'run.updated',
  'approval.updated',
  'reconciliation.updated',
  'erp.operation.updated',
  'security.alert',
] as const;

/** 事件名 → 默认级别（服务端未显式给 level 时的兜底） */
function inferLevel(type: string): SSELevel {
  if (
    type.startsWith('approval') ||
    type.startsWith('reconciliation') ||
    type.startsWith('erp.operation') ||
    type.startsWith('security')
  ) {
    return 'CRITICAL';
  }
  if (type.startsWith('run')) return 'IMPORTANT';
  return 'NORMAL';
}

function parseEvent(raw: MessageEvent, type: string): SSEEvent {
  let data: unknown = raw.data;
  let level: SSELevel | undefined;
  let eventType = type;

  if (typeof raw.data === 'string' && raw.data.length > 0) {
    try {
      const parsed = JSON.parse(raw.data) as { level?: string; type?: string; payload?: unknown };
      data = parsed.payload ?? parsed;
      if (parsed.level === 'CRITICAL' || parsed.level === 'IMPORTANT' || parsed.level === 'NORMAL') {
        level = parsed.level;
      }
      if (parsed.type) eventType = parsed.type;
    } catch {
      // 非 JSON 事件（如心跳注释）保持原文
    }
  }

  return {
    id: raw.lastEventId || null,
    type: eventType,
    level: level ?? inferLevel(eventType),
    data,
    receivedAt: Date.now(),
  };
}

export function useSSE(options: UseSSEOptions = {}): UseSSEResult {
  const { enabled = true, maxEvents = 50 } = options;

  const onFallbackPollRef = useRef(options.onFallbackPoll);
  onFallbackPollRef.current = options.onFallbackPoll;
  const onEventRef = useRef(options.onEvent);
  onEventRef.current = options.onEvent;

  const [connected, setConnected] = useState(false);
  const [degraded, setDegraded] = useState(false);
  const [events, setEvents] = useState<SSEEvent[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [failureCount, setFailureCount] = useState(0);
  const [lastEventId, setLastEventId] = useState<string | null>(null);
  const [epoch, setEpoch] = useState(0);

  const reconnect = useCallback(() => {
    setEpoch((v) => v + 1);
  }, []);

  useEffect(() => {
    if (!enabled) {
      setConnected(false);
      return;
    }

    let disposed = false;
    let es: EventSource | null = null;
    let pollTimer: number | null = null;
    let reviveTimer: number | null = null;
    let failures = 0;
    let pollAttempt = 0;

    const handle = (raw: MessageEvent, type: string) => {
      if (disposed) return;
      const event = parseEvent(raw, type);
      if (event.id) setLastEventId(event.id);
      setEvents((prev) => {
        const next = [...prev, event];
        return next.length > maxEvents ? next.slice(next.length - maxEvents) : next;
      });
      onEventRef.current?.(event);
    };

    const stopPolling = () => {
      if (pollTimer !== null) {
        window.clearTimeout(pollTimer);
        pollTimer = null;
      }
      if (reviveTimer !== null) {
        window.clearTimeout(reviveTimer);
        reviveTimer = null;
      }
      pollAttempt = 0;
    };

    const startPolling = () => {
      if (disposed || pollTimer !== null) return;
      setDegraded(true);
      const tick = () => {
        if (disposed) return;
        onFallbackPollRef.current?.();
        pollAttempt += 1;
        pollTimer = window.setTimeout(tick, backoffDelay(pollAttempt, 30_000));
      };
      onFallbackPollRef.current?.();
      pollTimer = window.setTimeout(tick, backoffDelay(1, 30_000));
      // 周期性尝试恢复实时通道（代理恢复后自动回到 SSE）
      reviveTimer = window.setTimeout(() => {
        if (disposed) return;
        stopPolling();
        connect();
      }, 60_000);
    };

    const connect = () => {
      if (disposed) return;
      try {
        es = new EventSource(streamUrl(), { withCredentials: true });
      } catch {
        setError('浏览器不支持实时通道');
        startPolling();
        return;
      }

      es.onopen = () => {
        if (disposed) return;
        failures = 0;
        setFailureCount(0);
        setConnected(true);
        setDegraded(false);
        setError(null);
      };

      es.onerror = () => {
        if (disposed) return;
        setConnected(false);
        failures += 1;
        setFailureCount(failures);
        setError('实时通道连接异常');
        // 连续失败达阈值后降级为轮询；此前的短暂断连由浏览器自动重连
        // （自动携带 Last-Event-ID，服务端按有界回放缓冲区续传）
        if (failures >= 3) {
          es?.close();
          es = null;
          startPolling();
        }
      };

      es.onmessage = (event) => handle(event, 'message');
      for (const name of NAMED_EVENTS) {
        es.addEventListener(name, (event) => handle(event as MessageEvent, name));
      }
    };

    connect();

    return () => {
      disposed = true;
      es?.close();
      es = null;
      stopPolling();
    };
  }, [enabled, maxEvents, epoch]);

  return { connected, degraded, lastEventId, events, error, failureCount, reconnect };
}
