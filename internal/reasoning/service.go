package reasoning

// Service 聚合 GraphStore 与 DB 池，是推理引擎对 HTTP/gRPC 两层的唯一门面。
//
// 多租户约定：图数据读写走 GraphStore（自带租户参数与租户事务）；
// reason.rules / reason.inference_runs 的读写走 pgxutil.TenantTx（显式租户
// 事务，注入 app.tenant_id 使 RLS 生效）——两条路径都不允许绕开租户边界。

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/ontology/graphload"
	"odap/internal/ontology/graphstore"
	"odap/internal/ontology/shacl"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
)

// Service 是推理引擎服务。
type Service struct {
	// Store 是语义图存储（事实与派生三元组读写）。
	Store graphstore.GraphStore
	// Pool 是平台数据库连接池（reason schema 的规则与运行记录读写）；可为 nil
	// （此时仅内置规则可用，规则持久化路径显式报错）。
	Pool *pgxpool.Pool
	// Log 是可选的日志器（nil 时不输出）。
	Log *slog.Logger
}

// NewService 构造推理引擎服务。
func NewService(store graphstore.GraphStore, pool *pgxpool.Pool, log *slog.Logger) *Service {
	return &Service{Store: store, Pool: pool, Log: log}
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default() // nil 日志器回退到默认（绝不让日志调用 panic）
}

// ForwardResult / IncrementalResult 定义在 forward.go / incremental.go。

// ForwardChain 对命名图执行全量前向链物化，派生三元组以 FactKind=DERIVED 写回。
func (s *Service) ForwardChain(ctx context.Context, tenantID ids.ID, graphIRI string, ruleKeys []string, maxIterations int) (*ForwardResult, error) {
	start := time.Now()
	if err := validateTenantGraph(tenantID, graphIRI); err != nil {
		return nil, err
	}
	rules, err := s.loadRules(ctx, tenantID, ruleKeys)
	if err != nil {
		return nil, err
	}
	facts, err := loadFacts(ctx, s.Store, tenantID, graphIRI)
	if err != nil {
		return nil, err
	}
	res, err := runForwardChain(rules, facts, nil, maxIterations)
	if err != nil {
		return nil, err
	}
	res.DurationMS = time.Since(start).Milliseconds()
	if len(res.Derived) > 0 {
		if _, err := s.Store.InsertTriples(ctx, tenantID, graphIRI, res.Derived); err != nil {
			return nil, err
		}
	}
	s.log().Info("前向链物化完成",
		"tenant_id", tenantID.String(), "graph_iri", graphIRI,
		"derived", len(res.Derived), "iterations", res.Iterations,
		"duration_ms", time.Since(start).Milliseconds())
	return res, nil
}

// BackwardChain 目标驱动求证（只读：不产生派生事实）。
func (s *Service) BackwardChain(ctx context.Context, tenantID ids.ID, graphIRI string, goal TriplePattern, ruleKeys []string) (*BackwardResult, error) {
	if err := validateTenantGraph(tenantID, graphIRI); err != nil {
		return nil, err
	}
	if goal.Subject == "" && goal.Predicate == "" && goal.Object == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "goal 不能全为空（无目标即无求证）")
	}
	rules, err := s.loadRules(ctx, tenantID, ruleKeys)
	if err != nil {
		return nil, err
	}
	facts, err := loadFacts(ctx, s.Store, tenantID, graphIRI)
	if err != nil {
		return nil, err
	}
	return RunBackward(rules, facts, goal)
}

// loadRules 装载参与推理的规则集：代码内置规则（含 DB 启停开关行合并）+
// reason.rules 自定义规则；ruleKeys 非空时过滤（未命中的键显式报错）。
func (s *Service) loadRules(ctx context.Context, tenantID ids.ID, ruleKeys []string) ([]Rule, error) {
	builtin := BuiltinRules()
	byKey := make(map[string]*Rule, len(builtin))
	for i := range builtin {
		byKey[builtin[i].RuleKey] = &builtin[i]
	}

	// DB 侧：内置规则启停行 + 自定义规则。Pool 未装配时仅内置规则（默认全启用），
	// 持久化路径在各自入口显式拦截，这里不重复报错。
	if s.Pool != nil && !tenantID.IsZero() {
		err := pgxutil.TenantTx(ctx, s.Pool, tenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `
                SELECT rule_key, kind, body::text AS body, enabled, is_builtin
                  FROM reason.rules`)
			if err != nil {
				return errors.Wrap(err, errors.CodeInternal, "推理引擎：读取规则失败")
			}
			defer rows.Close()
			for rows.Next() {
				var key, kind, body string
				var enabled, isBuiltin bool
				if err := rows.Scan(&key, &kind, &body, &enabled, &isBuiltin); err != nil {
					return errors.Wrap(err, errors.CodeInternal, "推理引擎：扫描规则行失败")
				}
				if isBuiltin {
					// 内置规则启停开关行：仅合并 enabled 状态。
					if r, ok := byKey[key]; ok {
						r.Enabled = enabled
					}
					continue
				}
				r, err := ParseRuleBody([]byte(body))
				if err != nil {
					// 单条规则体损坏不阻断整体推理（写路径已严格校验，此处为防御）：
					// 跳过并留痕，绝不静默——日志可查。
					if s.Log != nil {
						s.Log.Warn("推理引擎：跳过无法解析的自定义规则",
							"rule_key", key, "error", err.Error())
					}
					continue
				}
				r.Enabled = enabled
				r.Builtin = false
				if existing, ok := byKey[r.RuleKey]; ok {
					*existing = r // 自定义规则覆盖同键内置（注册端已拒绝，防御性兜底）
					continue
				}
				byKey[r.RuleKey] = &r
			}
			return rows.Err()
		})
		if err != nil {
			return nil, err
		}
	}

	// 过滤：显式点名时未命中的键报错（fail-closed，绝不静默缩小规则集）。
	if len(ruleKeys) > 0 {
		missing := make([]string, 0)
		for _, k := range ruleKeys {
			if _, ok := byKey[strings.TrimSpace(k)]; !ok {
				missing = append(missing, k)
			}
		}
		if len(missing) > 0 {
			return nil, errors.Ef(errors.CodeNotFound, "规则不存在：%s", strings.Join(missing, ", "))
		}
	}

	out := make([]Rule, 0, len(byKey))
	for _, k := range ruleKeys {
		if r, ok := byKey[strings.TrimSpace(k)]; ok {
			out = append(out, *r)
		}
	}
	if len(ruleKeys) == 0 {
		for i := range builtin {
			out = append(out, builtin[i])
		}
		// 自定义规则（不在内置列表中的）按 key 序追加，保证结果确定性。
		customKeys := make([]string, 0, len(byKey))
		for key := range byKey {
			if !isBuiltinKey(key) {
				customKeys = append(customKeys, key)
			}
		}
		sort.Strings(customKeys)
		for _, key := range customKeys {
			out = append(out, *byKey[key])
		}
	}
	return out, nil
}

