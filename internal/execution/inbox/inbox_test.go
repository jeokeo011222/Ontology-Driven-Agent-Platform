package inbox

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"odap/internal/platform/clock"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// ---------------------------------------------------------------------------
// 内存 fake
// ---------------------------------------------------------------------------

type memInboxRecord struct {
	status    string
	lastError string
	processed bool
}

type memDeadLetter struct {
	attempts int
	reason   string
}

// memInboxStore 用内存结构模拟 dedup / inbox / 死信的持久化语义。
type memInboxStore struct {
	dedup       map[string]bool
	records     map[string]*memInboxRecord
	deadLetters map[string]*memDeadLetter
	failDedup   bool
}

func newMemInboxStore() *memInboxStore {
	return &memInboxStore{
		dedup:       make(map[string]bool),
		records:     make(map[string]*memInboxRecord),
		deadLetters: make(map[string]*memDeadLetter),
	}
}

func consentKey(consumer string, ev Event) string {
	return consumer + "/" + ev.EventID.String()
}

func (m *memInboxStore) dedupInsert(_ context.Context, consumer string, ev Event) (bool, error) {
	if m.failDedup {
		return false, fmt.Errorf("mem: injected dedup failure")
	}
	k := consentKey(consumer, ev)
	if m.dedup[k] {
		return false, nil
	}
	m.dedup[k] = true
	return true, nil
}

func (m *memInboxStore) insertReceived(_ context.Context, consumer string, ev Event) error {
	k := consentKey(consumer, ev)
	if _, ok := m.records[k]; !ok {
		m.records[k] = &memInboxRecord{status: "RECEIVED"}
	}
	return nil
}

func (m *memInboxStore) markProcessed(_ context.Context, consumer string, ev Event, _ time.Time) error {
	k := consentKey(consumer, ev)
	r, ok := m.records[k]
	if !ok {
		r = &memInboxRecord{}
		m.records[k] = r
	}
	r.status = "PROCESSED"
	r.processed = true
	return nil
}

func (m *memInboxStore) markFailed(_ context.Context, consumer string, ev Event, errMsg string) error {
	k := consentKey(consumer, ev)
	r, ok := m.records[k]
	if !ok {
		r = &memInboxRecord{}
		m.records[k] = r
	}
	r.status = "FAILED"
	r.lastError = errMsg
	return nil
}

func (m *memInboxStore) upsertDeadLetter(_ context.Context, consumer string, ev Event, reason string) error {
	k := consentKey(consumer, ev)
	dl, ok := m.deadLetters[k]
	if !ok {
		dl = &memDeadLetter{}
		m.deadLetters[k] = dl
	}
	dl.attempts++
	dl.reason = reason
	return nil
}

// countingHandler 记录调用次数，并可按脚本返回错误。
type countingHandler struct {
	calls int
	err   error
}

func (h *countingHandler) Handle(_ context.Context, _ Event) error {
	h.calls++
	return h.err
}

func newEvent() Event {
	return Event{
		EventID:       ids.New(),
		TenantID:      ids.New(),
		EventType:     "StepCompleted",
		Payload:       json.RawMessage(`{"ok":true}`),
		PayloadDigest: "digest-1",
		OccurredAt:    time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC),
	}
}

// ---------------------------------------------------------------------------
// ① 同事件重复投递 Handler 只被调用一次
// ---------------------------------------------------------------------------

func TestProcessDuplicateDeliveryHandlesOnce(t *testing.T) {
	ev := newEvent()
	store := newMemInboxStore()
	h := &countingHandler{}
	c := &Consumer{Clock: &clock.FakeClock{Current: time.Now()}, Name: "consumer-A", Handler: h, store: store}

	for i := 0; i < 3; i++ {
		if err := c.Process(context.Background(), ev); err != nil {
			t.Fatalf("第 %d 次投递失败: %v", i+1, err)
		}
	}
	if h.calls != 1 {
		t.Fatalf("Handler 调用次数 = %d, 期望 1（幂等）", h.calls)
	}
	if got := store.records[consentKey("consumer-A", ev)].status; got != "PROCESSED" {
		t.Fatalf("处理记录状态 = %q, 期望 PROCESSED", got)
	}
}

// ---------------------------------------------------------------------------
// ② Handler 失败 → FAILED + 死信登记
// ---------------------------------------------------------------------------

