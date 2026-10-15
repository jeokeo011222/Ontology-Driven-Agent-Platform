package graphload

// 本文件把 manifest 投影为**结构化的节点/边**，供界面把本体画成图。
//
// # 为什么从 manifest 投影，而不是回头去解析图上文里的 Turtle / RDF-XML
//
// 与 RDFTurtle / OWLRDFXML / SHACLTurtle 同源：那三份文本由 TBox / ABox /
// BuildShapes 产出，本文件复用同样的三份投影。因此界面上的每一个节点、每一条边
// 都能在源码页签里逐条找到对应语句——"图"与"文本"不可能各说各话。
//
// 若反过来解析文本，就等于在本仓新增一个 RDF 解析器（见 graphload.go 文件头：
// 仓内刻意没有 RDF 解析器），而且解析失败时会得到一张残缺的图，页面上却看不出缺了什么。
//
// # 形状图的取值口径
//
// 约束要点取自 shacl.PropertyShape 的**结构体字段**（即校验时真正求值的约束），
// 与 SHACLTurtle 完全一致，不从 Keywords 列表反推。这样"图上显示的约束"与
// "实际执行的约束"是同一份数据。

import (
	"strconv"
	"strings"

	"odap/internal/ontology/shacl"
	"odap/internal/platform/errors"
)

// 节点类型。取值是**固定枚举**，界面按它选择样式与图例。
const (
	// GraphNodeClass 本体里的类（TBox）。
	GraphNodeClass = "class"
	// GraphNodeInstance 样例实例（ABox）。
	GraphNodeInstance = "instance"
	// GraphNodeShape SHACL 形状（sh:NodeShape）。
	GraphNodeShape = "shape"
	// GraphNodeConstraint 形状的属性形状（sh:PropertyShape）——挂在形状上的约束项。
	GraphNodeConstraint = "constraint"
)

// 边类型。
const (
	// GraphEdgeRelation 对象属性（类与类之间，或实例与实例之间）。
	GraphEdgeRelation = "relation"
	// GraphEdgeType 实例到其类（rdf:type）。
	GraphEdgeType = "type"
	// GraphEdgeTarget 形状到目标类（sh:targetClass）。
	GraphEdgeTarget = "target"
	// GraphEdgeProperty 形状到属性形状（sh:property）。
	GraphEdgeProperty = "property"
)

// GraphNode 是图上的一个节点。
type GraphNode struct {
	// ID 是节点的词项 IRI，与产出文本里的 `<...>` 完全一致，便于逐条对照。
	ID string `json:"id"`
	// Kind 见 GraphNodeClass / GraphNodeInstance / GraphNodeShape / GraphNodeConstraint。
	Kind string `json:"kind"`
	// Label 是画在节点里的主标题：优先用中文标签，没有则回落词项局部名。
	Label string `json:"label"`
	// Subtitle 是词项局部名（与源码里的写法一致），与 Label 相同时省略。
	Subtitle string `json:"subtitle,omitempty"`
	// Details 是节点内的要点行（类的属性、实例的属性取值、约束项…）。
	// 它们同样是图里的三元组，画成节点内的文字而不是叶子节点，是为了避免
	// 一个大本体刚打开就散成几百个字符节点。
	Details []string `json:"details,omitempty"`
}

// GraphEdge 是图上的一条边。
type GraphEdge struct {
	// From / To 是节点 ID（词项 IRI）。
	From string `json:"from"`
	To   string `json:"to"`
	// Kind 见 GraphEdgeRelation / GraphEdgeType / GraphEdgeTarget / GraphEdgeProperty。
	Kind string `json:"kind"`
	// Label 是边上的谓词简称（如 `claimType` / `sh:targetClass`）。
	Label string `json:"label"`
}

// Graph 是一张自包含的图（节点 + 边）。
type Graph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

