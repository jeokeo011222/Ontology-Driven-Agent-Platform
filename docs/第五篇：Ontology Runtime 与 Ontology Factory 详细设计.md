# 《第五篇：Ontology Runtime 与 Ontology Factory 详细设计》

ODAP · Ontology-Driven Agent Platform · 核心技术设计

> **本篇 scope 标签**：`[MVP-V1.0]` 本体包与 Registry（§四/§五）、8 阶段 Factory 流水线与断点续跑（§六）、Policy IR 算子白名单与数值语义（§七）、Runtime 查询/验证与资源预算（§八）、Instance Layer 经 ERP Port 实时查询（§8.2/§8.4）、Mock ERP 端到端（§十五）；`[V1.5]` SPARQL 受限开放、Factory 并行阶段调度；`[V2]` 可视化本体编辑器高级特性。状态与 API 契约以 `contracts/01`–`03` 为唯一真源，本篇只引用不重定义。

## 1. 设计目标：让企业本体从静态知识模型变成可执行的业务语义资产

ODAP 与普通 AI Agent 平台的关键区别，在于它不只让 Agent 阅读企业文档、调用工具，而是通过企业本体建立统一的业务语义层，让智能体能够理解业务对象、对象之间的关系、业务约束以及操作的合法性。

例如，在企业报销业务中：

- ERP 记录员工、部门、报销单、预算和付款等业务事实。
- 企业制度规定报销条件、审批权限、费用标准与例外情形。
- Ontology 将这些概念、关系、约束和制度依据组织为机器可处理的语义模型。
- Agent 根据本体理解业务、分析证据、生成候选方案。
- Policy Engine 判断当前操作是否获准。
- Harness 负责执行流程与审批。
- ERP Adapter 将获准的操作转换为 ERP 接口调用，并确认结果。

因此，本篇设计的核心目标是建立两个相互配合的子系统：

## Ontology Factory

企业制度、数据字典、业务文档 → 候选本体 → 验证、测试、审核、发布

负责本体的生产与治理

不可变版本 · 依赖锁定 · 兼容性检查

## Ontology Runtime

加载已发布本体 → 语义查询 → 约束校验 → 关系解析 → 证据返回

负责本体的运行与消费

### Harness + Authorization + ERP Port

本体解释业务语义，但业务操作仍需独立授权、审批和执行验证。

本篇延续前四篇确定的技术路线：

- Go 应用层与 React/TypeScript 管理前端。
- PostgreSQL 保存平台元数据、版本索引、任务和权限数据。
- OWL/RDF/SHACL 引擎通过适配接口接入。
- 对象存储保存本体制品、源文档、验证报告和证据文件。
- Docker Compose 支持私有化 MVP 部署。
- 先使用 Mock ERP，不依赖真实 ERP 才能验证本体系统。

本篇还需要明确一个重要边界：本体能够表达业务语义与约束，但不自动成为授权系统，也不自动证明现实世界中的事实。

例如，本体可以表达“金额超过部门审批限额的报销单需要更高级别审批”，但本体本身不能证明某张报销单的金额真实，也不能单独决定当前用户是否有权审批它。

## 2. 总体架构与模块划分

### 2.1 逻辑架构

```
                     企业业务知识来源
        ┌──────────────┼──────────────┐
        ▼              ▼              ▼
   企业制度文档      ERP 数据字典     业务规则与术语
        └──────────────┼──────────────┘
                       ▼
                Ontology Factory
                       │
          ┌────────────┼────────────┐
          ▼            ▼            ▼
       文档解析      本体建模      约束生成
          └────────────┼────────────┘
                       ▼
              候选本体与 Policy IR
                       ▼
             语法、语义、约束验证
                       ▼
                 测试与人工审核
                       ▼
               Ontology Registry
                       ▼
                Ontology Runtime
          ┌────────────┼────────────┐
          ▼            ▼            ▼
       语义查询      关系推理      约束验证
          └────────────┼────────────┘
                       ▼
             结构化结果 + 证据引用
                       ▼
                 Harness Runtime
                       ▼
           授权 / 审批 / Tool / ERP Port
```

Factory 和 Runtime 应采用不同的权限与运行环境。

Factory 可以执行文档解析、模型调用、候选本体生成和测试；Runtime 则以读取已发布版本为主，提供稳定的语义服务。运行时不应因为某个 Agent 提交了新的本体文件，就自动加载或发布该文件。

### 2.2 核心模块

| 模块                      | 核心职责                   |
| ----------------------- | ---------------------- |
| Ontology Source Manager | 管理制度、数据字典、术语和来源版本      |
| Document Ingestion      | 文档解析、切分、结构提取与来源定位      |
| Ontology Generator      | 根据受控输入生成候选本体           |
| Policy IR Compiler      | 将制度要求转换为结构化规则中间表示      |
| Ontology Compiler       | 编译、规范化本体制品             |
| Semantic Validator      | 检查 OWL 结构、一致性与类型约束     |
| SHACL Validator         | 验证 RDF 数据图是否符合约束       |
| Rule Validator          | 验证业务规则及规则间冲突           |
| Ontology Test Runner    | 执行正例、反例、回归与兼容性测试       |
| Ontology Registry       | 管理本体身份、版本、摘要与发布状态      |
| Ontology Runtime        | 提供查询、类型检查、关系解析与验证接口    |
| Evidence Resolver       | 将语义结论映射到制度条款、数据来源和验证记录 |
| Dependency Resolver     | 锁定本体导入、模块依赖和兼容版本       |

这些模块在 MVP 阶段可以是 Go 服务内的逻辑组件；RDF/OWL 推理引擎可通过进程或服务接口独立运行，避免把特定引擎绑定到整个 ODAP 业务层。

## 3. OWL、RDF、SHACL 与业务规则的职责划分

本体系统最容易出现的架构错误，是把所有业务逻辑都塞进 OWL，或者认为只要 SHACL 验证通过，就代表业务流程合法。

ODAP 应采用分层语义模型。

### 3.1 RDF：统一表达业务知识

RDF 使用主语、谓语、宾语构成三元组。例如：

```
@prefix ex: <https://example.org/odap/expense#> .
@prefix rdfs: <http://www.w3.org/2000/01/rdf-schema#> .

ex:ExpenseClaim a rdfs:Class .
ex:Employee a rdfs:Class .
ex:Department a rdfs:Class .

ex:submittedBy a rdf:Property .
ex:belongsToDepartment a rdf:Property .
ex:claimAmount a rdf:Property .

ex:claim001
    a ex:ExpenseClaim ;
    ex:submittedBy ex:employee007 ;
    ex:claimAmount "1280.00" .
```

这是简化示例。正式制品需要声明完整前缀、数据类型、命名空间和本体元数据。

RDF 适合表达：

- 企业实体与概念。
- 实体之间的关系。
- 术语定义和分类。
- 来源与证据之间的关联。
- 从 ERP 或其他数据源映射得到的业务事实。

RDF 中存在某个三元组，并不代表它必然是现实真相。ODAP 需要同时管理事实来源、获取时间、数据版本和可信度。

### 3.2 OWL：表达概念、类型与逻辑关系

OWL 适合表达：

- 类与子类关系。
- 对象属性和数据属性。
- 等价类与不相交类。
- 属性域和值域。
- 部分逻辑限制与分类关系。

例如：

```
@prefix ex: <https://example.org/odap/expense#> .
@prefix owl: <http://www.w3.org/2002/07/owl#> .
@prefix rdfs: <http://www.w3.org/2000/01/rdf-schema#> .

ex:Employee a owl:Class .
ex:Manager a owl:Class ;
    rdfs:subClassOf ex:Employee .

ex:ExpenseClaim a owl:Class .

ex:submittedBy
    a owl:ObjectProperty ;
    rdfs:domain ex:ExpenseClaim ;
    rdfs:range ex:Employee .
```

应谨慎理解 OWL 的语义：某些 OWL 约束是开放世界逻辑下的推理约束，不等同于数据库的 `NOT NULL` 或业务校验失败条件。

例如，未发现某个员工的部门关系，不一定意味着员工没有部门；也可能只是当前数据图没有收集到这一事实。

### 3.3 SHACL：验证数据图是否符合约束

SHACL 适合表达可执行的数据形状与约束，例如必需字段、数据类型、基数、取值范围和关系目标。

```
@prefix ex: <https://example.org/odap/expense#> .
@prefix sh: <http://www.w3.org/ns/shacl#> .
@prefix xsd: <http://www.w3.org/2001/XMLSchema#> .

ex:ExpenseClaimShape
    a sh:NodeShape ;
    sh:targetClass ex:ExpenseClaim ;
    sh:property [
        sh:path ex:claimAmount ;
        sh:datatype xsd:decimal ;
        sh:minCount 1 ;
        sh:maxCount 1 ;
        sh:minInclusive 0 ;
    ] .
```

