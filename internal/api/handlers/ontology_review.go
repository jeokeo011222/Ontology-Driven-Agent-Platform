package handlers

// 本体驱动的业务审查（业务工作台本体化，需求 1）。
//
// 用户在前端选中一个本体、用自然语言描述业务目标；本文件在**同一次请求内**
// 完成整条链路并给出结论，并把每个阶段作为 runtime.run_steps 与 Run 同事务落库，
// 使「我的任务 → 任务详情 → 执行过程」能看到真实的、基于本体的处理过程。
//
// 链路（每一步都真实执行，失败即明确报错，绝不伪造）：
//  1. 取本体版本快照，解析清单（DisallowUnknownFields），构造 SHACL 形状——规则的来源；
//  2. 调受治理的模型网关，按本体真实词表与形状把业务目标抽取为符合本体的实例事实，
//     并给出初步判断与逐条规则自查；
//  3. 用平台自己的清单解析器（graphload.ParseManifest）复核模型产出的实例：
//     类必须已声明、属性/关系必须属于该类、字面量必须可强转——不合规则直接 422，
//     绝不把「模型编出来的字段」写进图；
//  4. TBox + 复核后的 ABox 装载到**该 Run 专属的命名图**（图 IRI 由 run id 派生，
//     与任务记录指向同一标识，可互相反查）；
//  5. 反假绿探针：命中目标类的实例数为 0 时拒绝给结论——否则 SHACL 会「无实例即合规」；
//  6. 执行真实 SHACL 校验，得到权威规则结论；
//  7. 确定性合成最终结论：模型说「通过」**不能**覆盖 SHACL 的「不合规」。
//
// 状态推进走 harness.Machine（唯一允许写 agent_runs.status 的地方）：
//   CREATED → CONTEXT_BUILDING → READY → PLANNING → PLAN_VALIDATING → RUNNING
//   → COMPLETED（合规）或 MANUAL_REVIEW（需人工复核）。
// 不新增任何事件（保持事件词表 31 条不变）。

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"odap/internal/api/sse"
	"odap/internal/api/store"
	"odap/internal/harness"
	"odap/internal/llm"
	"odap/internal/ontology/graphload"
	"odap/internal/ontology/shacl"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/tenantctx"
)

// 模型可给出的初步判断词表。
const (
	decisionApprove      = "APPROVE"
	decisionReject       = "REJECT"
	decisionNeedInfo     = "NEED_INFO"
	decisionNeedApproval = "NEED_APPROVAL"
)

// 模型对单条形状的自查结论词表。
const (
	ruleSatisfied    = "SATISFIED"
	ruleViolated     = "VIOLATED"
	ruleInconclusive = "INCONCLUSIVE"
)

// run_steps.step_type 取值。命名与本体校验页同一套心智模型，便于并排阅读。
const (
	stepContextBuild  = "ONTOLOGY_CONTEXT_BUILD"
	stepFactExtract   = "ONTOLOGY_FACT_EXTRACT"
	stepGraphLoad     = "ONTOLOGY_GRAPH_LOAD"
	stepShaclValidate = "ONTOLOGY_SHACL_VALIDATE"
	stepRuleReview    = "ONTOLOGY_RULE_REVIEW"
	stepConclusion    = "ONTOLOGY_CONCLUSION"
)

