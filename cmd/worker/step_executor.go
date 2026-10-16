package main

// worker 步进执行器：领取 Run 后真实推进一次执行（任务 #136，评测清单 L7）。
//
// 执行链（对 agent_version_id 非空的 Run）：
//
//	LeaseAcquired → ContextBuildStarted → ContextBuildSucceeded（→READY）
//	→ PlanGenerated（→PLANNING）→ PlanValidationSucceeded（→PLAN_VALIDATING→RUNNING）
//	→ [READ 工具真实执行 + 模型调用（用量自动入账）+ 证据落库]
//	→ RunCompleted（HasInflightExternalOps=false）/ RunFailed
//
// 诚实边界（fail-closed，绝不伪造成功）：
//   - agent_version_id 为空（本体驱动 Run）→ 只做租约簿记后放回，说明原因；
//     本体驱动执行链由后续任务接线，这里不假装推进。
//   - 模型网关未装配 → RunFailed 并写明"模型网关未装配"。
//   - 非 READ 工具不执行（EXTERNAL_WRITE 必须走人工审批链路），记日志跳过；
//   - 工具执行失败 → 调用记录如实落 FAILED + RunFailed，绝不吞错继续。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/action"
	"odap/internal/agent"
	"odap/internal/api/handlers"
	"odap/internal/api/store"
	"odap/internal/evidence"
	"odap/internal/harness"
	"odap/internal/llm"
	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
	"odap/internal/platform/tenantctx"
	"odap/internal/registry"
	"odap/internal/tools"
)

// stepExecutor 是单次 Run 执行的依赖集合（进程级构造一次，循环复用）。
type stepExecutor struct {
	pool     *pgxpool.Pool
	machine  *harness.Machine
	handler  *handlers.Handler // 复用 handlers 的真实工具执行器（唯一实现，禁止再造第二份）
	st       *store.Store      // 调用记录（runtime.tool_invocations）
	catalog  *registry.CatalogService
	skills   agent.SkillLoader // 技能展开（dependency_manifest.skills → 提示词小节）
	evidence *evidence.Store
	gw       *llm.Gateway // 可为 nil：LLM 步骤 fail-closed
	logger   *slog.Logger
}

// runPayload 是 worker 补查的运行要素（ClaimedRun 不含业务字段）。
type runPayload struct {
	AgentVersionID ids.ID
	InputPayload   json.RawMessage
}

// fetchRunPayload 按 id 读 runtime.agent_runs 的业务字段（RLS 生效，走租户事务）。
func fetchRunPayload(ctx context.Context, pool *pgxpool.Pool, tenantID, runID ids.ID) (runPayload, error) {
	var out runPayload
	err := pgxutil.TenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		var agentVer *string
		if err := tx.QueryRow(ctx,
			`SELECT agent_version_id::text, input_payload
			   FROM runtime.agent_runs WHERE id = $1 AND tenant_id = $2`,
			runID.String(), tenantID.String()).Scan(&agentVer, &out.InputPayload); err != nil {
			return err
		}
		if agentVer != nil && *agentVer != "" {
			parsed, perr := ids.Parse(*agentVer)
			if perr != nil {
				return fmt.Errorf("agent_version_id 不合法: %w", perr)
			}
			out.AgentVersionID = parsed
		}
		return nil
	})
	return out, err
}

