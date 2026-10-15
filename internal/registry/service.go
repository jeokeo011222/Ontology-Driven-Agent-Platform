package registry

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
	"odap/internal/platform/tenantctx"
)

// 插件版本状态（registry.plugin_versions.status CHECK）。
const (
	VersionCreated    = "CREATED"
	VersionValidating = "VALIDATING"
	VersionTesting    = "TESTING"
	VersionStaged     = "STAGED"
	VersionApproved   = "APPROVED"
	VersionPublished  = "PUBLISHED"
	VersionDeprecated = "DEPRECATED"
	VersionDisabled   = "DISABLED"
	VersionRejected   = "REJECTED"
)

// 安装状态（registry.plugin_installations.installation_state CHECK）。
const (
	InstallActive     = "ACTIVE"
	InstallUpgrading  = "UPGRADING"
	InstallRolledBack = "ROLLED_BACK"
	InstallDisabled   = "DISABLED"
)

// 插件状态（registry.plugins.status CHECK）。
const (
	PluginStatusActive   = "ACTIVE"
	PluginStatusDisabled = "DISABLED"
	PluginStatusArchived = "ARCHIVED"
)

// publishTransitions 是发布状态机的正向跃迁表（逆向一律拒绝）。
var publishTransitions = map[string]string{
	VersionCreated:    VersionValidating,
	VersionValidating: VersionTesting,
	VersionTesting:    VersionStaged,
	VersionStaged:     VersionApproved,
	VersionApproved:   VersionPublished,
}

// 存储层哨兵错误。
var (
	errRepoNotFound     = errRepo("not found")
	errRepoConflict     = errRepo("conflict")
	errRepoInvalidState = errRepo("invalid state")
)

type errRepo string

func (e errRepo) Error() string { return string(e) }

// Plugin 是插件定义（registry.plugins）。
type Plugin struct {
	ID          ids.ID
	TenantID    ids.ID // 全零表示平台级插件（tenant_id NULL，对所有租户可见）
	PluginKey   string
	PluginKind  string
	Name        string
	Description string
	OwnerOrgID  ids.ID
	Status      string
	CreatedAt   time.Time
}

// PluginVersion 是插件版本（registry.plugin_versions）。
type PluginVersion struct {
	ID             ids.ID
	TenantID       ids.ID
	PluginID       ids.ID
	Version        string
	Manifest       []byte
	ArtifactURI    string
	ArtifactDigest string
	SignatureRef   string
	Status         string
	CreatedBy      ids.ID
	CreatedAt      time.Time
	PublishedAt    *time.Time
}

// Installation 是安装记录（registry.plugin_installations，升级历史保留）。
type Installation struct {
	ID                ids.ID
	TenantID          ids.ID
	PluginID          ids.ID
	PluginVersionID   ids.ID
	InstallationState string
	ConfigJSON        map[string]any
	InstalledBy       ids.ID
	InstalledAt       time.Time
	UpdatedAt         time.Time
}

// PluginRepository 抽象插件域持久化。
// 写操作在实现内部保证原子性（PG 走事务 + SET LOCAL 租户上下文）。
type PluginRepository interface {
	InsertPlugin(ctx context.Context, p Plugin) error
	SelectPluginByKey(ctx context.Context, tenantID ids.ID, key string) (Plugin, error)
	SelectPlugin(ctx context.Context, tenantID, pluginID ids.ID) (Plugin, error)

	InsertPluginVersion(ctx context.Context, v PluginVersion) error
	SelectPluginVersion(ctx context.Context, tenantID, pluginID ids.ID, version string) (PluginVersion, error)
	SelectPluginVersionByID(ctx context.Context, tenantID, versionID ids.ID) (PluginVersion, error)
	UpdatePluginVersionStatus(ctx context.Context, tenantID, versionID ids.ID, status string, publishedAt *time.Time) error

	SelectActiveInstallation(ctx context.Context, tenantID, pluginID ids.ID) (Installation, error)
	SelectInstallation(ctx context.Context, tenantID, installID ids.ID) (Installation, error)
	SelectInstallations(ctx context.Context, tenantID, pluginID ids.ID) ([]Installation, error)
	InsertInstallation(ctx context.Context, inst Installation) error
	UpdateInstallationState(ctx context.Context, tenantID, installID ids.ID, state string, at time.Time) error

	// InstallVersion 原子安装/升级：插入 UPGRADING 新行 → 兼容校验 →
	// 同事务旧行 ROLLED_BACK + 新行 ACTIVE。校验失败整体回滚（旧行保持原状）。
	// 相同 (tenant,plugin,version) 已激活时为幂等操作，返回既有行 ID。
	InstallVersion(ctx context.Context, inst Installation) (ids.ID, error)
	// SwitchActiveInstallation 原子切换激活安装（回滚，仅状态迁移）：
	// 目标行 → ACTIVE，原 ACTIVE 行 → ROLLED_BACK。
	SwitchActiveInstallation(ctx context.Context, tenantID, pluginID, targetInstallID ids.ID, at time.Time) error
}

