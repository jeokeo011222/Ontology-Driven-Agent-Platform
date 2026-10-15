package registry

import (
	"context"
	"testing"

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
