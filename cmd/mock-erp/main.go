// cmd/mock-erp 是独立运行的 Mock ERP 服务入口（第七篇 §9.2/§10）。
//
// 它模拟远端 ERP 的业务数据、状态机、幂等、操作查询与故障注入，
// 与 ODAP 平台库物理分离：平台通过 erp.Port + Adapter 访问它，
// 绝不直接读写其内部存储（第七篇 §20 代码仓库边界）。
package main

import (
	"context"
	stderrors "errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"odap/internal/platform/config"
	"odap/internal/platform/logging"
	"odap/mockerp"
)

func main() {
	logger := logging.New("info")

	// 优先使用统一配置；Mock ERP 独立于平台库运行，缺失不相关配置时回退默认端口。
	addr := os.Getenv("MOCKERP_HTTP_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	if cfg, err := config.Load("mock-erp"); err == nil {
		logger = logging.New(cfg.LogLevel)
		if cfg.MockERPHTTPAddr != "" {
			addr = cfg.MockERPHTTPAddr
		}
	} else {
		logger.Warn("config.Load 失败，使用默认监听地址", "error", err, "addr", addr)
	}

	// 种子装载 fail-closed：env 指定了种子文件但缺失/非法时拒绝启动，
	// 绝不带空库静默对外服务。
	handler, err := mockerp.NewHandler(logger, os.Getenv("MOCKERP_SEED_FILE"))
	if err != nil {
		logger.Error("mock-erp 启动失败", "error", err)
		os.Exit(1)
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// SIGINT/SIGTERM → context 取消 → 优雅关闭。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("mock-erp 启动", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !stderrors.Is(err, http.ErrServerClosed) {
			logger.Error("mock-erp 监听失败", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("mock-erp 收到关闭信号，开始优雅关闭")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("优雅关闭失败", "error", err)
		os.Exit(1)
	}
	logger.Info("mock-erp 已停止")
}
