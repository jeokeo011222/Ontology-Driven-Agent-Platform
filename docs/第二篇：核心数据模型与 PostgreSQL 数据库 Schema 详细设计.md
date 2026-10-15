# 第二篇：核心数据模型与 PostgreSQL 数据库 Schema 详细设计

版本：V1.0 · 承接第一篇《工程架构与代码骨架》· 适用阶段：MVP 数据层实施

本篇将平台的架构设计落实到可执行的数据库设计，明确每类业务对象如何存储、如何关联、如何进行版本管理，以及如何保证智能体在权限、审批、任务恢复和 ERP 操作方面的可靠性。

这套平台的数据层有一个特殊之处：它既要管理传统企业应用的数据，也要管理智能体运行过程中产生的计划、决策、工具调用和证据。因此，不能简单地把所有内容都放进几张业务表，也不应该把所有数据都塞进 RDF 图数据库。

核心设计原则：关系数据库管理平台状态，OWL/RDF 管理业务语义，SHACL 管理数据约束，文件存储管理大对象，审计与证据记录解释系统做过什么。

## 一、数据库总体架构

### 1.1 数据存储分工

PostgreSQL：平台权威状态库

组织、权限、插件注册表、版本索引、任务状态、审批单、ERP 操作记录、审计索引与证据元数据。

OWL / RDF：本体语义层

概念、类、属性、关系、公理、业务对象语义及其语义查询。由可替换的本体引擎管理。

对象存储：版本文件与大对象

本体包、制度原文、插件制品、模型输出附件、执行证据和导出报告。可使用 S3 兼容存储或受控本地存储。

审计与证据：可追溯记录

谁在何时、依据哪个策略和版本、执行了什么动作、得到什么结果，以及结果由哪些证据支持。

以上是逻辑职责划分，不意味着 MVP 必须部署四套独立服务。例如，本体文件和审计附件初期可以放在同一对象存储中；RDF 引擎也可以作为独立模块接入。关键是明确各类数据的权威来源，避免同一业务状态出现两个互相矛盾的“真相”。

### 1.2 数据库 Schema 分组

建议将表按领域划分为以下逻辑组。初期可以全部位于一个 PostgreSQL 数据库中，使用统一命名规范；只有在隔离、容量或治理需求明确后，才考虑拆分数据库。

| Schema 分组     | 核心内容                |
| ------------- | ------------------- |
| `iam`         | 租户、组织、用户、成员关系、角色、权限 |
| `governance`  | 策略版本、授权绑定、审批规则      |
| `registry`    | 插件、智能体、工具及版本        |
| `ontology`    | 本体包、版本、制品、依赖、验证与发布  |
| `runtime`     | 任务、执行步骤、工具调用、检查点    |
| `workflow`    | 审批单、审批动作、流程状态       |
| `integration` | ERP 操作、幂等键、回执与对账    |
| `audit`       | 审计事件、证据元数据          |
| `system`      | 系统配置、迁移辅助信息、后台任务    |

这里的 Schema 是 PostgreSQL 的命名空间，不是独立数据库。它有助于组织权限、迁移和代码边界，但不能代替应用层授权。

### 1.3 统一数据规范

所有业务表应遵循以下约定：

- 主键采用 UUID；在 PostgreSQL 中可使用 `uuid` 类型。UUIDv7 可作为候选生成方案，但需要统一由应用层或数据库层生成，不能混用不兼容的生成逻辑。
- 需要租户隔离的记录带 `tenant_id`。
- 业务时间统一存储为 `timestamptz`，API 使用 ISO 8601 格式。
- 可变业务记录使用 `created_at`、`updated_at`；不可变版本与审计记录原则上不允许原地修改。
- 状态使用受约束的文本值或枚举，避免任意字符串造成状态漂移。
- 版本化对象同时保存人类可读版本号和不可变版本 ID。
- 大型文件不直接塞进普通业务行；数据库存储文件摘要、存储位置、大小和媒体类型。
- 密钥、访问令牌和密码不得以明文写入普通业务表或日志。

以下 SQL 以 PostgreSQL 为目标，属于建议实施的初始 Schema。为了突出核心关系，部分运维字段和索引可在迁移中继续补充。

## 二、租户、组织与身份数据模型

企业私有化不意味着可以忽略多租户设计。即使第一家客户只使用一个租户，也建议在核心业务表中保留租户边界，为未来集团、多法人和多业务单元扩展做好准备。

### 2.1 实体关系

Tenant · 企业租户

Organization

部门、业务单元、法人

User

员工、管理员、服务身份

通过 Membership 关联用户与组织，并通过角色绑定获得相应权限。

Role → Permission

角色分配权限，授权范围由数据属性和业务策略进一步限制

### 2.2 租户与组织表

```

CREATE SCHEMA IF NOT EXISTS iam;

CREATE TABLE iam.tenants (
    id UUID PRIMARY KEY,
    tenant_key TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'ACTIVE'
        CHECK (status IN ('ACTIVE', 'SUSPENDED', 'ARCHIVED')),
    settings JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE iam.organizations (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES iam.tenants(id),
    parent_id UUID,
    org_code TEXT NOT NULL,
    name TEXT NOT NULL,
    org_type TEXT NOT NULL DEFAULT 'DEPARTMENT',
    status TEXT NOT NULL DEFAULT 'ACTIVE'
        CHECK (status IN ('ACTIVE', 'INACTIVE')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, org_code),
    FOREIGN KEY (tenant_id, parent_id)
        REFERENCES iam.organizations(tenant_id, id)
);

CREATE INDEX idx_organizations_parent
    ON iam.organizations(tenant_id, parent_id);

```

这里使用复合外键确保部门的父节点属于同一租户，避免组织树跨租户关联。

生产实现还应在服务层禁止组织形成环，并明确组织合并、移动和停用时对角色授权及历史记录的处理方式。

### 2.3 用户、成员关系与服务身份

```
CREATE TABLE iam.users (
    id UUID PRIMARY KEY,
    login_name TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    email TEXT,
    identity_provider TEXT,
    external_subject TEXT,
    status TEXT NOT NULL DEFAULT 'ACTIVE'
        CHECK (status IN ('ACTIVE', 'DISABLED', 'LOCKED')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE iam.memberships (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES iam.tenants(id),
    user_id UUID NOT NULL REFERENCES iam.users(id),
    org_id UUID NOT NULL,
    membership_type TEXT NOT NULL DEFAULT 'EMPLOYEE',
    status TEXT NOT NULL DEFAULT 'ACTIVE'
        CHECK (status IN ('ACTIVE', 'INACTIVE')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, user_id, org_id),
    FOREIGN KEY (tenant_id, org_id)
        REFERENCES iam.organizations(tenant_id, id)
);

CREATE INDEX idx_memberships_user
    ON iam.memberships(tenant_id, user_id);
```

需要注意，用户可以属于多个组织，因此不能把 `org_id` 简单地作为 `users` 表的单一字段。

如果后续支持外部顾问、跨法人审批、服务账号和机器人身份，应分别定义身份类型、身份来源及有效期。智能体本身不能冒充某位员工；每次执行都应同时记录请求人、实际执行身份以及授权依据。

对于 OIDC/SSO，建议保存身份提供方和外部主体映射。不要把外部登录令牌直接当作长期业务凭证保存。

## 三、RBAC、ABAC 与授权策略数据模型

平台应同时支持两种授权逻辑：

