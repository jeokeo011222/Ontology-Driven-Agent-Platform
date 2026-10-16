package store

// Action 元数据的 HTTP 层读侧适配（action.actions，迁移 000044）。
//
// 与 catalog/skill 同一套约定：RLS 租户事务 + queryMaps（uuid/jsonb/vector 列
// 显式 ::text 投影）+ keyset cursor 分页。向量列不做全量投影——列表页不需要
// 1024 维载荷，相似检索在服务端用 `<=>` 算完距离后只回标量。

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"odap/internal/action"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// ActionListFilter 是动作列表的筛选条件。
type ActionListFilter struct {
	// Type/Outcome/ActorKind 为空表示不过滤；非空必须命中枚举白名单（fail-closed）。
	Type      string
	Outcome   string
	ActorKind string
	// From/To 是 occurred_at 的闭区间过滤（可为空）。
	From *time.Time
	To   *time.Time
	// Q 模糊匹配 title/summary。
	Q string
	// Limit 由 handler 经 NormalizeLimit 收敛（默认 10/页）。
	Limit int
	// Cursor 是 (occurred_at, id) 降序 keyset 游标。
	Cursor string
}

// listActionsSQL 的筛选参数全部带类型断言默认值：空串/NULL 语义由参数侧表达，
// 避免把筛选条件拼进 SQL（全部走参数绑定，无注入面）。
const listActionsSQL = `
SELECT id::text AS id, run_id::text AS run_id, type, title, summary,
       input_digest, output_digest, outcome, actor_kind, actor_name,
       embed_status, occurred_at, created_at
  FROM action.actions
 WHERE ($1::timestamptz IS NULL OR (occurred_at, id) < ($1::timestamptz, $2::uuid))
   AND ($3::text = '' OR type = $3)
   AND ($4::text = '' OR outcome = $4)
   AND ($5::text = '' OR actor_kind = $5)
   AND ($6::timestamptz IS NULL OR occurred_at >= $6)
   AND ($7::timestamptz IS NULL OR occurred_at <= $7)
   AND ($8::text = '' OR title ILIKE '%' || $8 || '%' OR summary ILIKE '%' || $8 || '%')
 ORDER BY occurred_at DESC, id DESC
 LIMIT $9`