// PluginService 提供插件注册、版本发布与安装管理能力。
type PluginService struct {
	Pool *pgxpool.Pool
	repo PluginRepository
}

// NewPluginService 用生产存储（pgxpool）构造服务。
func NewPluginService(pool *pgxpool.Pool) *PluginService {
	return &PluginService{Pool: pool, repo: pgPluginRepository{pool: pool}}
}

// RegisterPlugin 注册插件（租户内 plugin_key 唯一，冲突返回 CodeConflict）。
func (s *PluginService) RegisterPlugin(ctx context.Context, key, kind, name string) (ids.ID, error) {
	tenantID, err := tenantFromContext(ctx)
	if err != nil {
		return ids.ID{}, err
	}
	if key == "" {
		return ids.ID{}, errors.E(errors.CodeValidationFailed, "plugin_key 不能为空")
	}
	if !pluginKinds[kind] {
		return ids.ID{}, errors.Ef(errors.CodeValidationFailed, "非法插件类型 %q", kind)
	}
	if name == "" {
		return ids.ID{}, errors.E(errors.CodeValidationFailed, "插件名不能为空")
	}
	if _, err := s.repo.SelectPluginByKey(ctx, tenantID, key); err == nil {
		return ids.ID{}, errors.Ef(errors.CodeConflict, "plugin_key %q 已存在", key)
	} else if !notFound(err) {
		return ids.ID{}, errors.Wrap(err, errors.CodeInternal, "查询插件失败")
	}

	p := Plugin{
		ID:         ids.New(),
		TenantID:   tenantID,
		PluginKey:  key,
		PluginKind: kind,
		Name:       name,
		Status:     PluginStatusActive,
		CreatedAt:  time.Now().UTC(),
	}
	if err := s.repo.InsertPlugin(ctx, p); err != nil {
		if conflict(err) {
			return ids.ID{}, errors.Ef(errors.CodeConflict, "plugin_key %q 已存在", key)
		}
		return ids.ID{}, errors.Wrap(err, errors.CodeInternal, "注册插件失败")
	}
	return p.ID, nil
}

// CreateVersion 创建插件版本。幂等键 (tenant, plugin, version)：
// 已存在且 artifact_digest 一致时返回既有版本 ID；digest 不一致返回
// CodePluginVersionConflict（同版本号不得复用不同制品）。
func (s *PluginService) CreateVersion(ctx context.Context, pluginID ids.ID, version, manifest string, artifactDigest string) (ids.ID, error) {
	tenantID, err := tenantFromContext(ctx)
	if err != nil {
		return ids.ID{}, err
	}
	if version == "" {
		return ids.ID{}, errors.E(errors.CodeValidationFailed, "version 不能为空")
	}
	if artifactDigest == "" {
		return ids.ID{}, errors.E(errors.CodeValidationFailed, "artifact_digest 不能为空")
	}
	if err := ValidateManifest([]byte(manifest)); err != nil {
		return ids.ID{}, err
	}
	if _, err := s.repo.SelectPlugin(ctx, tenantID, pluginID); err != nil {
		if notFound(err) {
			return ids.ID{}, errors.Ef(errors.CodePluginNotFound, "插件 %s 不存在", pluginID)
		}
		return ids.ID{}, errors.Wrap(err, errors.CodeInternal, "查询插件失败")
	}

	existing, err := s.repo.SelectPluginVersion(ctx, tenantID, pluginID, version)
	if err == nil {
		if existing.ArtifactDigest == artifactDigest {
			return existing.ID, nil // 幂等重放
		}
		return ids.ID{}, errors.Ef(errors.CodePluginVersionConflict,
			"版本 %s 已存在且制品摘要不同（防版本号复用）", version)
	}
	if !notFound(err) {
		return ids.ID{}, errors.Wrap(err, errors.CodeInternal, "查询插件版本失败")
	}

	subjectID, err := subjectFromContext(ctx)
	if err != nil {
		return ids.ID{}, err
	}
	v := PluginVersion{
		ID:             ids.New(),
		TenantID:       tenantID,
		PluginID:       pluginID,
		Version:        version,
		Manifest:       []byte(manifest),
		ArtifactDigest: artifactDigest,
		Status:         VersionCreated,
		CreatedBy:      subjectID,
		CreatedAt:      time.Now().UTC(),
	}
	if err := s.repo.InsertPluginVersion(ctx, v); err != nil {
		if conflict(err) {
			// 并发创建同一版本：按幂等语义复核。
			existing, serr := s.repo.SelectPluginVersion(ctx, tenantID, pluginID, version)
			if serr == nil && existing.ArtifactDigest == artifactDigest {
				return existing.ID, nil
			}
			return ids.ID{}, errors.Ef(errors.CodePluginVersionConflict, "版本 %s 已存在", version)
		}
		return ids.ID{}, errors.Wrap(err, errors.CodeInternal, "创建插件版本失败")
	}
	return v.ID, nil
}

