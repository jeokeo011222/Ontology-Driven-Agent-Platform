package llm

import (
	"context"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/platform/clock"
	"odap/internal/platform/secretbox"
)

// BuildGenerationGateway 装配"生成/执行类调用"专用的模型网关——cmd/api（生成端点）
// 与 cmd/worker（步进执行器）共用的唯一装配实现。
//
// 与 llm-check 的差别只有一处，但这一处是硬约束：**只接受真实 HTTP 端点**。
// MOCK 端点在装配前被显式剔除，避免本体/Agent/工具的自动生成与 Run 步进执行落到
// 回放式 MockProvider 上，产出看起来像真的、实际是回放的伪造内容
// （项目硬约束：严禁 mock 数据、严禁静默降级）。
//
// 没有任何可用 HTTP 端点时返回 nil：调用方据此 fail-closed
// （生成端点返回 UNAVAILABLE；worker 对 LLM 步骤记 RunFailed 并说明原因），
// 而不是"生成成功"却写进假数据。
func BuildGenerationGateway(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger,
	secrets *secretbox.Cipher) *Gateway {
	loader := &RegistryLoader{Pool: pool}
	bindings, excluded, err := loader.Load(ctx)
	if err != nil {
		logger.Error("生成网关：Model Registry 装载失败，自动生成将不可用", "error", err)
		return nil
	}

	kept := make([]EndpointBinding, 0, len(bindings))
	for _, b := range bindings {
		if b.DeploymentType != "HTTP" {
			excluded = append(excluded, Exclusion{
				ProviderID: b.ProviderID, EndpointID: b.EndpointID,
				EndpointName: b.EndpointName, ModelID: b.ModelID,
				Reason: "非 HTTP 端点（MOCK/回放）不参与自动生成，避免产出伪造内容",
			})
			continue
		}
		kept = append(kept, b)
	}

	ledger := NewUsageLedger(pool)
	gw := BuildGateway(kept, excluded, GatewayOptions{
		Logger:  logger,
		Getenv:  os.Getenv,
		Secrets: secrets,
		Limiter: NewLimiter(clock.RealClock{}),
		Ledger:  ledger,
		Budget:  &BudgetGuard{Ledger: ledger, Limit: 5.0, ReserveFactor: DefaultReserveFactor},
		Cache:   NewSemanticCache(nil, nil, clock.RealClock{}),
		Kind:    KindBatch,
	})
	if len(gw.Providers) == 0 {
		logger.Warn("生成网关：没有可用的真实 HTTP 模型端点，自动生成端点将返回 UNAVAILABLE",
			"excluded", len(gw.Unavailable))
		return nil
	}
	logger.Info("生成网关已装配", "providers", len(gw.Providers), "notes", len(gw.Unavailable))
	return gw
}
