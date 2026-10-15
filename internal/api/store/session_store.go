package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// GetSessionProjection 返回当前会话的用户投影 + 权限数组。
//
// 查询路径：
//  1. iam.users + iam.memberships + iam.organizations → 拼 org_path（如 "财务部/费用组"）
//  2. iam.tenants → tenant_name
//  3. governance.role_bindings → governance.roles → governance.role_permissions → governance.permissions
//     → 去重后的 permission_key 数组
//
// 权限展开说明：governance.permissions 是**全局共享目录**（不按租户隔离），
// role_permissions 连接 role_id → permission_id，role_bindings 连接 membership → role。
func (s *Store) GetSessionProjection(ctx context.Context, tenantID, userID ids.ID) (map[string]any, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}
	out := map[string]any{
		"permissions": []string{},
	}
	err := s.TenantTxAs(ctx, tenantID, func(tx pgx.Tx) error {
		// --- 基本用户信息 + org_path ---
		var (
			loginName   string
			displayName string
			email       string
			tenantName  string
			orgPath     string
		)
		if err := tx.QueryRow(ctx, `
		SELECT u.login_name, u.display_name, COALESCE(u.email, ''),
		       COALESCE(t.name, ''),
		       COALESCE(
		         (SELECT string_agg(o.name, '/' ORDER BY o.org_code)
		            FROM iam.memberships m
		            JOIN iam.organizations o ON o.id = m.org_id AND o.tenant_id = m.tenant_id
		           WHERE m.user_id = u.id AND m.tenant_id = $1 AND m.status = 'ACTIVE'),
		         '')
		  FROM iam.users u
		  JOIN iam.tenants t ON t.id = $1
		 WHERE u.id = $2`,
			tenantID.String(), userID.String()).
			Scan(&loginName, &displayName, &email, &tenantName, &orgPath); err != nil {
			if err == pgx.ErrNoRows {
				return errors.E(errors.CodeNotFound, "会话主体不存在或已被移除")
			}
			return errors.Wrap(err, errors.CodeInternal, "查询用户会话投影失败")
		}

		out["subject_id"] = userID.String()
		out["login_name"] = loginName
		out["display_name"] = displayName
		out["email"] = email
		out["tenant_id"] = tenantID.String()
		out["tenant_name"] = tenantName
		out["org_path"] = orgPath

		// --- 权限展开 ---
		// 从 memberships → role_bindings → roles → role_permissions → permissions
		rows, err := tx.Query(ctx, `
		SELECT DISTINCT p.permission_key
		  FROM iam.memberships m
		  JOIN governance.role_bindings rb ON rb.membership_id = m.id AND rb.tenant_id = m.tenant_id
		  JOIN governance.roles r ON r.id = rb.role_id AND r.tenant_id = rb.tenant_id
		  JOIN governance.role_permissions rp ON rp.role_id = r.id AND rp.tenant_id = rb.tenant_id
		  JOIN governance.permissions p ON p.id = rp.permission_id
		 WHERE m.user_id = $2
		   AND m.tenant_id = $1
		   AND m.status = 'ACTIVE'
		   AND (rb.valid_until IS NULL OR rb.valid_until > now())
		 ORDER BY p.permission_key`,
			tenantID.String(), userID.String())
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "查询权限投影失败")
		}
		defer rows.Close()
		var perms []string
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				return errors.Wrap(err, errors.CodeInternal, "扫描权限键失败")
			}
			perms = append(perms, key)
		}
		if err := rows.Err(); err != nil {
			return errors.Wrap(err, errors.CodeInternal, "遍历权限键失败")
		}
		if perms == nil {
			perms = []string{}
		}
		out["permissions"] = perms
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
