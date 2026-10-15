package graphload

// 本文件把 manifest 的三份投影序列化为**人可读、可对外复核**的文本：
//   - RDF   → Turtle（TBox + ABox 全量三元组）
//   - SHACL → Turtle（BuildShapes 产出的形状，即校验时真正求值的约束）
//   - OWL   → RDF/XML（术语层公理：类、对象属性、数据类型属性）
//
// # 为什么放在 graphload 而不是 graphstore
//
// graphstore 是**图存储访问**的唯一入口（词项、命名图、SPARQL），它的职责是"把三元组
// 可靠地写进/读出图"。而"把 manifest 投影成 RDF 文本"是本体层的语义投影，与具体的
// 三元组存储无关，也不发生任何图访问。放在 graphload 与 TBox/ABox/BuildShapes 同一处，
// 保证文本与"校验时实际装载/求值的内容"出自同一份投影函数，不会出现"页面看到的
// 与真正执行的不一致"。
//
// # 为什么词项转义必须回落到 graphstore
//
// 词项（IRI / 字面值 / 空白节点）的转义规则只允许有一份实现，见
// graphstore/rdf.go 的文件头约定。本文件只做**拼装**，一切词项文本都经
// graphstore.TermNTriples 产出，绝不在本包重复实现 escapeIRI / escapeLiteral。

import (
	"fmt"
	"strconv"
	"strings"

	"odap/internal/ontology/graphstore"
	"odap/internal/ontology/shacl"
)

// 命名空间 IRI（前缀声明用）。
const (
	iriRDFNamespace   = "http://www.w3.org/1999/02/22-rdf-syntax-ns#"
	iriRDFSNamespace  = "http://www.w3.org/2000/01/rdf-schema#"
	iriOWLNamespace   = "http://www.w3.org/2002/07/owl#"
	iriSHACLNamespace = "http://www.w3.org/ns/shacl#"
)

// turtlePrefixes 是写出文本时声明的前缀。
//
// 默认前缀 `:` 固定为本体命名空间（见 writeTurtlePrefixes），其余是标准词表。
// 声明固定的一组前缀（哪怕某一页没用到）比按需裁剪更可预期：Turtle/XML 解析器
// 接受未被引用的前缀声明，而"同一份文件在不同调用点前缀不同"会让人难以比对。
var turtlePrefixes = []struct{ prefix, iri string }{
	{"rdf", iriRDFNamespace},
	{"rdfs", iriRDFSNamespace},
	{"xsd", xsdNamespace},
	{"owl", iriOWLNamespace},
	{"sh", iriSHACLNamespace},
}

// ---------------------------------------------------------------------------
// Turtle 基础：前缀、词项
// ---------------------------------------------------------------------------

// writeTurtlePrefixes 写出 @prefix 头。
func writeTurtlePrefixes(b *strings.Builder, ns Namespace) {
	fmt.Fprintf(b, "@prefix : <%s> .\n", ns.Base())
	for _, p := range turtlePrefixes {
		fmt.Fprintf(b, "@prefix %s: <%s> .\n", p.prefix, p.iri)
	}
	b.WriteString("\n")
}

// isTurtleLocal 判断局部名能否直接写成 Turtle 的前缀名（PN_LOCAL）。
//
// 为什么不能复用 isLocalName：后者允许首字符为 '-'、末字符为 '.'（清单里的名字用它
// 校验），但 Turtle 的 PN_LOCAL 不允许——首字符必须是字母/数字/'_'，末字符不能是 '.'。
// 直接缩写会产出一份解析器拒绝的文件：页面显示正常、导出后却无法使用，属于静默缺陷。
func isTurtleLocal(s string) bool {
	if s == "" {
		return false
	}
	first := s[0]
	switch {
	case first >= 'a' && first <= 'z', first >= 'A' && first <= 'Z',
		first >= '0' && first <= '9', first == '_':
	default:
		return false
	}
	if s[len(s)-1] == '.' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '_', c == '-', c == '.':
		default:
			return false
		}
	}
	return true
}

