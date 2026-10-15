# 《第七篇：ERP Port、Adapter 与 Mock ERP 详细设计》

ODAP · Ontology-Driven Agent Platform · Enterprise Integration Architecture

本篇承接 ODAP 前六篇设计，重点解决一个决定平台能否真正落地的问题：

如何让 Agent 通过统一、受控、可审计的接口操作企业 ERP，而不让智能体与具体 ERP 的数据结构、API 协议和事务机制深度耦合？

ODAP 的目标不是重新开发一套 ERP，也不是让大模型直接连接企业数据库，而是在 ERP 之上建立一个本体驱动、权限受控、可以验证和审计的智能体执行层。

因此，本篇采用以下总体原则：

- ERP 是业务事实与事务的权威来源。 ODAP 不应未经明确设计就复制或取代 ERP 的账务、采购、员工和审批数据。
- ERP Port 是稳定的业务操作契约。 Agent 和 Harness 只依赖 Port，不直接依赖 SAP、用友、金蝶或其他 ERP 的专有接口。
- Adapter 负责协议转换。 它处理字段映射、身份转换、请求签名、分页、错误归一化和外部回执。
- Mock ERP 是正式工程组件。 它不仅用于展示界面，还用于模拟业务状态、事务冲突、网络故障、重复请求和超时等情况。
- 所有外部写操作都必须经过 Harness、授权引擎和操作账本。 Adapter 不得成为绕过平台控制的后门。
- 不假定跨系统事务天然原子化。 外部写入采用幂等、回执核验、对账及必要的补偿机制。

## 1. 本篇目标与架构边界

### 1.1 要实现的能力

完成本篇后，ODAP 应具备以下基础能力：

1. 使用统一接口查询员工、部门、报销单、合同、供应商等业务对象。
2. 使用标准操作契约提交报销、创建采购申请、更新业务记录等操作。
3. 通过不同 Adapter 连接不同 ERP，而不修改 Agent 的业务逻辑。
4. 将 ERP 原生字段映射到 ODAP 的标准对象模型和企业本体。
5. 对每次外部写入生成稳定的操作标识和幂等键。
6. 对超时、重复回调和结果未知的操作进行安全对账。
7. 在 Mock ERP 中模拟成功、拒绝、冲突、限流和部分故障。
8. 为将来连接真实 ERP 提供清晰的接口、配置和测试规范。

### 1.2 总体架构

### ODAP Agent

提出业务目标与候选操作

### Harness Execution Engine

计划校验、状态机、任务恢复、检查点、调用调度

Authorization Engine

主体、资源、操作、数据范围

Approval Service

高风险操作人工审批

### ERP Port + Operation Gateway

统一对象、统一操作、幂等、账本、请求校验与回执标准化

Mock ERP

开发与故障注入

ERP Adapter A

协议与字段映射

ERP Adapter B

另一种 ERP 实现

ERP / Enterprise Systems

外部业务事实、业务事务和原生审计记录

图中需要特别区分两个概念：

- ERP Port 定义平台需要什么业务能力。
- ERP Adapter 负责将这种能力映射到某个实际系统。

例如，Agent 只需要知道 `expense.submit` 是提交报销申请，不应知道目标 ERP 的原生 API 是 `/api/fin/exp/apply`，还是需要先调用多个接口完成草稿创建、校验和提交。

### 1.3 组件职责

| 组件                    | 核心职责            | 禁止承担的职责          |
| --------------------- | --------------- | ---------------- |
| ERP Port              | 定义稳定的业务接口与契约    | 直接依赖某个 ERP 的私有协议 |
| Operation Gateway     | 校验操作、幂等、账本和执行边界 | 绕过授权直接写入 ERP     |
| Adapter               | 协议转换、字段映射、远端调用  | 自行批准业务操作         |
| Mock ERP              | 模拟真实业务状态与故障     | 成为生产环境的权威业务数据源   |
| ERP Operation Ledger  | 记录操作意图、执行状态和回执  | 代替 ERP 的业务账簿     |
| Reconciliation Worker | 查询远端状态、消除未知结果   | 在不确定时盲目重发写请求     |
| Mapping Registry      | 管理字段、枚举和对象映射版本  | 静默覆盖已发布映射        |

## 2. 统一 ERP Port 设计

### 2.1 为什么不能直接使用通用 CRUD

如果只定义如下接口：

```
Create(ctx, object)
Read(ctx, id)
Update(ctx, id, object)
Delete(ctx, id)
```

虽然简单，却会丢失企业业务语义。

在 ERP 中，以下操作并不等价：

- 创建报销单与提交报销单。
- 创建采购申请与批准采购申请。
- 更新合同草稿与签署合同。
- 修改付款信息与执行付款。
- 删除草稿与冲销已入账凭证。

这些操作拥有不同的业务前置条件、权限、审批要求和副作用。

因此，ODAP 应当同时提供：

1. 查询型接口：读取业务对象、状态、版本和关联关系。
2. 命令型接口：执行明确的业务动作。
3. 能力发现接口：查询当前 ERP 连接实际支持什么。
4. 回执与对账接口：确认操作是否真正完成。

### 2.2 Port 的业务域划分

建议先定义一组稳定的业务域：

| 业务域          | 核心对象                                 | 示例操作          |
| ------------ | ------------------------------------ | ------------- |
| Organization | Tenant、Department、Employee           | 查询组织、员工与汇报关系  |
| Finance      | ExpenseClaim、Invoice、Voucher         | 提交报销、查询凭证状态   |
| Procurement  | PurchaseRequest、PurchaseOrder        | 创建采购申请、提交审批   |
| Contract     | Contract、ContractParty               | 创建草稿、提交审查     |
| Supplier     | Supplier、SupplierAccount             | 查询供应商、核验供应商状态 |
| Approval     | ApprovalRequest、ApprovalDecision     | 查询审批状态、获取审批记录 |
| Master Data  | Currency、CostCenter、Project          | 查询标准编码与有效性    |
| Evidence     | Receipt、Attachment、DocumentReference | 关联凭证、查询附件元数据  |

这些是 ODAP 的业务语义分类，不意味着每一种 ERP 都拥有完全相同的原生对象。

例如，某些 ERP 将报销单归在费用管理模块，另一些系统可能通过财务单据、工作流实例和附件接口共同表达报销业务。Adapter 负责适配这些差异。

### 2.3 标准对象模型

建议平台使用稳定的 `Canonical Model`（规范业务对象模型），并将它与 OWL/RDF 本体分开管理。

两者的职责不同：

- Canonical Model 规定接口交换的数据结构和操作契约。
- Ontology 规定业务对象的语义、概念关系、约束和推理规则。
- ERP 原生模型规定具体 ERP 的字段、编码和事务行为。

三者的关系如下：

```
ERP Native Model
       ↕
ERP Adapter Mapping
       ↕
ODAP Canonical Model
       ↕
Ontology Runtime / Business Rules
```

规范对象模型不应试图覆盖 ERP 的每一个原生字段。与当前任务无关的扩展字段可以放在受控扩展区，但核心字段必须有明确类型和语义。

### 2.4 规范对象的公共字段

建议所有核心业务对象共享以下元数据：

```
{
  "object_type": "ExpenseClaim",
  "object_id": "expense-001",
  "tenant_id": "tenant-001",
  "external_ref": {
    "connection_id": "erp-001",
    "native_object_type": "ExpenseApplication",
    "native_id": "ERP-EXP-8821"
  },
  "source_version": "17",
  "mapping_version": "1.0.0",
  "observed_at": "2026-10-12T09:00:00Z",
  "data_quality": "VERIFIED"
}
```

以上仅为字段设计示例。

`source_version` 表示外部系统版本或版本标识；如果目标 ERP 不支持可靠版本号，Adapter 可以使用该系统提供的更新时间、ETag 或其他条件版本机制，但必须声明其可靠性。

`observed_at` 是 ODAP 观察到该数据的时间，不代表该业务对象最后修改时间。

`data_quality` 用于区分已确认、部分映射、陈旧或存在冲突的数据。不能把缺少字段简单转换成默认值后，再将结果标记为已验证。

## 3. ERP Port 的接口契约

### 3.1 统一接口设计

建议使用 Go 接口表达业务能力，并通过 `context.Context` 传递超时与取消信号。

```
package erpport

import "context"

type ConnectionID string
type ObjectID string
type OperationID string

type ERPPort interface {
    Capabilities(
        ctx context.Context,
        connectionID ConnectionID,
    ) (Capabilities, error)

    GetObject(
        ctx context.Context,
        req GetObjectRequest,
    ) (ObjectResult, error)

    QueryObjects(
        ctx context.Context,
        req QueryObjectsRequest,
    ) (QueryResult, error)

    Execute(
        ctx context.Context,
        req ExecuteRequest,
    ) (OperationResult, error)

    GetOperation(
        ctx context.Context,
        req GetOperationRequest,
    ) (OperationResult, error)

    Health(
        ctx context.Context,
        connectionID ConnectionID,
    ) (HealthResult, error)
}
```

这是平台级接口，不要求所有 ERP 原生提供同名 API。Adapter 可以用一个远端接口实现，也可以编排多个远端接口完成一个业务动作。

