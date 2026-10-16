// Package nlquery 实现「自然语言 → 本体知识查询」服务：把用户自然语言问题
// 交给 LLM 转成 graphstore.Pattern，再在本体 ABox/TBox 上执行 Match，
// 返回原始三元组 + 自然语言总结。
//
// # 为什么不直接执行 SPARQL
//
// ODAP 用的是自研 Triples Store（internal/ontology/graphstore），底层基于
// PostgreSQL + 资源字典 + PK/POS/OSP 三索引，不引入 Jena/Fuseki 等 RDF
// 引擎，因此没有 SPARQL 查询计划器。LLM 在此扮演的是「声明式查询意图 →
// 显式 Pattern」的编译器，而不是 SPARQL 生成器。
//
// # 严格 fail-closed
//
//  1. LLM 必须严格引用 manifest 中声明的 IRI（类 / 关系 / 属性），严禁编造
//     任何白名单外的 IRI。校验失败 → ValidationFailed（而不是静默用
//     空 Pattern 查全图）。
//  2. GraphStore 未装配 → Unavailable。
//  3. SnapshotReader（manifest 读取能力）未装配 → Unavailable。
//  4. 本体 key 不存在 → NotFound。
//  5. LLM 未配置 / 调用失败 → Unavailable / ModelProviderError。
//
// # IRI 白名单
//
// 白名单按「本体命名空间 + 内置 RDF/RDFS 词表」构造，由 graphload.Manifest
// 遍历 classes / relations / attributes 确定性给出——LLM 输出的每个 IRI
// 都必须命中。白名单之外的 IRI 一律被视为 LLM 幻觉（MODEL_OUTPUT_INVALID）。
//
// # 依赖方向（架构规则 §4.3）
//
// 本包是领域层（ontology 子树），严格禁止反向依赖 HTTP 传输层
// （internal/api/**）。manifest 读取能力以 SnapshotReader 接口抽象，
// 由装配层（cmd/api / handlers）把 api/store.Store 注入。
package nlquery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"odap/internal/llm"
	"odap/internal/ontology/graphload"
	"odap/internal/ontology/graphstore"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// LLMCompleter 是模型网关的最小依赖面（*llm.Gateway 满足；测试可注入替身）。
type LLMCompleter interface {
	Complete(ctx context.Context, tenantID ids.ID, req llm.CompletionRequest) (llm.Completion, error)
}

// OntologySnapshot 是一次本体版本快照（manifest 解析前的只读视图）。
//
// 刻意不依赖 api/store 的同名结构体：让领域层自己定义最小需要的字段，
// 保持与 HTTP 传输层解耦。装配层在 handlers 里做一个薄适配。
type OntologySnapshot struct {
	OntologyKey string
	Name        string
	VersionID   string
	Version     string
	Status      string
	Manifest    json.RawMessage
}

// SnapshotReader 是本体版本快照读取接口（领域层唯一依赖面）。
//
// api/store.Store 的 OntologyVersionSnapshotByKey 返回 *store.OntologyVersionSnapshot，
// 与本接口的语义一致但结构体不同。装配层在 handlers 里做适配。
type SnapshotReader interface {
	OntologyVersionSnapshotByKey(ctx context.Context, ontologyKey string) (*OntologySnapshot, error)
}

// Service 编排 NL → Pattern → Match 全流程。
type Service struct {
	// Reader 读取本体 manifest（ ontology_key → 版本快照）。
	// 未装配时 Query 直接返回 UNAVAILABLE（fail-closed）。
	Reader SnapshotReader
	// GraphStore 执行 graphstore.Match；同上。
	GraphStore graphstore.GraphStore
	// LLM 是模型补全网关。未装配 → UNAVAILABLE。
	LLM LLMCompleter
}

// QueryResult 是一次自然语言问答的完整结果。
type QueryResult struct {
	OntologyKey string              `json:"ontology_key"`
	VersionID   string              `json:"version_id"`
	Version     string              `json:"version"`
	GraphIRI    string              `json:"graph_iri"`
	Answer      string              `json:"answer"`
	Triples     []graphstore.Triple `json:"triples"`
	Confidence  float64             `json:"confidence"`
	Model       string              `json:"model,omitempty"`
}

