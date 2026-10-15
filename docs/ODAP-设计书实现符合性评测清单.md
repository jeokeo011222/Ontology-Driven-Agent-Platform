# ODAP 设计书实现符合性评测清单

> **基准**：`docs/` 下 **18 本设计书** = 总纲（`企业本体驱动智能体平台详细技术设计书.md`）+ 第一篇 ~ 第十七篇。
> （`设计书评审与优化建议报告.md` 作为对照材料一并核验，不计入 18 本。）
>
> **与自查矩阵的分工**：`ODAP-MVP-设计符合性自查矩阵.md` 回答"**交付了什么、门禁是否全绿**"；本清单回答"**逐篇对照设计书，还有哪些没实现**"。两份文档互补，不互相替代。
>
> **生成方式**：17 个并行核验子代理按篇分组读取设计书并从磁盘交叉验证（路由 / 迁移 / 建表 / 包依赖 / 测试 / 源码符号），再由作者回到磁盘与**运行中的容器栈**复核关键结论。所有标 `[实]` 的条目均由作者本人复现。

---

## 0. 证据等级与判定口径

### 0.1 证据等级

| 标记 | 含义 | 判定依据 |
|---|---|---|
| **[实]** | **活体实测**：在运行中的 compose 栈上发真实 HTTP、在真实 PostgreSQL 上跑 SQL 探针、或在磁盘上跑 grep/find 得到可直接复制的输出 | 最高。可被任何人按 §8 命令复现 |
| **[码]** | **源码核验**：给出具体文件路径 + 符号名（函数/类型/常量）+ 表名 + 测试名，作者抽样复核 | 高 |
| **[子]** | **子代理报告**：证据锚点齐备（文件+符号），但作者未逐条独立复核 | 中。引用时建议先按锚点自证 |

### 0.2 判定口径（四档，全篇严格使用）

| 判定 | 含义 |
|---|---|
| **✅ 已实现** | 找到真实实现（文件 + 符号 + 表/路由/测试），且与设计要求一致 |
| **🟡 部分实现** | 骨架 / 表结构 / 端点就位，但关键行为缺失（只有表无写入路径、只有读端点无写端点、返回硬编码或占位、**已实现但无生产调用方**） |
| **⬜ 未实现** | 设计要求存在，但磁盘上找不到对应实现 |
| **➖ 范围外** | **设计书自身**对该章节标注了 `[V1.5]` 或 `V2`，或明确属非 MVP 阶段 |

### 0.3 本清单新增的一条判定子类（重要）

**「已实现但无调用方」必须判 🟡，不得判 ✅。**

这是本次评测发现的最普遍失真模式：库代码完整、单测全绿，但**没有任何生产代码 import 它**。单测通过不能作为"系统可用"的证据——只能证明"这个库自洽"。§3 给出完整接线矩阵。

---

## 1. 结论摘要

### 1.1 分级统计

| 判定 | 条目数 | 占比 | 说明 |
|---|---|---|---|
| ✅ 已实现 | 96 | 42% | 键值仍在真实的表、路由与测试上 |
| 🟡 部分实现 | 78 | 34% | 其中 **23 条属"已实现但零装配"** |
| ⬜ 未实现 | 41 | 18% | **其中 12 条属 MVP 范围内**（非范围外），是最需要处置的一类 |
| ➖ 范围外 | 15 | 6% | 设计书自标 `[V1.5]` / `V2` |
| **合计** | **230** | 100% | — |

### 1.2 最严重的 9 条（全部经活体实测，证据等级 [实]）

| # | 缺陷 | 实测现象 | 影响面 |
|---|---|---|---|
| **L1** | **租户上下文注入有两套实现，其中一套写的是同形不同名的 GUC，且 SQL 语法本身就不成立** | `GET /api/v1/admin/users` → **HTTP 500** `"SET LOCAL tenant 失败"`；`/admin/organizations`、`/admin/ontology-categories` 同 | **3 个管理页面直接报错**；涉及 3 文件 23 个 Store 方法 |
| **L2** | **`/api/v1/auth/` 前缀遮蔽了 `logout` 与 `session`** | `POST /api/v1/auth/logout` → **404 page not found**（纯文本）；`GET /api/v1/auth/session` → **404** | 契约已登记（openapi + 路由表）却不可达；登出与取会话功能**完全失效** |
| **L3** | **本体校验端点恒返回 503** | `POST /api/v1/ontologies/{id}/validate` → 503 `"本体校验需加载 OWL/RDF/SHACL 构件…（对象存储+图存储未装配）"` | 本体生命周期**卡在 validate**；`PublishOntology` 却允许 `DRAFT` 直发（无校验门禁） |
| **L4** | **3 个坏页面不在任何 UI 验收门禁的覆盖清单里** | 目录类页面渲染门禁只覆盖 6 个页面：`/admin/ontologies`、`/agents`、`/tools`、`/identity`、`/models`、`/workflows` | `UsersPage`、`OrgsPage`、`OntologyCategoriesPage` **从未被扫过** → L1 可长期存活 |
| **L5** | **`audit.evidence_records` 无 append-only 触发器** | 真实库非内部触发器仅 2 个：`audit.audit_events`、`registry.plugin_versions` | 证据包表仅靠 `REVOKE` 保护，owner / 超级用户仍可改；审计链只覆盖事件表 |
| **L6** | **审计链校验与 fail-closed 写入无运行期调用方** | `Recorder.Verify` / `WriteOrBlock` / `BuildBundle` 在非测试代码中**零命中**（仅定义处与 `_test.go`） | 哈希链"可验证"只在测试中成立；生产写入路径 `handlers.go:265` 直接 `Append` 且**忽略返回错误** |
| **L7** | **6 个一级包 / 5 个子包零装配** | `agent`、`registry`、`evidence`、`ontology`、`execution/inbox`、`execution/lease` 生产 import **均为 0** | 第八/九/十三/十四篇的引擎是"孤岛库"；见 §3 |
| **L8** | **CI 流水线完全不存在** | `find` 全仓无 `.github/`、无 `.gitlab-ci.yml`、无 `Jenkinsfile`；`.yml/.yaml` 仅 3 个（compose、openapi、otel-collector） | 第十六篇 §10.1 与第一篇 §14.3 明确要求；P0 退出条件「CI」实为未达成 |
| **L9** | **`run_events` / `workflow.instances` / `llm.routing_policies` 等设计书要求的表根本不存在** | 实测表全集 ~79 张，上述表均不在其中 | 第六篇 §13.2、第十四篇 §15.2、第九篇 §5.2 的硬要求落空 |

---

## 2. 活体实测缺陷详录（证据等级 [实]）

### 2.1 L1 —— 租户上下文注入：两套实现、三个 GUC、一条不成立的 SQL

**现象**（真实 HTTP，2026-10-13，栈运行中）：

```
GET /api/v1/admin/ontology-categories  -> HTTP 500  {"error":{"code":"INTERNAL","base_code":"INTERNAL",
                                                     "message":"SET LOCAL tenant 失败","retryable":false}}
GET /api/v1/admin/users                -> HTTP 500  同上
GET /api/v1/admin/organizations        -> HTTP 500  同上
--- 对照（走另一套实现，正常）---
GET /api/v1/admin/roles                -> HTTP 200  {"items":[…]}
GET /api/v1/admin/role-bindings        -> HTTP 200  {"items":[…]}
GET /api/v1/admin/overview             -> HTTP 200  {"counts":{…}}
```

**根因（两层，各自独立致命）**：

`internal/api/store/` 内部存在**两套**租户上下文注入实现：

| 实现 | 位置 | 实际写入的 GUC | 被 FORCE RLS 表读取的 GUC | 结果 |
|---|---|---|---|---|
| **A. 正确** | `store.go:50 TenantTx` → `middleware/tenant.go:56` → `pgxutil.WithTenantTx` → `set_config('app.tenant_id', $1, true)` | `app.tenant_id` | `app.tenant_id` | ✅ 一致 |
| **B. 有缺陷** | `auth_store.go:266 setTenant()` → `SET LOCAL app.current_tenant_id = $1` | `app.current_tenant_id` | `app.tenant_id` | ❌ 不一致 |

RLS 策略函数（`migrations/platform/000011_add_rls_and_hash_chain.sql:8`）：

```sql
CREATE OR REPLACE FUNCTION app.current_tenant_id()
RETURNS UUID AS $$
    SELECT NULLIF(current_setting('app.tenant_id', true), '')::uuid
$$ LANGUAGE sql STABLE;
```

注意：`app.current_tenant_id` **既是函数名，又被当成 GUC 名使用**。`app.current_tenant_id()` 这个**函数**读的是 `app.tenant_id`；而 `setTenant()` 写的是名为 `app.current_tenant_id` 的**配置参数**。二者毫无关系。

**真实 PostgreSQL 探针（决定性）**：

```
=== 探针1：SET LOCAL 是否接受参数占位符 ===
NOTICE:  SET 拒绝参数: syntax error at or near "$1"

=== 探针2：写 app.current_tenant_id 对 RLS 函数是否无效 ===
 guc_app_tenant_id | fn_result
-------------------+-----------
                   |
(1 row)                       <- 两者均为 NULL

=== 探针3：正确姿势 set_config(app.tenant_id) ===
              fn_result
--------------------------------------
 11111111-1111-1111-1111-111111111111
(1 row)
```

因此 `setTenant()` **在任何情况下都返回错误**——它连 SQL 都过不了解析（`SET` 是 utility 语句，PostgreSQL 不接受 `$1` 占位符）。错误被包装为 `CodeInternal "SET LOCAL tenant 失败"`，即用户看到的 500。

**爆炸半径**（`grep -rln "setTenant(ctx"`）：

| 文件 | 调用点 | 含义 |
|---|---|---|
| `internal/api/store/auth_store.go` | 17 | `RegisterUser`、`AdminCreateUser`、`ListUsersCursor`、`GetUserWithMemberships`、`UpdateUser`、`DisableUser`、`ListOrganizations`、`CreateOrganization`、`UpdateOrganization`、`DisableOrganization`、`AddMembership`、`RemoveMembership`、`BindRole`、`UnbindRole`、`ListRoleBindings`、`RevokeSessionByID` |
| `internal/api/store/ontology_category_store.go` | 5 | 本体分类全 CRUD |
| `internal/api/store/session_store.go` | 1 | 会话存储 |

对照：另有 **8 个文件**使用正确的 `s.TenantTx(`：`approvals_eval.go`、`audit_aggregate.go`、`catalog.go`、`config_crud.go`、`ontology_policy.go`、`plan_templates.go`、`runs.go`、`tool_version.go`。

**已确认可达并报 500 的 3 个端点**，且**三页都有前端**：

| 端点 | HTTP | 前端页面 |
|---|---|---|
| `GET /api/v1/admin/users` | **500** | `web/src/features/admin/pages/UsersPage.tsx:25` |
| `GET /api/v1/admin/organizations` | **500** | `web/src/features/admin/pages/OrgsPage.tsx:24` |
| `GET /api/v1/admin/ontology-categories` | **500** | `web/src/features/admin/pages/OntologyCategoriesPage.tsx:27` |

> ⚠️ 注意：`auth_store.go:816 ListRoleBindings` **虽然含 `setTenant`，但 `/admin/role-bindings` 返回 200**——说明该 handler 实际走的是 `config_crud.go` 里使用 `s.TenantTx` 的同名能力。**因此"含 setTenant 的方法数"≠"受损端点数"**；上表 3 条是逐一打 HTTP 确认的，其余待逐个确认。这正是"必须实测而非静态推断"的例证。

**正确修法（单一真源）**：删除 `auth_store.go:266` 的 `setTenant()`，全部改调 `s.TenantTx(ctx, fn)`；`pgxutil` 作为唯一注入点。附带需要一个"防回归"门禁：**禁止 `SET LOCAL ... = $n` 这种写法**（`SET` 不接受占位符）。

### 2.2 L2 —— `/api/v1/auth/` 前缀遮蔽

**现象**：

```
POST /api/v1/auth/logout   -> HTTP 404   404 page not found   （纯文本，Go 默认 NotFound）
GET  /api/v1/auth/session  -> HTTP 404   404 page not found
POST /api/v1/auth/login    -> HTTP 400   {"error":{"code":"INVALID_ARGUMENT",…}}   ← 可达
```

**根因**（`internal/api/router.go:168-203`）：路由声明表里有 4 条 auth 路由，但装配时只把 2 条放进 `publicMux`：

```go
for _, rt := range routes(h) {
    switch rt.Pattern {
    case "/api/v1/auth/register", "/api/v1/auth/login":
        publicMux.HandleFunc(rt.Method+" "+rt.Pattern, rt.Handler)
    default:
        apiMux.HandleFunc(rt.Method+" "+rt.Pattern, rt.Handler)   // ← logout / session 落到这里
    }
}
...
root.Handle("/api/v1/auth/", authChain)   // authChain = RateLimit(publicMux)
root.Handle("/api/v1/", apiChain)         // apiChain  = RequireIdentity(Auth(apiMux))
```

按 Go 1.22+ `ServeMux` 的**最长前缀优先**规则，`/api/v1/auth/` 比 `/api/v1/` 更具体，因此 `/api/v1/auth/logout` 与 `/api/v1/auth/session` 全部被送进 `publicMux`——而 `publicMux` 里没有这两条路由 → 404。

**为什么门禁全绿**：`TestAllContractEndpointsRegistered` 与 `TestOpenAPICoversAllRoutes` 都只断言 `routes()` **声明表**与契约的一致性，**从不发 HTTP 请求**。声明表里这两条确实存在，于是两个门禁都是绿的，而端点实际不可达。这是"断言对象 ≠ 被判对象"的又一类假绿。

**正确修法**：把 `logout`/`session` 移出 `publicMux` 的特例分支（改为显式白名单判断"仅 register/login 公开"），或用 `root.HandleFunc("GET /api/v1/auth/session", …)` 显式注册在 root 上；并补一条**真实 HTTP 可达性**门禁（对 `routes()` 里每条路由发一次请求，断言不返回 Go 默认 404 文本）。

### 2.3 L3 —— 本体校验端点恒 503，而发布无门禁

**现象**：`internal/api/handlers/ontologies.go:110`：

```go
h.fail(w, r, errors.E(errors.CodeUnavailable,
    "本体校验需加载 OWL/RDF/SHACL 构件并驱动 RDFS/SHACL 校验链（对象存储+图存储未装配）").
```

诚实报错（不是假成功），符合项目纪律。但后果是本体生命周期**无法完成校验环节**。

而 `internal/api/store/ontology_policy.go:118 PublishOntology` 的条件是：

```sql
UPDATE ontology.ontology_versions
   SET status = 'PUBLISHED', published_at = now()
 WHERE ontology_id = $1 AND ($2::text IS NULL OR version = $2)
   AND status IN ('VALID','APPROVED','DRAFT')     -- ← DRAFT 可直接发布
```

即：**DRAFT 状态可以跳过校验直接发布**，而唯一的校验入口又是 503。这构成一个"门禁存在但被绕过"的组合缺陷——设计书 §11.1 要求发布前有验证，实测无。

> 自查矩阵 §2 把 M04 的"`ontologies/{id}/validate|publish` 端点已实现"记为 ✅，且 §6 诚实清单**未登记 validate 恒 503**。本清单登记之。

### 2.4 L4 —— 为什么这些没被发现：验收门禁的"覆盖清单"本身就是缺陷来源

自查矩阵 §1.6 记录的产品级 UI 验收覆盖 **6 个页面**：`/admin/ontologies`、`/agents`、`/tools`、`/identity`、`/models`、`/workflows`。

而 L1 命中的 3 个坏页面 **不在其中**：`/admin/users`、`/admin/organizations`、`/admin/ontology-categories`。

同时 `/identity` 页面用的是 `/admin/roles` + `/admin/role-bindings`（均 200，见 `web/src/features/policies/api.ts:52,59`），因此**即使扫了 `/identity` 也扫不到 L1**。

**结论**：门禁的覆盖清单与"设计书要求的路由全集"脱钩，是缺陷能长期存活的直接原因。建议把目录类页面渲染门禁的页面清单改为**从 `web/src/app/router.tsx` 与 `contracts/openapi.yaml` 的交集自动生成**，而非手工枚举。

---

## 3. 装配接线矩阵（哪些"已实现"其实没接进进程）

在 Monorepo 里，"有包、有测试"与"被进程装配"是两回事。下表统计各包被**非测试代码** import 的次数（`grep -rn "odap/internal/<pkg>\"" --include=*.go internal/ cmd/ mockerp/`，排除自身与 `_test.go`）：

### 3.1 一级包

| 包 | 生产 import | 装配方 | 判定影响 |
|---|---|---|---|
| `internal/agent` | **0** | 无 | 第八/十四篇的执行链**完全未接** |
| `internal/registry` | **0** | 无 | 第四篇的 Registry 服务层未接 HTTP |
| `internal/evidence` | **0** | 无 | 第十一篇证据包未接 |
| `internal/ontology` | **0** | 无 | 第五/十三篇引擎未接 |
| `internal/execution` | **0**（子包 `outbox` 例外，4 处） | 仅 outbox | Inbox/lease 未接 |
| `internal/audit` | 2 | `cmd/api`、`api/handlers` | ✅ 已接（但仅 Appender 部分） |
| `internal/authorization` | 2 | `internal/agent`、`api/store` | ✅ |
| `internal/erp` | 3 | `internal/agent`、`api/handlers` | ✅ 部分 |
| `internal/harness` | 5 | `cmd/api`、`cmd/worker`、`internal/agent`、`api/handlers` | ✅ |
| `internal/identity` | 1 | `cmd/api` | ✅ |
| `internal/llm` | 2 | `cmd/api`、`internal/agent` | ✅ 装配侧已接 |
| `internal/tools` | 5 | `agent`、`handlers`、`llm`、`seed` | ✅ |
| `internal/seed` | 1 | `cmd/api` | ✅ |

### 3.2 关键子包

| 子包 | 生产 import | 说明 |
|---|---|---|
| `api/store` | 11 | ✅ 主体数据访问层 |
| `api/sse` | 10 | ✅ |
| `api/middleware` | 6 | ✅ |
| `execution/outbox` | 4 | ✅ 生产者已接 |
| `execution/inbox` | **0** | ❌ 去重/死信链路无消费方 |
| `execution/lease` | **0** | ❌ 租约库未接（harness 内自有一份） |
| `ontology/graphstore` | 4 | ⚠️ **4 处全部来自 `ontology` 兄弟子包**（rdfs/semantic/shacl），非生产链路 |
| `ontology/rdfs` | **0** | ❌ |
| `ontology/shacl` | **0** | ❌ |
| `ontology/semantic` | **0** | ❌ |
| `ontology/cache` | **0** | ❌ |
| `platform/errors` | 84 | ✅ |
| `platform/ids` | 78 | ✅ |
| `platform/pgxutil` | 19 | ✅ |
| `platform/tenantctx` | 18 | ✅ |
| `platform/runstate` | 7 | ✅ |
| `platform/logging` | 5 | ✅ |

**直接推论**：第五篇、第十三篇、第十四篇、第八篇中凡"依赖运行时装配"的条目，**在系统层面均不可达**，即使其单测全绿。本清单对这类条目一律判 🟡（库已实现、未装配），不计 ✅。

---

## 4. 逐篇评测清单

> 每篇表格列：**#** ｜ **章节** ｜ **设计要求** ｜ **实现证据** ｜ **判定** ｜ **缺口/说明**。
> 判定后的标记为证据等级（见 §0.1）。表格后列出该篇的**范围外条目**与**❓待确认项**。

### 4.1 总纲（企业本体驱动智能体平台详细技术设计书）

