package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// ListOntologyCategories 列出租户内全部分类（按 sort_order + 创建时间排序）。
func (s *Store) ListOntologyCategories(ctx context.Context, tenantID ids.ID) ([]map[string]any, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}
	var out []map[string]any
	err := s.TenantTxAs(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
		SELECT c.id::text AS id, COALESCE(c.parent_id::text, '') AS parent_id,
		       c.category_code, c.name, c.description, c.sort_order, c.status,
		       c.created_at, c.updated_at,
		       (SELECT COUNT(*) FROM ontology.ontologies o
		         WHERE o.category_id = c.id AND o.tenant_id = $1
		           AND o.status <> 'DELETED') AS ontology_count
		  FROM ontology.categories c
		 WHERE c.tenant_id = $1
		 ORDER BY c.parent_id NULLS FIRST, c.sort_order ASC, c.created_at ASC`,
			tenantID.String())
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "查询本体分类失败")
		}
		defer rows.Close()
		scanned, serr := scanRows(rows)
		if serr != nil {
			return serr
		}
		out = scanned
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CreateOntologyCategory 创建本体分类（根分类 parent_id 留空）。
func (s *Store) CreateOntologyCategory(ctx context.Context, tenantID, parentID ids.ID, code, name, description string, sortOrder int) (ids.ID, error) {
	if s == nil || s.Pool == nil {
		return ids.ID{}, errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}
	if code == "" || name == "" {
		return ids.ID{}, errors.E(errors.CodeInvalidArgument, "category_code / name 必填")
	}
	var newID ids.ID
	err := s.TenantTxAs(ctx, tenantID, func(tx pgx.Tx) error {
		newID = ids.New()
		var parentTxt any
		if !parentID.IsZero() {
			parentTxt = parentID.String()
			// parent 必须存在且属于同租户
			var cnt int
			if err := tx.QueryRow(ctx,
				`SELECT COUNT(*) FROM ontology.categories WHERE id = $1 AND tenant_id = $2`,
				parentID.String(), tenantID.String()).Scan(&cnt); err != nil {
				return errors.Wrap(err, errors.CodeInternal, "检查父分类失败")
			}
			if cnt == 0 {
				return errors.Ef(errors.CodeNotFound, "父分类 %s 不存在或不属于该租户", parentID)
			}
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO ontology.categories
			 (id, tenant_id, parent_id, category_code, name, description, sort_order, status)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, 'ACTIVE')`,
			newID.String(), tenantID.String(), parentTxt, code, name, description, sortOrder); err != nil {
			if isUniqueViolation(err) {
				return errors.Ef(errors.CodeConflict, "category_code %q 在该租户下已存在", code)
			}
			return errors.Wrap(err, errors.CodeInternal, "插入本体分类失败")
		}
		return nil
	})
	if err != nil {
		return ids.ID{}, err
	}
	return newID, nil
}

// UpdateOntologyCategory 更新本体分类（code 不可变）。
func (s *Store) UpdateOntologyCategory(ctx context.Context, tenantID, catID ids.ID, updates map[string]any) error {
	if s == nil || s.Pool == nil {
		return errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}
	return s.TenantTxAs(ctx, tenantID, func(tx pgx.Tx) error {
		sets := []string{}
		args := []any{catID.String()}
		i := 2
		if v, ok := updates["name"].(string); ok {
			sets = append(sets, "name = $"+itoa(i))
			args = append(args, v)
			i++
		}
		if v, ok := updates["description"].(string); ok {
			sets = append(sets, "description = $"+itoa(i))
			args = append(args, v)
			i++
		}
		if v, ok := updates["sort_order"].(int); ok {
			sets = append(sets, "sort_order = $"+itoa(i))
			args = append(args, v)
			i++
		}
		if v, ok := updates["status"].(string); ok {
			if v != "ACTIVE" && v != "INACTIVE" {
				return errors.Ef(errors.CodeInvalidArgument, "status %q 非法，仅 ACTIVE/INACTIVE", v)
			}
			sets = append(sets, "status = $"+itoa(i))
			args = append(args, v)
			i++
		}
		if len(sets) == 0 {
			return errors.E(errors.CodeInvalidArgument, "没有可更新字段")
		}

		// tenant_id 用独立占位符（早期 `tenant_id = $1` 与 cat_id 撞位，恒命中 0 行）。
		q := `UPDATE ontology.categories SET ` + stringsJoin(sets, ", ") + `, updated_at = now()
	         WHERE id = $1 AND tenant_id = $` + itoa(i)
		args = append(args, tenantID.String())
		tag, err := tx.Exec(ctx, q, args...)
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "更新本体分类失败")
		}
		if tag.RowsAffected() == 0 {
			return errors.Ef(errors.CodeNotFound, "本体分类 %s 不存在或不属于该租户", catID)
		}
		return nil
	})
}

