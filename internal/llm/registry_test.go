package llm

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// TestBuildGatewayExcludesDisabledEndpoint 验证被禁用的端点不会进入可路由集合，
// 同时**必须留痕**：静默跳过会让"已登记"与"可调用"看起来一致而实际不一致。
func TestBuildGatewayExcludesDisabledEndpoint(t *testing.T) {
	// 绑定必须是一个**可装配**的真实 HTTP 端点：MOCK 部署形态已下线，
	// 用 MOCK 绑定会因装配失败而被计入 Unavailable，测不到"禁用端点被排除"这一意图。
	bindings := []EndpointBinding{{
		ProviderID: "acme-llm", EndpointID: ids.New().String(), EndpointName: "ep-1",
		DeploymentType: "HTTP", BaseURL: "http://127.0.0.1:9/v1", CredentialRef: "TEST_LLM_KEY",
		ModelRevisionID: ids.New().String(), ModelID: "gpt-x-real",
		RevisionLabel: "0.1.0", Capabilities: ProviderCapabilities{NativeSchema: true, SupportsTools: true, MaxContextTokens: 8192},
	}}
	excluded := []Exclusion{{
		ProviderID: "openai-compat", EndpointName: "ep-disabled", ModelID: "gpt-x",
		Reason: "端点状态为 DISABLED（非 ACTIVE）",
	}}

	gw := BuildGateway(bindings, excluded, GatewayOptions{
		Logger: slog.Default(),
		Getenv: func(k string) string {
			if k == "TEST_LLM_KEY" {
				return "sk-test-0000"
			}
			return ""
		},
	})

	if len(gw.Providers) != 1 {
		t.Fatalf("期望装配 1 个端点，实际 %d", len(gw.Providers))
	}
	// 被禁用的模型必须仍在 Registered 里，否则请求它会被静默换成别家模型。
	if _, ok := gw.Router.Registered["gpt-x"]; !ok {
		t.Fatalf("被禁用的模型 gpt-x 未登记到 Registered，请求它会被静默替换")
	}
	if len(gw.Unavailable) != 1 {
		t.Fatalf("期望 1 条排除记录，实际 %d", len(gw.Unavailable))
	}
}

// TestRouterRefusesSilentModelSubstitution 是本文件最重要的断言：
// 请求一个"已登记但不可用"的模型时，绝不允许回落到别的可用模型。
func TestRouterRefusesSilentModelSubstitution(t *testing.T) {
	mock := NewMockProvider("mock-local/ep/mock-chat@0.1.0",
		ProviderCapabilities{NativeSchema: true, MaxContextTokens: 8192})
	r := &Router{
		Providers:  map[string]Provider{mock.Name(): mock},
		Routing:    map[string]string{}, // 不可用模型没有固定路由
		Registered: map[string]string{"gpt-x": "端点状态为 DISABLED（非 ACTIVE）"},
	}

	_, err := r.Resolve(CompletionRequest{ModelID: "gpt-x"})
	if err == nil {
		t.Fatalf("请求已登记但不可用的模型必须失败，实际成功回落到其他模型")
	}
	ae := errors.As(err)
	if ae.Code != errors.CodeUnavailable {
		t.Fatalf("期望 UNAVAILABLE，实际 %s（%v）", ae.Code, err)
	}
	if !strings.Contains(err.Error(), "gpt-x") {
		t.Fatalf("错误信息应包含被请求的模型名，实际: %v", err)
	}
}

