// Package graphstore 提供统一的语义图存储抽象（GraphStore 接口）及其
// PostgreSQL 适配器（PGStore）与内存实现（MemStore）。
//
// 设计依据：第十三篇 §5.3（存储选型门禁与 PG 承诺范围）、§6.1（资源字典）、
// §6.2（命名图与三元组）、§6.3（PK+POS+OSP 三索引）、§6.4（按 tenant 声明式分区）、
// §8（统一 GraphStore 接口）。
//
// PG 方案的显式承诺范围：≤1000 万三元组/租户；更大规模必须走专用 Store 路线（§5.3）。
package graphstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
)

// Term.Kind 取值，与 graph.terms.term_kind CHECK (term_kind IN (1,2,3)) 对齐（迁移 000009）。
const (
	KindIRI       uint8 = 1 // IRI
	KindBlankNode uint8 = 2 // 空白节点
	KindLiteral   uint8 = 3 // 字面值
)

// 事实类别（graph.triples.fact_kind）。区分来源事实与推理物化的派生事实（第十三篇 §4.4、§13）。
const (
	FactKindAsserted = "ASSERTED" // 来自数据源/映射的直接事实
	FactKindDerived  = "DERIVED"  // 推理引擎物化的派生事实
)

// Term 是 RDF 词项（资源字典 graph.terms 的规范形态）。
type Term struct {
	Kind     uint8  // KindIRI / KindBlankNode / KindLiteral
	Lexical  string // IRI、节点标识或字面值词法形式
	Datatype string // 仅字面值有效（datatype_iri）
	Lang     string // 仅字面值有效（language_tag）
}

// IsLiteral 报告是否为字面值。
func (t Term) IsLiteral() bool { return t.Kind == KindLiteral }

// IsZero 报告是否为零值词项。
func (t Term) IsZero() bool { return t == Term{} }

// CanonicalDigest 计算词项的规范摘要，用于资源字典去重（第十三篇 §6.1）。
//
// digest = sha256(kind || 0x1F || lexical || 0x1F || datatype || 0x1F || lang)
//
// 0x1F（Unit Separator）作为分隔符，避免不同字段拼接产生歧义。
func (t Term) CanonicalDigest() string {
	buf := make([]byte, 0, len(t.Lexical)+len(t.Datatype)+len(t.Lang)+4)
	buf = append(buf, t.Kind, 0x1f)
	buf = append(buf, t.Lexical...)
	buf = append(buf, 0x1f)
	buf = append(buf, t.Datatype...)
	buf = append(buf, 0x1f)
	buf = append(buf, t.Lang...)
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:])
}

// Triple 是一条带上下文的三元组事实。
type Triple struct {
	S, P, O    Term
	GraphIRI   string
	FactKind   string
	ObservedAt time.Time
	ValidFrom  *time.Time
	ValidTo    *time.Time
}

// Pattern 是三元组匹配模式；S/P/O 为 nil 表示该位置未绑定（通配）。
type Pattern struct {
	S, P, O  *Term
	GraphIRI string
	Limit    int
}

// Capabilities 声明某后端的能力边界。
//
// 为什么必须在接口上显式声明，而不是只写在文档里：这些差异会让**同一段业务代码在
// 不同后端上产生不同结果**（例如把推理物化的事实当成来源事实、把稳定句柄当成持久
// 主键）。声明出来后，调用方可以在装配期判定并作出明确选择；也让"适配器少存了一维"
// 这件事不可能静默通过。
type Capabilities struct {
	// ResourceDictionary 报告后端是否提供词项资源字典（见 TermDictionary）。
	ResourceDictionary bool
	// PersistentGraphID 报告 EnsureGraph 的返回值是否为持久图标识。
	// 为 false 时该返回值仅是 (tenant, graph_iri) 的稳定派生句柄。
	PersistentGraphID bool
	// FactMetadata 报告后端是否保留事实类别（ASSERTED/DERIVED）与时间维度
	// （observed_at / valid_from / valid_to）。
	FactMetadata bool
}

