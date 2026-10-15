# 《第四篇：插件协议与 Registry 注册中心详细设计》

ODAP · Ontology-Driven Agent Platform · 技术设计系列

> **Scope 标签（本篇范围声明，对齐评审报告 §5 的 MVP 治理要求）**
> - `[MVP-V1.0]`：统一 Manifest 与五类插件协议（§3）、Registry 核心表与生命周期状态机（§5）、ODAP Plugin Protocol v1（§6）、独立受控进程执行（§8.1）、租户安装/启用/停用（§9.3）、依赖锁定（§7.2）。
> - `[V1.5]`：容器 Runner 受控接入加固（§8.1 两种接入方式落地）、安全扫描与审核流程深化（§15 阶段三）、SHACL/高级能力联动。
> - `[V2]`：WASI 沙箱（§8.1）、私有插件仓库、分布式运行平台、组织级插件治理完整实现。

## 1. 设计目标与总体原则

ODAP 的核心不是简单地把多个 AI Agent 接在一起，而是构建一个由企业本体、权限策略、标准工具和执行治理共同驱动的智能体平台。因此，插件系统必须成为平台的统一扩展机制、能力登记中心和执行安全边界。

本篇设计承接前三篇：

- 第一篇：平台工程架构与代码骨架。
- 第二篇：核心数据模型与 PostgreSQL Schema。
- 第三篇：企业身份认证与权限引擎。
- 第四篇：插件协议与 Registry 注册中心。
- 后续第五篇：Ontology Runtime 与 Ontology Factory。
- 后续第六篇：Harness 执行引擎与状态机。
- 后续第七篇：ERP Port 与 Mock ERP。

本篇将重点解决八个问题：

1. Agent、Ontology、Tool、Policy 如何使用统一插件协议。
2. 插件如何声明身份、版本、依赖、能力与权限需求。
3. 插件如何注册、验证、测试、审批、发布、停用和回滚。
4. 插件运行时如何与 Go 平台服务进行标准化通信。
5. 如何防止恶意插件伪造身份、越权调用工具或绕过 Harness 直接访问 ERP。
6. 如何处理插件版本升级、依赖冲突、兼容性和供应链安全。
7. 如何实现插件隔离、超时、取消、重试与故障恢复。
8. 如何在不接入真实 ERP 的情况下，通过 Mock 插件完成端到端验证。

## 2. 插件系统的总体架构

### 2.1 插件类型

ODAP 第一版定义五类插件，采用统一注册协议，但根据类型启用不同的验证规则和运行方式。

## ODAP Plugin Registry

统一身份、版本、依赖、能力、签名与生命周期管理

Agent Plugin

理解任务、生成计划、组织专业工作流

Ontology Plugin

定义概念、关系、约束与业务语义

Tool Plugin

封装查询、计算、业务操作与外部接口

Policy Plugin

声明授权、审批、风险与业务约束规则

Model Provider Plugin

统一对接大语言模型、嵌入模型及企业私有模型服务

### Harness Runtime

上下文组装 → 权限检查 → 调度 → 执行 → 结果校验 → 审计

### 受控能力出口

Ontology Runtime · Tool Runtime · Model Gateway · ERP Port

这五类插件不是五套互不相干的系统，而是一个统一插件管理框架下的五种能力类型。

关键原则：注册不等于授权，发布不等于启用，启用不等于每次调用都获准。

例如，一个报销 Agent 声明自己需要读取报销单、查询预算、提交报销申请三项能力，并不意味着它注册后就自动获得这些权限。权限引擎必须结合租户、组织、用户委托、Agent 版本、任务上下文以及操作风险作出独立决策。

### 2.2 插件与平台的调用关系

标准调用链如下：

```
用户 / 定时任务 / 外部系统
          │
          ▼
    API Gateway
          │
          ▼
   Identity & Authorization
          │
          ▼
      Harness Runtime
          │
          ├── Agent Plugin
          │      ├── 请求工具调用
          │      ├── 请求本体查询
          │      └── 返回计划或结论
          │
          ├── Ontology Runtime
          ├── Tool Runtime
          ├── Policy Evaluator
          └── Model Gateway
                    │
                    ▼
              ERP Port
                    │
                    ▼
               Mock ERP
```

Agent 不应直接访问 ERP 数据库，不应持有 ERP 管理员凭据，也不应自行决定是否跳过审批。

即使插件具有网络访问能力，也必须通过运行环境限制其网络出口；不能仅依靠 Manifest 中的声明来保证安全。

## 3. 插件身份与 Manifest 协议

### 3.1 插件身份模型

每个插件至少需要以下标识：

| 字段                | 含义          | 示例                       |
| ----------------- | ----------- | ------------------------ |
| `plugin_id`       | 永久稳定的插件标识   | `com.odap.expense-agent` |
| `plugin_type`     | 插件类型        | `agent`                  |
| `version`         | 不可变版本       | `1.0.0`                  |
| `tenant_scope`    | 可见范围        | `platform`、`tenant`      |
| `artifact_digest` | 制品内容摘要      | `sha256:...`             |
| `manifest_digest` | Manifest 摘要 | `sha256:...`             |
| `publisher_id`    | 发布者身份       | `publisher.internal`     |
| `signature_ref`   | 签名记录引用      | `sig_...`                |

建议采用语义化版本号作为版本命名约定，例如 `1.2.3`，但版本字符串本身不能证明制品不可变。

不可变性的真正约束是：

- 已发布版本不能被原地覆盖。
- Manifest、制品摘要和签名必须绑定。
- 安装与运行时都校验实际制品摘要。
- 修改代码或 Manifest 必须生成新版本。
- 同一个版本标识若对应不同摘要，应拒绝发布并产生安全告警。

平台还应区分三种身份：

1. 插件身份：说明运行的是哪个插件版本。
2. 执行主体身份：说明此次操作代表谁执行。
3. 发布者身份：说明是谁构建并发布了这个制品。

三者不能相互替代。插件不能通过在请求体中填写另一个 `user_id`，就取得该用户的权限。

### 3.2 统一 Manifest

Manifest 是插件的静态声明文件，描述插件的身份、能力、依赖、接口、权限需求和运行约束。

下面给出一份建议的 YAML 格式。示例以报销 Agent 为例：

```
apiVersion: odap.io/v1
kind: AgentPlugin

metadata:
  id: com.odap.expense-agent
  name: Expense Assistant
  version: 1.0.0
  description: 企业报销申请辅助智能体
  publisher: publisher.internal
  labels:
    domain: finance
    maturity: experimental

spec:
  runtime:
    protocol: odap-plugin-v1
    kind: process
    entrypoint: /app/plugin
    timeoutSeconds: 60
    maxMemoryMiB: 512
    maxConcurrentInvocations: 8

  interfaces:
    invoke:
      inputSchema: schemas/invoke-input.json
      outputSchema: schemas/invoke-output.json

    health:
      enabled: true

  capabilities:
    - expense.read
    - budget.read
    - expense.draft.create

  requiredServices:
    - ontology.query
    - tool.invoke
    - model.generate

  permissions:
    requested:
      - resource: expense.claim
        actions: [read, create_draft]
      - resource: budget
        actions: [read]
    forbidden:
      - erp.payment.execute
      - erp.accounting.post

  ontologyDependencies:
    - id: com.odap.ontology.expense
      version: "^1.0.0"

  toolDependencies:
    - id: com.odap.tool.expense-query
      version: "^1.0.0"
    - id: com.odap.tool.budget-query
      version: "^1.0.0"

  policy:
    riskLevel: R2
    approvalRequiredFor:
      - expense.submit

  security:
    network:
      mode: deny-by-default
      allowedServices:
        - model-gateway
    secrets:
      mode: platform-injected
    filesystem:
      mode: read-only
```

这份 Manifest 是声明，不是授权凭证。`permissions.requested` 只能作为审核输入；真正生效的授权仍然来自平台策略和授权引擎。

`spec.runtime.kind` 的取值与执行路线一一对应：`process`（独立受控进程，MVP 默认）、`container`（容器 Runner，可选，需受控 dockerd 接入）、`wasi`（V2 评估，V1.0 拒绝实例化）；完整边界见 §8.1。示例中的 `kind: process` 即 MVP 默认执行方式。

此外，`forbidden` 是插件自我声明的安全约束，平台也应有独立的强制禁止策略。恶意插件可以删除自己的 `forbidden` 字段，因此平台不能依赖该字段来保护关键操作。

### 3.3 Manifest 规范要求

建议将 Manifest 的验证拆成六层：

1. 语法层：YAML 是否能解析，是否存在重复键。
2. Schema 层：字段、枚举、格式、必填项是否符合 JSON Schema。
3. 身份层：插件 ID、发布者、版本是否满足唯一性规则。
4. 依赖层：依赖是否存在，版本约束是否可解。
5. 安全层：权限需求、网络出口、资源限制、敏感能力是否符合政策。
6. 兼容层：协议版本、输入输出 Schema、依赖 API 是否与平台兼容。

