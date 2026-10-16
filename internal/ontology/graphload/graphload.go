// Package graphload 把「本体清单（manifest）」投影为**可执行的**图数据与 SHACL 形状。
//
// # 为什么不经过 RDF 文本（Turtle / N-Triples）
//
// 两个下游入口的可执行形式都不是 RDF 文本：
//   - graphstore.InsertTriples 接收结构化 []graphstore.Triple；
//   - shacl.Validate 接收结构化 []shacl.Shape。
//
// 而仓内**没有 RDF 解析器**（graphstore/rdf.go 只提供 N-Triples 的写出方向：
// writeNTerm / termText）。因此从 manifest 到"规则真的能执行"的最短路径是直接
// 构造结构：Turtle 只是 []Triple 的一种序列化，先补解析器再想用途是本末倒置。
// 等真需要"上传 .ttl 文件"时再补解析器，届时本包产出的 []Triple 就是它的输入。
//
// # 职责边界（刻意收窄，避免第二份真值来源）
//
//   - 本包只做**结构与闭包**校验：名字是否解析、类型是否在枚举内、引用是否存在。
//   - 本包**不做**取值约束校验（minCount / pattern / maxInclusive …）。
//     那是 SHACL 的职责，由 shacl.Validate 执行。若这里也查一遍，就会出现两套
//     规则实现，迟早漂移；而且会让"生成时报错"取代"校验时产出 finding"，
//     使 validation_findings 永远为空——反而消灭了产品最有价值的那部分数据。
//
// # 词项命名：必须避开 GraphStore 的保留命名空间
//
// 词项 IRI 统一用 https://… 形式，**不得**用 urn:odap: 前缀：该前缀是适配器保留的
// （graphstore.validateGraphIRI 拒绝它作为图 IRI；且其中的空白节点前缀在读出时会被
// 还原成 KindBlankNode）。用保留前缀命名业务词项，会让词项被误判为空白节点——
// 这类缺陷不报错，只静默改变语义。
package graphload

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"odap/internal/ontology/graphstore"
	"odap/internal/ontology/shacl"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// RDF / RDFS / XSD 词表 IRI。
const (
	iriRDFType      = "http://www.w3.org/1999/02/22-rdf-syntax-ns#type"
	iriRDFProperty  = "http://www.w3.org/1999/02/22-rdf-syntax-ns#Property"
	iriRDFSClass    = "http://www.w3.org/2000/01/rdf-schema#Class"
	iriRDFSLabel    = "http://www.w3.org/2000/01/rdf-schema#label"
	iriRDFSDomain   = "http://www.w3.org/2000/01/rdf-schema#domain"
	iriRDFSRange    = "http://www.w3.org/2000/01/rdf-schema#range"
	xsdNamespace    = "http://www.w3.org/2001/XMLSchema#"
	graphIRIRoot    = "https://odap.example/ont/"
	instancePatHint = "inst/"
)

// xsdShorthand 把清单里的类型简写映射为 XSD 局部名。
//
// 只认简写与完整 XSD IRI 两种形态；其余一律拒绝（fail-closed），
// 不猜测"模型大概想表达什么类型"。
var xsdShorthand = map[string]string{
	"string":   "string",
	"number":   "decimal",
	"decimal":  "decimal",
	"float":    "decimal",
	"integer":  "integer",
	"int":      "integer",
	"boolean":  "boolean",
	"bool":     "boolean",
	"date":     "date",
	"datetime": "dateTime",
	// enum：枚举型属性在 RDF 里仍是字符串字面量 —— 取值集合由形状里的 sh:in 约束
	// 表达，而不是由 datatype 表达 —— 因此映射到 xsd:string。
	// 契约侧（ontologyManifestSchema 的 type 枚举、本文件的错误提示、生成提示词）
	// 都把 enum 列为合法取值，实现必须同样接受，否则就是"契约允许、装载拒绝"。
	"enum": "string",
}

// xmlnsIRI 把类型声明解析为 XSD datatype IRI。
func xsdIRI(declared string) (string, bool) {
	s := strings.TrimSpace(declared)
	if s == "" {
		return "", false
	}
	if strings.Contains(s, "://") {
		return s, true
	}
	local, ok := xsdShorthand[strings.ToLower(s)]
	if !ok {
		return "", false
	}
	return xsdNamespace + local, true
}

// ---------------------------------------------------------------------------
// 命名空间
// ---------------------------------------------------------------------------

// Namespace 是本体的词项 IRI 命名空间（形如 https://odap.example/ont/<key>#）。
type Namespace struct {
	base string
}

// NewNamespace 由本体键构造命名空间。
//
// ontology_key 限定为 IRI 安全字符集：本体键参与词项 IRI 的构造，键里出现空格或
// 中文会让 IRI 需要转义，而"转义后的 IRI"与"未转义的 IRI"会被当成两个词项——
// 这类重复不报错，只会让图里出现两份等价但不相交的数据。因此此处显式拒绝，
// 并提示调用方改用 ASCII 键，而不是静默转义。
func NewNamespace(ontologyKey string) (Namespace, error) {
	k := strings.TrimSpace(ontologyKey)
	if k == "" {
		return Namespace{}, errors.E(errors.CodeValidationFailed, "graphload: ontology_key 不能为空")
	}
	for _, r := range k {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return Namespace{}, errors.Ef(errors.CodeValidationFailed,
				"graphload: ontology_key 只能包含 ASCII 字母、数字、'-'、'_'、'.'，实际为 %q（词项 IRI 的构造依赖它）", ontologyKey)
		}
	}
	return Namespace{base: graphIRIRoot + k + "#"}, nil
}

// Base 返回命名空间前缀。
func (n Namespace) Base() string { return n.base }

// ClassIRI 返回类的 IRI。
func (n Namespace) ClassIRI(class string) string { return n.base + class }

// RelationIRI 返回关系（全局谓词）的 IRI。
func (n Namespace) RelationIRI(relation string) string { return n.base + relation }

// AttributeIRI 返回属性的 IRI。
//
// 属性谓词**按类限定**（<ns><Class>.<attr>）而非全局同名：属性在清单里嵌套在类下，
// 若两个类各有同名属性却共用一个谓词，rdfs:domain 就会同时落在两个类上，进而让
// RDFS 规则 rdfs2 推出"该属性的主语同时属于两个类"——一个语义上错误、且不会报错的
// 结论。按类限定后，domain 唯一，推理可靠。
func (n Namespace) AttributeIRI(class, attribute string) string {
	return n.base + class + "." + attribute
}

