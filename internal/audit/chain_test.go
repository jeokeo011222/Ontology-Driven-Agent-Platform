package audit

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// ---------------------------------------------------------------------------
// 纯函数：EventDigest 稳定性
// ---------------------------------------------------------------------------

func baseEvent() Event {
	return Event{
		ID:           ids.New(),
		TenantID:     ids.New(),
		EventType:    "PermissionChanged",
		ActorType:    "USER",
		ActorID:      "u-1",
		ResourceType: "role",
		ResourceID:   "role-1",
		Action:       "grant",
		Outcome:      "SUCCESS",
		SegmentID:    "tenant_01:2026-10-13",
		SeqNo:        7,
		EventPayload: json.RawMessage(`{"b":2,"a":{"y":1,"x":[1,2,3]}}`),
		OccurredAt:   time.Date(2026, 10, 13, 12, 0, 0, 123456789, time.UTC),
	}
}

func TestEventDigestStable(t *testing.T) {
	ev := baseEvent()
	d1 := EventDigest("prev-hash", ev)
	d2 := EventDigest("prev-hash", ev)
	if d1 != d2 {
		t.Fatalf("同一 (prev, ev) 应产生同一摘要: %s vs %s", d1, d2)
	}
	if len(d1) != 64 {
		t.Fatalf("摘要应为 64 位十六进制小写, 实际长度 %d", len(d1))
	}
	if strings.ToLower(d1) != d1 {
		t.Fatalf("摘要应为小写十六进制: %s", d1)
	}
}

func TestEventDigestFieldSensitivity(t *testing.T) {
	base := baseEvent()
	d0 := EventDigest("prev-hash", base)

	mutators := map[string]func(*Event){
		"segment_id":    func(e *Event) { e.SegmentID = "other-segment" },
		"seq_no":        func(e *Event) { e.SeqNo++ },
		"event_payload": func(e *Event) { e.EventPayload = json.RawMessage(`{"a":1}`) },
		// 摘要对应的 occurred_at 精度为微秒（timestamptz 精度），
		// 因此以微秒为最小变化单位。
		"occurred_at":   func(e *Event) { e.OccurredAt = e.OccurredAt.Add(time.Microsecond) },
		"action":        func(e *Event) { e.Action = "revoke" },
		"resource_type": func(e *Event) { e.ResourceType = "policy" },
		"resource_id":   func(e *Event) { e.ResourceID = "role-2" },
	}
	for name, mutate := range mutators {
		t.Run(name, func(t *testing.T) {
			ev := baseEvent()
			mutate(&ev)
			if got := EventDigest("prev-hash", ev); got == d0 {
				t.Fatalf("字段 %s 变化后摘要不应相同", name)
			}
		})
	}

	// prev_digest 是 EventDigest 的输入参数（写入与校验时等于 ev.PrevDigest），
	// 其变化必须改变摘要。
	if EventDigest("prev-hash", baseEvent()) == EventDigest("other-prev", baseEvent()) {
		t.Fatal("prev_digest 参数变化后摘要不应相同")
	}
}

func TestEventDigestPayloadKeyOrderIrrelevant(t *testing.T) {
	a := baseEvent()
	a.EventPayload = json.RawMessage(`{"x":1,"y":{"p":true,"q":null}}`)
	b := baseEvent()
	// 同一语义、不同键序与空白。
	b.EventPayload = json.RawMessage("{\n  \"y\": {\"q\": null, \"p\": true},\n  \"x\": 1\n}")
	if EventDigest("p", a) != EventDigest("p", b) {
		t.Fatal("event_payload 的 map 键序不同应产生相同摘要")
	}
}

// ---------------------------------------------------------------------------
// 摘要覆盖源：PayloadCanonical（真实数据库往返一致性）
// ---------------------------------------------------------------------------

