package llm

import (
	"context"
	"math"
	"sync"
	"time"

	"odap/internal/platform/clock"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// 限流档位（第九篇 §14.3 令牌桶参数表的三个维度）。
const (
	KindInteractive = "INTERACTIVE"
	KindBatch       = "BATCH"
	KindHighCost    = "HIGH_COST"
)

// BucketParams 是单档限流参数（第九篇 §14.3）。
type BucketParams struct {
	RPM         float64
	TPM         float64
	Concurrency int
	Burst       float64
}

// DefaultParams 是各档默认参数（第九篇 §14.3 表：交互式/批量/高成本）。
// Burst 默认取单分钟配额的 1.5 倍（批量端点 2 倍）。
var DefaultParams = map[string]BucketParams{
	KindInteractive: {RPM: 60, TPM: 200000, Concurrency: 4, Burst: 90},
	KindBatch:       {RPM: 600, TPM: 2000000, Concurrency: 16, Burst: 1200},
	KindHighCost:    {RPM: 10, TPM: 500000, Concurrency: 2, Burst: 15},
}

// TenantCaps 是单租户上限（第九篇 §14.3 表右列）。
var TenantCaps = map[string]BucketParams{
	KindInteractive: {RPM: 600, TPM: 2000000, Concurrency: 32},
	KindBatch:       {RPM: 6000, TPM: 20000000, Concurrency: 128},
	KindHighCost:    {RPM: 60, TPM: 3000000, Concurrency: 8},
}

// DefaultQueueCapPerTenant 是每租户限流队列容量上限（第九篇 §14.3）。
const DefaultQueueCapPerTenant = 200

// Bucket 是令牌桶（第九篇 §14.3：令牌按毫秒级补充速率持续回填）。
// 使用方需自行保证串行访问（Limiter 持锁调用）。
type Bucket struct {
	Capacity        float64
	RefillPerSecond float64
	tokens          float64
	last            time.Time
}

// NewBucket 构造一个满桶。
func NewBucket(capacity, refillPerSecond float64, now time.Time) *Bucket {
	return &Bucket{Capacity: capacity, RefillPerSecond: refillPerSecond, tokens: capacity, last: now}
}

// Allow 尝试消耗 1 个令牌；不足或容量为 0 时返回 false。
func (b *Bucket) Allow(now time.Time) bool { return b.allowN(now, 1) }

// allowN 尝试消耗 n 个令牌。
func (b *Bucket) allowN(now time.Time, n float64) bool {
	if b == nil || b.Capacity <= 0 {
		return false
	}
	b.refill(now)
	if b.tokens >= n {
		b.tokens -= n
		return true
	}
	return false
}

// RetryAfter 返回补足 n 个令牌所需的等待时间（用于 HTTP Retry-After）。
func (b *Bucket) RetryAfter(now time.Time, n float64) time.Duration {
	if b == nil || b.RefillPerSecond <= 0 {
		return 0
	}
	b.refill(now)
	need := n - b.tokens
	if need <= 0 {
		return 0
	}
	return time.Duration(math.Ceil(need/b.RefillPerSecond*1000)) * time.Millisecond
}

// Tokens 返回当前令牌数（测试观测用）。
func (b *Bucket) Tokens() float64 {
	if b == nil {
		return 0
	}
	return b.tokens
}

// refill 依据流逝时间回填令牌。
func (b *Bucket) refill(now time.Time) {
	if now.After(b.last) {
		elapsed := now.Sub(b.last).Seconds()
		b.tokens = math.Min(b.Capacity, b.tokens+elapsed*b.RefillPerSecond)
		b.last = now
	}
}

// Limiter 是令牌桶限流与租户配额治理（第九篇 §14.3）。
//
// 按 (tenant, 端点档位) 维度维护桶；租户配额采用"公平份额 + 突发桶"：
// tenant_share = endpoint_quota / active_tenants，突发容量 = 1.5 × tenant_share。
// 超限行为按请求性质判定：交互式请求立即拒绝（CodeRateLimited + Retry-After），
// 可排队任务入有界队列（超限拒绝）。
type Limiter struct {
	Clock  clock.Clock
	Params map[string]BucketParams
	Caps   map[string]BucketParams

	// ActiveTenants 是滚动窗口内的活跃租户数，用于公平份额计算（默认 1）。
	ActiveTenants int

	// QueueCapPerTenant 是每租户队列容量上限（默认 DefaultQueueCapPerTenant）。
	QueueCapPerTenant int

	mu       sync.Mutex
	buckets  map[string]*Bucket
	inflight map[string]int
	queues   map[string][]ids.ID
}

// NewLimiter 构造限流器（参数缺省时用 DefaultParams / TenantCaps）。
func NewLimiter(clk clock.Clock) *Limiter {
	if clk == nil {
		clk = clock.RealClock{}
	}
	return &Limiter{
		Clock:    clk,
		Params:   DefaultParams,
		Caps:     TenantCaps,
		buckets:  map[string]*Bucket{},
		inflight: map[string]int{},
		queues:   map[string][]ids.ID{},
	}
}

// Acquire 申请一次调用许可（消耗 1 个 RPM 令牌 + 1 个并发名额）。
//
// 返回的 Release 必须在调用结束后调用以释放并发名额；超限时返回
// 携带 retry_after_seconds 的 CodeRateLimited。
func (l *Limiter) Acquire(ctx context.Context, tenantID ids.ID, kind string) (func(), error) {
	return l.acquire(ctx, tenantID, kind, 1)
}

// AcquireTokens 在 RPM 之外额外消耗 tokens 个令牌（TPM 维度记账用）。
func (l *Limiter) AcquireTokens(ctx context.Context, tenantID ids.ID, kind string, tokens float64) (func(), error) {
	if tokens < 1 {
		tokens = 1
	}
	return l.acquire(ctx, tenantID, kind, tokens)
}

func (l *Limiter) acquire(ctx context.Context, tenantID ids.ID, kind string, tokens float64) (func(), error) {
	if tenantID.IsZero() {
		return nil, errors.E(errors.CodeValidationFailed, "llm: 限流租户不能为空")
	}
	params, ok := l.paramsFor(kind)
	if !ok {
		return nil, errors.Ef(errors.CodeValidationFailed, "llm: 未知限流档位 %q", kind)
	}
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	key := tenantID.String() + "|" + kind
	// 并发上限（第九篇 §14.3：Agent/端点级并发限制）。
	if params.Concurrency > 0 && l.inflight[key] >= params.Concurrency {
		return l.throttleLocked(key, tenantID, kind, params, now, "并发上限已满")
	}
	// 速率（令牌桶）。
	b := l.bucketLocked(key, params)
	if !b.allowN(now, tokens) {
		return l.throttleLocked(key, tenantID, kind, params, now, "令牌不足")
	}
	l.inflight[key]++
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.inflight[key] > 0 {
			l.inflight[key]--
		}
	}, nil
}

