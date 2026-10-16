// Package unit 携带"装配接线"门禁（第十六篇 §4.3 / 实现符合性评测清单 L4）。
//
// 为什么需要它：Go 编译器只保证"被引用的包存在"，**不保证"写好的包被引用"**。
// 一个完整实现、带齐全测试、`go test ./...` 全绿的包，可以完全没有任何生产调用方
// ——它在编译期、测试期、甚至覆盖率统计里都不显形，却意味着对应的能力在真实系统里
// 根本没有接线。本项目真实发生过：internal/ontology/{shacl,rdfs,semantic,cache}
// 共 2500+ 行实现与测试全部就绪，而生产代码的 import 数为 0，
// POST /ontologies/{id}/validate 因此长期恒返回 503。
//
// 该缺陷无法用"检查某个包是否存在"来发现（它存在，而且很健康），只能从
// **依赖可达性**上发现：从进程入口（cmd/）出发走不到它。
package unit

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// danglingAllowlist 声明"允许从 cmd/ 不可达"的包及其理由。
//
// 设计取舍：门禁默认拒绝未接线的包，例外必须**显式登记理由**。
// 若改成"只检查白名单里的包是否被引用"，就退化成了人工维护的清单——
// 新写的悬空包只要没人想起加进去，就永远不会被发现。
//
// 本表是一份**技术债清单**，不是许可：每条都在说明"这块能力已经写好、
// 但还没有生产调用方"，以及它等的是哪一步接线。允许它存在的前提是：
//   - 条目必须指向真实存在的包、必须写理由（TestDanglingAllowlistIsNotStale）；
//   - 条目一旦变为可达就必须删除（TestNoDanglingInternalPackages 的"陈旧豁免"分支）。
//
// 因此本表只会缩小，不会随时间长出新的成员。
var danglingAllowlist = map[string]string{
	// ── 本体规则引擎：本轮已接线 shacl，其余三项仍待接 ──
	"internal/ontology/rdfs": "RDFS 推理机已实现且测试全绿；但本体校验链目前只执行 SHACL，" +
		"推理物化（FactKindDerived）尚未接入校验流程，因此无生产调用方",
	"internal/ontology/semantic": "受控语义查询服务已实现；尚无对外查询端点消费它（第十三篇 §8 的查询面未开放）",
	"internal/ontology/cache": "语义查询结果缓存已实现；其消费方是 semantic.Service，" +
		"后者尚未接线，本包因此也不可达",

	// ── 执行侧：等待 worker 步进执行器（评测清单 L7）──
	// internal/agent 与 internal/evidence 已于 2026-10-16 接线（cmd/worker 步进执行器），
	// 从本表移除；inbox/lease 仍待接线。
	"internal/execution/inbox": "消费者幂等（Inbox 模式）已实现；尚无消费方循环接入",
	"internal/execution/lease": "任务租约的纯领域判定已实现，但生产路径使用的是 " +
		"harness.Scheduler 内的条件 UPDATE 实现——两份实现并存，应择一（让 harness 复用本包判定）；" +
		"在此之前不得再新增第三份",
}

// productionEdges 解析**生产代码**（排除 _test.go）的 odap 内部依赖边。
//
// 与 arch_test.go 的 packageEdges 有两处刻意不同：
//   - 排除 _test.go：测试文件里的 import 会制造"看起来已接线"的假象，
//     而"只有测试引用它"正是本门禁要抓的形态本身；
//   - 覆盖 cmd/ 与 internal/：根必须包含进程入口，否则无从判断"可达"。
func productionEdges(t *testing.T) map[string]map[string]bool {
	t.Helper()
	edges := map[string]map[string]bool{}
	found := 0
	for _, root := range []string{"cmd", "internal"} {
		base := filepath.Join(repoRoot, root)
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
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
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
			found++
			for _, line := range strings.Split(string(raw), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "//") {
					continue
				}
				if m := reODAPImport.FindStringSubmatch(line); m != nil {
					if edges[rel] == nil {
						edges[rel] = map[string]bool{}
					}
					// 统一为**仓库相对**路径：包列表（internalProductionPackages）
					// 用的是仓库相对路径，若这里存完整导入路径（odap/internal/x），
					// 可达集合与包列表就永远不相交——门禁会恒报"全部悬空"。
					edges[rel][strings.TrimPrefix(m[1], "odap/")] = true
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("遍历 %s 失败: %v", root, err)
		}
	}
	// 解析器自检：什么都没读到时必须失败，否则本文件所有结论都是空的（假绿）。
	if found == 0 {
		t.Fatal("未读到任何生产 .go 文件（解析器失效，本门禁结论不可信）")
	}
	if len(edges) == 0 {
		t.Fatal("未解析出任何生产依赖边（解析器失效，本门禁结论不可信）")
	}
	return edges
}

