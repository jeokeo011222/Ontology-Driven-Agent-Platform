# ADR-005：图存储选型与 GraphStore 适配器边界

- **状态**：已接受
- **日期**：2026-10-14
- **决策域**：RDF 图存储后端、Go 与图存储的访问契约、租户隔离与事务边界（跨第十三篇、第二篇、第十六篇）
- **编号说明**：ADR-004 已由**第十七篇 §10.3**（前端数据层选型，状态：待评审）预定，故本文占用 ADR-005，不对 ADR-004 作改写。

## 背景

第十三篇要求平台具备 RDF 语义图存储能力（本体构件三元组、事实三元组、受控语义查询），并明确"本体运行时可替换"。此前唯一实现是自研的 PostgreSQL 三元组引擎（`graph.triples` + PK/POS/OSP 三索引 + 按 `tenant_id` 声明式分区），其接口 `GraphStore` 直接以 `pgx.Tx` 作为方法参数，导致：

1. **接口被 PostgreSQL 绑死**：任何非 PG 后端都无法实现该接口，所谓"可替换"只停留在设计书里。
2. **业务模块可能绕开适配器**：没有静态门禁阻止某个 handler 直接拼 SPARQL 或直接读 `graph.triples`，"所有访问都经过统一适配器"是一条无强制力的口头约定。

同时 `docs/图数据库的选型.md`（§四）给出的初始方案是：RDF 图存储用 **Apache Jena Fuseki**、持久化用 **TDB2**、SHACL 验证用**独立验证服务**，并明确要求 **"Go 与 Fuseki 通信经 GraphStore Adapter 统一访问"**、**"不要让所有 Go 业务模块直接拼接 SPARQL 并访问 Fuseki"**。企业业务数据、权限、任务与验证索引仍留在 PostgreSQL。

本 ADR 记录由此做出的三项决策：后端选型、适配器契约边界、以及"事务不可假定原子"的后果。

## 备选方案

### 方案 A：维持单一 PostgreSQL 三元组引擎

- **做法**：保持 `GraphStore` 以 `pgx.Tx` 为参数，不引入外部图存储。
- **代价**：接口永久绑定 PG 驱动类型；无法接入成熟的 SPARQL 引擎与 RDF 生态（SHACL 验证、推理）；第五索引/大规模三元组下的写放大只能靠分区与索引精简缓解（ADR-003 已把它列为待实测风险）。

### 方案 B：引入 Fuseki/TDB2，并**替换** PostgreSQL 三元组引擎

- **做法**：删掉 PG 三元组实现，全部图数据迁入 Fuseki。
- **代价**：把"已知能跑通且已验证 RLS 隔离"的实现换成"未验证"的新组件，迁移风险直接落在主链路；且 Fuseki 无 RLS、无跨库事务，会把原本由 PG 承担的权限与一致性责任暴露出来。

### 方案 C（**采纳**）：Fuseki/TDB2 与 PG 引擎并存，统一藏在存储无关的 `GraphStore` 契约之后

- **做法**：把 `GraphStore` 接口去 PG 化；PG 引擎与 Fuseki 适配器**平级**，由唯一装配入口按配置选择。
- **代价**：需要维护两套实现与一套跨后端一致性测试；新增一个运行期外部依赖（运维成本、许可与补丁义务）。

## 决策

### 决策 1：RDF 图存储后端 = Apache Jena Fuseki + TDB2，PG 引擎保留为默认

- 新增 `internal/ontology/graphstore/fusekistore.go`：经 **SPARQL 1.1 Protocol**（`{Endpoint}/query`、`{Endpoint}/update`）访问 Fuseki；持久化由 TDB2 承担。
- **默认后端仍是 PostgreSQL**（`GRAPHSTORE_MODE` 未设置 = `postgres`），既有部署行为不变；切换后端不改任何业务代码。
- 装配是**唯一入口**且 **fail-closed**：`graphstore.Open(graphstore.OpenConfig{...})`。`GRAPHSTORE_MODE` 取非法值必须**报错**，不得静默回退（静默回退会让"我切到了 Fuseki"与"其实还在 PG"无法区分）。
- 容器化起步：`compose.yaml` 提供 `fuseki` 服务并置于 `profile: graphstore`，默认 `up` 不启动它（`api` 服务刻意**不**写 `depends_on: fuseki`，避免可选 profile 未启用时 `docker compose up` 直接报错）。
- SHACL 验证**不在 Fuseki 内做**：按选型文档 §三，走**独立验证服务**；Fuseki 只被定位为图基础设施，不做业务执行引擎。

### 决策 2：`GraphStore` 契约必须存储无关，能力差异必须显式声明

