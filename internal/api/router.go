// Package api 装配 ODAP 对外 HTTP 层：基于 Go 1.22+ 标准库 http.ServeMux 的
// 方法+路径模式路由（不引入任何第三方路由库），统一前缀 /api/v1。
//
// 权威契约：docs/contracts/03-API命名与契约规范.md §2（端点总表）；
// BFF 聚合端点见第十七篇 §11.1（contracts/03 §4 裁决）。
package api

import (
	"log/slog"
	"net/http"

	"odap/internal/api/handlers"
	"odap/internal/api/middleware"
	"odap/internal/reasoning"
)

// Options 是路由装配选项。
type Options struct {
	Logger   *slog.Logger
	Resolver middleware.IdentityResolver
	CORS     middleware.CORSConfig
	// RateLimitCapacity/Refill 为每租户令牌桶参数（<=0 时取默认 60/30）。
	RateLimitCapacity float64
	RateLimitRefill   float64
	// Reasoning 是推理引擎服务（模块 B）；nil 时 /api/v1/reasoning/* 端点
	// 不注册（模块未装配，而非注册后返回 404——装配缺失必须在启动日志明示）。
	Reasoning *reasoning.Service
}

// RouteSpec 是一条路由的声明（供路由测试与 openapi 交叉核对）。
type RouteSpec struct {
	Method  string
	Pattern string
	Handler http.HandlerFunc
	// Public 为 true 表示该端点**不需要会话**（挂在认证链之外，仍需限流）。
	//
	// 该标记必须与路由声明放在一起：早期由 NewRouter 内的 switch 手工枚举
	// "哪些路径是公开的"，并与挂载前缀 `/api/v1/auth/` 组合使用，导致
	// /api/v1/auth/logout 与 /api/v1/auth/session 被更具体的 /api/v1/auth/
	// 前缀吞进 publicMux，而 publicMux 只认识 register/login →
	// 两个已实现且已注册的端点恒定返回纯文本 404（详见实现符合性评测清单 L2）。
	Public bool
}

