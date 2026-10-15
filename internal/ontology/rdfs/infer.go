// Package rdfs 实现 ODAP 的 RDFS 子集物化推理（第十三篇 §8/§12/§14.1）。
//
// # 已实现规则清单（逐条锚定 W3C RDF Semantics 官方 entailment rules 编号）
//
//	rdfs2   (?p rdfs:domain ?c) ∧ (?x ?p ?y) → (?x rdf:type ?c)
//	rdfs3   (?p rdfs:range ?c)  ∧ (?x ?p ?y) → (?y rdf:type ?c)        （?y 非字面值）
//	rdfs4a  (?x ?p ?y)                        → (?x rdf:type rdfs:Resource)
//	rdfs4b  (?x ?p ?y)                        → (?y rdf:type rdfs:Resource)（?y 非字面值）
//	rdfs5   (?p rdfs:subPropertyOf ?q) ∧ (?q rdfs:subPropertyOf ?r) → (?p rdfs:subPropertyOf ?r)
//	rdfs6   (?p rdf:type rdf:Property)        → (?p rdfs:subPropertyOf ?p)
//	rdfs7   (?p rdfs:subPropertyOf ?q) ∧ (?x ?p ?y) → (?x ?q ?y)
//	rdfs8   (?c rdf:type rdfs:Class)          → (?c rdfs:subClassOf rdfs:Resource)
//	rdfs9   (?u rdfs:subClassOf ?v) ∧ (?x rdf:type ?u) → (?x rdf:type ?v)
//	rdfs10  (?c rdf:type rdfs:Class)          → (?c rdfs:subClassOf ?c)（自反）
//	rdfs11  (?u rdfs:subClassOf ?v) ∧ (?v rdfs:subClassOf ?w) → (?u rdfs:subClassOf ?w)
//	rdfs12  (?p rdf:type rdfs:ContainerMembershipProperty) → (?p rdfs:subPropertyOf rdfs:member)
//	rdfs13  (?c rdf:type rdfs:Datatype)       → (?c rdfs:subClassOf rdfs:Literal)
//
// # 未实现规则导致的漏推导（假阴性）风险
//
// 本引擎**不是**完整 RDFS/OWL 推理器。以下语义未实现：rdfs1（plain literal 类型化）、
// rdfs14（对 rdfs:Resource 的实例化）、以及 owl:sameAs / owl:equivalentClass /
// owl:equivalentProperty / owl:inverseOf / owl:TransitiveProperty / owl:SymmetricProperty /
// owl:FunctionalProperty 与 OWL 2 RL 其余规则（第十三篇 §14.1）。
//
// **未实现的规则不是语义中立的**：当某个结论客观可由本体推出、但其推导链依赖未实现的规则时，
// 本引擎会**漏推导（假阴性）**——即"查不到"并不等于"事实为假"（第十三篇 §14.1 假阴性风险提示、
// §14.2 开放世界假设）。因此：
//
//  1. 本体包必须声明其依赖的规则集合；依赖未实现规则时应由本体负责人显式确认。
//  2. 查询结果的"未找到"必须结合开放世界语义解释，不得表现为"事实为假"。
//  3. 能力边界应通过运行时能力清单对外暴露，供上层 Agent 与治理工作台显式展示。
package rdfs

