# 《第六篇：Harness 执行引擎与状态机详细设计》

ODAP · Ontology-Driven Agent Platform · Runtime Execution Architecture

本篇承接前五篇设计，解决 ODAP 从“能够理解企业业务”到“能够安全、可靠地执行企业业务”的关键问题。

前五篇已经建立了平台架构、数据库模型、身份与权限引擎、插件 Registry，以及 Ontology Runtime 与 Ontology Factory。现在需要将这些模块组织成一个真正可运行的执行系统。

核心设计原则：LLM 负责提出计划和解释问题，Harness 负责控制执行、验证约束、落实权限、管理状态和处理故障。

Harness 不是一个简单的 Agent 循环，也不是把模型、工具串联起来的工作流。它是 ODAP 的受控执行内核，必须确保每一次工具调用、每一次审批暂停、每一次外部系统写入，都有明确的状态、权限、版本、执行记录和恢复路径。

## 1. Harness 的设计目标与边界

### 1.1 Harness 需要解决的核心问题

企业级智能体执行任务时，会遇到一系列普通聊天系统不必处理的问题：

- 模型可能生成无效计划或调用不存在的工具。
- 任务可能执行数十步，期间服务重启或网络中断。
- 某个步骤可能成功，但返回结果时发生超时。
- 用户权限可能在任务运行期间被撤销。
- 业务本体可能更新，而运行中的任务仍依赖旧版本。
- 高风险操作可能必须等待人工审批。
- ERP 可能已执行写入，但平台尚未收到回执。
- Agent 可能重复请求相同操作，造成重复提交。
- 不可信插件可能试图绕过平台，直接访问业务系统。
- 任务需要暂停、恢复、取消、重试或转交人工处理。

因此，Harness 必须是一个持久化、可恢复、可审计、具备权限强制执行能力的状态机运行时。

### 1.2 Harness 的职责边界

| 模块                   | 负责什么             | 不负责什么           |
| -------------------- | ---------------- | --------------- |
| LLM / Agent          | 提出计划、解释语义、生成候选参数 | 自行授予权限、直接修改运行状态 |
| Harness              | 状态机、调度、检查点、恢复、取消 | 取代业务规则和授权引擎     |
| Authorization Engine | 判断主体能否执行操作       | 实际执行 ERP 操作     |
| Ontology Runtime     | 提供语义查询、约束验证与证据   | 自动批准业务操作        |
| Tool Runtime         | 在隔离环境内运行工具       | 自行绕过授权访问 ERP    |
| Workflow / Approval  | 管理审批请求与决定        | 仅凭模型意见批准高风险操作   |
| ERP Port / Adapter   | 调用 ERP 并对账确认结果   | 将超时直接视为操作失败     |
| Audit / Evidence     | 保存决策、证据和执行轨迹     | 替代业务事务本身        |

### 1.3 Harness 的基本执行链路

1\. 创建任务

验证请求者身份、租户、任务范围与幂等键

2\. 固定执行上下文

绑定 Agent、Ontology、Tool、Policy 及运行配置版本

3\. 生成执行计划

Agent 提出结构化计划，Harness 校验计划契约

4\. 验证与授权

检查本体约束、业务规则、权限和审批条件

5\. 执行步骤

调度工具、保存检查点、记录调用和证据

6\. 处理审批或异常

暂停、等待、恢复、对账或转人工处理

7\. 完成与审计

核验最终结果，保存执行摘要与外部回执

这条链路不是简单的单向流水线。遇到审批、缺失信息、工具失败或外部结果未知时，Harness 必须能够安全暂停，并在条件满足后继续执行。

## 2. Harness 总体架构

### 2.1 逻辑模块

建议在 Go 应用层建立以下模块：

```
internal/harness/
├── domain/
│   ├── run.go
│   ├── state.go
│   ├── plan.go
│   ├── step.go
│   ├── event.go
│   └── checkpoint.go
├── application/
│   ├── create_run.go
│   ├── plan_run.go
│   ├── advance_run.go
│   ├── approve_run.go
│   ├── cancel_run.go
│   ├── recover_run.go
│   └── reconcile_run.go
├── engine/
│   ├── scheduler.go
│   ├── state_machine.go
│   ├── dependency_resolver.go
│   ├── retry_policy.go
│   └── execution_coordinator.go
├── context/
│   ├── builder.go
│   ├── version_lock.go
│   └── context_policy.go
├── planner/
│   ├── planner.go
│   ├── plan_validator.go
│   └── plan_compiler.go
├── checkpoint/
│   ├── repository.go
│   └── manager.go
├── recovery/
│   ├── recovery_worker.go
│   ├── lease_manager.go
│   └── reconciliation.go
├── ports/
│   ├── agent.go
│   ├── tool.go
│   ├── authorization.go
│   ├── ontology.go
│   ├── approval.go
│   ├── erp.go
│   └── audit.go
└── transport/
    └── handlers.go
```

其中，`domain` 定义状态、事件与不变量；`application` 编排用例；`engine` 决定任务何时推进；`ports` 隔离外部系统；`recovery` 处理崩溃、超时与未知结果。

### 2.2 运行部署模式

MVP 建议使用三个应用进程：

### ODAP API Server

接收请求、认证鉴权、创建任务、查询状态、处理审批操作

### Harness Worker

领取任务、生成计划、执行步骤、保存检查点、处理恢复与对账

PostgreSQL

任务、状态、事件、检查点、Outbox

受控执行服务

Tool Runtime、Ontology Runtime、ERP Port

MVP 不需要立刻引入复杂的分布式工作流平台。可以先使用 PostgreSQL 任务表、租约、条件更新和 Outbox 建立可靠执行骨架；当任务量、执行时长或分布式协调需求增长后，再考虑专用工作流引擎。

重要：API Server 和 Worker 可以分别部署，但不能各自维护互不关联的任务状态。PostgreSQL 中的持久化状态是运行时的权威记录。

## 3. 核心状态机设计

Harness 不应只用一个 `status` 字段表达整个执行过程。一个任务可能包含多个步骤，每个步骤可能调用多个工具，而工具又可能发起 ERP 操作。这些对象的生命周期并不完全相同。

建议采用四层状态机：

1. Run State Machine：整个任务的生命周期。
2. Step State Machine：单个执行步骤的生命周期。
3. Invocation State Machine：一次 Agent 或 Tool 调用的生命周期。
4. External Operation State Machine：外部业务操作的执行与对账状态。

四层状态必须分开保存。任务失败不意味着所有步骤都失败；工具超时也不意味着 ERP 操作没有执行。

### 3.1 Run 状态定义

建议第一版采用以下状态：

| 状态                        | 含义                  | 是否可调度       |
| ------------------------- | ------------------- | ----------- |
| `CREATED`                 | 任务已创建，等待初始化         | 是           |
| `CONTEXT_BUILDING`        | 正在构建运行上下文           | 是           |
| `READY`                   | 上下文已固定，可以规划或执行      | 是           |
| `PLANNING`                | Agent 正在生成执行计划      | 否，当前规划调用进行中 |
| `PLAN_VALIDATING`         | 正在检查计划结构、依赖和风险      | 否           |
| `RUNNING`                 | 正在执行步骤              | 是           |
| `WAITING_INPUT`           | 等待用户补充信息            | 否           |
| `WAITING_APPROVAL`        | 等待人工审批              | 否           |
| `PAUSED`                  | 由用户或管理员主动暂停         | 否           |
| `RECOVERING`              | 正在恢复任务或检查外部操作结果     | 仅恢复工作可调度    |
| `CANCELLING`              | 取消请求已受理，正在停止后续步骤并等待在途外部操作收敛 | 仅取消收敛工作可调度   |
| `REQUIRES_RECONCILIATION` | 存在尚未确定的外部操作结果       | 仅对账工作可调度    |
| `COMPLETED`               | 任务按定义成功完成           | 否           |
| `FAILED`                  | 任务已终止且无法继续          | 否           |
| `CANCELLED`               | 任务取消完成，且不存在未处理的执行风险 | 否           |
| `EXPIRED`                 | 任务超过允许的生命周期         | 否           |
| `MANUAL_REVIEW`           | 需要人工介入才能安全继续        | 否           |

`CANCELLING` 是统一的取消入口状态：**任何取消请求都不得直接把任务置为终态**。取消的第一动作是停止接纳新步骤，然后由取消收敛工作（Cancellation Convergence Worker）检查是否存在在途或结果未知的外部操作：

- 无在途外部操作 → `CANCELLING → CANCELLED`（事件 `CancelConverged`）。
- 存在结果未知的外部操作 → `CANCELLING → REQUIRES_RECONCILIATION`，对账确认后再决定 `CANCELLED`、`FAILED` 或 `MANUAL_REVIEW`。
- 对账发现无法自动收敛 → `CANCELLING → MANUAL_REVIEW`。禁止 `CANCELLING → FAILED` 直达，以避免把"结果未知的外部写入"静默当成失败。

`COMPLETED`、`FAILED`、`CANCELLED` 和 `EXPIRED` 通常属于终态。但如果任务仍存在结果未知的外部写入，就不能直接进入普通的 `CANCELLED` 或 `FAILED`，而应先完成对账或转入人工处理。

例如，用户取消了一笔正在提交到 ERP 的报销付款操作。Harness 可以立即停止后续步骤，但不能据此声称付款已经取消。

### 3.2 Run 状态转换图

