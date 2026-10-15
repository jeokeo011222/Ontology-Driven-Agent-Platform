package graphstore

// 本文件是图存储后端的**唯一装配入口**：从配置到 GraphStore 实例的映射集中在此，
// 避免 api / worker / seed / CLI 各自拼装一份（历史上"第二份实现"曾导致 RLS 隔离
// 静默失效，此处不重复同类错误）。

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/platform/errors"
)

// Backend 是图存储后端标识。
type Backend string

const (
	// BackendPostgres 使用 PostgreSQL 的 graph schema（第十三篇 §5.3 的默认方案）。
	BackendPostgres Backend = "postgres"
	// BackendFuseki 使用 Apache Jena Fuseki + TDB2（docs/图数据库的选型.md、ADR-005）。
	BackendFuseki Backend = "fuseki"
)

// 环境变量名。集中在此定义，保证"唯一真值来源"：任何进程要读图存储配置，
// 都调用 ConfigFromEnv，而不是各写一份 os.Getenv("FUSEKI_..."）。
const (
	EnvGraphStoreMode     = "GRAPHSTORE_MODE"
	EnvFusekiEndpoint     = "FUSEKI_ENDPOINT"
	EnvFusekiUsername     = "FUSEKI_USERNAME"
	EnvFusekiPassword     = "FUSEKI_PASSWORD"
	EnvFusekiTimeoutSecs  = "FUSEKI_TIMEOUT_SECONDS"
	defaultFusekiTimeoutS = 30
)

// ParseBackend 解析后端标识；空串回退到 postgres（保持向后兼容：未配置即旧行为）。
//
// 无法识别的取值一律报错，**不做静默回退**——把 fuskei 拼错却悄悄跑在 PostgreSQL 上，
// 会让"已启用图数据库"成为一句无法核实的声明。
func ParseBackend(raw string) (Backend, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return BackendPostgres, nil
	case string(BackendPostgres):
		return BackendPostgres, nil
	case string(BackendFuseki):
		return BackendFuseki, nil
	default:
		return "", errors.Ef(errors.CodeValidationFailed,
			"%s 取值非法：%q（可选 %s | %s）", EnvGraphStoreMode, raw, BackendPostgres, BackendFuseki)
	}
}

// OpenConfig 是装配 GraphStore 所需的全部输入。
type OpenConfig struct {
	Backend Backend
	// Pool 在 BackendPostgres 时必填。
	Pool *pgxpool.Pool
	// Fuseki 在 BackendFuseki 时必填。
	Fuseki FusekiConfig
}

// Open 按配置构造 GraphStore。
func Open(cfg OpenConfig) (GraphStore, error) {
	backend, err := ParseBackend(string(cfg.Backend))
	if err != nil {
		return nil, err
	}
	switch backend {
	case BackendPostgres:
		if cfg.Pool == nil {
			return nil, errors.E(errors.CodeValidationFailed,
				"graphstore: postgres 后端需要已建立的连接池")
		}
		return &PGStore{Pool: cfg.Pool}, nil
	case BackendFuseki:
		return NewFusekiStore(cfg.Fuseki)
	default:
		// ParseBackend 已保证不可达；保留分支以防新增枚举时漏改。
		return nil, errors.Ef(errors.CodeValidationFailed, "graphstore: 未实现的后端 %q", backend)
	}
}

// ConfigFromEnv 从环境变量解析图存储配置。
//
// getenv 由调用方注入（通常传 os.Getenv），便于测试与"冻结配置"的受控装配。
func ConfigFromEnv(getenv func(string) string) (OpenConfig, error) {
	backend, err := ParseBackend(getenv(EnvGraphStoreMode))
	if err != nil {
		return OpenConfig{}, err
	}
	cfg := OpenConfig{Backend: backend}
	if backend != BackendFuseki {
		return cfg, nil
	}
	timeout := time.Duration(defaultFusekiTimeoutS) * time.Second
	if raw := strings.TrimSpace(getenv(EnvFusekiTimeoutSecs)); raw != "" {
		secs, err := strconv.Atoi(raw)
		if err != nil || secs <= 0 {
			return OpenConfig{}, errors.Ef(errors.CodeValidationFailed,
				"%s 必须是正整数秒数，得到 %q", EnvFusekiTimeoutSecs, raw)
		}
		timeout = time.Duration(secs) * time.Second
	}
	cfg.Fuseki = FusekiConfig{
		Endpoint: getenv(EnvFusekiEndpoint),
		Username: getenv(EnvFusekiUsername),
		Password: getenv(EnvFusekiPassword),
		Timeout:  timeout,
	}
	if err := cfg.Fuseki.Validate(); err != nil {
		return OpenConfig{}, err
	}
	return cfg, nil
}

// EnvConfigFromOS 是 ConfigFromEnv(os.Getenv) 的便捷形式。
func EnvConfigFromOS() (OpenConfig, error) { return ConfigFromEnv(os.Getenv) }

// Describe 返回可安全打印的配置摘要（**不含凭据**）。
func (c OpenConfig) Describe() string {
	if c.Backend != BackendFuseki {
		return fmt.Sprintf("backend=%s", BackendPostgres)
	}
	return fmt.Sprintf("backend=%s endpoint=%s auth=%t",
		BackendFuseki, strings.TrimRight(c.Fuseki.Endpoint, "/"), c.Fuseki.Username != "")
}
