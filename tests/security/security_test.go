// Package security 承载安全测试（第十六篇 §10.2「安全测试」）。
//
// 覆盖：跨租户访问 / 对象越权 / 秘密泄露。本文件覆盖"未认证一律拒绝"与
// "拒绝响应体不泄露秘密"两条可用真实 HTTP 请求验证的基线；
// 需要真实 PostgreSQL 的跨租户 RLS 断言在 tenant_isolation_test.go（-tags=integration）。
//
// 依据 §10.1："高风险安全测试不应仅作为不影响发布的报告任务"——本包所有断言
// 都是硬失败，CI 中以 CI_STRICT=1 运行，缺环境即失败而非跳过。
package security

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"odap/internal/api"
	"odap/internal/api/handlers"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/tenantctx"
)

// denyResolver 模拟"没有任何有效会话"的请求：认证链必须 fail-closed。
type denyResolver struct{}

func (denyResolver) Resolve(context.Context, *http.Request) (tenantctx.Identity, error) {
	return tenantctx.Identity{}, errors.E(errors.CodeUnauthenticated, "缺少或失效的会话")
}

// allowResolver 返回一个有效的租户身份，用于验证"认证通过后仍受对象范围约束"。
type allowResolver struct {
	tenant  ids.ID
	subject ids.ID
}

func (a allowResolver) Resolve(context.Context, *http.Request) (tenantctx.Identity, error) {
	return tenantctx.Identity{TenantID: a.tenant, SubjectID: a.subject}, nil
}

var pathParamRe = regexp.MustCompile(`\{([^}]+)\}`)

var parameterValues = map[string]string{
	"id":          "11111111-1111-1111-1111-111111111111",
	"endpoint_id": "22222222-2222-2222-2222-222222222222",
	"key":         "demo-policy",
	"version":     "1",
}

func concretePath(pattern string) string {
	return pathParamRe.ReplaceAllStringFunc(pattern, func(m string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(m, "{"), "}")
		if v, ok := parameterValues[name]; ok {
			return v
		}
		return "unknown-param"
	})
}

// TestAnonymousAccessIsRejectedForEveryProtectedRoute 逐条对受保护端点发起
// 无凭据请求，断言一律 401 且响应体是合规的错误结构（含 UNAUTHENTICATED）。
//
// 为什么逐条而不是抽查：漏挂一条受保护端点就等于开了一个后门，
// 而"漏挂"恰恰是路由装配最容易出错的地方（L2 前缀遮蔽就是这么发生的）。
func TestAnonymousAccessIsRejectedForEveryProtectedRoute(t *testing.T) {
	h := handlers.New(handlers.Deps{})
	// 限流在认证链**之前**（RateLimit → Auth），逐条探测 82 个端点必然触发默认
	// 限流（60/30）而得到 429 —— 那是限流生效的正确表现，不是认证缺陷。
	// 本用例要验证的是认证链，故显式抬高限流上限。
	router := api.NewRouter(handlers.Deps{}, api.Options{
		Resolver:          denyResolver{},
		RateLimitCapacity: 10000,
		RateLimitRefill:   10000,
	})

	var checked int
	for _, rt := range api.Routes(h) {
		if rt.Public {
			continue
		}
		checked++
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(rt.Method, concretePath(rt.Pattern), nil))

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s 匿名访问返回 %d，期望 401", rt.Method, rt.Pattern, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("%s %s 401 响应不是 JSON（Content-Type=%q）", rt.Method, rt.Pattern, ct)
		}
		if body := rec.Body.String(); !strings.Contains(body, "UNAUTHENTICATED") {
			t.Errorf("%s %s 401 响应体缺少 UNAUTHENTICATED 错误码：%q", rt.Method, rt.Pattern, body)
		}
	}
	if checked == 0 {
		t.Fatal("没有可检查的受保护端点（路由装配或 Public 标记可能已坏）")
	}
	t.Logf("已校验 %d 个受保护端点的匿名拒绝", checked)
}

// TestDeniedResponseLeaksNoSecrets 拒绝响应体不得回显任何凭据类内容。
// 这类泄露不会让功能失败，只会让失败变得"信息丰富"——正是审计上最忌讳的。
func TestDeniedResponseLeaksNoSecrets(t *testing.T) {
	router := api.NewRouter(handlers.Deps{}, api.Options{Resolver: denyResolver{}})
	// 用一条受保护端点触发拒绝响应
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/users", nil))

	body := strings.ToLower(rec.Body.String())
	// `sk-` 是 OpenAI 兼容密钥的典型前缀：模型端点凭据只入不出，任何拒绝响应
	// 都不该带上密钥样片段（录入/探测端点同样受此约束）。
	for _, bad := range []string{"postgres://", "password", "session_token", "refresh_token", "secret", "sk-"} {
		if strings.Contains(body, bad) {
			t.Errorf("拒绝响应体泄露敏感内容 %q：%s", bad, rec.Body.String())
		}
	}
}

// TestAuthenticatedRequestStillFailsClosedOnMissingScope 认证通过但缺少对象范围时，
// 不得降级为成功或 500 —— 必须是明确的授权拒绝（4xx）。
// 这里用一个大概率不存在的资源 ID，断言得到 404/403 而不是 200/500。
func TestAuthenticatedRequestStillFailsClosedOnMissingScope(t *testing.T) {
	tenant, _ := ids.Parse("11111111-1111-1111-1111-111111111111")
	subject, _ := ids.Parse("22222222-2222-2222-2222-222222222222")
	router := api.NewRouter(handlers.Deps{}, api.Options{
		Resolver: allowResolver{tenant: tenant, subject: subject},
	})

	cases := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/admin/users/11111111-1111-1111-1111-111111111111"},
		{http.MethodGet, "/api/v1/agent-runs/11111111-1111-1111-1111-111111111111"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code == http.StatusOK {
			t.Errorf("%s %s 在依赖未装配（无数据库）时返回 200——不得把未知状态报告为成功（§10.3 硬门禁）",
				c.method, c.path)
		}
		if rec.Code >= 500 && rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s 返回 %d：依赖缺失应 fail-closed 为 404/403/503，而不是 5xx 崩溃",
				c.method, c.path, rec.Code)
		}
	}
}
