package graphstore

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// ---------------------------------------------------------------------------
// 测试替身
//
// 这里刻意**不**实现一个迷你 SPARQL 引擎：那会变成"用自己造的引擎验证自己造的
// 客户端"。本文件验证的是适配器**发出的请求**与**解析响应的方式**（最易出错、
// 也最关乎安全的两端）；引擎语义的正确性由 graphstore-check 对真实 Fuseki 的
// 端到端自检覆盖（见 tools/ci/12-graphstore.sh）。
//
// 替身按请求体**语义**分类应答，而不是按调用序号 —— 否则测试会把自己的调用顺序
// 假设固化下来，适配器将来少发/多发一次请求反而可能"测不出来"。
// ---------------------------------------------------------------------------

type recordedRequest struct {
	Path        string
	ContentType string
	Accept      string
	AuthHeader  string
	Body        string
}

type fusekiStub struct {
	mu sync.Mutex

	// status 非 0 时，所有请求统一返回该 HTTP 状态。
	status int
	// existing 是存在性预检查（VALUES）返回的三元组行（JSON 片段，不带外层 []）。
	existing string
	// count 是 COUNT(*) 查询返回的计数值。
	count string
	// matchRows 是模式匹配（SELECT ?s ?p ?o）返回的绑定行（JSON 片段）。
	matchRows string
	// override 可拦截任意请求；第三个返回值为 true 表示已处理。
	override func(recordedRequest) (int, string, bool)

	requests []recordedRequest
	updates  []string
}

func (s *fusekiStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	rec := recordedRequest{
		Path:        r.URL.Path,
		ContentType: r.Header.Get("Content-Type"),
		Accept:      r.Header.Get("Accept"),
		AuthHeader:  r.Header.Get("Authorization"),
		Body:        string(body),
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, rec)
	if strings.HasSuffix(rec.Path, "/update") {
		s.updates = append(s.updates, rec.Body)
	}
	status, existing, count, matchRows, override := s.status, s.existing, s.count, s.matchRows, s.override
	s.mu.Unlock()

	if override != nil {
		if st, bd, handled := override(rec); handled {
			s.write(w, st, bd)
			return
		}
	}
	if status != 0 {
		s.write(w, status, "engine said no")
		return
	}
	if strings.HasSuffix(rec.Path, "/update") {
		s.write(w, http.StatusNoContent, "")
		return
	}
	switch {
	case strings.Contains(rec.Body, "VALUES (?s ?p ?o)"):
		s.write(w, http.StatusOK, resultsJSON("s,p,o", existing))
	case strings.Contains(rec.Body, "COUNT(*)"):
		if count == "" {
			count = "0"
		}
		s.write(w, http.StatusOK, fmt.Sprintf(
			`{"head":{"vars":["n"]},"results":{"bindings":[{"n":{"type":"literal","value":%q,"datatype":"http://www.w3.org/2001/XMLSchema#integer"}}]}}`,
			count))
	case strings.HasPrefix(strings.TrimSpace(rec.Body), "ASK"):
		s.write(w, http.StatusOK, `{"head":{},"boolean":true}`)
	default:
		s.write(w, http.StatusOK, resultsJSON("s,p,o,g", matchRows))
	}
}

func (s *fusekiStub) write(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/sparql-results+json")
	w.WriteHeader(status)
	if body != "" {
		_, _ = w.Write([]byte(body))
	}
}

// resultsJSON 组装 SPARQL 1.1 JSON 结果集；vars 为逗号分隔的变量名，rawRows 为绑定对象列表。
func resultsJSON(vars, rawRows string) string {
	names := strings.Split(vars, ",")
	quoted := make([]string, 0, len(names))
	for _, n := range names {
		quoted = append(quoted, `"`+n+`"`)
	}
	return fmt.Sprintf(`{"head":{"vars":[%s]},"results":{"bindings":[%s]}}`,
		strings.Join(quoted, ","), rawRows)
}

func litCell(v string) string { return fmt.Sprintf(`{"type":"literal","value":%q}`, v) }

func iriCell(v string) string { return fmt.Sprintf(`{"type":"uri","value":%q}`, v) }

// tripleRow 生成一条绑定行；g 为空时不带 graph 变量。
func tripleRow(s, p, o, g string) string {
	row := fmt.Sprintf(`{"s":%s,"p":%s,"o":%s`, iriCell(s), iriCell(p), o)
	if g != "" {
		row += fmt.Sprintf(`,"g":%s`, iriCell(g))
	}
	return row + "}"
}

// newStubStore 启动替身并返回适配器。
func newStubStore(t *testing.T, stub *fusekiStub, cfg ...func(*FusekiConfig)) *FusekiStore {
	t.Helper()
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	fcfg := FusekiConfig{Endpoint: srv.URL + "/odap", Timeout: 5 * time.Second, HTTPClient: srv.Client()}
	for _, mut := range cfg {
		mut(&fcfg)
	}
	store, err := NewFusekiStore(fcfg)
	if err != nil {
		t.Fatalf("构造适配器失败: %v", err)
	}
	return store
}

// ---------------------------------------------------------------------------
// 测试常量
// ---------------------------------------------------------------------------

var (
	testTenant  = mustParseID("11111111-1111-4111-8111-111111111111")
	otherTenant = mustParseID("22222222-2222-4222-8222-222222222222")
)

const testGraphIRI = "https://ex.example/onto"

func mustParseID(s string) ids.ID {
	id, err := ids.Parse(s)
	if err != nil {
		panic(err)
	}
	return id
}

func iri(s string) Term   { return Term{Kind: KindIRI, Lexical: s} }
func lit(s string) Term   { return Term{Kind: KindLiteral, Lexical: s} }
func bnode(s string) Term { return Term{Kind: KindBlankNode, Lexical: s} }

func simpleTriple() Triple {
	return Triple{S: iri("https://ex/s"), P: iri("https://ex/p"), O: lit("v")}
}

// ptrTerm 返回词项指针（Pattern 的位置绑定形式）。
func ptrTerm(t Term) *Term { return &t }

// triplePatternRE 捕获查询中最内层的花括号组（即三重图模式本身）。
var triplePatternRE = regexp.MustCompile(`\{ ([^{}]+?) \}`)

func (s *fusekiStub) recorded() []recordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]recordedRequest, len(s.requests))
	copy(out, s.requests)
	return out
}