这表示报销单应具有且仅具有一个非负十进制金额值。

需要注意：示例使用了 `ex:claimAmount`，生产环境应为金额单位、币种、精度、舍入规则和来源另行制定明确约定。单纯检查 `xsd:decimal` 并不能保证金额计算正确。

### 3.4 业务规则引擎：处理流程和操作条件

部分规则更适合在业务规则或策略层表达：

- 报销金额超过限额时必须升级审批。
- 某种费用类别需要附件。
- 已付款报销单不能重复提交付款。
- 特定操作只能由指定组织范围内的角色执行。
- 高风险操作必须在执行前重新检查权限。

这些规则不应全部混入 OWL 或 SHACL。

建议将职责固定如下：

| 层次                   | 解决的问题              |
| -------------------- | ------------------ |
| RDF                  | 业务知识和关系如何表示        |
| OWL                  | 概念、类型和逻辑关系是什么      |
| SHACL                | 数据图是否满足结构与约束要求     |
| Business Rule Engine | 业务条件、流程规则与状态转换是否合法 |
| Authorization Engine | 当前主体是否获准执行该操作      |
| Harness              | 什么时候执行、何时审批、如何恢复   |
| ERP Adapter          | 如何安全调用外部系统并确认结果    |

第五篇的核心原则：语义、验证、授权和执行可以协作，但不能相互替代。

## 4. 本体数据模型与版本结构

### 4.1 本体包 Ontology Package

ODAP 不应把一个企业的全部知识塞进单一的大本体。建议采用“核心本体 + 业务域本体 + 企业扩展本体”的分层结构。

ODAP Core Ontology

通用实体、组织、身份引用、时间、来源与证据语义

Finance Ontology

报销、预算、发票、付款、财务科目

HR Ontology

员工、岗位、部门、组织任职

Procurement Ontology

供应商、采购申请、订单、合同

Enterprise Extensions

企业自定义术语、流程和约束

建议每个本体包至少包含：

```
ontology-package/
├── ontology.yaml
├── ontology.ttl
├── shapes.ttl
├── rules/
│   ├── rules.json
│   └── policy-ir.json
├── mappings/
│   └── erp-mappings.json
├── tests/
│   ├── positive-cases.json
│   ├── negative-cases.json
│   └── regression-cases.json
├── evidence/
│   └── source-manifest.json
└── docs/
    └── ontology-overview.md
```

其中，`ontology.ttl` 存放 OWL/RDF 本体；`shapes.ttl` 存放 SHACL 约束；业务规则、ERP 映射、测试和证据分别独立管理。

本体包中的文件引用必须受控，所有制品都应被计算摘要，并纳入发布清单。不能允许运行时根据本体文件中的任意 URL 自动加载外部内容。

### 4.2 Manifest 设计

建议在第四篇统一插件 Manifest 的基础上，为 `OntologyPlugin` 增加专属字段：

```
apiVersion: odap.io/v1
kind: OntologyPlugin

metadata:
  id: com.odap.ontology.expense
  name: Enterprise Expense Ontology
  version: 1.0.0
  publisher: publisher.internal

spec:
  ontology:
    format: turtle
    owlProfile: OWL2RL
    namespaces:
      expense: "https://example.org/odap/expense#"

  artifacts:
    ontology: ontology.ttl
    shapes: shapes.ttl
    policyIR: rules/policy-ir.json
    erpMappings: mappings/erp-mappings.json

  imports:
    - id: com.odap.ontology.core
      version: "1.0.0"

  validation:
    consistencyCheck: true
    shaclValidation: true
    ruleConflictCheck: true
    regressionTests: true

  runtime:
    queryProfile: read-only
    maxQueryDurationMs: 3000
    maxResultCount: 1000

  evidence:
    requireSourceReferences: true
    requireRuleTraceability: true
```

这里使用 `OWL2RL` 作为示例配置，并不意味着所有企业本体都必须采用该配置。实际支持的 OWL Profile、推理规则与限制应由 ODAP 的引擎适配器明确声明。

还应区分：

- 本体版本：语义定义的版本。
- 数据图版本：某一时点的业务事实快照或数据视图版本。
- 规则版本：业务规则与策略定义的版本。
- 映射版本：外部 ERP 字段到本体属性的映射版本。
- 引擎版本：执行验证或推理的引擎及配置版本。

这些版本可以独立演进，但一次运行必须保存实际使用的版本组合，否则后续无法可靠复现某个语义查询结果。

### 4.3 本体版本与兼容性

本体版本不能只靠语义化版本号判断兼容性。需要结合语义差异、查询依赖和数据映射进行验证。

建议将变更分为三类：

| 变更类型  | 典型示例                | 处理建议          |
| ----- | ------------------- | ------------- |
| 向后兼容  | 增加一个可选属性或注释         | 自动测试后可进入常规审核  |
| 潜在破坏性 | 修改属性范围、基数或推理规则      | 必须执行依赖回归与业务审核 |
| 明确破坏性 | 删除属性、重命名关键概念、改变业务含义 | 发布新主版本或执行显式迁移 |

注意，新增 OWL 公理也可能改变既有推理结果。因此，不能简单规定“新增内容一定兼容”。

对于 Agent 依赖的关键概念、Tool 输入输出契约和 Policy 规则，发布流程应生成语义差异报告，列出受影响的依赖项，并对关联 Agent 和业务用例执行回归测试。

## 5. Ontology Registry 的扩展设计

第四篇已定义通用插件 Registry。本节增加本体专属的数据模型。

### 5.1 核心表

建议增加以下逻辑表：

- `ontology.packages`：本体逻辑身份。
- `ontology.versions`：不可变本体版本。
- `ontology.artifacts`：本体、SHACL、规则、映射等制品。
- `ontology.imports`：本体导入关系。
- `ontology.validation_runs`：验证任务。
- `ontology.validation_findings`：验证问题明细。
- `ontology.publications`：发布与启用记录。
- `ontology.source_documents`：制度与来源文件元数据。
- `ontology.evidence_links`：规则、概念与来源证据的映射。
- `ontology.runtime_bindings`：本体版本与运行配置的绑定记录。

如果平台选择统一使用 `registry.plugin_versions` 管理所有插件版本，也可以通过 `plugin_type = 'ontology'` 实现版本身份统一，再由本体扩展表保存额外字段。MVP 不需要重复实现两套通用版本系统。

### 5.2 本体扩展表 SQL

以下示例采用独立 `ontology` Schema，展示本体版本与导入关系的核心结构。

```
CREATE SCHEMA IF NOT EXISTS ontology;

CREATE TABLE ontology.packages (
    id              UUID PRIMARY KEY,
    ontology_id     TEXT NOT NULL UNIQUE,
    tenant_id       UUID NULL,
    display_name    TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    created_by      UUID NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE ontology.versions (
    id                  UUID PRIMARY KEY,
    package_id          UUID NOT NULL
        REFERENCES ontology.packages(id),
    version             TEXT NOT NULL,
    plugin_version_id   UUID NULL
        REFERENCES registry.plugin_versions(id),
    ontology_digest     TEXT NOT NULL,
    shapes_digest       TEXT NOT NULL,
    policy_ir_digest    TEXT NULL,
    mappings_digest     TEXT NULL,
    semantic_profile    TEXT NOT NULL,
    lifecycle_state     TEXT NOT NULL CHECK (
        lifecycle_state IN (
            'draft',
            'validating',
            'staged',
            'approved',
            'published',
            'deprecated',
            'revoked',
            'rejected'
        )
    ),
    created_by          UUID NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at        TIMESTAMPTZ NULL,

    UNIQUE (package_id, version)
);

CREATE TABLE ontology.imports (
    id                      UUID PRIMARY KEY,
    ontology_version_id     UUID NOT NULL
        REFERENCES ontology.versions(id),
    imported_package_id     UUID NOT NULL
        REFERENCES ontology.packages(id),
    imported_version_id     UUID NOT NULL
        REFERENCES ontology.versions(id),
    import_kind             TEXT NOT NULL CHECK (
        import_kind IN ('required', 'optional')
    ),

    UNIQUE (
        ontology_version_id,
        imported_package_id,
        imported_version_id
    )
);
```

在这份示例中，`ontology_digest` 等字段应由服务端计算，不能直接接受客户端提供的摘要作为事实。正式实现还需增加唯一约束、租户策略、发布状态保护、审计关联与并发控制。

`ontology.imports` 保存解析后的具体版本，而不是只存 `^1.0.0` 这样的范围。范围约束可留在源 Manifest 中，发布时再解析为锁定的版本关系。

