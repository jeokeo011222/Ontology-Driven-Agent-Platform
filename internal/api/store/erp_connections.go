package store

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/secretbox"
)

// ERP 连接凭据密文的 AAD 作用域（列名级隔离：跨表搬运密文时认证直接失败）。
const erpCredentialAADScope = "integration.erp_connections.credential_ciphertext"

// erpConnectionStatuses 是连接状态取值集合（DELETED 为软删态，审计可追溯）。
var erpConnectionStatuses = map[string]bool{"ACTIVE": true, "DISABLED": true, "DELETED": true}

// ErpSystem 是 integration.erp_systems 字典行（全局只读，无 RLS）。
type ErpSystem struct {
	SystemCode  string `json:"system_code"`
	DisplayName string `json:"display_name"`
	AdapterKey  string `json:"adapter_key"`
	Implemented bool   `json:"implemented"`
	IsDefault   bool   `json:"is_default"`
	SortNo      int    `json:"sort_no"`
	Description string `json:"description"`
}

// ListErpSystems 返回 ERP 系统类型字典（按 sort_no 排序，默认项在前由 sort_no 保证）。
func (s *Store) ListErpSystems(ctx context.Context) ([]ErpSystem, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}
	rows, err := s.Pool.Query(ctx, `
        SELECT system_code, display_name, adapter_key, implemented, is_default, sort_no, description
          FROM integration.erp_systems
         ORDER BY sort_no, system_code`)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "读取 ERP 系统字典失败")
	}
	defer rows.Close()
	out := []ErpSystem{}
	for rows.Next() {
		var e ErpSystem
		if err := rows.Scan(&e.SystemCode, &e.DisplayName, &e.AdapterKey,
			&e.Implemented, &e.IsDefault, &e.SortNo, &e.Description); err != nil {
			return nil, errors.Wrap(err, errors.CodeInternal, "解析 ERP 系统字典行失败")
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ErpConnection 是 integration.erp_connections 的读模型。
//
// 凭据安全：本结构**绝不携带 credential_ciphertext 本体**——读取路径只投影
// 打码值 credential_masked 与来源枚举 credential_source，明文密文只在使用点
// （动态装配解析器）按需解密。
type ErpConnection struct {
	ID               string `json:"id"`
	SystemCode       string `json:"system_code"`
	SystemName       string `json:"system_name"`
	Name             string `json:"name"`
	BaseURL          string `json:"base_url"`
	CredentialRef    string `json:"credential_ref,omitempty"`
	CredentialMasked string `json:"credential_masked"`
	CredentialSource string `json:"credential_source"`
	HasCredential    bool   `json:"has_credential"`
	Status           string `json:"status"`
	CreatedBy        string `json:"created_by"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

// erpConnectionColumns 是连接表的显式投影（绝不包含 credential_ciphertext）。
const erpConnectionColumns = `
        c.id::text AS id, c.system_code, s.display_name AS system_name,
        c.name, c.base_url, c.credential_ref,
        CASE WHEN c.credential_ciphertext IS NOT NULL THEN 'CIPHERTEXT'
             WHEN c.credential_ref IS NOT NULL AND c.credential_ref <> '' THEN 'ENV_REF'
             ELSE 'NONE' END AS credential_source,
        c.credential_masked,
        (c.credential_ciphertext IS NOT NULL OR (c.credential_ref IS NOT NULL AND c.credential_ref <> '')) AS has_credential,
        c.status, c.created_by, c.created_at::text, c.updated_at::text`

func scanErpConnection(row map[string]any) ErpConnection {
	return ErpConnection{
		ID:               asString(row["id"]),
		SystemCode:       asString(row["system_code"]),
		SystemName:       asString(row["system_name"]),
		Name:             asString(row["name"]),
		BaseURL:          asString(row["base_url"]),
		CredentialRef:    asString(row["credential_ref"]),
		CredentialSource: asString(row["credential_source"]),
		CredentialMasked: asString(row["credential_masked"]),
		HasCredential:    asBool(row["has_credential"]),
		Status:           asString(row["status"]),
		CreatedBy:        asString(row["created_by"]),
		CreatedAt:        asString(row["created_at"]),
		UpdatedAt:        asString(row["updated_at"]),
	}
}

// ListErpConnections 返回租户 ERP 连接列表（cursor 分页 + status/system_code/q 筛选）。
func (s *Store) ListErpConnections(ctx context.Context, limit int, cursor, status, systemCode, q string) (*ListResult, error) {
	if status != "" && !erpConnectionStatuses[status] {
		return nil, errors.E(errors.CodeInvalidArgument, "status 不在取值集合内")
	}
	from, err := decKey(cursor)
	if err != nil {
		return nil, err
	}
	// 默认不含软删行（与本体/工具列表同一口径）；显式 status=DELETED 时才可见。
	res := &ListResult{Items: []map[string]any{}}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT `+erpConnectionColumns+`
              FROM integration.erp_connections c
              JOIN integration.erp_systems s ON s.system_code = c.system_code
             WHERE ($1::text IS NULL OR c.created_at > $1::timestamptz)
               AND ($2::text IS NULL OR c.status = $2)
               AND ($3::text IS NULL OR c.system_code = $3)
               AND ($4::text IS NULL OR c.name ILIKE '%'||$4||'%' OR c.base_url ILIKE '%'||$4||'%')
               AND ($5::bool = FALSE OR c.status <> 'DELETED')
             ORDER BY c.created_at ASC, c.id ASC
             LIMIT $6`,
			nullableStr(from), nullableStr(status), nullableStr(systemCode),
			nullableStr(strings.TrimSpace(q)), status != "DELETED", limit+1)
		if err != nil {
			return err
		}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}
		res.Items = rows
		if hasMore && len(rows) > 0 {
			if k, ok := rows[len(rows)-1]["created_at"].(string); ok {
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

// CreateErpConnectionInput 是创建 ERP 连接的输入。
type CreateErpConnectionInput struct {
	SystemCode string
	Name       string
	BaseURL    string
	// CredentialRef 与 Credential 二选一（与 llm.endpoints 同规则）：
	//   - CredentialRef：环境变量引用名（回落路径）
	//   - Credential：界面录入的明文凭据，落库前加密为信封密文（密文优先）
	CredentialRef string
	Credential    string
	Status        string
	CreatedBy     string
}

// CreateErpConnection 创建租户级 ERP 连接。
//
// 凭据落库形态与模型端点完全一致：明文只在 store 层短暂存活，进入 SQL 参数的
// 只有 AES-256-GCM 信封密文（AAD 绑定连接 id）；对外只回打码值。
func (s *Store) CreateErpConnection(ctx context.Context, in CreateErpConnectionInput) (*ErpConnection, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}
	in.SystemCode = strings.TrimSpace(in.SystemCode)
	in.Name = strings.TrimSpace(in.Name)
	in.BaseURL = strings.TrimSpace(in.BaseURL)
	in.CredentialRef = strings.TrimSpace(in.CredentialRef)
	if in.SystemCode == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "system_code 必填")
	}
	if in.Name == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "name 必填")
	}
	if !strings.HasPrefix(in.BaseURL, "http://") && !strings.HasPrefix(in.BaseURL, "https://") {
		return nil, errors.E(errors.CodeInvalidArgument, "base_url 必填且须以 http:// 或 https:// 开头")
	}
	if in.Credential != "" && in.CredentialRef != "" {
		return nil, errors.E(errors.CodeInvalidArgument, "credential 与 credential_ref 二选一：不能同时提供")
	}
	if in.Status == "" {
		in.Status = "ACTIVE"
	}
	if !erpConnectionStatuses[in.Status] || in.Status == "DELETED" {
		return nil, errors.E(errors.CodeInvalidArgument, "status 仅支持 ACTIVE / DISABLED")
	}

	connID := ids.New()
	var (
		credCiphertext []byte
		credMasked     string
	)
	credRef := in.CredentialRef
	if in.Credential != "" {
		if s.cipher == nil {
			return nil, errors.E(errors.CodeUnavailable,
				"服务端未配置凭据主密钥（ODAP_SECRET_MASTER_KEY），无法加密存储凭据；"+
					"请配置后重试，或改用 credential_ref 环境变量引用路径")
		}
		ct, cerr := s.cipher.Encrypt([]byte(in.Credential), secretbox.AADForScope(erpCredentialAADScope, connID.String()))
		if cerr != nil {
			return nil, errors.Wrap(cerr, errors.CodeInternal, "加密 ERP 连接凭据失败")
		}
		credCiphertext = ct
		credMasked = secretbox.Mask(in.Credential)
		credRef = ""
	}

	// system_code 必须是字典中 implemented=true 的项：金蝶/SAP 适配器尚未实现，
	// 创建只会得到一个无法装配的空壳——fail-closed 拒绝，并明确指出原因。
	var out *ErpConnection
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		var implemented bool
		var displayName string
		err := tx.QueryRow(ctx,
			`SELECT display_name, implemented FROM integration.erp_systems WHERE system_code = $1`,
			in.SystemCode).Scan(&displayName, &implemented)
		if err != nil {
			if err == pgx.ErrNoRows {
				return errors.E(errors.CodeInvalidArgument, "未知的 ERP 系统类型："+in.SystemCode)
			}
			return errors.Wrap(err, errors.CodeInternal, "读取 ERP 系统字典失败")
		}
		if !implemented {
			return errors.E(errors.CodeInvalidArgument,
				"ERP 类型「"+displayName+"」的适配器尚未实现，暂不能创建连接（字典已建模，待适配器落地后开放）")
		}
		// 注意：RETURNING 只能引用目标表列，无法 JOIN —— erpConnectionColumns
		// 带了 s.display_name（system_name），因此不能直接用于 INSERT RETURNING。
		// 先 INSERT，再按 id 回读连接投影。
		if _, err := tx.Exec(ctx, `
            INSERT INTO integration.erp_connections
                (id, tenant_id, system_code, name, base_url,
                 credential_ref, credential_ciphertext, credential_masked, status, created_by)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5, $6, $7, $8, $9)`,
			connID.String(), in.SystemCode, in.Name, in.BaseURL,
			nullableStr(credRef), nullableBytes(credCiphertext), credMasked, in.Status, in.CreatedBy); err != nil {
			return errors.Wrap(err, errors.CodeInternal, "创建 ERP 连接失败")
		}
		rows, err := queryMaps(ctx, tx, `
            SELECT `+erpConnectionColumns+`
              FROM integration.erp_connections c
              JOIN integration.erp_systems s ON s.system_code = c.system_code
             WHERE c.id = $1`, connID.String())
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errors.E(errors.CodeInternal, "创建 ERP 连接失败：回读连接投影为空")
		}
		c := scanErpConnection(rows[0])
		out = &c
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateErpConnectionInput 是 ERP 连接的部分更新输入（指针表达"是否提供"）。
//
// credential 的三态语义（与模型端点 api_key 完全一致）：
//
//	省略字段   —— 不修改现有凭据
//	非空字符串 —— 替换为新的凭据（重新加密落库）
//	显式空串   —— 清空凭据（此后必须保留非空 credential_ref，否则 400）
type UpdateErpConnectionInput struct {
	Name          *string
	BaseURL       *string
	CredentialRef *string
	Credential    *string
	Status        *string
}

