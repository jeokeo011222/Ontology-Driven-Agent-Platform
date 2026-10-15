package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// ---------------------------------------------------------------------------
// Skills — Skill Registry（Skill 作为一等 registry 资源）
//
// 与 Agents/Tools 完全同构：定义表 + 不可变版本表；删除一律软删（status=DELETED）；
// 平台内置技能（is_builtin=true）的 Update/Delete/版本变更一律 409。
// 所有 jsonb 列读取必须 ::text 投影（queryMaps 的历史坑）。
// ---------------------------------------------------------------------------

// validateAgentSkillRefs 校验 Agent 对技能的引用（用户自建链路，fail-closed）：
// 每个 skill_key 必须存在于 registry.skills（且未软删），并且至少有一个
// PUBLISHED 版本。任一不满足即 404 —— 与 seed/load.go 对 manifest.tools 的
// 悬空引用拒绝口径一致，绝不静默放行。
func validateAgentSkillRefs(ctx context.Context, tx pgx.Tx, skillKeys []string) error {
	for _, sk := range skillKeys {
		var n int
		err := tx.QueryRow(ctx, `
            SELECT count(*)
              FROM registry.skills s
              JOIN registry.skill_versions v ON v.skill_id = s.id AND v.status = 'PUBLISHED'
             WHERE s.tenant_id = app.current_tenant_id() AND s.skill_key = $1 AND s.status <> 'DELETED'`,
			sk).Scan(&n)
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "校验技能引用失败")
		}
		if n == 0 {
			return errors.Ef(errors.CodeNotFound,
				"dependency_manifest.skills 引用了不存在或没有已发布版本的技能 %q（拒绝悬空引用）", sk)
		}
	}
	return nil
}

// mergeSkillDependencies 把技能键列表合并进 Agent 的 dependency_manifest
// （{"skills": [...]}），用于创建/编辑 Agent 版本时的 manifest 透传。
// skills 为空时保持原 manifest 语义（nil → '{}'）。
// 返回序列化后的 manifest JSON（非 nil 时保证合法 JSON）。
func mergeSkillDependencies(ctx context.Context, tx pgx.Tx, rawManifest []byte, skillKeys []string) ([]byte, error) {
	if len(skillKeys) == 0 {
		out, _ := optionalJSON(rawManifest)
		return out, nil
	}
	if err := validateAgentSkillRefs(ctx, tx, skillKeys); err != nil {
		return nil, err
	}
	base := map[string]any{}
	if len(rawManifest) > 0 && json.Valid(rawManifest) {
		if err := json.Unmarshal(rawManifest, &base); err != nil {
			return nil, errors.E(errors.CodeInvalidArgument, "dependency_manifest 必须是合法 JSON 对象")
		}
	}
	base["skills"] = skillKeys
	out, err := json.Marshal(base)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "序列化 dependency_manifest 失败")
	}
	return out, nil
}

// ensureSkillMutable 拦截对**平台内置技能**的修改。
//
// 内置技能是平台自身能力，登记在技能管理页供查看，但不允许编辑、发布新版本
// 或删除。此校验放在 store 层而非仅前端隐藏按钮：绕过界面直接打 API 同样
// 必须被拒，不能只靠 UI。
//
// 返回 nil 表示可改；返回 409 Conflict 表示内置项不可改；无该行时返回 NotFound。
func ensureSkillMutable(ctx context.Context, tx pgx.Tx, skillID ids.ID) error {
	var isBuiltin bool
	err := tx.QueryRow(ctx,
		`SELECT is_builtin FROM registry.skills WHERE id = $1 AND status <> 'DELETED'`,
		skillID.String()).Scan(&isBuiltin)
	if err != nil {
		if err == pgx.ErrNoRows {
			return errors.E(errors.CodeNotFound, "技能不存在")
		}
		return errors.Wrap(err, errors.CodeInternal, "读取技能失败")
	}
	if isBuiltin {
		return errors.E(errors.CodeConflict, "该技能为平台内置技能，不可修改（仅用户自建技能可编辑/发布/删除）")
	}
	return nil
}

const listSkillsSQL = `
SELECT s.id::text AS id, s.skill_key, s.name, s.domain, s.description, s.status, s.is_builtin,
       v.id::text AS latest_version_id, v.version AS latest_version,
       v.status AS latest_version_status
  FROM registry.skills s
  LEFT JOIN LATERAL (
      SELECT id, version, status FROM registry.skill_versions
       WHERE skill_id = s.id ORDER BY created_at DESC LIMIT 1
  ) v ON TRUE
 WHERE ($1::text IS NULL OR s.skill_key > $1)
   AND (($3::text = '' AND s.status <> 'DELETED') OR ($3::text <> '' AND s.status = $3::text))
   AND ($4::text = '' OR s.skill_key ILIKE '%' || $4 || '%' OR s.name ILIKE '%' || $4 || '%' OR coalesce(s.description, '') ILIKE '%' || $4 || '%')
 ORDER BY s.skill_key ASC
 LIMIT $2`

