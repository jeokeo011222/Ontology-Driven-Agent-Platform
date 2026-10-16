package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"odap/internal/ontology/graphstore"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/tenantctx"
)

// ---------------------------------------------------------------------------
// tool 注册入口
// ---------------------------------------------------------------------------

func (s *MCPServer) registerTools() {
	s.mcp.AddTool(
		mcp.NewTool("ontology_list",
			mcp.WithDescription("列出当前租户下所有已发布本体及其版本摘要。只读。"),
			mcp.WithInteger("limit",
				mcp.Description("最多返回多少条；默认 50，上限 200"),
			),
		),
		s.handleOntologyList,
	)

	s.mcp.AddTool(
		mcp.NewTool("ontology_get_shapes",
			mcp.WithDescription("获取指定本体的 SHACL 形状约束（命名图中的 TBox + SHACL triples）。"),
			mcp.WithString("ontology_id",
				mcp.Description("本体 UUID"),
				mcp.Required(),
			),
			mcp.WithInteger("limit",
				mcp.Description("返回三元组上限；默认 200"),
			),
		),
		s.handleOntologyGetShapes,
	)

	s.mcp.AddTool(
		mcp.NewTool("ontology_search_instances",
			mcp.WithDescription("在图存储中按 (subject, predicate, object) 模式搜索实例（至少提供其中一个）。"),
			mcp.WithString("subject_iri", mcp.Description("主语 IRI（可选）")),
			mcp.WithString("predicate_iri", mcp.Description("谓词 IRI（可选）")),
			mcp.WithString("object_iri", mcp.Description("宾语 IRI（可选）")),
			mcp.WithString("graph_iri", mcp.Description("限定命名图（可选）")),
			mcp.WithInteger("limit", mcp.Description("返回上限；默认 50")),
		),
		s.handleOntologySearchInstances,
	)

	s.mcp.AddTool(
		mcp.NewTool("knowledge_search",
			mcp.WithDescription("企业知识库混合检索（向量 + 词项），返回 topK 相关分块。"),
			mcp.WithString("query", mcp.Description("自然语言查询文本（向量腿用这个）"), mcp.Required()),
			mcp.WithInteger("top_k", mcp.Description("返回上限；默认 5")),
		),
		s.handleKnowledgeSearch,
	)

	s.mcp.AddTool(
		mcp.NewTool("agent_invoke",
			mcp.WithDescription("调用指定 Agent 创建一次 Run（异步任务，返回 run_id 供后续查询）。"),
			mcp.WithString("agent_key", mcp.Description("Agent 的 ontology_key（唯一标识）"), mcp.Required()),
			mcp.WithString("input_json", mcp.Description("Agent 输入 JSON 字符串（可选，空则用默认 prompt）")),
		),
		s.handleAgentInvoke,
	)
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func tenantFromCtx(ctx context.Context) (ids.ID, error) {
	id, ok := tenantctx.FromContext(ctx)
	if !ok || id.TenantID.IsZero() {
		return ids.ID{}, errors.E(errors.CodeUnauthenticated, "缺少租户身份（请确认 MCP 端点挂在认证链下）")
	}
	return id.TenantID, nil
}

func textResult(v any) *mcp.CallToolResult {
	var text string
	switch t := v.(type) {
	case string:
		text = t
	case error:
		text = t.Error()
	default:
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			text = fmt.Sprintf("%+v", v)
		} else {
			text = string(b)
		}
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{mcp.NewTextContent(text)},
	}
}

func errorResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{mcp.NewTextContent(fmt.Sprintf("tool error: %s", err.Error()))},
		IsError: true,
	}
}

// ---------------------------------------------------------------------------
// ontology_list
// ---------------------------------------------------------------------------

