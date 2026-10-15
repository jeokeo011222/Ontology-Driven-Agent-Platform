package shacl

import (
	"context"
	"testing"

	"odap/internal/ontology/graphstore"
)

// ---------------------------------------------------------------------------
// 白名单 ⟷ 实现一致性守门
// ---------------------------------------------------------------------------
//
// validate.go 用 supportedShapeKeywords / supportedPropertyKeywords 两份**人工维护**的
// 白名单做 fail-closed 判定：白名单内的关键字被放行，白名单外的直接报错。
//
// 「白名单里有」与「这条约束真的会被求值」是两件事，于是存在一个现有测试**发现不了**
// 的失效模式：
//
//	把 sh:xxx 加进白名单，却忘了在 evalProperty / nodeResults 里实现它。
//	→ 形状声明了这条约束、校验器放行了它、但从不检查它
//	→ 报告显示「通过」，而这条约束从未生效（静默降级）
//
// 既有的 TestValidateSupportedConstraints 覆盖了「每个约束的求值行为」，但它构造的
// PropertyShape **没有设置 Keywords**（checkKeywords 收到空列表即放行），因此它恰好
// 绕过了这条链路。本文件补的就是这条链路：对白名单里的每一个**载荷约束**的属性关键字，
// 都在「声明了该关键字」的形状上验证「违反必被发现、合规必不误报」。
//
// 白名单必须被下面三组集合**严格划分**（见 TestPropertyKeywordWhitelistIsAccountedFor）：
//   - structuralPropertyKeywords：只声明类型 / 定位 / 修饰，本身不构成可判定约束；
//   - enforcedPropertyKeywords：构成一条可判定约束，必须有用例；
//   - 落在两者之外的键：不允许存在，一旦出现即为漏实现。

// structuralPropertyKeywords：白名单内、但本身不构成可判定约束的属性关键字。
// 值必须写明理由 —— 空理由会被守门测试拒绝，避免用「登记一下绕过检查」。
var structuralPropertyKeywords = map[string]string{
	"PropertyShape": "节点类型声明：标记该节点是 sh:PropertyShape，不判对错",
	"path":          "定位用：指明约束落在哪个谓词上，缺它会报错，但它自身不判对错",
	"severity":      "修饰用：只改写发现项的严重级别，不改变是否违规（语义见 TestValidateSeverityWarningDoesNotBreakConformance）",
}

// structuralShapeKeywords：白名单内、不构成可判定约束的形状级关键字。
var structuralShapeKeywords = map[string]string{
	"NodeShape": "节点类型声明：标记该节点是 sh:NodeShape",
	"property":  "装载用：把 PropertyShape 挂到本形状上，本身不判对错",
	"severity":  "修饰用：改写本形状下发现项的默认严重级别",
}

// shapeLevelKeywordsWithBehavior：形状级关键字里**可判定行为**的那部分。
// 值是被既有测试覆盖的说明，供人工核对是否有测试真正执行了它。
var shapeLevelKeywordsWithBehavior = map[string]string{
	"targetClass": "TestValidateSupportedConstraints：焦点节点由 rdf:type 命中，未命中则不产生发现项",
	"targetNode":  "TestValidateTargetNode：显式指定焦点节点",
}

// keywordCase 是一条属性关键字的双向用例：违反与合规各一组数据。
type keywordCase struct {
	ps               PropertyShape // Path 由装配时统一填 testPath，Keywords 由装配时按关键字名补
	violating        []graphstore.Term
	violatingExtras  []graphstore.Triple
	conforming       []graphstore.Term
	conformingExtras []graphstore.Triple
	wantComponent    string
}