// Publish 推进版本状态机：CREATED→VALIDATING→TESTING→STAGED→APPROVED→PUBLISHED。
// 逆向与跨级跃迁一律拒绝（CodePluginVersionConflict）；已 PUBLISHED 时幂等成功。
func (s *PluginService) Publish(ctx context.Context, versionID ids.ID) error {
	tenantID, err := tenantFromContext(ctx)
	if err != nil {
		return err
	}
	v, err := s.repo.SelectPluginVersionByID(ctx, tenantID, versionID)
	if err != nil {
		if notFound(err) {
			return errors.Ef(errors.CodePluginNotFound, "插件版本 %s 不存在", versionID)
		}
		return errors.Wrap(err, errors.CodeInternal, "查询插件版本失败")
	}
	if v.Status == VersionPublished {
		return nil // 幂等
	}
	next, ok := publishTransitions[v.Status]
	if !ok {
		return errors.Ef(errors.CodePluginVersionConflict,
			"版本 %s 处于状态 %s，不接受发布推进（逆向/终态拒绝）", versionID, v.Status)
	}
	var publishedAt *time.Time
	if next == VersionPublished {
		now := time.Now().UTC()
		publishedAt = &now
	}
	if err := s.repo.UpdatePluginVersionStatus(ctx, tenantID, versionID, next, publishedAt); err != nil {
		if notFound(err) {
			return errors.Ef(errors.CodePluginNotFound, "插件版本 %s 不存在", versionID)
		}
		if err == errRepoInvalidState {
			return errors.Ef(errors.CodePluginVersionConflict, "版本 %s 状态跃迁被拒绝", versionID)
		}
		return errors.Wrap(err, errors.CodeInternal, "更新版本状态失败")
	}
	return nil
}

// Install 安装/升级插件。状态机：新行 UPGRADING → 兼容校验 →
// 同一事务内旧行 ROLLED_BACK + 新行 ACTIVE；校验失败整体回滚，
// 旧行保持 ACTIVE（升级失败不影响现网）。升级历史全部保留。
func (s *PluginService) Install(ctx context.Context, tenantID, pluginID, versionID ids.ID, installedBy ids.ID) (ids.ID, error) {
	ctxTenant, err := tenantFromContext(ctx)
	if err != nil {
		return ids.ID{}, err
	}
	if ctxTenant != tenantID {
		return ids.ID{}, errors.E(errors.CodePermissionDenied, "安装目标租户与上下文租户不一致")
	}
	if installedBy.IsZero() {
		return ids.ID{}, errors.E(errors.CodeValidationFailed, "installed_by 不能为空")
	}

	// 预校验（快速失败 + 精确错误码；原子保证在 InstallVersion 内复核）。
	v, err := s.repo.SelectPluginVersionByID(ctx, tenantID, versionID)
	if err != nil {
		if notFound(err) {
			return ids.ID{}, errors.Ef(errors.CodePluginNotFound, "插件版本 %s 不存在", versionID)
		}
		return ids.ID{}, errors.Wrap(err, errors.CodeInternal, "查询插件版本失败")
	}
	if v.PluginID != pluginID {
		return ids.ID{}, errors.E(errors.CodePluginVersionConflict, "插件版本与插件不匹配")
	}
	if v.Status != VersionPublished {
		return ids.ID{}, errors.Ef(errors.CodePluginVersionConflict,
			"插件版本状态 %s，仅 PUBLISHED 可安装", v.Status)
	}
	plugin, err := s.repo.SelectPlugin(ctx, tenantID, pluginID)
	if err != nil {
		if notFound(err) {
			return ids.ID{}, errors.Ef(errors.CodePluginNotFound, "插件 %s 不存在", pluginID)
		}
		return ids.ID{}, errors.Wrap(err, errors.CodeInternal, "查询插件失败")
	}
	if plugin.Status != PluginStatusActive {
		return ids.ID{}, errors.Ef(errors.CodeConflict, "插件状态 %s，拒绝安装", plugin.Status)
	}

	inst := Installation{
		ID:                ids.New(),
		TenantID:          tenantID,
		PluginID:          pluginID,
		PluginVersionID:   versionID,
		InstallationState: InstallUpgrading,
		ConfigJSON:        map[string]any{},
		InstalledBy:       installedBy,
		InstalledAt:       time.Now().UTC(),
		UpdatedAt:         time.Now().UTC(),
	}
	installID, err := s.repo.InstallVersion(ctx, inst)
	if err != nil {
		if notFound(err) {
			return ids.ID{}, errors.E(errors.CodePluginNotFound, "插件或插件版本不存在")
		}
		if err == errRepoInvalidState {
			return ids.ID{}, errors.E(errors.CodePluginVersionConflict, "兼容校验失败：版本不可安装")
		}
		if conflict(err) {
			return ids.ID{}, errors.E(errors.CodeConflict, "该版本已安装过，请改用回滚")
		}
		return ids.ID{}, errors.Wrap(err, errors.CodeInternal, "安装插件失败")
	}
	return installID, nil
}

