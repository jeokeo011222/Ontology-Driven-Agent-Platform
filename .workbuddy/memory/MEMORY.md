# ODAP 项目长期约定（跨会话复用）

> 只放**跨会话必须遵守**的约定与易踩的坑；过程记录见同目录 `YYYY-MM-DD.md`。

## 环境与工具

- **仓库不是 git 仓库**（无 `.git`）。改动无法用 diff 追溯；跨会话只看文档与记忆。
- Windows + Docker Desktop + **Git Bash**。所有 docker 命令前置 `MSYS_NO_PATHCONV=1`
  （否则以 `/` 开头的参数被当 Windows 路径转换）。
- 连接数据库/服务时用 `bash tools/dev-run.sh <go 命令>` 在容器内跑（`--network odap_default`）。
  `tools/dev-run.sh` 内**硬编码**了 `/d/Code/Ontology-Agent`，移动仓库需改它。
- 纯 Go 的构建/测试**本机也有 Go**：`/c/Users/JokerZ/go-sdk/go/bin/go.exe`（不在 PATH，
  需 `export PATH="/c/Users/JokerZ/go-sdk/go/bin:$PATH"` + `GOFLAGS=-mod=mod GOPROXY=off`）。
  经 `$CI_GO` 的 CI 脚本（`tools/ci/*.sh`）会自动找到它。**本机无 `make`、无 `psql`**。
- 浏览器验收：`agent-browser` 输出**绝不接管道**（会 SIGTERM 误判）；开头幂等 `close`；
  冷启动首次 `open` 可能在文档 commit 前返回 → 必须"**重开 → 轮询**"兜底；
  **页面状态（如对话框）不跨 Bash 调用存活**，一次交互流程要写在同一个调用里。
- 回环请求前 `unset http_proxy https_proxy` + `export no_proxy=127.0.0.1,localhost`。
- ⚠️ **本仓库可能被多个进程/会话并发修改**（实测：`000024_seed_console_permissions.sql`、
  `web/src/**`、`.env.example`、`datasets/**` 曾被本会话之外的进程改动）。
  因此：① **新增迁移前先 `ls migrations/platform/` 确认编号未被占用**；
  ② 镜像重建时间可能不是本会话触发，**不要用"镜像构建时间"推断自己的改动是否已生效**；
  ③ 活体证据要选**不依赖镜像时序**的（真实 PG 探针 / 端到端断言 / DB 侧行核对）。
- Windows 版 `curl` 写 `/tmp/x.json` 会因路径转换失败 → 用 `-w` 内联输出，**不落临时文件**。

## 启动 / 停止 / 重启（根目录三个脚本，唯一入口）

- `bash start.sh` 启动并**轮询真实探针**等待就绪；`--build` 重建镜像；`--seed` / `--reseed`
  导入演示数据；`--profile objectstore|observability`；`--init-env` 生成 `.env`。
- `bash stop.sh` 停止并**保留数据卷**；`--purge` 必须同时 `--yes`（只给 `--purge` 一律拒绝）。
- `bash restart.sh` = stop（保留数据）+ start，透传选项；**明确拒绝 `--purge`**。
- **`docker compose up -d` 不会重建镜像**（已实测：Created 不变）。改完代码必须
  `--build`，否则跑的是旧镜像。
- 就绪探针：`GET /healthz`（仅证明进程活着）与 `GET /readyz`（**含数据库检查**，用它）。
- compose 项目名 `odap`；端口：api `8080`、web `5173`、postgres `5432`、
  mockerp-db 宿主 `5443`（`.env` 的 `MOCKERP_DB_HOST_PORT` 覆盖，本机 5433 被占用）。
- 演示身份在 `.env`：`ODAP_DEV_TENANT` / `ODAP_DEV_SUBJECT`（空 → nginx 不注入
  `X-Odap-Dev-*` → 浏览器 401，**与生产行为一致，不是故障**）。

## 部署产物的真源（易错）

- **宿主 `web/dist` 不是部署输入**：`Dockerfile.web` 在镜像内 `npm run build`，
  且 `.dockerignore` **排除 `web/dist`**。任何"拿本地 dist 判部署对不对"的断言都错。
  正确不变量：**服务端字节 == web 镜像内字节**。
