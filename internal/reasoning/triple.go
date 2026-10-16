// Package reasoning 实现 ODAP 的推理引擎（模块 B）：
// 半朴素前向链（物化至不动点）、目标驱动后向链、增量更新、
// 自定义规则注册（reason.rules）与一致性检查（SHACL 联动）。
//
// 规则模式词法约定（与 contracts/proto/reasoning.proto 一致）：
//   - 空串 ""    ：匿名变量（通配，各出现位置相互独立）；
//   - "?x" 形式  ：具名变量，同一规则内同名变量绑定为同一词项；
//   - 其余字符串 ：常量词项——`"..."` 字面值（可带 ^^<datatype> / @lang 后缀）、
//     `_:label` 空白节点、其余按 IRI 处理。
package reasoning

import (
	"strconv"
	"strings"

	"odap/internal/ontology/graphstore"
	"odap/internal/platform/errors"
)

// variablePrefix 是具名变量的前缀（"?x"）。
const variablePrefix = "?"

// TriplePattern 是三元组匹配模式（premises / conclusion 的组成单元）。
// 各字段为空串表示匿名变量，"?x" 表示具名变量，其余为常量词项词法。
type TriplePattern struct {
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	Object    string `json:"object"`
}

// isVariable 报告该词法位置是否为变量（空串=匿名变量；"?x"=具名变量）。
func isVariable(s string) bool { return s == "" || strings.HasPrefix(s, variablePrefix) }

// isNamedVariable 报告是否为具名变量（可跨位置共享绑定）。
func isNamedVariable(s string) bool {
	return strings.HasPrefix(s, variablePrefix) && len(s) > len(variablePrefix)
}

// varName 返回具名变量的名字（去掉 "?" 前缀）。
func varName(s string) string { return strings.TrimPrefix(s, variablePrefix) }

// termFromLexical 把词法字符串解析为图存储词项：
// `"lex"` / `"lex"^^<dt>` / `"lex"@lang` → 字面值；`_:label` → 空白节点；其余 → IRI。
func termFromLexical(s string) (graphstore.Term, error) {
	if s == "" {
		return graphstore.Term{}, errors.E(errors.CodeValidationFailed, "词法词项不能为空")
	}
	if strings.HasPrefix(s, `_"`) || strings.HasPrefix(s, "_:") {
		label := strings.TrimPrefix(s, "_:")
		if label == "" {
			return graphstore.Term{}, errors.Ef(errors.CodeValidationFailed, "空白节点词法 %q 缺少 label", s)
		}
		return graphstore.Term{Kind: graphstore.KindBlankNode, Lexical: label}, nil
	}
	if strings.HasPrefix(s, `"`) {
		// 找到未转义的结束引号（strconv.Quote 的转义只会产生 \" 与 \\，逐字节回退扫描可靠）。
		end := -1
		for i := 1; i < len(s); i++ {
			if s[i] == '\\' {
				i++ // 跳过转义字符
				continue
			}
			if s[i] == '"' {
				end = i
				break
			}
		}
		if end < 0 {
			return graphstore.Term{}, errors.Ef(errors.CodeValidationFailed, "字面值词法 %q 缺少结束引号", s)
		}
		lex, err := strconv.Unquote(s[:end+1])
		if err != nil {
			return graphstore.Term{}, errors.Ef(errors.CodeValidationFailed, "字面值词法 %q 非法：%v", s, err)
		}
		t := graphstore.Term{Kind: graphstore.KindLiteral, Lexical: lex}
		rest := s[end+1:]
		switch {
		case rest == "":
			// 简单字面值（RDF 1.1 视同 xsd:string，图存储归一化为空 datatype）。
		case strings.HasPrefix(rest, "^^<") && strings.HasSuffix(rest, ">"):
			t.Datatype = rest[len("^^<") : len(rest)-1]
			if t.Datatype == "" {
				return graphstore.Term{}, errors.Ef(errors.CodeValidationFailed, "字面值词法 %q 的 datatype 为空", s)
			}
		case strings.HasPrefix(rest, "@"):
			t.Lang = rest[1:]
			if t.Lang == "" {
				return graphstore.Term{}, errors.Ef(errors.CodeValidationFailed, "字面值词法 %q 的 language tag 为空", s)
			}
		default:
			return graphstore.Term{}, errors.Ef(errors.CodeValidationFailed, "字面值词法 %q 含无法识别的后缀 %q", s, rest)
		}
		return t, nil
	}
	return graphstore.Term{Kind: graphstore.KindIRI, Lexical: s}, nil
}

// termToLexical 把图存储词项序列化回词法字符串（termFromLexical 的逆）。
func termToLexical(t graphstore.Term) string {
	switch t.Kind {
	case graphstore.KindBlankNode:
		return "_:" + t.Lexical
	case graphstore.KindLiteral:
		b := strings.Builder{}
		b.WriteString(strconv.Quote(t.Lexical))
		if t.Datatype != "" {
			b.WriteString("^^<" + t.Datatype + ">")
		}
		if t.Lang != "" {
			b.WriteString("@" + t.Lang)
		}
		return b.String()
	default:
		return t.Lexical
	}
}

