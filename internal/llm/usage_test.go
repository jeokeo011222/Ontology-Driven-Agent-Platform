package llm

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// memUsageStore 是内存账本，复现 UNIQUE(tenant_id, request_id) 的去重语义。
type memUsageStore struct {
	mu      sync.Mutex
	entries map[string]Usage
}

func newMemUsageStore() *memUsageStore { return &memUsageStore{entries: map[string]Usage{}} }

func key(tenantID ids.ID, requestID string) string { return tenantID.String() + "|" + requestID }

func (m *memUsageStore) insert(ctx context.Context, tenantID ids.ID, u Usage) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key(tenantID, u.RequestID)
	if _, ok := m.entries[k]; ok {
		return false, nil
	}
	m.entries[k] = u
	return true, nil
}

func (m *memUsageStore) sumCost(ctx context.Context, tenantID ids.ID, from, to time.Time) (float64, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var total float64
	var count int64
	prefix := tenantID.String() + "|"
	for k, u := range m.entries {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		total += u.EstimatedCost
		count++
	}
	return total, count, nil
}

func (m *memUsageStore) len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

// TestUsageLedgerDedupe 验证重复 request_id 不重复结算（§16.2）。
func TestUsageLedgerDedupe(t *testing.T) {
	store := newMemUsageStore()
	l := &UsageLedger{store: store}
	tenant := ids.New()
	u := Usage{RequestID: "req-1", RunID: ids.New(), EstimatedCost: 0.5}
	if err := l.Record(context.Background(), tenant, u); err != nil {
		t.Fatalf("Record 1: %v", err)
	}
	u.EstimatedCost = 999 // 重试携带不同估算值也不得再落一条
	if err := l.Record(context.Background(), tenant, u); err != nil {
		t.Fatalf("Record 2: %v", err)
	}
	if got := store.len(); got != 1 {
		t.Fatalf("账本条目数=%d, want 1", got)
	}
	total, count, err := l.SumCost(context.Background(), tenant, time.Time{}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("SumCost: %v", err)
	}
	if count != 1 || total != 0.5 {
		t.Fatalf("SumCost total=%v count=%d, want 0.5/1", total, count)
	}
}

// TestUsageLedgerValidation 验证必填字段校验。
func TestUsageLedgerValidation(t *testing.T) {
	l := &UsageLedger{store: newMemUsageStore()}
	if err := l.Record(context.Background(), ids.ID{}, Usage{RequestID: "x"}); errors.As(err).Code != errors.CodeValidationFailed {
		t.Fatalf("零租户应拒绝，got %v", err)
	}
	if err := l.Record(context.Background(), ids.New(), Usage{}); errors.As(err).Code != errors.CodeValidationFailed {
		t.Fatalf("空 request_id 应拒绝，got %v", err)
	}
}

// TestBudgetExceeded 验证预算超限阻断。
func TestBudgetExceeded(t *testing.T) {
	store := newMemUsageStore()
	l := &UsageLedger{store: store}
	tenant := ids.New()
	if err := l.Record(context.Background(), tenant, Usage{RequestID: "req-1", EstimatedCost: 50}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	g := &BudgetGuard{Ledger: l, Limit: 100, ReserveFactor: 1.2}
	// 已用 50 + 预计 50 × 1.2 = 110 > 100 → 阻断。
	if err := g.Check(context.Background(), tenant, 50); errors.As(err).Code != errors.CodeBudgetExceeded {
		t.Fatalf("超预算应返回 BUDGET_EXCEEDED，got %v", err)
	}
	// 已用 50 + 预计 10 × 1.2 = 62 <= 100 → 放行。
	if err := g.Check(context.Background(), tenant, 10); err != nil {
		t.Fatalf("未超预算应放行，got %v", err)
	}
}

// TestBudgetReserveFactor 验证预留上浮系数生效（§11.2 失败调用可能仍计费）。
func TestBudgetReserveFactor(t *testing.T) {
	l := &UsageLedger{store: newMemUsageStore()}
	tenant := ids.New()
	// 上限 100，预计 90：1.2 上浮后 108 超限；1.0 不超限。
	over := &BudgetGuard{Ledger: l, Limit: 100, ReserveFactor: 1.2}
	if err := over.Check(context.Background(), tenant, 90); errors.As(err).Code != errors.CodeBudgetExceeded {
		t.Fatalf("上浮后应超限，got %v", err)
	}
	exact := &BudgetGuard{Ledger: l, Limit: 100, ReserveFactor: 1.0}
	if err := exact.Check(context.Background(), tenant, 90); err != nil {
		t.Fatalf("无上浮时不应超限，got %v", err)
	}
}
