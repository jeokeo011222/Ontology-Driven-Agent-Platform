package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"odap/internal/platform/clock"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// 数据分级（第九篇 §8.1 统一数据分类枚举）。
const (
	DataClassPublic       = "PUBLIC"
	DataClassInternal     = "INTERNAL"
	DataClassConfidential = "CONFIDENTIAL"
	DataClassRestricted   = "RESTRICTED"
	// DataClassHighSensitive 是高敏感聚合标签：命中即按"禁用语义缓存"处理
	// （第九篇 §12.3 关键约束 1），仅允许 L1 且需显式开启。
	DataClassHighSensitive = "HIGH_SENSITIVE"
)

// 缓存命中层级（第九篇 §12.3）。
const (
	CacheL1 = "L1"
	CacheL2 = "L2"
	CacheL3 = "L3"
)

// 语义缓存默认参数（第九篇 §12.3 参数表初始建议值）。
const (
	DefaultSimilarityThreshold = 0.97
	DefaultSemanticTTL         = 24 * time.Hour
	DefaultSemanticTopK        = 5
)

// CacheHitWeight 是缓存命中结果在证据链中的使用权值（<1）。
const CacheHitWeight = 0.3

// Embedder 计算文本向量（L2 语义缓存用）。
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// RuleMatcher 尝试用已发布本体或规则包命中查询（L3 本体规则缓存用）。
type RuleMatcher interface {
	Match(ctx context.Context, tenantID ids.ID, query string) (string, bool, error)
}

type l1Entry struct {
	completion Completion
	expiresAt  time.Time
}

type l2Entry struct {
	tenantID   ids.ID
	modelID    string
	revision   string
	dataClass  string
	query      string
	vector     []float32
	completion Completion
	expiresAt  time.Time
}

// SemanticCache 实现第九篇 §12 的三层语义缓存。
//
// 三层自精确到宽松：L1 精确 digest → L2 语义 embedding 相似度 → L3 本体规则命中。
// 关键约束（§12.3）：
//   - 缓存键含数据分级标签；HIGH_SENSITIVE 直接禁用 L2/L3，仅允许 L1 且需显式开启；
//   - 命中要求同 model_revision、同 data_classification、同租户（跨租户绝不共享）；
//   - 命中响应置 CacheHit=true 并附命中层级；调用方须用 DowngradeWeight 降权。
type SemanticCache struct {
	Embedder Embedder
	Rules    RuleMatcher
	Clock    clock.Clock

	// Enabled 显式开启 L1（高敏感数据仅在此为真时才允许 L1，§12.3 关键约束 1）。
	Enabled bool

	SimilarityThreshold float64
	TTL                 time.Duration
	TopK                int

	mu sync.Mutex
	l1 map[string]l1Entry
	l2 []l2Entry
	// latestRevision 记录每个 (tenant,model,dataClass) 分组的最新 revision，
	// 保证 L2 只在同一 model_revision 内比较（§12.3 关键约束）。
	latestRevision map[string]string
}

// NewSemanticCache 构造语义缓存（参数取默认值；L2/L3 依赖 Embedder/Rules）。
func NewSemanticCache(emb Embedder, rules RuleMatcher, clk clock.Clock) *SemanticCache {
	if clk == nil {
		clk = clock.RealClock{}
	}
	return &SemanticCache{
		Embedder:            emb,
		Rules:               rules,
		Clock:               clk,
		SimilarityThreshold: DefaultSimilarityThreshold,
		TTL:                 DefaultSemanticTTL,
		TopK:                DefaultSemanticTopK,
		l1:                  map[string]l1Entry{},
		latestRevision:      map[string]string{},
	}
}

// isHighSensitive 报告数据分级是否属于"禁止语义缓存"的高敏感集合。
// 第九篇 §12.3 关键约束 1：RESTRICTED 禁用 L2，CONFIDENTIAL 默认禁用 L2。
func isHighSensitive(class string) bool {
	switch class {
	case DataClassHighSensitive, DataClassRestricted, DataClassConfidential:
		return true
	default:
		return false
	}
}

// l1Allowed 报告该分级的请求是否允许 L1 精确缓存。
func (c *SemanticCache) l1Allowed(class string) bool {
	if isHighSensitive(class) {
		return c.Enabled
	}
	return true
}