// enforcedPropertyKeywords：每个「载荷约束」的属性关键字，以及它的违反 / 合规用例。
//
// 键必须与 supportedPropertyKeywords 严格一致：多一个或少一个都会让守门测试失败。
var enforcedPropertyKeywords = map[string]keywordCase{
	"minCount": {
		ps:            PropertyShape{MinCount: intp(1)},
		conforming:    []graphstore.Term{iTerm("https://ex/alice")},
		wantComponent: "MinCount",
	},
	"maxCount": {
		ps:            PropertyShape{MaxCount: intp(1)},
		violating:     []graphstore.Term{litTerm("a", xsdString), litTerm("b", xsdString)},
		conforming:    []graphstore.Term{litTerm("a", xsdString)},
		wantComponent: "MaxCount",
	},
	"datatype": {
		ps:            PropertyShape{Datatype: xsdString},
		violating:     []graphstore.Term{iTerm("https://ex/alice")},
		conforming:    []graphstore.Term{litTerm("Alice", xsdString)},
		wantComponent: "Datatype",
	},
	"class": {
		ps:         PropertyShape{Class: odapEmployee},
		violating:  []graphstore.Term{iTerm("https://ex/bob")},
		conforming: []graphstore.Term{iTerm("https://ex/alice")},
		conformingExtras: []graphstore.Triple{{
			S: iTerm("https://ex/alice"), P: iTerm(rdfTypeIRI), O: iTerm(odapEmployee), GraphIRI: testGraph,
		}},
		wantComponent: "Class",
	},
	"nodeKind": {
		ps:            PropertyShape{NodeKind: NodeKindIRI},
		violating:     []graphstore.Term{litTerm("Alice", xsdString)},
		conforming:    []graphstore.Term{iTerm("https://ex/alice")},
		wantComponent: "NodeKind",
	},
	"in": {
		ps:            PropertyShape{In: []graphstore.Term{litTerm("PENDING", xsdString)}},
		violating:     []graphstore.Term{litTerm("UNKNOWN", xsdString)},
		conforming:    []graphstore.Term{litTerm("PENDING", xsdString)},
		wantComponent: "In",
	},
	"minInclusive": {
		ps:            PropertyShape{MinInclusive: f64p(0)},
		violating:     []graphstore.Term{litTerm("-1", xsdDecimal)},
		conforming:    []graphstore.Term{litTerm("5", xsdDecimal)},
		wantComponent: "MinInclusive",
	},
	"maxInclusive": {
		ps:            PropertyShape{MaxInclusive: f64p(10)},
		violating:     []graphstore.Term{litTerm("11", xsdDecimal)},
		conforming:    []graphstore.Term{litTerm("10", xsdDecimal)},
		wantComponent: "MaxInclusive",
	},
	"minExclusive": {
		ps:            PropertyShape{MinExclusive: f64p(0)},
		violating:     []graphstore.Term{litTerm("0", xsdDecimal)},
		conforming:    []graphstore.Term{litTerm("1", xsdDecimal)},
		wantComponent: "MinExclusive",
	},
	"maxExclusive": {
		ps:            PropertyShape{MaxExclusive: f64p(2)},
		violating:     []graphstore.Term{litTerm("2", xsdDecimal)},
		conforming:    []graphstore.Term{litTerm("1", xsdDecimal)},
		wantComponent: "MaxExclusive",
	},
	"pattern": {
		ps:            PropertyShape{Pattern: `^CLM-[0-9]+$`},
		violating:     []graphstore.Term{litTerm("X-1", xsdString)},
		conforming:    []graphstore.Term{litTerm("CLM-1024", xsdString)},
		wantComponent: "Pattern",
	},
	"minLength": {
		ps:            PropertyShape{MinLength: intp(3)},
		violating:     []graphstore.Term{litTerm("ab", xsdString)},
		conforming:    []graphstore.Term{litTerm("abc", xsdString)},
		wantComponent: "MinLength",
	},
	"maxLength": {
		ps:            PropertyShape{MaxLength: intp(3)},
		violating:     []graphstore.Term{litTerm("abcd", xsdString)},
		conforming:    []graphstore.Term{litTerm("abc", xsdString)},
		wantComponent: "MaxLength",
	},
	"hasValue": {
		ps:            PropertyShape{HasValue: termp(litTerm("PENDING", xsdString))},
		violating:     []graphstore.Term{litTerm("DONE", xsdString)},
		conforming:    []graphstore.Term{litTerm("PENDING", xsdString)},
		wantComponent: "HasValue",
	},
}

// runDeclaredShape 构造一个**声明了 keyword** 的形状并执行校验。
//
// 与 validate_test.go 的 runShape 的唯一区别是它会设置 Keywords —— 这正是本守门测试
// 要覆盖的那条链路：白名单判定发生在 Validate 内部，只看 Keywords 与约束字段是否一致。
func runDeclaredShape(t *testing.T, ps PropertyShape, keyword string,
	values []graphstore.Term, extra []graphstore.Triple) Report {
	t.Helper()
	m, tenant := seedBase(t)

	ps.Path = testPath
	ps.Keywords = []string{"PropertyShape", "path", keyword}

	ctx := context.Background()
	focus := iTerm(testFocusIRI)
	path := iTerm(testPath)
	triples := make([]graphstore.Triple, 0, len(values)+len(extra))
	for _, v := range values {
		triples = append(triples, graphstore.Triple{S: focus, P: path, O: v, GraphIRI: testGraph})
	}
	triples = append(triples, extra...)
	if len(triples) > 0 {
		if _, err := m.InsertTriples(ctx, tenant, testGraph, triples); err != nil {
			t.Fatalf("seed 值失败: %v", err)
		}
	}

	shapes := []Shape{{
		ID:          "ExpenseClaimShape",
		TargetClass: []string{testFocusCls},
		Severity:    SeverityViolation,
		Keywords:    []string{"NodeShape", "targetClass", "property"},
		Properties:  []PropertyShape{ps},
	}}
	rep, err := Validate(m, tenant, testGraph, shapes)
	if err != nil {
		t.Fatalf("Validate 失败: %v", err)
	}
	return rep
}

