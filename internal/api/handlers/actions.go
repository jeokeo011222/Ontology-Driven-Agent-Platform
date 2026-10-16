package handlers

// Action 查询端点（模块 D「本体 Action 管理」的 HTTP 读侧）。
//
// 路由（由 router.go 装配）：
//   GET  /api/v1/actions                → ListActions
//   GET  /api/v1/actions/{id}           → GetAction
//   POST /api/v1/actions/{id}/similar   → SimilarActions
//   GET  /api/v1/actions/{id}/prov.ttl  → ActionProvTurtle
//
// # 审计与自举
//
// 本文件所有端点都**不**记 Action 审计：Action 记录由 runs/tool_executors/
// reasoning/knowledge/A2A 各业务流经 RecordActionForFlow 写入；而"查询动作/
// 相似检索"本身若再记一条 QUERY 型 Action，会形成自举（每次查询都生成一条
// 新动作，下次查询又能检索到它……），污染相似检索空间并撑爆溯源图。审计
// 哈希链照旧由各业务流调用方的 recordAudit 负责，与端点无关。
//
// # 装配点（由 main.go/router.go 完成，本文件不碰 handlers.go）
//
// Deps 需要新增字段 `Actions *action.Recorder`（记录器本体）；RecordActionForFlow
// 在该字段未装配时 WARN 一次并返回，绝不 panic、绝不阻断业务流。

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"odap/internal/action"
	"odap/internal/api/store"
	"odap/internal/ontology/graphstore"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/tenantctx"
)

// warnActionsRecorderOnce 保证"Recorder 未装配"的告警每进程只发一次
// （业务流高频调用 RecordActionForFlow，重复告警只会刷屏）。
var warnActionsRecorderOnce sync.Once

// RecordActionForFlow 供各业务流（runs/tool_executors/reasoning/knowledge/A2A）
// 记录动作。租户由调用方显式提供（覆盖 in.TenantID，以调用方上下文为准）。
//
// 契约：本方法**绝不**因记录失败向业务流返回错误——动作记录是旁路观测，
// 失败只留 ERROR 日志供监控补录；Recorder 自身的嵌入失败更是连日志都不该
// 打扰主流程（由 Recorder 落 embed_status 显式留痕）。
func (h *Handler) RecordActionForFlow(ctx context.Context, tenantID ids.ID, in action.Input) {
	if h.deps.Actions == nil {
		warnActionsRecorderOnce.Do(func() {
			if h.deps.Logger != nil {
				h.deps.Logger.Warn("action.Recorder 未装配，业务流动作记录已跳过" +
					"（装配点：Deps.Actions = action.NewRecorder(pool, graphStore, embedAdapter, modelID)）")
			}
		})
		return
	}
	in.TenantID = tenantID
	if err := h.deps.Actions.Record(ctx, in); err != nil {
		if h.deps.Logger != nil {
			h.deps.Logger.Error("业务流动作记录失败（主流程不阻断，需监控补录）",
				"tenant_id", tenantID.String(), "type", in.Type, "title", in.Title, "error", err)
		}
	}
}

// requireActionDeps 是查询端点的公共前置：身份 + 持久化适配器可用性。
func (h *Handler) requireActionDeps(w http.ResponseWriter, r *http.Request) (tenantctx.Identity, bool) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return tenantctx.Identity{}, false
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配，动作查询不可用"))
		return tenantctx.Identity{}, false
	}
	return id, true
}

// actionTimeParam 解析 RFC3339 时间查询参数（空串 → nil）。
func actionTimeParam(r *http.Request, name string) (*time.Time, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, errors.E(errors.CodeInvalidArgument, name+" 必须是 RFC3339 时间（如 2026-10-16T00:00:00Z）")
	}
	return &t, nil
}