应拒绝未知的关键字段、危险的路径引用和不受支持的协议版本。对于 Manifest 中引用的 Schema、规则和其他制品，应限定引用范围，防止路径穿越和未授权的外部文件加载。

设计决策：Manifest 负责声明，Registry 负责验证与登记，Authorization 负责授权，Harness 负责编排，PEP 负责执行前强制检查。

## 4. 插件注册中心 Registry 设计

### 4.1 Registry 的职责边界

Registry 不只是一个插件列表页面，而是平台中插件从开发到运行的可信元数据中心。

建议拆成以下逻辑组件：

| 组件                    | 职责                       |
| --------------------- | ------------------------ |
| Manifest Parser       | 解析 Manifest，输出标准内部结构     |
| Schema Validator      | 验证字段、类型、输入输出契约           |
| Dependency Resolver   | 解析插件依赖并生成锁定清单            |
| Artifact Manager      | 保存插件包、校验摘要、管理制品          |
| Signature Verifier    | 校验发布者签名及可信密钥             |
| Security Scanner      | 扫描依赖、容器配置、权限需求与已知风险      |
| Compatibility Checker | 检查平台、协议、本体与工具版本兼容性       |
| Lifecycle Manager     | 控制验证、审批、发布、停用与回滚         |
| Registry API          | 提供查询、注册、审核、发布等接口         |
| Runtime Resolver      | 为 Harness 解析确定的插件版本与执行制品 |

这些组件在 MVP 阶段可以作为 Go 服务中的独立模块实现，不必拆成多个微服务。制品存储与运行时隔离则应具有清晰的独立接口。

### 4.2 插件生命周期

建议使用明确的状态机，而不是在数据库中简单存储一个 `enabled` 布尔值。

DRAFT

开发中，尚未提交

SUBMITTED

已提交，等待验证

VALIDATING

Schema、依赖、安全、兼容性检查

TESTING

契约测试与沙箱测试

STAGED

验证通过，等待审核

APPROVED

审核通过，可进入发布流程

PUBLISHED

版本制品已正式发布

DEPRECATED / DISABLED

停止推荐或禁止新执行，可按策略处理存量任务

典型流转：

```
DRAFT
  → SUBMITTED
  → VALIDATING
  → TESTING
  → STAGED
  → APPROVED
  → PUBLISHED
```

异常分支包括：

- 任一验证失败：进入 `REJECTED`，保留诊断报告。
- 验证期间发现高危问题：进入 `QUARANTINED`。
- 发布后出现安全问题：进入 `DISABLED` 或 `REVOKED`。
- 不再建议使用：进入 `DEPRECATED`。
- 需要恢复旧版本：重新选择已验证的不可变版本发布，不修改历史制品。

这里有一个容易混淆的地方：插件版本状态和插件运行状态必须分开。

例如，`1.0.0` 可以保持 `PUBLISHED`，但被设置为禁止新任务使用；历史审计仍然能够确认它曾经发布。另一个 `1.1.0` 可以正常发布，但尚未被某个租户启用。

建议分别维护：

- 版本生命周期：`DRAFT → PUBLISHED → DEPRECATED / REVOKED`。
- 租户启用状态：`NOT_INSTALLED / ENABLED / DISABLED`。
- 执行实例状态：`STARTING / READY / UNHEALTHY / STOPPED`。

三套状态不能混用。

### 4.3 多租户与共享插件

ODAP 是企业私有化平台，建议支持三种插件可见范围：

- 平台级插件：由平台管理员维护，经过统一审核后可供多个租户安装。
- 租户级插件：企业自行开发或定制，仅在该租户范围内可见。
- 组织级配置：复用已发布的插件版本，但配置特定部门、岗位或业务域的启用策略。

租户安装不应复制一份可随意修改的公共插件制品，而应建立安装记录，固定插件版本与摘要。

这样，平台升级不会意外覆盖企业正在使用的版本，企业也可以明确控制升级时机。

## 5. PostgreSQL 核心数据模型

以下 Schema 是建议新增的 Registry 专属结构，与第二篇的 Agent、Ontology、任务执行和审计表衔接。具体表名可按已有迁移规范统一。

### 5.1 核心表关系

```
plugins
  ├── plugin_versions
  │     ├── plugin_artifacts
  │     ├── plugin_dependencies
  │     ├── plugin_capability_declarations
  │     ├── plugin_validation_runs
  │     └── plugin_signatures
  │
  ├── plugin_installations
  └── plugin_publications

plugin_versions
  └── plugin_reviews
```

核心概念：

- `plugins`：插件逻辑身份。
- `plugin_versions`：不可变的版本定义。
- `plugin_artifacts`：制品及其摘要、存储位置。
- `plugin_dependencies`：声明的依赖。
- `plugin_validation_runs`：验证过程及结果。
- `plugin_reviews`：审核决定。
- `plugin_installations`：租户安装和启用状态。
- `plugin_publications`：发布记录和环境范围。

### 5.2 插件主表

```
CREATE SCHEMA IF NOT EXISTS registry;

CREATE TABLE registry.plugins (
    id              UUID PRIMARY KEY,
    plugin_id       TEXT NOT NULL UNIQUE,
    plugin_type     TEXT NOT NULL CHECK (
        plugin_type IN (
            'agent',
            'ontology',
            'tool',
            'policy',
            'model_provider'
        )
    ),
    visibility      TEXT NOT NULL CHECK (
        visibility IN ('platform', 'tenant')
    ),
    tenant_id       UUID NULL,
    publisher_id    UUID NOT NULL,
    display_name    TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by      UUID NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    CHECK (
        (visibility = 'platform' AND tenant_id IS NULL)
        OR
        (visibility = 'tenant' AND tenant_id IS NOT NULL)
    )
);

CREATE INDEX idx_plugins_tenant_type
ON registry.plugins (tenant_id, plugin_type);
```

生产环境中，`plugin_id` 的全局唯一性还是租户内唯一性，应与命名策略一起确定。上面的设计采用全局唯一 ID，减少依赖引用时的歧义。

### 5.3 插件版本表

```
CREATE TABLE registry.plugin_versions (
    id                  UUID PRIMARY KEY,
    plugin_id           UUID NOT NULL
        REFERENCES registry.plugins(id),
    version             TEXT NOT NULL,
    protocol_version    TEXT NOT NULL,
    manifest_json       JSONB NOT NULL,
    manifest_digest     TEXT NOT NULL,
    artifact_digest     TEXT NOT NULL,
    lifecycle_state     TEXT NOT NULL CHECK (
        lifecycle_state IN (
            'draft',
            'submitted',
            'validating',
            'testing',
            'staged',
            'approved',
            'published',
            'deprecated',
            'disabled',
            'revoked',
            'rejected',
            'quarantined'
        )
    ),
    compatibility_json  JSONB NOT NULL DEFAULT '{}',
    created_by          UUID NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at        TIMESTAMPTZ NULL,

    UNIQUE (plugin_id, version)
);

CREATE INDEX idx_plugin_versions_state
ON registry.plugin_versions (lifecycle_state, created_at DESC);
```

注意：数据库中的 `UNIQUE (plugin_id, version)` 只能防止重复创建同名版本，不能自动防止已发布版本的 `manifest_json` 或摘要被更新。

评审前版本中的 `UNIQUE (id, artifact_digest)` 已删除：`id` 已是主键，任何包含 `id` 的复合唯一约束都与主键等价，属于噪音约束，不增加约束力；若确需约束"同一制品摘要只对应一个版本"，应建 `(artifact_digest)` 上的独立索引，但同一制品被多个版本引用（重新打包未变更内容）是合法场景，故不建。

应同时采取以下措施：

1. 应用层禁止更新已发布版本的身份、Manifest、摘要和依赖。
2. 数据库触发器或专门的写入权限限制保护关键字段。
3. 运行时通过独立只读接口读取已发布版本。
4. 所有发布、撤销和重新启用行为进入审计日志。
5. 若采用管理员数据库直连运维，也必须保留数据库级保护与操作审计。

**已发布版本不可变的数据库层模板（P2 修复）**——触发器 + 列级权限双保险：