// ontologyReviewSchema 是模型输出骨架（字段白名单 + 数组上限）。
//
// 属性名取自本体真实声明（类名枚举化、属性/关系名进入 properties 白名单），
// 而不是让模型自由发挥：自由发挥的字段会在第 3 步被清单解析器拒绝，
// 等于把一次能被 Schema 当场拦下的错误推迟到一次昂贵的往返之后。
func ontologyReviewSchema(parsed *graphload.Manifest) []byte {
	classNames := make([]string, 0, len(parsed.Classes))
	propNames := make([]string, 0, 16)
	seen := map[string]bool{}
	for _, c := range parsed.Classes {
		classNames = append(classNames, c.Name)
		for _, a := range c.Attributes {
			if !seen[a.Name] {
				seen[a.Name] = true
				propNames = append(propNames, a.Name)
			}
		}
	}
	for _, rel := range parsed.Relations {
		if !seen[rel.Name] {
			seen[rel.Name] = true
			propNames = append(propNames, rel.Name)
		}
	}
	props := map[string]any{}
	for _, n := range propNames {
		// 一律声明为字符串：graphload 的字面量构造会把字符串按声明类型强转
		// （数字 "1250.00"、布尔 "true" 都能正确落到 number/boolean 槽位），
		// 于是同一份 Schema 不必为每个属性分别推导 JSON 类型。
		props[n] = map[string]any{"type": "string"}
	}
	shapeIDs := make([]string, 0, len(parsed.Shapes))
	for _, s := range parsed.Shapes {
		shapeIDs = append(shapeIDs, s.ID)
	}
	factItem := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"class", "id", "properties"},
		"properties": map[string]any{
			"class":      map[string]any{"type": "string", "enum": classNames},
			"id":         map[string]any{"type": "string"},
			"properties": map[string]any{"type": "object", "additionalProperties": false, "properties": props},
		},
	}
	ruleCheck := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"shape_id", "outcome"},
		"properties": map[string]any{
			"shape_id": map[string]any{"type": "string", "enum": shapeIDs},
			"outcome":  map[string]any{"type": "string", "enum": []string{ruleSatisfied, ruleViolated, ruleInconclusive}},
			"evidence": map[string]any{"type": "string"},
		},
	}
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"facts", "decision", "reasons"},
		"properties": map[string]any{
			"facts":    map[string]any{"type": "array", "maxItems": 8, "items": factItem},
			"decision": map[string]any{"type": "string", "enum": []string{decisionApprove, decisionReject, decisionNeedInfo, decisionNeedApproval}},
			"reasons":  map[string]any{"type": "array", "maxItems": 10, "items": map[string]any{"type": "string"}},
			"rule_checks": map[string]any{
				"type": "array", "maxItems": 30, "items": ruleCheck,
			},
		},
	}
	raw, _ := json.Marshal(schema)
	return raw
}

