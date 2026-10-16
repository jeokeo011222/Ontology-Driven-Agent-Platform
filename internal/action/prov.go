package action

// 本文件是 Action 模块的 PROV-O 溯源层：
//   - BuildProvenance：把一次动作投影为 PROV-O 三元组（写入命名图前构造）；
//   - Turtle：把命名图里的三元组序列化为人可读、可对外复核的 Turtle 文本
//     （GET /api/v1/actions/{id}/prov.ttl 导出）。
//
// # 与 graphload/serialize.go 的关系
//
// 风格与其对齐（前缀声明 + 逐三元组一行），但刻意不依赖它的任何私有函数：
// 它服务"本体清单投影"，本文件服务"运行期动作溯源"，两者的前缀集合与图来源
// 都不同。词项转义则必须只有一份实现——全部经 graphstore.TermNTriples 产出
// （N-Triples 是 Turtle 的子集，规范转义在 Turtle 里同样合法），本文件绝不
// 自己拼 IRI/字面值文本。
//
// # PROV-O 建模口径
//
//   - 动作   → prov:Activity（actionIRI = urn:odap:action:{action_id}）
//   - 主体   → prov:Agent（actorIRI，按 kind+name 的摘要确定性派生：同一主体
//     在所有动作的溯源图里是同一个节点，跨动作的"谁在做决策"因此可连图查询）
//   - 关联   → prov:wasAssociatedWith（Activity → Agent）
//   - 时间   → prov:atTime（xsd:dateTime 字面值）
//   - 输入   → prov:used（摘要派生的 prov:Entity → Activity）
//   - 产出   → prov:wasGeneratedBy（Entity → Activity）
//
// 摘要为空的动作不产生对应 Entity（没有内容就没有可溯源的实体，硬造一个
// 空节点只会污染图）。

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"odap/internal/ontology/graphstore"
	"odap/internal/platform/ids"
)

// PROV-O / RDF / XSD 命名空间（prov 前缀按实施契约固定为 W3C 官方 IRI）。
const (
	ProvNamespace = "http://www.w3.org/ns/prov#"
	RDFNamespace  = "http://www.w3.org/1999/02/22-rdf-syntax-ns#"
	XSDNamespace  = "http://www.w3.org/2001/XMLSchema#"
)

// 本模块用到的 PROV-O / RDF 谓词与类型 IRI。
const (
	RDFType               = RDFNamespace + "type"
	ProvActivityType      = ProvNamespace + "Activity"
	ProvAgentType         = ProvNamespace + "Agent"
	ProvEntityType        = ProvNamespace + "Entity"
	ProvWasAssociatedWith = ProvNamespace + "wasAssociatedWith"
	ProvAtTime            = ProvNamespace + "atTime"
	ProvUsed              = ProvNamespace + "used"
	ProvWasGeneratedBy    = ProvNamespace + "wasGeneratedBy"
)

// actionIRIRoot / actorIRIRoot / contentIRIRoot 是本模块的 URN 命名空间根。
//
// 注意区分两类"IRI"：这里的 URN 是**三元组词项**（主语/宾语），允许使用
// urn:odap: 前缀；而命名图 IRI 是调用方视角的图名，graphstore 对它禁用
// urn:odap: 保留命名空间，因此溯源图用 prov:{tenant}:{action_id} 形态。
const (
	actionIRIRoot  = "urn:odap:action:"
	actorIRIRoot   = "urn:odap:actor:"
	contentIRIRoot = "urn:odap:content:"
)

// ProvenanceGraphIRI 返回某动作的 PROV-O 溯源命名图 IRI（调用方视角）。
// 形态 prov:{tenant_id}:{action_id}，与 action.actions.prov_graph_iri 列的
// 取值完全一致：详情页按该列读回三元组，导出端点按它落 Turtle 头注释。
func ProvenanceGraphIRI(tenantID, actionID ids.ID) string {
	return "prov:" + tenantID.String() + ":" + actionID.String()
}

// ActionIRI 返回动作节点的 IRI（prov:Activity）。
func ActionIRI(actionID ids.ID) string {
	return actionIRIRoot + actionID.String()
}

// ActorIRI 返回动作主体节点的 IRI（prov:Agent）。
//
// 按 (kind, name) 的 SHA-256 前 16 字节确定性派生，而不是把 name 直接拼进 URN：
// name 是自由文本（用户名/智能体键/工具键），直接拼接会产出含空白或非法字符的
// IRI；确定性摘要则保证同一主体恒定映射到同一节点，IRI 形态永远合法。主体的
// 可读名存在 action.actions.actor_name 列，溯源图里不需要重复。
func ActorIRI(kind, name string) string {
	sum := sha256.Sum256([]byte(kind + "\x1f" + name))
	return actorIRIRoot + strings.ToLower(kind) + ":" + hex.EncodeToString(sum[:8])
}

// DigestEntityIRI 返回内容摘要对应的 prov:Entity 节点 IRI。
// digest 是 SHA-256 hex（Recorder 已归一），本身即合法 IRI 片段。
func DigestEntityIRI(digest string) string {
	return contentIRIRoot + digest
}