- RBAC：员工担任什么角色，例如财务审核员、部门经理。
- ABAC：当前操作是否满足属性条件，例如报销金额、所属部门、数据密级、申请人和审批人是否为同一人。

最终的授权决策由授权服务统一计算，而不是在各业务模块里分别写一套权限判断。

### 3.1 角色与权限表

```
CREATE SCHEMA IF NOT EXISTS governance;

CREATE TABLE governance.roles (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES iam.tenants(id),
    role_key TEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    is_system_role BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, role_key)
);

CREATE TABLE governance.permissions (
    id UUID PRIMARY KEY,
    permission_key TEXT NOT NULL UNIQUE,
    resource_type TEXT NOT NULL,
    action TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT ''
);

CREATE TABLE governance.role_permissions (
    tenant_id UUID NOT NULL,
    role_id UUID NOT NULL,
    permission_id UUID NOT NULL
        REFERENCES governance.permissions(id),
    PRIMARY KEY (tenant_id, role_id, permission_id),
    FOREIGN KEY (tenant_id, role_id)
        REFERENCES governance.roles(tenant_id, id)
);

CREATE TABLE governance.role_bindings (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    membership_id UUID NOT NULL,
    role_id UUID NOT NULL,
    scope_type TEXT NOT NULL DEFAULT 'ORG',
    scope_org_id UUID,
    valid_from TIMESTAMPTZ NOT NULL DEFAULT now(),
    valid_until TIMESTAMPTZ,
    granted_by UUID NOT NULL REFERENCES iam.users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    FOREIGN KEY (tenant_id, membership_id)
        REFERENCES iam.memberships(tenant_id, id),
    FOREIGN KEY (tenant_id, role_id)
        REFERENCES governance.roles(tenant_id, id),
    FOREIGN KEY (tenant_id, scope_org_id)
        REFERENCES iam.organizations(tenant_id, id),

    CHECK (valid_until IS NULL OR valid_until > valid_from),
    CHECK (
        (scope_type = 'TENANT' AND scope_org_id IS NULL)
        OR
        (scope_type = 'ORG' AND scope_org_id IS NOT NULL)
    )
);

CREATE INDEX idx_role_bindings_membership
    ON governance.role_bindings(tenant_id, membership_id);
```

在真实项目中，系统级权限目录可以跨租户共享，而业务角色和授权绑定通常需要租户隔离。以上设计采用全局权限定义、租户级角色分配的方案。

### 3.2 策略版本表

授权策略应具备版本管理，避免策略修改后，历史任务无法解释当时为什么获得授权。

```
CREATE TABLE governance.policy_versions (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES iam.tenants(id),
    policy_key TEXT NOT NULL,
    version TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'DRAFT'
        CHECK (status IN (
            'DRAFT', 'VALIDATING', 'PUBLISHED',
            'DISABLED', 'RETIRED'
        )),
    policy_language TEXT NOT NULL,
    policy_document JSONB NOT NULL,
    content_digest TEXT NOT NULL,
    created_by UUID NOT NULL REFERENCES iam.users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, policy_key, version)
);

CREATE INDEX idx_policy_versions_lookup
    ON governance.policy_versions(
        tenant_id, policy_key, status
    );
```

策略正文可以采用受控 JSON 表达式或专门的策略语言。不要将任意 SQL 或未经沙箱限制的脚本作为策略直接执行。

### 3.3 授权决策必须保存什么？

一次重要授权决策至少应记录：

- 请求主体、租户、目标资源和动作。
- 当时使用的角色绑定及策略版本。
- 决策结果：`ALLOW`、`DENY` 或 `REQUIRE_APPROVAL`。
- 关键输入属性的摘要或受控快照。
- 决策时间、请求 ID 和关联的 `run_id`。
- 如果要求审批，关联的审批单及后续决策。

并不是所有低风险读取都需要永久保存完整属性快照。应按风险等级设置留存策略，敏感属性尽可能只保存必要摘要，避免审计系统本身成为隐私泄露渠道。

## 四、插件、智能体与工具注册表

插件注册表描述“系统里有哪些可用能力”，但不直接承载每次任务的运行状态。运行实例放在 `runtime` Schema，定义和版本放在 `registry` Schema。

### 4.1 插件与插件版本

```
CREATE SCHEMA IF NOT EXISTS registry;

CREATE TABLE registry.plugins (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES iam.tenants(id),
    plugin_key TEXT NOT NULL,
    plugin_kind TEXT NOT NULL
        CHECK (plugin_kind IN (
            'AGENT', 'ONTOLOGY', 'TOOL', 'POLICY'
        )),
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    owner_org_id UUID,
    status TEXT NOT NULL DEFAULT 'ACTIVE'
        CHECK (status IN ('ACTIVE', 'DISABLED', 'ARCHIVED')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, plugin_key),
    FOREIGN KEY (tenant_id, owner_org_id)
        REFERENCES iam.organizations(tenant_id, id)
);

CREATE TABLE registry.plugin_versions (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    plugin_id UUID NOT NULL,
    version TEXT NOT NULL,
    manifest JSONB NOT NULL,
    artifact_uri TEXT,
    artifact_digest TEXT NOT NULL,
    signature_ref TEXT,
    status TEXT NOT NULL DEFAULT 'CREATED'
        CHECK (status IN (
            'CREATED', 'VALIDATING', 'TESTING', 'STAGED',
            'APPROVED', 'PUBLISHED', 'DEPRECATED',
            'DISABLED', 'REJECTED'
        )),
    created_by UUID NOT NULL REFERENCES iam.users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, plugin_id, version),
    FOREIGN KEY (tenant_id, plugin_id)
        REFERENCES registry.plugins(tenant_id, id)
);

CREATE INDEX idx_plugin_versions_status
    ON registry.plugin_versions(tenant_id, status);
```

`artifact_digest` 应基于规范化后的插件制品计算。相同版本号不应允许替换成不同内容；若内容改变，应生成新版本。

### 4.2 智能体定义与版本

智能体是可运行的业务能力定义，其版本应固定 Prompt、计划约束、工具依赖、本体依赖和模型配置策略。

```
CREATE TABLE registry.agents (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    plugin_id UUID NOT NULL,
    agent_key TEXT NOT NULL,
    domain TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'ACTIVE',

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, agent_key),
    FOREIGN KEY (tenant_id, plugin_id)
        REFERENCES registry.plugins(tenant_id, id)
);

CREATE TABLE registry.agent_versions (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    agent_id UUID NOT NULL,
    plugin_version_id UUID NOT NULL,
    version TEXT NOT NULL,
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    prompt_template JSONB NOT NULL DEFAULT '{}'::jsonb,
    input_schema JSONB NOT NULL,
    output_schema JSONB NOT NULL,
    dependency_manifest JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'DRAFT'
        CHECK (status IN (
            'DRAFT', 'VALIDATING', 'APPROVED',
            'PUBLISHED', 'DEPRECATED', 'DISABLED'
        )),
    content_digest TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, agent_id, version),
    FOREIGN KEY (tenant_id, agent_id)
        REFERENCES registry.agents(tenant_id, id),
    FOREIGN KEY (tenant_id, plugin_version_id)
        REFERENCES registry.plugin_versions(tenant_id, id)
);
```

这里的 `config` 可以包含模型标识、最大执行步数、超时等非秘密配置。模型 API 密钥应由密钥管理机制提供，不应直接写入 `config`。

