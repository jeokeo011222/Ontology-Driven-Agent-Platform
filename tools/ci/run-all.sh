#!/usr/bin/env bash
# run-all.sh —— 本地一站式流水线（与 .github/workflows/ci.yml 的 job 序列同源）。
#
# 用法：
#   bash tools/ci/run-all.sh              # 宽松：缺环境记 SKIP，不阻断
#   bash tools/ci/run-all.sh --strict     # 严格：等价 CI_STRICT=1（发布前用）
#   bash tools/ci/run-all.sh --no-smoke   # 跳过 compose 冒烟（无 Docker 时）
#   bash tools/ci/run-all.sh --only 01,04 # 只跑指定阶段
#
# 退出码：0 全部通过（含 SKIP）；1 有阶段失败；3 无失败但有跳过（未完整验证）。
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

STRICT=0
RUN_SMOKE=1
ONLY=""
while [ $# -gt 0 ]; do
  case "$1" in
    --strict) STRICT=1 ;;
    --no-smoke) RUN_SMOKE=0 ;;
    --only) ONLY="${2:-}"; shift ;;
    *) echo "未知参数：$1" >&2; exit 2 ;;
  esac
  shift
done
export CI_STRICT="$STRICT"

STAGES=(
  "01-format.sh:格式与静态分析"
  "02-unit.sh:单元测试+前端类型"
  "03-migrate.sh:数据库迁移验证"
  "04-contract.sh:契约测试"
  "05-integration.sh:集成测试"
  "06-semantic.sh:本体语义回归"
  "07-e2e.sh:Mock ERP 端到端"
  "08-security.sh:安全测试"
  "09-fault-injection.sh:故障注入"
  "10-artifact.sh:不可变制品"
  "12-graphstore.sh:图存储契约自检"
)
[ "$RUN_SMOKE" = "1" ] && STAGES+=("11-smoke.sh:部署冒烟")

SUMMARY_DIR="artifacts/ci"
mkdir -p "$SUMMARY_DIR"
SUMMARY="$SUMMARY_DIR/summary.txt"
: >"$SUMMARY"

PASS=0; FAIL=0; SKIP=0
printf 'ODAP CI 本地流水线（CI_STRICT=%s）  %s\n' "$STRICT" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf '%-26s %-8s %s\n' "阶段" "结果" "耗时"
printf '%s\n' "------------------------------------------------------------"

for entry in "${STAGES[@]}"; do
  script="${entry%%:*}"; label="${entry#*:}"
  num="${script%%-*}"
  if [ -n "$ONLY" ] && ! printf '%s' ",$ONLY," | grep -q ",$num,"; then
    continue
  fi
  t0=$(date +%s)
  bash "tools/ci/$script" >"/tmp/ci-$script.log" 2>&1
  rc=$?
  dt=$(( $(date +%s) - t0 ))
  case "$rc" in
    0)   res="PASS"; col="\033[32m"; PASS=$((PASS + 1)) ;;
    3)   res="SKIP"; col="\033[33m"; SKIP=$((SKIP + 1)) ;;
    *)   res="FAIL"; col="\033[31m"; FAIL=$((FAIL + 1)) ;;
  esac
  printf "%-26s ${col}%-8s\033[0m %ds\n" "$label" "$res" "$dt"
  printf '%s|%s|%ds\n' "$label" "$res" "$dt" >>"$SUMMARY"
  if [ "$rc" != "0" ]; then
    # 失败与跳过都回显尾部日志，避免"看到结果却不知原因"
    sed 's/^/      /' "/tmp/ci-$script.log" | tail -12
  fi
done

printf '%s\n' "------------------------------------------------------------"
printf '通过 %d · 失败 %d · 跳过 %d\n' "$PASS" "$FAIL" "$SKIP"

[ "$FAIL" -eq 0 ] || exit 1
[ "$SKIP" -eq 0 ] || exit 3
exit 0
