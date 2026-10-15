import type { BadgeMeta } from '@/components/status-badge';

/**
 * 审计事件结论（outcome）词表 —— 审计中心与证据包视图共用。
 *
 * 为什么这张表必须与后端真实写入值一致：
 *   审计事件表的 `outcome` 列**没有 CHECK 约束**，契约（contracts/openapi.yaml
 *   `#/components/schemas/AuditEvent`）也把它声明为自由 `type: string`。后端
 *   `h.recordAudit` 实际写入的是 SUCCESS / DENIED / REJECTED / ERROR
 *   （见 internal/api/handlers/*.go；DENIED / REJECTED 亦见于
 *    internal/api/store/audit_aggregate.go 的安全告警统计口径）。
 *
 *   早期前端沿用设计稿里的**授权决策**词表（ALLOW / DENY / REQUIRE_APPROVAL），
 *   与真实写入值不匹配，于是词表外取值经 MetaBadge 安全兜底后几乎每行都显示
 *   "未知状态"——不崩溃，但页面等于没显示结论。
 *
 * 为什么不把词表写在某个页面里：审计中心渲染 EvidencePage，EvidencePage 若反向
 * 从 AuditPage 取词表会形成循环依赖；本模块作为两页的单一真源，避免再一次漂移。
 *
 * 词表外取值仍由 MetaBadge 兜底为"未知状态"（显式可见，不静默伪装成已知状态）。
 */
export const AUDIT_OUTCOME_META: Record<string, BadgeMeta> = {
  SUCCESS: { label: '成功', tone: 'success', title: '操作已按预期执行完成' },
  DENIED: { label: '拒绝', tone: 'error', title: '被授权 / 策略拒绝，或未通过校验' },
  REJECTED: { label: '驳回', tone: 'error', title: '经人工或质量门禁驳回' },
  ERROR: { label: '错误', tone: 'error', title: '执行过程中发生错误' },
};

/** 结论筛选下拉项：取值与后端实际写入值一致，不提供不会命中的选项。 */
export const AUDIT_OUTCOME_OPTIONS = (['SUCCESS', 'DENIED', 'REJECTED', 'ERROR'] as const).map((value) => ({
  value,
  label: AUDIT_OUTCOME_META[value].label,
}));
