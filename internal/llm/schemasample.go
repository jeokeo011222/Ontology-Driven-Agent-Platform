package llm

import (
	"encoding/json"
	"math"
)

// 本文件实现"依据 JSON Schema 合成最小合法实例"。
//
// 存在意义：离线演示与离线评估需要一个**能产出结构合法输出**的端点。若确定性
// 模拟端点只能返回纯文本，那么第九篇 §9.1 的结构化输出回路（generate-then-validate
// 与受限重生成）以及下游本体验证都无法被真正走通，演示与测试会退化为"只调用了
// 一次接口"，而不是"验证了链路"。
//
// 覆盖范围刻意与 internal/tools 的校验子集保持一致
// （type / required / properties / enum / items / maximum / minimum，另接受
// default 与 const 作为显式取值来源）。超出该子集时返回 ok=false，
// 由调用方决定回落策略 —— 绝不"猜一个可能不合法"的值。
//
// 注意：合成结果只保证结构合法，不保证业务语义正确。这正是平台的原则：
// 模型输出默认是候选结果，而不是经过认证的业务事实（第九篇 §2.1 原则 7）。

// schemaSample 合成一个满足 raw schema 的最小实例；ok=false 表示超出可合成子集。
func schemaSample(raw json.RawMessage) (any, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var s struct {
		Type       string                     `json:"type"`
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
		Enum       []any                      `json:"enum"`
		Items      json.RawMessage            `json:"items"`
		Maximum    *float64                   `json:"maximum"`
		Minimum    *float64                   `json:"minimum"`
		Const      json.RawMessage            `json:"const"`
		Default    json.RawMessage            `json:"default"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, false
	}

	// 取值优先级：const > default > enum 首项 > 按 type 合成。
	if len(s.Const) > 0 {
		var v any
		if err := json.Unmarshal(s.Const, &v); err != nil {
			return nil, false
		}
		return v, true
	}
	if len(s.Default) > 0 {
		var v any
		if err := json.Unmarshal(s.Default, &v); err != nil {
			return nil, false
		}
		return v, true
	}
	if len(s.Enum) > 0 {
		return s.Enum[0], true
	}

	switch s.Type {
	case "object":
		out := make(map[string]any, len(s.Required))
		for _, name := range s.Required {
			prop, ok := s.Properties[name]
			if !ok {
				// required 声明了 properties 未定义的字段：无法合成。
				return nil, false
			}
			v, ok := schemaSample(prop)
			if !ok {
				return nil, false
			}
			out[name] = v
		}
		return out, true
	case "array":
		// 只合成 1 个元素：子集不支持 minItems，单元素既确定又足以通过校验。
		v, ok := schemaSample(s.Items)
		if !ok {
			return nil, false
		}
		return []any{v}, true
	case "string":
		return "sample", true
	case "integer":
		return sampleNumber(s.Minimum, s.Maximum, true)
	case "number":
		return sampleNumber(s.Minimum, s.Maximum, false)
	case "boolean":
		return true, true
	case "null":
		return nil, true
	default:
		// 无 type 也无 const/default/enum：无法判定形状，不猜。
		return nil, false
	}
}

// sampleNumber 合成了一个落在 [minimum, maximum] 内的数值。
// 优先取 minimum（贴近下界，便于观察边界行为）；无下界时取 0，
// 若 0 超上界则取上界。若下界本身超上界，说明 schema 自相矛盾，返回失败。
func sampleNumber(min, max *float64, integer bool) (any, bool) {
	if min != nil && max != nil && *min > *max {
		return nil, false
	}
	v := 0.0
	if min != nil {
		v = *min
	} else if max != nil && *max < 0 {
		v = *max
	}
	if integer {
		v = math.Ceil(v)
		if max != nil && v > *max {
			v = math.Floor(*max)
		}
		return int64(v), true
	}
	return v, true
}
