package store

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	ierr "odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
)

// RegisterTenantFirst 是注册时租户已存在的场景。
// Register 方法返回 (tenantID, userID, membershipID, rootOrgID, error)。

// RegisterUser 是注册流程的事务内核：
// 1. 按 org_code 查 organization → 拿到 tenant_id
// 2. 创建 user + credentials（bcrypt）+ membership（user-org 关联）
// 返回 userID / membershipID 供 SessionService.Issue 使用。
func (s *Store) RegisterUser(ctx context.Context, orgCode, loginName, displayName, email, password string) (tenantID ids.ID, userID ids.ID, membershipID ids.ID, err error) {
	if s == nil || s.Pool == nil {
		return ids.ID{}, ids.ID{}, ids.ID{}, ierr.E(ierr.CodeUnavailable, "数据库连接池未就绪")
	}
	if orgCode == "" || loginName == "" || displayName == "" || password == "" {
		return ids.ID{}, ids.ID{}, ids.ID{}, ierr.E(ierr.CodeInvalidArgument, "org_code / login_name / display_name / password 必填")
	}
	if len(password) < 6 {
		return ids.ID{}, ids.ID{}, ids.ID{}, ierr.E(ierr.CodeValidationFailed, "密码至少 6 位")
	}

	// bcrypt 哈希（cost=10）。盐由 bcrypt 内部生成，不需要单独 password_salt 字段；
	// credentials.password_salt 在此简单复用 bcrypt hash 前缀 $2a$10$...
	hash, berr := bcrypt.GenerateFromPassword([]byte(password), 10)
	if berr != nil {
		return ids.ID{}, ids.ID{}, ids.ID{}, ierr.Wrap(berr, ierr.CodeInternal, "密码哈希失败")
	}
	hashStr := string(hash)

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return ids.ID{}, ids.ID{}, ids.ID{}, ierr.Wrap(err, ierr.CodeInternal, "开启事务失败")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. 按 org_code 查组织，拿到 tenant_id
	//
	// 走 SECURITY DEFINER 解析函数而不是直查 iam.organizations：注册发生在租户上下文
	// 建立之前，而该表启用了 FORCE ROW LEVEL SECURITY，直查会被策略挡成 0 行。
	// 详见迁移 000022。
	var tenantTxt *string
	err = tx.QueryRow(ctx,
		`SELECT app.resolve_org_tenant($1)::text`, orgCode).Scan(&tenantTxt)
	if err != nil {
		return ids.ID{}, ids.ID{}, ids.ID{}, ierr.Wrap(err, ierr.CodeInternal, "查询组织失败")
	}
	if tenantTxt == nil {
		return ids.ID{}, ids.ID{}, ids.ID{}, ierr.Ef(ierr.CodeNotFound, "org_code %q 对应的组织不存在", orgCode)
	}
	if tenantID, err = ids.Parse(*tenantTxt); err != nil {
		return ids.ID{}, ids.ID{}, ids.ID{}, ierr.Wrap(err, ierr.CodeInternal, "tenant_id 非法")
	}

	// 2. 注入租户上下文（后续 membership 插入需要 RLS）。
	//
	// 此处不能改用 s.TenantTxAs：注册发生在租户身份建立之前，tenantID 是同事务内
	// 上一步由 app.resolve_org_tenant() 反查得到的，无法从 context 取得。
	// 因此直接复用与 TenantTx / TenantTxAs 完全相同的底层原语
	// pgxutil.WithTenantTx，保证"写 app.tenant_id"的代码全仓唯一。
	if err := pgxutil.WithTenantTx(ctx, tx, tenantID); err != nil {
		return ids.ID{}, ids.ID{}, ids.ID{}, ierr.Wrap(err, ierr.CodeInternal, "注入租户上下文失败")
	}

	// 3. 创建 user
	userID = ids.New()
	_, err = tx.Exec(ctx,
		`INSERT INTO iam.users (id, login_name, display_name, email, subject_version, status)
		 VALUES ($1, $2, $3, $4, 1, 'ACTIVE')`,
		userID.String(), loginName, displayName, email)
	if err != nil {
		if isUniqueViolation(err) {
			return ids.ID{}, ids.ID{}, ids.ID{}, ierr.Ef(ierr.CodeConflict, "login_name %q 已存在", loginName)
		}
		return ids.ID{}, ids.ID{}, ids.ID{}, ierr.Wrap(err, ierr.CodeInternal, "插入用户失败")
	}

	// 4. 创建 credentials
	_, err = tx.Exec(ctx,
		`INSERT INTO iam.credentials (id, user_id, password_hash, password_salt)
		 VALUES ($1, $2, $3, $4)`,
		ids.New().String(), userID.String(), hashStr, hashStr)
	if err != nil {
		return ids.ID{}, ids.ID{}, ids.ID{}, ierr.Wrap(err, ierr.CodeInternal, "插入凭据失败")
	}

	// 5. 创建 membership（把用户加入注册用的 org）
	//
	// 子查询按 (tenant_id, org_code) 定位组织：此处租户上下文已由 pgxutil.WithTenantTx
	// 注入（app.tenant_id），RLS 允许读取本租户的组织；若把 tenant_id 与 org_code 混用
	// 同一参数会永远匹配不到行，导致 org_id 为 NULL。
	membershipID = ids.New()
	_, err = tx.Exec(ctx,
		`INSERT INTO iam.memberships (id, tenant_id, user_id, org_id, membership_type, status)
		 VALUES ($1, $2, $3,
		         (SELECT id FROM iam.organizations WHERE tenant_id = $2 AND org_code = $4 LIMIT 1),
		         'EMPLOYEE', 'ACTIVE')`,
		membershipID.String(), tenantID.String(), userID.String(), orgCode)
	if err != nil {
		return ids.ID{}, ids.ID{}, ids.ID{}, ierr.Wrap(err, ierr.CodeInternal, "插入成员关系失败")
	}

	if err := tx.Commit(ctx); err != nil {
		return ids.ID{}, ids.ID{}, ids.ID{}, ierr.Wrap(err, ierr.CodeInternal, "提交事务失败")
	}
	return tenantID, userID, membershipID, nil
}

