# 第十六篇：ODAP MVP 总工程实施蓝图、代码仓库规划与端到端交付路线

文档定位： 本篇是 ODAP（Ontology-Driven Agent Platform，企业本体驱动智能体平台）从架构设计进入实际工程开发的总实施文件。

> **本篇 scope 标签**：`[MVP-V1.0]` 本篇整体即 MVP 范围声明——第二章"必须做什么/暂时不做什么"（§2.1/§2.2）是范围冻结真源，暂缓项保留架构接口、不进入首期交付，其恢复条件指向第十五篇（评估仿真）与第十七篇（前端）对应章节；计划体系以 `docs/adr/ADR-001` 为唯一权威（§7.2 示例已对齐统一 IR），Registry 数据模型以 `docs/adr/ADR-002` 为基线（§5.1），自研/开源选型约束以 `docs/adr/ADR-003` 为准（§3.2）。

前十五篇已经分别设计了 ODAP 的数据模型、权限引擎、插件协议、本体工厂、Harness、ERP 接入、多智能体运行时、LLM Gateway、事件总线、审计证据、数据治理、知识图谱与语义推理、业务工作流、评估仿真和质量门禁。

现在最重要的任务不是再增加概念，而是把这些设计整合为一套可以创建仓库、启动服务、逐步编写代码、自动测试、最终完成端到端业务闭环的工程实施方案。

本篇将明确：

- ODAP MVP 到底要交付什么。
- 哪些模块必须首期实现，哪些可以暂缓。
- Go、React/TypeScript、PostgreSQL、OWL/RDF/SHACL 和 Mock ERP 如何组合。
- 单仓库如何组织，模块之间如何划定边界。
- 数据库迁移、API 契约、服务启动顺序如何安排。
- 第一个完整业务场景如何贯穿所有模块。
- 如何使用 Docker Compose 启动开发环境。
- 如何通过自动化测试、质量门禁和故障注入验收。
- 如何从单机 MVP 平稳演进到企业私有化部署。

# 一、ODAP MVP 的总体目标

## 1.1 产品目标

ODAP 不是简单的 Agent 聊天界面，也不是把 LLM 接到 ERP 上的工具集合。

它的核心定位是：

以企业本体为业务语义层，以 Agent 为规划与推理主体，以 Harness 为执行控制层，以权限、审批和证据机制约束业务行动，通过标准化接口连接企业现有业务系统。

其核心业务链路如下。

用户提出业务目标

例如：检查报销单并按企业制度提交审批

企业本体与语义查询

识别业务对象、属性、关系、规则和数据来源

Agent 规划与执行计划编译

生成结构化 Plan IR，明确依赖、输入和预期结果

Harness + 规则验证 + 权限决策

检查业务条件、执行边界、审批要求与版本一致性

Mock ERP 操作网关

幂等执行、操作账本、回执核验和未知结果对账

审计证据 + 评估报告

解释做了什么、依据是什么、结果是否真实确认

## 1.2 MVP 的成功标准

首期 MVP 不要求一次性实现完整 OWL 2 DL 推理器、所有 ERP 连接器或任意复杂的多 Agent 自主协作。

它必须证明以下六件事：

1. 语义正确： 能把企业业务对象、规则和数据表示为可查询、可验证的语义模型。
2. 执行受控： Agent 不能随意调用未注册的工具，也不能绕过权限和审批。
3. 业务可执行： 至少一个完整业务场景可以通过标准 ERP Port 完成模拟读写。
4. 故障可恢复： 超时、重复消息、Worker 崩溃和回执丢失不会导致盲目重复业务操作。
5. 过程可追溯： 可以从一次业务结果追溯到任务、计划、规则、授权、审批、操作回执和证据。
6. 质量可验证： 本体、规则、Agent 或工具升级后，可以运行回归测试并阻断不合格版本。

这六项比首期支持多少种模型、多少个 Agent 或多少个插件更重要。

# 二、MVP 范围冻结：必须做什么，暂时不做什么

ODAP 的设计覆盖面较广。如果把十五篇设计同时当作首期开发任务，很容易出现大量模块都有界面、却没有一条业务链真正闭合的问题。

建议先冻结 V1.0-MVP 的范围。

## 2.1 首期必须实现

| 模块                    | MVP 交付范围                              |
| --------------------- | ------------------------------------- |
| 企业身份与权限               | 本地开发身份、角色权限、对象范围检查、审批控制               |
| 插件 Registry           | Agent、Tool、Ontology、Policy 的基础注册与版本锁定 |
| Ontology Factory      | 导入预定义本体包，生成候选包并执行基础验证                 |
| Semantic Runtime      | RDF 图存储、受控查询 DSL、基础 RDFS、SHACL 验证     |
| Agent Runtime         | Planner、Executor、Verifier 的基础执行与协作    |
| Harness               | 持久化状态机、任务步骤、检查点、暂停与恢复                 |
| ERP Port              | 统一接口与独立 Mock ERP                      |
| ERP Operation Gateway | 写入授权、审批检查、幂等、操作账本、回执核验                |
| LLM Gateway           | 统一模型接口、结构化输出校验、超时和预算                  |
| Event/Worker          | PostgreSQL Outbox、异步 Worker、任务租约、失败恢复 |
| Audit/Evidence        | 关键事件审计、证据包、摘要校验                       |
| Evaluation            | 固定测试集、确定性断言、端到端回归、Quality Gate        |
| 前端                    | 登录、业务工作台、运行详情、审批、语义查询、评估报告            |
| 部署                    | Docker Compose、迁移脚本、健康检查、备份与恢复说明      |

这里的“基础注册”意味着先把接口、Manifest、版本和生命周期跑通，不要求首期实现所有插件隔离方式。

## 2.2 首期暂缓

以下功能保留架构接口，但不应成为 MVP 的阻塞项：

- 完整 OWL 2 DL 推理。
- 任意 SPARQL 查询。
- 自动发现并接入任意企业 ERP。
- 无人审核地从制度文档自动发布本体。
- 任意代码形式的第三方插件。
- 自动生成并自动发布业务规则。
- 大规模跨区域分布式部署。
- Kubernetes 多集群调度。
- 多模型复杂动态路由与自动成本优化。
- 自动修改生产策略的自优化 Agent。
- 通用 BPMN 引擎。
- 直接写入真实 ERP 数据库。

暂缓不等于取消，而是避免它们抢占首期业务闭环所需的工程资源。

## 2.3 MVP 的首个业务域

建议采用员工报销审核与提交审批作为第一个垂直切片。

它能覆盖：

- 员工、部门、费用类别、报销单等本体对象。
- 制度规则与 SHACL 数据验证。
- ERP 数据读取。
- Agent 规划与工具调用。
- 权限和人工审批。
- 模拟 ERP 写入。
- 幂等、回执和失败恢复。
- 审计证据与评估回归。

同时，它的业务范围足够清晰，便于构建可控的 Mock ERP。

首期不要求自动完成真实付款。先将“检查报销单、生成审核建议、经授权提交审批”跑通；资金支付等高风险动作不属于这个 MVP 的默认执行范围。

# 三、总体技术架构与选型

## 3.1 技术栈冻结

结合 ODAP 的目标与首期复杂度，建议冻结以下技术栈。