\#chatgpt-mermaid-\_r_2c7\_{font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";font-size:16px;fill:rgb(237, 237, 237);}@keyframes edge-animation-frame{from{stroke-dashoffset:0;}}@keyframes dash{to{stroke-dashoffset:0;}}#chatgpt-mermaid-\_r_2c7\_ .edge-animation-slow{stroke-dasharray:9,5!important;stroke-dashoffset:900;animation:dash 50s linear infinite;stroke-linecap:round;}#chatgpt-mermaid-\_r_2c7\_ .edge-animation-fast{stroke-dasharray:9,5!important;stroke-dashoffset:900;animation:dash 20s linear infinite;stroke-linecap:round;}#chatgpt-mermaid-\_r_2c7\_ .error-icon{fill:rgb(48, 48, 48);}#chatgpt-mermaid-\_r_2c7\_ .error-text{fill:rgb(237, 237, 237);stroke:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_2c7\_ .edge-thickness-normal{stroke-width:1px;}#chatgpt-mermaid-\_r_2c7\_ .edge-thickness-thick{stroke-width:3.5px;}#chatgpt-mermaid-\_r_2c7\_ .edge-pattern-solid{stroke-dasharray:0;}#chatgpt-mermaid-\_r_2c7\_ .edge-thickness-invisible{stroke-width:0;fill:none;}#chatgpt-mermaid-\_r_2c7\_ .edge-pattern-dashed{stroke-dasharray:3;}#chatgpt-mermaid-\_r_2c7\_ .edge-pattern-dotted{stroke-dasharray:2;}#chatgpt-mermaid-\_r_2c7\_ .marker{fill:rgb(175, 175, 175);stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_2c7\_ .marker.cross{stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_2c7\_ svg{font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";font-size:16px;}#chatgpt-mermaid-\_r_2c7\_ p{margin:0;}#chatgpt-mermaid-\_r_2c7\_ defs [id$="-barbEnd"]{fill:rgb(175, 175, 175);stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_2c7\_ g.stateGroup text{fill:rgb(31, 78, 148);stroke:none;font-size:10px;}#chatgpt-mermaid-\_r_2c7\_ g.stateGroup text{fill:rgb(237, 237, 237);stroke:none;font-size:10px;}#chatgpt-mermaid-\_r_2c7\_ g.stateGroup .state-title{font-weight:bolder;fill:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_2c7\_ g.stateGroup rect{fill:rgb(9, 23, 44);stroke:rgb(31, 78, 148);}#chatgpt-mermaid-\_r_2c7\_ g.stateGroup line{stroke:rgb(175, 175, 175);stroke-width:1;}#chatgpt-mermaid-\_r_2c7\_ .transition{stroke:rgb(175, 175, 175);stroke-width:1;fill:none;}#chatgpt-mermaid-\_r_2c7\_ .stateGroup .composit{fill:transparent;border-bottom:1px;}#chatgpt-mermaid-\_r_2c7\_ .stateGroup .alt-composit{fill:#e0e0e0;border-bottom:1px;}#chatgpt-mermaid-\_r_2c7\_ .state-note{stroke:rgb(58, 132, 63);fill:rgb(48, 48, 48);}#chatgpt-mermaid-\_r_2c7\_ .state-note text{fill:rgb(237, 237, 237);stroke:none;font-size:10px;}#chatgpt-mermaid-\_r_2c7\_ .stateLabel .box{stroke:none;stroke-width:0;fill:rgb(9, 23, 44);opacity:0.5;}#chatgpt-mermaid-\_r_2c7\_ .edgeLabel .label rect{fill:rgb(9, 23, 44);opacity:0.5;}#chatgpt-mermaid-\_r_2c7\_ .edgeLabel{background-color:rgb(0, 0, 0);text-align:center;}#chatgpt-mermaid-\_r_2c7\_ .edgeLabel p{background-color:rgb(0, 0, 0);}#chatgpt-mermaid-\_r_2c7\_ .edgeLabel rect{opacity:0.5;background-color:rgb(0, 0, 0);fill:rgb(0, 0, 0);}#chatgpt-mermaid-\_r_2c7\_ .edgeLabel .label text{fill:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_2c7\_ .label div .edgeLabel{color:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_2c7\_ .stateLabel text{fill:rgb(237, 237, 237);font-size:10px;font-weight:bold;}#chatgpt-mermaid-\_r_2c7\_ .node circle.state-start{fill:rgb(175, 175, 175);stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_2c7\_ .node .fork-join{fill:rgb(175, 175, 175);stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_2c7\_ .node circle.state-end{fill:rgb(31, 78, 148);stroke:transparent;stroke-width:1.5;}#chatgpt-mermaid-\_r_2c7\_ .end-state-inner{fill:transparent;stroke-width:1.5;}#chatgpt-mermaid-\_r_2c7\_ .node rect{fill:rgb(9, 23, 44);stroke:rgb(31, 78, 148);stroke-width:1px;}#chatgpt-mermaid-\_r_2c7\_ .node polygon{fill:rgb(9, 23, 44);stroke:rgb(31, 78, 148);stroke-width:1px;}#chatgpt-mermaid-\_r_2c7\_ [id$="-barbEnd"]{fill:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_2c7\_ .statediagram-cluster rect{fill:rgb(9, 23, 44);stroke:rgb(31, 78, 148);stroke-width:1px;}#chatgpt-mermaid-\_r_2c7\_ .cluster-label,#chatgpt-mermaid-\_r_2c7\_ .nodeLabel{color:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_2c7\_ .statediagram-cluster rect.outer{rx:5px;ry:5px;}#chatgpt-mermaid-\_r_2c7\_ .statediagram-state .divider{stroke:rgb(31, 78, 148);}#chatgpt-mermaid-\_r_2c7\_ .statediagram-state .title-state{rx:5px;ry:5px;}#chatgpt-mermaid-\_r_2c7\_ .statediagram-cluster.statediagram-cluster .inner{fill:transparent;}#chatgpt-mermaid-\_r_2c7\_ .statediagram-cluster.statediagram-cluster-alt .inner{fill:rgb(48, 48, 48);}#chatgpt-mermaid-\_r_2c7\_ .statediagram-cluster .inner{rx:0;ry:0;}#chatgpt-mermaid-\_r_2c7\_ .statediagram-state rect.basic{rx:5px;ry:5px;}#chatgpt-mermaid-\_r_2c7\_ .statediagram-state rect.divider{stroke-dasharray:10,10;fill:rgb(48, 48, 48);}#chatgpt-mermaid-\_r_2c7\_ .note-edge{stroke-dasharray:5;}#chatgpt-mermaid-\_r_2c7\_ .statediagram-note rect{fill:rgb(48, 48, 48);stroke:rgb(58, 132, 63);stroke-width:1px;rx:0;ry:0;}#chatgpt-mermaid-\_r_2c7\_ .statediagram-note rect{fill:rgb(48, 48, 48);stroke:rgb(58, 132, 63);stroke-width:1px;rx:0;ry:0;}#chatgpt-mermaid-\_r_2c7\_ .statediagram-note text{fill:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_2c7\_ .statediagram-note .nodeLabel{color:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_2c7\_ .statediagram .edgeLabel{color:red;}#chatgpt-mermaid-\_r_2c7\_ [id$="-dependencyStart"],#chatgpt-mermaid-\_r_2c7\_ [id$="-dependencyEnd"]{fill:rgb(175, 175, 175);stroke:rgb(175, 175, 175);stroke-width:1;}#chatgpt-mermaid-\_r_2c7\_ .statediagramTitleText{text-anchor:middle;font-size:18px;fill:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_2c7\_ [data-look="neo"].statediagram-cluster rect{fill:rgb(9, 23, 44);stroke:url(#chatgpt-mermaid-\_r_2c7\_-gradient);stroke-width:1;}#chatgpt-mermaid-\_r_2c7\_ [data-look="neo"].statediagram-cluster rect.outer{rx:5px;ry:5px;filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_2c7\_ .node .neo-node{stroke:rgb(31, 78, 148);}#chatgpt-mermaid-\_r_2c7\_ [data-look="neo"].node rect,#chatgpt-mermaid-\_r_2c7\_ [data-look="neo"].cluster rect,#chatgpt-mermaid-\_r_2c7\_ [data-look="neo"].node polygon{stroke:url(#chatgpt-mermaid-\_r_2c7\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_2c7\_ [data-look="neo"].swimlane.cluster rect{filter:none;}#chatgpt-mermaid-\_r_2c7\_ [data-look="neo"].node path{stroke:url(#chatgpt-mermaid-\_r_2c7\_-gradient);stroke-width:1px;}#chatgpt-mermaid-\_r_2c7\_ [data-look="neo"].node .outer-path{filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_2c7\_ [data-look="neo"].node .neo-line path{stroke:rgb(31, 78, 148);filter:none;}#chatgpt-mermaid-\_r_2c7\_ [data-look="neo"].node circle{stroke:url(#chatgpt-mermaid-\_r_2c7\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_2c7\_ [data-look="neo"].node circle .state-start{fill:#000000;}#chatgpt-mermaid-\_r_2c7\_ [data-look="neo"].icon-shape .icon{fill:url(#chatgpt-mermaid-\_r_2c7\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_2c7\_ [data-look="neo"].icon-shape .icon-neo path{stroke:url(#chatgpt-mermaid-\_r_2c7\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_2c7\_ :root{--mermaid-font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";}CREATEDCONTEXT_BUILDINGREADYPLANNINGPLAN_VALIDATINGRUNNINGWAITING_INPUTWAITING_APPROVALPAUSEDRECOVERINGREQUIRES_RECONCILIATIONCOMPLETEDFAILEDMANUAL_REVIEWCANCELLED重新规划

该图是逻辑状态图，正式实现仍需为每一条转换定义触发事件、前置条件、事务边界和审计要求。

另外，`EXPIRED` 不能简单地由一个定时器直接设置。如果任务涉及在途 ERP 操作，过期处理应先判断是否需要对账，再决定最终状态。

### 3.3 状态转换必须由事件触发

建议定义明确的领域事件：

```
RunCreated
ContextBuildStarted
ContextBuildSucceeded
ContextBuildFailed
PlanGenerated
PlanValidationSucceeded
PlanValidationFailed
PlanRegenerationRequested
StepStarted
StepSucceeded
StepFailed
InputRequested
InputReceived
InputDeadlineExceeded
ApprovalRequested
ApprovalResolved
ApprovalRejected
ApprovalExpired
RunPaused
RunResumed
CancellationRequested
CancelConverged
RecoveryStarted
RecoveryCompleted
ExternalOperationUnknown
ReconciliationSucceeded
ReconciliationFailed
ManualInterventionRequired
ManualReviewResolved
RunCompleted
RunFailed
RunExpired
```

上述事件清单与 §14.1 的 Go 常量、§14.2 的转换矩阵三者必须保持一致；新增事件时必须同步三处，并由契约测试断言"事件清单 = 常量集合 = 矩阵覆盖集合"（见 §19）。

状态更新应通过统一的状态机服务完成，禁止 Agent 或 Tool 直接更新 `agent_runs.status`。

例如，模型返回“任务已经完成”，并不能触发 `RunCompleted`。Harness 必须验证：

- 必需步骤均已成功。
- 不存在未处理的工具调用。
- 不存在未知结果的 ERP 操作。
- 必需的业务验证已经完成。
- 任务输出符合最终输出契约。

满足这些条件后，才允许任务进入 `COMPLETED`。

## 4. Step、Invocation 与 External Operation 状态机

### 4.1 Step 状态

一个 Run 通常包含多个 Step。建议采用：

```
PENDING
  → READY
  → RUNNING
  → SUCCEEDED

RUNNING
  → WAITING_INPUT
  → WAITING_APPROVAL
  → RETRY_WAIT
  → FAILED
  → CANCELLED
  → UNKNOWN
```

其中：

- `PENDING`：依赖尚未满足。
- `READY`：依赖已满足，可以执行。
- `RUNNING`：正在执行。
- `RETRY_WAIT`：等待符合策略的重试。
- `UNKNOWN`：执行结果暂时无法确定。
- `FAILED`：已经确认失败，且没有继续重试的计划。
- `CANCELLED`：该步骤已安全取消。

Step 状态应反映业务执行阶段，而不是简单复制 Tool 的状态。例如，一个“提交报销单”步骤可能包含参数准备、业务验证、审批、ERP 提交和回执核验等多个内部动作。

### 4.2 Invocation 状态

一次 Agent 或 Tool 调用建议使用：

```
CREATED
  → AUTHORIZING
  → READY
  → DISPATCHED
  → RUNNING
  → SUCCEEDED / FAILED / TIMED_OUT / CANCELLED
```

`TIMED_OUT` 表示平台在限定时间内没有得到可确认的结果。它不自动意味着被调用方停止运行，也不意味着副作用没有发生。

调用记录至少需要绑定：

- 调用 ID。
- Run ID 与 Step ID。
- 插件 ID、版本 ID 和制品摘要。
- 输入参数摘要。
- 授权决策 ID。
- 超时与重试配置。
- 调用开始和结束时间。
- 输出摘要或错误信息。
- 取消请求与实际终止确认。

### 4.3 External Operation 状态

外部业务操作必须拥有独立状态机，尤其是 ERP 写操作。

| 状态                 | 含义                      |
| ------------------ | ----------------------- |
| `CREATED`          | 已建立操作记录                 |
| `VALIDATING`       | 正在检查参数与业务约束             |
| `REJECTED`         | 被验证或授权规则拒绝              |
| `WAITING_APPROVAL` | 等待审批                    |
| `READY`            | 具备执行条件                  |
| `DISPATCHED`       | 已向适配器发出执行请求             |
| `EXECUTING`        | 适配器正在执行                 |
| `SUCCEEDED`        | 已确认外部操作成功               |
| `FAILED`           | 已确认外部操作失败               |
| `UNKNOWN`          | 外部执行结果未知                |
| `RECONCILING`      | 正在查询外部系统以确定结果           |
| `MANUAL_REVIEW`    | 自动对账无法解决，需要人工处理         |
| `CANCELLED`        | 已确认操作取消，或已证明操作尚未执行且取消有效 |

特别强调：`DISPATCHED` 不能直接转为 `SUCCEEDED`。只有拿到可信的外部回执，或通过可靠对账确认最终状态，才能完成该转换。

### 4.4 三层状态关系

考虑一个报销任务：

- Run：`RUNNING`
- Step：`RUNNING`
- Invocation：`TIMED_OUT`
- ERP Operation：`UNKNOWN`

此时正确的处理不是将所有状态统一改成 `FAILED`，而是：

1. 停止后续依赖该结果的步骤。
2. 将 Run 转入 `REQUIRES_RECONCILIATION`。
3. 由恢复 Worker 查询 ERP 操作状态。
4. 若确认成功，保存回执并继续后续步骤。
5. 若确认未执行，按策略决定是否重新提交。
6. 若无法确认，进入 `MANUAL_REVIEW`。