// Rollback 回滚到指定历史安装（仅状态迁移，不产生新行）：
// 目标行 ROLLED_BACK → ACTIVE，当前 ACTIVE 行 → ROLLED_BACK。
func (s *PluginService) Rollback(ctx context.Context, tenantID, pluginID, targetInstallID ids.ID) error {
	ctxTenant, err := tenantFromContext(ctx)
	if err != nil {
		return err
	}
	if ctxTenant != tenantID {
		return errors.E(errors.CodePermissionDenied, "回滚目标租户与上下文租户不一致")
	}
	target, err := s.repo.SelectInstallation(ctx, tenantID, targetInstallID)
	if err != nil {
		if notFound(err) {
			return errors.Ef(errors.CodePluginNotFound, "安装记录 %s 不存在", targetInstallID)
		}
		return errors.Wrap(err, errors.CodeInternal, "查询安装记录失败")
	}
	if target.PluginID != pluginID {
		return errors.E(errors.CodePluginNotFound, "安装记录不属于该插件")
	}
	switch target.InstallationState {
	case InstallActive:
		return nil // 已是当前激活版本，幂等
	case InstallRolledBack:
		// 继续执行切换。
	default:
		return errors.Ef(errors.CodeConflict, "安装记录状态 %s，不能作为回滚目标", target.InstallationState)
	}

	if err := s.repo.SwitchActiveInstallation(ctx, tenantID, pluginID, targetInstallID, time.Now().UTC()); err != nil {
		if notFound(err) {
			return errors.E(errors.CodePluginNotFound, "安装记录不存在")
		}
		if err == errRepoInvalidState {
			return errors.E(errors.CodeConflict, "回滚目标状态已变化，请刷新后重试")
		}
		if conflict(err) {
			return errors.E(errors.CodeConflict, "回滚会导致双激活安装，已拒绝")
		}
		return errors.Wrap(err, errors.CodeInternal, "回滚安装失败")
	}
	return nil
}

// Disable 停用插件的当前激活安装（ACTIVE → DISABLED）。
func (s *PluginService) Disable(ctx context.Context, tenantID, pluginID ids.ID) error {
	ctxTenant, err := tenantFromContext(ctx)
	if err != nil {
		return err
	}
	if ctxTenant != tenantID {
		return errors.E(errors.CodePermissionDenied, "操作租户与上下文租户不一致")
	}
	active, err := s.repo.SelectActiveInstallation(ctx, tenantID, pluginID)
	if err != nil {
		if notFound(err) {
			return errors.E(errors.CodeConflict, "没有激活安装可停用")
		}
		return errors.Wrap(err, errors.CodeInternal, "查询激活安装失败")
	}
	if err := s.repo.UpdateInstallationState(ctx, tenantID, active.ID, InstallDisabled, time.Now().UTC()); err != nil {
		if notFound(err) {
			return errors.E(errors.CodePluginNotFound, "安装记录不存在")
		}
		if conflict(err) {
			return errors.E(errors.CodeConflict, "停用会导致安装状态冲突")
		}
		return errors.Wrap(err, errors.CodeInternal, "停用插件失败")
	}
	return nil
}

// Enable 恢复被停用的安装（DISABLED → ACTIVE）。
// 已存在激活安装时拒绝（部分唯一索引同语义：双 ACTIVE 冲突）。
func (s *PluginService) Enable(ctx context.Context, tenantID, pluginID ids.ID) error {
	ctxTenant, err := tenantFromContext(ctx)
	if err != nil {
		return err
	}
	if ctxTenant != tenantID {
		return errors.E(errors.CodePermissionDenied, "操作租户与上下文租户不一致")
	}
	installations, err := s.repo.SelectInstallations(ctx, tenantID, pluginID)
	if err != nil {
		return errors.Wrap(err, errors.CodeInternal, "查询安装记录失败")
	}
	var target *Installation
	for i := range installations {
		switch installations[i].InstallationState {
		case InstallActive:
			return errors.E(errors.CodeConflict, "已存在激活安装（双 ACTIVE 冲突），无需恢复")
		case InstallDisabled:
			target = &installations[i]
		}
	}
	if target == nil {
		return errors.E(errors.CodeConflict, "没有已停用的安装可恢复")
	}
	if err := s.repo.UpdateInstallationState(ctx, tenantID, target.ID, InstallActive, time.Now().UTC()); err != nil {
		if notFound(err) {
			return errors.E(errors.CodePluginNotFound, "安装记录不存在")
		}
		if conflict(err) {
			return errors.E(errors.CodeConflict, "恢复会导致双激活安装，已拒绝")
		}
		return errors.Wrap(err, errors.CodeInternal, "恢复插件失败")
	}
	return nil
}

