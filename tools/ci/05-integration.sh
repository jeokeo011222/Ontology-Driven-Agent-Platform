#!/usr/bin/env bash
# 05-integration.sh —— 模块集成测试（§10.1 第 5 级 / §10.2 集成测试）。
#
# 用真实 PostgreSQL 验证数据库事务、Outbox、操作账本、审批与回执处理。
# 覆盖两处：tests/integration/（跨模块）与 internal 内带 integration tag 的 PG 测试。
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

ci_stage "05 集成测试（真实 PostgreSQL）"
ci_require_go
ci_require_db

if ! "$CI_GO" test -tags=integration ./tests/integration/... -count=1 -timeout "${CI_TEST_TIMEOUT:-600s}" 2>&1; then
  ci_fail_exit "tests/integration 失败"
fi
ci_note "tests/integration：通过"

if ! "$CI_GO" test -tags=integration ./internal/... -count=1 -timeout "${CI_TEST_TIMEOUT:-600s}" 2>&1; then
  ci_fail_exit "internal 内 integration 测试失败"
fi
ci_note "internal（-tags=integration）：通过"

ci_ok
