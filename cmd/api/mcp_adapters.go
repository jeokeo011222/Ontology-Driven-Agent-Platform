package main

// mcp_adapters.go 把 cmd/api 能拿到的具体依赖（Store、Catalog、GraphStore、
// LLM Gateway）适配为 mcp.Deps 的接口。
//
// 为什么放在 package main 而不是 internal/mcp：
//   internal/mcp 是领域层包，按 §4.3 架构规则不得反向依赖 internal/api/store。
//   适配器天生就是装配层（cmd/api）的职责——main.go 拿到所有组件实例后，
//   在此文件里做最后一层类型桥接。

import (
	"context"
	"encoding/json"

	"odap/internal/api/store"
	"odap/internal/knowledge"
	"odap/internal/mcp"
	"odap/internal/ontology/graphstore"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/tenantctx"
	"odap/internal/registry"
)

// ---------------------------------------------------------------------------
// OntologyLister 适配器
// ---------------------------------------------------------------------------
//
// 只返回 PUBLISHED 状态的本体（MCP 工具面向外部 AI，只能看到已发布版本）。

type ontologyListerAdapter struct {
	Store *store.Store
}

func (a *ontologyListerAdapter) ListPublishedOntologies(ctx context.Context, tenantID ids.ID, limit int) ([]map[string]any, error) {
	if a == nil || a.Store == nil {
		return nil, errors.E(errors.CodeUnavailable, "ontology store 未装配")
	}
	res, err := a.Store.ListOntologies(ctx, limit, "", "PUBLISHED", "")
	if err != nil {
		return nil, err
	}
	if res == nil {
		return []map[string]any{}, nil
	}
	return res.Items, nil
}

// ---------------------------------------------------------------------------
// AgentRunner 适配器
// ---------------------------------------------------------------------------

// agentRunnerAdapter 把 Store + CatalogService 适配为 mcp.AgentRunner。
//
// 调用链：
//  1. registry.CatalogService.ResolveAgentVersion — agent_key → 最新 PUBLISHED AgentVersion
//  2. store.Store.CreateRun — 写入 CREATED 状态的 Run（异步任务，返回 run_id）
//
// 注意：Run 的 request_by 取自 tenantctx.SubjectID；MCP 端点挂在认证链下
// （tenantctx 中必有 SubjectID），缺失时 fail-closed 返回 CodeUnauthenticated。
type agentRunnerAdapter struct {
	Store   *store.Store
	Catalog *registry.CatalogService
}

func (a *agentRunnerAdapter) InvokeAgent(ctx context.Context, tenantID ids.ID, agentKey string, input map[string]any) (string, error) {
	if a == nil || a.Store == nil || a.Catalog == nil {
		return "", errors.E(errors.CodeUnavailable, "agent runner 未装配")
	}
	if agentKey == "" {
		return "", errors.E(errors.CodeInvalidArgument, "agent_key 是必填参数")
	}

	// 1. 从 tenantctx 取 subject 身份（CreateRun 要求非零 RequestedBy）。
	id, ok := tenantctx.FromContext(ctx)
	if !ok || id.SubjectID.IsZero() {
		return "", errors.E(errors.CodeUnauthenticated, "缺少认证身份（MCP 端点必须挂在认证链下）")
	}

	// 2. agent_key → agent_version_id（只取 PUBLISHED 版本，运行期绝不绑定未发布版本）。
	ver, err := a.Catalog.ResolveAgentVersion(ctx, tenantID, agentKey, "")
	if err != nil {
		return "", err
	}

	// 3. 序列化 input 为 JSON（空输入用 {}）。
	var inputPayload []byte
	var inputDigest string
	if len(input) > 0 {
		b, merr := json.Marshal(input)
		if merr != nil {
			return "", errors.E(errors.CodeInvalidArgument, "input 序列化失败: "+merr.Error())
		}
		inputPayload = b
		inputDigest = ids.New().String() // 简单的幂等键占位
	} else {
		inputPayload = []byte(`{}`)
	}

	// 4. 创建 Run（CREATED 状态，由 harness.Scheduler 异步领取执行）。
	run, err := a.Store.CreateRun(ctx, store.CreateRunInput{
		RequestedBy:    id.SubjectID,
		AgentVersionID: ver.ID,
		InputPayload:   inputPayload,
		InputDigest:    inputDigest,
		Priority:       0,
		MaxAttempts:    3,
	})
	if err != nil {
		return "", err
	}
	if run == nil || run.ID == "" {
		return "", errors.E(errors.CodeInternal, "CreateRun 返回空结果")
	}
	return run.ID, nil
}

