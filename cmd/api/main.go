// cmd/api 是 ODAP 平台 API 进程入口。
//
// 用法：
//
//	go run ./cmd/api                 # 启动 HTTP API
//	go run ./cmd/api serve           # 同上
//	go run ./cmd/api seed --dataset finance-demo   # 导入演示数据（幂等）
//	go run ./cmd/api seed --dataset finance-demo --replace  # 重建演示数据（清除本数据集声明过的行）
//	go run ./cmd/api llm-check       # 模型调用链端到端自检（含落账回查）
//	go run ./cmd/api graphstore-check  # 图存储（Fuseki/TDB2 或 PostgreSQL）端到端自检
//
// 环境变量：ODAP_DATABASE_URL（或 DATABASE_URL）、ODAP_HTTP_ADDR（或 HTTP_ADDR，
// 默认 :8080）、APP_ENV、LOG_LEVEL、MOCK_ERP_BASE_URL、ODAP_ERP_CONNECTION_ID、
// GRAPHSTORE_MODE（postgres|fuseki，默认 postgres）、FUSEKI_ENDPOINT、
// FUSEKI_USERNAME、FUSEKI_PASSWORD、FUSEKI_TIMEOUT_SECONDS。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/api"
	"odap/internal/api/handlers"
	"odap/internal/api/middleware"
	"odap/internal/api/sse"
	"odap/internal/api/store"
	"odap/internal/audit"
	"odap/internal/erp"
	"odap/internal/erp/adapters/mock"
	"odap/internal/erp/adapters/nc65"
	"odap/internal/execution/outbox"
	"odap/internal/harness"
	"odap/internal/identity"
	"odap/internal/ontology/graphstore"
	"odap/internal/platform/clock"
	"odap/internal/platform/ids"
	"odap/internal/platform/logging"
	"odap/internal/platform/pgxutil"
	"odap/internal/platform/secretbox"
	"odap/internal/platform/tenantctx"
	seedpkg "odap/internal/seed"
)

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
		args = args[1:]
	}
	switch cmd {
	case "serve":
		if err := serve(); err != nil {
			fmt.Fprintln(os.Stderr, "api:", err)
			os.Exit(1)
		}
	case "seed":
		if err := seed(args); err != nil {
			fmt.Fprintln(os.Stderr, "api seed:", err)
			os.Exit(1)
		}
	case "llm-check":
		if err := llmCheck(args); err != nil {
			fmt.Fprintln(os.Stderr, "api llm-check:", err)
			os.Exit(1)
		}
	case "graphstore-check":
		if err := graphStoreCheck(args); err != nil {
			fmt.Fprintln(os.Stderr, "api graphstore-check:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: api [serve|seed [--dataset finance-demo] [--replace]|llm-check [--agent KEY]|graphstore-check [--tenant UUID] [--graph IRI]]")
		os.Exit(2)
	}
}