// ontologyReviewSystemPrompt 由本体**真实**词表渲染，不含任何硬编码规则文案。
func ontologyReviewSystemPrompt(parsed *graphload.Manifest, ontologyName string) string {
	var b strings.Builder
	b.WriteString("你是企业业务合规审查助手。你的唯一职责是：依据下面给出的本体（业务对象与规则）")
	b.WriteString("审查用户提交的业务请求，抽取其中与本体相关的业务事实，并给出初步判断。\n")
	b.WriteString("硬性要求：\n")
	b.WriteString("1. facts 中的每个实例，class 必须是下面【业务类】中出现的类名，properties 的键必须是该类声明的属性或关系名；不确定的信息不要编造，宁可不填该键。\n")
	b.WriteString("2. 关系型属性的值必须填写为本轮 facts 中另一个实例的 id。\n")
	b.WriteString("3. decision 只能是 APPROVE / REJECT / NEED_INFO / NEED_APPROVAL；信息不足以判断时选 NEED_INFO。\n")
	b.WriteString("4. rule_checks 必须逐条覆盖下面【形状约束】中的每个形状，shape_id 必须原样引用。\n")
	b.WriteString("5. 平台会用你自己的 SHACL 引擎重跑一遍约束；你的 APPROVE 不能覆盖引擎判定的不合规，请按实际证据作答。\n\n")
	b.WriteString("本体名称：" + ontologyName + "\n")
	if parsed.Summary != "" {
		b.WriteString("本体摘要：" + parsed.Summary + "\n")
	}
	b.WriteString("\n【业务类】\n")
	for _, c := range parsed.Classes {
		b.WriteString("- " + c.Name)
		if c.Label != "" {
			b.WriteString("（" + c.Label + "）")
		}
		if c.Description != "" {
			b.WriteString("：" + c.Description)
		}
		b.WriteString("\n")
		for _, a := range c.Attributes {
			req := ""
			if a.Required {
				req = "（必填）"
			}
			b.WriteString("    · 属性 " + a.Name + " : " + a.Type + req)
			if a.Description != "" {
				b.WriteString(" — " + a.Description)
			}
			b.WriteString("\n")
		}
	}
	if len(parsed.Relations) > 0 {
		b.WriteString("\n【关系】\n")
		for _, rel := range parsed.Relations {
			b.WriteString("- " + rel.Name + " : " + rel.FromClass + " → " + rel.ToClass)
			if rel.Cardinality != "" {
				b.WriteString("（" + rel.Cardinality + "）")
			}
			if rel.Description != "" {
				b.WriteString(" — " + rel.Description)
			}
			b.WriteString("\n")
		}
	}
	if len(parsed.Enumerations) > 0 {
		b.WriteString("\n【取值集合】\n")
		for _, e := range parsed.Enumerations {
			b.WriteString("- " + e.Name + " : " + strings.Join(e.Values, " / ") + "\n")
		}
	}
	b.WriteString("\n【形状约束（必须逐条自查）】\n")
	for _, s := range parsed.Shapes {
		b.WriteString("- " + s.ID + " 作用于 " + s.TargetClass)
		if s.Severity != "" {
			b.WriteString("，级别 " + s.Severity)
		}
		b.WriteString("\n")
		for _, p := range s.Properties {
			parts := []string{"路径 " + p.Path}
			if p.MinCount != nil {
				parts = append(parts, "最少 "+itoa(*p.MinCount))
			}
			if p.MaxCount != nil {
				parts = append(parts, "最多 "+itoa(*p.MaxCount))
			}
			if p.Datatype != "" {
				parts = append(parts, "数据类型 "+p.Datatype)
			}
			if p.Class != "" {
				parts = append(parts, "值类型 "+p.Class)
			}
			if p.NodeKind != "" {
				parts = append(parts, "节点种类 "+p.NodeKind)
			}
			if len(p.In) > 0 {
				parts = append(parts, "取值只能是 "+strings.Join(p.In, "/"))
			}
			if p.MinInclusive != nil {
				parts = append(parts, "最小 "+ftoa(*p.MinInclusive))
			}
			if p.MaxInclusive != nil {
				parts = append(parts, "最大 "+ftoa(*p.MaxInclusive))
			}
			if p.MinExclusive != nil {
				parts = append(parts, "须大于 "+ftoa(*p.MinExclusive))
			}
			if p.MaxExclusive != nil {
				parts = append(parts, "须小于 "+ftoa(*p.MaxExclusive))
			}
			if p.Pattern != "" {
				parts = append(parts, "须匹配 "+p.Pattern)
			}
			if p.MinLength != nil {
				parts = append(parts, "最短 "+itoa(*p.MinLength))
			}
			if p.MaxLength != nil {
				parts = append(parts, "最长 "+itoa(*p.MaxLength))
			}
			b.WriteString("    · " + strings.Join(parts, "，") + "\n")
		}
	}
	return b.String()
}

