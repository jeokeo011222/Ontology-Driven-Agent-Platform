package evidence

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
)

// Store 持久化证据包分项到 audit.evidence_records。
type Store struct {
	Pool *pgxpool.Pool
}

// Save 把证据包的各分项写入 audit.evidence_records（run_id 取自 bundle）。
//
// audit.evidence_records 启用了 FORCE RLS，需在租户事务内写入。
func (s *Store) Save(ctx context.Context, tenantID ids.ID, b *Bundle) error {
	if s == nil || s.Pool == nil {
		return errors.E(errors.CodeInternal, "evidence: store has no pool")
	}
	if tenantID.IsZero() {
		return errors.E(errors.CodeValidationFailed, "evidence: tenantID must not be zero")
	}
	if b == nil {
		return errors.E(errors.CodeValidationFailed, "evidence: nil bundle")
	}
	if b.RunID.IsZero() {
		return errors.E(errors.CodeValidationFailed, "evidence: bundle runID must not be zero")
	}
	return inTenantTx(ctx, s.Pool, tenantID, func(tx pgx.Tx) error {
		for _, it := range b.Items {
			var meta any
			if len(it.Metadata) > 0 {
				meta = string(it.Metadata)
			} else {
				meta = "{}"
			}
			if _, err := tx.Exec(ctx, insertEvidenceSQL,
				ids.New().String(), tenantID.String(), b.RunID.String(),
				it.Type, it.SourceType, it.SourceRef, it.ContentDigest,
				it.StorageURI, it.Summary, meta); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListByRun 读取指定运行的全部证据分项。
func (s *Store) ListByRun(ctx context.Context, tenantID, runID ids.ID) ([]EvidenceItem, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.E(errors.CodeInternal, "evidence: store has no pool")
	}
	if tenantID.IsZero() || runID.IsZero() {
		return nil, errors.E(errors.CodeValidationFailed, "evidence: tenantID and runID must not be zero")
	}
	var items []EvidenceItem
	err := inTenantTx(ctx, s.Pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, listEvidenceSQL, tenantID.String(), runID.String())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				it   EvidenceItem
				meta string
			)
			if err := rows.Scan(&it.Type, &it.SourceType, &it.SourceRef,
				&it.ContentDigest, &it.StorageURI, &it.Summary, &meta); err != nil {
				return err
			}
			it.Metadata = json.RawMessage(meta)
			items = append(items, it)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "evidence: list by run")
	}
	return items, nil
}

// insertEvidenceSQL 写入证据分项；step_id 留空（本包不绑定具体步骤）。
const insertEvidenceSQL = `
INSERT INTO audit.evidence_records
    (id, tenant_id, run_id, evidence_type, source_type, source_ref,
     content_digest, storage_uri, summary, metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

const listEvidenceSQL = `
SELECT evidence_type, source_type, COALESCE(source_ref, ''),
       content_digest, COALESCE(storage_uri, ''), summary, metadata
  FROM audit.evidence_records
 WHERE tenant_id = $1
   AND run_id = $2
 ORDER BY created_at ASC, id ASC`

// inTenantTx 在租户事务（SET LOCAL app.tenant_id）中执行 fn 并提交。
func inTenantTx(ctx context.Context, pool *pgxpool.Pool, tenantID ids.ID, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := pgxutil.WithTenantTx(ctx, tx, tenantID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
