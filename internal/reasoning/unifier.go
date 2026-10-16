package reasoning

// unify：后向链的变量统一器（并查集 + 根变量词项表）。
//
// 为什么需要它而不是复用 matchPattern：matchPattern 只能"模式对事实"，
// 而后向链需要"结论模式对目标模式"的统一——两侧都可能有未绑定变量
// （如结论 ?y sameAs ?x 对目标 ?g sameAs z），此时必须把 ?g 与 ?y **互为别名**
// 地链接起来，任意一侧后续被绑定时另一侧同步获得绑定。matchPattern 的
// bindings 无法表达变量-变量链接，故用并查集承载。

import "odap/internal/ontology/graphstore"

type unifier struct {
	// parent 是变量并查集的父指针（缺省或自指表示自己是根）。
	parent map[string]string
	// terms 是根变量上的词项绑定（仅根变量存词项）。
	terms map[string]graphstore.Term
}

func newUnifier(base bindings) *unifier {
	u := &unifier{parent: make(map[string]string, len(base)+2), terms: make(map[string]graphstore.Term, len(base)+2)}
	for k, v := range base {
		u.parent[k] = k
		u.terms[k] = v
	}
	return u
}

// clone 复制统一器（选择点分支前必须复制，避免污染兄弟分支）。
func (u *unifier) clone() *unifier {
	nu := &unifier{
		parent: make(map[string]string, len(u.parent)+2),
		terms:  make(map[string]graphstore.Term, len(u.terms)+2),
	}
	for k, v := range u.parent {
		nu.parent[k] = v
	}
	for k, v := range u.terms {
		nu.terms[k] = v
	}
	return nu
}

// root 返回变量的并查集根（路径未做压缩，规模小无需优化）。
func (u *unifier) root(v string) string {
	for {
		p, ok := u.parent[v]
		if !ok || p == v {
			return v
		}
		v = p
	}
}

// link 把两个变量链接为同一等价类（词项待后续绑定）。
func (u *unifier) link(a, b string) {
	ra, rb := u.root(a), u.root(b)
	if ra == rb {
		return
	}
	u.parent[rb] = ra
}

// bind 把词项绑定到变量所在等价类的根上。
// 同时把变量登记进 parent（保证 flatten/遍历可见——词项表里的键是根名，
// 未登记的变量会在投影时被静默丢掉）。
func (u *unifier) bind(v string, t graphstore.Term) {
	if _, ok := u.parent[v]; !ok {
		u.parent[v] = v
	}
	u.terms[u.root(v)] = t
}

// lookupTerm 返回变量当前绑定（经根解析）。
func (u *unifier) lookupTerm(v string) (graphstore.Term, bool) {
	t, ok := u.terms[u.root(v)]
	return t, ok
}

// resolveVarName 返回词法位置的具名变量经别名链解析后的根变量名
// （goalKey 规范化用：别名两侧的变量必须归并为同一个规范名）。
func (u *unifier) resolveVarName(lex string) string {
	return u.root(varName(lex))
}

// unifyPatterns 逐位置统一两个三元组模式。
func (u *unifier) unifyPatterns(a, b TriplePattern) bool {
	return u.unifyPos(a.Subject, b.Subject) &&
		u.unifyPos(a.Predicate, b.Predicate) &&
		u.unifyPos(a.Object, b.Object)
}

// unifyPos 统一单个词法位置（常量对常量判等；变量按等价类绑定/链接；
// 匿名变量为通配）。
func (u *unifier) unifyPos(a, b string) bool {
	aVar, bVar := isNamedVariable(a), isNamedVariable(b)
	switch {
	case !aVar && !bVar:
		ta, err := termFromLexical(a)
		if err != nil {
			return false
		}
		tb, err := termFromLexical(b)
		if err != nil {
			return false
		}
		return termEqual(ta, tb)
	case aVar && bVar:
		ra, rb := u.root(varName(a)), u.root(varName(b))
		ta, oka := u.terms[ra]
		tb, okb := u.terms[rb]
		switch {
		case oka && okb:
			return termEqual(ta, tb)
		case oka:
			u.link(varName(b), varName(a)) // rb → ra
			return true
		case okb:
			u.link(varName(a), varName(b)) // ra → rb
			return true
		default:
			u.link(varName(b), varName(a))
			return true
		}
	case aVar:
		t, err := termFromLexical(b)
		if err != nil {
			return false
		}
		if bound, ok := u.lookupTerm(varName(a)); ok {
			return termEqual(bound, t)
		}
		u.bind(varName(a), t)
		return true
	case bVar:
		t, err := termFromLexical(a)
		if err != nil {
			return false
		}
		if bound, ok := u.lookupTerm(varName(b)); ok {
			return termEqual(bound, t)
		}
		u.bind(varName(b), t)
		return true
	default:
		// 至少一侧是匿名变量：通配。
		return true
	}
}

// matchTriple 以统一器状态把模式匹配到事实三元组（未绑定变量就地绑定）。
func (u *unifier) matchTriple(p TriplePattern, t graphstore.Triple) bool {
	return u.matchPos(p.Subject, t.S) &&
		u.matchPos(p.Predicate, t.P) &&
		u.matchPos(p.Object, t.O)
}

func (u *unifier) matchPos(lex string, term graphstore.Term) bool {
	if !isNamedVariable(lex) {
		if isVariable(lex) {
			return true // 匿名变量：通配
		}
		c, err := termFromLexical(lex)
		if err != nil {
			return false
		}
		return termEqual(c, term)
	}
	if bound, ok := u.lookupTerm(varName(lex)); ok {
		return termEqual(bound, term)
	}
	u.bind(varName(lex), term)
	return true
}

// flatten 把统一器状态投影为普通绑定：**每个**变量都解析到其等价类根的
// 词项（而非只输出根变量）——否则经别名链接的目标变量会丢失绑定。
// 词项表以根名为键，单独遍历（变量可能未进 parent 却已有绑定）。
func (u *unifier) flatten() bindings {
	b := make(bindings, len(u.parent)+len(u.terms))
	for r, t := range u.terms {
		b[r] = t
	}
	for v := range u.parent {
		if t, ok := u.terms[u.root(v)]; ok {
			b[v] = t
		}
	}
	return b
}

// instantiateLexical 把词法位置解析为规范化键串（供证明栈环检测）：
// 已绑定变量解析为词项词法；未绑定变量按根名占位；匿名变量为通配占位。
func (u *unifier) instantiateLexical(lex string) string {
	if !isNamedVariable(lex) {
		if isVariable(lex) {
			return "*" // 匿名变量：通配（任何取值同键，环检测语义正确）
		}
		return lex
	}
	r := u.root(varName(lex))
	if t, ok := u.terms[r]; ok {
		return termToLexical(t)
	}
	return "?" + r
}
