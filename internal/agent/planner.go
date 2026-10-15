package agent

import (
	"context"
	"encoding/json"
	"strings"

	"odap/internal/llm"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// Intent 是规划输入（第十四篇 §3.1 BusinessIntent 的工程化子集）。
type Intent struct {
	Domain          string
	Utterance       string
	AllowedTools    []string
	OntologyVersion string
}

// Planner 只产候选计划（ADR-001 决策 1）。
//
// 关键约束：LLM 输出不可信。Planner 的职责止于"生成候选逻辑计划"，
// 不拥有编译权、类型检查权与执行权；候选计划未经 Harness Plan Compiler
// 编译（类型检查/数据流校验/依赖无环/版本绑定/freeze）不得进入执行状态。
// 因此 Propose 在结构校验失败时返回"候选计划 + 错误"，由上层编译/裁决。
type Planner struct {
	// LLM 是受治理的推理入口（限流/预算/缓存/结构化输出回路）。
	LLM *llm.Gateway
	// ModelID 指定规划所用模型（空则由 Router 选择）。
	ModelID string
}

// planResponseSchema 是规划输出的最小 JSON Schema（供结构化输出校验）。
// 仅使用 tools.ValidateArgs 支持的子集（type/required/properties/items/enum/min/max）。
const planResponseSchema = `{
  "type": "object",
  "required": ["nodes"],
  "properties": {
    "domain": {"type": "string"},
    "nodes": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["id", "kind"],
        "properties": {
          "id": {"type": "string"},
          "kind": {"type": "string"},
          "deps": {"type": "array", "items": {"type": "string"}},
          "tool_key": {"type": "string"},
          "effect_class": {"type": "string"}
        }
      }
    }
  }
}`

const plannerSystemPrompt = `你是 ODAP 语义规划助手，只输出候选执行计划 JSON。
要求：
1) 节点类型只能取 START/END/LLM_CALL/TOOL_CALL/READ_TOOL/WRITE_TOOL/SEMANTIC_QUERY/APPROVAL_GATE/DECISION/PARALLEL_FORK/JOIN/SUBPLAN/NOOP。
2) 依赖图必须无环，必须包含 START 与 END。
3) 任何写操作节点（WRITE_TOOL）必须声明 effect_class=EXTERNAL_WRITE，且必须存在 APPROVAL_GATE 前驱。
4) 只输出 JSON，不要输出解释文本。`

// planDraft 是面向 LLM 的候选计划 DTO（字符串 ID，便于模型输出）。
type planDraft struct {
	Domain string      `json:"domain"`
	Nodes  []nodeDraft `json:"nodes"`
}

type nodeDraft struct {
	ID           string          `json:"id"`
	Kind         string          `json:"kind"`
	Deps         []string        `json:"deps"`
	ToolKey      string          `json:"tool_key"`
	EffectClass  string          `json:"effect_class"`
	Params       json.RawMessage `json:"params"`
	EvidenceRefs []string        `json:"evidence_refs"`
}

// Propose 生成候选计划并做结构校验（第十四篇 §5.3 规划器内部流程）。
//
// 返回约定：
//   - 结构校验通过：返回 (plan, nil)；
//   - 结构校验失败：返回 (候选 plan, err)，由上层 Harness Plan Compiler 决定
//     修复策略（重规划/拒绝），本函数不擅自修复或执行；
//   - LLM 输出非法 JSON / 结构化校验失败：返回 (zero, CodeModelOutputInvalid)。
func (p *Planner) Propose(ctx context.Context, tenantID ids.ID, intent Intent) (Plan, error) {
	if p == nil || p.LLM == nil {
		return Plan{}, errors.E(errors.CodeInternal, "agent: planner 未配置 LLM Gateway")
	}
	if tenantID.IsZero() {
		return Plan{}, errors.E(errors.CodeValidationFailed, "agent: tenantID 不能为空")
	}
	runID := ids.New()
	req := llm.CompletionRequest{
		TenantID:           tenantID,
		RunID:              runID,
		StepID:             ids.New(),
		ModelID:            p.ModelID,
		Messages:           []llm.Message{{Role: "system", Content: plannerSystemPrompt}, {Role: "user", Content: buildUserPrompt(intent)}},
		ResponseSchema:     json.RawMessage(planResponseSchema),
		DataClassification: llm.DataClassInternal,
		IdempotencyKey:     runID.String(),
	}
	comp, err := p.LLM.Complete(ctx, tenantID, req)
	if err != nil {
		return Plan{}, err
	}

	var draft planDraft
	if err := json.Unmarshal([]byte(comp.Content), &draft); err != nil {
		return Plan{}, errors.Wrap(err, errors.CodeModelOutputInvalid, "agent: 候选计划输出非合法 JSON")
	}
	plan := draftToPlan(draft, tenantID, intent)
	if err := ValidatePlan(plan); err != nil {
		return plan, err
	}
	return plan, nil
}

// draftToPlan 把候选 DTO 转为 Plan IR；ID 由可信服务端生成，不采信模型提供的 ID。
func draftToPlan(d planDraft, tenantID ids.ID, intent Intent) Plan {
	domain := d.Domain
	if domain == "" {
		domain = intent.Domain
	}
	nodes := make([]Node, 0, len(d.Nodes))
	for _, nd := range d.Nodes {
		nodes = append(nodes, Node{
			ID:           nd.ID,
			Kind:         NodeKind(nd.Kind),
			Deps:         nd.Deps,
			ToolKey:      nd.ToolKey,
			EffectClass:  nd.EffectClass,
			Params:       nd.Params,
			EvidenceRefs: nd.EvidenceRefs,
		})
	}
	return Plan{
		PlanID:      ids.New(),
		TenantID:    tenantID,
		IntentID:    ids.New(),
		Domain:      domain,
		Nodes:       nodes,
		VersionLock: VersionLock{OntologyVersion: intent.OntologyVersion},
	}
}

func buildUserPrompt(intent Intent) string {
	var b strings.Builder
	b.WriteString("业务域：")
	b.WriteString(intent.Domain)
	b.WriteString("\n用户诉求：")
	b.WriteString(intent.Utterance)
	// 工具白名单进计划：AllowedTools 是本次运行可用工具的完整集合
	// （manifest.tools + 技能 tool_bindings 并集，见 skills.go 的
	// SkillExpansion.MergeToolKeys）。Planner 只能从中选择，不得编造工具键。
	if len(intent.AllowedTools) > 0 {
		b.WriteString("\n可用工具（只允许从下列工具中选择，不得编造工具键）：")
		b.WriteString("\n" + strings.Join(intent.AllowedTools, ", "))
	}
	return b.String()
}
