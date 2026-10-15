package shacl

import (
	"context"
	"strings"
	"testing"

	"odap/internal/ontology/graphstore"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

const (
	testGraph    = "https://ex/graph/instance"
	testFocusIRI = "https://ex/claim1"
	testFocusCls = "https://ex/ExpenseClaim"
	testPath     = "https://ex/p"
	xsdString    = "http://www.w3.org/2001/XMLSchema#string"
	xsdDecimal   = "http://www.w3.org/2001/XMLSchema#decimal"
	odapEmployee = "https://ex/Employee"
)

func iTerm(s string) graphstore.Term { return graphstore.Term{Kind: graphstore.KindIRI, Lexical: s} }
func litTerm(s, dt string) graphstore.Term {
	return graphstore.Term{Kind: graphstore.KindLiteral, Lexical: s, Datatype: dt}
}

func intp(v int) *int                          { return &v }
func f64p(v float64) *float64                  { return &v }
func termp(t graphstore.Term) *graphstore.Term { return &t }

func seedBase(t *testing.T) (*graphstore.MemStore, ids.ID) {
	t.Helper()
	m := graphstore.NewMemStore()
	tenant := ids.New()
	focus := iTerm(testFocusIRI)
	typeTerm := iTerm(rdfTypeIRI)
	init := []graphstore.Triple{{S: focus, P: typeTerm, O: iTerm(testFocusCls), GraphIRI: testGraph}}
	if _, err := m.InsertTriples(context.Background(), tenant, testGraph, init); err != nil {
		t.Fatalf("seed 失败: %v", err)
	}
	return m, tenant
}

func runShape(t *testing.T, values []graphstore.Term, extra []graphstore.Triple, ps PropertyShape) Report {
	t.Helper()
	m, tenant := seedBase(t)
	ctx := context.Background()
	focus := iTerm(testFocusIRI)
	path := iTerm(testPath)
	triples := make([]graphstore.Triple, 0, len(values))
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
		Properties:  []PropertyShape{ps},
	}}
	rep, err := Validate(m, tenant, testGraph, shapes)
	if err != nil {
		t.Fatalf("Validate 失败: %v", err)
	}
	return rep
}

func hasComponent(rep Report, comp string) bool {
	for _, r := range rep.Results {
		if r.Component == comp {
			return true
		}
	}
	return false
}

