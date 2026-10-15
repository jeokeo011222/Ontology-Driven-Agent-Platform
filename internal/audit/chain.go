// Package audit 实现审计事件哈希链与审计写入 fail-closed 分级。
//
// 权威来源：docs/第十一篇：可观测性、审计证据链与企业级运维治理详细设计.md
//   - §9.1 四层完整性：① 事件级摘要 → ② 分段哈希链 → ③ 周期性检查点 →
//     ④ 数字签名与归档。本包实现第①②层（事件摘要与段内哈希链）。
//   - §9.2/§9.7 审计写入 fail-closed 分级：高危操作审计失败必须阻断。
//
// append-only 约束：audit.audit_events 的应用角色只有 INSERT + SELECT 权限
// （UPDATE/DELETE 已被 REVOKE），且存在 BEFORE UPDATE OR DELETE 触发器直接
// raise exception。本包对该表**只 INSERT、只 SELECT**，绝不 UPDATE/DELETE；
// 修正错误数据只能追加新事件（补偿语义）。
package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/platform/clock"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
)

// fieldSep 是摘要公式中的协议域分隔符（0x1f），避免不同字段拼接被错误混用。
const fieldSep = 0x1f

// Event 是一条审计事件（与 audit.audit_events 行对应）。
type Event struct {
	ID           ids.ID
	TenantID     ids.ID
	EventType    string
	ActorType    string
	ActorID      string
	ResourceType string
	ResourceID   string
	Action       string
	Outcome      string
	RequestID    string
	RunID        ids.ID
	PolicyRefs   json.RawMessage
	EventPayload json.RawMessage
	// PayloadCanonical 是写入时确定的规范化 JSON 字节，事件摘要覆盖它。
	// 为什么需要单独一列（集成测试实测缺陷）：event_payload 是 JSONB，
	// PostgreSQL 会对写入值重新序列化（键按「长度+字节序」重排、数字改写为
	// 规范表示如 1e2→100、键值间补空格），因此「写入时对原始字节算摘要、
	// 校验时对回读文本算摘要」必然不一致。摘要必须覆盖调用方自己确定并
	// 逐字节持久化的字节序列。
	PayloadCanonical string
	SegmentID        string
	SeqNo            int64
	PrevDigest       string
	EventDigest      string
	OccurredAt       time.Time
}

// canonicalJSON 递归规范化 JSON：对象键按字典序排序、去除无意义空白，
// 保证同一语义载荷产生同一字节序列（§9.3 规范化编码要求）。
//
// 实现要点：
//   - 使用 json.Decoder 的 UseNumber，保留数字原始字面量，避免 float64
//     往返造成的精度与格式漂移（如 1.10 -> 1.1）；
//   - 解码为 any 后由 json.Marshal 重新编码，encoding/json 对 map 键自动
//     按字典序排序、并压缩空白；数组元素顺序作为语义保留。
func canonicalJSON(raw json.RawMessage) ([]byte, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return []byte("null"), nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "audit: canonicalize json payload")
	}
	// 拒绝尾随内容，避免"看似合法"的多值载荷进入摘要。
	if dec.More() {
		return nil, errors.E(errors.CodeInternal, "audit: trailing data after json payload")
	}
	return json.Marshal(v)
}

// payloadBytes 返回事件摘要所覆盖的载荷字节。
//
// 优先使用写入时确定并持久化的规范化字节（PayloadCanonical，来自
// audit.audit_events.event_payload_canonical）；为空时由 EventPayload 现场
// 规范化（纯函数测试路径，不触库）。
func (ev Event) payloadBytes() []byte {
	if ev.PayloadCanonical != "" {
		return []byte(ev.PayloadCanonical)
	}
	b, err := canonicalJSON(ev.EventPayload)
	if err != nil {
		// 规范化失败时退化为原始字节，保证摘要函数始终有定义（不会 panic）；
		// 调用方在写入前应保证 payload 合法 JSON。
		return []byte(ev.EventPayload)
	}
	return b
}

