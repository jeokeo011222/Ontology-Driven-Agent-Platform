package reasoning

// 一致性检查：SHACL 校验联动 + OWL 级别的图内自检。
//
// 当前检出三类问题：
//  1. SHACL   ——转发 internal/ontology/shacl 引擎（形状取自本体版本 manifest）；
//  2. SAMEAS_CYCLE ——owl:sameAs 把 ≥2 个不同节点闭合成环（等价类自相缠绕；
//     OWL 语义下 sameAs 环本身合法，但业务侧通常意味着建模错误，按 Warning 报告）；
//  3. DISJOINT_WITH ——本体声明了 (C1 owl:disjointWith C2) 而图中存在实例
//     同时类型化为 C1 与 C2（真 OWL 不一致，按 Violation 报告）。

import (
	"context"
	"sort"

	"odap/internal/ontology/graphstore"
	"odap/internal/ontology/shacl"
	"odap/internal/platform/ids"
)

// 一致性问题类别。
const (
	IssueSHACL        = "SHACL"
	IssueSameAsCycle  = "SAMEAS_CYCLE"
	IssueDisjointWith = "DISJOINT_WITH"
)

// SHACL 严重级别（与 shacl 包对齐）。
const (
	severityViolation = "Violation"
	severityWarning   = "Warning"
)

// ConsistencyIssue 是一条一致性问题。
type ConsistencyIssue struct {
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	Object  string `json:"object,omitempty"`
	Message string `json:"message"`
}

// ConsistencyReport 是一致性检查结果。
type ConsistencyReport struct {
	Conforms bool               `json:"conforms"`
	Issues   []ConsistencyIssue `json:"issues"`
}

// CheckConsistency 对命名图执行一致性检查（SHACL 联动 + sameAs 环 + 互补类共现）。
func (s *Service) CheckConsistency(ctx context.Context, tenantID ids.ID, graphIRI string) (*ConsistencyReport, error) {
	if err := validateTenantGraph(tenantID, graphIRI); err != nil {
		return nil, err
	}
	facts, err := loadFacts(ctx, s.Store, tenantID, graphIRI)
	if err != nil {
		return nil, err
	}
	report := &ConsistencyReport{Conforms: true, Issues: []ConsistencyIssue{}}

	add := func(issue ConsistencyIssue) {
		report.Issues = append(report.Issues, issue)
		if issue.Kind != IssueSHACL || issue.severity() == severityViolation {
			report.Conforms = false
		}
	}

	// 1. SHACL 联动（形状不可解析时降级为跳过，不吞掉其余检查）。
	shapes, err := s.loadShapes(ctx, tenantID, graphIRI)
	if err == nil {
		shaclReport, err := validateShapes(s.Store, tenantID, graphIRI, shapes)
		if err != nil {
			return nil, err
		}
		for _, r := range shaclReport.Results {
			add(ConsistencyIssue{
				Kind:    IssueSHACL,
				Subject: r.FocusNode,
				Object:  r.Path,
				Message: "[" + r.Severity + "] " + r.ShapeID + " / " + r.Component + ": " + r.Message,
			})
		}
	}

	// 2. owl:sameAs 环。
	for _, cycle := range sameAsCycles(facts) {
		add(ConsistencyIssue{
			Kind:    IssueSameAsCycle,
			Subject: cycle[0],
			Object:  cycle[len(cycle)-1],
			Message: "owl:sameAs 成环（等价类成员：" + joinLexicals(cycle) + "），请检查是否为建模错误",
		})
	}

	// 3. 互补类共现（仅当本体显式声明了 owl:disjointWith）。
	for _, v := range disjointViolations(facts) {
		add(ConsistencyIssue{
			Kind:    IssueDisjointWith,
			Subject: v.subject,
			Object:  v.object,
			Message: "实例同时类型化为互斥类 " + v.subject + " 与 " + v.object + "（本体已声明 owl:disjointWith）",
		})
	}
	return report, nil
}

// severity 从 SHACL 摘要消息中还原严重级别（消息以 [Severity] 前缀承载）。
// 这是 ConsistencyIssue 的私有辅助（公开结构不冗余存级别）。
func (i ConsistencyIssue) severity() string {
	if len(i.Message) > 0 && i.Message[0] == '[' {
		for j := 1; j < len(i.Message); j++ {
			if i.Message[j] == ']' {
				return i.Message[1:j]
			}
		}
	}
	return ""
}

// sameAsCycle 表示一个 sameAs 等价环（成员为词法 IRI，按序排列）。
type sameAsCycle = []string

