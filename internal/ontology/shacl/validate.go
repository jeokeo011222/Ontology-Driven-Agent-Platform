// Package shacl 实现 ODAP 的 SHACL 核心子集校验器（第十三篇 §15）。
//
// # 支持矩阵（V1.0）
//
// 目标声明：sh:targetClass、sh:targetNode
// 属性约束：sh:property + sh:path（仅单步谓词路径 IRI，不含 sh:inversePath / 序列路径 /
// sh:alternativePath）、sh:minCount、sh:maxCount、sh:datatype、sh:class、sh:nodeKind
// （IRI/BlankNode/Literal）、sh:in、sh:minInclusive、sh:maxInclusive、sh:minExclusive、
// sh:maxExclusive、sh:pattern（Go regexp）、sh:minLength、sh:maxLength、sh:hasValue
//
// # 显式排除（fail-closed，不静默跳过）
//
//   - sh:sparql（含 sh:ask / sh:select）：**显式排除**。理由与第十三篇 §15.1、§8.2、§9.1
//     一致——ODAP 不自研 SPARQL。遇到该关键字必须返回 ONTOLOGY_VALIDATION_FAILED，
//     绝不静默跳过该约束后报告"通过"。
//   - 任意未知 sh:* 关键字：同样 fail-closed 拒绝，不静默忽略，避免制造"验证通过"的假象。
//   - SHACL Advanced Features（sh:expression / sh:rule 等）：派生规则走推理引擎（§12），
//     不走 SHACL 规则扩展。
package shacl

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"odap/internal/ontology/graphstore"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

const rdfTypeIRI = "http://www.w3.org/1999/02/22-rdf-syntax-ns#type"

// Severity 是校验结果严重级别。
type Severity string

const (
	SeverityViolation Severity = "Violation"
	SeverityWarning   Severity = "Warning"
	SeverityInfo      Severity = "Info"
)

// NodeKind 是 sh:nodeKind 的取值。
type NodeKind string

const (
	NodeKindIRI       NodeKind = "IRI"
	NodeKindBlankNode NodeKind = "BlankNode"
	NodeKindLiteral   NodeKind = "Literal"
)

// supportedShapeKeywords 是形状层允许出现的 sh:* 关键字（去前缀）。
var supportedShapeKeywords = map[string]bool{
	"NodeShape":   true,
	"targetClass": true,
	"targetNode":  true,
	"property":    true,
	"severity":    true,
}

// supportedPropertyKeywords 是属性形状层允许出现的 sh:* 关键字（去前缀）。
var supportedPropertyKeywords = map[string]bool{
	"PropertyShape": true,
	"path":          true,
	"minCount":      true,
	"maxCount":      true,
	"datatype":      true,
	"class":         true,
	"nodeKind":      true,
	"in":            true,
	"minInclusive":  true,
	"maxInclusive":  true,
	"minExclusive":  true,
	"maxExclusive":  true,
	"pattern":       true,
	"minLength":     true,
	"maxLength":     true,
	"hasValue":      true,
	"severity":      true,
}

// Shape 是一个 sh:NodeShape。
//
// Keywords 记录该形状上出现的 sh:* 关键字名（去 sh: 前缀）。校验器对其中任何不支持的关键字
// （含 sparql）都会 fail-closed 拒绝；调用方只需如实列出出现过的关键字即可。
type Shape struct {
	ID          string
	TargetClass []string          // sh:targetClass 的类 IRI
	TargetNode  []graphstore.Term // sh:targetNode 指定的焦点节点
	Properties  []PropertyShape   // sh:property
	Keywords    []string          // 出现过的 sh:* 关键字（去前缀）
	Severity    Severity
}

