package audit

import (
	"context"
	"sync"

	"odap/internal/platform/errors"
)

// Criticality 表示一次审计写入所保护操作的风险等级（第十一篇 §9.7）。
type Criticality int

const (
	// CriticalityNormal 是普通操作：查询、普通状态变更、一般任务事件。
	// 审计写入失败时缓冲补写，调用方记录但不阻断业务。
	CriticalityNormal Criticality = iota
	// CriticalityCritical 是高危操作：ERP 写操作、权限变更（角色/策略/授权）、
	// 证据导出。审计写入失败必须阻断执行，不得继续操作。
	CriticalityCritical
)

// Appender 是审计写入的最小接口，*Recorder 实现它。
// 抽象出来便于测试注入失败，且使 WriteOrBlock 不直接依赖数据库。
type Appender interface {
	Append(ctx context.Context, ev Event) (Event, error)
}

// Buffer 是审计写入的缓冲补写通道（§9.7 普通操作路径）。
// 生产实现应对接本地 WAL；测试使用 MemBuffer。
type Buffer interface {
	Append(ev Event) error
}

// MemBuffer 是 Buffer 的内存实现（仅供测试与非持久化场景）。
type MemBuffer struct {
	mu     sync.Mutex
	events []Event
}

// Append 追加一条待补写事件。
func (b *MemBuffer) Append(ev Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, ev)
	return nil
}

// Len 返回已缓冲事件数。
func (b *MemBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.events)
}

// Events 返回已缓冲事件的副本。
func (b *MemBuffer) Events() []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Event, len(b.events))
	copy(out, b.events)
	return out
}

// WriteOrBlock 按风险等级执行审计写入（第十一篇 §9.7 fail-closed 分级）。
//
// 策略：
//   - CriticalityCritical（ERP 写操作、权限变更、证据导出）：写入失败必须
//     返回 CodeAuditWriteFailed 交由调用方阻断业务——高危操作不得在审计缺失
//     的情况下继续执行；此路径**不写缓冲**，因为阻断是唯一正确结果。
//   - CriticalityNormal（查询、普通状态变更、一般任务事件）：写入失败时尝试
//     buf.Append 缓冲补写（best-effort），仍返回 CodeAuditWriteFailed，调用方
//     记录并监控补写延迟，但不阻断业务。
//
// 成功路径不触碰缓冲。
func WriteOrBlock(ctx context.Context, app Appender, buf Buffer, ev Event, crit Criticality) error {
	if app == nil {
		return errors.E(errors.CodeInternal, "audit: nil appender")
	}
	if _, err := app.Append(ctx, ev); err == nil {
		return nil
	} else if crit == CriticalityCritical {
		// 高危：阻断，不写缓冲。
		return errors.Wrap(err, errors.CodeAuditWriteFailed,
			"audit: 高危操作审计写入失败，已阻断业务（fail-closed）")
	} else {
		// 普通：尽力缓冲补写，不阻断业务。
		if buf != nil {
			_ = buf.Append(ev)
		}
		return errors.Wrap(err, errors.CodeAuditWriteFailed,
			"audit: 审计写入失败，已缓冲补写（不阻断业务）")
	}
}

var (
	_ Appender = (*Recorder)(nil)
	_ Buffer   = (*MemBuffer)(nil)
)
