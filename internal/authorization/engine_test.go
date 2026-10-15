package authorization

import (
	"context"
	"testing"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/tenantctx"
)

// fakePolicyRepo 是 PolicyRepository 的内存实现。
type fakePolicyRepo struct {
	policies []StoredPolicy
	err      error
}

func (f fakePolicyRepo) LoadPublishedPolicies(_ context.Context, _ ids.ID) ([]StoredPolicy, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.policies, nil
}

// fakeClosure 是 DescendantsResolver 的内存实现（orgID -> 子树，含自身）。
type fakeClosure struct {
	descendants map[string][]ids.ID
}

func (f fakeClosure) DescendantIDs(_ context.Context, _, orgID ids.ID) ([]ids.ID, error) {
	return f.descendants[orgID.String()], nil
}

func newTestEngine(policies []StoredPolicy, descendants map[string][]ids.ID) *Engine {
	return &Engine{policies: fakePolicyRepo{policies: policies}, closure: fakeClosure{descendants: descendants}}
}

func authzCtx(tenantID ids.ID) context.Context {
	return tenantctx.WithIdentity(context.Background(), tenantctx.Identity{TenantID: tenantID, RequestID: "test"})
}

func baseRequest() Request {
	return Request{
		SubjectID:    ids.MustParse("11111111-1111-1111-1111-111111111111"),
		SubjectType:  "USER",
		Action:       "expense.read",
		ResourceType: "ExpenseClaim",
		ResourceID:   "claim-1",
		Environment:  EnvProduction,
		Attributes:   map[string]any{},
	}
}

func policyDoc(t *testing.T, rules string) []byte {
	t.Helper()
	return []byte(`{"version":"1","rules":[` + rules + `]}`)
}

func TestDecideAllowDenyRequireApprovalAndDefault(t *testing.T) {
	tenant := ids.New()
	allowRule := `{"id":"allow","action":"expense.read","resource_type":"ExpenseClaim","effect":"ALLOW","reason_code":"OWN_READ","priority":10,"risk_level":"LOW"}`
	denyRule := `{"id":"deny","action":"expense.read","resource_type":"ExpenseClaim","effect":"DENY","reason_code":"LOCKED_CLAIM","priority":1}`
	approvalRule := `{"id":"approval","action":"expense.read","resource_type":"ExpenseClaim","effect":"REQUIRE_APPROVAL","reason_code":"NEED_APPROVAL","priority":5}`

	cases := []struct {
		name       string
		docs       [][]byte
		versions   []string
		wantEffect string
		wantReason string
	}{
		{
			name:       "single allow",
			docs:       [][]byte{policyDoc(t, allowRule)},
			versions:   []string{"1.0.0"},
			wantEffect: EffectAllow,
			wantReason: "OWN_READ",
		},
		{
			name:       "explicit deny beats allow regardless of priority",
			docs:       [][]byte{policyDoc(t, allowRule+","+denyRule)},
			versions:   []string{"1.0.0"},
			wantEffect: EffectDeny,
			wantReason: "LOCKED_CLAIM",
		},
		{
			name:       "require approval beats allow",
			docs:       [][]byte{policyDoc(t, allowRule+","+approvalRule)},
			versions:   []string{"1.0.0"},
			wantEffect: EffectRequireApproval,
			wantReason: "NEED_APPROVAL",
		},
		{
			name:       "default deny when nothing matches",
			docs:       [][]byte{policyDoc(t, `{"id":"other","action":"other.action","resource_type":"Other","effect":"ALLOW","reason_code":"X"}`)},
			versions:   []string{"1.0.0"},
			wantEffect: EffectDeny,
			wantReason: ReasonNoMatchingRule,
		},
		{
			name:       "no published policies defaults to deny",
			docs:       nil,
			versions:   nil,
			wantEffect: EffectDeny,
			wantReason: ReasonNoMatchingRule,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stored := make([]StoredPolicy, 0, len(tc.docs))
			for i, doc := range tc.docs {
				stored = append(stored, StoredPolicy{PolicyKey: "p", Version: tc.versions[i], Document: doc})
			}
			engine := newTestEngine(stored, nil)
			decision, err := engine.Decide(authzCtx(tenant), baseRequest())
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			if decision.Effect != tc.wantEffect {
				t.Fatalf("effect = %s, want %s", decision.Effect, tc.wantEffect)
			}
			if decision.ReasonCode != tc.wantReason {
				t.Fatalf("reason = %s, want %s", decision.ReasonCode, tc.wantReason)
			}
			if decision.FactsDigest == "" {
				t.Fatal("facts digest 不能为空")
			}
		})
	}
}

