#!/usr/bin/env bash
#
# MonitorAll 一键关闭（Docker Compose）
#   ./stop.sh       停止并移除容器（保留数据：数据库 / 密钥 / 证书）
#   ./stop.sh -v    连同数据一起清空（危险，需二次确认）
#
set -euo pipefail
cd "$(dirname "$0")"

command -v docker >/dev/null 2>&1 || { echo "❌ 未找到 docker。" >&2; exit 1; }
if docker compose version >/dev/null 2>&1; then
  DC="docker compose"
elif command -v docker-compose >/dev/null 2>&1; then
  DC="docker-compose"
else
  echo "❌ 未找到 docker compose。" >&2; exit 1
fi

PURGE=0
for arg in "$@"; do
  case "$arg" in
    -v|--volumes) PURGE=1 ;;
    -h|--help) sed -n '2,5p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "未知参数: $arg" >&2; exit 1 ;;
  esac
done

if [ "$PURGE" = "1" ]; then
  echo "⚠️  将删除容器【并清空全部数据】（数据库 / 密钥 / 证书 / 看板配置）。"
  read -r -p "确认请输入 yes： " ans
  if [ "$ans" != "yes" ]; then echo "已取消。"; exit 0; fi
  echo "==> 停止并清空数据…"
  $DC down -v
else
  echo "==> 停止并移除容器（数据保留在 ./data）…"
  $DC down
fi

echo ""
echo "==> 剩余容器："
$DC ps
echo ""
echo "已关闭。重新启动： ./start.sh"
