# 本地开发指南

## 1. 环境准备

- Docker + Docker Compose（必需，工具链与依赖服务都在容器内）
- Go 1.23（可选；容器内已含，未安装时可全程用 `tools/dev-run.sh`）
- Node 20（可选；仅前端本地开发需要）

```bash
cp .env.example .env
docker compose up -d --build
docker compose run --rm api seed --dataset finance-demo
```

## 2. 改代码后如何验证

**Go 代码**（免重建镜像，在 compose 网络内运行）：

```bash
bash tools/dev-run.sh go build ./...
bash tools/dev-run.sh go test ./internal/api/...
```

**前端**（Vite 开发服务器，默认 5173）：

```bash
cd web && npm ci && npm run dev
```

**数据库结构变更**：新增 `migrations/platform/0000NN_*.sql`（编号连续），然后

```bash
make migrate
```

## 3. 测试

```bash
make test                 # 包内单元测试
make test-contract        # 契约文件规范性
make test-security        # 匿名拒绝与秘密泄露
make test-integration     # 需 PostgreSQL
make test-faultinjection  # 需 PostgreSQL
make test-e2e             # 需已启动 API
make ci                   # 全部阶段（本地流水线）
```

测试分层、前置条件与覆盖范围见根 `README.md` §4。

## 4. 交付门禁

```bash
make lint    # 契约一致性（文档 ↔ Go 常量 ↔ DDL ↔ 前端标签）
make gates   # 路由可达性 + 租户上下文 + SQL 绑定 + 契约一致性
```

`make gates` 的四条门禁各自对应一类**历史上真实发生过**的缺陷：

| 门禁 | 防的是什么 |
| --- | --- |
| 路由可达性 | 端点"已注册"却因前缀遮蔽恒定 404（声明表 ≠ 可达） |
| 租户上下文 | 第二份注入实现写入错误的 GUC，导致 RLS 静默失效 |
| SQL 绑定实参 | 占位符个数与绑定实参不符，运行时必然报错 |
| 契约一致性 | 状态词表/错误码/事件词表在文档与代码之间漂移 |

> 门禁设计纪律：判据"要么精确、要么不做"。词法上无法区分的模式只能作为
> ADVISORY 输出清单，不得升级为硬失败——否则会被加白名单绕过，退化成噪声。

## 5. 排错要点

- **`lookup postgres: no such host`**：在宿主机直接跑 Go 命令会解析不了 compose
  服务名。用 `tools/dev-run.sh`（容器网络内），或把连接串主机改为 `127.0.0.1`。
- **`.env` 里的连接串是给容器用的**：主机名是 compose 服务名，端口是容器内端口
  （`mockerp-db` 容器内为 5432，宿主机映射为 5433）。
- **`psql -c` 多语句是隐式单事务**：其中一条报错会回滚全部，排查时请分步执行。
- **Windows/Git Bash**：路径含盘符时用 `MSYS_NO_PATHCONV=1`；`curl -o /tmp/x` 会被
  路径转换破坏，改用 `-w` 内联输出或写到项目内相对路径。
