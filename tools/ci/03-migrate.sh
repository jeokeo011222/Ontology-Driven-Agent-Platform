#!/usr/bin/env bash
# 03-migrate.sh —— 数据库迁移验证（§10.1 第 3 级 / §5.2 迁移编号）。
#
# 验证内容：
#   1) 平台库与 Mock ERP 库两条迁移链都能从零跑通（向前迁移、可重复 up）；
#   2) up 之后关键 schema 与登记表确实存在（防"迁移报成功但库是空的"静默降级）；
#   3) 迁移记录表条数与 migrations/platform/*.sql 文件数一致（防漏跑）。
#
# 前置：ODAP_DATABASE_URL（app 角色）/ ODAP_MIGRATE_URL（owner 角色）。
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

ci_stage "03 数据库迁移验证"
ci_require_go
ci_require_db

OWNER_URL="${ODAP_MIGRATE_URL:-$ODAP_DATABASE_URL}"
# ci_psql 通过该变量选择连接串（owner 具备读 information_schema/pg_class 的权限）
export ODAP_MIGRATE_URL="$OWNER_URL"

# ── 迁移文件本身的静态校验（放在执行之前：先诊断，再动手）──────────────
# 这两条对应一类真实缺陷：**编号复用 + 文件重命名**。
# cmd/migrate 按**文件名**判重（记录表主键是 version=文件名），于是：
#   · 同一编号下先后放入两个不同名的迁移 → 两者都会被执行（DDL 重复）；
#   · 迁移文件改名（000022_x.sql → 000026_x.sql）→ 新名被视为未应用 → 重复执行 → 建表冲突。
disk_dir="migrations/platform"
disk_names="$(ls -1 "$disk_dir"/*.sql 2>/dev/null | xargs -n1 basename | sort)"

dups="$(printf '%s\n' "$disk_names" | sed -E 's/^([0-9]+)_.*/\1/' | sort | uniq -d | tr '\n' ' ')"
if [ -n "$dups" ]; then
  ci_fail_exit "迁移编号重复：${dups}（同一编号下存在多个 .sql —— 编号必须唯一）"
fi
ci_note "迁移编号唯一性：通过（$(printf '%s\n' "$disk_names" | wc -l | tr -d ' ') 个文件）"

if [ -n "$ODAP_DATABASE_URL" ] || [ -n "${ODAP_MIGRATE_URL:-}" ]; then
  db_names="$(ci_psql "SELECT version FROM system.schema_migrations ORDER BY version" | sed '/^$/d' | sort)"
  if [ -n "$db_names" ]; then
    orphans="$(comm -23 <(printf '%s\n' "$db_names") <(printf '%s\n' "$disk_names"))"
    if [ -n "$orphans" ]; then
      ci_fail "迁移记录表存在磁盘上已不存在的文件名（迁移被重命名或删除）："
      printf '%s\n' "$orphans" | sed 's/^/      /'
      ci_fail "  → 该编号的 DDL 已执行过；若同一内容以新文件名再执行会建表冲突。"
      ci_fail "  → 处置：恢复原文件名，或删除该条记录后确认对应 DDL 幂等，切勿直接新加同名文件。"
      exit 1
    fi
    ci_note "迁移记录与磁盘文件一致（无重命名漂移）"
  fi
fi

ci_note "执行平台库迁移：go run ./cmd/migrate up"
if ! ODAP_MIGRATE_URL="$OWNER_URL" "$CI_GO" run ./cmd/migrate up 2>&1; then
  ci_fail_exit "平台库迁移失败（cmd/migrate up）"
fi
ci_ok "平台库迁移"

# ── 迁移条数 vs 文件数（防漏跑/半途中断）──
# 记录表实际位于 system schema（cmd/migrate 的落地命名），不是 public.schema_migrations。
want="$(ls -1 migrations/platform/*.sql 2>/dev/null | wc -l | tr -d ' ')"
got="$(ci_psql "SELECT count(*) FROM system.schema_migrations" | tr -d ' ')"
if [ -z "$got" ]; then
  ci_fail_exit "无法查询 system.schema_migrations（迁移记录表缺失或改名）"
elif [ "$want" != "$got" ]; then
  ci_fail_exit "迁移条数不符：磁盘 ${want} 个 .sql，库内记录 ${got} 条"
else
  ci_note "迁移条数核对：${got}/${want} 一致"
fi

# ── 关键 schema 存在性（§5.1 Schema 划分的**实际落地名**）──
# 注意：§5.1 表格里 approval / erp / evidence / reasoning / agent 等是"逻辑范围"，
# 该节已明确"描述的是同一逻辑范围的清单而非物理 schema 名，迁移脚本按分册命名落地"。
# 因此这里断言的是真实存在的物理 schema，另用下面的 required_tables 覆盖逻辑域，
# 避免写出一组永远不可能满足的 schema 名（那会让门禁变噪声）。
required_schemas="iam registry ontology graph execution runtime workflow integration audit llm planning evaluation release_mgmt simulation"
in_list="$(printf "'%s'," $required_schemas)"; in_list="${in_list%,}"
present_schemas="$(ci_psql "SELECT schema_name FROM information_schema.schemata WHERE schema_name IN (${in_list})")"
missing=""
for s in $required_schemas; do
  printf '%s\n' "$present_schemas" | grep -qx "$s" || missing="$missing $s"
done
if [ -n "$missing" ]; then
  ci_fail_exit "以下必需 schema 缺失：${missing}"
fi
ci_note "必需 schema 全部存在（${required_schemas// /, }）"

# ── 逻辑域落地表存在性（对应 §5.1 中命名与物理 schema 不同的四类）──
required_tables="workflow.approvals workflow.approval_actions integration.erp_operations integration.erp_receipts audit.evidence_records"
tbl_list="$(printf "'%s'," $required_tables)"; tbl_list="${tbl_list%,}"
present_tables="$(ci_psql "SELECT table_schema||'.'||table_name FROM information_schema.tables WHERE table_schema||'.'||table_name IN (${tbl_list})")"
missing_tbl=""
for t in $required_tables; do
  printf '%s\n' "$present_tables" | grep -qx "$t" || missing_tbl="$missing_tbl $t"
done
if [ -n "$missing_tbl" ]; then
  ci_fail_exit "以下关键表缺失：${missing_tbl}"
fi
ci_note "关键表全部存在（审批/ERP 操作与回执/证据）"

# ── RLS 已启用（跨租户隔离的物理前提）──
rls_cnt="$(ci_psql "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.relrowsecurity AND n.nspname NOT IN ('pg_catalog','information_schema')" | tr -d ' ')"
if [ -z "$rls_cnt" ] || [ "$rls_cnt" -lt 1 ] 2>/dev/null; then
  ci_fail_exit "未发现启用 RLS 的表（跨租户隔离缺失）"
fi
ci_note "已启用 RLS 的表：${rls_cnt} 张"

# ── Mock ERP 库迁移（可选：仅在提供 MOCKERP_DATABASE_URL 时）──
if [ -n "${MOCKERP_DATABASE_URL:-}" ]; then
  ci_note "执行 Mock ERP 库迁移"
  if ! ODAP_DATABASE_URL="$ODAP_DATABASE_URL" \
       "$CI_GO" run ./cmd/migrate up --dir mockerp/migrations --url "$MOCKERP_DATABASE_URL" 2>&1; then
    ci_fail_exit "Mock ERP 库迁移失败"
  fi
  ci_ok "Mock ERP 库迁移"
else
  ci_note "未提供 MOCKERP_DATABASE_URL，跳过 Mock ERP 库迁移"
fi

ci_ok
