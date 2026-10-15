package agent

import (
	"context"
	"testing"

	"odap/internal/llm"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

func plannerGateway(p llm.Provider) *llm.Gateway {
	return &llm.Gateway{Providers: map[string]llm.Provider{p.Name(): p}}
}

const validPlanJSON = `{
  "domain": "FINANCE",
  "nodes": [
    {"id": "s", "kind": "START"},
    {"id": "q", "kind": "READ_TOOL", "deps": ["s"], "tool_key": "semantic.query.expenses", "effect_class": "READ"},
    {"id": "e", "kind": "END", "deps": ["q"]}
  ]
}`

const cyclicPlanJSON = `{
  "nodes": [
    {"id": "s", "kind": "START"},
    {"id": "a", "kind": "NOOP", "deps": ["b"]},
    {"id": "b", "kind": "NOOP", "deps": ["a"]},
    {"id": "e", "kind": "END", "deps": ["s"]}
  ]
}`

const writePlanMissingGateJSON = `{
  "nodes": [
    {"id": "s", "kind": "START"},
    {"id": "w", "kind": "WRITE_TOOL", "deps": ["s"], "tool_key": "erp.submit", "effect_class": "EXTERNAL_WRITE"},
    {"id": "e", "kind": "END", "deps": ["w"]}
  ]
}`

func TestPlannerPropose(t *testing.T) {
	tenant := ids.New()
	intent := Intent{Domain: "FINANCE", Utterance: "帮我准备一张报销单", AllowedTools: []string{"semantic.query.expenses"}, OntologyVersion: "ont-1"}

	t.Run("合法计划 JSON 成功", func(t *testing.T) {
		p := llm.NewMockProvider("mock", llm.ProviderCapabilities{})
		p.PushContent(validPlanJSON)
		planner := &Planner{LLM: plannerGateway(p)}
		plan, err := planner.Propose(context.Background(), tenant, intent)
		if err != nil {
			t.Fatalf("Propose: %v", err)
		}
		if len(plan.Nodes) != 3 || plan.Domain != "FINANCE" {
			t.Fatalf("plan=%+v", plan)
		}
		if plan.VersionLock.OntologyVersion != "ont-1" {
			t.Fatalf("版本锁定未带入：%+v", plan.VersionLock)
		}
	})

	t.Run("环状计划返回候选与错误", func(t *testing.T) {
		p := llm.NewMockProvider("mock", llm.ProviderCapabilities{})
		p.PushContent(cyclicPlanJSON)
		planner := &Planner{LLM: plannerGateway(p)}
		plan, err := planner.Propose(context.Background(), tenant, intent)
		if err == nil {
			t.Fatalf("环状计划应返回错误")
		}
		if errors.As(err).Code != errors.CodeValidationFailed {
			t.Fatalf("错误码=%s, want VALIDATION_FAILED", errors.As(err).Code)
		}
		if len(plan.Nodes) == 0 {
			t.Fatalf("应同时返回候选计划供上层编译/裁决")
		}
	})

	t.Run("写节点缺审批门禁返回错误", func(t *testing.T) {
		p := llm.NewMockProvider("mock", llm.ProviderCapabilities{})
		p.PushContent(writePlanMissingGateJSON)
		planner := &Planner{LLM: plannerGateway(p)}
		if _, err := planner.Propose(context.Background(), tenant, intent); errors.As(err).Code != errors.CodeValidationFailed {
			t.Fatalf("应返回 VALIDATION_FAILED，got %v", err)
		}
	})

	t.Run("非 JSON 输出返回 MODEL_OUTPUT_INVALID", func(t *testing.T) {
		p := llm.NewMockProvider("mock", llm.ProviderCapabilities{})
		// 修复回路上限 2 次，需提供 3 条非法响应。
		for i := 0; i < llm.MaxSchemaRepairAttempts+1; i++ {
			p.PushContent("这不是 JSON")
		}
		planner := &Planner{LLM: plannerGateway(p)}
		_, err := planner.Propose(context.Background(), tenant, intent)
		if errors.As(err).Code != errors.CodeModelOutputInvalid {
			t.Fatalf("应返回 MODEL_OUTPUT_INVALID，got %v", err)
		}
	})

	t.Run("未配置 Gateway 拒绝", func(t *testing.T) {
		planner := &Planner{}
		if _, err := planner.Propose(context.Background(), tenant, intent); errors.As(err).Code != errors.CodeInternal {
			t.Fatalf("应返回 INTERNAL，got %v", err)
		}
	})

	t.Run("零租户拒绝", func(t *testing.T) {
		p := llm.NewMockProvider("mock", llm.ProviderCapabilities{})
		planner := &Planner{LLM: plannerGateway(p)}
		if _, err := planner.Propose(context.Background(), ids.ID{}, intent); errors.As(err).Code != errors.CodeValidationFailed {
			t.Fatalf("应返回 VALIDATION_FAILED，got %v", err)
		}
	})
}
