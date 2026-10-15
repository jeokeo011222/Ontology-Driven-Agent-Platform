//go:build integration

package outbox

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
)

// TestAppendConcurrentSameAggregateNoDuplicateNoGap 验证 §5.3 的核心承诺：
// 同一聚合上并发 Append 时，aggregate_seq 无重复、无跳号。
//
// 8 个 goroutine 各 Append 10 次（共 80 次），断言最终得到的序号集合恰为 1..80。
// 依赖 ODAP_DATABASE_URL（odap_app 角色，FORCE RLS 生效）。
func TestAppendConcurrentSameAggregateNoDuplicateNoGap(t *testing.T) {
	dsn := os.Getenv("ODAP_DATABASE_URL")
	if dsn == "" {
		t.Skip("ODAP_DATABASE_URL 未设置，跳过 PG 集成测试")
	}
	ctx := context.Background()
	pool, err := pgxutil.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	defer pool.Close()

	tenantID := ids.New()
	aggregateID := ids.New()
	w := &Writer{Pool: pool}

	const goroutines = 8
	const perGoroutine = 10

	var (
		mu   sync.Mutex
		seqs = make(map[int64]int)
		wg   sync.WaitGroup
		errs []error
	)
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				ev, err := w.Append(ctx, tenantID, Event{
					AggregateType: "run",
					AggregateID:   aggregateID,
					EventType:     "StepCompleted",
					SchemaVersion: "v1",
					Payload:       []byte(fmt.Sprintf(`{"g":%d,"i":%d}`, g, i)),
					OccurredAt:    time.Now().UTC(),
				})
				if err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
					return
				}
				mu.Lock()
				seqs[ev.AggregateSeq]++
				mu.Unlock()
			}
		}(g)
	}
	wg.Wait()

	if len(errs) > 0 {
		t.Fatalf("并发 Append 出现错误: %v", errs[0])
	}
	if len(seqs) != goroutines*perGoroutine {
		t.Fatalf("序号去重后数量 = %d, 期望 %d（存在重复或缺失）", len(seqs), goroutines*perGoroutine)
	}
	for want := int64(1); want <= goroutines*perGoroutine; want++ {
		if seqs[want] != 1 {
			t.Fatalf("序号 %d 出现 %d 次，期望恰好 1 次", want, seqs[want])
		}
	}
}
