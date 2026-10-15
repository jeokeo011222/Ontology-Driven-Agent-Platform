import { resolveBadgeMeta, type BadgeMeta } from './safe-meta';

/**
 * components/status-badge/version-status.ts —— **版本生命周期**状态词表（跨资源共用）
 *
 * 真源：migrations/platform/ 的 CHECK 约束（本词表尚未被 docs/contracts/01 收录）
 *   - ontology.ontology_versions.status : DRAFT VALIDATING VALID REJECTED APPROVED
 *                                        PUBLISHED DEPRECATED DISABLED
 *   - registry.agent_versions.status    : DRAFT VALIDATING APPROVED PUBLISHED
 *                                        DEPRECATED DISABLED
 *   - registry.tool_versions.status     : 同 agent_versions
 *   - registry.plugin_versions.status   : CREATED VALIDATING TESTING STAGED APPROVED
 *                                        PUBLISHED DEPRECATED DISABLED REJECTED
 *
 * 为什么合并成一张表：同一"版本生命周期"语义在各资源下标签应当一致（草稿/验证中/
 * 已发布/已废弃/已停用…），分散维护必然漂移。各资源子集的收窄由 types/domain.ts
 * 的联合类型（OntologyVersionStatus / RegistryVersionStatus）表达。
 *
 * 注：三条约束**互不一致**（本体版本独有 VALID/REJECTED，插件版本用 CREATED
 * 而非 DRAFT），这属于平台词表治理缺口，已记入自查矩阵。
 */
export const VERSION_STATUS_META: Record<string, BadgeMeta> = {
  CREATED: { label: '已创建', tone: 'neutral', title: '版本已创建，尚未开始校验' },
  DRAFT: { label: '草稿', tone: 'neutral', title: '草稿：可编辑，不参与运行' },
  VALIDATING: { label: '验证中', tone: 'info', title: '正在执行结构校验 / 语义回归 / 依赖兼容性检查' },
  VALID: { label: '已验证', tone: 'info', title: '校验通过，尚未提交发布审批' },
  TESTING: { label: '测试中', tone: 'info', title: '正在执行契约与安全测试' },
  STAGED: { label: '已暂存', tone: 'info', title: '已进入暂存环境，等待审批发布' },
  REJECTED: { label: '验证未通过', tone: 'error', title: '校验或测试未通过，存在阻断项' },
  APPROVED: { label: '待发布', tone: 'warning', title: '已通过审批，等待发布为不可变版本' },
  PUBLISHED: { label: '已发布', tone: 'success', title: '已发布为不可变版本，可被运行锁定引用' },
  DEPRECATED: { label: '已废弃', tone: 'neutral', title: '已废弃：不再用于新的运行' },
  DISABLED: { label: '已停用', tone: 'neutral', title: '已停用：不得用于新的运行' },
};

/** 尚无版本行（`latest_version_status` 为 null） */
export const NO_VERSION_BADGE_META: BadgeMeta = {
  label: '无版本',
  tone: 'neutral',
  title: '该资源尚无版本（从未创建版本行）',
};

/**
 * 解析版本状态 → 徽标元数据（**永不抛错**）。
 * 真实取值来自网络且三张版本表子集不同，必须走安全访问器（缺陷 #19）。
 */
export function versionStatusBadge(status: unknown, context: string): BadgeMeta {
  if (status === null || status === undefined) return NO_VERSION_BADGE_META;
  return resolveBadgeMeta(VERSION_STATUS_META, status, context);
}