func (s *MCPServer) handleOntologyList(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	tenantID, err := tenantFromCtx(ctx)
	if err != nil {
		return errorResult(err), nil
	}
	if s.deps.Ontology == nil {
		return errorResult(errors.E(errors.CodeUnavailable, "ontology lister 未装配")), nil
	}
	args := req.GetArguments()
	limit := 50
	if raw, ok := args["limit"]; ok {
		if n, ok := toInt(raw); ok && n > 0 && n <= 200 {
			limit = n
		}
	}
	list, err := s.deps.Ontology.ListPublishedOntologies(ctx, tenantID, limit)
	if err != nil {
		return errorResult(err), nil
	}
	return textResult(list), nil
}

// ---------------------------------------------------------------------------
// ontology_get_shapes
// ---------------------------------------------------------------------------

func (s *MCPServer) handleOntologyGetShapes(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	tenantID, err := tenantFromCtx(ctx)
	if err != nil {
		return errorResult(err), nil
	}
	if s.deps.Graph == nil {
		return errorResult(errors.E(errors.CodeUnavailable, "graph store 未装配")), nil
	}
	args := req.GetArguments()
	oid, _ := args["ontology_id"].(string)
	if oid == "" {
		return errorResult(errors.E(errors.CodeInvalidArgument, "ontology_id 是必填参数")), nil
	}
	if _, err := ids.Parse(oid); err != nil {
		return errorResult(errors.E(errors.CodeInvalidArgument, "ontology_id 必须是合法 UUID")), nil
	}
	limit := 200
	if raw, ok := args["limit"]; ok {
		if n, ok := toInt(raw); ok && n > 0 && n <= 2000 {
			limit = n
		}
	}
	graphIRI := "on:" + oid
	triples, err := s.deps.Graph.Match(ctx, tenantID, graphstore.Pattern{
		GraphIRI: graphIRI,
		Limit:    limit,
	})
	if err != nil {
		return errorResult(err), nil
	}
	return textResult(map[string]any{
		"graph_iri": graphIRI,
		"count":     len(triples),
		"triples":   formatTriples(triples),
	}), nil
}

// ---------------------------------------------------------------------------
// ontology_search_instances
// ---------------------------------------------------------------------------

func (s *MCPServer) handleOntologySearchInstances(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	tenantID, err := tenantFromCtx(ctx)
	if err != nil {
		return errorResult(err), nil
	}
	if s.deps.Graph == nil {
		return errorResult(errors.E(errors.CodeUnavailable, "graph store 未装配")), nil
	}
	args := req.GetArguments()

	var pat graphstore.Pattern
	if v, ok := args["subject_iri"].(string); ok && v != "" {
		t := graphstore.Term{Kind: graphstore.KindIRI, Lexical: v}
		pat.S = &t
	}
	if v, ok := args["predicate_iri"].(string); ok && v != "" {
		t := graphstore.Term{Kind: graphstore.KindIRI, Lexical: v}
		pat.P = &t
	}
	if v, ok := args["object_iri"].(string); ok && v != "" {
		t := graphstore.Term{Kind: graphstore.KindIRI, Lexical: v}
		pat.O = &t
	}
	if v, ok := args["graph_iri"].(string); ok {
		pat.GraphIRI = v
	}
	if pat.S == nil && pat.P == nil && pat.O == nil {
		return errorResult(errors.E(errors.CodeInvalidArgument,
			"subject_iri / predicate_iri / object_iri 至少提供一个")), nil
	}
	pat.Limit = 50
	if raw, ok := args["limit"]; ok {
		if n, ok := toInt(raw); ok && n > 0 && n <= 500 {
			pat.Limit = n
		}
	}
	triples, err := s.deps.Graph.Match(ctx, tenantID, pat)
	if err != nil {
		return errorResult(err), nil
	}
	return textResult(map[string]any{
		"count":   len(triples),
		"results": formatTriples(triples),
	}), nil
}

// ---------------------------------------------------------------------------
// knowledge_search —— 混合检索（向量腿 + 词项腿，RRF 融合）
// ---------------------------------------------------------------------------
//
// 容错降级策略：
//   - 向量腿（SearchVector）返回 CodeUnavailable → 嵌入链路未配置，**降级只用词项腿**
//     （不阻断，用户能看到词项命中的结果）。
//   - 词项腿（SearchTerms）返回 CodeUnavailable → Store/pg_trgm 未装配，**致命错误**
//     （词项腿是知识库检索的最底线）。
//   - 其他错误（网络、数据库）→ 两路都报错时返回词项腿的错误。