// tenantFromContext / subjectFromContext 从可信上下文解析身份。
func tenantFromContext(ctx context.Context) (ids.ID, error) {
	id, ok := tenantctx.FromContext(ctx)
	if !ok || id.TenantID.IsZero() {
		return ids.ID{}, errors.E(errors.CodeUnauthenticated, "缺少可信租户上下文")
	}
	return id.TenantID, nil
}

func subjectFromContext(ctx context.Context) (ids.ID, error) {
	id, ok := tenantctx.FromContext(ctx)
	if !ok || id.SubjectID.IsZero() {
		return ids.ID{}, errors.E(errors.CodeUnauthenticated, "缺少可信主体上下文")
	}
	return id.SubjectID, nil
}

// notFound / conflict 判断存储层哨兵。
func notFound(err error) bool { return err == errRepoNotFound }

func conflict(err error) bool { return err == errRepoConflict }

// pgPluginRepository 是 PluginRepository 的 PostgreSQL 实现。
// registry.plugins / plugin_versions / plugin_installations 均启用 RLS，
// 全部走事务 + SET LOCAL 租户上下文（平台级 tenant_id NULL 行对租户可见）。
type pgPluginRepository struct {
	pool *pgxpool.Pool
}

func (r pgPluginRepository) InsertPlugin(ctx context.Context, p Plugin) error {
	return writeInTenantTx(ctx, r.pool, p.TenantID, func(tx pgx.Tx) error {
		var ownerOrg any
		if !p.OwnerOrgID.IsZero() {
			ownerOrg = p.OwnerOrgID.String()
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO registry.plugins (id, tenant_id, plugin_key, plugin_kind, name, description, owner_org_id, status, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			p.ID.String(), p.TenantID.String(), p.PluginKey, p.PluginKind, p.Name, p.Description,
			ownerOrg, p.Status, p.CreatedAt)
		if err != nil && isUniqueViolation(err) {
			return errRepoConflict
		}
		return err
	})
}

func (r pgPluginRepository) SelectPluginByKey(ctx context.Context, tenantID ids.ID, key string) (Plugin, error) {
	var p Plugin
	err := readInTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`SELECT id, tenant_id, plugin_key, plugin_kind, name, description, owner_org_id, status, created_at
			 FROM registry.plugins WHERE tenant_id = $1 AND plugin_key = $2`,
			tenantID.String(), key)
		var scanErr error
		p, scanErr = scanPlugin(row)
		return scanErr
	})
	return p, err
}

func (r pgPluginRepository) SelectPlugin(ctx context.Context, tenantID, pluginID ids.ID) (Plugin, error) {
	var p Plugin
	err := readInTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`SELECT id, tenant_id, plugin_key, plugin_kind, name, description, owner_org_id, status, created_at
			 FROM registry.plugins WHERE tenant_id = $1 AND id = $2`,
			tenantID.String(), pluginID.String())
		var scanErr error
		p, scanErr = scanPlugin(row)
		return scanErr
	})
	return p, err
}

func (r pgPluginRepository) InsertPluginVersion(ctx context.Context, v PluginVersion) error {
	return writeInTenantTx(ctx, r.pool, v.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO registry.plugin_versions
			 (id, tenant_id, plugin_id, version, manifest, artifact_uri, artifact_digest, signature_ref, status, created_by, created_at, published_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			v.ID.String(), v.TenantID.String(), v.PluginID.String(), v.Version, v.Manifest,
			v.ArtifactURI, v.ArtifactDigest, v.SignatureRef, v.Status, v.CreatedBy.String(),
			v.CreatedAt, v.PublishedAt)
		if err != nil && isUniqueViolation(err) {
			return errRepoConflict
		}
		return err
	})
}

func (r pgPluginRepository) SelectPluginVersion(ctx context.Context, tenantID, pluginID ids.ID, version string) (PluginVersion, error) {
	var v PluginVersion
	err := readInTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`SELECT id, tenant_id, plugin_id, version, manifest, artifact_uri, artifact_digest, signature_ref, status, created_by, created_at, published_at
			 FROM registry.plugin_versions WHERE tenant_id = $1 AND plugin_id = $2 AND version = $3`,
			tenantID.String(), pluginID.String(), version)
		var scanErr error
		v, scanErr = scanPluginVersion(row)
		return scanErr
	})
	return v, err
}

