package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// 工具调用状态机（runtime.tool_invocations.status）。
//
//	READ / INTERNAL_WRITE（无需审批）：
//	  EXECUTING → SUCCEEDED | FAILED
//	EXTERNAL_WRITE（需审批，fail-closed）：
//	  PENDING_APPROVAL → APPROVED → EXECUTING → SUCCEEDED | FAILED
//	                  → REJECTED
//
// 状态推进只能经 TransitionToolInvocation（带前置状态守卫的 CAS 更新），
// 杜绝"审批通过后重复执行"与"终态被改写"。
const (
	InvocationPendingApproval = "PENDING_APPROVAL"
	InvocationApproved        = "APPROVED"
	InvocationExecuting       = "EXECUTING"
	InvocationSucceeded       = "SUCCEEDED"
	InvocationFailed          = "FAILED"
	InvocationRejected        = "REJECTED"
)

// invocationTerminalStates 是终态集合：终态记录不可再被任何转换修改。
var invocationTerminalStates = map[string]bool{
	InvocationSucceeded: true,
	InvocationFailed:    true,
	InvocationRejected:  true,
}

// TenantTxRows 是 TenantTx 的投影便捷封装（返回行集）。
func (s *Store) TenantTxRows(ctx context.Context, fn func(pgx.Tx) ([]map[string]any, error)) ([]map[string]any, error) {
	var out []map[string]any
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := fn(tx)
		if err != nil {
			return err
		}
		out = rows
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// invocationColumns 是 tool_invocations 的显式投影（uuid/jsonb 一律 ::text）。
const invocationColumns = `
        id::text, tool_id::text, tool_version_id::text, tool_key, effect_class,
        status, input_payload::text, output_payload::text,
        error_code, error_summary, requested_by, approved_by, approval_decided_at::text,
        started_at::text, finished_at::text, created_at::text, updated_at::text`

// CreateToolInvocationInput 是创建调用记录的输入。
type CreateToolInvocationInput struct {
	ToolID        ids.ID
	ToolVersionID ids.ID
	ToolKey       string
	EffectClass   string
	Status        string
	InputPayload  []byte
	RequestedBy   string
}

// CreateToolInvocation 落一条调用记录（同事务返回投影）。
func (s *Store) CreateToolInvocation(ctx context.Context, in CreateToolInvocationInput) (map[string]any, error) {
	if in.ToolID.IsZero() || in.ToolVersionID.IsZero() || in.ToolKey == "" || in.EffectClass == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "tool_id/tool_version_id/tool_key/effect_class 必填")
	}
	if !json.Valid(in.InputPayload) {
		return nil, errors.E(errors.CodeInvalidArgument, "input_payload 必须是合法 JSON")
	}
	invID := ids.New()
	rows, err := s.TenantTxRows(ctx, func(tx pgx.Tx) ([]map[string]any, error) {
		// tenant_id 必须显式写入 app.current_tenant_id()：本表 FORCE RLS，
		// WITH CHECK (tenant_id = app.current_tenant_id())，漏写会让 tenant_id 为 NULL
		// 从而被策略拒绝（表现为 rows.Err() 上抛 "遍历结果失败"）。
		return queryMaps(ctx, tx, `
            INSERT INTO runtime.tool_invocations
                (id, tenant_id, tool_id, tool_version_id, tool_key, effect_class,
                 status, input_payload, requested_by)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5, $6, $7::jsonb, $8)
            RETURNING `+invocationColumns,
			invID.String(), in.ToolID.String(), in.ToolVersionID.String(),
			in.ToolKey, in.EffectClass, in.Status, string(in.InputPayload), in.RequestedBy)
	})
	if err != nil {
		return nil, err
	}
	return rows[0], nil
}

// GetToolInvocation 按 id 读取调用记录。
func (s *Store) GetToolInvocation(ctx context.Context, invID ids.ID) (map[string]any, error) {
	if invID.IsZero() {
		return nil, errors.E(errors.CodeInvalidArgument, "调用 id 不合法")
	}
	rows, err := s.TenantTxRows(ctx, func(tx pgx.Tx) ([]map[string]any, error) {
		return queryMaps(ctx, tx, `
            SELECT `+invocationColumns+` FROM runtime.tool_invocations WHERE id = $1`,
			invID.String())
	})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.E(errors.CodeNotFound, "工具调用记录不存在")
	}
	return rows[0], nil
}

