# ODAP 第八篇：Agent Runtime、多智能体协作与 Agent 生命周期管理详细设计

版本：V1.0 · 技术基线：Go + React/TypeScript + PostgreSQL · 部署模式：企业私有化部署

> 范围标签：[MVP-V1.0] §2/§5/§10/§11 · [V1.5] §7 多智能体协作 · [V2] §17 深度委派

## 1. 设计目标与核心原则

ODAP（Ontology-Driven Agent Platform）的 Agent 不应只是一个调用大模型、再依次执行工具的聊天机器人，而应当是一个受到企业本体约束、由 Harness 控制执行、受权限引擎管辖、通过 ERP Port 完成业务操作，并且全程可审计的业务执行主体。

例如，员工提交一张差旅报销单，ODAP 可以组织多个 Agent 协作：

- 报销处理 Agent：理解用户意图，收集报销单信息。
- 制度分析 Agent：依据已发布的企业本体和制度规则判断业务要求。
- 数据核验 Agent：核对员工、部门、预算、金额和凭证。
- 风险审查 Agent：识别异常金额、重复报销和潜在风险。
- 流程规划 Agent：生成满足约束的执行计划。
- 结果验证 Agent：检查计划执行结果、ERP 回执和证据完整性。

但必须明确一个关键边界：这些 Agent 可以提出判断、生成计划和请求调用工具，不能因为其他 Agent 同意，就自动获得授权或绕过审批。权限引擎、本体验证、审批服务和 ERP Operation Gateway 仍然是独立的控制边界。

### 1.1 本篇建设范围

| 模块                        | 核心职责                      |
| ------------------------- | ------------------------- |
| Agent Registry            | Agent 定义、版本、能力和发布状态       |
| Agent Runtime             | 加载 Agent、执行推理任务、管理上下文与资源  |
| Multi-Agent Orchestrator  | 多 Agent 调度、依赖管理、结果汇总与冲突处理 |
| Agent Harness Integration | 接入第六篇的执行状态机、检查点和恢复机制      |
| Context Builder           | 组装任务、本体、权限、工具及证据上下文       |
| Agent Evaluation          | 测试、评估、灰度发布和版本回滚           |
| Agent Governance          | 生命周期治理、权限边界、审计和风险控制       |

本篇不重新实现已有的 Harness、Ontology Runtime、权限引擎和 ERP Gateway，而是定义 Agent Runtime 如何与它们协同。

## 2. 总体架构

业务入口层

Web UI · API · 定时任务 · 外部系统事件

Agent Orchestrator

任务分解 · 依赖调度 · 冲突处理 · 结果聚合

Planner Agent

生成候选计划

Domain Agents

执行领域分析

Risk Agent

识别风险与冲突

Verifier Agent

检查结果与证据

Harness + Agent Runtime

状态机 · 上下文 · 预算 · 工具调用 · 检查点 · 恢复

Ontology Runtime

语义查询、约束验证、证据

Policy & Approval

授权、审批、策略评估

Tool Registry → ERP Operation Gateway → ERP Port

受控工具执行、操作账本、回执与对账

这套架构中有三个必须区分的概念：

1. Agent 是决策与分析单元：负责理解、推理、提出候选方案。
2. Orchestrator 是协作调度器：负责谁在何时执行、依赖什么结果、何时结束。
3. Harness 是受控执行机制：负责步骤状态、工具调用、检查点、审批暂停、恢复和失败处理。

不能把这三者混成一个无限循环的 LLM 调用函数，否则很难保证可恢复性、权限边界和执行结果的可解释性。

## 3. Agent 的分类与角色体系

ODAP 不应把所有 Agent 设计成同一种模板。建议按照职责将其划分为六类。

| 类型       | 主要职责                  | 典型输出                   |
| -------- | --------------------- | ---------------------- |
| Planner  | 把业务目标转换成候选任务图         | Execution Plan         |
| Domain   | 处理某个业务领域              | Domain Result          |
| Analyst  | 分析数据、提取规律和解释异常        | Analysis Report        |
| Critic   | 检查假设、规则冲突和遗漏          | Critique Report        |
| Executor | 在 Harness 授权下请求执行具体步骤 | Tool/Operation Request |
| Verifier | 检查结果、约束和证据是否满足要求      | Verification Report    |

同一个 Agent 可以拥有多种能力，但应在 Manifest 中明确声明其职责与权限，而不是仅凭 Prompt 中的一句描述来控制安全边界。

### 3.1 角色之间的协作规则

- Planner 可以提出执行计划，但不能自行批准计划。
- Domain Agent 可以生成业务结论，但不能修改已发布的正式本体。
- Critic 可以否定或质疑其他 Agent 的结论，但不能自行决定权限。
- Executor 只能请求其已获授权的工具；真正的调用仍由 Harness 和工具执行层控制。
- Verifier 应尽可能通过确定性校验、本体验证和回执检查验证结果，而非仅让另一个模型评价模型输出。
- Orchestrator 负责决定是否继续、补充证据、转人工或终止任务。

尤其需要注意：Verifier 不能因为输出了“验证通过”，就把未经验证的业务事实变成可信事实。 验证报告必须记录验证对象、检查规则、结果、来源和缺失信息。

## 4. Agent Manifest：定义 Agent 的标准协议

Agent 必须是可注册、可版本化、可测试、可发布、可撤销的标准化组件。

建议采用 YAML 作为人工编写格式，在注册时转换为经过 Schema 验证的内部结构。

### 4.1 Manifest 示例

以下为报销处理 Agent 的示例。它描述的是平台组件的声明，不代表已经获得生产环境权限。

```

apiVersion: odap.io/v1
kind: AgentPlugin

metadata:
  id: finance.expense-processor
  name: Expense Processor
  displayName: 报销处理智能体
  version: 1.0.0
  publisher: odap-builtin
  description: 负责收集、整理和处理报销业务请求

spec:
  role: domain
  domain: finance.expense
  entrypoint:
    runtime: container
    protocol: odap-agent-v1
    endpoint: /invoke

  capabilities:
    - expense.read
    - expense.prepare
    - ontology.query
    - ontology.validate

  inputs:
    schemaRef: schemas/expense-input.schema.json

  outputs:
    schemaRef: schemas/expense-output.schema.json

  bindings:
    ontology:
      package: finance.expense
      versionConstraint: ">=1.0.0 <2.0.0"
      requirePublished: true

    tools:
      - finance.expense.read
      - finance.expense.prepare

    erp:
      mode: request-only
      allowedOperations:
        - expense.create_draft

  context:
    maxItems: 100
    includeSourceReferences: true
    includePolicyVersion: true

  execution:
    timeoutSeconds: 60
    maxModelCalls: 8
    maxToolCalls: 12
    maxRetries: 1
    maxOutputBytes: 262144

  security:
    networkPolicy: deny-by-default
    secretAccess: none
    requireTenantContext: true

  governance:
    riskLevel: medium
    evaluationProfile: finance-expense-v1
    humanReviewRequiredForPublication: true

```

### 4.2 Manifest 的设计要点

第一，能力声明不等于权限授予。

`capabilities` 说明 Agent 在设计上可能执行哪些类型的任务；具体用户、租户、业务对象、数据范围和操作是否允许，仍由权限引擎根据运行时上下文判断。

第二，版本约束不能替代版本锁定。

`versionConstraint` 只用于解析候选版本。一次正式任务启动时，平台必须将其解析为确切的 Agent、本体、规则包、工具和映射版本，并写入 Run 的版本快照。任务执行中不得悄悄切换版本。

第三，Agent 不应直接持有 ERP 凭据。

Agent 输出结构化的业务意图或操作请求；Harness、权限引擎与 ERP Gateway 负责授权、凭据使用、远端调用和回执核验。

第四，Manifest 必须受 Schema 与策略双重检查。

即使 YAML 语法正确，也可能声明越权能力、不兼容的本体版本或不安全的运行方式。因此，注册时需要同时进行格式验证、依赖检查、安全策略检查和能力审查。

## 5. Agent 生命周期管理

Agent 的生命周期不应只是“上传插件”和“删除插件”两个动作，而应覆盖开发、验证、发布、运行、升级与退役。

### 5.1 生命周期状态机

DRAFT

草稿与开发

VALIDATING

Manifest、依赖、安全和接口验证

TESTING

功能测试、评估集、回归测试

REVIEW

业务负责人、安全负责人审核

PUBLISHED

不可变的已发布版本

ACTIVE

允许被新任务选用

暂停调度：SUSPENDED

停止使用：RETIRED

安全撤销：REVOKED

状态转换必须通过服务端命令完成，并进行主体授权、版本检查、审计记录与并发控制。

建议区分以下语义：

- `PUBLISHED`：该版本已经通过发布流程，但不一定被调度器选用。
- `ACTIVE`：该版本被允许参与新任务的版本解析。
- `SUSPENDED`：临时禁止新任务使用，可以保留已有任务以供受控处置。
- `RETIRED`：正常停止使用，历史记录仍可查询。
- `REVOKED`：由于安全、合规或重大缺陷而撤销；必须进一步决定在途任务是否暂停、终止或转人工。

撤销不等于自动取消已经完成的 ERP 操作。 如果某个 Agent 在撤销前已经提交了 ERP 操作，平台必须继续查询操作账本、验证回执并完成对账。

### 5.2 发布与运行分离

建议建立两套独立流程：

