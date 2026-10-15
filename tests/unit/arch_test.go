// Package unit 承载跨模块的架构约束测试（第十六篇 §4.3）。
//
// 为什么必须存在：Go 编译器只保证"被引用的包存在"，不保证"引用方向正确"。
// 一旦 semantic 直接 import erp/adapters，编译通过、单测全绿，但
// §4.3「semantic 不应直接发起 ERP 写操作」这条架构约束已经被静默破坏，
// 且没有任何测试能发现——只有架构测试能拦住它（§4.3 明确要求
// "这些依赖关系应通过代码审查和架构测试持续维护"）。
package unit

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const repoRoot = "../.."

// 匹配 `import "odap/internal/x"`、`alias "odap/internal/x"` 与块内的 `"odap/internal/x"`。
var reODAPImport = regexp.MustCompile(`^\s*(?:import\s+)?(?:[A-Za-z_][A-Za-z0-9_]*\s+)?"(odap/internal/[A-Za-z0-9_/]+)"`)

// packageEdges 返回 from（相对仓库根，如 internal/ontology）→ 其 import 的 odap 内部包集合。
func packageEdges(t *testing.T) map[string]map[string]bool {
	t.Helper()
	edges := map[string]map[string]bool{}
	base := filepath.Join(repoRoot, "internal")
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, rerr := filepath.Rel(repoRoot, filepath.Dir(path))
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if m := reODAPImport.FindStringSubmatch(line); m != nil {
				if edges[rel] == nil {
					edges[rel] = map[string]bool{}
				}
				edges[rel][m[1]] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 internal 失败: %v", err)
	}
	if len(edges) == 0 {
		t.Fatal("未解析出任何包依赖（解析器失效，本测试的结论不可信）")
	}
	return edges
}

// underPkg 判断 pkg 是否等于 prefix 或位于其子包下（避免 "internal/erp" 误匹配 "internal/erpx"）。
func underPkg(pkg, prefix string) bool {
	return pkg == prefix || strings.HasPrefix(pkg, prefix+"/")
}

type archRule struct {
	name       string
	fromPrefix string
	fromExcept []string
	forbidden  []string
	reason     string
}

// 依据第十六篇 §4.3「Go 包之间的依赖规则」。
var archRules = []archRule{
	{
		name:       "语义层不得依赖 ERP",
		fromPrefix: "internal/ontology",
		forbidden:  []string{"odap/internal/erp"},
		reason:     "§4.3：semantic 不应直接发起 ERP 写操作，写操作必须经 Operation Gateway",
	},
	{
		name:       "授权层不得依赖具体 ERP Adapter",
		fromPrefix: "internal/authorization",
		forbidden:  []string{"odap/internal/erp"},
		reason:     "§4.3：authorization 不应依赖具体 ERP Adapter",
	},
	{
		name:       "Agent 不得绕过 Gateway 直连 Adapter",
		fromPrefix: "internal/agent",
		forbidden:  []string{"odap/internal/erp/adapters"},
		reason:     "§4.3：agent/planner 不应绕过 Harness 直接执行工具",
	},
	{
		name:       "平台层不得依赖业务域",
		fromPrefix: "internal/platform",
		forbidden: []string{
			"odap/internal/ontology", "odap/internal/agent", "odap/internal/harness",
			"odap/internal/llm", "odap/internal/erp", "odap/internal/authorization",
			"odap/internal/registry", "odap/internal/evidence", "odap/internal/tools",
			"odap/internal/execution", "odap/internal/api", "odap/internal/audit",
			"odap/internal/identity",
		},
		reason: "§4.3：依赖方向应自外向内（Handler → Service → Domain → Port → Adapter），平台层位于最内层",
	},
	{
		name:       "非传输层不得反向依赖 API 层",
		fromPrefix: "internal",
		fromExcept: []string{"internal/api"},
		forbidden:  []string{"odap/internal/api"},
		reason:     "§4.3：HTTP 传输层只能被 cmd 装配，领域包反向依赖会导致业务规则被传输细节绑死",
	},
}

