# ODAP 第九篇：模型供应商抽象层、LLM Gateway 与推理治理详细设计

版本：V1.0 · 架构基线：Go + React/TypeScript + PostgreSQL · 部署模式：企业私有化部署

> 范围标签：[MVP-V1.0] §2/§3/§6/§7/§14 · [V1.5] §12 语义缓存 · [V2] 多供应商市场化路由

## 1. 设计目标

ODAP 已经通过前八篇建立了企业本体、Agent Registry、Harness、多智能体调度、权限控制与 ERP 操作网关。下一步必须解决一个基础问题：

如何让整个平台能够使用不同的 LLM、切换模型供应商、控制推理成本，并保证模型调用不会破坏企业业务流程的安全边界？

如果每个 Agent 都直接调用某一家模型供应商的 API，系统很快就会出现以下问题：

- Agent 代码绑定特定供应商的 SDK、模型名称和请求格式。
- 更换模型时，需要修改大量业务代码和 Prompt。
- 不同模型的结构化输出、工具调用和错误格式不一致。
- 模型成本、延迟、限流和故障分散在各个 Agent 中，难以统一治理。
- 企业敏感数据可能被发送到未经批准的外部模型服务。
- 模型升级后，即使 Agent、本体和业务规则没有变化，业务结果仍可能发生变化。
- 模型调用失败时，系统无法区分安全可重试的失败与可能产生重复副作用的操作。

因此，ODAP 应建设统一的 LLM Gateway（大模型网关），在 Agent Runtime 与实际模型供应商之间建立稳定、可治理的抽象层。

### 1.1 本篇的设计边界

Agent Runtime / Harness

任务上下文、执行预算、业务状态机

LLM Gateway

统一协议、策略路由、成本控制、数据治理、可观测性

Model Router

能力匹配与路由

Inference Governance

预算、脱敏与策略

云端模型

企业私有模型

兼容接口模型

本篇负责模型推理调用、模型供应商抽象、路由和治理，不替代前几篇已经定义的组件：

- Agent Runtime 仍然负责 Agent 生命周期和调用边界。
- Harness 仍然负责任务执行、检查点、恢复与人工审批。
- 权限引擎仍然负责业务授权。
- Ontology Runtime 仍然负责本体查询与语义验证。
- ERP Gateway 仍然是 ERP 写操作的安全边界。

尤其要强调：LLM Gateway 不是业务授权服务，也不是 ERP 操作网关。 即使模型给出“允许报销”的结论，或者生成了一段看似正确的工具调用参数，也不能因此绕过本体验证、权限判断和人工审批。

## 2. 总体架构

建议把模型调用体系分为六层。

| 层级 | 模块                      | 核心职责                 |
| -- | ----------------------- | -------------------- |
| L1 | Agent Model Client      | 为 Agent 提供稳定的调用接口    |
| L2 | LLM Gateway API         | 统一请求、响应、流式输出与错误协议    |
| L3 | Inference Policy Engine | 检查模型用途、数据等级、预算与允许的能力 |
| L4 | Model Router            | 根据能力、延迟、成本和治理要求选择模型  |
| L5 | Provider Adapter        | 将统一请求转换成供应商或本地模型协议   |
| L6 | Model Endpoint          | 实际执行推理的云端或本地服务       |

在 LLM Gateway 旁边，还应设置以下公共服务：

- Model Registry：管理模型标识、能力、供应商、部署位置和状态。
- Credential Manager：安全保存 API Key、服务凭据和本地端点访问凭据。
- Usage Ledger：记录模型调用、Token 用量、成本估算和预算扣减。
- Inference Telemetry：收集延迟、错误、限流、路由和模型质量指标。
- Policy Registry：保存租户、业务域和 Agent 的推理策略版本。
- Evaluation Service：对模型及路由策略进行离线评估和灰度验证。

### 2.1 关键架构原则

1. Agent 不直接持有供应商 API Key。
2. Agent 不直接依赖供应商 SDK。
3. 模型路由由平台策略决定，而不是让模型自行决定。
4. 模型能力声明必须经过验证，不能仅凭供应商返回的标签认定。
5. 每次调用必须关联租户、Run、Agent 版本和推理策略版本。
6. 所有调用均受预算、超时、并发和数据访问策略约束。
7. 模型输出默认是候选结果，而不是经过认证的业务事实。
8. 模型供应商切换不能改变企业权限、审批和 ERP 写入规则。
9. 模型升级和路由变化必须可追溯、可评估、可回滚。
10. 不能承诺不同模型之间具有完全相同的输出语义，应通过契约测试与业务评估管理差异。

## 3. 统一模型调用协议

LLM Gateway 最重要的设计不是接入多少家模型，而是建立一个足够稳定的内部协议。

建议定义统一的 `InferenceRequest`、`InferenceResponse`、`ModelCapabilities` 和 `ProviderError`。所有 Provider Adapter 都负责把供应商原生协议转换成这套内部协议。

### 3.1 InferenceRequest

建议包含以下字段：

| 字段                    | 说明                 |
| --------------------- | ------------------ |
| `request_id`          | 本次模型调用的唯一标识        |
| `tenant_id`           | 租户标识，由可信服务注入       |
| `run_id` / `step_id`  | 关联 Harness 运行与步骤   |
| `agent_ref`           | Agent 的确切版本        |
| `task_class`          | 任务类别，例如分类、提取、分析、规划 |
| `messages`            | 规范化的模型消息           |
| `response_mode`       | 文本、结构化 JSON 或流式输出  |
| `output_schema`       | 需要遵循的结构化输出 Schema  |
| `tool_definitions`    | 本次调用可见的工具声明        |
| `generation_config`   | 温度、输出 Token 上限等参数  |
| `routing_policy_ref`  | 已解析的路由策略版本         |
| `data_classification` | 输入数据的敏感等级          |
| `deadline`            | 绝对截止时间             |
| `budget_ref`          | 对应的预算账户或预算预留       |
| `trace_id`            | 全链路追踪标识            |

其中，`tenant_id`、`agent_ref`、预算账户和授权上下文必须来自可信的服务端运行环境。不能让用户或模型通过请求体随意指定其他租户或提升自己的预算。

### 3.2 Go 核心类型

以下代码是建议的内部接口草案：

```

package llmgateway

import (
    "context"
    "time"
)

type TaskClass string

const (
    TaskClassification TaskClass = "CLASSIFICATION"
    TaskExtraction     TaskClass = "EXTRACTION"
    TaskAnalysis       TaskClass = "ANALYSIS"
    TaskPlanning       TaskClass = "PLANNING"
    TaskEmbedding      TaskClass = "EMBEDDING"
)

type ResponseMode string

const (
    ResponseText ResponseMode = "TEXT"
    ResponseJSON ResponseMode = "JSON_SCHEMA"
)

type Message struct {
    Role    string
    Content []ContentPart
}

type ContentPart struct {
    Type string
    Text string
}

type GenerationConfig struct {
    MaxOutputTokens int
    Temperature     *float64
    TopP            *float64
}

type InferenceRequest struct {
    RequestID          string
    TenantID           string
    RunID              string
    StepID             string
    AgentRef           string
    TaskClass          TaskClass
    Messages           []Message
    ResponseMode       ResponseMode
    OutputSchema       []byte
    Tools              []ToolDefinition
    Generation         GenerationConfig
    RoutingPolicyRef   string
    DataClassification string
    Deadline           time.Time
    BudgetRef          string
    TraceID            string
}

type InferenceResponse struct {
    RequestID    string
    ProviderID   string
    ModelID      string
    ModelRevision string
    Output       []byte
    FinishReason string
    Usage        TokenUsage
    Latency      time.Duration
    RequestDigest string
    ResponseDigest string
}

type TokenUsage struct {
    InputTokens  int64
    OutputTokens int64
    TotalTokens  int64
}

type Gateway interface {
    Generate(
        ctx context.Context,
        req InferenceRequest,
    ) (InferenceResponse, error)
}

```

这份接口还需要在正式工程中进一步补齐多模态内容、流式事件、拒答标记、缓存元数据、供应商请求 ID、模型调用费用和安全诊断等字段。对于结构化输出，还应区分模型是否声明完成、是否发生截断，以及本地 Schema 验证是否通过。

### 3.3 为什么要定义统一协议

假设 ODAP 接入三个模型端点：

- 云端通用大模型。
- 企业内网部署的开源模型。
- 专门用于结构化信息抽取的模型。

Agent 只需提交统一的 `InferenceRequest`。Gateway 根据策略选定模型，Adapter 负责请求格式转换，返回统一的 `InferenceResponse`。

因此，业务 Agent 不需要知道某家供应商使用什么字段名称、怎样处理工具调用、如何表示 Token 用量或返回何种异常格式。

