package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"odap/internal/platform/errors"
)

// 本文件实现工具入参校验所需的 JSON Schema 最小子集：
// type / required / properties / enum / items / additionalProperties(false)
// / maximum / minimum / maxItems。
//
// 设计取向（fail-closed）：
//   - 出现**未登记**的校验关键字时直接拒绝，而不是静默忽略。静默忽略会让
//     作者以为已生效的约束实际上形同虚设（"写了密钥校验但没校验"）。
//   - 明确列入 benignKeywords 的元数据关键字会被安全忽略；即使忽略也不削弱
//     任何校验强度。
//   - 未知的 type 取值一律拒绝。
//
// additionalProperties=false 必须真的生效（不是 benign）：平台下发给模型的
// 结构化输出 Schema（如 ontologyManifestSchema）普遍声明它，而下游落库/装载
// 路径用 DisallowUnknownFields 严格解码同一份 JSON。若这一层宽容接受多余字段，
// 多余字段会一路通过校验、再在下游解码时以"结构与约定不符"失败 —— 契约说不允许、
// 校验层默默接受、下游才拒绝，模型既看不到"多余字段"这一真实原因，也就无从修正。
//
// 工具 Schema 由平台在插件发布流水线中校验（第四篇 §5），运行期只需保证
// "参数结构与已发布 Schema 一致"。
var benignKeywords = map[string]bool{
	"$schema":     true,
	"$id":         true,
	"title":       true,
	"description": true,
	"default":     true,
	"examples":    true,
	"format":      true,
	"$comment":    true,
}

var supportedKeywords = map[string]bool{
	"type":                 true,
	"required":             true,
	"properties":           true,
	"enum":                 true,
	"items":                true,
	"additionalProperties": true,
	"maximum":              true,
	"minimum":              true,
	"maxItems":             true,
}

// schemaNode 是受支持关键字的结构化视图。
type schemaNode struct {
	Type       string                     `json:"type"`
	Required   []string                   `json:"required"`
	Properties map[string]json.RawMessage `json:"properties"`
	Enum       []any                      `json:"enum"`
	Items      json.RawMessage            `json:"items"`
	Maximum    *float64                   `json:"maximum"`
	Minimum    *float64                   `json:"minimum"`

	// MaxItems 是数组长度上限（只对 type:"array" 有意义）。
	MaxItems *int `json:"maxItems"`

	// AdditionalProperties 只支持布尔形态：false = 禁止多余字段；true = 无约束
	// （与省略等价）。声明为 schema 对象的形态语义不明确，本子集不实现，直接拒绝。
	AdditionalProperties json.RawMessage `json:"additionalProperties"`
}

// ValidateArgs 用 schema 校验工具入参。失败返回 CodeValidationFailed，
// 消息中带有具体字段路径（如 $[0].amount），便于 Agent 自我修正。
func ValidateArgs(schema json.RawMessage, args map[string]any) error {
	if len(schema) == 0 {
		return errors.E(errors.CodeValidationFailed, "tools: empty tool input schema")
	}
	if args == nil {
		args = map[string]any{}
	}
	return validateValue("$", schema, args)
}

// ValidateSchema 只校验 schema 本身是否落在平台支持的子集内，不校验任何数据值。
//
// 用途：在发布/注入阶段就拦住"用了未登记关键字"的 schema。若拖到入参校验时才
// 发现，作者会以为约束已生效 —— 写了 anyOf/exclusiveMinimum 以为在生效，
// 而本子集从未实现它们，该字段实际处于"无约束"状态。
func ValidateSchema(schema json.RawMessage) error {
	if len(schema) == 0 {
		return errors.E(errors.CodeValidationFailed, "tools: empty schema")
	}
	return validateSchemaNode("$", schema)
}

// validateSchemaNode 递归检查 schema 每一层的关键字与 type 取值。
func validateSchemaNode(path string, rawSchema json.RawMessage) error {
	node, err := parseSchemaNode(path, rawSchema)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(node.Properties))
	for name := range node.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := validateSchemaNode(path+"."+name, node.Properties[name]); err != nil {
			return err
		}
	}
	if len(node.Items) > 0 {
		if err := validateSchemaNode(path+"[]", node.Items); err != nil {
			return err
		}
	}
	return nil
}