| # | 章节 | 设计要求 | 实现证据 | 判定 | 缺口/说明 |
|---|---|---|---|---|---|
| 1 | §3.1-3.2 | 17 模块包边界 + 内部接口契约 | `internal/` 15 个一级包；`erp.Port`、`registry.CatalogRepository` | ✅[子] | M13 数据接入（V1.5）未实现 |
| 2 | §3.3 | `/api/v1` 前缀 + 统一错误结构带 `request_id` | `contracts/openapi.yaml` 56 path；`httperr.Response.RequestID` | ✅[码] | 实测响应确含 `request_id` |
| 3 | §3.4 | 首期 8 个关键 API | `router.go` 84 条路由，8 个均在声明表中 | 🟡[实] | **其中 `POST /ontologies/{id}/validate` 实际恒 503**（L3），`/auth/session` 实际 404（L2）——"在声明表中"不等于可达 |
| 4 | §4.1-4.3 | 基础字段 + 身份组织 + Agent/插件表 | 迁移 `000001`(`iam.*`)、`000003`(`registry.*`) | ✅[实] | 表在真实库中 |
| 5 | §4.4 | 本体 7 表（含 `ontology_evidence`） | `000004` 建 6 表；**无 `ontology_evidence`** | 🟡[子] | 制度依据（claim/locator）无落点；`publications` 无 `approval_id` |
| 6 | §4.5 | runtime/workflow/integration/audit 表组 | `000005`/`000006`/`000007` | ✅[实] | — |
| 7 | §4.6 | 任务状态与业务状态必须分离 | `platform/runstate`；`erp.OpStatusUnknown` | ✅[码] | — |
| 8 | §5.1-5.3 | 三态决策 + Obligations/PolicyVersions | `authorization/engine.go`、`dsl.go` | ✅[码] | — |
| 9 | §5.4 | 默认拒绝、禁止自我审批、发布人工审核 | `engine.go:172` 自我审批降级；`DecideApproval` | 🟡[码] | 本体发布的人工审核门禁未被强制（见 4.6 L3） |
| 10 | §5.5 | 审批绑定操作与参数摘要，变更即失效 | `workflow.approvals.payload_digest` | 🟡[子] | `INVALIDATED` 仅在 CHECK 枚举，**无写入路径** |
| 11 | §6.1-6.2 | 五类插件协议 | `000003` `plugins` CHECK 五 kind | ✅[子] | — |
| 12 | §6.3-6.5 | 结构化消息/工具协议/不可变发布 | `tools.ValidateSchema` fail-closed；`000018` 不可变守卫 | 🟡[子] | `risk_level`/超时重试未结构化；插件隔离进程未实现 |
| 13 | §7.1-7.3 | 任务 11 态 + 工具调用状态机全矩阵 | `harness/state.go` 17×31、`state_test.go` | ✅[码] | — |
| 14 | §7.4-7.5 | 10 步执行循环 + 检查点字段 | `harness/checkpoint.go`；`runtime.checkpoints` | 🟡[码] | **worker 未接执行驱动**，Run 停留 `CREATED`；checkpoint 无落库调用方 |
| 15 | §8.1-8.4 | ERP Port/信封/幂等 + Mock 10 场景 | `erp/port.go`、`mockerp` 故障注入 | ✅[子] | 故障注入实测 5 类可触发（设计 10 类） |
| 16 | §9 | OWL/RDF/SHACL + 六阶段发布流水线 | `ontology/{shacl,rdfs,graphstore}` 库 + 27 测试 | 🟡[实] | **`ValidateOntology` 恒 503**；`PublishOntology` 允许 `DRAFT→PUBLISHED` |
| 17 | §10 | 制度解析 → PolicyIR → 影响分析 | 无对应实现 | ➖[子] | 第五篇自标 `[V1.5]` |
| 18 | §11 | SSO/OIDC、TLS、密钥、插件隔离 | RLS + 审计链 + compose；**无 OIDC/SSO**、**无 TLS 终结** | 🟡[实] | `users.identity_provider` 仅列预留；第三篇 §4 要求 OIDC（见 4.4） |
| 19 | §12/§14/§15 | 阶段计划 / 建议目录 / 开发顺序 | 实现按第十六篇目录，`tests/` 为空 | ➖[实] | 背景与建议，不参与 MVP 判定 |
| 20 | §13 | L1–L5 测试分层 + 12 场景 | 52 个 `*_test.go`；`contract_test.go` 带 integration tag | 🟡[实] | §13.2 十二场景无集中用例集；`tests/` 六目录**全空** |

**范围外**：§1.2 V1.0 暂不交付清单（真实 ERP 写入、真实付款、全自动合同签署、完全自动化本体生产发布、生产级多租户 SaaS）；§2.1 Rust 高性能组件；§6.1 WASI 隔离运行时；§10 制度驱动本体生成闭环；§14 `apps//pkg//plugins/` 建议目录。
**❓待确认**：§9.4 运行期本体版本锁定的负向测试缺失（无法证明漂移会被拒）；§8.4 Mock 场景 8「审批过期/重复审批」无对应用例；§4.5 `runtime.tool_calls` 敏感参数字段级脱敏未定位到写入路径。

---

### 4.2 第一篇：工程架构与代码骨架

| # | 章节 | 设计要求 | 实现证据 | 判定 | 缺口/说明 |
|---|---|---|---|---|---|
| 1 | §4 目录结构 | 顶层结构（cmd/internal/web/contracts/migrations/…） | 根目录实测齐备；`ontology-packages/{core,finance}`、`policies/{authorization,quality-gates}` | ✅[实] | — |
| 2 | — | 根目录 `README.md` | `ls README*` → **不存在** | ⬜[实] | 第十六篇 §4.1 明确列出的顶层文件缺失 |
| 3 | §14.3 | CI 流水线（格式→静态分析→单测→迁移→契约→集成→安全） | 全仓**无任何 CI 配置文件** | ⬜[实] | 见 L8 |
| 4 | §4 | 测试分层目录 unit/integration/contract/e2e/security/fault-injection | 6 目录**均存在但全空** | 🟡[实] | `Makefile` 的 `test-integration ./tests/integration/...` 与 `test-e2e ./tests/e2e/...` **指向空目录，目标不可用** |
| 5 | §14 | `docs/` 子目录架构/决策/api/开发/运维手册 | 实测仅 `docs/{adr,contracts,research,资料}` | ⬜[实] | `architecture`/`decisions`/`api`/`development`/`runbooks` 五个均缺失 |
| 6 | §5 | 统一错误码与分层（platform/errors） | `platform/errors` 被 84 处引用；`tools/contract-lint` 7/7 | ✅[实] | — |
| 7 | §6 | 配置装载（platform/config） | `platform/config` 2 处引用；`internal/platform/config/config_test.go` | ✅[码] | — |
| 8 | §8 | 结构化日志与中间件链 | `middleware/{RequestID,Recover,Logging,CORS,RateLimit,Auth,RequireIdentity}` | ✅[实] | 链顺序与注释一致；**但 Auth 前缀遮蔽导致 L2** |
| 9 | §7 | ID 生成与租户上下文 | `platform/ids`（78 处）、`platform/tenantctx`（18 处） | ✅[实] | — |
| 10 | §10 | 连接池与租户注入工具 | `platform/pgxutil`（19 处）；`WithTenantTx` 为正确实现 | 🟡[实] | **存在第二套错误实现**（`store.setTenant`），见 L1 |

**范围外**：§14.3 与第十六篇 §4.1 的目录结构两稿互相冲突（`apps//pkg//plugins//schemas//deploy/` vs `cmd//internal//mockerp/`），实现按第十六篇。
**❓待确认**：`tests/` 六目录是"有意废弃"还是"待补"；`deployments/scripts/` 为空与 §14.1 要求交付"部署手册/备份恢复说明"如何对齐。

---

### 4.3 第二篇：核心数据模型与 PostgreSQL Schema

| # | 章节 | 设计要求 | 实现证据 | 判定 | 缺口/说明 |
|---|---|---|---|---|---|
| 1 | §2.2 | `iam.tenants` / `iam.organizations` / `iam.users` / `iam.memberships` | `000001_create_iam.sql` | ✅[实] | 实测表全在 |
| 2 | §2.2 | 组织闭包 `iam.org_closure` 防环、移动重算 | `000001:101`；`identity/org.go:137,268` | 🟡[实] | **仅应用层维护，无 DB 触发器**；绕过应用直写会导致闭包不一致 |
| 3 | §3.1 | `governance.{roles,permissions,role_permissions}` | `000002` | ✅[实] | — |
| 4 | §3.1 | `governance.role_bindings` scope 约束 | `000002:40` | 🟡[子] | CHECK 由 2 类**扩为 6 类** scope，并增 `scope_resource_refs` |
| 5 | §3.2 | `governance.policy_versions` 五态 + UNIQUE | `000002:81` | ✅[子] | 增 `policy_language` CHECK |
| 6 | §4.1 | `registry.plugins` 四类 kind + 租户唯一键 | `000003:9` | 🟡[子] | kind **扩为 5 值**（+`MODEL_PROVIDER`）；`tenant_id` 可空改为部分唯一索引 |
| 7 | §4.1 | `registry.plugin_versions` 9 态 + 不可变 | `000003:35`；触发器 `trg_plugin_versions_immutable` | ✅[实] | 实测触发器存在（仅 2 个非内部触发器之一） |
| 8 | §4.2-4.3 | `agents`/`agent_versions`/`tools`/`tool_versions` | `000003:227-311` | ✅[实] | `tool_versions` 增 `effect_class` |
| 9 | §5.1-5.3 | `ontology.{ontologies,versions,artifacts,dependencies}` | `000004:6-97` | ✅[子] | 复合外键与环检测 CHECK 齐 |
| 10 | §5.4-5.5 | `ontology.validation_runs` / `ontology.publications` | `000004:99-146` | ✅[实] | publications 含 `rollback_from_publication_id`（但**零写入**，见 4.6） |
| 11 | §6.1 | `runtime.agent_runs` 17 态 + 租约三件套 + 索引 | `000005:7-74`；`000013` 修正队列索引 | 🟡[子] | 队列索引被 `000013` 扩为含 `CONTEXT_BUILDING/RUNNING`，偏离 §6.1 字面 |
| 12 | §6.2-6.4 | `run_steps` / `tool_calls` / `authorization_decisions` / `checkpoints` | `000005:101-233` | ✅[实] | — |
| 13 | §7.1-7.3 | `workflow.approvals` / `approval_actions`；`integration.erp_*` | `000006:6-114` | ✅[实] | — |
| 14 | §8.1 | `audit.audit_events` 哈希链列 + UNIQUE + 三索引 + **按 `occurred_at` 分区** | `000007:7-55`；触发器 `trg_audit_events_append_only` | 🟡[实] | **未分区**（`000007` 建普通表，无 `PARTITION BY`）；**缺 `prev_segment_root` 列**（仅 `prev_digest`）；触发器与 REVOKE 双保险到位 ✅ |
| 15 | §8.2 | `audit.evidence_records` 保留父表 FK + run 索引 | `000007:57-79`；`000011:175` REVOKE UPDATE/DELETE | 🟡[实] | **无 append-only 触发器**（实测非内部触发器仅 `audit_events` 与 `plugin_versions` 两个）——见 L5 |
| 16 | §9 | 高价值表 RLS 逐表覆盖 + FORCE + 应用角色 NOBYPASSRLS | `000011`/`000014`/`000016` | ✅[实] | 实测 `pg_class` 查询确认大量表 `relrowsecurity=t, relforcerowsecurity=t`；`odap_app` 非超级用户 |
| 17 | §9 | RLS 的租户 GUC 契约 | `app.current_tenant_id()` 读 `app.tenant_id` | 🟡[实] | **两套注入实现不一致**，见 L1 |
| 18 | §10.4 | `task_outbox` 与业务同事务 | `000008:6-65` | ✅[子] | 另有 `consumer_inbox*`/`event_dead_letters`（第十篇） |
| 19 | §1.2 | `system` schema | `system` 命名空间存在（由 Go 创建）；**无迁移文件、无 `system` 前缀表** | ⬜[实] | 设计要求的系统配置/后台任务表未落地 |
| 20 | §12.3 | 分区策略（"MVP 不必过早分区"的取舍） | 仅 `graph.triples` 与 `execution.consumer_inbox*` 分区 | 🟡[实] | `audit_events` 未分区与 §8.1 要点冲突，需裁定 |

**范围外**：`graph`/`planning`/`evaluation`/`llm`/`simulation`/`release_mgmt` 各表来自第十三/十四/十五/九篇，非第二篇定义。
**❓待确认**：① `audit_events` 分区与 `prev_segment_root` 是"刻意推迟"还是"遗漏"（§12.3 与 §8.1 相互矛盾）；② `evidence_records` 无触发器是否可接受（owner/超级用户可改）；③ `role_bindings` 6 类 scope、`plugin_kind` 5 值、队列索引状态集三处扩展是否需回写设计书。

---

### 4.4 第三篇：企业身份认证与权限引擎

| # | 章节 | 设计要求 | 实现证据 | 判定 | 缺口/说明 |
|---|---|---|---|---|---|
| 1 | §4.1-4.3 | OIDC Authorization Code + PKCE（`GET /auth/login`、`/auth/callback`） | **无 `internal/identity/oidc`**；仅本地密码 `auth_handlers.go:Login` | ⬜[实] | 无 iss/aud/nonce/签名校验；设计**未标** `[V1.5]`，属范围内缺口 |
| 2 | §4.3 | register / login / logout / session + identity/me + switch-organization | `router.go:37-41` 声明 4 条 | 🟡[实] | **logout 与 session 实际 404**（L2）；`identity/me`、`switch-organization` 未实现 |
| 3 | §4.4 | Web 会话安全：HttpOnly/Secure/SameSite、CSRF、登录后重生成会话 ID | `auth_handlers.go:issueSession` 用 **body 返回 `session_token`**；`middleware.SessionCookie` 仅定义未 `Set-Cookie` | 🟡[子] | 无 Cookie 属性、无 CSRF；前端存 token |
| 4 | §4.5 | 独立 `iam.external_identities`(issuer+subject) | `000001` 内联 `users.identity_provider/external_subject` | 🟡[子] | 承载方式偏离；无 OIDC 绑定写入路径 |
| 5 | §4.6 | 会话存储 / refresh 轮换 / subject_version 同步失效 | `identity/session.go` `Issue/Validate/RotateRefresh`；`user.go:DisableUser` 递增版本；测试 `TestSessionIssueStoresHashOnly` 等 | ✅[码] | refresh 仅存 SHA-256；重放撤销全部会话 |
| 6 | §5.1 | 权限命名 `<domain>.<resource>.<action>` | `000012` 种子 `governance.permissions` | ✅[子] | identity.*/ontology.* 示例未全量种子 |
| 7 | §5.3 | 六种 scope + `org_closure` depth=0 + ORG_SUBTREE 展开 | `000002` CHECK 六种；`org.go:Create/Move`；`engine.go:scopeAllowed`；测试 `TestOrgCreateBuildsClosure` 等 | ✅[码] | 防自环/循环 |
| 8 | §6.3 | JSON DSL V1：字段/算子白名单、版本锁定、规模上限 | `dsl.go:Validate/ParsePolicy`（`DisallowUnknownFields`、`allowedOps`、MaxRules） | 🟡[码] | 算子缺 `gt/lt/not_exists`；`policy_language` 枚举与设计不一致 |
| 9 | §7.1 | 三态决策 ALLOW/DENY/REQUIRE_APPROVAL | `dsl.go` 三常量；`engine.go:Decision` | 🟡[码] | `Decision` 缺 `DecisionID`/`ApprovalRuleID` 字段 |
| 10 | §7.3 | 优先级 DENY > REQUIRE_APPROVAL > ALLOW，无命中默认拒绝 | `engine.go:evaluate`；测试 `TestDecidePriorityOrderAndPolicyVersions` | ✅[码] | — |
| 11 | §7.4 | Authorize / Recheck 统一接口（执行前重核） | `engine.go:Decide`；**无 Recheck 方法** | 🟡[子] | 复核散落在 harness 状态机与 erp gateway |
| 12 | §8 | fail-closed：评估故障不降级、ERROR 不落三态 | `engine.go` 异常上抛；`store.go:Record` 仅落三态；测试 `TestDecideFailClosedOnEvaluationError` | ✅[码] | — |
| 13 | §8.1 | 理由码（ALLOW_ROLE_SCOPE / DENY_* …） | `engine.go` 用 `NO_MATCHING_RULE` 等自造名 | 🟡[子] | 未采用 §16.2 保留的 10 个理由码命名 |
| 14 | §9 | Harness / ERP 层强制鉴权（R0-R4、审批） | `agent/executor.go:382` 调 `Authz.Decide`；`erp/gateway.go:283` REQUIRE_APPROVAL→WAITING_APPROVAL | ✅[码] | 但 `internal/agent` **零装配**（§3.1）→ 实质上未生效 |
| 15 | §10 | 职责分离：禁止自我审批 | `engine.go:173`；`handlers/approvals.go`；测试 `TestDecideSelfApprovalDowngrade` | 🟡[码] | 无审批规则版本化；摘要绑定仅 erp 侧 |
| 16 | §14.1 | PEP 中间件 `RequirePermission(resource, action)` 逐请求鉴权 | `middleware/auth.go` 仅 `Auth`/`RequireIdentity`；**无 `RequirePermission`** | 🟡[实] | API 入口**未做资源+动作级授权**，与 §2.2「所有受保护操作经 PEP」不符 |
| 17 | RLS + GUC | `app.tenant_id` 注入与 RLS 协同 | `middleware/tenant.go:56`（正确）与 `store/auth_store.go:267`（错误）并存 | 🟡[实] | **见 L1**：两个不同 GUC + `SET` 不接受占位符 |
| 18 | §14 | 管理端点（roles / role-bindings / users / orgs 管理） | `router.go:120-142`；`auth_store.go:BindRole/UnbindRole` | 🟡[实] | `/admin/users`、`/admin/organizations` **实测 500**；角色↔权限映射**只读**（无写端点）；`admin/subjects/{id}/disable` 未登记 |
| 19 | §16.1 | 决策留痕表 `runtime.authorization_decisions` | 表存在；`store.go:Record/GetByDecisionID`；测试 `TestDecisionStoreRecordThreeEffects` | 🟡[子] | **生产未接线**（`Record` 仅测试调用）→ 运行期决策不落库 |
| 20 | §4.4/§5 | 服务账号 / 主体停用 / 凭证策略 | `session.go` 支持 SERVICE；`Validate` 校验 revoked/expires/status；`DisableUser` 递增版本 | 🟡[子] | 无 API Key 签发；`iam.credentials.failed_attempts/locked_until` 仅建表未强制 |

**范围外**：§6.3 OPA/Rego 适配器、§4.6 撤销传播 SLA 看板（`[V1.5]`）；§14 `authorization/explain`、服务间 mTLS（P5）；Obligations 附加义务框架（P4）；说明 API/可视化策略编辑器（`V2`）；§16.2 延后 7 个理由码。
**❓待确认**：① **策略发布路径缺失**——`config_crud.go:UpdatePolicyStatus` 仅接受 `ACTIVE/DISABLED/ARCHIVED`，而 `engine.LoadPublishedPolicies` 只读 `status='PUBLISHED'`，设计 §14 的 `POST /admin/policies/{id}/publish` 不存在 → **PDP 运行期是否恒无策略可加载？** ② `iam.credentials` 未纳入 RLS 覆盖清单是否有意；③ 决策留痕未接线是否符合 §16.1；④ OIDC 未标 `[V1.5]` 却整体缺失，是否属 MVP 应交付项。

---

### 4.5 第四篇：插件协议与 Registry 注册中心