// ---------------------------------------------------------------------------
// KnowledgeSearcher —— 真实适配器
// ---------------------------------------------------------------------------
//
// knowledgeSearcherAdapter 桥接 Store + embed 闭包 + KBEmbeddingModel。
// 两条腿独立 fail-closed：
//   - SearchTerms 只依赖 pg_trgm 扩展，Store 可用就可用（trigram 相似度）。
//   - SearchVector 需要 LLM Gateway.Embed + KBEmbeddingModel 双重就绪。
//     任一缺失时 SearchVector 返回 CodeUnavailable——但 SearchTerms 仍可用，
//     绝不整条链路静默降级成"没东西"。

// EmbedFn 是嵌入函数闭包签名。与 action.EmbedFn 底层完全一致但不引入 action 依赖；
// 组装时直接复用 action.Recorder 构造的网关闭包即可。

type knowledgeSearcherAdapter struct {
	Store            *store.Store
	EmbedFn          func(ctx context.Context, tenantID ids.ID, texts []string) ([][]float32, error)
	KBEmbeddingModel string // 空则 SearchVector fail-closed
}

// SearchVector 走嵌入腿：queryText → embed → pgvector 余弦 topK。
// embeddingModel 参数允许调用方点名覆盖适配器默认模型（空则退回 KBEmbeddingModel）。
func (a *knowledgeSearcherAdapter) SearchVector(ctx context.Context, tenantID ids.ID, embeddingModel, queryText string, topK int) ([]mcp.VectorHit, error) {
	if a == nil || a.Store == nil {
		return nil, errors.E(errors.CodeUnavailable, "knowledge store 未装配")
	}
	model := embeddingModel
	if model == "" {
		model = a.KBEmbeddingModel
	}
	if model == "" {
		return nil, errors.E(errors.CodeUnavailable,
			"knowledge vector search 未配置嵌入模型（ODAP_KB_EMBEDDING_MODEL），请启用后再试")
	}
	if a.EmbedFn == nil {
		return nil, errors.E(errors.CodeUnavailable,
			"knowledge vector search 未装配（LLM Gateway Embed 不可用）")
	}
	// 1. queryText → 向量（单点嵌入，batch=1）。
	vectors, err := a.EmbedFn(ctx, tenantID, []string{queryText})
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeUnavailable, "query embedding 失败")
	}
	if len(vectors) == 0 || len(vectors[0]) == 0 {
		return nil, errors.E(errors.CodeInternal, "embed 返回空向量")
	}
	// 2. 格式化为 pgvector 字面量。
	queryVecLiteral := knowledge.VectorLiteral(vectors[0])
	// 3. pgvector 余弦检索。
	hits, err := a.Store.SearchKnowledgeVector(ctx, model, queryVecLiteral, topK, nil)
	if err != nil {
		return nil, err
	}
	return hitsToVectorHits(hits), nil
}

// SearchTerms 走词项腿：trigram 相似度 + 子串命中。
func (a *knowledgeSearcherAdapter) SearchTerms(ctx context.Context, tenantID ids.ID, terms []string, topK int) ([]mcp.VectorHit, error) {
	if a == nil || a.Store == nil {
		return nil, errors.E(errors.CodeUnavailable, "knowledge store 未装配")
	}
	hits, err := a.Store.SearchKnowledgeTerms(ctx, terms, topK, nil)
	if err != nil {
		return nil, err
	}
	return hitsToVectorHits(hits), nil
}

// hitsToVectorHits 把 store.KnowledgeHit 映射为 mcp.VectorHit。
func hitsToVectorHits(hits []store.KnowledgeHit) []mcp.VectorHit {
	out := make([]mcp.VectorHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, mcp.VectorHit{
			DocumentID: h.DocumentID,
			DocTitle:   h.DocTitle,
			ChunkIndex: h.ChunkIndex,
			Content:    h.Content,
			Score:      h.Score,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// 编译期接口校验（package main 内，防漂移）
// ---------------------------------------------------------------------------

var (
	_ mcp.OntologyLister    = (*ontologyListerAdapter)(nil)
	_ mcp.AgentRunner       = (*agentRunnerAdapter)(nil)
	_ mcp.KnowledgeSearcher = (*knowledgeSearcherAdapter)(nil)
)

// graphReaderAdapter 保留但当前 main.go 直接把 graphstore.GraphStore 赋给
// mcp.Deps.Graph（签名完全一致，省一层包装）。
type graphReaderAdapter struct {
	GS graphstore.GraphStore
}

func (a *graphReaderAdapter) Match(ctx context.Context, tenantID ids.ID, p graphstore.Pattern) ([]graphstore.Triple, error) {
	if a == nil || a.GS == nil {
		return nil, errors.E(errors.CodeUnavailable, "graph store 未装配")
	}
	return a.GS.Match(ctx, tenantID, p)
}

var _ mcp.GraphReader = (*graphReaderAdapter)(nil)
