package semantic

import (
	"context"
	"testing"

	"odap/internal/ontology/graphstore"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

const (
	qGraph  = "https://ex/graph/instance"
	exA     = "https://ex/alice"
	exB     = "https://ex/bob"
	exC     = "https://ex/carol"
	exKnown = "https://ex/knows"
	exName  = "https://ex/name"
	exRole  = "https://ex/role"
)

func iTerm(s string) graphstore.Term { return graphstore.Term{Kind: graphstore.KindIRI, Lexical: s} }
func lit(s string) graphstore.Term {
	return graphstore.Term{Kind: graphstore.KindLiteral, Lexical: s}
}

func seed(t *testing.T) (*graphstore.MemStore, ids.ID) {
	t.Helper()
	m := graphstore.NewMemStore()
	tenant := ids.New()
	triples := []graphstore.Triple{
		{S: iTerm(exA), P: iTerm(exKnown), O: iTerm(exB), GraphIRI: qGraph},
		{S: iTerm(exA), P: iTerm(exKnown), O: iTerm(exC), GraphIRI: qGraph},
		{S: iTerm(exA), P: iTerm(exName), O: lit("Alice"), GraphIRI: qGraph},
		{S: iTerm(exB), P: iTerm(exName), O: lit("Bob"), GraphIRI: qGraph},
		{S: iTerm(exC), P: iTerm(exName), O: lit("Carol"), GraphIRI: qGraph},
		{S: iTerm(exB), P: iTerm(exRole), O: lit("manager"), GraphIRI: qGraph},
	}
	if _, err := m.InsertTriples(context.Background(), tenant, qGraph, triples); err != nil {
		t.Fatalf("seed 失败: %v", err)
	}
	return m, tenant
}

func TestExecuteSinglePattern(t *testing.T) {
	m, tenant := seed(t)
	svc := &Service{Store: m}
	res, err := svc.Execute(context.Background(), tenant, Query{
		GraphIRI: qGraph,
		Where:    []Clause{{Subject: exA, Predicate: exKnown, Object: "?who"}},
		Select:   []string{"who"},
	})
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("应匹配 2 行，实际 %d：%+v", len(res.Rows), res.Rows)
	}
	got := map[string]bool{}
	for _, r := range res.Rows {
		got[r["who"].Lexical] = true
	}
	if !got[exB] || !got[exC] {
		t.Fatalf("绑定结果不含 bob/carol：%+v", res.Rows)
	}
}

func TestExecuteTwoPatternJoin(t *testing.T) {
	m, tenant := seed(t)
	svc := &Service{Store: m}
	res, err := svc.Execute(context.Background(), tenant, Query{
		GraphIRI: qGraph,
		Where: []Clause{
			{Subject: exA, Predicate: exKnown, Object: "?friend"},
			{Subject: "?friend", Predicate: exName, Object: "?friendName"},
		},
		Select: []string{"?friendName"},
	})
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("连接应产生 2 行，实际 %d：%+v", len(res.Rows), res.Rows)
	}
	names := map[string]bool{}
	for _, r := range res.Rows {
		names[r["friendName"].Lexical] = true
	}
	if !names["Bob"] || !names["Carol"] {
		t.Fatalf("连接结果应含 Bob/Carol，实际 %+v", res.Rows)
	}
}

func TestExecuteVariableConsistency(t *testing.T) {
	m, tenant := seed(t)
	svc := &Service{Store: m}
	res, err := svc.Execute(context.Background(), tenant, Query{
		GraphIRI: qGraph,
		Where: []Clause{
			{Subject: "?x", Predicate: exName, Object: "?n"},
			{Subject: "?x", Predicate: exRole, Object: "?r"},
		},
		Select: []string{"x", "n", "r"},
	})
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("同一变量应被一致绑定，应得 1 行，实际 %d：%+v", len(res.Rows), res.Rows)
	}
	row := res.Rows[0]
	if row["x"].Lexical != exB || row["n"].Lexical != "Bob" || row["r"].Lexical != "manager" {
		t.Fatalf("绑定不正确: %+v", row)
	}
}