func TestDecideConditions(t *testing.T) {
	tenant := ids.New()
	rule := `{"id":"high_amount","action":"expense.read","resource_type":"ExpenseClaim","effect":"ALLOW","reason_code":"OK","conditions":[{"field":"amount","op":"gte","value":10000}]}`
	engine := newTestEngine([]StoredPolicy{{PolicyKey: "p", Version: "1.0.0", Document: policyDoc(t, rule)}}, nil)

	// 条件缺失：attributes 中没有 amount → 条件不满足 → 默认拒绝。
	req := baseRequest()
	decision, err := engine.Decide(authzCtx(tenant), req)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision.Effect != EffectDeny || decision.ReasonCode != ReasonNoMatchingRule {
		t.Fatalf("missing condition decision = %+v, want default deny", decision)
	}

	// 条件满足：amount=20000 >= 10000 → ALLOW。
	req.Attributes["amount"] = float64(20000)
	decision, err = engine.Decide(authzCtx(tenant), req)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision.Effect != EffectAllow {
		t.Fatalf("effect = %s, want ALLOW", decision.Effect)
	}

	// 条件不满足：amount=5000 < 10000 → 默认拒绝。
	req.Attributes["amount"] = float64(5000)
	decision, err = engine.Decide(authzCtx(tenant), req)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision.Effect != EffectDeny {
		t.Fatalf("effect = %s, want DENY", decision.Effect)
	}
}

func TestDecideFailClosedOnEvaluationError(t *testing.T) {
	tenant := ids.New()

	// 类型错误：gte 算子遇到字符串操作数 → 求值错误，绝不落 ALLOW/DENY。
	rule := `{"id":"bad_type","action":"expense.read","resource_type":"ExpenseClaim","effect":"ALLOW","reason_code":"OK","conditions":[{"field":"amount","op":"gte","value":10000}]}`
	engine := newTestEngine([]StoredPolicy{{PolicyKey: "p", Version: "1.0.0", Document: policyDoc(t, rule)}}, nil)
	req := baseRequest()
	req.Attributes["amount"] = "not-a-number"
	decision, err := engine.Decide(authzCtx(tenant), req)
	if err == nil {
		t.Fatal("type error must surface as error")
	}
	if code := errors.As(err).Code; code != errors.CodeAUTHZPolicyEvaluationError {
		t.Fatalf("err code = %s, want AUTHZ_POLICY_EVALUATION_ERROR", code)
	}
	if decision.Effect == EffectAllow || decision.Effect == EffectDeny {
		t.Fatalf("fail-closed 违反：求值错误竟落决策 %+v", decision)
	}

	// panic 恢复：eq 两侧为不可比较的 map 会触发运行时 panic，必须被恢复。
	panicRule := `{"id":"panic_rule","action":"expense.read","resource_type":"ExpenseClaim","effect":"ALLOW","reason_code":"OK","conditions":[{"field":"payload","op":"eq","value":{"a":1}}]}`
	engine = newTestEngine([]StoredPolicy{{PolicyKey: "p", Version: "1.0.0", Document: policyDoc(t, panicRule)}}, nil)
	req = baseRequest()
	req.Attributes["payload"] = map[string]any{"a": 1}
	decision, err = engine.Decide(authzCtx(tenant), req)
	if err == nil {
		t.Fatal("panic must be recovered into error")
	}
	if code := errors.As(err).Code; code != errors.CodeAUTHZPolicyEvaluationError {
		t.Fatalf("panic err code = %s, want AUTHZ_POLICY_EVALUATION_ERROR", code)
	}
	if decision.Effect == EffectAllow || decision.Effect == EffectDeny {
		t.Fatalf("fail-closed 违反：panic 后竟落决策 %+v", decision)
	}

	// 已发布但非法的文档：按求值内部错误处理，不落决策。
	engine = newTestEngine([]StoredPolicy{{PolicyKey: "p", Version: "9.9.9", Document: []byte(`{"version":"9","rules":[]}`)}}, nil)
	if _, err := engine.Decide(authzCtx(tenant), baseRequest()); err == nil {
		t.Fatal("invalid published doc must fail closed")
	} else if code := errors.As(err).Code; code != errors.CodeAUTHZPolicyEvaluationError {
		t.Fatalf("invalid doc err code = %s, want AUTHZ_POLICY_EVALUATION_ERROR", code)
	}

	// 存储故障：不落决策，返回 INTERNAL。
	engine = &Engine{policies: fakePolicyRepo{err: errors.E(errors.CodeUnavailable, "db down")}, closure: fakeClosure{}}
	if _, err := engine.Decide(authzCtx(tenant), baseRequest()); errors.As(err).Code != errors.CodeInternal {
		t.Fatalf("store failure err = %v, want INTERNAL", err)
	}
}

