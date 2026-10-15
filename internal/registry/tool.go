package registry

import (
	"context"

	"github.com/jackc/pgx/v5"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

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
