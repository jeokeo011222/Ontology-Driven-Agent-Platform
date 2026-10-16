package store

// reason schema 的 HTTP 层持久化适配器：推理规则（reason.rules）与
// 推理运行记录（reason.inference_runs）。
//
// 与 internal/reasoning.Service 的分工：本文件只做行存取与列表游标，
// 规则体（body）的**结构校验**单一真值在 reasoning.ParseRuleBody——
// HTTP 注册路径必须先过严格校验再落库，避免损坏的规则体进入推理引擎。

import (
	"context"

	"github.com/jackc/pgx/v5"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/reasoning"
)

// 规则 kind 的合法筛选值。
var ruleKindFilter = map[string]bool{
	reasoning.KindRDFS:   true,
	reasoning.KindOWL2RL: true,
	reasoning.KindCUSTOM: true,
}

// RulePatch 是规则更新载荷（指针语义：nil 表示不更新该字段）。
type RulePatch struct {
	Enabled     *bool
	Name        *string
	Description *string
}

// ListRules 分页列出规则（cursor 10/页，kind 筛选）。
//
// 内置规则惰性物化：规则本体由代码承载（reasoning.BuiltinRules），DB 只存
// 启停开关行。列表访问时补齐缺失的开关行（ON CONFLICT DO NOTHING，幂等），
// 使内置规则在规则页可见、可按行启停（PATCH 走同一行）——没有 DB 行的
// 内置规则对用户不可见，启停也无从谈起。
func (s *Store) ListRules(ctx context.Context, limit int, cursor, kind string) (*ListResult, error) {
	if kind != "" && !ruleKindFilter[kind] {
		return nil, errors.E(errors.CodeInvalidArgument, "kind 筛选值不在枚举内（RDFS | OWL2RL | CUSTOM）")
	}
	n, err := NormalizeLimit(limit, 10)
	if err != nil {
		return nil, err
	}
	c, err := DecodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		// 内置规则开关行补齐：仅当 DB 行数与代码内置集不一致时执行写入
		// （新增内置规则版本升级、首次访问两种场景），常态列表零写入。
		builtins := reasoning.BuiltinRules()
		var have int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM reason.rules WHERE is_builtin`).Scan(&have); err != nil {
			return errors.Wrap(err, errors.CodeInternal, "统计内置规则开关行失败")
		}
		if have != len(builtins) {
			keys := make([]string, len(builtins))
			names := make([]string, len(builtins))
			kinds := make([]string, len(builtins))
			descs := make([]string, len(builtins))
			for i, b := range builtins {
				keys[i], names[i], kinds[i], descs[i] = b.RuleKey, b.Name, b.Kind, b.Description
			}
			if _, err := tx.Exec(ctx, `
                WITH src(rule_key, name, kind, description) AS (
                    SELECT * FROM unnest($1::text[], $2::text[], $3::text[], $4::text[])
                )
                INSERT INTO reason.rules
                    (id, tenant_id, rule_key, name, kind, body, description, enabled, is_builtin, created_by)
                SELECT gen_random_uuid(), app.current_tenant_id(),
                       src.rule_key, src.name, src.kind, '{}'::jsonb, src.description, TRUE, TRUE, NULL
                  FROM src
                 WHERE NOT EXISTS (
                    SELECT 1 FROM reason.rules r
                     WHERE r.tenant_id = app.current_tenant_id() AND r.rule_key = src.rule_key)
                ON CONFLICT (tenant_id, rule_key) DO NOTHING`,
				keys, names, kinds, descs); err != nil {
				return errors.Wrap(err, errors.CodeInternal, "补齐内置规则开关行失败")
			}
		}

		var qerr error
		rows, qerr = queryMaps(ctx, tx, `
            SELECT id::text, rule_key, name, kind, body::text AS body, description,
                   enabled, is_builtin, created_by::text AS created_by, created_at, updated_at
              FROM reason.rules
             WHERE ($1::timestamptz IS NULL OR (created_at, id) < ($1::timestamptz, $2::uuid))
               AND ($3::text = '' OR kind = $3::text)
             ORDER BY created_at DESC, id DESC
             LIMIT $4`,
			cursorTime(c), cursorKey(c), kind, n+1)
		return qerr
	})
	if err != nil {
		return nil, err
	}
	return cursorPage(rows, n, "created_at", "id")
}

// GetRule 按 ID 取单条规则。
func (s *Store) GetRule(ctx context.Context, id ids.ID) (map[string]any, error) {
	var rows []map[string]any
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		var qerr error
		rows, qerr = queryMaps(ctx, tx, `
            SELECT id::text, rule_key, name, kind, body::text AS body, description,
                   enabled, is_builtin, created_by::text AS created_by, created_at, updated_at
              FROM reason.rules
             WHERE id = $1`, id.String())
		return qerr
	})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.E(errors.CodeNotFound, "规则不存在")
	}
	return rows[0], nil
}

// CreateCustomRule 注册自定义规则（kind=CUSTOM、is_builtin=false）。
// body 为原始 JSON，先过 reasoning.ParseRuleBody 的 DisallowUnknownFields
// 严格校验（仅允许 rule_key/name/kind/premises/conclusion/description）。
func (s *Store) CreateCustomRule(ctx context.Context, body []byte, createdBy ids.ID) (map[string]any, error) {
	rule, err := reasoning.ParseRuleBody(body)
	if err != nil {
		return nil, err
	}
	if rule.Kind != reasoning.KindCUSTOM {
		return nil, errors.E(errors.CodeInvalidArgument, "注册端仅接受 kind=CUSTOM 的自定义规则")
	}
	for _, k := range reasoning.BuiltinRuleKeys() {
		if k == rule.RuleKey {
			return nil, errors.E(errors.CodeConflict, "rule_key 与内置规则冲突")
		}
	}

	var out map[string]any
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, qerr := queryMaps(ctx, tx, `
            INSERT INTO reason.rules (id, tenant_id, rule_key, name, kind, body, description, enabled, is_builtin, created_by)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5::jsonb, $6, TRUE, FALSE, $7)
            RETURNING id::text, rule_key, name, kind, body::text AS body, description,
                      enabled, is_builtin, created_by::text AS created_by, created_at, updated_at`,
			ids.New().String(), rule.RuleKey, rule.Name, reasoning.KindCUSTOM, string(body), rule.Description,
			nullableID(createdBy))
		if qerr != nil {
			if isUniqueViolation(qerr) {
				return errors.E(errors.CodeConflict, "rule_key 已存在（唯一键 tenant_id+rule_key 冲突）")
			}
			return qerr
		}
		if len(rows) == 0 {
			return errors.E(errors.CodeInternal, "规则写入后未返回行")
		}
		out = rows[0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateRule 更新规则（启停/名称/描述）。内置规则仅允许 enabled 切换——
// is_builtin=true 的行携带其他字段变更一律 Conflict。
func (s *Store) UpdateRule(ctx context.Context, id ids.ID, patch RulePatch) (map[string]any, error) {
	var out map[string]any
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, qerr := queryMaps(ctx, tx,
			`SELECT is_builtin FROM reason.rules WHERE id = $1`, id.String())
		if qerr != nil {
			return qerr
		}
		if len(rows) == 0 {
			return errors.E(errors.CodeNotFound, "规则不存在")
		}
		isBuiltin, _ := rows[0]["is_builtin"].(bool)
		if isBuiltin && (patch.Name != nil || patch.Description != nil) {
			return errors.E(errors.CodeConflict, "内置规则仅允许启停（enabled），不可修改其他字段")
		}
		updated, uerr := queryMaps(ctx, tx, `
            UPDATE reason.rules
               SET enabled = COALESCE($2, enabled),
                   name = COALESCE($3, name),
                   description = COALESCE($4, description),
                   updated_at = now()
             WHERE id = $1
             RETURNING id::text, rule_key, name, kind, body::text AS body, description,
                       enabled, is_builtin, created_by::text AS created_by, created_at, updated_at`,
			id.String(), patch.Enabled, patch.Name, patch.Description)
		if uerr != nil {
			return uerr
		}
		if len(updated) == 0 {
			return errors.E(errors.CodeNotFound, "规则不存在")
		}
		out = updated[0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteRule 删除自定义规则；内置规则一律 409（仅可启停）。
func (s *Store) DeleteRule(ctx context.Context, id ids.ID) error {
	return s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, qerr := queryMaps(ctx, tx,
			`SELECT is_builtin FROM reason.rules WHERE id = $1`, id.String())
		if qerr != nil {
			return qerr
		}
		if len(rows) == 0 {
			return errors.E(errors.CodeNotFound, "规则不存在")
		}
		if isBuiltin, _ := rows[0]["is_builtin"].(bool); isBuiltin {
			return errors.E(errors.CodeConflict, "内置规则不可删除（仅可启停）")
		}
		tag, derr := tx.Exec(ctx, `DELETE FROM reason.rules WHERE id = $1`, id.String())
		if derr != nil {
			return derr
		}
		if tag.RowsAffected() == 0 {
			return errors.E(errors.CodeNotFound, "规则不存在")
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// 推理运行记录（reason.inference_runs）
// ---------------------------------------------------------------------------

// CreateInferenceRunInput 是创建运行记录的载荷。
type CreateInferenceRunInput struct {
	GraphIRI    string
	Mode        string
	RuleKeys    []string
	InputCount  int64
	TriggeredBy ids.ID
}

// ListResult / cursor 辅助定义于 store.go。

// CreateInferenceRun 记录一次推理运行（status=RUNNING）。
func (s *Store) CreateInferenceRun(ctx context.Context, in CreateInferenceRunInput) (map[string]any, error) {
	switch in.Mode {
	case "FORWARD", "BACKWARD", "INCREMENTAL":
	default:
		return nil, errors.E(errors.CodeInvalidArgument, "mode 不在枚举内（FORWARD | BACKWARD | INCREMENTAL）")
	}
	// rule_keys 列是 text[] 且 NOT NULL：pgx 按 []string 绑定（nil 必须换
	// 成空数组，NULL 会撞 NOT NULL 约束），不省略 =「未筛选规则」语义。
	ruleKeys := in.RuleKeys
	if ruleKeys == nil {
		ruleKeys = []string{}
	}
	var out map[string]any
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, qerr := queryMaps(ctx, tx, `
            INSERT INTO reason.inference_runs (id, tenant_id, graph_iri, mode, rule_keys, input_triple_count, status, triggered_by)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4::text[], $5, 'RUNNING', $6)
            RETURNING id::text, graph_iri, mode, rule_keys::text AS rule_keys,
                      input_triple_count, derived_triple_count, status, error,
                      duration_ms, triggered_by::text AS triggered_by, created_at`,
			ids.New().String(), in.GraphIRI, in.Mode, ruleKeys, in.InputCount,
			nullableID(in.TriggeredBy))
		if qerr != nil {
			return qerr
		}
		if len(rows) == 0 {
			return errors.E(errors.CodeInternal, "运行记录写入后未返回行")
		}
		out = rows[0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListInferenceRuns 分页列出推理运行（cursor 10/页）。
func (s *Store) ListInferenceRuns(ctx context.Context, limit int, cursor string) (*ListResult, error) {
	n, err := NormalizeLimit(limit, 10)
	if err != nil {
		return nil, err
	}
	c, err := DecodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		var qerr error
		rows, qerr = queryMaps(ctx, tx, `
            SELECT id::text, graph_iri, mode, rule_keys::text AS rule_keys,
                   input_triple_count, derived_triple_count, status, error,
                   duration_ms, triggered_by::text AS triggered_by, created_at
              FROM reason.inference_runs
             WHERE ($1::timestamptz IS NULL OR (created_at, id) < ($1::timestamptz, $2::uuid))
             ORDER BY created_at DESC, id DESC
             LIMIT $3`,
			cursorTime(c), cursorKey(c), n+1)
		return qerr
	})
	if err != nil {
		return nil, err
	}
	return cursorPage(rows, n, "created_at", "id")
}

// UpdateInferenceRun 收尾一次运行（status/错误/派生数/耗时）。
func (s *Store) UpdateInferenceRun(ctx context.Context, id ids.ID, status, errMsg string, derivedCount, durationMs int64) (map[string]any, error) {
	switch status {
	case "SUCCEEDED", "FAILED":
	default:
		return nil, errors.E(errors.CodeInvalidArgument, "status 不在枚举内（SUCCEEDED | FAILED）")
	}
	var out map[string]any
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, qerr := queryMaps(ctx, tx, `
            UPDATE reason.inference_runs
               SET status = $2, error = $3, derived_triple_count = $4, duration_ms = $5
             WHERE id = $1
             RETURNING id::text, graph_iri, mode, rule_keys::text AS rule_keys,
                       input_triple_count, derived_triple_count, status, error,
                       duration_ms, triggered_by::text AS triggered_by, created_at`,
			id.String(), status, errMsg, derivedCount, durationMs)
		if qerr != nil {
			return qerr
		}
		if len(rows) == 0 {
			return errors.E(errors.CodeNotFound, "推理运行不存在")
		}
		out = rows[0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 私有辅助
// ---------------------------------------------------------------------------

// cursorTime/cursorKey/nullableID 复用本包既有定义（action.go / runs.go）。

// cursorPage 按"多取一条"约定生成 ListResult。
func cursorPage(rows []map[string]any, limit int, timeCol, keyCol string) (*ListResult, error) {
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	next := ""
	if hasMore && len(rows) > 0 {
		t, k, ok := keyedRow(rows[len(rows)-1], timeCol, keyCol)
		if !ok {
			return nil, errors.E(errors.CodeInternal, "分页键列缺失")
		}
		next = nextCursorOf(t, k, hasMore)
	}
	return &ListResult{Items: rows, NextCursor: next}, nil
}
