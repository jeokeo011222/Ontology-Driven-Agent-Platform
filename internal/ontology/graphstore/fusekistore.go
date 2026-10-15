package graphstore

// FusekiStore 是 GraphStore 的 Apache Jena Fuseki + TDB2 适配器。
//
// 设计依据：docs/图数据库的选型.md（选型结论）与 ADR-005。
//
// # 协议
//
// 只使用 SPARQL 1.1 Protocol 的两个显式端点，不依赖 Graph Store Protocol 的
// "整图替换" 语义（GSP 的 PUT/POST 是替换/追加整图，与 ODAP 需要的
// "按模式增删 + 精确计数"不匹配，且难以给出幂等的增删条数）：
//
//	{Endpoint}/query   POST application/sparql-query   → application/sparql-results+json
//	{Endpoint}/update  POST application/sparql-update  → 204
//
// 读写全部走 INSERT DATA / DELETE DATA / SELECT，不使用 LOAD、不支持 SERVICE，
// 也不把调用方文本拼进查询结构（见 rdf.go 的转义与校验）。
//
// # 与 PostgreSQL 方案的语义差异（显式声明，不模拟）
//
//   - **命名图元数据**：PG 方案把 graph_type / ontology_version 存在
//     graph.named_graphs；TDB2 没有等价的图元数据表。按选型文档的边界
//     （"本体版本、权限、业务流程、验证任务和证据治理仍然由 ODAP 自己负责"），
//     这些治理元数据仍应保存在 PostgreSQL，本适配器不假装存了它们。
//     因此 EnsureGraph 只做入参校验并返回**稳定句柄**（由 tenant+graphIRI 派生，
//     跨进程可复现，但非持久主键），调用方需要跨进程一致的图标识时以 graph_iri 为准。
//   - **事实元数据**：fact_kind / observed_at / valid_from / valid_to 是 PG 侧的
//     治理元数据（区分来源事实与推理物化事实、时间旅行）。纯 RDF 存储中不存在
//     这些维度，本适配器写入时忽略它们、读取时固定返回 FactKind=ASSERTED 与零值
//     时间（不填 now()，避免把"未知"伪装成"已知"）。
//   - **词项资源字典**：TDB2 由引擎自身的节点字典承担去重，本适配器**不实现**
//     TermDictionary（调用方的类型断言会失败，而不是拿到一个假 term_id）。
//   - **批量写入的原子性**：INSERT DATA 在一次请求内是原子的；但跨
//     PostgreSQL / Fuseki / ERP 的业务操作**不因此获得分布式事务**（选型文档
//     §三 的明确边界）。调用方必须自行设计补偿或对账。
//
// 以上差异全部通过 Capabilities() 显式声明，调用方可在装配期判定，
// 而不是等到数据出错才发现"适配器少存了一维"。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

const (
	// defaultFusekiTimeout 是单次 HTTP 请求的兜底超时。
	defaultFusekiTimeout = 30 * time.Second
	// maxFusekiResponseBytes 限制解析的响应体大小，防止异常响应耗尽内存。
	maxFusekiResponseBytes = 32 << 20
	// maxFusekiErrorBytes 限制错误响应体回显长度（用于排错，不回显整页 HTML）。
	maxFusekiErrorBytes = 400
)

// FusekiConfig 是 Fuseki 数据集的连接配置。
type FusekiConfig struct {
	// Endpoint 是数据集根地址，例如 http://fuseki:3030/odap。
	// 适配器在其后追加 /query 与 /update。
	Endpoint string
	// Username/Password 非空时使用 HTTP Basic 认证。
	//
	// ⚠️ 生产环境必须配置认证并把端口限制在受信网络（选型文档：Fuseki 是基础设施，
	// 不是业务执行引擎；未经认证的 SPARQL 端点等价于"任意 RDF 读写"）。
	Username string
	Password string
	// Timeout 是单次请求超时；<=0 时使用 defaultFusekiTimeout。
	Timeout time.Duration
	// HTTPClient 可注入自定义客户端（测试或自定义传输层）；为 nil 时按 Timeout 构造。
	HTTPClient *http.Client
}