func (s *fusekiStub) capturedUpdates() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.updates))
	copy(out, s.updates)
	return out
}

// ---------------------------------------------------------------------------
// 1. 协议形状
// ---------------------------------------------------------------------------

func TestFusekiUsesOnlyQueryAndUpdateEndpoints(t *testing.T) {
	stub := &fusekiStub{}
	store := newStubStore(t, stub)
	ctx := context.Background()

	if _, err := store.EnsureGraph(ctx, testTenant, testGraphIRI, "INSTANCE", "v1"); err != nil {
		t.Fatalf("EnsureGraph: %v", err)
	}
	if _, err := store.InsertTriples(ctx, testTenant, testGraphIRI, []Triple{simpleTriple()}); err != nil {
		t.Fatalf("InsertTriples: %v", err)
	}
	if _, err := store.DeleteTriples(ctx, testTenant, testGraphIRI, []Triple{simpleTriple()}); err != nil {
		t.Fatalf("DeleteTriples: %v", err)
	}
	if _, err := store.Match(ctx, testTenant, Pattern{GraphIRI: testGraphIRI}); err != nil {
		t.Fatalf("Match: %v", err)
	}
	if _, err := store.CountTriples(ctx, testTenant, testGraphIRI); err != nil {
		t.Fatalf("CountTriples: %v", err)
	}
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	reqs := stub.recorded()
	if len(reqs) == 0 {
		t.Fatal("适配器未发出任何请求")
	}
	for _, r := range reqs {
		if r.Path != "/odap/query" && r.Path != "/odap/update" {
			t.Errorf("适配器访问了非 SPARQL 端点: %s", r.Path)
		}
		if strings.Contains(r.Body, "CREATE GRAPH") {
			t.Errorf("适配器不应发出建图语句（SPARQL 1.1 无标准 CREATE）: %s", r.Body)
		}
	}
}

func TestFusekiSetsProperRequestHeaders(t *testing.T) {
	stub := &fusekiStub{}
	store := newStubStore(t, stub)
	if _, err := store.InsertTriples(context.Background(), testTenant, testGraphIRI, []Triple{simpleTriple()}); err != nil {
		t.Fatalf("InsertTriples: %v", err)
	}

	var sawQuery, sawUpdate bool
	for _, r := range stub.recorded() {
		if strings.HasSuffix(r.Path, "/query") {
			sawQuery = true
			if r.ContentType != "application/sparql-query; charset=utf-8" {
				t.Errorf("查询 Content-Type 错误: %q", r.ContentType)
			}
			if r.Accept != "application/sparql-results+json" {
				t.Errorf("Accept 头错误: %q（必须显式要求 JSON 结果集）", r.Accept)
			}
		} else {
			sawUpdate = true
			if r.ContentType != "application/sparql-update; charset=utf-8" {
				t.Errorf("更新 Content-Type 错误: %q", r.ContentType)
			}
		}
	}
	if !sawQuery || !sawUpdate {
		t.Fatalf("期望同时出现查询与更新请求，sawQuery=%t sawUpdate=%t", sawQuery, sawUpdate)
	}
}

func TestFusekiSendsBasicAuthAndKeepsCredentialsOutOfURL(t *testing.T) {
	stub := &fusekiStub{}
	store := newStubStore(t, stub, func(c *FusekiConfig) {
		c.Username, c.Password = "odap", "s3cret"
	})
	if _, err := store.InsertTriples(context.Background(), testTenant, testGraphIRI, []Triple{simpleTriple()}); err != nil {
		t.Fatalf("InsertTriples: %v", err)
	}
	reqs := stub.recorded()
	if len(reqs) == 0 {
		t.Fatal("未发出请求")
	}
	for _, r := range reqs {
		if !strings.HasPrefix(r.AuthHeader, "Basic ") {
			t.Fatalf("未发送 Basic 认证头: %q", r.AuthHeader)
		}
	}
	if strings.Contains(store.Endpoint(), "@") || strings.Contains(store.Endpoint(), "s3cret") {
		t.Fatalf("凭据不得出现在端点 URL 中: %s", store.Endpoint())
	}
}

// ---------------------------------------------------------------------------
// 2. 租户隔离
// ---------------------------------------------------------------------------

func TestFusekiEveryRequestCarriesTenantNamespace(t *testing.T) {
	stub := &fusekiStub{}
	store := newStubStore(t, stub)
	ctx := context.Background()

	if _, err := store.InsertTriples(ctx, testTenant, testGraphIRI, []Triple{simpleTriple()}); err != nil {
		t.Fatalf("InsertTriples: %v", err)
	}
	if _, err := store.Match(ctx, testTenant, Pattern{}); err != nil { // 跨命名图通配
		t.Fatalf("Match: %v", err)
	}
	if _, err := store.CountTriples(ctx, testTenant, testGraphIRI); err != nil {
		t.Fatalf("CountTriples: %v", err)
	}
	if _, err := store.DeleteTriples(ctx, testTenant, testGraphIRI, []Triple{simpleTriple()}); err != nil {
		t.Fatalf("DeleteTriples: %v", err)
	}

	want := graphNamespacePrefix(testTenant)
	for _, r := range stub.recorded() {
		if !strings.Contains(r.Body, want) {
			t.Errorf("请求未携带本租户命名空间 %q:\n%s", want, r.Body)
		}
		if strings.Contains(r.Body, graphNamespacePrefix(otherTenant)) {
			t.Errorf("请求携带了其他租户的命名空间（越权风险）:\n%s", r.Body)
		}
	}
}

