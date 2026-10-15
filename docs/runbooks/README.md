# 运维手册索引

面向"已部署环境"的日常操作与故障处置。

## 1. 启停

```bash
docker compose up -d --build      # 启动（含一次性迁移 job）
docker compose ps                 # 查看状态
docker compose logs -f api        # 跟随 API 日志
docker compose down               # 停止（保留数据卷）
docker compose down -v            # 停止并删除数据卷（仅在需要重建时）
./stop.sh / ./start.sh / ./restart.sh   # 仓库根目录的便捷脚本
```

## 2. 迁移

```bash
make migrate            # 平台库（owner 账号 ODAP_MIGRATE_URL）
make migrate-mockerp    # Mock ERP 库（必须显式 --url，勿只靠 --dir 推断）
```

要点：

- 迁移为一次性 job（`restart: "no"`），退出码非 0 即启动失败，schema 未就绪不接受流量。
- 平台库使用 owner 账号做 DDL/GRANT，运行时连接一律使用 `odap_app`（非超级用户、`NOBYPASSRLS`）。
- `cmd/migrate` 会依据 `ODAP_DATABASE_URL` 的用户名与密码自动设置 `odap_app` 角色密码
  （迁移 000017），因此改运行时密码只需改这一行。
- 迁移记录表是 `system.schema_migrations`。

## 3. 健康检查与就绪

| 端点 | 语义 | 处置 |
| --- | --- | --- |
| `/healthz` | 进程存活，不查依赖 | 200 即进程正常 |
| `/readyz` | 探测数据库 | 200 就绪；503 表示依赖不可用或探针未配置，**此时不应接流量** |

`readyz` 的 503 是**如实反馈**而非故障掩盖：宁可拒绝流量，也不返回假成功。

## 4. 备份与恢复

```bash
docker compose exec -T postgres pg_dump -U odap -d odap -Fc > odap-$(date +%F).dump
docker compose exec -T postgres pg_restore -U odap -d odap --clean --if-exists < odap-YYYY-MM-DD.dump
```

注意：

- 平台库与 Mock ERP 库必须**分别备份**，两者账号与生命周期独立。
- 只追加表（`audit.audit_events`、`audit.evidence_records`）恢复后应核对行数与哈希链连续性。

## 5. 常见故障处置

| 现象 | 可能原因 | 处置 |
| --- | --- | --- |
| api 起不来，日志报 `password authentication failed for user "odap_app"` | 迁移 000017 未执行或 `ODAP_DATABASE_URL` 密码变更后未重跑迁移 | 重跑 `make migrate` |
| api 起不来，`lookup postgres: no such host` | 在容器外执行、或 `ODAP_DATABASE_URL` 指向错误主机 | 在 compose 网络内执行；检查 `.env` |
| `readyz` 持续 503 | 数据库未就绪或迁移失败 | `docker compose logs migrate`、`docker compose logs postgres` |
| 端点返回纯文本 404 | 路由前缀遮蔽 | 运行 `make gates`，路由可达性门禁会指出具体端点 |
| 查询结果为空但预期有数据 | 租户上下文未注入（RLS 过滤掉全部行） | 检查是否走 `pgxutil.TenantTx`；运行 `make gates` |
| 写入报 `append-only` | 试图 UPDATE/DELETE 审计或证据表 | 属预期保护，改为插入新记录 |

## 6. 发布前检查表

1. `make ci`（本地完整流水线）全绿；`bash tools/ci/run-all.sh --strict` 无 SKIP。
2. Quality Gate 通过：安全测试与故障注入均不得为"仅报告"。
3. 候选制品 sha256 已记录（`artifacts/ci/manifest.json`）。
4. 变更涉及契约时，`docs/contracts/*` 与 `contracts/openapi.yaml` 已同步。
5. 涉及迁移时，已在隔离环境验证"从零 up"与"重复 up"两条路径。