const rrfK = 60.0 // Reciprocal Rank Fusion 的 k 参数（标准值 60）

func (s *MCPServer) handleKnowledgeSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	tenantID, err := tenantFromCtx(ctx)
	if err != nil {
		return errorResult(err), nil
	}
	if s.deps.KB == nil {
		return errorResult(errors.E(errors.CodeUnavailable, "knowledge searcher 未装配")), nil
	}
	args := req.GetArguments()
	query, _ := args["query"].(string)
	if query == "" {
		return errorResult(errors.E(errors.CodeInvalidArgument, "query 是必填参数")), nil
	}
	topK := 5
	if raw, ok := args["top_k"]; ok {
		if n, ok := toInt(raw); ok && n > 0 && n <= 50 {
			topK = n
		}
	}

	// 1. 并行跑两条腿（在同一 goroutine 里顺序调，避免并发复杂度；两路都是 DB 查询不耗时）。
	terms := tokenize(query)
	termHits, termErr := s.deps.KB.SearchTerms(ctx, tenantID, terms, topK)
	vecHits, vecErr := s.deps.KB.SearchVector(ctx, tenantID, "", query, topK)

	// 2. 词项腿是底线——如果 Store/词项检索都不可用，整个 tool 报错。
	if termErr != nil && !isUnavailable(termErr) {
		return errorResult(termErr), nil
	}
	// 3. 向量腿 CodeUnavailable → 优雅降级（嵌入链路没配置，词项腿还能用）。
	var vecAvailable bool
	switch {
	case vecErr == nil:
		vecAvailable = true
	case isUnavailable(vecErr):
		// 嵌入链路未就绪，跳过向量腿；但如果词项腿也挂了，还是报错。
		vecAvailable = false
	default:
		// 其他错误（网络、DB 瞬时失败）——也优雅降级，不阻断。
		vecAvailable = false
	}

	if len(termHits) == 0 && !vecAvailable {
		// 两条腿都没东西（向量腿也不可用）。
		return textResult(map[string]any{
			"query":         query,
			"rrf_applied":   false,
			"vec_available": false,
			"hits":          []any{},
			"note":          "词项腿无命中，向量腿未配置嵌入模型——请确认知识库已上传文档且嵌入模型已就绪",
		}), nil
	}

	// 4. 单腿直接用；双腿做 RRF 融合。
	var merged []rankedHit
	if vecAvailable && len(vecHits) > 0 && len(termHits) > 0 {
		merged = rrfMerge(rrfK, vecHits, termHits)
	} else if vecAvailable && len(vecHits) > 0 {
		for i, h := range vecHits {
			merged = append(merged, rankedHit{hit: h, rrf: 1 / (rrfK + float64(i+1))})
		}
	} else {
		for i, h := range termHits {
			merged = append(merged, rankedHit{hit: h, rrf: 1 / (rrfK + float64(i+1))})
		}
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].rrf > merged[j].rrf })

	// 5. 截断到 topK。
	if len(merged) > topK {
		merged = merged[:topK]
	}
	out := make([]VectorHit, len(merged))
	for i, m := range merged {
		out[i] = m.hit
	}
	return textResult(map[string]any{
		"query":         query,
		"rrf_applied":   vecAvailable && len(vecHits) > 0 && len(termHits) > 0,
		"vec_available": vecAvailable,
		"hits":          formatKBHits(out),
	}), nil
}

// rankedHit 是 RRF 融合后的单条命中。
type rankedHit struct {
	hit VectorHit
	rrf float64
}

