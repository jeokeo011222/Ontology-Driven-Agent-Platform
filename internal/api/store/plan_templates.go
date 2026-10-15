package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// listPlanTemplatesSQL 读取租户的工作流模板资产。
//
// 模板与执行计划共用 planning.plans / planning.plan_revisions（迁移 000010 建表），
// 以 plans.status = 'TEMPLATE' 为判别值区分"可复用模板资产"与"执行实例"。
//
// 缺陷 #24 记录：本端点此前无条件返回 503 并断言"plan-templates 资产在 MVP 数据库中
// 无对应持久表"。该断言在种入 plan_ir.template = true / plans.status = 'TEMPLATE' 的
// 模板行之后已经失真——表在、行也在，只是查询从未被写出来。此处按真实数据读取。
//
// 投影口径：
//   - 取每个模板的**最新修订**（revision_number 降序），与前端"不可变版本"语义一致；
//   - name / ontology_ref 取自 plan_ir（业务定义落在数据文件，不额外落库为列）；
//   - node_kinds 由 plan_ir.steps[].kind 展开为 text[]，对齐第十七篇 §7.3 节点词表；
//   - state 由修订的 validation_status 归一为大写生命周期值（契约统一大写枚举）。
//
// 返回 text[] 而非 jsonb：pgx 对 jsonb 返回 []byte，经 encoding/json 会被编成
// base64 字符串；text[] 映射为 []string，正是前端期望的字符串数组。
const listPlanTemplatesSQL = `
SELECT p.plan_id::text AS id,
       COALESCE(r.plan_ir->>'name', p.domain || ' 工作流模板') AS name,
       p.domain,
       p.status,
       COALESCE(r.revision_number, 0) AS version,
       CASE COALESCE(r.validation_status, '')
            WHEN 'VALID'   THEN 'VALIDATED'
            WHEN 'INVALID' THEN 'REJECTED'
            ELSE 'DRAFT'
       END AS state,
       ARRAY(
           SELECT e->>'kind'
             FROM jsonb_array_elements(COALESCE(r.plan_ir->'steps', '[]'::jsonb)) AS e
       ) AS node_kinds,
       r.plan_ir->>'ontology_ref' AS ontology_ref,
       COALESCE(r.plan_revision_id::text, '') AS revision_id,
       COALESCE(r.created_at, p.created_at) AS updated_at
  FROM planning.plans p
  LEFT JOIN LATERAL (
      SELECT plan_revision_id, revision_number, plan_ir, validation_status, created_at
        FROM planning.plan_revisions
       WHERE plan_id = p.plan_id
       ORDER BY revision_number DESC
       LIMIT 1
  ) r ON TRUE
 WHERE p.status = 'TEMPLATE'
   AND ($1::text IS NULL OR p.plan_id::text > $1)
 ORDER BY p.plan_id ASC
 LIMIT $2`

// ListPlanTemplates 返回工作流模板资产（按 plan_id 升序 cursor 分页）。
func (s *Store) ListPlanTemplates(ctx context.Context, limit int, cursor string) (*ListResult, error) {
	from, err := decKey(cursor)
	if err != nil {
		return nil, err
	}
	res := &ListResult{Items: []map[string]any{}}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, listPlanTemplatesSQL, nullableStr(from), limit+1)
		if err != nil {
			return err
		}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}
		res.Items = rows
		if hasMore && len(rows) > 0 {
			if k, ok := rows[len(rows)-1]["id"].(string); ok {
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
