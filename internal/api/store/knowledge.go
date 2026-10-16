package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// ---------------------------------------------------------------------------
// Knowledge — 企业知识问答（kb.documents / kb.chunks / kb.ingest_jobs）
//
// 与其他 registry 资源同构：文档软删（status=DELETED，保留行审计），分块与
// 任务物理删（向量与任务无审计价值）；摄取任务创建即快照 embedding_model，
// 重试不漂移。所有 jsonb 读取必须 ::text 投影（queryMaps 的历史坑）。
// ---------------------------------------------------------------------------

// CreateDocumentInput 是知识文档创建（同步分块落库 + 建两个摄取任务）的输入。
type CreateDocumentInput struct {
	Title      string
	SourcePath string
	FileKind   string
	SizeBytes  int64
	Sha256     string
	UploadedBy ids.ID
	// EmbeddingModel 是 EMBED 任务的模型快照（h.deps.KBEmbeddingModel，创建前必须非空）。
	EmbeddingModel string
	// TargetOntologyVersionID 为空时只创建 EMBED 任务（跳过抽取）。
	TargetOntologyVersionID string
	// Chunks 是已切好的分块内容（顺序即 chunk_index）。
	Chunks []string
}

// CreateDocument 在单租户事务内完成：sha256 去重 → 文档落库 → 分块批量插入 →
// 创建 EMBED/EXTRACT 摄取任务。返回文档与任务摘要（含 id）。
func (s *Store) CreateDocument(ctx context.Context, in CreateDocumentInput) (map[string]any, error) {
	var out map[string]any
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		// 1. sha256 去重（UNIQUE(tenant_id, sha256) 的前置显式检查，给出 409 而非裸冲突）。
		var existID, existStatus string
		err := tx.QueryRow(ctx, `
            SELECT id::text, status FROM kb.documents WHERE sha256 = $1 LIMIT 1`, in.Sha256).
			Scan(&existID, &existStatus)
		switch {
		case err == nil:
			if existStatus != "DELETED" {
				return errors.Ef(errors.CodeConflict,
					"同内容文档已存在（sha256 去重命中 %s），如需替换请先删除原文档", existID)
			}
			// 曾被删除的同内容文档：复用该行（保留审计轨迹），重置状态后重建分块与任务。
		case err == pgx.ErrNoRows:
			existID = ""
		default:
			return errors.Wrap(err, errors.CodeInternal, "查询文档去重失败")
		}

		docID := existID
		if docID == "" {
			docID = ids.New().String()
			if _, err := tx.Exec(ctx, `
                INSERT INTO kb.documents
                    (id, tenant_id, title, source_path, file_kind, size_bytes, sha256, chunk_count, status, uploaded_by)
                VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5, $6, $7, 'READY', $8)`,
				docID, in.Title, in.SourcePath, in.FileKind, in.SizeBytes, in.Sha256, len(in.Chunks),
				in.UploadedBy.String()); err != nil {
				return errors.Wrap(err, errors.CodeConflict, "文档写入失败（sha256 冲突或参数非法）")
			}
		} else {
			if _, err := tx.Exec(ctx, `
                UPDATE kb.documents SET
                    title = $2, source_path = $3, file_kind = $4, size_bytes = $5,
                    chunk_count = $6, status = 'READY', error = '', uploaded_by = $7, updated_at = now()
                WHERE id = $1`, docID, in.Title, in.SourcePath, in.FileKind, in.SizeBytes,
				len(in.Chunks), in.UploadedBy.String()); err != nil {
				return errors.Wrap(err, errors.CodeInternal, "文档重建失败")
			}
		}

		// 2. 分块批量插入（内容上传时已同步切好；embedding 留空待 EMBED 任务回填）。
		for i, content := range in.Chunks {
			if _, err := tx.Exec(ctx, `
                INSERT INTO kb.chunks (id, tenant_id, document_id, chunk_index, content)
                VALUES ($1, app.current_tenant_id(), $2, $3, $4)`,
				ids.New().String(), docID, i, content); err != nil {
				return errors.Wrap(err, errors.CodeInternal, "分块写入失败")
			}
		}

		// 3. 摄取任务：EMBED 必建（模型快照）；EXTRACT 仅在指明目标本体版本时创建。
		embedJobID := ids.New().String()
		if _, err := tx.Exec(ctx, `
            INSERT INTO kb.ingest_jobs
                (id, tenant_id, document_id, kind, embedding_model, status, progress_total)
            VALUES ($1, app.current_tenant_id(), $2, 'EMBED', $3, 'PENDING', $4)`,
			embedJobID, docID, in.EmbeddingModel, len(in.Chunks)); err != nil {
			return errors.Wrap(err, errors.CodeConflict, "EMBED 任务创建失败（同文档任务已存在？）")
		}
		out = map[string]any{
			"id": docID, "title": in.Title, "file_kind": in.FileKind,
			"size_bytes": in.SizeBytes, "sha256": in.Sha256,
			"chunk_count": len(in.Chunks), "status": "READY",
			"source_path":  in.SourcePath,
			"embed_job_id": embedJobID,
		}
		if in.TargetOntologyVersionID != "" {
			// fail-closed：EXTRACT 的目标本体版本必须真实存在（悬空引用让 worker 阶段才炸）。
			var n int
			if err := tx.QueryRow(ctx, `
                SELECT count(*) FROM ontology.ontology_versions WHERE id = $1`,
				in.TargetOntologyVersionID).Scan(&n); err != nil {
				return errors.Wrap(err, errors.CodeInternal, "校验目标本体版本失败")
			}
			if n == 0 {
				return errors.E(errors.CodeNotFound, "target_ontology_version_id 指向的本体版本不存在")
			}
			extractJobID := ids.New().String()
			if _, err := tx.Exec(ctx, `
                INSERT INTO kb.ingest_jobs
                    (id, tenant_id, document_id, kind, target_ontology_version_id, status, progress_total)
                VALUES ($1, app.current_tenant_id(), $2, 'EXTRACT', $3, 'PENDING', $4)`,
				extractJobID, docID, in.TargetOntologyVersionID, len(in.Chunks)); err != nil {
				return errors.Wrap(err, errors.CodeConflict, "EXTRACT 任务创建失败")
			}
			out["extract_job_id"] = extractJobID
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

const listDocumentsSQL = `
SELECT d.id::text AS id, d.title, d.source_path, d.file_kind, d.size_bytes, d.sha256,
       d.chunk_count, d.status, d.error, d.uploaded_by::text AS uploaded_by,
       d.created_at, d.updated_at
  FROM kb.documents d
 WHERE d.tenant_id = app.current_tenant_id()
   AND d.status <> 'DELETED'
   AND ($2::timestamptz IS NULL OR (d.created_at, d.id) < ($2::timestamptz, $3::uuid))
   AND ($4::text = '' OR d.title ILIKE '%' || $4 || '%' OR d.source_path ILIKE '%' || $4 || '%')
 ORDER BY d.created_at DESC, d.id DESC
 LIMIT $1`

// ListDocuments 返回知识文档列表（(created_at,id) 降序 keyset 分页，支持 ?q=）。
// 软删文档不出现在列表；limit 由 handler 经 NormalizeLimit 收敛（默认 10）。
func (s *Store) ListDocuments(ctx context.Context, limit int, cursor, q string) (*ListResult, error) {
	c, err := DecodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	var cursorT any
	var cursorID any
	if c != nil {
		cursorT = c.Time
		cursorID = c.Key
	}
	res := &ListResult{Items: []map[string]any{}}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, listDocumentsSQL, limit, cursorT, cursorID, q)
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

// GetDocument 返回文档详情：文档行 + 分块预览（前 50 条，按序）+ 摄取任务列表。
func (s *Store) GetDocument(ctx context.Context, id string) (map[string]any, error) {
	var doc map[string]any
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT id::text AS id, title, source_path, file_kind, size_bytes, sha256,
                   chunk_count, status, error, uploaded_by::text AS uploaded_by,
                   created_at, updated_at
              FROM kb.documents
             WHERE id = $1 AND tenant_id = app.current_tenant_id()`, id)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errors.E(errors.CodeNotFound, "文档不存在")
		}
		doc = rows[0]
		if chunks, err := queryMaps(ctx, tx, `
            SELECT id::text AS id, chunk_index, content,
                   (embedding IS NOT NULL) AS has_embedding, embedding_model, created_at
              FROM kb.chunks
             WHERE document_id = $1 AND tenant_id = app.current_tenant_id()
             ORDER BY chunk_index ASC LIMIT 50`, id); err != nil {
			return err
		} else {
			doc["chunks"] = chunks
		}
		if jobs, err := queryMaps(ctx, tx, `
            SELECT id::text AS id, kind, coalesce(target_ontology_version_id::text, '') AS target_ontology_version_id,
                   embedding_model, status, attempt, max_attempts, progress_done, progress_total,
                   stage, error, lease_owner, next_run_at, created_at, updated_at
              FROM kb.ingest_jobs
             WHERE document_id = $1 AND tenant_id = app.current_tenant_id()
             ORDER BY kind ASC`, id); err != nil {
			return err
		} else {
			doc["jobs"] = jobs
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return doc, nil
}

// RetryJob 手动重试失败的摄取任务：仅 FAILED 可重试；重置 attempt 并立即排队。
func (s *Store) RetryJob(ctx context.Context, jobID string) (map[string]any, error) {
	var job map[string]any
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            UPDATE kb.ingest_jobs SET
                status = 'PENDING', attempt = 0, error = '',
                next_run_at = now(), lease_owner = '', lease_expires_at = NULL, updated_at = now()
            WHERE id = $1 AND tenant_id = app.current_tenant_id() AND status = 'FAILED'
            RETURNING id::text AS id, document_id::text AS document_id, kind, status, attempt`,
			jobID)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			// 区分"不存在"与"状态不允许"。
			var n int
			if err := tx.QueryRow(ctx, `
                SELECT count(*) FROM kb.ingest_jobs WHERE id = $1 AND tenant_id = app.current_tenant_id()`,
				jobID).Scan(&n); err != nil {
				return errors.Wrap(err, errors.CodeInternal, "查询摄取任务失败")
			}
			if n == 0 {
				return errors.E(errors.CodeNotFound, "摄取任务不存在")
			}
			return errors.E(errors.CodeConflict, "仅 FAILED 状态的任务可重试")
		}
		job = rows[0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return job, nil
}

// DeleteDocument 软删文档：documents.status=DELETED（保留行审计），分块与任务
// 级联物理删除（向量无审计价值，且避免死数据参与检索）。
func (s *Store) DeleteDocument(ctx context.Context, id string) error {
	return s.TenantTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
            UPDATE kb.documents SET status = 'DELETED', updated_at = now()
             WHERE id = $1 AND tenant_id = app.current_tenant_id() AND status <> 'DELETED'`, id)
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "文档软删失败")
		}
		if tag.RowsAffected() == 0 {
			return errors.E(errors.CodeNotFound, "文档不存在或已删除")
		}
		// 物理删分块（连同向量）与摄取任务；文档行保留审计。
		if _, err := tx.Exec(ctx, `
            DELETE FROM kb.chunks WHERE document_id = $1 AND tenant_id = app.current_tenant_id()`, id); err != nil {
			return errors.Wrap(err, errors.CodeInternal, "删除分块失败")
		}
		if _, err := tx.Exec(ctx, `
            DELETE FROM kb.ingest_jobs WHERE document_id = $1 AND tenant_id = app.current_tenant_id()`, id); err != nil {
			return errors.Wrap(err, errors.CodeInternal, "删除摄取任务失败")
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// 混合检索（knowledge-retrieval 工具的数据面）：向量腿 + 词项腿，RRF 融合在
// 调用方完成 —— 两腿各自 topK，融合权重只在这一个口径里定义。
// ---------------------------------------------------------------------------

// KnowledgeHit 是混合检索单腿结果（RRF 融合前的原始命中）。
type KnowledgeHit struct {
	DocumentID string
	DocTitle   string
	SourcePath string
	ChunkIndex int
	Content    string
	Score      float64
}

// SearchKnowledgeVector 向量腿：pgvector 余弦 topK（点名模型 + 过滤已回填向量）。
// queryVec 是 '[0.1,0.2,...]' 字面量；documentIDs 为空表示不过滤。
func (s *Store) SearchKnowledgeVector(ctx context.Context, embeddingModel, queryVec string, topK int, documentIDs []string) ([]KnowledgeHit, error) {
	hits := []KnowledgeHit{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
            SELECT c.document_id::text AS document_id, d.title, d.source_path, c.chunk_index, c.content,
                   1 - (c.embedding <=> $2::vector) AS score
              FROM kb.chunks c
              JOIN kb.documents d ON d.id = c.document_id
             WHERE c.tenant_id = app.current_tenant_id() AND d.tenant_id = app.current_tenant_id()
               AND c.embedding IS NOT NULL AND c.embedding_model = $1
               AND ($4::uuid[] IS NULL OR c.document_id::uuid = ANY($4::uuid[]))
             ORDER BY c.embedding <=> $2::vector
             LIMIT $3`, embeddingModel, queryVec, topK, uuidArrayOrNull(documentIDs))
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "向量检索失败")
		}
		defer rows.Close()
		for rows.Next() {
			var h KnowledgeHit
			if err := rows.Scan(&h.DocumentID, &h.DocTitle, &h.SourcePath, &h.ChunkIndex, &h.Content, &h.Score); err != nil {
				return errors.Wrap(err, errors.CodeInternal, "向量检索行扫描失败")
			}
			hits = append(hits, h)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return hits, nil
}

