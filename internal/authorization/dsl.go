// Package authorization 实现统一授权引擎（第三篇）：
// JSON DSL V1 策略校验、决策求值与决策持久化。
// 安全基调：fail-closed——任何校验失败、求值异常或范围越界都不得产生 ALLOW。
package authorization

import (
	"bytes"
	"encoding/json"
	"fmt"

	"odap/internal/platform/errors"
)

// 策略语言版本与效果白名单（第三篇 §6.3：JSON DSL 子集锁定，禁任意代码）。
const (
	// PolicyVersion 是 JSON DSL V1 的版本号（同时兼容 "1.0" 书写形式）。
	PolicyVersion = "1"

	EffectAllow           = "ALLOW"
	EffectDeny            = "DENY"
	EffectRequireApproval = "REQUIRE_APPROVAL"
)

// 策略规模上限（发布校验期执行，防止病态文档耗尽运行期资源）。
const (
	MaxRulesPerPolicy    = 100
	MaxConditionsPerRule = 20
)

// 比较算子白名单（task 规范锁定，不含算术/函数/自定义表达式）。
const (
	OpEq       = "eq"
	OpNe       = "ne"
	OpIn       = "in"
	OpNotIn    = "not_in"
	OpGte      = "gte"
	OpLte      = "lte"
	OpExists   = "exists"
	OpContains = "contains"
)

// allowedOps 是算子白名单集合。
var allowedOps = map[string]bool{
	OpEq: true, OpNe: true, OpIn: true, OpNotIn: true,
	OpGte: true, OpLte: true, OpExists: true, OpContains: true,
}

// allowedEffects 是效果白名单集合。
var allowedEffects = map[string]bool{
	EffectAllow: true, EffectDeny: true, EffectRequireApproval: true,
}

// allowedConditionFields 是条件字段白名单（第三篇 §6.1 六类属性的 V1 子集）。
// 运行期条件值只从 Request.Attributes 取；未登记字段在发布校验期直接拒绝。
var allowedConditionFields = map[string]string{
	// 主体属性
	"subject_id": "主体属性", "subject_type": "主体属性",
	"org_id": "主体属性", "role_keys": "主体属性",
	// 资源属性
	"resource_id": "资源属性", "resource_type": "资源属性",
	"resource_owner_id": "资源属性", "owner_org_id": "资源属性",
	"data_sensitivity": "资源属性", "amount": "资源属性", "currency": "资源属性",
	"resource_ref": "资源属性", "assignee_id": "资源属性",
	// 操作属性
	"action": "操作属性",
	// 环境属性
	"environment": "环境属性", "network_zone": "环境属性", "auth_strength": "环境属性",
	// 任务属性
	"agent_version_id": "任务属性", "run_id": "任务属性",
	// 证据属性
	"ontology_version": "证据属性", "policy_version": "证据属性",
}

// Policy 是 JSON DSL V1 策略文档（白名单字段，未知字段拒绝）。
type Policy struct {
	Version string `json:"version"`
	Rules   []Rule `json:"rules"`
}

// Rule 是单条可判定规则。Priority 越大越先评估；
// 显式 DENY 优先于 ALLOW 的合成规则在引擎层实现（§7.3）。
type Rule struct {
	ID           string      `json:"id"`
	Action       string      `json:"action"`
	ResourceType string      `json:"resource_type"`
	Effect       string      `json:"effect"`
	Conditions   []Condition `json:"conditions,omitempty"`
	Priority     int         `json:"priority"`
	ReasonCode   string      `json:"reason_code"`
	RiskLevel    string      `json:"risk_level,omitempty"`
}

// Condition 是原子条件（字段 + 算子 + 字面量值），全部为 AND 组合。
type Condition struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value any    `json:"value,omitempty"`
}

// Validate 校验策略 JSON（发布校验期调用）。
// 白名单校验：未知字段 / 未知算子 / 未知 effect / 未登记条件字段 /
// 规模超限 / 规则 ID 重复一律拒绝，返回 CodeAUTHZPolicyInvalid。
func Validate(policyJSON []byte) error {
	dec := json.NewDecoder(bytes.NewReader(policyJSON))
	dec.DisallowUnknownFields() // 字段白名单：未知字段直接拒绝
	var p Policy
	if err := dec.Decode(&p); err != nil {
		return errors.Ef(errors.CodeAUTHZPolicyInvalid, "策略 JSON 解析失败: %v", err)
	}
	// 拒绝尾部多余内容（如两个 JSON 对象拼接）。
	if dec.More() {
		return errors.E(errors.CodeAUTHZPolicyInvalid, "策略 JSON 含有尾部多余内容")
	}
	if p.Version != PolicyVersion && p.Version != PolicyVersion+".0" {
		return errors.Ef(errors.CodeAUTHZPolicyInvalid, "不支持的策略版本 %q（期望 %s）", p.Version, PolicyVersion)
	}
	if len(p.Rules) > MaxRulesPerPolicy {
		return errors.Ef(errors.CodeAUTHZPolicyInvalid, "规则数 %d 超过上限 %d", len(p.Rules), MaxRulesPerPolicy)
	}

	seenIDs := make(map[string]bool, len(p.Rules))
	for i, rule := range p.Rules {
		if err := validateRule(rule, i, seenIDs); err != nil {
			return err
		}
	}
	return nil
}

