package registry

import (
	"context"
	"testing"
	"time"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/tenantctx"
)

// fakeRegistryRepo 是 PluginRepository 的内存实现（测试专用，不连接数据库）。
// 与 PG 实现对齐：幂等键、单 ACTIVE 不变量、安装事务原子性全部在内存复现。
type fakeRegistryRepo struct {
	plugins       []Plugin
	versions      []PluginVersion
	installations []Installation
}

func (f *fakeRegistryRepo) findPlugin(tenantID, pluginID ids.ID) (*Plugin, bool) {
	for i := range f.plugins {
		if f.plugins[i].TenantID == tenantID && f.plugins[i].ID == pluginID {
			return &f.plugins[i], true
		}
	}
	return nil, false
}

func (f *fakeRegistryRepo) findVersionByID(tenantID, versionID ids.ID) (*PluginVersion, bool) {
	for i := range f.versions {
		if f.versions[i].TenantID == tenantID && f.versions[i].ID == versionID {
			return &f.versions[i], true
		}
	}
	return nil, false
}

func (f *fakeRegistryRepo) findInstallation(tenantID, installID ids.ID) (*Installation, bool) {
	for i := range f.installations {
		if f.installations[i].TenantID == tenantID && f.installations[i].ID == installID {
			return &f.installations[i], true
		}
	}
	return nil, false
}

func (f *fakeRegistryRepo) InsertPlugin(_ context.Context, p Plugin) error {
	for _, existing := range f.plugins {
		if existing.TenantID == p.TenantID && existing.PluginKey == p.PluginKey {
			return errRepoConflict
		}
	}
	f.plugins = append(f.plugins, p)
	return nil
}

func (f *fakeRegistryRepo) SelectPluginByKey(_ context.Context, tenantID ids.ID, key string) (Plugin, error) {
	for _, p := range f.plugins {
		if p.TenantID == tenantID && p.PluginKey == key {
			return p, nil
		}
	}
	return Plugin{}, errRepoNotFound
}

func (f *fakeRegistryRepo) SelectPlugin(_ context.Context, tenantID, pluginID ids.ID) (Plugin, error) {
	if p, ok := f.findPlugin(tenantID, pluginID); ok {
		return *p, nil
	}
	return Plugin{}, errRepoNotFound
}

func (f *fakeRegistryRepo) InsertPluginVersion(_ context.Context, v PluginVersion) error {
	for _, existing := range f.versions {
		if existing.TenantID == v.TenantID && existing.PluginID == v.PluginID && existing.Version == v.Version {
			return errRepoConflict
		}
	}
	f.versions = append(f.versions, v)
	return nil
}

func (f *fakeRegistryRepo) SelectPluginVersion(_ context.Context, tenantID, pluginID ids.ID, version string) (PluginVersion, error) {
	for _, v := range f.versions {
		if v.TenantID == tenantID && v.PluginID == pluginID && v.Version == version {
			return v, nil
		}
	}
	return PluginVersion{}, errRepoNotFound
}

func (f *fakeRegistryRepo) SelectPluginVersionByID(_ context.Context, tenantID, versionID ids.ID) (PluginVersion, error) {
	if v, ok := f.findVersionByID(tenantID, versionID); ok {
		return *v, nil
	}
	return PluginVersion{}, errRepoNotFound
}

func (f *fakeRegistryRepo) UpdatePluginVersionStatus(_ context.Context, tenantID, versionID ids.ID, status string, publishedAt *time.Time) error {
	v, ok := f.findVersionByID(tenantID, versionID)
	if !ok {
		return errRepoNotFound
	}
	v.Status = status
	v.PublishedAt = publishedAt
	return nil
}

func (f *fakeRegistryRepo) SelectActiveInstallation(_ context.Context, tenantID, pluginID ids.ID) (Installation, error) {
	for _, inst := range f.installations {
		if inst.TenantID == tenantID && inst.PluginID == pluginID && inst.InstallationState == InstallActive {
			return inst, nil
		}
	}
	return Installation{}, errRepoNotFound
}

func (f *fakeRegistryRepo) SelectInstallation(_ context.Context, tenantID, installID ids.ID) (Installation, error) {
	if inst, ok := f.findInstallation(tenantID, installID); ok {
		return *inst, nil
	}
	return Installation{}, errRepoNotFound
}

