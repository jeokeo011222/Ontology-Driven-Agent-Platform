#!/usr/bin/env bash
# 全路由渲染体检：逐路由 open → 读主区域文本长度 → 判定白屏 / 内容缺失。
# 判据（见 local-webapp-screenshot-verify §3 / §2b）：
#   - .main-region 不存在或 innerText 极短 → 页面崩溃（React 卸载整棵树）；
#   - 出现"错误码"但无页面标题 → 该页数据端点报错；
#   - 有 .page__title → 页面结构正常。
set -u
URL="http://127.0.0.1:5173"
TMP="/d/Code/Ontology-Agent/artifacts/cache/_probe"
mkdir -p "$TMP"
export AGENT_BROWSER_ARGS="--no-sandbox,--disable-dev-shm-usage,--disable-gpu"
export no_proxy="127.0.0.1,localhost" NO_PROXY="127.0.0.1,localhost"
agent-browser close >/dev/null 2>&1 || true

# 预热：浏览器 daemon 冷启动时，生命周期内的第一次 open 会因竞争而失败——
# 表现为 #root / .main-region 均不存在（读数 -1），极易被误判成"首条路由白屏"。
# 因此先对一条已知可用路由做一次预热 open，再进入正式巡检。
agent-browser open "$URL/admin" >/dev/null 2>&1
sleep 8

ROUTES=(
  "/workspace" "/assistant" "/tasks" "/approvals" "/apps"
  "/admin" "/admin/identity" "/admin/ontologies" "/admin/data-sources" "/admin/agents"
  "/admin/tools" "/admin/workflows" "/admin/models" "/admin/connectors" "/admin/policies"
  "/admin/monitoring" "/admin/audit" "/admin/evaluation" "/admin/releases"
)

probe() {
  agent-browser eval "(()=>{try{return String($1)}catch(e){return 'ERR:'+e.message}})()" > "$TMP/p.txt" 2>/dev/null
  tr -d '\r\n' < "$TMP/p.txt" | sed 's/^"//; s/"$//'
}

# 等待页面就绪：#root 存在且已渲染出页面标题或状态标题。
# 不能只靠固定 sleep——`open` 返回时导航可能尚未完成（DOM 里连 #root 都没有），
# 此时任何读数都会得到 -1，被误判为"白屏"。宁可轮询等待，也不要误报。
wait_ready() {  # $1=路由（用于兜底重新 open）
  local i t
  for i in $(seq 1 20); do
    t=$(probe 'document.querySelector("#root") && document.querySelector(".page__title,.state__title") ? "1" : "0"')
    [ "$t" = "1" ] && return 0
    sleep 1
  done
  # 兜底：重新导航一次再等
  agent-browser open "$URL$1" >/dev/null 2>&1
  for i in $(seq 1 20); do
    t=$(probe 'document.querySelector("#root") && document.querySelector(".page__title,.state__title") ? "1" : "0"')
    [ "$t" = "1" ] && return 0
    sleep 1
  done
  return 1
}

printf '%-24s %-6s %-8s %-6s %s\n' ROUTE TITLE ROOTLEN ERRORS NOTE
for r in "${ROUTES[@]}"; do
  agent-browser open "$URL$r" >/dev/null 2>&1
  if ! wait_ready "$r"; then
    printf '%-24s %-6s %-8s %-6s %s\n' "$r" "NOTREADY" "-1" "-" "-1"
    continue
  fi
  title=$(probe 'document.querySelector(".page__title") ? document.querySelector(".page__title").textContent : "-"')
  rootlen=$(probe 'document.querySelector("#root") ? document.querySelector("#root").innerText.replace(/\s+/g,"").length : -1')
  errs=$(probe 'document.querySelector(".state__title") ? document.querySelector(".state__title").textContent : "-"')
  note=$(probe 'document.querySelector(".main-region") ? (document.querySelector(".main-region").innerText.replace(/\s+/g,"").length) : -1')
  printf '%-24s %-6s %-8s %-6s %s\n' "$r" "${title:0:20}" "$rootlen" "$errs" "$note"
done

agent-browser close >/dev/null 2>&1 || true
exit 0