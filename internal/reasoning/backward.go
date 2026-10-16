package reasoning

// 后向链引擎：目标驱动的 SLD 求证。
//
// 求证策略：目标先对**基础事实**匹配（直接得证），再对每条启用规则的每个
// 结论模式做统一（unify），递归证明其全部前提。环检测：同一目标键在当前
// 证明栈中已出现即剪枝——递归规则（如 sameAs 传递）在环状图上不设剪枝
// 将无限递归。

import (
	"odap/internal/ontology/graphstore"
	"odap/internal/platform/errors"
)

// errProofSearchExhausted 是证明搜索预算耗尽（步数/深度）时返回的错误。
var errProofSearchExhausted = errors.E(errors.CodeResourceExhausted,
	"后向链证明搜索超限：规则应用步数或递归深度达到上限，无法给出完整结论")

// ProofStep 是证明路径上的一步规则应用（前提与结论均已实例化）。
type ProofStep struct {
	RuleKey    string
	Premises   []TriplePattern
	Conclusion graphstore.Triple
}

// solution 是一次成功的求证：目标变量绑定 + 规则应用轨迹。
type solution struct {
	binding bindings
	trace   []ProofStep
}

// BackwardResult 是后向链的执行结果。
type BackwardResult struct {
	Proved     bool
	ProofTrace []ProofStep         // 首个成功证明的规则应用序列
	Bindings   []map[string]string // 目标变量的全部解（词法形式）
}

// backwardEngine 承载一次后向链求证的可变状态。
type backwardEngine struct {
	fs        *factSet
	rules     []Rule
	stack     map[string]bool // 目标键 → 是否在当前证明栈中（环剪枝）
	stepQuota int             // 规则应用步数预算（防组合爆炸）
	exhausted bool            // 步数预算耗尽标记（超限显式报错，绝不静默给出不完整结论）
}

// maxProofSteps 是单次求证的规则应用步数上限。开放目标（含变量）经规则链
// 展开的状态空间随词表常量数增长，10 万步在常规本体规模下足够给出完整解，
// 同时为病态规则集保留明确的失败出口。
const maxProofSteps = 100000

// maxProofDepth 是证明递归深度上限（环剪枝之外的第二道防线）。
const maxProofDepth = 128

// RunBackward 对目标 pattern 求证；目标各位置可为变量以获取绑定解。
func RunBackward(rules []Rule, facts []graphstore.Triple, goal TriplePattern) (*BackwardResult, error) {
	for i := range rules {
		if err := ValidateRule(rules[i]); err != nil {
			return nil, err
		}
	}
	eng := &backwardEngine{
		fs:        newFactSet(facts),
		rules:     rules,
		stack:     make(map[string]bool),
		stepQuota: maxProofSteps,
	}
	sols := eng.prove(goal, bindings{}, 0)
	if eng.exhausted {
		return nil, errProofSearchExhausted
	}

	res := &BackwardResult{Proved: len(sols) > 0}
	goalVars := namedVariables(goal)
	seen := make(map[string]bool)
	for _, s := range sols {
		// 首个成功解的轨迹作为证明路径。
		if res.ProofTrace == nil && len(s.trace) > 0 {
			res.ProofTrace = s.trace
		}
		// 目标变量绑定（无变量的目标得证时给一个空绑定占位）。
		vals := make(map[string]string, len(goalVars))
		for _, v := range goalVars {
			if t, ok := s.binding[v]; ok {
				vals[v] = termToLexical(t)
			}
		}
		key := bindingKey(vals)
		if !seen[key] {
			seen[key] = true
			res.Bindings = append(res.Bindings, vals)
		}
	}
	return res, nil
}

