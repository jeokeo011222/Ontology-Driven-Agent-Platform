// Package collab 实现协同编辑的房间管理：每个 ontology 有一个独立房间，
// 房间内 WebSocket 消息（yjs 二进制同步帧 + awareness 状态）原样中继给
// 所有连接客户端。yjs 自带 CRDT，本包只做消息转发，不做持久化。
//
// 设计约束：
//   - 房间按 ontologyId 隔离，防止跨本体串编辑；
//   - 内存存储：进程重启后房间状态丢失，依赖 yjs 客户端重连时全量同步
//     （y-websocket provider 断开重连会自动请求 full sync）；
//   - 背压处置：向慢客户端写时使用带超时的 channel 投递，超时则关闭连接
//     让客户端重连重同步（与 SSE hub 的分级背压策略一致）。
package collab

import (
	"log/slog"
	"sync"
	"time"

	"odap/internal/platform/ids"

	"github.com/gorilla/websocket"
)

// defaultManager 是进程内的全局协同房间管理器（按 ontologyId 隔离）。
// 由 NewClient 默认使用；装配期也可通过 SetManager 覆盖（如注入带上下文的 logger）。
var defaultManager = NewManager(slog.Default())

// SetManager 替换全局默认 Manager（装配期覆盖，测试也可注入 stub）。
func SetManager(m *Manager) {
	if m != nil {
		defaultManager = m
	}
}

// DefaultManager 返回全局默认 Manager。
func DefaultManager() *Manager { return defaultManager }

// NewClient 构造一个客户端并加入对应 ontology 房间。ontologyID 为空时
// 返回 nil（handler 侧应先校验）。
func NewClient(ontologyID, userID string, conn *websocket.Conn) *Client {
	if ontologyID == "" || conn == nil {
		return nil
	}
	room := defaultManager.GetOrCreate(ontologyID)
	c := &Client{
		ID:     ids.New().String(),
		UserID: userID,
		Room:   room,
		conn:   conn,
		send:   make(chan []byte, writeBufferSize),
	}
	room.Join(c)
	return c
}

// 写超时：向慢客户端发送消息的最大等待时间。
const writeTimeout = 5 * time.Second

// 写缓冲：每个连接的出站 channel 容量。
const writeBufferSize = 64

// Client 是房间里的一个 WebSocket 客户端。
type Client struct {
	// ID 是客户端唯一标识（连接建立时生成，用于在房间中区分不同连接）。
	ID string
	// UserID 是业务层的用户标识（从认证上下文提取）。
	UserID string
	// UserName 是业务层的用户名（可选，用于 awareness 显示）。
	UserName string
	// Room 是所属房间。
	Room *Room
	// conn 是底层 WebSocket 连接。
	conn *websocket.Conn
	// send 是出站消息队列（读循环把消息塞进这里，写循环从这里取出写回客户端）。
	send chan []byte
	// closed 标记该 client 是否已被清理（幂等 Close 用）。
	closed bool
	mu     sync.Mutex
}

// Room 按 ontologyId 隔离的协同编辑房间。
type Room struct {
	// ID 通常就是 ontologyId。
	ID string

	mu      sync.Mutex
	clients map[*Client]struct{}
	logger  *slog.Logger
}

// Manager 是房间管理器（单例，管理所有协同编辑房间）。
type Manager struct {
	mu     sync.RWMutex
	rooms  map[string]*Room
	logger *slog.Logger
}

// NewManager 创建 Manager。
func NewManager(logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		rooms:  make(map[string]*Room),
		logger: logger,
	}
}

// GetOrCreate 获取或创建指定 ontologyId 的房间。
func (m *Manager) GetOrCreate(ontologyID string) *Room {
	m.mu.RLock()
	r, ok := m.rooms[ontologyID]
	m.mu.RUnlock()
	if ok {
		return r
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// double check
	if r, ok = m.rooms[ontologyID]; ok {
		return r
	}
	r = &Room{
		ID:      ontologyID,
		clients: make(map[*Client]struct{}),
		logger:  m.logger.With("room", ontologyID),
	}
	m.rooms[ontologyID] = r
	m.logger.Info("collab room created", "ontology_id", ontologyID)
	return r
}

// RoomCount 返回当前活跃房间数。
func (m *Manager) RoomCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.rooms)
}

// ClientsTotal 返回全 Manager 当前客户端总数。
func (m *Manager) ClientsTotal() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	total := 0
	for _, r := range m.rooms {
		r.mu.Lock()
		total += len(r.clients)
		r.mu.Unlock()
	}
	return total
}

