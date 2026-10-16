package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"odap/internal/api/handlers"
	"odap/internal/platform/errors"
	"odap/internal/platform/tenantctx"
)

// contractEndpoints 是 contracts/03 §2 端点总表的期望集合（method + pattern）。
// 任一缺漏都会使本测试失败，防止"文档有、路由无"。
var contractEndpoints = []string{
	// Auth 端点（公开 + 受保护）
	"POST /api/v1/auth/register",
	"POST /api/v1/auth/login",
	"POST /api/v1/auth/logout",
	"GET /api/v1/auth/session",
	// Chat 会话（2026-10-16 P0-4 新增）
	"POST /api/v1/chat/sessions",
	"GET /api/v1/chat/sessions",
	"GET /api/v1/chat/sessions/{id}",
	"PATCH /api/v1/chat/sessions/{id}",
	"DELETE /api/v1/chat/sessions/{id}",
	"POST /api/v1/chat/sessions/{id}/archive",
	"POST /api/v1/chat/sessions/{id}/messages",
	"GET /api/v1/chat/sessions/{id}/messages",
	// 协同编辑 WebSocket（P1-3 新增：y-websocket 二进制帧中继）
	"GET /api/v1/collab/{ontologyId}/ws",
	// Run
	"POST /api/v1/runs",
	"GET /api/v1/runs",
	"GET /api/v1/runs/{id}",
	"GET /api/v1/runs/{id}/steps",
	"POST /api/v1/runs/{id}/cancel",
	"POST /api/v1/runs/{id}/reconcile",
	"POST /api/v1/runs/{id}/lease/renew",
	// Agent
	"GET /api/v1/agents",
	"POST /api/v1/agents",
	"GET /api/v1/agents/{id}",
	"PATCH /api/v1/agents/{id}",
	"DELETE /api/v1/agents/{id}",
	"POST /api/v1/agents/{id}/publish",
	"POST /api/v1/agents/generate",
	// Ontology
	"GET /api/v1/ontologies",
	"POST /api/v1/ontologies",
	"POST /api/v1/ontologies/generate",
	"GET /api/v1/ontologies/{id}",
	"PATCH /api/v1/ontologies/{id}",
	"DELETE /api/v1/ontologies/{id}",
	"POST /api/v1/ontologies/{id}/validate",
	"POST /api/v1/ontologies/{id}/publish",
	"GET /api/v1/ontologies/{id}/artifacts",
	// 本体自然语言问答
	"POST /api/v1/ontology/nl-query",
	// Plugin
	"GET /api/v1/plugins",
	"POST /api/v1/plugins",
	"GET /api/v1/plugins/{id}",
	"PATCH /api/v1/plugins/{id}",
	"DELETE /api/v1/plugins/{id}",
	"POST /api/v1/plugins/{id}/enable",
	// Tool
	"GET /api/v1/tools",
	"POST /api/v1/tools",
	"GET /api/v1/tools/{id}",
	"PATCH /api/v1/tools/{id}",
	"DELETE /api/v1/tools/{id}",
	"POST /api/v1/tools/{id}/invoke",
	"GET /api/v1/tools/{id}/invocations",
	"POST /api/v1/tools/{id}/invocations/{inv_id}/approve",
	"POST /api/v1/tools/{id}/invocations/{inv_id}/reject",
	"POST /api/v1/tools/generate",
	// Skill（一等 registry 资源）
	"GET /api/v1/skills",
	"POST /api/v1/skills",
	"GET /api/v1/skills/{id}",
	"PATCH /api/v1/skills/{id}",
	"DELETE /api/v1/skills/{id}",
	"GET /api/v1/skills/{id}/versions",
	"POST /api/v1/skills/{id}/versions",
	"POST /api/v1/skills/{id}/publish",
	// Workflow（插件式）
	"GET /api/v1/workflows",
	"POST /api/v1/workflows",
	"GET /api/v1/workflows/{id}",
	"PATCH /api/v1/workflows/{id}",
	"DELETE /api/v1/workflows/{id}",
	"POST /api/v1/workflows/{id}/publish",
	"POST /api/v1/workflows/generate",
	// Policy
	"GET /api/v1/policies",
	"POST /api/v1/policies",
	"GET /api/v1/policies/{id}/versions",
	"POST /api/v1/policies/{id}/versions",
	"PATCH /api/v1/policies/{key}/versions/{version}",
	"DELETE /api/v1/policies/{key}/versions/{version}",
	// Approval
	"GET /api/v1/approvals",
	"GET /api/v1/approvals/{id}",
	"POST /api/v1/approvals/{id}/decision",
	// ERP
	"GET /api/v1/erp/capabilities",
	"POST /api/v1/erp/operations/{id}/reconcile",
	"GET /api/v1/runs/{id}/erp-operations",
	// ERP 系统字典与租户连接（与ERP集成）
	"GET /api/v1/admin/erp-systems",
	"GET /api/v1/admin/erp-connections",
	"POST /api/v1/admin/erp-connections",
	"POST /api/v1/admin/erp-connections/test",
	"PATCH /api/v1/admin/erp-connections/{id}",
	"DELETE /api/v1/admin/erp-connections/{id}",
	// Audit
	"GET /api/v1/audit-events",
	// Evaluation
	"GET /api/v1/evaluations",
	"POST /api/v1/evaluations",
	"GET /api/v1/evaluations/{id}",
	// Plan templates
	"GET /api/v1/plan-templates",
	"POST /api/v1/plan-templates",
	// Tasks + stream
	"GET /api/v1/tasks/{id}",
	"GET /api/v1/stream",
	// BFF 聚合端点
	"GET /api/v1/workspace/summary",
	"GET /api/v1/workspace/ontologies",
	"GET /api/v1/apps",
	"GET /api/v1/admin/overview",
	"GET /api/v1/admin/roles",
	"POST /api/v1/admin/roles",
	"DELETE /api/v1/admin/roles/{id}",
	"GET /api/v1/admin/permissions",
	"GET /api/v1/admin/role-bindings",
	"POST /api/v1/admin/role-bindings",
	"DELETE /api/v1/admin/role-bindings/{id}",
	// Admin Users
	"GET /api/v1/admin/users",
	"POST /api/v1/admin/users",
	"GET /api/v1/admin/users/{id}",
	"PATCH /api/v1/admin/users/{id}",
	"DELETE /api/v1/admin/users/{id}",
	"POST /api/v1/admin/subjects/{id}/disable",
	// Admin Ontology Categories
	"GET /api/v1/admin/ontology-categories",
	"POST /api/v1/admin/ontology-categories",
	"PATCH /api/v1/admin/ontology-categories/{id}",
	"DELETE /api/v1/admin/ontology-categories/{id}",
	// Admin Organizations
	"GET /api/v1/admin/organizations",
	"POST /api/v1/admin/organizations",
	"PATCH /api/v1/admin/organizations/{id}",
	"DELETE /api/v1/admin/organizations/{id}",
	// Admin model providers
	"GET /api/v1/admin/model-providers",
	"POST /api/v1/admin/model-providers",
	"POST /api/v1/admin/model-providers/probe",
	"GET /api/v1/admin/model-providers/{id}",
	"PATCH /api/v1/admin/model-providers/{id}",
	"DELETE /api/v1/admin/model-providers/{id}",
	"POST /api/v1/admin/model-providers/{id}/endpoints",
	"PATCH /api/v1/admin/model-endpoints/{endpoint_id}",
	"DELETE /api/v1/admin/model-endpoints/{endpoint_id}",
	"GET /api/v1/admin/model-endpoints/{endpoint_id}/models",
	"POST /api/v1/admin/model-endpoints/{endpoint_id}/models",
	"DELETE /api/v1/admin/model-endpoints/{endpoint_id}/models/{model_revision_id}",
	"GET /api/v1/admin/connectors",
	"POST /api/v1/admin/connectors/{id}/test",
	"GET /api/v1/ops/alerts",
	"GET /api/v1/releases",
	"POST /api/v1/releases/{id}/promote",
	"POST /api/v1/releases/{id}/rollback",
	// 模块 A：企业知识问答本体（知识库）
	"POST /api/v1/knowledge/documents",
	"GET /api/v1/knowledge/documents",
	"GET /api/v1/knowledge/documents/{id}",
	"DELETE /api/v1/knowledge/documents/{id}",
	"POST /api/v1/knowledge/jobs/{id}/retry",
	// 模块 C：A2A 多智能体协同（注册表 + JSON-RPC 网关）
	"GET /api/v1/a2a/remote-agents",
	"POST /api/v1/a2a/remote-agents",
	"GET /api/v1/a2a/remote-agents/{id}",
	"PATCH /api/v1/a2a/remote-agents/{id}",
	"DELETE /api/v1/a2a/remote-agents/{id}",
	"POST /api/v1/a2a/remote-agents/{id}/probe",
	"POST /api/v1/a2a",
	// 模块 D：本体 Action 管理（查询侧）
	"GET /api/v1/actions",
	"GET /api/v1/actions/{id}",
	"POST /api/v1/actions/{id}/similar",
	"GET /api/v1/actions/{id}/prov.ttl",
}

