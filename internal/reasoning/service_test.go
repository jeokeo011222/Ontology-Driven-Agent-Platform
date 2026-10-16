package reasoning

// Service 集成测试（MemStore + nil Pool：仅内置规则路径）。

import (
	"context"
	"testing"

	"odap/internal/ontology/graphstore"
	"odap/internal/platform/ids"
)

// TestServiceForwardChainWritesDerived 验证 Service.ForwardChain 把派生三元组
// 以 FactKind=DERIVED 写回图存储（MemStore），且二次执行幂等（不再产出）。
func TestServiceForwardChainWritesDerived(t *testing.T) {
	const (
		subClassOf = IRIRDFSSubClassOf
		typ        = IRIRDFType
		A          = "https://odap.example/ont/demo#A"
		B          = "https://odap.example/ont/demo#B"
		inst       = "https://odap.example/data/i1"
	)
	store := graphstore.NewMemStore()
	tenant := testTenant()
	graphIRI := "https://odap.example/ont/demo/v-" + tenant.String()

	if _, err := store.InsertTriples(context.Background(), tenant, graphIRI, []graphstore.Triple{
		t3(A, subClassOf, B),
		t3(inst, typ, A),
	}); err != nil {
		t.Fatalf("插入事实失败：%v", err)
	}

	svc := NewService(store, nil, nil)
	res, err := svc.ForwardChain(context.Background(), tenant, graphIRI, nil, 0)
	if err != nil {
		t.Fatalf("ForwardChain 失败：%v", err)
	}
	if len(res.Derived) != 1 || !hasTriple(res.Derived, inst, typ, B) {
		t.Fatalf("期望派生 (inst type B)，实际 %v", res.Derived)
	}

	// 落库校验：派生事实 fact_kind=DERIVED。
	all, err := store.Match(context.Background(), tenant, graphstore.Pattern{GraphIRI: graphIRI})
	if err != nil {
		t.Fatalf("读回失败：%v", err)
	}
	foundDerived := false
	for _, tr := range all {
		if tr.S.Lexical == inst && tr.P.Lexical == typ && tr.O.Lexical == B {
			if tr.FactKind != graphstore.FactKindDerived {
				t.Errorf("落库的派生三元组 fact_kind 应为 DERIVED，实际 %q", tr.FactKind)
			}
			foundDerived = true
		}
	}
	if !foundDerived {
		t.Fatal("图存储中未找到派生三元组")
	}

	// 幂等：再次执行不产出新事实。
	res2, err := svc.ForwardChain(context.Background(), tenant, graphIRI, nil, 0)
	if err != nil {
		t.Fatalf("二次 ForwardChain 失败：%v", err)
	}
	if len(res2.Derived) != 0 {
		t.Errorf("二次执行应无新派生，实际 %d 条", len(res2.Derived))
	}
}

// TestServiceBackwardChain 验证 Service.BackwardChain（MemStore）。
func TestServiceBackwardChain(t *testing.T) {
	const (
		sameAs = IRIOWLSameAs
		a      = "https://odap.example/data/a"
		b      = "https://odap.example/data/b"
	)
	store := graphstore.NewMemStore()
	tenant := testTenant()
	graphIRI := "https://odap.example/ont/demo/v-" + tenant.String()
	if _, err := store.InsertTriples(context.Background(), tenant, graphIRI, []graphstore.Triple{
		t3(a, sameAs, b),
	}); err != nil {
		t.Fatalf("插入事实失败：%v", err)
	}
	svc := NewService(store, nil, nil)
	res, err := svc.BackwardChain(context.Background(), tenant, graphIRI,
		TriplePattern{Subject: a, Predicate: sameAs, Object: b}, nil)
	if err != nil {
		t.Fatalf("BackwardChain 失败：%v", err)
	}
	if !res.Proved {
		t.Fatal("直接事实目标应可证")
	}
}

// TestServiceHealthCheckNilPool 验证 Pool 未装配时健康检查仅计内置规则。
func TestServiceHealthCheckNilPool(t *testing.T) {
	svc := NewService(graphstore.NewMemStore(), nil, nil)
	status, count, err := svc.HealthCheck(context.Background(), ids.ID{})
	if err != nil {
		t.Fatalf("HealthCheck 失败：%v", err)
	}
	if status != "SERVING" || count != int64(len(builtinRules)) {
		t.Fatalf("status=%s rules_loaded=%d（期望 SERVING / %d）", status, count, len(builtinRules))
	}
}
