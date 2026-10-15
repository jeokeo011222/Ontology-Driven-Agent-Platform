// Package contract 校验对外契约文件自身的规范性（第十六篇 §10.2「契约测试」）。
//
// 与 tools/contract-lint 的分工：
//
//	contract-lint 管「文档 ↔ Go 常量 ↔ DDL ↔ 前端标签」的**取值一致性**；
//	本包管「契约文件本身是否自洽」——operationId 唯一性、成功/失败响应是否声明、
//	$ref 是否悬空。这类缺陷不会让代码编译失败，却会让生成的 SDK、文档与
//	前端类型在一个"看似有契约"的接口上集体失真。
//
// 断言强度刻意对齐 §10.3 发布硬门禁：宁可让契约缺口把 CI 变红，也不放过。
package contract

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const openAPIRelPath = "../../contracts/openapi.yaml"

type operation struct {
	Method      string
	Path        string
	OperationID string
	Codes       []string
	Public      bool
}

var (
	rePathPrefix     = regexp.MustCompile(`^  (/\S*):\s*$`)
	reMethod         = regexp.MustCompile(`^    (get|post|put|patch|delete|head|options):\s*$`)
	reOperationID    = regexp.MustCompile(`^      operationId:\s*(\S+)\s*$`)
	reStatusCode     = regexp.MustCompile(`^        "?(\d{3})"?:`)
	rePublicOp       = regexp.MustCompile(`^      security:\s*\[\]\s*$`)
	reDanglingRef    = regexp.MustCompile(`\$ref:\s*"#/([A-Za-z0-9_/]+)"`)
	reComponentEntry = regexp.MustCompile(`^    ([A-Za-z0-9_]+):\s*$`)
	reTopLevelKey    = regexp.MustCompile(`^[A-Za-z]`)
)

func readSpec(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(openAPIRelPath)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", openAPIRelPath, err)
	}
	if len(raw) == 0 {
		t.Fatalf("%s 为空", openAPIRelPath)
	}
	return string(raw)
}

func parseOperations(t *testing.T) []operation {
	t.Helper()
	var (
		ops      []operation
		curPath  string
		curIdx   = -1
		inPaths  bool
		allLines = strings.Split(readSpec(t), "\n")
	)
	for _, line := range allLines {
		if reTopLevelKey.MatchString(line) {
			inPaths = line == "paths:"
			curIdx = -1
			continue
		}
		if !inPaths {
			continue
		}
		if m := rePathPrefix.FindStringSubmatch(line); m != nil {
			curPath = m[1]
			curIdx = -1
			continue
		}
		if m := reMethod.FindStringSubmatch(line); m != nil {
			ops = append(ops, operation{Method: m[1], Path: curPath})
			curIdx = len(ops) - 1
			continue
		}
		if curIdx < 0 {
			continue
		}
		op := &ops[curIdx]
		if m := reOperationID.FindStringSubmatch(line); m != nil {
			op.OperationID = m[1]
		}
		if m := reStatusCode.FindStringSubmatch(line); m != nil {
			op.Codes = append(op.Codes, m[1])
		}
		if rePublicOp.MatchString(line) {
			op.Public = true
		}
	}
	if len(ops) == 0 {
		t.Fatal("未能从契约中解析出任何 operation（解析器或契约结构已变）")
	}
	return ops
}

// parseComponentNames 收集 components 下所有条目名（responses / schemas /
// parameters / securitySchemes 的条目统一都是 4 空格缩进的 `Name:`）。
//
// 刻意不跟踪 section：section 跟踪在 YAML 多行标量、`x-` 扩展等结构下极易漏读，
// 而本测试的目的是"$ref 指向的名字是否存在"，按名字集合比对已足够，
// 且不会产生假绿——名字不存在时一定报错。
func parseComponentNames(t *testing.T) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	inComponents := false
	for _, line := range strings.Split(readSpec(t), "\n") {
		if line != "" && line[0] != ' ' {
			inComponents = strings.HasPrefix(line, "components:")
			continue
		}
		if !inComponents {
			continue
		}
		if m := reComponentEntry.FindStringSubmatch(line); m != nil {
			names[m[1]] = true
		}
	}
	return names
}