func (r pgPluginRepository) SelectPluginVersionByID(ctx context.Context, tenantID, versionID ids.ID) (PluginVersion, error) {
	var v PluginVersion
	err := readInTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`SELECT id, tenant_id, plugin_id, version, manifest, artifact_uri, artifact_digest, signature_ref, status, created_by, created_at, published_at
			 FROM registry.plugin_versions WHERE tenant_id = $1 AND id = $2`,
			tenantID.String(), versionID.String())
		var scanErr error
		v, scanErr = scanPluginVersion(row)
		return scanErr
	})
	return v, err
}

func (r pgPluginRepository) UpdatePluginVersionStatus(ctx context.Context, tenantID, versionID ids.ID, status string, publishedAt *time.Time) error {
	return writeInTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE registry.plugin_versions SET status = $3, published_at = $4
			 WHERE tenant_id = $1 AND id = $2`,
			tenantID.String(), versionID.String(), status, publishedAt)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errRepoNotFound
		}
		return nil
	})
}

func (r pgPluginRepository) SelectActiveInstallation(ctx context.Context, tenantID, pluginID ids.ID) (Installation, error) {
	var inst Installation
	err := readInTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`SELECT id, tenant_id, plugin_id, plugin_version_id, installation_state, config_json, installed_by, installed_at, updated_at
			 FROM registry.plugin_installations
			 WHERE tenant_id = $1 AND plugin_id = $2 AND installation_state = 'ACTIVE'`,
			tenantID.String(), pluginID.String())
		var scanErr error
		inst, scanErr = scanInstallation(row)
		return scanErr
	})
	return inst, err
}

func (r pgPluginRepository) SelectInstallation(ctx context.Context, tenantID, installID ids.ID) (Installation, error) {
	var inst Installation
	err := readInTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`SELECT id, tenant_id, plugin_id, plugin_version_id, installation_state, config_json, installed_by, installed_at, updated_at
			 FROM registry.plugin_installations WHERE tenant_id = $1 AND id = $2`,
			tenantID.String(), installID.String())
		var scanErr error
		inst, scanErr = scanInstallation(row)
		return scanErr
	})
	return inst, err
}

func (r pgPluginRepository) SelectInstallations(ctx context.Context, tenantID, pluginID ids.ID) ([]Installation, error) {
	var out []Installation
	err := readInTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		rows, qErr := tx.Query(ctx,
			`SELECT id, tenant_id, plugin_id, plugin_version_id, installation_state, config_json, installed_by, installed_at, updated_at
			 FROM registry.plugin_installations
			 WHERE tenant_id = $1 AND plugin_id = $2
			 ORDER BY installed_at, id`,
			tenantID.String(), pluginID.String())
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			inst, scanErr := scanInstallation(rows)
			if scanErr != nil {
				return scanErr
			}
			out = append(out, inst)
		}
		return rows.Err()
	})
	return out, err
}

func (r pgPluginRepository) InsertInstallation(ctx context.Context, inst Installation) error {
	return writeInTenantTx(ctx, r.pool, inst.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO registry.plugin_installations
			 (id, tenant_id, plugin_id, plugin_version_id, installation_state, config_json, installed_by, installed_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			inst.ID.String(), inst.TenantID.String(), inst.PluginID.String(), inst.PluginVersionID.String(),
			inst.InstallationState, jsonbOf(inst.ConfigJSON), inst.InstalledBy.String(), inst.InstalledAt, inst.UpdatedAt)
		if err != nil && isUniqueViolation(err) {
			return errRepoConflict
		}
		return err
	})
}

func (r pgPluginRepository) UpdateInstallationState(ctx context.Context, tenantID, installID ids.ID, state string, at time.Time) error {
	return writeInTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE registry.plugin_installations SET installation_state = $3, updated_at = $4
			 WHERE tenant_id = $1 AND id = $2`,
			tenantID.String(), installID.String(), state, at)
		if err != nil {
			if isUniqueViolation(err) {
				return errRepoConflict // 双 ACTIVE 冲突（部分唯一索引）
			}
			return err
		}
		if tag.RowsAffected() == 0 {
			return errRepoNotFound
		}
		return nil
	})
}

