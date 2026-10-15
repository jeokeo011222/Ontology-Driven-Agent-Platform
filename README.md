# ODAP — 企业本体驱动智能体平台

ODAP（Ontology-Driven Agent Platform）是一个以**企业本体**为语义中枢、
以**可审计的执行链**为骨架的智能体平台：业务请求经本体语义解析为受控计划，
在 Harness 状态机中逐步执行，任何外部写操作都必须经过授权、审批、幂等与回执对账，
并留下可追溯的审计记录与证据包。

**权威来源**（发生冲突时以设计书为准）：

- 总纲：`docs/企业本体驱动智能体平台详细技术设计书.md`
- 工程蓝图（目录规划、Compose、CI、交付阶段）：`docs/第十六篇：ODAP MVP 总工程实施蓝图、代码仓库规划与端到端交付路线.md`
- 对外契约唯一机器可读真源：`contracts/openapi.yaml`
- 状态/错误码/事件词表：`docs/contracts/01` ~ `docs/contracts/03`

---

## 1. 快速开始

前置：Docker + Docker Compose。Go 1.23 与 Node 20 可选（容器内已含工具链）。

```bash
cp .env.example .env          # 开发用默认值可直接使用
docker compose up -d --build  # 起最小拓扑：postgres/migrate/mockerp-db/migrate-mockerp/mockerp/api/worker/web
docker compose run --rm api seed --dataset finance-demo   # 导入演示数据
```

验证：

| 端点 | 地址 | 说明 |
| --- | --- | --- |
| API 存活 | http://127.0.0.1:8080/healthz | 不依赖数据库 |
| API 就绪 | http://127.0.0.1:8080/readyz | 探测数据库；依赖不可用返回 503 |
| 前端工作台 | http://127.0.0.1:5173 | nginx 托管构建产物，`/api` 反代到 api |
| 平台库 | 127.0.0.1:5432 | 库 `odap`；owner `odap`，运行时 `odap_app` |
| Mock ERP 库 | 127.0.0.1:5433 | 库 `mockerp` |

可选依赖按 profile 启动：

```bash
docker compose --profile objectstore up -d     # MinIO（9000/9001）
docker compose --profile observability up -d   # 本地 OTLP collector（4317/4318）
```

> 生产部署不要设置 `ODAP_DEV_*`：后端不会装配 DevResolver，也不应注入身份头。

---

## 2. 目录结构

```
cmd/            api / worker / migrate / mock-erp —— 只做装配、配置读取、启动与优雅关闭
internal/       领域逻辑（15 个一级包）：api / identity / authorization / registry /
                ontology / harness / agent / execution / erp / approval / audit /
                evidence / llm / seed / platform
contracts/      对外契约：openapi.yaml、事件与 JSON Schema
migrations/     平台库迁移（mockerp/migrations 负责 Mock ERP 库，账号与生命周期独立）
deployments/    compose Dockerfile、nginx 模板、OTel 配置、运维脚本
datasets/       演示与评估数据集（finance-demo 为可运行样例）
ontology-packages/  本体包（Turtle 等）
web/            React + TypeScript 前端
tests/          跨模块测试：unit / contract / integration / e2e / security / fault-injection
tools/          contract-lint、gates（交付门禁）、ci（流水线脚本）、本地验证脚本
docs/           设计书、ADR、契约规范、运维手册
```

组织原则（第十六篇 §4.2）：

1. `cmd/` 只负责初始化依赖、读配置、启动服务、处理关闭信号。
2. `internal/` 负责领域逻辑；HTTP Handler 不实现业务规则。
3. `contracts/` 保存对外 API、事件与结构化输出契约。
4. `tests/` 保存跨模块测试；各模块仍可在自身目录内保留紧邻代码的单元测试。

包依赖方向见 `docs/architecture/README.md`，由 `tests/unit` 架构测试持续校验。

---

## 3. 常用命令

```bash
make help                 # 列出全部目标
make build                # go build ./...
make test                 # go test ./...（包内单元测试）
make test-contract        # 契约文件规范性测试
make test-security        # 匿名拒绝矩阵与秘密泄露
make test-integration     # -tags=integration（需 PostgreSQL，走 dev-run.sh 进容器网络）
make test-faultinjection  # -tags=faultinjection（需 PostgreSQL）
make test-e2e             # -tags=e2e（需已启动 API 与 ODAP_E2E_BASE_URL）
make test-graphstore      # 图存储契约自检（真实 SPARQL 引擎，L1 端点/L2 compose Fuseki/L3 RDFLib）
make graphstore-check     # 对指定端点跑 17 项断言（FUSEKI_ENDPOINT=http://127.0.0.1:3030/odap）
make lint                 # 契约一致性门禁
make gates                # 交付门禁：路由可达性 + 租户上下文 + SQL 绑定 + 契约一致性
make ci                   # 本地完整流水线（与 .github/workflows/ci.yml 同源）
```

需要连接数据库或 API 的命令统一使用 `tools/dev-run.sh`（它在 compose 网络内执行 Go 命令）：

```bash
bash tools/dev-run.sh go test ./internal/api/...
bash tools/dev-run.sh go run ./cmd/api seed --dataset finance-demo
```

---

## 4. 测试分层与门禁

对应第十六篇 §10.2 的六层，各自都有可运行的测试目录：