这正是 Harness 与普通工具调用循环之间最重要的区别之一。

## 5. 执行计划与 Plan Compiler

### 5.1 为什么不能直接执行模型输出

LLM 输出可能包含：

- 不存在的 Tool ID。
- 不符合工具 Schema 的参数。
- 无效的依赖关系。
- 循环步骤。
- 超出任务范围的操作。
- 不合理的执行顺序。
- 未声明的敏感数据访问。
- 试图跳过验证或审批的指令。

因此，模型输出只能视为候选计划。它必须经过 Plan Compiler 和 Plan Validator，才能成为可执行的内部计划。

### 5.2 Plan 数据结构

建议计划采用有向无环图（DAG）表示，而不是只有一串自然语言步骤。

```
{
  "plan_schema_version": "1.0",
  "goal": "提交符合制度要求的报销申请",
  "steps": [
    {
      "step_id": "load_claim",
      "kind": "tool",
      "tool_id": "com.odap.tool.expense.get",
      "depends_on": [],
      "risk_level": "R0"
    },
    {
      "step_id": "validate_claim",
      "kind": "ontology_validation",
      "depends_on": ["load_claim"],
      "risk_level": "R0"
    },
    {
      "step_id": "submit_claim",
      "kind": "erp_operation",
      "operation": "expense.submit",
      "depends_on": ["validate_claim"],
      "risk_level": "R2"
    }
  ]
}
```

该示例用于说明结构，不代表上述工具或风险级别已经在 Registry 中实际注册。

计划中的 `risk_level` 不能由模型自行决定。Harness 应根据工具注册信息、操作类型、数据敏感度和企业策略重新计算或校验风险等级。

### 5.3 Plan Compiler 的处理流程

```
LLM Plan
   │
   ▼
Schema Validation
   │
   ▼
Tool / Operation Resolution
   │
   ▼
Dependency Graph Validation
   │
   ▼
Ontology & Business Constraint Check
   │
   ▼
Risk Classification
   │
   ▼
Authorization Pre-check
   │
   ▼
Execution Plan + Version Lock
```

验证至少包含以下内容：

1. Plan Schema 合法。
2. 所有 Tool、Agent 和 Operation 均存在。
3. 参数满足对应 Schema。
4. 依赖节点存在且不存在环。
5. 所有步骤都在当前任务授权范围内。
6. 数据访问范围符合租户和对象权限。
7. 写操作已声明副作用和幂等要求。
8. 高风险操作具备审批或拒绝路径。
9. 计划复杂度、步骤数量和总执行时间未超过限额。
10. 计划中使用的版本能够被固定并加载。

### 5.4 计划执行期间允许重新规划吗？

允许，但必须受控。

建议区分：

- 局部重规划：某个只读步骤失败后，重新生成替代查询方案。
- 全局重规划：原执行计划无法继续，需要重新生成整个剩余计划。
- 受限恢复：已执行过外部副作用，必须先确定其结果，才能生成后续计划。

重规划不得抹除历史执行记录，也不能让模型通过改变计划来绕过审批。

一旦执行计划发生实质变化，应重新检查相关工具能力、权限、业务约束和审批条件。涉及高风险写操作时，必须重新计算操作摘要；如果审批绑定的参数或操作含义发生变化，原审批不得继续生效。

## 6. Harness 执行循环与调度器

### 6.1 核心执行循环

Harness Worker 的职责不是“持续向模型提问直到它说完成”，而是根据持久化状态推进有限的执行步骤。

推荐的执行循环如下：

```
领取可执行任务
    │
    ▼
加载持久化状态与租约
    │
    ▼
检查取消、过期与暂停标志
    │
    ▼
选取满足依赖条件的步骤
    │
    ▼
构建最小化运行上下文
    │
    ▼
检查参数、版本与权限
    │
    ▼
创建持久化调用记录
    │
    ▼
执行 Agent / Tool / 验证器
    │
    ▼
持久化结果与检查点
    │
    ├── 继续下一步骤
    ├── 等待输入或审批
    ├── 安排重试
    ├── 进入结果对账
    └── 结束任务
```

每次执行循环都必须有明确的工作量上限，例如最大推进步数、单次 Worker 时间预算和单任务并发限制，防止一个任务无限占用资源。

### 6.2 调度策略

MVP 可以使用 PostgreSQL 实现任务领取。调度器优先选择：

1. 已到期的恢复与对账任务。
2. 已到达重试时间的步骤。
3. 正常可执行任务。
4. 低优先级后台分析任务。

高风险操作不能因为优先级高就跳过审批或授权。

建议在任务表中增加：

```
priority
available_at
lease_owner
lease_expires_at
attempt_count
max_attempts
cancel_requested_at
```

Worker 通过短期租约领取任务，租约过期后其他 Worker 才能接管。领取任务应采用原子条件更新，避免多个 Worker 同时执行同一任务。

示例 SQL（按队列类型分治，取消收敛与对账队列独立领取）：

```
WITH candidate AS (
    SELECT id
    FROM runtime.agent_runs
    WHERE tenant_id = $2
      AND status = ANY($3::text[])
      AND available_at <= now()
      AND (
          lease_expires_at IS NULL
          OR lease_expires_at < now()
      )
      AND NOT (cancel_requested_at IS NOT NULL AND status <> 'CANCELLING')
    ORDER BY priority DESC, available_at ASC
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE runtime.agent_runs r
SET lease_owner = $1,
    lease_expires_at = now() + interval '30 seconds',
    lease_epoch = r.lease_epoch + 1,
    updated_at = now()
FROM candidate c
WHERE r.id = c.id
RETURNING r.*;
```

`$3` 按队列传入不同的可调度状态集，四类队列互不混用：

| 队列 | 可领取状态 | 说明 |
|---|---|---|
| 执行队列 | `READY`、`RUNNING`、`RECOVERING` | 正常推进与恢复 |
| 取消收敛队列 | `CANCELLING` | 停止新步骤、等待在途操作收敛，见 §3.1 |
| 对账队列 | `REQUIRES_RECONCILIATION` | 按 `last_reconciliation_at` 独立扫描领取，处理未知外部结果 |
| 规划队列 | `READY`（仅规划 Worker） | 规划与执行可由不同 Worker 承担，但必须共用同一租约与 `lease_epoch` |

**租约续期**：长工具调用（LLM 推理、ERP 提交）可能超过 30 秒租约。Worker 必须提供续期接口（`POST /internal/runs/{id}/lease/renew`，携带当前 `lease_epoch`），在执行阻塞点前续期；续期失败即视为失去所有权，立即停止推进。租约时长应可配置，并对"续期失败 → 强制放弃 → 保留账本"路径做故障注入测试。

该 SQL 是实现示例。正式版本还需要将租户过滤、可调度状态、租约续期、取消标志和版本字段纳入并发控制（本示例已包含租户过滤、状态集参数化、取消标志与 `lease_epoch` 递增）。

此外，`SKIP LOCKED` 只解决任务领取时的行锁竞争，并不能防止一个租约已经失效的旧 Worker 继续运行。因此，还需要在后续写入中使用 fencing token 或等效的单调递增执行代次。

### 6.3 Fencing Token：防止过期 Worker 写入

假设 Worker A 获得任务租约后暂停，租约到期；Worker B 接管任务并继续执行。此时 A 恢复运行，如果它仍然能够修改任务状态，就可能覆盖 B 的新结果。

建议每次租约接管时递增 `lease_epoch`：

```
Run ID: run-001

Worker A: lease_epoch = 8
Worker B: lease_epoch = 9
```

所有状态更新必须携带当前 `lease_epoch`。旧 Worker 的写入因版本不匹配而被拒绝。

例如：

```
UPDATE runtime.agent_runs
SET status = $1,
    updated_at = now()
WHERE id = $2
  AND lease_owner = $3
  AND lease_epoch = $4;
```

如果影响行数为零，说明该 Worker 已失去所有权，必须停止推进任务并重新读取状态。

注意：数据库 fencing token 只能保护数据库中的状态更新。外部 ERP 操作还必须使用独立的幂等键、操作账本和对账机制。

## 7. 执行上下文与版本锁定

### 7.1 为什么需要 Context Builder

每次调用模型或工具时，都需要构建受控上下文。如果直接把整个任务历史、所有本体、全部工具描述和所有企业数据放入模型上下文，会造成成本高、信息混乱和数据泄露风险。

建议由 `Context Builder` 统一生成执行上下文。

上下文分为四层：

| 层次                | 内容                    |
| ----------------- | --------------------- |
| System Context    | 平台规则、模型调用限制、工具使用规范    |
| Execution Context | Run、Step、计划、当前状态与取消标志 |
| Semantic Context  | 固定版本的本体、相关查询结果与证据     |
| Business Context  | 当前任务所需的业务对象与最小必要字段    |

上下文中应包含版本引用、对象引用和证据 ID，而不是默认复制所有原始数据。

### 7.2 Execution Manifest

每个 Run 在进入执行阶段前，应生成不可随意修改的 `Execution Manifest`：

```
{
  "manifest_version": "1.0",
  "run_id": "run-example",
  "tenant_id": "tenant-example",
  "initiator_id": "user-example",
  "agent": {
    "id": "com.odap.agent.expense",
    "version": "1.0.0",
    "digest": "sha256:..."
  },
  "ontology_bindings": [
    {
      "id": "com.odap.ontology.expense",
      "version": "1.2.0",
      "digest": "sha256:..."
    }
  ],
  "policy_bundle_digest": "sha256:...",
  "tool_lock_digest": "sha256:...",
  "context_policy_version": "1.0",
  "created_at": "2026-10-12T10:00:00Z"
}
```

上述 ID 和摘要均为示例值。

Manifest 应记录本次执行所使用的 Agent、Ontology、Policy 和 Tool 版本。对于依赖模型服务的任务，还应记录模型提供方、模型版本标识、关键推理配置和可用的调用元数据。

需要明确：固定版本不等于保证 LLM 输出完全可重复。 模型服务可能更新底层实现，即使请求参数一致，生成结果也可能存在差异。因此，Harness 还必须保存实际输入摘要、输出记录、工具结果与决策轨迹。

### 7.3 运行中版本变化

建议遵循以下规则：

- 已启动 Run 默认继续使用锁定版本。
- 新版本发布不会自动替换正在运行的版本。
- 新任务只能使用满足启用策略的版本。
- 安全撤销可以阻止旧版本继续被调用。
- 版本被撤销后，运行中的高风险任务应进入暂停、恢复检查或人工审核流程。
- 不允许通过后台静默替换版本来维持任务运行。

## 8. 检查点与持久化恢复

### 8.1 检查点的作用

检查点用于记录任务在某个确定边界上的执行状态，使 Worker 崩溃后能够恢复，而不是从头执行全部步骤。

建议在以下时机创建检查点：

- 运行上下文固定后。
- 执行计划通过验证后。
- 每个步骤开始前。
- 每个步骤完成后。
- 提交外部操作前。
- 进入审批等待状态时。
- 完成外部操作对账后。
- 任务暂停或恢复时。

对于大量只读、短耗时步骤，可以根据成本合并部分检查点；对于写操作和审批边界，应保持严格的持久化记录。

### 8.2 检查点内容

```
{
  "checkpoint_version": "1.0",
  "run_id": "run-example",
  "step_id": "submit_claim",
  "sequence": 12,
  "state": "WAITING_APPROVAL",
  "plan_digest": "sha256:...",
  "context_digest": "sha256:...",
  "completed_steps": [
    "load_claim",
    "validate_claim"
  ],
  "pending_operations": [],
  "pending_approvals": [
    "approval-example"
  ],
  "created_at": "2026-10-12T10:05:00Z"
}
```

检查点不应无条件保存完整模型上下文或全部业务数据。大型输出可以保存在对象存储中，数据库中保存引用、摘要和必要的恢复元数据。

对于涉及敏感数据的上下文，还需要执行访问控制、加密、保留期限和删除策略。

### 8.3 恢复时不能盲目重放