// TestEventDigestPrefersPayloadCanonical 摘要必须覆盖写入时确定的规范化字节，
// 而不是数据库对 JSONB 的再序列化结果（见迁移 000015 说明）。
func TestEventDigestPrefersPayloadCanonical(t *testing.T) {
	ev := baseEvent()
	// 模拟数据库回读：键被重排、补空格、数字被改写为规范表示。
	ev.EventPayload = json.RawMessage(`{"a": {"x": [1, 2, 3], "y": 1}, "b": 100}`)
	ev.PayloadCanonical = `{"a":{"x":[1,2,3],"y":1},"b":1e2}`

	canonicalOnly := ev
	canonicalOnly.EventPayload = json.RawMessage(ev.PayloadCanonical)

	if EventDigest("p", ev) != EventDigest("p", canonicalOnly) {
		t.Fatal("摘要应覆盖 PayloadCanonical，而非 EventPayload 的数据库表示")
	}
	// 若忽略 PayloadCanonical，摘要本会不同（证明该字段确实在起作用）。
	bare := ev
	bare.PayloadCanonical = ""
	if EventDigest("p", bare) == EventDigest("p", canonicalOnly) {
		t.Fatal("EventPayload 与规范化字节文本不同时，忽略 PayloadCanonical 应得到不同摘要")
	}
}

// TestVerifyChainDetectsPayloadCanonicalMismatch event_payload 被篡改但
// event_payload_canonical 未同步时，必须被校验发现。
func TestVerifyChainDetectsPayloadCanonicalMismatch(t *testing.T) {
	segment := "tenant_01:2026-10-13"
	evs := buildChain(segment, `{"a":1}`, `{"a":2}`, `{"a":3}`)
	for i := range evs {
		canon, err := canonicalJSON(evs[i].EventPayload)
		if err != nil {
			t.Fatalf("canonicalJSON: %v", err)
		}
		evs[i].PayloadCanonical = string(canon)
	}
	if err := verifyChain(evs); err != nil {
		t.Fatalf("带规范化副本的合法链应通过: %v", err)
	}

	evs[1].EventPayload = json.RawMessage(`{"a":999}`)
	if err := verifyChain(evs); err == nil || !integrityBroken(err) {
		t.Fatalf("event_payload 被篡改应检出 EVIDENCE_INTEGRITY_BROKEN，实际 %v", err)
	}
}

// TestJSONSemanticallyEqual 语义相等：容忍表示差异，不容忍值级修改。
func TestJSONSemanticallyEqual(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"键序无关", `{"a":1,"b":2}`, `{"b":2,"a":1}`, true},
		{"空白无关", "{\n \"a\": 1\n}", `{"a":1}`, true},
		{"数字改写容忍（1e2 vs 100）", `{"n":1e2}`, `{"n":100}`, true},
		{"嵌套等价", `{"a":{"x":[1,2]}}`, `{ "a" : { "x" : [ 1 , 2 ] } }`, true},
		{"值不同", `{"a":1}`, `{"a":2}`, false},
		{"数组顺序敏感", `{"a":[1,2]}`, `{"a":[2,1]}`, false},
		{"缺键", `{"a":1,"b":2}`, `{"a":1}`, false},
		{"类型不同", `{"a":"1"}`, `{"a":1}`, false},
		{"null 等价", `{"a":null}`, `{"a":null}`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := jsonSemanticallyEqual([]byte(c.a), []byte(c.b)); got != c.want {
				t.Fatalf("jsonSemanticallyEqual(%s,%s) = %v, want %v", c.a, c.b, got, c.want)
			}
		})
	}
}

// TestEventDigestOCCurredAtMicrosecondPrecision occurred_at 摘要取整到微秒：
// 纳秒差异（数据库无法保留）不影响摘要，微秒差异必须影响。
func TestEventDigestOccurredAtMicrosecondPrecision(t *testing.T) {
	a := baseEvent()
	b := baseEvent()
	b.OccurredAt = b.OccurredAt.Add(time.Nanosecond) // 微秒内差异
	if EventDigest("p", a) != EventDigest("p", b) {
		t.Fatal("同一微秒内的纳秒差异不应改变摘要（timestamptz 精度为微秒）")
	}
	c := baseEvent()
	c.OccurredAt = c.OccurredAt.Add(time.Microsecond)
	if EventDigest("p", a) == EventDigest("p", c) {
		t.Fatal("微秒级差异必须改变摘要")
	}
}

