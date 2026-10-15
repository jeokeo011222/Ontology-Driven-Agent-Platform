package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"odap/internal/platform/errors"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

const verifyOutput = `{
  "status": "COMPLETED",
  "amount": 120.5,
  "items": ["a", "b"],
  "meta": {"owner": "u1"},
  "note": ""
}`

func TestVerify(t *testing.T) {
	cases := []struct {
		name       string
		output     json.RawMessage
		assertions []Assertion
		wantPassed bool
		failCount  int
	}{
		{
			name:       "NOT_EMPTY 通过",
			output:     raw(verifyOutput),
			assertions: []Assertion{{Kind: AssertNotEmpty, Target: "status"}},
			wantPassed: true,
		},
		{
			name:       "NOT_EMPTY 失败（空字符串）",
			output:     raw(verifyOutput),
			assertions: []Assertion{{Kind: AssertNotEmpty, Target: "note"}},
			wantPassed: false,
			failCount:  1,
		},
		{
			name:       "EQUALS 通过",
			output:     raw(verifyOutput),
			assertions: []Assertion{{Kind: AssertEquals, Target: "status", Expected: raw(`"COMPLETED"`)}},
			wantPassed: true,
		},
		{
			name:       "EQUALS 失败",
			output:     raw(verifyOutput),
			assertions: []Assertion{{Kind: AssertEquals, Target: "status", Expected: raw(`"FAILED"`)}},
			wantPassed: false,
			failCount:  1,
		},
		{
			name:       "CONTAINS 字符串通过",
			output:     raw(verifyOutput),
			assertions: []Assertion{{Kind: AssertContains, Target: "status", Expected: raw(`"COMP"`)}},
			wantPassed: true,
		},
		{
			name:       "CONTAINS 数组通过",
			output:     raw(verifyOutput),
			assertions: []Assertion{{Kind: AssertContains, Target: "items", Expected: raw(`"b"`)}},
			wantPassed: true,
		},
		{
			name:       "CONTAINS 数组失败",
			output:     raw(verifyOutput),
			assertions: []Assertion{{Kind: AssertContains, Target: "items", Expected: raw(`"z"`)}},
			wantPassed: false,
			failCount:  1,
		},
		{
			name:   "JSON_SCHEMA 通过",
			output: raw(verifyOutput),
			assertions: []Assertion{{
				Kind: AssertJSONSchema, Target: "meta",
				Expected: raw(`{"type":"object","required":["owner"],"properties":{"owner":{"type":"string"}}}`),
			}},
			wantPassed: true,
		},
		{
			name:   "JSON_SCHEMA 失败",
			output: raw(verifyOutput),
			assertions: []Assertion{{
				Kind: AssertJSONSchema, Target: "meta",
				Expected: raw(`{"type":"object","required":["missing"],"properties":{}}`),
			}},
			wantPassed: false,
			failCount:  1,
		},
		{
			name:       "FIELD_EXISTS 通过",
			output:     raw(verifyOutput),
			assertions: []Assertion{{Kind: AssertFieldExists, Target: "meta.owner"}},
			wantPassed: true,
		},
		{
			name:       "FIELD_EXISTS 失败",
			output:     raw(verifyOutput),
			assertions: []Assertion{{Kind: AssertFieldExists, Target: "meta.nope"}},
			wantPassed: false,
			failCount:  1,
		},
		{
			name:       "RANGE 通过",
			output:     raw(verifyOutput),
			assertions: []Assertion{{Kind: AssertRange, Target: "amount", Expected: raw(`{"min":100,"max":200}`)}},
			wantPassed: true,
		},
		{
			name:       "RANGE 失败",
			output:     raw(verifyOutput),
			assertions: []Assertion{{Kind: AssertRange, Target: "amount", Expected: raw(`{"min":200,"max":300}`)}},
			wantPassed: false,
			failCount:  1,
		},
		{
			name:   "失败聚合（多断言同时失败）",
			output: raw(verifyOutput),
			assertions: []Assertion{
				{Kind: AssertEquals, Target: "status", Expected: raw(`"FAILED"`)},
				{Kind: AssertFieldExists, Target: "nope"},
				{Kind: AssertRange, Target: "amount", Expected: raw(`{"min":1000}`)},
				{Kind: AssertNotEmpty, Target: "status"}, // 通过
			},
			wantPassed: false,
			failCount:  3,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			report, err := Verify(c.output, c.assertions)
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if report.Passed != c.wantPassed {
				t.Fatalf("passed=%v, want %v（failures=%v）", report.Passed, c.wantPassed, report.Failures)
			}
			if len(report.Failures) != c.failCount {
				t.Fatalf("失败数=%d, want %d", len(report.Failures), c.failCount)
			}
			for _, f := range report.Failures {
				if f.Assertion == "" || f.Message == "" {
					t.Fatalf("失败条目缺少断言标识或原因：%+v", f)
				}
			}
		})
	}
}

func TestVerifyInvalidJSON(t *testing.T) {
	_, err := Verify(raw("{not json"), nil)
	if errors.As(err).Code != errors.CodeModelOutputInvalid {
		t.Fatalf("非法 JSON 应返回 MODEL_OUTPUT_INVALID，got %v", err)
	}
}

func TestVerifyUnknownAssertionKind(t *testing.T) {
	report, err := Verify(raw(verifyOutput), []Assertion{{Kind: "WAT", Target: "status"}})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if report.Passed || len(report.Failures) != 1 || !strings.Contains(report.Failures[0].Message, "未登记") {
		t.Fatalf("未登记断言应失败：%+v", report)
	}
}
