package a2a

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 辅助：构造 mock A2A Server
// ---------------------------------------------------------------------------

// newMockA2AServer 返回一个 httptest.Server，按 handler 行为响应 JSON-RPC 信封。
func newMockA2AServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 所有请求都应 POST JSON-RPC。
		if r.Method != http.MethodPost {
			t.Fatalf("mock server 收到非 POST 方法: %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Fatalf("mock server 收到非 JSON Content-Type: %s", ct)
		}
		handler(w, r)
	}))
}

// writeJSONRPCResult 写出 mock 成功响应。
func writeJSONRPCResult(w http.ResponseWriter, id any, result any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": JSONRPCVersion,
		"id":      id,
		"result":  result,
	})
}

// writeJSONRPCError 写出 mock JSON-RPC error 响应。
func writeJSONRPCError(w http.ResponseWriter, id any, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": JSONRPCVersion,
		"id":      id,
		"error":   map[string]any{"code": code, "message": msg},
	})
}

// parseEnvelope 解析请求信封并返回 method + params（测试用）。
func parseEnvelope(t *testing.T, r *http.Request) (string, map[string]any) {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("读取请求体失败: %v", err)
	}
	var env struct {
		JSONRPC string         `json:"jsonrpc"`
		Method  string         `json:"method"`
		Params  map[string]any `json:"params"`
		ID      any            `json:"id"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("解析请求信封失败: %v (body=%s)", err, string(body))
	}
	if env.JSONRPC != JSONRPCVersion {
		t.Fatalf("客户端 jsonrpc=%q, want %q", env.JSONRPC, JSONRPCVersion)
	}
	if env.Params == nil {
		env.Params = map[string]any{}
	}
	return env.Method, env.Params
}

// ---------------------------------------------------------------------------
// 1. 正常发送：SendMessage → 服务端返回 completed task
// ---------------------------------------------------------------------------

func TestSendMessage_Success(t *testing.T) {
	server := newMockA2AServer(t, func(w http.ResponseWriter, r *http.Request) {
		method, params := parseEnvelope(t, r)
		if method != MethodMessageSend {
			t.Errorf("method=%q, want %q", method, MethodMessageSend)
		}
		msg, ok := params["message"].(map[string]any)
		if !ok {
			t.Fatalf("params.message 不是对象: %+v", params["message"])
		}
		role := msg["role"]
		if role != "user" {
			t.Errorf("message.role=%q, want user", role)
		}
		parts, _ := msg["parts"].([]any)
		if len(parts) != 1 {
			t.Fatalf("message.parts len=%d, want 1", len(parts))
		}
		part, _ := parts[0].(map[string]any)
		if part["text"] != "hello world" {
			t.Errorf("part.text=%q, want hello world", part["text"])
		}

		writeJSONRPCResult(w, 1, map[string]any{
			"kind": "task",
			"id":   "task-123",
			"status": map[string]any{
				"state":     "completed",
				"timestamp": "2026-10-16T12:00:00Z",
			},
			"artifacts": []any{
				map[string]any{
					"artifactId": "task-123-output",
					"name":       "运行输出",
					"parts": []any{
						map[string]any{"kind": "text", "text": "done"},
					},
				},
			},
		})
	})
	defer server.Close()

	client := NewClient(server.Client(), 5*time.Second)
	task, err := client.SendMessage(context.Background(), server.URL+"/api/v1/a2a", "", "",
		Message{Role: "user", Parts: []MessagePart{{Kind: "text", Text: "hello world"}}},
		nil)
	if err != nil {
		t.Fatalf("SendMessage 返回错误: %v", err)
	}
	if task.ID != "task-123" {
		t.Errorf("task.ID=%q, want task-123", task.ID)
	}
	if task.Status.State != "completed" {
		t.Errorf("task.Status.State=%q, want completed", task.Status.State)
	}
	if len(task.Artifacts) != 1 || task.Artifacts[0].Parts[0].Text != "done" {
		t.Errorf("artifacts 形状不符合预期: %+v", task.Artifacts)
	}
	if !IsTaskTerminal(task) {
		t.Error("completed task 应被视为终态")
	}
}

// ---------------------------------------------------------------------------
// 2. 正常发送：带认证头（env 变量引用）
// ---------------------------------------------------------------------------

func TestSendMessage_WithAuthHeader(t *testing.T) {
	os.Setenv("MY_A2A_TOKEN", "super-secret-token")
	defer os.Unsetenv("MY_A2A_TOKEN")

	server := newMockA2AServer(t, func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-A2A-Token")
		if got != "super-secret-token" {
			t.Errorf("auth header = %q, want super-secret-token", got)
		}
		// 还应带默认 Accept / Content-Type
		if ct := r.Header.Get("Accept"); ct != "application/json" {
			t.Errorf("Accept=%q, want application/json", ct)
		}
		writeJSONRPCResult(w, 1, map[string]any{
			"id":     "t-1",
			"status": map[string]any{"state": "completed"},
		})
	})
	defer server.Close()

	client := NewClient(server.Client(), 5*time.Second)
	_, err := client.SendMessage(context.Background(), server.URL+"/api/v1/a2a",
		"X-A2A-Token", "MY_A2A_TOKEN",
		Message{Role: "user", Parts: []MessagePart{{Text: "hi"}}}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 3. 认证头配置了但 env 变量未设置 → fail-closed
// ---------------------------------------------------------------------------

func TestSendMessage_AuthEnvMissing_FailClosed(t *testing.T) {
	os.Unsetenv("MY_A2A_TOKEN") // 确保不存在

	client := NewClient(nil, 5*time.Second)
	_, err := client.SendMessage(context.Background(), "http://localhost/api/v1/a2a",
		"X-A2A-Token", "MY_A2A_TOKEN",
		Message{Role: "user", Parts: []MessagePart{{Text: "hi"}}}, nil)
	if err == nil {
		t.Fatal("期望错误，got nil")
	}
	if !strings.Contains(err.Error(), "MY_A2A_TOKEN") {
		t.Errorf("错误消息应包含缺失的 env 变量名，got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 4. 解析错误：远端返回 -32602 InvalidParams
// ---------------------------------------------------------------------------

func TestSendMessage_RemoteInvalidParams(t *testing.T) {
	server := newMockA2AServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSONRPCError(w, 1, CodeInvalidParams, "message.parts 必填")
	})
	defer server.Close()

	client := NewClient(server.Client(), 5*time.Second)
	_, err := client.SendMessage(context.Background(), server.URL+"/api/v1/a2a", "", "",
		Message{Role: "user", Parts: []MessagePart{}}, nil)
	if err == nil {
		t.Fatal("期望 RPCError，got nil")
	}
	rpc, ok := IsRPCError(err)
	if !ok {
		t.Fatalf("错误不是 *RPCError: %T %v", err, err)
	}
	if rpc.Code != CodeInvalidParams {
		t.Errorf("code=%d, want %d", rpc.Code, CodeInvalidParams)
	}
	if rpc.Message != "message.parts 必填" {
		t.Errorf("message=%q, want message.parts 必填", rpc.Message)
	}
}

// ---------------------------------------------------------------------------
// 5. 解析错误：远端返回 HTTP 500 + 非法 JSON（不是 JSON-RPC 信封）
// ---------------------------------------------------------------------------

func TestSendMessage_RemoteHTTP500_NotJSONRPC(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintln(w, "gateway timeout")
	}))
	defer server.Close()

	client := NewClient(server.Client(), 5*time.Second)
	_, err := client.SendMessage(context.Background(), server.URL+"/api/v1/a2a", "", "",
		Message{Role: "user", Parts: []MessagePart{{Text: "hi"}}}, nil)
	if err == nil {
		t.Fatal("期望错误，got nil")
	}
	// 不是 RPCError（因为 HTTP 500 且响应不是 JSON-RPC 信封）
	if _, ok := IsRPCError(err); ok {
		t.Errorf("不应解析为 RPCError，got %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("错误应包含 HTTP 500 诊断信息，got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 6. 解析错误：远端返回 HTTP 404 + JSON-RPC error（某些 Server 会这样）
// ---------------------------------------------------------------------------

func TestSendMessage_RemoteHTTP404_WithRPCError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": JSONRPCVersion,
			"id":      1,
			"error":   map[string]any{"code": CodeMethodNotFound, "message": "method not found"},
		})
	}))
	defer server.Close()

	client := NewClient(server.Client(), 5*time.Second)
	_, err := client.SendMessage(context.Background(), server.URL+"/api/v1/a2a", "", "",
		Message{Role: "user", Parts: []MessagePart{{Text: "hi"}}}, nil)
	if err == nil {
		t.Fatal("期望 RPCError，got nil")
	}
	rpc, ok := IsRPCError(err)
	if !ok {
		t.Fatalf("错误不是 RPCError: %T %v", err, err)
	}
	if rpc.Code != CodeMethodNotFound {
		t.Errorf("code=%d, want %d", rpc.Code, CodeMethodNotFound)
	}
}

// ---------------------------------------------------------------------------
// 7. 超时处理：单次调用超时 → 返回 CodeDeadlineExceeded
// ---------------------------------------------------------------------------

func TestSendMessage_Timeout(t *testing.T) {
	server := newMockA2AServer(t, func(w http.ResponseWriter, r *http.Request) {
		// 故意慢响应
		time.Sleep(300 * time.Millisecond)
		writeJSONRPCResult(w, 1, map[string]any{"id": "t-1", "status": map[string]any{"state": "completed"}})
	})
	defer server.Close()

	client := NewClient(server.Client(), 2*time.Second)
	// 用 50ms 单次超时覆盖默认
	_, err := client.SendMessage(context.Background(), server.URL+"/api/v1/a2a", "", "",
		Message{Role: "user", Parts: []MessagePart{{Text: "hi"}}}, nil,
		WithTimeout(50*time.Millisecond))
	if err == nil {
		t.Fatal("期望超时错误，got nil")
	}
	// 验证错误包含超时语义
	if !strings.Contains(err.Error(), "超时") && !strings.Contains(err.Error(), "DEADLINE_EXCEEDED") {
		t.Errorf("错误应包含超时诊断，got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 8. 超时处理：默认 120s 不主动中断快响应
// ---------------------------------------------------------------------------

func TestSendMessage_DefaultTimeoutDoesNotExpire(t *testing.T) {
	server := newMockA2AServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSONRPCResult(w, 1, map[string]any{"id": "t-1", "status": map[string]any{"state": "completed"}})
	})
	defer server.Close()

	client := NewClient(server.Client(), 120*time.Second)
	_, err := client.SendMessage(context.Background(), server.URL+"/api/v1/a2a", "", "",
		Message{Role: "user", Parts: []MessagePart{{Text: "hi"}}}, nil)
	if err != nil {
		t.Fatalf("快响应不应超时，got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 9. GetTask 正常
// ---------------------------------------------------------------------------

func TestGetTask_Success(t *testing.T) {
	server := newMockA2AServer(t, func(w http.ResponseWriter, r *http.Request) {
		method, params := parseEnvelope(t, r)
		if method != MethodTasksGet {
			t.Errorf("method=%q, want %q", method, MethodTasksGet)
		}
		if params["id"] != "my-task-id" {
			t.Errorf("params.id=%v, want my-task-id", params["id"])
		}
		writeJSONRPCResult(w, 1, map[string]any{
			"id":     "my-task-id",
			"status": map[string]any{"state": "working"},
		})
	})
	defer server.Close()

	client := NewClient(server.Client(), 5*time.Second)
	task, err := client.GetTask(context.Background(), server.URL+"/api/v1/a2a", "", "", "my-task-id")
	if err != nil {
		t.Fatalf("GetTask 返回错误: %v", err)
	}
	if task.ID != "my-task-id" {
		t.Errorf("task.ID=%q, want my-task-id", task.ID)
	}
	if task.Status.State != "working" {
		t.Errorf("task.Status.State=%q, want working", task.Status.State)
	}
	if IsTaskTerminal(task) {
		t.Error("working task 不应视为终态")
	}
}

// ---------------------------------------------------------------------------
// 10. ListTasks 分页
// ---------------------------------------------------------------------------

func TestListTasks_Success(t *testing.T) {
	server := newMockA2AServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, params := parseEnvelope(t, r)
		if params["limit"] != float64(5) {
			t.Errorf("limit=%v, want 5", params["limit"])
		}
		writeJSONRPCResult(w, 1, map[string]any{
			"items": []any{
				map[string]any{"id": "t-1", "status": map[string]any{"state": "completed"}},
				map[string]any{"id": "t-2", "status": map[string]any{"state": "working"}},
			},
			"next_cursor": "page2",
		})
	})
	defer server.Close()

	client := NewClient(server.Client(), 5*time.Second)
	items, next, err := client.ListTasks(context.Background(), server.URL+"/api/v1/a2a", "", "",
		&ListParams{Limit: 5})
	if err != nil {
		t.Fatalf("ListTasks 返回错误: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items len=%d, want 2", len(items))
	}
	if next != "page2" {
		t.Errorf("next=%q, want page2", next)
	}
	if items[0].Status.State != "completed" {
		t.Errorf("items[0].state=%q, want completed", items[0].Status.State)
	}
}

// ---------------------------------------------------------------------------
// 11. CancelTask 正常
// ---------------------------------------------------------------------------

func TestCancelTask_Success(t *testing.T) {
	server := newMockA2AServer(t, func(w http.ResponseWriter, r *http.Request) {
		method, params := parseEnvelope(t, r)
		if method != MethodTasksCancel {
			t.Errorf("method=%q, want %q", method, MethodTasksCancel)
		}
		if params["id"] != "t-1" {
			t.Errorf("params.id=%v, want t-1", params["id"])
		}
		writeJSONRPCResult(w, 1, map[string]any{
			"id":     "t-1",
			"status": map[string]any{"state": "canceled"},
		})
	})
	defer server.Close()

	client := NewClient(server.Client(), 5*time.Second)
	task, err := client.CancelTask(context.Background(), server.URL+"/api/v1/a2a", "", "", "t-1")
	if err != nil {
		t.Fatalf("CancelTask 返回错误: %v", err)
	}
	if task.Status.State != "canceled" {
		t.Errorf("state=%q, want canceled", task.Status.State)
	}
}

// ---------------------------------------------------------------------------
// 12. GatewayURL 规范化（注册表 base_url → JSON-RPC 端点）
// ---------------------------------------------------------------------------

func TestGatewayURL(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"https://example.com", "https://example.com/api/v1/a2a"},
		{"https://example.com/", "https://example.com/api/v1/a2a"},
		{"https://example.com/api/v1/a2a", "https://example.com/api/v1/a2a"},
		{"https://example.com/api/v1/a2a/", "https://example.com/api/v1/a2a"},
		{"http://localhost:8080", "http://localhost:8080/api/v1/a2a"},
	}
	for _, c := range cases {
		got := GatewayURL(c.input)
		if got != c.want {
			t.Errorf("GatewayURL(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 13. 网络错误（连接拒绝）→ CodeUnavailable
// ---------------------------------------------------------------------------

func TestSendMessage_ConnectionRefused(t *testing.T) {
	client := NewClient(nil, 2*time.Second)
	_, err := client.SendMessage(context.Background(), "http://127.0.0.1:1/api/v1/a2a", "", "",
		Message{Role: "user", Parts: []MessagePart{{Text: "hi"}}}, nil)
	if err == nil {
		t.Fatal("期望连接拒绝错误，got nil")
	}
	// 应包含无法连接诊断。
	if !strings.Contains(err.Error(), "无法连接") {
		t.Errorf("错误应包含无法连接诊断，got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 14. SendTaskSubscribe 正常返回 SSE 流
// ---------------------------------------------------------------------------

func TestSendTaskSubscribe_SSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "event: TaskStatus\ndata: {\"task_id\":\"t-1\",\"state\":\"working\"}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "event: A2ATaskFinal\ndata: {\"id\":\"t-1\",\"status\":{\"state\":\"completed\"}}\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	client := NewClient(server.Client(), 5*time.Second)
	rc, err := client.SendTaskSubscribe(context.Background(), server.URL+"/api/v1/a2a", "", "",
		Message{Role: "user", Parts: []MessagePart{{Text: "hi"}}}, nil)
	if err != nil {
		t.Fatalf("SendTaskSubscribe 返回错误: %v", err)
	}
	defer rc.Close()

	scanner := NewSSEScanner(rc)
	var events []SSEEvent
	for scanner.Scan() {
		events = append(events, scanner.Event())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner 错误: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events len=%d, want 2", len(events))
	}
	if events[0].Event != "TaskStatus" {
		t.Errorf("events[0].Event=%q, want TaskStatus", events[0].Event)
	}
	if events[1].Event != "A2ATaskFinal" {
		t.Errorf("events[1].Event=%q, want A2ATaskFinal", events[1].Event)
	}
}

// ---------------------------------------------------------------------------
// 15. NewClient 零值参数回退
// ---------------------------------------------------------------------------

func TestNewClient_Defaults(t *testing.T) {
	c := NewClient(nil, 0)
	if c.http == nil {
		t.Error("http 客户端不应为 nil（应回退到 DefaultClient）")
	}
	if c.timeout != defaultSyncWaitTimeout {
		t.Errorf("timeout=%v, want %v", c.timeout, defaultSyncWaitTimeout)
	}
}

// ---------------------------------------------------------------------------
// 16. 客户端 JSON-RPC id 递增
// ---------------------------------------------------------------------------

func TestRequestID_Incremental(t *testing.T) {
	server := newMockA2AServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var env struct{ ID int64 `json:"id"` }
		_ = json.Unmarshal(body, &env)
		writeJSONRPCResult(w, env.ID, map[string]any{"id": "t-1", "status": map[string]any{"state": "completed"}})
	})
	defer server.Close()

	client := NewClient(server.Client(), 5*time.Second)
	// 三次调用，id 应严格递增。
	_, _ = client.SendMessage(context.Background(), server.URL+"/api/v1/a2a", "", "",
		Message{Role: "user", Parts: []MessagePart{{Text: "a"}}}, nil)
	_, _ = client.SendMessage(context.Background(), server.URL+"/api/v1/a2a", "", "",
		Message{Role: "user", Parts: []MessagePart{{Text: "b"}}}, nil)
	_, _ = client.SendMessage(context.Background(), server.URL+"/api/v1/a2a", "", "",
		Message{Role: "user", Parts: []MessagePart{{Text: "c"}}}, nil)
	// 只做非零验证（并发时无法精确预期初始值）。
	if client.requestID.Load() < 3 {
		t.Errorf("requestID 应至少增长到 3，got %d", client.requestID.Load())
	}
}
