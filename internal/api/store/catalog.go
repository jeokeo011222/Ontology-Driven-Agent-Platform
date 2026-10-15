package store

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/tenantctx"
)

// digestBytes 返回 SHA-256 十六进制小写摘要。
func digestBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// encKey/decKey 是文本键分页游标（适用于无 created_at 列的注册表资产）。
func encKey(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func decKey(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return "", errors.E(errors.CodeInvalidArgument, "cursor 不合法")
	}
	return string(b), nil
}

// subjectFromContext 读取当前主体（写入 created_by）。
func subjectFromContext(ctx context.Context) (ids.ID, error) {
	id, ok := tenantctx.FromContext(ctx)
	if !ok || id.SubjectID.IsZero() {
		return ids.ID{}, errors.E(errors.CodeUnavailable, "缺少主体身份，无法执行写入")
	}
	return id.SubjectID, nil
}

// ListResult 是通用列表结果。
type ListResult struct {
	Items      []map[string]any `json:"items"`
	NextCursor string           `json:"next_cursor"`
}

// insertPlainPlugin 创建租户级插件与插件版本（agent/tool 版本的外键前置）。
func insertPlainPlugin(ctx context.Context, tx pgx.Tx, pluginKey, pluginKind, name string, manifest []byte, createdBy ids.ID) (string, error) {
	var pluginID string
	if err := tx.QueryRow(ctx, `
        INSERT INTO registry.plugins (id, tenant_id, plugin_key, plugin_kind, name, description)
        VALUES ($1, app.current_tenant_id(), $2, $3, $4, '')
        RETURNING id::text`,
		ids.New().String(), pluginKey, pluginKind, name).Scan(&pluginID); err != nil {
		return "", errors.Wrap(err, errors.CodeConflict, "插件键已存在或写入失败")
	}
	if err := tx.QueryRow(ctx, `
        INSERT INTO registry.plugin_versions
            (id, tenant_id, plugin_id, version, manifest, artifact_digest, status, created_by)
        VALUES ($1, app.current_tenant_id(), $2, '0.1.0', $3::jsonb, $4, 'CREATED', $5)
        RETURNING id::text`,
		ids.New().String(), pluginID, string(manifest), digestBytes(manifest), createdBy.String()).Scan(new(string)); err != nil {
		return "", errors.Wrap(err, errors.CodeInternal, "创建插件版本失败")
	}
	return pluginID, nil
}

// ---------------------------------------------------------------------------
// Agents
// ---------------------------------------------------------------------------

const listAgentsSQL = `
SELECT a.id::text AS id, a.agent_key, a.domain, a.description, a.status, a.is_builtin,
       v.id::text AS latest_version_id, v.version AS latest_version,
       v.status AS latest_version_status
  FROM registry.agents a
  LEFT JOIN LATERAL (
      SELECT id, version, status FROM registry.agent_versions
       WHERE agent_id = a.id ORDER BY created_at DESC LIMIT 1
  ) v ON TRUE
 WHERE ($1::text IS NULL OR a.agent_key > $1)
   AND (($3::text = '' AND a.status <> 'DELETED') OR ($3::text <> '' AND a.status = $3::text))
   AND ($4::text = '' OR a.agent_key ILIKE '%' || $4 || '%' OR coalesce(a.description, '') ILIKE '%' || $4 || '%')
 ORDER BY a.agent_key ASC
 LIMIT $2`

// ensureAgentMutable 拦截对**平台内置智能体**的修改。
//
// 内置智能体（AI 自动生成 / 本体校验 / 运行编排 / ERP 对账 / 模型治理）是平台
// 自身能力，登记在 Agent 管理页供查看，但**不允许**编辑、发布或删除。此校验放在
// store 层而非仅前端隐藏按钮：绕过界面直接打 API 同样必须被拒，不能只靠 UI。
//
// 返回 nil 表示可改；返回 409 Conflict 表示内置项不可改；无该行时返回 NotFound。
func ensureAgentMutable(ctx context.Context, tx pgx.Tx, agentID ids.ID) error {
	var isBuiltin bool
	err := tx.QueryRow(ctx,
		`SELECT is_builtin FROM registry.agents WHERE id = $1 AND status <> 'DELETED'`,
		agentID.String()).Scan(&isBuiltin)
	if err != nil {
		if err == pgx.ErrNoRows {
			return errors.E(errors.CodeNotFound, "Agent 不存在")
		}
		return errors.Wrap(err, errors.CodeInternal, "读取 Agent 失败")
	}
	if isBuiltin {
		return errors.E(errors.CodeConflict, "该 Agent 为平台内置智能体，不可修改（仅用户自建 Agent 可编辑/发布/删除）")
	}
	return nil
}

