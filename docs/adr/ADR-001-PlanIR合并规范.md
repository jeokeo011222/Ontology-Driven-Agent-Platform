# ADR-001：Plan IR 合并规范（计划体系统一）

- **状态**：已接受
- **日期**：2026-10-12
- **决策域**：执行计划 IR、计划编译职责边界（跨第六篇、第八篇、第十四篇、第十六篇）

## 背景

计划体系在设计书中存在四份互相冲突的定义（评审报告 C2）：

| 出处 | 定义形态 | 节点类型 |
|---|---|---|
| 第六篇 §5 | Plan 数据结构 + Plan Compiler 处理流程 | step.kind = tool / ontology_validation / erp_operation |
| 第八篇 §7 | Execution Plan 数据结构 + §7.3 计划编译器 + §7.4 Orchestrator 接口（含 Compile 职责） | 另一套 step 描述 |
| 第十四篇 §5-6/§17 | Execution Plan IR（13 类节点）+ Planner/PlanCompiler/Executor 接口 | 13 类节点（最完整） |
| 第十六篇 §7.2 | steps.type 枚举 | TOOL_CALL / ONTOLOGY_VALIDATE / RULE_EVALUATE / AUTHORIZATION_CHECK / ERP_OPERATION / ERP_OPERATION_VERIFY / EVIDENCE_BUILD |

四份定义对"谁能编译计划、谁能调度计划"的边界说法各不相同：第八篇 §7.4 让 Orchestrator 承担 Compile 职责，第十四篇 §17 又定义了一套 Executor.Invoke 通用调用接口，与 ERP Gateway 的唯一写入口设计存在张力。若不收敛，实现期将出现两套 Plan IR、两条编译路径和两个执行入口。

## 决策

1. **Planner 只产候选计划**。LLM 输出不可信：Planner（含第十四篇 Semantic Planner、第八篇 Agent Planner）的职责止于生成候选逻辑计划与解释，不拥有编译权、类型检查权和执行权。候选计划未经编译不得进入执行状态。
2. **编译、类型检查与策略检查统一归 Harness Plan Compiler**（第六篇 §5 扩展）。Plan Compiler 是唯一的编译权威：节点类型以第十四篇 §6.2 的 13 类为基线（该定义最完整），第六篇 §5 与第十六篇 §7.2 的节点枚举收敛到该基线；类型检查、数据流校验、依赖无环检查、版本绑定与 freeze 均由 Compiler 完成。
3. **Orchestrator 只管 DAG 调度与依赖推进**。第八篇 §7.4 中 Orchestrator 的 Compile 职责删除，改为调用 Harness Plan Compiler（由第八篇修订落地）；Orchestrator 不得自行改写计划内容、不得自行绑定工具版本。
4. **删除重复接口定义**。第十四篇 §17.2 `Executor.Invoke` 通用接口与第八篇 §7.4 的重复接口定义删除，改为引用本 ADR 与第六篇 §9 的统一工具执行协议；第十四篇 §17.2 的工具执行入口按"只读执行器 / 写网关"拆分重写（见第十四篇修订）。
5. **计划 freeze 后不可变，digest 绑定完整版本集**。冻结后的 `PlanRevision` 不可修改，实质变化必须产生新修订版。计划 digest 绑定的版本字段取第六篇 §7.2 Execution Manifest 与第七篇 §6.5 映射版本发布的并集：ontology、tool_binding、action_contract、rule_bundle、mapping、policy、adapter、model_routing_policy 八类版本（合并后的一处定义，两篇互相引用，不得再各自扩充）。
6. **节点类型基线（以第十四篇 §6.2 的 13 类为唯一枚举）**：SEMANTIC_QUERY、TOOL_CALL、RULE_EVALUATION、DATA_VALIDATION、BRANCH、FAN_OUT、JOIN、APPROVAL_GATE、WAIT_EVENT、HUMAN_TASK、COMPENSATION、RESULT_VERIFY、EVIDENCE_FINALIZE。第十六篇 §7.2 的 AUTHORIZATION_CHECK / ERP_OPERATION / ERP_OPERATION_VERIFY / ONTOLOGY_VALIDATE 映射为：授权检查归 Execution Guard 前置门（第六篇 §9.2）而非计划节点；ERP 操作与回执验证归第七篇 ERP Gateway + RESULT_VERIFY；本体验证归 DATA_VALIDATION。

## 理由

- 四份 IR 中只有第十四篇覆盖了分支、并行、审批门、人工任务、补偿与证据收尾等企业工作流必需节点；以其为基线减少重新设计。
- LLM 候选 + 确定性编译是第六篇 §5.1 已确立的原则（"为什么不能直接执行模型输出"），本 ADR 把该原则推广为跨篇职责边界。
- Orchestrator 保留编译职责会造成"调度器改写计划"，破坏 freeze 不可变与审批绑定的参数摘要前提（第十四篇 §9.2/§12.3）。
- 通用 Executor.Invoke 会成为绕过 ERP Gateway 的第二写入口，与第七篇 §7"统一写入控制"冲突。

## 后果

- 正面：一份 IR、一个编译器、一个执行写入口；计划版本可冻结、可复核、可回归测试；第十六篇示例与第十四篇接口不再分叉。
- 负面：第八篇 §7.3/§7.4 需要修订（删除 Orchestrator 的 Compile 职责与重复接口）；第十四篇 §17.2 接口重写；已按旧 IR 编写的代码需迁移。迁移成本一次性，且集中在编译层。
- 约束：新增节点类型必须走 ADR 并同步第六篇 §5、第十四篇 §6.2 与 contracts/openapi.yaml；计划模板资产走 `/api/v1/plan-templates`（contracts/03 §4 裁决），执行一律 `/api/v1/runs`。

## 关联文档

- 第六篇 §5（Plan Compiler 权威）、§7.2（Execution Manifest 版本绑定）、§9（统一工具执行协议）
- 第七篇 §6.5（映射版本发布）、§7（ERP Operation Gateway 唯一写入口）
- 第八篇 §7.2-7.4（本次删除 Compile 职责与重复接口，由第八篇修订落地）
- 第十四篇 §5-6/§17（IR 基线与接口重写）
- 第十六篇 §7.2（示例改用统一 IR）
- contracts/01（状态词表唯一真源）、contracts/03（API 命名裁决）
- 设计书评审与优化建议报告 C2