// env 读取环境变量（带默认值）。
func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envFirst 返回第一个非空环境变量。
func envFirst(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// databaseURL 解析数据库连接串（ODAP_DATABASE_URL 优先，DATABASE_URL 兜底）。
func databaseURL() string {
	return envFirst("ODAP_DATABASE_URL", "DATABASE_URL")
}

// serve 启动 HTTP API 进程。
func serve() error {
	logger := logging.New(env("LOG_LEVEL", "info"))
	addr := env("ODAP_HTTP_ADDR", env("HTTP_ADDR", ":8080"))
	appEnv := env("APP_ENV", "development")

	dbURL := databaseURL()
	if dbURL == "" {
		return fmt.Errorf("ODAP_DATABASE_URL (or DATABASE_URL) is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := pgxutil.Connect(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	logger.Info("database connected")

	// ---- LLM 端点自配置（实战 HTTP provider）----
	// 如果用户通过环境变量指定了真实端点，覆盖 llm.endpoints 里的 base_url / credential_ref
	// 并自动启用到 ACTIVE。这一步在数据库连接之后、HTTP server 之前执行。
	if err := configureLLMEndpoints(ctx, pool, logger); err != nil {
		logger.Error("LLM 端点自配置失败（不阻断启动，但 HTTP provider 可能不可用）", "error", err)
	}

	clk := clock.RealClock{}
	owner := workerOwner("api")
	hub := sse.NewHub(1024)

	// ---- 凭据加密主密钥（模型端点密文凭据）----
	// 界面录入 api-key 时由 store 层加密落库、装配期解密还原，供解密用的
	// 主密钥来自环境变量。未配置（返回 nil）时该路径 fail-closed（503），
	// 而 credential_ref 环境变量引用路径不受影响；主密钥非法（长度/编码错）
	// 则启动失败——绝不带着一个"看似能加密"的坏密钥继续跑。
	secretCipher, err := secretbox.NewFromEnv(os.Getenv)
	if err != nil {
		return fmt.Errorf("凭据主密钥 ODAP_SECRET_MASTER_KEY 无效：%w", err)
	}
	if secretCipher == nil {
		logger.Warn("未配置 ODAP_SECRET_MASTER_KEY：界面录入 api-key 将不可用（fail-closed 返回 503），仅 credential_ref 路径可用")
	}

	// ---- 图存储装配（第十三篇 §5.3 / docs/图数据库的选型.md / ADR-005）----
	// 唯一装配入口是 graphstore.Open；配置非法即启动失败（fail-closed），
	// **绝不静默回退**到另一个后端——"已启用图数据库"必须是可核实的声明，
	// 把 fuseki 拼错却悄悄跑在 PostgreSQL 上不算达标。
	gsCfg, err := graphstore.EnvConfigFromOS()
	if err != nil {
		return err
	}
	gsCfg.Pool = pool
	graphStore, err := graphstore.Open(gsCfg)
	if err != nil {
		return err
	}
	caps := graphStore.Capabilities()
	logger.Info("graph store configured",
		"backend", string(gsCfg.Backend),
		"resource_dictionary", caps.ResourceDictionary,
		"persistent_graph_id", caps.PersistentGraphID,
		"fact_metadata", caps.FactMetadata)

	sessionSvc := identity.NewSessionService(pool, clk)
	deps := handlers.Deps{
		Store:     store.New(pool, store.WithCipher(secretCipher)),
		Machine:   &harness.Machine{Pool: pool, Clock: clk},
		Scheduler: &harness.Scheduler{Pool: pool, Clock: clk, Owner: owner, LeaseTTL: 30 * time.Second},
		Outbox:    &outbox.Writer{Pool: pool},
		Audit:     &audit.Recorder{Pool: pool, Clock: clk},
		Events:    hub,
		Clock:     clk,
		Logger:    logger,
		ReadyFunc: readinessProbe(pool, graphStore),
		// 本体校验的数据面：POST /ontologies/{id}/validate 需要用同一份图存储
		// 装载 TBox/ABox 并执行 SHACL 校验。与 readinessProbe 共用实例，
		// 避免装配出第二份图存储客户端（历史上"第二份实现"曾导致租户隔离静默失效）。
		GraphStore: graphStore,
		// ERP 操作账本读面（任务详情「外部操作与对账」）。
		ErpStore:       erp.NewPGStore(pool),
		Version:        "1.0.0",
		SessionService: sessionIssuerAdapter{svc: sessionSvc},
		// 生产环境会话 Cookie 带 Secure（需 HTTPS）；开发/演示走明文 http，
		// 必须为 false，否则浏览器不会保存 Cookie，实时通道（SSE）将持续 401。
		SecureCookies: appEnv != "development",
	}
	// 生成类端点（本体/Agent/工具/工作流自动生成）使用独立网关：
	// 只接受真实 HTTP 端点，避免落到 MockProvider 产出伪造内容。
	// 注意：*llm.Gateway 为 nil 时不能赋给接口（会得到非 nil 接口包裹 nil 指针），
	// 因此显式判空后再赋值；未装配时生成端点 fail-closed。
	if gw := buildGenerationGateway(ctx, pool, logger, secretCipher); gw != nil {
		deps.LLM = gw
	}
	deps.DocsRoot = env("ODAP_DOCS_ROOT", "docs/资料")
	// 上传文档落盘根目录（AI 自动生成的本地文件来源）。与只读的制度文档语料目录
	// 分离，否则用户上传会写到 :ro 挂载上并直接失败。
	deps.UploadsRoot = env("ODAP_UPLOAD_ROOT", "var/uploads")

	// ERP 集成：优先接入真实用友 NC65；只有显式配置了 MOCK_ERP_BASE_URL
	// 时才装配 Mock ERP（绝不作为默认兜底）。两者都未配置时 ERP 保持未装配，
	// 相关端点 fail-closed 返回 UNAVAILABLE 并说明原因。
	if cfg, enabled, cerr := nc65.ConfigFromEnv(os.Getenv); cerr != nil {
		logger.Error("NC65 Adapter 配置无效，ERP 保持未装配（fail-closed）", "error", cerr)
	} else if enabled {
		ad, nerr := nc65.New(cfg)
		if nerr != nil {
			logger.Error("NC65 Adapter 装配失败，ERP 保持未装配（fail-closed）", "error", nerr)
		} else {
			deps.ERP = ad
			deps.ERPConnectionID = env("ODAP_ERP_CONNECTION_ID", "nc65")
			logger.Info("ERP adapter configured", "adapter", nc65.AdapterID, "base_url", cfg.BaseURL,
				"objects", len(cfg.Objects), "operations", len(cfg.Operations))
		}
	} else if baseURL := env("MOCK_ERP_BASE_URL", ""); baseURL != "" {
		deps.ERP = mock.NewHTTPAdapter(baseURL, &http.Client{Timeout: 10 * time.Second})
		deps.ERPConnectionID = env("ODAP_ERP_CONNECTION_ID", "default")
		logger.Info("ERP adapter configured", "adapter", "mock-erp", "base_url", baseURL)
	}

	resolver := buildResolver(pool, clk, appEnv, logger)

	corsCfg := middleware.CORSConfig{}
	if appEnv == "development" {
		corsCfg = middleware.DefaultDevCORS()
	}

	router := api.NewRouter(deps, api.Options{
		Logger:   logger,
		Resolver: resolver,
		CORS:     corsCfg,
	})

	// 开发环境：启动 Outbox→SSE 桥接，使实时通道有真实事件来源。
	bridgeCtx, cancelBridge := context.WithCancel(ctx)
	defer cancelBridge()
	if appEnv == "development" {
		go runOutboxBridge(bridgeCtx, pool, hub, owner, logger)
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("api listening", "addr", addr, "env", appEnv)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		return err
	}
	logger.Info("api stopped")
	return nil
}

// multiResolver 依次尝试多个身份解析器（开发环境：调试头 → 会话）。
type multiResolver struct {
	resolvers []middleware.IdentityResolver
}

// Resolve 实现 middleware.IdentityResolver。
func (m multiResolver) Resolve(ctx context.Context, r *http.Request) (tenantctx.Identity, error) {
	var lastErr error
	for _, res := range m.resolvers {
		id, err := res.Resolve(ctx, r)
		if err == nil {
			return id, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no identity resolver configured")
	}
	return tenantctx.Identity{}, lastErr
}

// buildResolver 按环境装配身份解析器。
//
//   - development：调试头（便于本地联调）+ 会话令牌；
//   - 其它环境：仅会话令牌（Bearer / odap_session Cookie）。
func buildResolver(pool *pgxpool.Pool, clk clock.Clock, appEnv string, logger *slog.Logger) middleware.IdentityResolver {
	session := middleware.SessionResolver{Validator: identity.NewSessionService(pool, clk)}
	if appEnv == "development" {
		logger.Warn("development auth enabled: X-Odap-Dev-* headers accepted (never use in production)")
		return multiResolver{resolvers: []middleware.IdentityResolver{middleware.DevResolver{}, session}}
	}
	return session
}

// readinessProbe 组合数据库与图存储的探针。
//
// 为什么必须组合而不是只用数据库探针：图存储不是可选加速器，而是本体与语义层的
// 数据载体。若它不可用而 /readyz 仍返回 200，编排系统会把流量继续送到一个无法完成
// 本体验证与语义查询的实例上——这正是 §10.3 发布硬门禁中的"把未知状态报告为成功"。
//
// 只有提供独立探针的后端（Fuseki）才会被额外探测；PG 后端的图数据就在同一个连接池
// 里，池探针已覆盖。
func readinessProbe(pool *pgxpool.Pool, gs graphstore.GraphStore) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := pool.Ping(ctx); err != nil {
			return fmt.Errorf("database: %w", err)
		}
		if pinger, ok := gs.(interface{ Ping(context.Context) error }); ok {
			if err := pinger.Ping(ctx); err != nil {
				return fmt.Errorf("graph store: %w", err)
			}
		}
		return nil
	}
}

// workerOwner 生成进程唯一标识（pod/主机:pid）。
func workerOwner(prefix string) string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "localhost"
	}
	return fmt.Sprintf("%s:%s:%d", prefix, host, os.Getpid())
}

