// Package store 是 HTTP 层的持久化适配器（adapter）。
//
// 为什么它是一个独立包而不是直接调用各领域服务：任务的交付边界是
// internal/api/**，且明确禁止改动既有 internal 包。对缺乏"读侧查询"能力的
// 资源（列表/游标分页/BFF 聚合），本包以只读/轻写 SQL 适配器补齐；对已具备
// 完整领域服务的写路径（Run 状态机、租约、审计追加）则直接复用既有包。
//
// 约定：
//   - 所有跨租户可见性依赖 RLS，读写一律在租户事务（app.tenant_id）内执行
//     （middleware.TenantTx）；
//   - 通用行映射（queryMaps）要求 SQL 对 uuid/jsonb/数组列显式 ::text 投影，
//     保证返回值为可安全序列化的简单类型，避免 pgx 对 UUID/JSONB 的
//     any 解码差异导致响应体漂移。
package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/api/middleware"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
	"odap/internal/platform/secretbox"
)

// Store 封装 HTTP 层的持久化访问。
type Store struct {
	Pool *pgxpool.Pool

	// cipher 用于模型端点凭据的信封加密/解密。为 nil 表示服务端未配置主密钥
	// （ODAP_SECRET_MASTER_KEY 为空）——此时「界面录入 api-key」路径 fail-closed，
	// 绝不落明文；环境变量引用路径不受影响。
	cipher *secretbox.Cipher
}

// Option 是 Store 的可选装配参数。
type Option func(*Store)

// WithCipher 注入凭据加密器（nil 表示未配置主密钥，调用方应 fail-closed）。
func WithCipher(c *secretbox.Cipher) Option {
	return func(s *Store) { s.cipher = c }
}

// New 构造 Store。
func New(pool *pgxpool.Pool, opts ...Option) *Store {
	s := &Store{Pool: pool}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// Ready 探测数据库可用性（/readyz）。
func (s *Store) Ready(ctx context.Context) error {
	if s == nil || s.Pool == nil {
		return errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}
	if err := s.Pool.Ping(ctx); err != nil {
		return errors.Wrap(err, errors.CodeUnavailable, "数据库不可用")
	}
	return nil
}

// TenantTx 在租户事务中执行 fn（RLS 会话变量注入见 middleware.TenantTx）。
func (s *Store) TenantTx(ctx context.Context, fn func(pgx.Tx) error) error {
	if s == nil || s.Pool == nil {
		return errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}
	return middleware.TenantTx(ctx, s.Pool, fn)
}

// TenantTxAs 在**显式指定**的租户事务中执行 fn。
//
// 存在的理由：登录/注册/开发态直连等路径上，租户 ID 是方法入参（由业务解析得到），
// 并不存在于 context，无法走 TenantTx。但"把租户 ID 写进 app.tenant_id"这件事
// 必须**全仓只有一处实现**——这里直接复用 middleware/pgxutil 使用的同一原语
// pgxutil.WithTenantTx（SELECT set_config('app.tenant_id', $1, true)）。
//
// 反面教材（本次修复的根因）：曾被写成
//
//	SET LOCAL app.current_tenant_id = $1
//
// 该写法有两处致命错误：① SET 的语法不接受参数占位符，PostgreSQL 在**解析期**
// 即报 syntax error at or near "$1"，语句根本不会执行；② RLS 策略函数读的是
// app.tenant_id，app.current_tenant_id 只是个函数名，即便 SET 成功也不影响策略。
// 两者叠加的结果是"任何调用它的方法都必然失败"。
func (s *Store) TenantTxAs(ctx context.Context, tenantID ids.ID, fn func(pgx.Tx) error) error {
	if s == nil || s.Pool == nil {
		return errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}
	if tenantID.IsZero() {
		return errors.E(errors.CodePermissionDenied, "缺少租户身份")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return errors.Wrap(err, errors.CodeUnavailable, "开启事务失败")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := pgxutil.WithTenantTx(ctx, tx, tenantID); err != nil {
		return errors.Wrap(err, errors.CodeInternal, "注入租户上下文失败")
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return errors.Wrap(err, errors.CodeInternal, "提交事务失败")
	}
	return nil
}

// queryMaps 执行查询并把每行转换为 map[string]any。
//
// 前置要求：SQL 需对 uuid/jsonb/数组列做 ::text 投影；未投影的复杂类型会被
// 归一化为 JSON 字符串（[]byte）或 fmt 字符串，绝不 panic。
//
// 零行必须返回**非 nil 空切片**：契约 contracts/03 §1.4 规定列表响应为
// {items, next_cursor}，其中 items 是数组。若此处返回 nil，encoding/json 会把
// items 序列化为 null，调用方（前端列表页）按数组消费时即崩溃——
// 且该缺陷只在表为空时显形，是典型的"空集才触发"契约违例。
func queryMaps(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]map[string]any, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "查询失败")
	}
	defer rows.Close()

	fields := rows.FieldDescriptions()
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = string(f.Name)
	}
	out := make([]map[string]any, 0)
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, errors.Wrap(err, errors.CodeInternal, "读取结果失败")
		}
		row := make(map[string]any, len(names))
		for i, v := range vals {
			row[names[i]] = normalizeValue(v)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "遍历结果失败")
	}
	return out, nil
}

