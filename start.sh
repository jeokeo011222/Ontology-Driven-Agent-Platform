#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# ODAP 启动脚本（依据第十六篇 §8.1 服务拓扑 / §8.2 Compose 骨架）
#
# 用法：
#   bash start.sh                          # 启动最小路径，并等待真的就绪
#   bash start.sh --build                  # 先重建镜像再启动（改过 Go / 前端代码）
#   bash start.sh --profile objectstore    # 追加 MinIO 对象存储（9000/9001）
#   bash start.sh --profile observability  # 追加本地 OTLP collector（4317/4318）
#   bash start.sh --seed                   # 就绪后导入演示数据（幂等，可重复执行）
#   bash start.sh --reseed                 # 就绪后重建演示数据（--replace，先清该数据集的行）
#   bash start.sh --init-env               # 缺 .env 时从 .env.example 生成（全新 clone 首启）
#   bash start.sh --no-wait                # 只把容器拉起来，不等就绪
#   bash start.sh --timeout 300            # 就绪等待上限（秒，默认 240）
#   bash start.sh --logs                   # 就绪后继续跟随日志（Ctrl-C 退出，不影响容器）
#
# 退出码：0 = 全部服务就绪；1 = 前置检查 / 启动 / 迁移 / 就绪 任一步失败。
#
# 为什么不是一句 `docker compose up -d --build`：
#   1. `up -d` 返回 0 不代表可用。迁移 job 失败时依赖它的服务**不会启动**，
#      而 compose 只在 stderr 里提一句；脚本必须自己校验一次性 job 的退出码
#      并把日志端到端打出来。
#   2. 就绪必须**轮询真实探针**（/readyz 会真的查库），不能固定 sleep。
#   3. 缺 .env 时 compose 只会抛出 `${VAR:?}` 断言错误，用户看不出该改哪里。
#   4. 端口只绑 127.0.0.1（见 compose.yaml），脚本把可访问地址直接打出来。
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

# 无论从哪个目录调用，都以脚本所在目录（= 仓库根）为工作目录：
# compose.yaml / .env / datasets 全是相对路径，cd 错了会静默用到别的文件。
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

# Git Bash(MSYS) 会把以 / 开头的参数当 Windows 路径转换，破坏容器内路径。
export MSYS_NO_PATHCONV=1
# 供挂载用的宿主绝对路径：MSYS 下 `pwd -W` 得到 D:/... 形式（Docker 可接受），
# 非 MSYS 环境回退到普通 POSIX 路径。
HOST_ROOT="$(cd "$ROOT" && { pwd -W 2> /dev/null || pwd; })"
# 本机 http_proxy 会拦截回环请求，导致就绪探针误判"连不上"。强制旁路。
unset http_proxy https_proxy HTTP_PROXY HTTPS_PROXY 2>/dev/null || true
export no_proxy="127.0.0.1,localhost" NO_PROXY="127.0.0.1,localhost"

# compose.yaml 里写死的宿主端口（不是 .env 变量；改端口要同时改 compose.yaml）。
# 允许用环境变量覆盖：便于本地改端口后对齐，也让就绪失败分支可被测试。
API_PORT="${ODAP_API_PORT:-8080}"
WEB_PORT="${ODAP_WEB_PORT:-5173}"

# ── 选项解析 ────────────────────────────────────────────────────────────────
BUILD=0
SEED=0
RESEED=0
INIT_ENV=0
WAIT=1
FOLLOW_LOGS=0
TIMEOUT=240
PROFILES=()

usage() { sed -n '3,18p' "$0" | sed 's/^# \{0,1\}//'; }