// TestFusekiMatchQueryIsPositionallyWellFormed 是**回归断言**：精确比对生成的查询文本。
//
// 为什么必须精确比对而不是子串断言：曾经这里存在一个真实缺陷——绑定词项被统一写在
// 三个变量之前，生成了 `?s <p> ?s ?p ?o` 这样的四词项图模式。它在只做 strings.Contains
// 的替身测试里完全看不出问题，却在真实引擎上直接语法报错
// （rdflib: "Expected SelectQuery, found 'GRAPH'"）。断言完整文本是最省事且最强的护栏。
func TestFusekiMatchQueryIsPositionallyWellFormed(t *testing.T) {
	phys := graphNamespacePrefix(testTenant) + testGraphIRI
	cases := []struct {
		name string
		p    Pattern
		want string
	}{
		{
			name: "全通配（图固定）",
			p:    Pattern{GraphIRI: testGraphIRI},
			want: "SELECT ?s ?p ?o WHERE { GRAPH <" + phys + "> { ?s ?p ?o } } ORDER BY ?s ?p ?o",
		},
		{
			name: "仅主语绑定",
			p:    Pattern{S: ptrTerm(iri("https://ex/s")), GraphIRI: testGraphIRI},
			want: "SELECT ?p ?o WHERE { GRAPH <" + phys +
				"> { <https://ex/s> ?p ?o } } ORDER BY ?p ?o",
		},
		{
			name: "仅谓语绑定",
			p:    Pattern{P: ptrTerm(iri("https://ex/p")), GraphIRI: testGraphIRI},
			want: "SELECT ?s ?o WHERE { GRAPH <" + phys +
				"> { ?s <https://ex/p> ?o } } ORDER BY ?s ?o",
		},
		{
			name: "仅宾语绑定（字面值）",
			p:    Pattern{O: ptrTerm(lit("v")), GraphIRI: testGraphIRI},
			want: "SELECT ?s ?p WHERE { GRAPH <" + phys +
				`> { ?s ?p "v" } } ORDER BY ?s ?p`,
		},
		{
			name: "主语绑定 + 跨命名图 + 限流",
			p:    Pattern{S: ptrTerm(iri("https://ex/s")), Limit: 7},
			want: "SELECT ?p ?o ?g WHERE { GRAPH ?g { <https://ex/s> ?p ?o } " +
				`FILTER(STRSTARTS(STR(?g), "` + graphNamespacePrefix(testTenant) +
				`")) } ORDER BY ?p ?o ?g LIMIT 7`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildMatchQuery(testTenant, tc.p)
			if err != nil {
				t.Fatalf("buildMatchQuery: %v", err)
			}
			if got != tc.want {
				t.Fatalf("查询文本不符：\n期望 %s\n实际 %s", tc.want, got)
			}
			// 图模式必须恰好三个词项（缺陷形态是四个）。
			m := triplePatternRE.FindStringSubmatch(got)
			if m == nil {
				t.Fatalf("未能从查询中识别出三重图模式：%s", got)
			}
			if n := len(strings.Fields(m[1])); n != 3 {
				t.Fatalf("图模式应恰好 3 个词项，实际 %d：%q", n, m[1])
			}
			// 投影变量必须与自由位置一致：绑定位置**不得**出现在 SELECT 与 ORDER BY 中，
			// 否则不同引擎对"未出现于模式的投影变量"处理不一致（rdflib 会省略该绑定）。
			projection := got[:strings.Index(got, " WHERE")]
			for i, pos := range []*Term{tc.p.S, tc.p.P, tc.p.O} {
				if pos == nil {
					continue
				}
				if strings.Contains(projection, positionVars[i]) {
					t.Fatalf("已绑定位置 %d 不应被投影：%s", i, projection)
				}
			}
		})
	}
}

func TestFusekiFullyBoundMatchUsesAsk(t *testing.T) {
	phys := graphNamespacePrefix(testTenant) + testGraphIRI
	p := Pattern{S: ptrTerm(iri("https://ex/s")), P: ptrTerm(iri("https://ex/p")),
		O: ptrTerm(lit("v")), GraphIRI: testGraphIRI}
	got, err := buildAskQuery(testTenant, p)
	if err != nil {
		t.Fatalf("buildAskQuery: %v", err)
	}
	want := "ASK { GRAPH <" + phys + `> { <https://ex/s> <https://ex/p> "v" } }`
	if got != want {
		t.Fatalf("全绑定模式应使用 ASK：\n期望 %s\n实际 %s", want, got)
	}
	// 全绑定模式不得走 SELECT 路径（会生成 `SELECT WHERE {...}` 这类非法查询）。
	if _, err := buildMatchQuery(testTenant, p); err == nil {
		t.Fatal("buildMatchQuery 对全绑定模式必须报错，由调用方转 ASK")
	}

	stub := &fusekiStub{override: func(req recordedRequest) (int, string, bool) {
		if strings.HasPrefix(strings.TrimSpace(req.Body), "ASK") {
			return 200, `{"head":{},"boolean":true}`, true
		}
		return 0, "", false
	}}
	store := newStubStore(t, stub)
	got3, err := store.Match(context.Background(), testTenant, p)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(got3) != 1 {
		t.Fatalf("存在时应返回 1 条，实际 %d", len(got3))
	}
	if got3[0].S.Lexical != "https://ex/s" || got3[0].O.Lexical != "v" || got3[0].GraphIRI != testGraphIRI {
		t.Fatalf("重建的三元组不正确: %+v", got3[0])
	}
	// ASK 返回 false 时必须返回空结果，而不是"0 条 + 错误"或"1 条假结果"。
	emptyStub := &fusekiStub{override: func(req recordedRequest) (int, string, bool) {
		return 200, `{"head":{},"boolean":false}`, true
	}}
	store2 := newStubStore(t, emptyStub)
	got4, err := store2.Match(context.Background(), testTenant, p)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(got4) != 0 {
		t.Fatalf("不存在时应返回空，实际 %d 条", len(got4))
	}
}

func TestFusekiMatchWildcardFiltersByTenantPrefix(t *testing.T) {
	stub := &fusekiStub{}
	store := newStubStore(t, stub)
	if _, err := store.Match(context.Background(), testTenant, Pattern{}); err != nil {
		t.Fatalf("Match: %v", err)
	}
	reqs := stub.recorded()
	if len(reqs) != 1 {
		t.Fatalf("期望 1 次查询，实际 %d", len(reqs))
	}
	q := reqs[0].Body
	if !strings.Contains(q, "GRAPH ?g") {
		t.Fatalf("通配命名图查询必须使用 GRAPH ?g 变量:\n%s", q)
	}
	if !strings.Contains(q, "STRSTARTS") || !strings.Contains(q, graphNamespacePrefix(testTenant)) {
		t.Fatalf("通配命名图查询缺少租户前缀过滤（会读到其他租户的数据）:\n%s", q)
	}
}