// UpdateErpConnection 更新 ERP 连接（白名单字段；system_code 创建后不可变）。
func (s *Store) UpdateErpConnection(ctx context.Context, connID ids.ID, in UpdateErpConnectionInput) (*ErpConnection, error) {
	if connID.IsZero() {
		return nil, errors.E(errors.CodeInvalidArgument, "连接 id 不合法")
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "name 不能为空")
	}
	if in.BaseURL != nil {
		u := strings.TrimSpace(*in.BaseURL)
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return nil, errors.E(errors.CodeInvalidArgument, "base_url 须以 http:// 或 https:// 开头")
		}
	}
	if in.Status != nil && !erpConnectionStatuses[*in.Status] {
		return nil, errors.E(errors.CodeInvalidArgument, "status 不在取值集合内")
	}

	var out *ErpConnection
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		// 先读现状（含凭据引用），再决定三态凭据如何落。
		var (
			credRef   *string
			curMasked string
		)
		err := tx.QueryRow(ctx, `
            SELECT credential_ref, credential_masked
              FROM integration.erp_connections
             WHERE id = $1 AND status <> 'DELETED'`, connID.String()).
			Scan(&credRef, &curMasked)
		if err != nil {
			if err == pgx.ErrNoRows {
				return errors.E(errors.CodeNotFound, "ERP 连接不存在或已删除")
			}
			return errors.Wrap(err, errors.CodeInternal, "读取 ERP 连接失败")
		}

		newMasked := curMasked
		setCred := false
		if in.Credential != nil {
			if *in.Credential == "" {
				// 显式空串 = 清空密文；清空后必须保留非空 credential_ref（fail-closed）。
				effectiveRef := credRef
				if in.CredentialRef != nil {
					effectiveRef = in.CredentialRef
				}
				if effectiveRef == nil || strings.TrimSpace(*effectiveRef) == "" {
					return errors.E(errors.CodeInvalidArgument,
						"清空凭据后必须保留非空 credential_ref（环境变量引用），否则连接无法装配")
				}
				newMasked = ""
				if _, err := tx.Exec(ctx, `
                    UPDATE integration.erp_connections
                       SET credential_ciphertext = NULL, credential_masked = '', updated_at = now()
                     WHERE id = $1`, connID.String()); err != nil {
					return errors.Wrap(err, errors.CodeInternal, "清空 ERP 连接凭据失败")
				}
			} else {
				// 非空 = 替换为新凭据（密文优先，credential_ref 清空）。
				if s.cipher == nil {
					return errors.E(errors.CodeUnavailable,
						"服务端未配置凭据主密钥（ODAP_SECRET_MASTER_KEY），无法加密存储凭据")
				}
				ct, cerr := s.cipher.Encrypt([]byte(*in.Credential), secretbox.AADForScope(erpCredentialAADScope, connID.String()))
				if cerr != nil {
					return errors.Wrap(cerr, errors.CodeInternal, "加密 ERP 连接凭据失败")
				}
				newMasked = secretbox.Mask(*in.Credential)
				if _, err := tx.Exec(ctx, `
                    UPDATE integration.erp_connections
                       SET credential_ciphertext = $2, credential_masked = $3,
                           credential_ref = NULL, updated_at = now()
                     WHERE id = $1`, connID.String(), ct, newMasked); err != nil {
					return errors.Wrap(err, errors.CodeInternal, "更新 ERP 连接凭据失败")
				}
			}
			setCred = true
		}

		// 字段级白名单更新（凭据列已单独处理）。
		if _, err := tx.Exec(ctx, `
            UPDATE integration.erp_connections
               SET name = COALESCE($2, name),
                   base_url = COALESCE($3, base_url),
                   credential_ref = CASE WHEN $4 THEN $5 ELSE credential_ref END,
                   status = COALESCE($6, status),
                   updated_at = now()
             WHERE id = $1`,
			connID.String(),
			nullableStrPtr(in.Name), nullableStrPtr(in.BaseURL),
			!setCred, nullableStrPtr(in.CredentialRef),
			nullableStrPtr(in.Status)); err != nil {
			return errors.Wrap(err, errors.CodeInternal, "更新 ERP 连接失败")
		}

		rows, err := queryMaps(ctx, tx, `
            SELECT `+erpConnectionColumns+`
              FROM integration.erp_connections c
              JOIN integration.erp_systems s ON s.system_code = c.system_code
             WHERE c.id = $1`, connID.String())
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errors.E(errors.CodeNotFound, "ERP 连接不存在")
		}
		c := scanErpConnection(rows[0])
		out = &c
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SoftDeleteErpConnection 软删 ERP 连接（置 DELETED，保留审计链）。
func (s *Store) SoftDeleteErpConnection(ctx context.Context, connID ids.ID) error {
	if connID.IsZero() {
		return errors.E(errors.CodeInvalidArgument, "连接 id 不合法")
	}
	return s.TenantTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
            UPDATE integration.erp_connections
               SET status = 'DELETED', updated_at = now()
             WHERE id = $1 AND status <> 'DELETED'`, connID.String())
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "删除 ERP 连接失败")
		}
		if tag.RowsAffected() == 0 {
			return errors.E(errors.CodeNotFound, "ERP 连接不存在或已删除")
		}
		return nil
	})
}

// ErpConnectionSecret 是动态装配时的连接凭据投影（解密结果只驻留内存）。
type ErpConnectionSecret struct {
	ID            string
	SystemCode    string
	AdapterKey    string
	Name          string
	BaseURL       string
	CredentialRef string
	Credential    string
}

// ResolveActiveErpConnection 在当前租户内解析指定系统类型的 ACTIVE 连接
// （供动态装配使用，凭据解密结果只驻留内存）。
//
// 解析规则（显式、可解释，绝不静默）：
//   - 命中密文连接：解密为明文凭据；**解密失败直接报错，绝不回落
//     credential_ref**——回落会把「密文坏了/密钥轮换失败」伪装成「环境变量
//     没配」，掩盖真实故障（与模型端点凭据的既定硬约束一致）；
//   - 命中 ref 连接：返回引用名，由装配方从环境变量解析；
//   - 无 ACTIVE 连接：返回 (nil, nil)，由装配方决定是否使用 env 传统端口。
func (s *Store) ResolveActiveErpConnection(ctx context.Context, systemCode string) (*ErpConnectionSecret, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}
	var secret *ErpConnectionSecret
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		var (
			connID     string
			adapterKey string
			name       string
			baseURL    string
			credRef    *string
			credCipher []byte
		)
		err := tx.QueryRow(ctx, `
            SELECT c.id::text, s.adapter_key, c.name, c.base_url, c.credential_ref, c.credential_ciphertext
              FROM integration.erp_connections c
              JOIN integration.erp_systems s ON s.system_code = c.system_code
             WHERE c.system_code = $1 AND c.status = 'ACTIVE'
             ORDER BY c.created_at ASC
             LIMIT 1`, systemCode).
			Scan(&connID, &adapterKey, &name, &baseURL, &credRef, &credCipher)
		if err != nil {
			if err == pgx.ErrNoRows {
				return nil // 无连接：交由调用方显式处理，不算错误
			}
			return errors.Wrap(err, errors.CodeInternal, "解析 ERP 连接失败")
		}
		sec := &ErpConnectionSecret{
			ID:         connID,
			SystemCode: systemCode,
			AdapterKey: adapterKey,
			Name:       name,
			BaseURL:    baseURL,
		}
		if credCipher != nil {
			if s.cipher == nil {
				return errors.E(errors.CodeUnavailable,
					"ERP 连接凭据为密文存储，但服务端未配置主密钥（ODAP_SECRET_MASTER_KEY），无法解密装配")
			}
			plain, derr := s.cipher.Decrypt(credCipher, secretbox.AADForScope(erpCredentialAADScope, connID))
			if derr != nil {
				return errors.E(errors.CodeUnavailable,
					"ERP 连接凭据解密失败（密钥不匹配或密文损坏）；为安全计不回落到环境变量引用，请重新录入凭据")
			}
			sec.Credential = string(plain)
		} else if credRef != nil {
			sec.CredentialRef = *credRef
		}
		secret = sec
		return nil
	})
	if err != nil {
		return nil, err
	}
	return secret, nil
}

// asBool 把查询归一化值断言为 bool（非 bool 返回 false）。
func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

// nullableStrPtr 把 *string 转为 SQL NULL 语义（nil → nil）。
func nullableStrPtr(p *string) *string {
	if p == nil {
		return nil
	}
	return p
}
