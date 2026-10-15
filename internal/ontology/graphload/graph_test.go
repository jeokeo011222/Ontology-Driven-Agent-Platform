package graphload

// 本文件锁定图投影（界面把本体画成图所依赖的那份结构）的对外契约。
//
// 最重要的一条不变量：**每条边的两端都必须在 nodes 里出现过**。
// 违反它不会报错，只会让界面上画出一条连着空气的边（或整块缺失），
// 因此必须由测试来保证，而不是靠肉眼在页面上看。

import (
	"strings"
	"testing"
)

// assertEndpointsResolve 校验每条边的两端都能在节点集合里找到。
func assertEndpointsResolve(t *testing.T, g Graph) {
	t.Helper()
	known := make(map[string]bool, len(g.Nodes))
	for _, n := range g.Nodes {
		if n.ID == "" {
			t.Fatalf("存在 ID 为空的节点：%+v", n)
		}
		if known[n.ID] {
			t.Fatalf("节点 ID 重复：%s（图上会出现两个同名节点）", n.ID)
		}
		known[n.ID] = true
		if n.Label == "" {
			t.Fatalf("节点 %s 没有标题（画出来是一个空框）", n.ID)
		}
	}
	for _, e := range g.Edges {
		if !known[e.From] {
			t.Fatalf("边的起点 %s 不在节点集合里", e.From)
		}
		if !known[e.To] {
			t.Fatalf("边的终点 %s 不在节点集合里", e.To)
		}
		if e.Label == "" {
			t.Fatalf("边 %s→%s 没有谓词", e.From, e.To)
		}
	}
}

func TestOntologyGraphWithoutInstances(t *testing.T) {
	ns := mustNamespace(t)
	m, err := ParseManifest([]byte(conformingManifest))
	if err != nil {
		t.Fatalf("清单应通过解析：%v", err)
	}

	g := m.OntologyGraph(ns, false)
	assertEndpointsResolve(t, g)

	if len(g.Nodes) != 2 {
		t.Fatalf("只看类时应有 2 个节点，实际 %d 个", len(g.Nodes))
	}
	if len(g.Edges) != 1 {
		t.Fatalf("只看类时应有 1 条关系边，实际 %d 条", len(g.Edges))
	}
	edge := g.Edges[0]
	if edge.Kind != GraphEdgeRelation || edge.Label != "signed_by" {
		t.Fatalf("关系边的类型/谓词不对：%+v", edge)
	}
	if edge.From != ns.ClassIRI("Contract") || edge.To != ns.ClassIRI("Party") {
		t.Fatalf("关系边的方向不对：%+v", edge)
	}
	// 实例节点必须在关闭开关时**完全不出现**（不是画淡色占位）。
	for _, n := range g.Nodes {
		if n.Kind != GraphNodeClass {
			t.Fatalf("关闭实例后不应出现 %s 类型节点", n.Kind)
		}
	}
}

func TestOntologyGraphWithInstances(t *testing.T) {
	ns := mustNamespace(t)
	m, err := ParseManifest([]byte(conformingManifest))
	if err != nil {
		t.Fatalf("清单应通过解析：%v", err)
	}

	g := m.OntologyGraph(ns, true)
	assertEndpointsResolve(t, g)

	// 2 个类 + 2 个实例。
	if len(g.Nodes) != 4 {
		t.Fatalf("应画出 4 个节点，实际 %d 个", len(g.Nodes))
	}
	// 1 条类间关系 + 2 条 rdf:type + 1 条实例间关系。
	if len(g.Edges) != 4 {
		t.Fatalf("应画出 4 条边，实际 %d 条", len(g.Edges))
	}

	byID := make(map[string]GraphNode, len(g.Nodes))
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}

	contract := byID[ns.ClassIRI("Contract")]
	if contract.Label != "合同" || contract.Subtitle != "Contract" {
		t.Fatalf("类节点标题不对：%+v", contract)
	}
	// 属性要点必须按**声明顺序**，而不是 map 的随机顺序。
	wantAttrs := []string{"contract_no: string *", "amount: number *"}
	if len(contract.Details) != len(wantAttrs) {
		t.Fatalf("属性要点条数不对：%+v", contract.Details)
	}
	for i, want := range wantAttrs {
		if contract.Details[i] != want {
			t.Fatalf("属性要点[%d] 期望 %q，实际 %q", i, want, contract.Details[i])
		}
	}

	claim := byID[ns.InstanceIRI("C-0001")]
	if claim.Kind != GraphNodeInstance || claim.Subtitle != "合同" {
		t.Fatalf("实例节点不对：%+v", claim)
	}
	if len(claim.Details) != 2 || !strings.HasPrefix(claim.Details[0], "contract_no: ") ||
		!strings.HasPrefix(claim.Details[1], "amount: ") {
		t.Fatalf("实例属性要点不对（顺序/前缀）：%+v", claim.Details)
	}

	// rdf:type 边与实例间关系边都要在。
	var hasType, hasRel bool
	for _, e := range g.Edges {
		if e.Kind == GraphEdgeType && e.From == ns.InstanceIRI("C-0001") && e.To == ns.ClassIRI("Contract") {
			hasType = true
		}
		if e.Kind == GraphEdgeRelation && e.From == ns.InstanceIRI("C-0001") && e.To == ns.InstanceIRI("P-01") {
			hasRel = true
		}
	}
	if !hasType || !hasRel {
		t.Fatalf("缺少实例的 rdf:type 或实例关系边：%+v", g.Edges)
	}
}

