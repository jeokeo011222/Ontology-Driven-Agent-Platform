package authorization

import (
	"context"
	stderrors "errors"

	"github.com/jackc/pgx/v5/pgconn"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/tenantctx"
)

// errRepoNotFound / errRepoConflict 是存储层哨兵错误，由服务翻译为平台错误码。
var (
	errRepoNotFound = errRepo("not found")
	errRepoConflict = errRepo("conflict")
)

type errRepo string

func (e errRepo) Error() string { return string(e) }

// notFound / conflict 判断存储层哨兵。
func notFound(err error) bool { return err == errRepoNotFound }

func conflict(err error) bool { return err == errRepoConflict }

// tenantFromContext 从可信上下文解析租户 ID（第十六篇 §5.4）。
func tenantFromContext(ctx context.Context) (ids.ID, error) {
	id, ok := tenantctx.FromContext(ctx)
	if !ok || id.TenantID.IsZero() {
		return ids.ID{}, errors.E(errors.CodeUnauthenticated, "缺少可信租户上下文")
	}
	return id.TenantID, nil
}

// isUniqueViolation 判断 PostgreSQL 唯一约束冲突（SQLSTATE 23505）。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if stderrors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}