不过，统一协议并不意味着所有模型都能执行所有任务。某模型如果不支持严格的 JSON Schema 输出，就不能被标记为具备该能力；必要时可以采用生成后校验，但不能假装供应商已经提供了严格约束生成。

## 4. Model Provider Adapter：供应商适配层

建议为每一种供应商协议或部署类型实现独立 Adapter，而不是在业务代码中充满供应商名称判断。

### 4.1 适配器接口

```
type ModelCapabilities struct {
    TextGeneration       bool
    StructuredOutput     bool
    ToolCalling          bool
    Streaming            bool
    Embeddings           bool
    ImageInput           bool
    AudioInput           bool
    AudioOutput          bool
    ContextWindow        int
    MaxOutputTokens      int
    SupportedSchemaModes []string
}

type ModelEndpoint struct {
    ProviderID  string
    EndpointID  string
    ModelID     string
    Revision    string
    Capabilities ModelCapabilities
}

type Provider interface {
    ID() string

    DiscoverCapabilities(
        ctx context.Context,
        endpoint ModelEndpoint,
    ) (ModelCapabilities, error)

    Generate(
        ctx context.Context,
        endpoint ModelEndpoint,
        req InferenceRequest,
    ) (InferenceResponse, error)

    Health(
        ctx context.Context,
        endpoint ModelEndpoint,
    ) error
}
```

接口中不应直接暴露供应商原生响应结构，否则统一协议会失去意义。

对于流式生成，可以在同一套抽象下增加 `Stream` 方法，使用统一的事件类型，例如：

- `RESPONSE_STARTED`
- `TEXT_DELTA`
- `STRUCTURED_OUTPUT_DELTA`
- `USAGE_UPDATE`
- `RESPONSE_COMPLETED`
- `RESPONSE_FAILED`

应注意：并非每个供应商都支持全部事件，Adapter 需要明确声明能力，并在不支持时返回明确错误。

### 4.2 Adapter 的内部处理流程

统一 InferenceRequest

参数与能力检查

统一协议 → 供应商原生请求

HTTP / SDK / 内网推理端点

原生响应 → 统一响应与错误

Adapter 负责协议兼容，但不负责决定某个 Agent 是否有权读取工资数据或提交报销单。数据访问授权必须在请求进入模型之前，由更高层的策略和上下文构建机制处理。

### 4.3 供应商接入策略

建议把 Provider 分为三类：

| 类型        | 接入方式            | 主要注意事项           |
| --------- | --------------- | ---------------- |
| 官方云模型     | 官方 API 或受支持 SDK | 数据出境、区域、保留政策、限流  |
| 企业私有模型    | 内网推理服务          | 网络隔离、服务容量、模型制品管理 |
| 兼容 API 模型 | 兼容接口协议          | 兼容性测试、非标准扩展、能力差异 |

OpenAI 兼容接口等协议可以作为降低接入成本的选项，但协议兼容不代表语义兼容。结构化输出、工具调用、多模态、错误码、上下文长度与 Token 计量仍然需要通过契约测试确认。

### 4.4 能力发现与验证

Model Registry 中的模型能力不能仅凭人工填写或服务端自报。

建议能力确认流程为：

1. 获取供应商或部署端点声明。
2. 与平台已知的模型元数据进行比对。
3. 在受控测试环境中执行能力探测。
4. 运行结构化输出、工具调用、超时和错误处理测试。
5. 将通过验证的能力写入带版本的 Registry 记录。
6. 端点或模型升级后重新评估。

对于不可安全自动探测的能力，采用管理员确认加契约测试，不应直接对生产端点执行可能触发副作用的探测请求。

## 5. Model Registry：模型注册与版本管理

Model Registry 是 LLM Gateway 的基础元数据服务。它与 Agent Registry 不同：Agent Registry 管理“业务能力组件”，Model Registry 管理“推理能力端点”。

### 5.1 核心实体

建议至少管理以下对象：

- Provider：供应商或推理服务提供方。
- Endpoint：实际可调用的模型服务端点。
- Model：逻辑模型标识。
- Model Revision：模型版本、部署修订版或供应商可提供的版本标识。
- Capability Profile：经过验证的能力声明。
- Deployment Profile：云端、私有部署、区域与网络边界。
- Pricing Profile：适用的计价规则及生效时间。
- Model Status：可用、限流、暂停、退役或撤销。

建议的状态包括：

`DRAFT → VALIDATING → ACTIVE → DEGRADED → SUSPENDED → RETIRED`

对于存在严重安全或合规问题的端点，还可以使用 `REVOKED` 状态。

### 5.2 建议的数据表

| 表                         | 用途           |
| ------------------------- | ------------ |
| `llm.providers`           | 供应商元数据       |
| `llm.endpoints`           | 可调用端点        |
| `llm.model_revisions`     | 模型版本与部署修订    |
| `llm.capability_profiles` | 经验证的能力记录     |
| `llm.pricing_profiles`    | 计价规则与有效期     |
| `llm.endpoint_health`     | 健康检查与可用性     |
| `llm.routing_policies`    | 路由策略版本       |
| `llm.usage_ledger`        | 推理用量账本       |
| `llm.budget_reservations` | 预算预留与结算      |
| `llm.provider_events`     | 限流、故障和状态变化事件 |

供应商凭据不应以明文形式存储在这些普通业务表中。应保存指向企业密钥管理服务的引用，必要时使用加密存储，并限制读取和轮换权限。

### 5.3 模型版本锁定

一次 Agent Run 应记录确切的模型选择，而不是只记录“使用高性能模型”。

建议至少记录：

- `provider_id`
- `endpoint_id`
- `model_id`
- 可获得的 `model_revision`
- 路由策略版本
- 推理配置摘要
- Provider Adapter 版本
- 能力配置版本
- 计价规则版本
- 数据治理策略版本

对于托管模型，如果供应商不提供不可变的模型版本，ODAP 必须明确标记版本不可固定，并记录调用时间及供应商提供的标识。此时平台不能承诺模型行为可完全复现，应通过评估与灰度机制管理潜在变化。

## 6. Model Router：模型路由与选择策略

Model Router 负责回答一个问题：当前这项任务，应该调用哪个满足约束的模型？

不能简单地永远选择最便宜的模型，也不应所有任务都使用最强模型。ODAP 中的任务差异很大：简单字段提取、制度分析、多 Agent 规划、长文档处理和多模态识别，对能力、成本与时延的要求都不同。

### 6.1 路由的输入条件

建议将路由决策建立在以下条件之上：

| 条件    | 示例                         |
| ----- | -------------------------- |
| 任务类型  | 分类、提取、分析、规划、嵌入             |
| 数据等级  | 公开、内部、机密、严格受限              |
| 必需能力  | JSON Schema、工具调用、长上下文、图像输入 |
| 业务风险  | 低风险辅助、高风险业务分析              |
| 质量要求  | 通过指定评估集的最低门槛               |
| 延迟要求  | 同步交互、后台任务、批处理              |
| 成本预算  | 单次调用预算、Run 剩余预算            |
| 端点健康度 | 可用性、限流、错误率、队列延迟            |
| 租户配置  | 允许的供应商、地域和私有模型范围           |
| 部署约束  | 仅内网、指定区域、禁止外部调用            |

其中，数据安全和能力要求是硬约束；成本、延迟和模型质量通常是满足硬约束后的优化目标。

### 6.2 路由流程

1\. 接收任务

Task Class · Data Class · Deadline · Budget

2\. 硬约束过滤

数据策略、允许供应商、能力、租户限制

3\. 候选模型集合

仅保留已验证且可用的端点

4\. 策略评分与选择

质量、成本、时延、健康度

5\. 记录路由决策

策略版本、候选集、选择原因、预算预留

如果没有满足全部硬约束的模型，应返回 `MODEL_CAPABILITY_UNAVAILABLE`（基础码 `UNAVAILABLE`，见 docs/contracts/02-统一错误码注册表.md §3.4），而不是为了让请求成功而自动选择一个违反数据政策的端点。

### 6.3 路由策略示例

以下为示意性配置：

```

apiVersion: odap.io/v1
kind: ModelRoutingPolicy

metadata:
  id: finance.expense-analysis
  version: 1.0.0

spec:
  taskClasses:
    - EXTRACTION
    - ANALYSIS

  dataPolicy:
    allowedDeploymentTypes:
      - PRIVATE
      - APPROVED_CLOUD
    requireApprovedRegion: true

  requirements:
    structuredOutput: true
    minimumEvaluationProfile: finance-expense-quality-v1

  selection:
    mode: constrained-optimization
    objectives:
      quality:
        weight: 0.55
      latency:
        weight: 0.20
      cost:
        weight: 0.25

  fallback:
    allowCrossProvider: true
    requireSameDataPolicy: true
    requireCapabilityMatch: true
    requireBudgetRecheck: true

  safety:
    denyIfNoEligibleModel: true
    noSilentPolicyDowngrade: true

```

