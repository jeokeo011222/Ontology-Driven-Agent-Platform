package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"odap/internal/api/sse"
	"odap/internal/api/store"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/runstate"
)

// createRunRequest 是 POST /api/v1/runs 的请求体。
//
// 两种驱动方式（二选一，由请求体形态决定走哪条分支）：
//   - **本体驱动**（业务工作台的主路径）：携带 ontology_id + objective。
//     服务端在本次请求内按该本体的规则审查业务目标并给出结论，因此不需要
//     agent_version_id —— 执行的「规则来源」是本体，而不是某个智能体版本；
//   - Agent 驱动（既有路径）：携带 agent_version_id，不带 ontology_id。
type createRunRequest struct {
	AgentVersionID string          `json:"agent_version_id"`
	OntologyID     string          `json:"ontology_id"`
	Objective      string          `json:"objective"`
	BusinessType   string          `json:"business_type"`
	Input          json.RawMessage `json:"input"`
	Priority       *int            `json:"priority,omitempty"`
	MaxAttempts    *int            `json:"max_attempts,omitempty"`
	DeadlineAt     *time.Time      `json:"deadline_at,omitempty"`
}

// CreateRun 处理 POST /api/v1/runs。
//
// 创建唯一执行资源 Run（contracts/03 §2）。requested_by 取自信任身份，
// 不接受请求体覆盖（第十六篇 §5.4）。
func (h *Handler) CreateRun(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	var req createRunRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}

	// ---- 本体驱动分支（业务工作台、需求 1）----
	//
	// 必须在解析 agent_version_id **之前**分流：本体驱动的请求体里本来就没有
	// agent_version_id，若先做 ids.Parse("") 会得到「agent_version_id 必须是 UUID」
	// 这类与真实成因无关的 400，用户看不出「该填 ontology_id」。
	if strings.TrimSpace(req.OntologyID) != "" {
		ontologyID, err := ids.Parse(req.OntologyID)
		if err != nil {
			h.fail(w, r, errors.E(errors.CodeInvalidArgument, "ontology_id 必须是 UUID"))
			return
		}
		echoIdempotency(w, r)
		run, version, err := h.reviewByOntology(r, id, ontologyID, req.Objective, req.BusinessType)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		// 审查在本次请求内已完成，返回的视图里直接带上结论与本体版本号，
		// 前端无需再猜「这次任务到底跑完没有」。
		h.writeJSON(w, http.StatusCreated, runViewOf(run, version))
		return
	}

	agentVersionID, err := ids.Parse(req.AgentVersionID)
	if err != nil {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "agent_version_id 必须是 UUID"))
		return
	}
	if len(req.Input) == 0 {
		req.Input = json.RawMessage("{}")
	}
	if !json.Valid(req.Input) {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "input 必须是合法 JSON"))
		return
	}
	priority := 100
	if req.Priority != nil {
		priority = *req.Priority
	}
	maxAttempts := 3
	if req.MaxAttempts != nil {
		maxAttempts = *req.MaxAttempts
		if maxAttempts < 1 || maxAttempts > 10 {
			h.fail(w, r, errors.E(errors.CodeInvalidArgument, "max_attempts 必须在 1..10 之间"))
			return
		}
	}
	echoIdempotency(w, r)

	run, err := h.deps.Store.CreateRun(r.Context(), storeRunInput(id, agentVersionID, req, priority, maxAttempts))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "RunCreated", "run.create", "SUCCESS", "run", run.ID, ids.ID{},
		map[string]any{"status": run.Status, "idempotency_key": idempotencyKey(r)})
	h.publish(id.TenantID.String(), "RunCreated", sse.Normal,
		map[string]any{"run_id": run.ID, "status": run.Status})
	h.writeJSON(w, http.StatusCreated, run)
}

