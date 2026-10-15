# 实施方案：模型供应商选择 / 与ERP集成字典化 / 工具实做 / 统一分页筛选 / 静默降级核查

## Context（背景与目标）

用户提出 5 项需求：
1. AI 自动生成弹窗的「指定模型」改为**从模型供应商下拉选择**（复用「模型与推理」页配置），并修复「生成失败：llm: 读取供应商响应失败」。
2. 管理控制台菜单「ERP与外部集成」改名「与ERP集成」并移到「数据接入与映射」之前；ERP 做**字典**（用友NC65/金蝶星空云/SAP，默认 NC65），并实现**完整连接器配置流程**（用户已确认）。
3. 工具管理**全部按实际业务执行**（严禁 mock）：外部写类工具实现**带审批确认的执行**（用户已确认）。
4. 管理控制台各列表统一 **10 行/页 + 筛选、查询**。
5. 全模块**静默降级核查**，以实战为基础。

关键探查结论：
- 报错根因：[httpprovider.go](d:/Code/Ontology-Agent/internal/llm/httpprovider.go) `http.Client{Timeout: 60s}` 在本体生成（8192 tokens 输出，耗时远超 60s）时于读响应体阶段超时，`io.ReadAll` 失败被包装为「llm: 读取供应商响应失败」。
- 后端 `POST /ontologies/generate` 已支持 `model_id` 参数（[generation.go](d:/Code/Ontology-Agent/internal/api/handlers/generation.go)），网关按 `Routing[model_id]→provider` 路由。
- 前端 4 个生成弹窗的 model_id 均为自由文本：OntologyListPage.tsx:371、AgentsPage.tsx:475、ToolsPage.tsx:524、WorkflowsPage.tsx:587；已有 `useModelProviders()`（web/src/features/llm-providers/api.ts）封装 `/admin/model-providers`。
- `InvokeTool`（handlers/tools.go:81-127）恒 503；`handlers.Deps` 已有 `ERP erp.Port`（compose 注入 `MOCK_ERP_BASE_URL` → mockerp 容器为真实 HTTP ERP 模拟器）；`OperationGateway.Ports` 是 `PortResolver` 接口，可插动态解析。
- 内置工具数据在 datasets/finance-demo/tools.json（16 个工具，含 execution_config.adapter：mock-erp/nc65/docingest/shacl/graphload/llm-gateway/retriever/ocr/budget/ledger/audit-chain/evidence）；`LatestToolVersion`（store/tool_version.go）未查 execution_config，需扩展。
- 凭据加密可复用 `internal/platform/secretbox`（AES-256-GCM 信封，AAD 前缀绑定）。
- `GET /runs/{id}/erp-operations` 路由/openapi 均缺失（任务详情页 404 横幅的根因）；`/admin/organizations` 无分页（违反硬约束）。

---

## 需求 1：模型供应商下拉 + 生成超时修复

### 后端
- [internal/llm/httpprovider.go]：
  - `DefaultHTTPTimeout` 60s → 300s，支持环境变量 `ODAP_LLM_HTTP_TIMEOUT` 覆盖（Duration 解析，非法值直接报错退出装配，fail-closed）。
  - `Complete` 中读响应体失败时用 `errors.Is(err, context.DeadlineExceeded)` 识别超时，返回明确的「模型供应商响应超时（生成大输出时可能超出时间上限），可配置 ODAP_LLM_HTTP_TIMEOUT」消息，而非笼统的「读取供应商响应失败」。
- openapi.yaml 无需变更（无新端点）。

### 前端
- 新建 `web/src/hooks/useModelOptions.ts`：基于 `useModelProviders()`，将 endpoints×model_ids 展开为 Select 选项：`value=model_id`（网关路由键），`label=「供应商显示名 / 端点名 / model_id」`；按 model_id 去重；无可用端点时返回空数组并暴露 `emptyReason`（弹窗内显示明确提示「未配置可用模型端点，请先在模型与推理页登记」，fail-closed，不禁用提交外的其他功能——但 model_id 为空时由后端按能力路由，属合法路径，保持允许）。
- 新建共享小组件 `web/src/components/model-select.tsx`（Select + 空态提示），替换 4 处自由文本 Input：
  - `web/src/features/ontology/pages/OntologyListPage.tsx:371`
  - `web/src/features/agents/pages/AgentsPage.tsx:475`
  - `web/src/features/tools/pages/ToolsPage.tsx:524`
  - `web/src/features/workflows/pages/WorkflowsPage.tsx:587`
