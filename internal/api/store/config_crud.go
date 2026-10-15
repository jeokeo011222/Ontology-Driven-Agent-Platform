// Package store — 配置类资源完整 CRUD 方法。
//
// 补齐 catalog.go 与 model_providers.go 已实现的 List/Create 之外的
// GetOne / Update / Delete 能力。删除一律采用软删除（status 置
// DELETED）而非物理删除，保持审计可追溯（§13 证据链）。
package store

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// ---------------------------------------------------------------------------
// Agents — GetOne / Update / Delete
// ---------------------------------------------------------------------------

// GetAgent 返回单个 Agent 的最新版本详情。
func (s *Store) GetAgent(ctx context.Context, id ids.ID) (map[string]any, error) {
	out := map[string]any{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT a.id::text AS id, a.agent_key, a.domain, a.description, a.status, a.is_builtin,
                   v.id::text AS latest_version_id, v.version AS latest_version,
                   v.input_schema::text, v.output_schema::text, v.status AS latest_version_status,
                   v.dependency_manifest::text AS dependency_manifest
              FROM registry.agents a
              LEFT JOIN LATERAL (
                  SELECT id, version, input_schema, output_schema, status, dependency_manifest
                    FROM registry.agent_versions
                   WHERE agent_id = a.id ORDER BY created_at DESC LIMIT 1
              ) v ON TRUE
             WHERE a.id = $1`, id.String())
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errors.E(errors.CodeNotFound, "Agent 不存在")
		}
		out = rows[0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateAgent 更新 Agent 的元数据（agent_key 不可变，版本升级走 publish）。
// 平台内置智能体不可修改（409）。
//
// skills 非 nil 时为「绑定技能」的显式替换语义：写入最新版本的
// dependency_manifest.skills（引用校验 fail-closed），并重算该版本
// content_digest（digest 覆盖 dependencies，确保下游版本锁定随之失效）。
func (s *Store) UpdateAgent(ctx context.Context, id ids.ID, description, domain string, skills *[]string) (map[string]any, error) {
	out := map[string]any{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		if err := ensureAgentMutable(ctx, tx, id); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
            UPDATE registry.agents
               SET description = COALESCE(NULLIF($2, ''), description),
                   domain = COALESCE(NULLIF($3, ''), domain)
             WHERE id = $1 AND status <> 'DELETED'`,
			id.String(), nullableStr(description), nullableStr(domain))
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "更新 Agent 失败")
		}
		if tag.RowsAffected() == 0 {
			return errors.E(errors.CodeNotFound, "Agent 不存在")
		}
		if skills != nil {
			if err := updateAgentVersionSkills(ctx, tx, id, *skills); err != nil {
				return err
			}
		}
		rows, err := queryMaps(ctx, tx, `
            SELECT id::text AS id, agent_key, domain, description, status
              FROM registry.agents WHERE id = $1`, id.String())
		if err != nil {
			return err
		}
		out = rows[0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// jsonText 把 queryMaps 归一化后的 jsonb 值（JSON 字符串）安全转回字节。
// 空值按 SQL NULL 语义折为 'null'，保证 json.RawMessage 始终合法。
func jsonText(v any) string {
	s, ok := v.(string)
	if !ok || s == "" {
		return "null"
	}
	return s
}

// updateAgentVersionSkills 把最新版本的 dependency_manifest.skills 替换为给定
// 列表并重算 content_digest。技能引用校验 fail-closed（404）。
func updateAgentVersionSkills(ctx context.Context, tx pgx.Tx, agentID ids.ID, skills []string) error {
	rows, err := queryMaps(ctx, tx, `
        SELECT id::text AS version_id, version, input_schema::text AS input_schema,
               output_schema::text AS output_schema, prompt_template::text AS prompt_template,
               config::text AS config, dependency_manifest::text AS dependency_manifest
          FROM registry.agent_versions
         WHERE agent_id = $1
         ORDER BY created_at DESC LIMIT 1`, agentID.String())
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return errors.E(errors.CodeNotFound, "Agent 不存在")
	}
	v := rows[0]
	deps, err := mergeSkillDependencies(ctx, tx, []byte(jsonText(v["dependency_manifest"])), skills)
	if err != nil {
		return err
	}
	content, _ := json.Marshal(map[string]any{
		"input":        json.RawMessage(jsonText(v["input_schema"])),
		"output":       json.RawMessage(jsonText(v["output_schema"])),
		"prompt":       json.RawMessage(jsonText(v["prompt_template"])),
		"config":       json.RawMessage(jsonText(v["config"])),
		"dependencies": json.RawMessage(deps),
	})
	if _, err := tx.Exec(ctx, `
        UPDATE registry.agent_versions
           SET dependency_manifest = $2::jsonb, content_digest = $3
         WHERE id = $1`, jsonText(v["version_id"]), string(deps), digestBytes(content)); err != nil {
		return errors.Wrap(err, errors.CodeInternal, "更新 Agent 依赖清单失败")
	}
	return nil
}

