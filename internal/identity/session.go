package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/platform/clock"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
	"odap/internal/platform/tenantctx"
)

// 会话主体类型（iam.sessions.subject_type CHECK 约束）。
const (
	SubjectTypeHuman   = "HUMAN"
	SubjectTypeService = "SERVICE"
	SubjectTypeAgent   = "AGENT"
	SubjectTypeSystem  = "SYSTEM"
)

// Session 对应 iam.sessions。
// 安全约束（第三篇 §4.4）：刷新令牌只存 SHA-256 哈希，禁止明文落库；
// RefreshTokenPlaintext 仅在 Issue/RotateRefresh 当次返回给调用方，绝不持久化。
type Session struct {
	ID                    ids.ID
	TenantID              ids.ID
	SubjectID             ids.ID
	SubjectType           string
	SubjectVersion        int64
	AuthMethod            string
	IssuedAt              time.Time
	LastActiveAt          time.Time
	ExpiresAt             time.Time
	RevokedAt             *time.Time
	RevokeReason          string
	RefreshTokenHash      string
	RefreshExpiresAt      time.Time
	RefreshRotatedAt      *time.Time
	RefreshTokenPlaintext string // 仅内存传递，禁止写入任何存储
}

// SessionRepository 抽象会话持久化。
// RLS 约束：iam.sessions 启用行级安全，全部操作按 (tenant_id, id) 定位。
type SessionRepository interface {
	// InsertSession 插入会话（只持久化哈希，忽略 RefreshTokenPlaintext）。
	InsertSession(ctx context.Context, s Session) error
	// SelectSession 按租户查询会话；不存在返回 errRepoNotFound。
	SelectSession(ctx context.Context, tenantID, sessionID ids.ID) (Session, error)
	// TouchSession 更新 last_active_at。
	TouchSession(ctx context.Context, tenantID, sessionID ids.ID, at time.Time) error
	// RevokeSession 条件撤销（revoked_at IS NULL 才更新）；
	// 已撤销视为幂等成功，不存在返回 errRepoNotFound。
	RevokeSession(ctx context.Context, tenantID, sessionID ids.ID, at time.Time, reason string) error
	// RevokeAllForSubject 撤销某主体在租户内的全部会话（重放响应）。
	RevokeAllForSubject(ctx context.Context, tenantID, subjectID ids.ID, at time.Time, reason string) error
	// RotateRefresh 条件轮换：仅当 refresh_rotated_at IS NULL 时更新
	// 哈希与新过期时间并打轮换时间戳；已轮换返回 errRepoConflict（重放信号），
	// 会话不存在返回 errRepoNotFound。
	RotateRefresh(ctx context.Context, tenantID, sessionID ids.ID, newHash string, newExpiry, rotatedAt time.Time) error
	// SelectTenantStatus 查询租户状态（iam.tenants 无 RLS）。
	SelectTenantStatus(ctx context.Context, tenantID ids.ID) (string, error)
}

// SessionService 提供会话签发、校验、撤销与刷新令牌轮换能力。
type SessionService struct {
	Pool  *pgxpool.Pool
	Clock clock.Clock
	users UserRepository
	repo  SessionRepository
	// RefreshTTL 是轮换后新刷新令牌的 TTL；<=0 时保持原刷新过期时间（绝对寿命不变）。
	RefreshTTL time.Duration
}

// NewSessionService 用生产存储（pgxpool）构造服务。
func NewSessionService(pool *pgxpool.Pool, clk clock.Clock) *SessionService {
	if clk == nil {
		clk = clock.RealClock{}
	}
	return &SessionService{
		Pool:  pool,
		Clock: clk,
		users: pgUserRepository{pool: pool},
		repo:  pgSessionRepository{pool: pool},
	}
}

func (s *SessionService) now() time.Time {
	if s.Clock == nil {
		return time.Now().UTC()
	}
	return s.Clock.Now()
}

