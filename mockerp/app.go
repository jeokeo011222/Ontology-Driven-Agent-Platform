// Package mockerp 是 Mock ERP 服务的装配入口。
//
// 之所以需要这一层：Go 的 internal 可见性规则只允许 mockerp/ 子树内的包
// 导入 mockerp/internal/*，而进程入口位于 cmd/mock-erp。这里把内部分层
// （domain/service/repository/api）装配成一个可直接挂载的 http.Handler，
// 既保留清晰分层，又不让 cmd 直接依赖内部实现细节。
package mockerp

import (
	"log/slog"
	"net/http"

	"odap/mockerp/internal/api"
	"odap/mockerp/internal/repository"
	"odap/mockerp/internal/service"
)

// NewHandler 装配内存版 Mock ERP 的 HTTP 处理器（第七篇 §9.2/§10）。
// 内存存储优先；若切换 PostgreSQL，只需替换 repository 实现。
func NewHandler(logger *slog.Logger) http.Handler {
	return api.NewHandler(service.New(repository.NewMemory()), logger)
}
