package graphload

import (
	"context"
	"strings"
	"testing"

	"odap/internal/ontology/graphstore"
	"odap/internal/ontology/shacl"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// ---------------------------------------------------------------------------
// 测试语料：合同场景（与生成端 ontologyManifestSchema 同构）
// ---------------------------------------------------------------------------

const testOntologyKey = "contract-demo"

const conformingManifest = `{
  "summary": "合同管理本体（样例）",
  "classes": [
    {"name": "Contract", "label": "合同", "description": "一份合同", "attributes": [
      {"name": "contract_no", "type": "string", "description": "合同编号", "required": true},
      {"name": "amount", "type": "number", "description": "合同金额", "required": true}
    ]},
    {"name": "Party", "label": "合同相对方", "description": "签约主体", "attributes": [
      {"name": "party_name", "type": "string", "description": "主体名称", "required": true}
    ]}
  ],
  "relations": [
    {"name": "signed_by", "from_class": "Contract", "to_class": "Party",
     "cardinality": "many-to-one", "description": "合同由某主体签署"}
  ],
  "enumerations": [
    {"name": "ContractStatus", "values": ["草稿", "生效", "终止"]}
  ],
  "shapes": [
    {"id": "ContractShape", "target_class": "Contract", "severity": "Violation", "properties": [
      {"path": "contract_no", "min_count": 1, "max_count": 1, "datatype": "string", "pattern": "^HT-[0-9]{4}$"},
      {"path": "amount", "min_count": 1, "datatype": "decimal", "min_inclusive": 0, "max_inclusive": 1000000},
      {"path": "signed_by", "min_count": 1, "class": "Party", "node_kind": "IRI"}
    ]}
  ],
  "instances": [
    {"class": "Party", "id": "P-01", "properties": {"party_name": "甲方有限公司"}},
    {"class": "Contract", "id": "C-0001",
     "properties": {"contract_no": "HT-0001", "amount": 100000, "signed_by": "P-01"}}
  ]
}`

// violatingManifest 与合规版同构，只把实例换成一条**违反形状约束但结构完全合法**的合同：
// 编号不匹配 sh:pattern、金额超 sh:maxInclusive、且缺少必填关系 signed_by。
//
// 关键：这份清单能通过 ParseManifest（结构与闭包都合法），只会被 SHACL 拦下。
// 这正是"平台管结构、SHACL 管取值"的职责划分在测试里的体现。
const violatingManifest = `{
  "summary": "合同管理本体（样例）",
  "classes": [
    {"name": "Contract", "label": "合同", "description": "一份合同", "attributes": [
      {"name": "contract_no", "type": "string", "description": "合同编号", "required": true},
      {"name": "amount", "type": "number", "description": "合同金额", "required": true}
    ]},
    {"name": "Party", "label": "合同相对方", "description": "签约主体", "attributes": [
      {"name": "party_name", "type": "string", "description": "主体名称", "required": true}
    ]}
  ],
  "relations": [
    {"name": "signed_by", "from_class": "Contract", "to_class": "Party",
     "cardinality": "many-to-one", "description": "合同由某主体签署"}
  ],
  "enumerations": [],
  "shapes": [
    {"id": "ContractShape", "target_class": "Contract", "severity": "Violation", "properties": [
      {"path": "contract_no", "min_count": 1, "max_count": 1, "datatype": "string", "pattern": "^HT-[0-9]{4}$"},
      {"path": "amount", "min_count": 1, "datatype": "decimal", "min_inclusive": 0, "max_inclusive": 1000000},
      {"path": "signed_by", "min_count": 1, "class": "Party", "node_kind": "IRI"}
    ]}
  ],
  "instances": [
    {"class": "Contract", "id": "C-0002", "properties": {"contract_no": "BAD", "amount": 9999999}}
  ]
}`

func mustNamespace(t *testing.T) Namespace {
	t.Helper()
	ns, err := NewNamespace(testOntologyKey)
	if err != nil {
		t.Fatalf("NewNamespace 失败: %v", err)
	}
	return ns
}

func mustParse(t *testing.T, raw string) *Manifest {
	t.Helper()
	m, err := ParseManifest([]byte(raw))
	if err != nil {
		t.Fatalf("ParseManifest 失败: %v", err)
	}
	return m
}

// pointerOf 取出错误的 JSON Pointer（闭包校验必须给出可定位位置）。
func pointerOf(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("期望报错，实际为 nil")
	}
	ae := errors.As(err)
	if ae == nil {
		t.Fatalf("期望 *errors.AppError，实际 %T: %v", err, err)
	}
	p, _ := ae.Details["pointer"].(string)
	return p
}

// ---------------------------------------------------------------------------
// 解析与闭包校验
// ---------------------------------------------------------------------------

