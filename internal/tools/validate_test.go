package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"odap/internal/platform/errors"
)

func mustJSON(t *testing.T, s string) json.RawMessage {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("bad test JSON: %v", err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func decodeArgs(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("bad test args: %v", err)
	}
	return m
}

// TestValidateArgsTable 覆盖合法与非法入参（类型/必填/枚举/嵌套/数值边界）。
func TestValidateArgsTable(t *testing.T) {
	schema := mustJSON(t, `{
		"type": "object",
		"required": ["amount", "currency"],
		"properties": {
			"amount":   {"type": "number", "minimum": 0.01, "maximum": 1000000},
			"currency": {"type": "string", "enum": ["CNY", "USD"]},
			"memo":     {"type": "string"},
			"lines": {
				"type": "array",
				"items": {
					"type": "object",
					"required": ["sku", "qty"],
					"properties": {
						"sku": {"type": "string"},
						"qty": {"type": "integer", "minimum": 1}
					}
				}
			}
		}
	}`)

	ok := []struct {
		name string
		args string
	}{
		{"最小合法", `{"amount": 12.5, "currency": "CNY"}`},
		{"含可选字段", `{"amount": 1, "currency": "USD", "memo": ""}`},
		{"嵌套数组合法", `{"amount": 5, "currency": "CNY", "lines": [{"sku": "A", "qty": 2}]}`},
		{"整数可写作 2.0", `{"amount": 5, "currency": "CNY", "lines": [{"sku": "A", "qty": 2.0}]}`},
	}
	for _, c := range ok {
		t.Run("OK/"+c.name, func(t *testing.T) {
			if err := ValidateArgs(schema, decodeArgs(t, c.args)); err != nil {
				t.Fatalf("expected valid, got: %v", err)
			}
		})
	}

	bad := []struct {
		name    string
		args    string
		wantSub string // 期望出现在错误消息中的字段路径或关键字
	}{
		{"缺必填 amount", `{"currency": "CNY"}`, "amount"},
		{"类型错 amount 为字符串", `{"amount": "12", "currency": "CNY"}`, "type mismatch"},
		{"枚举越界", `{"amount": 1, "currency": "EUR"}`, "enum"},
		{"低于最小值", `{"amount": 0.001, "currency": "CNY"}`, "minimum"},
		{"超过最大值", `{"amount": 2000000, "currency": "CNY"}`, "maximum"},
		{"嵌套缺必填 sku", `{"amount": 1, "currency": "CNY", "lines": [{"qty": 1}]}`, "sku"},
		{"嵌套 qty 非整数", `{"amount": 1, "currency": "CNY", "lines": [{"sku": "A", "qty": 1.5}]}`, "integer"},
		{"嵌套数组下标定位", `{"amount": 1, "currency": "CNY", "lines": [{"sku":"A","qty":1},{"sku":"B","qty":0}]}`, "[1]"},
	}
	for _, c := range bad {
		t.Run("BAD/"+c.name, func(t *testing.T) {
			err := ValidateArgs(schema, decodeArgs(t, c.args))
			if err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if code := errors.As(err).Code; code != errors.CodeValidationFailed {
				t.Fatalf("error code = %s, want %s", code, errors.CodeValidationFailed)
			}
			if !contains(err.Error(), c.wantSub) {
				t.Fatalf("error %q does not mention %q", err.Error(), c.wantSub)
			}
		})
	}
}

// TestValidateArgsFailClosedOnUnknownKeyword 未登记的校验关键字必须拒绝，
// 而不是被静默忽略（否则"写了约束"与"约束生效"会脱节）。
func TestValidateArgsFailClosedOnUnknownKeyword(t *testing.T) {
	schema := mustJSON(t, `{"type":"string","pattern":"^[A-Z]+$"}`)
	err := ValidateArgs(schema, map[string]any{"x": "abc"})
	if err == nil {
		t.Fatal("schema with unsupported keyword accepted, want error")
	}
	if code := errors.As(err).Code; code != errors.CodeValidationFailed {
		t.Fatalf("error code = %s, want %s", code, errors.CodeValidationFailed)
	}
}

// TestValidateArgsBenignKeywordsIgnored 元数据关键字被安全忽略。
func TestValidateArgsBenignKeywordsIgnored(t *testing.T) {
	schema := mustJSON(t, `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"title": "Amount",
		"description": "金额",
		"type": "number"
	}`)
	if err := ValidateArgs(schema, map[string]any{"": 1}); err != nil {
		// 根 schema 是 number，但 args 是 object：应报 type mismatch。
		if code := errors.As(err).Code; code != errors.CodeValidationFailed {
			t.Fatalf("error code = %s", code)
		}
	}
	// 顶层为 object 时的合法路径。
	schema2 := mustJSON(t, `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"title": "Args",
		"type": "object",
		"properties": {"n": {"type": "number", "description": "数量"}}
	}`)
	if err := ValidateArgs(schema2, decodeArgs(t, `{"n": 3}`)); err != nil {
		t.Fatalf("benign keywords caused rejection: %v", err)
	}
}

// TestValidateArgsEnforcesAdditionalPropertiesFalse 验证 additionalProperties=false
// 真的生效，而不是被当作"无害元数据"忽略。
//
// 为什么这条不能松：平台下发给模型的结构化输出 Schema（本体/Agent/工具/工作流）
// 普遍声明它，而下游落库/装载路径对同一份 JSON 用 DisallowUnknownFields 严格解码。
// 若校验层宽容接受多余字段，多余字段会通过校验、再在下游以"结构与约定不符"失败 ——
// 模型既看不到"多余字段"这个真实原因，也就无法在受限重生成里修正。
func TestValidateArgsEnforcesAdditionalPropertiesFalse(t *testing.T) {
	schema := mustJSON(t, `{
		"type": "object", "additionalProperties": false,
		"required": ["a"],
		"properties": {
			"a": {"type": "string"},
			"nest": {"type": "object", "additionalProperties": false,
				"properties": {"b": {"type": "string"}}}
		}
	}`)
	if err := ValidateArgs(schema, decodeArgs(t, `{"a":"x","nest":{"b":"y"}}`)); err != nil {
		t.Fatalf("合规入参不应被拒: %v", err)
	}

	err := ValidateArgs(schema, decodeArgs(t, `{"a":"x","label":"多出来的"}`))
	if err == nil || errors.As(err).Code != errors.CodeValidationFailed {
		t.Fatalf("顶层多余字段应被拒绝，实际 %v", err)
	}

	// 多余字段必须一次报全：重生成次数有上限，一次只报一个会把"修 n 处"
	// 变成"n 轮重生成"，超出上限后模型就没有机会了。
	err = ValidateArgs(schema, decodeArgs(t, `{"a":"x","label":"l","comment":"c"}`))
	if err == nil || !strings.Contains(err.Error(), "label") || !strings.Contains(err.Error(), "comment") {
		t.Fatalf("多余字段应一次报全，实际 %v", err)
	}

	if err := ValidateArgs(schema, decodeArgs(t, `{"a":"x","nest":{"b":"y","extra":1}}`)); err == nil {
		t.Fatal("嵌套节点的多余字段应被拒绝")
	}

	lenient := mustJSON(t, `{"type":"object","additionalProperties":true,
		"properties":{"a":{"type":"string"}}}`)
	if err := ValidateArgs(lenient, decodeArgs(t, `{"a":"x","b":1}`)); err != nil {
		t.Fatalf("additionalProperties=true 等价于无约束，不应拒绝: %v", err)
	}

	// 声明为 schema 对象的形态本子集未实现：fail-closed 拒绝，而不是默默放行。
	unsupported := mustJSON(t, `{"type":"object","additionalProperties":{"type":"string"}}`)
	if err := ValidateSchema(unsupported); err == nil {
		t.Fatal("additionalProperties 的 schema 对象形态应被拒绝")
	}
}

// TestValidateArgsMaxItems 验证 maxItems 这个"输出规模预算"关键字真的生效。
//
// 为什么这条不能松：AI 自动生成类接口把 "类 / 关系 / 枚举 / 形状 / 实例" 各自的
// 数量上限写进 schema 的 maxItems，让"模型写超了"成为**可修复的 schema 违规**
// （诊断会回灌给模型）。若这一层不生效，模型会一路写下去撞上输出上限被截断，
// 而截断是确定性失败（见 llm.invalidJSONError），无法在受限重生成里修复。
func TestValidateArgsMaxItems(t *testing.T) {
	schema := mustJSON(t, `{
		"type": "object",
		"properties": {
			"classes": {
				"type": "array",
				"maxItems": 2,
				"items": {"type": "object", "required": ["name"], "properties": {"name": {"type": "string"}}}
			}
		}
	}`)

	// 受理：恰好等于上限仍合法（maxItems 是"上限"不是"必须小于"）。
	if err := ValidateArgs(schema, decodeArgs(t, `{"classes":[{"name":"A"},{"name":"B"}]}`)); err != nil {
		t.Fatalf("等于上限的数组应被接受: %v", err)
	}
	if err := ValidateArgs(schema, decodeArgs(t, `{"classes":[{"name":"A"}]}`)); err != nil {
		t.Fatalf("未达上限的数组应被接受: %v", err)
	}

	// 拒绝：超过上限，且错误消息要能定位（带路径 + 上限 + 实际条数）。
	err := ValidateArgs(schema, decodeArgs(t, `{"classes":[{"name":"A"},{"name":"B"},{"name":"C"}]}`))
	if err == nil {
		t.Fatal("超过 maxItems 的数组应被拒绝")
	}
	if code := errors.As(err).Code; code != errors.CodeValidationFailed {
		t.Fatalf("error code = %s, want %s", code, errors.CodeValidationFailed)
	}
	msg := err.Error()
	if !strings.Contains(msg, "classes") || !strings.Contains(msg, "max 2") || !strings.Contains(msg, "got 3") {
		t.Fatalf("错误消息应带路径与数量（实际 %q）", msg)
	}

	// fail-closed：maxItems 写在非数组上是"作者以为在限制、实际不生效"，必须拒绝。
	if err := ValidateSchema(mustJSON(t, `{"type":"string","maxItems":2}`)); err == nil {
		t.Fatal("maxItems 写在非数组上应被拒绝")
	}
	// 负值是无意义声明，同样拒绝。
	if err := ValidateSchema(mustJSON(t, `{"type":"array","maxItems":-1,"items":{"type":"string"}}`)); err == nil {
		t.Fatal("负数 maxItems 应被拒绝")
	}
}

// TestValidateArgsUnsupportedType 未登记的 type 取值必须拒绝。
func TestValidateArgsUnsupportedType(t *testing.T) {
	schema := mustJSON(t, `{"type":"object","properties":{"x":{"type":"tuple"}}}`)
	err := ValidateArgs(schema, decodeArgs(t, `{"x": [1,2]}`))
	if err == nil {
		t.Fatal("unsupported type accepted, want error")
	}
	if !contains(err.Error(), "unsupported type") {
		t.Fatalf("error %q does not mention unsupported type", err.Error())
	}
}

// TestValidateArgsEmptySchemaAndNilArgs 空 Schema 与 nil 入参的边界。
func TestValidateArgsEmptySchemaAndNilArgs(t *testing.T) {
	if err := ValidateArgs(nil, nil); err == nil {
		t.Fatal("empty schema accepted, want error")
	}
	schema := mustJSON(t, `{"type":"object"}`)
	if err := ValidateArgs(schema, nil); err != nil {
		t.Fatalf("nil args with object schema should be valid: %v", err)
	}
}

// TestPathOf 校验错误可提取字段路径（供 evidence/审计记录）。
func TestPathOf(t *testing.T) {
	schema := mustJSON(t, `{"type":"object","required":["a"],"properties":{"a":{"type":"string"}}}`)
	err := ValidateArgs(schema, decodeArgs(t, `{}`))
	if err := PathOf(err); err != "$" {
		t.Fatalf("PathOf = %q, want $", err)
	}
	if p := PathOf(nil); p != "" {
		t.Fatalf("PathOf(nil) = %q, want empty", p)
	}
	if p := PathOf(errors.E(errors.CodeNotFound, "x")); p != "" {
		t.Fatalf("PathOf(non-validation) = %q, want empty", p)
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