func (f *fakeRegistryRepo) SelectInstallations(_ context.Context, tenantID, pluginID ids.ID) ([]Installation, error) {
	var out []Installation
	for _, inst := range f.installations {
		if inst.TenantID == tenantID && inst.PluginID == pluginID {
			out = append(out, inst)
		}
	}
	return out, nil
}

func (f *fakeRegistryRepo) InsertInstallation(_ context.Context, inst Installation) error {
	for _, existing := range f.installations {
		if existing.TenantID == inst.TenantID && existing.PluginID == inst.PluginID &&
			existing.PluginVersionID == inst.PluginVersionID {
			return errRepoConflict
		}
	}
	if inst.InstallationState == InstallActive {
		if err := f.assertSingleActive(inst.TenantID, inst.PluginID, inst.ID); err != nil {
			return err
		}
	}
	f.installations = append(f.installations, inst)
	return nil
}

func (f *fakeRegistryRepo) UpdateInstallationState(_ context.Context, tenantID, installID ids.ID, state string, at time.Time) error {
	inst, ok := f.findInstallation(tenantID, installID)
	if !ok {
		return errRepoNotFound
	}
	if state == InstallActive {
		if err := f.assertSingleActive(tenantID, inst.PluginID, installID); err != nil {
			return err
		}
	}
	inst.InstallationState = state
	inst.UpdatedAt = at
	return nil
}

// assertSingleActive 复现部分唯一索引：同 (tenant, plugin) 不得出现两个 ACTIVE。
func (f *fakeRegistryRepo) assertSingleActive(tenantID, pluginID, exceptID ids.ID) error {
	for _, inst := range f.installations {
		if inst.TenantID == tenantID && inst.PluginID == pluginID &&
			inst.InstallationState == InstallActive && inst.ID != exceptID {
			return errRepoConflict
		}
	}
	return nil
}

func (f *fakeRegistryRepo) InstallVersion(_ context.Context, inst Installation) (ids.ID, error) {
	v, ok := f.findVersionByID(inst.TenantID, inst.PluginVersionID)
	if !ok {
		return ids.ID{}, errRepoNotFound
	}
	if v.PluginID != inst.PluginID || v.Status != VersionPublished {
		return ids.ID{}, errRepoInvalidState
	}
	p, ok := f.findPlugin(inst.TenantID, inst.PluginID)
	if !ok {
		return ids.ID{}, errRepoNotFound
	}
	if p.Status != PluginStatusActive {
		return ids.ID{}, errRepoInvalidState
	}
	// 同 (tenant, plugin, version) 已有行：已激活幂等，否则冲突。
	for _, existing := range f.installations {
		if existing.TenantID == inst.TenantID && existing.PluginID == inst.PluginID &&
			existing.PluginVersionID == inst.PluginVersionID {
			if existing.InstallationState == InstallActive {
				return existing.ID, nil
			}
			return ids.ID{}, errRepoConflict
		}
	}
	// 新行 UPGRADING → 旧行 ROLLED_BACK → 新行 ACTIVE（任一校验失败即不回写）。
	newInst := inst
	newInst.InstallationState = InstallUpgrading
	f.installations = append(f.installations, newInst)
	for i := range f.installations {
		cur := &f.installations[i]
		if cur.TenantID == inst.TenantID && cur.PluginID == inst.PluginID &&
			cur.InstallationState == InstallActive && cur.ID != newInst.ID {
			cur.InstallationState = InstallRolledBack
			cur.UpdatedAt = inst.UpdatedAt
		}
	}
	f.installations[len(f.installations)-1].InstallationState = InstallActive
	return newInst.ID, nil
}

func (f *fakeRegistryRepo) SwitchActiveInstallation(_ context.Context, tenantID, pluginID, targetInstallID ids.ID, at time.Time) error {
	target, ok := f.findInstallation(tenantID, targetInstallID)
	if !ok || target.PluginID != pluginID {
		return errRepoNotFound
	}
	if target.InstallationState != InstallRolledBack {
		return errRepoInvalidState
	}
	for i := range f.installations {
		cur := &f.installations[i]
		if cur.TenantID == tenantID && cur.PluginID == pluginID &&
			cur.InstallationState == InstallActive && cur.ID != targetInstallID {
			cur.InstallationState = InstallRolledBack
			cur.UpdatedAt = at
		}
	}
	target.InstallationState = InstallActive
	target.UpdatedAt = at
	return nil
}