func TestFusekiRejectsResultsFromForeignTenantNamespace(t *testing.T) {
	foreign := graphNamespacePrefix(otherTenant) + testGraphIRI
	stub := &fusekiStub{matchRows: tripleRow("https://ex/s", "https://ex/p", litCell("v"), foreign)}
	store := newStubStore(t, stub)
	_, err := store.Match(context.Background(), testTenant, Pattern{})
	if err == nil {
		t.Fatal("读到其他租户命名图的结果必须报错，而不是静默丢弃或返回")
	}
	if code := errors.As(err).Code; code != errors.CodeInternal {
		t.Fatalf("期望 INTERNAL 错误码，实际 %s（%v）", code, err)
	}
}

// ---------------------------------------------------------------------------
// 3. 注入抵抗
// ---------------------------------------------------------------------------

// sparqlSkeleton 去掉所有 <IRI> 与 "literal" 片段，返回其余骨架文本。
//
// 用途：断言敌对载荷**只出现在**被尖括号/引号包裹的片段内部。若转义有任何疏漏，
// 载荷的结构性 token 就会落进骨架里，被下面的断言抓住。
func sparqlSkeleton(q string) string {
	var b strings.Builder
	i := 0
	for i < len(q) {
		switch q[i] {
		case '<':
			j := i + 1
			for j < len(q) && q[j] != '>' {
				if q[j] == '\\' {
					j++
				}
				j++
			}
			i = j + 1
			b.WriteString("<IRI>")
		case '"':
			j := i + 1
			for j < len(q) && q[j] != '"' {
				if q[j] == '\\' {
					j++
				}
				j++
			}
			i = j + 1
			b.WriteString("<LITERAL>")
		default:
			b.WriteByte(q[i])
			i++
		}
	}
	return b.String()
}

// insertDataSkeletonRE 是合法 INSERT DATA 唯一允许的骨架形态。
var insertDataSkeletonRE = regexp.MustCompile(
	`^INSERT DATA \{ GRAPH <IRI> \{\s*(<IRI> <IRI> <LITERAL> \.\s*)*\} \}$`)

func TestFusekiEscapesAdversarialTermContent(t *testing.T) {
	adversarialIRI := "https://ex/evil> } ; INSERT DATA { <x> <y> <z"
	adversarialLit := `"; DELETE WHERE { ?s ?p ?o } ; "`

	stub := &fusekiStub{}
	store := newStubStore(t, stub)
	if _, err := store.InsertTriples(context.Background(), testTenant, testGraphIRI, []Triple{
		{S: iri(adversarialIRI), P: iri("https://ex/p"), O: lit(adversarialLit)},
	}); err != nil {
		t.Fatalf("InsertTriples: %v", err)
	}
	updates := stub.capturedUpdates()
	if len(updates) != 1 {
		t.Fatalf("期望 1 次 INSERT DATA，实际 %d", len(updates))
	}
	update := updates[0]
	skel := strings.TrimSpace(sparqlSkeleton(update))

	// 骨架必须严格等于"合法的 INSERT DATA 形态"：敌对载荷没有留下任何结构痕迹。
	if !insertDataSkeletonRE.MatchString(skel) {
		t.Fatalf("生成的更新语句结构非法（词项可能逃逸出边界）:\n骨架=%s\n原文=%s", skel, update)
	}
	for _, leaked := range []string{"DELETE WHERE", "<x>", "<y>", "<z>", "?s ?p ?o"} {
		if strings.Contains(skel, leaked) {
			t.Fatalf("敌对载荷逃逸出词项边界，骨架中出现 %q:\n%s", leaked, skel)
		}
	}
	// 载荷必须仍然完整存在于片段内部（确认不是靠"丢弃内容"通过）。
	if !strings.Contains(update, `\u003E`) || !strings.Contains(update, `\u003C`) {
		t.Errorf("IRI 中的 '<' / '>' 未被转义:\n%s", update)
	}
	if !strings.Contains(update, `\"`) {
		t.Errorf("字面值中的 '\"' 未被转义:\n%s", update)
	}
	if !strings.Contains(update, "DELETE WHERE") {
		t.Errorf("载荷内容应被完整保留（仅处于字面值内部）:\n%s", update)
	}
}

func TestFusekiRejectsReservedNamespaceGraphIRI(t *testing.T) {
	store := newStubStore(t, &fusekiStub{})
	ctx := context.Background()
	for _, bad := range []string{"urn:odap:t:other:g:x", "urn:odap:bnode:abc"} {
		if _, err := store.Match(ctx, testTenant, Pattern{GraphIRI: bad}); err == nil {
			t.Fatalf("保留命名空间 %q 必须被拒绝", bad)
		}
	}
	if _, err := store.Match(ctx, testTenant, Pattern{GraphIRI: "https://ex/ok"}); err != nil {
		t.Fatalf("普通命名图不应被拒绝: %v", err)
	}
}

func TestPhysicalGraphIRISeparatesTenantsAndRoundTrips(t *testing.T) {
	a, err := physicalGraphIRI(testTenant, testGraphIRI)
	if err != nil {
		t.Fatalf("physicalGraphIRI: %v", err)
	}
	b, err := physicalGraphIRI(otherTenant, testGraphIRI)
	if err != nil {
		t.Fatalf("physicalGraphIRI: %v", err)
	}
	if a == b {
		t.Fatal("不同租户 + 相同调用方 IRI 产生了相同物理图 IRI（隔离失效）")
	}
	visible, ok := visibleGraphIRI(testTenant, a)
	if !ok || visible != testGraphIRI {
		t.Fatalf("往返失败: visible=%q ok=%t", visible, ok)
	}
	if _, ok := visibleGraphIRI(testTenant, b); ok {
		t.Fatal("另一个租户的物理图 IRI 不应被还原为本租户视图")
	}
	// 调用方把另一个租户的物理 IRI 当作自己的 IRI 传入：保留命名空间被显式拒绝，
	// 因此不存在"用别人的物理图 IRI 构造出等价图"的路径。
	if _, err := physicalGraphIRI(testTenant, b); err == nil {
		t.Fatal("把保留命名空间的 IRI 当作用户命名图必须被拒绝")
	}
}

// ---------------------------------------------------------------------------
// 4. 计数语义
// ---------------------------------------------------------------------------

func existingRow() string {
	return tripleRow("https://ex/existing", "https://ex/p", litCell("v"), "")
}

