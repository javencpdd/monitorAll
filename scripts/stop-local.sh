#!/usr/bin/env bash
#
# 停止裸跑的 MonitorAll（配套 scripts/start-local.sh）
#
#   ./scripts/stop-local.sh
#
# 只停止本仓库目录下的 MediaMTX 与 monitorall 进程，不影响其他实例。
#
set -uo pipefail
cd "$(dirname "$0")/.."
ROOT="$(pwd)"

stop() { # $1=说明 $2=pkill 匹配串
  PIDS=$(pgrep -f "$2" 2>/dev/null | tr '\n' ' ')
  if [ -n "${PIDS// /}" ]; then
    kill $PIDS 2>/dev/null
    sleep 1
    kill -9 $PIDS 2>/dev/null   # 兜底：仍未退出则强杀
    echo "    已停止 $1 (pid: $PIDS)"
  else
    echo "    $1 未在运行"
  fi
}

echo "───────────────────────────────────────────────"
echo " 停止裸跑实例（根目录: $ROOT）"
echo "───────────────────────────────────────────────"
stop "后端 monitorall" "$ROOT/server/bin/monitorall --config"
stop "MediaMTX"        "$ROOT/mediamtx/mediamtx $ROOT/mediamtx/mediamtx.yml"
echo ""
echo " 残留进程检查："
pgrep -af 'monitorall|mediamtx' 2>/dev/null | grep -v pgrep || echo "    （无）"
echo ""
echo " 说明：数据不丢失 —— SQLite(${ROOT}/data/monitorall.db)、密钥、证书均保留。"
echo " 重新启动： pkill -f monitorall ; ./scripts/start-local.sh"
echo "───────────────────────────────────────────────"
