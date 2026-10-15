package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"odap/mockerp/internal/domain"
	"odap/mockerp/internal/repository"
	"odap/mockerp/internal/service"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	svc := service.New(repository.NewMemory())
	h := NewHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func doJSON(t *testing.T, srv *httptest.Server, method, path string, body any, headers map[string]string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求失败：%v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, srv.URL+path, reader)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("请求 %s %s 失败：%v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp.StatusCode, out
}

func createClaim(t *testing.T, srv *httptest.Server, id string) {
	t.Helper()
	status, body := doJSON(t, srv, http.MethodPost, "/mock/objects", map[string]any{
		"object_type": "ExpenseClaim",
		"object_id":   id,
		"attributes":  map[string]any{"amount": "1280.50", "currency": "CNY"},
	}, nil)
	if status != http.StatusCreated {
		t.Fatalf("创建对象失败：status=%d body=%v", status, body)
	}
}

func getObject(t *testing.T, srv *httptest.Server, objectType, id string) map[string]any {
	t.Helper()
	status, body := doJSON(t, srv, http.MethodGet, "/mock/objects/"+objectType+"/"+id, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("读取对象失败：status=%d body=%v", status, body)
	}
	obj, ok := body["object"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺少 object：%v", body)
	}
	return obj
}

func TestObjectLifecycle(t *testing.T) {
	srv := newTestServer(t)
	createClaim(t, srv, "claim-001")

	obj := getObject(t, srv, "ExpenseClaim", "claim-001")
	if obj["status"] != domain.StatusDraft {
		t.Fatalf("初始状态应为 DRAFT，得到 %v", obj["status"])
	}
	if obj["version"] != float64(1) {
		t.Fatalf("初始版本应为 1，得到 %v", obj["version"])
	}

	if status, _ := doJSON(t, srv, http.MethodGet, "/mock/objects/ExpenseClaim/missing", nil, nil); status != http.StatusNotFound {
		t.Fatalf("未知对象应返回 404，得到 %d", status)
	}

	status, body := doJSON(t, srv, http.MethodPost, "/mock/objects/query", map[string]any{
		"object_type": "ExpenseClaim",
	}, nil)
	if status != http.StatusOK {
		t.Fatalf("查询失败：status=%d", status)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("查询应返回 1 条，得到 %d", len(items))
	}
}

func TestIdempotentReplay(t *testing.T) {
	srv := newTestServer(t)
	createClaim(t, srv, "claim-002")
	headers := map[string]string{"Idempotency-Key": "k-replay"}
	cmd := map[string]any{"operation": "expense.submit", "object_id": "claim-002"}

	status, first := doJSON(t, srv, http.MethodPost, "/mock/operations", cmd, headers)
	if status != http.StatusOK {
		t.Fatalf("首次提交失败：status=%d body=%v", status, first)
	}
	if first["status"] != domain.OpStatusSucceeded {
		t.Fatalf("首次提交应 SUCCEEDED，得到 %v", first["status"])
	}

	status, second := doJSON(t, srv, http.MethodPost, "/mock/operations", cmd, headers)
	if status != http.StatusOK {
		t.Fatalf("重复提交失败：status=%d", status)
	}
	if second["operation_id"] != first["operation_id"] {
		t.Fatalf("同幂等键应返回同一操作：%v vs %v", first["operation_id"], second["operation_id"])
	}
	if second["replay"] != true {
		t.Fatalf("重复提交应标记 replay=true，得到 %v", second["replay"])
	}

	obj := getObject(t, srv, "ExpenseClaim", "claim-002")
	if obj["version"] != float64(2) {
		t.Fatalf("重复提交不得重复产生副作用，版本应为 2，得到 %v", obj["version"])
	}
	if obj["status"] != domain.StatusPendingApproval {
		t.Fatalf("提交后状态应为 PENDING_APPROVAL，得到 %v", obj["status"])
	}
}

func TestIdempotencyConflict(t *testing.T) {
	srv := newTestServer(t)
	createClaim(t, srv, "claim-003")
	headers := map[string]string{"Idempotency-Key": "k-conflict"}

	if status, _ := doJSON(t, srv, http.MethodPost, "/mock/operations", map[string]any{
		"operation": "expense.submit", "object_id": "claim-003", "payload": map[string]any{"amount": 1280.50},
	}, headers); status != http.StatusOK {
		t.Fatalf("首次提交应成功，得到 %d", status)
	}

	status, body := doJSON(t, srv, http.MethodPost, "/mock/operations", map[string]any{
		"operation": "expense.submit", "object_id": "claim-003", "payload": map[string]any{"amount": 1580.50},
	}, headers)
	if status != http.StatusConflict {
		t.Fatalf("同键不同摘要应返回 409，得到 %d body=%v", status, body)
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj["code"] != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("错误码应为 IDEMPOTENCY_CONFLICT，得到 %v", errObj["code"])
	}
}

