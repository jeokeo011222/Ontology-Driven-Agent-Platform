#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# ODAP 重启脚本 = 停止（**保留数据卷**）+ 启动（同样的选项）
#
# 用法：
#   bash restart.sh                          # 停止后重新拉起（等价 down + up -d）
#   bash restart.sh --build                  # 先重建镜像再拉起（改过代码时用）
#   bash restart.sh --profile objectstore    # 带 profile 的服务一并重启
#   bash restart.sh --seed                   # 重启后导入演示数据（幂等）
#   bash restart.sh --reseed                 # 重启后重建演示数据（--replace）
#   bash restart.sh --no-wait                # 不等待就绪
#   bash restart.sh --timeout 300            # 就绪等待上限（秒）
#   bash restart.sh --logs                   # 就绪后跟随日志
#
# 为什么不是 `docker compose restart`：
#   该命令只是给**已存在的容器**发重启信号，不会重建容器、不会套用新的镜像、
#   不会套用改动后的 compose.yaml 或 .env。想让"改了代码 / 改了配置"生效，
#   必须重建容器 —— 也就是 down + up。本脚本做的就是后者。
#
# 为什么拒绝 --purge：重启是不该丢数据的动作。--purge 会删数据卷（不可恢复），
# 因此这里明确拒绝并要求显式走 `bash stop.sh --purge --yes`，避免"手滑重启"变清库。
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

export MSYS_NO_PATHCONV=1

# start.sh / stop.sh 都接受 --profile，两边都要拿到同一份，否则带 profile 时
# 停止阶段会漏掉那些服务（down 只处理当前启用的 profile）。
START_ARGS=()
STOP_ARGS=()

usage() { sed -n '3,20p' "$0" | sed 's/^# \{0,1\}//'; }

while [ $# -gt 0 ]; do
  case "$1" in
    --purge)
      printf '\n✗ 已拒绝执行：restart 不接受 --purge。\n' >&2
      printf '  --purge 会删除数据卷（平台库 / Mock ERP 库 / 对象存储），**不可恢复**。\n' >&2
      printf '  重启不应该丢数据。若确实要清库，请显式执行：\n' >&2
      printf '      bash stop.sh --purge --yes\n' >&2
      printf '  然后再： bash start.sh\n\n' >&2
      exit 1
      ;;
    --yes | -y)
      printf '\n! 提示：restart 不需要 --yes（它不执行任何破坏性操作），已忽略。\n' >&2
      ;;
    --profile)
      shift
      [ $# -gt 0 ] || { echo "✗ --profile 需要一个参数（objectstore | observability）" >&2; exit 1; }
      case "$1" in
        objectstore | observability) ;;
        *) echo "✗ 未知 profile：$1（可选：objectstore | observability）" >&2; exit 1 ;;
      esac
      START_ARGS+=(--profile "$1")
      STOP_ARGS+=(--profile "$1")
      ;;
    -h | --help) usage; exit 0 ;;
    --build | --seed | --reseed | --no-wait | --logs | --init-env)
      START_ARGS+=("$1")
      ;;
    --timeout)
      shift
      [ $# -gt 0 ] || { echo "✗ --timeout 需要一个秒数" >&2; exit 1; }
      START_ARGS+=(--timeout "$1")
      ;;
    *) echo "✗ 未知参数：$1（用 --help 查看用法）" >&2; exit 1 ;;
  esac
  shift
done

step() { printf '\n▸ %s\n' "$*"; }

step "第 1/2 步：停止（保留数据卷）"
bash "$ROOT/stop.sh" "${STOP_ARGS[@]+"${STOP_ARGS[@]}"}"

step "第 2/2 步：启动"
exec bash "$ROOT/start.sh" "${START_ARGS[@]+"${START_ARGS[@]}"}"