// withDefaults 返回填入默认值后的副本。
func (c FusekiConfig) withDefaults() FusekiConfig {
	if c.Timeout <= 0 {
		c.Timeout = defaultFusekiTimeout
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: c.Timeout}
	}
	return c
}

// Validate 校验配置。
func (c FusekiConfig) Validate() error {
	if strings.TrimSpace(c.Endpoint) == "" {
		return errors.E(errors.CodeValidationFailed,
			"Fuseki 图存储已启用但未配置端点（FUSEKI_ENDPOINT）")
	}
	if !strings.HasPrefix(c.Endpoint, "http://") && !strings.HasPrefix(c.Endpoint, "https://") {
		return errors.Ef(errors.CodeValidationFailed,
			"FUSEKI_ENDPOINT 必须是 http(s) URL，得到 %q", c.Endpoint)
	}
	if (c.Username == "") != (c.Password == "") {
		return errors.E(errors.CodeValidationFailed,
			"Fuseki 认证凭据必须成对配置（FUSEKI_USERNAME 与 FUSEKI_PASSWORD）")
	}
	return nil
}

// FusekiStore 实现 GraphStore。
type FusekiStore struct {
	queryURL  string
	updateURL string
	client    *http.Client
	username  string
	password  string
}

var _ GraphStore = (*FusekiStore)(nil)

// Capabilities 声明本后端的能力边界（见类型注释的"语义差异"一节）。
func (s *FusekiStore) Capabilities() Capabilities {
	return Capabilities{
		ResourceDictionary: false, // 词项去重由 TDB2 节点字典承担，无 graph.terms 表
		PersistentGraphID:  false, // 无独立命名图元数据表，EnsureGraph 返回稳定句柄
		FactMetadata:       false, // 不保留 fact_kind 与时间维度
	}
}

// 注意：FusekiStore **不实现** TermDictionary（无 UpsertTerm 方法）。
// 这不是遗漏，而是语义表达：此后端没有独立的词项资源字典，调用方的类型断言
// 会失败，而不是拿到一个永远报错的方法或伪造的 term_id。

// NewFusekiStore 构造适配器并校验配置。
func NewFusekiStore(cfg FusekiConfig) (*FusekiStore, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()
	base := strings.TrimRight(cfg.Endpoint, "/")
	return &FusekiStore{
		queryURL:  base + "/query",
		updateURL: base + "/update",
		client:    cfg.HTTPClient,
		username:  cfg.Username,
		password:  cfg.Password,
	}, nil
}

// Endpoint 返回数据集根地址（脱敏：不含凭据，凭据从不进入 URL）。
func (s *FusekiStore) Endpoint() string {
	return strings.TrimSuffix(s.queryURL, "/query")
}

// Ping 探测图存储可用性（供 /readyz 使用）。
//
// 使用 `ASK {}`（空组图模式，合法且不触发任何数据扫描），避免用 SELECT 做探针时
// 因数据量增长而变慢——探针必须是常量代价。
func (s *FusekiStore) Ping(ctx context.Context) error {
	res, err := s.doQuery(ctx, "ASK {}")
	if err != nil {
		return err
	}
	if res.Boolean == nil || !*res.Boolean {
		return errors.E(errors.CodeUnavailable, "graphstore: Fuseki 探针 ASK {} 未返回 true")
	}
	return nil
}

// EnsureGraph 见 GraphStore 接口注释与类型注释中的"命名图元数据"一节。
//
// TDB2 的命名图在首次写入事实时物化，SPARQL 1.1 Update 也没有标准的建图语句，
// 因此本方法不发送任何写请求，只做入参校验并返回稳定句柄。
func (s *FusekiStore) EnsureGraph(_ context.Context, tenantID ids.ID, graphIRI, graphType, ontologyVersion string) (int64, error) {
	if _, err := physicalGraphIRI(tenantID, graphIRI); err != nil {
		return 0, err
	}
	if strings.TrimSpace(graphType) == "" {
		return 0, errors.E(errors.CodeValidationFailed, "graph_type 不能为空")
	}
	_ = ontologyVersion // 治理元数据留在 PostgreSQL，见类型注释
	return stableGraphHandle(tenantID, graphIRI), nil
}

