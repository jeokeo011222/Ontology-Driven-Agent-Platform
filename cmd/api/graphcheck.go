// cmd/api 的图存储自检子命令。
//
// 为什么需要它：图存储（Fuseki/TDB2 或 PostgreSQL graph schema）是否**真的**可用，
// 不能靠"配置写了、进程起来了"来推断。本命令对当前装配的后端执行一轮端到端的
// 读/写/删/隔离/幂等断言，逐条打印真实结果，任一条失败即以非 0 退出码结束。
//
// 它同时是"适配器可替换"的验收手段：同一份断言在 postgres 与 fuseki 两个后端上
// 必须得到相同结论（能力差异除外，且差异由 Capabilities 显式声明并被本命令校验）。
//
// 用法：
//
//	go run ./cmd/api graphstore-check
//	go run ./cmd/api graphstore-check --tenant <uuid> --graph <iri>
//
// 清理语义：只删除本次写入的三元组。命名图元数据行（仅 PG 后端有）使用**固定的
// 自检图 IRI**，因此重复运行最多残留一行，不影响后续运行与业务数据。
package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/ontology/graphstore"
	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
)

// checkGraphIRI 是自检专用的固定命名图 IRI（与业务图隔离，便于识别与清理）。
const checkGraphIRI = "https://odap.example/__graphstore_check__"

// graphCheckResult 汇总自检结论。
type graphCheckResult struct {
	passed  int
	failed  int
	skipped int
}

func (r *graphCheckResult) ok(name, detail string) {
	r.passed++
	fmt.Printf("  ✓ %-42s %s\n", name, detail)
}

func (r *graphCheckResult) bad(name string, err error) {
	r.failed++
	fmt.Printf("  ✗ %-42s %v\n", name, err)
}

func (r *graphCheckResult) skip(name, reason string) {
	r.skipped++
	fmt.Printf("  – %-42s 跳过：%s\n", name, reason)
}