// DeleteOntologyCategory 删除本体分类。
//
// 软约束：如果分类下存在本体（ontology.ontologies.category_id 引用），
// 返回 409 CONFLICT 拒绝删除，调用方应先处理关联本体或改用停用（status→INACTIVE）。
// 已软删除（status=DELETED）的本体不算"存在关联内容"：它们已从工作视图移除，
// 若仍据此阻止删除分类，调用方会陷入"删不掉分类又找不到是哪个本体挡着"的困境。
func (s *Store) DeleteOntologyCategory(ctx context.Context, tenantID, catID ids.ID) error {
	if s == nil || s.Pool == nil {
		return errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}
	return s.TenantTxAs(ctx, tenantID, func(tx pgx.Tx) error {
		// 1. 检查是否有本体引用
		var usageCount int
		if err := tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM ontology.ontologies WHERE tenant_id = $1 AND category_id = $2 AND status <> 'DELETED'`,
			tenantID.String(), catID.String()).Scan(&usageCount); err != nil {
			return errors.Wrap(err, errors.CodeInternal, "检查本体分类引用失败")
		}
		if usageCount > 0 {
			return errors.Ef(errors.CodeConflict,
				"分类下有 %d 个本体，请先处理关联或改为停用（status→INACTIVE）", usageCount)
		}

		// 2. 检查是否有子分类（先删子再删父，或让调用方先停用整棵子树）
		var childCount int
		if err := tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM ontology.categories WHERE tenant_id = $1 AND parent_id = $2`,
			tenantID.String(), catID.String()).Scan(&childCount); err != nil {
			return errors.Wrap(err, errors.CodeInternal, "检查子分类失败")
		}
		if childCount > 0 {
			return errors.Ef(errors.CodeConflict,
				"分类下还有 %d 个子分类，请先处理子分类", childCount)
		}

		// 3. 删除。tenant_id 用独立占位符：早期 `tenant_id = $1` 与 cat_id 撞位，
		//    DELETE 恒命中 0 行 → 删除接口恒返回 404。
		tag, err := tx.Exec(ctx,
			`DELETE FROM ontology.categories WHERE id = $1 AND tenant_id = $2`,
			catID.String(), tenantID.String())
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "删除本体分类失败")
		}
		if tag.RowsAffected() == 0 {
			return errors.Ef(errors.CodeNotFound, "本体分类 %s 不存在或不属于该租户", catID)
		}
		return nil
	})
}

// GetOntologyCategory 查询单个分类（带引用计数）。
func (s *Store) GetOntologyCategory(ctx context.Context, tenantID, catID ids.ID) (map[string]any, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}
	var item map[string]any
	err := s.TenantTxAs(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
		SELECT c.id::text AS id, COALESCE(c.parent_id::text, '') AS parent_id,
		       c.category_code, c.name, c.description, c.sort_order, c.status,
		       c.created_at, c.updated_at,
		       (SELECT COUNT(*) FROM ontology.ontologies o
		         WHERE o.category_id = c.id AND o.tenant_id = $1
		           AND o.status <> 'DELETED') AS ontology_count
		  FROM ontology.categories c
		 WHERE c.tenant_id = $1 AND c.id = $2`,
			tenantID.String(), catID.String())
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "查询本体分类失败")
		}
		defer rows.Close()
		items, serr := scanRows(rows)
		if serr != nil {
			return errors.Wrap(serr, errors.CodeInternal, "扫描本体分类失败")
		}
		if len(items) == 0 {
			return errors.Ef(errors.CodeNotFound, "本体分类 %s 不存在或不属于该租户", catID)
		}
		item = items[0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return item, nil
}
