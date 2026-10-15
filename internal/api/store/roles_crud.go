// Package store — 角色（governance.roles）与权限目录的读写。
//
// 设计意图（第十七篇 §8.5 / 第二篇 §11.2）：平台只内置**一个**系统管理员角色
// （is_system_role = TRUE），其余角色一律由管理员按需"人工重建"（自定义角色）。
//   - 新建：写入 is_system_role = FALSE 的自定义角色，可同时绑定权限目录中的权限键；
//   - 删除：系统内置角色恒不可删（避免管理员把自己锁死）；仍被绑定引用的角色不可删
//     （先解绑），拒绝时返回 409 并给出可操作原因，绝不静默。
//
// 安全约束：权限键必须先存在于全局权限目录 governance.permissions，禁止写入
// "伪权限"（否则会在授权展开时静默失效）。租户隔离经 app.current_tenant_id() 落实。
package store

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// CreateRole 创建一个自定义角色（is_system_role 恒为 FALSE）。
func (s *Store) CreateRole(ctx context.Context, roleKey, name, description string, permissions []string) (map[string]any, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}
	roleKey = strings.TrimSpace(roleKey)
	name = strings.TrimSpace(name)
	if roleKey == "" || name == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "role_key / name 必填")
	}
	newID := ids.New()
	out := map[string]any{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		// 校验权限键真实存在：不允许写入目录之外的权限键，避免"看似授权、实际无效"。
		if len(permissions) > 0 {
			missing, err := missingPermissionKeys(ctx, tx, permissions)
			if err != nil {
				return err
			}
			if len(missing) > 0 {
				return errors.Ef(errors.CodeInvalidArgument,
					"以下权限键不在权限目录中：%s", strings.Join(missing, ", "))
			}
		}

		if _, err := tx.Exec(ctx, `
            INSERT INTO governance.roles (id, tenant_id, role_key, name, description, is_system_role)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, FALSE)`,
			newID.String(), roleKey, name, description); err != nil {
			if isUniqueViolation(err) {
				return errors.Ef(errors.CodeConflict, "角色标识 %q 已存在", roleKey)
			}
			return errors.Wrap(err, errors.CodeInternal, "创建角色失败")
		}

		if len(permissions) > 0 {
			if _, err := tx.Exec(ctx, `
                INSERT INTO governance.role_permissions (tenant_id, role_id, permission_id)
                SELECT app.current_tenant_id(), $1, p.id
                  FROM governance.permissions p
                 WHERE p.permission_key = ANY($2)`,
				newID.String(), permissions); err != nil {
				return errors.Wrap(err, errors.CodeInternal, "写入角色权限失败")
			}
		}

		rows, err := queryMaps(ctx, tx, `
            SELECT id::text AS id, role_key, name, description, is_system_role, created_at
              FROM governance.roles WHERE id = $1`, newID.String())
		if err != nil {
			return err
		}
		out = rows[0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteRole 删除自定义角色。系统内置角色与仍被引用的角色一律拒绝（409）。
func (s *Store) DeleteRole(ctx context.Context, id ids.ID) error {
	if s == nil || s.Pool == nil {
		return errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}
	return s.TenantTx(ctx, func(tx pgx.Tx) error {
		var isSystem bool
		if err := tx.QueryRow(ctx,
			`SELECT is_system_role FROM governance.roles WHERE id = $1`,
			id.String()).Scan(&isSystem); err != nil {
			if err == pgx.ErrNoRows {
				return errors.E(errors.CodeNotFound, "角色不存在")
			}
			return errors.Wrap(err, errors.CodeInternal, "查询角色失败")
		}
		if isSystem {
			return errors.E(errors.CodeConflict, "系统内置角色不可删除")
		}

		var bound int
		if err := tx.QueryRow(ctx,
			`SELECT count(*)::int FROM governance.role_bindings WHERE role_id = $1`,
			id.String()).Scan(&bound); err != nil {
			return errors.Wrap(err, errors.CodeInternal, "检查角色绑定失败")
		}
		if bound > 0 {
			return errors.Ef(errors.CodeConflict, "角色仍被 %d 条绑定引用，请先解绑后再删除", bound)
		}

		if _, err := tx.Exec(ctx,
			`DELETE FROM governance.role_permissions WHERE role_id = $1`, id.String()); err != nil {
			return errors.Wrap(err, errors.CodeInternal, "清理角色权限失败")
		}
		tag, err := tx.Exec(ctx, `DELETE FROM governance.roles WHERE id = $1`, id.String())
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "删除角色失败")
		}
		if tag.RowsAffected() == 0 {
			return errors.E(errors.CodeNotFound, "角色不存在")
		}
		return nil
	})
}

// ListPermissions 返回全局权限目录（供角色创建界面选择真实权限键）。
func (s *Store) ListPermissions(ctx context.Context) (map[string]any, error) {
	out := map[string]any{"items": []map[string]any{}}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT permission_key, resource_type, action, description
              FROM governance.permissions ORDER BY permission_key ASC`)
		if err != nil {
			return err
		}
		if rows == nil {
			rows = []map[string]any{}
		}
		out["items"] = rows
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// missingPermissionKeys 返回入参中不存在于权限目录的权限键。
func missingPermissionKeys(ctx context.Context, tx pgx.Tx, keys []string) ([]string, error) {
	rows, err := tx.Query(ctx, `
        SELECT k FROM unnest($1::text[]) AS k
         WHERE NOT EXISTS (
             SELECT 1 FROM governance.permissions p WHERE p.permission_key = k)`, keys)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "校验权限键失败")
	}
	defer rows.Close()
	var missing []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, errors.Wrap(err, errors.CodeInternal, "读取权限键失败")
		}
		missing = append(missing, k)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "读取权限键失败")
	}
	return missing, nil
}