```
-- 应用角色对不可变列只读：只能更新生命周期相关列
REVOKE UPDATE, DELETE ON registry.plugin_versions FROM odap_app;
GRANT SELECT, INSERT ON registry.plugin_versions TO odap_app;
GRANT UPDATE (lifecycle_state, published_at, compatibility_json)
    ON registry.plugin_versions TO odap_app;

-- 触发器：published 族状态禁止物理删除、禁止修改身份/Manifest/摘要
CREATE OR REPLACE FUNCTION registry.guard_published_plugin_version()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.lifecycle_state IN ('published', 'deprecated', 'revoked', 'disabled') THEN
            RAISE EXCEPTION
                'plugin_version % in state % must not be deleted',
                OLD.id, OLD.lifecycle_state
                USING ERRCODE = 'restrict_violation';
        END IF;
        RETURN OLD;
    END IF;

    IF OLD.lifecycle_state IN ('published', 'deprecated', 'revoked', 'disabled') THEN
        IF NEW.plugin_id IS DISTINCT FROM OLD.plugin_id
           OR NEW.version IS DISTINCT FROM OLD.version
           OR NEW.manifest_json IS DISTINCT FROM OLD.manifest_json
           OR NEW.manifest_digest IS DISTINCT FROM OLD.manifest_digest
           OR NEW.artifact_digest IS DISTINCT FROM OLD.artifact_digest
           OR NEW.created_by IS DISTINCT FROM OLD.created_by
           OR NEW.created_at IS DISTINCT FROM OLD.created_at
        THEN
            RAISE EXCEPTION
                'plugin_version % is immutable in state %; create a new version instead',
                OLD.id, OLD.lifecycle_state
                USING ERRCODE = 'restrict_violation';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER trg_plugin_versions_immutable
    BEFORE UPDATE OR DELETE ON registry.plugin_versions
    FOR EACH ROW
    EXECUTE FUNCTION registry.guard_published_plugin_version();
```

说明：生命周期状态迁移（`published` → `deprecated` 等，§4.2 状态机允许的合法流转）只更新 `lifecycle_state` 与 `published_at`，不受触发器阻断；列级权限从账号层面把“改身份/Manifest/摘要”变为无权限，触发器作为数据库层兜底，即使运维直连也无法静默改写。本模板与 §5.3 DDL 的 `tenant_id` 复合唯一键补充合并为同一迁移。

### 5.4 依赖与制品表

```
CREATE TABLE registry.plugin_dependencies (
    id                  UUID PRIMARY KEY,
    plugin_version_id   UUID NOT NULL
        REFERENCES registry.plugin_versions(id),
    dependency_type     TEXT NOT NULL CHECK (
        dependency_type IN (
            'plugin',
            'ontology',
            'tool',
            'policy',
            'service'
        )
    ),
    dependency_id       TEXT NOT NULL,
    version_constraint  TEXT NOT NULL,
    optional            BOOLEAN NOT NULL DEFAULT FALSE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (
        plugin_version_id,
        dependency_type,
        dependency_id
    )
);

CREATE TABLE registry.plugin_artifacts (
    id                  UUID PRIMARY KEY,
    plugin_version_id   UUID NOT NULL
        REFERENCES registry.plugin_versions(id),
    artifact_kind       TEXT NOT NULL CHECK (
        artifact_kind IN (
            'container_image',
            'wasm_module',
            'bundle',
            'manifest',
            'schema',
            'test_report',
            'sbom'
        )
    ),
    digest              TEXT NOT NULL,
    storage_uri         TEXT NOT NULL,
    media_type          TEXT NOT NULL,
    size_bytes          BIGINT NOT NULL CHECK (size_bytes >= 0),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (plugin_version_id, artifact_kind, digest)
);
```

`storage_uri` 仅用于引用受控对象存储中的制品，不应允许运行时直接访问任意 URL。下载制品必须由 Artifact Manager 完成权限检查、摘要校验与必要的签名校验。

对于依赖版本范围，Registry 在发布时应生成完整的依赖锁定清单。运行中的任务不能每次重新解析 `^1.0.0`，否则同一任务在不同时间可能加载不同依赖。

### 5.5 安装、验证和审核记录

```
CREATE TABLE registry.plugin_installations (
    id                  UUID PRIMARY KEY,
    tenant_id           UUID NOT NULL,
    plugin_id           UUID NOT NULL
        REFERENCES registry.plugins(id),
    plugin_version_id   UUID NOT NULL
        REFERENCES registry.plugin_versions(id),
    installation_state  TEXT NOT NULL CHECK (
        installation_state IN (
            'active',
            'upgrading',
            'rolled_back',
            'disabled'
        )
    ),
    config_json         JSONB NOT NULL DEFAULT '{}',
    installed_by        UUID NOT NULL,
    installed_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, plugin_id, plugin_version_id)
);

-- 每个 (tenant_id, plugin_id) 同时最多一行生效安装
CREATE UNIQUE INDEX idx_installations_single_active
    ON registry.plugin_installations (tenant_id, plugin_id)
    WHERE installation_state = 'active';

CREATE TABLE registry.plugin_validation_runs (
    id                  UUID PRIMARY KEY,
    plugin_version_id   UUID NOT NULL
        REFERENCES registry.plugin_versions(id),
    validation_stage    TEXT NOT NULL,
    result              TEXT NOT NULL CHECK (
        result IN ('passed', 'failed', 'warning', 'cancelled')
    ),
    report_digest       TEXT NULL,
    report_uri          TEXT NULL,
    details_json        JSONB NOT NULL DEFAULT '{}',
    started_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at         TIMESTAMPTZ NULL
);

CREATE TABLE registry.plugin_reviews (
    id                  UUID PRIMARY KEY,
    plugin_version_id   UUID NOT NULL
        REFERENCES registry.plugin_versions(id),
    reviewer_id         UUID NOT NULL,
    decision            TEXT NOT NULL CHECK (
        decision IN ('approved', 'rejected', 'changes_requested')
    ),
    reason              TEXT NOT NULL,
    reviewed_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

这些表是核心结构示例，生产迁移还应补齐发布者引用、软删除规则、审计关联、租户 RLS 和时间分区策略。

**安装记录的唯一约束与升级语义（P1 修复）**：`plugin_installations` 采用 `UNIQUE (tenant_id, plugin_id, plugin_version_id)`，不再使用 `UNIQUE (tenant_id, plugin_id)`。旧约束迫使升级原地改写 `plugin_version_id`，升级历史丢失、无法回滚到旧版本；新约束下每次安装、升级、回滚都是独立行，配合以下状态机：

- `ACTIVE`：当前生效版本。每个 `(tenant_id, plugin_id)` 同时最多一行 `ACTIVE`（由部分唯一索引 `idx_installations_single_active` 保证）。
- `UPGRADING`：升级目标行，升级流程进行中。
- `ROLLED_BACK`：升级失败被回滚的行，或升级成功后被取代的旧行——已退出生效集合，但记录永久保留。
- `DISABLED`：被租户管理员停用的行，保留配置以便恢复。

流转规则：

- 安装：插入一行，状态 `ACTIVE`（或按租户策略先 `DISABLED` 再启用）。
- 升级：插入新版本行 `UPGRADING`；依赖与兼容校验通过后，在同一事务内把旧行置 `ROLLED_BACK`、新行置 `ACTIVE`（条件更新，见 §10.4）。
- 升级失败：新行置 `ROLLED_BACK`，旧行保持 `ACTIVE`——旧行从未被删除，也从未被改写。
- 回滚：把当前 `ACTIVE` 行置 `ROLLED_BACK`，把目标旧行重新置 `ACTIVE`；回滚只是状态迁移，不生成新版本、不触碰制品。
- 停用/恢复：`ACTIVE` 与 `DISABLED` 互转。

“当前安装版本”查询 = 该 `(tenant_id, plugin_id)` 下状态为 `ACTIVE` 的行；升级历史 = 全部行按 `installed_at` 排序，回滚链路可由 `installed_by` 与审计事件还原。

另外，插件审核记录应采用追加式历史记录。重新审核不是覆盖旧决定，而是新增一条记录。对高风险插件，应执行职责分离，避免开发者同时完成发布审批。

### 5.6 能力声明、签名与发布记录（§5.1 关系图补齐）

§5.1 关系图中列出但前文未给出 DDL 的三张表在此补齐。三张表都是版本级（或插件级）记录，`tenant_id` 从父记录冗余（平台级插件为 NULL，租户级插件为对应租户），用于复合外键的租户一致校验与 RLS 过滤；引用完整性由简单外键保证，复合外键在 `tenant_id` 非空时追加租户一致性约束。

```
CREATE TABLE registry.plugin_capability_declarations (
    id                  UUID PRIMARY KEY,
    tenant_id           UUID,
    plugin_version_id   UUID NOT NULL,
    capability_name     TEXT NOT NULL,
    capability_type     TEXT NOT NULL CHECK (
        capability_type IN ('consume', 'provide')
    ),
    description         TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (plugin_version_id, capability_name, capability_type),
    FOREIGN KEY (plugin_version_id)
        REFERENCES registry.plugin_versions(id),
    FOREIGN KEY (tenant_id, plugin_version_id)
        REFERENCES registry.plugin_versions(tenant_id, id)
);

CREATE INDEX idx_capability_declarations_name
    ON registry.plugin_capability_declarations (capability_name);
