#!/usr/bin/env bash
# run-gates.sh —— ODAP 交付门禁总入口（评测清单 §6「门禁盲区」的整改落地）。
#
# 与既有 contract-lint（契约一致性）互补：contract-lint 管「文档 vs 常量 vs DDL」，
# 本脚本管「代码**能不能真的跑通**」——即把"声明对了"与"系统可用"分开判。
#
# 逐条门禁与其对应的历史缺陷：
#   1) 路由可达性   —— Go 测试，逐条真实请求 routes() 里每个端点。
#                     防 L2：端点"已注册"却因前缀遮蔽恒定 404（声明表 ≠ 可达）。
#   2) 租户上下文   —— 静态。防 L1：SET LOCAL app.current_tenant_id = $1
#                     必然失败，且制造了第二份租户注入实现。
#   3) SQL 绑定     —— 静态。防"占位符个数与实参个数不符"（registry 回滚路径
#                     由本门禁发现：SQL 用 $4 却只绑 3 个实参 → 必然 500）。
#   4) 契约一致性   —— 复用既有 tools/contract-lint。它管两类"声明漂移"：
#                      (a) 文档 vs 常量 vs DDL（状态词表 / 错误码）；
#                      (b) 前端写方法 vs contracts/openapi.yaml ——
#                          `AsyncTask` 只能配声明了 202 的端点，反之亦然；
#                          且前端调用的写端点必须在 openapi 中登记（已知缺口须
#                          在 contract-lint 的 unregisteredWebEndpoints 显式登记理由）。
#                      这一条来自真实缺陷类：ontologies/validate、ontologies/publish、
#                      agents/publish、workflows/publish 四处**同时**把同步 200 端点
#                      声明成 202 任务引用，页面因此去轮询不存在的任务号。
#   5) 装配可达性   —— Go 测试（tests/unit）。防"写好的包没人调用"：
#                     完整实现 + 测试全绿的包可以没有任何生产调用方，编译、单测、
#                     覆盖率全都不显形，但对应能力在真实系统里根本没接线。
#                     本项目真实发生过：internal/ontology/{shacl,rdfs,semantic,cache}
#                     共 2500+ 行实现就绪，生产 import 数为 0，导致
#                     POST /ontologies/{id}/validate 长期恒返回 503。
#                     门禁从 cmd/ 做依赖可达性闭包，例外必须在
#                     tests/unit/wiring_test.go 的 danglingAllowlist 中登记理由。
#
# 用法：bash tools/gates/run-gates.sh [--no-go]
#   --no-go  跳过需要 Go 工具链的门禁（1 与 4），仅跑纯静态门禁。
#
# 解释器与工具链探测（勿退回"静默跳过"）：
#   本机与 CI 的解释器名不同（本机 python / CI 仅 python3）；Go 也可能已安装但
#   不在 PATH（Windows 下常见，如 /c/Users/<u>/go-sdk/go/bin/go.exe）。历史缺陷：
#   原脚本只做 `command -v go`，在本机恒为假 → 两条最关键的 Go 门禁被**静默跳过**，
#   却仍打印"门禁结果：通过 2 项"并 exit 0（假绿）。现改为显式探测 + 显式告警。
set -uo pipefail

PROJ="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$PROJ"

RUN_GO=1
[ "${1:-}" = "--no-go" ] && RUN_GO=0

fail=0
pass=0
skip=0

banner() { printf '\n=== %s ===\n' "$1"; }

# ── 解释器探测：python3 优先（CI/类 Unix），回退 python（本机 Windows Git Bash）──
PY=""
for cand in python3 python; do
  if command -v "$cand" >/dev/null 2>&1; then PY="$cand"; break; fi
done
if [ -z "$PY" ]; then
  echo "run-gates: 未找到 python3/python，无法运行静态门禁" >&2
  exit 2
fi

# ── Go 工具链探测：$GO > PATH > 常见安装路径（含 Windows go-sdk）──
GO_BIN=""
if [ -n "${GO:-}" ] && [ -x "${GO}" ]; then
  GO_BIN="${GO}"
elif command -v go >/dev/null 2>&1; then
  GO_BIN="go"
else
  for cand in \
    "$HOME/go-sdk/go/bin/go" \
    "$HOME/sdk/go/bin/go" \
    "/c/Users/${USERNAME:-}/go-sdk/go/bin/go.exe" \
    "/usr/local/go/bin/go"; do
    if [ -x "$cand" ]; then GO_BIN="$cand"; break; fi
  done
fi

banner "门禁 2/5：租户上下文注入"
if "$PY" tools/gates/tenant_context_gate.py .; then pass=$((pass + 1)); else fail=$((fail + 1)); fi

banner "门禁 3/5：SQL 绑定实参个数"
if "$PY" tools/gates/sql_argument_gate.py .; then pass=$((pass + 1)); else fail=$((fail + 1)); fi

if [ "$RUN_GO" = "1" ]; then
  if [ -n "$GO_BIN" ]; then
    banner "门禁 1/5：路由可达性（真实请求逐条探测）"
    if "$GO_BIN" test ./internal/api/ -run 'TestEveryDeclaredRouteIsReachable|TestAuthSubtreeSiblingsAreNotShadowed|TestPublicEndpointsStayPublic|TestProtectedRoutesRejectAnonymous'; then
      pass=$((pass + 1))
    else
      fail=$((fail + 1))
    fi

    banner "门禁 4/5：契约一致性（contract-lint）"
    if "$GO_BIN" run ./tools/contract-lint -root .; then pass=$((pass + 1)); else fail=$((fail + 1)); fi

    banner "门禁 5/5：装配可达性（已实现的包必须从 cmd/ 可达）"
    if "$GO_BIN" test ./tests/unit/ -run 'TestNoDanglingInternalPackages|TestDanglingAllowlistIsNotStale|TestWiringGateSeesKnownEdges' -v; then
      pass=$((pass + 1))
    else
      fail=$((fail + 1))
    fi
  else
    # 不静默：把"未跑"显式计入统计并标注，避免被误读为"通过"。
    skip=$((skip + 3))
    banner "⚠ 跳过门禁 1/5 / 4/5 / 5/5：未找到 Go 工具链"
    printf '  可用 GO=/path/to/go 指定，或用 bash tools/dev-run.sh 在容器内运行。\n'
    printf '  ★ 注意：本次结果不代表这两条门禁通过，仅供快速静态自查。\n'
  fi
fi

printf '\n────────────────────────────\n'
printf '门禁结果：通过 %d 项，失败 %d 项' "$pass" "$fail"
[ "$skip" -gt 0 ] && printf '，跳过 %d 项' "$skip"
printf '\n'
[ "$fail" -eq 0 ] || exit 1
# 有跳过时以非零码提示"未完整验证"，供 CI 与本地脚本区分"全绿"与"部分未验"。
if [ "$skip" -gt 0 ] && [ "$RUN_GO" = "1" ]; then exit 3; fi
