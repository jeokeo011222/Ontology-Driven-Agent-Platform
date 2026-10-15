// Package tools 实现工具注册表解析与工具入参校验（第二篇 §4.3 /
// 第六篇 §8 工具调用协议）。
//
// 安全边界：capabilities 只是工具的能力"声明"，不等于授权。真正的调用
// 许可由 internal/authorization 在每次调用时判定（第四篇/第三篇），本包
// 只负责"解析到正确的工具版本"与"入参结构合法"。
package tools

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
)

// ToolVersion 是某一工具版本的完整定义（registry.tool_versions）。
type ToolVersion struct {
	ID           ids.ID
	TenantID     ids.ID
	ToolKey      string
	Version      string
	InputSchema  json.RawMessage
	OutputSchema json.RawMessage
	// EffectClass 决定该工具是否产生外部副作用（READ / EXTERNAL_WRITE /
	// INTERNAL_WRITE）。只有 READ 可走只读执行器；写操作必须经写网关
	// 与幂等键（第六篇 §8、第七篇）。
	EffectClass   string
	Capabilities  []string
	ContentDigest string
}

// Registry 是工具版本注册表的只读解析入口。
type Registry struct {
	Pool *pgxpool.Pool
}

const toolVersionSelect = `
SELECT tv.id, tv.tenant_id, t.tool_key, tv.version,
       tv.input_schema, tv.output_schema, tv.effect_class,
       tv.capabilities, tv.content_digest
  FROM registry.tool_versions tv
  JOIN registry.tools t
    ON t.id = tv.tool_id AND t.tenant_id = tv.tenant_id
 WHERE tv.tenant_id = $1`

// Resolve 精确解析指定工具键与版本；仅返回 PUBLISHED 版本。
//
// 不存在或未发布一律返回 CodeNotFound（不区分"不存在"与"未发布"，避免
// 向调用方泄露未发布工具的存在，第四篇 §5 信息最小化）。
func (r *Registry) Resolve(ctx context.Context, tenantID ids.ID, toolKey, version string) (*ToolVersion, error) {
	return r.query(ctx, tenantID, toolVersionSelect+
		` AND t.tool_key = $2 AND tv.version = $3 AND tv.status = 'PUBLISHED'`,
		toolKey, version)
}

// ResolveLatest 解析指定工具键的最新 PUBLISHED 版本（按发布时间倒序）。
func (r *Registry) ResolveLatest(ctx context.Context, tenantID ids.ID, toolKey string) (*ToolVersion, error) {
	return r.query(ctx, tenantID, toolVersionSelect+
		` AND t.tool_key = $2 AND tv.status = 'PUBLISHED'
		  ORDER BY tv.created_at DESC, tv.version DESC LIMIT 1`,
		toolKey)
}

func (r *Registry) query(ctx context.Context, tenantID ids.ID, sql string, args ...any) (*ToolVersion, error) {
	if r == nil || r.Pool == nil {
		return nil, errors.E(errors.CodeInternal, "tools: registry has no pool")
	}
	if tenantID.IsZero() {
		return nil, errors.E(errors.CodeValidationFailed, "tools: tenantID must not be zero")
	}
	params := append([]any{tenantID.String()}, args...)

	var (
		tv        ToolVersion
		idStr     string
		tenStr    string
		capsSlice []string
	)
	err := inTenantTx(ctx, r.Pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, sql, params...).Scan(
			&idStr, &tenStr, &tv.ToolKey, &tv.Version,
			&tv.InputSchema, &tv.OutputSchema, &tv.EffectClass,
			&capsSlice, &tv.ContentDigest,
		)
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, errors.Ef(errors.CodeNotFound, "tools: no published tool version for key=%q", args[0])
		}
		return nil, errors.Wrap(err, errors.CodeInternal, "tools: resolve tool version")
	}
	if tv.ID, err = ids.Parse(idStr); err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "tools: parse tool version id")
	}
	if tv.TenantID, err = ids.Parse(tenStr); err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "tools: parse tenant id")
	}
	tv.Capabilities = capsSlice
	return &tv, nil
}

// inTenantTx 在租户事务中执行 fn（registry.tools/tool_versions 启用 FORCE RLS，
// 读取必须显式注入租户上下文，否则被行级策略过滤）。
func inTenantTx(ctx context.Context, pool *pgxpool.Pool, tenantID ids.ID, fn func(pgx.Tx) error) error {
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