// namedVariables 返回模式中出现的具名变量（去重，按出现序）。
func namedVariables(p TriplePattern) []string {
	seen := make(map[string]bool)
	out := make([]string, 0)
	for _, lex := range []string{p.Subject, p.Predicate, p.Object} {
		if isNamedVariable(lex) {
			name := varName(lex)
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
}

// bindingKey 为解去重生成键。
func bindingKey(m map[string]string) string {
	s := make([]byte, 0, 64)
	for _, k := range sortedKeys(m) {
		s = append(s, k...)
		s = append(s, '=')
		s = append(s, m[k]...)
		s = append(s, 0x1f)
	}
	return string(s)
}

// goalKey 计算目标的**规范化键**（供证明栈环检测）：
// 变量按出现序重命名为 #0/#1/…（已绑定变量解析为词项词法，常量原样，
// 匿名变量为通配占位）。规范化是必须的：不同规则的前提变量名各不相同，
// 语义相同的开放目标（如 "?x ⊑ ?y" 与 "?a ⊑ ?b"）若按原始名比较将永远
// 不相等，环剪枝失效导致递归不终止。
func goalKey(p TriplePattern, u *unifier) string {
	ord := map[string]string{}
	next := 0
	canonical := func(lex string) string {
		if isNamedVariable(lex) {
			if _, bound := u.lookupTerm(varName(lex)); bound {
				// 已绑定变量以其词项词法参与键——否则「(B⊑Other)」与
				// 全开放目标「(?a⊑?b)」会得到同一键，环剪枝失效。
				return u.instantiateLexical(lex)
			}
			name := u.resolveVarName(lex) // 未绑定变量按根变量出现序编号
			if tag, ok := ord[name]; ok {
				return tag
			}
			tag := "#" + itoa(next)
			next++
			ord[name] = tag
			return tag
		}
		return u.instantiateLexical(lex) // 常量原样；匿名变量为通配
	}
	return canonical(p.Subject) + "\x1f" + canonical(p.Predicate) + "\x1f" + canonical(p.Object)
}

// itoa 是无依赖的小整数转字符串（键构造专用）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// prove 对目标求证，返回全部成功解（环检测作用于当前证明栈）。
func (eng *backwardEngine) prove(goal TriplePattern, base bindings, depth int) []solution {
	if depth > maxProofDepth {
		eng.exhausted = true
		return nil
	}
	u := newUnifier(base)
	key := goalKey(goal, u)
	if eng.stack[key] {
		return nil // 环剪枝：同一目标已在当前证明栈中
	}
	eng.stack[key] = true
	defer delete(eng.stack, key)

	out := make([]solution, 0)

	// 1. 基础事实：目标直接匹配图事实即得证。
	for _, t := range eng.fs.candidates(goal) {
		ub := u.clone()
		if !ub.matchTriple(goal, t) {
			continue
		}
		out = append(out, solution{binding: ub.flatten()})
	}

	// 2. 规则展开：每个结论模式与目标统一后递归证明全部前提。
	for _, rule := range eng.rules {
		if !rule.Enabled {
			continue
		}
		for ci := range rule.Conclusion {
			sub := eng.proveViaRule(rule, ci, goal, u, depth+1)
			out = append(out, sub...)
		}
	}
	return out
}

// proveViaRule 经由 rule 的第 ci 条结论模式证明 goal。
func (eng *backwardEngine) proveViaRule(rule Rule, ci int, goal TriplePattern, base *unifier, depth int) []solution {
	if eng.stepQuota <= 0 {
		eng.exhausted = true
		return nil
	}
	eng.stepQuota--

	u := base.clone()
	// 结论模式与目标统一（两侧均可能含变量，含变量-变量统一）。
	if !u.unifyPatterns(rule.Conclusion[ci], goal) {
		return nil
	}
	// 递归证明全部前提（依序链接绑定）。
	sols := eng.proveSeq(rule.Premises, u, depth)
	out := make([]solution, 0, len(sols))
	for _, s := range sols {
		// 实例化本条结论与全部前提作为轨迹（前提在 s.binding 下应全部可绑定）。
		step := ProofStep{RuleKey: rule.RuleKey}
		for _, p := range rule.Premises {
			if t, ok := patternToTriple(p, s.binding); ok {
				step.Premises = append(step.Premises, TriplePattern{
					Subject: termToLexical(t.S), Predicate: termToLexical(t.P), Object: termToLexical(t.O),
				})
			}
		}
		if ct, ok := patternToTriple(rule.Conclusion[ci], s.binding); ok {
			step.Conclusion = ct
		}
		trace := append(append([]ProofStep(nil), s.trace...), step)
		out = append(out, solution{binding: s.binding, trace: trace})
	}
	return out
}

// proveSeq 依序证明模式序列，链式传递绑定。
func (eng *backwardEngine) proveSeq(patterns []TriplePattern, u *unifier, depth int) []solution {
	if len(patterns) == 0 {
		return []solution{{binding: u.flatten()}}
	}
	head, tail := patterns[0], patterns[1:]
	out := make([]solution, 0)
	for _, s := range eng.prove(head, u.flatten(), depth) {
		tailBase := newUnifier(s.binding)
		for _, t := range eng.proveSeq(tail, tailBase, depth) {
			merged := solution{
				binding: t.binding,
				trace:   append(append([]ProofStep(nil), s.trace...), t.trace...),
			}
			out = append(out, merged)
		}
	}
	return out
}

// sortedKeys 返回 map 键的排序副本（为去重键提供确定性）。
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// 插入排序（绑定数量极小，避免引入 sort 依赖）。
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
