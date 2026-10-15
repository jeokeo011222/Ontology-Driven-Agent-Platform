// cmd/migrate 是 ODAP 数据库迁移工具（第十六篇 §5）。
// 用法：
//
//	go run ./cmd/migrate up                      # 平台库（ODAP_DATABASE_URL）
//	go run ./cmd/migrate up --dir mockerp/migrations --url $MOCKERP_DATABASE_URL
//
// 规则（第二篇 §11.3）：已应用迁移不可修改；每次应用在一个事务内完成；
// 失败即退出（不留半应用状态）；重复执行幂等。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"odap/internal/platform/config"
	"odap/internal/platform/logging"
)

// appRoleName 是运行时应用数据库角色名（迁移 000011 创建）。
const appRoleName = "odap_app"

const createMigrationsTable = `
CREATE TABLE IF NOT EXISTS system.schema_migrations (
    version     TEXT PRIMARY KEY,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
)`

func main() {
	logger := logging.New("info")

	// 注意：标准 flag 包在首个非 flag 参数（子命令 "up"）处停止解析，
	// 因此这里直接对 os.Args[2:] 解析，保证 --dir/--url 在子命令之后仍生效。
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	var (
		dir = fs.String("dir", "", "migrations directory (default from config.MigrationsDir)")
		url = fs.String("url", "", "database URL (default ODAP_DATABASE_URL / MOCKERP_DATABASE_URL)")
	)
	if len(os.Args) < 2 || os.Args[1] != "up" {
		fmt.Fprintln(os.Stderr, "usage: migrate up [--dir DIR] [--url URL]")
		os.Exit(2)
	}
	_ = fs.Parse(os.Args[2:])

	cfg, err := config.Load("migrate")
	if err != nil {
		logger.Error("config load failed", "error", err)
		os.Exit(1)
	}
	dbURL := *url
	if dbURL == "" {
		if *dir != "" && strings.Contains(*dir, "mockerp") {
			dbURL = cfg.MockERPDatabaseURL
		} else if cfg.ODAPMigrateURL != "" {
			// 迁移需要 owner 权限（DDL/GRANT），与应用运行时账号分离
			dbURL = cfg.ODAPMigrateURL
		} else {
			dbURL = cfg.ODAPDatabaseURL
		}
	}
	migDir := *dir
	if migDir == "" {
		migDir = cfg.MigrationsDir
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		logger.Error("connect failed", "error", err)
		os.Exit(1)
	}
	defer conn.Close(ctx)

	// 把运行时应用角色密码注入会话 GUC，供迁移 000017 设置 odap_app 角色密码。
	// 单一真值来源：ODAP_DATABASE_URL（见 appRolePassword 的说明）。
	if pw := appRolePassword(cfg.ODAPDatabaseURL); pw != "" {
		if _, err := conn.Exec(ctx,
			"SELECT set_config('odap.app_password', $1, false)", pw,
		); err != nil {
			logger.Error("inject app role password failed", "error", err)
			os.Exit(1)
		}
	}

	if _, err := conn.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS system"); err != nil {
		logger.Error("create system schema failed", "error", err)
		os.Exit(1)
	}
	if _, err := conn.Exec(ctx, createMigrationsTable); err != nil {
		logger.Error("create schema_migrations failed", "error", err)
		os.Exit(1)
	}

	files, err := filepath.Glob(filepath.Join(migDir, "*.sql"))
	if err != nil {
		logger.Error("glob migrations failed", "error", err)
		os.Exit(1)
	}
	sort.Strings(files)
	if len(files) == 0 {
		logger.Error("no migration files found", "dir", migDir)
		os.Exit(1)
	}

	applied, skipped := 0, 0
	for _, f := range files {
		version := filepath.Base(f)
		sqlBytes, err := os.ReadFile(f)
		if err != nil {
			logger.Error("read migration failed", "file", version, "error", err)
			os.Exit(1)
		}

		var exists bool
		if err := conn.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM system.schema_migrations WHERE version = $1)",
			version,
		).Scan(&exists); err != nil {
			logger.Error("migration state check failed", "file", version, "error", err)
			os.Exit(1)
		}
		if exists {
			skipped++
			continue
		}

		// 每个迁移一个事务：失败即整体回滚，退出码非 0（第十六篇 §8.2 migrate-mockerp 语义）
		tx, err := conn.Begin(ctx)
		if err != nil {
			logger.Error("begin failed", "file", version, "error", err)
			os.Exit(1)
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback(ctx)
			logger.Error("migration FAILED (rolled back)", "file", version, "error", err)
			os.Exit(1)
		}
		if _, err := tx.Exec(ctx,
			"INSERT INTO system.schema_migrations (version) VALUES ($1)", version,
		); err != nil {
			_ = tx.Rollback(ctx)
			logger.Error("record migration failed", "file", version, "error", err)
			os.Exit(1)
		}
		if err := tx.Commit(ctx); err != nil {
			logger.Error("commit failed", "file", version, "error", err)
			os.Exit(1)
		}
		applied++
		logger.Info("migration applied", "file", version)
	}

	logger.Info("migrate up complete", "applied", applied, "skipped", skipped, "dir", migDir)
}

// appRolePassword 从运行时连接串解析应用角色（odap_app）的密码。
//
// 为什么不新增独立环境变量：运行时连接串 ODAP_DATABASE_URL 已经携带该密码，
// 若再引入 ODAP_APP_DB_PASSWORD，两处密码就可能漂移，导致“迁移设置了一个密码、
// 应用却用另一个密码连接”的一致性缺陷。这里让连接串成为唯一真值来源。
//
// 返回空串的两种情况（迁移 000017 将回退到本地开发默认值）：
//   - 连接串不可解析；
//   - 连接串用户不是 odap_app（例如按设计书 §8.3 原文以 owner 账号 odap 连接），
//     此时不应误改应用角色密码。
func appRolePassword(dbURL string) string {
	c, err := pgconn.ParseConfig(dbURL)
	if err != nil {
		return ""
	}
	if c.User != appRoleName {
		return ""
	}
	return c.Password
}