func TestDecideScopeChecks(t *testing.T) {
	tenant := ids.New()
	subject := ids.MustParse("11111111-1111-1111-1111-111111111111")
	other := ids.MustParse("22222222-2222-2222-2222-222222222222")
	orgRoot := ids.MustParse("33333333-3333-3333-3333-333333333333")
	orgChild := ids.MustParse("44444444-4444-4444-4444-444444444444")
	orgOther := ids.MustParse("55555555-5555-5555-5555-555555555555")

	allowRule := `{"id":"allow","action":"expense.read","resource_type":"ExpenseClaim","effect":"ALLOW","reason_code":"OK"}`
	engine := newTestEngine([]StoredPolicy{{PolicyKey: "p", Version: "1.0.0", Document: policyDoc(t, allowRule)}},
		map[string][]ids.ID{orgRoot.String(): {orgRoot, orgChild}})

	cases := []struct {
		name       string
		scopes     []string
		attributes map[string]any
		wantEffect string
		wantReason string
	}{
		{
			name:       "TENANT scope covers any org",
			scopes:     []string{ScopeTenant},
			attributes: map[string]any{"org_id": orgOther.String()},
			wantEffect: EffectAllow,
		},
		{
			name:       "ORG scope exact match",
			scopes:     []string{ScopeOrg + orgRoot.String()},
			attributes: map[string]any{"org_id": orgRoot.String()},
			wantEffect: EffectAllow,
		},
		{
			name:       "ORG scope mismatch escalates",
			scopes:     []string{ScopeOrg + orgRoot.String()},
			attributes: map[string]any{"org_id": orgOther.String()},
			wantEffect: EffectDeny,
			wantReason: errors.CodeAUTHZScopeEscalation,
		},
		{
			name:       "ORG_SUBTREE covers descendant org",
			scopes:     []string{ScopeOrgSubtree + orgRoot.String()},
			attributes: map[string]any{"org_id": orgChild.String()},
			wantEffect: EffectAllow,
		},
		{
			name:       "ORG_SUBTREE excludes outsider",
			scopes:     []string{ScopeOrgSubtree + orgRoot.String()},
			attributes: map[string]any{"org_id": orgOther.String()},
			wantEffect: EffectDeny,
			wantReason: errors.CodeAUTHZScopeEscalation,
		},
		{
			name:       "SELF covers own resource",
			scopes:     []string{ScopeSelf},
			attributes: map[string]any{"resource_owner_id": subject.String()},
			wantEffect: EffectAllow,
		},
		{
			name:       "SELF excludes others resource",
			scopes:     []string{ScopeSelf},
			attributes: map[string]any{"resource_owner_id": other.String()},
			wantEffect: EffectDeny,
			wantReason: errors.CodeAUTHZScopeEscalation,
		},
		{
			name:       "ASSIGNED covers assigned-to-self",
			scopes:     []string{ScopeAssigned},
			attributes: map[string]any{"assignee_id": subject.String()},
			wantEffect: EffectAllow,
		},
		{
			name:       "ASSIGNED excludes others assignment",
			scopes:     []string{ScopeAssigned},
			attributes: map[string]any{"assignee_id": other.String()},
			wantEffect: EffectDeny,
			wantReason: errors.CodeAUTHZScopeEscalation,
		},
		{
			name:       "EXPLICIT covers listed ref",
			scopes:     []string{ScopeExplicit + "doc-1"},
			attributes: map[string]any{"resource_ref": "doc-1"},
			wantEffect: EffectAllow,
		},
		{
			name:       "EXPLICIT excludes unlisted ref",
			scopes:     []string{ScopeExplicit + "doc-1"},
			attributes: map[string]any{"resource_ref": "doc-2"},
			wantEffect: EffectDeny,
			wantReason: errors.CodeAUTHZScopeEscalation,
		},
		{
			name:       "no scope dimension passes",
			scopes:     nil,
			attributes: map[string]any{},
			wantEffect: EffectAllow,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := baseRequest()
			req.Scopes = tc.scopes
			req.Attributes = tc.attributes
			decision, err := engine.Decide(authzCtx(tenant), req)
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			if decision.Effect != tc.wantEffect {
				t.Fatalf("effect = %s, want %s", decision.Effect, tc.wantEffect)
			}
			if tc.wantReason != "" && decision.ReasonCode != tc.wantReason {
				t.Fatalf("reason = %s, want %s", decision.ReasonCode, tc.wantReason)
			}
		})
	}
}

