#!/usr/bin/env bash
#
# MonitorAll 视频链路自检
#   ./diag.sh
#
# 按「组件 → 路径 → 播放」顺序逐项探测，并在每一环节给出针对性修复提示。
# 用途：视频不出画面时，先跑这个，把输出整段贴出来即可定位。
#
set -uo pipefail
cd "$(dirname "$0")"

MTX_API="http://127.0.0.1:9997"
BE="http://127.0.0.1:8080"
LAN_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"

hr() { echo "───────────────────────────────────────────────"; }
code() { curl --noproxy '*' --max-time 3 -s -o /dev/null -w '%{http_code}' "$1" 2>/dev/null || echo 000; }

hr
echo " 视频链路自检  宿主机 IP: ${LAN_IP:-（未取到）}"
hr

# ── 1. MediaMTX 控制 API ────────────────────────────────────────────────
echo ""
echo "【1】MediaMTX 控制 API（:9997）"
C=$(code "$MTX_API/v3/paths/list")
BODY=$(curl --noproxy '*' --max-time 3 -s "$MTX_API/v3/paths/list" 2>/dev/null)
echo "    HTTP $C  ${BODY:0:120}"
case "$C" in
  200) echo "    ✅ 可匿名访问" ;;
  401) echo "    ❌ 401 认证被拒 —— 后端无法注册拉流（根因：api 权限被限本机）"
       echo "       修复：deploy/mediamtx/mediamtx.yml 的 authInternalUsers 中"
       echo "       api 条目 ips 必须为 [] （不能是 [\"127.0.0.1\",\"::1\"]，Docker 源 IP 是 172.x）"
       echo "       然后： docker compose restart mediamtx" ;;
  000) echo "    ❌ 无响应 —— 容器未运行或端口未映射"
       echo "       排查： docker compose ps ; docker compose logs mediamtx --tail 50" ;;
  *)   echo "    ⚠️  异常状态码" ;;
esac

# ── 2. 已注册路径 ───────────────────────────────────────────────────────
echo ""
echo "【2】MediaMTX 已注册路径"
NAMES=""
if [ "$C" = "200" ]; then
  NAMES=$(echo "$BODY" | grep -oE '"name":"[^"]*"' | sed 's/"name":"//;s/"$//')
  if [ -z "$NAMES" ]; then
    echo "    （空）后端尚未注册任何拉流路径"
    echo "     → 确认前端已添加视频数据源；或重启后端让适配器重连："
    echo "       docker compose restart monitorall"
  else
    echo "$NAMES" | while read -r n; do
      [ -z "$n" ] && continue
      P=$(curl --noproxy '*' --max-time 3 -s "$MTX_API/v3/paths/get/$n" 2>/dev/null)
      RDY=$(echo "$P" | grep -oE '"ready":(true|false)' | head -1 | sed 's/"ready"://')
      SRC=$(echo "$P" | grep -oE '"source":\{[^}]*' | head -1 | grep -oE '"type":"[^"]*"' | sed 's/"type":"//;s/"//')
      if [ "$RDY" = "true" ]; then
        echo "    ✅ $n    ready=true   source=${SRC:-publisher}"
      else
        echo "    ⚠️  $n    ready=${RDY:-未知}  → MediaMTX 拉不到上游流，见【5】"
      fi
    done
  fi
else
  echo "    （跳过：API 不可用）"
fi

# ── 3. 后端健康 ─────────────────────────────────────────────────────────
echo ""
echo "【3】后端（:8080）"
echo "    /healthz → HTTP $(code "$BE/healthz")"
RZ=$(curl --noproxy '*' --max-time 3 -s "$BE/readyz" 2>/dev/null)
echo "    /readyz  → ${RZ:0:200}"
if echo "$RZ" | grep -q '"mediamtx":true'; then
  echo "    ✅ 后端认为 MediaMTX 就绪"
elif echo "$RZ" | grep -q 'mediamtxError'; then
  echo "    ❌ 后端连不上 MediaMTX（mediamtxError 见上行）—— 同【1】的 401 问题"
else
  echo "    ⚠️  未能判定，检查后端日志： docker compose logs monitorall --tail 50"
fi

# ── 4. 数据源 ───────────────────────────────────────────────────────────
echo ""
echo "【4】数据源（后端视角）"
DS=$(curl --noproxy '*' --max-time 3 -s "$BE/api/v1/datasources" 2>/dev/null)
if [ -z "$DS" ]; then
  echo "    （无响应）后端未就绪"
else
  echo "$DS" | grep -oE '"(name|status|url)":"[^"]*"' | sed 's/"//g' | paste - - - 2>/dev/null | head -10 \
    || echo "    ${DS:0:300}"
fi

# ── 5. 上游 RTMP 源可达性 ───────────────────────────────────────────────
echo ""
echo "【5】上游 RTMP 源可达性"
SRCS=$(echo "$DS" | grep -oE 'rtmp://[^"]*' | sort -u)
if [ -z "$SRCS" ]; then
  echo "    （未从数据源中解析到 rtmp:// 地址）"
else
  for u in $SRCS; do
    HP=$(echo "$u" | sed -E 's#rtmp://([^/]+).*#\1#')
    H=${HP%%:*}; P=${HP##*:}
    if timeout 3 bash -c "echo > /dev/tcp/$H/$P" 2>/dev/null; then
      echo "    ✅ $HP   TCP 可达"
    else
      echo "    ❌ $HP   TCP 不可达 → MediaMTX 永远拉不到流（ready 恒 false）"
      echo "       排查：机器人/相机是否开机、是否在同网段、防火墙是否放行 $P"
    fi
  done
fi

# ── 6. 播放地址 ─────────────────────────────────────────────────────────
echo ""
echo "【6】播放地址连通性（浏览器实际访问的那条）"
CFG_HOST=$(grep -E '^[[:space:]]*publicHost:' config.yaml 2>/dev/null | head -1 | sed -E 's/.*"([^"]*)".*/\1/')
HOST=${CFG_HOST:-$LAN_IP}
if [ -n "$NAMES" ]; then
  echo "$NAMES" | while read -r n; do
    [ -z "$n" ] && continue
    U="http://$HOST:8888/$n/index.m3u8"
    echo "    HLS   $U → HTTP $(code "$U")"
  done
else
  echo "    （无路径可测）"
fi
echo "    提示：publicHost=${CFG_HOST:-（未设置）}，浏览器需用 http://${HOST}:8080 打开平台；"
echo "          若 publicHost 不是本机 IP，播放地址会指向浏览器不可达的主机。"

hr
echo " 判读顺序：①API 200 → ②路径出现 → ②ready=true → ⑥HLS 200 → 浏览器出画面"
echo " 卡在哪一步，就修哪一步；纯 HTTP 下 WebRTC 不可用，卡片首选协议请选 HLS。"
hr