- `api` 镜像**不含 `datasets/`**（只 COPY 二进制）。要跑 `seed` 必须挂仓库：
  `docker compose run --rm --no-deps -v "$(pwd -W):/src" -w /src api seed --dataset finance-demo`。

## 门禁（改动后至少要跑这些）

- **`make gates`**（= `bash tools/gates/run-gates.sh`）—— 全量交付门禁总入口，含下列 5 条：
  - **路由可达性**（`internal/api/router_reachability_test.go`）：遍历 `Routes(h)` 逐条发**真实请求**，
    断言响应是 JSON（或 204）而非 Go 默认纯文本 404/405。公开路由默认是 `text/plain 404`，天然可判别。
  - **租户上下文**（`tools/gates/tenant_context_gate.py`）：禁 4 类写法（见"数据与口径"）。
  - **SQL 绑定**（`tools/gates/sql_argument_gate.py`）：强制"绑定实参个数 == 不同占位符个数"；
    另对"同一占位符跨两列"输出 **ADVISORY 人工清单**（不参与退出码）。
  - **契约一致性**（`tools/contract-lint`）。
  - **装配可达性**（`tests/unit/wiring_test.go`）：从 `cmd/` 做 BFS 依赖闭包，防"包已实现+测试全绿
    但生产 import 数为 0"。例外须在 `danglingAllowlist` 登记理由；条目变可达即报"陈旧豁免"。
- `tools/contract-lint` —— 当前 **10/10**。管三类：① 状态词表/错误码/事件词表/前端标签；
  ② **前端写方法 ↔ openapi**：`AsyncTask` 只能配声明了 202 的端点，且前端调用的写端点必须
  在 openapi 登记（已知缺口登记在 `unregisteredWebEndpoints`）。
  基础错误码固定 12 个；要表达新语义优先加**域码**（基础码不变），否则契约门禁会红。
  ⚠️ 它**判不了**"内联对象类型与响应 schema 逐字段一致"（如 `{accepted: boolean}`）——
  根治靠按 openapi 生成 `web/src/api/generated/types.ts`（当前是刻意留空的占位）。
- `tools/http-entry-cache-verify.sh` —— 入口 `no-store` 等 8 项。
- `tools/ui-catalog-verify.sh` —— 6 个目录页真实数据渲染，36 项。
  ⚠️ **它的页面清单是手工枚举的**（ontologies/agents/tools/identity/models/workflows），
  而设计书要求 27 条路由 → `UsersPage`/`OrgsPage`/`OntologyCategoriesPage` **从未被扫**。
- `gofmt -l internal cmd mockerp tools` 必须为空 + `go build ./...` / `go vet` / `go test`。

### 门禁的四条铁律（本项目已多次踩坑）

1. **断言对象必须 == 被判对象**。`TestOpenAPICoversAllRoutes` / `TestAllContractEndpointsRegistered`
   只断言 `routes()` **声明表**，从不发 HTTP → 声明了但不可达的端点（如 `auth/logout`）全绿。
2. **"含某字符串" ≠ "可用"**。看到 SQL 里有 `app.current_tenant_id` 就以为租户上下文 OK，
   既没核对 GUC 名，也没验证 SQL 能否被解析。
3. **单测全绿 ≠ 系统可用**。必须先做**接线分析**：从 `cmd/` 出发看该包是否真的被 import。
   判 🟡 而非 ✅。
4. **覆盖清单不得手工枚举**。UI 门禁的页面清单应由 `web/src/app/router.tsx` ∩
   `contracts/openapi.yaml` 生成。
5. **发现一处缺陷，就扫全仓同类**。同一缺陷类几乎从不只出现一次：`AsyncTask` 错用在
   4 个端点上、前端调用了 5 个契约里不存在的端点。逐个手改必然漏，要写门禁。
6. **返 200/通过是假绿的高发区**。SHACL 在空图返回 `conforms=true`；未知 severity
   若映射成"不阻断"门禁就被绕过；未执行的检查写成 `true` 是假绿、写成 `false` 是凭空造失败
   → **未执行一律写 `NULL`**。反假绿必须给**正向证据**（"图里确实有 N 个待校验实例"），
   而不是"没找到反例"。同理：`AsyncTask` ⇔ 202；未知 severity → 阻断（fail-closed）。