// ListAgents 返回租户 Agent 列表（按 agent_key 升序 cursor 分页，支持 ?status= 与 ?q=）。
// 软删除（status=DELETED）默认不出现在列表，除非显式按 status=DELETED 筛选。
func (s *Store) ListAgents(ctx context.Context, limit int, cursor, status, q string) (*ListResult, error) {
	from, err := decKey(cursor)
	if err != nil {
		return nil, err
	}
	res := &ListResult{Items: []map[string]any{}}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, listAgentsSQL, nullableStr(from), limit+1, status, q)
		if err != nil {
			return err
		}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}
		res.Items = rows
		if hasMore && len(rows) > 0 {
			if k, ok := rows[len(rows)-1]["agent_key"].(string); ok {
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

// CreateAgentInput 是创建 Agent 的输入。
type CreateAgentInput struct {
	AgentKey     string
	Domain       string
	Description  string
	Version      string
	InputSchema  []byte
	OutputSchema []byte
	// 以下三项可选：AI 自动生成 Agent 时由模型产出（提示词/运行配置/依赖工具清单）。
	// 为空时保持表默认值 '{}'，不影响人工创建路径。
	PromptTemplate     []byte
	ConfigJSON         []byte
	DependencyManifest []byte
}

// CreateAgent 创建 Agent 定义与首个 DRAFT 版本（含其所属插件与插件版本）。
func (s *Store) CreateAgent(ctx context.Context, in CreateAgentInput) (map[string]any, error) {
	if in.AgentKey == "" || in.Domain == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "agent_key 与 domain 必填")
	}
	if !json.Valid(in.InputSchema) || !json.Valid(in.OutputSchema) {
		return nil, errors.E(errors.CodeInvalidArgument, "input_schema/output_schema 必须是合法 JSON")
	}
	createdBy, err := subjectFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if in.Version == "" {
		in.Version = "0.1.0"
	}
	out := map[string]any{}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		manifest, _ := json.Marshal(map[string]any{"kind": "AGENT", "agent_key": in.AgentKey})
		pluginID, err := insertPlainPlugin(ctx, tx, "agent:"+in.AgentKey, "AGENT", in.AgentKey, manifest, createdBy)
		if err != nil {
			return err
		}
		var pluginVersionID string
		if err := tx.QueryRow(ctx, `
            SELECT id::text FROM registry.plugin_versions WHERE plugin_id = $1 ORDER BY created_at DESC LIMIT 1`,
			pluginID).Scan(&pluginVersionID); err != nil {
			return errors.Wrap(err, errors.CodeInternal, "读取插件版本失败")
		}
		agentID := ids.New().String()
		if _, err := tx.Exec(ctx, `
            INSERT INTO registry.agents (id, tenant_id, plugin_id, agent_key, domain, description)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5)`,
			agentID, pluginID, in.AgentKey, in.Domain, in.Description); err != nil {
			return errors.Wrap(err, errors.CodeConflict, "agent_key 已存在")
		}
		prompt, _ := optionalJSON(in.PromptTemplate)
		cfg, _ := optionalJSON(in.ConfigJSON)
		deps, _ := optionalJSON(in.DependencyManifest)
		content, _ := json.Marshal(map[string]any{
			"input": json.RawMessage(in.InputSchema), "output": json.RawMessage(in.OutputSchema),
			"prompt": json.RawMessage(prompt), "config": json.RawMessage(cfg), "dependencies": json.RawMessage(deps),
		})
		versionID := ids.New().String()
		if _, err := tx.Exec(ctx, `
            INSERT INTO registry.agent_versions
                (id, tenant_id, agent_id, plugin_version_id, version, input_schema, output_schema,
                 prompt_template, config, dependency_manifest, content_digest, status)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5::jsonb, $6::jsonb,
                    $7::jsonb, $8::jsonb, $9::jsonb, $10, 'DRAFT')`,
			versionID, agentID, pluginVersionID, in.Version, string(in.InputSchema), string(in.OutputSchema),
			string(prompt), string(cfg), string(deps), digestBytes(content)); err != nil {
			return errors.Wrap(err, errors.CodeConflict, "agent 版本已存在或写入失败")
		}
		out["id"] = agentID
		out["agent_key"] = in.AgentKey
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

// PublishAgent 把指定 Agent 的最新版本置为 PUBLISHED。
func (s *Store) PublishAgent(ctx context.Context, agentID ids.ID, version string) (map[string]any, error) {
	out := map[string]any{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		if err := ensureAgentMutable(ctx, tx, agentID); err != nil {
			return err
		}
		var versionID, newStatus string
		if err := tx.QueryRow(ctx, `
            UPDATE registry.agent_versions
               SET status = 'PUBLISHED', published_at = now()
             WHERE agent_id = $1 AND ($2::text IS NULL OR version = $2)
               AND status IN ('DRAFT','VALIDATING','APPROVED')
               AND id = (SELECT id FROM registry.agent_versions
                          WHERE agent_id = $1 ORDER BY created_at DESC LIMIT 1)
            RETURNING id::text, status`, agentID.String(), nullableStr(version)).Scan(&versionID, &newStatus); err != nil {
			if err == pgx.ErrNoRows {
				return errors.E(errors.CodeConflict, "没有可发布的 Agent 版本（状态或版本不匹配）")
			}
			return errors.Wrap(err, errors.CodeInternal, "发布 Agent 失败")
		}
		out["agent_id"] = agentID.String()
		out["version_id"] = versionID
		out["status"] = newStatus
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Plugins
// ---------------------------------------------------------------------------

// ListPlugins 返回平台级与租户级插件列表。
func (s *Store) ListPlugins(ctx context.Context, limit int, cursor string) (*ListResult, error) {
	from, err := decKey(cursor)
	if err != nil {
		return nil, err
	}
	res := &ListResult{Items: []map[string]any{}}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT p.id::text AS id, p.plugin_key, p.plugin_kind, p.name, p.description, p.status,
                   (p.tenant_id IS NULL) AS is_platform,
                   i.installation_state
              FROM registry.plugins p
              LEFT JOIN registry.plugin_installations i
                     ON i.plugin_id = p.id AND i.installation_state = 'ACTIVE'
             WHERE ($1::text IS NULL OR p.plugin_key > $1)
             ORDER BY p.plugin_key ASC
             LIMIT $2`, nullableStr(from), limit+1)
		if err != nil {
			return err
		}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}
		res.Items = rows
		if hasMore && len(rows) > 0 {
			if k, ok := rows[len(rows)-1]["plugin_key"].(string); ok {
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

// CreatePluginInput 是注册插件的输入。
type CreatePluginInput struct {
	PluginKey   string
	PluginKind  string
	Name        string
	Description string
	Version     string
	Manifest    []byte
}

var validPluginKinds = map[string]bool{
	"AGENT": true, "ONTOLOGY": true, "TOOL": true, "POLICY": true, "MODEL_PROVIDER": true,
}

// CreatePlugin 注册插件及其首个版本（DRAFT）。
func (s *Store) CreatePlugin(ctx context.Context, in CreatePluginInput) (map[string]any, error) {
	if in.PluginKey == "" || in.Name == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "plugin_key 与 name 必填")
	}
	if !validPluginKinds[in.PluginKind] {
		return nil, errors.E(errors.CodeInvalidArgument, "plugin_kind 不在契约枚举内")
	}
	if len(in.Manifest) == 0 || !json.Valid(in.Manifest) {
		return nil, errors.E(errors.CodeInvalidArgument, "manifest 必须是合法 JSON")
	}
	if in.Version == "" {
		in.Version = "0.1.0"
	}
	createdBy, err := subjectFromContext(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		pluginID := ids.New().String()
		if _, err := tx.Exec(ctx, `
            INSERT INTO registry.plugins (id, tenant_id, plugin_key, plugin_kind, name, description)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5)`,
			pluginID, in.PluginKey, in.PluginKind, in.Name, in.Description); err != nil {
			return errors.Wrap(err, errors.CodeConflict, "plugin_key 已存在")
		}
		versionID := ids.New().String()
		if _, err := tx.Exec(ctx, `
            INSERT INTO registry.plugin_versions
                (id, tenant_id, plugin_id, version, manifest, artifact_digest, status, created_by)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4::jsonb, $5, 'CREATED', $6)`,
			versionID, pluginID, in.Version, string(in.Manifest), digestBytes(in.Manifest), createdBy.String()); err != nil {
			return errors.Wrap(err, errors.CodeConflict, "插件版本已存在或写入失败")
		}
		out["id"] = pluginID
		out["plugin_key"] = in.PluginKey
		out["plugin_kind"] = in.PluginKind
		out["version_id"] = versionID
		out["status"] = "DRAFT"
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// EnablePlugin 为当前租户启用插件（安装记录置 ACTIVE，幂等）。
func (s *Store) EnablePlugin(ctx context.Context, pluginID ids.ID, configJSON []byte) (map[string]any, error) {
	if pluginID.IsZero() {
		return nil, errors.E(errors.CodeInvalidArgument, "plugin_id 不合法")
	}
	if len(configJSON) == 0 {
		configJSON = []byte("{}")
	}
	if !json.Valid(configJSON) {
		return nil, errors.E(errors.CodeInvalidArgument, "config 必须是合法 JSON")
	}
	installedBy, err := subjectFromContext(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		var versionID string
		if err := tx.QueryRow(ctx, `
            SELECT id::text FROM registry.plugin_versions
             WHERE plugin_id = $1 AND status IN ('CREATED','VALIDATING','TESTING','STAGED','APPROVED','PUBLISHED')
             ORDER BY created_at DESC LIMIT 1`, pluginID.String()).Scan(&versionID); err != nil {
			if err == pgx.ErrNoRows {
				return errors.E(errors.CodeNotFound, "插件不存在或没有可用版本")
			}
			return errors.Wrap(err, errors.CodeInternal, "读取插件版本失败")
		}
		if _, err := tx.Exec(ctx, `
            INSERT INTO registry.plugin_installations
                (id, tenant_id, plugin_id, plugin_version_id, installation_state, config_json, installed_by)
            VALUES ($1, app.current_tenant_id(), $2, $3, 'ACTIVE', $4::jsonb, $5)
            ON CONFLICT (tenant_id, plugin_id, plugin_version_id)
            DO UPDATE SET installation_state = 'ACTIVE', updated_at = now()`,
			ids.New().String(), pluginID.String(), versionID, string(configJSON), installedBy.String()); err != nil {
			return errors.Wrap(err, errors.CodeInternal, "启用插件失败")
		}
		out["plugin_id"] = pluginID.String()
		out["plugin_version_id"] = versionID
		out["installation_state"] = "ACTIVE"
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Tools
// ---------------------------------------------------------------------------

var validEffectClass = map[string]bool{"READ": true, "EXTERNAL_WRITE": true, "INTERNAL_WRITE": true}

// ensureToolMutable 拦截对**平台内置工具**的修改。
//
// 内置工具（文档读取 / 本体校验 / 图谱装载 / 模型调用治理网关 / ERP 适配 /
// 审计链 / 证据哈希链）是平台自身能力，登记在工具管理页供查看，但**不允许**
// 编辑或删除。此校验放在 store 层而非仅前端隐藏按钮：绕过界面直接打 API
// 同样必须被拒，不能只靠 UI。
//
// 返回 nil 表示可改；返回 409 Conflict 表示内置项不可改；无该行时返回 NotFound。
func ensureToolMutable(ctx context.Context, tx pgx.Tx, toolID ids.ID) error {
	var isBuiltin bool
	err := tx.QueryRow(ctx,
		`SELECT is_builtin FROM registry.tools WHERE id = $1 AND status <> 'DELETED'`,
		toolID.String()).Scan(&isBuiltin)
	if err != nil {
		if err == pgx.ErrNoRows {
			return errors.E(errors.CodeNotFound, "工具不存在")
		}
		return errors.Wrap(err, errors.CodeInternal, "读取工具失败")
	}
	if isBuiltin {
		return errors.E(errors.CodeConflict, "该工具为平台内置工具，不可修改（仅用户自建工具可编辑/删除）")
	}
	return nil
}

// ListTools 返回租户工具列表（cursor 分页 + effect_class/status/q 筛选）。
func (s *Store) ListTools(ctx context.Context, limit int, cursor, effectClass, status, q string) (*ListResult, error) {
	if effectClass != "" && !validEffectClass[effectClass] {
		return nil, errors.E(errors.CodeInvalidArgument, "effect_class 不在契约枚举内")
	}
	from, err := decKey(cursor)
	if err != nil {
		return nil, err
	}
	res := &ListResult{Items: []map[string]any{}}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT t.id::text AS id, t.tool_key, t.description, t.status, t.is_builtin,
                   v.id::text AS latest_version_id, v.version AS latest_version,
                   v.effect_class, v.status AS latest_version_status
              FROM registry.tools t
              LEFT JOIN LATERAL (
                  SELECT id, version, effect_class, status FROM registry.tool_versions
                   WHERE tool_id = t.id ORDER BY created_at DESC LIMIT 1
              ) v ON TRUE
             WHERE ($1::text IS NULL OR t.tool_key > $1)
               AND ($2::text IS NULL OR v.effect_class = $2)
               AND (($4::text = '' AND t.status <> 'DELETED') OR ($4::text <> '' AND t.status = $4::text))
               AND ($5::text = '' OR t.tool_key ILIKE '%' || $5 || '%' OR coalesce(t.description, '') ILIKE '%' || $5 || '%')
             ORDER BY t.tool_key ASC
             LIMIT $3`, nullableStr(from), nullableStr(effectClass), limit+1, status, q)
		if err != nil {
			return err
		}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}
		res.Items = rows
		if hasMore && len(rows) > 0 {
			if k, ok := rows[len(rows)-1]["tool_key"].(string); ok {
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

// CreateToolInput 是创建工具的输入。
type CreateToolInput struct {
	ToolKey      string
	Description  string
	Version      string
	InputSchema  []byte
	OutputSchema []byte
	Capabilities []string
	EffectClass  string
}

// CreateTool 创建工具定义与首个 DRAFT 版本。
func (s *Store) CreateTool(ctx context.Context, in CreateToolInput) (map[string]any, error) {
	if in.ToolKey == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "tool_key 必填")
	}
	if !json.Valid(in.InputSchema) || !json.Valid(in.OutputSchema) {
		return nil, errors.E(errors.CodeInvalidArgument, "input_schema/output_schema 必须是合法 JSON")
	}
	if in.EffectClass == "" {
		in.EffectClass = "READ"
	}
	if !validEffectClass[in.EffectClass] {
		return nil, errors.E(errors.CodeInvalidArgument, "effect_class 不在契约枚举内")
	}
	if in.Version == "" {
		in.Version = "0.1.0"
	}
	createdBy, err := subjectFromContext(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		manifest, _ := json.Marshal(map[string]any{"kind": "TOOL", "tool_key": in.ToolKey})
		pluginID, err := insertPlainPlugin(ctx, tx, "tool:"+in.ToolKey, "TOOL", in.ToolKey, manifest, createdBy)
		if err != nil {
			return err
		}
		var pluginVersionID string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM registry.plugin_versions WHERE plugin_id = $1 LIMIT 1`,
			pluginID).Scan(&pluginVersionID); err != nil {
			return errors.Wrap(err, errors.CodeInternal, "读取插件版本失败")
		}
		toolID := ids.New().String()
		if _, err := tx.Exec(ctx, `
            INSERT INTO registry.tools (id, tenant_id, plugin_id, tool_key, description)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4)`,
			toolID, pluginID, in.ToolKey, in.Description); err != nil {
			return errors.Wrap(err, errors.CodeConflict, "tool_key 已存在")
		}
		content, _ := json.Marshal(map[string]any{"input": json.RawMessage(in.InputSchema), "output": json.RawMessage(in.OutputSchema)})
		versionID := ids.New().String()
		caps := in.Capabilities
		if caps == nil {
			caps = []string{}
		}
		if _, err := tx.Exec(ctx, `
            INSERT INTO registry.tool_versions
                (id, tenant_id, tool_id, plugin_version_id, version, input_schema, output_schema,
                 capabilities, effect_class, content_digest, status)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5::jsonb, $6::jsonb, $7, $8, $9, 'DRAFT')`,
			versionID, toolID, pluginVersionID, in.Version, string(in.InputSchema), string(in.OutputSchema),
			caps, in.EffectClass, digestBytes(content)); err != nil {
			return errors.Wrap(err, errors.CodeConflict, "工具版本已存在或写入失败")
		}
		out["id"] = toolID
		out["tool_key"] = in.ToolKey
		out["version_id"] = versionID
		out["effect_class"] = in.EffectClass
		out["status"] = "DRAFT"
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// nullableStr 把空串转为 NULL（供 SQL 可选参数使用）。
func nullableStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nullableBytes 把空/零长字节切片转为 NULL（供密文列使用）。
func nullableBytes(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}

// optionalJSON 把可选 JSON 载荷规范化为非空字节；为空或非法时回退为 '{}'。
// 依赖列在库里有 NOT NULL DEFAULT '{}'，因此这里必须给出合法 JSON 而不是空串。
func optionalJSON(raw []byte) ([]byte, bool) {
	if len(raw) == 0 || !json.Valid(raw) {
		return []byte("{}"), false
	}
	return raw, true
}
