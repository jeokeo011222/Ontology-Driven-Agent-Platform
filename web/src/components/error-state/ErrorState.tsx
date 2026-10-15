import type { ReactNode } from 'react';
import { Button } from '@/components/ui';
import { describeError } from '@/api/errors';
import { StateView } from './StateView';

export interface ErrorStateProps {
  error: unknown;
  /** 重试（仅在契约 retryable 或数据获取失败时提供） */
  onRetry?: () => void;
  /** 额外操作（如“重新加载最新数据”“申请权限”“前往对账”） */
  extraActions?: ReactNode;
  compact?: boolean;
}

/**
 * 统一错误态。
 * §9.1 四种操作状态不能混用同一种失败提示：
 *   - 权限不足（forbidden）
 *   - 数据冲突（conflict，§10.6.4 数据已被他人修改）
 *   - 结果未知（unknownOutcome，走对账，绝不写成失败）
 *   - 一般失败（可重试 / 不可重试）
 */
export function ErrorState({ error, onRetry, extraActions, compact }: ErrorStateProps) {
  const p = describeError(error);

  const diagnostic = (
    <span className="text-xs muted">
      错误码 <span className="mono">{p.code}</span>
      {p.baseCode && p.baseCode !== p.code ? (
        <>
          {' '}
          · 基础码 <span className="mono">{p.baseCode}</span>
        </>
      ) : null}
      {p.requestId ? (
        <>
          {' '}
          · 请求 ID <span className="copy-id">{p.requestId}</span>
        </>
      ) : null}
    </span>
  );

  const actions = (
    <>
      {p.retryable && onRetry ? (
        <Button variant="primary" size="sm" onClick={onRetry} icon="refresh">
          重试
        </Button>
      ) : null}
      {extraActions}
    </>
  );

  if (p.unknownOutcome) {
    return (
      <StateView
        kind="unknown"
        title={p.title}
        description={
          <>
            {p.message}
            <br />
            {p.guidance}
            <br />
            {diagnostic}
          </>
        }
        actions={actions}
        inline={compact}
      />
    );
  }

  if (p.forbidden) {
    return (
      <StateView
        kind="forbidden"
        title={p.title}
        description={
          <>
            {p.message}
            <br />
            {p.guidance}
            <br />
            {diagnostic}
          </>
        }
        actions={actions}
        inline={compact}
      />
    );
  }

  return (
    <StateView
      kind="error"
      title={p.title}
      description={
        <>
          {p.message}
          <br />
          <strong>处理建议：</strong>
          {p.guidance}
          {p.conflict ? (
            <>
              <br />
              该资源已被他人修改。请重新加载最新版本，保留你已填写的内容后再提交。
            </>
          ) : null}
          <br />
          {diagnostic}
        </>
      }
      actions={actions}
      inline={compact}
    />
  );
}