#### 5.2.1 生命周期状态转换表（8 态）

`lifecycle_state` 八态的合法转换如下，未列出的转换一律非法，由条件更新在数据库层拒绝：

| 从 \ 到 | draft | validating | staged | approved | published | deprecated | revoked | rejected |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| draft | - | ✓ | - | - | - | - | - | ✓ |
| validating | ✓ | - | ✓ | - | - | - | - | ✓ |
| staged | - | ✓ | - | ✓ | - | - | - | ✓ |
| approved | - | - | - | - | ✓ | - | - | ✓ |
| published | - | - | - | - | - | ✓ | ✓ | - |
| deprecated | - | - | - | - | - | - | - | - |
| revoked | - | ✓（条件恢复） | - | - | - | - | - | - |
| rejected | - | - | - | - | - | - | - | - |

关键规则：

- `rejected` 是吸收态：审核拒绝的版本**不可恢复**为 `published`，只能修订内容后创建新版本；禁止用状态回滚"洗白"被拒版本。
- `revoked` 的条件恢复路径：因安全漏洞撤销的版本必须新建 `draft` 修复并走完整验证流程，**不允许**原地恢复；因依赖失效等非安全原因撤销的版本，经安全负责人与本体管理员双签后可按 `revoked → validating` 回退重验，并记录恢复授权单号。
- `published → revoked` 只允许单向；`published → deprecated` 表示自然演进退役，不阻塞存量运行绑定（§11.3）。
- `draft`/`validating` 阶段的否决不进入 `rejected` 的治理统计，仅作开发中否决记录。

条件更新示例（防止并发与非法转换，语义对齐 §11.2"发布操作应使用条件状态迁移"）：

```
-- 发布：仅 approved 状态可执行
UPDATE ontology.versions
SET lifecycle_state = 'published',
    published_at = now()
WHERE id = $1
  AND lifecycle_state = 'approved';

-- 撤销：仅 published 状态可执行
UPDATE ontology.versions
SET lifecycle_state = 'revoked'
WHERE id = $1
  AND lifecycle_state = 'published';
```

服务端执行条件更新后必须检查影响行数：为 0 时返回 `CONFLICT`（`contracts/02`），由调用方重新读取当前状态后再决策，不得在应用层先查后改（TOCTOU）。

### 5.3 验证记录与问题明细

```
CREATE TABLE ontology.validation_runs (
    id                  UUID PRIMARY KEY,
    ontology_version_id UUID NOT NULL
        REFERENCES ontology.versions(id),
    validator_version   TEXT NOT NULL,
    validation_profile  TEXT NOT NULL,
    status              TEXT NOT NULL CHECK (
        status IN (
            'queued',
            'running',
            'passed',
            'failed',
            'cancelled'
        )
    ),
    summary_json        JSONB NOT NULL DEFAULT '{}',
    report_digest       TEXT NULL,
    started_at          TIMESTAMPTZ NULL,
    finished_at         TIMESTAMPTZ NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE ontology.validation_findings (
    id                  UUID PRIMARY KEY,
    validation_run_id   UUID NOT NULL
        REFERENCES ontology.validation_runs(id),
    severity            TEXT NOT NULL CHECK (
        severity IN ('info', 'warning', 'error', 'critical')
    ),
    finding_code        TEXT NOT NULL,
    focus_node          TEXT NULL,
    path                TEXT NULL,
    source_reference    JSONB NOT NULL DEFAULT '{}',
    message             TEXT NOT NULL,
    details_json        JSONB NOT NULL DEFAULT '{}',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_ontology_findings_run_severity
ON ontology.validation_findings (
    validation_run_id,
    severity
);
```

验证报告应与具体的本体摘要、SHACL 摘要、规则版本、验证器版本和验证配置绑定。

否则，若后续升级验证器或调整推理配置，系统将无法分辨“本体变了”还是“验证环境变了”。

## 6. Ontology Factory：从企业制度到候选本体

这是 ODAP 的核心差异化模块。

Ontology Factory 不应该只是“把 PDF 交给大模型，再让大模型输出 Turtle”。这种方式虽然可以生成文件，但难以保证概念完整性、规则可执行性、来源可追溯性和语义稳定性。

建议采用一条具有中间表示、验证和审核环节的流水线。

### 6.1 标准生产流程

### 01

Source Ingestion

接收制度、数据字典、表结构和业务说明

### 02

Document Structuring

提取章节、条款、定义、条件、例外与来源位置

### 03

Policy IR Generation

生成带有来源引用的结构化业务规则

### 04

Ontology Modeling

生成概念、属性、关系、类型与约束候选

### 05

Mapping Generation

建立 ERP 字段、业务对象与本体属性的映射

### 06

Compile & Validate

执行语法、语义、SHACL、规则和映射验证

### 07

Test & Review

运行正反例、回归测试，进入人工审核

### 08

Publish

固定版本、摘要、依赖、证据并发布

每个阶段都应生成独立产物，记录输入摘要、输出摘要、使用的模型或工具版本、错误和证据引用。这样某一阶段失败时，可以从已保存的中间结果恢复，而不是重新执行整条流水线。

#### 6.1.1 阶段状态机与断点续跑

八个阶段各自是一个可独立重跑的状态机，阶段状态取值：

```
queued → running → succeeded
              ├→ failed →（修复后重新入队）queued
              ├→ cancelled
              └→ skipped
```

- 整条流水线状态由各阶段聚合：全部 `succeeded`/`skipped` 才算流水线 `succeeded`；任一阶段 `failed` 且未重跑成功，流水线为 `failed`；用户可 `cancelled` 尚未开始的阶段。
- **单阶段重跑**：只对 `failed`/`cancelled` 阶段重新入队；已 `succeeded` 的阶段默认复用其产物摘要，重跑不改变上游产物摘要时，下游阶段无需重跑（以摘要比对判定）。
- **产物契约**：每次阶段执行必须落库一条记录，包含输入 digest、输出 digest、引擎/模型版本、起止时间与证据引用；下游阶段只能以上游记录的 `output_digest` 为输入，不得直接读取对象存储中的裸文件。

```
CREATE TABLE ontology.factory_pipeline_runs (
    id UUID PRIMARY KEY,
    package_id UUID NOT NULL REFERENCES ontology.packages(id),
    candidate_version_id UUID REFERENCES ontology.versions(id),
    tenant_id UUID NULL,
    status TEXT NOT NULL CHECK (
        status IN ('queued', 'running', 'failed', 'cancelled', 'succeeded')
    ),
    trigger_type TEXT NOT NULL,
    created_by UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ
);

CREATE TABLE ontology.factory_stage_runs (
    id UUID PRIMARY KEY,
    pipeline_run_id UUID NOT NULL
        REFERENCES ontology.factory_pipeline_runs(id),
    stage_no SMALLINT NOT NULL CHECK (stage_no BETWEEN 1 AND 8),
    stage_key TEXT NOT NULL,
    status TEXT NOT NULL CHECK (
        status IN (
            'queued', 'running', 'succeeded',
            'failed', 'cancelled', 'skipped'
        )
    ),
    attempt INTEGER NOT NULL DEFAULT 1,
    input_digest TEXT NOT NULL,
    output_digest TEXT,
    engine_version TEXT NOT NULL,
    error_code TEXT,
    artifact_ref TEXT,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,

    UNIQUE (pipeline_run_id, stage_no, attempt)
);

CREATE INDEX idx_factory_stage_runs_lookup
    ON ontology.factory_stage_runs(pipeline_run_id, stage_no, status);
```

`engine_version` 记录该阶段实际使用的模型、编译器或验证器版本（如 `expense-modeler@2.1.0`、`policy-ir-compiler@1.4.0`）；模型或编译器升级后重跑同一阶段，即使输入 digest 相同也产生新的 stage_run 记录，保证"同一产物由不同引擎版本复现"可被审计区分。失败阶段的 `error_code` 使用 `contracts/02` 已登记的错误码，不得自造。

### 6.2 第一阶段：来源资料治理

支持的来源可以包括：

- PDF、DOCX、Markdown 等企业制度文档。
- Excel 数据字典、字段说明和业务规则表。
- ERP 表结构、API Schema 和枚举字典。
- 已有 OWL/RDF 本体。
- 企业术语表、流程说明和人工审核意见。

每份来源资料至少应记录：

```
source_id
tenant_id
source_type
document_version
content_digest
storage_reference
ingested_at
effective_from
effective_until
confidentiality_level
```

这里的有效起止时间尤其重要。制度文件可能已经过期，或者只在特定期间有效。Ontology Factory 必须保留制度版本及其生效范围，不能只使用最新上传的文件。

