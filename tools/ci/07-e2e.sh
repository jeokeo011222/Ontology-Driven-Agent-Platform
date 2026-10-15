#!/usr/bin/env bash
# 07-e2e.sh —— Mock ERP 端到端测试（§10.1 第 7 级 / §10.2 端到端测试 / §11.6 P4 验收）。
#
# 从 API 创建业务任务开始，验证整条工作流直至业务终态、审计记录与证据包。
# 需要已启动的 api + mockerp（compose 或本地进程），地址经 ODAP_E2E_BASE_URL 提供。
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

ci_stage "07 Mock ERP 端到端测试"
ci_require_go
ci_require_db
ci_require_env ODAP_E2E_BASE_URL "ODAP_E2E_BASE_URL 未设置（E2E 需要已启动的 API 服务）"

if ! NO_PROXY=127.0.0.1,localhost no_proxy=127.0.0.1,localhost \
     "$CI_GO" test -tags=e2e ./tests/e2e/... -count=1 -timeout "${CI_TEST_TIMEOUT:-600s}" 2>&1; then
  ci_fail_exit "tests/e2e 失败"
fi
ci_ok
