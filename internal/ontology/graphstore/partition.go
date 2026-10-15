package graphstore

import (
	"context"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// triplesPartitionPrefix 是 graph.triples 按租户 LIST 分区的分区名前缀。
// 依据：第十三篇 §6.4（按 tenant 声明式分区）与迁移 000009（graph.triples ... PARTITION BY LIST (tenant_id)）。
const triplesPartitionPrefix = "graph.triples_t_"

// partitionSuffixPattern 约束分区名后缀必须为 32 位小写十六进制（即去掉连字符的 uuid）。
// ids.ID 是定长 16 字节数组，其规范化结果恒定满足该模式；此处再做一次显式校验，
// 用于拒绝任何非 ids.ID 来源的字符串进入 DDL 标识符，杜绝标识符注入。
var partitionSuffixPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// TenantPartitionName 返回租户对应的 graph.triples 分区全限定名：graph.triples_t_<uuid 去连字符>。
func TenantPartitionName(tenantID ids.ID) (string, error) {
	suffix, err := partitionSuffix(tenantID)
	if err != nil {
		return "", err
	}
	return triplesPartitionPrefix + suffix, nil
}

func partitionSuffix(tenantID ids.ID) (string, error) {
	if tenantID.IsZero() {
		return "", errors.E(errors.CodeValidationFailed, "tenant_id 不能为空")
	}
	hexForm := strings.ReplaceAll(tenantID.String(), "-", "")
	if !partitionSuffixPattern.MatchString(hexForm) {
		return "", errors.E(errors.CodeValidationFailed, "tenant_id 不是合法的 uuid")
	}
	return hexForm, nil
}

// TenantPartitionDDL 返回幂等的分区创建语句。
//
// 注意：PostgreSQL 的分区边界值 PARTITION FOR VALUES 必须是字面量，不允许使用参数占位符，
// 因此这里拼接的是由 ids.ID 规范化得到的 uuid 字符串；分区标识符已通过 partitionSuffixPattern 校验，
// 边界值来自 ids.ID.String()（8-4-4-4-12 形式），二者均无法承载注入载荷。
func TenantPartitionDDL(tenantID ids.ID) (string, error) {
	name, err := TenantPartitionName(tenantID)
	if err != nil {
		return "", err
	}
	return "CREATE TABLE IF NOT EXISTS " + name +
		" PARTITION OF graph.triples FOR VALUES IN ('" + tenantID.String() + "')", nil
}

// EnsureTenantPartition 幂等创建租户的 graph.triples 分区，是租户供给（tenant provisioning）流程的一部分。
//
// 与设计书的关系（第十三篇 §6.4）：
//   - graph.triples 为 PARTITION BY LIST (tenant_id) 的分区表；POS/OSP 二级索引在分区上自动派生本地索引，
//     索引维护总量与三索引方案一致（§6.3、§6.4）。
//   - 所有查询都从可信上下文注入 tenant_id，规划器据此做分区裁剪（partition pruning），
//     跨租户扫描在计划层即被排除；"删租户"演变为 DETACH PARTITION + 归档（§6.4）。
//   - 迁移 000009 不为未知租户预建分区（第二篇迁移规范），因此新租户初始化时必须显式建分区；
//     否则插入会报 "no partition of relation graph.triples found for row"。
//
// 该函数幂等（CREATE TABLE IF NOT EXISTS），可在租户初始化流程中重复调用。
func EnsureTenantPartition(ctx context.Context, pool *pgxpool.Pool, tenantID ids.ID) error {
	if pool == nil {
		return errors.E(errors.CodeInternal, "graphstore: 连接池不能为空")
	}
	ddl, err := TenantPartitionDDL(tenantID)
	if err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, ddl); err != nil {
		return errors.Wrap(err, errors.CodeInternal, "graphstore: 创建租户分区失败")
	}
	return nil
}

// PartitionExists 检查给定租户的分区是否已存在（pg_class 查询）。
// 供租户供给流程做前置校验，避免依赖写入失败才发现分区缺失。
func PartitionExists(ctx context.Context, pool *pgxpool.Pool, tenantID ids.ID) (bool, error) {
	if pool == nil {
		return false, errors.E(errors.CodeInternal, "graphstore: 连接池不能为空")
	}
	name, err := TenantPartitionName(tenantID)
	if err != nil {
		return false, err
	}
	var exists bool
	err = pool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace "+
			"WHERE n.nspname = 'graph' AND c.relname = $1 AND c.relispartition)",
		strings.TrimPrefix(name, "graph."),
	).Scan(&exists)
	if err != nil {
		return false, errors.Wrap(err, errors.CodeInternal, "graphstore: 查询分区是否存在失败")
	}
	return exists, nil
}