// isBuiltinKey 报告键是否为内置规则键。
func isBuiltinKey(key string) bool { return builtinKeySet()[key] }

// ValidateResultItem 是 SHACL 校验发现项摘要（Service 层类型，供 HTTP/gRPC 投影）。
type ValidateResultItem struct {
	ShapeID   string `json:"shape_id"`
	FocusNode string `json:"focus_node"`
	Path      string `json:"path"`
	Component string `json:"component"`
	Severity  string `json:"severity"`
	Message   string `json:"message"`
}

// ValidateReport 是一次校验的摘要。
type ValidateReport struct {
	Conforms bool
	Results  []ValidateResultItem
}

// Validate 对命名图执行 SHACL 校验（转发 internal/ontology/shacl 引擎）。
//
// 形状解析：graph_iri 末段是本体版本 UUID（graphload.GraphIRI 的派生规则），
// 据此从 ontology.ontology_versions 取 manifest 并经 graphload.BuildShapes
// 构造形状——与装载/校验管线共用同一份 manifest → shapes 实现。
func (s *Service) Validate(ctx context.Context, tenantID ids.ID, graphIRI string) (*ValidateReport, error) {
	if err := validateTenantGraph(tenantID, graphIRI); err != nil {
		return nil, err
	}
	shapes, err := s.loadShapes(ctx, tenantID, graphIRI)
	if err != nil {
		return nil, err
	}
	report, err := shacl.Validate(s.Store, tenantID, graphIRI, shapes)
	if err != nil {
		return nil, err
	}
	out := &ValidateReport{Conforms: report.Conforms, Results: make([]ValidateResultItem, 0, len(report.Results))}
	for _, r := range report.Results {
		out.Results = append(out.Results, ValidateResultItem{
			ShapeID: r.ShapeID, FocusNode: r.FocusNode, Path: r.Path,
			Component: r.Component, Severity: r.Severity, Message: r.Message,
		})
	}
	return out, nil
}

// loadShapes 从本体版本 manifest 解析 SHACL 形状。
func (s *Service) loadShapes(ctx context.Context, tenantID ids.ID, graphIRI string) ([]shacl.Shape, error) {
	if s.Pool == nil {
		return nil, errors.E(errors.CodeUnavailable, "推理引擎：数据库连接池未就绪（无法解析 SHACL 形状）")
	}
	// graphload.GraphIRI = <ns base>/<version_id>：末段即本体版本 UUID。
	seg := graphIRI
	if idx := strings.LastIndex(seg, "/"); idx >= 0 {
		seg = seg[idx+1:]
	}
	versionID, err := ids.Parse(seg)
	if err != nil {
		return nil, errors.E(errors.CodeInvalidArgument,
			"graph_iri 不是本体版本数据图（末段不是 UUID），无法解析 SHACL 形状")
	}
	var manifestRaw string
	var ontologyKey string
	err = pgxutil.TenantTx(ctx, s.Pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
            SELECT o.ontology_key, v.manifest::text
              FROM ontology.ontology_versions v
              JOIN ontology.ontologies o ON o.id = v.ontology_id
             WHERE v.id = $1`, versionID.String()).Scan(&ontologyKey, &manifestRaw)
	})
	if err == pgx.ErrNoRows {
		return nil, errors.E(errors.CodeNotFound, "graph_iri 对应的本体版本不存在")
	}
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "推理引擎：读取本体版本失败")
	}
	ns, err := graphload.NewNamespace(ontologyKey)
	if err != nil {
		return nil, err
	}
	parsed, err := graphload.ParseManifest(json.RawMessage(manifestRaw))
	if err != nil {
		return nil, errors.E(errors.CodeValidationFailed, "本体清单解析失败："+err.Error())
	}
	return parsed.BuildShapes(ns)
}

// HealthCheck 探测服务可用性；tenantID 为零值时仅统计内置规则。
func (s *Service) HealthCheck(ctx context.Context, tenantID ids.ID) (string, int64, error) {
	if s.Store == nil {
		return "NOT_SERVING", 0, errors.E(errors.CodeUnavailable, "推理引擎：GraphStore 未装配")
	}
	count := int64(len(builtinRules))
	if s.Pool != nil && !tenantID.IsZero() {
		var custom int64
		err := pgxutil.TenantTx(ctx, s.Pool, tenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`SELECT count(*) FROM reason.rules WHERE is_builtin = FALSE`).Scan(&custom)
		})
		if err != nil {
			return "NOT_SERVING", count, err
		}
		count += custom
	}
	return "SERVING", count, nil
}