| 层次        | 首选方案                          | 主要职责                                     |
| --------- | ----------------------------- | ---------------------------------------- |
| 前端        | React + TypeScript + Vite     | 管理工作台、业务流程、审计与评估                         |
| 应用后端      | Go                            | API、权限、Agent Runtime、Harness、ERP Gateway |
| 元数据与事务数据库 | PostgreSQL                    | 任务、权限、审批、注册表、审计、操作账本                     |
| 语义数据层     | RDF 数据模型 + PostgreSQL MVP 实现  | 三元组、图快照、语义查询                             |
| 本体验证      | OWL/RDFS 与 SHACL 引擎的适配层       | 类型检查、约束验证、受控推理                           |
| 模型接入      | 自建 LLM Gateway                | 统一模型调用与治理                                |
| 异步执行      | PostgreSQL Outbox + Go Worker | 任务分发、租约、恢复、重试                            |
| 制品存储      | S3 兼容对象存储接口                   | 本体包、证据包、评估报告                             |
| 开发部署      | Docker Compose                | 本地开发与 MVP 演示                             |
| 自动化测试     | Go 测试 + 前端测试 + 集成测试           | 单元、契约、回归、故障注入                            |

底层是否采用 Rust，不应在 MVP 初期成为关键路径。只有在基准测试证实 RDF 解析、图索引、批量匹配等计算模块成为主要瓶颈时，再将相应的纯计算内核替换为 Rust 实现。

## 3.2 关键架构决策

### 决策 A：先采用模块化单体，而非微服务群

首期采用一个 Go API Server，内部按领域划分模块；Worker、Mock ERP 和前端作为独立进程部署。

这样既能维持模块边界，也避免过早引入服务发现、分布式事务和复杂运维。

```
React Web
    │
    ▼
Go API Server
    ├── Identity / Authorization
    ├── Plugin Registry
    ├── Ontology Factory / Runtime
    ├── Agent Runtime / Harness
    ├── Workflow / Tool Registry
    ├── ERP Operation Gateway
    ├── Audit / Evidence
    ├── Evaluation / Quality Gate
    └── API / Event Outbox
             │
             ├──────── PostgreSQL
             ├──────── Object Storage
             ├──────── LLM Provider
             └──────── Go Worker
                           │
                           ▼
                        Mock ERP
```

这里的模块边界是逻辑边界。模块可以在同一个 Go 进程中，但必须通过明确的接口调用，不能随意读写其他模块的内部表。

### 决策 B：业务事实与平台运行状态分离

PostgreSQL 可以承载多个 Schema，但不同数据的权威来源必须明确：

- ODAP 数据库：平台任务、审批、权限、执行记录与操作账本。
- Mock ERP 数据库：模拟的企业业务对象及其权威状态。
- 语义图存储：本体实例、标准化事实和派生关系。
- 对象存储：大型制品、证据包与报告。

不能把 ODAP 中的本地缓存当作 ERP 的权威业务状态。

### 决策 C：先实现确定性执行，再扩展自主性

Agent 可以提出计划，但计划必须通过类型、规则、权限和风险检查。

MVP 中，业务写入路径不允许由模型生成任意 SQL、Shell 或未注册工具调用。所有外部写入必须经过固定的 ERP Operation Gateway。

### 决策 D：本体运行时采用可替换接口

先定义 `GraphStore`、`OntologyValidator`、`Reasoner` 等接口。首期可以使用 PostgreSQL 图存储和经验证的标准语义引擎适配器，不自行从零实现全部推理标准。

正式选择具体 OWL/RDF/SHACL 库前，需要核验许可证、运行环境、语义支持范围和实际测试结果，不能仅依据“支持 RDF”就假定其完整支持所需语义。

# 四、单仓库工程规划

建议 ODAP 采用 Monorepo：前端、Go 后端、Mock ERP、数据库迁移、部署文件和测试集统一管理。

这样可以让 API 契约、Schema 版本、端到端测试和开发环境保持一致。

## 4.1 推荐仓库结构

```
odap/
├── cmd/
│   ├── api/
│   │   └── main.go
│   ├── worker/
│   │   └── main.go
│   ├── mock-erp/
│   │   └── main.go
│   └── migrate/
│       └── main.go
│
├── internal/
│   ├── platform/
│   │   ├── config/
│   │   ├── database/
│   │   ├── logging/
│   │   ├── telemetry/
│   │   ├── httpserver/
│   │   └── shutdown/
│   │
│   ├── identity/
│   ├── authorization/
│   ├── registry/
│   ├── plugin/
│   ├── ontology/
│   │   ├── factory/
│   │   ├── package/
│   │   ├── runtime/
│   │   ├── graphstore/
│   │   ├── validator/
│   │   └── reasoning/
│   │
│   ├── semantic/
│   │   ├── dsl/
│   │   ├── parser/
│   │   ├── typecheck/
│   │   ├── planner/
│   │   ├── executor/
│   │   └── evidence/
│   │
│   ├── agent/
│   │   ├── runtime/
│   │   ├── planner/
│   │   ├── executor/
│   │   ├── verifier/
│   │   └── collaboration/
│   │
│   ├── workflow/
│   ├── harness/
│   ├── tools/
│   ├── llmgateway/
│   ├── erp/
│   │   ├── port/
│   │   ├── gateway/
│   │   ├── adapters/
│   │   └── reconciliation/
│   │
│   ├── execution/
│   │   ├── outbox/
│   │   ├── inbox/
│   │   ├── worker/
│   │   ├── lease/
│   │   └── recovery/
│   │
│   ├── audit/
│   ├── evidence/
│   ├── evaluation/
│   ├── simulation/
│   ├── qualitygate/
│   └── api/
│       ├── handlers/
│       ├── middleware/
│       └── routes/
│
├── mockerp/
│   ├── internal/
│   │   ├── domain/
│   │   ├── service/
│   │   ├── repository/
│   │   └── api/
│   └── migrations/
│
├── web/
│   ├── src/
│   │   ├── app/
│   │   ├── api/
│   │   ├── components/
│   │   ├── features/
│   │   ├── layouts/
│   │   ├── routes/
│   │   └── types/
│   ├── package.json
│   └── vite.config.ts
│
├── contracts/
│   ├── openapi/
│   ├── jsonschema/
│   └── events/
│
├── ontology-packages/
│   ├── core/
│   └── finance/
│
├── policies/
│   ├── authorization/
│   └── quality-gates/
│
├── datasets/
│   ├── finance/
│   ├── ontology/
│   └── security/
│
├── migrations/
│   └── platform/
│
├── deployments/
│   ├── compose/
│   ├── config/
│   └── scripts/
│
├── tests/
│   ├── unit/
│   ├── integration/
│   ├── contract/
│   ├── e2e/
│   ├── security/
│   └── fault-injection/
│
├── docs/
│   ├── architecture/
│   ├── decisions/
│   ├── adr/            # 跨分册收敛 ADR（ADR-001~003、ADR-005，见 §12.3）
│   ├── api/
│   ├── development/
│   └── runbooks/
│
├── Makefile
├── go.mod
├── go.sum
├── compose.yaml
├── .env.example
├── .gitignore
└── README.md
```

## 4.2 目录组织原则

建议遵循四条约束：

1. `cmd/` 只负责初始化依赖、读取配置、启动服务和处理关闭信号。
2. `internal/` 负责领域逻辑，不能让 HTTP Handler 直接实现业务规则。
3. `contracts/` 保存对外 API、事件和结构化输出的契约。
4. `tests/` 保存跨模块测试；各模块仍可在自身目录内保留紧邻代码的单元测试。

另一个关键点：`migrations/` 只负责 ODAP 平台数据库，`mockerp/migrations/` 只负责 Mock ERP。两者应拥有不同的数据库账号和迁移生命周期。

## 4.3 Go 包之间的依赖规则

