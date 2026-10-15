package handlers

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"odap/internal/api/sse"
	"odap/internal/platform/errors"
)

// sseRetryMillis 是客户端重连建议间隔（毫秒）。
const sseRetryMillis = 3000

// sseHeartbeat 是心跳注释间隔（保持连接穿过中间代理）。
const sseHeartbeat = 15 * time.Second

// Stream 处理 GET /api/v1/stream（SSE 统一实时通道）。
//
// 行为（contracts/03 §1.9 / 第十七篇 §10.6）：
//   - 按租户过滤事件；
//   - 支持 Last-Event-ID 续传（请求头或 ?last_event_id=），回放有界缓冲；
//   - 事件分级 CRITICAL/IMPORTANT/NORMAL，背压时只丢弃 NORMAL（由 sse.Hub 保证）；
//   - 15s 心跳注释防止空闲连接被中间设备切断。
func (h *Handler) Stream(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Events == nil {
		h.fail(w, r, unavailable("实时事件总线未装配"))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		h.fail(w, r, errors.E(errors.CodeInternal, "服务端不支持流式响应"))
		return
	}
	last := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if last == "" {
		last = strings.TrimSpace(r.URL.Query().Get("last_event_id"))
	}

	sub, err := h.deps.Events.Subscribe(id.TenantID.String(), last)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer h.deps.Events.Close(sub)

	hdr := w.Header()
	hdr.Set("Content-Type", "text/event-stream; charset=utf-8")
	hdr.Set("Cache-Control", "no-cache, no-transform")
	hdr.Set("Connection", "keep-alive")
	hdr.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	fmt.Fprintf(w, "retry: %d\n\n", sseRetryMillis)
	flusher.Flush()

	hb := time.NewTicker(sseHeartbeat)
	defer hb.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-sub.C():
			if !ok {
				// 订阅被服务端强制关闭（慢消费者溢出）：结束流，客户端将
				// 携带 Last-Event-ID 重连续传。
				return
			}
			writeSSEEvent(w, ev)
			flusher.Flush()
		case <-hb.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

// writeSSEEvent 按 SSE 规范写出单条事件。
func writeSSEEvent(w http.ResponseWriter, ev sse.Event) {
	payload := "null"
	if len(ev.Data) > 0 {
		payload = string(ev.Data)
	}
	fmt.Fprintf(w, "id: %s\n", ev.ID)
	if ev.Type != "" {
		fmt.Fprintf(w, "event: %s\n", ev.Type)
	}
	// 单行 data 中包含 criticality 与原始载荷，便于客户端分级处理。
	fmt.Fprintf(w, "data: {\"criticality\":%q,\"payload\":%s}\n\n", ev.Criticality.String(), payload)
}