// GraphStore 是平台统一的语义图存储接口（第十三篇 §8）。
//
// 接口刻意保持**存储无关**：不得出现 pgx.Tx、SQL、SPARQL 等某一后端的实现细节，
// 否则新增后端（如 Fuseki/TDB2，见 docs/图数据库的选型.md）时无法自然实现，
// 会被迫在接口上开一个永远返回错误的缺口。词项资源字典这类 PG 专有能力
// 另用 TermDictionary 可选接口表达（见下）。
type GraphStore interface {
	// Capabilities 返回本后端的能力边界。
	Capabilities() Capabilities
	// EnsureGraph 幂等创建命名图并返回 graph_id。
	//
	// 返回值语义随后端而异，以 Capabilities().PersistentGraphID 为准：
	// 提供持久命名图元数据表的后端（PGStore）返回可长期引用的 graph_id；
	// 纯 RDF 后端（FusekiStore）返回由 (tenant, graph_iri) 派生的稳定句柄
	// （跨进程可复现，但不是持久主键）。调用方若需要跨进程一致的图标识，
	// 必须以 graph_iri 为准。
	EnsureGraph(ctx context.Context, tenantID ids.ID, graphIRI, graphType, ontologyVersion string) (int64, error)
	// InsertTriples 写入三元组，返回实际新增条数。
	InsertTriples(ctx context.Context, tenantID ids.ID, graphIRI string, triples []Triple) (int, error)
	// DeleteTriples 删除三元组，返回实际删除条数。
	DeleteTriples(ctx context.Context, tenantID ids.ID, graphIRI string, triples []Triple) (int, error)
	// Match 按模式匹配三元组。
	Match(ctx context.Context, tenantID ids.ID, p Pattern) ([]Triple, error)
	// CountTriples 统计某命名图的三元组数。
	CountTriples(ctx context.Context, tenantID ids.ID, graphIRI string) (int64, error)
}

// TermDictionary 是"词项资源字典"能力（第十三篇 §6.1）。
//
// 为什么把它从 GraphStore 中拆出来：资源字典（graph.terms 表 + canonical_digest
// 去重）是 PostgreSQL 方案的实现手段，不是语义图存储的**语义**要求。RDF 原生存储
// （Fuseki/TDB2）由引擎自身的节点字典编码承担去重，既不需要也不应模拟该能力。
// 因此它是可选能力，调用方按需做类型断言：
//
//	if td, ok := store.(graphstore.TermDictionary); ok { ... }
//
// 当前仓内消费者（rdfs / shacl / semantic）只使用 GraphStore 的
// Match/InsertTriples/CountTriples，不依赖本接口。
type TermDictionary interface {
	// UpsertTerm 按 canonical_digest 去重写入词项，返回 term_id。
	//
	// 资源字典是平台级共享的：graph.terms 没有 tenant_id，词项本身不含租户信息，
	// 租户隔离由事实表（graph.triples）与命名图承担。
	UpsertTerm(ctx context.Context, t Term) (int64, error)
}

// PGStore 是 GraphStore 的 PostgreSQL 适配器。
type PGStore struct {
	Pool *pgxpool.Pool

	// ensured 记忆本进程已确保过分区的租户，避免每次写入都执行 DDL。
	// 分区创建是幂等的（CREATE TABLE IF NOT EXISTS），且分区本应在租户供给时建立；
	// 此处记忆只是为了减少重复的 DDL 加锁开销，不影响跨进程正确性。
	ensured sync.Map // tenant uuid string -> struct{}
}

var _ GraphStore = (*PGStore)(nil)
var _ TermDictionary = (*PGStore)(nil)

// Capabilities：PG 方案在三个维度上都完整（命名图元数据表、资源字典、事实元数据）。
func (s *PGStore) Capabilities() Capabilities {
	return Capabilities{ResourceDictionary: true, PersistentGraphID: true, FactMetadata: true}
}

