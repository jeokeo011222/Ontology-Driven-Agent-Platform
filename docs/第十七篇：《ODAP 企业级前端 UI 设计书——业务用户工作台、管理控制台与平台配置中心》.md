# 第十七篇：《ODAP 企业级前端 UI 设计书——业务用户工作台、管理控制台与平台配置中心》

文档定位： 本篇是 ODAP（Ontology-Driven Agent Platform，企业本体驱动智能体平台）的前端产品设计总规范，覆盖面向普通业务用户的前端 UI，以及面向管理员、开发者和运维人员的后端设置 UI。

> **本篇 scope 标签**：前端 MVP 以 §14.3 六页冻结为准（业务工作台、任务执行详情、审批中心、本体工作台、平台设置、审计与评估，均 P0）。`[MVP-V1.0]` 上述六页 + SSE 实时通道（§10.6）+ TanStack Query 数据层（§10.3，ADR-004 待评审）；`[V1.5]` 深色主题、移动端完整审批、通知邮件摘要；`[V2]` 低代码应用构建。API 路径以 `contracts/03` 为唯一真源，本篇端点表只引用不改写；状态词表以 `contracts/01` 为唯一真源。

前十六篇已经完成了 ODAP 的总体架构、权限、插件、本体、Agent、Harness、ERP 接入、语义推理、审计、评估和工程实施设计。本篇要解决的是最后一个重要问题：

如何把复杂的企业本体、智能体执行、业务流程、权限审批和证据体系，设计成普通员工容易使用、管理员容易配置、开发者容易调试、审计人员容易追溯的统一产品界面。

这不是单纯的页面美化方案，而是前端的信息架构、交互规范、组件体系、页面设计、权限行为、API 契约、状态呈现和工程实施标准。

# 一、产品设计目标与总体原则

## 1.1 ODAP 的用户分层

ODAP 面向的不是单一用户群体。建议从产品设计阶段就明确五类角色。