func TestValidateSupportedConstraints(t *testing.T) {
	employeeType := graphstore.Triple{
		S: iTerm("https://ex/alice"), P: iTerm(rdfTypeIRI), O: iTerm(odapEmployee), GraphIRI: testGraph,
	}

	tests := []struct {
		name        string
		values      []graphstore.Term
		extra       []graphstore.Triple
		ps          PropertyShape
		wantConform bool
		wantComp    string
	}{
		{
			name:        "minCount 通过",
			values:      []graphstore.Term{iTerm("https://ex/alice")},
			ps:          PropertyShape{Path: testPath, MinCount: intp(1)},
			wantConform: true,
		},
		{
			name:        "minCount 违反",
			ps:          PropertyShape{Path: testPath, MinCount: intp(1)},
			wantConform: false,
			wantComp:    "MinCount",
		},
		{
			name:        "maxCount 通过",
			values:      []graphstore.Term{litTerm("a", xsdString), litTerm("b", xsdString)},
			ps:          PropertyShape{Path: testPath, MaxCount: intp(2)},
			wantConform: true,
		},
		{
			name:        "maxCount 违反",
			values:      []graphstore.Term{litTerm("a", xsdString), litTerm("b", xsdString), litTerm("c", xsdString)},
			ps:          PropertyShape{Path: testPath, MaxCount: intp(2)},
			wantConform: false,
			wantComp:    "MaxCount",
		},
		{
			name:        "datatype 通过",
			values:      []graphstore.Term{litTerm("Alice", xsdString)},
			ps:          PropertyShape{Path: testPath, Datatype: xsdString},
			wantConform: true,
		},
		{
			name:        "datatype 违反",
			values:      []graphstore.Term{iTerm("https://ex/alice")},
			ps:          PropertyShape{Path: testPath, Datatype: xsdString},
			wantConform: false,
			wantComp:    "Datatype",
		},
		{
			name:        "class 通过",
			values:      []graphstore.Term{iTerm("https://ex/alice")},
			extra:       []graphstore.Triple{employeeType},
			ps:          PropertyShape{Path: testPath, Class: odapEmployee},
			wantConform: true,
		},
		{
			name:        "class 违反",
			values:      []graphstore.Term{iTerm("https://ex/bob")},
			ps:          PropertyShape{Path: testPath, Class: odapEmployee},
			wantConform: false,
			wantComp:    "Class",
		},
		{
			name:        "nodeKind IRI 通过",
			values:      []graphstore.Term{iTerm("https://ex/alice")},
			ps:          PropertyShape{Path: testPath, NodeKind: NodeKindIRI},
			wantConform: true,
		},
		{
			name:        "nodeKind IRI 违反",
			values:      []graphstore.Term{litTerm("Alice", xsdString)},
			ps:          PropertyShape{Path: testPath, NodeKind: NodeKindIRI},
			wantConform: false,
			wantComp:    "NodeKind",
		},
		{
			name:        "in 通过",
			values:      []graphstore.Term{litTerm("PENDING", xsdString)},
			ps:          PropertyShape{Path: testPath, In: []graphstore.Term{litTerm("PENDING", xsdString), litTerm("DONE", xsdString)}},
			wantConform: true,
		},
		{
			name:        "in 违反",
			values:      []graphstore.Term{litTerm("UNKNOWN", xsdString)},
			ps:          PropertyShape{Path: testPath, In: []graphstore.Term{litTerm("PENDING", xsdString)}},
			wantConform: false,
			wantComp:    "In",
		},
		{
			name:        "minInclusive 通过",
			values:      []graphstore.Term{litTerm("5", xsdDecimal)},
			ps:          PropertyShape{Path: testPath, MinInclusive: f64p(0)},
			wantConform: true,
		},
		{
			name:        "minInclusive 违反",
			values:      []graphstore.Term{litTerm("-1", xsdDecimal)},
			ps:          PropertyShape{Path: testPath, MinInclusive: f64p(0)},
			wantConform: false,
			wantComp:    "MinInclusive",
		},
		{
			name:        "maxInclusive 通过",
			values:      []graphstore.Term{litTerm("10", xsdDecimal)},
			ps:          PropertyShape{Path: testPath, MaxInclusive: f64p(10)},
			wantConform: true,
		},
		{
			name:        "maxInclusive 违反",
			values:      []graphstore.Term{litTerm("11", xsdDecimal)},
			ps:          PropertyShape{Path: testPath, MaxInclusive: f64p(10)},
			wantConform: false,
			wantComp:    "MaxInclusive",
		},
		{
			name:        "minExclusive 通过",
			values:      []graphstore.Term{litTerm("1", xsdDecimal)},
			ps:          PropertyShape{Path: testPath, MinExclusive: f64p(0)},
			wantConform: true,
		},
		{
			name:        "minExclusive 违反",
			values:      []graphstore.Term{litTerm("0", xsdDecimal)},
			ps:          PropertyShape{Path: testPath, MinExclusive: f64p(0)},
			wantConform: false,
			wantComp:    "MinExclusive",
		},
		{
			name:        "maxExclusive 通过",
			values:      []graphstore.Term{litTerm("1", xsdDecimal)},
			ps:          PropertyShape{Path: testPath, MaxExclusive: f64p(2)},
			wantConform: true,
		},
		{
			name:        "maxExclusive 违反",
			values:      []graphstore.Term{litTerm("2", xsdDecimal)},
			ps:          PropertyShape{Path: testPath, MaxExclusive: f64p(2)},
			wantConform: false,
			wantComp:    "MaxExclusive",
		},
		{
			name:        "pattern 通过",
			values:      []graphstore.Term{litTerm("CLM-1024", xsdString)},
			ps:          PropertyShape{Path: testPath, Pattern: `^CLM-[0-9]+$`},
			wantConform: true,
		},
		{
			name:        "pattern 违反",
			values:      []graphstore.Term{litTerm("X-1", xsdString)},
			ps:          PropertyShape{Path: testPath, Pattern: `^CLM-[0-9]+$`},
			wantConform: false,
			wantComp:    "Pattern",
		},
		{
			name:        "minLength 通过",
			values:      []graphstore.Term{litTerm("abc", xsdString)},
			ps:          PropertyShape{Path: testPath, MinLength: intp(3)},
			wantConform: true,
		},
		{
			name:        "minLength 违反",
			values:      []graphstore.Term{litTerm("ab", xsdString)},
			ps:          PropertyShape{Path: testPath, MinLength: intp(3)},
			wantConform: false,
			wantComp:    "MinLength",
		},
		{
			name:        "maxLength 通过",
			values:      []graphstore.Term{litTerm("abc", xsdString)},
			ps:          PropertyShape{Path: testPath, MaxLength: intp(3)},
			wantConform: true,
		},
		{
			name:        "maxLength 违反",
			values:      []graphstore.Term{litTerm("abcd", xsdString)},
			ps:          PropertyShape{Path: testPath, MaxLength: intp(3)},
			wantConform: false,
			wantComp:    "MaxLength",
		},
		{
			name:        "hasValue 通过",
			values:      []graphstore.Term{litTerm("PENDING", xsdString)},
			ps:          PropertyShape{Path: testPath, HasValue: termp(litTerm("PENDING", xsdString))},
			wantConform: true,
		},
		{
			name:        "hasValue 违反",
			values:      []graphstore.Term{litTerm("DONE", xsdString)},
			ps:          PropertyShape{Path: testPath, HasValue: termp(litTerm("PENDING", xsdString))},
			wantConform: false,
			wantComp:    "HasValue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep := runShape(t, tt.values, tt.extra, tt.ps)
			if rep.Conforms != tt.wantConform {
				t.Fatalf("Conforms=%v，期望 %v；结果=%+v", rep.Conforms, tt.wantConform, rep.Results)
			}
			if tt.wantComp != "" && !hasComponent(rep, tt.wantComp) {
				t.Fatalf("期望发现项组件 %s，实际 %+v", tt.wantComp, rep.Results)
			}
			if tt.wantConform && len(rep.Results) != 0 {
				t.Fatalf("通过用例不应有发现项，实际 %+v", rep.Results)
			}
		})
	}
}