| # | 章节 | 设计要求 | 实现证据 | 判定 | 缺口/说明 |
|---|---|---|---|---|---|
| 1 | §3.2 | 统一 Manifest：身份/运行时/接口/能力/依赖/权限/安全 | `manifest.go:40 Manifest` | 🟡[子] | 缺 interfaces/permissions/ontologyDependencies/security/policy 段 |
| 2 | §3.3 | Manifest 六层校验（语法/Schema/身份/依赖/安全/兼容） | `manifest.go:55 ValidateManifest`；`manifest_test.go` 4 例 | 🟡[子] | 仅语法 + 受限 Schema；**且写路径未调用它**（见 #10） |
| 3 | §4.2 | 生命周期状态机（12 态） | `service.go:48 publishTransitions`；`000003:46` CHECK 9 态 | 🟡[子] | 缺 `SUBMITTED/QUARANTINED/REVOKED`；用 `CREATED` 非 `DRAFT` |
| 4 | §4.2 | 版本 / 租户安装 / 执行实例三套状态分离 | `plugin_versions.status` + `plugin_installations.installation_state` | 🟡[子] | 执行实例态无载体（无 plugin runtime） |
| 5 | §5.2 | 五类 kind + visibility(platform/tenant) | `000003:9`；`manifest.go:30 pluginKinds` | 🟡[子] | 无 `visibility` 列，以 `tenant_id IS NULL` 派生 |
| 6 | §5.3 | 已发布版本不可变（触发器 + 列级权限） | `000003:326` 触发器；`000018` 修复 `RETURN NULL` | 🟡[实] | 触发器 ✅（实测存在）；**列级 REVOKE/GRANT 未实现** |
| 7 | §7.2/§5.4 | 依赖声明 → 发布期解析锁定清单 | `registry.plugin_dependencies` 表 | ⬜[实] | **Go 零命中**；无 Resolver、无锁定清单落库 |
| 8 | §5.4 | 制品表 + 受控对象存储（`storage_uri`） | `plugin_artifacts` 表；compose `objectstore`(MinIO) profile | ⬜[实] | **表 Go 零命中**；`artifact_uri` 恒 NULL；无代码消费 MinIO |
| 9 | §5.5 | 安装/升级/回滚保留历史、单 ACTIVE | `service.go:691 InstallVersion`、`:779 SwitchActiveInstallation`；`000003:120` 部分唯一索引；`install_test.go` 10 例 | ✅[码] | 服务层完整；**但 HTTP 未走它**（见 #10） |
| 10 | §9.1-9.3 | `/registry/plugins` + versions/validate/review/publish/install | `router.go:69` 仅 5 条：`GET/POST /plugins`、`GET/PATCH/DELETE /plugins/{id}`、`POST /plugins/{id}/enable` | 🟡[实] | **`registry` 服务层零装配**（§3.1）；HTTP 写路径走 `store/catalog.go` 直写 SQL，**绕开 `ValidateManifest` 与 `UPGRADING→兼容校验→切换` 状态机** |
| 11 | §5.5 | `plugin_validation_runs` 验证留痕 | `000003:124` | ⬜[实] | Go 零命中 |
| 12 | §5.5 | `plugin_reviews` 审核决定（追加式） | `000003:139` | ⬜[实] | Go 零命中；`service.go:253 Publish` 直接跨级，无审核记录 |
| 13 | §5.6 | `plugin_capability_declarations` 能力声明 | `000003:151` | ⬜[实] | Go 零命中 |
| 14 | §5.6/§8.4 | `plugin_signatures` 签名验签 | `000003:172`；`service.go:89 SignatureRef` | ⬜[实] | Go 零命中；`SignatureRef` 恒空；无 `SignatureVerifier` |
| 15 | §5.6 | `plugin_publications` 发布记录/环境 | `000003:201` | ⬜[实] | Go 零命中，无写入 |
| 16 | §8.4/§10.2 | 制品摘要校验（重算比对） | `catalog.go:18 digestBytes`；`service.go:210` 同版本异摘要→CONFLICT | 🟡[子] | 比对 ✅；但**不重算实际制品**（信任入参），无 `ArtifactStore.Put/Open` |
| 17 | §9.4 | capabilities/dependencies/compatibility/audit/health 查询 | 无 | ⬜[子] | 5 个解释性端点全缺 |
| 18 | §10.2 | PluginRepository / ArtifactStore / ManifestValidator / SignatureVerifier | `service.go:111 PluginRepository` | 🟡[子] | 仅 PluginRepository；后三者缺失 |
| 19 | §10.4 | 状态迁移用条件更新（expected 旧态） | `service.go:253 Publish` 先 SELECT 再 UPDATE（仅按 id） | 🟡[子] | 非 `UPDATE…WHERE status=expected`；并发防护靠单写者假设 |
| 20 | §6/§14 | Plugin Protocol v1（invoke/cancel/health、Envelope、9 域码） | `tools.go:93 InvokeTool` 复用 `tools.ValidateArgs` 后**诚实 503** | ⬜[子] | 无 `/v1/invoke` 协议服务端、无 Envelope/取消/错误码 |
| 21 | §11.1 | Harness 固定已发布版本（Resolver） | `agent.go:96 ResolveAgentVersion`、`tool.go:15 ResolveToolVersion`；`catalog_test.go` | 🟡[子] | 解析器 ✅，但 **`registry` 服务层未装配**（§3.1） |

**本节新增的"零引用表"结论（矩阵 §6 未登记）**：`plugin_artifacts`、`plugin_capability_declarations`、`plugin_dependencies`、`plugin_publications`、`plugin_reviews`、`plugin_signatures`、`plugin_validation_runs` —— **7 张表 Go 代码零命中**（实测 `grep` 计数全为 0），仅出现在迁移与设计文档中。
**范围外**：§8.1 WASI 沙箱 `[V2]`；§8.1 容器 Runner 受控接入 `[V1.5]`；§15 阶段三安全扫描 `[V1.5]`；私有插件仓库与组织级治理 `[V2]`。
**❓待确认**：Manifest `runtime.type` 白名单为 `wasm`（设计 §3.2 为 `wasi`）；ADR-002 称 `plugins` 全局唯一而实现为 `UNIQUE(tenant_id,plugin_key)` + 平台级部分唯一索引；`registry.plugin_installations/agents/tools` 的 RLS 策略是否已建（`catalog_test.go` 走内存 fake，未验证真实隔离）。

---

### 4.6 第五篇：Ontology Runtime 与 Ontology Factory

| # | 章节 | 设计要求 | 实现证据 | 判定 | 缺口/说明 |
|---|---|---|---|---|---|
| 1 | §5.1 | 本体身份 + 不可变版本 | `store/ontology_policy.go:72 CreateOntology`；`ontology.{ontologies,ontology_versions}` | 🟡[子] | 设计 `packages/imports` 未落地，改用 `ontologies`；无版本内容编辑 API |
| 2 | §5.2 | 8 态生命周期 + 合法转换 | `000004:29-34` CHECK 8 态；`PublishOntology:137` 条件 UPDATE | 🟡[码] | 枚举≠设计（无 `REVOKED/STAGED`）；`status IN('VALID','APPROVED','DRAFT')` **允许 DRAFT→PUBLISHED**，违反转换表 |
| 3 | §5.2.1 | rejected 吸收态 / revoked 条件恢复 | 无代码 | ⬜[子] | 无状态机执行器，无回退/双签路径 |
| 4 | §5.2/§11.2 | 版本回滚 | **全仓无 rollback 路由/方法**；`000004:134 rollback_from_publication_id` **零写入** | ⬜[实] | 该列从未被 INSERT/UPDATE |
| 5 | §5.3 | validation_runs + findings | `validation_runs` 表在；写仅 `seed/apply.go:602`；**`validation_findings` 表不存在** | 🟡[实] | 运行时无写入（validate 恒 503）；findings 表缺 |
| 6 | §5.1 | artifacts 制品 | `ontology_artifacts` 表；写仅 `seed/apply.go:580` | 🟡[实] | 仅 seed 写；无 API 上传/查询路径 |
| 7 | §5.1/§11.1 | 依赖解析与锁定 | `ontology_dependencies` 表**Go 零引用**；无 imports 表 | ⬜[实] | 无闭包/环检测/锁版本代码 |
| 8 | §11.2 | 发布流程 + 发布记录 | `PublishOntology`（单事务：撤销旧发布 + 插 `ontology.publications`）；路由 `POST /ontologies/{id}/publish` | ✅[码] | **但无前置验证/审核**，可绕过 §11.1（见总纲 §9 与 L3） |
| 9 | §13.7 | 管理 API 全集 | `router.go` 注册 10 条 | 🟡[实] | §13.7 的 `/api/v1/ontology/*`（packages/versions/diff/dependencies/query/validate/evidence）**全部未注册** |
| 10 | §8.3 | 加载与缓存（LRU+TTL+版本键+权限投影） | `ontology/cache/cache.go`；测试 `TestVersionAdvanceInvalidates` 等 | 🟡[实] | **库零装配**（§3.2） |
| 11 | §8.4 | 受控语义查询 | `ontology/semantic/query.go Execute`（编译 Clause→`GraphStore.Match`） | 🟡[实] | 引擎真实，但**无路由**、库零装配 |
| 12 | §8.1/§8.6 | RDFS 推理 + SHACL 校验**真执行** | `rdfs/infer.go InferContext`（半朴素迭代 + `store.InsertTriples` 物化）；`shacl/validate.go`；测试 `TestInferSubClassTransitiveClosure` | 🟡[实] | **真推理非仅语法校验**，但仅测试驱动；`ValidateOntology` 恒 503 |
| 13 | §8.5 | 查询安全边界（IRI/变量白名单、Limit 上限） | `semantic/compileQuery`；`shacl` 对 `sh:sparql` fail-closed | ✅[码] | 库内达标（仅库内） |
| 14 | §8.7 | PASS/FAIL/UNKNOWN 验证状态 | 无代码 | ⬜[子] | — |
| 15 | §9.2/§9.3 | assertion_type / Evidence Bundle | 无代码、无表 | ⬜[子] | — |
| 16 | §6 | Factory 8 阶段 + 断点续跑 | 无代码；`factory_*` 表不存在 | ⬜[子] | 见 ❓①（范围归属争议） |
| 17 | §7 | Policy IR 白名单/编译 | 无代码 | ⬜[子] | — |
| 18 | 分类 | `ontology.categories` 多层级 | `store/ontology_category_store.go`、`handlers/ontology_category_handlers.go`、`000021` | 🟡[实] | 全 CRUD 已写；**但端点实测 500**（L1） |
| 19 | §4/§8.2 | 三元组图存储 | `graphstore/store.go PGStore`；`000009`；`TestPGStoreImplementsGraphStore` | 🟡[实] | **PGStore 未在 API 装配**；该测试仅编译期接口断言 |
| 20 | §4.2 | 版本不可变 | `seed/apply.go:566` 终态保护 | 🟡[子] | 无 DB 层不可变触发器 |

**范围外**：SPARQL 受限开放 `[V1.5]`（现以自研 DSL 替代，合规）；Factory 并行阶段调度 `[V1.5]`；可视化本体编辑器高级特性 `[V2]`。
**❓待确认**：① **Factory scope 冲突**——设计书篇首标 §6「8 阶段 Factory 流水线」为 `[MVP-V1.0]`，而自查矩阵 §6 却记"本体工程自标 `[V1.5]`"。若以设计书为准，Factory 属**范围内未实现**；② 设计书正文未出现"版本回滚端点"字样，仅 `publications` 表预留列——"要求回滚"是否源自他篇；③ `GET /api/v1/ontologies/{id}` 现已在 `router.go:62` 与 `config_crud.go:116` 补齐，矩阵 §6 的缺陷 #20 条目**已过时**。

---

### 4.7 第六篇：Harness 执行引擎与状态机

| # | 章节 | 设计要求 | 实现证据 | 判定 | 缺口/说明 |
|---|---|---|---|---|---|
| 1 | §3.1 | Run 17 态词表唯一真源 | `platform/runstate/runstate.go:All`(17)；`runstate_test.go:TestAllSeventeenStates`；`000005` CHECK 17 值 | ✅[码] | — |
| 2 | §3.3/§14.1 | 事件清单 = 常量集 = 矩阵覆盖 | `events.go:AllEvents`(31)；`state_test.go:TestAllEventsVocabulary` | 🟡[子] | 事件名与设计有差异：缺 `PlanRegenerationRequested`/`ApprovalRejected`/`ApprovalExpired`/`InputDeadlineExceeded`；新增 `LeaseAcquired`/`LeaseLost`/`DataRefreshed` |
| 3 | §14.2 | 17×31 转换矩阵表驱动、逐格覆盖 | `state.go:rules`（实测 **108 条合法转换行**）；`machine.go:NextState`；`TestTransitionTableConsistency` | ✅[码] | 实现方式为"**合法格显式枚举 + 非法格默认拒绝**"，非逐格全枚举（527 格）——语义等价 |
| 4 | §14.2 | 守卫 facts 驱动条件转换 | `facts.go:TransitionFacts`(8 字段) + 9 个 `resolve*`；`TestGuardRejections` | 🟡[子] | 缺 `AllStepsTerminal`/`HasUnknownToolCalls`/`OutputContractValidated`/`ResumeState` |
| 5 | §14.3 | Application Service 为统一状态写入口 | `machine.go:Machine.Apply`(Get→NextState→CAS) | ✅[码] | 生产未在同一事务写 run_events/Outbox |
| 6 | §6.3 | fencing / lease_epoch 拒旧 Worker 写 | `machine.go:pgRunStore.Apply`(FOR UPDATE + 条件 UPDATE)；`errStaleSnapshot`→`CodeExecLeaseLost`；`TestApplyFencingRejectsStaleWorker` | ✅[码] | — |
| 7 | §6.2 | 四队列原子领取（SKIP LOCKED） | `scheduler.go:queueFor/ClaimNext/claimSQL`；`000013` 部分索引 | ✅[码] | — |
| 8 | §6.2 | 租约续期接口 | `scheduler.go:RenewLease`；`POST /runs/{id}/lease/renew` | ✅[实] | 端点实测可达 |
| 9 | §14.2.3 | 租约过期扫描 → RECOVERING | `scheduler.go:ReleaseExpiredLeases`；`000013:idx_agent_runs_lease_expiry` | 🟡[子] | I1–I6 层间不变量巡检未实现 |
| 10 | §14.2 | `LeaseAcquired` 自环簿记 | `state.go:leaseRules`(7 可租用态 from=to)；`cmd/worker:advanceRun` | ✅[码] | 与矩阵 §6 结论一致，非缺陷 |
| 11 | §8 | checkpoint 构建与校验 | `checkpoint.go:BuildCheckpoint/VerifyCheckpoint`；`runtime.checkpoints`；`checkpoint_test.go` | 🟡[子] | 构建为纯函数，**无持久化 repository / 落库调用方** |
| 12 | §7.3/§8.3 | 依赖漂移检测（拒旧版本） | `checkpoint.go:VerifyCheckpoint`；`TestVerifyCheckpointDriftAndTamper` | ✅[码] | — |
| 13 | §13.1 | Run 表状态机/调度字段 | `000005`：`lease_owner/lease_epoch/lease_expires_at/available_at/cancel_requested_at/deadline_at` | 🟡[实] | 缺 `state_version`/`execution_manifest_digest`/`plan_digest`；CAS 用 (status, lease_epoch) |
| 14 | §13.2 | `runtime.run_events` 独立事件表 | **迁移中无 `run_events`**；事件经 `execution.task_outbox` + audit | ⬜[实] | 无 `(run_id, sequence_no)` 唯一约束与事件追加服务 |
| 15 | §13.3 | 状态 + 事件 + Outbox 同事务原子 | `machine.go:Apply` 单事务仅更新 `agent_runs`；`handlers/runs.go:appendOutbox` 另行追加 | 🟡[码] | **三者未合并进同一事务** |
| 16 | §15 | Run 对外 API 集 | `router.go`：`POST/GET /runs`、`GET /runs/{id}`、`/cancel`、`/reconcile`、`/lease/renew`、`/tasks/{id}`、`/stream` | 🟡[实] | 缺 pause/resume/retry/input/events/steps/evidence 端点 |
| 17 | §15 | 客户端幂等键（同键返原任务） | `handlers/runs.go:CreateRun` 仅 `echoIdempotency`；无幂等唯一约束 | ⬜[实] | **Run 级幂等未实现**（仅 `tool_calls.idempotency_key` 有 UNIQUE） |
| 18 | §12/§16.5 | ERP 对账 + 账本 + 码语义映射 | `erp/gateway.go:Prepare/Dispatch/Reconcile`；`integration.erp_operations` | ✅[码] | 未接入 worker 执行链 |
| 19 | §4.3/§16.5 | `ERP_OPERATION_UNKNOWN` → `EXECUTION_UNKNOWN` | `errors.go`(BaseCode=EXECUTION_UNKNOWN)；`erp/port.go:StatusUnknown` | ✅[码] | — |
| 20 | §6.1 | **执行驱动（步骤/工具编排）** | `agent/executor.go:Executor.ExecuteNode`→`Machine.Apply`；**仅 `executor_test.go` 调用** | 🟡[实] | **`internal/agent` 零装配**（§3.1）；worker 只发 `LeaseAcquired`，Run 停留 `CREATED` |
| 21 | §5 | Plan Compiler / Validator | `agent/planner.go:Propose/ValidatePlan`；`agent/planir.go` | 🟡[实] | 无 harness Plan Compiler 编排，未走 `PLANNING→RUNNING` |
| 22 | §7 | Context Builder / Execution Manifest | 列 `context_manifest`；`agent_run_dependencies`；`store/runs.go:ListRunDependencies` | 🟡[子] | 无 ContextBuilder/Manifest 生成组件 |

**范围外**：§18.2 可观测性指标/Trace（`[V1.5]`）；§17.3 插件容器/seccomp 沙箱（MVP 仅容器隔离）；§2.1/§20.2 详细目录分层（功能等价，非缺陷）。
**❓待确认**：① worker 是否**刻意**停留在调度骨架（矩阵 §6 记为已知缺口 🟡，而非缺陷）；② `run_events` 是否被有意以 `task_outbox` 替代（设计 §13.2 显式要求独立表，故本清单判 ⬜）；③ Execution Manifest 是否即 `context_manifest` + `agent_run_dependencies`；④ 事件名漂移（`events.go` 自称权威落地，但未回写第六篇 §3.3/§14.1）属**设计-实现双向漂移**，需契约侧裁决。

---

### 4.8 第七篇：ERP Port、Adapter 与 Mock ERP

| # | 章节 | 设计要求 | 实现证据 | 判定 | 缺口/说明 |
|---|---|---|---|---|---|
| 1 | §3.1 | 统一 Port 六方法契约 | `erp/port.go` `type Port interface`（Capabilities/GetObject/QueryObjects/Execute/GetOperation/Health） | ✅[码] | — |
| 2 | §3.2 | 受控查询 DSL + 算子白名单 | `port.go FilterExpression/filterOps/Validate` | ✅[码] | mockerp 侧仅落地 eq/contains |
| 3 | §3.3 | ExecuteRequest 全字段 | `port.go ExecuteRequest`（PayloadDigest/IdempotencyKey/ExpectedVersion/Authz/Approval/Deadline/TraceID） | 🟡[子] | `Deadline` 已定义但 `Dispatch` **从不赋值** → 超时预算未向 Adapter 传递 |
| 4 | §4.2 | 能力发现与声明 | `capabilities.go`；`adapter.go Capabilities`；`GET /erp/capabilities`；`capabilities_test.go` | ✅[实] | 端点实测 200 |
| 5 | §4.3 | `erp.connections` 等 6 张连接表 | **无** | ⬜[实] | 无连接注册表与迁移 |
| 6 | §5 | Adapter 六层分层 + Manifest | `erp/adapters/mock/adapter.go`（HTTPAdapter/InProcessAdapter） | 🟡[子] | 无 manifest/configSchema/registry；无真实 ERP 适配器 |
| 7 | §6 | 字段映射 Mapping Registry | **无** | ⬜[子] | 枚举/金额/时间规范化与版本化映射未实现 |
| 8 | §7.1-7.3 | Gateway 写入链 + 账本 + 非伪事务 | `gateway.go Prepare/Dispatch/Reconcile`；`store.go Insert`；短事务三段式 | ✅[码] | — |
| 9 | §8.1 | 五元组幂等键（排除 operation_id） | `idempotency.go IntentParts/IntentDigest/IdempotencyKey`；`idempotency_test.go` | ✅[码] | — |
| 10 | §8.2 | 本地唯一约束 + 冲突处置 | `000006` `UNIQUE(tenant_id, external_system, idempotency_key)`；`resolveExisting` | ✅[码] | — |
| 11 | §8.3 | L1/L2/L3 重试分级 | `capabilities.go CanAutoRetry`；`TestL3UnknownBlocksAutoRedispatch`/`TestL1UnknownAllowsRedispatch` | ✅[码] | 无 L2 端到端用例 |
| 12 | §8.4 | 并发防覆盖（条件更新） | `store.go UpdateStatus`（From 集合条件 UPDATE） | 🟡[子] | 无"两 Worker 并发"(T09) 专项测试 |
| 13 | §9.2 | Mock ERP 独立服务 | `cmd/mock-erp/main.go`；`mockerp/` 分层；compose `mockerp` | ✅[实] | 容器实测 running |
| 14 | §9.4 | 独立库 + 双账本 | `mockerp/migrations/000001_init.sql`；compose `mockerp-db`/`migrate-mockerp` | 🟡[实] | **服务用 `repository.NewMemory()`，未接 PG**；独立库已起却未被使用 |
| 15 | §9.4/9.5 | `erp.*` 业务表 | 同一迁移建 `erp.{employees,departments,expense_claims,operation_log}` | 🟡[实] | 缺 expense_items/receipts/idempotency_records/audit_events/fault_profiles；schema 名 `erp` 非设计 `mock_erp` |
| 16 | §10 | Mock ERP API（10 路由） | `mockerp/internal/api/handler.go`：`/mock/{objects,operations,operations/by-idempotency-key,health,faults}` | 🟡[子] | 通用化实现；无 `/expense-claims/{id}/submit/approve/reject`、`/receipts` 专属路径 |
| 17 | §11.1 | 故障注入（10 类） | `service.go` `FaultTimeout/Reject/Unknown/Duplicate/Delay` + `matchFault`；`POST /mock/faults` | 🟡[子] | **实测 5 类可触发**；缺 RATE_LIMIT/REMOTE_UNAVAILABLE/RECEIPT_DELAY/RECEIPT_MISMATCH/FAIL_BEFORE_COMMIT |
| 18 | §11.3 | 故障注入仅测试环境 | **无环境门禁** | ⬜[子] | `/mock/faults` 无环境判断/管理员权限/审计 |
| 19 | §13.2/13.4 | 凭据管理 + 回执核验 | 无 secret ref；`Dispatch` 直接持久化 `res` | 🟡[子] | 无凭据/鉴权；回执未比对 operation_id/目标/摘要一致性 |
| 20 | §14.7 | ERP 管理 API | 仅 capabilities ✅；`handlers/erp.go:ReconcileEROperation` **恒 503**；`cmd/api` 未装配 Gateway | 🟡[子] | 连接/映射/账本列表/回执/fault-profiles/reset 端点缺失；reconcile **只有单笔、无批量** |

