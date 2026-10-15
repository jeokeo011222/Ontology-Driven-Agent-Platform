#!/usr/bin/env bash
# 09-fault-injection.sh —— 故障注入测试（§10.1 第 8 级 / §10.2 故障注入测试）。
#
# 重点验证：提交后响应丢失、重复消息、Worker 崩溃、权限撤销、版本冲突、证据服务故障。
# 对应 §10.3 硬门禁中的"因重试产生重复的非幂等业务操作"。
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

ci_stage "09 故障注入测试"
ci_require_go

if ! "$CI_GO" test -tags=faultinjection ./tests/fault-injection/... -count=1 -timeout "${CI_TEST_TIMEOUT:-600s}" 2>&1; then
  ci_fail_exit "tests/fault-injection 失败"
fi
ci_note "tests/fault-injection：通过"

# 执行域可靠性组件的既有测试（outbox/inbox/lease 的并发与幂等断言）。
if ! "$CI_GO" test ./internal/execution/... -count=1 2>&1; then
  ci_fail_exit "internal/execution 可靠性组件测试失败"
fi
ci_note "internal/execution：通过"

ci_ok
