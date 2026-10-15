#!/usr/bin/env bash
# 12-graphstore.sh —— 图存储契约自检（docs/图数据库的选型.md、ADR-005）。
#
# 为什么把这一阶段独立出来：图存储（Fuseki/TDB2 或 PostgreSQL graph schema）能否
# 真正承载语义图数据，是本体验证、SHACL 校验与受控语义查询的前置条件。只跑
# `go test ./internal/ontology/graphstore/` 不够——那里的 HTTP 替身验证的是
# "适配器发了什么、怎么解析响应"，**不验证**生成的 SPARQL 在真实引擎上是否合法。
# （实际教训：适配器曾生成过 4 词项的图模式，替身测试全绿、真实引擎直接语法报错。）
#
# 三级降级，逐级说明用了哪一条：
#   L1 已配置且可达的图存储端点（GRAPHSTORE_MODE=fuseki + FUSEKI_ENDPOINT）
#   L2 用 compose 临时拉起 Fuseki（--profile graphstore），用完即回收
#   L3 RDFLib 提供真实 SPARQL 1.1 引擎语义的测试端点（tools/fake-sparql/）
# 三级都不满足 → SKIP（CI_STRICT=1 时视为失败）。
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

ci_stage "12 图存储契约自检"

CHECK_SCRIPT="tools/ci/graphstore-check.sh"
[ -f "$CHECK_SCRIPT" ] || ci_fail_exit "缺少 $CHECK_SCRIPT"

TMP_PORT="${GRAPHSTORE_CHECK_PORT:-18080}"
ENDPOINT="${FUSEKI_ENDPOINT:-http://127.0.0.1:${TMP_PORT}/odap}"

# probe_endpoint 返回 0 表示该端点的 SPARQL 查询端点可用。
probe_endpoint() {
  local ep="$1"
  local body
  body="$(curl -sS --noproxy '*' -X POST \
      -H 'Content-Type: application/sparql-query' \
      --data 'ASK {}' "${ep}/query" 2>/dev/null)" || return 1
  printf '%s' "$body" | grep -q '"boolean"'
}

TIER=""
if [ -n "${FUSEKI_ENDPOINT:-}" ] && probe_endpoint "${FUSEKI_ENDPOINT}"; then
  TIER="L1：使用已配置且可达的端点 ${FUSEKI_ENDPOINT}"
  ENDPOINT="$FUSEKI_ENDPOINT"
elif command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1 && [ -f .env ]; then
  ci_note "L2：尝试用 compose 临时拉起 Fuseki（--profile graphstore）…"
  if docker compose --profile graphstore up -d fuseki >/tmp/ci-graphstore-compose.log 2>&1; then
    cleanup_fuseki() { docker compose --profile graphstore stop fuseki >/dev/null 2>&1 || true; }
    trap cleanup_fuseki EXIT
    deadline=$(( $(date +%s) + 120 ))
    while [ "$(date +%s)" -lt "$deadline" ]; do
      if probe_endpoint "$ENDPOINT"; then TIER="L2：compose 临时 Fuseki（${ENDPOINT}）"; break; fi
      sleep 3
    done
  else
    ci_note "compose 未能拉起 Fuseki（镜像拉取受限？），转入 L3"
    tail -5 /tmp/ci-graphstore-compose.log | sed 's/^/      /' || true
  fi
fi

if [ -z "$TIER" ]; then
  # GRAPHSTORE_CHECK_PY 可显式指定带 rdflib 的解释器（CI 里由 workflow 安装；
  # 开发机上常是某个隔离 venv，而非 PATH 里的默认 python）。
  PY="${GRAPHSTORE_CHECK_PY:-}"
  if [ -z "$PY" ]; then
    for cand in $(ci_find_py 2>/dev/null || true); do
      if "$cand" -c 'import rdflib' >/dev/null 2>&1; then PY="$cand"; break; fi
    done
  fi
  if [ -n "$PY" ] && "$PY" -c 'import rdflib' >/dev/null 2>&1; then
    [ -f tools/fake-sparql/rdflib_endpoint.py ] || ci_fail_exit "缺少 tools/fake-sparql/rdflib_endpoint.py"
    "$PY" tools/fake-sparql/rdflib_endpoint.py --port "$TMP_PORT" >/tmp/ci-rdflib-endpoint.log 2>&1 &
    ENDPOINT_PID=$!
    cleanup_py() { kill "$ENDPOINT_PID" >/dev/null 2>&1 || true; }
    trap cleanup_py EXIT
    deadline=$(( $(date +%s) + 30 ))
    while [ "$(date +%s)" -lt "$deadline" ]; do
      # 子进程必须活着：否则"探测成功"可能来自端口上**别的**进程（例如残留的
      # 上一次自检端点），那样会把结论归因给一个根本没启动的引擎。
      if ! kill -0 "$ENDPOINT_PID" >/dev/null 2>&1; then
        ci_fail "RDFLib 端点启动即退出（端口 ${TMP_PORT} 可能被占用）："
        tail -20 /tmp/ci-rdflib-endpoint.log | sed 's/^/      /' || true
        exit 1
      fi
      if probe_endpoint "$ENDPOINT"; then break; fi
      sleep 1
    done
    if ! kill -0 "$ENDPOINT_PID" >/dev/null 2>&1; then
      ci_fail "RDFLib 端点已退出（端口 ${TMP_PORT} 可能被占用）："
      tail -20 /tmp/ci-rdflib-endpoint.log | sed 's/^/      /' || true
      exit 1
    fi
    if probe_endpoint "$ENDPOINT"; then
      TIER="L3：RDFLib 真实 SPARQL 1.1 引擎（测试端点，非 Fuseki）"
    else
      ci_fail "RDFLib 端点未能在 30s 内就绪："
      tail -20 /tmp/ci-rdflib-endpoint.log | sed 's/^/      /' || true
      exit 1
    fi
  fi
fi

if [ -z "$TIER" ]; then
  ci_skip "无可用图存储端点（未配置 FUSEKI_ENDPOINT、compose 不可用、且 python3+rdflib 缺失）"
fi

ci_note "$TIER"
ci_note "运行 graphstore-check（对真实 SPARQL 引擎做读/写/删/隔离/幂等断言）…"

if ! bash "$CHECK_SCRIPT" "$ENDPOINT"; then
  ci_fail_exit "图存储自检失败（适配器生成的 SPARQL 在真实引擎上不成立）"
fi

ci_ok