// SearchKnowledgeTerms 词项腿：trigram 相似度 + 子串命中，按词项打分取 topK。
// 任一词项命中即入选；score 取各词项 similarity 的最大值。
func (s *Store) SearchKnowledgeTerms(ctx context.Context, terms []string, topK int, documentIDs []string) ([]KnowledgeHit, error) {
	hits := []KnowledgeHit{}
	terms = nonEmptyTerms(terms)
	if len(terms) == 0 {
		return hits, nil
	}
	// 动态拼词项条件：$3.. 为词项（$1=topK，$2=文档过滤）。
	var conds, scores []string
	args := []any{topK, uuidArrayOrNull(documentIDs)}
	for _, term := range terms {
		p := len(args) + 1
		args = append(args, term)
		conds = append(conds, fmt.Sprintf(
			"(c.content ILIKE '%%' || $%d || '%%' OR similarity(c.content, $%d) > 0.1)", p, p))
		scores = append(scores, fmt.Sprintf("similarity(c.content, $%d)", p))
	}
	scoreExpr := "GREATEST(" + strings.Join(scores, ", ") + ")"
	sql := `
        SELECT c.document_id::text AS document_id, d.title, d.source_path, c.chunk_index, c.content,
               ` + scoreExpr + ` AS score
          FROM kb.chunks c
          JOIN kb.documents d ON d.id = c.document_id
         WHERE c.tenant_id = app.current_tenant_id() AND d.tenant_id = app.current_tenant_id()
           AND ($2::uuid[] IS NULL OR c.document_id::uuid = ANY($2::uuid[]))
           AND (` + strings.Join(conds, " OR ") + `)
         ORDER BY ` + scoreExpr + ` DESC
         LIMIT $1`
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "词项检索失败")
		}
		defer rows.Close()
		for rows.Next() {
			var h KnowledgeHit
			if err := rows.Scan(&h.DocumentID, &h.DocTitle, &h.SourcePath, &h.ChunkIndex, &h.Content, &h.Score); err != nil {
				return errors.Wrap(err, errors.CodeInternal, "词项检索行扫描失败")
			}
			hits = append(hits, h)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return hits, nil
}

// uuidArrayOrNull 空切片转 NULL（避免 ANY(空数组) 恒假）。
func uuidArrayOrNull(ids []string) any {
	if len(ids) == 0 {
		return nil
	}
	return ids
}

// nonEmptyTerms 去重去空并裁剪词项。
func nonEmptyTerms(terms []string) []string {
	out := make([]string, 0, len(terms))
	seen := map[string]bool{}
	for _, t := range terms {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}