这里的权重只是示例，不能作为生产环境的通用最优值。正式路由应先排除不合规模型，再对合格候选模型进行比较。

### 6.4 模型评分

对合格候选模型，可以定义一个归一化评分：

\\[ S(m)=w_q Q(m)+w_l L(m)+w_c C(m)+w_h H(m) \\]

其中：

- \\(Q(m)\\)：经任务评估得到的质量评分。
- \\(L(m)\\)：时延表现的归一化得分。
- \\(C(m)\\)：成本效率得分。
- \\(H(m)\\)：端点健康度。
- \\(w_q,w_l,w_c,w_h\\)：经过治理审批的权重。

所有得分应归一化到可比较范围。质量指标需要来自与任务相关的评估集，而不能直接用模型参数规模、营销标签或某个通用排行榜分数替代。

更重要的是，路由不能把硬约束与评分混在一起。例如，一个质量评分很高但不允许接收该租户机密数据的云端模型，应当在评分前被排除。

**质量评分保鲜。** 评估集分数会过期，评分必须与端点 revision 绑定管理：

1. 对 revision 可固定的端点，质量评分随该 revision 登记，revision 不变则评分有效。
2. 对托管模型等不可固定 revision 的端点（§5.3），供应商静默升级即可让登记评分失效。对此类端点执行定期影子评估：按固定周期（默认 7 天，或按累计调用量触发）将抽样任务送往该端点，与锁定 revision 的基线端点对比，结果写入评估记录。
3. 影子评估与 §6.5 影子路由联动：影子路由流量同时作为质量保鲜的样本来源，不为评估额外制造业务外调用；影子结果不触发业务操作。
4. revision 漂移判定：影子评估相对登记评分的关键指标下降超过容差（默认 5%）时，判定为 revision 漂移，自动触发重新评估并暂停该端点的策略路由资格，直至重新评估通过；漂移事件记入审计与 `llm.provider_events`。

### 6.5 路由模式

建议支持四种模式：

- 固定路由：指定某个经过验证的模型，适合高可控性任务。
- 策略路由：根据能力、预算和治理要求自动选择。
- 故障切换：首选模型不可用时，在预先批准的候选集内切换。
- 影子路由：将同一任务送往额外模型进行评估，但影子结果不直接触发业务操作。

影子路由尤其适合模型升级测试，但必须评估额外的 Token 成本、敏感数据暴露面和供应商请求日志风险。

## 7. Inference Governance：推理治理引擎

推理治理引擎是 LLM Gateway 中不可缺少的安全组件。它不应只是几个配置项，而应成为可以审计、版本化和测试的策略执行服务。

建议将治理分成六个维度：

1. 数据分类与模型端点允许范围。
2. 租户和 Agent 预算。
3. 模型能力与任务匹配。
4. 调用频率和并发限制。
5. 内容处理与结构化输出要求。
6. 日志保留、审计与供应商治理。

### 7.1 治理决策

每次调用在发送到模型之前，至少需要通过以下检查：

```
ValidateRequest
    ↓
ResolveTenantAndPrincipal
    ↓
ResolveModelPolicy
    ↓
ClassifyInputData
    ↓
CheckAllowedEndpoints
    ↓
CheckRequiredCapabilities
    ↓
CheckBudgetAndQuota
    ↓
ApplyDataControls
    ↓
ReserveBudget
    ↓
RouteAndInvoke
```

任一步骤失败，都应有明确的错误码与处置方式。

例如，`MODEL_DATA_POLICY_DENIED` 表示当前数据不能发送到候选模型；`MODEL_BUDGET_EXCEEDED` 表示预算不足；`MODEL_CAPABILITY_UNAVAILABLE` 表示没有合格的模型支持所需能力（三者定义见 docs/contracts/02-统一错误码注册表.md §3.4）。这些错误不能通过换一个未经批准的模型来规避。

### 7.2 策略决策结果

建议统一采用以下结果：

| 决策                     | 含义              |
| ---------------------- | --------------- |
| `ALLOW`                | 请求满足策略，可以执行     |
| `DENY`                 | 请求违反硬性约束        |
| `ALLOW_WITH_TRANSFORM` | 只有完成脱敏或其他变换后才允许 |
| `ALLOW_WITH_LIMITS`    | 允许，但必须执行额外限制    |
| `NO_ELIGIBLE_MODEL`    | 没有符合全部要求的候选模型   |
| `REQUIRE_REVIEW`       | 需要管理员或数据责任人确认   |

这里的 `REQUIRE_REVIEW` 主要用于模型使用、数据分类或策略变更等治理事项，不等于企业业务审批。报销、付款、合同签署等业务操作仍由原有 Approval Service 负责。

上表是治理决策结果，不是对外错误码。决策映射到错误码时以 docs/contracts/02-统一错误码注册表.md 为准：`NO_ELIGIBLE_MODEL` → `MODEL_CAPABILITY_UNAVAILABLE`，`DENY`（数据分级禁止）→ `MODEL_DATA_POLICY_DENIED`，预算不足 → `MODEL_BUDGET_EXCEEDED`（§14.1）。

### 7.3 推理策略的层级

建议采用以下优先级：

1. 平台不可违反的安全底线。
2. 法律、企业合规和数据处理政策。
3. 租户级模型供应商与部署限制。
4. 业务域级策略。
5. Agent 级任务要求。
6. 单次调用的非安全性偏好。

下层策略只能进一步收紧限制，不能覆盖上层的硬性拒绝。

例如，Agent 申请“为了提高准确率，把全部报销附件发送给外部模型”，如果租户策略禁止机密附件出境，路由器必须拒绝，不得因 Agent 的质量要求而放宽策略。

## 8. 数据安全、隐私与模型调用边界

对于私有化部署的 ODAP，模型数据治理是核心卖点之一。必须明确：平台部署在企业内网，不代表每次模型调用都天然留在内网。 如果内网 Agent 调用了外部模型 API，相关输入仍可能离开企业控制域。

### 8.1 数据分类

建议建立统一的数据分类枚举：

| 等级             | 示例              | 默认处理方式                 |
| -------------- | --------------- | ---------------------- |
| `PUBLIC`       | 公开政策、公开产品资料     | 可按策略发送至批准的模型           |
| `INTERNAL`     | 普通内部业务信息        | 仅允许批准的部署与用途            |
| `CONFIDENTIAL` | 员工、合同、财务等敏感业务数据 | 默认限制外部发送，按批准策略处理       |
| `RESTRICTED`   | 特别严格受控的数据或密钥材料  | 默认禁止进入通用模型上下文，须有明确例外控制 |

分类应以企业正式制度为准。上述是 ODAP 的技术分类建议，不是对具体法律义务的判断。

### 8.2 脱敏与数据最小化

建议建立独立的 `Data Preparation Pipeline`：

1. 根据本次任务确定必需字段。
2. 读取数据分类与字段级敏感标签。
3. 删除无关字段。
4. 对允许变换的字段执行脱敏或假名化。
5. 检查变换结果是否满足任务需要。
6. 将处理结果及策略版本写入审计元数据。
7. 仅向合格模型端点发送允许的输入。

需要注意，脱敏并不总能消除风险。唯一标识、罕见组合和可关联字段仍可能重新识别主体。因此，是否允许外发应由数据策略决定，而不能仅以“已经脱敏”作为放行理由。

### 8.3 数据流与存储控制

建议明确管理以下位置：

- 模型请求正文。
- 模型响应正文。
- Provider Adapter 的临时缓冲区。
- 供应商侧的请求日志和保留机制。
- 本地日志、Tracing 和错误上报系统。
- 评估集、缓存与回放记录。
- 供应商或模型训练用途的相关条款。

默认建议：

- API Key 和访问凭据不进入 Prompt。
- 访问令牌不进入模型上下文。
- 日志默认不保存完整敏感 Prompt 和响应。
- 缓存按租户、数据范围和策略隔离。
- 对外部模型启用前，核实企业批准的保留、训练使用、处理区域与合同条款。
- 对不能证明满足数据要求的端点，默认不纳入候选集合。

### 8.4 Prompt Injection 与模型输入

LLM Gateway 可以协助实施输入检测、内容标记和风险分类，但不能单独承担全部提示注入防护。

ODAP 应继续采用第八篇的 Context Builder 设计，将可信系统策略与不可信业务内容分开，并保证模型无权直接改变：

- 租户身份。
- Agent 能力。
- 工具白名单。
- 本体版本。
- 预算限制。
- 授权和审批状态。
- ERP Gateway 的安全规则。

