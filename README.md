# MonitorAll — 多源异构监控看板平台

一个**局域网内**的卡片式监控看板：统一接入 RTMP 视频流、ROS1/ROS2 话题、HTTP 接口数据，用**拖拽布局 + 可选渲染方式**自由编排成监控大屏。

## 一句话

> 让四足机器人 / 工业设备的多源异构数据（视频、ROS 话题、HTTP 接口），在局域网内以「拖拽即得的卡片看板」零配置可视化出来。

## 能力矩阵

| 数据源 | 说明 |
|---|---|
| 视频流 | 输入 RTMP / RTSP（`rtmp://172.31.68.227:1936/live/lite3`、`rtsp://user:pass@192.168.1.64:554/…`），MediaMTX 转封装为 **WebRTC（亚秒级）**，不可用时自动降级 **HLS（1–3s）**；另支持 **publish 模式**（接收远端 WHIP / RTMP 推流，创建后回显推流地址）。拉不到画面时卡片直接展示**诊断原因**（拉流未就绪 / 等待推流 / 管理 API 不通），不再只有一句通用文案 |
| ROS1 / ROS2 | 经 rosbridge WebSocket 统一代理，运行时切换版本，配置体验一致 |
| HTTP 接口 | 定时轮询 + JSONPath 提取，规避跨域，统一鉴权与缓存 |
| 离线回放 | 上传 `.json` / `.jsonl` 记录文件（≤200MB）建回放数据源，之后与普通数据源一样建卡订阅 |

| 渲染器（P0） | 说明 |
|---|---|
| 原始数据 | JSON 树，可折叠 / 类型标注 / 深度控制 |
| 表格 | 自动拍平、列选择、排序 |
| 仪表 Gauge | 量程 / 单位 / 红黄绿阈值区段 |
| XY 折线图 | 多字段映射、时间窗口、CSV 导出 |
| 图像 | ROS Image / 网络图片，时间戳水印 |
| 地图 | 高德 JS API（GCJ-02），轨迹 + 位姿 Marker；底图可切 **标准 / 暗色 / 卫星**（卫星走官方瓦片图层，非 mapStyle） |
| 视频播放器 | WebRTC→HLS 降级、协议指示 |

- **卡片式布局**：`grid-layout-plus` 拖拽 / 缩放 / 网格吸附 / 布局持久化。
- **地图看板（v3）**：与网格看板并列的独立看板类型——以高德地图为画布挂载节点与浮层卡片，详见下方专节。
- **平台细节**：顶栏天气徽标；品牌 Logo / 站点名在设置抽屉内自定义（存于 `data/branding/`）。
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
├── docs/                     # 开发文档 + 操作手册 + v3 计划书/测试计划
│   ├── DEVELOPMENT.md        # 开发文档（数据契约、协议、API、配置全表）
│   ├── USER_GUIDE.md         # 操作手册（部署、接入数据源、编排看板、FAQ）
│   ├── 系统第三版计划书.md   # v3 地图看板（MapBoard）设计与被否方案
│   └── v3-mapboard-test-plan.md  # MapBoard 验收用例
├── note/                     # 踩坑笔记 01–07（环境/后端/前端/地图/ROS/调试/视频）+ README 索引
├── web/                      # Vue3 前端（构建产物 → server/internal/web/dist）
├── server/                   # Go 后端（cmd / internal / config.example.yaml / Makefile）
├── mediamtx/                 # MediaMTX 二进制（Linux x86_64）+ mediamtx.yml
├── deploy/                   # Dockerfile、docker-compose.yml、config.yaml、mediamtx-default.yml
│   ├── start.sh / stop.sh    # Docker 一键启停 + 就绪检查
│   └── diag.sh               # 视频链路自检（API/路径/播放地址逐项判定）
├── scripts/
│   ├── start-local.sh / stop-local.sh   # 裸跑（无 Docker）一键启停
│   ├── smoke.sh              # 冒烟自测
│   ├── verify-rtsp.sh        # RTSP / publish 端到端验证（自包含：起服务→验证→清理）
│   └── ws-keepalive-probe.mjs # WS 保活实证（连上→心跳→观测存活时长）
├── data/                     # 运行时数据（SQLite / 密钥 / 证书 / branding，均 gitignore）
└── README.md
```

## 首次 clone 后先做什么

### 环境要求

| 部署方式 | 必需 | 说明 |
|---|---|---|
| **裸跑（本地进程，推荐开发用）** | Go **≥ 1.22**（`server/go.mod`）、MediaMTX | 前端产物**已随仓库提交**（`server/internal/web/dist`），不改前端就**不需要 Node** |
| **改过 `web/` 之后** | 再追加 Node **≥ 20**（`web/package.json` 的 `engines`） | 需 `make build-web` 重新产出前端产物 |
| **Docker Compose（推荐交付用）** | Docker + Compose 插件 | Node/Go 的构建都在**镜像内**完成，宿主机无需安装 |

> 模块下载被网络拦住时（常见报错 `Bad Gateway` / `timeout`），用国内代理：
> `export GOPROXY=https://goproxy.cn,direct`（Dockerfile 中已内置该设置）。

