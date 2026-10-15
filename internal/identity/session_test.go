package identity

import (
	"context"
	"testing"
	"time"

	"odap/internal/platform/clock"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/tenantctx"
)

// fakeSessionRepo 是 SessionRepository 的内存实现（测试专用，不连接数据库）。
// 约定与 PG 实现一致：绝不持久化刷新令牌明文；条件更新语义用哨兵错误表达。
type fakeSessionRepo struct {
	sessions     map[string]Session
	tenantStatus map[string]string
}

func newFakeSessionRepo() *fakeSessionRepo {
	return &fakeSessionRepo{sessions: map[string]Session{}, tenantStatus: map[string]string{}}
}

func sessionKey(tenantID, sessionID ids.ID) string {
	return tenantID.String() + ":" + sessionID.String()
}

func (f *fakeSessionRepo) InsertSession(_ context.Context, s Session) error {
	// 与 PG 实现对齐：明文字段不进入存储（只落 refresh_token_hash 列）。
	s.RefreshTokenPlaintext = ""
	f.sessions[sessionKey(s.TenantID, s.ID)] = s
	return nil
}

func (f *fakeSessionRepo) SelectSession(_ context.Context, tenantID, sessionID ids.ID) (Session, error) {
	s, ok := f.sessions[sessionKey(tenantID, sessionID)]
	if !ok {
		return Session{}, errRepoNotFound
	}
	return s, nil
}

func (f *fakeSessionRepo) TouchSession(_ context.Context, tenantID, sessionID ids.ID, at time.Time) error {
	key := sessionKey(tenantID, sessionID)
	s, ok := f.sessions[key]
	if !ok {
		return errRepoNotFound
	}
	s.LastActiveAt = at
	f.sessions[key] = s
	return nil
}

func (f *fakeSessionRepo) RevokeSession(_ context.Context, tenantID, sessionID ids.ID, at time.Time, reason string) error {
	key := sessionKey(tenantID, sessionID)
	s, ok := f.sessions[key]
	if !ok {
		return errRepoNotFound
	}
	if s.RevokedAt != nil {
		return nil // 已撤销，幂等
	}
	s.RevokedAt = &at
	s.RevokeReason = reason
	f.sessions[key] = s
	return nil
}

func (f *fakeSessionRepo) RevokeAllForSubject(_ context.Context, tenantID, subjectID ids.ID, at time.Time, reason string) error {
	for key, s := range f.sessions {
		if s.TenantID == tenantID && s.SubjectID == subjectID && s.RevokedAt == nil {
			s.RevokedAt = &at
			s.RevokeReason = reason
			f.sessions[key] = s
		}
	}
	return nil
}

func (f *fakeSessionRepo) RotateRefresh(_ context.Context, tenantID, sessionID ids.ID, newHash string, newExpiry, rotatedAt time.Time) error {
	key := sessionKey(tenantID, sessionID)
	s, ok := f.sessions[key]
	if !ok {
		return errRepoNotFound
	}
	if s.RefreshRotatedAt != nil {
		return errRepoConflict // 已轮换：重放信号
	}
	s.RefreshTokenHash = newHash
	s.RefreshExpiresAt = newExpiry
	rotated := rotatedAt
	s.RefreshRotatedAt = &rotated
	f.sessions[key] = s
	return nil
}

func (f *fakeSessionRepo) SelectTenantStatus(_ context.Context, tenantID ids.ID) (string, error) {
	st, ok := f.tenantStatus[tenantID.String()]
	if !ok {
		return "", errRepoNotFound
	}
	return st, nil
}

// newTestSessionService 组装内存 fake 的会话服务。
func newTestSessionService(clk clock.Clock) (*SessionService, *fakeSessionRepo, *fakeUserRepo) {
	sessions := newFakeSessionRepo()
	users := newFakeUserRepo()
	svc := &SessionService{Clock: clk, repo: sessions, users: users}
	return svc, sessions, users
}

// mustCreateUser 经 UserService 创建测试用户。
func mustCreateUser(t *testing.T, users *fakeUserRepo, ctx context.Context, login string) ids.ID {
	t.Helper()
	id, err := (&UserService{repo: users}).Create(ctx, login, login, "")
	if err != nil {
		t.Fatalf("create user %s: %v", login, err)
	}
	return id
}