func TestFusekiInsertReportsOnlyNewlyAddedCount(t *testing.T) {
	stub := &fusekiStub{existing: existingRow()}
	store := newStubStore(t, stub)
	triples := []Triple{
		{S: iri("https://ex/existing"), P: iri("https://ex/p"), O: lit("v")}, // 已存在
		{S: iri("https://ex/new1"), P: iri("https://ex/p"), O: lit("v")},
		{S: iri("https://ex/new2"), P: iri("https://ex/p"), O: lit("v")},
	}
	n, err := store.InsertTriples(context.Background(), testTenant, testGraphIRI, triples)
	if err != nil {
		t.Fatalf("InsertTriples: %v", err)
	}
	if n != 2 {
		t.Fatalf("期望新增 2 条（已存在的不计），实际 %d", n)
	}
	updates := stub.capturedUpdates()
	if len(updates) != 1 {
		t.Fatalf("期望 1 次 INSERT DATA，实际 %d", len(updates))
	}
	if strings.Contains(updates[0], "ex/existing") {
		t.Errorf("已存在的三元组不应出现在 INSERT DATA 中:\n%s", updates[0])
	}
	if !strings.Contains(updates[0], "ex/new1") || !strings.Contains(updates[0], "ex/new2") {
		t.Errorf("新增三元组未出现在 INSERT DATA 中:\n%s", updates[0])
	}
}

func TestFusekiInsertSkipsUpdateWhenEverythingExists(t *testing.T) {
	stub := &fusekiStub{existing: existingRow()}
	store := newStubStore(t, stub)
	n, err := store.InsertTriples(context.Background(), testTenant, testGraphIRI,
		[]Triple{{S: iri("https://ex/existing"), P: iri("https://ex/p"), O: lit("v")}})
	if err != nil {
		t.Fatalf("InsertTriples: %v", err)
	}
	if n != 0 {
		t.Fatalf("期望新增 0 条，实际 %d", n)
	}
	if got := len(stub.capturedUpdates()); got != 0 {
		t.Fatalf("全部已存在时不应发出写请求，实际 %d 次", got)
	}
}

func TestFusekiInsertDeduplicatesWithinRequest(t *testing.T) {
	stub := &fusekiStub{}
	store := newStubStore(t, stub)
	dup := simpleTriple()
	n, err := store.InsertTriples(context.Background(), testTenant, testGraphIRI, []Triple{dup, dup, dup})
	if err != nil {
		t.Fatalf("InsertTriples: %v", err)
	}
	if n != 1 {
		t.Fatalf("请求内重复三元组应去重后计 1 条，实际 %d", n)
	}
	updates := stub.capturedUpdates()
	if got := strings.Count(updates[0], `<https://ex/s> <https://ex/p> "v" .`); got != 1 {
		t.Fatalf("INSERT DATA 中重复三元组出现 %d 次:\n%s", got, updates[0])
	}
}

func TestFusekiDeleteReportsOnlyDeletedCount(t *testing.T) {
	stub := &fusekiStub{existing: existingRow()}
	store := newStubStore(t, stub)
	n, err := store.DeleteTriples(context.Background(), testTenant, testGraphIRI, []Triple{
		{S: iri("https://ex/existing"), P: iri("https://ex/p"), O: lit("v")}, // 存在
		{S: iri("https://ex/absent"), P: iri("https://ex/p"), O: lit("v")},   // 不存在
	})
	if err != nil {
		t.Fatalf("DeleteTriples: %v", err)
	}
	if n != 1 {
		t.Fatalf("期望删除 1 条，实际 %d", n)
	}
	updates := stub.capturedUpdates()
	if len(updates) != 1 || !strings.HasPrefix(updates[0], "DELETE DATA") {
		t.Fatalf("期望 1 次 DELETE DATA，实际 %v", updates)
	}
	if strings.Contains(updates[0], "ex/absent") {
		t.Errorf("不存在的三元组不应出现在 DELETE DATA 中:\n%s", updates[0])
	}
}

func TestFusekiRejectsMismatchedTripleGraphIRI(t *testing.T) {
	store := newStubStore(t, &fusekiStub{})
	bad := simpleTriple()
	bad.GraphIRI = "https://ex/other"
	_, err := store.InsertTriples(context.Background(), testTenant, testGraphIRI, []Triple{bad})
	if err == nil {
		t.Fatal("三元组自带 GraphIRI 与调用参数不一致时必须拒绝")
	}
	if code := errors.As(err).Code; code != errors.CodeValidationFailed {
		t.Fatalf("期望 VALIDATION_FAILED，实际 %s", code)
	}
}

// ---------------------------------------------------------------------------
// 5. 响应解析
// ---------------------------------------------------------------------------

func TestFusekiMatchParsesTermsAndRestoresBlankNode(t *testing.T) {
	raw := strings.Join([]string{
		tripleRow("https://ex/s", "https://ex/p",
			`{"type":"literal","value":"128.50","datatype":"http://www.w3.org/2001/XMLSchema#decimal"}`, ""),
		tripleRow(bnodeIRI("b1/带斜杠"), "https://ex/note",
			`{"type":"literal","value":"纯文本","datatype":"http://www.w3.org/2001/XMLSchema#string"}`, ""),
	}, ",")
	stub := &fusekiStub{matchRows: raw}
	store := newStubStore(t, stub)

	got, err := store.Match(context.Background(), testTenant, Pattern{GraphIRI: testGraphIRI})
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("期望 2 条，实际 %d", len(got))
	}
	if got[0].O.Lexical != "128.50" || got[0].O.Datatype != "http://www.w3.org/2001/XMLSchema#decimal" {
		t.Errorf("字面值解析错误: %+v", got[0].O)
	}
	if got[0].FactKind != FactKindAsserted {
		t.Errorf("纯 RDF 后端应返回 ASSERTED，实际 %q", got[0].FactKind)
	}
	if !got[0].ObservedAt.IsZero() || got[0].ValidFrom != nil || got[0].ValidTo != nil {
		t.Errorf("纯 RDF 后端不得伪造时间戳，实际 %+v", got[0])
	}
	if got[1].S.Kind != KindBlankNode || got[1].S.Lexical != "b1/带斜杠" {
		t.Errorf("空白节点未还原: %+v", got[1].S)
	}
	// 往返后 CanonicalDigest 必须与写入侧一致（依赖 xsd:string 归一化）。
	want := normalizeTerm(lit("纯文本"))
	if got[1].O.CanonicalDigest() != want.CanonicalDigest() {
		t.Errorf("xsd:string 归一化失败，往返 digest 不一致: %+v vs %+v", got[1].O, want)
	}
	if got[1].GraphIRI != testGraphIRI {
		t.Errorf("GraphIRI 应为调用方视角的 IRI，实际 %q", got[1].GraphIRI)
	}
}

