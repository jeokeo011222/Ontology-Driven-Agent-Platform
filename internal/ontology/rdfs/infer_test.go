package rdfs

import (
	"context"
	"fmt"
	"testing"

	"odap/internal/ontology/graphstore"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

const testGraph = "https://ex/graph/instance"

func tIRI(s string) graphstore.Term { return graphstore.Term{Kind: graphstore.KindIRI, Lexical: s} }
func tLit(s string) graphstore.Term {
	return graphstore.Term{Kind: graphstore.KindLiteral, Lexical: s}
}

func tr(s, p, o graphstore.Term) graphstore.Triple {
	return graphstore.Triple{S: s, P: p, O: o, GraphIRI: testGraph}
}

func seed(t *testing.T, m *graphstore.MemStore, tenant ids.ID, triples []graphstore.Triple) {
	t.Helper()
	if _, err := m.InsertTriples(context.Background(), tenant, testGraph, triples); err != nil {
		t.Fatalf("seed 失败: %v", err)
	}
}

func hasFact(t *testing.T, m *graphstore.MemStore, tenant ids.ID, s, p, o graphstore.Term) bool {
	t.Helper()
	got, err := m.Match(context.Background(), tenant, graphstore.Pattern{
		S: &s, P: &p, O: &o, GraphIRI: testGraph,
	})
	if err != nil {
		t.Fatalf("Match 失败: %v", err)
	}
	return len(got) > 0
}

func TestInferRuleTable(t *testing.T) {
	// 每个用例只启用被测规则，从而隔离该规则的推导效果。
	tests := []struct {
		rule       string
		base       []graphstore.Triple
		want       [][3]graphstore.Term
		wantAbsent [][3]graphstore.Term
	}{
		{
			rule: "rdfs2",
			base: []graphstore.Triple{
				tr(tIRI("https://ex/manages"), tIRI(RDFSDomain), tIRI("https://ex/Manager")),
				tr(tIRI("https://ex/alice"), tIRI("https://ex/manages"), tIRI("https://ex/bob")),
			},
			want: [][3]graphstore.Term{
				{tIRI("https://ex/alice"), tIRI(RDFType), tIRI("https://ex/Manager")},
			},
		},
		{
			rule: "rdfs3",
			base: []graphstore.Triple{
				tr(tIRI("https://ex/manages"), tIRI(RDFSRange), tIRI("https://ex/Employee")),
				tr(tIRI("https://ex/alice"), tIRI("https://ex/manages"), tIRI("https://ex/bob")),
			},
			want: [][3]graphstore.Term{
				{tIRI("https://ex/bob"), tIRI(RDFType), tIRI("https://ex/Employee")},
			},
		},
		{
			rule: "rdfs4a",
			base: []graphstore.Triple{
				tr(tIRI("https://ex/alice"), tIRI("https://ex/knows"), tIRI("https://ex/bob")),
			},
			want: [][3]graphstore.Term{
				{tIRI("https://ex/alice"), tIRI(RDFType), tIRI(RDFSResource)},
			},
		},
		{
			rule: "rdfs4b",
			base: []graphstore.Triple{
				tr(tIRI("https://ex/alice"), tIRI("https://ex/knows"), tIRI("https://ex/bob")),
			},
			want: [][3]graphstore.Term{
				{tIRI("https://ex/bob"), tIRI(RDFType), tIRI(RDFSResource)},
			},
		},
		{
			rule: "rdfs4b",
			base: []graphstore.Triple{
				tr(tIRI("https://ex/alice"), tIRI("https://ex/name"), tLit("Alice")),
			},
			wantAbsent: [][3]graphstore.Term{
				{tLit("Alice"), tIRI(RDFType), tIRI(RDFSResource)},
			},
		},
		{
			rule: "rdfs5",
			base: []graphstore.Triple{
				tr(tIRI("https://ex/a"), tIRI(RDFSSubPropertyOf), tIRI("https://ex/b")),
				tr(tIRI("https://ex/b"), tIRI(RDFSSubPropertyOf), tIRI("https://ex/c")),
			},
			want: [][3]graphstore.Term{
				{tIRI("https://ex/a"), tIRI(RDFSSubPropertyOf), tIRI("https://ex/c")},
			},
		},
		{
			rule: "rdfs6",
			base: []graphstore.Triple{
				tr(tIRI("https://ex/p"), tIRI(RDFType), tIRI(RDFProperty)),
			},
			want: [][3]graphstore.Term{
				{tIRI("https://ex/p"), tIRI(RDFSSubPropertyOf), tIRI("https://ex/p")},
			},
		},
		{
			rule: "rdfs7",
			base: []graphstore.Triple{
				tr(tIRI("https://ex/a"), tIRI(RDFSSubPropertyOf), tIRI("https://ex/b")),
				tr(tIRI("https://ex/x"), tIRI("https://ex/a"), tIRI("https://ex/y")),
			},
			want: [][3]graphstore.Term{
				{tIRI("https://ex/x"), tIRI("https://ex/b"), tIRI("https://ex/y")},
			},
		},
		{
			rule: "rdfs8",
			base: []graphstore.Triple{
				tr(tIRI("https://ex/Employee"), tIRI(RDFType), tIRI(RDFSClass)),
			},
			want: [][3]graphstore.Term{
				{tIRI("https://ex/Employee"), tIRI(RDFSSubClassOf), tIRI(RDFSResource)},
			},
		},
		{
			rule: "rdfs9",
			base: []graphstore.Triple{
				tr(tIRI("https://ex/FinanceEmployee"), tIRI(RDFSSubClassOf), tIRI("https://ex/Employee")),
				tr(tIRI("https://ex/e1024"), tIRI(RDFType), tIRI("https://ex/FinanceEmployee")),
			},
			want: [][3]graphstore.Term{
				{tIRI("https://ex/e1024"), tIRI(RDFType), tIRI("https://ex/Employee")},
			},
		},
		{
			rule: "rdfs10",
			base: []graphstore.Triple{
				tr(tIRI("https://ex/Employee"), tIRI(RDFType), tIRI(RDFSClass)),
			},
			want: [][3]graphstore.Term{
				{tIRI("https://ex/Employee"), tIRI(RDFSSubClassOf), tIRI("https://ex/Employee")},
			},
		},
		{
			rule: "rdfs11",
			base: []graphstore.Triple{
				tr(tIRI("https://ex/a"), tIRI(RDFSSubClassOf), tIRI("https://ex/b")),
				tr(tIRI("https://ex/b"), tIRI(RDFSSubClassOf), tIRI("https://ex/c")),
			},
			want: [][3]graphstore.Term{
				{tIRI("https://ex/a"), tIRI(RDFSSubClassOf), tIRI("https://ex/c")},
			},
		},
		{
			rule: "rdfs12",
			base: []graphstore.Triple{
				tr(tIRI("https://ex/p"), tIRI(RDFType), tIRI(RDFSContainerMembershipProperty)),
			},
			want: [][3]graphstore.Term{
				{tIRI("https://ex/p"), tIRI(RDFSSubPropertyOf), tIRI(RDFSMember)},
			},
		},
		{
			rule: "rdfs13",
			base: []graphstore.Triple{
				tr(tIRI("https://ex/MyDatatype"), tIRI(RDFType), tIRI(RDFSDatatype)),
			},
			want: [][3]graphstore.Term{
				{tIRI("https://ex/MyDatatype"), tIRI(RDFSSubClassOf), tIRI(RDFSLiteral)},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.rule, func(t *testing.T) {
			tenant := ids.New()
			m := graphstore.NewMemStore()
			seed(t, m, tenant, tt.base)

			stats, err := Infer(m, tenant, testGraph, Options{EnabledRules: map[string]bool{tt.rule: true}})
			if err != nil {
				t.Fatalf("Infer 失败: %v", err)
			}
			if stats.ByRule[tt.rule] == 0 && len(tt.want) > 0 {
				t.Fatalf("规则 %s 未产生任何推导，stats=%+v", tt.rule, stats)
			}
			for _, w := range tt.want {
				if !hasFact(t, m, tenant, w[0], w[1], w[2]) {
					t.Fatalf("期望推导出 %v %v %v", w[0], w[1], w[2])
				}
			}
			for _, w := range tt.wantAbsent {
				if hasFact(t, m, tenant, w[0], w[1], w[2]) {
					t.Fatalf("不应推导出 %v %v %v", w[0], w[1], w[2])
				}
			}
		})
	}
}

func TestInferSubClassTransitiveClosure(t *testing.T) {
	tenant := ids.New()
	m := graphstore.NewMemStore()
	seed(t, m, tenant, []graphstore.Triple{
		tr(tIRI("https://ex/FinanceEmployee"), tIRI(RDFSSubClassOf), tIRI("https://ex/Employee")),
		tr(tIRI("https://ex/Employee"), tIRI(RDFSSubClassOf), tIRI("https://ex/Person")),
		tr(tIRI("https://ex/e1024"), tIRI(RDFType), tIRI("https://ex/FinanceEmployee")),
	})

	stats, err := Infer(m, tenant, testGraph, Options{})
	if err != nil {
		t.Fatalf("Infer 失败: %v", err)
	}
	// rdfs11 计算子类传递闭包，rdfs9 应用到实例（第十三篇 §12.3）。
	for _, class := range []string{"https://ex/Employee", "https://ex/Person"} {
		if !hasFact(t, m, tenant, tIRI("https://ex/e1024"), tIRI(RDFType), tIRI(class)) {
			t.Fatalf("期望推导出 e1024 属于 %s", class)
		}
	}
	if !hasFact(t, m, tenant, tIRI("https://ex/FinanceEmployee"), tIRI(RDFSSubClassOf), tIRI("https://ex/Person")) {
		t.Fatal("期望推导出 FinanceEmployee subClassOf Person")
	}
	if !stats.Completed {
		t.Fatalf("应达到不动点，stats=%+v", stats)
	}
}

func TestInferFixpointTerminatesAndIsIdempotent(t *testing.T) {
	tenant := ids.New()
	m := graphstore.NewMemStore()
	seed(t, m, tenant, []graphstore.Triple{
		tr(tIRI("https://ex/a"), tIRI(RDFSSubClassOf), tIRI("https://ex/b")),
		tr(tIRI("https://ex/b"), tIRI(RDFSSubClassOf), tIRI("https://ex/c")),
		tr(tIRI("https://ex/x"), tIRI(RDFType), tIRI("https://ex/a")),
	})

	first, err := Infer(m, tenant, testGraph, Options{})
	if err != nil {
		t.Fatalf("Infer 失败: %v", err)
	}
	if !first.Completed || first.StopReason != StopFixpoint {
		t.Fatalf("首次应到达不动点，stats=%+v", first)
	}
	if first.Inferred == 0 {
		t.Fatal("首次应产生派生事实")
	}

	second, err := Infer(m, tenant, testGraph, Options{})
	if err != nil {
		t.Fatalf("第二次 Infer 失败: %v", err)
	}
	if second.Inferred != 0 {
		t.Fatalf("幂等：第二次不应有新增，实际 %d", second.Inferred)
	}
	if !second.Completed {
		t.Fatalf("第二次应仍到达不动点，stats=%+v", second)
	}
}

func TestInferStopsAtMaxInferred(t *testing.T) {
	tenant := ids.New()
	m := graphstore.NewMemStore()
	// 构造一条较长的子类链 + 实例，可产生大量推导。
	base := []graphstore.Triple{
		tr(tIRI("https://ex/C0"), tIRI(RDFType), tIRI(RDFSClass)),
		tr(tIRI("https://ex/e"), tIRI(RDFType), tIRI("https://ex/C0")),
	}
	for i := 0; i < 10; i++ {
		base = append(base, tr(tIRI(fmt.Sprintf("https://ex/C%d", i)), tIRI(RDFSSubClassOf), tIRI(fmt.Sprintf("https://ex/C%d", i+1))))
	}
	seed(t, m, tenant, base)

	stats, err := Infer(m, tenant, testGraph, Options{MaxInferred: 1})
	if err != nil {
		t.Fatalf("Infer 失败: %v", err)
	}
	if !stats.Truncated || stats.StopReason != StopMaxInferred {
		t.Fatalf("应在 MaxInferred 处安全停止，stats=%+v", stats)
	}
	if stats.Inferred > 1 {
		t.Fatalf("派生态数量应被限制在 1，实际 %d", stats.Inferred)
	}
}

func TestInferEnabledRulesSubset(t *testing.T) {
	tenant := ids.New()
	m := graphstore.NewMemStore()
	seed(t, m, tenant, []graphstore.Triple{
		tr(tIRI("https://ex/A"), tIRI(RDFSSubClassOf), tIRI("https://ex/B")),
		tr(tIRI("https://ex/B"), tIRI(RDFSSubClassOf), tIRI("https://ex/C")),
		tr(tIRI("https://ex/x"), tIRI(RDFType), tIRI("https://ex/A")),
	})

	// 仅启用 rdfs9：应推出实例的新类型，但不做子类传递（rdfs11 关闭）。
	stats, err := Infer(m, tenant, testGraph, Options{EnabledRules: map[string]bool{"rdfs9": true}})
	if err != nil {
		t.Fatalf("Infer 失败: %v", err)
	}
	if !hasFact(t, m, tenant, tIRI("https://ex/x"), tIRI(RDFType), tIRI("https://ex/C")) {
		t.Fatal("rdfs9 应沿已知子类关系推出 x 属于 C")
	}
	if hasFact(t, m, tenant, tIRI("https://ex/A"), tIRI(RDFSSubClassOf), tIRI("https://ex/C")) {
		t.Fatal("rdfs11 未启用时不应推导出 A subClassOf C")
	}
	if stats.ByRule["rdfs11"] != 0 {
		t.Fatalf("rdfs11 未启用，计数应为 0，实际 %d", stats.ByRule["rdfs11"])
	}
}

func TestInferRejectsUnknownRule(t *testing.T) {
	tenant := ids.New()
	m := graphstore.NewMemStore()
	_, err := Infer(m, tenant, testGraph, Options{EnabledRules: map[string]bool{"rdfs99": true}})
	appErr := errors.As(err)
	if appErr.Code != errors.CodeOntologyValidationFailed {
		t.Fatalf("错误码应为 %s，实际 %s", errors.CodeOntologyValidationFailed, appErr.Code)
	}
}

func TestInferOptionValidation(t *testing.T) {
	tenant := ids.New()
	m := graphstore.NewMemStore()

	if _, err := Infer(nil, tenant, testGraph, Options{}); errors.As(err).Code != errors.CodeInternal {
		t.Fatalf("nil store 应返回 %s，实际 %s", errors.CodeInternal, errors.As(err).Code)
	}
	if _, err := Infer(m, ids.ID{}, testGraph, Options{}); errors.As(err).Code != errors.CodeValidationFailed {
		t.Fatalf("零值 tenant 应返回 %s", errors.CodeValidationFailed)
	}
	if _, err := Infer(m, tenant, "", Options{}); errors.As(err).Code != errors.CodeValidationFailed {
		t.Fatalf("空 graph 应返回 %s", errors.CodeValidationFailed)
	}
	if _, err := Infer(m, tenant, testGraph, Options{MaxIterations: -1}); errors.As(err).Code != errors.CodeValidationFailed {
		t.Fatal("负 MaxIterations 应返回错误")
	}
}