// TestPropertyKeywordWhitelistIsAccountedFor 断言白名单里**没有**任何关键字处于
// 「被放行但无实现、也无理由」的状态。
func TestPropertyKeywordWhitelistIsAccountedFor(t *testing.T) {
	for kw := range supportedPropertyKeywords {
		if _, ok := enforcedPropertyKeywords[kw]; ok {
			continue
		}
		if reason, ok := structuralPropertyKeywords[kw]; ok {
			if reason == "" {
				t.Fatalf("属性关键字 sh:%s 被归为结构性但没写理由：请说明它为何不构成可判定约束", kw)
			}
			continue
		}
		t.Fatalf("属性关键字 sh:%s 在白名单里，但既没有 enforcedPropertyKeywords 用例，"+
			"也没有登记到 structuralPropertyKeywords。\n"+
			"两种情况：① 它是可判定约束 → 请在 evalProperty/nodeResults 里实现它并补用例；\n"+
			"② 它只是声明或修饰 → 请登记到 structuralPropertyKeywords 并写明理由。\n"+
			"在补齐之前，声明该关键字的形状会被放行却从不校验，即静默降级。", kw)
	}

	// 反向：用例表与理由表不得出现白名单外的关键字（那意味着两份清单已经漂移）。
	for kw := range enforcedPropertyKeywords {
		if !supportedPropertyKeywords[kw] {
			t.Fatalf("enforcedPropertyKeywords 含白名单外的关键字 sh:%s", kw)
		}
	}
	for kw := range structuralPropertyKeywords {
		if !supportedPropertyKeywords[kw] {
			t.Fatalf("structuralPropertyKeywords 含白名单外的关键字 sh:%s", kw)
		}
	}
}

// TestShapeKeywordWhitelistIsAccountedFor 对形状级白名单做同样的守门。
func TestShapeKeywordWhitelistIsAccountedFor(t *testing.T) {
	for kw := range supportedShapeKeywords {
		if reason, ok := structuralShapeKeywords[kw]; ok {
			if reason == "" {
				t.Fatalf("形状关键字 sh:%s 被归为结构性但没写理由", kw)
			}
			continue
		}
		if covered, ok := shapeLevelKeywordsWithBehavior[kw]; ok {
			if covered == "" {
				t.Fatalf("形状关键字 sh:%s 声称有行为覆盖但没写明是哪个测试", kw)
			}
			continue
		}
		t.Fatalf("形状关键字 sh:%s 在白名单里，但既未归为结构性，也没有写明覆盖它的测试", kw)
	}
	for kw := range structuralShapeKeywords {
		if !supportedShapeKeywords[kw] {
			t.Fatalf("structuralShapeKeywords 含白名单外的关键字 sh:%s", kw)
		}
	}
	for kw := range shapeLevelKeywordsWithBehavior {
		if !supportedShapeKeywords[kw] {
			t.Fatalf("shapeLevelKeywordsWithBehavior 含白名单外的关键字 sh:%s", kw)
		}
	}
}

// TestPropertyKeywordsAreEnforcedUnderDeclaration 对每个载荷约束的关键字做双向验证：
// 「声明了该关键字」的形状上，违反必被发现、合规必不误报。
func TestPropertyKeywordsAreEnforcedUnderDeclaration(t *testing.T) {
	for kw, c := range enforcedPropertyKeywords {
		c := c
		t.Run(kw, func(t *testing.T) {
			rep := runDeclaredShape(t, c.ps, kw, c.violating, c.violatingExtras)
			if rep.Conforms {
				t.Fatalf("形状声明了 sh:%s 且数据违反它，却判定通过：该关键字被放行但没有被求值", kw)
			}
			if !hasComponent(rep, c.wantComponent) {
				t.Fatalf("期望发现项组件 %s，实际 %+v", c.wantComponent, rep.Results)
			}
			// 每条发现项都应带上形状身份，否则发现项身份无法区分不同形状的同类违规。
			for _, r := range rep.Results {
				if r.ShapeID != "ExpenseClaimShape" {
					t.Fatalf("发现项缺少形状身份，实际 %+v", r)
				}
			}

			ok := runDeclaredShape(t, c.ps, kw, c.conforming, c.conformingExtras)
			if !ok.Conforms || len(ok.Results) != 0 {
				t.Fatalf("合规数据不应产生发现项，实际 %+v", ok.Results)
			}
		})
	}
}

// TestStructuralPropertyKeywordReasonsAreNonEmpty 是守门测试的自检：
// 理由表本身不能是「登记一下就过」的橡皮图章。
func TestStructuralPropertyKeywordReasonsAreNonEmpty(t *testing.T) {
	if len(structuralPropertyKeywords) == 0 || len(structuralShapeKeywords) == 0 {
		t.Fatal("结构性关键字理由表为空：守门测试将形同虚设")
	}
	for kw, reason := range structuralPropertyKeywords {
		if len([]rune(reason)) < 8 {
			t.Fatalf("属性关键字 sh:%s 的理由过短（%q），无法据此判断归类是否正确", kw, reason)
		}
	}
}