恢复 Worker 必须先判断中断发生在哪个阶段：

| 中断阶段           | 恢复策略              |
| -------------- | ----------------- |
| 计划生成前          | 根据已保存上下文重新规划      |
| 只读工具执行前        | 重新执行前检查权限与输入      |
| 只读工具执行后但结果未持久化 | 根据调用契约决定重试或重新查询   |
| 外部写操作已准备但尚未发送  | 校验操作记录后再发送        |
| 外部写操作已发送但无回执   | 先对账，不得直接重复提交      |
| 等待审批           | 恢复审批状态，不重新生成原操作参数 |
| 审批后、执行前        | 重新校验审批绑定摘要与当前授权   |
| 任务完成后          | 返回已有完成结果，不重复执行副作用 |

这也是为什么“至少一次执行”不能直接等同于“业务操作可以重复执行”。

### 8.4 Outbox 与事件一致性

Harness 更新任务状态时，往往还需要发布事件或安排后续任务。例如，创建审批请求后，需要通知审批服务；外部操作提交后，需要安排对账任务。

建议采用事务型 Outbox：

1. 在同一数据库事务中更新业务状态并写入 Outbox。
2. 后台发布器读取待发送事件。
3. 发布成功后记录处理状态。
4. 消费者依据事件 ID 做幂等处理。

这样可以避免数据库已经提交、但事件尚未发布就发生进程崩溃的问题。

Outbox 能保证数据库内的状态与待发布事件保持一致，但不能自动保证远程 ERP 操作 exactly-once。外部系统仍需依赖适配器幂等、操作查询和对账机制。

## 9. 工具调用的统一执行协议

### 9.1 统一 Tool Invocation Request

Harness 不应针对每一种工具编写不同的执行循环。所有工具都应通过第四篇定义的插件协议调用，并由 Tool Runtime 统一处理。

建议请求结构如下：

```
{
  "protocol_version": "1.0",
  "invocation_id": "inv-example",
  "run_id": "run-example",
  "step_id": "load_claim",
  "tenant_id": "tenant-example",
  "plugin": {
    "id": "com.odap.tool.expense.get",
    "version": "1.0.0",
    "digest": "sha256:..."
  },
  "input": {
    "claim_id": "claim-example"
  },
  "deadline": "2026-10-12T10:01:00Z",
  "cancellation_token": "cancel-example",
  "trace_id": "trace-example"
}
```

该协议用于说明字段设计。正式实现中，取消令牌应采用不可预测、短期有效且绑定具体调用的凭据，不应把示例字符串直接用作生产安全令牌。

### 9.2 工具执行的强制门禁

每一次工具调用都必须经过：

1. 检查插件是否已发布、启用且未撤销。
2. 检查请求中的版本与 Registry 是否一致。
3. 校验输入 JSON Schema。
4. 检查当前 Run 是否允许继续执行。
5. 检查调用者身份与委托链。
6. 执行 Authorization Engine 检查。
7. 检查数据访问范围与工具能力。
8. 创建持久化调用记录。
9. 在隔离执行环境中调用工具。
10. 校验输出 Schema。
11. 保存结果、摘要与审计事件。
12. 根据结果推进 Step 和 Run 状态。

高风险操作不能只依赖创建 Run 时的授权结果。调用前仍需检查当前权限、审批状态和操作参数。

### 9.3 工具结果协议

建议将正常结果、业务拒绝和系统故障明确区分：

```
{
  "invocation_id": "inv-example",
  "status": "SUCCEEDED",
  "output": {
    "claim_id": "claim-example",
    "claim_amount": "1280.00"
  },
  "evidence_ids": [],
  "error": null
}
```

工具返回的 `SUCCEEDED` 只表示这次工具调用成功。它不自动表示业务目标已完成，更不代表后续 ERP 操作已经成功。

例如，查询工具成功返回报销单，与报销单通过业务验证，是两个不同结果；ERP 适配器成功接收请求，与 ERP 确认该操作已提交，也可能是两个不同结果。

## 10. 重试、超时与取消策略

### 10.1 错误分类

Harness 必须在重试前识别错误类型，不能把所有错误都交给模型自由决定。

| 错误类型                      | 默认处理             |
| ------------------------- | ---------------- |
| `INVALID_INPUT`           | 修正参数或向用户询问，不盲目重试 |
| `SCHEMA_VIOLATION`        | 拒绝输出并记录诊断        |
| `AUTHORIZATION_DENIED`    | 停止操作，不通过重试绕过     |
| `APPROVAL_REQUIRED`       | 进入审批流程           |
| `RATE_LIMITED`            | 按服务端提示与重试策略延迟    |
| `TRANSIENT_FAILURE`       | 在预算内退避重试         |
| `TIMEOUT_READ_ONLY`       | 根据查询安全性决定重新查询    |
| `TIMEOUT_SIDE_EFFECT`     | 先对账，再决定后续动作      |
| `VERSION_REVOKED`         | 阻止调用并进入恢复或人工处理   |
| `DEPENDENCY_UNAVAILABLE`  | 暂停相关步骤，按策略恢复     |
| `UNKNOWN_EXTERNAL_RESULT` | 进入结果对账           |

### 10.2 指数退避与重试预算

对于可安全重试的瞬时故障，可以采用指数退避与随机抖动：

\\[ d_n=\min(d\_{\max},d_0\cdot 2^n)+J_n \\]

其中：

- \\(d_0\\) 是初始等待时间。
- \\(d\_{\max}\\) 是最大退避时间。
- \\(J_n\\) 是随机抖动。
- \\(n\\) 是当前重试序号。

实际系统还应限制最大尝试次数、任务总耗时、总模型调用次数和总工具调用次数。

重试预算必须在任务级和工具级同时管理，防止模型反复生成等价调用，耗尽资源。

### 10.3 什么时候绝不能自动重试？

对于可能产生外部副作用的操作，只有在以下条件满足时才能重新提交：

- 已有操作记录可查询。
- 幂等键稳定且绑定同一业务意图。
- 适配器能够确认此前操作未执行，或者远端明确支持幂等。
- 当前权限仍然有效。
- 审批仍有效，且操作参数没有发生变化。
- 重试仍在业务允许的时间范围内。

如果无法确认远端操作是否已经执行，应优先进入 `RECONCILING`，而不是重新发起一次写操作。

### 10.4 取消语义

取消需要分为三个层次：

1. 取消请求：用户请求停止任务。
2. 停止后续调度：Harness 不再启动新的步骤。
3. 确认操作停止：当前工具或外部系统确认操作已经安全终止。

前两个动作通常可以很快完成，第三个动作可能无法保证。

例如，某个 ERP 付款操作已经提交，远端可能正在处理。Harness 可以停止后续工作，但不能声称付款已撤销。

建议 API 返回清晰的取消状态：

```
CANCEL_REQUESTED
STOPPING
CANCELLED
CANCEL_REQUIRES_RECONCILIATION
```

如果任务有未知的外部操作结果，应先完成对账或进入人工处理，再将任务归入最终终态。

## 11. 审批等待、恢复与权限重检

### 11.1 审批是持久化等待，不是阻塞 Worker

当操作需要人工审批时，Harness 应：

1. 持久化待审批操作。
2. 固定操作参数、目标对象、业务对象版本和摘要。
3. 创建审批请求。
4. 将 Run 设为 `WAITING_APPROVAL`。
5. 释放 Worker，不持续占用执行线程。
6. 等待审批事件。
7. 审批通过后重新检查授权、参数摘要和操作有效期。
8. 通过后恢复执行。

审批请求应绑定不可变操作摘要，例如：

```
approval_binding_digest =
  SHA256(
    canonical_operation_payload
    + target_object_version
    + policy_bundle_digest
    + ontology_binding_digest
  )
```

这是概念示例。生产实现应使用明确版本的规范化编码方式，确保同一业务参数不会因为 JSON 字段顺序或数字格式不同而得到不一致的摘要。

### 11.2 审批通过后为什么仍需重新授权？

因为审批通过与当前执行权限不是同一个事实。

审批完成后可能发生：

- 发起者的权限被撤销。
- 执行 Agent 版本被禁用。
- 本体或策略版本因安全问题被撤销。
- 业务对象状态发生变化。
- 操作参数被修改。
- 审批超过有效期。
- 当前用户不再具有执行资格。

因此，恢复执行时至少需要重新检查：

- 当前主体与委托链。
- 当前资源和操作范围。
- 审批状态及审批人资格。
- 操作摘要是否一致。
- 业务对象版本是否仍然符合条件。
- 当前策略是否允许继续。
- 本体约束是否仍满足。

若重新授权失败，不能因为“之前已经审批过”就继续执行。

### 11.3 防止自我审批

审批系统应遵循第三篇定义的职责分离规则。除非企业明确配置并允许某种受控例外，否则不能让同一主体既发起高风险操作又批准该操作。

Agent 的意见、模型的自评或系统自动生成的“审批建议”，不能冒充人类审批决定。

## 12. 外部操作对账与恢复

这一部分是 Harness 最需要认真实现的机制。

### 12.1 为什么单靠数据库事务不够？

PostgreSQL 可以保证本地状态更新的事务一致性，但不能把本地数据库事务和外部 ERP 的事务天然合并成一个原子事务。

例如：

1. Harness 创建 ERP 操作记录。
2. 适配器向 ERP 发送付款请求。
3. ERP 成功处理付款。
4. 网络连接断开，回执未到达。
5. Harness 无法判断付款是否已经发生。

如果此时把操作标记为失败并直接重试，就可能造成重复付款。

### 12.2 ERP Operation Ledger

每个外部副作用都必须有独立操作账本，建议保存：

```
operation_id
tenant_id
run_id
step_id
adapter_id
operation_type
target_object_id
target_object_version
idempotency_key
payload_digest
authorization_decision_id
approval_id
status
remote_reference
remote_receipt
dispatch_attempt_count
last_reconciliation_at
created_at
updated_at
```

操作记录应在发送外部请求之前持久化。

幂等键应在同一业务意图的重试过程中保持稳定。不能每次重试都创建一个新的幂等键，否则远端无法识别这是同一个操作。

### 12.3 对账流程