// ShapeIRI 返回形状的 IRI。
func (n Namespace) ShapeIRI(shapeID string) string { return n.base + "shape/" + shapeID }

// InstanceIRI 返回实例的 IRI。
func (n Namespace) InstanceIRI(id string) string { return n.base + instancePatHint + id }

// GraphIRI 返回某本体版本的数据命名图 IRI（调用方视角）。
//
// 按 (ontology_key, version_id) 确定性派生：同一个版本重复校验落在同一张图上，
// 因此 InsertTriples 的幂等去重（ON CONFLICT DO NOTHING）天然生效，无需额外的
// "图标识"列，也不需要区分"首次装载"与"重新校验"两条路径。
func GraphIRI(ontologyKey, versionID string) (string, error) {
	ns, err := NewNamespace(ontologyKey)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(versionID) == "" {
		return "", errors.E(errors.CodeValidationFailed, "graphload: version_id 不能为空")
	}
	// 去掉命名空间末尾的 '#'，避免图 IRI 里出现裸 '#'（会被当作片段标识）。
	return ns.Base()[:len(ns.Base())-1] + "/" + versionID, nil
}

// ---------------------------------------------------------------------------
// 清单的强类型投影
// ---------------------------------------------------------------------------

// Manifest 是本体清单的强类型投影，与生成端 ontologyManifestSchema 一一对应。
type Manifest struct {
	Summary      string           `json:"summary"`
	Classes      []Class          `json:"classes"`
	Relations    []Relation       `json:"relations"`
	Enumerations []Enumeration    `json:"enumerations"`
	Shapes       []ShapeSpec      `json:"shapes"`
	Constraints  []ConstraintSpec `json:"constraints"`
	Instances    []Instance       `json:"instances"`

	// 以下索引由 ParseManifest 建立，供闭包校验与投影复用。
	attrIndex map[string]map[string]Attribute // class -> attribute -> 声明
	relIndex  map[string]Relation             // relation 名 -> 声明
	instIndex map[string]string               // instance id -> class
}

// Class 是一个业务类。
type Class struct {
	Name        string      `json:"name"`
	Label       string      `json:"label"`
	Description string      `json:"description"`
	Attributes  []Attribute `json:"attributes"`
}

// Attribute 是类的属性（字面值槽位）。
type Attribute struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// Relation 是类之间的关系（对象属性）。
type Relation struct {
	Name        string `json:"name"`
	FromClass   string `json:"from_class"`
	ToClass     string `json:"to_class"`
	Cardinality string `json:"cardinality"`
	Description string `json:"description"`
}

// Enumeration 是一个取值集合。
type Enumeration struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

// ShapeSpec 是 SHACL 形状的清单形态（结构化，不是 Turtle）。
type ShapeSpec struct {
	ID          string         `json:"id"`
	TargetClass string         `json:"target_class"`
	Severity    string         `json:"severity"`
	Properties  []PropertySpec `json:"properties"`
}

// PropertySpec 是 SHACL 属性形状的清单形态。
//
// 字段刻意与 shacl.PropertyShape 同名同义（下划线风格），避免"清单里叫一个名字、
// 校验时叫另一个名字"造成的翻译层错误。
type PropertySpec struct {
	Path         string   `json:"path"`
	MinCount     *int     `json:"min_count"`
	MaxCount     *int     `json:"max_count"`
	Datatype     string   `json:"datatype"`
	Class        string   `json:"class"`
	NodeKind     string   `json:"node_kind"`
	In           []string `json:"in"`
	MinInclusive *float64 `json:"min_inclusive"`
	MaxInclusive *float64 `json:"max_inclusive"`
	MinExclusive *float64 `json:"min_exclusive"`
	MaxExclusive *float64 `json:"max_exclusive"`
	Pattern      string   `json:"pattern"`
	MinLength    *int     `json:"min_length"`
	MaxLength    *int     `json:"max_length"`
	Severity     string   `json:"severity"`
}

// ConstraintSpec 是业务硬约束（文档抽取通道，借鉴竞品 shaclConstraints 设计）。
//
// shapes 通道让模型自己搭形状骨架；constraints 通道让它把文档中"数值区间/枚举/
// 长度/必填/正则"这类**可逐字验证**的业务硬约束单独列出。两条通道在 BuildShapes
// 合并（业务优先覆盖），约束指向的类没有显式形状时自动补形状——约束绝不静默丢弃。
//
// 与 PropertySpec 的差异：path 按「类 + 名字」解析（ODAP 属性谓词按类限定，
// 全局同名会歧义），且不含 datatype/class/node_kind（类型与取值形态由类声明
// 确定性给出，模型重复声明只会引入与声明矛盾的可能）。
type ConstraintSpec struct {
	Class        string   `json:"class"`
	Path         string   `json:"path"`
	Severity     string   `json:"severity"`
	MinCount     *int     `json:"min_count"`
	MaxCount     *int     `json:"max_count"`
	In           []string `json:"in"`
	MinInclusive *float64 `json:"min_inclusive"`
	MaxInclusive *float64 `json:"max_inclusive"`
	MinExclusive *float64 `json:"min_exclusive"`
	MaxExclusive *float64 `json:"max_exclusive"`
	Pattern      string   `json:"pattern"`
	MinLength    *int     `json:"min_length"`
	MaxLength    *int     `json:"max_length"`
}

// Instance 是样例实例（ABox）。Properties 的取值形态由属性/关系声明决定。
type Instance struct {
	Class      string         `json:"class"`
	ID         string         `json:"id"`
	Properties map[string]any `json:"properties"`
}

// ---------------------------------------------------------------------------
// 解析与闭包校验
// ---------------------------------------------------------------------------

