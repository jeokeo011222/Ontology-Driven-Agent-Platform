#!/usr/bin/env bash
# 目录类页面渲染门禁 —— 缺陷 #19、#20、#24 的常驻回归测试。
#
# 覆盖 6 个"应当有真实数据"的列表页：
#   企业本体 / Agent 管理 / 工具管理 / 身份与权限 / 模型与推理 / 工作流与 Harness
#
# 回答的问题：这些列表页是否
#   ① 不白屏（React 树未被卸载）；
#   ② 渲染出真实数据行（而不是空态）；
#   ③ 状态徽标为**词表内取值**（未出现"未知状态"占位）；
#   ④ 未把 404 伪装成"资源不存在"（缺陷 #20）。
#
# 为什么必须单独有此门禁：`tools/ui-route-sweep.sh` 只断言"页面有没有渲染出标题"，
# 而缺陷 #19 的形态恰好是**标题在、表格渲染时崩**——页面结构部分存在，
# 徽标却永远出不来。仅断言标题的门禁对这类缺陷是假阴性。
# 缺陷 #24 是同一门禁抓出的另一种形态："页面标题 + 空态标题都在、无报错"，
# 但一行数据都没有——逐路由体检同样给不出任何异常。
#
# 纪律（同 tools/ui-verify.sh）：不接管道（管道会 SIGTERM 误判成浏览器启动失败）；
#   开头幂等 close；open → 轮询就绪 → 落文件断言。
set -u

PROJ="/d/Code/Ontology-Agent"
URL="http://127.0.0.1:5173"
TMP="$PROJ/artifacts/cache/_catalog_verify"
mkdir -p "$TMP"

export AGENT_BROWSER_ARGS="--no-sandbox,--disable-dev-shm-usage,--disable-gpu"
# 本机 http_proxy=127.0.0.1:xxxx 会拦截对回环地址的请求，导致浏览器连不上本地 web
# 而被误判为"页面白屏"。强制旁路回环地址。
unset http_proxy https_proxy HTTP_PROXY HTTPS_PROXY
export no_proxy="127.0.0.1,localhost" NO_PROXY="127.0.0.1,localhost"

agent-browser close > /dev/null 2>&1 || true

PASS=0
FAIL=0

probe() {  # $1=JS 表达式 → 回声一行字符串
  agent-browser eval "(()=>{try{return String($1)}catch(e){return 'ERR:'+e.message}})()" \
    > "$TMP/probe.txt" 2> /dev/null
  tr -d '\r\n' < "$TMP/probe.txt" | sed 's/^"//; s/"$//'
}

check() {  # $1=描述  $2=实测值  $3=期望值
  if [ "$2" = "$3" ]; then
    printf '  ✓ %-18s %s\n' "$1" "$2"
    PASS=$((PASS + 1))
  else
    printf '  ✗ %-18s 实测=%s 期望=%s\n' "$1" "$2" "$3"
    FAIL=$((FAIL + 1))
  fi
}

wait_ready() {  # $1=路由
  local i t
  for i in $(seq 1 25); do
    t=$(probe 'document.querySelector("#root") && document.querySelector(".page__title,.state__title") ? "1" : "0"')
    [ "$t" = "1" ] && return 0
    sleep 1
  done
  # 兜底：重新导航一次再等。首次 open 可能在文档 commit 前就返回，
  # 或被"上一条路由的卸载"打断，此时 DOM 一直是空的——重导一次即可救回，
  # 否则会把正常页面误报成白屏（本波次实测发生过）。
  agent-browser open "$URL$1" > /dev/null 2>&1
  for i in $(seq 1 25); do
    t=$(probe 'document.querySelector("#root") && document.querySelector(".page__title,.state__title") ? "1" : "0"')
    [ "$t" = "1" ] && return 0
    sleep 1
  done
  return 1
}

# 预热：daemon 冷启动时，生命周期内的**第一次** open 常常在文档 commit 之前就返回，
# 之后所有读数都是空 DOM → 表现为"首条路由白屏"。固定 sleep 不可靠（机器负载不同，
# 6s 时够时不够），必须**轮询到真的渲染出结构**再进入正式巡检。
agent-browser open "$URL/admin" > /dev/null 2>&1
for i in $(seq 1 30); do
  t=$(probe 'document.querySelector("#root") && document.querySelector(".page__title,.state__title") ? "1" : "0"')
  [ "$t" = "1" ] && break
  sleep 1
done

# 路由 : 期望页面标题 : 期望最小数据行数
# 注意：身份页两张表（角色 1 + 绑定 1 = 2）；模型页两张表（供应商 2 + 端点 2 = 4）
# 工作流页：1 个模板（planning.plans.status='TEMPLATE' 的最新修订）
TARGETS=(
  "/admin/ontologies:企业本体:1"
  "/admin/agents:Agent 管理:1"
  "/admin/tools:工具管理:7"
  "/admin/identity:身份与权限:2"
  "/admin/models:模型与推理:4"
  "/admin/workflows:工作流与 Harness:1"
)

for t in "${TARGETS[@]}"; do
  route="${t%%:*}"; rest="${t#*:}"; want_title="${rest%%:*}"; want_rows="${rest##*:}"
  echo "──────────────────────────────────────────"
  echo "ROUTE $route"
  agent-browser open "$URL$route" > /dev/null 2>&1
  if ! wait_ready "$route"; then
    echo "  ✗ 未就绪（DOM 未渲染出标题）——疑似白屏"
    FAIL=$((FAIL + 1))
    continue
  fi

  title=$(probe 'document.querySelector(".page__title") ? document.querySelector(".page__title").textContent : "（无）"')
  state=$(probe 'document.querySelector(".state__title") ? document.querySelector(".state__title").textContent : "（无错误态）"')
  rows=$(probe 'document.querySelectorAll("tbody tr").length')
  badges=$(probe 'document.querySelectorAll(".badge").length')
  unknown=$(probe 'document.body.innerText.indexOf("未知状态")>=0 ? "是" : "否"')
  misleading=$(probe 'document.body.innerText.indexOf("不在可见范围")>=0 ? "是" : "否"')

  check "页面标题" "$title" "$want_title"
  check "无错误态" "$state" "（无错误态）"
  check "数据行数" "$rows" "$want_rows"
  check "出现未知状态" "$unknown" "否"
  check "误导性 404 文案" "$misleading" "否"
  if [ "$badges" -gt 0 ] 2> /dev/null; then
    printf '  ✓ %-18s %s\n' "徽标数" "$badges"
    PASS=$((PASS + 1))
  else
    printf '  ✗ %-18s 实测=%s（期望 > 0）\n' "徽标数" "$badges"
    FAIL=$((FAIL + 1))
  fi
  echo "  徽标文本: $(probe 'Array.from(document.querySelectorAll(".badge")).map(function(b){return b.textContent}).join(",")')"
  agent-browser screenshot "$TMP$(echo "$route" | tr '/' '_').png" > /dev/null 2>&1 || true
done

agent-browser close > /dev/null 2>&1 || true

echo "──────────────────────────────────────────"
echo "目录类页面渲染门禁：通过 $PASS 项，失败 $FAIL 项"
[ "$FAIL" -eq 0 ] || exit 1
