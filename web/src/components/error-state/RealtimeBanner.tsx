import { Button } from '@/components/ui';
import { Icon } from '@/components/icon';

export interface RealtimeBannerProps {
  /** 已降级为轮询 */
  degraded: boolean;
  connected: boolean;
  error: string | null;
  failureCount: number;
  lastEventId: string | null;
  onReconnect?: () => void;
}

/**
 * 实时通道状态提示（§10.6.1）。
 * 要求：**降级状态必须在 UI 明确提示“实时通道降级”**，
 * 因为此时审批结果与对账转正可能有延迟，用户不能误以为实时。
 */
export function RealtimeBanner({
  degraded,
  connected,
  error,
  failureCount,
  lastEventId,
  onReconnect,
}: RealtimeBannerProps) {
  if (degraded) {
    return (
      <div className="banner banner--warning" role="status" aria-live="polite">
        <span className="banner__icon" aria-hidden="true">
          <Icon name="alertTriangle" size={16} />
        </span>
        <span>
          <strong>实时通道降级</strong>：SSE 不可用（连续失败 {failureCount} 次），已切换为退避轮询
          （1s → 2s → 4s → 上限 30s）。审批结果与对账状态可能存在延迟。
          {lastEventId ? (
            <>
              {' '}
              最近事件 ID：<span className="copy-id">{lastEventId}</span>。
            </>
          ) : null}
        </span>
        {onReconnect ? (
          <Button variant="secondary" size="sm" onClick={onReconnect} style={{ marginLeft: 'auto' }} icon="refresh">
            重试实时通道
          </Button>
        ) : null}
      </div>
    );
  }

  if (error && !connected) {
    return (
      <div className="banner banner--info" role="status" aria-live="polite">
        <span className="banner__icon" aria-hidden="true">
          <Icon name="rotateCw" size={16} />
        </span>
        <span>实时通道连接异常，正在自动重连（将携带 Last-Event-ID 续传）。</span>
      </div>
    );
  }

  return null;
}
