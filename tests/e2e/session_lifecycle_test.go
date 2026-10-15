//go:build e2e

// Package e2e 承载端到端测试（第十六篇 §10.2「端到端测试」）：
// 从 API 开始，走完一条完整业务链路，验证到最后的状态与副作用。
//
// 与集成测试的区别：这里不 import 内部包做断言，而是**真实发 HTTP 请求**，
// 因此能覆盖路由装配、认证链、序列化与错误码约定这些只有跨进程才暴露的问题。
// 需要已启动的 api 服务（compose 或本地进程），地址由 ODAP_E2E_BASE_URL 提供。
package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"odap/internal/seed"
)

func baseURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("ODAP_E2E_BASE_URL")
	if u == "" {
		t.Skip("ODAP_E2E_BASE_URL 未设置，跳过端到端测试")
	}
	return strings.TrimSuffix(u, "/")
}

func doJSON(t *testing.T, client *http.Client, method, url, token string, payload any) (int, []byte) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("序列化请求体失败: %v", err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s 请求失败: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// TestSessionLifecycleEndToEnd 覆盖"登录 → 会话校验 → 注销 → 注销后不可再用"整条链路。
//
// 为什么这四步必须一次跑完：注销的实现细节（会话标识取自 SessionID 还是 RequestID、
// UPDATE 是否真的命中目标行）在单步断言里都可能"看起来对"——只有
// "注销后同一令牌立刻失效"这一条能证明它真的落到了正确的会话上。
func TestSessionLifecycleEndToEnd(t *testing.T) {
	base := baseURL(t)
	login := os.Getenv("ODAP_E2E_LOGIN")
	if login == "" {
		login = "demo.admin"
	}
	password := os.Getenv("ODAP_E2E_PASSWORD")
	if password == "" {
		password = seed.DefaultDevLoginPassword
	}
	client := &http.Client{Timeout: 15 * time.Second}

	// ① 登录
	code, raw := doJSON(t, client, http.MethodPost, base+"/api/v1/auth/login", "", map[string]string{
		"login_name": login,
		"password":   password,
	})
	if code != http.StatusOK {
		t.Fatalf("登录返回 %d，期望 200；body=%s", code, raw)
	}
	var sess struct {
		SessionToken string `json:"session_token"`
	}
	if err := json.Unmarshal(raw, &sess); err != nil || sess.SessionToken == "" {
		t.Fatalf("登录响应缺少 session_token：err=%v body=%s", err, raw)
	}

	// ② 会话可用
	code, raw = doJSON(t, client, http.MethodGet, base+"/api/v1/auth/session", sess.SessionToken, nil)
	if code != http.StatusOK {
		t.Fatalf("会话校验返回 %d，期望 200；body=%s", code, raw)
	}

	// ③ 注销
	code, raw = doJSON(t, client, http.MethodPost, base+"/api/v1/auth/logout", sess.SessionToken, nil)
	if code != http.StatusNoContent && code != http.StatusOK {
		t.Fatalf("注销返回 %d，期望 204/200；body=%s", code, raw)
	}

	// ④ 注销后同一令牌必须立即失效（否则注销只是"看起来成功"）
	code, raw = doJSON(t, client, http.MethodGet, base+"/api/v1/auth/session", sess.SessionToken, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("注销后会话校验返回 %d，期望 401——注销未真正撤销会话；body=%s", code, raw)
	}
	if !strings.Contains(string(raw), "SESSION") && !strings.Contains(string(raw), "UNAUTHENTICATED") {
		t.Logf("提示：注销后 401 响应体未包含会话相关错误码：%s", raw)
	}
}

// TestHealthProbesArePublicAndHonest 存活探针必须公开且如实反映状态：
// healthz 恒 200；readyz 要么 200（依赖就绪）要么 503（依赖不可用），
// 绝不允许在依赖不可用时返回 200（§10.3："把未知业务状态错误报告为成功"）。
func TestHealthProbesArePublicAndHonest(t *testing.T) {
	base := baseURL(t)
	client := &http.Client{Timeout: 10 * time.Second}

	code, _ := doJSON(t, client, http.MethodGet, base+"/healthz", "", nil)
	if code != http.StatusOK {
		t.Fatalf("/healthz 返回 %d，期望 200", code)
	}

	code, raw := doJSON(t, client, http.MethodGet, base+"/readyz", "", nil)
	switch code {
	case http.StatusOK, http.StatusServiceUnavailable:
		// 两种都合法：如实反映就绪与否
	default:
		t.Fatalf("/readyz 返回 %d，期望 200 或 503（不得假成功也不得崩溃）；body=%s", code, raw)
	}
}