// throttleLocked 依据请求性质决定超限行为（第九篇 §14.3 超限行为判定规则）。
func (l *Limiter) throttleLocked(key string, tenantID ids.ID, kind string, params BucketParams, now time.Time, why string) (func(), error) {
	if isQueueable(kind) {
		qKey := tenantID.String() + "|" + kind
		if len(l.queues[qKey]) < l.queueCapPerTenant() {
			l.queues[qKey] = append(l.queues[qKey], tenantID)
			// 入队成功：调用方据 202 返回任务引用；Release 完成出队。
			return func() {
				l.mu.Lock()
				defer l.mu.Unlock()
				q := l.queues[qKey]
				if len(q) > 0 {
					l.queues[qKey] = q[1:]
				}
			}, nil
		}
		return nil, rateLimitedError("队列容量已达上限，拒绝入队", l.retryAfterLocked(key, now, 1))
	}
	// 交互式请求：立即拒绝，返回 429 + Retry-After（按令牌回填时间计算）。
	return nil, rateLimitedError(why, l.retryAfterLocked(key, now, 1))
}

// retryAfterLocked 由该租户档位的令牌桶估算补足时间。
func (l *Limiter) retryAfterLocked(key string, now time.Time, n float64) time.Duration {
	b := l.buckets[key]
	if b == nil {
		return 0
	}
	return b.RetryAfter(now, n)
}