func TestFusekiMatchWildcardMapsGraphIRIBackToVisibleForm(t *testing.T) {
	physical := graphNamespacePrefix(testTenant) + "https://ex.example/other"
	stub := &fusekiStub{matchRows: tripleRow("https://ex/s", "https://ex/p", litCell("v"), physical)}
	store := newStubStore(t, stub)
	got, err := store.Match(context.Background(), testTenant, Pattern{})
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("期望 1 条，实际 %d", len(got))
	}
	if got[0].GraphIRI != "https://ex.example/other" {
		t.Fatalf("命名图 IRI 应还原为调用方视角，实际 %q", got[0].GraphIRI)
	}
}

func TestFusekiCountTriplesParsesInteger(t *testing.T) {
	store := newStubStore(t, &fusekiStub{count: "42"})
	n, err := store.CountTriples(context.Background(), testTenant, testGraphIRI)
	if err != nil {
		t.Fatalf("CountTriples: %v", err)
	}
	if n != 42 {
		t.Fatalf("期望 42，实际 %d", n)
	}
}

func TestFusekiCountTriplesRejectsNonNumeric(t *testing.T) {
	store := newStubStore(t, &fusekiStub{count: "n/a"})
	if _, err := store.CountTriples(context.Background(), testTenant, testGraphIRI); err == nil {
		t.Fatal("非数字计数必须报错，而不是返回 0（把异常伪装成空数据）")
	}
}

func TestFusekiRejectsRawBlankNodeInResults(t *testing.T) {
	stub := &fusekiStub{matchRows: `{"s":{"type":"bnode","value":"b0"},"p":{"type":"uri","value":"https://ex/p"},"o":{"type":"literal","value":"v"}}`}
	store := newStubStore(t, stub)
	if _, err := store.Match(context.Background(), testTenant, Pattern{GraphIRI: testGraphIRI}); err == nil {
		t.Fatal("未经适配器编码的裸空白节点必须报错（暗示存在绕过适配器的写入）")
	}
}

func TestFusekiMatchRejectsInvalidInputs(t *testing.T) {
	store := newStubStore(t, &fusekiStub{})
	if _, err := store.Match(context.Background(), testTenant, Pattern{Limit: -1}); err == nil {
		t.Fatal("负 limit 必须被拒绝")
	}
	if _, err := store.Match(context.Background(), ids.ID{}, Pattern{}); err == nil {
		t.Fatal("零租户必须被拒绝")
	}
}

// ---------------------------------------------------------------------------
// 6. 错误映射与探针
// ---------------------------------------------------------------------------

func TestFusekiMapsHTTPStatusToPlatformCodes(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, errors.CodeUnavailable},
		{http.StatusForbidden, errors.CodeUnavailable},
		{http.StatusNotFound, errors.CodeUnavailable},
		{http.StatusBadRequest, errors.CodeInternal},
		{http.StatusInternalServerError, errors.CodeInternal},
	}
	for _, tc := range cases {
		store := newStubStore(t, &fusekiStub{status: tc.status})
		_, err := store.CountTriples(context.Background(), testTenant, testGraphIRI)
		if err == nil {
			t.Fatalf("HTTP %d 应产生错误", tc.status)
		}
		if got := errors.As(err).Code; got != tc.want {
			t.Errorf("HTTP %d 期望错误码 %s，实际 %s", tc.status, tc.want, got)
		}
	}
}

func TestFusekiSurfacesEngineErrorMessage(t *testing.T) {
	store := newStubStore(t, &fusekiStub{status: http.StatusBadRequest})
	_, err := store.CountTriples(context.Background(), testTenant, testGraphIRI)
	if err == nil || !strings.Contains(err.Error(), "engine said no") {
		t.Fatalf("错误信息应包含引擎返回内容以便排错，实际 %v", err)
	}
}

func TestFusekiPingRequiresBooleanTrue(t *testing.T) {
	store := newStubStore(t, &fusekiStub{})
	if err := store.Ping(context.Background()); err != nil {
		t.Fatalf("Ping 应成功: %v", err)
	}

	falseStub := &fusekiStub{override: func(recordedRequest) (int, string, bool) {
		return 200, `{"head":{},"boolean":false}`, true
	}}
	store2 := newStubStore(t, falseStub)
	if err := store2.Ping(context.Background()); err == nil {
		t.Fatal("Ping 收到 boolean=false 时必须报错")
	}
}

func TestFusekiPingUsesEmptyGroupPattern(t *testing.T) {
	stub := &fusekiStub{}
	store := newStubStore(t, stub)
	if err := store.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	reqs := stub.recorded()
	if len(reqs) != 1 || reqs[0].Body != "ASK {}" {
		t.Fatalf("探针必须是常量代价的 ASK {}，实际 %v", reqs)
	}
}

func TestFusekiRejectsOversizedResponse(t *testing.T) {
	stub := &fusekiStub{override: func(recordedRequest) (int, string, bool) {
		return 200, strings.Repeat("x", maxFusekiResponseBytes+10), true
	}}
	store := newStubStore(t, stub)
	_, err := store.CountTriples(context.Background(), testTenant, testGraphIRI)
	if err == nil {
		t.Fatal("超大响应必须被拒绝（防止内存耗尽）")
	}
	if code := errors.As(err).Code; code != errors.CodeResourceExhausted {
		t.Fatalf("期望 RESOURCE_EXHAUSTED，实际 %s", code)
	}
}

func TestFusekiEnsureGraphValidatesInputs(t *testing.T) {
	store := newStubStore(t, &fusekiStub{})
	ctx := context.Background()
	if _, err := store.EnsureGraph(ctx, ids.ID{}, testGraphIRI, "INSTANCE", "v1"); err == nil {
		t.Fatal("零租户必须被拒绝")
	}
	if _, err := store.EnsureGraph(ctx, testTenant, testGraphIRI, "  ", "v1"); err == nil {
		t.Fatal("空 graph_type 必须被拒绝")
	}
	id1, err := store.EnsureGraph(ctx, testTenant, testGraphIRI, "INSTANCE", "v1")
	if err != nil {
		t.Fatalf("EnsureGraph: %v", err)
	}
	id2, _ := store.EnsureGraph(ctx, testTenant, testGraphIRI, "INSTANCE", "v2")
	if id1 != id2 {
		t.Fatalf("同一 (tenant, graphIRI) 的稳定句柄必须相同：%d / %d", id1, id2)
	}
	if id1 < 0 {
		t.Fatalf("句柄必须为非负数，实际 %d", id1)
	}
}

