#!/usr/bin/env bash
# 11-smoke.sh —— 部署到隔离环境并冒烟测试（§10.1 第 11 级）。
#
# 用 compose 起最小拓扑，断言 /healthz 与 /readyz 就绪，然后回收。
# 冒烟必须验证"就绪"而非仅"进程在跑"：readyz 会暴露迁移未完成/依赖未连上的状态。
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

ci_stage "11 部署冒烟测试"

command -v docker >/dev/null 2>&1 || ci_skip "未找到 docker，无法执行 compose 冒烟"
docker compose version >/dev/null 2>&1 || ci_skip "docker compose 不可用"
[ -f .env ] || ci_skip "缺少 .env（compose 需要 ODAP_DB_PASSWORD 等变量）"

API_ADDR="${ODAP_HTTP_ADDR:-127.0.0.1:8080}"
BASE="http://${API_ADDR}"

ci_note "启动最小拓扑（postgres/migrate/mockerp-db/migrate-mockerp/mockerp/api）…"
if ! docker compose up -d --build postgres migrate mockerp-db migrate-mockerp mockerp api 2>&1 | tail -20; then
  ci_fail_exit "compose 启动失败"
fi

cleanup() { docker compose down >/dev/null 2>&1 || true; }
trap cleanup EXIT

# 等待 readyz（迁移 + 依赖就绪），最多 120s
deadline=$(( $(date +%s) + 120 ))
ready=0
while [ "$(date +%s)" -lt "$deadline" ]; do
  if curl -fsS -o /dev/null "$BASE/readyz" 2>/dev/null; then ready=1; break; fi
  sleep 3
done

if [ "$ready" != "1" ]; then
  ci_fail "readyz 在 120s 内未就绪，最近日志："
  docker compose logs --tail 40 api || true
  exit 1
fi

for ep in /healthz /readyz; do
  code="$(curl -s -o /dev/null -w '%{http_code}' "$BASE$ep")"
  if [ "$code" != "200" ]; then
    ci_fail_exit "冒烟失败：$ep 返回 $code（期望 200）"
  fi
  ci_note "$ep → 200"
done

ci_ok