// bucketLocked 返回（必要时创建）某租户档位的桶，并应用公平份额。
func (l *Limiter) bucketLocked(key string, params BucketParams) *Bucket {
	if b, ok := l.buckets[key]; ok {
		return b
	}
	refill := params.RPM / 60.0
	burst := params.Burst
	if burst <= 0 {
		burst = params.RPM / 40.0 // 1.5×RPM/分钟
	}
	if n := l.activeTenants(); n > 1 {
		// 公平份额：tenant_share = endpoint_quota / active_tenants；
		// 突发桶容量 = 1.5 × tenant_share（第九篇 §14.3）。
		share := params.RPM / float64(n)
		refill = share / 60.0
		burst = 1.5 * share
	}
	if burst <= 0 {
		burst = 1
	}
	b := NewBucket(burst, refill, l.now())
	l.buckets[key] = b
	return b
}

// QueuedCount 返回某租户某档位当前排队数量（测试与指标观测用）。
func (l *Limiter) QueuedCount(tenantID ids.ID, kind string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.queues[tenantID.String()+"|"+kind])
}

// Inflight 返回某租户某档位当前在途并发数（测试与指标观测用）。
func (l *Limiter) Inflight(tenantID ids.ID, kind string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inflight[tenantID.String()+"|"+kind]
}

func (l *Limiter) paramsFor(kind string) (BucketParams, bool) {
	params := l.Params
	if params == nil {
		params = DefaultParams
	}
	p, ok := params[kind]
	if !ok {
		return BucketParams{}, false
	}
	// 单租户上限：不得让某租户超过上限（第九篇 §14.3 表右列）。
	caps := l.Caps
	if caps == nil {
		caps = TenantCaps
	}
	if c, ok := caps[kind]; ok {
		if c.RPM > 0 && p.RPM > c.RPM {
			p.RPM = c.RPM
		}
		if c.Concurrency > 0 && p.Concurrency > c.Concurrency {
			p.Concurrency = c.Concurrency
		}
	}
	return p, true
}

func (l *Limiter) activeTenants() int {
	if l.ActiveTenants > 0 {
		return l.ActiveTenants
	}
	return 1
}

func (l *Limiter) queueCapPerTenant() int {
	if l.QueueCapPerTenant > 0 {
		return l.QueueCapPerTenant
	}
	return DefaultQueueCapPerTenant
}

func (l *Limiter) now() time.Time {
	if l.Clock == nil {
		return clock.RealClock{}.Now()
	}
	return l.Clock.Now().UTC()
}

// isQueueable 报告档位是否属于可排队任务（异步批处理、后台作业）。
func isQueueable(kind string) bool {
	return kind == KindBatch
}

// rateLimitedError 构造 CodeRateLimited 错误并附 Retry-After 秒数（contracts/02 §2）。
func rateLimitedError(msg string, d time.Duration) *errors.AppError {
	secs := math.Ceil(d.Seconds()*1000) / 1000
	if secs < 0 {
		secs = 0
	}
	return errors.E(errors.CodeRateLimited, "llm: "+msg).
		WithDetails(map[string]any{"retry_after_seconds": secs})
}

// RetryAfterSeconds 从限流错误中提取 Retry-After 秒数。
func RetryAfterSeconds(err error) (float64, bool) {
	ae := errors.As(err)
	if ae == nil || ae.Code != errors.CodeRateLimited || ae.Details == nil {
		return 0, false
	}
	switch v := ae.Details["retry_after_seconds"].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	default:
		return 0, false
	}
}
