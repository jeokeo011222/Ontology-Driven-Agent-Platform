package handlers

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"odap/internal/api/sse"
	"odap/internal/api/store"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/runstate"
	"odap/internal/platform/tenantctx"
)

// 本文件遵循 ontology_review_test.go 的既定模式：只测无需外部依赖的纯函数
// 与注入身份后的信封级 HTTP 行为；涉及 DB 的 CRUD/等待终态链路由
// internal/api 的 DBT 套件覆盖。

// ---------------------------------------------------------------------------
// JSON-RPC 2.0 信封校验
// ---------------------------------------------------------------------------

func TestParseA2AEnvelope(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		body    string
		wantErr bool
		// wantCode 断言 JSON-RPC 错误码（wantErr=true 时生效）。
		wantCode int
		// wantMethod / wantID 断言解析结果（wantErr=false 时生效）。
		wantMethod string
		wantID     string
	}{
		{
			name:       "合法请求：数字 id",
			body:       `{"jsonrpc":"2.0","id":42,"method":"tasks/get","params":{"id":"x"}}`,
			wantMethod: "tasks/get", wantID: "42",
		},
		{
			name:       "合法请求：字符串 id，params 缺省为 {}",
			body:       `{"jsonrpc":"2.0","id":"abc","method":"tasks/list"}`,
			wantMethod: "tasks/list", wantID: `"abc"`,
		},
		{
			name:       "合法请求：负数 id",
			body:       `{"jsonrpc":"2.0","id":-7,"method":"message/send"}`,
			wantMethod: "message/send", wantID: "-7",
		},
		{name: "jsonrpc 缺失", body: `{"id":1,"method":"tasks/get"}`, wantErr: true, wantCode: a2aCodeInvalidRequest},
		{name: "jsonrpc 版本错误", body: `{"jsonrpc":"1.0","id":1,"method":"tasks/get"}`, wantErr: true, wantCode: a2aCodeInvalidRequest},
		{name: "method 缺失", body: `{"jsonrpc":"2.0","id":1}`, wantErr: true, wantCode: a2aCodeInvalidRequest},
		{name: "method 非字符串", body: `{"jsonrpc":"2.0","id":1,"method":7}`, wantErr: true, wantCode: a2aCodeInvalidRequest},
		{name: "id 缺失", body: `{"jsonrpc":"2.0","method":"tasks/get"}`, wantErr: true, wantCode: a2aCodeInvalidRequest},
		{name: "id 为 null", body: `{"jsonrpc":"2.0","id":null,"method":"tasks/get"}`, wantErr: true, wantCode: a2aCodeInvalidRequest},
		{name: "id 为布尔", body: `{"jsonrpc":"2.0","id":true,"method":"tasks/get"}`, wantErr: true, wantCode: a2aCodeInvalidRequest},
		{name: "id 为对象", body: `{"jsonrpc":"2.0","id":{"x":1},"method":"tasks/get"}`, wantErr: true, wantCode: a2aCodeInvalidRequest},
		{name: "批量请求不支持", body: `[{"jsonrpc":"2.0","id":1,"method":"tasks/get"}]`, wantErr: true, wantCode: a2aCodeInvalidRequest},
		{name: "空请求体", body: ``, wantErr: true, wantCode: a2aCodeInvalidRequest},
		{name: "非法 JSON", body: `{jsonrpc`, wantErr: true, wantCode: a2aCodeParseError},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req, rpcErr := parseA2AEnvelope([]byte(tc.body))
			if tc.wantErr {
				if rpcErr == nil {
					t.Fatalf("期望错误，实际解析成功：%s", tc.body)
				}
				if rpcErr.Code != tc.wantCode {
					t.Fatalf("错误码期望 %d，实际 %d（message=%s）", tc.wantCode, rpcErr.Code, rpcErr.Message)
				}
				return
			}
			if rpcErr != nil {
				t.Fatalf("期望解析成功，实际报错：code=%d message=%s", rpcErr.Code, rpcErr.Message)
			}
			if req.Method != tc.wantMethod {
				t.Fatalf("method 期望 %s，实际 %s", tc.wantMethod, req.Method)
			}
			if string(req.ID) != tc.wantID {
				t.Fatalf("id 期望 %s，实际 %s", tc.wantID, string(req.ID))
			}
		})
	}
}

