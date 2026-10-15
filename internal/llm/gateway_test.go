package llm

import (
	"context"
	"encoding/json"
	"testing"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

const testPlanSchema = `{"type":"object","required":["x"],"properties":{"x":{"type":"string"}}}`

// TestCompleteStructured 验证结构化修复回路的能力声明区分（§9.1）。
func TestCompleteStructured(t *testing.T) {
	t.Run("native schema 直接请求不进入修复回路", func(t *testing.T) {
		p := NewMockProvider("native", ProviderCapabilities{NativeSchema: true})
		p.PushContent("{not json")
		req := CompletionRequest{ResponseSchema: json.RawMessage(testPlanSchema)}
		if _, err := CompleteStructured(context.Background(), p, req); err != nil {
			t.Fatalf("native schema 应直接返回，got %v", err)
		}
		if p.CallCount() != 1 {
			t.Fatalf("native schema 调用次数=%d, want 1", p.CallCount())
		}
	})

	t.Run("generate-then-validate 首次非法第二次纠正成功", func(t *testing.T) {
		p := NewMockProvider("gtv", ProviderCapabilities{NativeSchema: false})
		p.PushContent("{not json").PushContent(`{"x":"ok"}`)
		req := CompletionRequest{ResponseSchema: json.RawMessage(testPlanSchema)}
		comp, err := CompleteStructured(context.Background(), p, req)
		if err != nil {
			t.Fatalf("第二次应纠正成功，got %v", err)
		}
		if comp.Content != `{"x":"ok"}` {
			t.Fatalf("content=%q", comp.Content)
		}
		if p.CallCount() != 2 {
			t.Fatalf("调用次数=%d, want 2（1 初始 + 1 重生成）", p.CallCount())
		}
	})

	t.Run("generate-then-validate N 次后仍失败", func(t *testing.T) {
		p := NewMockProvider("gtv", ProviderCapabilities{NativeSchema: false})
		for i := 0; i < MaxSchemaRepairAttempts+1; i++ {
			p.PushContent(`{"y":1}`) // 缺少 required x
		}
		req := CompletionRequest{ResponseSchema: json.RawMessage(testPlanSchema)}
		_, err := CompleteStructured(context.Background(), p, req)
		if errors.As(err).Code != errors.CodeModelOutputInvalid {
			t.Fatalf("应返回 MODEL_OUTPUT_INVALID，got %v", err)
		}
		if p.CallCount() != MaxSchemaRepairAttempts+1 {
			t.Fatalf("调用次数=%d, want %d", p.CallCount(), MaxSchemaRepairAttempts+1)
		}
	})

	t.Run("供应商错误不进入修复回路", func(t *testing.T) {
		p := NewMockProvider("gtv", ProviderCapabilities{})
		p.PushError(MockTimeoutError())
		req := CompletionRequest{ResponseSchema: json.RawMessage(testPlanSchema)}
		if _, err := CompleteStructured(context.Background(), p, req); errors.As(err).Code != errors.CodeModelTimeout {
			t.Fatalf("应原样返回超时错误，got %v", err)
		}
		if p.CallCount() != 1 {
			t.Fatalf("供应商错误不应重试，调用次数=%d", p.CallCount())
		}
	})
}

// TestGatewayComplete 表驱动覆盖网关编排的六类路径。
func TestGatewayComplete(t *testing.T) {
	tenant := ids.MustParse("11111111-1111-4111-8111-111111111111")

	type tc struct {
		name     string
		setup    func(t *testing.T) (*Gateway, *MockProvider, CompletionRequest)
		wantCode string
		check    func(t *testing.T, g *Gateway, mock *MockProvider, comp Completion)
	}

	cases := []tc{
		{
			name: "限流拒绝",
			setup: func(t *testing.T) (*Gateway, *MockProvider, CompletionRequest) {
				mock := NewMockProvider("mock", ProviderCapabilities{})
				mock.PushContent("ok")
				lim := testLimiter()
				lim.Params = map[string]BucketParams{
					KindInteractive: {RPM: 60, Concurrency: 100, Burst: 1},
				}
				// 预先消耗唯一令牌（释放并发但不回填令牌）。
				release, err := lim.Acquire(context.Background(), tenant, KindInteractive)
				if err != nil {
					t.Fatalf("预置 Acquire: %v", err)
				}
				release()
				g := &Gateway{Providers: map[string]Provider{"mock": mock}, Limiter: lim}
				return g, mock, reqWith("m", DataClassInternal, "hi")
			},
			wantCode: errors.CodeRateLimited,
			check: func(t *testing.T, _ *Gateway, mock *MockProvider, _ Completion) {
				if mock.CallCount() != 0 {
					t.Fatalf("被限流时不应调用供应商，调用次数=%d", mock.CallCount())
				}
			},
		},
		{
			name: "预算阻断",
			setup: func(t *testing.T) (*Gateway, *MockProvider, CompletionRequest) {
				mock := NewMockProvider("mock", ProviderCapabilities{})
				mock.PushContent("ok")
				ledger := &UsageLedger{store: newMemUsageStore()}
				g := &Gateway{
					Providers: map[string]Provider{"mock": mock},
					Budget:    &BudgetGuard{Ledger: ledger, Limit: 0.0001, ReserveFactor: 1.2},
				}
				return g, mock, reqWith("m", DataClassInternal, "hi")
			},
			wantCode: errors.CodeBudgetExceeded,
			check: func(t *testing.T, _ *Gateway, mock *MockProvider, _ Completion) {
				if mock.CallCount() != 0 {
					t.Fatalf("超预算时不应调用供应商，调用次数=%d", mock.CallCount())
				}
			},
		},
		{
			name: "缓存命中降权",
			setup: func(t *testing.T) (*Gateway, *MockProvider, CompletionRequest) {
				mock := NewMockProvider("mock", ProviderCapabilities{})
				mock.PushContent("should-not-be-used")
				cache := NewSemanticCache(nil, nil, testClock())
				req := reqWith("m", DataClassInternal, "缓存问题")
				if err := cache.Store(context.Background(), tenant, req, Completion{Content: "cached", ModelRevision: "r1"}); err != nil {
					t.Fatalf("预置缓存: %v", err)
				}
				g := &Gateway{Providers: map[string]Provider{"mock": mock}, Cache: cache}
				return g, mock, req
			},
			check: func(t *testing.T, _ *Gateway, mock *MockProvider, comp Completion) {
				if comp.Content != "cached" || !comp.CacheHit || comp.CacheLayer != CacheL1 {
					t.Fatalf("应命中 L1 缓存，comp=%+v", comp)
				}
				if w := DowngradeWeight(comp); !(w < 1) {
					t.Fatalf("命中应降权，weight=%v", w)
				}
				if mock.CallCount() != 0 {
					t.Fatalf("命中缓存不应调用供应商，调用次数=%d", mock.CallCount())
				}
			},
		},
		{
			name: "provider 超时",
			setup: func(t *testing.T) (*Gateway, *MockProvider, CompletionRequest) {
				mock := NewMockProvider("mock", ProviderCapabilities{})
				mock.PushError(MockTimeoutError())
				g := &Gateway{Providers: map[string]Provider{"mock": mock}}
				return g, mock, reqWith("m", DataClassInternal, "hi")
			},
			wantCode: errors.CodeModelTimeout,
		},
		{
			name: "输出非法重试2次后失败",
			setup: func(t *testing.T) (*Gateway, *MockProvider, CompletionRequest) {
				mock := NewMockProvider("mock", ProviderCapabilities{NativeSchema: false})
				for i := 0; i < MaxSchemaRepairAttempts+1; i++ {
					mock.PushContent("{not json")
				}
				g := &Gateway{Providers: map[string]Provider{"mock": mock}}
				req := reqWith("m", DataClassInternal, "hi")
				req.ResponseSchema = json.RawMessage(testPlanSchema)
				return g, mock, req
			},
			wantCode: errors.CodeModelOutputInvalid,
			check: func(t *testing.T, _ *Gateway, mock *MockProvider, _ Completion) {
				if mock.CallCount() != MaxSchemaRepairAttempts+1 {
					t.Fatalf("调用次数=%d, want %d", mock.CallCount(), MaxSchemaRepairAttempts+1)
				}
			},
		},
		{
			name: "成功路径记账",
			setup: func(t *testing.T) (*Gateway, *MockProvider, CompletionRequest) {
				mock := NewMockProvider("mock", ProviderCapabilities{})
				mock.PushContent(`{"ok":true}`)
				ledger := &UsageLedger{store: newMemUsageStore()}
				g := &Gateway{Providers: map[string]Provider{"mock": mock}, Ledger: ledger}
				req := reqWith("m", DataClassInternal, "hi")
				req.IdempotencyKey = "req-1"
				req.RunID = ids.New()
				return g, mock, req
			},
			check: func(t *testing.T, g *Gateway, mock *MockProvider, comp Completion) {
				if comp.Content != `{"ok":true}` {
					t.Fatalf("content=%q", comp.Content)
				}
				if mock.CallCount() != 1 {
					t.Fatalf("成功路径调用次数=%d, want 1", mock.CallCount())
				}
				store, ok := g.Ledger.store.(*memUsageStore)
				if !ok {
					t.Fatalf("ledger store 类型异常")
				}
				if store.len() != 1 {
					t.Fatalf("账本条目数=%d, want 1", store.len())
				}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g, mock, req := c.setup(t)
			comp, err := g.Complete(context.Background(), tenant, req)
			if c.wantCode != "" {
				if got := errors.As(err).Code; got != c.wantCode {
					t.Fatalf("错误码=%s, want %s (err=%v)", got, c.wantCode, err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.check != nil {
				c.check(t, g, mock, comp)
			}
		})
	}
}

// TestGatewayNoEligibleProvider 验证无合格模型返回 UNAVAILABLE 语义错误（§6.2）。
func TestGatewayNoEligibleProvider(t *testing.T) {
	mock := NewMockProvider("mock", ProviderCapabilities{SupportsTools: false})
	req := reqWith("m", DataClassInternal, "hi")
	req.Tools = []ToolSpec{{Name: "t", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	g := &Gateway{Providers: map[string]Provider{"mock": mock}}
	_, err := g.Complete(context.Background(), ids.New(), req)
	if errors.As(err).Code != errors.CodeUnavailable {
		t.Fatalf("应返回 UNAVAILABLE，got %v", err)
	}
	if mock.CallCount() != 0 {
		t.Fatalf("无合格模型不应调用供应商")
	}
}