// Lookup 依次尝试 L1 → L2 → L3；命中时返回置位 CacheHit/CacheLayer 的结果。
func (c *SemanticCache) Lookup(ctx context.Context, tenantID ids.ID, req CompletionRequest) (Completion, bool, string, error) {
	if c == nil {
		return Completion{}, false, "", nil
	}
	if tenantID.IsZero() {
		return Completion{}, false, "", errors.E(errors.CodeValidationFailed, "llm: 语义缓存租户不能为空")
	}
	now := c.now()
	class := req.DataClassification

	c.mu.Lock()
	defer c.mu.Unlock()

	// L1：精确 digest 全量相等。
	if c.l1Allowed(class) {
		key := l1Key(tenantID, req)
		if e, ok := c.l1[key]; ok && now.Before(e.expiresAt) {
			return hit(e.completion, CacheL1), true, CacheL1, nil
		}
	}

	// L2/L3 只服务只读推理且禁用高敏感（§12.3 关键约束 1/3）。
	if !isHighSensitive(class) {
		if c.Embedder != nil {
			if comp, ok := c.lookupL2(ctx, tenantID, req, now); ok {
				return hit(comp, CacheL2), true, CacheL2, nil
			}
		}
		if c.Rules != nil {
			answer, ok, err := c.Rules.Match(ctx, tenantID, queryText(req))
			if err != nil {
				return Completion{}, false, "", errors.Wrap(err, errors.CodeInternal, "llm: L3 规则缓存匹配失败")
			}
			if ok {
				comp := Completion{Content: answer, FinishReason: "stop"}
				return hit(comp, CacheL3), true, CacheL3, nil
			}
		}
	}
	return Completion{}, false, "", nil
}

// lookupL2 在调用方已持锁的前提下执行 L2 语义相似匹配。
func (c *SemanticCache) lookupL2(ctx context.Context, tenantID ids.ID, req CompletionRequest, now time.Time) (Completion, bool) {
	qtext := queryText(req)
	vecs, err := c.Embedder.Embed(ctx, []string{qtext})
	if err != nil || len(vecs) == 0 || len(vecs[0]) == 0 {
		return Completion{}, false
	}
	queryVec := vecs[0]

	// 仅比较同一 (tenant, model, dataClass) 且属于最新 revision 的未过期条目。
	revision := c.latestRevision[groupKey(tenantID, req.ModelID, req.DataClassification)]
	type cand struct {
		sim  float64
		comp Completion
	}
	var cands []cand
	for _, e := range c.l2 {
		if e.tenantID != tenantID || e.modelID != req.ModelID || e.dataClass != req.DataClassification {
			continue
		}
		if !now.Before(e.expiresAt) {
			continue
		}
		if revision != "" && e.revision != revision {
			continue
		}
		sim := cosine(queryVec, e.vector)
		if sim >= c.threshold() {
			cands = append(cands, cand{sim: sim, comp: e.completion})
		}
	}
	if len(cands) == 0 {
		return Completion{}, false
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].sim > cands[j].sim })
	if k := c.topK(); len(cands) > k {
		cands = cands[:k] // 仅与最近 K 条候选比较，控制检索成本（§12.3 参数表）。
	}
	return cands[0].comp, true
}

// Store 写入缓存：L1 始终尝试（受 l1Allowed 约束）；L2 需 Embedder 且非高敏感。
// L3 由外部规则包维护，不在此写入。
func (c *SemanticCache) Store(ctx context.Context, tenantID ids.ID, req CompletionRequest, comp Completion) error {
	if c == nil {
		return nil
	}
	if tenantID.IsZero() {
		return errors.E(errors.CodeValidationFailed, "llm: 语义缓存租户不能为空")
	}
	class := req.DataClassification
	stored := comp
	stored.CacheHit = false
	stored.CacheLayer = ""
	now := c.now()
	expires := now.Add(c.ttl())

	c.mu.Lock()
	if c.l1Allowed(class) {
		c.l1[l1Key(tenantID, req)] = l1Entry{completion: stored, expiresAt: expires}
	}
	needL2 := !isHighSensitive(class) && c.Embedder != nil
	c.mu.Unlock()

	if needL2 {
		qtext := queryText(req)
		vecs, err := c.Embedder.Embed(ctx, []string{qtext})
		if err != nil {
			return err
		}
		if len(vecs) == 0 || len(vecs[0]) == 0 {
			return nil
		}
		c.mu.Lock()
		c.l2 = append(c.l2, l2Entry{
			tenantID:   tenantID,
			modelID:    req.ModelID,
			revision:   comp.ModelRevision,
			dataClass:  class,
			query:      qtext,
			vector:     vecs[0],
			completion: stored,
			expiresAt:  expires,
		})
		c.latestRevision[groupKey(tenantID, req.ModelID, class)] = comp.ModelRevision
		c.mu.Unlock()
	}
	return nil
}

