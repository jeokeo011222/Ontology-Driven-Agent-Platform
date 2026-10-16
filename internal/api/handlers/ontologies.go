package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"odap/internal/api/sse"
	"odap/internal/api/store"
	"odap/internal/ontology/graphload"
	"odap/internal/ontology/shacl"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// createOntologyRequest 是 POST /api/v1/ontologies 的请求体。
type createOntologyRequest struct {
	OntologyKey string          `json:"ontology_key"`
	Name        string          `json:"name"`
	Domain      string          `json:"domain"`
	Granularity string          `json:"granularity"`
	Description string          `json:"description,omitempty"`
	Version     string          `json:"version,omitempty"`
	Manifest    json.RawMessage `json:"manifest"`
}

// ListOntologies 处理 GET /api/v1/ontologies。
func (h *Handler) ListOntologies(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	limit, err := limitParam(r, 10)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	res, err := h.deps.Store.ListOntologies(r.Context(), limit, r.URL.Query().Get("cursor"), r.URL.Query().Get("status"), r.URL.Query().Get("q"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, res)
}

// CreateOntology 处理 POST /api/v1/ontologies。
func (h *Handler) CreateOntology(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	var req createOntologyRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	out, err := h.deps.Store.CreateOntology(r.Context(), store.CreateOntologyInput{
		OntologyKey: req.OntologyKey,
		Name:        req.Name,
		Domain:      req.Domain,
		Granularity: req.Granularity,
		Description: req.Description,
		Version:     req.Version,
		Manifest:    req.Manifest,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "OntologyCreated", "ontology.create", "SUCCESS", "ontology",
		stringify(out["id"]), ids.ID{}, map[string]any{"ontology_key": req.OntologyKey, "idempotency_key": idempotencyKey(r)})
	h.writeJSON(w, http.StatusCreated, out)
}

// ontologyValidatorVersion 记录在 validation_runs.validator_version 上的校验器版本。
//
// 必须显式记录：同一份 manifest 在不同版本的校验器下可能给出不同结论（例如新增了
// 一条支持的关键字、或修了一个误报）。没有这个字段，"上周还是 VALID、今天变 REJECTED"
// 就无法区分是数据变了还是校验器变了。
const ontologyValidatorVersion = "odap-shacl/1.0.0"

// ValidateOntology 处理 POST /api/v1/ontologies/{id}/validate。
//
// 执行链（每一步都真实执行，无占位、无静默降级）：
//  1. 读本体最新版本的 manifest；
//  2. graphload.ParseManifest 做结构与闭包校验（与生成端同一份实现）；
//  3. 投影为 []shacl.Shape，并按 (ontology_key, version_id) 派生确定性命名图 IRI；
//  4. 把 TBox + ABox 幂等装载进命名图；
//  5. **反假绿探针**：图中属于目标类的实例数为 0 时拒绝校验（详见下方说明）；
//  6. shacl.Validate 真实执行形状校验；
//  7. 同一事务内落 validation_runs + validation_findings，并把版本置为 VALID / REJECTED；
//  8. 能否发布由既有 PublishOntology 门禁决定（只认 VALID / APPROVED）。
func (h *Handler) ValidateOntology(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	if h.deps.GraphStore == nil {
		// 校验必须真读图。没有图存储时，"没查到问题"与"根本没查"无法区分，
		// 因此只能明确不可用，而不是返回一个空结论。
		h.fail(w, r, unavailable(
			"图存储未装配：SHACL 校验需要先装载本体与实例到命名图再真实读取，未装配时无法给出校验结论"))
		return
	}
	ontologyID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ctx := r.Context()

	snap, err := h.deps.Store.OntologyVersionSnapshot(ctx, ontologyID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ns, err := graphload.NewNamespace(snap.OntologyKey)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	parsed, err := graphload.ParseManifest(snap.Manifest)
	if err != nil {
		h.recordAudit(r, id, "OntologyValidationRejected", "ontology.validate", "DENIED", "ontology",
			ontologyID.String(), ids.ID{}, map[string]any{
				"version": snap.Version, "reason": "manifest_invalid",
			})
		h.fail(w, r, err)
		return
	}
	// 只有类、没有任何约束来源（shapes 与 constraints 双通道全空）的本体：
	// 没有可执行的规则。此时"通过"是无意义的（任何数据都会通过），必须显式拒绝，
	// 否则会把"没写约束"伪装成"数据合规"。
	if len(parsed.Shapes) == 0 && len(parsed.Constraints) == 0 {
		err := errors.E(errors.CodeValidationFailed,
			"本体未定义任何可执行约束（manifest.shapes 与 manifest.constraints 均为空）：没有约束就没有可判定的规则，无法判定为通过").
			WithDetails(map[string]any{
				"ontology_id": ontologyID.String(), "version": snap.Version,
				"hint": "请在生成时让模型产出 shapes（形状骨架）或 constraints（业务硬约束），或手工为 manifest 补上",
			})
		h.recordAudit(r, id, "OntologyValidationRejected", "ontology.validate", "DENIED", "ontology",
			ontologyID.String(), ids.ID{}, map[string]any{
				"version": snap.Version, "reason": "no_shapes",
			})
		h.fail(w, r, err)
		return
	}
	shapes, err := parsed.BuildShapes(ns)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	graphIRI, err := graphload.GraphIRI(snap.OntologyKey, snap.VersionID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	tbox, err := parsed.TBox(ns)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	abox, err := parsed.ABox(ns)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// 幂等装载。不单独先调 EnsureGraph：PGStore 的写入路径以 graph_type='INSTANCE'
	// 建图，先显式建一次再被覆盖只会留下一条与最终状态不符的记录——比不建更误导。
	if _, err := h.deps.GraphStore.InsertTriples(ctx, id.TenantID, graphIRI, append(tbox, abox...)); err != nil {
		h.fail(w, r, err)
		return
	}

	// ---- 反假绿探针（必须在写任何结论之前）----
	//
	// 现实中最容易发生的失败不是"数据违规"，而是"数据没装载"：命中 sh:targetClass 的
	// 实例数为 0 时，SHACL 按规范返回 Conforms=true 且无 finding —— 与"数据全部合规"
	// 在返回值上完全一致。若照常落库，就会得到一个**显示通过、实际什么都没查**的门禁。
	// 因此这里先证明"图里确实有待校验的实例"，再执行校验。
	targets := parsed.TargetClassIRIs(ns)
	focusNodes, err := graphload.CountTypedNodes(ctx, h.deps.GraphStore, id.TenantID, graphIRI, targets)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if focusNodes == 0 {
		err := errors.E(errors.CodeValidationFailed,
			"图中没有可校验的实例：仅装载了本体结构（TBox），未装载任何属于目标类的实例数据（ABox）。"+
				"此时 SHACL 会返回\"通过\"——那只说明没数据可查，不说明数据合规，故拒绝落库").
			WithDetails(map[string]any{
				"ontology_id": ontologyID.String(), "version": snap.Version,
				"graph_iri": graphIRI, "target_classes": targets,
				"tbox_triples": len(tbox), "abox_triples": len(abox),
			})
		h.recordAudit(r, id, "OntologyValidationRejected", "ontology.validate", "DENIED", "ontology",
			ontologyID.String(), ids.ID{}, map[string]any{
				"version": snap.Version, "reason": "no_focus_nodes",
				"graph_iri": graphIRI, "target_classes": targets,
			})
		h.fail(w, r, err)
		return
	}

	// 探针通过后才进入"校验中"状态：这样就不会出现"探针拒绝、版本却卡在 VALIDATING"
	// 的组合（VALIDATING 的版本既不能发布也不能说明发生了什么）。
	startedAt := time.Now().UTC()
	if _, err := h.deps.Store.MarkOntologyValidating(ctx, ontologyID); err != nil {
		h.fail(w, r, err)
		return
	}

	report, err := shacl.Validate(h.deps.GraphStore, id.TenantID, graphIRI, shapes)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	newStatus := "REJECTED"
	if report.Conforms {
		newStatus = "VALID"
	}
	reportJSON, _ := json.Marshal(map[string]any{
		"conforms": report.Conforms, "results": report.Results,
	})
	rec, err := h.deps.Store.RecordOntologyValidation(ctx, store.RecordOntologyValidationInput{
		OntologyID:       snap.OntologyID,
		VersionID:        snap.VersionID,
		ValidatorVersion: ontologyValidatorVersion,
		InputDigest:      digestHex(snap.Manifest),
		ReportDigest:     digestHex(reportJSON),
		StartedAt:        &startedAt,
		// 语法（manifest 结构与词项合法性）已由 ParseManifest 验证通过，因此为 true。
		SyntaxValid:     true,
		SHACLConformant: report.Conforms,
		// OWL 一致性 与 业务规则 两项**未执行**，故留 NULL 而不是 false：
		// false 表示"查了且不通过"，会凭空造出失败；true 则是假绿。
		OWLConsistent:      nil,
		BusinessRulesValid: nil,
		Findings:           findingsFromReport(report),
		NewStatus:          newStatus,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}

	h.recordAudit(r, id, "OntologyValidated", "ontology.validate", "SUCCESS", "ontology",
		ontologyID.String(), ids.ID{}, map[string]any{
			"version":      snap.Version,
			"conforms":     report.Conforms,
			"status":       stringify(rec["status"]),
			"findings":     len(report.Results),
			"focus_nodes":  focusNodes,
			"graph_iri":    graphIRI,
			"validator":    ontologyValidatorVersion,
			"input_digest": digestHex(snap.Manifest),
		})
	h.publish(id.TenantID.String(), "OntologyValidated", sse.Important,
		map[string]any{
			"ontology_id": ontologyID.String(), "version_id": snap.VersionID,
			"conforms": report.Conforms, "status": stringify(rec["status"]),
		})

	// 发现项复用与 GET /ontologies/{id} 完全相同的投影，前端两处展示不会出现
	// "同一条 finding 在详情页叫违规、在校验响应里叫 ERROR" 的分裂。
	stored, err := h.deps.Store.OntologyValidationFindings(ctx, ontologyID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"ontology_id": snap.OntologyID,
		"version_id":  snap.VersionID,
		"version":     snap.Version,
		// version_status 与 run_status 必须分开命名：前者是版本在状态机中的位置
		// （DRAFT/VALIDATING/VALID/REJECTED，决定能否发布），后者是本次校验运行的结论
		// （PASSED/FAILED）。两个都叫 status 时，调用方无法判断该去重试验证还是去修数据。
		"version_status":    stringify(rec["status"]),
		"conforms":          report.Conforms,
		"validation_run_id": stringify(rec["validation_run_id"]),
		"run_status":        stringify(rec["run_status"]),
		"validator_version": ontologyValidatorVersion,
		"findings":          stored["items"],
		"graph": map[string]any{
			"graph_iri":      graphIRI,
			"target_classes": targets,
			"focus_nodes":    focusNodes,
			"tbox_triples":   len(tbox),
			"abox_triples":   len(abox),
		},
		"checks": map[string]any{
			"syntax_valid": true,
			// 未执行的两项显式给 null：写 true 是假绿，写 false 则凭空造出失败。
			"shacl_conformant":     report.Conforms,
			"owl_consistent":       nil,
			"business_rules_valid": nil,
		},
	})
}

// findingsFromReport 把 SHACL 报告投影为待落库的发现项。
//
// Component（MinCount / Datatype / Pattern …）直接作为 finding_code：它是稳定的
// 机器可读标识，前端据此做分组与统计；message 才是给人看的说明。若把 message 当
// code，前端就只能靠字符串匹配分组，文案一改统计立刻崩。
func findingsFromReport(report shacl.Report) []store.ValidationFindingInput {
	out := make([]store.ValidationFindingInput, 0, len(report.Results))
	for _, res := range report.Results {
		details, _ := json.Marshal(map[string]any{
			"component": res.Component, "shacl_severity": res.Severity,
		})
		out = append(out, store.ValidationFindingInput{
			Severity:    dbSeverityOf(res.Severity),
			Code:        res.Component,
			FocusNode:   res.FocusNode,
			Path:        res.Path,
			Message:     res.Message,
			Digest:      res.Digest(),
			DetailsJSON: details,
		})
	}
	return out
}

// dbSeverityOf 把 SHACL 严重级别映射到 validation_findings.severity 的大写枚举。
//
// default 分支按阻断（ERROR）处理而不是放行：未知取值意味着"有东西没通过但级别没读懂"，
// 此时把它当作不阻断，门禁就被绕过了。这一分支的代价是可能多拦一次，方向是对的。
func dbSeverityOf(shaclSeverity string) string {
	switch shaclSeverity {
	case string(shacl.SeverityWarning):
		return "WARNING"
	case string(shacl.SeverityInfo):
		return "INFO"
	default: // Violation 及任何未知取值
		return "ERROR"
	}
}

// publishOntologyRequest 是 POST /api/v1/ontologies/{id}/publish 的请求体。
type publishOntologyRequest struct {
	Version     string `json:"version,omitempty"`
	Environment string `json:"environment,omitempty"`
}

// PublishOntology 处理 POST /api/v1/ontologies/{id}/publish。
func (h *Handler) PublishOntology(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	ontologyID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var req publishOntologyRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	out, err := h.deps.Store.PublishOntology(r.Context(), ontologyID, req.Version, req.Environment)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "OntologyPublished", "ontology.publish", "SUCCESS", "ontology", ontologyID.String(), ids.ID{},
		map[string]any{"environment": req.Environment, "idempotency_key": idempotencyKey(r)})
	h.publish(id.TenantID.String(), "OntologyPublished", sse.Important, map[string]any{"ontology_id": ontologyID.String()})
	h.writeJSON(w, http.StatusOK, out)
}

// GetOntologyArtifacts 处理 GET /api/v1/ontologies/{id}/artifacts。
//
// 由最新版本的 manifest **派生** RDF / OWL / SHACL 三份文本（复用 graphload 的
// TBox / ABox / BuildShapes 投影），因此：
//   - 不需要先跑校验，DRAFT 版本同样可见；
//   - 三份文本与"校验时实际装载进命名图、实际求值的形状"逐条一致（同一份投影函数）；
//   - 不需要图存储（与 ValidateOntology 不同）——本端点不发生任何图读写。
//
// 与 ValidateOntology 的另一点不同：**shapes 为空不报错**。没有形状是"这份本体尚未
// 声明约束"的正常状态，此时 SHACL 页显示空态，而不是伪造内容或把它当成校验失败。
func (h *Handler) GetOntologyArtifacts(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	ontologyID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ctx := r.Context()

	snap, err := h.deps.Store.OntologyVersionSnapshot(ctx, ontologyID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ns, err := graphload.NewNamespace(snap.OntologyKey)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	parsed, err := graphload.ParseManifest(snap.Manifest)
	if err != nil {
		// 清单结构/闭包非法时不产出任何文本：半份产物比没有产物更具误导性
		// （页面上看不出到底缺了什么）。错误原样透出，其中带可定位的 JSON Pointer。
		h.fail(w, r, err)
		return
	}
	graphIRI, err := graphload.GraphIRI(snap.OntologyKey, snap.VersionID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	tbox, err := parsed.TBox(ns)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	abox, err := parsed.ABox(ns)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	rdf, err := parsed.RDFTurtle(ns)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	owl, err := parsed.OWLRDFXML(ns)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	shaclText, err := parsed.SHACLTurtle(ns)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// 两张图（本体结构 / 形状约束）从与上文同一份投影派生：界面上的节点与边因此
	// 都能在源码页签里逐条找到对应语句。includeInstances 只是"这一份投影里要不要
	// 画出 ABox"，不改变它取自 manifest 这一事实；实例开关由界面切换，不需要再发请求。
	// 形状图投影失败（内部不变量被破坏）时整体报错，不返回一张与 SHACL 文本对不上的图。
	shapeGraph, err := parsed.ShapeGraph(ns)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"ontology_id":  snap.OntologyID,
		"ontology_key": snap.OntologyKey,
		"version_id":   snap.VersionID,
		"version":      snap.Version,
		"graph_iri":    graphIRI,
		"rdf":          map[string]any{"format": "text/turtle", "content": rdf},
		"owl":          map[string]any{"format": "application/rdf+xml", "content": owl},
		"shacl":        map[string]any{"format": "text/turtle", "content": shaclText},
		// graph.ontology 含实例，graph.ontology_tbox_only 只有类与关系：
		// 两份都返回而不是让前端自己过滤，是为了让"图里该有什么"只由后端定义一次。
		"graph": map[string]any{
			"ontology":           parsed.OntologyGraph(ns, true),
			"ontology_tbox_only": parsed.OntologyGraph(ns, false),
			"shapes":             shapeGraph,
		},
		// counts 全部取自 manifest（而非图存储）：manifest 是本端点的唯一真值来源，
		// 而"图里有多少实例"属于运行时状态，图未装载时会得到 0 并被误读为"没有实例"，
		// 因此这里不给那个数字。
		"counts": map[string]any{
			"tbox_triples":   len(tbox),
			"abox_triples":   len(abox),
			"shapes":         len(parsed.Shapes),
			"constraints":    len(parsed.Constraints),
			"target_classes": len(parsed.TargetClassIRIs(ns)),
			"instances":      parsed.InstanceCount(),
		},
	})
}

// ontologyClassesFromManifest 把本体详情里的 manifest 投影为页面所需的「类与关系」。
//
// 为什么不放在 store 层、也不交给前端解析：manifest → 类/关系的映射是**本体语义投影**
// （词项 IRI 的构造、关系按 from_class 归属），与持久化无关。放在这里可复用 graphload
// 的命名空间与清单解析，使详情页、校验、产物端点对"类与关系"只有一份理解，不会各自漂移。
// 此前详情接口不返回 classes，页面直接读 ontology.classes.length 抛 TypeError——
// 只在 VITE_USE_FIXTURES=1 的演示数据下有数据，掩盖了该缺陷。
//
// 三种情形必须严格区分，不得混为一谈：
//   - 无版本（manifest 为 nil）：返回**空数组**——没有版本就是没有类，页面显示空态；
//   - 能解析：返回投影结果；
//   - 存在但解析失败：返回错误（而不是空数组），否则"清单坏了"会被显示成"尚未定义"。
func ontologyClassesFromManifest(manifest any, ontologyKey string) ([]map[string]any, error) {
	if manifest == nil {
		return []map[string]any{}, nil
	}
	// GetOntology 的 SELECT 未对 jsonb 做 ::text 转换，pgx 因此把它解成 map[string]any，
	// 需要重新编码为字节再交给清单解析器（校验路径走的是 ::text 直读，见
	// store.OntologyVersionSnapshot 的同款说明）。
	raw, err := json.Marshal(manifest)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "本体清单序列化失败")
	}
	ns, err := graphload.NewNamespace(ontologyKey)
	if err != nil {
		return nil, err
	}
	m, err := graphload.ParseManifest(raw)
	if err != nil {
		return nil, err
	}
	classes := make([]map[string]any, 0, len(m.Classes))
	for _, c := range m.Classes {
		relations := make([]map[string]any, 0, len(m.Relations))
		for _, rel := range m.Relations {
			// 关系在清单里是全局声明（带 from_class），页面的「类与关系」按类分组，
			// 因此每个类只列出以它为起点的关系；其余归属于各自的起点类。
			if rel.FromClass != c.Name {
				continue
			}
			label := rel.Description
			if label == "" {
				label = rel.Name
			}
			relations = append(relations, map[string]any{
				"predicate": ns.RelationIRI(rel.Name),
				"target":    ns.ClassIRI(rel.ToClass),
				"label":     label,
			})
		}
		// 清单未写 label 时回落到类名：页面需要一个可读标题，用 IRI 的局部名比留空
		// 更可读，且这是同一份事实的两种呈现，不是补造数据。
		label := c.Label
		if label == "" {
			label = c.Name
		}
		classes = append(classes, map[string]any{
			"iri":         ns.ClassIRI(c.Name),
			"label":       label,
			"description": c.Description,
			"relations":   relations,
		})
	}
	return classes, nil
}
