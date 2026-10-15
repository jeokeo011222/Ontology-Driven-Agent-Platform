// Package tenantctx 在 context.Context 中传递可信的租户与主体身份。
// 安全约束（第十六篇 §5.4）：租户身份只能来自经过验证的认证上下文，
// 禁止从请求体或查询参数读取；后台 Worker 同样必须携带可信上下文。
package tenantctx

import (
	"context"

	"odap/internal/platform/ids"
)

type ctxKey int

const (
	keyTenant ctxKey = iota
	keySubject
	keyRequestID
	keyScopes
	keySession
)

// Identity 是一次请求的可信身份快照。
//
// SessionID 与 RequestID 是两个**互不相同**的概念，早期版本把二者合并在
// RequestID 一个字段里，导致注销（logout）拿到的实际是 HTTP 请求关联 ID，
// 撤销语句永远匹配不到会话。此处显式拆分：
//
//   - SessionID：iam.sessions.id，仅由会话校验链（identity.SessionService.Validate）
//     产出，是"撤销哪一个会话"的唯一依据；
//   - RequestID：HTTP 请求关联 ID（X-Request-Id），仅用于日志与错误响应串联，
//     由 middleware.RequestID 产出，**不得**被当作业务标识使用。
type Identity struct {
	TenantID  ids.ID
	SubjectID ids.ID // iam.users.id
	SessionID ids.ID // iam.sessions.id（无会话上下文，如开发态直连，为零值）
	RequestID string
	// Scopes 是数据范围列表（第三篇 §5.3 的 scope 结构，如 "TENANT" 或 "ORG:<id>"）。
	Scopes []string
}

// WithIdentity 将身份写入 context。
func WithIdentity(ctx context.Context, id Identity) context.Context {
	ctx = context.WithValue(ctx, keyTenant, id.TenantID)
	ctx = context.WithValue(ctx, keySubject, id.SubjectID)
	ctx = context.WithValue(ctx, keyRequestID, id.RequestID)
	ctx = context.WithValue(ctx, keyScopes, id.Scopes)
	ctx = context.WithValue(ctx, keySession, id.SessionID)
	return ctx
}

// FromContext 读取身份；缺失时返回 false（调用方必须按未认证处理）。
func FromContext(ctx context.Context) (Identity, bool) {
	tid, ok1 := ctx.Value(keyTenant).(ids.ID)
	sid, ok2 := ctx.Value(keySubject).(ids.ID)
	if !ok1 || !ok2 {
		return Identity{}, false
	}
	id := Identity{TenantID: tid, SubjectID: sid}
	if rid, ok := ctx.Value(keyRequestID).(string); ok {
		id.RequestID = rid
	}
	if sc, ok := ctx.Value(keyScopes).([]string); ok {
		id.Scopes = sc
	}
	if sess, ok := ctx.Value(keySession).(ids.ID); ok {
		id.SessionID = sess
	}
	return id, true
}

// MustFromContext 读取身份，缺失时 panic（仅用于已由中间件保证的路径）。
func MustFromContext(ctx context.Context) Identity {
	id, ok := FromContext(ctx)
	if !ok {
		panic("tenantctx: missing identity in context")
	}
	return id
}

// System 返回平台服务身份（系统级任务使用，第十六篇 §5.4）。
func System(tenantID ids.ID) Identity {
	return Identity{TenantID: tenantID, SubjectID: ids.ID{}, RequestID: "system"}
}