// ListToolInvocations 返回工具调用记录（cursor 分页 + ?status=）。
func (s *Store) ListToolInvocations(ctx context.Context, limit int, cursor, toolID, status string) (*ListResult, error) {
	if status != "" && !invocationStatusValid(status) {
		return nil, errors.E(errors.CodeInvalidArgument, "status 不在工具调用状态取值集合内")
	}
	from, err := decKey(cursor)
	if err != nil {
		return nil, err
	}
	res := &ListResult{Items: []map[string]any{}}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT `+invocationColumns+` FROM runtime.tool_invocations
             WHERE ($1::text IS NULL OR created_at > $1::timestamptz)
               AND ($2::text IS NULL OR tool_id::text = $2)
               AND ($3::text IS NULL OR status = $3)
             ORDER BY created_at ASC, id ASC
             LIMIT $4`,
			nullableStr(from), nullableStr(strings.TrimSpace(toolID)), nullableStr(status), limit+1)
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

func invocationStatusValid(status string) bool {
	switch status {
	case InvocationPendingApproval, InvocationApproved, InvocationExecuting,
		InvocationSucceeded, InvocationFailed, InvocationRejected, "CANCELLED":
		return true
	}
	return false
}

// TransitionToolInvocationInput 是状态推进的输入（CAS：fromStatus 必须与当前一致）。
type TransitionToolInvocationInput struct {
	InvID     ids.ID
	From      string
	To        string
	Output    []byte
	ErrorCode *string
	ErrorSum  *string
	// ApprovedBy / DecidedAt 仅在审批类转换（approve/reject）时提供。
	ApprovedBy *string
	DecidedAt  *time.Time
	// StartClock / FinishClock：分别置 started_at / finished_at = now()。
	StartClock  bool
	FinishClock bool
}

// TransitionToolInvocation 以乐观锁推进状态机。
//
// 返回推进后的记录。前置状态不匹配或已是终态 → CodeConflict（409）：
// 调用方（审批/执行）据此向用户明确说明"状态已被其他人推进"，绝不覆盖。
func (s *Store) TransitionToolInvocation(ctx context.Context, in TransitionToolInvocationInput) (map[string]any, error) {
	if in.InvID.IsZero() {
		return nil, errors.E(errors.CodeInvalidArgument, "调用 id 不合法")
	}
	if in.From == "" || in.To == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "from/to 状态必填")
	}
	if !invocationStatusValid(in.To) {
		return nil, errors.E(errors.CodeInvalidArgument, "目标状态不在取值集合内")
	}
	if invocationTerminalStates[in.From] {
		return nil, errors.E(errors.CodeConflict, "调用已处于终态，不能再变更")
	}
	if in.Output != nil && !json.Valid(in.Output) {
		return nil, errors.E(errors.CodeInvalidArgument, "output_payload 必须是合法 JSON")
	}
	rows, err := s.TenantTxRows(ctx, func(tx pgx.Tx) ([]map[string]any, error) {
		return queryMaps(ctx, tx, `
            UPDATE runtime.tool_invocations
               SET status = $2,
                   output_payload = COALESCE($3::jsonb, output_payload),
                   error_code = COALESCE($4, error_code),
                   error_summary = COALESCE($5, error_summary),
                   approved_by = COALESCE($6, approved_by),
                   approval_decided_at = COALESCE($7, approval_decided_at),
                   started_at = CASE WHEN $8 THEN now() ELSE started_at END,
                   finished_at = CASE WHEN $9 THEN now() ELSE finished_at END,
                   updated_at = now()
             WHERE id = $1 AND status = $10
            RETURNING `+invocationColumns,
			in.InvID.String(), in.To,
			nullableJSONText(in.Output), in.ErrorCode, in.ErrorSum,
			in.ApprovedBy, in.DecidedAt, in.StartClock, in.FinishClock, in.From)
	})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.E(errors.CodeConflict,
			"工具调用状态已被其他请求推进（期望 "+in.From+"），请刷新后重试")
	}
	return rows[0], nil
}
