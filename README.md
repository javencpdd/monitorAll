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
├── scripts/smoke.sh      # 冒烟自测
└── README.md
```

## 快速开始（本地）

前置：Go 1.22+、Node 18+（仅构建前端时需要）。

```bash
# 1. 构建前端并嵌入后端（首次或前端改动后执行）
cd server && make build-web

# 2. 编译单二进制
make build          # 产物在 server/bin/monitorall

# 3. 启动（默认 HTTP :8080 / HTTPS :8443，自签证书）
./bin/monitorall
```

启动后按横幅打印的 LAN 地址访问（如 `http://10.42.0.1:8080`），同一局域网内其它电脑直接打开即可。

> 首次以 HTTPS 访问前，先访问 `/trust` 信任自签证书（否则浏览器会拦截 WebRTC / HLS）。

## 快速开始（Docker Compose，推荐）

```bash
cd deploy
docker compose up -d --build
```

自动拉起 `monitorall`（含前端）+ `mediamtx` 两个容器。访问 `http://<主机IP>:8080`。

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

- 需求文档：`docs/PRD.md`
- 架构设计：`docs/ARCHITECTURE.md`（含统一 Frame 契约、WS 协议、REST API、错误码）
- 任务分解：`docs/TASKS.md`