例如，ERP 不提供直接的 `expense.submit` API 时，Adapter 可以先创建草稿，再执行校验，然后调用提交接口。但整个过程必须明确声明中间状态和可能产生的副作用，不能把多个外部调用伪装成一个具有原子性的事务。

### 3.2 查询请求与响应

查询接口应采用受控的查询 DSL，而不是允许 Agent 任意拼接 SQL 或直接提交任意原生查询语言。

```
type GetObjectRequest struct {
    TenantID     string
    ConnectionID ConnectionID
    ObjectType   string
    ObjectID     ObjectID
    Fields       []string
}

type QueryObjectsRequest struct {
    TenantID     string
    ConnectionID ConnectionID
    ObjectType   string
    Filter       FilterExpression
    Fields       []string
    PageSize     int
    Cursor       string
    OrderBy      []OrderClause
}

type ObjectResult struct {
    Object     CanonicalObject
    Version    string
    ObservedAt time.Time
}

type QueryResult struct {
    Items      []CanonicalObject
    NextCursor string
    HasMore    bool
}
```

正式实现中，`FilterExpression` 只能使用受控运算符和字段白名单。字段是否可查询，还应受到主体权限、对象范围和租户策略约束。

不应允许模型在请求中直接指定未经授权的租户 ID、内部连接凭据或 Adapter 私有配置。

### 3.3 命令请求：ExecuteRequest

写操作应以明确的业务命令表达，而不是把任意对象更新交给 Adapter。

```
type ExecuteRequest struct {
    TenantID       string
    ConnectionID   ConnectionID
    OperationID    OperationID

    OperationType  string
    TargetObjectID ObjectID

    Payload        map[string]any
    PayloadDigest  string

    IdempotencyKey string
    ExpectedVersion string

    AuthorizationDecisionID string
    ApprovalID              string

    Deadline time.Time
    TraceID  string
}
```

正式代码建议将不同业务命令定义为有类型的结构体，而不是长期使用无约束的 `map[string]any`。

例如：

```
type SubmitExpenseRequest struct {
    ExpenseClaimID string
    ExpectedVersion string
    Comment         string
}
```

由业务命令注册表将 `expense.submit` 映射到 `SubmitExpenseRequest` 的 Schema 和处理器。

以下字段需要特别注意：

- `PayloadDigest` 必须由可信服务按照规范化规则计算，不能直接相信 Agent 提交的摘要。
- `IdempotencyKey` 必须绑定一个明确的业务意图。
- `ExpectedVersion` 用于防止操作覆盖已经变化的业务对象。
- `AuthorizationDecisionID` 和 `ApprovalID` 是可追踪引用，不能替代重新验证授权和审批状态。
- `Deadline` 是平台执行期限，Adapter 仍需遵守自身连接和调用超时策略。

### 3.4 OperationResult

建议将调用结果与业务操作结果分开：

```
type OperationResult struct {
    OperationID  OperationID
    Status       OperationStatus

    RemoteRef    string
    ReceiptID    string

    PayloadDigest string
    ResultVersion string

    Retryable    bool
    Error        *ERPError

    StartedAt    *time.Time
    CompletedAt  *time.Time
}
```

推荐状态至少包括：

```
ACCEPTED
IN_PROGRESS
SUCCEEDED
FAILED
REJECTED
UNKNOWN
RECONCILING
MANUAL_REVIEW
```

`ACCEPTED` 表示对方已接受请求或任务，不等于业务已经完成。`UNKNOWN` 表示当前无法确认外部操作的最终结果。`REJECTED` 表示操作被明确拒绝，而不是普通网络错误。

## 4. ERP 能力发现与连接管理

### 4.1 为什么需要 Capabilities

不能假设所有 ERP Adapter 都支持完全相同的能力。例如：

- 一个 ERP 可以查询报销单，但不支持通过 API 提交报销。
- 一个 ERP 支持查询操作状态，另一个只返回同步结果。
- 一个 ERP 支持幂等键，另一个没有此功能。
- 一个 ERP 支持对象版本检查，另一个只提供更新时间。
- 一个 ERP 允许读取审批历史，但不允许外部系统创建审批任务。

Harness 必须在任务执行前知道这些差异。

### 4.2 能力声明

```
{
  "connection_id": "erp-001",
  "adapter_id": "com.odap.adapter.mock-erp",
  "adapter_version": "1.0.0",
  "capabilities": {
    "objects": [
      "Employee",
      "Department",
      "ExpenseClaim"
    ],
    "operations": [
      "expense.create_draft",
      "expense.submit",
      "expense.get_status"
    ],
    "features": {
      "idempotency": true,
      "operation_lookup": true,
      "optimistic_concurrency": true,
      "remote_approval": false,
      "receipt_verification": true
    }
  }
}
```

此处仅为 Mock ERP 的示例能力清单。

能力声明应由 Adapter 注册、管理员审核，并经过连接测试。不能允许 Agent 自行声明某个 ERP 支持幂等、回滚或操作查询。

### 4.3 ERP Connection 模型

建议在 PostgreSQL 中维护连接元数据：

```
erp.connections
erp.connection_capabilities
erp.connection_secrets_refs
erp.connection_health_checks
erp.connection_mappings
erp.connection_audit_events
```

连接记录应包含：

- 连接 ID 与租户归属。
- ERP 类型与环境标识。
- Adapter ID、版本与摘要。
- 连接状态与健康状态。
- 已启用的业务域和操作能力。
- 字段映射版本。
- 凭据引用，而非明文凭据。
- 网络访问策略和允许的目标地址。
- 最近一次健康检查时间。
- 创建、修改、审核与停用记录。

### 4.4 连接状态

建议使用以下状态：

```
DRAFT
  → CONFIGURED
  → VALIDATING
  → ACTIVE
  → DEGRADED
  → DISABLED
```

其中：

- `DRAFT`：刚创建。
- `CONFIGURED`：配置已保存。
- `VALIDATING`：正在测试连接和权限。
- `ACTIVE`：通过测试，可以被调度。
- `DEGRADED`：部分能力不可用或连接质量下降。
- `DISABLED`：禁止新操作。

连接进入 `DEGRADED` 时，可以根据能力允许只读查询继续运行，但写操作必须根据业务风险与连接状态策略重新评估。连接被停用后，不应继续接受新的写请求；已发出的请求则需要保留账本并按规则对账。

## 5. Adapter 插件架构

### 5.1 Adapter 的分层

建议将每个 Adapter 拆分为六层：

```
Adapter Manifest
      ↓
Connection Client
      ↓
Native API Client
      ↓
Native DTO Mapping
      ↓
Canonical Model Mapping
      ↓
ERP Port Implementation
```

更完整的实现还需要包含认证模块、限流器、重试策略、远端错误映射和回执解析器。

各层职责如下：

| 层                       | 作用                   |
| ----------------------- | -------------------- |
| Adapter Manifest        | 声明身份、版本、能力和配置 Schema |
| Connection Client       | 管理连接、超时、认证和网络策略      |
| Native API Client       | 调用 ERP 原生 API        |
| Native DTO Mapping      | 处理 ERP 原生请求与响应结构     |
| Canonical Mapping       | 转换为 ODAP 规范对象与命令     |
| ERP Port Implementation | 对外提供统一业务接口           |

### 5.2 Adapter Manifest

建议沿用第四篇插件协议：

```
apiVersion: odap.io/v1
kind: ERPAdapter

metadata:
  id: com.odap.adapter.mock-erp
  version: 1.0.0
  publisher: odap-core

spec:
  protocolVersion: "1.0"
  supportedDomains:
    - organization
    - finance

  supportedOperations:
    - expense.create_draft
    - expense.submit
    - expense.get_status

  features:
    idempotency: true
    operationLookup: true
    optimisticConcurrency: true
    receiptVerification: true

  runtime:
    isolation: container
    networkPolicy: restricted

  configSchema: schemas/connection-config.schema.json
```

正式注册时还应保存制品摘要、签名、兼容的 Port 版本、依赖锁定、测试报告与供应链证据。

`features` 只是能力声明，关键能力仍应通过契约测试或管理员验证。对于声称支持幂等的 Adapter，必须测试重复请求、并发请求和服务崩溃后的行为。

### 5.3 Adapter 不能直接操作平台数据库

Adapter 可以访问它被授权使用的 ERP 接口，但不应直接修改：

- `runtime.agent_runs`
- `runtime.approvals`
- `runtime.erp_operations`
- `runtime.run_events`

Adapter 的结果必须通过 Harness 或 Operation Gateway 返回。由平台可信服务决定如何更新状态、写入审计和推进任务。

这可以避免插件通过直接修改数据库来伪造任务成功、绕过审批或删除错误记录。

## 6. 字段映射与数据语义转换

### 6.1 为什么字段映射不能只是改名

不同 ERP 的差异不只是字段名不同，还可能包括：

- 编码体系不同。
- 状态枚举不同。
- 金额单位与币种处理不同。
- 时间字段代表的业务含义不同。
- 一个业务对象被拆分为多个原生对象。
- 同一个原生字段在不同业务状态下含义不同。
- 空值、缺失字段与默认值的语义不同。
- 原生系统的主键与 ODAP 对象 ID 不一致。

因此，Mapping Registry 应管理的不仅是字段对应关系，还包括类型转换、枚举转换、空值语义、单位转换、版本兼容与数据质量规则。