// ---------------------------------------------------------------------------
// 7. 能力声明与后端装配
// ---------------------------------------------------------------------------

func TestFusekiCapabilitiesAreHonest(t *testing.T) {
	store := newStubStore(t, &fusekiStub{})
	caps := store.Capabilities()
	if caps.ResourceDictionary {
		t.Error("Fuseki 后端没有词项资源字典，ResourceDictionary 必须为 false")
	}
	if caps.PersistentGraphID {
		t.Error("Fuseki 后端没有持久 graph_id，PersistentGraphID 必须为 false")
	}
	if caps.FactMetadata {
		t.Error("Fuseki 后端不保留 fact_kind/时间维度，FactMetadata 必须为 false")
	}
	if _, ok := any(store).(TermDictionary); ok {
		t.Error("FusekiStore 不得实现 TermDictionary（否则调用方会拿到假 term_id）")
	}
}

func TestPGStoreAndMemStoreImplementTermDictionary(t *testing.T) {
	var pgDict TermDictionary = &PGStore{}
	if pgDict == nil {
		t.Fatal("PGStore 必须实现 TermDictionary")
	}
	var memDict TermDictionary = NewMemStore()
	if memDict == nil {
		t.Fatal("MemStore 必须实现 TermDictionary")
	}
}

func TestParseBackendFailsClosedOnUnknownValue(t *testing.T) {
	for _, raw := range []string{"", "postgres", "POSTGRES", " fuseki "} {
		if _, err := ParseBackend(raw); err != nil {
			t.Errorf("合法取值 %q 被拒绝: %v", raw, err)
		}
	}
	for _, raw := range []string{"fuskei", "neo4j", "pg"} {
		if _, err := ParseBackend(raw); err == nil {
			t.Errorf("非法取值 %q 必须被拒绝（不得静默回退到 postgres）", raw)
		}
	}
}

func TestConfigFromEnvRequiresEndpointForFuseki(t *testing.T) {
	env := map[string]string{EnvGraphStoreMode: "fuseki"}
	if _, err := ConfigFromEnv(func(k string) string { return env[k] }); err == nil {
		t.Fatal("fuseki 模式缺少 FUSEKI_ENDPOINT 时必须报错")
	}
	env[EnvFusekiEndpoint] = "http://fuseki:3030/odap"
	cfg, err := ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("合法配置被拒绝: %v", err)
	}
	if cfg.Backend != BackendFuseki || cfg.Fuseki.Endpoint != "http://fuseki:3030/odap" {
		t.Fatalf("配置解析错误: %+v", cfg)
	}
	if cfg.Fuseki.Timeout != 30*time.Second {
		t.Fatalf("默认超时应为 30s，实际 %v", cfg.Fuseki.Timeout)
	}
	for _, bad := range []string{"0", "-1", "abc"} {
		env[EnvFusekiTimeoutSecs] = bad
		if _, err := ConfigFromEnv(func(k string) string { return env[k] }); err == nil {
			t.Fatalf("FUSEKI_TIMEOUT_SECONDS=%q 必须被拒绝", bad)
		}
	}
}

func TestConfigFromEnvRejectsHalfConfiguredCredentials(t *testing.T) {
	env := map[string]string{
		EnvGraphStoreMode: "fuseki",
		EnvFusekiEndpoint: "http://fuseki:3030/odap",
		EnvFusekiUsername: "u",
	}
	if _, err := ConfigFromEnv(func(k string) string { return env[k] }); err == nil {
		t.Fatal("只配置用户名不配置密码必须被拒绝")
	}
	env[EnvFusekiPassword] = "p"
	if _, err := ConfigFromEnv(func(k string) string { return env[k] }); err != nil {
		t.Fatalf("成对凭据应被接受: %v", err)
	}
}

func TestConfigFromEnvDefaultsToPostgres(t *testing.T) {
	cfg, err := ConfigFromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatalf("默认配置不应报错: %v", err)
	}
	if cfg.Backend != BackendPostgres {
		t.Fatalf("未配置时应回退到 postgres（与旧行为一致），实际 %s", cfg.Backend)
	}
	if !strings.Contains(cfg.Describe(), "postgres") {
		t.Fatalf("Describe 应说明后端: %s", cfg.Describe())
	}
}

func TestOpenFailsClosedOnBadConfig(t *testing.T) {
	if _, err := Open(OpenConfig{Backend: BackendPostgres}); err == nil {
		t.Fatal("postgres 后端缺少连接池时必须报错")
	}
	if _, err := Open(OpenConfig{Backend: BackendFuseki}); err == nil {
		t.Fatal("fuseki 后端缺少端点时必须报错")
	}
	if _, err := Open(OpenConfig{Backend: Backend("neo4j")}); err == nil {
		t.Fatal("未知后端必须报错")
	}
}

func TestOpenFusekiReturnsUsableStore(t *testing.T) {
	gs, err := Open(OpenConfig{Backend: BackendFuseki,
		Fuseki: FusekiConfig{Endpoint: "http://fuseki:3030/odap"}})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, ok := gs.(*FusekiStore); !ok {
		t.Fatalf("期望 *FusekiStore，实际 %T", gs)
	}
}

func TestConfigFromEnvDescribeHidesCredentials(t *testing.T) {
	cfg, err := ConfigFromEnv(func(k string) string {
		return map[string]string{
			EnvGraphStoreMode: "fuseki",
			EnvFusekiEndpoint: "http://fuseki:3030/odap",
			EnvFusekiUsername: "admin",
			EnvFusekiPassword: "topsecret",
		}[k]
	})
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if desc := cfg.Describe(); strings.Contains(desc, "topsecret") {
		t.Fatalf("Describe 泄漏了凭据: %s", desc)
	}
}

// ---------------------------------------------------------------------------
// 8. 词项序列化
// ---------------------------------------------------------------------------