func TestProcessHandlerFailureRecordsDeadLetter(t *testing.T) {
	ev := newEvent()
	store := newMemInboxStore()
	h := &countingHandler{err: errors.E(errors.CodeERPAdapterError, "下游拒绝")}
	c := &Consumer{Clock: &clock.FakeClock{Current: time.Now()}, Name: "consumer-A", Handler: h, store: store}

	err := c.Process(context.Background(), ev)
	if err == nil {
		t.Fatal("Handler 失败时 Process 应返回错误")
	}
	rec := store.records[consentKey("consumer-A", ev)]
	if rec == nil || rec.status != "FAILED" {
		t.Fatalf("处理记录 = %+v, 期望状态 FAILED", rec)
	}
	if rec.lastError == "" {
		t.Fatal("FAILED 记录应写入 last_error")
	}
	dl := store.deadLetters[consentKey("consumer-A", ev)]
	if dl == nil {
		t.Fatal("失败事件应登记死信")
	}
	if dl.attempts != 1 {
		t.Fatalf("死信 attempt_count = %d, 期望 1", dl.attempts)
	}
	// 明文 payload 不入死信表：内存实现只保存引用与摘要，不含 Payload 内容。
}

// ---------------------------------------------------------------------------
// ③ dedup 命中短路不调用 Handler
// ---------------------------------------------------------------------------

func TestProcessDedupHitShortCircuits(t *testing.T) {
	ev := newEvent()
	store := newMemInboxStore()
	// 预置去重键，模拟该事件此前已被处理。
	store.dedup[consentKey("consumer-A", ev)] = true
	h := &countingHandler{}
	c := &Consumer{Clock: &clock.FakeClock{Current: time.Now()}, Name: "consumer-A", Handler: h, store: store}

	if err := c.Process(context.Background(), ev); err != nil {
		t.Fatalf("短路应返回 nil, 得到 %v", err)
	}
	if h.calls != 0 {
		t.Fatalf("dedup 命中时 Handler 不应被调用, 实际 %d 次", h.calls)
	}
	if len(store.records) != 0 {
		t.Fatalf("dedup 命中不应写入新的处理记录: %+v", store.records)
	}
}

// ---------------------------------------------------------------------------
// ④ 死信重复登记不违反唯一约束
// ---------------------------------------------------------------------------

func TestDeadLetterUpsertNoUniqueViolation(t *testing.T) {
	ev := newEvent()
	store := newMemInboxStore()
	h := &countingHandler{err: errors.E(errors.CodeInternal, "确定性失败")}
	c := &Consumer{Clock: &clock.FakeClock{Current: time.Now()}, Name: "consumer-A", Handler: h, store: store}

	// 第一次投递：失败并登记死信。
	if err := c.Process(context.Background(), ev); err == nil {
		t.Fatal("首次处理应失败")
	}
	// 强行为同一事件再投递一次：先清除 dedup 键以模拟独立投递通道。
	delete(store.dedup, consentKey("consumer-A", ev))
	if err := c.Process(context.Background(), ev); err == nil {
		t.Fatal("再次处理应失败")
	}

	dl := store.deadLetters[consentKey("consumer-A", ev)]
	if dl == nil {
		t.Fatal("死信应存在")
	}
	if dl.attempts != 2 {
		t.Fatalf("死信 attempt_count = %d, 期望 2（去重登记累加而非报唯一冲突）", dl.attempts)
	}
	if len(store.deadLetters) != 1 {
		t.Fatalf("同一 (consumer, event) 只应有一条死信, 实际 %d 条", len(store.deadLetters))
	}
}

// 附加：参数校验。
func TestProcessValidation(t *testing.T) {
	store := newMemInboxStore()
	h := &countingHandler{}
	base := &Consumer{Name: "c", Handler: h, store: store}

	ev := newEvent()
	ev.EventID = ids.ID{}
	if err := base.Process(context.Background(), ev); err == nil {
		t.Fatal("零 EventID 应被拒绝")
	}
	ev = newEvent()
	ev.TenantID = ids.ID{}
	if err := base.Process(context.Background(), ev); err == nil {
		t.Fatal("零 TenantID 应被拒绝")
	}
	noHandler := &Consumer{Name: "c", store: store}
	if err := noHandler.Process(context.Background(), newEvent()); err == nil {
		t.Fatal("nil Handler 应被拒绝")
	}
	noName := &Consumer{Handler: h, store: store}
	if err := noName.Process(context.Background(), newEvent()); err == nil {
		t.Fatal("空 Name 应被拒绝")
	}
}