// VerifyPassword 按 login_name 查用户 + 凭据并校验密码。
// 成功返回 tenantID, userID, subjectVersion；用户不存在或密码错误一律返回 CodeUnauthenticated，
// 不区分"用户不存在"和"密码错误"以防范用户名枚举攻击。
func (s *Store) VerifyPassword(ctx context.Context, loginName, password string) (tenantID ids.ID, userID ids.ID, subjectVersion int64, err error) {
	if s == nil || s.Pool == nil {
		return ids.ID{}, ids.ID{}, 0, ierr.E(ierr.CodeUnavailable, "数据库连接池未就绪")
	}
	// 查询：login_name 全局唯一，credentials 通过 user_id 关联，
	// organization 反推 tenant_id（跨组织全局唯一 login_name + org_code 平台唯一都能定位 tenant）。
	var (
		userTxt    string
		hash       string
		tenantTxt  *string
		version    int64
		userStatus string
	)
	err = s.Pool.QueryRow(ctx,
		`SELECT u.id::text, c.password_hash, u.subject_version, u.status,
		        app.resolve_login_tenant($1)::text
		   FROM iam.users u
		   LEFT JOIN iam.credentials c ON c.user_id = u.id
		   WHERE u.login_name = $1`, loginName).
		Scan(&userTxt, &hash, &version, &userStatus, &tenantTxt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return ids.ID{}, ids.ID{}, 0, ierr.E(ierr.CodeUnauthenticated, "登录名或密码错误")
		}
		return ids.ID{}, ids.ID{}, 0, ierr.Wrap(err, ierr.CodeInternal, "查询凭据失败")
	}
	if userStatus != "ACTIVE" {
		return ids.ID{}, ids.ID{}, 0, ierr.Ef(ierr.CodePermissionDenied, "用户状态 %s，拒绝登录", userStatus)
	}
	if tenantTxt == nil {
		return ids.ID{}, ids.ID{}, 0, ierr.E(ierr.CodeUnauthenticated, "用户未加入任何组织，无法登录")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return ids.ID{}, ids.ID{}, 0, ierr.E(ierr.CodeUnauthenticated, "登录名或密码错误")
	}
	if userID, err = ids.Parse(userTxt); err != nil {
		return ids.ID{}, ids.ID{}, 0, ierr.Wrap(err, ierr.CodeInternal, "user_id 非法")
	}
	if tenantID, err = ids.Parse(*tenantTxt); err != nil {
		return ids.ID{}, ids.ID{}, 0, ierr.Wrap(err, ierr.CodeInternal, "tenant_id 非法")
	}
	return tenantID, userID, version, nil
}

// --- Revoke session (logout) ---

// RevokeSessionByID 撤销指定会话（Logout 用）。
func (s *Store) RevokeSessionByID(ctx context.Context, tenantID, sessionID ids.ID) error {
	if s == nil || s.Pool == nil {
		return ierr.E(ierr.CodeUnavailable, "数据库连接池未就绪")
	}
	return s.TenantTxAs(ctx, tenantID, func(tx pgx.Tx) error {
		// 注意占位符必须区分：tenant_id 与 id 是两个不同的比较值。
		// 早期写作 `WHERE tenant_id = $1 AND id = $1`（只传 1 个参数），
		// 等价于要求 tenant_id = session_id，永远匹配不到行 → 注销恒返回 404。
		tag, err := tx.Exec(ctx,
			`UPDATE iam.sessions SET revoked_at = now(), revoke_reason = 'logout'
			 WHERE tenant_id = $1 AND id = $2 AND revoked_at IS NULL`,
			tenantID.String(), sessionID.String())
		if err != nil {
			return ierr.Wrap(err, ierr.CodeInternal, "撤销会话失败")
		}
		if tag.RowsAffected() == 0 {
			return ierr.Ef(ierr.CodeNotFound, "会话 %s 不存在或已撤销", sessionID)
		}
		return nil
	})
}

// --- AdminCreateUser（管理员创建用户，走 tenant context）---