// PropertyShape 是一个 sh:PropertyShape。
type PropertyShape struct {
	ID           string
	Path         string // sh:path（单步谓词路径 IRI）
	MinCount     *int
	MaxCount     *int
	Datatype     string
	Class        string
	NodeKind     NodeKind
	In           []graphstore.Term
	MinInclusive *float64
	MaxInclusive *float64
	MinExclusive *float64
	MaxExclusive *float64
	Pattern      string
	MinLength    *int
	MaxLength    *int
	HasValue     *graphstore.Term
	Keywords     []string
	Severity     Severity

	re *regexp.Regexp // 编译后的 sh:pattern
}

// Result 是一条校验发现项。
type Result struct {
	// ShapeID 是产出该发现项的形状 IRI。
	//
	// 它不参与校验逻辑，只用于给发现项定义**跨轮次稳定的身份**（见 Result.Digest）：
	// 没有它，两条约束同一路径的不同形状会在同一焦点节点上产出完全相同的身份串，
	// 回归对比时就会被当成"同一个问题"而漏掉一条。
	ShapeID   string
	FocusNode string
	Path      string
	Component string
	Message   string
	Severity  string
}

// Report 是一次校验的完整结果。
type Report struct {
	Conforms bool
	Results  []Result
}

// Validate 对命名图执行 SHACL 校验。
//
// 行为说明：没有 sh:targetClass / sh:targetNode 的 NodeShape 没有焦点节点，因此不产生任何
// 发现项（与 SHACL 规范一致：未被目标命中的形状不参与校验）。
func Validate(store graphstore.GraphStore, tenantID ids.ID, graphIRI string, shapes []Shape) (Report, error) {
	if store == nil {
		return Report{}, errors.E(errors.CodeInternal, "shacl: GraphStore 不能为空")
	}
	if tenantID.IsZero() {
		return Report{}, errors.E(errors.CodeValidationFailed, "shacl: tenant_id 不能为空")
	}
	if graphIRI == "" {
		return Report{}, errors.E(errors.CodeValidationFailed, "shacl: graph_iri 不能为空")
	}

	rep := Report{Conforms: true}
	for i := range shapes {
		sh := &shapes[i]
		if err := checkKeywords(sh.Keywords, supportedShapeKeywords); err != nil {
			return Report{}, err
		}
		for j := range sh.Properties {
			ps := &sh.Properties[j]
			if err := checkKeywords(ps.Keywords, supportedPropertyKeywords); err != nil {
				return Report{}, err
			}
			if err := compilePropertyShape(ps); err != nil {
				return Report{}, err
			}
		}

		focus, err := focusNodes(store, tenantID, graphIRI, sh)
		if err != nil {
			return Report{}, err
		}
		for _, fn := range focus {
			for j := range sh.Properties {
				results, err := evalProperty(store, tenantID, graphIRI, fn, sh.ID, sh.Properties[j], sh.Severity)
				if err != nil {
					return Report{}, err
				}
				for _, r := range results {
					if r.Severity == string(SeverityViolation) {
						rep.Conforms = false
					}
					rep.Results = append(rep.Results, r)
				}
			}
		}
	}
	return rep, nil
}

// checkKeywords 对关键字做 fail-closed 校验。
func checkKeywords(keywords []string, supported map[string]bool) error {
	for _, k := range keywords {
		name := strings.TrimPrefix(k, "sh:")
		if name == "sparql" {
			return errors.E(errors.CodeOntologyValidationFailed,
				"本实现不自研 SPARQL，故不支持 sh:sparql（第十三篇 §15.1 显式排除；需要该能力请改用受控规则）")
		}
		if !supported[name] {
			return errors.Ef(errors.CodeOntologyValidationFailed,
				"不支持的 SHACL 关键字 sh:%s（fail-closed 拒绝，不静默忽略）", name)
		}
	}
	return nil
}