// registryTestEnv 组装测试环境。
type registryTestEnv struct {
	svc     *PluginService
	repo    *fakeRegistryRepo
	ctx     context.Context
	tenant  ids.ID
	subject ids.ID
}

func newRegistryTestEnv() *registryTestEnv {
	repo := &fakeRegistryRepo{}
	tenant := ids.New()
	subject := ids.New()
	ctx := tenantctx.WithIdentity(context.Background(), tenantctx.Identity{
		TenantID:  tenant,
		SubjectID: subject,
		RequestID: "test",
	})
	return &registryTestEnv{svc: &PluginService{repo: repo}, repo: repo, ctx: ctx, tenant: tenant, subject: subject}
}

// mustPublish 把版本一路推进到 PUBLISHED。
func mustPublish(t *testing.T, env *registryTestEnv, versionID ids.ID) {
	t.Helper()
	for i := 0; i < 5; i++ {
		if err := env.svc.Publish(env.ctx, versionID); err != nil {
			t.Fatalf("Publish step %d: %v", i, err)
		}
	}
}

// seedPlugin 注册测试插件。
func seedPlugin(t *testing.T, env *registryTestEnv, key string) ids.ID {
	t.Helper()
	pluginID, err := env.svc.RegisterPlugin(env.ctx, key, KindAgent, "Plugin "+key)
	if err != nil {
		t.Fatalf("RegisterPlugin(%s): %v", key, err)
	}
	return pluginID
}

// seedVersion 在指定插件下创建版本，并直接置位目标状态
// （用于构造非标准路径，如 TESTING/REJECTED）。
func seedVersion(t *testing.T, env *registryTestEnv, pluginID ids.ID, version, status string) ids.ID {
	t.Helper()
	versionID, err := env.svc.CreateVersion(env.ctx, pluginID, version, validManifest, "sha256:"+version)
	if err != nil {
		t.Fatalf("CreateVersion(%s): %v", version, err)
	}
	if status == VersionCreated {
		return versionID
	}
	v, ok := env.repo.findVersionByID(env.tenant, versionID)
	if !ok {
		t.Fatal("seeded version missing")
	}
	v.Status = status
	return versionID
}

func TestPluginRegisterAndDuplicate(t *testing.T) {
	env := newRegistryTestEnv()
	id, err := env.svc.RegisterPlugin(env.ctx, "expense-agent", KindAgent, "Expense")
	if err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}
	if id.IsZero() {
		t.Fatal("RegisterPlugin returned zero id")
	}
	_, err = env.svc.RegisterPlugin(env.ctx, "expense-agent", KindAgent, "Expense Again")
	if errors.As(err).Code != errors.CodeConflict {
		t.Fatalf("duplicate key err = %v, want CONFLICT", err)
	}
	if _, err := env.svc.RegisterPlugin(env.ctx, "bad-kind", "ALIEN", "x"); errors.As(err).Code != errors.CodeValidationFailed {
		t.Fatalf("bad kind err = %v, want VALIDATION_FAILED", err)
	}
}

func TestPluginCreateVersionIdempotentAndConflict(t *testing.T) {
	env := newRegistryTestEnv()
	pluginID, err := env.svc.RegisterPlugin(env.ctx, "p", KindTool, "P")
	if err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}

	first, err := env.svc.CreateVersion(env.ctx, pluginID, "1.0.0", validManifest, "sha256:a")
	if err != nil {
		t.Fatalf("CreateVersion: %v", err)
	}
	// 幂等重放：同版本同摘要 → 同一 ID。
	again, err := env.svc.CreateVersion(env.ctx, pluginID, "1.0.0", validManifest, "sha256:a")
	if err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	if again != first {
		t.Fatalf("idempotent replay id = %s, want %s", again, first)
	}
	// 同版本不同摘要 → 冲突（防版本号复用）。
	_, err = env.svc.CreateVersion(env.ctx, pluginID, "1.0.0", validManifest, "sha256:b")
	if errors.As(err).Code != errors.CodePluginVersionConflict {
		t.Fatalf("digest mismatch err = %v, want PLUGIN_VERSION_CONFLICT", err)
	}
	// 非法清单 → 清单错误码。
	_, err = env.svc.CreateVersion(env.ctx, pluginID, "2.0.0", `{"key":"x"}`, "sha256:c")
	if errors.As(err).Code != errors.CodePluginManifestInvalid {
		t.Fatalf("bad manifest err = %v, want PLUGIN_MANIFEST_INVALID", err)
	}
	// 未知插件。
	_, err = env.svc.CreateVersion(env.ctx, ids.New(), "1.0.0", validManifest, "sha256:d")
	if errors.As(err).Code != errors.CodePluginNotFound {
		t.Fatalf("unknown plugin err = %v, want PLUGIN_NOT_FOUND", err)
	}
}