func (c *SemanticCache) now() time.Time {
	if c.Clock == nil {
		return clock.RealClock{}.Now()
	}
	return c.Clock.Now().UTC()
}

func (c *SemanticCache) threshold() float64 {
	if c.SimilarityThreshold <= 0 {
		return DefaultSimilarityThreshold
	}
	return c.SimilarityThreshold
}

func (c *SemanticCache) ttl() time.Duration {
	if c.TTL <= 0 {
		return DefaultSemanticTTL
	}
	return c.TTL
}

func (c *SemanticCache) topK() int {
	if c.TopK <= 0 {
		return DefaultSemanticTopK
	}
	return c.TopK
}

// hit 标记命中结果的层级与原始 request_id（§12.3 关键约束 2）。
func hit(comp Completion, layer string) Completion {
	comp.CacheHit = true
	comp.CacheLayer = layer
	return comp
}

// DowngradeWeight 返回缓存命中结果在证据链中的使用权重。
//
// 第九篇 §12.3 关键约束 2：命中响应必须回传 cache_hit=true 并附命中层级与原始
// request_id；Agent 侧必须把命中结果降权使用——命中条目在证据链中标记为
// "复用结果"而不是"本次新证据"，不得作为新的事实来源写入证据库，也不得因命中
// 而跳过身份校验、授权、数据新鲜度检查与业务验证（§12.2）。
//
// 返回 <1 的权重（CacheHitWeight = 0.3）用于把"复用结果"与"本次新证据"区分开。
func DowngradeWeight(c Completion) float64 {
	if c.CacheHit {
		return CacheHitWeight
	}
	return 1.0
}

// l1Key 计算 L1 精确缓存键（第九篇 §12.1/§12.3）：
// sha256(model_id || 确定性参数 || messages 规范化摘要 || schema 摘要 || data_classification)。
// 租户维度由调用方隔离，不参与摘要以免跨租户比较（跨租户绝不共享）。
func l1Key(tenantID ids.ID, req CompletionRequest) string {
	// 用租户前缀做命名空间隔离：同摘要不同租户绝不共享条目。
	h := sha256.New()
	h.Write([]byte("tenant=" + tenantID.String() + "\x00"))
	h.Write([]byte("model=" + req.ModelID + "\x00"))
	h.Write([]byte("class=" + req.DataClassification + "\x00"))
	if req.Temperature != nil {
		fmt.Fprintf(h, "temp=%.6f\x00", *req.Temperature)
	}
	if req.Seed != nil {
		fmt.Fprintf(h, "seed=%d\x00", *req.Seed)
	}
	fmt.Fprintf(h, "max=%d\x00", req.MaxOutputTokens)
	h.Write([]byte("msgs="))
	for _, m := range req.Messages {
		h.Write([]byte(m.Role))
		h.Write([]byte{0})
		h.Write([]byte(m.Content))
		h.Write([]byte{0})
		for _, tc := range m.ToolCalls {
			h.Write([]byte(tc.ID))
			h.Write([]byte(tc.Name))
			h.Write(tc.Arguments)
			h.Write([]byte{0})
		}
	}
	h.Write([]byte("schema="))
	h.Write(canonicalJSON(req.ResponseSchema))
	h.Write([]byte("tools="))
	for _, t := range req.Tools {
		h.Write([]byte(t.Name))
		h.Write(canonicalJSON(t.InputSchema))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func groupKey(tenantID ids.ID, modelID, dataClass string) string {
	return tenantID.String() + "|" + modelID + "|" + dataClass
}

// queryText 生成 L2/L3 的规范化查询文本。
func queryText(req CompletionRequest) string {
	var b strings.Builder
	for i, m := range req.Messages {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(strings.TrimSpace(m.Content))
	}
	return strings.TrimSpace(b.String())
}

// cosine 计算两个向量的余弦相似度；任一为零向量时返回 0。
func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