// 轻量格式化（避免为两句拼接引入 fmt 依赖）。
func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func ftoa(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

// reviewFact 是模型抽取的实例事实（与 graphload.Instance 同构）。
type reviewFact struct {
	Class      string         `json:"class"`
	ID         string         `json:"id"`
	Properties map[string]any `json:"properties"`
}

// reviewRuleCheck 是模型对单条形状的自查结论。
type reviewRuleCheck struct {
	ShapeID  string `json:"shape_id"`
	Outcome  string `json:"outcome"`
	Evidence string `json:"evidence,omitempty"`
}

// reviewModelOutput 是模型输出的强类型投影。
type reviewModelOutput struct {
	Facts      []reviewFact      `json:"facts"`
	Decision   string            `json:"decision"`
	Reasons    []string          `json:"reasons"`
	RuleChecks []reviewRuleCheck `json:"rule_checks"`
}

// reviewOutcome 是一次本体审查的确定性结论（服务端合成，模型不得覆盖）。
type reviewOutcome struct {
	Decision string   `json:"decision"`
	Reasons  []string `json:"reasons"`
	// Conforms 是 SHACL 引擎的权威判定。
	Conforms bool `json:"conforms"`
	// RuleChecks 是模型逐条形状的自查（仅作参考证据，不构成结论）。
	RuleChecks []reviewRuleCheck `json:"rule_checks,omitempty"`
	// ModelDecision 保留模型的原始判断，便于对比「模型认为通过、引擎判定不合规」这类分歧。
	ModelDecision string           `json:"model_decision"`
	ShapeFindings []map[string]any `json:"shape_findings,omitempty"`
	GraphIRI      string           `json:"graph_iri"`
	Ontology      map[string]any   `json:"ontology"`
	Facts         []reviewFact     `json:"facts"`
}

// reviewByOntology 执行「基于本体规则审查业务请求」的完整链路。
//
// 返回：已落库的 Run、本体版本号（供视图展示）、错误。
func (h *Handler) reviewByOntology(r *http.Request, id tenantctx.Identity, ontologyID ids.ID,
	objective, businessType string) (*store.Run, string, error) {
	ctx := r.Context()

	// ---- 前置能力检查（fail-closed：依赖未装配时明确报错，不建 Run）----
	if h.deps.Store == nil {
		return nil, "", unavailable("持久化适配器未装配")
	}
	if h.deps.GraphStore == nil {
		return nil, "", unavailable("语义图存储未装配：本体规则审查必须真实装载并校验数据图，无图存储时无法给出结论")
	}
	if h.deps.Machine == nil {
		return nil, "", unavailable("执行引擎未装配，无法推进任务状态")
	}
	if _, err := h.llmGateway(); err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(objective) == "" {
		return nil, "", errors.E(errors.CodeInvalidArgument, "objective 必填：本体审查需要用户的业务目标作为输入")
	}
	if len(objective) > 2000 {
		return nil, "", errors.E(errors.CodeInvalidArgument, "objective 过长（上限 2000 字）")
	}

	// ---- 1. 本体版本快照（规则来源）----
	snap, err := h.deps.Store.OntologyVersionSnapshot(ctx, ontologyID)
	if err != nil {
		return nil, "", err
	}
	if len(snap.Manifest) == 0 {
		return nil, "", errors.E(errors.CodeValidationFailed, "本体版本没有清单内容，无法执行规则审查").
			WithDetails(map[string]any{"ontology_id": snap.OntologyID, "version": snap.Version})
	}

	ns, err := graphload.NewNamespace(snap.OntologyKey)
	if err != nil {
		return nil, "", err
	}
	parsed, err := graphload.ParseManifest(snap.Manifest)
	if err != nil {
		return nil, "", errors.Wrap(err, errors.CodeValidationFailed, "本体清单不合法，无法执行规则审查")
	}
	if len(parsed.Shapes) == 0 {
		// 没有形状 = 没有可执行的规则。此时「通过」是无意义的（任何数据都会通过），
		// 必须显式拒绝，否则会把「没写约束」伪装成「业务合规」。
		return nil, "", errors.E(errors.CodeValidationFailed,
			"本体未定义 SHACL 形状（manifest.shapes 为空）：没有形状就没有可执行的规则，无法给出审查结论").
			WithDetails(map[string]any{
				"ontology_id": snap.OntologyID, "version": snap.Version,
				"hint": "请在管理端为本体补充形状约束后重试",
			})
	}
	shapes, err := parsed.BuildShapes(ns)
	if err != nil {
		return nil, "", err
	}

	// Run id 先生成：它同时决定该 Run 专属的命名图 IRI，使「图」与「任务」指向同一标识。
	runID := ids.New()
	graphIRI, err := graphload.GraphIRI(snap.OntologyKey, "run-"+runID.String())
	if err != nil {
		return nil, "", err
	}

	// ---- 2. 模型按本体规则抽取业务事实并给出初步判断 ----
	schema := ontologyReviewSchema(parsed)
	userPrompt := "业务目标：" + objective
	if strings.TrimSpace(businessType) != "" {
		userPrompt += "\n业务类型标注：" + businessType
	}
	_, comp, err := h.callLLM(ctx, id.TenantID, "", []llm.Message{
		{Role: "system", Content: ontologyReviewSystemPrompt(parsed, snap.Name)},
		{Role: "user", Content: userPrompt},
	}, schema, "ontology-review/"+runID.String())
	if err != nil {
		return nil, "", err
	}
	var model reviewModelOutput
	if raw := comp.Content; strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &model); err != nil {
			return nil, "", errors.E(errors.CodeValidationFailed,
				"模型输出无法解析为审查结果结构").
				WithDetails(map[string]any{"parse_error": err.Error(), "raw_prefix": clip(raw, 300)})
		}
	}
	if model.Decision == "" {
		model.Decision = decisionNeedInfo
	}
	if len(model.Facts) == 0 {
		// 抽不出任何事实：可能是目标与本本体无关，也可能是用户描述不足。
		// 两种情况都不能给出「通过」——没有事实就没有可校验的对象。
		model.Decision = decisionNeedInfo
		model.Reasons = append(model.Reasons, "未能从业务目标中抽取出属于本本体的业务事实")
	}

	// ---- 3. 用平台清单解析器复核模型产出的实例（唯一真值来源）----
	// 把模型事实并回原清单后再解析：类必须已声明、属性/关系必须属于该类、
	// 字面量必须能按声明类型强转。这里若放行，后面就会把编造的字段写进图。
	merged, err := mergeManifestInstances(snap.Manifest, model.Facts)
	if err != nil {
		return nil, "", err
	}
	reviewManifest, err := graphload.ParseManifest(merged)
	if err != nil {
		return nil, "", errors.Wrap(err, errors.CodeValidationFailed,
			"模型抽取的业务事实不符合本体定义（类/属性/关系/取值不合法）")
	}

	tbox, err := parsed.TBox(ns)
	if err != nil {
		return nil, "", err
	}
	abox, err := reviewManifest.ABox(ns)
	if err != nil {
		return nil, "", errors.Wrap(err, errors.CodeValidationFailed, "生成业务事实数据图失败")
	}

	// ---- 4. 装载到该 Run 专属的命名图（TBox + 复核后的 ABox）----
	if _, err := h.deps.GraphStore.InsertTriples(ctx, id.TenantID, graphIRI, append(tbox, abox...)); err != nil {
		return nil, "", err
	}

	// ---- 5. 反假绿探针：必须先证明「图里确实有待校验的实例」----
	targets := parsed.TargetClassIRIs(ns)
	focusNodes, err := graphload.CountTypedNodes(ctx, h.deps.GraphStore, id.TenantID, graphIRI, targets)
	if err != nil {
		return nil, "", err
	}
	if focusNodes == 0 {
		return nil, "", errors.E(errors.CodeValidationFailed,
			"图中没有可校验的实例：仅装载了本体结构（TBox），未装载任何属于目标类的业务事实。此时 SHACL 会返回「通过」——那只说明没数据可查，不说明业务合规，故不给出结论").
			WithDetails(map[string]any{
				"ontology_id": snap.OntologyID, "version": snap.Version,
				"graph_iri": graphIRI, "target_classes": targets,
				"abox_triples": len(abox),
			})
	}

	// ---- 6. 真实 SHACL 校验（权威规则结论）----
	report, err := shacl.Validate(h.deps.GraphStore, id.TenantID, graphIRI, shapes)
	if err != nil {
		return nil, "", err
	}

	// ---- 7. 确定性结论合成（模型不得覆盖引擎判定）----
	decision, reasons := composeReviewOutcome(report, model)
	findingViews := make([]map[string]any, 0, len(report.Results))
	for _, res := range report.Results {
		findingViews = append(findingViews, map[string]any{
			"component":  res.Component,
			"severity":   res.Severity,
			"focus_node": res.FocusNode,
			"path":       res.Path,
			"message":    res.Message,
			"digest":     res.Digest(),
		})
	}
	outcome := reviewOutcome{
		Decision:      decision,
		Reasons:       reasons,
		Conforms:      report.Conforms,
		RuleChecks:    model.RuleChecks,
		ModelDecision: model.Decision,
		ShapeFindings: findingViews,
		GraphIRI:      graphIRI,
		Ontology: map[string]any{
			"id": snap.OntologyID, "key": snap.OntologyKey, "name": snap.Name,
			"version": snap.Version, "version_id": snap.VersionID,
		},
		Facts: model.Facts,
	}
	conclusionJSON, _ := json.Marshal(outcome)
	planJSON, _ := json.Marshal(reviewPlan(parsed, focusNodes))
	inputJSON, _ := json.Marshal(map[string]any{"objective": objective, "business_type": businessType})

	// ---- 8. Run + 全过程步骤同事务落库 ----
	startedAt := time.Now().UTC()
	finishedAt := startedAt
	steps := reviewSteps(parsed, snap, model, report, focusNodes, graphIRI, decision, len(tbox), len(abox),
		startedAt, finishedAt)
	_, err = h.deps.Store.CreateRun(ctx, store.CreateRunInput{
		RunID:             runID,
		RequestedBy:       id.SubjectID,
		OntologyVersionID: parseIDOrZero(snap.VersionID),
		Objective:         objective,
		BusinessType:      strings.TrimSpace(businessType),
		Plan:              planJSON,
		ReviewConclusion:  conclusionJSON,
		OutputPayload:     conclusionJSON,
		OutputDigest:      digestHex(conclusionJSON),
		InputPayload:      inputJSON,
		InputDigest:       digestHex(inputJSON),
		Priority:          100,
		MaxAttempts:       1,
		Steps:             steps,
		Dependencies: []store.RunDependencyInput{{
			DepType:    "ONTOLOGY_VERSION",
			DepID:      parseIDOrZero(snap.VersionID),
			DepVersion: snap.Version,
			DepDigest:  digestHex(snap.Manifest),
		}},
	})
	if err != nil {
		return nil, "", err
	}

	// ---- 9. 按合法状态矩阵推进到终态 ----
	if err := h.advanceReviewedRun(ctx, runID, decision == decisionApprove); err != nil {
		return nil, "", err
	}
	// 回读一次：CreateRun 的 RETURNING 拿到的还是 CREATED 的中间态（started_at /
	// finished_at 尚未写入）。直接改写内存里的 run.Status 虽然也能让前端看到终态，
	// 但时间戳会停留在创建时刻——任务详情页会显示「完成于创建时刻」这种自相矛盾的
	// 记录。回读一次拿到状态机写下的真实终态，代价是一次主键查询。
	final, err := h.deps.Store.GetRun(ctx, runID)
	if err != nil {
		return nil, "", err
	}

	h.recordAudit(r, id, "RunCreated", "run.create", "SUCCESS", "run", final.ID, ids.ID{},
		map[string]any{
			"status": final.Status, "ontology_id": snap.OntologyID, "ontology_version": snap.Version,
			"decision": decision, "conforms": report.Conforms,
			"focus_nodes": focusNodes, "graph_iri": graphIRI,
			"idempotency_key": idempotencyKey(r),
		})
	h.publish(id.TenantID.String(), "RunCreated", sse.Important,
		map[string]any{"run_id": final.ID, "status": final.Status, "decision": decision})

	return final, snap.Version, nil
}