// Issue 签发会话。subject_type 为 HUMAN 时从 users 表读取当前
// subject_version 写入会话（撤销传播的基准）；禁用/锁定的用户拒绝签发。
// 返回的 Session.RefreshTokenPlaintext 是唯一一次可见的刷新令牌明文。
func (s *SessionService) Issue(ctx context.Context, tenantID, subjectID ids.ID, subjectType, authMethod string, ttl, refreshTTL time.Duration) (Session, error) {
	if tenantID.IsZero() || subjectID.IsZero() {
		return Session{}, errors.E(errors.CodeValidationFailed, "tenantID 与 subjectID 不能为空")
	}
	if !validSubjectType(subjectType) {
		return Session{}, errors.Ef(errors.CodeValidationFailed, "非法 subject_type %q", subjectType)
	}
	if authMethod == "" {
		return Session{}, errors.E(errors.CodeValidationFailed, "auth_method 不能为空")
	}
	if ttl <= 0 || refreshTTL <= 0 {
		return Session{}, errors.E(errors.CodeValidationFailed, "ttl 与 refreshTTL 必须为正")
	}

	subjectVersion := int64(1)
	if subjectType == SubjectTypeHuman {
		u, err := s.users.SelectUser(ctx, subjectID)
		if err != nil {
			if notFound(err) {
				return Session{}, errors.Ef(errors.CodeNotFound, "用户 %s 不存在", subjectID)
			}
			return Session{}, errors.Wrap(err, errors.CodeInternal, "查询用户失败")
		}
		if u.Status != "ACTIVE" {
			return Session{}, errors.Ef(errors.CodePermissionDenied, "用户状态 %s，拒绝签发会话", u.Status)
		}
		subjectVersion = u.SubjectVersion
	}

	now := s.now()
	plaintext, err := newRefreshToken()
	if err != nil {
		return Session{}, errors.Wrap(err, errors.CodeInternal, "生成刷新令牌失败")
	}
	sess := Session{
		ID:                    ids.New(),
		TenantID:              tenantID,
		SubjectID:             subjectID,
		SubjectType:           subjectType,
		SubjectVersion:        subjectVersion,
		AuthMethod:            authMethod,
		IssuedAt:              now,
		LastActiveAt:          now,
		ExpiresAt:             now.Add(ttl),
		RefreshTokenHash:      hashRefreshToken(plaintext),
		RefreshExpiresAt:      now.Add(refreshTTL),
		RefreshTokenPlaintext: plaintext,
	}
	if err := s.repo.InsertSession(ctx, sess); err != nil {
		return Session{}, errors.Wrap(err, errors.CodeInternal, "写入会话失败")
	}
	return sess, nil
}