// shortenIRI 把 IRI 缩写为前缀名；无法缩写时返回空串（调用方回落 `<...>`）。
//
// 优先匹配本体命名空间（写成默认前缀 `:`），再匹配标准词表前缀。命中前缀但局部名
// 不是合法 PN_LOCAL 时同样返回空串——宁可用完整 IRI，也不产出解析器拒绝的文本。
func shortenIRI(iri string, ns Namespace) string {
	if local := strings.TrimPrefix(iri, ns.Base()); local != iri {
		if isTurtleLocal(local) {
			return ":" + local
		}
		return ""
	}
	for _, p := range turtlePrefixes {
		local := strings.TrimPrefix(iri, p.iri)
		if local == iri {
			continue
		}
		if isTurtleLocal(local) {
			return p.prefix + ":" + local
		}
		return ""
	}
	return ""
}

// writeTurtleTerm 按 Turtle 词法写入一枚词项。
//
// IRI 可缩写则写前缀名，否则与字面值/空白节点一样，直接用 graphstore 产出的规范
// N-Triples 文本（N-Triples 是 Turtle 的子集，因此该文本在 Turtle 里同样合法）。
// 字面值的 datatype **不做**二次缩写：在一个已按规范转义的词项文本上做字符串改写，
// 收益只是少几个字符，代价是多一处会与转义规则耦合的改写点。
func writeTurtleTerm(b *strings.Builder, ns Namespace, t graphstore.Term) error {
	if t.Kind == graphstore.KindIRI {
		if short := shortenIRI(t.Lexical, ns); short != "" {
			b.WriteString(short)
			return nil
		}
	}
	text, err := graphstore.TermNTriples(t)
	if err != nil {
		return err
	}
	b.WriteString(text)
	return nil
}

// turtleObjectText 把一枚词项渲染为字符串（供谓词-对象列表拼接）。
func turtleObjectText(ns Namespace, t graphstore.Term) (string, error) {
	var b strings.Builder
	if err := writeTurtleTerm(&b, ns, t); err != nil {
		return "", err
	}
	return b.String(), nil
}

// turtleIRIObjectText 把一个 IRI 渲染为字符串。
func turtleIRIObjectText(ns Namespace, iri string) (string, error) {
	return turtleObjectText(ns, graphstore.Term{Kind: graphstore.KindIRI, Lexical: iri})
}

// ---------------------------------------------------------------------------
// RDF（Turtle）
// ---------------------------------------------------------------------------

