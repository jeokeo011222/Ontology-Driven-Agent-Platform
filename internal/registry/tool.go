package registry

import (
	"context"

	"github.com/jackc/pgx/v5"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// errNoPublishedVersion 标记"工具存在但没有任何 PUBLISHED 版本"。
// 与 errRepoNotFound（工具本身不存在）区分开：两者对调用方的语义不同（404 vs 409）。
var errNoPublishedVersion = errors.E(errors.CodePluginVersionConflict, "工具没有已发布版本")

// InvocableTool 是"可调用的工具版本"：工具标识 + 最新 PUBLISHED 版本。
type InvocableTool struct {
	Tool    ToolRef
	Version ToolVersion
}

// ResolveLatestPublishedTool 解析工具的**最新已发布版本**（调用链的真值来源）。
//
// 语义铁律：必须按「status='PUBLISHED' 之内取最新」过滤，而不是"取最新版本再看
// 状态"。后者会在"PUBLISHED v1 + DRAFT v2"时让 v2 挡住可用的 v1——工具被自己的
// 草稿锁死。这是从 store 直查实现里修掉的真实缺陷。
//
// 工具不存在 → CodePluginNotFound（404）；存在但无已发布版本 →
// CodePluginVersionConflict（409）。
func (s *CatalogService) ResolveLatestPublishedTool(ctx context.Context, tenantID, toolID ids.ID) (InvocableTool, error) {
	if toolID.IsZero() {
		return InvocableTool{}, errors.E(errors.CodeInvalidArgument, "tool_id 不合法")
	}
	v, err := s.repo.SelectLatestPublishedTool(ctx, tenantID, toolID)
	if err != nil {
		if err == errNoPublishedVersion {
			return InvocableTool{}, errors.Ef(errors.CodePluginVersionConflict,
				"工具 %s 没有已发布版本（仅 PUBLISHED 可调用）", toolID)
		}
		if notFound(err) {
			return InvocableTool{}, errors.Ef(errors.CodePluginNotFound, "工具 %s 不存在", toolID)
		}
		return InvocableTool{}, errors.Wrap(err, errors.CodeInternal, "解析工具版本失败")
	}
	return v, nil
}

// ResolveLatestPublishedToolByKey 按 tool_key 解析最新已发布版本
// （dependency_manifest.tools 声明的是 key 而非 id；与 ResolveLatestPublishedTool
// 同一语义铁律：PUBLISHED 之内取最新）。工具不存在 → 404；无已发布版本 → 409。
func (s *CatalogService) ResolveLatestPublishedToolByKey(ctx context.Context, tenantID ids.ID, toolKey string) (InvocableTool, error) {
	if toolKey == "" {
		return InvocableTool{}, errors.E(errors.CodeInvalidArgument, "tool_key 不合法")
	}
	tool, err := s.repo.SelectToolByKey(ctx, tenantID, toolKey)
	if err != nil {
		if notFound(err) {
			return InvocableTool{}, errors.Ef(errors.CodePluginNotFound, "工具 %q 不存在", toolKey)
		}
		return InvocableTool{}, errors.Wrap(err, errors.CodeInternal, "查询工具失败")
	}
	return s.ResolveLatestPublishedTool(ctx, tenantID, tool.ID)
}

// ResolveToolVersion 解析工具版本。仅 PUBLISHED 可解析：
// 工具不存在返回 CodePluginNotFound；版本不存在或未发布返回
// CodePluginVersionConflict。
func (s *CatalogService) ResolveToolVersion(ctx context.Context, tenantID ids.ID, toolKey, version string) (ToolVersion, error) {
	tool, err := s.repo.SelectToolByKey(ctx, tenantID, toolKey)
	if err != nil {
		if notFound(err) {
			return ToolVersion{}, errors.Ef(errors.CodePluginNotFound, "工具 %q 不存在", toolKey)
		}
		return ToolVersion{}, errors.Wrap(err, errors.CodeInternal, "查询工具失败")
	}
	if version == "" {
		return ToolVersion{}, errors.E(errors.CodeValidationFailed, "解析工具版本必须显式指定 version")
	}
	v, err := s.repo.SelectToolVersion(ctx, tenantID, tool.ID, version)
	if err != nil {
		if notFound(err) {
			return ToolVersion{}, errors.Ef(errors.CodePluginVersionConflict,
				"工具 %q 版本 %s 不存在", toolKey, version)
		}
		return ToolVersion{}, errors.Wrap(err, errors.CodeInternal, "查询工具版本失败")
	}
	if v.Status != VersionPublished {
		return ToolVersion{}, errors.Ef(errors.CodePluginVersionConflict,
			"工具 %q 版本 %s 状态为 %s，仅 PUBLISHED 可解析", toolKey, version, v.Status)
	}
	return v, nil
}

func (r pgCatalogRepository) SelectLatestPublishedTool(ctx context.Context, tenantID, toolID ids.ID) (InvocableTool, error) {
	var out InvocableTool
	err := readInTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		// 第一条：工具本体必须存在（区分 404 与 409）。
		row := tx.QueryRow(ctx,
			`SELECT id, tenant_id, plugin_id, tool_key, description, status
			 FROM registry.tools WHERE tenant_id = $1 AND id = $2`,
			tenantID.String(), toolID.String())
		var (
			idText, tenantTxt, pluginTxt string
			scanErr                      error
		)
		if scanErr = row.Scan(&idText, &tenantTxt, &pluginTxt, &out.Tool.ToolKey,
			&out.Tool.Description, &out.Tool.Status); scanErr != nil {
			if scanErr == pgx.ErrNoRows {
				return errRepoNotFound
			}
			return scanErr
		}
		if out.Tool.ID, scanErr = ids.Parse(idText); scanErr != nil {
			return scanErr
		}
		if out.Tool.TenantID, scanErr = ids.Parse(tenantTxt); scanErr != nil {
			return scanErr
		}
		if out.Tool.PluginID, scanErr = ids.Parse(pluginTxt); scanErr != nil {
			return scanErr
		}
		// 第二条：PUBLISHED 之内取最新——不是"取最新再看状态"。
		vrow := tx.QueryRow(ctx,
			`SELECT id, tenant_id, tool_id, plugin_version_id, version, input_schema, output_schema,
			        capabilities, effect_class, execution_config, content_digest, status, created_at
			 FROM registry.tool_versions
			 WHERE tenant_id = $1 AND tool_id = $2 AND status = 'PUBLISHED'
			 ORDER BY created_at DESC
			 LIMIT 1`,
			tenantID.String(), toolID.String())
		var toolTxt, pluginVerTx string
		if scanErr = vrow.Scan(&idText, &tenantTxt, &toolTxt, &pluginVerTx, &out.Version.Version,
			&out.Version.InputSchema, &out.Version.OutputSchema, &out.Version.Capabilities,
			&out.Version.EffectClass, &out.Version.ExecutionConfig,
			&out.Version.ContentDigest, &out.Version.Status, &out.Version.CreatedAt); scanErr != nil {
			if scanErr == pgx.ErrNoRows {
				return errNoPublishedVersion
			}
			return scanErr
		}
		if out.Version.ID, scanErr = ids.Parse(idText); scanErr != nil {
			return scanErr
		}
		if out.Version.TenantID, scanErr = ids.Parse(tenantTxt); scanErr != nil {
			return scanErr
		}
		if out.Version.ToolID, scanErr = ids.Parse(toolTxt); scanErr != nil {
			return scanErr
		}
		if out.Version.PluginVersionID, scanErr = ids.Parse(pluginVerTx); scanErr != nil {
			return scanErr
		}
		return nil
	})
	return out, err
}