### 写静态门禁的两条实现纪律（否则必退化）

- **判据"要么精确、要么不做"**。SQL "同一占位符跨两列"在**词法上与合法同值复用完全同形**：
  缺陷 `WHERE tenant_id = $1 AND id = $1` vs 合法 `m.tenant_id = $1 ... t.id = $1`。
  无数据流分析必然误报 → 只能做 ADVISORY；强制项留"绑定实参个数 == 占位符个数"。
- **两个必踩的误报坑**：① 说明性注释里引用反面教材会触发静态门禁 → **先剥离注释**
  （用等长空格替换以保持行号映射）；② 多行调用 + **尾随逗号**会产生纯空白实参段 →
  切分后**必须剔除空白段**（曾致 8 处命中中 7 处误报）。
- 触发器类测试收尾**必须核对 `pg_trigger.tgenabled` 回到 `O`**（别留下禁用状态）。
- `psql -c` 多语句是**隐式单事务**：一条报错回滚全部 → 清理类操作**分步执行**。

## 图存储（RDF，ADR-005）

- **唯一访问路径**：`graphstore.Open` 是装配入口，`graphstore.GraphStore` 是调用契约。
  **禁止**业务模块直接拼 SPARQL 或直连 Fuseki（由 `tests/unit/arch_test.go` 静态门禁强制，
  而非文档约定）。SPARQL/Fuseki 标识**只能**出现在 `internal/ontology/graphstore/` 内。
- 后端由 `GRAPHSTORE_MODE` 选（空/`postgres` = 默认 PG 引擎；`fuseki` = Fuseki + TDB2）。
  **非法值必须报错，不得静默回退**（否则"切到 Fuseki"与"其实还在 PG"无法区分）。
- `GraphStore` 接口**不得**出现 `pgx.Tx`/SQL/SPARQL 细节；PG 专有的词项资源字典是**可选接口**
  `TermDictionary`（类型断言探测）。能力边界用 `Capabilities{ResourceDictionary,PersistentGraphID,FactMetadata}`
  显式声明——PG 全 true，Fuseki 全 false 且不实现 `TermDictionary`。"少存一维"必须装配期可判定。
- 租户隔离靠**物理命名图** `urn:odap:t:<tenant>:g:<iri>`；读到非本租户命名空间**必须报错**，
  不得静默丢弃（否则"隔离生效"与"绕过但没命中"混为一谈）。
- **必须对真实 SPARQL 引擎验证**：`internal/ontology/graphstore` 的 HTTP 替身只能证明"发了什么/怎么解析"，
  **证明不了 SPARQL 合法**（已实测：4 词项图模式替身全绿、真实引擎语法报错；依赖引擎回显绑定常量也只在真实引擎暴露）。
  入口 `bash tools/ci/12-graphstore.sh`（L1 已配置端点 → L2 compose Fuseki → L3 RDFLib，L3 带子进程存活检查）。
- **ADR 编号**：`docs/adr/` 是四位无前导零的跨分册空间；**ADR-004 已被第十七篇预定**给"前端数据层选型
  （待评审）"，图存储是 **ADR-005**。勿混淆 `docs/adr/ADR-005` 与 `docs/decisions/ADR-0005`。
- 本机实测：**无 `make`**、**无 `psql`**；Go 在 `/c/Users/JokerZ/go-sdk/go/bin/go.exe`；
  带 rdflib 7.6 的 python 在 `C:/Users/JokerZ/.workbuddy/binaries/python/envs/default/Scripts/python.exe`
  （`GRAPHSTORE_CHECK_PY` 指它即可跑 L3）；Docker 拉 `stain/jena-fuseki` 会因本机代理截断镜像层失败。

## 数据与口径

- `llm.*` 是**平台级表**（无 `tenant_id`、无 RLS）→ 不走 `TenantTx`，用 `pgx.BeginFunc`。
- **租户上下文只有一条正确路径**：`s.TenantTx(ctx, fn)` → `middleware.TenantTx` →
  `pgxutil.WithTenantTx` → `set_config('app.tenant_id', $1, true)`。
  RLS 函数 `app.current_tenant_id()` 读的就是 **`app.tenant_id`**。
