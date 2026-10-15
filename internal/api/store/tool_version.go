package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// ToolVersionRecord 是工具最新版本的执行元数据（供参数校验与调用前置检查）。
//
// execution_config 是该工具的真实执行契约（adapter + 对象/操作映射），是
// 工具测试调用（POST /tools/{id}/invoke → handlers/tool_executors.go）的
// 派发依据：没有它，执行器只能瞎猜"该调哪个 ERP 对象"。
type ToolVersionRecord struct {
	ToolID          string
	ToolKey         string
	VersionID       string
	Version         string
	EffectClass     string
	InputSchema     json.RawMessage
	OutputSchema    json.RawMessage
	ExecutionConfig json.RawMessage
	Status          string
}

// LatestToolVersion 返回工具的最新版本（含输入/输出 Schema 与执行契约）。
func (s *Store) LatestToolVersion(ctx context.Context, toolID ids.ID) (*ToolVersionRecord, error) {
	if toolID.IsZero() {
		return nil, errors.E(errors.CodeInvalidArgument, "tool_id 不合法")
	}
	var out *ToolVersionRecord
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT t.tool_key, v.id::text AS version_id, v.version, v.effect_class,
                   v.input_schema::text AS input_schema, v.output_schema::text AS output_schema,
                   v.execution_config::text AS execution_config, v.status
              FROM registry.tools t
              JOIN registry.tool_versions v ON v.tool_id = t.id
             WHERE t.id = $1
             ORDER BY v.created_at DESC
             LIMIT 1`, toolID.String())
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errors.E(errors.CodeNotFound, "工具不存在或没有可用版本")
		}
		row := rows[0]
		rec := &ToolVersionRecord{
			ToolID:      toolID.String(),
			ToolKey:     asString(row["tool_key"]),
			VersionID:   asString(row["version_id"]),
			Version:     asString(row["version"]),
			EffectClass: asString(row["effect_class"]),
			Status:      asString(row["status"]),
		}
		if raw := asString(row["input_schema"]); raw != "" {
			rec.InputSchema = json.RawMessage(raw)
		}
		if raw := asString(row["output_schema"]); raw != "" {
			rec.OutputSchema = json.RawMessage(raw)
		}
		if raw := asString(row["execution_config"]); raw != "" {
			rec.ExecutionConfig = json.RawMessage(raw)
		}
		out = rec
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// asString 归一化 map 值到字符串（json.RawMessage / string）。
func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.RawMessage:
		return string(t)
	case []byte:
		return string(t)
	default:
		return ""
	}
}