func TestFaultInjection(t *testing.T) {
	tests := []struct {
		name             string
		fault            map[string]any
		wantHTTP         int
		wantOpStatus     string
		wantObjectStatus string
		wantVersion      float64
	}{
		{
			name:             "timeout：已提交但响应丢失，客户端收到超时",
			fault:            map[string]any{"kind": "timeout", "operation_type": "expense.submit"},
			wantHTTP:         http.StatusGatewayTimeout,
			wantOpStatus:     domain.OpStatusUnknown,
			wantObjectStatus: domain.StatusPendingApproval,
			wantVersion:      2,
		},
		{
			name:             "unknown：结果未知但已执行",
			fault:            map[string]any{"kind": "unknown", "operation_type": "expense.submit"},
			wantHTTP:         http.StatusOK,
			wantOpStatus:     domain.OpStatusUnknown,
			wantObjectStatus: domain.StatusPendingApproval,
			wantVersion:      2,
		},
		{
			name:             "reject：业务拒绝且不产生副作用",
			fault:            map[string]any{"kind": "reject", "operation_type": "expense.submit"},
			wantHTTP:         http.StatusUnprocessableEntity,
			wantOpStatus:     "",
			wantObjectStatus: domain.StatusDraft,
			wantVersion:      1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(t)
			claimID := "claim-" + tc.fault["kind"].(string)
			createClaim(t, srv, claimID)

			if status, _ := doJSON(t, srv, http.MethodPost, "/mock/faults", tc.fault, nil); status != http.StatusOK {
				t.Fatalf("配置故障失败：%d", status)
			}

			headers := map[string]string{"Idempotency-Key": "k-" + tc.fault["kind"].(string)}
			status, body := doJSON(t, srv, http.MethodPost, "/mock/operations", map[string]any{
				"operation": "expense.submit", "object_id": claimID,
			}, headers)
			if status != tc.wantHTTP {
				t.Fatalf("HTTP 状态应为 %d，得到 %d body=%v", tc.wantHTTP, status, body)
			}
			if tc.wantOpStatus != "" && body["status"] != tc.wantOpStatus {
				t.Fatalf("操作状态应为 %s，得到 %v", tc.wantOpStatus, body["status"])
			}

			// 副作用是否落地的断言。
			obj := getObject(t, srv, "ExpenseClaim", claimID)
			if obj["status"] != tc.wantObjectStatus {
				t.Fatalf("对象状态应为 %s，得到 %v", tc.wantObjectStatus, obj["status"])
			}
			if obj["version"] != tc.wantVersion {
				t.Fatalf("对象版本应为 %v，得到 %v", tc.wantVersion, obj["version"])
			}

			// 超时场景：远端操作可被查询（对账路径），且能按幂等键查到。
			if tc.wantOpStatus == domain.OpStatusUnknown {
				opID, _ := body["operation_id"].(string)
				if opID == "" {
					t.Fatalf("未知结果响应必须带 operation_id：%v", body)
				}
				qs, qbody := doJSON(t, srv, http.MethodGet, "/mock/operations/"+opID, nil, nil)
				if qs != http.StatusOK || qbody["status"] != domain.OpStatusUnknown {
					t.Fatalf("按 ID 查询远端操作失败：status=%d body=%v", qs, qbody)
				}
				ks, kbody := doJSON(t, srv, http.MethodGet, "/mock/operations/by-idempotency-key/k-"+tc.fault["kind"].(string), nil, nil)
				if ks != http.StatusOK || kbody["operation_id"] != opID {
					t.Fatalf("按幂等键查询远端操作失败：status=%d body=%v", ks, kbody)
				}
			}
		})
	}
}

func TestVersionConflict(t *testing.T) {
	srv := newTestServer(t)
	createClaim(t, srv, "claim-version")

	status, body := doJSON(t, srv, http.MethodPost, "/mock/operations", map[string]any{
		"operation": "expense.submit", "object_id": "claim-version",
	}, map[string]string{"If-Match": `"9"`, "Idempotency-Key": "k-version"})
	if status != http.StatusConflict {
		t.Fatalf("版本不匹配应返回 409，得到 %d body=%v", status, body)
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj["code"] != "ERP_VERSION_CONFLICT" {
		t.Fatalf("错误码应为 ERP_VERSION_CONFLICT，得到 %v", errObj["code"])
	}
	// 版本冲突不得产生副作用。
	if obj := getObject(t, srv, "ExpenseClaim", "claim-version"); obj["status"] != domain.StatusDraft {
		t.Fatalf("版本冲突后对象不应变化，得到 %v", obj["status"])
	}
}

func TestHealth(t *testing.T) {
	srv := newTestServer(t)
	status, body := doJSON(t, srv, http.MethodGet, "/mock/health", nil, nil)
	if status != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("健康检查失败：status=%d body=%v", status, body)
	}
}

func TestDuplicateFaultBypassesDedup(t *testing.T) {
	srv := newTestServer(t)
	createClaim(t, srv, "claim-dup")
	headers := map[string]string{"Idempotency-Key": "k-dup"}
	cmd := map[string]any{"operation": "expense.submit", "object_id": "claim-dup"}

	if status, _ := doJSON(t, srv, http.MethodPost, "/mock/operations", cmd, headers); status != http.StatusOK {
		t.Fatalf("首次提交应成功")
	}
	// 注入 duplicate 故障：跳过幂等去重，强制重复接收。
	if status, _ := doJSON(t, srv, http.MethodPost, "/mock/faults", map[string]any{
		"kind": "duplicate", "operation_type": "expense.submit",
	}, nil); status != http.StatusOK {
		t.Fatalf("配置 duplicate 故障失败")
	}
	// 该次为同一 state 上的重复提交，submit 跃迁会因状态非法被拒（暴露重复接收）。
	status, _ := doJSON(t, srv, http.MethodPost, "/mock/operations", cmd, headers)
	if status != http.StatusConflict {
		t.Fatalf("duplicate 故障下重复提交应触发非法跃迁（409），得到 %d", status)
	}
}