func TestPackageDependencyDirection(t *testing.T) {
	edges := packageEdges(t)
	checked := 0
	for _, r := range archRules {
		for from, tos := range edges {
			if !underPkg(from, r.fromPrefix) {
				continue
			}
			skipped := false
			for _, ex := range r.fromExcept {
				if underPkg(from, ex) {
					skipped = true
					break
				}
			}
			if skipped {
				continue
			}
			checked++
			var hit []string
			for to := range tos {
				for _, f := range r.forbidden {
					if underPkg(to, f) {
						hit = append(hit, to)
						break
					}
				}
			}
			if len(hit) > 0 {
				sort.Strings(hit)
				t.Errorf("[%s] %s 依赖了 %s —— %s", r.name, from, strings.Join(hit, ", "), r.reason)
			}
		}
	}
	if checked == 0 {
		t.Fatal("没有匹配到任何包，规则可能已失效（fromPrefix 写错？）")
	}
	t.Logf("架构依赖规则 %d 条，覆盖 %d 个包", len(archRules), checked)
}

// TestAnalyzerSeesKnownEdges 自检：解析器必须能看到已知存在的真实依赖边。
// 若解析器失效（正则被改坏、目录结构变化），上面那条测试可能因为"什么都没看到"
// 而假绿——本测试专门堵这个洞。
func TestAnalyzerSeesKnownEdges(t *testing.T) {
	edges := packageEdges(t)
	known := []struct{ from, to string }{
		{"internal/authorization", "odap/internal/platform/ids"},
		{"internal/harness", "odap/internal/platform/runstate"},
	}
	for _, k := range known {
		tos, ok := edges[k.from]
		if !ok {
			t.Fatalf("解析器未能看到包 %s 的任何依赖（解析器失效）", k.from)
		}
		if !tos[k.to] {
			t.Fatalf("解析器未看到已知依赖边 %s → %s（解析器失效）", k.from, k.to)
		}
	}
}

// ---------------------------------------------------------------------------
// 图存储唯一入口（docs/图数据库的选型.md、ADR-005）
// ---------------------------------------------------------------------------

// graphStorePackage 是允许出现 RDF/SPARQL 与 Fuseki 具体细节的**唯一**包。
const graphStorePackage = "internal/ontology/graphstore"

// forbiddenOutsideGraphStore 列出只允许出现在 graphstore 包内的标识与语法片段。
//
// 这不是"提防恶意代码"，而是防止选型文档明令禁止的形态悄悄长出来：
// "不要让所有 Go 业务模块直接拼接 SPARQL 并访问 Fuseki。所有访问都经过 ODAP 的
// GraphStore Adapter"。一旦某个 handler 为了方便直接拼一句 SELECT，隔离、租户边界、
// 资源预算就全部绕过了，而且编译与功能测试都不会有任何反应。
//
// 判据刻意选择**语法上不可同形**的 token：
//   - Fuseki 专有标识（后端一旦替换就必须改名，不会天然重名）；
//   - SPARQL 的数据操作关键字与协议媒体类型（Go 里没有别的用途）。
var forbiddenOutsideGraphStore = []string{
	"FusekiStore", "FusekiConfig", "NewFusekiStore", "BackendFuseki",
	"EnvFusekiEndpoint", "EnvFusekiUsername", "EnvFusekiPassword",
	"FUSEKI_ENDPOINT", "FUSEKI_USERNAME", "FUSEKI_PASSWORD",
	"INSERT DATA", "DELETE DATA", "STRSTARTS",
	"application/sparql-query", "application/sparql-update", "application/sparql-results+json",
}

// gateSelfFiles 是本门禁**判据定义自身**所在的文件。
//
// 它必然包含上述 token（否则无从定义"禁止出现哪些 token"）。把它排除掉而不是
// 放宽判据，是为了保持"要么精确、要么不做"：如果改成匹配更宽松的模式，判据就会
// 退化成可被同形文本绕过的噪声。
var gateSelfFiles = map[string]bool{
	"tests/unit/arch_test.go": true,
}