// EventDigest 计算事件摘要（第十一篇 §9.1 四层完整性的第一层：事件级摘要）。
//
// 公式（严格按字节拼接，字段之间以 0x1f 分隔）：
//
//	SHA-256(
//	    segment_id        || 0x1f ||
//	    seq_no(十进制)     || 0x1f ||
//	    prev_digest       || 0x1f ||
//	    canonical_payload || 0x1f ||
//	    occurred_at(RFC3339Nano, UTC) || 0x1f ||
//	    action            || 0x1f ||
//	    resource_type     || 0x1f ||
//	    resource_id
//	)
//
// 其中 canonical_payload 取 ev.PayloadCanonical（写入时确定的规范化字节），
// 缺省时退化为 canonicalJSON(ev.EventPayload)——见 payloadBytes 的说明：
// 摘要绝不能覆盖数据库对 JSONB 的再序列化结果。
//
// 返回十六进制小写。occurred_at 统一转换为 UTC 并**取整到微秒**后格式化：
// PostgreSQL 的 timestamptz 只保留微秒精度（多余位按就近取整），若摘要覆盖
// 纳秒级原始值，写入与回读两侧算出的字节序列必然不同。写入路径（Append）
// 也先把同一取整结果落库，因此「摘要覆盖的字节 == 数据库中的值」恒成立。
//
// 摘要是第②层"分段哈希链"的链节：H_i = SHA256(prev_digest || C(E_i))，
// 由 prev_digest 字段承载前序摘要，从而把段内事件连成一条可验证的链。
func EventDigest(prevDigest string, ev Event) string {
	var buf bytes.Buffer
	buf.WriteString(ev.SegmentID)
	buf.WriteByte(fieldSep)
	buf.WriteString(strconv.FormatInt(ev.SeqNo, 10))
	buf.WriteByte(fieldSep)
	buf.WriteString(prevDigest)
	buf.WriteByte(fieldSep)
	buf.Write(ev.payloadBytes())
	buf.WriteByte(fieldSep)
	buf.WriteString(ev.OccurredAt.UTC().Round(time.Microsecond).Format(time.RFC3339Nano))
	buf.WriteByte(fieldSep)
	buf.WriteString(ev.Action)
	buf.WriteByte(fieldSep)
	buf.WriteString(ev.ResourceType)
	buf.WriteByte(fieldSep)
	buf.WriteString(ev.ResourceID)
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:])
}

// jsonSemanticallyEqual 比较两段 JSON 的语义相等性，用于校验
// event_payload（JSONB）与 event_payload_canonical 是否描述同一载荷。
//
// 容忍数据库的表示差异（对象键重排、数字改写如 1e2→100、空白差异），
// 但不容忍任何值级修改：数组顺序敏感，字符串/布尔/null 精确比较，
// 数字按数值比较。
func jsonSemanticallyEqual(a, b []byte) bool {
	av, err := decodeAny(a)
	if err != nil {
		return false
	}
	bv, err := decodeAny(b)
	if err != nil {
		return false
	}
	return jsonValueEqual(av, bv)
}

// decodeAny 解码 JSON 且保留数字原始字面量（UseNumber）。
func decodeAny(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// jsonValueEqual 递归比较解码后的 JSON 值（语义相等，数字按数值比较）。
func jsonValueEqual(a, b any) bool {
	switch av := a.(type) {
	case nil:
		return b == nil
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case json.Number:
		bv, ok := b.(json.Number)
		if !ok {
			return false
		}
		af, aerr := av.Float64()
		bf, berr := bv.Float64()
		if aerr != nil || berr != nil {
			return av.String() == bv.String()
		}
		return af == bf
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !jsonValueEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			w, ok := bv[k]
			if !ok || !jsonValueEqual(v, w) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// verifyChain 校验一段审计事件的哈希链（纯函数，PG 版 Verify 复用）。
//
// 校验项：
//   - 按 seq_no 升序重排后，seq_no 必须从 1 起连续无空洞；
//   - 第一条事件 prev_digest 必须为空串，其后每条 prev_digest 必须等于
//     前一条的重算摘要（链式衔接）；
//   - 每条事件重算的 EventDigest 必须与存储值一致。
//
// 任一项不符返回 CodeEvidenceIntegrityBroken（§9.6 审计验证失败不得静默跳过）。
func verifyChain(events []Event) error {
	if len(events) == 0 {
		return nil
	}
	sorted := make([]Event, len(events))
	copy(sorted, events)
	// 乱序输入应被排序后判定（事件可能因并发读取或分区裁剪而乱序返回）。
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].SeqNo < sorted[j].SeqNo })

	prev := ""
	for i := range sorted {
		ev := sorted[i]
		wantSeq := int64(i + 1)
		if ev.SeqNo != wantSeq {
			return errors.Ef(errors.CodeEvidenceIntegrityBroken,
				"audit: 段 %s 序号不连续：第 %d 条 seq_no=%d，期望 %d",
				ev.SegmentID, i+1, ev.SeqNo, wantSeq)
		}
		if ev.PrevDigest != prev {
			return errors.Ef(errors.CodeEvidenceIntegrityBroken,
				"audit: 段 %s seq_no=%d prev_digest 断链（存储 %q，期望 %q）",
				ev.SegmentID, ev.SeqNo, ev.PrevDigest, prev)
		}
		// event_payload 是可查询的 JSONB 索引，其内容必须与摘要覆盖的规范化
		// 副本语义一致（容忍 jsonb 的键重排与数字改写，不容忍值级修改）；
		// 两列任一被篡改都在此暴露。
		if ev.PayloadCanonical != "" {
			want, err := canonicalJSON(ev.EventPayload)
			if err != nil || !jsonSemanticallyEqual(want, []byte(ev.PayloadCanonical)) {
				return errors.Ef(errors.CodeEvidenceIntegrityBroken,
					"audit: 段 %s seq_no=%d 载荷与规范化副本不一致（event_payload 与 event_payload_canonical 描述的不是同一载荷）",
					ev.SegmentID, ev.SeqNo)
			}
		}
		computed := EventDigest(prev, ev)
		if computed != ev.EventDigest {
			return errors.Ef(errors.CodeEvidenceIntegrityBroken,
				"audit: 段 %s seq_no=%d 事件摘要不匹配（存储 %q，重算 %q）",
				ev.SegmentID, ev.SeqNo, ev.EventDigest, computed)
		}
		prev = computed
	}
	return nil
}