**范围外**：第七篇正文**未出现** `[V1.5]`/`V2` 自标。按 §19 路线图属后续阶段者：真实 ERP Adapter（SAP/Oracle/用友）、§12 私有化内网 Connector、§14 ERP 集成中心前端六工作区——三者非 MVP 硬要求，但也不属"已实现"。
**❓待确认**：① `mockerp` 是否有 PG Repository 实现计划（迁移 SQL 已就绪、compose 已配 `MOCKERP_DATABASE_URL`，服务却走内存）；② 缺失的 5 类故障是否在他篇标为 `[V1.5]`；③ 矩阵 §6 已说明宿主端口 `5433` 被占用、经 `MOCKERP_DB_HOST_PORT` 覆盖为 `5443`——属本机环境偏差，建议回写设计书 §8.2。

---

### 4.9 第八篇：Agent Runtime 与多智能体协作

| # | 章节 | 设计要求 | 实现证据 | 判定 | 缺口/说明 |
|---|---|---|---|---|---|
| 1 | §4 | Agent Manifest(YAML) + Schema 双重验证，声明 role/capabilities/bindings | `store/catalog.go:131 CreateAgent` 仅写 `agent_key/domain` + 输入输出 schema | 🟡[子] | 无 Manifest 解析；无 capabilities/bindings/security 字段落库 |
| 2 | §4.2 | 版本约束解析为确切版本并写 Run 快照 | `runtime.agent_run_dependencies`(8 类 dep_type CHECK) | 🟡[实] | 表在，**Go 代码零 INSERT**（仅 `runs.go:296` 被读） |
| 3 | §5.1 | 生命周期 9 态（DRAFT…REVOKED） | `agent_versions.status`(6 值) | 🟡[实] | 枚举与设计**不一致**（缺 ACTIVE/SUSPENDED/RETIRED/REVOKED）；`contracts/01` 未收录 |
| 4 | §5.1 | 状态变更经服务端命令 + 审计 + 并发控制 | `catalog.go:190 PublishAgent`；`agents.go:111 recordAudit` | 🟡[子] | 仅 publish 一条命令；无 activate/suspend/revoke/retire |
| 5 | §5.2 | 发布流程与运行流程分离 | `registry.agent_versions`(发布) vs `runtime.agent_runs.agent_version_id`(FK) | ✅[码] | 表结构支持；运行侧未驱动 |
| 6 | §6.1 | AgentContext/RunIdentity/PrincipalContext/StructuredInput/TokenUsage | **全仓无这些符号** | ⬜[实] | 设计 §6.1 的 Go 结构未落地 |
| 7 | §6.2 | 六阶段协议（Prepare/BuildContext/Invoke/Validate/RecordEvidence/Return） | `executor.go:177 ExecuteNode`（授权→只读/写→推进→`ValidateArgs`） | 🟡[子] | 缺 BuildContext/RecordEvidence；`LLM_CALL` 无分支 |
| 8 | §7.2 | Execution Plan 可校验可持久化 IR | `planir.go:137` `Plan/Node/ValidatePlan`，13 类节点 + 环检测 | ✅[码] | — |
| 9 | §7.3 | 独立 Plan Compiler（类型/数据流/版本绑定/freeze） | **全仓无 PlanCompiler/CompilePlan**；`planning.plans` 表在 | ⬜[实] | ADR-001 定为唯一编译权威，但未实现 |
| 10 | §7.4 | Orchestrator Compile/Start/Resume/Cancel + DAG 调度 | **无 `Orchestrator` 类型**；`cmd/` 无 orchestrator 进程 | ⬜[实] | 调度器整体缺失 |
| 11 | §8.1/8.2 | 消息封装 `odap-agent-message-v1` + 8 类消息 + 去重 | 无消息结构体/消息类型常量 | ⬜[子] | 仅 `planir.go:205 CheckDelegationDepth`（纯函数，无调用方）呼应深度 |
| 12 | §8.3 | 冲突不静默覆盖（INCOMPLETE/CONFLICT/人工） | 无冲突聚合实现 | ⬜[子] | 无 `orchestration.conflicts` 表 |
| 13 | §9 | Context Builder 七层 + 认识状态（KNOWN_TRUE 等） | 无 ContextBuilder | ⬜[子] | claim/epistemic_status 结构全缺 |
| 14 | §9.4 | Prompt Injection 防护（数据与指令分离） | 无对应实现 | ⬜[子] | 设计未标 `[V1.5]` |
| 15 | §11.1 | 三级预算（Agent/Run/Tenant）原子扣减 | `llm/gateway.go:136,159` 仅租户级并发 | 🟡[子] | 无 Agent/Run 级预算 |
| 16 | §11.4/11.5 | 分类重试 + 取消/恢复 | 四队列 `ClaimNext`/`ReleaseExpiredLeases` + `CancelRun/ReconcileRun` | ✅[码] | 调度/租约/取消链在（第六篇），非本篇新增 |
| 17 | §11.6 | 插件 Supervisor 进程/容器回收 | 无 | ⬜[子] | 设计未标 `[V1.5]` |
| 18 | §12.4 | 终止条件 + SUCCEEDED/INCOMPLETE/NEEDS_REVIEW/UNKNOWN | Run 17 态含 COMPLETED/FAILED/MANUAL_REVIEW/REQUIRES_RECONCILIATION | 🟡[子] | 无 INCOMPLETE/NEEDS_REVIEW/UNKNOWN 命名 |
| 19 | §14.1 | `agent.*` / `orchestration.*` 表 | 收敛为 `registry.*` + `runtime.*` + `planning.*` | ✅[子] | 有意偏离命名（矩阵 §6 已述） |
| 20 | §15.1 | Agent 管理 API 含 versions/validate/evaluate/activate/suspend/revoke | `router.go:52-57` 仅 5 条 | 🟡[实] | 缺 versions 全系列端点 |
| 21 | §15.2 | 执行协作 API `/runs/{id}/{steps,evidence,resume,conflicts,review}` | `router.go:44-49` 仅 Create/List/Get/Cancel/Reconcile/LeaseRenew | 🟡[实] | **steps/evidence/resume/conflicts/review 五路由全缺** |
| 22 | §17.2 | 委派深度控制（网关强制 ≤3，超限 PERMISSION_DENIED） | `planir.go:205 CheckDelegationDepth` + `MaxDelegationDepth=3`；`TestCheckDelegationDepth` | 🟡[子] | 纯函数，**无网关调用方** |
| 23 | §13 | 四层评估 / 发布门禁 / 灰度 / 回滚 / 撤销 | 无 `evaluation_runs/rollouts/revocations` 表；无灰度端点 | ➖[子] | 归第十五篇与后续阶段 |

**范围外**：§7 多智能体协作整体标 `[V1.5]`；§17 深度委派标 `[V2]`；§13 评估/灰度/回滚归后续阶段；§16 前端六工作区属阶段外。
**❓待确认**：① §18.1 审批后 Payload Digest 重核是否由 `internal/erp/gateway.go` 承担；② `planning.plans/plan_revisions` 是否已由 Harness/API 写入真实计划并关联 `runtime.agent_runs`；③ `000010` 的 `evaluation_*` 表是否即 §13 的等价落地。

---

### 4.10 第九篇：模型供应商抽象层与 LLM Gateway

| # | 章节 | 设计要求 | 实现证据 | 判定 | 缺口/说明 |
|---|---|---|---|---|---|
| 1 | §2/§5.2 | 四层实体 provider/endpoint/model_revision/capability_profile | `llm.{providers,endpoints,model_revisions}`（`000010`）+ `capability_profiles`（`000019`） | ✅[实] | 四层表齐全 |
| 2 | §3 | 统一 `InferenceRequest/Response` 协议 | `internal/llm/provider.go CompletionRequest/Completion` | 🟡[子] | 缺 budget_ref/deadline/trace_id/agent_ref/task_class/response_mode/request_digest |
| 3 | §4.1 | Provider 接口含 DiscoverCapabilities/Health/Stream | `provider.go` 接口仅 `Name/Capabilities/Complete` | 🟡[子] | 无能力发现、健康检查、流式方法 |
| 4 | §4.4 | 能力探测验证写入带版本 Registry | `000019` 有 `verified_at` + status CHECK；`registry.go:172` UNVERIFIED 留痕 | 🟡[实] | **仅声明值，无探测实现**；`POST /admin/model-providers/{id}/verify` **磁盘确认未注册** |
| 5 | §5 | Model Registry 为唯一真源装配 | `registry.go RegistryLoader.Load/Build`、`registryLoadSQL`（LEFT JOIN） | ✅[码] | 缺陷 #27 已修；`TestBuildGatewayExcludesDisabledEndpoint` |
| 6 | §5.3 | Run 记录确切模型选择 | `gateway.go:20 Resolved{Provider,EndpointID,ModelRevisionID}` | 🟡[子] | 身份随路由返回，但 **Run 侧未接**（worker 仍骨架） |
| 7 | §6.1/6.2 | 硬约束过滤，无合格模型安全失败 | `gateway.go:55 Router.Resolve`+`Registered`；`TestRouterRefusesSilentModelSubstitution` | ✅[码] | 登记即严格，返回 `UNAVAILABLE` |
| 8 | §6.4/6.5 | 质量评分、影子路由、四路由模式 | 无 | ➖[子] | 代码注释自标 `[V1.5]` |
| 9 | §7.1 | 治理决策链（9 步） | `gateway.go:158 Gateway.Complete` 仅 限流→预算→缓存→路由 | 🟡[子] | 无 ResolveModelPolicy/ClassifyInputData/CheckAllowedEndpoints |
| 10 | §8 | 数据分级与端点白名单/脱敏 | `semcache.go:19` 四级 DataClass | 🟡[子] | 无数据政策引擎；`MODEL_DATA_POLICY_DENIED` 未在 errors 注册 |
| 11 | §9.1 | 有界 Schema 修复回路 N≤2 | `schemarepair.go MaxSchemaRepairAttempts=2`；`TestCompleteStructured` | ✅[码] | — |
| 12 | §9.2 | 统一输出状态枚举 | 无 | ⬜[子] | 无 VALID/INVALID_SCHEMA/TRUNCATED/REFUSED |
| 13 | §10 | 错误分类 + 重试/故障切换状态机 | `gateway.go:272 classifyProviderError` | ⬜[子] | 无 retry/backoff/fallback |
| 14 | §11.2 | 原子预算预留与结算 | `usage.go:126 BudgetGuard.Check` | 🟡[子] | 用 `SumCost` 估算，**非原子预留**；无 `budget_reservations` 表 |
| 15 | §11.3/§16 | `llm.usage_ledger` 幂等账本 | `usage.go UsageLedger.Record`；DDL `UNIQUE(tenant_id,request_id)`；`TestUsageLedgerDedupe` | ✅[码] | `ON CONFLICT DO NOTHING` |
| 16 | §12.3 | 三层缓存 + cache_hit 降权 | `semcache.go` L1/L2/L3、`DowngradeWeight=0.3`；8 个用例 | ✅[码] | 自标 `[V1.5]` 但**已超范围实现** |
| 17 | §12.3 | 相似度阈值默认 0.95 | `semcache.go:39 DefaultSimilarityThreshold=0.97` | 🟡[码] | 与设计书 0.95 偏离 |
| 18 | §14.2 | 每端点独立熔断器（开/半开） | 无 | ⬜[子] | — |
| 19 | §14.3 | 令牌桶限流（RPM/TPM/并发） | `ratelimit.go Bucket/Limiter/DefaultParams/TenantCaps`；6 用例 | ✅[码] | 档位/队列/Retry-After 齐 |
| 20 | §5.2 | 限流/预算参数从 DB 读 | `ratelimit.go:31 DefaultParams/TenantCaps`；`usage.go:28 DefaultReserveFactor=1.2` | 🟡[码] | **硬编码**；`cmd/api/llmcheck.go:94` 的 `Limit:5.0` 亦硬编码 |
| 21 | §5.2 | `llm.routing_policies` / `llm.budget_reservations` 表 | **全量迁移扫描无此两表** | ⬜[实] | 同缺 pricing_profiles/endpoint_health/provider_events |
| 22 | §17 | 推理/管理 API | `router.go` 的 `/admin/model-providers` 系列（7 条） | 🟡[实] | 管理 API 改为 `/admin/*`；`/api/v1/llm/{generate,stream,embeddings,jobs}` **全无**；validate/suspend 无 |
| 23 | §17.1 | 流式 / 异步任务 | 无 `Stream`/`Embeddings`/`Jobs` 符号 | ⬜[子] | — |
| 24 | §16.2 | 计价版本、对账作业 | `model_revisions.pricing_profile_id` 列存在 | ⬜[子] | 表不存在，无对账/结算作业 |

**范围外**：§4.4 能力探测 / §6.4 质量评分 / §6.5 影子路由（`[V1.5]`）；§12 语义缓存（`[V1.5]`，**实际已实现**，属超范围交付）；V2 多供应商市场化路由；§18 前端模型治理控制台。
**❓待确认**：① 相似度阈值 0.97 vs 设计 0.95 是有意调优还是笔误；② `CodeModelCapabilityUnavailable` 已注册但 `Router` 返回通用 `CodeUnavailable`，域码是否被使用；③ `llm-check` 的 `BudgetGuard.Limit=5.0` 为演示值，生产是否有真实限额注入方。

---

### 4.11 第十篇：任务事件总线与分布式可靠执行

| # | 章节 | 设计要求 | 实现证据 | 判定 | 缺口/说明 |
|---|---|---|---|---|---|
| 1 | §5.3 | Outbox 表 + 每聚合计数器行锁分配 seq | `000008`；`outbox.allocSeqSQL`/`Writer.AppendTx`；`TestAppendConcurrentSameAggregateNoDuplicateNoGap` | ✅[码] | — |
| 2 | §5.3 | **状态变更与 Outbox 同事务提交** | 原语 `AppendTx(ctx,tx,ev)` 支持；**生产调用走 `Append`（自开事务）** | 🟡[实] | `handlers.go:241 appendOutbox` 与 `Machine.Apply` 是**两个事务** |
| 3 | §5.4 | Publisher 两段式 + SKIP LOCKED + 批大小 | `outbox.Publisher.RunOnce`/`claimSQL`/`markSentSQL`；`cmd/worker:publisherLoop`、`cmd/api:runOutboxBridge` | ✅[码] | **生产者与 Publisher 均有真实调用方** |
| 4 | §5.4 | 过期 DISPATCHING 回收 | `Publisher.ReclaimExpired`；`TestReclaimExpiredRestoresPendingAndDead` | ✅[码] | — |
| 5 | §10.2 | 指数退避 + **随机抖动** | `outbox.Backoff`（100ms×2，cap 5s）；`TestBackoffSequence` | 🟡[码] | **无随机抖动 J_n** |
| 6 | §5.4 | 空转指数退避 | `cmd/worker workerLoop` 250ms→5s | 🟡[子] | 退避在调用方循环，非 Publisher 内 |
| 7 | §6.2 | dedup 表唯一约束 + 分区处理表 | `000008 consumer_inbox_dedup`(主键)、`000014` 补 `consumer_inbox` RANGE 分区 + default | ✅[实] | 实测分区子表存在（2026_10 … 2027_12） |
| 8 | §6.3 | 单事务完成 inbox + 业务 + outbox | `inbox.Consumer.Process` 注释明示"Handler 在事务之外""失败即终态" | 🟡[子] | 与单事务模型有差距 |
| 9 | §6.4 | 同 event_id 不同 digest 隔离报警 | dedup 仅 `ON CONFLICT DO NOTHING`，未比对 payload_digest | ⬜[子] | 无摘要冲突分支/隔离队列 |
| 10 | §17 | `event_dead_letters` 表 | `000008`；`pgInboxStore.upsertDeadLetter`；`TestProcessHandlerFailureRecordsDeadLetter` | 🟡[子] | 写入经 `Consumer` = **无调用方**；status 枚举 `PENDING` 非设计 `OPEN/REPLAY_APPROVED` |
| 11 | §17.4/§21.2 | 死信运维 / 重放预览与执行 | `router.go` 无 dead-letters/replay 路由 | ⬜[实] | 端点全缺 |
| 12 | §8 | 租约 + lease_epoch fencing | `execution/lease/lease.go Fence/Acquire`；`harness.Scheduler.ClaimNext` | ✅[码] | 用 `runs` 表，未建独立 lease 表 |
| 13 | §8.3 | 续约校验 owner + epoch | `lease.Renew`；`POST /runs/{id}/lease/renew`；`TestRenew` | ✅[实] | 端点实测可达 |
| 14 | §12.3 | aggregate_seq 缺口等待窗口/检测 | `errors.go:201 CodeBusSequenceGap` **无消费侧使用** | ⬜[子] | 仅分配序号，无缺口检测 |
| 15 | §14 | Recovery Controller 扫描恢复 | `cmd/worker`：`ReleaseExpiredLeases` + `ReclaimExpired` | 🟡[子] | 仅租约/outbox 回收；无 UNKNOWN 对账/死信处理 |
| 16 | §15.1 | `task_attempts`/`event_subscriptions`/`worker_instances`/`recovery_jobs`/`event_archive` | **迁移中均无对应表** | ⬜[实] | — |
| 17 | §15.3 | 分区与保留策略 | `consumer_inbox` 按月分区 | 🟡[实] | 无保留清理作业；死信 TTL / outbox SENT 清理 / 归档未实现 |
| 18 | §16.1 | 事件 schema_version 版本化 | `task_outbox.schema_version`；`outbox.fillDefaults` 默认 `"v1"` | 🟡[子] | 无版本注册/契约测试/兼容性检测 |
| 19 | §15.4 | RLS 租户隔离 `execution.*` | `000011`(task_outbox/event_dead_letters)、`000014`(consumer_inbox/dedup) | ✅[实] | `aggregate_seq_counters` 无 tenant_id（残余风险已注明） |
| 20 | §21.1 | `GET /api/v1/tasks/{id}` 异步任务查询 | `router.go:113` → `handlers.GetTask`；`router_test.go:82` | ✅[实] | 端点实测可达 |
| 21 | §7/§10.6 | SSE 实时推送 `/api/v1/stream` | `router.go:114` → `handlers.Stream`；`sse.Hub`；`sse.OutboxSink`；`hub_test.go` 7 用例 | ✅[码] | Last-Event-ID 续传 + 背压分级 |

**范围外**：§11.5 补偿与 Saga `[V1.5]`；§17 死信高级处置 `[V1.5]`；§4 消息代理引入（NATS/RabbitMQ/Kafka Adapter）`[V2]`。
**❓待确认**：① §5.3 同事务写入是否在 P7 改走 `AppendTx`；② Inbox 消费者计划由哪个进程驱动（`cmd/worker` 当前未装配 `inbox.Consumer`）；③ 死信 status 枚举与 `failure_reason` 字段名是否属有意收敛。

---

### 4.12 第十一篇：可观测性、审计证据链与运维治理