// DeleteAgent 软删除 Agent（status → DELETED）。平台内置智能体不可删除（409）。
func (s *Store) DeleteAgent(ctx context.Context, id ids.ID) error {
	return s.TenantTx(ctx, func(tx pgx.Tx) error {
		if err := ensureAgentMutable(ctx, tx, id); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
            UPDATE registry.agents SET status = 'DELETED'
             WHERE id = $1 AND status <> 'DELETED'`, id.String())
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "删除 Agent 失败")
		}
		if tag.RowsAffected() == 0 {
			return errors.E(errors.CodeNotFound, "Agent 不存在或已删除")
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// Ontologies — GetOne / Update / Delete
// ---------------------------------------------------------------------------

// GetOntology 返回单个本体及其最新版本详情。
func (s *Store) GetOntology(ctx context.Context, id ids.ID) (map[string]any, error) {
	out := map[string]any{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT o.id::text AS id, o.ontology_key, o.name, o.domain,
                   o.granularity, o.description, o.status,
                   v.id::text AS latest_version_id, v.version AS latest_version,
                   v.manifest, v.status AS latest_version_status
              FROM ontology.ontologies o
              LEFT JOIN LATERAL (
                  SELECT id, version, manifest, status FROM ontology.ontology_versions
                   WHERE ontology_id = o.id ORDER BY created_at DESC LIMIT 1
              ) v ON TRUE
             WHERE o.id = $1`, id.String())
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errors.E(errors.CodeNotFound, "本体不存在")
		}
		out = rows[0]
		// 校验发现项：web/src/features/ontology/pages/OntologyDetailPage.tsx 直接对
		// ontology.findings 调用 .filter(...)，因此这个键必须**恒存在且为数组**。
		// 此前 GetOntology 不返回该键，真实接口下详情页会在渲染时抛 TypeError
		// （只在 VITE_USE_FIXTURES=1 的演示数据模式下有数据，掩盖了该缺陷）。
		findings, err := queryLatestFindings(ctx, tx, id.String())
		if err != nil {
			return err
		}
		out["findings"] = findings["items"]
		out["validation"] = findings["run"]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateOntology 更新本体的元数据与启停状态。
//
// status 为三态：省略（空串）= 不改动；ACTIVE = 启用；INACTIVE = 停用。
// 其余取值一律 fail-closed 拒绝（本体级 status 无 CHECK 约束，不能把
// 任意字符串写库后再让它冒充合法状态）。已软删除（DELETED）的行不可改。
func (s *Store) UpdateOntology(ctx context.Context, id ids.ID, name, description, status string) (map[string]any, error) {
	if status != "" && status != "ACTIVE" && status != "INACTIVE" {
		return nil, errors.E(errors.CodeInvalidArgument, "status 只能是 ACTIVE 或 INACTIVE")
	}
	out := map[string]any{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
            UPDATE ontology.ontologies
               SET name = COALESCE(NULLIF($2, ''), name),
                   description = COALESCE(NULLIF($3, ''), description),
                   status = COALESCE(NULLIF($4, ''), status)
             WHERE id = $1 AND status <> 'DELETED'`,
			id.String(), nullableStr(name), nullableStr(description), nullableStr(status))
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "更新本体失败")
		}
		if tag.RowsAffected() == 0 {
			return errors.E(errors.CodeNotFound, "本体不存在")
		}
		rows, err := queryMaps(ctx, tx, `
            SELECT id::text AS id, ontology_key, name, domain, granularity, description, status
              FROM ontology.ontologies WHERE id = $1`, id.String())
		if err != nil {
			return err
		}
		out = rows[0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteOntology 软删除本体。
func (s *Store) DeleteOntology(ctx context.Context, id ids.ID) error {
	return s.TenantTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
            UPDATE ontology.ontologies SET status = 'DELETED'
             WHERE id = $1 AND status <> 'DELETED'`, id.String())
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "删除本体失败")
		}
		if tag.RowsAffected() == 0 {
			return errors.E(errors.CodeNotFound, "本体不存在或已删除")
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// Plugins — GetOne / Update / Delete
// ---------------------------------------------------------------------------

// GetPlugin 返回单个插件及其最新版本详情。
func (s *Store) GetPlugin(ctx context.Context, id ids.ID) (map[string]any, error) {
	out := map[string]any{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT p.id::text AS id, p.plugin_key, p.plugin_kind, p.name, p.description, p.status,
                   v.id::text AS latest_version_id, v.version AS latest_version, v.manifest,
                   (p.tenant_id IS NULL) AS is_platform
              FROM registry.plugins p
              LEFT JOIN LATERAL (
                  SELECT id, version, manifest FROM registry.plugin_versions
                   WHERE plugin_id = p.id ORDER BY created_at DESC LIMIT 1
              ) v ON TRUE
             WHERE p.id = $1`, id.String())
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errors.E(errors.CodeNotFound, "插件不存在")
		}
		out = rows[0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UpdatePlugin 更新插件的元数据。
func (s *Store) UpdatePlugin(ctx context.Context, id ids.ID, name, description string) (map[string]any, error) {
	out := map[string]any{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
            UPDATE registry.plugins
               SET name = COALESCE(NULLIF($2, ''), name),
                   description = COALESCE(NULLIF($3, ''), description)
             WHERE id = $1 AND status <> 'DELETED'`,
			id.String(), nullableStr(name), nullableStr(description))
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "更新插件失败")
		}
		if tag.RowsAffected() == 0 {
			return errors.E(errors.CodeNotFound, "插件不存在")
		}
		rows, err := queryMaps(ctx, tx, `
            SELECT id::text AS id, plugin_key, plugin_kind, name, description, status
              FROM registry.plugins WHERE id = $1`, id.String())
		if err != nil {
			return err
		}
		out = rows[0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeletePlugin 软删除插件（同时标记所有安装记录为 DISABLED）。
func (s *Store) DeletePlugin(ctx context.Context, id ids.ID) error {
	return s.TenantTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
            UPDATE registry.plugins SET status = 'DELETED'
             WHERE id = $1 AND status <> 'DELETED'`, id.String())
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "删除插件失败")
		}
		if tag.RowsAffected() == 0 {
			return errors.E(errors.CodeNotFound, "插件不存在或已删除")
		}
		// 同步禁用所有租户的安装记录。失败必须回滚：否则插件已置 DELETED
		// 而安装记录仍为 ACTIVE，形成"已删除仍可用"的不一致状态。
		if _, err := tx.Exec(ctx, `
            UPDATE registry.plugin_installations SET installation_state = 'DISABLED'
             WHERE plugin_id = $1 AND installation_state = 'ACTIVE'`, id.String()); err != nil {
			return errors.Wrap(err, errors.CodeInternal, "禁用插件安装记录失败")
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// Tools — GetOne / Update / Delete
// ---------------------------------------------------------------------------

// GetTool 返回单个工具及其最新版本详情。
func (s *Store) GetTool(ctx context.Context, id ids.ID) (map[string]any, error) {
	out := map[string]any{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT t.id::text AS id, t.tool_key, t.description, t.status, t.is_builtin,
                   v.id::text AS latest_version_id, v.version AS latest_version,
                   v.input_schema, v.output_schema, v.capabilities, v.effect_class,
                   v.status AS latest_version_status
              FROM registry.tools t
              LEFT JOIN LATERAL (
                  SELECT id, version, input_schema, output_schema, capabilities, effect_class, status
                    FROM registry.tool_versions
                   WHERE tool_id = t.id ORDER BY created_at DESC LIMIT 1
              ) v ON TRUE
             WHERE t.id = $1`, id.String())
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errors.E(errors.CodeNotFound, "工具不存在")
		}
		out = rows[0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateTool 更新工具的元数据。
func (s *Store) UpdateTool(ctx context.Context, id ids.ID, description string) (map[string]any, error) {
	out := map[string]any{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		if err := ensureToolMutable(ctx, tx, id); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
            UPDATE registry.tools
               SET description = COALESCE(NULLIF($2, ''), description)
             WHERE id = $1 AND status <> 'DELETED'`,
			id.String(), nullableStr(description))
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "更新工具失败")
		}
		if tag.RowsAffected() == 0 {
			return errors.E(errors.CodeNotFound, "工具不存在")
		}
		rows, err := queryMaps(ctx, tx, `
            SELECT id::text AS id, tool_key, description, status
              FROM registry.tools WHERE id = $1`, id.String())
		if err != nil {
			return err
		}
		out = rows[0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteTool 软删除工具。
func (s *Store) DeleteTool(ctx context.Context, id ids.ID) error {
	return s.TenantTx(ctx, func(tx pgx.Tx) error {
		if err := ensureToolMutable(ctx, tx, id); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
            UPDATE registry.tools SET status = 'DELETED'
             WHERE id = $1 AND status <> 'DELETED'`, id.String())
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "删除工具失败")
		}
		if tag.RowsAffected() == 0 {
			return errors.E(errors.CodeNotFound, "工具不存在或已删除")
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// Policies — GetOne / Update / Delete
// ---------------------------------------------------------------------------

// GetPolicy 返回某个 policy_key 的全部版本（ListPolicyVersions 已有，此处复用）。

// UpdatePolicyStatus 更新某个策略版本的状态（激活/停用）。
func (s *Store) UpdatePolicyStatus(ctx context.Context, policyKey, version, status string) (map[string]any, error) {
	if policyKey == "" || version == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "policy_key 与 version 必填")
	}
	switch status {
	case "ACTIVE", "DISABLED", "ARCHIVED":
	default:
		return nil, errors.E(errors.CodeInvalidArgument, "status 仅支持 ACTIVE / DISABLED / ARCHIVED")
	}
	out := map[string]any{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
            UPDATE governance.policy_versions
               SET status = $3, updated_at = now()
             WHERE policy_key = $1 AND version = $2`, policyKey, version, status)
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "更新策略状态失败")
		}
		if tag.RowsAffected() == 0 {
			return errors.E(errors.CodeNotFound, "策略版本不存在")
		}
		rows, err := queryMaps(ctx, tx, `
            SELECT id::text AS id, policy_key, version, status, policy_language,
                   created_at, published_at
              FROM governance.policy_versions
             WHERE policy_key = $1 AND version = $2`, policyKey, version)
		if err != nil {
			return err
		}
		out = rows[0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeletePolicyVersion 删除指定策略版本（仅允许删除 DRAFT/REJECTED 状态）。
func (s *Store) DeletePolicyVersion(ctx context.Context, policyKey, version string) error {
	if policyKey == "" || version == "" {
		return errors.E(errors.CodeInvalidArgument, "policy_key 与 version 必填")
	}
	return s.TenantTx(ctx, func(tx pgx.Tx) error {
		// 只允许删除非 PUBLISHED 的版本，保护运行时策略
		tag, err := tx.Exec(ctx, `
            DELETE FROM governance.policy_versions
             WHERE policy_key = $1 AND version = $2 AND status IN ('DRAFT','REJECTED','DISABLED')`,
			policyKey, version)
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "删除策略版本失败")
		}
		if tag.RowsAffected() == 0 {
			return errors.E(errors.CodeConflict, "策略版本不存在或已发布不可删除")
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// ModelProviders — Update / Delete
// ---------------------------------------------------------------------------

// GetModelProvider 返回单个模型供应商及其端点详情。
func (s *Store) GetModelProvider(ctx context.Context, providerID string) (map[string]any, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}
	out := map[string]any{}
	rows, err := s.queryMapsNoTenant(ctx, `
        SELECT p.provider_id, p.display_name, p.provider_type, p.status, p.created_at,
               e.endpoint_id::text AS endpoint_id, e.endpoint_name, e.deployment_type,
               e.region_code, e.base_url, e.status AS endpoint_status,
               coalesce(e.credential_masked, '') AS credential_masked,
               CASE WHEN e.credential_ciphertext IS NOT NULL THEN 'CIPHERTEXT'
                    WHEN e.credential_ref IS NOT NULL THEN 'ENV_REF'
                    ELSE 'NONE' END AS credential_source,
               (e.credential_ciphertext IS NOT NULL OR e.credential_ref IS NOT NULL) AS has_credential
          FROM llm.providers p
          LEFT JOIN LATERAL (
              SELECT endpoint_id, endpoint_name, deployment_type, region_code,
                     base_url, credential_ref, credential_ciphertext, credential_masked, status
                FROM llm.endpoints
               WHERE provider_id = p.provider_id ORDER BY created_at DESC LIMIT 1
          ) e ON TRUE
         WHERE p.provider_id = $1`, providerID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.E(errors.CodeNotFound, "模型供应商不存在")
	}
	out = rows[0]
	return out, nil
}

// UpdateModelProvider 更新模型供应商的基础信息与端点状态。
func (s *Store) UpdateModelProvider(ctx context.Context, providerID, displayName, status string) (map[string]any, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}
	if err := ValidateProviderID(providerID); err != nil {
		return nil, err
	}
	if displayName == "" && status == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "display_name 或 status 至少填一项")
	}
	if status != "" && !modelStatuses[status] {
		return nil, errors.E(errors.CodeInvalidArgument, "status 仅支持 ACTIVE / DISABLED")
	}
	if displayName != "" {
		displayName = strings.TrimSpace(displayName)
	}
	tag, err := s.Pool.Exec(ctx, `
        UPDATE llm.providers
           SET display_name = COALESCE(NULLIF($2, ''), display_name),
               status = COALESCE(NULLIF($3, ''), status)
         WHERE provider_id = $1`, providerID, nullableStr(displayName), nullableStr(status))
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "更新模型供应商失败")
	}
	if tag.RowsAffected() == 0 {
		return nil, errors.E(errors.CodeNotFound, "模型供应商不存在")
	}
	return s.GetModelProvider(ctx, providerID)
}

// DeleteModelProvider 删除模型供应商及其名下端点（应用层级联 + 下游保护）。
//
// 为什么不是数据库级联：000010 定义的 llm.endpoints.provider_id → llm.providers
// 外键是 NO ACTION（并非 ON DELETE CASCADE）；且端点被 llm.model_revisions 引用时
// 也无法级联删除。此前的裸 DELETE 遇到带端点的供应商会直接触发 FK 违例，
// 被包装成 INTERNAL 500（"删除模型供应商失败"），用户看不到任何可操作原因。
// 这里改为在单事务内显式"先查引用、再删端点、最后删供应商"，把失败变成可定位的 409。
//
// 下游保护（任一命中 → 409，绝不静默吞掉）：
//   - 名下端点已被 llm.model_revisions 引用（版本档案是审计链的一部分）；
//   - 名下端点已产生 llm.usage_ledger 记录（账本是审计真值，永不硬删）。
//
// 命中保护时应改为**停用**（PATCH status=DISABLED），而不是硬删。
func (s *Store) DeleteModelProvider(ctx context.Context, providerID string) error {
	if s == nil || s.Pool == nil {
		return errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}
	if err := ValidateProviderID(providerID); err != nil {
		return err
	}

	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return errors.Wrap(err, errors.CodeUnavailable, "开启删除事务失败")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM llm.providers WHERE provider_id = $1)`,
		providerID).Scan(&exists); err != nil {
		return errors.Wrap(err, errors.CodeInternal, "检查供应商存在性失败")
	}
	if !exists {
		return errors.E(errors.CodeNotFound, "模型供应商不存在："+providerID)
	}

	// ① 版本档案引用检查（名下任一端点被 llm.model_revisions 引用即拒绝）。
	var revCount int
	if err := tx.QueryRow(ctx, `
        SELECT COUNT(*) FROM llm.model_revisions r
          JOIN llm.endpoints e ON e.endpoint_id = r.endpoint_id
         WHERE e.provider_id = $1`, providerID).Scan(&revCount); err != nil {
		return errors.Wrap(err, errors.CodeInternal, "检查版本档案引用失败")
	}
	if revCount > 0 {
		return errors.Ef(errors.CodeConflict,
			"供应商 %s 名下端点已关联 %d 条模型版本档案，请先清理版本档案，或改为停用供应商（PATCH /api/v1/admin/model-providers/%s，status=DISABLED）",
			providerID, revCount, providerID)
	}

	// ② 用量账本引用检查（账本是审计真值，永不硬删；有账本就拒绝删除）。
	var ledgerCount int
	if err := tx.QueryRow(ctx, `
        SELECT COUNT(*) FROM llm.usage_ledger l
          JOIN llm.endpoints e ON e.endpoint_id = l.endpoint_id
         WHERE e.provider_id = $1`, providerID).Scan(&ledgerCount); err != nil {
		return errors.Wrap(err, errors.CodeInternal, "检查用量账本引用失败")
	}
	if ledgerCount > 0 {
		return errors.Ef(errors.CodeConflict,
			"供应商 %s 名下端点已产生 %d 条用量账本记录（审计真值不可硬删），请改为停用供应商（PATCH /api/v1/admin/model-providers/%s，status=DISABLED）",
			providerID, ledgerCount, providerID)
	}

	// 先删端点，再删供应商（应用层级联，规避 NO ACTION 外键）。
	if _, err := tx.Exec(ctx, `DELETE FROM llm.endpoints WHERE provider_id = $1`, providerID); err != nil {
		return errors.Wrap(err, errors.CodeInternal, "删除供应商名下端点失败")
	}
	tag, err := tx.Exec(ctx, `DELETE FROM llm.providers WHERE provider_id = $1`, providerID)
	if err != nil {
		return errors.Wrap(err, errors.CodeInternal, "删除模型供应商失败")
	}
	if tag.RowsAffected() == 0 {
		return errors.E(errors.CodeNotFound, "模型供应商不存在："+providerID)
	}
	if err := tx.Commit(ctx); err != nil {
		return errors.Wrap(err, errors.CodeInternal, "提交删除事务失败")
	}
	return nil
}
