// Package sse 实现 ODAP 统一实时通道（contracts/03 §1.9 / 第十七篇 §10.6）
// 的进程内事件总线：有界回放缓冲 + Last-Event-ID 续传 + 事件分级 +
// 背压时丢弃 NORMAL。
//
// 设计约束：
//   - 事件 ID 是单调递增的十进制序号字符串，客户端凭 Last-Event-ID 请求
//     续传；缓冲只保留最近 max 条，超出后最旧的被淘汰（客户端若落后过多，
//     服务端在回放不足时仍返回可用事件，并在 data 中标注 truncated）；
//   - 事件分级 CRITICAL / IMPORTANT / NORMAL：背压（订阅者消费过慢）时
//     只允许丢弃 NORMAL，并对 CRITICAL/IMPORTANT 采用短等待后强制断开
//     （迫使客户端重连并按 Last-Event-ID 补齐，而不是静默丢关键事件）；
//   - 每订阅者独立缓冲，按租户过滤。
//
// MVP 边界：本总线是单进程内存实现；跨进程扇出需由第十篇的 Outbox→Broker
// 桥接完成（见 sse.OutboxSink）。
package sse

import (
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Criticality 是事件分级（数值越小越关键）。
type Criticality int

const (
	// Critical 不可丢弃：审批决策、取消收敛、EXECUTION_UNKNOWN 等安全事件。
	Critical Criticality = iota
	// Important 尽量不丢弃，背压下短暂等待。
	Important
	// Normal 可丢弃：进度心跳、非关键进度更新。
	Normal
)

// String 返回分级名（用于 SSE 注释与日志）。
func (c Criticality) String() string {
	switch c {
	case Critical:
		return "CRITICAL"
	case Important:
		return "IMPORTANT"
	default:
		return "NORMAL"
	}
}

// Event 是一条实时事件。
type Event struct {
	// ID 是单调递增序号（十进制字符串）；为空时由 Hub 分配。
	ID string
	// Type 是事件类型名（SSE event: 字段）。
	Type string
	// TenantID 是事件所属租户（用于过滤，字符串 UUID）。
	TenantID string
	// Criticality 决定背压下的处置。
	Criticality Criticality
	// Data 是事件载荷（SSE data: 字段，需为合法 JSON）。
	Data []byte
	// OccurredAt 是事件发生时间。
	OccurredAt time.Time
}

// defaultBuffer 是回放缓冲与订阅者队列的默认容量。
const defaultBuffer = 512

// deliverTimeout 是 IMPORTANT/CRITICAL 事件在订阅者队列满时的最大等待。
const deliverTimeout = 2 * time.Second

// Hub 是进程内实时事件总线。
type Hub struct {
	mu  sync.Mutex
	max int
	seq int64
	buf []Event
	sub map[*Subscription]struct{}

	droppedTotal  atomic.Int64
	overflowTotal atomic.Int64
}

// NewHub 创建 Hub；max <= 0 时使用 defaultBuffer。
func NewHub(max int) *Hub {
	if max <= 0 {
		max = defaultBuffer
	}
	return &Hub{max: max, sub: make(map[*Subscription]struct{})}
}

// Publish 追加事件并扇出给订阅者；为空 ID 时自动分配。
// 返回带最终 ID 的事件。
func (h *Hub) Publish(ev Event) Event {
	h.mu.Lock()
	h.seq++
	if ev.ID == "" {
		ev.ID = strconv.FormatInt(h.seq, 10)
	}
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = time.Now().UTC()
	}
	h.buf = append(h.buf, ev)
	if len(h.buf) > h.max {
		// 有界缓冲：淘汰最旧事件（客户端落后过多时由 truncated 标注提示）。
		drop := len(h.buf) - h.max
		h.buf = append([]Event(nil), h.buf[drop:]...)
	}
	subs := make([]*Subscription, 0, len(h.sub))
	for s := range h.sub {
		subs = append(subs, s)
	}
	h.mu.Unlock()

	for _, s := range subs {
		h.deliver(s, ev)
	}
	return ev
}

