package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"odap/internal/platform/clock"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// ---------------------------------------------------------------------------
// 内存 fake：writerStore / dispatchStore / Sink
// ---------------------------------------------------------------------------

// memWriterStore 用内存计数器模拟"每聚合一行计数器"的语义：
// 同一聚合每次领取得到单调递增且不重复的序号。
type memWriterStore struct {
	counters map[string]int64
	events   []Event
	failOn   int // >0 时对第 failOn 次 insertTx 返回错误（0 表示不注入失败）
	inserts  int
}

func (m *memWriterStore) key(aggType string, aggID ids.ID) string {
	return aggType + "/" + aggID.String()
}

func (m *memWriterStore) allocSeq(_ context.Context, _ pgx.Tx, aggType string, aggID ids.ID) (int64, error) {
	if m.counters == nil {
		m.counters = make(map[string]int64)
	}
	k := m.key(aggType, aggID)
	m.counters[k]++
	return m.counters[k], nil
}

func (m *memWriterStore) insertTx(_ context.Context, _ pgx.Tx, ev Event) error {
	m.inserts++
	if m.failOn > 0 && m.inserts == m.failOn {
		return fmt.Errorf("mem: injected insert failure")
	}
	m.events = append(m.events, ev)
	return nil
}

// memRow 是内存发件箱行。
type memRow struct {
	ev          Event
	status      string
	attempts    int
	available   time.Time
	claimUntil  time.Time
	lastErrCode string
	publishedAt time.Time
}

// memDispatchStore 用内存表模拟 Publisher 的两段式持久化语义。
type memDispatchStore struct {
	rows      []*memRow
	lastBatch int
}

func (m *memDispatchStore) addRow(ev Event, available time.Time) *memRow {
	r := &memRow{ev: ev, status: "PENDING", available: available}
	m.rows = append(m.rows, r)
	return r
}

func (m *memDispatchStore) claim(_ context.Context, _ ids.ID, _ string, batch int, now time.Time, ttl time.Duration) ([]claimedEvent, error) {
	m.lastBatch = batch
	var out []claimedEvent
	for _, r := range m.rows {
		if len(out) >= batch {
			break
		}
		if r.status != "PENDING" || r.available.After(now) {
			continue
		}
		r.status = "DISPATCHING"
		r.claimUntil = now.Add(ttl)
		out = append(out, claimedEvent{Ev: r.ev, AttemptCount: r.attempts})
	}
	return out, nil
}

func (m *memDispatchStore) markSent(_ context.Context, _ ids.ID, eventIDs []ids.ID, at time.Time) error {
	for _, id := range eventIDs {
		for _, r := range m.rows {
			if r.ev.EventID == id && r.status == "DISPATCHING" {
				r.status = "SENT"
				r.attempts++
				r.publishedAt = at
			}
		}
	}
	return nil
}

func (m *memDispatchStore) markFailed(_ context.Context, _ ids.ID, eventID ids.ID, errCode string, at, nextAvailable time.Time, dead bool) error {
	for _, r := range m.rows {
		if r.ev.EventID == eventID {
			r.status = "FAILED"
			if dead {
				r.status = "DEAD"
			}
			r.attempts++
			r.lastErrCode = errCode
			r.available = nextAvailable
			r.claimUntil = time.Time{}
		}
	}
	return nil
}

func (m *memDispatchStore) reclaimExpired(_ context.Context, _ ids.ID, now time.Time, maxAttempts int) (int, error) {
	n := 0
	for _, r := range m.rows {
		if r.status != "DISPATCHING" || !r.claimUntil.Before(now) {
			continue
		}
		if r.attempts >= maxAttempts {
			r.status = "DEAD"
		} else {
			r.status = "PENDING"
			r.available = now
		}
		r.claimUntil = time.Time{}
		n++
	}
	return n, nil
}

// fakeSink 是可注入失败的 Sink。
type fakeSink struct {
	calls []ids.ID
	err   error
}

func (f *fakeSink) Publish(_ context.Context, ev Event) error {
	f.calls = append(f.calls, ev.EventID)
	return f.err
}

// ---------------------------------------------------------------------------
// ① Append 分配 seq = 1,2,3
// ---------------------------------------------------------------------------

func TestAppendAllocatesMonotonicSeq(t *testing.T) {
	w := &Writer{store: &memWriterStore{}}
	tid := ids.New()
	aid := ids.New()

	var got []int64
	for i := 0; i < 3; i++ {
		ev, err := w.AppendTx(context.Background(), nil, Event{
			TenantID:      tid,
			AggregateType: "run",
			AggregateID:   aid,
			EventType:     "StepStarted",
			Payload:       json.RawMessage(`{"i":1}`),
		})
		if err != nil {
			t.Fatalf("第 %d 次 AppendTx 失败: %v", i, err)
		}
		if ev.EventID.IsZero() {
			t.Fatalf("第 %d 次 AppendTx 未分配 EventID", i)
		}
		if ev.PayloadDigest == "" {
			t.Fatalf("第 %d 次 AppendTx 未计算 PayloadDigest", i)
		}
		got = append(got, ev.AggregateSeq)
	}
	want := []int64{1, 2, 3}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("seq 序列 = %v, 期望 %v", got, want)
		}
	}
}