// patternTriple 是 LLM 输出的一条查询子句（对应一个 graphstore.Pattern）。
//
// 三个槽位都是可选字符串："" 表示通配，否则必须是白名单内的合法 IRI。
// 典型形态：{"predicate": "<类IRI>"}（查该类全部实例），或
// {"subject": "<实例IRI>", "predicate": "rdf:type"}（查某实例的类型）。
type patternTriple struct {
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	Object    string `json:"object"`
}

// llmQueryOutput 是 LLM 输出的 schema（additionalProperties: false）。
type llmQueryOutput struct {
	Triples    []patternTriple `json:"triples"`
	Answer     string          `json:"answer"`
	Confidence float64         `json:"confidence"`
}

// llmQuerySchema 是强制 LLM 结构化输出的 JSON Schema（additionalProperties: false）。
//
// 之所以不用 map[string]any 在代码里硬写，而是内嵌 JSON 字面量：保证严格
// additionalProperties: false，禁止 LLM 塞脏字段进来污染下游。
const llmQuerySchemaJSON = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["triples", "answer", "confidence"],
  "properties": {
    "triples": {
      "type": "array",
      "minItems": 1,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "properties": {
          "subject":   {"type": "string"},
          "predicate": {"type": "string"},
          "object":    {"type": "string"}
        }
      }
    },
    "answer":     {"type": "string", "minLength": 1},
    "confidence": {"type": "number", "minimum": 0, "maximum": 1}
  }
}`

// 内置 RDF/RDFS 词表（白名单兜底）。这些 IRI 不是业务声明的，
// 但 LLM 在自然语言问题里很可能引用"类型/标签/域/值域"这类标准谓词。
var builtinIRIs = map[string]bool{
	"http://www.w3.org/1999/02/22-rdf-syntax-ns#type":     true,
	"http://www.w3.org/1999/02/22-rdf-syntax-ns#Property": true,
	"http://www.w3.org/2000/01/rdf-schema#Class":          true,
	"http://www.w3.org/2000/01/rdf-schema#label":          true,
	"http://www.w3.org/2000/01/rdf-schema#domain":         true,
	"http://www.w3.org/2000/01/rdf-schema#range":          true,
}

// Query 把自然语言问题转成本体查询并执行。
//
// modelID 为空时走 Gateway.Resolve 的"按能力择优"分支（与
// handlers.GenerateOntology 同口径）；topK<=0 或 >200 会被收敛到 [1,200]。
func (s *Service) Query(ctx context.Context, tenantID ids.ID, ontologyKey, question, modelID string, topK int) (*QueryResult, error) {
	if s == nil || s.Reader == nil {
		return nil, errors.E(errors.CodeUnavailable, "nlquery: SnapshotReader 未装配（无法读取本体 manifest）")
	}
	if s.GraphStore == nil {
		return nil, errors.E(errors.CodeUnavailable, "nlquery: GraphStore 未装配")
	}
	if s.LLM == nil {
		return nil, errors.E(errors.CodeUnavailable, "nlquery: 模型网关未装配（无法把自然语言转成语义图查询）")
	}
	qk := strings.TrimSpace(ontologyKey)
	if qk == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "ontology_key 不能为空")
	}
	q := strings.TrimSpace(question)
	if q == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "question 不能为空")
	}
	if topK <= 0 {
		topK = 50
	}
	if topK > 200 {
		topK = 200
	}

	// --- 1. 读取本体 manifest + 构造白名单 ---
	snap, err := s.Reader.OntologyVersionSnapshotByKey(ctx, qk)
	if err != nil {
		return nil, err // NotFound / Unavailable / ValidationFailed 已在 Store 层分类
	}
	if len(snap.Manifest) == 0 {
		return nil, errors.E(errors.CodeValidationFailed,
			"nlquery: 本体版本没有 manifest（无法构造查询意图）")
	}
	parsed, err := graphload.ParseManifest(snap.Manifest)
	if err != nil {
		return nil, errors.E(errors.CodeValidationFailed, "nlquery: 本体清单解析失败："+err.Error())
	}
	ns, err := graphload.NewNamespace(qk)
	if err != nil {
		return nil, err
	}
	allowed := buildIRIClosure(parsed, ns)
	graphIRI, err := graphload.GraphIRI(qk, snap.VersionID)
	if err != nil {
		return nil, err
	}

	// --- 2. LLM → 结构化 Pattern ---
	out, completion, err := s.callLLM(ctx, tenantID, modelID, snap, parsed, ns, q)
	if err != nil {
		return nil, err
	}

	// --- 3. 白名单校验 + Pattern 构造 ---
	patterns, err := toPatterns(out.Triples, allowed, topK)
	if err != nil {
		return nil, err
	}

	// --- 4. GraphStore.Match ---
	results := make([]graphstore.Triple, 0, topK*len(patterns))
	for _, p := range patterns {
		p.GraphIRI = graphIRI
		rows, merr := s.GraphStore.Match(ctx, tenantID, p)
		if merr != nil {
			return nil, errors.Wrap(merr, errors.CodeInternal, "nlquery: GraphStore.Match 失败")
		}
		results = append(results, rows...)
		if len(results) >= topK {
			results = results[:topK]
			break
		}
	}

	modelRev := ""
	if completion != nil {
		modelRev = completion.ModelRevision
	}

	return &QueryResult{
		OntologyKey: qk,
		VersionID:   snap.VersionID,
		Version:     snap.Version,
		GraphIRI:    graphIRI,
		Answer:      strings.TrimSpace(out.Answer),
		Triples:     results,
		Confidence:  clamp01(out.Confidence),
		Model:       modelRev,
	}, nil
}

// callLLM 发一次带严格 ResponseSchema 的补全请求。
//
// prompt 三要素：① manifest 里全部类/关系/属性的局部名 + IRI 白名单（枚举）；
// ② 业务硬约束 / 样例实例（帮助模型理解语义）；③ 强约束：只允许引用白名单
// 内 IRI，任何不在白名单里的 IRI 都被视为幻觉 → 上游拒绝。
func (s *Service) callLLM(ctx context.Context, tenantID ids.ID, modelID string,
	snap *OntologySnapshot, parsed *graphload.Manifest,
	ns graphload.Namespace, question string) (*llmQueryOutput, *llm.Completion, error) {

	system := buildSystemPrompt(ns, parsed, snap.Name, parsed.Summary)
	user := "问题：" + question + `