模型可以建议执行工具，但工具执行服务必须重新验证参数和权限。无论模型生成的文本多么可信，都不能把它当成可信控制指令。

## 9. 结构化输出与工具调用治理

ODAP 是本体驱动的企业智能体平台，因此模型输出不能停留在自然语言层面。模型需要能够输出符合业务 Schema 的结构化数据，并与 Ontology Runtime、Harness 和工具执行层协作。

### 9.1 结构化输出的处理链路

模型生成候选输出

JSON / Schema 语法验证

字段、类型、枚举与引用检查

本体约束与业务规则验证

返回已校验结果或诊断信息

这几个验证阶段有不同的意义：

- JSON 语法合法，不代表 Schema 合法。
- Schema 合法，不代表业务对象真实存在。
- 对象存在，不代表满足本体和业务规则。
- 本体验证通过，不代表当前用户有权执行写操作。
- 获得业务授权，也不代表所有需要人工审批的步骤已经批准。

这些验证必须保持分层，不能用一次模型输出验证替代所有业务控制。

**校验失败的修复回路。** 结构化输出校验失败不是简单返回错误，而是有界的修复回路：

1. 第一层校验（JSON 语法 / Schema）失败时进入受限重生成：将校验诊断作为修正提示回注，要求模型重新生成；重生成次数上限为 N（默认 2 次，按任务契约配置）。
2. N 次重生成后仍不合法，终止为 `OUTPUT_INVALID` 终态，错误码 `MODEL_OUTPUT_INVALID`（基础码 `VALIDATION_FAILED`，见 docs/contracts/02-统一错误码注册表.md §3.4）。该终态不进入下游步骤，也不把半成品结果包装成成功。
3. 本体验证失败不进入重生成回路：结构合法但语义或业务校验失败时，按 §9.2 状态返回诊断，由 Harness 决定修正输入或转人工，而不是让模型对同一个错误反复重试。

**能力声明必须区分两种结构化输出能力。** Model Registry 中分别登记（§4.4）：

- native schema：端点原生支持严格 Schema 约束生成，输出不合法概率低。
- generate-then-validate：端点只能自由生成后由本地校验，不合法概率高，必须走受限重生成回路。

路由时按任务对输出确定性的要求选择：高风险业务提取优先 native schema。constrained decoding（如 outlines、xgrammar 等约束解码库）可作为私有部署端点的可选增强，从解码层保证语法合法；但它不保证语义与业务正确，因此不能替代本体验证、权限检查与人工审批。

### 9.2 统一输出模式

建议模型输出支持以下状态：

| 状态                 | 说明              |
| ------------------ | --------------- |
| `VALID`            | 输出通过结构化校验       |
| `INVALID_SCHEMA`   | 输出不符合 Schema    |
| `TRUNCATED`        | 输出被截断，无法可靠解析    |
| `REFUSED`          | 模型拒绝完成请求        |
| `INCOMPLETE`       | 输出缺少完成任务所需的信息   |
| `SEMANTIC_FAILURE` | 结构合法，但语义或业务校验失败 |
| `UNKNOWN`          | 现有证据不足以确定结果     |

只有 `VALID` 才表示结构化输出校验成功，并不自动表示整个业务任务成功。

### 9.3 Schema 版本管理

Agent 的输出 Schema 应当版本化，建议在调用时记录：

- Schema ID。
- Schema 版本。
- Schema 摘要。
- 验证器版本。
- 验证结果与诊断。
- 后续本体验证所使用的版本。

Schema 更新可能导致字段语义变化。例如，将 `amount` 从“报销申请金额”改成“实际批准金额”，即使 JSON 类型完全相同，也可能改变业务含义。因此需要语义兼容性测试，而不能只比较字段类型。

### 9.4 工具调用的治理原则

模型原生工具调用功能只是生成结构化的工具请求，不是实际执行工具的权限。

正确流程为：

```
模型提出 Tool Call
       ↓
解析与 Schema 校验
       ↓
检查 Tool Registry
       ↓
检查当前 Agent 的能力范围
       ↓
由权限引擎进行授权判断
       ↓
必要时进入人工审批
       ↓
Harness 调度 Tool Executor
       ↓
记录结果、证据与审计
```

工具请求应具有明确的 `tool_call_id`、工具版本、参数摘要和关联 Run。工具执行结果也应经过 Schema 验证，防止错误数据进入后续 Agent。

不应把模型返回的工具参数直接拼接为 SQL、Shell 命令或任意脚本执行。 应优先采用具有固定契约、参数白名单和明确副作用等级的工具接口。

## 10. 多模型协作与模型降级策略

在 ODAP 中，多 Agent 协作往往意味着多次模型调用。模型降级可以提高可用性，但也可能改变任务质量和输出语义，因此需要严格管理。

### 10.1 降级层级

建议将降级分为四级：

1. 同模型端点恢复：短暂网络错误后，按照策略退避重试。
2. 同模型的备用端点：例如同一模型的另一个已批准部署实例。
3. 同等能力的备用模型：通过兼容性与质量评估的模型。
4. 确定性降级或人工接管：使用规则引擎、缓存的有效结果或人工处理。

不能保证每个任务都有合适的备用模型。如果没有符合数据、能力和质量要求的候选端点，正确做法是拒绝继续推理，或返回可恢复的失败状态。

### 10.2 哪些任务可以降级

| 任务类型       | 降级策略                     |
| ---------- | ------------------------ |
| 简单分类       | 可切换到已验证的轻量模型             |
| 固定格式提取     | 可切换到满足 Schema 与评估门槛的模型   |
| 复杂制度分析     | 仅切换到通过相同业务评估的候选模型        |
| 本体约束检查     | 优先使用确定性验证引擎，不用另一个 LLM 替代 |
| 权限判断       | 不能降级到 LLM；必须调用权限引擎       |
| ERP 写入状态核验 | 不能用模型猜测；必须查询账本和回执        |
| 高风险业务裁定    | 应按已批准策略转人工或暂停            |

### 10.3 故障切换状态机

```
REQUEST_CREATED
      ↓
POLICY_CHECKED
      ↓
PRIMARY_SELECTED
      ↓
INVOKING
   ┌──┴──────────────┐
   ↓                 ↓
SUCCESS           FAILURE
                     ↓
              CLASSIFY_ERROR
                     ↓
            ┌────────┴────────┐
            ↓                 ↓
      RETRYABLE           NON_RETRYABLE
            ↓                 ↓
     BUDGET_RECHECK        RETURN_ERROR
            ↓
     APPROVED_FALLBACK?
       ┌────┴────┐
       ↓         ↓
      YES        NO
       ↓         ↓
 SELECT_BACKUP  STOP
```

### 10.4 为什么不能对所有错误自动重试

模型请求通常是推理请求，但在以下场景中，盲目重试仍可能带来风险：

- 已经消耗大量 Token 或超过租户预算。
- 请求携带高敏感数据，重试意味着再次传输。
- 流式响应已经部分输出，重试会产生重复结果。
- 同一个 Run 的其他步骤已经使用了第一次的部分结果。
- 备用模型不具备相同能力或使用了不同的安全策略。

因此，重试和故障切换必须由 Gateway 结合错误分类、预算、任务状态和策略决定，而不是在 Provider Adapter 内部无条件实现。

## 11. Token 预算、成本控制与 Usage Ledger

模型调用的成本应成为 ODAP 的平台级资源，而不是分散在各个 Agent 中的临时统计。

### 11.1 预算层级

建议支持四级预算：

- 请求级：单次推理最大输入、输出和允许成本。
- Agent 级：某个 Agent 在一次 Run 中可使用的总推理资源。
- Run 级：一次完整业务任务的总预算。
- Tenant 级：租户每日、每月和并发使用额度。

预算还应区分硬性限制与软性告警：

- 硬性预算耗尽时，停止新增调用。
- 软性预算接近上限时，告警并触发更节约的策略。
- 已进入外部调用的请求不能假定能被即时取消或退费。
- 对业务关键任务，可以预留必要预算，避免普通任务耗尽所有资源。

### 11.2 预算预留与结算

推荐采用以下流程：

1. Gateway 在发起调用前估算输入、最大输出和预计成本。
2. Budget Service 原子预留预算。
3. 模型调用结束后，读取供应商提供的 Token 用量；若不可用，则标记为估算值。
4. Usage Ledger 记录实际用量或估算用量。
5. 根据适用计价规则结算。
6. 释放未使用的预留预算。
7. 如果调用失败但供应商仍可能收费，按已知信息结算或标记为待核对。

这里的费用不应被视为绝对精确：供应商计价可能变化，缓存命中、批处理、推理模式或其他计费维度也可能影响最终费用。因此需要保留计价规则版本和费用来源。

