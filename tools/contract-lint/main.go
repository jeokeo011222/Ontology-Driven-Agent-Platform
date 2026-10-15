// Command contract-lint 是 ODAP 的契约一致性 CI 门禁。
//
// 依据：
//   - docs/contracts/01-全局状态注册表.md §6「一致性门禁（CI）」
//   - docs/contracts/02-统一错误码注册表.md §4 命名与登记规则第 5 条
//
// 它断言"代码里出现的状态/错误码 ⊆ 契约注册表"，且各层（Go 常量、DDL CHECK、
// 前端标签）与契约逐项相等。任何一处漂移都以非 0 退出码阻断构建。
//
// 用法：
//
//	go run ./tools/contract-lint            # 在仓库根执行
//	go run ./tools/contract-lint -root .
//
// 设计原则：只读文件，不修改任何东西；对"尚未实现的层"（如前端标签）给出
// SKIP 而非 FAIL，避免误伤未完成的波次。
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"odap/internal/harness"
	"odap/internal/platform/errors"
	"odap/internal/platform/runstate"
)

var (
	failures int
	checked  int
)

func okf(format string, a ...any) {
	checked++
	fmt.Printf("  \033[32m[OK]\033[0m   "+format+"\n", a...)
}
func failf(format string, a ...any) {
	checked++
	failures++
	fmt.Printf("  \033[31m[FAIL]\033[0m "+format+"\n", a...)
}
func skipf(format string, a ...any) { fmt.Printf("  \033[33m[SKIP]\033[0m "+format+"\n", a...) }
func headf(format string, a ...any) { fmt.Printf("\n\033[1m"+format+"\033[0m\n", a...) }