> **MediaMTX 二进制的平台限制**：仓库自带的是 **Linux x86_64** 版本（`mediamtx/mediamtx`，约 55MB）。
> macOS / Windows / ARM 机器请到 [releases](https://github.com/bluenviron/mediamtx/releases) 下载对应版本替换，
> 或直接走 **Docker 方案**（用官方镜像，不依赖本机二进制）。

### 两条路径怎么选

| | 裸跑 | Docker Compose |
|---|---|---|
| 一条命令 | `./scripts/start-local.sh` | `cd deploy && ./start.sh` |
| 构成 | MediaMTX 进程 + `server/bin/monitorall` | 两个容器，自动编排与拉起依赖 |
| 适合 | 开发调试、机器上没有 Docker | 长期运行、团队交付、多台机器复制 |

两种方案提供的**能力完全一致**，区别只在运行形态；但**不能同时跑**（8080 / 8888 / 9997 端口会打架，见下节步骤 0）。

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

# 1. 全新 clone 必做：编译后端单二进制（server/bin/ 不入版本库，必须本地编译）
cd server && make build && cd ..                 # 产物 server/bin/monitorall

# 2. 只有改过 web/ 前端源码时才需要重建前端产物；仓库已自带 dist，其余情况整行跳过
#    cd server && make build-web && cd ..

# 3. 启动（推荐）：一条命令拉起 MediaMTX + 后端，预检端口、等待就绪并打印访问地址
./scripts/start-local.sh                          # 二进制缺失时会自动补做步骤 1

# 4. 停止
./scripts/stop-local.sh
```

<details>
<summary>手工分步版（与上面等价，排障时用）</summary>

```bash
# MediaMTX（独立于后端的转封装进程；二进制见上方「MediaMTX 平台限制」说明）
cd mediamtx && ./mediamtx                         # 前台运行，占用当前终端
#   ⚠️ MediaMTX 按【当前目录】查找 mediamtx.yml，不 cd 也不传配置路径时会以空配置启动
#      （WARN "configuration file not found"），api 可能被关闭 → 后端连不上 :9997
#   后台等价写法： ./mediamtx/mediamtx ./mediamtx/mediamtx.yml &

# 后端（在仓库根目录执行：./data 里的 SQLite / 密钥 / 证书都相对当前目录解析）
./server/bin/monitorall --config server/config.yaml
```

</details>

启动后访问 `http://<本机局域网IP>:8080`（脚本会打印）。验证是否真的起来：

```bash
curl --noproxy '*' http://127.0.0.1:8080/healthz      # 期望 {"ok":true,...}
cd deploy && ./diag.sh                                 # 视频链路逐项自检
```

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
>
> 注意 Docker 方案读的是 **`deploy/config.yaml`**（由 compose 挂载进容器），**不是** `server/config.yaml`——改错文件不会生效。

首次 clone 后同样可以一条命令启动，**宿主机无需 Go / Node**（都在镜像里构建）。

启动后验证：

```bash
docker compose ps                                      # 两个容器都应 healthy/up
curl --noproxy '*' http://127.0.0.1:8080/healthz       # 期望 {"ok":true,...}
./diag.sh                                              # 视频链路逐项自检（诊断首选）
```

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

### 用卡片订阅话题（三步）

1. **建数据源**：新建 → 选「ROS 话题」→ 选 ROS1/ROS2 → 填 `ws://<机器人IP>:9090`（ROS2 填 Domain ID）→ 填话题列表（换行或逗号分隔，如 `/odom`、`/imu/data`）→ 测试连接 → 保存。
2. **自动建通道**：保存后**每个 topic 生成一个 Channel**；自动发现不全时可在通道选择器点「从数据源发现」或「+ 手工新增」。
3. **建卡片**：新建卡片 → 选该 Channel → 渲染器按消息类型自动推荐（NavSatFix→地图、Imu/Odometry→折线图、BatteryState→仪表、Image→图像）→ 卡片挂载即自动订阅并实时刷新。

> **网络要求**：需要的是「**运行后端的主机 → 机器人 rosbridge 的 9090 端口**」TCP 可达。
> 同一局域网是最省事的充分条件（机器人 WiFi 热点直连天然满足），但不是必要条件——异地组网 / VPN 同样可行。
> 浏览器只连后端 `:8080`，**不必**与机器人同网。真正必须同处一个 ROS 图的是 **rosbridge 与话题发布者**（建议同机部署）：
> ROS2 需 `ROS_DOMAIN_ID` 一致（跨网段还需配 `ROS_DISCOVERY_SERVER`），ROS1 需 `ROS_MASTER_URI` 一致。

> **ROS1 注意**：subscribe 报文的 `type` 字段必填，而向导只收话题名，因此 ROS1 依赖 **rosapi 在线**自动补消息类型；
> ROS2 无此要求。ROS2 订阅后无数据是**静默**的，后端以 3s 超时判定并 10s 后重试一次订阅。

> ROS 数据源**不需要配字段路径**：`sensor_msgs/NavSatFix` 会直接归一化成 `geo_pose` 帧，
> 地图卡只需设「输入坐标系」，不必填 latPath/lonPath（这与 JSON / HTTP 通道不同）。

## 高德地图配置（Key / 安全密钥 / WebGL）

地图卡片使用高德 JS API 2.0（国内合规底图，GCJ-02）。在 `server/config.yaml` 配置：

```yaml
web:
  amapKey: "你的高德 Key"
  # 与 Key 一一对应的安全密钥（控制台「应用管理 → 我的应用」里和 Key 同页展示）
  amapSecurityCode: "你的 securityJsCode"
  # 无 GPU / 软件渲染 / 部分手机 WebView 必须开，否则样式切换无效
  amapForceWebGL: true
```

| 配置项 | 作用 | 缺失后果 |
|---|---|---|
| `amapKey` | 高德 JS API Key | 地图卡降级为「配置指引 + 坐标列表」，**不会**换境外底图 |
| `amapSecurityCode` | 2021-12-02 之后申请的 Key 必须配对 | 样式服务静默失败：底图正常，但切暗色等样式无反应 |
| `amapForceWebGL` | 关闭 WebGL 性能保护、强制启用 WebGL 绘制 | 无 GPU/手机 WebView 下高德报"浏览器版本过低"且不渲染样式 |

> **未配置 Key 时**，地图卡显示配置指引 + 纯坐标/轨迹列表，**不会**降级到境外底图（合规硬约束）。
> 坐标系转换在前端 `gcoord` 离线完成（WGS-84 → GCJ-02），后端只盖章 `crs` 不改坐标值。

### 底图样式切换不生效？按顺序排查

| 排查项 | 说明 |
|---|---|
| 样式值是否合法 | 官方内置样式只有 normal / dark / light / whitesmoke / fresh / grey / graffiti / macaron / blue / darkblue / wine。**卫星不是 mapStyle**，本平台用官方 `TileLayer.Satellite` 图层实现 |
| 是否配了安全密钥 | 见上表 `amapSecurityCode` |
| 是否启用 WebGL | 见上表 `amapForceWebGL`；控制台出现"浏览器版本过低"基本就是它 |
| 区分「没调用」还是「没渲染」 | 控制台执行 `__maMap.getMapStyle()`：有值说明前端已调用成功、是高德侧没画出来；`undefined` 才是前端没走到 |

### 配了 Key 还是无图？看横幅上的真实错误

地图卡降级时，横幅会区分两种情况，并给出**真实失败原因**（不再一律提示"去配 Key"）：

- **未配置 Key**：显示配置指引（见上方 yaml）。
- **已配置但加载失败**：显示「高德底图加载失败：{真实错误}」+ 高德控制台链接。常见错误码：

| 错误码 | 含义 | 处理 |
|---|---|---|
| `USERKEY_PLAT_NOMATCH` | Key 类型不对 | 必须是**「Web 端（JS API）」**类型；「Web 服务」Key 只能调 REST 接口，不能加载 JS API |
| `INVALID_USER_SCODE` | Key 与安全密钥不配对 | 两者必须来自控制台**同一个 Key 项** |
| `DOMAIN_NOT_MATCH` | 域名白名单不含当前访问地址 | 把看板访问 host 加进白名单，或清空白名单 |
| `KEY_DISABLE` | Key 被禁用 / 过期 / 配额满 | 控制台启用，或新建 Key |

浏览器 F12 → Console 同步打印 `[amap] 高德 JS API 加载失败：` 及完整错误对象，与横幅二选一即可定位。

### 地图卡的字段路径（JSON / HTTP 通道）

JSON 通道的载荷被后端包成 `{ root: 原始响应 }`，因此字段路径**必须带 `root.` 前缀**或由前端自动补（`data.pose.latitude` 会被自动尝试为 `root.data.pose.latitude`）。路径分隔符 **点分与斜杠均可**（`data.pose.latitude` 与 `data/pose/latitude` 等价）。

## 地图看板（MapBoard · v3）

除「网格看板」外，平台还提供**地图看板**：以一整块高德地图为画布，把已接入的通道挂成**可移动的地图节点**；节点既是位置锚点，也是视频 / 仪表 / 折线 / 图像 / JSON / 表格卡片的宿主。创建入口与网格 Dashboard 并列（看板列表区分 `grid` / `map`），改动自动持久化。

- **节点**：从已有 geo 类通道创建（`sensor_msgs/NavSatFix` 或 JSON 字段路径均可），位置实时驱动；≥3s 无帧自动置灰，恢复数据后解除；支持静态兜底坐标；上限 20 个，超限显式提示。
- **卡片挂载**：点击节点弹出**跟随浮层卡片**，复用现有 7 类渲染器（`map` 渲染器禁止绑节点，防止嵌套第二张地图）；非视频软上限 6、视频硬上限 4，超限暂停占位、可一键恢复。
- **跟随与排版**：卡片默认跟随节点移动；可拖拽脱钩、一键「回到跟随」、缩放（视频锁 16:9）；节点移出视口时自动隐藏卡片并断流，回到视野快速恢复——**全程 WS 订阅集合不变**。
- **地图与合规**：单看板只有**一个** AMap 实例（节点用自研 Web Mercator 投影器以 DOM 叠加，不建 Marker 对象）；底图样式可切（标准 / 暗色 / 卫星图层）；**未配置 Key 时降级为自研虚拟投影器**，核心功能仍可用，且绝不请求境外底图。
- **可观测**：Key 缺失、字段路径取不到值、持久化失败、节点 / 可见卡 / 视频路计数都有可见出口；诊断 HUD 可开关。

> 设计取舍（含被否方案）见 `docs/系统第三版计划书.md`，验收用例见 `docs/v3-mapboard-test-plan.md`。

## 视频降级说明（重要）

MediaMTX **官方不支持 HTTP-FLV**（issue #1460），因此默认降级路径为 **HLS（`hlsVariant: mpegts`）+ hls.js**（1–3s 延迟）。若你已有外部 FLV 网关（SRS / ZLMediaKit / nginx-http-flv），在 `config.yaml` 配置：

```yaml
mediamtx:
  flvBase: "http://172.31.68.9:8081/live"   # 指向 FLV 网关的 app 根地址
```

配置后视频播放器会启用 FLV 后端（`mpegts.js`），无需改代码。

补充三条：

- **RTSP 源**：默认走 **TCP** 传输（UDP 易花屏 / 连不上），新建数据源时可切换；地址支持 `rtsp://user:pass@host:554/…`，凭据会完整保留。
- **publish 模式**（接收远端推流）：不必填可拉的源地址，填 MediaMTX 路径即可；创建成功后页面会**回显 WHIP 与 RTMP 推流地址**供复制。
- **MediaMTX 路径**：只填**流名**（如 `live/m20pro-f`），填完整地址（`rtmp://…`）会被后端当场拦截（40001）并提示正确写法。路径是流的唯一身份：同路径 + 同上游放行（同一路相机在多看板复用），同路径 + 不同上游会被拦截并点名冲突数据源。
- **失败态诊断**：拉不到画面时卡片会说明具体原因（拉流未就绪 / 等待推流 / MediaMTX 管理 API 不通），publish 模式直接给出可复制的推流地址；排查首选 `deploy/diag.sh`。

## 测试

```bash
cd server
make vet     # go vet
make test    # go test（含 Frame 归一化 / 引用计数 / 限流 / 退避 / 存储 / 加密 / 坐标合规）
make smoke   # 冒烟自测（启动 → 探活 → 默认看板 → 前端产物）
```

前端另有 **19 条源码级回归守卫**——守的都是 `vue-tsc` 抓不到的运行期 / 静默失败缺陷（改回旧写法必红）：

```bash
cd web
npm run typecheck   # vue-tsc --noEmit
npm test            # source-guards.mjs
```

## 合规红线（地图）

1. 底图仅用高德 JS API，禁止 Google Maps / 未审校 OSM 等境外底图。
2. 坐标系 GCJ-02，WGS-84 → GCJ-02 在前端 `gcoord` 离线完成。
3. 界面显式声明输入坐标系，涉及行政边界/国界仅用国内合规数据源。

## 更多

- **操作手册**：`docs/USER_GUIDE.md`（部署、接入数据源、编排看板、渲染器参考、FAQ 排查）
- **开发文档**：`docs/DEVELOPMENT.md`（仓库结构、数据模型与 TS/Go 契约对照、WS 协议、REST API 与错误码、渲染器与适配器扩展、配置全表）
- **v3 地图看板**：`docs/系统第三版计划书.md`（设计与被否方案）、`docs/v3-mapboard-test-plan.md`（验收用例）
- **踩坑笔记**：`note/`（7 篇，按「现象 → 根因 → 修复 → 教训」组织）

> 早期的过程性文档（PRD / 架构设计 / 任务分解）已从仓库移除：它们写于开发之前，多处结论已被实测推翻
> （如 HTTP-FLV 兜底、状态枚举、目录结构）。需要找回来可 `git checkout <commit> -- docs/PRD.md`。