- **接口去 PG 化**：`GraphStore` 的方法**不得**出现 `pgx.Tx`、SQL 或 SPARQL 细节。原 PG 专有的"词项资源字典"（`graph.terms` 去重与 ID 分配）拆为**可选接口** `TermDictionary`，调用方用类型断言探测，而不是把它塞进主接口迫使 Fuseki 假装实现。
- **能力边界显式化**：新增 `Capabilities{ResourceDictionary, PersistentGraphID, FactMetadata}`，由 `GraphStore.Capabilities()` 返回。
  - PG 引擎返回全 `true`；Fuseki 适配器返回全 `false`（TDB2 不提供词项 ID 字典；图句柄用 `sha256(tenant, graphIRI)` 稳定推导，不落库）。
  - 原则：**"适配器少存了一维"必须在装配期就可被调用方判定**，不得靠"调用时才发现/静默降级"暴露。
- **`EnsureGraph` 的语义按能力分层**：Fuseki 版只做纯校验并返回稳定图句柄，不创建任何持久元数据（TDB2 无 schema、命名图按需存在）。

### 决策 3：租户隔离靠**物理命名图**，跨命名空间读到结果必须**报错**

- 物理图 IRI 形如 `urn:odap:t:<tenant-uuid>:g:<调用方图IRI>`；`validateGraphIRI` 拒绝空/空白/控制字符以及保留命名空间，因此调用方**无法**构造出别人的物理图名。
- 通配查询用 `GRAPH ?g` + `FILTER(STRSTARTS(STR(?g), "<本租户前缀>"))`；跨租户 `Match`/`Count` 归零。
- 读到**非本租户命名空间**的结果时**必须报错**（`visibleGraphIRI` 返回 `ok=false` → 错误），而**不是**静默丢弃——静默丢弃会把"隔离生效"与"隔离被绕过但恰好没命中"混为一谈。

### 决策 4：跨 Fuseki / PostgreSQL / ERP 的操作**不假定原子事务**

- 不提供"跨图存储与关系库的分布式事务"抽象。适配器不假装有回滚能力：写失败即失败，由上层按 Saga/补偿处理。
- 由此，**验证失败 / 验证不完整 / 引擎异常必须分别处理**（选型文档 §三的要求）：三者语义不同，不得合并为一个"验证未通过"。

### 决策 5：受控词项构造集中在唯一文本层，业务模块禁止直接拼 SPARQL

- 词项与命名图的文本构造只在 `internal/ontology/graphstore/rdf.go` 完成（N-Triples 转义、IRI 转义、语言标签与 datatype 校验、词项归一化）。SPARQL 查询文本由适配器内部按 `Pattern` **按位置**生成，不接受外部拼接。
- **静态门禁**：`tests/unit/arch_test.go` 断言 SPARQL 数据操作关键字、Fuseki 专有标识与 SPARQL 协议媒体类型**不得出现在 `internal/ontology/graphstore/` 之外**，且扫描器自身必须命中非零（`insideHits == 0` 即判定扫描器失效——防止"因为什么都没扫到而假绿"）。
- 门禁同时断言装配确实发生（`cmd/` 下存在 `graphstore.Open` / `graphstore.EnvConfigFromOS`）与就绪探针已重绑（`readinessProbe(pool, graphStore)`）。

### 决策 6：词项表示的两处归一化（引擎差异必须被吸收）

- **空白节点**：RDF 空白节点无寻址标识且 SPARQL 禁止其出现在 `VALUES` 中。适配器把它提升为保留命名空间 `urn:odap:bnode:<percent-encoded>` 的受控 IRI，读回时还原为 `KindBlankNode`。这是**显式取舍**（牺牲"与外部引擎的空节点同一性"换取"可寻址、可往返"）。
- **`xsd:string` 折叠**：RDF 1.1 中简单字面值 ≡ `"x"^^xsd:string`，但不同引擎的 SPARQL JSON 输出不一致。读取与内部记账侧统一把 `xsd:string` 折叠为空 datatype，避免**往返 digest 不一致**。

### 决策 7：验证必须在**真实 SPARQL 引擎**上做

- `internal/ontology/graphstore` 的 HTTP 替身只能验证"适配器发了什么请求、怎么解析响应"，**验证不了生成的 SPARQL 是否合法**。历史上适配器曾生成 4 词项的图模式（语法错误），替身测试全绿而真实引擎直接 `ParseException`。
- 因此新增独立阶段 `tools/ci/12-graphstore.sh` + `api graphstore-check` 子命令（17 项断言：Ping、能力声明自洽、图句柄幂等、零租户 fail-closed、写入含转义字面值与空白节点、重复写入幂等、计数、谓词绑定/全通配/跨命名图通配、空白节点可寻址、跨租户隔离、删除与幂等、清理归零）。
- 端点获取**三级降级**：L1 已配置且可达的 Fuseki → L2 compose 临时拉起 Fuseki → L3 RDFLib 提供真实 SPARQL 1.1 引擎语义的测试端点。L3 就绪判定附带**子进程存活检查**，否则端口上残留的别的进程会让探测"成功"从而把结论归因给一个根本没启动的引擎。
- `tools/fake-sparql/rdflib_endpoint.py` **不是 ODAP 组成部分、不得用于生产**，仅用于无 Fuseki 时提供引擎语义。