// stableGraphHandle 由 (tenant, graph_iri) 派生稳定句柄。
//
// 取 sha256 前 8 字节并屏蔽符号位，保证非负且跨进程可复现。
// 它**不是**数据库主键，不得作为跨进程的图标识使用（见 GraphStore 接口注释）。
func stableGraphHandle(tenantID ids.ID, graphIRI string) int64 {
	sum := sha256.Sum256([]byte(tenantID.String() + "\x1f" + graphIRI))
	return int64(binary.BigEndian.Uint64(sum[:8]) & math.MaxInt64)
}

// InsertTriples 写入三元组，返回实际新增条数。
//
// 实现分两步，而不是"直接 INSERT DATA 后按入参长度返回"——后者会把重复三元组
// 也算作新增，属于静默虚报：
//
//  1. 用 VALUES + GRAPH 查询判定入参中哪些三元组已存在（一次往返，常量条数请求）；
//  2. 仅对不存在的部分发送一条 INSERT DATA。
func (s *FusekiStore) InsertTriples(ctx context.Context, tenantID ids.ID, graphIRI string, triples []Triple) (int, error) {
	// 参数校验先于空批次返回（见 validateWriteArgs 的说明）。
	if err := validateWriteArgs(tenantID, graphIRI, triples); err != nil {
		return 0, err
	}
	if len(triples) == 0 {
		return 0, nil
	}
	physical, err := physicalGraphIRI(tenantID, graphIRI)
	if err != nil {
		return 0, err
	}
	pending, err := dedupeTriples(graphIRI, triples)
	if err != nil {
		return 0, err
	}
	existing, err := s.existingTriples(ctx, physical, pending)
	if err != nil {
		return 0, err
	}
	toInsert := make([]Triple, 0, len(pending))
	for _, t := range pending {
		key, err := tripleKeyOf(t)
		if err != nil {
			return 0, err
		}
		if _, ok := existing[key]; ok {
			continue
		}
		toInsert = append(toInsert, t)
	}
	if len(toInsert) == 0 {
		return 0, nil
	}
	update, err := buildInsertData(physical, toInsert)
	if err != nil {
		return 0, err
	}
	if err := s.doUpdate(ctx, update); err != nil {
		return 0, err
	}
	return len(toInsert), nil
}

// DeleteTriples 删除三元组，返回实际删除条数（入参中不存在的三元组不计入）。
func (s *FusekiStore) DeleteTriples(ctx context.Context, tenantID ids.ID, graphIRI string, triples []Triple) (int, error) {
	if err := validateWriteArgs(tenantID, graphIRI, triples); err != nil {
		return 0, err
	}
	if len(triples) == 0 {
		return 0, nil
	}
	physical, err := physicalGraphIRI(tenantID, graphIRI)
	if err != nil {
		return 0, err
	}
	pending, err := dedupeTriples(graphIRI, triples)
	if err != nil {
		return 0, err
	}
	existing, err := s.existingTriples(ctx, physical, pending)
	if err != nil {
		return 0, err
	}
	toDelete := make([]Triple, 0, len(pending))
	for _, t := range pending {
		key, err := tripleKeyOf(t)
		if err != nil {
			return 0, err
		}
		if _, ok := existing[key]; ok {
			toDelete = append(toDelete, t)
		}
	}
	if len(toDelete) == 0 {
		return 0, nil
	}
	update, err := buildDeleteData(physical, toDelete)
	if err != nil {
		return 0, err
	}
	if err := s.doUpdate(ctx, update); err != nil {
		return 0, err
	}
	return len(toDelete), nil
}