对于每次任务，系统需要固定 `agent_version_id`，而不是仅记录 `agent_key` 或“最新版”。否则一次任务运行到一半时，管理员发布新版本，就可能导致任务前后使用不同的执行规则。

### 4.3 工具定义与版本

```
CREATE TABLE registry.tools (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    plugin_id UUID NOT NULL,
    tool_key TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'ACTIVE',

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, tool_key),
    FOREIGN KEY (tenant_id, plugin_id)
        REFERENCES registry.plugins(tenant_id, id)
);

CREATE TABLE registry.tool_versions (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    tool_id UUID NOT NULL,
    plugin_version_id UUID NOT NULL,
    version TEXT NOT NULL,
    input_schema JSONB NOT NULL,
    output_schema JSONB NOT NULL,
    capabilities TEXT[] NOT NULL DEFAULT '{}',
    execution_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    content_digest TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'DRAFT',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, tool_id, version),
    FOREIGN KEY (tenant_id, tool_id)
        REFERENCES registry.tools(tenant_id, id),
    FOREIGN KEY (tenant_id, plugin_version_id)
        REFERENCES registry.plugin_versions(tenant_id, id)
);
```

工具的 `capabilities` 只是工具能力声明。用户权限、智能体权限、业务策略和运行时安全约束必须独立验证，不能只根据工具清单放行。

如果工具版本状态要参与发布控制，应像智能体版本一样使用受约束的状态集合，并建立正式的发布与回滚规则。

## 五、本体注册、版本、依赖与发布模型

这是 ODAP 与普通 Agent 平台的关键区别之一：本体不是一段提示词，而是具有语义结构、约束、依赖、验证结果和发布生命周期的业务知识资产。

### 5.1 本体包与版本

```
CREATE SCHEMA IF NOT EXISTS ontology;

CREATE TABLE ontology.ontologies (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES iam.tenants(id),
    ontology_key TEXT NOT NULL,
    name TEXT NOT NULL,
    domain TEXT NOT NULL,
    granularity TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    owner_org_id UUID,
    status TEXT NOT NULL DEFAULT 'ACTIVE',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, ontology_key),
    FOREIGN KEY (tenant_id, owner_org_id)
        REFERENCES iam.organizations(tenant_id, id)
);

CREATE TABLE ontology.ontology_versions (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    ontology_id UUID NOT NULL,
    version TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'DRAFT'
        CHECK (status IN (
            'DRAFT', 'VALIDATING', 'VALID',
            'REJECTED', 'APPROVED', 'PUBLISHED',
            'DEPRECATED', 'DISABLED'
        )),
    manifest JSONB NOT NULL,
    content_digest TEXT NOT NULL,
    semantic_digest TEXT,
    created_by UUID NOT NULL REFERENCES iam.users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, ontology_id, version),
    FOREIGN KEY (tenant_id, ontology_id)
        REFERENCES ontology.ontologies(tenant_id, id)
);

CREATE INDEX idx_ontology_versions_status
    ON ontology.ontology_versions(
        tenant_id, ontology_id, status
    );
```

`content_digest` 标识整个规范化制品包的内容；`semantic_digest` 可选，用于对规范化后的语义内容进行摘要比较。二者的算法、规范化规则和版本都必须明确，否则不同引擎可能对相同逻辑内容计算出不一致的摘要。

### 5.2 本体制品表

```
CREATE TABLE ontology.ontology_artifacts (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    ontology_version_id UUID NOT NULL,
    artifact_type TEXT NOT NULL
        CHECK (artifact_type IN (
            'OWL', 'RDF', 'SHACL', 'MAPPING',
            'RULES', 'TESTS', 'EVIDENCE', 'DOCUMENT'
        )),
    logical_path TEXT NOT NULL,
    media_type TEXT NOT NULL,
    storage_uri TEXT NOT NULL,
    content_digest TEXT NOT NULL,
    size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, ontology_version_id, logical_path),
    FOREIGN KEY (tenant_id, ontology_version_id)
        REFERENCES ontology.ontology_versions(tenant_id, id)
);
```

每个制品均需要记录内容摘要。存储 URI 只是定位信息，不能当作内容完整性证明。读取制品时，应校验摘要并验证调用者是否有权访问对应租户和版本。

### 5.3 本体依赖关系

```
CREATE TABLE ontology.ontology_dependencies (
    tenant_id UUID NOT NULL,
    source_version_id UUID NOT NULL,
    target_version_id UUID NOT NULL,
    dependency_type TEXT NOT NULL
        CHECK (dependency_type IN (
            'IMPORTS', 'EXTENDS', 'MAPS_TO', 'CONSTRAINS'
        )),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (
        tenant_id, source_version_id,
        target_version_id, dependency_type
    ),

    FOREIGN KEY (tenant_id, source_version_id)
        REFERENCES ontology.ontology_versions(tenant_id, id),
    FOREIGN KEY (tenant_id, target_version_id)
        REFERENCES ontology.ontology_versions(tenant_id, id),

    CHECK (source_version_id <> target_version_id)
);
```

依赖图可能出现循环，因此仅使用外键不足以保证依赖关系合法。发布流程必须检查依赖闭包、循环导入、缺失版本和不兼容约束。对支持循环语义导入的特定本体场景，也应由本体引擎明确判断其是否允许，而不能简单把所有环都视为合法或非法。

### 5.4 本体验证记录

```
CREATE TABLE ontology.validation_runs (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    ontology_version_id UUID NOT NULL,
    validator_version TEXT NOT NULL,
    input_digest TEXT NOT NULL,
    status TEXT NOT NULL
        CHECK (status IN (
            'RUNNING', 'PASSED', 'FAILED', 'ERROR'
        )),
    syntax_valid BOOLEAN,
    owl_consistent BOOLEAN,
    shacl_conformant BOOLEAN,
    business_rules_valid BOOLEAN,
    report_uri TEXT,
    report_digest TEXT,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,

    UNIQUE (tenant_id, id),
    FOREIGN KEY (tenant_id, ontology_version_id)
        REFERENCES ontology.ontology_versions(tenant_id, id)
);
```

验证报告应能追溯到确切的本体版本、输入数据、校验器版本和规则版本。只记录一个 `valid = true` 不足以支持企业审计。

如果某个验证项不适用，应将其标记为“不适用”并说明原因，而不是把它误写成已经通过。实际实现可以使用额外状态字段或独立的验证明细表表达这种区别。

### 5.5 发布记录

建议另建 `ontology.publications` 表，记录每次发布的目标环境、发布人、审批记录、发布版本和回滚来源。这样可以区分“一个版本曾经发布过”与“当前生产环境正在使用哪个版本”。

发布流程应在事务中更新数据库侧的发布指针，同时确保本体制品已经完整落盘并通过摘要验证。若本体引擎的加载不是事务性的，则需要采用预加载、切换指针和失败回滚的方式，不能假设数据库事务可以自动回滚外部引擎状态。

## 六、AgentRun 与执行状态持久化

智能体运行时的数据模型必须回答三个问题：

1. 任务现在处于什么状态？
2. 任务之前执行了哪些步骤，依据哪些版本和证据？
3. Worker 中断后，系统从哪里恢复，如何避免重复副作用？

因此，至少需要 `agent_runs`、`run_steps`、`tool_calls` 和 `checkpoints` 四类表。

### 6.1 任务主表

