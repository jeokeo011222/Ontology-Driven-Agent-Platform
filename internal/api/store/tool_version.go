package store

import (
	"encoding/json"
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