// queryMapsNoTenant 在无租户上下文下执行查询（仅用于无 RLS 的平台表，
// 例如 iam.tenants 的平台级清单）。零行同 queryMaps 返回非 nil 空切片。
func (s *Store) queryMapsNoTenant(ctx context.Context, sql string, args ...any) ([]map[string]any, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}
	rows, err := s.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "查询失败")
	}
	defer rows.Close()
	fields := rows.FieldDescriptions()
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = string(f.Name)
	}
	out := make([]map[string]any, 0)
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, errors.Wrap(err, errors.CodeInternal, "读取结果失败")
		}
		row := make(map[string]any, len(names))
		for i, v := range vals {
			row[names[i]] = normalizeValue(v)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "遍历结果失败")
	}
	return out, nil
}

// normalizeValue 归一化 pgx 返回值，保证可被 encoding/json 正确序列化。
func normalizeValue(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case []byte:
		if json.Valid(t) {
			return json.RawMessage(append([]byte(nil), t...))
		}
		return string(t)
	case time.Time:
		return t.UTC()
	case [16]byte:
		id := ids.ID(t)
		return id.String()
	default:
		return v
	}
}

// ---------------------------------------------------------------------------
// cursor 分页（contracts/03 §1.4：仅 cursor 式，禁止 offset 深分页）
// ---------------------------------------------------------------------------

// Cursor 是 keyset 分页游标：按 (排序时间, 主键) 降序翻页。
type Cursor struct {
	Time time.Time `json:"t"`
	Key  string    `json:"k"`
}

// EncodeCursor 编码游标（base64url，不透明）。
func EncodeCursor(t time.Time, key string) string {
	b, _ := json.Marshal(Cursor{Time: t.UTC(), Key: key})
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor 解码游标；空串表示首页。
func DecodeCursor(raw string) (*Cursor, error) {
	if raw == "" {
		return nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.E(errors.CodeInvalidArgument, "cursor 不合法")
	}
	var c Cursor
	if err := json.Unmarshal(b, &c); err != nil || c.Key == "" || c.Time.IsZero() {
		return nil, errors.E(errors.CodeInvalidArgument, "cursor 不合法")
	}
	return &c, nil
}

// NormalizeLimit 收敛分页大小到 [1, 200]，缺省 10（配置类资源默认 10 行/页）。
func NormalizeLimit(n, def int) (int, error) {
	if n == 0 {
		if def <= 0 {
			def = 10
		}
		return def, nil
	}
	if n < 0 || n > 200 {
		return 0, errors.E(errors.CodeInvalidArgument, "limit 必须在 1..200 之间")
	}
	return n, nil
}

// nextCursorOf 依据"多取一条"的结果集计算 next_cursor。
// rows 已按 (t DESC, k DESC) 排序；hasMore 表示存在更多数据。
func nextCursorOf(t time.Time, key string, hasMore bool) string {
	if !hasMore {
		return ""
	}
	return EncodeCursor(t, key)
}

// keyedRow 是从通用行映射中读取 (时间, 主键) 的辅助器。
func keyedRow(row map[string]any, timeCol, keyCol string) (time.Time, string, bool) {
	t, ok := row[timeCol].(time.Time)
	if !ok {
		return time.Time{}, "", false
	}
	k, ok := row[keyCol].(string)
	if !ok {
		return time.Time{}, "", false
	}
	return t, k, true
}