**tokenizer 版本管理。** 平台估算 Token 必须使用与供应商一致的 tokenizer，且 tokenizer 自身也要版本化：每次估算记录 tokenizer 名称与版本，写入 Usage Ledger 的估算条目。当供应商更换 tokenizer（或模型升级导致分词变化）时，历史估算与新估算之间会出现系统性偏差；此时必须在估算偏差登记表中记录偏差方向、样本量与影响窗口，并在对账与预算告警中显式使用修正系数，不得静默沿用旧偏差。

**供应商账单对账作业。** 每月运行一次"供应商账单 vs usage_ledger"对账作业：按租户、端点与账期核对供应商侧计费和平台账本，输出差异报告（未结算、重复结算、漏结算、估算偏差超阈值条目）。差异条目进入待核对状态，由平台管理员处置；计价规则版本变更不能追溯改变历史账本（§16.2），已结算记录不原地覆盖，修正通过调整记录完成。

**失败调用的自动结算规则。** 调用失败不等于不收费：供应商超时、熔断、取消之后仍可能产生费用。规则如下：

1. 结果未知或已发出的失败调用，默认按预留上浮系数自动结算：估算费用乘以上浮系数（初始建议 1.2，可按供应商历史偏差率调整），结算状态标记为 `PENDING_PROVIDER_CONFIRM`。
2. 月度对账或供应商账单到达后，将 `PENDING_PROVIDER_CONFIRM` 修正为实际值，差额记入调整记录。
3. 明确未发出（如策略拒绝、本地校验失败即返回）的调用不结算、不计费。

### 11.3 Usage Ledger 示例

```
{
  "usage_id": "usage_001",
  "tenant_id": "tenant_01",
  "run_id": "run_01",
  "step_id": "step_03",
  "request_id": "req_001",
  "provider_id": "provider_a",
  "model_id": "model_x",
  "model_revision": "revision_2026_10",
  "input_tokens": 4200,
  "output_tokens": 850,
  "usage_source": "PROVIDER_REPORTED",
  "pricing_profile_version": "2.1.0",
  "estimated_cost": "0.0124",
  "currency": "USD",
  "status": "SETTLED"
}
```

上述费用仅用于说明字段结构，不代表任何模型的实际报价。

### 11.4 并行 Agent 的预算问题

假设一次 Run 允许四个 Agent 并行执行。如果每个 Agent 都读取同一个“剩余预算 100”的快照，并各自认为可以消耗 80，最终总用量就可能超限。

解决方式是使用中央预算预留服务，或通过数据库原子扣减和条件更新分配额度。并行任务只能消费已预留的预算，不应自行假设共享预算尚未被其他任务使用。

### 11.5 成本优化策略

建议按以下顺序优化：

1. 减少不必要的上下文与重复查询。
2. 缓存可安全复用的结果。
3. 对简单任务选择轻量模型。
4. 对复杂任务采用分阶段推理。
5. 使用批处理或异步任务处理非实时请求。
6. 对失败重试设置预算上限。
7. 根据评估结果优化 Prompt 与输出长度。

不要简单地通过降低所有模型的推理能力来减少成本。对于高风险业务任务，节省成本必须服从质量门槛和安全要求。

## 12. 缓存与结果复用

模型调用缓存能够降低延迟与费用，但在企业业务系统中，缓存错误的结果可能比多调用一次模型更危险。

建议将缓存分为三类：

| 缓存类型        | 适用场景       | 风险控制            |
| ----------- | ---------- | --------------- |
| 语义无关的精确请求缓存 | 相同、可复用的请求  | 严格匹配版本与请求摘要     |
| 语义缓存（embedding 相似度） | 表述不同但语义等价的只读请求 | 相似度阈值与 TTL 双闸门，数据分级限制（§12.3） |
| 确定性任务结果缓存   | 固定输入的分类或提取 | 需要确定性或足够稳定的任务契约 |
| 本体与规则缓存     | 本体元数据、规则包  | 按不可变版本摘要隔离      |

对于非确定性模型推理，不应假定相同输入一定产生相同结果。缓存策略需要明确是否允许复用、有效期、适用范围和业务风险。

### 12.1 缓存键

建议缓存键至少包含：

```
tenant_id
+ data_scope_digest
+ normalized_request_digest
+ model_revision
+ provider_adapter_version
+ generation_config_digest
+ response_schema_digest
+ routing_policy_digest
+ data_policy_digest
```

如果任务涉及本体或规则语义，还应纳入相应版本摘要。

### 12.2 不应缓存的情况

以下场景默认不缓存，除非经过单独的安全设计：

- 请求依赖实时 ERP 状态。
- 结果包含用户特定权限判断。
- 结果依赖动态审批状态。
- 输入包含高敏感数据且没有明确的缓存许可。
- 结果随时间或业务状态变化。
- 请求含有不可复用的随机标识或副作用相关参数。

即使命中模型缓存，也不能跳过当前请求的身份校验、授权、数据新鲜度检查和业务验证。

### 12.3 语义缓存：三层结构与参数

精确 digest 只能命中"完全相同的请求"。对于表述不同但语义等价的请求（如"报销需要哪些凭证"与"报销凭证要求是什么"），可以在此基础上叠加语义缓存，但必须按层收敛风险。三层缓存自精确到宽松依次为：

| 层级 | 命中依据 | 适用请求 | 风险控制 |
| --- | --- | --- | --- |
| L1 精确缓存 | `normalized_request_digest` 全量相等（§12.1 缓存键） | 相同、可复用的请求 | 严格匹配版本与请求摘要；默认开启 |
| L2 语义缓存 | 请求 embedding 与历史请求的相似度 ≥ 阈值 | 表述不同、语义等价的只读推理 | 仅 `INTERNAL` 及以下数据分级；相似度阈值 + TTL 双闸门；人工抽查 |
| L3 本体规则缓存 | 命中已发布本体或规则包的确定性结论 | 可由本体规则直接推导的结论 | 结论必须附带规则版本与推导链，按不可变版本摘要隔离 |

相似度阈值与 TTL 参数（初始建议值，按租户与业务域可调整）：

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `semantic_similarity_threshold` | 0.95 | 低于该值的相似请求不命中 L2，避免把不同意图误判为同一请求 |
| `semantic_top_k` | 5 | 仅与最近 K 条候选比较，控制检索成本 |
| `semantic_cache_ttl` | 24 小时 | L2 条目存活期；超期即失效，不自动续期 |
| `rule_cache_ttl` | 与本体包版本绑定 | L3 条目随规则包版本失效，不设独立时钟 TTL |
| `exact_cache_ttl` | 按任务契约配置 | L1 条目存活期，确定性任务可延长 |

关键约束：

1. 缓存键必须包含数据分级标签（`data_classification`）：分级为 `RESTRICTED` 的请求禁用 L2 语义缓存，`CONFIDENTIAL` 默认禁用 L2（见 §8.1 数据分类）；数据分级变更时，即使请求摘要相同也不得跨级命中。
2. 命中响应必须回传 `cache_hit=true`，并附命中层级（`L1`/`L2`/`L3`）与原始 `request_id`。Agent 侧必须把缓存命中结果降权使用：命中条目在证据链中标记为"复用结果"而不是"本次新证据"，不得作为新的事实来源写入证据库，也不得因命中而跳过身份校验、授权、数据新鲜度检查与业务验证（§12.2）。
3. 语义缓存只服务只读推理：任何涉及 ERP 写入、审批状态、权限判断的请求不进入 L2/L3。
4. L2 条目按租户与数据范围隔离存储，检索时不跨租户、不跨数据范围比较 embedding。

## 13. 流式输出与长任务推理

LLM Gateway 应支持同步调用与流式调用，但两者的业务语义需要明确区分。

### 13.1 两种调用模式

同步推理适合：

- 简单分类。
- 短文本提取。
- 结构化字段补全。
- 需要快速返回的辅助任务。

流式推理适合：

- 长文本生成。
- 用户交互式分析。
- 需要逐步展示进展的任务。

对企业业务执行，建议使用流式输出改善交互体验，但不要把“已经显示部分文本”当成任务已经完成。只有完整响应到达、输出校验通过且 Harness 确认步骤状态后，才可以提交正式结果。

### 13.2 流式事件协议

建议统一定义以下事件：

```
{
  "event_type": "TEXT_DELTA",
  "request_id": "req_001",
  "sequence": 12,
  "payload": {
    "text": "建议先核对报销凭证"
  }
}
```

事件顺序应在同一请求内可追踪。对于断线恢复，可以记录最后已确认的事件序号，但不应假设所有供应商都支持从中断位置继续生成。无法恢复时，Gateway 应明确返回中断状态，由调用方决定重新请求还是终止。