// termEqual 按词项全字段判等（与 shacl.termEqual 同判据）。
func termEqual(a, b graphstore.Term) bool {
	return a.Kind == b.Kind && a.Lexical == b.Lexical && a.Datatype == b.Datatype && a.Lang == b.Lang
}

// TermLexical 是 termToLexical 的导出形态（HTTP/gRPC 响应投影用）。
func TermLexical(t graphstore.Term) string { return termToLexical(t) }

// patternToTriple 把模式按当前绑定实例化为具体三元组。
// 任一位置为未绑定变量时返回 ok=false（调用方按规则结构错误处理）。
func patternToTriple(p TriplePattern, b bindings) (graphstore.Triple, bool) {
	s, ok := instantiateField(p.Subject, b)
	if !ok {
		return graphstore.Triple{}, false
	}
	pd, ok := instantiateField(p.Predicate, b)
	if !ok {
		return graphstore.Triple{}, false
	}
	o, ok := instantiateField(p.Object, b)
	if !ok {
		return graphstore.Triple{}, false
	}
	return graphstore.Triple{S: s, P: pd, O: o}, true
}

// instantiateField 把单个词法位置按绑定实例化为词项。
func instantiateField(lex string, b bindings) (graphstore.Term, bool) {
	if !isVariable(lex) {
		t, err := termFromLexical(lex)
		if err != nil {
			return graphstore.Term{}, false
		}
		return t, true
	}
	if !isNamedVariable(lex) {
		return graphstore.Term{}, false // 匿名变量不可出现在结论（注册校验已拒绝，此处防御）
	}
	t, ok := b[varName(lex)]
	return t, ok
}

// tripleKeyOf 返回三元组的内存键（词法拼接，用于事实集去重）。
func tripleKeyOf(t graphstore.Triple) string {
	return termToLexical(t.S) + "\x1f" + termToLexical(t.P) + "\x1f" + termToLexical(t.O)
}

// LexicalTriple 是词法形式的三元组（HTTP/gRPC 入参的通用形态）。
type LexicalTriple struct {
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	Object    string `json:"object"`
}

// TriplesFromLexical 把词法三元组解析为图存储词项三元组（各位置按词法约定
// 识别 IRI/字面值/空白节点；解析失败即整体失败并指明位置，绝不静默跳过）。
func TriplesFromLexical(in []LexicalTriple) ([]graphstore.Triple, error) {
	out := make([]graphstore.Triple, 0, len(in))
	for i, lt := range in {
		s, err := termFromLexical(lt.Subject)
		if err != nil {
			return nil, errors.Ef(errors.CodeInvalidArgument, "第 %d 条三元组 subject 非法：%v", i, err)
		}
		p, err := termFromLexical(lt.Predicate)
		if err != nil {
			return nil, errors.Ef(errors.CodeInvalidArgument, "第 %d 条三元组 predicate 非法：%v", i, err)
		}
		o, err := termFromLexical(lt.Object)
		if err != nil {
			return nil, errors.Ef(errors.CodeInvalidArgument, "第 %d 条三元组 object 非法：%v", i, err)
		}
		out = append(out, graphstore.Triple{S: s, P: p, O: o})
	}
	return out, nil
}

// bindings 是具名变量到词项的绑定（键不含 "?" 前缀）。
type bindings map[string]graphstore.Term

// cloneBindings 复制绑定（选择点分支前必须复制，避免污染兄弟分支）。
func cloneBindings(b bindings) bindings {
	nb := make(bindings, len(b)+2)
	for k, v := range b {
		nb[k] = v
	}
	return nb
}

// matchPattern 以当前绑定匹配三元组；成功返回扩展后的绑定副本。
// 常量位置要求词项精确相等；具名变量要求与既有绑定一致或完成新绑定；
// 匿名变量仅作通配。
func matchPattern(p TriplePattern, t graphstore.Triple, b bindings) (bindings, bool) {
	nb := cloneBindings(b)
	if !matchField(p.Subject, t.S, nb) || !matchField(p.Predicate, t.P, nb) || !matchField(p.Object, t.O, nb) {
		return nil, false
	}
	return nb, true
}

func matchField(lex string, term graphstore.Term, b bindings) bool {
	switch {
	case !isVariable(lex):
		c, err := termFromLexical(lex)
		if err != nil {
			return false
		}
		return termEqual(c, term)
	case isNamedVariable(lex):
		name := varName(lex)
		if bound, ok := b[name]; ok {
			return termEqual(bound, term)
		}
		b[name] = term
		return true
	default:
		return true // 匿名变量：通配
	}
}
