package reasoning

// 内置规则集（代码承载，非 DB）：RDFS 子集 + OWL 2 RL 核心子集。
//
// reason.rules 表中内置规则只存"启停开关行"（is_builtin=true、body 为空），
// 规则本体由本文件唯一承载——DB 与代码两份规则体会漂移，故不落库。
//
// 词表 IRI 与 graphload/shacl 内的同名词表常量保持同值（各包自持小词表，
// 该约束在 rules_builtin_test.go 中以断言固定，防止三处漂移）。

// RDF / RDFS / OWL 词表 IRI。
const (
	IRIRDFType     = "http://www.w3.org/1999/02/22-rdf-syntax-ns#type"
	IRIRDFProperty = "http://www.w3.org/1999/02/22-rdf-syntax-ns#Property"

	IRIRDFSSubClassOf                  = "http://www.w3.org/2000/01/rdf-schema#subClassOf"
	IRIRDFSSubPropertyOf               = "http://www.w3.org/2000/01/rdf-schema#subPropertyOf"
	IRIRDFSDomain                      = "http://www.w3.org/2000/01/rdf-schema#domain"
	IRIRDFSRange                       = "http://www.w3.org/2000/01/rdf-schema#range"
	IRIRDFSContainerMembershipProperty = "http://www.w3.org/2000/01/rdf-schema#ContainerMembershipProperty"

	IRIOWLInverseOf          = "http://www.w3.org/2002/07/owl#inverseOf"
	IRIOWLSymmetricProperty  = "http://www.w3.org/2002/07/owl#SymmetricProperty"
	IRIOWLTransitiveProperty = "http://www.w3.org/2002/07/owl#TransitiveProperty"
	IRIOWLSameAs             = "http://www.w3.org/2002/07/owl#sameAs"
	IRIOWLEquivalentClass    = "http://www.w3.org/2002/07/owl#equivalentClass"
	IRIOWLDisjointWith       = "http://www.w3.org/2002/07/owl#disjointWith"
)

// 规则 kind（与 reason.rules.kind CHECK 约束对齐）。
const (
	KindRDFS   = "RDFS"
	KindOWL2RL = "OWL2RL"
	KindCUSTOM = "CUSTOM"
)

// Rule 是一条推理规则：premises（合取）⇒ conclusion（同时实例化产出）。
type Rule struct {
	RuleKey     string
	Name        string
	Kind        string // KindRDFS / KindOWL2RL / KindCUSTOM
	Premises    []TriplePattern
	Conclusion  []TriplePattern
	Description string
	// Builtin 标记规则是否为代码内置（内置规则不可增删，仅可启停）。
	Builtin bool
	// Enabled 控制该规则是否参与推理。
	Enabled bool
}