另外，来源文件的内容应视为待处理数据，而不是系统指令。企业文档中即使包含“忽略之前的规则”之类文本，也不能改变 Agent 的系统权限、平台安全策略或工具调用边界。

### 6.3 第二阶段：制度条款结构化

以一条假设的企业报销制度为例：

> 差旅住宿费用应提供有效凭证。超过部门授权额度的申请须由更高级别审批人批准。特殊情况需要填写说明。

Factory 不应直接将这段文字压缩成一句自然语言摘要，而应提取为多个结构化要素：

- 适用业务对象：住宿费用报销。
- 前置条件：属于差旅住宿费用。
- 必需证据：有效凭证。
- 条件分支：申请金额超过部门授权额度。
- 处理要求：进入更高级别审批。
- 例外机制：需要填写特殊情况说明。
- 证据来源：原始制度文件、版本、条款位置。
- 未确定事项：何谓有效凭证、授权额度从哪里读取、谁属于更高级别审批人。

其中，“未确定事项”不能由模型自行猜测后悄悄写入正式规则，而应形成待确认问题，交给企业管理员或业务负责人处理。

### 6.4 第三阶段：Policy IR 中间表示

建议在生成 OWL/RDF 和 SHACL 之前，先将制度转化为受控的 Policy IR（Policy Intermediate Representation）。

Policy IR 的作用是隔离自然语言理解与正式语义制品，便于审核、测试、差异比较和规则编译。

示例：

```
{
  "ir_version": "1.0",
  "rule_id": "expense.lodging.receipt.required",
  "domain": "finance.expense",
  "rule_type": "obligation",
  "subject": {
    "concept": "ExpenseClaim"
  },
  "condition": {
    "operator": "equals",
    "left": {
      "property": "expenseCategory"
    },
    "right": {
      "constant": "lodging"
    }
  },
  "requirement": {
    "operator": "exists",
    "property": "receiptEvidence"
  },
  "source_refs": [
    {
      "source_id": "policy-expense-001",
      "document_version": "3",
      "locator": "section-4.2"
    }
  ],
  "status": "needs_review"
}
```

该 JSON 是示例协议，不是完整的 JSON Schema。正式实现需要定义运算符白名单、字段类型、规则版本、空值语义和编译规则。

尤其要注意：规则中的 `source_refs` 只证明这条规则引用了某份来源材料，不代表该规则已经被来源材料准确支持。Factory 还需要记录原文片段、提取结果与审核状态，以便人工验证。

### 6.5 第四阶段：本体模型生成

在 Policy IR 的基础上，Factory 才生成候选本体：

- 从业务概念生成 OWL 类。
- 从业务关联生成对象属性。
- 从数据字段生成数据属性。
- 从数据字典生成枚举、单位和类型信息。
- 从结构性要求生成 SHACL 约束。
- 从流程条件生成业务规则或策略候选。
- 从 ERP Schema 生成映射草案。

模型输出必须通过编译器、验证器和测试器，不应直接写入正式 Registry 的发布版本。

### 6.6 第五阶段：ERP 映射生成

例如，Mock ERP 中可能有：

```
expense_claims
  id
  employee_id
  department_id
  amount
  currency
  category
  status
```

Ontology Factory 可以生成候选映射：

```
{
  "mapping_id": "expense-claim-mapping-v1",
  "source": {
    "system": "mock-erp",
    "entity": "expense_claims"
  },
  "target": {
    "ontology_class": "ExpenseClaim"
  },
  "fields": [
    {
      "source_field": "amount",
      "target_property": "claimAmount",
      "transform": "decimal"
    },
    {
      "source_field": "currency",
      "target_property": "currencyCode",
      "transform": "currency_code"
    },
    {
      "source_field": "employee_id",
      "target_property": "submittedBy",
      "transform": "entity_reference"
    }
  ]
}
```

这些映射只是候选。正式映射还需要校验主键、空值、币种、精度、实体引用、字段权限和 ERP 字段语义。

`employee_id` 并不一定等同于提交人：它可能表示员工本人，也可能表示代报销人。因此，映射不能只依据字段名称推断语义。

## 7. Policy IR 的规则类型与编译体系

### 7.1 规则分类

Policy IR 不宜只使用一个通用的 `condition → action` 结构。不同规则具有不同的执行语义，应明确区分。

| 规则类型            | 含义            | 典型处理位置                   |
| --------------- | ------------- | ------------------------ |
| `definition`    | 概念定义与分类       | OWL / RDF                |
| `constraint`    | 数据结构和取值约束     | SHACL                    |
| `obligation`    | 必须满足的业务要求     | SHACL 或业务规则引擎            |
| `decision`      | 在条件下选择业务处理结果  | 业务规则引擎                   |
| `approval`      | 是否需要人工审批及审批路径 | Workflow / Policy Engine |
| `authorization` | 谁可以执行什么操作     | Authorization Engine     |
| `mapping`       | 外部数据到本体的转换    | Mapping Runtime          |
| `inference`     | 可从现有事实推导的关系   | OWL 推理或受控规则引擎            |

Factory 应根据规则类型选择对应的编译目标。对于不能安全地自动转换的规则，应标记为 `needs_review` 或 `unsupported`，不能为了完成发布而自动降级为普通文本。

### 7.2 规则运算符白名单

为了避免生成任意代码执行，Policy IR 只应允许有限的运算符集合，例如：

```
逻辑：
  AND, OR, NOT

比较：
  EQ, NE, GT, GTE, LT, LTE

集合：
  IN, NOT_IN, CONTAINS

存在性：
  EXISTS, NOT_EXISTS

数值：
  ADD, SUBTRACT, MULTIPLY, DIVIDE

时间：
  BEFORE, AFTER, WITHIN_INTERVAL

业务状态：
  STATE_IS, TRANSITION_ALLOWED
```

这些运算符只是第一版集合。涉及数值、时间和业务状态的运算按以下口径执行（V1.0 锁定）：

- **除零与溢出**：白名单中的 `DIVIDE` 遇到除零、溢出或非有限结果时，**统一映射为 `UNKNOWN`**——不是 `0`、不是错误终止、也不是跳过该规则；上层按 §8.7 的未知状态语义显式呈现。`ADD`/`SUBTRACT`/`MULTIPLY` 超出精度范围同样映射为 `UNKNOWN`。
- **十进制精度与舍入**：所有金额与数值运算使用定点十进制，精度 `NUMERIC(38, 10)`（38 位有效数字、10 位小数）；运算中间结果不四舍五入，仅在规则输出与证据记录时按 `HALF_UP`（四舍五入）舍入到 10 位小数。禁止二进制浮点参与金额比较（与 §3.3 的金额约定一致）。
- **缺失值与 `null`**：比较任一输入缺失时，比较结果为 `UNKNOWN`，不得默认当作 `false` 或 `0`；`NOT` 作用于 `UNKNOWN` 仍为 `UNKNOWN`。
- **时区与时间区间边界**：时间统一以 `TIMESTAMPTZ` 存储与比较，区间为左闭右开 `[from, until)`；跨时区业务周期以租户配置时区为准。
- **比较双方类型兼容性**：发布校验期做静态类型检查，类型不兼容的规则直接拒绝发布，不留到运行期。
- **规则执行的时间与资源限制**：单条规则求值默认不超过 100 ms，整套规则按 §8.1.1 的资源预算执行。

不应允许 IR 中直接嵌入 SQL、Go 代码、Shell 命令或任意脚本。

### 7.3 规则编译结果

每条规则需要记录其源 IR、编译目标、编译器版本和编译结果。

```
PolicyIR
   │
   ├── definition  ──→ OWL/RDF
   ├── constraint  ──→ SHACL
   ├── decision    ──→ Business Rule Runtime
   ├── approval    ──→ Workflow Policy
   ├── authorization ─→ Authorization Policy Candidate
   └── mapping     ──→ Mapping Runtime
```

授权类规则即使由 Factory 生成，也只能作为候选策略。其正式生效必须经过权限管理员审核，并由第三篇定义的授权引擎执行。

例如，“部门经理可以审批本部门报销单”包含至少三个不同问题：

1. 什么是部门经理？
2. 报销单属于哪个部门？
3. 当前登录主体是否具备相应审批权限？

本体可以帮助表达前两个问题中的业务语义，但第三个问题必须结合可信身份、组织关系、委托范围和授权策略进行判断。

## 8. Ontology Runtime 详细设计

Ontology Runtime 是已发布本体的查询与验证执行层。它应提供稳定的业务语义接口，不让每个 Agent 自己加载 Turtle 文件、编写 SPARQL 或执行推理。

### 8.1 Runtime 的核心职责