while [ $# -gt 0 ]; do
  case "$1" in
    --build) BUILD=1 ;;
    --seed) SEED=1 ;;
    --reseed) SEED=1; RESEED=1 ;;
    --init-env) INIT_ENV=1 ;;
    --no-wait) WAIT=0 ;;
    --logs) FOLLOW_LOGS=1 ;;
    --profile)
      shift
      [ $# -gt 0 ] || { echo "✗ --profile 需要一个参数（objectstore | observability）" >&2; exit 1; }
      case "$1" in
        objectstore | observability) PROFILES+=("$1") ;;
        *) echo "✗ 未知 profile：$1（可选：objectstore | observability）" >&2; exit 1 ;;
      esac
      ;;
    --timeout)
      shift
      [ $# -gt 0 ] || { echo "✗ --timeout 需要一个秒数" >&2; exit 1; }
      case "$1" in
        '' | *[!0-9]*) echo "✗ --timeout 必须是正整数秒数，收到：$1" >&2; exit 1 ;;
        0) echo "✗ --timeout 必须 ≥ 1；若要只启动不等就绪，请用 --no-wait" >&2; exit 1 ;;
      esac
      TIMEOUT="$1"
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

# ── 前置检查 ────────────────────────────────────────────────────────────────
step "前置检查"

command -v docker > /dev/null 2>&1 ||
  die "未找到 docker 命令。请安装 Docker Desktop，并确保 docker 在 PATH 中。"
ok "docker 可用"

docker info > /dev/null 2>&1 ||
  die "Docker 守护进程未运行。请先启动 Docker Desktop，等状态变为 Running 后重试。"
ok "Docker 守护进程在运行"

docker compose version > /dev/null 2>&1 ||
  die "未找到 docker compose（v2）。本仓库使用 compose.yaml，需要 Docker Compose v2（'docker compose' 而非 'docker-compose'）。"
ok "docker compose v2 可用"

command -v curl > /dev/null 2>&1 ||
  die "未找到 curl。就绪判定依赖它轮询 /readyz（不做固定 sleep）；请安装 curl 或改用 'docker compose up -d' 自行等待。"
ok "curl 可用"

if [ ! -f .env ]; then
  if [ "$INIT_ENV" = "1" ]; then
    cp .env.example .env
    ok "已从 .env.example 生成 .env（内含占位密码，仅适用于本地开发）"
  else
    die "缺少 .env。请执行：  cp .env.example .env
    或加上 --init-env 让本脚本代为生成。
    （compose.yaml 里多个变量标了必填断言 \${VAR:?}，缺 .env 时它只会报一句断言错误，看不出该改哪里。）"
  fi
fi
ok ".env 存在"

# compose.yaml 中标了 ${VAR:?} 的变量：缺失即启动失败。这里一次性全查出来，
# 而不是让 compose 只报第一个。
REQUIRED_VARS=(
  ODAP_DB_PASSWORD
  MOCKERP_DB_PASSWORD
  ODAP_DATABASE_URL
  ODAP_MIGRATE_URL
  MOCKERP_DATABASE_URL
  ODAP_HTTP_ADDR
  MOCKERP_HTTP_ADDR
  WORKER_CONCURRENCY
  WORKER_LEASE_SECONDS
)
# 启用 objectstore profile 时，MinIO 凭据也是 :? 断言，缺失同样启动失败。
for p in "${PROFILES[@]+"${PROFILES[@]}"}"; do
  if [ "$p" = "objectstore" ]; then
    REQUIRED_VARS+=(OBJECT_STORE_ACCESS_KEY OBJECT_STORE_SECRET_KEY)
  fi
done

missing=()
for v in "${REQUIRED_VARS[@]}"; do
  val="$(grep -E "^${v}=" .env 2> /dev/null | head -1 | cut -d= -f2- || true)"
  [ -n "${val:-}" ] || missing+=("$v")