严格要求：
1. triples 数组每一项都对应一条 graphstore.Match Pattern。subject/predicate/object 三个槽位都是可选字符串；任一槽位为空字符串即代表该位置是通配（nil）。
2. 所有引用的 IRI 必须完全等于 system prompt 里枚举的某个值。绝对禁止编造 system prompt 里没有的 IRI。
3. 若问题语义无法在当前本体中落地（例如本体没有对应类/关系），返回空 triples、answer 说明原因、confidence 为 0，不要编造。
4. confidence 取值 0~1，反映你对 pattern 正确性的主观把握。
5. answer 用中文，不超过 300 字，引用类/关系/属性时直接用局部名（无需拼完整 IRI）。`

	schemaBytes := []byte(llmQuerySchemaJSON)
	req := llm.CompletionRequest{
		TenantID:           tenantID,
		ModelID:            modelID,
		Messages:           []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: user}},
		ResponseSchema:     schemaBytes,
		MaxOutputTokens:    2048,
		DataClassification: "INTERNAL",
		IdempotencyKey:     "nlquery:" + snap.VersionID + ":" + question,
	}
	comp, err := s.LLM.Complete(ctx, tenantID, req)
	if err != nil {
		return nil, nil, err
	}
	var out llmQueryOutput
	if perr := json.Unmarshal([]byte(comp.Content), &out); perr != nil {
		return nil, &comp, errors.E(errors.CodeModelOutputInvalid,
			"nlquery: 模型输出不是合法 JSON（违反 ResponseSchema 约束）").
			WithDetails(map[string]any{
				"parse_error": perr.Error(),
				"raw_prefix":  clip(comp.Content, 400),
			})
	}
	if len(out.Triples) == 0 {
		out.Triples = []patternTriple{}
	}
	return &out, &comp, nil
}

// buildSystemPrompt 组装 LLM system：白名单 IRI + 清单概览。
func buildSystemPrompt(ns graphload.Namespace, m *graphload.Manifest, ontologyName, summary string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "你是一个语义图查询助手。当前本体是：%s（ontology_key=%s）。\n\n",
		ontologyName, strings.TrimPrefix(ns.Base(), strings.TrimSuffix(ns.Base(), "#")))
	if strings.TrimSpace(summary) != "" {
		fmt.Fprintf(&b, "摘要：%s\n\n", strings.TrimSpace(summary))
	}

	b.WriteString("=== 可用类（所有实例都属于这里面某个类） ===\n")
	for _, c := range m.Classes {
		iri := ns.ClassIRI(c.Name)
		fmt.Fprintf(&b, "- %s  IRI=%s", c.Name, iri)
		if strings.TrimSpace(c.Label) != "" {
			fmt.Fprintf(&b, "  label=%s", strings.TrimSpace(c.Label))
		}
		if strings.TrimSpace(c.Description) != "" {
			fmt.Fprintf(&b, "  说明=%s", strings.TrimSpace(c.Description))
		}
		b.WriteString("\n")
		for _, a := range c.Attributes {
			attrIRI := ns.AttributeIRI(c.Name, a.Name)
			fmt.Fprintf(&b, "    .%s  IRI=%s  类型=%s  必填=%v", a.Name, attrIRI, a.Type, a.Required)
			if strings.TrimSpace(a.Description) != "" {
				fmt.Fprintf(&b, "  说明=%s", strings.TrimSpace(a.Description))
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("\n=== 可用关系（类之间的对象属性） ===\n")
	if len(m.Relations) == 0 {
		b.WriteString("（无）\n")
	}
	for _, r := range m.Relations {
		fmt.Fprintf(&b, "- %s  IRI=%s  从=%s  到=%s  基数=%s",
			r.Name, ns.RelationIRI(r.Name), r.FromClass, r.ToClass, r.Cardinality)
		if strings.TrimSpace(r.Description) != "" {
			fmt.Fprintf(&b, "  说明=%s", strings.TrimSpace(r.Description))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n=== 业务硬约束（SHACL） ===\n")
	if len(m.Constraints) == 0 && len(m.Shapes) == 0 {
		b.WriteString("（无可执行约束）\n")
	}
	for _, c := range m.Constraints {
		fmt.Fprintf(&b, "- 类 %s 路径 %s  严重级别 %s",
			c.Class, c.Path, c.Severity)
		if c.MinCount != nil {
			fmt.Fprintf(&b, "  min=%d", *c.MinCount)
		}
		if c.MaxCount != nil {
			fmt.Fprintf(&b, "  max=%d", *c.MaxCount)
		}
		if c.In != nil {
			fmt.Fprintf(&b, "  枚举=%v", c.In)
		}
		fmt.Fprintln(&b)
	}

	b.WriteString("\n=== 标准 RDF/RDFS 词表（可引用） ===\n")
	b.WriteString("- http://www.w3.org/1999/02/22-rdf-syntax-ns#type  （实例 → 类）\n")
	b.WriteString("- http://www.w3.org/2000/01/rdf-schema#label\n")
	b.WriteString("- http://www.w3.org/2000/01/rdf-schema#domain\n")
	b.WriteString("- http://www.w3.org/2000/01/rdf-schema#range\n")
	b.WriteString("- http://www.w3.org/1999/02/22-rdf-syntax-ns#Property\n")
	b.WriteString("- http://www.w3.org/2000/01/rdf-schema#Class\n")

	b.WriteString("\n=== 输出要求 ===\n")
	b.WriteString("输出必须严格符合调用方给出的 JSON Schema。triples 里的 subject/predicate/object 都是字符串槽位：为空表示该位置通配，非空必须完全等于上面枚举的某条 IRI。任何不在枚举里的 IRI 都属于编造，绝对禁止。\n")
	return b.String()
}

// buildIRIClosure 收集 manifest 全部可引用 IRI + builtin。
//
// 返回 map 便于 O(1) 校验；sort.Strings 用来保证测试确定性（不依赖 map 遍历顺序）。
func buildIRIClosure(m *graphload.Manifest, ns graphload.Namespace) map[string]bool {
	seen := make(map[string]bool, 32+builtinIRIsCount)
	for iri := range builtinIRIs {
		seen[iri] = true
	}
	for _, c := range m.Classes {
		seen[ns.ClassIRI(c.Name)] = true
		for _, a := range c.Attributes {
			seen[ns.AttributeIRI(c.Name, a.Name)] = true
		}
	}
	for _, r := range m.Relations {
		seen[ns.RelationIRI(r.Name)] = true
	}
	return seen
}

const builtinIRIsCount = 6

// toPatterns 把 LLM 的 triples 数组转为 graphstore.Pattern 切片。
//
// 白名单校验是关键防线：任何 subject/predicate/object 引用了不在白名单里
// 的 IRI 都直接返回 ValidationFailed。这比"静默把非法槽位当通配"安全得多
// —— 后者会把一个具体查询退化成全图扫描，还看不出为什么。
//
// 如果某条 Triple 三个槽位全空（全通配），直接跳过：等价于 Pattern{}，
// 会让 Match 返回整个图，topK 截断后还能看，但没语义价值。
func toPatterns(in []patternTriple, allowed map[string]bool, topK int) ([]graphstore.Pattern, error) {
	out := make([]graphstore.Pattern, 0, len(in))
	for i, pt := range in {
		s, err := validateSlot("subject", pt.Subject, allowed)
		if err != nil {
			return nil, wrapSlotErr(i, err)
		}
		p, err := validateSlot("predicate", pt.Predicate, allowed)
		if err != nil {
			return nil, wrapSlotErr(i, err)
		}
		o, err := validateSlot("object", pt.Object, allowed)
		if err != nil {
			return nil, wrapSlotErr(i, err)
		}
		if s == nil && p == nil && o == nil {
			continue
		}
		out = append(out, graphstore.Pattern{S: s, P: p, O: o, Limit: topK})
	}
	if len(out) == 0 {
		return nil, errors.E(errors.CodeModelOutputInvalid,
			"nlquery: 模型没有给出任何合法的查询子句（全部槽位为空或非法）")
	}
	return out, nil
}

// validateSlot 把 LLM 输出的字符串槽位转换成 *graphstore.Term。
//
// 空串 → nil（通配）。非空 → 校验白名单 → KindIRI Term。
// 白名单校验对大小写敏感：IRI 本身就是大小写敏感的全局标识符，静默归一
// （比如把 Http:// 和 http:// 当作一样）会引入语义漂移——两条不同的
// 错误 IRI 被当作相同，实际图里根本找不到，却静默返回空集。
func validateSlot(name, raw string, allowed map[string]bool) (*graphstore.Term, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return nil, nil
	}
	if !allowed[v] {
		return nil, errors.E(errors.CodeValidationFailed,
			"nlquery: LLM 输出引用了白名单外的 IRI（幻觉）").
			WithDetails(map[string]any{
				"slot": name,
				"iri":  v,
			})
	}
	return &graphstore.Term{Kind: graphstore.KindIRI, Lexical: v}, nil
}

func wrapSlotErr(idx int, err error) error {
	if d := errors.As(err).Details; d != nil {
		d["triple_index"] = idx
	}
	return err
}

// clamp01 把 confidence 收敛到 [0,1]。
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// clip 截断字符串（留前 N 字符），避免把模型的大段回复直接塞进错误详情里。
func clip(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