```
CREATE SCHEMA IF NOT EXISTS runtime;

CREATE TABLE runtime.agent_runs (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    requested_by UUID NOT NULL REFERENCES iam.users(id),
    agent_version_id UUID NOT NULL,
    status TEXT NOT NULL DEFAULT 'CREATED'
        CHECK (status IN (
            'CREATED', 'CONTEXT_BUILDING', 'READY',
            'PLANNING', 'PLAN_VALIDATING', 'RUNNING',
            'WAITING_INPUT', 'WAITING_APPROVAL', 'PAUSED',
            'RECOVERING', 'CANCELLING',
            'REQUIRES_RECONCILIATION', 'MANUAL_REVIEW',
            'COMPLETED', 'FAILED', 'CANCELLED', 'EXPIRED'
        )),
    input_payload JSONB NOT NULL,
    input_digest TEXT NOT NULL,
    output_payload JSONB,
    output_digest TEXT,
    context_manifest JSONB NOT NULL DEFAULT '{}'::jsonb,
    correlation_id UUID NOT NULL,
    attempt_count INTEGER NOT NULL DEFAULT 0
        CHECK (attempt_count >= 0),
    max_attempts INTEGER NOT NULL DEFAULT 3
        CHECK (max_attempts >= 1),
    priority INTEGER NOT NULL DEFAULT 100
        CHECK (priority >= 0),
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deadline_at TIMESTAMPTZ,
    cancel_requested_at TIMESTAMPTZ,
    cancel_convergence_deadline TIMESTAMPTZ,
    last_reconciliation_at TIMESTAMPTZ,
    lease_owner TEXT,
    lease_expires_at TIMESTAMPTZ,
    lease_epoch BIGINT NOT NULL DEFAULT 0
        CHECK (lease_epoch >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    error_code TEXT,
    error_summary TEXT,

    UNIQUE (tenant_id, id),
    FOREIGN KEY (tenant_id, agent_version_id)
        REFERENCES registry.agent_versions(tenant_id, id)
);

CREATE INDEX idx_agent_runs_queue
    ON runtime.agent_runs(status, priority DESC, available_at ASC)
    WHERE status IN ('CREATED', 'READY', 'RECOVERING');

CREATE INDEX idx_agent_runs_cancel_queue
    ON runtime.agent_runs(available_at ASC)
    WHERE status = 'CANCELLING';

CREATE INDEX idx_agent_runs_reconcile_queue
    ON runtime.agent_runs(last_reconciliation_at ASC NULLS FIRST)
    WHERE status = 'REQUIRES_RECONCILIATION';

CREATE INDEX idx_agent_runs_deadline
    ON runtime.agent_runs(deadline_at ASC)
    WHERE finished_at IS NULL;

CREATE INDEX idx_agent_runs_actor
    ON runtime.agent_runs(tenant_id, requested_by, created_at DESC);

CREATE INDEX idx_agent_runs_status
    ON runtime.agent_runs(tenant_id, status, created_at DESC);
```

状态枚举与 `docs/contracts/01-全局状态注册表.md` 严格一致（17 态），由契约测试断言集合相等。要点：

- `CANCELLING` 是统一取消入口：取消请求只置 `cancel_requested_at` 并进入该状态，由取消收敛队列处理在途外部操作（第六篇 §14.2.1）；`cancel_convergence_deadline` 到期未收敛转 `MANUAL_REVIEW`。
- 租约三件套（`lease_owner`/`lease_expires_at`/`lease_epoch`）支撑第六篇 §6.2 的队列分治与 §6.3 的 fencing token；`lease_epoch` 单调递增，旧 Worker 写入因版本不匹配被拒绝。
- `REQUIRES_RECONCILIATION` 由对账队列按 `last_reconciliation_at` 独立扫描，不与执行队列混用。
- 已删除旧枚举值 `QUEUED`（排队语义由 `available_at` + 队列索引表达，见 contracts/01 §1.1 映射表）。

`context_manifest` 保存的是上下文的版本清单和引用信息，而不是无限制地保存整个模型上下文。对于需要追溯的输入快照，应记录摘要、来源和访问控制信息。

正式实现中，`agent_run_dependencies` 表记录运行固定的全部依赖版本，而不是将所有依赖只保存在 JSON 中。关系表便于查询、外键约束、依赖审计和兼容性分析：

```
CREATE TABLE runtime.agent_run_dependencies (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    run_id UUID NOT NULL,
    dep_type TEXT NOT NULL
        CHECK (dep_type IN (
            'AGENT_VERSION', 'ONTOLOGY_VERSION', 'TOOL_VERSION',
            'POLICY_VERSION', 'MODEL_CONFIG', 'ERP_CONNECTION',
            'ERP_MAPPING_VERSION', 'PLAN_TEMPLATE'
        )),
    dep_id UUID NOT NULL,
    dep_version TEXT NOT NULL,
    dep_digest TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, run_id, dep_type, dep_id),
    FOREIGN KEY (tenant_id, run_id)
        REFERENCES runtime.agent_runs(tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX idx_run_deps_run
    ON runtime.agent_run_dependencies(tenant_id, run_id);
```

要点：

1. `context_manifest` JSONB 保留为人可读的摘要，**权威依赖以本表为准**；两者不一致时以本表为准并记审计。
2. `dep_digest` 与第六篇 Execution Manifest、检查点的 `dependency_digest` 同源，保证恢复时可验证"依赖未漂移"。
3. `ERP_CONNECTION` 与 `ERP_MAPPING_VERSION` 两类依赖收编第七篇 §6.5 的映射版本绑定要求，消除第六篇/第七篇版本绑定字段集不一致的问题。
4. 与第八篇 `VersionLock`、第九篇九元组模型版本锁定的关系：本表是数据库层的统一落点，上层锁结构由本表派生（见 ADR-002）。

### 6.2 执行步骤表

```
CREATE TABLE runtime.run_steps (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    run_id UUID NOT NULL,
    step_no INTEGER NOT NULL CHECK (step_no >= 0),
    step_type TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'PENDING'
        CHECK (status IN (
            'PENDING', 'READY', 'RUNNING', 'RETRY_WAIT',
            'WAITING_INPUT', 'WAITING_APPROVAL',
            'SUCCEEDED', 'FAILED', 'SKIPPED', 'CANCELLED',
            'UNKNOWN', 'RECONCILING'
        )),
    input_digest TEXT,
    output_digest TEXT,
    result_payload JSONB,
    evidence_refs JSONB NOT NULL DEFAULT '[]'::jsonb,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    error_code TEXT,
    error_summary TEXT,

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, run_id, step_no),
    FOREIGN KEY (tenant_id, run_id)
        REFERENCES runtime.agent_runs(tenant_id, id)
);

CREATE INDEX idx_run_steps_run
    ON runtime.run_steps(tenant_id, run_id, step_no);
```

步骤结果应该尽量结构化，并保存必要的摘要与证据引用。思维链、内部推理草稿等内容不应被无差别地当作审计记录保存。企业审计需要的是可核查的输入、决策依据、规则结果、工具调用和执行回执，而不是无约束地留存所有内部生成文本。

### 6.3 工具调用表