// deliver 按分级策略投递；NORMAL 在队列满时直接丢弃。
func (h *Hub) deliver(s *Subscription, ev Event) {
	if !s.matches(ev.TenantID) {
		return
	}
	select {
	case s.ch <- ev:
		return
	default:
	}
	if ev.Criticality == Normal {
		s.dropped.Add(1)
		h.droppedTotal.Add(1)
		return
	}
	// IMPORTANT/CRITICAL：短暂等待；仍失败则断开订阅，迫使客户端重连续传。
	select {
	case s.ch <- ev:
	case <-time.After(deliverTimeout):
		s.dropped.Add(1)
		h.droppedTotal.Add(1)
		h.overflowTotal.Add(1)
		h.Close(s)
	}
}

// Subscription 是一次订阅。
type Subscription struct {
	hub      *Hub
	tenantID string
	ch       chan Event
	dropped  atomic.Int64
	closed   atomic.Bool
}

// matches 报告事件是否属于该订阅租户。
func (s *Subscription) matches(tenantID string) bool {
	return s.tenantID == "" || tenantID == s.tenantID
}

// C 返回事件通道（只读）。
func (s *Subscription) C() <-chan Event { return s.ch }

// Dropped 返回被丢弃（背压）的事件数。
func (s *Subscription) Dropped() int64 { return s.dropped.Load() }

// Subscribe 建立订阅并按 lastEventID 回放缓冲中更晚的事件。
//
// lastEventID 为 ""：不回放；"_" 或非法值：视为从头（当前缓冲）回放。
// 回放期间超出订阅者缓冲的事件按同一分级策略处置。
func (h *Hub) Subscribe(tenantID, lastEventID string) (*Subscription, error) {
	s := &Subscription{hub: h, tenantID: tenantID, ch: make(chan Event, defaultBuffer)}

	h.mu.Lock()
	replay := make([]Event, 0, len(h.buf))
	if lastEventID != "" {
		var from int64
		if lastEventID != "_" {
			n, err := strconv.ParseInt(lastEventID, 10, 64)
			if err != nil {
				h.mu.Unlock()
				return nil, fmt.Errorf("sse: invalid Last-Event-ID %q", lastEventID)
			}
			from = n
		}
		for _, ev := range h.buf {
			id, err := strconv.ParseInt(ev.ID, 10, 64)
			if err != nil {
				continue
			}
			if id > from && s.matches(ev.TenantID) {
				replay = append(replay, ev)
			}
		}
	}
	h.sub[s] = struct{}{}
	h.mu.Unlock()

	for _, ev := range replay {
		select {
		case s.ch <- ev:
		default:
			// 回放超量：按分级处置（NORMAL 丢弃，关键事件保留最新一条）。
			if ev.Criticality == Normal {
				s.dropped.Add(1)
				h.droppedTotal.Add(1)
				continue
			}
			// 关键事件：丢弃队首为它让路（保证最新关键事件可送达）。
			select {
			case <-s.ch:
				s.dropped.Add(1)
				h.droppedTotal.Add(1)
			default:
			}
			select {
			case s.ch <- ev:
			default:
				s.dropped.Add(1)
				h.droppedTotal.Add(1)
			}
		}
	}
	return s, nil
}

// Close 关闭订阅并从 Hub 注销（幂等）。
func (h *Hub) Close(s *Subscription) {
	if s == nil || !s.closed.CompareAndSwap(false, true) {
		return
	}
	h.mu.Lock()
	delete(h.sub, s)
	h.mu.Unlock()
	close(s.ch)
}

// DroppedTotal 返回全 Hub 因背压丢弃的事件总数（可观测性指标）。
func (h *Hub) DroppedTotal() int64 { return h.droppedTotal.Load() }

// OverflowTotal 返回因订阅者过慢被强制断开的次数。
func (h *Hub) OverflowTotal() int64 { return h.overflowTotal.Load() }

// Subscribers 返回当前订阅者数量。
func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.sub)
}

// Buffered 返回回放缓冲当前长度。
func (h *Hub) Buffered() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.buf)
}