- 生成提交逻辑不变（model_id 有值才传）。

---

## 需求 2：菜单改名移位 + ERP 字典 + 完整连接器配置流程

### 菜单与标题
- [web/src/layouts/AdminLayout.tsx:52-60]：`{ to: '/admin/connectors', label: '与ERP集成' }` 移到 data-sources 之前。
- ConnectorsPage 标题「ERP 与外部集成」→「与ERP集成」。

### 迁移 000036（integration schema）
- 全局字典表 `integration.erp_systems`（无 RLS，全局只读字典）：
  - 列：`system_code text PK`、`display_name text`、`adapter_key text`、`implemented boolean`、`is_default boolean`、`sort_no int`。
  - 种子：`(nc65, 用友NC65, nc65, true, true, 1)`、`(kingdee_cloud, 金蝶星空云, kingdee, false, false, 2)`、`(sap, SAP, sap, false, false, 3)`。
- 租户连接表 `integration.erp_connections`（RLS + FORCE RLS + 审计触发器，模式照抄 llm.endpoints）：
  - `id uuid PK`、`tenant_id`、`system_code → erp_systems`、`name text`、`base_url text`、`credential_ciphertext bytea`（secretbox 信封，AAD 绑定 connection_id）、`credential_ref text`、`credential_masked text`、`status text`（ACTIVE/DISABLED/DELETED）、`created_by`、`created_at/updated_at`、软删语义（status=DELETED，审计追溯）。
- 权限种子（按现有 iam 权限表名与 sysadmin 绑定方式，实现时对照 dataset.go/既有迁移）：新增 `erp.connection.read`、`erp.connection.write`、`tool.invoke`、`tool.approve`（ON CONFLICT DO NOTHING；sysadmin 角色补绑定）。

### 后端（新文件 internal/api/handlers/erp_connections.go + store/erp_connections.go）
- `GET /api/v1/admin/erp-systems`：字典列表（含 implemented/is_default）。
- `GET /api/v1/admin/erp-connections`：cursor 分页（默认 10，最大 200）+ `?status=&system_code=&q=`；**绝不 SELECT credential_ciphertext 本体**，只回 credential_masked + has_credential。
- `POST /api/v1/admin/erp-connections`：创建（system_code 必须为字典中 implemented=true 的项，否则 422；金蝶/SAP 明确报「适配器未实现」）；凭据双路径（ciphertext 优先 / credential_ref 回落）与 llm.endpoints 同规则；写审计 + SSE。
- `PATCH /api/v1/admin/erp-connections/{id}`：三态凭据（省略=不改；非空=替换；显式空串=清空，清空后必须保留非空 credential_ref 否则 400）。
- `DELETE /api/v1/admin/erp-connections/{id}`：软删。
- `POST /api/v1/admin/erp-connections/test`：用入参直连测试（nc65 走其健康检查/登录探针），**不落库、不产生业务写入**；凭据明文不进日志/审计/SSE。
- 写操作全部要求 `Idempotency-Key`（ASCII 校验沿用现有 helper）。
- 动态装配：
  - 新建 `internal/erp/dynamic.go`：`DBPortResolver` 实现 `erp.PortResolver`——按 tenant+system 查 integration.erp_connections（ACTIVE），解密凭据（secretbox，AAD=connection_id），构造 nc65 Adapter；解析顺序：**DB 连接优先 → env 装配的传统端口（显式标注 legacy）→ 无则 fail-closed UNAVAILABLE**，绝不静默伪造。
  - main.go：OperationGateway.Ports 换成该 Resolver（env 端口作为显式回落项注入）。
- 解密失败**不回落** credential_ref，直接报错（沿用模型端点硬规则）。

### 前端（web/src/features/erp-connectors/）
- ConnectorsPage 重构：
  - 顶部「ERP 系统类型」字典卡片：`GET /admin/erp-systems` 渲染，默认选中 用友NC65，未实现项明确标注「适配器未实现」且创建弹窗中禁用。
  - 「ERP 连接」列表（10 行/页 + 筛选）：系统类型、名称、base_url、凭据打码、状态、操作（编辑/删除/测试）。
  - 「新增连接」弹窗：类型下拉（字典，默认 NC65）+ 名称 + base_url + 凭据字段 + **测试连接**按钮（调 test 端点，不入库）；创建走 POST，带 Idempotency-Key（沿用 `makeIdempotencyKey` 安全转换）。
  - 底部保留现有「已安装连接器插件」清单。
