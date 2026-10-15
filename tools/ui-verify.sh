#!/usr/bin/env bash
# ODAP 前端产品级 UI 验收截图脚本（第十七篇 §3 视觉规范）。
#
# 纪律（见 local-webapp-screenshot-verify）：
#   - 开头幂等 close，清掉上一次崩掉的僵死会话；
#   - 每个视图一次 open → 断言非空 → 截图，不跨 Bash 调用传状态；
#   - 不接管道（管道会 SIGTERM 误判成浏览器启动失败），一律落文件再读；
#   - 截图后看字节数，2–8KB 视为白屏。
set -u

PROJ="/d/Code/Ontology-Agent"
URL="http://127.0.0.1:5173"
OUT="$PROJ/artifacts/shots"
TMP="$PROJ/artifacts/cache/_probe"

export AGENT_BROWSER_ARGS="--no-sandbox,--disable-dev-shm-usage,--disable-gpu"
# 本机可能设置 http_proxy=127.0.0.1:xxxx，会拦截对 127.0.0.1 的请求，导致
# 浏览器连不上本地 web 而误判为"页面白屏"。这里强制旁路回环地址。
unset http_proxy https_proxy HTTP_PROXY HTTPS_PROXY
export no_proxy="127.0.0.1,localhost" NO_PROXY="127.0.0.1,localhost"
mkdir -p "$OUT" "$TMP"
find "$OUT" -name '*.png' -delete 2>/dev/null

agent-browser close >/dev/null 2>&1 || true

PASS=0
FAIL=0

probe() {  # $1=JS 表达式 → 回声一行字符串（不判定）
  agent-browser eval "(()=>{try{return String($1)}catch(e){return 'ERR:'+e.message}})()" \
    > "$TMP/probe.txt" 2>/dev/null
  tr -d '\r\n' < "$TMP/probe.txt" | sed 's/^"//; s/"$//'
}

# 等待页面就绪：#root 存在且已渲染出页面标题或状态标题。
# 不能只靠固定 sleep——`open` 返回时导航可能尚未完成（DOM 里连 #root 都没有），
# 此时全部断言都会失败，被误判成"白屏/图标缺失"。宁可轮询等待。
wait_ready() {  # $1=完整 URL（兜底重新导航用）
  local i t
  for i in $(seq 1 20); do
    t=$(probe 'document.querySelector("#root") && document.querySelector(".page__title,.state__title") ? "1" : "0"')
    [ "$t" = "1" ] && return 0
    sleep 1
  done
  agent-browser open "$1" >/dev/null 2>&1
  for i in $(seq 1 20); do
    t=$(probe 'document.querySelector("#root") && document.querySelector(".page__title,.state__title") ? "1" : "0"')
    [ "$t" = "1" ] && return 0
    sleep 1
  done
  return 1
}

ask() {  # $1=JS 表达式  $2=说明（只打印读数，不判定）
  local got
  got=$(probe "$1")
  echo "  [read] $2 = $got"
}

check() {  # $1=返回布尔的 JS 表达式  $2=说明
  local got
  got=$(probe "$1")
  if [ "$got" = "true" ]; then
    echo "  [OK]   $2"; PASS=$((PASS + 1))
  else
    echo "  [FAIL] $2（实际 $got）"; FAIL=$((FAIL + 1))
  fi
}

# 通用断言：页面级图标 + 侧栏图标 + 品牌徽记 + 无裸字形
assert_shell() {
  check "document.querySelectorAll('.page__heading-icon svg').length === 1" "页面头部图标已渲染"
  # 注意：Icon 组件把 className 注入到 <svg> 自身（<svg class="icon sidenav__link-icon">），
  # 因此这里必须用复合选择器 svg.sidenav__link-icon，而非后代选择器。
  check "document.querySelectorAll('svg.sidenav__link-icon').length >= 4" "侧栏导航图标已渲染"
  check "document.querySelectorAll('svg.sidenav__link-icon').length === document.querySelectorAll('.sidenav__link').length" "每个导航项都有图标（无纯文字项）"
  check "document.querySelectorAll('.brand-logo svg circle').length === 3" "品牌徽记（三点连边）已渲染"
  check "document.querySelectorAll('svg.icon').length >= 8" "SVG 图标总数 >= 8"
  check "document.querySelector('.icon-btn svg') !== null" "顶栏控件为图标按钮"
}