func TestValidateRejectsSparql(t *testing.T) {
	m, tenant := seedBase(t)
	shapes := []Shape{{
		ID:          "BadShape",
		TargetClass: []string{testFocusCls},
		Keywords:    []string{"sparql"},
	}}
	_, err := Validate(m, tenant, testGraph, shapes)
	if err == nil {
		t.Fatal("遇到 sh:sparql 必须返回错误")
	}
	appErr := errors.As(err)
	if appErr.Code != errors.CodeOntologyValidationFailed {
		t.Fatalf("错误码应为 %s，实际 %s", errors.CodeOntologyValidationFailed, appErr.Code)
	}
	if !strings.Contains(appErr.Message, "sparql") {
		t.Fatalf("错误消息应说明不支持 sh:sparql，实际 %q", appErr.Message)
	}
}

func TestValidateRejectsUnknownKeyword(t *testing.T) {
	m, tenant := seedBase(t)
	shapes := []Shape{{
		ID:          "UnknownShape",
		TargetClass: []string{testFocusCls},
		Keywords:    []string{"sh:closed"},
	}}
	_, err := Validate(m, tenant, testGraph, shapes)
	if appErr := errors.As(err); appErr.Code != errors.CodeOntologyValidationFailed {
		t.Fatalf("未知关键字应 fail-closed 返回 %s，实际 %s", errors.CodeOntologyValidationFailed, appErr.Code)
	}

	shapes2 := []Shape{{
		ID:          "UnknownPropShape",
		TargetClass: []string{testFocusCls},
		Properties:  []PropertyShape{{Path: testPath, Keywords: []string{"sh:qualifiedValueShape"}}},
	}}
	if _, err := Validate(m, tenant, testGraph, shapes2); errors.As(err).Code != errors.CodeOntologyValidationFailed {
		t.Fatalf("属性形状上的未知关键字应被拒绝")
	}
}