func (r pgPluginRepository) InstallVersion(ctx context.Context, inst Installation) (ids.ID, error) {
	var installID ids.ID
	err := writeInTenantTx(ctx, r.pool, inst.TenantID, func(tx pgx.Tx) error {
		// 1. 版本必须存在且 PUBLISHED，且属于目标插件。
		var vPlugin, vStatus string
		if scanErr := tx.QueryRow(ctx,
			`SELECT plugin_id, status FROM registry.plugin_versions WHERE tenant_id = $1 AND id = $2`,
			inst.TenantID.String(), inst.PluginVersionID.String()).Scan(&vPlugin, &vStatus); scanErr != nil {
			if scanErr == pgx.ErrNoRows {
				return errRepoNotFound
			}
			return scanErr
		}
		versionPluginID, parseErr := ids.Parse(vPlugin)
		if parseErr != nil {
			return parseErr
		}
		if versionPluginID != inst.PluginID || vStatus != VersionPublished {
			return errRepoInvalidState
		}

		// 2. 插件必须存在且 ACTIVE。
		var pStatus string
		if scanErr := tx.QueryRow(ctx,
			`SELECT status FROM registry.plugins WHERE tenant_id = $1 AND id = $2`,
			inst.TenantID.String(), inst.PluginID.String()).Scan(&pStatus); scanErr != nil {
			if scanErr == pgx.ErrNoRows {
				return errRepoNotFound
			}
			return scanErr
		}
		if pStatus != PluginStatusActive {
			return errRepoInvalidState
		}

		// 3. 同 (tenant, plugin, version) 已有行：已激活则幂等，否则冲突。
		var existingState, existingID string
		row := tx.QueryRow(ctx,
			`SELECT id, installation_state FROM registry.plugin_installations
			 WHERE tenant_id = $1 AND plugin_id = $2 AND plugin_version_id = $3`,
			inst.TenantID.String(), inst.PluginID.String(), inst.PluginVersionID.String())
		switch scanErr := row.Scan(&existingID, &existingState); {
		case scanErr == nil:
			if existingState == InstallActive {
				installID, _ = ids.Parse(existingID)
				return nil // 幂等：无需变更
			}
			return errRepoConflict
		case scanErr == pgx.ErrNoRows:
			// 继续安装。
		default:
			return scanErr
		}

		// 4. 新行 UPGRADING。
		if _, err := tx.Exec(ctx,
			`INSERT INTO registry.plugin_installations
			 (id, tenant_id, plugin_id, plugin_version_id, installation_state, config_json, installed_by, installed_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			inst.ID.String(), inst.TenantID.String(), inst.PluginID.String(), inst.PluginVersionID.String(),
			InstallUpgrading, jsonbOf(inst.ConfigJSON), inst.InstalledBy.String(), inst.InstalledAt, inst.UpdatedAt); err != nil {
			if isUniqueViolation(err) {
				return errRepoConflict
			}
			return err
		}

		// 5. 旧激活行 ROLLED_BACK（先撤旧再激活，避开部分唯一索引）。
		if _, err := tx.Exec(ctx,
			`UPDATE registry.plugin_installations SET installation_state = 'ROLLED_BACK', updated_at = $3
			 WHERE tenant_id = $1 AND plugin_id = $2 AND installation_state = 'ACTIVE'`,
			inst.TenantID.String(), inst.PluginID.String(), inst.UpdatedAt); err != nil {
			return err
		}

		// 6. 新行 ACTIVE。
		if _, err := tx.Exec(ctx,
			`UPDATE registry.plugin_installations SET installation_state = 'ACTIVE', updated_at = $3
			 WHERE tenant_id = $1 AND id = $2`,
			inst.TenantID.String(), inst.ID.String(), inst.UpdatedAt); err != nil {
			return err
		}
		installID = inst.ID
		return nil
	})
	return installID, err
}

func (r pgPluginRepository) SwitchActiveInstallation(ctx context.Context, tenantID, pluginID, targetInstallID ids.ID, at time.Time) error {
	return writeInTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		// 目标行必须存在且为 ROLLED_BACK。
		var state string
		if err := tx.QueryRow(ctx,
			`SELECT installation_state FROM registry.plugin_installations
			 WHERE tenant_id = $1 AND id = $2 AND plugin_id = $3`,
			tenantID.String(), targetInstallID.String(), pluginID.String()).Scan(&state); err != nil {
			if err == pgx.ErrNoRows {
				return errRepoNotFound
			}
			return err
		}
		if state != InstallRolledBack {
			return errRepoInvalidState
		}
		// 原 ACTIVE 行 → ROLLED_BACK。
		if _, err := tx.Exec(ctx,
			`UPDATE registry.plugin_installations SET installation_state = 'ROLLED_BACK', updated_at = $4
			 WHERE tenant_id = $1 AND plugin_id = $2 AND installation_state = 'ACTIVE' AND id <> $3`,
			tenantID.String(), pluginID.String(), targetInstallID.String(), at); err != nil {
			return err
		}
		// 目标行 → ACTIVE。
		//
		// 占位符修正：`updated_at = $4` 是上一语句（4 个实参）复制过来的笔误。
		// 本语句只绑定 3 个实参（tenantID / targetInstallID / at），PostgreSQL
		// 会拒绝准备语句（"bind message supplies 3 parameters, but prepared
		// statement requires 4"），整条切换激活的回滚路径必然 500。
		tag, err := tx.Exec(ctx,
			`UPDATE registry.plugin_installations SET installation_state = 'ACTIVE', updated_at = $3
			 WHERE tenant_id = $1 AND id = $2 AND installation_state = 'ROLLED_BACK'`,
			tenantID.String(), targetInstallID.String(), at)
		if err != nil {
			if isUniqueViolation(err) {
				return errRepoConflict
			}
			return err
		}
		if tag.RowsAffected() == 0 {
			return errRepoInvalidState
		}
		return nil
	})
}

// scanPlugin / scanPluginVersion / scanInstallation 负责行到领域对象的映射。
func scanPlugin(row pgx.Row) (Plugin, error) {
	var (
		p         Plugin
		idText    string
		tenantTxt *string
		ownerTxt  *string
	)
	err := row.Scan(&idText, &tenantTxt, &p.PluginKey, &p.PluginKind, &p.Name, &p.Description,
		&ownerTxt, &p.Status, &p.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return Plugin{}, errRepoNotFound
		}
		return Plugin{}, err
	}
	if p.ID, err = ids.Parse(idText); err != nil {
		return Plugin{}, err
	}
	if tenantTxt != nil {
		if p.TenantID, err = ids.Parse(*tenantTxt); err != nil {
			return Plugin{}, err
		}
	}
	if ownerTxt != nil {
		if p.OwnerOrgID, err = ids.Parse(*ownerTxt); err != nil {
			return Plugin{}, err
		}
	}
	return p, nil
}

func scanPluginVersion(row pgx.Row) (PluginVersion, error) {
	var (
		v         PluginVersion
		idText    string
		tenantTxt *string
		pluginTxt string
		createdBy string
		published *time.Time
	)
	err := row.Scan(&idText, &tenantTxt, &pluginTxt, &v.Version, &v.Manifest, &v.ArtifactURI,
		&v.ArtifactDigest, &v.SignatureRef, &v.Status, &createdBy, &v.CreatedAt, &published)
	if err != nil {
		if err == pgx.ErrNoRows {
			return PluginVersion{}, errRepoNotFound
		}
		return PluginVersion{}, err
	}
	if v.ID, err = ids.Parse(idText); err != nil {
		return PluginVersion{}, err
	}
	if tenantTxt != nil {
		if v.TenantID, err = ids.Parse(*tenantTxt); err != nil {
			return PluginVersion{}, err
		}
	}
	if v.PluginID, err = ids.Parse(pluginTxt); err != nil {
		return PluginVersion{}, err
	}
	if v.CreatedBy, err = ids.Parse(createdBy); err != nil {
		return PluginVersion{}, err
	}
	v.PublishedAt = published
	return v, nil
}

func scanInstallation(row pgx.Row) (Installation, error) {
	var (
		inst      Installation
		idText    string
		tenantTxt string
		pluginTxt string
		versionTx string
		byText    string
		config    []byte
	)
	err := row.Scan(&idText, &tenantTxt, &pluginTxt, &versionTx, &inst.InstallationState, &config,
		&byText, &inst.InstalledAt, &inst.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return Installation{}, errRepoNotFound
		}
		return Installation{}, err
	}
	if inst.ID, err = ids.Parse(idText); err != nil {
		return Installation{}, err
	}
	if inst.TenantID, err = ids.Parse(tenantTxt); err != nil {
		return Installation{}, err
	}
	if inst.PluginID, err = ids.Parse(pluginTxt); err != nil {
		return Installation{}, err
	}
	if inst.PluginVersionID, err = ids.Parse(versionTx); err != nil {
		return Installation{}, err
	}
	if inst.InstalledBy, err = ids.Parse(byText); err != nil {
		return Installation{}, err
	}
	inst.ConfigJSON = jsonbMap(config)
	return inst, nil
}

// readInTenantTx / writeInTenantTx 在租户事务（SET LOCAL）中执行查询（RLS 安全路径）。
func readInTenantTx(ctx context.Context, pool *pgxpool.Pool, tenantID ids.ID, fn func(pgx.Tx) error) error {
	return writeInTenantTx(ctx, pool, tenantID, fn)
}

func writeInTenantTx(ctx context.Context, pool *pgxpool.Pool, tenantID ids.ID, fn func(pgx.Tx) error) error {
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

// jsonbOf / jsonbMap 处理 JSONB 参数与结果。
func jsonbOf(m map[string]any) []byte {
	if len(m) == 0 {
		return []byte("{}")
	}
	b, err := json.Marshal(m)
	if err != nil {
		return []byte("{}")
	}
	return b
}

func jsonbMap(b []byte) map[string]any {
	m := map[string]any{}
	if len(b) == 0 {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

// isUniqueViolation 判断 PostgreSQL 唯一约束冲突（SQLSTATE 23505）。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if stderrors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}