// routes 是全部业务路由的单一真源：注册与文档核对共用同一份声明。
func routes(h *handlers.Handler) []RouteSpec {
	return []RouteSpec{
		// --- Auth 公开端点（不走 Auth 链）---
		{http.MethodPost, "/api/v1/auth/register", h.Register, true},
		{http.MethodPost, "/api/v1/auth/login", h.Login, true},
		// Auth 端点（走 Auth 链）
		{http.MethodPost, "/api/v1/auth/logout", h.Logout, false},
		{http.MethodGet, "/api/v1/auth/session", h.GetCurrentSession, false},

		// Run 资源（唯一执行资源）
		{http.MethodPost, "/api/v1/runs", h.CreateRun, false},
		{http.MethodGet, "/api/v1/runs", h.ListRuns, false},
		{http.MethodGet, "/api/v1/runs/{id}", h.GetRun, false},
		{http.MethodGet, "/api/v1/runs/{id}/steps", h.ListRunSteps, false},
		{http.MethodPost, "/api/v1/runs/{id}/cancel", h.CancelRun, false},
		{http.MethodPost, "/api/v1/runs/{id}/reconcile", h.ReconcileRun, false},
		{http.MethodPost, "/api/v1/runs/{id}/lease/renew", h.RenewLease, false},

		// Agent
		{http.MethodGet, "/api/v1/agents", h.ListAgents, false},
		{http.MethodPost, "/api/v1/agents", h.CreateAgent, false},
		{http.MethodGet, "/api/v1/agents/{id}", h.GetAgent, false},
		{http.MethodPatch, "/api/v1/agents/{id}", h.UpdateAgent, false},
		{http.MethodDelete, "/api/v1/agents/{id}", h.DeleteAgent, false},
		{http.MethodPost, "/api/v1/agents/{id}/publish", h.PublishAgent, false},
		// Agent 自动生成（读制度文档/需求 → 大模型 → DRAFT）
		{http.MethodPost, "/api/v1/agents/generate", h.GenerateAgent, false},

		// Ontology
		{http.MethodGet, "/api/v1/ontologies", h.ListOntologies, false},
		{http.MethodPost, "/api/v1/ontologies", h.CreateOntology, false},
		// 本体自动生成（读制度文档 → 大模型 → DRAFT）
		{http.MethodPost, "/api/v1/ontologies/generate", h.GenerateOntology, false},
		{http.MethodGet, "/api/v1/ontologies/{id}", h.GetOntology, false},
		{http.MethodPatch, "/api/v1/ontologies/{id}", h.UpdateOntology, false},
		{http.MethodDelete, "/api/v1/ontologies/{id}", h.DeleteOntology, false},
		{http.MethodPost, "/api/v1/ontologies/{id}/validate", h.ValidateOntology, false},
		{http.MethodPost, "/api/v1/ontologies/{id}/publish", h.PublishOntology, false},
		// 本体产物（RDF/OWL/SHACL 文本，由 manifest 派生，无需先校验）
		{http.MethodGet, "/api/v1/ontologies/{id}/artifacts", h.GetOntologyArtifacts, false},

		// Plugin
		{http.MethodGet, "/api/v1/plugins", h.ListPlugins, false},
		{http.MethodPost, "/api/v1/plugins", h.CreatePlugin, false},
		{http.MethodGet, "/api/v1/plugins/{id}", h.GetPlugin, false},
		{http.MethodPatch, "/api/v1/plugins/{id}", h.UpdatePlugin, false},
		{http.MethodDelete, "/api/v1/plugins/{id}", h.DeletePlugin, false},
		{http.MethodPost, "/api/v1/plugins/{id}/enable", h.EnablePlugin, false},

		// Tool
		{http.MethodGet, "/api/v1/tools", h.ListTools, false},
		{http.MethodPost, "/api/v1/tools", h.CreateTool, false},
		{http.MethodGet, "/api/v1/tools/{id}", h.GetTool, false},
		{http.MethodPatch, "/api/v1/tools/{id}", h.UpdateTool, false},
		{http.MethodDelete, "/api/v1/tools/{id}", h.DeleteTool, false},
		{http.MethodPost, "/api/v1/tools/{id}/invoke", h.InvokeTool, false},
		// 工具调用记录与审批门禁（EXTERNAL_WRITE 需审批后才真实执行）
		{http.MethodGet, "/api/v1/tools/{id}/invocations", h.ListToolInvocations, false},
		{http.MethodPost, "/api/v1/tools/{id}/invocations/{inv_id}/approve", h.ApproveToolInvocation, false},
		{http.MethodPost, "/api/v1/tools/{id}/invocations/{inv_id}/reject", h.RejectToolInvocation, false},
		// 工具自动生成（读需求/制度文档 → 大模型 → DRAFT）
		{http.MethodPost, "/api/v1/tools/generate", h.GenerateTool, false},

		// Skill（一等 registry 资源；前端 PermissionGate 沿用 admin.agent.write/publish
		// 权限键，与 Agent 管理同组；服务端与 agents/tools 一致走统一认证链）
		{http.MethodGet, "/api/v1/skills", h.ListSkills, false},
		{http.MethodPost, "/api/v1/skills", h.CreateSkill, false},
		{http.MethodGet, "/api/v1/skills/{id}", h.GetSkill, false},
		{http.MethodPatch, "/api/v1/skills/{id}", h.UpdateSkill, false},
		{http.MethodDelete, "/api/v1/skills/{id}", h.DeleteSkill, false},
		{http.MethodGet, "/api/v1/skills/{id}/versions", h.ListSkillVersions, false},
		{http.MethodPost, "/api/v1/skills/{id}/versions", h.CreateSkillVersion, false},
		{http.MethodPost, "/api/v1/skills/{id}/publish", h.PublishSkill, false},

		// Workflow（插件式：plugin_kind='WORKFLOW'）
		{http.MethodGet, "/api/v1/workflows", h.ListWorkflows, false},
		{http.MethodPost, "/api/v1/workflows", h.CreateWorkflow, false},
		{http.MethodGet, "/api/v1/workflows/{id}", h.GetWorkflow, false},
		{http.MethodPatch, "/api/v1/workflows/{id}", h.UpdateWorkflow, false},
		{http.MethodDelete, "/api/v1/workflows/{id}", h.DeleteWorkflow, false},
		{http.MethodPost, "/api/v1/workflows/{id}/publish", h.PublishWorkflow, false},
		// 工作流自动生成（读需求/制度文档 → 大模型 → DRAFT）
		{http.MethodPost, "/api/v1/workflows/generate", h.GenerateWorkflow, false},

		// Policy
		{http.MethodGet, "/api/v1/policies", h.ListPolicies, false},
		{http.MethodPost, "/api/v1/policies", h.CreatePolicy, false},
		{http.MethodGet, "/api/v1/policies/{id}/versions", h.ListPolicyVersions, false},
		{http.MethodPost, "/api/v1/policies/{id}/versions", h.CreatePolicyVersion, false},
		{http.MethodPatch, "/api/v1/policies/{key}/versions/{version}", h.UpdatePolicyStatus, false},
		{http.MethodDelete, "/api/v1/policies/{key}/versions/{version}", h.DeletePolicyVersion, false},

		// Approval
		{http.MethodGet, "/api/v1/approvals", h.ListApprovals, false},
		{http.MethodGet, "/api/v1/approvals/{id}", h.GetApproval, false},
		{http.MethodPost, "/api/v1/approvals/{id}/decision", h.DecideApproval, false},

		// ERP
		{http.MethodGet, "/api/v1/erp/capabilities", h.ERPCapabilities, false},
		{http.MethodPost, "/api/v1/erp/operations/{id}/reconcile", h.ReconcileEROperation, false},
		{http.MethodGet, "/api/v1/runs/{id}/erp-operations", h.ListRunErpOperations, false},

		// 与ERP集成（ERP 系统字典 + 租户连接配置）
		{http.MethodGet, "/api/v1/admin/erp-systems", h.ListErpSystems, false},
		{http.MethodGet, "/api/v1/admin/erp-connections", h.ListErpConnections, false},
		{http.MethodPost, "/api/v1/admin/erp-connections", h.CreateErpConnection, false},
		{http.MethodPost, "/api/v1/admin/erp-connections/test", h.TestErpConnection, false},
		{http.MethodPatch, "/api/v1/admin/erp-connections/{id}", h.UpdateErpConnection, false},
		{http.MethodDelete, "/api/v1/admin/erp-connections/{id}", h.DeleteErpConnection, false},

		// Audit
		{http.MethodGet, "/api/v1/audit-events", h.ListAuditEvents, false},

		// Evaluation
		{http.MethodGet, "/api/v1/evaluations", h.ListEvaluations, false},
		{http.MethodPost, "/api/v1/evaluations", h.CreateEvaluation, false},
		{http.MethodGet, "/api/v1/evaluations/{id}", h.GetEvaluation, false},

		// Plan templates（资产端点）
		{http.MethodGet, "/api/v1/plan-templates", h.ListPlanTemplates, false},
		{http.MethodPost, "/api/v1/plan-templates", h.CreatePlanTemplate, false},

		// 异步任务状态 + 实时通道
		{http.MethodGet, "/api/v1/tasks/{id}", h.GetTask, false},
		{http.MethodGet, "/api/v1/stream", h.Stream, false},

		// BFF 聚合端点（第十七篇 §11.1）
		{http.MethodGet, "/api/v1/workspace/summary", h.WorkspaceSummary, false},
		{http.MethodGet, "/api/v1/workspace/ontologies", h.WorkspaceOntologies, false},
		{http.MethodGet, "/api/v1/apps", h.Apps, false},
		{http.MethodGet, "/api/v1/admin/overview", h.AdminOverview, false},
		{http.MethodGet, "/api/v1/admin/roles", h.AdminRoles, false},
		{http.MethodPost, "/api/v1/admin/roles", h.AdminCreateRole, false},
		{http.MethodDelete, "/api/v1/admin/roles/{id}", h.AdminDeleteRole, false},
		{http.MethodGet, "/api/v1/admin/permissions", h.AdminPermissions, false},
		{http.MethodGet, "/api/v1/admin/role-bindings", h.AdminRoleBindings, false},
		{http.MethodPost, "/api/v1/admin/role-bindings", h.AdminBindRole, false},
		{http.MethodDelete, "/api/v1/admin/role-bindings/{id}", h.AdminUnbindRole, false},

		// Admin Users
		{http.MethodGet, "/api/v1/admin/users", h.AdminListUsers, false},
		{http.MethodPost, "/api/v1/admin/users", h.AdminCreateUser, false},
		{http.MethodGet, "/api/v1/admin/users/{id}", h.AdminGetUser, false},
		{http.MethodPatch, "/api/v1/admin/users/{id}", h.AdminUpdateUser, false},
		{http.MethodDelete, "/api/v1/admin/users/{id}", h.AdminDeleteUser, false},

		// Admin Subjects（主体停用，§14：status=DISABLED + subject_version 递增撤销传播）
		{http.MethodPost, "/api/v1/admin/subjects/{id}/disable", h.AdminDisableSubject, false},

		// Admin Ontology Categories（管理员）
		{http.MethodGet, "/api/v1/admin/ontology-categories", h.ListOntologyCategories, false},
		{http.MethodPost, "/api/v1/admin/ontology-categories", h.CreateOntologyCategory, false},
		{http.MethodPatch, "/api/v1/admin/ontology-categories/{id}", h.UpdateOntologyCategory, false},
		{http.MethodDelete, "/api/v1/admin/ontology-categories/{id}", h.DeleteOntologyCategory, false},

		// Admin Organizations
		{http.MethodGet, "/api/v1/admin/organizations", h.AdminListOrgs, false},
		{http.MethodPost, "/api/v1/admin/organizations", h.AdminCreateOrg, false},
		{http.MethodPatch, "/api/v1/admin/organizations/{id}", h.AdminUpdateOrg, false},
		{http.MethodDelete, "/api/v1/admin/organizations/{id}", h.AdminDeleteOrg, false},
		{http.MethodGet, "/api/v1/admin/model-providers", h.AdminModelProviders, false},
		{http.MethodPost, "/api/v1/admin/model-providers", h.CreateModelProvider, false},
		// 录入前连通性探测（真实试调一次，不落库、不写账本）
		{http.MethodPost, "/api/v1/admin/model-providers/probe", h.ProbeModelProvider, false},
		{http.MethodGet, "/api/v1/admin/model-providers/{id}", h.GetModelProvider, false},
		{http.MethodPatch, "/api/v1/admin/model-providers/{id}", h.UpdateModelProvider, false},
		{http.MethodDelete, "/api/v1/admin/model-providers/{id}", h.DeleteModelProvider, false},
		{http.MethodPost, "/api/v1/admin/model-providers/{id}/endpoints", h.AddModelEndpoint, false},
		{http.MethodPatch, "/api/v1/admin/model-endpoints/{endpoint_id}", h.UpdateModelEndpoint, false},
		{http.MethodDelete, "/api/v1/admin/model-endpoints/{endpoint_id}", h.DeleteModelEndpoint, false},
		// 模型版本登记（写 llm.model_revisions + 内容寻址的能力档案 llm.capability_profiles）
		{http.MethodGet, "/api/v1/admin/model-endpoints/{endpoint_id}/models", h.ListModelRevisions, false},
		{http.MethodPost, "/api/v1/admin/model-endpoints/{endpoint_id}/models", h.RegisterModelRevision, false},
		{http.MethodDelete, "/api/v1/admin/model-endpoints/{endpoint_id}/models/{model_revision_id}", h.DeleteModelRevision, false},
		{http.MethodGet, "/api/v1/admin/connectors", h.AdminConnectors, false},
		{http.MethodPost, "/api/v1/admin/connectors/{id}/test", h.TestConnector, false},
		{http.MethodGet, "/api/v1/ops/alerts", h.OpsAlerts, false},
		{http.MethodGet, "/api/v1/releases", h.Releases, false},
		{http.MethodPost, "/api/v1/releases/{id}/promote", h.PromoteRelease, false},
		{http.MethodPost, "/api/v1/releases/{id}/rollback", h.RollbackRelease, false},

		// --- 模块 A：企业知识问答本体（知识库） ---
		{http.MethodPost, "/api/v1/knowledge/documents", h.CreateKnowledgeDocument, false},
		{http.MethodGet, "/api/v1/knowledge/documents", h.ListKnowledgeDocuments, false},
		{http.MethodGet, "/api/v1/knowledge/documents/{id}", h.GetKnowledgeDocument, false},
		{http.MethodDelete, "/api/v1/knowledge/documents/{id}", h.DeleteKnowledgeDocument, false},
		{http.MethodPost, "/api/v1/knowledge/jobs/{id}/retry", h.RetryKnowledgeJob, false},

		// --- 模块 C：A2A 多智能体协同（注册表 + JSON-RPC 网关；Agent Card
		// 挂认证链外的根路径，见 NewRouter）---
		{http.MethodGet, "/api/v1/a2a/remote-agents", h.ListRemoteAgents, false},
		{http.MethodPost, "/api/v1/a2a/remote-agents", h.CreateRemoteAgent, false},
		{http.MethodGet, "/api/v1/a2a/remote-agents/{id}", h.GetRemoteAgent, false},
		{http.MethodPatch, "/api/v1/a2a/remote-agents/{id}", h.UpdateRemoteAgent, false},
		{http.MethodDelete, "/api/v1/a2a/remote-agents/{id}", h.DeleteRemoteAgent, false},
		{http.MethodPost, "/api/v1/a2a/remote-agents/{id}/probe", h.ProbeRemoteAgent, false},
		{http.MethodPost, "/api/v1/a2a", h.A2AGateway, false},

		// --- 模块 D：本体 Action 管理（查询侧；记录由各业务流旁路写入）---
		{http.MethodGet, "/api/v1/actions", h.ListActions, false},
		{http.MethodGet, "/api/v1/actions/{id}", h.GetAction, false},
		{http.MethodPost, "/api/v1/actions/{id}/similar", h.SimilarActions, false},
		{http.MethodGet, "/api/v1/actions/{id}/prov.ttl", h.ActionProvTurtle, false},
	}
}