const (
	selectTermSQL = `SELECT term_id FROM graph.terms WHERE canonical_digest = $1 ORDER BY term_id LIMIT 1`
	insertTermSQL = `INSERT INTO graph.terms (term_kind, lexical_form, datatype_iri, language_tag, canonical_digest)
VALUES ($1, $2, $3, $4, $5) RETURNING term_id`

	upsertGraphSQL = `INSERT INTO graph.named_graphs (tenant_id, graph_iri, graph_type, ontology_version)
VALUES ($1, $2, $3, $4)
ON CONFLICT (tenant_id, graph_iri) DO UPDATE
  SET graph_type = EXCLUDED.graph_type,
      ontology_version = EXCLUDED.ontology_version
RETURNING graph_id`

	insertTripleSQL = `INSERT INTO graph.triples
  (tenant_id, graph_id, subject_id, predicate_id, object_id, fact_kind, observed_at, valid_from, valid_to)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (tenant_id, graph_id, subject_id, predicate_id, object_id) DO NOTHING`

	deleteTripleSQL = `DELETE FROM graph.triples
WHERE tenant_id = $1 AND graph_id = $2 AND subject_id = $3 AND predicate_id = $4 AND object_id = $5`

	selectGraphSQL = `SELECT graph_id FROM graph.named_graphs WHERE tenant_id = $1 AND graph_iri = $2`
)

// withTenantTx 在事务内设置租户上下文后再执行 fn。
//
// 为什么必须如此（第十三篇 §6.4、§7.2、§21.5）：
//   - ontology.* 等表启用了 FORCE RLS，读写必须在事务内先 SET LOCAL app.tenant_id，
//     否则读 0 行/写被拒；
//   - graph.triples 虽未启用 RLS，但迁移 000009 已声明"应用数据库角色非超级用户、无 BYPASSRLS"，
//     统一走 WithTenantTx 是纵深防御，且在 RLS 未来覆盖 graph Schema 时无需改动调用方。
func (s *PGStore) withTenantTx(ctx context.Context, tenantID ids.ID, fn func(pgx.Tx) error) error {
	if s.Pool == nil {
		return errors.E(errors.CodeInternal, "graphstore: 连接池不能为空")
	}
	if tenantID.IsZero() {
		return errors.E(errors.CodeValidationFailed, "tenant_id 不能为空")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return errors.Wrap(err, errors.CodeInternal, "graphstore: 开启事务失败")
	}
	// 已提交后再 Rollback 返回 ErrTxClosed，忽略即可。
	defer func() { _ = tx.Rollback(ctx) }()

	if err := pgxutil.WithTenantTx(ctx, tx, tenantID); err != nil {
		return errors.Wrap(err, errors.CodeInternal, "graphstore: 设置租户上下文失败")
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return errors.Wrap(err, errors.CodeInternal, "graphstore: 提交事务失败")
	}
	return nil
}

// ensurePartition 确保租户分区存在（幂等），供所有写入路径前置调用。
func (s *PGStore) ensurePartition(ctx context.Context, tenantID ids.ID) error {
	key := tenantID.String()
	if _, ok := s.ensured.Load(key); ok {
		return nil
	}
	if err := EnsureTenantPartition(ctx, s.Pool, tenantID); err != nil {
		return err
	}
	s.ensured.Store(key, struct{}{})
	return nil
}

// EnsureGraph 幂等创建命名图；已存在时更新 graph_type/ontology_version 并返回既有 graph_id。
func (s *PGStore) EnsureGraph(ctx context.Context, tenantID ids.ID, graphIRI, graphType, ontologyVersion string) (int64, error) {
	if strings.TrimSpace(graphIRI) == "" {
		return 0, errors.E(errors.CodeValidationFailed, "graph_iri 不能为空")
	}
	if strings.TrimSpace(graphType) == "" {
		return 0, errors.E(errors.CodeValidationFailed, "graph_type 不能为空")
	}
	var graphID int64
	err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, upsertGraphSQL, tenantID.String(), graphIRI, graphType, nullableString(ontologyVersion)).Scan(&graphID)
	})
	if err != nil {
		return 0, err
	}
	return graphID, nil
}

