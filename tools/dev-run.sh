#!/usr/bin/env bash
# dev-run.sh —— 在 compose 网络内、以容器工具链运行任意 Go 命令（免重建镜像）。
#
# 为什么需要它：api/worker 镜像里的二进制是构建时固化的，改完 Go 代码要验证
# 就得 docker compose build（每次约 30s）。开发迭代时用本脚本 go run 更快，
# 且与 compose 内的 postgres 同网络，连接串可直接复用 .env。
#
# 用法：
#   bash tools/dev-run.sh go run ./cmd/api seed --dataset finance-demo
#   bash tools/dev-run.sh go test ./internal/api/...
#   bash tools/dev-run.sh go build ./...
set -eu

PROJ="/d/Code/Ontology-Agent"
NET="${ODAP_DEV_NET:-odap_default}"
cd "$PROJ"

# 从 .env 取出本机连接串（不 source 整个文件，避免注释/特殊字符干扰）。
DB_URL="$(grep -E '^ODAP_DATABASE_URL=' .env | head -1 | cut -d= -f2-)"
if [ -z "${DB_URL}" ]; then
  echo "dev-run: .env 中缺少 ODAP_DATABASE_URL" >&2
  exit 1
fi
APP_ENV_VAL="$(grep -E '^APP_ENV=' .env | head -1 | cut -d= -f2-)"
LOG_LEVEL_VAL="$(grep -E '^LOG_LEVEL=' .env | head -1 | cut -d= -f2-)"
# 迁移用 owner 连接串（cmd/migrate 的 ODAP_MIGRATE_URL）。
MIGRATE_URL="$(grep -E '^ODAP_MIGRATE_URL=' .env | head -1 | cut -d= -f2-)"

MSYS_NO_PATHCONV=1 docker run --rm --network "$NET" \
  -v "//d/Code/Ontology-Agent:/src" -w /src \
  -e PATH=/usr/local/go/bin:/usr/bin:/bin \
  -e GOPROXY=https://goproxy.cn,direct \
  -e GOFLAGS=-mod=mod \
  -e ODAP_DATABASE_URL="$DB_URL" \
  -e ODAP_MIGRATE_URL="${MIGRATE_URL:-$DB_URL}" \
  -e APP_ENV="${APP_ENV_VAL:-development}" \
  -e LOG_LEVEL="${LOG_LEVEL_VAL:-info}" \
  -e ODAP_E2E_BASE_URL="${ODAP_E2E_BASE_URL:-http://api:8080}" \
  -e MOCK_ERP_BASE_URL="${MOCK_ERP_BASE_URL:-http://mockerp:8081}" \
  -e CI_STRICT="${CI_STRICT:-0}" \
  -v odap-gocache:/go/pkg/mod \
  -v odap-gobuild:/root/.cache/go-build \
  golang:1.23-bookworm "$@"
