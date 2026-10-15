package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/llm"
	"odap/internal/platform/clock"
	"odap/internal/platform/secretbox"
)

// buildGenerationGateway 装配"生成类端点"专用的模型网关。
//
// 与 llm-check 的差别只有一处，但这一处是硬约束：**只接受真实 HTTP 端点**。
// MOCK 端点在装配前被显式剔除，避免本体/Agent/工具/工作流的自动生成落到
// 回放式 MockProvider 上，产出看起来像真的、实际是回放的伪造内容
// （项目硬约束：严禁 mock 数据、严禁静默降级）。
//
// 没有任何可用 HTTP 端点时返回 nil：调用方据此 fail-closed，
// 生成端点会以 UNAVAILABLE 明确说明原因，而不是"生成成功"却写进假数据。
func buildGenerationGateway(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger,
	secrets *secretbox.Cipher) *llm.Gateway {
	loader := &llm.RegistryLoader{Pool: pool}
	bindings, excluded, err := loader.Load(ctx)
	if err != nil {
		logger.Error("生成网关：Model Registry 装载失败，自动生成将不可用", "error", err)
		return nil
	}

	kept := make([]llm.EndpointBinding, 0, len(bindings))
	for _, b := range bindings {
		if b.DeploymentType != "HTTP" {
			excluded = append(excluded, llm.Exclusion{
				ProviderID: b.ProviderID, EndpointID: b.EndpointID,
				EndpointName: b.EndpointName, ModelID: b.ModelID,
				Reason: "非 HTTP 端点（MOCK/回放）不参与自动生成，避免产出伪造内容",
			})
			continue
		}
		kept = append(kept, b)
	}

	ledger := llm.NewUsageLedger(pool)
	gw := llm.BuildGateway(kept, excluded, llm.GatewayOptions{
		Logger:  logger,
		Getenv:  os.Getenv,
		Secrets: secrets,
		Limiter: llm.NewLimiter(clock.RealClock{}),
		Ledger:  ledger,
		Budget:  &llm.BudgetGuard{Ledger: ledger, Limit: 5.0, ReserveFactor: llm.DefaultReserveFactor},
		Cache:   llm.NewSemanticCache(nil, nil, clock.RealClock{}),
		Kind:    llm.KindBatch,
	})
	if len(gw.Providers) == 0 {
		logger.Warn("生成网关：没有可用的真实 HTTP 模型端点，自动生成端点将返回 UNAVAILABLE",
			"excluded", len(gw.Unavailable))
		return nil
	}
	logger.Info("生成网关已装配", "providers", len(gw.Providers), "notes", len(gw.Unavailable))
	return gw
}
