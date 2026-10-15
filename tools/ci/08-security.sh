#!/usr/bin/env bash
# 08-security.sh —— 安全测试（§10.1 第 8 级 / §10.2 安全测试 / §10.3 硬门禁）。
#
# 覆盖：跨租户访问、对象越权、提示注入、工具绕过、审批摘要篡改、秘密泄露、危险网络访问。
#
# ★ 按 §10.1 明确要求：高风险安全测试**不应仅作为不影响发布的报告任务**，
#   因此本阶段在任何模式下都不得降级为"仅提示"——缺环境只能 SKIP（本地）或 FAIL（CI），
#   绝不允许"跑过就算过"。
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

ci_stage "08 安全测试"
ci_require_go

# 纯静态/单测类安全断言（无需 DB）：跨租户访问矩阵、越权拒绝、注入防护。
if ! "$CI_GO" test ./tests/security/... -count=1 -timeout "${CI_TEST_TIMEOUT:-300s}" 2>&1; then
  ci_fail_exit "tests/security 失败"
fi
ci_note "tests/security（静态+单测）：通过"

# 需要真实库的跨租户隔离断言（RLS 生效性）。
if [ -n "${ODAP_DATABASE_URL:-}" ]; then
  if ! "$CI_GO" test -tags=integration ./tests/security/... -count=1 2>&1; then
    ci_fail_exit "tests/security（-tags=integration，RLS 跨租户隔离）失败"
  fi
  ci_note "tests/security（跨租户 RLS）：通过"
else
  # 静态部分已通过，但跨租户 RLS 断言未执行 → 必须显式标记"未完整验证"，
  # 不允许因"静态过了"就对外表现为本阶段全绿。
  ci_skip "未提供 ODAP_DATABASE_URL：跨租户 RLS 安全断言未执行"
fi

ci_ok