// Recorder 以 append-only 方式写入审计事件（只 INSERT）。
type Recorder struct {
	Pool  *pgxpool.Pool
	Clock clock.Clock
}

// Append 在单个租户事务内分配段序号、读取链头并写入一条审计事件。
//
// 步骤（§9.4 并发写入与顺序）：
//  1. seq := audit.next_segment_seq(tenant, segment)——每租户每段一行计数器，
//     行锁分配段内序号，禁止 max+1 或自建序列；
//  2. prev := 该段 seq_no 最大一条的 event_digest（无行则 ""）；
//  3. 计算 EventDigest(prev, ev)；
//  4. INSERT audit.audit_events（只 INSERT，绝不 UPDATE/DELETE）。
//
// segment_id 为空时取 occurred_at 的 UTC 日期 "2006-01-02"。
func (r *Recorder) Append(ctx context.Context, ev Event) (Event, error) {
	if r == nil || r.Pool == nil {
		return ev, errors.E(errors.CodeInternal, "audit: recorder has no pool")
	}
	if ev.TenantID.IsZero() {
		return ev, errors.E(errors.CodeValidationFailed, "audit: tenantID must not be zero")
	}
	if ev.EventType == "" || ev.Action == "" || ev.ResourceType == "" {
		return ev, errors.E(errors.CodeValidationFailed,
			"audit: eventType, action and resourceType must not be empty")
	}
	if ev.ID.IsZero() {
		ev.ID = ids.New()
	}
	clk := r.Clock
	if clk == nil {
		clk = clock.RealClock{}
	}
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = clk.Now()
	}
	// 取整到微秒并与摘要使用同一值落库：timestamptz 只有微秒精度，
	// 若落库值与摘要覆盖值不同，回读重算必然不匹配。
	ev.OccurredAt = ev.OccurredAt.UTC().Round(time.Microsecond)
	if ev.SegmentID == "" {
		ev.SegmentID = ev.OccurredAt.UTC().Format("2006-01-02")
	}
	if len(ev.PolicyRefs) == 0 {
		ev.PolicyRefs = json.RawMessage("[]")
	}
	if len(ev.EventPayload) == 0 {
		ev.EventPayload = json.RawMessage("{}")
	}

	err := inTenantTx(ctx, r.Pool, ev.TenantID, func(tx pgx.Tx) error {
		var seq int64
		if err := tx.QueryRow(ctx, nextSegmentSeqSQL,
			ev.TenantID.String(), ev.SegmentID).Scan(&seq); err != nil {
			return err
		}
		prev, err := lastDigest(ctx, tx, ev.TenantID, ev.SegmentID)
		if err != nil {
			return err
		}
		ev.SeqNo = seq
		ev.PrevDigest = prev
		// 规范化字节在写入时确定并单独持久化，摘要覆盖它——绝不能覆盖
		// 数据库对 JSONB 的再序列化结果（见 000015 迁移说明）。
		canon, cerr := canonicalJSON(ev.EventPayload)
		if cerr != nil {
			return cerr
		}
		ev.PayloadCanonical = string(canon)
		ev.EventDigest = EventDigest(prev, ev)

		var runID any
		if !ev.RunID.IsZero() {
			runID = ev.RunID.String()
		}
		_, err = tx.Exec(ctx, insertAuditSQL,
			ev.ID.String(), ev.TenantID.String(), ev.EventType, ev.ActorType,
			ev.ActorID, ev.ResourceType, ev.ResourceID, ev.Action, ev.Outcome,
			ev.RequestID, runID, string(ev.PolicyRefs), string(ev.EventPayload),
			ev.PayloadCanonical, ev.EventDigest, ev.SegmentID, ev.SeqNo,
			ev.PrevDigest, ev.OccurredAt)
		return err
	})
	if err != nil {
		return ev, wrapInternal(err, "audit: append event")
	}
	return ev, nil
}