// TestParseA2ASendParamsToolsArgs 验证 metadata.tools_args 的透传语义：
// 合法对象入参按工具键解析；标量值导致整体 -32602（fail-closed，绝不静默丢弃）。
func TestParseA2ASendParamsToolsArgs(t *testing.T) {
	t.Parallel()
	params, _, rpcErr := parseA2ASendParams(json.RawMessage(
		`{"message":{"role":"user","parts":[{"kind":"text","text":"审核 EXP-2026-001"}]},` +
			`"metadata":{"tools_args":{"erp.expense.query":{"expense_id":"EXP-2026-001"}}}}`))
	if rpcErr != nil {
		t.Fatalf("合法 tools_args 不应报错：%s", rpcErr.Message)
	}
	args, ok := params.Metadata.ToolsArgs["erp.expense.query"]
	if !ok || args["expense_id"] != "EXP-2026-001" {
		t.Fatalf("tools_args 未按工具键解析：%v", params.Metadata.ToolsArgs)
	}
	if _, _, rpcErr := parseA2ASendParams(json.RawMessage(
		`{"message":{"role":"user","parts":[{"kind":"text","text":"x"}]},` +
			`"metadata":{"tools_args":{"erp.expense.query":"not-an-object"}}}`)); rpcErr == nil || rpcErr.Code != a2aCodeInvalidParams {
		t.Fatalf("标量 tools_args 应 -32602，实际 %+v", rpcErr)
	}
}