1. 发布流程：构建制品 → 校验 → 测试 → 审核 → 发布 → 激活。
2. 运行流程：解析版本 → 创建运行快照 → 组装上下文 → 执行 → 验证 → 记录结果。

这样可以避免某个 Agent 更新后，正在运行的任务突然改变行为。

## 6. Agent Runtime 的核心接口

Agent Runtime 是 Agent 执行的统一入口。它负责把一次调用变成具有明确输入、输出、资源上限和证据记录的可追踪执行单元。

### 6.1 运行上下文

每次 Agent 调用都应携带一个受控的 `AgentContext`。

```

package agentruntime

import (
    "context"
    "time"
)

type RunIdentity struct {
    TenantID string
    RunID    string
    StepID   string
    TraceID  string
}

type VersionLock struct {
    AgentVersion       string
    OntologyDigest     string
    PolicyBundleDigest string
    ToolsetDigest      string
    MappingDigest      string
}

type Budget struct {
    Deadline       time.Time
    MaxModelCalls  int
    MaxToolCalls   int
    MaxOutputBytes int64
}

type AgentContext struct {
    Identity   RunIdentity
    Versions   VersionLock
    Budget     Budget
    Principal  PrincipalContext
    Input      StructuredInput
    Evidence   EvidenceReferences
}

type PrincipalContext struct {
    ActorID      string
    ActorType    string
    Organization string
    ScopeRef     string
}

type StructuredInput struct {
    SchemaRef string
    Data      []byte
}

type EvidenceReferences struct {
    SourceIDs []string
}

type AgentOutput struct {
    Status       string
    Result       []byte
    EvidenceIDs  []string
    Diagnostics  []Diagnostic
    TokenUsage   TokenUsage
}

type Diagnostic struct {
    Code    string
    Message string
}

type TokenUsage struct {
    InputTokens  int64
    OutputTokens int64
}

type Agent interface {
    Invoke(
        ctx context.Context,
        input AgentContext,
    ) (AgentOutput, error)
}

```

这是接口草案，正式实现还需补齐 Schema 验证、敏感字段策略、预算扣减与错误类型。

关键设计要求：

- `TenantID`、用户身份和授权上下文由可信服务注入，不能直接信任模型生成的字段。
- `VersionLock` 应在运行启动时解析并固定，不能由 Agent 自行修改。
- `EvidenceReferences` 只允许引用当前租户和当前任务可访问的证据。
- `Deadline` 必须传播到模型客户端、工具调用和下游服务。
- 输出必须经过大小限制和结构化校验。
- Agent 不能自行报告并决定自己的权限结果；授权决策由独立权限引擎产生。

### 6.2 Agent 执行协议

统一调用协议建议包含以下阶段：

1. Prepare：验证输入、主体上下文、版本锁定和预算。
2. Build Context：加载任务相关事实、制度、本体与历史证据。
3. Invoke：调用模型或确定性逻辑。
4. Validate Output：验证输出 Schema、引用和业务约束。
5. Record Evidence：记录输入摘要、输出摘要、使用的版本和工具引用。
6. Return Result：向 Orchestrator 返回结构化结果。

模型请求与响应的原文可能包含敏感信息，因此审计不应默认无限制保存全部提示词、思维链或原始业务数据。优先保存可复核的结构化结论、必要输入摘要、引用、规则轨迹和调用元数据，并依据企业保留策略处理原始内容。

## 7. 多智能体协作与调度引擎

ODAP 的多 Agent 系统不应默认采用“所有 Agent 互相聊天，直到达成一致”的模式。无约束的群聊容易造成重复推理、上下文膨胀、执行顺序不清和责任无法追溯。

建议以有向无环任务图（DAG）为默认调度模式，有限状态机为执行控制，事件消息为协作载体。只有在确实需要反复研究、补充证据或修订计划时，才允许进入受限的循环。

### 7.1 三种协作模式