func TestDecideSelfApprovalDowngrade(t *testing.T) {
	tenant := ids.New()
	subject := ids.MustParse("11111111-1111-1111-1111-111111111111")
	allowRule := `{"id":"approve","action":"expense.approve","resource_type":"ExpenseClaim","effect":"ALLOW","reason_code":"APPROVER_OK","priority":10}`
	engine := newTestEngine([]StoredPolicy{{PolicyKey: "p", Version: "1.0.0", Document: policyDoc(t, allowRule)}}, nil)

	req := baseRequest()
	req.Action = "expense.approve"
	self := subject
	req.OnBehalfOf = &self

	decision, err := engine.Decide(authzCtx(tenant), req)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision.Effect != EffectRequireApproval {
		t.Fatalf("effect = %s, want REQUIRE_APPROVAL（自我审批降级）", decision.Effect)
	}
	if decision.ReasonCode != ReasonSelfApproval {
		t.Fatalf("reason = %s, want %s", decision.ReasonCode, ReasonSelfApproval)
	}

	// 代表他人时不降级。
	other := ids.MustParse("66666666-6666-6666-6666-666666666666")
	req.OnBehalfOf = &other
	decision, err = engine.Decide(authzCtx(tenant), req)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision.Effect != EffectAllow {
		t.Fatalf("effect = %s, want ALLOW", decision.Effect)
	}
}

func TestDecidePriorityOrderAndPolicyVersions(t *testing.T) {
	tenant := ids.New()
	low := `{"id":"low","action":"expense.read","resource_type":"ExpenseClaim","effect":"ALLOW","reason_code":"LOW","priority":1}`
	high := `{"id":"high","action":"expense.read","resource_type":"ExpenseClaim","effect":"ALLOW","reason_code":"HIGH","priority":99}`
	stored := []StoredPolicy{
		{PolicyKey: "p2", Version: "2.0.0", Document: policyDoc(t, high)},
		{PolicyKey: "p1", Version: "1.0.0", Document: policyDoc(t, low)},
	}
	engine := newTestEngine(stored, nil)

	decision, err := engine.Decide(authzCtx(tenant), baseRequest())
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision.ReasonCode != "HIGH" {
		t.Fatalf("reason = %s, want HIGH（高优先级胜出）", decision.ReasonCode)
	}
	if len(decision.PolicyVersions) != 2 {
		t.Fatalf("policy versions = %v, want 2 个贡献版本", decision.PolicyVersions)
	}
	if decision.PolicyVersions[0] != "1.0.0" || decision.PolicyVersions[1] != "2.0.0" {
		t.Fatalf("policy versions 未排序: %v", decision.PolicyVersions)
	}
}

func TestDecideRequiresTenantContext(t *testing.T) {
	engine := newTestEngine(nil, nil)
	if _, err := engine.Decide(context.Background(), baseRequest()); errors.As(err).Code != errors.CodeUnauthenticated {
		t.Fatalf("no tenant ctx err = %v, want UNAUTHENTICATED", err)
	}
}

func TestDecideValidatesRequest(t *testing.T) {
	tenant := ids.New()
	engine := newTestEngine(nil, nil)

	req := baseRequest()
	req.SubjectID = ids.ID{}
	if _, err := engine.Decide(authzCtx(tenant), req); errors.As(err).Code != errors.CodeValidationFailed {
		t.Fatalf("zero subject err = %v, want VALIDATION_FAILED", err)
	}

	req = baseRequest()
	req.Action = ""
	if _, err := engine.Decide(authzCtx(tenant), req); errors.As(err).Code != errors.CodeValidationFailed {
		t.Fatalf("empty action err = %v, want VALIDATION_FAILED", err)
	}
}