// OntologyGraph 投影本体结构图。
//
// includeInstances 为 true 时把 ABox 一并画出（实例节点 + rdf:type 边 +
// 实例之间的关系边）；为 false 时只画类与类之间的关系。
//
// 实例的字面值属性画在实例节点内部（Details），不另造字面值节点：字面值不是可导航的
// 实体，把它们摊成节点只会让图变密却提供不了额外信息。
func (m *Manifest) OntologyGraph(ns Namespace, includeInstances bool) Graph {
	g := Graph{Nodes: []GraphNode{}, Edges: []GraphEdge{}}

	classLabel := make(map[string]string, len(m.Classes))
	for _, c := range m.Classes {
		classLabel[c.Name] = classDisplayLabel(c)
		g.Nodes = append(g.Nodes, classGraphNode(ns, c))
	}

	for _, r := range m.Relations {
		g.Edges = append(g.Edges, GraphEdge{
			From: ns.ClassIRI(r.FromClass), To: ns.ClassIRI(r.ToClass),
			Kind: GraphEdgeRelation, Label: r.Name,
		})
	}

	if !includeInstances {
		return g
	}

	for _, in := range m.Instances {
		// 实例节点的标题用实例 ID（它就是图里的主语），副标题给出它所属的类，
		// 这样不必回到本体图也能看出类别归属。
		g.Nodes = append(g.Nodes, GraphNode{
			ID: ns.InstanceIRI(in.ID), Kind: GraphNodeInstance,
			Label: in.ID, Subtitle: classLabel[in.Class],
			Details: m.instanceDetails(in),
		})
		g.Edges = append(g.Edges, GraphEdge{
			From: ns.InstanceIRI(in.ID), To: ns.ClassIRI(in.Class),
			Kind: GraphEdgeType, Label: "rdf:type",
		})
	}

	// 实例之间的对象属性边。字面值属性已进 node.Details，这里不再产出边。
	for _, in := range m.Instances {
		for key, raw := range in.Properties {
			ref, err := m.ResolveTerm(in.Class, key)
			// ParseManifest 已做过闭包校验，此处不可能失败。真失败时跳过这一条边
			// 不影响其它数据（边的两端都已单独画出），但绝不"补一条假边"来凑数。
			if err != nil || !ref.IsRelation {
				continue
			}
			for _, target := range asStringList(raw) {
				g.Edges = append(g.Edges, GraphEdge{
					From: ns.InstanceIRI(in.ID), To: ns.InstanceIRI(target),
					Kind: GraphEdgeRelation, Label: ref.Name,
				})
			}
		}
	}
	return g
}

// classDisplayLabel 取类的展示名：优先中文标签，缺失时回落词项局部名。
func classDisplayLabel(c Class) string {
	if label := strings.TrimSpace(c.Label); label != "" {
		return label
	}
	return c.Name
}

// classGraphNode 构造一个类节点（本体结构图与形状图共用，保证同一类的节点形态一致）。
func classGraphNode(ns Namespace, c Class) GraphNode {
	label := classDisplayLabel(c)
	node := GraphNode{ID: ns.ClassIRI(c.Name), Kind: GraphNodeClass, Label: label}
	if label != c.Name {
		node.Subtitle = c.Name
	}
	for _, a := range c.Attributes {
		node.Details = append(node.Details, attributeBrief(a))
	}
	return node
}

// classByName 按类名取声明（找不到时返回 false）。
func (m *Manifest) classByName(name string) (Class, bool) {
	for i := range m.Classes {
		if m.Classes[i].Name == name {
			return m.Classes[i], true
		}
	}
	return Class{}, false
}

// classNodeByIRI 按词项 IRI 反查类声明并构造节点。
//
// 形状的目标类由 BuildShapes 从清单派生，因此这里的反查必然命中；万一没命中，
// 仍要画出这个节点——否则会有一条边指向不存在的节点，图会静默缺一块。
func (m *Manifest) classNodeByIRI(ns Namespace, iri string) GraphNode {
	for i := range m.Classes {
		if ns.ClassIRI(m.Classes[i].Name) == iri {
			return classGraphNode(ns, m.Classes[i])
		}
	}
	return GraphNode{ID: iri, Kind: GraphNodeClass, Label: localNameOf(iri)}
}

// instanceDetails 汇总实例的字面值属性，形如 `claim_no: RC-2026-0001`。
//
// 遍历顺序按**类里声明的属性顺序**，而不是 `Properties` 这个 map 的随机顺序：
// 同一份清单两次打开时的要点行次序必须一致，否则截图与人工核对都无法复现。
func (m *Manifest) instanceDetails(in Instance) []string {
	class, ok := m.classByName(in.Class)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(class.Attributes))
	for _, a := range class.Attributes {
		raw, present := in.Properties[a.Name]
		if !present {
			continue
		}
		term, err := literalFor(a.Type, raw)
		if err != nil {
			// 取值与声明类型不符属于装载期问题（ParseManifest 已拦下），此处如实
			// 标出"非法"，不猜一个看着正常的值画上去。
			out = append(out, a.Name+": 取值非法")
			continue
		}
		out = append(out, a.Name+": "+term.Lexical)
	}
	return out
}

// attributeBrief 把属性声明写成一行要点，如 `amount: decimal`（必填则带 ` *`）。
func attributeBrief(a Attribute) string {
	line := a.Name + ": " + a.Type
	if a.Required {
		line += " *"
	}
	return line
}

