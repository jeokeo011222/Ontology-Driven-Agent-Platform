package knowledge

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/llm"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
)

// Embedder 是批量向量嵌入客户端的最小接口（*llm.Gateway 满足）。
// 单独定义接口而非直接依赖 *llm.Gateway：测试可以注入桩实现。
type Embedder interface {
	Embed(ctx context.Context, tenantID ids.ID, req llm.EmbeddingRequest) (llm.EmbeddingResponse, error)
}

const (
	// RequiredEmbeddingDims 是 kb.chunks.embedding 列的固定维度 vector(1024)。
	// 首块维度探针发现不一致必须 fail-closed：向量空间不可截断/映射，截断
	// 等于静默数据污染，后续所有余弦检索都会失真。
	RequiredEmbeddingDims = 1024
	// embedBatchSize 是单批嵌入条数上限（网关不做分批，由调用方控制）。
	embedBatchSize = 32
)

// EmbedDocument 把文档的全部分块批量嵌入并逐批回填 kb.chunks.embedding
// 与 embedding_model。分块内容直接读自 kb.chunks（上传时已同步落库），
// 不依赖原始文件，因此 worker 无需 uploads 卷也能执行。
//
// modelID 必须点名（嵌入只允许点名路由）；onProgress 每批回调一次
// (done, total, stage)，供任务层续租与进度落库，可为 nil。
func EmbedDocument(ctx context.Context, pool *pgxpool.Pool, tenantID ids.ID, documentID, modelID string,
	embedder Embedder, onProgress func(done, total int, stage string) error) error {
	if pool == nil {
		return errors.E(errors.CodeInternal, "嵌入失败：数据库连接未装配")
	}
	if embedder == nil {
		return errors.E(errors.CodeInternal, "嵌入失败：嵌入客户端未装配")
	}
	if strings.TrimSpace(modelID) == "" {
		return errors.E(errors.CodeInvalidArgument, "嵌入失败：未配置嵌入模型（ODAP_KB_EMBEDDING_MODEL）")
	}
	// 1. 读取分块（只取 id/index/content，按序嵌入）。
	chunks, err := loadChunks(ctx, pool, tenantID, documentID)
	if err != nil {
		return err
	}
	total := len(chunks)
	if total == 0 {
		// 无分块（空文档）：没有可嵌入的内容，直接视为完成。
		return notifyProgress(onProgress, 0, 0, "embedding")
	}
	// 2. 逐批嵌入回填。
	for start := 0; start < total; start += embedBatchSize {
		end := start + embedBatchSize
		if end > total {
			end = total
		}
		batch := chunks[start:end]
		texts := make([]string, 0, len(batch))
		for _, c := range batch {
			texts = append(texts, c.content)
		}
		resp, err := embedder.Embed(ctx, tenantID, llm.EmbeddingRequest{
			TenantID: tenantID,
			ModelID:  modelID,
			Texts:    texts,
			// 幂等键带批次偏移：重试时同批可被账本去重，不同批互不干扰。
			IdempotencyKey: fmt.Sprintf("kb-embed:%s:%d", documentID, start),
		})
		if err != nil {
			return err
		}
		// 3. 首块维度探针（fail-closed）：任何维度 ≠ 1024 都拒绝写库。
		if resp.Dimensions != RequiredEmbeddingDims {
			return errors.Ef(errors.CodeValidationFailed,
				"嵌入模型 %q 返回维度 %d，与 kb.chunks 的 vector(%d) 不符；禁止截断或映射，请更换 ODAP_KB_EMBEDDING_MODEL 指定的模型",
				modelID, resp.Dimensions, RequiredEmbeddingDims)
		}
		if len(resp.Vectors) != len(texts) {
			return errors.Ef(errors.CodeInternal,
				"嵌入供应商返回向量数 %d 与请求文本数 %d 不一致", len(resp.Vectors), len(texts))
		}
		// 4. 回填本批向量。
		if err := tenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
			for i, vec := range resp.Vectors {
				if len(vec) != RequiredEmbeddingDims {
					return errors.Ef(errors.CodeValidationFailed,
						"嵌入向量维度 %d 与声明的 %d 不一致（模型 %q）", len(vec), RequiredEmbeddingDims, modelID)
				}
				if _, err := tx.Exec(ctx, `
                    UPDATE kb.chunks
                       SET embedding = $2::vector, embedding_model = $3
                     WHERE id = $1 AND tenant_id = app.current_tenant_id()`,
					batch[i].id, VectorLiteral(vec), modelID); err != nil {
					return errors.Wrap(err, errors.CodeInternal, "回填分块向量失败")
				}
			}
			return nil
		}); err != nil {
			return err
		}
		if err := notifyProgress(onProgress, end, total, "embedding"); err != nil {
			return err
		}
	}
	return nil
}

// chunkRow 是分块的极小投影（嵌入只关心主键、序号与正文）。
type chunkRow struct {
	id      string
	index   int
	content string
}

// loadChunks 读取文档全部分块（按 chunk_index 升序）。
func loadChunks(ctx context.Context, pool *pgxpool.Pool, tenantID ids.ID, documentID string) ([]chunkRow, error) {
	var chunks []chunkRow
	err := tenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
            SELECT id::text, chunk_index, content
              FROM kb.chunks
             WHERE tenant_id = app.current_tenant_id() AND document_id = $1
             ORDER BY chunk_index ASC`, documentID)
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "读取分块失败")
		}
		defer rows.Close()
		for rows.Next() {
			var c chunkRow
			if err := rows.Scan(&c.id, &c.index, &c.content); err != nil {
				return errors.Wrap(err, errors.CodeInternal, "分块行扫描失败")
			}
			chunks = append(chunks, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return chunks, nil
}

// VectorLiteral 把 float32 向量格式化为 pgvector 的字面量 '[0.1,0.2,...]'
// （嵌入回填与查询向量共用同一格式化口径）。
func VectorLiteral(vec []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, v := range vec {
		if i > 0 {
			b.WriteByte(',')
		}
		// 'g'/-1/32：float32 的最短精确表示，避免精度漂移。
		b.WriteString(strconv.FormatFloat(float64(v), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// tenantTx 在"带租户上下文"的事务内执行 fn（委托 pgxutil 的唯一租户事务实现）。
func tenantTx(ctx context.Context, pool *pgxpool.Pool, tenantID ids.ID, fn func(tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return errors.Wrap(err, errors.CodeInternal, "开启事务失败")
	}
	if err := pgxutil.WithTenantTx(ctx, tx, tenantID); err != nil {
		_ = tx.Rollback(ctx)
		return errors.Wrap(err, errors.CodeInternal, "注入租户上下文失败")
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return errors.Wrap(err, errors.CodeInternal, "提交事务失败")
	}
	return nil
}

// notifyProgress 安全回调进度（nil 回调与回调错误都不阻断主流程以外的事务语义：
// 回调返回错误时原样上抛，供任务层把"续租失败"转换为任务失败）。
func notifyProgress(onProgress func(done, total int, stage string) error, done, total int, stage string) error {
	if onProgress == nil {
		return nil
	}
	return onProgress(done, total, stage)
}
