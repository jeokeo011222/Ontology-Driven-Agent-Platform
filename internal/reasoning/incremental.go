package reasoning

// 增量更新：仅对新增事实做前提匹配，产出增量派生。
//
// 与全量前向链的差异只在初始 delta：全量物化的初始 delta 是**全部事实**，
// 增量更新的初始 delta 是**新增事实**——既有事实已经过此前的不动点收敛，
// 新结论必然经过新事实参与 join 才能产生（半朴素法的标准性质）。

import (
	"context"
	"strings"

	"odap/internal/ontology/graphstore"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// IncrementalResult 是增量更新的执行结果。
type IncrementalResult struct {
	// Derived 是由新增事实触发的增量派生（调用方写回时置 FactKind=DERIVED）。
	Derived []graphstore.Triple
	// Inserted 是新增事实实际落图条数（重复不计）。
	Inserted int
}

// RunIncremental 是增量推理的可测入口：facts 为图的既有事实，added 为新增事实。
func RunIncremental(rules []Rule, facts []graphstore.Triple, added []graphstore.Triple) (*IncrementalResult, error) {
	if len(added) == 0 {
		return &IncrementalResult{Derived: []graphstore.Triple{}}, nil
	}
	res, err := runForwardChain(rules, facts, added, DefaultMaxIterations)
	if err != nil {
		return nil, err
	}
	if res.Derived == nil {
		res.Derived = []graphstore.Triple{}
	}
	return &IncrementalResult{Derived: res.Derived, Inserted: len(added)}, nil
}

// IncrementalUpdate 把新增事实幂等写入图存储（FactKind=ASSERTED），随后做
// 增量推理并派生三元组写回（FactKind=DERIVED）。
func (s *Service) IncrementalUpdate(ctx context.Context, tenantID ids.ID, graphIRI string, added []graphstore.Triple) (*IncrementalResult, error) {
	if err := validateTenantGraph(tenantID, graphIRI); err != nil {
		return nil, err
	}
	if len(added) == 0 {
		return nil, errors.E(errors.CodeInvalidArgument, "added_triples 不能为空（增量更新必须携带新增事实）")
	}
	for i := range added {
		if err := validateTriple(added[i]); err != nil {
			return nil, err
		}
		added[i].GraphIRI = graphIRI
		added[i].FactKind = graphstore.FactKindAsserted
	}
	inserted, err := s.Store.InsertTriples(ctx, tenantID, graphIRI, added)
	if err != nil {
		return nil, err
	}

	rules, err := s.loadRules(ctx, tenantID, nil)
	if err != nil {
		return nil, err
	}
	facts, err := loadFacts(ctx, s.Store, tenantID, graphIRI)
	if err != nil {
		return nil, err
	}
	out, err := RunIncremental(rules, facts, added)
	if err != nil {
		return nil, err
	}
	out.Inserted = inserted
	if len(out.Derived) > 0 {
		if _, err := s.Store.InsertTriples(ctx, tenantID, graphIRI, out.Derived); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// validateTenantGraph 校验租户与命名图参数（Service 层公共前置）。
func validateTenantGraph(tenantID ids.ID, graphIRI string) error {
	if tenantID.IsZero() {
		return errors.E(errors.CodeInvalidArgument, "tenant_id 不能为空")
	}
	if strings.TrimSpace(graphIRI) == "" {
		return errors.E(errors.CodeInvalidArgument, "graph_iri 不能为空")
	}
	return nil
}

// validateTriple 校验词法三元组（Service 层入参防御）。
func validateTriple(t graphstore.Triple) error {
	for _, term := range []graphstore.Term{t.S, t.P, t.O} {
		if term.Kind == graphstore.KindIRI && strings.ContainsRune(term.Lexical, ' ') {
			return errors.Ef(errors.CodeInvalidArgument, "三元组词项 %q 含空白字符（IRI 非法）", term.Lexical)
		}
		if term.Kind == graphstore.KindLiteral && term.Lang != "" && term.Datatype != "" {
			return errors.E(errors.CodeInvalidArgument, "字面值不能同时声明 language 与 datatype")
		}
	}
	return nil
}
