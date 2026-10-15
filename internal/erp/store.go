package erp

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
)

// ErrIdempotencyExists 表示 (tenant_id, external_system, idempotency_key) 唯一约束冲突。
// Gateway 据此转入 §8.2 的"已有记录处置"分支，而不是当作普通错误。
var ErrIdempotencyExists = stderrors.New("erp: idempotency key already exists")

func isIdempotencyExists(err error) bool {
	return stderrors.Is(err, ErrIdempotencyExists)
}

// PGStore 是 OperationStore 的 PostgreSQL 实现。
//
// RLS 约束（第二篇 §11.3）：integration.erp_operations / erp_receipts 均已 FORCE RLS，
// 应用连接角色 odap_app 非超级用户；任何读写都必须在事务内先
// SET LOCAL app.tenant_id（由 pgxutil.WithTenantTx 完成），否则读 0 行 / 写被拒。
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore 构造 PostgreSQL 账本存储。
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

const operationColumns = `id, tenant_id, run_id, tool_call_id, adapter_key, external_system,
	object_type, object_id, action, idempotency_key, expected_version, payload_snapshot,
	payload_digest, approval_id, status, created_at, dispatched_at, finished_at`

// Insert 写入一条操作账本。
func (s *PGStore) Insert(ctx context.Context, tenantID ids.ID, op *Operation) error {
	if op == nil {
		return errors.E(errors.CodeInternal, "erp: nil operation")
	}
	payload, err := json.Marshal(clonePayload(op.PayloadSnapshot))
	if err != nil {
		return errors.Wrap(err, errors.CodeInternal, "erp: 序列化 payload_snapshot 失败")
	}
	createdAt := op.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO integration.erp_operations (
				id, tenant_id, run_id, tool_call_id, adapter_key, external_system,
				object_type, object_id, action, idempotency_key, expected_version,
				payload_snapshot, payload_digest, approval_id, status, created_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
			op.ID.String(), tenantID.String(),
			uuidOrNull(op.RunID), uuidOrNull(op.ToolCallID),
			op.AdapterKey, op.ExternalSystem, op.ObjectType, op.ObjectID, op.Action,
			op.IdempotencyKey, textOrNull(op.ExpectedVersion),
			payload, op.PayloadDigest, uuidOrNull(op.ApprovalID), op.Status, createdAt,
		)
		if err != nil {
			if isUniqueViolation(err, "idempotency") {
				return ErrIdempotencyExists
			}
			if isUniqueViolation(err, "") {
				return errors.Wrap(err, errors.CodeConflict, "erp: 操作主键冲突")
			}
			return errors.Wrap(err, errors.CodeInternal, "erp: 写操作账本失败")
		}
		return nil
	})
}

// GetByID 按本地主键读取操作。
func (s *PGStore) GetByID(ctx context.Context, tenantID ids.ID, operationID ids.ID) (*Operation, error) {
	var op *Operation
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			"SELECT "+operationColumns+" FROM integration.erp_operations WHERE tenant_id = $1 AND id = $2",
			tenantID.String(), operationID.String())
		got, err := scanOperation(row)
		if err != nil {
			return err
		}
		op = got
		return nil
	})
	if err != nil {
		if stderrors.Is(err, pgx.ErrNoRows) {
			return nil, errors.Ef(errors.CodeNotFound, "erp: 操作 %s 不存在", operationID)
		}
		return nil, err
	}
	return op, nil
}

// GetByIdempotencyKey 按 (tenant, external_system, idempotency_key) 读取操作。
func (s *PGStore) GetByIdempotencyKey(ctx context.Context, tenantID ids.ID, externalSystem, key string) (*Operation, error) {
	var op *Operation
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			"SELECT "+operationColumns+` FROM integration.erp_operations
			  WHERE tenant_id = $1 AND external_system = $2 AND idempotency_key = $3`,
			tenantID.String(), externalSystem, key)
		got, err := scanOperation(row)
		if err != nil {
			return err
		}
		op = got
		return nil
	})
	if err != nil {
		if stderrors.Is(err, pgx.ErrNoRows) {
			return nil, errors.Ef(errors.CodeNotFound, "erp: 幂等键 %s 无对应操作", key)
		}
		return nil, err
	}
	return op, nil
}