// runOutboxBridge 周期性把 Outbox 中待发布事件扇出为 SSE 事件（开发环境）。
func runOutboxBridge(ctx context.Context, pool *pgxpool.Pool, hub *sse.Hub, owner string, logger *slog.Logger) {
	pub := &outbox.Publisher{
		Pool:  pool,
		Clock: clock.RealClock{},
		Sink:  sse.OutboxSink{Hub: hub, CriticalityFor: sse.EventTypeCriticality},
		Owner: owner,
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tenants, err := activeTenantIDs(ctx, pool)
			if err != nil {
				logger.Warn("outbox bridge: list tenants failed", "error", err)
				continue
			}
			for _, tid := range tenants {
				if _, err := pub.RunOnce(ctx, tid); err != nil {
					logger.Warn("outbox bridge: publish failed", "error", err)
				}
				if _, err := pub.ReclaimExpired(ctx, tid); err != nil {
					logger.Warn("outbox bridge: reclaim failed", "error", err)
				}
			}
		}
	}
}

// activeTenantIDs 列出 ACTIVE 租户（iam.tenants 无 RLS，平台级查询）。
// sessionIssuerAdapter 把 identity.SessionService 适配为 handlers.SessionIssuer。
type sessionIssuerAdapter struct {
	svc *identity.SessionService
}

