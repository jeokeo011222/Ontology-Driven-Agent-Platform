# 企业本体：RDF / OWL / SHACL 抽屉式查看

## Context（为什么做这件事）

「企业本体 → 本体列表」目前只能点「打开工作台」跳到详情页，而详情页展示的是
结构化清单（类与关系、实例、发现项），**看不到本体真正落地到图里/校验时的三份
产物文本**：RDF 三元组、OWL 公理、SHACL 形状。使用者无法核对"这份本体到底表达了
什么"，也无法把产物导出给外部工具（Jena/RDF4J/pyshacl）复核。

本需求：在本体列表的**行内直接**打开一个右侧抽屉，看到该本体最新版本的
RDF、OWL、SHACL 三份文本。已确认的三项口径（不再变更）：

1. **入口**：本体列表行内按钮开抽屉，保留原有「打开工作台」入口；
2. **内容来源**：服务端由 `manifest` **派生**（复用 `graphload`），**随时可见、无需先校验**；
3. **序列化格式**：RDF → Turtle，SHACL → Turtle，OWL → RDF/XML。

硬约束（沿用项目既有约定）：严禁静默降级 / 补零 / 伪造数据；缺失或无效数据直接报错；
不留死代码；中文注释解释"为什么"；不做超出要求的改动。

---

## 设计要点

### 为什么派生而不是回读图存储

`manifest` 是唯一真值来源，`graphload` 已经把它的三种投影实现得很完整
（`TBox` / `ABox` / `BuildShapes`）。若改从图存储回读，就要求"必须先跑过校验"，
且会引入第二份真值（图里的数据可能被别的路径写过）。派生保证三份文本与
校验时实际装载/求值的内容**逐条一致**，且对 DRAFT 版本也可见。

### 为什么不能在上层重复实现词项转义

`graphstore/rdf.go` 是本仓**唯一**的词项/命名图文本构造层，`termText` / `escapeIRI` /
`escapeLiteral` 全是私有，且文件头明确约定"词项与 SPARQL 文本只允许在本包内构造"。
上层要用词项文本，只能走本包新增的导出薄封装，**绝不重复实现转义**——
重复实现迟早漂移，而漂移方向是"写出的 RDF 与图里存的词项不是同一个"。

---

## 实施步骤

### 1. `internal/ontology/graphstore/rdf.go`（改）

新增导出函数：

```go
// TermNTriples 返回词项的规范 N-Triples 文本。
//
// 为什么要导出：上层（graphload 的序列化、artifact 端点）需要把 []
// 渲染成人可读的 RDF 文本，而转义规则必须只有一份实现——若在别的包
// 里再写一遍 escapeIRI/escapeLiteral，两份实现迟早漂移，漂移的后果是
// "界面上看到的 RDF"与"图里实际存的词项"不是同一个东西，且不报错。
func TermNTriples(t Term) (string, error) { return termText(t) }
```

### 2. `internal/ontology/graphload/serialize.go`（新）

三个方法一律返回 `(string, error)`，出错即返回错（不产出半份文本）。

- **前缀表**：默认前缀 `:` = `ns.Base()`，加 `rdf` / `rdfs` / `xsd` / `owl` / `sh`。
- `shortenIRI(iri string, ns Namespace) string`：命中已知前缀且局部名是 Turtle
  `PN_LOCAL` 安全形态（首字符为字母/数字/`_`，末字符非 `.`，全体 ∈ `[A-Za-z0-9._-]`）
  才缩成 `p:local`；否则返回空串，由调用方回落 `<...>`。
  **为什么带安全判定**：`isLocalName` 允许 `-foo` / `foo.` 这类首尾字符，它们在
  Turtle 里**不是**合法 `PN_LOCAL`，直接缩写会产出一份解析器拒绝的文件——这属于"看起来
  正常、实际不可用"的静默缺陷。
- `writeTurtleTerm(b, ns, t)`：IRI → 可缩则 `p:local`，否则用 `graphstore.TermNTriples`
  的 `<...>` 原文；字面值 / 空白节点 → 直接用 `TermNTriples` 原文（**不**对
  datatype 做二次缩写：没有理由在一个已按规范转义的词项文本上做字符串改写）。
- `(m *Manifest) RDFTurtle(ns Namespace) (string, error)`：`@prefix` 头 + `TBox` 全量
  再 `ABox` 全量，每条三元组一行 `S P O .`。