// UpsertTerm 按 canonical_digest 去重写入词项（TermDictionary 能力）。
//
// graph.terms 仅有非唯一索引 idx_terms_digest（无 UNIQUE 约束），因此无法使用
// ON CONFLICT，这里采用"先查 canonical_digest、再插入"的策略（第十三篇 §6.1）。
// 并发下极少数情况可能插入重复词项，读取端以最早一条（ORDER BY term_id）为准，
// 语义仍然正确（同 digest 即同词项）。
//
// 本方法无需租户事务：迁移 000009 未对 graph schema 启用 RLS（资源字典是平台级
// 共享表，无 tenant_id 列）。**若将来给 graph.terms 加上 RLS，本方法会静默退化为
// "读不到→重复插入"**，届时必须同步改为租户上下文路径，故在此显式记录该前置条件。
func (s *PGStore) UpsertTerm(ctx context.Context, t Term) (int64, error) {
	if s.Pool == nil {
		return 0, errors.E(errors.CodeInternal, "graphstore: 连接池不能为空")
	}
	if err := validateTerm(t); err != nil {
		return 0, err
	}
	digest := t.CanonicalDigest()
	var id int64
	err := s.Pool.QueryRow(ctx, selectTermSQL, digest).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != pgx.ErrNoRows {
		return 0, errors.Wrap(err, errors.CodeInternal, "graphstore: 查询词项失败")
	}
	if err := s.Pool.QueryRow(ctx, insertTermSQL,
		int16(t.Kind), t.Lexical, nullableString(t.Datatype), nullableString(t.Lang), digest,
	).Scan(&id); err != nil {
		return 0, errors.Wrap(err, errors.CodeInternal, "graphstore: 写入词项失败")
	}
	return id, nil
}

// upsertTermTx 在调用方事务内写入词项（PG 内部批量路径，避免每词项各开一个事务）。
func (s *PGStore) upsertTermTx(ctx context.Context, tx pgx.Tx, t Term) (int64, error) {
	if tx == nil {
		return 0, errors.E(errors.CodeInternal, "graphstore: 事务不能为空")
	}
	if err := validateTerm(t); err != nil {
		return 0, err
	}
	digest := t.CanonicalDigest()

	var id int64
	err := tx.QueryRow(ctx, selectTermSQL, digest).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != pgx.ErrNoRows {
		return 0, errors.Wrap(err, errors.CodeInternal, "graphstore: 查询词项失败")
	}
	if err := tx.QueryRow(ctx, insertTermSQL,
		int16(t.Kind), t.Lexical, nullableString(t.Datatype), nullableString(t.Lang), digest,
	).Scan(&id); err != nil {
		return 0, errors.Wrap(err, errors.CodeInternal, "graphstore: 写入词项失败")
	}
	return id, nil
}

// validateTerm 校验词项合法性（两个写入路径共用，避免两处出现不一致的判据）。
func validateTerm(t Term) error {
	if t.Kind != KindIRI && t.Kind != KindBlankNode && t.Kind != KindLiteral {
		return errors.Ef(errors.CodeValidationFailed, "非法 term_kind=%d", t.Kind)
	}
	if t.Lexical == "" {
		return errors.E(errors.CodeValidationFailed, "词项 lexical_form 不能为空")
	}
	return nil
}