// execute 对已领取的 Run 做一次真实步进。返回是否发生了业务推进
// （false = 仅租约簿记，如本体驱动 Run / 重复领取已终态 Run）。
func (e *stepExecutor) execute(ctx context.Context, sched *harness.Scheduler, claimed *harness.ClaimedRun) bool {
	runCtx := tenantctx.WithIdentity(ctx, tenantctx.System(claimed.TenantID))
	runID := claimed.RunID

	apply := func(event harness.RunEventType, facts harness.TransitionFacts) error {
		return e.machine.Apply(runCtx, runID, event, facts)
	}
	failed := func(stage string, err error) {
		e.logger.Error("step executor failed; marking run FAILED",
			"run_id", runID.String(), "stage", stage, "error", err)
		if ferr := apply(harness.EventRunFailed, harness.TransitionFacts{LeaseValid: true}); ferr != nil {
			e.logger.Warn("apply run-failed transition failed",
				"run_id", runID.String(), "error", ferr)
		}
		// 模块 D：Run 终态（FAILED）动作留痕（旁路观测）。
		e.recordRunAction(runCtx, claimed, action.OutcomeError, "Run 步进失败："+stage, err.Error())
	}
	renew := func() bool {
		if _, err := sched.RenewLease(runCtx, claimed.TenantID, runID, sched.Owner, claimed.LeaseEpoch); err != nil {
			e.logger.Warn("renew lease failed; abort step", "run_id", runID.String(), "error", err)
			return false
		}
		return true
	}

	// 1) 租约簿记（沿用既有语义：真实驱动一次状态机写入）。
	facts := harness.TransitionFacts{LeaseValid: true}
	if err := apply(harness.EventLeaseAcquired, facts); err != nil {
		e.logger.Warn("apply lease-acquired transition failed", "run_id", runID.String(), "error", err)
		return false
	}
	if !renew() {
		return false
	}

	// 2) 补查业务字段。
	payload, err := fetchRunPayload(ctx, e.pool, claimed.TenantID, runID)
	if err != nil {
		failed("fetch_run", err)
		return true
	}
	if payload.AgentVersionID.IsZero() {
		// 本体驱动 Run（无 Agent 绑定）：执行链由资产包/本体驱动任务接线，
		// 这里诚实说明并保持可被再次领取——不伪造"已执行"。
		e.logger.Info("run has no agent_version_id; only lease bookkeeping performed",
			"run_id", runID.String())
		return false
	}

	// 3) 解析 Agent 版本（仅 PUBLISHED 可被运行时绑定）。
	av, err := e.catalog.ResolveAgentVersionByID(runCtx, claimed.TenantID, payload.AgentVersionID)
	if err != nil {
		failed("resolve_agent_version", err)
		return true
	}

	// 4) 上下文构建 → 计划就绪（单步直跑没有独立规划阶段：提示词渲染即计划）。
	// 计划校验按 §14.2 矩阵是两段跃迁：PLANNING --PlanValidationSucceeded-->
	// PLAN_VALIDATING（校验进行中）--PlanValidationSucceeded--> RUNNING（开跑）。
	// 只发一次会让 Run 卡死在 PLAN_VALIDATING，RunCompleted 被矩阵拒绝
	// （EXEC_INVALID_TRANSITION，A2A 冒烟真实事故；失败路径因 RunFailed 在
	// PLAN_VALIDATING 合法而被掩盖）。与 ontology_review 的双发先例一致。
	for _, ev := range []harness.RunEventType{
		harness.EventContextBuildStarted, harness.EventContextBuildSucceeded,
		harness.EventPlanGenerated,
		harness.EventPlanValidationSucceeded, harness.EventPlanValidationSucceeded,
	} {
		if err := apply(ev, harness.TransitionFacts{LeaseValid: true}); err != nil {
			failed("transition:"+string(ev), err)
			return true
		}
		if !renew() {
			return true
		}
	}

	// 5) READ 工具真实执行（dependency_manifest.tools，仅 READ 类）。
	toolResults, err := e.executeDeclaredReadTools(runCtx, claimed, av, payload)
	if err != nil {
		failed("read_tools", err)
		return true
	}

	// 6) 模型调用（用量由 Gateway 自动入账 llm.usage_ledger）。
	if e.gw == nil {
		failed("llm", fmt.Errorf("模型网关未装配（没有可用的真实 HTTP 模型端点）：worker 无法真实执行模型步骤"))
		return true
	}
	messages, warnings, err := e.renderPrompt(runCtx, claimed.TenantID, av, payload, toolResults)
	if err != nil {
		failed("render_prompt", err)
		return true
	}
	cfg := agentConfig(av.Config)
	idemKey := fmt.Sprintf("worker-step-%s-%d", runID.String(), claimed.AttemptCount)
	temperature := cfg.Temperature
	comp, err := e.gw.Complete(runCtx, claimed.TenantID, llm.CompletionRequest{
		TenantID:           claimed.TenantID,
		RunID:              runID,
		StepID:             ids.New(),
		Messages:           messages,
		Temperature:        &temperature,
		MaxOutputTokens:    cfg.MaxOutputTokens,
		DataClassification: llm.DataClassInternal,
		IdempotencyKey:     idemKey,
	})
	if err != nil {
		failed("llm_complete", err)
		return true
	}

	// 7) 模型输出写证据（INFER 决策 + 时间轴 task_id=run_id）。
	if err := e.saveEvidence(runCtx, claimed.TenantID, runID, evidenceItemInput(
		"MODEL_OUTPUT", "llm", "model:"+comp.ModelRevision, comp.Content,
		"INFER", "", "", warnings...)); err != nil {
		failed("evidence_model_output", err)
		return true
	}

	// 8) 完成。
	if err := apply(harness.EventRunCompleted, harness.TransitionFacts{
		LeaseValid: true, HasInflightExternalOps: false,
	}); err != nil {
		failed("run_completed", err)
		return true
	}
	e.logger.Info("run stepped to COMPLETED",
		"run_id", runID.String(), "tools_executed", len(toolResults),
		"model", comp.ModelRevision, "output_tokens", comp.OutputTokens)
	// 模块 D：Run 终态（COMPLETED）动作留痕（旁路观测）。
	e.recordRunAction(runCtx, claimed, action.OutcomeSuccess, "Run 完成",
		"model="+comp.ModelRevision+" tools="+fmt.Sprintf("%d", len(toolResults)))
	return true
}