推荐依赖方向：

```
API Handler
    ↓
Application Service
    ↓
Domain Logic / Use Case
    ↓
Interface (Port)
    ↑
Infrastructure Adapter
```

例如：

- `harness` 可以依赖 `erp/gateway` 定义的业务接口。
- `erp/gateway` 可以调用授权、审批和操作账本接口。
- `erp/adapters/mock` 实现 ERP Port。
- `authorization` 不应依赖具体 ERP Adapter。
- `semantic` 不应直接发起 ERP 写操作。
- `agent/planner` 不应绕过 Harness 直接执行工具。

这些依赖关系应通过代码审查和架构测试持续维护。

# 五、数据库设计整合：从十五篇 Schema 到可迁移工程

前面的设计涉及权限、任务、插件、本体、语义图、审批、ERP 操作、审计、证据和评估。如果直接把所有 SQL 一次性拼进一个大文件，后期迁移和测试会非常困难。

建议采用按领域分组、按依赖排序、向前迁移、可验证恢复的数据库策略。

## 5.1 Schema 划分

| Schema         | 主要数据                                         |
| -------------- | -------------------------------------------- |
| `iam`          | 用户、角色、成员关系、授权策略                              |
| `registry`     | 插件五类 kind（AGENT/ONTOLOGY/TOOL/POLICY/MODEL_PROVIDER，ADR-002）、插件版本、依赖、制品；Action Contract 版本化资产注册位置（第十四篇 §4.1） |
| `ontology`     | 本体包、版本、验证、发布记录                               |
| `graph`        | RDF Terms、Named Graphs、Triples、来源引用          |
| `reasoning`    | 推理配置、推导记录、物化任务                               |
| `agent`        | Agent 定义、版本、运行配置                             |
| `planning`     | 计划、计划修订、计划校验记录（第十四篇 §15.1：`planning.plans`/`plan_revisions`/`validation_runs`） |
| `workflow`     | 工作流模板、版本、节点定义                                |
| `execution`    | Runs、Steps、Attempts、Checkpoints、Outbox、Inbox、authorization_decisions、agent_run_dependencies |
| `approval`     | 审批请求、审批动作、审批摘要                               |
| `erp`          | 连接配置、操作账本、回执与对账                              |
| `audit`        | 审计事件、检查点、访问记录                                |
| `evidence`     | 证据包、制品、引用和验证记录                               |
| `llm`          | 模型端点、路由策略、预算、用量                              |
| `evaluation`   | 数据集、案例、评估运行和指标                               |
| `simulation`   | 仿真场景、仿真运行、故障注入                               |
| `release_mgmt` | 候选发布、质量门禁、发布记录                               |

并非所有 Schema 都需要在首个迭代中建完。推荐先创建首期必需表，再按功能逐步增加。

与分册 DDL 的对应关系（避免迁移落地时命名漂移）：执行域表（Runs/Steps/Attempts/Checkpoints/Tool Calls/authorization_decisions/agent_run_dependencies）以第二篇 `runtime` schema 的 DDL 为权威，本表 `execution` 行描述的是同一逻辑范围的清单而非物理 schema 名，迁移脚本按第二篇命名落地；`authorization_decisions`（授权决策可追溯，P0-4 修复）与 `agent_run_dependencies`（RunVersionLock 落点，ADR-002）已由第二篇加入，此处登记以防第十六篇迁移漏项；计划域三表以第十四篇 §15.1 为权威；插件与 Action Contract 注册模型以第四篇 + ADR-002 为基线。

## 5.2 迁移编号

建议使用连续编号，避免多个开发分支各自生成难以排序的迁移文件。

```
migrations/platform/
├── 000001_create_iam.sql
├── 000002_create_registry.sql
├── 000003_create_ontology.sql
├── 000004_create_execution.sql
├── 000005_create_approval.sql
├── 000006_create_erp_ledger.sql
├── 000007_create_audit.sql
├── 000008_create_evidence.sql
├── 000009_create_evaluation.sql
└── 000010_add_required_indexes.sql
```

这只是建议的初始拆分。实际编号应随仓库内已提交的迁移确定，不要覆盖已在共享环境执行过的迁移。

## 5.3 迁移规则

- 已提交并应用的迁移原则上不可修改，使用新迁移修正。
- 数据库升级前检查备份与恢复能力。
- 对大表增加索引时评估锁表和执行时间。
- 破坏性字段变更采用扩展、迁移、收缩的分阶段策略。
- 对关键状态列使用受控枚举或约束，避免任意字符串造成状态污染。
- 为关键迁移添加升级测试，并在空数据库和上一稳定版本数据库上验证。

MVP 不必立刻实现复杂的在线 Schema 变更平台，但至少应具备版本记录、迁移失败退出和恢复演练。

## 5.4 多租户隔离

所有企业业务数据必须有明确的租户边界。对需要租户隔离的表，优先使用 `tenant_id`，并通过服务层权限校验与 PostgreSQL Row-Level Security 等机制形成纵深防御。

需要特别注意：

- 不能相信请求体中的 `tenant_id`。
- 租户身份必须来自经过验证的认证上下文。
- 后台 Worker 也必须携带可信的租户上下文。
- 系统级任务应使用明确的服务身份和最小权限。
- 全局共享本体与租户业务实例必须分开授权。
- 数据库连接池复用连接时，必须正确设置并清理事务级租户上下文，防止上下文泄漏。

RLS 是纵深防御，不替代应用层的对象级、字段级和业务操作授权。

# 六、服务接口与契约先行

ODAP 的模块数量较多，如果先各自开发、最后再联调，很容易出现数据结构、状态名和错误处理不一致的问题。

建议采用 Contract-First（契约优先） 开发方式。

## 6.1 统一接口契约

首期需要统一管理三类契约：

- OpenAPI：前端与后端的 HTTP API。
- JSON Schema：Agent 输出、插件 Manifest、工作流参数和证据清单。
- Event Schema：Outbox 事件、Worker 消费消息与状态变更通知。

例如，报销审核工作流的 Agent 输出不应是自由文本：

```
{
  "schema_version": "1.0",
  "claim_id": "claim-1001",
  "assessment_status": "NEEDS_APPROVAL",
  "findings": [
    {
      "code": "RECEIPT_REQUIRED",
      "severity": "WARNING",
      "fact_refs": ["fact-201"]
    }
  ],
  "recommended_action": "SUBMIT_FOR_APPROVAL",
  "required_checks": [
    "ONTOLOGY_VALIDATION",
    "AUTHORIZATION_CHECK",
    "APPROVAL_CHECK"
  ]
}
```

这个 JSON 只是结构化的候选评估结果，不是直接执行命令。后端仍需验证 Schema、本体语义、业务规则、权限、审批和当前业务状态。

## 6.2 API 错误模型

建议所有 API 使用统一的错误响应结构：

```
{
  "error": {
    "code": "APPROVAL_REQUIRED",
    "message": "当前操作需要完成审批",
    "request_id": "req-001",
    "retryable": false,
    "details": {
      "operation_id": "op-001"
    }
  }
}
```

字段含义：

- `code`：稳定的机器可读错误代码。
- `message`：可供用户理解的描述，不应泄露内部敏感信息。
- `request_id`：用于日志和 Trace 关联。
- `retryable`：供调用方参考的重试提示，不是绕过业务检查的许可。
- `details`：经过权限过滤的结构化信息。

业务失败不能一律返回 HTTP 500。参数错误、未授权、审批未完成、资源冲突、限流和内部错误应分别映射为合适的 HTTP 状态码及稳定业务错误码。