```

`capability_type` 区分“消费”（插件需要平台或其他插件提供的能力，对应 Manifest `requiredServices`/`capabilities`）与“提供”（插件对外暴露的能力，供 Registry 能力目录聚合）。能力声明只是声明，不等于授权（§7.1）。

```
CREATE TABLE registry.plugin_signatures (
    id                  UUID PRIMARY KEY,
    tenant_id           UUID,
    plugin_version_id   UUID NOT NULL,
    artifact_kind       TEXT NOT NULL CHECK (
        artifact_kind IN (
            'container_image',
            'wasm_module',
            'bundle',
            'manifest',
            'schema',
            'test_report',
            'sbom'
        )
    ),
    artifact_digest     TEXT NOT NULL,
    signature_algorithm TEXT NOT NULL CHECK (
        signature_algorithm IN (
            'ed25519',
            'ecdsa-p256-sha256',
            'rsa-pss-sha256'
        )
    ),
    key_id              TEXT NOT NULL,
    signature           TEXT NOT NULL,
    signer_id           UUID NOT NULL,
    signed_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    verified_at         TIMESTAMPTZ,

    UNIQUE (plugin_version_id, artifact_kind, artifact_digest, key_id),
    FOREIGN KEY (plugin_version_id)
        REFERENCES registry.plugin_versions(id),
    FOREIGN KEY (tenant_id, plugin_version_id)
        REFERENCES registry.plugin_versions(tenant_id, id)
);
```

签名只证明“制品与签名密钥之间的关系”，不代表插件逻辑安全（§8.4）。同一版本对同一制品更换密钥重签是一条新记录，历史签名保留以备审计。

```
CREATE TABLE registry.plugin_publications (
    id                  UUID PRIMARY KEY,
    tenant_id           UUID,
    plugin_id           UUID NOT NULL,
    plugin_version_id   UUID NOT NULL,
    environment         TEXT NOT NULL CHECK (
        environment IN ('development', 'staging', 'production')
    ),
    published_by        UUID NOT NULL,
    published_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at          TIMESTAMPTZ,

    FOREIGN KEY (plugin_id)
        REFERENCES registry.plugins(id),
    FOREIGN KEY (plugin_version_id)
        REFERENCES registry.plugin_versions(id),
    FOREIGN KEY (tenant_id, plugin_id)
        REFERENCES registry.plugins(tenant_id, id)
);

-- 同一版本在同一环境同时只允许一条有效发布；撤销后重新发布生成新记录
CREATE UNIQUE INDEX idx_plugin_publications_active
    ON registry.plugin_publications (plugin_version_id, environment)
    WHERE revoked_at IS NULL;
```

发布记录是追加式的：撤销（`revoked_at`）不删除历史行，重新发布生成新记录，保证“哪个版本何时发布到哪个环境、由谁发布”完整可查。`plugin_id` 必须与 `plugin_version_id` 属于同一插件，该一致性由注册服务的发布流程保证（§10.4 条件更新事务内校验）。

为支持上述复合外键，`registry.plugins` 与 `registry.plugin_versions` 需要与第二篇 DDL 一致的租户复合唯一键：

```
ALTER TABLE registry.plugins
    ADD CONSTRAINT uq_plugins_tenant_id UNIQUE (tenant_id, id);

ALTER TABLE registry.plugin_versions
    ADD COLUMN tenant_id UUID,
    ADD CONSTRAINT uq_plugin_versions_tenant_id UNIQUE (tenant_id, id);
```

`plugin_versions.tenant_id` 从 `plugins.tenant_id` 冗余（平台级插件为 NULL）。这两条 ALTER 合并进对应迁移，不改变既有全局唯一 `plugin_id` 与 `(plugin_id, version)` 约束。

### 5.7 JSONB 的使用边界

适合放入 `JSONB` 的数据：

- Manifest 内容及扩展字段。
- 插件运行配置。
- 验证诊断明细。
- 兼容性报告。
- 非核心、可演进的能力声明。

应保留为独立列或关联表的数据：

- 插件身份与租户 ID。
- 版本与制品摘要。
- 生命周期状态。
- 依赖关系。
- 发布者与审批人。
- 安装状态。
- 需要唯一约束或高频过滤的字段。

不要把整个 Registry 设计成一个巨大的 JSONB 文档。身份、依赖和生命周期是平台治理的关键数据，必须能被数据库约束和索引。

## 6. 插件运行协议：ODAP Plugin Protocol v1

这是本篇最重要的工程部分。

如果每个插件都自行设计 HTTP API、输入输出结构和错误处理方式，平台很快会出现多种不兼容接口。因此，ODAP 应定义统一的插件协议，让 Agent、Tool、Ontology 等插件在各自专业接口之外，共享相同的调用封装、身份上下文、错误模型、取消机制与审计关联。

### 6.1 协议选型

MVP 推荐：

- 控制面：Go HTTP API。
- 插件调用面：HTTP + JSON，统一协议与 Schema。
- 本地隔离执行：独立进程或容器。
- 轻量纯计算插件：未来可支持 WASI。
- 高吞吐内部通信：必要时增加 gRPC，但不作为 MVP 的必选项。
- 异步任务：通过平台任务系统和消息队列处理，不让长时间任务占用同步调用连接。

第一版不建议直接让所有插件实现 Go 原生插件接口，也不建议把所有插件编译进一个二进制。

Go 的原生插件机制在部署、构建版本、依赖和隔离方面存在限制，不适合作为多租户、第三方插件的通用安全边界。

### 6.2 标准调用请求

所有插件调用应由平台生成统一的调用信封（Invocation Envelope）。

```
package pluginprotocol

import (
    "encoding/json"
    "time"
)

type InvocationEnvelope struct {
    ProtocolVersion string          `json:"protocol_version"`
    InvocationID    string          `json:"invocation_id"`
    CorrelationID   string          `json:"correlation_id"`
    TraceID         string          `json:"trace_id"`

    TenantID        string          `json:"tenant_id"`
    RunID           string          `json:"run_id"`
    StepID          string          `json:"step_id"`

    PluginID        string          `json:"plugin_id"`
    PluginVersionID string          `json:"plugin_version_id"`
    ArtifactDigest  string          `json:"artifact_digest"`

    AuthorizationDecisionID string `json:"authorization_decision_id,omitempty"`

    Operation       string          `json:"operation"`
    Input           json.RawMessage `json:"input"`

    Deadline        time.Time       `json:"deadline"`
    IdempotencyKey  string          `json:"idempotency_key,omitempty"`
}
```

需要特别注意：上面的结构是平台内部协议模型。不应将它未经筛选地原样暴露给插件。

例如，插件调用请求中的 `TenantID`、`PluginVersionID` 等字段只能由平台可信组件生成。插件如果能够自行提交这些字段并让平台直接信任，就可能伪造租户、版本或执行来源。

推荐实现方式：

1. Harness 生成可信的调用上下文。
2. Tool Runtime 解析注册中心中的插件版本。
3. Authorization Engine 校验本次调用。
4. Plugin Runtime 构造实际发送给插件的请求。
5. 插件返回结果后，Runtime 校验响应 Schema、调用 ID 与资源限制。
6. 平台记录调用结果、证据引用和审计事件。

插件只获得完成任务所必需的上下文，不获得平台数据库连接或内部签名密钥。

**Envelope 字段边界（澄清 §6.x 与 `/v1/invoke` 的关系）**：

- **Runtime 生成并校验，插件不可提交、不可伪造**：`ProtocolVersion`、`InvocationID`、`CorrelationID`、`TraceID`、`TenantID`、`RunID`、`StepID`、`PluginID`、`PluginVersionID`、`ArtifactDigest`、`AuthorizationDecisionID`、`Deadline`、`IdempotencyKey`。其中授权决策 ID 由 Authorization Engine 校验通过后注入（第三篇授权决策记录），插件只读使用（如写入自己的审计上下文），不能自行指定。插件在 `/v1/invoke` 请求体中提交这些字段时，Runtime 一律忽略并以自身构造的值为准，不一致只记录审计，不作为信任来源。
- **插件可提交**：`Operation` 与 `Input`（受 §6.5 输入 Schema 约束）。`Input` 中不得携带上述受信字段的覆盖值；Schema 校验拒绝包含保留键的输入。
- **插件按需申请字段**：Manifest 可声明 `requiredEnvelopeFields`（如 `RunID`、`StepID`、`CorrelationID`），Runtime 按最小必要原则投影——未声明的受信字段默认不下发；插件需要更多字段时必须升级 Manifest 版本并重新审核，而不是在运行时试探。
- **响应方向同理**：插件不得在响应中修改 `InvocationID`、协议版本等信封字段；Runtime 校验响应信封与请求一一对应（§6.3）。

### 6.3 标准调用响应

```
package pluginprotocol

import (
    "encoding/json"
    "time"
)

type InvocationResponse struct {
    ProtocolVersion string          `json:"protocol_version"`
    InvocationID    string          `json:"invocation_id"`

    Status          string          `json:"status"`
    Output          json.RawMessage `json:"output,omitempty"`
    Error           *PluginError    `json:"error,omitempty"`

    EvidenceRefs    []string        `json:"evidence_refs,omitempty"`
    Warnings        []string        `json:"warnings,omitempty"`

    StartedAt       time.Time       `json:"started_at"`
    FinishedAt      time.Time       `json:"finished_at"`
}