1. 加载指定版本的本体与依赖。
2. 检查版本摘要和依赖锁定清单。
3. 提供概念、属性和关系查询。
4. 支持受控的语义查询与推理。
5. 执行 SHACL 验证。
6. 调用业务规则运行时。
7. 解析数据来源和证据引用。
8. 返回结构化结果、验证状态与诊断信息。
9. 对查询耗时、结果大小、递归深度和内存使用实施限制。

Runtime 不负责：

- 决定用户是否具有平台权限。
- 自行审批业务操作。
- 直接修改 ERP。
- 自动发布新本体。
- 把缺失事实当作明确的否定事实。
- 把推理结果当作经过独立验证的现实事实。

#### 8.1.1 性能与并发预算

Runtime 必须为推理与查询设置可观测、可配置的资源边界，避免单个租户或单次慢推理拖垮共享进程：

| 维度 | 默认值（可按租户套餐调整） | 说明 |
| --- | --- | --- |
| 单次推理最长时长 | 3000 ms（与 Manifest `runtime.maxQueryDurationMs` 对齐） | 超时即取消，返回 `DEADLINE_EXCEEDED` 类错误（`contracts/02`），不得无限等待 |
| 单租户并发推理上限 | 8 | 超出时进入有界租户队列（默认深度 32），队列满返回 `RESOURCE_EXHAUSTED`，明确拒绝而非伪造成功 |
| 推理器进程内存上限 | 1 GiB | 超限由 Supervisor 重启进程并告警；进行中的查询按 `ERROR` 处理（§8.7），不返回部分结果 |
| 推理器进程 CPU 配额 | 1 core（可突发至 2 core） | 独立进程部署时按 cgroup 配额限制；MVP 单进程内以 worker 池 + 信号量隔离 |
| 查询结果上限 | 1000 行（Manifest `maxResultCount`） | 超出即分页或拒绝，禁止全量导出 |

取消语义：`ctx` 取消（客户端断开、任务取消、超时）必须传播到推理器适配层，由适配层中断底层引擎调用；引擎不支持中断时，至少停止等待并废弃结果、释放进程资源。取消后的查询不得写入缓存，也不得产生"看起来成功"的部分结果。

慢查询隔离：超过 1 秒的查询记为慢查询，记录 `query_digest`、`ontology_digest`、`data_graph_version` 与耗时直方图；同一 `query_digest` 连续慢查询触发告警（第十一篇），必要时按租户降级或限流。推理引擎（OWL Reasoner / SHACL / 业务规则）以独立进程运行，资源边界（内存、CPU、单次最长时长）由进程级配额强制，不与 API 进程共享事件循环。

### 8.2 运行时数据分层

建议将运行时数据分为四层：

| 层次               | 内容            | 示例             |
| ---------------- | ------------- | -------------- |
| Schema Layer     | 已发布本体与类型定义    | `ExpenseClaim` |
| Constraint Layer | SHACL 与业务规则   | 金额必填、附件要求      |
| Instance Layer   | 业务事实及数据图      | 某张报销单金额        |
| Evidence Layer   | 来源、回执、校验和推理解释 | ERP 记录、制度条款    |

这种分层有利于控制版本和权限，也便于排查语义问题。

例如，同一个 `ExpenseClaim` 类型可以保持不变，但某一时刻的报销单实例数据已经更新。此时应更新数据图版本或数据视图，不应为每次业务数据变更创建一个新的本体版本。

Instance Layer 的数据来源在 MVP 有明确边界：**Runtime 不持有全量业务副本**。Instance Query（§8.4）在 MVP 一律经 ERP Port 实时查询，请求携带授权上下文（租户、主体、数据范围），由 ERP Port 返回已按权限过滤的业务数据；Runtime 只在 Schema/Constraint 层持有已发布本体，在 Evidence 层保存查询产生的证据引用。

这与缓存的关系：经 ERP Port 的实时查询结果可以进入短期缓存（TTL 上限与失效规则见 §8.3），但缓存是**派生数据**而非业务副本——`data_graph_version` 推进或授权上下文变化即失效，未命中或过期时回源 ERP Port，不得以本地缓存应答陈旧实例。禁止把 ERP 全量同步进 RDF 图作为 MVP 目标：那会把数据新鲜度、权限过滤和保留策略问题全部引入语义层，与 §8.5"本体层类型约束不等于数据访问权限"直接冲突。

### 8.3 本体加载与缓存

建议采用按需加载和受控缓存：

- 轻量本体可由 Runtime 进程加载。
- 大型本体可由独立语义引擎服务加载。
- 已发布版本按摘要缓存。
- 数据图按租户、数据源和快照版本隔离。
- SHACL 验证结果按输入图摘要、Shape 摘要和验证配置缓存。
- 本体或规则版本发生变化时，不复用不匹配的缓存。

缓存键应包含实际影响结果的全部关键版本，例如：

```
tenant_id
ontology_digest
imports_lock_digest
data_graph_version
shape_digest
rule_bundle_digest
reasoner_version
query_digest
```

对于动态数据源，还必须考虑数据快照或新鲜度。即使本体版本完全不变，ERP 数据更新也可能改变查询结果。

`data_graph_version` 是数据新鲜度的唯一载体，语义如下：

- **由数据源快照/远端版本驱动**：该版本号不由本体版本推导。ERP 侧每次同步或读取产生或携带一个远端版本标识（ERP 数据版本号、同步批次号或快照 ID），Runtime 将其登记为 `data_graph_version`，写入缓存键与 Evidence Bundle（§9.3）。
- **动态源设 TTL 上限**：直接经 ERP Port 实时查询的实例数据（§8.2）不设长期缓存，查询结果缓存 TTL 默认上限 60 秒，且 TTL 命中后仍以 `data_graph_version` 校验，版本不一致即回源；静态/低频源（字典、枚举）TTL 可延长至 24 小时，并按变更事件主动失效。
- **ERP 回执确认后版本推进**：写操作回执经第七篇对账确认成功后，该业务对象关联的 `data_graph_version` 立即推进并广播缓存失效，消除"回执已确认、缓存仍返回旧值"的窗口。与第十二篇的联动：回执事件同时驱动映射侧数据版本推进，两侧以同一 `data_graph_version` 对齐（第十二篇回执事件 → 本篇缓存失效；本篇查询版本 → 第十二篇血缘记录，互相引用）。
- 缓存键中带 `data_graph_version` 的查询（Instance Query、Validation Query），未携带版本号的请求按 TTL 上限处理，不允许无限期复用。

### 8.4 查询接口

建议将运行时接口分为四类：

| 接口类型             | 作用                |
| ---------------- | ----------------- |
| Schema Query     | 查询类、属性、关系及类型定义    |
| Instance Query   | 查询符合条件的业务对象       |
| Relation Query   | 解析实体之间的关联路径       |
| Validation Query | 检查数据图、对象或规则是否满足约束 |

例如：

```
POST /api/v1/ontology/query
```

请求：

```
{
  "ontology_id": "com.odap.ontology.expense",
  "version": "1.0.0",
  "operation": "instance.search",
  "class": "ExpenseClaim",
  "filters": [
    {
      "property": "claimAmount",
      "operator": "gte",
      "value": 1000
    }
  ],
  "limit": 100
}
```

这里使用的是 ODAP 自己的结构化查询协议，而不是直接接受任意 SPARQL。Runtime 将经过校验的请求转换为底层查询。

第一版应支持有限的过滤、排序、分页和关系遍历。若未来开放 SPARQL，应采用受限查询配置，限制可访问图、允许操作、执行时间和结果规模。

### 8.5 结构化查询的安全要求

- 不接受任意图名称或任意外部文件路径。
- 限制查询深度和最大结果数。
- 防止通过关系遍历读取未授权业务对象。
- 查询结果需要按租户和对象权限过滤。
- 过滤条件应进行类型检查。
- 对敏感属性实行字段级访问控制。
- 查询失败时返回明确的错误状态，不自动返回未经验证的部分结果。

这里有一个关键点：本体层的类型约束不等于数据访问权限。

即使某个用户有权查询 `ExpenseClaim` 类型，也不代表他能读取企业内所有报销单。Runtime 必须将查询范围与可信的授权上下文结合，或者通过受控的数据访问接口获取已过滤的数据。

### 8.6 验证接口

建议提供：

```
POST /api/v1/ontology/validate
```

输入可以包括：

- 本体 ID 与版本。
- 待验证数据图引用。
- 验证配置。
- 需要执行的验证类型。
- 调用关联 ID。

返回内容应明确区分：

```
{
  "status": "completed",
  "ontology_version": "1.0.0",
  "checks": {
    "syntax": "passed",
    "owl_consistency": "passed",
    "shacl": "failed",
    "business_rules": "not_run"
  },
  "summary": {
    "violations": 1,
    "warnings": 2
  },
  "report_ref": "evidence://validation/report-example"
}
```

