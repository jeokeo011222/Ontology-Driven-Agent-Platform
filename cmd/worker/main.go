// cmd/worker 是 ODAP 平台执行 Worker 进程入口。
//
// 职责（第六篇 §6.2/§6.3）：按四类调度队列（执行/恢复/对账/取消收敛）原子领取
// Run，经 harness.Machine 在租约 fencing 下推进状态，并通过 transactional
// outbox 发布事件；对绑定了 Agent 版本的 Run，由步进执行器（step_executor.go）
// 真实推进一次执行：READ 工具真实执行 + 受治理模型调用（用量入账）+ 证据落库。
//
// 环境变量：ODAP_DATABASE_URL（或 DATABASE_URL）、WORKER_CONCURRENCY（默认 4）、
// WORKER_LEASE_SECONDS（默认 30）、LOG_LEVEL、ODAP_SECRET_MASTER_KEY（模型端点
// 密钥解密；未配置时 credential_ref 路径仍可用）、GRAPHSTORE_*（SHACL 工具需要）、
// MOCK_ERP_BASE_URL / ODAP_NC65_*（ERP 类工具需要；未配置时相应工具 fail-closed）、
// WORKER_INGEST_CONCURRENCY（知识摄取并发，默认 2）、ODAP_KB_EMBEDDING_MODEL
// （知识分块嵌入点名模型；未配置时 EMBED 任务执行期 fail-closed）、
// ODAP_UPLOAD_ROOT（仅审计溯源提示；分块内容读自 kb.chunks，不依赖该卷）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/action"
	"odap/internal/agent"
	"odap/internal/api/handlers"
	"odap/internal/api/store"
	"odap/internal/audit"
	"odap/internal/erp/adapters/mock"
	"odap/internal/erp/adapters/nc65"
	"odap/internal/evidence"
	"odap/internal/execution/outbox"
	"odap/internal/harness"
	"odap/internal/knowledge"
	"odap/internal/llm"
	"odap/internal/ontology/graphstore"
	"odap/internal/platform/clock"
	"odap/internal/platform/ids"
	"odap/internal/platform/logging"
	"odap/internal/platform/pgxutil"
	"odap/internal/platform/runstate"
	"odap/internal/platform/secretbox"
	"odap/internal/registry"
	"odap/internal/toolruntime"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		os.Exit(1)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func run() error {
	logger := logging.New(env("LOG_LEVEL", "info"))

	dbURL := env("ODAP_DATABASE_URL", env("DATABASE_URL", ""))
	if dbURL == "" {
		return fmt.Errorf("ODAP_DATABASE_URL (or DATABASE_URL) is required")
	}
	concurrency := envInt("WORKER_CONCURRENCY", 4)
	if concurrency < 1 {
		concurrency = 1
	}
	leaseSeconds := envInt("WORKER_LEASE_SECONDS", 30)
	if leaseSeconds < 1 {
		leaseSeconds = 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := pgxutil.Connect(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	clk := clock.RealClock{}
	host, _ := os.Hostname()
	base := fmt.Sprintf("worker:%s:%d", host, os.Getpid())

	// ---- 模型端点密钥（可选）：未配置主密钥时界面录入的 api-key 不可解，
	// credential_ref 环境变量引用路径不受影响（与 cmd/api 同一 fail-closed 语义）。
	secretCipher, err := secretbox.NewFromEnv(os.Getenv)
	if err != nil {
		return fmt.Errorf("凭据主密钥 ODAP_SECRET_MASTER_KEY 无效：%w", err)
	}
	if secretCipher == nil {
		logger.Warn("未配置 ODAP_SECRET_MASTER_KEY：密文 credential 引用将无法解密（fail-closed）")
	}

	// ---- 模型网关（与 cmd/api 共用唯一装配实现；只接受真实 HTTP 端点）----
	// 为 nil 时步进执行器对模型步骤 fail-closed（RunFailed 并说明原因）。
	gw := llm.BuildGenerationGateway(ctx, pool, logger, secretCipher)

	// ---- 图存储（可选）：SHACL 类工具执行需要；未配置时该类工具 UNAVAILABLE。
	gsCfg, gerr := graphstore.EnvConfigFromOS()
	if gerr != nil {
		return gerr
	}
	gsCfg.Pool = pool
	graphStore, gerr := graphstore.Open(gsCfg)
	if gerr != nil {
		return gerr
	}

	// ---- 工具执行依赖：复用 handlers.Handler 的真实执行器（唯一实现）----
	st := store.New(pool, store.WithCipher(secretCipher))
	// 模块 D：动作记录器（与 cmd/api 同装配语义）。嵌入桥接网关 Embed
	//（点名 ODAP_KB_EMBEDDING_MODEL）；未配置嵌入模型时所有动作 embed_status=SKIPPED。
	kbEmbeddingModel := env("ODAP_KB_EMBEDDING_MODEL", "")
	var kbEmbedFn action.EmbedFn
	if gw != nil && kbEmbeddingModel != "" {
		kbEmbedFn = func(ctx context.Context, tenantID ids.ID, texts []string) ([][]float32, error) {
			resp, eerr := gw.Embed(ctx, tenantID, llm.EmbeddingRequest{
				TenantID: tenantID, ModelID: kbEmbeddingModel,
				Texts: texts, IdempotencyKey: ids.New().String(),
			})
			if eerr != nil {
				return nil, eerr
			}
			return resp.Vectors, nil
		}
	}
	actRecorder := action.NewRecorder(pool, graphStore, kbEmbedFn, kbEmbeddingModel)
	execHandler := handlers.New(handlers.Deps{
		Store:      st,
		Audit:      &audit.Recorder{Pool: pool, Clock: clk},
		Logger:     logger,
		DocsRoot:   env("ODAP_DOCS_ROOT", "docs/资料"),
		GraphStore: graphStore,
		Catalog:    registry.NewCatalogService(pool),
		ToolCache:  toolruntime.NewResultCache(toolruntime.DefaultTTL),
		Actions:    actRecorder,
	})
	// ERP 适配器（可选）：配置了 MOCK_ERP_BASE_URL 或 NC65 时装配（与 cmd/api 同规则）。
	if baseURL := env("MOCK_ERP_BASE_URL", ""); baseURL != "" {
		execHandler.SetERP(mock.NewHTTPAdapter(baseURL, &http.Client{Timeout: 10 * time.Second}), "default")
	} else if cfg, enabled, cerr := nc65.ConfigFromEnv(os.Getenv); cerr == nil && enabled {
		if ad, aerr := nc65.New(cfg); aerr == nil {
			execHandler.SetERP(ad, "nc65")
		}
	}

	executor := &stepExecutor{
		pool:    pool,
		machine: &harness.Machine{Pool: pool, Clock: clk},
		handler: execHandler,
		st:      st,
		catalog: registry.NewCatalogService(pool),
		// 技能展开：dependency_manifest.skills → 提示词小节（agent 包唯一生产接线点）。
		skills: agent.SkillLoaderFunc(func(ctx context.Context, tenantID ids.ID, keys []string) ([]agent.SkillSnapshot, error) {
			snaps, err := registry.NewPluginService(pool).ResolvePublishedSkillVersions(ctx, tenantID, keys)
			if err != nil {
				return nil, err
			}
			out := make([]agent.SkillSnapshot, 0, len(snaps))
			for _, s := range snaps {
				out = append(out, agent.SkillSnapshot{
					SkillKey: s.SkillKey, Name: s.Name, Version: s.Version,
					VersionID: s.VersionID.String(), PromptFragment: s.PromptFragment,
					ToolBindings: s.ToolBindings, ContentDigest: s.ContentDigest,
				})
			}
			return out, nil
		}),
		evidence: &evidence.Store{Pool: pool},
		gw:       gw,
		logger:   logger,
	}

	logger.Info("worker started",
		"concurrency", concurrency, "lease_seconds", leaseSeconds, "owner", base,
		"llm_gateway", gw != nil)

	// ---- 知识摄取回路（企业知识问答模块 A）：EMBED/EXTRACT 两个队列 ----
	// 分块内容直接读 kb.chunks（上传时已同步落库），不依赖 uploads 卷；
	// source_path 仅审计溯源。嵌入模型缺省时 EMBED 任务在执行期 fail-closed。
	kbUploadsRoot := env("ODAP_UPLOAD_ROOT", "uploads")
	ingestConcurrency := envInt("WORKER_INGEST_CONCURRENCY", 2)
	if ingestConcurrency < 1 {
		ingestConcurrency = 1
	}
	kbRunner := &knowledge.Runner{
		Pool:           pool,
		Graph:          graphStore,
		EmbeddingModel: kbEmbeddingModel,
		Logger:         logger,
		Actions:        actRecorder,
	}
	if gw != nil {
		// 网关可用时同一实例同时提供 Embed/Complete 能力（镜像 cmd/api 装配）。
		kbRunner.Embedder = gw
		kbRunner.Completer = gw
	} else {
		logger.Warn("未装配 LLM 网关：知识摄取任务（EMBED/EXTRACT）将 fail-closed")
	}
	logger.Info("ingest loop configured",
		"concurrency", ingestConcurrency, "uploads_root", kbUploadsRoot,
		"embedding_model", kbEmbeddingModel)

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		owner := fmt.Sprintf("%s:%d", base, i)
		go func(owner string) {
			defer wg.Done()
			workerLoop(ctx, pool, clk, owner, time.Duration(leaseSeconds)*time.Second, logger, executor)
		}(owner)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		publisherLoop(ctx, pool, clk, base+":pub", logger)
	}()
	// 知识摄取循环：并发领取 kb.ingest_jobs（SKIP LOCKED 去重），租约内分批执行。
	wg.Add(1)
	go func() {
		defer wg.Done()
		kbRunner.IngestJobLoop(ctx, func(ctx context.Context) []ids.ID {
			tenants, err := activeTenantIDs(ctx, pool)
			if err != nil {
				logger.Warn("ingest: list tenants failed", "error", err)
				return nil
			}
			return tenants
		}, ingestConcurrency)
	}()

	<-ctx.Done()
	logger.Info("shutdown signal received, waiting for workers")
	wg.Wait()
	logger.Info("worker stopped")
	return nil
}

// workerLoop 是单个 Worker 的调度循环：四队列依次领取 → 步进执行 → 状态簿记。
func workerLoop(ctx context.Context, pool *pgxpool.Pool, clk clock.Clock, owner string,
	leaseTTL time.Duration, logger *slog.Logger, executor *stepExecutor) {
	sched := &harness.Scheduler{Pool: pool, Clock: clk, Owner: owner, LeaseTTL: leaseTTL}

	// 四队列优先级：取消收敛 > 对账 > 恢复 > 正常（避免取消/对账饥饿）。
	classes := []runstate.SchedulingClass{
		runstate.ClassCancelling,
		runstate.ClassReconciliation,
		runstate.ClassRecovery,
		runstate.ClassNormal,
	}

	backoff := 250 * time.Millisecond
	const maxBackoff = 5 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}
		claimedAny := false
		tenants, err := activeTenantIDs(ctx, pool)
		if err != nil {
			logger.Warn("list tenants failed", "owner", owner, "error", err)
			sleepCtx(ctx, maxBackoff)
			continue
		}
		for _, tenantID := range tenants {
			// 周期性回收过期租约（第六篇 §14.2.3）。
			if _, err := sched.ReleaseExpiredLeases(ctx, tenantID); err != nil {
				logger.Warn("release expired leases failed", "tenant", tenantID.String(), "error", err)
			}
			for _, class := range classes {
				claimed, err := sched.ClaimNext(ctx, tenantID, class)
				if err != nil {
					logger.Error("claim next failed", "tenant", tenantID.String(), "class", string(class), "error", err)
					continue
				}
				if claimed == nil {
					continue
				}
				claimedAny = true
				executor.execute(ctx, sched, claimed)
				break // 本轮该租户已领取一个任务，进入下一租户
			}
		}
		if claimedAny {
			backoff = 250 * time.Millisecond
			continue
		}
		sleepCtx(ctx, backoff)
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// publisherLoop 周期性把 outbox 事件发布到 Sink（第十篇 §5.4 两段式）。
func publisherLoop(ctx context.Context, pool *pgxpool.Pool, clk clock.Clock, owner string, logger *slog.Logger) {
	pub := &outbox.Publisher{
		Pool:  pool,
		Clock: clk,
		Sink:  logSink{logger: logger},
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
				logger.Warn("publisher: list tenants failed", "error", err)
				continue
			}
			for _, tenantID := range tenants {
				if n, err := pub.RunOnce(ctx, tenantID); err != nil {
					logger.Warn("publisher: run once failed", "tenant", tenantID.String(), "error", err)
				} else if n > 0 {
					logger.Debug("published outbox events", "tenant", tenantID.String(), "count", n)
				}
				if _, err := pub.ReclaimExpired(ctx, tenantID); err != nil {
					logger.Warn("publisher: reclaim expired failed", "tenant", tenantID.String(), "error", err)
				}
			}
		}
	}
}

// logSink 是消息代理的 MVP 占位实现：记录事件并接受之。
//
// 生产环境应替换为真实 Broker 生产者（第十篇）；此处保留不丢事件的语义，
// 且 Publisher.RunOnce 的领取/标记流程是真实的（事件会置为 SENT）。
type logSink struct{ logger *slog.Logger }

// Publish 实现 outbox.Sink。
func (s logSink) Publish(_ context.Context, ev outbox.Event) error {
	if s.logger != nil {
		payload := json.RawMessage(ev.Payload)
		s.logger.Debug("publish outbox event",
			"event_id", ev.EventID.String(),
			"type", ev.EventType,
			"aggregate_seq", ev.AggregateSeq,
			"payload", string(payload))
	}
	return nil
}

// activeTenantIDs 列出 ACTIVE 租户（iam.tenants 无 RLS）。
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

// sleepCtx 可被取消的退避等待。
func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