func TestParseManifest_OK(t *testing.T) {
	m := mustParse(t, conformingManifest)
	if len(m.Classes) != 2 || len(m.Relations) != 1 || len(m.Shapes) != 1 {
		t.Fatalf("解析结果与语料不符: classes=%d relations=%d shapes=%d",
			len(m.Classes), len(m.Relations), len(m.Shapes))
	}
	if got := m.InstanceCount(); got != 2 {
		t.Fatalf("实例数期望 2，实际 %d", got)
	}
	// 前向引用：C-0001 引用 P-01，而 P-01 在数组中排在前面；
	// 反向排列同样必须成立（索引先建全、再校验）。
	reordered := strings.Replace(conformingManifest,
		`{"class": "Party", "id": "P-01", "properties": {"party_name": "甲方有限公司"}},
    {"class": "Contract", "id": "C-0001",
     "properties": {"contract_no": "HT-0001", "amount": 100000, "signed_by": "P-01"}}`,
		`{"class": "Contract", "id": "C-0001",
     "properties": {"contract_no": "HT-0001", "amount": 100000, "signed_by": "P-01"}},
    {"class": "Party", "id": "P-01", "properties": {"party_name": "甲方有限公司"}}`, 1)
	if reordered == conformingManifest {
		t.Fatal("测试自身失效：语料重排替换未生效")
	}
	if _, err := ParseManifest([]byte(reordered)); err != nil {
		t.Fatalf("实例顺序不应影响闭包校验，实际失败: %v", err)
	}
}

