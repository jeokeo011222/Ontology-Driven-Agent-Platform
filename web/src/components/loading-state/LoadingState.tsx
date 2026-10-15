import { StateView } from '@/components/error-state';

export interface LoadingStateProps {
  /** 自定义文案，如“正在加载任务详情…” */
  label?: string;
  inline?: boolean;
}

/** 加载中态（§10.5） */
export function LoadingState({ label, inline }: LoadingStateProps) {
  return <StateView kind="loading" title={label} inline={inline} />;
}

export interface SkeletonListProps {
  rows?: number;
}

/** 列表骨架屏，避免加载期布局跳动 */
export function SkeletonList({ rows = 4 }: SkeletonListProps) {
  return (
    <div className="stack stack--tight" aria-hidden="true">
      {Array.from({ length: rows }).map((_, i) => (
        <div key={i} className="skeleton" style={{ height: 18, width: `${100 - i * 6}%` }} />
      ))}
    </div>
  );
}
