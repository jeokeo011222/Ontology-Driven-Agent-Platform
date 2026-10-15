// Package agent 实现 ODAP 语义执行计划的候选生成、结构校验与步骤执行编排。
//
// 权威来源与职责边界：
//   - docs/adr/ADR-001-PlanIR合并规范.md：Planner 只产候选计划；编译/类型检查/
//     策略检查归 Harness Plan Compiler（第六篇 §5）；Orchestrator 只管 DAG 调度。
//   - docs/第十四篇 §5-§6（Plan IR 与 13 类节点基线）、§9（执行前安全门）、
//     §12（审批/授权/高风险动作治理）、§17.2（只读执行器 vs 写网关）。
//   - docs/第八篇 §7.4/§8.2（任务创建权独占 Orchestrator）、§17.2（委派深度控制）。
//   - docs/第六篇 §3.3（状态更新必须经 Harness Machine，禁止直接写 status）。
//
// 本包不编译计划、不冻结版本、不做策略裁决；这些均属 Harness Plan Compiler。
package agent

import (
	"encoding/json"
	"fmt"
	"sort"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// NodeKind 是执行计划节点类型。
//
// 命名与 ADR-001 §6 的 13 类节点基线一一对应（第十四篇 §6.2 为唯一枚举基线）：
//
//	本枚举            →  ADR-001 §6.2 基线
//	START / END       →  工作流入口/出口（基线隐式）
//	LLM_CALL          →  模型推理步骤（Compute 语义）
//	TOOL_CALL         →  TOOL_CALL
//	READ_TOOL         →  TOOL_CALL（effect_class = READ/COMPUTE）
//	WRITE_TOOL        →  TOOL_CALL（effect_class = INTERNAL_WRITE/EXTERNAL_WRITE）
//	SEMANTIC_QUERY    →  SEMANTIC_QUERY
//	APPROVAL_GATE     →  APPROVAL_GATE
//	DECISION          →  BRANCH
//	PARALLEL_FORK     →  FAN_OUT
//	JOIN              →  JOIN
//	SUBPLAN           →  复合子计划（基线以计划引用表达）
//	NOOP              →  占位/收尾（对应 EVIDENCE_FINALIZE 等空操作收尾）
//
// 同步约定：新增节点类型必须先走 ADR 并同步第六篇 §5、第十四篇 §6.2 与契约文件。
type NodeKind string

// 13 类节点类型常量。
const (
	NodeStart         NodeKind = "START"
	NodeEnd           NodeKind = "END"
	NodeLLMCall       NodeKind = "LLM_CALL"
	NodeToolCall      NodeKind = "TOOL_CALL"
	NodeReadTool      NodeKind = "READ_TOOL"
	NodeWriteTool     NodeKind = "WRITE_TOOL"
	NodeSemanticQuery NodeKind = "SEMANTIC_QUERY"
	NodeApprovalGate  NodeKind = "APPROVAL_GATE"
	NodeDecision      NodeKind = "DECISION"
	NodeParallelFork  NodeKind = "PARALLEL_FORK"
	NodeJoin          NodeKind = "JOIN"
	NodeSubplan       NodeKind = "SUBPLAN"
	NodeNoop          NodeKind = "NOOP"
)

// AllNodeKinds 返回全部 13 类节点类型（顺序即注册顺序）。
var AllNodeKinds = []NodeKind{
	NodeStart, NodeEnd, NodeLLMCall, NodeToolCall, NodeReadTool, NodeWriteTool,
	NodeSemanticQuery, NodeApprovalGate, NodeDecision, NodeParallelFork,
	NodeJoin, NodeSubplan, NodeNoop,
}

// Valid 报告节点类型是否已登记。
func (k NodeKind) Valid() bool {
	for _, x := range AllNodeKinds {
		if x == k {
			return true
		}
	}
	return false
}

// 动作副作用分级（第十四篇 §4.3）。
// 动作类别由受信任的工具注册与治理流程决定，Agent 不得在运行时自行降低风险等级。
const (
	EffectRead          = "READ"
	EffectCompute       = "COMPUTE"
	EffectInternalWrite = "INTERNAL_WRITE"
	EffectExternalWrite = "EXTERNAL_WRITE"
)

// ConditionSpec 是受控分支条件（DECISION 节点的条件，第十四篇 §6.3 控制依赖）。
type ConditionSpec struct {
	Kind string `json:"kind"`
	Expr string `json:"expr"`
}

// Node 是计划 IR 中的一个节点（第十四篇 §6.1）。
type Node struct {
	ID   string   `json:"id"`
	Kind NodeKind `json:"kind"`
	Deps []string `json:"deps,omitempty"`
	// ToolKey 是工具登录键（TOOL_CALL/READ_TOOL/WRITE_TOOL/SEMANTIC_QUERY 必填）。
	ToolKey string `json:"tool_key,omitempty"`
	// EffectClass 是副作用分级（第十四篇 §4.3）；写节点必须为 EXTERNAL_WRITE。
	EffectClass string `json:"effect_class,omitempty"`
	// Params 是节点参数（执行前经 tools.ValidateArgs 校验）。
	Params json.RawMessage `json:"params,omitempty"`
	// Condition 仅 DECISION 节点使用。
	Condition *ConditionSpec `json:"condition,omitempty"`
	// EvidenceRefs 是该节点产出/引用的证据引用。
	EvidenceRefs []string `json:"evidence_refs,omitempty"`
}

// VersionLock 是计划绑定的版本集（ADR-001 决策 5 的八类版本并集子集）。
type VersionLock struct {
	OntologyVersion      string `json:"ontology_version,omitempty"`
	ToolBindingDigest    string `json:"tool_binding_digest,omitempty"`
	ActionContractDigest string `json:"action_contract_digest,omitempty"`
	PolicyVersion        string `json:"policy_version,omitempty"`
	ModelRoutingPolicy   string `json:"model_routing_policy,omitempty"`
}

// Plan 是执行计划 IR（第十四篇 §6.1 的 Go 视图）。
// 注意：Plan 是"候选计划"，未经 Harness Plan Compiler 编译不得进入执行状态（ADR-001 决策 1）。
type Plan struct {
	PlanID      ids.ID      `json:"plan_id"`
	TenantID    ids.ID      `json:"tenant_id"`
	IntentID    ids.ID      `json:"intent_id"`
	Domain      string      `json:"domain"`
	Nodes       []Node      `json:"nodes"`
	VersionLock VersionLock `json:"version_lock,omitempty"`
}

// ValidatePlan 执行候选计划的结构校验（第十四篇 §6.4 计划不变量 / §9.1 计划校验）。
//
// 校验项：节点 ID 唯一、依赖指向存在节点、全图无环（报出环上节点）、
// 起止节点存在、写节点必须声明 EXTERNAL_WRITE 且有 APPROVAL_GATE 前驱。
//
// 注意：本函数只做结构级校验；类型检查、数据流校验、策略检查与版本绑定归
// Harness Plan Compiler（ADR-001 决策 2），不在此实现。
func ValidatePlan(p Plan) error {
	if len(p.Nodes) == 0 {
		return errors.E(errors.CodeValidationFailed, "plan: 计划至少需要一个节点")
	}
	byID := make(map[string]Node, len(p.Nodes))
	for _, n := range p.Nodes {
		if n.ID == "" {
			return errors.E(errors.CodeValidationFailed, "plan: 节点 ID 不能为空")
		}
		if !n.Kind.Valid() {
			return errors.Ef(errors.CodeValidationFailed,
				"plan: 节点 %s 的 kind=%q 未登记（ADR-001 §6 基线 13 类）", n.ID, n.Kind)
		}
		if _, dup := byID[n.ID]; dup {
			return errors.Ef(errors.CodeValidationFailed, "plan: 节点 ID 重复：%s", n.ID)
		}
		byID[n.ID] = n
	}

	// 依赖存在性 + 自环。
	for _, n := range p.Nodes {
		for _, d := range n.Deps {
			if d == n.ID {
				return errors.Ef(errors.CodeValidationFailed, "plan: 节点 %s 存在自环依赖", n.ID)
			}
			if _, ok := byID[d]; !ok {
				return errors.Ef(errors.CodeValidationFailed, "plan: 节点 %s 的依赖 %q 不存在", n.ID, d)
			}
		}
	}

	// 起止节点。
	if !hasKind(p.Nodes, NodeStart) {
		return errors.E(errors.CodeValidationFailed, "plan: 缺少 START 节点")
	}
	if !hasKind(p.Nodes, NodeEnd) {
		return errors.E(errors.CodeValidationFailed, "plan: 缺少 END 节点")
	}

	// 环检测（报出环上节点）。
	if cyc := findCycle(p); len(cyc) > 0 {
		return errors.Ef(errors.CodeValidationFailed, "plan: 依赖图存在环，环上节点=%v", cyc)
	}

	// 写节点安全门（第十四篇 §6.4：未完成必需数据验证不允许提交；
	// §12.2 Approval Gate：高风险动作必须有 APPROVAL_GATE 前驱）。
	for _, n := range p.Nodes {
		if n.Kind != NodeWriteTool && n.EffectClass != EffectExternalWrite {
			continue
		}
		if n.EffectClass != EffectExternalWrite {
			return errors.Ef(errors.CodeValidationFailed,
				"plan: 写节点 %s 必须声明 effect_class=EXTERNAL_WRITE（实际 %q）", n.ID, n.EffectClass)
		}
		if !hasApprovalAncestor(n, byID) {
			return errors.Ef(errors.CodeValidationFailed,
				"plan: 写节点 %s 缺少 APPROVAL_GATE 前驱（第十四篇 §12.2）", n.ID)
		}
	}
	return nil
}

// MaxDelegationDepth 是委派深度策略上限（第八篇 §17.2，默认 3，可按租户收紧）。
const MaxDelegationDepth = 3

// CheckDelegationDepth 校验委派深度（第八篇 §17.2）。
// 超限按权限拒绝处理（§17.2 第 3 条：返回 PERMISSION_DENIED 并记审计），
// 不允许"先收下再报错"的延迟处置。
func CheckDelegationDepth(depth int) error {
	if depth < 0 {
		return errors.Ef(errors.CodeValidationFailed, "agent: 委派深度不能为负：%d", depth)
	}
	if depth > MaxDelegationDepth {
		return errors.Ef(errors.CodePermissionDenied,
			"agent: 委派深度 %d 超过策略上限 %d（第八篇 §17.2）", depth, MaxDelegationDepth)
	}
	return nil
}

func hasKind(nodes []Node, kind NodeKind) bool {
	for _, n := range nodes {
		if n.Kind == kind {
			return true
		}
	}
	return false
}

// hasApprovalAncestor 沿依赖反向搜索，判断写节点是否存在 APPROVAL_GATE 前驱。
func hasApprovalAncestor(n Node, byID map[string]Node) bool {
	seen := map[string]bool{}
	stack := append([]string{}, n.Deps...)
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[cur] {
			continue
		}
		seen[cur] = true
		anc, ok := byID[cur]
		if !ok {
			continue
		}
		if anc.Kind == NodeApprovalGate {
			return true
		}
		stack = append(stack, anc.Deps...)
	}
	return false
}