\#chatgpt-mermaid-\_r_2ft\_{font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";font-size:16px;fill:rgb(237, 237, 237);}@keyframes edge-animation-frame{from{stroke-dashoffset:0;}}@keyframes dash{to{stroke-dashoffset:0;}}#chatgpt-mermaid-\_r_2ft\_ .edge-animation-slow{stroke-dasharray:9,5!important;stroke-dashoffset:900;animation:dash 50s linear infinite;stroke-linecap:round;}#chatgpt-mermaid-\_r_2ft\_ .edge-animation-fast{stroke-dasharray:9,5!important;stroke-dashoffset:900;animation:dash 20s linear infinite;stroke-linecap:round;}#chatgpt-mermaid-\_r_2ft\_ .error-icon{fill:rgb(48, 48, 48);}#chatgpt-mermaid-\_r_2ft\_ .error-text{fill:rgb(237, 237, 237);stroke:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_2ft\_ .edge-thickness-normal{stroke-width:1px;}#chatgpt-mermaid-\_r_2ft\_ .edge-thickness-thick{stroke-width:3.5px;}#chatgpt-mermaid-\_r_2ft\_ .edge-pattern-solid{stroke-dasharray:0;}#chatgpt-mermaid-\_r_2ft\_ .edge-thickness-invisible{stroke-width:0;fill:none;}#chatgpt-mermaid-\_r_2ft\_ .edge-pattern-dashed{stroke-dasharray:3;}#chatgpt-mermaid-\_r_2ft\_ .edge-pattern-dotted{stroke-dasharray:2;}#chatgpt-mermaid-\_r_2ft\_ .marker{fill:rgb(175, 175, 175);stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_2ft\_ .marker.cross{stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_2ft\_ svg{font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";font-size:16px;}#chatgpt-mermaid-\_r_2ft\_ p{margin:0;}#chatgpt-mermaid-\_r_2ft\_ .label{font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";color:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_2ft\_ .cluster-label text{fill:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_2ft\_ .cluster-label span{color:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_2ft\_ .cluster-label span p{background-color:transparent;}#chatgpt-mermaid-\_r_2ft\_ .label text,#chatgpt-mermaid-\_r_2ft\_ span{fill:rgb(237, 237, 237);color:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_2ft\_ .node rect,#chatgpt-mermaid-\_r_2ft\_ .node circle,#chatgpt-mermaid-\_r_2ft\_ .node ellipse,#chatgpt-mermaid-\_r_2ft\_ .node polygon,#chatgpt-mermaid-\_r_2ft\_ .node path{fill:rgb(9, 23, 44);stroke:rgb(31, 78, 148);stroke-width:1px;}#chatgpt-mermaid-\_r_2ft\_ .rough-node .label text,#chatgpt-mermaid-\_r_2ft\_ .node .label text,#chatgpt-mermaid-\_r_2ft\_ .image-shape .label,#chatgpt-mermaid-\_r_2ft\_ .icon-shape .label{text-anchor:middle;}#chatgpt-mermaid-\_r_2ft\_ .node .katex path{fill:#000;stroke:#000;stroke-width:1px;}#chatgpt-mermaid-\_r_2ft\_ .rough-node .label,#chatgpt-mermaid-\_r_2ft\_ .node .label,#chatgpt-mermaid-\_r_2ft\_ .image-shape .label,#chatgpt-mermaid-\_r_2ft\_ .icon-shape .label{text-align:center;}#chatgpt-mermaid-\_r_2ft\_ .node.clickable{cursor:pointer;}#chatgpt-mermaid-\_r_2ft\_ .root .anchor path{fill:rgb(175, 175, 175)!important;stroke-width:0;stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_2ft\_ .arrowheadPath{fill:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_2ft\_ .edgePath .path{stroke:rgb(175, 175, 175);stroke-width:1px;}#chatgpt-mermaid-\_r_2ft\_ .flowchart-link{stroke:rgb(175, 175, 175);fill:none;}#chatgpt-mermaid-\_r_2ft\_ .edgeLabel{background-color:rgb(0, 0, 0);text-align:center;}#chatgpt-mermaid-\_r_2ft\_ .edgeLabel p{background-color:rgb(0, 0, 0);}#chatgpt-mermaid-\_r_2ft\_ .edgeLabel rect{opacity:0.5;background-color:rgb(0, 0, 0);fill:rgb(0, 0, 0);}#chatgpt-mermaid-\_r_2ft\_ .labelBkg{background-color:rgba(0, 0, 0, 0.5);}#chatgpt-mermaid-\_r_2ft\_ .cluster rect{fill:rgb(48, 48, 48);stroke:rgba(255, 255, 255, 0.15);stroke-width:1px;}#chatgpt-mermaid-\_r_2ft\_ .cluster text{fill:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_2ft\_ .cluster span{color:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_2ft\_ div.mermaidTooltip{position:absolute;text-align:center;max-width:200px;padding:2px;font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";font-size:12px;background:rgb(48, 48, 48);border:1px solid rgba(255, 255, 255, 0.15);border-radius:2px;pointer-events:none;z-index:100;}#chatgpt-mermaid-\_r_2ft\_ .flowchartTitleText{text-anchor:middle;font-size:18px;fill:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_2ft\_ rect.text{fill:none;stroke-width:0;}#chatgpt-mermaid-\_r_2ft\_ .icon-shape,#chatgpt-mermaid-\_r_2ft\_ .image-shape{background-color:rgb(0, 0, 0);text-align:center;}#chatgpt-mermaid-\_r_2ft\_ .icon-shape p,#chatgpt-mermaid-\_r_2ft\_ .image-shape p{background-color:rgb(0, 0, 0);padding:2px;}#chatgpt-mermaid-\_r_2ft\_ .icon-shape .label rect,#chatgpt-mermaid-\_r_2ft\_ .image-shape .label rect{opacity:0.5;background-color:rgb(0, 0, 0);fill:rgb(0, 0, 0);}#chatgpt-mermaid-\_r_2ft\_ .label-icon{display:inline-block;height:1em;overflow:visible;vertical-align:-0.125em;}#chatgpt-mermaid-\_r_2ft\_ .node .label-icon path{fill:currentColor;stroke:revert;stroke-width:revert;}#chatgpt-mermaid-\_r_2ft\_ .node .neo-node{stroke:rgb(31, 78, 148);}#chatgpt-mermaid-\_r_2ft\_ [data-look="neo"].node rect,#chatgpt-mermaid-\_r_2ft\_ [data-look="neo"].cluster rect,#chatgpt-mermaid-\_r_2ft\_ [data-look="neo"].node polygon{stroke:url(#chatgpt-mermaid-\_r_2ft\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_2ft\_ [data-look="neo"].swimlane.cluster rect{filter:none;}#chatgpt-mermaid-\_r_2ft\_ [data-look="neo"].node path{stroke:url(#chatgpt-mermaid-\_r_2ft\_-gradient);stroke-width:1px;}#chatgpt-mermaid-\_r_2ft\_ [data-look="neo"].node .outer-path{filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_2ft\_ [data-look="neo"].node .neo-line path{stroke:rgb(31, 78, 148);filter:none;}#chatgpt-mermaid-\_r_2ft\_ [data-look="neo"].node circle{stroke:url(#chatgpt-mermaid-\_r_2ft\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_2ft\_ [data-look="neo"].node circle .state-start{fill:#000000;}#chatgpt-mermaid-\_r_2ft\_ [data-look="neo"].icon-shape .icon{fill:url(#chatgpt-mermaid-\_r_2ft\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_2ft\_ [data-look="neo"].icon-shape .icon-neo path{stroke:url(#chatgpt-mermaid-\_r_2ft\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_2ft\_ .node text{font-size:14px;font-weight:600;letter-spacing:normal;fill:rgb(153, 206, 255);}#chatgpt-mermaid-\_r_2ft\_ .edgeLabels text{font-size:13px;font-weight:600;letter-spacing:-0.08px;fill:rgb(153, 206, 255);}#chatgpt-mermaid-\_r_2ft\_ .node tspan[font-weight="normal"],#chatgpt-mermaid-\_r_2ft\_ .edgeLabels tspan[font-weight="normal"]{font-weight:600;}#chatgpt-mermaid-\_r_2ft\_ .edgeLabel .label rect{opacity:1;rx:13px;ry:13px;fill:rgb(0, 14, 26);stroke:rgb(26, 62, 95);stroke-width:1px;}#chatgpt-mermaid-\_r_2ft\_ .node rect,#chatgpt-mermaid-\_r_2ft\_ .node circle,#chatgpt-mermaid-\_r_2ft\_ .node ellipse,#chatgpt-mermaid-\_r_2ft\_ .node polygon,#chatgpt-mermaid-\_r_2ft\_ .node path{fill:rgb(0, 40, 77);stroke:rgba(255, 255, 255, 0.1);stroke-width:1px;}#chatgpt-mermaid-\_r_2ft\_ .node rect{rx:16px;ry:16px;}#chatgpt-mermaid-\_r_2ft\_ .node.mermaid-decision .label-container{fill:rgb(0, 14, 26);stroke:rgb(26, 62, 95);stroke-dasharray:2,2;}#chatgpt-mermaid-\_r_2ft\_ .edgePaths .flowchart-link{stroke:rgb(175, 175, 175);stroke-width:1px;stroke-linecap:round;stroke-linejoin:round;}#chatgpt-mermaid-\_r_2ft\_ .marker{fill:rgb(175, 175, 175);stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_2ft\_ :root{--mermaid-font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";}External operationdispatchedReceipt received?Validate receiptReceipt confirms success?Mark SUCCEEDEDRecord confirmed failureMark UNKNOWNQuery remote operationstatusRemote result known?Persist receipt and reconcileSafe retry permitted?Retry with sameidempotency keyManual reviewBackoff and reconcile againYesYesNoNoSuccessNot executedYesNoStill unknown

这张图表达的是一般恢复路径。具体 ERP 适配器可能没有查询操作状态的接口；此时应根据远端系统的能力声明，将操作转入人工核对或企业定义的补偿流程。

### 12.4 补偿操作不等于回滚

对于已经完成的外部操作，某些业务可以通过另一笔操作进行补偿，例如冲销、撤回申请或创建更正单。

但补偿操作与数据库回滚不同：

- 补偿操作本身可能失败。
- 补偿操作可能需要再次审批。
- 原始操作仍保留在 ERP 审计记录中。
- 补偿可能改变业务状态，但不一定消除原始影响。

因此，Harness 应将补偿作为新的受控业务操作执行，保留与原操作的关联，而不是直接删除原始执行记录。

## 13. PostgreSQL 持久化模型

前两篇已经建立 Run、Step、Tool Call、Checkpoint、Approval、ERP Operation 和 Audit 的基础模型。本篇建议在此基础上补充状态机和调度需要的字段，而不是另建一套互不关联的任务表。

### 13.1 Run 表关键字段

```
ALTER TABLE runtime.agent_runs
    ADD COLUMN IF NOT EXISTS state_version BIGINT
        NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS lease_owner TEXT,
    ADD COLUMN IF NOT EXISTS lease_epoch BIGINT
        NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS lease_expires_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS available_at TIMESTAMPTZ
        NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS cancel_requested_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS deadline_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS execution_manifest_digest TEXT,
    ADD COLUMN IF NOT EXISTS plan_digest TEXT;
```

这些字段用于实现：

- `state_version`：乐观并发控制。
- `lease_owner`：当前 Worker 标识。
- `lease_epoch`：防止旧 Worker 继续写入。
- `lease_expires_at`：任务租约期限。
- `available_at`：任务何时重新具备调度资格。
- `cancel_requested_at`：持久化取消请求。
- `deadline_at`：任务的总执行期限。
- `execution_manifest_digest`：执行版本清单摘要。
- `plan_digest`：当前执行计划摘要。

正式迁移时，还应检查原表是否已有同名字段、旧状态值和现有索引。生产数据库变更应采用可回滚的迁移方案，并避免在高负载时执行未经评估的大表锁操作。

### 13.2 Run 事件表

建议增加独立事件表，用于状态变更追踪和故障诊断：

```
CREATE TABLE runtime.run_events (
    id              UUID PRIMARY KEY,
    tenant_id       UUID NOT NULL,
    run_id          UUID NOT NULL
        REFERENCES runtime.agent_runs(id),
    sequence_no     BIGINT NOT NULL,
    event_type      TEXT NOT NULL,
    from_state      TEXT,
    to_state        TEXT,
    actor_type      TEXT NOT NULL,
    actor_id        TEXT,
    lease_epoch     BIGINT,
    payload_digest  TEXT,
    payload         JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (run_id, sequence_no)
);

CREATE INDEX idx_run_events_tenant_time
ON runtime.run_events (tenant_id, created_at DESC);
```

对于正式部署，事件表应纳入前两篇设计的租户隔离与 RLS 规则。事件记录应通过受控服务追加，普通运行角色不应具有任意修改或删除历史事件的权限。

事件中的 `payload` 应只保存必要的状态变化信息，不应默认记录完整模型上下文、认证凭据或所有敏感业务数据。

### 13.3 原子状态转换

Run 状态更新、事件写入和 Outbox 写入应尽可能在同一数据库事务中完成。

例如：

```
BEGIN;

UPDATE runtime.agent_runs
SET status = $1,
    state_version = state_version + 1,
    updated_at = now()
WHERE id = $2
  AND state_version = $3
  AND lease_owner = $4
  AND lease_epoch = $5;

-- 应用程序必须检查 UPDATE 影响行数。
-- 只有更新成功，才继续插入对应事件和 Outbox。

INSERT INTO runtime.run_events (
    id,
    tenant_id,
    run_id,
    sequence_no,
    event_type,
    from_state,
    to_state,
    actor_type,
    actor_id,
    lease_epoch
)
VALUES (
    $6, $7, $2, $8, $9, $10, $1,
    'worker', $4, $5
);

COMMIT;
```

该示例省略了 Outbox 插入语句与部分字段。正式实现应在同一事务中插入 Outbox 记录，并确保事件序号通过原子机制生成。

