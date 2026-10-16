package handlers

// ReasoningHandler 是推理引擎（模块 B）的 HTTP 适配层。
//
// 为什么是独立结构体而不是 Handler 的方法集：handlers.go 的 Deps 由装配侧
// 统一接线（不属于本模块的交付边界），这里以包级构造函数注入推理服务，
// 路由注册时由 cmd/api 调用 NewReasoningHandler + SetBase 完成挂接——
// 既不改 Deps 结构体，也让推理服务的缺失可以被显式探测（全部端点
// fail-closed 返回 UNAVAILABLE「推理引擎未装配」）。

import (
	"io"
	"net/http"
	"strconv"

	"odap/internal/action"
	"odap/internal/api/httperr"
	"odap/internal/api/middleware"
	"odap/internal/api/sse"
	"odap/internal/api/store"
	"odap/internal/ontology/graphload"
	"odap/internal/ontology/graphstore"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/reasoning"
)

// ReasoningHandler 承载推理引擎的 HTTP 处理器。
type ReasoningHandler struct {
	h   *Handler           // 基础设施（writeJSON/fail/audit/SSE/identity）
	svc *reasoning.Service // 推理服务；nil = 未装配
}

// NewReasoningHandler 构造推理引擎处理器（SetBase 注入基础 Handler）。
func NewReasoningHandler(svc *reasoning.Service) *ReasoningHandler {
	return &ReasoningHandler{svc: svc}
}

// SetBase 注入基础 Handler（路由装配时调用；返回自身便于链式装配）。
func (rh *ReasoningHandler) SetBase(h *Handler) *ReasoningHandler {
	rh.h = h
	return rh
}

// ready 校验装配状态：推理服务未装配时 fail-closed 返回 UNAVAILABLE。
func (rh *ReasoningHandler) ready() (*Handler, error) {
	if rh == nil || rh.svc == nil {
		return nil, unavailable("推理引擎未装配：请在服务端接线 reasoning.Service 后重启 API")
	}
	if rh.h == nil {
		return nil, errors.E(errors.CodeInternal, "推理处理器未挂接基础 Handler（装配缺失）")
	}
	return rh.h, nil
}

// writeErr 是不依赖基础 Handler 的错误写出（仅装配缺失路径使用）。
func (rh *ReasoningHandler) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	if rh.h != nil {
		rh.h.fail(w, r, err)
		return
	}
	httperr.Write(w, requestIDOf(r), err)
}

// rawBody 读取请求体（CreateRule 需要原始字节做 DisallowUnknownFields 严格校验）。
func (rh *ReasoningHandler) rawBody(r *http.Request, max int64) ([]byte, error) {
	if max <= 0 {
		max = 1 << 20 // 1 MiB：单条规则体的合理上界
	}
	defer func() { _ = r.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(r.Body, max+1))
	if err != nil {
		return nil, errors.E(errors.CodeInvalidArgument, "读取请求体失败")
	}
	if int64(len(b)) > max {
		return nil, errors.E(errors.CodeInvalidArgument, "请求体超过大小上限")
	}
	return b, nil
}

// ---------------------------------------------------------------------------
// 规则管理：GET/POST /api/v1/reasoning/rules、GET/PATCH/DELETE /rules/{id}
// ---------------------------------------------------------------------------