// Join 把一个 client 加入房间。房间自己会管理生命周期（空了不销毁，依赖
// 客户端重连机制；若要回收空房间可由外部定期调 CleanupEmpty）。
func (r *Room) Join(c *Client) {
	r.mu.Lock()
	r.clients[c] = struct{}{}
	r.mu.Unlock()
	r.logger.Info("client joined collab room", "client_id", c.ID, "user_id", c.UserID,
		"total_clients", r.ClientCount())
}

// Leave 把 client 从房间移除并关闭连接。
func (r *Room) Leave(c *Client) {
	r.mu.Lock()
	if _, ok := r.clients[c]; ok {
		delete(r.clients, c)
		r.mu.Unlock()
		c.Close()
		r.logger.Info("client left collab room", "client_id", c.ID, "user_id", c.UserID,
			"total_clients", r.ClientCount())
		return
	}
	r.mu.Unlock()
	c.Close()
}

// Broadcast 把消息广播给房间内除 except 之外的所有客户端。
// msg 是 yjs 二进制帧（update sync / awareness / pong 等）。
func (r *Room) Broadcast(msg []byte, except *Client) {
	r.mu.Lock()
	targets := make([]*Client, 0, len(r.clients))
	for c := range r.clients {
		if c == except {
			continue
		}
		targets = append(targets, c)
	}
	r.mu.Unlock()
	for _, c := range targets {
		c.sendMsg(msg)
	}
}

// ClientCount 返回当前房间连接数。
func (r *Room) ClientCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.clients)
}

// CleanupEmpty 清除空房间（由外部定时调用）。
func (m *Manager) CleanupEmpty() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, r := range m.rooms {
		r.mu.Lock()
		count := len(r.clients)
		r.mu.Unlock()
		if count == 0 {
			delete(m.rooms, id)
			m.logger.Info("cleaned up empty collab room", "ontology_id", id)
		}
	}
}

// sendMsg 把消息塞进出站 channel；超时则关闭连接。
func (c *Client) sendMsg(msg []byte) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	ch := c.send
	c.mu.Unlock()
	select {
	case ch <- msg:
	default:
		// 队列满 —— 背压；不阻塞读循环以免影响其他 client。直接关闭，
		// 客户端重连会走 full sync。
		c.Room.logger.Warn("client send queue full, dropping connection",
			"client_id", c.ID)
		c.Close()
	}
}

// Close 关闭连接并标记 client 为 closed（幂等）。
func (c *Client) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	// 先关 channel（写循环退出），再关 WebSocket（底层断链会让读循环退出）。
	defer close(c.send)
	defer func() {
		if c.conn != nil {
			_ = c.conn.Close()
		}
	}()
	c.mu.Unlock()
}

// WriteLoop 从 send channel 取消息写回客户端。退出条件：channel 关闭或
// 写超时。由 handler 的 goroutine 调用。
func (c *Client) WriteLoop() {
	defer c.Room.Leave(c)
	ticker := time.NewTicker(30 * time.Second) // 30s 心跳，防止反向代理断空闲连接
	defer ticker.Stop()
	for {
		select {
		case msg, ok := <-c.send:
			if !ok {
				return
			}
			if err := c.writeMessage(msg); err != nil {
				return
			}
		case <-ticker.C:
			// ping 帧
			if err := c.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout)); err != nil {
				return
			}
		}
	}
}

// writeMessage 带超时地写一帧。
func (c *Client) writeMessage(msg []byte) error {
	c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	defer c.conn.SetWriteDeadline(time.Time{})
	return c.conn.WriteMessage(websocket.BinaryMessage, msg)
}

// ReadLoop 持续读取来自客户端的消息（yjs 二进制帧）并广播给房间其他成员。
// 退出条件：读错误（客户端断开或发送 close 帧）。由 handler 的 goroutine 调用。
func (c *Client) ReadLoop() {
	defer c.Room.Leave(c)
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})
	for {
		mt, payload, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		switch mt {
		case websocket.BinaryMessage:
			c.Room.Broadcast(payload, c)
		case websocket.PingMessage:
			// 写循环里的 ticker 已经在发 ping，这里客户端 ping 被动回 pong
			// 由 gorilla/websocket 默认处理（见 DefaultDialer 的 PongHandler 已设）。
			continue
		case websocket.CloseMessage:
			return
		}
	}
}