### 13.3 长任务的异步模式

长时间模型任务不应一直占用 HTTP 连接。建议提供：

- 创建推理任务。
- 返回 `request_id`。
- 查询任务状态。
- 订阅事件或轮询结果。
- 请求取消。
- 获取最终输出与用量。

取消只是停止继续发起或尽可能中止推理请求，不代表已经产生的供应商费用可以撤销，也不代表下游业务操作自动回滚。

## 14. 错误模型、限流与可用性治理

统一错误模型是多供应商架构稳定运行的关键。本节依次定义错误码映射（§14.1）、熔断器（§14.2）与限流闭环（§14.3）。

### 14.1 错误码

统一错误模型是多供应商架构稳定运行的关键。Gateway 对外错误码统一引用 docs/contracts/02-统一错误码注册表.md（唯一真源），本篇不另行定义错误码。Provider Adapter 负责把供应商原生错误映射为下表已登记的模型域（MODEL）域码与基础码。

模型域（MODEL）域码：

| 域码 | 基础码 | 含义 | 默认处理 |
| --- | --- | --- | --- |
| `MODEL_DATA_POLICY_DENIED` | `PERMISSION_DENIED` | 数据分级禁止发送该模型；不能靠换未批准模型规避 | 不重试，记审计 |
| `MODEL_BUDGET_EXCEEDED` | `RESOURCE_EXHAUSTED` | 预算不足，停止新增调用 | 停止新增调用，不无限重试 |
| `MODEL_CAPABILITY_UNAVAILABLE` | `UNAVAILABLE` | 无合格模型支持所需能力（含无合格模型、能力不匹配） | 重新评估候选或安全失败 |
| `MODEL_OUTPUT_INVALID` | `VALIDATION_FAILED` | 输出未过五层校验（受限重生成 ≤N 次后仍失败，§9.1） | 隔离结果并记录诊断 |
| `MODEL_PROVIDER_UNAVAILABLE` | `UNAVAILABLE` | 供应商不可用或熔断中 | 退避重试或故障切换 |

供应商原生错误的映射规则：

| 供应商原生错误 | 映射到 |
| --- | --- |
| 认证或凭据失败 | `UNAUTHENTICATED` |
| 限流 | `RATE_LIMITED`（尊重 `Retry-After`） |
| 服务不可用、熔断器打开 | `MODEL_PROVIDER_UNAVAILABLE` |
| 调用超时 | `DEADLINE_EXCEEDED`；结果未知时升级为 `EXECUTION_UNKNOWN` 并走对账 |
| 上下文超限、参数不合法 | `INVALID_ARGUMENT` |
| 预算不足 | `MODEL_BUDGET_EXCEEDED` |
| 输出校验失败 | `MODEL_OUTPUT_INVALID` |

模型拒答（`REFUSED`）是合法的输出状态（§9.2），不是错误码，按业务策略处理；请求已取消（`CANCELLED`）属于调用方意图，停止后续处理即可，两者均不需要单独错误码。所有响应同时返回 `code`（域码）与 `base_code`（基础码），结构见 contracts/02 §4。

### 14.2 熔断器

建议为每个模型端点维护独立熔断器，观察：

- 连续失败次数。
- 滚动窗口错误率。
- 请求延迟。
- 限流比例。
- 健康检查结果。

熔断器进入打开状态后，应暂时停止向该端点发送新请求；经过冷却时间后，进入半开状态，使用少量受控请求探测恢复情况。

熔断器只能帮助控制可用性，不应自行决定绕过数据政策。备用端点仍须通过相同的治理检查。

### 14.3 限流策略

至少应对以下六层对象实施限流：

- 租户级请求速率限制。
- Agent 级并发限制。
- 模型端点级并发限制。
- 供应商账户级配额控制。
- 高成本任务的专门限流。
- 后台批处理与交互请求的资源隔离。

**令牌桶参数表。** 每个限流对象维护一个令牌桶，参数按"端点 × 租户"配置，默认值与单租户上限如下（初始建议，可按租户合同与端点容量调整）：

| 维度 | 默认 RPM | 默认 TPM | 默认并发 | 单租户上限 |
| --- | --- | --- | --- | --- |
| 交互式端点（同步 `/generate`、`/stream`） | 60 | 200,000 | 4 | RPM ≤ 600，TPM ≤ 2,000,000，并发 ≤ 32 |
| 批量端点（`/embeddings`、异步 `/jobs`） | 600 | 2,000,000 | 16 | RPM ≤ 6,000，TPM ≤ 20,000,000，并发 ≤ 128 |
| 高成本任务（长上下文、多步规划） | 10 | 500,000 | 2 | RPM ≤ 60，TPM ≤ 3,000,000，并发 ≤ 8 |

桶容量（burst）默认为单分钟配额的 1.5 倍（批量端点 2 倍），允许短时突发；令牌按毫秒级补充速率持续回填。RPM 超限与 TPM 超限分别计数，任一维度耗尽即触发超限行为。

**租户配额分配算法：公平份额 + 突发桶。**

1. 公平份额：`tenant_share = endpoint_quota / active_tenants`（活跃租户数按滚动窗口统计），任何租户不得长期占用超过公平份额的资源。
2. 突发桶：每个租户在公平份额之外拥有容量为 `burst = 1.5 × tenant_share` 的令牌桶，允许短时超份额使用；突发额度耗尽后回到公平份额速率。
3. 空闲回收：租户未使用的份额进入共享池，供已耗尽突发桶的租户按等待时间顺序申请，避免"配额闲置与其它租户被拒"并存。
4. 动态调整：端点健康度下降（熔断半开、错误率升高）时，按策略同比例收缩各租户份额，并记录策略版本。

**超限行为判定规则。** 超限时不区分任务类型地全部拒绝，而按请求性质判定：

| 请求性质 | 判定 | 行为 |
| --- | --- | --- |
| 可排队任务（异步批处理、后台作业） | 入队等待 | 进入限流队列，按优先级与截止时间调度；受理方返回 `202` 与任务引用 |
| 交互式请求（用户等待中的同步调用） | 立即拒绝 | 返回 `429` + `Retry-After`（按令牌回填时间计算），基础码 `RATE_LIMITED`（contracts/02 §2） |

**队列容量上限与背压。** 限流队列不是无限缓冲：

- 队列容量上限按端点配置（初始建议：每端点 1,000 条或每租户 200 条），达到上限后新任务直接返回过载结果，由调用方退避后重试。
- 队列中任务超过截止时间即失效并释放配额，失效计入指标（§15.2）。
- 队列等待时间超过阈值（默认 30 秒）触发背压：暂停该端点的新交互式受理并告警，直到积压下降到安全水位。

## 15. 可观测性、审计与运行指标

模型治理不能仅依靠日志排查。建议将每次推理调用纳入 ODAP 的统一 Trace、Metrics 和 Audit 体系。

### 15.1 Trace 关联关系

```
Business Request
  └── Agent Run
       └── Harness Step
            └── LLM Gateway Request
                 ├── Policy Evaluation
                 ├── Routing Decision
                 ├── Provider Attempt
                 ├── Retry / Fallback
                 ├── Usage Settlement
                 └── Output Validation
```

每个节点都应使用统一的 `trace_id`、`run_id` 和 `request_id`，便于从业务结果追溯到实际模型调用。

### 15.2 核心指标

| 指标类别 | 指标示例                       |
| ---- | -------------------------- |
| 可用性  | 成功率、失败率、熔断次数               |
| 性能   | 首 Token 延迟、总延迟、P50/P95/P99 |
| 成本   | 每 Run 费用、每任务 Token、预算超限率   |
| 质量   | Schema 通过率、业务评估通过率、拒答率     |
| 路由   | 各模型使用量、备用路由比例、无合格模型比例      |
| 治理   | 策略拒绝次数、数据变换次数、敏感请求拦截数      |
| 稳定性  | 超时率、限流率、重试率、缓存命中率          |

注意，首 Token 延迟只适用于流式请求；并非所有模型都会返回完整、准确的 Token 使用量，因此指标中应区分供应商报告值、平台估算值和未知值。

### 15.3 审计事件

建议为以下动作记录独立审计事件：

- 模型注册与端点修改。
- 能力声明验证。
- 路由策略创建与修改。
- 供应商凭据轮换。
- 数据策略修改。
- 模型调用被允许或拒绝。
- 路由与故障切换。
- 预算预留与结算。
- 模型版本或端点撤销。
- 灰度发布与回滚。

审计记录应包含执行主体、时间、租户、策略版本、目标对象、变更摘要和结果。敏感请求正文不应被默认完整复制到审计日志中。

## 16. LLM Gateway 数据库设计

