# 架构文档索引

本目录收纳跨模块的架构总览与约束。分册设计书位于 `docs/` 根目录，按能力域划分。

## 1. 分层与依赖方向

第十六篇 §4.3 规定的依赖方向：

```
HTTP Handler (internal/api)
        ↓
Application Service
        ↓
Domain Logic / Use Case
        ↓
Interface (Port)  ←──────────┐
        ↑                    │
Infrastructure Adapter ──────┘
```

明文约束（由 `tests/unit` 的架构测试持续校验，违反即 CI 失败）：

| 约束 | 理由 |
| --- | --- |
| `internal/platform` 不依赖任何业务域 | 平台层位于最内层，只有被依赖 |
| 非传输层不反向依赖 `internal/api` | 业务规则不得被传输细节绑死 |
| `internal/ontology` 不依赖 `internal/erp` | 语义层不得直接发起 ERP 写操作 |
| `internal/authorization` 不依赖 `internal/erp` | 授权层不得绑定具体 ERP Adapter |
| `internal/agent` 不依赖 `internal/erp/adapters` | 不得绕过 Operation Gateway 直连 Adapter |

新增包依赖时请先对照该表；若确有必要打破，应先落一份 ADR（见 `docs/adr/`）。

## 2. 进程与部署拓扑

| 进程 | 职责 | 关键约束 |
| --- | --- | --- |
| `cmd/api` | HTTP 接口、认证链、SSE | 迁移未完成不接受流量 |
| `cmd/worker` | Outbox 消费、Run 推进、租约续期 | 同一 Run 的推进经租约串行化 |
| `cmd/migrate` | 平台库与 Mock ERP 库迁移 | 一次性 job，失败即启动失败 |
| `cmd/mock-erp` | 独立 Mock ERP 服务与数据库 | 与平台库账号、生命周期完全隔离 |

## 3. 数据层关键设计

- **Schema 划分**：见第十六篇 §5.1 与 `migrations/platform/`。
  注意该节表格中的 `approval` / `erp` / `evidence` 等是**逻辑范围**而非物理 schema 名，
  实际落地为 `workflow.approvals*` / `integration.erp_*` / `audit.evidence_records`。
- **RLS 与租户上下文**：所有租户域表启用 `FORCE ROW LEVEL SECURITY`，
  策略函数读 `app.tenant_id`；应用角色 `odap_app` 为 `NOBYPASSRLS`。
  唯一注入实现是 `internal/platform/pgxutil.TenantTx`。
- **只追加表**：`audit.audit_events`、`audit.evidence_records`。
- **分区表**：`execution.consumer_inbox` 按 `received_at` 分区（迁移 000014 修正）。

## 4. 相关文档

- `docs/企业本体驱动智能体平台详细技术设计书.md` —— 总纲
- `docs/第一` ~ `docs/第十七篇` —— 各能力域分册
- `docs/adr/` —— 架构决策记录
- `docs/contracts/` —— 状态注册表、错误码注册表、API 命名规范、模块追溯矩阵