func TestExecuteLimitAndTruncation(t *testing.T) {
	m, tenant := seed(t)
	svc := &Service{Store: m}

	res, err := svc.Execute(context.Background(), tenant, Query{
		GraphIRI: qGraph,
		Where:    []Clause{{Subject: "?s", Predicate: exKnown, Object: "?o"}},
		Select:   []string{"s", "o"},
		Limit:    1,
	})
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	if len(res.Rows) != 1 || !res.Truncated {
		t.Fatalf("Limit=1 应截断为 1 行且 Truncated=true，实际 %+v", res)
	}

	// Limit=0 使用默认上限，不截断。
	res2, err := svc.Execute(context.Background(), tenant, Query{
		GraphIRI: qGraph,
		Where:    []Clause{{Subject: "?s", Predicate: exKnown, Object: "?o"}},
		Select:   []string{"s", "o"},
	})
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	if len(res2.Rows) != 2 || res2.Truncated {
		t.Fatalf("默认 Limit 不应截断，实际 %+v", res2)
	}
}

func TestExecuteRejectsInvalidQueries(t *testing.T) {
	m, tenant := seed(t)
	svc := &Service{Store: m, MaxLimit: 10}

	tests := []struct {
		name string
		q    Query
	}{
		{"空 GraphIRI", Query{Where: []Clause{{Subject: exA}}}},
		{"非法 GraphIRI", Query{GraphIRI: "not an iri", Where: []Clause{{Subject: exA}}}},
		{"空 Where", Query{GraphIRI: qGraph}},
		{"全空 Clause", Query{GraphIRI: qGraph, Where: []Clause{{}}}},
		{"Select 未知变量", Query{GraphIRI: qGraph, Where: []Clause{{Subject: exA, Predicate: exKnown, Object: "?who"}}, Select: []string{"?nope"}}},
		{"Limit 超上限", Query{GraphIRI: qGraph, Where: []Clause{{Subject: exA}}, Limit: 11}},
		{"非法变量名", Query{GraphIRI: qGraph, Where: []Clause{{Subject: "?1bad"}}}},
		{"常量非 IRI", Query{GraphIRI: qGraph, Where: []Clause{{Subject: "alice"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.Execute(context.Background(), tenant, tt.q)
			if err == nil {
				t.Fatal("应返回错误")
			}
			if appErr := errors.As(err); appErr.Code != errors.CodeSemanticQueryInvalid {
				t.Fatalf("错误码应为 %s，实际 %s（%v）", errors.CodeSemanticQueryInvalid, appErr.Code, err)
			}
		})
	}
}

func TestExecuteParameterValidation(t *testing.T) {
	m, tenant := seed(t)
	if _, err := (&Service{}).Execute(context.Background(), tenant, Query{}); errors.As(err).Code != errors.CodeInternal {
		t.Fatalf("nil store 应返回 %s", errors.CodeInternal)
	}
	svc := &Service{Store: m}
	if _, err := svc.Execute(context.Background(), ids.ID{}, Query{GraphIRI: qGraph, Where: []Clause{{Subject: exA}}}); errors.As(err).Code != errors.CodeValidationFailed {
		t.Fatalf("零值 tenant 应返回 %s", errors.CodeValidationFailed)
	}
}

func TestExecuteDefaultSelectProjectsAllVariables(t *testing.T) {
	m, tenant := seed(t)
	svc := &Service{Store: m}
	res, err := svc.Execute(context.Background(), tenant, Query{
		GraphIRI: qGraph,
		Where:    []Clause{{Subject: exA, Predicate: exKnown, Object: "?who"}},
	})
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("应返回 2 行，实际 %d", len(res.Rows))
	}
	for _, r := range res.Rows {
		if _, ok := r["who"]; !ok {
			t.Fatalf("默认投影应含变量 who，实际 %+v", r)
		}
	}
}
