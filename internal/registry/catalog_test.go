package registry

import (
	"context"
	"testing"
	"time"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// fakeCatalogRepo 是 CatalogRepository 的内存实现。
type fakeCatalogRepo struct {
	agents    []AgentRef
	agentVers []AgentVersion
	tools     []ToolRef
	toolVers  []ToolVersion
}

func (f *fakeCatalogRepo) SelectAgentByKey(_ context.Context, tenantID ids.ID, agentKey string) (AgentRef, error) {
	for _, a := range f.agents {
		if a.TenantID == tenantID && a.AgentKey == agentKey {
			return a, nil
		}
	}
	return AgentRef{}, errRepoNotFound
}

func (f *fakeCatalogRepo) SelectAgentVersions(_ context.Context, tenantID, agentID ids.ID) ([]AgentVersion, error) {
	var out []AgentVersion
	for _, v := range f.agentVers {
		if v.TenantID == tenantID && v.AgentID == agentID {
			out = append(out, v)
		}
	}
	return out, nil
}

func (f *fakeCatalogRepo) SelectAgentVersionByID(_ context.Context, tenantID, versionID ids.ID) (AgentVersion, error) {
	for _, v := range f.agentVers {
		if v.TenantID == tenantID && v.ID == versionID {
			return v, nil
		}
	}
	return AgentVersion{}, errRepoNotFound
}

func (f *fakeCatalogRepo) SelectToolByKey(_ context.Context, tenantID ids.ID, toolKey string) (ToolRef, error) {
	for _, t := range f.tools {
		if t.TenantID == tenantID && t.ToolKey == toolKey {
			return t, nil
		}
	}
	return ToolRef{}, errRepoNotFound
}

func (f *fakeCatalogRepo) SelectToolVersion(_ context.Context, tenantID, toolID ids.ID, version string) (ToolVersion, error) {
	for _, v := range f.toolVers {
		if v.TenantID == tenantID && v.ToolID == toolID && v.Version == version {
			return v, nil
		}
	}
	return ToolVersion{}, errRepoNotFound
}

// SelectLatestPublishedTool 与生产 SQL 同语义：PUBLISHED 之内取 created_at 最新。
func (f *fakeCatalogRepo) SelectLatestPublishedTool(_ context.Context, tenantID, toolID ids.ID) (InvocableTool, error) {
	var tool *ToolRef
	for i := range f.tools {
		if f.tools[i].TenantID == tenantID && f.tools[i].ID == toolID {
			tool = &f.tools[i]
			break
		}
	}
	if tool == nil {
		return InvocableTool{}, errRepoNotFound
	}
	var best *ToolVersion
	for i := range f.toolVers {
		v := &f.toolVers[i]
		if v.TenantID != tenantID || v.ToolID != toolID || v.Status != VersionPublished {
			continue
		}
		if best == nil || v.CreatedAt.After(best.CreatedAt) {
			best = v
		}
	}
	if best == nil {
		return InvocableTool{}, errNoPublishedVersion
	}
	return InvocableTool{Tool: *tool, Version: *best}, nil
}

func catalogEnv() (*CatalogService, *fakeCatalogRepo, context.Context, ids.ID) {
	repo := &fakeCatalogRepo{}
	tenant := ids.New()
	ctx := context.Background()
	return &CatalogService{repo: repo}, repo, ctx, tenant
}

func TestResolveAgentVersion(t *testing.T) {
	svc, repo, ctx, tenant := catalogEnv()
	agentID := ids.New()
	repo.agents = append(repo.agents, AgentRef{ID: agentID, TenantID: tenant, AgentKey: "expense", Domain: "finance"})
	repo.agentVers = append(repo.agentVers,
		AgentVersion{ID: ids.New(), TenantID: tenant, AgentID: agentID, Version: "1.0.0", Status: VersionPublished},
		AgentVersion{ID: ids.New(), TenantID: tenant, AgentID: agentID, Version: "2.0.0", Status: "DRAFT"},
	)

	// 显式版本：仅 PUBLISHED 可解析。
	v, err := svc.ResolveAgentVersion(ctx, tenant, "expense", "1.0.0")
	if err != nil {
		t.Fatalf("ResolveAgentVersion: %v", err)
	}
	if v.Version != "1.0.0" {
		t.Fatalf("version = %s, want 1.0.0", v.Version)
	}

	// 未发布版本 → 冲突。
	if _, err := svc.ResolveAgentVersion(ctx, tenant, "expense", "2.0.0"); errors.As(err).Code != errors.CodePluginVersionConflict {
		t.Fatalf("draft version err = %v, want PLUGIN_VERSION_CONFLICT", err)
	}

	// 不存在的版本 → 冲突。
	if _, err := svc.ResolveAgentVersion(ctx, tenant, "expense", "9.9.9"); errors.As(err).Code != errors.CodePluginVersionConflict {
		t.Fatalf("missing version err = %v, want PLUGIN_VERSION_CONFLICT", err)
	}

	// 智能体不存在 → NOT_FOUND。
	if _, err := svc.ResolveAgentVersion(ctx, tenant, "ghost", "1.0.0"); errors.As(err).Code != errors.CodePluginNotFound {
		t.Fatalf("missing agent err = %v, want PLUGIN_NOT_FOUND", err)
	}

	// 空版本 → 最新 PUBLISHED（1.0.0，而非 DRAFT 的 2.0.0）。
	latest, err := svc.ResolveAgentVersion(ctx, tenant, "expense", "")
	if err != nil {
		t.Fatalf("latest resolve: %v", err)
	}
	if latest.Version != "1.0.0" {
		t.Fatalf("latest = %s, want 1.0.0", latest.Version)
	}

	// 列表按版本降序。
	versions, err := svc.ListAgentVersions(ctx, tenant, "expense")
	if err != nil {
		t.Fatalf("ListAgentVersions: %v", err)
	}
	if len(versions) != 2 || versions[0].Version != "2.0.0" {
		t.Fatalf("versions = %+v, want 2.0.0 first", versions)
	}
}

func TestResolveAgentVersionNoPublished(t *testing.T) {
	svc, repo, ctx, tenant := catalogEnv()
	agentID := ids.New()
	repo.agents = append(repo.agents, AgentRef{ID: agentID, TenantID: tenant, AgentKey: "a"})
	repo.agentVers = append(repo.agentVers, AgentVersion{ID: ids.New(), TenantID: tenant, AgentID: agentID, Version: "1.0.0", Status: "DRAFT"})

	if _, err := svc.ResolveAgentVersion(ctx, tenant, "a", ""); errors.As(err).Code != errors.CodePluginVersionConflict {
		t.Fatalf("no published err = %v, want PLUGIN_VERSION_CONFLICT", err)
	}
}

func TestResolveToolVersion(t *testing.T) {
	svc, repo, ctx, tenant := catalogEnv()
	toolID := ids.New()
	repo.tools = append(repo.tools, ToolRef{ID: toolID, TenantID: tenant, ToolKey: "erp.write"})
	repo.toolVers = append(repo.toolVers,
		ToolVersion{ID: ids.New(), TenantID: tenant, ToolID: toolID, Version: "1.2.0", Status: VersionPublished, EffectClass: "EXTERNAL_WRITE"},
		ToolVersion{ID: ids.New(), TenantID: tenant, ToolID: toolID, Version: "1.3.0", Status: "DRAFT"},
	)

	v, err := svc.ResolveToolVersion(ctx, tenant, "erp.write", "1.2.0")
	if err != nil {
		t.Fatalf("ResolveToolVersion: %v", err)
	}
	if v.EffectClass != "EXTERNAL_WRITE" {
		t.Fatalf("effect class = %s", v.EffectClass)
	}
	if _, err := svc.ResolveToolVersion(ctx, tenant, "erp.write", "1.3.0"); errors.As(err).Code != errors.CodePluginVersionConflict {
		t.Fatalf("draft tool err = %v, want PLUGIN_VERSION_CONFLICT", err)
	}
	if _, err := svc.ResolveToolVersion(ctx, tenant, "erp.write", "9.9.9"); errors.As(err).Code != errors.CodePluginVersionConflict {
		t.Fatalf("missing tool version err = %v, want PLUGIN_VERSION_CONFLICT", err)
	}
	if _, err := svc.ResolveToolVersion(ctx, tenant, "ghost", "1.2.0"); errors.As(err).Code != errors.CodePluginNotFound {
		t.Fatalf("missing tool err = %v, want PLUGIN_NOT_FOUND", err)
	}
	// 工具解析必须显式指定版本（工具调用需固定绑定）。
	if _, err := svc.ResolveToolVersion(ctx, tenant, "erp.write", ""); errors.As(err).Code != errors.CodeValidationFailed {
		t.Fatalf("empty version err = %v, want VALIDATION_FAILED", err)
	}
}

// TestResolveLatestPublishedTool 锁定调用链真值来源的语义铁律：
// 「PUBLISHED 之内取最新」——工具自己的 DRAFT 版本不得挡住可用的已发布版本。
func TestResolveLatestPublishedTool(t *testing.T) {
	svc, repo, ctx, tenant := catalogEnv()
	toolID := ids.New()
	toolRef := ToolRef{ID: toolID, TenantID: tenant, ToolKey: "erp.read", Status: VersionPublished}
	repo.tools = append(repo.tools, toolRef)
	repo.toolVers = append(repo.toolVers,
		// PUBLISHED v1 在前，DRAFT v2 更晚——"取最新再看状态"会错误返回 409。
		ToolVersion{ID: ids.New(), TenantID: tenant, ToolID: toolID, Version: "1.0.0",
			Status: VersionPublished, EffectClass: "READ", CreatedAt: time.Unix(100, 0)},
		ToolVersion{ID: ids.New(), TenantID: tenant, ToolID: toolID, Version: "2.0.0",
			Status: "DRAFT", EffectClass: "READ", CreatedAt: time.Unix(200, 0)},
		// 更早的 PUBLISHED v0.9.0——确认取的是"最新"而非第一条。
		ToolVersion{ID: ids.New(), TenantID: tenant, ToolID: toolID, Version: "0.9.0",
			Status: VersionPublished, EffectClass: "READ", CreatedAt: time.Unix(50, 0)},
	)

	got, err := svc.ResolveLatestPublishedTool(ctx, tenant, toolID)
	if err != nil {
		t.Fatalf("ResolveLatestPublishedTool: %v", err)
	}
	if got.Version.Version != "1.0.0" {
		t.Fatalf("resolved version = %s, want 1.0.0（DRAFT 2.0.0 不得挡住 PUBLISHED 1.0.0）", got.Version.Version)
	}
	if got.Tool.ToolKey != "erp.read" {
		t.Fatalf("tool key = %s, want erp.read", got.Tool.ToolKey)
	}

	// 工具不存在 → 404（不是 409）。
	if _, err := svc.ResolveLatestPublishedTool(ctx, tenant, ids.New()); errors.As(err).Code != errors.CodePluginNotFound {
		t.Fatalf("missing tool err = %v, want PLUGIN_NOT_FOUND", err)
	}

	// 存在但没有任何 PUBLISHED 版本 → 409。
	ghostID := ids.New()
	repo.tools = append(repo.tools, ToolRef{ID: ghostID, TenantID: tenant, ToolKey: "draft.only"})
	repo.toolVers = append(repo.toolVers, ToolVersion{ID: ids.New(), TenantID: tenant, ToolID: ghostID,
		Version: "0.1.0", Status: "DRAFT", CreatedAt: time.Unix(10, 0)})
	if _, err := svc.ResolveLatestPublishedTool(ctx, tenant, ghostID); errors.As(err).Code != errors.CodePluginVersionConflict {
		t.Fatalf("no published err = %v, want PLUGIN_VERSION_CONFLICT", err)
	}

	// 零值 toolID → 参数错误。
	if _, err := svc.ResolveLatestPublishedTool(ctx, tenant, ids.ID{}); errors.As(err).Code != errors.CodeInvalidArgument {
		t.Fatalf("zero id err = %v, want INVALID_ARGUMENT", err)
	}
}

// TestResolveAgentVersionByID 锁定按 id 解析的语义（Run 绑定的是版本 id）：
// PUBLISHED 可解析；DRAFT → 409；不存在 → 404；零值 id → 参数错误。
func TestResolveAgentVersionByID(t *testing.T) {
	svc, repo, ctx, tenant := catalogEnv()
	agentID := ids.New()
	repo.agents = append(repo.agents, AgentRef{ID: agentID, TenantID: tenant, AgentKey: "auditor"})
	pubID := ids.New()
	repo.agentVers = append(repo.agentVers,
		AgentVersion{ID: pubID, TenantID: tenant, AgentID: agentID, Version: "1.0.0", Status: VersionPublished},
		AgentVersion{ID: ids.New(), TenantID: tenant, AgentID: agentID, Version: "2.0.0", Status: "DRAFT"},
	)

	v, err := svc.ResolveAgentVersionByID(ctx, tenant, pubID)
	if err != nil {
		t.Fatalf("ResolveAgentVersionByID: %v", err)
	}
	if v.Version != "1.0.0" {
		t.Fatalf("version = %s, want 1.0.0", v.Version)
	}
	if _, err := svc.ResolveAgentVersionByID(ctx, tenant, ids.New()); errors.As(err).Code != errors.CodePluginNotFound {
		t.Fatalf("missing err = %v, want PLUGIN_NOT_FOUND", err)
	}
	if _, err := svc.ResolveAgentVersionByID(ctx, tenant, ids.ID{}); errors.As(err).Code != errors.CodeInvalidArgument {
		t.Fatalf("zero id err = %v, want INVALID_ARGUMENT", err)
	}
	// DRAFT 版本：fake 的 SelectAgentVersionByID 按 id 查到 → 状态守卫在 service 层。
	draftID := repo.agentVers[1].ID
	if _, err := svc.ResolveAgentVersionByID(ctx, tenant, draftID); errors.As(err).Code != errors.CodePluginVersionConflict {
		t.Fatalf("draft err = %v, want PLUGIN_VERSION_CONFLICT", err)
	}
}

// TestResolveLatestPublishedToolByKey 锁定按 key 解析（dependency_manifest.tools
// 声明的是 key）：组合 SelectToolByKey + ResolveLatestPublishedTool 的语义不变。
func TestResolveLatestPublishedToolByKey(t *testing.T) {
	svc, repo, ctx, tenant := catalogEnv()
	toolID := ids.New()
	repo.tools = append(repo.tools, ToolRef{ID: toolID, TenantID: tenant, ToolKey: "erp.read", Status: VersionPublished})
	repo.toolVers = append(repo.toolVers,
		ToolVersion{ID: ids.New(), TenantID: tenant, ToolID: toolID, Version: "1.0.0",
			Status: VersionPublished, EffectClass: "READ", CreatedAt: time.Unix(100, 0)},
		ToolVersion{ID: ids.New(), TenantID: tenant, ToolID: toolID, Version: "2.0.0",
			Status: "DRAFT", CreatedAt: time.Unix(200, 0)},
	)

	got, err := svc.ResolveLatestPublishedToolByKey(ctx, tenant, "erp.read")
	if err != nil {
		t.Fatalf("ResolveLatestPublishedToolByKey: %v", err)
	}
	if got.Version.Version != "1.0.0" {
		t.Fatalf("version = %s, want 1.0.0（DRAFT 不得挡 PUBLISHED）", got.Version.Version)
	}
	if _, err := svc.ResolveLatestPublishedToolByKey(ctx, tenant, "ghost.tool"); errors.As(err).Code != errors.CodePluginNotFound {
		t.Fatalf("missing err = %v, want PLUGIN_NOT_FOUND", err)
	}
	if _, err := svc.ResolveLatestPublishedToolByKey(ctx, tenant, ""); errors.As(err).Code != errors.CodeInvalidArgument {
		t.Fatalf("empty key err = %v, want INVALID_ARGUMENT", err)
	}
}