// recordRunAction 模块 D：Run 终态动作留痕（旁路观测；记录失败仅留日志，
// 绝不影响 Run 状态机的推进结果）。e.handler 未装配或未接 Actions 时静默跳过。
func (e *stepExecutor) recordRunAction(ctx context.Context, claimed *harness.ClaimedRun, outcome, title, detail string) {
	if e.handler == nil {
		return
	}
	e.handler.RecordActionForFlow(ctx, claimed.TenantID, action.Input{
		Type:      action.TypeAction,
		Title:     title,
		Summary:   detail,
		RunID:     claimed.RunID,
		Outcome:   outcome,
		ActorKind: action.ActorWorker,
		ActorName: "worker",
	})
}

// executeDeclaredReadTools 解析并执行 dependency_manifest.tools 中声明的 READ 工具。
// 非 READ 类（EXTERNAL_WRITE/INTERNAL_WRITE）诚实跳过：写入类必须走人工审批链路，
// worker 直跑绝不绕过。args 取 input_payload.tools_args[tool_key]（缺省用 input 本身），
// 由 tools.ValidateArgs 按 input_schema 强校验——校验不过如实失败。
func (e *stepExecutor) executeDeclaredReadTools(ctx context.Context, claimed *harness.ClaimedRun,
	av registry.AgentVersion, payload runPayload) (map[string]map[string]any, error) {
	results := map[string]map[string]any{}
	var manifest struct {
		Tools []string `json:"tools"`
	}
	if len(av.DependencyManifest) > 0 {
		if err := json.Unmarshal(av.DependencyManifest, &manifest); err != nil {
			return nil, fmt.Errorf("dependency_manifest 不是合法 JSON: %w", err)
		}
	}
	argsOf := toolArgsOf(payload.InputPayload)
	for _, toolKey := range manifest.Tools {
		inv, err := e.catalog.ResolveLatestPublishedToolByKey(ctx, claimed.TenantID, toolKey)
		if err != nil {
			return nil, fmt.Errorf("解析工具 %s 失败: %w", toolKey, err)
		}
		if inv.Version.EffectClass != "READ" {
			e.logger.Info("skip non-READ tool in direct step (approval chain required)",
				"run_id", claimed.RunID.String(), "tool_key", toolKey,
				"effect_class", inv.Version.EffectClass)
			continue
		}
		args := argsOf(toolKey)
		if args == nil {
			args = map[string]any{}
		}
		if err := tools.ValidateArgs(inv.Version.InputSchema, args); err != nil {
			return nil, fmt.Errorf("工具 %s 入参未通过 input_schema 校验: %w", toolKey, err)
		}
		inputJSON, _ := json.Marshal(args)
		invRec, err := e.st.CreateToolInvocation(ctx, store.CreateToolInvocationInput{
			ToolID: inv.Tool.ID, ToolVersionID: inv.Version.ID,
			ToolKey: inv.Tool.ToolKey, EffectClass: inv.Version.EffectClass,
			Status: store.InvocationExecuting, InputPayload: inputJSON,
			RequestedBy: tenantctx.MustFromContext(ctx).SubjectID.String(),
		})
		if err != nil {
			return nil, fmt.Errorf("工具 %s 建调用记录失败: %w", toolKey, err)
		}
		invID, perr := ids.Parse(strMap(invRec, "id"))
		if perr != nil {
			return nil, fmt.Errorf("工具 %s 调用记录 id 不合法: %w", toolKey, perr)
		}
		rec := &store.ToolVersionRecord{
			ToolID:          inv.Tool.ID.String(),
			ToolKey:         inv.Tool.ToolKey,
			VersionID:       inv.Version.ID.String(),
			Version:         inv.Version.Version,
			EffectClass:     inv.Version.EffectClass,
			InputSchema:     inv.Version.InputSchema,
			OutputSchema:    inv.Version.OutputSchema,
			ExecutionConfig: inv.Version.ExecutionConfig,
			Status:          inv.Version.Status,
		}
		id := tenantctx.MustFromContext(ctx)
		out, execErr := e.handler.ExecuteToolInvocation(ctx, id, rec, invID.String(), args)
		if execErr != nil {
			errMsg := clipText(execErr.Error(), 500)
			if _, terr := e.st.TransitionToolInvocation(ctx, store.TransitionToolInvocationInput{
				InvID: invID, From: store.InvocationExecuting, To: store.InvocationFailed,
				ErrorCode: &errMsg, ErrorSum: &errMsg,
			}); terr != nil {
				e.logger.Warn("transition invocation to FAILED failed", "invocation_id", invID.String(), "error", terr)
			}
			return nil, fmt.Errorf("工具 %s 真实执行失败: %w", toolKey, execErr)
		}
		outJSON, _ := json.Marshal(out)
		if _, terr := e.st.TransitionToolInvocation(ctx, store.TransitionToolInvocationInput{
			InvID: invID, From: store.InvocationExecuting, To: store.InvocationSucceeded,
			Output: outJSON, FinishClock: true,
		}); terr != nil {
			e.logger.Warn("transition invocation to SUCCEEDED failed", "invocation_id", invID.String(), "error", terr)
		}
		results[toolKey] = out
		if err := e.saveEvidence(ctx, claimed.TenantID, claimed.RunID, evidenceItemInput(
			"TOOL_OUTPUT", "tool", "tool:"+toolKey, string(outJSON),
			"TOOL", claimed.RunID.String(), toolKey)); err != nil {
			return nil, fmt.Errorf("工具 %s 输出写证据失败: %w", toolKey, err)
		}
	}
	return results, nil
}

