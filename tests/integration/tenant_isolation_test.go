//go:build integration

// Package integration 承载跨模块集成测试（第十六篇 §10.2「集成测试」）：
// 使用真实 PostgreSQL 验证数据库事务、RLS 隔离与关键约束。
package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"odap/internal/api/store"
	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
)

// TestOntologyCategoryCrossTenantIsolation 验证 RLS 跨租户隔离真的生效：
// 租户 A 写入的数据对租户 B 不可见，反之亦然。
//
// 为什么这条断言不可省：L1 缺陷期间租户上下文写进了**另一个** GUC
// （app.current_tenant_id，而策略函数读的是 app.tenant_id），RLS 策略求值得到 NULL，
// 隔离**静默失效**——所有单元测试仍然全绿。只有"两个真实租户各自读一次并比对"
// 才能发现这类缺陷，因此它必须作为集成层的常驻回归。
func TestOntologyCategoryCrossTenantIsolation(t *testing.T) {
	appDSN := os.Getenv("ODAP_DATABASE_URL")
	if appDSN == "" {
		t.Skip("ODAP_DATABASE_URL 未设置，跳过 PG 集成测试")
	}
	ownerDSN := os.Getenv("ODAP_MIGRATE_URL")
	if ownerDSN == "" {
		ownerDSN = appDSN
	}

	ctx := context.Background()
	owner, err := pgxutil.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("连接 owner 库失败: %v", err)
	}
	defer owner.Close()
	app, err := pgxutil.Connect(ctx, appDSN)
	if err != nil {
		t.Fatalf("连接 app 库失败: %v", err)
	}
	defer app.Close()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000_000)
	tenantA, tenantB := ids.New(), ids.New()
	for i, tid := range []ids.ID{tenantA, tenantB} {
		if _, err := owner.Exec(ctx,
			`INSERT INTO iam.tenants (id, tenant_key, name, status) VALUES ($1,$2,$3,'ACTIVE')`,
			tid.String(), fmt.Sprintf("itest-%d-%s", i, suffix), "集成测试租户"); err != nil {
			t.Fatalf("创建测试租户失败: %v", err)
		}
	}
	t.Cleanup(func() {
		// 顺序：先删引用方（categories），再删被引用方（tenants）。
		_, _ = owner.Exec(ctx,
			`DELETE FROM ontology.categories WHERE tenant_id IN ($1,$2)`,
			tenantA.String(), tenantB.String())
		_, _ = owner.Exec(ctx,
			`DELETE FROM iam.tenants WHERE id IN ($1,$2)`,
			tenantA.String(), tenantB.String())
	})

	st := store.New(app)
	codeA := "IT-A-" + suffix
	codeB := "IT-B-" + suffix

	if _, err := st.CreateOntologyCategory(ctx, tenantA, ids.ID{}, codeA, "A 分类", "", 0); err != nil {
		t.Fatalf("租户A创建分类失败: %v", err)
	}
	if _, err := st.CreateOntologyCategory(ctx, tenantB, ids.ID{}, codeB, "B 分类", "", 0); err != nil {
		t.Fatalf("租户B创建分类失败: %v", err)
	}

	rowsA, err := st.ListOntologyCategories(ctx, tenantA)
	if err != nil {
		t.Fatalf("租户A查询分类失败: %v", err)
	}
	rowsB, err := st.ListOntologyCategories(ctx, tenantB)
	if err != nil {
		t.Fatalf("租户B查询分类失败: %v", err)
	}

	assertOnly := func(who string, rows []map[string]any, want, other string) {
		t.Helper()
		if len(rows) != 1 {
			t.Fatalf("%s 期望看到 1 条分类，实际 %d 条（%v）—— RLS 未按租户过滤",
				who, len(rows), categoryCodes(rows))
		}
		got, _ := rows[0]["category_code"].(string)
		if got == other {
			t.Fatalf("%s 看到了其他租户的数据 %q（跨租户数据泄露）", who, got)
		}
		if got != want {
			t.Fatalf("%s 看到 %q，期望 %q", who, got, want)
		}
	}
	assertOnly("租户A", rowsA, codeA, codeB)
	assertOnly("租户B", rowsB, codeB, codeA)
}

func categoryCodes(rows []map[string]any) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if s, ok := r["category_code"].(string); ok {
			out = append(out, s)
		}
	}
	return out
}