type PluginError struct {
    Code       string         `json:"code"`
    Message    string         `json:"message"`
    Retryable  bool           `json:"retryable"`
    Details    map[string]any `json:"details,omitempty"`
}
```

建议定义以下错误码类别（全部登记在 `docs/contracts/02-统一错误码注册表.md` §3.3 PLUGIN 域，原 9 码全部收编，本篇不得新增未登记错误码）：

| 域码                             | 基础码               | 含义           | 默认处理                       |
| ------------------------ | ----------------- | ------------ | -------------------------- |
| `PLUGIN_INVALID_INPUT`   | INVALID_ARGUMENT  | 输入不符合 Schema | 不重试，修正输入                   |
| `PLUGIN_UNSUPPORTED_OPERATION` | INVALID_ARGUMENT | 不支持该操作      | 不重试                        |
| `PLUGIN_PERMISSION_DENIED` | PERMISSION_DENIED | 权限检查失败      | 不重试，记录审计                   |
| `PLUGIN_DEPENDENCY_UNAVAILABLE` | UNAVAILABLE   | 依赖服务不可用     | 按策略退避重试                    |
| `PLUGIN_RATE_LIMITED`    | RATE_LIMITED      | 达到限流阈值      | 尊重退避策略                     |
| `PLUGIN_DEADLINE_EXCEEDED` | DEADLINE_EXCEEDED | 调用超过截止时间    | 视操作类型决定；有副作用时转 `EXECUTION_UNKNOWN` |
| `PLUGIN_RESOURCE_EXHAUSTED` | RESOURCE_EXHAUSTED | 资源配额不足   | 不立即无限重试                    |
| `PLUGIN_INTERNAL_ERROR`  | INTERNAL          | 插件内部错误      | 有限重试或隔离                    |
| `PLUGIN_RESULT_INVALID`  | VALIDATION_FAILED | 输出不符合契约     | 隔离结果并记录诊断                  |

`PluginError.Code` 在协议层使用上表域码全名；响应同时返回 `base_code`（基础码）供客户端通用处理，映射关系与重试语义以注册表为准，本篇不另行定义。注意 `PERMISSION_DENIED` 过去在第一篇与本篇各定义一次，现统一收编为域码 `PLUGIN_PERMISSION_DENIED`（基础码 `PERMISSION_DENIED`）；授权三态 `ALLOW`/`DENY`/`REQUIRE_APPROVAL` 是决策不是错误码（contracts/02 §1 原则 4）。

错误响应必须有明确的状态和诊断信息。不能因为插件返回 HTTP 200，就把业务失败认定为成功；也不能把插件进程退出直接解释为远端 ERP 操作失败。

特别是具有副作用的操作：调用超时只表示平台未在截止时间内获得可信结果。必须由 ERP Adapter 根据幂等键和远端回执进行对账，不能直接盲目重试。

### 6.4 标准运行端点

对于采用 HTTP 的容器插件，建议统一约定：

| 端点               | 方法   | 用途        |
| ---------------- | ---- | --------- |
| `/health/live`   | GET  | 进程是否存活    |
| `/health/ready`  | GET  | 是否可以接收调用  |
| `/protocol/info` | GET  | 协议版本和接口能力 |
| `/v1/invoke`     | POST | 执行标准调用    |
| `/v1/cancel`     | POST | 请求取消指定调用  |

`/v1/cancel` 只是取消请求，不保证业务操作已撤销。插件必须在安全边界内处理中断；对于已提交的外部写操作，撤销需要走独立的业务补偿或对账流程。

插件运行端口只允许 Runtime 访问，不能直接暴露到企业用户网络。

### 6.5 输入输出 Schema

每个插件版本必须绑定不可变的输入、输出 Schema。

例如，报销 Agent 的输入可以包含：

```
{
  "type": "object",
  "required": ["task", "claim_id"],
  "properties": {
    "task": {
      "type": "string",
      "enum": [
        "inspect",
        "prepare_draft",
        "explain_policy"
      ]
    },
    "claim_id": {
      "type": "string",
      "minLength": 1,
      "maxLength": 128
    }
  },
  "additionalProperties": false
}
```

生产环境还需要验证字段格式、嵌套深度、数组长度、输入总字节数和敏感数据处理规则。对于 JSON Schema 的具体方言，应在协议版本中固定，例如采用 Draft 2020-12，并明确引用解析规则。

不允许插件在运行时自行替换自己的 Schema。接口变更必须生成新的插件版本并重新验证。

## 7. 插件能力模型与依赖解析

### 7.1 能力声明不等于能力授予

ODAP 应将能力分成三层：

- 声明能力：插件开发者在 Manifest 中描述自己能够做什么。
- 注册能力：Registry 验证并记录该插件提供的接口。
- 授权能力：权限引擎根据具体主体、任务和资源决定本次能否执行。

例如：

```
Plugin: expense-agent
  声明能力：
    expense.read
    expense.draft.create
    expense.policy.explain

Tool Registry:
  已登记接口：
    expense.query
    expense.draft.create
    policy.search

Authorization Engine:
  当前任务允许：
    expense.query = Allow
    expense.draft.create = RequireApproval
    erp.payment.execute = Deny
```

即使 Agent 试图在自然语言计划中要求执行付款，Harness 也必须在真正调用工具前进行权限检查。

能力名称应使用稳定的命名空间，例如：

```
ontology.query
ontology.validate
tool.invoke
expense.read
expense.draft.create
expense.submit
erp.operation.execute
erp.payment.execute
```

`tool.invoke` 这种平台级权限不应被理解为可以调用任意工具。实际调用仍需检查具体工具 ID、操作名称、参数范围及业务风险。

### 7.2 依赖解析与锁定

依赖声明只描述兼容范围，发布时应解析出实际版本。

例如，Agent 声明：

```
dependencies:
  - id: com.odap.ontology.expense
    version: "^1.0.0"
  - id: com.odap.tool.budget-query
    version: "^1.2.0"
```

Registry 在发布阶段解析并生成锁定清单：

```
{
  "root": {
    "plugin_id": "com.odap.expense-agent",
    "version": "1.0.0"
  },
  "resolved_dependencies": [
    {
      "plugin_id": "com.odap.ontology.expense",
      "version": "1.1.2",
      "artifact_digest": "sha256:..."
    },
    {
      "plugin_id": "com.odap.tool.budget-query",
      "version": "1.2.4",
      "artifact_digest": "sha256:..."
    }
  ]
}
```

这里的摘要是格式示意，实际发布时必须使用真实计算值。

每次运行需要保存实际依赖锁定清单，确保任务可复现。即使后来发布了更高版本，已有运行记录也必须能够还原原来的插件依赖。

### 7.3 依赖冲突规则

第一版至少处理以下情况：

1. 同一插件 ID 被解析出互不兼容的版本。
2. 必需依赖不存在或被撤销。
3. 插件依赖循环。
4. Ontology 版本与 Agent 所需语义不兼容。
5. Tool 的输入输出契约发生破坏性变更。
6. Policy 版本发生变化，导致旧插件的执行行为不再满足安全要求。

发现依赖冲突时，Registry 不应静默选择一个“最接近”的版本。应生成结构化诊断，说明冲突链路、涉及的版本和可行修复方案。

## 8. 插件执行隔离与安全边界

### 8.1 推荐的隔离等级（分级路线，P0 修复）

V1.0 采用**分级执行路线**，不再把容器作为 MVP 默认方案：

| 执行方式         | 适用场景             | 路线定位               |
| ------------ | ---------------- | ------------------ |
| 同进程 Go 模块    | 平台内部、可信核心逻辑      | 可用，但不是不可信插件的隔离方案   |
| **独立受控进程**   | 自研、可信、资源要求较低的插件  | **MVP 默认执行方式**    |
| 容器           | 通用插件和模型工具服务      | **可选 Runner**（需受控 dockerd 接入，见下） |
| WASI 沙箱      | 轻量、确定性较强的纯计算插件   | **V2 评估项**，V1.0 不承诺 |
| 独立虚拟机或强化隔离环境 | 高风险、不可信或需要强隔离的插件 | 按风险需要部署            |

**MVP 默认：独立受控进程**。每个插件以专用低权用户启动，进程边界由以下最低强制项构成：

- 非 root 用户运行，禁止提权；
- seccomp 系统调用过滤（默认 profile + 按需白名单）；
- cgroup 限额（CPU、内存、PID 数），超限即终止并记录事件；
- 文件系统只读（临时目录使用独立挂载的 tmpfs 或专用路径）；
- 网络默认拒绝，仅放行平台显式声明的受控出口（Model Gateway / Tool Runtime / Ontology Runtime）。

**容器作为可选 Runner**：需要完整镜像语义（自带运行时依赖、多语言插件）时启用。容器同样必须满足上面的最低强制项（非 root、seccomp/cgroup、只读根文件系统、网络默认拒绝）。Worker 启停容器需要 dockerd 访问权，MVP 提供两种受控接入方式，二选一：

| 接入方式 | 做法 | 取舍 |
| --- | --- | --- |
| **受控 socket 代理** | Worker 不直连 `/var/run/docker.sock`；由受控代理暴露白名单化的 Docker API 子集（create/start/stop/rm/logs/inspect），代理校验镜像摘要、资源上限与网络策略后代为执行 | 权限收得最死，代理成为单点（需自身加固与 HA）；适配多 Worker 共享 dockerd 的部署 |
| **DinD sidecar** | 每个 Worker 主机/Pod 配一个独立 Docker-in-Docker sidecar，Worker 通过本地 Unix socket 访问自己独占的 dockerd | 隔离彻底（一个 dockerd 不控制别人的容器），但每个 sidecar 是独立 daemon，资源占用与镜像缓存重复 |

**明确禁止**：把宿主机 `docker.sock` 直接挂进 Worker 容器——那等价于授予宿主 root 权限（P0 评审结论）。选择建议：私有化单机部署优先 DinD sidecar；多 Worker 集群优先受控 socket 代理。

**WASI 标注 V2 评估**：轻量纯计算插件的 WASI 沙箱在 V2 结合 WebAssembly 运行时成熟度评估，V1.0 不进入实现范围。

**与第一篇部署拓扑的衔接**：第一篇 §13.1/§13.2 的 Compose 拓扑新增 `pluginruntime` 组件（进程 Supervisor + 可选容器 Runner），与 server/worker 同处 backend 网络；插件进程由 pluginruntime 托管，不直接暴露端口到用户网络（§6.4）。`pluginruntime` 自身的健康检查与资源限制按第一篇 §13.3 生产 Compose 检查清单配置。

**Manifest 对齐**：§3.2 的 `spec.runtime.kind` 枚举与本表一一对应——`process`（独立受控进程，MVP 默认）、`container`（容器 Runner，可选）、`wasi`（V2 评估，V1.0 拒绝实例化）；`kind: declarative` 的声明式插件由平台内置执行器运行，不经过进程/容器边界。Manifest 声明了路线未启用的 `runtime.kind` 时，Registry 在启用检查中拒绝（§9.3）。

### 8.2 网络与凭据策略

建议默认拒绝插件的任意网络访问。

插件若需要模型能力，应通过 Model Gateway；若需要查询业务数据，应通过已授权的 Tool Runtime；若需要 ERP 操作，应通过 ERP Port。

```
Agent Plugin
    │
    ├── 禁止：直接连接 PostgreSQL
    ├── 禁止：直接访问 ERP 内网地址
    ├── 禁止：读取宿主机环境变量中的密钥
    │
    ├── 允许：调用获准的 Tool
    ├── 允许：调用 Ontology Runtime
    └── 允许：调用 Model Gateway
