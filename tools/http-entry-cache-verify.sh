#!/usr/bin/env bash
# 入口缓存头门禁 —— 缺陷 #23 的常驻回归测试（纯 HTTP，不依赖浏览器）。
#
# 回答的问题：
#   ① web 容器返回的 index.html 是否**声明了不可缓存**（否则浏览器启发式缓存入口，
#      用户会被钉死在旧 bundle 上——代码修了、镜像也重建了，用户却看不到）；
#   ② 带内容哈希的静态资源是否可长期强缓存、**只有一个** Cache-Control 头，
#      且 index.html 引用的哈希资源确实存在（否则是"入口指向已删除的旧包"）；
#   ③ 服务端发出去的字节是否**等于 web 镜像里的字节**（防"nginx 服务了旧文件"）；
#   ④ web 镜像是否比前端源码新（防"改了代码但没重建镜像"）。
#
#   ③ 的判据为什么**不是**本地 web/dist：Dockerfile.web 在镜像内 `npm run build`
#   产出 /app/dist，而 .dockerignore **排除 web/dist** —— 本地 dist 根本不是部署
#   输入。拿它当判据会在镜像重建后稳定假失败（实测：宿主 index-ByPFyw7x.js 与
#   镜像 index--cQBdYvB.js 内容不同，而服务端与镜像的 sha256 完全一致）。
#   同理，判据一律用**内容摘要**而非文件名：文件名哈希由构建环境决定。
#
#   ④ 为什么需要单独一条：`docker compose up -d` **不会**重建镜像（已实测，
#   镜像 Created 不变）。所以"改完前端直接重启"就会得到旧界面——这个坑没有任何
#   其它门禁能发现（它们都只断言"页面渲染出来了"）。
#
# 为什么必须有：缺陷 #23 的全部症状对其它门禁都是**不可见**的——
#   `docker compose up` 成功、`ui-route-sweep.sh` 19/19 通过、`ui-catalog-verify.sh` 全绿，
#   因为它们每次都是**新起浏览器**访问，从不复用用户的缓存。真实用户用的是**同一个浏览器**。
#
# 用法：bash tools/http-entry-cache-verify.sh
set -u

URL="${ODAP_WEB_URL:-http://127.0.0.1:5173}"
# 以脚本所在目录推导项目根，避免写死绝对路径（脚本位于 tools/ 下）。
PROJ="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST="$PROJ/web/dist/index.html"

# 本机 http_proxy 会拦截回环请求 → 误判"连不上"。强制旁路。
unset http_proxy https_proxy HTTP_PROXY HTTPS_PROXY
export no_proxy="127.0.0.1,localhost" NO_PROXY="127.0.0.1,localhost"
# docker run 的 -c 脚本参数含 /usr/share/... 这类以 / 开头的字符串，
# Git Bash(MSYS) 会把它当 Windows 路径转换。统一关闭。
export MSYS_NO_PATHCONV=1

PASS=0
FAIL=0

check() { # $1=描述 $2=实测 $3=期望
  if [ "$2" = "$3" ]; then
    printf '  ✓ %-26s %s\n' "$1" "$2"
    PASS=$((PASS + 1))
  else
    printf '  ✗ %-26s 实测=%s 期望=%s\n' "$1" "$2" "$3"
    FAIL=$((FAIL + 1))
  fi
}

hdr() { # $1=URL  $2=头名（大小写不敏感）→ 回声全部匹配行（可能多行）
  curl -sI "$1" 2> /dev/null | tr -d '\r' | awk -v h="$2" 'BEGIN{IGNORECASE=1} index($0,h":")==1 {print substr($0,length(h)+2)}'
}

echo "──────────────────────────────────────────────────────────"
echo "① 入口 HTML 必须不可缓存"
for path in "/" "/index.html" "/admin/workflows"; do
  cc=$(hdr "$URL$path" "Cache-Control")
  case "$cc" in
    *no-store*) check "Cache-Control($path)" "no-store" "no-store" ;;
    *no-cache*) check "Cache-Control($path)" "no-cache" "no-store" ;;
    *) check "Cache-Control($path)" "${cc:-（缺失）}" "no-store" ;;
  esac
done

echo "② 带哈希资源可 immutable，且只有一个 Cache-Control"
asset=$(curl -s "$URL/" 2> /dev/null | grep -oE 'assets/index-[A-Za-z0-9_-]+\.js' | head -1)
if [ -z "$asset" ]; then
  check "定位入口 JS" "（未找到）" "assets/index-*.js"
else
  check "入口 JS 可访问" "$(curl -s -o /dev/null -w '%{http_code}' "$URL/$asset")" "200"
  n=$(curl -sI "$URL/$asset" 2> /dev/null | tr -d '\r' | grep -ci '^cache-control:')
  check "Cache-Control 头数" "$n" "1"
  acc=$(hdr "$URL/$asset" "Cache-Control")
  case "$acc" in
    *immutable*) check "资产 Cache-Control" "immutable" "immutable" ;;
    *) check "资产 Cache-Control" "${acc:-（缺失）}" "immutable" ;;
  esac
fi

echo "③ 服务端产物 == web 镜像内产物（防「nginx 服务了旧文件」）"
img="$(docker compose images -q web 2> /dev/null | head -1)"
srv=$(curl -s "$URL/" 2> /dev/null | grep -oE 'assets/index-[A-Za-z0-9_-]+\.js' | head -1)
if [ -n "$img" ] && [ -n "$srv" ]; then
  srv_sum=$(curl -s "$URL/$srv" 2> /dev/null | sha256sum | cut -d' ' -f1)
  img_sum=$(docker run --rm --entrypoint sh "$img" \
    -c "sha256sum /usr/share/nginx/html/$srv" 2> /dev/null | cut -d' ' -f1)
  check "服务端 ⇄ 镜像内产物" "${srv_sum:0:12}" "${img_sum:0:12}"
  printf '    （比对 %s）\n' "$srv"
else
  check "定位 web 镜像 / 入口产物" "img=${img:-无} srv=${srv:-无}" "两者均非空"
fi

echo "④ web 镜像是否比前端源码新（防「改了代码但没重建镜像」）"
# compose up -d 不会重建镜像，因此"改前端 → 直接重启"会得到旧界面。
# 这一条是唯一能发现该情况的断言。
newest_src=$(find "$PROJ/web/src" "$PROJ/web/package.json" -type f -printf '%T@\n' 2> /dev/null |
  sort -rn | head -1)
src_epoch="${newest_src%%.*}"
img_epoch=""
if [ -n "$img" ]; then
  img_epoch=$(date -d "$(docker image inspect "$img" --format '{{.Created}}' 2> /dev/null)" +%s 2> /dev/null)
fi
if [ -n "$src_epoch" ] && [ -n "$img_epoch" ]; then
  if [ "$img_epoch" -ge "$src_epoch" ]; then
    check "镜像不早于前端源码" "是" "是"
  else
    check "镜像不早于前端源码" "否（镜像偏旧）" "是"
    printf '    → 前端源码有改动但 web 镜像未重建，请执行： bash start.sh --build\n'
  fi
else
  echo "  （跳过：无法取得镜像或源码时间戳）"
fi

echo "──────────────────────────────────────────────────────────"
echo "入口缓存头门禁：通过 $PASS 项，失败 $FAIL 项"
[ "$FAIL" -eq 0 ] || exit 1
