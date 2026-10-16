package reasoning

// 增量更新测试：仅产出与新增事实相关的派生；词法词项解析。

import (
	"testing"

	"odap/internal/ontology/graphstore"
)

// TestIncrementalOnlyDerivesFromNewFacts 验证增量更新只产出由新增事实触发的
// 派生（既有事实已经收敛，不得重复产出）。
func TestIncrementalOnlyDerivesFromNewFacts(t *testing.T) {
	const (
		subClassOf = IRIRDFSSubClassOf
		typ        = IRIRDFType
		A          = "https://odap.example/ont/demo#A"
		B          = "https://odap.example/ont/demo#B"
		old1       = "https://odap.example/data/old1"
		new1       = "https://odap.example/data/new1"
	)
	// 既有事实：A⊑B 与一个旧实例（其类型提升已收敛在 facts 中）。
	facts := []graphstore.Triple{
		t3(A, subClassOf, B),
		t3(old1, typ, A),
	}
	// 先做全量物化，得到收敛后的事实集。
	closure, err := RunForward(BuiltinRules(), facts, 0)
	if err != nil {
		t.Fatalf("全量物化失败：%v", err)
	}
	closed := append(append([]graphstore.Triple{}, facts...), closure.Derived...)

	// 新增事实：新实例 type A —— 增量派生应只包含 new1 type B。
	added := []graphstore.Triple{t3(new1, typ, A)}
	out, err := RunIncremental(BuiltinRules(), closed, added)
	if err != nil {
		t.Fatalf("增量更新失败：%v", err)
	}
	if len(out.Derived) != 1 {
		t.Fatalf("期望恰好 1 条增量派生，实际 %d 条：%v", len(out.Derived), out.Derived)
	}
	if !hasTriple(out.Derived, new1, typ, B) {
		t.Errorf("增量派生应包含 (new1 type B)，实际 %v", out.Derived)
	}
	if hasTriple(out.Derived, old1, typ, B) {
		t.Error("增量派生不得重复产出既有事实的结论")
	}
}

// TestIncrementalEmpty 验证空新增事实直接返回空结果。
func TestIncrementalEmpty(t *testing.T) {
	out, err := RunIncremental(BuiltinRules(), []graphstore.Triple{}, nil)
	if err != nil {
		t.Fatalf("空增量应成功返回，实际错误：%v", err)
	}
	if len(out.Derived) != 0 {
		t.Errorf("空增量不应有派生，实际 %d 条", len(out.Derived))
	}
}

// TestTermLexicalRoundTrip 验证词法↔词项往返（IRI/字面值/带 datatype/带 lang/空白节点）。
func TestTermLexicalRoundTrip(t *testing.T) {
	cases := []string{
		"https://odap.example/ont/demo#A",
		`"hello"`,
		`"42"^^<http://www.w3.org/2001/XMLSchema#integer>`,
		`"你好"@zh-CN`,
		"_:b1",
	}
	for _, lex := range cases {
		term, err := termFromLexical(lex)
		if err != nil {
			t.Fatalf("解析 %s 失败：%v", lex, err)
		}
		if got := termToLexical(term); got != lex {
			t.Errorf("往返不一致：输入 %s → 词项 → 输出 %s", lex, got)
		}
	}
}

// TestTriplesFromLexical 验证词法三元组解析与错误定位。
func TestTriplesFromLexical(t *testing.T) {
	got, err := TriplesFromLexical([]LexicalTriple{
		{Subject: "https://odap.example/data/i1", Predicate: IRIRDFType, Object: "https://odap.example/ont/demo#A"},
	})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(got) != 1 || got[0].S.Lexical != "https://odap.example/data/i1" {
		t.Fatalf("解析结果不符：%v", got)
	}
	if _, err := TriplesFromLexical([]LexicalTriple{{Subject: "", Predicate: "p", Object: "o"}}); err == nil {
		t.Error("空主语应被拒绝")
	}
}

// TestFactKindOnDerived 验证引擎产出的派生三元组带 DERIVED 标记（写回图存储
// 时以 fact_kind 落库， asserted/derived 可区分）。
func TestFactKindOnDerived(t *testing.T) {
	A := "https://odap.example/ont/demo#A"
	B := "https://odap.example/ont/demo#B"
	i := "https://odap.example/data/i1"
	// 单条 subClassOf 无法触发任何规则，需配合实例类型才能产出派生。
	facts := []graphstore.Triple{
		t3(A, IRIRDFSSubClassOf, B),
		t3(i, IRIRDFType, A),
	}
	res, err := RunForward(BuiltinRules(), facts, 0)
	if err != nil {
		t.Fatalf("RunForward 失败：%v", err)
	}
	if len(res.Derived) == 0 {
		t.Fatal("应有派生事实")
	}
	for _, tr := range res.Derived {
		if tr.FactKind != graphstore.FactKindDerived {
			t.Errorf("派生三元组 FactKind 应为 DERIVED，实际 %q", tr.FactKind)
		}
	}
}
