# ADR-002：Registry 数据模型与版本锁定合并

- **状态**：已接受
- **日期**：2026-10-12
- **决策域**：插件 Registry 数据模型、RunVersionLock、审批摘要公式（跨第二篇、第四篇、第八篇、第九篇）

## 背景

1. **Registry 两套表结构冲突**（评审报告 C3）：第二篇 §4 采用 `plugins(tenant_id, plugin_key)` 加 agents/tools 独立表；第四篇 §5 采用 `plugins` 全局唯一 + `plugin_dependencies` 一统模型。两套的租户模型与唯一键均冲突，且第二篇 `plugin_installations` 的 `UNIQUE (tenant_id, plugin_id)` 会丢失升级历史（同一插件同版本重复安装被拒，但同插件多版本安装记录语义不清）。
2. **RunVersionLock 两说**（评审报告 C8）：第八篇定义 5 项版本锁定，第九篇定义 9 项，运行锁定的版本范围没有单一结构。
3. **审批摘要公式两处**：第六篇 §11.1 要求审批绑定操作参数、目标对象、业务对象版本和摘要；第七篇要求绑定 adapter/mapping 版本。两处字段集未合并，执行前重算摘要时无法判断"摘要到底覆盖哪些版本"。

## 决策

1. **Registry 数据模型以第四篇一统模型为基线**：
   - `plugins` 全局唯一（`plugin_key` 全局唯一），插件五类 kind：`AGENT` / `ONTOLOGY` / `TOOL` / `POLICY` / `MODEL_PROVIDER`；租户通过安装与启用记录获得使用权，而不是在插件主键中携带 tenant_id。
   - 一套插件状态枚举（第四篇 §4.2 生命周期为准），第二篇 agents/tools 独立表中的同类状态收敛到该枚举。
   - 安装历史表唯一键为 `UNIQUE (tenant_id, plugin_id, plugin_version_id)`，保留同插件多版本安装与升级历史。
   - 第二篇 registry schema 按此迁移对齐（迁移脚本由第二篇修订落地，含数据搬迁与旧键废弃说明）。
2. **RunVersionLock 合并为单一结构**：以第八篇 5 项为基础骨架，第九篇扩展项作为 `model` 子结构挂入（model_provider、model_revision、routing_policy_version、数据策略版本等归子结构），形成全平台唯一的运行版本锁定结构。数据库落点为第二篇 `runtime.agent_run_dependencies` 表（已含 `dep_type` + 版本引用），第八篇与第九篇的定义改为引用该表与本 ADR。
3. **审批摘要公式统一为一处定义**：取第六篇 §11.1 四项与第七篇 adapter/mapping 版本的并集——
   `operation_digest = H(operation_type || target_object_id || expected_object_version || canonical_payload || ontology_digest || mapping_version || adapter_version || policy_version)`
   该公式在第六篇 §11.1 定义，第七篇 §6.5/§8.1 与第十四篇 §12.3 引用；执行前重算摘要并与审批记录绑定摘要比对，不一致即失效（第十四篇 §12.3 行为不变）。`expected_object_version` 缺失时按策略显式记录为无版本约束，不得静默省略。

## 理由

- 第四篇一统模型天然支持"一个插件多租户安装"与插件间依赖（`plugin_dependencies`），避免每租户复制插件主记录；五类 kind 覆盖总书 §6.2 的五种插件协议，消除 4→5 的漏项风险。
- 版本锁定若维持两套，Run 恢复与证据复核时无法回答"这次运行到底固定了哪些版本"；单一结构 + 关系表落点同时满足查询、外键约束与依赖审计。
- 审批摘要若不含 mapping/adapter 版本，映射或适配器升级后旧审批可能覆盖语义已变的操作，形成"审批通过但执行的是另一套语义"的缺口。

## 后果

- 正面：Registry 迁移目标唯一；版本锁定可查询、可审计；审批绑定覆盖语义层版本。
- 负面：第二篇 §4 需要重写迁移（含历史数据搬迁策略：旧 agents/tools 表数据并入 plugins + 安装记录）；第九篇 9 项中不属于 model 子结构的项需明确归属，评审时逐项确认。
- 约束：新增插件类型只能扩 `kind` 枚举（走 ADR + contracts 同步），不得新建并行注册表；`agent_run_dependencies` 的 `dep_type` 枚举即 RunVersionLock 字段清单，DDL 与文档由 CI 一致性 lint 校验相等。

## 关联文档

- 第二篇 §4（registry schema 迁移对齐）、`runtime.agent_run_dependencies`
- 第四篇 §4-5（一统模型基线）
- 第六篇 §7.2（Execution Manifest）、§11.1（审批摘要公式唯一定义）
- 第七篇 §6.5（映射版本）、§8.1（幂等键与摘要绑定）
- 第八篇（RunVersionLock 5 项并入）、第九篇 §5（9 项中 model 子结构并入）
- 第十四篇 §12.3（执行前摘要重算）
- 设计书评审与优化建议报告 C3、C8
