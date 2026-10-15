package agent

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"odap/internal/platform/errors"
	"odap/internal/tools"
)

// 断言类型（第十五篇评估契约 / 第十四篇 §13.4 部分成功报告的断言子集）。
const (
	AssertNotEmpty    = "NOT_EMPTY"
	AssertEquals      = "EQUALS"
	AssertContains    = "CONTAINS"
	AssertJSONSchema  = "JSON_SCHEMA"
	AssertFieldExists = "FIELD_EXISTS"
	AssertRange       = "RANGE"
)

// Assertion 是一条输出断言。
//
// Target 支持点分路径（如 "result.status"）；空字符串或 "$" 表示整个输出。
type Assertion struct {
	Kind     string          `json:"kind"`
	Target   string          `json:"target,omitempty"`
	Expected json.RawMessage `json:"expected,omitempty"`
}

// Failure 是一条断言失败。
type Failure struct {
	Assertion string `json:"assertion"`
	Message   string `json:"message"`
}

// VerifyReport 是断言聚合报告。
type VerifyReport struct {
	Passed   bool      `json:"passed"`
	Failures []Failure `json:"failures,omitempty"`
}

// Verify 校验输出是否满足全部断言。失败聚合到 VerifyReport.Failures，
// 不短路（一次给出全部失败，便于诊断）。
func Verify(output json.RawMessage, assertions []Assertion) (VerifyReport, error) {
	var doc any
	if len(output) > 0 {
		if err := json.Unmarshal(output, &doc); err != nil {
			return VerifyReport{}, errors.Wrap(err, errors.CodeModelOutputInvalid, "agent: 待校验输出非合法 JSON")
		}
	}
	report := VerifyReport{Passed: true}
	for _, a := range assertions {
		if msg := evalAssertion(doc, a); msg != "" {
			report.Failures = append(report.Failures, Failure{Assertion: describe(a), Message: msg})
		}
	}
	report.Passed = len(report.Failures) == 0
	return report, nil
}

func describe(a Assertion) string {
	target := a.Target
	if target == "" {
		target = "$"
	}
	return a.Kind + ":" + target
}

// evalAssertion 返回空串表示通过，否则返回失败原因。
func evalAssertion(doc any, a Assertion) string {
	switch a.Kind {
	case AssertNotEmpty:
		v, ok := resolveTarget(doc, a.Target)
		if !ok {
			return "目标路径不存在"
		}
		if isEmptyValue(v) {
			return "值为空"
		}
		return ""
	case AssertEquals:
		v, ok := resolveTarget(doc, a.Target)
		if !ok {
			return "目标路径不存在"
		}
		want, err := decodeExpected(a.Expected)
		if err != nil {
			return "期望值非合法 JSON：" + err.Error()
		}
		if !reflect.DeepEqual(normalize(v), normalize(want)) {
			return fmt.Sprintf("值不相等：got %v, want %v", v, want)
		}
		return ""
	case AssertContains:
		v, ok := resolveTarget(doc, a.Target)
		if !ok {
			return "目标路径不存在"
		}
		return checkContains(v, a.Expected)
	case AssertJSONSchema:
		v, ok := resolveTarget(doc, a.Target)
		if !ok {
			return "目标路径不存在"
		}
		obj, ok := v.(map[string]any)
		if !ok {
			return fmt.Sprintf("目标不是 JSON 对象：%T", v)
		}
		if len(a.Expected) == 0 {
			return "缺少 JSON Schema"
		}
		if err := tools.ValidateArgs(a.Expected, obj); err != nil {
			return err.Error()
		}
		return ""
	case AssertFieldExists:
		if _, ok := resolveTarget(doc, a.Target); !ok {
			return "字段不存在"
		}
		return ""
	case AssertRange:
		v, ok := resolveTarget(doc, a.Target)
		if !ok {
			return "目标路径不存在"
		}
		f, ok := toFloat(v)
		if !ok {
			return fmt.Sprintf("目标不是数值：%v", v)
		}
		var bounds struct {
			Min *float64 `json:"min"`
			Max *float64 `json:"max"`
		}
		if err := json.Unmarshal(a.Expected, &bounds); err != nil {
			return "RANGE 期望值需为 {\"min\":..,\"max\":..}：" + err.Error()
		}
		if bounds.Min != nil && f < *bounds.Min {
			return fmt.Sprintf("值 %v 低于下限 %v", f, *bounds.Min)
		}
		if bounds.Max != nil && f > *bounds.Max {
			return fmt.Sprintf("值 %v 高于上限 %v", f, *bounds.Max)
		}
		return ""
	default:
		return "未登记的断言类型：" + a.Kind
	}
}

// checkContains 校验字符串包含子串或数组包含元素。
func checkContains(v any, expected json.RawMessage) string {
	want, err := decodeExpected(expected)
	if err != nil {
		return "期望值非合法 JSON：" + err.Error()
	}
	switch actual := v.(type) {
	case string:
		sub, ok := want.(string)
		if !ok {
			return "字符串包含断言的期望值必须是字符串"
		}
		if !strings.Contains(actual, sub) {
			return fmt.Sprintf("字符串 %q 不包含 %q", actual, sub)
		}
		return ""
	case []any:
		for _, item := range actual {
			if reflect.DeepEqual(normalize(item), normalize(want)) {
				return ""
			}
		}
		return fmt.Sprintf("数组中不含期望元素 %v", want)
	default:
		return fmt.Sprintf("不支持包含断言的类型：%T", v)
	}
}

// resolveTarget 按点分路径解析；空路径或 "$" 返回整个文档。
func resolveTarget(doc any, path string) (any, bool) {
	if path == "" || path == "$" {
		if doc == nil {
			return nil, false
		}
		return doc, true
	}
	cur := doc
	for _, seg := range strings.Split(path, ".") {
		if seg == "" {
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok := m[seg]
		if !ok {
			return nil, false
		}
		cur = v
	}
	if cur == nil {
		return nil, false
	}
	return cur, true
}

// decodeExpected 解码期望值。
func decodeExpected(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("期望值为空")
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// isEmptyValue 报告值是否为空（nil / 空字符串 / 空数组 / 空对象）。
func isEmptyValue(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	default:
		return false
	}
}

// normalize 统一数值类型（json 数字为 float64），便于 DeepEqual 比较。
func normalize(v any) any {
	switch t := v.(type) {
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = normalize(t[i])
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = normalize(vv)
		}
		return out
	default:
		return v
	}
}

// toFloat 把 JSON 数字或 Go 数值转为 float64。
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}