// BuildProvenance 构造一个动作的 PROV-O 溯源三元组（写入 prov 命名图的批次）。
//
// 每条三元组的 GraphIRI 都显式设为目标命名图：graphstore 的写入路径会校验
// "逐条 GraphIRI 与调用参数一致"，显式设置让不一致在写入前就失败，而不是
// 被静默忽略后写错图。
func BuildProvenance(tenantID, actionID ids.ID, actorKind, actorName,
	inputDigest, outputDigest string, occurredAt time.Time) []graphstore.Triple {
	graphIRI := ProvenanceGraphIRI(tenantID, actionID)
	actionIRI := ActionIRI(actionID)
	actorIRI := ActorIRI(actorKind, actorName)
	newTriple := func(s, p, o graphstore.Term) graphstore.Triple {
		return graphstore.Triple{
			S: s, P: p, O: o,
			GraphIRI:   graphIRI,
			FactKind:   graphstore.FactKindAsserted,
			ObservedAt: occurredAt,
		}
	}
	iri := func(v string) graphstore.Term { return graphstore.Term{Kind: graphstore.KindIRI, Lexical: v} }
	literal := func(v, datatype string) graphstore.Term {
		return graphstore.Term{Kind: graphstore.KindLiteral, Lexical: v, Datatype: datatype}
	}

	// 1. 动作是 Activity；主体是 Agent；二者关联；动作时间。
	triples := []graphstore.Triple{
		newTriple(iri(actionIRI), iri(RDFType), iri(ProvActivityType)),
		newTriple(iri(actorIRI), iri(RDFType), iri(ProvAgentType)),
		newTriple(iri(actionIRI), iri(ProvWasAssociatedWith), iri(actorIRI)),
		newTriple(iri(actionIRI), iri(ProvAtTime),
			literal(occurredAt.UTC().Format(time.RFC3339Nano), XSDNamespace+"dateTime")),
	}
	// 2. 输入摘要 → prov:used 的 Entity。
	if inputDigest != "" {
		inIRI := iri(DigestEntityIRI(inputDigest))
		triples = append(triples,
			newTriple(inIRI, iri(RDFType), iri(ProvEntityType)),
			newTriple(iri(actionIRI), iri(ProvUsed), inIRI),
		)
	}
	// 3. 输出摘要 → prov:wasGeneratedBy 的 Entity（Entity 指回 Activity）。
	if outputDigest != "" {
		outIRI := iri(DigestEntityIRI(outputDigest))
		triples = append(triples,
			newTriple(outIRI, iri(RDFType), iri(ProvEntityType)),
			newTriple(outIRI, iri(ProvWasGeneratedBy), iri(actionIRI)),
		)
	}
	return triples
}

// turtlePrefixes 是 Turtle 导出声明的前缀（声明固定一组而非按需裁剪：
// 解析器接受未引用的前缀声明，而"同一份文件在不同调用点前缀不同"难以比对）。
var turtlePrefixes = []struct{ prefix, iri string }{
	{"prov", ProvNamespace},
	{"rdf", RDFNamespace},
	{"xsd", XSDNamespace},
}

// isTurtleLocal 判断局部名能否写成 Turtle 前缀名（PN_LOCAL）。
//
// 与 graphload/serialize.go 的同名函数口径一致（首字符必须是字母/数字/_，
// 末字符不能是 '.'）；在那里它是私有函数，本包不得依赖，故在此按同一规则
// 独立实现——只有一处消费（本文件的 shortenIRI），不存在第二实现漂移的温床。
func isTurtleLocal(s string) bool {
	if s == "" {
		return false
	}
	first := s[0]
	if !(first >= 'a' && first <= 'z' || first >= 'A' && first <= 'Z' ||
		first >= '0' && first <= '9' || first == '_') {
		return false
	}
	if s[len(s)-1] == '.' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

// shortenIRI 把 IRI 缩写为前缀名；无法缩写时返回空串（调用方回落完整 <IRI>）。
func shortenIRI(iri string) string {
	for _, p := range turtlePrefixes {
		local := strings.TrimPrefix(iri, p.iri)
		if local == iri {
			continue
		}
		if isTurtleLocal(local) {
			return p.prefix + ":" + local
		}
		return ""
	}
	return ""
}

// writeTurtleTerm 按 Turtle 词法写入一枚词项：IRI 可缩写则用前缀名，
// 否则用 graphstore.TermNTriples 的规范文本（转义规则只有一份实现）。
func writeTurtleTerm(b *strings.Builder, t graphstore.Term) error {
	if t.Kind == graphstore.KindIRI {
		if short := shortenIRI(t.Lexical); short != "" {
			b.WriteString(short)
			return nil
		}
	}
	text, err := graphstore.TermNTriples(t)
	if err != nil {
		return err
	}
	b.WriteString(text)
	return nil
}

// Turtle 把溯源命名图的三元组序列化为 Turtle 文本。
//
// graphName 只进头部注释（Turtle 本身不携带图名，命名图属于 TriG 概念）：
// 导出文件必须能自证"我从哪个图来"，否则下载后与详情页比对时无从对账。
// 词项序列化失败（理论上不可能：能从图存储读回的词项必然合法）时跳过该条
// 并写入注释说明——宁可少一行也不产出解析器拒绝的文件。
func Turtle(graphName string, triples []graphstore.Triple) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# ODAP PROV-O 溯源图 %s\n", graphName)
	b.WriteString("\n")
	for _, p := range turtlePrefixes {
		fmt.Fprintf(&b, "@prefix %s: <%s> .\n", p.prefix, p.iri)
	}
	b.WriteString("\n")
	for _, tr := range triples {
		var line strings.Builder
		if err := writeTurtleTerm(&line, tr.S); err != nil {
			fmt.Fprintf(&b, "# 跳过无法序列化的三元组（主语：%v）\n", err)
			continue
		}
		line.WriteByte(' ')
		if err := writeTurtleTerm(&line, tr.P); err != nil {
			fmt.Fprintf(&b, "# 跳过无法序列化的三元组（谓词：%v）\n", err)
			continue
		}
		line.WriteByte(' ')
		if err := writeTurtleTerm(&line, tr.O); err != nil {
			fmt.Fprintf(&b, "# 跳过无法序列化的三元组（宾语：%v）\n", err)
			continue
		}
		line.WriteString(" .\n")
		b.WriteString(line.String())
	}
	return b.String()
}
