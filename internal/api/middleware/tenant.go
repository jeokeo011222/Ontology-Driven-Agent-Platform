package middleware

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/api/httperr"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
	"odap/internal/platform/tenantctx"
)

// IdentityFromContext 读取可信身份；缺失按未认证 fail-closed 返回错误。
func IdentityFromContext(ctx context.Context) (tenantctx.Identity, error) {
	id, ok := tenantctx.FromContext(ctx)
	if !ok || id.TenantID.IsZero() {
		return tenantctx.Identity{}, errors.E(errors.CodeUnauthenticated, "缺少可信身份上下文")
	}
	return id, nil
}

// RequireIdentity 在下游处理器之前强制要求已认证身份（纵深防御：
// 即使路由被错误地挂到认证链之外，也会在此拒绝）。
func RequireIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := IdentityFromContext(r.Context()); err != nil {
			httperr.Write(w, RequestIDFromContext(r.Context()), err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// TenantTx 在开启的租户事务内执行 fn：显式注入 app.tenant_id（SET LOCAL），
// 事务结束自动清理会话变量，杜绝连接池上下文泄漏（第十六篇 §5.4）。
//
// 实现委托给 pgxutil.TenantTx（平台层唯一实现），本函数只负责
// HTTP 侧的 fail-closed 身份解析与错误码映射。历史上此处曾自行复制一份事务包装，
// 是 L1 缺陷（第二份注入实现 + 写错 GUC 名）的土壤，故收敛为单一实现。
func TenantTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	if pool == nil {
		return errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}
	tenantID, err := tenantIDFromContext(ctx)
	if err != nil {
		return err
	}
	if err := pgxutil.TenantTx(ctx, pool, tenantID, fn); err != nil {
		// 回调返回的**业务错误**（NotFound / Conflict / InvalidArgument …）必须原样透出。
		// 历史上这里无条件 errors.Wrap(err, CodeInternal)，把 404/409/400 一律改写成 500
		// "租户事务失败"，使所有在租户事务内做的存在性/引用/唯一性校验对外全部失真
		// （前端拿到 500 只能提示"服务器错误"，无法给出可操作原因）。
		if ae, ok := err.(*errors.AppError); ok {
			return ae
		}
		return errors.Wrap(err, errors.CodeInternal, "租户事务失败")
	}
	return nil
}

// tenantIDFromContext 解析租户 ID（fail-closed）。
func tenantIDFromContext(ctx context.Context) (ids.ID, error) {
	id, ok := tenantctx.FromContext(ctx)
	if !ok || id.TenantID.IsZero() {
		return ids.ID{}, errors.E(errors.CodePermissionDenied, "缺少租户身份")
	}
	return id.TenantID, nil
}
