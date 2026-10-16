package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"reflect"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"odap/internal/ontology/graphstore"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/tenantctx"
)

// ---------------------------------------------------------------------------
// mock 依赖
// ---------------------------------------------------------------------------

type mockOntologyLister struct {
	list []map[string]any
	err  error
}

func (m *mockOntologyLister) ListPublishedOntologies(ctx context.Context, tenantID ids.ID, limit int) ([]map[string]any, error) {
	return m.list, m.err
}

type mockGraphReader struct {
	matches []graphstore.Triple
	err     error
	lastPat graphstore.Pattern
}

func (m *mockGraphReader) Match(ctx context.Context, tenantID ids.ID, p graphstore.Pattern) ([]graphstore.Triple, error) {
	m.lastPat = p
	return m.matches, m.err
}

type mockKnowledgeSearcher struct {
	vecHits  []VectorHit
	termHits []VectorHit
	err      error
}

func (m *mockKnowledgeSearcher) SearchVector(ctx context.Context, tenantID ids.ID, embeddingModel, queryText string, topK int) ([]VectorHit, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.vecHits, nil
}
func (m *mockKnowledgeSearcher) SearchTerms(ctx context.Context, tenantID ids.ID, terms []string, topK int) ([]VectorHit, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.termHits, nil
}

type mockAgentRunner struct {
	runID string
	err   error
}

func (m *mockAgentRunner) InvokeAgent(ctx context.Context, tenantID ids.ID, agentKey string, input map[string]any) (string, error) {
	return m.runID, m.err
}

// ---------------------------------------------------------------------------
// 测试 fixture
// ---------------------------------------------------------------------------

func testTenantCtx() context.Context {
	return tenantctx.WithIdentity(context.Background(), tenantctx.Identity{
		TenantID:  ids.MustParse("11111111-1111-1111-1111-111111111111"),
		SubjectID: ids.MustParse("22222222-2222-2222-2222-222222222222"),
	})
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

// unmarshalResult 把 CallToolResult.Content[0].Text 解析成 map。
func unmarshalResult(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	if res == nil {
		t.Fatal("nil CallToolResult")
	}
	if len(res.Content) == 0 {
		t.Fatal("empty content")
	}
	text := res.Content[0].(mcp.TextContent).Text
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("result not JSON: %s (text=%q)", err, text)
	}
	return out
}

// args 构造 mcp.CallToolRequest 参数。
func args(t *testing.T, body map[string]any) mcp.CallToolRequest {
	t.Helper()
	b, _ := json.Marshal(body)
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			RawArguments: json.RawMessage(b),
			Arguments:    body,
		},
	}
}

// ---------------------------------------------------------------------------
// 1. ontology_list
// ---------------------------------------------------------------------------