## 理由

1. **选型文档已给出结论，本 ADR 只补"接口边界"这一实现级决策**：把 Fuseki 定位为图基础设施、把治理（版本、权限、流程、验证任务、证据）留在 ODAP 自己手里，是第十三篇"本体运行时可替换"的落地方式。
2. **并存优于替换**：PG 引擎经过 RLS 隔离与集成测试验证，直接替换会把未验证组件放进主链路。并存让"换后端"变成一次配置变更，而不是一次数据迁移。
3. **能力显式化优于接口膨胀**：把 PG 专有能力放进可选接口 + `Capabilities`，比"让 Fuseki 也实现一个空的词项字典"更诚实——后者会让调用方以为拿到了 ID，实际是静默错的。
4. **隔离必须可证伪**：跨租户结果"报错"而不是"过滤为空"，使隔离断言具备可证伪性；否则测试全绿也可能是隔离根本没生效。
5. **承认没有分布式事务**：跨 Fuseki/PG/ERP 的原子性本来就不存在，写进 ADR 是为了防止后续实现者"补一个看起来能回滚的抽象"从而引入更隐蔽的不一致。

## 后果

- **正面**：后端可替换从设计落实为代码与门禁；业务模块无法绕过适配器；租户隔离、空白节点往返、引擎错误回显都有可运行的证据；CI 具备在无 Fuseki 环境下仍验证"SPARQL 合法性"的能力。
- **负面 / 代价**：
  - 新增一个运行期外部依赖（Fuseki/TDB2），带来版本升级、安全补丁、备份恢复与高可用规划的持续义务；其许可与数据流须按第十六篇 §10.3 的私有化要求核验。
  - 两套后端 + 一套跨后端一致性测试的维护成本。
  - 空白节点提升是**不可逆的语义取舍**：通过 ODAP 往返的空节点与外部的空节点不再具备同一性；若将来需要共享空节点图，须重新评估。
  - Fuseki 无跨库事务：跨图与关系库的操作必须由上层补偿逻辑兜底，实现复杂度从存储层上移到业务层。
- **约束**：
  - 任何新业务模块**不得**直接构造 SPARQL 或直连 Fuseki，必须经 `graphstore.GraphStore`；该约束由架构门禁强制，非文档约定。
  - 就绪探针必须反映图存储真实状态（`readinessProbe(pool, graphStore)`）：把未知状态报告为成功属 §10.3 的阻断级缺陷。
  - 图存储写入与 RLS 的租户上下文注入仍以 `internal/platform/pgxutil.TenantTx` 为**唯一**路径（PG 后端）。

## 何时应重新评估

1. 选型文档 §四要求的基准测试（真实本体与业务数据的 SHACL-SPARQL 支持、复杂关系查询性能、增量验证、并发读写、备份恢复、租户隔离）不达标。
2. TDB2 在目标规模下的并发写与持久化行为不满足要求，或需要超出 RDFS+SHACL 的推理能力。
3. 出现"必须跨图存储与 PostgreSQL 原子提交"的真实业务需求（当前明确不支持）。
4. PG 五索引在 1 亿三元组目标下写放大无法通过分区与索引精简缓解（ADR-003 已登记同一触发条件）。

## 关联文档

- `docs/图数据库的选型.md`（§一 Fuseki 职责边界、§二 SHACL 不做在图内、§三 验证链路、§四 最终选型建议）
- ADR-003《自研与开源选型评估》领域四（RDF 存储与推理的选型约束与触发条件）
- 第十三篇（本体与语义图）、第二篇（PostgreSQL Schema，`graph.triples` 与索引）、第十六篇 §10.1/§10.3（流水线阶段与发布硬门禁）
- 实现：`internal/ontology/graphstore/{store.go,memstore.go,rdf.go,fusekistore.go,factory.go}`、`cmd/api/graphcheck.go`、`cmd/api/main.go`（装配与就绪探针）
- 门禁与验证：`tests/unit/arch_test.go`（唯一入口 + 装配可达性 + 探针重绑）、`tools/ci/12-graphstore.sh`（L1/L2/L3 降级）、`tools/ci/graphstore-check.sh`、`tools/fake-sparql/rdflib_endpoint.py`（非生产）
- 部署：`compose.yaml`（`profile: graphstore` 的 `fuseki` 服务）、`.env.example`（`GRAPHSTORE_MODE` / `FUSEKI_*`）