// parseSchemaNode 解析并校验"一层" schema：先做关键字白名单检查（fail-closed），
// 再反序列化为结构化视图。ValidateArgs 与 ValidateSchema 共用同一条路径，
// 避免"校验入参"与"校验 schema"两处对关键字集合的理解漂移。
func parseSchemaNode(path string, rawSchema json.RawMessage) (schemaNode, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(rawSchema, &keys); err != nil {
		return schemaNode{}, errors.Wrap(err, errors.CodeValidationFailed, "tools: malformed schema at "+path)
	}
	for k := range keys {
		if !supportedKeywords[k] && !benignKeywords[k] {
			return schemaNode{}, errors.Ef(errors.CodeValidationFailed,
				"tools: schema at %s uses unsupported keyword %q (refusing to ignore a validation keyword)",
				path, k)
		}
	}
	var node schemaNode
	if err := json.Unmarshal(rawSchema, &node); err != nil {
		return schemaNode{}, errors.Wrap(err, errors.CodeValidationFailed, "tools: malformed schema at "+path)
	}
	if node.Type != "" && !validType(node.Type) {
		return schemaNode{}, errors.Ef(errors.CodeValidationFailed,
			"tools: schema at %s declares unsupported type %q", path, node.Type)
	}
	if node.MaxItems != nil {
		// fail-closed：maxItems 写在非数组上就等于"作者以为在限制一个数组，实际它不生效"。
		// 另外 JSON Schema 要求 maxItems 是非负整数，负值是无意义声明，一并拒绝。
		if node.Type != "array" {
			return schemaNode{}, errors.Ef(errors.CodeValidationFailed,
				"tools: schema at %s declares maxItems but type is %q (maxItems only applies to arrays)", path, node.Type)
		}
		if *node.MaxItems < 0 {
			return schemaNode{}, errors.Ef(errors.CodeValidationFailed,
				"tools: schema at %s declares negative maxItems %d", path, *node.MaxItems)
		}
	}
	if len(node.AdditionalProperties) > 0 {
		switch strings.TrimSpace(string(node.AdditionalProperties)) {
		case "true", "false":
		default:
			return schemaNode{}, errors.Ef(errors.CodeValidationFailed,
				"tools: schema at %s declares additionalProperties in a form this subset does not implement (only boolean is supported)", path)
		}
	}
	return node, nil
}