// ListActions 返回租户动作列表（occurred_at 降序 cursor 分页，10/页）。
func (s *Store) ListActions(ctx context.Context, f ActionListFilter) (*ListResult, error) {
	if f.Type != "" && !action.ValidType(f.Type) {
		return nil, errors.E(errors.CodeInvalidArgument, "type 不在契约枚举内")
	}
	if f.Outcome != "" && !action.ValidOutcome(f.Outcome) {
		return nil, errors.E(errors.CodeInvalidArgument, "outcome 不在契约枚举内")
	}
	if f.ActorKind != "" && !action.ValidActorKind(f.ActorKind) {
		return nil, errors.E(errors.CodeInvalidArgument, "actor_kind 不在契约枚举内")
	}
	cur, err := DecodeCursor(f.Cursor)
	if err != nil {
		return nil, err
	}
	res := &ListResult{Items: []map[string]any{}}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, listActionsSQL,
			cursorTime(cur), cursorKey(cur),
			f.Type, f.Outcome, f.ActorKind, f.From, f.To, f.Q, f.Limit+1)
		if err != nil {
			return err
		}
		hasMore := len(rows) > f.Limit
		if hasMore {
			rows = rows[:f.Limit]
		}
		res.Items = rows
		if hasMore && len(rows) > 0 {
			if t, k, ok := keyedRow(rows[len(rows)-1], "occurred_at", "id"); ok {
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

// cursorTime/cursorKey 是 DecodeCursor 结果的取值辅助（nil 安全）。
func cursorTime(c *Cursor) *time.Time {
	if c == nil {
		return nil
	}
	return &c.Time
}

func cursorKey(c *Cursor) *string {
	if c == nil {
		return nil
	}
	return &c.Key
}

// GetAction 返回单个动作详情（含 prov_graph_iri，供详情页读回溯源三元组）。
// embedding 载荷刻意不投影：1024 维向量不属于详情页的读取面。
func (s *Store) GetAction(ctx context.Context, id ids.ID) (map[string]any, error) {
	if id.IsZero() {
		return nil, errors.E(errors.CodeInvalidArgument, "action id 不合法")
	}
	out := map[string]any{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT id::text AS id, run_id::text AS run_id, type, title, summary,
                   input_digest, output_digest, outcome, prov_graph_iri,
                   actor_kind, actor_name, embedding_model, embed_status, embed_error,
                   occurred_at, created_at
              FROM action.actions WHERE id = $1`, id.String())
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errors.E(errors.CodeNotFound, "动作不存在")
		}
		out = rows[0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SimilarActions 返回与目标动作最相似的动作（余弦距离升序 topK）。
//
// 目标动作必须已完成嵌入（embed_status='SUCCEEDED' 且 embedding 非空），否则
// 返回**明确的** Conflict——"该动作尚未完成嵌入补算"是可操作的语义（等补算/
// 触发补算），不是模糊的 500。源向量经 `embedding::text` 读出后原样作为
// `$n::vector` 的参数回传：pgvector 的文本表示（"[v1,v2,...]"）与 cast 输入
// 同构，无需在 Go 侧解析再重排（少一处精度漂移点）。
func (s *Store) SimilarActions(ctx context.Context, id ids.ID, topK int) ([]map[string]any, error) {
	if id.IsZero() {
		return nil, errors.E(errors.CodeInvalidArgument, "action id 不合法")
	}
	if topK <= 0 {
		topK = 5
	}
	if topK > 50 {
		return nil, errors.E(errors.CodeInvalidArgument, "top_k 必须在 1..50 之间")
	}
	var sourceVector string
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
            SELECT coalesce(embedding::text, '')
              FROM action.actions
             WHERE id = $1 AND embed_status = 'SUCCEEDED'`, id.String()).Scan(&sourceVector)
		if err != nil {
			if err == pgx.ErrNoRows {
				// 区分两种语义：动作不存在（404）vs 存在但未完成嵌入（409）。
				var exists bool
				if e := tx.QueryRow(ctx,
					`SELECT EXISTS(SELECT 1 FROM action.actions WHERE id = $1)`, id.String()).Scan(&exists); e != nil {
					return errors.Wrap(e, errors.CodeInternal, "查询动作失败")
				}
				if !exists {
					return errors.E(errors.CodeNotFound, "动作不存在")
				}
				return errors.E(errors.CodeConflict, "该动作尚未完成嵌入补算")
			}
			return errors.Wrap(err, errors.CodeInternal, "读取动作嵌入失败")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if sourceVector == "" {
		return nil, errors.E(errors.CodeConflict, "该动作尚未完成嵌入补算")
	}
	var items []map[string]any
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT id::text AS id, type, title, summary, outcome,
                   actor_kind, actor_name, occurred_at,
                   embedding <=> $2::vector AS distance
              FROM action.actions
             WHERE id <> $1
               AND embed_status = 'SUCCEEDED'
               AND embedding IS NOT NULL
             ORDER BY embedding <=> $2::vector
             LIMIT $3`, id.String(), sourceVector, topK)
		if err != nil {
			return err
		}
		items = rows
		return nil
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// UpdateEmbedding 回填某动作的嵌入向量（异步补算任务/重试通道使用）：
// 向量落列、登记模型、状态置 SUCCEEDED 并清空 embed_error。
// RowsAffected=0 视为动作不存在（404），绝不静默假成功。
func (s *Store) UpdateEmbedding(ctx context.Context, id ids.ID, model string, vector []float32) error {
	if id.IsZero() {
		return errors.E(errors.CodeInvalidArgument, "action id 不合法")
	}
	if len(vector) == 0 {
		return errors.E(errors.CodeInvalidArgument, "向量不能为空")
	}
	return s.TenantTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
            UPDATE action.actions
               SET embedding = $2::vector, embedding_model = $3,
                   embed_status = 'SUCCEEDED', embed_error = ''
             WHERE id = $1`, id.String(), action.FormatVector(vector), model)
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "回填动作嵌入失败")
		}
		if tag.RowsAffected() == 0 {
			return errors.E(errors.CodeNotFound, "动作不存在")
		}
		return nil
	})
}
