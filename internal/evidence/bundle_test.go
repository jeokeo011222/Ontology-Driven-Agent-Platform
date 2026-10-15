package evidence

import (
	"encoding/json"
	"testing"
	"time"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

func sampleItems() []EvidenceItem {
	return []EvidenceItem{
		{Type: "SOURCE_FACT", SourceType: "erp", SourceRef: "invoice-1", ContentDigest: "d1", Summary: "发票"},
		{Type: "POLICY_DECISION", SourceType: "authz", SourceRef: "decision-1", ContentDigest: "d2"},
		{Type: "ERP_RECEIPT", SourceType: "erp", SourceRef: "receipt-1", ContentDigest: "d3", Metadata: json.RawMessage(`{"x":1}`)},
	}
}

func integrityBroken(err error) bool {
	ae, ok := err.(*errors.AppError)
	return ok && ae.Code == errors.CodeEvidenceIntegrityBroken
}

func TestBuildAndVerifyBundle(t *testing.T) {
	tid := ids.New()
	rid := ids.New()
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)

	b, err := BuildBundle(tid, rid, sampleItems(), now)
	if err != nil {
		t.Fatalf("BuildBundle 失败: %v", err)
	}
	if b.BundleDigest == "" {
		t.Fatal("BundleDigest 不应为空")
	}
	if !b.CreatedAt.Equal(now) {
		t.Fatalf("CreatedAt = %s, 期望 %s", b.CreatedAt, now)
	}
	if err := VerifyBundle(b); err != nil {
		t.Fatalf("VerifyBundle 应通过，实际: %v", err)
	}
}

func TestBuildBundleValidation(t *testing.T) {
	tid := ids.New()
	rid := ids.New()
	now := time.Now()

	if _, err := BuildBundle(ids.ID{}, rid, sampleItems(), now); err == nil {
		t.Fatal("零 tenantID 应被拒绝")
	}
	if _, err := BuildBundle(tid, ids.ID{}, sampleItems(), now); err == nil {
		t.Fatal("零 runID 应被拒绝")
	}

	// 空分项（缺少 ContentDigest）。
	items := sampleItems()
	items[1].ContentDigest = ""
	_, err := BuildBundle(tid, rid, items, now)
	if err == nil || !integrityBroken(err) {
		t.Fatalf("空 ContentDigest 应返回 CodeEvidenceIntegrityBroken，得到 %v", err)
	}
}

func TestBundleDigestOrderIrrelevant(t *testing.T) {
	tid := ids.New()
	rid := ids.New()
	now := time.Now()

	a, err := BuildBundle(tid, rid, sampleItems(), now)
	if err != nil {
		t.Fatal(err)
	}
	// 打乱输入顺序，集合相同 → 摘要相同。
	shuffled := []EvidenceItem{sampleItems()[2], sampleItems()[0], sampleItems()[1]}
	b, err := BuildBundle(tid, rid, shuffled, now)
	if err != nil {
		t.Fatal(err)
	}
	if a.BundleDigest != b.BundleDigest {
		t.Fatalf("顺序不同但集合相同的证据包摘要应相同: %s vs %s", a.BundleDigest, b.BundleDigest)
	}
}

func TestVerifyBundleDetectsTamper(t *testing.T) {
	tid := ids.New()
	rid := ids.New()
	now := time.Now()

	cases := []struct {
		name   string
		mutate func(b *Bundle)
	}{
		{
			name:   "篡改某一分项 digest",
			mutate: func(b *Bundle) { b.Items[0].ContentDigest = "tampered" },
		},
		{
			name:   "缺分项",
			mutate: func(b *Bundle) { b.Items = b.Items[:len(b.Items)-1] },
		},
		{
			name:   "分项 digest 置空",
			mutate: func(b *Bundle) { b.Items[0].ContentDigest = "" },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := BuildBundle(tid, rid, sampleItems(), now)
			if err != nil {
				t.Fatal(err)
			}
			c.mutate(b)
			if err := VerifyBundle(b); err == nil || !integrityBroken(err) {
				t.Fatalf("期望 CodeEvidenceIntegrityBroken，得到 %v", err)
			}
		})
	}

	// nil bundle
	if err := VerifyBundle(nil); err == nil || !integrityBroken(err) {
		t.Fatalf("nil bundle 应返回 CodeEvidenceIntegrityBroken，得到 %v", err)
	}
}