// ShapeGraph 投影形状约束图：形状 → 目标类，以及形状 → 约束项。
//
// 目标类节点与本体结构图里的类是同一批 IRI，因此两张图切换时同一个类看起来是同一个节点。
//
// 约束节点的 ID 用 `sh.ID + ".p" + 序号`——这正是 SHACLTurtle 为属性形状派生的主语
// IRI。同一个形状下同一条路径出现两次会得到两个不同 ID 的节点，不会互相覆盖成
// 一张"少了一半约束"的图。
func (m *Manifest) ShapeGraph(ns Namespace) (Graph, error) {
	g := Graph{Nodes: []GraphNode{}, Edges: []GraphEdge{}}
	shapes, err := m.BuildShapes(ns)
	if err != nil {
		return Graph{}, err
	}
	// BuildShapes 对每个 ShapeSpec 恰好产出一个 Shape、对其每个 PropertySpec 恰好产出一个
	// PropertyShape，因此两个切片的长度与下标一一对应（同一包内的构造不变量）。
	// 一旦不成立，说明投影实现已经漂移；此时宁可整体报错，也不画出一张与
	// SHACL 源码对不上的图。
	if len(shapes) != len(m.Shapes) {
		return Graph{}, errors.E(errors.CodeValidationFailed,
			"graphload: 形状投影条数与清单不一致，无法画出形状图")
	}

	classSeen := make(map[string]bool, len(m.Classes))
	for i := range shapes {
		spec := &m.Shapes[i]
		sh := &shapes[i]

		shape := GraphNode{ID: sh.ID, Kind: GraphNodeShape, Label: spec.ID, Subtitle: "sh:NodeShape"}
		if sh.Severity != "" {
			shape.Details = append(shape.Details, "severity "+string(sh.Severity))
		}
		g.Nodes = append(g.Nodes, shape)

		for _, tc := range sh.TargetClass {
			if !classSeen[tc] {
				classSeen[tc] = true
				g.Nodes = append(g.Nodes, m.classNodeByIRI(ns, tc))
			}
			g.Edges = append(g.Edges, GraphEdge{
				From: sh.ID, To: tc, Kind: GraphEdgeTarget, Label: "sh:targetClass",
			})
		}

		for j := range spec.Properties {
			ps := &sh.Properties[j]
			id := sh.ID + ".p" + strconv.Itoa(j+1)
			g.Nodes = append(g.Nodes, GraphNode{
				ID: id, Kind: GraphNodeConstraint,
				Label:    spec.Properties[j].Path,
				Subtitle: shortTerm(ns, ps.Path),
				Details:  propertyShapeDetails(ps, ns),
			})
			g.Edges = append(g.Edges, GraphEdge{
				From: sh.ID, To: id, Kind: GraphEdgeProperty, Label: "sh:property",
			})
		}
	}
	return g, nil
}

// propertyShapeDetails 把一条属性形状的**实际取值**写成要点行。
func propertyShapeDetails(ps *shacl.PropertyShape, ns Namespace) []string {
	out := make([]string, 0, 8)
	addInt := func(key string, v *int) {
		if v != nil {
			out = append(out, key+" "+strconv.Itoa(*v))
		}
	}
	addFloat := func(key string, v *float64) {
		if v != nil {
			out = append(out, key+" "+strconv.FormatFloat(*v, 'f', -1, 64))
		}
	}

	addInt("minCount", ps.MinCount)
	addInt("maxCount", ps.MaxCount)
	if ps.Datatype != "" {
		out = append(out, "datatype "+shortTerm(ns, ps.Datatype))
	}
	if ps.Class != "" {
		out = append(out, "class "+shortTerm(ns, ps.Class))
	}
	if ps.NodeKind != "" {
		out = append(out, "nodeKind "+string(ps.NodeKind))
	}
	if len(ps.In) > 0 {
		values := make([]string, 0, len(ps.In))
		for _, t := range ps.In {
			values = append(values, t.Lexical)
		}
		out = append(out, "in ["+strings.Join(values, ", ")+"]")
	}
	addFloat("minInclusive", ps.MinInclusive)
	addFloat("maxInclusive", ps.MaxInclusive)
	addFloat("minExclusive", ps.MinExclusive)
	addFloat("maxExclusive", ps.MaxExclusive)
	if ps.Pattern != "" {
		out = append(out, "pattern "+ps.Pattern)
	}
	addInt("minLength", ps.MinLength)
	addInt("maxLength", ps.MaxLength)
	if ps.HasValue != nil {
		out = append(out, "hasValue "+ps.HasValue.Lexical)
	}
	if ps.Severity != "" {
		out = append(out, "severity "+string(ps.Severity))
	}
	return out
}

// shortTerm 把 IRI 缩写成与源码页签一致的写法（`:ExpenseClaim.amount` / `xsd:decimal`）；
// 无法缩写时回落局部名，再回落整条 IRI。
func shortTerm(ns Namespace, iri string) string {
	if short := shortenIRI(iri, ns); short != "" {
		return short
	}
	if local := localNameOf(iri); local != "" {
		return local
	}
	return iri
}

// localNameOf 取 IRI 的最后一个 '#' 或 '/' 之后的片段。
func localNameOf(iri string) string {
	s := iri
	if i := strings.LastIndex(s, "#"); i >= 0 {
		return s[i+1:]
	}
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}