如果 `UPDATE` 影响行数为零，应用必须回滚事务，并重新读取当前状态，不能继续插入一条虚假的成功事件。

### 13.4 事件日志与当前状态的关系

建议同时保留：

- 当前状态字段：便于快速查询和调度。
- 事件日志：用于审计与恢复分析。
- 检查点：用于恢复执行上下文。
- 外部操作账本：用于处理 ERP 副作用与对账。

这四者职责不同，不能只保存事件日志就假设能够无损恢复全部任务。模型生成结果、工具输出、外部回执和关键输入摘要仍需要保存为可引用的持久化制品。

## 14. Go 状态机实现

### 14.1 状态和事件类型

```
package harness

type RunState string

const (
    RunCreated                 RunState = "CREATED"
    RunContextBuilding         RunState = "CONTEXT_BUILDING"
    RunReady                   RunState = "READY"
    RunPlanning                RunState = "PLANNING"
    RunPlanValidating          RunState = "PLAN_VALIDATING"
    RunRunning                 RunState = "RUNNING"
    RunWaitingInput            RunState = "WAITING_INPUT"
    RunWaitingApproval         RunState = "WAITING_APPROVAL"
    RunPaused                  RunState = "PAUSED"
    RunRecovering              RunState = "RECOVERING"
    RunRequiresReconciliation  RunState = "REQUIRES_RECONCILIATION"
    RunManualReview            RunState = "MANUAL_REVIEW"
    RunCompleted               RunState = "COMPLETED"
    RunFailed                  RunState = "FAILED"
    RunCancelled               RunState = "CANCELLED"
    RunExpired                 RunState = "EXPIRED"
)

type RunEventType string

const (
    EventContextBuildStarted   RunEventType = "ContextBuildStarted"
    EventContextBuildSuccess   RunEventType = "ContextBuildSucceeded"
    EventContextBuildFailed    RunEventType = "ContextBuildFailed"
    EventPlanGenerated         RunEventType = "PlanGenerated"
    EventPlanValidated         RunEventType = "PlanValidationSucceeded"
    EventPlanValidationFailed  RunEventType = "PlanValidationFailed"
    EventPlanRegeneration      RunEventType = "PlanRegenerationRequested"
    EventStepStarted           RunEventType = "StepStarted"
    EventStepSucceeded         RunEventType = "StepSucceeded"
    EventStepFailed            RunEventType = "StepFailed"
    EventInputRequested        RunEventType = "InputRequested"
    EventInputReceived         RunEventType = "InputReceived"
    EventInputDeadline         RunEventType = "InputDeadlineExceeded"
    EventApprovalRequested     RunEventType = "ApprovalRequested"
    EventApprovalResolved      RunEventType = "ApprovalResolved"
    EventApprovalRejected      RunEventType = "ApprovalRejected"
    EventApprovalExpired       RunEventType = "ApprovalExpired"
    EventRunPaused             RunEventType = "RunPaused"
    EventRunResumed            RunEventType = "RunResumed"
    EventCancelRequested       RunEventType = "CancellationRequested"
    EventCancelConverged       RunEventType = "CancelConverged"
    EventRecoveryStarted       RunEventType = "RecoveryStarted"
    EventRecoveryCompleted     RunEventType = "RecoveryCompleted"
    EventExternalUnknown       RunEventType = "ExternalOperationUnknown"
    EventReconciliationDone    RunEventType = "ReconciliationSucceeded"
    EventReconciliationFailed  RunEventType = "ReconciliationFailed"
    EventManualReview          RunEventType = "ManualInterventionRequired"
    EventManualReviewResolved  RunEventType = "ManualReviewResolved"
    EventRunCompleted          RunEventType = "RunCompleted"
    EventRunFailed             RunEventType = "RunFailed"
    EventRunExpired            RunEventType = "RunExpired"
)
```

状态机应当是一个纯业务组件：它判断当前状态和事件是否构成合法转换，不直接调用模型、工具或 ERP。取消、完成等条件判断所需的运行时事实（是否存在在途外部操作、是否所有步骤已终态等）由 Application Service 预先计算，通过 `TransitionFacts` 传入——状态机因此保持可测试的纯函数性质，同时避免"无条件取消"这类缺陷。

### 14.2 转换矩阵（权威定义）

```
type TransitionFacts struct {
    HasInflightExternalOps  bool     // 存在 DISPATCHED/EXECUTING/UNKNOWN 的 ERP 操作
    AllStepsTerminal        bool     // 所有 Step 已到终态
    HasUnknownToolCalls     bool     // 存在 UNKNOWN/RECONCILING 的工具调用
    OutputContractValidated bool     // 最终输出已通过契约校验
    Cancelling              bool     // 取消收敛进行中（对账结果去向的依据）
    ResumeState             RunState // 恢复/暂停前的目标状态
    RetryBudgetLeft         bool     // 计划重生成预算未耗尽
}
```

完整转换矩阵（`-` 表示非法转换，返回 `invalid transition` 错误）。列标题为事件常量名，行标题为状态常量名：

| 当前状态 \ 事件 | Context Build Started | Context Build Succeeded | Context Build Failed | Plan Generated | Plan Validation Succeeded | Plan Validation Failed | Plan Regeneration Requested | Step Started | Step Succeeded | Step Failed | Input Requested | Input Received | Input Deadline Exceeded | Approval Requested | Approval Resolved | Approval Rejected | Approval Expired | Run Paused | Run Resumed | Cancellation Requested | Cancel Converged | Recovery Started | Recovery Completed | External Operation Unknown | Reconciliation Succeeded | Reconciliation Failed | Manual Intervention Required | Manual Review Resolved | Run Completed | Run Failed | Run Expired |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| `CREATED` | `CONTEXT_BUILDING` | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | `CANCELLING` | - | - | - | - | - | - | - | - | - | - | `EXPIRED` |
| `CONTEXT_BUILDING` | - | `READY` | `FAILED` | - | - | - | - | - | - | - | - | - | - | - | - | - | - | `PAUSED` | - | `CANCELLING` | - | `RECOVERING` | - | `REQUIRES_RECONCILIATION` | - | - | - | - | - | `FAILED` | `EXPIRED` |
| `READY` | - | - | - | `PLANNING` | - | - | - | - | - | - | - | - | - | - | - | - | - | `PAUSED` | - | `CANCELLING` | - | `RECOVERING` | - | - | - | - | - | - | - | `FAILED` | `EXPIRED` |
| `PLANNING` | - | - | - | - | `PLAN_VALIDATING` | - | - | - | - | - | - | - | - | - | - | - | - | `PAUSED` | - | `CANCELLING` | - | `RECOVERING` | - | - | - | - | - | - | - | `FAILED` | `EXPIRED` |
| `PLAN_VALIDATING` | - | - | - | - | `RUNNING` | `FAILED` † | `READY` ‡ | - | - | - | - | - | - | - | - | - | - | `PAUSED` | - | `CANCELLING` | - | `RECOVERING` | - | - | - | - | - | - | - | `FAILED` | `EXPIRED` |
| `RUNNING` | - | - | - | - | - | - | - | `RUNNING` | `RUNNING` | `RUNNING` § | `WAITING_INPUT` | - | - | `WAITING_APPROVAL` | - | - | - | `PAUSED` | - | `CANCELLING` | - | `RECOVERING` | - | `REQUIRES_RECONCILIATION` | - | - | `MANUAL_REVIEW` | - | `COMPLETED` ¶ | `FAILED` | `EXPIRED` |
| `WAITING_INPUT` | - | - | - | - | - | - | - | - | - | - | - | `RUNNING` | `FAILED` ‖ | - | - | - | - | `PAUSED` | - | `CANCELLING` | - | `RECOVERING` | - | `REQUIRES_RECONCILIATION` | - | - | `MANUAL_REVIEW` | - | - | `FAILED` | `EXPIRED` |
| `WAITING_APPROVAL` | - | - | - | - | - | - | - | - | - | - | - | - | - | - | `RUNNING` | `FAILED` ‖ | `FAILED` ‖ | `PAUSED` | - | `CANCELLING` | - | `RECOVERING` | - | `REQUIRES_RECONCILIATION` | - | - | `MANUAL_REVIEW` | - | - | `FAILED` | `EXPIRED` |
| `PAUSED` | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | `RUNNING` / `facts.ResumeState` | `CANCELLING` | - | `RECOVERING` | - | - | - | - | `MANUAL_REVIEW` | - | - | `FAILED` | `EXPIRED` |
| `RECOVERING` | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | `PAUSED` | - | `CANCELLING` | - | - | `RUNNING` / `facts.ResumeState` | `REQUIRES_RECONCILIATION` | - | - | `MANUAL_REVIEW` | - | `COMPLETED` ¶ | `FAILED` | - |
| `CANCELLING` | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | `PAUSED` | - | - | `CANCELLED` | - | - | `REQUIRES_RECONCILIATION` | `CANCELLED` ‖ | `MANUAL_REVIEW` | `MANUAL_REVIEW` | `MANUAL_REVIEW` | - | - | - |
| `REQUIRES_RECONCILIATION` | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | `CANCELLING` | - | `RECOVERING` | - | - | `RUNNING` / `COMPLETED` ¶ | `MANUAL_REVIEW` | `MANUAL_REVIEW` | - | `COMPLETED` ¶ | `FAILED` | - |
| `MANUAL_REVIEW` | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | `CANCELLING` | - | - | - | - | - | - | - | `RUNNING` / `FAILED` / `CANCELLED` | - | - | - |
| `COMPLETED` | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - |
| `FAILED` | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - |
| `CANCELLED` | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - |
| `EXPIRED` | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - | - |

守卫条件（`facts` 不满足时转换必须被拒绝，并记录 `guard_rejected` 审计事件）：

- † `PlanValidationFailed → FAILED`：仅当 `RetryBudgetLeft = false`；否则应走 `PlanRegenerationRequested → READY` 重新规划。
- ‡ `PlanRegenerationRequested → READY`：仅当 `RetryBudgetLeft = true`，且计划指针回退到上一已校验版本。
- § `StepFailed` 在 Run 层保持 `RUNNING`（重试语义在 Step 状态机处理）；仅当应用服务判定不可继续时才发 `RunFailed`。
- ¶ `RunCompleted → COMPLETED` 要求 `AllStepsTerminal && !HasUnknownToolCalls && !HasInflightExternalOps && OutputContractValidated` 全部成立；缺任何一项都必须先走对账或人工路径。`ReconciliationSucceeded → COMPLETED` 同样要求该组合成立，否则回到 `RUNNING` 继续未完成步骤。
- ‖ 涉及 `WAITING_APPROVAL`/`WAITING_INPUT` 的拒绝、过期、超时转换：若 `HasInflightExternalOps = true`，禁止直达 `FAILED`，必须先转 `REQUIRES_RECONCILIATION`。`ReconciliationSucceeded → CANCELLED` 仅在 `facts.Cancelling = true` 时合法。
- `RunExpired → EXPIRED`：若 `HasInflightExternalOps = true`，定时器只能发 `ExternalOperationUnknown`（转对账），不得直接置 `EXPIRED`（与 §3.2 一致）。
- 所有进入 `CANCELLING` 的转换都不要求在途操作为空；所有离开 `CANCELLING` 的转换都必须经过在途操作检查。

基于矩阵的表驱动实现（消除手写 switch 的遗漏风险）：

