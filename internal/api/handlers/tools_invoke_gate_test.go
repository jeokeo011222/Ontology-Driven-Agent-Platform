package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"odap/internal/api/store"
	"odap/internal/platform/ids"
	"odap/internal/platform/tenantctx"
	"odap/internal/toolruntime"
)

// invokeRequest 构造带可信身份与合法路径参数的工具调用请求。
// 该请求会依次通过：身份 → 持久化 → 路径参数 → 请求体 → 工具目录解析；
// 本文件锁定「目录解析器未装配 → 明确 503」这条诚实门禁。
func invokeRequest(tenantID, toolID ids.ID) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/"+toolID.String()+"/invoke", nil)
	req.SetPathValue("id", toolID.String())
	ctx := tenantctx.WithIdentity(req.Context(), tenantctx.Identity{
		TenantID: tenantID, SubjectID: ids.New(),
	})
	return req.WithContext(ctx)
}

// TestInvokeTool_CatalogGate 证明工具调用的版本解析依赖未装配时，
// 返回的是明确的 UNAVAILABLE，而不是假成功或莫名 404。
func TestInvokeTool_CatalogGate(t *testing.T) {
	t.Run("目录解析器未装配 → 503 UNAVAILABLE", func(t *testing.T) {
		// Store 已装配（nil pool：本门禁在触库之前失败，不会 panic），
		// 但 Catalog 为 nil —— 装配缺口的报错必须指向缺口本身。
		h := New(Deps{Store: store.New(nil), Catalog: nil})
		rec := httptest.NewRecorder()
		h.InvokeTool(rec, invokeRequest(ids.New(), ids.New()))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("期望 503，实际 %d（body=%s）", rec.Code, rec.Body.String())
		}
		if code := errorCode(t, rec); code != "UNAVAILABLE" {
			t.Fatalf("期望码 UNAVAILABLE，实际 %s", code)
		}
	})

	t.Run("未装配目录解析器时不得返回 2xx（无假成功）", func(t *testing.T) {
		h := New(Deps{Store: store.New(nil), ToolCache: toolruntime.NewResultCache(0)})
		rec := httptest.NewRecorder()
		h.InvokeTool(rec, invokeRequest(ids.New(), ids.New()))
		if rec.Code >= 200 && rec.Code < 300 {
			t.Fatalf("依赖未就绪时不得返回成功状态，实际 %d（body=%s）", rec.Code, rec.Body.String())
		}
	})
}
