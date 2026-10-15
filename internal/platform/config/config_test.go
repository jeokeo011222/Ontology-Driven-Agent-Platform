package config

import "testing"

// TestLoadPlatformDBRequirementIsServiceScoped 固化“按服务决定是否要求平台库连接串”
// 的规则：api/worker/migrate 必须提供 ODAP_DATABASE_URL；mock-erp 不要求平台库连接串
// （它独立于平台库运行，不应因缺少无关配置而整体失败——否则 LOG_LEVEL 等会被静默丢弃）。
// 同时确认 mock-erp 仍必须提供自己的 MOCKERP_DATABASE_URL。
func TestLoadPlatformDBRequirementIsServiceScoped(t *testing.T) {
	cases := []struct {
		name        string
		service     string
		odapURL     string
		mockERPURL  string
		wantErr     bool
		wantAddrUse string
	}{
		{name: "api with platform db", service: "api", odapURL: "postgres://x", wantErr: false},
		{name: "api without platform db", service: "api", odapURL: "", wantErr: true},
		{name: "worker without platform db", service: "worker", odapURL: "", wantErr: true},
		{name: "migrate without platform db", service: "migrate", odapURL: "", wantErr: true},
		// mock-erp：不要求平台库；应加载成功并读回 MOCKERP_HTTP_ADDR
		{name: "mock-erp without platform db", service: "mock-erp", odapURL: "", mockERPURL: "postgres://m", wantErr: false, wantAddrUse: ":9999"},
		// mock-erp：提供了平台库也应成功（向后兼容）
		{name: "mock-erp with platform db", service: "mock-erp", odapURL: "postgres://x", mockERPURL: "postgres://m", wantErr: false, wantAddrUse: ":9999"},
		// mock-erp：仍必须有自己的库连接串
		{name: "mock-erp without own db", service: "mock-erp", odapURL: "", mockERPURL: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ODAP_DATABASE_URL", tc.odapURL)
			t.Setenv("MOCKERP_DATABASE_URL", tc.mockERPURL)
			t.Setenv("MOCKERP_HTTP_ADDR", ":9999")
			cfg, err := Load(tc.service)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Load(%q) 期望报错，实际成功", tc.service)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load(%q) 意外报错: %v", tc.service, err)
			}
			if tc.wantAddrUse != "" && cfg.MockERPHTTPAddr != tc.wantAddrUse {
				t.Fatalf("Load(%q).MockERPHTTPAddr = %q, want %q", tc.service, cfg.MockERPHTTPAddr, tc.wantAddrUse)
			}
		})
	}
}