// Match 按模式匹配三元组。
//
// 租户边界：GraphIRI 绑定到本租户物理图时直接 `GRAPH <...>`，不绑定时用
// `GRAPH ?g` 加 `STRSTARTS` 前缀过滤。**过滤条件由租户派生，不是可选参数**，
// 因此跨租户读取在结构上不可能发生。返回结果中若出现不属于本租户命名空间的图，
// 视为越界结果并报错（丢弃优于静默吞掉）。
//
// 位置绑定与变量投影的取舍：已绑定的位置**只投影自由变量**，绑定常量在 Go 侧
// 从 Pattern 重建，而**不**依赖引擎把常量回显为一个绑定变量。原因是"投影一个未出现
// 在模式中的变量"在 SPARQL 里是合法的但结果里没有该绑定（rdflib 直接省略，不同引擎
// 行为不一致），依赖它会让适配器在换引擎时静默丢失字段。
func (s *FusekiStore) Match(ctx context.Context, tenantID ids.ID, p Pattern) ([]Triple, error) {
	if tenantID.IsZero() {
		return nil, errors.E(errors.CodeValidationFailed, "tenant_id 不能为空")
	}
	if p.Limit < 0 {
		return nil, errors.E(errors.CodeValidationFailed, "limit 不能为负")
	}
	if len(p.freePositions()) == 0 {
		return s.matchFullyBound(ctx, tenantID, p)
	}
	query, err := buildMatchQuery(tenantID, p)
	if err != nil {
		return nil, err
	}
	res, err := s.doQuery(ctx, query)
	if err != nil {
		return nil, err
	}
	out := make([]Triple, 0, len(res.Results.Bindings))
	for _, binding := range res.Results.Bindings {
		tr, err := tripleFromBinding(tenantID, binding, p)
		if err != nil {
			return nil, err
		}
		out = append(out, tr)
	}
	return out, nil
}

// matchFullyBound 处理 S/P/O 全部绑定的模式（等价于存在性判定）。
//
// 此时没有任何变量可投影，用 ASK 而不是退化成 `SELECT`（后者会生成
// `SELECT WHERE { ... }` 这类非法查询）。返回 0 或 1 条三元组。
func (s *FusekiStore) matchFullyBound(ctx context.Context, tenantID ids.ID, p Pattern) ([]Triple, error) {
	query, err := buildAskQuery(tenantID, p)
	if err != nil {
		return nil, err
	}
	res, err := s.doQuery(ctx, query)
	if err != nil {
		return nil, err
	}
	if res.Boolean == nil {
		return nil, errors.E(errors.CodeInternal, "graphstore: ASK 查询未返回 boolean 字段")
	}
	if !*res.Boolean {
		return nil, nil
	}
	return []Triple{{
		S:          normalizeTerm(*p.S),
		P:          normalizeTerm(*p.P),
		O:          normalizeTerm(*p.O),
		GraphIRI:   p.GraphIRI,
		FactKind:   FactKindAsserted,
		ObservedAt: time.Time{}, // 纯 RDF 存储无时间维度，见类型注释
	}}, nil
}

