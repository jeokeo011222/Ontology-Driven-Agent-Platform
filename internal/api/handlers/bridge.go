package handlers

import (
	"odap/internal/api/store"
	"odap/internal/harness"
	"odap/internal/platform/ids"
	"odap/internal/platform/tenantctx"
)

// 本文件是 handlers 与既有领域包的薄桥接（类型别名/常量转发/输入映射），
// 目的是让 handler 代码只表达 DTO↔领域输入的转换，不重复声明领域类型。

// storeRunStep 是 store.RunStep 的别名（响应直接嵌入该结构）。
type storeRunStep = store.RunStep

// eventCancellationRequested 转发 harness 事件常量，避免在 handler 中硬编码字符串。
const eventCancellationRequested = harness.EventCancellationRequested

// noFacts 返回零事实快照（取消入口不依赖外部事实，见第六篇 §14.2 铁律③）。
func noFacts() harness.TransitionFacts { return harness.TransitionFacts{} }

// storeRunFilter 构造 Run 列表过滤条件。
func storeRunFilter(status, cursor string, limit int) store.RunListFilter {
	return store.RunListFilter{Status: status, Cursor: cursor, Limit: limit}
}

// storeRunInput 把创建 Run 的 DTO 映射为持久化输入（requested_by 取自身份）。
func storeRunInput(id tenantctx.Identity, agentVersionID ids.ID, req createRunRequest, priority, maxAttempts int) store.CreateRunInput {
	return store.CreateRunInput{
		RequestedBy:    id.SubjectID,
		AgentVersionID: agentVersionID,
		InputPayload:   []byte(req.Input),
		InputDigest:    digestHex(req.Input),
		Priority:       priority,
		MaxAttempts:    maxAttempts,
		DeadlineAt:     req.DeadlineAt,
	}
}