```
CREATE TABLE runtime.tool_calls (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    run_id UUID NOT NULL,
    step_id UUID,
    tool_version_id UUID NOT NULL,
    call_no INTEGER NOT NULL CHECK (call_no >= 0),
    status TEXT NOT NULL DEFAULT 'PENDING'
        CHECK (status IN (
            'PENDING', 'VALIDATING', 'REJECTED',
            'WAITING_APPROVAL', 'READY', 'DISPATCHED',
            'EXECUTING', 'SUCCEEDED', 'FAILED',
            'UNKNOWN', 'RECONCILING', 'MANUAL_REVIEW'
        )),
    idempotency_key TEXT NOT NULL,
    input_digest TEXT NOT NULL,
    input_payload JSONB,
    output_digest TEXT,
    output_payload JSONB,
    authorization_decision_id UUID,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    error_code TEXT,
    base_error_code TEXT,

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, run_id, call_no),
    UNIQUE (tenant_id, idempotency_key),
    FOREIGN KEY (tenant_id, run_id)
        REFERENCES runtime.agent_runs(tenant_id, id),
    FOREIGN KEY (tenant_id, step_id)
        REFERENCES runtime.run_steps(tenant_id, id),
    FOREIGN KEY (tenant_id, tool_version_id)
        REFERENCES registry.tool_versions(tenant_id, id),
    FOREIGN KEY (tenant_id, authorization_decision_id)
        REFERENCES runtime.authorization_decisions(tenant_id, id)
);

CREATE INDEX idx_tool_calls_run
    ON runtime.tool_calls(tenant_id, run_id, call_no);

CREATE INDEX idx_tool_calls_recovery
    ON runtime.tool_calls(status, started_at)
    WHERE status IN (
        'DISPATCHED', 'EXECUTING', 'UNKNOWN', 'RECONCILING'
    );
```

幂等键应由平台按操作语义稳定生成或在创建操作时持久化，不能在每次重试时随机生成。

此外，数据库唯一约束只保证本地记录不重复，不保证 ERP 远端只执行一次。远端适配器还必须根据 ERP 能力实施去重、查询和对账。

#### 授权决策持久化表

第三篇 §7.4 要求每次授权决策可追溯。以下表是 `tool_calls.authorization_decision_id` 的引用目标，也是审批绑定（总书 §5.5）与审计证据链的共同锚点：

```
CREATE TABLE runtime.authorization_decisions (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    decision_id TEXT NOT NULL,
    subject_type TEXT NOT NULL
        CHECK (subject_type IN ('USER', 'AGENT', 'SERVICE', 'SYSTEM')),
    subject_id UUID NOT NULL,
    on_behalf_of UUID,
    delegation_chain JSONB NOT NULL DEFAULT '[]'::jsonb,
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT,
    environment TEXT NOT NULL
        CHECK (environment IN ('DEVELOPMENT', 'STAGING', 'PRODUCTION')),
    request_snapshot JSONB NOT NULL,
    effect TEXT NOT NULL
        CHECK (effect IN ('ALLOW', 'DENY', 'REQUIRE_APPROVAL')),
    reason_code TEXT NOT NULL,
    obligations JSONB NOT NULL DEFAULT '[]'::jsonb,
    policy_versions JSONB NOT NULL DEFAULT '[]'::jsonb,
    risk_level TEXT,
    facts_digest TEXT NOT NULL,
    evaluated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    latency_ms INTEGER,

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, decision_id)
);

CREATE INDEX idx_authz_decisions_subject
    ON runtime.authorization_decisions(tenant_id, subject_id, evaluated_at DESC);

CREATE INDEX idx_authz_decisions_resource
    ON runtime.authorization_decisions(tenant_id, resource_type, resource_id, evaluated_at DESC);
```

要点：

1. `effect` 三态与第三篇 §7.1 一致；`ERROR`（求值器内部错误）**不得**落库为 `ALLOW` 或 `DENY`，按 contracts/02 记 `AUTHZ_POLICY_EVALUATION_ERROR` 并 fail-closed。
2. `request_snapshot` 保存授权请求的结构化快照（主体、动作、资源、上下文属性），支持事后重放判别；敏感字段按字段级脱敏规则存储。
3. `policy_versions` 记录本次决策实际使用的策略版本集合，支撑"策略变更后重验"（第六篇 §7.5 检查点恢复场景）。
4. `decision_id` 对外暴露（审计查询、审批摘要、错误 `details`），`id` 仅内部使用。
5. 决策记录保留期与审计事件一致（默认 7 年或按租户策略），纳入审计库分区策略（§8.1）。

### 6.4 检查点表

```
CREATE TABLE runtime.checkpoints (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    run_id UUID NOT NULL,
    checkpoint_no BIGINT NOT NULL CHECK (checkpoint_no >= 0),
    resume_step_no INTEGER NOT NULL CHECK (resume_step_no >= 0),
    state_payload JSONB NOT NULL,
    state_digest TEXT NOT NULL,
    dependency_digest TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, run_id, checkpoint_no),
    FOREIGN KEY (tenant_id, run_id)
        REFERENCES runtime.agent_runs(tenant_id, id)
);

CREATE INDEX idx_checkpoints_latest
    ON runtime.checkpoints(tenant_id, run_id, checkpoint_no DESC);
```

检查点保存恢复所必需的最小状态，包括完成的步骤、待执行的步骤、版本清单、证据引用和必要的业务状态。

恢复时必须重新检查权限。即使用户创建任务时具有权限，恢复时也不能假设这些权限仍然有效。

### 6.5 任务状态更新规则

不应允许任何业务代码直接执行：

```
UPDATE runtime.agent_runs
SET status = 'COMPLETED'
WHERE id = $1;
```

而不验证状态迁移是否合法。

建议所有状态转换通过统一的状态服务完成，例如：

```
QUEUED → RUNNING
RUNNING → WAITING_APPROVAL
WAITING_APPROVAL → RUNNING
RUNNING → COMPLETED
RUNNING → FAILED
RUNNING → RECOVERING
RECOVERING → RUNNING
```

状态服务必须检查当前状态、执行版本、租约持有者和业务前置条件，并使用带条件的更新或锁定机制，防止并发 Worker 覆盖彼此的状态。

## 七、审批与 ERP 操作数据模型

这部分直接关系到平台能否安全地从“分析建议”升级到“执行企业业务”。

需要严格区分三种对象：

- `Approval`：谁批准了什么。
- `ERPOperation`：系统试图在外部 ERP 执行什么。
- `ERPReceipt`：外部系统实际返回了什么结果。

这三者不能合并成一张简单的任务表。

### 7.1 审批单

```
CREATE SCHEMA IF NOT EXISTS workflow;

CREATE TABLE workflow.approvals (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES iam.tenants(id),
    run_id UUID,
    requested_by UUID NOT NULL REFERENCES iam.users(id),
    approval_type TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'PENDING'
        CHECK (status IN (
            'PENDING', 'APPROVED', 'REJECTED',
            'CANCELLED', 'EXPIRED', 'INVALIDATED'
        )),
    subject TEXT NOT NULL,
    payload_snapshot JSONB NOT NULL,
    payload_digest TEXT NOT NULL,
    policy_version_refs JSONB NOT NULL DEFAULT '[]'::jsonb,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at TIMESTAMPTZ,

    UNIQUE (tenant_id, id),
    FOREIGN KEY (tenant_id, run_id)
        REFERENCES runtime.agent_runs(tenant_id, id)
);

CREATE INDEX idx_approvals_pending
    ON workflow.approvals(tenant_id, created_at)
    WHERE status = 'PENDING';

CREATE TABLE workflow.approval_actions (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    approval_id UUID NOT NULL,
    actor_id UUID NOT NULL REFERENCES iam.users(id),
    decision TEXT NOT NULL
        CHECK (decision IN ('APPROVE', 'REJECT')),
    comment TEXT NOT NULL DEFAULT '',
    payload_digest TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    FOREIGN KEY (tenant_id, approval_id)
        REFERENCES workflow.approvals(tenant_id, id)
);
```

