package llm

import (
	"context"
	"math"
	"testing"
	"time"

	"odap/internal/platform/clock"
	"odap/internal/platform/ids"
)

// fakeEmbedder 是受控的文本→向量映射（用于 L2 阈值边界测试）。
type fakeEmbedder struct {
	vecs map[string][]float32
	err  error
}

func (f *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, ok := f.vecs[t]
		if !ok {
			v = []float32{0, 0}
		}
		out[i] = v
	}
	return out, nil
}

// fakeRules 是可编程的 L3 规则命中器。
type fakeRules struct {
	answer string
	hit    bool
	err    error
}

func (f *fakeRules) Match(ctx context.Context, tenantID ids.ID, query string) (string, bool, error) {
	return f.answer, f.hit, f.err
}

func testClock() *clock.FakeClock {
	return &clock.FakeClock{Current: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)}
}

func reqWith(model, class, content string) CompletionRequest {
	return CompletionRequest{
		ModelID:            model,
		DataClassification: class,
		Messages:           []Message{{Role: "user", Content: content}},
	}
}

// TestL1CacheHitAndDowngrade 验证 L1 精确命中和"复用结果"降权（§12 关键约束 2）。
func TestL1CacheHitAndDowngrade(t *testing.T) {
	tenant := ids.New()
	c := NewSemanticCache(nil, nil, testClock())
	req := reqWith("m1", DataClassInternal, "报销需要哪些凭证")
	if err := c.Store(context.Background(), tenant, req, Completion{Content: "凭证清单", ModelRevision: "r1"}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	comp, hit, layer, err := c.Lookup(context.Background(), tenant, req)
	if err != nil || !hit {
		t.Fatalf("L1 Lookup hit=%v err=%v", hit, err)
	}
	if layer != CacheL1 || comp.CacheLayer != CacheL1 || !comp.CacheHit {
		t.Fatalf("layer=%s comp.Layer=%s comp.CacheHit=%v", layer, comp.CacheLayer, comp.CacheHit)
	}
	if comp.Content != "凭证清单" {
		t.Fatalf("content=%q", comp.Content)
	}
	if w := DowngradeWeight(comp); !(w < 1) {
		t.Fatalf("DowngradeWeight=%v, want <1", w)
	}
	if w := DowngradeWeight(Completion{}); w != 1.0 {
		t.Fatalf("非命中权重=%v, want 1.0", w)
	}
}

// TestL2ThresholdBoundary 验证 L2 相似度阈值边界（>= 阈值命中，< 阈值不命中）。
func TestL2ThresholdBoundary(t *testing.T) {
	tenant := ids.New()
	emb := &fakeEmbedder{vecs: map[string][]float32{
		"store": {1, 0},
		"hit":   {0.9, float32(math.Sqrt(1 - 0.9*0.9))},    // cosine = 0.9
		"miss":  {0.88, float32(math.Sqrt(1 - 0.88*0.88))}, // cosine = 0.88
	}}
	c := NewSemanticCache(emb, nil, testClock())
	c.SimilarityThreshold = 0.89

	storeReq := reqWith("m1", DataClassInternal, "store")
	if err := c.Store(context.Background(), tenant, storeReq, Completion{Content: "语义命中", ModelRevision: "r1"}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	// 相似度 0.9 >= 0.89 → 命中 L2。
	hitReq := reqWith("m1", DataClassInternal, "hit")
	comp, hit, layer, err := c.Lookup(context.Background(), tenant, hitReq)
	if err != nil || !hit || layer != CacheL2 {
		t.Fatalf("L2 hit=%v layer=%s err=%v", hit, layer, err)
	}
	if comp.Content != "语义命中" {
		t.Fatalf("content=%q", comp.Content)
	}
	// 相似度 0.88 < 0.89 → 不命中。
	missReq := reqWith("m1", DataClassInternal, "miss")
	if _, hit, _, _ := c.Lookup(context.Background(), tenant, missReq); hit {
		t.Fatalf("相似度低于阈值不应命中")
	}
}

// TestL3RuleHit 验证 L3 本体规则命中。
func TestL3RuleHit(t *testing.T) {
	tenant := ids.New()
	c := NewSemanticCache(nil, &fakeRules{answer: "规则化结论：允许报销", hit: true}, testClock())
	comp, hit, layer, err := c.Lookup(context.Background(), tenant, reqWith("m1", DataClassInternal, "任意查询"))
	if err != nil || !hit || layer != CacheL3 {
		t.Fatalf("L3 hit=%v layer=%s err=%v", hit, layer, err)
	}
	if comp.Content != "规则化结论：允许报销" || !comp.CacheHit {
		t.Fatalf("comp=%+v", comp)
	}
}

// TestHighSensitiveDisablesSemanticCache 验证高敏感分级禁用 L2/L3，
// 且仅当显式开启时才允许 L1（§12.3 关键约束 1）。
func TestHighSensitiveDisablesSemanticCache(t *testing.T) {
	tenant := ids.New()
	emb := &fakeEmbedder{vecs: map[string][]float32{"q": {1, 0}}}
	c := NewSemanticCache(emb, &fakeRules{answer: "x", hit: true}, testClock())
	req := reqWith("m1", DataClassHighSensitive, "q")

	// 未显式开启：任何层都不得命中。
	if err := c.Store(context.Background(), tenant, req, Completion{Content: "c", ModelRevision: "r1"}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, hit, _, _ := c.Lookup(context.Background(), tenant, req); hit {
		t.Fatalf("高敏感且未开启：不应有任何命中")
	}
	// 显式开启：仅 L1 可用。
	c.Enabled = true
	if err := c.Store(context.Background(), tenant, req, Completion{Content: "c", ModelRevision: "r1"}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	comp, hit, layer, _ := c.Lookup(context.Background(), tenant, req)
	if !hit || layer != CacheL1 {
		t.Fatalf("高敏感显式开启应仅命中 L1：hit=%v layer=%s", hit, layer)
	}
	if comp.Content != "c" {
		t.Fatalf("content=%q", comp.Content)
	}
}

// TestCrossTenantNotShared 验证跨租户绝不共享（§12.3 关键约束 4）。
func TestCrossTenantNotShared(t *testing.T) {
	a, b := ids.New(), ids.New()
	c := NewSemanticCache(nil, nil, testClock())
	req := reqWith("m1", DataClassInternal, "同一问题")
	if err := c.Store(context.Background(), a, req, Completion{Content: "a 的答案"}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, hit, _, _ := c.Lookup(context.Background(), b, req); hit {
		t.Fatalf("跨租户不得命中 L1")
	}
	// 同租户应命中。
	if _, hit, _, _ := c.Lookup(context.Background(), a, req); !hit {
		t.Fatalf("同租户应命中")
	}
}

// TestL2CrossTenantNotShared 验证 L2 检索不跨租户比较 embedding。
func TestL2CrossTenantNotShared(t *testing.T) {
	a, b := ids.New(), ids.New()
	emb := &fakeEmbedder{vecs: map[string][]float32{"q": {1, 0}}}
	c := NewSemanticCache(emb, nil, testClock())
	req := reqWith("m1", DataClassInternal, "q")
	if err := c.Store(context.Background(), a, req, Completion{Content: "a", ModelRevision: "r1"}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, hit, _, _ := c.Lookup(context.Background(), b, req); hit {
		t.Fatalf("L2 跨租户不得命中")
	}
}

// TestTTLExpiry 验证 L2 条目 TTL 过期即失效，不自动续期（§12.3 参数表）。
func TestTTLExpiry(t *testing.T) {
	tenant := ids.New()
	clk := testClock()
	emb := &fakeEmbedder{vecs: map[string][]float32{"q": {1, 0}}}
	c := NewSemanticCache(emb, nil, clk)
	c.TTL = time.Hour
	req := reqWith("m1", DataClassInternal, "q")
	if err := c.Store(context.Background(), tenant, req, Completion{Content: "c", ModelRevision: "r1"}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, hit, _, _ := c.Lookup(context.Background(), tenant, req); !hit {
		t.Fatalf("TTL 内应命中")
	}
	clk.Advance(2 * time.Hour)
	if _, hit, _, _ := c.Lookup(context.Background(), tenant, req); hit {
		t.Fatalf("TTL 过期后不应命中")
	}
}

// TestL1CacheDisabledForHighSensitiveByDefault 验证默认不开启时高敏感 L1 也不命中。
func TestL1CacheDisabledForHighSensitiveByDefault(t *testing.T) {
	c := NewSemanticCache(nil, nil, testClock())
	req := reqWith("m1", DataClassRestricted, "x")
	if c.l1Allowed(req.DataClassification) {
		t.Fatalf("RESTRICTED 默认不应允许 L1")
	}
}
