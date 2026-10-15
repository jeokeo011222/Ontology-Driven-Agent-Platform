// Package config 从环境变量加载平台配置（第十六篇 §8.3 .env.example 对齐）。
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config 是进程级配置。
type Config struct {
	AppEnv  string // development | production
	Service string // api | worker | mock-erp | migrate

	// HTTP
	HTTPAddr string

	// 数据库
	ODAPDatabaseURL    string
	ODAPMigrateURL     string // 迁移专用 owner 账号（ODAP_MIGRATE_URL，缺省回退 ODAP_DATABASE_URL）
	MockERPDatabaseURL string

	// Mock ERP（api/worker 侧调用地址）
	MockERPBaseURL  string
	MockERPHTTPAddr string

	// 对象存储：local 模式用本地目录，s3 模式用 MinIO 端点
	ObjectStoreMode      string // local | s3
	ObjectStoreLocalDir  string
	ObjectStoreEndpoint  string
	ObjectStoreAccessKey string
	ObjectStoreSecretKey string
	ObjectStoreUseSSL    bool

	// 凭据加密主密钥（AES-256-GCM，32 字节 base64/hex）。
	// 留空表示未配置：界面「填 api-key」路径 fail-closed，绝不落明文。
	SecretMasterKey string

	// 观测
	LogLevel string

	// 迁移
	MigrationsDir string

	// Worker
	WorkerConcurrency int
	LeaseDuration     time.Duration
}

// Load 从环境变量读取配置并校验必填项。
func Load(service string) (*Config, error) {
	c := &Config{
		Service:              service,
		AppEnv:               getenv("APP_ENV", "development"),
		HTTPAddr:             getenv("ODAP_HTTP_ADDR", ":8080"),
		ODAPDatabaseURL:      os.Getenv("ODAP_DATABASE_URL"),
		ODAPMigrateURL:       os.Getenv("ODAP_MIGRATE_URL"),
		MockERPDatabaseURL:   os.Getenv("MOCKERP_DATABASE_URL"),
		MockERPBaseURL:       getenv("MOCK_ERP_BASE_URL", "http://127.0.0.1:8081"),
		MockERPHTTPAddr:      getenv("MOCKERP_HTTP_ADDR", ":8081"),
		ObjectStoreMode:      getenv("OBJECT_STORE_MODE", "local"),
		ObjectStoreLocalDir:  getenv("OBJECT_STORE_LOCAL_DIR", ".odap-objectstore"),
		ObjectStoreEndpoint:  getenv("OBJECT_STORE_ENDPOINT", ""),
		ObjectStoreAccessKey: os.Getenv("OBJECT_STORE_ACCESS_KEY"),
		ObjectStoreSecretKey: os.Getenv("OBJECT_STORE_SECRET_KEY"),
		SecretMasterKey:      os.Getenv("ODAP_SECRET_MASTER_KEY"),
		LogLevel:             getenv("LOG_LEVEL", "info"),
		MigrationsDir:        getenv("MIGRATIONS_DIR", "migrations/platform"),
		WorkerConcurrency:    getenvInt("WORKER_CONCURRENCY", 4),
		LeaseDuration:        getenvDuration("WORKER_LEASE_SECONDS", 30*time.Second),
	}
	// 平台库连接串并非所有服务都需要：mock-erp 与仅迁移 Mock ERP 库的场景
	// （mockerp-db）完全独立于平台库运行（第七篇 §20 仓库边界）。若对它们也强制
	// 要求 ODAP_DATABASE_URL，会导致 config.Load 整体失败、进而静默丢弃同批次的
	// LOG_LEVEL / MOCKERP_HTTP_ADDR 等有效配置，并打印误导性告警。
	needsPlatformDB := c.Service != "mock-erp" && c.Service != "migrate-mockerp"
	if needsPlatformDB && c.ODAPDatabaseURL == "" {
		return nil, fmt.Errorf("config: ODAP_DATABASE_URL is required")
	}
	if c.Service == "mock-erp" || c.Service == "migrate-mockerp" {
		if c.MockERPDatabaseURL == "" {
			return nil, fmt.Errorf("config: MOCKERP_DATABASE_URL is required for %s", c.Service)
		}
	}
	return c, nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getenvDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if secs, err := strconv.Atoi(v); err == nil {
			return time.Duration(secs) * time.Second
		}
	}
	return def
}
