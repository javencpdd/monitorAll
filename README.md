# MonitorAll — 多源异构监控看板平台

一个**局域网内**的卡片式监控看板：统一接入 RTMP 视频流、ROS1/ROS2 话题、HTTP 接口数据，用**拖拽布局 + 可选渲染方式**自由编排成监控大屏。

## 一句话

> 让四足机器人 / 工业设备的多源异构数据（视频、ROS 话题、HTTP 接口），在局域网内以「拖拽即得的卡片看板」零配置可视化出来。

## 能力矩阵

| 数据源 | 说明 |
|---|---|
| 视频流 | 输入 RTMP（如 `rtmp://172.31.68.227:1936/live/lite3`），MediaMTX 转封装为 **WebRTC（亚秒级）**，不可用时自动降级 **HLS（1–3s）** |
| ROS1 / ROS2 | 经 rosbridge WebSocket 统一代理，运行时切换版本，配置体验一致 |
| HTTP 接口 | 定时轮询 + JSONPath 提取，规避跨域，统一鉴权与缓存 |

| 渲染器（P0） | 说明 |
|---|---|
| 原始数据 | JSON 树，可折叠 / 类型标注 / 深度控制 |
| 表格 | 自动拍平、列选择、排序 |
| 仪表 Gauge | 量程 / 单位 / 红黄绿阈值区段 |
| XY 折线图 | 多字段映射、时间窗口、CSV 导出 |
| 图像 | ROS Image / 网络图片，时间戳水印 |
| 地图 | 高德 JS API（GCJ-02），轨迹 + 位姿 Marker |
| 视频播放器 | WebRTC→HLS 降级、协议指示 |

- **卡片式布局**：`grid-layout-plus` 拖拽 / 缩放 / 网格吸附 / 布局持久化。
- **稳定性**：四态可视化（在线/离线/重连中/错误）、指数退避重连、单卡片错误不白屏、前端 WS 断线自动恢复。

## 技术栈

| 层 | 选型 |
|---|---|
| 前端 | Vue 3 + Vite + TypeScript + Naive UI + Pinia + grid-layout-plus + ECharts + hls.js + gcoord + 高德 JS API 2.0 |
| 后端 | Go（Gin + gorilla/websocket + modernc.org/sqlite 纯 Go 免 CGO） |
| 流媒体 | MediaMTX（RTMP → WebRTC / HLS） |
| 部署 | 单二进制（go:embed 前端产物）/ Docker Compose |

## 目录结构

```
monitorAll/
├── docs/                 # PRD、架构设计、任务分解
├── web/                  # Vue3 前端（构建产物 → server/internal/web/dist）
├── server/               # Go 后端（cmd / internal / config / Makefile）
├── deploy/               # Dockerfile、docker-compose.yml、mediamtx 配置
│   ├── start.sh / stop.sh    # Docker 一键启停 + 就绪检查
│   └── diag.sh               # 视频链路自检（API/路径/播放地址逐项判定）
├── scripts/
│   ├── start-local.sh / stop-local.sh   # 裸跑（无 Docker）一键启停
│   └── smoke.sh          # 冒烟自测
└── README.md
```

## 快速开始（裸跑 · 不使用 Docker）

裸跑是**两个独立进程**，通过 MediaMTX 的 `:9997` HTTP API 协作：

```
浏览器 ──:8080──> monitorall（单二进制，含前端 + SQLite）
                      │ 控制 API（:9997，注册拉流路径）
                      ▼
                  MediaMTX ──拉流──> rtmp://相机:1935/live/xxx
浏览器 ──:8888──> MediaMTX（HLS 播放地址，画面不经后端转发）
```

| 进程 | 端口 | 作用 |
|---|---|---|
| `mediamtx` | 1935(RTMP) / 8888(HLS) / 8889(WebRTC) / 9997(API) | 拉流 + 转封装 |
| `monitorall` | 8080(HTTP) / 8443(HTTPS) | 平台 + 前端 + 数据编排 |

