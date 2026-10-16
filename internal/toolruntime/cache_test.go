package toolruntime

import (
	"testing"
	"time"
)

// newTestCache 构造可注入时钟的缓存。
func newTestCache(ttl time.Duration) (*ResultCache, *time.Time) {
	c := NewResultCache(ttl)
	base := time.Unix(1700000000, 0)
	c.now = func() time.Time { return base }
	return c, &base
}

func TestResultCacheHitAndMiss(t *testing.T) {
	c, _ := newTestCache(time.Minute)
	args := map[string]any{"invoice_id": "INV-1"}

	if _, ok := c.Get("erp.read", "1.0.0", args); ok {
		t.Fatal("空缓存不应命中")
	}
	c.Put("erp.read", "1.0.0", args, map[string]any{"total": 42})

	got, ok := c.Get("erp.read", "1.0.0", args)
	if !ok || got["total"] != float64(42) {
		t.Fatalf("got = %v, ok = %v, want 命中且 total=42", got, ok)
	}

	// 不同版本是不同键。
	if _, ok := c.Get("erp.read", "2.0.0", args); ok {
		t.Fatal("不同版本不应命中")
	}
	// 不同工具是不同键。
	if _, ok := c.Get("ledger.query", "1.0.0", args); ok {
		t.Fatal("不同工具不应命中")
	}
}

// TestResultCacheKeyOrderIndependent 参数 map 的键序不影响命中
// （依赖 json.Marshal 的键排序保证）。
func TestResultCacheKeyOrderIndependent(t *testing.T) {
	c, _ := newTestCache(time.Minute)
	// 两个 map 内容相同、构造顺序不同。
	a := map[string]any{"a": 1, "b": 2, "c": 3}
	b := map[string]any{"c": 3, "a": 1, "b": 2}
	c.Put("erp.read", "1.0.0", a, map[string]any{"ok": true})
	if _, ok := c.Get("erp.read", "1.0.0", b); !ok {
		t.Fatal("同内容不同键序的参数应命中")
	}
}

func TestResultCacheTTLExpiry(t *testing.T) {
	c, base := newTestCache(time.Minute)
	args := map[string]any{"id": "x"}
	c.Put("erp.read", "1.0.0", args, map[string]any{"ok": true})

	// 时间前进到 TTL 边界内 → 命中。
	*base = base.Add(59 * time.Second)
	if _, ok := c.Get("erp.read", "1.0.0", args); !ok {
		t.Fatal("TTL 内应命中")
	}
	// 前进到过期 → miss（惰性过期会顺带删除条目）。
	*base = base.Add(2 * time.Second)
	if _, ok := c.Get("erp.read", "1.0.0", args); ok {
		t.Fatal("过期后应 miss")
	}
	// 过期后重新 Put 应正常工作。
	c.Put("erp.read", "1.0.0", args, map[string]any{"ok": true, "again": true})
	if got, ok := c.Get("erp.read", "1.0.0", args); !ok || got["again"] != true {
		t.Fatalf("过期后重新写入应可命中，got = %v", got)
	}
}

// TestResultCacheDisabled 锁定禁用态语义：ttl <= 0 时 Get 恒 miss、
// Put 为 no-op——调用方无需判空，行为与"没有缓存"完全一致。
func TestResultCacheDisabled(t *testing.T) {
	for _, ttl := range []time.Duration{0, -1, -time.Hour} {
		c := NewResultCache(ttl)
		args := map[string]any{"id": "x"}
		c.Put("erp.read", "1.0.0", args, map[string]any{"ok": true})
		if _, ok := c.Get("erp.read", "1.0.0", args); ok {
			t.Fatalf("ttl=%v 禁用态不应命中", ttl)
		}
	}
	// nil 接收者同样安全（调用方可能没装配缓存）。
	var c *ResultCache
	if _, ok := c.Get("k", "v", nil); ok {
		t.Fatal("nil 缓存不应命中")
	}
	c.Put("k", "v", nil, map[string]any{})
	c.InvalidateAll() // 不得 panic
}

// TestResultCacheInvalidateAll 任何写入成功 → 整体失效（粗粒度是唯一不漏）。
func TestResultCacheInvalidateAll(t *testing.T) {
	c, _ := newTestCache(time.Minute)
	c.Put("erp.read", "1.0.0", map[string]any{"id": "1"}, map[string]any{"a": 1})
	c.Put("ledger.query", "1.0.0", map[string]any{"id": "2"}, map[string]any{"b": 2})

	c.InvalidateAll()
	if _, ok := c.Get("erp.read", "1.0.0", map[string]any{"id": "1"}); ok {
		t.Fatal("InvalidateAll 后 erp.read 不应命中")
	}
	if _, ok := c.Get("ledger.query", "1.0.0", map[string]any{"id": "2"}); ok {
		t.Fatal("InvalidateAll 后 ledger.query 不应命中")
	}
}

