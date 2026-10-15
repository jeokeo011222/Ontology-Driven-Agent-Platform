package graphload

// 本文件锁定三份序列化产物（RDF/SHACL/OWL）的**对外契约**。它们会被 artifacts 端点
// 原样返回给界面与外部工具（Jena / RDF4J / pyshacl），因此正确性必须由测试保证：
//   - Turtle 的语法合法性（前缀缩写不得产出非法 PN_LOCAL）；
//   - 字面值/IRI 的转义必须**委托**给 graphstore 的单一实现；
//   - SHACL 文本必须覆盖"校验时真正求值的每一条约束"，不能漏（漏=显示了却没查）；
//   - OWL RDF/XML 必须能被 XML 解析器成功解析。

import (
	"encoding/xml"
	"strings"
	"testing"

	"odap/internal/ontology/graphstore"
)

// escapingManifest：类标签含会破坏 RDF/XML 语法的字符，且 shapes 为空
// （空形状是"尚未声明约束"的正常状态，序列化必须如实产出只含前缀的文本）。
const escapingManifest = `{
  "summary": "含特殊字符 <&> 的摘要",
  "classes": [
    {"name": "Contract", "label": "甲\"方\"\n<&>", "description": "一行\"双引号\"", "attributes": []}
  ],
  "relations": [],
  "enumerations": [],
  "shapes": [],
  "instances": []
}`

// ---------------------------------------------------------------------------
// 前缀缩写：合法性与回落
// ---------------------------------------------------------------------------

func TestIsTurtleLocal(t *testing.T) {
	ok := []string{"Contract", "contract_no", "_x", "1a", "a-b", "a.b"}
	for _, s := range ok {
		if !isTurtleLocal(s) {
			t.Fatalf("%q 应可写成前缀名", s)
		}
	}
	// 这四种在清单里是**合法**的名字（isLocalName 允许），但在 Turtle 的 PN_LOCAL
	// 里不合法。直接缩写会产出一份解析器拒绝的文件——必须回落完整 IRI。
	bad := []string{"", "-bad", "bad.", ".bad", "inst/C-0001", "合同", "a b"}
	for _, s := range bad {
		if isTurtleLocal(s) {
			t.Fatalf("%q 不是合法 PN_LOCAL，必须回落", s)
		}
	}
}