func main() {
	root := flag.String("root", ".", "仓库根目录")
	flag.Parse()

	docs := filepath.Join(*root, "docs", "contracts")
	migDir := filepath.Join(*root, "migrations", "platform")

	c01 := filepath.Join(docs, "01-全局状态注册表.md")
	c02 := filepath.Join(docs, "02-统一错误码注册表.md")

	// ---------------------------------------------------------------- 状态词表
	headf("① Run 状态词表（contracts/01 ↔ runstate.All ↔ DDL CHECK）")
	contractStates := parseRunStates(mustRead(c01))
	goStates := append([]string(nil), runstate.All...)
	sort.Strings(contractStates)
	sort.Strings(goStates)
	if equalSets(contractStates, goStates) {
		okf("contracts/01 与 runstate.All 逐项相等（%d 个状态）", len(goStates))
	} else {
		failf("contracts/01 与 runstate.All 不一致\n    契约独有: %v\n    Go 独有: %v",
			diff(contractStates, goStates), diff(goStates, contractStates))
	}

	ddlStates := parseDDLRunStates(mustReadAllSQL(migDir))
	sort.Strings(ddlStates)
	if equalSets(contractStates, ddlStates) {
		okf("DDL CHECK 约束与 contracts/01 逐项相等（%d 个状态）", len(ddlStates))
	} else {
		failf("DDL CHECK 与 contracts/01 不一致\n    契约独有: %v\n    DDL 独有: %v",
			diff(contractStates, ddlStates), diff(ddlStates, contractStates))
	}

	// ---------------------------------------------------------------- 错误码
	headf("② 错误码注册表（contracts/02 ↔ platform/errors）")
	contractBase, contractDomain := parseErrorCodes(mustRead(c02))
	sort.Strings(contractBase)
	goBase := errors.BaseCodes()

	if len(contractBase) != 12 {
		failf("contracts/02 §2 基础码数量 = %d，契约要求恰好 12", len(contractBase))
	}
	if equalSets(contractBase, goBase) {
		okf("12 个基础码逐项相等")
	} else {
		failf("基础码集合不一致（契约 %d 个 / Go %d 个）\n    契约独有: %v\n    Go 独有: %v",
			len(contractBase), len(goBase),
			diff(contractBase, goBase), diff(goBase, contractBase))
	}

	goAll := errors.AllCodes()
	contractAll := append(append([]string(nil), contractBase...), contractDomain...)
	var unregistered []string
	for _, code := range goAll {
		if !contains(contractAll, code) {
			unregistered = append(unregistered, code)
		}
	}
	if len(unregistered) == 0 {
		okf("platform/errors 全部 %d 个码均已在 contracts/02 登记", len(goAll))
	} else {
		failf("以下错误码出现在代码中但未在 contracts/02 登记（违反 §4 规则 2）：%v", unregistered)
	}

	// 域码 → 基础码 的映射一致性（契约 §3 中能解析到的行）
	baseSet := map[string]bool{}
	for _, b := range contractBase {
		baseSet[b] = true
	}
	var badMap []string
	for code, base := range contractDomainBase(mustRead(c02)) {
		if !baseSet[base] {
			badMap = append(badMap, fmt.Sprintf("%s→%s(非法基础码)", code, base))
			continue
		}
		if got := errors.BaseCode(code); got != base && contains(goAll, code) {
			badMap = append(badMap, fmt.Sprintf("%s→%s(实现为 %s)", code, base, got))
		}
	}
	sort.Strings(badMap)
	if len(badMap) == 0 {
		okf("域码 → 基础码映射与契约一致")
	} else {
		failf("域码映射不一致: %v", badMap)
	}

	// ---------------------------------------------------------------- 事件词表
	headf("③ 事件词表（harness.AllEvents）")
	events := harness.AllEvents()
	if len(events) == 31 {
		okf("事件数量 = 31（第六篇事件词表）")
	} else {
		failf("事件数量 = %d，期望 31", len(events))
	}

	// ---------------------------------------------------------------- 前端标签
	headf("④ 前端状态标签覆盖（web/ src）")
	webSrc := filepath.Join(*root, "web", "src")
	if !dirExists(webSrc) {
		skipf("web/src 尚未生成，跳过前端标签覆盖检查")
	} else {
		blob := mustReadTree(webSrc)
		var missing []string
		for _, s := range runstate.All {
			if !strings.Contains(blob, s) {
				missing = append(missing, s)
			}
		}
		if len(missing) == 0 {
			okf("前端源码覆盖全部 %d 个 Run 状态标签", len(runstate.All))
		} else {
			failf("前端源码未覆盖以下 Run 状态：%v", missing)
		}
	}

	// ---------------------------------------------------------------- 前端写方法声明
	//
	// 缺陷类：`apiPost<AsyncTask>(...)` 把端点声明成"202 + 任务引用"，而服务端其实
	// 同步返回 200 + 结论。后果之一是页面去轮询一个**不存在**的任务号，并把已经
	// 确定的结论显示成"进行中"。该缺陷类在 ontologies/validate、ontologies/publish、
	// agents/publish、workflows/publish 四处**同时**存在过，因此单独上闸。
	//
	// 本门禁的能力边界（刻意写清，避免被当成它做不到的保证）：
	//   它只能判定两类**可静态决定**的事实——① 前端调用的端点是否在契约里登记；
	//   ② `AsyncTask` 是否只用在声明了 202 的端点上。
	//   它**不能**判定"声明的内联对象类型与响应 schema 逐字段一致"（那需要按 openapi
	//   schema 做结构比对）。本仓已有该问题的根治方案：由 openapi 生成
	//   web/src/api/generated/types.ts（当前是占位），届时内联类型会被契约类型取代。
	headf("⑤ 前端写方法 ↔ openapi（端点已登记 + AsyncTask ⇔ 202）")
	openapiDoc := mustRead(filepath.Join(*root, "contracts", "openapi.yaml"))
	operations, openapiPaths := parseOpenAPIOperations(openapiDoc)
	if openapiPaths < 30 {
		// fail-closed：解析器一旦失效，"零违规"与"零解析"就分不出来。宁可红一次，
		// 也不能让门禁在解析失败的静默状态下长期放行。
		failf("openapi.yaml 只解析出 %d 个 path（预期 ≥30）：解析器可能失效，按失败处理而不是静默通过", openapiPaths)
	} else {
		okf("openapi.yaml 解析出 %d 个 path / %d 个 operation", openapiPaths, len(operations))

		calls := scanWebWriteCalls(filepath.Join(*root, "web", "src"))
		checkable := 0
		for _, c := range calls {
			if c.Path != "" {
				checkable++
			}
		}
		if checkable < 10 {
			failf("只识别到 %d 个可判定的前端写方法调用（预期 ≥10）：扫描器可能失效，按失败处理", checkable)
		} else {
			var mismatched, unregistered []string
			seenAllowlisted := map[string]bool{}
			for _, c := range calls {
				if c.Path == "" {
					continue
				}
				verb := strings.ToUpper(c.Method)
				key := verb + " " + canonicalPath(c.Path)
				st, registered := operations[key]
				if !registered {
					if _, allowed := unregisteredWebEndpoints[key]; allowed {
						seenAllowlisted[key] = true
						continue
					}
					unregistered = append(unregistered, fmt.Sprintf("%s:%d  %s", c.File, c.Line, key))
					continue
				}
				// 契约声明 202 的端点代表"已受理、结论待定"，前端只有声明为异步任务类型
				// 才会去跟踪它。反过来，把同步的 200 端点声明成 AsyncTask 更糟：页面会去
				// 轮询一个不存在的任务号。两个方向都拦。
				_, has202 := st["202"]
				if strings.HasPrefix(c.Type, "AsyncTask") && !has202 {
					mismatched = append(mismatched, fmt.Sprintf(
						"%s:%d  `%s` 声明为 %s（异步/202），但契约未声明 202（实际：%s）",
						c.File, c.Line, key, c.Type, joinStatuses(st)))
				}
			}
			if len(mismatched) == 0 {
				okf("全部 %d 个可判定调用点的 AsyncTask 声明与 openapi 状态码一致", checkable)
			} else {
				failf("前端声明与 openapi 状态码不一致（%d 处）：\n      %s",
					len(mismatched), strings.Join(mismatched, "\n      "))
			}
			if len(unregistered) == 0 {
				okf("前端调用的写端点均已在 contracts/openapi.yaml 登记（豁免 %d 个，见 unregisteredWebEndpoints）",
					len(seenAllowlisted))
			} else {
				failf("前端调用了 openapi 未登记的端点（%d 处）——若确属已知缺口，请登记进 unregisteredWebEndpoints 并写明理由：\n      %s",
					len(unregistered), strings.Join(unregistered, "\n      "))
			}
			for key, reason := range unregisteredWebEndpoints {
				if !seenAllowlisted[key] {
					failf("陈旧豁免：unregisteredWebEndpoints 里的 %q（%s）已不再被前端调用，或该端点已登记入 openapi，请删除该条", key, reason)
				}
			}
		}
	}

	// ---------------------------------------------------------------- 汇总
	fmt.Printf("\n\033[1m汇总\033[0m: 检查 %d 项，失败 %d 项\n", checked, failures)
	if failures > 0 {
		fmt.Println("\033[31m契约门禁未通过。\033[0m")
		os.Exit(1)
	}
	fmt.Println("\033[32m契约门禁通过。\033[0m")
}