// Routes 返回业务路由声明（不含 /healthz、/readyz）。
func Routes(h *handlers.Handler) []RouteSpec { return routes(h) }

// NewRouter 装配完整 HTTP 处理链。
//
// 链顺序（由外到内）：
//
//	RequestID → Recover → Logging → CORS → [公开 auth 端点] → RateLimit → Auth → RequireIdentity → mux
//
// /healthz 与 /readyz 为公开端点；routes() 中标记 Public 的端点
// （当前为 /api/v1/auth/register 与 /api/v1/auth/login）同样不要求会话；
// 其余 /api/v1 端点一律要求已认证身份并带租户上下文。
//
// 装配规则（防前缀遮蔽）：
//   - Public 路由按**完整 (方法, 路径)** 逐条注册到 root，确保不会遮蔽同一
//     子树下需要认证的兄弟端点；
//   - 其余全部注册进受保护 mux，以 /api/v1/ 子树整体挂载。
func NewRouter(deps handlers.Deps, opts Options) http.Handler {
	h := handlers.New(deps)
	rs := routes(h)

	cap, refill := opts.RateLimitCapacity, opts.RateLimitRefill
	if cap <= 0 {
		cap = 60
	}
	if refill <= 0 {
		refill = 30
	}

	// --- 受保护子树：routes() 中所有非 Public 路由 ---
	apiMux := http.NewServeMux()
	for _, rt := range rs {
		if rt.Public {
			continue
		}
		apiMux.HandleFunc(rt.Method+" "+rt.Pattern, rt.Handler)
	}
	// 推理引擎（模块 B）：独立 handler 结构体（持有 *reasoning.Service），
	// 服务未装配时不注册路由，并在启动日志明示（fail-closed 于装配期暴露）。
	if opts.Reasoning != nil {
		rh := handlers.NewReasoningHandler(opts.Reasoning).SetBase(h)
		for _, rt := range []RouteSpec{
			{http.MethodGet, "/api/v1/reasoning/rules", rh.ListRules, false},
			{http.MethodPost, "/api/v1/reasoning/rules", rh.CreateRule, false},
			{http.MethodGet, "/api/v1/reasoning/rules/{id}", rh.GetRule, false},
			{http.MethodPatch, "/api/v1/reasoning/rules/{id}", rh.PatchRule, false},
			{http.MethodDelete, "/api/v1/reasoning/rules/{id}", rh.DeleteRule, false},
			{http.MethodPost, "/api/v1/reasoning/runs", rh.CreateRun, false},
			{http.MethodGet, "/api/v1/reasoning/runs", rh.ListRuns, false},
		} {
			apiMux.HandleFunc(rt.Method+" "+rt.Pattern, rt.Handler)
		}
	}
	var apiChain http.Handler = apiMux
	apiChain = middleware.RequireIdentity(apiChain)
	apiChain = middleware.Auth(opts.Resolver)(apiChain)
	apiChain = middleware.RateLimit(cap, refill, nil)(apiChain)

	// --- 公开端点：routes() 中标记 Public 的路由 ---
	// 必须**逐条按完整 (方法, 路径) 注册**，不能合并成一个更宽的前缀挂载：
	// 否则同一子树上未标记 Public 的兄弟端点会被前缀吞掉（本文件此前的
	// /api/v1/auth/ 前缀就吞掉了 logout 与 session）。
	// Go 1.22+ ServeMux 的最具体匹配规则保证 METHOD+精确路径优先于 /api/v1/ 子树。
	publicChain := middleware.RateLimit(cap, refill, nil)

	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", h.Healthz)
	root.HandleFunc("GET /readyz", h.Readyz)
	// A2A Agent Card（协议标准发现路径 /.well-known/agent.json）：卡片内容与
	// 租户绑定，因此挂**同一条认证链**（外部 Agent 用 Bearer Token 发现），
	// 而非完全公开——泄露租户资产清单比多一步认证更危险。
	root.Handle("GET /.well-known/agent.json", publicChain(
		middleware.Auth(opts.Resolver)(
			middleware.RequireIdentity(http.HandlerFunc(h.AgentCard)))))
	for _, rt := range rs {
		if !rt.Public {
			continue
		}
		root.Handle(rt.Method+" "+rt.Pattern, publicChain(rt.Handler))
	}
	root.Handle("/api/v1/", apiChain)

	var chain http.Handler = root
	chain = middleware.CORS(opts.CORS)(chain)
	chain = middleware.Logging(opts.Logger)(chain)
	chain = middleware.Recover(opts.Logger)(chain)
	chain = middleware.RequestID(chain)
	return chain
}