// internalProductionPackages 列出 internal/ 下所有含生产代码的包。
func internalProductionPackages(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	err := filepath.WalkDir(filepath.Join(repoRoot, "internal"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// testdata 是 Go 工具链约定的"永不 import"目录（编译器直接忽略），
			// 其中的 .go 文件是给测试用的编译夹具，不是生产代码——按惯例整目录跳过。
			if strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(repoRoot, filepath.Dir(path))
		if rerr != nil {
			return rerr
		}
		seen[filepath.ToSlash(rel)] = true
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 internal 失败: %v", err)
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	if len(out) == 0 {
		t.Fatal("未发现任何 internal 生产包（解析器失效）")
	}
	return out
}

// TestNoDanglingInternalPackages 断言每个 internal 生产包都能从 cmd/ 出发走到。
func TestNoDanglingInternalPackages(t *testing.T) {
	edges := productionEdges(t)
	pkgs := internalProductionPackages(t)

	// 反向索引：被谁引用。
	importedBy := map[string][]string{}
	for from, tos := range edges {
		for to := range tos {
			importedBy[to] = append(importedBy[to], from)
		}
	}

	// 从 cmd/* 出发做 BFS。
	reachable := map[string]bool{}
	queue := []string{}
	for from := range edges {
		if strings.HasPrefix(from, "cmd/") {
			reachable[from] = true
			queue = append(queue, from)
		}
	}
	if len(queue) == 0 {
		t.Fatal("cmd/ 下未发现任何生产包（进程入口缺失或解析器失效）")
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for to := range edges[cur] {
			if reachable[to] {
				continue
			}
			reachable[to] = true
			queue = append(queue, to)
		}
	}

	var dangling []string
	for _, p := range pkgs {
		if reachable[p] {
			if _, exempt := danglingAllowlist[p]; exempt {
				t.Errorf("[陈旧豁免] %s 已从 cmd/ 可达，但仍留在 danglingAllowlist 中 —— "+
					"请删除该条目，避免门禁保护范围随时间悄悄变窄", p)
			}
			continue
		}
		if _, exempt := danglingAllowlist[p]; exempt {
			continue
		}
		dangling = append(dangling, p)
	}
	if len(dangling) > 0 {
		sort.Strings(dangling)
		for _, p := range dangling {
			bys := importedBy[p]
			sort.Strings(bys)
			who := "无任何生产代码引用"
			if len(bys) > 0 {
				who = "仅被 " + strings.Join(bys, ", ") + " 引用，而这些包自身也不可达"
			}
			t.Errorf("[悬空包] %s：%s。已实现的包必须从 cmd/ 可达（否则能力没有真正接线）；"+
				"若确实要暂不接线，请在 danglingAllowlist 中登记理由", p, who)
		}
	}
	t.Logf("生产包 %d 个，其中可达 %d 个，显式豁免 %d 个",
		len(pkgs), countReachable(pkgs, reachable), len(danglingAllowlist))
}

// countReachable 统计可达包数（仅用于日志，不参与断言）。
func countReachable(pkgs []string, reachable map[string]bool) int {
	n := 0
	for _, p := range pkgs {
		if reachable[p] {
			n++
		}
	}
	return n
}

// TestDanglingAllowlistIsNotStale 断言豁免条目指向真实存在的包。
//
// 两个方向都要堵：包已删除却留着条目（保护范围变窄），以及包名写错
// （条目看起来"生效"，实际豁免的是一个不存在的包——真正的悬空包照旧通过）。
func TestDanglingAllowlistIsNotStale(t *testing.T) {
	pkgs := internalProductionPackages(t)
	exists := map[string]bool{}
	for _, p := range pkgs {
		exists[p] = true
	}
	for p, reason := range danglingAllowlist {
		if !exists[p] {
			t.Errorf("danglingAllowlist 中的 %q 不是真实存在的 internal 生产包（包名写错或包已删除）", p)
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("danglingAllowlist 中的 %q 未填写理由：豁免必须可审查", p)
		}
	}
}

// TestWiringGateSeesKnownEdges 自检：解析器必须能看到若干**已知存在**的生产依赖边。
//
// 若正则被改坏、或目录结构变化导致什么都没读到，上面那条测试会因"悬空集合为空"
// 而假绿。本测试用已知为真的边把它堵住——其中最后一条正是本轮打通的接线
// （本体校验 → SHACL 引擎），它一旦被移除，悬空包门禁必须重新变红。
func TestWiringGateSeesKnownEdges(t *testing.T) {
	edges := productionEdges(t)
	known := []struct{ from, to string }{
		{"cmd/api", "internal/api"},
		{"internal/api", "internal/api/handlers"},
		{"internal/api/handlers", "internal/ontology/graphload"},
		{"internal/api/handlers", "internal/ontology/shacl"},
		{"internal/ontology/graphload", "internal/ontology/graphstore"},
	}
	for _, k := range known {
		tos, ok := edges[k.from]
		if !ok {
			t.Fatalf("解析器未看到包 %s 的任何生产依赖", k.from)
		}
		if !tos[k.to] {
			t.Fatalf("解析器未看到已知生产依赖边 %s → %s（解析器失效或接线被移除）", k.from, k.to)
		}
	}
}