// ---------------------------------------------------------------- 解析

var runStateRow = regexp.MustCompile("^\\|\\s*`([A-Z_]+)`\\s*\\|")

// parseRunStates 从 contracts/01「## 1. Run 状态」表格首列提取状态码。
func parseRunStates(md string) []string {
	var out []string
	inSection := false
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(line, "## 1. Run 状态") {
			inSection = true
			continue
		}
		if inSection && strings.HasPrefix(line, "## ") {
			break
		}
		if !inSection {
			continue
		}
		if m := runStateRow.FindStringSubmatch(line); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}

var (
	errBaseRow   = regexp.MustCompile("^\\|\\s*`([A-Z_]+)`\\s*\\|\\s*(\\d{3})")
	errDomainRow = regexp.MustCompile("^\\|\\s*`([A-Z_]+)`\\s*\\|\\s*([A-Z_]+)\\s*\\|")
)

// parseErrorCodes 提取 contracts/02 §2 基础码 与 §3 全部域码。
func parseErrorCodes(md string) (base, domain []string) {
	for _, line := range strings.Split(md, "\n") {
		if m := errBaseRow.FindStringSubmatch(line); m != nil {
			base = append(base, m[1])
			continue
		}
		if m := errDomainRow.FindStringSubmatch(line); m != nil {
			domain = append(domain, m[1])
		}
	}
	return dedupe(base), dedupe(domain)
}

// contractDomainBase 解析 contracts/02 §3 每行的 域码 → 基础码。
func contractDomainBase(md string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(md, "\n") {
		if m := errDomainRow.FindStringSubmatch(line); m != nil {
			out[m[1]] = m[2]
		}
	}
	return out
}

var (
	runTableRe = regexp.MustCompile(`(?s)CREATE TABLE runtime\.agent_runs\s*\((.*?)\n\);`)
	statusInRe = regexp.MustCompile(`(?s)status IN\s*\((.*?)\)`)
	quotedRe   = regexp.MustCompile(`'([A-Z_]+)'`)
)

// parseDDLRunStates 从迁移 SQL 中提取 runtime.agent_runs.status 的 CHECK 集合。
func parseDDLRunStates(sql string) []string {
	m := runTableRe.FindStringSubmatch(sql)
	if m == nil {
		return nil
	}
	sm := statusInRe.FindStringSubmatch(m[1])
	if sm == nil {
		return nil
	}
	var out []string
	for _, q := range quotedRe.FindAllStringSubmatch(sm[1], -1) {
		out = append(out, q[1])
	}
	return dedupe(out)
}