import (
	"context"
	"strconv"
	"time"

	"odap/internal/ontology/graphstore"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// RDFS/RDF 词汇表 IRI 常量。
const (
	RDFType                         = "http://www.w3.org/1999/02/22-rdf-syntax-ns#type"
	RDFProperty                     = "http://www.w3.org/1999/02/22-rdf-syntax-ns#Property"
	RDFSSubClassOf                  = "http://www.w3.org/2000/01/rdf-schema#subClassOf"
	RDFSSubPropertyOf               = "http://www.w3.org/2000/01/rdf-schema#subPropertyOf"
	RDFSDomain                      = "http://www.w3.org/2000/01/rdf-schema#domain"
	RDFSRange                       = "http://www.w3.org/2000/01/rdf-schema#range"
	RDFSResource                    = "http://www.w3.org/2000/01/rdf-schema#Resource"
	RDFSClass                       = "http://www.w3.org/2000/01/rdf-schema#Class"
	RDFSDatatype                    = "http://www.w3.org/2000/01/rdf-schema#Datatype"
	RDFSLiteral                     = "http://www.w3.org/2000/01/rdf-schema#Literal"
	RDFSContainerMembershipProperty = "http://www.w3.org/2000/01/rdf-schema#ContainerMembershipProperty"
	RDFSMember                      = "http://www.w3.org/2000/01/rdf-schema#member"
)

// RuleNames 是本引擎已实现规则编号的完整清单（锚定 W3C 规则编号）。
var RuleNames = []string{
	"rdfs2", "rdfs3", "rdfs4a", "rdfs4b", "rdfs5", "rdfs6", "rdfs7",
	"rdfs8", "rdfs9", "rdfs10", "rdfs11", "rdfs12", "rdfs13",
}

// 默认预算上限，防止 OWL 级三元组爆炸（第十三篇 §12.2、§21.2）。
const (
	DefaultMaxIterations = 16
	DefaultMaxInferred   = 100000
)

// 停止原因。
const (
	StopFixpoint      = "FIXPOINT"
	StopMaxIterations = "MAX_ITERATIONS"
	StopMaxInferred   = "MAX_INFERRED"
)

var (
	termType          = iri(RDFType)
	termSubClassOf    = iri(RDFSSubClassOf)
	termSubPropertyOf = iri(RDFSSubPropertyOf)
	termResource      = iri(RDFSResource)
	termMember        = iri(RDFSMember)
	termLiteral       = iri(RDFSLiteral)
)

// Options 控制推理的规则集合与资源预算。
type Options struct {
	// MaxIterations 是半朴素迭代的步数上限（≤0 时用 DefaultMaxIterations）。
	MaxIterations int
	// MaxInferred 是允许新增的派生三元组上限（≤0 时用 DefaultMaxInferred）。
	MaxInferred int
	// EnabledRules 显式指定启用的规则编号；为空表示启用全部已实现规则。
	// 非空时，仅值为 true 的规则生效，未列出的规则视为关闭。
	EnabledRules map[string]bool
}

// Stats 是一次推理的结果统计。
type Stats struct {
	Iterations   int            // 实际迭代步数
	Inferred     int            // 新增派生三元组数
	Materialized int            // 实际写入存储的派生三元组数
	ByRule       map[string]int // 各规则贡献的新增三元组数
	Completed    bool           // 是否达到不动点
	Truncated    bool           // 是否因预算上限提前停止
	StopReason   string         // FIXPOINT / MAX_ITERATIONS / MAX_INFERRED
}

// Infer 在给定命名图上执行 RDFS 子集物化推理，并把派生三元组写回存储。
// 这是不接收 context 的便捷入口；需要取消或超时控制时请使用 InferContext。
func Infer(store graphstore.GraphStore, tenantID ids.ID, graphIRI string, opts Options) (Stats, error) {
	return InferContext(context.Background(), store, tenantID, graphIRI, opts)
}

// InferContext 与 Infer 相同，但支持 context 取消。
func InferContext(ctx context.Context, store graphstore.GraphStore, tenantID ids.ID, graphIRI string, opts Options) (Stats, error) {
	if store == nil {
		return Stats{}, errors.E(errors.CodeInternal, "rdfs: GraphStore 不能为空")
	}
	if tenantID.IsZero() {
		return Stats{}, errors.E(errors.CodeValidationFailed, "rdfs: tenant_id 不能为空")
	}
	if graphIRI == "" {
		return Stats{}, errors.E(errors.CodeValidationFailed, "rdfs: graph_iri 不能为空")
	}
	opts, err := normalizeOptions(opts)
	if err != nil {
		return Stats{}, err
	}
	rules, err := enableRules(opts.EnabledRules)
	if err != nil {
		return Stats{}, err
	}

	base, err := store.Match(ctx, tenantID, graphstore.Pattern{GraphIRI: graphIRI})
	if err != nil {
		return Stats{}, err
	}

	e := newEngine()
	delta := make([]fact, 0, len(base))
	for _, t := range base {
		f := fact{s: t.S, p: t.P, o: t.O}
		if e.addRaw(f) {
			delta = append(delta, f)
		}
	}

	stats := Stats{ByRule: make(map[string]int)}
	inferred := make([]fact, 0)
	for iter := 1; iter <= opts.MaxIterations; iter++ {
		stats.Iterations = iter
		produced := e.step(delta, rules, &stats)
		if len(produced) == 0 {
			stats.Completed = true
			stats.StopReason = StopFixpoint
			break
		}
		remaining := opts.MaxInferred - len(inferred)
		if len(produced) >= remaining {
			inferred = append(inferred, produced[:remaining]...)
			stats.Truncated = true
			stats.StopReason = StopMaxInferred
			break
		}
		inferred = append(inferred, produced...)
		delta = produced
		if iter == opts.MaxIterations {
			stats.StopReason = StopMaxIterations
		}
	}
	if !stats.Completed && stats.StopReason == "" {
		stats.StopReason = StopMaxIterations
	}

	stats.Inferred = len(inferred)
	if len(inferred) > 0 {
		now := time.Now().UTC()
		triples := make([]graphstore.Triple, 0, len(inferred))
		for _, f := range inferred {
			triples = append(triples, graphstore.Triple{
				S:          f.s,
				P:          f.p,
				O:          f.o,
				GraphIRI:   graphIRI,
				FactKind:   graphstore.FactKindDerived,
				ObservedAt: now,
			})
		}
		n, err := store.InsertTriples(ctx, tenantID, graphIRI, triples)
		if err != nil {
			return stats, err
		}
		stats.Materialized = n
	}
	return stats, nil
}

func normalizeOptions(opts Options) (Options, error) {
	if opts.MaxIterations < 0 {
		return opts, errors.E(errors.CodeValidationFailed, "rdfs: MaxIterations 不能为负")
	}
	if opts.MaxInferred < 0 {
		return opts, errors.E(errors.CodeValidationFailed, "rdfs: MaxInferred 不能为负")
	}
	if opts.MaxIterations == 0 {
		opts.MaxIterations = DefaultMaxIterations
	}
	if opts.MaxInferred == 0 {
		opts.MaxInferred = DefaultMaxInferred
	}
	return opts, nil
}

func enableRules(m map[string]bool) (map[string]bool, error) {
	out := make(map[string]bool, len(RuleNames))
	if len(m) == 0 {
		for _, r := range RuleNames {
			out[r] = true
		}
		return out, nil
	}
	known := make(map[string]bool, len(RuleNames))
	for _, r := range RuleNames {
		known[r] = true
	}
	for r, on := range m {
		if !known[r] {
			return nil, errors.Ef(errors.CodeOntologyValidationFailed,
				"rdfs: 未知的规则编号 %q（未登记的规则不会被静默忽略）", r)
		}
		out[r] = on
	}
	return out, nil
}

// fact 是引擎内部的 (s, p, o) 三元组。
type fact struct {
	s, p, o graphstore.Term
}

func (f fact) key() string {
	return termKey(f.s) + "\x1f" + termKey(f.p) + "\x1f" + termKey(f.o)
}

func termKey(t graphstore.Term) string {
	return strconv.FormatUint(uint64(t.Kind), 10) + "\x1f" + t.Lexical + "\x1f" + t.Datatype + "\x1f" + t.Lang
}

// termSet 是按 termKey 去重的词项集合。
type termSet map[string]graphstore.Term

// engine 维护全量事实与各类索引，供半朴素迭代使用。
type engine struct {
	all  map[string]fact
	byPr map[string][]fact // predicate termKey -> facts

	subClassOf    map[string]termSet // u -> {v}
	subClassOfRev map[string]termSet // v -> {u}
	subProp       map[string]termSet // p -> {q}
	subPropRev    map[string]termSet // q -> {p}
	domain        map[string]termSet // p -> {c}
	rng           map[string]termSet // p -> {c}
	types         map[string]termSet // x -> {c}
	typesRev      map[string]termSet // c -> {x}
}

func newEngine() *engine {
	return &engine{
		all:           make(map[string]fact),
		byPr:          make(map[string][]fact),
		subClassOf:    make(map[string]termSet),
		subClassOfRev: make(map[string]termSet),
		subProp:       make(map[string]termSet),
		subPropRev:    make(map[string]termSet),
		domain:        make(map[string]termSet),
		rng:           make(map[string]termSet),
		types:         make(map[string]termSet),
		typesRev:      make(map[string]termSet),
	}
}

func setAdd(m map[string]termSet, key string, t graphstore.Term) {
	s := m[key]
	if s == nil {
		s = termSet{}
		m[key] = s
	}
	s[termKey(t)] = t
}

// addRaw 把事实纳入全量集合与索引；已存在则返回 false。
func (e *engine) addRaw(f fact) bool {
	if f.s.Kind == graphstore.KindLiteral {
		return false // 字面量不能作主语（非法 RDF）
	}
	k := f.key()
	if _, ok := e.all[k]; ok {
		return false
	}
	e.all[k] = f
	e.byPr[termKey(f.p)] = append(e.byPr[termKey(f.p)], f)

	if f.p.Kind == graphstore.KindIRI {
		switch f.p.Lexical {
		case RDFSSubClassOf:
			if f.o.Kind != graphstore.KindLiteral {
				setAdd(e.subClassOf, termKey(f.s), f.o)
				setAdd(e.subClassOfRev, termKey(f.o), f.s)
			}
		case RDFSSubPropertyOf:
			if f.o.Kind != graphstore.KindLiteral {
				setAdd(e.subProp, termKey(f.s), f.o)
				setAdd(e.subPropRev, termKey(f.o), f.s)
			}
		case RDFSDomain:
			if f.o.Kind != graphstore.KindLiteral {
				setAdd(e.domain, termKey(f.s), f.o)
			}
		case RDFSRange:
			if f.o.Kind != graphstore.KindLiteral {
				setAdd(e.rng, termKey(f.s), f.o)
			}
		case RDFType:
			if f.o.Kind == graphstore.KindIRI {
				setAdd(e.types, termKey(f.s), f.o)
				setAdd(e.typesRev, termKey(f.o), f.s)
			}
		}
	}
	return true
}

// step 执行一轮半朴素推理：规则体至少有一个原子取自 delta，其余取自全量集合。
// 返回本轮新推导出的事实（同时已并入全量集合与索引）。
func (e *engine) step(delta []fact, rules map[string]bool, stats *Stats) []fact {
	produced := make([]fact, 0)
	seen := make(map[string]bool)
	emit := func(rule string, f fact) {
		if f.s.Kind == graphstore.KindLiteral {
			return
		}
		k := f.key()
		if seen[k] {
			return
		}
		seen[k] = true
		if !e.addRaw(f) {
			return
		}
		produced = append(produced, f)
		stats.ByRule[rule]++
	}

	if rules["rdfs2"] {
		for _, f := range delta {
			if isIRI(f.p, RDFSDomain) {
				for _, g := range e.byPr[termKey(f.s)] {
					emit("rdfs2", fact{s: g.s, p: termType, o: f.o})
				}
			}
			for _, c := range e.domain[termKey(f.p)] {
				emit("rdfs2", fact{s: f.s, p: termType, o: c})
			}
		}
	}
	if rules["rdfs3"] {
		for _, f := range delta {
			if isIRI(f.p, RDFSRange) {
				for _, g := range e.byPr[termKey(f.s)] {
					if g.o.Kind != graphstore.KindLiteral {
						emit("rdfs3", fact{s: g.o, p: termType, o: f.o})
					}
				}
			}
			if f.o.Kind != graphstore.KindLiteral {
				for _, c := range e.rng[termKey(f.p)] {
					emit("rdfs3", fact{s: f.o, p: termType, o: c})
				}
			}
		}
	}
	if rules["rdfs4a"] {
		for _, f := range delta {
			emit("rdfs4a", fact{s: f.s, p: termType, o: termResource})
		}
	}
	if rules["rdfs4b"] {
		for _, f := range delta {
			if f.o.Kind != graphstore.KindLiteral {
				emit("rdfs4b", fact{s: f.o, p: termType, o: termResource})
			}
		}
	}
	if rules["rdfs5"] {
		for _, f := range delta {
			if !isIRI(f.p, RDFSSubPropertyOf) {
				continue
			}
			p, q := f.s, f.o
			for _, r := range e.subProp[termKey(q)] {
				emit("rdfs5", fact{s: p, p: termSubPropertyOf, o: r})
			}
			for _, sub := range e.subPropRev[termKey(p)] {
				emit("rdfs5", fact{s: sub, p: termSubPropertyOf, o: q})
			}
		}
	}
	if rules["rdfs6"] {
		for _, f := range delta {
			if isIRI(f.p, RDFType) && isIRI(f.o, RDFProperty) {
				emit("rdfs6", fact{s: f.s, p: termSubPropertyOf, o: f.s})
			}
		}
	}
	if rules["rdfs7"] {
		for _, f := range delta {
			if isIRI(f.p, RDFSSubPropertyOf) {
				for _, g := range e.byPr[termKey(f.s)] {
					emit("rdfs7", fact{s: g.s, p: f.o, o: g.o})
				}
			}
			for _, q := range e.subProp[termKey(f.p)] {
				emit("rdfs7", fact{s: f.s, p: q, o: f.o})
			}
		}
	}
	if rules["rdfs8"] {
		for _, f := range delta {
			if isIRI(f.p, RDFType) && isIRI(f.o, RDFSClass) {
				emit("rdfs8", fact{s: f.s, p: termSubClassOf, o: termResource})
			}
		}
	}
	if rules["rdfs9"] {
		for _, f := range delta {
			if isIRI(f.p, RDFSSubClassOf) {
				for _, x := range e.typesRev[termKey(f.s)] {
					emit("rdfs9", fact{s: x, p: termType, o: f.o})
				}
			}
			if isIRI(f.p, RDFType) {
				for _, v := range e.subClassOf[termKey(f.o)] {
					emit("rdfs9", fact{s: f.s, p: termType, o: v})
				}
			}
		}
	}
	if rules["rdfs10"] {
		for _, f := range delta {
			if isIRI(f.p, RDFType) && isIRI(f.o, RDFSClass) {
				emit("rdfs10", fact{s: f.s, p: termSubClassOf, o: f.s})
			}
		}
	}
	if rules["rdfs11"] {
		for _, f := range delta {
			if !isIRI(f.p, RDFSSubClassOf) {
				continue
			}
			u, v := f.s, f.o
			for _, w := range e.subClassOf[termKey(v)] {
				emit("rdfs11", fact{s: u, p: termSubClassOf, o: w})
			}
			for _, sub := range e.subClassOfRev[termKey(u)] {
				emit("rdfs11", fact{s: sub, p: termSubClassOf, o: v})
			}
		}
	}
	if rules["rdfs12"] {
		for _, f := range delta {
			if isIRI(f.p, RDFType) && isIRI(f.o, RDFSContainerMembershipProperty) {
				emit("rdfs12", fact{s: f.s, p: termSubPropertyOf, o: termMember})
			}
		}
	}
	if rules["rdfs13"] {
		for _, f := range delta {
			if isIRI(f.p, RDFType) && isIRI(f.o, RDFSDatatype) {
				emit("rdfs13", fact{s: f.s, p: termSubClassOf, o: termLiteral})
			}
		}
	}
	return produced
}

func iri(lexical string) graphstore.Term {
	return graphstore.Term{Kind: graphstore.KindIRI, Lexical: lexical}
}

func isIRI(t graphstore.Term, lexical string) bool {
	return t.Kind == graphstore.KindIRI && t.Lexical == lexical
}