// graphStoreCheck 是 `api graphstore-check` 的入口。
func graphStoreCheck(args []string) error {
	fs := flag.NewFlagSet("graphstore-check", flag.ExitOnError)
	tenantFlag := fs.String("tenant", "", "自检使用的租户 UUID；留空时自动选取")
	graphFlag := fs.String("graph", checkGraphIRI, "自检使用的命名图 IRI")
	timeoutFlag := fs.Int("timeout", 60, "整轮自检的超时（秒）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := graphstore.EnvConfigFromOS()
	if err != nil {
		return err
	}
	fmt.Printf("图存储自检：%s\n", cfg.Describe())

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutFlag)*time.Second)
	defer cancel()

	var pool *pgxpool.Pool
	if cfg.Backend == graphstore.BackendPostgres {
		dbURL := databaseURL()
		if dbURL == "" {
			return fmt.Errorf("ODAP_DATABASE_URL (or DATABASE_URL) is required for postgres 后端自检")
		}
		p, err := pgxutil.Connect(ctx, dbURL)
		if err != nil {
			return err
		}
		defer p.Close()
		pool = p
		cfg.Pool = p
	}

	store, err := graphstore.Open(cfg)
	if err != nil {
		return err
	}

	tenant, tenantNote, err := resolveCheckTenant(ctx, cfg, pool, *tenantFlag)
	if err != nil {
		return err
	}
	otherTenant := ids.New()
	fmt.Printf("租户：%s（%s）\n", tenant, tenantNote)
	fmt.Printf("命名图：%s\n\n", *graphFlag)

	res := &graphCheckResult{}

	// ---- 0. 可达性 ----
	if pinger, ok := store.(interface{ Ping(context.Context) error }); ok {
		if err := pinger.Ping(ctx); err != nil {
			res.bad("图存储可达性（Ping）", err)
		} else {
			res.ok("图存储可达性（Ping）", "OK")
		}
	} else {
		res.skip("图存储可达性（Ping）", "此后端未提供独立探针（PG 后端以连接池探针为准）")
	}

	// ---- 1. 能力声明自洽性 ----
	// 探针必须先自证：Capabilities().ResourceDictionary 与实际是否实现
	// TermDictionary 接口必须一致，否则"能力声明"本身就是不可信的元数据。
	caps := store.Capabilities()
	_, hasDict := store.(graphstore.TermDictionary)
	if caps.ResourceDictionary == hasDict {
		res.ok("能力声明自洽（ResourceDictionary）",
			fmt.Sprintf("声明=%t 实现=%t", caps.ResourceDictionary, hasDict))
	} else {
		res.bad("能力声明自洽（ResourceDictionary）",
			fmt.Errorf("Capabilities().ResourceDictionary=%t 与 TermDictionary 实现=%t 不一致",
				caps.ResourceDictionary, hasDict))
	}

	// ---- 2. 幂等建图 ----
	h1, err := store.EnsureGraph(ctx, tenant, *graphFlag, "INSTANCE", "check")
	if err != nil {
		res.bad("EnsureGraph 首次调用", err)
		return finish(res)
	}
	h2, err := store.EnsureGraph(ctx, tenant, *graphFlag, "INSTANCE", "check")
	if err != nil {
		res.bad("EnsureGraph 幂等调用", err)
		return finish(res)
	}
	if h1 != h2 {
		res.bad("EnsureGraph 幂等调用", fmt.Errorf("两次返回不同 graph_id：%d / %d", h1, h2))
	} else {
		res.ok("EnsureGraph 幂等调用", fmt.Sprintf("graph_id=%d（持久标识=%t）", h1, caps.PersistentGraphID))
	}

	// ---- 3. 非法输入 fail-closed ----
	if _, err := store.Match(ctx, ids.ID{}, graphstore.Pattern{}); err == nil {
		res.bad("零租户 Match 必须被拒绝", fmt.Errorf("未返回错误（租户边界失效）"))
	} else {
		res.ok("零租户 Match 必须被拒绝", "已拒绝")
	}
	if _, err := store.InsertTriples(ctx, ids.ID{}, *graphFlag, nil); err == nil {
		res.bad("零租户 InsertTriples 必须被拒绝", fmt.Errorf("未返回错误"))
	} else {
		res.ok("零租户 InsertTriples 必须被拒绝", "已拒绝")
	}

	// ---- 4. 写入（含需要转义的字面值与空白节点）----
	ex := "https://odap.example/finance#"
	mkIRI := func(s string) graphstore.Term { return graphstore.Term{Kind: graphstore.KindIRI, Lexical: s} }
	probe := []graphstore.Triple{
		{S: mkIRI(ex + "ProbeClaim"), P: mkIRI(ex + "probeLabel"),
			O: graphstore.Term{Kind: graphstore.KindLiteral, Lexical: "带\"引号\"与\n换行的报销单"}},
		{S: mkIRI(ex + "ProbeClaim"), P: mkIRI(ex + "probeAmount"),
			O: graphstore.Term{Kind: graphstore.KindLiteral, Lexical: "128.50",
				Datatype: "http://www.w3.org/2001/XMLSchema#decimal"}},
		{S: mkIRI(ex + "ProbeClaim"), P: mkIRI(ex + "probeNote"),
			O: graphstore.Term{Kind: graphstore.KindBlankNode, Lexical: "b1/带斜杠"}},
	}
	// 先清理历史残留，使断言可重复（自检图是固定的，必须能反复跑）。
	if n, err := store.DeleteTriples(ctx, tenant, *graphFlag, probe); err == nil && n > 0 {
		fmt.Printf("  · 清理历史残留三元组 %d 条\n", n)
	}
	n, err := store.InsertTriples(ctx, tenant, *graphFlag, probe)
	if err != nil {
		res.bad("InsertTriples 首轮写入", err)
		return finish(res)
	}
	if n != len(probe) {
		res.bad("InsertTriples 首轮写入", fmt.Errorf("期望新增 %d 条，实际 %d 条", len(probe), n))
	} else {
		res.ok("InsertTriples 首轮写入", fmt.Sprintf("新增 %d 条（含转义字面值与空白节点）", n))
	}

	// ---- 5. 幂等：重复写入不得虚报新增 ----
	n, err = store.InsertTriples(ctx, tenant, *graphFlag, probe)
	if err != nil {
		res.bad("InsertTriples 重复写入幂等", err)
	} else if n != 0 {
		res.bad("InsertTriples 重复写入幂等", fmt.Errorf("期望新增 0 条，实际 %d 条（虚报）", n))
	} else {
		res.ok("InsertTriples 重复写入幂等", "新增 0 条")
	}

	// ---- 6. 计数 ----
	cnt, err := store.CountTriples(ctx, tenant, *graphFlag)
	if err != nil {
		res.bad("CountTriples", err)
	} else if cnt != int64(len(probe)) {
		res.bad("CountTriples", fmt.Errorf("期望 %d，实际 %d", len(probe), cnt))
	} else {
		res.ok("CountTriples", fmt.Sprintf("%d 条", cnt))
	}

	// ---- 7. 模式匹配（主谓绑定 / 通配）----
	var b strings.Builder
	bound, err := store.Match(ctx, tenant, graphstore.Pattern{
		P: ptrTerm(mkIRI(ex + "probeAmount")), GraphIRI: *graphFlag,
	})
	if err != nil {
		res.bad("Match（谓词绑定）", err)
	} else if len(bound) != 1 {
		res.bad("Match（谓词绑定）", fmt.Errorf("期望 1 条，实际 %d 条", len(bound)))
	} else {
		b.Reset()
		b.WriteString(bound[0].O.Lexical)
		if bound[0].O.Datatype == "" {
			b.WriteString("（无 datatype）")
		} else {
			b.WriteString("^^" + bound[0].O.Datatype)
		}
		res.ok("Match（谓词绑定）", fmt.Sprintf("1 条，宾语=%s", b.String()))
	}

	all, err := store.Match(ctx, tenant, graphstore.Pattern{GraphIRI: *graphFlag})
	if err != nil {
		res.bad("Match（全通配）", err)
	} else if len(all) != len(probe) {
		res.bad("Match（全通配）", fmt.Errorf("期望 %d 条，实际 %d 条", len(probe), len(all)))
	} else {
		res.ok("Match（全通配）", fmt.Sprintf("%d 条", len(all)))
	}

	crossGraph, err := store.Match(ctx, tenant, graphstore.Pattern{})
	if err != nil {
		res.bad("Match（跨命名图通配）", err)
	} else if len(crossGraph) < len(probe) {
		res.bad("Match（跨命名图通配）",
			fmt.Errorf("期望至少 %d 条（含本次写入），实际 %d 条", len(probe), len(crossGraph)))
	} else {
		res.ok("Match（跨命名图通配）", fmt.Sprintf("%d 条", len(crossGraph)))
	}

	// ---- 8. 空白节点可寻址（适配器提升为受控 IRI 后才可能）----
	bnode, err := store.Match(ctx, tenant, graphstore.Pattern{
		O:        ptrTerm(graphstore.Term{Kind: graphstore.KindBlankNode, Lexical: "b1/带斜杠"}),
		GraphIRI: *graphFlag,
	})
	if err != nil {
		res.bad("空白节点可寻址（Match）", err)
	} else if len(bnode) != 1 {
		res.bad("空白节点可寻址（Match）", fmt.Errorf("期望 1 条，实际 %d 条", len(bnode)))
	} else if bnode[0].O.Kind != graphstore.KindBlankNode {
		res.bad("空白节点可寻址（Match）", fmt.Errorf("读回的 term_kind=%d，期望 KindBlankNode", bnode[0].O.Kind))
	} else {
		res.ok("空白节点可寻址（Match）", "读到 1 条且 kind 还原为空白节点")
	}

	// ---- 9. 租户隔离（读路径）----
	otherMatch, err := store.Match(ctx, otherTenant, graphstore.Pattern{GraphIRI: *graphFlag})
	if err != nil {
		res.bad("跨租户 Match 隔离", err)
	} else if len(otherMatch) != 0 {
		res.bad("跨租户 Match 隔离", fmt.Errorf("另一租户读到 %d 条（隔离失效）", len(otherMatch)))
	} else {
		res.ok("跨租户 Match 隔离", "另一租户读到 0 条")
	}
	otherCount, err := store.CountTriples(ctx, otherTenant, *graphFlag)
	if err != nil {
		res.bad("跨租户 CountTriples 隔离", err)
	} else if otherCount != 0 {
		res.bad("跨租户 CountTriples 隔离", fmt.Errorf("另一租户计数 %d（隔离失效）", otherCount))
	} else {
		res.ok("跨租户 CountTriples 隔离", "另一租户计数 0")
	}

	// ---- 10. 删除与清理 ----
	del, err := store.DeleteTriples(ctx, tenant, *graphFlag, probe[:1])
	if err != nil {
		res.bad("DeleteTriples（已存在）", err)
	} else if del != 1 {
		res.bad("DeleteTriples（已存在）", fmt.Errorf("期望删除 1 条，实际 %d 条", del))
	} else {
		res.ok("DeleteTriples（已存在）", "删除 1 条")
	}
	delAgain, err := store.DeleteTriples(ctx, tenant, *graphFlag, probe[:1])
	if err != nil {
		res.bad("DeleteTriples 幂等", err)
	} else if delAgain != 0 {
		res.bad("DeleteTriples 幂等", fmt.Errorf("期望删除 0 条，实际 %d 条", delAgain))
	} else {
		res.ok("DeleteTriples 幂等", "重复删除 0 条")
	}
	// 无条件清理（即使上面的断言失败也要回收，避免污染共享图存储）。
	if _, err := store.DeleteTriples(ctx, tenant, *graphFlag, probe); err != nil {
		fmt.Printf("  ! 清理残留失败（请人工检查命名图 %s）：%v\n", *graphFlag, err)
	}
	final, err := store.CountTriples(ctx, tenant, *graphFlag)
	if err != nil {
		res.bad("清理后计数归零", err)
	} else if final != 0 {
		res.bad("清理后计数归零", fmt.Errorf("清理后仍有 %d 条", final))
	} else {
		res.ok("清理后计数归零", "0 条")
	}

	return finish(res)
}

