#!/usr/bin/env bash
# 04-contract.sh —— API / Plugin / Event 契约测试（§10.1 第 4 级 / §10.2 契约测试）。
#
# 两层：
#   a) contract-lint：文档 vs Go 常量 vs DDL vs 前端标签 的逐项一致性（既有工具）；
#   b) tests/contract：契约文件本身的可解析性与规范性（OpenAPI / JSON Schema / 事件 Schema）。
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

ci_stage "04 契约测试"
ci_require_go

ci_note "contract-lint（契约一致性门禁）"
if ! "$CI_GO" run ./tools/contract-lint -root . 2>&1; then
  ci_fail_exit "contract-lint 失败"
fi
ci_ok "contract-lint"

ci_note "tests/contract 契约文件规范测试"
if ! "$CI_GO" test ./tests/contract/... -count=1 2>&1; then
  ci_fail_exit "tests/contract 失败"
fi
ci_ok

ci_ok "契约测试"