// Validate 校验会话并返回可信身份。校验链（全部 fail-closed）：
// 租户上下文存在 → 会话存在 → 未撤销 → 未过期 → 令牌版本与会话绑定一致 →
// 会话版本与主体当前版本一致（撤销传播）→ 租户 ACTIVE。
// 任一生效性失配返回 CodeIAMSessionRevoked（撤销传播的统一出口）。
func (s *SessionService) Validate(ctx context.Context, sessionID ids.ID, subjectVersion int64) (tenantctx.Identity, error) {
	// RLS 约束：会话查询必须携带租户上下文；缺失按未认证处理。
	tenantID, err := tenantFromContext(ctx)
	if err != nil {
		return tenantctx.Identity{}, err
	}
	sess, err := s.repo.SelectSession(ctx, tenantID, sessionID)
	if err != nil {
		if notFound(err) {
			return tenantctx.Identity{}, errors.Ef(errors.CodeNotFound, "会话 %s 不存在", sessionID)
		}
		return tenantctx.Identity{}, errors.Wrap(err, errors.CodeInternal, "查询会话失败")
	}
	// 纵深防御：行级租户与上下文租户必须一致。
	if sess.TenantID != tenantID {
		return tenantctx.Identity{}, errors.Ef(errors.CodeNotFound, "会话 %s 不存在", sessionID)
	}

	now := s.now()
	if sess.RevokedAt != nil {
		return tenantctx.Identity{}, errors.E(errors.CodeIAMSessionRevoked, "会话已撤销")
	}
	if !now.Before(sess.ExpiresAt) {
		return tenantctx.Identity{}, errors.E(errors.CodeIAMSessionRevoked, "会话已过期")
	}
	// 令牌携带的版本必须与签发时绑定的版本一致。
	if sess.SubjectVersion != subjectVersion {
		return tenantctx.Identity{}, errors.E(errors.CodeIAMSessionRevoked, "subject_version 失配，会话已失效（撤销传播）")
	}
	// 撤销传播：签发版本必须仍等于主体当前版本。停用/权限变更会递增
	// 用户 subject_version，旧会话在此全部失配，无需逐会话删除。
	if sess.SubjectType == SubjectTypeHuman {
		u, uerr := s.users.SelectUser(ctx, sess.SubjectID)
		if uerr != nil {
			if notFound(uerr) {
				return tenantctx.Identity{}, errors.E(errors.CodeIAMSessionRevoked, "主体不存在，会话失效")
			}
			return tenantctx.Identity{}, errors.Wrap(uerr, errors.CodeInternal, "查询主体失败")
		}
		if u.SubjectVersion != sess.SubjectVersion {
			return tenantctx.Identity{}, errors.E(errors.CodeIAMSessionRevoked, "subject_version 已递增，会话失效（撤销传播）")
		}
		if u.Status != "ACTIVE" {
			return tenantctx.Identity{}, errors.Ef(errors.CodeIAMSessionRevoked, "主体状态 %s，会话失效", u.Status)
		}
	}

	status, err := s.repo.SelectTenantStatus(ctx, tenantID)
	if err != nil {
		if notFound(err) {
			return tenantctx.Identity{}, errors.E(errors.CodeNotFound, "租户不存在")
		}
		return tenantctx.Identity{}, errors.Wrap(err, errors.CodeInternal, "查询租户状态失败")
	}
	if status != "ACTIVE" {
		return tenantctx.Identity{}, errors.Ef(errors.CodeIAMTenantInactive, "租户状态 %s", status)
	}

	// 活跃时间更新为 best-effort：失败不影响校验结论（审计意义大于控制意义）。
	_ = s.repo.TouchSession(ctx, tenantID, sessionID, now)

	// SessionID 必须显式回传：注销链需要"撤销当前这一个会话"，而 RequestID 是
	// HTTP 请求关联 ID，两者语义不同，早期把会话 ID 塞进 RequestID 会在
	// SessionResolver 里被请求 ID 覆盖，导致 logout 永远撤销不到会话。
	// RequestID 留空，由 middleware 层填充为真实请求 ID。
	return tenantctx.Identity{
		TenantID:  sess.TenantID,
		SubjectID: sess.SubjectID,
		SessionID: sessionID,
	}, nil
}

// Revoke 撤销会话（注销/强制下线）。重复撤销幂等。
func (s *SessionService) Revoke(ctx context.Context, sessionID ids.ID, reason string) error {
	tenantID, err := tenantFromContext(ctx)
	if err != nil {
		return err
	}
	if err := s.repo.RevokeSession(ctx, tenantID, sessionID, s.now(), reason); err != nil {
		if notFound(err) {
			return errors.Ef(errors.CodeNotFound, "会话 %s 不存在", sessionID)
		}
		return errors.Wrap(err, errors.CodeInternal, "撤销会话失败")
	}
	return nil
}