## 6.3 API 版本管理

推荐统一采用 `/api/v1/` 前缀。

对破坏性变更，应新增版本或采用明确的兼容过渡期。对于 Agent、Ontology、Workflow、Policy 和 Tool 的版本，应独立于 HTTP API 版本管理。

一个 HTTP API 版本并不能代表整个业务执行依赖的版本。

# 七、首个端到端业务流程：报销审核与提交审批

本章是整个 MVP 的核心。建议开发团队先把这个流程完整实现，再扩展到合同、采购、人力资源等业务域。

## 7.1 用户场景

用户提交请求：

> 请检查报销单 `claim-1001`，核对员工、费用类别、凭证、预算和企业制度；如果符合提交条件，则按授权策略完成提交审批，并生成完整的执行报告。

ODAP 应执行以下流程：

1. 认证用户身份，确认租户、角色和业务范围。
2. 读取报销单、员工、部门、费用类别和预算信息。
3. 通过语义查询与本体验证，检查对象关系和数据完整性。
4. 运行报销规则，生成带事实引用的审核结果。
5. Planner 生成结构化执行计划，Verifier 检查计划依赖和风险。
6. Harness 对每个节点进行状态管理，重新检查执行所需的权限。
7. 如果需要审批，创建审批请求并暂停相关写入步骤。
8. 审批通过后，再次校验授权、审批摘要、对象版本和业务状态。
9. 通过 ERP Operation Gateway 向 Mock ERP 提交操作。
10. 核验权威业务状态与操作回执，生成审计事件和 Evidence Bundle。
11. 运行工作流结果断言，形成端到端评估报告。

## 7.2 执行计划的结构

建议使用明确版本化的 Plan IR，而不是让模型生成可直接执行的程序。Plan IR 的全平台唯一定义见 `docs/adr/ADR-001`：节点类型以第十四篇 §6.2 的 13 类为基线，编译/类型检查/策略检查归 Harness Plan Compiler，Orchestrator 只管 DAG 调度；本篇不另建枚举。

示意（报销审核流程按统一 IR 表达）：

```
{
  "plan_version": "1.0",
  "plan_type": "BUSINESS_WORKFLOW",
  "intent_id": "intent-001",
  "input": {
    "claim_id": "claim-1001"
  },
  "nodes": [
    {
      "node_id": "load_claim",
      "kind": "TOOL_CALL",
      "tool_ref": "erp.get_expense_claim@1.0.0",
      "effect_class": "READ",
      "depends_on": []
    },
    {
      "node_id": "validate_ontology",
      "kind": "DATA_VALIDATION",
      "validation_profile": "finance-shacl@1.0.0",
      "depends_on": ["load_claim"]
    },
    {
      "node_id": "evaluate_policy",
      "kind": "RULE_EVALUATION",
      "rule_bundle_ref": "finance-expense-rules@1.0.0",
      "depends_on": ["validate_ontology"]
    },
    {
      "node_id": "approve_submission",
      "kind": "APPROVAL_GATE",
      "approval_policy_ref": "finance.expense.submit.approval@1.0.0",
      "depends_on": ["evaluate_policy"]
    },
    {
      "node_id": "submit_claim",
      "kind": "TOOL_CALL",
      "tool_ref": "erp-adapter.submit-expense@2.3.0",
      "effect_class": "EXTERNAL_WRITE",
      "depends_on": ["approve_submission"]
    },
    {
      "node_id": "verify_receipt",
      "kind": "RESULT_VERIFY",
      "depends_on": ["submit_claim"]
    },
    {
      "node_id": "build_evidence",
      "kind": "EVIDENCE_FINALIZE",
      "depends_on": ["verify_receipt"]
    }
  ]
}
```

与本篇早期示例的差异说明（ADR-001 决策 6）：

- 原 `AUTHORIZATION_CHECK` 步骤不是计划节点：授权检查归 Execution Guard 执行前门（第六篇 §9.2），每次写操作执行前重检。
- 原 `ONTOLOGY_VALIDATE` 归 `DATA_VALIDATION`；原 `RULE_EVALUATE` 归 `RULE_EVALUATION`；原 `EVIDENCE_BUILD` 归 `EVIDENCE_FINALIZE`。
- 原 `ERP_OPERATION`/`ERP_OPERATION_VERIFY`：ERP 写经写网关与第七篇 Operation Gateway（第十四篇 §17.2 拆分后的唯一外部写路径），回执核验归 `RESULT_VERIFY`。
- 字段名从 `steps`/`type` 统一为 `nodes`/`kind`，与第十四篇 §6.1 对齐；`tool_ref` 携带工具及版本，绑定与版本锁定由 Plan Compiler 完成。

以上为结构示例，正式流程还需要包含条件分支、审批暂停、失败路径、重试策略、补偿策略和节点输入输出 Schema。

尤其需要明确：如果 `evaluate_policy` 发现数据不完整，流程应进入补充材料或人工复核分支，而不是继续提交。

## 7.3 核心状态机

工作流状态不另建词表，一律引用 `docs/contracts/01` 的 17 态 Run 状态（转换矩阵以第六篇 §14.2 为权威）：`CREATED`、`CONTEXT_BUILDING`、`READY`、`PLANNING`、`PLAN_VALIDATING`、`RUNNING`、`WAITING_INPUT`、`WAITING_APPROVAL`、`PAUSED`、`RECOVERING`、`CANCELLING`、`REQUIRES_RECONCILIATION`、`MANUAL_REVIEW`、`COMPLETED`、`FAILED`、`CANCELLED`、`EXPIRED`。本篇早期示例中的 `WAITING_RECONCILIATION`/`NEEDS_MANUAL_REVIEW` 分别映射为 `REQUIRES_RECONCILIATION`/`MANUAL_REVIEW`（contracts/01 §1.1 映射表）。

需要将任务状态、步骤状态和 ERP 操作状态分开管理。

例如，整个任务处于 `REQUIRES_RECONCILIATION` 时，某个 ERP 操作可能处于 `UNKNOWN`，而之前的读取和验证步骤仍然是 `SUCCEEDED`。不能因为任务级状态未知，就把所有步骤统一标记为失败。

## 7.4 正常流程验收

对正常案例，至少验证：

- 报销单读取正确。
- 语义检查和业务规则结果符合预期。
- 计划依赖正确。
- 用户具备对应业务范围的权限。
- 审批要求得到满足。
- ERP 业务操作只执行一次。
- 权威业务状态符合预期。
- 回执与操作账本一致。
- 证据包包含版本、事实、规则、审批和回执引用。
- 端到端评估结果通过。

## 7.5 异常流程验收

至少覆盖：

- 报销单不存在。
- 员工状态异常。
- 凭证缺失。
- 预算数据过期。
- 本体验证失败。
- 用户没有权限。
- 审批被拒绝。
- 审批后报销单发生变化。
- ERP 提交后响应丢失。
- Worker 在操作完成后崩溃。
- 回执与实际业务状态不一致。
- 证据构建暂时失败。

对每个案例都应定义预期终态、允许执行的副作用、必需的审计事件和必须保留的证据。

# 八、开发环境：Docker Compose 与本地启动

首期应做到开发者克隆仓库后，通过统一命令就能启动基础服务、执行迁移、导入本体包、加载测试数据和运行端到端测试。

## 8.1 服务拓扑

建议开发环境包含：