```

这里的“允许”仍须经过运行环境网络规则和平台授权检查，而不是只看 Manifest。

平台注入的凭据应具有最小权限、明确用途、短有效期和可撤销性。模型 API 密钥、ERP 密钥、签名私钥不能写入 Manifest、插件镜像或普通日志。

### 8.3 防止绕过 Harness

这是 ODAP 的关键安全要求。

如果 Agent 插件能直接连接 ERP，平台就无法保证审批、权限检查、幂等、审计和证据留存都已执行。

建议采取多层控制：

1. 插件运行网络与 ERP 网络隔离。
2. ERP 凭据只交给受控的 ERP Adapter。
3. ERP Adapter 不接受插件自行声明的用户身份或授权结论。
4. ERP Adapter 对每次写操作执行独立的授权与策略检查。
5. 所有外部操作携带平台生成的关联 ID、幂等键和授权上下文引用。
6. 关键操作必须校验审批是否仍然有效。
7. Adapter 记录请求摘要、执行结果、外部回执和对账状态。

Harness 是主要执行编排点，但不应成为唯一的安全防线。ERP Adapter 必须具备独立的强制执行能力。

### 8.4 插件供应链安全

建议在发布流程中增加以下检查：

- 制品摘要计算与签名验证。
- 依赖清单和软件物料清单（SBOM）保存。
- 已知漏洞扫描。
- 容器镜像来源与基础镜像策略检查。
- 危险权限和不必要网络出口检查。
- 恶意输入、越权调用、资源耗尽等安全测试。
- 发布者身份与审核记录验证。

签名证明制品与签名密钥之间的关系，不代表插件逻辑一定安全。因此，签名、静态扫描、沙箱测试、审核和运行时限制必须共同使用。

对于发现高危漏洞的插件版本，平台应支持撤销发布资格、阻止新任务启动、终止符合条件的运行实例，并保留审计记录。正在进行的关键业务操作应先进入安全对账或人工处理流程，不能为了停用插件而直接丢弃执行状态。

## 9. Registry API 详细设计

统一 API 前缀为 `/api/v1`，管理操作必须经过第三篇的身份认证和授权引擎。

### 9.1 插件注册与查询

| 方法     | API                                            | 说明       |
| ------ | ---------------------------------------------- | -------- |
| `POST` | `/api/v1/registry/plugins`                     | 创建插件逻辑身份 |
| `GET`  | `/api/v1/registry/plugins`                     | 查询可见插件   |
| `GET`  | `/api/v1/registry/plugins/{pluginId}`          | 查询插件详情   |
| `POST` | `/api/v1/registry/plugins/{pluginId}/versions` | 创建候选版本   |
| `GET`  | `/api/v1/registry/plugins/{pluginId}/versions` | 查询版本列表   |
| `GET`  | `/api/v1/registry/versions/{versionId}`        | 查询版本与摘要  |

创建插件、创建版本和上传制品建议采用分阶段操作。不能因为客户端提交了一个包含 Manifest 的 JSON，就直接将其标记为已发布。

### 9.2 验证、审核与发布

| 方法     | API                                              | 说明        |
| ------ | ------------------------------------------------ | --------- |
| `POST` | `/api/v1/registry/versions/{id}/validate`        | 发起静态验证    |
| `POST` | `/api/v1/registry/versions/{id}/test`            | 发起隔离测试    |
| `GET`  | `/api/v1/registry/versions/{id}/validation-runs` | 查询验证报告    |
| `POST` | `/api/v1/registry/versions/{id}/reviews`         | 提交审核决定    |
| `POST` | `/api/v1/registry/versions/{id}/publish`         | 发布通过审核的版本 |
| `POST` | `/api/v1/registry/versions/{id}/deprecate`       | 标记弃用      |
| `POST` | `/api/v1/registry/versions/{id}/revoke`          | 撤销版本资格    |

所有会改变状态的接口应采用显式的命令语义。对于可能重复提交的请求，建议使用 `Idempotency-Key`，并为请求摘要绑定去重记录，防止相同键被用于不同请求内容。

审核与发布应分离：审核通过不意味着已经发布，发布前还要检查审核记录、制品摘要、签名、依赖锁定清单以及当前安全策略。

### 9.3 租户安装与启用

| 方法      | API                                                     | 说明        |
| ------- | ------------------------------------------------------- | --------- |
| `POST`  | `/api/v1/tenants/{tenantId}/plugins/install`            | 安装指定插件版本  |
| `GET`   | `/api/v1/tenants/{tenantId}/plugins`                    | 查询已安装插件   |
| `PATCH` | `/api/v1/tenants/{tenantId}/plugins/{pluginId}`         | 修改受限配置或状态 |
| `POST`  | `/api/v1/tenants/{tenantId}/plugins/{pluginId}/enable`  | 启用插件      |
| `POST`  | `/api/v1/tenants/{tenantId}/plugins/{pluginId}/disable` | 禁用插件      |

启用操作必须检查：

- 目标版本已正式发布且未被撤销。
- 所有必需依赖可用。
- 租户具备安装与启用权限。
- 租户级策略允许该插件的能力。
- 插件配置符合 Schema。
- 对应运行环境满足安全要求。

不能让普通业务用户通过修改 `config_json` 来新增权限、任意网络地址或高风险操作能力。

### 9.4 查询与解释 API

为了方便后续 Agent 开发平台、管理控制台和故障排查，还应提供：

- `GET /api/v1/registry/capabilities`：查询平台支持的能力目录。
- `GET /api/v1/registry/versions/{id}/dependencies`：查看锁定依赖。
- `GET /api/v1/registry/versions/{id}/compatibility`：查询兼容性报告。
- `GET /api/v1/registry/versions/{id}/audit`：查询版本相关审计记录。
- `GET /api/v1/registry/installations/{id}/health`：查询安装实例健康状态。

查询结果应遵守租户隔离和敏感信息脱敏规则。平台级插件可见，不代表其内部配置、签名材料和审核备注对所有租户开放。

## 10. Go 代码架构与核心接口

### 10.1 推荐目录

承接 ODAP 总体工程目录，建议补充以下模块：

```
internal/
├── registry/
│   ├── domain/
│   │   ├── plugin.go
│   │   ├── version.go
│   │   ├── manifest.go
│   │   ├── lifecycle.go
│   │   └── dependency.go
│   ├── application/
│   │   ├── register.go
│   │   ├── validate.go
│   │   ├── review.go
│   │   ├── publish.go
│   │   └── resolve.go
│   ├── ports/
│   │   ├── repository.go
│   │   ├── artifact_store.go
│   │   ├── signer.go
│   │   └── validator.go
│   ├── adapters/
│   │   ├── postgres/
│   │   ├── objectstore/
│   │   └── signatures/
│   └── http/
│       ├── handlers.go
│       └── routes.go
│
├── pluginruntime/
│   ├── protocol/
│   ├── launcher/
│   ├── container/
│   ├── process/
│   ├── sandbox/
│   └── health/
│
├── authorization/
├── harness/
├── toolruntime/
└── audit/
```

`registry` 负责插件元数据、制品和生命周期；`pluginruntime` 负责实际运行；`authorization` 负责授权；`harness` 负责编排。它们通过接口依赖，而不是互相直接访问数据库表。

### 10.2 Registry 核心接口

```
package ports