done
if [ ${#missing[@]} -gt 0 ]; then
  printf '    在 .env 中缺失或为空的必需变量：\n' >&2
  for v in "${missing[@]}"; do printf '      - %s\n' "$v" >&2; done
  die "请补齐上述变量后重试（对照 .env.example）。"
fi
ok "必需变量齐全（${#REQUIRED_VARS[@]} 个）"

COMPOSE=(docker compose)
for p in "${PROFILES[@]+"${PROFILES[@]}"}"; do COMPOSE+=(--profile "$p"); done

if [ ${#PROFILES[@]} -gt 0 ]; then
  ok "额外 profile：${PROFILES[*]}"
else
  ok "只用最小路径（无额外 profile）"
fi

# ── 启动 ────────────────────────────────────────────────────────────────────
if [ "$BUILD" = "1" ]; then
  step "启动服务（--build：先重建镜像）"
  UP_ARGS=(up -d --build)
else
  step "启动服务"
  UP_ARGS=(up -d)
fi

# up 失败要给 compose 自己的输出留全，故不放管道。
if ! "${COMPOSE[@]}" "${UP_ARGS[@]}"; then
  printf '\n✗ docker compose up 失败。以下为最近日志（migrate / api）：\n' >&2
  "${COMPOSE[@]}" logs --tail 40 migrate migrate-mockerp api 2>&1 || true
  exit 1
fi

# ── 校验一次性迁移 job ──────────────────────────────────────────────────────
# `up -d` 返回 0 不代表 schema 就绪：迁移 job 是 restart:"no" 的一次性容器，
# 失败时依赖它的 api/worker 根本不会启动。必须自己查退出码。
step "校验一次性迁移 job"
for svc in migrate migrate-mockerp; do
  line="$("${COMPOSE[@]}" ps -a --format '{{.Service}}|{{.State}}|{{.ExitCode}}' 2> /dev/null |
    awk -F'|' -v s="$svc" '$1==s {print $2"|"$3; exit}')"
  if [ -z "$line" ]; then
    warn "$svc 未出现在容器列表（可能被 profile 排除）"
    continue
  fi
  state="${line%%|*}"
  code="${line##*|}"
  if [ "$state" != "exited" ] || [ "$code" != "0" ]; then
    printf '\n✗ %s 未成功完成（state=%s exit=%s）——schema 未就绪，依赖它的服务不会启动。\n' "$svc" "$state" "$code" >&2
    printf '  以下为 %s 的日志：\n' "$svc" >&2
    "${COMPOSE[@]}" logs --tail 60 "$svc" 2>&1 || true
    exit 1
  fi
  ok "$svc 已完成（exit=0）"
done

# ── 等待就绪（轮询真实探针，不做固定 sleep）────────────────────────────────
API_URL="http://127.0.0.1:${API_PORT}"
WEB_URL="http://127.0.0.1:${WEB_PORT}"

if [ "$WAIT" = "1" ]; then
  step "等待就绪（上限 ${TIMEOUT}s；轮询 /readyz 与工作台首页）"
  deadline=$((SECONDS + TIMEOUT))
  api_ok=0
  web_ok=0
  while [ "$SECONDS" -lt "$deadline" ]; do
    if [ "$api_ok" = "0" ]; then
      # /readyz 会真的查库（{"checks":{"database":"ok"},"status":"ok"}），
      # 比 /healthz（只证明进程活着）更严格——正是"可用"的定义。
      if curl -fsS --max-time 3 "$API_URL/readyz" 2> /dev/null | grep -q '"status":"ok"'; then
        api_ok=1
      fi
    fi
    if [ "$web_ok" = "0" ]; then
      code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$WEB_URL/" 2> /dev/null || true)"
      [ "$code" = "200" ] && web_ok=1
    fi
    [ "$api_ok" = "1" ] && [ "$web_ok" = "1" ] && break
    printf '.'
    sleep 2
  done
  printf '\n'

  if [ "$api_ok" != "1" ] || [ "$web_ok" != "1" ]; then
    printf '\n✗ 等待 %ss 后仍未就绪（api=%s web=%s）。\n' "$TIMEOUT" "$api_ok" "$web_ok" >&2
    printf '  容器状态：\n' >&2
    "${COMPOSE[@]}" ps -a 2>&1 || true
    printf '\n  api 最近日志：\n' >&2
    "${COMPOSE[@]}" logs --tail 40 api 2>&1 || true
    printf '\n  web 最近日志：\n' >&2
    "${COMPOSE[@]}" logs --tail 40 web 2>&1 || true
    exit 1
  fi
  ok "api 就绪（/readyz → status=ok）"
  ok "web 可访问（HTTP 200）"
else
  step "跳过就绪等待（--no-wait）"
fi

# ── 演示数据 ────────────────────────────────────────────────────────────────
if [ "$SEED" = "1" ]; then
  if [ "$RESEED" = "1" ]; then
    step "重建演示数据（seed --replace）"
    SEED_ARGS=(seed --dataset finance-demo --replace)
  else
    step "导入演示数据（seed，幂等）"
    SEED_ARGS=(seed --dataset finance-demo)
  fi
  # 用 compose 里的 api 镜像跑 seed：不需要本机 Go 工具链。
  #
  # 但必须把仓库挂进去并切到 /src：**api 镜像里没有 datasets/**（Dockerfile.api
  # 只 COPY 编译好的二进制），而 seed 会按相对路径读 datasets/<name>/dataset.json。
  # 不挂载就会得到：
  #   api seed: seed: 读取 dataset.json 失败: open datasets/finance-demo/dataset.json: no such file or directory
  #
  # 挂成读写（与 tools/dev-run.sh 一致）：OBJECT_STORE_MODE=local 时种子可能要写
  # .odap-objectstore，只读挂载会让这一步失败。
  if ! "${COMPOSE[@]}" run --rm --no-deps \
    -v "${HOST_ROOT}:/src" -w /src \
    api "${SEED_ARGS[@]}"; then
    die "演示数据导入失败（上方为 seed 输出）。服务本身已就绪，可单独重跑：
    docker compose run --rm --no-deps -v \"${HOST_ROOT}:/src\" -w /src api seed --dataset finance-demo"
  fi
  ok "演示数据已写入"
fi

# ── 汇总 ────────────────────────────────────────────────────────────────────
step "容器状态"
"${COMPOSE[@]}" ps -a || true

step "入口"
printf '  工作台      %s\n' "$WEB_URL"
printf '  API 健康    %s/healthz\n' "$API_URL"
printf '  API 就绪    %s/readyz    ← 含数据库连通性检查\n' "$API_URL"
if [ ${#PROFILES[@]} -gt 0 ]; then
  for p in "${PROFILES[@]}"; do
    case "$p" in
      objectstore) printf '  MinIO 控制台 http://127.0.0.1:9001\n' ;;
      observability) printf '  OTLP         gRPC 127.0.0.1:4317 / HTTP 127.0.0.1:4318\n' ;;
    esac
  done
fi

# 演示身份一旦缺失，浏览器打开工作台会得到 401 —— 这是本项目最容易被误当成
# "登录坏了"的坑（nginx 不注入 X-Odap-Dev-* 头，与生产行为一致）。
dev_tenant="$(grep -E '^ODAP_DEV_TENANT=' .env 2> /dev/null | head -1 | cut -d= -f2- || true)"
dev_subject="$(grep -E '^ODAP_DEV_SUBJECT=' .env 2> /dev/null | head -1 | cut -d= -f2- || true)"
if [ -z "${dev_tenant:-}" ] || [ -z "${dev_subject:-}" ]; then
  printf '\n  ! .env 未完整设置开发演示身份。nginx 不会注入 X-Odap-Dev-* 头，\n'
  printf '    浏览器打开工作台将得到 401 UNAUTHENTICATED（这与生产行为一致，不是故障）。\n'
  printf '    本地演示请在 .env 中设置后重启：\n'
  printf '      ODAP_DEV_TENANT=11111111-1111-1111-1111-111111111111\n'
  printf '      ODAP_DEV_SUBJECT=22222222-2222-2222-2222-222222222222\n'
fi

printf '\n启动完成。停止： bash stop.sh    重启： bash restart.sh\n'

if [ "$FOLLOW_LOGS" = "1" ]; then
  step "跟随日志（Ctrl-C 退出，不会停止容器）"
  exec "${COMPOSE[@]}" logs -f --tail 20
fi
