package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/llm"
	"odap/internal/ontology/graphload"
	"odap/internal/ontology/graphstore"
	"odap/internal/ontology/shacl"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// Completer 是补全客户端的最小接口（*llm.Gateway 满足）。
type Completer interface {
	Complete(ctx context.Context, tenantID ids.ID, req llm.CompletionRequest) (llm.Completion, error)
}

// GraphWriter 是图写入的最小接口（*graphstore.GraphStore 满足）。
type GraphWriter interface {
	InsertTriples(ctx context.Context, tenantID ids.ID, graphIRI string, triples []graphstore.Triple) (int, error)
}

// ExtractOptions 是知识抽取的运行参数。
type ExtractOptions struct {
	Pool      *pgxpool.Pool
	Completer Completer
	Graph     GraphWriter
	TenantID  ids.ID
	JobID     ids.ID
	// DocumentID 只用于实例 IRI（urn:odap:kb:doc:{docID}:inst:{n}）与分块定位。
	DocumentID string
	// TargetOntologyVersionID 是抽取目标本体版本（EXTRACT 任务必需）。
	TargetOntologyVersionID string
	// ModelID 是补全点名模型；为空时由网关按能力确定性择优
	//（结构化输出优先原生 Schema 端点，复刻生成端点口径）。
	ModelID string
	// MaxRepairRounds 是校验失败后的受限重生成上限（默认 llm.MaxSchemaRepairAttempts）。
	MaxRepairRounds int
	// OnProgress 每处理完一个分块回调一次，可为 nil。
	OnProgress func(done, total int, stage string) error
}

// extractionOutput 是 LLM 结构化输出的固定骨架。
type extractionOutput struct {
	Instances []extractionInstance `json:"instances"`
	Triples   []extractionTriple   `json:"triples"`
}

type extractionInstance struct {
	ID         string         `json:"id"`
	Class      string         `json:"class"`
	Properties map[string]any `json:"properties"`
}

type extractionTriple struct {
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	Object    string `json:"object"`
}