// ValidateShapeKeywords 对单个形状（含其属性形状）做关键字 fail-closed 检查。
//
// 为什么单独导出：生成路径（AI 产 SHACL 形状）需要在**落库之前**就拒绝含不支持
// 关键字的形状，而不是等到校验时才发现。若生成路径自己维护一份"受支持关键字"清单，
// 就会出现第二份真值来源——两份清单迟早漂移，而漂移的方向必然是"生成端放行、
// 校验端拒绝"或更糟的"两端都放行"。此处复用 Validate 内部的同一份白名单与同一
// 判定函数，保证两条路径的结论在构造上一致。
func ValidateShapeKeywords(sh Shape) error {
	if err := checkKeywords(sh.Keywords, supportedShapeKeywords); err != nil {
		return err
	}
	for i := range sh.Properties {
		if err := checkKeywords(sh.Properties[i].Keywords, supportedPropertyKeywords); err != nil {
			return err
		}
	}
	return nil
}

func compilePropertyShape(ps *PropertyShape) error {
	if strings.TrimSpace(ps.Path) == "" {
		return errors.E(errors.CodeOntologyValidationFailed, "sh:property 缺少 sh:path")
	}
	if !isPredicatePath(ps.Path) {
		return errors.Ef(errors.CodeOntologyValidationFailed,
			"仅支持单步谓词路径 sh:path，实际为 %q（V1.0 不支持 sh:inversePath / 序列路径 / 替代路径）", ps.Path)
	}
	if ps.NodeKind != "" && ps.NodeKind != NodeKindIRI && ps.NodeKind != NodeKindBlankNode && ps.NodeKind != NodeKindLiteral {
		return errors.Ef(errors.CodeOntologyValidationFailed, "非法的 sh:nodeKind=%q", ps.NodeKind)
	}
	if ps.MinCount != nil && *ps.MinCount < 0 {
		return errors.E(errors.CodeOntologyValidationFailed, "sh:minCount 不能为负")
	}
	if ps.MaxCount != nil && *ps.MaxCount < 0 {
		return errors.E(errors.CodeOntologyValidationFailed, "sh:maxCount 不能为负")
	}
	if ps.MinLength != nil && *ps.MinLength < 0 {
		return errors.E(errors.CodeOntologyValidationFailed, "sh:minLength 不能为负")
	}
	if ps.MaxLength != nil && *ps.MaxLength < 0 {
		return errors.E(errors.CodeOntologyValidationFailed, "sh:maxLength 不能为负")
	}
	if ps.Pattern != "" {
		re, err := regexp.Compile(ps.Pattern)
		if err != nil {
			return errors.Wrap(err, errors.CodeOntologyValidationFailed, "sh:pattern 不是合法的 Go 正则表达式")
		}
		ps.re = re
	}
	return nil
}

// isPredicatePath 判断是否为可接受的单步谓词路径（IRI）。
func isPredicatePath(p string) bool {
	if strings.HasPrefix(p, "^") {
		return false
	}
	if strings.ContainsAny(p, "()| \t\n") {
		return false
	}
	if strings.Count(p, "://") > 1 {
		return false // 形如 "a://b/a://c" 的序列路径
	}
	if strings.HasPrefix(p, "urn:") {
		return true
	}
	return strings.Contains(p, "://")
}

func focusNodes(store graphstore.GraphStore, tenantID ids.ID, graphIRI string, sh *Shape) ([]graphstore.Term, error) {
	ctx := context.Background()
	seen := make(map[string]bool)
	out := make([]graphstore.Term, 0)
	add := func(t graphstore.Term) {
		k := termKey(t)
		if seen[k] {
			return
		}
		seen[k] = true
		out = append(out, t)
	}

	for _, tn := range sh.TargetNode {
		add(tn)
	}
	if len(sh.TargetClass) > 0 {
		typeTerm := graphstore.Term{Kind: graphstore.KindIRI, Lexical: rdfTypeIRI}
		for _, cls := range sh.TargetClass {
			classTerm := graphstore.Term{Kind: graphstore.KindIRI, Lexical: cls}
			matches, err := store.Match(ctx, tenantID, graphstore.Pattern{
				P: &typeTerm, O: &classTerm, GraphIRI: graphIRI,
			})
			if err != nil {
				return nil, err
			}
			for _, mt := range matches {
				add(mt.S)
			}
		}
	}
	return out, nil
}

