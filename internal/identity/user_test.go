package identity

import (
	"context"
	"testing"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// fakeUserRepo 是 UserRepository 的内存实现（测试专用，不连接数据库）。
type fakeUserRepo struct {
	users map[string]User
}

func newFakeUserRepo() *fakeUserRepo {
	return &fakeUserRepo{users: map[string]User{}}
}

func (f *fakeUserRepo) InsertUser(_ context.Context, u User) error {
	for _, existing := range f.users {
		if existing.LoginName == u.LoginName {
			return errRepoConflict
		}
	}
	f.users[u.ID.String()] = u
	return nil
}

func (f *fakeUserRepo) SelectUser(_ context.Context, userID ids.ID) (User, error) {
	u, ok := f.users[userID.String()]
	if !ok {
		return User{}, errRepoNotFound
	}
	return u, nil
}

func (f *fakeUserRepo) DisableUser(_ context.Context, userID ids.ID) (int64, error) {
	u, ok := f.users[userID.String()]
	if !ok {
		return 0, errRepoNotFound
	}
	if u.Status == "DISABLED" {
		return u.SubjectVersion, nil // 幂等
	}
	u.Status = "DISABLED"
	u.SubjectVersion++
	f.users[userID.String()] = u
	return u.SubjectVersion, nil
}

func (f *fakeUserRepo) BumpSubjectVersion(_ context.Context, userID ids.ID) (int64, error) {
	u, ok := f.users[userID.String()]
	if !ok {
		return 0, errRepoNotFound
	}
	u.SubjectVersion++
	f.users[userID.String()] = u
	return u.SubjectVersion, nil
}

func TestUserCreate(t *testing.T) {
	svc := &UserService{repo: newFakeUserRepo()}
	id, err := svc.Create(context.Background(), "alice", "Alice", "alice@example.com")
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	u, err := svc.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if u.LoginName != "alice" || u.Status != "ACTIVE" || u.SubjectVersion != 1 {
		t.Fatalf("unexpected user: %+v", u)
	}
}

func TestUserCreateDuplicateLoginConflict(t *testing.T) {
	svc := &UserService{repo: newFakeUserRepo()}
	ctx := context.Background()
	if _, err := svc.Create(ctx, "bob", "Bob", ""); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, err := svc.Create(ctx, "bob", "Bob Again", "")
	if errors.As(err).Code != errors.CodeConflict {
		t.Fatalf("duplicate login err = %v, want CONFLICT", err)
	}
}

func TestUserCreateValidatesInput(t *testing.T) {
	svc := &UserService{repo: newFakeUserRepo()}
	if _, err := svc.Create(context.Background(), "", "x", ""); errors.As(err).Code != errors.CodeValidationFailed {
		t.Fatalf("empty login err = %v, want VALIDATION_FAILED", err)
	}
	if _, err := svc.Create(context.Background(), "x", "", ""); errors.As(err).Code != errors.CodeValidationFailed {
		t.Fatalf("empty display name err = %v, want VALIDATION_FAILED", err)
	}
}

func TestUserDisableBumpsSubjectVersion(t *testing.T) {
	repo := newFakeUserRepo()
	svc := &UserService{repo: repo}
	ctx := context.Background()
	id, err := svc.Create(ctx, "carol", "Carol", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := svc.Disable(ctx, id); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	u, err := svc.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if u.Status != "DISABLED" {
		t.Fatalf("status = %s, want DISABLED", u.Status)
	}
	if u.SubjectVersion != 2 {
		t.Fatalf("subject_version = %d, want 2 (旧会话全部失配)", u.SubjectVersion)
	}

	// 幂等：重复 Disable 不再递增版本。
	if err := svc.Disable(ctx, id); err != nil {
		t.Fatalf("second Disable: %v", err)
	}
	u, _ = svc.Get(ctx, id)
	if u.SubjectVersion != 2 {
		t.Fatalf("subject_version after idempotent Disable = %d, want 2", u.SubjectVersion)
	}
}

func TestUserBumpSubjectVersion(t *testing.T) {
	repo := newFakeUserRepo()
	svc := &UserService{repo: repo}
	ctx := context.Background()
	id, err := svc.Create(ctx, "dave", "Dave", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for want := int64(2); want <= 4; want++ {
		got, err := svc.BumpSubjectVersion(ctx, id)
		if err != nil {
			t.Fatalf("BumpSubjectVersion: %v", err)
		}
		if got != want {
			t.Fatalf("version = %d, want %d", got, want)
		}
	}
}

func TestUserOperationsNotFound(t *testing.T) {
	svc := &UserService{repo: newFakeUserRepo()}
	ctx := context.Background()
	missing := ids.New()

	if err := svc.Disable(ctx, missing); errors.As(err).Code != errors.CodeNotFound {
		t.Fatalf("Disable missing err = %v, want NOT_FOUND", err)
	}
	if _, err := svc.BumpSubjectVersion(ctx, missing); errors.As(err).Code != errors.CodeNotFound {
		t.Fatalf("Bump missing err = %v, want NOT_FOUND", err)
	}
	if _, err := svc.Get(ctx, missing); errors.As(err).Code != errors.CodeNotFound {
		t.Fatalf("Get missing err = %v, want NOT_FOUND", err)
	}
}
