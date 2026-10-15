package llm

import (
	"context"
	"testing"
	"time"

	"odap/internal/platform/clock"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// TestBucketRefillRate 验证令牌补充速率（毫秒级回填）。
func TestBucketRefillRate(t *testing.T) {
	now := time.Unix(0, 0)
	b := NewBucket(5, 2, now) // 容量 5，每秒回填 2
	for i := 0; i < 5; i++ {
		if !b.Allow(now) {
			t.Fatalf("第 %d 次应放行", i+1)
		}
	}
	if b.Allow(now) {
		t.Fatalf("令牌耗尽后不应放行")
	}
	now = now.Add(time.Second) // 回填 2 个
	if !b.Allow(now) || !b.Allow(now) {
		t.Fatalf("回填 2 个令牌后两次应放行")
	}
	if b.Allow(now) {
		t.Fatalf("仅回填 2 个令牌，第三次不应放行")
	}
}

// TestBucketBurstCapacity 验证突发容量上限。
func TestBucketBurstCapacity(t *testing.T) {
	now := time.Unix(0, 0)
	b := NewBucket(3, 0.001, now)
	for i := 0; i < 3; i++ {
		if !b.Allow(now) {
			t.Fatalf("突发容量 %d 内应放行", i+1)
		}
	}
	if b.Allow(now) {
		t.Fatalf("超出突发容量不应放行")
	}
}

func testLimiter() *Limiter {
	l := NewLimiter(&clock.FakeClock{Current: time.Unix(0, 0)})
	l.Caps = map[string]BucketParams{} // 关闭单租户上限钳制，便于精确断言
	return l
}

// TestLimiterConcurrencyLimit 验证并发上限与释放回收。
func TestLimiterConcurrencyLimit(t *testing.T) {
	l := testLimiter()
	l.Params = map[string]BucketParams{
		KindInteractive: {RPM: 6000, Concurrency: 2, Burst: 100},
	}
	tenant := ids.New()
	ctx := context.Background()
	r1, err := l.Acquire(ctx, tenant, KindInteractive)
	if err != nil {
		t.Fatalf("第 1 次 Acquire: %v", err)
	}
	if _, err := l.Acquire(ctx, tenant, KindInteractive); err != nil {
		t.Fatalf("第 2 次 Acquire: %v", err)
	}
	if _, err := l.Acquire(ctx, tenant, KindInteractive); errors.As(err).Code != errors.CodeRateLimited {
		t.Fatalf("超出并发上限应返回 RATE_LIMITED，got %v", err)
	}
	r1() // 释放一个名额
	if _, err := l.Acquire(ctx, tenant, KindInteractive); err != nil {
		t.Fatalf("释放后应可再次 Acquire: %v", err)
	}
}

// TestLimiterRejectionAndRetryAfter 验证交互式超限立即拒绝并给出 Retry-After 数值。
func TestLimiterRejectionAndRetryAfter(t *testing.T) {
	l := testLimiter()
	l.Params = map[string]BucketParams{
		KindInteractive: {RPM: 60, Concurrency: 100, Burst: 1}, // 每 60s 1 个令牌
	}
	tenant := ids.New()
	ctx := context.Background()
	release, err := l.Acquire(ctx, tenant, KindInteractive)
	if err != nil {
		t.Fatalf("首次 Acquire: %v", err)
	}
	defer release()

	_, err = l.Acquire(ctx, tenant, KindInteractive)
	if errors.As(err).Code != errors.CodeRateLimited {
		t.Fatalf("令牌不足应返回 RATE_LIMITED，got %v", err)
	}
	secs, ok := RetryAfterSeconds(err)
	if !ok {
		t.Fatalf("限流错误应携带 retry_after_seconds")
	}
	if secs < 0.9 || secs > 1.1 {
		t.Fatalf("retry_after_seconds=%v, want ≈1.0", secs)
	}
}

// TestLimiterQueueCapacity 验证可排队任务的队列容量上限（超限拒绝）。
func TestLimiterQueueCapacity(t *testing.T) {
	l := testLimiter()
	l.Params = map[string]BucketParams{
		KindBatch: {RPM: 60, Concurrency: 1, Burst: 1},
	}
	l.QueueCapPerTenant = 2
	tenant := ids.New()
	ctx := context.Background()

	// 占满唯一并发名额。
	if _, err := l.Acquire(ctx, tenant, KindBatch); err != nil {
		t.Fatalf("首次 Acquire: %v", err)
	}
	// 后续请求应进入有界队列。
	for i := 0; i < 2; i++ {
		if _, err := l.Acquire(ctx, tenant, KindBatch); err != nil {
			t.Fatalf("第 %d 次入队应成功: %v", i+1, err)
		}
	}
	if got := l.QueuedCount(tenant, KindBatch); got != 2 {
		t.Fatalf("队列长度=%d, want 2", got)
	}
	// 队列已满：拒绝。
	if _, err := l.Acquire(ctx, tenant, KindBatch); errors.As(err).Code != errors.CodeRateLimited {
		t.Fatalf("队列满应返回 RATE_LIMITED，got %v", err)
	}
}

// TestLimiterUnknownKind 验证未知档位拒绝。
func TestLimiterUnknownKind(t *testing.T) {
	l := testLimiter()
	if _, err := l.Acquire(context.Background(), ids.New(), "NOPE"); errors.As(err).Code != errors.CodeValidationFailed {
		t.Fatalf("未知档位应返回 VALIDATION_FAILED，got %v", err)
	}
}

// TestLimiterZeroTenant 验证零租户拒绝（fail-closed）。
func TestLimiterZeroTenant(t *testing.T) {
	l := testLimiter()
	if _, err := l.Acquire(context.Background(), ids.ID{}, KindInteractive); errors.As(err).Code != errors.CodeValidationFailed {
		t.Fatalf("零租户应返回 VALIDATION_FAILED，got %v", err)
	}
}
