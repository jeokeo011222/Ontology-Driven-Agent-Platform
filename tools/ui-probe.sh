#!/usr/bin/env bash
# 定点诊断：只查 /workspace 与 /admin，确认失败断言是"探针写错"还是"真缺陷"。
set -u
URL="http://127.0.0.1:5173"
TMP="/d/Code/Ontology-Agent/artifacts/cache/_probe"
mkdir -p "$TMP"
export AGENT_BROWSER_ARGS="--no-sandbox,--disable-dev-shm-usage,--disable-gpu"
agent-browser close >/dev/null 2>&1 || true

probe() {  # $1=JS 表达式  $2=说明
  local got
  agent-browser eval "(()=>{try{return String($1)}catch(e){return 'ERR:'+e.message}})()" \
    > "$TMP/p.txt" 2>/dev/null
  got=$(tr -d '\r\n' < "$TMP/p.txt" | sed 's/^"//; s/"$//')
  echo "  $2 = $got"
}

echo "=== /workspace ==="
agent-browser open "$URL/workspace" >/dev/null 2>&1; sleep 6
probe 'document.querySelectorAll(".metric").length' "metric 元素数"
probe 'document.querySelectorAll(".metric__icon").length' "metric__icon 数"
probe 'document.querySelectorAll(".metric__icon svg").length' "metric__icon>svg 数"
probe 'document.querySelectorAll("svg.metric__icon").length' "svg.metric__icon 数"
probe 'document.querySelector(".page__title") ? document.querySelector(".page__title").textContent : "NULL"' "页面标题"
probe 'document.querySelectorAll(".page__heading-icon").length' "页面头部图标容器数"
probe 'document.querySelectorAll(".sidenav__link").length' "侧栏链接数"
probe 'document.querySelectorAll("svg.sidenav__link-icon").length' "svg.sidenav__link-icon 数"
probe 'document.querySelectorAll(".sidenav__link-icon").length' "任意 .sidenav__link-icon 数"
probe 'document.querySelector(".main-region") ? document.querySelector(".main-region").innerText.slice(0,120).replace(/\n/g," ") : "NULL"' "主区域文本前 120 字"
probe 'document.querySelectorAll("svg.icon").length' "全页 svg.icon 数"

echo "=== /admin ==="
agent-browser open "$URL/admin" >/dev/null 2>&1; sleep 6
probe 'document.querySelector(".page__title") ? document.querySelector(".page__title").textContent : "NULL"' "页面标题"
probe 'document.querySelectorAll(".page__heading-icon").length' "页面头部图标容器数"
probe 'document.querySelectorAll("svg.sidenav__link-icon").length' "svg.sidenav__link-icon 数"
probe 'document.querySelector(".main-region") ? document.querySelector(".main-region").innerText.slice(0,120).replace(/\n/g," ") : "NULL"' "主区域文本前 120 字"

agent-browser close >/dev/null 2>&1