// RotateRefresh 轮换刷新令牌：旧令牌标记 refresh_rotated_at 并签发新令牌。
// 重放检测：已轮换的令牌再次使用即判定泄露，立即撤销该主体全部会话并返回错误。
func (s *SessionService) RotateRefresh(ctx context.Context, sessionID ids.ID) (Session, error) {
	tenantID, err := tenantFromContext(ctx)
	if err != nil {
		return Session{}, err
	}
	sess, err := s.repo.SelectSession(ctx, tenantID, sessionID)
	if err != nil {
		if notFound(err) {
			return Session{}, errors.Ef(errors.CodeNotFound, "会话 %s 不存在", sessionID)
		}
		return Session{}, errors.Wrap(err, errors.CodeInternal, "查询会话失败")
	}

	now := s.now()
	if sess.RevokedAt != nil {
		return Session{}, errors.E(errors.CodeIAMSessionRevoked, "会话已撤销")
	}
	if !now.Before(sess.RefreshExpiresAt) {
		return Session{}, errors.E(errors.CodeIAMSessionRevoked, "刷新令牌已过期")
	}
	if sess.RefreshRotatedAt != nil {
		return Session{}, s.replayResponse(ctx, tenantID, sess.SubjectID, now)
	}

	plaintext, err := newRefreshToken()
	if err != nil {
		return Session{}, errors.Wrap(err, errors.CodeInternal, "生成刷新令牌失败")
	}
	newExpiry := sess.RefreshExpiresAt
	if s.RefreshTTL > 0 {
		newExpiry = now.Add(s.RefreshTTL)
	}
	newHash := hashRefreshToken(plaintext)
	if err := s.repo.RotateRefresh(ctx, tenantID, sessionID, newHash, newExpiry, now); err != nil {
		if conflict(err) {
			// 条件更新未命中：并发场景下旧令牌已被轮换，同样按重放处置。
			return Session{}, s.replayResponse(ctx, tenantID, sess.SubjectID, now)
		}
		if notFound(err) {
			return Session{}, errors.Ef(errors.CodeNotFound, "会话 %s 不存在", sessionID)
		}
		return Session{}, errors.Wrap(err, errors.CodeInternal, "轮换刷新令牌失败")
	}

	rotatedAt := now
	sess.RefreshTokenHash = newHash
	sess.RefreshExpiresAt = newExpiry
	sess.RefreshRotatedAt = &rotatedAt
	sess.RefreshTokenPlaintext = plaintext
	return sess, nil
}

// replayResponse 执行重放响应：撤销该主体全部会话并返回失配错误。
func (s *SessionService) replayResponse(ctx context.Context, tenantID, subjectID ids.ID, now time.Time) error {
	if err := s.repo.RevokeAllForSubject(ctx, tenantID, subjectID, now, "refresh_token_replay"); err != nil {
		return errors.Wrap(err, errors.CodeInternal, "重放响应：撤销全部会话失败")
	}
	return errors.E(errors.CodeIAMSessionRevoked, "检测到刷新令牌重放，已撤销该主体全部会话")
}

func validSubjectType(t string) bool {
	switch t {
	case SubjectTypeHuman, SubjectTypeService, SubjectTypeAgent, SubjectTypeSystem:
		return true
	}
	return false
}

// newRefreshToken 生成 256 位随机刷新令牌明文（hex 编码）。
func newRefreshToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// hashRefreshToken 计算刷新令牌的 SHA-256 哈希（唯一允许的存储形态）。
func hashRefreshToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// pgSessionRepository 是 SessionRepository 的 PostgreSQL 实现。
// iam.sessions 启用 RLS，全部走事务 + SET LOCAL 租户上下文。
type pgSessionRepository struct {
	pool *pgxpool.Pool
}

const sessionColumns = `id, tenant_id, subject_id, subject_type, subject_version, auth_method,
	issued_at, last_active_at, expires_at, revoked_at, revoke_reason,
	refresh_token_hash, refresh_expires_at, refresh_rotated_at`

func (r pgSessionRepository) InsertSession(ctx context.Context, s Session) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := pgxutil.WithTenantTx(ctx, tx, s.TenantID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO iam.sessions
		 (id, tenant_id, subject_id, subject_type, subject_version, auth_method,
		  issued_at, last_active_at, expires_at, refresh_token_hash, refresh_expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		s.ID.String(), s.TenantID.String(), s.SubjectID.String(), s.SubjectType, s.SubjectVersion,
		s.AuthMethod, s.IssuedAt, s.LastActiveAt, s.ExpiresAt, s.RefreshTokenHash, s.RefreshExpiresAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r pgSessionRepository) SelectSession(ctx context.Context, tenantID, sessionID ids.ID) (Session, error) {
	var sess Session
	err := readInTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`SELECT `+sessionColumns+` FROM iam.sessions WHERE tenant_id = $1 AND id = $2`,
			tenantID.String(), sessionID.String())
		var scanErr error
		sess, scanErr = scanSession(row)
		return scanErr
	})
	return sess, err
}