- `(m *Manifest) SHACLTurtle(ns Namespace) (string, error)`：调 `BuildShapes(ns)`，
  逐形状输出 `rdf:type sh:NodeShape`、`sh:targetClass`（每个目标类一条）、
  `sh:targetNode`、`sh:severity`、`sh:property <shapeIRI>.pN`；再逐属性形状输出
  `sh:path` / `minCount` / `maxCount` / `datatype` / `class` / `nodeKind` /
  `in`（Turtle 集合）/ `min|maxInclusive` / `min|maxExclusive` / `pattern` /
  `min|maxLength` / `hasValue` / `severity`。
  **取值一律来自 `PropertyShape` 的结构体字段，而不是 `Keywords` 列表**：字段是
  "真正会被求值的约束"，`Keywords` 只是 fail-closed 判据；从这里取才能保证
  抽屉里看到的约束 = 校验时执行的约束。
  `hasValue` 当前清单形态（`PropertySpec`）无法设置，仍如实序列化——序列化器
  丢字段是"撒谎"，代价是 3 行。
- `(m *Manifest) OWLRDFXML(ns Namespace) (string, error)`：XML 声明 + `xmlns:rdf/rdfs/owl/xsd`
  + 本体命名空间；`<owl:Ontology rdf:about=graphIRI>` 断言本体 IRI；逐类
  `<owl:Class>`（`rdfs:label` / `rdfs:comment`）；逐关系 `<owl:ObjectProperty>`
  （`rdfs:domain` / `rdfs:range`）；逐属性 `<owl:DatatypeProperty>`
  （`rdfs:domain` = 所属类，`rdfs:range` = 声明的 XSD datatype）。
  自带 `xmlTextEscape` / `xmlAttrEscape`（**XML 文本与属性的转义规则不同**，
  属性值里的 `"` 必须转义，文本里则无需）。

> 注意：本文件在 `graphstore` 包外构造 RDF 文本，但**不触碰**门禁
> (`tests/unit/arch_test.go`) 禁止的那批 token（Fuseki 标识 / `INSERT DATA` /
> `DELETE DATA` / `STRSTARTS` / sparql 媒体类型），且所有词项转义都经
> `TermNTriples` 唯一入口。

### 3. 端点 `GET /api/v1/ontologies/{id}/artifacts`

`internal/api/handlers/ontologies.go` 追加 `GetOntologyArtifacts`：

```
identity → store 非空 → pathID
→ Store.OntologyVersionSnapshot(id)        // 与 validate 同一份读取（manifest 已带 ::text）
→ graphload.NewNamespace(ontology_key)
→ graphload.ParseManifest(manifest)        // 结构/闭包非法 → 照原样 422，不出半份产物
→ graphload.GraphIRI(key, version_id)
→ RDFTurtle / SHACLTurtle / OWLRDFXML
→ writeJSON
```

响应体：

```json
{
  "ontology_key": "…", "version_id": "…", "version": "…",
  "graph_iri": "https://odap.example/ont/<key>/<version_id>",
  "rdf":   { "format": "text/turtle",           "content": "…" },
  "owl":   { "format": "application/rdf+xml",   "content": "…" },
  "shacl": { "format": "text/turtle",           "content": "…" },
  "counts": { "tbox_triples": 0, "abox_triples": 0, "shapes": 0,
              "target_classes": 0, "instances": 0 }
}
```

关键取舍：

- **不需要 `GraphStore`**（与 `validate` 不同）：本端点从 manifest 派生，图存储未装配
  时也能看。
- **不因为 `shapes` 为空而报错**（与 `validate` 不同）：没有形状时 SHACL 页显示
  "该版本未声明任何 sh:shapes" 的空态提示，**不伪造**内容。
- `counts` 全部取自 manifest（`len(TBox)` / `len(ABox)` / `len(Shapes)` /
  `len(TargetClassIRIs)` / `InstanceCount()`）。**不**把 `focus_nodes` 放进响应——
  它是"图里实际有多少实例"的运行时属性，图未装载时会得到 0，而那个 0 会被误读为
  "没有实例"。不给不出现在此处的数字，比给一个环境相关的数字更诚实。

契约三处同步（硬约束）：

- `internal/api/router.go` 的 `routes()` 追加该路由；
- `internal/api/router_test.go` 的 `contractEndpoints` 追加
  `"GET /api/v1/ontologies/{id}/artifacts"`；
