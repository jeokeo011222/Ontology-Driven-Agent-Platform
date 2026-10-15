package api

import (
	"os"
	"strings"
	"testing"

	"odap/internal/api/handlers"
	"odap/internal/platform/runstate"
)

// openapiPath 是契约中声明的路径（不含 method）。
func openAPIPaths(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("../../contracts/openapi.yaml")
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "  /") && strings.HasSuffix(line, ":") {
			out[strings.TrimSuffix(strings.TrimPrefix(line, "  "), ":")] = true
		}
	}
	if len(out) == 0 {
		t.Fatalf("no paths parsed from openapi.yaml")
	}
	return out
}

// TestOpenAPICoversAllRoutes 断言 contracts/openapi.yaml 覆盖全部已注册路由
// （契约规范 §5.3 CI 门禁："文档端点 ⊆ openapi 端点"）。
func TestOpenAPICoversAllRoutes(t *testing.T) {
	paths := openAPIPaths(t)
	h := handlers.New(handlers.Deps{})
	for _, rt := range Routes(h) {
		if !paths[rt.Pattern] {
			t.Fatalf("route %s %s missing from contracts/openapi.yaml", rt.Method, rt.Pattern)
		}
	}
	// 反向：文档中的每个路径都必须有对应实现（除公开探针外）。
	routePaths := map[string]bool{"/healthz": true, "/readyz": true}
	for _, rt := range Routes(h) {
		routePaths[rt.Pattern] = true
	}
	for p := range paths {
		if !routePaths[p] {
			t.Fatalf("openapi path %s has no implementation", p)
		}
	}
}

// TestOpenAPIRunStatusEnumMatchesRegistry 断言 openapi 中的 RunStatus 枚举与
// contracts/01 的 17 态逐项相等（同序）。
func TestOpenAPIRunStatusEnumMatchesRegistry(t *testing.T) {
	raw, err := os.ReadFile("../../contracts/openapi.yaml")
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	lines := strings.Split(string(raw), "\n")
	var (
		inRunStatus bool
		inEnum      bool
		got         []string
	)
	for _, line := range lines {
		if strings.HasPrefix(line, "    RunStatus:") {
			inRunStatus = true
			continue
		}
		if !inRunStatus {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "enum:" {
			inEnum = true
			continue
		}
		if inEnum {
			if strings.HasPrefix(trimmed, "- ") {
				got = append(got, strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
				continue
			}
			// 枚举块结束
			break
		}
	}
	if len(got) != len(runstate.All) {
		t.Fatalf("RunStatus enum length=%d want %d (%v)", len(got), len(runstate.All), got)
	}
	for i, want := range runstate.All {
		if got[i] != want {
			t.Fatalf("RunStatus[%d]=%s want %s", i, got[i], want)
		}
	}
}