// ListActions 处理 GET /api/v1/actions（筛选 + cursor 10/页）。
//
// 不记审计：见文件头"审计与自举"。
func (h *Handler) ListActions(w http.ResponseWriter, r *http.Request) {
	_, ok := h.requireActionDeps(w, r)
	if !ok {
		return
	}
	limit, err := limitParam(r, 10)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	from, err := actionTimeParam(r, "from")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	to, err := actionTimeParam(r, "to")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	res, err := h.deps.Store.ListActions(r.Context(), store.ActionListFilter{
		Type:      r.URL.Query().Get("type"),
		Outcome:   r.URL.Query().Get("outcome"),
		ActorKind: r.URL.Query().Get("actor_kind"),
		From:      from,
		To:        to,
		Q:         r.URL.Query().Get("q"),
		Limit:     limit,
		Cursor:    r.URL.Query().Get("cursor"),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, res)
}

// GetAction 处理 GET /api/v1/actions/{id}：行字段 + 关联 run 摘要 + PROV-O 三元组。
//
// 不记审计：见文件头"审计与自举"。
func (h *Handler) GetAction(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireActionDeps(w, r)
	if !ok {
		return
	}
	actionID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	row, err := h.deps.Store.GetAction(r.Context(), actionID)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// 关联 run 摘要（best-effort：run 已被清理/不存在时置 null，不拖垮详情页；
	// 其他内部错误则如实失败）。
	out := row
	out["run"] = nil
	if raw, has := row["run_id"].(string); has && raw != "" {
		if runID, perr := ids.Parse(raw); perr == nil {
			run, gerr := h.deps.Store.GetRun(r.Context(), runID)
			switch {
			case gerr == nil:
				out["run"] = map[string]any{
					"id":            run.ID,
					"status":        run.Status,
					"objective":     run.Objective,
					"error_summary": run.ErrorSummary,
					"created_at":    run.CreatedAt,
					"finished_at":   run.FinishedAt,
				}
			case errors.As(gerr).Code != errors.CodeNotFound:
				h.fail(w, r, gerr)
				return
			}
		}
	}

	// PROV-O 三元组读回（从 prov_graph_iri 指向的命名图）。
	provIRI, _ := row["prov_graph_iri"].(string)
	switch {
	case provIRI == "":
		out["prov_triples"] = []map[string]any{}
	case h.deps.GraphStore == nil:
		// 诚实标注而不是静默空列表：图存储未装配是装配缺陷，不能伪装成"没有溯源"。
		out["prov_triples"] = []map[string]any{}
		out["prov_unavailable"] = "图存储未装配，无法读取 PROV-O 三元组"
	default:
		ts, merr := h.deps.GraphStore.Match(r.Context(), id.TenantID,
			graphstore.Pattern{GraphIRI: provIRI})
		if merr != nil {
			h.fail(w, r, merr)
			return
		}
		out["prov_triples"] = renderProvTriples(ts)
	}
	h.writeJSON(w, http.StatusOK, out)
}

// actionSimilarRequest 是 POST /actions/{id}/similar 的请求体。
type actionSimilarRequest struct {
	TopK int `json:"top_k"`
}

// SimilarActions 处理 POST /api/v1/actions/{id}/similar（余弦距离升序 topK）。
//
// 不记审计：见文件头"审计与自举"（similar 记一条 QUERY 型 Action 会自举）。
func (h *Handler) SimilarActions(w http.ResponseWriter, r *http.Request) {
	_, ok := h.requireActionDeps(w, r)
	if !ok {
		return
	}
	actionID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var req actionSimilarRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	if req.TopK < 0 {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "top_k 不能为负"))
		return
	}
	items, err := h.deps.Store.SimilarActions(r.Context(), actionID, req.TopK)
	if err != nil {
		// 409「该动作尚未完成嵌入补算」按 store 层语义原样透传。
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// ActionProvTurtle 处理 GET /api/v1/actions/{id}/prov.ttl（Turtle 导出）。
//
// 不记审计：见文件头"审计与自举"。Content-Type 为 text/turtle 而非 JSON。
func (h *Handler) ActionProvTurtle(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireActionDeps(w, r)
	if !ok {
		return
	}
	actionID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	row, err := h.deps.Store.GetAction(r.Context(), actionID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	provIRI, _ := row["prov_graph_iri"].(string)
	if strings.TrimSpace(provIRI) == "" {
		h.fail(w, r, errors.E(errors.CodeNotFound, "该动作没有 PROV-O 溯源图"))
		return
	}
	if h.deps.GraphStore == nil {
		h.fail(w, r, unavailable("图存储未装配，无法导出 PROV-O Turtle"))
		return
	}
	ts, err := h.deps.GraphStore.Match(r.Context(), id.TenantID,
		graphstore.Pattern{GraphIRI: provIRI})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/turtle; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(action.Turtle(provIRI, ts)))
}

// actionTermKind 把图存储词项种类映射为 API 词汇（读侧展示，不发明新概念）。
func actionTermKind(k uint8) string {
	switch k {
	case graphstore.KindIRI:
		return "IRI"
	case graphstore.KindBlankNode:
		return "BLANK_NODE"
	case graphstore.KindLiteral:
		return "LITERAL"
	}
	return "UNKNOWN"
}

// actionTermMap 把词项渲染为结构化 JSON（kind/value/datatype/lang），
// 与 N-Triples 文本相比可读且可程序化消费。
func actionTermMap(t graphstore.Term) map[string]any {
	out := map[string]any{"kind": actionTermKind(t.Kind), "value": t.Lexical}
	if t.Datatype != "" {
		out["datatype"] = t.Datatype
	}
	if t.Lang != "" {
		out["lang"] = t.Lang
	}
	return out
}

// renderProvTriples 把溯源三元组渲染为详情页的 prov_triples 数组。
func renderProvTriples(ts []graphstore.Triple) []map[string]any {
	out := make([]map[string]any, 0, len(ts))
	for _, tr := range ts {
		out = append(out, map[string]any{
			"subject":     actionTermMap(tr.S),
			"predicate":   actionTermMap(tr.P),
			"object":      actionTermMap(tr.O),
			"graph_iri":   tr.GraphIRI,
			"fact_kind":   tr.FactKind,
			"observed_at": tr.ObservedAt,
		})
	}
	return out
}