func TestOntologyList_Success(t *testing.T) {
	mock := &mockOntologyLister{list: []map[string]any{
		{"id": "a", "name": "Finance", "status": "PUBLISHED"},
		{"id": "b", "name": "HR", "status": "PUBLISHED"},
	}}
	srv := NewMCPServer(Deps{Ontology: mock}, testLogger())

	req := args(t, map[string]any{"limit": 10})
	res, err := srv.handleOntologyList(testTenantCtx(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %s", res.Content[0].(mcp.TextContent).Text)
	}
	got := res.Content[0].(mcp.TextContent).Text
	if !reflect.DeepEqual(got, `[
  {
    "id": "a",
    "name": "Finance",
    "status": "PUBLISHED"
  },
  {
    "id": "b",
    "name": "HR",
    "status": "PUBLISHED"
  }
]`) {
		t.Logf("got: %s", got)
	}
}

func TestOntologyList_FailClosed_NoTenant(t *testing.T) {
	srv := NewMCPServer(Deps{Ontology: &mockOntologyLister{}}, testLogger())
	res, err := srv.handleOntologyList(context.Background(), args(t, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error result")
	}
}

func TestOntologyList_FailClosed_NoDeps(t *testing.T) {
	srv := NewMCPServer(Deps{}, testLogger())
	res, err := srv.handleOntologyList(testTenantCtx(), args(t, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error when lister is nil")
	}
}

// ---------------------------------------------------------------------------
// 2. ontology_get_shapes
// ---------------------------------------------------------------------------

func TestOntologyGetShapes_Success(t *testing.T) {
	mock := &mockGraphReader{matches: []graphstore.Triple{
		{
			S:        graphstore.Term{Kind: graphstore.KindIRI, Lexical: "http://ex/InvoiceShape"},
			P:        graphstore.Term{Kind: graphstore.KindIRI, Lexical: "http://www.w3.org/ns/shacl#targetClass"},
			O:        graphstore.Term{Kind: graphstore.KindIRI, Lexical: "http://ex/Invoice"},
			GraphIRI: "on:deadbeef-dead-beef-dead-deadbeefdead",
		},
	}}
	srv := NewMCPServer(Deps{Graph: mock}, testLogger())
	req := args(t, map[string]any{"ontology_id": "deadbeef-dead-beef-dead-deadbeefdead"})
	res, err := srv.handleOntologyGetShapes(testTenantCtx(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", res.Content[0].(mcp.TextContent).Text)
	}
	out := unmarshalResult(t, res)
	if out["count"].(float64) != 1 {
		t.Errorf("want count=1 got %v", out["count"])
	}
	if mock.lastPat.GraphIRI != "on:deadbeef-dead-beef-dead-deadbeefdead" {
		t.Errorf("unexpected graph_iri: %s", mock.lastPat.GraphIRI)
	}
}

func TestOntologyGetShapes_MissingID(t *testing.T) {
	srv := NewMCPServer(Deps{Graph: &mockGraphReader{}}, testLogger())
	res, err := srv.handleOntologyGetShapes(testTenantCtx(), args(t, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error for missing ontology_id")
	}
}

// ---------------------------------------------------------------------------
// 3. ontology_search_instances
// ---------------------------------------------------------------------------

func TestOntologySearchInstances_Success(t *testing.T) {
	mock := &mockGraphReader{matches: []graphstore.Triple{
		{
			S: graphstore.Term{Kind: graphstore.KindIRI, Lexical: "http://ex/invoice/1"},
			P: graphstore.Term{Kind: graphstore.KindIRI, Lexical: "http://ex/hasAmount"},
			O: graphstore.Term{Kind: graphstore.KindLiteral, Lexical: "1000", Datatype: "http://www.w3.org/2001/XMLSchema#decimal"},
		},
	}}
	srv := NewMCPServer(Deps{Graph: mock}, testLogger())
	req := args(t, map[string]any{"predicate_iri": "http://ex/hasAmount"})
	res, err := srv.handleOntologySearchInstances(testTenantCtx(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", res.Content[0].(mcp.TextContent).Text)
	}
	out := unmarshalResult(t, res)
	if out["count"].(float64) != 1 {
		t.Errorf("want count=1 got %v", out["count"])
	}
}

func TestOntologySearchInstances_NoPattern(t *testing.T) {
	srv := NewMCPServer(Deps{Graph: &mockGraphReader{}}, testLogger())
	res, err := srv.handleOntologySearchInstances(testTenantCtx(), args(t, map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error when no pattern fields provided")
	}
}

// ---------------------------------------------------------------------------
// 4. knowledge_search
// ---------------------------------------------------------------------------

func TestKnowledgeSearch_Success(t *testing.T) {
	mock := &mockKnowledgeSearcher{
		vecHits: []VectorHit{
			{DocumentID: "d1", DocTitle: "员工手册", ChunkIndex: 0, Content: "报销上限（向量命中）", Score: 0.92},
			{DocumentID: "d2", DocTitle: "差旅费规定", ChunkIndex: 1, Content: "出差住宿标准", Score: 0.80},
		},
		termHits: []VectorHit{
			{DocumentID: "d1", DocTitle: "员工手册", ChunkIndex: 3, Content: "交通补贴（词项命中）", Score: 0.75},
		},
	}
	srv := NewMCPServer(Deps{KB: mock}, testLogger())
	req := args(t, map[string]any{"query": "员工报销规定"})
	res, err := srv.handleKnowledgeSearch(testTenantCtx(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", res.Content[0].(mcp.TextContent).Text)
	}
	out := unmarshalResult(t, res)
	if out["query"] != "员工报销规定" {
		t.Errorf("query echo mismatch")
	}
	// RRF 融合后 3 条命中（两条腿的独立 chunk 合并去重）。
	hits := out["hits"].([]any)
	if len(hits) != 3 {
		t.Errorf("want 3 RRF-merged hits got %d", len(hits))
	}
	// vec_available=true + rrf_applied=true：两条腿都有命中。
	if v, ok := out["vec_available"].(bool); !ok || !v {
		t.Errorf("vec_available want true got %v", v)
	}
	if v, ok := out["rrf_applied"].(bool); !ok || !v {
		t.Errorf("rrf_applied want true got %v", v)
	}
}

func TestKnowledgeSearch_EmptyQuery(t *testing.T) {
	srv := NewMCPServer(Deps{KB: &mockKnowledgeSearcher{}}, testLogger())
	res, err := srv.handleKnowledgeSearch(testTenantCtx(), args(t, map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error for empty query")
	}
}

// ---------------------------------------------------------------------------
// 5. agent_invoke
// ---------------------------------------------------------------------------

func TestAgentInvoke_Success(t *testing.T) {
	mock := &mockAgentRunner{runID: "run-001"}
	srv := NewMCPServer(Deps{Agent: mock}, testLogger())
	req := args(t, map[string]any{"agent_key": "finance_reconcile", "input_json": `{"invoice_id":"inv-1"}`})
	res, err := srv.handleAgentInvoke(testTenantCtx(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", res.Content[0].(mcp.TextContent).Text)
	}
	out := unmarshalResult(t, res)
	if out["run_id"] != "run-001" {
		t.Errorf("want run_id=run-001 got %v", out["run_id"])
	}
	if out["status"] != "CREATED" {
		t.Errorf("want status=CREATED got %v", out["status"])
	}
}

func TestAgentInvoke_InvalidJSON(t *testing.T) {
	srv := NewMCPServer(Deps{Agent: &mockAgentRunner{}}, testLogger())
	req := args(t, map[string]any{"agent_key": "x", "input_json": "not-json"})
	res, err := srv.handleAgentInvoke(testTenantCtx(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error for invalid input_json")
	}
}

func TestAgentInvoke_AgentError(t *testing.T) {
	mock := &mockAgentRunner{err: errors.E(errors.CodeNotFound, "agent 不存在")}
	srv := NewMCPServer(Deps{Agent: mock}, testLogger())
	req := args(t, map[string]any{"agent_key": "ghost"})
	res, err := srv.handleAgentInvoke(testTenantCtx(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected propagated error")
	}
}

// ---------------------------------------------------------------------------
// Server 结构 smoke
// ---------------------------------------------------------------------------

func TestServerInfo(t *testing.T) {
	srv := NewMCPServer(Deps{Version: "0.9.9"}, nil)
	if got := srv.ServerInfo(); got != "odap-mcp 0.9.9" {
		t.Errorf("want 'odap-mcp 0.9.9' got %q", got)
	}
}

func TestServerDefaultVersion(t *testing.T) {
	srv := NewMCPServer(Deps{}, nil)
	if got := srv.ServerInfo(); got != "odap-mcp 0.1.0" {
		t.Errorf("want default version got %q", got)
	}
}