// saveEvidence 落一条证据记录（BuildBundle 校验词表与摘要完整性，fail-closed）。
func (e *stepExecutor) saveEvidence(ctx context.Context, tenantID, runID ids.ID, item evidence.EvidenceItem) error {
	b, err := evidence.BuildBundle(tenantID, runID, []evidence.EvidenceItem{item}, time.Now())
	if err != nil {
		return err
	}
	return e.evidence.Save(ctx, tenantID, b)
}

// evidenceItemInput 组装证据项（ContentDigest 由内容实算，绝不手写）。
func evidenceItemInput(evType, sourceType, sourceRef, content,
	decisionType, taskID, toolName string, warnings ...string) evidence.EvidenceItem {
	summary := clipText(content, 200)
	if len(warnings) > 0 {
		summary = strings.Join(warnings, "；") + "｜" + summary
	}
	return evidence.EvidenceItem{
		Type:          evType,
		SourceType:    sourceType,
		SourceRef:     sourceRef,
		ContentDigest: sha256HexWorker([]byte(content)),
		Summary:       summary,
		Metadata:      json.RawMessage(fmt.Sprintf(`{"content_sha256": %q}`, sha256HexWorker([]byte(content)))),
		DecisionType:  decisionType,
		TaskID:        taskID,
		ToolName:      toolName,
	}
}