func TestAppendTxRejectsMissingFields(t *testing.T) {
	w := &Writer{store: &memWriterStore{}}
	_, err := w.AppendTx(context.Background(), nil, Event{EventType: "X"})
	if ae, ok := asAppError(err); !ok || ae.Code != errors.CodeValidationFailed {
		t.Fatalf("缺少 tenantID 应返回 VALIDATION_FAILED, 得到 %v", err)
	}
}

// ---------------------------------------------------------------------------
// ② RunOnce 领取-发布-标记 SENT
// ---------------------------------------------------------------------------

func TestRunOncePublishesAndMarksSent(t *testing.T) {
	tid := ids.New()
	clk := &clock.FakeClock{Current: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)}
	st := &memDispatchStore{}
	ev := Event{EventID: ids.New(), TenantID: tid, EventType: "StepCompleted"}
	r := st.addRow(ev, clk.Now().Add(-time.Second))

	sink := &fakeSink{}
	p := &Publisher{Clock: clk, Sink: sink, Owner: "w1", BatchSize: 10, store: st}

	n, err := p.RunOnce(context.Background(), tid)
	if err != nil {
		t.Fatalf("RunOnce 失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("成功发布数 = %d, 期望 1", n)
	}
	if len(sink.calls) != 1 || sink.calls[0] != ev.EventID {
		t.Fatalf("Sink 调用记录异常: %v", sink.calls)
	}
	if r.status != "SENT" {
		t.Fatalf("状态 = %q, 期望 SENT", r.status)
	}
	if r.attempts != 1 {
		t.Fatalf("attempt_count = %d, 期望 1", r.attempts)
	}
	if r.publishedAt.IsZero() {
		t.Fatal("published_at 未写入")
	}
}

// ---------------------------------------------------------------------------
// ③ 发布失败 → FAILED + 退避 + attempt_count+1
// ---------------------------------------------------------------------------

func TestRunOnceMarksFailedWithBackoff(t *testing.T) {
	tid := ids.New()
	clk := &clock.FakeClock{Current: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)}
	st := &memDispatchStore{}
	ev := Event{EventID: ids.New(), TenantID: tid, EventType: "StepCompleted"}
	r := st.addRow(ev, clk.Now().Add(-time.Second))

	sink := &fakeSink{err: errors.E(errors.CodeERPAdapterError, "下游不可用")}
	p := &Publisher{Clock: clk, Sink: sink, Owner: "w1", store: st}

	if _, err := p.RunOnce(context.Background(), tid); err != nil {
		t.Fatalf("RunOnce 失败: %v", err)
	}
	if r.status != "FAILED" {
		t.Fatalf("状态 = %q, 期望 FAILED", r.status)
	}
	if r.attempts != 1 {
		t.Fatalf("attempt_count = %d, 期望 1", r.attempts)
	}
	if r.lastErrCode != errors.CodeERPAdapterError {
		t.Fatalf("last_error_code = %q, 期望 %q", r.lastErrCode, errors.CodeERPAdapterError)
	}
	wantAvail := clk.Now().Add(Backoff(1))
	if !r.available.Equal(wantAvail) {
		t.Fatalf("available_at = %s, 期望 %s（退避后移）", r.available, wantAvail)
	}
}

// ---------------------------------------------------------------------------
// ④ 超过最大尝试 → DEAD
// ---------------------------------------------------------------------------

func TestRunOnceExceedingMaxAttemptsMarksDead(t *testing.T) {
	tid := ids.New()
	clk := &clock.FakeClock{Current: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)}
	st := &memDispatchStore{}
	// 已经完成 MaxAttempts 次尝试，本次为第 MaxAttempts+1 次。
	ev := Event{EventID: ids.New(), TenantID: tid, EventType: "StepCompleted"}
	r := st.addRow(ev, clk.Now().Add(-time.Second))
	r.attempts = MaxAttempts

	sink := &fakeSink{err: errors.E(errors.CodeInternal, "持续失败")}
	p := &Publisher{Clock: clk, Sink: sink, Owner: "w1", store: st}

	if _, err := p.RunOnce(context.Background(), tid); err != nil {
		t.Fatalf("RunOnce 失败: %v", err)
	}
	if r.status != "DEAD" {
		t.Fatalf("状态 = %q, 期望 DEAD", r.status)
	}
	if r.attempts != MaxAttempts+1 {
		t.Fatalf("attempt_count = %d, 期望 %d", r.attempts, MaxAttempts+1)
	}
}

