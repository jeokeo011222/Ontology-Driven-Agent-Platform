package reasoning

// 前向链测试：不动点收敛、类型提升、非收敛显式报错。

import (
	"strings"
	"testing"

	"odap/internal/ontology/graphstore"
)

// iri 构造 IRI 词项。
func iri(s string) graphstore.Term { return graphstore.Term{Kind: graphstore.KindIRI, Lexical: s} }

// t3 构造 IRI 三元组。
func t3(s, p, o string) graphstore.Triple {
	return graphstore.Triple{S: iri(s), P: iri(p), O: iri(o)}
}

// hasTriple 报告三元组集合中是否包含指定 SPO。
func hasTriple(ts []graphstore.Triple, s, p, o string) bool {
	for _, t := range ts {
		if t.S.Lexical == s && t.P.Lexical == p && t.O.Lexical == o {
			return true
		}
	}
	return false
}

// TestForwardChainFixpoint 验证 subclass 传递 + 类型提升在不动点收敛：
// A⊑B⊑C 与 i type A ⇒ 派生 a⊑C、i type B、i type C，且重跑不再产出新事实。
func TestForwardChainFixpoint(t *testing.T) {
	const (
		subClassOf = IRIRDFSSubClassOf
		rdfType    = IRIRDFType
		A          = "https://odap.example/ont/demo#A"
		B          = "https://odap.example/ont/demo#B"
		C          = "https://odap.example/ont/demo#C"
		inst       = "https://odap.example/data/i1"
	)
	facts := []graphstore.Triple{
		t3(A, subClassOf, B),
		t3(B, subClassOf, C),
		t3(inst, rdfType, A),
	}
	rules := BuiltinRules()

	res, err := RunForward(rules, facts, 0)
	if err != nil {
		t.Fatalf("RunForward 失败：%v", err)
	}
	// 期望派生：A⊑C（传递）、i type B、i type C（类型提升）。
	for _, want := range [][3]string{
		{A, subClassOf, C},
		{inst, rdfType, B},
		{inst, rdfType, C},
	} {
		if !hasTriple(res.Derived, want[0], want[1], want[2]) {
			t.Errorf("派生集缺少 (%s %s %s)；实际 %d 条", want[0], want[1], want[2], len(res.Derived))
		}
	}
	if len(res.Derived) != 3 {
		t.Errorf("期望恰好 3 条派生，实际 %d 条：%v", len(res.Derived), res.Derived)
	}
	if res.Iterations < 2 {
		t.Errorf("三层层次至少需要 2 轮迭代，实际 %d 轮", res.Iterations)
	}
	// 不动点稳定性：把派生并入事实重跑，不得再产出新事实。
	closed := append(append([]graphstore.Triple{}, facts...), res.Derived...)
	res2, err := RunForward(rules, closed, 0)
	if err != nil {
		t.Fatalf("不动点重跑失败：%v", err)
	}
	if len(res2.Derived) != 0 {
		t.Errorf("不动点未收敛：重跑仍产出 %d 条新事实", len(res2.Derived))
	}
}

// TestForwardChainSymmetricProperty 验证对称属性规则（owl-symmetric）。
func TestForwardChainSymmetricProperty(t *testing.T) {
	const (
		P   = "https://odap.example/ont/demo#knows"
		x   = "https://odap.example/data/x"
		y   = "https://odap.example/data/y"
		typ = IRIRDFType
	)
	facts := []graphstore.Triple{
		t3(P, typ, IRIOWLSymmetricProperty),
		t3(x, P, y),
	}
	res, err := RunForward(BuiltinRules(), facts, 0)
	if err != nil {
		t.Fatalf("RunForward 失败：%v", err)
	}
	if !hasTriple(res.Derived, y, P, x) {
		t.Fatalf("对称属性未派生 (%s %s %s)", y, P, x)
	}
}

// TestForwardChainNotConverged 验证迭代超限时显式报错「推理未收敛」，
// 绝不静默截断：max_iterations=1 不足以让三层层次链闭合。
func TestForwardChainNotConverged(t *testing.T) {
	const (
		subClassOf = IRIRDFSSubClassOf
		rdfType    = IRIRDFType
		A          = "https://odap.example/ont/demo#A"
		B          = "https://odap.example/ont/demo#B"
		C          = "https://odap.example/ont/demo#C"
		inst       = "https://odap.example/data/i1"
	)
	facts := []graphstore.Triple{
		t3(A, subClassOf, B),
		t3(B, subClassOf, C),
		t3(inst, rdfType, A),
	}
	_, err := RunForward(BuiltinRules(), facts, 1)
	if err == nil {
		t.Fatal("期望「推理未收敛」错误，实际 nil")
	}
	if !strings.Contains(err.Error(), "推理未收敛") {
		t.Errorf("错误信息应包含「推理未收敛」，实际：%v", err)
	}
}

// TestForwardChainDomainRange 验证 domain/range 推断（rdfs2/rdfs3）。
func TestForwardChainDomainRange(t *testing.T) {
	const (
		domain = IRIRDFSDomain
		rangeP = IRIRDFSRange
		typ    = IRIRDFType
		P      = "https://odap.example/ont/demo#worksFor"
		Comp   = "https://odap.example/ont/demo#Company"
		Pers   = "https://odap.example/ont/demo#Person"
		x      = "https://odap.example/data/emp1"
		y      = "https://odap.example/data/co1"
	)
	facts := []graphstore.Triple{
		t3(P, domain, Pers),
		t3(P, rangeP, Comp),
		t3(x, P, y),
	}
	res, err := RunForward(BuiltinRules(), facts, 0)
	if err != nil {
		t.Fatalf("RunForward 失败：%v", err)
	}
	if !hasTriple(res.Derived, x, typ, Pers) {
		t.Error("domain 推断缺失：x type Person")
	}
	if !hasTriple(res.Derived, y, typ, Comp) {
		t.Error("range 推断缺失：y type Company")
	}
}