func TestPluginPublishStateMachine(t *testing.T) {
	env := newRegistryTestEnv()
	pluginID := seedPlugin(t, env, "p")
	v1 := seedVersion(t, env, pluginID, "1.0.0", VersionCreated)

	// 正向跃迁链：CREATED→VALIDATING→TESTING→STAGED→APPROVED→PUBLISHED。
	wantStates := []string{VersionValidating, VersionTesting, VersionStaged, VersionApproved, VersionPublished}
	for i, want := range wantStates {
		if err := env.svc.Publish(env.ctx, v1); err != nil {
			t.Fatalf("Publish step %d: %v", i, err)
		}
		v, _ := env.repo.findVersionByID(env.tenant, v1)
		if v.Status != want {
			t.Fatalf("after publish step %d status = %s, want %s", i, v.Status, want)
		}
	}
	// 已发布：幂等。
	if err := env.svc.Publish(env.ctx, v1); err != nil {
		t.Fatalf("Publish on PUBLISHED should be idempotent: %v", err)
	}
	v, _ := env.repo.findVersionByID(env.tenant, v1)
	if v.PublishedAt == nil {
		t.Fatal("PUBLISHED 必须记录 published_at")
	}

	// 终态/逆向拒绝：REJECTED 与 DEPRECATED 不接受发布推进。
	for _, terminal := range []string{VersionRejected, VersionDeprecated, VersionDisabled} {
		badVersion := seedVersion(t, env, pluginID, "t-"+terminal, terminal)
		if err := env.svc.Publish(env.ctx, badVersion); errors.As(err).Code != errors.CodePluginVersionConflict {
			t.Fatalf("publish from %s err = %v, want PLUGIN_VERSION_CONFLICT", terminal, err)
		}
	}

	// 未知版本。
	if err := env.svc.Publish(env.ctx, ids.New()); errors.As(err).Code != errors.CodePluginNotFound {
		t.Fatalf("publish unknown err = %v, want PLUGIN_NOT_FOUND", err)
	}
}

func TestPluginInstallUpgradeAndRollback(t *testing.T) {
	env := newRegistryTestEnv()

	// 首个版本安装。
	pluginID := seedPlugin(t, env, "p")
	v1 := seedVersion(t, env, pluginID, "1.0.0", VersionCreated)
	mustPublish(t, env, v1)
	inst1, err := env.svc.Install(env.ctx, env.tenant, pluginID, v1, env.subject)
	if err != nil {
		t.Fatalf("Install v1: %v", err)
	}
	if got := env.repo.installations; len(got) != 1 || got[0].InstallationState != InstallActive {
		t.Fatalf("after first install: %+v", got)
	}

	// 幂等重装：相同版本 → 同一安装行，不产生新行。
	again, err := env.svc.Install(env.ctx, env.tenant, pluginID, v1, env.subject)
	if err != nil {
		t.Fatalf("idempotent install: %v", err)
	}
	if again != inst1 {
		t.Fatalf("idempotent install id = %s, want %s", again, inst1)
	}
	if len(env.repo.installations) != 1 {
		t.Fatalf("idempotent install created new row: %d rows", len(env.repo.installations))
	}

	// 升级：v2 发布后安装，v1 → ROLLED_BACK，v2 → ACTIVE，历史保留。
	v2 := seedVersion(t, env, pluginID, "2.0.0", VersionCreated)
	mustPublish(t, env, v2)
	inst2, err := env.svc.Install(env.ctx, env.tenant, pluginID, v2, env.subject)
	if err != nil {
		t.Fatalf("Install v2: %v", err)
	}
	installations, _ := env.repo.SelectInstallations(env.ctx, env.tenant, pluginID)
	if len(installations) != 2 {
		t.Fatalf("upgrade history rows = %d, want 2（升级保留历史）", len(installations))
	}
	states := map[string]string{}
	for _, inst := range installations {
		states[inst.ID.String()] = inst.InstallationState
	}
	if states[inst1.String()] != InstallRolledBack {
		t.Fatalf("v1 installation state = %s, want ROLLED_BACK", states[inst1.String()])
	}
	if states[inst2.String()] != InstallActive {
		t.Fatalf("v2 installation state = %s, want ACTIVE", states[inst2.String()])
	}

	// 回滚到 v1：仅状态迁移，行数不变。
	if err := env.svc.Rollback(env.ctx, env.tenant, pluginID, inst1); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	installations, _ = env.repo.SelectInstallations(env.ctx, env.tenant, pluginID)
	if len(installations) != 2 {
		t.Fatalf("rollback changed row count: %d, want 2（仅状态迁移）", len(installations))
	}
	states = map[string]string{}
	for _, inst := range installations {
		states[inst.ID.String()] = inst.InstallationState
	}
	if states[inst1.String()] != InstallActive || states[inst2.String()] != InstallRolledBack {
		t.Fatalf("after rollback states = %+v", states)
	}
	// 回滚到已激活目标：幂等。
	if err := env.svc.Rollback(env.ctx, env.tenant, pluginID, inst1); err != nil {
		t.Fatalf("idempotent rollback: %v", err)
	}
}