// Verify 读取指定租户指定段的全部审计事件并校验哈希链完整性。
//
// 读取按 seq_no 升序；校验逻辑复用纯函数 verifyChain。任一不符返回
// CodeEvidenceIntegrityBroken。
func (r *Recorder) Verify(ctx context.Context, tenantID ids.ID, segmentID string) error {
	if r == nil || r.Pool == nil {
		return errors.E(errors.CodeInternal, "audit: recorder has no pool")
	}
	if tenantID.IsZero() {
		return errors.E(errors.CodeValidationFailed, "audit: tenantID must not be zero")
	}
	if segmentID == "" {
		return errors.E(errors.CodeValidationFailed, "audit: segmentID must not be empty")
	}
	var events []Event
	err := inTenantTx(ctx, r.Pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, selectSegmentSQL, tenantID.String(), segmentID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				ev        Event
				idStr     string
				tenStr    string
				runStr    string
				policy    string
				payload   string
				canonical string
			)
			if err := rows.Scan(&idStr, &tenStr, &ev.EventType, &ev.ActorType,
				&ev.ActorID, &ev.ResourceType, &ev.ResourceID, &ev.Action,
				&ev.Outcome, &ev.RequestID, &runStr, &policy, &payload,
				&canonical, &ev.EventDigest, &ev.SegmentID, &ev.SeqNo,
				&ev.PrevDigest, &ev.OccurredAt); err != nil {
				return err
			}
			if ev.ID, err = ids.Parse(idStr); err != nil {
				return err
			}
			if ev.TenantID, err = ids.Parse(tenStr); err != nil {
				return err
			}
			if runStr != "" {
				if ev.RunID, err = ids.Parse(runStr); err != nil {
					return err
				}
			}
			ev.PolicyRefs = json.RawMessage(policy)
			ev.EventPayload = json.RawMessage(payload)
			ev.PayloadCanonical = canonical
			events = append(events, ev)
		}
		return rows.Err()
	})
	if err != nil {
		return wrapInternal(err, "audit: verify segment")
	}
	return verifyChain(events)
}

// lastDigest 读取段内 seq_no 最大一条事件的摘要作为链头；无行返回 ""。
func lastDigest(ctx context.Context, tx pgx.Tx, tenantID ids.ID, segmentID string) (string, error) {
	var prev string
	err := tx.QueryRow(ctx, lastDigestSQL, tenantID.String(), segmentID).Scan(&prev)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return prev, nil
}

// wrapInternal 仅在错误尚未携带稳定错误码时包装为 INTERNAL。
func wrapInternal(err error, msg string) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*errors.AppError); ok {
		return err
	}
	return errors.Wrap(err, errors.CodeInternal, msg)
}

// inTenantTx 在租户事务（SET LOCAL app.tenant_id）中执行 fn 并提交。
// audit.audit_events 启用了 FORCE RLS，读写必须显式注入租户上下文。
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

// nextSegmentSeqSQL 调用数据库函数按"每租户每段一行计数器"行锁分配段内序号。
const nextSegmentSeqSQL = `SELECT audit.next_segment_seq($1, $2)`

// lastDigestSQL 读取段内链头（禁止 max(seq_no)+1 之类自建序列，此处仅读摘要）。
const lastDigestSQL = `
SELECT event_digest
  FROM audit.audit_events
 WHERE tenant_id = $1
   AND segment_id = $2
 ORDER BY seq_no DESC
 LIMIT 1`

// insertAuditSQL 只 INSERT，绝不 UPDATE/DELETE（append-only）。
const insertAuditSQL = `
INSERT INTO audit.audit_events
    (id, tenant_id, event_type, actor_type, actor_id, resource_type,
     resource_id, action, outcome, request_id, run_id, policy_refs,
     event_payload, event_payload_canonical, event_digest, segment_id, seq_no,
     prev_digest, occurred_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
        $13, $14, $15, $16, $17, $18, $19)`

// selectSegmentSQL 按段读取全部事件（升序），用于哈希链校验；run_id 以文本读取；
// event_payload_canonical 是摘要覆盖源，必须读回。
const selectSegmentSQL = `
SELECT id, tenant_id, event_type, actor_type, COALESCE(actor_id, ''),
       resource_type, resource_id, action, outcome, COALESCE(request_id, ''),
       COALESCE(run_id::text, ''), policy_refs, event_payload,
       event_payload_canonical, event_digest,
       segment_id, seq_no, COALESCE(prev_digest, ''), occurred_at
  FROM audit.audit_events
 WHERE tenant_id = $1
   AND segment_id = $2
 ORDER BY seq_no ASC`
