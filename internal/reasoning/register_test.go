package reasoning

// 规则注册校验测试：body 严格解析（DisallowUnknownFields）与结构校验。

import (
	"strings"
	"testing"
)

// TestParseRuleBodyAcceptsValid 验证合法规则体解析通过。
func TestParseRuleBodyAcceptsValid(t *testing.T) {
	body := []byte(`{
      "rule_key": "biz-dual-role",
      "name": "双重角色推断",
      "kind": "CUSTOM",
      "premises": [
        {"subject": "?x", "predicate": "https://odap.example/ont/demo#manages", "object": "?y"},
        {"subject": "?y", "predicate": "https://odap.example/ont/demo#manages", "object": "?x"}
      ],
      "conclusion": [
        {"subject": "?x", "predicate": "https://odap.example/ont/demo#peerOf", "object": "?y"}
      ],
      "description": "互相管理的人互为同侪"
    }`)
	r, err := ParseRuleBody(body)
	if err != nil {
		t.Fatalf("合法规则体解析失败：%v", err)
	}
	if r.RuleKey != "biz-dual-role" || r.Kind != KindCUSTOM || len(r.Premises) != 2 || len(r.Conclusion) != 1 {
		t.Fatalf("解析结果不符：%+v", r)
	}
}

// TestParseRuleBodyRejectsUnknownFields 验证未知字段被拒绝（含字段名笔误场景）。
func TestParseRuleBodyRejectsUnknownFields(t *testing.T) {
	cases := map[string]string{
		"premise 笔误（应为 premises）": `{"rule_key":"k","premise":[{"subject":"a","predicate":"b","object":"c"}],"conclusion":[]}`,
		"多余字段 when":               `{"rule_key":"k","when":[{"subject":"a","predicate":"b","object":"c"}],"conclusion":[]}`,
		"多余字段 priority":           `{"rule_key":"k","priority":1,"premises":[],"conclusion":[]}`,
	}
	for name, raw := range cases {
		if _, err := ParseRuleBody([]byte(raw)); err == nil {
			t.Errorf("%s：期望拒绝，实际通过", name)
		} else if !strings.Contains(err.Error(), "规则体非法") && !strings.Contains(err.Error(), "不能") {
			t.Errorf("%s：错误信息应说明规则体非法，实际：%v", name, err)
		}
	}
}

// TestParseRuleBodyRejectsBadStructure 验证结构非法（结论变量未在前提中出现等）。
func TestParseRuleBodyRejectsBadStructure(t *testing.T) {
	cases := map[string]string{
		"结论变量未在前提出现": `{"rule_key":"k","premises":[{"subject":"?x","predicate":"http://e/p","object":"?y"}],"conclusion":[{"subject":"?z","predicate":"http://e/q","object":"?y"}]}`,
		"结论含匿名变量":    `{"rule_key":"k","premises":[{"subject":"?x","predicate":"http://e/p","object":"?y"}],"conclusion":[{"subject":"?x","predicate":"http://e/q","object":""}]}`,
		"空 premises": `{"rule_key":"k","premises":[],"conclusion":[{"subject":"a","predicate":"b","object":"c"}]}`,
		"空 rule_key": `{"rule_key":"","premises":[{"subject":"a","predicate":"b","object":"c"}],"conclusion":[{"subject":"a","predicate":"b","object":"c"}]}`,
		"kind 非枚举":   `{"rule_key":"k","kind":"WHATEVER","premises":[{"subject":"a","predicate":"b","object":"c"}],"conclusion":[{"subject":"a","predicate":"b","object":"c"}]}`,
		"常量词法非法":     `{"rule_key":"k","premises":[{"subject":"\"未闭合","predicate":"b","object":"c"}],"conclusion":[{"subject":"a","predicate":"b","object":"c"}]}`,
	}
	for name, raw := range cases {
		if _, err := ParseRuleBody([]byte(raw)); err == nil {
			t.Errorf("%s：期望拒绝，实际通过", name)
		}
	}
}

// TestBuiltinRulesShape 验证内置规则集自身全部通过结构校验（规则定义回归防线）。
func TestBuiltinRulesShape(t *testing.T) {
	for _, r := range BuiltinRules() {
		if err := ValidateRule(r); err != nil {
			t.Errorf("内置规则 %s 校验失败：%v", r.RuleKey, err)
		}
		if !r.Builtin || !r.Enabled {
			t.Errorf("内置规则 %s 应默认 Builtin/Enabled", r.RuleKey)
		}
	}
	if len(builtinKeySet()) == 0 {
		t.Fatal("内置规则集为空")
	}
}