// RDFTurtle 把 manifest 的全部三元组（TBox 术语层 + ABox 实例层）序列化为 Turtle。
func (m *Manifest) RDFTurtle(ns Namespace) (string, error) {
	tbox, err := m.TBox(ns)
	if err != nil {
		return "", err
	}
	abox, err := m.ABox(ns)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	writeTurtlePrefixes(&b, ns)
	for _, tr := range tbox {
		if err := writeTurtleTriple(&b, ns, tr); err != nil {
			return "", err
		}
	}
	for _, tr := range abox {
		if err := writeTurtleTriple(&b, ns, tr); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}

// writeTurtleTriple 写出单条三元组（每条一行）。
func writeTurtleTriple(b *strings.Builder, ns Namespace, tr graphstore.Triple) error {
	for i, t := range []graphstore.Term{tr.S, tr.P, tr.O} {
		if i > 0 {
			b.WriteByte(' ')
		}
		if err := writeTurtleTerm(b, ns, t); err != nil {
			return err
		}
	}
	b.WriteString(" .\n")
	return nil
}

// ---------------------------------------------------------------------------
// SHACL（Turtle）
// ---------------------------------------------------------------------------

// shaclStmt 是一条待输出的谓词-对象对。
type shaclStmt struct {
	predicate string
	object    string
}

// SHACLTurtle 把 manifest 的形状（BuildShapes 的产物）序列化为 Turtle。
//
// 取值一律取自 shacl.PropertyShape 的结构体字段，而不是它的 Keywords 列表：
// 字段是"校验时真正会被求值的约束"，Keywords 只是 fail-closed 判据。从字段取值
// 才能保证抽屉里看到的约束 = 实际执行的约束（反之则会出现"显示了但没查"）。
func (m *Manifest) SHACLTurtle(ns Namespace) (string, error) {
	shapes, err := m.BuildShapes(ns)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	writeTurtlePrefixes(&b, ns)
	for i := range shapes {
		if err := writeShapeTurtle(&b, ns, &shapes[i]); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}

// writeShapeTurtle 写出一个 NodeShape 及其全部属性形状。
func writeShapeTurtle(b *strings.Builder, ns Namespace, sh *shacl.Shape) error {
	subj, err := turtleIRIObjectText(ns, sh.ID)
	if err != nil {
		return err
	}
	stmts := []shaclStmt{{predicate: "rdf:type", object: "sh:NodeShape"}}
	for _, tc := range sh.TargetClass {
		obj, err := turtleIRIObjectText(ns, tc)
		if err != nil {
			return err
		}
		stmts = append(stmts, shaclStmt{predicate: "sh:targetClass", object: obj})
	}
	for _, tn := range sh.TargetNode {
		obj, err := turtleObjectText(ns, tn)
		if err != nil {
			return err
		}
		stmts = append(stmts, shaclStmt{predicate: "sh:targetNode", object: obj})
	}
	if sh.Severity != "" {
		stmts = append(stmts, shaclStmt{predicate: "sh:severity", object: "sh:" + string(sh.Severity)})
	}
	for i := range sh.Properties {
		// 属性形状没有独立 IRI（BuildShapes 只在 NodeShape 上贴 sh:property），
		// 因此按下标从母形状 IRI 确定性派生一个可寻址 IRI：既能被引用，也不会
		// 因重复生成而漂移。
		pid := sh.ID + ".p" + strconv.Itoa(i+1)
		obj, err := turtleIRIObjectText(ns, pid)
		if err != nil {
			return err
		}
		stmts = append(stmts, shaclStmt{predicate: "sh:property", object: obj})
	}
	if err := writeStatements(b, subj, stmts); err != nil {
		return err
	}
	for i := range sh.Properties {
		if err := writePropertyShapeTurtle(b, ns, sh.ID+".p"+strconv.Itoa(i+1), &sh.Properties[i]); err != nil {
			return err
		}
	}
	return nil
}

// writePropertyShapeTurtle 写出一个属性形状。
func writePropertyShapeTurtle(b *strings.Builder, ns Namespace, id string, ps *shacl.PropertyShape) error {
	subj, err := turtleIRIObjectText(ns, id)
	if err != nil {
		return err
	}
	stmts := []shaclStmt{{predicate: "rdf:type", object: "sh:PropertyShape"}}

	addIRI := func(pred, iri string) error {
		obj, err := turtleIRIObjectText(ns, iri)
		if err != nil {
			return err
		}
		stmts = append(stmts, shaclStmt{predicate: pred, object: obj})
		return nil
	}
	addTerm := func(pred string, t graphstore.Term) error {
		obj, err := turtleObjectText(ns, t)
		if err != nil {
			return err
		}
		stmts = append(stmts, shaclStmt{predicate: pred, object: obj})
		return nil
	}
	addInt := func(pred string, v *int) {
		if v != nil {
			stmts = append(stmts, shaclStmt{predicate: pred, object: strconv.Itoa(*v)})
		}
	}
	addFloat := func(pred string, v *float64) {
		if v != nil {
			stmts = append(stmts, shaclStmt{predicate: pred, object: strconv.FormatFloat(*v, 'f', -1, 64)})
		}
	}

	if ps.Path != "" {
		if err := addIRI("sh:path", ps.Path); err != nil {
			return err
		}
	}
	addInt("sh:minCount", ps.MinCount)
	addInt("sh:maxCount", ps.MaxCount)
	if ps.Datatype != "" {
		if err := addIRI("sh:datatype", ps.Datatype); err != nil {
			return err
		}
	}
	if ps.Class != "" {
		if err := addIRI("sh:class", ps.Class); err != nil {
			return err
		}
	}
	if ps.NodeKind != "" {
		stmts = append(stmts, shaclStmt{predicate: "sh:nodeKind", object: "sh:" + string(ps.NodeKind)})
	}
	if len(ps.In) > 0 {
		var list strings.Builder
		list.WriteByte('(')
		for _, v := range ps.In {
			obj, err := turtleObjectText(ns, v)
			if err != nil {
				return err
			}
			list.WriteString(" " + obj)
		}
		list.WriteString(" )")
		stmts = append(stmts, shaclStmt{predicate: "sh:in", object: list.String()})
	}
	addFloat("sh:minInclusive", ps.MinInclusive)
	addFloat("sh:maxInclusive", ps.MaxInclusive)
	addFloat("sh:minExclusive", ps.MinExclusive)
	addFloat("sh:maxExclusive", ps.MaxExclusive)
	if ps.Pattern != "" {
		if err := addTerm("sh:pattern", graphstore.Term{
			Kind: graphstore.KindLiteral, Lexical: ps.Pattern, Datatype: xsdNamespace + "string",
		}); err != nil {
			return err
		}
	}
	addInt("sh:minLength", ps.MinLength)
	addInt("sh:maxLength", ps.MaxLength)
	// hasValue 目前无法由清单形态（PropertySpec）设置，但仍如实序列化：
	// 序列化器丢掉一个已赋值的字段是"撒谎"，将来清单支持该字段时也不会漏。
	if ps.HasValue != nil {
		if err := addTerm("sh:hasValue", *ps.HasValue); err != nil {
			return err
		}
	}
	if ps.Severity != "" {
		stmts = append(stmts, shaclStmt{predicate: "sh:severity", object: "sh:" + string(ps.Severity)})
	}
	return writeStatements(b, subj, stmts)
}

// writeStatements 以谓词-对象列表（`;` 续行）写出一个主语的全部语句。
func writeStatements(b *strings.Builder, subject string, stmts []shaclStmt) error {
	if len(stmts) == 0 {
		return nil
	}
	for i, st := range stmts {
		if i == 0 {
			fmt.Fprintf(b, "%s %s %s", subject, st.predicate, st.object)
			continue
		}
		fmt.Fprintf(b, " ;\n    %s %s", st.predicate, st.object)
	}
	b.WriteString(" .\n\n")
	return nil
}

// ---------------------------------------------------------------------------
// OWL（RDF/XML）
// ---------------------------------------------------------------------------

// OWLRDFXML 把术语层公理序列化为 RDF/XML。
//
// 只含 TBox（类 / 对象属性 / 数据类型属性）：OWL 视角关心的是"本体声明了什么"，
// 个体数据属于 RDF 视图（见 RDFTurtle）。本体 IRI 取命名空间根（去掉末尾 '#'），
// 它是术语命名空间的标识，与"某个版本的命名图 IRI"是两件事。
func (m *Manifest) OWLRDFXML(ns Namespace) (string, error) {
	ontologyIRI := strings.TrimSuffix(ns.Base(), "#")
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString("<rdf:RDF\n")
	fmt.Fprintf(&b, "    xmlns:rdf=\"%s\"\n", xmlAttrEscape(iriRDFNamespace))
	fmt.Fprintf(&b, "    xmlns:rdfs=\"%s\"\n", xmlAttrEscape(iriRDFSNamespace))
	fmt.Fprintf(&b, "    xmlns:owl=\"%s\"\n", xmlAttrEscape(iriOWLNamespace))
	fmt.Fprintf(&b, "    xmlns:xsd=\"%s\"\n", xmlAttrEscape(xsdNamespace))
	fmt.Fprintf(&b, "    xmlns:od=\"%s\">\n\n", xmlAttrEscape(ns.Base()))

	fmt.Fprintf(&b, "  <owl:Ontology rdf:about=\"%s\">\n", xmlAttrEscape(ontologyIRI))
	if summary := strings.TrimSpace(m.Summary); summary != "" {
		fmt.Fprintf(&b, "    <rdfs:comment>%s</rdfs:comment>\n", xmlTextEscape(summary))
	}
	b.WriteString("  </owl:Ontology>\n\n")

	for _, c := range m.Classes {
		classIRI := ns.ClassIRI(c.Name)
		fmt.Fprintf(&b, "  <owl:Class rdf:about=\"%s\">\n", xmlAttrEscape(classIRI))
		if label := strings.TrimSpace(c.Label); label != "" {
			fmt.Fprintf(&b, "    <rdfs:label>%s</rdfs:label>\n", xmlTextEscape(label))
		}
		if desc := strings.TrimSpace(c.Description); desc != "" {
			fmt.Fprintf(&b, "    <rdfs:comment>%s</rdfs:comment>\n", xmlTextEscape(desc))
		}
		b.WriteString("  </owl:Class>\n\n")

		for _, a := range c.Attributes {
			dt, ok := xsdIRI(a.Type)
			if !ok {
				// ParseManifest 已保证类型合法，走到这里说明投影与校验的口径不一致。
				return "", fmt.Errorf("graphload: 属性 %s.%s 的类型 %q 不在契约枚举内", c.Name, a.Name, a.Type)
			}
			attrIRI := ns.AttributeIRI(c.Name, a.Name)
			fmt.Fprintf(&b, "  <owl:DatatypeProperty rdf:about=\"%s\">\n", xmlAttrEscape(attrIRI))
			fmt.Fprintf(&b, "    <rdfs:domain rdf:resource=\"%s\"/>\n", xmlAttrEscape(classIRI))
			fmt.Fprintf(&b, "    <rdfs:range rdf:resource=\"%s\"/>\n", xmlAttrEscape(dt))
			if desc := strings.TrimSpace(a.Description); desc != "" {
				fmt.Fprintf(&b, "    <rdfs:comment>%s</rdfs:comment>\n", xmlTextEscape(desc))
			}
			b.WriteString("  </owl:DatatypeProperty>\n\n")
		}
	}

	for _, r := range m.Relations {
		relIRI := ns.RelationIRI(r.Name)
		fmt.Fprintf(&b, "  <owl:ObjectProperty rdf:about=\"%s\">\n", xmlAttrEscape(relIRI))
		fmt.Fprintf(&b, "    <rdfs:domain rdf:resource=\"%s\"/>\n", xmlAttrEscape(ns.ClassIRI(r.FromClass)))
		fmt.Fprintf(&b, "    <rdfs:range rdf:resource=\"%s\"/>\n", xmlAttrEscape(ns.ClassIRI(r.ToClass)))
		if desc := strings.TrimSpace(r.Description); desc != "" {
			fmt.Fprintf(&b, "    <rdfs:comment>%s</rdfs:comment>\n", xmlTextEscape(desc))
		}
		b.WriteString("  </owl:ObjectProperty>\n\n")
	}

	b.WriteString("</rdf:RDF>\n")
	return b.String(), nil
}

// xmlTextEscape 转义 XML 元素文本。
func xmlTextEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

// xmlAttrEscape 转义 XML 属性值。
//
// 与元素文本的规则**不同**：属性值里的引号必须转义（否则会提前闭合属性），
// 因此不能直接复用 xmlTextEscape。
func xmlAttrEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&apos;")
	return r.Replace(s)
}