- api.ts 增加 erp-systems / erp-connections 封装与领域类型。

---

## 需求 3：工具真实执行 + 审批门禁

### 迁移 000037（runtime schema）
- `runtime.tool_invocations`：`id uuid PK`、`tenant_id`、`tool_id`、`tool_version_id`、`tool_key`、`effect_class`、`status`（PENDING_APPROVAL/APPROVED/EXECUTING/SUCCEEDED/FAILED/REJECTED/CANCELLED）、`input_payload jsonb`、`output_payload jsonb`、`error_code`、`error_summary`、`requested_by`、`approved_by`、`approval_decided_at`、`started_at/finished_at`、`created_at/updated_at`。RLS + FORCE RLS + 审计触发器。

### 执行器（新文件 internal/api/handlers/tool_executors.go）
按 execution_config.adapter 派发（从 LatestToolVersion 扩展查询 execution_config 列）：
- `mock-erp` / `nc65`：经 `h.deps.ERP`（Port：GetObject/QueryObjects/Execute）真实调用 mockerp 容器/NC65；object/op 取 execution_config。
- `docingest`：复用 internal/docingest 真实提取（word/excel/text）。
- `shacl`：真实 SHACL 引擎求值（输入含 manifest+实例）。
- `graphload`：graphload.ParseManifest 真实解析。
- `llm-gateway`：经 `h.llmGateway()` 完整治理链（限流+预算+缓存+路由+结构化输出），结果入用量账本。
- `retriever`：对制度文档根目录（Deps 已有 DocsRoot，生成功能在用）做真实词项检索打分。
- `ledger` / `audit-chain`：经 audit.Recorder.Append 写真实审计事件（哈希链）。
- `ocr` / `budget` / `evidence` / 未知 adapter：**fail-closed UNAVAILABLE**，明确说明原因（无真实后端 / 证据锚定依赖 Run 上下文），绝不伪造输出。

### Invoke 流程重写（handlers/tools.go）
- READ / INTERNAL_WRITE：校验 input→执行器→写 tool_invocations（SUCCEEDED/FAILED）→审计+SSE，返回 200。
- EXTERNAL_WRITE：创建 `PENDING_APPROVAL` 记录（需 `tool.approve` 权限者审批），审计+SSE，返回 202 + invocation id。
- `POST /tools/{id}/invocations/{invId}/approve`：`tool.approve` 权限 → 状态置 APPROVED→EXECUTING→执行器→落结果（同事务推进状态，事务外远端调用，照 OperationGateway 事务纪律）。
- `POST /tools/{id}/invocations/{invId}/reject`：置 REJECTED。
- `GET /api/v1/tools/{id}/invocations`：cursor 分页 + `?status=`。
- 幂等键：approve/reject/create 均要求 Idempotency-Key；审批通过后执行阶段禁止重复远端调用（状态机守卫）。
- 审计事件：tool.invoke.requested / approved / rejected / executed（沿用审计词表风格，新增 event 类型同步词表）。

### 前端（ToolsPage）
- 测试调用弹窗：执行后展示真实输出/错误；EXTERNAL_WRITE 提交后显示「待审批」状态。
- 新增「调用记录」区块（10 行/页 + 状态筛选）：状态、请求人、审批人、耗时、输出摘要。
- 待审批记录对具备 `tool.approve` 权限的用户显示「通过/驳回」按钮（PermissionGate 包裹）。
- 移除现有「测试调用恒 503」横幅，改为按工具 effect_class 渲染真实边界说明。

### 契约
- openapi.yaml：新增 `/admin/erp-systems`、`/admin/erp-connections`(+`/{id}`、`/test`)、`/tools/{id}/invocations`(+approve/reject)、`/runs/{id}/erp-operations`；更新 `/tools/{id}/invoke`（200/202/4xx）与列表端点新查询参数。
- router_test.go：contractEndpoints 同步全部新增路由；跑 `go run ./tools/contract-lint`。