此处 `report_ref` 是示例引用。正式接口应使用平台定义的证据 ID 或受控引用格式，不能直接接受任意 URL。

尤其不能只返回一个 `valid: false`。调用方需要知道失败的是语法、一致性、SHACL 还是业务规则，以及具体违反了哪条约束。

### 8.7 验证状态与未知状态

建议统一使用明确的状态：

- `PASS`：所请求的验证均通过。
- `FAIL`：至少有一项确定违反约束。
- `INCOMPLETE`：部分检查没有完成。
- `UNKNOWN`：证据不足，无法判断。
- `ERROR`：验证引擎或执行环境发生故障。

例如，缺少员工部门信息时，不应直接推断“员工不属于任何部门”。如果某条业务规则依赖部门信息，那么正确结果可能是 `UNKNOWN` 或 `INCOMPLETE`，并说明缺少哪些事实。

这对企业业务非常重要：未知不是通过，也不一定是失败；未知必须显式呈现，并由业务策略决定后续处理方式。

## 9. 语义查询、关系推理与证据链

### 9.1 从实体到关系路径

Ontology Runtime 的价值不仅是查询某个对象，还在于能够解释对象与对象之间的语义关系。

例如：

```
报销单
  ├── submittedBy ──→ 员工
  │                    └── belongsTo ──→ 部门
  ├── chargedTo ─────→ 预算项目
  ├── supportedBy ───→ 发票 / 票据
  └── governedBy ────→ 报销制度条款
```

当 Agent 询问“这张报销单为什么需要升级审批”时，Runtime 不应只返回一个布尔值，而应尽可能提供：

- 当前报销单金额。
- 适用的部门授权额度。
- 相关的部门关系事实。
- 命中的规则及规则版本。
- 对应的制度条款。
- 数据来源及获取时间。
- 仍未解决的条件。

这样，Agent 才能生成可检查的解释，而不是单纯依赖语言模型猜测业务原因。

### 9.2 推理结果与事实结果必须分离

建议为结果增加 `assertion_type` 字段：

```
SOURCE_FACT
DERIVED_FACT
VALIDATION_FINDING
POLICY_DECISION
MODEL_HYPOTHESIS
```

含义分别是：

- `SOURCE_FACT`：从可信业务源直接获取的事实。
- `DERIVED_FACT`：根据已发布规则推导出的结论。
- `VALIDATION_FINDING`：约束检查产生的问题或通过结果。
- `POLICY_DECISION`：授权或业务策略引擎给出的决定。
- `MODEL_HYPOTHESIS`：模型提出但尚未验证的假设。

例如，ERP 返回“报销金额为 1280 元”是来源事实；本体根据组织关系推导出“该员工属于某部门”可能是推导事实；规则引擎返回“需要升级审批”则是策略或业务规则决定。

这些结论不能在存储时混成同一种“事实”，否则下游 Agent 很容易将推测当成确定数据。

### 9.3 Evidence Bundle 设计

建议每次重要的语义查询或验证都可以生成一个 Evidence Bundle：

```
{
  "evidence_bundle_id": "evb-example",
  "tenant_id": "tenant-example",
  "query_digest": "sha256:...",
  "ontology_digest": "sha256:...",
  "data_snapshot_ref": "snapshot-example",
  "rule_bundle_digest": "sha256:...",
  "reasoner_version": "reasoner-example",
  "result": {
    "status": "INCOMPLETE",
    "conclusion": "需要升级审批，但审批人尚未确定"
  },
  "supporting_facts": [],
  "rule_traces": [],
  "source_refs": [],
  "missing_facts": [
    "department_approval_limit"
  ]
}
```

这是结构示例，空数组表示该示例未展开具体事实，并非生产数据。

Evidence Bundle 应包含足以复核结论的关键版本、输入摘要、规则轨迹和来源引用。对于敏感数据，应存储受控引用或经过脱敏的摘要，而不是无差别复制整个 ERP 数据集。

同时需要区分两个概念：

- 可追溯：能够找到结论依据与来源。
- 可证明：依据与推理过程足以在指定语义规则下推出结论。

有证据引用不等于推理必然正确；模型给出置信度也不能代替规则验证或正式证明。

## 10. Ontology Factory 的多智能体设计

Ontology Factory 可以利用 Agent 提升建模效率，但不能把最终语义正确性完全交给单一大模型。

建议划分为六种专业角色。它们可以由同一个模型在不同任务中执行，也可以由不同模型分别执行。

Source Analyst

提取术语、条款、定义与来源位置

Concept Modeler

生成类、属性、关系、类型与概念层级

Constraint Engineer

将可确定的约束转换为 SHACL 与业务规则

ERP Mapper

提出字段映射、类型转换与实体关联

Conflict Analyst

识别矛盾、重复定义、循环依赖和未决条件

Test Generator

生成正例、反例、边界测试与回归用例

### Ontology Review Gate

自动验证 + 业务负责人审核 + 本体管理员发布

### 10.1 Agent 的输入与输出契约

每个角色都必须有明确的输入和输出 Schema。例如，`Concept Modeler` 的输入可以包括：

- 经过权限过滤的来源文档片段。
- 已存在的本体版本。
- 当前业务域的术语表。
- Policy IR。
- 允许使用的概念和属性命名规范。

输出应是候选概念定义和来源引用，而不是直接执行数据库写入。

`Conflict Analyst` 的输出应包含：

```
finding_code
severity
affected_concepts
conflicting_rules
source_refs
explanation
suggested_resolution
requires_human_decision
```

如果两份制度对于同一事项给出了互相矛盾的规定，系统应呈现冲突与生效时间，而不是自动选择模型认为更合理的一条。

### 10.2 多 Agent 的职责边界

建议采用“生成与审查分离”的流程：

1. Source Analyst 提取条款。
2. Concept Modeler 生成概念模型。
3. Constraint Engineer 编写候选约束。
4. ERP Mapper 生成数据映射。
5. Conflict Analyst 检查矛盾和未决问题。
6. Test Generator 生成测试集。
7. 编译器和验证器执行机器检查。
8. 业务负责人审核业务含义。
9. 本体管理员批准正式发布。

审查 Agent 可以帮助发现问题，但不能把“多个模型都同意”视为形式化正确性的证明。真正的正确性仍依赖确定性验证、测试覆盖、可信来源和必要的人工审核。

### 10.3 置信度和人工审核

建议将置信度作为辅助排序信息，而不是发布门槛的唯一依据。

例如：

| 发现类型        | 自动处理建议        |
| ----------- | ------------- |
| 文档中明确列出的枚举值 | 可生成候选，机器验证后审核 |
| 明确的必填字段     | 可生成 SHACL 候选  |
| 模糊的“合理金额”   | 必须澄清业务阈值      |
| 两份制度的规则冲突   | 必须进入冲突审核      |
| 推测的组织关系     | 不得自动作为可信事实    |
| 授权与审批规则     | 必须由相应责任人审核    |

即使模型对某条规则给出 99% 置信度，也不能因此跳过审批权限审核或发布控制。

## 11. 本体发布与运行时绑定

### 11.1 发布前置条件

一个本体版本进入正式发布状态，建议至少满足：

- Manifest 与 Schema 验证通过。
- 制品摘要与签名校验通过。
- 必需依赖已解析并锁定。
- RDF/OWL 语法检查通过。
- 配置要求的一致性检查通过。
- SHACL 检查达到发布策略要求。
- 业务规则冲突检查完成。
- 必需的正例、反例与回归测试通过。
- ERP 映射检查通过，或明确标记不涉及外部映射。
- 来源证据满足要求。
- 必需的人工审核完成。
- 兼容性报告与版本差异报告已保存。

并非所有本体都必须通过同一组验证。比如，纯术语本体可能没有实例数据，也不一定需要 SHACL 数据验证。平台应通过 `validation_profile` 定义每种本体的必需检查，而不是无条件执行一套固定规则。

### 11.2 发布流程

```
候选版本
   │
   ▼
Schema / Artifact 验证
   │
   ▼
依赖解析与锁定
   │
   ▼
语义与 SHACL 验证
   │
   ▼
规则冲突与回归测试
   │
   ▼
业务审核与技术审核
   │
   ▼
生成不可变发布清单
   │
   ▼
发布并登记摘要
   │
   ▼
租户启用 / Harness 绑定
```

发布操作应使用条件状态迁移和审计记录。发布清单需要绑定本体制品、SHACL 制品、规则包、映射、依赖和验证报告的摘要。

### 11.3 运行时版本固定

一次 Agent 运行开始时，Harness 应固定本次运行所需的本体版本，而不是在每次查询时都使用“最新版本”。

例如：