// TestRouterSelectsUnregisteredModelByCapability 验证"未登记模型"仍可按能力择优：
// 这是合法的平台择优（调用方没有点名具体模型），与上面的静默替换有本质区别。
func TestRouterSelectsUnregisteredModelByCapability(t *testing.T) {
	mini := NewMockProvider("mini", ProviderCapabilities{NativeSchema: false, MaxContextTokens: 8192})
	full := NewMockProvider("full", ProviderCapabilities{NativeSchema: true, MaxContextTokens: 32768})
	r := &Router{
		Providers:  map[string]Provider{mini.Name(): mini, full.Name(): full},
		Routing:    map[string]string{},
		Registered: map[string]string{},
	}

	// 子集顺序："full" < "mini"（字母序），因此两种情况都要能选出端点。
	got, err := r.Resolve(CompletionRequest{ModelID: "whatever"})
	if err != nil {
		t.Fatalf("未登记模型应能按能力择优，实际失败: %v", err)
	}
	if got.Provider == nil {
		t.Fatalf("未返回 Provider")
	}

	// 要求结构化输出时应优先原生 Schema 端点（§6.4/§9.1）。
	got, err = r.Resolve(CompletionRequest{
		ModelID:        "whatever",
		ResponseSchema: json.RawMessage(`{"type":"object"}`),
	})
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got.Provider.Name() != full.Name() {
		t.Fatalf("要求结构化输出时应优先原生 Schema 端点 %q，实际 %q", full.Name(), got.Provider.Name())
	}
}

// TestSchemaSampleConformsToDeclaration 验证合成实例确实满足所声明的 Schema，
// 且超出子集时明确返回失败（不猜值）。
func TestSchemaSampleConformsToDeclaration(t *testing.T) {
	schema := json.RawMessage(`{
      "type": "object",
      "required": ["decision", "reasons"],
      "properties": {
        "decision": {"type": "string", "enum": ["APPROVE", "REJECT"]},
        "reasons": {"type": "array", "items": {"type": "string"}}
      }
    }`)
	v, ok := schemaSample(schema)
	if !ok {
		t.Fatalf("应能合成该 Schema 的实例")
	}
	obj, isObj := v.(map[string]any)
	if !isObj {
		t.Fatalf("期望对象，实际 %T", v)
	}
	if obj["decision"] != "APPROVE" {
		t.Fatalf("enum 应取首项 APPROVE，实际 %v", obj["decision"])
	}
	arr, isArr := obj["reasons"].([]any)
	if !isArr || len(arr) != 1 {
		t.Fatalf("数组应合成 1 个元素，实际 %v", obj["reasons"])
	}

	// 无 type 也无 const/default/enum：形状不可判定，必须失败而不是随便返回。
	if _, ok := schemaSample(json.RawMessage(`{"title":"无类型"}`)); ok {
		t.Fatalf("形状不可判定的 Schema 应合成失败")
	}
}

// TestMockProviderSynthesizesSchemaOutputOnlyWhenNative 验证 §9.1 的能力区分：
// 只有原生 Schema 端点才直接产出合法 JSON；generate-then-validate 端点必须
// 自由生成，由其不合法来驱动修复回路。
func TestMockProviderSynthesizesSchemaOutputOnlyWhenNative(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","required":["decision"],"properties":{"decision":{"type":"string","enum":["APPROVE"]}}}`)
	req := CompletionRequest{ModelID: "m", ResponseSchema: schema}

	native := NewMockProvider("native", ProviderCapabilities{NativeSchema: true})
	comp, err := native.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("complete 失败: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(comp.Content), &obj); err != nil {
		t.Fatalf("原生 Schema 端点的输出应为合法 JSON，实际 %q", comp.Content)
	}
	if obj["decision"] != "APPROVE" {
		t.Fatalf("输出应满足 enum，实际 %v", obj["decision"])
	}

	free := NewMockProvider("free", ProviderCapabilities{NativeSchema: false})
	comp2, err := free.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("complete 失败: %v", err)
	}
	if json.Valid([]byte(comp2.Content)) {
		t.Fatalf("非原生 Schema 端点应自由生成（以便驱动修复回路），实际直接给了合法 JSON: %q", comp2.Content)
	}
}