[Peirlo HR - Modern HRMS](https://images.openai.com/static-rsc-4/TnwE2Q2R6IXQapaxyMYXaW4vffeHtC8qvwBB32z7GZiD4xgTDsC9enlP8cweW11TuaOLncY99sXjgiSFe-TejEr9D-WZ-Cjkf9L3HOseudTJTRa89Da3ii-7dDGEdbQkwoDh8St01OtanfTG_Pr8ObiVb4laaS3sQw2rJTGa3YI?purpose=inline)

1\. 业务用户

员工、部门负责人、业务专员。

主要关心任务能否完成、需要提供什么材料、是否需要自己确认，以及结果是否可靠。不应要求他们理解 RDF、SHACL 或 Agent Harness。

2\. 业务管理者

部门主管、财务主管、流程负责人。

关注待办审批、业务规则命中、例外情况、风险和执行结果。

[Visual Ontology Modeling for Domain Experts and Business Users with metaphactory](https://images.openai.com/static-rsc-4/GR4Z7LnXf-cPYun9S_7toe1CkxawUdDH1pNg0qthXP5Fr-TZLvRFIjIeHdBkOtO00onDVkg7KrhrqYqZLnnJSd67yIbMpRdXZ6bp3wQWuweMI1AgTd2-5SVYWj5FrIPylRvMJho4q6-RYyENg5kWOby-HmSKjUd8HYLLTY87f_o?purpose=inline)

3\. 本体与 Agent 设计者

企业架构师、业务分析师、Agent 开发者。

负责维护本体、语义映射、工具、工作流、Agent 版本与验证测试。

[Management • Manage roles • Palantir](https://images.openai.com/static-rsc-4/No-icTTJNW7h8vyMUlSx06QVy9NBl7HLGd0OtriNxj-hVcbpX7RunYJsJR05PfSbDD-mrWxYIsRQpof1qK43DEDuRQxWqfiMGUcvP6Mxj4COSpteV8nsmcgkTe4w4LJxkB1Apvr7gWSiS0rsEFa11XtanYmw2Ym_tzlawsHQpME?purpose=inline)

4\. 平台管理员

系统管理员、安全管理员、租户管理员。

负责用户、权限、模型接入、插件注册、数据源、策略、运行资源与系统配置。

[Agentic Observability with MCP Apps - Amazon OpenSearch Service](https://images.openai.com/static-rsc-4/gmtBXa2X6rvnpeR46ummOcwaEY6AB3xMRmksuslYvcboZWVrJw0SyWESNXXzZJnckmA8Huc5QdtmyAoPl1D7-0bwzOocJw6Z74_v_1lkMWdJbbhCTVVNKDUBee1mZw7xioAgrrBJpxmmFfJmsfwSWgRZm3VVExEoST3VeparYx0?purpose=inline)

5\. 运维与审计人员

SRE、合规、内审、质量负责人。

关注服务健康、失败任务、操作账本、证据完整性、质量门禁和版本变更。

同一个用户可以兼任多个角色，但 UI 不应仅靠角色名称判断访问权限。页面、按钮、字段和数据都应服从后端的实际授权决策。

## 1.2 六项核心设计原则

1. 业务优先： 普通员工看到业务任务和业务结果，而不是底层技术名词。
2. 执行透明： 用户能知道 Agent 做了什么、依据什么、下一步是什么。
3. 权限可见： 清楚区分可以自动执行、需要审批、无权执行和暂时无法确定的操作。
4. 配置可治理： 本体、规则、Agent、模型和工作流都有版本、验证与发布过程。
5. 证据可追溯： 每一个关键结果都可以跳转到来源、规则、审批和执行记录。
6. 前端不承担安全职责： 隐藏按钮只是交互优化，真正的权限校验必须由服务端执行。

# 二、整体产品形态：两个工作台、一个统一入口

建议 ODAP 采用统一 Web 应用，内部划分为两个主要工作台。

## ODAP 企业本体驱动智能体平台

统一身份、统一搜索、统一通知、统一权限、统一审计。

A. 业务用户工作台

员工、业务主管、审批人。

AI 业务助手

我的任务

业务应用

审批中心

执行报告

B. 平台管理控制台

管理员、架构师、开发者、审计人员。

本体与数据

Agent 与工具

工作流与策略

模型与集成

运行监控与质量

统一治理层

身份认证、RBAC/ABAC、租户隔离、审批策略、审计日志、版本与发布管理。

两个工作台不是两套独立系统，而是共享组件、设计语言、身份上下文和 API 契约的两个产品空间。

业务用户可以通过授权入口查看自己的任务；平台管理员则可从管理控制台进入更复杂的配置、诊断和发布功能。

## 2.1 推荐的顶层导航

业务工作台：

- 工作台
- AI 业务助手
- 我的任务
- 审批中心
- 业务应用
- 历史记录

平台管理控制台：

- 管理概览
- 身份与权限
- 企业本体
- 数据接入与映射
- Agent 与工具
- 工作流与 Harness
- 模型与推理
- ERP 与外部集成
- 策略与审批
- 运行监控
- 审计与证据
- 评估与质量门禁
- 插件与版本发布
- 系统设置

导航必须依据实际权限动态生成，但服务端必须对每个目标路由和 API 再次授权。

# 三、全局视觉设计规范

## 3.1 品牌与界面风格

ODAP 建议采用以下视觉定位：

企业级、克制、专业、可信赖；业务端简洁，管理端高密度，技术端可深入。

不建议采用过度炫酷的 AI 科技风、全屏渐变、到处发光的卡片或大面积动画。ODAP 的价值在于企业业务执行的可控性和可追溯性，界面应体现稳定、清晰和秩序。

## ODAP 视觉基准

我的工作台

待处理任务与业务进展

待办

## 08

已完成

## 24

业务用户端

浅色背景、低视觉噪声、明确的主操作。

平台管理

执行策略

已验证 · v1.2

工作流版本

草稿 · v1.3

平台管理端

表格、配置面板、版本差异与状态信息。

以上为视觉方向示意，不是最终页面截图。实际组件应使用统一的设计令牌实现。

## 3.2 色彩系统

建议使用一套中性的基础色，配合少量语义色。

| 用途   | 建议颜色         | 使用规则        |
| ---- | ------------ | ----------- |
| 品牌主色 | 深蓝 `#2457A7` | 主操作、当前选中、链接 |
| 深色文字 | `#172B4D`    | 标题、关键业务信息   |
| 次级文字 | `#667085`    | 辅助说明、时间、注释  |
| 页面背景 | `#F4F6F8`    | 工作区背景       |
| 卡片背景 | `#FFFFFF`    | 内容面板、表格、表单  |
| 成功   | `#16803C`    | 已确认完成的状态    |
| 警告   | `#B54708`    | 待复核、风险提醒    |
| 错误   | `#B42318`    | 失败、拒绝、阻断    |
| 信息   | `#175CD3`    | 提示、进行中、辅助状态 |

颜色不能成为唯一的状态表达方式。成功、失败、警告等状态必须同时配合文字和适当的图标。

## 3.3 字体、间距与布局

- 字体：优先使用系统无衬线字体栈，中文保持清晰易读。
- 基础字号：14px。
- 页面标题：24–28px。
- 区域标题：16–18px。
- 辅助文字：12–13px，重要内容不得依赖过小字号。
- 基础间距：4px 的倍数。
- 圆角：输入框 6px，卡片 8–12px。
- 业务页面内容最大宽度可按场景限制；管理端表格可占满主工作区。
- 复杂页面优先采用分区和标签页，不应无限纵向堆叠。

## 3.4 明暗主题

首期建议默认浅色主题。

深色主题可以作为后续增强，尤其适用于长时间查看日志、Trace 和执行图的技术人员，但不应为实现深色主题而延误业务闭环。

# 四、业务用户 UI 详细设计

业务端的核心任务不是“让用户管理 Agent”，而是让用户以最少的认知负担完成业务任务，并在关键决策点理解系统的判断。

## 4.1 页面一：业务工作台首页

### 页面目标

用户登录后，第一眼就知道：

- 有什么事情需要处理。
- 哪些任务正在运行。
- 哪些任务需要自己提供信息或确认。
- 最近完成了什么。
- 遇到异常时应该去哪里处理。

### 页面布局

# 我的工作台

业务任务与执行进度

待我处理

# 08

审批、补充材料、确认

运行中

# 03

正在执行的业务任务

已完成

# 24

所选统计周期

需关注

# 02

异常、失败或待复核

快捷操作

发起业务任务

描述目标，由 ODAP 生成受控执行计划

进入审批中心

处理需要人工确认的业务动作

最近任务

报销单审核 · claim-1001

10:32 · 需要审批

待审批

报销单审核 · claim-0998

09:45 · 执行完成

已完成

页面原型中的数量与业务记录均为演示数据。

### 功能要求

1. 指标只统计当前用户有权访问的任务。
2. 每个指标都能点击进入对应筛选后的列表。
3. 任务状态必须由后端状态驱动。
4. 运行中任务应支持刷新和继续查看。
5. 对失败任务提供明确的下一步操作，而不是只显示“执行失败”。
6. 不向普通员工暴露模型密钥、内部 Prompt、其他租户数据或不必要的技术日志。

## 4.2 页面二：AI 业务助手

这是 ODAP 面向普通业务用户的核心交互入口。

### 设计原则

采用“自然语言输入 + 结构化执行计划 + 明确的业务结果”三段式交互，而不是把整个产品设计成普通聊天机器人。

## AI 业务助手

描述你要完成的工作，ODAP 将检查条件并生成执行计划。

用户请求

请检查报销单 claim-1001，核对凭证、费用类别和预算，并在符合制度时提交审批。

ODAP 的执行计划

待确认

1. 读取报销单及关联业务数据。
2. 检查本体约束、凭证完整性和费用规则。
3. 核验当前用户权限和所需审批。
4. 条件满足后提交审批并核验结果。

执行边界

不会直接付款；需要审批的动作将进入审批流程。

查看详情

确认并开始

输入框原型

描述你的业务目标……

### 交互状态

| 状态     | UI 行为           |
| ------ | --------------- |
| 正在理解   | 显示请求已接收，不伪造推理进度 |
| 需要补充信息 | 明确列出缺失字段或业务对象   |
| 计划待确认  | 展示步骤、风险和预期副作用   |
| 执行中    | 显示实际步骤状态与已确认进度  |
| 等待审批   | 展示审批人、审批内容和下一步  |
| 结果待核验  | 明确说明外部操作尚未确认    |
| 已完成    | 展示业务结果和证据入口     |
| 失败或需复核 | 给出错误原因、可执行的恢复选项 |

需要注意：并非所有只读操作都需要用户逐项确认。可由策略决定哪些计划自动启动、哪些动作必须确认；界面必须遵守策略，不能自行降低确认门槛。

## 4.3 页面三：我的任务

任务列表应提供：

- 按状态、业务类型、发起时间和执行结果筛选。
- 按任务 ID 或业务对象编号搜索。
- 查看执行进度、最近更新时间和当前等待原因。
- 查看任务详情与历史记录。
- 在权限允许时取消尚未执行的任务。
- 对失败任务查看恢复建议。

推荐状态标签：

`待运行`、`运行中`、`待审批`、`待对账`、`已完成`、`已失败`、`已取消`、`需人工复核`。

不能把“等待 ERP 对账”显示为普通的“运行中”，也不能将未知的外部操作结果直接显示为“失败”。

## 4.4 页面四：审批中心

审批中心是业务端的高风险交互页面，应优先保证清晰、完整和不易误操作。

审批详情应展示：

- 申请人、业务对象和所属组织。
- 本次具体操作与预期业务影响。
- 关键输入参数及其版本。
- Agent 的建议与结构化理由。
- 适用规则、事实来源与异常项。
- 操作风险等级及风险分类。
- 当前授权范围和审批链。
- 审批摘要或操作参数的不可变版本标识。
- 审批通过、拒绝、退回补充信息等选项。

审批通过不等于外部操作已经成功。审批页面应将“审批状态”和“ERP 执行状态”分别呈现。

对于高风险操作，建议增加二次确认，并清晰说明该确认会授权什么操作、作用于哪个对象、有效期多久。是否需要双人审批、双人复核或职责分离，应由策略配置决定。

## 4.5 页面五：业务应用

业务应用不建议一开始就建设任意拖拽式应用构建器。

首期通过标准化业务应用模板承载本体和 Agent 能力，例如：

- 费用与报销。
- 合同审查。
- 采购审核。
- 企业知识问答。
- 经营分析。

每个应用至少配置：

- 应用名称、图标、说明。
- 适用组织与用户范围。
- 可使用的业务对象。
- 允许的任务类型。
- 所关联的本体、Agent、工作流版本。
- 结果展示模板。
- 允许的业务操作与审批策略。

用户只看到自己有权使用的应用。应用内部的权限仍需按业务对象和具体操作再次校验。

## 4.6 页面六：执行报告

报告不应只有一段 LLM 总结。

建议采用五个标签页：

1. 业务结果： 完成了什么、当前业务状态是什么。
2. 执行过程： 实际执行步骤、耗时、重试与失败节点。
3. 判断依据： 本体版本、规则命中、事实来源、验证结果。
4. 操作与审批： 授权决策、审批记录、ERP 操作账本和回执。
5. 证据包： 证据目录、版本清单、摘要校验和下载权限。

报告中的推理解释应区分已验证事实、规则结论、模型建议和未知项。不能把模型生成的解释包装成已经验证的数学证明或业务事实。

# 五、平台管理 UI：总体布局与导航

管理控制台是 ODAP 的配置与治理中心。它不仅管理系统参数，还负责将企业业务规则、本体、Agent、工具、模型和工作流组合成可验证、可发布的业务能力。

## 5.1 管理控制台布局

建议采用固定左侧导航、顶部全局栏、中央工作区的经典企业管理布局。

## ODAP Console

Ontology-Driven Agent Platform

工作空间

管理概览

身份与权限

企业本体

数据接入

Agent 与工具

工作流与 Harness

模型与推理

策略与审批

运行监控

审计与证据

评估与质量

管理概览 / 平台健康

## 平台管理概览

服务状态

健康

待处理告警

## 02

最近运行

费用审核 · 成功

本体回归 · 需复核

演示布局：实际状态与数字必须由后端实时数据驱动。

## 5.2 全局顶部栏

顶部栏建议包括：

- 当前企业或租户。
- 当前工作空间。
- 全局搜索。
- 通知中心。
- 帮助与操作文档。
- 当前用户与身份切换入口（仅在允许的身份体系下）。
- 当前环境标识，例如开发、测试、生产。

生产环境标识应始终明显可见，避免管理员误把测试操作应用到生产环境。

## 5.3 管理概览首页

管理首页不应只堆叠图表。建议按四个层次呈现：

- 系统健康： API、Worker、数据库、模型提供方和外部连接状态。
- 业务运行： 任务量、成功率、失败类型、待审批数量和待对账操作。
- 语义健康： 本体验证失败、映射问题、数据新鲜度和语义回归。
- 治理与质量： 近期策略变更、失败门禁、未处理安全告警和待发布版本。

每项指标必须标明统计范围、时间窗口和数据来源。平台健康不能只用一个综合分数表示，更不能让平均成功率掩盖未授权写入等严重问题。

# 六、企业本体 UI 设计

本体是 ODAP 与普通 Agent 平台的关键区别之一。因此，本体管理不能只是一张知识图谱可视化，而应形成从建模、映射、验证、版本管理到发布的完整工作台。

## 6.1 本体工作台的信息架构

建议将本体管理划分为以下子页面：

- 本体包列表。
- 类与对象类型。
- 属性与关系。
- 业务约束。
- 数据源映射。
- 图谱实例浏览。
- SHACL 验证报告。
- 推理结果与来源追踪。
- 版本差异。
- 发布与回滚。

## 6.2 本体编辑器布局

## Finance Ontology

草稿 v1.3.0 · 基于已发布版本 v1.2.0

未发布

结构

关系

约束

版本

类与关系

ExpenseClaim

报销单 · 企业业务对象

submittedBy → Employee

提交人关系

expenseCategory → ExpenseCategory

费用类别关系

hasReceipt → Receipt

凭证关联

验证发现项

必需关系缺失

某个测试对象缺少 submittedBy 关系；是否阻断发布取决于已配置的约束级别。

## 6.3 本体编辑与发布规则

1. 本体采用草稿、验证中、已验证、待发布、已发布、已废弃等状态。
2. 编辑草稿不应直接影响生产运行中的版本。
3. 发布前必须执行结构校验、SHACL 验证、语义回归和依赖兼容性检查。
4. 发布报告必须显示与上一个稳定版本的差异。
5. 工作流和 Agent 运行时必须锁定所使用的本体版本。
6. 发布新版本时不能静默改变正在执行任务的语义上下文。
7. 回滚应恢复到明确的已发布版本，并评估依赖该版本的任务是否需要重跑。

## 6.4 数据映射 UI

数据映射页面负责把 ERP 字段和外部数据转换为本体属性与关系。

应支持：

- 连接器与数据源选择。
- 源字段到本体属性的映射。
- 类型转换和规范化规则。
- 主键与对象身份映射。
- 空值、冲突、过期和异常值处理。
- 小样本预览。
- 映射版本比较。
- 测试运行与错误报告。
- 数据血缘与来源追踪。

映射页面应区分“源字段不存在”“源字段为空”“转换失败”和“多个来源互相冲突”，不能统一显示成一个模糊的“数据异常”。

## 6.5 图谱浏览器

图谱浏览器的主要目标是辅助理解关系和诊断问题，而不是取代查询语言。

建议提供：

- 按对象 ID 搜索。
- 查看对象类型与属性。
- 展示一跳或有限深度关系。
- 按命名图、来源、版本和权限过滤。
- 查看事实来源与推导链。
- 将结果导出为受权限控制的结构化文件。
- 限制节点数量、查询深度和执行时间。

图谱界面不得因为某个用户能够看到节点，就默认其可以查看该节点的全部属性或关联对象。

# 七、Agent、工具与工作流配置 UI

这是平台管理员和开发者使用频率最高的配置区域之一。

## 7.1 Agent 管理页

Agent 列表应展示：

- 名称与业务职责。
- 关联业务域。
- 当前版本与发布状态。
- 使用的模型策略。
- 可用工具。
- 关联本体和工作流。
- 最近评估结果。
- 最近运行状态。
- 是否允许进入生产环境。

Agent 详情页建议划分为：

- 基本信息。
- 输入输出契约。
- Prompt 与 Context 模板。
- 工具权限。
- 模型策略。
- 本体与规则依赖。
- 测试用例。
- 评估结果。
- 版本差异。
- 发布记录。

Prompt 编辑器必须与运行时真正使用的模板版本关联，不能让管理员修改了页面内容，却不知道生产运行时实际加载了哪个版本。

## 7.2 Tool Registry 页面

工具配置页面应强调能力边界和风险，而不是只展示工具名称。

每个工具至少展示：

| 字段        | 说明                |
| --------- | ----------------- |
| Tool ID   | 稳定标识              |
| 业务描述      | 工具的用途与边界          |
| 输入 Schema | 参数类型和校验规则         |
| 输出 Schema | 返回数据契约            |
| 权限要求      | 所需的业务操作权限         |
| 风险等级      | 只读、低风险写入、高风险操作等分类 |
| 幂等策略      | 是否支持幂等及其保证范围      |
| 超时与重试     | 默认限制与失败处理         |
| 版本        | 工具实现版本            |
| 测试状态      | 契约测试与安全测试结果       |

工具页面应支持模拟调用、参数校验和错误响应预览。模拟调用必须与真实写入隔离，并清楚显示当前运行模式。

## 7.3 工作流与 Harness 编辑器

建议首期采用结构化节点编辑器 + 表单配置，而不是一开始就实现复杂的自由拖拽流程编排器。

工作流节点类型可以包括：

- 读取业务对象。
- 语义查询。
- 本体验证。
- 业务规则评估。
- Agent 规划。
- 工具调用。
- 权限检查。
- 人工审批。
- ERP 操作。
- 结果核验。
- 证据生成。
- 条件分支。
- 失败处理与人工接管。

每个节点需要配置输入输出 Schema、前置条件、超时、失败策略、重试策略和权限要求。

### 工作流状态图

\#chatgpt-mermaid-\_r_73i\_{font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";font-size:16px;fill:rgb(237, 237, 237);}@keyframes edge-animation-frame{from{stroke-dashoffset:0;}}@keyframes dash{to{stroke-dashoffset:0;}}#chatgpt-mermaid-\_r_73i\_ .edge-animation-slow{stroke-dasharray:9,5!important;stroke-dashoffset:900;animation:dash 50s linear infinite;stroke-linecap:round;}#chatgpt-mermaid-\_r_73i\_ .edge-animation-fast{stroke-dasharray:9,5!important;stroke-dashoffset:900;animation:dash 20s linear infinite;stroke-linecap:round;}#chatgpt-mermaid-\_r_73i\_ .error-icon{fill:rgb(48, 48, 48);}#chatgpt-mermaid-\_r_73i\_ .error-text{fill:rgb(237, 237, 237);stroke:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_73i\_ .edge-thickness-normal{stroke-width:1px;}#chatgpt-mermaid-\_r_73i\_ .edge-thickness-thick{stroke-width:3.5px;}#chatgpt-mermaid-\_r_73i\_ .edge-pattern-solid{stroke-dasharray:0;}#chatgpt-mermaid-\_r_73i\_ .edge-thickness-invisible{stroke-width:0;fill:none;}#chatgpt-mermaid-\_r_73i\_ .edge-pattern-dashed{stroke-dasharray:3;}#chatgpt-mermaid-\_r_73i\_ .edge-pattern-dotted{stroke-dasharray:2;}#chatgpt-mermaid-\_r_73i\_ .marker{fill:rgb(175, 175, 175);stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_73i\_ .marker.cross{stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_73i\_ svg{font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";font-size:16px;}#chatgpt-mermaid-\_r_73i\_ p{margin:0;}#chatgpt-mermaid-\_r_73i\_ .label{font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";color:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_73i\_ .cluster-label text{fill:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_73i\_ .cluster-label span{color:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_73i\_ .cluster-label span p{background-color:transparent;}#chatgpt-mermaid-\_r_73i\_ .label text,#chatgpt-mermaid-\_r_73i\_ span{fill:rgb(237, 237, 237);color:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_73i\_ .node rect,#chatgpt-mermaid-\_r_73i\_ .node circle,#chatgpt-mermaid-\_r_73i\_ .node ellipse,#chatgpt-mermaid-\_r_73i\_ .node polygon,#chatgpt-mermaid-\_r_73i\_ .node path{fill:rgb(9, 23, 44);stroke:rgb(31, 78, 148);stroke-width:1px;}#chatgpt-mermaid-\_r_73i\_ .rough-node .label text,#chatgpt-mermaid-\_r_73i\_ .node .label text,#chatgpt-mermaid-\_r_73i\_ .image-shape .label,#chatgpt-mermaid-\_r_73i\_ .icon-shape .label{text-anchor:middle;}#chatgpt-mermaid-\_r_73i\_ .node .katex path{fill:#000;stroke:#000;stroke-width:1px;}#chatgpt-mermaid-\_r_73i\_ .rough-node .label,#chatgpt-mermaid-\_r_73i\_ .node .label,#chatgpt-mermaid-\_r_73i\_ .image-shape .label,#chatgpt-mermaid-\_r_73i\_ .icon-shape .label{text-align:center;}#chatgpt-mermaid-\_r_73i\_ .node.clickable{cursor:pointer;}#chatgpt-mermaid-\_r_73i\_ .root .anchor path{fill:rgb(175, 175, 175)!important;stroke-width:0;stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_73i\_ .arrowheadPath{fill:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_73i\_ .edgePath .path{stroke:rgb(175, 175, 175);stroke-width:1px;}#chatgpt-mermaid-\_r_73i\_ .flowchart-link{stroke:rgb(175, 175, 175);fill:none;}#chatgpt-mermaid-\_r_73i\_ .edgeLabel{background-color:rgb(0, 0, 0);text-align:center;}#chatgpt-mermaid-\_r_73i\_ .edgeLabel p{background-color:rgb(0, 0, 0);}#chatgpt-mermaid-\_r_73i\_ .edgeLabel rect{opacity:0.5;background-color:rgb(0, 0, 0);fill:rgb(0, 0, 0);}#chatgpt-mermaid-\_r_73i\_ .labelBkg{background-color:rgba(0, 0, 0, 0.5);}#chatgpt-mermaid-\_r_73i\_ .cluster rect{fill:rgb(48, 48, 48);stroke:rgba(255, 255, 255, 0.15);stroke-width:1px;}#chatgpt-mermaid-\_r_73i\_ .cluster text{fill:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_73i\_ .cluster span{color:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_73i\_ div.mermaidTooltip{position:absolute;text-align:center;max-width:200px;padding:2px;font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";font-size:12px;background:rgb(48, 48, 48);border:1px solid rgba(255, 255, 255, 0.15);border-radius:2px;pointer-events:none;z-index:100;}#chatgpt-mermaid-\_r_73i\_ .flowchartTitleText{text-anchor:middle;font-size:18px;fill:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_73i\_ rect.text{fill:none;stroke-width:0;}#chatgpt-mermaid-\_r_73i\_ .icon-shape,#chatgpt-mermaid-\_r_73i\_ .image-shape{background-color:rgb(0, 0, 0);text-align:center;}#chatgpt-mermaid-\_r_73i\_ .icon-shape p,#chatgpt-mermaid-\_r_73i\_ .image-shape p{background-color:rgb(0, 0, 0);padding:2px;}#chatgpt-mermaid-\_r_73i\_ .icon-shape .label rect,#chatgpt-mermaid-\_r_73i\_ .image-shape .label rect{opacity:0.5;background-color:rgb(0, 0, 0);fill:rgb(0, 0, 0);}#chatgpt-mermaid-\_r_73i\_ .label-icon{display:inline-block;height:1em;overflow:visible;vertical-align:-0.125em;}#chatgpt-mermaid-\_r_73i\_ .node .label-icon path{fill:currentColor;stroke:revert;stroke-width:revert;}#chatgpt-mermaid-\_r_73i\_ .node .neo-node{stroke:rgb(31, 78, 148);}#chatgpt-mermaid-\_r_73i\_ [data-look="neo"].node rect,#chatgpt-mermaid-\_r_73i\_ [data-look="neo"].cluster rect,#chatgpt-mermaid-\_r_73i\_ [data-look="neo"].node polygon{stroke:url(#chatgpt-mermaid-\_r_73i\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_73i\_ [data-look="neo"].swimlane.cluster rect{filter:none;}#chatgpt-mermaid-\_r_73i\_ [data-look="neo"].node path{stroke:url(#chatgpt-mermaid-\_r_73i\_-gradient);stroke-width:1px;}#chatgpt-mermaid-\_r_73i\_ [data-look="neo"].node .outer-path{filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_73i\_ [data-look="neo"].node .neo-line path{stroke:rgb(31, 78, 148);filter:none;}#chatgpt-mermaid-\_r_73i\_ [data-look="neo"].node circle{stroke:url(#chatgpt-mermaid-\_r_73i\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_73i\_ [data-look="neo"].node circle .state-start{fill:#000000;}#chatgpt-mermaid-\_r_73i\_ [data-look="neo"].icon-shape .icon{fill:url(#chatgpt-mermaid-\_r_73i\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_73i\_ [data-look="neo"].icon-shape .icon-neo path{stroke:url(#chatgpt-mermaid-\_r_73i\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_73i\_ .node text{font-size:14px;font-weight:600;letter-spacing:normal;fill:rgb(153, 206, 255);}#chatgpt-mermaid-\_r_73i\_ .edgeLabels text{font-size:13px;font-weight:600;letter-spacing:-0.08px;fill:rgb(153, 206, 255);}#chatgpt-mermaid-\_r_73i\_ .node tspan[font-weight="normal"],#chatgpt-mermaid-\_r_73i\_ .edgeLabels tspan[font-weight="normal"]{font-weight:600;}#chatgpt-mermaid-\_r_73i\_ .edgeLabel .label rect{opacity:1;rx:13px;ry:13px;fill:rgb(0, 14, 26);stroke:rgb(26, 62, 95);stroke-width:1px;}#chatgpt-mermaid-\_r_73i\_ .node rect,#chatgpt-mermaid-\_r_73i\_ .node circle,#chatgpt-mermaid-\_r_73i\_ .node ellipse,#chatgpt-mermaid-\_r_73i\_ .node polygon,#chatgpt-mermaid-\_r_73i\_ .node path{fill:rgb(0, 40, 77);stroke:rgba(255, 255, 255, 0.1);stroke-width:1px;}#chatgpt-mermaid-\_r_73i\_ .node rect{rx:16px;ry:16px;}#chatgpt-mermaid-\_r_73i\_ .node.mermaid-decision .label-container{fill:rgb(0, 14, 26);stroke:rgb(26, 62, 95);stroke-dasharray:2,2;}#chatgpt-mermaid-\_r_73i\_ .edgePaths .flowchart-link{stroke:rgb(175, 175, 175);stroke-width:1px;stroke-linecap:round;stroke-linejoin:round;}#chatgpt-mermaid-\_r_73i\_ .marker{fill:rgb(175, 175, 175);stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_73i\_ :root{--mermaid-font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";}草稿静态校验校验通过?仿真与回归质量门禁通过?待发布发布审批已发布版本运行监控发现严重问题?停止新任务或回滚否是否是是否

工作流发布应采用不可变版本。对已发布工作流的修改必须创建新版本，再经过测试与门禁，不能直接修改正在运行的版本定义。

## 7.4 运行调试器

运行调试器面向开发者和运维人员，展示：

- Run、Step、Attempt 和 Trace ID。
- 实际执行顺序。
- 节点输入输出摘要。
- 工具调用及其耗时。
- 语义查询与验证结果。
- 模型请求元数据和 Token 使用情况。
- 授权与审批决策。
- 重试、超时和恢复记录。
- 证据生成状态。

敏感 Prompt、原始模型输入、员工隐私和业务秘密必须按照权限策略进行脱敏或禁止展示。调试器默认采用最小必要信息，不能因为用户是开发者就自动授予全部业务数据权限。

# 八、后端设置 UI：系统管理员配置中心

这里的“后端设置 UI”指管理 ODAP 平台本身的配置界面，不是直接操作数据库的后台页面。

配置中心需要把技术配置转化为受控、可验证、可审计的操作。

## 8.1 系统设置的一级菜单

建议包含：

1. 企业与租户设置。
2. 用户、组织与身份认证。
3. 角色、权限与授权策略。
4. 模型提供方与 LLM Gateway。
5. ERP 连接器与外部系统。
6. 数据源与同步任务。
7. 插件注册与制品管理。
8. Agent、工具与工作流默认策略。
9. 审批策略与风险分级。
10. 任务队列与执行资源。
11. 日志、监控与告警。
12. 审计、证据和数据保留。
13. 评估门禁与发布策略。
14. 系统安全与网络配置。

每个设置项都应有明确的适用范围：平台级、租户级、业务域级或应用级。低层级设置不能越权覆盖更高层级的安全底线。

## 8.2 模型提供方设置

模型配置页应包含：

- 提供方名称与协议类型。
- Endpoint 和部署名称。
- 模型标识。
- 凭据引用。
- 请求超时与并发限制。
- Token 预算与费用上限。
- 结构化输出能力。
- 可用性检查。
- 数据出境与隐私策略。
- 默认用途与允许使用的 Agent 范围。
- 模型版本及最近验证时间。

建议支持以下模型配置状态：

`未配置` → `待验证` → `验证通过` → `已启用`。

验证失败时显示可操作的错误，例如连接失败、认证失败、模型不可用或结构化输出不符合契约。

API Key 只允许写入秘密存储，不允许再次以明文回显。 页面可以显示已配置状态、最后验证时间和轮换入口，但不应在浏览器中缓存真实密钥。

模型切换也不应自动改变所有生产 Agent。应由模型策略明确规定适用范围，并经过相关任务集的评估与质量门禁。

## 8.3 ERP 与外部连接设置

每个连接器配置应包含：

- 连接器类型。
- Endpoint。
- 认证方式。
- 凭据引用。
- 允许访问的资源范围。
- 只读或写入能力。
- 超时、限流和重试配置。
- 健康检查。
- 数据映射版本。
- 最近同步时间。
- 最近错误与诊断信息。

建议为连接器设计独立的“测试连接”和“验证权限”功能。

测试连接只能执行预定义的安全探测。不得允许管理员在 UI 中随意输入 URL 后由服务器直接访问，以免形成 SSRF 或内部网络探测入口。

## 8.4 执行策略与风险设置

这是 ODAP 的关键管理页面。

建议将策略划分为四个层级：

- 操作分类： 读取、建议、提交、修改、删除、支付等。
- 授权范围： 用户、组织、业务对象和字段范围。
- 风险分级： 影响程度、可逆性、财务影响和数据敏感性。
- 执行要求： 自动执行、用户确认、单人审批、多人审批或禁止执行。

示例配置：

```

policy_id: finance.expense.submit
version: "1.0.0"

scope:
  tenant: current
  resource_type: ExpenseClaim

operation:
  action: SUBMIT_FOR_APPROVAL
  risk_level: medium

execution:
  mode: approval_required
  require_authorization: true
  require_fresh_resource_state: true
  bind_approval_to_payload: true
  idempotency_required: true

evidence:
  require_policy_decision: true
  require_erp_receipt: true

failure:
  unknown_external_result: reconcile
  reconciliation_failure: manual_review

```

以上仅为配置结构示例。实际政策应由企业业务和安全负责人确认，并经过类型校验、策略测试和发布审批。

需要特别区分：

- 审批策略决定什么情况下需要人类授权。
- 权限策略决定当前主体是否有资格执行操作。
- 业务规则决定业务对象是否满足操作条件。
- 系统执行策略决定如何处理超时、重试、对账和恢复。

四者不能合并为一个 LLM 判断，也不能通过单个“允许自动执行”开关统一控制。

## 8.5 用户、角色与权限管理

用户管理页至少包含：

- 用户列表与状态。
- 组织和部门归属。
- 身份提供方。
- 角色绑定。
- 业务范围授权。
- 字段级敏感数据访问。
- 近期安全事件。
- 授权有效期。
- 停用和权限撤销记录。

权限管理页建议支持“模拟授权决策”：

输入用户、资源、动作和上下文，系统返回允许或拒绝，以及经过权限过滤的决策理由。

例如：

- 用户 A 是否可以查看报销单 `claim-1001`？
- 用户 A 是否可以提交该报销单？
- 该操作是否需要审批？
- 哪条策略拒绝了该操作？

模拟决策功能只用于解释和验证，不能让管理员通过前端参数直接伪造正式授权上下文。

## 8.6 任务队列与执行资源

平台管理员需要查看：

- 待运行任务数量。
- 正在运行的 Worker 数量。
- 队列等待时间。
- 重试次数与失败率。
- 租约过期任务。
- 死信或需要人工接管的任务。
- 各类工具的并发与超时。
- 模型请求并发与预算。

管理 UI 可以允许管理员调整经过白名单验证的资源参数，但不能允许通过普通配置页面关闭租户隔离、取消权限检查或绕过操作核验。

## 8.7 审计、证据与数据保留

设置项应包括：

- 审计事件范围。
- 证据保留期限。
- 访问日志保留期限。
- 敏感字段脱敏规则。
- 证据导出权限。
- 归档策略。
- 删除审批与法律保留例外。
- 证据校验周期。

证据完整性、访问权限和保留期限是不同的概念。哈希能够帮助检测内容变化，但不能证明证据内容在生成时就是真实的，也不能防止拥有足够底层权限的攻击者改写全部数据。因此，重要场景还需要受控写入、权限分离、外部备份和必要的不可变存储。

# 九、权限、状态与危险操作的统一交互规范

ODAP 的很多页面涉及真实业务动作，因此必须形成统一的安全交互规范。

## 9.1 四种不同的操作状态

| 状态   | 界面表达         | 用户可以做什么      |
| ---- | ------------ | ------------ |
| 允许执行 | 显示可执行操作及适用范围 | 按策略执行        |
| 需要审批 | 显示审批条件和业务影响  | 发起审批或提交申请    |
| 明确拒绝 | 显示可公开的拒绝原因   | 申请权限或联系管理员   |
| 状态未知 | 明确说明结果尚未确认   | 查看对账状态或转人工处理 |

“暂时没有权限”“业务条件不满足”和“ERP 状态未知”不能共用同一种失败提示。

## 9.2 危险操作确认对话框

对于撤销发布、停用连接器、回滚策略或授权外部写入等高影响操作，确认框应展示：

- 操作对象。
- 操作影响。
- 适用租户与环境。
- 是否可逆。
- 是否需要额外审批。
- 需要输入的确认信息。
- 操作完成后的核验方式。

确认按钮的文字应该明确描述动作，例如“停用连接器”，而不是统一使用“确定”。

## 9.3 审批摘要绑定

审批 UI 与后端必须共同保证审批内容和实际执行内容一致。

推荐流程：

1. 后端生成标准化操作参数。
2. 计算参数摘要，并绑定对象 ID、版本、操作类型和有效期。
3. 审批人查看该摘要所对应的完整业务内容。
4. 审批通过后，服务端验证审批记录。
5. 执行前重新读取权威业务状态。
6. 如果关键参数或对象版本变化，则要求重新审批或转人工复核。

审批摘要不应只绑定一段展示给用户看的自然语言说明，否则实际执行参数可能在审批后发生变化。

## 9.4 危险操作 UI 规则

- 删除、停用和回滚等操作不能与普通导航链接采用相同的视觉权重。
- 关键操作应有明确的成功或失败反馈。
- 未收到后端确认时不能显示成功。
- 操作正在执行时防止重复提交，但不能只依赖前端按钮禁用实现幂等。
- 网络超时后应显示“结果待确认”，而不是自动重新发送非幂等操作。

# 十、前端技术架构与组件设计

建议前端使用 React + TypeScript + Vite，与前十六篇工程实施蓝图保持一致。

## 10.1 前端目录结构

```
web/
├── src/
│   ├── app/
│   │   ├── App.tsx
│   │   ├── router.tsx
│   │   ├── providers.tsx
│   │   └── route-guards.tsx
│   │
│   ├── api/
│   │   ├── client.ts
│   │   ├── generated/
│   │   ├── errors.ts
│   │   └── query-keys.ts
│   │
│   ├── components/
│   │   ├── ui/
│   │   ├── data-table/
│   │   ├── status-badge/
│   │   ├── error-state/
│   │   ├── empty-state/
│   │   ├── loading-state/
│   │   ├── confirm-dialog/
│   │   └── permission-gate/
│   │
│   ├── layouts/
│   │   ├── BusinessLayout.tsx
│   │   └── AdminLayout.tsx
│   │
│   ├── features/
│   │   ├── workspace/
│   │   ├── assistant/
│   │   ├── tasks/
│   │   ├── approvals/
│   │   ├── business-apps/
│   │   ├── ontology/
│   │   ├── data-mapping/
│   │   ├── agents/
│   │   ├── tools/
│   │   ├── workflows/
│   │   ├── llm-providers/
│   │   ├── erp-connectors/
│   │   ├── policies/
│   │   ├── monitoring/
│   │   ├── audit/
│   │   ├── evidence/
│   │   └── evaluation/
│   │
│   ├── hooks/
│   ├── lib/
│   ├── styles/
│   │   ├── tokens.css
│   │   └── globals.css
│   ├── types/
│   └── main.tsx
│
├── package.json
└── vite.config.ts
```

## 10.2 页面与业务逻辑分离

推荐使用 Feature-Based Architecture（按业务功能组织）。

例如，审批页面应包含：

- 页面组件：负责布局。
- Query Hook：负责读取待审批请求。
- Mutation Hook：负责提交审批动作。
- 表单 Schema：负责前端输入校验。
- API Client：负责通信与错误处理。
- Domain Type：负责审批对象类型。
- Permission Gate：负责 UI 层的可见性控制。

业务规则不能只写在 React 组件里。所有关键规则应由服务端负责，前端校验仅用于提高用户体验。

## 10.3 状态管理策略（选型已冻结）

前端数据层选型经评审冻结为 **TanStack Query（@tanstack/react-query v5）+ React Hook Form + Zod** 组合，理由与取舍记录于 **ADR-004《前端数据层选型》（状态：待评审）**；ADR 批准前按此组合实施，批准后本段改为引用 ADR 编号。不引入 Redux 等全局业务状态 Store——服务端状态的所有权在服务端，前端只做投影与缓存。

| 数据类型 | 管理方式 | 选型 |
| --- | --- | --- |
| 服务端数据 | 统一的数据获取与缓存层 | TanStack Query：query-key 集中管理（`api/query-keys.ts`），缓存失效按资源 + 版本维度显式声明 |
| 页面局部状态 | React State / URL 参数 | 筛选项、面板开合等不进 Query 缓存 |
| 表单状态 | 统一表单与 Schema 校验 | React Hook Form + Zod；Zod schema 与 `contracts/openapi.yaml` 生成的类型对齐，前端校验只做体验层，权威校验在服务端 |
| 用户会话 | 认证模块统一管理 | 只存当前身份的最小投影，不存权限全集；权限判断以服务端实时响应为准 |
| 执行中任务 | 后端任务状态驱动 | SSE 实时通道为主、退避轮询为降级（见 §10.6） |
| 不可变配置版本 | 后端提供版本 ID 和状态 | 版本号进入 query-key，版本变化即自然失效重取 |

不要把整个后端业务状态复制到全局前端 Store，再让多个页面各自修改副本。

## 10.4 API 契约与类型生成

建议从 `contracts/openapi/` 生成前端 API 类型。

```
OpenAPI Contract
       │
       ├── 后端 Handler 与 DTO 校验
       │
       └── 前端 TypeScript Types
                    │
                    ▼
              Feature Hooks
                    │
                    ▼
                Page UI
```

前端不得自行假设后端枚举、字段和错误代码。新增状态时，应同步更新 Schema、后端、前端和测试集。

## 10.5 统一页面状态组件

每个数据页面必须具备：

- 加载中。
- 加载失败。
- 空数据。
- 部分数据。
- 权限不足。
- 数据过期。
- 操作提交中。
- 操作结果未知。
- 操作成功。
- 操作失败及恢复指引。

这些状态应由共享组件实现，避免每个页面都使用不同的提示文字和交互模式。

## 10.6 实时通道（SSE）与长任务处理

### 10.6.1 通道定型：SSE 为主，轮询为降级

执行中任务、审批待办与对账状态的刷新方式定型为 **SSE（Server-Sent Events）**，端点 `GET /api/v1/stream`（契约见 `contracts/03` §1.9/§2），替代原“轮询或 SSE 等方式”的未决表述。

- **鉴权方式**：EventSource 无法自定义请求头，主鉴权方式为平台会话 Cookie（HttpOnly + Secure + SameSite，见第三篇 §4.4 与 §4.6 会话表），由浏览器自动携带；禁止在 URL query 中传递长期令牌。Bearer 令牌场景（如有）使用服务端签发的一次性 ticket 参数，ticket 一次性消费、短时效。
- **断线重连续传**：浏览器重连自动携带 `Last-Event-ID`；服务端按 stream 会话维护有界回放缓冲区（默认保留最近 5 分钟或 500 条事件），重连时从 `Last-Event-ID` 之后补发；断连超出缓冲区时改为全量快照重取，并提示“状态已重新同步”。
- **按租户/权限过滤**：过滤在服务端完成——连接建立时绑定租户与主体，事件按第三篇授权决策过滤后推送；前端不得假设“收到的事件即可信”，仍按 §11.2 三层权限结构处理。
- **背压策略**：事件分三级——`CRITICAL`（审批结果、写操作回执、对账状态变更、安全事件）、`IMPORTANT`（Run 状态转换、任务完成/失败）、`NORMAL`（进度百分比、日志流）。缓冲超过阈值时**丢弃 `NORMAL` 级事件并触发告警**（告警接入第十一篇），`CRITICAL`/`IMPORTANT` 保证投递；丢失的 `NORMAL` 事件由随后的快照事件兜底。
- **轮询降级路径**：仅当 SSE 不可用（代理不支持、连接持续失败）时降级为轮询，采用指数退避 + 抖动（1s → 2s → 4s → 上限 30s）；单资源轮询遵循 `contracts/03` §1.8 的长时操作规范（`GET /api/v1/tasks/{id}`），不得对运行资源无限轮询。降级状态在 UI 明确提示“实时通道降级”。

### 10.6.2 三类场景的通道选择

| 场景 | 主通道 | 降级路径 | 选择理由 |
| --- | --- | --- | --- |
| 运行监控（Run/Step 状态、进度） | SSE `IMPORTANT` 级 | 退避轮询（上限 30s） | 状态转换需近实时，进度百分比可丢；连接恢复后以快照对齐 |
| 审批待办（新审批、审批结果） | SSE `CRITICAL` 级 | 退避轮询（上限 15s）+ 页面可见性触发刷新 | 审批时效敏感，等待态不得依赖用户手动刷新 |
| 对账状态（UNKNOWN → 已确认） | SSE `CRITICAL` 级 | 退避轮询（上限 15s） | 结果未知是显式业务状态，转正必须及时驱散“执行中”假象 |

### 10.6.3 长任务轮询组件

长任务（本体生成、语义验证、回归评估、发布推进）统一封装为 `<TaskProgress>` 组件与 `useTask` hook：创建返回 202 + 任务引用后，组件按 `contracts/03` §1.8 以退避轮询 `GET /api/v1/tasks/{id}` 跟踪，任务终态后自动失效相关 Query 缓存。组件必须区分“排队中/执行中/已完成/失败/结果待确认”，不得把 202 渲染成成功。

### 10.6.4 乐观并发（ETag / version）

- 写操作默认携带 `If-Match`（值为资源 `ETag` 或 `version` 字段）；服务端校验不通过返回 `409 CONFLICT`（`contracts/02`），前端统一处理为“数据已被他人修改”：重新拉取最新版本，保留用户已填写的差异字段，提示确认后重试。
- 审批提交、策略发布、本体发布等关键写操作强制乐观并发；表单提交期间禁用重复提交，但幂等最终由 `Idempotency-Key` + 服务端保证（`contracts/03` §1.6），前端禁用按钮只是体验优化。

# 十一、路由、页面权限与 API 对应关系

建议前端路由遵循统一命名规则，并明确每个页面依赖的后端接口。

## 11.1 主要路由

| 路由                      | 页面       | 主要 API                          | 契约对齐说明（以 `contracts/03` 为唯一真源） |
| ----------------------- | -------- | ------------------------------- | --- |
| `/workspace`            | 业务工作台    | `/api/v1/workspace/summary`     | **BFF 聚合端点**：由前端 BFF 聚合层实现（底层组合 `/api/v1/runs`、`/api/v1/approvals` 等），契约登记进 `contracts/openapi.yaml`（contracts/03 §4 裁决） |
| `/assistant`            | AI 业务助手  | `/api/v1/runs`                  | contracts/03 §2（POST 创建 / GET 查询，cursor 分页） |
| `/tasks`                | 我的任务     | `/api/v1/runs`                  | 同上，配合 `?status=` 过滤（取值以 `contracts/01` 为准） |
| `/tasks/:runId`         | 执行详情     | `/api/v1/runs/{id}`             | contracts/03 §2 |
| `/approvals`            | 审批中心     | `/api/v1/approvals`             | 列表端点需登记 openapi.yaml 后实现 |
| `/approvals/:id`        | 审批详情     | `/api/v1/approvals/{id}`、`/api/v1/approvals/{id}/decision` | decision 已登记 contracts/03 §2；详情查询登记后实现 |
| `/apps`                 | 业务应用     | `/api/v1/apps`                  | **BFF 聚合端点**：由 BFF 层实现并登记进 `contracts/openapi.yaml`（contracts/03 §4 裁决） |
| `/admin`                | 管理概览     | `/api/v1/admin/overview`        | BFF 聚合端点，登记 openapi.yaml（底层 `/api/v1/runs`、`/api/v1/ops/alerts`） |
| `/admin/identity`       | 身份与权限    | `/api/v1/admin/roles`、`/api/v1/admin/role-bindings`、`/api/v1/admin/subjects/{id}/disable` | 第三篇 §14 |
| `/admin/ontologies`     | 本体列表     | `/api/v1/ontologies`            | contracts/03 §2 |
| `/admin/ontologies/:id` | 本体详情     | `/api/v1/ontologies/{id}`       | contracts/03 §2（`validate`/`publish` 为子资源 POST） |
| `/admin/data-sources`   | 数据接入     | `/api/v1/data-sources`          | 权威来源第十二篇；需登记 openapi.yaml 后实现 |
| `/admin/agents`         | Agent 管理 | `/api/v1/agents`、`/api/v1/agents/{id}/publish` | contracts/03 §2 |
| `/admin/tools`          | 工具管理     | `/api/v1/tools`、`/api/v1/tools/{id}/invoke` | contracts/03 §2 |
| `/admin/workflows`      | 工作流管理    | `/api/v1/plan-templates`        | contracts/03 §4 裁决：`workflows` 降级为模板资产路径，执行一律走 `/api/v1/runs` |
| `/admin/models`         | 模型配置     | `/api/v1/admin/model-providers` | 权威来源第九篇；需登记 openapi.yaml 后实现 |
| `/admin/connectors`     | 外部连接     | `/api/v1/admin/connectors`、`/api/v1/erp/capabilities` | ERP 能力发现已登记 contracts/03 §2；连接器管理权威来源第七篇，需登记 |
| `/admin/policies`       | 策略管理     | `/api/v1/policies`、`/api/v1/policies/{id}/versions` | contracts/03 §2 |
| `/admin/monitoring`     | 运行监控     | `/api/v1/ops/alerts` 等 + BFF 聚合 | 权威来源第十一篇；聚合视图登记 openapi.yaml |
| `/admin/audit`          | 审计中心     | `/api/v1/audit-events`          | contracts/03 §2 |
| `/admin/evaluation`     | 评估中心     | `/api/v1/evaluations`、`/api/v1/evaluations/{id}` | contracts/03 §2/§4 裁决（替代原 `/api/v1/evaluation/runs`） |
| `/admin/releases`       | 发布管理     | `/api/v1/releases`、`/api/v1/releases/{id}/promote`、`/api/v1/releases/{id}/rollback` | 第十五篇 §16.4 |

上表以 `contracts/03` 为唯一契约真源：标注“需登记 openapi.yaml”或“BFF 聚合端点”的路径，在登记进 `contracts/openapi.yaml` 前不得进入实现；前端不得为匹配页面自行创建重复业务端点。统一实时通道为 `GET /api/v1/stream`（§10.6）。

## 11.2 权限控制的三层结构

第一层：路由访问控制

未授权用户不能进入对应页面。

第二层：组件与字段控制

按权限决定按钮、数据字段和敏感信息是否展示。

第三层：后端强制授权

每个 API、对象、字段和业务操作均重新校验权限。

前两层用于改善交互体验，第三层才是安全边界。即使用户通过浏览器直接构造请求，也不能绕过后端授权。

# 十二、响应式布局、可访问性与企业使用体验

## 12.1 桌面优先，兼顾移动端

ODAP 的管理控制台包含大量表格、版本差异和执行详情，建议以桌面端为主。

- 桌面端：完整的双栏工作台、表格、图谱和调试视图。
- 平板端：允许折叠侧栏，保留审批和任务查询。
- 手机端：优先支持查看任务、处理审批、接收通知和查看简要报告。
- 手机端不应强行压缩复杂的本体编辑器或工作流调试器；可提示切换到桌面端。

## 12.2 可访问性要求

可访问性基线为 **WCAG 2.1 AA**，验收方式为自动化检测（axe-core 类工具）+ 键盘走查 + 对比度抽样，结果纳入 §13.2 验收清单：

- 正文文字对比度 **≥ 4.5:1**；大号文字（≥ 18pt/24px，或 14pt/18.7px 加粗）≥ 3:1；图标、输入框边框等非文本控件 ≥ 3:1。§3.2 色彩表中的语义色（成功 `#16803C`、警告 `#B54708`、错误 `#B42318`）在白色卡片背景上均满足 4.5:1，用作文字时直接取用，不得自行降饱和；深色文字 `#172B4D`、次级文字 `#667085` 在页面背景 `#F4F6F8` 上同样满足要求。
- 主要操作可通过键盘完成；焦点顺序符合视觉顺序，焦点指示可见（对比度 ≥ 3:1）；不存在键盘陷阱。
- 输入框具有可识别标签；状态变化能够被辅助技术感知（任务状态更新进入 `aria-live` 区域）。
- 错误提示说明具体字段与修复方向。
- 图谱和流程图提供可访问的文本替代信息。
- 高风险确认不依赖悬浮提示或颜色辨识。
- 支持适当缩放（至 200%）与系统减少动画设置。

## 12.3 表格设计规范

企业管理页面会大量使用表格，应统一：

- 服务端分页、排序和筛选。
- 可复制的业务 ID。
- 明确的列宽和长文本处理。
- 重要字段固定显示。
- 过滤条件可清除。
- 批量操作展示影响范围。
- 导出前重新校验数据访问权限。
- 不在列表中暴露用户无权查看的敏感字段。

## 12.4 通知中心

通知类型建议包括：

- 待审批。
- 需要补充信息。
- 任务执行完成。
- 执行失败或结果未知。
- 本体验证失败。
- 质量门禁未通过。
- 连接器异常。
- 高风险安全事件。

通知应支持已读、未读、筛选和跳转到具体业务对象。对高风险安全事件，通知不能代替正式告警、审计记录和事件响应流程。

**渠道边界**（三个通道，职责不重叠）：

- **站内通知中心**：默认通道，承载待审批、需要补充信息、任务完成/失败、门禁未通过等业务事件；支持已读、未读、筛选与跳转。
- **邮件**：可选通道，用于审批提醒与周期性摘要（频率可配置）；邮件不承载敏感业务明细，只含跳转链接。
- **告警通道**：高危安全事件（越权写入、审批绕过、跨租户访问、审计哈希链校验失败）**不走站内/邮件兜底**，必须进入第十一篇告警生命周期（Alert Manager、负责人、升级条件、恢复验证）；通知中心只展示告警的引用条目，不作为唯一告警载体。

# 十三、前端测试与质量验收

前端 UI 不能只通过“截图看起来正常”来验收。必须验证权限行为、状态一致性、错误处理和高风险交互。

## 13.1 测试层次

| 测试类型     | 测试重点                |
| -------- | ------------------- |
| 组件单元测试   | 状态标签、表单校验、确认对话框     |
| 页面集成测试   | 页面数据加载、筛选、提交和错误处理   |
| API 契约测试 | 前后端 Schema 和错误代码一致  |
| 端到端测试    | 用户发起任务、审批、查看结果      |
| 权限测试     | 越权访问、敏感字段和路由保护      |
| 可访问性测试   | 键盘导航、标签、对比度和状态提示    |
| 视觉回归测试   | 关键页面布局、组件和响应式变化     |
| 故障测试     | 网络中断、请求超时、重复点击、结果未知 |

## 13.2 必须通过的 UI 验收案例

## 前端验收清单

0 / 17

普通员工只能看到自己有权访问的任务和业务应用。

未授权用户不能通过直接输入管理端 URL 获取管理数据。

审批页面完整显示操作对象、影响、规则依据和参数摘要。

审批通过后，UI 不会错误地将其显示为 ERP 操作成功。

ERP 超时后，任务详情显示结果待确认，并提供对账入口。

本体发布前能够查看结构差异、验证报告和回归结果。

工作流发布必须经过校验和质量门禁。

模型凭据写入后不会以明文再次返回浏览器。

跨租户的任务、图谱、审计和导出请求都会被后端拒绝。

任务状态刷新后与服务端一致，不依赖浏览器本地猜测。

高风险操作具备明确的确认、失败反馈和审计记录。

页面具备加载、空状态、权限不足和异常恢复提示。

核心业务页面支持键盘操作和可读的状态提示。

关键页面通过 API 契约与端到端测试。

SSE 断线后按 Last-Event-ID 续传，关键事件（审批结果、对账转正）不丢失；降级为轮询时有明确提示。

切换租户后，Query 缓存、SSE 订阅与页面数据不残留上一租户内容；越权数据在服务端即被拒绝。

已冻结/已发布的工作流与计划在 UI 中只读，编辑入口禁用并引导"创建新版本"，禁止原地修改运行中版本定义。

复制前端验收清单

## 13.3 UI 发布门禁

以下问题应阻断正式发布：

- 前端能绕过服务端授权访问敏感数据。
- 审批操作参数与最终执行参数不一致。
- 将未知状态显示为成功。
- 密钥或敏感数据泄露到浏览器日志。
- 高风险操作缺少必要的确认或审批。
- 核心业务流程无法完成。
- 关键前后端契约不一致。

普通视觉瑕疵可以进入后续优化清单，但安全和业务正确性问题不能作为已知缺陷直接放行。

# 十四、前端开发实施路线

前端工作应与后端垂直切片并行，不建议等全部后端开发完成后才开始做 UI。

## 14.1 六个开发阶段

| 阶段    | 开发内容                          | 阶段验收               |
| ----- | ----------------------------- | ------------------ |
| UI-P0 | 设计令牌、基础组件、路由、认证上下文、API Client | 工程可启动，页面骨架完整       |
| UI-P1 | 工作台、AI 助手、任务列表、运行详情           | 能发起并追踪一个只读任务       |
| UI-P2 | 审批中心、业务应用、Mock ERP 浏览器        | 完成报销审核与审批交互        |
| UI-P3 | 本体工作台、数据映射、验证报告               | 可查看、编辑草稿、验证和比较本体版本 |
| UI-P4 | Agent、工具、工作流、模型、连接器和策略设置      | 可以配置并验证一个候选工作流     |
| UI-P5 | 监控、审计、证据、评估、发布与安全验收           | 完成端到端测试和管理闭环       |

## 14.2 每个阶段的交付要求

每个页面不只是完成静态布局，还必须包含：

- 页面路由和访问权限。
- API 契约与数据类型。
- 加载、错误和空状态。
- 表单验证。
- 提交与取消逻辑。
- 成功、失败和未知状态反馈。
- 审计相关操作的关联 ID。
- 对应的组件测试和页面测试。

## 14.3 MVP 最小页面组合

如果要优先得到一个可演示、可测试的版本，首期只需集中完成以下页面：

业务工作台

发起任务、查看待办和运行状态。

P0

任务执行详情

查看步骤、判断依据、错误和恢复状态。

P0

审批中心

审核操作内容、参数摘要和风险。

P0

本体工作台

查看本体对象、关系和验证发现项。

P0

平台设置

管理模型、连接器、权限与执行策略。

P0

审计与评估

核验操作证据、运行结果和发布质量。

P0

# 十五、最终产品形态与设计结论

完成本篇后，ODAP 的产品设计就形成了完整闭环：

- 前十六篇：定义平台的架构、语义、执行、安全、数据与工程实现。
- 第十七篇：定义用户如何使用平台、管理员如何配置平台，以及如何通过 UI 管理运行和发布。

建议将 ODAP 最终呈现为三个互相衔接的体验层。

第一层：业务体验层

让员工通过自然语言和标准业务界面完成任务，不需要了解本体、图存储和执行引擎的内部细节。

关键页面：工作台、AI 助手、我的任务、审批中心、执行报告。

第二层：语义与自动化设计层

让业务架构师和开发者把企业对象、关系、约束、工具、Agent 和工作流组织成可验证的业务能力。

关键页面：本体工作台、数据映射、Agent Registry、工具管理、工作流编辑器。

第三层：治理与运维层

让管理员、运维和审计人员管理权限、执行策略、模型、外部连接、运行状态和发布质量。

关键页面：身份与权限、系统设置、监控、审计、证据、评估与质量门禁。

## 我的最终建议

ODAP 的前端设计应坚持一个核心取舍：

业务用户端尽可能简单，管理配置端足够强大，所有关键执行行为始终透明、受控、可验证。

不建议在 MVP 阶段先做一个庞大而复杂的低代码平台，也不建议把所有能力塞进一个聊天窗口。先把报销业务闭环需要的六个核心页面做好，再逐步扩展本体设计器、工作流编辑器和完整管理控制台。

这样，ODAP 才能同时满足两种要求：普通员工觉得它易用，企业技术和安全团队又能够真正信任它。

至此，ODAP 的第十七篇前端 UI 设计书完成。它与前十六篇共同构成从总体架构、核心引擎、安全治理到产品交互的完整设计体系。下一步最有价值的工作已经不是继续增加设计篇章，而是依据第十六篇工程蓝图，建立仓库、冻结 API 契约，并以“报销审核与提交审批”为第一个可运行的垂直切片，开始实际开发。