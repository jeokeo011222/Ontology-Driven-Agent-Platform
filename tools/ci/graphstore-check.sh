#!/usr/bin/env bash
# graphstore-check.sh —— 对指定 SPARQL 端点运行 `api graphstore-check`。
#
# 用法：bash tools/ci/graphstore-check.sh [SPARQL 端点根地址]
#
# 为什么单独抽出来：12-graphstore.sh 需要在三个层级的端点（已配置的 Fuseki /
# compose 临时 Fuseki / RDFLib 测试端点）上跑同一份断言，装配方式相同，
# 只有端点不同。抽出来避免"三处各写一份装配"再次出现真值分叉。
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

ENDPOINT="${1:-}"
if [ -z "$ENDPOINT" ]; then
  echo "用法：graphstore-check.sh <SPARQL 端点根地址，如 http://127.0.0.1:3030/odap>" >&2
  exit 2
fi

# 本地端点必须绕过代理：本机 CI 环境里 http_proxy 会拦截 127.0.0.1 的请求。
export NO_PROXY="127.0.0.1,localhost,${NO_PROXY:-}"
export no_proxy="$NO_PROXY"
unset http_proxy https_proxy HTTP_PROXY HTTPS_PROXY

export GRAPHSTORE_MODE=fuseki
export FUSEKI_ENDPOINT="$ENDPOINT"

ci_require_go

# cmd/api graphstore-check 需要平台库时才会读 ODAP_DATABASE_URL；
# fuseki 后端下不需要，因此这里不强制要求环境变量。
exec "$CI_GO" run ./cmd/api graphstore-check
