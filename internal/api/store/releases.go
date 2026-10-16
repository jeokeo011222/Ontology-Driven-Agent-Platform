package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// 发布状态机（与前端 Release 类型对齐，见迁移 000040 注释）：
//
//	candidate --promote(gate_state=passed)--> promoted
//	promoted  --rollback-->                  rolled_back
//
// 非法流转 fail-closed 返回 409（如 candidate 直接 rollback、重复 promote）。
const (
	ReleaseStateCandidate  = "candidate"
	ReleaseStatePromoted   = "promoted"
	ReleaseStateRolledBack = "rolled_back"

	ReleaseGatePassed = "passed"
)

// ListReleases 返回发布实体列表（GET /releases 的真源：release_mgmt.releases）。
//
// 此前该方法读 quality_gate_decisions（门禁决策记录），与前端 Release 形状
// 完全脱节；000040 建立发布实体后统一到本查询。cursor 分页（created_at, id 键集）。
func (s *Store) ListReleases(ctx context.Context, limit int, cursor, q string) (*ListResult, error) {
	var fromTime, fromID any
	if cur, err := DecodeCursor(cursor); err != nil {
		return nil, err
	} else if cur != nil {
		fromTime = cur.Time
		fromID = cur.Key
	}
	res := &ListResult{Items: []map[string]any{}}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT release_id::text AS id, artifact_type, artifact_id, version,
                   state, gate_state, environment, promoted_at
              FROM release_mgmt.releases
             WHERE ($1::timestamptz IS NULL OR (created_at, release_id) < ($1::timestamptz, $2::uuid))
               AND ($3::text = '' OR artifact_type ILIKE '%' || $3 || '%'
                    OR artifact_id ILIKE '%' || $3 || '%' OR version ILIKE '%' || $3 || '%')
             ORDER BY created_at DESC, release_id DESC
             LIMIT $4`, fromTime, fromID, q, limit+1)
		if err != nil {
			return err
		}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}
		res.Items = rows
		if hasMore && len(rows) > 0 {
			if t, k, ok := keyedRow(rows[len(rows)-1], "created_at", "id"); ok {
				res.NextCursor = nextCursorOf(t, k, true)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// PromoteRelease 将 candidate 发布为 promoted（质量门禁必须 passed）。
//
// 条件 UPDATE（CAS）：状态守卫在数据库层完成，并发下不会重复发布；
// 未命中行区分「不存在」(404) 与「状态不允许」(409)。
func (s *Store) PromoteRelease(ctx context.Context, tenantID, releaseID ids.ID, environment string) (map[string]any, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}
	var out map[string]any
	err := s.TenantTxAs(ctx, tenantID, func(tx pgx.Tx) error {
		updated, err := promoteReleaseTx(ctx, tx, tenantID, releaseID, environment)
		if err != nil {
			return err
		}
		out = updated
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func promoteReleaseTx(ctx context.Context, tx pgx.Tx, tenantID, releaseID ids.ID, environment string) (map[string]any, error) {
	tag, err := tx.Exec(ctx, `
        UPDATE release_mgmt.releases
           SET state = 'promoted', promoted_at = now(), updated_at = now(),
               environment = COALESCE(NULLIF($3, ''), environment)
         WHERE release_id = $1 AND tenant_id = $2
           AND state = 'candidate' AND gate_state = 'passed'`,
		releaseID.String(), tenantID.String(), environment)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "发布失败")
	}
	if tag.RowsAffected() == 0 {
		return nil, releaseTransitionError(ctx, tx, tenantID, releaseID,
			"仅 gate_state=passed 的 candidate 可发布")
	}
	return readReleaseTx(ctx, tx, tenantID, releaseID)
}

// RollbackRelease 将 promoted 回滚为 rolled_back。
//
// targetVersion 语义（对齐前端固定传的 'previous_published'）：解析为该 artifact
// 的上一个发布实体版本号记录进 environment 备注；直接版本号则原样留痕。
// 依赖重跑的评估属于后续链路（hook 见评估报告批次 B 备注）。
func (s *Store) RollbackRelease(ctx context.Context, tenantID, releaseID ids.ID, targetVersion string) (map[string]any, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}
	var out map[string]any
	err := s.TenantTxAs(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
            UPDATE release_mgmt.releases
               SET state = 'rolled_back', updated_at = now()
             WHERE release_id = $1 AND tenant_id = $2 AND state = 'promoted'`,
			releaseID.String(), tenantID.String())
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "回滚失败")
		}
		if tag.RowsAffected() == 0 {
			return releaseTransitionError(ctx, tx, tenantID, releaseID,
				"仅 promoted 状态可回滚")
		}
		out, err = readReleaseTx(ctx, tx, tenantID, releaseID)
		if err != nil {
			return err
		}
		if targetVersion != "" {
			out["rollback_target"] = targetVersion
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// releaseTransitionError 区分 404（不存在）与 409（存在但状态机不允许）。
// 断言对象必须 == 被判对象：只看 RowsAffected 会把两种原因混成一个错误。
func releaseTransitionError(ctx context.Context, tx pgx.Tx, tenantID, releaseID ids.ID, why string) error {
	rows, err := queryMaps(ctx, tx, `
        SELECT state, gate_state FROM release_mgmt.releases
         WHERE release_id = $1 AND tenant_id = $2`, releaseID.String(), tenantID.String())
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return errors.Ef(errors.CodeNotFound, "发布记录 %s 不存在", releaseID)
	}
	return errors.Ef(errors.CodeConflict, "发布记录 %s 当前状态 %s（门禁 %s）不允许该操作：%s",
		releaseID, asString(rows[0]["state"]), asString(rows[0]["gate_state"]), why)
}

// readReleaseTx 读取单条发布实体并适配为响应形状（promoted_at 归一化为 RFC3339 由 JSON 层处理）。
func readReleaseTx(ctx context.Context, tx pgx.Tx, tenantID, releaseID ids.ID) (map[string]any, error) {
	rows, err := queryMaps(ctx, tx, `
        SELECT release_id::text AS id, artifact_type, artifact_id, version,
               state, gate_state, environment, promoted_at
          FROM release_mgmt.releases
         WHERE release_id = $1 AND tenant_id = $2`, releaseID.String(), tenantID.String())
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.Ef(errors.CodeNotFound, "发布记录 %s 不存在", releaseID)
	}
	return rows[0], nil
}