func TestPluginInstallFailureKeepsOldActive(t *testing.T) {
	env := newRegistryTestEnv()
	pluginID := seedPlugin(t, env, "p")
	v1 := seedVersion(t, env, pluginID, "1.0.0", VersionCreated)
	mustPublish(t, env, v1)
	inst1, err := env.svc.Install(env.ctx, env.tenant, pluginID, v1, env.subject)
	if err != nil {
		t.Fatalf("Install v1: %v", err)
	}

	// 目标版本未发布（TESTING）→ 升级失败。
	v2 := seedVersion(t, env, pluginID, "2.0.0", VersionTesting)
	if _, err := env.svc.Install(env.ctx, env.tenant, pluginID, v2, env.subject); errors.As(err).Code != errors.CodePluginVersionConflict {
		t.Fatalf("install unpublished err = %v, want PLUGIN_VERSION_CONFLICT", err)
	}
	// 旧行仍 ACTIVE，且没有残留 UPGRADING 行。
	installations, _ := env.repo.SelectInstallations(env.ctx, env.tenant, pluginID)
	if len(installations) != 1 {
		t.Fatalf("failed upgrade left %d rows, want 1（旧行仍 ACTIVE）", len(installations))
	}
	if installations[0].ID != inst1 || installations[0].InstallationState != InstallActive {
		t.Fatalf("old installation corrupted: %+v", installations[0])
	}

	// 版本与插件不匹配 → 冲突。
	otherPlugin := seedPlugin(t, env, "other")
	v3 := seedVersion(t, env, otherPlugin, "3.0.0", VersionCreated)
	mustPublish(t, env, v3)
	if _, err := env.svc.Install(env.ctx, env.tenant, pluginID, v3, env.subject); errors.As(err).Code != errors.CodePluginVersionConflict {
		t.Fatalf("plugin mismatch err = %v, want PLUGIN_VERSION_CONFLICT", err)
	}
	// 未知版本。
	if _, err := env.svc.Install(env.ctx, env.tenant, pluginID, ids.New(), env.subject); errors.As(err).Code != errors.CodePluginNotFound {
		t.Fatalf("unknown version err = %v, want PLUGIN_NOT_FOUND", err)
	}
}