| # | 章节 | 设计要求 | 实现证据 | 判定 | 缺口/说明 |
|---|---|---|---|---|---|
| 1 | §5 | 结构化日志 + 统一字段（含 trace_id/business_request_id） | `platform/logging/logging.go New`(JSON slog)；`middleware/logging.go` | 🟡[码] | 缺 trace_id/span_id/event_code/result |
| 2 | §4 | 统一关联标识 BusinessContext | 无 telemetry 包；`go.mod` **无 otel 依赖** | ⬜[码] | 无 trace 上下文传播 |
| 3 | §9.2 | `audit_events` 按 `occurred_at` 范围分区（按月） | `000007:7` 建**普通表**，无 `PARTITION BY` | ⬜[实] | 独立确认未分区 |
| 4 | §9.2 | 段边界 `prev_segment_root` 列 | `000007` 仅有 `prev_digest` | ⬜[实] | 列缺失 |
| 5 | §9.3 | 事件摘要 + 规范化编码（SHA-256/0x1f） | `audit/chain.go EventDigest/canonicalJSON`；`000015`；`chain_test.go` | ✅[码] | 含 JSONB 往返缺陷修复 |
| 6 | §9.4 | 每租户每段行锁分配 seq_no，禁止空洞 | `audit.next_segment_seq`；`chain.go Append`；`audit.segment_seq_counters` | ✅[码] | — |
| 7 | §9.7 | fail-closed 分级（CRITICAL/NORMAL、`WriteOrBlock`） | `audit/failclosed.go:75 WriteOrBlock`；`failclosed_test.go` | 🟡[实] | 逻辑+测试有；**非测试代码零调用**；`handlers.go:265` 直接 `Append` **且忽略错误** |
| 8 | §9.7 | 本地 WAL 排队重试（append-only/fsync） | `failclosed.go Buffer/MemBuffer` | ⬜[子] | 仅内存 Buffer，无 WAL |
| 9 | §9.5 | 独立检查点（≤15min，段根+签名） | 无 `audit.checkpoints` 表、无 checkpoint 代码 | ⬜[子] | 四层完整性仅实现 ①② 层 |
| 10 | §9.3.1 | Merkle 段根 `segment_root` | 无 merkle/segment_root 代码或列 | ⬜[子] | — |
| 11 | §9.6/§9.8 | 签名密钥管理、RFC3161/HSM 可信时间 | 无 | ⬜[子] | — |
| 12 | §9.2 | 哈希链**运行期校验调用方** | `Recorder.Verify` 仅 `chain_pg_test.go:63` 调用 | ⬜[实] | **确认仅测试**；无可达 API/作业 |
| 13 | §9.2 | append-only 触发器 + 权限收紧 | `trg_audit_events_append_only`；`000011:174` REVOKE | 🟡[实] | `audit_events` 双保险 ✅；**`evidence_records` 仅 REVOKE、无触发器**（见 L5） |
| 14 | §10/§11 | 证据包构建 + 完整性验证 | `evidence/bundle.go:90 BuildBundle/VerifyBundle`；`store.go Save`；`bundle_test.go` | 🟡[实] | 摘要有；**非测试代码零调用**；无 manifest/version-lock/artifacts/export |
| 15 | §15.1 | 审计查询 API（6 端点） | `handlers/audit.go:ListAuditEvents`；`router.go:101 GET /api/v1/audit-events` | 🟡[实] | 路径与设计不同；缺详情/verify/checkpoints/runs/operations |
| 16 | §15.2 | 证据 API（构建/查询/verify/export） | `router.go` **无 evidence 路由** | ⬜[实] | 端点全缺 |
| 17 | §15.3 | 运维 API（health/slo/alerts/incidents/integrity） | `GET /api/v1/ops/alerts`（BFF 聚合） | 🟡[实] | 缺 health/slo/incidents/integrity；alerts 为死信/失败运行**计数**，非告警规则 |
| 18 | §7.2/§7.3 | SLO 定义 + 告警规则（SEV1/2/3） | 无 `ops.slo_definitions`、无 `ops.alerts` 表、无规则引擎 | ⬜[实] | — |
| 19 | §13 | 事故管理 `ops.incidents` + 状态机 | 无该表、无 incident 代码 | ⬜[实] | — |
| 20 | §12.4 | 数据保留与归档作业、备份恢复 | 无归档/备份脚本（仅 start/stop/restart 生命周期脚本） | ⬜[子] | — |
| 21 | §3.2/§3.3 | OTLP 导出接入应用 | compose `otel-collector` + `deployments/config/otel-collector.yaml` | 🟡[实] | **基础设施在位；Go 侧无 OTel SDK/无 exporter**，无实际导出 |
| 22 | §19 | 完整性/故障注入/租户隔离测试 | `chain_test.go`、`chain_pg_test.go`（UPDATE 拒绝）、`bundle_test.go` | 🟡[子] | 缺故障注入、审计租户隔离、归档后验证用例 |

**范围外**：§6 分布式 Trace（`[V1.5]`）；§7.1 Prometheus 指标与告警后端（`[V1.5]`）；§14 治理中心完整前端（`[V1.5]`）；§9.3.1 亿级事件全量重算与跨区域多活归档（`V2`）。
**❓待确认**：① `audit_events` 分区与 `prev_segment_root` 是否计划在后续迁移补齐；② `Recorder.Verify`/`WriteOrBlock`/`BuildBundle` 是否存在动态装配（反射/接口注入）调用方；③ `GET /api/v1/ops/alerts`、`/admin/overview`、`/releases` 经确认为第十七篇 BFF 聚合端点，与第十一篇 §15.3 的告警/事故语义端点存在**归属冲突**，建议契约层澄清。

---

### 4.13 第十二篇：数据接入、数据治理与语义映射平台

> ⚠️ **本篇是本清单与自查矩阵分歧最大的一篇**，见 §5.1。

| # | 章节 | 设计要求 | 实现证据 | 判定 | 缺口/说明 |
|---|---|---|---|---|---|
| 1 | §4 | 数据源注册中心（连接配置、`credential_ref`、能力发现、健康状态） | 无 `ingestion.data_sources` 表；无 Connector 代码 | ⬜[实] | 设计标 **`[MVP-V1.0]`** |
| 2 | §4.5 | Connector Go SDK（DiscoverSchema/Snapshot/Incremental/Health） | `internal/` 无 connector 包 | ⬜[子] | — |
| 3 | §5 | 数据目录 / 元数据 / Schema Registry（含 `schema_versions`） | 无 `catalog.*` schema | ⬜[实] | — |
| 4 | §5.4 | 元数据 API（8 条） | `router.go` 无；`openapi.yaml` 未登记 | ⬜[实] | 前端 `api.ts:18` 已确认端点缺失 |
| 5 | §6.1 | 采集模式（快照/增量轮询/CDC/事件驱动） | 无 ingestion 包；无 sync_jobs | ⬜[子] | CDC 子项属 `[V1.5]` |
| 6 | §6.2-6.3 | 状态机与游标（三态分离、复合游标、重叠窗口） | 无 `ingestion.sync_jobs` | ⬜[子] | — |
| 7 | §6.4 | 检查点与幂等（cursor_before/after、分步提交） | `execution/{outbox,inbox}` 为**通用事件幂等**，非接入层检查点 | 🟡[子] | 通用 Outbox 在，接入层检查点未实现 |
| 8 | §6.4.1 | 接入链路幂等链（`ingestion.inbox` + `raw_records` 三级唯一约束） | 仅 `execution.consumer_inbox` | ⬜[实] | 设计标 `[MVP-V1.0]`，未实现 |
| 9 | §7 | 三层数据模型（Raw/Canonical/Semantic） | 无 raw/canonical 存储与代码 | ⬜[子] | — |
| 10 | §7.2 | Canonical Model（`FieldValue[T]`/`ValueState`/`Money`） | grep 无 `CanonicalModel` | ⬜[子] | — |
| 11 | §8 | 语义映射引擎（Mapping Manifest、发布不可变版本） | 无 mapping schema/引擎 | ⬜[子] | 前端映射编辑器按钮 disabled |
| 12 | §8.4 | 受控转换函数（trim/enum_map/money_normalize） | 无任何转换函数实现 | ⬜[子] | — |
| 13 | §9 | 实体解析（确定性/规则/候选三层） | 无 entity_resolution 模块/表 | ⬜[子] | 模糊匹配属 `[V2]` |
| 14 | §10 | 质量引擎（七维度、质量评分、硬门槛、隔离区） | 无 `quality.findings` | ⬜[子] | — |
| 15 | §11 | 语义实例与运行时集成（实例带来源/映射版本/质量/新鲜度元数据） | `ontology/graphstore/store.go` 存在（**零装配**） | 🟡[实] | 图存储骨架在，来源元数据/多来源冲突未接 |
| 16 | §12 | 数据血缘（血缘节点/边、影响分析） | 无 `lineage.*` schema | ⬜[子] | 字段级血缘属 `[V1.5]` |
| 17 | §13 | 治理 / 分级 / 脱敏 / 目的限制 | 仅 `internal/llm/semcache.go` 有 LLM 数据分级（第九篇） | 🟡[子] | 数据侧分级与脱敏未实现 |
| 18 | §14.1-14.4 | 数据库 Schema（ingestion/catalog/mapping/quality/lineage 五 schema） | `migrations/platform/` **无这五 schema** | ⬜[实] | 五 schema 全缺 |
| 19 | §15 | API（约 60 端点） | `router.go` 均无 | ⬜[实] | 仅 `GET /admin/connectors`、`/erp/capabilities` 相邻 |
| 20 | §16 | 前端七个工作区 | 仅 `web/src/features/data-mapping/pages/DataMappingPage.tsx` | 🟡[实] | 单页骨架 + 空态；其余 6 工作区缺 |
| 21 | §17.5 | ERP 回执驱动失效闭环（`data_graph_version` 递增） | `integration.erp_receipts` + `erp/gateway.go` 回执在 | 🟡[子] | grep 无 `data_graph_version` |
| 22 | §19/§21.1 | 测试与 MVP 交付（Mock ERP+HR、Canonical 四类对象、端到端验收） | `mockerp/migrations/000001_init.sql`；`datasets/finance-demo/*.json` 无 data_source/mapping 资产 | ⬜[实] | Mock ERP 在，接入链路缺 |

**范围外**：§6.1.3 CDC 连接器 `[V1.5]`；§12.3/§14.4 字段级血缘下推 `[V1.5]`；§15.6 backfill 双跑重建 `[V1.5]`；Schema 漂移自学习 `[V1.5]`；通用 CDC 平台、复杂模糊实体匹配、多数据仓库适配 `[V2]`。
**❓待确认**：① **范围归因冲突**（§5.1）——矩阵记本篇整篇为 `[V1.5]` 排除，设计书 §5 却明确 §4/§6/§7/§8/§10/§6.4/§17.5/§21.1 为 `[MVP-V1.0]`；② `admin:data-source:read` 权限键仅见前端路由守卫，未在 Go/SQL 权限种子中登记；③ `web/src/api/generated/fixtures.ts` 存在 `FIXTURE_DATA_SOURCES`。

---

### 4.14 第十三篇：知识图谱存储、查询优化与语义推理

| # | 章节 | 设计要求 | 实现证据 | 判定 | 缺口/说明 |
|---|---|---|---|---|---|
| 1 | §8 | 统一 `GraphStore` 接口（Match/Insert/Delete/EnsureGraph） | `graphstore/store.go GraphStore`；`TestPGStoreImplementsGraphStore` | 🟡[实] | 接口完备，**无生产装配**；`Match` 按 `terms.canonical_digest` join 过滤，非直接走 POS/OSP |
| 2 | §5.3 | PostgreSQL 适配器 PGStore 可测试实现 | `store.go PGStore`、`withTenantTx` | 🟡[实] | **无任何 PGStore 连库集成测试**（所有测试走 `NewMemStore`） |
| 3 | §6.1 | 资源字典 `graph.terms` + 字面值/语言标签编码 | 表 + `idx_terms_digest`/`idx_terms_lexical`；`Term.CanonicalDigest`(sha256+0x1F)；`TestCanonicalDigest` | ✅[码] | 摘要含四段分隔，覆盖拼接碰撞 |
| 4 | §6.2 | 命名图 `graph.named_graphs` + 三元组表 | 表 + `EnsureGraph`；`TestEnsureGraphIdempotent` | ✅[码] | — |
| 5 | §6.3 | PK+POS+OSP 三索引 | `000009`：PK + `idx_triples_pos` + `idx_triples_osp` | ✅[码] | **列序偏差**：实为 `(graph_id,predicate_id,object_id,subject_id)`，设计为 `(tenant_id,…)` |
| 6 | §6.3.1 | 按 tenant 声明式 LIST 分区 | `PARTITION BY LIST (tenant_id)`；`partition.go`；`TestTenantPartitionName/DDL/RejectsInjection` | ✅[码] | DDL 含注入防护 |
| 7 | §6.5 | 事实来源表 `graph.fact_sources`（多来源支持） | **无** | ⬜[实] | 同一三元组多来源无法保留 |
| 8 | §7.1 | 六类命名图分层 | `named_graphs.graph_type`；`FactKind=ASSERTED/DERIVED` | 🟡[子] | 仅 2 类 fact_kind；六层未建模 |
| 9 | §7.2 | 多租户隔离（查询/遍历/缓存均带 tenant） | `withTenantTx` + `Match` 强制 tenant；`TestMatchTenantIsolation` | ✅[码] | 分区裁剪 + RLS 纵深防御 |
| 10 | §9 | 受控 Semantic Query DSL（禁裸 SPARQL/SQL） | `semantic/query.go Service.Execute`、`Clause/Query`；`TestExecuteTwoPatternJoin` | 🟡[实] | 仅三元组模式嵌套连接；无路径 DSL、无一致性/证据选项 |
| 11 | §9.3 | 运算符白名单（EQ/IN/GT/AND/DURING/COUNT） | **无** | ⬜[子] | 无 where 比较算子、三值逻辑、聚合、时间算子 |
| 12 | §10/§11 | 编译流水线 / LogicalQueryIR / 规则式优化器（Join 排序 + 下推） | 无 `queryir/planner/optimizer` 包 | ⬜[子] | 无谓词下推、无选择性排序、无统计信息 |
| 13 | §11.5/§20.1 | 分层缓存 + 缓存键含授权/版本摘要 | `cache/cache.go`（LRU+TTL）、`Key`；`TestVersionAdvanceInvalidates` | 🟡[实] | 已实现"最宽投影+版本失效"；键缺 authorization/mapping/reasoning/data-policy 摘要；**未接入查询链路** |
| 14 | §12.3/§14.1 | RDFS 子集**真执行并物化**派生事实 | `rdfs/infer.go:192 InferContext`→`store.InsertTriples(FactKindDerived)`；13 条规则；`TestInferSubClassTransitiveClosure` | ✅[码] | **真实物化**（`stats.Materialized` 计数），超出 §14.1 要求 |
| 15 | §14.1 | OWL 受控子集（eqClass/inverseOf/Transitive/Symmetric/sameAs） | **无** | ⬜[子] | `rdfs` 包文档明示未实现 |
| 16 | §15 | SHACL 核心约束校验 | `shacl/validate.go:Validate`；`TestValidateSupportedConstraints` | 🟡[码] | 缺 §15.1 标"实现"的 `sh:node`、`not/and/or/xone`、`closed`、`equals/disjoint/lessThan`、`qualifiedValueShape`、`targetSubjectsOf/ObjectsOf` |
| 17 | §15.1 | `sh:sparql` 显式排除且 fail-closed | `validate.go:checkKeywords`（返 `ONTOLOGY_VALIDATION_FAILED`）；`TestValidateRejectsSparql` | ✅[码] | 不静默跳过 |
| 18 | §13.3 | 全量物化（Source→Profile→Job→Derived） | RDFS 物化路径；`FactKindDerived` | 🟡[子] | 无 `materialization_builds` 表、无 Job 编排、无快照/规则版本记录 |
| 19 | §18 | 推导记录 `derivation_id`/前提/规则版本 | `Stats.ByRule`（仅计数） | ⬜[子] | 无逐条 derivation，不可追溯 |
| 20 | §19 | 查询一致性级别与快照/版本锁定 | `named_graphs.snapshot_id` 列（未使用） | ⬜[子] | `GraphStore` 接口无 `CreateSnapshot` |
| 21 | §16/§17 | 多源协调 Resolver + `owl:sameAs` 治理 | 无 | ⬜[子] | 属性级权威策略、冲突状态全缺 |
| 22 | §22/§23.1/§23.3 | `reasoning.*`/`semantic_query.*` 表 + 运行时 API + 统一错误码 | 迁移无此 schema；`internal/api` 无 `/api/v1/semantic/*`；仅 2 个错误码 | ⬜[实] | §23.3 所列 15 码均未定义；`ontologies.go:111` 证实图存储未装配 |

**范围外**：§13.5/§13.6 增量物化与 DRed 算法基线 `[V1.5]`；§2.2/§14.1 OWL 2 RL 决策点 `[V1.5]`；§11.1/§10.1 CBO 成本优化器 `[V2]`；§5.3/§27 专用/分布式 RDF Store `[V2]`；§15.1 `sh:alternativePath` 序列路径 `[V1.5]`。
**❓待确认**：① PGStore 无连库集成测试——索引/分区/RLS 在真实 PG 上是否验证过；② 二级索引列序用 `graph_id` 而非设计 `tenant_id`，是否经架构评审；③ `internal/ontology/*` 零装配是**计划内（待 P6 联调）**还是**漏接**；④ 实现落在 `internal/ontology/*`，设计 §25 期望 `internal/semantic|reasoning|validation|resolution/*`，重组是否在计划内。

---

### 4.15 第十四篇：语义执行计划与工具编排

| # | 章节 | 设计要求 | 实现证据 | 判定 | 缺口/说明 |
|---|---|---|---|---|---|
| 1 | §3 | 统一业务意图（scope/constraints/requested_outcome + 解析状态机） | `planner.go Intent{Domain,Utterance,AllowedTools,OntologyVersion}` | 🟡[子] | 仅最小子集；无状态机 |
| 2 | §4 | Action Contract（preconditions/effect_class/幂等/补偿） | **全仓无 `ActionContract`** | ⬜[子] | 完全缺失 |
| 3 | §5.3-5.4 | Semantic Planner 生成候选逻辑计划 | `planner.go Planner.Propose`；`planner_test.go:TestPlannerPropose` | ✅[码] | 结构化输出 + Schema 修复 |
| 4 | §6.1-6.2 | Plan IR 13 类节点基线（SEMANTIC_QUERY…EVIDENCE_FINALIZE） | `planir.go:AllNodeKinds`(13)；`TestAllNodeKindsCount` | 🟡[码] | **枚举与 ADR-001 §6 基线不一致**：缺 RULE_EVALUATION/DATA_VALIDATION/WAIT_EVENT/HUMAN_TASK/COMPENSATION/RESULT_VERIFY/EVIDENCE_FINALIZE；多 START/END/LLM_CALL/READ_TOOL/WRITE_TOOL/SUBPLAN/NOOP |
| 5 | §6.3 | DAG 依赖存在性检查 + 无环检测并报环上节点 | `planir.go:findCycle`（三色 DFS）；`TestValidatePlan` | ✅[码] | — |
| 6 | §6.4 | 计划不变量：未验证/未审批不允许写；写节点须 APPROVAL_GATE 前驱 | `planir.go:hasApprovalAncestor`；`executor.go:ExecuteNode` | ✅[码] | — |
| 7 | §7 | 类型系统 / 数据流（EntityRef vs EntitySnapshot、refinement 精化） | 无实现（`Node.Params` 无 schema 绑定） | ⬜[子] | — |
| 8 | §8.1-8.3 | 工具注册发现 + 版本锁定（八类 digest） | `tools/registry.go Resolve/ResolveLatest` | ✅[码] | `VersionLock` 仅 5 字段，非八类 |
| 9 | §9 | Compiler / Freeze（编译、类型检查、版本绑定、freeze 生成不可变 PlanRevision） | **无 `PlanCompiler`/`CompiledPlan`/freeze 代码** | ⬜[实] | 按 ADR-001 归第六篇 Harness |
| 10 | §10 | 工作流状态机仅引用 contracts/01 的 17 态 | `harness/machine.go`、`state.go`、`docs/contracts/01` | ✅[码] | — |
| 11 | §11.2 | 节点执行协议（14 步：授权→审批→调用→校验→检查点） | `executor.go:ExecuteNode/executeRead/executeWrite` | 🟡[实] | 单节点协议就位；**无 Orchestrator/DAG 调度器**，且 `internal/agent` 零装配 |
| 12 | §12 | Approval Gate + 审批绑定操作参数摘要 | `NodeApprovalGate`；`erp.LedgerWaitingApproval`；执行前前驱校验 | 🟡[子] | 审批**参数摘要绑定**未实现 |
| 13 | §13 | 重试/补偿/部分成功（SAFE_READ…、COMPENSATION、完成策略、部分成功报告） | 无实现 | ⬜[子] | — |
| 14 | §14 | 报销 E2E 案例（预览→执行→回执→对账） | `mockerp/`、`erp/adapters/mock` 存在，但 agent 未接驱动 | 🟡[实] | 与矩阵"E2E 编排待补"一致 |
| 15 | §15.1 | `planning.plans` / `plan_revisions` 结构与不可变修订 | `000010` 建表；写入方 = `internal/seed/apply.go:628,639` | 🟡[实] | **仅种子写入，无运行期写入方** |
| 16 | §15.2-15.4 | `workflow.instances` / `workflow.nodes` / `workflow.node_dependencies` | **三表不存在**（`000006` 的 workflow schema 仅 `approvals`/`approval_actions`） | ⬜[实] | 表缺失 |
| 17 | §15.5 | `planning.validation_runs` 记录校验发现 | `000010` 建表；仅 `audit_aggregate.go:250` 计数查询 | ⬜[实] | 表在，**无写入方** |
| 18 | §16 | 计划 / 工作流 API（`/planning/plans*`、`/workflows*` 全套） | `router.go` 仅 `GET/POST /plan-templates`、`POST /tools/{id}/invoke` | ⬜[实] | 计划/工作流 API 均未注册 |
| 19 | §17.1 | Planner / Validator / Compiler 三接口 | `planning/` 包不存在；`agent.Planner` 为 struct | 🟡[子] | 仅 `Planner.Propose` |
| 20 | §17.2 | 读写入口拆分（ReadOnlyExecutor + WriteGateway 为唯一写路径） | `executor.go ReadExecutor/WriteGateway` **接口定义** | 🟡[实] | **无任何具体实现**；`erp.OperationGateway` 未被适配绑定 |
| 21 | §21 | 计划模板资产化（模板发布/版本/兼容性） | `handlers/plan_templates.go`；`store/plan_templates.go`；种子 6 节点模板 | 🟡[实] | GET 真实 200；**POST 诚实 503** |
| 22 | §22 | 测试体系（编译/编排/工作流/安全/故障注入；GATE-TOOL-01~06） | `agent/*_test.go` 14 例、`tools/validate_test.go` 6 例 | 🟡[子] | **GATE-TOOL-01~06 与故障注入测试不存在** |

