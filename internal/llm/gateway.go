package llm

import (
	"context"
	stderrors "errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"odap/internal/platform/clock"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// Resolved 是一次路由解析的结果：选中的端点及其在 Model Registry 中的身份。
//
// 身份必须随选择结果一起返回，而不是事后从 Provider 名字反推：用量账本
// （llm.usage_ledger.endpoint_id NOT NULL）与 §5.3「Run 必须记录确切的模型选择」
// 都要求拿到真实的端点/模型版本标识。
type Resolved struct {
	Provider        Provider
	EndpointID      ids.ID
	ModelRevisionID ids.ID
}

// Router 选择满足约束的 Provider（第九篇 §6）。
//
// 本实现是最小可用版本：按 ModelID 静态映射或能力匹配选择端点，不实现
// §6.4 的质量评分与 §6.5 影子路由（quality score / shadow routing 留作 V1.5，
// 需与评估平台联动，见第十五篇）。绝不因能力或数据政策不满足而静默降级
// （§6.2：无合格模型应返回 UNAVAILABLE 语义错误）。
type Router struct {
	Providers map[string]Provider
	// Routing 是 ModelID → Provider 名称的静态映射（固定路由，§6.5）。
	Routing map[string]string
	// Registered 记录**已在 Model Registry 登记**的全部 ModelID → 不可用原因。
	//
	// 为什么需要它：如果只把"成功装配的端点"放进 Routing，那么被禁用/凭据缺失的
	// 模型在 Routing 里根本不存在，请求它会顺着"按能力择优"分支落到别的模型上 ——
	// 这正是 §6.2 禁止的静默降级（调用方以为用了 A 模型，实际用了 B）。
	// 有了 Registered，登记过但不可用的模型会以 UNAVAILABLE 明确失败。
	Registered map[string]string
}

// Resolve 选择 Provider；无满足硬约束的端点返回 CodeUnavailable。
//
// 路由语义（第九篇 §5.3 模型版本锁定、§6.2 禁止静默降级）分三种情况：
//
//  1. 请求点名的 ModelID 已固定路由到一个可用端点 → 采用该端点；
//  2. 请求点名的 ModelID 已登记但当前不可用（端点禁用、凭据缺失、能力不足）
//     → 返回 UNAVAILABLE，**绝不换一个模型顶上**：调用方按模型能力与计价做的
//     预算、质量门禁与可复现性假设会随之失效，Run 记录的模型选择与实际调用
//     也不符（§5.3 要求记录确切的模型选择）；
//  3. 请求未点名任何已登记的 ModelID → 才允许按能力做确定性择优。
func (r *Router) Resolve(req CompletionRequest) (Resolved, error) {
	if r == nil || len(r.Providers) == 0 {
		return Resolved{}, errors.E(errors.CodeUnavailable, "llm: 未配置任何模型供应商")
	}
	needTools := len(req.Tools) > 0
	needSeed := req.Seed != nil

	if name, ok := r.Routing[req.ModelID]; ok {
		if p, found := r.Providers[name]; found && satisfies(p.Capabilities(), needTools, needSeed) {
			return resolvedFor(p), nil
		}
		// 有固定映射但当前不可用：走下面的"已登记即拒绝"分支。
	}
	if reason, registered := r.Registered[req.ModelID]; registered {
		if reason == "" {
			reason = fmt.Sprintf("固定路由端点不满足本次调用的能力要求（need_tools=%v，need_seed=%v）", needTools, needSeed)
		}
		return Resolved{}, errors.Ef(errors.CodeUnavailable,
			"llm: 模型 %q 已登记但当前不可用（%s）；拒绝回落到其他模型", req.ModelID, reason)
	}
	// 未登记的 ModelID：按能力确定性择优（调用方没有指定具体模型，选择权在平台）。
	names := make([]string, 0, len(r.Providers))
	for n := range r.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	// 本次要求结构化输出时，把声明原生 Schema 能力的端点排到前面（第九篇 §6.4/§9.1）：
	// 原生约束生成的输出不合法概率显著更低。generate-then-validate 端点不被排除，
	// 只是要额外走受限重生成回路，因此排在其后。
	if len(req.ResponseSchema) > 0 {
		names = preferNativeSchema(names, r.Providers)
	}
	for _, n := range names {
		p := r.Providers[n]
		if satisfies(p.Capabilities(), needTools, needSeed) {
			return resolvedFor(p), nil
		}
	}
	return Resolved{}, errors.Ef(errors.CodeUnavailable,
		"llm: 无满足能力要求的合格模型（model=%q，need_tools=%v，need_seed=%v）",
		req.ModelID, needTools, needSeed)
}

// ResolveEmbedding 为向量嵌入调用做**点名专用**路由。
//
// 与 Resolve 的语义差异（§6.2 极端形式）：嵌入向量与训练模型强绑定，向量
// 空间不可跨模型混用，因此这里**不存在**"按能力择优"分支：
//   - 未点名 ModelID → 直接 UNAVAILABLE（Gateway.Embed 已前置拦截，这里兜底）；
//   - 点名的 ModelID 有固定路由且端点实现 EmbeddingProvider 且能力声明
//     Embeddings → 采用；
//   - 点名的 ModelID 已登记但当前不可用（未装配/适配器不支持/能力未声明）
//     → UNAVAILABLE 并携带原因，绝不换模型顶上。
func (r *Router) ResolveEmbedding(req EmbeddingRequest) (Resolved, error) {
	if r == nil || len(r.Providers) == 0 {
		return Resolved{}, errors.E(errors.CodeUnavailable, "llm: 未配置任何模型供应商")
	}
	model := strings.TrimSpace(req.ModelID)
	if model == "" {
		return Resolved{}, errors.E(errors.CodeUnavailable,
			"llm: 嵌入调用必须点名已登记且支持 embeddings 的模型（禁止能力择优回落）")
	}
	if name, ok := r.Routing[model]; ok {
		if p, found := r.Providers[name]; found {
			if _, implement := p.(EmbeddingProvider); implement && p.Capabilities().Embeddings {
				return resolvedFor(p), nil
			}
			return Resolved{}, errors.Ef(errors.CodeUnavailable,
				"llm: 模型 %q 的端点 %s 未声明 embeddings 能力（或适配器不支持），拒绝嵌入调用", model, p.Name())
		}
	}
	if reason, registered := r.Registered[model]; registered {
		if reason == "" {
			reason = "端点未装配或能力不满足"
		}
		return Resolved{}, errors.Ef(errors.CodeUnavailable,
			"llm: 模型 %q 已登记但当前不可用（%s）；拒绝回落到其他模型", model, reason)
	}
	return Resolved{}, errors.Ef(errors.CodeUnavailable,
		"llm: 模型 %q 未在 Model Registry 登记，无法用于向量嵌入（请先在「模型与推理」页登记模型版本并勾选 embeddings 能力，然后重启 API 装配）", model)
}

// preferNativeSchema 把声明原生 Schema 能力的端点前置（组内顺序不变，保持确定性）。
func preferNativeSchema(names []string, providers map[string]Provider) []string {
	native := make([]string, 0, len(names))
	rest := make([]string, 0, len(names))
	for _, n := range names {
		if providers[n].Capabilities().NativeSchema {
			native = append(native, n)
			continue
		}
		rest = append(rest, n)
	}
	return append(native, rest...)
}

// resolvedFor 取出 Provider 携带的端点身份（未携带时为零值，由调用方决定如何处置）。
func resolvedFor(p Provider) Resolved {
	endpointID, modelRevisionID := EndpointOf(p)
	return Resolved{Provider: p, EndpointID: endpointID, ModelRevisionID: modelRevisionID}
}

// satisfies 判定能力是否满足硬约束（数据安全与能力要求是硬约束，§6.1）。
func satisfies(caps ProviderCapabilities, needTools, needSeed bool) bool {
	if needTools && !caps.SupportsTools {
		return false
	}
	if needSeed && !caps.SupportsSeed {
		return false
	}
	return true
}

// Gateway 编排一次推理调用（第九篇 §7.1 治理决策链的工程化落地）：
// 限流 → 预算 → 缓存查（含 cache_hit 与降权）→ 结构化输出回路 →
// 用量记账（写失败只记录不阻断业务，但预算超限必须阻断）→ 返回。
type Gateway struct {
	Providers map[string]Provider
	Router    *Router
	Cache     *SemanticCache
	Limiter   *Limiter
	Ledger    *UsageLedger
	Budget    *BudgetGuard
	Clock     clock.Clock
	Logger    *slog.Logger

	// Kind 是限流档位（默认 KindInteractive）。
	Kind string

	// Unavailable 记录装配期被排除/降级的端点与原因（只读诊断信息）。
	//
	// 不参与路由决策：路由的"不可用"语义由 Router 的固定映射 + UNAVAILABLE
	// 错误码表达（见 Router.Resolve）。保留这份清单是为了让"已登记但不可调用"
	// 有据可查，而不是只能靠翻日志。
	Unavailable []Exclusion
}

// Complete 执行一次受治理的推理调用。
func (g *Gateway) Complete(ctx context.Context, tenantID ids.ID, req CompletionRequest) (Completion, error) {
	if tenantID.IsZero() {
		return Completion{}, errors.E(errors.CodeValidationFailed, "llm: tenantID 不能为空")
	}
	// 1. 限流（第九篇 §14.3）。
	if g.Limiter != nil {
		release, err := g.Limiter.Acquire(ctx, tenantID, g.kind())
		if err != nil {
			return Completion{}, err
		}
		defer release()
	}
	// 2. 预算（第九篇 §11.2；超限必须阻断）。
	if g.Budget != nil {
		if err := g.Budget.Check(ctx, tenantID, estimateCost(req)); err != nil {
			return Completion{}, err
		}
	}
	// 3. 缓存查（第九篇 §12：命中回传 cache_hit 与层级，调用方须降权）。
	if g.Cache != nil {
		comp, hit, _, err := g.Cache.Lookup(ctx, tenantID, req)
		if err != nil {
			g.log().Warn("语义缓存查询失败", "error", err)
		} else if hit {
			return comp, nil
		}
	}
	// 4. 路由 + 结构化输出回路。
	resolved, err := g.router().Resolve(req)
	if err != nil {
		return Completion{}, err
	}
	if resolved.EndpointID.IsZero() {
		// 端点身份缺失意味着这条 Provider 不是从 Model Registry 装配的。
		// 后果不是"调用失败"，而是账本写不进去（endpoint_id NOT NULL），
		// 而账本写失败按 §16.2 只告警不阻断 —— 于是账本会一直空着却无人察觉。
		// 因此这里必须留下明确告警，而不是让缺失静默传递到账本层。
		g.log().Warn("端点身份缺失：用量账本将无法落账（Provider 未经 Model Registry 装配）",
			"provider", resolved.Provider.Name(), "model", req.ModelID)
	}
	comp, err := CompleteStructured(ctx, resolved.Provider, req)
	if err != nil {
		return Completion{}, classifyProviderError(err)
	}
	// 5. 用量记账：写失败只记录不阻断业务（第九篇 §16.2）。
	if g.Ledger != nil {
		if err := g.Ledger.Record(ctx, tenantID, g.usageFor(req, comp, resolved)); err != nil {
			g.log().Warn("用量记账失败", "error", err)
		}
	}
	// 6. 回填缓存（best-effort）。
	if g.Cache != nil {
		if err := g.Cache.Store(ctx, tenantID, req, comp); err != nil {
			g.log().Warn("语义缓存写入失败", "error", err)
		}
	}
	return comp, nil
}

// Embed 执行一次受治理的向量嵌入调用（与 Complete 同一条治理链：
// 限流 → 预算 → 点名路由 → 调用 → 用量记账）。
//
// 与补全的关键差异：**只允许点名路由**。嵌入向量与训练模型强绑定——不同
// 模型的向量空间不可混用，混用会让检索结果静默失真，因此按能力择优回落
// 在这里是制造静默数据污染，必须禁用（§6.2 的极端形式）。
// 未点名、点名的模型未登记、或登记的模型未声明 Embeddings 能力，一律
// UNAVAILABLE 明确失败。
func (g *Gateway) Embed(ctx context.Context, tenantID ids.ID, req EmbeddingRequest) (EmbeddingResponse, error) {
	if tenantID.IsZero() {
		return EmbeddingResponse{}, errors.E(errors.CodeValidationFailed, "llm: tenantID 不能为空")
	}
	if strings.TrimSpace(req.ModelID) == "" {
		return EmbeddingResponse{}, errors.E(errors.CodeUnavailable,
			"llm: 嵌入调用必须点名已登记且支持 embeddings 的模型（向量维度与模型强绑定，禁止能力择优回落）")
	}
	// 1. 限流（与补全共用同一档位与计数器：嵌入是真实的模型调用，消耗配额）。
	if g.Limiter != nil {
		release, err := g.Limiter.Acquire(ctx, tenantID, g.kind())
		if err != nil {
			return EmbeddingResponse{}, err
		}
		defer release()
	}
	// 2. 预算：嵌入只有输入侧费用，按文本规模粗估（与 estimateCost 同量级系数）。
	if g.Budget != nil {
		in := 0
		for _, t := range req.Texts {
			in += len(t)/4 + 1
		}
		if err := g.Budget.Check(ctx, tenantID, float64(in)*0.000001); err != nil {
			return EmbeddingResponse{}, err
		}
	}
	// 3. 点名路由（仅 Routing 命中且 Provider 实现 EmbeddingProvider 且能力声明 Embeddings）。
	resolved, err := g.router().ResolveEmbedding(req)
	if err != nil {
		return EmbeddingResponse{}, err
	}
	if resolved.EndpointID.IsZero() {
		g.log().Warn("端点身份缺失：用量账本将无法落账（Provider 未经 Model Registry 装配）",
			"provider", resolved.Provider.Name(), "model", req.ModelID)
	}
	// 4. 调用。
	ep, ok := resolved.Provider.(EmbeddingProvider)
	if !ok {
		return EmbeddingResponse{}, errors.Ef(errors.CodeUnavailable,
			"llm: 端点 %s 的适配器未实现向量嵌入，拒绝调用", resolved.Provider.Name())
	}
	resp, err := ep.Embed(ctx, req)
	if err != nil {
		return EmbeddingResponse{}, classifyProviderError(err)
	}
	// 5. 用量记账：embeddings 只有输入侧计费（usage.prompt_tokens）。
	if g.Ledger != nil {
		requestID := req.IdempotencyKey
		if requestID == "" {
			requestID = ids.New().String()
		}
		if err := g.Ledger.Record(ctx, tenantID, Usage{
			RequestID:        requestID,
			EndpointID:       resolved.EndpointID,
			ModelRevisionID:  resolved.ModelRevisionID,
			InputTokens:      int64(resp.InputTokens),
			Source:           UsageSourceProviderReported,
			Currency:         "USD",
			SettlementStatus: SettlementSettled,
		}); err != nil {
			g.log().Warn("用量记账失败", "error", err)
		}
	}
	return resp, nil
}

func (g *Gateway) router() *Router {
	if g.Router != nil {
		return g.Router
	}
	return &Router{Providers: g.Providers}
}

func (g *Gateway) kind() string {
	if g.Kind != "" {
		return g.Kind
	}
	return KindInteractive
}

func (g *Gateway) log() *slog.Logger {
	if g.Logger != nil {
		return g.Logger
	}
	return slog.Default()
}

// usageFor 由请求、响应与路由结果构造账本条目。
// RequestID 取 IdempotencyKey，保证同一逻辑请求重试不重复结算（§16.2）。
//
// EndpointID/ModelRevisionID 必须来自 Resolved：账本的这两列是对账与"Run 使用了
// 哪个端点/模型版本"的唯一依据，缺了它们账本即使写进去也无法追溯（§5.3）。
func (g *Gateway) usageFor(req CompletionRequest, comp Completion, resolved Resolved) Usage {
	requestID := req.IdempotencyKey
	if requestID == "" {
		requestID = ids.New().String()
	}
	return Usage{
		RequestID:        requestID,
		RunID:            req.RunID,
		StepID:           req.StepID,
		EndpointID:       resolved.EndpointID,
		ModelRevisionID:  resolved.ModelRevisionID,
		InputTokens:      int64(comp.InputTokens),
		OutputTokens:     int64(comp.OutputTokens),
		Source:           UsageSourceEstimated,
		Currency:         "USD",
		SettlementStatus: SettlementSettled,
	}
}

// estimateCost 是调用前的粗略费用估算（第九篇 §11.2 步骤 1）。
// 仅用于预算预留判定，不代表真实计价（真实计价规则版本化后由计价服务结算）。
func estimateCost(req CompletionRequest) float64 {
	in := 0
	for _, m := range req.Messages {
		in += len(m.Content)/4 + 1
	}
	out := req.MaxOutputTokens
	if out <= 0 {
		out = 512
	}
	return float64(in)*0.000001 + float64(out)*0.000002
}

// classifyProviderError 把供应商错误映射为已登记错误码（第九篇 §14.1 / contracts/02）。
func classifyProviderError(err error) error {
	if err == nil {
		return nil
	}
	if stderrors.Is(err, context.DeadlineExceeded) || stderrors.Is(err, context.Canceled) {
		return errors.E(errors.CodeModelTimeout, "llm: 模型调用超时或取消")
	}
	ae := errors.As(err)
	if ae.Code != errors.CodeInternal {
		// 已是分类错误（MODEL_* / RATE_LIMITED / UNAVAILABLE / BUDGET_EXCEEDED / ...）。
		return ae
	}
	if ae.Cause == nil {
		return ae
	}
	return errors.Wrap(ae.Cause, errors.CodeModelProviderError, "llm: 模型供应商调用失败")
}
