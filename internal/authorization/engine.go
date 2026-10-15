package authorization

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/platform/clock"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
)

// 默认拒绝与范围越界的理由码。
const (
	ReasonNoMatchingRule  = "NO_MATCHING_RULE"
	ReasonSelfApproval    = "SELF_APPROVAL_REQUIRED"
	ReasonScopeEscalation = errors.CodeAUTHZScopeEscalation
	ReasonPolicyInvalid   = "PUBLISHED_POLICY_INVALID"
	DefaultEnvironment    = "PRODUCTION"
)

// 环境取值（runtime.authorization_decisions.environment CHECK）。
const (
	EnvDevelopment = "DEVELOPMENT"
	EnvStaging     = "STAGING"
	EnvProduction  = "PRODUCTION"
)

// 数据范围前缀（第三篇 §5.3 六种 scope 的文本形态）。
const (
	ScopeTenant     = "TENANT"
	ScopeOrg        = "ORG:"
	ScopeOrgSubtree = "ORG_SUBTREE:"
	ScopeSelf       = "SELF"
	ScopeAssigned   = "ASSIGNED"
	ScopeExplicit   = "EXPLICIT:"
)

// DelegationHop 是委托链上的一跳（A 委托 B，B 委托 C……）。
type DelegationHop struct {
	SubjectID   ids.ID `json:"subject_id"`
	SubjectType string `json:"subject_type"`
}

// Request 是统一授权请求（第三篇 §7.2 的 V1 子集）。
// Attributes 是受信属性源：条件求值只读这里，缺失即条件不满足。
type Request struct {
	SubjectID       ids.ID
	SubjectType     string
	OnBehalfOf      *ids.ID
	DelegationChain []DelegationHop
	Action          string
	ResourceType    string
	ResourceID      string
	Attributes      map[string]any
	Scopes          []string
	Environment     string
}

// Decision 是授权决策（三态 + 理由 + 义务 + 依据 + 事实指纹）。
type Decision struct {
	Effect         string
	ReasonCode     string
	Obligations    []string
	PolicyVersions []string
	RiskLevel      string
	FactsDigest    string
}

// StoredPolicy 是存储层的已发布策略版本。
type StoredPolicy struct {
	PolicyKey string
	Version   string
	Document  []byte
}

// PolicyRepository 抽象已发布策略的加载。
type PolicyRepository interface {
	// LoadPublishedPolicies 加载租户内 status=PUBLISHED 的策略版本。
	LoadPublishedPolicies(ctx context.Context, tenantID ids.ID) ([]StoredPolicy, error)
}

// DescendantsResolver 抽象组织闭包查询（ORG_SUBTREE 展开用）。
type DescendantsResolver interface {
	// DescendantIDs 返回 orgID 及其全部后代 ID（含自身）。
	DescendantIDs(ctx context.Context, tenantID, orgID ids.ID) ([]ids.ID, error)
}

// Engine 是统一授权决策引擎。
type Engine struct {
	Pool     *pgxpool.Pool
	Clock    clock.Clock
	policies PolicyRepository
	closure  DescendantsResolver
}

// NewEngine 用生产存储构造引擎。
func NewEngine(pool *pgxpool.Pool, clk clock.Clock) *Engine {
	if clk == nil {
		clk = clock.RealClock{}
	}
	return &Engine{
		Pool:     pool,
		Clock:    clk,
		policies: pgPolicyRepository{pool: pool},
		closure:  pgClosureResolver{pool: pool},
	}
}