```
{
  "run_id": "run-example",
  "ontology_bindings": [
    {
      "ontology_id": "com.odap.ontology.core",
      "version": "1.0.0",
      "digest": "sha256:..."
    },
    {
      "ontology_id": "com.odap.ontology.expense",
      "version": "1.2.0",
      "digest": "sha256:..."
    }
  ]
}
```

如果运行期间本体被停用或撤销，Runtime 应依据撤销原因、操作风险和任务状态作出处理：

- 对一般只读查询，可以按企业策略允许现有任务完成，或要求重新绑定版本。
- 对因安全漏洞而撤销的版本，应立即阻止新调用，并按风险策略处理中途运行的任务。
- 对已发起的 ERP 写操作，应进入对账或人工处理，而不是直接假定操作已回滚。

这样既能保证版本可复现，也能响应真实的安全事件。

## 12. Ontology Runtime 的 Go 接口设计

为了避免业务代码与具体 OWL/RDF 引擎耦合，建议通过 Ports & Adapters 方式封装底层实现。

### 12.1 核心接口

```
package ontologyruntime

import (
    "context"
    "encoding/json"
)

type OntologyRef struct {
    OntologyID string `json:"ontology_id"`
    VersionID  string `json:"version_id"`
    Digest     string `json:"digest"`
}

type QueryRequest struct {
    Ontology  OntologyRef     `json:"ontology"`
    Operation string          `json:"operation"`
    Input     json.RawMessage `json:"input"`
    Limit     int             `json:"limit"`
}

type QueryResult struct {
    Status      string          `json:"status"`
    Data        json.RawMessage `json:"data,omitempty"`
    EvidenceIDs []string        `json:"evidence_ids,omitempty"`
    Diagnostics []Diagnostic    `json:"diagnostics,omitempty"`
}

type Diagnostic struct {
    Code     string `json:"code"`
    Severity string `json:"severity"`
    Message  string `json:"message"`
}

type QueryEngine interface {
    Query(
        ctx context.Context,
        req QueryRequest,
    ) (*QueryResult, error)
}

type ValidationEngine interface {
    Validate(
        ctx context.Context,
        req ValidationRequest,
    ) (*ValidationResult, error)
}
```

`QueryEngine` 是平台应用层接口，不意味着客户端可以自由选择任意本体版本。调用前仍需由 Runtime 根据 Registry 记录确认版本存在、已发布、摘要正确且允许使用。

### 12.2 底层引擎适配器

建议通过独立适配器封装不同的语义引擎：

```
Ontology Runtime
       │
       ├── RDF Store Adapter
       │      └── RDF 数据存储
       │
       ├── OWL Reasoner Adapter
       │      └── 一致性与推理
       │
       ├── SHACL Adapter
       │      └── 数据图约束验证
       │
       └── Business Rule Adapter
              └── 业务规则执行
```

MVP 不必立即部署四个独立服务。可以先选择一个能满足所需 OWL Profile、RDF 查询和 SHACL 验证要求的组合，再通过接口封装差异。

选型时需要实际验证：

- 支持哪些 OWL 语义与推理配置。
- 是否支持所需的 SHACL 约束。
- 对大规模 RDF 图的内存和查询表现。
- 增量更新和版本隔离能力。
- 进程崩溃后的恢复能力。
- 商业许可与私有化部署要求。

不要只根据引擎名称判断其功能覆盖率。具体版本、配置和扩展都会影响实际语义行为。

## 13. 管理 API 与前端设计

本体管理前端建议分为六个工作区，而不是将所有功能堆在一个超长页面中。

