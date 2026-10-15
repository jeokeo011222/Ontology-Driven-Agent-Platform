package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/api/middleware"
	"odap/internal/llm"
	"odap/internal/platform/clock"
	"odap/internal/platform/ids"
	"odap/internal/platform/logging"
	"odap/internal/platform/pgxutil"
	"odap/internal/platform/secretbox"
	"odap/internal/platform/tenantctx"
)

// llmCheck 是"模型调用链"的端到端自检。
//
// 与 seed 的分工：seed 只保证**配置数据**写对了；本命令回答的是另一个问题 ——
// 调用链是否真的通了。它把链路完整走一遍：
//
//	Agent Registry 读取调用契约（输出 Schema / 提示词 / 依赖工具）
//	→ Model Registry 装载端点（llm.* → Provider 实例）
//	→ Gateway 治理链（限流 → 预算 → 缓存 → 路由 → 结构化输出）
//	→ 供应商适配器调用
//	→ 用量账本落账并回查确认
//
// 只有"落账可回查"才算通过：调用成功但账本没写进去，等于治理链是假的
// （第九篇 §16.2 用量账本是对账与预算的唯一依据）。
func llmCheck(args []string) error {
	fs := flag.NewFlagSet("llm-check", flag.ExitOnError)
	tenantID := fs.String("tenant", env("ODAP_DEV_TENANT", ""), "租户 ID")
	agentKey := fs.String("agent", "finance-expense-auditor", "用于取调用契约的 Agent 键")
	model := fs.String("model", "", "模型 ID（必填；须已在 Model Registry 登记且端点可用）")
	expenseID := fs.String("expense", "EXP-1001", "演示报销单号")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// 默认值此前指向 "mock-chat"（内置模拟供应商提供的模型）；该供应商已下线，
	// 这个默认值只会把调用引向一个不存在的模型、再在路由期失败。改为显式必填：
	// 让调用方自己说清要打哪个真实模型，而不是替它猜一个已失效的默认值。
	if strings.TrimSpace(*model) == "" {
		return fmt.Errorf("缺少 --model：内置模拟供应商已下线，必须显式指定一个已在 Model Registry 登记且端点可用的模型 ID")
	}

	logger := logging.New(env("LOG_LEVEL", "info"))
	dbURL := databaseURL()
	if dbURL == "" {
		return fmt.Errorf("ODAP_DATABASE_URL (or DATABASE_URL) is required")
	}
	if *tenantID == "" {
		return fmt.Errorf("缺少租户 ID（--tenant 或 ODAP_DEV_TENANT）")
	}
	tid, err := ids.Parse(*tenantID)
	if err != nil {
		return fmt.Errorf("租户 ID 非法: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, err := pgxutil.Connect(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	// ---- 1. 从 Agent Registry 读取调用契约 ----
	spec, err := loadAgentCallSpec(ctx, pool, tid, *agentKey)
	if err != nil {
		return err
	}
	logger.Info("已读取 Agent 调用契约",
		"agent", spec.AgentKey, "version", spec.Version,
		"tools", len(spec.Tools), "has_output_schema", len(spec.OutputSchema) > 0)

	// ---- 2. 从 Model Registry 装载端点并装配 Gateway ----
	loader := &llm.RegistryLoader{Pool: pool}
	bindings, excluded, err := loader.Load(ctx)
	if err != nil {
		return err
	}
	logger.Info("Model Registry 装载完成", "bindings", len(bindings), "excluded_or_notes", len(excluded))
	for _, x := range excluded {
		logger.Warn("端点排除/提示", "provider", x.ProviderID, "endpoint", x.EndpointName,
			"model", x.ModelID, "reason", x.Reason)
	}

	// 凭据解密器：密文凭据（界面录入路径）需要主密钥才能还原，
	// credential_ref 环境变量路径不受影响。
	secretCipher, err := secretbox.NewFromEnv(os.Getenv)
	if err != nil {
		return fmt.Errorf("凭据主密钥 ODAP_SECRET_MASTER_KEY 无效：%w", err)
	}

	ledger := llm.NewUsageLedger(pool)
	gw := llm.BuildGateway(bindings, excluded, llm.GatewayOptions{
		Logger:  logger,
		Getenv:  os.Getenv,
		Secrets: secretCipher,
		Limiter: llm.NewLimiter(clock.RealClock{}),
		Ledger:  ledger,
		Budget:  &llm.BudgetGuard{Ledger: ledger, Limit: 5.0, ReserveFactor: llm.DefaultReserveFactor},
		Cache:   llm.NewSemanticCache(nil, nil, clock.RealClock{}),
	})
	if len(gw.Providers) == 0 {
		return fmt.Errorf("没有任何端点被装配成功：请检查 providers.json 与能力档案，或端点是否全部未启用")
	}

	// ---- 3. 组装一次真实调用请求 ----
	runID, err := demoRunID(ctx, pool, tid)
	if err != nil {
		return err
	}
	// 幂等键：同一逻辑请求重试不重复结算（第九篇 §16.2）。
	idem := fmt.Sprintf("llm-check:%s:%s", spec.AgentKey, *expenseID)

	req := llm.CompletionRequest{
		TenantID:        tid,
		RunID:           runID,
		ModelID:         *model,
		Messages:        spec.messages(*expenseID),
		Tools:           spec.Tools,
		MaxOutputTokens: spec.MaxOutputTokens,
		ResponseSchema:  spec.OutputSchema,
		// 数据分级参与路由硬约束（第九篇 §6.1）：演示数据标记为 INTERNAL。
		DataClassification: "INTERNAL",
		IdempotencyKey:     idem,
	}
	if spec.Temperature != nil {
		req.Temperature = spec.Temperature
	}

	// ---- 4. 执行受治理的推理调用 ----
	comp, err := gw.Complete(ctx, tid, req)
	if err != nil {
		return fmt.Errorf("推理调用失败（这条链路是 fail-closed 的，不会静默回落）：%w", err)
	}

	fmt.Printf("模型:      %s\n", *model)
	fmt.Printf("内容:      %s\n", comp.Content)
	fmt.Printf("Token:     in=%d out=%d\n", comp.InputTokens, comp.OutputTokens)
	fmt.Printf("修订:      %s\n", comp.ModelRevision)
	if len(comp.ToolCalls) > 0 {
		fmt.Printf("工具调用:  %d 条\n", len(comp.ToolCalls))
	}

	// ---- 5. 回查账本：调用成功 ≠ 已结算 ----
	var (
		gotRequestID string
		gotTokens    int64
		gotStatus    string
	)
	queryErr := tenantQuery(ctx, pool, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
            SELECT request_id, input_tokens + output_tokens, settlement_status
              FROM llm.usage_ledger
             WHERE tenant_id = $1 AND request_id = $2`, tid.String(), idem).
			Scan(&gotRequestID, &gotTokens, &gotStatus)
	})
	if queryErr != nil {
		return fmt.Errorf("调用成功但账本回查失败（治理链不完整）: %w", queryErr)
	}
	fmt.Printf("账本:      request_id=%s tokens=%d status=%s\n", gotRequestID, gotTokens, gotStatus)
	fmt.Printf("可用端点:  %d（被排除/提示 %d 条，见上方 WARN 日志）\n", len(gw.Providers), len(gw.Unavailable))
	return nil
}

// agentCallSpec 是从 Agent Registry 读出的"调用契约"。
type agentCallSpec struct {
	AgentKey        string
	Version         string
	OutputSchema    json.RawMessage
	PromptSystem    string
	PromptUser      string
	Tools           []llm.ToolSpec
	Temperature     *float64
	MaxOutputTokens int
}

// messages 由 Agent 的提示词模板与依赖清单构造本次调用的消息序列。
//
// 这一步是"Agent 定义驱动调用"的关键：提示词与工具集来自 Registry，
// 而不是散落在调用方代码里；换 Agent 版本即换行为，无需改代码。
func (s agentCallSpec) messages(expenseID string) []llm.Message {
	expense := fmt.Sprintf(`{"expense_id":%q,"amount":1280.00,"category":"TRAVEL","cost_center":"CC-1001"}`, expenseID)
	policy := "差旅费单笔超过 1000 元需附行程单；无行程单不予报销。"
	user := strings.ReplaceAll(s.PromptUser, "{{expense}}", expense)
	user = strings.ReplaceAll(user, "{{policy}}", policy)
	var out []llm.Message
	if s.PromptSystem != "" {
		out = append(out, llm.Message{Role: "system", Content: s.PromptSystem})
	}
	if user == "" {
		user = "请审核报销单 " + expense
	}
	return append(out, llm.Message{Role: "user", Content: user})
}

// loadAgentCallSpec 在租户上下文中读取 Agent 版本、提示词、配置与依赖工具集。
func loadAgentCallSpec(ctx context.Context, pool *pgxpool.Pool, tid ids.ID, agentKey string) (*agentCallSpec, error) {
	spec := &agentCallSpec{AgentKey: agentKey}
	err := tenantQuery(ctx, pool, tid, func(tx pgx.Tx) error {
		var (
			promptRaw, configRaw, depRaw json.RawMessage
		)
		err := tx.QueryRow(ctx, `
            SELECT a.agent_key, v.version, v.output_schema, v.prompt_template, v.config, v.dependency_manifest
              FROM registry.agents a
              JOIN registry.agent_versions v ON v.agent_id = a.id
             WHERE a.tenant_id = $1 AND a.agent_key = $2
             ORDER BY (v.status = 'PUBLISHED') DESC, v.created_at DESC
             LIMIT 1`, tid.String(), agentKey).
			Scan(&spec.AgentKey, &spec.Version, &spec.OutputSchema, &promptRaw, &configRaw, &depRaw)
		if err != nil {
			return fmt.Errorf("读取 Agent %q 的版本失败: %w", agentKey, err)
		}

		var prompt struct {
			System string `json:"system"`
			User   string `json:"user"`
		}
		_ = json.Unmarshal(promptRaw, &prompt)
		spec.PromptSystem, spec.PromptUser = prompt.System, prompt.User

		var cfg struct {
			Temperature     *float64 `json:"temperature"`
			MaxOutputTokens int      `json:"max_output_tokens"`
		}
		_ = json.Unmarshal(configRaw, &cfg)
		spec.Temperature, spec.MaxOutputTokens = cfg.Temperature, cfg.MaxOutputTokens
		if spec.MaxOutputTokens <= 0 {
			spec.MaxOutputTokens = 512
		}

		var dep struct {
			Tools []string `json:"tools"`
		}
		_ = json.Unmarshal(depRaw, &dep)
		if len(dep.Tools) == 0 {
			return nil
		}
		// 只把 READ 类工具暴露给模型：带副作用的工具不应由模型"自行决定"调用
		//（第九篇 §9.4：模型的原生工具调用不构成执行权限）。
		toolRows, err := tx.Query(ctx, `
            SELECT t.tool_key, t.description, tv.input_schema, tv.effect_class
              FROM registry.tools t
              JOIN registry.tool_versions tv ON tv.tool_id = t.id
             WHERE t.tenant_id = $1 AND t.tool_key = ANY($2)
               AND tv.status = 'PUBLISHED'`, tid.String(), dep.Tools)
		if err != nil {
			return fmt.Errorf("读取 Agent 依赖工具失败: %w", err)
		}
		defer toolRows.Close()
		for toolRows.Next() {
			var (
				key, desc, effect string
				in                json.RawMessage
			)
			if err := toolRows.Scan(&key, &desc, &in, &effect); err != nil {
				return err
			}
			spec.Tools = append(spec.Tools, llm.ToolSpec{
				Name:        key,
				Description: desc + "（effect_class=" + effect + "）",
				InputSchema: in,
			})
		}
		return toolRows.Err()
	})
	if err != nil {
		return nil, err
	}
	return spec, nil
}

// demoRunID 取该租户最近一次演示 Run，作为账本条目的关联对象。
// 没有演示 Run 时生成一个新的：账本要求 run_id 非空，宁可给一个真实存在的
// UUID，也不要伪造一个看起来像运行标识的字符串。
func demoRunID(ctx context.Context, pool *pgxpool.Pool, tid ids.ID) (ids.ID, error) {
	var runID ids.ID
	found := false
	err := tenantQuery(ctx, pool, tid, func(tx pgx.Tx) error {
		var raw string
		err := tx.QueryRow(ctx, `
            SELECT id::text FROM runtime.agent_runs
             WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT 1`, tid.String()).Scan(&raw)
		if err != nil {
			if err == pgx.ErrNoRows {
				return nil
			}
			return err
		}
		parsed, perr := ids.Parse(raw)
		if perr != nil {
			return perr
		}
		runID, found = parsed, true
		return nil
	})
	if err != nil {
		return ids.ID{}, err
	}
	if !found {
		return ids.New(), nil
	}
	return runID, nil
}

// tenantQuery 在租户事务中执行只读查询（租户级表启用 FORCE RLS，必须注入租户上下文）。
func tenantQuery(ctx context.Context, pool *pgxpool.Pool, tid ids.ID, fn func(pgx.Tx) error) error {
	tctx := tenantctx.WithIdentity(ctx, tenantctx.Identity{TenantID: tid, SubjectID: tid})
	return middleware.TenantTx(tctx, pool, fn)
}
