#!/usr/bin/env bash
# 10-artifact.sh —— 构建不可变候选制品（§10.1 第 10 级）。
#
# 产出：
#   artifacts/ci/dist/{api,worker,migrate,mock-erp,contract-lint}  —— -trimpath 构建
#   artifacts/ci/manifest.json                                     —— 名称/大小/sha256
#
# "不可变"的含义：-trimpath 去掉本地路径，构建产物只由源码决定；
# manifest 记录每个二进制的 sha256，供后续环境部署时比对（防篡改/防串版本）。
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

ci_stage "10 构建不可变候选制品"
ci_require_go

OUT_DIR="artifacts/ci"
DIST="$OUT_DIR/dist"
mkdir -p "$DIST"

# main 包 → 制品名映射
declare -A TARGETS=(
  [./cmd/api]=api
  [./cmd/worker]=worker
  [./cmd/migrate]=migrate
  [./cmd/mock-erp]=mock-erp
  [./tools/contract-lint]=contract-lint
)

MANIFEST="$OUT_DIR/manifest.json"
{
  printf '{\n'
  printf '  "generated_at": "%s",\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf '  "go_version": "%s",\n' "$("$CI_GO" version | awk '{print $3}')"
  printf '  "artifacts": [\n'
} >"$MANIFEST"

first=1
for pkg in "${!TARGETS[@]}"; do
  name="${TARGETS[$pkg]}"
  if [ ! -d "${pkg#./}" ]; then
    ci_note "跳过不存在的 main 包：$pkg"
    continue
  fi
  if ! "$CI_GO" build -trimpath -o "$DIST/$name" "$pkg" 2>&1; then
    ci_fail_exit "构建 $pkg 失败"
  fi
  size="$(wc -c <"$DIST/$name" | tr -d ' ')"
  digest="$(sha256sum "$DIST/$name" | awk '{print $1}')"
  [ "$first" = "1" ] || printf ',\n' >>"$MANIFEST"
  first=0
  printf '    {"name": "%s", "package": "%s", "size": %s, "sha256": "%s"}' \
    "$name" "$pkg" "$size" "$digest" >>"$MANIFEST"
  ci_note "$name  $digest"
done

printf '\n  ]\n}\n' >>"$MANIFEST"
ci_note "清单写入 $MANIFEST"

ci_ok
