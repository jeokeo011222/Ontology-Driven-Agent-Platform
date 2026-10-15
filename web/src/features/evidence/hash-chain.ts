import type { AuditEvent } from '@/types';

export interface ChainCheck {
  /** 相邻事件之间的 prev_hash → hash 链接是否连续（按服务端返回顺序） */
  linked: boolean;
  /** 已核对的事件数 */
  checked: number;
  /** 首个断链处的事件 ID */
  brokenAt?: string;
}

/**
 * 前端哈希链连贯性检查（仅用于展示与诊断）。
 *
 * 边界声明（§8.7）：
 *   哈希能够帮助检测内容变化，但**不能证明证据内容在生成时就是真实的**，
 *   也不能防止拥有足够底层权限的攻击者改写全部数据。
 *   因此重要场景还需要受控写入、权限分离、外部备份与不可变存储；
 *   本前端检查不作为任何完整性担保。
 */
export function verifyChain(events: AuditEvent[]): ChainCheck {
  if (events.length <= 1) return { linked: true, checked: events.length };

  for (let i = 0; i < events.length - 1; i += 1) {
    const current = events[i];
    const older = events[i + 1];
    if (current.prev_hash !== older.hash) {
      return { linked: false, checked: i, brokenAt: current.id };
    }
  }
  return { linked: true, checked: events.length };
}