// AdminCreateUser 在指定租户内创建 user + credentials + membership（按 org_code 查 org）。
// 与 RegisterUser 不同的是：
//   - 需要 caller 已经知道 tenantID（管理员上下文）
//   - 不签发会话
//   - membership_type 可自定义
func (s *Store) AdminCreateUser(ctx context.Context, tenantID ids.ID, loginName, displayName, email, password, orgCode, mtype string) (ids.ID, ids.ID, error) {
	if s == nil || s.Pool == nil {
		return ids.ID{}, ids.ID{}, ierr.E(ierr.CodeUnavailable, "数据库连接池未就绪")
	}
	if loginName == "" || displayName == "" || password == "" || orgCode == "" {
		return ids.ID{}, ids.ID{}, ierr.E(ierr.CodeInvalidArgument, "login_name/display_name/password/org_code 必填")
	}
	if mtype == "" {
		mtype = "EMPLOYEE"
	}
	hash, berr := bcrypt.GenerateFromPassword([]byte(password), BcryptCost)
	if berr != nil {
		return ids.ID{}, ids.ID{}, ierr.Wrap(berr, ierr.CodeInternal, "密码哈希失败")
	}
	hashStr := string(hash)

	var userID, membershipID ids.ID
	err := s.TenantTxAs(ctx, tenantID, func(tx pgx.Tx) error {
		// 查 org
		var orgIDTxt string
		if err := tx.QueryRow(ctx,
			`SELECT id::text FROM iam.organizations WHERE tenant_id = $1 AND org_code = $2 LIMIT 1`,
			tenantID.String(), orgCode).Scan(&orgIDTxt); err != nil {
			if err == pgx.ErrNoRows {
				return ierr.Ef(ierr.CodeNotFound, "org_code %q 在当前租户不存在", orgCode)
			}
			return ierr.Wrap(err, ierr.CodeInternal, "查 org 失败")
		}

		// 建 user
		userID = ids.New()
		if _, err := tx.Exec(ctx,
			`INSERT INTO iam.users (id, login_name, display_name, email, subject_version, status)
			 VALUES ($1, $2, $3, $4, 1, 'ACTIVE')`,
			userID.String(), loginName, displayName, email); err != nil {
			if isUniqueViolation(err) {
				return ierr.Ef(ierr.CodeConflict, "login_name %q 已存在", loginName)
			}
			return ierr.Wrap(err, ierr.CodeInternal, "插入用户失败")
		}

		// 建 credentials
		if _, err := tx.Exec(ctx,
			`INSERT INTO iam.credentials (id, user_id, password_hash, password_salt)
			 VALUES ($1, $2, $3, $4)`,
			ids.New().String(), userID.String(), hashStr, hashStr); err != nil {
			return ierr.Wrap(err, ierr.CodeInternal, "插入凭据失败")
		}

		// 建 membership
		membershipID = ids.New()
		if _, err := tx.Exec(ctx,
			`INSERT INTO iam.memberships (id, tenant_id, user_id, org_id, membership_type, status)
			 VALUES ($1, $2, $3, $4, $5, 'ACTIVE')`,
			membershipID.String(), tenantID.String(), userID.String(), orgIDTxt, mtype); err != nil {
			return ierr.Wrap(err, ierr.CodeInternal, "插入成员关系失败")
		}
		return nil
	})
	if err != nil {
		return ids.ID{}, ids.ID{}, err
	}
	return userID, membershipID, nil
}

// --- helpers ---

// 注意：这里**没有**本地 setTenant 辅助函数。
//
// 早期版本在此实现了 `SET LOCAL app.current_tenant_id = $1`，这是一个必然失败的
// 写法（SET 不接受占位符；且 RLS 读的是 app.tenant_id 而非该名字），并且制造了
// "第二份租户注入实现"，与 middleware.TenantTx 形成两套真相。
// 现已全部收敛到 s.TenantTxAs / pgxutil.WithTenantTx 单一实现。

// TenantIDFromOrgCode 按 org_code 反查 tenantID（Register 端点用的简写）。
func (s *Store) TenantIDFromOrgCode(ctx context.Context, orgCode string) (ids.ID, error) {
	tid, _, err := s.TenantKeyFromOrgCode(ctx, orgCode)
	return tid, err
}

// TenantKeyFromOrgCode 按组织业务码反查 tenant_key（注册时租户可能尚未存在）。
// 如果组织不存在但 org_code 恰好等于某租户的 tenant_key，返回对应 tenant。
func (s *Store) TenantKeyFromOrgCode(ctx context.Context, orgCode string) (tenantID ids.ID, tenantKey string, err error) {
	if s == nil || s.Pool == nil {
		return ids.ID{}, "", ierr.E(ierr.CodeUnavailable, "数据库连接池未就绪")
	}
	// 先查 organizations（最常见路径）。同样走 SECURITY DEFINER 解析函数：
	// 注册前没有租户上下文，直查 iam.organizations 会被 RLS 挡成 0 行（迁移 000022）。
	var txt *string
	err = s.Pool.QueryRow(ctx,
		`SELECT app.resolve_org_tenant($1)::text`, orgCode).Scan(&txt)
	if err != nil {
		return ids.ID{}, "", ierr.Wrap(err, ierr.CodeInternal, "查 organization 失败")
	}
	if txt != nil {
		if tenantID, err = ids.Parse(*txt); err != nil {
			return ids.ID{}, "", ierr.Wrap(err, ierr.CodeInternal, "tenant_id 非法")
		}
		if err := s.Pool.QueryRow(ctx,
			`SELECT tenant_key FROM iam.tenants WHERE id = $1`, tenantID.String()).Scan(&tenantKey); err != nil {
			return ids.ID{}, "", ierr.Wrap(err, ierr.CodeInternal, "查 tenant_key 失败")
		}
		return tenantID, tenantKey, nil
	}
	// 降级：org_code 即 tenant_key（组织尚未创建，先注册租户）
	err = s.Pool.QueryRow(ctx,
		`SELECT id::text, tenant_key FROM iam.tenants WHERE tenant_key = $1 LIMIT 1`,
		orgCode).Scan(&txt, &tenantKey)
	if err != nil {
		if err == pgx.ErrNoRows {
			return ids.ID{}, "", ierr.Ef(ierr.CodeNotFound, "org_code %q 未找到对应租户或组织", orgCode)
		}
		return ids.ID{}, "", ierr.Wrap(err, ierr.CodeInternal, "查 tenant 失败")
	}
	if tenantID, err = ids.Parse(*txt); err != nil {
		return ids.ID{}, "", ierr.Wrap(err, ierr.CodeInternal, "tenant_id 非法")
	}
	return tenantID, tenantKey, nil
}