func TestValidateNodeShapeWithoutTarget(t *testing.T) {
	m, tenant := seedBase(t)
	shapes := []Shape{{
		ID:         "NoTargetShape",
		Properties: []PropertyShape{{Path: testPath, MinCount: intp(1)}},
	}}
	rep, err := Validate(m, tenant, testGraph, shapes)
	if err != nil {
		t.Fatalf("非预期错误: %v", err)
	}
	if !rep.Conforms || len(rep.Results) != 0 {
		t.Fatalf("无目标的 NodeShape 应不产生任何发现项，实际 %+v", rep)
	}
}

func TestValidateTargetNode(t *testing.T) {
	m, tenant := seedBase(t)
	shapes := []Shape{{
		ID:         "NodeShape",
		TargetNode: []graphstore.Term{iTerm(testFocusIRI)},
		Properties: []PropertyShape{{Path: testPath, MinCount: intp(1)}},
	}}
	rep, err := Validate(m, tenant, testGraph, shapes)
	if err != nil {
		t.Fatalf("非预期错误: %v", err)
	}
	if rep.Conforms || !hasComponent(rep, "MinCount") {
		t.Fatalf("targetNode 命中且缺值时应违反 minCount，实际 %+v", rep)
	}
	if rep.Results[0].FocusNode != testFocusIRI {
		t.Fatalf("焦点节点应为 %s，实际 %s", testFocusIRI, rep.Results[0].FocusNode)
	}
}

func TestValidateRejectsUnsupportedPath(t *testing.T) {
	m, tenant := seedBase(t)
	for _, path := range []string{"^https://ex/p", "https://ex/a|https://ex/b", "https://ex/a/https://ex/b"} {
		shapes := []Shape{{
			TargetClass: []string{testFocusCls},
			Properties:  []PropertyShape{{Path: path}},
		}}
		if _, err := Validate(m, tenant, testGraph, shapes); errors.As(err).Code != errors.CodeOntologyValidationFailed {
			t.Fatalf("路径 %q 应被拒绝", path)
		}
	}
	// 缺少 sh:path 也必须拒绝。
	shapes := []Shape{{TargetClass: []string{testFocusCls}, Properties: []PropertyShape{{}}}}
	if _, err := Validate(m, tenant, testGraph, shapes); errors.As(err).Code != errors.CodeOntologyValidationFailed {
		t.Fatal("缺少 sh:path 应被拒绝")
	}
}

func TestValidateRejectsBadRegex(t *testing.T) {
	m, tenant := seedBase(t)
	shapes := []Shape{{
		TargetClass: []string{testFocusCls},
		Properties:  []PropertyShape{{Path: testPath, Pattern: "("}},
	}}
	if _, err := Validate(m, tenant, testGraph, shapes); errors.As(err).Code != errors.CodeOntologyValidationFailed {
		t.Fatal("非法正则应被拒绝")
	}
}

func TestValidateSeverityWarningDoesNotBreakConformance(t *testing.T) {
	rep := runShape(t, nil, nil, PropertyShape{Path: testPath, MinCount: intp(1), Severity: SeverityWarning})
	if !rep.Conforms {
		t.Fatalf("Warning 级发现项不应破坏一致性，实际 %+v", rep)
	}
	if len(rep.Results) != 1 || rep.Results[0].Severity != string(SeverityWarning) {
		t.Fatalf("应产生一条 Warning 发现项，实际 %+v", rep.Results)
	}
}

func TestValidateParameterValidation(t *testing.T) {
	m, tenant := seedBase(t)
	if _, err := Validate(nil, tenant, testGraph, nil); errors.As(err).Code != errors.CodeInternal {
		t.Fatalf("nil store 应返回 %s", errors.CodeInternal)
	}
	if _, err := Validate(m, ids.ID{}, testGraph, nil); errors.As(err).Code != errors.CodeValidationFailed {
		t.Fatalf("零值 tenant 应返回 %s", errors.CodeValidationFailed)
	}
	if _, err := Validate(m, tenant, "", nil); errors.As(err).Code != errors.CodeValidationFailed {
		t.Fatalf("空 graph 应返回 %s", errors.CodeValidationFailed)
	}
}

func TestValidateMinCountZeroAllowsMissing(t *testing.T) {
	rep := runShape(t, nil, nil, PropertyShape{Path: testPath, MinCount: intp(0)})
	if !rep.Conforms {
		t.Fatalf("minCount=0 且无取值应通过，实际 %+v", rep)
	}
}