### 流程

```bash
# 0. 若 Docker 栈正在运行，先停掉（否则 8080/8888/9997 端口冲突）
cd deploy && ./stop.sh && cd ..

# 1. 启动 MediaMTX（转封装进程，独立于后端）
#    下载：https://github.com/bluenviron/mediamtx/releases（本仓库 mediamtx/ 已自带）
cd ~/monitorAll/mediamtx && ./mediamtx          # 前台运行，会占用当前终端
# 等价后台写法（不 cd 进去时【必须】显式传配置路径）：
#   nohup ~/monitorAll/mediamtx/mediamtx ~/monitorAll/mediamtx/mediamtx.yml &
# ⚠️ MediaMTX 按【当前目录】查找配置文件（mediamtx.yml → 系统路径）。不 cd 也不传路径时
#    会 WARN "configuration file not found ... using an empty configuration" 并以空配置启动，
#    api 可能被关闭 → 后端连不上 :9997。

# 2. 构建前端并嵌入（首次，或改过 web/ 之后；已构建过可跳过）
cd server && make build-web && cd ..

# 3. 编译后端单二进制
cd server && make build && cd ..                # 产物 server/bin/monitorall

# 4. 启动后端（读取 server/config.yaml；./data 相对当前目录，故在仓库根目录执行）
./server/bin/monitorall --config server/config.yaml
#   想一条命令搞定 1+4：./scripts/start-local.sh（自动起 MediaMTX、预检端口、等待就绪）

# 5. 停止
./scripts/stop-local.sh                          # 或手工 kill 两个进程
```

启动后访问 `http://<本机局域网IP>:8080`（脚本会打印）。

### 裸跑配置要点（`server/config.yaml`）

| 配置项 | 裸跑取值 | 说明 |
|---|---|---|
| `mediamtx.apiBase` | `http://127.0.0.1:9997` | **不能写 compose 服务名 `mediamtx`**——裸跑无 Docker DNS，会报 `lookup mediamtx: server misbehaving` |
| `mediamtx.portRtmp` | `1935` | 裸跑 MediaMTX 默认 RTMP 端口（Docker 方案才是 1936） |
| `mediamtx.publicHost` | 本机局域网 IP（留空则自动探测） | 浏览器靠它拼 HLS 播放地址，留错会拉不到画面 |
| `mediamtx.publicScheme` | `http` | 与 MediaMTX 是否加密保持一致 |
| `mediamtx.encryption` | `never` | 纯 HTTP 场景下关闭，否则 HLS/WHEP 会被证书卡住 |
| `server.lanHost` | 本机局域网 IP（留空自动探测） | 横幅与证书 SAN 用它 |

> MediaMTX 侧配置（`mediamtx/mediamtx.yml`）必须 `api: true`；且 `api` 权限的 `ips` 要包含 `127.0.0.1`（裸跑后端从本机访问）。
> 若出现 `401 authentication error`，就是这两项之一没满足——详见 `deploy/diag.sh` 的输出提示。

> 首次以 HTTPS 访问前，先访问 `/trust` 信任自签证书（否则浏览器会拦截 WebRTC / HLS）。
> 纯 HTTP 下 **WebRTC 不可用**，视频卡片首选协议请选 **HLS**。

## 快速开始（Docker Compose，推荐）

```bash
cd deploy
./start.sh          # 一键：构建镜像 + 启动 + 就绪检查 + 打印访问地址
./stop.sh           # 一键停止（保留数据）；./stop.sh -v 连数据一起清空
```

脚本会自动拉起 `monitorall`（含前端）+ `mediamtx` 两个容器，并校验 `config.yaml` 里的 IP 是否匹配宿主机。