- `web`：React 前端开发服务器。
- `api`：ODAP Go API Server。
- `worker`：异步任务 Worker。
- `mockerp`：独立 Mock ERP。
- `postgres`：平台数据库。
- `mockerp-db`：Mock ERP 独立数据库。
- `objectstore`：S3 兼容对象存储（可选）。
- `otel-collector`：本地可观测性采集（可选）。

首期不需要将所有可选服务强制加入最小启动路径。

## 8.2 Compose 配置示例

下面给出用于开发环境的骨架。数据库镜像版本、健康检查命令、对象存储镜像和持久化配置应在正式仓库中明确锁定并经过测试。

```

services:
  postgres:
    image: postgres:17
    environment:
      POSTGRES_DB: odap
      POSTGRES_USER: odap
      POSTGRES_PASSWORD: ${ODAP_DB_PASSWORD:?set ODAP_DB_PASSWORD}
    ports:
      - "127.0.0.1:5432:5432"
    volumes:
      - odap-postgres:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U odap -d odap"]
      interval: 5s
      timeout: 3s
      retries: 20

  mockerp-db:
    image: postgres:17
    environment:
      POSTGRES_DB: mockerp
      POSTGRES_USER: mockerp
      POSTGRES_PASSWORD: ${MOCKERP_DB_PASSWORD:?set MOCKERP_DB_PASSWORD}
    ports:
      - "127.0.0.1:5433:5432"
    volumes:
      - mockerp-postgres:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U mockerp -d mockerp"]
      interval: 5s
      timeout: 3s
      retries: 20

  migrate-mockerp:
    build:
      context: .
      dockerfile: deployments/compose/Dockerfile.migrate
    environment:
      MOCKERP_DATABASE_URL: ${MOCKERP_DATABASE_URL:?set MOCKERP_DATABASE_URL}
      MIGRATIONS_DIR: mockerp/migrations
    depends_on:
      mockerp-db:
        condition: service_healthy
    restart: "no"

  mockerp:
    build:
      context: .
      dockerfile: deployments/compose/Dockerfile.mockerp
    environment:
      DATABASE_URL: ${MOCKERP_DATABASE_URL:?set MOCKERP_DATABASE_URL}
      HTTP_ADDR: ":8081"
    depends_on:
      mockerp-db:
        condition: service_healthy
      migrate-mockerp:
        condition: service_completed_successfully

  api:
    build:
      context: .
      dockerfile: deployments/compose/Dockerfile.api
    environment:
      DATABASE_URL: ${ODAP_DATABASE_URL:?set ODAP_DATABASE_URL}
      MOCK_ERP_BASE_URL: http://mockerp:8081
      HTTP_ADDR: ":8080"
      APP_ENV: development
    ports:
      - "127.0.0.1:8080:8080"
    depends_on:
      postgres:
        condition: service_healthy
      migrate-mockerp:
        condition: service_completed_successfully

  worker:
    build:
      context: .
      dockerfile: deployments/compose/Dockerfile.worker
    environment:
      DATABASE_URL: ${ODAP_DATABASE_URL:?set ODAP_DATABASE_URL}
      APP_ENV: development
    depends_on:
      postgres:
        condition: service_healthy
      migrate-mockerp:
        condition: service_completed_successfully

  web:
    build:
      context: .
      dockerfile: deployments/compose/Dockerfile.web
    environment:
      API_BASE_URL: http://api:8080
    ports:
      - "127.0.0.1:5173:5173"
    depends_on:
      api:
        condition: service_started

  objectstore:
    image: minio/minio:RELEASE.2024-01-16T16-07-39Z
    command: server /data --console-address ":9001"
    environment:
      MINIO_ROOT_USER: ${OBJECT_STORE_ACCESS_KEY:?set OBJECT_STORE_ACCESS_KEY}
      MINIO_ROOT_PASSWORD: ${OBJECT_STORE_SECRET_KEY:?set OBJECT_STORE_SECRET_KEY}
    ports:
      - "127.0.0.1:9000:9000"
      - "127.0.0.1:9001:9001"
    volumes:
      - odap-objectstore:/data
    profiles: ["objectstore"]
    healthcheck:
      test: ["CMD", "mc", "ready", "local"]
      interval: 5s
      timeout: 3s
      retries: 20

  otel-collector:
    image: otel/opentelemetry-collector-contrib:0.96.0
    command: ["--config=/etc/otel/config.yaml"]
    volumes:
      - ./deployments/config/otel-collector.yaml:/etc/otel/config.yaml:ro
    ports:
      - "127.0.0.1:4317:4317"
      - "127.0.0.1:4318:4318"
    profiles: ["observability"]
    depends_on:
      postgres:
        condition: service_healthy

volumes:
  odap-postgres:
  mockerp-postgres:
  odap-objectstore:

```

这是示意配置，尚不是可以直接用于生产环境的完整文件。正式落地前应统一环境变量名称、添加最小权限数据库账号、补齐服务健康检查、数据库迁移依赖、密钥管理和网络访问控制。

与 §8.1 拓扑的对应关系：`web`、`objectstore`、`otel-collector` 已补齐；其中 `objectstore` 与 `otel-collector` 是 §8.1 标注的可选服务，通过 Compose profile（`objectstore`/`observability`）隔离，最小启动路径为 `docker compose up -d --build`，需要对象存储或本地可观测性采集时追加 `--profile objectstore --profile observability`。`migrate-mockerp` 是一次性迁移 job（`restart: "no"`，退出码非 0 即启动失败）：`mockerp`、`api`、`worker` 均以 `depends_on: migrate-mockerp: service_completed_successfully` 等待 Mock ERP 迁移完成，避免服务在 Schema 未就绪时接受流量；平台侧迁移（`cmd/migrate`，Makefile `migrate` 目标）与 `migrate-mockerp` 同构，在同一启动序列中先于 `api`/`worker` 执行。原 compose 中 `${MOCKERp_*}` 变量名与 `.env.example` 的 `${MOCKERP_*}` 不一致（本段已统一为大写），变量名必须在实际应用配置中统一，否则健康检查与迁移会读到空值。

其中 `POSTGRES_PASSWORD` 等开发凭据必须由本地环境提供，不应把真实密码提交到 Git 仓库。开发端口只绑定本机，避免无意暴露到局域网或公网。

## 8.3 环境配置文件

建议提供 `.env.example`：

```
APP_ENV=development

ODAP_DB_PASSWORD=replace-with-local-secret
MOCKERP_DB_PASSWORD=replace-with-local-secret

ODAP_DATABASE_URL=postgres://odap:replace-with-local-secret@postgres:5432/odap?sslmode=disable
MOCKERP_DATABASE_URL=postgres://mockerp:replace-with-local-secret@mockerp-db:5432/mockerp?sslmode=disable

ODAP_HTTP_ADDR=:8080
MOCKERP_HTTP_ADDR=:8081

OBJECT_STORE_ACCESS_KEY=replace-with-local-secret
OBJECT_STORE_SECRET_KEY=replace-with-local-secret

LLM_PROVIDER_MODE=mock
OBJECT_STORE_MODE=local
```

变量名必须在实际应用配置中统一。这里的 `LLM_PROVIDER_MODE=mock` 表示开发时可以使用确定性模拟模型响应，不需要一开始就依赖外部模型 API。`OBJECT_STORE_MODE=local` 表示未启动 `objectstore` profile 时使用本地文件系统目录替代 S3 兼容存储；启动该 profile 后改为使用 MinIO 端点，此时 `OBJECT_STORE_ACCESS_KEY`/`OBJECT_STORE_SECRET_KEY` 必须已设置（compose 中以 `:?` 断言，缺失即启动失败）。