[OntoCanvas – Visual Ontology Editor](https://images.openai.com/static-rsc-4/fA8sZ59yEV9R45QMRMWwprvl2GoSjz02GgsZp3-ubd-BuflGVAXOztJTqUlbZrrsW7qnc4mSA6I0WYnDa8-aLXLNSaVzb6yguIXHBsLJFqwU6sY3-7oCVLnRNfK5uqXAbBtYbVLxsq9pGVY0bPpAYIK148aGfZX8MP19uMglOYA?purpose=inline)

13.1 本体工作台

查看业务域、概念层级、实体关系、属性定义和依赖图。支持从企业核心本体逐层进入财务、HR、采购等业务域。

[DocTruth | Auditable LLM extraction for Java](https://images.openai.com/static-rsc-4/OIikKUTPGMLGB10xa9ALfJBeqAtpbAlwLzTMT4yRtAGd4J04o9iPXBl81KBOhFHNLCeef79P6t6J9LBxoO-DbTJZ6nZ-hPnvu2SzzNK3TYApydQLWusrNbFDnuDKtYahszgBvjS4frg7jbol30j44uwG7H6bh2Ih8T0KbDPEEpQ?purpose=inline)

13.2 制度解析工作台

导入制度文件，查看提取条款、来源定位、待确认问题和 Policy IR，允许业务人员逐条确认。

[Data Observability: Powering Healthcare Data Transparency](https://images.openai.com/static-rsc-4/UNn3vIkrivy-VW0Q5QyWnRcgWdwm8dTqP029wu-2d42RNz-76VkQwm1Mi1xGDGs9451oqfFOM7nAseuAVc-_25B5un7eYPzLrdKg3gBHMKLJjwkBmOw9PBXQbzM5hkq2kawhZvJetHa630CPPXcl-tVSpE_JOvCtGRgzPl2v1oE?purpose=inline)

13.3 验证与测试中心

展示语法、一致性、SHACL、规则冲突、映射错误、正反例测试和回归结果。

[Deployment Template | Devtron Docs](https://images.openai.com/static-rsc-4/boO1sCXphTBPrcEh_TpGJ4-94cd8c4EvYgL2BNz1cv1VFJNyUrxr1NpJZsSjlfE3wzMWAeBuUSlEW4AHyO73d8j28oshWgaYByTYHSYNTXBVc0Bbyiqf5zcnyono4GNJX_4RzNMlSLR5JwZsR83ZRXbFcz9Ys91FTcjB05N0zgA?purpose=inline)

13.4 版本与发布中心

比较版本差异、查看依赖锁定清单、审核记录、签名摘要和发布状态。

[超越预定义模式：TRACE-KG，从复杂文档构建上下文丰富的知识图谱 | alphaXiv](https://images.openai.com/static-rsc-4/kKIVyexUIO_J7MVhvwCmGxAjk6OjFvy0Iz66hrT-5ZbmwhBYiO7jPTNDtrsDNCJXq3PMAgchear_CYL28F1bDiIcWUgqsK59nSpYVxPERWR5mx6yuM8a9Oh1Npe7Vli2I1UvbyeujcUQY4FXU0cn0t6a44Bd3PgHCXl4bPM8PfQ?purpose=inline)

13.5 语义查询与证据中心

执行受控查询，查看对象关系、命中规则、来源条款、数据版本和未决条件。

[Role-Based Access Control for Payroll | Secure HR Data | LeiPay](https://images.openai.com/static-rsc-4/YUGs_aZTjgw6Le_gJ-Zei0vpXYHErR-Bzp6G9LukFhEMBHCOD7r9Kee_vtvXoD0qLwBddf55INcLoZemA7ivfH8BrvXoFj7Azo6sQpXmgpF6beyYmnnekapio3vIStUi3C48-AQ5H5pB-fimBuVvc6ksVKvhhwcyjnsIJ2mKWbg?purpose=inline)

13.6 治理与权限中心

配置本体管理员、业务审核人、发布审批人和租户可见范围，查看所有治理操作审计。

上面图片用于界面风格参考，实际产品应以 ODAP 的业务模型、交互流程和设计规范为准。

### 13.7 API 建议

| 方法     | API                                           | 用途       |
| ------ | --------------------------------------------- | -------- |
| `POST` | `/api/v1/ontology/packages`                   | 创建本体包    |
| `POST` | `/api/v1/ontology/packages/{id}/versions`     | 创建候选版本   |
| `POST` | `/api/v1/ontology/versions/{id}/validate`     | 执行本体验证   |
| `GET`  | `/api/v1/ontology/versions/{id}/diff`         | 查看版本语义差异 |
| `GET`  | `/api/v1/ontology/versions/{id}/dependencies` | 查看依赖关系   |
| `POST` | `/api/v1/ontology/versions/{id}/reviews`      | 提交审核意见   |
| `POST` | `/api/v1/ontology/versions/{id}/publish`      | 发布已审核版本  |
| `POST` | `/api/v1/ontology/query`                      | 执行语义查询   |
| `POST` | `/api/v1/ontology/validate`                   | 验证业务数据图  |
| `GET`  | `/api/v1/ontology/evidence/{id}`              | 查询证据包    |

所有 API 都必须经过身份认证、租户隔离、资源级权限检查和审计。耗时较长的本体生成、语义验证和回归测试应采用异步任务机制，返回任务 ID，再通过查询接口获取进度与结果。

## 14. 安全与可靠性设计

本体不仅是知识数据，也是会影响业务决策的语义资产。错误本体可能使 Agent 系统性地误解企业制度，因此需要专门的安全治理。

### 14.1 核心威胁与防护

| 威胁             | 防护措施                   |
| -------------- | ---------------------- |
| 恶意文档诱导模型越权     | 来源内容视为不可信数据，工具权限独立控制   |
| 本体导入恶意外部资源     | 禁止任意网络导入，依赖使用锁定清单      |
| 恶意或失控推理        | 限制推理时间、内存、递归和结果规模      |
| 本体语义被意外改变      | 不可变版本、摘要校验、语义差异和回归测试   |
| 错误制度转化为正式规则    | 来源追溯、未决项标记、人工审核        |
| 跨租户语义数据泄露      | 租户隔离、对象级授权、查询结果过滤      |
| Agent 直接修改正式本体 | 只能生成候选版本，发布须经过受控流程     |
| 验证器升级导致结果变化    | 记录验证器版本、配置和输入摘要        |
| 运行期间本体被撤销      | 按风险策略停止新调用并处理在途任务      |
| 证据引用被伪造        | 使用平台生成的证据 ID、摘要校验和访问控制 |

### 14.2 故障处理

如果 RDF 存储不可用，Runtime 不应把查询失败转换成空结果，因为空结果可能被 Agent 误认为“没有相关业务对象”。

如果 SHACL 验证器超时，应返回 `INCOMPLETE` 或明确的执行错误，而不是默认通过。

如果业务规则依赖的组织数据缺失，应返回缺失条件和待补充信息。

如果本体生成任务失败，应保留已完成的中间产物、输入摘要和错误诊断，支持从失败阶段恢复。

对于任何影响高风险业务操作的语义检查，验证服务不可用不能成为绕过检查的理由。

## 15. 端到端 MVP：用 Mock ERP 验证完整链路

建议以“员工报销”作为第一个业务域。它包含员工、部门、报销单、预算、凭证、制度、审批和 ERP 操作，足以验证本体系统的主要能力。

### 15.1 MVP 数据模型

先定义最小对象集合：

```
Employee
Department
ExpenseClaim
ExpenseCategory
Budget
ReceiptEvidence
ApprovalRequest
PolicyClause
```

核心关系：

```
Employee ──belongsTo──> Department

ExpenseClaim ──submittedBy──> Employee
ExpenseClaim ──chargedTo──> Budget
ExpenseClaim ──hasEvidence──> ReceiptEvidence
ExpenseClaim ──governedBy──> PolicyClause

ApprovalRequest ──reviews──> ExpenseClaim
```

### 15.2 MVP 制度规则

先使用一组明确、可测试的规则：

1. 报销单必须存在有效员工引用。
2. 金额必须是非负数值。
3. 币种必须来自允许的币种集合。
4. 需要凭证的费用类别必须具备对应证据。
5. 超过配置限额的申请必须进入升级审批流程。
6. 缺失关键事实时不得自动认定通过。
7. 付款、过账等高风险操作必须经过独立授权与审批。

其中第 4、5 条的具体适用范围与金额限额应由企业业务负责人确认。MVP 可以使用明确标记为测试数据的配置值，但不能把示例阈值当成正式企业制度。

### 15.3 端到端执行流程

### 1

导入企业制度

将报销制度、字段字典和测试数据放入受控来源库

### 2

生成候选本体

生成概念、关系、Policy IR、SHACL 与 ERP 映射

### 3

执行自动验证

检查语法、一致性、约束、冲突和测试用例

### 4

人工审核发布

业务负责人确认制度含义，本体管理员批准发布

### 5

启动报销 Agent

Harness 固定 Agent、本体、工具和规则版本

### 6

查询与验证

Runtime 查询报销单、解析关系并生成证据包

### 7

授权与审批

权限引擎决定允许、拒绝或需要审批

### 8

Mock ERP 执行

通过 ERP Port 模拟提交操作并返回回执

### 9

审计与复核

查看决策、证据、执行结果和版本绑定

### 15.4 MVP 验收用例

| 测试场景               | 预期结果         |
| ------------------ | ------------ |
| 正常报销单，必需信息齐全       | 验证通过         |
| 报销单缺少必填金额          | SHACL 验证失败   |
| 员工部门关系缺失           | 返回未知或不完整状态   |
| 制度中存在相互矛盾的条款       | 发布前出现冲突诊断    |
| Agent 使用未发布本体      | Runtime 拒绝加载 |
| 本体摘要与 Registry 不匹配 | 拒绝执行         |
| 报销金额触发升级条件         | 创建相应审批流程     |
| Agent 尝试直接访问 ERP   | 网络策略或适配器拒绝   |
| Mock ERP 请求超时      | 进入结果对账，不盲目重试 |
| 本体升级改变关键规则         | 必须执行关联回归测试   |

MVP 的成功标准不应是“模型能生成一份本体文件”，而应是：从企业制度输入开始，能够生成候选本体、识别错误、完成审核、正式发布、供 Agent 查询，并将经过验证的语义结果安全地连接到业务执行链路。

## 16. 实施顺序与工程交付

建议分成五个阶段。实际工期取决于团队人数、所选语义引擎和验证深度，这里不预设固定周数。

1. P1：Ontology Package 与 Registry
   - 定义本体 Manifest。
   - 实现本体版本、依赖、制品与发布记录。
   - 统一摘要、签名与不可变版本约束。
2. P2：语义引擎适配
   - 选定 RDF/OWL/SHACL 引擎组合。
   - 实现加载、查询、验证与错误诊断接口。
   - 增加查询配额、租户隔离与版本缓存。
3. P3：Ontology Factory
   - 实现文档导入和来源追踪。
   - 定义 Policy IR Schema。
   - 实现候选概念、规则与映射生成。
   - 建立未决项和冲突审核机制。
4. P4：Harness 与 Agent 集成
   - 为 Agent 提供受控语义查询工具。
   - 固定运行时本体与依赖版本。
   - 将查询结果、验证结果和证据包接入审计。
5. P5：Mock ERP 端到端验证
   - 建立报销业务本体。
   - 实现正反例、冲突和兼容性测试。
   - 验证授权、审批、模拟写入和结果对账。

## 17. 本篇最终架构决策

建议将以下内容固化到 ODAP 的架构规范中。

| 领域     | 最终决策                                 |
| ------ | ------------------------------------ |
| 本体表示   | OWL/RDF                              |
| 数据约束   | SHACL                                |
| 业务流程规则 | 独立业务规则与工作流引擎                         |
| 授权     | 继续使用第三篇定义的 Authorization Engine      |
| 本体生成   | Ontology Factory + Policy IR + 确定性验证 |
| 运行时    | Ontology Runtime，通过统一接口访问语义引擎        |
| 版本管理   | 本体、规则、映射、数据快照和验证器版本分开管理              |
| 来源追踪   | 每条重要规则关联原始来源与审核记录                    |
| 发布     | 不可变版本、依赖锁定、验证和人工审核                   |
| 运行时安全  | 受控查询、资源限制、租户与对象权限过滤                  |
| 证据     | Evidence Bundle 关联来源、规则轨迹与版本摘要       |
| 首个业务域  | 员工报销 + Mock ERP                      |
| 生产治理   | 未知状态显式呈现，语义验证不能绕过授权与审批               |

## 18. 第五篇完成后，ODAP 的核心价值在哪里？

完成前三篇时，ODAP 已经有平台骨架、权限体系和插件治理能力；完成第五篇后，它开始具备真正的企业语义建模、语义运行和制度转化能力。

普通 Agent 平台通常将企业知识放在文档检索、提示词和工具描述中。ODAP 则进一步把企业知识组织为可验证、可版本化、可查询的业务语义资产：

- 企业制度能够转化为结构化候选规则。
- 业务概念和关系能够被统一表示。
- 数据约束可以自动执行。
- 语义查询可以返回证据与未决条件。
- Agent 能够在明确的语义和权限边界内工作。
- 本体变更能够进行版本比较、回归验证和受控发布。

但真正的价值不在于“用了 OWL 或 SHACL”，而在于建立了从制度来源 → 结构化语义 → 确定性验证 → Agent 推理 → 权限与审批 → ERP 执行 → 结果回执的完整治理链路。

下一篇建议继续编写：

《第六篇：Harness 执行引擎与状态机详细设计》

重点设计任务上下文、执行计划、步骤状态机、工具调度、审批暂停与恢复、幂等键、检查点、故障恢复、人工接管，以及如何将本体查询和验证结果转化为受控的业务执行流程。