// validateValue 递归校验一个 JSON 值；path 用于错误定位。
func validateValue(path string, rawSchema json.RawMessage, value any) error {
	node, err := parseSchemaNode(path, rawSchema)
	if err != nil {
		return err
	}

	if node.Type != "" {
		if err := checkType(path, node.Type, value); err != nil {
			return err
		}
	}

	// enum：与 JSON 语义一致地比较（数字按数值比较）。
	if len(node.Enum) > 0 {
		if !enumContains(node.Enum, value) {
			return errors.Ef(errors.CodeValidationFailed,
				"tools: %s value %v not in enum %v", path, value, node.Enum)
		}
	}

	// 数值边界。
	if node.Maximum != nil || node.Minimum != nil {
		f, ok := toFloat(value)
		if !ok {
			return errors.Ef(errors.CodeValidationFailed,
				"tools: %s has numeric bound but value %v is not a number", path, value)
		}
		if node.Maximum != nil && f > *node.Maximum {
			return errors.Ef(errors.CodeValidationFailed,
				"tools: %s value %v exceeds maximum %v", path, f, *node.Maximum)
		}
		if node.Minimum != nil && f < *node.Minimum {
			return errors.Ef(errors.CodeValidationFailed,
				"tools: %s value %v below minimum %v", path, f, *node.Minimum)
		}
	}

	// 对象：required + properties 递归。
	if obj, ok := value.(map[string]any); ok {
		for _, req := range node.Required {
			if _, present := obj[req]; !present {
				return errors.Ef(errors.CodeValidationFailed,
					"tools: %s missing required property %q", path, req)
			}
		}
		names := make([]string, 0, len(node.Properties))
		for name := range node.Properties {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			child, present := obj[name]
			if !present {
				continue
			}
			if err := validateValue(path+"."+name, node.Properties[name], child); err != nil {
				return err
			}
		}
		// additionalProperties=false：多余的键一次报全，而不是只报第一个。
		// 重生成次数有上限（默认 2 次），一次只报一个多余字段会把"修 n 处"变成
		// "n 轮重生成"，超出上限后模型就没有机会了。
		if strings.TrimSpace(string(node.AdditionalProperties)) == "false" {
			var extra []string
			for name := range obj {
				if _, declared := node.Properties[name]; !declared {
					extra = append(extra, name)
				}
			}
			if len(extra) > 0 {
				sort.Strings(extra)
				return errors.Ef(errors.CodeValidationFailed,
					"tools: %s has unexpected properties not allowed by the schema (additionalProperties=false): %s",
					path, strings.Join(extra, ", "))
			}
		}
	}

	// 数组：长度上限 + items 递归。
	if arr, ok := value.([]any); ok {
		// maxItems 是"输出规模预算"的落地点：模型写超了会被判为可修复的 schema 违规
		// （诊断会回灌），而不是一路写下去撞输出上限被截断（截断无法修复，见
		// llm.invalidJSONError）。两者都失败，但前者的失败原因与修法对模型可见。
		if node.MaxItems != nil && len(arr) > *node.MaxItems {
			return errors.Ef(errors.CodeValidationFailed,
				"tools: %s has too many items (max %d, got %d)", path, *node.MaxItems, len(arr))
		}
		if len(node.Items) > 0 {
			for i, item := range arr {
				if err := validateValue(fmt.Sprintf("%s[%d]", path, i), node.Items, item); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func validType(t string) bool {
	switch t {
	case "object", "array", "string", "integer", "number", "boolean", "null":
		return true
	default:
		return false
	}
}

func checkType(path, want string, value any) error {
	ok := false
	switch want {
	case "object":
		_, ok = value.(map[string]any)
	case "array":
		_, ok = value.([]any)
	case "string":
		_, ok = value.(string)
	case "boolean":
		_, ok = value.(bool)
	case "null":
		ok = value == nil
	case "integer":
		// json.Unmarshal 到 any 时数字为 float64；整数要求小数部分为 0。
		if f, isNum := toFloat(value); isNum {
			ok = f == math.Trunc(f)
		}
	case "number":
		_, ok = toFloat(value)
	}
	if !ok {
		return errors.Ef(errors.CodeValidationFailed,
			"tools: %s type mismatch: want %s, got %T (%v)", path, want, value, value)
	}
	return nil
}

// toFloat 把 JSON 数字（float64）或 Go 数值转为 float64。
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

// enumContains 判断 value 是否命中 enum（数字按数值比较，字符串按精确匹配）。
func enumContains(enum []any, value any) bool {
	for _, e := range enum {
		if equalJSON(e, value) {
			return true
		}
	}
	return false
}

func equalJSON(a, b any) bool {
	af, aok := toFloat(a)
	bf, bok := toFloat(b)
	if aok && bok {
		return af == bf
	}
	as, aok := a.(string)
	bs, bok := b.(string)
	if aok && bok {
		return as == bs
	}
	ab, aok := a.(bool)
	bb, bok := b.(bool)
	if aok && bok {
		return ab == bb
	}
	if a == nil && b == nil {
		return true
	}
	return false
}

// PathOf 返回错误消息中的字段路径（供调用方记录 evidence），
// 非校验错误时返回空串。
func PathOf(err error) string {
	ae := errors.As(err)
	if ae == nil || ae.Code != errors.CodeValidationFailed {
		return ""
	}
	const marker = "tools: "
	i := strings.Index(ae.Message, marker)
	if i < 0 {
		return ""
	}
	rest := ae.Message[i+len(marker):]
	end := strings.IndexAny(rest, " ")
	if end < 0 {
		return rest
	}
	return rest[:end]
}