func TestParseManifest_ClosureFailures(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(string) string
		wantPtr  string // 期望 JSON Pointer（子串匹配）
		wantWord string // 期望错误信息包含的关键词
	}{
		{
			name: "未知字段被拒绝",
			mutate: func(s string) string {
				return strings.Replace(s, `"summary": "`, `"unknown_field": 1, "summary": "`, 1)
			},
			wantPtr:  "",
			wantWord: "结构与约定不符",
		},
		{
			name:     "类名为空",
			mutate:   func(s string) string { return strings.Replace(s, `"name": "Contract"`, `"name": "  "`, 1) },
			wantPtr:  "/classes/0/name",
			wantWord: "类名为空",
		},
		{
			name:     "类名非 ASCII 局部名",
			mutate:   func(s string) string { return strings.Replace(s, `"name": "Contract"`, `"name": "合同"`, 1) },
			wantPtr:  "/classes/0/name",
			wantWord: "不是合法的词项局部名",
		},
		{
			name:     "属性类型不在枚举内",
			mutate:   func(s string) string { return strings.Replace(s, `"type": "number"`, `"type": "money"`, 1) },
			wantPtr:  "/classes/0/attributes/1/type",
			wantWord: "不在契约枚举内",
		},
		{
			name:     "关系 to_class 未定义",
			mutate:   func(s string) string { return strings.Replace(s, `"to_class": "Party"`, `"to_class": "Vendor"`, 1) },
			wantPtr:  "/relations/0/to_class",
			wantWord: "to_class 未在 classes 中定义",
		},
		{
			name: "关系名重复（同一谓词两组 domain/range）",
			mutate: func(s string) string {
				return strings.Replace(s,
					`"cardinality": "many-to-one", "description": "合同由某主体签署"}
  ],`,
					`"cardinality": "many-to-one", "description": "合同由某主体签署"},
    {"name": "signed_by", "from_class": "Party", "to_class": "Contract", "description": "重复声明"}
  ],`, 1)
			},
			wantPtr:  "/relations/1/name",
			wantWord: "关系名重复",
		},
		{
			name: "属性名与关系名冲突",
			mutate: func(s string) string {
				return strings.Replace(s, `"name": "party_name", "type": "string"`, `"name": "signed_by", "type": "string"`, 1)
			},
			wantPtr:  "/classes/1/attributes/signed_by",
			wantWord: "与同名关系冲突",
		},
		{
			name: "形状 target_class 未定义",
			mutate: func(s string) string {
				return strings.Replace(s, `"target_class": "Contract"`, `"target_class": "Invoice"`, 1)
			},
			wantPtr:  "/shapes/0/target_class",
			wantWord: "target_class 未在 classes 中定义",
		},
		{
			name: "形状路径既非关系也非该类属性",
			mutate: func(s string) string {
				return strings.Replace(s, `{"path": "amount", "min_count": 1, "datatype": "decimal"`,
					`{"path": "party_name", "min_count": 1, "datatype": "string"`, 1)
			},
			wantPtr:  "/shapes/0/properties/1/path",
			wantWord: "既不是已声明的关系，也不是类 Contract 的属性",
		},
		{
			name: "形状属性为空（空形状等于声称校验过）",
			mutate: func(s string) string {
				start := strings.Index(s, `"properties": [`)
				end := strings.Index(s[start:], `]}`)
				return s[:start] + `"properties": []` + s[start+end+1:]
			},
			wantPtr:  "/shapes/0/properties",
			wantWord: "properties 不能为空",
		},
		{
			name:     "sh:datatype 不在枚举内",
			mutate:   func(s string) string { return strings.Replace(s, `"datatype": "decimal"`, `"datatype": "money"`, 1) },
			wantPtr:  "/shapes/0/properties/1/datatype",
			wantWord: "不在契约枚举内",
		},
		{
			name:     "sh:class 引用的类未定义",
			mutate:   func(s string) string { return strings.Replace(s, `"class": "Party"`, `"class": "Vendor"`, 1) },
			wantPtr:  "/shapes/0/properties/2/class",
			wantWord: "sh:class 引用的类未在 classes 中定义",
		},
		{
			name:     "sh:nodeKind 非法",
			mutate:   func(s string) string { return strings.Replace(s, `"node_kind": "IRI"`, `"node_kind": "Any"`, 1) },
			wantPtr:  "/shapes/0/properties/2/node_kind",
			wantWord: "不在 IRI/BlankNode/Literal 之内",
		},
		{
			name: "绝对 IRI 谓词被拒绝",
			mutate: func(s string) string {
				return strings.Replace(s, `{"path": "amount"`, `{"path": "https://evil.example/amount"`, 1)
			},
			wantPtr:  "/shapes/0/properties/1/path",
			wantWord: "不接受绝对 IRI 谓词",
		},
		{
			name: "实例 class 未定义",
			mutate: func(s string) string {
				return strings.Replace(s, `"class": "Party", "id": "P-01"`, `"class": "Vendor", "id": "P-01"`, 1)
			},
			wantPtr:  "/instances/0/class",
			wantWord: "class 未在 classes 中定义",
		},
		{
			name:     "实例 id 重复",
			mutate:   func(s string) string { return strings.Replace(s, `"id": "C-0001"`, `"id": "P-01"`, 1) },
			wantPtr:  "/instances/1/id",
			wantWord: "实例 id 重复",
		},
		{
			name:     "关系指向不存在的实例（闭包断链）",
			mutate:   func(s string) string { return strings.Replace(s, `"signed_by": "P-01"`, `"signed_by": "P-99"`, 1) },
			wantPtr:  "/instances/1/properties/signed_by",
			wantWord: "指向的实例不存在：P-99",
		},
		{
			name:     "关系指向的实例类不匹配",
			mutate:   func(s string) string { return strings.Replace(s, `"signed_by": "P-01"`, `"signed_by": "C-0001"`, 1) },
			wantPtr:  "/instances/1/properties/signed_by",
			wantWord: "to_class 是 Party，但实例 C-0001 属于 Contract",
		},
		{
			// 关系方向写反：把 signed_by 的 from_class 改成 Party 后，
			// 合同实例 C-0001 仍在使用它 —— 必须在 from 端（主体归属）被拒绝，
			// 而不是等到 to 端给出一条指向客体实例的困惑报错。
			name: "关系从错误的类出发（方向写反）",
			mutate: func(s string) string {
				return strings.Replace(s, `"from_class": "Contract", "to_class": "Party"`, `"from_class": "Party", "to_class": "Party"`, 1)
			},
			wantPtr:  "/instances/1/properties/signed_by",
			wantWord: "只能从类 Party 出发",
		},
		{
			name: "属性取值与声明类型不符",
			mutate: func(s string) string {
				return strings.Replace(s, `"amount": 100000`, `"amount": "十万"`, 1)
			},
			wantPtr:  "/instances/1/properties/amount",
			wantWord: "不能解析为数字",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.mutate(conformingManifest)
			if raw == conformingManifest && tc.name != "未知字段被拒绝" {
				t.Fatalf("测试自身失效：语料替换未生效")
			}
			_, err := ParseManifest([]byte(raw))
			if err == nil {
				t.Fatal("期望 ParseManifest 报错，实际通过（闭包校验存在漏洞）")
			}
			if !strings.Contains(err.Error(), tc.wantWord) {
				t.Fatalf("错误信息缺少关键词 %q\n实际: %v", tc.wantWord, err)
			}
			if tc.wantPtr != "" {
				if got := pointerOf(t, err); got != tc.wantPtr {
					t.Fatalf("JSON Pointer 期望 %q，实际 %q", tc.wantPtr, got)
				}
			}
			// 一律是 422 级别的错误码，前端才能把问题定位到字段。
			if ae := errors.As(err); ae != nil && ae.HTTPStatus() != 422 {
				t.Fatalf("期望 422，实际 %d", ae.HTTPStatus())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 命名空间与图 IRI
// ---------------------------------------------------------------------------

func TestNamespaceAndGraphIRI(t *testing.T) {
	ns := mustNamespace(t)
	if got := ns.ClassIRI("Contract"); got != "https://odap.example/ont/contract-demo#Contract" {
		t.Fatalf("ClassIRI 不符: %s", got)
	}
	if got := ns.AttributeIRI("Contract", "amount"); got != "https://odap.example/ont/contract-demo#Contract.amount" {
		t.Fatalf("属性谓词必须按类限定: %s", got)
	}
	if got := ns.InstanceIRI("C-0001"); got != "https://odap.example/ont/contract-demo#inst/C-0001" {
		t.Fatalf("InstanceIRI 不符: %s", got)
	}

	if _, err := NewNamespace("合同本体"); err == nil {
		t.Fatal("非 ASCII 的 ontology_key 必须被拒绝（否则词项 IRI 需要转义，会出现两份等价但不相交的数据）")
	}
	if _, err := NewNamespace(""); err == nil {
		t.Fatal("空 ontology_key 必须被拒绝")
	}

	g1, err := GraphIRI(testOntologyKey, "ver-1")
	if err != nil {
		t.Fatalf("GraphIRI 失败: %v", err)
	}
	g2, _ := GraphIRI(testOntologyKey, "ver-1")
	if g1 != g2 {
		t.Fatalf("图 IRI 必须确定性派生: %s vs %s", g1, g2)
	}
	if strings.Contains(g1, "#") {
		t.Fatalf("图 IRI 不得含裸 '#'（片段标识会被截断）: %s", g1)
	}
	// urn:odap: 是 GraphStore 适配器的保留命名空间；用它做图 IRI 会被适配器拒绝，
	// 且其中的空白节点前缀会被还原成 KindBlankNode。此处直接对着真实适配器验证。
	if strings.HasPrefix(g1, "urn:odap:") {
		t.Fatalf("图 IRI 不得使用保留命名空间: %s", g1)
	}
	sm := graphstore.NewMemStore()
	if _, err := sm.InsertTriples(context.Background(), ids.New(), g1, []graphstore.Triple{{
		S: graphstore.Term{Kind: graphstore.KindIRI, Lexical: ns.ClassIRI("Contract")},
		P: graphstore.Term{Kind: graphstore.KindIRI, Lexical: iriRDFType},
		O: graphstore.Term{Kind: graphstore.KindIRI, Lexical: iriRDFSClass},
	}}); err != nil {
		t.Fatalf("派生的图 IRI 必须被真实适配器接受，实际: %v", err)
	}
}

func TestXsdIRI(t *testing.T) {
	ok := map[string]string{
		"string":   xsdNamespace + "string",
		"number":   xsdNamespace + "decimal",
		"integer":  xsdNamespace + "integer",
		"INTEGER":  xsdNamespace + "integer",
		"boolean":  xsdNamespace + "boolean",
		"date":     xsdNamespace + "date",
		"datetime": xsdNamespace + "dateTime",
		// 契约把 enum 列为合法的属性类型（见 ontologyManifestSchema 与错误提示），
		// 枚举取值在 RDF 里仍是字符串字面量，因此映射到 xsd:string。
		"enum":                        xsdNamespace + "string",
		"http://example.org/custom#T": "http://example.org/custom#T",
	}
	for in, want := range ok {
		got, valid := xsdIRI(in)
		if !valid || got != want {
			t.Fatalf("xsdIRI(%q) = (%q,%v)，期望 (%q,true)", in, got, valid, want)
		}
	}
	for _, bad := range []string{"", "  ", "money", "xsd:string"} {
		if _, valid := xsdIRI(bad); valid {
			t.Fatalf("xsdIRI(%q) 必须被拒绝（fail-closed，不猜测意图）", bad)
		}
	}
}

// ---------------------------------------------------------------------------
// 投影：三元组数量与形状关键字
// ---------------------------------------------------------------------------

func TestProjections_TripleCounts(t *testing.T) {
	m := mustParse(t, conformingManifest)
	ns := mustNamespace(t)

	tbox, err := m.TBox(ns)
	if err != nil {
		t.Fatalf("TBox 失败: %v", err)
	}
	// Contract: rdf:type + rdfs:label + 2 属性×3 = 8
	// Party   : rdf:type + rdfs:label + 1 属性×3 = 5
	// signed_by: rdf:type + rdfs:domain + rdfs:range = 3
	if len(tbox) != 16 {
		t.Fatalf("TBox 三元组期望 16，实际 %d", len(tbox))
	}

	abox, err := m.ABox(ns)
	if err != nil {
		t.Fatalf("ABox 失败: %v", err)
	}
	// P-01: rdf:type + party_name = 2
	// C-0001: rdf:type + contract_no + amount + signed_by = 4
	if len(abox) != 6 {
		t.Fatalf("ABox 三元组期望 6，实际 %d", len(abox))
	}

	// 属性谓词必须按类限定；关系谓词必须全局。
	var attrPredicate, relPredicate bool
	for _, tr := range tbox {
		if tr.S.Lexical == ns.AttributeIRI("Contract", "amount") {
			attrPredicate = true
		}
		if tr.S.Lexical == ns.RelationIRI("signed_by") {
			relPredicate = true
		}
	}
	if !attrPredicate || !relPredicate {
		t.Fatalf("TBox 缺少预期的属性/关系谓词声明: attr=%v rel=%v", attrPredicate, relPredicate)
	}
}

func TestBuildShapes_KeywordsAreHonestAndFailClosed(t *testing.T) {
	m := mustParse(t, conformingManifest)
	ns := mustNamespace(t)
	shapes, err := m.BuildShapes(ns)
	if err != nil {
		t.Fatalf("BuildShapes 失败: %v", err)
	}
	if len(shapes) != 1 {
		t.Fatalf("形状数期望 1，实际 %d", len(shapes))
	}
	sh := shapes[0]
	if sh.ID != ns.ShapeIRI("ContractShape") {
		t.Fatalf("形状 IRI 不符: %s", sh.ID)
	}
	if len(sh.TargetClass) != 1 || sh.TargetClass[0] != ns.ClassIRI("Contract") {
		t.Fatalf("targetClass 不符: %v", sh.TargetClass)
	}
	if sh.Properties[0].Path != ns.AttributeIRI("Contract", "contract_no") {
		t.Fatalf("属性形状路径未解析为按类限定的谓词: %s", sh.Properties[0].Path)
	}
	if sh.Properties[2].Path != ns.RelationIRI("signed_by") {
		t.Fatalf("关系路径未解析为全局谓词: %s", sh.Properties[2].Path)
	}
	// Keywords 必须如实反映"实际设置了值的关键字"：多列→误拒，漏列→门禁形同虚设。
	if err := shacl.ValidateShapeKeywords(sh); err != nil {
		t.Fatalf("投影出的关键字必须全部被 shacl 接受，实际: %v", err)
	}

	// 生成路径与校验路径共用同一份白名单：sh:sparql 必须被拒绝。
	bad := sh
	bad.Keywords = append(append([]string{}, sh.Keywords...), "sparql")
	if err := shacl.ValidateShapeKeywords(bad); err == nil {
		t.Fatal("含 sh:sparql 的形状必须被拒绝（第十三篇 §15.1 显式排除）")
	}
}

// ---------------------------------------------------------------------------
// 端到端：装载 → SHACL 校验（不依赖 PostgreSQL / Fuseki，用 MemStore 真跑）
// ---------------------------------------------------------------------------

// load 把清单的 TBox + ABox 写入指定命名图，返回图 IRI。
func load(t *testing.T, m *Manifest, key, versionID string) (*graphstore.MemStore, ids.ID, Namespace, string) {
	t.Helper()
	ns, err := NewNamespace(key)
	if err != nil {
		t.Fatalf("NewNamespace 失败: %v", err)
	}
	graphIRI, err := GraphIRI(key, versionID)
	if err != nil {
		t.Fatalf("GraphIRI 失败: %v", err)
	}
	store := graphstore.NewMemStore()
	tenant := ids.New()
	tbox, err := m.TBox(ns)
	if err != nil {
		t.Fatalf("TBox 失败: %v", err)
	}
	abox, err := m.ABox(ns)
	if err != nil {
		t.Fatalf("ABox 失败: %v", err)
	}
	if _, err := store.InsertTriples(context.Background(), tenant, graphIRI, append(tbox, abox...)); err != nil {
		t.Fatalf("InsertTriples 失败: %v", err)
	}
	return store, tenant, ns, graphIRI
}

func TestEndToEnd_ConformingInstancePasses(t *testing.T) {
	m := mustParse(t, conformingManifest)
	store, tenant, ns, graphIRI := load(t, m, testOntologyKey, "ver-ok")

	shapes, err := m.BuildShapes(ns)
	if err != nil {
		t.Fatalf("BuildShapes 失败: %v", err)
	}
	report, err := shacl.Validate(store, tenant, graphIRI, shapes)
	if err != nil {
		t.Fatalf("shacl.Validate 失败: %v", err)
	}
	if !report.Conforms {
		t.Fatalf("合规实例应当通过，实际发现 %d 条问题: %+v", len(report.Results), report.Results)
	}
	if len(report.Results) != 0 {
		t.Fatalf("合规实例不应产生任何 finding，实际 %+v", report.Results)
	}
}

func TestEndToEnd_ViolatingInstanceIsBlockedWithLocations(t *testing.T) {
	m := mustParse(t, violatingManifest)
	store, tenant, ns, graphIRI := load(t, m, testOntologyKey, "ver-bad")

	shapes, err := m.BuildShapes(ns)
	if err != nil {
		t.Fatalf("BuildShapes 失败: %v", err)
	}
	report, err := shacl.Validate(store, tenant, graphIRI, shapes)
	if err != nil {
		t.Fatalf("shacl.Validate 失败: %v", err)
	}
	if report.Conforms {
		t.Fatal("违反形状约束的合同必须被拦下（门禁失效）")
	}

	// 三类违规都必须被定位到"哪个焦点节点、哪条路径"——这正是 ontology.validation_findings
	// 的 focus_node / path 两列的数据来源，也是前端唯一能展示"为什么不合格"的依据。
	wantFocus := ns.InstanceIRI("C-0002")
	got := map[string]string{} // component -> path
	for _, r := range report.Results {
		if r.FocusNode != wantFocus {
			t.Fatalf("finding 的焦点节点应为 %s，实际 %s", wantFocus, r.FocusNode)
		}
		if r.Severity != string(shacl.SeverityViolation) {
			t.Fatalf("finding 严重级别应为 Violation，实际 %s", r.Severity)
		}
		got[r.Component] = r.Path
	}
	for _, c := range []string{"Pattern", "MaxInclusive", "MinCount"} {
		if _, ok := got[c]; !ok {
			t.Fatalf("缺少 %s 违规项，实际组件集合 %v", c, got)
		}
	}
	if got["MaxInclusive"] != ns.AttributeIRI("Contract", "amount") {
		t.Fatalf("MaxInclusive 的 path 应为合同金额谓词，实际 %s", got["MaxInclusive"])
	}
	if got["MinCount"] != ns.RelationIRI("signed_by") {
		t.Fatalf("MinCount 的 path 应为签署关系谓词，实际 %s", got["MinCount"])
	}
}

// TestProbe_CatchesTheFalseGreen 是"探针先行自证"：先证明**没有探针会假绿**，
// 再证明探针能识破它。
//
// 场景是现实中最容易发生的一种：本体（TBox）装载成功，但数据（ABox）一条都没装载
// ——例如数据接入尚未配置、或租户刚建好。此时 SHACL 找不到任何焦点节点，
// 按规范返回 Conforms=true、无 finding。若调用方据此把版本置为 VALID，
// 就得到一个"显示通过、实际什么都没查"的门禁，比直接报 503 危险得多。
func TestProbe_CatchesTheFalseGreen(t *testing.T) {
	m := mustParse(t, conformingManifest)
	ns := mustNamespace(t)
	shapes, err := m.BuildShapes(ns)
	if err != nil {
		t.Fatalf("BuildShapes 失败: %v", err)
	}
	targets := m.TargetClassIRIs(ns)
	if len(targets) != 1 || targets[0] != ns.ClassIRI("Contract") {
		t.Fatalf("目标类集合不符: %v", targets)
	}

	t.Run("空图：校验报通过，探针报 0 —— 这正是不加探针就会假绿的情形", func(t *testing.T) {
		store := graphstore.NewMemStore()
		tenant := ids.New()
		graphIRI, _ := GraphIRI(testOntologyKey, "ver-empty")

		report, err := shacl.Validate(store, tenant, graphIRI, shapes)
		if err != nil {
			t.Fatalf("shacl.Validate 失败: %v", err)
		}
		if !report.Conforms || len(report.Results) != 0 {
			t.Fatalf("前提不成立：空图上的 SHACL 结论应为 Conforms=true 且无 finding，实际 %+v", report)
		}
		n, err := CountTypedNodes(context.Background(), store, tenant, graphIRI, targets)
		if err != nil {
			t.Fatalf("CountTypedNodes 失败: %v", err)
		}
		if n != 0 {
			t.Fatalf("空图上的焦点节点数应为 0，实际 %d", n)
		}
	})

	t.Run("只有 TBox、没有 ABox：校验同样报通过，探针同样报 0", func(t *testing.T) {
		store := graphstore.NewMemStore()
		tenant := ids.New()
		graphIRI, _ := GraphIRI(testOntologyKey, "ver-tbox-only")
		tbox, err := m.TBox(ns)
		if err != nil {
			t.Fatalf("TBox 失败: %v", err)
		}
		if _, err := store.InsertTriples(context.Background(), tenant, graphIRI, tbox); err != nil {
			t.Fatalf("InsertTriples 失败: %v", err)
		}
		total, err := store.CountTriples(context.Background(), tenant, graphIRI)
		if err != nil {
			t.Fatalf("CountTriples 失败: %v", err)
		}
		if total == 0 {
			t.Fatal("前提不成立：TBox 应当写入若干三元组")
		}
		report, err := shacl.Validate(store, tenant, graphIRI, shapes)
		if err != nil {
			t.Fatalf("shacl.Validate 失败: %v", err)
		}
		if !report.Conforms {
			t.Fatalf("前提不成立：只有 TBox 时 SHACL 结论应为 Conforms=true，实际 %+v", report)
		}
		// 注意：此时三元组数 > 0，因此"只看 CountTriples>0"的探针会被骗过；
		// 探针必须统计**属于目标类的实例节点**，而不是三元组总量。
		n, err := CountTypedNodes(context.Background(), store, tenant, graphIRI, targets)
		if err != nil {
			t.Fatalf("CountTypedNodes 失败: %v", err)
		}
		if n != 0 {
			t.Fatalf("只有 TBox 时焦点节点数应为 0，实际 %d", n)
		}
	})

	t.Run("装载 ABox 后探针报正数", func(t *testing.T) {
		_, store, tenant, _, graphIRI := func() (*Manifest, *graphstore.MemStore, ids.ID, Namespace, string) {
			st, tn, ns, g := load(t, m, testOntologyKey, "ver-loaded")
			return m, st, tn, ns, g
		}()
		n, err := CountTypedNodes(context.Background(), store, tenant, graphIRI, targets)
		if err != nil {
			t.Fatalf("CountTypedNodes 失败: %v", err)
		}
		if n != 1 {
			t.Fatalf("装载后 Contract 实例数应为 1，实际 %d", n)
		}
	})

	t.Run("跨租户不可见（探针不会读到别的租户的数据）", func(t *testing.T) {
		store, _, _, graphIRI := load(t, m, testOntologyKey, "ver-isolation")
		other := ids.New()
		n, err := CountTypedNodes(context.Background(), store, other, graphIRI, targets)
		if err != nil {
			t.Fatalf("CountTypedNodes 失败: %v", err)
		}
		if n != 0 {
			t.Fatalf("另一租户读到 %d 个焦点节点，租户隔离失效", n)
		}
	})
}

// TestLoadIsIdempotent 证明"重复校验"不会把图写脏：图 IRI 按版本确定性派生，
// 三元组按 (s,p,o) 去重，因此第二次装载新增 0 条。
func TestLoadIsIdempotent(t *testing.T) {
	m := mustParse(t, conformingManifest)
	ns := mustNamespace(t)
	graphIRI, _ := GraphIRI(testOntologyKey, "ver-idem")
	store := graphstore.NewMemStore()
	tenant := ids.New()

	tbox, _ := m.TBox(ns)
	abox, _ := m.ABox(ns)
	all := append(tbox, abox...)

	first, err := store.InsertTriples(context.Background(), tenant, graphIRI, all)
	if err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	second, err := store.InsertTriples(context.Background(), tenant, graphIRI, all)
	if err != nil {
		t.Fatalf("二次写入失败: %v", err)
	}
	if first != len(all) {
		t.Fatalf("首次写入应新增 %d 条，实际 %d", len(all), first)
	}
	if second != 0 {
		t.Fatalf("重复装载不应新增三元组，实际新增 %d 条", second)
	}
}

// ---------------------------------------------------------------------------
// 业务约束通道（constraints）：双通道派生 + 合并 + 可满足性校验
// ---------------------------------------------------------------------------

// constraintsOnlyManifest 与 violatingManifest 同构，但**完全没有 shapes**：
// 全部约束走 constraints 通道。这验证双通道设计的关键承诺——业务约束不因
// "没有形状可挂"而静默丢失（自动补建形状）。
const constraintsOnlyManifest = `{
  "summary": "合同管理本体（约束通道）",
  "classes": [
    {"name": "Contract", "label": "合同", "description": "一份合同", "attributes": [
      {"name": "contract_no", "type": "string", "description": "合同编号", "required": true},
      {"name": "amount", "type": "number", "description": "合同金额", "required": true}
    ]},
    {"name": "Party", "label": "合同相对方", "description": "签约主体", "attributes": [
      {"name": "party_name", "type": "string", "description": "主体名称", "required": true}
    ]}
  ],
  "relations": [
    {"name": "signed_by", "from_class": "Contract", "to_class": "Party",
     "cardinality": "many-to-one", "description": "合同由某主体签署"}
  ],
  "enumerations": [],
  "shapes": [],
  "constraints": [
    {"class": "Contract", "path": "contract_no", "min_count": 1, "pattern": "^HT-[0-9]{4}$"},
    {"class": "Contract", "path": "amount", "min_count": 1, "min_inclusive": 0, "max_inclusive": 1000000},
    {"class": "Contract", "path": "signed_by", "min_count": 1}
  ],
  "instances": [
    {"class": "Party", "id": "P-01", "properties": {"party_name": "甲方有限公司"}},
    {"class": "Contract", "id": "C-0001",
     "properties": {"contract_no": "HT-0001", "amount": 100000, "signed_by": "P-01"}}
  ]
}`

func TestConstraintsOnly_ChannelDerivesShapes(t *testing.T) {
	m := mustParse(t, constraintsOnlyManifest)
	if len(m.Constraints) != 3 {
		t.Fatalf("约束条数应为 3，实际 %d", len(m.Constraints))
	}
	ns := mustNamespace(t)
	shapes, err := m.BuildShapes(ns)
	if err != nil {
		t.Fatalf("BuildShapes 失败: %v", err)
	}
	// 显式形状为 0，但约束通道必须自动补出一个形状，且 3 条约束一条不丢。
	if len(shapes) != 1 {
		t.Fatalf("应自动派生 1 个形状，实际 %d", len(shapes))
	}
	if shapes[0].ID != ns.ShapeIRI("auto-Contract") {
		t.Fatalf("派生形状 ID 应为 auto-Contract 的 IRI，实际 %s", shapes[0].ID)
	}
	if len(shapes[0].Properties) != 3 {
		t.Fatalf("派生形状应含 3 条属性形状，实际 %d", len(shapes[0].Properties))
	}
	// 探针的目标类必须把约束通道的类算进去，否则"约束只挂在约束通道类上"的
	// 本体会被误判为 0 实例（反假绿探针漏报）。
	targets := m.TargetClassIRIs(ns)
	found := false
	for _, iri := range targets {
		if iri == ns.ClassIRI("Contract") {
			found = true
		}
	}
	if !found {
		t.Fatalf("TargetClassIRIs 应包含约束通道的类 Contract，实际 %v", targets)
	}
}

func TestConstraintsOnly_ViolatingInstanceBlocked(t *testing.T) {
	// 把合规实例换成违规实例（同 TestEndToEnd 的违规形态），约束通道必须同样拦得住。
	violating := strings.Replace(constraintsOnlyManifest,
		`{"class": "Contract", "id": "C-0001",
     "properties": {"contract_no": "HT-0001", "amount": 100000, "signed_by": "P-01"}}`,
		`{"class": "Contract", "id": "C-0002", "properties": {"contract_no": "BAD", "amount": 9999999}}`, 1)
	if violating == constraintsOnlyManifest {
		t.Fatal("测试语料替换失败")
	}
	m := mustParse(t, violating)
	store, tenant, ns, graphIRI := load(t, m, testOntologyKey, "ver-bad-c")
	shapes, err := m.BuildShapes(ns)
	if err != nil {
		t.Fatalf("BuildShapes 失败: %v", err)
	}
	report, err := shacl.Validate(store, tenant, graphIRI, shapes)
	if err != nil {
		t.Fatalf("shacl.Validate 失败: %v", err)
	}
	if report.Conforms {
		t.Fatal("约束通道的违规合同必须被拦下（约束丢失 = 静默放行）")
	}
	got := map[string]bool{}
	for _, r := range report.Results {
		if r.FocusNode != ns.InstanceIRI("C-0002") {
			t.Fatalf("finding 焦点节点应为 C-0002，实际 %s", r.FocusNode)
		}
		got[r.Component] = true
	}
	for _, c := range []string{"Pattern", "MaxInclusive", "MinCount"} {
		if !got[c] {
			t.Fatalf("缺少 %s 违规项，实际组件集合 %v", c, got)
		}
	}
}

func TestConstraints_MergeOverridesExplicitShape(t *testing.T) {
	// 显式形状写了宽松 pattern，约束通道写了文档里的严格 pattern：
	// 合并后业务约束优先（它来自文档逐字抽取，比模型骨架更贴近原意）。
	raw := strings.Replace(conformingManifest,
		`{"path": "contract_no", "min_count": 1, "max_count": 1, "datatype": "string", "pattern": "^HT-[0-9]{4}$"},`,
		`{"path": "contract_no", "min_count": 1, "max_count": 1, "datatype": "string", "pattern": "^.*$"},`, 1)
	if raw == conformingManifest {
		t.Fatal("测试语料替换失败")
	}
	raw = strings.Replace(raw, `"shapes": [`,
		`"constraints": [{"class": "Contract", "path": "contract_no", "pattern": "^HT-[0-9]{4}$"}], "shapes": [`, 1)
	m := mustParse(t, raw)
	ns := mustNamespace(t)
	shapes, err := m.BuildShapes(ns)
	if err != nil {
		t.Fatalf("BuildShapes 失败: %v", err)
	}
	if len(shapes) != 1 {
		t.Fatalf("合并后形状数应为 1（不新增），实际 %d", len(shapes))
	}
	for _, ps := range shapes[0].Properties {
		if ps.Path == ns.AttributeIRI("Contract", "contract_no") {
			if ps.Pattern != "^HT-[0-9]{4}$" {
				t.Fatalf("业务约束应覆盖显式形状的 pattern，实际 %q", ps.Pattern)
			}
			return
		}
	}
	t.Fatal("合并后形状缺少 contract_no 属性形状")
}

func TestConstraints_ClosureFailures(t *testing.T) {
	base := strings.Replace(conformingManifest, `"shapes": [`,
		`"constraints": [{"class": "Contract", "path": "contract_no", "pattern": "^HT-[0-9]{4}$"}], "shapes": [`, 1)
	if base == conformingManifest {
		t.Fatal("测试语料替换失败")
	}
	cases := []struct {
		name    string
		mutate  func(string) string
		pointer string
		keyword string
	}{
		{
			name: "class 未定义",
			mutate: func(s string) string {
				return strings.Replace(s, `"class": "Contract", "path": "contract_no"`, `"class": "NoSuch", "path": "contract_no"`, 1)
			},
			pointer: "/constraints/0/class", keyword: "未在 classes 中定义",
		},
		{
			name:    "path 未定义",
			mutate:  func(s string) string { return strings.Replace(s, `"path": "contract_no"`, `"path": "no_such_attr"`, 1) },
			pointer: "/constraints/0/path", keyword: "既不是已声明的关系",
		},
		{
			name: "min_count 大于 max_count",
			mutate: func(s string) string {
				return strings.Replace(s, `"path": "contract_no", "pattern"`, `"path": "contract_no", "min_count": 3, "max_count": 1, "pattern"`, 1)
			},
			pointer: "/constraints/0/min_count", keyword: "不可满足",
		},
		{
			name:    "正则不合法",
			mutate:  func(s string) string { return strings.Replace(s, `^HT-[0-9]{4}$`, `^HT-[0-9($`, 1) },
			pointer: "/constraints/0/pattern", keyword: "不是合法正则",
		},
		{
			name: "正则用于数值属性",
			mutate: func(s string) string {
				return strings.Replace(s, `"path": "contract_no", "pattern": "^HT-[0-9]{4}$"`, `"path": "amount", "pattern": "^HT-[0-9]{4}$"`, 1)
			},
			pointer: "/constraints/0/pattern", keyword: "只能用于文本属性",
		},
		{
			name: "数值区间用于文本属性",
			mutate: func(s string) string {
				return strings.Replace(s, `"path": "contract_no", "pattern": "^HT-[0-9]{4}$"`, `"path": "contract_no", "min_inclusive": 0`, 1)
			},
			pointer: "/constraints/0/min_inclusive", keyword: "数值区间只能用于",
		},
		{
			name: "in 用于数值属性",
			mutate: func(s string) string {
				return strings.Replace(s, `"path": "contract_no", "pattern": "^HT-[0-9]{4}$"`, `"path": "amount", "in": ["a", "b"]`, 1)
			},
			pointer: "/constraints/0/in", keyword: "不能用于数值属性",
		},
		{
			name: "关系不接受取值约束",
			mutate: func(s string) string {
				return strings.Replace(s, `"path": "contract_no", "pattern": "^HT-[0-9]{4}$"`, `"path": "signed_by", "pattern": "^x$"`, 1)
			},
			pointer: "/constraints/0/path", keyword: "只接受 min_count/max_count",
		},
		{
			name:    "占用保留前缀 auto- 的显式形状",
			mutate:  func(s string) string { return strings.Replace(s, `"id": "ContractShape"`, `"id": "auto-Contract"`, 1) },
			pointer: "/shapes/0/id", keyword: "保留前缀",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.mutate(base)
			_, err := ParseManifest([]byte(raw))
			if err == nil {
				t.Fatal("期望报错，实际通过")
			}
			if p := pointerOf(t, err); p != tc.pointer {
				t.Fatalf("JSON Pointer 应为 %s，实际 %s（错误：%v）", tc.pointer, p, err)
			}
			if !strings.Contains(err.Error(), tc.keyword) {
				t.Fatalf("错误信息应含 %q，实际：%v", tc.keyword, err)
			}
		})
	}
}
