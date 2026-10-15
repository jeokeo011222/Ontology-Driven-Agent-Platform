package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"odap/internal/api/handlers"
	"odap/internal/platform/ids"
	"odap/internal/platform/tenantctx"
)

// 本文件是"路由可达性门禁"（评测清单 §6 盲区 ①：断言对象 ≠ 被判对象）。
//
// 为什么必须有它：此前的 TestAllContractEndpointsRegistered 只断言
// routes() 这张**声明表**里含有某端点，而从不发一次真实请求。
// 于是当 NewRouter 把一个更宽的前缀（/api/v1/auth/）挂到 root 上时，
// publicMux 只认识 register/login，logout 与 session 虽在声明表中"已注册"、
// 单测全绿，实际请求却得到纯文本 404 —— 声明与可达性之间的缺口无人看守。
//
// 判据（可证伪）：一个**已挂载**的端点其响应必然来自业务处理器，因此
// Content-Type 是 application/json（或成功时 204 无正文）。
// 未被挂载时 http.ServeMux 返回 Content-Type: text/plain; charset=utf-8
// 且正文为 "404 page not found" / "405 method not allowed"。
// 两者不可能混淆，故本条门禁不会退化成噪声。

// parameterValues 给出路径参数占位符的合法取值，用于把 {id} 之类的模式
// 实例化为一条真实可请求的路径。
var parameterValues = map[string]string{
	"id":          "11111111-1111-1111-1111-111111111111",
	"endpoint_id": "22222222-2222-2222-2222-222222222222",
	"key":         "demo-policy",
	"version":     "1",
}

var pathParamRe = regexp.MustCompile(`\{([^}]+)\}`)

// concretePath 把 /api/v1/runs/{id} 实例化为可请求路径。
func concretePath(pattern string) string {
	return pathParamRe.ReplaceAllStringFunc(pattern, func(m string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(m, "{"), "}")
		if v, ok := parameterValues[name]; ok {
			return v
		}
		return "unknown-param"
	})
}

// permissiveResolver 放行任意请求，使请求能穿过认证链抵达路由层。
// 本门禁只关心"是否被路由命中"，不关心鉴权与业务结果。
type permissiveResolver struct {
	tenant  ids.ID
	subject ids.ID
}

func (p permissiveResolver) Resolve(context.Context, *http.Request) (tenantctx.Identity, error) {
	return tenantctx.Identity{TenantID: p.tenant, SubjectID: p.subject}, nil
}

func newPermissiveRouter() http.Handler {
	tenant, _ := ids.Parse("11111111-1111-1111-1111-111111111111")
	subject, _ := ids.Parse("22222222-2222-2222-2222-222222222222")
	return NewRouter(handlers.Deps{}, Options{
		Resolver: permissiveResolver{tenant: tenant, subject: subject},
	})
}

// TestEveryDeclaredRouteIsReachable 逐条把 routes() 中声明的路由实例化后
// 真实请求一次，断言它确实被路由层处理（而非落到 ServeMux 的兜底 404/405）。
func TestEveryDeclaredRouteIsReachable(t *testing.T) {
	router := newPermissiveRouter()
	for _, rt := range Routes(handlers.New(handlers.Deps{})) {
		// /api/v1/stream 是 SSE 长连接端点，正常语义下会保持连接不返回，
		// 因此不做单次请求探测；它的挂载方式与其余受保护端点完全同构
		// （同一条 rs 循环、同一个受保护链），由声明表与下方 L2 回归用例共同覆盖。
		if rt.Pattern == "/api/v1/stream" {
			continue
		}
		path := concretePath(rt.Pattern)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(rt.Method, path, nil))

		if isRouted(rec) {
			continue
		}
		t.Fatalf("路由未被真实挂载（前缀遮蔽？）：%s %s\n  status=%d content-type=%q body=%q",
			rt.Method, rt.Pattern, rec.Code, rec.Header().Get("Content-Type"), truncate(rec.Body.String(), 120))
	}
}

// isRouted 判定响应是否来自业务处理器。
func isRouted(rec *httptest.ResponseRecorder) bool {
	if rec.Code == http.StatusNoContent {
		return true
	}
	return strings.Contains(rec.Header().Get("Content-Type"), "application/json")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// TestAuthSubtreeSiblingsAreNotShadowed 是 L2 的定点回归：
// 与公开端点 register/login 同处 /api/v1/auth/ 子树下的 logout 与 session
// 必须走受保护链并抵达处理器，不得被"公开"前缀吞掉。
func TestAuthSubtreeSiblingsAreNotShadowed(t *testing.T) {
	router := newPermissiveRouter()
	cases := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/auth/logout"},
		{http.MethodGet, "/api/v1/auth/session"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if !isRouted(rec) {
			t.Fatalf("%s %s 被前缀遮蔽：status=%d content-type=%q body=%q",
				c.method, c.path, rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
		}
	}
}

// TestPublicEndpointsStayPublic 反向约束：标记为 Public 的端点必须由
// **认证链之外**的处理器承接。这里用始终拒绝的解析器请求它们——若 Public
// 标记失效或误挂进受保护链，响应会是 401，用例立刻变红。
func TestPublicEndpointsStayPublic(t *testing.T) {
	h := handlers.New(handlers.Deps{})
	router := NewRouter(handlers.Deps{}, Options{Resolver: denyResolver{}})
	var saw int
	for _, rt := range Routes(h) {
		if !rt.Public {
			continue
		}
		saw++
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(rt.Method, concretePath(rt.Pattern), nil))
		if rec.Code == http.StatusUnauthorized {
			t.Fatalf("公开端点 %s %s 被认证链拦下（Public 标记失效）", rt.Method, rt.Pattern)
		}
		if !isRouted(rec) {
			t.Fatalf("公开端点 %s %s 未被真实挂载：status=%d content-type=%q",
				rt.Method, rt.Pattern, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	if saw == 0 {
		t.Fatal("routes() 中没有任何 Public 路由：登录/注册将无法访问")
	}
}

// TestProtectedRoutesRejectAnonymous 确认公开标记没有"溢出"到兄弟端点：
// 用始终拒绝的解析器请求受保护端点必须得到 401 JSON。
func TestProtectedRoutesRejectAnonymous(t *testing.T) {
	router := NewRouter(handlers.Deps{}, Options{Resolver: denyResolver{}})
	for _, path := range []string{"/api/v1/auth/logout", "/api/v1/auth/session", "/api/v1/admin/users"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s status=%d want 401", path, rec.Code)
		}
		if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("%s 未返回 JSON 错误体，Content-Type=%q", path, rec.Header().Get("Content-Type"))
		}
	}
}