> **实施修正（与本文示例的一处有意偏离）**：上面的示例把连接串写成 `postgres://odap:...`，即用**数据库属主**账号承载应用连接，这是不正确的。
> 属主账号是超级用户/可绕过 RLS 的角色，而第二篇 §9.2 要求**行级安全（RLS）必须在应用角色上真实生效**——用属主连接会让 RLS 静默失效（跨租户读写全部放行），且任何测试都发现不了。
> 因此实现中的连接串使用独立应用角色：
>
> ```
> ODAP_DATABASE_URL=postgres://odap_app:<password>@postgres:5432/odap?sslmode=disable
> ```
>
> `odap_app` 由迁移创建为 `LOGIN NOSUPERUSER NOBYPASSRLS`，其密码不在 `.env` 与迁移里各写一份（两处漂移风险），
> 而是**从 `ODAP_DATABASE_URL` 单一真值派生**：`cmd/migrate` 解析连接串取 `Password`，经 GUC `odap.app_password`
> 传给迁移 `000017_set_app_role_password.sql` 执行 `ALTER ROLE ... WITH LOGIN PASSWORD`。
> 属主账号仅用于建库与迁移管理，不参与任何应用请求路径。

正式环境应启用合适的数据库 TLS、凭据管理和网络隔离；不能直接照搬开发环境的 `sslmode=disable`。

## 8.4 Makefile

建议统一常用命令：

```

.PHONY: dev-up dev-down migrate seed test test-integration test-e2e lint

dev-up:
	docker compose up -d --build

dev-down:
	docker compose down

migrate:
	go run ./cmd/migrate up

seed:
	go run ./cmd/api seed --dataset finance-demo

test:
	go test ./...

test-integration:
	go test -tags=integration ./tests/integration/...

test-e2e:
	go test -tags=e2e ./tests/e2e/...

lint:
	golangci-lint run
	cd web && npm run lint

```

命令名可以调整，但应尽早固定团队共同使用的开发入口。数据库初始化和测试数据导入需要可重复执行，并且不能意外覆盖真实企业数据。

# 九、前端工程与页面实施顺序

前端不应先做大量静态大屏，而应优先完成支撑端到端业务的最小界面。

## 9.1 首期页面清单

| 优先级 | 页面             | 必须实现的能力              |
| --- | -------------- | -------------------- |
| P0  | 登录与工作区         | 用户身份、租户和角色上下文        |
| P0  | 业务工作台          | 创建报销审核任务、查看任务状态      |
| P0  | 运行详情           | 查看执行树、步骤状态、失败原因      |
| P0  | 审批中心           | 查看待审批内容、风险、参数摘要并处理审批 |
| P0  | Mock ERP 数据浏览器 | 查看报销单、员工和操作记录        |
| P1  | 本体工作台          | 查看类、属性、关系和验证发现项      |
| P1  | 语义查询工作台        | 提交受控查询并查看事实来源        |
| P1  | 审计与证据中心        | 查询执行记录、回执、证据包        |
| P1  | 评估与回归中心        | 运行测试、比较版本、查看门禁       |
| P2  | 插件与发布中心        | 管理插件、本体、Agent 和发布状态  |

## 9.2 运行详情页

运行详情页是 ODAP 最重要的前端页面之一。

建议分为三个主要区域：

顶部：任务摘要

任务 ID、业务对象、当前状态、Agent 版本、规则版本、开始时间。

左侧：执行树

读取对象 → 语义验证 → 规则判断 → 权限检查 → 审批 → ERP 操作。

右侧：步骤详情

输入输出摘要、耗时、错误代码、重试记录、状态变更。

底部：事实、审批与证据

事实来源、规则命中、授权决策、审批记录、ERP 回执和 Evidence Bundle。

这比只展示 Agent 对话更能体现 ODAP 的企业级价值。

## 9.3 前端工程约束

- TypeScript 开启严格类型检查。
- API 类型尽量从 OpenAPI 契约生成。
- 统一处理分页、错误、加载、取消和异步任务状态。
- 不在浏览器端保存 ERP 凭据或服务端秘密。
- 任何审批动作都应显示操作对象、业务影响和审批绑定内容。
- 前端隐藏按钮不能代替后端授权。
- 敏感数据应根据字段级权限决定是否返回，而不只是前端遮盖。

# 十、CI/CD 与自动化测试流水线

ODAP 的 CI 不能只执行 `go test ./...`。由于系统包含语义推理、权限、外部操作和异步恢复，必须分层验证。

## 10.1 推荐流水线

```
提交代码
   ↓
格式检查 / 静态分析
   ↓
Go 单元测试 + 前端类型检查
   ↓
数据库迁移验证
   ↓
API / Plugin / Event 契约测试
   ↓
模块集成测试
   ↓
Ontology / SHACL / Semantic 回归
   ↓
Mock ERP 端到端测试
   ↓
安全测试 + 故障注入
   ↓
Quality Gate
   ↓
构建不可变候选制品
   ↓
部署到隔离环境并冒烟测试
```

每一阶段应明确失败是否阻断后续步骤。高风险安全测试不应仅作为不影响发布的报告任务。

## 10.2 测试分层

### 单元测试

测试规则、状态机、授权决策、查询编译、参数校验和幂等键等纯逻辑。

### 契约测试

验证前后端 API、插件 Manifest、事件 Schema、ERP Port 和模型结构化输出的一致性。

### 集成测试

使用真实 PostgreSQL 和独立 Mock ERP，验证数据库事务、Outbox、操作账本、审批和回执处理。

### 端到端测试

从前端或 API 创建业务任务开始，验证整条工作流直至业务终态、审计记录与证据包。

### 故障注入测试

重点验证提交后响应丢失、重复消息、Worker 崩溃、权限撤销、版本冲突和证据服务故障。

### 安全测试

验证跨租户访问、对象越权、提示注入、工具绕过、审批摘要篡改、秘密泄露和危险网络访问。

## 10.3 发布硬门禁

以下问题建议作为 V1.0 发布的硬阻断条件：

- 未授权的外部写入。
- 未经要求的审批绕过。
- 跨租户数据泄露。
- 因重试产生重复的非幂等业务操作。
- 把未知业务状态错误报告为成功。
- 关键审计记录缺失或无法验证。
- 本体或规则回归造成严重业务判断错误。

对普通性能退化、非关键自然语言质量变化等问题，可以使用阈值和人工复核策略，但必须在发布政策中事先定义。

# 十一、分阶段工程交付计划

建议按依赖关系推进，而不是按十五篇文档的编号机械地逐篇实现。下面的周期是一个工程规划示例，实际排期取决于开发人数、团队经验和具体语义引擎的选型结果。

## 11.1 八个交付阶段

| 阶段                 | 主要交付                       | 阶段退出条件          |
| ------------------ | -------------------------- | --------------- |
| P0 工程底座            | 仓库、配置、日志、数据库、Compose、CI    | 开发者可重复启动，基础测试通过 |
| P1 身份与 Registry    | 用户、租户、角色、插件注册与版本           | 能注册、查询、停用一个测试插件 |
| P2 本体与语义           | 本体包、图存储、DSL、SHACL、基础推理     | 报销样例能查询并通过验证    |
| P3 Harness 与 Agent | 任务状态机、Plan IR、工具注册、Worker  | 可执行只读工作流并恢复中断任务 |
| P4 Mock ERP 写入     | Gateway、审批、幂等、操作账本、回执      | 完成一次受控模拟提交并对账   |
| P5 审计与证据           | 审计服务、Evidence Bundle、完整性验证 | 能追溯业务结果及其依据     |
| P6 评估与发布门禁         | 固定数据集、回归、故障注入、Quality Gate | 不合格候选版本能够被阻断    |
| P7 完整 MVP          | 前端工作台、端到端演示、部署与运维文档        | 所有关键验收场景通过      |

