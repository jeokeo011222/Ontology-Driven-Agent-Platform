package graphstore

// 本文件是 GraphStore 与 RDF 存储之间的**唯一**词项/命名图文本构造层。
//
// 设计依据：docs/图数据库的选型.md —— "不要让所有 Go 业务模块直接拼接 SPARQL
// 并访问 Fuseki。所有访问都经过 ODAP 的 GraphStore Adapter，由它统一处理身份、
// 租户、允许查询的图、资源预算、版本和审计"。
//
// 该约束在代码层的落地方式有两条，缺一不可：
//
//  1. 词项一律经 writeNTerm 序列化为 N-Triples 形式，其中的保留字符（<>"{}|^\`、
//     控制字符、字面值引号与反斜杠）全部按规范转义。调用方传入的任何字符串都只可能
//     落在 `<...>` 或 `"..."` 的**内部**，不存在通过词项内容改写查询结构的路径。
//  2. 命名图一律经 physicalGraphIRI 加上租户命名空间前缀。租户隔离因此是
//     **结构性**的（另一个租户的物理图 IRI 不满足本租户前缀条件），而不是依赖
//     调用方自觉传对参数，也不依赖后端是否实现了行级权限。
//
// 词项与 SPARQL 文本只允许在本包内构造，见 tests/unit/arch_test.go 的
// "图存储唯一入口" 规则。