审批的核心是 `payload_digest`：审批人批准的是某个确定版本的操作内容，而不是笼统地批准“执行这项业务”。

例如，报销单金额、收款账户、业务对象版本、目标操作或关键策略版本发生变化时，必须重新验证审批是否仍然有效；对于影响审批实质的变化，应将原审批标记为失效并重新申请。

审批动作应追加记录，不应通过修改旧动作来覆盖历史决定。还应在授权服务中禁止默认自我审批，并检查审批人的组织范围、审批额度和职责分离规则。

### 7.2 ERP 操作表

```
CREATE SCHEMA IF NOT EXISTS integration;

CREATE TABLE integration.erp_operations (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES iam.tenants(id),
    run_id UUID,
    tool_call_id UUID,
    adapter_key TEXT NOT NULL,
    external_system TEXT NOT NULL,
    object_type TEXT NOT NULL,
    object_id TEXT NOT NULL,
    action TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    expected_version TEXT,
    payload_snapshot JSONB NOT NULL,
    payload_digest TEXT NOT NULL,
    approval_id UUID,
    status TEXT NOT NULL DEFAULT 'PENDING'
        CHECK (status IN (
            'PENDING', 'VALIDATING', 'REJECTED',
            'WAITING_APPROVAL', 'READY', 'DISPATCHED',
            'EXECUTING', 'SUCCEEDED', 'FAILED',
            'UNKNOWN', 'RECONCILING', 'MANUAL_REVIEW'
        )),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    dispatched_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,

    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, external_system, idempotency_key),
    FOREIGN KEY (tenant_id, run_id)
        REFERENCES runtime.agent_runs(tenant_id, id),
    FOREIGN KEY (tenant_id, tool_call_id)
        REFERENCES runtime.tool_calls(tenant_id, id),
    FOREIGN KEY (tenant_id, approval_id)
        REFERENCES workflow.approvals(tenant_id, id)
);

CREATE INDEX idx_erp_operations_reconcile
    ON integration.erp_operations(status, created_at)
    WHERE status IN (
        'DISPATCHED', 'EXECUTING', 'UNKNOWN', 'RECONCILING'
    );
```

`external_system` 应指向经过注册的系统标识，而不是允许模型任意指定的 URL。`adapter_key` 也必须在服务端注册表中解析，并经过授权和能力检查。

### 7.3 ERP 回执表

```
CREATE TABLE integration.erp_receipts (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    operation_id UUID NOT NULL,
    receipt_type TEXT NOT NULL,
    external_operation_id TEXT,
    external_object_version TEXT,
    result_status TEXT NOT NULL,
    response_digest TEXT,
    response_payload JSONB,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    FOREIGN KEY (tenant_id, operation_id)
        REFERENCES integration.erp_operations(tenant_id, id)
);

CREATE INDEX idx_erp_receipts_operation
    ON integration.erp_receipts(tenant_id, operation_id, received_at);
```

允许同一个操作拥有多条回执，例如提交回执、状态查询结果和对账结果。不能简单地给 `operation_id` 设置唯一约束，因为一次操作可能需要多次查询和对账。

### 7.4 正确的事务边界

平台数据库与外部 ERP 通常无法参与同一个分布式事务。因此推荐采用以下模式：

1. 在本地事务中保存操作意图、审批关联、幂等键和待执行状态。
2. 提交事务后，由 Worker 或可靠的分发机制执行外部请求。
3. 收到结果后，开启新的本地事务保存回执和状态变更。
4. 如果调用超时，标记为 `UNKNOWN`，通过查询或对账确认实际状态。
5. 如果无法确定结果，进入 `MANUAL_REVIEW`，不得盲目重试。

如果采用事务 Outbox，应在保存操作意图的同一数据库事务中写入 Outbox 事件，确保业务记录与待发送事件不会一边成功、一边丢失。

这套设计不是保证网络世界中的所有操作都能“恰好一次”，而是通过持久化、幂等、回执和对账，把重复副作用风险控制在可管理范围内。

## 八、审计事件与证据模型

企业级智能体平台不仅要回答“结果是什么”，还要能够回答“为什么这样做、依据什么、由谁批准、实际执行到了哪一步”。

建议将审计事件与业务表分离。业务表保存当前状态，审计表保存关键事件的历史记录。

### 8.1 审计事件表

```
CREATE SCHEMA IF NOT EXISTS audit;

CREATE TABLE audit.audit_events (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES iam.tenants(id),
    event_type TEXT NOT NULL,
    actor_type TEXT NOT NULL,
    actor_id TEXT,
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    action TEXT NOT NULL,
    outcome TEXT NOT NULL,
    request_id TEXT,
    run_id UUID,
    policy_refs JSONB NOT NULL DEFAULT '[]'::jsonb,
    event_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    event_digest TEXT NOT NULL,
    segment_id TEXT NOT NULL,
    seq_no BIGINT NOT NULL,
    prev_digest TEXT,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, segment_id, seq_no)
);

CREATE INDEX idx_audit_events_tenant_time
    ON audit.audit_events(tenant_id, occurred_at DESC);

CREATE INDEX idx_audit_events_resource
    ON audit.audit_events(
        tenant_id, resource_type, resource_id, occurred_at DESC
    );

CREATE INDEX idx_audit_events_run
    ON audit.audit_events(tenant_id, run_id, occurred_at);
```

哈希链支撑列（对齐第十一篇 §9.1 四层完整性机制）：

1. `event_digest = H(canonical(event_payload) || occurred_at || seq_no)`，`prev_digest` 指向同 `segment_id` 内上一条事件的 `event_digest`，形成段内哈希链；`seq_no` 由每租户每段一个计数器行锁分配，禁止空洞回填。
2. `segment_id` 按时间滚动（如每日一段），段边界事件记录 `prev_segment_root`，与第十一篇的独立检查点、受控签名、WORM 归档共同构成"事件摘要 → 段链 → 检查点 → 签名归档"四级结构。
3. `occurred_at`（事件发生，数据库时钟）与 `recorded_at`（落库）分离；合规场景的可信时间戳源（RFC 3161/HSM）按第十一篇 P1 修复项接入，验证层增加"时间可信度"检查。
4. 防篡改落库：应用角色只授 `INSERT` + `SELECT`；`UPDATE`/`DELETE` 由触发器直接拒绝（`raise exception`），高要求场景异步复制到独立受控存储（第十一篇 §9.5）。
5. 分区策略：按 `occurred_at` 做范围分区（如按月），保留期与归档策略见第十一篇；`seq_no` 分配器与分区解耦，避免跨分区计数竞争。

建议记录的事件包括：

- 用户登录、组织切换和权限变更。
- 智能体启动、暂停、恢复、取消和结束。
- 本体版本校验、审批、发布与回滚。
- 工具调用的授权决定、执行结果和错误。
- 审批申请、批准、拒绝、失效。
- ERP 操作提交、回执、对账和人工介入。
- 插件启用、禁用、升级和回滚。
- 关键配置、模型策略和访问策略的变更。

审计记录应采用追加写入方式。应用角色不应拥有任意修改或删除审计记录的权限；对高要求场景，可以进一步将审计事件异步复制到独立的受控存储。