# 通用断言：页面里不得残留裸 Unicode 字形图标
assert_no_glyph() {
  local n
  n=$(probe "(()=>{const t=document.querySelector('.main-region')?.innerText||'';return String((t.match(/[\u26a0\u2691\u2298\u21c4\u2715\u2713\u25b6\u25d4\u21bb\u2016]/g)||[]).length)})()")
  if [ "$n" = "0" ]; then
    echo "  [OK]   主区域无裸字形图标（0 个）"; PASS=$((PASS + 1))
  else
    echo "  [FAIL] 主区域仍有 $n 个裸字形图标"; FAIL=$((FAIL + 1))
  fi
}

shot() {  # $1=文件名  $2=路由
  local name="$1" route="$2"
  echo "── $name  ($route)"
  agent-browser open "$URL$route" >/dev/null 2>&1
  wait_ready "$URL$route"
  assert_shell
  assert_no_glyph
  agent-browser screenshot "$OUT/$name.png" >/dev/null 2>&1
  local sz
  sz=$(stat -c %s "$OUT/$name.png" 2>/dev/null || echo 0)
  echo "  [size] $name.png = ${sz} bytes"
}

shot 01_workspace_business   "/workspace"
shot 02_tasks                "/tasks"
shot 03_approvals            "/approvals"
shot 04_business_apps        "/apps"
shot 05_assistant            "/assistant"
shot 06_admin_overview       "/admin"
shot 07_admin_ontology       "/admin/ontologies"
shot 08_admin_identity       "/admin/identity"
shot 09_admin_monitoring     "/admin/monitoring"
shot 10_admin_audit          "/admin/audit"

# 工作台指标卡细节：图标 + 32px 数值 + 等宽数字
echo "── 指标卡细节（/workspace）"
agent-browser open "$URL/workspace" >/dev/null 2>&1
wait_ready "$URL/workspace"
check "document.querySelectorAll('.metric').length === 4" "工作台 4 张指标卡"
check "document.querySelectorAll('.metric__icon svg').length === 4" "指标卡图标 4 个"
ask "getComputedStyle(document.querySelector('.metric__value')).fontSize" "指标数值字号"
ask "getComputedStyle(document.querySelector('.metric__value')).fontVariantNumeric" "指标数值等宽数字"
ask "getComputedStyle(document.querySelector('.page__title')).fontSize" "页面标题字号"
ask "getComputedStyle(document.body).fontSize" "正文字号"
agent-browser screenshot "$OUT/11_workspace_metrics.png" >/dev/null 2>&1
stat -c '  [size] 11_workspace_metrics.png = %s bytes' "$OUT/11_workspace_metrics.png" 2>/dev/null

# 侧栏折叠态（管理控制台）：验证折叠后只剩图标且宽度回收
echo "── 侧栏折叠（/admin）"
agent-browser open "$URL/admin" >/dev/null 2>&1
wait_ready "$URL/admin"
agent-browser eval 'document.querySelector(".icon-btn").click(); "ok"' >/dev/null 2>&1
sleep 1
check "document.querySelector('.workspace-body--collapsed') !== null" "折叠态已应用"
ask "Math.round(document.querySelector('.sidenav').getBoundingClientRect().width)" "折叠后侧栏宽度(px)"
agent-browser screenshot "$OUT/12_admin_collapsed.png" >/dev/null 2>&1
stat -c '  [size] 12_admin_collapsed.png = %s bytes' "$OUT/12_admin_collapsed.png" 2>/dev/null

agent-browser close >/dev/null 2>&1 || true

echo
echo "=== 截图清单 ==="
ls -la --time-style=+%H:%M:%S "$OUT"/*.png 2>/dev/null
echo
echo "=== 断言：通过 $PASS 项，失败 $FAIL 项 ==="
[ "$FAIL" -eq 0 ] || exit 1
exit 0