// finish 打印汇总并返回（有失败即返回错误 → 非 0 退出码）。
func finish(r *graphCheckResult) error {
	fmt.Printf("\n汇总：通过 %d · 失败 %d · 跳过 %d\n", r.passed, r.failed, r.skipped)
	if r.failed > 0 {
		return fmt.Errorf("图存储自检未通过：%d 项失败", r.failed)
	}
	fmt.Println("图存储自检通过。")
	return nil
}

// resolveCheckTenant 决定自检使用的租户。
//
// PG 后端写入需要租户分区存在：优先复用既有 ACTIVE 租户，避免为一次性自检
// 在真实库里留下新分区；Fuseki 后端的租户只用于命名空间，任意 UUID 均可。
func resolveCheckTenant(ctx context.Context, cfg graphstore.OpenConfig, pool *pgxpool.Pool, override string) (ids.ID, string, error) {
	if override != "" {
		id, err := ids.Parse(override)
		if err != nil {
			return ids.ID{}, "", fmt.Errorf("--tenant 不是合法 UUID: %w", err)
		}
		return id, "由 --tenant 指定", nil
	}
	if cfg.Backend == graphstore.BackendPostgres && pool != nil {
		var raw string
		err := pool.QueryRow(ctx, `SELECT id::text FROM iam.tenants WHERE status = 'ACTIVE' ORDER BY id LIMIT 1`).Scan(&raw)
		if err == nil {
			id, perr := ids.Parse(raw)
			if perr != nil {
				return ids.ID{}, "", perr
			}
			return id, "复用既有 ACTIVE 租户（不新建分区）", nil
		}
		return ids.New(), "未找到 ACTIVE 租户，使用临时租户（将为它创建分区）", nil
	}
	return ids.New(), "为本次自检生成的临时租户（Fuseki 后端只需命名空间）", nil
}

// ptrTerm 返回词项指针（Pattern 的位置绑定形式）。
func ptrTerm(t graphstore.Term) *graphstore.Term { return &t }