// advanceReviewedRun 按第六篇 §14.2 的合法矩阵把新建 Run 推进到执行态。
//
// 审查在同一次请求内已经完成，因此这里按「上下文已构建 → 计划已生成 → 计划已校验」
// 逐步推进，最后按结论落到 COMPLETED 或 MANUAL_REVIEW。不新增事件、不跳步。
func (h *Handler) advanceReviewedRun(ctx context.Context, runID ids.ID, approve bool) error {
	for _, ev := range []harness.RunEventType{
		harness.EventContextBuildStarted,
		harness.EventContextBuildSucceeded,
		harness.EventPlanGenerated,
		harness.EventPlanValidationSucceeded,
		harness.EventPlanValidationSucceeded,
	} {
		if err := h.deps.Machine.Apply(ctx, runID, ev, noFacts()); err != nil {
			return err
		}
	}
	last := harness.EventManualInterventionRequired
	if approve {
		last = harness.EventRunCompleted
	}
	return h.deps.Machine.Apply(ctx, runID, last, noFacts())
}

// composeReviewOutcome 合成最终结论：引擎判定优先，模型判断只在引擎放行时才生效。
//
// 这个方向是刻意选定的：模型「说通过」而引擎判不合规时，若采纳模型，就等于让
// 一次语言判断覆盖真实的规则求值结果——那正是本项目最禁止的「伪造合规」。
func composeReviewOutcome(report shacl.Report, model reviewModelOutput) (decision string, reasons []string) {
	reasons = append(reasons, model.Reasons...)
	if !report.Conforms {
		reasons = append(reasons, "SHACL 引擎判定不合规："+summarizeFindings(report))
		return decisionNeedInfo, reasons
	}
	switch model.Decision {
	case decisionApprove:
		return decisionApprove, reasons
	case decisionNeedApproval:
		reasons = append(reasons, "模型建议提交审批：需人工确认后再执行受控操作")
		return decisionNeedApproval, reasons
	case decisionReject:
		reasons = append(reasons, "模型判定不满足业务规则")
		return decisionNeedInfo, reasons
	default:
		return decisionNeedInfo, reasons
	}
}

