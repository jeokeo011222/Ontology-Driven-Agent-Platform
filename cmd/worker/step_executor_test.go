package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"odap/internal/llm"
	"odap/internal/platform/ids"
	"odap/internal/registry"
)

// newRenderEnv 构造无技能加载器的渲染环境（技能展开在 skills 非 nil 时才发生）。
func newRenderEnv() *stepExecutor { return &stepExecutor{} }

// TestExpandTemplate 锁定 {{key}} 占位符语义：命中替换、未命中保留原样、
// 非占位符花括号不误伤。
func TestExpandTemplate(t *testing.T) {
	got := expandTemplate("报销单：{{expense}}；制度：{{policy}}", func(key string) (string, bool) {
		if key == "expense" {
			return `{"id":"INV-1"}`, true
		}
		return "", false
	})
	want := `报销单：{"id":"INV-1"}；制度：{{policy}}`
	if got != want {
		t.Fatalf("got = %q, want %q", got, want)
	}
	// 无占位符原样返回。
	if s := expandTemplate("plain text", func(string) (string, bool) { return "x", true }); s != "plain text" {
		t.Fatalf("plain = %q", s)
	}
	// 孤立 {{（无闭合）不 panic、原样返回。
	if s := expandTemplate("a {{ b", func(string) (string, bool) { return "x", true }); s != "a {{ b" {
		t.Fatalf("unclosed = %q", s)
	}
	// 嵌套占位符键含空格被 trim。
	if s := expandTemplate("{{ key }}", func(k string) (string, bool) { return "v", k == "key" }); s != "v" {
		t.Fatalf("trim = %q", s)
	}
}