- **显式租户 ID 的入口是 `s.TenantTxAs(ctx, tenantID, fn)`**（登录/注册/开发态直连等
  context 里没有租户的场景）。它同样只走 `pgxutil.WithTenantTx`，
  所以"写 `app.tenant_id` 的代码全仓唯一"。**禁止**再写本地 `setTenant()` 辅助函数。
- 已修复的历史缺陷（`store.setTenant()` 写 `SET LOCAL app.current_tenant_id = $1`）：
  (a) PostgreSQL 的 `SET` **不接受参数占位符**（`syntax error at or near "$1"`）；
  (b) 即使成功，`app.current_tenant_id` 与 `app.tenant_id` 是**两个不同 GUC**。
  实测：写 `app.current_tenant_id` 时 `app.current_tenant_id()` 返回 NULL（租户隔离静默失效）。
  真实爆炸半径是 **22 个方法**（不止当时观测到的 3 个端点）；
  其中 9 个还叠加了"同一占位符跨两列"缺陷（`WHERE tenant_id = $1 AND id = $1` → 恒命中 0 行 → 恒 404）。
- **身份模型有两个不可混用的字段**（`tenantctx.Identity`）：
  `SessionID`（= `iam.sessions.id`，仅由 `identity.SessionService.Validate` 产出，注销唯一依据）
  与 `RequestID`（HTTP 请求关联 ID，`middleware.RequestID` 产出，**不得**当业务标识）。
  早期把会话 ID 塞进 `RequestID`，被 `SessionResolver` 用请求 ID 覆盖 → 注销永远撤不到会话。
- **公开路由在 `RouteSpec.Public` 上声明**（路由单一真源）。`NewRouter` 对 Public 路由
  按**完整 (方法, 路径)** 逐条挂载。**禁止**改回 `/api/v1/auth/` 这类前缀挂载
  —— 前缀会吞掉同子树下需要认证的兄弟端点（`auth/logout`、`auth/session` 曾因此恒定纯文本 404）。
- **`internal/{agent,registry,evidence,ontology}` 生产 import 为 0**（孤岛库）；
  `execution/{inbox,lease}`、`ontology/{rdfs,shacl,semantic,cache}` 亦然。
- `provider_id` 规范形状 `^[a-z0-9]+(-[a-z0-9]+)*$`（2–63）；派生只在**无损映射**时成立，
  中文显示名必须显式提供（`DeriveProviderID` 返回 ok=false，fail-closed）。
- 装配 SQL 用 `LEFT JOIN`：新建的"有端点、无模型版本"端点必须**带原因**出现在诊断里。
- **本体校验链为何仍 503**（不要误判为"漏接线"）：`ontology.ontology_artifacts`
  只有 `content_digest` / `storage_uri`，**没有 `content` 列**，且仓内**没有 Turtle 解析器**。
  要真正驱动 `internal/ontology/{rdfs,shacl}` 必须先补"对象存储 / 制品加载器"。
- **本体发布门禁**：只允许 `VALID` / `APPROVED` 进入 `PUBLISHED`（第五篇 §11.1）。
  `DRAFT` 曾可直接发布 → 已收敛；失败返回 **409 CONFLICT**。
- `ontology.validation_runs` 的 `validation_profile` / `summary_json` / `created_at` 与
  `ontology.validation_findings` 表由**迁移 000023** 补齐（原 000004 漏建，设计书 §5.3 有）。
- `audit.evidence_records` 的 append-only 是**双保险**：000011 的
  `REVOKE UPDATE, DELETE ... FROM odap_app`（权限层）+ 000025 的
  `trg_evidence_records_append_only`（触发器层，连 owner 也拦）。

## 文档工具链

- `tools/md2html.py <src.md> <dst.html>` 只支持 Markdown 子集，且实现是**朴素 `split("|")`**：
  表格单元内不能出现裸 `|`（`` `GET|POST` `` 要写成 `GET/POST`），否则撑出多余列。
- 行内粗体正则 `\*\*([^*]+)\*\*` 遇内容含 `*`（如 `system.*`）会**失配**。
  交付前用脚本核对"每张表列数一致 + HTML 无残留 `**`"。