func evalProperty(store graphstore.GraphStore, tenantID ids.ID, graphIRI string,
	focus graphstore.Term, shapeID string, ps PropertyShape, shapeSeverity Severity) ([]Result, error) {

	ctx := context.Background()
	pathTerm := graphstore.Term{Kind: graphstore.KindIRI, Lexical: ps.Path}
	values, err := store.Match(ctx, tenantID, graphstore.Pattern{
		S: &focus, P: &pathTerm, GraphIRI: graphIRI,
	})
	if err != nil {
		return nil, err
	}

	sev := severityOf(ps.Severity, shapeSeverity)
	base := Result{ShapeID: shapeID, FocusNode: focus.Lexical, Path: ps.Path, Severity: string(sev)}
	results := make([]Result, 0)

	if ps.MinCount != nil && len(values) < *ps.MinCount {
		r := base
		r.Component = "MinCount"
		r.Message = fmt.Sprintf("属性 %s 的取值数为 %d，少于 sh:minCount=%d", ps.Path, len(values), *ps.MinCount)
		results = append(results, r)
	}
	if ps.MaxCount != nil && len(values) > *ps.MaxCount {
		r := base
		r.Component = "MaxCount"
		r.Message = fmt.Sprintf("属性 %s 的取值数为 %d，多于 sh:maxCount=%d", ps.Path, len(values), *ps.MaxCount)
		results = append(results, r)
	}

	for _, v := range values {
		nodeIssues, err := nodeResults(store, tenantID, graphIRI, v.O, ps, base)
		if err != nil {
			return nil, err
		}
		results = append(results, nodeIssues...)
	}

	if ps.HasValue != nil {
		found := false
		for _, v := range values {
			if termEqual(v.O, *ps.HasValue) {
				found = true
				break
			}
		}
		if !found {
			r := base
			r.Component = "HasValue"
			r.Message = fmt.Sprintf("属性 %s 的取值集合中缺少 sh:hasValue=%s", ps.Path, ps.HasValue.Lexical)
			results = append(results, r)
		}
	}
	return results, nil
}

