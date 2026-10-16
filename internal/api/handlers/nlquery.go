// Package handlers — NL→SPARQL 自然语言问答端点。
//
// # 端点
//
// POST /api/v1/ontology/nl-query
//
// # 依赖检查（装配期 fail-closed）
//
// 三个前置能力任一缺失，端点明确返回 503 UNAVAILABLE 并给出原因，绝不伪造
// 一个 "查到了 N 条" 的空壳结果：
//   - Deps.Store          —— 读取本体 manifest（ontology_key → 版本快照）
//   - Deps.GraphStore     —— 执行 graphstore.Match
//   - Deps.LLM            —— 把自然语言转成结构化查询 pattern
//
// # 依赖方向（架构规则 §4.3）
//
// nlquery.Service 是领域层（ontology 子树），不得依赖 HTTP 传输层。
// 本文件里的 nlSnapshotReader 把 api/store.Store 适配为 nlquery.SnapshotReader
// 接口——这是传输层 → 领域层的依赖注入方向（合规）。
//
// # 审计
//
// 每次成功查询落一条 audit 事件 OntologyNlQuery / SUCCESS；失败落 DENIED
// 并携带 error_code，便于事后回溯"某次问答到底是模型幻觉还是本体不存在"。
//
// # 幂等
//
// Idempotency-Key 头从 handlers 公共辅助里自动回写到响应头（echoIdempotency）。
// 因为下游是只读查询 + Gateway.Complete 自身支持 IdempotencyKey（账本去重），
// 不需要额外的"相同请求直接返回"缓存层。
package handlers

import (
	"context"
	"net/http"
	"strings"

	"odap/internal/api/sse"
	apiStore "odap/internal/api/store"
	"odap/internal/ontology/nlquery"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// NlQueryRequest 是自然语言问答的入参。
type NlQueryRequest struct {
	OntologyKey string `json:"ontology_key"`
	Question    string `json:"question"`
	ModelID     string `json:"model_id,omitempty"`
	// TopK 是期望返回的三元组条数上限；<=0 取默认 50；上限 200。
	TopK int `json:"top_k,omitempty"`
}

// nlSnapshotReader 把 api/store.Store 适配为 nlquery.SnapshotReader。
//
// 两个结构体（api/store.OntologyVersionSnapshot 与 nlquery.OntologySnapshot）
// 字段语义一致但属于不同层，Go 的隐式接口无法跨层自动满足，这里显式转换。
type nlSnapshotReader struct {
	s *apiStore.Store
}

func (r *nlSnapshotReader) OntologyVersionSnapshotByKey(ctx context.Context, k string) (*nlquery.OntologySnapshot, error) {
	if r == nil || r.s == nil {
		return nil, errors.E(errors.CodeUnavailable, "nlSnapshotReader: Store 未装配")
	}
	snap, err := r.s.OntologyVersionSnapshotByKey(ctx, k)
	if err != nil {
		return nil, err
	}
	return &nlquery.OntologySnapshot{
		OntologyKey: snap.OntologyKey,
		Name:        snap.Name,
		VersionID:   snap.VersionID,
		Version:     snap.Version,
		Status:      snap.Status,
		Manifest:    snap.Manifest,
	}, nil
}

// NlQuery 处理 POST /api/v1/ontology/nl-query。
//
// 流程：
//  1. 读取可信身份 + 校验装配
//  2. 解析请求体（disallow unknown fields）
//  3. 构造 nlquery.Service → Query（内部完成 LLM 白名单校验 + GraphStore.Match）
//  4. 审计 + SSE 事件（best-effort）
//  5. 返回 { ontology_key, version_id, version, graph_iri, answer, triples, confidence, model }
func (h *Handler) NlQuery(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// 装配期 fail-closed：三缺一都无法给出可信回答。
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配（无法读取本体 manifest）"))
		return
	}
	if h.deps.GraphStore == nil {
		h.fail(w, r, unavailable("GraphStore 未装配（无法在语义图上执行查询）"))
		return
	}
	llmGW, err := h.llmGateway()
	if err != nil {
		h.fail(w, r, err)
		return
	}

	var req NlQueryRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	req.OntologyKey = strings.TrimSpace(req.OntologyKey)
	req.Question = strings.TrimSpace(req.Question)
	req.ModelID = strings.TrimSpace(req.ModelID)
	if req.OntologyKey == "" {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "ontology_key 不能为空"))
		return
	}
	if req.Question == "" {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "question 不能为空"))
		return
	}

	svc := &nlquery.Service{
		Reader:     &nlSnapshotReader{s: h.deps.Store},
		GraphStore: h.deps.GraphStore,
		LLM:        llmGW,
	}

	res, qerr := svc.Query(r.Context(), id.TenantID, req.OntologyKey, req.Question, req.ModelID, req.TopK)
	idemKey := idempotencyKey(r)

	if qerr != nil {
		// 失败审计：把错误码写进 outcome / payload，便于运营事后定位
		// "某次问答为什么失败"（本体不存在 / 模型幻觉 / GraphStore 不可达）。
		ae := errors.As(qerr)
		h.recordAudit(r, id, "OntologyNlQuery", "ontology.nl_query", "DENIED",
			"ontology", req.OntologyKey, ids.ID{}, map[string]any{
				"question":        req.Question,
				"top_k":           req.TopK,
				"error_code":      ae.Code,
				"error_message":   ae.Cause.Error(),
				"idempotency_key": idemKey,
			})
		h.fail(w, r, qerr)
		return
	}

	// 成功审计。
	h.recordAudit(r, id, "OntologyNlQuery", "ontology.nl_query", "SUCCESS",
		"ontology", res.OntologyKey, ids.ID{}, map[string]any{
			"question":        req.Question,
			"top_k":           req.TopK,
			"triple_count":    len(res.Triples),
			"confidence":      res.Confidence,
			"model":           res.Model,
			"version_id":      res.VersionID,
			"version":         res.Version,
			"idempotency_key": idemKey,
		})
	h.publish(id.TenantID.String(), "OntologyNlQuery", sse.Normal,
		map[string]any{
			"ontology_key": res.OntologyKey,
			"version_id":   res.VersionID,
			"triple_count": len(res.Triples),
		})

	echoIdempotency(w, r)
	h.writeJSON(w, http.StatusOK, res)
}
