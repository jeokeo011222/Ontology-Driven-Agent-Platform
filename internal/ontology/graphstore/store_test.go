package graphstore

import (
	"context"
	"testing"
	"time"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

func mkIRI(s string) Term   { return Term{Kind: KindIRI, Lexical: s} }
func mkBlank(s string) Term { return Term{Kind: KindBlankNode, Lexical: s} }
func mkLit(lex, dt string) Term {
	return Term{Kind: KindLiteral, Lexical: lex, Datatype: dt}
}

func TestCanonicalDigest(t *testing.T) {
	base := mkIRI("https://ex/Person")
	if base.CanonicalDigest() != base.CanonicalDigest() {
		t.Fatal("摘要应稳定")
	}
	if mkIRI("https://ex/Person").CanonicalDigest() == mkIRI("https://ex/Employee").CanonicalDigest() {
		t.Fatal("不同 lexical 的摘要不应相同")
	}
	// datatype 与 lang 位于不同字段，摘要必须区分（0x1F 分隔避免拼接歧义）。
	a := Term{Kind: KindLiteral, Lexical: "x", Datatype: "en"}
	b := Term{Kind: KindLiteral, Lexical: "x", Lang: "en"}
	if a.CanonicalDigest() == b.CanonicalDigest() {
		t.Fatal("datatype 与 lang 混淆时摘要不应相同")
	}
	// 拼接歧义攻击：("a","b") 与 ("ab","") 必须区分。
	c := Term{Kind: KindIRI, Lexical: "a", Datatype: "b"}
	d := Term{Kind: KindIRI, Lexical: "ab"}
	if c.CanonicalDigest() == d.CanonicalDigest() {
		t.Fatal("分隔符缺失会导致摘要碰撞")
	}
}

func TestEnsureGraphIdempotent(t *testing.T) {
	ctx := context.Background()
	tenant := ids.New()
	m := NewMemStore()

	id1, err := m.EnsureGraph(ctx, tenant, "https://ex/g", "INSTANCE", "v1")
	if err != nil {
		t.Fatalf("非预期错误: %v", err)
	}
	id2, err := m.EnsureGraph(ctx, tenant, "https://ex/g", "INSTANCE", "v2")
	if err != nil {
		t.Fatalf("非预期错误: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("幂等建图应返回同一 graph_id，得到 %d 与 %d", id1, id2)
	}
	// 不同租户即使 graphIRI 相同也应得到不同 id。
	idOther, err := m.EnsureGraph(ctx, ids.New(), "https://ex/g", "INSTANCE", "v1")
	if err != nil {
		t.Fatalf("非预期错误: %v", err)
	}
	if idOther == id1 {
		t.Fatal("不同租户不应复用 graph_id")
	}
}

func TestUpsertTermDedup(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()

	id1, err := m.UpsertTerm(ctx, mkIRI("https://ex/Person"))
	if err != nil {
		t.Fatalf("非预期错误: %v", err)
	}
	id2, err := m.UpsertTerm(ctx, mkIRI("https://ex/Person"))
	if err != nil {
		t.Fatalf("非预期错误: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("相同词项应复用 term_id，得到 %d 与 %d", id1, id2)
	}

	id3, err := m.UpsertTerm(ctx, mkIRI("https://ex/Employee"))
	if err != nil {
		t.Fatalf("非预期错误: %v", err)
	}
	if id3 == id1 {
		t.Fatal("不同词项不应复用 term_id")
	}

	idLit1, _ := m.UpsertTerm(ctx, mkLit("1", "http://www.w3.org/2001/XMLSchema#integer"))
	idLit2, _ := m.UpsertTerm(ctx, mkLit("1", "http://www.w3.org/2001/XMLSchema#decimal"))
	if idLit1 == idLit2 {
		t.Fatal("相同 lexical 但不同 datatype 的字面值不应视为同一词项")
	}

	if _, err := m.UpsertTerm(ctx, Term{Kind: 9, Lexical: "x"}); err == nil {
		t.Fatal("非法 term_kind 应返回错误")
	}
	if _, err := m.UpsertTerm(ctx, Term{Kind: KindIRI}); err == nil {
		t.Fatal("空 lexical 应返回错误")
	}
}

func TestInsertTriplesCount(t *testing.T) {
	ctx := context.Background()
	tenant := ids.New()
	graph := "https://ex/g"
	m := NewMemStore()

	person := mkIRI("https://ex/Person")
	named := mkIRI("https://ex/named")
	alice := mkIRI("https://ex/alice")

	facts := []Triple{
		{S: alice, P: named, O: mkLit("Alice", "http://www.w3.org/2001/XMLSchema#string")},
		{S: alice, P: mkIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#type"), O: person},
	}
	n, err := m.InsertTriples(ctx, tenant, graph, facts)
	if err != nil {
		t.Fatalf("非预期错误: %v", err)
	}
	if n != 2 {
		t.Fatalf("首次插入应新增 2 条，实际 %d", n)
	}

	// 重复插入同一条 + 新增一条。
	facts2 := []Triple{facts[0], {S: mkIRI("https://ex/bob"), P: named, O: mkLit("Bob", "http://www.w3.org/2001/XMLSchema#string")}}
	n2, err := m.InsertTriples(ctx, tenant, graph, facts2)
	if err != nil {
		t.Fatalf("非预期错误: %v", err)
	}
	if n2 != 1 {
		t.Fatalf("第二次插入应新增 1 条，实际 %d", n2)
	}

	cnt, err := m.CountTriples(ctx, tenant, graph)
	if err != nil {
		t.Fatalf("非预期错误: %v", err)
	}
	if cnt != 3 {
		t.Fatalf("三元组数应为 3，实际 %d", cnt)
	}
}

func TestMatchBinding(t *testing.T) {
	ctx := context.Background()
	tenant := ids.New()
	graph := "https://ex/g"
	m := NewMemStore()

	alice := mkIRI("https://ex/alice")
	bob := mkIRI("https://ex/bob")
	knows := mkIRI("https://ex/knows")
	named := mkIRI("https://ex/name")

	seed := []Triple{
		{S: alice, P: knows, O: bob},
		{S: alice, P: named, O: mkLit("Alice", "xsd:string")},
		{S: bob, P: named, O: mkLit("Bob", "xsd:string")},
	}
	if _, err := m.InsertTriples(ctx, tenant, graph, seed); err != nil {
		t.Fatalf("seed 失败: %v", err)
	}

	tests := []struct {
		name string
		pat  Pattern
		want int
	}{
		{"全通配", Pattern{GraphIRI: graph}, 3},
		{"按主语绑定", Pattern{S: &alice, GraphIRI: graph}, 2},
		{"按谓词绑定", Pattern{P: &knows, GraphIRI: graph}, 1},
		{"按宾语绑定", Pattern{O: &bob, GraphIRI: graph}, 1},
		{"主谓绑定", Pattern{S: &alice, P: &knows, GraphIRI: graph}, 1},
		{"无匹配的主体", Pattern{S: ptr(mkIRI("https://ex/nobody")), GraphIRI: graph}, 0},
		{"跨图隔离", Pattern{GraphIRI: "https://ex/other"}, 0},
		{"limit 截断", Pattern{GraphIRI: graph, Limit: 2}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := m.Match(ctx, tenant, tt.pat)
			if err != nil {
				t.Fatalf("非预期错误: %v", err)
			}
			if len(got) != tt.want {
				t.Fatalf("匹配 %d 条，期望 %d 条", len(got), tt.want)
			}
		})
	}
}

func TestMatchTenantIsolation(t *testing.T) {
	ctx := context.Background()
	t1, t2 := ids.New(), ids.New()
	graph := "https://ex/g"
	m := NewMemStore()
	_, _ = m.InsertTriples(ctx, t1, graph, []Triple{{S: mkIRI("https://ex/a"), P: mkIRI("https://ex/p"), O: mkIRI("https://ex/b")}})

	got1, _ := m.Match(ctx, t1, Pattern{GraphIRI: graph})
	if len(got1) != 1 {
		t.Fatalf("租户 1 应有 1 条，实际 %d", len(got1))
	}
	got2, _ := m.Match(ctx, t2, Pattern{GraphIRI: graph})
	if len(got2) != 0 {
		t.Fatalf("租户 2 不应看到租户 1 的数据，实际 %d", len(got2))
	}
	if _, err := m.Match(ctx, ids.ID{}, Pattern{}); err == nil {
		t.Fatal("零值 tenant 应返回错误")
	}
}

func TestDeleteTriples(t *testing.T) {
	ctx := context.Background()
	tenant := ids.New()
	graph := "https://ex/g"
	m := NewMemStore()

	a := mkIRI("https://ex/a")
	p := mkIRI("https://ex/p")
	b := mkIRI("https://ex/b")
	seed := []Triple{{S: a, P: p, O: b}, {S: a, P: p, O: mkIRI("https://ex/c")}}
	_, _ = m.InsertTriples(ctx, tenant, graph, seed)

	n, err := m.DeleteTriples(ctx, tenant, graph, []Triple{{S: a, P: p, O: b}})
	if err != nil {
		t.Fatalf("非预期错误: %v", err)
	}
	if n != 1 {
		t.Fatalf("应删除 1 条，实际 %d", n)
	}
	n2, _ := m.DeleteTriples(ctx, tenant, graph, []Triple{{S: a, P: p, O: b}})
	if n2 != 0 {
		t.Fatalf("重复删除应为 0，实际 %d", n2)
	}
	cnt, _ := m.CountTriples(ctx, tenant, graph)
	if cnt != 1 {
		t.Fatalf("剩余应为 1，实际 %d", cnt)
	}
}

func TestObservedAtDefaultAndFactKind(t *testing.T) {
	ctx := context.Background()
	tenant := ids.New()
	graph := "https://ex/g"
	m := NewMemStore()
	_, _ = m.InsertTriples(ctx, tenant, graph, []Triple{{S: mkIRI("https://ex/a"), P: mkIRI("https://ex/p"), O: mkIRI("https://ex/b")}})
	got, _ := m.Match(ctx, tenant, Pattern{GraphIRI: graph})
	if len(got) != 1 {
		t.Fatalf("应有 1 条，实际 %d", len(got))
	}
	if got[0].FactKind != FactKindAsserted {
		t.Fatalf("缺省 fact_kind 应为 %s，实际 %s", FactKindAsserted, got[0].FactKind)
	}
	if got[0].ObservedAt.IsZero() {
		t.Fatal("observed_at 应被填充")
	}
	if got[0].ObservedAt.After(time.Now().Add(time.Minute)) {
		t.Fatal("observed_at 不应是未来时间")
	}
}

func TestPGStoreImplementsGraphStore(t *testing.T) {
	// PGStore 只需编译通过（不连库）。
	var s GraphStore = &PGStore{}
	if s == nil {
		t.Fatal("PGStore 应实现 GraphStore")
	}
}

func TestMemStoreErrorCodes(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	_, err := m.EnsureGraph(ctx, ids.ID{}, "https://ex/g", "INSTANCE", "")
	if appErr := errors.As(err); appErr.Code != errors.CodeValidationFailed {
		t.Fatalf("错误码应为 %s，实际 %s", errors.CodeValidationFailed, appErr.Code)
	}
	_, err = m.InsertTriples(ctx, ids.New(), "", []Triple{{}})
	if appErr := errors.As(err); appErr.Code != errors.CodeValidationFailed {
		t.Fatalf("错误码应为 %s，实际 %s", errors.CodeValidationFailed, appErr.Code)
	}
}

func ptr(t Term) *Term { return &t }
