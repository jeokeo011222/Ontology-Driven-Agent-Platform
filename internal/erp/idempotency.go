package erp

import (
	"crypto/sha256"
	"encoding/hex"

	"odap/internal/platform/ids"
)

// fieldSep 是幂等键派生使用的字段分隔符（U+001F，单元分隔符）。
// 作用：让相邻字段无法通过内容错位伪造出相同的规范化串
// （例如 op="a\x1fb"+target="" 与 op="a"+target="b" 必须得到不同摘要）。
const fieldSep = "\x1f"

// noneTarget 表示"无目标对象"（第七篇 §8.1 规则 4）。
const noneTarget = "none"

// IntentParts 是一次业务意图的派生五元组（第七篇 §8.1）。
//
// 铁律：operation_id（本地主键 erp_operations.id）不进入五元组——
// 否则进程崩溃后按新 attempt 重建 operation 记录时键必然变化，
// 恢复重试就退化为二次提交。
type IntentParts struct {
	RunID  ids.ID
	StepID ids.ID
	// OperationType 是业务命令类型（如 expense.submit），不含 attempt 序号。
	OperationType string
	// TargetObjectID 是目标业务对象；为空时按 "none" 参与派生。
	TargetObjectID string
	// PayloadDigest 是规范化 payload 的 SHA-256（hex）。
	PayloadDigest string
}

// IntentDigest 返回五元组的 SHA-256 hex 摘要（不含 tenant）。
func IntentDigest(p IntentParts) string {
	target := p.TargetObjectID
	if target == "" {
		target = noneTarget
	}
	h := sha256.New()
	_, _ = h.Write([]byte(p.RunID.String()))
	_, _ = h.Write([]byte(fieldSep))
	_, _ = h.Write([]byte(p.StepID.String()))
	_, _ = h.Write([]byte(fieldSep))
	_, _ = h.Write([]byte(p.OperationType))
	_, _ = h.Write([]byte(fieldSep))
	_, _ = h.Write([]byte(target))
	_, _ = h.Write([]byte(fieldSep))
	_, _ = h.Write([]byte(p.PayloadDigest))
	return hex.EncodeToString(h.Sum(nil))
}

// IdempotencyKey 生成平台统一幂等键：
//
//	"odap:" + tenant_id + ":" + hex(SHA-256(run_id||0x1f||step_id||0x1f||
//	                                 operation_type||0x1f||target_object_id||0x1f||payload_digest))
//
// 生成权归 Operation Gateway：Harness 与 Agent 只透传五元组，不得自行拼装键，
// 以免多入口生成规则漂移（第七篇 §8.1 规则 3）。
func IdempotencyKey(tenantID ids.ID, p IntentParts) string {
	return "odap:" + tenantID.String() + ":" + IntentDigest(p)
}