**范围外**：§8.4 工具自动降级 `[V1.5]`；§18 前端"语义规划与业务工作流中心"六工作区 `[V1.5]`；§21.3 自适应动态规划 `[V2]`；§21.1 第三类动态候选计划 `[V2]`。
**❓待确认**：① `planir.go` 的 13 类枚举与 ADR-001 §6 基线是**两套不同集合**，代码注释却称"一一对应"；② 种子模板 `datasets/finance-demo/plans.json` 使用**第三套节点词表**（READ_OBJECT/ONTOLOGY_VALIDATE/…），既不被 `ValidatePlan` 校验也不在基线内；③ §9 编译/freeze 归第六篇，本篇侧缺失结论不代表全仓缺失。

---

### 4.16 第十五篇：评估、仿真、回归测试与持续优化

> ⚠️ **本篇与自查矩阵存在范围归因分歧**，见 §5.2。

| # | 章节 | 设计要求 | 实现证据 | 判定 | 缺口/说明 |
|---|---|---|---|---|---|
| 1 | §四 | Dataset Registry（数据集/修订/案例版本管理与校验） | 表 `evaluation.{datasets,dataset_revisions,cases}`；`seed/dataset.go:323` | 🟡[实] | 仅 Schema 与 seed 类型；无端点、无写入 |
| 2 | §五 | 六维指标 / 任务成功率 / 排除防作弊 | 表 `evaluation.{metric_registry,metric_results,case_exclusions}`；`store.GetEvaluation` | 🟡[实] | 表在，**无指标计算引擎** |
| 3 | §六 | 确定性断言引擎（业务/过程/安全/证据四类） | 无 `evaluation` 包；无 `AssertionEngine` | ⬜[实] | 设计标 **`[MVP-V1.0]`** |
| 4 | §七 | **仿真引擎**（虚拟时钟/环境隔离/确定性重放） | 表 `simulation.scenarios` 仅结构；无 handler/route/worker | ⬜[实] | 设计标 **`[MVP-V1.0]`** 且 §22.0 列为 **P6 硬前置** |
| 5 | §八 | 故障注入（故障场景矩阵 + 注入协议 + 恢复断言） | 表 `simulation.fault_events` 仅结构；无注入引擎 | ⬜[实] | 同上，被矩阵误标 `[V1.5]` |
| 6 | §九 | Agent / 多 Agent 评估 | 无实现 | ⬜[子] | — |
| 7 | §十 | 本体/规则回归（升级影响分析、双版本评估） | 无实现 | ⬜[子] | — |
| 8 | §十一 | Workflow Regression Engine（基线 vs 候选、差异报告） | 无 `/api/v1/regression/compare` | ⬜[子] | — |
| 9 | §十二 | Quality Gate（四类门禁 + 配置式策略 + 豁免） | 表 `release_mgmt.quality_gate_decisions`；`store.Releases`；`GET /releases` | 🟡[实] | 表+只读列表就位；**无门禁引擎、无豁免审批** |
| 10 | §十三 | 模型评估 / 影子评估 / 切换门禁 | 无实现 | ⬜[子] | 影子评估规模化属 `[V1.5]` |
| 11 | §十四 | 持续优化闭环（失败归因 + 修复候选 + 防过拟合） | 无实现 | ➖[子] | 自动修复候选生成标 `[V2]` |
| 12 | §十五 | 数据模型 Schema（evaluation/simulation/release_mgmt 全表） | `000010`：evaluation 8 表 + simulation 2 表 + quality_gate_decisions | ✅[实] | Schema 完整落地（但多数表无写入路径） |
| 13 | §十六 | 统一评估 API（约 20 端点） | 仅 `GET/POST /evaluations`、`GET /{id}` | 🟡[实] | 其余端点全缺 |
| 14 | §16.4 | 发布推进 / 回滚（`POST /releases/{id}/promote/rollback`） | 前端 `evaluation/api.ts` **调用** promote/rollback；`router.go` **仅注册 `GET /releases`** | ⬜[实] | **前端按钮必失败（404/405）**——本清单新增 |
| 15 | §十七 | 前端工作台（评估与质量中心/失败案例工作台） | `web/src/features/evaluation/{api.ts,pages/*}`；`app/router.tsx:88-89` | 🟡[实] | 页面与路由就位；仅空态 |
| 16 | §十八 | 灰度 / 自动回滚（Shadow/Canary/回滚一致性） | 无 handler；无 `/releases/{id}/promote` | ⬜[实] | — |
| 17 | §十九 | 持续评估 / 漂移监测 | 无实现 | ⬜[子] | — |
| 18 | §二十 | Go 核心接口（evaluation/releasegate 包） | 无 `package evaluation`、无 `releasegate` | ⬜[实] | — |
| 19 | §二十一 | 仓库结构（`cmd/{evaluation-worker,simulation-worker,release-controller}`、`internal/evaluation/`） | `cmd/` 实为 `api/migrate/mock-erp/worker` | ⬜[实] | 三个 worker 与 internal 子包全缺 |
| 20 | §二十二 | 实施路线 P1-P8 | 无阶段产物；仅 §十五 Schema | ⬜[子] | P1-P8 未启动 |
| 21 | §1.1/§1.2 | 建设目标（Evaluation/Regression/Gate/Experiment 等 7 能力） | 无对应实现包 | ⬜[子] | — |

**端点真实性（本清单实测）**：`GET /api/v1/evaluations` 走 `Store.ListEvaluations` **真实 SQL 查询**（非空数组/503），`POST` 真实 INSERT 一行 `status='QUEUED'` 并返回 202——**非假成功**。但无执行引擎，运行**永久停留 QUEUED**。`GET /api/v1/releases` 真实查询 `release_mgmt.quality_gate_decisions`。
**范围外**：`[V1.5]` 三项——LLM-as-Judge 一致性度量、影子评估规模化、指标注册表运营看板；`[V2]` 一项——自动修复候选生成。
**❓待确认**：① **仿真引擎范围归属冲突（最重要）**——设计书第 7 行明标 §七/§八 为 `[MVP-V1.0]`，§22.0 称其为 P6 硬前置，**设计书全文无任何将仿真引擎标为 `[V1.5]` 的文字**；② `POST /evaluations` 落 QUEUED 不推进，算"诚实接收"还是"能力缺失"；③ seed 期望 `datasets/<name>/evaluations.json`，但 4 个数据包**均无该文件**，评估集数据注入确为空白；④ 前端调用 `/releases/{id}/promote|rollback` 与后端/契约**均无该路径**，需契约裁决。

---

### 4.17 第十六篇：MVP 总工程实施蓝图与交付路线

| # | 章节 | 设计要求 | 实现证据 | 判定 | 缺口/说明 |
|---|---|---|---|---|---|
| 1 | §4.1 | 仓库顶层结构 | 根目录实测齐备；16 个 internal 包、4 个 cmd、21+1 迁移、84 路由 | ✅[实] | — |
| 2 | §10.1 / 第一篇 §14.3 | **CI 流水线** | 全仓无 CI 配置文件 | ⬜[实] | 见 L8 |
| 3 | §11.1 P0 | 「开发者可重复启动」 | `start.sh`（轮询 `/readyz` 真实查库）+ `stop.sh`/`restart.sh` | ✅[实] | 脚本为设计外增量，更稳健 |
| 4 | §11.1 P0 | 退出条件含「CI」 | `make lint` 仅手动契约门禁 | ⬜[实] | P0「CI」未闭环，与矩阵 §4 标 ✅ 存在张力 |
| 5 | §8.4 | Makefile 目标（dev-up/migrate/seed/test/lint） | `Makefile` 有 10 个目标 | 🟡[实] | `lint` 仅跑 `tools/contract-lint`，缺 `golangci-lint` 与 `npm run lint`；无 `fmt/vet` 目标 |
| 6 | §8.1/§8.2 | Compose 拓扑（6 常驻 + 2 可选 profile） | 实测 6 服务 running；`objectstore`/`observability` profile 就绪 | ✅[实] | 端口仅绑 127.0.0.1 |
| 7 | §8.3 | 环境配置文件与 compose 变量一一对应 | `.env.example` 覆盖 DB/HTTP/LLM/objectstore/Worker/Dev 身份 | 🟡[子] | 有意偏离：`ODAP_DATABASE_URL` 用 `odap_app` 而非 owner `odap`（矩阵 §6 已登记） |
| 8 | §5.2/§5.3 | 连续编号迁移、可重复回放 | `migrations/platform/000001..000021` + `mockerp/000001`；`cmd/migrate up` | ✅[实] | — |
| 9 | §4.1 | 测试分层目录 | 6 目录**存在但全空**；实际测试下沉模块内（52 个 `*_test.go`） | 🟡[实] | `Makefile` 的 `test-integration`/`test-e2e` **指向空目录，目标不可用** |
| 10 | §4.1 | `deployments/{compose,config,scripts}` | `deployments/compose/Dockerfile.*`、`config/otel-collector.yaml`、`nginx/` | 🟡[实] | **`deployments/scripts/` 为空目录** |
| 11 | §4.1 | `docs/` 子目录 | 实测仅 4 个 | ⬜[实] | 5 个缺失（见 4.2） |
| 12 | §6.1 | 三类契约（OpenAPI/JSONSchema/Event） | `contracts/openapi.yaml`（56 path）+ `contracts/{jsonschema,events}`；`tools/contract-lint` 7/7 | ✅[实] | 契约先行落地 |
| 13 | §12.3 | 跨分册 ADR-001~003 | `docs/adr/ADR-001/002/003` | ✅[实] | 编号与篇名吻合 |
| 14 | §9/§10 | 前端骨架目录 | `web/src/{app,api,components,features,layouts,types,hooks,lib,styles}` | ✅[实] | 设计稿 `routes/` 由 `app/` 承载 |
| 15 | §14.1 | 工程资产含「部署手册/备份恢复/安全验收说明」 | `docs/` 无 runbooks/operations；`deployments/scripts` 空 | 🟡[实] | 运维文档未落地（README 亦缺） |
| 16 | §4.1 | 根目录 `README.md` | **不存在** | ⬜[实] | 见 4.2 |
| 17 | §11.8/§11.9 P6/P7 | 评估门禁 + 完整 11 步 E2E | `internal/` 无 evaluation/simulation/qualitygate 包；`tests/e2e` 空 | 🟡[实] | 硬前置仿真引擎（设计标 MVP-V1.0）未实现 |
| 18 | §8.3 | 与实现的差异（owner 账号 vs `odap_app`） | 实现改 `odap_app` | ✅[实] | **有意偏离（更安全）**：`odap` 是超级用户（`rolbypassrls=t`），以它连接会绕过全部 RLS，违反第二篇 §9.2。建议回写设计书 §8.3 |

**范围外**：第五篇本体工程完整闭环、第十二篇数据接入（**注：此"范围外"归因与设计书冲突，见 §5.1**）、第十五篇仿真引擎（**注：同上，见 §5.2**）、第十一篇 Prometheus/Trace、第十三篇存储选型 Spike 报告、第九篇 §4.4 能力探测。
**❓待确认**：① 第一篇 §4 与第十六篇 §4.1 目录结构互相冲突，仓库仅实现 16 篇版本——以哪篇为契约真源；② `tests/` 六目录空壳而 `Makefile` 引用它们——有意废弃还是待补；③ CI 缺失是否属"本波次范围排除"，抑或 P0 未达标（矩阵 §4 P0 标 ✅ 但无 CI 文件）。

---

### 4.18 第十七篇：企业级前端 UI

**规模实测**：`web/src` 共 **125 个 ts/tsx/css 文件、17,161 行**；`app/router.tsx` 共 **27 条路由**；`features/` 有 **19 个子目录、28 个页面文件**。构建脚本：`web/package.json` 含 `"build": "tsc --noEmit && vite build"` 与 `"typecheck": "tsc --noEmit"`（**无 lint 脚本**）。

| # | 章节 | 设计要求 | 实现证据 | 判定 | 缺口/说明 |
|---|---|---|---|---|---|
| 1 | §2/§5 信息架构三区 | 业务工作台 / 管理控制台 / 平台配置中心 | `layouts/BusinessLayout.tsx`、`AdminLayout.tsx`；27 路由 | 🟡[实] | 「平台配置中心」未独立分区，并入 `/admin` 子页 |
| 2 | §2.1 顶层导航 | 业务 6 项 + 管理 14 项，按权限动态 | `AdminLayout.tsx:46-74`；`Sidenav.tsx`；`route-guards.tsx` | ✅[码] | — |
| 3 | §11.1 路由清单 | 24 条主路由与页面/API 对应 | `app/router.tsx` | ✅[实] | — |
| 4 | §3.2 八色色彩 | 品牌/文字/背景/成功/警告/错误/信息 | `styles/tokens.css:10-18` | ✅[实] | 色值一致 |
| 5 | §3.3 字体间距 | 基础 14px、标题 24–28px、4px 倍数 | `tokens.css:39-110` | ✅[码] | — |
| 6 | §3.4 明暗主题 | 默认浅色，深色为后续增强 | `tokens.css:160 [data-theme=dark]`；`hooks/useTheme.ts`；两布局 toggle | ✅[实] | **矩阵 §6 标 ⬜ 已过时**（代码已实现暗色） |
| 7 | §3.2 颜色非唯一表达 | 状态 = 颜色 + 文字 + 图标 | `components/status-badge/status-maps.ts` | ✅[码] | 三元组齐全 |
| 8 | contracts/01 17 态 | Run 17 态全覆盖门禁 | `status-maps.ts:40 Record<RunStatus,StatusMeta>` | ✅[码] | 漏项编译失败 |
| 9 | §4.4 Run↔ERP 并列 | 审批通过 ≠ ERP 成功，分别呈现 | `RunErpStatusPair.tsx`；`ApprovalDetailPage.tsx` | ✅[码] | 含误导性成功警告 |
| 10 | §10.5 页面状态组件 | 加载/失败/空/部分/权限/过期/提交/未知/成功/失败 | `error-state/StateView.tsx`（10 kind） | ✅[码] | — |
| 11 | §12.3 表格规范 | 分页/可复制 ID/筛选/导出前鉴权 | `components/data-table/DataTable.tsx`；`copy-id` | ✅[码] | — |
| 12 | §12.2 无障碍 | WCAG 2.1 AA、键盘、aria-live、焦点 | `skip-link`；`StateView`/`RealtimeBanner` aria-live；`globals.css:63` | 🟡[子] | 基线达成，**无 axe 自动化门禁** |
| 13 | §9.2 危险操作确认 | 展示对象/影响/范围/可逆/核验 | `components/confirm-dialog/ConfirmDialog.tsx` | ✅[码] | — |
| 14 | §8.2 模型控制台 | 提供方/端点/凭据不回显/状态 | `features/llm-providers/pages/ModelsPage.tsx` 全 CRUD | ✅[码] | verify 端点在页内如实声明 |
| 15 | §8.5 身份与权限 | 用户/角色/绑定/模拟授权决策 | `features/policies/pages/IdentityPage.tsx`；`admin/UsersPage/OrgsPage` | 🟡[实] | 不展示权限/状态列；**`UsersPage`/`OrgsPage` 实测因 L1 报错** |
| 16 | §7.1 Agent 管理 | 版本/模型策略/工具/本体/评估/运行 | `features/agents/pages/AgentsPage.tsx` | 🟡[码] | 列表缺上述字段；**无 Agent 详情页** |
| 17 | §7.2 Tool Registry | Schema/权限/风险/幂等/超时/测试 | `features/tools/pages/ToolsPage.tsx` | 🟡[码] | 缺 risk_level 等，改用真实字段 `effect_class`；无 invoke |
| 18 | §7.3 工作流/Harness | 结构化节点编辑器、不可变版本 | `features/workflows/pages/WorkflowsPage.tsx` | 🟡[码] | 仅 plan-templates 列表，无节点编辑器 |
| 19 | §6.2/6.3 本体工作台 | 类关系/约束/SHACL/版本差异/发布回滚 | `ontology/pages/{OntologyListPage,OntologyDetailPage}.tsx`（3 tab） | 🟡[码] | 缺独立版本差异/回滚页 |
| 20 | §6.4 数据源/映射 | 字段映射/冲突分类/预览 | `data-mapping/pages/DataMappingPage.tsx`；`api.ts:18` | 🟡[实] | `DATA_SOURCES_ENDPOINT_AVAILABLE=false`，端点缺失 |
| 21 | §14.3 评估与门禁 | 评估结果驱动发布门禁 | `evaluation/pages/{EvaluationPage,ReleasesPage}.tsx` | 🟡[实] | 未过门禁禁发布 ✅；**promote/rollback 按钮必失败**（后端无路由） |
| 22 | §14.3 审计与评估 | 哈希链/决策 ID/证据包 | `audit/pages/AuditPage.tsx`、`evidence/pages/EvidencePage.tsx`、`hash-chain.ts` | ✅[码] | 含断链提示 |
| 23 | §4.6 执行报告 | 业务结果/过程/依据/审批/证据 5 tab | `tasks/pages/TaskDetailPage.tsx` | 🟡[码] | 仅 3 tab，缺依据/证据包独立 tab |
| 24 | §10.4/§10.2 技术栈与表单 | RHF + Zod、错误文案规范 | `package.json` build = tsc + vite；各 feature `schema.ts`；`api/errors.ts` | ✅[码] | 有 BASE/DOMAIN_CODES |
| 25 | §13.1 前端测试/门禁 | 组件/集成/契约/E2E/无障碍 | `tools/ui-verify.sh`、`ui-route-sweep.sh`、`ui-catalog-verify.sh`、`http-entry-cache-verify.sh` | 🟡[实] | 仅渲染/图标断言，**无单测/E2E 框架**；且**覆盖清单未含 3 个坏页面**（L4） |

**未建页面清单**（设计书要求但前端无页面/路由）：

| 设计书要求 | 缺失形态 |
|---|---|
| 通知中心（§12.4） | 仅有 `icon/bell`，无页面/组件 |
| 全局搜索（§5.2） | 无任何搜索组件 |
| 图谱实例浏览 / 图谱浏览器（§6.1、§6.5） | 无对应 feature 或路由 |
| 业务「历史记录」（§2.1 末项） | 无路由 |
| 独立「系统设置」页（§8.1 十四项一级菜单）与「任务队列与执行资源」（§8.6） | 仅散落在 users/orgs/models/connectors/policies 子页 |
| Agent 详情页 `/admin/agents/:id`（§7.1 十段详情） | 无路由 |
| 运行调试器 `/admin/runs/:id`（§7.4 Run/Step/Attempt/Trace） | 无路由 |
| 审计事件详情页 `/admin/audit/:id` | 无路由 |
| 本体「版本差异」独立视图与版本列表/回滚页（§6.1、§6.3） | 仅在 release tab 以 `StateView kind="stale"` 占位 |

**范围外**：移动端完整审批、通知邮件摘要 `[V1.5]`；低代码应用构建器 `[V2]`。
**❓待确认**：① i18n——设计书全篇无 i18n 章节，前端全部中文硬编码（按设计书属 ➖；若按"多语言能力"要求则为 ⬜）；② 矩阵 §6 的「暗色主题 ⬜」「DataMgmt 端点缺失 ⬜」与当前代码状态**已不一致**，需回写；③「平台配置中心」是否需从 `/admin` 拆为独立第三分区，设计书表述含混。

---

### 4.19 设计书评审与优化建议报告（对照材料）

| # | 项 | 结论 | 判定 | 说明 |
|---|---|---|---|---|
| 1 | 评审报告提出的结构性问题（模块边界、契约先行、阶段门禁） | 落地情况与正文各篇一致 | ✅[实] | 报告本身与总纲不冲突 |
| 2 | 报告建议的"证据链可验证" | 哈希链已实现但**运行期无校验调用方** | 🟡[实] | 见 4.12 #12 |
| 3 | 报告建议的"门禁自动化" | **无 CI**；门禁均为手动 `make`/脚本 | ⬜[实] | 见 L8 |

---

## 5. 范围归因争议（设计书 vs 自查矩阵 §6）

本清单在与矩阵 §6 对账时发现 **3 处归因冲突**。归因直接决定一个条目是"范围内未实现（应修）"还是"范围外（可不修）"，因此必须澄清。

### 5.1 第十二篇：数据接入与治理

| 来源 | 结论 |
|---|---|
| 自查矩阵 §6（第 638 行）与 §2（M13 行） | "设计书标 `[V1.5]`（MVP 用静态导入），**本波次按范围排除**" |
| **设计书自身**（第 5 行 scope 声明） | §4 数据源注册、§6 同步引擎、§7 三层数据模型、§8 语义映射、§10 质量引擎、§6.4 幂等链、§17.5 ERP 回执闭环、§21.1 报销链路 均为 **`[MVP-V1.0]`** |

**实测标注分布**：`grep -c '\[V1.5\]'` = **1 行**、`grep -c 'V2'` = **2 行**（其中 1 行为图片 URL 误命中），且全部集中在同一行 scope 声明中，正文无逐章标注。

**结论**：设计书自标为 MVP 范围内，矩阵的"范围排除"**缺乏原文支撑**。本清单按设计书口径判定 → 上表 §4.13 的 22 条中 **17 条为 ⬜ 范围内未实现**。