// ListByRun 返回某 Run 名下的全部操作账本（按创建时间升序）。
//
// 任务详情页「外部操作与对账」页签的数据源：Run 与 ERP 操作并列展示
// （contracts/01 §5.1）。RLS 由 inTenantTx 保证。
func (s *PGStore) ListByRun(ctx context.Context, tenantID ids.ID, runID ids.ID) ([]*Operation, error) {
	var ops []*Operation
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			"SELECT "+operationColumns+` FROM integration.erp_operations
			  WHERE tenant_id = $1 AND run_id = $2
			  ORDER BY created_at ASC, id ASC`,
			tenantID.String(), runID.String())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			op, err := scanOperation(rows)
			if err != nil {
				return err
			}
			ops = append(ops, op)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return ops, nil
}

// UpdateStatus 执行条件状态更新；返回是否真正更新了行。
func (s *PGStore) UpdateStatus(ctx context.Context, tenantID ids.ID, operationID ids.ID, tr StatusTransition) (bool, error) {
	args := []any{tenantID.String(), operationID.String(), tr.To}
	set := []string{"status = $3"}
	if tr.SetDispatched {
		args = append(args, tr.At)
		set = append(set, fmt.Sprintf("dispatched_at = $%d", len(args)))
	}
	if tr.SetFinished {
		args = append(args, tr.At)
		set = append(set, fmt.Sprintf("finished_at = $%d", len(args)))
	}
	where := "tenant_id = $1 AND id = $2"
	if len(tr.From) > 0 {
		args = append(args, tr.From)
		where += fmt.Sprintf(" AND status = ANY($%d::text[])", len(args))
	}
	stmt := "UPDATE integration.erp_operations SET " + strings.Join(set, ", ") + " WHERE " + where

	updated := false
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, stmt, args...)
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "erp: 更新操作状态失败")
		}
		updated = tag.RowsAffected() > 0
		return nil
	})
	if err != nil {
		return false, err
	}
	return updated, nil
}

// AppendReceipt 追加一条回执（一次操作可有多条：submit/query/reconcile）。
func (s *PGStore) AppendReceipt(ctx context.Context, tenantID ids.ID, r *Receipt) error {
	if r == nil {
		return errors.E(errors.CodeInternal, "erp: nil receipt")
	}
	payload, err := json.Marshal(clonePayload(r.ResponsePayload))
	if err != nil {
		return errors.Wrap(err, errors.CodeInternal, "erp: 序列化 response_payload 失败")
	}
	receivedAt := r.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	}
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO integration.erp_receipts (
				id, tenant_id, operation_id, receipt_type, external_operation_id,
				external_object_version, result_status, response_digest,
				response_payload, received_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			r.ID.String(), tenantID.String(), r.OperationID.String(), r.ReceiptType,
			textOrNull(r.ExternalOperationID), textOrNull(r.ExternalObjectVersion),
			r.ResultStatus, textOrNull(r.ResponseDigest), payload, receivedAt,
		)
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "erp: 写操作回执失败")
		}
		return nil
	})
}