// extractionSchema 是 ResponseSchema：additionalProperties:false、triples ≤40、
// instances 封顶 20（防止单块实例爆炸；骨架固定，模型只能填空）。
var extractionSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["instances", "triples"],
  "properties": {
    "instances": {
      "type": "array",
      "maxItems": 20,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["id", "class", "properties"],
        "properties": {
          "id":         {"type": "string", "pattern": "^[A-Za-z0-9_.-]+$"},
          "class":      {"type": "string"},
          "properties": {"type": "object"}
        }
      }
    },
    "triples": {
      "type": "array",
      "maxItems": 40,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["subject", "predicate", "object"],
        "properties": {
          "subject":   {"type": "string", "pattern": "^[A-Za-z0-9_.-]+$"},
          "predicate": {"type": "string"},
          "object":    {"type": "string", "pattern": "^[A-Za-z0-9_.-]+$"}
        }
      }
    }
  }
}`)

// ExtractDocument 对文档全部分块执行"结构化抽取 → 白名单/闭包/SHACL 校验 →
// 有界修复 → 写图 → findings 落库"的抽取回路。
//
// 修复回路与 generation.go 同型：校验失败把违规描述回灌重生成，最多
// MaxRepairRounds 轮；finish_reason=length 是确定性失败（重生成只会同样截断），
// 直接报错不进回路。
func ExtractDocument(ctx context.Context, opts ExtractOptions) error {
	if opts.Pool == nil || opts.Completer == nil || opts.Graph == nil {
		return errors.E(errors.CodeInternal, "抽取失败：依赖未装配（Pool/Completer/Graph）")
	}
	if opts.TenantID.IsZero() || opts.JobID.IsZero() {
		return errors.E(errors.CodeInternal, "抽取失败：租户或任务标识缺失")
	}
	maxRounds := opts.MaxRepairRounds
	if maxRounds <= 0 {
		maxRounds = llm.MaxSchemaRepairAttempts
	}
	// 1. 目标本体快照（classes/relations 白名单来源）。
	ontologyKey, base, err := loadOntologySnapshot(ctx, opts.Pool, opts.TenantID, opts.TargetOntologyVersionID)
	if err != nil {
		return err
	}
	// 2. 分块与目标图 IRI。
	chunks, err := loadChunks(ctx, opts.Pool, opts.TenantID, opts.DocumentID)
	if err != nil {
		return err
	}
	if len(chunks) == 0 {
		return errors.E(errors.CodeValidationFailed, "文档没有可抽取的分块（空文档）")
	}
	graphIRI, err := graphload.GraphIRI(ontologyKey, opts.TargetOntologyVersionID)
	if err != nil {
		return err
	}
	// 3. 逐分块抽取（instSeq 文档内全局递增，保证实例 IRI 唯一）。
	instSeq := 0
	for _, c := range chunks {
		if err := extractChunk(ctx, opts, base, ontologyKey, graphIRI, c, &instSeq, maxRounds); err != nil {
			return err
		}
		if err := notifyProgress(opts.OnProgress, c.index+1, len(chunks), "extract"); err != nil {
			return err
		}
	}
	return nil
}

// extractChunk 抽取单个分块：LLM 结构化输出 → 白名单/闭包/SHACL 校验（失败
// 落 findings 并构造修复提示词重生成，最多 maxRounds 轮）→ 通过后写图。
func extractChunk(ctx context.Context, opts ExtractOptions, base *graphload.Manifest, ontologyKey, graphIRI string,
	c chunkRow, instSeq *int, maxRounds int) error {
	ns, err := graphload.NewNamespace(ontologyKey)
	if err != nil {
		return err
	}
	systemPrompt := buildExtractSystemPrompt(base)
	userPrompt := buildExtractUserPrompt(base, c.content)

	var (
		out    *extractionOutput
		parsed *graphload.Manifest
		report shacl.Report
	)
	for attempts := 1; ; attempts++ {
		comp, err := callCompleter(ctx, opts.Completer, opts.TenantID, opts.ModelID,
			[]llm.Message{{Role: "system", Content: systemPrompt}, {Role: "user", Content: userPrompt}},
			extractionSchema, fmt.Sprintf("kb-extract:%s:%d:%d", opts.JobID.String(), c.index, attempts))
		if err != nil {
			return err
		}
		// finish_reason=length：确定性失败（重生成只会同样截断），不进修复回路。
		if comp.FinishReason == llm.FinishLength {
			return errors.Ef(errors.CodeModelOutputInvalid,
				"抽取输出在分块 %d 被截断（finish_reason=length）：输出上限不足，无法通过重生成修复", c.index)
		}
		out, parsed, report, err = validateExtraction(base, ns, comp.Content)
		if err == nil {
			break
		}
		// 本轮违规全量落 findings（digest 去重；整体失败时也有据可查）。
		if ferr := InsertFindings(ctx, opts.Pool, opts.TenantID, opts.JobID, c.id, report.Results); ferr != nil {
			return ferr
		}
		if attempts > maxRounds {
			// 修复回路耗尽：违规已随每轮落 findings，这里整体失败（任务按
			// 退避重试；重试重跑全部分块，图写入以三元组主键幂等去重）。
			return errors.Ef(errors.CodeModelOutputInvalid,
				"分块 %d 抽取在 %d 轮受限重生成后仍不合规：%s", c.index, maxRounds, err.Error())
		}
		// 构造修复提示词（复刻 generation.go 的修复模式）：回灌上一轮输出 +
		// 逐条违规描述 + 白名单与骨架要求。
		userPrompt = buildRepairPrompt(base, comp.Content, report)
	}

	// 写图：把 graphload 命名空间下的实例 IRI 改写为知识库实例 IRI
	//（urn:odap:kb:doc:{docID}:inst:{n}）。该前缀不是 graphstore 的空白节点
	// 保留前缀（urn:odap:bnode:），读写往返保持 IRI 语义。
	rewrite := make(map[string]string, len(out.Instances))
	for i := range out.Instances {
		*instSeq++
		rewrite[ns.InstanceIRI(out.Instances[i].ID)] =
			fmt.Sprintf("urn:odap:kb:doc:%s:inst:%d", opts.DocumentID, *instSeq)
	}
	abox, err := parsed.ABox(ns)
	if err != nil {
		return errors.Wrap(err, errors.CodeInternal, "生成实例三元组失败")
	}
	final := make([]graphstore.Triple, 0, len(abox))
	for _, t := range abox {
		if t.S.Kind == graphstore.KindIRI {
			if v, ok := rewrite[t.S.Lexical]; ok {
				t.S.Lexical = v
			}
		}
		if t.O.Kind == graphstore.KindIRI {
			if v, ok := rewrite[t.O.Lexical]; ok {
				t.O.Lexical = v
			}
		}
		final = append(final, t)
	}
	if _, err := opts.Graph.InsertTriples(ctx, opts.TenantID, graphIRI, final); err != nil {
		return errors.Wrap(err, errors.CodeInternal, "写入抽取三元组失败")
	}
	return nil
}

// validateExtraction 解析并校验 LLM 抽取输出，返回：结构化输出、合并后的
// Manifest（闭包已验证，供 ABox 生成）、SHACL 报告。
// 非 nil error 表示本轮不合规（白名单/闭包/SHACL Violation，均可进修复回路）。
func validateExtraction(base *graphload.Manifest, ns graphload.Namespace, content string) (
	*extractionOutput, *graphload.Manifest, shacl.Report, error) {
	var noReport shacl.Report
	// 1. 解析 JSON（固定骨架：未知字段即违规，触发修复）。
	dec := json.NewDecoder(strings.NewReader(content))
	dec.DisallowUnknownFields()
	var out extractionOutput
	if err := dec.Decode(&out); err != nil {
		return nil, nil, noReport, errors.E(errors.CodeModelOutputInvalid,
			"输出不是符合固定骨架的合法 JSON："+err.Error())
	}
	// 2. 白名单校验：class 取自 classes、predicate 取自 relations、
	// 三元组两端必须引用已声明的实例 id。
	if err := checkWhitelist(base, &out); err != nil {
		return nil, nil, noReport, err
	}
	// 3. 把 triples 合并进实例关系属性，构造「目标本体 classes/relations + 抽取
	// 实例」的清单做闭包校验（关系方向、to_class 归属、属性类型全部复用
	// ParseManifest 的既有校验）。
	mergeTriples(&out)
	parsed, err := graphload.ParseManifest(mergedManifest(base, &out))
	if err != nil {
		return nil, nil, noReport, errors.E(errors.CodeValidationFailed, "闭包校验失败："+err.Error())
	}
	// 4. SHACL 校验：MemStore 装载 TBox + 抽取实例（标准 ns IRI，写图期再改写），
	// 绝不把修复轮次的中间产物写进真实图存储。
	shapes, err := parsed.BuildShapes(ns)
	if err != nil {
		return nil, nil, noReport, err
	}
	mem := graphstore.NewMemStore()
	tbox, err := parsed.TBox(ns)
	if err != nil {
		return nil, nil, noReport, err
	}
	abox, err := parsed.ABox(ns)
	if err != nil {
		return nil, nil, noReport, err
	}
	// 探针图 IRI 不得使用保留命名空间（graphstore.validateGraphIRI 拒绝 urn:odap:）。
	const probeGraph = "https://odap.example/kb/extract-validation"
	if _, err := mem.InsertTriples(context.Background(), validationTenantID(), probeGraph, append(tbox, abox...)); err != nil {
		return nil, nil, noReport, err
	}
	rep, err := shacl.Validate(mem, validationTenantID(), probeGraph, shapes)
	if err != nil {
		return nil, nil, noReport, err
	}
	// 5. 只有 Violation 阻断（与生成端点预检口径一致）；Warning/Info 只记录。
	for _, res := range rep.Results {
		if res.Severity == string(shacl.SeverityViolation) {
			return &out, parsed, rep, errors.E(errors.CodeValidationFailed, "SHACL 校验存在 Violation")
		}
	}
	return &out, parsed, rep, nil
}

// checkWhitelist 校验输出白名单。
func checkWhitelist(base *graphload.Manifest, out *extractionOutput) error {
	classes := map[string]bool{}
	for _, c := range base.Classes {
		classes[c.Name] = true
	}
	relations := map[string]bool{}
	for _, r := range base.Relations {
		relations[r.Name] = true
	}
	instIDs := map[string]bool{}
	for i := range out.Instances {
		in := &out.Instances[i]
		if in.ID == "" {
			return errors.Ef(errors.CodeModelOutputInvalid, "instances[%d].id 为空", i)
		}
		if instIDs[in.ID] {
			return errors.Ef(errors.CodeModelOutputInvalid, "实例 id 重复：%s", in.ID)
		}
		instIDs[in.ID] = true
		if !classes[in.Class] {
			return errors.Ef(errors.CodeModelOutputInvalid,
				"实例 %s 的 class %q 不在目标本体 classes 白名单内", in.ID, in.Class)
		}
	}
	for i := range out.Triples {
		t := &out.Triples[i]
		if !instIDs[t.Subject] {
			return errors.Ef(errors.CodeModelOutputInvalid,
				"triples[%d].subject %q 未在 instances 中声明", i, t.Subject)
		}
		if !instIDs[t.Object] {
			return errors.Ef(errors.CodeModelOutputInvalid,
				"triples[%d].object %q 未在 instances 中声明", i, t.Object)
		}
		if !relations[t.Predicate] {
			return errors.Ef(errors.CodeModelOutputInvalid,
				"triples[%d].predicate %q 不在目标本体 relations 白名单内", i, t.Predicate)
		}
	}
	return nil
}

// mergeTriples 把三元组（subject --predicate--> object）合并进实例的关系属性
// （key=predicate，value=[]string），使闭包校验与 ABox 生成复用 graphload 既有实现。
func mergeTriples(out *extractionOutput) {
	byID := make(map[string]*extractionInstance, len(out.Instances))
	for i := range out.Instances {
		in := &out.Instances[i]
		if in.Properties == nil {
			in.Properties = map[string]any{}
		}
		byID[in.ID] = in
	}
	for _, t := range out.Triples {
		in, ok := byID[t.Subject]
		if !ok {
			continue
		}
		switch v := in.Properties[t.Predicate].(type) {
		case nil:
			in.Properties[t.Predicate] = []string{t.Object}
		case []string:
			in.Properties[t.Predicate] = append(v, t.Object)
		default:
			in.Properties[t.Predicate] = []string{fmt.Sprint(v), t.Object}
		}
	}
}

// mergedManifest 构造「目标本体 classes/relations/enumerations/shapes/constraints
// + 抽取实例」的清单 JSON（交给 ParseManifest 做闭包校验与 BuildShapes）。
func mergedManifest(base *graphload.Manifest, out *extractionOutput) []byte {
	payload := map[string]any{
		"summary":   base.Summary,
		"classes":   base.Classes,
		"relations": base.Relations,
		"instances": out.Instances,
	}
	if len(base.Enumerations) > 0 {
		payload["enumerations"] = base.Enumerations
	}
	if len(base.Shapes) > 0 {
		payload["shapes"] = base.Shapes
	}
	if len(base.Constraints) > 0 {
		payload["constraints"] = base.Constraints
	}
	b, _ := json.Marshal(payload)
	return b
}

// callCompleter 封装一次补全调用（结构化输出 + 幂等键）。
func callCompleter(ctx context.Context, c Completer, tenantID ids.ID, modelID string,
	msgs []llm.Message, schema json.RawMessage, idemKey string) (llm.Completion, error) {
	if c == nil {
		return llm.Completion{}, errors.E(errors.CodeInternal, "补全客户端未装配")
	}
	return c.Complete(ctx, tenantID, llm.CompletionRequest{
		TenantID:           tenantID,
		ModelID:            modelID,
		Messages:           msgs,
		ResponseSchema:     schema,
		MaxOutputTokens:    4096,
		DataClassification: "INTERNAL",
		IdempotencyKey:     idemKey,
	})
}

// loadOntologySnapshot 读取目标本体版本的 ontology_key 与 manifest。
func loadOntologySnapshot(ctx context.Context, pool *pgxpool.Pool, tenantID ids.ID, versionID string) (string, *graphload.Manifest, error) {
	if versionID == "" {
		return "", nil, errors.E(errors.CodeInvalidArgument, "抽取失败：任务缺少 target_ontology_version_id")
	}
	var key, manifestText string
	err := tenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		// manifest 必须 ::text：pgx 把 jsonb 解成 map，ParseManifest 需要原始 JSON。
		return tx.QueryRow(ctx, `
            SELECT o.ontology_key, v.manifest::text
              FROM ontology.ontology_versions v
              JOIN ontology.ontologies o ON o.id = v.ontology_id
             WHERE v.id = $1`, versionID).Scan(&key, &manifestText)
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", nil, errors.E(errors.CodeNotFound, "抽取目标本体版本不存在")
		}
		return "", nil, errors.Wrap(err, errors.CodeInternal, "读取目标本体版本失败")
	}
	base, err := graphload.ParseManifest([]byte(manifestText))
	if err != nil {
		return "", nil, errors.E(errors.CodeValidationFailed, "目标本体清单解析失败："+err.Error())
	}
	return key, base, nil
}

// validationTenantID 是 MemStore 校验探针使用的租户标识（内存实现不适用 RLS，
// 只要求非零；与真实租户无关，不落任何持久化存储）。
func validationTenantID() ids.ID {
	id, _ := ids.Parse("00000000-0000-0000-0000-0000000000aa")
	return id
}

// InsertFindings 把 SHACL 结果写入 kb.extraction_findings（digest 去重，
// Violation/Warning/Info 全量落库供诊断；两个数都用 NULLIF 防御空串）。
func InsertFindings(ctx context.Context, pool *pgxpool.Pool, tenantID, jobID ids.ID,
	chunkID string, results []shacl.Result) error {
	if pool == nil || len(results) == 0 {
		return nil
	}
	return tenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		for _, res := range results {
			payload, _ := json.Marshal(map[string]any{
				"shape_id":   res.ShapeID,
				"focus_node": res.FocusNode,
				"path":       res.Path,
				"component":  res.Component,
			})
			if _, err := tx.Exec(ctx, `
                INSERT INTO kb.extraction_findings
                    (id, tenant_id, job_id, chunk_id, finding_digest, severity, message, payload)
                VALUES ($1, app.current_tenant_id(), $2, NULLIF($3, '')::uuid, $4, $5, $6, $7::jsonb)
                ON CONFLICT (job_id, finding_digest) DO NOTHING`,
				ids.New().String(), jobID.String(), chunkID, findingDigestOf(res),
				findingSeverity(res.Severity), res.Message, payload); err != nil {
				return errors.Wrap(err, errors.CodeInternal, "写入抽取发现失败")
			}
		}
		return nil
	})
}

// findingSeverity 把 SHACL severity 映射为 findings 表的枚举值。
func findingSeverity(sev string) string {
	switch sev {
	case string(shacl.SeverityViolation):
		return "ERROR"
	case string(shacl.SeverityWarning):
		return "WARNING"
	default:
		return "INFO"
	}
}

// findingDigestOf 与校验管线同一规则：shape_id/focus_node/path/component/severity
// 元组的 SHA-256（0x1f 分隔），保证跨轮次可对比、可去重。
func findingDigestOf(res shacl.Result) string {
	join := strings.Join([]string{res.ShapeID, res.FocusNode, res.Path, res.Component, res.Severity}, "\x1f")
	sum := sha256.Sum256([]byte(join))
	return hex.EncodeToString(sum[:])
}

// --- 提示词（白名单来自目标本体，骨架固定） ---

// buildExtractSystemPrompt 构造抽取系统提示词：class/predicate 白名单硬约束。
func buildExtractSystemPrompt(base *graphload.Manifest) string {
	var b strings.Builder
	b.WriteString("你是企业知识库的信息抽取引擎。从给定文本片段中抽取实体与关系，输出 JSON。\n")
	b.WriteString("硬性规则：\n")
	b.WriteString("1. 只输出一个 JSON 对象，形如 {\"instances\":[...],\"triples\":[...]}，不要输出任何其他文字。\n")
	b.WriteString("2. instances[].class 必须逐字取自下方「可选类」清单，禁止发明类。\n")
	b.WriteString("3. triples[].predicate 必须逐字取自下方「可选关系」清单，禁止发明关系；关系方向必须与声明的 from/to 一致。\n")
	b.WriteString("4. triples[].subject/object 必须引用本次 instances 中已声明的 id。\n")
	b.WriteString("5. instances[].id 用 e1、e2 等短标识；instances[].properties 的键必须逐字取自该类的「属性」或「可选关系」，属性值统一输出字符串。\n")
	b.WriteString("6. 文本中没有可抽取内容时输出空数组，禁止编造。\n")
	b.WriteString(buildWhitelistPrompt(base))
	return b.String()
}

// buildExtractUserPrompt 构造用户提示词：本体概要 + 文本片段。
func buildExtractUserPrompt(base *graphload.Manifest, chunk string) string {
	var b strings.Builder
	if strings.TrimSpace(base.Summary) != "" {
		b.WriteString("本体概要：" + strings.TrimSpace(base.Summary) + "\n\n")
	}
	b.WriteString("待抽取文本片段：\n<<<\n" + chunk + "\n>>>\n")
	b.WriteString("请按系统规则输出 JSON。")
	return b.String()
}

// buildWhitelistPrompt 输出类/关系白名单。
func buildWhitelistPrompt(base *graphload.Manifest) string {
	var b strings.Builder
	b.WriteString("\n可选类（class 只能取自这里）：\n")
	for _, c := range base.Classes {
		b.WriteString("- " + c.Name)
		if len(c.Attributes) > 0 {
			names := make([]string, 0, len(c.Attributes))
			for _, a := range c.Attributes {
				names = append(names, a.Name)
			}
			b.WriteString("（属性：" + strings.Join(names, "、") + "）")
		}
		b.WriteString("\n")
	}
	b.WriteString("\n可选关系（predicate 只能取自这里，方向固定）：\n")
	for _, r := range base.Relations {
		b.WriteString(fmt.Sprintf("- %s（%s → %s）\n", r.Name, r.FromClass, r.ToClass))
	}
	return b.String()
}

// buildRepairPrompt 构造修复提示词：回灌上一轮输出 + 违规明细 + 白名单与骨架要求。
func buildRepairPrompt(base *graphload.Manifest, previous string, report shacl.Report) string {
	var b strings.Builder
	b.WriteString("你上一次的输出未通过校验，请修复后重新输出完整 JSON。\n\n")
	b.WriteString("上一轮输出：\n<<<\n" + previous + "\n>>>\n\n")
	if len(report.Results) > 0 {
		b.WriteString("违规明细：\n")
		for i, res := range report.Results {
			if i >= 20 {
				break
			}
			b.WriteString(fmt.Sprintf("- [%s] %s %s %s：%s\n",
				res.Severity, res.ShapeID, res.FocusNode, res.Path, res.Message))
		}
		b.WriteString("\n")
	}
	b.WriteString("修复要求：\n")
	b.WriteString("1. 逐条修正上述错误；无法确定的字段直接删除而不是猜测。\n")
	b.WriteString("2. class/predicate 仍必须取自白名单：\n")
	b.WriteString(buildWhitelistPrompt(base))
	b.WriteString("3. 只输出修复后的完整 JSON 对象，不要任何解释。")
	return b.String()
}
