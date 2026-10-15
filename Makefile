# ODAP Monorepo Makefile（第十六篇 §8.4）
# 容器内 Go 工具链：GO_IMAGE 锁定版本，go/build/test 均通过容器执行，
# 保证开发者本机无需安装 Go 也能获得一致的构建结果。

GO_IMAGE := golang:1.23-bookworm
DOCKER_RUN_GO := docker run --rm -v "$(shell pwd -W):/src" -v odap-gocache:/go/pkg/mod -v odap-gobuild:/root/.cache/go-build -w /src -e GOPROXY=https://goproxy.cn,direct -e GOFLAGS=-mod=mod $(GO_IMAGE)

.PHONY: help dev-up dev-down migrate migrate-mockerp seed test test-contract test-security test-integration test-e2e test-faultinjection test-graphstore graphstore-check lint gates ci build tidy

help:
	@echo "dev-up / dev-down    启动/停止开发环境（compose，最小路径：postgres/migrate/mockerp-db/migrate-mockerp/mockerp/api/worker/web）"
	@echo "                     需要对象存储或本地可观测性时：docker compose --profile objectstore --profile observability up -d"
	@echo "migrate              平台库迁移（cmd/migrate up，owner 账号 ODAP_MIGRATE_URL）"
	@echo "migrate-mockerp      Mock ERP 库迁移（cmd/migrate up --dir mockerp/migrations --url \$MOCKERP_DATABASE_URL）"
	@echo "seed                 导入演示数据（go run ./cmd/api seed --dataset finance-demo）"
	@echo "build                go build ./..."
	@echo "test                 go test ./...（包内单元测试）"
	@echo "test-contract        go test ./tests/contract/...（OpenAPI/JSON Schema 契约规范性）"
	@echo "test-security        go test ./tests/security/...（匿名拒绝矩阵与秘密泄露）"
	@echo "test-integration     go test -tags=integration ./tests/integration/...（需 PostgreSQL）"
	@echo "test-e2e             go test -tags=e2e ./tests/e2e/...（需已启动 API，ODAP_E2E_BASE_URL）"
	@echo "test-faultinjection  go test -tags=faultinjection ./tests/fault-injection/...（需 PostgreSQL）"
	@echo "test-graphstore      图存储契约自检（tools/ci/12-graphstore.sh，L1 已配置端点/L2 compose Fuseki/L3 RDFLib）"
	@echo "graphstore-check     对指定端点跑 api graphstore-check（需 FUSEKI_ENDPOINT=...）"
	@echo "lint                 契约一致性门禁（tools/contract-lint）"
	@echo "gates                全量交付门禁（路由可达性 + 租户上下文 + SQL 绑定 + 契约一致性）"
	@echo "ci                   本地完整流水线（tools/ci/run-all.sh，与 .github/workflows/ci.yml 同源）"
	@echo "tidy                 go mod tidy"

dev-up:
	docker compose up -d --build

dev-down:
	docker compose down

# 平台库迁移：cmd/migrate 读 ODAP_MIGRATE_URL（owner 账号）；
# 但 config.Load 无条件要求 ODAP_DATABASE_URL，故调用前需两个变量都在环境中。
migrate:
	$(DOCKER_RUN_GO) go run ./cmd/migrate up

# Mock ERP 库迁移：必须显式传 --url 指向 $MOCKERP_DATABASE_URL，不能只靠 --dir 推断方向，
# 否则 cmd/migrate 会回退到 ODAP_MIGRATE_URL/ODAP_DATABASE_URL，把 mockerp 迁移打到平台库。
migrate-mockerp:
	$(DOCKER_RUN_GO) go run ./cmd/migrate up --dir mockerp/migrations --url $$MOCKERP_DATABASE_URL

seed:
	$(DOCKER_RUN_GO) go run ./cmd/api seed --dataset finance-demo

build:
	$(DOCKER_RUN_GO) go build ./...

test:
	$(DOCKER_RUN_GO) go test ./...

# 契约与安全测试无需数据库，直接用容器工具链即可（与 test 同构）。
test-contract:
	$(DOCKER_RUN_GO) go test ./tests/contract/... -count=1

test-security:
	$(DOCKER_RUN_GO) go test ./tests/security/... -count=1

# 集成/故障注入/E2E 需要连接 compose 内的数据库或 api 服务，
# 故必须走 dev-run.sh（它带 --network odap_default 并注入 .env 的连接串）；
# DOCKER_RUN_GO 不带网络，用它跑这些目标会得到 "lookup postgres: no such host"。
test-integration:
	bash tools/dev-run.sh go test -tags=integration ./tests/integration/... -count=1

test-faultinjection:
	bash tools/dev-run.sh go test -tags=faultinjection ./tests/fault-injection/... -count=1

test-e2e:
	bash tools/dev-run.sh go test -tags=e2e ./tests/e2e/... -count=1

# 图存储契约自检（第十六篇 §10.1 第 12 阶段、docs/图数据库的选型.md、ADR-005）：
# 与单元测试里的 HTTP 替身不同，这里必须对**真实 SPARQL 引擎**执行断言——
# 替身只能验证"适配器发了什么、怎么解析响应"，验证不了生成的 SPARQL 是否合法。
# 脚本按 L1（已配置端点）→ L2（compose 临时 Fuseki）→ L3（RDFLib 测试端点）逐级降级；
# 三级都不可用时 SKIP（CI 中 CI_STRICT=1 视为失败）。
# 指定 L3 解释器：GRAPHSTORE_CHECK_PY=/path/to/python（需可 import rdflib）。
test-graphstore:
	bash tools/ci/12-graphstore.sh

# 对某个已就绪的 SPARQL 端点直接跑 17 项断言（不自动拉起引擎）。
# 用法：make graphstore-check FUSEKI_ENDPOINT=http://127.0.0.1:3030/odap
graphstore-check:
	@if [ -z "$$FUSEKI_ENDPOINT" ]; then echo "用法：make graphstore-check FUSEKI_ENDPOINT=http://127.0.0.1:3030/odap" >&2; exit 2; fi
	bash tools/ci/graphstore-check.sh "$$FUSEKI_ENDPOINT"

tidy:
	$(DOCKER_RUN_GO) go mod tidy

# 契约一致性门禁（contracts/01 §6、contracts/02 §4）：
# 断言 状态词表/错误码/事件词表 在 契约文档、Go 常量、DDL、前端标签 之间逐项一致。
lint:
	$(DOCKER_RUN_GO) go run ./tools/contract-lint -root .

# 全量交付门禁：在契约一致性之外，补上"代码能不能真的跑通"的三条：
#   路由可达性（真实请求逐条探测，防前缀遮蔽）
#   租户上下文注入（静态，防 SET LOCAL app.current_tenant_id 类缺陷）
#   SQL 绑定实参个数（静态，防占位符/实参不匹配）
# 本机已装 Go 时可直接跑（更快）；未装则在容器内跑。
gates:
	bash tools/gates/run-gates.sh

# 本地完整流水线（第十六篇 §10.1）：与 .github/workflows/ci.yml 的 job 一一对应，
# 保证"本地跑得通 == CI 跑得通"。--no-smoke 可跳过 compose 冒烟。
ci:
	bash tools/ci/run-all.sh --no-smoke
