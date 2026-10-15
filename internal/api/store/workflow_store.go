package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// ---------------------------------------------------------------------------
// Workflows（插件式：workflow 作为 plugin_kind='WORKFLOW' 的插件资产）
//
// 与 registry.agents / registry.tools 完全同构：定义表 + 版本表，
// 版本不可变性由迁移 000018 的触发器统一保证。
// ---------------------------------------------------------------------------

const listWorkflowsSQL = `
SELECT w.id::text AS id, w.workflow_key, w.name, w.description, w.status, w.is_builtin,
       v.id::text AS latest_version_id, v.version AS latest_version,
       v.status AS latest_version_status
  FROM registry.workflows w
  LEFT JOIN LATERAL (
      SELECT id, version, status FROM registry.workflow_versions
       WHERE workflow_id = w.id ORDER BY created_at DESC LIMIT 1
  ) v ON TRUE
 WHERE ($1::text IS NULL OR w.workflow_key > $1)
   AND (($3::text = '' AND w.status <> 'DELETED') OR ($3::text <> '' AND w.status = $3::text))
   AND ($4::text = '' OR w.workflow_key ILIKE '%' || $4 || '%' OR coalesce(w.name, '') ILIKE '%' || $4 || '%' OR coalesce(w.description, '') ILIKE '%' || $4 || '%')
 ORDER BY w.workflow_key ASC
 LIMIT $2`

// ensureWorkflowMutable 拦截对**平台内置工作流**的修改。
//
// 内置工作流（Harness 运行编排 / 对账收敛 / 本体生命周期等）是平台自身编排能力，
// 登记在「工作流与 Harness」页供查看，但**不允许**编辑、发布或删除。此校验放在
// store 层而非仅前端隐藏按钮：绕过界面直接打 API 同样必须被拒，不能只靠 UI。
//
// 返回 nil 表示可改；返回 409 Conflict 表示内置项不可改；无该行时返回 NotFound。
func ensureWorkflowMutable(ctx context.Context, tx pgx.Tx, workflowID ids.ID) error {
	var isBuiltin bool
	err := tx.QueryRow(ctx,
		`SELECT is_builtin FROM registry.workflows WHERE id = $1 AND status <> 'DELETED'`,
		workflowID.String()).Scan(&isBuiltin)
	if err != nil {
		if err == pgx.ErrNoRows {
			return errors.E(errors.CodeNotFound, "工作流不存在")
		}
		return errors.Wrap(err, errors.CodeInternal, "读取工作流失败")
	}
	if isBuiltin {
		return errors.E(errors.CodeConflict, "该工作流为平台内置工作流，不可修改（仅用户自建工作流可编辑/发布/删除）")
	}
	return nil
}