### 6.2 Mapping 配置示例

```
{
  "mapping_id": "expense-claim-mapping",
  "version": "1.0.0",
  "source": {
    "system": "mock-erp",
    "object_type": "ExpenseApplication"
  },
  "target": {
    "canonical_type": "ExpenseClaim"
  },
  "fields": [
    {
      "source": "applicationNo",
      "target": "external_ref.native_id",
      "transform": "string"
    },
    {
      "source": "totalAmount",
      "target": "amount.value",
      "transform": "decimal"
    },
    {
      "source": "currencyCode",
      "target": "amount.currency",
      "transform": "currency_code"
    },
    {
      "source": "approvalStatus",
      "target": "workflow_status",
      "transform": "enum_map",
      "enum_map": {
        "DRAFT": "DRAFT",
        "PENDING": "PENDING_APPROVAL",
        "APPROVED": "APPROVED",
        "REJECTED": "REJECTED"
      }
    }
  ]
}
```

这是映射格式示例，不是通用 ERP 标准。每个真实 Adapter 都需要基于实际接口和业务文档定义自己的映射。

### 6.3 金额与时间的规范化

金额应使用十进制定点类型或精确十进制字符串，避免直接使用 IEEE 754 二进制浮点数表达财务金额。

推荐：

```
{
  "value": "1280.50",
  "currency": "CNY"
}
```

而不是将金额统一存成不带币种的浮点数。

同时，金额转换、汇率转换、税额计算和舍入规则必须明确记录。ODAP 不应在不知道目标 ERP 规则的情况下自行推测精度。

时间字段则需要区分：

- 业务日期，例如报销所属日期。
- 事件发生时间。
- 远端系统最后修改时间。
- ODAP 观察时间。
- 带时区的时间戳。

不能把没有时区的业务日期自动解释为 UTC 时间戳。

### 6.4 未知、缺失与不支持

映射结果应区分：

```
PRESENT
MISSING
NULL
UNMAPPED
UNSUPPORTED
CONFLICT
STALE
```

这些状态的语义不同：

- `MISSING`：来源数据没有提供该字段。
- `NULL`：来源明确提供空值。
- `UNMAPPED`：有来源字段，但没有已批准的转换规则。
- `UNSUPPORTED`：目标 Port 无法表达该能力。
- `CONFLICT`：不同来源给出不一致的值。
- `STALE`：数据超过允许的新鲜度。

在涉及权限、合规、审批和财务判断时，缺失字段不能自动补成一个“安全”的默认值。

### 6.5 映射版本发布

建议映射版本采用不可变发布方式：

1. 创建候选映射。
2. 运行 Schema 与类型检查。
3. 执行样例数据转换。
4. 检查必需字段覆盖率。
5. 执行回归测试。
6. 生成语义差异报告。
7. 由管理员审核发布。
8. 将映射版本绑定到新任务。

已运行任务不应因为映射更新而静默改变解释结果。

## 7. ERP Operation Gateway：统一写入控制

这是 ODAP 与 ERP 之间最关键的安全边界之一。

### 7.1 写入执行链

所有 ERP 写操作必须经过统一 Gateway：

```
Agent Intent
    ↓
Harness Plan Validator
    ↓
Ontology / Business Validation
    ↓
Authorization Check
    ↓
Approval Check
    ↓
Canonical Command Validation
    ↓
Create ERP Operation Ledger
    ↓
Recheck Authorization & Object Version
    ↓
Adapter Dispatch
    ↓
Receipt Verification
    ↓
Persist Result + Audit Event
```

其中，创建操作账本必须发生在远端调用之前。

如果操作要求审批，Gateway 不应提前调用 Adapter；它应持久化待审批操作，并在审批通过后重新校验。

### 7.2 操作账本与命令不可变性

每个操作至少绑定：

- 操作 ID。
- 租户和 ERP 连接 ID。
- 操作类型与目标对象。
- 规范化请求摘要。
- 幂等键。
- 预期对象版本。
- 授权决策引用。
- 审批引用。
- Adapter 和映射版本。
- 外部回执与远端引用。
- 执行尝试记录。

如果操作参数发生实质变化，应创建新的操作意图或重新进行审批，而不是覆盖已经发送的操作内容。

### 7.3 事务边界

本地数据库事务只负责本地数据的一致性，例如：

- 保存操作意图。
- 写入操作状态。
- 保存授权和审批引用。
- 追加事件与 Outbox。

远端 ERP 事务由 ERP 自身负责。两者之间通过幂等键、回执和对账实现业务层面的可靠协作。

不能采用这样的伪事务逻辑：

```
BEGIN PostgreSQL
  INSERT operation
  CALL remote ERP
  UPDATE operation
COMMIT
```

因为远端调用可能超时或成功后失联，而本地事务仍然无法知道远端是否已提交。长时间持有数据库事务也会增加锁竞争和资源耗尽风险。

正确做法是把本地状态转换拆成有明确边界的持久化阶段。

## 8. 幂等协议与重复操作防护

### 8.1 幂等键设计

同一业务意图的所有安全重试都应复用相同幂等键。幂等键不能只基于用户输入的业务对象 ID，因为同一对象可能合法地执行多次不同操作。

例如，一张报销单可以先创建草稿，再提交一次；以后被驳回修改后，还可能再次提交。每次提交意图都需要有明确的操作身份和版本边界。

因此，幂等键必须由**业务意图的各组成部分派生**，而不是由"每次操作新建的内部 ID"派生：

```
idempotency_key = "odap:" + tenant_id + ":" +
    hex(SHA-256(
        run_id          || "\x1f" ||
        step_id         || "\x1f" ||
        operation_type  || "\x1f" ||   // 如 expense.submit，不含 attempt 序号
        target_object_id|| "\x1f" ||   // 目标业务对象；无目标对象时为 "none"
        payload_digest                     // 规范化 payload 的 SHA-256
    ))
```

规则与边界：

1. `operation_id` 只是操作的本地主键（对应 `erp_operations.id`），**不进入幂等键**。否则进程崩溃后按新 attempt 重新创建 operation 记录时键必然变化，恢复重试就变成了二次提交。
2. `payload_digest` 参与派生：参数变化（如金额、收款对象）即构成新的业务意图，允许且应当产生新键；这与"参数变更后旧审批失效"（第六篇 §11.1）是同一原则的两侧。
3. `run_id + step_id + operation_type + target_object_id + payload_digest` 五元组在平台内唯一；重复提交同一意图（网络重试、Worker 崩溃恢复、对账后补发）键不变，由本地唯一约束与远端去重共同拦截。
4. **生成权归 Operation Gateway**（第七篇 §7）：Harness 与 Agent 只透传五元组，不得自行拼装键，避免多入口生成规则漂移。
5. 键中不出现敏感信息；完整键可写入日志与审计，但请求头中的 `Idempotency-Key` 只传截断的引用 ID（本地记录主键），不回显完整哈希。
6. 对不支持幂等键的 ERP（L3），Adapter 用同一公式在本地去重账本中登记；键公式是全平台统一的，与远端能力无关。

### 8.2 本地幂等记录

建议建立专门的幂等记录，或在现有 ERP Operation 表中加入等效的唯一约束：

```
CREATE UNIQUE INDEX uq_erp_operation_idempotency
ON runtime.erp_operations (
    tenant_id,
    connection_id,
    idempotency_key
);
```

如果现有表的列名不同，应按实际 Schema 调整。

当同一键再次到达时：

- 如果请求摘要一致，返回已有操作状态。
- 如果请求摘要不同，返回 `IDEMPOTENCY_CONFLICT`。
- 如果原操作正在执行，返回已有操作的当前状态。
- 如果原操作结果未知，进入查询与对账，而不是创建第二笔操作。

### 8.3 远端幂等能力的分级

建议将 Adapter 的幂等能力划分为三类：

| 级别 | 能力                   | 允许策略              |
| -- | -------------------- | ----------------- |
| L1 | 远端原生支持幂等键，且经过验证      | 可以按契约安全重试         |
| L2 | 远端不支持原生幂等，但可可靠查询操作结果 | 先查询确认，再决定是否重试     |
| L3 | 既不支持幂等，也无法可靠查询结果     | 结果未知时禁止自动重发，转人工对账 |

L2 的安全性依赖远端查询能力和业务标识的唯一性。仅仅能够按单据号查询对象，并不必然能够确认某次操作是否执行过。

L3 并不意味着无法连接该 ERP，而是意味着某些写操作必须采取更保守的执行策略。

### 8.4 并发写入与对象版本

除了幂等，还需要防止两个 Agent 同时修改同一业务对象。

优先使用目标 ERP 提供的：

- ETag / `If-Match`。
- 原生版本号。
- 乐观锁字段。
- 条件更新接口。
- 单据状态校验与原子提交能力。

如果远端没有可靠版本机制，Adapter 必须声明限制，Gateway 应对高风险操作采取更严格的串行化和人工确认策略。

本地锁不能完全替代远端版本校验，因为其他用户或其他系统仍然可能同时修改 ERP 对象。

## 9. Mock ERP：不只是一个假接口

### 9.1 设计目标

Mock ERP 应当是 ODAP MVP 的第一个可运行业务系统，而不是简单返回固定 JSON 的演示服务。