// summarizeFindings 给出发现项的短摘要（只用于人读的理由串，规则细节在形状发现里）。
func summarizeFindings(report shacl.Report) string {
	if len(report.Results) == 0 {
		return "未给出具体发现项"
	}
	var b strings.Builder
	for i, res := range report.Results {
		if i >= 5 {
			b.WriteString("…")
			break
		}
		if i > 0 {
			b.WriteString("；")
		}
		b.WriteString(res.FocusNode)
		if res.Path != "" {
			b.WriteString(" 的 " + res.Path)
		}
		b.WriteString(" " + res.Component)
	}
	return b.String()
}

// reviewPlan 由本体真实结构派生结构化执行计划（真实生成，非前端硬编码）。
func reviewPlan(parsed *graphload.Manifest, focusNodes int) map[string]any {
	classes := make([]string, 0, len(parsed.Classes))
	for _, c := range parsed.Classes {
		classes = append(classes, c.Name)
	}
	shapeIDs := make([]string, 0, len(parsed.Shapes))
	for _, s := range parsed.Shapes {
		shapeIDs = append(shapeIDs, s.ID)
	}
	return map[string]any{
		"source":      "ontology",
		"classes":     classes,
		"shape_ids":   shapeIDs,
		"focus_nodes": focusNodes,
		"steps": []map[string]any{
			{"kind": stepContextBuild, "name": "装载本体上下文"},
			{"kind": stepFactExtract, "name": "抽取业务事实"},
			{"kind": stepGraphLoad, "name": "装载业务事实数据图"},
			{"kind": stepShaclValidate, "name": "执行形状约束校验"},
			{"kind": stepRuleReview, "name": "按规则复核"},
			{"kind": stepConclusion, "name": "合成审查结论"},
		},
	}
}