// sameAsCycles 找出 sameAs 有向图中所有非平凡强连通分量（SCC，Tarjan）。
// 用 SCC 而非无向连通分量：单向链（e→d）不构成环，只有互相可达的节点
// 才是真正的等价环。
func sameAsCycles(facts []graphstore.Triple) []sameAsCycle {
	adj := make(map[string][]string)
	for _, t := range facts {
		if t.P.Lexical != IRIOWLSameAs {
			continue
		}
		if t.S.Kind != graphstore.KindIRI || t.O.Kind != graphstore.KindIRI {
			continue // 字面值/空白节点上的 sameAs 无等价语义，交由 SHACL/建模侧处理
		}
		s, o := t.S.Lexical, t.O.Lexical
		if s == o {
			continue // 自环（对称规则的正常产物）不构成问题
		}
		adj[s] = append(adj[s], o)
	}
	// Tarjan 迭代版（避免深图递归爆栈）。
	index, low := map[string]int{}, map[string]int{}
	onStack := map[string]bool{}
	var stack []string
	var out []sameAsCycle
	counter := 0

	var strongconnect func(v string)
	strongconnect = func(v string) {
		index[v], low[v] = counter, counter
		counter++
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range adj[v] {
			if _, seen := index[w]; !seen {
				strongconnect(w)
				if low[w] < low[v] {
					low[v] = low[w]
				}
			} else if onStack[w] && index[w] < low[v] {
				low[v] = index[w]
			}
		}
		if low[v] == index[v] {
			component := []string{}
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				component = append(component, w)
				if w == v {
					break
				}
			}
			if len(component) >= 2 {
				sort.Strings(component)
				out = append(out, component)
			}
		}
	}
	// 稳定遍历顺序（键排序），保证报告顺序确定。
	nodes := make([]string, 0, len(adj))
	for v := range adj {
		nodes = append(nodes, v)
	}
	sort.Strings(nodes)
	for _, v := range nodes {
		if _, seen := index[v]; !seen {
			strongconnect(v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

// disjointPair 是一次互补类共现违例。
type disjointPair struct {
	subject string // 违例实例
	object  string // 与之互斥的另一个类
}

// disjointViolations 检出互补类共现：本体声明 (C1 disjointWith C2) 且存在
// 实例同时类型化为 C1 与 C2。
func disjointViolations(facts []graphstore.Triple) []disjointPair {
	// 声明的互斥对（双向索引）。
	pairs := make(map[string]map[string]bool)
	for _, t := range facts {
		if t.P.Lexical != IRIOWLDisjointWith || t.S.Kind != graphstore.KindIRI || t.O.Kind != graphstore.KindIRI {
			continue
		}
		if pairs[t.S.Lexical] == nil {
			pairs[t.S.Lexical] = make(map[string]bool)
		}
		if pairs[t.O.Lexical] == nil {
			pairs[t.O.Lexical] = make(map[string]bool)
		}
		pairs[t.S.Lexical][t.O.Lexical] = true
		pairs[t.O.Lexical][t.S.Lexical] = true
	}
	if len(pairs) == 0 {
		return nil
	}
	// 实例的类型集合。
	types := make(map[string]map[string]bool)
	for _, t := range facts {
		if t.P.Lexical != IRIRDFType || t.S.Kind != graphstore.KindIRI || t.O.Kind != graphstore.KindIRI {
			continue
		}
		if types[t.S.Lexical] == nil {
			types[t.S.Lexical] = make(map[string]bool)
		}
		types[t.S.Lexical][t.O.Lexical] = true
	}
	out := make([]disjointPair, 0)
	for inst, cls := range types {
		for c1 := range cls {
			for c2 := range pairs[c1] {
				if c1 != c2 && cls[c2] {
					// 同一对只报一次（以实例 + 有序类对去重）。
					a, b := c1, c2
					if a > b {
						a, b = b, a
					}
					out = append(out, disjointPair{subject: inst + " ⊑ " + a, object: b})
				}
			}
		}
	}
	// 去重（实例×有序类对）。
	seen := make(map[string]bool)
	dedup := make([]disjointPair, 0, len(out))
	for _, v := range out {
		key := v.subject + "\x1f" + v.object
		if !seen[key] {
			seen[key] = true
			dedup = append(dedup, v)
		}
	}
	sort.Slice(dedup, func(i, j int) bool { return dedup[i].subject < dedup[j].subject })
	return dedup
}

// joinLexicals 把环成员拼为人读串。
func joinLexicals(items []string) string {
	out := ""
	for i, it := range items {
		if i > 0 {
			out += " ↔ "
		}
		out += it
	}
	return out
}

// validateShapes 转发 shacl.Validate（独立小函数，便于单测关注点分离）；
// validateTenantGraph 定义于 incremental.go。
func validateShapes(st graphstore.GraphStore, tenantID ids.ID, graphIRI string, shapes []shacl.Shape) (shacl.Report, error) {
	return shacl.Validate(st, tenantID, graphIRI, shapes)
}
