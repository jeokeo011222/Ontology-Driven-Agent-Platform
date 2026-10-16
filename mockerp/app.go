// Package mockerp 是 Mock ERP 服务的装配入口。
//
// 之所以需要这一层：Go 的 internal 可见性规则只允许 mockerp/ 子树内的包
// 导入 mockerp/internal/*，而进程入口位于 cmd/mock-erp。这里把内部分层
// （domain/service/repository/api）装配成一个可直接挂载的 http.Handler，
// 既保留清晰分层，又不让 cmd 直接依赖内部实现细节。
package mockerp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"odap/mockerp/internal/api"
	"odap/mockerp/internal/domain"
	"odap/mockerp/internal/repository"
	"odap/mockerp/internal/service"
)

// NewHandler 装配内存版 Mock ERP 的 HTTP 处理器（第七篇 §9.2/§10）。
// 内存存储优先；若切换 PostgreSQL，只需替换 repository 实现。
//
// seedPath 非空时于启动时装载种子对象（JSON 数组，元素为 domain.Object 形状），
// 让演示 ERP 每次启动都携带真实的业务数据（重启安全、声明式）。种子文件缺失、
// 非法或对象冲突一律报错 fail-closed——演示 ERP 绝不带空库或半截数据静默启动；
// seedPath 为空表示不装载（单测/本地自担，启动即为空库）。
func NewHandler(logger *slog.Logger, seedPath string) (http.Handler, error) {
	repo := repository.NewMemory()
	if seedPath != "" {
		data, err := os.ReadFile(seedPath)
		if err != nil {
			return nil, fmt.Errorf("mock-erp 种子文件读取失败: %w", err)
		}
		var objects []domain.Object
		if err := json.Unmarshal(data, &objects); err != nil {
			return nil, fmt.Errorf("mock-erp 种子文件不是合法的对象数组（%s）: %w", seedPath, err)
		}
		if len(objects) == 0 {
			return nil, fmt.Errorf("mock-erp 种子文件为空（%s）", seedPath)
		}
		for i := range objects {
			obj := &objects[i]
			if obj.ObjectType == "" || obj.ObjectID == "" {
				return nil, fmt.Errorf("mock-erp 种子第 %d 项缺少 object_type/object_id", i+1)
			}
			if obj.Version < 1 {
				obj.Version = 1
			}
			if err := repo.CreateObject(context.Background(), obj); err != nil {
				return nil, fmt.Errorf("mock-erp 种子第 %d 项（%s/%s）装载失败: %w",
					i+1, obj.ObjectType, obj.ObjectID, err)
			}
		}
		logger.Info("mock-erp 种子装载完成", "file", seedPath, "objects", len(objects))
	}
	return api.NewHandler(service.New(repo), logger), nil
}
