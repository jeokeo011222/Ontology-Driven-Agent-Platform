#!/usr/bin/env bash
# lib.sh —— ODAP CI 分阶段脚本的公共库（第十六篇 §10.1 流水线 / §10.2 测试分层）。
#
# 设计原则（与"门禁要么精确、要么不做"同源）：
#   1) **本地与 CI 同源**：CI YAML 只负责编排与 needs 阻断，实际命令全在本目录脚本里，
#      因此"本地跑得通"就等于"CI 跑得通"，杜绝把无法本地验证的命令塞进 YAML。
#   2) **缺环境不静默**：任何阶段在缺少前置条件时，要么硬失败，要么打印醒目的
#      SKIPPED 并把原因写进 $GITHUB_STEP_SUMMARY / 标准输出——绝不允许"没跑却看着像通过"。
#   3) **严格模式**：CI_STRICT=1 时，缺前置条件 = 失败（§10.3 发布硬门禁）。
#      本地默认 CI_STRICT=0，缺 DB 时跳过，便于无 Docker 环境开发。
#
# 用法（各阶段脚本顶部）：
#   set -uo pipefail
#   source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
#   ci_stage "01 格式检查与静态分析"
#   ...
#   ci_done          # 打印并退出；EXIT_SKIP(3) 与非零失败由调用方在阶段内决定

# shellcheck disable=SC2034
CI_LIB_VERSION="1"

# ── .env 加载：仅在变量未设置时填充（已导出的环境变量优先）──
# 目的：本地直接 `bash tools/ci/03-migrate.sh` 即可跑，无需手工 export；
# 同时绝不覆盖 CI 注入的值（CI 里不放置 .env 或放置后仍以 secret 为准）。
ci_load_env() {
  [ -f .env ] || return 0
  local line key val
  while IFS= read -r line; do
    case "$line" in ''|\#*) continue ;; esac
    case "$line" in *=*) ;; *) continue ;; esac
    key="${line%%=*}"
    val="${line#*=}"
    # 合法 shell 变量名才接受
    case "$key" in *[!A-Za-z0-9_]*) continue ;; esac
    eval "local cur=\${$key:-}"
    [ -n "$cur" ] || export "$key=$val"
  done <.env
}

ci_init() {
  CI_PROJ="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
  cd "$CI_PROJ" || exit 2
  export CI_PROJ

  CI_STRICT="${CI_STRICT:-0}"
  # 容器/CI 环境默认关闭代理干扰，并锁定国内镜像以稳定拉依赖（可在外部覆盖）。
  export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
  export GOFLAGS="${GOFLAGS:--mod=mod}"
  export no_proxy="${no_proxy:-127.0.0.1,localhost}"

  ci_load_env

  # 连接串主机名归一（见 ci_normalize_db_url 注释）。
  local v
  for v in ODAP_DATABASE_URL ODAP_MIGRATE_URL MOCKERP_DATABASE_URL; do
    eval "local cur=\${$v:-}"
    [ -n "$cur" ] || continue
    local fixed
    fixed="$(ci_normalize_db_url "$cur")"
    [ "$fixed" = "$cur" ] || export "$v=$fixed"
  done
}

# ci_normalize_db_url —— 把面向容器的主机名改写成宿主机可达的回环地址。
#
# 为什么需要：.env 是为**容器内**服务编写的，主机名是 compose 服务名
# （postgres / mockerp-db）。CI 脚本在宿主机执行 go 命令时这些名字不可解析，
# 会得到 "lookup postgres: no such host"——迁移/集成/E2E 阶段将全部无法运行。
# 端口映射以 compose.yaml 为准：postgres 127.0.0.1:5432、mockerp-db 127.0.0.1:5433。
# 已是回环/外部地址的连接串原样返回，因此 CI（service container 用 localhost）不受影响。
ci_normalize_db_url() {
  local u="$1" mp="${MOCKERP_DB_HOST_PORT:-5433}"
  case "$u" in
    *@postgres:*|*@mockerp-db:*)
      # 必须连同端口一起替换：只替换 "@postgres:" 会把原端口拼到新主机名后面
      # （@postgres:5432/odap → @127.0.0.1:54325432/odap，库名被解析成 "5432/odap"）。
      printf '%s' "$u" | sed -E \
        -e 's#@postgres:([0-9]+)#@127.0.0.1:\1#' \
        -e "s#@mockerp-db:[0-9]+#@127.0.0.1:${mp}#"
      ;;
    *) printf '%s' "$u" ;;
  esac
}

