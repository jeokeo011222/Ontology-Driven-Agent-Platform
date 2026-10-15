package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// ---------------------------------------------------------------------------
// Approvals
// ---------------------------------------------------------------------------

// ListApprovals 按 created_at 降序返回审批列表。
func (s *Store) ListApprovals(ctx context.Context, status string, limit int, cursor string) (*ListResult, error) {
	cur, err := DecodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	var curTime *time.Time
	var curID *string
	if cur != nil {
		curTime, curID = &cur.Time, &cur.Key
	}
	res := &ListResult{Items: []map[string]any{}}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT id::text AS id, run_id::text AS run_id, approval_type, status, subject,
                   payload_digest, expires_at, created_at, decided_at
              FROM workflow.approvals
             WHERE ($1::text IS NULL OR status = $1)
               AND ($2::timestamptz IS NULL OR (created_at, id) < ($2::timestamptz, $3::uuid))
             ORDER BY created_at DESC, id DESC
             LIMIT $4`, nullableStr(status), curTime, curID, limit+1)
		if err != nil {
			return err
		}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}
		res.Items = rows
		if hasMore && len(rows) > 0 {
			last := rows[len(rows)-1]
			if t, k, ok := keyedRow(last, "created_at", "id"); ok {
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

// GetApproval 读取单条审批，并附带追加式决策动作记录。
func (s *Store) GetApproval(ctx context.Context, approvalID ids.ID) (map[string]any, error) {
	if approvalID.IsZero() {
		return nil, errors.E(errors.CodeInvalidArgument, "approval_id 不合法")
	}
	out := map[string]any{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT id::text AS id, run_id::text AS run_id, approval_type, status, subject,
                   payload_digest, policy_version_refs::text, expires_at, created_at, decided_at
              FROM workflow.approvals WHERE id = $1`, approvalID.String())
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errors.E(errors.CodeNotFound, "审批不存在或不在可见范围")
		}
		out = rows[0]
		actions, err := queryMaps(ctx, tx, `
            SELECT id::text AS id, actor_id::text AS actor_id, decision, comment,
                   payload_digest, created_at
              FROM workflow.approval_actions
             WHERE approval_id = $1 ORDER BY created_at ASC`, approvalID.String())
		if err != nil {
			return err
		}
		if actions == nil {
			actions = []map[string]any{}
		}
		out["actions"] = actions
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DecideApproval 记录审批决策（追加式），并把审批状态推进为 APPROVED/REJECTED。
//
// expectedDigest 非空时作为 If-Match 前置条件校验：与审批当前 payload_digest
// 不一致则返回 CONFLICT，避免基于过期快照决策。
func (s *Store) DecideApproval(ctx context.Context, approvalID ids.ID, decision, comment, expectedDigest string) (map[string]any, error) {
	if approvalID.IsZero() {
		return nil, errors.E(errors.CodeInvalidArgument, "approval_id 不合法")
	}
	var newStatus string
	switch decision {
	case "APPROVE":
		newStatus = "APPROVED"
	case "REJECT":
		newStatus = "REJECTED"
	default:
		return nil, errors.E(errors.CodeInvalidArgument, "decision 必须是 APPROVE 或 REJECT")
	}
	actorID, err := subjectFromContext(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		var (
			payloadDigest string
			requestedBy   string
		)
		if err := tx.QueryRow(ctx, `
            SELECT payload_digest, requested_by::text FROM workflow.approvals
             WHERE id = $1 FOR UPDATE`, approvalID.String()).Scan(&payloadDigest, &requestedBy); err != nil {
			if err == pgx.ErrNoRows {
				return errors.E(errors.CodeNotFound, "审批不存在或不在可见范围")
			}
			return errors.Wrap(err, errors.CodeInternal, "读取审批失败")
		}
		// 禁止自我审批（第三篇 / 第二篇 §7 的职责分离要求）。
		if requestedBy == actorID.String() {
			return errors.E(errors.CodePermissionDenied, "禁止自我审批")
		}
		if expectedDigest != "" && expectedDigest != payloadDigest {
			return errors.E(errors.CodeConflict, "If-Match 版本前置条件不满足").
				WithDetails(map[string]any{"current_version": payloadDigest})
		}
		tag, err := tx.Exec(ctx, `
            UPDATE workflow.approvals
               SET status = $2, decided_at = now()
             WHERE id = $1 AND status = 'PENDING'`, approvalID.String(), newStatus)
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "更新审批状态失败")
		}
		if tag.RowsAffected() == 0 {
			return errors.E(errors.CodeConflict, "审批不处于 PENDING 状态，无法决策")
		}
		if _, err := tx.Exec(ctx, `
            INSERT INTO workflow.approval_actions
                (id, tenant_id, approval_id, actor_id, decision, comment, payload_digest)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5, $6)`,
			ids.New().String(), approvalID.String(), actorID.String(), decision, comment, payloadDigest); err != nil {
			return errors.Wrap(err, errors.CodeInternal, "写入审批动作失败")
		}
		out["id"] = approvalID.String()
		out["status"] = newStatus
		out["decision"] = decision
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Evaluations
// ---------------------------------------------------------------------------