### 8.2 证据记录表

```
CREATE TABLE audit.evidence_records (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES iam.tenants(id),
    run_id UUID,
    step_id UUID,
    evidence_type TEXT NOT NULL,
    source_type TEXT NOT NULL,
    source_ref TEXT,
    content_digest TEXT NOT NULL,
    storage_uri TEXT,
    summary TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, id),
    FOREIGN KEY (tenant_id, run_id)
        REFERENCES runtime.agent_runs(tenant_id, id),
    FOREIGN KEY (tenant_id, step_id)
        REFERENCES runtime.run_steps(tenant_id, id)
);

CREATE INDEX idx_evidence_run
    ON audit.evidence_records(tenant_id, run_id, created_at);
```

证据可以是制度条款、SHACL 验证报告、业务对象快照、工具返回值、审批记录或 ERP 回执。证据摘要应尽量指向原始来源，并保存内容摘要、采集时间和版本信息。

需要区分三个概念：

- 来源证据：系统依据什么信息进行判断。
- 规则证据：哪条规则或约束得到了满足或违反。
- 执行证据：外部操作是否发生，以及结果是什么。

模型生成的解释本身不能自动证明事实成立。证据应尽可能由可核查的数据源、规则执行结果和系统回执支撑。

### 8.3 审计完整性与留存

单纯在数据库中保存一条 `event_digest` 并不能证明记录不可篡改。如果需要更强的防篡改能力，可使用按顺序链接的哈希、定期签名的审计批次以及独立只写存储，并对验证程序进行定期演练。

审计数据也可能包含个人信息、合同内容和财务数据。必须按企业留存政策进行访问控制、脱敏、保留期限管理和合规删除，不能以“审计”为由无限期保存所有敏感数据。

## 九、多租户隔离与 PostgreSQL RLS

平台至少需要两层租户隔离：

1. 应用层：认证后的请求上下文确定租户，业务查询必须显式限定租户。
2. 数据库层：对高价值租户数据启用 Row-Level Security（RLS），降低遗漏过滤条件导致越权的风险。

RLS 不能替代业务权限判断。例如，用户即使属于某个租户，也不代表他有权读取该租户内所有员工的报销单。

### 9.1 RLS 示例

```
ALTER TABLE runtime.agent_runs
    ENABLE ROW LEVEL SECURITY;

ALTER TABLE runtime.agent_runs
    FORCE ROW LEVEL SECURITY;

CREATE POLICY agent_runs_tenant_isolation
ON runtime.agent_runs
USING (
    tenant_id =
    NULLIF(current_setting('app.tenant_id', true), '')::uuid
)
WITH CHECK (
    tenant_id =
    NULLIF(current_setting('app.tenant_id', true), '')::uuid
);
```

请求开始时，应用应在当前数据库事务中设置可信的租户上下文：

```
BEGIN;

SELECT set_config('app.tenant_id', $1, true);

SELECT id, status
FROM runtime.agent_runs
WHERE tenant_id = $1
  AND id = $2;

COMMIT;
```

第三个参数 `true` 表示配置只在当前事务范围内有效。对于连接池，这是非常重要的，可以减少租户上下文残留到下一次借用连接的风险。

### 9.2 RLS 的安全前提

- 应用数据库角色不能是超级用户，也不能拥有绕过 RLS 的属性。
- 业务查询仍需显式加入租户条件，方便审查和利用索引。
- 所有需要隔离的业务表都要逐表审查，不能只保护 `agent_runs`。
- 后台 Worker 的任务领取、系统维护和租户间操作必须使用明确授权的服务身份。
- 跨租户运维接口应采用独立的高权限入口，记录操作者、理由和审计事件。
- 应测试连接池复用、事务回滚、异常退出和后台任务等情况。

特别需要注意：如果应用使用一个共享数据库账号，并允许该账号任意设置租户上下文，那么 RLS 主要是防止应用查询遗漏，而不是防御该账号本身被完全攻陷。若威胁模型包含恶意应用进程，需要进一步设计数据库身份隔离或可信的租户上下文传递机制。

## 十、事务边界、并发控制与状态一致性

本平台会同时存在 API 请求、Worker 执行、审批操作和 ERP 回执。因此，必须明确哪些操作需要在一个本地事务中完成，哪些操作不能放在数据库事务里。

### 10.1 典型事务边界

| 业务场景      | 应在同一事务中完成的操作                     |
| --------- | -------------------------------- |
| 创建智能体任务   | 插入 `agent_runs`、固定依赖版本、插入待执行任务事件 |
| 申请审批      | 创建审批单、冻结操作摘要、更新任务或操作状态           |
| 审批通过      | 锁定审批单、检查摘要与有效期、记录审批动作、更新状态       |
| 发起 ERP 操作 | 保存操作意图、幂等键、Outbox 事件             |
| 保存 ERP 回执 | 插入回执、更新操作状态、记录必要的审计事件            |
| 发布本体      | 校验版本状态、记录发布事件、切换发布指针             |

数据库事务中不应包含耗时的模型调用、远程 ERP 请求或长时间的本体推理。否则容易产生长事务、锁竞争和连接池耗尽。

### 10.2 审批的并发控制

审批处理应使用条件更新或行锁，避免两个审批请求同时修改同一审批单。

概念上可以使用：

```
BEGIN;

SELECT id, status, payload_digest
FROM workflow.approvals
WHERE tenant_id = $1
  AND id = $2
FOR UPDATE;

-- 应用层在此校验审批权限、有效期和摘要。

UPDATE workflow.approvals
SET status = 'APPROVED',
    decided_at = now()
WHERE tenant_id = $1
  AND id = $2
  AND status = 'PENDING';

-- 必须检查更新行数恰好为 1。
-- 同一事务中插入 approval_actions 与审计事件。

COMMIT;
```

正式实现还应防止审批人批准自己提交的敏感操作，并验证审批额度、职责分离和业务对象当前版本。

### 10.3 乐观锁与版本冲突

对于可变业务对象，可使用 `version` 或 `updated_at` 实现乐观锁。例如，只有当前版本与请求的 `expected_version` 相同时才允许修改。

```
UPDATE some_business_object
SET payload = $1,
    version = version + 1,
    updated_at = now()
WHERE tenant_id = $2
  AND id = $3
  AND version = $4;
```

如果影响行数为零，应返回版本冲突，由业务流程决定重新读取、重新验证或请求人工介入，而不是自动覆盖其他人的修改。

对于不可变的本体版本和插件制品，不应通过原地更新实现升级。应创建新版本，并显式切换发布状态。

### 10.4 Outbox 与任务分发

建议将 `task_outbox` 作为可选但推荐的基础设施表。创建任务时，任务记录与待发送事件必须在同一事务中提交。Worker 或分发器领取 Outbox 事件后进行投递，并记录重试状态。

即使引入 Outbox，也仍然需要消费者幂等处理，因为消息可能重复投递。Outbox 解决的是本地数据库状态与消息发布之间的可靠性问题，不是外部副作用的“恰好一次”保证。

## 十一、数据库迁移与初始化方案

### 11.1 迁移顺序

建议严格按照外键依赖顺序实施，避免一次迁移创建过多相互依赖的表。

0001

租户与组织

tenants、organizations、users、memberships

0002

权限治理

roles、permissions、role_bindings、policy_versions

0003

插件注册表

plugins、plugin_versions、agents、agent_versions、tools

