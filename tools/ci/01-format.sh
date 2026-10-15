#!/usr/bin/env bash
# 01-format.sh —— 格式检查与静态分析（§10.1 第 1 级 / §11.2 P0）。
#
# 三项：gofmt 未格式化清单、go vet、go build。任一非空/非零即失败。
# 这是最便宜的一级，失败必须阻断后续（§10.1"每一阶段应明确失败是否阻断后续步骤"）。
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

ci_stage "01 格式检查与静态分析"
ci_require_go

# ── gofmt ──
GO_DIR="$(dirname "$CI_GO")"
GOFMT="$GO_DIR/gofmt"
[ -x "$GOFMT" ] || GOFMT="$GO_DIR/gofmt.exe"
[ -x "$GOFMT" ] || GOFMT="gofmt"

unformatted="$("$GOFMT" -l cmd internal mockerp tools 2>&1)"
if [ -n "$unformatted" ]; then
  ci_fail "以下文件未通过 gofmt："
  printf '%s\n' "$unformatted" | sed 's/^/    /'
  exit 1
fi
ci_note "gofmt：全部已格式化"

# ── go vet ──
if ! "$CI_GO" vet ./... 2>&1; then
  ci_fail_exit "go vet 发现问题"
fi
ci_note "go vet：通过"

# ── go build ──
if ! "$CI_GO" build ./... 2>&1; then
  ci_fail_exit "go build 失败"
fi
ci_note "go build：通过"

ci_ok