它至少要模拟：

- 员工、部门、费用类别和报销单等业务数据。
- 草稿、待审批、已批准、已驳回等业务状态。
- 创建、提交、查询、版本冲突等业务操作。
- 重复请求和幂等键。
- 网络延迟、请求超时、限流和服务不可用。
- ERP 已经执行操作但回执丢失的情况。
- 操作成功但客户端尚未持久化结果的情况。
- 权限不足、业务规则拒绝和字段校验失败。
- 操作查询与人工对账。

这样才能验证 Harness 在真实故障模式下是否安全。

### 9.2 Mock ERP 的内部架构

```
Mock ERP Service
├── API Layer
│   ├── Object Query API
│   ├── Command API
│   └── Operation Status API
├── Business Domain
│   ├── Employee
│   ├── Department
│   └── ExpenseClaim
├── Transaction Engine
│   ├── State Transition Rules
│   ├── Version Check
│   └── Idempotency Store
├── Fault Injection
│   ├── Latency
│   ├── Timeout
│   ├── Response Loss
│   └── Temporary Failure
└── Audit / Receipt
    ├── Operation Ledger
    ├── Receipt Generator
    └── Reconciliation API
```

建议 Mock ERP 作为独立 Go 服务运行，通过与真实 Adapter 相同的 Port 契约接入 ODAP。

关键原则：Mock ERP 的业务数据与 ODAP 的操作账本分开保存。 这样才能模拟“ERP 已提交成功，但 ODAP 尚未收到回执”的真实故障。

### 9.3 推荐技术选型

MVP 阶段可以采用：

- Go：Mock ERP 服务与业务状态机。
- PostgreSQL：保存模拟业务对象、操作状态与回执。
- React + TypeScript：展示 ERP 数据、操作历史和故障注入面板。
- Docker Compose：统一启动 ODAP、Mock ERP、PostgreSQL。
- Go 集成测试：模拟重复调用、超时、并发写入和故障恢复。

不必为 Mock ERP 单独引入复杂消息中间件。优先把数据库事务、幂等、状态转换与故障测试做正确。

### 9.4 Mock ERP 数据模型

建议先建立以下表：

```
mock_erp.employees
mock_erp.departments
mock_erp.expense_claims
mock_erp.expense_items
mock_erp.receipt_evidence
mock_erp.operations
mock_erp.idempotency_records
mock_erp.receipts
mock_erp.audit_events
mock_erp.fault_profiles
```

其中，`mock_erp.operations` 是 Mock ERP 自身对已接收操作的记录，不能直接复用 ODAP 的 `runtime.erp_operations`。

两者的职责不同：

- ODAP 账本记录平台想执行什么、授权了什么、调用到了哪一步。
- Mock ERP 账本记录模拟远端实际接收并执行了什么。

这种双账本结构，才能测试两边状态不一致时的恢复机制。

### 9.5 Mock ExpenseClaim 表结构

```
CREATE TABLE mock_erp.expense_claims (
    id              UUID PRIMARY KEY,
    tenant_id       UUID NOT NULL,
    claim_number    TEXT NOT NULL,
    employee_id     UUID NOT NULL,
    department_id   UUID NOT NULL,

    amount           NUMERIC(20, 4) NOT NULL,
    currency         CHAR(3) NOT NULL,

    status           TEXT NOT NULL,
    version          BIGINT NOT NULL DEFAULT 1,

    submitted_at     TIMESTAMPTZ,
    approved_at      TIMESTAMPTZ,
    rejected_at      TIMESTAMPTZ,

    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, claim_number),
    CHECK (amount >= 0),
    CHECK (status IN (
        'DRAFT',
        'PENDING_APPROVAL',
        'APPROVED',
        'REJECTED',
        'CANCELLED'
    ))
);
```

生产实现还应为租户隔离、对象访问控制、外键约束和状态转换规则建立相应机制。数据库 `CHECK` 只能约束单个字段的取值，不能取代完整业务状态机。

这里的状态仅用于演示报销申请生命周期。真实企业可能存在“待补充”“待财务复核”“待付款”等更多状态，需要在企业业务域本体和 ERP 映射中明确表达。

## 10. Mock ERP API 设计

建议使用 `/mock-erp/api/v1` 作为独立服务前缀。

| 方法     | 路径                                     | 作用       |
| ------ | -------------------------------------- | -------- |
| `GET`  | `/employees/{id}`                      | 查询员工     |
| `GET`  | `/departments/{id}`                    | 查询部门     |
| `GET`  | `/expense-claims/{id}`                 | 查询报销单    |
| `POST` | `/expense-claims`                      | 创建报销草稿   |
| `POST` | `/expense-claims/{id}/submit`          | 提交报销申请   |
| `POST` | `/expense-claims/{id}/approve`         | 模拟审批通过   |
| `POST` | `/expense-claims/{id}/reject`          | 模拟审批驳回   |
| `GET`  | `/operations/{id}`                     | 查询远端操作状态 |
| `GET`  | `/operations/by-idempotency-key/{key}` | 按幂等键查询操作 |
| `GET`  | `/receipts/{id}`                       | 查询操作回执   |

`approve` 和 `reject` 接口只用于模拟 ERP 自己的工作流能力。ODAP 不能因为 Mock ERP 暴露了这两个接口，就允许 Agent 自行审批。实际审批权限仍由 ODAP 的 Approval Service 和企业策略决定。

### 10.1 创建报销草稿

请求示例：

```
POST /mock-erp/api/v1/expense-claims
Content-Type: application/json
```

```
{
  "employee_id": "employee-001",
  "department_id": "department-001",
  "amount": "1280.50",
  "currency": "CNY"
}
```

返回示例：

```
{
  "id": "claim-001",
  "claim_number": "EXP-2026-0001",
  "status": "DRAFT",
  "version": 1
}
```

真实实现应使用合法的 UUID 或其他正式 ID 格式；上述值仅用于说明接口。

### 10.2 提交报销申请

请求示例：

```
POST /mock-erp/api/v1/expense-claims/claim-001/submit
Idempotency-Key: odap-operation-001
If-Match: "1"
```

成功响应示例：

```
{
  "operation_id": "remote-op-001",
  "status": "SUCCEEDED",
  "claim_id": "claim-001",
  "claim_status": "PENDING_APPROVAL",
  "result_version": 2,
  "receipt_id": "receipt-001"
}
```

其中：

- `Idempotency-Key` 防止同一业务意图被重复提交。
- `If-Match` 表达客户端预期的对象版本。
- `operation_id` 用于查询远端执行记录。
- `receipt_id` 用于后续回执核验。

若对象已经从版本 1 变成版本 2，而请求仍然携带 `If-Match: "1"`，Mock ERP 应返回版本冲突，而不是静默覆盖。

### 10.3 幂等键冲突

假设第一次请求使用同一幂等键，提交金额为 1280.50；第二次请求却使用相同幂等键提交金额为 1580.50。

Mock ERP 必须返回明确的冲突错误，例如：

```
{
  "error": {
    "code": "IDEMPOTENCY_CONFLICT",
    "message": "The idempotency key is bound to a different request."
  }
}
```

不能把第二次请求当成新的业务操作，也不能静默返回第一次请求的成功结果而不提示摘要不一致。

## 11. Mock ERP 故障注入

### 11.1 必须模拟的故障

建议提供一个仅在测试环境启用的故障注入模块。

| 故障类型                        | 模拟行为         | 验证目标    |
| --------------------------- | ------------ | ------- |
| `DELAY_RESPONSE`            | 延迟返回         | 超时与期限处理 |
| `FAIL_BEFORE_COMMIT`        | 提交前失败        | 安全重试    |
| `COMMIT_THEN_DROP_RESPONSE` | ERP 已提交，丢弃响应 | 未知结果对账  |
| `DUPLICATE_REQUEST`         | 重复接收同一请求     | 幂等      |
| `VERSION_CONFLICT`          | 对象版本不匹配      | 乐观并发控制  |
| `RATE_LIMIT`                | 返回限流错误       | 退避重试    |
| `REMOTE_UNAVAILABLE`        | 服务暂时不可用      | 故障恢复    |
| `RECEIPT_DELAY`             | 延迟生成或返回回执    | 回执查询与恢复 |
| `RECEIPT_MISMATCH`          | 返回不匹配回执      | 防止伪成功   |
| `UNKNOWN_RESULT`            | 暂时无法查询最终状态   | 对账与人工接管 |

### 11.2 最重要的故障：提交成功但响应丢失

这一场景必须单独测试：

1. Harness 创建操作账本。
2. Gateway 发送提交请求。
3. Mock ERP 将报销单状态更新为 `PENDING_APPROVAL`。
4. Mock ERP 保存自己的操作记录。
5. Mock ERP 丢弃 HTTP 响应。
6. Harness 收到超时。
7. ODAP 将操作标记为 `UNKNOWN`。
8. Reconciliation Worker 查询 Mock ERP。
9. Mock ERP 返回原操作及回执。
10. ODAP 保存回执并将操作标记为 `SUCCEEDED`。
11. Harness 恢复任务，不再重复提交。

预期结果：ERP 中只发生一次提交，ODAP 最终恢复到正确状态。