建议沿用 ODAP 现有 PostgreSQL 多租户与迁移规范，建立 `llm` Schema，并将推理调用账本与 Harness 的运行账本关联起来。

### 16.1 基础表设计

下面是一个简化的核心表结构示例：

```

CREATE SCHEMA IF NOT EXISTS llm;

CREATE TABLE llm.providers (
    provider_id       TEXT PRIMARY KEY,
    display_name      TEXT NOT NULL,
    provider_type     TEXT NOT NULL,
    status            TEXT NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE llm.endpoints (
    endpoint_id       UUID PRIMARY KEY,
    provider_id       TEXT NOT NULL
        REFERENCES llm.providers(provider_id),
    endpoint_name     TEXT NOT NULL,
    deployment_type   TEXT NOT NULL,
    region_code       TEXT,
    credential_ref    TEXT,
    status            TEXT NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE llm.model_revisions (
    model_revision_id UUID PRIMARY KEY,
    endpoint_id       UUID NOT NULL
        REFERENCES llm.endpoints(endpoint_id),
    model_id          TEXT NOT NULL,
    revision_label    TEXT,
    capability_digest TEXT NOT NULL,
    pricing_profile_id TEXT,
    status            TEXT NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE llm.usage_ledger (
    usage_id           UUID PRIMARY KEY,
    tenant_id          UUID NOT NULL,
    run_id             UUID NOT NULL,
    step_id            UUID,
    request_id         TEXT NOT NULL,
    endpoint_id        UUID NOT NULL,
    model_revision_id  UUID,
    input_tokens       BIGINT,
    output_tokens      BIGINT,
    usage_source       TEXT NOT NULL,
    estimated_cost     NUMERIC(20, 8),
    currency           CHAR(3),
    settlement_status  TEXT NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, request_id)
);

CREATE INDEX idx_llm_usage_tenant_time
    ON llm.usage_ledger (tenant_id, created_at);

CREATE INDEX idx_llm_usage_run
    ON llm.usage_ledger (tenant_id, run_id);

```

该 SQL 是结构草案，生产实现还需增加租户 RLS、端点状态约束、版本关系校验、费用来源、计价版本、账本更正记录和相应外键。

### 16.2 一致性要求

- `usage_ledger` 不应因重试而重复记录同一个逻辑结算。
- 一次逻辑请求可能有多个 Provider Attempt，因此建议区分 `inference_requests` 和 `inference_attempts`。
- 预算预留、扣减和释放必须具有幂等性。
- 已结算的账本记录不应原地覆盖；修正应通过调整记录或追加事件完成。
- 供应商响应超时后，不能假设该请求没有发生或没有费用。
- 计价规则更新不能改变历史请求的计费依据。

建议进一步增加：

- `llm.inference_requests`
- `llm.inference_attempts`
- `llm.routing_decisions`
- `llm.routing_policies`
- `llm.pricing_profiles`
- `llm.budget_reservations`
- `llm.endpoint_health`
- `llm.audit_events`

## 17. LLM Gateway API 设计

建议提供平台内部调用 API 与管理 API 两类接口。

### 17.1 推理 API

| 方法     | 接口                             | 功能       |
| ------ | ------------------------------ | -------- |
| `POST` | `/api/v1/llm/generate`         | 非流式生成    |
| `POST` | `/api/v1/llm/stream`           | 流式生成     |
| `POST` | `/api/v1/llm/embeddings`       | 向量嵌入     |
| `POST` | `/api/v1/llm/jobs`             | 创建异步推理任务 |
| `GET`  | `/api/v1/llm/jobs/{id}`        | 查询异步任务   |
| `POST` | `/api/v1/llm/jobs/{id}/cancel` | 请求取消     |

对外 API 应使用 ODAP 的统一身份认证与访问控制。内部服务间调用还应验证服务身份、租户上下文与请求完整性，不能仅依赖网络内网位置。

### 17.2 管理 API

| 方法     | 接口                                    | 功能         |
| ------ | ------------------------------------- | ---------- |
| `GET`  | `/api/v1/llm/providers`               | 查询供应商      |
| `POST` | `/api/v1/llm/providers`               | 注册供应商      |
| `GET`  | `/api/v1/llm/endpoints`               | 查询端点       |
| `POST` | `/api/v1/llm/endpoints`               | 注册端点       |
| `POST` | `/api/v1/llm/endpoints/{id}/validate` | 验证端点       |
| `POST` | `/api/v1/llm/endpoints/{id}/suspend`  | 暂停端点       |
| `GET`  | `/api/v1/llm/routing-policies`        | 查询路由策略     |
| `POST` | `/api/v1/llm/routing-policies`        | 创建策略版本     |
| `GET`  | `/api/v1/llm/usage`                   | 查询用量       |
| `GET`  | `/api/v1/llm/budgets`                 | 查询预算状态     |
| `GET`  | `/api/v1/llm/health`                  | 查询模型服务健康状态 |

供应商凭据的新增、更新和轮换应通过受控密钥管理流程完成。普通查询接口不应返回明文密钥。

## 18. 前端：模型治理控制台

建议在 ODAP 管理端新增一个一级模块：模型与推理治理中心。