func TestPluginDisableEnableAndDoubleActive(t *testing.T) {
	env := newRegistryTestEnv()
	pluginID := seedPlugin(t, env, "p")
	v1 := seedVersion(t, env, pluginID, "1.0.0", VersionCreated)
	mustPublish(t, env, v1)
	inst1, err := env.svc.Install(env.ctx, env.tenant, pluginID, v1, env.subject)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	// 停用 → DISABLED。
	if err := env.svc.Disable(env.ctx, env.tenant, pluginID); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	inst, _ := env.repo.SelectInstallation(env.ctx, env.tenant, inst1)
	if inst.InstallationState != InstallDisabled {
		t.Fatalf("state = %s, want DISABLED", inst.InstallationState)
	}
	// 无激活安装时再停用 → 冲突。
	if err := env.svc.Disable(env.ctx, env.tenant, pluginID); errors.As(err).Code != errors.CodeConflict {
		t.Fatalf("disable without active err = %v, want CONFLICT", err)
	}

	// 恢复 → ACTIVE。
	if err := env.svc.Enable(env.ctx, env.tenant, pluginID); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	inst, _ = env.repo.SelectInstallation(env.ctx, env.tenant, inst1)
	if inst.InstallationState != InstallActive {
		t.Fatalf("state = %s, want ACTIVE", inst.InstallationState)
	}

	// 双 ACTIVE 冲突：已激活时再 Enable → 拒绝；仓库层同样拒绝造双。
	if err := env.svc.Enable(env.ctx, env.tenant, pluginID); errors.As(err).Code != errors.CodeConflict {
		t.Fatalf("enable with active err = %v, want CONFLICT（双 ACTIVE 冲突）", err)
	}
	err = env.repo.InsertInstallation(env.ctx, Installation{
		ID: ids.New(), TenantID: env.tenant, PluginID: pluginID,
		PluginVersionID: v1, InstallationState: InstallActive,
		InstalledBy: env.subject,
	})
	if !conflict(err) {
		t.Fatalf("repo insert second ACTIVE err = %v, want conflict", err)
	}

	// 无可恢复安装时 Enable → 冲突。
	emptyPlugin, _ := env.svc.RegisterPlugin(env.ctx, "empty", KindTool, "Empty")
	if err := env.svc.Enable(env.ctx, env.tenant, emptyPlugin); errors.As(err).Code != errors.CodeConflict {
		t.Fatalf("enable nothing err = %v, want CONFLICT", err)
	}
}

func TestPluginInstallCrossTenantRejected(t *testing.T) {
	env := newRegistryTestEnv()
	pluginID := seedPlugin(t, env, "p")
	v1 := seedVersion(t, env, pluginID, "1.0.0", VersionCreated)
	mustPublish(t, env, v1)

	otherTenant := ids.New()
	// 安装目标租户与上下文租户不一致 → 拒绝。
	if _, err := env.svc.Install(env.ctx, otherTenant, pluginID, v1, env.subject); errors.As(err).Code != errors.CodePermissionDenied {
		t.Fatalf("cross tenant install err = %v, want PERMISSION_DENIED", err)
	}
	// 回滚/停用同样校验租户一致性。
	if err := env.svc.Rollback(env.ctx, otherTenant, pluginID, ids.New()); errors.As(err).Code != errors.CodePermissionDenied {
		t.Fatalf("cross tenant rollback err = %v, want PERMISSION_DENIED", err)
	}
	if err := env.svc.Disable(env.ctx, otherTenant, pluginID); errors.As(err).Code != errors.CodePermissionDenied {
		t.Fatalf("cross tenant disable err = %v, want PERMISSION_DENIED", err)
	}
}

func TestPluginMissingTenantContext(t *testing.T) {
	env := newRegistryTestEnv()
	if _, err := env.svc.RegisterPlugin(context.Background(), "k", KindAgent, "n"); errors.As(err).Code != errors.CodeUnauthenticated {
		t.Fatalf("no tenant ctx err = %v, want UNAUTHENTICATED", err)
	}
}

func TestPluginRollbackInvalidTarget(t *testing.T) {
	env := newRegistryTestEnv()
	pluginID := seedPlugin(t, env, "p")
	v1 := seedVersion(t, env, pluginID, "1.0.0", VersionCreated)
	mustPublish(t, env, v1)
	inst1, err := env.svc.Install(env.ctx, env.tenant, pluginID, v1, env.subject)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	// 目标安装不存在。
	if err := env.svc.Rollback(env.ctx, env.tenant, pluginID, ids.New()); errors.As(err).Code != errors.CodePluginNotFound {
		t.Fatalf("missing target err = %v, want PLUGIN_NOT_FOUND", err)
	}
	// 目标属于其他插件。
	otherPlugin, _ := env.svc.RegisterPlugin(env.ctx, "other", KindTool, "Other")
	if err := env.svc.Rollback(env.ctx, env.tenant, otherPlugin, inst1); errors.As(err).Code != errors.CodePluginNotFound {
		t.Fatalf("foreign target err = %v, want PLUGIN_NOT_FOUND", err)
	}
}