- `contracts/openapi.yaml` 追加 `paths` 条目 + `OntologyArtifacts` schema
  （`required` 明确列出 `rdf/owl/shacl/counts`）。

### 4. 前端

| 文件 | 改动 |
| --- | --- |
| `web/src/components/ui/Drawer.tsx`（新） | 右侧抽屉：遮罩点击/ESC 关闭、焦点移入面板、`onClose` 走 ref（照抄 [Dialog.tsx](file:///d:/Code-Ontology-Agent/web/src/components/ui/Dialog.tsx) 的既有理由：每次渲染新建引用会让输入被抢焦点）、内容区独立滚动、页脚固定 |
| `web/src/components/ui/index.ts` | 导出 `Drawer` |
| `web/src/styles/globals.css` | 追加 `.drawer-backdrop` / `.drawer` / `.drawer__header|body|footer`，沿用既有 `.dialog*` 的纵向 flex 约定（内容区唯一滚动、页脚固定，避免长内容把按钮顶出视口） |
| `web/src/types/domain.ts` | 追加 `OntologyArtifacts`（含 `rdf/owl/shacl/counts`） |
| `web/src/features/ontology/api.ts` | 追加 `useOntologyArtifacts(id: string \| undefined, enabled: boolean)`（`enabled` 由抽屉开关驱动，未打开不发请求） |
| `web/src/features/ontology/pages/OntologyArtifactsDrawer.tsx`（新） | 三页签 RDF / OWL / SHACL（顶级 `Tabs`，复用 `components/ui`），每页 `pre` 只读文本 + 「复制」按钮；页脚显示 counts；加载/错误态如实展示（错误原文透出，不兜底） |
| `web/src/features/ontology/pages/OntologyListPage.tsx` | 行内「查看 RDF / OWL / SHACL」按钮（`Button size="sm" variant="ghost"`），与「打开工作台」并列；点击后 `setArtifactOntology(item)` 开抽屉 |

### 5. 测试与验证

新增 `internal/ontology/graphload/serialize_test.go`（表驱动、只依赖已有公开 API）：

- 三份文本均非空，且以预期头部开始（`@prefix` / `<?xml`）；
- `RDFTurtle` 行数 = `len(TBox)+len(ABox)`（每条一行）；
- 每个 shape 与其每条属性约束都在 `SHACLTurtle` 里出现（`sh:minCount` 等逐个断言）；
- 转义：标签含 `"` / `<` / `&` / 换行时，Turtle 与 XML 各自的转义都正确；
- 非法局部名（如 `-bad` / `bad.`）走 `<...>` 回落而不是被缩成非法 `PN_LOCAL`；
- `OWLRDFXML` 能被 `encoding/xml` 成功解析（结构合法性）且含 `owl:DatatypeProperty`
  与 `rdfs:range`。

验证命令与实测：

- `D:\go\bin\go.exe build ./...`、`go vet ./internal/...`、`go test ./internal/ontology/... ./tests/unit/...`
- `D:\go\bin\go.exe run ./tools/contract-lint -root .`
- `web`：`npx tsc --noEmit`
- `docker compose up -d --build api`（Go 改动后必须重建；migrate 随 api 启动自动跑）
- PowerShell 实测：`demo.admin` 登录拿 `session_token`，`GET /api/v1/ontologies/{id}/artifacts`
  断言 `rdf/owl/shacl` 三份 `content` 非空、`counts` 与列表数量吻合；再用一个**没有 shapes**
  的本体验证 SHACL 页为空态（HTTP 仍 200）与一个 **manifest 非法**的本体验证 422。
- 浏览器回归：企业本体列表行内开抽屉，三页签可切换、复制可用、ESC 与遮罩可关、无红色横幅。

---

## 收尾要报告的一件事（本需求范围外，先不改）

排查中发现一个**既有缺陷**：`GET /api/v1/ontologies/{id}` 实测**不返回 `classes`**，
而 [OntologyDetailPage.tsx](file:///d:/Code-Ontology-Agent/web/src/features/ontology/pages/OntologyDetailPage.tsx#L412)
直接读 `ontology.classes.length` → 真实数据下抛 `TypeError`，"类与关系"页签是坏的。
本需求不触碰详情页，完成后向用户报告并询问是否一并修（不擅自扩大改动范围）。