// ---------------------------------------------------------------------------
// ⑤ ReclaimExpired 回收 DISPATCHING 残留
// ---------------------------------------------------------------------------

func TestReclaimExpiredRestoresPendingAndDead(t *testing.T) {
	tid := ids.New()
	clk := &clock.FakeClock{Current: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)}
	st := &memDispatchStore{}

	stuck := st.addRow(Event{EventID: ids.New(), TenantID: tid}, clk.Now())
	stuck.status = "DISPATCHING"
	stuck.claimUntil = clk.Now().Add(-time.Minute)
	stuck.attempts = 1

	exhausted := st.addRow(Event{EventID: ids.New(), TenantID: tid}, clk.Now())
	exhausted.status = "DISPATCHING"
	exhausted.claimUntil = clk.Now().Add(-time.Minute)
	exhausted.attempts = MaxAttempts

	// 未过期的 DISPATCHING 不应被触碰。
	fresh := st.addRow(Event{EventID: ids.New(), TenantID: tid}, clk.Now())
	fresh.status = "DISPATCHING"
	fresh.claimUntil = clk.Now().Add(time.Minute)

	p := &Publisher{Clock: clk, Sink: &fakeSink{}, Owner: "w1", store: st}
	n, err := p.ReclaimExpired(context.Background(), tid)
	if err != nil {
		t.Fatalf("ReclaimExpired 失败: %v", err)
	}
	if n != 2 {
		t.Fatalf("回收条数 = %d, 期望 2", n)
	}
	if stuck.status != "PENDING" {
		t.Fatalf("残留状态 = %q, 期望 PENDING", stuck.status)
	}
	if exhausted.status != "DEAD" {
		t.Fatalf("超限状态 = %q, 期望 DEAD", exhausted.status)
	}
	if fresh.status != "DISPATCHING" {
		t.Fatalf("未过期租约被误回收: %q", fresh.status)
	}
}

// ---------------------------------------------------------------------------
// ⑥ Backoff 序列：100ms,200ms,400ms,...,5s 封顶
// ---------------------------------------------------------------------------

func TestBackoffSequence(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 100 * time.Millisecond},
		{1, 100 * time.Millisecond},
		{2, 200 * time.Millisecond},
		{3, 400 * time.Millisecond},
		{4, 800 * time.Millisecond},
		{5, 1600 * time.Millisecond},
		{6, 3200 * time.Millisecond},
		{7, 5 * time.Second},
		{8, 5 * time.Second},
		{100, 5 * time.Second},
	}
	for _, c := range cases {
		if got := Backoff(c.attempt); got != c.want {
			t.Errorf("Backoff(%d) = %s, 期望 %s", c.attempt, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// ⑦ BatchSize 上限
// ---------------------------------------------------------------------------

func TestRunOnceBatchSizeIsCapped(t *testing.T) {
	cases := []struct {
		name      string
		batchSize int
		wantBatch int
	}{
		{"超上限被截断", 1000, MaxBatchSize},
		{"零取默认", 0, DefaultBatchSize},
		{"正常值透传", 7, 7},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tid := ids.New()
			clk := &clock.FakeClock{Current: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)}
			st := &memDispatchStore{}
			p := &Publisher{Clock: clk, Sink: &fakeSink{}, Owner: "w1", BatchSize: c.batchSize, store: st}
			if _, err := p.RunOnce(context.Background(), tid); err != nil {
				t.Fatalf("RunOnce 失败: %v", err)
			}
			if st.lastBatch != c.wantBatch {
				t.Fatalf("实际批量 = %d, 期望 %d", st.lastBatch, c.wantBatch)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 附加：Publisher 参数校验
// ---------------------------------------------------------------------------

func TestRunOnceValidation(t *testing.T) {
	st := &memDispatchStore{}
	base := &Publisher{Clock: &clock.FakeClock{Current: time.Now()}, Sink: &fakeSink{}, Owner: "w1", store: st}
	if _, err := base.RunOnce(context.Background(), ids.ID{}); err == nil {
		t.Fatal("零租户应被拒绝")
	}
	noOwner := &Publisher{Clock: base.Clock, Sink: base.Sink, store: st}
	if _, err := noOwner.RunOnce(context.Background(), ids.New()); err == nil {
		t.Fatal("空 Owner 应被拒绝")
	}
	noSink := &Publisher{Clock: base.Clock, Owner: "w1", store: st}
	if _, err := noSink.RunOnce(context.Background(), ids.New()); err == nil {
		t.Fatal("nil Sink 应被拒绝")
	}
	var nilPub *Publisher
	if _, err := nilPub.RunOnce(context.Background(), ids.New()); err == nil {
		t.Fatal("nil Publisher 应被拒绝")
	}
}