// scanGraphStoreLeaks 返回 (违规位置列表, graphstore 包内命中次数)。
//
// 返回值第二个分量用于**自证**：若扫描器失效，包内命中次数会变成 0，
// 测试据此可以发现"因为什么都没扫到而假绿"。
func scanGraphStoreLeaks(t *testing.T) (leaks []string, insideHits int) {
	t.Helper()
	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "web": true, ".workbuddy": true,
		"artifacts": true, ".odap-objectstore": true, "vendor": true,
	}
	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] || (strings.HasPrefix(d.Name(), ".") && d.Name() != "." && d.Name() != "..") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, rerr := filepath.Rel(repoRoot, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if gateSelfFiles[rel] {
			return nil
		}
		inGraphStore := strings.HasPrefix(filepath.ToSlash(filepath.Dir(rel)), graphStorePackage)
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for i, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			// 注释不参与判定：文档注释里提到这些名字是正常的（本文件就是例子）。
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
				continue
			}
			for _, tok := range forbiddenOutsideGraphStore {
				if !strings.Contains(line, tok) {
					continue
				}
				if inGraphStore {
					insideHits++
					continue
				}
				leaks = append(leaks, fmt.Sprintf("%s:%d 出现 %q", rel, i+1, tok))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历仓库失败: %v", err)
	}
	return leaks, insideHits
}

func TestGraphStoreIsTheOnlySPARQLEntryPoint(t *testing.T) {
	leaks, insideHits := scanGraphStoreLeaks(t)
	if len(leaks) > 0 {
		sort.Strings(leaks)
		t.Errorf("图存储访问必须唯一经 %s，以下位置直接触及了 SPARQL/Fuseki 细节：\n  %s\n"+
			"依据：docs/图数据库的选型.md 与 docs/adr/ADR-005。请改为依赖 graphstore.GraphStore 接口。",
			graphStorePackage, strings.Join(leaks, "\n  "))
	}
	// 自证：扫描器必须在 graphstore 包内看到这些 token，否则上面的"无违规"毫无意义。
	if insideHits == 0 {
		t.Fatal("扫描器在 graphstore 包内未命中任何受控 token（扫描器失效，结论不可信）")
	}
	t.Logf("图存储唯一入口：包外违规 %d 处；包内受控命中 %d 处", len(leaks), insideHits)
}

// TestGraphStoreAdapterIsActuallyAssembled 验证适配器**真的被进程装配**，
// 而不是只存在于包里。
//
// 依据：L4 装配接线门禁的思想——从 cmd/ 出发做可达性闭包。没有这条，一个"写得很完整
// 但从未被任何进程构造"的适配器会让人误以为图数据库已经启用。
func TestGraphStoreAdapterIsActuallyAssembled(t *testing.T) {
	required := []string{"graphstore.Open", "graphstore.EnvConfigFromOS"}
	found := map[string]bool{}
	err := filepath.WalkDir(filepath.Join(repoRoot, "cmd"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for _, want := range required {
			if strings.Contains(string(raw), want) {
				found[want] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 cmd 失败: %v", err)
	}
	for _, want := range required {
		if !found[want] {
			t.Errorf("cmd/ 下没有任何进程调用 %s —— 图存储适配器未被装配（等于图数据库未启用）", want)
		}
	}
}

// TestGraphStoreRebindsReadinessProbe 验证图存储可用性被纳入就绪判定。
//
// 依据：§10.3 发布硬门禁"把未知状态报告为成功"。图存储不可用而 /readyz 仍 200，
// 会让编排系统把流量送到一个无法完成本体验证与语义查询的实例上。
func TestGraphStoreRebindsReadinessProbe(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot, "cmd", "api", "main.go"))
	if err != nil {
		t.Fatalf("读取 cmd/api/main.go 失败: %v", err)
	}
	src := string(raw)
	if !strings.Contains(src, "readinessProbe(pool, graphStore)") {
		t.Error("cmd/api 未把图存储纳入就绪探针（独立探针的后端不可用时 /readyz 仍会返回 200）")
	}
	if strings.Contains(src, "ReadyFunc:      pool.Ping,") {
		t.Error("cmd/api 仍在使用只探数据库的探针，图存储故障不会被反映到 /readyz")
	}
}