// --- bcrypt ---

// BcryptCost 是密码哈希成本（生产=10）。
const BcryptCost = 10

// hashPassword 工具方法（admin 创建用户时复用）。
func hashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), BcryptCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// isUniqueViolation 复用自 store.go（用 sqlstate 23505 判断）。
func isUniqueViolation(err error) bool {
	return stringsContains(err.Error(), "SQLSTATE 23505") || stringsContains(err.Error(), "23505")
}

func stringsContains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsCI(s, sub))
}

func containsCI(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if equalFold(s[i:i+len(sub)], sub) {
			return true
		}
	}
	return false
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ai, bi := a[i], b[i]
		if ai >= 'A' && ai <= 'Z' {
			ai += 32
		}
		if bi >= 'A' && bi <= 'Z' {
			bi += 32
		}
		if ai != bi {
			return false
		}
	}
	return true
}

// ListUsersCursor 返回用户列表（按登录名字典序 cursor 分页，支持 ?status= 与 ?q=）。
// 业务约束：iam.users 无 tenant_id 列，通过 memberships + organizations 反租。
func (s *Store) ListUsersCursor(ctx context.Context, tenantID ids.ID, cursor string, limit int, status, search string) ([]map[string]any, string, error) {
	if s == nil || s.Pool == nil {
		return nil, "", ierr.E(ierr.CodeUnavailable, "数据库连接池未就绪")
	}
	if limit <= 0 {
		limit = 10
	}
	var out []map[string]any
	var nextCursor string
	err := s.TenantTxAs(ctx, tenantID, func(tx pgx.Tx) error {
		// base64 解码 cursor 为 login_name（按登录名字典序分页）
		loginPrefix, _ := decodeCursor(cursor)

		q := `SELECT u.id::text AS id, u.login_name, u.display_name, COALESCE(u.email, '') AS email,
		        u.status, u.subject_version, u.created_at, u.updated_at,
		        STRING_AGG(o.name || ':' || m.membership_type, ',') FILTER (WHERE m.status = 'ACTIVE') AS memberships
		   FROM iam.users u
		   JOIN iam.memberships m ON m.user_id = u.id AND m.tenant_id = $1
		   JOIN iam.organizations o ON o.id = m.org_id AND o.tenant_id = $1`
		args := []any{tenantID.String()}
		conds := []string{}
		if loginPrefix != "" {
			args = append(args, loginPrefix)
			conds = append(conds, `u.login_name > `+pgxPlaceholder(len(args)))
		}
		if status != "" {
			args = append(args, status)
			conds = append(conds, `u.status = `+pgxPlaceholder(len(args)))
		}
		if search != "" {
			args = append(args, search)
			p := pgxPlaceholder(len(args))
			conds = append(conds,
				`(u.login_name ILIKE '%' || `+p+` || '%' OR coalesce(u.display_name, '') ILIKE '%' || `+p+` || '%' OR coalesce(u.email, '') ILIKE '%' || `+p+` || '%')`)
		}
		if len(conds) > 0 {
			q += ` WHERE ` + strings.Join(conds, ` AND `)
		}
		q += ` GROUP BY u.id`
		q += ` ORDER BY u.login_name ASC LIMIT ` + pgxPlaceholder(len(args)+1)
		args = append(args, limit+1)

		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return ierr.Wrap(err, ierr.CodeInternal, "查询用户列表失败")
		}
		defer rows.Close()

		scanned, serr := scanRows(rows)
		if serr != nil {
			return serr
		}
		out = scanned
		if len(out) > limit {
			out = out[:limit]
			if lastLogin, ok := out[limit-1]["login_name"].(string); ok {
				nextCursor = encodeCursor(lastLogin)
			}
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return out, nextCursor, nil
}

func pgxPlaceholder(n int) string {
	return "$" + itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [10]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// GetUserWithMemberships 按 ID 查询单个用户（含 memberships）。
func (s *Store) GetUserWithMemberships(ctx context.Context, tenantID, userID ids.ID) (map[string]any, error) {
	if s == nil || s.Pool == nil {
		return nil, ierr.E(ierr.CodeUnavailable, "数据库连接池未就绪")
	}
	var item map[string]any
	err := s.TenantTxAs(ctx, tenantID, func(tx pgx.Tx) error {
		q := `SELECT u.id::text AS id, u.login_name, u.display_name, COALESCE(u.email, '') AS email,
		        u.status, u.subject_version, u.created_at, u.updated_at,
		        STRING_AGG(o.id::text || ':' || o.name, ',') FILTER (WHERE m.status = 'ACTIVE') AS organizations
		   FROM iam.users u
		   JOIN iam.memberships m ON m.user_id = u.id AND m.tenant_id = $1
		   JOIN iam.organizations o ON o.id = m.org_id AND o.tenant_id = $1
		  WHERE u.id = $2
		  GROUP BY u.id LIMIT 1`
		rows, err := tx.Query(ctx, q, tenantID.String(), userID.String())
		if err != nil {
			return ierr.Wrap(err, ierr.CodeInternal, "查询用户失败")
		}
		defer rows.Close()
		items, serr := scanRows(rows)
		if serr != nil {
			return ierr.Wrap(serr, ierr.CodeInternal, "查询用户失败")
		}
		if len(items) == 0 {
			return ierr.Ef(ierr.CodeNotFound, "用户 %s 不存在或不属于该租户", userID)
		}
		item = items[0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return item, nil
}

// UpdateUser 管理员更新用户基本信息（display_name / email）与可选的重置密码。
//
// 密码处理：updates["password"] 为非空字符串时，用 bcrypt 重新哈希并 **UPSERT**
// iam.credentials（user_id 唯一），同时清空失败计数与锁定。凭据明文绝不落库、不回显。
func (s *Store) UpdateUser(ctx context.Context, tenantID, userID ids.ID, updates map[string]any) error {
	if s == nil || s.Pool == nil {
		return ierr.E(ierr.CodeUnavailable, "数据库连接池未就绪")
	}
	var newPassword string
	if v, ok := updates["password"].(string); ok && v != "" {
		if len(v) < 6 {
			return ierr.E(ierr.CodeValidationFailed, "密码至少 6 位")
		}
		newPassword = v
	}
	return s.TenantTxAs(ctx, tenantID, func(tx pgx.Tx) error {
		sets := []string{}
		args := []any{userID.String()}
		i := 2
		if v, ok := updates["display_name"].(string); ok {
			sets = append(sets, "display_name = $"+itoa(i))
			args = append(args, v)
			i++
		}
		if v, ok := updates["email"].(string); ok {
			sets = append(sets, "email = $"+itoa(i))
			args = append(args, v)
			i++
		}
		if len(sets) == 0 && newPassword == "" {
			return ierr.E(ierr.CodeInvalidArgument, "没有可更新字段")
		}
		// 只有基础字段需要 UPDATE iam.users；仅改密码时跳过（凭据单独更新）。
		if len(sets) > 0 {
			// tenant_id 必须使用独立占位符：早期写成 `tenant_id = $1`（而 $1 是 user_id），
			// 等价于要求 tenant_id = user_id，UPDATE 恒命中 0 行 → 接口恒返回 404。
			tenantPH := "$" + itoa(i)
			args = append(args, tenantID.String())
			q := `UPDATE iam.users SET ` + stringsJoin(sets, ", ") + `, updated_at = now()
		         WHERE id = $1 AND id IN (SELECT user_id FROM iam.memberships WHERE tenant_id = ` + tenantPH + `)`
			tag, err := tx.Exec(ctx, q, args...)
			if err != nil {
				return ierr.Wrap(err, ierr.CodeInternal, "更新用户失败")
			}
			if tag.RowsAffected() == 0 {
				return ierr.Ef(ierr.CodeNotFound, "用户 %s 不存在或不属于该租户", userID)
			}
		}

		if newPassword != "" {
			// 归属校验：用户必须属于本租户（iam.users 无 tenant_id，经 memberships 反租）。
			var belongs bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS(SELECT 1 FROM iam.memberships WHERE tenant_id = $1 AND user_id = $2)`,
				tenantID.String(), userID.String()).Scan(&belongs); err != nil {
				return ierr.Wrap(err, ierr.CodeInternal, "校验用户归属失败")
			}
			if !belongs {
				return ierr.Ef(ierr.CodeNotFound, "用户 %s 不存在或不属于该租户", userID)
			}
			hash, berr := bcrypt.GenerateFromPassword([]byte(newPassword), BcryptCost)
			if berr != nil {
				return ierr.Wrap(berr, ierr.CodeInternal, "密码哈希失败")
			}
			hashStr := string(hash)
			if _, err := tx.Exec(ctx, `
                INSERT INTO iam.credentials (id, user_id, password_hash, password_salt, last_changed_at, failed_attempts, locked_until)
                VALUES ($1, $2, $3, $4, now(), 0, NULL)
                ON CONFLICT (user_id) DO UPDATE
                   SET password_hash = EXCLUDED.password_hash,
                       password_salt = EXCLUDED.password_salt,
                       last_changed_at = now(),
                       failed_attempts = 0,
                       locked_until = NULL`,
				ids.New().String(), userID.String(), hashStr, hashStr); err != nil {
				return ierr.Wrap(err, ierr.CodeInternal, "重置密码失败")
			}
			if _, err := tx.Exec(ctx, `UPDATE iam.users SET updated_at = now() WHERE id = $1`,
				userID.String()); err != nil {
				return ierr.Wrap(err, ierr.CodeInternal, "更新用户时间失败")
			}
		}
		return nil
	})
}

// DisableUser 停用用户（status=DISABLED + 递增 subject_version，触发撤销传播）。
func (s *Store) DisableUser(ctx context.Context, tenantID, userID ids.ID) error {
	if s == nil || s.Pool == nil {
		return ierr.E(ierr.CodeUnavailable, "数据库连接池未就绪")
	}
	return s.TenantTxAs(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE iam.users SET status = 'DISABLED', subject_version = subject_version + 1, updated_at = now()
			 WHERE id = $1 AND id IN (SELECT user_id FROM iam.memberships WHERE tenant_id = $2)`,
			userID.String(), tenantID.String())
		if err != nil {
			return ierr.Wrap(err, ierr.CodeInternal, "停用用户失败")
		}
		if tag.RowsAffected() == 0 {
			return ierr.Ef(ierr.CodeNotFound, "用户 %s 不存在或不属于该租户", userID)
		}
		return nil
	})
}

// --- Organizations ---

// ListOrganizations 返回组织列表（cursor 分页 + ?q= 关键字，按 org_code 升序）。
func (s *Store) ListOrganizations(ctx context.Context, tenantID ids.ID, limit int, cursor, q string) (*ListResult, error) {
	if s == nil || s.Pool == nil {
		return nil, ierr.E(ierr.CodeUnavailable, "数据库连接池未就绪")
	}
	from, err := decKey(cursor)
	if err != nil {
		return nil, err
	}
	res := &ListResult{Items: []map[string]any{}}
	err = s.TenantTxAs(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id::text AS id, COALESCE(parent_id::text, '') AS parent_id,
			        org_code, name, org_type, status, created_at, updated_at
			   FROM iam.organizations
			  WHERE ($1::text IS NULL OR org_code > $1)
			    AND ($3::text = '' OR org_code ILIKE '%' || $3 || '%' OR name ILIKE '%' || $3 || '%')
			  ORDER BY org_code ASC
			  LIMIT $2`, nullableStr(from), limit+1, q)
		if err != nil {
			return ierr.Wrap(err, ierr.CodeInternal, "查询组织失败")
		}
		defer rows.Close()
		scanned, serr := scanRows(rows)
		if serr != nil {
			return serr
		}
		hasMore := len(scanned) > limit
		if hasMore {
			scanned = scanned[:limit]
		}
		res.Items = scanned
		if hasMore && len(scanned) > 0 {
			if k, ok := scanned[len(scanned)-1]["org_code"].(string); ok {
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

// CreateOrganization 在租户内创建组织（根组织 parent_id 留空），同时补 org_closure 自环行。
func (s *Store) CreateOrganization(ctx context.Context, tenantID, parentID ids.ID, code, name, orgType string) (ids.ID, error) {
	if s == nil || s.Pool == nil {
		return ids.ID{}, ierr.E(ierr.CodeUnavailable, "数据库连接池未就绪")
	}
	if code == "" || name == "" {
		return ids.ID{}, ierr.E(ierr.CodeInvalidArgument, "org_code / name 必填")
	}
	if orgType == "" {
		orgType = "DEPARTMENT"
	}
	var newID ids.ID
	err := s.TenantTxAs(ctx, tenantID, func(tx pgx.Tx) error {
		newID = ids.New()
		var parentTxt any
		if !parentID.IsZero() {
			parentTxt = parentID.String()
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO iam.organizations (id, tenant_id, parent_id, org_code, name, org_type, status)
			 VALUES ($1, $2, $3, $4, $5, $6, 'ACTIVE')`,
			newID.String(), tenantID.String(), parentTxt, code, name, orgType); err != nil {
			if isUniqueViolation(err) {
				return ierr.Ef(ierr.CodeConflict, "org_code %q 已存在", code)
			}
			return ierr.Wrap(err, ierr.CodeInternal, "插入组织失败")
		}
		// 闭包表自环行 depth=0
		if _, err := tx.Exec(ctx,
			`INSERT INTO iam.org_closure (tenant_id, ancestor_id, descendant_id, depth)
			 VALUES ($1, $2, $2, 0)`, tenantID.String(), newID.String()); err != nil {
			return ierr.Wrap(err, ierr.CodeInternal, "插入闭包失败")
		}
		return nil
	})
	if err != nil {
		return ids.ID{}, err
	}
	return newID, nil
}

// UpdateOrganization 更新组织（code 不可变）。
func (s *Store) UpdateOrganization(ctx context.Context, tenantID, orgID ids.ID, updates map[string]any) error {
	if s == nil || s.Pool == nil {
		return ierr.E(ierr.CodeUnavailable, "数据库连接池未就绪")
	}
	return s.TenantTxAs(ctx, tenantID, func(tx pgx.Tx) error {
		sets := []string{}
		args := []any{orgID.String()}
		i := 2
		if v, ok := updates["name"].(string); ok {
			sets = append(sets, "name = $"+itoa(i))
			args = append(args, v)
			i++
		}
		if v, ok := updates["org_type"].(string); ok {
			sets = append(sets, "org_type = $"+itoa(i))
			args = append(args, v)
			i++
		}
		if len(sets) == 0 {
			return ierr.E(ierr.CodeInvalidArgument, "没有可更新字段")
		}
		// tenant_id 用独立占位符（早期 `tenant_id = $1` 与 org_id 撞位，恒命中 0 行）。
		q := `UPDATE iam.organizations SET ` + stringsJoin(sets, ", ") + `, updated_at = now()
	         WHERE id = $1 AND tenant_id = $` + itoa(i)
		args = append(args, tenantID.String())
		tag, err := tx.Exec(ctx, q, args...)
		if err != nil {
			return ierr.Wrap(err, ierr.CodeInternal, "更新组织失败")
		}
		if tag.RowsAffected() == 0 {
			return ierr.Ef(ierr.CodeNotFound, "组织 %s 不存在或不属于该租户", orgID)
		}
		return nil
	})
}

// DisableOrganization 停用组织（软删除 status=INACTIVE）。
func (s *Store) DisableOrganization(ctx context.Context, tenantID, orgID ids.ID) error {
	if s == nil || s.Pool == nil {
		return ierr.E(ierr.CodeUnavailable, "数据库连接池未就绪")
	}
	return s.TenantTxAs(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE iam.organizations SET status = 'INACTIVE', updated_at = now()
			 WHERE id = $1 AND tenant_id = $2`, orgID.String(), tenantID.String())
		if err != nil {
			return ierr.Wrap(err, ierr.CodeInternal, "停用组织失败")
		}
		if tag.RowsAffected() == 0 {
			return ierr.Ef(ierr.CodeNotFound, "组织 %s 不存在或不属于该租户", orgID)
		}
		return nil
	})
}

// --- Memberships ---

// AddMembership 把用户加入组织（同一用户可在多组织中）。
func (s *Store) AddMembership(ctx context.Context, tenantID, userID, orgID ids.ID, mtype string) (ids.ID, error) {
	if s == nil || s.Pool == nil {
		return ids.ID{}, ierr.E(ierr.CodeUnavailable, "数据库连接池未就绪")
	}
	if mtype == "" {
		mtype = "EMPLOYEE"
	}
	var newID ids.ID
	err := s.TenantTxAs(ctx, tenantID, func(tx pgx.Tx) error {
		newID = ids.New()
		if _, err := tx.Exec(ctx,
			`INSERT INTO iam.memberships (id, tenant_id, user_id, org_id, membership_type, status)
			 VALUES ($1, $2, $3, $4, $5, 'ACTIVE')`,
			newID.String(), tenantID.String(), userID.String(), orgID.String(), mtype); err != nil {
			if isUniqueViolation(err) {
				return ierr.E(ierr.CodeConflict, "该用户已属于此组织")
			}
			return ierr.Wrap(err, ierr.CodeInternal, "插入成员关系失败")
		}
		return nil
	})
	if err != nil {
		return ids.ID{}, err
	}
	return newID, nil
}

// RemoveMembership 从组织移除用户。
func (s *Store) RemoveMembership(ctx context.Context, tenantID, membershipID ids.ID) error {
	if s == nil || s.Pool == nil {
		return ierr.E(ierr.CodeUnavailable, "数据库连接池未就绪")
	}
	return s.TenantTxAs(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`DELETE FROM iam.memberships WHERE id = $1 AND tenant_id = $2`,
			membershipID.String(), tenantID.String())
		if err != nil {
			return ierr.Wrap(err, ierr.CodeInternal, "删除成员关系失败")
		}
		if tag.RowsAffected() == 0 {
			return ierr.Ef(ierr.CodeNotFound, "成员关系 %s 不存在", membershipID)
		}
		return nil
	})
}

// --- Role Bindings ---

// BindRole 把角色绑定到 membership。
//
// grantedBy 是执行本次授权的**主体用户**（iam.users.id），对应 NOT NULL 外键
// governance.role_bindings.granted_by。早期实现把 tenant_id 塞进该列并同时多传了
// 一个绑定参数（7 个实参 / 6 个占位符），既触发绑定参数个数错误，也会在修复后
// 立刻撞上外键违例，因此这里把调用者主体显式作为入参。
func (s *Store) BindRole(ctx context.Context, tenantID, membershipID, roleID ids.ID, scopeType string, scopeOrgID *ids.ID, grantedBy ids.ID) (ids.ID, error) {
	if s == nil || s.Pool == nil {
		return ids.ID{}, ierr.E(ierr.CodeUnavailable, "数据库连接池未就绪")
	}
	allowScope := map[string]bool{"TENANT": true, "ORG": true, "ORG_SUBTREE": true, "SELF": true, "ASSIGNED": true, "EXPLICIT": true}
	if !allowScope[scopeType] {
		return ids.ID{}, ierr.Ef(ierr.CodeInvalidArgument, "scope_type %q 非法", scopeType)
	}
	if scopeType == "ORG" || scopeType == "ORG_SUBTREE" {
		if scopeOrgID == nil || scopeOrgID.IsZero() {
			return ids.ID{}, ierr.E(ierr.CodeInvalidArgument, "ORG/ORG_SUBTREE scope 需要 scope_org_id")
		}
	}
	if grantedBy.IsZero() {
		return ids.ID{}, ierr.E(ierr.CodeInvalidArgument, "granted_by（授权主体）缺失")
	}
	var newID ids.ID
	err := s.TenantTxAs(ctx, tenantID, func(tx pgx.Tx) error {
		newID = ids.New()
		var scopeTxt any
		if scopeOrgID != nil && !scopeOrgID.IsZero() {
			scopeTxt = scopeOrgID.String()
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO governance.role_bindings
			 (id, tenant_id, membership_id, role_id, scope_type, scope_org_id, granted_by)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			newID.String(), tenantID.String(), membershipID.String(), roleID.String(), scopeType, scopeTxt, grantedBy.String()); err != nil {
			if isUniqueViolation(err) {
				return ierr.E(ierr.CodeConflict, "该绑定已存在")
			}
			return ierr.Wrap(err, ierr.CodeInternal, "插入角色绑定失败")
		}
		return nil
	})
	if err != nil {
		return ids.ID{}, err
	}
	return newID, nil
}

// UnbindRole 解除角色绑定。
func (s *Store) UnbindRole(ctx context.Context, tenantID, bindingID ids.ID) error {
	if s == nil || s.Pool == nil {
		return ierr.E(ierr.CodeUnavailable, "数据库连接池未就绪")
	}
	return s.TenantTxAs(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`DELETE FROM governance.role_bindings WHERE id = $1 AND tenant_id = $2`,
			bindingID.String(), tenantID.String())
		if err != nil {
			return ierr.Wrap(err, ierr.CodeInternal, "删除角色绑定失败")
		}
		if tag.RowsAffected() == 0 {
			return ierr.Ef(ierr.CodeNotFound, "角色绑定 %s 不存在", bindingID)
		}
		return nil
	})
}

// ListRoleBindings 列出租户内全部角色绑定（用于管理员查看分配情况）。
func (s *Store) ListRoleBindings(ctx context.Context, tenantID ids.ID) ([]map[string]any, error) {
	if s == nil || s.Pool == nil {
		return nil, ierr.E(ierr.CodeUnavailable, "数据库连接池未就绪")
	}
	var out []map[string]any
	err := s.TenantTxAs(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT rb.id::text AS id, r.role_key, r.name AS role_name, rb.scope_type,
			        COALESCE(rb.scope_org_id::text, '') AS scope_org_id,
			        u.login_name, u.display_name, o.name AS org_name, rb.created_at
			   FROM governance.role_bindings rb
			   JOIN governance.roles r ON r.id = rb.role_id AND r.tenant_id = rb.tenant_id
			   JOIN iam.memberships m ON m.id = rb.membership_id AND m.tenant_id = rb.tenant_id
			   JOIN iam.users u ON u.id = m.user_id
			   LEFT JOIN iam.organizations o ON o.id = m.org_id
			  ORDER BY u.login_name, r.role_key`)
		if err != nil {
			return ierr.Wrap(err, ierr.CodeInternal, "查询角色绑定失败")
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

// ---- cursor helpers ----

func encodeCursor(key string) string {
	return base64Encode([]byte(key))
}
func decodeCursor(s string) (string, error) {
	b, err := base64Decode(s)
	if err != nil {
		return "", nil
	}
	return string(b), nil
}
func base64Encode(b []byte) string {
	const table = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	out := make([]byte, 0, (len(b)+2)/3*4)
	for i := 0; i < len(b); i += 3 {
		var triple uint32
		n := 0
		for j := 0; j < 3 && i+j < len(b); j++ {
			triple = triple<<8 | uint32(b[i+j])
			n++
		}
		for k := 3; k >= 0; k-- {
			if k > n {
				out = append(out, '=')
			} else {
				out = append(out, table[(triple>>(k*6))&0x3F])
			}
		}
	}
	return string(out)
}
func base64Decode(s string) ([]byte, error) {
	const table = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	index := make([]byte, 128)
	for i := range index {
		index[i] = 255
	}
	for i, c := range []byte(table) {
		index[c] = byte(i)
	}
	var out []byte
	var acc uint32
	var bits int
	for _, c := range []byte(s) {
		if c == '=' {
			break
		}
		if c >= 128 || index[c] == 255 {
			return nil, errors.New("bad base64url")
		}
		acc = (acc << 6) | uint32(index[c])
		bits += 6
		if bits >= 8 {
			bits -= 8
			out = append(out, byte(acc>>bits))
		}
	}
	return out, nil
}

// stringsJoin 本地实现（避免额外 import）。
func stringsJoin(s []string, sep string) string {
	if len(s) == 0 {
		return ""
	}
	var total int
	for _, v := range s {
		total += len(v)
	}
	total += len(sep) * (len(s) - 1)
	buf := make([]byte, 0, total)
	for i, v := range s {
		if i > 0 {
			buf = append(buf, sep...)
		}
		buf = append(buf, v...)
	}
	return string(buf)
}

// scanRows 把 pgx.Rows 扫成 []map[string]any（keys 小写）。
func scanRows(rows pgx.Rows) ([]map[string]any, error) {
	fields := rows.FieldDescriptions()
	out := make([]map[string]any, 0)
	for rows.Next() {
		vals := make([]any, len(fields))
		ptrs := make([]any, len(fields))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		for i, f := range fields {
			key := string(f.Name)
			v := vals[i]
			switch x := v.(type) {
			case []uint8:
				row[key] = string(x)
			default:
				row[key] = v
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