// CountReceipts 统计某操作指定类型的回执条数。
func (s *PGStore) CountReceipts(ctx context.Context, tenantID ids.ID, operationID ids.ID, receiptType string) (int, error) {
	count := 0
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM integration.erp_receipts
			  WHERE tenant_id = $1 AND operation_id = $2 AND receipt_type = $3`,
			tenantID.String(), operationID.String(), receiptType).Scan(&count)
	})
	if err != nil {
		return 0, errors.Wrap(err, errors.CodeInternal, "erp: 统计回执失败")
	}
	return count, nil
}

// LatestReceipt 取某操作指定类型的最新回执。
func (s *PGStore) LatestReceipt(ctx context.Context, tenantID ids.ID, operationID ids.ID, receiptType string) (*Receipt, error) {
	var r *Receipt
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`SELECT id, tenant_id, operation_id, receipt_type, external_operation_id,
			        external_object_version, result_status, response_digest,
			        response_payload, received_at
			   FROM integration.erp_receipts
			  WHERE tenant_id = $1 AND operation_id = $2 AND receipt_type = $3
			  ORDER BY received_at DESC LIMIT 1`,
			tenantID.String(), operationID.String(), receiptType)
		got, err := scanReceipt(row)
		if err != nil {
			return err
		}
		r = got
		return nil
	})
	if err != nil {
		if stderrors.Is(err, pgx.ErrNoRows) {
			return nil, errors.Ef(errors.CodeNotFound, "erp: 操作 %s 无 %s 回执", operationID, receiptType)
		}
		return nil, err
	}
	return r, nil
}

// inTenantTx 在租户事务中执行 fn（SET LOCAL app.tenant_id，事务结束自动清理）。
func (s *PGStore) inTenantTx(ctx context.Context, tenantID ids.ID, fn func(pgx.Tx) error) error {
	if s == nil || s.pool == nil {
		return errors.E(errors.CodeInternal, "erp: store 未配置连接池")
	}
	if tenantID.IsZero() {
		return errors.E(errors.CodeValidationFailed, "erp: tenant id 不能为空")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return errors.Wrap(err, errors.CodeInternal, "erp: 开启事务失败")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := pgxutil.WithTenantTx(ctx, tx, tenantID); err != nil {
		return errors.Wrap(err, errors.CodeInternal, "erp: 注入租户上下文失败")
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return errors.Wrap(err, errors.CodeInternal, "erp: 提交事务失败")
	}
	return nil
}

func scanOperation(row pgx.Row) (*Operation, error) {
	var (
		idStr, tenantStr                              string
		runStr, toolStr, approvalStr, expectedVersion *string
		payload                                       []byte
		dispatchedAt, finishedAt                      *time.Time
		op                                            Operation
	)
	if err := row.Scan(
		&idStr, &tenantStr, &runStr, &toolStr, &op.AdapterKey, &op.ExternalSystem,
		&op.ObjectType, &op.ObjectID, &op.Action, &op.IdempotencyKey, &expectedVersion,
		&payload, &op.PayloadDigest, &approvalStr, &op.Status, &op.CreatedAt,
		&dispatchedAt, &finishedAt,
	); err != nil {
		return nil, err
	}
	var err error
	if op.ID, err = ids.Parse(idStr); err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "erp: 解析 operation id")
	}
	if op.TenantID, err = ids.Parse(tenantStr); err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "erp: 解析 tenant id")
	}
	if op.RunID, err = parseOptionalID(runStr); err != nil {
		return nil, err
	}
	if op.ToolCallID, err = parseOptionalID(toolStr); err != nil {
		return nil, err
	}
	if op.ApprovalID, err = parseOptionalID(approvalStr); err != nil {
		return nil, err
	}
	if expectedVersion != nil {
		op.ExpectedVersion = *expectedVersion
	}
	op.DispatchedAt = dispatchedAt
	op.FinishedAt = finishedAt
	op.PayloadSnapshot = map[string]any{}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &op.PayloadSnapshot); err != nil {
			return nil, errors.Wrap(err, errors.CodeInternal, "erp: 解析 payload_snapshot")
		}
	}
	return &op, nil
}

func scanReceipt(row pgx.Row) (*Receipt, error) {
	var (
		idStr, tenantStr, operationIDStr string
		externalOpID, externalObjVersion *string
		responseDigest                   *string
		payload                          []byte
		r                                Receipt
	)
	if err := row.Scan(
		&idStr, &tenantStr, &operationIDStr, &r.ReceiptType, &externalOpID,
		&externalObjVersion, &r.ResultStatus, &responseDigest, &payload, &r.ReceivedAt,
	); err != nil {
		return nil, err
	}
	var err error
	if r.ID, err = ids.Parse(idStr); err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "erp: 解析 receipt id")
	}
	if r.TenantID, err = ids.Parse(tenantStr); err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "erp: 解析 receipt tenant id")
	}
	if r.OperationID, err = ids.Parse(operationIDStr); err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "erp: 解析 receipt operation id")
	}
	if externalOpID != nil {
		r.ExternalOperationID = *externalOpID
	}
	if externalObjVersion != nil {
		r.ExternalObjectVersion = *externalObjVersion
	}
	if responseDigest != nil {
		r.ResponseDigest = *responseDigest
	}
	r.ResponsePayload = map[string]any{}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &r.ResponsePayload); err != nil {
			return nil, errors.Wrap(err, errors.CodeInternal, "erp: 解析 response_payload")
		}
	}
	return &r, nil
}

func parseOptionalID(s *string) (ids.ID, error) {
	if s == nil || *s == "" {
		return ids.ID{}, nil
	}
	id, err := ids.Parse(*s)
	if err != nil {
		return ids.ID{}, errors.Wrap(err, errors.CodeInternal, "erp: 解析可选 UUID")
	}
	return id, nil
}

func uuidOrNull(id ids.ID) any {
	if id.IsZero() {
		return nil
	}
	return id.String()
}

func textOrNull(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// isUniqueViolation 判断错误是否为 unique_violation（23505）。
// contains 为空表示任意唯一约束；非空则要求约束名包含该子串。
func isUniqueViolation(err error, contains string) bool {
	var pgErr *pgconn.PgError
	if !stderrors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return contains == "" || strings.Contains(pgErr.ConstraintName, contains)
}
