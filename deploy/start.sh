#!/usr/bin/env bash
#
# MonitorAll 一键启动（Docker Compose）
#   ./start.sh          构建镜像 + 启动，并做就绪检查与地址提示
#   ./start.sh --no-build  跳过镜像构建（仅重启容器，改配置时更快）
#
set -euo pipefail
cd "$(dirname "$0")"

BUILD=1
for arg in "$@"; do
  case "$arg" in
    --no-build) BUILD=0 ;;
    -h|--help) sed -n '2,6p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "未知参数: $arg" >&2; exit 1 ;;
  esac
done

# ——— 前置检查 ———
command -v docker >/dev/null 2>&1 || { echo "❌ 未找到 docker，请先安装 Docker。" >&2; exit 1; }
if docker compose version >/dev/null 2>&1; then
  DC="docker compose"
elif command -v docker-compose >/dev/null 2>&1; then
  DC="docker-compose"
else
  echo "❌ 未找到 docker compose（v1/v2 均不可用）。" >&2; exit 1
fi
docker info >/dev/null 2>&1 || { echo "❌ Docker daemon 不可用（未启动或当前用户无权限）。" >&2; exit 1; }

[ -f config.yaml ] || { echo "❌ 缺少 config.yaml（当前目录：$(pwd)）" >&2; exit 1; }

# ——— 宿主机 IP 与配置比对（最常见的「视频不出画面」原因）———
LAN_IPS="$(hostname -I 2>/dev/null | tr ' ' '\n' | grep -vE '^(127\.|172\.1[6-9]\.|172\.2[0-9]\.|172\.3[01]\.)' | grep -v '^$' || true)"
CONFIG_HOST="$(grep -E '^[[:space:]]*publicHost:' config.yaml | head -1 | sed -E 's/.*"([^"]*)".*/\1/' || true)"

echo "───────────────────────────────────────────────"
echo " 宿主机可用 IP : $(echo "$LAN_IPS" | tr '\n' ' ')"
echo " 配置 publicHost: ${CONFIG_HOST:-（未设置）}"
if [ -n "${CONFIG_HOST:-}" ] && ! echo "$LAN_IPS" | grep -qx "$CONFIG_HOST"; then
  echo ""
  echo " ⚠️  publicHost 不在上面的宿主机 IP 列表里，浏览器将访问不到视频！"
  echo "     请把 config.yaml 的 lanHost 与 publicHost 改成上面列表中的 IP。"
fi
echo "───────────────────────────────────────────────"

# ——— 启动 ———
if [ "$BUILD" = "1" ]; then
  echo "==> 构建镜像并启动（首次较慢，前端产物在构建阶段打包进镜像）…"
  $DC up -d --build
else
  echo "==> 复用镜像重启容器（重新加载 config.yaml / mediamtx.yml）…"
  $DC up -d
  $DC restart
fi

# ——— 就绪检查（curl 绕过代理，避免 http_proxy 干扰本机探活）———
echo "==> 等待后端就绪…"
ready=0
for _ in $(seq 1 40); do
  if curl -sf --noproxy '*' --max-time 2 http://127.0.0.1:8080/healthz >/dev/null 2>&1; then ready=1; break; fi
  sleep 1
done
if [ "$ready" = "1" ]; then
  echo "    ✅ 后端就绪"
else
  echo "    ⚠️  40s 内未就绪，查看日志： $DC logs monitorall --tail 50"
fi

# ——— MediaMTX 检查 ———
# 循环等待（容器初始化可能比后端慢，一次性检查会误报）；
# 同时区分「端口不通」与「401 需要认证」——后者不会自愈，必须改配置。
echo "==> 等待 MediaMTX 就绪…"
mtxReady=0
mtxCode=""
MTX_API="http://127.0.0.1:9997/v3/paths/list"
for _ in $(seq 1 30); do
  mtxCode="$(curl -s --noproxy '*' --max-time 2 -o /dev/null -w '%{http_code}' "$MTX_API" 2>/dev/null || echo 000)"
  if [ "$mtxCode" = "200" ]; then mtxReady=1; break; fi
  # 401 = 认证被拒：重试无用，立刻跳出报错，避免干等 30s
  [ "$mtxCode" = "401" ] && break
  sleep 1
done
if [ "$mtxReady" = "1" ]; then
  echo "    ✅ MediaMTX 就绪（API :9997）"
  # 展示当前已注册的流路径（等几秒让后端注册拉流后再看更有意义）
  sleep 3
  MTX_PATHS="$(curl -sS --noproxy '*' --max-time 2 "$MTX_API" 2>/dev/null \
    | grep -oE '"name":"[^"]*"' | sed 's/"name":"//;s/"$//' | tr '\n' ' ' || true)"
  if [ -n "$MTX_PATHS" ]; then
    echo "    当前流路径：$MTX_PATHS"
    echo "    （若某路径一直不 ready，检查推流端 RTMP 源是否可达）"
  else
    echo "    当前流路径：（空 —— 后端会自动注册拉流，稍等后可再查：）"
    echo "      curl $MTX_API"
  fi
elif [ "$mtxCode" = "401" ]; then
  echo "    ❌ MediaMTX API 返回 401（需要认证）→ 后端无法注册拉流，视频不会出画面"
  echo "       修复：deploy/mediamtx/mediamtx.yml 里的 authInternalUsers 必须显式声明匿名访问，"
  echo "       且 api 权限条目的 ips 不能只写 127.0.0.1 —— Docker 里后端源 IP 是 172.x，"
  echo "       官方默认的 ['127.0.0.1','::1'] 会让所有 API 请求被拒。改为 ips: []。"
  echo "       改完执行： $DC restart mediamtx"
else
  echo "    ⚠️  MediaMTX 未就绪（API :9997 无响应，code=$mtxCode）→ 视频无法播放"
  echo "        查看原因： $DC logs mediamtx --tail 50"
fi

echo ""
echo "==> 容器状态："
$DC ps

echo ""
echo "==> 访问地址（挑浏览器能访问的那个 IP）："
for ip in $LAN_IPS; do
  echo "    http://$ip:8080"
done
echo ""
echo "提示：只改了 config.yaml / mediamtx.yml 时，用 ./start.sh --no-build 更快；"
echo "      改了 web/ 或 server/ 代码后必须重建，直接 ./start.sh。"
