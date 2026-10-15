#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# ODAP 停止脚本（`docker compose down` 的封装）
#
# 用法：
#   bash stop.sh                            # 停止并删除容器/网络，**保留数据卷**
#   bash stop.sh --profile objectstore      # 连启用过的 profile 服务一起停
#   bash stop.sh --purge --yes              # 连数据卷一起删除（不可逆！见下）
#
# 默认只停止，不删数据：数据卷（odap-postgres / mockerp-postgres / odap-objectstore）
# 会被保留，`bash start.sh` 再次拉起后数据仍在。
#
# --purge 会删除上面三个卷，包括已导入的演示数据与全部 Run / 审计记录，**不可恢复**。
# 因此它要求**同时**给出 --yes；只给 --purge 会被拒绝（fail-closed，见末尾守卫）。
#
# 与 start.sh 的差异（有意为之）：本脚本在 .env 缺失时**不阻塞**，只警告。
# 停止是"脱困"操作——不能因为配置文件丢了就关不掉已经在跑的容器。
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

export MSYS_NO_PATHCONV=1

PROFILES=()
PURGE=0
ASSUME_YES=0

usage() { sed -n '3,16p' "$0" | sed 's/^# \{0,1\}//'; }

while [ $# -gt 0 ]; do
  case "$1" in
    --purge) PURGE=1 ;;
    --yes | -y) ASSUME_YES=1 ;;
    --profile)
      shift
      [ $# -gt 0 ] || { echo "✗ --profile 需要一个参数（objectstore | observability）" >&2; exit 1; }
      case "$1" in
        objectstore | observability) PROFILES+=("$1") ;;
        *) echo "✗ 未知 profile：$1（可选：objectstore | observability）" >&2; exit 1 ;;
      esac
      ;;
    -h | --help) usage; exit 0 ;;
    *) echo "✗ 未知参数：$1（用 --help 查看用法）" >&2; exit 1 ;;
  esac
  shift
done

step() { printf '\n▸ %s\n' "$*"; }
ok() { printf '  ✓ %s\n' "$*"; }
warn() { printf '  ! %s\n' "$*" >&2; }
die() {
  printf '\n✗ %s\n' "$*" >&2
  exit 1
}

step "前置检查"
command -v docker > /dev/null 2>&1 || die "未找到 docker 命令。"
docker info > /dev/null 2>&1 ||
  die "Docker 守护进程未运行（容器可能已经停了，但无法确认；请启动 Docker Desktop）。"
ok "docker 可用"

if [ ! -f .env ]; then
  # 不阻塞：compose 读不到 .env 时 ${VAR:?} 会失败，但 down 通常仍然可行。
  warn "缺少 .env —— compose 变量插值可能失败；停止操作仍会尝试执行。"
fi

COMPOSE=(docker compose)
for p in "${PROFILES[@]+"${PROFILES[@]}"}"; do COMPOSE+=(--profile "$p"); done

# ── 危险操作守卫：删除数据卷必须显式确认 ────────────────────────────────────
# 只匹配 compose 声明的三个数据卷（项目名来自 compose.yaml 的 `name: odap`，
# 故实际名为 odap_odap-postgres / odap_mockerp-postgres / odap_odap-objectstore）。
# 有意**不匹配** odap-gocache / odap-gobuild —— 那是 dev-run.sh 与 Makefile 手工
# 创建的 Go 构建缓存，不属于项目数据，删掉只会白白付一次重建成本。
OAP_DATA_VOLUMES=()
while IFS= read -r v; do
  [ -n "$v" ] && OAP_DATA_VOLUMES+=("$v")
done < <(docker volume ls --format '{{.Name}}' 2> /dev/null | grep -E '(^|_)(odap|mockerp)-(postgres|objectstore)$' || true)

if [ "$PURGE" = "1" ]; then
  printf '\n'
  printf '═%.0s' {1..74}
  printf '\n'
  printf '⚠️  此操作非常危险，可能导致不可逆的数据丢失！\n'
  printf '═%.0s' {1..74}
  printf '\n'
  printf '  --purge 将删除以下数据卷（连同其中的全部数据）：\n'
  if [ ${#OAP_DATA_VOLUMES[@]} -gt 0 ]; then
    for v in "${OAP_DATA_VOLUMES[@]}"; do printf '      - %s\n' "$v"; done
  else
    printf '      （当前未发现本项目的卷）\n'
  fi
  printf '\n'
  printf '  具体会丢什么：\n'
  printf '    · 平台库：全部租户 / 本体 / Run / 审计 / 账本记录（含种子演示数据）\n'
  printf '    · Mock ERP 库：仿真业务数据\n'
  printf '    · 对象存储：已上传的构件\n'
  printf '  恢复方式：只能重新 `docker compose up` 迁移 + `bash start.sh --seed` 重建演示数据；\n'
  printf '            非演示数据无法找回。\n'
  printf '═%.0s' {1..74}
  printf '\n'

  if [ "$ASSUME_YES" != "1" ]; then
    die "已拒绝执行：--purge 需要显式确认。
    请确认无误后执行：   bash stop.sh --purge --yes
    （此守卫是 fail-closed 的：只给 --purge 一律不删。）"
  fi
  warn "已确认 --purge --yes，即将删除上述数据卷。"
fi

# ── 停止 ────────────────────────────────────────────────────────────────────
if [ "$PURGE" = "1" ]; then
  step "停止服务并删除数据卷（--purge）"
  DOWN_ARGS=(down --remove-orphans --volumes)
else
  step "停止服务（保留数据卷）"
  DOWN_ARGS=(down --remove-orphans)
fi

if ! "${COMPOSE[@]}" "${DOWN_ARGS[@]}"; then
  die "docker compose down 失败（上方为 compose 输出）。"
fi
ok "compose down 完成"

# ── 校验：确实没有残留容器 ──────────────────────────────────────────────────
# 不校验就报告"已停止"是典型的自欺：down 可能因权限、网络或 profile 漏项部分失败。
step "校验停止结果"
remaining="$("${COMPOSE[@]}" ps -aq 2> /dev/null | wc -l | tr -d ' ')"
# 注：ps -aq 只列出本项目（compose project）的容器。
if [ "$remaining" != "0" ]; then
  warn "仍有 $remaining 个本项目容器存在，请检查："
  "${COMPOSE[@]}" ps -a 2>&1 || true
  die "停止未彻底完成。"
fi
ok "本项目容器已全部移除"

if [ "$PURGE" = "1" ]; then
  left_over=()
  while IFS= read -r v; do
    [ -n "$v" ] && left_over+=("$v")
  done < <(docker volume ls --format '{{.Name}}' 2> /dev/null | grep -E '(^|_)(odap|mockerp)-(postgres|objectstore)$' || true)
  if [ ${#left_over[@]} -gt 0 ]; then
    warn "仍有数据卷存在：${left_over[*]}"
  else
    ok "数据卷已删除"
  fi
else
  if [ ${#OAP_DATA_VOLUMES[@]} -gt 0 ]; then
    ok "数据卷已保留：${OAP_DATA_VOLUMES[*]}"
  fi
fi

printf '\n停止完成。重新启动： bash start.sh\n'
if [ "$PURGE" != "1" ] && [ ${#OAP_DATA_VOLUMES[@]} -gt 0 ]; then
  printf '                                  （数据保留；如需清库： bash stop.sh --purge --yes）\n'
fi