```
type Transition struct {
    From  RunState
    Event RunEventType
    To    func(f TransitionFacts) (RunState, bool)
}

var transitions = []Transition{
    {RunCreated, EventContextBuildStarted, func(f TransitionFacts) (RunState, bool) {
        return RunContextBuilding, true
    }},
    {RunRunning, EventCancelRequested, func(f TransitionFacts) (RunState, bool) {
        return RunCancelling, true
    }},
    {RunCancelling, EventCancelConverged, func(f TransitionFacts) (RunState, bool) {
        if f.HasInflightExternalOps {
            return RunCancelling, false
        }
        return RunCancelled, true
    }},
    {RunRunning, EventRunCompleted, func(f TransitionFacts) (RunState, bool) {
        if f.AllStepsTerminal && !f.HasUnknownToolCalls &&
            !f.HasInflightExternalOps && f.OutputContractValidated {
            return RunCompleted, true
        }
        return RunRunning, false
    }},
    // …矩阵中其余每一行对应一个条目，由代码生成或人工对照矩阵登记
}

func NextState(
    current RunState,
    event RunEventType,
    facts TransitionFacts,
) (RunState, error) {
    for _, t := range transitions {
        if t.From == current && t.Event == event {
            next, ok := t.To(facts)
            if !ok {
                return current, fmt.Errorf(
                    "transition guard rejected: state=%s event=%s facts=%+v",
                    current, event, facts,
                )
            }
            return next, nil
        }
    }
    return current, fmt.Errorf(
        "invalid transition: state=%s event=%s",
        current, event,
    )
}
```

工程约束：

1. `transitions` 表与上面的矩阵必须一一对应。矩阵以表驱动数据声明，测试从矩阵自动生成用例，逐格覆盖合法与非法转换；矩阵新增一行而实现未登记时测试必须失败（见 §19.1）。
2. `TransitionFacts` 由 Application Service 在同一个事务内计算并随事件持久化（`facts_digest`），保证任何状态转换都可重放为"状态 + 事件 + 事实"三元组，供审计与恢复复盘。
3. 终态（`COMPLETED`/`FAILED`/`CANCELLED`/`EXPIRED`）不接受任何出边转换；重新发起必须创建新 Run 并通过幂等键关联旧 Run。

#### 14.2.1 取消与对账的竞争条件

取消请求与正在执行的副作用之间的竞争是最高风险路径，必须显式设计：

1. `CancellationRequested` 只做三件事：置 `cancel_requested_at`、停止接纳新步骤、发事件进入 `CANCELLING`。它**不**停止已经 dispatch 到 ERP 的调用。
2. 取消收敛 Worker 按 §6.2 的取消收敛队列扫描 `CANCELLING` 且存在 `DISPATCHED/EXECUTING/UNKNOWN` 外部操作的 Run，等待其收敛；对每个未知操作按第七篇幂等键查询远端结果。
3. 全部外部操作收敛后发 `CancelConverged`（无未知操作）或 `ReconciliationSucceeded`（`facts.Cancelling = true`）进入 `CANCELLED`。
4. 收敛超时（`cancel_convergence_deadline`，默认取 `max(step deadline) + 对账窗口`）未完成 → `MANUAL_REVIEW`，并触发告警。

#### 14.2.2 四层状态机的层间不变量

以下不变量由恢复 Worker 巡检断言，违反即告警并转 `MANUAL_REVIEW`（不得自动修复）：

| # | 不变量 |
|---|---|
| I1 | Run ∈ 终态 ⟹ 所有 Step ∈ 终态，且不存在 `RUNNING`/`UNKNOWN` 的 Invocation |
| I2 | Run = `COMPLETED` ⟹ 不存在 `UNKNOWN`/`RECONCILING` 的 External Operation |
| I3 | Run = `CANCELLED`/`EXPIRED` ⟹ 不存在 `DISPATCHED`/`EXECUTING` 的 External Operation |
| I4 | Step = `WAITING_APPROVAL` ⟹ 存在状态为 `PENDING` 的 Approval，且 Run ∈ {`RUNNING`, `WAITING_APPROVAL`} |
| I5 | `lease_owner` 非空的 Run 必须存在未过期的 `lease_expires_at`；两者不一致即租约损坏 |
| I6 | 同一 `run_id` 同一时刻只允许一个 Worker 持有租约（`lease_epoch` 单调递增，无回退） |

#### 14.2.3 恢复扫描与巡检

恢复 Worker 按固定周期执行：租约过期扫描（`lease_expires_at < now()` 且非终态 → `RecoveryStarted`）、未知外部操作扫描（`REQUIRES_RECONCILIATION` 且超过对账窗口未推进 → 升级处理）、孤儿 Step 扫描（Step 非终态但 Run 已终态 → 违反 I1，告警）、不变量全量巡检（I1–I6）。巡检结果写入审计，不自动改状态。

同时，事件本身也不能单独证明转换条件已经满足。`RunCompleted` 事件必须由经过检查的应用服务生成，而不能由模型或插件直接提交。

### 14.3 Application Service

建议将状态机调用与数据库持久化统一封装：

```
type RunRepository interface {
    Load(ctx context.Context, id string) (*Run, error)
    CompareAndSwap(
        ctx context.Context,
        run *Run,
        expectedVersion int64,
    ) error
    AppendEvent(ctx context.Context, event RunEvent) error
}

type TransitionService struct {
    Runs  RunRepository
    Guard TransitionGuard
}

func (s *TransitionService) Apply(
    ctx context.Context,
    runID string,
    event RunEvent,
) error {
    run, err := s.Runs.Load(ctx, runID)
    if err != nil {
        return err
    }

    if err := s.Guard.Check(ctx, run, event); err != nil {
        return err
    }

    next, err := NextState(run.State, event.Type)
    if err != nil {
        return err
    }

    expectedVersion := run.StateVersion
    run.State = next
    run.StateVersion++

    // 生产实现应将状态更新、事件写入和 Outbox
    // 封装在同一个数据库事务中。
    return s.Runs.CompareAndSwap(
        ctx, run, expectedVersion,
    )
}
```

这里的 `TransitionGuard` 用于检查租约、权限、审批、未完成操作、任务期限等业务前置条件。

生产实现应使用事务型 Repository 方法，一次性完成状态更新、事件追加和 Outbox 插入；不应将这些操作拆成多个独立事务后再依赖补偿来维持一致性。

## 15. Harness 对外 API 设计

建议统一使用 `/api/v1/runs` 管理任务，避免 Agent、工具、审批和前端分别维护不同的任务状态接口。

| 方法     | API                           | 作用             |
| ------ | ----------------------------- | -------------- |
| `POST` | `/api/v1/runs`                | 创建任务           |
| `GET`  | `/api/v1/runs/{id}`           | 查询任务状态         |
| `GET`  | `/api/v1/runs/{id}/events`    | 查询执行事件         |
| `GET`  | `/api/v1/runs/{id}/steps`     | 查询步骤列表         |
| `GET`  | `/api/v1/runs/{id}/evidence`  | 查询证据引用         |
| `POST` | `/api/v1/runs/{id}/pause`     | 请求暂停           |
| `POST` | `/api/v1/runs/{id}/resume`    | 恢复任务           |
| `POST` | `/api/v1/runs/{id}/cancel`    | 请求取消           |
| `POST` | `/api/v1/runs/{id}/retry`     | 对符合条件的失败步骤发起重试 |
| `POST` | `/api/v1/runs/{id}/reconcile` | 触发外部操作对账       |
| `POST` | `/api/v1/runs/{id}/input`     | 提交待补充信息        |

创建任务时建议支持客户端幂等键：

```
POST /api/v1/runs
Idempotency-Key: client-request-unique-key
```

服务器应在租户和调用者作用域内，对该键建立唯一约束，并校验请求摘要。相同键、相同请求摘要应返回原任务；相同键、不同请求摘要应返回冲突，而不是创建第二个任务。

### 15.1 创建任务请求

```
{
  "agent_id": "com.odap.agent.expense",
  "goal": "检查报销单并按制度提交申请",
  "input": {
    "expense_claim_id": "claim-example"
  },
  "execution_policy": {
    "max_steps": 30,
    "deadline_seconds": 600,
    "require_approval_for_write": true
  }
}
```

`execution_policy` 只能在企业允许的范围内由调用者指定。客户端不能通过提交 `require_approval_for_write: false` 来关闭企业强制审批策略。

服务器应计算最终生效策略，并将其版本和摘要绑定到 Execution Manifest。

### 15.2 状态查询

建议返回：

```
{
  "run_id": "run-example",
  "status": "WAITING_APPROVAL",
  "current_step": "submit_claim",
  "progress": {
    "completed_steps": 2,
    "total_steps": 3
  },
  "pending_approval_id": "approval-example",
  "reconciliation_required": false,
  "created_at": "2026-10-12T10:00:00Z",
  "updated_at": "2026-10-12T10:05:00Z"
}
```

进度字段只应作为展示信息，不能代替真正的状态判断。比如 `2/3` 并不代表任务一定可以安全继续，剩余步骤可能涉及高风险审批或未知的外部副作用。

### 15.3 等待态超时与升级链

`WAITING_APPROVAL` 与 `WAITING_INPUT` 必须有明确的时间出口，禁止无限期等待。每个审批/输入任务携带 `deadline_at`，超时处理按固定链推进：

```
deadline 临近（T-25%）  → 提醒审批人/请求人（通知中心 + 审计事件）
deadline 过半（T-50%）  → 升级至备用审批人或请求人的上级（按第三篇升级策略）
deadline 到期           → 按策略自动拒绝（默认）：ApprovalExpired / InputDeadlineExceeded
```

关键约束：

1. **过期不等于无条件失败**。若 `facts.HasInflightExternalOps = true`，`ApprovalExpired`/`InputDeadlineExceeded` 必须按矩阵守卫先转 `REQUIRES_RECONCILIATION`，完成对账后才能进 `FAILED`。已批准但未执行的写操作随审批任务一并失效，需重新审批。
2. 自动拒绝的默认行为可由租户策略改为"升级至人工"，但不得配置为"默认放行"——超时放行等于绕过审批。
3. 超时扫描由恢复 Worker 统一执行（与租约扫描同周期），扫描 SQL 与 §6.2 队列分治，避免长事务。
4. 提醒、升级、拒绝三个动作都产生审计事件，关联原审批任务与 Run。

## 16. 与 ODAP 其他模块的集成契约

Harness 是执行控制中枢，但不应成为所有业务能力的实现者。它通过明确的接口连接其他模块。

### 16.1 关键接口

| 接口                     | 核心职责                      |
| ---------------------- | ------------------------- |
| `AgentInvoker`         | 请求 Agent 生成计划、分析结果或提出修订方案 |
| `ToolInvoker`          | 调用经过注册、审核和授权的工具           |
| `OntologyService`      | 查询本体、验证数据、获取语义证据          |
| `AuthorizationService` | 判断操作是否允许、是否需要审批           |
| `ApprovalService`      | 创建、查询和处理审批请求              |
| `ERPPort`              | 提交外部操作、查询操作状态和核验回执        |
| `CheckpointRepository` | 保存检查点与恢复元数据               |
| `AuditService`         | 记录关键事件和决策                 |
| `PolicyService`        | 获取执行限制、风险分类和重试策略          |

### 16.2 统一执行上下文

建议 Harness 为每次执行构造统一上下文：

```
type ExecutionContext struct {
    RunID          string
    TenantID       string
    InitiatorID    string
    EffectiveID    string
    AgentVersionID string
    ManifestDigest string
    PlanDigest     string
    TraceID        string
    Deadline       time.Time
}
```

这只是基础字段示例。正式实现还应携带委托链、租户成员关系版本、策略绑定、取消信号和必要的资源访问范围。

上下文中的主体身份由平台可信服务生成，不允许 LLM 通过修改 JSON 字段更换身份。

### 16.3 Harness 与 Ontology Runtime

Harness 调用 Ontology Runtime 时，应明确指定：

- 本体版本和摘要。
- 数据图或快照版本。
- 查询类型和参数。
- 当前租户和授权范围。
- 需要返回的证据级别。
- 查询时间与结果规模限制。

Ontology Runtime 返回的 `FAIL`、`UNKNOWN` 或 `INCOMPLETE` 必须由 Harness 根据任务策略处理。不能为了让任务继续执行，就把所有非成功状态都转换成“验证通过”。

### 16.4 Harness 与 Authorization Engine

权限检查至少发生在三个时间点：

1. 计划阶段：排除明显不允许的操作。
2. 工具调用阶段：执行前检查实际资源、参数和能力。
3. 外部副作用提交阶段：对高风险操作再次检查权限、审批和操作摘要。

计划阶段的授权预检查只是筛选，不应作为之后写操作的永久授权凭据。

### 16.5 Harness 与 ERP Port