// TestA2AGatewayRejectsBadEnvelope 走完整 HTTP 层（注入租户身份、不装配 Store）：
// 信封/白名单校验发生在任何数据面依赖之前，无需 DB 即可验证协议行为。
func TestA2AGatewayRejectsBadEnvelope(t *testing.T) {
	t.Parallel()
	h := New(Deps{})
	cases := []struct {
		name     string
		body     string
		wantCode int
	}{
		{"非法 JSON → -32700", `{oops`, a2aCodeParseError},
		{"jsonrpc 版本错误 → -32600", `{"jsonrpc":"1.0","id":1,"method":"tasks/get"}`, a2aCodeInvalidRequest},
		{"白名单外方法 → -32601", `{"jsonrpc":"2.0","id":1,"method":"agent/list"}`, a2aCodeMethodNotFound},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/a2a", strings.NewReader(tc.body))
			req = req.WithContext(tenantctx.WithIdentity(context.Background(), tenantctx.Identity{
				TenantID: ids.New(), SubjectID: ids.New(),
			}))
			rr := httptest.NewRecorder()
			h.A2AGateway(rr, req)
			resp := rr.Result()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("JSON-RPC 错误也应以 HTTP 200 返回（由 error.code 承载语义），实际 %d", resp.StatusCode)
			}
			var body struct {
				JSONRPC string `json:"jsonrpc"`
				Error   *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("响应体不是合法 JSON：%v", err)
			}
			if body.JSONRPC != "2.0" || body.Error == nil || body.Error.Code != tc.wantCode {
				t.Fatalf("期望 jsonrpc=2.0 且 error.code=%d，实际 %+v", tc.wantCode, body)
			}
		})
	}

	// 未认证请求在信封校验前即被拒（平台错误语义，非 JSON-RPC）。
	req := httptest.NewRequest(http.MethodPost, "/api/v1/a2a", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	h.A2AGateway(rr, req)
	if rr.Code < 400 {
		t.Fatalf("未认证请求应被拒绝，实际状态码 %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Run 17 态 → A2A 任务状态映射（表驱动锁定，fail-safe：未知状态归 working）
// ---------------------------------------------------------------------------

func TestA2ATaskStateMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		runStatus string
		want      string
	}{
		{runstate.Created, "submitted"},
		{runstate.ContextBuilding, "working"},
		{runstate.Ready, "working"},
		{runstate.Planning, "working"},
		{runstate.PlanValidating, "working"},
		{runstate.Running, "working"},
		{runstate.WaitingInput, "working"},
		{runstate.WaitingApproval, "working"},
		{runstate.Paused, "working"},
		{runstate.Recovering, "working"},
		{runstate.Cancelling, "working"},
		{runstate.RequiresReconciliation, "working"},
		{runstate.ManualReview, "working"},
		{runstate.Completed, "completed"},
		{runstate.Failed, "failed"},
		{runstate.Expired, "failed"},
		{runstate.Cancelled, "canceled"},
		{"UNKNOWN_FUTURE_STATE", "working"}, // fail-safe：新增平台状态不得映射成终态
	}
	for _, tc := range cases {
		if got := a2aTaskStateOf(tc.runStatus); got != tc.want {
			t.Errorf("a2aTaskStateOf(%s) = %s，期望 %s", tc.runStatus, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Agent Card 装配形状
// ---------------------------------------------------------------------------

func TestA2AAgentCardShape(t *testing.T) {
	t.Parallel()
	skills := []map[string]any{
		{"id": "run-orchestrator", "name": "运行编排", "description": "d1"},
		{"id": "enterprise-knowledge-qa", "name": "企业知识问答", "description": "d2"},
	}
	card := a2aAgentCardOf("ODAP", "9.9.9", "https://odap.example.com", skills)
	if card["name"] != "ODAP" || card["version"] != "9.9.9" {
		t.Fatalf("name/version 装配错误：%+v", card)
	}
	if card["url"] != "https://odap.example.com/api/v1/a2a" {
		t.Fatalf("url 必须指向 JSON-RPC 单端点，实际 %v", card["url"])
	}
	caps, ok := card["capabilities"].(map[string]any)
	if !ok || caps["streaming"] != true {
		t.Fatalf("capabilities.streaming 必须为 true，实际 %+v", card["capabilities"])
	}
	gotSkills, ok := card["skills"].([]map[string]any)
	if !ok || len(gotSkills) != 2 {
		t.Fatalf("skills 汇总错误：%+v", card["skills"])
	}
	if gotSkills[0]["id"] != "run-orchestrator" || gotSkills[0]["name"] != "运行编排" {
		t.Fatalf("skill 条目必须是 {id,name,description}：%+v", gotSkills[0])
	}
	in, _ := card["defaultInputModes"].([]string)
	out, _ := card["defaultOutputModes"].([]string)
	if len(in) == 0 || len(out) == 0 {
		t.Fatalf("默认输入/输出模式不能为空：%+v / %+v", in, out)
	}

	// 空租户（无 registry 记录）：skills 必须是空数组而非 null。
	empty := a2aAgentCardOf("ODAP", "1.0.0", "http://x", nil)
	if s, ok := empty["skills"].([]map[string]any); !ok || len(s) != 0 {
		t.Fatalf("无技能时 skills 应为空数组，实际 %#v", empty["skills"])
	}
}

// a2aSelfBaseURL：反代头优先，其次请求 scheme。
func TestA2ASelfBaseURL(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodGet, "http://odap.internal/.well-known/agent.json", nil)
	if got := a2aSelfBaseURL(r); got != "http://odap.internal" {
		t.Fatalf("无反代头时应取请求 scheme，实际 %s", got)
	}
	r.Header.Set("X-Forwarded-Proto", "https")
	if got := a2aSelfBaseURL(r); got != "https://odap.internal" {
		t.Fatalf("X-Forwarded-Proto 优先，实际 %s", got)
	}
}

// ---------------------------------------------------------------------------
// send 类 params 校验
// ---------------------------------------------------------------------------

func TestParseA2ASendParams(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		raw     string
		wantErr bool
		want    string // 期望拼接文本
	}{
		{
			name: "单个文本段",
			raw:  `{"message":{"role":"user","parts":[{"kind":"text","text":"审查合同"}]},"metadata":{"agent_key":"contract-reviewer"}}`,
			want: "审查合同",
		},
		{
			name: "多文本段按序拼接",
			raw:  `{"message":{"role":"user","parts":[{"kind":"text","text":"a"},{"kind":"text","text":"b"}]}}`,
			want: "a\nb",
		},
		{name: "parts 缺失", raw: `{"message":{"role":"user"}}`, wantErr: true},
		{name: "无文本段（仅文件）", raw: `{"message":{"parts":[{"kind":"file","fileUri":"s3://x"}]}}`, wantErr: true},
		{name: "文本为空白", raw: `{"message":{"parts":[{"kind":"text","text":"   "}]}}`, wantErr: true},
		{name: "params 为 null", raw: `null`, wantErr: true},
		{name: "params 非对象", raw: `[1,2]`, wantErr: true},
		{
			name:    "文本超长（2001 字）",
			raw:     `{"message":{"parts":[{"kind":"text","text":"` + strings.Repeat("好", a2aObjectiveLimit+1) + `"}]}}`,
			wantErr: true,
		},
		{
			name: "恰好 2000 字",
			raw:  `{"message":{"parts":[{"kind":"text","text":"` + strings.Repeat("好", a2aObjectiveLimit) + `"}]}}`,
			want: strings.Repeat("好", a2aObjectiveLimit),
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			params, text, rpcErr := parseA2ASendParams(json.RawMessage(tc.raw))
			if tc.wantErr {
				if rpcErr == nil || rpcErr.Code != a2aCodeInvalidParams {
					t.Fatalf("期望 -32602，实际 %+v", rpcErr)
				}
				return
			}
			if rpcErr != nil {
				t.Fatalf("期望成功，实际报错：%s", rpcErr.Message)
			}
			if text != tc.want {
				t.Fatalf("文本期望 %q，实际 %q", tc.want, text)
			}
			if params == nil {
				t.Fatal("params 不应为 nil")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SSE 事件按 task id 过滤（防串流）
// ---------------------------------------------------------------------------

func TestA2AEventForTask(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		data  string
		task  string
		match bool
	}{
		{"run_id 匹配", `{"run_id":"r-1","status":"RUNNING"}`, "r-1", true},
		{"run_id 不匹配（必须丢弃防串流）", `{"run_id":"r-2"}`, "r-1", false},
		{"载荷无 run_id", `{"message":"hello"}`, "r-1", false},
		{"载荷非法 JSON", `not-json`, "r-1", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := a2aEventForTask(sse.Event{Type: "RunCreated", Data: []byte(tc.data)}, tc.task); got != tc.match {
				t.Fatalf("期望 %v，实际 %v", tc.match, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// task 视图：artifacts 与错误映射
// ---------------------------------------------------------------------------

func TestA2AArtifactsOf(t *testing.T) {
	t.Parallel()
	t.Run("output_payload 成为文本 artifact", func(t *testing.T) {
		run := &store.Run{ID: "r-1", Status: runstate.Completed, OutputPayload: []byte(`{"summary":"完成"}`)}
		arts := a2aArtifactsOf(run)
		if len(arts) != 1 {
			t.Fatalf("期望 1 个 artifact，实际 %d", len(arts))
		}
		parts, ok := arts[0]["parts"].([]map[string]any)
		if !ok || len(parts) != 1 || parts[0]["kind"] != "text" || parts[0]["text"] != `{"summary":"完成"}` {
			t.Fatalf("artifact 形状错误：%+v", arts[0])
		}
	})
	t.Run("本体审查 Run 回退 review_conclusion", func(t *testing.T) {
		run := &store.Run{ID: "r-2", Status: runstate.Completed, ReviewConclusion: []byte("结论")}
		arts := a2aArtifactsOf(run)
		if len(arts) != 1 || arts[0]["parts"].([]map[string]any)[0]["text"] != "结论" {
			t.Fatalf("review_conclusion 回退失败：%+v", arts)
		}
	})
	t.Run("无输出返回空数组", func(t *testing.T) {
		run := &store.Run{ID: "r-3", Status: runstate.Running}
		if arts := a2aArtifactsOf(run); len(arts) != 0 {
			t.Fatalf("无输出必须返回空数组（不编造），实际 %+v", arts)
		}
	})
}

// ---------------------------------------------------------------------------
// 错误映射与 tasks/list 状态过滤
// ---------------------------------------------------------------------------

func TestA2ARPCErrorOf(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		err      error
		wantCode int
	}{
		{"参数类 → -32602", errors.E(errors.CodeInvalidArgument, "参数不对"), a2aCodeInvalidParams},
		{"业务类 → -32000 带中文 message", errors.E(errors.CodeNotFound, "任务不存在"), a2aCodeServerError},
		{"未知错误 → -32603（不泄漏底层）", stderrors.New("raw pg error"), a2aCodeInternalError},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := a2aRPCErrorOf(tc.err)
			if got.Code != tc.wantCode {
				t.Fatalf("错误码期望 %d，实际 %d（message=%s）", tc.wantCode, got.Code, got.Message)
			}
		})
	}
}

func TestA2AStatusFilter(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in       string
		want     string
		wantCode int // 0 = 成功
	}{
		{"", "", 0},
		{"submitted", runstate.Created, 0},
		{"completed", runstate.Completed, 0},
		{"failed", runstate.Failed, 0},
		{"canceled", runstate.Cancelled, 0},
		{runstate.Running, runstate.Running, 0}, // 平台状态直通
		{"working", "", a2aCodeInvalidParams},   // 多对一状态不接受
		{"bogus", "", a2aCodeInvalidParams},
	}
	for _, tc := range cases {
		got, rpcErr := a2aStatusFilter(tc.in)
		if tc.wantCode == 0 {
			if rpcErr != nil || got != tc.want {
				t.Errorf("a2aStatusFilter(%q)：期望 %q，实际 %q（err=%+v）", tc.in, tc.want, got, rpcErr)
			}
			continue
		}
		if rpcErr == nil || rpcErr.Code != tc.wantCode {
			t.Errorf("a2aStatusFilter(%q)：期望 -32602，实际 %+v", tc.in, rpcErr)
		}
	}
}