> **⚠️ 首次部署必做**：编辑 `deploy/config.yaml`，把 `server.lanHost` 和 `mediamtx.publicHost` 改成**你宿主机访问看板用的局域网 IP**（`hostname -I` 查看）。
> 留空会退化成 `127.0.0.1` 或容器内网 IP，浏览器将拉不到视频。

### 改动后要不要重建镜像？

| 改了什么 | 命令 | 原因 |
|---|---|---|
| `web/**` 或 `server/**` 代码 | `./start.sh` | 代码在**镜像构建阶段**编译/打包，必须重建 |
| `deploy/config.yaml`、`mediamtx/mediamtx.yml` | `./start.sh --no-build` | 只读挂载，重启容器即重新加载，无需重建 |
| `deploy/docker-compose.yml` | `./start.sh` | compose 需重新创建容器 |

> 只改配置时**务必用 `--no-build`**（或 `docker compose restart`）。直接 `docker compose up -d` 对已运行容器不会重启，配置改动不会生效。
> 改了前端却只重启不重建，界面会停留在旧版本——这是最容易踩的坑。

## ROS 接入前置条件

本平台通过 **rosbridge WebSocket** 接入 ROS（版本无关、开发快）：

```bash
# ROS1 (Noetic/Melodic)
sudo apt install ros-noetic-rosbridge-suite
roslaunch rosbridge_server rosbridge_websocket.launch   # 默认 ws://<host>:9090

# ROS2 (Humble/Foxy)
sudo apt install ros-humble-rosbridge-suite
ros2 launch rosbridge_server rosbridge_websocket_launch.xml
```

> MVP 主路径是**手工填写 topic**（ROS2 各发行版 rosapi 服务名不一致，话题下拉为软增强，失败静默降级）。
> 图像话题限流 10Hz + JPEG 压缩（rosbridge 侧 `compression: jpeg`），从源头降低 JSON 序列化压力。

## 高德地图 Key

地图卡片使用高德 JS API 2.0（国内合规底图，GCJ-02）。在 `server/config.yaml` 配置：

```yaml
web:
  amapKey: "你的高德 Key"
```

> **未配置 Key 时**，地图卡显示配置指引 + 纯坐标/轨迹列表，**不会**降级到境外底图（合规硬约束）。
> 坐标系转换在前端 `gcoord` 离线完成（WGS-84 → GCJ-02），后端只盖章 `crs` 不改坐标值。

## 视频降级说明（重要）

MediaMTX **官方不支持 HTTP-FLV**（issue #1460），因此默认降级路径为 **HLS（`hlsVariant: mpegts`）+ hls.js**（1–3s 延迟）。若你已有外部 FLV 网关（SRS / ZLMediaKit / nginx-http-flv），在 `config.yaml` 配置：

```yaml
mediamtx:
  flvBase: "http://172.31.68.9:8081/live"   # 指向 FLV 网关的 app 根地址
```

配置后视频播放器会启用 FLV 后端（`mpegts.js`），无需改代码。

## 测试

```bash
cd server
make vet     # go vet
make test    # go test（含 Frame 归一化 / 引用计数 / 限流 / 退避 / 存储 / 加密 / 坐标合规）
make smoke   # 冒烟自测（启动 → 探活 → 默认看板 → 前端产物）
```

## 合规红线（地图）

1. 底图仅用高德 JS API，禁止 Google Maps / 未审校 OSM 等境外底图。
2. 坐标系 GCJ-02，WGS-84 → GCJ-02 在前端 `gcoord` 离线完成。
3. 界面显式声明输入坐标系，涉及行政边界/国界仅用国内合规数据源。

## 更多

- **操作手册**：`docs/USER_GUIDE.md`（部署、接入数据源、编排看板、渲染器参考、FAQ 排查）
- 需求文档：`docs/PRD.md`
- 架构设计：`docs/ARCHITECTURE.md`（含统一 Frame 契约、WS 协议、REST API、错误码）
- 任务分解：`docs/TASKS.md`