## 11.2 P0：工程底座

先完成：

- 创建仓库与模块目录。
- 建立配置加载和错误处理规范。
- 接入 PostgreSQL 与迁移框架。
- 建立 API Server 和 Mock ERP 的健康检查。
- 建立 Go 单元测试与前端类型检查。
- 固定本地开发命令。
- 加入基本的结构化日志和请求关联 ID。

P0 完成时，不需要 Agent、LLM 或知识图谱已经工作。目标是确保工程基础稳定。

## 11.3 P1：身份与插件注册

实现：

- 最小用户和租户模型。
- RBAC 与对象范围校验。
- Agent、Tool、Ontology、Policy 的基础 Manifest。
- Registry 注册、查询、版本锁定和停用。
- 管理接口的权限校验。

阶段验收：注册一个测试 Agent，查询其版本、依赖和状态；没有授权的用户不能修改或停用该 Agent。

## 11.4 P2：本体与语义运行时

实现：

- Finance Ontology 包。
- RDF Terms、Named Graphs 和 Triples。
- 受控 Semantic Query DSL。
- 类型检查与查询编译。
- 基础 RDFS 推理。
- SHACL 验证适配器。
- 查询结果的来源与版本元数据。

阶段验收：给定固定报销单数据，系统可以返回员工、部门、费用类别和凭证关系；缺少必需关系时产生明确验证发现项。

## 11.5 P3：Harness 与 Agent

先用确定性 Planner 或固定计划模板完成第一条执行链，再接入 LLM 生成候选计划。

实现：

- Run、Step、Attempt 和 Checkpoint。
- Plan IR Schema 与验证器。
- Tool Registry 和受控参数。
- Outbox、Worker 租约和恢复逻辑。
- Planner、Executor、Verifier 的最小职责分离。

阶段验收：任务中断后可以恢复；恢复过程不会无条件重复已完成的非幂等动作。

## 11.6 P4：Mock ERP 与受控写入

实现：

- 独立 Mock ERP 数据库。
- ERP Port 与 Adapter。
- Operation Gateway。
- 授权和审批绑定。
- 幂等键和操作账本。
- 回执验证与未知结果对账。

阶段验收：执行报销提交，检查业务状态、操作账本、审批记录和回执；注入响应丢失后，系统不能直接再次提交。

## 11.7 P5：审计与证据

实现：

- 统一关联 ID。
- 关键业务事件审计。
- Evidence Bundle。
- 版本锁定清单。
- 事实、规则、授权、审批、ERP 回执的引用关系。
- 证据完整性验证接口。

阶段验收：可以针对任意一次成功或失败运行，重建其主要执行路径与业务依据。

## 11.8 P6：评估与门禁

实现：

- 报销标准测试集。
- 确定性断言引擎。
- 本体与规则回归。
- Mock ERP 故障注入。
- 基线与候选版本比较。
- 发布硬门禁。

阶段验收：人为制造一个审批绕过或重复操作缺陷，测试能够复现问题并阻止候选版本发布。

## 11.9 P7：端到端 MVP

最后完善：

- 前端业务工作台。
- 运行追踪和审批界面。
- 语义查询与本体验证界面。
- 审计证据查看。
- 评估报告与发布门禁。
- 部署、备份、恢复和故障处理文档。

阶段验收：新开发环境可以按照文档初始化、导入演示数据、运行完整业务流程和执行回归测试。

# 十二、开发团队、任务拆分与协作机制

如果是小团队，建议采用领域模块负责人制度，不必为每个模块配置独立团队。

## 12.1 建议的角色

- 架构负责人： 冻结模块边界、接口和 ADR（架构决策记录）。
- Go 后端负责人： API、权限、Harness、ERP Gateway、异步执行。
- 语义工程负责人： 本体、RDF、SHACL、DSL、推理适配。
- 前端负责人： React/TypeScript 工作台和运行追踪。
- 测试与质量负责人： 测试集、端到端、故障注入和发布门禁。
- 业务规则负责人： 确认报销流程、制度含义、例外情况和预期结果。

一个人可以承担多个角色，但业务规则的确认不应完全由编写规则代码的人自行完成。

## 12.2 每个模块的交付物

每个开发任务都应至少包含：

1. 模块职责和边界。
2. 输入输出契约。
3. 数据库变更。
4. 正常流程。
5. 失败和未知状态处理。
6. 权限要求。
7. 单元与集成测试。
8. 日志、指标和审计要求。
9. 验收标准。
10. 对其他模块的依赖。

例如，ERP Gateway 的交付不能只包括一个 `Execute()` 函数，还必须包括幂等、授权、审批绑定、回执核验、错误状态和故障注入测试。

## 12.3 架构决策记录

跨分册冲突收敛类 ADR 已沉淀在 `docs/adr/` 目录（全平台权威，各分册引用不重定义）；模块级决策记录在 `docs/decisions/`。两类合起来构成完整决策清单：

```
# docs/adr/（跨分册收敛，已接受）
ADR-001-PlanIR合并规范.md            # 计划体系四份定义合并（第六/八/十四/十六篇）
ADR-002-Registry数据模型与版本锁定合并.md  # Registry 二选一 + RunVersionLock 合并 + 审批摘要公式统一
ADR-003-自研与开源选型评估.md          # 工作流编排/队列/LLM 网关/RDF 存储四领域选型约束
ADR-005-图存储选型与GraphStore适配器边界.md  # Fuseki/TDB2 与 PG 引擎并存 + GraphStore 适配器边界
                                     # 注：ADR-004 已由第十七篇 §10.3 预定给"前端数据层选型"（待评审）

# docs/decisions/（模块级）
ADR-0001-modular-monolith.md
ADR-0002-postgres-platform-storage.md
ADR-0003-mock-erp-isolation.md
ADR-0004-plan-ir-contract.md          # 已被 docs/adr/ADR-001 取代，保留仅作历史记录
ADR-0005-ontology-engine-adapter.md
ADR-0006-outbox-and-worker-lease.md
ADR-0007-erp-idempotency-and-reconciliation.md
ADR-0008-evidence-bundle-integrity.md
ADR-0009-quality-gate-hard-blocks.md
```

> 注意 `docs/adr/ADR-005` 与 `docs/decisions/ADR-0005` 是**两个独立编号空间**（前者四位无前导零、跨分册收敛；后者四位带前导零、模块级），不要互相引用。

每份 ADR 至少记录：背景、备选方案、最终决定、理由、代价、后续复核条件。`docs/adr/` 中的四份另按统一结构记录状态（已接受）、日期、后果与关联文档，并在触发重新评估的条件满足时复审（ADR-003 与 ADR-005 每项均显式列出了触发条件）。

这能避免团队在不同模块中反复做出相互冲突的技术决定。

# 十三、MVP 风险清单与应对措施