// ---------------------------------------------------------------------------
// 纯函数：canonicalJSON 稳定性
// ---------------------------------------------------------------------------

func TestCanonicalJSON(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"对象键排序", `{"b":1,"a":2}`, `{"a":2,"b":1}`},
		{"嵌套对象排序", `{"z":{"b":1,"a":[3,2,1]},"a":1}`, `{"a":1,"z":{"a":[3,2,1],"b":1}}`},
		{"数组顺序保留", `[3,1,2]`, `[3,1,2]`},
		{"去除空白", "{\n  \"a\" :  [ 1 , 2 ]\n}", `{"a":[1,2]}`},
		{"空载荷归一为 null", ``, `null`},
		{"数字字面量保留", `{"n":1.10}`, `{"n":1.10}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := canonicalJSON(json.RawMessage(c.in))
			if err != nil {
				t.Fatalf("canonicalJSON 出错: %v", err)
			}
			if string(got) != c.want {
				t.Fatalf("canonicalJSON(%q) = %q, 期望 %q", c.in, got, c.want)
			}
		})
	}

	if _, err := canonicalJSON(json.RawMessage(`{bad json`)); err == nil {
		t.Fatal("非法 JSON 应返回错误")
	}
}

// ---------------------------------------------------------------------------
// verifyChain 检测能力
// ---------------------------------------------------------------------------

// buildChain 构造一条合法的段内哈希链。
func buildChain(segment string, payloads ...string) []Event {
	base := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	out := make([]Event, 0, len(payloads))
	prev := ""
	for i, p := range payloads {
		ev := Event{
			ID:           ids.New(),
			TenantID:     ids.New(),
			SegmentID:    segment,
			SeqNo:        int64(i + 1),
			Action:       "act",
			ResourceType: "res",
			ResourceID:   "r-1",
			EventPayload: json.RawMessage(p),
			OccurredAt:   base.Add(time.Duration(i) * time.Second),
		}
		ev.PrevDigest = prev
		ev.EventDigest = EventDigest(prev, ev)
		prev = ev.EventDigest
		out = append(out, ev)
	}
	return out
}

func integrityBroken(err error) bool {
	ae, ok := err.(*errors.AppError)
	return ok && ae.Code == errors.CodeEvidenceIntegrityBroken
}

func TestVerifyChain(t *testing.T) {
	segment := "tenant_01:2026-10-13"

	cases := []struct {
		name    string
		events  func() []Event
		wantErr bool
	}{
		{
			name:   "正常链通过",
			events: func() []Event { return buildChain(segment, `{"a":1}`, `{"a":2}`, `{"a":3}`) },
		},
		{
			name: "篡改 payload 被检出",
			events: func() []Event {
				evs := buildChain(segment, `{"a":1}`, `{"a":2}`, `{"a":3}`)
				evs[1].EventPayload = json.RawMessage(`{"a":999}`)
				return evs
			},
			wantErr: true,
		},
		{
			name: "篡改 seq_no 留空洞被检出",
			events: func() []Event {
				evs := buildChain(segment, `{"a":1}`, `{"a":2}`, `{"a":3}`)
				evs[2].SeqNo = 4
				return evs
			},
			wantErr: true,
		},
		{
			name: "prev_digest 断链被检出",
			events: func() []Event {
				evs := buildChain(segment, `{"a":1}`, `{"a":2}`, `{"a":3}`)
				evs[2].PrevDigest = "tampered-prev"
				return evs
			},
			wantErr: true,
		},
		{
			name: "乱序输入排序后判定通过",
			events: func() []Event {
				evs := buildChain(segment, `{"a":1}`, `{"a":2}`, `{"a":3}`)
				return []Event{evs[2], evs[0], evs[1]}
			},
		},
		{
			name:   "空段视为通过",
			events: func() []Event { return nil },
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := verifyChain(c.events())
			if c.wantErr {
				if err == nil {
					t.Fatal("期望完整性错误，实际通过")
				}
				if !integrityBroken(err) {
					t.Fatalf("期望 CodeEvidenceIntegrityBroken，得到 %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("期望通过，实际错误: %v", err)
			}
		})
	}
}
