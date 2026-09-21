#!/usr/bin/env bash
# MonitorAll 冒烟自测：启动后端 → 探活 → 校验默认看板与前端产物 → 退出。
# 用法：bash scripts/smoke.sh [二进制路径]
set -euo pipefail

BIN="${1:-./bin/monitorall}"
PORT="${MONITORALL_PORT:-8080}"
BASE="http://127.0.0.1:${PORT}"
WORKDIR="$(mktemp -d)"
LOG="$WORKDIR/monitorall.log"
PID=""

cleanup() {
  if [ -n "$PID" ] && kill -0 "$PID" 2>/dev/null; then
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
  fi
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

if [ ! -x "$BIN" ]; then
  echo "二进制不存在或不可执行: $BIN（先执行 make build）" >&2
  exit 1
fi

echo "==> 启动: $BIN"
"$BIN" --log-level error >"$LOG" 2>&1 &
PID=$!

# 等待就绪（最多 15s）
ready=0
for _ in $(seq 1 30); do
  if curl -sf --max-time 2 "$BASE/healthz" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 0.5
done
if [ "$ready" != "1" ]; then
  echo "服务未在 15s 内就绪，日志如下：" >&2
  cat "$LOG" >&2
  exit 1
fi

echo "==> 检查 /healthz"
curl -sf --max-time 3 "$BASE/healthz" >/dev/null && echo "  healthz OK"

echo "==> 检查默认看板（D8 自动创建）"
curl -sf --max-time 3 "$BASE/api/v1/dashboards" >/dev/null && echo "  默认看板 OK"

echo "==> 检查前端产物（go:embed 生效）"
curl -sf --max-time 3 "$BASE/" | grep -q '<html' && echo "  前端 index.html OK"

echo "==> 冒烟通过 ✅"