// TestResultCacheDunderKeysExcluded dunder 键（服务端注入的审批凭据等）
// 不参与键空间——它们不影响只读结果，进键只会造成无谓 miss。
func TestResultCacheDunderKeysExcluded(t *testing.T) {
	c, _ := newTestCache(time.Minute)
	clean := map[string]any{"invoice_id": "INV-1"}
	withDunder := map[string]any{"invoice_id": "INV-1", "__approval_token": "secret-xyz"}

	c.Put("erp.read", "1.0.0", withDunder, map[string]any{"ok": true})
	if _, ok := c.Get("erp.read", "1.0.0", clean); !ok {
		t.Fatal("dunder 键剔除后，干净参数应命中带 dunder 写入的条目")
	}
	if _, ok := c.Get("erp.read", "1.0.0", withDunder); !ok {
		t.Fatal("带 dunder 键的读取也应命中同一键")
	}
}

// TestResultCacheUnserializableArgsSkipped 不可序列化参数 → 双侧完全跳过缓存。
// 绝不能退化成公共键：否则不同参数之间会互相误命中（错误数据比慢更危险）。
func TestResultCacheUnserializableArgsSkipped(t *testing.T) {
	c, _ := newTestCache(time.Minute)
	bad := map[string]any{"fn": func() {}}
	good := map[string]any{"id": "1"}

	// Put：参数不可序列化 → no-op。
	c.Put("erp.read", "1.0.0", bad, map[string]any{"evil": true})
	if _, ok := c.Get("erp.read", "1.0.0", good); ok {
		t.Fatal("不可序列化参数的写入不得污染任何键")
	}

	// 先写入合法条目；Get 带不可序列化参数 → miss（不退化成公共键命中）。
	c.Put("erp.read", "1.0.0", good, map[string]any{"ok": true})
	if _, ok := c.Get("erp.read", "1.0.0", bad); ok {
		t.Fatal("不可序列化参数的读取必须完全跳过缓存")
	}
}

// TestResultCacheEvictsAtCap 达到条目上限 → 整体清空（保守失效语义，不做 LRU）。
func TestResultCacheEvictsAtCap(t *testing.T) {
	c, _ := newTestCache(time.Minute)
	c.Put("erp.read", "1.0.0", map[string]any{"id": "first"}, map[string]any{"a": 1})

	for i := 0; i < maxEntries; i++ {
		c.Put("filler", "1.0.0", map[string]any{"i": i}, map[string]any{"i": i})
	}
	// 上限被填满后再次 Put 触发整体清空，first 条目随之丢失。
	c.Put("trigger", "1.0.0", map[string]any{"i": 0}, map[string]any{})
	if _, ok := c.Get("erp.read", "1.0.0", map[string]any{"id": "first"}); ok {
		t.Fatal("超限清空后旧条目不应保留")
	}
	// 清空后写入的新条目仍可正常命中。
	if _, ok := c.Get("trigger", "1.0.0", map[string]any{"i": 0}); !ok {
		t.Fatal("清空后新写入条目应可命中")
	}
}

// TestResultCacheDeepCopyIsolation 缓存返回值与内部条目互不污染。
func TestResultCacheDeepCopyIsolation(t *testing.T) {
	c, _ := newTestCache(time.Minute)
	args := map[string]any{"id": "x"}
	c.Put("erp.read", "1.0.0", args, map[string]any{"nested": map[string]any{"v": 1}})

	// 改写 Get 返回的嵌套对象，不得污染缓存。
	got, _ := c.Get("erp.read", "1.0.0", args)
	got["nested"].(map[string]any)["v"] = 999
	got["new"] = "side-effect"

	again, _ := c.Get("erp.read", "1.0.0", args)
	if again["nested"].(map[string]any)["v"] != float64(1) {
		t.Fatalf("缓存内条目被外部改写污染：nested.v = %v", again["nested"])
	}
	if _, ok := again["new"]; ok {
		t.Fatal("缓存内条目被外部新增键污染")
	}
}

// TestCacheKeyShape 校验 cacheKey 的结构与 dunder/序列化规则本身。
func TestCacheKeyShape(t *testing.T) {
	k1, ok := cacheKey("tool", "1.0.0", map[string]any{"b": 1, "a": 2})
	if !ok {
		t.Fatal("可序列化参数应产出键")
	}
	k2, _ := cacheKey("tool", "1.0.0", map[string]any{"a": 2, "b": 1})
	if k1 != k2 {
		t.Fatal("键序不同、内容相同应产出同一键")
	}
	if _, ok := cacheKey("tool", "1.0.0", map[string]any{"x": fmtChan()}); ok {
		t.Fatal("含不可序列化值应返回 ok=false")
	}
}

func fmtChan() chan int { return make(chan int) }

// TestResultCacheUnserializableOutputSkipped 编译期防线：确认输出序列化失败时
// Put 静默放弃（channel 类型的输出会 Marshal 失败）。
func TestResultCacheUnserializableOutputSkipped(t *testing.T) {
	c, _ := newTestCache(time.Minute)
	args := map[string]any{"id": "x"}
	c.Put("erp.read", "1.0.0", args, map[string]any{"ch": make(chan int)})
	if _, ok := c.Get("erp.read", "1.0.0", args); ok {
		t.Fatal("输出不可序列化时不得写入任何条目")
	}
}
