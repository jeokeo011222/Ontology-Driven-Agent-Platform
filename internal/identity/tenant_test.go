package identity

import (
	"context"
	"testing"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// fakeTenantRepo 是 TenantRepository 的内存实现（测试专用，不连接数据库）。
type fakeTenantRepo struct {
	byID   map[string]Tenant
	byKey  map[string]string // tenant_key -> id text
	failOn map[string]bool   // 模拟底层故障
}

func newFakeTenantRepo() *fakeTenantRepo {
	return &fakeTenantRepo{byID: map[string]Tenant{}, byKey: map[string]string{}, failOn: map[string]bool{}}
}

func (f *fakeTenantRepo) InsertTenant(_ context.Context, t Tenant) error {
	if f.failOn["insert"] {
		return errRepo("db down")
	}
	if _, ok := f.byKey[t.TenantKey]; ok {
		return errRepoConflict
	}
	f.byID[t.ID.String()] = t
	f.byKey[t.TenantKey] = t.ID.String()
	return nil
}

func (f *fakeTenantRepo) SelectTenantByID(_ context.Context, id ids.ID) (Tenant, error) {
	if f.failOn["select"] {
		return Tenant{}, errRepo("db down")
	}
	t, ok := f.byID[id.String()]
	if !ok {
		return Tenant{}, errRepoNotFound
	}
	return t, nil
}

func (f *fakeTenantRepo) SelectTenantByKey(_ context.Context, key string) (Tenant, error) {
	if f.failOn["select"] {
		return Tenant{}, errRepo("db down")
	}
	idText, ok := f.byKey[key]
	if !ok {
		return Tenant{}, errRepoNotFound
	}
	return f.byID[idText], nil
}

func TestTenantCreateAndGet(t *testing.T) {
	svc := &TenantService{repo: newFakeTenantRepo()}
	ctx := context.Background()

	id, err := svc.Create(ctx, "acme", "Acme Corp")
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if id.IsZero() {
		t.Fatal("Create returned zero id")
	}

	got, err := svc.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if got.TenantKey != "acme" || got.Name != "Acme Corp" || got.Status != "ACTIVE" {
		t.Fatalf("unexpected tenant: %+v", got)
	}
}

func TestTenantCreateRejectsEmptyFields(t *testing.T) {
	svc := &TenantService{repo: newFakeTenantRepo()}
	ctx := context.Background()

	if _, err := svc.Create(ctx, "", "x"); errors.As(err).Code != errors.CodeValidationFailed {
		t.Fatalf("empty key err = %v, want VALIDATION_FAILED", err)
	}
	if _, err := svc.Create(ctx, "k", ""); errors.As(err).Code != errors.CodeValidationFailed {
		t.Fatalf("empty name err = %v, want VALIDATION_FAILED", err)
	}
}

func TestTenantCreateDuplicateKeyConflict(t *testing.T) {
	svc := &TenantService{repo: newFakeTenantRepo()}
	ctx := context.Background()

	if _, err := svc.Create(ctx, "acme", "Acme"); err != nil {
		t.Fatalf("first Create error: %v", err)
	}
	_, err := svc.Create(ctx, "acme", "Acme Again")
	if errors.As(err).Code != errors.CodeConflict {
		t.Fatalf("duplicate key err = %v, want CONFLICT", err)
	}
}

func TestTenantGetNotFound(t *testing.T) {
	svc := &TenantService{repo: newFakeTenantRepo()}
	_, err := svc.Get(context.Background(), ids.New())
	if errors.As(err).Code != errors.CodeNotFound {
		t.Fatalf("Get missing err = %v, want NOT_FOUND", err)
	}
}

func TestEnsureSeedTenantIdempotent(t *testing.T) {
	repo := newFakeTenantRepo()
	svc := &TenantService{repo: repo}
	ctx := context.Background()

	first, err := svc.EnsureSeedTenant(ctx)
	if err != nil {
		t.Fatalf("first EnsureSeedTenant error: %v", err)
	}
	second, err := svc.EnsureSeedTenant(ctx)
	if err != nil {
		t.Fatalf("second EnsureSeedTenant error: %v", err)
	}
	if first != second {
		t.Fatalf("EnsureSeedTenant not idempotent: %s != %s", first, second)
	}
	if len(repo.byID) != 1 {
		t.Fatalf("seed tenant rows = %d, want 1", len(repo.byID))
	}
	seed, err := svc.Get(ctx, first)
	if err != nil {
		t.Fatalf("Get seed error: %v", err)
	}
	if seed.TenantKey != SeedTenantKey || seed.Status != "ACTIVE" {
		t.Fatalf("unexpected seed tenant: %+v", seed)
	}
}

func TestTenantInternalErrorWrapped(t *testing.T) {
	repo := newFakeTenantRepo()
	repo.failOn["insert"] = true
	svc := &TenantService{repo: repo}

	_, err := svc.Create(context.Background(), "boom", "Boom")
	if errors.As(err).Code != errors.CodeInternal {
		t.Fatalf("db failure err = %v, want INTERNAL", err)
	}
}