如果该测试无法稳定通过，就不应接入可能产生付款、过账或其他重大业务副作用的真实 ERP 写接口。

### 11.3 故障注入安全边界

故障注入功能只能在测试环境中启用，应由平台管理员控制，并在每次使用时记录审计事件。

生产环境不得通过普通用户请求参数启用诸如“提交后丢弃回执”或“伪造成功回执”等测试行为。

## 12. 真实 ERP Adapter 接入规范

### 12.1 Adapter 接入流程

每个真实 ERP Adapter 必须通过统一的接入流程：

1. 登记 Adapter 的身份、版本、摘要与来源。
2. 审核配置 Schema 和依赖。
3. 配置测试环境连接。
4. 验证身份认证与最小权限。
5. 获取并核验能力清单。
6. 建立原生对象与 Canonical Model 的映射。
7. 执行只读契约测试。
8. 执行写操作的幂等、版本冲突和回执测试。
9. 执行故障注入和对账测试。
10. 生成兼容性与安全测试报告。
11. 由管理员批准启用。
12. 先以只读模式接入，再按业务域逐步开放写权限。

Adapter 发布通过不代表它可以立即操作企业生产 ERP。生产连接、生产权限、操作范围和风险策略应独立审批。

### 12.2 连接真实 ERP 的三种方式

