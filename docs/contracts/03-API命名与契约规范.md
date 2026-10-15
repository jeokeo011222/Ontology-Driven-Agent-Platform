# contracts/03-API 命名与契约规范

> **本文件是 ODAP 对外 API 路径、分页、错误响应、幂等与实时通道的唯一真源。**
> 各分册出现的端点以本表为准；冲突处按 §4 裁决表收敛。版本：v1.0 · 2026-10-12

---

## 1. 通用规则

1. 前缀：所有对外 API 统一 `/api/v1`；JSON 为默认交换格式。
2. 资源命名：名词复数、小写 kebab-case（`/api/v1/agent-runs` 之类驼峰路径不允许）。
3. 动作用子资源 POST 表达，不用动词路径：`POST /api/v1/runs/{id}/cancel`、`POST /api/v1/runs/{id}/reconcile`、`POST /api/v1/approvals/{id}/decision`。
4. 分页：列表统一 cursor 式 `?limit=&cursor=`，响应 `{ "items": [...], "next_cursor": "..." }`；禁止 offset 深分页。
5. 过滤与排序：过滤用查询参数（`?status=&created_after=`），排序用 `?order=`（枚举值，不接受任意字段名）。
6. 幂等：所有写操作接受 `Idempotency-Key` 请求头（传本地记录主键的截断引用，不回显完整哈希，公式见第七篇 §8.1）。
7. 错误响应：统一结构见 contracts/02 §4（`code` + `base_code` + `retryable` + `details` + `request_id`），`message`/`details` 必须脱敏。
8. 长时操作：同步返回 202 + 任务引用，客户端通过 `GET /api/v1/tasks/{id}` 轮询（带退避）或 SSE 订阅；不得让客户端无限轮询运行资源。
9. 实时通道：统一 SSE `GET /api/v1/stream`（见第十七篇定型：`last-event-id` 重连续传、按租户过滤、背压时丢弃非关键事件并告警）；轮询仅作带退避的降级路径。
10. 契约源：`contracts/openapi.yaml` 是唯一机器可读契约，前端类型与客户端 SDK 由其生成；本文件与 openapi.yaml 冲突时以 openapi.yaml 为准并回修本文件。

## 2. 核心端点总表（权威）

| 端点 | 方法 | 功能 | 权威来源 |
|---|---|---|---|
| `/api/v1/runs` | POST/GET | 创建/查询 Run（唯一执行资源） | 总书 §3.4 |
| `/api/v1/runs/{id}` | GET | Run 状态与进度查询 | 第六篇 §15.2 |
| `/api/v1/runs/{id}/cancel` | POST | 请求取消（进入 `CANCELLING`） | 第六篇 §14.2.1 |
| `/api/v1/runs/{id}/reconcile` | POST | Run 级批量对账编排 | 第六篇 |
| `/api/v1/runs/{id}/lease/renew` | POST | Worker 租约续期（内部端点，需 mTLS） | 第六篇 §6.2 |
| `/api/v1/agents`、`/api/v1/agents/{id}/publish` | GET/POST | Agent 注册与发布 | 第四篇 |
| `/api/v1/ontologies`、`/api/v1/ontologies/{id}/validate`、`/api/v1/ontologies/{id}/publish` | GET/POST | 本体验证与发布 | 第五篇 |
| `/api/v1/plugins`、`/api/v1/plugins/{id}/enable` | GET/POST | 插件安装与租户启用 | 第四篇 |
| `/api/v1/tools`、`/api/v1/tools/{id}/invoke` | GET/POST | 工具发现与受控调用 | 第四篇 |
| `/api/v1/policies`、`/api/v1/policies/{id}/versions` | GET/POST | 策略版本管理 | 第三篇 |
| `/api/v1/approvals/{id}/decision` | POST | 审批决策 | 总书 §3.4 |
| `/api/v1/erp/capabilities` | GET | ERP 能力发现 | 总书 §3.4 |
| `/api/v1/erp/operations/{id}/reconcile` | POST | 单笔操作对账（Run 级=编排，Operation 级=单笔，底层同一 Reconciler） | 第七篇 |
| `/api/v1/audit-events` | GET | 审计查询 | 第十一篇 |
| `/api/v1/evaluations`、`/api/v1/evaluations/{id}` | GET/POST | 评估任务（含仿真与回归） | 第十五篇 |
| `/api/v1/plan-templates` | GET/POST | 计划模板资产（工作流模板，非执行资源） | 第十四篇收敛 |
| `/api/v1/stream` | GET（SSE） | 统一实时通道 | 第十七篇 |
| `/api/v1/tasks/{id}` | GET | 异步任务状态 | 本规范 §1.8 |

## 3. 内部端点（不进 OpenAPI 公网面）

`/internal/runs/{id}/lease/renew`、`/internal/workers/heartbeat`、`/internal/recovery/scan` 等仅服务间 mTLS 可达，命名空间 `/internal`，禁止对外暴露。

## 4. 历史命名冲突裁决表

| 冲突 | 出现处 | 裁决 |
|---|---|---|
| `/api/v1/runs` vs `/api/v1/agent-runs` | 第十篇 vs 第八篇 | **`/api/v1/runs`**；第八篇示例改为引用 |
| `/api/v1/runs` vs `/api/v1/workflows` | 第十篇 vs 第十四篇 | **执行一律 `/api/v1/runs`**；`workflows` 降级为 `/api/v1/plan-templates`（模板资产），第十四篇执行示例改写 |
| `/api/v1/evaluations` vs `/api/v1/evaluation/runs` | 第十七篇 vs 第十五篇 | **`/api/v1/evaluations`**；第十五篇示例改为引用 |
| `/api/v1/workspace/summary`、`/api/v1/apps` | 第十七篇 §11.1（无后端设计） | 保留为前端聚合端点，但必须在第十七篇补注"由 BFF 聚合层实现，契约登记进 openapi.yaml" |
| Run 级 vs Operation 级 reconcile | 第六篇 vs 第七篇 | 两者并存但职责分明：Run 级=批量编排入口，Operation 级=单笔入口，底层同一 Reconciler；文档互相引用 |

## 5. 版本演进

1. 仅允许破坏性变更升 `/api/v2`；`/api/v1` 内只做加法（新字段、新枚举值、新端点）。
2. 新增字段必须可被旧客户端忽略；删除字段必须先标记 deprecated 并至少保留一个版本周期。
3. OpenAPI 变更必须走契约评审（与 contracts/01、02 同一次评审），CI 校验"文档端点 ⊆ openapi 端点"。
