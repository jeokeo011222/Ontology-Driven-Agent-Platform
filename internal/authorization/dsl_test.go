package authorization

import (
	"strings"
	"testing"

	"odap/internal/platform/errors"
)

func mustInvalid(t *testing.T, name, doc string) {
	t.Helper()
	err := Validate([]byte(doc))
	if err == nil {
		t.Fatalf("%s: expected rejection", name)
	}
	if code := errors.As(err).Code; code != errors.CodeAUTHZPolicyInvalid {
		t.Fatalf("%s: err code = %s, want AUTHZ_POLICY_INVALID", name, code)
	}
}

func TestValidateAcceptsWellFormedPolicy(t *testing.T) {
	doc := `{
	  "version": "1",
	  "rules": [
	    {
	      "id": "expense.read.own",
	      "action": "expense.read",
	      "resource_type": "ExpenseClaim",
	      "effect": "ALLOW",
	      "conditions": [
	        {"field": "resource_owner_id", "op": "eq", "value": "u-1"},
	        {"field": "amount", "op": "gte", "value": 0}
	      ],
	      "priority": 10,
	      "reason_code": "OWN_CLAIM_READ",
	      "risk_level": "LOW"
	    },
	    {
	      "id": "expense.approve.high_amount",
	      "action": "expense.approve",
	      "resource_type": "ExpenseClaim",
	      "effect": "REQUIRE_APPROVAL",
	      "conditions": [
	        {"field": "amount", "op": "gte", "value": 10000},
	        {"field": "currency", "op": "in", "value": ["CNY", "USD"]},
	        {"field": "resource_ref", "op": "exists"}
	      ],
	      "priority": 5,
	      "reason_code": "REQUIRE_APPROVAL_AMOUNT"
	    }
	  ]
	}`
	if err := Validate([]byte(doc)); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	p, err := ParsePolicy([]byte(doc))
	if err != nil {
		t.Fatalf("ParsePolicy: %v", err)
	}
	if len(p.Rules) != 2 {
		t.Fatalf("rules = %d, want 2", len(p.Rules))
	}
	if p.Rules[0].Effect != EffectAllow || p.Rules[1].Effect != EffectRequireApproval {
		t.Fatalf("unexpected effects: %+v", p.Rules)
	}
}

func TestValidateAcceptsEmptyRules(t *testing.T) {
	if err := Validate([]byte(`{"version":"1","rules":[]}`)); err != nil {
		t.Fatalf("empty rules should be valid: %v", err)
	}
}

func TestValidateRejectsMalformedDocuments(t *testing.T) {
	cases := map[string]string{
		"invalid json":          `{"version": "1",`,
		"trailing content":      `{"version":"1","rules":[]}{"version":"1","rules":[]}`,
		"bad version":           `{"version":"2","rules":[]}`,
		"unknown top field":     `{"version":"1","rules":[],"extra":1}`,
		"unknown rule field":    `{"version":"1","rules":[{"id":"r","action":"a","resource_type":"R","effect":"ALLOW","reason_code":"X","surprise":true}]}`,
		"unknown effect":        `{"version":"1","rules":[{"id":"r","action":"a","resource_type":"R","effect":"MAYBE","reason_code":"X"}]}`,
		"unknown op":            `{"version":"1","rules":[{"id":"r","action":"a","resource_type":"R","effect":"ALLOW","reason_code":"X","conditions":[{"field":"amount","op":"regex","value":"x"}]}]}`,
		"unregistered field":    `{"version":"1","rules":[{"id":"r","action":"a","resource_type":"R","effect":"ALLOW","reason_code":"X","conditions":[{"field":"claimAmount","op":"eq","value":1}]}]}`,
		"missing id":            `{"version":"1","rules":[{"action":"a","resource_type":"R","effect":"ALLOW","reason_code":"X"}]}`,
		"duplicate id":          `{"version":"1","rules":[{"id":"r","action":"a","resource_type":"R","effect":"ALLOW","reason_code":"X"},{"id":"r","action":"a","resource_type":"R","effect":"DENY","reason_code":"Y"}]}`,
		"missing action":        `{"version":"1","rules":[{"id":"r","resource_type":"R","effect":"ALLOW","reason_code":"X"}]}`,
		"missing resource type": `{"version":"1","rules":[{"id":"r","action":"a","effect":"ALLOW","reason_code":"X"}]}`,
		"missing reason code":   `{"version":"1","rules":[{"id":"r","action":"a","resource_type":"R","effect":"ALLOW"}]}`,
		"exists with value":     `{"version":"1","rules":[{"id":"r","action":"a","resource_type":"R","effect":"ALLOW","reason_code":"X","conditions":[{"field":"amount","op":"exists","value":1}]}]}`,
		"op without value":      `{"version":"1","rules":[{"id":"r","action":"a","resource_type":"R","effect":"ALLOW","reason_code":"X","conditions":[{"field":"amount","op":"eq"}]}]}`,
		"in with scalar value":  `{"version":"1","rules":[{"id":"r","action":"a","resource_type":"R","effect":"ALLOW","reason_code":"X","conditions":[{"field":"currency","op":"in","value":"CNY"}]}]}`,
		"gte with bool value":   `{"version":"1","rules":[{"id":"r","action":"a","resource_type":"R","effect":"ALLOW","reason_code":"X","conditions":[{"field":"amount","op":"gte","value":true}]}]}`,
		"contains with number":  `{"version":"1","rules":[{"id":"r","action":"a","resource_type":"R","effect":"ALLOW","reason_code":"X","conditions":[{"field":"currency","op":"contains","value":3}]}]}`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			mustInvalid(t, name, doc)
			if !strings.Contains(name, "invalid json") {
				// 非法文档必须给出可读的拒绝原因。
				err := Validate([]byte(doc))
				if msg := errors.As(err).Message; msg == "" {
					t.Fatalf("%s: rejection without message", name)
				}
			}
		})
	}
}

func TestValidateRejectsTooManyRules(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"version":"1","rules":[`)
	for i := 0; i < MaxRulesPerPolicy+1; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"id":"r","action":"a","resource_type":"R","effect":"ALLOW","reason_code":"X"}`)
	}
	sb.WriteString(`]}`)
	mustInvalid(t, "too many rules", sb.String())
}

func TestAllWhitelistedOpsRoundTrip(t *testing.T) {
	// 每个白名单算子至少有一个可校验通过的最小用例。
	docs := map[string]string{
		"eq":       `{"field":"currency","op":"eq","value":"CNY"}`,
		"ne":       `{"field":"currency","op":"ne","value":"USD"}`,
		"in":       `{"field":"currency","op":"in","value":["CNY"]}`,
		"not_in":   `{"field":"currency","op":"not_in","value":["USD"]}`,
		"gte":      `{"field":"amount","op":"gte","value":1}`,
		"lte":      `{"field":"amount","op":"lte","value":1}`,
		"exists":   `{"field":"amount","op":"exists"}`,
		"contains": `{"field":"currency","op":"contains","value":"CN"}`,
	}
	for op, cond := range docs {
		doc := `{"version":"1","rules":[{"id":"r","action":"a","resource_type":"R","effect":"ALLOW","reason_code":"X","conditions":[` + cond + `]}]}`
		if err := Validate([]byte(doc)); err != nil {
			t.Fatalf("op %s rejected: %v", op, err)
		}
	}
}