// InsertTriples 写入三元组并返回实际新增条数（主键冲突的重复三元组不计入）。
func (s *PGStore) InsertTriples(ctx context.Context, tenantID ids.ID, graphIRI string, triples []Triple) (int, error) {
	// 参数校验先于空批次返回（见 validateWriteArgs 的说明）。
	if err := validateWriteArgs(tenantID, graphIRI, triples); err != nil {
		return 0, err
	}
	if len(triples) == 0 {
		return 0, nil
	}
	// ⚠️ graph.triples 是按 tenant_id 的 LIST 分区表，迁移不预建任何分区，
	// 缺少分区时插入会报 "no partition of relation graph.triples found for row"。
	// 因此所有插入路径前必须确保分区存在（第十三篇 §6.4 / 租户供给流程）。
	if err := s.ensurePartition(ctx, tenantID); err != nil {
		return 0, err
	}

	inserted := 0
	err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		graphID, err := ensureGraphTx(ctx, tx, tenantID, graphIRI)
		if err != nil {
			return err
		}
		termCache := make(map[string]int64)
		batch := &pgx.Batch{}
		queued := 0
		for _, tr := range triples {
			sID, err := s.cachedTermID(ctx, tx, termCache, tr.S)
			if err != nil {
				return err
			}
			pID, err := s.cachedTermID(ctx, tx, termCache, tr.P)
			if err != nil {
				return err
			}
			oID, err := s.cachedTermID(ctx, tx, termCache, tr.O)
			if err != nil {
				return err
			}
			factKind := tr.FactKind
			if factKind == "" {
				factKind = FactKindAsserted
			}
			observed := tr.ObservedAt
			if observed.IsZero() {
				observed = time.Now().UTC()
			}
			batch.Queue(insertTripleSQL,
				tenantID.String(), graphID, sID, pID, oID, factKind, observed, tr.ValidFrom, tr.ValidTo)
			queued++
		}
		br := tx.SendBatch(ctx, batch)
		for i := 0; i < queued; i++ {
			ct, err := br.Exec()
			if err != nil {
				_ = br.Close()
				return errors.Wrap(err, errors.CodeInternal, "graphstore: 写入三元组失败")
			}
			inserted += int(ct.RowsAffected())
		}
		if err := br.Close(); err != nil {
			return errors.Wrap(err, errors.CodeInternal, "graphstore: 完成三元组批量写入失败")
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return inserted, nil
}

// DeleteTriples 删除三元组并返回实际删除条数。
func (s *PGStore) DeleteTriples(ctx context.Context, tenantID ids.ID, graphIRI string, triples []Triple) (int, error) {
	if err := validateWriteArgs(tenantID, graphIRI, triples); err != nil {
		return 0, err
	}
	if len(triples) == 0 {
		return 0, nil
	}
	deleted := 0
	err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var graphID int64
		if err := tx.QueryRow(ctx, selectGraphSQL, tenantID.String(), graphIRI).Scan(&graphID); err != nil {
			if err == pgx.ErrNoRows {
				return nil // 命名图不存在，无可删除事实
			}
			return errors.Wrap(err, errors.CodeInternal, "graphstore: 查询命名图失败")
		}
		batch := &pgx.Batch{}
		queued := 0
		for _, tr := range triples {
			sID, ok, err := lookupTermID(ctx, tx, tr.S)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			pID, ok, err := lookupTermID(ctx, tx, tr.P)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			oID, ok, err := lookupTermID(ctx, tx, tr.O)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			batch.Queue(deleteTripleSQL, tenantID.String(), graphID, sID, pID, oID)
			queued++
		}
		if queued == 0 {
			return nil
		}
		br := tx.SendBatch(ctx, batch)
		for i := 0; i < queued; i++ {
			ct, err := br.Exec()
			if err != nil {
				_ = br.Close()
				return errors.Wrap(err, errors.CodeInternal, "graphstore: 删除三元组失败")
			}
			deleted += int(ct.RowsAffected())
		}
		if err := br.Close(); err != nil {
			return errors.Wrap(err, errors.CodeInternal, "graphstore: 完成三元组批量删除失败")
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}

// Match 按模式匹配三元组。
//
// 查询始终带 tenant_id（§6.4、§7.2）：
//   - tenant_id 是 graph.triples 的 LIST 分区键，写入 WHERE 可让规划器做分区裁剪，
//     跨租户分区在计划层被排除；
//   - 同时它是 RLS/租户隔离的执行期断言，防止任何调用方绕过租户边界。
//
// 谓词/宾语/主语的绑定通过 canonical_digest 与 graph.terms 连接实现；
// 未绑定的位置不加过滤条件，由 POS/OSP 索引承担访问路径（§6.3）。
func (s *PGStore) Match(ctx context.Context, tenantID ids.ID, p Pattern) ([]Triple, error) {
	if tenantID.IsZero() {
		return nil, errors.E(errors.CodeValidationFailed, "tenant_id 不能为空")
	}
	if p.Limit < 0 {
		return nil, errors.E(errors.CodeValidationFailed, "limit 不能为负")
	}
	var out []Triple
	err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		sql, args := buildMatchSQL(tenantID, p)
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "graphstore: 查询三元组失败")
		}
		defer rows.Close()
		for rows.Next() {
			var (
				graphIRI string
				sKind    int16
				sLex     string
				sDT      *string
				sLang    *string
				pKind    int16
				pLex     string
				pDT      *string
				pLang    *string
				oKind    int16
				oLex     string
				oDT      *string
				oLang    *string
				factKind string
				observed time.Time
				vf       *time.Time
				vt       *time.Time
			)
			if err := rows.Scan(&graphIRI,
				&sKind, &sLex, &sDT, &sLang,
				&pKind, &pLex, &pDT, &pLang,
				&oKind, &oLex, &oDT, &oLang,
				&factKind, &observed, &vf, &vt,
			); err != nil {
				return errors.Wrap(err, errors.CodeInternal, "graphstore: 扫描三元组失败")
			}
			out = append(out, Triple{
				S:          Term{Kind: uint8(sKind), Lexical: sLex, Datatype: deref(sDT), Lang: deref(sLang)},
				P:          Term{Kind: uint8(pKind), Lexical: pLex, Datatype: deref(pDT), Lang: deref(pLang)},
				O:          Term{Kind: uint8(oKind), Lexical: oLex, Datatype: deref(oDT), Lang: deref(oLang)},
				GraphIRI:   graphIRI,
				FactKind:   factKind,
				ObservedAt: observed,
				ValidFrom:  vf,
				ValidTo:    vt,
			})
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CountTriples 统计某命名图的三元组数。
func (s *PGStore) CountTriples(ctx context.Context, tenantID ids.ID, graphIRI string) (int64, error) {
	if err := validateGraphReadArgs(tenantID, graphIRI); err != nil {
		return 0, err
	}
	var n int64
	err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			"SELECT count(*) FROM graph.triples t "+
				"JOIN graph.named_graphs g ON g.graph_id = t.graph_id "+
				"WHERE t.tenant_id = $1 AND g.graph_iri = $2",
			tenantID.String(), graphIRI,
		).Scan(&n)
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

func buildMatchSQL(tenantID ids.ID, p Pattern) (string, []any) {
	var b strings.Builder
	b.WriteString("SELECT g.graph_iri, " +
		"ts.term_kind, ts.lexical_form, ts.datatype_iri, ts.language_tag, " +
		"tp.term_kind, tp.lexical_form, tp.datatype_iri, tp.language_tag, " +
		"tob.term_kind, tob.lexical_form, tob.datatype_iri, tob.language_tag, " +
		"t.fact_kind, t.observed_at, t.valid_from, t.valid_to " +
		"FROM graph.triples t " +
		"JOIN graph.named_graphs g ON g.graph_id = t.graph_id " +
		"JOIN graph.terms ts ON ts.term_id = t.subject_id " +
		"JOIN graph.terms tp ON tp.term_id = t.predicate_id " +
		"JOIN graph.terms tob ON tob.term_id = t.object_id " +
		"WHERE t.tenant_id = $1")
	args := []any{tenantID.String()}
	appendFilter := func(alias string, t *Term) {
		if t == nil {
			return
		}
		args = append(args, t.CanonicalDigest())
		fmt.Fprintf(&b, " AND %s.canonical_digest = $%d", alias, len(args))
	}
	if p.GraphIRI != "" {
		args = append(args, p.GraphIRI)
		fmt.Fprintf(&b, " AND g.graph_iri = $%d", len(args))
	}
	appendFilter("ts", p.S)
	appendFilter("tp", p.P)
	appendFilter("tob", p.O)
	b.WriteString(" ORDER BY t.subject_id, t.predicate_id, t.object_id")
	if p.Limit > 0 {
		args = append(args, p.Limit)
		fmt.Fprintf(&b, " LIMIT $%d", len(args))
	}
	return b.String(), args
}

// ensureGraphTx 在事务内幂等创建命名图（供 InsertTriples 自动补建）。
func ensureGraphTx(ctx context.Context, tx pgx.Tx, tenantID ids.ID, graphIRI string) (int64, error) {
	var graphID int64
	err := tx.QueryRow(ctx, upsertGraphSQL, tenantID.String(), graphIRI, "INSTANCE", nil).Scan(&graphID)
	if err != nil {
		return 0, errors.Wrap(err, errors.CodeInternal, "graphstore: 确保命名图失败")
	}
	return graphID, nil
}

func (s *PGStore) cachedTermID(ctx context.Context, tx pgx.Tx, cache map[string]int64, t Term) (int64, error) {
	key := t.CanonicalDigest()
	if id, ok := cache[key]; ok {
		return id, nil
	}
	id, err := s.upsertTermTx(ctx, tx, t)
	if err != nil {
		return 0, err
	}
	cache[key] = id
	return id, nil
}

func lookupTermID(ctx context.Context, tx pgx.Tx, t Term) (int64, bool, error) {
	var id int64
	err := tx.QueryRow(ctx, selectTermSQL, t.CanonicalDigest()).Scan(&id)
	if err == pgx.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, errors.Wrap(err, errors.CodeInternal, "graphstore: 查询词项失败")
	}
	return id, true, nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