func TestAllContractEndpointsRegistered(t *testing.T) {
	h := handlers.New(handlers.Deps{})
	got := map[string]bool{}
	for _, rt := range Routes(h) {
		got[rt.Method+" "+rt.Pattern] = true
	}
	for _, want := range contractEndpoints {
		if !got[want] {
			t.Fatalf("contract endpoint not registered: %s", want)
		}
	}
	if len(got) != len(contractEndpoints) {
		t.Fatalf("route count=%d want=%d", len(got), len(contractEndpoints))
	}
}

// denyResolver 始终拒绝认证，用于验证认证链生效。
type denyResolver struct{}

func (denyResolver) Resolve(context.Context, *http.Request) (tenantctx.Identity, error) {
	return tenantctx.Identity{}, errors.E(errors.CodeUnauthenticated, "no creds")
}

func TestHealthEndpointsPublic(t *testing.T) {
	router := NewRouter(handlers.Deps{}, Options{Resolver: denyResolver{}})
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if path == "/healthz" && rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d want 200", path, rec.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s body not json: %v", path, err)
		}
		if _, ok := body["status"]; !ok {
			t.Fatalf("%s missing status field", path)
		}
	}
}

func TestBusinessEndpointsRequireAuth(t *testing.T) {
	router := NewRouter(handlers.Deps{}, Options{Resolver: denyResolver{}})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not json: %v", err)
	}
	eb, ok := body["error"].(map[string]any)
	if !ok || eb["base_code"] != "UNAUTHENTICATED" {
		t.Fatalf("unexpected error body: %v", body)
	}
	if body["request_id"] == "" || body["request_id"] == nil {
		t.Fatalf("request_id missing")
	}
}

func TestRequestIDHeaderEchoed(t *testing.T) {
	router := NewRouter(handlers.Deps{}, Options{Resolver: denyResolver{}})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Header().Get("X-Request-Id") == "" {
		t.Fatalf("X-Request-Id not echoed")
	}
}