| 层 | 目录 / 命令 | 前置条件 | 覆盖内容 |
| --- | --- | --- | --- |
| 单元测试 | `internal/**/*_test.go`、`tests/unit` | Go | 规则、状态机、授权决策、查询编译、参数校验、幂等键；架构依赖方向 |
| 契约测试 | `tests/contract`、`make lint` | Go | OpenAPI 规范性（operationId 唯一、path 不重复、成功/失败响应声明、`$ref` 不悬空）、词表一致性 |
| 集成测试 | `tests/integration`、`-tags=integration` | PostgreSQL | 事务、RLS 跨租户隔离、Outbox/Inbox、操作账本、审批与回执 |
| 端到端测试 | `tests/e2e`、`-tags=e2e` | 运行中的 API | 从 API 走到业务终态：会话生命周期、探针诚实性 |
| 故障注入 | `tests/fault-injection`、`-tags=faultinjection` | PostgreSQL | 重复消息、失败入死信、去重短路 |
| 安全测试 | `tests/security` | Go / PostgreSQL | 匿名拒绝矩阵、越权与跨租户、秘密泄露 |

在上述六层之外另有一条**图存储契约自检**（`make test-graphstore`；CI 作业 10，脚本 `tools/ci/12-graphstore.sh`）：

| 层 | 目录 / 命令 | 前置条件 | 覆盖内容 |
| --- | --- | --- | --- |
| 图存储自检 | `cmd/api graphstore-check`、`make test-graphstore` | 真实 SPARQL 引擎 | 适配器生成的 SPARQL 在真实引擎上合法、租户命名图强隔离、空白节点可寻址、写入条数精确与幂等、跨后端行为一致 |

它必须跑在**真实引擎**上：`internal/ontology/graphstore` 的 HTTP 替身只能验证"适配器发了什么请求、怎么解析响应"，验证不了生成的查询是否合法（历史上适配器曾生成 4 词项的图模式，替身全绿而真实引擎语法报错）。详见 `docs/adr/ADR-005-图存储选型与GraphStore适配器边界.md` 与 `docs/图数据库的选型.md`。

**发布硬门禁**（第十六篇 §10.3）由 `make gates` 与 CI 的 Quality Gate 共同保证：
未授权外部写入、审批绕过、跨租户泄露、重试导致的重复非幂等操作、
把未知状态报告为成功、关键审计缺失、本体与规则回归，任一出现即阻断发布。

---

## 5. 持续集成

`.github/workflows/ci.yml` 按 §10.1 组织 13 个作业，**所有实际命令都在 `tools/ci/*.sh` 中**，
因此本地 `bash tools/ci/run-all.sh` 与 CI 的行为一致：

```
01 格式与静态分析 → 02 单元测试+前端类型检查 → 03 数据库迁移验证 → 04 契约测试
→ 05 集成测试 → 06 本体语义回归 → 07 Mock ERP 端到端
→ 08 安全测试 / 09 故障注入 / 10 图存储契约自检
→ 11 Quality Gate → 12 不可变候选制品 → 13 部署冒烟
```

作业编号是**执行顺序**；`tools/ci/*.sh` 的脚本前缀沿用第十六篇 §10.1 的级号
（`10-artifact` / `11-smoke` / `12-graphstore`），二者内容一一对应、数字不同。

本地运行（`--strict` 表示缺环境即失败，与 CI 的 `CI_STRICT=1` 等价）：

```bash
bash tools/ci/run-all.sh --no-smoke      # 跳过 compose 冒烟
bash tools/ci/run-all.sh --strict        # 发布前完整校验
bash tools/ci/run-all.sh --only 01,04    # 只跑指定阶段
```

安全测试、故障注入与图存储自检**不作为"仅报告"任务**：它们是 Quality Gate 的输入，失败即阻断。

---

## 6. 关键工程约定

1. **租户上下文只有一个注入实现**：`internal/platform/pgxutil.TenantTx`。
   它通过 `set_config('app.tenant_id', $1, true)` 注入——RLS 策略函数读的正是
   `app.tenant_id`。历史上曾出现第二份实现写入另一个 GUC（`app.current_tenant_id`），
   使策略函数求值得到 NULL、租户隔离**静默失效**，因此该约束由
   `tools/gates/tenant_context_gate.py` 静态守卫。
2. **依赖方向自外向内**：Handler → Service → Domain → Port → Adapter；
   `internal/platform` 不依赖任何业务域，非传输层不反向依赖 `internal/api`。
   由 `tests/unit` 架构测试守护。
3. **契约先改文档再改代码**：`contracts/openapi.yaml` 是唯一机器可读真源，
   与状态/错误码/事件词表的取值一致性由 `make lint` 校验。
4. **审计与证据只追加**：`audit.audit_events` 与 `audit.evidence_records`
   对应用角色撤销 UPDATE/DELETE，并由数据库触发器兜底（迁移 000007 / 000025）。
5. **失败不伪装成功**：能力未接入时返回明确错误（如 503），绝不返回空结果或 200。

---

## 7. 文档索引

| 文档 | 位置 |
| --- | --- |
| 架构总览与依赖规则 | `docs/architecture/README.md` |
| 架构决策记录（ADR） | `docs/adr/`，索引见 `docs/decisions/README.md` |
| API 契约与用法 | `docs/api/README.md` |
| 本地开发与测试 | `docs/development/README.md` |
| 运维手册（启停、迁移、故障处置） | `docs/runbooks/README.md` |
| 设计书与分册 | `docs/` 根目录各篇 |
| 实现符合性评测 | `docs/ODAP-设计书实现符合性评测清单.md` |