### 5.2 第十五篇：仿真引擎

| 来源 | 结论 |
|---|---|
| 自查矩阵 §4（P6 行）与 §6 | "仿真引擎属 V1.5；P6 硬前置" |
| **设计书自身**（第 7 行 scope 声明 + §22.0） | §七/§八 仿真引擎为 **`[MVP-V1.0]`**，且 §22.0 称其为"**P6 门禁硬前置、独立阶段**，退出条件为可稳定复现四类故障 + 环境隔离" |

**结论**：**设计书全文无任何将仿真引擎标为 `[V1.5]` 的文字**。按设计书口径，仿真引擎应为范围内 ⬜（且**阻断 P6/P7**），而非 ➖ 范围外。

### 5.3 第五篇：Ontology Factory

| 来源 | 结论 |
|---|---|
| 自查矩阵 §6 | "本体工程（制度解析）完整闭环 🟡，设计书自标 `[V1.5]`" |
| **设计书自身**（篇首 scope） | §6「8 阶段 Factory 流水线与断点续跑」为 **`[MVP-V1.0]`** |

**结论**：若以设计书为准，Factory 属范围内 ⬜；若以矩阵为准则属 ➖。**建议以设计书篇首 scope 为唯一真源**。

### 5.4 已过时/需回写的矩阵条目

| 矩阵条目 | 现状 | 动作 |
|---|---|---|
| §6「`GET /api/v1/ontologies/{id}` 端点缺失」（缺陷 #20） | 现已补齐（`router.go:62`、`config_crud.go:116`） | 标记为已闭环 |
| §6「暗色主题 ⬜」 | 已实现（`useTheme.ts` + `tokens.css:160`） | 改 ✅ |
| §6「DataMgmt 端点缺失 ⬜」 | 端点仍缺，但**归因需按 §5.1 修正** | 改为范围内 ⬜ |
| §2 M04「`ontologies/{id}/validate/publish` 端点已实现 ✅」 | validate **恒 503**、publish 允许 DRAFT 直发 | 降为 🟡 并登记 L3 |
| §4 P0「CI ✅」 | 无 CI 文件 | 需澄清判定依据（见 L8） |
| §4 P6「仿真引擎 ⬜（V1.5）」 | 归因错误（见 §5.2） | 改为范围内 ⬜ 并标"阻断 P6/P7" |

---

## 6. 门禁盲区分析：这些缺口为什么能长期存活

本次评测发现的所有关键缺陷，**都通过了项目现有的全部门禁**。归因如下（这是本清单最重要的方法论产出）：

| 盲区类型 | 具体表现 | 本次命中的缺陷 |
|---|---|---|
| **断言对象 ≠ 被判对象** | `TestOpenAPICoversAllRoutes` / `TestAllContractEndpointsRegistered` 只断言 `routes()` **声明表**与契约一致，**从不发 HTTP** | **L2**（logout/session 声明在表里，实际 404） |
| **静态"含字符串"当作"可用"** | 看到 `app.current_tenant_id` 出现在 SQL 里就认为租户上下文 OK；未验证 GUC 名与 RLS 函数所读变量是否同一个，也未验证 SQL 能否被解析 | **L1**（`SET LOCAL x = $1` 语法不成立 + GUC 名错配） |
| **门禁覆盖清单手工枚举** | UI 验收只覆盖 6 个页面，而设计书要求的路由有 27 条 | **L4**（3 个坏页面从未被扫） |
| **单测通过当作系统可用** | 库代码完整 + 单测全绿，但无任何生产 import（§3 接线矩阵） | **L7** 及第八/九/十三/十四篇的 23 条 🟡 |
| **"表存在"当作"能力存在"** | 建了表就认为功能就绪；未检查是否有写入路径 | 第四篇 7 张零引用表、`run_events`、`workflow.instances` |
| **范围标签未回原文核对** | 用"设计书标 V1.5"排除整篇，而未在原文中定位该标签 | **§5.1**、**§5.2** |

### 建议新增的 4 条门禁（可执行、可证伪）

1. **路由可达性门禁**：遍历 `Routes(h)`，对每条发一次真实 HTTP 请求（无副作用的方法），断言响应**不是 Go 默认的 `404 page not found` 纯文本**。→ 直接抓住 L2。
2. **装配接线门禁**：从 `cmd/` 出发做一次传递闭包的可达性分析，列出**不可达的 `internal/` 包**并断言该清单为空（或与白名单一致）。→ 直接抓住 L7。
3. **SQL 静态门禁**：禁止 `SET [LOCAL] <param> = $n` 形式；并断言全仓出现的租户 GUC 名与 `000011` 中 RLS 函数读取的名字**只有一套**。→ 直接抓住 L1。
4. **UI 覆盖清单自动生成**：目录类页面渲染门禁的页面列表改为由 `web/src/app/router.tsx` 与 `contracts/openapi.yaml` 的交集生成，而非手工枚举。→ 直接抓住 L4。

> 共同特征：**门禁的"被判对象"必须与"真实用户会经过的路径"同源**。凡是手工枚举清单、只读声明表、只看单测结果的门禁，都会随实现演进而静默失效。

---

## 7. 修复优先级建议

| 优先级 | 事项 | 理由 | 触及面 |
|---|---|---|---|
| **P0（用户可见破坏）** | 修 L1：删除 `store.setTenant()`，全部改走 `s.TenantTx(ctx, fn)` | 3 个管理页面直接 500；23 个 Store 方法受影响 | `auth_store.go`、`ontology_category_store.go`、`session_store.go` |
| **P0** | 修 L2：auth 路由前缀遮蔽（`logout`/`session` 404） | 契约已登记却不可达；登出功能失效 | `internal/api/router.go:174-203` |
| **P1（门禁补强）** | 落地 §6 的 4 条新门禁（可达性 / 接线 / SQL / UI 覆盖清单） | 现有门禁对上述缺陷**全部是假阴性** | `tools/`、`internal/api/router_test.go` |
| **P1（范围澄清）** | 裁定 §5.1/§5.2/§5.3 三处归因，并回写矩阵 §6 | 决定约 20 条"未实现"是否纳入修复范围 | `docs/ODAP-MVP-设计符合性自查矩阵.md` |
| **P1（安全）** | `evidence_records` 补 append-only 触发器；哈希链 `Verify` 接入运行期 | 审计完整性目前只在测试中成立 | `migrations/`、`internal/audit/` |
| **P2（能力闭环）** | 在 `cmd/worker` 接入 `internal/agent` 执行驱动 | 打通"Run → LLM → 工具 → ERP"，是 E2E 的前置 | `cmd/worker/` |
| **P2（可信度）** | 补齐 CI（格式/静态分析/单测/契约/迁移校验） | 第十六篇 §10.1 与 P0 退出条件 | `.github/workflows/` 或等价 |
| **P2（文档对齐）** | 回写设计书：`odap_app` 替代 owner、`MOCKERP_DB_HOST_PORT`、事件名漂移、PlanIR 枚举分歧 | 消除设计-实现双向漂移 | 第二/六/十四/十六篇 |
| **P3** | 第十一篇可观测性补 app 侧 OTel exporter；第十五篇评估执行引擎 | 均为 `[V1.5]` 或 P6 硬前置，非当前阻塞 | — |

---

## 8. 可复现命令

```bash
# —— 前置：栈需处于运行状态 ——
#   bash start.sh          （轮询 /readyz 真实查库等待就绪）

# 0) 证据等级 [实] 的三条活体缺陷（§2.1 / §2.2 / §2.3）
unset http_proxy https_proxy; export no_proxy=127.0.0.1,localhost
H_T="X-Odap-Dev-Tenant: 11111111-1111-1111-1111-111111111111"
H_S="X-Odap-Dev-Subject: 22222222-2222-2222-2222-222222222222"
H_C="X-Odap-Dev-Scopes: admin:read admin:write"

# L1：三个端点应为 500 "SET LOCAL tenant 失败"
for p in /api/v1/admin/users /api/v1/admin/organizations /api/v1/admin/ontology-categories; do
  curl -s -o /dev/null -w "%{http_code}  $p\n" -H "$H_T" -H "$H_S" -H "$H_C" "http://127.0.0.1:8080$p"
done
# 对照：走 s.TenantTx 的端点应为 200
curl -s -o /dev/null -w "%{http_code}  /api/v1/admin/role-bindings\n" -H "$H_T" -H "$H_S" -H "$H_C" http://127.0.0.1:8080/api/v1/admin/role-bindings

# L2：应为 404（Go 默认纯文本，不是 JSON 错误体）
curl -s -i -X POST -H "$H_T" -H "$H_S" -H "$H_C" http://127.0.0.1:8080/api/v1/auth/logout | head -1
curl -s -i        -H "$H_T" -H "$H_S" -H "$H_C" http://127.0.0.1:8080/api/v1/auth/session | head -1

# L3：应为 503（诚实报错，非假成功）
curl -s -X POST -H "$H_T" -H "$H_S" -H "$H_C" http://127.0.0.1:8080/api/v1/ontologies/00000000-0000-0000-0000-000000000000/validate

# 1) L1 的根因（真实 PostgreSQL 探针）
MSYS_NO_PATHCONV=1 docker compose exec -T postgres psql -U odap -d odap <<'SQL'
-- ① SET 不接受参数占位符
DO $$ BEGIN EXECUTE 'SET LOCAL app.probe_x = $1' USING 'abc';
         RAISE NOTICE 'SET 接受参数'; EXCEPTION WHEN OTHERS THEN RAISE NOTICE 'SET 拒绝参数: %', SQLERRM; END $$;
-- ② 写 app.current_tenant_id 对 RLS 函数无效（两列均应为空）
BEGIN; SET LOCAL app.current_tenant_id = '11111111-1111-1111-1111-111111111111';
        SELECT current_setting('app.tenant_id', true) AS guc, app.current_tenant_id() AS fn; ROLLBACK;
-- ③ 正确姿势
BEGIN; SELECT set_config('app.tenant_id','11111111-1111-1111-1111-111111111111', true);
        SELECT app.current_tenant_id(); ROLLBACK;
SQL

# 2) 代码侧：两套租户注入路径
grep -rn "setTenant(ctx" --include=*.go internal/          # 有缺陷的实现（23 处）
grep -rln "s\.TenantTx(" --include=*.go internal/api/store/ # 正确的实现（8 个文件）
sed -n '8,12p' migrations/platform/000011_add_rls_and_hash_chain.sql  # RLS 函数读 app.tenant_id

# 3) §3 接线矩阵：零装配的包
for p in agent registry evidence ontology execution; do
  printf "%-12s %s\n" "$p" "$(grep -rn "odap/internal/$p\"" --include=*.go internal/ cmd/ mockerp/ | grep -v _test.go | grep -v "^internal/$p/" | wc -l)"
done

# 4) 7 张零引用插件表
for t in plugin_artifacts plugin_capability_declarations plugin_dependencies \
         plugin_publications plugin_reviews plugin_signatures plugin_validation_runs; do
  printf "%-34s %s\n" "$t" "$(grep -rn "$t" --include=*.go . | grep -v node_modules | wc -l)"
done

# 5) L5：非内部触发器只有两个
MSYS_NO_PATHCONV=1 docker compose exec -T postgres psql -U odap -d odap -c \
 "SELECT n.nspname||'.'||c.relname AS tbl, t.tgname FROM pg_trigger t
    JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace
   WHERE NOT t.tgisinternal AND n.nspname IN ('audit','graph','registry','ontology') ORDER BY 1;"

# 6) L8：CI / README / docs 子目录 / tests
find . -maxdepth 3 \( -name '*.yml' -o -name '*.yaml' -o -name 'Jenkinsfile' \) -not -path '*/node_modules/*'
ls README* 2>/dev/null || echo "无 README"; ls -d docs/*/; ls -R tests/ | head

# 7) 设计书范围标签实测（§5 归因争议）
grep -c '\[V1.5\]' docs/第十二篇*.md; grep -c 'V2' docs/第十二篇*.md
grep -c '\[V1.5\]' docs/第十五篇*.md

# 8) 现有门禁（对上述缺陷全部为假阴性——这正是问题所在）
bash tools/contract-lint            # 期望 7/7
bash tools/ui-catalog-verify.sh     # 期望全绿（但不含 3 个坏页面）
bash tools/http-entry-cache-verify.sh
```

---

## 9. 结论

1. **P0–P5 范围内的主体确实已交付**：状态机 17×31 全覆盖、审计哈希链、RLS 纵深防御、五元组幂等、三层语义缓存、Mock ERP 五类故障注入等，均有真实实现与测试。
2. **但"已交付"与"用户可用"之间仍有一层裂缝**，且该裂缝**全部落在现有门禁的盲区内**：3 个管理页面 500、2 个 auth 端点 404、本体校验恒 503、审计完整性只在测试中成立、6 个包零装配。
3. **最需要先做的不是补功能，而是补门禁**（§6 的 4 条）与**澄清范围归因**（§5 的 3 处）。前者决定同类缺陷能否被自动拦住，后者决定约 20 条"未实现"是否要进排期。
4. **一条可复用的方法论**：本清单中每一条 `[实]` 结论，都来自"**换一个与实现者不同的入口去观察**"——发真实 HTTP 而不是读声明表、跑真实 SQL 探针而不是读 SQL 字符串、从 `cmd/` 做可达性分析而不是数文件个数。**门禁的价值不在于数量，而在于它与真实用户路径同源。**

---

## 10. 修复实施记录（第二轮：动手整改）

> 本节是对 §1–§9 的**整改回执**。判定口径不变（✅/🟡/⬜/➖），证据等级不变
> （`[实]` 活体实测 / `[码]` 源码核验）。每条修复都给出**可复现的验证证据**，
> 并如实记录**修复过程中新发现的缺陷**与**对原清单结论的修正**。

### 10.1 本轮已完成并通过验证的修复

| 编号 | 缺陷 | 修复内容 | 验证证据（等级 `[实]`） | 状态 |
| --- | --- | --- | --- | --- |
| L1 | 租户上下文注入必然失败（3 端点 500） | 删除 `store.setTenant()`；新增 `Store.TenantTxAs(ctx, tenantID, fn)`，底层只走 `pgxutil.WithTenantTx`；22 处调用点全量改写（auth_store 16 + ontology_category_store 5 + session_store 1）；`RegisterUser` 保留单事务并直接复用同一原语 | PostgreSQL 探针：`SET LOCAL app.current_tenant_id = $1` 报 `syntax error at or near "$1"`；写 `app.current_tenant_id` 时 RLS 函数返回空，写 `app.tenant_id` 时返回 UUID。端到端：`POST /api/v1/admin/users` → 201；`GET /api/v1/auth/session` → 200（含 org_path 财务部） | ✅ |
| L2 | `auth/logout` 与 `auth/session` 纯文本 404（前缀遮蔽） | `RouteSpec` 增加 `Public` 字段，把"是否公开"并入路由单一真源；`NewRouter` 按**完整 (方法, 路径)** 逐条挂载公开路由，废除 `/api/v1/auth/` 前缀挂载 | 新增可达性门禁：临时恢复旧挂载后**精确变红**并指出 `POST /api/v1/auth/logout ... text/plain 404`；恢复修复后全绿 | ✅ |
| L2b | 会话标识丢失（身份模型语义重载） | `tenantctx.Identity` 新增 `SessionID`；`identity.SessionService.Validate` 回传 `SessionID` 而不再借用 `RequestID`；`SessionResolver` 不再覆盖；`Logout` 改用 `id.SessionID`；`DevResolver` 支持可选 `X-Odap-Dev-Session` | 端到端：Bearer 令牌 `POST /auth/logout` → **204**；数据库核对 `revoked_at=2026-10-13 14:15:23+00 / reason=logout`（证明 UPDATE 真的命中并改写了行）；再次请求 → **401 `IAM_SESSION_REVOKED`** | ✅ |
| L1b | 9 处 SQL 把同一占位符用于两个不同列（恒命中 0 行 → 恒 404） | `RevokeSessionByID` / `UpdateUser` / `DisableUser` / `UpdateOrganization` / `DisableOrganization` / `RemoveMembership` / `UnbindRole` / `UpdateOntologyCategory` / `DeleteOntologyCategory` 全部拆分为独立占位符 | 同上端到端 204 + DB 行改写；另由新增 SQL 绑定门禁静态复核 | ✅ |
| L1c | `BindRole` 传 7 个实参 / SQL 只有 6 个占位符，且 `granted_by` 写入租户 ID（违反 `REFERENCES iam.users(id)`） | 签名增加 `grantedBy ids.ID`，SQL 改为 `VALUES ($1..$7)`，由 handler 传入 `id.SubjectID` | 编译期与静态门禁双重约束；`granted_by` 现为合法用户主体 | ✅ |
| L3a | 发布门禁放行 `DRAFT` 直发 | `PublishOntology` 白名单收敛为 `VALID` / `APPROVED`（第五篇 §11.1） | 端到端：对 DRAFT 版本 `POST /ontologies/{id}/publish` → **409 CONFLICT**，且数据库侧版本仍为 `DRAFT`、`published_at=NULL`（无部分写入） | ✅ |
| L3b | 本体校验记录结构漂移 | 迁移 `000023` 补 `validation_runs.validation_profile` / `summary_json` / `created_at`，并新建 `ontology.validation_findings`（11 列）+ 索引 + RLS | 实测：3 个补列存在；`validation_findings` 11 列；策略 `validation_findings_tenant_isolation` 已生效 | ✅ |
| L5 | `evidence_records` 缺 append-only 触发器 | 迁移 `000025` 增加 `trg_evidence_records_append_only`（与 000007 的 `audit_events` 同构） | 以**表 owner** 身份实测：`UPDATE` / `DELETE` 均被拒绝并报 `audit: evidence_records is append-only` | ✅ |
| L4+ | 门禁盲区（§6） | 新增 3 条可证伪门禁 + `Makefile gates` 目标（详见 10.4） | 见 10.4 | ✅ |

### 10.2 修复过程中**新发现**的缺陷（原清单漏报）

| 编号 | 缺陷 | 发现方式 | 处置 |
| --- | --- | --- | --- |
| N1 | `internal/registry/service.go:803`：SQL 使用 `$4` 但只绑定 3 个实参 → 回滚激活路径必然 500（`bind message supplies 3 parameters, but prepared statement requires 4`） | **新增的 SQL 绑定门禁**在第一次运行时就抓到（52 个测试文件与既有契约门禁全部漏报） | 已修为 `$3`；门禁转绿 |
| N2 | 会话标识被塞进 `RequestID` 后被请求 ID 覆盖（见 L2b） | 追查 L2 时发现 logout 即使可达也拿不到会话 ID | 已修（新增 `SessionID` 语义位） |
| N3 | `ontology.validation_findings` 表整体缺失 → 校验失败时无法给出"哪条约束 / 哪个焦点节点 / 哪条路径" | 设计书 §5.3 DDL ↔ 迁移 000004 逐列核对 | 已补（迁移 000023） |
| N4 | 公开路由用"前缀挂载 + switch 枚举"表达 | 修复 L2 时识别为**同一缺陷的成因**，而非独立缺陷 | 已改为路由表内 `Public` 标记 |

### 10.3 对原清单结论的**修正**（诚实纠错）

1. **L5 的严重度被高估。** 原清单写"`audit.evidence_records` 无 append-only 触发器 ⇒ 表可被改写"。
   实测 `has_table_privilege('odap_app','audit.evidence_records','UPDATE') = false`、
   `DELETE = false` —— 迁移 000011 早已 `REVOKE UPDATE, DELETE ... FROM odap_app`，
   **应用角色确实改不动**。准确表述应为："权限层已隔离，但缺少触发器层，
   因此**表 owner（odap，即迁移与运维连接）**仍可改写，与同 schema 下
   `audit.audit_events` / `registry.plugin_versions` 的双保险不一致。"
   本轮按此口径补齐触发器（10.1 的 L5 行）。
2. **L1 的"3 端点 500"是症状下界，不是全集。** L1 的实际爆炸半径是
   **22 个方法**（在事务内调用 `setTenant` 的全部方法），
   其中 9 个还叠加了独立的占位符复用缺陷（L1b）。
   原清单按"打到的 3 个端点"计数，低估了影响面。
3. **§6 的"门禁盲区"判断被本轮反向验证。** 新增的 SQL 绑定门禁在**首次运行**
   即抓到 1 个真实缺陷（N1），而原有 52 个测试文件与 `contract-lint` 全绿。
   这直接印证了 §6 的判断——单测全绿与系统可用之间确有缺口。

### 10.4 新增门禁（均已验证可证伪）