func TestWriteNTermSerialization(t *testing.T) {
	cases := []struct {
		name string
		term Term
		want string
	}{
		{"IRI", iri("https://ex/a"), "<https://ex/a>"},
		{"中文字面值", lit("报销单"), `"报销单"`},
		{"转义字面值", lit("a\"b\\c\nd"), `"a\"b\\c\nd"`},
		{"带 datatype", Term{Kind: KindLiteral, Lexical: "1.5", Datatype: "http://www.w3.org/2001/XMLSchema#decimal"},
			`"1.5"^^<http://www.w3.org/2001/XMLSchema#decimal>`},
		{"带语言", Term{Kind: KindLiteral, Lexical: "报销单", Lang: "zh-CN"}, `"报销单"@zh-CN`},
		{"空白节点提升为保留 IRI", bnode("b1/带斜杠"), "<urn:odap:bnode:b1%2F%E5%B8%A6%E6%96%9C%E6%9D%A0>"},
		{"IRI 中的尖括号被转义", iri("https://ex/a>b"), `<https://ex/a\u003Eb>`},
		{"控制字符转义", lit("a\x01b"), `"a\u0001b"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := termText(tc.term)
			if err != nil {
				t.Fatalf("termText: %v", err)
			}
			if got != tc.want {
				t.Fatalf("期望 %s，实际 %s", tc.want, got)
			}
		})
	}
}

func TestWriteNTermRejectsInvalidTerms(t *testing.T) {
	cases := map[string]Term{
		"非法 kind":            {Kind: 9, Lexical: "x"},
		"空 lexical":          {Kind: KindIRI},
		"空空白节点 label":        {Kind: KindBlankNode},
		"lang 与 datatype 并存": {Kind: KindLiteral, Lexical: "x", Lang: "zh", Datatype: "https://ex/dt"},
		"非法语言标签":             {Kind: KindLiteral, Lexical: "x", Lang: "z h"},
		"过长主语言标签":            {Kind: KindLiteral, Lexical: "x", Lang: "abcdefghi"},
		"非法 datatype":        {Kind: KindLiteral, Lexical: "x", Datatype: "https://ex/d t"},
	}
	for name, term := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := termText(term); err == nil {
				t.Fatalf("%s 必须被拒绝", name)
			}
		})
	}
}

func TestBNodePercentEncodingRoundTrips(t *testing.T) {
	for _, label := range []string{"b1", "b 1", "b%1", "带斜杠/x", "emoji-\U0001F600", "a\tb"} {
		encoded := bnodeIRI(label)
		if !strings.HasPrefix(encoded, reservedBNodeIRIPrefix) {
			t.Fatalf("编码结果缺少保留前缀: %s", encoded)
		}
		term, err := termFromIRI(encoded)
		if err != nil {
			t.Fatalf("termFromIRI(%s): %v", encoded, err)
		}
		if term.Kind != KindBlankNode || term.Lexical != label {
			t.Fatalf("往返失败: 期望 label=%q，实际 %+v", label, term)
		}
	}
	if _, err := termFromIRI(reservedBNodeIRIPrefix + "%zz"); err == nil {
		t.Fatal("非法百分号编码必须报错")
	}
}

func TestValidateGraphIRIRejectsWhitespaceAndControlChars(t *testing.T) {
	for _, bad := range []string{"", "  ", "https://ex/a b", "https://ex/a\nb"} {
		if err := validateGraphIRI(bad); err == nil {
			t.Errorf("%q 必须被拒绝", bad)
		}
	}
	if err := validateGraphIRI("https://ex/ok"); err != nil {
		t.Errorf("合法 IRI 被拒绝: %v", err)
	}
}

func TestNormalizeTermOnlyFoldsXsdString(t *testing.T) {
	if got := normalizeTerm(Term{Kind: KindLiteral, Lexical: "x", Datatype: xsdStringIRI}); got.Datatype != "" {
		t.Fatalf("xsd:string 应折叠为空 datatype，实际 %q", got.Datatype)
	}
	other := "http://www.w3.org/2001/XMLSchema#integer"
	if got := normalizeTerm(Term{Kind: KindLiteral, Lexical: "1", Datatype: other}); got.Datatype != other {
		t.Fatalf("其他 datatype 不得被改动，实际 %q", got.Datatype)
	}
	if got := normalizeTerm(iri(xsdStringIRI)); got.Datatype != "" || got.Kind != KindIRI {
		t.Fatalf("IRI 词项不得被改动，实际 %+v", got)
	}
}

// ---------------------------------------------------------------------------
// 9. 与 PG/Mem 后端的行为一致性（同一判据，不得各写一份）
// ---------------------------------------------------------------------------

func TestAllBackendsRejectMismatchedTripleGraphIRI(t *testing.T) {
	bad := simpleTriple()
	bad.GraphIRI = "https://ex/elsewhere"
	stores := map[string]GraphStore{
		"mem":    NewMemStore(),
		"fuseki": newStubStore(t, &fusekiStub{}),
	}
	for name, gs := range stores {
		if _, err := gs.InsertTriples(context.Background(), testTenant, testGraphIRI, []Triple{bad}); err == nil {
			t.Errorf("%s 后端未拒绝 GraphIRI 不一致的三元组", name)
		}
	}
}

func TestAllBackendsShareTenantValidation(t *testing.T) {
	stores := map[string]GraphStore{
		"mem":    NewMemStore(),
		"fuseki": newStubStore(t, &fusekiStub{}),
	}
	ctx := context.Background()
	for name, gs := range stores {
		if _, err := gs.Match(ctx, ids.ID{}, Pattern{}); err == nil {
			t.Errorf("%s 后端未拒绝零租户 Match", name)
		}
		if _, err := gs.CountTriples(ctx, ids.ID{}, testGraphIRI); err == nil {
			t.Errorf("%s 后端未拒绝零租户 CountTriples", name)
		}
		if _, err := gs.EnsureGraph(ctx, ids.ID{}, testGraphIRI, "INSTANCE", ""); err == nil {
			t.Errorf("%s 后端未拒绝零租户 EnsureGraph", name)
		}
	}
}

func TestMemStoreCapabilitiesMatchPGStore(t *testing.T) {
	pg := (&PGStore{}).Capabilities()
	mem := NewMemStore().Capabilities()
	fk := (&FusekiStore{}).Capabilities()
	if pg != mem {
		t.Fatalf("PG 与 Mem 的能力声明必须一致（Mem 用于替代 PG 跑单测）：%+v vs %+v", pg, mem)
	}
	if pg == fk {
		t.Fatal("Fuseki 的能力声明不应与 PG 相同（否则等于隐式伪造能力）")
	}
}
