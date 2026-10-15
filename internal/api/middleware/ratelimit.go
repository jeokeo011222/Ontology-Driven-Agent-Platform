package middleware

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"odap/internal/api/httperr"
	"odap/internal/platform/errors"
	"odap/internal/platform/tenantctx"
)

// bucket 是单键令牌桶（独立实现，不跨包引用 internal/llm 的限流器）。
type bucket struct {
	tokens   float64
	last     time.Time
	lastUsed time.Time
}

// tokenLimiter 是按键限流的令牌桶集合。
type tokenLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	capacity float64
	refill   float64 // 每秒补充令牌数
	idleTTL  time.Duration
	maxKeys  int
}

const defaultMaxKeys = 8192

func newTokenLimiter(capacity, refillPerSecond float64) *tokenLimiter {
	if capacity < 1 {
		capacity = 1
	}
	if refillPerSecond <= 0 {
		refillPerSecond = capacity
	}
	return &tokenLimiter{
		buckets:  make(map[string]*bucket),
		capacity: capacity,
		refill:   refillPerSecond,
		idleTTL:  10 * time.Minute,
		maxKeys:  defaultMaxKeys,
	}
}

// allow 消费一个令牌；拒绝时返回建议等待时长。
func (l *tokenLimiter) allow(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buckets) > l.maxKeys {
		l.sweepLocked(now)
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.capacity, last: now}
		l.buckets[key] = b
	}
	// 按经过时间补充令牌（上限为桶容量）。
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * l.refill
		if b.tokens > l.capacity {
			b.tokens = l.capacity
		}
		b.last = now
	}
	b.lastUsed = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	needed := 1 - b.tokens
	retryAfter := time.Duration(needed / l.refill * float64(time.Second))
	if retryAfter < time.Second {
		retryAfter = time.Second
	}
	return false, retryAfter
}

// sweepLocked 清理长时间空闲的桶，防止键空间无限增长。
func (l *tokenLimiter) sweepLocked(now time.Time) {
	for k, b := range l.buckets {
		if now.Sub(b.lastUsed) > l.idleTTL {
			delete(l.buckets, k)
		}
	}
}

// RateLimit 对每个键施加令牌桶限流；超限返回 429 RATE_LIMITED 并带 Retry-After。
//
// keyFn 为 nil 时按「租户 ID（无则远端地址）」分桶。
func RateLimit(capacity, refillPerSecond float64, keyFn func(*http.Request) string) func(http.Handler) http.Handler {
	lim := newTokenLimiter(capacity, refillPerSecond)
	if keyFn == nil {
		keyFn = defaultKey
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ok, retryAfter := lim.allow(keyFn(r), time.Now())
			if !ok {
				w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
				httperr.Write(w, RequestIDFromContext(r.Context()),
					errors.E(errors.CodeRateLimited, "请求过于频繁，请稍后重试"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// defaultKey 以租户为限流维度（无身份时退化为远端地址）。
func defaultKey(r *http.Request) string {
	if id, ok := tenantctx.FromContext(r.Context()); ok && !id.TenantID.IsZero() {
		return "tenant:" + id.TenantID.String()
	}
	return "addr:" + r.RemoteAddr
}
