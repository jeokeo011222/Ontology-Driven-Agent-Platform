package authorization

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
)

// DecisionRecord 是 runtime.authorization_decisions 的一行（决策留痕）。
type DecisionRecord struct {
	ID              string
	DecisionID      string
	TenantID        ids.ID
	SubjectType     string
	SubjectID       ids.ID
	OnBehalfOf      *ids.ID
	DelegationChain []DelegationHop
	Action          string
	ResourceType    string
	ResourceID      string
	Environment     string
	RequestSnapshot map[string]any
	Effect          string
	ReasonCode      string
	Obligations     []string
	PolicyVersions  []string
	RiskLevel       string
	FactsDigest     string
	EvaluatedAt     time.Time
}

// DecisionRepository 抽象授权决策的持久化。
type DecisionRepository interface {
	// InsertDecision 写入决策行；decision_id 冲突返回 errRepoConflict。
	InsertDecision(ctx context.Context, rec DecisionRecord) error
	// SelectDecisionByDecisionID 按 (tenant_id, decision_id) 查询；不存在返回 errRepoNotFound。
	SelectDecisionByDecisionID(ctx context.Context, tenantID ids.ID, decisionID string) (DecisionRecord, error)
}

// DecisionStore 提供授权决策的记录与回查（P0-4：tool_calls 的引用目标）。
type DecisionStore struct {
	Pool *pgxpool.Pool
	repo DecisionRepository
}

// NewDecisionStore 用生产存储（pgxpool）构造。
func NewDecisionStore(pool *pgxpool.Pool) *DecisionStore {
	return &DecisionStore{Pool: pool, repo: pgDecisionRepository{pool: pool}}
}

// Record 持久化授权决策并返回 decision_id 文本。
// 只落三态（ALLOW/DENY/REQUIRE_APPROVAL）；ERROR 及其他非法效果不落库，
// 直接返回错误（fail-closed：不可信的决策不得进入证据链）。
func (s *DecisionStore) Record(ctx context.Context, d Decision, req Request) (string, error) {
	switch d.Effect {
	case EffectAllow, EffectDeny, EffectRequireApproval:
	default:
		return "", errors.Ef(errors.CodeValidationFailed, "非法决策效果 %q，拒绝落库", d.Effect)
	}
	if d.ReasonCode == "" {
		return "", errors.E(errors.CodeValidationFailed, "决策缺少 reason_code，拒绝落库")
	}
	subjectType, err := decisionSubjectType(req.SubjectType)
	if err != nil {
		return "", err
	}
	environment := req.Environment
	if environment == "" {
		environment = DefaultEnvironment
	}
	if !validEnvironment(environment) {
		return "", errors.Ef(errors.CodeValidationFailed, "非法环境 %q", environment)
	}
	tenantID, err := tenantFromContext(ctx)
	if err != nil {
		return "", err
	}

	rec := DecisionRecord{
		ID:              ids.New().String(),
		DecisionID:      ids.New().String(),
		TenantID:        tenantID,
		SubjectType:     subjectType,
		SubjectID:       req.SubjectID,
		OnBehalfOf:      req.OnBehalfOf,
		DelegationChain: req.DelegationChain,
		Action:          req.Action,
		ResourceType:    req.ResourceType,
		ResourceID:      req.ResourceID,
		Environment:     environment,
		RequestSnapshot: requestSnapshot(req),
		Effect:          d.Effect,
		ReasonCode:      d.ReasonCode,
		Obligations:     d.Obligations,
		PolicyVersions:  d.PolicyVersions,
		RiskLevel:       d.RiskLevel,
		FactsDigest:     d.FactsDigest,
		EvaluatedAt:     time.Now().UTC(),
	}
	if err := s.repo.InsertDecision(ctx, rec); err != nil {
		if conflict(err) {
			return "", errors.E(errors.CodeConflict, "decision_id 冲突")
		}
		return "", errors.Wrap(err, errors.CodeInternal, "写入授权决策失败")
	}
	return rec.DecisionID, nil
}

// GetByDecisionID 按 decision_id 回查决策（按租户隔离）。
func (s *DecisionStore) GetByDecisionID(ctx context.Context, tenantID, decisionID ids.ID) (DecisionRecord, error) {
	rec, err := s.repo.SelectDecisionByDecisionID(ctx, tenantID, decisionID.String())
	if err != nil {
		if notFound(err) {
			return DecisionRecord{}, errors.Ef(errors.CodeNotFound, "授权决策 %s 不存在", decisionID)
		}
		return DecisionRecord{}, errors.Wrap(err, errors.CodeInternal, "查询授权决策失败")
	}
	return rec, nil
}

// decisionSubjectType 把请求主体类型映射为决策表约束的取值。
func decisionSubjectType(subjectType string) (string, error) {
	switch subjectType {
	case "USER", "AGENT", "SERVICE", "SYSTEM":
		return subjectType, nil
	case "HUMAN": // iam.sessions 的主体类型在决策表中记为用户
		return "USER", nil
	case "":
		return "", errors.E(errors.CodeValidationFailed, "subject_type 不能为空")
	default:
		return "", errors.Ef(errors.CodeValidationFailed, "非法 subject_type %q", subjectType)
	}
}