func (r pgSessionRepository) TouchSession(ctx context.Context, tenantID, sessionID ids.ID, at time.Time) error {
	return writeInTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE iam.sessions SET last_active_at = $3 WHERE tenant_id = $1 AND id = $2`,
			tenantID.String(), sessionID.String(), at)
		return err
	})
}

func (r pgSessionRepository) RevokeSession(ctx context.Context, tenantID, sessionID ids.ID, at time.Time, reason string) error {
	return writeInTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE iam.sessions SET revoked_at = $3, revoke_reason = $4
			 WHERE tenant_id = $1 AND id = $2 AND revoked_at IS NULL`,
			tenantID.String(), sessionID.String(), at, reason)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			return nil
		}
		// 未命中：区分“已撤销（幂等成功）”与“不存在”。
		var revokedAt *time.Time
		err = tx.QueryRow(ctx,
			`SELECT revoked_at FROM iam.sessions WHERE tenant_id = $1 AND id = $2`,
			tenantID.String(), sessionID.String()).Scan(&revokedAt)
		if err != nil {
			if err == pgx.ErrNoRows {
				return errRepoNotFound
			}
			return err
		}
		return nil // 已撤销，幂等
	})
}

func (r pgSessionRepository) RevokeAllForSubject(ctx context.Context, tenantID, subjectID ids.ID, at time.Time, reason string) error {
	return writeInTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE iam.sessions SET revoked_at = $3, revoke_reason = $4
			 WHERE tenant_id = $1 AND subject_id = $2 AND revoked_at IS NULL`,
			tenantID.String(), subjectID.String(), at, reason)
		return err
	})
}

func (r pgSessionRepository) RotateRefresh(ctx context.Context, tenantID, sessionID ids.ID, newHash string, newExpiry, rotatedAt time.Time) error {
	return writeInTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE iam.sessions
			 SET refresh_token_hash = $3, refresh_expires_at = $4, refresh_rotated_at = $5
			 WHERE tenant_id = $1 AND id = $2 AND refresh_rotated_at IS NULL`,
			tenantID.String(), sessionID.String(), newHash, newExpiry, rotatedAt)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// 未命中：已轮换（重放）或不存在，交由调用方区分。
			var rotated *time.Time
			scanErr := tx.QueryRow(ctx,
				`SELECT refresh_rotated_at FROM iam.sessions WHERE tenant_id = $1 AND id = $2`,
				tenantID.String(), sessionID.String()).Scan(&rotated)
			if scanErr != nil {
				if scanErr == pgx.ErrNoRows {
					return errRepoNotFound
				}
				return scanErr
			}
			if rotated != nil {
				return errRepoConflict
			}
			return errRepoNotFound
		}
		return nil
	})
}

func (r pgSessionRepository) SelectTenantStatus(ctx context.Context, tenantID ids.ID) (string, error) {
	var status string
	err := r.pool.QueryRow(ctx,
		`SELECT status FROM iam.tenants WHERE id = $1`, tenantID.String()).Scan(&status)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", errRepoNotFound
		}
		return "", err
	}
	return status, nil
}

func scanSession(row pgx.Row) (Session, error) {
	var (
		s        Session
		idText   string
		tenantTx string
		subjTx   string
		revoked  *time.Time
		reason   *string
		rotated  *time.Time
	)
	err := row.Scan(&idText, &tenantTx, &subjTx, &s.SubjectType, &s.SubjectVersion, &s.AuthMethod,
		&s.IssuedAt, &s.LastActiveAt, &s.ExpiresAt, &revoked, &reason,
		&s.RefreshTokenHash, &s.RefreshExpiresAt, &rotated)
	if err != nil {
		if err == pgx.ErrNoRows {
			return Session{}, errRepoNotFound
		}
		return Session{}, err
	}
	var perr error
	if s.ID, perr = ids.Parse(idText); perr != nil {
		return Session{}, perr
	}
	if s.TenantID, perr = ids.Parse(tenantTx); perr != nil {
		return Session{}, perr
	}
	if s.SubjectID, perr = ids.Parse(subjTx); perr != nil {
		return Session{}, perr
	}
	s.RevokedAt = revoked
	s.RefreshRotatedAt = rotated
	if reason != nil {
		s.RevokeReason = *reason
	}
	return s, nil
}