// reviewSteps 把每个真实阶段投影为一条 runtime.run_steps。
//
// result_payload 里刻意带上前端渲染所需的 rationale / ontology_version /
// rule_refs / fact_refs，使任务详情页无需在前端推断或补造内容。
func reviewSteps(parsed *graphload.Manifest, snap *store.OntologyVersionSnapshot, model reviewModelOutput,
	report shacl.Report, focusNodes int, graphIRI, decision string, tboxCount, aboxCount int,
	startedAt, finishedAt time.Time) []store.RunStepInput {
	factRefs := make([]string, 0, len(model.Facts))
	for _, f := range model.Facts {
		factRefs = append(factRefs, f.Class+"/"+f.ID)
	}
	shapeIDs := make([]string, 0, len(parsed.Shapes))
	for _, s := range parsed.Shapes {
		shapeIDs = append(shapeIDs, s.ID)
	}
	classNames := make([]string, 0, len(parsed.Classes))
	for _, c := range parsed.Classes {
		classNames = append(classNames, c.Name)
	}
	payload := func(name, rationale string, extra map[string]any) []byte {
		base := map[string]any{
			"name":             name,
			"rationale":        rationale,
			"ontology_version": snap.Version,
		}
		for k, v := range extra {
			base[k] = v
		}
		raw, _ := json.Marshal(base)
		return raw
	}
	st := func(no int, stepType, name, rationale string, extra map[string]any) store.RunStepInput {
		s, f := startedAt, finishedAt
		return store.RunStepInput{
			StepNo:        no,
			StepType:      stepType,
			Status:        "SUCCEEDED",
			ResultPayload: payload(name, rationale, extra),
			StartedAt:     &s,
			FinishedAt:    &f,
		}
	}
	steps := []store.RunStepInput{
		st(1, stepContextBuild, "装载本体上下文",
			"读取本体版本快照并解析清单，作为本次审查的规则来源",
			map[string]any{
				"ontology_key": snap.OntologyKey, "ontology_name": snap.Name,
				"classes": classNames, "shape_ids": shapeIDs,
			}),
		st(2, stepFactExtract, "抽取业务事实",
			"大模型依据本体词表从业务目标中抽取业务事实",
			map[string]any{
				"facts": model.Facts, "model_decision": model.Decision,
				"fact_refs": factRefs,
			}),
		st(3, stepGraphLoad, "装载业务事实数据图",
			"把本体结构与复核后的业务事实装载到本次任务专属的命名图",
			map[string]any{
				"graph_iri": graphIRI, "tbox_triples": tboxCount, "abox_triples": aboxCount,
				"focus_nodes": focusNodes, "fact_refs": factRefs,
			}),
		st(4, stepShaclValidate, "执行形状约束校验",
			"使用本体形状约束执行 SHACL 校验，得到权威规则结论",
			map[string]any{
				"conforms": report.Conforms, "focus_nodes": focusNodes,
				"findings": len(report.Results), "shape_ids": shapeIDs,
				"rule_refs": shapeIDs,
			}),
		st(5, stepRuleReview, "按规则复核",
			"按形状约束逐条复核，记录模型的判断与依据",
			map[string]any{
				"model_decision": model.Decision, "rule_checks": model.RuleChecks,
				"reasons": model.Reasons, "rule_refs": shapeIDs,
			}),
		st(6, stepConclusion, "合成审查结论",
			"服务端依据真实 SHACL 结果确定性合成审查结论（模型判断不得覆盖不合规结论）",
			map[string]any{
				"decision": decision, "conforms": report.Conforms,
				"reasons": append([]string{}, model.Reasons...),
			}),
	}
	return steps
}

