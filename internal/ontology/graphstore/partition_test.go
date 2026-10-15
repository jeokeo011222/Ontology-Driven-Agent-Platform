package graphstore

import (
	"strings"
	"testing"

	"odap/internal/platform/ids"
)

func TestTenantPartitionName(t *testing.T) {
	tests := []struct {
		name       string
		tenant     ids.ID
		wantSuffix string
		wantErr    bool
	}{
		{
			name:       "常规 uuid",
			tenant:     ids.MustParse("12345678-90ab-4cde-8f01-23456789abcd"),
			wantSuffix: "1234567890ab4cde8f0123456789abcd",
		},
		{
			name:       "全 f uuid",
			tenant:     ids.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff"),
			wantSuffix: "ffffffffffffffffffffffffffffffff",
		},
		{
			name:    "零值 uuid 拒绝",
			tenant:  ids.ID{},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := TenantPartitionName(tt.tenant)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("期望错误，实际得到 %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("非预期错误: %v", err)
			}
			want := triplesPartitionPrefix + tt.wantSuffix
			if got != want {
				t.Fatalf("分区名 = %q，期望 %q", got, want)
			}
			if !partitionSuffixPattern.MatchString(tt.wantSuffix) {
				t.Fatalf("分区后缀 %q 未通过合法性校验", tt.wantSuffix)
			}
		})
	}
}

func TestPartitionSuffixPatternRejectsInjection(t *testing.T) {
	bad := []string{
		"abc' OR 1=1 --",
		"../../etc/passwd",
		"ABCDEF1234567890ABCDEF1234567890", // 大写不合法（规范化后只会是小写）
		"short",
		"1234567890ab4cde8f0123456789abcd0", // 33 位
	}
	for _, s := range bad {
		if partitionSuffixPattern.MatchString(s) {
			t.Fatalf("危险后缀 %q 不应通过校验", s)
		}
	}
}

func TestTenantPartitionDDL(t *testing.T) {
	tenant := ids.MustParse("12345678-90ab-4cde-8f01-23456789abcd")
	ddl, err := TenantPartitionDDL(tenant)
	if err != nil {
		t.Fatalf("非预期错误: %v", err)
	}
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS",
		"graph.triples_t_1234567890ab4cde8f0123456789abcd",
		"PARTITION OF graph.triples",
		"FOR VALUES IN ('12345678-90ab-4cde-8f01-23456789abcd')",
	} {
		if !strings.Contains(ddl, want) {
			t.Fatalf("DDL 缺少片段 %q\n实际: %s", want, ddl)
		}
	}
}

func TestTenantPartitionDDLRejectsZeroTenant(t *testing.T) {
	if _, err := TenantPartitionDDL(ids.ID{}); err == nil {
		t.Fatal("零值 tenant 应返回错误")
	}
}