// renderPrompt 按 prompt_template（{system, user}）渲染消息；user 模板的
// {{key}} 占位符用 input_payload 顶层键替换（JSON 序列化值）；工具结果以
// {{tool:<key>}} 注入；dependency_manifest.skills 经 agent.CompileSkills 展开
// （技能小节追加进系统提示词——Skill 一等资源的运行时落点，fail-closed）。
// payload 缺失的占位符保留原样并返回 warning——让模型看到"数据缺位"比静默
// 替换成空串更诚实。
func (e *stepExecutor) renderPrompt(ctx context.Context, tenantID ids.ID, av registry.AgentVersion,
	payload runPayload, toolResults map[string]map[string]any) ([]llm.Message, []string, error) {
	var tpl struct {
		System string `json:"system"`
		User   string `json:"user"`
	}
	if len(av.PromptTemplate) == 0 {
		return nil, nil, fmt.Errorf("agent 版本 %s 没有 prompt_template：无法渲染提示词", av.ID)
	}
	if err := json.Unmarshal(av.PromptTemplate, &tpl); err != nil {
		return nil, nil, fmt.Errorf("prompt_template 不是合法 JSON 对象: %w", err)
	}
	if strings.TrimSpace(tpl.System) == "" {
		return nil, nil, fmt.Errorf("prompt_template.system 为空：拒绝无系统提示词的模型调用")
	}
	var input map[string]any
	if len(payload.InputPayload) > 0 {
		if err := json.Unmarshal(payload.InputPayload, &input); err != nil {
			return nil, nil, fmt.Errorf("run input_payload 不是合法 JSON 对象: %w", err)
		}
	}
	var warnings []string
	render := func(t string) string {
		return expandTemplate(t, func(key string) (string, bool) {
			// {{tool:<tool_key>}}：注入该工具的真实执行结果。
			if strings.HasPrefix(key, "tool:") {
				if v, ok := toolResults[strings.TrimPrefix(key, "tool:")]; ok {
					raw, _ := json.Marshal(v)
					return string(raw), true
				}
				warnings = append(warnings, "占位符 {{"+key+"}} 引用的工具没有执行结果")
				return "", false
			}
			if v, ok := input[key]; ok {
				raw, _ := json.Marshal(v)
				return string(raw), true
			}
			warnings = append(warnings, "占位符 {{"+key+"}} 在 input_payload 中无对应值")
			return "", false
		})
	}
	// 技能展开（fail-closed：声明的技能不存在/未发布 → Run 失败，绝不静默跳过）。
	systemPrompt := tpl.System
	if e.skills != nil {
		var manifest struct {
			Skills []string `json:"skills"`
		}
		if len(av.DependencyManifest) > 0 {
			if err := json.Unmarshal(av.DependencyManifest, &manifest); err != nil {
				return nil, nil, fmt.Errorf("dependency_manifest 不是合法 JSON 对象: %w", err)
			}
		}
		exp, err := agent.CompileSkills(ctx, e.skills, tenantID, manifest.Skills)
		if err != nil {
			return nil, nil, fmt.Errorf("技能展开失败: %w", err)
		}
		systemPrompt = exp.ApplyToPrompt(systemPrompt)
	}
	return []llm.Message{
		{Role: "system", Content: render(systemPrompt)},
		{Role: "user", Content: render(tpl.User)},
	}, warnings, nil
}

// expandTemplate 替换 {{key}} 占位符；replace 返回 false 时保留原样。
func expandTemplate(t string, replace func(key string) (string, bool)) string {
	var b strings.Builder
	for {
		start := strings.Index(t, "{{")
		if start < 0 {
			b.WriteString(t)
			return b.String()
		}
		end := strings.Index(t[start:], "}}")
		if end < 0 {
			b.WriteString(t)
			return b.String()
		}
		end += start
		key := strings.TrimSpace(t[start+2 : end])
		if v, ok := replace(key); ok {
			b.WriteString(t[:start])
			b.WriteString(v)
		} else {
			b.WriteString(t[:end+2]) // 保留原样（含花括号）
		}
		t = t[end+2:]
	}
}

// agentConfig 投影 agent_versions.config（缺省安全值：temperature 0、1024 token）。
type agentRunConfig struct {
	Temperature     float64 `json:"temperature"`
	MaxOutputTokens int     `json:"max_output_tokens"`
}

func agentConfig(raw json.RawMessage) agentRunConfig {
	cfg := agentRunConfig{MaxOutputTokens: 1024}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &cfg)
	}
	if cfg.MaxOutputTokens <= 0 || cfg.MaxOutputTokens > 8192 {
		cfg.MaxOutputTokens = 1024
	}
	return cfg
}

// toolArgsOf 提取 input_payload.tools_args（{tool_key: args} 映射；缺省 nil）。
func toolArgsOf(input json.RawMessage) func(toolKey string) map[string]any {
	var wrapper struct {
		ToolsArgs map[string]map[string]any `json:"tools_args"`
	}
	if len(input) > 0 {
		_ = json.Unmarshal(input, &wrapper)
	}
	return func(toolKey string) map[string]any {
		return wrapper.ToolsArgs[toolKey]
	}
}

// clipText 按字节预算安全截断：直接字节切片会把多字节 UTF-8 字符拦腰截断，
// 产出非法字节序列被 PostgreSQL 拒绝（SQLSTATE 22021）。这里回退到最近的
// rune 起始字节再截断，保证结果始终是合法 UTF-8。
func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// sha256HexWorker 实算内容摘要（证据完整性：摘要必须来自内容本身）。
func sha256HexWorker(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func strMap(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}