// Decide 执行授权决策。fail-closed 基调（contracts/02）：
// 任何加载/解析/求值异常都返回错误且绝不产出 ALLOW/DENY 决策；
// 策略越界返回携带 CodeAUTHZScopeEscalation 的 DENY 决策。
func (e *Engine) Decide(ctx context.Context, req Request) (Decision, error) {
	if req.SubjectID.IsZero() {
		return Decision{}, errors.E(errors.CodeValidationFailed, "subject_id 不能为空")
	}
	if req.Action == "" || req.ResourceType == "" {
		return Decision{}, errors.E(errors.CodeValidationFailed, "action 与 resource_type 不能为空")
	}
	tenantID, err := tenantFromContext(ctx)
	if err != nil {
		return Decision{}, err
	}

	factsDigest := computeFactsDigest(req)
	scopeDecision := Decision{
		Effect:      EffectDeny,
		ReasonCode:  ReasonScopeEscalation,
		FactsDigest: factsDigest,
	}

	// 1. 范围检查：请求必须落在已授权数据范围内，越界即 DENY（AUTHZ_SCOPE_ESCALATION）。
	allowed, err := e.scopeAllowed(ctx, tenantID, req)
	if err != nil {
		return Decision{}, err // 求值内部错误：不带任何决策返回（fail-closed）
	}
	if !allowed {
		return scopeDecision, nil
	}

	// 2. 加载该租户 PUBLISHED 策略版本（按 version 排序）。
	stored, err := e.policies.LoadPublishedPolicies(ctx, tenantID)
	if err != nil {
		return Decision{}, errors.Wrap(err, errors.CodeInternal, "加载已发布策略失败")
	}
	sort.SliceStable(stored, func(i, j int) bool { return stored[i].Version < stored[j].Version })

	policies := make([]Policy, 0, len(stored))
	for _, sp := range stored {
		p, perr := ParsePolicy(sp.Document)
		if perr != nil {
			// 已发布文档理论上通过过发布校验；运行期解析失败按内部错误处理。
			return Decision{}, errors.Wrap(perr, errors.CodeAUTHZPolicyEvaluationError, ReasonPolicyInvalid)
		}
		policies = append(policies, p)
	}

	// 3. 规则按 Priority 求值（显式 DENY 优先于 ALLOW；无命中默认拒绝）。
	decision, err := e.evaluate(policies, stored, req, factsDigest)
	if err != nil {
		return Decision{}, err
	}

	// 4. 自我审批检测：代表自己且将放行时降级为 REQUIRE_APPROVAL。
	if decision.Effect == EffectAllow && req.OnBehalfOf != nil && *req.OnBehalfOf == req.SubjectID {
		decision.Effect = EffectRequireApproval
		decision.ReasonCode = ReasonSelfApproval
		decision.Obligations = []string{"create_approval_flow"}
	}
	return decision, nil
}

// evaluate 汇总全部规则求值结果（panic 恢复 + 类型错误均按求值失败处理）。
func (e *Engine) evaluate(policies []Policy, stored []StoredPolicy, req Request, factsDigest string) (decision Decision, err error) {
	defer func() {
		if r := recover(); r != nil {
			decision = Decision{}
			err = errors.Wrap(fmt.Errorf("panic: %v", r), errors.CodeAUTHZPolicyEvaluationError, "授权求值器内部错误")
		}
	}()

	type scoredRule struct {
		rule    Rule
		version string
	}
	var matched []scoredRule

	for i, p := range policies {
		version := stored[i].Version
		for _, rule := range p.Rules {
			if rule.Action != req.Action || rule.ResourceType != req.ResourceType {
				continue
			}
			ok, cerr := conditionsSatisfied(rule.Conditions, req.Attributes)
			if cerr != nil {
				return Decision{}, cerr // 类型错误：fail-closed，不落任何决策
			}
			if ok {
				matched = append(matched, scoredRule{rule: rule, version: version})
			}
		}
	}

	// 稳定排序：Priority 降序，同级按 version 升序、规则 ID 升序。
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].rule.Priority != matched[j].rule.Priority {
			return matched[i].rule.Priority > matched[j].rule.Priority
		}
		if matched[i].version != matched[j].version {
			return matched[i].version < matched[j].version
		}
		return matched[i].rule.ID < matched[j].rule.ID
	})

	if len(matched) == 0 {
		return Decision{
			Effect:      EffectDeny,
			ReasonCode:  ReasonNoMatchingRule,
			FactsDigest: factsDigest,
		}, nil
	}

	// 合成优先级：DENY > REQUIRE_APPROVAL > ALLOW（第三篇 §7.3）。
	winner := matched[0].rule
	for _, m := range matched {
		if m.rule.Effect == EffectDeny {
			winner = m.rule
			break
		}
		if m.rule.Effect == EffectRequireApproval && winner.Effect == EffectAllow {
			winner = m.rule
		}
	}

	versions := make([]string, 0, len(matched))
	seen := make(map[string]bool, len(matched))
	for _, m := range matched {
		if m.version != "" && !seen[m.version] {
			seen[m.version] = true
			versions = append(versions, m.version)
		}
	}
	sort.Strings(versions)

	decision = Decision{
		Effect:         winner.Effect,
		ReasonCode:     winner.ReasonCode,
		PolicyVersions: versions,
		RiskLevel:      winner.RiskLevel,
		FactsDigest:    factsDigest,
	}
	if decision.Effect == EffectRequireApproval {
		decision.Obligations = []string{"create_approval_flow"}
	}
	return decision, nil
}

