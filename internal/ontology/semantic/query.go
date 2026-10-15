// Package semantic 实现 ODAP 的受控语义查询 DSL（第十三篇 §9）。
//
// 安全边界（§9.1、§8.2）：**不允许任意 SPARQL / SQL 字符串拼接**。查询被表达为受控的
// 结构化 Clause 列表，经合法性校验（变量命名、常量 IRI、Select 白名单、Limit 上限）后
// 编译为一系列 GraphStore.Match 调用并按变量做嵌套连接；所有常量都作为参数传给 GraphStore，
// 不参与任何字符串拼接，因而不存在注入面。
package semantic

import (
	"context"
	"regexp"
	"sort"
	"strings"

	"odap/internal/ontology/graphstore"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// 默认查询预算。
const (
	DefaultLimit           = 100
	DefaultMaxLimit        = 1000
	DefaultMaxIntermediate = 100000
)

var varNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Clause 是一个三元组模式：字段为空串表示未绑定；以 "?" 前缀表示变量；否则为常量 IRI。
type Clause struct {
	Subject   string
	Predicate string
	Object    string
}

// Query 是一条受控语义查询。
type Query struct {
	GraphIRI string
	Where    []Clause
	Select   []string // 变量名（可带或不带 "?" 前缀）；为空时投影 Where 中出现的全部变量
	Limit    int
}

// Row 是一行查询结果（变量名 -> 词项绑定）。
type Row map[string]graphstore.Term

// Result 是查询结果。
type Result struct {
	Rows      []Row
	Truncated bool
}

// Service 是语义查询服务。
type Service struct {
	Store           graphstore.GraphStore
	MaxLimit        int // ≤0 时用 DefaultMaxLimit
	MaxIntermediate int // ≤0 时用 DefaultMaxIntermediate
}

type compiledClause struct {
	sVar, pVar, oVar       string
	sConst, pConst, oConst *graphstore.Term
}

// Execute 执行受控语义查询。
func (s *Service) Execute(ctx context.Context, tenantID ids.ID, q Query) (Result, error) {
	if s == nil || s.Store == nil {
		return Result{}, errors.E(errors.CodeInternal, "semantic: Store 不能为空")
	}
	if tenantID.IsZero() {
		return Result{}, errors.E(errors.CodeValidationFailed, "semantic: tenant_id 不能为空")
	}
	maxLimit := s.MaxLimit
	if maxLimit <= 0 {
		maxLimit = DefaultMaxLimit
	}
	maxIntermediate := s.MaxIntermediate
	if maxIntermediate <= 0 {
		maxIntermediate = DefaultMaxIntermediate
	}

	clauses, selectVars, limit, err := compileQuery(q, maxLimit)
	if err != nil {
		return Result{}, err
	}

	// 嵌套连接：逐条连接 Clause，已绑定的变量作为常量代入下一轮匹配。
	bindings := []map[string]graphstore.Term{{}}
	for _, cc := range clauses {
		next := make([]map[string]graphstore.Term, 0)
		for _, b := range bindings {
			pat := graphstore.Pattern{
				GraphIRI: q.GraphIRI,
				S:        resolve(cc.sConst, cc.sVar, b),
				P:        resolve(cc.pConst, cc.pVar, b),
				O:        resolve(cc.oConst, cc.oVar, b),
			}
			triples, err := s.Store.Match(ctx, tenantID, pat)
			if err != nil {
				return Result{}, err
			}
			for _, t := range triples {
				nb := cloneBinding(b)
				if cc.sVar != "" {
					nb[cc.sVar] = t.S
				}
				if cc.pVar != "" {
					nb[cc.pVar] = t.P
				}
				if cc.oVar != "" {
					nb[cc.oVar] = t.O
				}
				next = append(next, nb)
				if len(next) > maxIntermediate {
					return Result{}, errors.Ef(errors.CodeSemanticQueryInvalid,
						"查询中间结果超过上限 %d，请收紧查询条件", maxIntermediate)
				}
			}
		}
		bindings = next
		if len(bindings) == 0 {
			break
		}
	}

	rows := make([]Row, 0)
	seen := make(map[string]bool)
	truncated := false
	for _, b := range bindings {
		row := make(Row, len(selectVars))
		parts := make([]string, 0, len(selectVars))
		complete := true
		for _, v := range selectVars {
			val, ok := b[v]
			if !ok {
				complete = false
				break
			}
			row[v] = val
			parts = append(parts, termKey(val))
		}
		if !complete {
			continue
		}
		key := strings.Join(parts, "\x1f")
		if seen[key] {
			continue
		}
		seen[key] = true
		if len(rows) >= limit {
			truncated = true
			break
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rowKey(rows[i]) < rowKey(rows[j]) })

	return Result{Rows: rows, Truncated: truncated}, nil
}

// compileQuery 校验并把 Query 编译为可执行的 Clause 列表。
func compileQuery(q Query, maxLimit int) ([]compiledClause, []string, int, error) {
	if strings.TrimSpace(q.GraphIRI) == "" {
		return nil, nil, 0, errors.E(errors.CodeSemanticQueryInvalid, "semantic: GraphIRI 不能为空")
	}
	if !validIRI(q.GraphIRI) {
		return nil, nil, 0, errors.Ef(errors.CodeSemanticQueryInvalid, "semantic: GraphIRI 不是合法 IRI: %q", q.GraphIRI)
	}
	if len(q.Where) == 0 {
		return nil, nil, 0, errors.E(errors.CodeSemanticQueryInvalid, "semantic: Where 至少需要一个 Clause")
	}

	clauses := make([]compiledClause, 0, len(q.Where))
	varOrder := make([]string, 0)
	varSet := make(map[string]bool)
	note := func(name string) {
		if name == "" || varSet[name] {
			return
		}
		varSet[name] = true
		varOrder = append(varOrder, name)
	}

	for i, c := range q.Where {
		var cc compiledClause
		var err error
		cc.sVar, cc.sConst, err = parseField(c.Subject)
		if err != nil {
			return nil, nil, 0, wrapField(i, "Subject", err)
		}
		cc.pVar, cc.pConst, err = parseField(c.Predicate)
		if err != nil {
			return nil, nil, 0, wrapField(i, "Predicate", err)
		}
		cc.oVar, cc.oConst, err = parseField(c.Object)
		if err != nil {
			return nil, nil, 0, wrapField(i, "Object", err)
		}
		if cc.sVar == "" && cc.sConst == nil && cc.pVar == "" && cc.pConst == nil && cc.oVar == "" && cc.oConst == nil {
			return nil, nil, 0, errors.Ef(errors.CodeSemanticQueryInvalid, "semantic: 第 %d 个 Clause 三个位置全为空", i)
		}
		note(cc.sVar)
		note(cc.pVar)
		note(cc.oVar)
		clauses = append(clauses, cc)
	}

	selectVars := make([]string, 0)
	selectSeen := make(map[string]bool)
	if len(q.Select) == 0 {
		selectVars = append(selectVars, varOrder...)
	} else {
		for _, sv := range q.Select {
			name := strings.TrimPrefix(sv, "?")
			if !varNamePattern.MatchString(name) {
				return nil, nil, 0, errors.Ef(errors.CodeSemanticQueryInvalid, "semantic: 非法的 Select 字段 %q", sv)
			}
			if !varSet[name] {
				return nil, nil, 0, errors.Ef(errors.CodeSemanticQueryInvalid, "semantic: Select 引用了未在 Where 中出现的变量 ?%s", name)
			}
			if selectSeen[name] {
				continue
			}
			selectSeen[name] = true
			selectVars = append(selectVars, name)
		}
	}

	limit := q.Limit
	if limit == 0 {
		limit = DefaultLimit
	}
	if limit < 0 {
		return nil, nil, 0, errors.E(errors.CodeSemanticQueryInvalid, "semantic: Limit 不能为负")
	}
	if limit > maxLimit {
		return nil, nil, 0, errors.Ef(errors.CodeSemanticQueryInvalid,
			"semantic: Limit=%d 超过上限 %d", limit, maxLimit)
	}
	return clauses, selectVars, limit, nil
}

// parseField 解析 Clause 的单个位置。
// 返回：变量名（无变量为空）、常量词项（非常量为 nil）、错误。
func parseField(s string) (string, *graphstore.Term, error) {
	if s == "" {
		return "", nil, nil
	}
	if strings.HasPrefix(s, "?") {
		name := s[1:]
		if !varNamePattern.MatchString(name) {
			return "", nil, errors.Ef(errors.CodeSemanticQueryInvalid, "非法变量名 %q", s)
		}
		return name, nil, nil
	}
	if !validIRI(s) {
		return "", nil, errors.Ef(errors.CodeSemanticQueryInvalid, "常量不是合法 IRI: %q", s)
	}
	return "", &graphstore.Term{Kind: graphstore.KindIRI, Lexical: s}, nil
}

func wrapField(idx int, field string, err error) error {
	return errors.Ef(errors.CodeSemanticQueryInvalid, "semantic: 第 %d 个 Clause 的 %s 非法: %s", idx, field, errors.As(err).Message)
}

// validIRI 做轻量 IRI 校验：非空、无空白字符，且含 scheme 冒号。
func validIRI(s string) bool {
	if s == "" || strings.ContainsAny(s, " \t\n\r") {
		return false
	}
	if strings.Contains(s, "://") {
		return true
	}
	i := strings.IndexByte(s, ':')
	return i > 0
}

func resolve(c *graphstore.Term, varName string, b map[string]graphstore.Term) *graphstore.Term {
	if c != nil {
		return c
	}
	if varName == "" {
		return nil
	}
	if v, ok := b[varName]; ok {
		return &v
	}
	return nil
}

func cloneBinding(b map[string]graphstore.Term) map[string]graphstore.Term {
	nb := make(map[string]graphstore.Term, len(b)+1)
	for k, v := range b {
		nb[k] = v
	}
	return nb
}

func rowKey(r Row) string {
	keys := make([]string, 0, len(r))
	for k := range r {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteByte(0x1f)
		sb.WriteString(termKey(r[k]))
		sb.WriteByte(0x1e)
	}
	return sb.String()
}

func termKey(t graphstore.Term) string {
	return string(rune(t.Kind)) + "\x1f" + t.Lexical + "\x1f" + t.Datatype + "\x1f" + t.Lang
}
