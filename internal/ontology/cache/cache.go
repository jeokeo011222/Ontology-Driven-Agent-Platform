// Package cache 实现语义查询结果缓存（第十三篇 §11.5 缓存策略、§20 缓存分层）。
//
// # 只缓存"最宽权限投影"结果
//
// 缓存键必须包含 (tenant_id, 图版本/ontology_version, 查询摘要, 权限投影标签)。
// 缓存条目只允许保存**最宽权限投影**的结果——即对该租户内所有通过基础认证的调用方均可见的
// 字段与行（第十三篇 §11.5）。理由：若按调用者各自的授权范围分别缓存，会因投影不同而产生
// 结果泄漏或缓存污染；键中的权限标签只能防止跨版本串用，无法把"窄权限结果被当作宽权限结果
// 返回"的风险归零。正确做法是缓存最宽投影，命中后按当前调用方的授权范围**重新过滤输出**。
// 因此本实现对任何非最宽投影的写入请求直接拒绝，并且对非最宽投影的读取一律视为未命中。
package cache

import (
	"container/list"
	"sync"
	"time"

	"odap/internal/platform/clock"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// PermissionProjection 标识查询结果被裁剪到的权限投影范围。
type PermissionProjection string

const (
	// ProjectionWidest 是租户内最宽权限投影，唯一允许写入共享缓存的投影。
	ProjectionWidest PermissionProjection = "WIDEST"
	// ProjectionCrossTenant 表示结果包含跨租户数据，绝不允许写入共享缓存。
	ProjectionCrossTenant PermissionProjection = "CROSS_TENANT"
)

// Cacheable 报告该权限投影是否允许写入结果缓存。
func (p PermissionProjection) Cacheable() bool { return p == ProjectionWidest }

// 默认配置。
const (
	DefaultMaxEntries = 1024
	DefaultTTL        = 5 * time.Minute
)

// Key 是结果缓存的键。
type Key struct {
	TenantID         ids.ID
	GraphIRI         string
	OntologyVersion  string
	DataGraphVersion string // 数据图版本；版本推进即失效（与 ERP 回执→语义实例刷新联动）
	QueryDigest      string
	Permission       PermissionProjection
}

func (k Key) String() string {
	return k.TenantID.String() + "\x1f" + k.GraphIRI + "\x1f" + k.OntologyVersion + "\x1f" +
		k.DataGraphVersion + "\x1f" + k.QueryDigest + "\x1f" + string(k.Permission)
}

type entry struct {
	key       Key
	value     any
	expiresAt time.Time
}

// Cache 是带 LRU 淘汰与 TTL 的内存结果缓存。
type Cache struct {
	mu         sync.Mutex
	clk        clock.Clock
	maxEntries int
	ttl        time.Duration
	items      map[string]*list.Element // 缓存键 -> 链表元素
	order      *list.List               // 表头为最近使用
}

// New 创建结果缓存。clk 为 nil 时使用 clock.RealClock；maxEntries≤0 用 DefaultMaxEntries；
// ttl≤0 用 DefaultTTL。
func New(clk clock.Clock, maxEntries int, ttl time.Duration) *Cache {
	if clk == nil {
		clk = clock.RealClock{}
	}
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Cache{
		clk:        clk,
		maxEntries: maxEntries,
		ttl:        ttl,
		items:      make(map[string]*list.Element),
		order:      list.New(),
	}
}

// Get 读取缓存。
//
// 非最宽权限投影的读取一律视为未命中：共享缓存里的最宽投影结果不可被直接用作窄投影结果。
func (c *Cache) Get(key Key) (any, bool) {
	if !key.Permission.Cacheable() {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key.String()]
	if !ok {
		return nil, false
	}
	e := el.Value.(*entry)
	if !c.clk.Now().Before(e.expiresAt) {
		c.removeElement(el)
		return nil, false
	}
	c.order.MoveToFront(el)
	return e.value, true
}

// Put 写入缓存。
//
// 拒绝缓存任何非最宽权限投影（含跨租户投影）的查询结果，返回 PERMISSION_DENIED。
// 同一 (tenant, graph, query, permission) 但数据图版本/本体版本不同的旧条目会被立即清除，
// 从而实现"版本推进即失效"。
func (c *Cache) Put(key Key, value any) error {
	if key.TenantID.IsZero() {
		return errors.E(errors.CodeValidationFailed, "cache: tenant_id 不能为空")
	}
	if !key.Permission.Cacheable() {
		return errors.E(errors.CodePermissionDenied,
			"cache: 拒绝缓存非最宽权限投影结果（含租户外数据投影），仅允许 "+string(ProjectionWidest))
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// 版本推进失效：清除同一查询在旧版本下的条目。
	c.evictStaleVersions(key)

	if el, ok := c.items[key.String()]; ok {
		e := el.Value.(*entry)
		e.value = value
		e.expiresAt = c.clk.Now().Add(c.ttl)
		c.order.MoveToFront(el)
		return nil
	}

	e := &entry{key: key, value: value, expiresAt: c.clk.Now().Add(c.ttl)}
	el := c.order.PushFront(e)
	c.items[key.String()] = el
	for c.order.Len() > c.maxEntries {
		c.removeElement(c.order.Back())
	}
	return nil
}

// InvalidateGraph 使某命名图的全部缓存条目失效。
//
// 联动说明：ERP 回执确认后会触发语义实例刷新（第十二篇数据接入与语义映射、第七篇 ERP Port），
// 此时该命名图的 dataGraphVersion 推进，必须使旧结果失效，避免读到刷新前的投影结果。
func (c *Cache) InvalidateGraph(graphIRI string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, el := range c.items {
		if el.Value.(*entry).key.GraphIRI == graphIRI {
			c.removeElement(el)
		}
	}
}

// Len 返回当前缓存条目数（供测试与观测使用）。
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// evictStaleVersions 清除同一 (tenant, graph, query, permission) 下版本不同的旧条目。
func (c *Cache) evictStaleVersions(key Key) {
	for _, el := range c.items {
		e := el.Value.(*entry)
		if e.key.TenantID == key.TenantID &&
			e.key.GraphIRI == key.GraphIRI &&
			e.key.QueryDigest == key.QueryDigest &&
			e.key.Permission == key.Permission &&
			(e.key.DataGraphVersion != key.DataGraphVersion || e.key.OntologyVersion != key.OntologyVersion) {
			c.removeElement(el)
		}
	}
}

func (c *Cache) removeElement(el *list.Element) {
	if el == nil {
		return
	}
	c.order.Remove(el)
	delete(c.items, el.Value.(*entry).key.String())
}