// CountTriples 统计某命名图的三元组数。
func (s *FusekiStore) CountTriples(ctx context.Context, tenantID ids.ID, graphIRI string) (int64, error) {
	if err := validateGraphReadArgs(tenantID, graphIRI); err != nil {
		return 0, err
	}
	physical, err := physicalGraphIRI(tenantID, graphIRI)
	if err != nil {
		return 0, err
	}
	var b strings.Builder
	b.WriteString("SELECT (COUNT(*) AS ?n) WHERE { GRAPH <")
	b.WriteString(escapeIRI(physical))
	b.WriteString("> { ?s ?p ?o } }")
	res, err := s.doQuery(ctx, b.String())
	if err != nil {
		return 0, err
	}
	if len(res.Results.Bindings) == 0 {
		// COUNT(*) 必返回一行；缺失说明响应结构不符合 SPARQL 1.1 结果集约定。
		return 0, errors.E(errors.CodeInternal, "graphstore: COUNT 查询未返回结果行")
	}
	cell, ok := res.Results.Bindings[0]["n"]
	if !ok {
		return 0, errors.E(errors.CodeInternal, "graphstore: COUNT 查询结果缺少变量 ?n")
	}
	n, err := strconv.ParseInt(cell.Value, 10, 64)
	if err != nil {
		return 0, errors.Wrap(err, errors.CodeInternal, "graphstore: COUNT 结果不是整数").
			WithDetails(map[string]any{"value": cell.Value})
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// 查询/更新构造（仅本包可见）
// ---------------------------------------------------------------------------

// freePositions 返回 Pattern 中未绑定的位置下标（0=主语, 1=谓语, 2=宾语）。
func (p Pattern) freePositions() []int {
	var out []int
	for i, pos := range []*Term{p.S, p.P, p.O} {
		if pos == nil {
			out = append(out, i)
		}
	}
	return out
}

// positionVars 是三个位置对应的变量名。
var positionVars = [3]string{"?s", "?p", "?o"}

// buildTriplePattern 按位置写出三重图模式：绑定位置写常量词项，未绑定位置写变量。
//
// ⚠️ 必须**按位置**生成：曾经这里先把绑定词项统一写出来再补三个变量，生成了
// `?s <p> ?s ?p ?o` 这样的四词项模式，在只做子串断言的替身测试里看不出问题，
// 却在真实引擎上直接语法报错（rdflib: "Expected SelectQuery, found 'GRAPH'"）。
func buildTriplePattern(b *strings.Builder, p Pattern) error {
	for i, pos := range []*Term{p.S, p.P, p.O} {
		if i > 0 {
			b.WriteByte(' ')
		}
		if pos == nil {
			b.WriteString(positionVars[i])
			continue
		}
		if err := writeNTerm(b, normalizeTerm(*pos)); err != nil {
			return err
		}
	}
	return nil
}

// buildGraphPatternOpen 写出图模式的开头（GRAPH <物理图> { 或 GRAPH ?g {）。
func buildGraphPatternOpen(b *strings.Builder, tenantID ids.ID, p Pattern) (wildcard bool, err error) {
	if p.GraphIRI == "" {
		b.WriteString("GRAPH ?g { ")
		return true, nil
	}
	physical, err := physicalGraphIRI(tenantID, p.GraphIRI)
	if err != nil {
		return false, err
	}
	b.WriteString("GRAPH <")
	b.WriteString(escapeIRI(physical))
	b.WriteString("> { ")
	return false, nil
}

// buildMatchQuery 构造受控 SELECT 查询。
//
// 只投影**自由变量**（绑定位置在 Go 侧从 Pattern 重建，见 Match 的说明）。
func buildMatchQuery(tenantID ids.ID, p Pattern) (string, error) {
	free := p.freePositions()
	if len(free) == 0 {
		return "", errors.E(errors.CodeValidationFailed,
			"buildMatchQuery 需要至少一个未绑定位置；全绑定模式请用 buildAskQuery")
	}
	var b strings.Builder
	b.WriteString("SELECT")
	for _, i := range free {
		b.WriteByte(' ')
		b.WriteString(positionVars[i])
	}
	wildcard := p.GraphIRI == ""
	if wildcard {
		b.WriteString(" ?g")
	}
	b.WriteString(" WHERE { ")
	if _, err := buildGraphPatternOpen(&b, tenantID, p); err != nil {
		return "", err
	}
	if err := buildTriplePattern(&b, p); err != nil {
		return "", err
	}
	b.WriteString(" } ")
	if wildcard {
		b.WriteString(tenantFilter(tenantID))
	}
	b.WriteString("} ORDER BY")
	for _, i := range free {
		b.WriteByte(' ')
		b.WriteString(positionVars[i])
	}
	if wildcard {
		b.WriteString(" ?g")
	}
	if p.Limit > 0 {
		fmt.Fprintf(&b, " LIMIT %d", p.Limit)
	}
	return b.String(), nil
}

// buildAskQuery 构造全绑定模式的存在性查询。
func buildAskQuery(tenantID ids.ID, p Pattern) (string, error) {
	var b strings.Builder
	b.WriteString("ASK { ")
	if _, err := buildGraphPatternOpen(&b, tenantID, p); err != nil {
		return "", err
	}
	if err := buildTriplePattern(&b, p); err != nil {
		return "", err
	}
	b.WriteString(" } }")
	return b.String(), nil
}

// tenantFilter 返回通配命名图必须携带的租户前缀过滤片段。
func tenantFilter(tenantID ids.ID) string {
	return `FILTER(STRSTARTS(STR(?g), "` + escapeLiteral(graphNamespacePrefix(tenantID)) + `")) `
}

// buildInsertData 构造 INSERT DATA（去重后的三元组）。
func buildInsertData(physicalGraphIRI string, triples []Triple) (string, error) {
	return buildDataUpdate("INSERT DATA", physicalGraphIRI, triples)
}

// buildDeleteData 构造 DELETE DATA。
func buildDeleteData(physicalGraphIRI string, triples []Triple) (string, error) {
	return buildDataUpdate("DELETE DATA", physicalGraphIRI, triples)
}

func buildDataUpdate(op, physicalGraphIRI string, triples []Triple) (string, error) {
	var b strings.Builder
	b.WriteString(op)
	b.WriteString(" { GRAPH <")
	b.WriteString(escapeIRI(physicalGraphIRI))
	b.WriteString("> {")
	for _, t := range triples {
		b.WriteByte('\n')
		if err := writeNTriple(&b, t); err != nil {
			return "", err
		}
	}
	b.WriteString("\n} }")
	return b.String(), nil
}

// writeNTriple 写出一条 `S P O .`（位置上的变量不是本函数职责，这里全是常量词项）。
func writeNTriple(b *strings.Builder, t Triple) error {
	if err := writeTerms(b, normalizeTerm(t.S), normalizeTerm(t.P), normalizeTerm(t.O)); err != nil {
		return err
	}
	b.WriteString(" .")
	return nil
}

// writeTerms 以空格分隔写出若干词项（不含结尾标点）。
func writeTerms(b *strings.Builder, terms ...Term) error {
	for i, term := range terms {
		if i > 0 {
			b.WriteByte(' ')
		}
		if err := writeNTerm(b, term); err != nil {
			return err
		}
	}
	return nil
}

// buildValuesQuery 构造"入参三元组中哪些已存在"的判定查询。
//
// 使用 VALUES 而非逐条 ASK：一次往返完成整批判定，避免 N 次网络往返。
// VALUES 数据块的每一行必须以 ")" 收尾，因此这里直接自行拼接括号，
// 不复用带尾部 " ." 的三元组写法（避免"先生成再截断"的脆弱处理）。
func buildValuesQuery(physicalGraphIRI string, triples []Triple) (string, error) {
	var b strings.Builder
	b.WriteString("SELECT ?s ?p ?o WHERE { VALUES (?s ?p ?o) {")
	for _, t := range triples {
		b.WriteString("\n  (")
		if err := writeTerms(&b, normalizeTerm(t.S), normalizeTerm(t.P), normalizeTerm(t.O)); err != nil {
			return "", err
		}
		b.WriteByte(')')
	}
	b.WriteString("\n} GRAPH <")
	b.WriteString(escapeIRI(physicalGraphIRI))
	b.WriteString("> { ?s ?p ?o } }")
	return b.String(), nil
}

// existingTriples 返回 pending 中已存在于 physicalGraphIRI 的三元组（按内部键）。
func (s *FusekiStore) existingTriples(ctx context.Context, physicalGraphIRI string, pending []Triple) (map[string]struct{}, error) {
	query, err := buildValuesQuery(physicalGraphIRI, pending)
	if err != nil {
		return nil, err
	}
	res, err := s.doQuery(ctx, query)
	if err != nil {
		return nil, err
	}
	found := make(map[string]struct{}, len(res.Results.Bindings))
	for _, binding := range res.Results.Bindings {
		sTerm, err := sparqlTerm(binding, "s")
		if err != nil {
			return nil, err
		}
		pTerm, err := sparqlTerm(binding, "p")
		if err != nil {
			return nil, err
		}
		oTerm, err := sparqlTerm(binding, "o")
		if err != nil {
			return nil, err
		}
		key, err := tripleKeyOf(Triple{S: sTerm, P: pTerm, O: oTerm})
		if err != nil {
			return nil, err
		}
		found[key] = struct{}{}
	}
	return found, nil
}

// dedupeTriples 去掉入参中的重复三元组并校验词项与命名图合法性。
//
// 必须去重：INSERT DATA 在单次请求内出现重复三元组时行为由实现决定（多数引擎幂等），
// 但按入参长度计数会虚报；DELETE DATA 出现重复同理。
func dedupeTriples(graphIRI string, in []Triple) ([]Triple, error) {
	if err := validateTripleGraph(graphIRI, in); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]Triple, 0, len(in))
	for _, t := range in {
		key, err := tripleKeyOf(t)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, t)
	}
	return out, nil
}