// rrfMerge 用 Reciprocal Rank Fusion 融合多条排序结果。
// k 是 RRF 参数（标准 60）；排名从 1 开始。
// 同一条目（DocumentID+ChunkIndex 键）在多路出现时 RRF 分数累加。
func rrfMerge(k float64, legs ...[]VectorHit) []rankedHit {
	scoreByKey := map[string]float64{}
	hitsByKey := map[string]VectorHit{}
	for _, leg := range legs {
		for i, h := range leg {
			key := h.DocumentID + "|" + fmt.Sprintf("%d", h.ChunkIndex)
			scoreByKey[key] += 1 / (k + float64(i+1))
			if _, ok := hitsByKey[key]; !ok {
				hitsByKey[key] = h
			}
		}
	}
	out := make([]rankedHit, 0, len(scoreByKey))
	for key, s := range scoreByKey {
		out = append(out, rankedHit{hit: hitsByKey[key], rrf: s})
	}
	return out
}

// isUnavailable 判断 err 是否是 CodeUnavailable（适配器未装配 fail-closed）。
func isUnavailable(err error) bool {
	if err == nil {
		return false
	}
	ae := errors.As(err)
	return ae != nil && ae.Code == errors.CodeUnavailable
}

// ---------------------------------------------------------------------------
// agent_invoke
// ---------------------------------------------------------------------------

func (s *MCPServer) handleAgentInvoke(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	tenantID, err := tenantFromCtx(ctx)
	if err != nil {
		return errorResult(err), nil
	}
	if s.deps.Agent == nil {
		return errorResult(errors.E(errors.CodeUnavailable, "agent runner 未装配")), nil
	}
	args := req.GetArguments()
	agentKey, _ := args["agent_key"].(string)
	if agentKey == "" {
		return errorResult(errors.E(errors.CodeInvalidArgument, "agent_key 是必填参数")), nil
	}
	input := map[string]any{}
	if raw, ok := args["input_json"].(string); ok && raw != "" {
		if err := json.Unmarshal([]byte(raw), &input); err != nil {
			return errorResult(errors.E(errors.CodeInvalidArgument, "input_json 不是合法 JSON")), nil
		}
	}
	runID, err := s.deps.Agent.InvokeAgent(ctx, tenantID, agentKey, input)
	if err != nil {
		return errorResult(err), nil
	}
	return textResult(map[string]any{
		"run_id":    runID,
		"agent_key": agentKey,
		"status":    "CREATED",
	}), nil
}

// ---------------------------------------------------------------------------
// 工具函数
// ---------------------------------------------------------------------------

func toInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case json.Number:
		n, err := t.Int64()
		if err != nil {
			return 0, false
		}
		return int(n), true
	}
	return 0, false
}

func formatTriples(triples []graphstore.Triple) []map[string]any {
	out := make([]map[string]any, len(triples))
	for i, t := range triples {
		out[i] = map[string]any{
			"graph":     t.GraphIRI,
			"subject":   termLex(t.S),
			"predicate": termLex(t.P),
			"object":    termLex(t.O),
			"kind":      t.FactKind,
		}
	}
	return out
}

func termLex(t graphstore.Term) string {
	if t.IsLiteral() {
		return fmt.Sprintf("\"%s\"^^%s", t.Lexical, t.Datatype)
	}
	return t.Lexical
}

func formatKBHits(hits []VectorHit) []map[string]any {
	out := make([]map[string]any, len(hits))
	for i, h := range hits {
		out[i] = map[string]any{
			"document_id": h.DocumentID,
			"title":       h.DocTitle,
			"chunk_index": h.ChunkIndex,
			"score":       roundScore(h.Score),
			"content":     truncate(h.Content, 400),
		}
	}
	return out
}

func roundScore(f float64) float64 {
	return float64(int(f*1000)) / 1000
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func tokenize(s string) []string {
	var out []string
	var cur []rune
	for _, r := range s {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '，' || r == '。' || r == '：' || r == '；' || r == '！' || r == '？':
			if len(cur) > 0 {
				out = append(out, string(cur))
				cur = nil
			}
		default:
			cur = append(cur, r)
		}
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	if len(out) == 0 {
		out = append(out, s)
	}
	return out
}

// ensure server package stays referenced (avoid unused import lint).
var _ server.ServerOption