import (
    "context"

    "odap/internal/registry/domain"
)

type PluginRepository interface {
    CreatePlugin(
        ctx context.Context,
        plugin *domain.Plugin,
    ) error

    GetPlugin(
        ctx context.Context,
        pluginID string,
    ) (*domain.Plugin, error)

    CreateVersion(
        ctx context.Context,
        version *domain.PluginVersion,
    ) error

    GetVersion(
        ctx context.Context,
        versionID string,
    ) (*domain.PluginVersion, error)

    TransitionLifecycle(
        ctx context.Context,
        versionID string,
        expected domain.LifecycleState,
        next domain.LifecycleState,
    ) error
}

type ArtifactStore interface {
    Put(
        ctx context.Context,
        content []byte,
        expectedDigest string,
    ) (string, error)

    Open(
        ctx context.Context,
        digest string,
    ) ([]byte, error)
}

type ManifestValidator interface {
    Validate(
        ctx context.Context,
        manifest []byte,
    ) (*domain.ValidationReport, error)
}

type SignatureVerifier interface {
    Verify(
        ctx context.Context,
        artifactDigest string,
        signature []byte,
        publisherID string,
    ) error
}
```

`TransitionLifecycle` 采用预期旧状态进行比较更新，可避免两个并发请求同时审核或发布同一个版本。

`ArtifactStore.Put` 不能盲目信任客户端提交的 `expectedDigest`。实现必须对实际内容重新计算摘要，并确认其与预期值一致；对象存储路径也应由服务端生成或规范化。

### 10.3 插件运行器接口

```
package pluginruntime

import (
    "context"

    "odap/pkg/pluginprotocol"
)

type Runner interface {
    Start(ctx context.Context) error

    Invoke(
        ctx context.Context,
        req pluginprotocol.InvocationEnvelope,
    ) (*pluginprotocol.InvocationResponse, error)

    Cancel(
        ctx context.Context,
        invocationID string,
    ) error

    Health(ctx context.Context) (*HealthStatus, error)

    Stop(ctx context.Context) error
}

type HealthStatus struct {
    Ready   bool
    Message string
}
```

不同执行方式可以实现不同 Runner：

- `ProcessRunner`
- `ContainerRunner`
- `WASIRunner`

Harness 不需要知道插件究竟是容器还是独立进程，只需依赖统一接口。具体执行环境由安装配置和平台安全策略决定。

### 10.4 生命周期更新的并发控制

发布逻辑不能写成：

```
读取状态 → 判断 APPROVED → 更新 PUBLISHED
```

因为多个请求可能同时读取到 `APPROVED`。

推荐采用事务内条件更新：

```
UPDATE registry.plugin_versions
SET lifecycle_state = 'published',
    published_at = now()
WHERE id = $1
  AND lifecycle_state = 'approved'
RETURNING id;
```

如果没有返回记录，说明当前状态已改变，或版本不满足发布前提。应用应返回明确的冲突错误，再重新读取状态并判断，而不是强行覆盖。

正式发布还需要在同一事务或可靠工作流中确保审核、签名、依赖锁定和发布记录一致。制品上传、容器扫描等耗时步骤不应长时间占用数据库事务；可以先生成验证结果，再用最终事务完成发布资格检查和状态切换。

## 11. 与 Harness 的集成规范

Registry 解决“插件是什么、哪个版本可信、具备哪些声明能力”；Harness 解决“当前任务怎样调用它”。

两者之间必须有一份不可变的执行解析结果。

### 11.1 执行绑定

任务启动时，Harness 需要固定：

```
RunExecutionManifest
  ├── Agent Plugin ID + Version + Digest
  ├── Ontology Versions + Digests
  ├── Tool Versions + Digests
  ├── Policy Versions + Digests
  ├── Model Provider Configuration Reference
  ├── Authorization Context Reference
  └── Execution Environment Profile
```

这份绑定清单应存储在任务记录或独立的执行清单表中，并与审计事件关联。

其中，模型配置可以包含模型名称、参数、服务提供方标识及配置版本，但不应把 API 密钥写入执行清单。

### 11.2 调用前后门禁

一次工具调用的标准流程：

1. Harness 根据已锁定的执行清单选择插件。
2. Runtime 验证插件状态、摘要和依赖。
3. Authorization Engine 检查主体、工具、资源、动作和上下文。
4. 如果需要审批，创建绑定操作摘要的审批记录。
5. 获准后，Runtime 校验输入 Schema 和资源配额。
6. 启动或复用符合隔离策略的插件实例。
7. 执行调用，记录调用 ID、耗时、输出摘要和错误。
8. 校验响应 Schema、输出大小和证据引用。
9. 对涉及外部副作用的操作，通过 ERP Port 对账。
10. 将结果、证据与审计事件写入平台存储。

即使第 3 步允许调用，也不能跳过第 5 步的输入校验；即使第 7 步返回成功，也不能把未确认的 ERP 结果认定为已完成。

### 11.3 插件停用与在途任务

插件被停用时，需要明确区分：

- 禁止创建新调用。
- 禁止现有任务发起新的高风险调用。
- 终止可以安全中断的纯计算任务。
- 对已提交或结果未知的 ERP 操作进入对账。
- 保留运行日志和已生成的证据。

如果插件是因为安全漏洞被撤销，应立即阻止新调用，并按风险策略中断运行实例；但不能把中断实例等同于撤销已经发生的外部业务操作。

### 11.4 运行结果的可信度

插件输出只是待验证的数据，不能自动成为企业事实。

例如，报销 Agent 输出：

```
{
  "claim_id": "CLM-2026-001",
  "recommended_amount": 1280,
  "conclusion": "符合报销政策"
}
```

平台不能仅凭这份 JSON 就认定报销金额正确或审批已经完成。应根据业务需要进行：

- 输出 Schema 验证。
- 本体与业务约束验证。
- 证据引用核验。
- 必要的人工审批。
- ERP 写入及外部回执确认。

本体定义业务语义，Policy 约束允许行为，插件执行专业任务，Harness 控制执行流程，ERP 回执确认外部事务结果。 这五个角色应在代码和数据模型中保持分离。

## 12. 插件配置、密钥与租户隔离

### 12.1 配置分层

不要把所有插件配置放进同一份 YAML。建议分为四层：

| 配置层    | 示例            | 管理者           |
| ------ | ------------- | ------------- |
| 插件默认配置 | 默认超时、输入限制     | 插件发布者         |
| 平台运行配置 | 容器资源上限、网络策略   | 平台管理员         |
| 租户配置   | 企业组织映射、业务域选项  | 企业管理员         |
| 任务运行参数 | 本次报销单 ID、查询范围 | Harness 按任务生成 |

插件开发者不能通过默认配置覆盖平台安全约束。租户管理员也不能关闭平台强制执行的安全限制。

建议最终生效配置遵循以下原则：

```
Effective Config =
    Plugin Defaults
  + Approved Tenant Configuration
  + Runtime Parameters
  ∩ Platform Security Constraints
```

这里的交集表示安全约束优先，不是把四份 JSON 简单合并。

### 12.2 密钥管理

Manifest 中仅允许声明所需密钥的逻辑名称或用途，例如：

```
secrets:
  required:
    - name: model-provider-credential
      purpose: invoke-approved-model-provider