// ListEvaluations 按 created_at 降序返回评估运行列表。
func (s *Store) ListEvaluations(ctx context.Context, status string, limit int, cursor string) (*ListResult, error) {
	cur, err := DecodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	var curTime *time.Time
	var curID *string
	if cur != nil {
		curTime, curID = &cur.Time, &cur.Key
	}
	res := &ListResult{Items: []map[string]any{}}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT evaluation_run_id::text AS id, subject_type, subject_id, subject_revision,
                   dataset_revision_id::text AS dataset_revision_id, suite_revision,
                   environment_digest, status, started_at, completed_at, created_at
              FROM evaluation.runs
             WHERE ($1::text IS NULL OR status = $1)
               AND ($2::timestamptz IS NULL OR (created_at, evaluation_run_id) < ($2::timestamptz, $3::uuid))
             ORDER BY created_at DESC, evaluation_run_id DESC
             LIMIT $4`, nullableStr(status), curTime, curID, limit+1)
		if err != nil {
			return err
		}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}
		res.Items = rows
		if hasMore && len(rows) > 0 {
			last := rows[len(rows)-1]
			if t, k, ok := keyedRow(last, "created_at", "id"); ok {
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

// CreateEvaluationInput 是创建评估运行的输入。
type CreateEvaluationInput struct {
	SubjectType       string
	SubjectID         string
	SubjectRevision   string
	DatasetRevisionID ids.ID
	SuiteRevision     string
	EnvironmentDigest string
}

// CreateEvaluation 创建评估运行（QUEUED）。
func (s *Store) CreateEvaluation(ctx context.Context, in CreateEvaluationInput) (map[string]any, error) {
	if in.SubjectType == "" || in.SubjectID == "" || in.SubjectRevision == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "subject_type/subject_id/subject_revision 必填")
	}
	if in.DatasetRevisionID.IsZero() || in.SuiteRevision == "" || in.EnvironmentDigest == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "dataset_revision_id/suite_revision/environment_digest 必填")
	}
	id := ids.New().String()
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
            INSERT INTO evaluation.runs
                (evaluation_run_id, tenant_id, subject_type, subject_id, subject_revision,
                 dataset_revision_id, suite_revision, environment_digest, status)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5, $6, $7, 'QUEUED')`,
			id, in.SubjectType, in.SubjectID, in.SubjectRevision,
			in.DatasetRevisionID.String(), in.SuiteRevision, in.EnvironmentDigest)
		if err != nil {
			return errors.Wrap(err, errors.CodeInvalidArgument, "数据集修订版不存在或写入失败")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "status": "QUEUED"}, nil
}

// GetEvaluation 读取单个评估运行。
func (s *Store) GetEvaluation(ctx context.Context, evalID ids.ID) (map[string]any, error) {
	if evalID.IsZero() {
		return nil, errors.E(errors.CodeInvalidArgument, "evaluation_id 不合法")
	}
	var out map[string]any
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT evaluation_run_id::text AS id, subject_type, subject_id, subject_revision,
                   dataset_revision_id::text AS dataset_revision_id, suite_revision,
                   environment_digest, status, started_at, completed_at, created_at
              FROM evaluation.runs WHERE evaluation_run_id = $1`, evalID.String())
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errors.E(errors.CodeNotFound, "评估运行不存在或不在可见范围")
		}
		out = rows[0]
		metrics, err := queryMaps(ctx, tx, `
            SELECT metric_key, metric_value, sample_count, created_at
              FROM evaluation.metric_results
             WHERE evaluation_run_id = $1 ORDER BY metric_key ASC`, evalID.String())
		if err != nil {
			return err
		}
		if metrics == nil {
			metrics = []map[string]any{}
		}
		out["metrics"] = metrics
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