// ListSkills 返回租户技能列表（按 skill_key 升序 cursor 分页，支持 ?status= 与 ?q=）。
// 软删除（status=DELETED）默认不出现在列表，除非显式按 status=DELETED 筛选。
// limit 由 handler 经 NormalizeLimit 收敛（默认 10，上限 200）。
func (s *Store) ListSkills(ctx context.Context, limit int, cursor, status, q string) (*ListResult, error) {
	from, err := decKey(cursor)
	if err != nil {
		return nil, err
	}
	res := &ListResult{Items: []map[string]any{}}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, listSkillsSQL, nullableStr(from), limit+1, status, q)
		if err != nil {
			return err
		}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}
		res.Items = rows
		if hasMore && len(rows) > 0 {
			if k, ok := rows[len(rows)-1]["skill_key"].(string); ok {
				res.NextCursor = encKey(k)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// CreateSkillInput 是创建技能（含首版本）的输入。
type CreateSkillInput struct {
	SkillKey       string
	Name           string
	Domain         string
	Description    string
	Version        string
	PromptFragment string
	// ToolBindings 是该技能运行时并入可用工具集合的 tool_key 列表；
	// 每个 key 必须指向 registry.tools 中 status='ACTIVE' 的真实工具（fail-closed）。
	ToolBindings []string
	InputSchema  []byte
	OutputSchema []byte
}

// validateSkillToolBindings 校验工具绑定：每个 tool_key 必须存在且 ACTIVE。
// 悬空引用会让 Agent 计划编译时才炸（更晚、更难定位），这里在写入前拦住。
func validateSkillToolBindings(ctx context.Context, tx pgx.Tx, keys []string) error {
	for _, tk := range keys {
		var status string
		err := tx.QueryRow(ctx,
			`SELECT status FROM registry.tools WHERE tenant_id = app.current_tenant_id() AND tool_key = $1`,
			tk).Scan(&status)
		if err != nil {
			if err == pgx.ErrNoRows {
				return errors.Ef(errors.CodeNotFound, "tool_bindings 引用了不存在的工具 %q（拒绝悬空引用）", tk)
			}
			return errors.Wrap(err, errors.CodeInternal, "校验工具绑定失败")
		}
		if status != "ACTIVE" {
			return errors.Ef(errors.CodeConflict, "工具 %q 状态为 %s，仅 ACTIVE 工具可绑定", tk, status)
		}
	}
	return nil
}

// CreateSkill 创建技能定义与首个 DRAFT 版本。
func (s *Store) CreateSkill(ctx context.Context, in CreateSkillInput) (map[string]any, error) {
	if in.SkillKey == "" || in.Name == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "skill_key 与 name 必填")
	}
	if !json.Valid(in.InputSchema) || !json.Valid(in.OutputSchema) {
		return nil, errors.E(errors.CodeInvalidArgument, "input_schema/output_schema 必须是合法 JSON")
	}
	if in.Version == "" {
		in.Version = "0.1.0"
	}
	if in.ToolBindings == nil {
		in.ToolBindings = []string{}
	}
	out := map[string]any{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		if err := validateSkillToolBindings(ctx, tx, in.ToolBindings); err != nil {
			return err
		}
		skillID := ids.New().String()
		if _, err := tx.Exec(ctx, `
            INSERT INTO registry.skills (id, tenant_id, skill_key, name, domain, description)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5)`,
			skillID, in.SkillKey, in.Name, in.Domain, in.Description); err != nil {
			return errors.Wrap(err, errors.CodeConflict, "skill_key 已存在")
		}
		bindings, _ := json.Marshal(in.ToolBindings)
		content, _ := json.Marshal(map[string]any{
			"prompt_fragment": in.PromptFragment,
			"tool_bindings":   json.RawMessage(bindings),
			"input":           json.RawMessage(in.InputSchema),
			"output":          json.RawMessage(in.OutputSchema),
		})
		versionID := ids.New().String()
		if _, err := tx.Exec(ctx, `
            INSERT INTO registry.skill_versions
                (id, tenant_id, skill_id, version, prompt_fragment, tool_bindings,
                 input_schema, output_schema, content_digest, status)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5::jsonb, $6::jsonb, $7::jsonb, $8, 'DRAFT')`,
			versionID, skillID, in.Version, in.PromptFragment, string(bindings),
			string(in.InputSchema), string(in.OutputSchema), digestBytes(content)); err != nil {
			return errors.Wrap(err, errors.CodeConflict, "技能版本已存在或写入失败")
		}
		out["id"] = skillID
		out["skill_key"] = in.SkillKey
		out["version_id"] = versionID
		out["version"] = in.Version
		out["status"] = "DRAFT"
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetSkill 返回单个技能及其最新版本详情。
func (s *Store) GetSkill(ctx context.Context, id ids.ID) (map[string]any, error) {
	out := map[string]any{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT s.id::text AS id, s.skill_key, s.name, s.domain, s.description, s.status, s.is_builtin,
                   v.id::text AS latest_version_id, v.version AS latest_version,
                   v.prompt_fragment::text AS prompt_fragment,
                   v.tool_bindings::text AS tool_bindings,
                   v.input_schema::text AS input_schema, v.output_schema::text AS output_schema,
                   v.status AS latest_version_status
              FROM registry.skills s
              LEFT JOIN LATERAL (
                  SELECT id, version, prompt_fragment, tool_bindings, input_schema, output_schema, status
                    FROM registry.skill_versions
                   WHERE skill_id = s.id ORDER BY created_at DESC LIMIT 1
              ) v ON TRUE
             WHERE s.id = $1`, id.String())
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errors.E(errors.CodeNotFound, "技能不存在")
		}
		out = rows[0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateSkill 更新技能的元数据（PATCH 语义：空串=不改动；skill_key 不可变）。
// status 仅接受 ACTIVE/INACTIVE（其余取值 fail-closed，DELETED 只能走删除）。
// 平台内置技能不可修改（409）。
func (s *Store) UpdateSkill(ctx context.Context, id ids.ID, name, description, domain, status string) (map[string]any, error) {
	if status != "" && status != "ACTIVE" && status != "INACTIVE" {
		return nil, errors.E(errors.CodeInvalidArgument, "status 只能是 ACTIVE 或 INACTIVE")
	}
	out := map[string]any{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		if err := ensureSkillMutable(ctx, tx, id); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
            UPDATE registry.skills
               SET name = COALESCE(NULLIF($2, ''), name),
                   description = COALESCE(NULLIF($3, ''), description),
                   domain = COALESCE(NULLIF($4, ''), domain),
                   status = COALESCE(NULLIF($5, ''), status),
                   updated_at = now()
             WHERE id = $1 AND status <> 'DELETED'`,
			id.String(), nullableStr(name), nullableStr(description), nullableStr(domain), nullableStr(status))
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "更新技能失败")
		}
		if tag.RowsAffected() == 0 {
			return errors.E(errors.CodeNotFound, "技能不存在")
		}
		rows, err := queryMaps(ctx, tx, `
            SELECT id::text AS id, skill_key, name, domain, description, status
              FROM registry.skills WHERE id = $1`, id.String())
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

// DeleteSkill 软删除技能（status → DELETED）。平台内置技能不可删除（409）。
func (s *Store) DeleteSkill(ctx context.Context, id ids.ID) error {
	return s.TenantTx(ctx, func(tx pgx.Tx) error {
		if err := ensureSkillMutable(ctx, tx, id); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
            UPDATE registry.skills SET status = 'DELETED', updated_at = now()
             WHERE id = $1 AND status <> 'DELETED'`, id.String())
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "删除技能失败")
		}
		if tag.RowsAffected() == 0 {
			return errors.E(errors.CodeNotFound, "技能不存在或已删除")
		}
		return nil
	})
}

// CreateSkillVersionInput 是为技能追加版本（DRAFT）的输入。
type CreateSkillVersionInput struct {
	Version        string
	PromptFragment string
	ToolBindings   []string
	InputSchema    []byte
	OutputSchema   []byte
}

// CreateSkillVersion 为技能追加一个 DRAFT 版本（版本号不可与已有版本重复）。
func (s *Store) CreateSkillVersion(ctx context.Context, skillID ids.ID, in CreateSkillVersionInput) (map[string]any, error) {
	if in.Version == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "version 必填")
	}
	if !json.Valid(in.InputSchema) || !json.Valid(in.OutputSchema) {
		return nil, errors.E(errors.CodeInvalidArgument, "input_schema/output_schema 必须是合法 JSON")
	}
	if in.ToolBindings == nil {
		in.ToolBindings = []string{}
	}
	out := map[string]any{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		if err := ensureSkillMutable(ctx, tx, skillID); err != nil {
			return err
		}
		if err := validateSkillToolBindings(ctx, tx, in.ToolBindings); err != nil {
			return err
		}
		bindings, _ := json.Marshal(in.ToolBindings)
		content, _ := json.Marshal(map[string]any{
			"prompt_fragment": in.PromptFragment,
			"tool_bindings":   json.RawMessage(bindings),
			"input":           json.RawMessage(in.InputSchema),
			"output":          json.RawMessage(in.OutputSchema),
		})
		versionID := ids.New().String()
		if _, err := tx.Exec(ctx, `
            INSERT INTO registry.skill_versions
                (id, tenant_id, skill_id, version, prompt_fragment, tool_bindings,
                 input_schema, output_schema, content_digest, status)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5::jsonb, $6::jsonb, $7::jsonb, $8, 'DRAFT')`,
			versionID, skillID.String(), in.Version, in.PromptFragment, string(bindings),
			string(in.InputSchema), string(in.OutputSchema), digestBytes(content)); err != nil {
			return errors.Wrap(err, errors.CodeConflict, "技能版本已存在或写入失败")
		}
		out["skill_id"] = skillID.String()
		out["version_id"] = versionID
		out["version"] = in.Version
		out["status"] = "DRAFT"
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// PublishSkill 把技能的最新版本 DRAFT → PUBLISHED（fail-closed）。
//
// 门禁：版本必须存在且为 DRAFT（PUBLISHED 重复发布 / 无版本一律 409）。
// 单条 UPDATE 带 CAS 条件，天然防并发重复发布。
func (s *Store) PublishSkill(ctx context.Context, skillID ids.ID) (map[string]any, error) {
	out := map[string]any{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		if err := ensureSkillMutable(ctx, tx, skillID); err != nil {
			return err
		}
		var versionID string
		if err := tx.QueryRow(ctx, `
            UPDATE registry.skill_versions
               SET status = 'PUBLISHED', published_at = now()
             WHERE skill_id = $1 AND status = 'DRAFT'
               AND id = (SELECT id FROM registry.skill_versions
                          WHERE skill_id = $1 ORDER BY created_at DESC LIMIT 1)
            RETURNING id::text`, skillID.String()).Scan(&versionID); err != nil {
			if err == pgx.ErrNoRows {
				return errors.E(errors.CodeConflict, "没有可发布的技能版本（最新版本不存在或已发布）")
			}
			return errors.Wrap(err, errors.CodeInternal, "发布技能失败")
		}
		out["skill_id"] = skillID.String()
		out["version_id"] = versionID
		out["status"] = "PUBLISHED"
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListSkillVersions 返回技能的全部版本（按创建时间倒序）。
func (s *Store) ListSkillVersions(ctx context.Context, skillID ids.ID) (*ListResult, error) {
	res := &ListResult{Items: []map[string]any{}}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT id::text AS id, skill_id::text AS skill_id, version,
                   prompt_fragment::text AS prompt_fragment,
                   tool_bindings::text AS tool_bindings,
                   input_schema::text AS input_schema, output_schema::text AS output_schema,
                   content_digest, status, created_at, published_at
              FROM registry.skill_versions
             WHERE skill_id = $1
             ORDER BY created_at DESC`, skillID.String())
		if err != nil {
			return err
		}
		res.Items = rows
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// ListSkillsByKeys 按技能键批量取**最新已发布**版本（Agent 计划编译的运行时热路径）。
//
// 返回行的 skill_key 可能少于请求键：不存在的技能 / 没有已发布版本的技能不会出现，
// 由调用方（internal/agent 计划编译）fail-closed 报错，绝不在本层静默补空。
// JSONB 列一律 ::text 投影（pgx 读 jsonb 缺 ::text 会反序列化失败）。
func (s *Store) ListSkillsByKeys(ctx context.Context, skillKeys []string) (*ListResult, error) {
	if len(skillKeys) == 0 {
		return &ListResult{Items: []map[string]any{}}, nil
	}
	res := &ListResult{Items: []map[string]any{}}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT s.id::text AS id, s.skill_key, s.name, s.domain,
                   v.id::text AS version_id, v.version,
                   v.prompt_fragment::text AS prompt_fragment,
                   v.tool_bindings::text AS tool_bindings,
                   v.input_schema::text AS input_schema,
                   v.output_schema::text AS output_schema,
                   v.content_digest, v.status
              FROM registry.skills s
              JOIN LATERAL (
                  SELECT id, version, prompt_fragment, tool_bindings, input_schema, output_schema,
                         content_digest, status
                    FROM registry.skill_versions
                   WHERE skill_id = s.id AND status = 'PUBLISHED'
                   ORDER BY created_at DESC LIMIT 1
              ) v ON TRUE
             WHERE s.status = 'ACTIVE' AND s.skill_key = ANY($1)
             ORDER BY s.skill_key ASC`, skillKeys)
		if err != nil {
			return err
		}
		res.Items = rows
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}