// mergeManifestInstances 把模型抽取的事实并回本体清单，交由同一解析器复核。
//
// 之所以不另写一套实例校验：graphload.ParseManifest 是本体装载/校验路径的唯一
// 入口，这里复用它意味着「模型产出的实例」与「种子/生成产出的实例」受完全相同的
// 闭包约束——不会出现「生成端严格、审查端宽松」的双标。
func mergeManifestInstances(manifest json.RawMessage, facts []reviewFact) (json.RawMessage, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(manifest, &doc); err != nil {
		return nil, errors.Wrap(err, errors.CodeValidationFailed, "本体清单不是合法 JSON")
	}
	factsJSON, err := json.Marshal(facts)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "序列化业务事实失败")
	}
	doc["instances"] = factsJSON
	merged, err := json.Marshal(doc)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "合并本体清单失败")
	}
	return merged, nil
}

// parseIDOrZero 把可选文本 id 解析为 ids.ID；空串或非法值返回零值。
//
// 非法值返回零值而不是 panic：该 id 来自服务端读取的本体版本快照，
// 若已非法说明存储层被外部改动；此时 Run 仍应可创建（本体版本列留 NULL），
// 但结论与步骤照常，不会因此产生一条写坏的数据。
func parseIDOrZero(raw string) ids.ID {
	if strings.TrimSpace(raw) == "" {
		return ids.ID{}
	}
	parsed, err := ids.Parse(raw)
	if err != nil {
		return ids.ID{}
	}
	return parsed
}
