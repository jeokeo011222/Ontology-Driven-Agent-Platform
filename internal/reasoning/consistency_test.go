package reasoning

// 一致性检查测试：sameAs 环检出与互补类共现检出（SHACL 联动路径需要 DB，
// 在此以纯图内检查为主）。

import (
	"context"
	"strings"
	"testing"

	"odap/internal/ontology/graphstore"
	"odap/internal/platform/ids"
)

// testTenant 是测试用租户 ID。
func testTenant() ids.ID {
	id, err := ids.Parse("0197c0de-7c0d-7000-8000-000000000001")
	if err != nil {
		panic(err)
	}
	return id
}

// TestSameAsCycles 验证 sameAs 环检出：三点环报 1 个等价类；自环不报；线性链不报。
func TestSameAsCycles(t *testing.T) {
	const sameAs = IRIOWLSameAs
	a := "https://odap.example/data/a"
	b := "https://odap.example/data/b"
	c := "https://odap.example/data/c"
	d := "https://odap.example/data/d"
	e := "https://odap.example/data/e"

	facts := []graphstore.Triple{
		// 三点环 a↔b↔c↔a。
		t3(a, sameAs, b), t3(b, sameAs, a),
		t3(b, sameAs, c), t3(c, sameAs, b),
		t3(c, sameAs, a), t3(a, sameAs, c),
		// 自环（对称规则的正常产物，不应报告）。
		t3(d, sameAs, d),
		// 线性链 e→d（不成环）。
		t3(e, sameAs, d),
	}
	cycles := sameAsCycles(facts)
	if len(cycles) != 1 {
		t.Fatalf("应恰好检出 1 个等价环，实际 %d 个：%v", len(cycles), cycles)
	}
	cycle := cycles[0]
	if len(cycle) != 3 {
		t.Fatalf("环成员应为 3 个，实际 %v", cycle)
	}
	for _, want := range []string{a, b, c} {
		if cycle[0] != want && cycle[1] != want && cycle[2] != want {
			t.Errorf("环成员缺少 %s", want)
		}
	}
}

// TestDisjointViolations 验证互补类共现检出：声明了 disjointWith 且实例双类型化。
func TestDisjointViolations(t *testing.T) {
	const (
		typ          = IRIRDFType
		disjointWith = IRIOWLDisjointWith
	)
	Person := "https://odap.example/ont/demo#Person"
	Robot := "https://odap.example/ont/demo#Robot"
	x := "https://odap.example/data/cyborg"

	facts := []graphstore.Triple{
		t3(Person, disjointWith, Robot),
		t3(x, typ, Person),
		t3(x, typ, Robot),
		// 无关实例（单类型化，不违例）。
		t3("https://odap.example/data/human", typ, Person),
	}
	violations := disjointViolations(facts)
	if len(violations) != 1 {
		t.Fatalf("应恰好检出 1 条互补违例，实际 %d 条：%v", len(violations), violations)
	}
	if !strings.Contains(violations[0].subject, x) {
		t.Errorf("违例主体应为实例 %s，实际 %q", x, violations[0].subject)
	}
}

// TestCheckConsistencyReport 验证 Service.CheckConsistency 对违例图返回
// conforms=false 且列出 sameAs 环问题（MemStore + nil Pool：SHACL 形状解析
// 需要数据库，自动降级跳过，不吞掉图内检查）。
func TestCheckConsistencyReport(t *testing.T) {
	const sameAs = IRIOWLSameAs
	a := "https://odap.example/data/a"
	b := "https://odap.example/data/b"

	store := graphstore.NewMemStore()
	tenant := testTenant()
	graphIRI := "https://odap.example/ont/demo/v-" + tenant.String()
	facts := []graphstore.Triple{
		t3(a, sameAs, b),
		t3(b, sameAs, a),
	}
	if _, err := store.InsertTriples(context.Background(), tenant, graphIRI, facts); err != nil {
		t.Fatalf("插入事实失败：%v", err)
	}

	svc := NewService(store, nil, nil)
	report, err := svc.CheckConsistency(context.Background(), tenant, graphIRI)
	if err != nil {
		t.Fatalf("CheckConsistency 失败：%v", err)
	}
	if report.Conforms {
		t.Fatal("存在 sameAs 环时 conforms 应为 false")
	}
	found := false
	for _, issue := range report.Issues {
		if issue.Kind == IssueSameAsCycle {
			found = true
		}
	}
	if !found {
		t.Errorf("应包含 SAMEAS_CYCLE 问题，实际 %v", report.Issues)
	}
}