// tripleFromBinding 把一行 SPARQL 结果还原为 Triple。
//
// 位置语义：Pattern 中已绑定的位置直接取 Pattern 的词项（归一化后），
// **不**依赖引擎把常量回显成变量绑定；未绑定的位置才读结果集里的变量。
func tripleFromBinding(tenantID ids.ID, binding map[string]sparqlValue, p Pattern) (Triple, error) {
	tr := Triple{
		FactKind: FactKindAsserted,
		// ObservedAt / ValidFrom / ValidTo 在纯 RDF 存储中不存在（见类型注释），
		// 固定为零值而不是填 now() —— 填 now() 会把"未知"伪装成"已知"。
	}
	bound := []*Term{p.S, p.P, p.O}
	names := []string{"s", "p", "o"}
	dst := []*Term{&tr.S, &tr.P, &tr.O}
	for i := range bound {
		if bound[i] != nil {
			*dst[i] = normalizeTerm(*bound[i])
			continue
		}
		term, err := sparqlTerm(binding, names[i])
		if err != nil {
			return Triple{}, err
		}
		*dst[i] = term
	}
	if p.GraphIRI != "" {
		tr.GraphIRI = p.GraphIRI
		return tr, nil
	}
	cell, ok := binding["g"]
	if !ok {
		return Triple{}, errors.E(errors.CodeInternal, "graphstore: 通配命名图查询缺少变量 ?g")
	}
	visible, ok := visibleGraphIRI(tenantID, cell.Value)
	if !ok {
		return Triple{}, errors.E(errors.CodeInternal,
			"graphstore: 查询返回了不属于本租户命名空间的结果（拒绝静默丢弃）")
	}
	tr.GraphIRI = visible
	return tr, nil
}

