package handlers

// 本体驱动审查的纯函数单测。
//
// 为什么只测纯函数：整条链路中真正不可替代的部分是「结论怎么合成」「模型产出
// 怎么复核」这两个判定，它们不需要数据库、不需要模型网关，却决定了系统会不会
// 输出「伪造的合规」。而端到端链路依赖真实模型端点（本环境容器无外网），
// 一旦把验证绑在端到端上，这些不变式就长期没有自动化保护。
//
// 被锁住的三条不变式：
//  1. 模型说「通过」**不能**覆盖 SHACL 引擎判定的「不合规」；
//  2. 模型抽取的业务事实必须过平台自己的清单解析器（类/属性/取值闭包）；
//  3. 平台的模型输出 Schema 必须落在平台支持的 JSON Schema 子集内。

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"odap/internal/api/store"
	"odap/internal/ontology/graphload"
	"odap/internal/ontology/shacl"
	"odap/internal/tools"
)

// reviewTestManifest 是一份最小的、结构完整且能通过 graphload.ParseManifest 的清单。
const reviewTestManifest = `{
  "summary": "订单类业务对象",
  "classes": [
    {
      "name": "Order",
      "label": "订单",
      "description": "业务单据",
      "attributes": [
        {"name": "amount", "type": "number", "description": "金额", "required": true},
        {"name": "status", "type": "enum", "description": "状态", "required": false}
      ]
    }
  ],
  "relations": [],
  "enumerations": [{"name": "OrderStatus", "values": ["NEW", "PAID"]}],
  "shapes": [
    {
      "id": "OrderShape",
      "target_class": "Order",
      "severity": "Violation",
      "properties": [{"path": "amount", "min_count": 1}]
    }
  ],
  "instances": []
}`

// mustParseReviewManifest 解析测试清单；解析失败即视为夹具损坏。
func mustParseReviewManifest(t *testing.T) *graphload.Manifest {
	t.Helper()
	m, err := graphload.ParseManifest([]byte(reviewTestManifest))
	if err != nil {
		t.Fatalf("测试清单不合法（夹具损坏）：%v", err)
	}
	return m
}

// TestComposeReviewOutcome_EngineVerdictCannotBeOverriddenByModel 锁住本项目
// 最核心的一条业务不变式：机检结论优先于模型判断。
//
// 若这条反了（采纳模型 APPROVE），系统就会在「SHACL 已判定不合规」时对外输出
// 「合规」——这正是硬约束里禁止的「伪造合规」。因此这里对「模型 APPROVE + 引擎
// 不合规」单独设一条最严格的断言：既要求结论是 NEED_INFO，也要求理由里带上引擎
// 的发现项（不能悄悄吞掉）。
func TestComposeReviewOutcome_EngineVerdictCannotBeOverriddenByModel(t *testing.T) {
	violation := shacl.Report{
		Conforms: false,
		Results: []shacl.Result{{
			ShapeID: "OrderShape", Component: "MinCount",
			FocusNode: "https://odap.example/ont/test#inst/o1", Path: "https://odap.example/ont/test#Order.amount",
			Message: "取值数不足", Severity: string(shacl.SeverityViolation),
		}},
	}
	conform := shacl.Report{Conforms: true}

	t.Run("模型 APPROVE + 引擎不合规 → NEED_INFO（模型不得覆盖）", func(t *testing.T) {
		decision, reasons := composeReviewOutcome(violation, reviewModelOutput{
			Decision: decisionApprove, Reasons: []string{"模型认为满足业务规则"},
		})
		if decision != decisionNeedInfo {
			t.Fatalf("期望 NEED_INFO，实际 %s", decision)
		}
		joined := strings.Join(reasons, "\n")
		if !strings.Contains(joined, "SHACL") {
			t.Fatalf("理由中必须体现引擎判定，实际：%v", reasons)
		}
	})

	t.Run("模型 APPROVE + 引擎合规 → APPROVE", func(t *testing.T) {
		decision, _ := composeReviewOutcome(conform, reviewModelOutput{Decision: decisionApprove})
		if decision != decisionApprove {
			t.Fatalf("期望 APPROVE，实际 %s", decision)
		}
	})

	t.Run("模型 REJECT + 引擎合规 → NEED_INFO（不擅自执行受控结论）", func(t *testing.T) {
		decision, reasons := composeReviewOutcome(conform, reviewModelOutput{Decision: decisionReject})
		if decision != decisionNeedInfo {
			t.Fatalf("期望 NEED_INFO，实际 %s", decision)
		}
		if len(reasons) == 0 {
			t.Fatal("期望给出理由，实际为空")
		}
	})

	t.Run("模型 NEED_APPROVAL + 引擎合规 → NEED_APPROVAL（保留人工审批）", func(t *testing.T) {
		decision, _ := composeReviewOutcome(conform, reviewModelOutput{Decision: decisionNeedApproval})
		if decision != decisionNeedApproval {
			t.Fatalf("期望 NEED_APPROVAL，实际 %s", decision)
		}
	})

	t.Run("模型输出为空（decision 缺失）+ 引擎合规 → NEED_INFO", func(t *testing.T) {
		decision, _ := composeReviewOutcome(conform, reviewModelOutput{})
		if decision != decisionNeedInfo {
			t.Fatalf("期望 NEED_INFO，实际 %s", decision)
		}
	})
}