---

## 需求 4：10 行/页 + 筛选查询

### 后端（补筛选参数，全部 cursor 分页默认 10）
- `/agents`：+`?status=&q=`（store.ListAgents 扩展）。
- `/workflows`：+`?status=&q=`。
- `/ontologies`：+`?status=&q=&category=`。
- `/tools`：+`?status=&q=`（保留 effect_class）。
- `/admin/users`：+`?status=&q=`；limit 错误不再忽略（返回 400）。
- `/admin/organizations`：**补 limitParam(10) cursor 分页** +`?q=`（当前无分页，硬约束违规）。
- `/admin/connectors`：+`?q=`。
- `/audit-events`：handler 默认 limit 100 → `limitParam(r, 10)`（既有筛选保留）。
- `/runs`：+`?q=`（objective/business_type ILIKE）。
- 其余列表（evaluations/releases/policies/roles/role-bindings/permissions/model-providers/erp）核对 limitParam 默认值=10，缺失筛选按上表模式补 `?status=&q=` 的适用子集。

### 前端
- 新建 `web/src/components/list-filter-bar.tsx`：状态 Select + 关键字 Input（300ms 防抖）+ 重置，query 参数进 useQuery key。
- 所有管理端列表页 `limit` 统一改 10 并接入 FilterBar：AgentsPage、ToolsPage、WorkflowsPage、OntologyListPage、UsersPage、OrganizationsPage、ConnectorsPage、AuditPage、MonitoringPage(runs)、EvaluationsPage、ReleasesPage、PoliciesPage、IdentityPage(roles/bindings/permissions)、ModelsPage（如为列表形态）。
- DataTable 保持「加载更多」cursor 形态（硬约束禁止 offset 深分页），每页 10 条。

---

## 需求 5：静默降级核查

- 注册 `GET /runs/{id}/erp-operations`（handler+store 查询 ERP operations 账本 by run_id，RLS 内）→ 消除任务详情页 NOT_FOUND 横幅。
- 全仓 sweep 并修复：
  - `internal/`：SQL `coalesce(...,0)` 掩盖缺失、handler 返回常量当业务数据、错误被吞（`_ = err`）。
  - `web/src/`：`?? 0`/`|| 0` 补零掩盖缺失（管理概览类页面改红色提示横幅——已有规范）、空 `.catch(()=>{})`。
- `/admin/organizations` 分页缺失（并入需求 4 修复）。
- 发现即修，无法立即修的明确列出并说明（不静默）。

---

## 实施顺序

1. 需求 1（独立、最小）：httpprovider 超时修复 + useModelOptions + 4 弹窗替换。
2. 需求 2：迁移 000036 → store/handlers → 动态装配 → 前端连接器页。
3. 需求 3：迁移 000037 → 执行器 → invoke/approve 流程 → ToolsPage。
4. 需求 4：后端筛选参数 → FilterBar + 各页接入。
5. 需求 5：erp-operations 路由 + 全仓 sweep。
6. 每步完成即 `go build ./... && go vet ./... && go test ./...`（含 integration 标签用例）+ `go run ./tools/contract-lint` + web `npx tsc --noEmit`；迁移完成后单独重建 migrate 镜像，最后重建 api/web 容器并浏览器实测。

## 验证（端到端）

1. 需求1：本体生成弹窗下拉选 DeepSeek 端点 → 生成成功（真实调用，观察耗时 >60s 不再超时）；无可用端点时显示明确提示。
2. 需求2：菜单「与ERP集成」位置与名称正确；字典显示 NC65(默认)/金蝶/SAP(未实现禁用)；创建 NC65 连接（先测试连接）→ 列表可见、凭据打码；编辑/删除软删生效；审计与 SSE 事件可见。
3. 需求3：`erp.expense.query` 测试调用返回 mockerp 真实数据；`doc.word.read` 真实解析文档；`erp.expense.decide` 提交后待审批 → 审批人通过后真实执行并落账本；驳回路径同样落审计；ocr/budget 显示明确 UNAVAILABLE 原因。
4. 需求4：各列表页 10 行/页、状态筛选与关键字查询经后端参数过滤（网络面板确认 query 参数生效）。
5. 需求5：任务详情页不再出现 erp-operations 404；全仓无 `?? 0` 类补零残留。
