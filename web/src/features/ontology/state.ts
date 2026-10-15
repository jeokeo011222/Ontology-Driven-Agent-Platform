import { resolveBadgeMeta, versionStatusBadge, type BadgeMeta } from '@/components/status-badge';

/**
 * features/ontology/state.ts —— 本体相关的两个状态维度
 *
 * ⚠️ 本体列表/详情存在**两个互不相同**的状态维度，不能混为一谈：
 *   1. `latest_version_status` —— 版本生命周期（草稿 / 验证中 / 已发布 / 已废弃…）。
 *      词表真源：migrations/platform/000004 的 ontology_versions.status CHECK 约束；
 *      跨资源共用词表见 components/status-badge/version-status.ts。
 *   2. `status` —— 本体级状态（ontology.ontologies.status，当前实现写入 ACTIVE）。
 *
 * 缺陷 #19 根因回顾：本文件此前只有一个 `ONTOLOGY_STATE_META`，键是
 *   draft/validating/... 六个**自造小写别名**，却按后端**不存在**的
 *   `OntologySummary.state` 索引 —— 词表与真实字段、真实取值三方错位，
 *   列表页 `META[item.state].tone` 直接抛 TypeError 白屏。
 */

/** 本体级状态（ontology.ontologies.status）中文标签；未知取值由安全访问器兜底 */
export const ONTOLOGY_STATUS_META: Record<string, BadgeMeta> = {
  ACTIVE: { label: '启用', tone: 'success', title: '本体处于启用状态' },
  INACTIVE: { label: '已停用', tone: 'neutral', title: '本体已停用，不再用于新的运行' },
  ARCHIVED: { label: '已归档', tone: 'neutral', title: '本体已归档，不再用于新的运行' },
};

/**
 * 解析**版本**状态 → 徽标元数据（永不抛错）。
 * 走跨资源共用的版本词表，null/undefined 显示"无版本"。
 */
export function ontologyVersionBadge(status: unknown, context = '本体列表'): BadgeMeta {
  return versionStatusBadge(status, context);
}

/** 解析**本体级**状态 → 徽标元数据（永不抛错） */
export function ontologyStatusBadge(status: unknown, context = '本体列表'): BadgeMeta {
  return resolveBadgeMeta(ONTOLOGY_STATUS_META, status, context);
}