func nodeResults(store graphstore.GraphStore, tenantID ids.ID, graphIRI string,
	o graphstore.Term, ps PropertyShape, base Result) ([]Result, error) {

	out := make([]Result, 0)
	comp := func(component, message string) {
		r := base
		r.Component = component
		r.Message = message
		out = append(out, r)
	}

	if ps.Datatype != "" {
		if o.Kind != graphstore.KindLiteral || o.Datatype != ps.Datatype {
			comp("Datatype", fmt.Sprintf("取值 %s 的 datatype 不是 sh:datatype=%s", describe(o), ps.Datatype))
		}
	}
	if ps.NodeKind != "" {
		if nodeKindOf(o) != ps.NodeKind {
			comp("NodeKind", fmt.Sprintf("取值 %s 的 nodeKind 不是 sh:nodeKind=%s", describe(o), ps.NodeKind))
		}
	}
	if ps.Class != "" {
		if o.Kind == graphstore.KindLiteral {
			comp("Class", fmt.Sprintf("字面值 %s 不能是类 %s 的实例", describe(o), ps.Class))
		} else {
			typeTerm := graphstore.Term{Kind: graphstore.KindIRI, Lexical: rdfTypeIRI}
			classTerm := graphstore.Term{Kind: graphstore.KindIRI, Lexical: ps.Class}
			got, err := store.Match(context.Background(), tenantID, graphstore.Pattern{
				S: &o, P: &typeTerm, O: &classTerm, GraphIRI: graphIRI,
			})
			if err != nil {
				return nil, err
			}
			if len(got) == 0 {
				comp("Class", fmt.Sprintf("取值 %s 不是 sh:class=%s 的实例", describe(o), ps.Class))
			}
		}
	}
	if len(ps.In) > 0 {
		found := false
		for _, allowed := range ps.In {
			if termEqual(o, allowed) {
				found = true
				break
			}
		}
		if !found {
			comp("In", fmt.Sprintf("取值 %s 不在 sh:in 允许集合内", describe(o)))
		}
	}

	if ps.MinInclusive != nil {
		if num, ok := parseNumber(o); !ok {
			comp("MinInclusive", fmt.Sprintf("取值 %s 无法作为数值比较 sh:minInclusive", describe(o)))
		} else if num < *ps.MinInclusive {
			comp("MinInclusive", fmt.Sprintf("取值 %v 小于 sh:minInclusive=%v", num, *ps.MinInclusive))
		}
	}
	if ps.MaxInclusive != nil {
		if num, ok := parseNumber(o); !ok {
			comp("MaxInclusive", fmt.Sprintf("取值 %s 无法作为数值比较 sh:maxInclusive", describe(o)))
		} else if num > *ps.MaxInclusive {
			comp("MaxInclusive", fmt.Sprintf("取值 %v 大于 sh:maxInclusive=%v", num, *ps.MaxInclusive))
		}
	}
	if ps.MinExclusive != nil {
		if num, ok := parseNumber(o); !ok {
			comp("MinExclusive", fmt.Sprintf("取值 %s 无法作为数值比较 sh:minExclusive", describe(o)))
		} else if num <= *ps.MinExclusive {
			comp("MinExclusive", fmt.Sprintf("取值 %v 不大于 sh:minExclusive=%v", num, *ps.MinExclusive))
		}
	}
	if ps.MaxExclusive != nil {
		if num, ok := parseNumber(o); !ok {
			comp("MaxExclusive", fmt.Sprintf("取值 %s 无法作为数值比较 sh:maxExclusive", describe(o)))
		} else if num >= *ps.MaxExclusive {
			comp("MaxExclusive", fmt.Sprintf("取值 %v 不小于 sh:maxExclusive=%v", num, *ps.MaxExclusive))
		}
	}

	if ps.re != nil && !ps.re.MatchString(o.Lexical) {
		comp("Pattern", fmt.Sprintf("取值 %s 不匹配 sh:pattern=%q", describe(o), ps.Pattern))
	}
	if ps.MinLength != nil && utf8.RuneCountInString(o.Lexical) < *ps.MinLength {
		comp("MinLength", fmt.Sprintf("取值长度 %d 小于 sh:minLength=%d", utf8.RuneCountInString(o.Lexical), *ps.MinLength))
	}
	if ps.MaxLength != nil && utf8.RuneCountInString(o.Lexical) > *ps.MaxLength {
		comp("MaxLength", fmt.Sprintf("取值长度 %d 大于 sh:maxLength=%d", utf8.RuneCountInString(o.Lexical), *ps.MaxLength))
	}
	return out, nil
}

func severityOf(ps, sh Severity) Severity {
	if ps != "" {
		return ps
	}
	if sh != "" {
		return sh
	}
	return SeverityViolation
}

func nodeKindOf(t graphstore.Term) NodeKind {
	switch t.Kind {
	case graphstore.KindIRI:
		return NodeKindIRI
	case graphstore.KindBlankNode:
		return NodeKindBlankNode
	default:
		return NodeKindLiteral
	}
}

func parseNumber(t graphstore.Term) (float64, bool) {
	if t.Kind != graphstore.KindLiteral {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(t.Lexical), 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func termEqual(a, b graphstore.Term) bool {
	return a.Kind == b.Kind && a.Lexical == b.Lexical && a.Datatype == b.Datatype && a.Lang == b.Lang
}

func termKey(t graphstore.Term) string {
	return string(rune(t.Kind)) + "\x1f" + t.Lexical + "\x1f" + t.Datatype + "\x1f" + t.Lang
}

func describe(t graphstore.Term) string {
	return t.Lexical
}
