import { Badge, type BadgeTone } from '@/components/ui';
import type { RiskLevel } from '@/types';

/**
 * 风险等级徽标。
 * 来源：第十七篇 §8.4（风险分级：影响程度、可逆性、财务影响、数据敏感性）。
 * 注意：风险等级不是 contracts/01 的状态词表成员，而是业务分级的 UI 投影，
 * 因此本文件不将其纳入 RUN/STEP/ERP 状态映射。
 */
const RISK_META: Record<RiskLevel, { label: string; tone: BadgeTone }> = {
  low: { label: '低风险', tone: 'neutral' },
  medium: { label: '中风险', tone: 'warning' },
  high: { label: '高风险', tone: 'error' },
  critical: { label: '严重风险', tone: 'error' },
};

export function RiskBadge({ level }: { level: RiskLevel }) {
  const meta = RISK_META[level] ?? RISK_META.low;
  return (
    <Badge tone={meta.tone} title={`风险等级：${meta.label}`}>
      {meta.label}
    </Badge>
  );
}

export function riskLabel(level: RiskLevel): string {
  return (RISK_META[level] ?? RISK_META.low).label;
}