// ListWorkflows 返回租户工作流列表（按 workflow_key 升序 cursor 分页，支持 ?status= 与 ?q=）。
// 软删除（status=DELETED）默认不出现在列表，除非显式按 status=DELETED 筛选。
func (s *Store) ListWorkflows(ctx context.Context, limit int, cursor, status, q string) (*ListResult, error) {
	from, err := decKey(cursor)
	if err != nil {
		return nil, err
	}
	res := &ListResult{Items: []map[string]any{}}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, listWorkflowsSQL, nullableStr(from), limit+1, status, q)
		if err != nil {
			return err
		}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}
		res.Items = rows
		if hasMore && len(rows) > 0 {
			if k, ok := rows[len(rows)-1]["workflow_key"].(string); ok {
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

// CreateWorkflowInput 是创建工作流的输入。
type CreateWorkflowInput struct {
	WorkflowKey  string
	Name         string
	Description  string
	Version      string
	Definition   []byte // 编排文档（节点/边/触发器）JSON
	InputSchema  []byte
	OutputSchema []byte
}

// CreateWorkflow 创建工作流定义与首个 DRAFT 版本（含其所属插件与插件版本）。
func (s *Store) CreateWorkflow(ctx context.Context, in CreateWorkflowInput) (map[string]any, error) {
	if in.WorkflowKey == "" || in.Name == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "workflow_key 与 name 必填")
	}
	if !json.Valid(in.Definition) {
		return nil, errors.E(errors.CodeInvalidArgument, "definition 必须是合法 JSON")
	}
	if len(in.InputSchema) == 0 {
		in.InputSchema = []byte("{}")
	}
	if len(in.OutputSchema) == 0 {
		in.OutputSchema = []byte("{}")
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
		manifest, _ := json.Marshal(map[string]any{"kind": "WORKFLOW", "workflow_key": in.WorkflowKey})
		pluginID, err := insertPlainPlugin(ctx, tx, "workflow:"+in.WorkflowKey, "WORKFLOW", in.Name, manifest, createdBy)
		if err != nil {
			return err
		}
		var pluginVersionID string
		if err := tx.QueryRow(ctx, `
            SELECT id::text FROM registry.plugin_versions WHERE plugin_id = $1 ORDER BY created_at DESC LIMIT 1`,
			pluginID).Scan(&pluginVersionID); err != nil {
			return errors.Wrap(err, errors.CodeInternal, "读取插件版本失败")
		}
		workflowID := ids.New().String()
		if _, err := tx.Exec(ctx, `
            INSERT INTO registry.workflows (id, tenant_id, plugin_id, workflow_key, name, description)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5)`,
			workflowID, pluginID, in.WorkflowKey, in.Name, in.Description); err != nil {
			return errors.Wrap(err, errors.CodeConflict, "workflow_key 已存在")
		}
		content, _ := json.Marshal(map[string]any{
			"definition": json.RawMessage(in.Definition),
			"input":      json.RawMessage(in.InputSchema),
			"output":     json.RawMessage(in.OutputSchema),
		})
		versionID := ids.New().String()
		if _, err := tx.Exec(ctx, `
            INSERT INTO registry.workflow_versions
                (id, tenant_id, workflow_id, plugin_version_id, version, definition,
                 input_schema, output_schema, content_digest, status)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5::jsonb, $6::jsonb, $7::jsonb, $8, 'DRAFT')`,
			versionID, workflowID, pluginVersionID, in.Version, string(in.Definition),
			string(in.InputSchema), string(in.OutputSchema), digestBytes(content)); err != nil {
			return errors.Wrap(err, errors.CodeConflict, "工作流版本已存在或写入失败")
		}
		out["id"] = workflowID
		out["workflow_key"] = in.WorkflowKey
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

// GetWorkflow 返回单个工作流定义及其最新版本（含 definition）。
func (s *Store) GetWorkflow(ctx context.Context, workflowID ids.ID) (map[string]any, error) {
	out := map[string]any{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT w.id::text AS id, w.workflow_key, w.name, w.description, w.status, w.is_builtin,
                   v.id::text AS latest_version_id, v.version AS latest_version,
                   v.status AS latest_version_status, v.definition
              FROM registry.workflows w
              LEFT JOIN LATERAL (
                  SELECT id, version, status, definition FROM registry.workflow_versions
                   WHERE workflow_id = w.id ORDER BY created_at DESC LIMIT 1
              ) v ON TRUE
             WHERE w.id = $1`, workflowID.String())
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errors.Ef(errors.CodeNotFound, "工作流 %s 不存在", workflowID)
		}
		out = rows[0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateWorkflow 更新工作流的元数据（workflow_key 不可变，版本升级走 publish）。
func (s *Store) UpdateWorkflow(ctx context.Context, workflowID ids.ID, name, description string) (map[string]any, error) {
	out := map[string]any{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		if err := ensureWorkflowMutable(ctx, tx, workflowID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
            UPDATE registry.workflows
               SET name = COALESCE(NULLIF($2, ''), name),
                   description = COALESCE(NULLIF($3, ''), description)
             WHERE id = $1 AND status <> 'DELETED'`,
			workflowID.String(), nullableStr(name), nullableStr(description))
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "更新工作流失败")
		}
		if tag.RowsAffected() == 0 {
			return errors.E(errors.CodeNotFound, "工作流不存在")
		}
		rows, err := queryMaps(ctx, tx, `
            SELECT id::text AS id, workflow_key, name, description, status
              FROM registry.workflows WHERE id = $1`, workflowID.String())
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

// PublishWorkflow 把指定工作流的最新版本置为 PUBLISHED。
func (s *Store) PublishWorkflow(ctx context.Context, workflowID ids.ID, version string) (map[string]any, error) {
	out := map[string]any{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		if err := ensureWorkflowMutable(ctx, tx, workflowID); err != nil {
			return err
		}
		var versionID, newStatus string
		if err := tx.QueryRow(ctx, `
            UPDATE registry.workflow_versions
               SET status = 'PUBLISHED', published_at = now()
             WHERE workflow_id = $1 AND ($2::text IS NULL OR version = $2)
               AND status IN ('DRAFT','VALIDATING','APPROVED')
               AND id = (SELECT id FROM registry.workflow_versions
                          WHERE workflow_id = $1 ORDER BY created_at DESC LIMIT 1)
            RETURNING id::text, status`, workflowID.String(), nullableStr(version)).Scan(&versionID, &newStatus); err != nil {
			if err == pgx.ErrNoRows {
				return errors.E(errors.CodeConflict, "没有可发布的工作流版本（状态或版本不匹配）")
			}
			return errors.Wrap(err, errors.CodeInternal, "发布工作流失败")
		}
		out["workflow_id"] = workflowID.String()
		out["version_id"] = versionID
		out["status"] = newStatus
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteWorkflow 软删除工作流（status → DELETED，保持审计可追溯）。
func (s *Store) DeleteWorkflow(ctx context.Context, workflowID ids.ID) error {
	return s.TenantTx(ctx, func(tx pgx.Tx) error {
		if err := ensureWorkflowMutable(ctx, tx, workflowID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`UPDATE registry.workflows SET status = 'DELETED'
              WHERE id = $1 AND status <> 'DELETED'`, workflowID.String())
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "删除工作流失败")
		}
		if tag.RowsAffected() == 0 {
			return errors.Ef(errors.CodeNotFound, "工作流 %s 不存在或已删除", workflowID)
		}
		return nil
	})
}