[API Integration Services for CRM, ERP and B2B Systems](https://images.openai.com/static-rsc-4/STeQWaDUdCwOKp6MT6hyRmi_w0QXWgPZ3BQ7BacRUYHJoeQCy1qxZBlPqTtCbDyzBLZ03ZEQy4O-BWW3bNKUqctn5E-aVoDQEiOGtXRiUGbfU4ZGc63eYcRyp8hYqpmm8daobaALvZwy8ZSOhGU5_kxaGZD26ok4LjxO-BVc-sQ?purpose=inline)

模式 A：官方 API

优先选择 ERP 官方 REST、SOAP 或其他正式集成接口。便于权限控制、版本管理与审计。

[Design Pattern for CloudNative Application — Series 1 | by Bindu C | Medium](https://images.openai.com/static-rsc-4/k-JFhgpfebGhq_N6JuLHgKSLl-jl1tjsRq0nUH1thiEcOOSBd_QE9zqRur9zzqpRIG7Y_DCwz5gMMw8E0uVBFWJVOq3mbQSoGC7xmJC-iPs_0x9g_lp6sPJjC3BezE0lBXSTFBmMjOoNlAydr9flA8tH1w_sf8c3sOowaMQsrGQ?purpose=inline)

模式 B：企业集成中间件

当 ERP 已经由 ESB、API Gateway 或企业集成平台统一管理时，Adapter 可以对接中间件，而不必直接连接 ERP。

[On-prem connectivity | Workato Docs](https://images.openai.com/static-rsc-4/ruTbYQPyy963Tl0UauN6Hx2-g-8g2UZ2rAxf5AZjq5vaZxkECDHzLAeP2Cq9ldR2jf6L5Rb51G_4FW99Ff0iT3jTmEn2v2Xo3MfUomxRC57SQ-CdFn3-uNz1fNIAxL7mvD5Msk27O9VNkydX89JQRUOC_MifXJH3Q0bGSs9xp8A?purpose=inline)

模式 C：企业内网连接器

适用于私有化部署或 ERP 位于隔离内网的场景。可部署受控 Connector，主动连接 ODAP 的授权消息通道，避免直接暴露 ERP。

### 12.3 为什么不建议直接读写 ERP 数据库

直接访问 ERP 数据库看似简单，但不应作为通用的业务写入方式：

- 可能绕过 ERP 的业务校验和工作流。
- 可能破坏隐藏的关联约束。
- 可能绕过 ERP 自身的审计记录。
- 可能因版本升级而失效。
- 可能绕过库存、财务或采购模块的事务处理逻辑。

如果确实需要数据库只读抽取，应单独定义只读连接、数据范围、快照时间和新鲜度策略。写操作优先通过 ERP 支持的业务接口完成。

### 12.4 私有化连接器设计

对于不能从外部访问的 ERP，可以部署企业内网 Connector：

```
ODAP Control Plane
       │
       │ 授权的请求 / 响应通道
       ▼
Enterprise Connector
       │
       ├── 身份与配置校验
       ├── 请求签名与重放防护
       ├── 本地任务队列
       ├── ERP Adapter
       └── 审计与回执
       │
       ▼
Internal ERP Network
```

Connector 应采用双向认证、短期凭据、出站访问白名单和消息签名。它只能接收已授权的业务命令，不能成为任意内网访问代理。

## 13. 安全模型与威胁防护

ERP 接入会让 ODAP 从信息处理系统进入实际业务操作领域，因此需要比普通工具插件更严格的安全约束。

### 13.1 必须防范的风险

| 风险      | 典型场景               | 防护措施                     |
| ------- | ------------------ | ------------------------ |
| 权限绕过    | Agent 直接调用 Adapter | 网络隔离、Gateway 强制授权        |
| 租户越权    | 查询到其他企业数据          | 服务端租户绑定、RLS、对象级授权        |
| 凭据泄露    | 日志或插件输出包含 Token    | Secret Manager、脱敏、短期凭据   |
| SSRF    | 连接配置指向任意内网地址       | 出站白名单、DNS/IP 校验、禁止任意 URL |
| 重放攻击    | 重复发送历史写请求          | 幂等键、请求签名、有效期与操作账本        |
| 参数篡改    | 审批后修改金额或目标对象       | 不可变 payload digest、审批绑定  |
| 回执伪造    | 插件自行报告成功           | 可信 Adapter、签名或可核验远端回执    |
| 版本供应链风险 | 恶意 Adapter 更新      | 签名、摘要锁定、审核与回滚            |
| 业务语义错误  | 金额、币种、状态映射错误       | 映射验证、契约测试、人工审核           |
| 重复副作用   | 超时后重复提交付款          | 对账优先、稳定幂等键               |
| 数据泄露    | 工具返回超范围字段          | 字段级权限、最小化返回、输出过滤         |

### 13.2 连接凭据

凭据应保存在专门的秘密管理系统中。数据库只保存凭据引用及必要的元数据，不保存明文密码、API Token 或私钥。

凭据轮换后，Adapter 应能够在不中断不相关任务的情况下刷新连接。旧凭据失效、权限变化和连接停用都应形成审计事件。

### 13.3 网络安全

ERP Adapter 的网络目标必须由管理员配置。不能让模型通过提交 URL、Host Header 或重定向地址来控制 Adapter 访问任意内网服务。

至少需要：

- 允许访问的目标域名或 IP 段。
- 对 DNS 解析结果进行安全校验。
- 阻止访问云元数据地址和不允许的内网目标。
- 限制 HTTP 重定向目标。
- 限制请求体、响应体和连接数。
- 区分只读与写入凭据。
- 对所有网络调用设置超时。

对于企业内网 ERP，还需要结合企业防火墙和网络分区策略进行部署设计。

### 13.4 回执验证

不能只根据 HTTP 200 判断业务成功。

Adapter 应根据具体 ERP 契约验证：

- 响应对应的操作 ID 是否正确。
- 目标对象是否正确。
- 操作摘要或关键业务字段是否匹配。
- 远端状态是否达到预期。
- 远端版本或事件序号是否合理。
- 回执来源是否可信。
- 是否存在部分成功或异步处理状态。

如果无法完成核验，应返回 `UNKNOWN` 或 `MANUAL_REVIEW`，而不是伪造成功。

## 14. ODAP 管理前端设计

建议在 React + TypeScript 前端中增加“ERP 集成中心”，包含以下六个工作区。

[Data Connection - UI Cards by Filip Justić for Balkan Brothers on Dribbble](https://images.openai.com/static-rsc-4/fT1xssI4uEXbOhmCQT0hX7qEO_WqSny0cWC7ITluOb1WnAElRFJC-PEAzBWnlYd6RNfjyNom3EnCEOErMjKtfkTr4aLSWcrIPtY0LvaiwQTdCdnHF4Na9fILm2PcaoYmZ2K9dvJr5YMao9qWKjSo2IrZSHaQUFk0zLAriWYcSK4?purpose=inline)

14.1 ERP 连接管理

展示连接状态、Adapter 版本、健康检查、已启用业务域、最近错误和凭据有效性。

[Data sources and targets | CloverDX 7.5.1 Documentation](https://images.openai.com/static-rsc-4/u4LZDp0AKJEUWUz-Jthqw5pvo8QhXN3gjiPwoeY7CR1aDl2EAZjxx4US31YKLq3LtqEnAhUgBR7l50hJk2-uAk0G5kjE6DiKiKoIw-0hmikjjFcKuhkJ5RTVxG8nvd0sceR-0vOXtmlxq-AxoiEw2HqNXOUH-N6RtHDho811iAw?purpose=inline)

14.2 对象与字段映射

展示 ERP 原生字段、Canonical Model 字段、转换规则、空值语义、映射覆盖率与版本差异。

[Talisman Platform](https://images.openai.com/static-rsc-4/0I03fIQmrRkUhaqg0cKdKHzbhaoZ1g-1xjtcxOL6f4LDbx3_CjbUgWnB2NtjSAJVMpTeZ4xZEcvtilY-bEV-Si_pFYBf-CHROXno-vjhlA-s36D1Gs2t-dRU9akMKzBQcPOAuJ78pZ-BJYfO8UZ5NMkRAG6-ziQDielik0T-XaA?purpose=inline)

14.3 能力与契约测试

展示已支持的对象与操作、幂等能力、版本控制能力、测试结果及未通过的契约。

[Operational Dashboard — Recon360](https://images.openai.com/static-rsc-4/RRhDCFJ6pc9k41-9r0K_5W12oJSObMmkSl8v2sUZxXVzd-_vOODm5PdJ5qXVpLDSKCnX83MUeQvDPlHSSAyRMIGn3WE9fMrSGfYGR1eT_Y-QbOfBD4Qz5c-vgc3c2Hkvz8BbyKBChi_Sek1Pm45O52A9Hjr9Mpt-TBV-zmyH8Oo?purpose=inline)

14.4 操作账本与对账

查询外部操作、远端引用、回执、未知结果、重试历史与人工处置记录。

[Mock API for Testing - Use Cases, Features, Examples](https://images.openai.com/static-rsc-4/UlBmMhxPFBYbiUooZebiDUZhl_7T1qoUqm8wV4MvlVTaFolwb1Rq_v7-immlzoLS8UzIWNtqIAYtf7JdNgn__VbhSzRFoVZPBpUO-2KSD0YZ0jYPhydH8M3ti-XXRyAIqdZPPPb-LN3-XJ2H5bkI9YkQWLKauqUtPkqVdoKrBB4?purpose=inline)

14.5 Mock ERP 实验室

生成测试数据、触发延迟和回执丢失、模拟版本冲突、重复提交与远端故障。

[Access Governance | Oracle](https://images.openai.com/static-rsc-4/IoLBMLNa0ZzcQ8Nyn--juaWPaWMohjbSFaSmfdrPbgZXWThJuBoVVlM_MfKnMzzCcgdUiBmBiTvdE6tNZ9-GKr9f9F6eh12nhfWzNvuiOMXNdt6XwDvSS9qu6Tn64jC_zE1IAPTUW6arng8h9ASRxpqi5yqX0CFaFGTHqKgWKdg?purpose=inline)

14.6 写入策略与安全治理

管理业务操作权限、审批要求、环境隔离、连接授权和生产启用流程。

### 14.7 建议增加的 API

| 方法     | 路径                                          | 作用     |
| ------ | ------------------------------------------- | ------ |
| `GET`  | `/api/v1/erp/connections`                   | 查询连接   |
| `POST` | `/api/v1/erp/connections`                   | 创建连接配置 |
| `POST` | `/api/v1/erp/connections/{id}/validate`     | 验证连接   |
| `GET`  | `/api/v1/erp/connections/{id}/capabilities` | 查询能力   |
| `GET`  | `/api/v1/erp/mappings`                      | 查询映射版本 |
| `POST` | `/api/v1/erp/mappings/{id}/validate`        | 验证映射   |
| `GET`  | `/api/v1/erp/operations`                    | 查询操作账本 |
| `GET`  | `/api/v1/erp/operations/{id}`               | 查询单个操作 |
| `POST` | `/api/v1/erp/operations/{id}/reconcile`     | 请求对账   |
| `GET`  | `/api/v1/erp/receipts/{id}`                 | 查询回执   |
| `POST` | `/api/v1/mock-erp/fault-profiles`           | 配置测试故障 |
| `POST` | `/api/v1/mock-erp/reset`                    | 重置测试环境 |

最后两个接口必须仅在受控测试环境启用，且需要单独的管理员权限与审计。

## 15. Go 工程接口与 Adapter 实现示例

下面给出一个可以作为实际工程起点的接口分层。它强调业务命令、适配器和操作账本之间的依赖关系。

### 15.1 Adapter 接口

```
package erpport

import "context"

type Adapter interface {
    ID() string
    Version() string

    Capabilities(
        ctx context.Context,
        connectionID string,
    ) (Capabilities, error)

    GetObject(
        ctx context.Context,
        req GetObjectRequest,
    ) (ObjectResult, error)

    QueryObjects(
        ctx context.Context,
        req QueryObjectsRequest,
    ) (QueryResult, error)

    Execute(
        ctx context.Context,
        req ExecuteRequest,
    ) (OperationResult, error)

    GetOperation(
        ctx context.Context,
        req GetOperationRequest,
    ) (OperationResult, error)

    Health(
        ctx context.Context,
        connectionID string,
    ) (HealthResult, error)
}
```

Registry 负责发现与选择 Adapter，连接管理模块负责提供经过校验的连接配置，Gateway 负责在满足权限与业务条件后调用 `Execute`。

Adapter 不应该自行决定是否批准一笔业务操作。

### 15.2 Operation Gateway 接口

```
package erpgateway

import (
    "context"

    "odap/internal/erpport"
)

type OperationGateway interface {
    Execute(
        ctx context.Context,
        req erpport.ExecuteRequest,
    ) (erpport.OperationResult, error)

    GetOperation(
        ctx context.Context,
        tenantID string,
        operationID string,
    ) (erpport.OperationResult, error)

    Reconcile(
        ctx context.Context,
        tenantID string,
        operationID string,
    ) (erpport.OperationResult, error)
}
```

Gateway 内部应执行以下逻辑：

1. 校验租户和业务操作。
2. 验证请求 Schema。
3. 计算规范化请求摘要。
4. 检查已有幂等记录。
5. 检查当前权限与审批状态。
6. 检查对象版本和映射版本。
7. 持久化操作意图。
8. 调用 Adapter。
9. 保存调用结果或未知状态。
10. 在可验证的条件下完成对账。
11. 通过事务写入审计事件与 Outbox。

### 15.3 为什么不能在 Gateway 中无限重试

Gateway 应负责决定当前操作是否可以重试，但具体的延迟调度、跨进程恢复与长期对账应由 Harness 和 Recovery Worker 负责。

例如，ERP 返回 `429 Too Many Requests`，Gateway 可以记录限流信息并安排延迟重试；如果请求已经发出但连接超时，Gateway 应保存 `UNKNOWN`，而不是在当前 HTTP 请求中持续重发。

这种设计可以避免请求线程长期占用，也便于任务在服务重启后恢复。

## 16. ERP 操作错误模型

建议为 Adapter 统一错误码，避免将所有错误压缩成字符串。

| 错误码                          | 语义          | 建议处理             |
| ---------------------------- | ----------- | ---------------- |
| `ERP_AUTHENTICATION_FAILED`  | ERP 身份认证失败  | 检查凭据，不盲目重试       |
| `ERP_PERMISSION_DENIED`      | ERP 拒绝当前主体  | 停止操作并审计          |
| `ERP_VALIDATION_FAILED`      | 业务字段或状态校验失败 | 返回业务错误           |
| `ERP_OBJECT_NOT_FOUND`       | 目标对象不存在     | 核验对象 ID 与访问范围    |
| `ERP_VERSION_CONFLICT`       | 对象版本已变化     | 重新读取并重新评估        |
| `ERP_RATE_LIMITED`           | 远端限流        | 按约定退避            |
| `ERP_UNAVAILABLE`            | 远端暂不可用      | 只对可安全重试的操作重试     |
| `ERP_TIMEOUT_UNKNOWN`        | 超时且结果不明     | 对账优先             |
| `ERP_IDEMPOTENCY_CONFLICT`   | 相同幂等键对应不同请求 | 拒绝并告警            |
| `ERP_RECEIPT_INVALID`        | 回执缺失或无法核验   | 保留未知状态并调查        |
| `ERP_CAPABILITY_UNSUPPORTED` | 当前连接不支持该操作  | 拒绝调用或采用经过审核的替代方案 |
| `ERP_MAPPING_FAILED`         | 字段或枚举无法转换   | 阻止写入并记录诊断        |

统一错误模型可以让 Harness 采取确定性策略，而不是让 LLM 根据不完整的错误文本自由猜测是否应重试。

## 17. 端到端场景：从报销申请到 ERP 提交

为了验证前六篇与本篇的设计能够协同工作，建议将“报销申请”作为第一个完整业务域。

### 17.1 业务流程

\#chatgpt-mermaid-\_r_353\_{font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";font-size:16px;fill:rgb(237, 237, 237);}@keyframes edge-animation-frame{from{stroke-dashoffset:0;}}@keyframes dash{to{stroke-dashoffset:0;}}#chatgpt-mermaid-\_r_353\_ .edge-animation-slow{stroke-dasharray:9,5!important;stroke-dashoffset:900;animation:dash 50s linear infinite;stroke-linecap:round;}#chatgpt-mermaid-\_r_353\_ .edge-animation-fast{stroke-dasharray:9,5!important;stroke-dashoffset:900;animation:dash 20s linear infinite;stroke-linecap:round;}#chatgpt-mermaid-\_r_353\_ .error-icon{fill:rgb(48, 48, 48);}#chatgpt-mermaid-\_r_353\_ .error-text{fill:rgb(237, 237, 237);stroke:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_353\_ .edge-thickness-normal{stroke-width:1px;}#chatgpt-mermaid-\_r_353\_ .edge-thickness-thick{stroke-width:3.5px;}#chatgpt-mermaid-\_r_353\_ .edge-pattern-solid{stroke-dasharray:0;}#chatgpt-mermaid-\_r_353\_ .edge-thickness-invisible{stroke-width:0;fill:none;}#chatgpt-mermaid-\_r_353\_ .edge-pattern-dashed{stroke-dasharray:3;}#chatgpt-mermaid-\_r_353\_ .edge-pattern-dotted{stroke-dasharray:2;}#chatgpt-mermaid-\_r_353\_ .marker{fill:rgb(175, 175, 175);stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_353\_ .marker.cross{stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_353\_ svg{font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";font-size:16px;}#chatgpt-mermaid-\_r_353\_ p{margin:0;}#chatgpt-mermaid-\_r_353\_ .label{font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";color:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_353\_ .cluster-label text{fill:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_353\_ .cluster-label span{color:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_353\_ .cluster-label span p{background-color:transparent;}#chatgpt-mermaid-\_r_353\_ .label text,#chatgpt-mermaid-\_r_353\_ span{fill:rgb(237, 237, 237);color:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_353\_ .node rect,#chatgpt-mermaid-\_r_353\_ .node circle,#chatgpt-mermaid-\_r_353\_ .node ellipse,#chatgpt-mermaid-\_r_353\_ .node polygon,#chatgpt-mermaid-\_r_353\_ .node path{fill:rgb(9, 23, 44);stroke:rgb(31, 78, 148);stroke-width:1px;}#chatgpt-mermaid-\_r_353\_ .rough-node .label text,#chatgpt-mermaid-\_r_353\_ .node .label text,#chatgpt-mermaid-\_r_353\_ .image-shape .label,#chatgpt-mermaid-\_r_353\_ .icon-shape .label{text-anchor:middle;}#chatgpt-mermaid-\_r_353\_ .node .katex path{fill:#000;stroke:#000;stroke-width:1px;}#chatgpt-mermaid-\_r_353\_ .rough-node .label,#chatgpt-mermaid-\_r_353\_ .node .label,#chatgpt-mermaid-\_r_353\_ .image-shape .label,#chatgpt-mermaid-\_r_353\_ .icon-shape .label{text-align:center;}#chatgpt-mermaid-\_r_353\_ .node.clickable{cursor:pointer;}#chatgpt-mermaid-\_r_353\_ .root .anchor path{fill:rgb(175, 175, 175)!important;stroke-width:0;stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_353\_ .arrowheadPath{fill:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_353\_ .edgePath .path{stroke:rgb(175, 175, 175);stroke-width:1px;}#chatgpt-mermaid-\_r_353\_ .flowchart-link{stroke:rgb(175, 175, 175);fill:none;}#chatgpt-mermaid-\_r_353\_ .edgeLabel{background-color:rgb(0, 0, 0);text-align:center;}#chatgpt-mermaid-\_r_353\_ .edgeLabel p{background-color:rgb(0, 0, 0);}#chatgpt-mermaid-\_r_353\_ .edgeLabel rect{opacity:0.5;background-color:rgb(0, 0, 0);fill:rgb(0, 0, 0);}#chatgpt-mermaid-\_r_353\_ .labelBkg{background-color:rgba(0, 0, 0, 0.5);}#chatgpt-mermaid-\_r_353\_ .cluster rect{fill:rgb(48, 48, 48);stroke:rgba(255, 255, 255, 0.15);stroke-width:1px;}#chatgpt-mermaid-\_r_353\_ .cluster text{fill:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_353\_ .cluster span{color:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_353\_ div.mermaidTooltip{position:absolute;text-align:center;max-width:200px;padding:2px;font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";font-size:12px;background:rgb(48, 48, 48);border:1px solid rgba(255, 255, 255, 0.15);border-radius:2px;pointer-events:none;z-index:100;}#chatgpt-mermaid-\_r_353\_ .flowchartTitleText{text-anchor:middle;font-size:18px;fill:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_353\_ rect.text{fill:none;stroke-width:0;}#chatgpt-mermaid-\_r_353\_ .icon-shape,#chatgpt-mermaid-\_r_353\_ .image-shape{background-color:rgb(0, 0, 0);text-align:center;}#chatgpt-mermaid-\_r_353\_ .icon-shape p,#chatgpt-mermaid-\_r_353\_ .image-shape p{background-color:rgb(0, 0, 0);padding:2px;}#chatgpt-mermaid-\_r_353\_ .icon-shape .label rect,#chatgpt-mermaid-\_r_353\_ .image-shape .label rect{opacity:0.5;background-color:rgb(0, 0, 0);fill:rgb(0, 0, 0);}#chatgpt-mermaid-\_r_353\_ .label-icon{display:inline-block;height:1em;overflow:visible;vertical-align:-0.125em;}#chatgpt-mermaid-\_r_353\_ .node .label-icon path{fill:currentColor;stroke:revert;stroke-width:revert;}#chatgpt-mermaid-\_r_353\_ .node .neo-node{stroke:rgb(31, 78, 148);}#chatgpt-mermaid-\_r_353\_ [data-look="neo"].node rect,#chatgpt-mermaid-\_r_353\_ [data-look="neo"].cluster rect,#chatgpt-mermaid-\_r_353\_ [data-look="neo"].node polygon{stroke:url(#chatgpt-mermaid-\_r_353\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_353\_ [data-look="neo"].swimlane.cluster rect{filter:none;}#chatgpt-mermaid-\_r_353\_ [data-look="neo"].node path{stroke:url(#chatgpt-mermaid-\_r_353\_-gradient);stroke-width:1px;}#chatgpt-mermaid-\_r_353\_ [data-look="neo"].node .outer-path{filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_353\_ [data-look="neo"].node .neo-line path{stroke:rgb(31, 78, 148);filter:none;}#chatgpt-mermaid-\_r_353\_ [data-look="neo"].node circle{stroke:url(#chatgpt-mermaid-\_r_353\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_353\_ [data-look="neo"].node circle .state-start{fill:#000000;}#chatgpt-mermaid-\_r_353\_ [data-look="neo"].icon-shape .icon{fill:url(#chatgpt-mermaid-\_r_353\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_353\_ [data-look="neo"].icon-shape .icon-neo path{stroke:url(#chatgpt-mermaid-\_r_353\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_353\_ .node text{font-size:14px;font-weight:600;letter-spacing:normal;fill:rgb(153, 206, 255);}#chatgpt-mermaid-\_r_353\_ .edgeLabels text{font-size:13px;font-weight:600;letter-spacing:-0.08px;fill:rgb(153, 206, 255);}#chatgpt-mermaid-\_r_353\_ .node tspan[font-weight="normal"],#chatgpt-mermaid-\_r_353\_ .edgeLabels tspan[font-weight="normal"]{font-weight:600;}#chatgpt-mermaid-\_r_353\_ .edgeLabel .label rect{opacity:1;rx:13px;ry:13px;fill:rgb(0, 14, 26);stroke:rgb(26, 62, 95);stroke-width:1px;}#chatgpt-mermaid-\_r_353\_ .node rect,#chatgpt-mermaid-\_r_353\_ .node circle,#chatgpt-mermaid-\_r_353\_ .node ellipse,#chatgpt-mermaid-\_r_353\_ .node polygon,#chatgpt-mermaid-\_r_353\_ .node path{fill:rgb(0, 40, 77);stroke:rgba(255, 255, 255, 0.1);stroke-width:1px;}#chatgpt-mermaid-\_r_353\_ .node rect{rx:16px;ry:16px;}#chatgpt-mermaid-\_r_353\_ .node.mermaid-decision .label-container{fill:rgb(0, 14, 26);stroke:rgb(26, 62, 95);stroke-dasharray:2,2;}#chatgpt-mermaid-\_r_353\_ .edgePaths .flowchart-link{stroke:rgb(175, 175, 175);stroke-width:1px;stroke-linecap:round;stroke-linejoin:round;}#chatgpt-mermaid-\_r_353\_ .marker{fill:rgb(175, 175, 175);stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_353\_ :root{--mermaid-font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";}用户发起报销任务Harness 创建 Run固定 Agent / Ontology /Policy / Mapping 版本查询 ERP 报销单Ontology Runtime 验证验证结果返回诊断并停止提交请求补充信息或人工审核Authorization Engine是否需要审批创建审批请求并暂停审批完成后重新检查创建操作账本审批与权限有效拒绝执行ERP Operation GatewayMock ERP Adapter回执可确认保存回执与事件对账 Worker远端状态人工处理Harness 完成任务FAILUNKNOWNPASS需要不需要否是成功未知已成功未执行且可安全重试无法确定

### 17.2 场景中的权限边界

假设员工提交一张金额为 1280.50 元的报销申请：

- Agent 可以查询并整理报销材料。
- Ontology Runtime 可以检查必需字段、凭证关联和制度约束。
- Authorization Engine 判断当前主体是否能提交该报销单。
- Approval Service 判断是否需要人工审批。
- Harness 只有在前置条件满足后，才会调用 ERP Gateway。
- ERP Adapter 将统一命令转换成目标 ERP 的原生请求。
- ERP 回执经过核验后，才将操作标记为成功。

金额阈值、审批层级和凭证要求应由企业制度配置，而不是硬编码在 Agent Prompt 中。

### 17.3 验证失败的处理

假设报销单缺少必需的凭证：

- Ontology Runtime 返回 `FAIL` 或对应的验证发现。
- Harness 不执行提交命令。
- Run 进入等待补充信息或业务拒绝路径。
- 前端显示具体缺失字段与对应制度证据。
- 用户补充凭证后，系统重新验证。
- 若对象版本已变化，还需要重新读取并核对相关字段。

不允许 Agent 因为“用户希望提交”就把验证失败转换成通过。

### 17.4 回执丢失的处理

假设 ERP 已成功将报销单提交到待审批状态，但网络中断导致 ODAP 超时：

- ODAP 保留原操作 ID 和幂等键。
- 操作账本记录 `UNKNOWN`。
- Harness 暂停依赖该结果的步骤。
- 对账 Worker 使用远端操作查询或稳定业务标识查明结果。
- 确认已提交后保存回执。
- Harness 继续执行后续查询或结束任务。

该流程必须通过自动化故障注入测试，不能只靠正常情况下的接口联调证明安全。

## 18. 自动化测试与验收体系

### 18.1 测试分层

建议将 ERP 集成测试分为五层：

| 测试层           | 测试内容                | 主要目的               |
| ------------- | ------------------- | ------------------ |
| 单元测试          | 字段转换、状态映射、错误码转换     | 保证局部逻辑正确           |
| Port 契约测试     | 所有 Adapter 实现统一接口规范 | 确保替换 Adapter 不破坏上层 |
| Mock ERP 集成测试 | 完整业务流程和数据持久化        | 验证端到端正确性           |
| 故障注入测试        | 超时、重复、崩溃、回执丢失       | 验证恢复和幂等            |
| 真实 ERP 沙箱测试   | 真实认证、字段映射、远端业务状态    | 验证实际系统兼容性          |

每一个新 Adapter 都必须通过公共契约测试。Adapter 专有能力可以有额外测试，但不能削弱通用安全要求。

### 18.2 必须通过的核心测试

建议至少包含以下测试用例：

### 验收清单

0/12 已核对

T01 · 正常提交

报销单提交成功，回执可核验，任务正确完成。

T02 · 重复提交

同一幂等键重复请求，远端只执行一次。

T03 · 相同键不同参数

请求摘要冲突，第二次请求被拒绝。

T04 · 版本冲突

对象已被修改，系统拒绝覆盖旧版本。

T05 · 提交后丢失响应

对账确认远端成功，不重复提交。

T06 · 提交前故障

确认操作未执行后，按策略安全重试。

T07 · 审批后权限撤销

恢复执行前重新授权，拒绝越权写入。

T08 · 审批摘要变化

参数发生变化时，原审批不再有效。

T09 · 两个 Worker 并发

同一 Run 不会被两个有效 Worker 同时推进。

T10 · 回执不匹配

不接受错误操作 ID 或错误目标对象的回执。

T11 · Adapter 被停用

阻止新调用，已有操作保留对账路径。

T12 · 映射升级

运行中的任务不被静默切换到新映射版本。

这个清单可用于研发和测试评审。实际验收时，还应保存测试输入、执行日志、操作账本和断言结果，不能只以人工勾选作为通过证据。

### 18.3 建议增加的性能测试

除了正确性，还需要测量：

- 只读查询的 P50、P95、P99 延迟。
- 写操作从创建账本到获得回执的耗时。
- 大量并发 Run 下的任务领取吞吐量。
- Worker 故障后的恢复时间。
- 未知外部操作的对账成功率与耗时。
- 大量审批等待任务对数据库的影响。
- Adapter 限流时的队列积压与资源消耗。
- 大对象和大量字段映射下的内存占用。

MVP 阶段先定义测试方法与基准环境，不宜在尚未确定机器规格和实际 ERP 性能之前承诺具体吞吐量。

## 19. 实施路线与开发优先级

结合 ODAP 当前采用的 Go + React/TypeScript、PostgreSQL、插件化架构与 Mock ERP 起步路线，建议按以下顺序推进。

### P1：统一 Port 与规范对象模型

先定义 Canonical Model、业务命令、错误码、版本字段和回执契约。

交付物：Go 接口、JSON Schema、对象模型与 Port 契约测试。

### P2：Operation Gateway 与操作账本

建立写入门禁、稳定幂等键、授权重检、审批绑定与外部操作记录。

交付物：操作账本迁移、Gateway、授权集成和幂等测试。

### P3：Mock ERP

实现员工、部门、报销单、业务状态机、版本控制与远端操作账本。

交付物：独立 Mock ERP 服务、种子数据、API 与容器部署配置。

### P4：回执与对账

实现 Recovery Worker、操作查询、未知结果处理、回执核验和人工接管。

交付物：提交后丢失响应等关键故障的自动化测试。

### P5：Harness 与本体联调

接入前六篇中的执行引擎、权限引擎、本体验证、审批和证据模块。

交付物：端到端报销 Agent 工作流。

### P6：真实 ERP Adapter

根据实际目标 ERP 的 API、授权、字段与业务流程，开发第一个真实 Adapter。

交付物：沙箱兼容性报告、生产连接安全评审与分阶段启用方案。

## 20. 推荐代码仓库结构

为了避免 Mock ERP 和正式 ERP Adapter 混在一起，建议将 ERP 集成部分组织成独立模块：

```
odap/
├── cmd/
│   ├── odap-api/
│   ├── odap-worker/
│   └── mock-erp/
├── internal/
│   ├── erpport/
│   │   ├── interfaces.go
│   │   ├── canonical_model.go
│   │   ├── commands.go
│   │   ├── capabilities.go
│   │   └── errors.go
│   ├── erpgateway/
│   │   ├── gateway.go
│   │   ├── operation_ledger.go
│   │   ├── idempotency.go
│   │   └── receipt_verifier.go
│   ├── erpregistry/
│   │   ├── registry.go
│   │   ├── connection.go
│   │   └── capabilities.go
│   ├── erpmapping/
│   │   ├── mapper.go
│   │   ├── transforms.go
│   │   └── validator.go
│   ├── adapters/
│   │   ├── mockerp/
│   │   └── example/
│   ├── mockerp/
│   │   ├── domain/
│   │   ├── application/
│   │   ├── repository/
│   │   ├── api/
│   │   └── faultinjection/
│   ├── harness/
│   ├── authorization/
│   ├── ontology/
│   └── audit/
├── schemas/
│   ├── erp-port/
│   ├── adapter-manifest/
│   └── canonical-model/
├── migrations/
│   ├── odap/
│   └── mock-erp/
├── tests/
│   ├── contract/
│   ├── integration/
│   ├── failure-injection/
│   └── e2e/
└── deployments/
    └── docker-compose.yml
```

`adapters/mockerp` 是 ODAP 侧的 Mock ERP Adapter；`cmd/mock-erp` 和 `internal/mockerp` 是模拟远端 ERP 的服务。两者必须分离，避免测试时直接读取 Mock ERP 的内部数据库而绕过真正的 Port。

## 21. 本篇架构决策汇总

| 决策领域      | 推荐方案                          |
| --------- | ----------------------------- |
| 集成模式      | ERP Port + Adapter            |
| 业务模型      | Canonical Model 与 Ontology 分层 |
| 操作方式      | 语义化业务命令优先，避免通用任意 CRUD         |
| 连接管理      | 独立连接 Registry 与能力声明           |
| 字段映射      | 版本化、可验证、发布后不可变                |
| 写入控制      | 统一 Operation Gateway          |
| 幂等        | 稳定幂等键 + 请求摘要 + 远端能力分级         |
| 并发        | 远端版本检查优先，本地锁作为补充              |
| 回执        | 可信来源、操作绑定、状态与对象核验             |
| 超时        | 未知结果先对账，禁止盲目重发                |
| Mock ERP  | 独立服务、独立数据库、独立远端操作账本           |
| 故障测试      | 提交后丢失响应、重复请求、版本冲突等            |
| 真实 ERP 接入 | 先只读，再分阶段开放写入                  |
| 私有化部署     | 内网 Connector、最小网络权限与秘密管理      |
| 前端        | ERP 连接、映射、能力测试、操作账本、故障实验室     |

## 22. 第七篇完成后的 ODAP 架构

完成前七篇后，ODAP 已经具备从企业知识建模到业务系统操作的完整架构设计：

1. 总体架构：定义平台边界、模块划分与技术路线。
2. 核心数据模型：管理任务、审批、检查点、审计与证据。
3. 身份与权限引擎：约束主体、资源、操作和数据范围。
4. 插件协议与 Registry：管理 Agent、Tool、Ontology、Policy 等扩展。
5. Ontology Runtime 与 Factory：将企业制度转化为可验证的业务语义资产。
6. Harness 执行引擎：实现计划执行、状态机、恢复与人工接管。
7. ERP Port、Adapter 与 Mock ERP：将受控执行转化为真实业务系统中的操作。

真正的工程闭环是：

企业制度 → Ontology → Agent 计划 → Harness 校验 → 授权与审批 → ERP 操作 → 回执核验 → 审计与证据。

其中最值得坚持的设计原则是：本体负责表达业务语义，Agent 负责提出候选行动，Harness 负责控制执行，授权与审批负责约束权力，ERP 负责权威业务事务，审计与证据负责让整个过程可追溯。