[Next.js AI Operations Dashboard | NeuralDesk | thefrontkit](https://images.openai.com/static-rsc-4/7KAZEs7j-MEGCKlcpJHxhq59jV-MiOQDMVofQvu3iSUDM-IZjNFT9VVcaemmnPydhAnRDqO2x7YeMUWMpRrC9ltpM4IwyakJdZYZaXZV4BI6Nfd__JDl61qQhrZUEEJXD5i_rpgjdDYbpIyvBoczVUyhX4JBo-iDMRj-pzEpl0s?purpose=inline)

18.1 模型注册中心

管理供应商、端点、部署类型、模型版本、能力、健康状态和验证结果。可以快速发现模型能力缺失与端点故障。

[6 best LLM gateways for developers in 2026 - Articles - Braintrust](https://images.openai.com/static-rsc-4/v1s1m3yHoUHCQO0Q6ptsNrhspjVNsW-vwXwdWzBXTLgCcT5nWEtV-xuHmpYAy5TgWHzsVcAXToynPqUMnKYwNqg9y7OnREQESL95CM_xI4A8GpEdD7BrifEZultuTno1Ef6DR2MHOajhVwD1NZf1ls4CusLHx1GZ5hpMn0KguEU?purpose=inline)

18.2 路由策略工作台

配置任务类型、允许的模型集合、质量门槛、成本与时延目标、故障切换策略，并查看策略变更影响。

[AI Observability • Token-Level Cost, Energy & Carbon Intelligence with Antarctica](https://images.openai.com/static-rsc-4/cys36ysmpF50XNzNOwTsa7VKsV4mHZlmhRwyfQIakBElf6wEglYG7I25UXVbf8A42cqxivBG-c_JHy8_LRgNoknAvFJ-p9x0vgmbU41QxQmB48MZ_-Fzy5C90DsT6EW_DbI8ClbQydTPcwt8PE4dLjrzPV644Y_-AISZt4mMtTU?purpose=inline)

18.3 用量与预算中心

展示租户、Agent、Run 和模型维度的 Token、费用估算、预算使用率、异常增长与限流情况。

[Discover and classify sensitive data - Data Access Security](https://images.openai.com/static-rsc-4/743PRKIyyqrf0FeylWCMjCbYufrSdH4O4J2SJ6STFI8vgUy7f1v2U9C2G20flsd383wtnGhfHEsk7LTWKR-ziNkrB37eii4-LYubluKdezFd3K0ThOrOwPyhaAA92YiO2lHpHbtVadWT6iggtkLX0Aemlgbaw5loP54erbMf8II?purpose=inline)

18.4 数据与推理策略中心

管理数据等级、允许的部署类型、地域要求、脱敏策略、日志保留策略和模型调用审批。

[Compare models using the model leaderboard - Microsoft Foundry | Microsoft Learn](https://images.openai.com/static-rsc-4/yk1zorSmK_SWa5F9JUsibPyp8tSX_T_U5OJ8wUv5FxF8n6HSTjJGb4WNP5Ae_vPZF7l6foRwlrqaWaE34mIg3eLSQahpaWp8dsfcsPgL7zRVUxiGEcHUeba4_3Wyc9QoVhOPFXn5kxKQfJkOQQkdEUxCGN-cJRhqq1iT4nC7S74?purpose=inline)

18.5 模型评估与发布中心

对比模型和路由策略的质量、延迟、成本及安全测试结果，支持影子评估、灰度启用和回滚。

以上为视觉方向示例，具体组件应遵循 ODAP 统一设计系统。

### 18.6 单次推理调用详情

建议为每次调用提供一个可审计的详情页，显示：

- 触发该调用的 Run、Step 和 Agent 版本。
- 任务类型、数据分类和路由策略版本。
- 候选模型及其被选中或排除的原因。
- 实际调用端点、可获得的模型版本标识。
- 延迟、Token 用量、成本来源和预算结算状态。
- 输出 Schema 与校验结果。
- 重试、故障切换和错误分类。
- 输入摘要、输出摘要及证据引用。
- 数据治理检查结果。

对敏感 Prompt 和原始响应，应根据用户权限、数据分类和保留策略决定是否允许查看，不应默认对所有管理员开放完整内容。

## 19. 测试体系与验收标准

LLM Gateway 的测试不能只验证“能否调用模型”。必须同时验证协议兼容、数据安全、预算、路由、故障恢复和业务约束。

### 19.1 测试分层

| 测试层  | 测试内容                        |
| ---- | --------------------------- |
| 单元测试 | 参数转换、错误映射、成本计算、路由评分         |
| 契约测试 | Provider Adapter 与统一协议的一致性  |
| 集成测试 | Gateway、预算、Registry、密钥管理的协作 |
| 安全测试 | 越权调用、敏感数据泄露、日志泄露、策略绕过       |
| 故障测试 | 限流、超时、断线、熔断、备用端点            |
| 质量评估 | 结构化输出、业务任务准确性、模型升级影响        |
| 性能测试 | 并发、P95 延迟、队列积压、预算服务压力       |
| 恢复测试 | Worker 重启、重复请求、未结算调用和任务恢复   |

### 19.2 必须通过的测试场景

建议将以下用例列为 MVP 发布门禁：

1. Agent 不能直接访问供应商凭据。
2. 未登记或未验证的模型端点不能被生产路由选中。
3. 不满足数据政策的模型必须被排除。
4. 没有符合要求的模型时，系统返回明确的无可用模型状态。
5. 输出 Schema 不合法时，下游业务步骤不会继续。
6. 模型超时后，预算和请求状态不会被错误地重复结算。
7. 两个并行 Agent 不能各自消费同一份未扣减的预算。
8. 同一逻辑请求的重复提交不会导致重复的预算结算。
9. 供应商限流时，重试遵守退避、截止时间和预算限制。
10. 故障切换不能绕过租户模型白名单和数据政策。
11. 模型升级导致输出变化时，评估和灰度机制能够发现差异。
12. 模型返回的工具调用不能绕过工具执行器的授权。
13. 模型端点撤销后，新请求不会继续使用该端点。
14. 高敏感 Prompt 不会因普通错误日志或 Trace 自动泄露。
15. 缓存命中不会绕过当前请求的权限、数据新鲜度和策略检查。
16. 影子模型的输出不会未经验证地触发业务写入。
17. 无法确认实际 Token 用量时，账本会明确标记估算值或未知值。
18. 任务取消后，系统能够区分模型调用终止、费用结算和业务步骤状态。

### 19.3 质量评估建议

模型评估应以业务任务为中心，而不是仅以通用文本评分为依据。

对报销业务域，可以建立以下指标：

- 报销字段抽取的准确率与召回率。
- 必需字段缺失的识别率。
- 规则引用与来源对应的正确率。
- 结构化输出通过率。
- 对未知事实和冲突事实的处理正确率。
- Prompt Injection 场景中的安全违规率。
- 备用模型与主模型的业务结果差异。
- 单个成功任务的平均推理成本。
- 端到端任务延迟。

安全关键指标应设置硬性门槛。不能因为平均准确率较高，就允许模型在未授权工具调用或敏感数据处理方面存在已知违规。

## 20. MVP 实施路线

建议遵循“先建立统一调用接口，再增加路由与治理，最后优化成本和智能调度”的顺序。

P1：统一协议与 Provider Adapter

完成 InferenceRequest、InferenceResponse、错误模型和第一个真实或测试端点。

验收：Agent 不依赖供应商 SDK；协议转换、超时和错误映射均有测试。

P2：Model Registry 与凭据管理

完成 Provider、Endpoint、Model Revision、能力配置与安全凭据引用。

验收：未验证端点不可生产使用；密钥不进入普通业务表和日志。

P3：Model Router 与推理策略

完成候选过滤、固定路由、策略路由、数据等级检查和有限故障切换。

验收：数据政策优先于成本与延迟；无合格模型时能够安全失败。

P4：预算、用量账本与可观测性

实现预算预留、调用计量、成本估算、审计、Tracing 与限流。

验收：并发调用不会超额消费同一预算；重试和故障能够被追踪。

P5：结构化输出与安全治理

接入 Schema 验证、数据变换、工具调用治理、敏感日志控制和安全测试。

验收：不合规输出不能进入下游业务步骤；模型不能伪造权限或审批。

P6：评估、灰度与前端治理中心

建立模型评估集、影子路由、灰度启用、版本撤销与控制台。

验收：升级影响可量化；故障时可暂停端点或恢复已验证的策略版本。

### 20.1 推荐的第一阶段范围

MVP 不建议一开始就接入十几家模型供应商，也不需要立即实现复杂的自动路由算法。

第一阶段完成以下最小闭环即可：

1. 一个统一 LLM Gateway。
2. 一个经过验证的模型端点。
3. 一个 Provider Adapter。
4. 一个结构化输出 Schema。
5. 一个预算和用量账本。
6. 一套基础数据分类与端点白名单。
7. 与 Agent Runtime 的版本锁定和 Trace 集成。
8. 一个调用详情页面。

完成后，再增加第二个模型端点，验证供应商切换、能力差异、预算结算和故障切换是否真正有效。

## 21. 推荐代码目录

在现有 ODAP 仓库中，建议增加以下模块：

```
odap/
├── internal/
│   ├── llmgateway/
│   │   ├── api/
│   │   ├── protocol/
│   │   ├── routing/
│   │   ├── governance/
│   │   ├── budget/
│   │   ├── usage/
│   │   ├── cache/
│   │   ├── streaming/
│   │   ├── telemetry/
│   │   └── errors/
│   ├── modelregistry/
│   ├── providers/
│   │   ├── provider_a/
│   │   ├── provider_b/
│   │   └── openai_compatible/
│   ├── credentials/
│   ├── evaluation/
│   ├── agentruntime/
│   ├── harness/
│   ├── authorization/
│   ├── ontologyruntime/
│   └── persistence/
├── schemas/
│   ├── inference-request.schema.json
│   ├── inference-response.schema.json
│   ├── model-capabilities.schema.json
│   └── routing-policy.schema.json
├── tests/
│   ├── provider-contract/
│   ├── routing/
│   ├── governance/
│   ├── security/
│   ├── recovery/
│   └── evaluation/
└── deployments/
    └── config/
```

目录名可以与前几篇的实际仓库结构进一步合并。特别要避免将 `llmgateway` 和 `agentruntime` 写成互相循环依赖的包：Agent Runtime 依赖 Gateway 接口，Gateway 不应反过来依赖具体业务 Agent 的实现。

## 22. 本篇最终架构决策

建议将以下设计作为 ODAP V1.0 的模型治理基线：

1. 所有 Agent 的模型调用统一经过 LLM Gateway。
2. Provider Adapter 负责协议转换，不能自行绕过统一治理流程。
3. Model Registry 管理供应商、端点、能力、版本与计价规则。
4. Model Router 先执行安全与能力过滤，再优化成本、质量和延迟。
5. 数据分类、脱敏、预算、限流和日志保留属于平台级策略。
6. 模型调用结果必须经过输出校验，业务事实还要经过本体与数据验证。
7. 工具调用由 Harness 和独立工具执行层控制，模型输出不构成执行授权。
8. 每个 Run 应记录实际使用的模型标识、可获得的版本信息、路由策略与推理配置。
9. 用量账本必须支持预算预留、幂等结算、估算值标记与历史计价版本。
10. 模型降级只能在满足同等数据政策与必要能力的候选集合内进行。
11. 模型升级、路由变化和供应商切换必须经过评估、灰度与可回滚的发布流程。
12. 企业私有化部署不应被误解为所有模型调用天然在内网；每个端点都需要明确的数据流与安全边界。

第九篇完成后，ODAP 就具备了从业务 Agent 到模型供应商之间的统一推理控制层。 这使企业能够更换模型、控制成本、统一安全策略，并将模型变化纳入正式的工程治理流程，而不必反复改写每个 Agent。

下一篇建议编写《第十篇：ODAP 任务事件总线、异步消息与分布式可靠执行详细设计》，重点解决 Agent Worker 扩容、事件投递、Outbox、任务租约、幂等消费、死信队列、跨服务状态一致性，以及 Harness 在多实例环境中的恢复与调度问题。这样可以把当前的模块化平台进一步推进到能够稳定承载企业级并发任务的运行架构。