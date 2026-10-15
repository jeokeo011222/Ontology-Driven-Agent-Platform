// Package identity 实现企业身份域（iam）的核心服务：
// 租户、组织树（闭包表）、用户与会话。
// 存储访问全部经由 Repository 接口抽象，生产实现走 pgxpool + $1 占位，
// 测试使用内存 fake，不连接数据库。
package identity

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// SeedTenantKey / SeedTenantName 是 EnsureSeedTenant 使用的种子租户标识。
// 种子租户承载平台级内置数据（系统角色、权限目录等），幂等可重复执行。
const (
	SeedTenantKey  = "platform"
	SeedTenantName = "ODAP Platform"
)

// Tenant 是租户的领域视图（对应 iam.tenants）。
type Tenant struct {
	ID        ids.ID
	TenantKey string
	Name      string
	Status    string
	Settings  map[string]any
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TenantRepository 抽象租户持久化。
type TenantRepository interface {
	// InsertTenant 插入新租户；tenant_key 冲突时返回 errRepoConflict。
	InsertTenant(ctx context.Context, t Tenant) error
	// SelectTenantByID 按主键查询；不存在返回 errRepoNotFound。
	SelectTenantByID(ctx context.Context, id ids.ID) (Tenant, error)
	// SelectTenantByKey 按业务键查询；不存在返回 errRepoNotFound。
	SelectTenantByKey(ctx context.Context, key string) (Tenant, error)
}

// TenantService 提供租户管理能力。
type TenantService struct {
	Pool *pgxpool.Pool
	repo TenantRepository
}

// NewTenantService 用生产存储（pgxpool）构造服务。
func NewTenantService(pool *pgxpool.Pool) *TenantService {
	return &TenantService{Pool: pool, repo: pgTenantRepository{pool: pool}}
}

// Create 创建租户。tenant_key 全局唯一，冲突返回 CodeConflict。
func (s *TenantService) Create(ctx context.Context, key, name string) (ids.ID, error) {
	if key == "" {
		return ids.ID{}, errors.E(errors.CodeValidationFailed, "tenant key 不能为空")
	}
	if name == "" {
		return ids.ID{}, errors.E(errors.CodeValidationFailed, "tenant name 不能为空")
	}
	now := time.Now().UTC()
	t := Tenant{
		ID:        ids.New(),
		TenantKey: key,
		Name:      name,
		Status:    "ACTIVE",
		Settings:  map[string]any{},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.repo.InsertTenant(ctx, t); err != nil {
		if conflict(err) {
			return ids.ID{}, errors.Ef(errors.CodeConflict, "tenant key %q 已存在", key)
		}
		return ids.ID{}, errors.Wrap(err, errors.CodeInternal, "插入租户失败")
	}
	return t.ID, nil
}

// Get 按 ID 查询租户；不存在返回 CodeNotFound。
func (s *TenantService) Get(ctx context.Context, id ids.ID) (Tenant, error) {
	t, err := s.repo.SelectTenantByID(ctx, id)
	if err != nil {
		if notFound(err) {
			return Tenant{}, errors.Ef(errors.CodeNotFound, "租户 %s 不存在", id)
		}
		return Tenant{}, errors.Wrap(err, errors.CodeInternal, "查询租户失败")
	}
	return t, nil
}

// EnsureSeedTenant 保证种子租户存在（幂等）：
// 已存在则直接返回其 ID，不存在则创建。重复调用不产生重复行。
func (s *TenantService) EnsureSeedTenant(ctx context.Context) (ids.ID, error) {
	existing, err := s.repo.SelectTenantByKey(ctx, SeedTenantKey)
	if err == nil {
		return existing.ID, nil
	}
	if !notFound(err) {
		return ids.ID{}, errors.Wrap(err, errors.CodeInternal, "查询种子租户失败")
	}
	return s.Create(ctx, SeedTenantKey, SeedTenantName)
}

// pgTenantRepository 是 TenantRepository 的 PostgreSQL 实现（RLS：iam.tenants 无租户行级策略）。
type pgTenantRepository struct {
	pool *pgxpool.Pool
}

func (r pgTenantRepository) InsertTenant(ctx context.Context, t Tenant) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO iam.tenants (id, tenant_key, name, status, settings, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		t.ID.String(), t.TenantKey, t.Name, t.Status, jsonbOf(t.Settings), t.CreatedAt, t.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return errRepoConflict
		}
		return err
	}
	return nil
}

func (r pgTenantRepository) SelectTenantByID(ctx context.Context, id ids.ID) (Tenant, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT id, tenant_key, name, status, settings, created_at, updated_at
		 FROM iam.tenants WHERE id = $1`, id.String())
	return scanTenant(row)
}

func (r pgTenantRepository) SelectTenantByKey(ctx context.Context, key string) (Tenant, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT id, tenant_key, name, status, settings, created_at, updated_at
		 FROM iam.tenants WHERE tenant_key = $1`, key)
	return scanTenant(row)
}

func scanTenant(row pgx.Row) (Tenant, error) {
	var (
		t        Tenant
		idText   string
		settings []byte
	)
	err := row.Scan(&idText, &t.TenantKey, &t.Name, &t.Status, &settings, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return Tenant{}, errRepoNotFound
		}
		return Tenant{}, err
	}
	id, err := ids.Parse(idText)
	if err != nil {
		return Tenant{}, err
	}
	t.ID = id
	t.Settings = jsonbMap(settings)
	return t, nil
}
