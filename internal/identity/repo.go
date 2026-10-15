package identity

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
	"odap/internal/platform/tenantctx"
)

// tenantFromContext 从可信上下文解析租户 ID。
// 安全约束（第十六篇 §5.4）：租户身份只能来自经过验证的认证上下文，
// 禁止从请求体或查询参数读取；缺失按未认证处理（fail-closed）。
func tenantFromContext(ctx context.Context) (ids.ID, error) {
	id, ok := tenantctx.FromContext(ctx)
	if !ok || id.TenantID.IsZero() {
		return ids.ID{}, errors.E(errors.CodeUnauthenticated, "缺少可信租户上下文")
	}
	return id.TenantID, nil
}

// readInTenantTx 在租户事务（SET LOCAL）中执行只读查询。
// RLS 表（organizations/org_closure/sessions 等）的读取必须显式注入
// 租户上下文，否则行级策略会过滤掉全部行；事务结束自动清理，杜绝连接池泄漏。
func readInTenantTx(ctx context.Context, pool *pgxpool.Pool, tenantID ids.ID, fn func(pgx.Tx) error) error {
	return writeInTenantTx(ctx, pool, tenantID, fn)
}

// writeInTenantTx 在租户事务中执行读写操作并提交。
func writeInTenantTx(ctx context.Context, pool *pgxpool.Pool, tenantID ids.ID, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := pgxutil.WithTenantTx(ctx, tx, tenantID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// errRepoNotFound / errRepoConflict 是 Repository 层的哨兵错误，
// 由各服务翻译为平台错误码（contracts/02）。
var (
	errRepoNotFound = errRepo("not found")
	errRepoConflict = errRepo("conflict")
)

type errRepo string

func (e errRepo) Error() string { return string(e) }

// isUniqueViolation 判断是否为 PostgreSQL 唯一约束冲突（SQLSTATE 23505）。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if ok := asPgError(err, &pgErr); ok {
		return pgErr.Code == "23505"
	}
	return false
}

// asPgError 尝试把 err 解包为 *pgconn.PgError。
func asPgError(err error, target **pgconn.PgError) bool {
	for err != nil {
		if pgErr, ok := err.(*pgconn.PgError); ok {
			*target = pgErr
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// jsonbOf 把 map 编码为 JSONB 参数（空 map 编码为 {}）。
func jsonbOf(m map[string]any) []byte {
	if len(m) == 0 {
		return []byte("{}")
	}
	b, err := json.Marshal(m)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// jsonbMap 把 JSONB 字节解码为 map；空或非法输入返回空 map。
func jsonbMap(b []byte) map[string]any {
	m := map[string]any{}
	if len(b) == 0 {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

// notFound 报告 err 是否为仓库层“不存在”哨兵。
func notFound(err error) bool { return err == errRepoNotFound }

// conflict 报告 err 是否为仓库层“冲突”哨兵。
func conflict(err error) bool { return err == errRepoConflict }

// compile-time 保证哨兵实现 error 接口。
var _ error = errRepoNotFound