[airflow architecture components | Medium](https://images.openai.com/static-rsc-4/pA_P7iZAgxLms2mx2D9dlYfv-7cB6QFv0uWAkJspCqlYppuw7hNUZ4YuVyG3hEoX10hk5IDNFpMKQhmpQ_c7qAMCgxNjgvcstXf43RyMifqlX6maE03ObOdR_rvC1U3S6X9oGD9zIVySSD6NgZpKrfikSvSrmF1x0I5s8SNWRFw?purpose=inline)

模式 A：流水线协作

适合标准业务流程。

Planner → Domain Agent → Verifier → Harness。每一步依赖上一步的结构化输出，便于测试、恢复和审计。

[Настройки в интерфейсе: как проектировать сложные параметры без перегруза | Блог HireHi](https://images.openai.com/static-rsc-4/yB8CGFSRSnzZTOxlQg0EN97fpE4mwFTpTdbQczPEyy98QPOl2cK1_HncE3TQ8miWLXJ1sGPg0-6BAd8MHmv9VFVw3ODyivC7aYvjOlRSNJMENvd4VvtXN7mwNQj5I02arhtEojXd0u0fCGj7TjuZ3-iyArlwkq5zYb_oKg5CpiA?purpose=inline)

模式 B：并行协作

适合相互独立的核验任务。

数据核验、制度核验和风险审查并行执行，随后由聚合器合并结果。共享输入应使用相同版本快照，避免不同 Agent 根据不同事实作出判断。

[Agentic Design Patterns: Navigating the Future of AI Engineering | by Dr. Sanjay Kumar | Medium](https://images.openai.com/static-rsc-4/btHRUz2ioztXn3mCEcvXdfhIyEG0Pdmo0CVoWBrXHo_UAs8WzNNC4ndDBjJf1y6byjsd7Uga4C6_Yv3CGWPbQtik1etuhIZmj_CVpBN4ITxgLsyZAs9h0HCbaZF1tW6f1J7nfzOGsxzU7hUewdM8aDHF0giK-XzvZ-OUM_wcI10?purpose=inline)

模式 C：有限复议循环

适合复杂问题和规则冲突。

初步结论 → Critic 提出异议 → 补充证据 → 重新验证。循环次数、调用预算和终止条件必须事先受控。

循环体内动态生成的迭代计划必须重新提交 Plan Compiler 编译，并对包含历史迭代节点的全图执行环检测，通过后才能进入 Harness；不得把循环当作绕过计划编译的通道（§8.2）。

默认采用 A 和 B。C 必须设定 `max_iterations`、截止时间、剩余预算和明确的退出条件。

### 7.2 Execution Plan 数据结构

Planner 生成的不是自由文本待办事项，而是可校验、可持久化的执行计划。

```
{
  "plan_id": "plan_01",
  "goal": "核验并准备一张报销单",
  "tasks": [
    {
      "task_id": "check_employee",
      "agent_ref": "finance.employee-checker@1.0.0",
      "depends_on": [],
      "input_ref": "expense_input",
      "output_ref": "employee_check",
      "retry_policy": "bounded_read_retry"
    },
    {
      "task_id": "check_policy",
      "agent_ref": "finance.policy-reviewer@1.0.0",
      "depends_on": [],
      "input_ref": "expense_input",
      "output_ref": "policy_check",
      "retry_policy": "bounded_read_retry"
    },
    {
      "task_id": "verify_expense",
      "agent_ref": "finance.expense-verifier@1.0.0",
      "depends_on": [
        "check_employee",
        "check_policy"
      ],
      "input_ref": "expense_input",
      "output_ref": "verification_result",
      "retry_policy": "none"
    }
  ],
  "limits": {
    "max_parallel_tasks": 3,
    "max_iterations": 2,
    "deadline_seconds": 120
  }
}
```

该示例中的 Agent 名称和版本是演示性标识。实际解析必须检查它们是否存在、是否处于可用状态，以及是否与当前本体、工具和策略版本兼容。

### 7.3 计划编译器

建议将 Planner 的输出交给一个独立的 `Plan Compiler`，依次检查：

1. JSON Schema 和字段完整性。
2. Agent 是否注册、已发布且允许被当前租户使用。
3. 依赖是否存在、是否形成环路。
4. 输入输出 Schema 是否兼容。
5. 工具和本体依赖是否满足要求。
6. 是否存在不允许的执行步骤或高风险操作。
7. 并行度、截止时间和总预算是否超限。
8. 需要人工审批的步骤是否配置了暂停和恢复路径。

模型提出的计划只能成为候选计划，编译通过后才可以进入 Harness。 编译通过也不代表业务执行已经获得授权。

### 7.4 Orchestrator 接口

```
type TaskStatus string

const (
    TaskPending   TaskStatus = "PENDING"
    TaskReady     TaskStatus = "READY"
    TaskRunning   TaskStatus = "RUNNING"
    TaskSucceeded TaskStatus = "SUCCEEDED"
    TaskFailed    TaskStatus = "FAILED"
    TaskBlocked   TaskStatus = "BLOCKED"
    TaskCancelled TaskStatus = "CANCELLED"
)

type TaskSpec struct {
    TaskID     string
    AgentRef   string
    DependsOn  []string
    InputRef   string
    OutputRef  string
}

type CompiledPlan struct {
    PlanID string
    Tasks  []TaskSpec
}

type Orchestrator interface {
    Compile(
        ctx context.Context,
        candidate []byte,
    ) (CompiledPlan, error)

    Start(
        ctx context.Context,
        plan CompiledPlan,
    ) (string, error)

    Resume(
        ctx context.Context,
        runID string,
    ) error

    Cancel(
        ctx context.Context,
        runID string,
    ) error
}
```

接口只展示核心职责。生产实现应使用持久化任务队列、数据库条件更新和租约机制，不能依靠单进程内存中的 goroutine 状态维持长时间任务。

## 8. Agent 之间的消息协议

多个 Agent 不能通过自由文本直接共享所有上下文。建议定义统一的消息封装，消息体采用经过 Schema 验证的结构化数据。

### 8.1 消息格式

```

{
  "protocol": "odap-agent-message-v1",
  "message_id": "msg_01",
  "correlation_id": "run_01",
  "causation_id": "msg_parent",
  "tenant_id": "tenant_01",
  "run_id": "run_01",
  "task_id": "check_policy",
  "delegation_depth": 0,
  "sender": {
    "agent_id": "finance.policy-reviewer",
    "version": "1.0.0"
  },
  "recipient": {
    "agent_id": "finance.expense-verifier",
    "version": "1.0.0"
  },
  "message_type": "TASK_RESULT",
  "schema_ref": "finance.policy-check-result.v1",
  "payload": {
    "status": "INCOMPLETE",
    "findings": [
      {
        "code": "MISSING_BUDGET_DATA",
        "severity": "warning",
        "evidence_refs": ["evidence_17"]
      }
    ]
  },
  "provenance": {
    "ontology_digest": "sha256:...",
    "policy_digest": "sha256:...",
    "source_evidence_refs": ["evidence_17"]
  },
  "created_at": "2026-10-12T10:00:00Z"
}

```

这里的时间和摘要均为示意值。

### 8.2 消息类型

| 类型                      | 用途               |
| ----------------------- | ---------------- |
| `TASK_REQUEST`          | 请求协作：传递结果、证据或质疑；不得直接创建执行任务 |
| `TASK_RESULT`           | 返回结构化执行结果        |
| `TASK_FAILURE`          | 报告执行失败           |
| `EVIDENCE_PROPOSAL`     | 提交事实或证据引用        |
| `CRITIQUE`              | 提出质疑、反例或冲突       |
| `VERIFICATION_RESULT`   | 返回校验结果           |
| `HUMAN_REVIEW_REQUIRED` | 请求人工介入           |
| `CANCEL_REQUEST`        | 请求取消下游任务         |

建议使用以下消息处理规则：

- 消息 ID 全局唯一，消费者按消息 ID 去重。
- `correlation_id` 关联同一业务运行，`causation_id` 记录因果来源。
- 消息必须校验租户、发送方、接收方和 Schema。
- 业务结果应通过引用传递大型证据，不宜在消息中复制大量原始文档。
- 消息队列至少一次投递时，消费者必须幂等；不能假设只投递一次。
- 不允许 Agent 通过消息声明自己已经获得审批。审批状态应从可信的审批服务查询。
- 任务创建权独占 Orchestrator：执行任务（`run_steps`）只能由 Orchestrator 依据编译后的计划创建（§7.3、§7.4）。Agent 间的 `TASK_REQUEST` 只承载结果、证据引用与质疑，接收方不得依据该消息自行创建执行任务或改写调度状态；确需新增执行节点时，只能提交候选计划并重新经过 Plan Compiler。
- 模式 C 循环体内动态生成的迭代计划，必须重新经过 Plan Compiler 编译，并对包含历史迭代节点的全图执行环检测；未通过编译与环检测的迭代不得进入 Harness（§7.1）。
- 消息只能携带协作内容，不能携带调度所有权。任务领取、租约与状态转换以数据库条件更新和租约为准（§14.4）；任何"我已在执行该任务"的消息声明都不构成执行权。

### 8.3 冲突不应被静默覆盖

如果两个 Agent 得出不同结论，不能简单使用“最后一个写入的结果”。

例如：

- 制度分析 Agent：未找到明确禁止条款。
- 风险审查 Agent：发现该报销单疑似重复提交。
- 数据核验 Agent：无法获取历史报销记录。

正确的聚合结果不是“制度分析通过”，而是保留各自发现，并根据预定义策略进入 `INCOMPLETE`、`CONFLICT` 或人工复核状态。

## 9. Context Builder：上下文构建与证据管理

Context Builder 是 ODAP 的关键基础设施。它决定 Agent 能看到哪些信息、信息来自哪里、使用哪个版本，以及哪些事实尚未确定。

### 9.1 上下文分层

建议将上下文分为七层：

| 层级       | 内容                  | 可信来源                   |
| -------- | ------------------- | ---------------------- |
| L0：运行控制  | 租户、任务 ID、截止时间、预算    | 平台可信服务                 |
| L1：主体与权限 | 用户、组织、数据范围、授权结果引用   | 身份与权限引擎                |
| L2：任务目标  | 用户请求、业务目标、输入 Schema | 经过验证的任务入口              |
| L3：业务事实  | 员工、报销单、预算等数据        | ERP Port 或已授权数据源       |
| L4：语义约束  | 类、属性、关系、SHACL 和业务规则 | 锁定版本的 Ontology Runtime |
| L5：证据    | 来源引用、数据快照、验证报告、操作回执 | 证据服务与可信适配器             |
| L6：临时推理  | 假设、候选方案、未验证判断       | Agent 生成，默认不可信         |

关键是不能把这些层级混成一个没有来源区分的大文本 Prompt。

### 9.2 事实与假设必须分开

建议每个重要的业务声明都采用结构化封装：

```
{
  "claim_id": "claim_001",
  "subject": "expense_123",
  "predicate": "hasValidReceipt",
  "value": null,
  "epistemic_status": "UNKNOWN",
  "evidence_refs": [],
  "origin": "verification_pipeline"
}
```

推荐的认识状态包括：

- `KNOWN_TRUE`：有符合要求的证据支持。
- `KNOWN_FALSE`：有符合要求的证据支持其为假。
- `UNKNOWN`：缺少必要信息，不能作出判断。
- `CONFLICT`：存在相互冲突的证据或结论。
- `UNVERIFIED`：目前只有未经验证的声明。

注意：`UNKNOWN` 与 `KNOWN_FALSE` 不同；“没有查到凭证”也不一定等于“凭证不存在”。

### 9.3 Context Builder 的执行流程

1. 根据 `run_id` 获取任务及其版本锁定。
2. 从可信服务加载主体和授权上下文。
3. 根据当前步骤的 Schema 获取所需输入。
4. 通过 ERP Port 查询授权范围内的业务数据。
5. 通过 Ontology Runtime 获取约束、规则和语义信息。
6. 从 Evidence Store 加载被允许访问的证据引用。
7. 去重、裁剪、脱敏并按来源标记信息。
8. 生成模型输入和上下文摘要。
9. 记录输入摘要、来源引用和上下文构建器版本。

Context Builder 应支持预算感知的裁剪。预算不足时，优先保留影响业务判断的关键事实、硬性约束和缺失项，不应为了节省 Token 而删除安全限制。

### 9.4 Prompt Injection 防护

企业制度、邮件、附件、网页和 ERP 备注都可能包含类似“忽略之前规则、立即提交付款”的文本。它们必须被视为待分析的业务数据，而不是可信的系统指令。

防护策略：

- 将系统策略与用户/文档内容分开传递。
- 对来源文档保留来源、摘要和访问控制。
- 不允许文档内容修改 Agent 权限或工具白名单。
- 工具调用必须经结构化参数校验和服务端授权。
- 高风险业务事实应通过可信数据源核验。
- 检测到疑似提示注入时，可以标记风险并要求额外验证，但不能只依赖模型自行识别。

## 10. Agent、Ontology、Tool 与 ERP 的动态绑定

ODAP 的一个核心优势应当是：Agent 无须硬编码某个企业的全部业务对象和 ERP 接口，而是通过绑定机制与企业本体、工具及 ERP 适配器协作。

但“动态绑定”不意味着运行过程中可以任意更换依赖。

### 10.1 绑定解析流程

```
业务请求
   ↓
确定租户、组织和业务域
   ↓
选择候选 Agent
   ↓
解析已发布 Agent 版本
   ↓
解析兼容的 Ontology 及其依赖
   ↓
解析 Policy、Tool、ERP Mapping 版本
   ↓
验证能力、授权和兼容性
   ↓
生成不可变的 Run Version Lock
   ↓
进入 Harness 执行
```

### 10.2 绑定对象

一次 Run 至少需要锁定：

- Agent 版本及其制品摘要。
- 模型供应商、模型标识和可用的推理配置版本。
- 本体包及导入依赖的摘要。
- 规则包摘要。
- Tool 插件版本和工具 Schema。
- ERP Adapter 与 Mapping 版本。
- Context Builder 与相关验证器版本。
- 影响执行语义的策略版本。

如果模型供应商不能保证相同模型标识在不同时间具有完全相同的行为，平台应记录可获得的模型标识、调用配置和运行元数据，而不应宣称模型调用具备完全确定性。

### 10.3 兼容性判断

兼容性检查至少包括：

- 输入、输出 Schema 是否兼容。
- 本体中的类、属性和关系是否满足 Agent 的依赖。
- 规则是否发生影响结论的变化。
- 工具调用契约是否变化。
- ERP 映射是否仍适用于目标业务对象。
- 权限策略是否允许当前任务所需操作。

对于已启动的 Run，默认保持原有版本锁定。若旧版本因安全问题被撤销，则根据风险策略暂停、终止或转人工；不能为了继续执行而悄悄切换到新版本。

## 11. Runtime 隔离、资源预算与故障控制

多 Agent 系统如果没有资源限制，很容易出现递归委派、无限复议、模型调用暴涨和工具重试风暴。ODAP 应在 Agent、Run、租户三个层级实施预算控制。

### 11.1 三级预算

| 层级         | 主要限制                            |
| ---------- | ------------------------------- |
| Agent 调用级  | 单次调用超时、输入输出大小、模型调用次数            |
| Run 任务级    | 总 Token、总模型成本、工具调用总数、最大循环次数、总耗时 |
| Tenant 租户级 | 并发 Run 数、并发模型请求、每日预算、存储和队列配额    |

预算的最终权威来源应是平台服务，而不是模型输出。

建议预算服务支持原子扣减或预留机制，避免多个并行 Agent 各自认为自己拥有完整的剩余预算。

### 11.2 推荐的默认参数

以下仅为 MVP 初始配置建议，实际数值应根据部署环境、模型价格和业务时效调整。

| 参数            | 初始建议             |
| ------------- | ---------------- |
| 单个 Agent 调用超时 | 60 秒             |
| Run 默认超时      | 5 分钟             |
| 并行 Agent 数    | 每 Run 最多 4 个     |
| 最大复议轮次        | 2 轮              |
| 单个工具调用超时      | 按工具契约配置          |
| 自动重试次数        | 最多 1～2 次，按错误类型区分 |
| 模型成本预算        | 由租户配置，不设统一硬编码金额  |

对写入 ERP 的操作，重试次数不能直接沿用普通模型调用策略。必须结合幂等能力、操作账本状态和回执情况进行判断。

### 11.3 运行隔离模型

推荐采用“控制面与执行面分离”的部署方式：

- 控制面：Registry、调度、权限、预算、审批、审计。
- 执行面：Agent Worker、工具执行器、模型客户端、隔离的插件进程。
- 数据面：PostgreSQL、证据存储、本体存储、ERP 连接器。

Go Agent Worker 负责调度和协议处理；对不可信的第三方插件，应采用独立进程或容器隔离，设置 CPU、内存、网络和文件系统限制。

不能把 Go 的 `context.Context` 当作安全沙箱。取消 Context 只是取消信号，插件进程可能仍然运行，因此需要执行器监督、超时终止和孤儿任务回收，其完整闭环见 §11.6。

### 11.4 重试策略

建议将错误分类后再决定是否重试。

| 错误类型          | 建议处理                  |
| ------------- | --------------------- |
| 临时网络故障        | 受预算限制的退避重试            |
| 模型服务限流        | 遵守 `Retry-After`，进入队列 |
| 输入 Schema 不合法 | 不自动重复相同请求，修正输入后再执行    |
| 本体验证失败        | 返回验证发现或要求补充数据         |
| 权限拒绝          | 不重试绕过权限；必要时转人工        |
| 审批待处理         | 暂停并等待可信审批事件           |
| ERP 写入结果未知    | 查询操作账本并对账，不直接重发       |
| 插件崩溃          | 可恢复的无副作用任务可有限重试       |
| 版本摘要不匹配       | 拒绝执行并触发安全告警           |

### 11.5 取消与恢复

取消 Run 时，平台需要区分：

1. 尚未开始的任务：标记为取消。
2. 正在运行的只读任务：发送取消信号并等待确认。
3. 正在执行的工具调用：依据工具契约取消或等待结果。
4. 已进入审批阶段的任务：撤销待执行请求或将审批标记为失效。
5. ERP 操作已经提交或结果未知：继续对账，不能把"Run 已取消"误当成"ERP 操作已撤销"。
6. Worker 已崩溃：按失联判定回收其任务租约，插件进程由 Supervisor 强制清理，产物按"保留账本与检查点、丢弃未提交中间态"处置（§11.6）；已提交的外部操作继续对账，不得因进程消失而被改写为失败。

这也是 Harness 与 Agent Runtime 必须保持独立的原因：模型停止生成，不代表外部业务事务已经停止。

### 11.6 插件 Supervisor 与孤儿回收闭环

插件 Supervisor 是插件进程的唯一父级负责人（可先以库形式实现于 Agent Worker 进程内，后续独立为 sidecar），负责失联判定、强制清理、产物处置与孤儿登记。没有这一层，"取消 Context"只是日志里的一行记录，插件进程会继续在后台运行，私有化长跑必然泄漏进程与孤儿任务。

**失联判定：心跳超时 + 租约过期双条件。** Worker 失联不凭单一信号判定，必须同时满足：

1. 心跳超时：Worker 超过心跳阈值（默认 45 秒，与租约时长一致）未向 `worker_instances` 写入心跳。
2. 租约过期：该 Worker 持有的全部任务租约均已过期（`lease_expires_at < now()`，即第二篇租约三件套 `lease_owner`/`lease_expires_at`/`lease_epoch` 中的过期判定）。

任一条不满足都只升级告警、由 Recovery Controller 观察，不执行清理：心跳超时但租约仍有效，可能只是 GC 停顿或网络抖动造成的"慢 Worker"，误杀会把可恢复任务变成孤儿。由于续约与心跳是同一个写入动作（第十篇 §8.3），双条件的实质是"整个租约周期内没有任何续期写入，且租约已过期"。

**插件进程强制清理：容器/PID 两级回收。** 对判定失联的 Worker 名下的每个插件进程执行：

1. 容器 Runner：调用容器运行时停止并移除容器（stop → rm），失败时强制 kill；按镜像与插件实例登记表核对，确保无漏网容器。
2. 进程 Runner：按登记的 PID 与进程组发送 `SIGTERM`，给予宽限期（默认 10 秒）自行退出；宽限期后仍存活的进程组发送 `SIGKILL`，随后确认进程组无残留子进程。
3. 信号必须送达插件进程自身；清理完成的判据是"进程不存在"，而不是 Go 侧收到了取消信号（§11.3）。
4. 清理前后各写一条审计：清理对象、Runner 类型、信号序列、退出码与时间戳。

**产物处置：保留账本与检查点，丢弃未提交中间态。** 强制清理按下表处置插件产物：

| 产物                                    | 处置 | 理由                      |
| ------------------------------------- | ---- | ----------------------- |
| `tool_calls`、`erp_operations` 账本记录   | 保留 | 恢复与对账的唯一依据              |
| Harness 检查点、已提交的 Outbox 事件、已持久化证据引用 | 保留 | 权威状态与事件账本               |
| 插件内存态、临时文件、未确认的输出缓冲       | 丢弃 | 未进入权威账本，恢复时不得当作既成事实  |

清理不等于业务回滚：已提交但发布失败的事件由 Outbox 重新发布（第十篇 §5.4）；结果未知的外部操作进入对账流程，不因进程被清理而改写状态。

**孤儿检测规则。** 孤儿任务（存在执行记录但无有效 Worker 的任务）的检测以第六篇 §14.2.3 恢复扫描与巡检为唯一真源：租约过期扫描（`lease_expires_at < now()` 且非终态 → `RecoveryStarted`）、孤儿 Step 扫描（Step 非终态但 Run 已终态 → 违反不变量 I1，告警）、不变量全量巡检（I1–I6）。插件 Supervisor 只改变进程世界，不直接改写业务状态；任务重新入队、状态重算与外部操作对账统一由第六篇状态机与第十篇 Recovery Controller 执行，巡检结果写入审计且不自动改状态。

## 12. 冲突裁定与证据合并

多 Agent 系统不应以多数投票作为通用的业务裁定方法。三个模型给出同样的答案，并不等于该答案正确；一个有权威来源支持的事实，也不应被两个无证据的猜测推翻。

建议将冲突处理分为四类。

### 12.1 冲突类型

- 事实冲突：两个数据源给出不同值。
- 语义冲突：不同 Agent 对同一个业务概念理解不同。
- 规则冲突：两个有效规则可能产生相反结果。
- 策略冲突：业务计划与权限、审批或安全约束不一致。

### 12.2 裁定优先级

建议采用以下顺序：

1. 权限和安全控制边界。
2. 已发布本体中的确定性约束和经确认的业务规则。
3. 可验证的权威业务事实及其新鲜度要求。
4. 具有明确来源的业务分析结论。
5. 模型生成的假设、推测和建议。

该优先级不能简单地被解释为“本体永远比事实重要”。例如，本体定义某个字段为必填，但 ERP 数据缺少该字段时，正确结果是报告缺失并按规则处理，而不是凭本体定义推断该字段的值。

### 12.3 结构化冲突报告

```
{
  "conflict_id": "conflict_001",
  "conflict_type": "FACT_CONFLICT",
  "subject_ref": "expense_123",
  "claims": [
    {
      "agent_id": "finance.data-checker",
      "claim": "历史记录未发现重复",
      "evidence_refs": ["query_result_1"]
    },
    {
      "agent_id": "finance.risk-reviewer",
      "claim": "存在相似报销记录",
      "evidence_refs": ["query_result_2"]
    }
  ],
  "resolution_status": "NEEDS_REVIEW",
  "required_action": "REQUERY_AUTHORITATIVE_SOURCE"
}
```

Orchestrator 可以触发一次重新查询，也可以将任务交给专门的冲突分析 Agent，但不能允许该 Agent 自行删除相反证据。

### 12.4 终止条件

一个多 Agent 任务应在以下任一条件成立时结束或暂停：

- 所有必需步骤完成，且确定性验证满足要求。
- 达到最大循环次数或预算上限。
- 出现无法自动消解的规则冲突。
- 必需事实缺失且无法进一步获取。
- 需要人工审批或人工裁决。
- 权限被撤销或执行版本失效。
- 遇到无法安全恢复的外部操作不确定状态。

结果状态至少需要区分 `SUCCEEDED`、`FAILED`、`INCOMPLETE`、`NEEDS_REVIEW`、`CANCELLED` 和 `UNKNOWN`，避免把所有非成功情况都混成一个“失败”。

## 13. Agent 评估、发布与灰度

Agent 能够运行，不代表 Agent 达到了可用于企业业务的质量标准。ODAP 需要建立独立于模型生成过程的评估体系。

### 13.1 四层评估体系

| 层级       | 评估内容           | 示例              |
| -------- | -------------- | --------------- |
| L1：接口正确性 | 协议、Schema、错误处理 | 非法输出是否被拒绝       |
| L2：任务质量  | 业务目标完成情况       | 报销信息是否提取完整      |
| L3：约束与安全 | 是否遵守本体、权限与审批   | 未授权写操作是否被阻止     |
| L4：生产可靠性 | 延迟、成本、恢复、对账    | ERP 回执丢失后是否正确对账 |

其中 L3 应包含不可被平均分抵消的硬性门槛。例如，只要测试发现 Agent 能绕过权限检查，就不能因为其平均任务准确率很高而发布。

### 13.2 评估数据集

建议为每个业务 Agent 建立固定的测试集，至少包含：

- 正常业务样例。
- 必填字段缺失。
- 相互矛盾的数据。
- 本体规则冲突。
- 制度文档包含恶意指令。
- 超出授权范围的数据访问。
- 重复工具调用。
- 模型输出格式错误。
- 超时、限流与插件崩溃。
- ERP 操作提交后响应丢失。
- 版本升级导致的语义变化。
- 需要人工审批或必须拒绝的高风险场景。

测试集应保存版本、来源、期望结果和判定方式。涉及企业数据时，必须遵守脱敏、访问权限与保留策略。

### 13.3 发布门禁

推荐采用硬性门禁与质量指标结合的方式：

- 所有安全关键测试必须通过。
- 所有必需的确定性规则测试必须通过。
- 输出 Schema 错误率满足要求。
- 关键业务场景的正确率达到约定阈值。
- 不存在未处置的高严重度缺陷。
- 成本和延迟不超过业务预算。
- 版本依赖、制品摘要和审批记录完整。

具体阈值应由业务负责人和平台管理员共同确定，不宜对所有 Agent 使用同一个准确率指标。

### 13.4 灰度发布

建议支持以下发布方式：

- 开发环境：使用模拟数据和 Mock ERP。
- 测试环境：固定测试集与集成测试。
- 影子模式：读取真实数据、生成候选结果，但不执行写入。
- 有限灰度：在明确租户、组织或业务范围内启用。
- 正式发布：满足门禁并完成授权配置后使用。
- 紧急撤销：出现安全问题时停止新任务，并处置在途任务。

对于会影响 ERP 的 Agent，灰度不能只比较输出质量，还必须验证写操作授权、幂等、审批、回执与对账流程。

### 13.5 版本回滚

回滚 Agent 版本不应意味着恢复整个企业数据库，也不应改变已经完成的外部事务。

正确做法是：

1. 停止新任务使用问题版本。
2. 对在途任务评估继续、暂停或终止策略。
3. 检查该版本已经触发的 ERP 操作。
4. 查询操作账本并完成必要对账。
5. 恢复之前已验证的 Agent 版本。
6. 保留问题版本、运行证据和事故记录。

## 14. 数据库设计与持久化

前几篇已经设计了统一的插件 Registry、Harness 运行表和审计体系。因此，本篇应在既有基础上扩展 Agent 专属元数据，避免再造一套平行的运行账本。

### 14.1 建议的数据表

| 表                                 | 用途                 |
| --------------------------------- | ------------------ |
| `agent.definitions`               | Agent 逻辑定义         |
| `agent.versions`                  | 不可变的 Agent 版本与制品摘要 |
| `agent.capabilities`              | Agent 声明的能力        |
| `agent.bindings`                  | 租户或业务域的 Agent 绑定   |
| `agent.evaluation_runs`           | 测试与评估执行记录          |
| `agent.evaluation_results`        | 单项测试结果             |
| `agent.rollouts`                  | 灰度和激活策略            |
| `agent.revocations`               | 撤销与处置记录            |
| `orchestration.plans`             | 编译后的执行计划（ADR-001 统一 IR，权威 DDL 见第十四篇 §15） |
| `orchestration.tasks`             | 计划中的任务节点           |
| `orchestration.task_dependencies` | 节点依赖关系             |
| `orchestration.conflicts`         | 冲突及其处置状态           |

多 Agent 消息不再单独建表：消息即事件，统一走第十篇 `EventEnvelope` + `consumer_inbox` 去重（按 `(consumer_name, event_id)`），消息元数据（Schema 版本、因果关系、证据引用）作为事件 payload 的受控字段持久化。早期设计中的 `orchestration.messages` 表已废止，避免与第十篇去重机制双轨。

实际部署时，建议沿用第二篇的 PostgreSQL schema 规范、租户隔离、迁移和审计约定。表名前缀和 Schema 命名应统一到现有仓库，不必机械地复制上述名称。

### 14.2 Agent 版本表示例

```
CREATE SCHEMA IF NOT EXISTS agent;

CREATE TABLE agent.definitions (
    tenant_id       UUID NOT NULL,
    agent_id        TEXT NOT NULL,
    display_name    TEXT NOT NULL,
    domain          TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by      UUID NOT NULL,
    PRIMARY KEY (tenant_id, agent_id)
);

CREATE TABLE agent.versions (
    tenant_id          UUID NOT NULL,
    agent_id           TEXT NOT NULL,
    version            TEXT NOT NULL,
    manifest_digest    TEXT NOT NULL,
    artifact_digest    TEXT NOT NULL,
    status             TEXT NOT NULL,
    evaluation_profile TEXT,
    published_at       TIMESTAMPTZ,
    published_by       UUID,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, agent_id, version),
    FOREIGN KEY (tenant_id, agent_id)
        REFERENCES agent.definitions (tenant_id, agent_id),
    CHECK (status IN (
        'DRAFT',
        'VALIDATING',
        'TESTING',
        'REVIEW',
        'PUBLISHED',
        'ACTIVE',
        'SUSPENDED',
        'RETIRED',
        'REVOKED'
    ))
);

CREATE INDEX idx_agent_versions_status
    ON agent.versions (tenant_id, status, agent_id);
```

说明：

- 该 SQL 是基础示例，不是完整的生产 Schema。
- 正式发布后的版本应禁止原地修改 Manifest、制品摘要及其语义内容。
- 状态变化应通过受控服务完成，并记录状态变更历史。
- `tenant_id` 应贯穿关联键，防止跨租户引用。
- 还应在数据库层或可信数据访问层实施行级安全策略，不能只依靠前端过滤。

### 14.3 执行计划与任务

计划应与 Harness 的 `agent_runs`、`run_steps` 关联，而不是自行维护一个不一致的任务状态系统。

建议：

- `orchestration.plans` 记录候选计划摘要、编译结果和版本锁定。
- `orchestration.tasks` 记录任务定义、依赖、输入输出引用和调度状态。
- `run_steps` 仍是实际执行步骤状态的权威记录。
- 多 Agent 消息不建专表：消息即事件，走第十篇 `EventEnvelope` + `consumer_inbox` 去重；消息 ID、Schema、因果关系和证据引用作为事件 payload 的受控字段持久化（`orchestration.messages` 表已废止，见 §14.1）。
- `tool_calls` 与 `erp_operations` 继续作为工具调用和 ERP 操作的权威账本。

不要把完整文档、全部模型响应和大型证据对象无限制塞入 PostgreSQL JSONB。结构化元数据存数据库，大型制品和受控证据放入对象存储，数据库保存对象引用、摘要和访问策略。

### 14.4 并发与一致性

- 使用唯一约束保证消息去重和任务身份唯一。
- 使用条件更新或租约实现任务抢占，防止两个 Worker 同时执行同一节点。
- 使用 Outbox 模式保证数据库状态变更与待发布事件的一致性。
- 将模型调用和 ERP 请求放在数据库事务之外。
- 通过幂等键和执行账本处理重试。
- 将 Run 的版本锁定和审批关联写入持久化记录，避免进程重启后丢失。

## 15. 核心 API 设计

建议沿用 ODAP 统一的 `/api/v1` 路径和现有身份认证、租户隔离及审计规范。

### 15.1 Agent 管理 API

| 方法     | 接口                                          | 功能          |
| ------ | ------------------------------------------- | ----------- |
| `GET`  | `/api/v1/agents`                            | 查询 Agent 列表 |
| `POST` | `/api/v1/agents`                            | 创建 Agent 定义 |
| `GET`  | `/api/v1/agents/{id}/versions`              | 查询版本        |
| `POST` | `/api/v1/agents/{id}/versions`              | 注册新版本       |
| `POST` | `/api/v1/agents/{id}/versions/{v}/validate` | 验证版本        |
| `POST` | `/api/v1/agents/{id}/versions/{v}/evaluate` | 运行评估        |
| `POST` | `/api/v1/agents/{id}/versions/{v}/publish`  | 申请发布        |
| `POST` | `/api/v1/agents/{id}/versions/{v}/activate` | 激活版本        |
| `POST` | `/api/v1/agents/{id}/versions/{v}/suspend`  | 暂停使用        |
| `POST` | `/api/v1/agents/{id}/versions/{v}/revoke`   | 撤销版本        |

发布、激活和撤销属于高影响管理操作，应检查对应管理权限并生成审计事件。

### 15.2 执行与协作 API

| 方法     | 接口                             | 功能            |
| ------ | ------------------------------ | ------------- |
| `POST` | `/api/v1/runs`                 | 创建 Agent 运行任务 |
| `GET`  | `/api/v1/runs/{id}`            | 查询运行状态        |
| `GET`  | `/api/v1/runs/{id}/steps`      | 查询执行步骤        |
| `GET`  | `/api/v1/runs/{id}/evidence`   | 查询证据索引        |
| `POST` | `/api/v1/runs/{id}/cancel`     | 请求取消（进入 `CANCELLING`） |
| `POST` | `/api/v1/runs/{id}/resume`     | 恢复可恢复任务       |
| `GET`  | `/api/v1/runs/{id}/conflicts`  | 查询冲突          |
| `POST` | `/api/v1/runs/{id}/review`     | 提交有权限的人工裁决    |

路径以 `docs/contracts/03-API命名与契约规范.md` 裁决为准：执行资源统一为 `/api/v1/runs`（本篇早期版本的 `/api/v1/agent-runs` 已收敛，见 contracts/03 §4 裁决表）；取消语义见第六篇 §14.2.1（先 `CANCELLING` 收敛，不得直达终态）。

示例：创建一个报销核验任务。

```
POST /api/v1/runs
Content-Type: application/json
Authorization: Bearer <access-token>
Idempotency-Key: expense-review-2026-001
```

```
{
  "agent_ref": "finance.expense-processor",
  "input": {
    "expense_claim_id": "expense_123"
  },
  "execution_mode": "propose",
  "requested_scope": {
    "business_domain": "finance.expense"
  }
}
```

这里的 `execution_mode: propose` 表示请求生成候选结果，而不是直接执行写入。服务端仍需验证 Agent、输入 Schema、租户、数据范围和业务权限。

对接口还应补充：

- 统一错误码与错误响应结构。
- 分页、过滤、限流和超时约定。
- 幂等键的作用域、有效期和摘要冲突规则。
- 长任务的异步受理与状态查询。
- 对象级授权及证据访问控制。
- 管理操作的审计与防重放措施。

## 16. 前端工作台设计

ODAP 前端应当让管理员理解 Agent 的行为和业务影响，而不只是展示一个聊天窗口。

建议设计六个主要工作区。

[Agentic AI SaaS Dashboard Design by Yeasin Islam on Dribbble](https://images.openai.com/static-rsc-4/6NsIPRnMcQ4YeQqwtxsR_OEEf64_YtVvi4tankR7isJUGggXXldOhQDxI2fGMq5XUyhOUTrlW68NhzI9EVxMQOtX25_W7Axb-ByCNnwteczzi0cYE3puezQiKdoZISu2iKrX7d6xDBcQE1mIZ43FzMaUQnuQ_tLVMWzhkBp9iZU?purpose=inline)

16.1 Agent 注册中心

Agent Registry

展示 Agent 名称、业务域、版本、状态、能力、本体依赖、评估结果和发布记录。支持检索、版本比较与依赖查看。

[What Is Workflow Builder? — Embeddable React SDK Overview](https://images.openai.com/static-rsc-4/y8c4QWFgwtKFwvm2ii-Z7JnS9VWf_O1D1nQw6JHl_clAjDrD1JHi5Yt-GsJriEU6gfPXzG8DSHKTEvkGUzdfx1RjyaZ6MlwrzB7SrLLjf1OP_Do0E0qHCx6DspvY0KLR3tcAIIXfjVdd0T9njjfs3wSdR_NyTMLxUJNIUMJr1rY?purpose=inline)

16.2 多 Agent 流程设计器

Orchestration Designer

通过 DAG 编辑器配置 Agent 节点、依赖、输入输出 Schema、失败分支、并行执行和人工介入节点。发布前显示静态检查结果。

[Honeycomb Agent Timeline | AI Agent Observability](https://images.openai.com/static-rsc-4/9WQ98JyI0JB9SuAGSBKYt7UUElGO8PrqIWuER-CEvPx9KOg9w6RtHd7dG5DebW5onCPvd5RYHsFIIBo8SXMgkzB02PN0gZbmaAON_ln-tMlJPW_w5N1lmC5yd5gt1QhhlQnUZe2e7tHhLPLeRXEI5SReAFFA04KQQH1UPDCkaBc?purpose=inline)

16.3 运行监控中心

Runtime Observatory

查看 Run 状态、步骤耗时、模型调用、工具调用、预算消耗、重试和错误分布。支持从一个异常步骤追溯到对应输入、输出和证据。

[About | Approach & Accountability | Borealis Security](https://images.openai.com/static-rsc-4/9r7DAtec5G0Bsfq_vQAnZTbxK9kevqJtxFUn-JrUkeyURSwvIA1yLIBKR9RZP1bSB1MOVymV061S6E1cTO-bGkkfNpE1C1a8EU0uH6aKJJzanEMC4unChiacujmLNo_Me_SuqQ0dL9-lrmobF9W5Tig1-Fk7_Zie2h39sTkkMsE?purpose=inline)

16.4 冲突与人工复核中心

Review Center

将不同 Agent 的结论、相反证据、制度依据、缺失事实及建议处置集中呈现。人工裁决必须记录主体、时间、理由和作用范围。

[Announcing: New evaluation comparisons in W&B Weave](https://images.openai.com/static-rsc-4/KqCw3-SgwNQK4-LwQfjuSPcrcZy4KD4-ZoKIEsS7tDFgDis-gqdpjxHrXqxPukd5R-PtDGap7VWItOYGZpX70rpk_D4O2ueuEp5dt5Bbmv_VyZIJ_Y73dpbl4oU1zStxgkOZcsrAy4hR-6kj9BZ4_kuTXdhFPNble_RENrVKdXY?purpose=inline)

16.5 评估与发布中心

Evaluation & Release

展示评估集、硬性门禁、历史版本对比、灰度范围、回滚入口和发布审批记录。

[KodeMi.ai | AI Code Assistant](https://images.openai.com/static-rsc-4/ZPuHTuSRmhPSA_oUZLUyQ76F5WrOYUrQDBlZrS6m4ayvF1LhYq8puhNxpmUTo08K4z1Evmy9HT2YKA3hTtTNH9b4jS4G18HbRtdpm561wO6wpxq1fOpXQrcZsJtQBtTeNUfBRBuhPZnKK0KUJ9UVTxbtCvCoKNOnhuxO7xp7smE?purpose=inline)

16.6 Agent 治理中心

Governance

管理租户可用 Agent、业务域绑定、工具权限、预算、网络隔离策略、撤销记录和审计策略。

这些图片用于表达界面方向，不是 ODAP 的最终页面设计稿。

### 16.7 运行详情页的关键布局

运行详情页建议分为三个区域：

- 左侧：执行图，显示节点状态、依赖和失败位置。
- 中间：业务结果，展示结构化结论、未知项、冲突和待办动作。
- 右侧：证据与治理，展示本体版本、规则版本、来源引用、授权决策、审批状态和 ERP 操作回执。

对高风险步骤，应明确区分“模型建议”“系统校验结果”“权限决策”“人工审批”和“ERP 已确认结果”，不能全部显示为一个绿色的“成功”。

## 17. 安全与治理设计

Agent Runtime 是 ODAP 中非常重要的安全边界之一。需要重点防范以下风险。

| 风险           | 必要控制                         |
| ------------ | ---------------------------- |
| Agent 越权调用工具 | 工具执行前由服务端重新授权                |
| 跨租户数据泄露      | 租户隔离、对象级授权、证据访问检查            |
| 恶意文档注入       | 数据与指令分离、工具白名单、来源追踪           |
| Agent 无限委派   | 最大深度、节点数量、循环次数和预算限制          |
| 插件逃逸         | 进程/容器隔离、资源配额、网络默认拒绝          |
| 未授权修改本体      | 仅允许受控 Factory 流程提交候选版本       |
| 审批后请求被修改     | 绑定不可变 Payload Digest，执行时重新核验 |
| 模型输出伪造授权     | 授权决策只能由可信权限服务签发              |
| 版本供应链风险      | 制品摘要、签名验证、依赖锁定与撤销            |
| 运行日志泄露敏感信息   | 最小化日志、字段脱敏、访问审计              |
| ERP 操作结果不明   | 操作账本、幂等、回执核验和对账              |

### 17.1 Agent 的最小权限原则

每个 Agent 都应具备明确的能力边界：

- 只能读取完成当前任务所需的数据。
- 只能调用声明且已获授权的工具。
- 不能直接连接生产数据库。
- 不能自行读取密钥存储。
- 不能自行修改权限策略或审批结果。
- 不能绕过 ERP Gateway 直接执行业务写入。
- 不能自行将候选本体、候选规则变成正式发布版本。

尤其不能把"Agent 是内部可信插件"作为免除隔离和授权的理由。内部 Agent 同样可能因模型误判、上下文污染、依赖缺陷或版本升级而产生错误操作。

### 17.2 委派深度控制

递归委派是多 Agent 系统最容易失控的路径：A 请求 B、B 请求 C，若不设上限，一次业务请求可能展开成指数级调用，把预算、队列与外部系统一起拖垮。

控制规则：

1. Envelope 增加 `delegation_depth` 字段（§8.1），从 0 开始计数，每次经消息网关向下游委派时加 1，并随消息全链路传递。
2. Gateway 侧强制校验：Agent 消息网关（Orchestrator 的统一消息入口）在接收与转发前校验该字段是否超过策略上限（默认 3，按租户与业务域可收紧）；超限消息直接拒绝，不进入任何队列，也不做"先收下再报错"的延迟处置。
3. 超限处置：拒绝该委派，按 docs/contracts/02-统一错误码注册表.md §3.2 返回 `AUTHZ_PERMISSION_DENIED`（基础码 `PERMISSION_DENIED`，需审计），记录发起方、接收方、当前深度、策略版本与 Run 标识；委派链断裂由 Orchestrator 按 §12.4 终止条件收敛，不得静默重试。
4. 深度只是第一道闸门：节点数量、循环次数、并行度与预算限制仍然同时有效（§11.1、§11.2），任一上限达标都按终止条件处理。

## 18. 完整端到端业务示例

以员工报销为例，将前七篇与本篇串成完整链路。

\#chatgpt-mermaid-\_r_3eb\_{font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";font-size:16px;fill:rgb(237, 237, 237);}@keyframes edge-animation-frame{from{stroke-dashoffset:0;}}@keyframes dash{to{stroke-dashoffset:0;}}#chatgpt-mermaid-\_r_3eb\_ .edge-animation-slow{stroke-dasharray:9,5!important;stroke-dashoffset:900;animation:dash 50s linear infinite;stroke-linecap:round;}#chatgpt-mermaid-\_r_3eb\_ .edge-animation-fast{stroke-dasharray:9,5!important;stroke-dashoffset:900;animation:dash 20s linear infinite;stroke-linecap:round;}#chatgpt-mermaid-\_r_3eb\_ .error-icon{fill:rgb(48, 48, 48);}#chatgpt-mermaid-\_r_3eb\_ .error-text{fill:rgb(237, 237, 237);stroke:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_3eb\_ .edge-thickness-normal{stroke-width:1px;}#chatgpt-mermaid-\_r_3eb\_ .edge-thickness-thick{stroke-width:3.5px;}#chatgpt-mermaid-\_r_3eb\_ .edge-pattern-solid{stroke-dasharray:0;}#chatgpt-mermaid-\_r_3eb\_ .edge-thickness-invisible{stroke-width:0;fill:none;}#chatgpt-mermaid-\_r_3eb\_ .edge-pattern-dashed{stroke-dasharray:3;}#chatgpt-mermaid-\_r_3eb\_ .edge-pattern-dotted{stroke-dasharray:2;}#chatgpt-mermaid-\_r_3eb\_ .marker{fill:rgb(175, 175, 175);stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_3eb\_ .marker.cross{stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_3eb\_ svg{font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";font-size:16px;}#chatgpt-mermaid-\_r_3eb\_ p{margin:0;}#chatgpt-mermaid-\_r_3eb\_ .label{font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";color:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_3eb\_ .cluster-label text{fill:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_3eb\_ .cluster-label span{color:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_3eb\_ .cluster-label span p{background-color:transparent;}#chatgpt-mermaid-\_r_3eb\_ .label text,#chatgpt-mermaid-\_r_3eb\_ span{fill:rgb(237, 237, 237);color:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_3eb\_ .node rect,#chatgpt-mermaid-\_r_3eb\_ .node circle,#chatgpt-mermaid-\_r_3eb\_ .node ellipse,#chatgpt-mermaid-\_r_3eb\_ .node polygon,#chatgpt-mermaid-\_r_3eb\_ .node path{fill:rgb(9, 23, 44);stroke:rgb(31, 78, 148);stroke-width:1px;}#chatgpt-mermaid-\_r_3eb\_ .rough-node .label text,#chatgpt-mermaid-\_r_3eb\_ .node .label text,#chatgpt-mermaid-\_r_3eb\_ .image-shape .label,#chatgpt-mermaid-\_r_3eb\_ .icon-shape .label{text-anchor:middle;}#chatgpt-mermaid-\_r_3eb\_ .node .katex path{fill:#000;stroke:#000;stroke-width:1px;}#chatgpt-mermaid-\_r_3eb\_ .rough-node .label,#chatgpt-mermaid-\_r_3eb\_ .node .label,#chatgpt-mermaid-\_r_3eb\_ .image-shape .label,#chatgpt-mermaid-\_r_3eb\_ .icon-shape .label{text-align:center;}#chatgpt-mermaid-\_r_3eb\_ .node.clickable{cursor:pointer;}#chatgpt-mermaid-\_r_3eb\_ .root .anchor path{fill:rgb(175, 175, 175)!important;stroke-width:0;stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_3eb\_ .arrowheadPath{fill:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_3eb\_ .edgePath .path{stroke:rgb(175, 175, 175);stroke-width:1px;}#chatgpt-mermaid-\_r_3eb\_ .flowchart-link{stroke:rgb(175, 175, 175);fill:none;}#chatgpt-mermaid-\_r_3eb\_ .edgeLabel{background-color:rgb(0, 0, 0);text-align:center;}#chatgpt-mermaid-\_r_3eb\_ .edgeLabel p{background-color:rgb(0, 0, 0);}#chatgpt-mermaid-\_r_3eb\_ .edgeLabel rect{opacity:0.5;background-color:rgb(0, 0, 0);fill:rgb(0, 0, 0);}#chatgpt-mermaid-\_r_3eb\_ .labelBkg{background-color:rgba(0, 0, 0, 0.5);}#chatgpt-mermaid-\_r_3eb\_ .cluster rect{fill:rgb(48, 48, 48);stroke:rgba(255, 255, 255, 0.15);stroke-width:1px;}#chatgpt-mermaid-\_r_3eb\_ .cluster text{fill:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_3eb\_ .cluster span{color:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_3eb\_ div.mermaidTooltip{position:absolute;text-align:center;max-width:200px;padding:2px;font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";font-size:12px;background:rgb(48, 48, 48);border:1px solid rgba(255, 255, 255, 0.15);border-radius:2px;pointer-events:none;z-index:100;}#chatgpt-mermaid-\_r_3eb\_ .flowchartTitleText{text-anchor:middle;font-size:18px;fill:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_3eb\_ rect.text{fill:none;stroke-width:0;}#chatgpt-mermaid-\_r_3eb\_ .icon-shape,#chatgpt-mermaid-\_r_3eb\_ .image-shape{background-color:rgb(0, 0, 0);text-align:center;}#chatgpt-mermaid-\_r_3eb\_ .icon-shape p,#chatgpt-mermaid-\_r_3eb\_ .image-shape p{background-color:rgb(0, 0, 0);padding:2px;}#chatgpt-mermaid-\_r_3eb\_ .icon-shape .label rect,#chatgpt-mermaid-\_r_3eb\_ .image-shape .label rect{opacity:0.5;background-color:rgb(0, 0, 0);fill:rgb(0, 0, 0);}#chatgpt-mermaid-\_r_3eb\_ .label-icon{display:inline-block;height:1em;overflow:visible;vertical-align:-0.125em;}#chatgpt-mermaid-\_r_3eb\_ .node .label-icon path{fill:currentColor;stroke:revert;stroke-width:revert;}#chatgpt-mermaid-\_r_3eb\_ .node .neo-node{stroke:rgb(31, 78, 148);}#chatgpt-mermaid-\_r_3eb\_ [data-look="neo"].node rect,#chatgpt-mermaid-\_r_3eb\_ [data-look="neo"].cluster rect,#chatgpt-mermaid-\_r_3eb\_ [data-look="neo"].node polygon{stroke:url(#chatgpt-mermaid-\_r_3eb\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_3eb\_ [data-look="neo"].swimlane.cluster rect{filter:none;}#chatgpt-mermaid-\_r_3eb\_ [data-look="neo"].node path{stroke:url(#chatgpt-mermaid-\_r_3eb\_-gradient);stroke-width:1px;}#chatgpt-mermaid-\_r_3eb\_ [data-look="neo"].node .outer-path{filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_3eb\_ [data-look="neo"].node .neo-line path{stroke:rgb(31, 78, 148);filter:none;}#chatgpt-mermaid-\_r_3eb\_ [data-look="neo"].node circle{stroke:url(#chatgpt-mermaid-\_r_3eb\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_3eb\_ [data-look="neo"].node circle .state-start{fill:#000000;}#chatgpt-mermaid-\_r_3eb\_ [data-look="neo"].icon-shape .icon{fill:url(#chatgpt-mermaid-\_r_3eb\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_3eb\_ [data-look="neo"].icon-shape .icon-neo path{stroke:url(#chatgpt-mermaid-\_r_3eb\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_3eb\_ .node text{font-size:14px;font-weight:600;letter-spacing:normal;fill:rgb(153, 206, 255);}#chatgpt-mermaid-\_r_3eb\_ .edgeLabels text{font-size:13px;font-weight:600;letter-spacing:-0.08px;fill:rgb(153, 206, 255);}#chatgpt-mermaid-\_r_3eb\_ .node tspan[font-weight="normal"],#chatgpt-mermaid-\_r_3eb\_ .edgeLabels tspan[font-weight="normal"]{font-weight:600;}#chatgpt-mermaid-\_r_3eb\_ .edgeLabel .label rect{opacity:1;rx:13px;ry:13px;fill:rgb(0, 14, 26);stroke:rgb(26, 62, 95);stroke-width:1px;}#chatgpt-mermaid-\_r_3eb\_ .node rect,#chatgpt-mermaid-\_r_3eb\_ .node circle,#chatgpt-mermaid-\_r_3eb\_ .node ellipse,#chatgpt-mermaid-\_r_3eb\_ .node polygon,#chatgpt-mermaid-\_r_3eb\_ .node path{fill:rgb(0, 40, 77);stroke:rgba(255, 255, 255, 0.1);stroke-width:1px;}#chatgpt-mermaid-\_r_3eb\_ .node rect{rx:16px;ry:16px;}#chatgpt-mermaid-\_r_3eb\_ .node.mermaid-decision .label-container{fill:rgb(0, 14, 26);stroke:rgb(26, 62, 95);stroke-dasharray:2,2;}#chatgpt-mermaid-\_r_3eb\_ .edgePaths .flowchart-link{stroke:rgb(175, 175, 175);stroke-width:1px;stroke-linecap:round;stroke-linejoin:round;}#chatgpt-mermaid-\_r_3eb\_ .marker{fill:rgb(175, 175, 175);stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_3eb\_ :root{--mermaid-font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";}员工提交报销请求身份认证与租户校验选择并锁定 Agent / Ontology/ Policy 版本Planner 生成候选执行计划Plan Compiler检查依赖与权限边界Harness 调度多 Agent查询员工、预算、历史报销数据Ontology Runtime验证业务约束Risk Agent 分析异常与冲突是否满足业务条件补充查询或人工复核拒绝或退回并生成证据权限引擎评估操作是否需要审批创建审批并暂停 Harness人工审批审批结果重新核验权限、版本和请求摘要ERP Operation GatewayERP Adapter / Mock ERP核验回执并对账Verifier 检查最终状态与证据Run 结束并记录审计缺少事实或存在冲突不满足满足是拒绝批准否

### 18.1 一个重要的工程细节

假设人工批准了报销单，但在实际写入 ERP 前，报销金额被修改。

此时平台不能沿用原来的审批结论直接执行。它必须重新核验审批绑定的请求摘要。如果 Payload Digest 不一致，应拒绝原操作请求，并根据业务策略重新发起审批。

同样，如果 ERP 已提交报销单，但网络响应丢失，Harness 应暂停相关后续步骤，交由操作账本和对账机制确定结果，而不是让 Agent 再次提交。

这些机制共同保证：多 Agent 的分析能力可以灵活扩展，但最终业务执行仍受可验证、可审计的控制流程约束。

## 19. MVP 实施计划与验收标准

建议在已有 Mock ERP 的基础上，分六个阶段完成第八篇的工程实现。

P1：Agent Registry 与 Manifest

建立 Agent 定义、版本、状态机、Schema 验证和制品摘要机制。

验收：非法 Manifest 被拒绝；已发布版本不可被静默修改；未经授权的用户不能发布或撤销 Agent。

P2：Agent Runtime 与 Context Builder

实现统一调用接口、版本锁定、结构化输入输出、预算、证据引用与超时控制。

验收：超时任务可识别；非法输出不能进入下游步骤；任务使用的版本和证据来源可追踪。

P3：Orchestrator 与消息协议

实现 DAG 编译、依赖调度、并行执行、消息去重、冲突处理和取消恢复。

验收：循环计划被拒绝；同一任务不会被多个 Worker 无控制地重复执行；Worker 重启后能够恢复任务状态。

P4：Harness、Ontology 与权限联调

将多 Agent 输出接入本体验证、授权决策、审批暂停和受控工具执行。

验收：模型无法伪造授权；缺少关键事实时不会误判为通过；审批绑定内容变化后不能直接执行。

P5：评估、发布与灰度

建立回归测试集、硬性发布门禁、影子模式、版本切换和紧急撤销。

验收：未通过安全门禁的 Agent 无法激活；撤销后新任务不再使用该版本；在途任务得到明确处置。

P6：前端工作台与端到端验收

完成 Agent 注册中心、流程设计器、运行监控、冲突复核和发布管理页面。

验收：从创建报销任务到验证结果、审批、Mock ERP 写入和最终审计，全流程可追踪。

### 19.1 必须通过的集成测试

1. Agent 使用未发布的本体版本时，任务启动失败。
2. Agent 返回不符合 Schema 的结果时，下游节点不执行。
3. 两个 Agent 对同一事实产生冲突时，系统保留双方证据。
4. 缺少关键数据时，系统返回 `UNKNOWN` 或 `INCOMPLETE`，而不是默认通过。
5. Agent 尝试调用未授权工具时，服务端拒绝调用。
6. 任务预算耗尽时，调度器停止新增调用。
7. 两个 Worker 同时抢占同一任务时，只允许一个有效执行租约。
8. 消息重复投递时，消费者能够幂等处理。
9. Agent 版本撤销时，新任务不能继续解析到该版本。
10. 审批后请求摘要发生变化时，ERP Gateway 拒绝执行。
11. ERP 提交成功但响应丢失时，系统进入对账流程，不盲目重发。
12. Run 取消后，已发出的外部操作仍能被查询和追踪。

## 20. 推荐的代码仓库结构

以下结构是在前几篇的基础上新增 Agent 相关模块，不要求一次性全部实现。

```
odap/
├── cmd/
│   ├── api/
│   ├── agent-worker/
│   ├── orchestrator/
│   └── mock-erp/
├── internal/
│   ├── agentregistry/
│   ├── agentruntime/
│   ├── agentcontext/
│   ├── agentbudget/
│   ├── agentevaluation/
│   ├── orchestration/
│   │   ├── compiler/
│   │   ├── scheduler/
│   │   ├── messages/
│   │   └── conflicts/
│   ├── harness/
│   ├── ontologyruntime/
│   ├── authorization/
│   ├── approval/
│   ├── toolregistry/
│   ├── erpport/
│   ├── erpgateway/
│   ├── evidence/
│   ├── audit/
│   └── persistence/
├── plugins/
│   ├── agents/
│   │   ├── finance-expense-processor/
│   │   ├── finance-policy-reviewer/
│   │   ├── finance-risk-reviewer/
│   │   └── finance-expense-verifier/
│   ├── ontologies/
│   └── tools/
├── schemas/
│   ├── agent-manifest.schema.json
│   ├── agent-message.schema.json
│   └── execution-plan.schema.json
├── tests/
│   ├── contract/
│   ├── integration/
│   ├── evaluation/
│   ├── security/
│   └── recovery/
├── migrations/
└── deployments/
    ├── docker-compose.yml
    └── config/
```

建议优先保持一个清晰的 Go 模块边界。MVP 阶段不必因为存在多个 Agent 就立即拆成大量微服务：可以先以模块化单体 API、独立 Worker 和独立 Mock ERP 起步，等并发、隔离和独立扩缩容的需求明确后，再拆分 Orchestrator、Registry 等服务。

关于自研编排执行与 Temporal / Cadence 的选型（含 Outbox、Inbox、租约与延迟队列是否由 river、pgmq 等成熟组件承载），评估结论见 docs/adr/ADR-003。本篇第 7、11、14 节与第十篇的 Outbox、Inbox、租约和恢复设计按"自研"情形编写：若 ADR 结论为引入成熟组件，这些节收缩为 ODAP 差异化层（ERP 对账、治理与审计）的设计，基础执行机制改为适配与配置，不得两边各建一套。

## 21. 本篇最终架构决策

综合 ODAP 前八篇，建议将 Agent Runtime 的核心设计固化为以下十条工程原则：

1. Agent 负责推理，Harness 负责执行控制，Orchestrator 负责协作调度。
2. Agent 必须具有明确的 Manifest、版本、能力声明和生命周期。
3. 多 Agent 默认采用 DAG 调度，只有明确需要时才允许有限循环。
4. 消息和输入输出均采用版本化 Schema，不依赖自由文本进行关键状态传递。
5. Context Builder 必须保留事实来源、规则依据、证据引用和未知状态。
6. Agent、本体、规则、工具和映射的版本必须在任务启动时锁定。
7. Agent 声明的能力不等于实际权限，工具和 ERP 操作必须重新进行服务端授权。
8. 评估、发布、灰度和撤销必须成为平台能力，而不是人工操作习惯。
9. 预算、超时、重试、取消和恢复必须由平台控制，不能交给模型自行决定。
10. 每一个重要业务结论和外部操作都应能追溯到版本、输入、证据、规则、授权与结果。

第八篇的核心价值，是把 ODAP 从“能够调用多个 Agent 的平台”提升为“能够治理多个 Agent、控制其执行，并对业务结果负责的企业级运行平台”。

下一篇建议进入《第九篇：ODAP 模型供应商抽象层、LLM Gateway 与推理治理详细设计》，统一模型供应商适配、模型路由、结构化输出、成本预算、限流、降级、模型版本记录及敏感数据处理策略。这样才能在不把整个系统绑定到单一模型供应商的前提下，支撑私有化部署与企业级运行。