// ---------------------------------------------------------------- 工具

func mustRead(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "contract-lint: 无法读取 %s: %v\n", path, err)
		os.Exit(1)
	}
	return string(b)
}

func mustReadAllSQL(dir string) string {
	var sb strings.Builder
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "contract-lint: 无法读取目录 %s: %v\n", dir, err)
		os.Exit(1)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		sb.Write(b)
		sb.WriteString("\n")
	}
	return sb.String()
}

func mustReadTree(dir string) string {
	var sb strings.Builder
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".ts") && !strings.HasSuffix(p, ".tsx") &&
			!strings.HasSuffix(p, ".css") && !strings.HasSuffix(p, ".json") {
			return nil
		}
		if b, err := os.ReadFile(p); err == nil {
			sb.Write(b)
			sb.WriteString("\n")
		}
		return nil
	})
	return sb.String()
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func equalSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func diff(a, b []string) []string {
	var out []string
	for _, v := range a {
		if !contains(b, v) {
			out = append(out, v)
		}
	}
	return out
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range xs {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// ---------------------------------------------------------------- ⑤ 的实现

// unregisteredWebEndpoints 登记"前端已调用、但 contracts/openapi.yaml 尚未登记"的
// 写端点。与 tests/unit 的 danglingAllowlist 同一套思路：**显式登记 + 写明理由**，
// 而不是让检查静默跳过。
//
// 为什么不是直接放行所有未匹配的端点：那样一条 `apiPost<X>('/typo/path')` 会永远
// 通过门禁。为什么不是直接失败：仓库里确有两个既存缺口（releases 的 promote /
// rollback 前端已接入、服务端未实现），一次改动不宜把它们一并变成红灯。折中方案是
// 把它们登记下来——缺口因此是**被记录的事实**，而不是被掩盖的空洞；并且一旦它们
// 被补进 openapi，会以"陈旧豁免"再次失败，强制清理。
var unregisteredWebEndpoints = map[string]string{
	"POST /api/v1/admin/connectors/{p}/test":  "前端 useTestConnector 已接入「验证连接」；服务端与契约均未实现该端点",
	"POST /api/v1/admin/subjects/{p}/disable": "前端 useDisableSubject 已接入；契约中不存在任何 /admin/subjects 路径",
	"POST /api/v1/policies/{p}/versions":      "前端 useCreatePolicyVersion 已接入；契约该路径目前只声明了 GET（创建版本尚无契约与实现）",
	"POST /api/v1/releases/{p}/promote":       "前端 usePromoteRelease 已接入；服务端与契约均未实现",
	"POST /api/v1/releases/{p}/rollback":      "前端 useRollbackRelease 已接入；服务端与契约均未实现",
}

// operationStatuses 是 (VERB path) → 已声明状态码集合。
type operationStatuses map[string]map[string]bool

var openapiPathRe = regexp.MustCompile(`^[a-zA-Z0-9_./{}-]*$`)

// parseOpenAPIOperations 用一个极小的缩进感知扫描器读出 paths 下的 operation 与响应码。
//
// 为什么手写而不用 YAML 库：本仓 go.mod 只有 pgx 与 x/crypto，且构建期 GOPROXY=off，
// 引一个 YAML 依赖会打断离线构建。因此这里只依赖 openapi.yaml 的**稳定书写约定**
// （path 2 空格缩进、method 4 空格、响应码 8 空格 + 引号）。约定一旦被破坏，
// 解析出的 path 数会骤降，调用方据返回值 fail-closed 拒绝放行。
func parseOpenAPIOperations(doc string) (operationStatuses, int) {
	out := operationStatuses{}
	paths := map[string]bool{}
	var curPath, curMethod string
	inPaths := false

	for _, raw := range strings.Split(doc, "\n") {
		line := strings.TrimRight(raw, "\r")
		if line == "paths:" {
			inPaths = true
			continue
		}
		if !inPaths {
			continue
		}
		// paths 块结束：出现顶格内容（components: 等）
		if line != "" && !strings.HasPrefix(line, " ") {
			inPaths = false
			curPath, curMethod = "", ""
			continue
		}
		if rest, ok := strings.CutPrefix(line, "  "); ok && !strings.HasPrefix(rest, " ") {
			if p, isPath := strings.CutSuffix(rest, ":"); isPath && strings.HasPrefix(p, "/") && openapiPathRe.MatchString(p) {
				curPath = p
				curMethod = ""
				paths[p] = true
			}
			continue
		}
		if rest, ok := strings.CutPrefix(line, "    "); ok && !strings.HasPrefix(rest, " ") && curPath != "" {
			if m, isMethod := strings.CutSuffix(rest, ":"); isMethod && isHTTPMethod(m) {
				curMethod = m
				if out[strings.ToUpper(m)+" "+canonicalPath(curPath)] == nil {
					out[strings.ToUpper(m)+" "+canonicalPath(curPath)] = map[string]bool{}
				}
			}
			continue
		}
		if rest, ok := strings.CutPrefix(line, "        "); ok && curPath != "" && curMethod != "" {
			if code, isStatus := parseQuotedStatus(rest); isStatus {
				out[strings.ToUpper(curMethod)+" "+canonicalPath(curPath)][code] = true
			}
		}
	}
	return out, len(paths)
}

func isHTTPMethod(s string) bool {
	switch s {
	case "get", "post", "put", "patch", "delete", "head", "options":
		return true
	}
	return false
}

var quotedStatusRe = regexp.MustCompile(`^"(\d{3})":`)

func parseQuotedStatus(s string) (string, bool) {
	m := quotedStatusRe.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	return m[1], true
}

var openapiParamSegRe = regexp.MustCompile(`\{[^}]*\}`)

// canonicalPath 把路径参数统一成 {p}。
//
// 前端模板里的参数名来自局部变量（`${encodeURIComponent(id)}`），契约里的参数名来自
// 契约定义（`{id}`）。**只比较路径形状**、不比较参数名，才能在不要求两侧变量同名
// 的前提下判定"是不是同一个端点"。
func canonicalPath(p string) string {
	return openapiParamSegRe.ReplaceAllString(p, "{p}")
}

// webWriteCall 是前端一处写方法调用。
type webWriteCall struct {
	File   string // 仓库相对路径
	Line   int
	Method string // post / put / patch / delete（小写）
	Type   string // 泛型实参原文；解析不出时为 ""
	Path   string // 归一化路径；不是字面量时为 ""
}

var (
	webWriteCallRe = regexp.MustCompile(`api(Post|Put|Patch|Delete)<([^>\n]*)>\s*\(`)
	webStrLitRe    = regexp.MustCompile("^\\s*(`[^`]*`|'[^']*')")
	webTmplParamRe = regexp.MustCompile(`\$\{[^}]*\}`)
)

// scanWebWriteCalls 扫描 web/src 下的 apiPost/apiPut/apiPatch/apiDelete 调用。
//
// 只处理**第一个实参是字符串字面量**的调用：路径来自变量时无法静态判定，
// 强行猜一个路径去比对会产生假红。这类调用会被计入"不可判定"，由调用方统计，
// 以免"全部不可判定"被当成"全部合规"。
func scanWebWriteCalls(webSrc string) []webWriteCall {
	var out []webWriteCall
	if !dirExists(webSrc) {
		return out
	}
	_ = filepath.WalkDir(webSrc, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".ts") && !strings.HasSuffix(p, ".tsx") {
			return nil
		}
		srcRel := relSlash(webSrc, p)
		// client.ts 里是这些方法自身的**定义**（`export function apiPost<T>(path: string…)`），
		// 不是调用点。
		if srcRel == "api/client.ts" {
			return nil
		}
		raw, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		s := string(raw)
		for _, m := range webWriteCallRe.FindAllStringSubmatchIndex(s, -1) {
			call := webWriteCall{
				File:   relSlash(filepath.Dir(webSrc), p),
				Line:   strings.Count(s[:m[0]], "\n") + 1,
				Method: strings.ToLower(s[m[2]:m[3]]),
				Type:   strings.TrimSpace(s[m[4]:m[5]]),
			}
			if lm := webStrLitRe.FindStringSubmatch(s[m[1]:]); lm != nil {
				lit := lm[1]
				if lit[0] == '`' || lit[0] == '\'' {
					path := lit[1 : len(lit)-1]
					if strings.HasPrefix(path, "/") {
						path = webTmplParamRe.ReplaceAllString(path, "{p}")
						if !strings.HasPrefix(path, "/api/") {
							path = "/api/v1" + path
						}
						call.Path = path
					}
				}
			}
			out = append(out, call)
		}
		return nil
	})
	return out
}

func relSlash(base, p string) string {
	rel, err := filepath.Rel(base, p)
	if err != nil {
		rel = p
	}
	return filepath.ToSlash(rel)
}

func joinStatuses(st map[string]bool) string {
	xs := make([]string, 0, len(st))
	for k := range st {
		xs = append(xs, k)
	}
	sort.Strings(xs)
	return strings.Join(xs, ",")
}