// TestOntologyReviewSchemaWithinPlatformSubset 证明平台下发给出模型的审查输出
// Schema 落在平台支持的 JSON Schema 子集内，且确实启用了「防多余字段 + 输出规模
// 预算」两项。
//
// 与既有 TestOntologyManifestSchemaWithinPlatformSubset 同因：模型输出校验会在
// 解析 Schema 阶段就拒绝未登记关键字（fail-closed），平台自己的 Schema 越界会把
// 「平台的缺陷」报成「模型输出不合法」，且只在真实端点接通后才暴露。
func TestOntologyReviewSchemaWithinPlatformSubset(t *testing.T) {
	parsed := mustParseReviewManifest(t)
	schema := ontologyReviewSchema(parsed)

	if err := tools.ValidateSchema(schema); err != nil {
		t.Fatalf("审查输出 Schema 不在平台支持子集内：%v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(schema, &doc); err != nil {
		t.Fatalf("Schema 不是合法 JSON：%v", err)
	}
	if doc["additionalProperties"] != false {
		t.Fatal("顶层必须启用 additionalProperties:false：否则模型多余字段会穿透到下游清单解析器")
	}
	// 数组上限：facts / reasons / rule_checks 三个数组都必须有界。
	props, _ := doc["properties"].(map[string]any)
	for _, name := range []string{"facts", "reasons", "rule_checks"} {
		sub, ok := props[name].(map[string]any)
		if !ok {
			t.Fatalf("Schema 缺少 %s 数组声明", name)
		}
		if _, ok := sub["maxItems"]; !ok {
			t.Fatalf("%s 必须有 maxItems 上限：无上限会导致输出规模无界被截断", name)
		}
	}
	// 平台子集不支持 oneOf/anyOf；出现即会被解析阶段拒绝。
	text := string(schema)
	for _, banned := range []string{"oneOf", "anyOf"} {
		if strings.Contains(text, banned) {
			t.Fatalf("Schema 使用了平台子集外的关键字 %s", banned)
		}
	}
}

// TestMergeManifestInstances_ClosureRejectsUndefined 证明模型抽取的事实会被平台
// 自己的清单解析器复核：类、属性、取值三项闭包任一不满足都必须报错。
//
// 这里刻意复用的是装载路径的同一解析器（graphload.ParseManifest）——如果审查链路
// 自己另写一套宽松校验，就会出现「生成端严格、审查端宽松」的双标，模型编造的
// 字段会静默进入数据图。
func TestMergeManifestInstances_ClosureRejectsUndefined(t *testing.T) {
	merged := func(t *testing.T, facts []reviewFact) ([]byte, error) {
		t.Helper()
		out, err := mergeManifestInstances(json.RawMessage(reviewTestManifest), facts)
		if err != nil {
			return nil, err
		}
		// 复核必须走真实解析器：这一步才能证明「引用存在、类型可转」。
		_, err = graphload.ParseManifest(out)
		return out, err
	}

	t.Run("合法事实 → 通过", func(t *testing.T) {
		_, err := merged(t, []reviewFact{{
			Class: "Order", ID: "o1",
			Properties: map[string]any{"amount": "1250.00", "status": "NEW"},
		}})
		if err != nil {
			t.Fatalf("合法事实不应报错：%v", err)
		}
	})

	t.Run("未声明的类 → 报错", func(t *testing.T) {
		if _, err := merged(t, []reviewFact{{
			Class: "NoSuchClass", ID: "x1", Properties: map[string]any{},
		}}); err == nil {
			t.Fatal("引用了未声明的类却通过校验，闭包失效")
		}
	})

	t.Run("未声明的属性键 → 报错", func(t *testing.T) {
		if _, err := merged(t, []reviewFact{{
			Class: "Order", ID: "x2",
			Properties: map[string]any{"nonexistent_key": "v"},
		}}); err == nil {
			t.Fatal("写了未声明的属性键却通过校验，闭包失效")
		}
	})

	t.Run("数值属性收到非数字字符串 → 报错", func(t *testing.T) {
		if _, err := merged(t, []reviewFact{{
			Class: "Order", ID: "x3",
			Properties: map[string]any{"amount": "abc"},
		}}); err == nil {
			t.Fatal("数值属性收到不可解析字符串却通过校验，字面量类型未复核")
		}
	})

	t.Run("数值属性收到数字字面量字符串 → 通过（按声明类型强转）", func(t *testing.T) {
		if _, err := merged(t, []reviewFact{{
			Class: "Order", ID: "x4",
			Properties: map[string]any{"amount": "1250"},
		}}); err != nil {
			t.Fatalf("可解析的数值字符串应被接受：%v", err)
		}
	})

	t.Run("清单本身不是合法 JSON → 报错（不静默返回原清单）", func(t *testing.T) {
		if _, err := mergeManifestInstances(json.RawMessage("{not json"), nil); err == nil {
			t.Fatal("非法清单却返回成功，合并不可能成立")
		}
	})
}

// TestReviewSteps_EveryStageIsRecorded 锁住「任务详情能看到基于本体的处理过程」
// 这一需求的结构前提：六个阶段各一条步骤、序号连续、每条都带人能读的名称与依据。
//
// 若步骤名或依据缺失，任务详情页只能显示裸 step_type 常量，与「基于本体处理」
// 的叙述对不上，用户看到的就是一串技术标识符。
func TestReviewSteps_EveryStageIsRecorded(t *testing.T) {
	parsed := mustParseReviewManifest(t)
	snap := &store.OntologyVersionSnapshot{
		OntologyID: "f0000000-0000-4000-8000-000000000001", OntologyKey: "finance-core",
		Name: "财务核心本体", VersionID: "f0000000-0000-4000-8000-000000000002", Version: "0.1.0",
	}
	model := reviewModelOutput{
		Decision: decisionApprove,
		Facts:    []reviewFact{{Class: "Order", ID: "o1", Properties: map[string]any{"amount": "1250.00"}}},
		Reasons:  []string{"满足最小金额约束"},
	}
	steps := reviewSteps(parsed, snap, model, shacl.Report{Conforms: true}, 1,
		"https://odap.example/ont/finance-core/run-1", decisionApprove, 3, 2,
		time.Unix(0, 0).UTC(), time.Unix(0, 0).UTC())

	wantTypes := []string{stepContextBuild, stepFactExtract, stepGraphLoad, stepShaclValidate,
		stepRuleReview, stepConclusion}
	if len(steps) != len(wantTypes) {
		t.Fatalf("期望 %d 个阶段，实际 %d", len(wantTypes), len(steps))
	}
	for i, st := range steps {
		if st.StepNo != i+1 {
			t.Fatalf("第 %d 条步骤序号应为 %d，实际 %d", i, i+1, st.StepNo)
		}
		if st.StepType != wantTypes[i] {
			t.Fatalf("第 %d 条步骤类型应为 %s，实际 %s", i, wantTypes[i], st.StepType)
		}
		if st.Status != "SUCCEEDED" {
			t.Fatalf("第 %d 条步骤状态应为 SUCCEEDED，实际 %s", i, st.Status)
		}
		var payload map[string]any
		if err := json.Unmarshal(st.ResultPayload, &payload); err != nil {
			t.Fatalf("第 %d 条步骤 payload 不是合法 JSON：%v", i, err)
		}
		for _, key := range []string{"name", "rationale", "ontology_version"} {
			if s, _ := payload[key].(string); s == "" {
				t.Fatalf("第 %d 条步骤 payload 缺少 %s", i, key)
			}
		}
	}
}

// TestRunStepViewOf_LiftsTraceFields 证明步骤视图会把 payload 里的叙述字段提升到
// 顶层，且 payload 损坏时退化为如实呈现 step_type，而不是编造名称或依据。
func TestRunStepViewOf_LiftsTraceFields(t *testing.T) {
	runID := "11111111-1111-4111-8111-111111111111"

	t.Run("合法 payload → 字段提升", func(t *testing.T) {
		st := store.RunStep{
			StepNo: 2, ID: "s2", StepType: stepFactExtract, Status: "SUCCEEDED",
			ResultPayload: json.RawMessage(`{"name":"抽取业务事实","rationale":"依据本体词表",
                "ontology_version":"0.1.0","rule_refs":["OrderShape"],"fact_refs":["Order/o1"]}`),
		}
		v := runStepViewOf(runID, st)
		if v.Seq != 2 || v.RunID != runID {
			t.Fatalf("序号/归属错误：%+v", v)
		}
		if v.Name != "抽取业务事实" {
			t.Fatalf("期望提升 name，实际 %q", v.Name)
		}
		if v.Kind != stepFactExtract {
			t.Fatalf("kind 应保持步骤类型，实际 %q", v.Kind)
		}
		if v.Rationale == nil || *v.Rationale != "依据本体词表" {
			t.Fatalf("期望提升 rationale，实际 %v", v.Rationale)
		}
		if v.OntologyVersion == nil || *v.OntologyVersion != "0.1.0" {
			t.Fatalf("期望提升 ontology_version，实际 %v", v.OntologyVersion)
		}
		if len(v.RuleRefs) != 1 || v.RuleRefs[0] != "OrderShape" {
			t.Fatalf("期望提升 rule_refs，实际 %v", v.RuleRefs)
		}
		if len(v.FactRefs) != 1 || v.FactRefs[0] != "Order/o1" {
			t.Fatalf("期望提升 fact_refs，实际 %v", v.FactRefs)
		}
	})

	t.Run("payload 损坏 → 只回退到 step_type，不编造叙述字段", func(t *testing.T) {
		st := store.RunStep{
			StepNo: 1, ID: "s1", StepType: stepContextBuild, Status: "SUCCEEDED",
			ResultPayload: json.RawMessage("this is not json"),
		}
		v := runStepViewOf(runID, st)
		if v.Name != stepContextBuild {
			t.Fatalf("损坏 payload 时 name 应回退为 step_type，实际 %q", v.Name)
		}
		if v.Rationale != nil || v.OntologyVersion != nil {
			t.Fatalf("损坏 payload 时不得编造字段：rationale=%v ontology_version=%v", v.Rationale, v.OntologyVersion)
		}
	})
}

// TestRunViewOf_OntologyBindingProjection 证明本体驱动 Run 的视图投影：
//   - 有本体版本号时才展示 ontology_version（查不到就不展示，而不是回填 UUID）；
//   - 本体驱动 Run 不伪造 agent_version_id，Agent 驱动 Run 正常展示。
func TestRunViewOf_OntologyBindingProjection(t *testing.T) {
	objective := "审查一笔 1250 元的采购订单"
	versionID := "f0000000-0000-4000-8000-000000000002"
	byType := "采购"

	t.Run("查到版本号 → 展示 ontology_version", func(t *testing.T) {
		run := &store.Run{
			ID: "r1", Status: "COMPLETED", RequestedBy: "u1",
			Objective: &objective, BusinessType: &byType, OntologyVersionID: &versionID,
		}
		v := runViewOf(run, "0.1.0")
		if v.Objective != objective {
			t.Fatalf("期望 objective，实际 %q", v.Objective)
		}
		if v.OntologyVersion == nil || *v.OntologyVersion != "0.1.0" {
			t.Fatalf("期望版本号 0.1.0，实际 %v", v.OntologyVersion)
		}
		if v.AgentVersionID != nil {
			t.Fatalf("本体驱动 Run 不得伪造 agent_version_id，实际 %v", *v.AgentVersionID)
		}
		if v.BusinessType == nil || *v.BusinessType != byType {
			t.Fatalf("期望业务类型，实际 %v", v.BusinessType)
		}
	})

	t.Run("未查到版本号 → 省略 ontology_version（不冒充 UUID）", func(t *testing.T) {
		run := &store.Run{ID: "r2", Status: "COMPLETED", OntologyVersionID: &versionID}
		v := runViewOf(run, "")
		if v.OntologyVersion != nil {
			t.Fatalf("未解析出版本号时不得展示 ontology_version，实际 %v", *v.OntologyVersion)
		}
	})

	t.Run("Agent 驱动 Run → 展示 agent_version_id", func(t *testing.T) {
		run := &store.Run{ID: "r3", Status: "CREATED", AgentVersionID: "a1"}
		v := runViewOf(run, "")
		if v.AgentVersionID == nil || *v.AgentVersionID != "a1" {
			t.Fatalf("期望 agent_version_id=a1，实际 %v", v.AgentVersionID)
		}
	})
}
