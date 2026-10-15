# API 文档索引

## 1. 唯一机器可读真源

**`contracts/openapi.yaml`** —— OpenAPI 3.1。与本文档冲突时以它为准，并回修本文档
（`docs/contracts/03` §1.10）。

覆盖情况由 `tests/contract` 持续校验：

- 每个 operation 必须有唯一 `operationId`，且至少声明一个 2xx 响应。
- 受保护端点必须声明 4xx（至少 401）——否则前端与 SDK 无法生成错误分支。
- 同一 path 键不得重复：YAML 重复键会让前一份定义被静默覆盖。
- 每个 `$ref` 必须指向真实存在的 `components` 条目。

`internal/api/contract_test.go` 另做双向校验：路由声明表与 `openapi.yaml` 的 path 集合互相覆盖。

## 2. 端点组织

| 域 | 前缀 | 说明 |
| --- | --- | --- |
| 健康探针 | `/healthz`、`/readyz` | 公开；`readyz` 探测数据库，依赖不可用返回 503 |
| 认证 | `/api/v1/auth/*` | `register`、`login` 公开；`session`、`logout` 需要会话 |
| 执行 | `/api/v1/agent-runs*` | 唯一执行资源是 `agent_runs` |
| 管理 | `/api/v1/admin/*` | 用户、组织、角色绑定、本体分类、模型供应商、连接器 |
| 本体 | `/api/v1/ontologies*` | 本体包、验证、发布 |
| 运维 | `/api/v1/ops/*` | 告警、候选发布与质量门 |

## 3. 通用约定

- **认证**：`Authorization: Bearer v1.<tenant_uuid>.<session_uuid>.<subject_version>`，
  或会话 Cookie `odap_session`（EventSource 无法自定义头，故支持 Cookie 兜底）。
  开发环境可用 `X-Odap-Dev-*` 头（仅 `APP_ENV=development`，生产禁用）。
- **分页**：不透明 keyset 游标，请求 `limit`（1..200）+ `cursor`，响应返回 `next_cursor`。
- **幂等**：写操作支持 `Idempotency-Key` 头，并在响应头回写以便核对。
- **乐观并发**：`If-Match` 不匹配返回 409，`details.current_version` 给出当前版本。
- **错误响应**：`{ "error": { "code", "base_code", "message", "retryable" }, "request_id" }`。
  12 个基础码与域码映射见 `docs/contracts/02`。
- **异步任务与 SSE**：见 `contracts/03` §1.8 / §1.9。

## 4. 相关文档

- `docs/contracts/01-全局状态注册表.md` —— Run 17 状态（权威）
- `docs/contracts/02-统一错误码注册表.md`
- `docs/contracts/03-API命名与契约规范.md`
- `docs/contracts/04-模块追溯矩阵.md` —— 需求 → 模块 → 端点的追溯
- `docs/第十七篇…` §11.1 —— BFF 聚合端点