// ParsePolicy 校验并解析策略文档（引擎加载路径复用）。
func ParsePolicy(policyJSON []byte) (Policy, error) {
	if err := Validate(policyJSON); err != nil {
		return Policy{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(policyJSON))
	dec.DisallowUnknownFields()
	var p Policy
	if err := dec.Decode(&p); err != nil {
		// Validate 已通过，理论上不可达。
		return Policy{}, errors.Wrap(err, errors.CodeAUTHZPolicyInvalid, "策略 JSON 解析失败")
	}
	return p, nil
}

func validateRule(rule Rule, idx int, seenIDs map[string]bool) error {
	prefix := fmt.Sprintf("规则[%d]", idx)
	if rule.ID == "" {
		return errors.Ef(errors.CodeAUTHZPolicyInvalid, "%s 缺少 id", prefix)
	}
	if seenIDs[rule.ID] {
		return errors.Ef(errors.CodeAUTHZPolicyInvalid, "%s id %q 重复", prefix, rule.ID)
	}
	seenIDs[rule.ID] = true
	if rule.Action == "" {
		return errors.Ef(errors.CodeAUTHZPolicyInvalid, "%s(id=%s) 缺少 action", prefix, rule.ID)
	}
	if rule.ResourceType == "" {
		return errors.Ef(errors.CodeAUTHZPolicyInvalid, "%s(id=%s) 缺少 resource_type", prefix, rule.ID)
	}
	if !allowedEffects[rule.Effect] {
		return errors.Ef(errors.CodeAUTHZPolicyInvalid, "%s(id=%s) 非法 effect %q", prefix, rule.ID, rule.Effect)
	}
	if rule.ReasonCode == "" {
		return errors.Ef(errors.CodeAUTHZPolicyInvalid, "%s(id=%s) 缺少 reason_code", prefix, rule.ID)
	}
	if len(rule.Conditions) > MaxConditionsPerRule {
		return errors.Ef(errors.CodeAUTHZPolicyInvalid, "%s(id=%s) 条件数 %d 超过上限 %d",
			prefix, rule.ID, len(rule.Conditions), MaxConditionsPerRule)
	}
	for j, cond := range rule.Conditions {
		if err := validateCondition(cond, fmt.Sprintf("%s(id=%s).条件[%d]", prefix, rule.ID, j)); err != nil {
			return err
		}
	}
	return nil
}

func validateCondition(cond Condition, where string) error {
	if _, ok := allowedConditionFields[cond.Field]; !ok {
		return errors.Ef(errors.CodeAUTHZPolicyInvalid, "%s 引用未登记字段 %q", where, cond.Field)
	}
	if !allowedOps[cond.Op] {
		return errors.Ef(errors.CodeAUTHZPolicyInvalid, "%s 使用未登记算子 %q", where, cond.Op)
	}
	if cond.Op == OpExists {
		if cond.Value != nil {
			return errors.Ef(errors.CodeAUTHZPolicyInvalid, "%s exists 算子不得携带 value", where)
		}
		return nil
	}
	if cond.Value == nil {
		return errors.Ef(errors.CodeAUTHZPolicyInvalid, "%s 算子 %q 缺少 value", where, cond.Op)
	}
	switch cond.Op {
	case OpIn, OpNotIn:
		if _, ok := cond.Value.([]any); !ok {
			return errors.Ef(errors.CodeAUTHZPolicyInvalid, "%s 算子 %q 的 value 必须是数组", where, cond.Op)
		}
	case OpContains:
		switch cond.Value.(type) {
		case string, []any:
		default:
			return errors.Ef(errors.CodeAUTHZPolicyInvalid, "%s 算子 %q 的 value 必须是字符串或数组", where, cond.Op)
		}
	case OpGte, OpLte:
		if _, ok := toFloat(cond.Value); ok {
			break
		}
		if _, ok := cond.Value.(string); !ok {
			return errors.Ef(errors.CodeAUTHZPolicyInvalid, "%s 算子 %q 的 value 必须是数值或字符串", where, cond.Op)
		}
	}
	return nil
}