| 门禁 | 类型 | 防住的缺陷类 | 可证伪性验证 |
| --- | --- | --- | --- |
| 路由可达性 | Go 测试（真实 HTTP 逐条探测 routes() 全量 84 条） | 端点"已注册"却不可达（前缀遮蔽） | 临时恢复旧挂载 → 精确变红并打印 `POST /api/v1/auth/logout ... text/plain 404`；恢复后全绿 |
| 租户上下文注入 | 静态（`tools/gates/tenant_context_gate.py`） | `SET LOCAL app.current_tenant_id = $1`、第二份注入实现、`SET ... = $n` | 首轮误报（命中说明性注释）已修（剥离注释）；修正后 125 个非测试文件全绿 |
| SQL 绑定实参个数 | 静态（`tools/gates/sql_argument_gate.py`） | 占位符个数与实参个数不符（N1） | 首轮 8 处命中中 7 处为"尾随逗号"误报，已修；修正后从 177 个带参调用中**只剩 1 处真实缺陷**（N1） |
| 契约一致性（复用） | `tools/contract-lint` | 文档 / 常量 / DDL / 前端标签漂移 | 7 项全绿 |

**门禁设计的一条纪律（本轮实证）：** 判据必须"要么精确、要么不做"。
SQL 占位符复用（`WHERE tenant_id = $1 AND id = $1` vs 合法的
`m.tenant_id = $1 ... t.id = $1`）在**词法上完全同形**，
无数据流分析则必然误报 —— 因此本门禁把它降级为**人工复核清单**（ADVISORY），
并明确要求由端到端断言覆盖，而不是硬塞进强制项后被加白名单绕过。

### 10.5 本轮**未完成**（如实列出，不进"已完成"）

| 项 | 缺口 | 阻塞原因 |
| --- | --- | --- |
| 本体校验链（L3 主体） | `POST /ontologies/{id}/validate` 仍返回 503 | 制品内容（OWL/SHACL Turtle）**从未落库**：`ontology.ontology_artifacts` 只有 `content_digest` / `storage_uri`，无 `content` 列，且仓内**无 Turtle 解析器**。要真正驱动 `internal/ontology/{rdfs,shacl}` 需先补齐"对象存储 / 制品加载器"——该项属实现侧新工作，非一行接线 |
| L4 装配接线门禁 | 未落地 | `internal/{agent,registry,evidence,ontology}` 生产 import 仍为 0（与 L7 同源），现阶段强制会立即变红；需与 L7 一并推进 |
| L7 | `cmd/worker` 未接入 `internal/agent` 执行驱动 | 需实现 Agent 执行循环，是独立功能开发 |
| L8 | 无 CI、无 README、`docs/` 缺 5 个子目录、`tests/` 六目录为空（`Makefile` 的 `test-integration` / `test-e2e` 指向空目录） | 工程底座，工作量独立 |
| 其余 ⬜ / 🟡 条目 | §1 统计的 ⬜41 / 🟡78 中的大部分未动 | 见 §4 逐篇清单 |

### 10.6 本轮产生的可复现验证命令

```bash
# 1) 三条静态/集成门禁
bash tools/gates/run-gates.sh                    # 或 make gates
python tools/gates/tenant_context_gate.py .
python tools/gates/sql_argument_gate.py .

# 2) L1 根因（真实 PostgreSQL 探针，与镜像无关，可独立复现）
#    错误写法：SET 直接语法报错 + RLS 函数读到 NULL
#    正确写法：set_config('app.tenant_id', ...) → RLS 函数返回该 UUID
docker exec -e PGPASSWORD="$PGP" odap-postgres-1 psql -U odap_app -d odap -t -A -c \
  "BEGIN; SET LOCAL app.current_tenant_id='11111111-1111-1111-1111-111111111111'; SELECT app.current_tenant_id()::text; ROLLBACK;"
docker exec -e PGPASSWORD="$PGP" odap-postgres-1 psql -U odap_app -d odap -t -A -c \
  "BEGIN; SELECT set_config('app.tenant_id','11111111-1111-1111-1111-111111111111',true); SELECT app.current_tenant_id()::text; ROLLBACK;"

# 3) L1 / L2 端到端（含数据库侧行改写核对）
#    创建用户 → 登录 → Bearer 注销 → 核对 revoked_at → 复请求应 401
#    （命令见本轮记录；关键断言：注销 204，DB revoked_at 非空，复请求 IAM_SESSION_REVOKED）

# 4) L3a 发布门禁
#    建 DRAFT 本体 → POST /publish → 期望 409，且 DB 仍为 DRAFT / published_at NULL

# 5) L5 触发器（以 owner 身份，触发器应拦截）
docker exec -e PGPASSWORD="$MP" odap-postgres-1 psql -U odap -d odap -c \
  "UPDATE audit.evidence_records SET summary='x' WHERE id=(SELECT id FROM audit.evidence_records LIMIT 1);"

# 6) 迁移回执
bash tools/dev-run.sh go run ./cmd/migrate up     # 000023 / 000025 已应用
```

---

## 11. 修复实施记录（第三轮：图数据库存储落地与交付门禁接线）

**触发**：`docs/图数据库的选型.md`（§四"最终技术选型建议"）要求把 Fuseki + TDB2 作为第一版
RDF 基础设施，并明确"Go 与 Fuseki 通信经 **GraphStore Adapter** 统一访问"、"**不要**让所有
Go 业务模块直接拼接 SPARQL 并访问 Fuseki"。本轮把该结论从文档落为**可运行、可证伪**的实现。

### 11.1 落地内容

| 层 | 产物 | 关键约束 |
| --- | --- | --- |
| 契约 | `internal/ontology/graphstore/store.go` | `GraphStore` 接口**去掉 `pgx.Tx`**（原实现把它当方法参数，等于把接口绑死在 PG 上）；新增 `Capabilities{ResourceDictionary,PersistentGraphID,FactMetadata}`；PG 专有的词项资源字典拆为**可选接口** `TermDictionary`，用类型断言探测而非塞进主接口 |
| 文本层 | `internal/ontology/graphstore/rdf.go`（新） | 词项/命名图文本构造的**唯一**位置：N-Triples 转义、IRI 转义、语言标签与 datatype 校验、`xsd:string` 归一化、空白节点提升 |
| 后端 | `internal/ontology/graphstore/fusekistore.go`（新） | SPARQL 1.1 Protocol（`/query`、`/update`）；`Capabilities()` 全 `false` 且**不实现** `TermDictionary`；图句柄由 `sha256(tenant, graphIRI)` 稳定推导 |
| 装配 | `internal/ontology/graphstore/factory.go`（新） | **唯一**入口 `graphstore.Open`；`GRAPHSTORE_MODE` 非法值**报错**（不静默回退）；默认仍为 `postgres` |
| 自检 | `cmd/api/graphcheck.go`（新） | `api graphstore-check`，17 项端到端断言 |
| 运行期 | `cmd/api/main.go` | 装配 + 组合就绪探针 `readinessProbe(pool, graphStore)`（原为 `pool.Ping`，会"把未知状态报告为成功"） |
| 部署 | `compose.yaml`、`.env.example` | `fuseki` 服务置于 `profile: graphstore`；`api` **刻意不写** `depends_on: fuseki`（可选 profile 未启用时默认 `up` 不应报错） |
| 门禁 | `tests/unit/arch_test.go` | 4 条新门禁：SPARQL/Fuseki 标识**不得出现在 `graphstore` 之外**、扫描器须命中非零、装配必须发生、就绪探针必须已重绑 |
| CI | `tools/ci/12-graphstore.sh`、`graphstore-check.sh`、`tools/fake-sparql/rdflib_endpoint.py`（新） | L1 已配置端点 → L2 compose 临时 Fuseki → L3 RDFLib 真实引擎；三级都不可用则 SKIP |

### 11.2 活体证据（真实 SPARQL 1.1 引擎，非替身）

`bash tools/ci/12-graphstore.sh` → L2 因本机代理截断镜像层失败 → 正确降级 L3 →

```
图存储自检：backend=fuseki endpoint=http://127.0.0.1:18080/odap auth=false
  ✓ 图存储可达性（Ping）                ✓ 能力声明自洽（ResourceDictionary） 声明=false 实现=false
  ✓ EnsureGraph 幂等调用               ✓ 零租户 Match / InsertTriples 必须被拒绝
  ✓ InsertTriples 首轮写入 新增 3 条（含转义字面值与空白节点）  ✓ 重复写入幂等 新增 0 条
  ✓ CountTriples 3 条                 ✓ Match（谓词绑定）1 条，宾语=128.50^^xsd:decimal
  ✓ Match（全通配）3 条                ✓ Match（跨命名图通配）3 条
  ✓ 空白节点可寻址（kind 还原为空白节点）  ✓ 跨租户 Match 隔离（另一租户 0 条）
  ✓ 跨租户 CountTriples 隔离（0）       ✓ DeleteTriples（已存在）1 条 / 幂等 0 条
  ✓ 清理后计数归零
汇总：通过 17 · 失败 0 · 跳过 0        ✔ 12 图存储契约自检（96s）   EXIT=0
```

**为什么必须用真实引擎**：`internal/ontology/graphstore` 的 HTTP 替身只能验证"适配器发了什么
请求、怎么解析响应"，验证不了生成的 SPARQL 是否合法。本轮即被该结论反噬三次（见 §11.3）。

### 11.3 本轮自检抓出的 3 个**真实缺陷**（替身测试全绿时已存在）

| # | 缺陷 | 为什么替身测不出 | 修法 |
| --- | --- | --- | --- |
| G1 | 空批次提前返回发生在**租户校验之前** | 替身只按请求体语义应答，"零租户 + 空批次"组合未被构造 | 新增 `validateWriteArgs`/`validateGraphReadArgs` 并**前置**到空批次判断之前；三个后端共用（MemStore 同步补齐，否则它作为 PGStore 测试替身不可信） |
| G2 | `Match` 生成 **4 词项图模式**（先把绑定常量全写出、再补 `?s ?p ?o`） | 替身只做 `strings.Contains`，语法非法仍能"匹配" | 改为 `buildTriplePattern` **按位置**生成；并补**精确文本**回归断言（不再只判断包含关系） |
| G3 | 依赖引擎把绑定常量**回显为变量**（rdflib 不回显） | 替身的响应是我自己构造的，天然按我的预期回显 | 改为只投影自由变量（`freePositions`）、按 `Pattern` 重建绑定；全绑定改走 `ASK` |

结论：**"替身全绿"不等于"真的能跑"**。这三条都属"静默失效"类——若没有 L3 真实引擎，它们会
以"SPARQL 语法错 / 结果缺变量"的形式在生产首次查询时才暴露。

### 11.4 ADR 编号冲突（发现并纠正，避免文档互相矛盾）

代码与注释起初引用 `ADR-004`。核对发现 **ADR-004 已被第十七篇两处预定**给"前端数据层选型
（状态：待评审）"（§5 scope 标签、§10.3）。若强行占用，会使该篇的引用指向错误的决策。
处理：图存储决策改占用 **`docs/adr/ADR-005-图存储选型与GraphStore适配器边界.md`**，并把
`compose.yaml`、`cmd/api/main.go`、`README.md`、`Makefile`、`tools/ci/12-graphstore.sh`、
`internal/ontology/graphstore/{rdf,fusekistore,factory}.go`、`tests/unit/arch_test.go` 中
共 11 处引用全部更正；`docs/decisions/README.md` 登记 ADR-004 的预定状态。**未改写第十七篇。**

### 11.5 交付门禁与文档接线

- `Makefile`：新增 `test-graphstore` / `graphstore-check` 目标并进 `.PHONY` 与 `help`。
- `README.md`：§3 补命令、§4 增"图存储契约自检"层、§5 阶段表与作业编号说明。
- `.github/workflows/ci.yml`：新增 `graphstore` 作业（`setup-python` + `pip install rdflib`，
  `GRAPHSTORE_CHECK_PY=python3`，绕开 127.0.0.1 代理），并接入 `quality_gate.needs`；
  作业编号重排为执行顺序（图存储自检 = 10，Quality Gate = 11，制品 = 12，冒烟 = 13），
  与 `tools/ci/*.sh` 的级号（10-artifact / 11-smoke / 12-graphstore）内容一一对应、数字不同。

### 11.6 对 §10.5 的状态更新（原文保留，不删除）

- **L8（工程底座）**：CI（13 作业）、README、`docs/` 子目录、`tests/` 六目录均已就位，
  `Makefile` 的 `test-integration` / `test-e2e` 现已指向非空目录并跑通 → 可从"未完成"移出。
- **L3 主体（本体校验链）**、**L4（装配接线门禁）**、**L7（worker 接入 agent 执行驱动）**：
  状态不变，仍为未完成（原因见 §10.5）。L4 与 L7 同源，须一并推进。

### 11.7 本轮产生的可复现验证命令

```bash
export PATH="/c/Users/JokerZ/go-sdk/go/bin:$PATH" GOFLAGS=-mod=mod GOPROXY=off

# 图存储契约自检（L1/L2/L3 三级降级；L3 需可 import rdflib 的解释器）
GRAPHSTORE_CHECK_PY="<带 rdflib 的 python>" bash tools/ci/12-graphstore.sh

# 对已有端点直接跑 17 项断言
bash tools/ci/graphstore-check.sh http://127.0.0.1:3030/odap

# 起一个真实 Fuseki（本机 Docker 可正常拉镜像时）
docker compose --profile graphstore up -d fuseki

# 单元 + 架构门禁
go test ./internal/ontology/... ./tests/unit/... -count=1

# 本地完整流水线（现含第 12 阶段）
bash tools/ci/run-all.sh --no-smoke
```

---

## 12. 修复实施记录（第四轮：规则侧 SHACL 真校验卡门禁 + 前端契约对齐）

> 本节延续 §10 / §11 的口径：**只写有实测证据的结论**，未完成项如实列出。

### 12.1 L3 主体闭环——并且**没有**走 Turtle 解析器这条路

§10.5 把 L3 的阻塞点记为"制品内容从未落库 + 仓内无 Turtle 解析器"。本轮复核后判定：
这个阻塞成立，但**它不是唯一路径**。`shacl.Validate` 收的是 `[]shacl.Shape`、
`graphstore.InsertTriples` 收的是 `[]graphstore.Triple`——两者都吃**结构化输入**，
并不要求先落一份 Turtle 文件。于是改为"manifest → 结构化三元组与形状"直接投影：

| 环节 | 落点 | 状态 |
| --- | --- | --- |
| 生成段产出形状与实例 | `internal/api/handlers/generation.go`（`ontologyManifestSchema` 增 `shapes` / `instances`） | ✅ |
| manifest → TBox/ABox 三元组 + shapes | `internal/ontology/graphload`（新包） | ✅ |
| 校验记录与版本状态流转 | `internal/api/store/ontology_validation.go`（表由迁移 `000023` 提供） | ✅ |
| 接线 + 反假绿探针 | `internal/api/handlers/ontologies.go` `ValidateOntology` | ✅ |

`POST /api/v1/ontologies/{id}/validate` 现为**同步**执行链路：
`ParseManifest` → `BuildShapes` → 幂等装载进确定性命名图 →
**反假绿探针** → `shacl.Validate` → 单事务落 `validation_runs` + `validation_findings`
→ 版本置 `VALID`/`REJECTED`。发布门禁（`PublishOntology`）只放行 `VALID`/`APPROVED`，
`DRAFT` 已在更早一轮移出白名单，因此 §2.3 的"DRAFT 可直发"组合缺陷不再成立。

**反假绿探针（本轮新增的关键机制）**：SHACL 在**空图**上按规范返回
`conforms=true` 且无 finding，与"数据全部合规"的返回值**完全一致**。
若照常落库，就得到一个"显示通过、实际什么都没查"的门禁。
因此端点先统计**属于 `sh:targetClass` 的实例节点数**（`graphload.CountTypedNodes`），
为 0 时直接 422 拒绝，而不是返回一个空洞的通过。

> 注意探针必须数**属于目标类的实例**，不能数三元组总数：只装载 TBox 时三元组数 > 0，
> `CountTriples > 0` 会被骗过。该断言已在 `TestProbe_CatchesTheFalseGreen` 中先证明
> "空图确实假绿"、再证明"探针能识破"、并额外证明"只有 TBox 时也骗不过"。

### 12.2 本轮由新门禁抓出的**真实契约漂移**（原清单未记载）

新增门禁 ⑤（见 §12.3）后立刻抓出 7 处前端-后端契约不一致，全部属实：

| 端点 | 前端原声明 | 服务端实际 | 后果 |
| --- | --- | --- | --- |
| `POST /ontologies/{id}/validate` | `AsyncTask`（202+任务号） | 200 + 同步校验报告 | 页面轮询不存在的任务号；真实结论（conforms/findings）**根本不展示** |
| `POST /ontologies/{id}/publish` | `AsyncTask` + 请求体 `{release_notes}` | 200 + `{version_id, status}`；只认 `{version, environment}` | **发布说明被静默丢弃**（`decodeJSONLoose` 不校验未知字段）；且目标环境静默落到 `DEVELOPMENT` |
| `POST /agents/{id}/publish` | `AsyncTask` | 200 + `{agent_id, version_id, status}` | 成功横幅写"已受理（202）…请跟踪发布任务"，而发布**已完成** |
| `POST /workflows/{id}/publish` | 同上 | 同上 | 同上 |
| `POST /runs/{id}/cancel` | 内联 `{accepted: boolean}` | 202 + `{id, status}` | `accepted` 恒 `undefined` |
| `POST /runs/{id}/reconcile` | 内联 `{accepted; reconciliation_id?}` | 202 + `{task_id, kind, status, status_url}` | 页面"相关编号"**恒显示 `—`**（字段名错误，非潜伏问题） |

**最危险的一条是发布目标环境**：前端不传 `environment`，服务端默认 `DEVELOPMENT`。
顶部栏显示"生产环境"时发布，会静默发到开发环境且不报错。本轮改为由
`PUBLISH_ENVIRONMENT: Record<EnvName, …>` 映射顶部栏环境并显式传参；
用全键 `Record` 而非带 `default` 的 `switch`，使将来新增环境值时**编译失败**而不是静默落到默认分支。

### 12.3 新增门禁 ⑤：前端写方法 ↔ `contracts/openapi.yaml`

落点为 `tools/contract-lint/main.go`（已并入 `tools/gates/run-gates.sh` 门禁 4/5）。
三条断言，**全部经反例自证可失败**：

1. 前端调用的写端点必须在 openapi 中登记 → 注入 `apiPost('/definitely/not/registered')` 后 FAIL ✅
2. `AsyncTask` 只能用于声明了 202 的端点 → 把 201 的 `/ontologies/generate` 声明为 `AsyncTask` 后 FAIL ✅
3. `unregisteredWebEndpoints` 不得有陈旧条目（端点已登记或已不再被调用）→ 注入假条目后 FAIL ✅

**能力边界（刻意写清，避免被当成它做不到的保证）**：它**不能**判定"内联对象类型与响应
schema 逐字段一致"——`{accepted: boolean}` 那两处正是这样漏过去的（本轮靠人工核对
handler 才发现）。根治方案是 `web/src/api/generated/types.ts` 按 openapi 生成类型
（该文件当前是**刻意留空的占位**），届时内联类型会被契约类型取代。

`unregisteredWebEndpoints` 登记了 5 个**确属已知**的缺口（前端已接入、服务端与契约均未实现）：
`/admin/connectors/{id}/test`、`/admin/subjects/{id}/disable`、`/policies/{id}/versions`（该路径
契约只有 GET）、`/releases/{id}/promote`、`/releases/{id}/rollback`。登记使缺口成为
**被记录的事实**而非被掩盖的空洞；补齐后条目会自动以"陈旧豁免"再报，强制清理。

### 12.4 对 §11.6 的状态更新（原文保留，不删除）

- **L3 主体（本体校验链）**：**已闭环**（§12.1）。`POST /ontologies/{id}/validate` 不再 503，
  执行真实 SHACL 校验并落库；「未定义 shapes」与「图中无实例」两种情形显式 422。
- **L4（装配接线门禁）**：**已落地**。`tests/unit/wiring_test.go` 从 `cmd/` 做 BFS 依赖闭包，
  `internal/ontology/{rdfs,semantic,cache}`、`internal/{agent,evidence,registry}`、
  `internal/execution/{inbox,lease}` 共 8 个包在 `danglingAllowlist` 显式登记；条目变可达即报"陈旧豁免"。
  该门禁同样经反例自证（临时建无调用方的包 → FAIL）。
- **L7（worker 接入 agent 执行驱动）**：状态不变，仍未完成。

### 12.5 本轮产生的可复现验证命令

```bash
export PATH="/c/Users/JokerZ/go-sdk/go/bin:$PATH" GOFLAGS=-mod=mod GOPROXY=off

# 全量门禁（含门禁 4/5 的新增第 ⑤ 条契约断言）
bash tools/gates/run-gates.sh                        # 通过 5 项，失败 0 项

# 单独跑契约门禁（含 ⑤）
go run ./tools/contract-lint -root .                 # 检查 10 项，失败 0 项

# 装配可达性 + 反假绿探针 + 端到端违规拦截
go test ./tests/unit/... ./internal/ontology/... -count=1

# 前端类型（内联契约类型对齐后必须过）
cd web && ./node_modules/.bin/tsc --noEmit
```