func TestShortenIRI(t *testing.T) {
	ns := mustNamespace(t)
	cases := map[string]string{
		ns.ClassIRI("Contract"):               ":Contract",
		ns.AttributeIRI("Contract", "amount"): ":Contract.amount",
		ns.RelationIRI("signed_by"):           ":signed_by",
		iriRDFType:                            "rdf:type",
		iriRDFSClass:                          "rdfs:Class",
		xsdNamespace + "decimal":              "xsd:decimal",
		iriSHACLNamespace + "NodeShape":       "sh:NodeShape",
		// 形状与实例 IRI 含 '/'，不是合法 PN_LOCAL：必须回落完整 IRI。
		ns.ShapeIRI("ContractShape"): "",
		ns.InstanceIRI("C-0001"):     "",
		// 未知命名空间不缩写。
		"http://example.org/foo#Bar": "",
		"":                           "",
	}
	for in, want := range cases {
		if got := shortenIRI(in, ns); got != want {
			t.Fatalf("shortenIRI(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// RDF（Turtle）
// ---------------------------------------------------------------------------

func TestRDFTurtle_OneStatementPerLineAndCountsMatchProjections(t *testing.T) {
	m := mustParse(t, conformingManifest)
	ns := mustNamespace(t)

	content, err := m.RDFTurtle(ns)
	if err != nil {
		t.Fatalf("RDFTurtle 失败: %v", err)
	}
	if !strings.HasPrefix(content, "@prefix : <"+ns.Base()+"> .\n") {
		t.Fatalf("Turtle 应以默认前缀（本体命名空间）开头，实际:\n%s", content)
	}
	for _, p := range turtlePrefixes {
		if !strings.Contains(content, "@prefix "+p.prefix+": <"+p.iri+"> .") {
			t.Fatalf("缺少前缀声明 %s: <%s>", p.prefix, p.iri)
		}
	}

	tbox, err := m.TBox(ns)
	if err != nil {
		t.Fatalf("TBox 失败: %v", err)
	}
	abox, err := m.ABox(ns)
	if err != nil {
		t.Fatalf("ABox 失败: %v", err)
	}

	// 每条三元组占一行且以 " ." 结尾：行数必须与投影数量一致，否则说明有语句
	// 被折叠/丢失（或写成了多行，外部工具逐行解析会出错）。
	var statements int
	truncated := strings.TrimRight(content, "\n")
	for _, line := range strings.Split(truncated, "\n") {
		if line == "" || strings.HasPrefix(line, "@prefix") {
			continue
		}
		if !strings.HasSuffix(line, " .") {
			t.Fatalf("每条三元组必须单行且以 \" .\" 结尾，实际: %q", line)
		}
		statements++
	}
	if want := len(tbox) + len(abox); statements != want {
		t.Fatalf("语句行数期望 %d（TBox %d + ABox %d），实际 %d", want, len(tbox), len(abox), statements)
	}

	// 抽查：类型声明、标签、按类限定的属性谓词、关系谓词、实例。
	for _, want := range []string{
		":Contract rdf:type rdfs:Class .",
		":Contract rdfs:label \"合同\"^^<" + xsdNamespace + "string> .",
		":Contract.amount rdfs:domain :Contract .",
		":signed_by rdfs:range :Party .",
		"rdf:type :Contract .",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("Turtle 缺少语句 %q", want)
		}
	}
}

// TestRDFTurtle_DelegatesLiteralEscaping 证明转义只有一份实现：序列化文本里的字面值
// 必须与 graphstore.TermNTriples 的输出**逐字符一致**。若本包自己写了一遍转义，
// 两份实现迟早漂移，而漂移的后果是"界面显示的 RDF"与"图里存的词项"不是同一个东西。
func TestRDFTurtle_DelegatesLiteralEscaping(t *testing.T) {
	m := mustParse(t, escapingManifest)
	ns := mustNamespace(t)

	content, err := m.RDFTurtle(ns)
	if err != nil {
		t.Fatalf("RDFTurtle 失败: %v", err)
	}
	label := m.Classes[0].Label
	token, err := graphstore.TermNTriples(graphstore.Term{
		Kind: graphstore.KindLiteral, Lexical: label, Datatype: xsdNamespace + "string",
	})
	if err != nil {
		t.Fatalf("TermNTriples 失败: %v", err)
	}
	if !strings.Contains(content, ":Contract rdfs:label "+token+" .") {
		t.Fatalf("字面值转义必须委托 graphstore 单一实现，期望 %q，实际:\n%s", token, content)
	}
	// 原始换行绝不能出现在语句中间（会凭空多出一行）。
	if strings.Contains(content, label) {
		t.Fatalf("未转义的原文不应出现在 RDF 文本里: %q", label)
	}
}

// ---------------------------------------------------------------------------
// SHACL（Turtle）
// ---------------------------------------------------------------------------

func TestSHACLTurtle_CoversEveryEvaluatedConstraint(t *testing.T) {
	m := mustParse(t, conformingManifest)
	ns := mustNamespace(t)
	shapes, err := m.BuildShapes(ns)
	if err != nil {
		t.Fatalf("BuildShapes 失败: %v", err)
	}
	content, err := m.SHACLTurtle(ns)
	if err != nil {
		t.Fatalf("SHACLTurtle 失败: %v", err)
	}

	for _, want := range []string{
		// 形状 IRI 含 '/'，不是合法 PN_LOCAL，因此以完整 IRI（尖括号）写出。
		"<" + ns.ShapeIRI("ContractShape") + "> rdf:type sh:NodeShape",
		"sh:targetClass :Contract",
		"sh:property <" + ns.ShapeIRI("ContractShape") + ".p1>",
		"sh:property <" + ns.ShapeIRI("ContractShape") + ".p2>",
		"sh:property <" + ns.ShapeIRI("ContractShape") + ".p3>",
		"sh:path :Contract.contract_no",
		"sh:minCount 1",
		"sh:maxCount 1",
		"sh:datatype xsd:string",
		"sh:datatype xsd:decimal",
		"sh:pattern",
		"sh:minInclusive 0",
		"sh:maxInclusive 1000000",
		"sh:path :signed_by",
		"sh:class :Party",
		"sh:nodeKind sh:IRI",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("SHACL 文本缺少 %q，实际:\n%s", want, content)
		}
	}
	if got := strings.Count(content, "rdf:type sh:PropertyShape"); got != len(shapes[0].Properties) {
		t.Fatalf("属性形状数期望 %d，实际 %d", len(shapes[0].Properties), got)
	}
	// sh:severity 只出现在**显式声明了它**的形状上：声明数量由 BuildShapes 的产物
	// 直接推导，而不是写死常量——本清单里只有 NodeShape 声明了 Violation，
	// 属性形状未声明（SHACL 默认即 Violation），因此这里是 1 而不是 4。
	wantSeverity := 0
	if shapes[0].Severity != "" {
		wantSeverity++
	}
	for i := range shapes[0].Properties {
		if shapes[0].Properties[i].Severity != "" {
			wantSeverity++
		}
	}
	if got := strings.Count(content, "sh:severity sh:Violation"); got != wantSeverity {
		t.Fatalf("sh:severity 期望出现 %d 次，实际 %d", wantSeverity, got)
	}
}

// 空 shapes 是正常状态：文本只含前缀声明，界面据此显示空态，**不伪造**形状。
func TestSHACLTurtle_EmptyShapesIsPreludeOnly(t *testing.T) {
	m := mustParse(t, escapingManifest)
	ns := mustNamespace(t)
	content, err := m.SHACLTurtle(ns)
	if err != nil {
		t.Fatalf("SHACLTurtle 失败: %v", err)
	}
	if !strings.HasPrefix(content, "@prefix : <"+ns.Base()+"> .\n") {
		t.Fatalf("空形状时仍应输出前缀头，实际:\n%s", content)
	}
	if strings.Contains(content, "sh:NodeShape") || strings.Contains(content, "sh:PropertyShape") {
		t.Fatalf("空 shapes 不应产出任何形状，实际:\n%s", content)
	}
}

// ---------------------------------------------------------------------------
// OWL（RDF/XML）
// ---------------------------------------------------------------------------

// owlDoc 是用于**结构合法性**校验的最小 XML 映射。namespace 前缀不参与匹配
// （encoding/xml 按局部名匹配），因此只写局部名。
type owlDoc struct {
	XMLName  xml.Name   `xml:"RDF"`
	Ontology []owlOnt   `xml:"Ontology"`
	Classes  []owlClass `xml:"Class"`
	Objects  []owlProp  `xml:"ObjectProperty"`
	Datatype []owlProp  `xml:"DatatypeProperty"`
}

type owlOnt struct {
	About   string `xml:"about,attr"`
	Comment string `xml:"comment"`
}

type owlClass struct {
	About   string `xml:"about,attr"`
	Label   string `xml:"label"`
	Comment string `xml:"comment"`
}

type owlProp struct {
	About   string `xml:"about,attr"`
	Domain  owlRes `xml:"domain"`
	Range   owlRes `xml:"range"`
	Comment string `xml:"comment"`
}

type owlRes struct {
	Resource string `xml:"resource,attr"`
}

func TestOWLRDFXML_IsParseableAndCarriesAxioms(t *testing.T) {
	m := mustParse(t, conformingManifest)
	ns := mustNamespace(t)
	content, err := m.OWLRDFXML(ns)
	if err != nil {
		t.Fatalf("OWLRDFXML 失败: %v", err)
	}
	if !strings.HasPrefix(content, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>") {
		t.Fatalf("缺少 XML 声明，实际:\n%s", content)
	}

	var doc owlDoc
	if err := xml.Unmarshal([]byte(content), &doc); err != nil {
		t.Fatalf("OWL RDF/XML 必须能被 XML 解析器解析，实际: %v\n%s", err, content)
	}
	if len(doc.Ontology) != 1 || doc.Ontology[0].About != strings.TrimSuffix(ns.Base(), "#") {
		t.Fatalf("本体 IRI 应为命名空间根，实际 %+v", doc.Ontology)
	}
	if len(doc.Classes) != len(m.Classes) {
		t.Fatalf("owl:Class 数量期望 %d，实际 %d", len(m.Classes), len(doc.Classes))
	}
	// 全部类属性都是 owl:DatatypeProperty：Contract 两个 + Party 一个 = 3。
	if len(doc.Datatype) != 3 {
		t.Fatalf("owl:DatatypeProperty 期望 3，实际 %d", len(doc.Datatype))
	}
	if len(doc.Objects) != 1 {
		t.Fatalf("owl:ObjectProperty 期望 1，实际 %d", len(doc.Objects))
	}
	// domain/range 必须齐全：少一个 range 就等于"声明了属性但没说它的取值范围"。
	for _, p := range append(doc.Objects, doc.Datatype...) {
		if p.Domain.Resource == "" || p.Range.Resource == "" {
			t.Fatalf("属性公理缺少 domain/range: %+v", p)
		}
	}
	if got := doc.Datatype[1].Range.Resource; got != xsdNamespace+"decimal" {
		t.Fatalf("amount 的 rdfs:range 应为 xsd:decimal，实际 %s", got)
	}
	if got := doc.Objects[0].Range.Resource; got != ns.ClassIRI("Party") {
		t.Fatalf("signed_by 的 rdfs:range 应为 Party，实际 %s", got)
	}
}

// TestOWLRDFXML_RoundTripsEscapedText 证明 XML 转义正确且可逆：解析回来必须与
// 原文逐字符一致。属性值里的引号与元素文本里的尖括号/& 若不加区分地转义，
// 就会在这里失败（前者不转义会提前闭合属性，后者不转义会破坏元素结构）。
func TestOWLRDFXML_RoundTripsEscapedText(t *testing.T) {
	m := mustParse(t, escapingManifest)
	ns := mustNamespace(t)
	content, err := m.OWLRDFXML(ns)
	if err != nil {
		t.Fatalf("OWLRDFXML 失败: %v", err)
	}
	var doc owlDoc
	if err := xml.Unmarshal([]byte(content), &doc); err != nil {
		t.Fatalf("含特殊字符的 RDF/XML 仍必须可解析，实际: %v\n%s", err, content)
	}
	if len(doc.Ontology) != 1 {
		t.Fatalf("本体元素缺失，实际 %+v", doc)
	}
	if got := doc.Ontology[0].Comment; got != m.Summary {
		t.Fatalf("摘要往返不一致: %q != %q", got, m.Summary)
	}
	if len(doc.Classes) != 1 {
		t.Fatalf("类元素缺失，实际 %+v", doc)
	}
	if got := doc.Classes[0].Label; got != m.Classes[0].Label {
		t.Fatalf("类标签往返不一致: %q != %q", got, m.Classes[0].Label)
	}
	if got := doc.Classes[0].Comment; got != m.Classes[0].Description {
		t.Fatalf("类描述往返不一致: %q != %q", got, m.Classes[0].Description)
	}
	// 属性值（IRI）以 " 转义进 rdf:about/rdf:resource：解析回来必须与投影出的 IRI 一致。
	if got := doc.Classes[0].About; got != ns.ClassIRI("Contract") {
		t.Fatalf("rdf:about 往返不一致: %q", got)
	}
}

// TestXMLEscapeHelpers 锁定两个转义函数的分工不同。
func TestXMLEscapeHelpers(t *testing.T) {
	if got := xmlTextEscape(`a<b>&c"d'e`); got != `a&lt;b&gt;&amp;c"d'e` {
		t.Fatalf("元素文本转义不符: %q", got)
	}
	if got := xmlAttrEscape(`a<b>&c"d'e`); got != `a&lt;b&gt;&amp;c&quot;d&apos;e` {
		t.Fatalf("属性值转义必须额外处理引号: %q", got)
	}
}
