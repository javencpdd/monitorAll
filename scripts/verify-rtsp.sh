#!/usr/bin/env bash
# RTSP / publish（接收推流）端到端验证
#
# 自包含：本调用内拉起后端 → 验证 → 清理收尾。
# 原因：沙箱下 setsid 起的进程不跨 Bash 调用存活，验证必须和启动在同一调用里。
set -uo pipefail
cd /home/jack/monitorAll
ROOT="$(pwd)"
BE="$ROOT/server/bin/monitorall"
LOG="$ROOT/logs/monitorall.log"
API="http://127.0.0.1:8080/api/v1"
CURL=(curl --noproxy '*' -s -m 10)

hr() { echo "───────────────────────────────────────────────"; }
# 从 {code,message,data} 包装里取 data.id
pid_() { python3 -c 'import sys,json
try:
    d=json.load(sys.stdin)
    print((d.get("data") or {}).get("id",""))
except Exception: print("")'; }

BE_PID=""
if ! timeout 2 bash -c "echo > /dev/tcp/127.0.0.1/8080" 2>/dev/null; then
  setsid nohup "$BE" --config "$ROOT/server/config.yaml" >> "$LOG" 2>&1 &
  BE_PID=$!
  for _ in $(seq 1 30); do
    curl --noproxy '*' --max-time 2 -sf http://127.0.0.1:8080/healthz >/dev/null 2>&1 && break
    sleep 1
  done
fi

CREATED_IDS=()
cleanup() {
  echo ""; hr; echo " 清理"; hr
  for id in "${CREATED_IDS[@]:-}"; do
    [ -n "$id" ] && "${CURL[@]}" -X DELETE "$API/datasources/$id" -o /dev/null \
      -w "  删除数据源 $id → HTTP %{http_code}\n"
  done
  for p in cam/ch101 live/verifywhip; do
    curl --noproxy '*' -s -m 5 -X DELETE "http://127.0.0.1:9997/v3/config/paths/delete/$p" \
      -o /dev/null -w "  删除 MediaMTX 路径 $p → HTTP %{http_code}\n"
  done
  [ -n "$BE_PID" ] && kill "$BE_PID" 2>/dev/null && echo "  已停止本轮拉起的后端 pid=$BE_PID"
}
trap cleanup EXIT

RTSP_BODY='{"name":"验证-海康RTSP","kind":"video","protocol":"rtsp","connParams":{
  "rtmpUrl":"rtsp://admin:okwy1688@192.168.2.69:554/Streaming/Channels/101",
  "mode":"pull","rtspTransport":"tcp","mediaMtxPath":"cam/ch101",
  "preferredProtocol":"hls","audio":false}}'

hr; echo " 【1】创建 RTSP 拉流数据源"; hr
RESP=$("${CURL[@]}" -X POST "$API/datasources" -H 'Content-Type: application/json' -d "$RTSP_BODY")
RTSP_ID=$(echo "$RESP" | pid_)
echo "  数据源 id : ${RTSP_ID:-（失败）}"
[ -n "$RTSP_ID" ] && CREATED_IDS+=("$RTSP_ID")

hr; echo " 【2】测试连接（触发适配器 Start → 向 MediaMTX 注册路径）"; hr
"${CURL[@]}" -X POST "$API/datasources/test" -H 'Content-Type: application/json' -d "$RTSP_BODY" \
  | python3 -c 'import sys,json
d=json.load(sys.stdin).get("data") or {}
print("  ok       :", d.get("ok"))
print("  通道     :", d.get("channels"))
print("  error    :", d.get("error") or "（无）")
print("  hint     :", d.get("hint") or "（无）")'

hr; echo " 【3】MediaMTX 侧是否注册为 RTSP 源"; hr
sleep 2
curl --noproxy '*' -s -m 5 http://127.0.0.1:9997/v3/paths/list \
  | python3 -c 'import sys,json
d=json.load(sys.stdin).get("items",[])
hit=[i for i in d if i["name"]=="cam/ch101"]
if not hit:
    print("  ❌ 未注册；现有路径:", [i["name"] for i in d]); raise SystemExit
i=hit[0]
print("  path        :", i["name"])
print("  source.type :", (i.get("source") or {}).get("type"), "  ← 期望 rtspSource")
print("  ready       :", i.get("ready"), "（摄像头不在同一网段时为 false，属预期）")'

hr; echo " 【4】通道 meta（源地址 / RTSP 传输方式）"; hr
[ -n "$RTSP_ID" ] && "${CURL[@]}" "$API/datasources/$RTSP_ID/channels" \
  | python3 -c 'import sys,json
d=json.load(sys.stdin)
d=d.get("data") if isinstance(d,dict) else d
for ch in (d or []):
    print("  通道:", ch.get("name"))
    for k,v in sorted((ch.get("meta") or {}).items()): print("     ", k, "=", v)'

PUB_BODY='{"name":"验证-接收推流","kind":"video","protocol":"rtmp","connParams":{
  "rtmpUrl":"","mode":"publish","mediaMtxPath":"live/verifywhip",
  "preferredProtocol":"hls","audio":false}}'

hr; echo " 【5】publish（接收远端 WHIP / RTMP 推流）"; hr
RESP2=$("${CURL[@]}" -X POST "$API/datasources" -H 'Content-Type: application/json' -d "$PUB_BODY")
PUB_ID=$(echo "$RESP2" | pid_)
echo "  数据源 id : ${PUB_ID:-（失败）}"
[ -n "$PUB_ID" ] && CREATED_IDS+=("$PUB_ID")
"${CURL[@]}" -X POST "$API/datasources/test" -H 'Content-Type: application/json' -d "$PUB_BODY" \
  | python3 -c 'import sys,json
d=json.load(sys.stdin).get("data") or {}
print("  ok :", d.get("ok"), " 通道:", d.get("channels"), " error:", d.get("error") or "（无）")'
echo ""
[ -n "$PUB_ID" ] && "${CURL[@]}" "$API/datasources/$PUB_ID/channels" \
  | python3 -c 'import sys,json
d=json.load(sys.stdin); d=d.get("data") if isinstance(d,dict) else d
for ch in (d or []):
    m=ch.get("meta") or {}
    print("  WHIP 推流地址 :", m.get("publishUrl"))
    print("  RTMP 推流地址 :", m.get("publishRtmpUrl"))
    print("  videoMode     :", m.get("videoMode"))'
echo ""
echo "  MediaMTX 侧 source 应为 publisher："
curl --noproxy '*' -s -m 5 http://127.0.0.1:9997/v3/paths/list \
  | python3 -c 'import sys,json
d=json.load(sys.stdin).get("items",[])
hit=[i for i in d if i["name"]=="live/verifywhip"]
print("   ", hit[0]["name"], "→ source.type =", (hit[0].get("source") or {}).get("type")) if hit \
  else print("    ❌ 未注册 live/verifywhip")'