// sparqlTerm 解析结果绑定中的一枚词项。
func sparqlTerm(binding map[string]sparqlValue, name string) (Term, error) {
	cell, ok := binding[name]
	if !ok {
		return Term{}, errors.Ef(errors.CodeInternal, "graphstore: 查询结果缺少变量 ?%s", name)
	}
	switch cell.Type {
	case "uri":
		if cell.Value == "" {
			return Term{}, errors.E(errors.CodeInternal, "graphstore: 结果中的 IRI 为空")
		}
		return termFromIRI(cell.Value)
	case "bnode":
		// 合规引擎不应返回裸空白节点（适配器写出的空白节点已提升为保留 IRI），
		// 若出现说明有其他写入方绕过适配器，必须显式暴露而不是静默接受。
		return Term{}, errors.E(errors.CodeInternal,
			"graphstore: 结果含未经适配器编码的空白节点（可能存在绕过适配器的写入）").
			WithDetails(map[string]any{"variable": name})
	case "literal", "typed-literal":
		dt := cell.Datatype
		if cell.Type == "typed-literal" && dt == "" {
			return Term{}, errors.E(errors.CodeInternal,
				"graphstore: typed-literal 结果缺少 datatype")
		}
		return normalizeTerm(Term{Kind: KindLiteral, Lexical: cell.Value, Datatype: dt, Lang: cell.Lang}), nil
	default:
		return Term{}, errors.Ef(errors.CodeInternal,
			"graphstore: 无法识别的结果绑定类型 %q（变量 ?%s）", cell.Type, name)
	}
}

// ---------------------------------------------------------------------------
// HTTP 传输
// ---------------------------------------------------------------------------

// sparqlResults 是 SPARQL 1.1 Query Results JSON Format 的最小投影。
type sparqlResults struct {
	Head struct {
		Vars []string `json:"vars"`
	} `json:"head"`
	Boolean *bool `json:"boolean"`
	Results struct {
		Bindings []map[string]sparqlValue `json:"bindings"`
	} `json:"results"`
}

type sparqlValue struct {
	Type     string `json:"type"`
	Value    string `json:"value"`
	Datatype string `json:"datatype"`
	Lang     string `json:"xml:lang"`
}