// findCycle 用三色 DFS 检测环，返回环上的节点序列（无环返回 nil）。
func findCycle(p Plan) []string {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(p.Nodes))
	deps := make(map[string][]string, len(p.Nodes))
	for _, n := range p.Nodes {
		deps[n.ID] = n.Deps
	}
	// 固定遍历顺序，保证结果确定。
	ids := make([]string, 0, len(p.Nodes))
	for _, n := range p.Nodes {
		ids = append(ids, n.ID)
	}
	sort.Strings(ids)

	var stack []string
	var cycle []string
	var dfs func(id string) bool
	dfs = func(id string) bool {
		color[id] = gray
		stack = append(stack, id)
		for _, d := range deps[id] {
			switch color[d] {
			case white:
				if dfs(d) {
					return true
				}
			case gray:
				// 回边：截取环。
				for i := len(stack) - 1; i >= 0; i-- {
					if stack[i] == d {
						cycle = append([]string{}, stack[i:]...)
						return true
					}
				}
				cycle = []string{d}
				return true
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
		return false
	}
	for _, id := range ids {
		if color[id] == white {
			if dfs(id) {
				return cycle
			}
		}
	}
	return nil
}

// String 便于调试输出。
func (p Plan) String() string {
	return fmt.Sprintf("Plan{id=%s domain=%s nodes=%d}", p.PlanID, p.Domain, len(p.Nodes))
}