```

真实凭据应由平台的密钥管理模块提供，并绑定到指定租户、插件版本、执行任务和允许的用途。

禁止：

- 在 Manifest 中存储真实密钥。
- 把密钥写入数据库普通配置字段。
- 将密钥放入模型上下文。
- 将密钥返回给 Agent 插件。
- 在错误消息或审计日志中记录完整密钥。

如果企业使用外部密钥管理系统，可通过统一的 `SecretProvider` 接口适配；MVP 可以从私有部署环境中的受控密钥服务开始。

## 13. 可观测性与审计

插件系统必须能够回答以下问题：

- 哪个用户或系统发起了任务？
- 哪个 Agent 版本参与了决策？
- 实际调用了哪个 Tool 版本？
- 调用时使用了哪些本体与策略版本？
- 哪一次权限检查决定允许或拒绝执行？
- 插件执行了多久，消耗了多少资源？
- 是否出现超时、取消、重试或依赖故障？
- 如果涉及 ERP，最终回执是什么？

建议统一使用以下关联字段：

```
tenant_id
run_id
step_id
invocation_id
plugin_id
plugin_version_id
artifact_digest
correlation_id
trace_id
authorization_decision_id
evidence_id
erp_operation_id
```

### 13.1 指标设计

第一版建议重点监控：

| 指标                                   | 用途            |
| ------------------------------------ | ------------- |
| `plugin_invocations_total`           | 调用次数及结果分布     |
| `plugin_invocation_duration_seconds` | 调用耗时分布        |
| `plugin_timeouts_total`              | 超时趋势          |
| `plugin_schema_failures_total`       | 输入输出契约错误      |
| `plugin_crashes_total`               | 进程或容器崩溃       |
| `plugin_dependency_failures_total`   | 依赖故障          |
| `plugin_authorization_denials_total` | 权限拒绝次数        |
| `plugin_resource_limit_hits_total`   | CPU、内存等资源限制触发 |
| `plugin_quarantines_total`           | 隔离事件          |

不要把 `tenant_id`、`run_id` 等高基数字段全部作为 Prometheus 标签，否则容易造成指标系统的存储压力。高基数关联信息应放在结构化日志和 Trace 中。

### 13.2 审计事件示例

```
{
  "event_type": "plugin.version.published",
  "tenant_id": "tenant-example",
  "plugin_id": "com.odap.expense-agent",
  "plugin_version": "1.0.0",
  "artifact_digest": "sha256:...",
  "actor_id": "admin-example",
  "decision": "approved",
  "correlation_id": "corr-example",
  "timestamp": "2026-10-12T10:00:00Z"
}
```

示例中的 ID 和摘要均为占位值。

审计系统应使用平台可信时钟、追加式记录和受控写入权限。对于高风险企业环境，可以进一步采用签名审计批次或外部不可变存储，提升审计记录的完整性。

## 14. 测试策略与验收标准

插件系统的测试不能只验证“插件能不能启动”。它还必须证明：协议可互操作、权限不会被绕过、版本可复现、异常可以恢复。

### 14.1 自动化测试矩阵

协议契约测试

输入输出 Schema、错误码、协议版本、取消请求、超时行为

Registry 状态测试

合法状态流转、非法发布、并发发布、重复请求、版本不可变性

依赖与兼容测试

依赖缺失、版本冲突、循环依赖、摘要不匹配、锁定清单复现

安全对抗测试

跨租户访问、伪造身份、直接访问 ERP、恶意网络请求、路径穿越

故障恢复测试

插件崩溃、容器重启、超时、取消、依赖服务中断、结果未知

端到端测试

安装插件、执行 Agent、调用 Tool、审批、Mock ERP 写入、审计查询

### 14.2 必须通过的安全测试

1. 普通用户不能发布未经审核的插件。
2. 插件不能通过伪造 `tenant_id` 获得其他租户的数据。
3. 插件不能通过伪造 `user_id` 提升权限。
4. Agent 不能自行修改 Harness 的执行状态。
5. 插件不能直接访问 ERP 网络或数据库。
6. 修改已审批操作的参数后，原审批必须失效。
7. 已撤销的插件版本不能启动新任务。
8. 插件制品摘要与 Registry 记录不一致时必须拒绝加载。
9. 输出不符合 Schema 时，结果不能直接传递到业务写入流程。
10. 超时或进程崩溃时，不得自动认定外部写操作失败。
11. 同一插件版本不能被并发请求发布成相互冲突的状态。
12. 插件被停用后，不能继续发起不符合停用策略的新调用。

### 14.3 MVP 验收标准

建议将以下项目作为第四篇完成的验收门槛。

| 验收项         | 完成标准                                |
| ----------- | ----------------------------------- |
| 统一 Manifest | 五类插件都有明确的 Schema 与示例                |
| 注册中心        | 支持注册、查询、验证、审核、发布、停用                 |
| 版本治理        | 已发布版本不可原地覆盖                         |
| 依赖管理        | 支持版本约束解析与依赖锁定                       |
| 签名与摘要       | 支持制品摘要校验和发布者签名验证                    |
| 运行协议        | 支持统一请求、响应、错误和取消语义                   |
| 运行隔离        | 至少一种隔离执行方式可用于插件测试                   |
| 权限联动        | 插件能力声明不自动授予权限                       |
| 租户隔离        | 跨租户访问测试全部通过                         |
| 审计          | 发布、安装、启用、停用和调用均可追踪                  |
| Mock 集成     | Agent 可通过 Harness 调用 Tool 并完成模拟业务操作 |
| 故障恢复        | 超时、崩溃和未知结果有明确处理路径                   |

测试通过率本身不是充分条件。涉及跨租户访问、权限绕过、制品完整性和 ERP 安全边界的测试，应要求零未解决的严重问题。

## 15. 分阶段实施计划

建议按四个阶段实现插件系统，避免一开始就开发复杂的插件市场和分布式运行平台。

1. 阶段一：协议与 Registry 最小闭环
   - 定义五类 Manifest Schema。
   - 实现 PostgreSQL Registry。
   - 完成插件身份、版本和状态机。
   - 提供本地 Mock 插件。
   - 完成发布前 Schema 校验。
2. 阶段二：隔离运行时与 Harness 接入
   - 实现 `Runner` 接口。
   - 完成容器或独立进程执行器。
   - 实现超时、取消、输入输出校验。
   - 通过 Harness 进行插件调用。
   - 接入第三篇的授权引擎。
3. 阶段三：安全供应链与依赖治理
   - 实现摘要与签名验证。
   - 增加依赖解析和锁定清单。
   - 增加安全扫描和审核流程。
   - 实现租户安装、启用与停用。
   - 完成恶意插件和跨租户对抗测试。
4. 阶段四：企业级运行治理
   - 增加插件健康监控和资源配额。
   - 完善版本撤销、升级与回滚。
   - 增加故障恢复与运行实例治理。
   - 建立插件审计、指标与追踪。
   - 为未来的私有插件仓库和组织级插件治理预留接口。

## 16. 本篇最终技术决策

建议将以下决策固化到 ODAP 架构规范中。

| 设计领域   | 最终建议                                      |
| ------ | ----------------------------------------- |
| 插件类型   | Agent、Ontology、Tool、Policy、Model Provider |
| 插件身份   | 稳定 ID + 不可变版本 + 制品摘要                      |
| 描述协议   | YAML Manifest，内部转换为规范化结构                  |
| Schema | JSON Schema，固定协议方言                        |
| 控制 API | Go HTTP API                               |
| 插件调用   | HTTP + JSON 起步                            |
| 运行方式   | 独立受控进程为 MVP 默认（非 root + seccomp + cgroup + 只读文件系统 + 网络默认拒绝），容器为可选 Runner（dockerd 受控接入：socket 代理或 DinD sidecar 二选一），WASI 为 V2 评估项 |
| 元数据    | PostgreSQL                                |
| 制品     | 受控对象存储                                    |
| 生命周期   | 显式状态机与条件状态迁移                              |
| 依赖     | 发布时解析，运行时使用锁定清单                           |
| 授权     | 统一 Authorization Engine，入口强制检查            |
| 关键操作   | 审批绑定不可变操作摘要                               |
| ERP 访问 | 只通过 ERP Port / Adapter                    |
| 审计     | 版本、权限、调用和外部回执全链路关联                        |
| 安全策略   | 默认拒绝、最小权限、隔离执行、失败关闭                       |

## 17. 第四篇完成后的系统能力

完成这一篇后，ODAP 将具备一个清晰的插件治理骨架：

- 企业可以开发自己的专业 Agent。
- 平台可以将业务本体封装成可版本化、可验证的插件。
- 工具可以通过统一协议提供业务能力。
- 策略可以独立登记和版本化。
- 模型服务可以通过统一接口接入。
- Registry 可以追踪插件从提交到发布的完整生命周期。
- Harness 可以按照固定版本和受控能力执行任务。
- 权限引擎可以在每次调用时决定是否允许执行。
- ERP Adapter 可以独立保护外部业务系统。

这也为你设想的“本体驱动智能体平台”建立了关键基础：企业本体不是一份孤立的知识文件，而是能够被注册、验证、锁定版本、由 Agent 消费并受到权限策略约束的运行时资产。

接下来最合理的工作是继续编写：

《第五篇：Ontology Runtime 与 Ontology Factory 详细设计》

这一篇将重点解决 ODAP 最具特色的部分：

1. OWL、RDF、SHACL 在运行时中的职责划分。
2. 本体注册、导入、验证、发布和版本兼容。
3. 数学与业务概念、实体、关系、约束的统一表示。
4. 企业制度文档到本体的转换流水线。
5. 本体生成 Agent 如何通过 Policy IR、概念模型和约束模型构建候选本体。
6. 如何验证候选本体、检测语义冲突，并通过人工审核后发布。
7. Agent 如何查询本体并获得可追溯的业务语义证据。
8. 本体版本如何与 Agent、Tool、Policy 和任务执行清单绑定。

这样，ODAP 就可以从通用的 Agent 插件平台，进一步走向真正以企业语义模型驱动业务执行的本体智能体平台。
