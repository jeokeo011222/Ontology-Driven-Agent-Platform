package reasoning

// 前向链引擎：半朴素（semi-naive）求值至不动点。
//
// 为什么用半朴素而不是朴素：朴素法每轮对**全部事实**重放全部规则，复杂度是
// O(迭代数 × 规则数 × |事实|^最大前提数)，在物化类规则（如 sameAs 对称）上
// 大量算力花在重推已知事实上。半朴素法每轮只把**新增 delta 事实**与规则前提
// 做 join（delta 至少匹配一条前提），其余前提对全量事实求值——产出与朴素法
// 等价，且天然满足"只对匹配 rule premises 的三元组求值"的要求。

import (
	"context"

	"odap/internal/ontology/graphstore"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// DefaultMaxIterations 是前向链迭代上限的默认值。
const DefaultMaxIterations = 100

// factSet 是图事实的内存表示（去重 + 谓词索引）。
type factSet struct {
	all    []graphstore.Triple
	byKey  map[string]graphstore.Triple
	byPred map[string][]graphstore.Triple // 谓词词法 → 三元组（谓词常量的前提走此索引）
}

func newFactSet(triples []graphstore.Triple) *factSet {
	fs := &factSet{
		byKey:  make(map[string]graphstore.Triple, len(triples)),
		byPred: make(map[string][]graphstore.Triple),
	}
	for _, t := range triples {
		fs.add(t)
	}
	return fs
}

// add 加入事实；已存在时返回 false（不去重会导致迭代不收敛）。
func (fs *factSet) add(t graphstore.Triple) bool {
	key := tripleKeyOf(t)
	if _, ok := fs.byKey[key]; ok {
		return false
	}
	fs.byKey[key] = t
	fs.all = append(fs.all, t)
	fs.byPred[t.P.Lexical] = append(fs.byPred[t.P.Lexical], t)
	return true
}

// candidates 返回模式可能匹配的三元组（谓词为常量时走索引，否则全量扫描）。
func (fs *factSet) candidates(p TriplePattern) []graphstore.Triple {
	if !isVariable(p.Predicate) {
		if bucket, ok := fs.byPred[p.Predicate]; ok {
			return bucket
		}
		return nil
	}
	return fs.all
}

// loadFacts 从图存储加载命名图全部事实。
func loadFacts(ctx context.Context, st graphstore.GraphStore, tenantID ids.ID, graphIRI string) ([]graphstore.Triple, error) {
	if st == nil {
		return nil, errors.E(errors.CodeInternal, "推理引擎：GraphStore 未装配")
	}
	triples, err := st.Match(ctx, tenantID, graphstore.Pattern{GraphIRI: graphIRI})
	if err != nil {
		return nil, err
	}
	if triples == nil {
		triples = []graphstore.Triple{}
	}
	return triples, nil
}

// ForwardResult 是前向链/增量推理的执行结果。
type ForwardResult struct {
	// Derived 是本次推理**新发现**的三元组（FactKind 置为 DERIVED，尚未写回）。
	Derived []graphstore.Triple
	// Iterations 是实际执行的迭代轮数。
	Iterations int
	// DurationMS 是 Service 层记录的端到端耗时（毫秒；引擎内部为 0）。
	DurationMS int64
}

// runForwardChain 以 seed 为初始 delta 做半朴素求值至不动点。
//
// seed 为 nil 表示全量物化（初始 delta = 现有全部事实）；非 nil 表示增量
// 场景（初始 delta = 新增事实）。产出的派生三元组只标记不写回，写回由
// 调用方（Service）以 FactKind=DERIVED 统一执行，便于聚合记账与失败回滚。
//
// 迭代超过 maxIterations 仍未收敛时显式报错，绝不静默截断——半截的物化
// 结果会让依赖它的校验/查询得出看似一致实则不完整的结论。
func runForwardChain(rules []Rule, facts []graphstore.Triple, seed []graphstore.Triple, maxIterations int) (*ForwardResult, error) {
	for i := range rules {
		if err := ValidateRule(rules[i]); err != nil {
			return nil, err
		}
	}
	if maxIterations <= 0 {
		maxIterations = DefaultMaxIterations
	}
	fs := newFactSet(facts)

	delta := seed
	if delta == nil {
		delta = facts
	}
	res := &ForwardResult{}

	for iter := 1; iter <= maxIterations; iter++ {
		news := evalDelta(rules, delta, fs)
		fresh := make([]graphstore.Triple, 0, len(news))
		for _, t := range news {
			if fs.add(t) {
				t.FactKind = graphstore.FactKindDerived
				fresh = append(fresh, t)
			}
		}
		if len(fresh) == 0 {
			res.Iterations = iter - 1
			return res, nil
		}
		res.Derived = append(res.Derived, fresh...)
		delta = fresh
	}
	// 达到上限仍有新事实：显式失败（绝不静默截断）。
	return nil, errors.Ef(errors.CodeResourceExhausted,
		"推理未收敛：前向链执行 %d 轮迭代后仍有新事实产出（max_iterations=%d）。"+
			"请检查规则集是否存在非单调/自我增殖结构，或调大 max_iterations", maxIterations, maxIterations)
}

// evalDelta 对一轮 delta 求值全部规则，返回本轮候选新事实（未去重过滤）。
//
// 求值方式：对规则的第 i 条前提与 delta 做 join（保证 delta 参与匹配），
// 其余前提对全量事实集求完整解。
func evalDelta(rules []Rule, delta []graphstore.Triple, fs *factSet) []graphstore.Triple {
	out := make([]graphstore.Triple, 0)
	if len(delta) == 0 {
		return out
	}
	for _, rule := range rules {
		if !rule.Enabled || len(rule.Premises) == 0 {
			continue
		}
		for i := range rule.Premises {
			for _, dt := range delta {
				b, ok := matchPattern(rule.Premises[i], dt, bindings{})
				if !ok {
					continue
				}
				for _, sol := range completePremises(rule.Premises, i, fs, b) {
					for _, c := range rule.Conclusion {
						t, ok := patternToTriple(c, sol)
						if !ok {
							continue // 结论含未绑定变量（注册校验应已拒绝，防御性跳过）
						}
						out = append(out, t)
					}
				}
			}
		}
	}
	return out
}

// completePremises 在跳过第 skip 条前提（已由 delta 匹配）的情况下，
// 对其余前提在事实集上求全部完整解。
func completePremises(premises []TriplePattern, skip int, fs *factSet, b bindings) []bindings {
	// 构造剩余前提的求值序列。
	rest := make([]TriplePattern, 0, len(premises)-1)
	for i, p := range premises {
		if i != skip {
			rest = append(rest, p)
		}
	}
	return matchSequence(rest, fs, b)
}

// matchSequence 依序对模式序列求全部解（标准 join 链）。
func matchSequence(patterns []TriplePattern, fs *factSet, b bindings) []bindings {
	if len(patterns) == 0 {
		return []bindings{b}
	}
	head, tail := patterns[0], patterns[1:]
	out := make([]bindings, 0)
	for _, t := range fs.candidates(head) {
		nb, ok := matchPattern(head, t, b)
		if !ok {
			continue
		}
		out = append(out, matchSequence(tail, fs, nb)...)
	}
	return out
}

// RunForward 是前向链的可测入口：对命名图全量物化并返回派生结果（不写回）。
func RunForward(rules []Rule, facts []graphstore.Triple, maxIterations int) (*ForwardResult, error) {
	return runForwardChain(rules, facts, nil, maxIterations)
}
