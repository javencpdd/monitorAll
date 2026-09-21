#!/usr/bin/env bash
#
# MonitorAll 裸跑启动（不使用 Docker）
#
#   ./scripts/start-local.sh         启动 MediaMTX + 后端
#   ./scripts/start-local.sh --build 先编译后端二进制再启动
#   ./scripts/stop-local.sh          停止（配套）
#
# 组成：
#   1) MediaMTX —— 独立进程，负责拉流转封装（RTMP→HLS/WebRTC），监听 1935/8888/8889/9997
#   2) monitorall —— Go 单二进制，内置前端（go:embed）与 SQLite，监听 8080/8443
#   两者通过 MediaMTX 的 :9997 HTTP API 协作，本机直连用 127.0.0.1 即可。
#
set -uo pipefail
cd "$(dirname "$0")/.."
ROOT="$(pwd)"

BUILD=0
for a in "$@"; do case "$a" in --build) BUILD=1 ;; esac; done

# 可通过环境变量覆盖路径
MTX_BIN="${MTX_BIN:-$ROOT/mediamtx/mediamtx}"
MTX_CONF="${MTX_CONF:-$ROOT/mediamtx/mediamtx.yml}"
BE_BIN="${BE_BIN:-$ROOT/server/bin/monitorall}"
BE_CONF="${BE_CONF:-$ROOT/server/config.yaml}"
LOG_DIR="$ROOT/logs"
mkdir -p "$LOG_DIR"

hr() { echo "───────────────────────────────────────────────"; }
port_busy() { timeout 2 bash -c "echo > /dev/tcp/127.0.0.1/$1" 2>/dev/null; }
# 后台脱离启动：$1=日志文件，其余为命令。
# setsid 使其脱离当前进程组/会话，父 shell 退出也不会被带走。
spawn() {
  local log="$1"; shift
  if command -v setsid >/dev/null 2>&1; then setsid nohup "$@" >> "$log" 2>&1 &
  else nohup "$@" >> "$log" 2>&1 & fi
}

hr
echo " MonitorAll 裸跑启动（无 Docker）"
echo " 根目录: $ROOT"
hr

# ── 0. 端口预检 ──────────────────────────────────────────────────────────
# 注意：8888/9997 是 MediaMTX 自己的端口，被占用【不等于冲突】——
# 只要 :9997 能正常应答，说明上一轮的 MediaMTX 还在跑，直接复用即可
#（用 pkill -f monitorall 只杀后端时就会出现这种情况）。
echo ""
echo "【0】端口预检"
MTX_ALIVE=0
if port_busy 9997; then
  if curl --noproxy '*' --max-time 2 -sf http://127.0.0.1:9997/v3/paths/list >/dev/null 2>&1; then
    MTX_ALIVE=1
    echo "    ℹ️  MediaMTX 已在运行（:9997 就绪），本轮将复用，不再重复启动"
  else
    echo "    ⚠️  :9997 被占用但无应答，可能是别的进程占用"
  fi
fi
if port_busy 8080; then
  echo "    ⚠️  :8080 已被占用（后端端口）—— 旧后端可能还在运行"
  echo "       先停掉： pkill -f 'bin/monitorall'   或改用端口：MONITORALL_SERVER_HTTPADDR=:9080"
  read -r -t 10 -p "    是否仍要继续启动？[y/N] " ANS
  case "${ANS:-N}" in [yY]*) ;; *) echo "已取消。"; exit 1 ;; esac
else
  echo "    ✅ :8080 空闲"
fi

# ── 1. 二进制检查 / 构建 ────────────────────────────────────────────────
echo ""
echo "【1】后端二进制"
if [ ! -x "$BE_BIN" ] || [ "$BUILD" = "1" ]; then
  echo "    编译中（cd server && make build，需 Go 1.22+）…"
  export PATH="$PATH:/usr/local/go/bin:$HOME/go/bin"
  export GOTMPDIR="${GOTMPDIR:-$HOME/.tmp}"
  mkdir -p "$GOTMPDIR"
  ( cd "$ROOT/server" && make build ) || { echo "    ❌ 编译失败，见上方错误"; exit 1; }
fi
if [ ! -x "$BE_BIN" ]; then echo "    ❌ 未找到 $BE_BIN"; exit 1; fi
echo "    ✅ $BE_BIN"

# ── 2. 启动 MediaMTX ────────────────────────────────────────────────────
echo ""
echo "【2】MediaMTX"
if port_busy 9997; then
  echo "    已在运行（:9997 可达），跳过启动"
else
  if [ ! -x "$MTX_BIN" ]; then echo "    ❌ 未找到 MediaMTX 二进制: $MTX_BIN"; echo "       可从 https://github.com/bluenviron/mediamtx/releases 下载"; exit 1; fi
  [ -f "$MTX_CONF" ] || { echo "    ❌ 未找到配置: $MTX_CONF"; exit 1; }
  spawn "$LOG_DIR/mediamtx.log" "$MTX_BIN" "$MTX_CONF"
  echo "    已启动，日志: $LOG_DIR/mediamtx.log"
fi
OK=0
for _ in $(seq 1 20); do
  C=$(curl --noproxy '*' --max-time 2 -s -o /dev/null -w '%{http_code}' http://127.0.0.1:9997/v3/paths/list 2>/dev/null || echo 000)
  if [ "$C" = "200" ]; then OK=1; break; fi
  if [ "$C" = "401" ]; then
    echo "    ❌ API 返回 401：配置文件需满足其一——"
    echo "       · api: true（不能是 false）；"
    echo "       · api 权限的 ips 需含 127.0.0.1（裸跑后端从本机访问）。"
    echo "       配置: $MTX_CONF"
    break
  fi
  sleep 1
done
[ "$OK" = "1" ] && echo "    ✅ API :9997 就绪" || echo "    ⚠️  API 未就绪，查看 $LOG_DIR/mediamtx.log"

# ── 3. 启动后端 ─────────────────────────────────────────────────────────
echo ""
echo "【3】后端 monitorall"
cd "$ROOT"            # 关键：./data（SQLite/密钥/证书）与 ./config.yaml 都相对当前目录解析
spawn "$LOG_DIR/monitorall.log" "$BE_BIN" --config "$BE_CONF"
BE_PID=$!
echo "    pid=$BE_PID  日志: $LOG_DIR/monitorall.log"
OK=0
for _ in $(seq 1 30); do
  if curl --noproxy '*' --max-time 2 -sf http://127.0.0.1:8080/healthz >/dev/null 2>&1; then OK=1; break; fi
  sleep 1
done
if [ "$OK" = "1" ]; then echo "    ✅ 后端 :8080 就绪"; else echo "    ⚠️  未就绪，查看 $LOG_DIR/monitorall.log"; fi

# ── 4. 汇总 ─────────────────────────────────────────────────────────────
LAN_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
echo ""
hr
echo " 访问地址（浏览器）："
echo "   平台    http://${LAN_IP}:8080"
echo "   HLS 播放 http://${LAN_IP}:8888/<path>/index.m3u8"
echo ""
echo " 下一步："
echo "   1) 平台里添加数据源（视频源填 rtmp://<相机IP>:<端口>/live/<流名>）"
echo "   2) 卡片首选协议选 HLS（纯 HTTP 下 WebRTC 不可用）"
echo "   3) 自检： cd deploy && ./diag.sh"
echo "   停止： ./scripts/stop-local.sh"
hr