Harness 不直接依赖具体 ERP 的 HTTP 接口。它向 ERP Port 提交统一的操作请求，由 Adapter 完成协议转换、幂等处理、远端调用和回执标准化。

统一执行链路为：

```
Agent
  ↓
Harness Plan / State Machine
  ↓
Authorization + Approval
  ↓
Tool Runtime
  ↓
ERP Port
  ↓
ERP Adapter
  ↓
Mock ERP / Real ERP
  ↓
Receipt + Reconciliation
  ↓
Harness State Update
```

对于只读查询，可以采用更轻量的执行路径；但所有外部写操作必须经过操作账本与回执处理。

## 17. 运行安全与资源治理

### 17.1 执行预算

每个 Run 应具有可配置且由平台强制执行的资源预算：

```
max_steps
max_agent_calls
max_tool_calls
max_retries
max_wall_clock_duration
max_context_tokens
max_output_bytes
max_concurrent_invocations
max_external_operations
```

对于不同业务域，可以使用不同的预算配置。例如，批量分析任务可以允许较多只读查询，但高风险业务写操作应采用更严格的调用次数、并发和执行期限限制。

### 17.2 任务优先级与公平调度

私有化企业部署中，不同部门可能同时提交大量任务。建议采用租户、组织、任务优先级和资源配额联合调度。

需要避免：

- 单一租户占满全部 Worker。
- 某个长时间运行的 Agent 阻塞其他任务。
- 模型调用异常造成大量重试。
- 恢复任务长期得不到调度。
- 高风险操作因排队过久而绕过正常审批流程。

建议为恢复与对账任务保留一定的调度能力，但不应无限提高其优先级，以免正常任务被永久饿死。

### 17.3 插件隔离

Harness 不能把“插件已注册”理解为“插件可信”。

建议执行环境至少满足：

- 插件以独立进程或容器运行。
- 默认无直接 ERP 网络访问权限。
- 文件系统采用最小权限挂载。
- 限制 CPU、内存、进程数和执行时长。
- 外部网络访问通过受控出口策略。
- 插件只能使用平台注册并获批的工具。
- 插件输出必须经过 Schema 校验。
- 插件崩溃不能直接破坏 Harness 主进程。
- 插件不能直接修改平台数据库中的运行状态。

MVP 可以从容器隔离开始；如果运行不可信的第三方插件，应进一步评估更强的沙箱机制。

## 18. 可观测性、审计与故障诊断

### 18.1 全链路关联 ID

每一次 Run 应形成统一的关联链：

```
run_id
  ├── plan_id
  ├── step_id
  │    ├── invocation_id
  │    ├── authorization_decision_id
  │    ├── approval_id
  │    ├── evidence_bundle_id
  │    └── erp_operation_id
  └── checkpoint_id
```

通过这些 ID，管理员应能从一次业务任务追溯到模型调用、工具参数、权限决定、审批记录和 ERP 回执。

### 18.2 核心指标

| 指标                                        | 目的               |
| ----------------------------------------- | ---------------- |
| `harness_runs_total`                      | 统计任务总量及结果        |
| `harness_run_duration_seconds`            | 观察端到端任务耗时        |
| `harness_step_duration_seconds`           | 定位慢步骤            |
| `harness_queue_wait_seconds`              | 监控调度积压           |
| `harness_retries_total`                   | 观察重试情况           |
| `harness_recoveries_total`                | 监控恢复次数           |
| `harness_unknown_operations_total`        | 监控外部结果未知问题       |
| `harness_reconciliation_duration_seconds` | 观察对账效率           |
| `harness_approval_wait_seconds`           | 观察审批等待时间         |
| `harness_stale_worker_writes_total`       | 监控过期 Worker 写入冲突 |
| `harness_budget_exceeded_total`           | 监控资源预算超限         |

指标标签应避免直接包含 Run ID、用户 ID 等高基数字段。详细关联信息放在日志和 Trace 中。

### 18.3 管理控制台必须支持的能力

- 查看 Run 的当前状态、进度和历史事件。
- 查看计划、依赖关系和每个步骤的输入输出摘要。
- 查看授权决定及其理由。
- 查看等待中的审批。
- 查看未知 ERP 操作与对账进度。
- 安全地暂停、恢复或请求取消任务。
- 查看失败原因和恢复建议。
- 对需要人工处理的任务记录处置意见。
- 检查执行时绑定的 Agent、Ontology、Tool 和 Policy 版本。

人工操作也必须进行权限检查并记录审计。管理员界面不能直接修改数据库中的状态字段来“修复”任务。

## 19. 测试策略与验收标准

Harness 的测试重点不是单纯验证正常流程，而是验证并发、崩溃、超时、权限变化与外部副作用条件下的正确性。

### 19.1 状态机测试

必须覆盖：

- 所有合法状态转换。
- 所有非法状态转换。
- 任务完成前置条件。
- 取消与暂停的竞争条件。
- 审批通过与取消同时发生。
- 任务过期与 ERP 操作执行同时发生。
- 旧 Worker 恢复后尝试写入。
- 重复事件与重复回调。
- 不同事件到达顺序下的恢复逻辑。

### 19.2 故障注入测试

建议模拟以下场景：

1. Worker 在调用工具前崩溃。
2. Worker 在工具成功后、结果持久化前崩溃。
3. Worker 在 ERP 请求发出后、收到回执前崩溃。
4. PostgreSQL 短暂不可用。
5. Ontology Runtime 超时。
6. 授权服务不可用。
7. 审批在执行前被撤销。
8. ERP 适配器重复收到同一请求。
9. 两个 Worker 同时尝试推进同一个 Run。
10. 外部操作长期无法确定结果。
11. 运行中的本体版本因安全问题被撤销。
12. 用户在关键写操作期间请求取消。

### 19.3 关键不变量

建议将以下规则固化为自动化测试：

- 一个 Run 同一时刻只能有一个有效推进租约。
- 过期 Worker 不能覆盖新 Worker 的状态。
- 同一个业务意图重试时，幂等键保持稳定。
- 未经授权的工具调用不能执行。
- 审批绑定摘要变化后，原审批不能继续生效。
- 任务完成前不存在未处理的必需步骤。
- 存在未知外部操作时，不能把任务直接标记为普通失败或取消完成。
- 外部写操作成功必须有可信回执或可靠对账证据。
- 恢复执行不能静默替换已固定的依赖版本。
- 所有关键状态变化都有可追踪的事件记录。

### 19.4 MVP 验收标准

| 验收项      | 完成标准                                   |
| -------- | -------------------------------------- |
| 状态机      | Run、Step、Invocation、ERP Operation 分层管理 |
| 持久化      | 任务状态、事件、检查点和操作账本均可恢复                   |
| 并发控制     | 多 Worker 下无重复推进造成的状态覆盖                 |
| 权限集成     | 工具调用与外部写操作均经过授权检查                      |
| 审批       | 支持暂停、审批、恢复及审批失效处理                      |
| 取消       | 区分取消请求、停止调度与外部操作终止                     |
| 对账       | 超时后先查询结果，不盲目重试写操作                      |
| 版本固定     | 每个 Run 保存执行清单和关键制品摘要                   |
| 故障恢复     | Worker 重启后可恢复未完成任务                     |
| 审计       | 关键状态转换、审批、调用和回执可追踪                     |
| Mock ERP | 完成端到端读写模拟与异常注入测试                       |
| 安全       | 无未解决的严重权限绕过或重复副作用问题                    |

## 20. 工程实施计划与代码结构

### 20.1 推荐开发顺序

建议先完成确定性执行内核，再接入复杂的 Agent 规划能力。否则，模型可以生成各种计划，却没有可靠机制保证执行安全。

1. 阶段一：状态机与持久化

   实现 Run、Step、Invocation、External Operation 的状态模型，完成数据库迁移、事件日志、状态转换校验和并发控制。
2. 阶段二：Worker 与任务调度

   实现任务领取、租约续期、Fencing Token、任务优先级、超时处理和恢复扫描。
3. 阶段三：计划与工具执行

   实现 Plan Schema、依赖图校验、工具调用协议、参数验证、输出验证和资源预算。
4. 阶段四：权限、审批与本体集成

   接入 Authorization Engine、Approval Service 和 Ontology Runtime，建立运行时版本绑定与高风险操作门禁。
5. 阶段五：ERP 对账与故障恢复

   实现操作账本、稳定幂等键、外部回执、未知结果处理和人工接管。
6. 阶段六：端到端验收

   通过 Mock ERP 验证报销场景，执行并发测试、崩溃恢复、权限撤销和重复请求测试。

### 20.2 建议补充的代码目录

在原有 ODAP 仓库结构中，增加以下目录：

```
internal/harness/
├── domain/
│   ├── run.go
│   ├── state.go
│   ├── step.go
│   ├── invocation.go
│   ├── operation.go
│   └── events.go
├── application/
│   ├── create_run.go
│   ├── execute_run.go
│   ├── transition_run.go
│   ├── approval_handler.go
│   └── cancel_run.go
├── engine/
│   ├── scheduler.go
│   ├── state_machine.go
│   ├── plan_compiler.go
│   ├── plan_validator.go
│   └── execution_coordinator.go
├── context/
│   ├── builder.go
│   └── manifest.go
├── checkpoint/
│   ├── manager.go
│   └── repository.go
├── recovery/
│   ├── worker.go
│   ├── lease.go
│   └── reconciler.go
├── ports/
│   ├── agent.go
│   ├── tool.go
│   ├── ontology.go
│   ├── authorization.go
│   ├── approval.go
│   └── erp.go
└── tests/
    ├── state_machine_test.go
    ├── concurrency_test.go
    ├── recovery_test.go
    ├── approval_test.go
    └── reconciliation_test.go
```

数据库方面，优先复用第二篇定义的表和 Schema；新增字段及索引应通过迁移管理，避免另起一套重复的 Run、审批和 ERP 操作数据模型。

## 21. 本篇最终架构决策

建议将以下决策纳入 ODAP 的正式架构规范。

| 领域    | 最终决策                                      |
| ----- | ----------------------------------------- |
| 执行内核  | Go 实现持久化 Harness                          |
| 状态机   | Run、Step、Invocation、External Operation 分层 |
| 调度    | PostgreSQL 任务领取 + 租约 + Fencing Token      |
| 并发控制  | 乐观版本校验与事务型状态更新                            |
| 计划执行  | LLM 生成候选计划，Compiler 校验后执行                 |
| 上下文   | Context Builder 按需构建最小必要上下文               |
| 版本治理  | 每个 Run 固定 Execution Manifest              |
| 检查点   | 关键执行边界持久化                                 |
| 事件一致性 | 事务型 Outbox                                |
| 工具调用  | 统一协议、Schema 校验、隔离运行                       |
| 授权    | 计划预检查 + 调用时检查 + 写操作前重检                    |
| 审批    | 操作摘要绑定、恢复前重新检查                            |
| 重试    | 区分只读调用和有副作用的操作                            |
| 外部副作用 | 操作账本、稳定幂等键、回执与对账                          |
| 取消    | 区分取消请求、停止调度与外部终止                          |
| 故障恢复  | 恢复 Worker + 对账 Worker + 人工处理              |
| 可观测性  | 统一事件、Trace、审计和证据引用                        |

## 22. 第六篇完成后的系统能力

至此，ODAP 的核心链路已经具备较完整的设计基础：

- 第一篇：总体架构与技术选型。
- 第二篇：核心数据模型与数据库 Schema。
- 第三篇：企业身份认证与权限引擎。
- 第四篇：插件协议与 Registry 注册中心。
- 第五篇：Ontology Runtime 与 Ontology Factory。
- 第六篇：Harness 执行引擎与状态机。

这六篇共同建立了平台的核心闭环：

Ontology

理解企业业务语义与制度约束

Agent

提出计划、分析证据、生成候选决策

Harness

验证计划、控制状态、调度工具、暂停与恢复

Authorization + Approval

决定谁可以执行什么操作，以及何时必须审批

ERP Port + Adapter

执行外部业务操作，核验回执并处理未知结果

Audit + Evidence

保存完整决策链、执行记录和可复核证据