import (
	"fmt"
	"strings"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

const (
	// reservedBNodeIRIPrefix 是空白节点在图存储中的保留 IRI 前缀。
	//
	// 为什么不能原样写 `_:b1`：RDF 空白节点没有可寻址标识（SPARQL 规范也禁止空白
	// 节点出现在 VALUES 数据块中），一旦以 `_:label` 落库，Match / DeleteTriples /
	// 存在性判定都无法再定位该三元组，InsertTriples 的"实际新增条数"也将无法计算。
	// 适配器因此把空白节点**提升**为保留命名空间下的受控 IRI：label 经百分号编码后
	// 可逆，读回时还原为 KindBlankNode。这是适配器的显式取舍（ADR-005）。
	reservedBNodeIRIPrefix = "urn:odap:bnode:"

	// graphNamespaceRoot 是租户命名图命名空间的根。
	graphNamespaceRoot = "urn:odap:t:"

	// graphNamespaceInfix 分隔租户 UUID 与调用方命名图 IRI。
	graphNamespaceInfix = ":g:"

	// xsdStringIRI 是 XSD string 数据类型 IRI。
	//
	// RDF 1.1 规定"简单字面值"与 `"x"^^xsd:string` 是同一个词项，但 SPARQL JSON
	// 结果集在不同引擎上对二者的输出不一致（Jena 对简单字面值通常省略 datatype，
	// 部分引擎会显式回填 xsd:string）。若不归一化，同一词项在写入侧与读取侧会得到
	// 两个 CanonicalDigest，导致存在性判定与"实际新增条数"静默算错。
	xsdStringIRI = "http://www.w3.org/2001/XMLSchema#string"
)

// graphNamespacePrefix 返回某租户的命名图命名空间前缀。
func graphNamespacePrefix(tenantID ids.ID) string {
	return graphNamespaceRoot + tenantID.String() + graphNamespaceInfix
}

// physicalGraphIRI 把调用方命名图 IRI 映射为图存储中的物理命名图 IRI。
//
// 映射是单射：不同租户、不同调用方 IRI 绝不产生相同物理图 IRI；且物理 IRI 必然
// 以本租户命名空间前缀开头，因此跨租户读取在结构上不可能发生。
func physicalGraphIRI(tenantID ids.ID, graphIRI string) (string, error) {
	if tenantID.IsZero() {
		return "", errors.E(errors.CodeValidationFailed, "tenant_id 不能为空")
	}
	if err := validateGraphIRI(graphIRI); err != nil {
		return "", err
	}
	return graphNamespacePrefix(tenantID) + graphIRI, nil
}

// visibleGraphIRI 把物理命名图 IRI 还原为调用方视角的 IRI。
// 不属于本租户命名空间时返回 ok=false——调用方必须把这种情况当作
// "越界结果"处理（丢弃并报错），而不是当作空结果。
func visibleGraphIRI(tenantID ids.ID, physical string) (string, bool) {
	prefix := graphNamespacePrefix(tenantID)
	if !strings.HasPrefix(physical, prefix) {
		return "", false
	}
	return physical[len(prefix):], true
}

// validateGraphIRI 校验调用方命名图 IRI。
//
// 保留字符的安全性由 escapeIRI 保证（转义后无法逃逸出 `<...>`），这里只拒绝两类
// 明确有害的输入：
//   - 空串或含空白/控制字符：几乎必然是调用方拼串出错，早失败优于写出畸形 IRI；
//   - 以保留命名空间（urn:odap:）开头：防止调用方 IRi 与适配器内部编码冲突。
func validateGraphIRI(graphIRI string) error {
	if strings.TrimSpace(graphIRI) == "" {
		return errors.E(errors.CodeValidationFailed, "graph_iri 不能为空")
	}
	for _, r := range graphIRI {
		if r <= 0x20 || r == 0x7f {
			return errors.E(errors.CodeValidationFailed, "graph_iri 不能包含空白或控制字符")
		}
	}
	if isReservedIRI(graphIRI) {
		return errors.Ef(errors.CodeValidationFailed,
			"graph_iri 不得使用保留命名空间 %q 前缀（该前缀由 GraphStore 适配器占用）", reservedIRINamespace)
	}
	return nil
}

// reservedIRINamespace 是适配器占用的 IRI 命名空间。
const reservedIRINamespace = "urn:odap:"

// isReservedIRI 报告 IRI 是否落在适配器保留命名空间内。
func isReservedIRI(iri string) bool {
	return strings.HasPrefix(iri, reservedIRINamespace)
}

// bnodeIRI 把空白节点 label 映射为可寻址的保留 IRI（可逆）。
func bnodeIRI(label string) string {
	return reservedBNodeIRIPrefix + percentEncode(label)
}

// termFromIRI 把读取到的 IRI 还原为词项：保留命名空间内的空白节点形式还原为
// KindBlankNode，其余按 KindIRI 处理。
func termFromIRI(iri string) (Term, error) {
	if strings.HasPrefix(iri, reservedBNodeIRIPrefix) {
		label, err := percentDecode(iri[len(reservedBNodeIRIPrefix):])
		if err != nil {
			return Term{}, errors.Wrap(err, errors.CodeInternal,
				"graphstore: 图存储返回的空白节点 IRI 编码非法").WithDetails(map[string]any{"iri": iri})
		}
		return Term{Kind: KindBlankNode, Lexical: label}, nil
	}
	return Term{Kind: KindIRI, Lexical: iri}, nil
}

// writeNTerm 把一枚词项按 N-Triples 词法写入 b（N-Triples 是 Turtle/SPARQL 的
// 子集，因此同一份文本既可用于 SPARQL 的 INSERT/DELETE DATA，也可作为词项指纹）。
func writeNTerm(b *strings.Builder, t Term) error {
	switch t.Kind {
	case KindIRI:
		if t.Lexical == "" {
			return errors.E(errors.CodeValidationFailed, "IRI 词项的 lexical 不能为空")
		}
		b.WriteByte('<')
		b.WriteString(escapeIRI(t.Lexical))
		b.WriteByte('>')
		return nil
	case KindBlankNode:
		if t.Lexical == "" {
			return errors.E(errors.CodeValidationFailed, "空白节点词项的 lexical 不能为空")
		}
		// 提升为保留 IRI，保证可寻址（见 reservedBNodeIRIPrefix 的说明）。
		b.WriteByte('<')
		b.WriteString(escapeIRI(bnodeIRI(t.Lexical)))
		b.WriteByte('>')
		return nil
	case KindLiteral:
		if t.Lang != "" && t.Datatype != "" {
			return errors.E(errors.CodeValidationFailed,
				"字面值不能同时声明 language 与 datatype（RDF 1.1 §3.3）")
		}
		b.WriteByte('"')
		b.WriteString(escapeLiteral(t.Lexical))
		b.WriteByte('"')
		if t.Lang != "" {
			if err := validateLanguageTag(t.Lang); err != nil {
				return err
			}
			b.WriteByte('@')
			b.WriteString(t.Lang)
			return nil
		}
		if t.Datatype != "" {
			if err := validateDatatypeIRI(t.Datatype); err != nil {
				return err
			}
			b.WriteString("^^<")
			b.WriteString(escapeIRI(t.Datatype))
			b.WriteByte('>')
		}
		return nil
	default:
		return errors.Ef(errors.CodeValidationFailed, "非法 term_kind=%d", t.Kind)
	}
}

// TermNTriples 返回词项的规范 N-Triples 文本。
//
// 为什么要导出：上层（graphload 的 RDF 序列化与 artifacts 端点）需要把词项渲染
// 成人可读的 RDF 文本，而转义规则必须**只有一份实现**。若在别的包里再写一遍
// escapeIRI / escapeLiteral，两份实现迟早漂移，漂移的后果是"界面上看到的 RDF"与
// "图里实际存的词项"不是同一个东西——这类偏差不报错。因此这里只做一层薄封装。
func TermNTriples(t Term) (string, error) { return termText(t) }

// termText 返回词项的 N-Triples 文本（出错时返回空串与错误）。
func termText(t Term) (string, error) {
	var b strings.Builder
	if err := writeNTerm(&b, t); err != nil {
		return "", err
	}
	return b.String(), nil
}

// escapeIRI 按 N-Triples 规则转义 IRI 内部字符。
//
// 只转义会破坏 `<...>` 边界或语法结构的字符；其余（含非 ASCII）原样保留，
// 与 N-Triples 1.1 对 IRI 的要求一致。
func escapeIRI(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		if r <= 0x20 || r == 0x7f || strings.ContainsRune("<>\"{}|^`\\", r) {
			fmt.Fprintf(&b, "\\u%04X", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// escapeLiteral 按 N-Triples 规则转义字面值内部字符。
func escapeLiteral(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				// N-Triples 未为退格/换页等定义短转义，统一用 \uXXXX。
				fmt.Fprintf(&b, "\\u%04X", r)
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

// validateLanguageTag 校验 BCP47 语言标签的常规形态 language[-subtag]*。
//
// 只接受字母/数字子标签，避免把任意字符串写进 @lang 位置（该位置不经引号转义，
// 是唯一一处"文本直接进入 SPARQL 结构"的地方，因此必须逐字符校验）。
func validateLanguageTag(tag string) error {
	if tag == "" {
		return errors.E(errors.CodeValidationFailed, "语言标签不能为空")
	}
	parts := strings.Split(tag, "-")
	for i, part := range parts {
		if part == "" {
			return errors.Ef(errors.CodeValidationFailed, "非法语言标签 %q", tag)
		}
		if i == 0 {
			// 主语言子标签：纯字母，长度 2~8（BCP47 §2.2.1）。
			if len(part) < 2 || len(part) > 8 || !allAlpha(part) {
				return errors.Ef(errors.CodeValidationFailed, "非法语言标签 %q", tag)
			}
			continue
		}
		if len(part) > 8 || !allAlnum(part) {
			return errors.Ef(errors.CodeValidationFailed, "非法语言标签 %q", tag)
		}
	}
	return nil
}

func allAlpha(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

func allAlnum(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// validateDatatypeIRI 校验数据类型的 IRI 形态。
func validateDatatypeIRI(dt string) error {
	if strings.TrimSpace(dt) == "" {
		return errors.E(errors.CodeValidationFailed, "datatype IRI 不能为空")
	}
	for _, r := range dt {
		if r <= 0x20 || r == 0x7f {
			return errors.Ef(errors.CodeValidationFailed, "datatype IRI %q 含空白或控制字符", dt)
		}
	}
	return nil
}

// normalizeTerm 归一化词项，保证同一 RDF 词项在任何后端上得到同一个 CanonicalDigest。
//
// 当前只做一条归一（xsd:string → 空 datatype，原因见 xsdStringIRI 注释）。
// 归一化只作用于**读取路径与写入侧记账**，不改变写出的 RDF 文本。
func normalizeTerm(t Term) Term {
	if t.Kind == KindLiteral && t.Datatype == xsdStringIRI {
		t.Datatype = ""
	}
	return t
}

// percentEncode 对空白节点 label 做可逆的百分号编码。
//
// 只保留 RFC 3986 的 unreserved 集合，其余字节一律 %XX，保证结果是合法 IRI 片段
// 且与另一个 label 不会编码冲突（单射）。
func percentEncode(s string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0x0f])
	}
	return b.String()
}

// percentDecode 还原 percentEncode 的结果。
func percentDecode(s string) (string, error) {
	if !strings.ContainsRune(s, '%') {
		return s, nil
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			b.WriteByte(s[i])
			continue
		}
		if i+2 >= len(s) {
			return "", fmt.Errorf("百分号编码不完整")
		}
		hi, ok1 := hexVal(s[i+1])
		lo, ok2 := hexVal(s[i+2])
		if !ok1 || !ok2 {
			return "", fmt.Errorf("百分号编码含非十六进制字符")
		}
		b.WriteByte(hi<<4 | lo)
		i += 2
	}
	return b.String(), nil
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}

// tripleKeyOf 返回三元组在适配器内部记账时使用的键（归一化后的词项文本）。
//
// 用**归一化后的 N-Triples 文本**而非 CanonicalDigest 作键，是因为它同时覆盖了
// 结构（IRI/字面值/空白节点）与内容，便于在排错时直接肉眼比对。
func tripleKeyOf(t Triple) (string, error) {
	s, err := termText(normalizeTerm(t.S))
	if err != nil {
		return "", err
	}
	p, err := termText(normalizeTerm(t.P))
	if err != nil {
		return "", err
	}
	o, err := termText(normalizeTerm(t.O))
	if err != nil {
		return "", err
	}
	return s + " " + p + " " + o, nil
}

// validateTripleGraph 校验每条三元组自带的 GraphIRI 与调用参数一致。
//
// 三个后端必须共用同一判据：落点命名图由 graphIRI **参数**决定，Triple.GraphIRI
// 只是冗余携带。若调用方逐条设了与之不一致的 GraphIRI，静默忽略就会写入"错图"
// 而调用方以为写到了别处——这是最难排查的一类缺陷。因此统一显式拒绝，
// 保证"适配器可替换"不是口号。
func validateTripleGraph(graphIRI string, triples []Triple) error {
	for i, tr := range triples {
		if tr.GraphIRI != "" && tr.GraphIRI != graphIRI {
			return errors.Ef(errors.CodeValidationFailed,
				"第 %d 条三元组的 GraphIRI(%q) 与调用参数(%q) 不一致", i, tr.GraphIRI, graphIRI)
		}
	}
	return nil
}

// validateWriteArgs 统一校验写入（Insert/Delete）路径的租户、命名图与三元组参数。
//
// ⚠️ 必须在"空批次提前返回"**之前**调用。否则会出现"随数据量变化的校验"：
// 调用方漏传租户但恰好传了空批次时返回成功，换成非空批次才报错——这类缺陷在
// 空数据环境（新租户、刚清库、灰度期）里完全不显形，属于最难排查的一类。
// 三个后端共用本判据，避免又出现"第二份实现"。（由 graphstore-check 自检暴露。）
func validateWriteArgs(tenantID ids.ID, graphIRI string, triples []Triple) error {
	if tenantID.IsZero() {
		return errors.E(errors.CodeValidationFailed, "tenant_id 不能为空")
	}
	if strings.TrimSpace(graphIRI) == "" {
		return errors.E(errors.CodeValidationFailed, "graph_iri 不能为空")
	}
	return validateTripleGraph(graphIRI, triples)
}

// validateGraphReadArgs 统一校验读取路径的租户与命名图参数。
func validateGraphReadArgs(tenantID ids.ID, graphIRI string) error {
	if tenantID.IsZero() {
		return errors.E(errors.CodeValidationFailed, "tenant_id 不能为空")
	}
	if strings.TrimSpace(graphIRI) == "" {
		return errors.E(errors.CodeValidationFailed, "graph_iri 不能为空")
	}
	return nil
}