func (a sessionIssuerAdapter) Issue(ctx context.Context, tenantID, subjectID ids.ID, subjectType, authMethod string, ttl, refreshTTL time.Duration) (handlers.IssuedSession, error) {
	s, err := a.svc.Issue(ctx, tenantID, subjectID, subjectType, authMethod, ttl, refreshTTL)
	if err != nil {
		return handlers.IssuedSession{}, err
	}
	return handlers.IssuedSession{
		SessionID:        s.ID.String(),
		TenantID:         s.TenantID.String(),
		SubjectID:        s.SubjectID.String(),
		SubjectVersion:   s.SubjectVersion,
		ExpiresAt:        s.ExpiresAt,
		RefreshExpiresAt: s.RefreshExpiresAt,
		RefreshPlain:     s.RefreshTokenPlaintext,
	}, nil
}

func activeTenantIDs(ctx context.Context, pool *pgxpool.Pool) ([]ids.ID, error) {
	rows, err := pool.Query(ctx, `SELECT id::text FROM iam.tenants WHERE status = 'ACTIVE'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ids.ID
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		id, err := ids.Parse(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// seed --dataset finance-demo（幂等）
// ---------------------------------------------------------------------------

// seed 从 datasets/<name>/ 读取数据文件并写入数据库。
//
// 数据不在此处硬编码：实体定义位于数据文件（dataset.json 及同名分节文件），
// 由 internal/seed 负责加载、校验与写入。本函数只做参数解析与依赖装配。
func seed(args []string) error {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	dataset := fs.String("dataset", "finance-demo", "dataset name")
	root := fs.String("dir", "datasets", "dataset root directory")
	// --replace 只清除"本数据集声明过的 ID"，不做任何表级 TRUNCATE。
	// 用途：演示数据迭代时已发布版本不可变，改文件重跑会被摘要校验拦下，
	// 该开关提供一条显式的重建路径。
	replace := fs.Bool("replace", false, "先清除本数据集声明过的行再写入（演示数据重建）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	logger := logging.New(env("LOG_LEVEL", "info"))

	dir := filepath.Join(*root, *dataset)
	ds, err := seedpkg.Load(dir)
	if err != nil {
		return err
	}

	dbURL := databaseURL()
	if dbURL == "" {
		return fmt.Errorf("ODAP_DATABASE_URL (or DATABASE_URL) is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, err := pgxutil.Connect(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	applier := &seedpkg.Applier{Pool: pool, Logger: logger, Replace: *replace}
	if *replace {
		logger.Warn("seed --replace：将先删除本数据集声明过的行，再重新写入", "dataset", ds.Name)
	}
	if err := applier.Apply(ctx, ds); err != nil {
		return err
	}

	logger.Info("seed complete",
		"dataset", ds.Name,
		"version", ds.Version,
		"tenant_id", ds.Tenant.ID,
		"users", len(ds.Tenant.Users),
		"ontologies", len(ds.Ontologies),
		"agents", len(ds.Agents),
		"tools", len(ds.Tools),
		"providers", len(ds.Providers),
		"plans", len(ds.Plans),
		"runs", len(ds.Runs))
	return nil
}

// ---------------------------------------------------------------------------
// configureLLMEndpoints —— 实战 HTTP provider 自配置
// ---------------------------------------------------------------------------
//
// 触发条件（任一）：
//
//	ODAP_LLM_BASE_URL  非空 → 覆盖 llm.endpoints.base_url 并启用 ACTIVE
//	ODAP_LLM_API_KEY   非空 → 同时把 credential_ref 指向该 env 变量名
//
// 作用范围：所有 deployment_type='HTTP' 的端点（不碰 MOCK 端点）。
//
// 为什么不把凭据写进数据库：
//
//	newProvider 从 getenv(b.CredentialRef) 取凭据（registry.go:413），
//	进程只存 env 变量名，密钥本身永远不落盘（第九篇 §4.4 凭据治理）。
func configureLLMEndpoints(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) error {
	baseURL := strings.TrimRight(env("ODAP_LLM_BASE_URL", ""), "/")
	credentialRef := env("ODAP_LLM_API_KEY_REF", "")
	apiKey := env("ODAP_LLM_API_KEY", "")

	// 没有任何 HTTP 配置 → 跳过（不是错误，用户可能只跑 MOCK）
	if baseURL == "" && apiKey == "" {
		return nil
	}

	// 凭据解析优先级：
	//   ODAP_LLM_API_KEY_REF 显式指定 env 变量名（灵活）
	//   否则默认 ODAP_LLM_API_KEY
	if credentialRef == "" {
		credentialRef = "ODAP_LLM_API_KEY"
	}

	// 如果 base_url 没显式配，但 API key 配了，也尝试启用——说明用户想启用真实端点，
	// 用 seed 文件里的默认 base_url（Ollama 本地）作为兜底。
	if baseURL == "" {
		logger.Info("未指定 ODAP_LLM_BASE_URL，使用 seed 默认 HTTP 端点 base_url")
	}

	// 先查有哪些 HTTP 端点，打印诊断
	rows, err := pool.Query(ctx, `
		SELECT endpoint_id::text, endpoint_name, base_url, credential_ref, status
		  FROM llm.endpoints
		 WHERE deployment_type = 'HTTP'
		 ORDER BY endpoint_name`)
	if err != nil {
		return fmt.Errorf("查询 HTTP 端点失败: %w", err)
	}
	defer rows.Close()

	type epRow struct {
		id, name, baseURL, credRef, status string
	}
	var eps []epRow
	for rows.Next() {
		var e epRow
		if err := rows.Scan(&e.id, &e.name, &e.baseURL, &e.credRef, &e.status); err != nil {
			return err
		}
		eps = append(eps, e)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(eps) == 0 {
		logger.Warn("数据库中没有任何 HTTP 端点（请先执行 seed 导入 finance-demo 数据集）")
		return nil
	}

	updates := 0
	for _, e := range eps {
		newBaseURL := e.baseURL
		newCredRef := e.credRef
		newStatus := e.status

		needBase := baseURL != "" && baseURL != e.baseURL
		needCred := credentialRef != "" && credentialRef != e.credRef
		needActivation := e.status != "ACTIVE"

		if !needBase && !needCred && !needActivation {
			continue
		}
		if needBase {
			newBaseURL = baseURL
		}
		if needCred {
			newCredRef = credentialRef
		}
		if needActivation {
			newStatus = "ACTIVE"
		}

		tag, err := pool.Exec(ctx, `
			UPDATE llm.endpoints
			   SET base_url = $1, credential_ref = $2, status = $3
			 WHERE endpoint_id = $4`,
			newBaseURL, newCredRef, newStatus, e.id)
		if err != nil {
			logger.Error("更新端点失败", "endpoint", e.name, "error", err)
			continue
		}
		if tag.RowsAffected() > 0 {
			updates++
			logger.Info("HTTP 端点已配置",
				"endpoint", e.name,
				"base_url", newBaseURL,
				"credential_ref", newCredRef,
				"status", newStatus)
		}
	}
	logger.Info("LLM 端点自配置完成", "updated_endpoints", updates, "has_api_key", apiKey != "")
	return nil
}