| 风险           | 可能后果            | 应对策略                    |
| ------------ | --------------- | ----------------------- |
| 首期范围过大       | 大量模块存在，但主链路无法运行 | 冻结报销业务域，按垂直切片交付         |
| 本体引擎选型不当     | 语义能力不足或集成困难     | 先定义适配接口，以实际兼容性测试决定选型    |
| Agent 自由度过高  | 计划不可控，难以复现      | 使用 Plan IR、受控工具与确定性验证   |
| 权限只在前端实现     | API 可被直接调用绕过    | 服务端统一授权，关键边界重复检查        |
| ERP 写入没有对账   | 超时后可能重复提交       | 操作账本、幂等键、未知状态和恢复控制器     |
| 审批与参数脱离      | 审批通过后操作内容被改变    | 审批绑定不可变参数摘要，执行前重新校验     |
| 数据库边界混乱      | 平台状态与业务事实冲突     | Mock ERP 独立数据库，明确权威数据来源 |
| 评估只看总分       | 安全问题被普通指标掩盖     | 安全硬门禁独立判定               |
| 证据过度收集       | 敏感数据暴露和存储成本增加   | 最小化采集、访问控制、保留期限         |
| 过早拆微服务       | 开发与部署复杂度激增      | 模块化单体，依据实际瓶颈拆分          |
| 过早引入 Rust 内核 | 增加跨语言维护成本       | 先基准测试，确认瓶颈后再替换          |
| 只做正常流程       | 上线后遇到未知结果无法恢复   | 把故障注入作为强制验收项            |

其中有三项应被视为架构底线：

第一，Agent 不拥有业务写入权限。 它提出动作，平台完成授权和执行。

第二，模型输出不是业务事实。 事实必须来自可识别的数据源、验证结果或明确标记的推导。

第三，外部操作未知时不盲目重试。 必须通过幂等机制、查询和对账确认结果，必要时转人工处理。

# 十四、V1.0 最终交付物与验收标准

当 ODAP MVP 完成时，仓库中应当至少包含以下可运行资产。

## 14.1 必须交付的工程资产

- 一个可以正常构建的 Go 后端工程。
- 一个可以独立启动的 React/TypeScript 前端。
- 一个独立运行的 Mock ERP 服务。
- 可重复执行的数据库迁移脚本。
- 一套 Finance Ontology 与报销演示数据。
- 受控语义查询、基础推理与 SHACL 验证。
- Agent Runtime、Plan IR 与 Harness。
- 权限、审批和 ERP Operation Gateway。
- Outbox、Worker、幂等和未知结果对账。
- 审计事件与 Evidence Bundle。
- 固定评估数据集、端到端测试和 Quality Gate。
- Docker Compose 开发环境。
- API 契约、架构决策记录和部署手册。
- 故障处理、备份恢复和安全验收说明。

## 14.2 端到端验收矩阵

| 测试编号    | 场景          | 通过标准                   |
| ------- | ----------- | ---------------------- |
| E2E-001 | 正常报销审核      | 完成预期业务流程，产生可核验的结果      |
| E2E-002 | 缺少必需凭证      | 阻止不合规提交，给出明确发现项        |
| E2E-003 | 用户无权提交      | 不发生 ERP 写入             |
| E2E-004 | 需要人工审批      | 写入步骤暂停，审批完成后按策略继续      |
| E2E-005 | 审批后参数变化     | 摘要或版本校验失败，阻止执行         |
| E2E-006 | 重复消息        | 不产生重复业务操作              |
| E2E-007 | ERP 响应丢失    | 进入未知结果处理并执行对账          |
| E2E-008 | Worker 崩溃恢复 | 根据持久化状态恢复，不盲目重放外部操作    |
| E2E-009 | 跨租户访问       | 拒绝访问，生成必要的安全审计记录       |
| E2E-010 | 本体版本升级      | 回归测试发现未预期的语义变化         |
| E2E-011 | 审计证据被修改     | 完整性验证能够发现被检测范围内的修改     |
| E2E-012 | 安全门禁失败      | 候选版本不能通过正常发布流程         |
| E2E-013 | 证据服务暂时不可用   | 不错误地宣称关键证据已完整生成        |
| E2E-014 | 数据源过期       | 正确标记数据新鲜度问题并阻止不安全的自动执行 |

以上十四项是建议的最低验收集，不代表完整的生产安全认证。每项都应有自动化测试、明确断言和测试运行记录。

## 14.3 验收应达到的程度

不建议只要求“页面能打开”“接口返回 200”或“Agent 能给出回答”。

每个关键业务场景都应回答四个问题：

1. 业务结果是否正确？ 以 Mock ERP 的权威状态和确定性断言为准。
2. 执行过程是否安全？ 以授权、审批、操作账本和故障测试为准。
3. 执行依据是否可追溯？ 以版本锁定、事实来源、审计和证据验证为准。
4. 失败是否可恢复？ 以状态机、幂等、对账和故障注入为准。

只有四项都通过，才能认为对应场景真正完成。

# 十五、从 MVP 进入企业试点的演进路线

MVP 验收通过后，不应直接把 Mock ERP 替换为真实 ERP 并开放所有操作。建议分阶段演进。

阶段 A：本地 MVP

使用固定数据、Mock ERP 和确定性模型响应，验证架构、状态机和完整业务闭环。

阶段 B：真实数据只读试点

连接企业授权的数据源，先验证字段映射、数据新鲜度、语义正确性和只读权限，不开放自动写入。

阶段 C：人工确认式执行

Agent 生成建议和执行计划，用户审核后由 Gateway 执行；每次写入均进行授权、审批与结果核验。

阶段 D：受限自动执行

只对已经验证、低风险、具有明确授权策略的操作开放自动执行；保留审计、监控、停止与恢复机制。

阶段 E：多业务域与企业级部署

扩展合同、采购、人力资源等本体与 Agent；根据实际规模再引入专用 RDF 存储、消息代理、Kubernetes 或独立计算内核。

真实 ERP 的只读接入也需要数据授权、字段级权限和数据分类；不能因为不写入，就默认所有数据都可以被 Agent 读取。

# 十六、最终工程建议：从哪十件事开始

如果现在就开始开发，我建议严格按照下面的顺序推进，而不是同时启动所有模块。

## ODAP 工程启动清单

0 / 10

1\. 创建 ODAP Monorepo、Go 工程、React/TypeScript 工程与基础 CI。

2\. 启动 PostgreSQL，建立迁移框架和租户上下文。

3\. 建立独立 Mock ERP 数据库、报销单模型与操作接口。

4\. 定义统一的 ExpenseClaim 业务模型和 Finance Ontology 包。

5\. 实现最小 GraphStore、Semantic Query DSL 与 SHACL 验证。

6\. 实现身份、对象权限、审批请求和审批参数摘要。

7\. 实现 Plan IR、Harness 状态机与持久化 Worker。

8\. 实现 ERP Operation Gateway、幂等操作账本和对账流程。

9\. 完成审计、Evidence Bundle 和十二至十四个核心端到端案例。

10\. 实现评估门禁，并让 CI 自动运行完整报销业务回归。

复制工程启动清单

## 最后的判断

ODAP 的竞争力不应只是“能调用多少个模型”或“能配置多少个 Agent”，而应当来自一套更深层的能力：

- 企业制度能够被结构化为可治理的本体和规则。
- 企业数据能够被映射为可查询、可验证的语义事实。
- Agent 能够根据业务目标生成受控的执行计划。
- Harness 能够可靠地执行、暂停、恢复和处理未知结果。
- 权限、审批和 ERP Gateway 能够约束真实业务动作。
- 审计与证据系统能够解释业务结果及其依据。
- 评估和回归平台能够持续证明系统没有因为升级而退化。

我的核心建议是：先把一个报销业务场景做深、做透、做成可重复验证的闭环，再扩展为通用的企业本体驱动智能体平台。

这既能尽快得到可运行的软件，也能避免 ODAP 最终停留在“架构很完整、模块很多、却无法稳定完成一个真实业务任务”的状态。