// doQuery 发送 SPARQL 查询并解析结果集。
func (s *FusekiStore) doQuery(ctx context.Context, query string) (*sparqlResults, error) {
	body, err := s.do(ctx, s.queryURL, "application/sparql-query", "application/sparql-results+json", query)
	if err != nil {
		return nil, err
	}
	var res sparqlResults
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&res); err != nil {
		return nil, errors.Wrap(err, errors.CodeUnavailable,
			"graphstore: 解析 SPARQL 结果集失败（响应不是合法 JSON）").
			WithDetails(map[string]any{"body_prefix": truncate(string(body), maxFusekiErrorBytes)})
	}
	return &res, nil
}

// doUpdate 发送 SPARQL 更新。
func (s *FusekiStore) doUpdate(ctx context.Context, update string) error {
	_, err := s.do(ctx, s.updateURL, "application/sparql-update", "", update)
	return err
}

// do 执行一次 HTTP 往返。
//
// 认证凭据只放进 Authorization 头，绝不进入 URL（避免出现在日志、代理与错误信息中）。
func (s *FusekiStore) do(ctx context.Context, url, contentType, accept, payload string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(payload))
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "graphstore: 构造 Fuseki 请求失败")
	}
	req.Header.Set("Content-Type", contentType+"; charset=utf-8")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if s.username != "" {
		req.SetBasicAuth(s.username, s.password)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeUnavailable,
			"graphstore: 无法连接图存储服务（Fuseki）")
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFusekiResponseBytes+1))
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeUnavailable, "graphstore: 读取图存储响应失败")
	}
	if int64(len(body)) > maxFusekiResponseBytes {
		return nil, errors.Ef(errors.CodeResourceExhausted,
			"graphstore: 图存储响应超过 %d 字节上限", int64(maxFusekiResponseBytes))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, mapFusekiStatus(resp.StatusCode, body)
	}
	return body, nil
}

// mapFusekiStatus 把 Fuseki 的 HTTP 状态映射为平台错误码。
//
// 逐类显式映射而不是"一律 UNAVAILABLE"，因为运维处置方式完全不同：
// 401/403 是凭据/授权配置问题，400/500 是适配器自身构造了非法请求或引擎侧故障
// （属于缺陷，不应被当作可重试的抖动）。
//
// 引擎的诊断正文必须进入**错误消息本身**（而不仅是 Details）：只放进 Details 时
// 日志与告警里只剩"请求被拒绝"，排障时拿不到 InvalidQueryException 之类的根因。
// 正文截断到 maxFusekiErrorBytes，并同样保留在 Details 中供结构化消费。
func mapFusekiStatus(status int, body []byte) error {
	detail := strings.TrimSpace(truncate(string(body), maxFusekiErrorBytes))
	suffix := ""
	if detail != "" {
		suffix = "：" + detail
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return errors.Ef(errors.CodeUnavailable,
			"graphstore: Fuseki 拒绝访问（HTTP %d），请检查 FUSEKI_USERNAME/FUSEKI_PASSWORD 与数据集授权%s",
			status, suffix).WithDetails(map[string]any{"body_prefix": detail})
	case http.StatusBadRequest, http.StatusConflict, http.StatusInternalServerError:
		return errors.Ef(errors.CodeInternal,
			"graphstore: Fuseki 拒绝了适配器发出的请求（HTTP %d），可能是 SPARQL 构造缺陷或引擎侧故障%s",
			status, suffix).WithDetails(map[string]any{"body_prefix": detail})
	case http.StatusNotFound:
		return errors.Ef(errors.CodeUnavailable,
			"graphstore: Fuseki 端点不存在（HTTP 404），请确认 FUSEKI_ENDPOINT 指向已启用 SPARQL 查询与更新的数据集%s",
			suffix).WithDetails(map[string]any{"body_prefix": detail})
	default:
		return errors.Ef(errors.CodeUnavailable,
			"graphstore: Fuseki 返回非预期状态 HTTP %d%s", status, suffix).
			WithDetails(map[string]any{"body_prefix": detail})
	}
}

// truncate 截断字符串用于错误回显。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}