// ListRules 处理 GET /api/v1/reasoning/rules?limit=&cursor=&kind=。
func (rh *ReasoningHandler) ListRules(w http.ResponseWriter, r *http.Request) {
	h, err := rh.ready()
	if err != nil {
		rh.writeErr(w, r, err)
		return
	}
	limit, err := limitParam(r, 10)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	res, err := h.deps.Store.ListRules(r.Context(), limit, r.URL.Query().Get("cursor"), r.URL.Query().Get("kind"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, res)
}

// CreateRule 处理 POST /api/v1/reasoning/rules。
// 写端点五件套：echoIdempotency、recordAudit、SSE、软错误（fail）、Idempotency 回显。
func (rh *ReasoningHandler) CreateRule(w http.ResponseWriter, r *http.Request) {
	h, err := rh.ready()
	if err != nil {
		rh.writeErr(w, r, err)
		return
	}
	echoIdempotency(w, r)
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	body, err := rh.rawBody(r, 0)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	row, err := h.deps.Store.CreateCustomRule(r.Context(), body, id.SubjectID)
	if err != nil {
		h.fail(w, r, err) // 软错误：校验失败/键冲突按契约结构返回
		return
	}
	ruleKey, _ := row["rule_key"].(string)
	ruleID, _ := row["id"].(string)
	h.recordAudit(r, id, "ReasoningRuleCreated", "reasoning.rule.create", "SUCCESS",
		"reasoning_rule", ruleID, ids.ID{}, map[string]any{"rule_key": ruleKey})
	h.publish(id.TenantID.String(), "reasoning.rule.created", sse.Normal, map[string]any{
		"id": ruleID, "rule_key": ruleKey,
	})
	h.writeJSON(w, http.StatusCreated, row)
}

// GetRule 处理 GET /api/v1/reasoning/rules/{id}。
func (rh *ReasoningHandler) GetRule(w http.ResponseWriter, r *http.Request) {
	h, err := rh.ready()
	if err != nil {
		rh.writeErr(w, r, err)
		return
	}
	ruleID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	row, err := h.deps.Store.GetRule(r.Context(), ruleID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, row)
}

// rulePatchBody 是 PATCH /rules/{id} 的载荷（指针语义：nil = 不更新）。
type rulePatchBody struct {
	Enabled     *bool   `json:"enabled,omitempty"`
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}

// PatchRule 处理 PATCH /api/v1/reasoning/rules/{id}。
// 内置规则仅允许 enabled 切换：is_builtin=true 的行携带 name/description
// 变更时由 store 层返回 409（绕过界面直打 API 同样被拒）。
func (rh *ReasoningHandler) PatchRule(w http.ResponseWriter, r *http.Request) {
	h, err := rh.ready()
	if err != nil {
		rh.writeErr(w, r, err)
		return
	}
	echoIdempotency(w, r)
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ruleID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var body rulePatchBody
	if err := decodeJSON(r, &body); err != nil {
		h.fail(w, r, err)
		return
	}
	if body.Enabled == nil && body.Name == nil && body.Description == nil {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "请求体为空：至少提供 enabled/name/description 之一"))
		return
	}
	row, err := h.deps.Store.UpdateRule(r.Context(), ruleID, store.RulePatch{
		Enabled:     body.Enabled,
		Name:        body.Name,
		Description: body.Description,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ruleKey, _ := row["rule_key"].(string)
	h.recordAudit(r, id, "ReasoningRuleUpdated", "reasoning.rule.update", "SUCCESS",
		"reasoning_rule", ruleID.String(), ids.ID{}, map[string]any{"rule_key": ruleKey})
	h.publish(id.TenantID.String(), "reasoning.rule.updated", sse.Normal, map[string]any{
		"id": ruleID.String(), "rule_key": ruleKey,
	})
	h.writeJSON(w, http.StatusOK, row)
}

// DeleteRule 处理 DELETE /api/v1/reasoning/rules/{id}（内置规则 409）。
func (rh *ReasoningHandler) DeleteRule(w http.ResponseWriter, r *http.Request) {
	h, err := rh.ready()
	if err != nil {
		rh.writeErr(w, r, err)
		return
	}
	echoIdempotency(w, r)
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ruleID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	row, err := h.deps.Store.GetRule(r.Context(), ruleID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ruleKey, _ := row["rule_key"].(string)
	if err := h.deps.Store.DeleteRule(r.Context(), ruleID); err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "ReasoningRuleDeleted", "reasoning.rule.delete", "SUCCESS",
		"reasoning_rule", ruleID.String(), ids.ID{}, map[string]any{"rule_key": ruleKey})
	h.publish(id.TenantID.String(), "reasoning.rule.deleted", sse.Normal, map[string]any{
		"id": ruleID.String(), "rule_key": ruleKey,
	})
	h.writeJSON(w, http.StatusOK, map[string]any{"id": ruleID.String(), "deleted": true})
}

// ---------------------------------------------------------------------------
// 推理运行：POST /api/v1/reasoning/runs、GET /api/v1/reasoning/runs
// ---------------------------------------------------------------------------

// runRequest 是 POST /runs 的载荷。
type runRequest struct {
	OntologyVersionID string                    `json:"ontology_version_id"`
	Mode              string                    `json:"mode"`
	RuleKeys          []string                  `json:"rule_keys,omitempty"`
	MaxIterations     int                       `json:"max_iterations,omitempty"`
	Goal              *reasoning.TriplePattern  `json:"goal,omitempty"`
	AddedTriples      []reasoning.LexicalTriple `json:"added_triples,omitempty"`
}

// CreateRun 处理 POST /api/v1/reasoning/runs。
//
// 流程：解析本体版本 → 图 IRI（复用 tool_executors 的 OntologyVersionSnapshot
// 方式）→ 记录 input_triple_count → 创建 RUNNING 运行记录 → 执行推理 →
// 回写 derived_triple_count/duration/status。
func (rh *ReasoningHandler) CreateRun(w http.ResponseWriter, r *http.Request) {
	h, err := rh.ready()
	if err != nil {
		rh.writeErr(w, r, err)
		return
	}
	echoIdempotency(w, r)
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var body runRequest
	if err := decodeJSON(r, &body); err != nil {
		h.fail(w, r, err)
		return
	}
	verID, err := ids.Parse(body.OntologyVersionID)
	if err != nil {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "ontology_version_id 不是合法 UUID"))
		return
	}
	// 按版本 id 精确定位（绝不允许传版本 id 却解析成本体最新版的口径漂移）。
	snap, err := h.deps.Store.OntologyVersionSnapshotByVersionID(r.Context(), verID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	graphIRI, err := graphload.GraphIRI(snap.OntologyKey, snap.VersionID)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	switch body.Mode {
	case "FORWARD", "BACKWARD", "INCREMENTAL":
	default:
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "mode 不在枚举内（FORWARD | BACKWARD | INCREMENTAL）"))
		return
	}

	// 执行前记录输入规模（含新增事实——INCREMENTAL 的输入以新增为准）。
	inputCount, err := rh.svc.Store.CountTriples(r.Context(), id.TenantID, graphIRI)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var addedTriples []graphstore.Triple
	if body.Mode == "INCREMENTAL" {
		addedTriples, err = reasoning.TriplesFromLexical(body.AddedTriples)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		if len(addedTriples) == 0 {
			h.fail(w, r, errors.E(errors.CodeInvalidArgument, "INCREMENTAL 模式必须提供非空 added_triples"))
			return
		}
		inputCount += int64(len(addedTriples))
	}

	run, err := h.deps.Store.CreateInferenceRun(r.Context(), store.CreateInferenceRunInput{
		GraphIRI:    graphIRI,
		Mode:        body.Mode,
		RuleKeys:    body.RuleKeys,
		InputCount:  inputCount,
		TriggeredBy: id.SubjectID,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	runID, _ := run["id"].(string)
	runUUID, _ := ids.Parse(runID)

	// 执行（失败也要落到 FAILED 记录，绝不静默丢运行）。
	status, errMsg := "SUCCEEDED", ""
	var result map[string]any
	execute := func() error {
		switch body.Mode {
		case "FORWARD":
			res, ferr := rh.svc.ForwardChain(r.Context(), id.TenantID, graphIRI, body.RuleKeys, body.MaxIterations)
			if ferr != nil {
				return ferr
			}
			result = map[string]any{
				"derived_triple_count": len(res.Derived),
				"iterations":           res.Iterations,
				"duration_ms":          res.DurationMS,
				"added_triples":        lexicalTriples(res.Derived),
			}
		case "BACKWARD":
			if body.Goal == nil {
				return errors.E(errors.CodeInvalidArgument, "BACKWARD 模式必须提供 goal（subject/predicate/object，空串=变量）")
			}
			res, ferr := rh.svc.BackwardChain(r.Context(), id.TenantID, graphIRI, *body.Goal, body.RuleKeys)
			if ferr != nil {
				return ferr
			}
			result = map[string]any{
				"proved":      res.Proved,
				"proof_trace": res.ProofTrace,
				"bindings":    res.Bindings,
			}
		case "INCREMENTAL":
			res, ferr := rh.svc.IncrementalUpdate(r.Context(), id.TenantID, graphIRI, addedTriples)
			if ferr != nil {
				return ferr
			}
			result = map[string]any{
				"derived_triple_count": len(res.Derived),
				"inserted_count":       res.Inserted,
				"derived_triples":      lexicalTriples(res.Derived),
			}
		}
		return nil
	}
	if execErr := execute(); execErr != nil {
		status, errMsg = "FAILED", execErr.Error()
		result = nil
	}

	// 执行后回写运行记录。
	derived := int64(0)
	durationMS := int64(0)
	if result != nil {
		if v, ok := result["derived_triple_count"].(int); ok {
			derived = int64(v)
		}
		if v, ok := result["duration_ms"].(int64); ok {
			durationMS = v
		}
	}
	final, err := h.deps.Store.UpdateInferenceRun(r.Context(), runUUID, status, errMsg, derived, durationMS)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	payload := map[string]any{"run": final}
	if result != nil {
		for k, v := range result {
			payload[k] = v
		}
	}

	if status == "SUCCEEDED" {
		h.recordAudit(r, id, "ReasoningRunFinished", "reasoning.run."+lower(body.Mode), "SUCCESS",
			"reasoning_run", runID, runUUID, map[string]any{"graph_iri": graphIRI, "derived_triple_count": derived})
		h.publish(id.TenantID.String(), "reasoning.run.finished", sse.Normal, map[string]any{
			"id": runID, "mode": body.Mode, "status": status, "derived_triple_count": derived,
		})
	} else {
		h.recordAudit(r, id, "ReasoningRunFinished", "reasoning.run."+lower(body.Mode), "FAILURE",
			"reasoning_run", runID, runUUID, map[string]any{"graph_iri": graphIRI, "error": errMsg})
		h.publish(id.TenantID.String(), "reasoning.run.failed", sse.Normal, map[string]any{
			"id": runID, "mode": body.Mode, "status": status, "error": errMsg,
		})
	}
	// 模块 D：推理运行动作留痕（旁路观测，失败不阻断响应）。
	inferOutcome := action.OutcomeSuccess
	if status != "SUCCEEDED" {
		inferOutcome = action.OutcomeError
	}
	h.RecordActionForFlow(r.Context(), id.TenantID, action.Input{
		Type:      action.TypeInfer,
		Title:     "推理运行：" + body.Mode + "（" + graphIRI + "）",
		Summary:   "run=" + runID + " derived=" + strconv.FormatInt(derived, 10) + " status=" + status,
		Outcome:   inferOutcome,
		ActorKind: action.ActorUser,
		ActorName: id.SubjectID.String(),
	})
	h.writeJSON(w, http.StatusOK, payload)
}

// ListRuns 处理 GET /api/v1/reasoning/runs（cursor 分页）。
func (rh *ReasoningHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	h, err := rh.ready()
	if err != nil {
		rh.writeErr(w, r, err)
		return
	}
	limit, err := limitParam(r, 10)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	res, err := h.deps.Store.ListInferenceRuns(r.Context(), limit, r.URL.Query().Get("cursor"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, res)
}

// lexicalTriples 把词项三元组投影为响应体词法形态（截断到合理规模防响应爆炸）。
func lexicalTriples(in []graphstore.Triple) []map[string]string {
	const maxTriples = 200
	out := make([]map[string]string, 0, len(in))
	for i, t := range in {
		if i >= maxTriples {
			break
		}
		out = append(out, map[string]string{
			"subject":   reasoning.TermLexical(t.S),
			"predicate": reasoning.TermLexical(t.P),
			"object":    reasoning.TermLexical(t.O),
		})
	}
	return out
}

// lower 把 mode 转小写用于审计 action（FORWARD → forward）。
func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// requestIDOf 读取请求上下文中的 RequestID（装配缺失路径的错误响应仍带追踪头）。
func requestIDOf(r *http.Request) string {
	return middleware.RequestIDFromContext(r.Context())
}