// ParseManifest 解析并校验本体清单。任何结构或闭包问题都返回 422 级别的错误。
//
// 未知字段一律拒绝（DisallowUnknownFields）：生成端的 JSON Schema 已声明
// additionalProperties=false，若这里宽容接受，就会出现"契约说不允许、实现默默接受"
// 的漂移。宁可让模型重试，也不要让一份不合契约的清单进入数据库。
func ParseManifest(raw []byte) (*Manifest, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.E(errors.CodeValidationFailed, "graphload: manifest 不能为空")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, errors.Wrap(err, errors.CodeValidationFailed, "graphload: manifest 结构与约定不符")
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// validate 做结构与闭包校验，并建立索引。
func (m *Manifest) validate() error {
	if len(m.Classes) == 0 {
		return errors.E(errors.CodeValidationFailed, "graphload: classes 不能为空")
	}
	m.attrIndex = make(map[string]map[string]Attribute, len(m.Classes))

	// --- 类 ---
	for i := range m.Classes {
		c := &m.Classes[i]
		c.Name = strings.TrimSpace(c.Name)
		if c.Name == "" {
			return jsonPointer("classes", i, "name", "类名为空")
		}
		if !isLocalName(c.Name) {
			return jsonPointer("classes", i, "name", fmt.Sprintf("类名 %q 不是合法的词项局部名（只允许字母、数字、'-'、'_'）", c.Name))
		}
		if _, dup := m.attrIndex[c.Name]; dup {
			return jsonPointer("classes", i, "name", fmt.Sprintf("类名重复：%s", c.Name))
		}
		attrs := make(map[string]Attribute, len(c.Attributes))
		for j := range c.Attributes {
			a := &c.Attributes[j]
			a.Name = strings.TrimSpace(a.Name)
			if a.Name == "" {
				return jsonPointer(fmt.Sprintf("classes/%d/attributes", i), j, "name", "属性名为空")
			}
			if !isLocalName(a.Name) {
				return jsonPointer(fmt.Sprintf("classes/%d/attributes", i), j, "name",
					fmt.Sprintf("属性名 %q 不是合法的词项局部名", a.Name))
			}
			if _, dup := attrs[a.Name]; dup {
				return jsonPointer(fmt.Sprintf("classes/%d/attributes", i), j, "name",
					fmt.Sprintf("类 %s 的属性名重复：%s", c.Name, a.Name))
			}
			if _, ok := xsdIRI(a.Type); !ok {
				return jsonPointer(fmt.Sprintf("classes/%d/attributes", i), j, "type",
					fmt.Sprintf("属性类型 %q 不在契约枚举内（string/number/integer/boolean/date/datetime/enum 或完整 XSD IRI）", a.Type))
			}
			attrs[a.Name] = *a
		}
		m.attrIndex[c.Name] = attrs
	}

	// --- 关系 ---
	m.relIndex = make(map[string]Relation, len(m.Relations))
	for i := range m.Relations {
		r := &m.Relations[i]
		r.Name = strings.TrimSpace(r.Name)
		r.FromClass = strings.TrimSpace(r.FromClass)
		r.ToClass = strings.TrimSpace(r.ToClass)
		if r.Name == "" {
			return jsonPointer("relations", i, "name", "关系名为空")
		}
		if !isLocalName(r.Name) {
			return jsonPointer("relations", i, "name", fmt.Sprintf("关系名 %q 不是合法的词项局部名", r.Name))
		}
		// 同一谓词只能有一组 domain/range：否则 rdfs:domain 会出现多个取值，
		// RDFS 规则 rdfs2 将推出互相矛盾的类归属。
		if _, dup := m.relIndex[r.Name]; dup {
			return jsonPointer("relations", i, "name",
				fmt.Sprintf("关系名重复：%s（同一谓词只能声明一组 from_class/to_class）", r.Name))
		}
		if _, ok := m.attrIndex[r.FromClass]; !ok {
			return jsonPointer("relations", i, "from_class",
				fmt.Sprintf("from_class 未在 classes 中定义：%s", r.FromClass))
		}
		if _, ok := m.attrIndex[r.ToClass]; !ok {
			return jsonPointer("relations", i, "to_class",
				fmt.Sprintf("to_class 未在 classes 中定义：%s", r.ToClass))
		}
		m.relIndex[r.Name] = *r
	}

	// 关系名与属性名不得冲突：解析规则是"先查关系、再查属性"，冲突会让同名属性的
	// 形状路径被静默解析成关系谓词。显式拒绝比隐式优先级更安全。
	//
	// 这里按**下标**遍历而不是遍历 attrIndex 这个 map：map 的遍历顺序在 Go 里是随机的，
	// 一份含多处冲突的清单会随进程启动时间给出不同的 JSON Pointer——前端据此定位时
	// 会时而指向这里、时而指向那里。校验必须可复现。
	for i := range m.Classes {
		for j := range m.Classes[i].Attributes {
			attrName := m.Classes[i].Attributes[j].Name
			if _, clash := m.relIndex[attrName]; clash {
				return errors.Ef(errors.CodeValidationFailed,
					"graphload: 属性名 %q（类 %s）与同名关系冲突；属性与关系共用命名空间，"+
						"请改名（如 %s → %s_attr）", attrName, m.Classes[i].Name, attrName, attrName).
					WithDetails(map[string]any{
						"pointer": fmt.Sprintf("/classes/%d/attributes/%s", i, attrName),
					})
			}
		}
	}

	// --- 形状 ---
	seenShapeID := make(map[string]bool, len(m.Shapes))
	for i := range m.Shapes {
		sh := &m.Shapes[i]
		sh.ID = strings.TrimSpace(sh.ID)
		if sh.ID == "" {
			sh.ID = fmt.Sprintf("shape-%d", i+1)
		}
		if !isLocalName(sh.ID) {
			return jsonPointer("shapes", i, "id", fmt.Sprintf("形状 id %q 不是合法的词项局部名", sh.ID))
		}
		// "auto-" 前缀保留给约束派生形状（见 mergedShapeSpecs）：显式形状占用它
		// 会与自动补建的形状在 ShapeIRI 上相撞。
		if strings.HasPrefix(sh.ID, "auto-") {
			return jsonPointer("shapes", i, "id",
				fmt.Sprintf("形状 id %q 占用了保留前缀 auto-（该前缀为业务约束自动派生的形状保留），请改名", sh.ID))
		}
		if seenShapeID[sh.ID] {
			return jsonPointer("shapes", i, "id", fmt.Sprintf("形状 id 重复：%s", sh.ID))
		}
		seenShapeID[sh.ID] = true

		sh.TargetClass = strings.TrimSpace(sh.TargetClass)
		if sh.TargetClass == "" {
			return jsonPointer("shapes", i, "target_class", "target_class 必填（没有目标类的形状不产生任何约束）")
		}
		if _, ok := m.attrIndex[sh.TargetClass]; !ok {
			return jsonPointer("shapes", i, "target_class", fmt.Sprintf("target_class 未在 classes 中定义：%s", sh.TargetClass))
		}
		if _, err := parseSeverity(sh.Severity, "Violation"); err != nil {
			return jsonPointer("shapes", i, "severity", err.Error())
		}
		if len(sh.Properties) == 0 {
			return jsonPointer("shapes", i, "properties", "properties 不能为空：空形状不产生任何约束，等于声称校验过")
		}
		for j := range sh.Properties {
			p := &sh.Properties[j]
			if _, err := m.ResolveTerm(sh.TargetClass, p.Path); err != nil {
				return jsonPointer(fmt.Sprintf("shapes/%d/properties", i), j, "path", err.Error())
			}
			if _, err := parseSeverity(p.Severity, ""); err != nil {
				return jsonPointer(fmt.Sprintf("shapes/%d/properties", i), j, "severity", err.Error())
			}
			if p.Datatype != "" {
				if _, ok := xsdIRI(p.Datatype); !ok {
					return jsonPointer(fmt.Sprintf("shapes/%d/properties", i), j, "datatype",
						fmt.Sprintf("sh:datatype %q 不在契约枚举内", p.Datatype))
				}
			}
			if p.Class != "" {
				if _, ok := m.attrIndex[p.Class]; !ok {
					return jsonPointer(fmt.Sprintf("shapes/%d/properties", i), j, "class",
						fmt.Sprintf("sh:class 引用的类未在 classes 中定义：%s", p.Class))
				}
			}
			if p.NodeKind != "" {
				switch shacl.NodeKind(p.NodeKind) {
				case shacl.NodeKindIRI, shacl.NodeKindBlankNode, shacl.NodeKindLiteral:
				default:
					return jsonPointer(fmt.Sprintf("shapes/%d/properties", i), j, "node_kind",
						fmt.Sprintf("sh:nodeKind %q 不在 IRI/BlankNode/Literal 之内", p.NodeKind))
				}
			}
		}
	}

	// --- 业务约束（文档抽取通道）---
	//
	// 这里只做**结构与可满足性**校验，不重复实现取值校验（那是 SHACL 的职责）。
	// 但"约束本身就是不可满足的"（min>max、正则编译不过、数值区间用在字符串上）
	// 属于结构错误：放到校验阶段才报，根因已隔了一层，排查成本高；且一条不可满足
	// 的约束会让所有合规数据都被判违规——这比没有约束更危险。
	for i := range m.Constraints {
		c := &m.Constraints[i]
		c.Class = strings.TrimSpace(c.Class)
		c.Path = strings.TrimSpace(c.Path)
		if c.Class == "" {
			return jsonPointer("constraints", i, "class", "class 必填（约束必须指明挂在哪个类上）")
		}
		if _, ok := m.attrIndex[c.Class]; !ok {
			return jsonPointer("constraints", i, "class", fmt.Sprintf("class 未在 classes 中定义：%s", c.Class))
		}
		ref, err := m.ResolveTerm(c.Class, c.Path)
		if err != nil {
			return jsonPointer(fmt.Sprintf("constraints/%d", i), -1, "path", err.Error())
		}
		if _, err := parseSeverity(c.Severity, "Violation"); err != nil {
			return jsonPointer(fmt.Sprintf("constraints/%d", i), -1, "severity", err.Error())
		}
		if c.MinCount != nil && c.MaxCount != nil && *c.MinCount > *c.MaxCount {
			return jsonPointer(fmt.Sprintf("constraints/%d", i), -1, "min_count",
				fmt.Sprintf("min_count=%d 大于 max_count=%d：约束不可满足", *c.MinCount, *c.MaxCount))
		}
		if c.MinLength != nil && c.MaxLength != nil && *c.MinLength > *c.MaxLength {
			return jsonPointer(fmt.Sprintf("constraints/%d", i), -1, "min_length",
				fmt.Sprintf("min_length=%d 大于 max_length=%d：约束不可满足", *c.MinLength, *c.MaxLength))
		}
		if c.MinInclusive != nil && c.MaxInclusive != nil && *c.MinInclusive > *c.MaxInclusive {
			return jsonPointer(fmt.Sprintf("constraints/%d", i), -1, "min_inclusive",
				fmt.Sprintf("min_inclusive=%v 大于 max_inclusive=%v：区间为空，约束不可满足",
					*c.MinInclusive, *c.MaxInclusive))
		}
		if c.MinExclusive != nil && c.MaxExclusive != nil && *c.MinExclusive >= *c.MaxExclusive {
			return jsonPointer(fmt.Sprintf("constraints/%d", i), -1, "min_exclusive",
				fmt.Sprintf("min_exclusive=%v 不小于 max_exclusive=%v：开区间为空，约束不可满足",
					*c.MinExclusive, *c.MaxExclusive))
		}
		if c.Pattern != "" {
			if _, err := regexp.Compile(c.Pattern); err != nil {
				return jsonPointer(fmt.Sprintf("constraints/%d", i), -1, "pattern",
					fmt.Sprintf("pattern %q 不是合法正则：%v", c.Pattern, err))
			}
		}
		if len(c.In) == 1 && c.In[0] == "" {
			return jsonPointer(fmt.Sprintf("constraints/%d", i), -1, "in", "in 的取值不能是空字符串")
		}
		for _, v := range c.In {
			if strings.TrimSpace(v) == "" {
				return jsonPointer(fmt.Sprintf("constraints/%d", i), -1, "in", "in 的取值不能是空白字符串")
			}
		}

		// 类型相容矩阵：约束种类必须与目标槽位的声明类型相容。
		// 数值区间作用在文本上、正则作用在数值上，都会造出"永远违规"或"永远不触发"
		// 的约束——两者都是静默失败，必须在解析期拦下。
		field := fmt.Sprintf("constraints/%d", i)
		if !ref.IsRelation {
			attrType := m.attrIndex[c.Class][ref.Name].Type
			switch attrType {
			case "number", "integer":
				if c.Pattern != "" {
					return jsonPointer(field, -1, "pattern",
						fmt.Sprintf("pattern 只能用于文本属性，但 %s.%s 的类型是 %s（数值请用 min_inclusive/max_inclusive）", c.Class, ref.Name, attrType))
				}
				if c.MinLength != nil || c.MaxLength != nil {
					return jsonPointer(field, -1, "min_length",
						fmt.Sprintf("长度约束只能用于文本属性，但 %s.%s 的类型是 %s", c.Class, ref.Name, attrType))
				}
				if len(c.In) > 0 {
					return jsonPointer(field, -1, "in",
						fmt.Sprintf("in（文本枚举）不能用于数值属性 %s.%s（数值请用 min_inclusive/max_inclusive）", c.Class, ref.Name))
				}
			case "boolean":
				if c.Pattern != "" || c.MinLength != nil || c.MaxLength != nil || len(c.In) > 0 ||
					c.MinInclusive != nil || c.MaxInclusive != nil || c.MinExclusive != nil || c.MaxExclusive != nil {
					return jsonPointer(field, -1, "path",
						fmt.Sprintf("布尔属性 %s.%s 只接受 min_count/max_count 约束", c.Class, ref.Name))
				}
			default: // string / enum / date / datetime
				if c.MinInclusive != nil || c.MaxInclusive != nil || c.MinExclusive != nil || c.MaxExclusive != nil {
					return jsonPointer(field, -1, "min_inclusive",
						fmt.Sprintf("数值区间只能用于 number/integer 属性，但 %s.%s 的类型是 %s", c.Class, ref.Name, attrType))
				}
			}
		} else {
			// 关系槽位：class/node_kind 由关系声明确定性给出，约束只允许管出现次数。
			if c.Pattern != "" || c.MinLength != nil || c.MaxLength != nil || len(c.In) > 0 ||
				c.MinInclusive != nil || c.MaxInclusive != nil || c.MinExclusive != nil || c.MaxExclusive != nil {
				return jsonPointer(field, -1, "path",
					fmt.Sprintf("关系 %s 只接受 min_count/max_count 约束（取值形态由 relations 声明决定）", ref.Name))
			}
		}
	}

	// --- 实例（闭包校验：引用必须存在，且关系的两端类必须吻合）---
	m.instIndex = make(map[string]string, len(m.Instances))
	for i := range m.Instances {
		in := &m.Instances[i]
		in.Class = strings.TrimSpace(in.Class)
		in.ID = strings.TrimSpace(in.ID)
		if in.ID == "" {
			return jsonPointer("instances", i, "id", "实例 id 为空")
		}
		if !isLocalName(in.ID) {
			return jsonPointer("instances", i, "id", fmt.Sprintf("实例 id %q 不是合法的词项局部名", in.ID))
		}
		if _, dup := m.instIndex[in.ID]; dup {
			return jsonPointer("instances", i, "id", fmt.Sprintf("实例 id 重复：%s（实例 id 全局唯一，构成 IRI）", in.ID))
		}
		if _, ok := m.attrIndex[in.Class]; !ok {
			return jsonPointer("instances", i, "class", fmt.Sprintf("class 未在 classes 中定义：%s", in.Class))
		}
		m.instIndex[in.ID] = in.Class
	}
	// 第二轮：属性键与关系引用（需要先建好全量实例索引，支持前向引用）。
	for i := range m.Instances {
		in := &m.Instances[i]
		for key, raw := range in.Properties {
			ref, err := m.ResolveTerm(in.Class, key)
			if err != nil {
				return jsonPointer(fmt.Sprintf("instances/%d/properties", i), -1, key, err.Error())
			}
			if !ref.IsRelation {
				if _, err := literalFor(m.attrIndex[in.Class][ref.Name].Type, raw); err != nil {
					return jsonPointer(fmt.Sprintf("instances/%d/properties", i), -1, key,
						fmt.Sprintf("类 %s 的属性 %s：%s", in.Class, ref.Name, err.Error()))
				}
				continue
			}
			rel := m.relIndex[ref.Name]
			// from 端同样必须吻合：ResolveTerm 按全局谓词名解析关系（先关系后属性），
			// 任何类的实例都能解析到关系名。若不校验主体归属，模型把关系方向写反
			// （在目标类一侧的实例上写出这条关系）只会在客体校验处得到一条难以自察
			// 的报错，重生成回路也拿不到"方向错了"这个关键信号。
			if rel.FromClass != in.Class {
				return jsonPointer(fmt.Sprintf("instances/%d/properties", i), -1, key,
					fmt.Sprintf("关系 %s 只能从类 %s 出发（from_class），但实例 %s 属于 %s（关系方向可能写反了：请在 from_class 对应的实例上使用该关系，或修正 relations 里的 from_class/to_class 声明）",
						ref.Name, rel.FromClass, in.ID, in.Class))
			}
			for _, target := range asStringList(raw) {
				targetClass, ok := m.instIndex[target]
				if !ok {
					return jsonPointer(fmt.Sprintf("instances/%d/properties", i), -1, key,
						fmt.Sprintf("关系 %s 指向的实例不存在：%s（闭包校验：所有跨实例引用必须可解析）", ref.Name, target))
				}
				if targetClass != rel.ToClass {
					return jsonPointer(fmt.Sprintf("instances/%d/properties", i), -1, key,
						fmt.Sprintf("关系 %s 的 to_class 是 %s，但实例 %s 属于 %s", ref.Name, rel.ToClass, target, targetClass))
				}
			}
		}
	}
	return nil
}

// TermRef 是"类 + 名字"解析结果：既可能是关系（全局谓词），也可能是属性（按类限定的谓词）。
type TermRef struct {
	// IsRelation 为 true 表示解析到关系；false 表示解析到该类自身的属性。
	IsRelation bool
	// Name 是清单中的名字（关系的全局名，或属性的局部名）。
	Name string
}

// ResolveTerm 把「类 + 名字」解析为一个谓词引用。
//
// 解析顺序固定为"先关系、后属性"，且**不靠猜**：清单里已显式声明了两者的归属。
// 由于属性与关系重名会在 validate 阶段被拒绝，这里的顺序不会产生歧义。
// 形状路径与实例属性键共用本函数，因此两者对同一个名字的解释**必然一致**。
func (m *Manifest) ResolveTerm(class, name string) (TermRef, error) {
	n := strings.TrimSpace(name)
	if n == "" {
		return TermRef{}, errors.E(errors.CodeValidationFailed, "graphload: 谓词名为空")
	}
	if strings.Contains(n, "://") {
		return TermRef{}, errors.Ef(errors.CodeValidationFailed,
			"graphload: 不接受绝对 IRI 谓词 %q —— 词项 IRI 由平台按 ontology_key 统一构造，"+
				"手工 IRI 会造出与平台命名空间不相交的第二套词项", n)
	}
	if _, ok := m.relIndex[n]; ok {
		return TermRef{IsRelation: true, Name: n}, nil
	}
	if attrs, ok := m.attrIndex[class]; ok {
		if _, ok := attrs[n]; ok {
			return TermRef{Name: n}, nil
		}
	}
	return TermRef{}, errors.Ef(errors.CodeValidationFailed,
		"graphload: 名字 %q 既不是已声明的关系，也不是类 %s 的属性", n, class)
}

// isLocalName 判断字符串能否直接作为 IRI 的局部名（无需转义）。
func isLocalName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// jsonPointer 组装带可定位路径的错误（RFC 6901，前端据此把问题指到具体字段）。
//
// container 以 '/' 分隔路径片段（如 "classes/0/attributes"）；index ≥ 0 时追加为
// 一段下标；最后追加 field。每段单独做转义，转义发生在拼接之前——顺序反过来会把
// 用作分隔符的 '/' 一并转义掉，产出一个虽以 "/" 开头、却指向错误位置的指针。
func jsonPointer(container string, index int, field, msg string) error {
	segments := make([]string, 0, 6)
	for _, s := range strings.Split(container, "/") {
		if s != "" {
			segments = append(segments, s)
		}
	}
	if index >= 0 {
		segments = append(segments, strconv.Itoa(index))
	}
	if field != "" {
		segments = append(segments, field)
	}
	escaped := make([]string, 0, len(segments))
	for _, s := range segments {
		escaped = append(escaped, strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1"))
	}
	return errors.E(errors.CodeValidationFailed, "graphload: "+msg).
		WithDetails(map[string]any{"pointer": "/" + strings.Join(escaped, "/")})
}

// asStringList 把 JSON 值归一为字符串列表（单值 / 列表两种形态都接受）。
func asStringList(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// literalFor 按**声明的属性类型**把 JSON 值转为字面值词项。
//
// 严格按声明类型转换，而不是"按 JSON 值的形状猜类型"：后者会让 xsd:decimal 的槽位
// 收到一个 xsd:string 字面值，随后 sh:datatype 校验失败——错误出现在校验阶段，
// 而根因在装载阶段，排查成本高得多。此处立即拒绝并指明字段。
//
// 注意边界：本函数只保证"字面值的类型与词法形式一致"，**不校验取值约束**
// （日期格式、长度、区间、正则），那些由 SHACL 负责。
func literalFor(declaredType string, v any) (graphstore.Term, error) {
	dt, ok := xsdIRI(declaredType)
	if !ok {
		return graphstore.Term{}, errors.Ef(errors.CodeValidationFailed, "属性类型 %q 不在契约枚举内", declaredType)
	}
	if v == nil {
		return graphstore.Term{}, errors.E(errors.CodeValidationFailed, "属性值为 null（缺值应直接省略该键）")
	}
	lit := func(lexical string) graphstore.Term {
		return graphstore.Term{Kind: graphstore.KindLiteral, Lexical: lexical, Datatype: dt}
	}

	switch dt {
	case xsdNamespace + "boolean":
		switch t := v.(type) {
		case bool:
			return lit(strconv.FormatBool(t)), nil
		case string:
			s := strings.TrimSpace(t)
			if s == "true" || s == "false" {
				return lit(s), nil
			}
		}
		return graphstore.Term{}, errors.Ef(errors.CodeValidationFailed,
			"期望布尔值，实际为 %s", describeJSON(v))

	case xsdNamespace + "decimal", xsdNamespace + "integer":
		switch t := v.(type) {
		case float64:
			if math.IsNaN(t) || math.IsInf(t, 0) {
				return graphstore.Term{}, errors.E(errors.CodeValidationFailed, "数值不是有限数")
			}
			if dt == xsdNamespace+"integer" && t != math.Trunc(t) {
				return graphstore.Term{}, errors.Ef(errors.CodeValidationFailed,
					"期望整数（xsd:integer），实际为 %v", t)
			}
			return lit(strconv.FormatFloat(t, 'f', -1, 64)), nil
		case string:
			s := strings.TrimSpace(t)
			if _, err := strconv.ParseFloat(s, 64); err != nil {
				return graphstore.Term{}, errors.Ef(errors.CodeValidationFailed,
					"期望数值，实际字符串 %q 不能解析为数字", t)
			}
			return lit(s), nil
		}
		return graphstore.Term{}, errors.Ef(errors.CodeValidationFailed,
			"期望数值，实际为 %s", describeJSON(v))

	default: // xsd:string / xsd:date / xsd:dateTime 以及完整 IRI 声明的其它类型
		if s, ok := v.(string); ok {
			return lit(s), nil
		}
		return graphstore.Term{}, errors.Ef(errors.CodeValidationFailed,
			"期望字符串（%s），实际为 %s", dt, describeJSON(v))
	}
}

// describeJSON 给出值的类型描述（用于错误信息，不泄漏大对象）。
func describeJSON(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "字符串"
	case float64:
		return "数字"
	case bool:
		return "布尔"
	case []any:
		return "数组"
	case map[string]any:
		return "对象"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// parseSeverity 解析 sh:severity；为空时返回 fallback。
func parseSeverity(raw string, fallback shacl.Severity) (shacl.Severity, error) {
	s := shacl.Severity(strings.TrimSpace(raw))
	if s == "" {
		s = fallback
	}
	switch s {
	case "":
		return "", nil
	case shacl.SeverityViolation, shacl.SeverityWarning, shacl.SeverityInfo:
		return s, nil
	default:
		return "", fmt.Errorf("sh:severity %q 不在 Violation/Warning/Info 之内", raw)
	}
}

// ---------------------------------------------------------------------------
// 投影：TBox / ABox / Shapes
// ---------------------------------------------------------------------------

// TBox 生成本体层（术语层）三元组。
func (m *Manifest) TBox(ns Namespace) ([]graphstore.Triple, error) {
	triples := make([]graphstore.Triple, 0, len(m.Classes)*4+len(m.Relations)*3)
	iri := func(s string) graphstore.Term { return graphstore.Term{Kind: graphstore.KindIRI, Lexical: s} }
	add := func(s, p, o string) {
		triples = append(triples, graphstore.Triple{S: iri(s), P: iri(p), O: iri(o)})
	}
	addLit := func(s, p, lexical string) {
		triples = append(triples, graphstore.Triple{S: iri(s), P: iri(p),
			O: graphstore.Term{Kind: graphstore.KindLiteral, Lexical: lexical, Datatype: xsdNamespace + "string"}})
	}

	for _, c := range m.Classes {
		classIRI := ns.ClassIRI(c.Name)
		add(classIRI, iriRDFType, iriRDFSClass)
		if label := strings.TrimSpace(c.Label); label != "" {
			addLit(classIRI, iriRDFSLabel, label)
		}
		attrs := m.attrIndex[c.Name]
		for _, a := range c.Attributes {
			attrIRI := ns.AttributeIRI(c.Name, a.Name)
			add(attrIRI, iriRDFType, iriRDFProperty)
			add(attrIRI, iriRDFSDomain, classIRI)
			// 属性是字面值槽位：range 取声明的 XSD 类型。
			// （rdfs3 会把 range 反推为主语的类型，但它显式排除字面值，因此不会产生噪声。）
			dt, _ := xsdIRI(attrs[a.Name].Type)
			add(attrIRI, iriRDFSRange, dt)
		}
	}
	for _, r := range m.Relations {
		relIRI := ns.RelationIRI(r.Name)
		add(relIRI, iriRDFType, iriRDFProperty)
		add(relIRI, iriRDFSDomain, ns.ClassIRI(r.FromClass))
		add(relIRI, iriRDFSRange, ns.ClassIRI(r.ToClass))
	}
	return triples, nil
}

// ABox 生成实例层三元组（样例数据）。
//
// 闭包已由 ParseManifest 保证，因此此处不再失败——重复校验同一份清单不会
// 在校验阶段才暴露装载阶段就已知的问题。
func (m *Manifest) ABox(ns Namespace) ([]graphstore.Triple, error) {
	triples := make([]graphstore.Triple, 0, len(m.Instances)*3)
	iri := func(s string) graphstore.Term { return graphstore.Term{Kind: graphstore.KindIRI, Lexical: s} }

	for _, in := range m.Instances {
		subj := ns.InstanceIRI(in.ID)
		triples = append(triples, graphstore.Triple{
			S: iri(subj), P: iri(iriRDFType), O: iri(ns.ClassIRI(in.Class)),
		})
		for key, raw := range in.Properties {
			ref, err := m.ResolveTerm(in.Class, key)
			if err != nil {
				return nil, err
			}
			if ref.IsRelation {
				for _, target := range asStringList(raw) {
					triples = append(triples, graphstore.Triple{
						S: iri(subj), P: iri(ns.RelationIRI(ref.Name)), O: iri(ns.InstanceIRI(target)),
					})
				}
				continue
			}
			lit, err := literalFor(m.attrIndex[in.Class][ref.Name].Type, raw)
			if err != nil {
				return nil, err
			}
			triples = append(triples, graphstore.Triple{
				S: iri(subj), P: iri(ns.AttributeIRI(in.Class, ref.Name)), O: lit,
			})
		}
	}
	return triples, nil
}

// BuildShapes 把清单里的形状投影为 SHACL 形状。
//
// 投影前先把业务约束（constraints 通道）合并进形状清单：业务字段优先覆盖显式形状
// 的同名字段，约束指向的类没有显式形状时自动补一个。Keywords 如实列出**实际设置
// 了值**的关键字：shacl.Validate 对清单外关键字 fail-closed 拒绝，若这里凭空多列会
// 误拒，漏列则会让门禁形同虚设。
func (m *Manifest) BuildShapes(ns Namespace) ([]shacl.Shape, error) {
	specs := m.mergedShapeSpecs()
	out := make([]shacl.Shape, 0, len(specs))
	for i := range specs {
		spec := &specs[i]
		sev, err := parseSeverity(spec.Severity, shacl.SeverityViolation)
		if err != nil {
			return nil, err
		}
		sh := shacl.Shape{
			ID:          ns.ShapeIRI(spec.ID),
			TargetClass: []string{ns.ClassIRI(spec.TargetClass)},
			Severity:    sev,
			Keywords:    []string{"NodeShape", "targetClass", "property", "severity"},
		}
		for j := range spec.Properties {
			ps, err := m.propertyShape(ns, spec.TargetClass, &spec.Properties[j])
			if err != nil {
				return nil, err
			}
			sh.Properties = append(sh.Properties, ps)
		}
		out = append(out, sh)
	}
	return out, nil
}

// mergedShapeSpecs 把业务约束合并进形状清单，返回合并后的规格副本。
//
// 副本语义刻意明确：绝不改写 m.Shapes 原始声明——合并是投影期的派生行为，
// 原始清单（落库的那份字节）必须保持模型产出的原样，否则"重新校验"会拿到
// 一份被上一次投影污染过的清单。
//
// 约束合并规则（借鉴竞品双通道设计，取其长、补其短）：
//   - 同类同路径：业务约束的**非空字段**覆盖显式形状的同名字段（业务约束来自
//     文档逐字抽取，比模型自己搭的骨架更贴近原意）；
//   - 同类不同路径：约束作为新的属性形状追加；
//   - 该类没有显式形状：自动补一个（ID 用保留前缀 auto-，validate 已禁止显式
//     形状占用）。约束绝不因"没有形状可挂"而静默丢失。
func (m *Manifest) mergedShapeSpecs() []ShapeSpec {
	specs := make([]ShapeSpec, len(m.Shapes))
	copy(specs, m.Shapes)
	byClass := make(map[string]int, len(specs))
	for i := range specs {
		byClass[specs[i].TargetClass] = i
	}
	for i := range m.Constraints {
		c := &m.Constraints[i]
		ps := PropertySpec{
			Path:         c.Path,
			MinCount:     c.MinCount,
			MaxCount:     c.MaxCount,
			In:           c.In,
			MinInclusive: c.MinInclusive,
			MaxInclusive: c.MaxInclusive,
			MinExclusive: c.MinExclusive,
			MaxExclusive: c.MaxExclusive,
			Pattern:      c.Pattern,
			MinLength:    c.MinLength,
			MaxLength:    c.MaxLength,
			Severity:     c.Severity,
		}
		idx, ok := byClass[c.Class]
		if !ok {
			idx = len(specs)
			specs = append(specs, ShapeSpec{
				ID:          "auto-" + c.Class,
				TargetClass: c.Class,
				Severity:    "Violation",
			})
			byClass[c.Class] = idx
		}
		merged := false
		props := specs[idx].Properties
		for j := range props {
			if props[j].Path == c.Path {
				props[j] = mergePropertySpec(props[j], ps)
				merged = true
				break
			}
		}
		if !merged {
			props = append(props, ps)
		}
		specs[idx].Properties = props
	}
	return specs
}

// mergePropertySpec 用约束（over）的非空字段覆盖显式形状（base）。
func mergePropertySpec(base, over PropertySpec) PropertySpec {
	if over.MinCount != nil {
		base.MinCount = over.MinCount
	}
	if over.MaxCount != nil {
		base.MaxCount = over.MaxCount
	}
	if over.Datatype != "" {
		base.Datatype = over.Datatype
	}
	if over.Class != "" {
		base.Class = over.Class
	}
	if over.NodeKind != "" {
		base.NodeKind = over.NodeKind
	}
	if len(over.In) > 0 {
		base.In = over.In
	}
	if over.MinInclusive != nil {
		base.MinInclusive = over.MinInclusive
	}
	if over.MaxInclusive != nil {
		base.MaxInclusive = over.MaxInclusive
	}
	if over.MinExclusive != nil {
		base.MinExclusive = over.MinExclusive
	}
	if over.MaxExclusive != nil {
		base.MaxExclusive = over.MaxExclusive
	}
	if over.Pattern != "" {
		base.Pattern = over.Pattern
	}
	if over.MinLength != nil {
		base.MinLength = over.MinLength
	}
	if over.MaxLength != nil {
		base.MaxLength = over.MaxLength
	}
	if over.Severity != "" {
		base.Severity = over.Severity
	}
	return base
}

// propertyShape 投影单个属性形状。
func (m *Manifest) propertyShape(ns Namespace, targetClass string, spec *PropertySpec) (shacl.PropertyShape, error) {
	ref, err := m.ResolveTerm(targetClass, spec.Path)
	if err != nil {
		return shacl.PropertyShape{}, err
	}
	path := ns.RelationIRI(ref.Name)
	if !ref.IsRelation {
		path = ns.AttributeIRI(targetClass, ref.Name)
	}

	ps := shacl.PropertyShape{
		Path:     path,
		MinCount: spec.MinCount,
		MaxCount: spec.MaxCount,
		In:       make([]graphstore.Term, 0, len(spec.In)),
		Keywords: []string{"PropertyShape", "path"},
	}
	if spec.Datatype != "" {
		dt, ok := xsdIRI(spec.Datatype)
		if !ok {
			return shacl.PropertyShape{}, errors.Ef(errors.CodeValidationFailed,
				"graphload: sh:datatype %q 不在契约枚举内", spec.Datatype)
		}
		ps.Datatype = dt
		ps.Keywords = append(ps.Keywords, "datatype")
	}
	if spec.Class != "" {
		ps.Class = ns.ClassIRI(spec.Class)
		ps.Keywords = append(ps.Keywords, "class")
	}
	if spec.NodeKind != "" {
		ps.NodeKind = shacl.NodeKind(spec.NodeKind)
		ps.Keywords = append(ps.Keywords, "nodeKind")
	}
	for _, v := range spec.In {
		// sh:in 的候选取字面值（xsd:string）：清单里的取值集合是文本枚举，
		// 若槽位是数值型，调用方应改用 sh:minInclusive/sh:maxInclusive。
		ps.In = append(ps.In, graphstore.Term{
			Kind: graphstore.KindLiteral, Lexical: v, Datatype: xsdNamespace + "string",
		})
	}
	if len(ps.In) > 0 {
		ps.Keywords = append(ps.Keywords, "in")
	}
	if spec.MinInclusive != nil {
		ps.MinInclusive = spec.MinInclusive
		ps.Keywords = append(ps.Keywords, "minInclusive")
	}
	if spec.MaxInclusive != nil {
		ps.MaxInclusive = spec.MaxInclusive
		ps.Keywords = append(ps.Keywords, "maxInclusive")
	}
	if spec.MinExclusive != nil {
		ps.MinExclusive = spec.MinExclusive
		ps.Keywords = append(ps.Keywords, "minExclusive")
	}
	if spec.MaxExclusive != nil {
		ps.MaxExclusive = spec.MaxExclusive
		ps.Keywords = append(ps.Keywords, "maxExclusive")
	}
	if spec.Pattern != "" {
		ps.Pattern = spec.Pattern
		ps.Keywords = append(ps.Keywords, "pattern")
	}
	if spec.MinLength != nil {
		ps.MinLength = spec.MinLength
		ps.Keywords = append(ps.Keywords, "minLength")
	}
	if spec.MaxLength != nil {
		ps.MaxLength = spec.MaxLength
		ps.Keywords = append(ps.Keywords, "maxLength")
	}
	if spec.Severity != "" {
		sev, err := parseSeverity(spec.Severity, "")
		if err != nil {
			return shacl.PropertyShape{}, err
		}
		ps.Severity = sev
		ps.Keywords = append(ps.Keywords, "severity")
	}
	// 关键字整体交给 shacl 包的同一份白名单判定：生成路径不维护第二份清单。
	if err := shacl.ValidateShapeKeywords(shacl.Shape{
		Properties: []shacl.PropertyShape{ps},
	}); err != nil {
		return shacl.PropertyShape{}, err
	}
	return ps, nil
}

// TargetClassIRIs 返回全部形状的目标类 IRI（去重）。反假绿探针用它确定"应该有多少实例"。
// 业务约束自动派生的形状（constraints 通道）同样是可执行约束，其目标类一并计入——
// 漏掉会让"约束只挂在约束通道的类上"的本体被探针误判为 0 实例。
func (m *Manifest) TargetClassIRIs(ns Namespace) []string {
	seen := make(map[string]bool, len(m.Shapes)+len(m.Constraints))
	out := make([]string, 0, len(m.Shapes)+len(m.Constraints))
	add := func(class string) {
		iri := ns.ClassIRI(class)
		if seen[iri] {
			return
		}
		seen[iri] = true
		out = append(out, iri)
	}
	// 按声明顺序输出（先形状后约束）：调用方（探针/诊断）拿到稳定序列，便于对比。
	for _, sh := range m.Shapes {
		add(sh.TargetClass)
	}
	for _, c := range m.Constraints {
		add(c.Class)
	}
	return out
}

// InstanceCount 返回清单声明的实例数（装载前的期望值）。
func (m *Manifest) InstanceCount() int { return len(m.Instances) }

// ---------------------------------------------------------------------------
// 反假绿探针
// ---------------------------------------------------------------------------

// CountTypedNodes 统计图中"属于指定类集合的实例节点"数量。
//
// 为什么必须有这个探针：SHACL 的焦点节点来自 sh:targetClass 对图内实例的匹配。
// 图是空的（或只有 TBox、没有 ABox）时，Validate 会返回 Conforms=true、Results 为空
// ——这与"数据全部合规"在返回值上完全无法区分。若把这种结果写进 validation_runs
// 并把版本置为 VALID，就得到了一个**显示为通过、实际什么都没查**的门禁，
// 比直接报 503 危险得多。因此调用方必须在校验前用本探针确认图里确实有实例。
func CountTypedNodes(ctx context.Context, store graphstore.GraphStore, tenantID ids.ID,
	graphIRI string, classIRIs []string) (int, error) {

	if store == nil {
		return 0, errors.E(errors.CodeInternal, "graphload: GraphStore 不能为空")
	}
	typeTerm := graphstore.Term{Kind: graphstore.KindIRI, Lexical: iriRDFType}
	seen := make(map[string]bool)
	for _, classIRI := range classIRIs {
		classTerm := graphstore.Term{Kind: graphstore.KindIRI, Lexical: classIRI}
		rows, err := store.Match(ctx, tenantID, graphstore.Pattern{
			P: &typeTerm, O: &classTerm, GraphIRI: graphIRI,
		})
		if err != nil {
			return 0, err
		}
		for _, r := range rows {
			seen[r.S.CanonicalDigest()] = true
		}
	}
	return len(seen), nil
}