// ListRuns 处理 GET /api/v1/runs（cursor 分页 + status 过滤）。
func (h *Handler) ListRuns(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	limit, err := limitParam(r, 10)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	filter := storeRunFilter(q.Get("status"), q.Get("cursor"), limit)
	filter.Q = q.Get("q")
	if raw := q.Get("created_after"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			h.fail(w, r, errors.E(errors.CodeInvalidArgument, "created_after 必须是 RFC3339 时间"))
			return
		}
		filter.CreatedAfter = &t
	}
	if raw := q.Get("created_before"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			h.fail(w, r, errors.E(errors.CodeInvalidArgument, "created_before 必须是 RFC3339 时间"))
			return
		}
		filter.CreatedBefore = &t
	}
	runs, next, err := h.deps.Store.ListRuns(r.Context(), filter)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// 一次批量取回本页用到的本体版本号，避免每行各查一次（N+1）。
	labels, err := h.deps.Store.OntologyVersionLabels(r.Context(), runOntologyVersionIDs(runs))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]runView, 0, len(runs))
	for i := range runs {
		items = append(items, runViewOf(&runs[i], labelFor(labels, runs[i].OntologyVersionID)))
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

// GetRun 处理 GET /api/v1/runs/{id}（返回 Run + 步骤 + 版本锁依赖）。
func (h *Handler) GetRun(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	runID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	run, err := h.deps.Store.GetRun(r.Context(), runID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	steps, err := h.deps.Store.ListRunSteps(r.Context(), runID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	deps, err := h.deps.Store.ListRunDependencies(r.Context(), runID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	labels, err := h.deps.Store.OntologyVersionLabels(r.Context(), runOntologyVersionIDs([]store.Run{*run}))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	stepViews := make([]runStepView, 0, len(steps))
	for _, st := range steps {
		stepViews = append(stepViews, runStepViewOf(run.ID, st))
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"run":          runViewOf(run, labelFor(labels, run.OntologyVersionID)),
		"steps":        stepViews,
		"dependencies": deps,
	})
}

// ListRunSteps 处理 GET /api/v1/runs/{id}/steps。
//
// 与 GET /runs/{id} 内嵌的 steps 同源同投影：任务详情页的「执行过程」页签
// 单独取这一步，两者不会出现「同一个步骤在详情里叫 A、在步骤列表里叫 B」。
func (h *Handler) ListRunSteps(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	runID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// 先证明 Run 在本租户可见：否则「Run 不存在」会被渲染成「这个任务没有任何步骤」。
	run, err := h.deps.Store.GetRun(r.Context(), runID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	steps, err := h.deps.Store.ListRunSteps(r.Context(), runID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]runStepView, 0, len(steps))
	for _, st := range steps {
		items = append(items, runStepViewOf(run.ID, st))
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": ""})
}

// CancelRun 处理 POST /api/v1/runs/{id}/cancel。
//
// 经 harness.Machine 走 CancellationRequested（第六篇 §14.2.1）：任意非终态
// 统一进入 CANCELLING，在途外部操作由取消收敛队列处理。
func (h *Handler) CancelRun(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	runID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil || h.deps.Machine == nil {
		h.fail(w, r, unavailable("执行引擎未装配，无法受理取消"))
		return
	}
	status, err := h.deps.Store.RunExists(r.Context(), runID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if runstate.IsTerminal(status) {
		h.fail(w, r, errors.E(errors.CodeConflict, "Run 已处于终态，无法取消").
			WithDetails(map[string]any{"status": status}))
		return
	}
	if !h.ifMatch(w, r, status) {
		return
	}
	echoIdempotency(w, r)
	if err := h.deps.Machine.Apply(r.Context(), runID, eventCancellationRequested, noFacts()); err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.appendOutbox(r, id.TenantID, "CancellationRequested", runID, map[string]any{"run_id": runID.String()}); err != nil && h.deps.Logger != nil {
		// 取消状态跃迁已提交，不能再以此失败回退请求；但绝不静默——
		// 留痕便于监控并补投 Outbox 事件。
		h.deps.Logger.Error("取消请求已受理，但 Outbox 事件入队失败（需补投）",
			"run_id", runID.String(), "error", err)
	}
	h.recordAudit(r, id, "CancellationRequested", "run.cancel", "SUCCESS", "run", runID.String(), runID,
		map[string]any{"previous_status": status, "idempotency_key": idempotencyKey(r)})
	h.publish(id.TenantID.String(), "CancellationRequested", sse.Critical,
		map[string]any{"run_id": runID.String()})
	h.writeJSON(w, http.StatusAccepted, map[string]any{
		"id":     runID.String(),
		"status": runstate.Cancelling,
	})
}

// ReconcileRun 处理 POST /api/v1/runs/{id}/reconcile。
//
// Run 级对账=批量编排入口（contracts/03 §4）：校验 Run 处于
// REQUIRES_RECONCILIATION，追加对账请求事件并返回 202 + 异步任务引用；
// 单笔外部操作对账由外部操作级端点负责，底层同一 Reconciler。
func (h *Handler) ReconcileRun(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	runID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	status, err := h.deps.Store.RunExists(r.Context(), runID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if status != runstate.RequiresReconciliation {
		h.fail(w, r, errors.E(errors.CodeConflict, "Run 不在待对账状态").
			WithDetails(map[string]any{"status": status}))
		return
	}
	echoIdempotency(w, r)
	if err := h.appendOutbox(r, id.TenantID, "ReconciliationRequested", runID,
		map[string]any{"run_id": runID.String()}); err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "ReconciliationRequested", "run.reconcile", "SUCCESS", "run", runID.String(), runID,
		map[string]any{"idempotency_key": idempotencyKey(r)})
	h.publish(id.TenantID.String(), "ReconciliationRequested", sse.Important,
		map[string]any{"run_id": runID.String()})
	h.writeJSON(w, http.StatusAccepted, map[string]any{
		"task_id":    runID.String(),
		"kind":       "RUN_RECONCILIATION",
		"status":     "QUEUED",
		"status_url": "/api/v1/tasks/" + runID.String(),
	})
}

// renewLeaseRequest 是 Worker 租约续期请求体。
type renewLeaseRequest struct {
	Owner      string `json:"owner"`
	LeaseEpoch int64  `json:"lease_epoch"`
}

// RenewLease 处理 POST /api/v1/runs/{id}/lease/renew（第六篇 §6.2）。
//
// 必须携带租约归属与代次；不匹配返回 EXEC_LEASE_LOST（映射为 CONFLICT），
// 调用方（Worker）须立即停止推进。
func (h *Handler) RenewLease(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	runID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Scheduler == nil {
		h.fail(w, r, unavailable("调度器未装配，无法续期租约"))
		return
	}
	var req renewLeaseRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	if req.Owner == "" {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "owner 必填"))
		return
	}
	expiry, err := h.deps.Scheduler.RenewLease(r.Context(), id.TenantID, runID, req.Owner, req.LeaseEpoch)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"run_id":           runID.String(),
		"lease_owner":      req.Owner,
		"lease_epoch":      req.LeaseEpoch,
		"lease_expires_at": expiry.UTC(),
	})
}

// ---------------------------------------------------------------------------
// 对外视图投影
// ---------------------------------------------------------------------------

// runView 是 Run 的对外视图（contracts/openapi.yaml components.schemas.Run）。
//
// 为什么不直接把 store.Run 发出去（store.Run 已经是 JSON 友好的）：
// 前端要展示的两个字段**四库里的列里没有**——`ontology_version` 需要跨表查
// ontology_versions.version，`updated_at` 是 created/started/finished 三者取最大
// 的派生值。把它们塞进 store.Run 会让存储层去操心展示口径；放在 handler 层，
// 存储层继续只表达「表里有什么」。
type runView struct {
	ID               string          `json:"id"`
	Objective        string          `json:"objective"`
	Status           string          `json:"status"`
	SubjectID        string          `json:"subject_id"`
	AgentVersionID   *string         `json:"agent_version_id,omitempty"`
	OntologyVersion  *string         `json:"ontology_version,omitempty"`
	BusinessType     *string         `json:"business_type,omitempty"`
	Progress         float64         `json:"progress"`
	Plan             json.RawMessage `json:"plan,omitempty"`
	ReviewConclusion json.RawMessage `json:"review_conclusion,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

// runViewOf 把存储层的 Run 投影为对外视图。
func runViewOf(run *store.Run, ontologyVersion string) runView {
	if run == nil {
		return runView{}
	}
	v := runView{
		ID:        run.ID,
		Status:    run.Status,
		SubjectID: run.RequestedBy,
		Progress:  progressOf(run.Status, 0) * 100,
		CreatedAt: run.CreatedAt,
		UpdatedAt: latestTimestamp(run.CreatedAt, run.StartedAt, run.FinishedAt),
	}
	if run.Objective != nil {
		v.Objective = *run.Objective
	}
	if run.AgentVersionID != "" {
		av := run.AgentVersionID
		v.AgentVersionID = &av
	}
	// 只有真的查到版本号才给出该字段：查不到时省略，而不是回填 ontology_version_id
	// （那是版本主键，不是人能读的版本号，两者混用会让人误以为本体版本叫一个 UUID）。
	if run.OntologyVersionID != nil && ontologyVersion != "" {
		ov := ontologyVersion
		v.OntologyVersion = &ov
	}
	v.BusinessType = run.BusinessType
	if len(run.Plan) > 0 {
		v.Plan = json.RawMessage(run.Plan)
	}
	if len(run.ReviewConclusion) > 0 {
		v.ReviewConclusion = json.RawMessage(run.ReviewConclusion)
	}
	return v
}

// latestTimestamp 返回基准时间与若干可空时间戳中最晚的一个（Run 的「最后变更时间」）。
func latestTimestamp(base time.Time, extra ...*time.Time) time.Time {
	out := base
	for _, t := range extra {
		if t != nil && t.After(out) {
			out = *t
		}
	}
	return out
}

// runOntologyVersionIDs 收集本页各 Run 绑定的本体版本 id，用于批量查版本号。
func runOntologyVersionIDs(runs []store.Run) []ids.ID {
	out := make([]ids.ID, 0, len(runs))
	for i := range runs {
		if runs[i].OntologyVersionID == nil {
			continue
		}
		parsed, err := ids.Parse(*runs[i].OntologyVersionID)
		if err != nil {
			continue
		}
		out = append(out, parsed)
	}
	return out
}

// labelFor 从批量查询结果里取版本号；缺失时返回空串（视图层会省略该字段，
// 而不是编一个版本号）。
func labelFor(labels map[string]string, versionID *string) string {
	if versionID == nil {
		return ""
	}
	return labels[*versionID]
}

// runStepView 是步骤的对外视图（contracts/openapi.yaml components.schemas.RunStep）。
//
// 字段名与前端任务详情页一致（seq/name/kind），而不是直译数据库列名
// （step_no/step_type）：同一份数据在两个界面里叫两个名字，是 UI 文案对不上的
// 最常见来源。
type runStepView struct {
	ID              string          `json:"id"`
	RunID           string          `json:"run_id"`
	Seq             int             `json:"seq"`
	Name            string          `json:"name"`
	Kind            string          `json:"kind"`
	Status          string          `json:"status"`
	Rationale       *string         `json:"rationale,omitempty"`
	OntologyVersion *string         `json:"ontology_version,omitempty"`
	RuleRefs        []string        `json:"rule_refs,omitempty"`
	FactRefs        []string        `json:"fact_refs,omitempty"`
	StartedAt       *time.Time      `json:"started_at,omitempty"`
	FinishedAt      *time.Time      `json:"finished_at,omitempty"`
	ErrorCode       *string         `json:"error_code,omitempty"`
	ErrorMessage    *string         `json:"error_message,omitempty"`
	ResultPayload   json.RawMessage `json:"result_payload,omitempty"`
}

// runStepViewOf 投影单个步骤。
//
// result_payload 里由本体审查链路写入的 name/rationale/ontology_version/rule_refs/
// fact_refs 会提升到顶层（前端直接渲染），同时保留原始 payload 供排查。
// payload 解析失败时不编造这些字段——只有 name 落到 step_type（如实呈现「这一步
// 是什么类型」），其余留空。
func runStepViewOf(runID string, st store.RunStep) runStepView {
	v := runStepView{
		ID:           st.ID,
		RunID:        runID,
		Seq:          st.StepNo,
		Name:         st.StepType,
		Kind:         st.StepType,
		Status:       st.Status,
		StartedAt:    st.StartedAt,
		FinishedAt:   st.FinishedAt,
		ErrorCode:    st.ErrorCode,
		ErrorMessage: st.ErrorSummary,
	}
	if len(st.ResultPayload) > 0 {
		v.ResultPayload = st.ResultPayload
		var payload map[string]any
		if err := json.Unmarshal(st.ResultPayload, &payload); err == nil {
			if s := payloadString(payload, "name"); s != "" {
				v.Name = s
			}
			if s := payloadString(payload, "rationale"); s != "" {
				v.Rationale = &s
			}
			if s := payloadString(payload, "ontology_version"); s != "" {
				v.OntologyVersion = &s
			}
			v.RuleRefs = payloadStringSlice(payload, "rule_refs")
			v.FactRefs = payloadStringSlice(payload, "fact_refs")
		}
	}
	return v
}

// payloadString 读取 payload 中的字符串字段；非字符串或缺失时返回空串。
func payloadString(payload map[string]any, key string) string {
	raw, ok := payload[key]
	if !ok || raw == nil {
		return ""
	}
	s, _ := raw.(string)
	return s
}

// payloadStringSlice 读取 payload 中的字符串数组；形态不符时返回空（不猜）。
func payloadStringSlice(payload map[string]any, key string) []string {
	raw, ok := payload[key]
	if !ok || raw == nil {
		return nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil
		}
		out = append(out, s)
	}
	return out
}
