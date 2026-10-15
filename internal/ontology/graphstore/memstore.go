package graphstore

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// MemStore 是 GraphStore 的内存实现，供单元测试、SHACL 校验与推理测试使用。
// 它不依赖真实数据库，语义上与 PGStore 对齐（去重、计数、幂等建图）。
type MemStore struct {
	mu sync.Mutex

	nextGraphID int64
	nextTermID  int64

	graphs   map[string]memGraph // graphKey -> 图元信息
	terms    map[string]int64    // canonical_digest -> term_id
	termByID map[int64]Term
	triples  map[string]memTriple // tripleKey -> 事实
	order    []string             // 插入顺序，保证 Match 结果稳定
}

type memGraph struct {
	id              int64
	graphType       string
	ontologyVersion string
}

type memTriple struct {
	tenantID    string
	graphIRI    string
	subjectID   int64
	predicateID int64
	objectID    int64
	factKind    string
	observed    time.Time
	validFrom   *time.Time
	validTo     *time.Time
}

var _ GraphStore = (*MemStore)(nil)
var _ TermDictionary = (*MemStore)(nil)

// Capabilities：内存实现完整对齐 PGStore 的语义（含资源字典与事实元数据），
// 因此可以代替 PGStore 跑单元 / 契约测试。
func (m *MemStore) Capabilities() Capabilities {
	return Capabilities{ResourceDictionary: true, PersistentGraphID: true, FactMetadata: true}
}

// NewMemStore 创建空的内存图存储。
func NewMemStore() *MemStore {
	return &MemStore{
		graphs:   make(map[string]memGraph),
		terms:    make(map[string]int64),
		termByID: make(map[int64]Term),
		triples:  make(map[string]memTriple),
	}
}