// TestRenderPrompt 验证：系统提示词必备、user 模板注入 input_payload、
// 工具结果以 tool:<key> 注入、缺失占位符保留并出 warning。
func TestRenderPrompt(t *testing.T) {
	av := registry.AgentVersion{
		ID:             ids.New(),
		PromptTemplate: json.RawMessage(`{"system":"你是审核助手","user":"单据：{{expense}} 工具：{{tool:erp.expense.query}} 缺失：{{nope}}"}`),
	}
	payload := runPayload{InputPayload: json.RawMessage(`{"expense":{"id":"INV-9"}}`)}
	toolResults := map[string]map[string]any{
		"erp.expense.query": {"total": 42.0},
	}
	msgs, warnings, err := newRenderEnv().renderPrompt(context.Background(), ids.New(), av, payload, toolResults)
	if err != nil {
		t.Fatalf("renderPrompt: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Role != "system" || msgs[1].Role != "user" {
		t.Fatalf("messages = %+v", msgs)
	}
	if msgs[0].Content != "你是审核助手" {
		t.Fatalf("system = %q", msgs[0].Content)
	}
	if !contains(msgs[1].Content, `{"id":"INV-9"}`) || !contains(msgs[1].Content, `"total":42`) {
		t.Fatalf("user 未注入数据：%q", msgs[1].Content)
	}
	if !contains(msgs[1].Content, "{{nope}}") {
		t.Fatalf("缺失占位符应保留原样：%q", msgs[1].Content)
	}
	if len(warnings) != 1 || !contains(warnings[0], "nope") {
		t.Fatalf("warnings = %v", warnings)
	}
}

// TestRenderPromptGuardrails 锁定 fail-closed 边界：无模板 / 空系统提示词 /
// 非法 JSON 一律拒绝（拒绝无提示词的模型调用比产出不可解释输出更安全）。
func TestRenderPromptGuardrails(t *testing.T) {
	env := newRenderEnv()
	ctx := context.Background()
	tenant := ids.New()
	if _, _, err := env.renderPrompt(ctx, tenant, registry.AgentVersion{ID: ids.New()}, runPayload{}, nil); err == nil {
		t.Fatal("无 prompt_template 应报错")
	}
	if _, _, err := env.renderPrompt(ctx, tenant, registry.AgentVersion{
		ID: ids.New(), PromptTemplate: json.RawMessage(`{"user":"x"}`),
	}, runPayload{}, nil); err == nil {
		t.Fatal("空 system 应报错")
	}
	if _, _, err := env.renderPrompt(ctx, tenant, registry.AgentVersion{
		ID: ids.New(), PromptTemplate: json.RawMessage(`not-json`),
	}, runPayload{}, nil); err == nil {
		t.Fatal("非法模板 JSON 应报错")
	}
}

// TestAgentConfigDefaults 锁定配置缺省与钳制。
func TestAgentConfigDefaults(t *testing.T) {
	if cfg := agentConfig(nil); cfg.MaxOutputTokens != 1024 || cfg.Temperature != 0 {
		t.Fatalf("缺省配置 = %+v", cfg)
	}
	if cfg := agentConfig(json.RawMessage(`{"max_output_tokens": 999999}`)); cfg.MaxOutputTokens != 1024 {
		t.Fatalf("超上限应回缺省，got %d", cfg.MaxOutputTokens)
	}
	if cfg := agentConfig(json.RawMessage(`{"max_output_tokens": 2048, "temperature": 0.3}`)); cfg.MaxOutputTokens != 2048 {
		t.Fatalf("合法值应生效，got %d", cfg.MaxOutputTokens)
	}
}

// TestToolArgsOf 验证 tools_args 提取与缺省 nil。
func TestToolArgsOf(t *testing.T) {
	argsOf := toolArgsOf(json.RawMessage(`{"tools_args": {"erp.expense.query": {"month": "2026-01"}}}`))
	if got := argsOf("erp.expense.query"); got == nil || got["month"] != "2026-01" {
		t.Fatalf("argsOf = %v", got)
	}
	if argsOf("other.tool") != nil {
		t.Fatal("未声明工具的 args 应为 nil")
	}
	if toolArgsOf(nil)("x") != nil {
		t.Fatal("空 input 应为 nil")
	}
}

// TestClipTextUTF8Boundary 锁定 UTF-8 边界安全：字节预算截断不得拆开多字节
// 字符（拆开会产出非法字节序列，被 PostgreSQL 以 SQLSTATE 22021 拒绝——
// A2A 冒烟中模型输出的中文摘要在 200 字节处截断的真实事故）。
func TestClipTextUTF8Boundary(t *testing.T) {
	long := strings.Repeat("报销单审核意见", 100) // 600 字节中文
	clipped := clipText(long, 200)
	if !utf8.ValidString(clipped) {
		t.Fatalf("clipText 产出非法 UTF-8：%q", clipped)
	}
	if !strings.HasSuffix(clipped, "…") {
		t.Fatalf("截断结果应以省略号结尾：%q", clipped)
	}
	// 截断体 ≤200 字节（rune 回退最多再让 2 字节）+ 省略号 3 字节。
	if len(clipped) > 203 {
		t.Fatalf("clipText 超字节预算：%d", len(clipped))
	}
	if got := clipText("短文本", 200); got != "短文本" {
		t.Fatalf("未超预算应原样返回，实际 %q", got)
	}
	if got := clipText("报销单审核意见", 5); !utf8.ValidString(got) || !strings.HasSuffix(got, "…") {
		t.Fatalf("中文字节边界截断失败：%q", got)
	}
}

// TestEvidenceItemInput 验证摘要实算与字段透传。
func TestEvidenceItemInput(t *testing.T) {
	item := evidenceItemInput("MODEL_OUTPUT", "llm", "model:gpt-x", "结论内容", "INFER", "run-1", "")
	if item.ContentDigest == "" || item.ContentDigest != sha256HexWorker([]byte("结论内容")) {
		t.Fatalf("digest 必须实算：%q", item.ContentDigest)
	}
	if item.DecisionType != "INFER" || item.TaskID != "run-1" {
		t.Fatalf("item = %+v", item)
	}
	if len(item.Summary) > 201*3 { // 200 字符 + 省略号（UTF-8 多字节上界）
		t.Fatalf("summary 未截断：%d", len(item.Summary))
	}
}

// TestLLMMessageShape 编译期防线：worker 渲染的消息形状与 llm.Message 对齐。
func TestLLMMessageShape(t *testing.T) {
	var _ []llm.Message = []llm.Message{{Role: "system", Content: "s"}}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}()
}
