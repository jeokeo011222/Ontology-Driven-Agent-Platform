// Package toolruntime 提供工具执行期的运行时支撑。
//
// # 只读结果缓存（借鉴竞品 MCP 工具缓存设计）
//
// READ 类工具的执行结果按「工具 + 版本 + 规范化参数」做短 TTL 缓存：
// agent 对同一只读工具的重复调用（同一轮多步推理里极常见）不再重复执行。
// 设计要点全部来自调用方语义：
//   - TTL 默认 60s，可配（env）；TTL <= 0 时缓存整体禁用（Get 恒 false）；
//   - 只缓存 READ：写入类工具的两次同参调用**不**等价（外部系统状态可能已变）；
//   - 任何写入类工具执行成功 → 整体失效（而不是精细失效——工具间的数据依赖
//     无法静态判断，粗粒度失效是唯一不漏的选择）；
//   - 缓存条目上限 5000：超限整体清空。不做 LRU——为省一次重复查询引入
//     逐条淘汰的复杂度不值当，缓存的失效语义本来就偏保守。
package toolruntime

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// DefaultTTL 是未显式配置时的缓存存活时间。
const DefaultTTL = 60 * time.Second

// maxEntries 是缓存条目上限；达到后整体清空（保守失效语义）。
const maxEntries = 5000

type cacheEntry struct {
	output    json.RawMessage
	expiresAt time.Time
}

// ResultCache 是 READ 工具执行结果的进程内 TTL 缓存。并发安全。
type ResultCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]cacheEntry
	now     func() time.Time
}

// NewResultCache 构造缓存。ttl <= 0 时返回禁用态实例（Get 恒 miss、Put 为 no-op），
// 调用方无需判空。
func NewResultCache(ttl time.Duration) *ResultCache {
	return &ResultCache{
		ttl:     ttl,
		entries: map[string]cacheEntry{},
		now:     time.Now,
	}
}

// Get 返回缓存命中（深拷贝：调用方改写返回值不得污染缓存内条目）。
func (c *ResultCache) Get(toolKey, version string, args map[string]any) (map[string]any, bool) {
	if c == nil || c.ttl <= 0 {
		return nil, false
	}
	key, ok := cacheKey(toolKey, version, args)
	if !ok {
		return nil, false
	}
	c.mu.Lock()
	e, ok := c.entries[key]
	if ok && !c.now().Before(e.expiresAt) {
		delete(c.entries, key) // 惰性过期
		ok = false
	}
	c.mu.Unlock()
	if !ok {
		return nil, false
	}
	var out map[string]any
	if err := json.Unmarshal(e.output, &out); err != nil {
		return nil, false // 理论不可达（写入侧已 Marshal 成功）；防御：当 miss
	}
	return out, true
}

// Put 写入缓存（args 或输出序列化失败则静默放弃——缓存是增强项，不是关键路径）。
func (c *ResultCache) Put(toolKey, version string, args map[string]any, output map[string]any) {
	if c == nil || c.ttl <= 0 || output == nil {
		return
	}
	raw, err := json.Marshal(output)
	if err != nil {
		return
	}
	key, ok := cacheKey(toolKey, version, args)
	if !ok {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= maxEntries {
		c.entries = map[string]cacheEntry{}
	}
	c.entries[key] = cacheEntry{output: raw, expiresAt: c.now().Add(c.ttl)}
}

// InvalidateAll 整体失效（任何写入类工具执行成功后调用）。
func (c *ResultCache) InvalidateAll() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = map[string]cacheEntry{}
}

// cacheKey 由「工具 + 版本 + 规范化参数」组成。
//
// 规范化依赖 json.Marshal 对 map 的键排序（Go 标准库保证按键名字典序输出），
// 因此调用方传 map 的键序不影响命中。dunder 键（"__" 前缀，服务端注入的
// 审批凭据等）从键空间剔除：它们不影响只读结果，进键只会造成无谓 miss。
// 参数不可序列化时返回 ok=false（Get/Put 双侧都完全跳过缓存——绝不能退化成
// 公共键，否则不同参数之间会互相误命中）。
func cacheKey(toolKey, version string, args map[string]any) (string, bool) {
	clean := make(map[string]any, len(args))
	for k, v := range args {
		if strings.HasPrefix(k, "__") {
			continue
		}
		clean[k] = v
	}
	raw, err := json.Marshal(clean)
	if err != nil {
		return "", false
	}
	var b strings.Builder
	b.WriteString(toolKey)
	b.WriteString("\x00")
	b.WriteString(version)
	b.WriteString("\x00")
	b.Write(raw)
	return b.String(), true
}