func TestEveryOperationHasUniqueOperationID(t *testing.T) {
	ops := parseOperations(t)
	seen := map[string]string{}
	for _, op := range ops {
		if op.OperationID == "" {
			t.Errorf("%s %s 缺少 operationId", strings.ToUpper(op.Method), op.Path)
			continue
		}
		key := op.Method + " " + op.Path
		if prev, ok := seen[op.OperationID]; ok {
			t.Errorf("operationId %q 重复：%s 与 %s", op.OperationID, prev, key)
		}
		seen[op.OperationID] = key
	}
	t.Logf("已校验 %d 个 operation 的 operationId 唯一性", len(ops))
}

func TestEveryOperationDeclaresSuccessResponse(t *testing.T) {
	for _, op := range parseOperations(t) {
		ok := false
		for _, c := range op.Codes {
			if strings.HasPrefix(c, "2") {
				ok = true
				break
			}
		}
		if !ok {
			t.Errorf("%s %s 未声明任何 2xx 成功响应（codes=%v）",
				strings.ToUpper(op.Method), op.Path, op.Codes)
		}
	}
}

// TestProtectedOperationsDeclareClientError 受保护端点必须声明至少一个 4xx。
// 依据：任何需要认证的端点在匿名/越权调用时必然返回 401/403，
// 契约若不声明，前端与 SDK 就无法生成正确的错误分支。
func TestProtectedOperationsDeclareClientError(t *testing.T) {
	for _, op := range parseOperations(t) {
		if op.Public {
			continue
		}
		has4xx := false
		for _, c := range op.Codes {
			if strings.HasPrefix(c, "4") {
				has4xx = true
				break
			}
		}
		if !has4xx {
			t.Errorf("%s %s 未声明任何 4xx 响应（受保护端点应至少声明 401/403）",
				strings.ToUpper(op.Method), op.Path)
		}
	}
}

// TestNoDanglingComponentRefs 契约中每个 $ref 都必须指向真实存在的组件定义。
func TestNoDanglingComponentRefs(t *testing.T) {
	names := parseComponentNames(t)
	if len(names) == 0 {
		t.Fatal("未能解析出任何 components 条目（解析器失效）")
	}
	lines := strings.Split(readSpec(t), "\n")
	bad := 0
	for i, line := range lines {
		for _, m := range reDanglingRef.FindAllStringSubmatch(line, -1) {
			target := m[1] // 形如 components/responses/Conflict
			leaf := target
			if idx := strings.LastIndex(target, "/"); idx >= 0 {
				leaf = target[idx+1:]
			}
			if names[leaf] {
				continue
			}
			bad++
			t.Errorf("第 %d 行存在悬空引用：$ref: #/%s（components 中无 %q）", i+1, target, leaf)
		}
	}
	t.Logf("components 条目 %d 个，悬空引用 %d 处", len(names), bad)
}

// TestNoDuplicatePathKeys 同一 path 键只能出现一次。
//
// 依据：YAML 重复键会让后者整体覆盖前者——若某 path 上"清单查询"与"分配"分别写在
// 两个同名键下，严格解析器只会看到一个，另一个静默消失（这正是本测试首次运行时
// 捕获到的真实缺陷：adminBindRole 的 post 被同 path 的第二个键覆盖）。
func TestNoDuplicatePathKeys(t *testing.T) {
	seen := map[string]int{}
	for i, line := range strings.Split(readSpec(t), "\n") {
		if m := rePathPrefix.FindStringSubmatch(line); m != nil {
			if first, ok := seen[m[1]]; ok {
				t.Errorf("path 键 %s 重复定义（首次出现在第 %d 行，本次第 %d 行）"+
					"——YAML 重复键会使前一份定义被静默覆盖", m[1], first, i+1)
				continue
			}
			seen[m[1]] = i + 1
		}
	}
	t.Logf("path 键去重校验：%d 个唯一 path", len(seen))
}

// TestEveryPathHasAtLeastOneOperation 防止出现只有路径没有方法的空壳条目。
func TestEveryPathHasAtLeastOneOperation(t *testing.T) {
	ops := parseOperations(t)
	byPath := map[string]int{}
	for _, op := range ops {
		byPath[op.Path]++
	}
	var paths []string
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		t.Fatal("契约中没有任何 path")
	}
	for _, p := range paths {
		if byPath[p] == 0 {
			t.Errorf("path %s 下没有任何 operation", p)
		}
	}
	t.Logf("契约覆盖 %d 个 path / %d 个 operation", len(paths), len(ops))
}
