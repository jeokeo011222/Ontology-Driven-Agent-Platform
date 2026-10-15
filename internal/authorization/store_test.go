package authorization

import (
	"context"
	"testing"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// fakeDecisionRepo 是 DecisionRepository 的内存实现。
type fakeDecisionRepo struct {
	records  []DecisionRecord
	failWith error
}

func (f *fakeDecisionRepo) InsertDecision(_ context.Context, rec DecisionRecord) error {
	if f.failWith != nil {
		return f.failWith
	}
	for _, r := range f.records {
		if r.DecisionID == rec.DecisionID {
			return errRepoConflict
		}
	}
	f.records = append(f.records, rec)
	return nil
}

func (f *fakeDecisionRepo) SelectDecisionByDecisionID(_ context.Context, tenantID ids.ID, decisionID string) (DecisionRecord, error) {
	for _, r := range f.records {
		if r.TenantID == tenantID && r.DecisionID == decisionID {
			return r, nil
		}
	}
	return DecisionRecord{}, errRepoNotFound
}

func testRequest() Request {
	other := ids.MustParse("22222222-2222-2222-2222-222222222222")
	return Request{
		SubjectID:    ids.MustParse("11111111-1111-1111-1111-111111111111"),
		SubjectType:  "HUMAN",
		OnBehalfOf:   &other,
		Action:       "expense.read",
		ResourceType: "ExpenseClaim",
		ResourceID:   "claim-1",
		Environment:  EnvProduction,
		Attributes:   map[string]any{"amount": float64(100)},
		Scopes:       []string{ScopeTenant},
	}
}

func TestDecisionStoreRecordThreeEffects(t *testing.T) {
	repo := &fakeDecisionRepo{}
	store := &DecisionStore{repo: repo}
	ctx := authzCtx(ids.New())

	effects := []struct {
		effect string
		reason string
	}{
		{EffectAllow, "OWN_READ"},
		{EffectDeny, ReasonNoMatchingRule},
		{EffectRequireApproval, ReasonSelfApproval},
	}
	for _, tc := range effects {
		decisionID, err := store.Record(ctx, Decision{
			Effect:         tc.effect,
			ReasonCode:     tc.reason,
			Obligations:    []string{"create_approval_flow"},
			PolicyVersions: []string{"1.0.0"},
			RiskLevel:      "LOW",
			FactsDigest:    "digest-" + tc.effect,
		}, testRequest())
		if err != nil {
			t.Fatalf("Record(%s): %v", tc.effect, err)
		}
		if decisionID == "" {
			t.Fatalf("Record(%s) returned empty decision id", tc.effect)
		}
		rec, err := store.GetByDecisionID(ctx, mustTenant(ctx), ids.MustParse(decisionID))
		if err != nil {
			t.Fatalf("GetByDecisionID(%s): %v", tc.effect, err)
		}
		if rec.Effect != tc.effect || rec.ReasonCode != tc.reason {
			t.Fatalf("round trip mismatch: %+v", rec)
		}
		if rec.FactsDigest != "digest-"+tc.effect {
			t.Fatalf("facts digest = %s", rec.FactsDigest)
		}
	}
	if len(repo.records) != len(effects) {
		t.Fatalf("stored rows = %d, want %d", len(repo.records), len(effects))
	}
}

// mustTenant 从测试上下文取出租户 ID。
func mustTenant(ctx context.Context) ids.ID {
	id, _ := tenantFromContext(ctx)
	return id
}

func TestDecisionStoreRejectsNonTriStateEffects(t *testing.T) {
	repo := &fakeDecisionRepo{}
	store := &DecisionStore{repo: repo}
	ctx := authzCtx(ids.New())

	for _, effect := range []string{"ERROR", "MAYBE", ""} {
		_, err := store.Record(ctx, Decision{Effect: effect, ReasonCode: "X", FactsDigest: "d"}, testRequest())
		if err == nil {
			t.Fatalf("effect %q must be rejected", effect)
		}
		if code := errors.As(err).Code; code != errors.CodeValidationFailed {
			t.Fatalf("effect %q err code = %s, want VALIDATION_FAILED", effect, code)
		}
	}
	if len(repo.records) != 0 {
		t.Fatalf("非法效果竟落库 %d 行", len(repo.records))
	}
}

func TestDecisionStoreValidations(t *testing.T) {
	store := &DecisionStore{repo: &fakeDecisionRepo{}}
	tenant := ids.New()
	ctx := authzCtx(tenant)

	// 缺少 reason_code。
	if _, err := store.Record(ctx, Decision{Effect: EffectAllow, FactsDigest: "d"}, testRequest()); errors.As(err).Code != errors.CodeValidationFailed {
		t.Fatalf("missing reason err = %v, want VALIDATION_FAILED", err)
	}

	// 非法 subject_type。
	req := testRequest()
	req.SubjectType = "ALIEN"
	if _, err := store.Record(ctx, Decision{Effect: EffectAllow, ReasonCode: "X", FactsDigest: "d"}, req); errors.As(err).Code != errors.CodeValidationFailed {
		t.Fatalf("bad subject type err = %v, want VALIDATION_FAILED", err)
	}

	// 非法 environment。
	req = testRequest()
	req.Environment = "MOON"
	if _, err := store.Record(ctx, Decision{Effect: EffectAllow, ReasonCode: "X", FactsDigest: "d"}, req); errors.As(err).Code != errors.CodeValidationFailed {
		t.Fatalf("bad environment err = %v, want VALIDATION_FAILED", err)
	}

	// 缺少租户上下文。
	if _, err := store.Record(context.Background(), Decision{Effect: EffectAllow, ReasonCode: "X", FactsDigest: "d"}, testRequest()); errors.As(err).Code != errors.CodeUnauthenticated {
		t.Fatalf("no tenant ctx err = %v, want UNAUTHENTICATED", err)
	}
}

func TestDecisionStoreHumanSubjectMappedToUser(t *testing.T) {
	store := &DecisionStore{repo: &fakeDecisionRepo{}}
	ctx := authzCtx(ids.New())
	decisionID, err := store.Record(ctx, Decision{Effect: EffectAllow, ReasonCode: "X", FactsDigest: "d"}, testRequest())
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	rec, err := store.GetByDecisionID(ctx, mustTenant(ctx), ids.MustParse(decisionID))
	if err != nil {
		t.Fatalf("GetByDecisionID: %v", err)
	}
	if rec.SubjectType != "USER" {
		t.Fatalf("subject_type = %s, want USER（HUMAN 映射）", rec.SubjectType)
	}
	if rec.Environment != EnvProduction {
		t.Fatalf("environment = %s", rec.Environment)
	}
	if rec.OnBehalfOf == nil {
		t.Fatal("on_behalf_of 丢失")
	}
	if rec.RequestSnapshot == nil || rec.RequestSnapshot["action"] != "expense.read" {
		t.Fatalf("request snapshot 不完整: %+v", rec.RequestSnapshot)
	}
}

func TestDecisionStoreGetByDecisionIDIsolation(t *testing.T) {
	store := &DecisionStore{repo: &fakeDecisionRepo{}}
	tenantA := ids.New()
	tenantB := ids.New()
	ctxA := authzCtx(tenantA)

	decisionID, err := store.Record(ctxA, Decision{Effect: EffectAllow, ReasonCode: "X", FactsDigest: "d"}, testRequest())
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	// 同租户可查。
	if _, err := store.GetByDecisionID(ctxA, tenantA, ids.MustParse(decisionID)); err != nil {
		t.Fatalf("same tenant lookup: %v", err)
	}
	// 跨租户不可见。
	if _, err := store.GetByDecisionID(authzCtx(tenantB), tenantB, ids.MustParse(decisionID)); errors.As(err).Code != errors.CodeNotFound {
		t.Fatalf("cross tenant lookup err = %v, want NOT_FOUND", err)
	}
	// 不存在的 decision_id。
	if _, err := store.GetByDecisionID(ctxA, tenantA, ids.New()); errors.As(err).Code != errors.CodeNotFound {
		t.Fatalf("missing decision err = %v, want NOT_FOUND", err)
	}
}

func TestDecisionStoreInternalFailure(t *testing.T) {
	store := &DecisionStore{repo: &fakeDecisionRepo{failWith: errRepo("db down")}}
	if _, err := store.Record(authzCtx(ids.New()), Decision{Effect: EffectAllow, ReasonCode: "X", FactsDigest: "d"}, testRequest()); errors.As(err).Code != errors.CodeInternal {
		t.Fatalf("store failure err = %v, want INTERNAL", err)
	}
}