0004

本体管理

ontologies、ontology_versions、artifacts、dependencies、validation_runs

0005

执行运行时

agent_runs、run_steps、tool_calls、checkpoints

0006

审批与 ERP

approvals、approval_actions、erp_operations、erp_receipts

0007

审计与证据

audit_events、evidence_records

0008

可靠任务分发

task_outbox、租约、重试与恢复辅助字段

0009

隔离与性能

RLS、补充索引、唯一约束与查询优化

以上是建议的迁移分组，不要求每组只有一个 SQL 文件。实际项目应按依赖和变更风险继续拆分。

### 11.2 初始化种子数据

首次部署需要建立：

- 系统内置权限目录。
- 最小化的系统管理员角色。
- 系统级审计事件类型和状态转换规则。
- 默认的 Mock ERP Adapter 注册信息。
- 基础的本体引擎与模型服务配置模板。
- 必要的系统健康检查与部署配置。

初始化过程必须幂等：重复执行不能创建多个同名管理员、重复权限或重复系统工具。

不要将一个默认管理员密码硬编码进仓库。应通过一次性初始化命令、受控密钥注入或企业身份提供方建立首个管理员。

### 11.3 数据库升级与回滚

数据库迁移与应用版本升级应协同管理。对于生产环境，推荐采用“扩展—迁移—切换—收缩”策略：

1. 新增兼容旧版本的字段或表。
2. 部署可以同时理解新旧结构的应用版本。
3. 执行数据回填和一致性检查。
4. 切换到新字段或新表。
5. 在确认无旧版本依赖后，单独清理过时结构。

不要把“执行 down SQL”视为所有迁移都能安全回滚。删除字段、覆盖数据和不可逆的数据转换需要备份、恢复演练或向前修复方案。

## 十二、索引、分区与容量规划

MVP 不必过早对所有表进行分区，但应从第一天开始关注增长最快的表。

### 12.1 重点表

| 表                          | 主要增长来源   | 优先优化方向             |
| -------------------------- | -------- | ------------------ |
| `runtime.agent_runs`       | 每次智能体任务  | 租户、状态、时间、请求人索引     |
| `runtime.run_steps`        | 每个执行步骤   | `run_id + step_no` |
| `runtime.tool_calls`       | 每次工具调用   | 任务顺序、恢复状态、幂等键      |
| `runtime.checkpoints`      | 每次检查点    | 按任务查最新检查点          |
| `integration.erp_receipts` | 每次提交和对账  | 操作 ID、回执时间         |
| `audit.audit_events`       | 每个关键审计事件 | 租户、时间、资源、任务        |
| `audit.evidence_records`   | 每项执行证据   | 任务、步骤、来源引用         |

### 12.2 JSONB 的使用边界

`JSONB` 适合保存有明确 Schema 的可扩展配置和结构化快照，但不能成为规避关系建模的借口。

适合 JSONB 的内容：

- 插件清单中的扩展配置。
- 智能体输入输出快照。
- 策略文档。
- 结构化审计属性。
- 经过定义的证据元数据。

不宜只放在 JSONB 中的内容：

- 需要外键约束的版本依赖。
- 需要独立查询和授权的组织、用户、审批与 ERP 操作。
- 需要高频过滤和唯一约束的业务状态。
- 需要被多条记录共享且必须保持一致的关键实体。

### 12.3 何时考虑分区？

当执行记录、工具调用和审计数据的实际增长导致索引膨胀、清理缓慢或查询性能下降时，再考虑按时间或租户策略进行分区。

分区之前，应先通过实际数据规模、查询计划、保留期限和备份恢复测试确定收益。特别是带有唯一约束和外键的表，PostgreSQL 的分区设计会影响约束实现，不能简单地把所有大表改为按月分区。

## 十三、数据保留、备份与恢复

本平台保存的不仅是普通业务记录，还包括企业制度、审批、执行证据和外部操作回执。保留策略应区分数据类型。

| 数据类型       | 建议管理方式                   |
| ---------- | ------------------------ |
| 当前组织与权限    | 保留当前有效状态，并保留必要的变更历史      |
| 插件、本体与策略版本 | 发布版本不可变；按依赖关系管理生命周期      |
| 运行步骤与检查点   | 按任务状态和保留策略保存；终态任务可归档     |
| ERP 操作与回执  | 根据审计和业务要求保留，不能在状态未知时直接清理 |
| 审计事件       | 追加写入，按合规要求设置保留期限         |
| 证据与附件      | 通过摘要、访问权限和留存规则管理         |
| 临时模型输出     | 只保存完成任务所需的最小内容           |

备份至少包括 PostgreSQL、对象存储中的制品和证据，以及恢复所需的密钥配置、版本清单和部署配置。备份应加密，并限制访问权限。

恢复演练必须验证：数据库记录中的 `storage_uri` 仍然可用、内容摘要一致、任务依赖版本齐全、审计链条可以核查。仅仅成功恢复 PostgreSQL 并不等于平台已恢复。

另外，恢复数据库备份时，应避免自动重新执行所有处于 `DISPATCHED` 或 `UNKNOWN` 的 ERP 操作。恢复流程必须先进行状态对账，再决定继续、重试或人工介入。

## 十四、数据库层安全检查清单

建议把以下检查纳入 CI 和发布验收，而不是等到生产部署时再补。

### 数据库工程验收清单

0/8

租户隔离

所有租户业务表具有租户归属；复合外键和 RLS 规则经过跨租户测试。

版本不可变

已发布的智能体、本体、策略和工具版本不能被静默覆盖。

审批绑定

审批决定绑定操作摘要、依赖版本和有效期；关键参数变化会使审批失效。

幂等与对账

重复请求受到约束；外部操作未知时进入查询、对账或人工处理。

并发状态控制

状态迁移有明确规则；Worker 租约、条件更新和重复消息处理经过测试。

审计完整性

关键权限决定、审批、发布和外部操作均有可追溯事件。

数据最小化

不无差别保存敏感 Prompt、模型上下文或业务原文；密钥不进入普通表。

恢复能力

备份、迁移、对象存储恢复及 ERP 未知状态对账经过演练。

以上清单可用于项目评审；勾选表示人工确认已完成相应验证，不代表系统自动检测了这些能力。

## 十五、第二篇的最终实施建议

完成本篇后，数据库层的核心实体和约束已经有了明确方向。正式编码时，建议优先实现以下四条完整链路，而不是一次性创建所有表后再开发业务：

1. \*\*身份链路：\*\*租户 → 组织 → 用户 → 成员关系 → 角色绑定 → 授权决策。
2. \*\*本体链路：\*\*本体包 → 不可变版本 → 制品 → 验证记录 → 审批 → 发布指针。
3. \*\*执行链路：\*\*AgentRun → RunStep → ToolCall → Checkpoint → 恢复。
4. \*\*外部操作链路：\*\*授权 → 审批 → ERP 操作意图 → 幂等提交 → 回执 → 对账 → 审计。

每条链路都应有独立的数据库迁移、Repository、领域服务、集成测试和失败恢复测试。

尤其要把握三个底线：

- 本体和智能体版本必须可追溯，历史任务不能因为发布新版本而悄悄改变语义。
- 审批必须绑定具体操作，不能将“批准某项业务”理解为无限制的授权。
- 数据库状态与外部 ERP 状态必须分开管理，超时、崩溃和重复消息不能制造虚假的成功或失败。
