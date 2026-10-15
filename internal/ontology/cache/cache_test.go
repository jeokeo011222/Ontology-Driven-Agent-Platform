package cache

import (
	"testing"
	"time"

	"odap/internal/platform/clock"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

func newTestCache(maxEntries int, ttl time.Duration) (*Cache, *clock.FakeClock) {
	clk := &clock.FakeClock{Current: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	return New(clk, maxEntries, ttl), clk
}

func baseKey(tenant ids.ID) Key {
	return Key{
		TenantID:         tenant,
		GraphIRI:         "https://ex/graph/instance",
		OntologyVersion:  "finance@1.2.0",
		DataGraphVersion: "v1",
		QueryDigest:      "sha256:abc",
		Permission:       ProjectionWidest,
	}
}

func TestGetPutHitMiss(t *testing.T) {
	c, _ := newTestCache(8, time.Minute)
	key := baseKey(ids.New())

	if _, ok := c.Get(key); ok {
		t.Fatal("空缓存不应命中")
	}
	if err := c.Put(key, "rows-1"); err != nil {
		t.Fatalf("Put 失败: %v", err)
	}
	got, ok := c.Get(key)
	if !ok || got != "rows-1" {
		t.Fatalf("应命中并返回值，实际 ok=%v got=%v", ok, got)
	}
	if c.Len() != 1 {
		t.Fatalf("应有 1 条，实际 %d", c.Len())
	}
}

func TestTTLExpiry(t *testing.T) {
	c, clk := newTestCache(8, time.Minute)
	key := baseKey(ids.New())
	if err := c.Put(key, "x"); err != nil {
		t.Fatalf("Put 失败: %v", err)
	}
	if _, ok := c.Get(key); !ok {
		t.Fatal("TTL 到期前应命中")
	}
	clk.Advance(time.Minute) // 恰好到期（now 不早于 expiresAt）
	if _, ok := c.Get(key); ok {
		t.Fatal("TTL 到期后不应命中")
	}
	if c.Len() != 0 {
		t.Fatalf("过期条目应被清除，实际 %d", c.Len())
	}
}

func TestVersionAdvanceInvalidates(t *testing.T) {
	c, _ := newTestCache(8, time.Minute)
	tenant := ids.New()
	v1 := baseKey(tenant)
	if err := c.Put(v1, "old"); err != nil {
		t.Fatalf("Put 失败: %v", err)
	}

	// 数据图版本推进（等价于 ERP 回执后语义实例刷新）。
	v2 := v1
	v2.DataGraphVersion = "v2"
	if err := c.Put(v2, "new"); err != nil {
		t.Fatalf("Put 失败: %v", err)
	}

	if _, ok := c.Get(v1); ok {
		t.Fatal("版本推进后旧版本条目必须失效")
	}
	if got, ok := c.Get(v2); !ok || got != "new" {
		t.Fatalf("新版本应命中，实际 ok=%v got=%v", ok, got)
	}
	if c.Len() != 1 {
		t.Fatalf("旧版本条目应被清除，实际 %d", c.Len())
	}
}

func TestOntologyVersionAdvanceInvalidates(t *testing.T) {
	c, _ := newTestCache(8, time.Minute)
	tenant := ids.New()
	v1 := baseKey(tenant)
	_ = c.Put(v1, "old")
	v2 := v1
	v2.OntologyVersion = "finance@1.3.0"
	_ = c.Put(v2, "new")
	if _, ok := c.Get(v1); ok {
		t.Fatal("本体版本推进后旧条目必须失效")
	}
}

func TestPermissionProjectionNotReused(t *testing.T) {
	c, _ := newTestCache(8, time.Minute)
	tenant := ids.New()
	widest := baseKey(tenant)

	if err := c.Put(widest, "widest-rows"); err != nil {
		t.Fatalf("最宽投影应可缓存: %v", err)
	}
	if _, ok := c.Get(widest); !ok {
		t.Fatal("最宽投影应命中")
	}

	// 收窄权限投影的查询不得复用最宽投影缓存。
	narrow := widest
	narrow.Permission = PermissionProjection("DEPT:FIN")
	if _, ok := c.Get(narrow); ok {
		t.Fatal("收窄投影不得复用最宽投影的缓存条目")
	}
	// 也不得把收窄投影结果写入共享缓存。
	if err := c.Put(narrow, "narrow-rows"); err == nil {
		t.Fatal("收窄投影写入应被拒绝")
	} else if appErr := errors.As(err); appErr.Code != errors.CodePermissionDenied {
		t.Fatalf("错误码应为 %s，实际 %s", errors.CodePermissionDenied, appErr.Code)
	}
}

func TestRejectCrossTenantProjection(t *testing.T) {
	c, _ := newTestCache(8, time.Minute)
	key := baseKey(ids.New())
	key.Permission = ProjectionCrossTenant

	if err := c.Put(key, "leak"); err == nil {
		t.Fatal("跨租户投影写入必须被拒绝")
	} else if appErr := errors.As(err); appErr.Code != errors.CodePermissionDenied {
		t.Fatalf("错误码应为 %s，实际 %s", errors.CodePermissionDenied, appErr.Code)
	}
	if _, ok := c.Get(key); ok {
		t.Fatal("跨租户投影读取必须未命中")
	}
}

func TestPutRejectsZeroTenant(t *testing.T) {
	c, _ := newTestCache(8, time.Minute)
	key := baseKey(ids.ID{})
	if err := c.Put(key, "x"); errors.As(err).Code != errors.CodeValidationFailed {
		t.Fatal("零值 tenant 应返回 VALIDATION_FAILED")
	}
}

func TestInvalidateGraph(t *testing.T) {
	c, _ := newTestCache(8, time.Minute)
	tenant := ids.New()
	k1 := baseKey(tenant)
	k2 := k1
	k2.QueryDigest = "sha256:def"
	_ = c.Put(k1, "a")
	_ = c.Put(k2, "b")

	other := k1
	other.GraphIRI = "https://ex/graph/other"
	_ = c.Put(other, "c")
	if c.Len() != 3 {
		t.Fatalf("应有 3 条，实际 %d", c.Len())
	}

	c.InvalidateGraph("https://ex/graph/instance")
	if c.Len() != 1 {
		t.Fatalf("失效后应仅剩 1 条，实际 %d", c.Len())
	}
	if _, ok := c.Get(other); !ok {
		t.Fatal("其他命名图的条目不应被误删")
	}
}

func TestLRUEviction(t *testing.T) {
	c, _ := newTestCache(2, time.Minute)
	tenant := ids.New()
	a := baseKey(tenant)
	a.QueryDigest = "a"
	b := baseKey(tenant)
	b.QueryDigest = "b"
	d := baseKey(tenant)
	d.QueryDigest = "d"

	_ = c.Put(a, "a")
	_ = c.Put(b, "b")
	if _, ok := c.Get(a); !ok { // 让 a 成为最近使用
		t.Fatal("a 应命中")
	}
	_ = c.Put(d, "d") // 触发淘汰，应淘汰 b

	if _, ok := c.Get(b); ok {
		t.Fatal("b 应被 LRU 淘汰")
	}
	if _, ok := c.Get(a); !ok {
		t.Fatal("a 应保留")
	}
	if _, ok := c.Get(d); !ok {
		t.Fatal("d 应保留")
	}
}

func TestNewDefaults(t *testing.T) {
	c := New(nil, 0, 0)
	if c.maxEntries != DefaultMaxEntries || c.ttl != DefaultTTL {
		t.Fatalf("默认值不正确: max=%d ttl=%v", c.maxEntries, c.ttl)
	}
	key := baseKey(ids.New())
	if err := c.Put(key, 1); err != nil {
		t.Fatalf("Put 失败: %v", err)
	}
	if _, ok := c.Get(key); !ok {
		t.Fatal("默认缓存应命中")
	}
}