// builtinRules 是内置规则唯一真值（只读；对外一律经 BuiltinRules 返回副本）。
var builtinRules = []Rule{
	// ---------------- RDFS 子集 ----------------
	{
		RuleKey: "rdfs-subclass-trans", Name: "subClassOf 传递性", Kind: KindRDFS,
		Premises: []TriplePattern{
			{Subject: "?a", Predicate: IRIRDFSSubClassOf, Object: "?b"},
			{Subject: "?b", Predicate: IRIRDFSSubClassOf, Object: "?c"},
		},
		Conclusion:  []TriplePattern{{Subject: "?a", Predicate: IRIRDFSSubClassOf, Object: "?c"}},
		Description: "若 a 是 b 的子类且 b 是 c 的子类，则 a 是 c 的子类（rdfs11）。",
	},
	{
		RuleKey: "rdfs-type-propagation", Name: "类型沿 subClassOf 提升", Kind: KindRDFS,
		Premises: []TriplePattern{
			{Subject: "?a", Predicate: IRIRDFSSubClassOf, Object: "?b"},
			{Subject: "?x", Predicate: IRIRDFType, Object: "?a"},
		},
		Conclusion:  []TriplePattern{{Subject: "?x", Predicate: IRIRDFType, Object: "?b"}},
		Description: "若 a 是 b 的子类且 x 是 a 的实例，则 x 也是 b 的实例（rdfs9）。",
	},
	{
		RuleKey: "rdfs-subprop-trans", Name: "subPropertyOf 传递性", Kind: KindRDFS,
		Premises: []TriplePattern{
			{Subject: "?p", Predicate: IRIRDFSSubPropertyOf, Object: "?q"},
			{Subject: "?q", Predicate: IRIRDFSSubPropertyOf, Object: "?r"},
		},
		Conclusion:  []TriplePattern{{Subject: "?p", Predicate: IRIRDFSSubPropertyOf, Object: "?r"}},
		Description: "属性蕴含关系的传递闭包（rdfs11 的属性版）。",
	},
	{
		RuleKey: "rdfs-subprop-value", Name: "取值沿 subPropertyOf 提升", Kind: KindRDFS,
		Premises: []TriplePattern{
			{Subject: "?p", Predicate: IRIRDFSSubPropertyOf, Object: "?q"},
			{Subject: "?x", Predicate: "?p", Object: "?y"},
		},
		Conclusion:  []TriplePattern{{Subject: "?x", Predicate: "?q", Object: "?y"}},
		Description: "若 p 是 q 的子属性且 x p y，则 x q y（rdfs7）。",
	},
	{
		RuleKey: "rdfs-domain", Name: "domain 推断类型", Kind: KindRDFS,
		Premises: []TriplePattern{
			{Subject: "?p", Predicate: IRIRDFSDomain, Object: "?c"},
			{Subject: "?x", Predicate: "?p", Object: "?y"},
		},
		Conclusion:  []TriplePattern{{Subject: "?x", Predicate: IRIRDFType, Object: "?c"}},
		Description: "若 p 的定义域是 c 且 x p y，则 x 是 c 的实例（rdfs2）。",
	},
	{
		RuleKey: "rdfs-range", Name: "range 推断类型", Kind: KindRDFS,
		Premises: []TriplePattern{
			{Subject: "?p", Predicate: IRIRDFSRange, Object: "?c"},
			{Subject: "?x", Predicate: "?p", Object: "?y"},
		},
		Conclusion:  []TriplePattern{{Subject: "?y", Predicate: IRIRDFType, Object: "?c"}},
		Description: "若 p 的值域是 c 且 x p y，则 y 是 c 的实例（rdfs3）。",
	},
	{
		RuleKey: "rdfs-container-membership", Name: "容器成员属性（简化）", Kind: KindRDFS,
		Premises: []TriplePattern{
			{Subject: "?p", Predicate: IRIRDFType, Object: IRIRDFSContainerMembershipProperty},
		},
		Conclusion:  []TriplePattern{{Subject: "?p", Predicate: IRIRDFType, Object: IRIRDFProperty}},
		Description: "容器成员属性（rdfs:member 的下标属性族）是属性的简化推断（rdfs 规范 ContainerMembershipProperty ⊑ rdf:Property 的硬编码展开）。",
	},

	// ---------------- OWL 2 RL 核心子集 ----------------
	{
		RuleKey: "owl-equivalent-class-fwd", Name: "等价类类型提升（正向）", Kind: KindOWL2RL,
		Premises: []TriplePattern{
			{Subject: "?a", Predicate: IRIOWLEquivalentClass, Object: "?b"},
			{Subject: "?x", Predicate: IRIRDFType, Object: "?a"},
		},
		Conclusion:  []TriplePattern{{Subject: "?x", Predicate: IRIRDFType, Object: "?b"}},
		Description: "若 a 与 b 等价且 x 是 a 的实例，则 x 也是 b 的实例（OWL 2 RL eq-ref/equivalent-class-as-subclass 的类型提升方向）。",
	},
	{
		RuleKey: "owl-equivalent-class-bwd", Name: "等价类类型提升（反向）", Kind: KindOWL2RL,
		Premises: []TriplePattern{
			{Subject: "?a", Predicate: IRIOWLEquivalentClass, Object: "?b"},
			{Subject: "?x", Predicate: IRIRDFType, Object: "?b"},
		},
		Conclusion:  []TriplePattern{{Subject: "?x", Predicate: IRIRDFType, Object: "?a"}},
		Description: "等价类类型提升的对称方向（与 owl-equivalent-class-fwd 合成双向提升）。",
	},
	{
		RuleKey: "owl-inverseof", Name: "逆属性推断", Kind: KindOWL2RL,
		Premises: []TriplePattern{
			{Subject: "?p", Predicate: IRIOWLInverseOf, Object: "?q"},
			{Subject: "?x", Predicate: "?p", Object: "?y"},
		},
		Conclusion:  []TriplePattern{{Subject: "?y", Predicate: "?q", Object: "?x"}},
		Description: "若 q 是 p 的逆属性且 x p y，则 y q x（OWL 2 RL prp-inv）。",
	},
	{
		RuleKey: "owl-symmetric", Name: "对称属性", Kind: KindOWL2RL,
		Premises: []TriplePattern{
			{Subject: "?p", Predicate: IRIRDFType, Object: IRIOWLSymmetricProperty},
			{Subject: "?x", Predicate: "?p", Object: "?y"},
		},
		Conclusion:  []TriplePattern{{Subject: "?y", Predicate: "?p", Object: "?x"}},
		Description: "若 p 是对称属性且 x p y，则 y p x（OWL 2 RL prp-symp）。",
	},
	{
		RuleKey: "owl-transitive", Name: "传递属性", Kind: KindOWL2RL,
		Premises: []TriplePattern{
			{Subject: "?p", Predicate: IRIRDFType, Object: IRIOWLTransitiveProperty},
			{Subject: "?x", Predicate: "?p", Object: "?y"},
			{Subject: "?y", Predicate: "?p", Object: "?z"},
		},
		Conclusion:  []TriplePattern{{Subject: "?x", Predicate: "?p", Object: "?z"}},
		Description: "若 p 是传递属性且 x p y、y p z，则 x p z（OWL 2 RL prp-trp）。",
	},
	{
		RuleKey: "owl-sameas-trans", Name: "sameAs 传递性", Kind: KindOWL2RL,
		Premises: []TriplePattern{
			{Subject: "?x", Predicate: IRIOWLSameAs, Object: "?y"},
			{Subject: "?y", Predicate: IRIOWLSameAs, Object: "?z"},
		},
		Conclusion:  []TriplePattern{{Subject: "?x", Predicate: IRIOWLSameAs, Object: "?z"}},
		Description: "sameAs 的传递闭包（OWL 2 RL eq-trans）。",
	},
	{
		RuleKey: "owl-sameas-sym", Name: "sameAs 对称性", Kind: KindOWL2RL,
		Premises: []TriplePattern{
			{Subject: "?x", Predicate: IRIOWLSameAs, Object: "?y"},
		},
		Conclusion:  []TriplePattern{{Subject: "?y", Predicate: IRIOWLSameAs, Object: "?x"}},
		Description: "sameAs 的对称闭包（OWL 2 RL eq-sym）。",
	},
}

// BuiltinRules 返回内置规则集的深副本（调用方可安全改写 Enabled 等字段）。
func BuiltinRules() []Rule {
	out := make([]Rule, len(builtinRules))
	for i, r := range builtinRules {
		r.Premises = append([]TriplePattern(nil), r.Premises...)
		r.Conclusion = append([]TriplePattern(nil), r.Conclusion...)
		r.Builtin = true
		r.Enabled = true
		out[i] = r
	}
	return out
}

// builtinKeySet 返回内置规则键集合（注册自定义规则时用于拒绝键冲突）。
func builtinKeySet() map[string]bool {
	m := make(map[string]bool, len(builtinRules))
	for _, r := range builtinRules {
		m[r.RuleKey] = true
	}
	return m
}

// BuiltinRuleKeys 返回全部内置规则键（排序后的副本，供健康检查/文档使用）。
func BuiltinRuleKeys() []string {
	keys := make([]string, 0, len(builtinRules))
	for k := range builtinKeySet() {
		keys = append(keys, k)
	}
	return keys
}
