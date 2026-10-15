#!/usr/bin/env bash
# 02-unit.sh —— Go 单元测试 + 前端类型检查（§10.1 第 2 级 / §10.2 单元测试）。
#
# Go 侧：go test ./...（含包内紧邻单测，按 §4.2 目录组织原则 4）。
# 前端侧：tsc --noEmit（TypeScript 严格类型检查，§9.3 前端工程约束第 1 条）。
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

ci_stage "02 Go 单元测试 + 前端类型检查"
ci_require_go

CI_TEST_TIMEOUT="${CI_TEST_TIMEOUT:-300s}"
if ! "$CI_GO" test ./... -count=1 -timeout "$CI_TEST_TIMEOUT" 2>&1; then
  ci_fail_exit "Go 单元测试失败"
fi
ci_ok "Go 单元测试"

# ── 前端类型检查 ──
if [ ! -f web/package.json ]; then
  ci_note "web/package.json 不存在，跳过前端类型检查"
else
  if [ ! -d web/node_modules ]; then
    if [ "${CI_INSTALL_WEB_DEPS:-0}" = "1" ]; then
      ci_note "安装前端依赖（npm ci）…"
      (cd web && npm ci) || ci_fail_exit "npm ci 失败"
    else
      ci_skip "web/node_modules 不存在（CI 中设 CI_INSTALL_WEB_DEPS=1 自动安装）"
    fi
  fi
  if ! (cd web && npm run typecheck) 2>&1; then
    ci_fail_exit "前端 tsc --noEmit 类型检查失败"
  fi
  ci_ok "前端类型检查"
fi

ci_ok
