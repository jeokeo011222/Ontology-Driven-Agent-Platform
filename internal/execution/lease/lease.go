// Package lease 实现任务租约的纯领域逻辑（不依赖数据库与时钟实现）。
//
// 租约解决两个问题（第六篇 §6.2/§6.3）：
//   - 互斥：同一时刻只有一个 Worker 持有某任务的租约；
//   - 防过期写入（fencing）：租约被接管时递增 epoch，旧 Worker 携带旧 epoch
//     的任何写入必须被拒绝，而不是静默覆盖新持有者的结果。
//
// 本包只做判定与规则表达；持久化与原子领取在 internal/harness.Scheduler 中
// 通过条件 UPDATE 落地（数据库层是最终 fencing）。
package lease

import (
	"time"

	"odap/internal/platform/errors"
)

// NotHeld 表示无持有者（空闲租约）。
const NotHeld = ""

// Lease 是一次租约状态快照。
type Lease struct {
	// Owner 是租约持有者标识；NotHeld 表示空闲。
	Owner string
	// ExpiresAt 是租约到期时刻（零值表示从未签发）。
	ExpiresAt time.Time
	// Epoch 是单调递增的执行代次（fencing token）。
	Epoch int64
	// Now 是判定所用的当前时刻（由调用方注入，保证可测试与可重放）。
	Now time.Time
}

// IsExpired 报告租约是否已到期。
//
// 边界约定：到期时刻与当前时刻相等视为**已过期**（<，而非 <=），
// 避免"恰好卡在到期瞬间"的窗口被两个 Worker 同时认领。
func (l Lease) IsExpired() bool {
	if l.ExpiresAt.IsZero() {
		return true
	}
	return l.Now.After(l.ExpiresAt) || l.Now.Equal(l.ExpiresAt)
}

// IsHeld 报告租约当前是否由某个持有者有效持有。
func (l Lease) IsHeld() bool {
	return l.Owner != NotHeld && !l.IsExpired()
}

// Remaining 返回租约剩余时长；已过期为负值或零。
func (l Lease) Remaining() time.Duration {
	return l.ExpiresAt.Sub(l.Now)
}

// Fence 校验写入者的代次是否仍然有效。
//
// candidateEpoch 是写入者持有的代次，holderEpoch 是数据库当前代次：
//   - candidate < holder：租约已被接管，写入者已失去所有权 → CodeExecLeaseLost；
//   - candidate > holder：写入者代次超前，说明状态不一致（不应发生），
//     同样视为失去所有权，交由调用方重新读取状态；
//   - 相等：写入者仍是当前持有者，放行。
func Fence(candidateEpoch, holderEpoch int64) error {
	if candidateEpoch != holderEpoch {
		return errors.Ef(errors.CodeExecLeaseLost,
			"lease: epoch mismatch: candidate=%d holder=%d (stale worker write rejected)",
			candidateEpoch, holderEpoch)
	}
	return nil
}

// Acquire 计算一次"领取/接管"后的租约：代次递增，持有者与到期时间更新。
//
// prev 可以是零值（首次领取）或上一个租约（接管，含已过期租约）。
// ttl <= 0 时按 0 处理（立即过期，仅用于测试显式构造）。
func Acquire(prev Lease, owner string, now time.Time, ttl time.Duration) Lease {
	if ttl < 0 {
		ttl = 0
	}
	return Lease{
		Owner:     owner,
		ExpiresAt: now.Add(ttl),
		Epoch:     prev.Epoch + 1,
		Now:       now,
	}
}

// Renew 计算一次续期后的租约。
//
// 仅当调用方仍是当前持有者（owner 与 epoch 均匹配）且租约尚未过期时才放行；
// 否则返回 CodeExecLeaseLost，调用方必须立即停止推进并放弃本次执行。
// 续期不改变 epoch——代次只在接管时递增。
func Renew(l Lease, owner string, epoch int64, now time.Time, ttl time.Duration) (Lease, error) {
	if l.Owner != owner || l.Epoch != epoch {
		return l, errors.Ef(errors.CodeExecLeaseLost,
			"lease: renew denied: lease held by %q@%d, caller is %q@%d",
			l.Owner, l.Epoch, owner, epoch)
	}
	if now.After(l.ExpiresAt) || now.Equal(l.ExpiresAt) {
		return l, errors.Ef(errors.CodeExecLeaseLost,
			"lease: renew denied: lease expired at %s (now %s)",
			l.ExpiresAt.Format(time.RFC3339), now.Format(time.RFC3339))
	}
	if ttl < 0 {
		ttl = 0
	}
	next := l
	next.ExpiresAt = now.Add(ttl)
	next.Now = now
	return next, nil
}

// Release 归还租约（清空持有者），代次递增以立即作废旧持有者的写入权。
func Release(l Lease) Lease {
	next := l
	next.Owner = NotHeld
	next.Epoch = l.Epoch + 1
	return next
}