func validEnvironment(env string) bool {
	switch env {
	case EnvDevelopment, EnvStaging, EnvProduction:
		return true
	}
	return false
}

// requestSnapshot 构造请求快照（证据链留痕）。
func requestSnapshot(req Request) map[string]any {
	snapshot := map[string]any{
		"subject_id":    req.SubjectID.String(),
		"subject_type":  req.SubjectType,
		"action":        req.Action,
		"resource_type": req.ResourceType,
		"resource_id":   req.ResourceID,
		"environment":   req.Environment,
		"scopes":        req.Scopes,
		"attributes":    req.Attributes,
	}
	if req.OnBehalfOf != nil {
		snapshot["on_behalf_of"] = req.OnBehalfOf.String()
	}
	if len(req.DelegationChain) > 0 {
		snapshot["delegation_chain"] = req.DelegationChain
	}
	return snapshot
}

// pgDecisionRepository 是 DecisionRepository 的 PostgreSQL 实现（RLS：runtime.authorization_decisions）。
type pgDecisionRepository struct {
	pool *pgxpool.Pool
}

func (r pgDecisionRepository) InsertDecision(ctx context.Context, rec DecisionRecord) error {
	chain, err := jsonBytes(rec.DelegationChain)
	if err != nil {
		return err
	}
	obligations, err := jsonBytes(rec.Obligations)
	if err != nil {
		return err
	}
	versions, err := jsonBytes(rec.PolicyVersions)
	if err != nil {
		return err
	}
	snapshot, err := json.Marshal(rec.RequestSnapshot)
	if err != nil {
		return err
	}
	var onBehalfOf any
	if rec.OnBehalfOf != nil {
		onBehalfOf = rec.OnBehalfOf.String()
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := pgxutil.WithTenantTx(ctx, tx, rec.TenantID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO runtime.authorization_decisions
		 (id, tenant_id, decision_id, subject_type, subject_id, on_behalf_of, delegation_chain,
		  action, resource_type, resource_id, environment, request_snapshot,
		  effect, reason_code, obligations, policy_versions, risk_level, facts_digest, evaluated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)`,
		rec.ID, rec.TenantID.String(), rec.DecisionID, rec.SubjectType, rec.SubjectID.String(), onBehalfOf, chain,
		rec.Action, rec.ResourceType, rec.ResourceID, rec.Environment, snapshot,
		rec.Effect, rec.ReasonCode, obligations, versions, rec.RiskLevel, rec.FactsDigest, rec.EvaluatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return errRepoConflict
		}
		return err
	}
	return tx.Commit(ctx)
}

func (r pgDecisionRepository) SelectDecisionByDecisionID(ctx context.Context, tenantID ids.ID, decisionID string) (DecisionRecord, error) {
	var rec DecisionRecord
	err := readInAuthzTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`SELECT id, tenant_id, decision_id, subject_type, subject_id, on_behalf_of, delegation_chain,
			        action, resource_type, resource_id, environment, request_snapshot,
			        effect, reason_code, obligations, policy_versions, risk_level, facts_digest, evaluated_at
			 FROM runtime.authorization_decisions
			 WHERE tenant_id = $1 AND decision_id = $2`,
			tenantID.String(), decisionID)
		var (
			idText, tenantText, subjectText string
			onBehalfText                    *string
			chain, obligations, versions    []byte
			snapshot                        []byte
		)
		scanErr := row.Scan(&idText, &tenantText, &rec.DecisionID, &rec.SubjectType, &subjectText, &onBehalfText, &chain,
			&rec.Action, &rec.ResourceType, &rec.ResourceID, &rec.Environment, &snapshot,
			&rec.Effect, &rec.ReasonCode, &obligations, &versions, &rec.RiskLevel, &rec.FactsDigest, &rec.EvaluatedAt)
		if scanErr != nil {
			if scanErr == pgx.ErrNoRows {
				return errRepoNotFound
			}
			return scanErr
		}
		var perr error
		rec.ID = idText
		if rec.TenantID, perr = ids.Parse(tenantText); perr != nil {
			return perr
		}
		if rec.SubjectID, perr = ids.Parse(subjectText); perr != nil {
			return perr
		}
		if onBehalfText != nil {
			id, perr := ids.Parse(*onBehalfText)
			if perr != nil {
				return perr
			}
			rec.OnBehalfOf = &id
		}
		if perr = json.Unmarshal(chain, &rec.DelegationChain); perr != nil {
			return perr
		}
		if perr = json.Unmarshal(obligations, &rec.Obligations); perr != nil {
			return perr
		}
		if perr = json.Unmarshal(versions, &rec.PolicyVersions); perr != nil {
			return perr
		}
		return json.Unmarshal(snapshot, &rec.RequestSnapshot)
	})
	return rec, err
}

// jsonBytes 把切片编码为 JSONB 字节（nil 编码为 []）。
func jsonBytes(v any) ([]byte, error) {
	if v == nil {
		return []byte("[]"), nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return b, nil
}