func TestShapeGraph(t *testing.T) {
	ns := mustNamespace(t)
	m, err := ParseManifest([]byte(conformingManifest))
	if err != nil {
		t.Fatalf("清单应通过解析：%v", err)
	}

	g, err := m.ShapeGraph(ns)
	if err != nil {
		t.Fatalf("形状图投影不应报错：%v", err)
	}
	assertEndpointsResolve(t, g)

	// 1 个形状 + 1 个目标类 + 3 个约束项。
	if len(g.Nodes) != 5 {
		t.Fatalf("应画出 5 个节点，实际 %d 个：%+v", len(g.Nodes), g.Nodes)
	}
	// 1 条 sh:targetClass + 3 条 sh:property。
	if len(g.Edges) != 4 {
		t.Fatalf("应画出 4 条边，实际 %d 条", len(g.Edges))
	}

	byID := make(map[string]GraphNode, len(g.Nodes))
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	shapeID := ns.ShapeIRI("ContractShape")
	shape := byID[shapeID]
	if shape.Kind != GraphNodeShape || shape.Label != "ContractShape" {
		t.Fatalf("形状节点不对：%+v", shape)
	}
	if len(shape.Details) != 1 || shape.Details[0] != "severity Violation" {
		t.Fatalf("形状节点要点不对：%+v", shape.Details)
	}
	if !hasEdge(g, shapeID, ns.ClassIRI("Contract"), GraphEdgeTarget) {
		t.Fatalf("缺少 sh:targetClass 边：%+v", g.Edges)
	}

	// 约束节点的 ID 与 SHACL 文本里的属性形状主语一致（shape IRI + .pN）。
	contractNo := byID[shapeID+".p1"]
	if contractNo.Kind != GraphNodeConstraint || contractNo.Label != "contract_no" {
		t.Fatalf("约束节点不对：%+v", contractNo)
	}
	if contractNo.Subtitle != ":Contract.contract_no" {
		t.Fatalf("约束节点的路径写法应与源码一致，实际 %q", contractNo.Subtitle)
	}
	// 约束要点必须来自**实际求值的字段**。
	for _, want := range []string{"minCount 1", "maxCount 1", "datatype xsd:string", "pattern ^HT-[0-9]{4}$"} {
		if !contains(contractNo.Details, want) {
			t.Fatalf("约束要点缺少 %q：%+v", want, contractNo.Details)
		}
	}
	amount := byID[shapeID+".p2"]
	for _, want := range []string{"minInclusive 0", "maxInclusive 1000000", "datatype xsd:decimal"} {
		if !contains(amount.Details, want) {
			t.Fatalf("金额约束要点缺少 %q：%+v", want, amount.Details)
		}
	}
	signedBy := byID[shapeID+".p3"]
	for _, want := range []string{"class :Party", "nodeKind IRI", "minCount 1"} {
		if !contains(signedBy.Details, want) {
			t.Fatalf("关系约束要点缺少 %q：%+v", want, signedBy.Details)
		}
	}
}

// twoShapesSamePath：两个形状约束同一条路径。约束节点若以"路径"为 ID，
// 两者会合并成一个节点，图上就会少掉一组约束——因此 ID 必须带上所属形状。
const twoShapesSamePath = `{
  "summary": "同一路径被两个形状约束",
  "classes": [
    {"name": "Contract", "label": "合同", "description": "", "attributes": [
      {"name": "amount", "type": "decimal", "description": "", "required": true}
    ]}
  ],
  "relations": [],
  "enumerations": [],
  "shapes": [
    {"id": "ShapeA", "target_class": "Contract", "severity": "Violation", "properties": [
      {"path": "amount", "min_count": 1}
    ]},
    {"id": "ShapeB", "target_class": "Contract", "severity": "Warning", "properties": [
      {"path": "amount", "max_inclusive": 100}
    ]}
  ],
  "instances": []
}`

func TestShapeGraphKeepsBothShapesOnSamePath(t *testing.T) {
	ns := mustNamespace(t)
	m, err := ParseManifest([]byte(twoShapesSamePath))
	if err != nil {
		t.Fatalf("清单应通过解析：%v", err)
	}
	g, err := m.ShapeGraph(ns)
	if err != nil {
		t.Fatalf("形状图投影不应报错：%v", err)
	}
	assertEndpointsResolve(t, g)

	// 2 个形状 + 1 个目标类（去重后只画一次）+ 2 个约束项。
	if len(g.Nodes) != 5 {
		t.Fatalf("应画出 5 个节点，实际 %d 个：%+v", len(g.Nodes), g.Nodes)
	}
	byID := make(map[string]GraphNode, len(g.Nodes))
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	a := byID[ns.ShapeIRI("ShapeA")+".p1"]
	b := byID[ns.ShapeIRI("ShapeB")+".p1"]
	if a.ID == b.ID {
		t.Fatalf("两个形状的同路径约束必须各自成节点，实际都是 %s", a.ID)
	}
	if !contains(a.Details, "minCount 1") || !contains(b.Details, "maxInclusive 100") {
		t.Fatalf("两个约束节点各自要点不对：A=%+v B=%+v", a.Details, b.Details)
	}
}

func TestShapeGraphEmptyShapes(t *testing.T) {
	ns := mustNamespace(t)
	m, err := ParseManifest([]byte(escapingManifest))
	if err != nil {
		t.Fatalf("清单应通过解析：%v", err)
	}
	g, err := m.ShapeGraph(ns)
	if err != nil {
		t.Fatalf("无形状时不应报错：%v", err)
	}
	if len(g.Nodes) != 0 || len(g.Edges) != 0 {
		t.Fatalf("无形状时应画出空图，实际 nodes=%d edges=%d", len(g.Nodes), len(g.Edges))
	}
}

func hasEdge(g Graph, from, to, kind string) bool {
	for _, e := range g.Edges {
		if e.From == from && e.To == to && e.Kind == kind {
			return true
		}
	}
	return false
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