// TestHTTPProviderRejectsToolSpecsWithoutCapability 验证能力不匹配时明确报错，
// 而不是静默丢弃工具声明（丢弃会让模型"不知道"有工具可用，行为与调用方预期不符）。
func TestHTTPProviderRejectsToolSpecsWithoutCapability(t *testing.T) {
	p, err := NewHTTPProvider("ep", "http://127.0.0.1:1/v1", "k", "m",
		ProviderCapabilities{NativeSchema: false, SupportsTools: false, MaxContextTokens: 8192})
	if err != nil {
		t.Fatalf("构造 provider 失败: %v", err)
	}
	_, err = p.Complete(context.Background(), CompletionRequest{
		ModelID: "m",
		Tools:   []ToolSpec{{Name: "t"}},
	})
	if err == nil {
		t.Fatalf("不支持工具调用的端点收到工具声明时应失败")
	}
	if errors.As(err).Code != errors.CodeUnavailable {
		t.Fatalf("期望 UNAVAILABLE，实际 %v", err)
	}
}

// TestHTTPProviderClassifiesStatusCodes 验证状态码到错误码的映射是显式的：
// 429 必须可被上游限流治理识别，401/403 是装配缺陷而不是"对方挂了"。
func TestHTTPProviderClassifiesStatusCodes(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{429, errors.CodeRateLimited},
		{401, errors.CodeUnavailable},
		{403, errors.CodeUnavailable},
		{504, errors.CodeModelTimeout},
		{500, errors.CodeModelProviderError},
		{400, errors.CodeModelProviderError},
	}
	for _, c := range cases {
		if got := errors.As(statusToError("ep", c.status, nil)).Code; got != c.want {
			t.Errorf("HTTP %d 期望 %s，实际 %s", c.status, c.want, got)
		}
	}
}

// TestHTTPProviderFallsBackToRegisteredModelID 验证调用方未点名模型时，
// 出站 model 字段回落到该端点登记的模型名。
//
// 背景：Router 在未点名 ModelID 时按能力择优，此时 req.ModelID 为空。
// 若适配器原样下发空串，供应商会以 400 拒绝（"you passed ."），
// 表现为"网关已装配但生成始终失败"，且错误信息完全指向供应商而非本平台。
func TestHTTPProviderFallsBackToRegisteredModelID(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("解析请求体失败: %v", err)
		}
		got = body.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	caps := ProviderCapabilities{SupportsTools: true, MaxContextTokens: 8192}
	p, err := NewHTTPProvider("ep", srv.URL, "k", "deepseek-chat", caps)
	if err != nil {
		t.Fatalf("构造 provider 失败: %v", err)
	}

	// 未点名模型 → 用登记的模型名。
	if _, err := p.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if got != "deepseek-chat" {
		t.Fatalf("未点名模型时出站 model 应为登记的 deepseek-chat，实际 %q", got)
	}

	// 点名模型 → 用点名的，不被登记的默认值覆盖。
	if _, err := p.Complete(context.Background(), CompletionRequest{
		ModelID:  "deepseek-reasoner",
		Messages: []Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if got != "deepseek-reasoner" {
		t.Fatalf("点名模型时出站 model 应为 deepseek-reasoner，实际 %q", got)
	}
}

// TestDecodeCapabilitiesJSONRejectsMissingContext 验证能力档案缺关键字段时报错，
// 而不是让 max_context_tokens 静默取零值。
func TestDecodeCapabilitiesJSONRejectsMissingContext(t *testing.T) {
	if _, err := decodeCapabilitiesJSON([]byte(`{"native_schema":true}`)); err == nil {
		t.Fatalf("缺少 max_context_tokens 时应报错")
	}
	caps, err := decodeCapabilitiesJSON([]byte(`{"native_schema":true,"supports_tools":true,"max_context_tokens":8192}`))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !caps.NativeSchema || !caps.SupportsTools || caps.MaxContextTokens != 8192 {
		t.Fatalf("解析结果不正确: %+v", caps)
	}
}
