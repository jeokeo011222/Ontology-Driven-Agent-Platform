package identity

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// User 是用户账号（对应 iam.users）。users 为全局表（登录名全局唯一），
// 与租户的归属关系由 iam.memberships 承载。
type User struct {
	ID               ids.ID
	LoginName        string
	DisplayName      string
	Email            string
	IdentityProvider string
	ExternalSubject  string
	SubjectVersion   int64
	Status           string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// UserRepository 抽象用户持久化。
type UserRepository interface {
	// InsertUser 插入用户（subject_version 初始为 1）；登录名冲突返回 errRepoConflict。
	InsertUser(ctx context.Context, u User) error
	// SelectUser 按 ID 查询；不存在返回 errRepoNotFound。
	SelectUser(ctx context.Context, userID ids.ID) (User, error)
	// DisableUser 停用用户并递增 subject_version（同一事务），
	// 使全部旧会话因版本失配而失效；已停用时为幂等操作，返回当前版本。
	DisableUser(ctx context.Context, userID ids.ID) (int64, error)
	// BumpSubjectVersion 递增 subject_version（撤销传播的核心机制）。
	BumpSubjectVersion(ctx context.Context, userID ids.ID) (int64, error)
}

// UserService 提供用户管理能力。
type UserService struct {
	Pool *pgxpool.Pool
	repo UserRepository
}

// NewUserService 用生产存储（pgxpool）构造服务。
func NewUserService(pool *pgxpool.Pool) *UserService {
	return &UserService{Pool: pool, repo: pgUserRepository{pool: pool}}
}

// Create 创建用户。login_name 全局唯一，冲突返回 CodeConflict。
func (s *UserService) Create(ctx context.Context, loginName, displayName, email string) (ids.ID, error) {
	if loginName == "" {
		return ids.ID{}, errors.E(errors.CodeValidationFailed, "login_name 不能为空")
	}
	if displayName == "" {
		return ids.ID{}, errors.E(errors.CodeValidationFailed, "display_name 不能为空")
	}
	now := time.Now().UTC()
	u := User{
		ID:             ids.New(),
		LoginName:      loginName,
		DisplayName:    displayName,
		Email:          email,
		SubjectVersion: 1,
		Status:         "ACTIVE",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.repo.InsertUser(ctx, u); err != nil {
		if conflict(err) {
			return ids.ID{}, errors.Ef(errors.CodeConflict, "login_name %q 已存在", loginName)
		}
		return ids.ID{}, errors.Wrap(err, errors.CodeInternal, "插入用户失败")
	}
	return u.ID, nil
}

// Disable 停用用户：同一事务内 users.status=DISABLED 且
// subject_version=subject_version+1。旧会话携带的版本号全部失配，
// Validate 时即拒绝（第三篇 §4.4 撤销传播）。重复调用幂等。
func (s *UserService) Disable(ctx context.Context, userID ids.ID) error {
	if _, err := s.repo.DisableUser(ctx, userID); err != nil {
		if notFound(err) {
			return errors.Ef(errors.CodeNotFound, "用户 %s 不存在", userID)
		}
		return errors.Wrap(err, errors.CodeInternal, "停用用户失败")
	}
	return nil
}

// BumpSubjectVersion 递增用户 subject_version，返回新版本号。
// 用于角色变更、委托终止、外部身份解绑等需要广播失效旧会话的场景。
func (s *UserService) BumpSubjectVersion(ctx context.Context, userID ids.ID) (int64, error) {
	version, err := s.repo.BumpSubjectVersion(ctx, userID)
	if err != nil {
		if notFound(err) {
			return 0, errors.Ef(errors.CodeNotFound, "用户 %s 不存在", userID)
		}
		return 0, errors.Wrap(err, errors.CodeInternal, "递增 subject_version 失败")
	}
	return version, nil
}

// Get 按 ID 查询用户。
func (s *UserService) Get(ctx context.Context, userID ids.ID) (User, error) {
	u, err := s.repo.SelectUser(ctx, userID)
	if err != nil {
		if notFound(err) {
			return User{}, errors.Ef(errors.CodeNotFound, "用户 %s 不存在", userID)
		}
		return User{}, errors.Wrap(err, errors.CodeInternal, "查询用户失败")
	}
	return u, nil
}

// pgUserRepository 是 UserRepository 的 PostgreSQL 实现（iam.users 无 RLS）。
type pgUserRepository struct {
	pool *pgxpool.Pool
}

func (r pgUserRepository) InsertUser(ctx context.Context, u User) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO iam.users (id, login_name, display_name, email, identity_provider, external_subject, subject_version, status, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		u.ID.String(), u.LoginName, u.DisplayName, u.Email, u.IdentityProvider, u.ExternalSubject,
		u.SubjectVersion, u.Status, u.CreatedAt, u.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return errRepoConflict
		}
		return err
	}
	return nil
}

func (r pgUserRepository) SelectUser(ctx context.Context, userID ids.ID) (User, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT id, login_name, display_name, email, identity_provider, external_subject, subject_version, status, created_at, updated_at
		 FROM iam.users WHERE id = $1`, userID.String())
	return scanUser(row)
}

func (r pgUserRepository) DisableUser(ctx context.Context, userID ids.ID) (int64, error) {
	// 单事务完成状态与版本号的原子更新（停用与版本失配必须同时生效）。
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var version int64
	err = tx.QueryRow(ctx,
		`UPDATE iam.users
		 SET status = 'DISABLED', subject_version = subject_version + 1, updated_at = now()
		 WHERE id = $1
		 RETURNING subject_version`, userID.String()).Scan(&version)
	if err == nil {
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return 0, commitErr
		}
		return version, nil
	}
	if err != pgx.ErrNoRows {
		return 0, err
	}

	// UPDATE 未命中：同一事务内区分“已停用（幂等）”与“不存在”。
	var (
		status  string
		current int64
	)
	err = tx.QueryRow(ctx,
		`SELECT status, subject_version FROM iam.users WHERE id = $1`, userID.String()).
		Scan(&status, &current)
	if err != nil {
		if err == pgx.ErrNoRows {
			return 0, errRepoNotFound
		}
		return 0, err
	}
	if status != "DISABLED" {
		// 理论不可达：UPDATE 未命中但行存在且未停用（如触发器拦截），按不存在处理。
		return 0, errRepoNotFound
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		return 0, commitErr
	}
	return current, nil
}

func (r pgUserRepository) BumpSubjectVersion(ctx context.Context, userID ids.ID) (int64, error) {
	var version int64
	err := r.pool.QueryRow(ctx,
		`UPDATE iam.users SET subject_version = subject_version + 1, updated_at = now()
		 WHERE id = $1 RETURNING subject_version`, userID.String()).Scan(&version)
	if err != nil {
		if err == pgx.ErrNoRows {
			return 0, errRepoNotFound
		}
		return 0, err
	}
	return version, nil
}

func scanUser(row pgx.Row) (User, error) {
	var (
		u      User
		idText string
		email  *string
		idp    *string
		extSub *string
	)
	err := row.Scan(&idText, &u.LoginName, &u.DisplayName, &email, &idp, &extSub,
		&u.SubjectVersion, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return User{}, errRepoNotFound
		}
		return User{}, err
	}
	if u.ID, err = ids.Parse(idText); err != nil {
		return User{}, err
	}
	if email != nil {
		u.Email = *email
	}
	if idp != nil {
		u.IdentityProvider = *idp
	}
	if extSub != nil {
		u.ExternalSubject = *extSub
	}
	return u, nil
}