# ci_psql "SQL" —— 执行只读 SQL 并回显结果（多行时逐行输出）。
#
# 优先本机 psql；本机无 psql（Windows 常见）时用**一次性容器客户端**连接。
#
# 反面教训（勿退回）：早期版本在本机无 psql 时回退到 `docker compose exec postgres`，
# 那条路径**忽略传入的连接串**，永远查 compose 里那一个库。后果是"检查 A 库却读了
# B 库"——用一次性临时库验证迁移时，它报出的却是共享库里的记录，把一次正确的
# 验证结论污染成了假失败。
ci_psql() {
  local sql="$1"
  local url="${ODAP_MIGRATE_URL:-${ODAP_DATABASE_URL:-}}"
  if command -v psql >/dev/null 2>&1; then
    psql "$url" -tAc "$sql" 2>/dev/null
    return $?
  fi
  command -v docker >/dev/null 2>&1 || return 1
  # 容器内访问宿主机回环地址需改走 host.docker.internal
  local durl="$url"
  case "$durl" in
    *127.0.0.1*) durl="${durl//127.0.0.1/host.docker.internal}" ;;
    *localhost*) durl="${durl//localhost/host.docker.internal}" ;;
  esac
  docker run --rm --add-host=host.docker.internal:host-gateway \
    postgres:16 psql "$durl" -tAc "$sql" 2>/dev/null
}

# ci_stage "标题" —— 阶段分隔与计时起点
ci_stage() {
  CI_STAGE_NAME="$1"
  CI_STAGE_T0="$(date +%s)"
  printf '\n\033[1m=== %s ===\033[0m\n' "$1"
}

# ci_note "..." —— 普通说明
ci_note() { printf '  %s\n' "$*"; }

# ci_ok "..." —— 阶段成功
ci_ok() {
  local dt=$(( $(date +%s) - CI_STAGE_T0 ))
  printf '\033[32m✔ %s（%ds）\033[0m\n' "${1:-$CI_STAGE_NAME}" "$dt"
}

# ci_fail "..." —— 阶段失败（调用方负责 exit，除非用 ci_fail_exit）
ci_fail() { printf '\033[31m✘ %s\033[0m\n' "$*"; }

ci_fail_exit() {
  ci_fail "$1"
  exit 1
}

# ci_skip "原因" —— 缺前置条件。严格模式下失败，否则标记跳过（退出码 3）。
ci_skip() {
  local reason="$1"
  if [ "$CI_STRICT" = "1" ]; then
    ci_fail "缺少前置条件，且 CI_STRICT=1（硬阻断）：${reason}"
    exit 1
  fi
  printf '\033[33m⚠ SKIPPED：%s\033[0m\n' "$reason"
  printf '  （本地宽松模式；CI 会以 CI_STRICT=1 运行并使本阶段硬失败）\n'
  ci_summary_note "⚠ SKIPPED $(basename "$0")：${reason}"
  exit 3
}

# ── 工具链探测 ────────────────────────────────────────────────────────
ci_find_go() {
  if [ -n "${GO:-}" ] && [ -x "${GO}" ]; then echo "${GO}"; return 0; fi
  if command -v go >/dev/null 2>&1; then echo "go"; return 0; fi
  local cand
  for cand in \
    "$HOME/go-sdk/go/bin/go" \
    "$HOME/sdk/go/bin/go" \
    "/c/Users/${USERNAME:-}/go-sdk/go/bin/go.exe" \
    "/usr/local/go/bin/go"; do
    [ -x "$cand" ] && { echo "$cand"; return 0; }
  done
  return 1
}

ci_find_py() {
  local cand
  for cand in python3 python; do
    command -v "$cand" >/dev/null 2>&1 && { echo "$cand"; return 0; }
  done
  return 1
}

# ci_require_go —— 无 Go 工具链则按严格模式处置
ci_require_go() {
  CI_GO="$(ci_find_go)" || ci_skip "未找到 Go 工具链（可用 GO=/path/to/go 指定）"
  export CI_GO
}

# ci_require_env VAR [说明] —— 环境变量缺失即跳过/失败
ci_require_env() {
  local var="$1" desc="${2:-$1 未设置}"
  if [ -z "${!var:-}" ]; then ci_skip "$desc"; fi
}

# ci_require_db —— 数据库连接串就绪检查（仅检查变量；连通性由阶段内命令自然暴露）
ci_require_db() {
  ci_require_env ODAP_DATABASE_URL "ODAP_DATABASE_URL 未设置（集成/迁移/E2E 阶段需要 PostgreSQL）"
}

# ── CI 摘要（GitHub Actions step summary 或本地 echo）───────────────
ci_summary_note() {
  local line="$1"
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    printf '%s\n' "$line" >>"$GITHUB_STEP_SUMMARY"
  fi
  printf '  · %s\n' "$line"
}

ci_init