// conditionsSatisfied 评估规则条件（AND 组合）。
// 条件字段只从 attributes 取：缺失即条件不满足。
func conditionsSatisfied(conditions []Condition, attributes map[string]any) (bool, error) {
	for _, cond := range conditions {
		value, present := attributes[cond.Field]
		if !present {
			return false, nil
		}
		ok, err := evalCondition(cond, value)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

func evalCondition(cond Condition, actual any) (bool, error) {
	switch cond.Op {
	case OpExists:
		return true, nil // 字段已取到（缺失在调用方已处理）
	case OpEq:
		return valuesEqual(actual, cond.Value), nil
	case OpNe:
		return !valuesEqual(actual, cond.Value), nil
	case OpIn:
		list, ok := cond.Value.([]any)
		if !ok {
			return false, typeError(cond, "value 必须是数组")
		}
		for _, item := range list {
			if valuesEqual(actual, item) {
				return true, nil
			}
		}
		return false, nil
	case OpNotIn:
		list, ok := cond.Value.([]any)
		if !ok {
			return false, typeError(cond, "value 必须是数组")
		}
		for _, item := range list {
			if valuesEqual(actual, item) {
				return false, nil
			}
		}
		return true, nil
	case OpGte, OpLte:
		return compareOrdered(cond, actual)
	case OpContains:
		return containsValue(cond, actual)
	default:
		// 发布校验已拦截未知算子；运行期兜底按求值失败处理。
		return false, typeError(cond, "未知算子")
	}
}

func typeError(cond Condition, detail string) error {
	return errors.Wrap(fmt.Errorf("条件 %s %s: %s", cond.Field, cond.Op, detail),
		errors.CodeAUTHZPolicyEvaluationError, "授权求值器类型错误")
}

// valuesEqual 做数值感知的相等比较（JSON 数字统一按 float64）。
func valuesEqual(a, b any) bool {
	an, aok := toFloat(a)
	bn, bok := toFloat(b)
	if aok && bok {
		return an == bn
	}
	if aok != bok {
		return false
	}
	return a == b
}

// compareOrdered 执行 gte/lte 比较（数值或字符串；类型不匹配按求值失败处理）。
func compareOrdered(cond Condition, actual any) (bool, error) {
	an, aok := toFloat(actual)
	bn, bok := toFloat(cond.Value)
	if aok && bok {
		if cond.Op == OpGte {
			return an >= bn, nil
		}
		return an <= bn, nil
	}
	as, aIsStr := actual.(string)
	bs, bIsStr := cond.Value.(string)
	if aIsStr && bIsStr {
		if cond.Op == OpGte {
			return as >= bs, nil
		}
		return as <= bs, nil
	}
	return false, typeError(cond, "操作数不是可比较的数值/字符串")
}

func containsValue(cond Condition, actual any) (bool, error) {
	switch container := cond.Value.(type) {
	case string:
		s, ok := actual.(string)
		if !ok {
			return false, nil
		}
		return strings.Contains(s, container), nil
	case []any:
		for _, item := range container {
			if valuesEqual(actual, item) {
				return true, nil
			}
		}
		return false, nil
	default:
		return false, typeError(cond, "value 必须是字符串或数组")
	}
}

// toFloat 把 JSON/Go 数值统一转换为 float64。
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// scopeAllowed 执行六种数据范围检查。全部维度都被覆盖才放行；
// 任一维度越界返回 false（调用方落 AUTHZ_SCOPE_ESCALATION 的 DENY 决策）。
func (e *Engine) scopeAllowed(ctx context.Context, tenantID ids.ID, req Request) (bool, error) {
	hasTenantScope := false
	var orgScopes, subtreeScopes []string
	var explicitRefs []string
	hasSelf, hasAssigned := false, false
	for _, scope := range req.Scopes {
		switch {
		case scope == ScopeTenant:
			hasTenantScope = true
		case strings.HasPrefix(scope, ScopeOrg):
			orgScopes = append(orgScopes, strings.TrimPrefix(scope, ScopeOrg))
		case strings.HasPrefix(scope, ScopeOrgSubtree):
			subtreeScopes = append(subtreeScopes, strings.TrimPrefix(scope, ScopeOrgSubtree))
		case strings.HasPrefix(scope, ScopeExplicit):
			explicitRefs = append(explicitRefs, strings.TrimPrefix(scope, ScopeExplicit))
		case scope == ScopeSelf:
			hasSelf = true
		case scope == ScopeAssigned:
			hasAssigned = true
		}
	}
	if hasTenantScope {
		return true, nil // TENANT 范围覆盖租户内全部数据
	}

	// 子树展开缓存：ORG_SUBTREE:<root> -> 该子树全部组织 ID（含根）。
	subtreeCache := make(map[string]map[string]bool)
	expandSubtree := func(root string) (map[string]bool, error) {
		if cached, ok := subtreeCache[root]; ok {
			return cached, nil
		}
		rootID, err := ids.Parse(root)
		if err != nil {
			return nil, errors.Wrap(err, errors.CodeAUTHZPolicyEvaluationError, "scope 中的组织 ID 非法")
		}
		descendants, err := e.closure.DescendantIDs(ctx, tenantID, rootID)
		if err != nil {
			return nil, errors.Wrap(err, errors.CodeAUTHZPolicyEvaluationError, "展开 ORG_SUBTREE 失败")
		}
		set := make(map[string]bool, len(descendants))
		for _, d := range descendants {
			set[d.String()] = true
		}
		subtreeCache[root] = set
		return set, nil
	}
	orgCovered := func(orgID string) (bool, error) {
		for _, s := range orgScopes {
			if s == orgID {
				return true, nil
			}
		}
		for _, root := range subtreeScopes {
			set, err := expandSubtree(root)
			if err != nil {
				return false, err
			}
			if set[orgID] {
				return true, nil
			}
		}
		return false, nil
	}
	refCovered := func(ref string) bool {
		for _, r := range explicitRefs {
			if r == ref {
				return true
			}
		}
		return false
	}

	subjectText := req.SubjectID.String()

	// 四个数据范围维度：任一“存在但未覆盖”的维度都构成越界。
	anyPresent, escalated := false, false
	cover := func(present, covered bool) {
		if !present {
			return
		}
		anyPresent = true
		if !covered {
			escalated = true
		}
	}

	if orgText, ok := stringAttr(req.Attributes, "org_id"); ok {
		covered, cerr := orgCovered(orgText)
		if cerr != nil {
			return false, cerr
		}
		cover(true, covered)
	} else if orgText, ok := stringAttr(req.Attributes, "owner_org_id"); ok {
		covered, cerr := orgCovered(orgText)
		if cerr != nil {
			return false, cerr
		}
		cover(true, covered)
	}
	if ownerText, ok := stringAttr(req.Attributes, "resource_owner_id"); ok {
		cover(true, (ownerText == subjectText && hasSelf) || refCovered(ownerText))
	}
	if assigneeText, ok := stringAttr(req.Attributes, "assignee_id"); ok {
		cover(true, (assigneeText == subjectText && hasAssigned) || refCovered(assigneeText))
	}
	if refText, ok := stringAttr(req.Attributes, "resource_ref"); ok {
		cover(true, refCovered(refText))
	}

	if escalated {
		return false, nil // 越界：任一维度未覆盖即拒绝
	}
	if !anyPresent {
		return true, nil // 无数据范围要求的动作级检查
	}
	return true, nil
}

func stringAttr(attributes map[string]any, key string) (string, bool) {
	v, ok := attributes[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// computeFactsDigest 计算请求事实指纹（确定性 JSON + SHA-256），
// 用于决策留痕与事后复核（attributes 键序由 json.Marshal 排序保证）。
func computeFactsDigest(req Request) string {
	facts := map[string]any{
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
		facts["on_behalf_of"] = req.OnBehalfOf.String()
	}
	raw, err := json.Marshal(facts)
	if err != nil {
		raw = []byte(req.SubjectID.String() + "|" + req.Action + "|" + req.ResourceType)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// pgPolicyRepository 是 PolicyRepository 的 PostgreSQL 实现（RLS：governance.policy_versions）。
type pgPolicyRepository struct {
	pool *pgxpool.Pool
}

func (r pgPolicyRepository) LoadPublishedPolicies(ctx context.Context, tenantID ids.ID) ([]StoredPolicy, error) {
	var out []StoredPolicy
	err := readInAuthzTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		rows, qErr := tx.Query(ctx,
			`SELECT policy_key, version, policy_document
			 FROM governance.policy_versions
			 WHERE tenant_id = $1 AND status = 'PUBLISHED'
			 ORDER BY version`, tenantID.String())
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			var sp StoredPolicy
			if scanErr := rows.Scan(&sp.PolicyKey, &sp.Version, &sp.Document); scanErr != nil {
				return scanErr
			}
			out = append(out, sp)
		}
		return rows.Err()
	})
	return out, err
}

// pgClosureResolver 是 DescendantsResolver 的 PostgreSQL 实现（RLS：iam.org_closure）。
type pgClosureResolver struct {
	pool *pgxpool.Pool
}

func (r pgClosureResolver) DescendantIDs(ctx context.Context, tenantID, orgID ids.ID) ([]ids.ID, error) {
	var out []ids.ID
	err := readInAuthzTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		rows, qErr := tx.Query(ctx,
			`SELECT DISTINCT descendant_id FROM iam.org_closure
			 WHERE tenant_id = $1 AND ancestor_id = $2`,
			tenantID.String(), orgID.String())
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			var text string
			if scanErr := rows.Scan(&text); scanErr != nil {
				return scanErr
			}
			id, parseErr := ids.Parse(text)
			if parseErr != nil {
				return parseErr
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	return out, err
}

// readInAuthzTenantTx 在租户事务（SET LOCAL）中执行只读查询（RLS 安全路径）。
func readInAuthzTenantTx(ctx context.Context, pool *pgxpool.Pool, tenantID ids.ID, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := pgxutil.WithTenantTx(ctx, tx, tenantID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