func (r pgCatalogRepository) SelectToolByKey(ctx context.Context, tenantID ids.ID, toolKey string) (ToolRef, error) {
	var tool ToolRef
	err := readInTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`SELECT id, tenant_id, plugin_id, tool_key, description, status
			 FROM registry.tools WHERE tenant_id = $1 AND tool_key = $2`,
			tenantID.String(), toolKey)
		var (
			idText, tenantTxt, pluginTxt string
			scanErr                      error
		)
		if scanErr = row.Scan(&idText, &tenantTxt, &pluginTxt, &tool.ToolKey,
			&tool.Description, &tool.Status); scanErr != nil {
			if scanErr == pgx.ErrNoRows {
				return errRepoNotFound
			}
			return scanErr
		}
		if tool.ID, scanErr = ids.Parse(idText); scanErr != nil {
			return scanErr
		}
		if tool.TenantID, scanErr = ids.Parse(tenantTxt); scanErr != nil {
			return scanErr
		}
		if tool.PluginID, scanErr = ids.Parse(pluginTxt); scanErr != nil {
			return scanErr
		}
		return nil
	})
	return tool, err
}

func (r pgCatalogRepository) SelectToolVersion(ctx context.Context, tenantID, toolID ids.ID, version string) (ToolVersion, error) {
	var v ToolVersion
	err := readInTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`SELECT id, tenant_id, tool_id, plugin_version_id, version, input_schema, output_schema,
			        capabilities, effect_class, execution_config, content_digest, status, created_at
			 FROM registry.tool_versions WHERE tenant_id = $1 AND tool_id = $2 AND version = $3`,
			tenantID.String(), toolID.String(), version)
		var (
			idText, tenantTxt, toolTxt, pluginTx string
			scanErr                              error
		)
		if scanErr = row.Scan(&idText, &tenantTxt, &toolTxt, &pluginTx, &v.Version,
			&v.InputSchema, &v.OutputSchema, &v.Capabilities, &v.EffectClass, &v.ExecutionConfig,
			&v.ContentDigest, &v.Status, &v.CreatedAt); scanErr != nil {
			if scanErr == pgx.ErrNoRows {
				return errRepoNotFound
			}
			return scanErr
		}
		if v.ID, scanErr = ids.Parse(idText); scanErr != nil {
			return scanErr
		}
		if v.TenantID, scanErr = ids.Parse(tenantTxt); scanErr != nil {
			return scanErr
		}
		if v.ToolID, scanErr = ids.Parse(toolTxt); scanErr != nil {
			return scanErr
		}
		if v.PluginVersionID, scanErr = ids.Parse(pluginTx); scanErr != nil {
			return scanErr
		}
		return nil
	})
	return v, err
}