func TestSessionIssueStoresHashOnly(t *testing.T) {
	clk := &clock.FakeClock{Current: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	svc, repo, users := newTestSessionService(clk)
	ctx := context.Background()
	tenant := ids.New()
	repo.tenantStatus[tenant.String()] = "ACTIVE"
	userID := mustCreateUser(t, users, ctx, "alice")

	sess, err := svc.Issue(ctx, tenant, userID, SubjectTypeHuman, "password", 15*time.Minute, 24*time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if sess.RefreshTokenPlaintext == "" {
		t.Fatal("Issue 必须当次返回刷新令牌明文")
	}
	if sess.RefreshTokenHash == sess.RefreshTokenPlaintext {
		t.Fatal("存储形态必须是 SHA-256 哈希，禁止明文")
	}
	if sess.SubjectVersion != 1 {
		t.Fatalf("subject_version = %d, want 1", sess.SubjectVersion)
	}
	stored, err := repo.SelectSession(ctx, tenant, sess.ID)
	if err != nil {
		t.Fatalf("SelectSession: %v", err)
	}
	if stored.RefreshTokenPlaintext != "" {
		t.Fatal("持久化会话中出现了刷新令牌明文")
	}
	if stored.RefreshTokenHash != sess.RefreshTokenHash {
		t.Fatal("落库哈希与签发哈希不一致")
	}
}

func TestSessionValidateTable(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tenant := ids.New()

	type setup struct {
		name         string
		advance      time.Duration
		revoke       bool
		disableUser  bool
		tenantStatus string
		checkVersion int64
		wantErrCode  string // 空表示期望成功
	}

	cases := []setup{
		{name: "happy path", checkVersion: 1, tenantStatus: "ACTIVE"},
		{name: "expired", advance: 16 * time.Minute, checkVersion: 1, tenantStatus: "ACTIVE", wantErrCode: errors.CodeIAMSessionRevoked},
		{name: "revoked", revoke: true, checkVersion: 1, tenantStatus: "ACTIVE", wantErrCode: errors.CodeIAMSessionRevoked},
		{name: "revocation propagation via subject_version", disableUser: true, checkVersion: 1, tenantStatus: "ACTIVE", wantErrCode: errors.CodeIAMSessionRevoked},
		{name: "token version mismatch after bump", disableUser: true, checkVersion: 2, tenantStatus: "ACTIVE", wantErrCode: errors.CodeIAMSessionRevoked},
		{name: "tenant suspended", checkVersion: 1, tenantStatus: "SUSPENDED", wantErrCode: errors.CodeIAMTenantInactive},
		{name: "tenant archived", checkVersion: 1, tenantStatus: "ARCHIVED", wantErrCode: errors.CodeIAMTenantInactive},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clk := &clock.FakeClock{Current: base}
			svc, repo, users := newTestSessionService(clk)
			ctx := context.Background()
			repo.tenantStatus[tenant.String()] = tc.tenantStatus
			userID := mustCreateUser(t, users, ctx, "u-"+tc.name)
			sess, err := svc.Issue(ctx, tenant, userID, SubjectTypeHuman, "password", 15*time.Minute, 24*time.Hour)
			if err != nil {
				t.Fatalf("Issue: %v", err)
			}
			if tc.disableUser {
				if err := (&UserService{repo: users}).Disable(ctx, userID); err != nil {
					t.Fatalf("Disable: %v", err)
				}
			}
			if tc.revoke {
				vctx := tenantctx.WithIdentity(ctx, tenantctx.Identity{TenantID: tenant})
				if err := svc.Revoke(vctx, sess.ID, "logout"); err != nil {
					t.Fatalf("Revoke: %v", err)
				}
			}
			clk.Advance(tc.advance)

			vctx := tenantctx.WithIdentity(ctx, tenantctx.Identity{TenantID: tenant, RequestID: "req-1"})
			identity, err := svc.Validate(vctx, sess.ID, tc.checkVersion)
			if tc.wantErrCode == "" {
				if err != nil {
					t.Fatalf("Validate error: %v", err)
				}
				if identity.TenantID != tenant || identity.SubjectID != userID {
					t.Fatalf("unexpected identity: %+v", identity)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate expected %s, got success", tc.wantErrCode)
			}
			if got := errors.As(err).Code; got != tc.wantErrCode {
				t.Fatalf("Validate err = %s, want %s", got, tc.wantErrCode)
			}
		})
	}
}

// TestSessionRevocationPropagationEvidence 是撤销传播的端到端证据：
// 停用用户 → 旧会话（v1）全部失配；重新签发的会话（v2）正常有效。
func TestSessionRevocationPropagationEvidence(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := &clock.FakeClock{Current: base}
	svc, repo, users := newTestSessionService(clk)
	ctx := context.Background()
	tenant := ids.New()
	repo.tenantStatus[tenant.String()] = "ACTIVE"
	userID := mustCreateUser(t, users, ctx, "alice")
	vctx := tenantctx.WithIdentity(ctx, tenantctx.Identity{TenantID: tenant})

	oldSession, err := svc.Issue(ctx, tenant, userID, SubjectTypeHuman, "password", 15*time.Minute, 24*time.Hour)
	if err != nil {
		t.Fatalf("Issue old: %v", err)
	}
	if _, err := svc.Validate(vctx, oldSession.ID, oldSession.SubjectVersion); err != nil {
		t.Fatalf("old session should validate before disable: %v", err)
	}

	// 停用用户：users.status=DISABLED 且 subject_version+1（同一事务）。
	if err := (&UserService{repo: users}).Disable(ctx, userID); err != nil {
		t.Fatalf("Disable: %v", err)
	}

	// 证据 1：旧会话失配（撤销传播，无需逐会话删除）。
	if _, err := svc.Validate(vctx, oldSession.ID, oldSession.SubjectVersion); errors.As(err).Code != errors.CodeIAMSessionRevoked {
		t.Fatalf("old session after disable err = %v, want IAM_SESSION_REVOKED", err)
	}

	// 证据 2：重新启用后签发的新会话携带新版本，验证通过。
	u, _ := users.SelectUser(ctx, userID)
	u.Status = "ACTIVE"
	users.users[userID.String()] = u
	newSession, err := svc.Issue(ctx, tenant, userID, SubjectTypeHuman, "password", 15*time.Minute, 24*time.Hour)
	if err != nil {
		t.Fatalf("Issue new: %v", err)
	}
	if newSession.SubjectVersion != oldSession.SubjectVersion+1 {
		t.Fatalf("new session version = %d, want %d", newSession.SubjectVersion, oldSession.SubjectVersion+1)
	}
	identity, err := svc.Validate(vctx, newSession.ID, newSession.SubjectVersion)
	if err != nil {
		t.Fatalf("new session should validate: %v", err)
	}
	if identity.SubjectID != userID {
		t.Fatalf("identity subject = %s, want %s", identity.SubjectID, userID)
	}
}

func TestSessionValidateMissingAndNoTenantContext(t *testing.T) {
	clk := &clock.FakeClock{Current: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	svc, repo, _ := newTestSessionService(clk)
	ctx := context.Background()
	tenant := ids.New()
	repo.tenantStatus[tenant.String()] = "ACTIVE"

	vctx := tenantctx.WithIdentity(ctx, tenantctx.Identity{TenantID: tenant})
	if _, err := svc.Validate(vctx, ids.New(), 1); errors.As(err).Code != errors.CodeNotFound {
		t.Fatalf("missing session err = %v, want NOT_FOUND", err)
	}
	if _, err := svc.Validate(ctx, ids.New(), 1); errors.As(err).Code != errors.CodeUnauthenticated {
		t.Fatalf("no tenant ctx err = %v, want UNAUTHENTICATED", err)
	}
}

func TestSessionIssueValidations(t *testing.T) {
	clk := &clock.FakeClock{Current: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	svc, repo, users := newTestSessionService(clk)
	ctx := context.Background()
	tenant := ids.New()
	repo.tenantStatus[tenant.String()] = "ACTIVE"
	userID := mustCreateUser(t, users, ctx, "alice")

	cases := []struct {
		name        string
		tenantID    ids.ID
		subjectID   ids.ID
		subjectType string
		ttl         time.Duration
		refreshTTL  time.Duration
	}{
		{"zero tenant", ids.ID{}, userID, SubjectTypeHuman, time.Minute, time.Hour},
		{"bad subject type", tenant, userID, "ALIEN", time.Minute, time.Hour},
		{"zero ttl", tenant, userID, SubjectTypeHuman, 0, time.Hour},
		{"zero refresh ttl", tenant, userID, SubjectTypeHuman, time.Minute, 0},
		{"missing user", tenant, ids.New(), SubjectTypeHuman, time.Minute, time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Issue(ctx, tc.tenantID, tc.subjectID, tc.subjectType, "password", tc.ttl, tc.refreshTTL)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if code := errors.As(err).Code; code != errors.CodeValidationFailed && code != errors.CodeNotFound {
				t.Fatalf("err = %s, want VALIDATION_FAILED or NOT_FOUND", code)
			}
		})
	}

	// 禁用用户拒绝签发。
	if err := (&UserService{repo: users}).Disable(ctx, userID); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	_, err := svc.Issue(ctx, tenant, userID, SubjectTypeHuman, "password", time.Minute, time.Hour)
	if errors.As(err).Code != errors.CodePermissionDenied {
		t.Fatalf("disabled user issue err = %v, want PERMISSION_DENIED", err)
	}
}

func TestSessionRotateAndReplay(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := &clock.FakeClock{Current: base}
	svc, repo, users := newTestSessionService(clk)
	svc.RefreshTTL = 24 * time.Hour
	ctx := context.Background()
	tenant := ids.New()
	repo.tenantStatus[tenant.String()] = "ACTIVE"
	userID := mustCreateUser(t, users, ctx, "alice")

	sess, err := svc.Issue(ctx, tenant, userID, SubjectTypeHuman, "password", 15*time.Minute, 24*time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// 同主体第二个会话：用于验证重放响应撤销“全部”会话。
	other, err := svc.Issue(ctx, tenant, userID, SubjectTypeHuman, "password", 15*time.Minute, 24*time.Hour)
	if err != nil {
		t.Fatalf("Issue second: %v", err)
	}

	vctx := tenantctx.WithIdentity(ctx, tenantctx.Identity{TenantID: tenant})

	rotated, err := svc.RotateRefresh(vctx, sess.ID)
	if err != nil {
		t.Fatalf("RotateRefresh: %v", err)
	}
	if rotated.RefreshTokenPlaintext == "" || rotated.RefreshTokenPlaintext == sess.RefreshTokenPlaintext {
		t.Fatal("轮换必须签发新的刷新令牌明文")
	}
	if rotated.RefreshRotatedAt == nil {
		t.Fatal("旧令牌必须标记 refresh_rotated_at")
	}
	if rotated.RefreshTokenHash == sess.RefreshTokenHash {
		t.Fatal("轮换后哈希必须更新")
	}

	// 重放：已轮换令牌再次使用 → 立即撤销全部会话并返回错误。
	clk.Advance(time.Minute)
	if _, err := svc.RotateRefresh(vctx, sess.ID); err == nil {
		t.Fatal("replayed rotate expected error")
	} else if errors.As(err).Code != errors.CodeIAMSessionRevoked {
		t.Fatalf("replay err = %s, want IAM_SESSION_REVOKED", errors.As(err).Code)
	}

	// 撤销传播证据：两个会话都被撤销，Validate 均失配。
	for _, id := range []ids.ID{sess.ID, other.ID} {
		if _, err := svc.Validate(vctx, id, 1); errors.As(err).Code != errors.CodeIAMSessionRevoked {
			t.Fatalf("session %s after replay err = %v, want IAM_SESSION_REVOKED", id, err)
		}
	}
}

func TestSessionRotateExpiredRefresh(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := &clock.FakeClock{Current: base}
	svc, repo, users := newTestSessionService(clk)
	ctx := context.Background()
	tenant := ids.New()
	repo.tenantStatus[tenant.String()] = "ACTIVE"
	userID := mustCreateUser(t, users, ctx, "alice")

	sess, err := svc.Issue(ctx, tenant, userID, SubjectTypeHuman, "password", 15*time.Minute, time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	vctx := tenantctx.WithIdentity(ctx, tenantctx.Identity{TenantID: tenant})
	clk.Advance(2 * time.Hour) // 刷新令牌也已过期
	if _, err := svc.RotateRefresh(vctx, sess.ID); errors.As(err).Code != errors.CodeIAMSessionRevoked {
		t.Fatalf("expired refresh err = %v, want IAM_SESSION_REVOKED", err)
	}
}

func TestSessionRevokeIdempotentAndNotFound(t *testing.T) {
	clk := &clock.FakeClock{Current: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	svc, repo, users := newTestSessionService(clk)
	ctx := context.Background()
	tenant := ids.New()
	repo.tenantStatus[tenant.String()] = "ACTIVE"
	userID := mustCreateUser(t, users, ctx, "alice")
	sess, err := svc.Issue(ctx, tenant, userID, SubjectTypeHuman, "password", 15*time.Minute, time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	vctx := tenantctx.WithIdentity(ctx, tenantctx.Identity{TenantID: tenant})

	if err := svc.Revoke(vctx, sess.ID, "logout"); err != nil {
		t.Fatalf("first Revoke: %v", err)
	}
	if err := svc.Revoke(vctx, sess.ID, "logout again"); err != nil {
		t.Fatalf("second Revoke should be idempotent: %v", err)
	}
	if err := svc.Revoke(vctx, ids.New(), "x"); errors.As(err).Code != errors.CodeNotFound {
		t.Fatalf("revoke missing err = %v, want NOT_FOUND", err)
	}
}