// EnsureGraph 幂等创建命名图。
func (m *MemStore) EnsureGraph(_ context.Context, tenantID ids.ID, graphIRI, graphType, ontologyVersion string) (int64, error) {
	if tenantID.IsZero() {
		return 0, errors.E(errors.CodeValidationFailed, "tenant_id 不能为空")
	}
	if strings.TrimSpace(graphIRI) == "" {
		return 0, errors.E(errors.CodeValidationFailed, "graph_iri 不能为空")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := graphKey(tenantID, graphIRI)
	if g, ok := m.graphs[key]; ok {
		if graphType != "" {
			g.graphType = graphType
		}
		if ontologyVersion != "" {
			g.ontologyVersion = ontologyVersion
		}
		m.graphs[key] = g
		return g.id, nil
	}
	m.nextGraphID++
	m.graphs[key] = memGraph{id: m.nextGraphID, graphType: graphType, ontologyVersion: ontologyVersion}
	return m.nextGraphID, nil
}

// UpsertTerm 按 canonical_digest 去重写入词项（TermDictionary 能力）。
// 与 PGStore 一致：资源字典是平台级共享的，不区分租户。
func (m *MemStore) UpsertTerm(_ context.Context, t Term) (int64, error) {
	if err := validateTerm(t); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.upsertTermLocked(t), nil
}

func (m *MemStore) upsertTermLocked(t Term) int64 {
	digest := t.CanonicalDigest()
	if id, ok := m.terms[digest]; ok {
		return id
	}
	m.nextTermID++
	m.terms[digest] = m.nextTermID
	m.termByID[m.nextTermID] = t
	return m.nextTermID
}

// InsertTriples 写入三元组并返回实际新增条数。
// 与 PGStore 对齐：命名图不存在时自动补建。
func (m *MemStore) InsertTriples(_ context.Context, tenantID ids.ID, graphIRI string, triples []Triple) (int, error) {
	// 参数校验先于空批次返回（见 validateWriteArgs 的说明）。
	if err := validateWriteArgs(tenantID, graphIRI, triples); err != nil {
		return 0, err
	}
	if len(triples) == 0 {
		return 0, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureGraphLocked(tenantID, graphIRI); err != nil {
		return 0, err
	}
	inserted := 0
	for _, tr := range triples {
		s := m.upsertTermLocked(tr.S)
		p := m.upsertTermLocked(tr.P)
		o := m.upsertTermLocked(tr.O)
		key := tripleKey(tenantID, graphIRI, s, p, o)
		if _, ok := m.triples[key]; ok {
			continue
		}
		factKind := tr.FactKind
		if factKind == "" {
			factKind = FactKindAsserted
		}
		observed := tr.ObservedAt
		if observed.IsZero() {
			observed = time.Now().UTC()
		}
		m.triples[key] = memTriple{
			tenantID:    tenantID.String(),
			graphIRI:    graphIRI,
			subjectID:   s,
			predicateID: p,
			objectID:    o,
			factKind:    factKind,
			observed:    observed,
			validFrom:   tr.ValidFrom,
			validTo:     tr.ValidTo,
		}
		m.order = append(m.order, key)
		inserted++
	}
	return inserted, nil
}

// DeleteTriples 删除三元组并返回实际删除条数。
func (m *MemStore) DeleteTriples(_ context.Context, tenantID ids.ID, graphIRI string, triples []Triple) (int, error) {
	// 与 PGStore 对齐：PGStore 经 withTenantTx 拒绝零租户，内存实现必须同样拒绝，
	// 否则用 MemStore 跑的测试会掩盖"调用方漏传租户"的缺陷（跨后端行为必须一致，
	// 否则 MemStore 作为 PGStore 的测试替身就是不可信的）。
	if err := validateWriteArgs(tenantID, graphIRI, triples); err != nil {
		return 0, err
	}
	if len(triples) == 0 {
		return 0, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	deleted := 0
	for _, tr := range triples {
		sDigest := tr.S.CanonicalDigest()
		pDigest := tr.P.CanonicalDigest()
		oDigest := tr.O.CanonicalDigest()
		s, ok1 := m.terms[sDigest]
		p, ok2 := m.terms[pDigest]
		o, ok3 := m.terms[oDigest]
		if !ok1 || !ok2 || !ok3 {
			continue
		}
		key := tripleKey(tenantID, graphIRI, s, p, o)
		if _, ok := m.triples[key]; !ok {
			continue
		}
		delete(m.triples, key)
		deleted++
	}
	if deleted > 0 {
		m.compactLocked()
	}
	return deleted, nil
}

func (m *MemStore) compactLocked() {
	kept := m.order[:0]
	for _, k := range m.order {
		if _, ok := m.triples[k]; ok {
			kept = append(kept, k)
		}
	}
	m.order = kept
}

// Match 按模式匹配三元组。
func (m *MemStore) Match(_ context.Context, tenantID ids.ID, p Pattern) ([]Triple, error) {
	if tenantID.IsZero() {
		return nil, errors.E(errors.CodeValidationFailed, "tenant_id 不能为空")
	}
	if p.Limit < 0 {
		return nil, errors.E(errors.CodeValidationFailed, "limit 不能为负")
	}
	var sD, pD, oD string
	if p.S != nil {
		sD = p.S.CanonicalDigest()
	}
	if p.P != nil {
		pD = p.P.CanonicalDigest()
	}
	if p.O != nil {
		oD = p.O.CanonicalDigest()
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]Triple, 0)
	for _, key := range m.order {
		mt, ok := m.triples[key]
		if !ok {
			continue
		}
		if mt.tenantID != tenantID.String() {
			continue
		}
		if p.GraphIRI != "" && mt.graphIRI != p.GraphIRI {
			continue
		}
		s := m.termByID[mt.subjectID]
		pr := m.termByID[mt.predicateID]
		o := m.termByID[mt.objectID]
		if sD != "" && s.CanonicalDigest() != sD {
			continue
		}
		if pD != "" && pr.CanonicalDigest() != pD {
			continue
		}
		if oD != "" && o.CanonicalDigest() != oD {
			continue
		}
		out = append(out, Triple{
			S:          s,
			P:          pr,
			O:          o,
			GraphIRI:   mt.graphIRI,
			FactKind:   mt.factKind,
			ObservedAt: mt.observed,
			ValidFrom:  mt.validFrom,
			ValidTo:    mt.validTo,
		})
		if p.Limit > 0 && len(out) >= p.Limit {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].S.Lexical != out[j].S.Lexical {
			return out[i].S.Lexical < out[j].S.Lexical
		}
		if out[i].P.Lexical != out[j].P.Lexical {
			return out[i].P.Lexical < out[j].P.Lexical
		}
		return out[i].O.Lexical < out[j].O.Lexical
	})
	return out, nil
}

// CountTriples 统计某命名图的三元组数。
func (m *MemStore) CountTriples(_ context.Context, tenantID ids.ID, graphIRI string) (int64, error) {
	if err := validateGraphReadArgs(tenantID, graphIRI); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for _, mt := range m.triples {
		if mt.tenantID == tenantID.String() && mt.graphIRI == graphIRI {
			n++
		}
	}
	return n, nil
}

func (m *MemStore) ensureGraphLocked(tenantID ids.ID, graphIRI string) error {
	key := graphKey(tenantID, graphIRI)
	if _, ok := m.graphs[key]; ok {
		return nil
	}
	m.nextGraphID++
	m.graphs[key] = memGraph{id: m.nextGraphID, graphType: "INSTANCE"}
	return nil
}

func graphKey(tenantID ids.ID, graphIRI string) string {
	return tenantID.String() + "\x1f" + graphIRI
}

func tripleKey(tenantID ids.ID, graphIRI string, s, p, o int64) string {
	return tenantID.String() + "\x1f" + graphIRI + "\x1f" +
		strconv.FormatInt(s, 10) + "\x1f" + strconv.FormatInt(p, 10) + "\x1f" + strconv.FormatInt(o, 10)
}
