# MonitorAll 操作手册

| 项 | 内容 |
| --- | --- |
| 版本 | **v2.0**（按当前代码重写，结论均核实到 `文件:行号`） |
| 日期 | 2026-09-26 |
| 适用版本 | MonitorAll v1.0.0（schema v1，`server/internal/config/config.go:19` / `:22`） |
| 文档定位 | 面向**使用者与运维者**：怎么装、怎么配、怎么接数据、怎么排障 |
| 配套 | 开发者文档 `docs/DEVELOPMENT.md`（契约、协议、API、配置全表）；踩坑笔记 `note/`（§1.2） |

> 假设读者第一次接触本系统。所有配置键、路径、端口、字段名均可在仓库中按给出的 `文件:行号` 复核。

---

## 目录

1. [文档定位与配套索引](#1-文档定位与配套索引)
2. [快速上手](#2-快速上手)
3. [核心概念](#3-核心概念)
4. [配置详解](#4-配置详解)
5. [四类数据源创建向导](#5-四类数据源创建向导)
6. [卡片与布局](#6-卡片与布局)
7. [视频播放说明](#7-视频播放说明)
8. [运维](#8-运维)
9. [常见问题排查（FAQ）](#9-常见问题排查faq)
10. [附录](#10-附录)

---

## 1. 文档定位与配套索引

### 1.1 三份文档怎么分工

| 文档 | 谁看 | 回答什么 |
| --- | --- | --- |
| **本手册** | 使用者、运维者 | 部署、配置、建源建卡、排障 |
| `docs/DEVELOPMENT.md` | 开发者 | 代码结构、构建、测试、改造注意点 |
| `README.md` | 快速开始与部署速查 |
| `note/` | 踩坑笔记：静默失败、漂移 bug 的排查顺序（§1.2） |

> 早期的过程性文档 `docs/ARCHITECTURE.md`、`docs/PRD.md`、`docs/TASKS.md` 已因与代码脱节而删除，
> 其中仍然有效的内容已并入 `docs/DEVELOPMENT.md`（尤其 §13「已知偏差与坑速查」）。

### 1.2 踩坑笔记 `note/`（"配了但没反应"先看这里）

手册只在对应位置给一句提示 + 链接，不重复展开。笔记记录的是**不报错、只会静默失效**的坑：

| 笔记 | 什么时候读 |
| --- | --- |
| [`../note/01-环境与构建.md`](../note/01-环境与构建.md) | 构建失败、Go/Node 路径、后台进程被回收 |
| [`../note/02-后端与协议.md`](../note/02-后端与协议.md) | MediaMTX 401、配置改了没生效、播放地址回退 |
| [`../note/03-前端框架与布局.md`](../note/03-前端框架与布局.md) | 拖拽缩放失效、界面停在旧版本 |
| [`../note/04-高德地图.md`](../note/04-高德地图.md) | **地图底图/样式不生效必读**（三配置项、四步排查） |
| [`../note/05-ROS接入.md`](../note/05-ROS接入.md) | ROS 订阅链路、ROS1/ROS2 差异、消息类型映射 |
| [`../note/06-调试方法论.md`](../note/06-调试方法论.md) | 无浏览器环境下的取证手段 |
| [`../note/07-视频流接入.md`](../note/07-视频流接入.md) | **接视频/RTSP/摄像头前必读**（pull/publish 判定、降级链） |

> `note/README.md` 记录了两条被实测推翻的旧结论：HTTP-FLV 兜底已被推翻（默认降级目标是 HLS）；`amap://styles/satellite` 是图层不是样式。笔记与旧文档冲突时**以笔记为准**。

---

## 2. 快速上手

### 2.1 选哪条路

| | 路线 A：Docker Compose | 路线 B：裸跑（两个进程） |
| --- | --- | --- |
| 前置 | Docker + Compose 插件 | Go 1.22+（改前端还需 Node 18+）、MediaMTX 二进制 |
| RTMP 端口 | **1936**（compose 映射 + `deploy/config.yaml`） | **1935**（`mediamtx/mediamtx.yml:295` `rtmpAddress: :1935`） |
| 数据库 | `deploy/data/monitorall.db` | 取决于启动目录，见 §4.8 |
| 配置文件 | `deploy/config.yaml` | `server/config.yaml` |
| 适合 | 长期部署 | 开发调试、现场快速验证 |

### 2.2 路线 A：Docker Compose

```bash
cd deploy
./start.sh              # 构建镜像 + 启动 + 就绪检查 + 打印访问地址
./start.sh --no-build   # 只改了配置时用（内部执行 restart，重新加载挂载文件）
./stop.sh               # 停止并保留数据；./stop.sh -v 连数据一起清空（需确认）
```

`deploy/start.sh` 会自动**比对 `config.yaml` 的 `publicHost` 与宿主机真实 IP**，不匹配直接告警（`deploy/start.sh:34-44`）——这是"视频不出画面"的头号原因。成功后浏览器打开 `http://<宿主机IP>:8080`。

### 2.3 路线 B：裸跑（推荐用脚本）

仓库已提供一键脚本，优先用它们：

```bash
# 在仓库根目录执行
./scripts/start-local.sh            # 起 MediaMTX + 后端，端口预检并等待就绪
./scripts/start-local.sh --build    # 先 make build 再启动
./scripts/stop-local.sh             # 停止（只杀本仓库目录下的进程，数据保留）
```

脚本依次完成：端口预检（`:9997` 已有 MediaMTX 在跑就复用）→ 二进制检查/编译 → 起 MediaMTX 并探 `:9997` → 起后端并探 `:8080` → 打印访问地址（`scripts/start-local.sh:44-134`）。

**手工方式（备选）**：`cd server && make build-web && make build && ./bin/monitorall --config ./config.yaml`；MediaMTX 需另起：`cd mediamtx && ./mediamtx`（必须 `cd` 进去或显式传配置路径，否则它以空配置启动、`api` 被关）。

> ⚠️ **相对路径必须站对目录**：`store.dsn` / `server.dataDir` / `server.secretKeyFile` 都是 `./data/...`，相对**进程当前工作目录**解析。`scripts/start-local.sh` 启动后端前 `cd` 到仓库根（`:110`），数据落 `<仓库根>/data/`；而 `cd server && ./bin/monitorall` 落 `server/data/`。两条路是**两个不同的数据库**，混用会以为"看板丢了"。

### 2.4 脚本清单

| 脚本 | 作用 | 执行位置 |
| --- | --- | --- |
| `scripts/start-local.sh` / `stop-local.sh` | 裸跑一键启停 | 任意（脚本内自行 `cd` 到仓库根） |
| `scripts/smoke.sh` | 冒烟：起后端 → 探 `/healthz` → 校验默认看板与前端产物 | 默认 `BIN=./bin/monitorall`（`:9`），应在 `server/` 下执行；或 `cd server && make smoke` |
| `scripts/verify-rtsp.sh` | RTSP / publish 端到端验证（自包含：起后端→验证→清理） | 任意（脚本内 `cd /home/jack/monitorAll`，换机器需改 `:6`） |
| `deploy/start.sh` / `stop.sh` | Docker 一键启停 | `deploy/` |
| `deploy/diag.sh` | 视频链路自检：按"组件 → 路径 → 播放"逐项探测并给修复提示 | `deploy/` |

### 2.5 成功判据（三条都要过）

```bash
curl http://127.0.0.1:8080/healthz   # {"ok":true,"goroutines":N,"uptimeSec":N,"wsConns":N}
curl http://127.0.0.1:8080/readyz    # {"ready":true,"detail":{"store":true,"mediamtx":true}}
curl http://127.0.0.1:9997/v3/paths/list   # 必须 200（401 见 §8.5）
```

字段见 `server/internal/api/system.go:35-38`。浏览器打开 `http://<本机IP>:8080` 应看到「默认看板」（首启自动创建，名字见 `config.go:170`）。

---

## 3. 核心概念

### 3.1 四层关系

```
看板 Dashboard
  └── 卡片 Card  =  通道 Channel  +  渲染器 Renderer  +  渲染配置
                          ↑
                    数据源 DataSource（一条流 / 一个 rosbridge / 一个 HTTP 服务 / 一个导入文件）
```

| 概念 | 通俗解释 | 举例 |
| --- | --- | --- |
| **数据源** | 「连到哪儿」 | `rtsp://admin:pwd@192.168.2.69:554/...`、`ws://172.31.68.227:9090` |
| **通道** | 「取哪一路数据」；**卡片绑定的是通道** | 视频源固定 1 个通道 = MediaMTX 路径（`server/internal/adapter/video/video.go:248-249`）；ROS 一个话题 = 一个通道 |
| **卡片** | 「看板上的一格」 | 仪表卡显示电池电压 |
| **看板** | 「一整块大屏」= 卡片 + 布局 | 「默认看板」 |

> 一个数据源可有多个通道（一个 ROS Master 下 20 个话题），后端只建**一条连接**，多张卡片共享，不会重复连接。

### 3.2 界面结构

```
┌──────────────────────────────────────────────────────────────┐
│ ① 顶栏  看板切换/新建 │ 编辑模式 │ +新建卡片 │ 保存 │ 连接状态 │ 主题 │ ⛨证书 │
├────────────┬─────────────────────────────────────────────────┤
│ ② 数据源面板│ ③ 网格画布（12 列栅格，行高 30px）               │
│   搜索框    │   卡片区（拖拽 / 缩放）                          │
│   ▾视频源   │                                                 │
│   ▾ROS      │                      ④ 卡片配置抽屉（右侧滑出）  │
│   ▾HTTP     │                                                 │
│ [+新建数据源]│                                                 │
└────────────┴─────────────────────────────────────────────────┘
```

栅格常量：`GRID_COLS = 12`、`ROW_HEIGHT = 30`、卡片最小 `2×2`（`web/src/utils/constants.ts:8-12`）。单看板卡片软上限 40 张，超过只提示不阻断（`constants.ts:20`）。

---

## 4. 配置详解

### 4.0 加载规则

优先级 **环境变量 > yaml > 内置默认值**（`config.go:355`）；环境变量名 = `MONITORALL_` + 路径大写下划线（`:492`）。未给 `--config` 时依次尝试 `./config.yaml`、`./data/config.yaml`，都没有则用纯默认值启动（`:359-364`）。下表"默认值"取自 `DefaultConfig()`（`config.go:280-351`），`server/config.yaml` 实际取值不同处已标注。

### 4.1 `server.*`（HTTP/HTTPS 与数据目录）

| yaml key | 默认值 | 说明 | 何时必须改 |
| --- | --- | --- | --- |
| `server.httpAddr` | `":8080"` | HTTP 监听，**永不跳转 HTTPS**（保证任何环境都能出画面） | 8080 被占用时 |
| `server.httpsAddr` | `":8443"` | HTTPS 监听地址 | 8443 被占用时 |
| `server.tlsEnabled` | **`true`**（`config.go:286`、`server/config.yaml:11`） | 是否启用 HTTPS | **纯 HTTP 部署必须改 `false`**，见下方 |
| `server.autoCert` | `true` | 首启自动生成自签证书（含全部 LAN IP 的 SAN） | 关 TLS 或自带证书时 |
| `server.certDir` | `"./data/cert"` | 证书目录 | Docker 已改 `/app/data/cert` |
| `server.lanHost` | `""` | 宿主机局域网 IP；**留空 = 自动探测首个私有 IPv4**（`server/cmd/monitorall/main.go:91-92`） | 探测不准时**必须**手填 |
| `server.dataDir` | `"./data"` | 数据根目录（SQLite / 密钥 / 证书 / 导入文件） | Docker 已改 `/app/data` |
| `server.secretKeyFile` | `"./data/secret.key"` | 敏感字段加密密钥，首启生成、权限 0600 | **丢失后已存密码无法解密** |
| `server.readTimeoutMs` | `15000` | HTTP 读超时 | 大 payload 场景 |
| `server.writeTimeoutMs` | `15000` | HTTP 写超时 | — |
| `server.shutdownTimeoutMs` | `10000` | 优雅退出超时 | — |

**`tlsEnabled` 开关说明（当前默认是 `true`，不要照抄旧文档）**：

| 取值 | 实际效果 |
| --- | --- |
| `true`（默认） | 同时监听 `:8080`（HTTP，不跳转）与 `:8443`（HTTPS）；`autoCert: true` 时首启自动生成自签证书。浏览器访问 `https://<IP>:8443`，首次先访问 `/trust` 下载并信任 CA。**WebRTC 可用**（安全上下文成立）。 |
| `false` | 只监听 `:8080`。纯 HTTP 无证书信任问题，任何浏览器都能出画面；但 `window.isSecureContext` 为 false，**WebRTC 不可用**，视频走 HLS。 |

联动两项（纯 HTTP 部署通常要一并改）：`mediamtx.publicScheme: auto` → `http`（`config.go:447-459`，auto 表示跟随 `tlsEnabled`）；`mediamtx.encryption: auto` → `never`（`config.go:462-471`，auto 表示仅当 TLS 开启才加密）。

### 4.2 `store.*`（SQLite）

| yaml key | 默认值 | 说明 | 何时必须改 |
| --- | --- | --- | --- |
| `store.dsn` | `"./data/monitorall.db"`（`config.go:297`） | 数据库路径，**相对当前工作目录** | Docker 改 `/app/data/monitorall.db`；裸跑见 §4.8 |
| `store.wal` | `true` | WAL + `synchronous=NORMAL`，`kill -9` 后可恢复 | 不建议改 |

### 4.3 `mediamtx.*`（全部 9 个键）

| yaml key | 默认值 | 说明 | 何时必须改 |
| --- | --- | --- | --- |
| `mediamtx.mode` | `"embedded"` | `embedded`=后端把路径注册进 MediaMTX 并编排拉流；`external`=外部已部署（后端只查不注册） | 用外部 MediaMTX 时 |
| `mediamtx.apiBase` | `"http://mediamtx:9997"`（`config.go:90`） | 控制 API。Docker 用 compose 服务名；**裸跑必须 `http://127.0.0.1:9997`**，写服务名会报 `lookup mediamtx: server misbehaving` | **裸跑必改**（`server/config.yaml:27` 已是） |
| `mediamtx.publicHost` | `""` | 浏览器可直连的 MediaMTX 主机；**留空时后端启动时自动填成 `server.lanHost`**（`main.go:94-95`） | ★ 与浏览器访问看板所用 IP 不一致时**必填** |
| `mediamtx.publicScheme` | `"auto"` | `auto`/`http`/`https`，决定播放地址 scheme | 纯 HTTP 改 `http` |
| `mediamtx.portRtmp` | `1936`（`config.go:93`） | RTMP 端口。**裸跑 `server/config.yaml:30` 实际是 `1935`** | 必须与真实 MediaMTX 一致 |
| `mediamtx.portHls` | `8888` | HLS 播放端口 | 改过 MediaMTX 配置时 |
| `mediamtx.portWebrtc` | `8889` | WebRTC 端口（WHEP 播放与 WHIP 推流共用） | 同上 |
| `mediamtx.encryption` | `"auto"` | `auto`/`never`/`always` | 纯 HTTP 改 `never`，否则 HLS/WHEP 被证书卡住 |
| `mediamtx.flvBase` | `""`（`config.go:97`） | **外部 HTTP-FLV 网关根地址**，如 `http://172.31.68.9:8081/live` | 需要 FLV 时才填，见下方 |

> **FLV 不是 MediaMTX 原生能力。** 官方不支持 HTTP-FLV；`flvBase` 留空时后端**根本不产 `flv` 这一路地址**（`server/internal/media/url.go:126`），默认降级目标就是 **HLS**。只有另配了 SRS / ZLMediaKit / nginx-http-flv 并填 `flvBase`，前端才出现 HTTP-FLV 选项；此时地址拼法为 `<flvBase>/<path 最后一段>.flv`（`url.go:129-133`），`app` 名要写在 `flvBase` 里。compose 不自带 FLV 网关。

### 4.4 `web.*`（高德地图三配置项，缺一不可）

| yaml key | 默认值 | 说明 | 缺失后果 |
| --- | --- | --- | --- |
| `web.amapKey` | `""`（`config.go:255`） | 高德 JS API Key | 地图卡显示配置指引 + 纯坐标列表，**不会**降级到境外底图（合规约束） |
| `web.amapSecurityCode` | `""`（`config.go:260`） | 高德**安全密钥 `securityJsCode`**，与 Key 一一对应不可混用 | 2021-12-02 后申请的 Key 必填。缺失时 **`setMapStyle` 切暗色/自定义样式静默失败**，底图始终回落标准样式——表现就是"切暗色底图毫无反应" |
| `web.amapForceWebGL` | **`true`**（`config.go:266`、`:340`） | 加载 JSAPI 前置 `window.forceWebGL` / `forceWebGLBaseRender` | 无 GPU / 软件渲染 / 手机 WebView 会被判为"性能不足"而不启用 WebGL，控制台报"浏览器版本过低"，底图能出但 **`mapStyle`/`setMapStyle` 一律不生效**（卫星等纯瓦片图层不受影响） |
| `web.theme` | `"dark"` | 前端默认主题 | — |

三项由 `/api/v1/system/runtime` 统一下发（`server/internal/api/system.go:104-106`）；安全密钥必须在 JSAPI 脚本加载**之前**挂到 `window._AMapSecurityConfig`（`web/src/utils/amapLoader.ts:96-100`）。完整排查见 [`../note/04-高德地图.md`](../note/04-高德地图.md)。

### 4.5 `bus.*` / `adapter.*` / `httpPoll.*` / `log.*`

| yaml key | 默认值 | 说明 |
| --- | --- | --- |
| `bus.defaultMaxHz` | `20` | `scalar` / `time_series` / `json` 帧率上限 |
| `bus.imageMaxHz` | `10` | 图像帧率上限 |
| `bus.videoStatusHz` | `0.5` | 视频状态帧频率（2s 一帧，只含地址与状态） |
| `bus.frameBatchMs` | `20` | 批量推送窗口 |
| `bus.frameBatchMax` | `64` | 单批上限 |
| `bus.connQueueDepth` | `256` | 每个 WS 连接输出队列（批次）容量 |
| `bus.ringCapacity` | `1200` | 后端环形缓冲，与前端 1200 点对齐（`constants.ts:18`） |
| `bus.backpressureNotifyMs` | `1000` | 丢帧通知节流（每通道每秒最多 1 条） |
| `bus.pingIntervalMs` | `15000` | 服务端 WS 心跳周期 |
| `bus.writeWaitMs` | `3000` | 写超时，超时即断连 |
| `adapter.healthIntervalMs` | `3000` | 健康探活周期 |
| `adapter.reconnectBaseMs` | `1000` | 重连退避基数 |
| `adapter.reconnectMaxMs` | `30000` | 退避上限 |
| `adapter.reconnectJitter` | `0.2` | ±20% 抖动 |
| `adapter.reconnectMaxRetry` | `-1` | `-1` = 无限重试 |
| `adapter.sampleTimeoutMs` | `3000` | 样例帧超时（3s 无数据 → 50004） |
| `httpPoll.minIntervalMs` | `100` | 轮询周期下限，前端也会 clamp（`DataSourceWizard.vue:145-148`） |
| `httpPoll.defaultTimeoutMs` | `3000` | 默认超时 |
| `httpPoll.failureThreshold` | `3` | 连续失败 3 次才置 `reconnecting` |
| `log.level` | `"info"` | `debug` / `info` / `warn` / `error` |
| `log.format` | `"json"` | `json` / `text` |
| `log.file` | `""` | **留空 = 仅 stdout**；填路径则按大小滚动 |
| `log.maxSizeMb` | `50` | 单文件上限 |
| `log.maxBackups` | `7` | 保留份数 |

### 4.6 环境变量覆盖

`MONITORALL_SERVER_TLSENABLED=false`、`MONITORALL_MEDIAMTX_PUBLICHOST=10.42.0.1`、`MONITORALL_SERVER_HTTPADDR=:9080`（8080 被占用时的临时解法）。

### 4.7 Docker 与裸跑的配置差异

| 配置 | Docker（`deploy/config.yaml`） | 裸跑（`server/config.yaml`） |
| --- | --- | --- |
| `server.tlsEnabled` | `false` | `true` |
| `server.autoCert` | `false` | `true` |
| `server.dataDir` | `/app/data` | `./data` |
| `store.dsn` | `/app/data/monitorall.db` | `./data/monitorall.db` |
| `mediamtx.apiBase` | `http://mediamtx:9997` | `http://127.0.0.1:9997` |
| `mediamtx.portRtmp` | `1936` | `1935` |
| `mediamtx.publicScheme` | `http` | `http` |
| `mediamtx.encryption` | `never` | `never` |

### 4.8 数据库到底在哪（裸跑最容易踩）

| 启动方式 | 工作目录 | 实际数据库路径 |
| --- | --- | --- |
| `./scripts/start-local.sh` | 仓库根（脚本内 `cd`，`start-local.sh:110`） | `<仓库根>/data/monitorall.db` |
| `cd server && ./bin/monitorall --config ./config.yaml` | `server/` | `<仓库根>/server/data/monitorall.db` |
| Docker Compose | 容器内 `/app` | 宿主机 `deploy/data/monitorall.db` |

原因：脚本为统一 `dataDir` / `imports` / 证书 / 密钥的相对路径，显式 `cd` 到仓库根再拉起后端。**不要混用两种启动方式**，否则会看到两套互不可见的看板数据。

---

## 5. 四类数据源创建向导

入口：左侧面板底部 **「+ 新建数据源」**，两步：**① 选类型 → ② 填参数（可先「测试连接」，前端等待上限 5s，`constants.ts:45`）**。后端接受 4 种 `kind`：`video` / `ros` / `http` / `replay`（`server/internal/api/source.go:24`）；**前端向导只暴露前三种**，第四种见 §5.4。

### 5.1 视频流（RTMP / RTSP）

字段表（表单见 `web/src/components/config/DataSourceWizard.vue:298-357`）：

| 字段 | 取值 / 示例 | 说明 |
| --- | --- | --- |
| 名称 | `lite3 相机` | 显示名 |
| **接入模式** | `pull`（拉流，默认）/ `publish`（接收远端推流） | `pull`：让 MediaMTX 主动去拉；`publish`：远端 WHIP/RTMP 推给本机 |
| 流地址 | `rtsp://admin:okwy1688@192.168.2.69:554/Streaming/Channels/101`<br>`rtmp://172.31.68.227:1935/live/lite3` | 支持 **`rtmp` / `rtmps` / `rtsp` / `rtsps`** 四种（`server/internal/media/url.go:74`）；端口缺省按协议补：RTMP 1935、RTSP 554（`url.go:53-58`、`config.go:100-102`）；`user:pass@` 凭据**原样保留**给 MediaMTX 拉流（`url.go:35-36`）；`publish` 模式可留空 |
| **RTSP 传输** | `tcp`（默认）/ `udp` / `automatic` | 仅 RTSP 源且非 `publish` 时出现；UDP 易花屏或连不上故默认 TCP，非法值回落 `tcp`（`video.go:211-219`） |
| **MediaMTX 路径** | 留空则自动取自流地址；`publish` 模式**必填**，如 `live/whip1` | 优先级高于流地址里的 path（`video.go:192-200`）；大小写与斜杠必须与实际推流一致 |
| 首选协议 | `webrtc`（默认）/ `hls` / `flv` | `flv` 仅在配了 `mediamtx.flvBase` 时才有意义 |
| 携带音频 | 关 / 开 | 是否请求音轨 |

**RTSP 摄像头填写示例**（海康/大华常见）：`rtsp://admin:okwy1688@192.168.2.69:554/Streaming/Channels/101`。前端正则明确放行 `user:pass@` 段（`constants.ts:171`），密码不会被脱敏丢弃。

**pull 还是收流？判定规则（`server/internal/adapter/video/video.go:176-189`）**：

- `isLocalPublishLocked()` **只对 `rtmp` / `rtmps` 生效**，**RTSP 一律返回 `false`**（`video.go:180-182`）——即便 RTSP 地址写 `127.0.0.1` 且端口一致，也不会判定为收流。
- 对 RTMP 还需同时满足：端口 == `mediamtx.portRtmp`，且 host ∈ {`127.0.0.1`, `localhost`, `server.lanHost`}。
- 满足则 `source: publisher`（等对方推），否则把完整原始 URL 交给 MediaMTX 自行拉流（`video.go:155-165`）；`publish` 模式恒为 `publisher`。

> 旧文档"127.0.0.1 或本机 IP 且端口一致即判定为收流"是错的，漏了"仅 RTMP"这一条。

**publish 模式创建成功后**：向导不关闭，原地回显两条推流地址并带「复制」按钮（`DataSourceWizard.vue:437-457`）：

| 类型 | 地址形态 | 给谁用 |
| --- | --- | --- |
| WHIP | `<scheme>://<publicHost>:8889/<path>/whip`（`url.go:151`） | 浏览器 / OBS 等 WebRTC 推流端 |
| RTMP | `rtmp://<publicHost>:<portRtmp>/<path>`（`url.go:152`） | ffmpeg / GStreamer |

⚠️ RTMP 推流地址端口随部署方式变：**Docker 1936 / 裸跑 1935**。纯 HTTP 下浏览器推流可能被安全策略拦截，此时改用 RTMP 或启用 HTTPS。

### 5.2 ROS 话题（ROS1 / ROS2）

前置：目标机器运行 rosbridge（默认 `ws://<host>:9090`）：`roslaunch rosbridge_server rosbridge_websocket.launch`（ROS1）/ `ros2 launch rosbridge_server rosbridge_websocket_launch.xml`（ROS2）。

| 字段 | 取值 / 示例 | 说明 |
| --- | --- | --- |
| ROS 版本 | `ros1` / `ros2`（默认 `ros2`） | 切换后表单字段不变 |
| rosbridge 地址 | `ws://172.31.68.227:9090` | 正则 `wss?://`（`constants.ts:174`） |
| Domain ID | `0` | 仅 ROS2 下发（`DataSourceWizard.vue:129`） |
| 话题列表 | `/odom`、`/imu/data`、`/battery`（逗号或换行分隔） | 手工填写为主路径；ROS2 各发行版 rosapi 服务名不一致，自动发现失败时静默降级 |

> 网络要求是「**运行后端的主机 → 机器人 rosbridge 的 9090 端口**」TCP 可达；浏览器只连后端 `:8080`，不必与机器人同网。详见 [`../note/05-ROS接入.md`](../note/05-ROS接入.md)。

**消息类型 → 归一化类型 → 推荐渲染器**（`server/internal/adapter/ros/mapping.go:27-45`）：

| ROS 消息类型 | 归一化类型 | 推荐渲染器 |
| --- | --- | --- |
| `std_msgs/Float32`、`Float64`、`Int32`、`Int64`、`UInt32`、`UInt64`、`Bool` | `scalar` | 仪表 |
| `std_msgs/String` | `scalar` | 原始数据 |
| `sensor_msgs/BatteryState`（取 `voltage`，单位 V） | `scalar` | 仪表 |
| `sensor_msgs/Imu`、`nav_msgs/Odometry` | `time_series_sample` | XY 折线图 |
| `sensor_msgs/NavSatFix` | `geo_pose` | 地图 |
| `sensor_msgs/Image` / `CompressedImage` | `image` | 图像 |
| 其它未知类型 | `json` | 原始数据 |

### 5.3 HTTP 轮询接口

| 字段 | 取值 / 示例 | 说明 |
| --- | --- | --- |
| 接口地址 | `http://172.31.68.9:8080/api/stat` | 必须以 `http://` 或 `https://` 开头 |
| 方法 | `GET` / `POST` | — |
| 轮询间隔(ms) | `1000`（下限 100） | 前后端都会 clamp |
| JSONPath | 留空取整个响应；如 `data.items` | — |
| 超时(ms) | `3000` | — |
| 请求体 | 仅 POST 时填写 | — |

> 轮询在**后端**进行，规避浏览器跨域（CORS）。后端参数见 `server/internal/model/source.go:58-68`（`url`/`method`/`intervalMs`/`headers`/`body`/`jsonPath`/`timeoutMs`/`insecureTls`）；`headers`（如 `Authorization`）属敏感字段，回显脱敏为 `***`。

### 5.4 离线回放（`replay`）——当前真实现状

**后端已完整实现，但没有前端 UI 入口**，目前只能用 REST + curl 使用：

| 环节 | 状态 | 依据 |
| --- | --- | --- |
| 回放适配器 | ✅ 已实现 | `server/internal/adapter/replay/`（`adapter.go` / `parse.go` / `scan.go` + `e2e_test.go` / `replay_test.go`） |
| 导入文件接口 | ✅ 3 个 | `server/internal/api/router.go:119-121`：`GET` / `POST` / `DELETE /:id` `/api/v1/imports` |
| 创建回放数据源 | ✅ 后端接受 | `source.go:24` 的 `oneof=video ros http replay`；`protocol` 必须 `file-replay`（`source.go:629-633`） |
| **前端入口** | ❌ 无 | 向导类型只有 video/ros/http（`DataSourceWizard.vue:54-58`）；`KIND_TEXT` 只有三项（`constants.ts:124-128`）；`web/src` 搜 `imports`/`replay`/`回放` **零命中** |

```bash
# 1) 上传记录文件（.json / .jsonl，上限 200MB，multipart 字段名 file；import.go:19-23,52-56）
curl -F "file=@robot-log.jsonl" http://127.0.0.1:8080/api/v1/imports     # 返回 data.id = <fileId>

# 2) 建回放数据源
curl -X POST http://127.0.0.1:8080/api/v1/datasources -H 'Content-Type: application/json' -d \
 '{"name":"离线回放","kind":"replay","protocol":"file-replay",
   "connParams":{"fileId":"<fileId>","speed":1,"loop":false,"timePath":"timestamp","jsonPath":""}}'

# 3) 之后它就是普通数据源：卡片向导第 ① 步能选到它的通道
```

`connParams`：`fileId`（imports 表主键，必填）、`speed`（倍速，≤0 按 1）、`loop`（播完是否循环）、`timePath`（帧时间戳字段路径，取不到则按记录序号推进，默认 100ms/帧）、`jsonPath`（每条记录再取子路径）——`server/internal/model/source.go:71-77`。文件落在 `<dataDir>/imports/`（`config.go:565`）。

### 5.5 数据源与通道管理

- **搜索**：面板顶部按数据源名 / 协议 / 通道名过滤。
- **折叠**：点数据源前的 `▾ / ▸`。
- **删除数据源**：hover 行 → `✕`（其下通道与引用它们的卡片一并清理，有二次确认）。
- **管理通道**：点「管理通道」→ 出现 `✕` → 删除单个通道。
- **拖拽建档**：**通道可直接拖到画布**，直接打开「新建卡片」抽屉并预选该通道。

---

## 6. 卡片与布局

### 6.1 新建卡片（三步向导，`web/src/components/config/CardConfigDrawer.vue:206-209`）

方式一：顶栏「**+ 新建卡片**」；方式二：**把通道从左侧面板拖到画布**（自动跳过第 ① 步）。

| 步骤 | 操作 |
| --- | --- |
| **① 选择通道** | 从列表选一个通道（选中后自动拉「样例帧」识别数据类型） |
| **② 渲染器** | 只列出与该数据类型兼容的渲染器，兼容项打「推荐」标签 |
| **③ 配置** | 通用项（下表）+ 渲染器专属配置（SchemaForm 驱动），下方实时预览 |

第 ③ 步通用项（`CardConfigDrawer.vue:234-251`）：`标题`、`单位`（如 `V`、`m·s⁻¹`）、`显示脚注`（开/关）、`小数位`（0–6，默认 2）。点「**添加到看板**」完成。

> 提示「3 秒内未收到数据」（`constants.ts:58`）说明该通道暂无数据流入，可先按通道声明类型选渲染器，或先解决数据源在线问题。

### 6.2 编辑模式 / 查看模式

点顶栏「**编辑模式**」切换（快捷键 `E`）。画布的 `is-draggable` / `is-resizable` 直接绑定编辑模式（`web/src/components/layout/DashboardCanvas.vue:209-210`）。

| 模式 | 行为 |
| --- | --- |
| **编辑模式** | 可拖拽/缩放、显示卡片操作按钮（⚙ 配置、✕ 删除）、可新建卡片 |
| **查看模式** | 锁定布局，禁止拖动与缩放（防误操作） |

### 6.3 拖拽与缩放（仅编辑模式）

- **移动**：按住卡片空白处拖动，松手吸附到 12 列栅格。
- **缩放**：鼠标移到卡片**右下角**出现斜纹手柄（光标变 `↘`），拖动改宽高。
- **约束**：最小 `2×2` 格（`constants.ts:11-12`），不可拖出画布；各渲染器还有自己的最小尺寸（地图 `minW:3,minH:4`，`manifests.ts:227`）。

### 6.4 保存、快捷键与多看板

| 快捷键 | 作用 |
| --- | --- |
| `Ctrl + S` / `Cmd + S` | 保存看板 |
| `E` | 切换编辑 / 查看模式 |
| `Esc` | 关闭抽屉 / 退出编辑 |
| 双击视频画面 | 卡片内全屏 |

（`web/src/composables/useHotkeys.ts:32-42`）有未保存改动时顶栏「保存」旁出现**橙色圆点**；拖拽/缩放会**立即自动落库**（轻量保存 `PUT /api/v1/dashboards/:id/layout`）；保存用乐观锁，`revision` 不匹配返回 40009 提示「看板已被修改，请刷新后重试」，**不做自动合并**。多看板在顶栏下拉切换 / 新建 / 重命名 / 删除（不可恢复，有二次确认），URL 变为 `/dashboard/<id>`。

### 6.5 七种渲染器：适用场景与关键配置

| 渲染器 | 接受的数据类型 | 默认尺寸 (w×h) | 关键配置项（默认值） |
| --- | --- | --- | --- |
| **原始数据**（json-tree） | 任意（兜底，含 `video_stream`） | 3×5 | `默认展开层级`(3)、`显示类型标注`(开)、`最大节点数`(5000，超出截断) |
| **表格**（table） | `table` / `json` / `time_series_sample` | 4×5 | `显示列`(留空=自动)、`最大行数`(1000)、`允许排序`(开)、`冻结首列`(关) |
| **仪表**（gauge） | `scalar` / `json` / `time_series_sample` | 2×4 | `数值字段`(`value`)、`量程下限/上限`(0/30)、`小数位`(2)、`阈值区段`(0-21 红"危险"/21-23 黄"警戒"/23-30 绿"安全")、`显示指针`(开)、`脚注显示 MIN/MAX`(开) |
| **XY 折线图**（line-chart） | `time_series_sample` / `scalar` / `json` | 4×5 | `Y 轴字段`(多选 ≤8)、`时间窗口`(60s)、`Y 轴量程`(auto)、`缓冲点数上限`(1200)、`降采样`(lttb)、`显示图例`(开) |
| **图像**（image） | `image` | 4×5 | `缩放方式`(contain)、`显示时间戳水印`(开)、`最大帧率`(10Hz，1–30) |
| **地图**（map） | `geo_pose` / `json` | 6×6 | `输入坐标系`(WGS-84)、`底图样式`(dark)、`轨迹尾迹点数`(200)、`纬度/经度/朝向字段路径`、`朝向单位`(rad)、`镜头跟随`(开)、`初始缩放`(17) |
| **视频播放器**（video） | `video_stream` | 4×6 | `首选协议`(webrtc)、`自动播放`(开)、`静音启动`(开)、`悬浮控制条`(开)、`自动降级`(开)、`重试首选协议间隔`(30s) |

（来源 `web/src/renderers/manifests.ts`：仪表 `:23-49`、折线图 `:75-129`、地图 `:152-214`、JSON 树 `:239-243`、表格 `:257-262`、图像 `:276-290`、视频 `:304-322`）

折线图支持暂停 / 清空 / 导出 CSV；视频支持双击全屏与手动强制切换协议。

### 6.6 地图卡：坐标系与字段路径（最容易配错）

**输入坐标系是三选**（旧文档只写了两种）：

| 选项 | 何时选 |
| --- | --- |
| **WGS-84（GPS 原始 / ROS NavSatFix）** ← 默认 | **GPS 模块原始数据、ROS `sensor_msgs/NavSatFix` 一律选这个** |
| GCJ-02（已转换） | 上游已做过国测局加密的坐标 |
| BD-09（百度） | 坐标来自百度地图系 |

（`web/src/renderers/manifests.ts:157-162`，默认 `WGS-84`）

- 选错会产生 **300–600m 偏移**（`manifests.ts:163`）。
- 转换在前端**离线**完成（`gcoord`，`web/src/utils/geo.ts:9,20-23`），**不调用** `AMap.convertFrom`（有配额且依赖外网）；目标坐标系恒为 GCJ-02（`constants.ts:100`）。
- **payload 里的原始坐标永不被改写**，转换只发生在渲染那一刻（`geo.ts:7`）；卡片脚注显示当前生效坐标系（`geo.ts:49-52`）。
- `web/src` 下只有 `geo.ts` 允许做坐标系换算（合规红线）。

**字段路径写法**（仅 `json` 通道需要，`geo_pose` 无需填）：

- 分隔符**点分或斜杠都行**：`data.pose.latitude` 与 `data/pose/latitude` 等价（`web/src/utils/jsonpath.ts:27-36`）；只认点分会把后者当成单个键名而**静默取不到值**。
- 支持数组下标 `a.b[0].c`（`jsonpath.ts:32`）。
- **`root.` 前缀可带可不带**：JSON 通道 payload 被后端包成 `{root: 原始响应}`（`server/internal/model/payload.go:89-91`），渲染器**两种都试**（先 `path` 再 `root.<path>`，`web/src/components/renderers/MapRenderer.vue:134-137`）。
- 留空时按序尝试候选：纬度 `latitude`→`lat`→`gps.lat`→`pose.position.lat`；经度 `longitude`→`lon`→`lng`→`gps.lon`→`gps.lng`→`pose.position.lon`（`MapRenderer.vue:100-114`）。朝向留空则不画箭头。

### 6.7 折线图卡：缓冲点数上限与 LTTB 降采样（新增）

| 配置项 | 默认值 | 范围 | 说明 |
| --- | --- | --- | --- |
| **缓冲点数上限**（`maxPoints`） | `1200` | 200–5000，步进 100 | 与后端 `bus.ringCapacity` 对齐；超出后环形缓冲自动淘汰最旧点（`manifests.ts:108-117`） |
| **降采样**（`sampling`） | `lttb` | `lttb` / `none` | 点数多时保形抽稀；关闭则全量绘制，点数大时掉帧（`manifests.ts:118-127`） |

> 高刷新率通道（如 20Hz IMU）建议保持 `lttb`；需要逐点精确排查时临时改 `none` 并调小缓冲上限。

---

## 7. 视频播放说明

### 7.1 红线：像素不过后端

后端只下发**地址与状态**，`video_stream` 帧绝不含像素（`server/internal/adapter/video/video.go:1-3`）；画面由**浏览器直连 MediaMTX**拉取。所以后端挂了不影响已在播放的画面，但**浏览器必须能直连 `mediamtx.publicHost` 的 8888/8889 端口**，否则一定黑屏。

### 7.2 三路播放地址怎么拼出来的（`server/internal/media/url.go:109-136`）

| 协议 | 地址形态 | 何时存在 |
| --- | --- | --- |
| WebRTC（WHEP） | `<scheme>://<host>:8889/<path>/whep` | 总是存在 |
| HLS | `<scheme>://<host>:8888/<path>/index.m3u8` | 总是存在（**默认降级目标**） |
| HTTP-FLV | `<flvBase>/<path最后一段>.flv` | **仅在 `mediamtx.flvBase` 非空时** |

`<host>` 回退链：**`mediamtx.publicHost` → `server.lanHost` → `127.0.0.1`**（`url.go:114-120`；publicHost 留空时后端启动时已自动填成 lanHost，`main.go:94-95`）。`<scheme>` 由 `publicScheme` 与 `tlsEnabled` 共同决定（`config.go:447-459`）。

### 7.3 延迟差异（`web/src/utils/constants.ts:73-78`）

| 协议 | 典型延迟 | 前提 |
| --- | --- | --- |
| WebRTC | ≤0.5s | 需要**安全上下文**（HTTPS 或 `localhost`） |
| HTTP-FLV | 1–2s | 需外部 FLV 网关 + 配了 `flvBase` |
| HLS | 2–3s | 唯一在纯 HTTP 下**必然可用**的一路 |

### 7.4 为什么有时 WebRTC 有时 HLS

1. **首选协议**取卡片配置的 `preferredProtocol`（默认 `webrtc`）。
2. **可用集**：后端只在 `urls` 里放有地址的协议，没配 `flvBase` 就没有 flv。
3. **后端挑协议**：`PickProtocol` 把首选置队首，其余按 `webrtc → flv → hls` 兜底（`url.go:156-176`）。
4. **前端实播**：按 `fallbackOrder`（首选在前，其余 `webrtc → hls → flv`）逐个尝试，协商超时 3s 即换下一个（`web/src/utils/video.ts:257-262`、`VideoRenderer.vue:83-121`）；关掉「自动降级」则只试首个。
5. **安全上下文**：WebRTC 要求 `window.isSecureContext`（`video.ts:315-317`）。**纯 HTTP 跨主机访问看板时它必然为 false** → 协商失败 → 落到 HLS。
6. **标记与回切**：实际协议 ≠ 首选时帧里带 `degradedFrom` / `retryInSec`（默认 30s，`config.go:108`、`video.go:416-422`），前端每 30s 后台探测 WHEP 是否恢复，恢复后自动切回（`video.ts:287-313`）。

> 结论：**用 `http://<局域网IP>:8080` 访问时画面走 HLS 是正常现象，不是故障。** 要 ≤0.5s 就启用 TLS（`server.tlsEnabled: true`）并信任自签证书，再把首选协议设成 WebRTC。

### 7.5 如何确认当前链路

1. **看卡片**：悬浮控制条右侧角标显示当前协议名，降级时显示「降级 HLS（2-3s）」，旁有「重试 ⟳ 30s」倒计时（`VideoRenderer.vue:269-272`）。
2. **手动强切**：悬浮条里的下拉（title="强制切换协议"）可锁定协议（`VideoRenderer.vue:256-265`）。
3. **查 MediaMTX**：`curl -s http://127.0.0.1:9997/v3/paths/get/live/lite3` —— `ready` 是否已拉到、`source.type` 是 `publisher`（收流）还是 `rtmp://…`/`rtsp://…`（拉流）。
4. **看帧内容**：`GET /api/v1/channels/<channelId>/sample` 返回的 `video_stream` payload 里，`protocol` 是当前生效协议，`urls` 是全部候选地址。

---

## 8. 运维

### 8.1 端口一览表

| 端口 | 属于 | 叫什么 / 干什么 | 配置键 |
| --- | --- | --- | --- |
| **8080** | 后端 | HTTP：看板页面 + REST + WebSocket（同端口） | `server.httpAddr` |
| **8443** | 后端 | HTTPS（仅 `tlsEnabled: true` 时监听） | `server.httpsAddr` |
| **1935** | MediaMTX（**裸跑**） | RTMP 入，`mediamtx/mediamtx.yml:295` `rtmpAddress: :1935` | `mediamtx.portRtmp` |
| **1936** | MediaMTX（**Docker**） | RTMP 入，compose 映射 `1936:1936`，`deploy/mediamtx/mediamtx.yml` `rtmpAddress: :1936` | `mediamtx.portRtmp` |
| **554** | **摄像头侧** | RTSP 摄像头默认端口（海康/大华）；本平台不监听，只是流地址缺省端口（`config.go:102`） | — |
| **8554** | MediaMTX | MediaMTX 自带 RTSP 服务监听（`mediamtx/mediamtx.yml:250`），compose 未映射，一般不需要 | MediaMTX 侧 |
| **8888** | MediaMTX | HLS 出（降级播放，浏览器直连） | `mediamtx.portHls` |
| **8889** | MediaMTX | WebRTC：WHEP 播放 + WHIP 推流，浏览器直连 | `mediamtx.portWebrtc` |
| **9997** | MediaMTX | HTTP 控制 API，后端用它注册/查询路径，**仅局域网** | `mediamtx.apiBase` |
| **9090** | rosbridge（外部） | ROS 数据源的 rosbridge WebSocket | 数据源表单 |
| **11311** | ROS1 master（可选） | `ROSConnParams.masterUri`（`server/internal/model/source.go:51`） | 数据源参数 |

### 8.2 健康检查

```bash
curl http://127.0.0.1:8080/healthz             # {"ok":true,"goroutines":N,"uptimeSec":N,"wsConns":N}
curl http://127.0.0.1:8080/healthz/goroutines  # 只返回 goroutine 计数，用于泄漏巡检
curl http://127.0.0.1:8080/readyz              # {"ready":true,"detail":{"store":true,"mediamtx":true}}
```

字段见 `server/internal/api/system.go:35-38`。注意 **MediaMTX 不可达不阻断整体 ready**，只在 `detail.mediamtxError` 说明（`system.go:85-87`）。`/healthz` 不带 `/api` 前缀（`router.go:73`），可直接给容器探针用。

### 8.3 日志在哪

| 部署方式 | 后端日志 | MediaMTX 日志 |
| --- | --- | --- |
| Docker | `docker compose logs -f monitorall` | `docker compose logs -f mediamtx` |
| 裸跑（脚本） | `logs/monitorall.log`（`scripts/start-local.sh:111`） | `logs/mediamtx.log`（`scripts/start-local.sh:89`） |
| 配置落文件 | `log.file` 填路径则按大小滚动（50MB × 7）；留空只走 stdout | — |

日志为 JSON，关键字段 `level` / `msg` / `module`。视频链路问题重点看 `module=adapter` 的 `WARN`（如"查询 MediaMTX 路径失败"，`video.go:376`）。

### 8.4 数据与备份

| 内容 | Docker | 裸跑（脚本启动） |
| --- | --- | --- |
| 数据库（看板/卡片/数据源） | `deploy/data/monitorall.db` | `<仓库根>/data/monitorall.db` |
| 加密密钥 | `deploy/data/secret.key`（**丢失后已存密码无法解密**） | `<仓库根>/data/secret.key` |
| 自签证书 | `deploy/data/cert/` | `<仓库根>/data/cert/` |
| 导入的回放文件 | `deploy/data/imports/` | `<仓库根>/data/imports/` |
| 平台配置 | `deploy/config.yaml`（建议纳入版本管理） | `server/config.yaml` |

备份：停服后整个复制 `data/` 目录（SQLite 用 WAL 模式，热复制可能漏掉 `-wal` 文件）。

### 8.5 排障表

| 现象 | 原因 | 解决 |
| --- | --- | --- |
| `curl :9997` 返回 **401** | MediaMTX api 权限 `ips` 限了 `127.0.0.1`；Docker 网桥下后端源 IP 是 172.x | `deploy/mediamtx/mediamtx.yml` 的 api 条目 `ips` 改 `[]`，然后 `docker compose restart mediamtx`（`deploy/diag.sh:29-33`） |
| `:9997` 无响应 | MediaMTX 没起来 / 端口没映射 | `docker compose ps`；`docker compose logs mediamtx --tail 50` |
| `no stream is available on path 'xxx'` | MediaMTX 上无此流 | 核对 path 大小写与斜杠；确认 `api: true`；确认上游在推 |
| 画面「协商超时」/ `Failed to fetch` | 前端拿到的地址不可达 | 核对 `mediamtx.publicHost` / `server.lanHost` == 浏览器访问看板用的 IP |
| 纯 HTTP 下永远走 HLS | 安全上下文不成立，WebRTC 被浏览器拦 | 正常现象；要亚秒就开 TLS 并信任证书（§4.1） |
| 「数据源在线但一直转圈」 | path 与实际推流不一致 | 核对 path；`curl :9997/v3/paths/list` 看 `ready` |
| RTSP 花屏 / 连不上 | UDP 丢包 | RTSP 传输改 `tcp`（默认已是） |
| 改了 MediaMTX 配置没生效 | 单文件 bind mount 绑 inode，MediaMTX 不自动重载 | `docker compose restart mediamtx` |
| 改了平台配置没生效 | `docker compose up -d` 对已运行容器**不重启** | `./start.sh --no-build`（内部 restart） |
| 改了前端/后端代码没生效 | 代码在镜像构建阶段编译并 embed | `./start.sh` **重建**；浏览器 `Ctrl+Shift+R` 强刷 |
| 「看板不见了」 | 换启动目录 → `./data` 解析到另一个库 | 见 §4.8，固定用同一种启动方式 |
| 保存报「看板已被修改」 | 乐观锁 `revision` 冲突 | 刷新后重试，**不会自动合并** |

一键自检：`cd deploy && ./diag.sh`。

---

## 9. 常见问题排查（FAQ）

### Q1. 视频卡片不出画面，按什么顺序查？

① `docker compose ps`（mediamtx 必须 running）+ `curl :9997/v3/paths/list` 必须 200；② `curl :9997/v3/paths/get/live/lite3` 看 `ready` 与 `source.type`；③ 源本身可不可拉：`ffprobe -v error -show_streams rtsp://admin:pwd@192.168.2.69:554/Streaming/Channels/101`；④ `cd deploy && ./diag.sh`。

### Q2. 配了 `web.amapKey`，切暗色底图还是没反应？

大概率缺 **`web.amapSecurityCode`**。2021-12-02 之后申请的 Key 必须配安全密钥，否则 `setMapStyle` **静默失败**回落标准样式，没有任何报错。两项都要填且一一对应。详见 [`../note/04-高德地图.md`](../note/04-高德地图.md)。

### Q3. 地图样式/卫星不对，或点位偏了几百米？

- `mapStyle` 只有三个选项：`amap://styles/normal`（标准路网）/ `dark`（暗色）/ `satellite`（卫星）。卫星在官方是**图层**而非样式，机制不同，见 [`../note/04-高德地图.md`](../note/04-高德地图.md)。
- 无 GPU / 软件渲染环境确认 `web.amapForceWebGL: true`（默认已是）。
- 偏移是**输入坐标系选错**：GPS 原始数据与 ROS `NavSatFix` 选 **WGS-84**，已加密选 GCJ-02，百度系选 BD-09（§6.6）。

### Q4. HTTPS / TLS 怎么开、要不要开？

- **当前 `server.tlsEnabled` 默认是 `true`**（`config.go:286`、`server/config.yaml:11`）；Docker 部署模板 `deploy/config.yaml` 是 `false`。所以取决于你用哪套配置，**不要照抄旧文档的默认值**。
- `true`：监听 `:8443`，`autoCert: true` 时首启自动生成含全部 LAN IP SAN 的自签证书；首次用 HTTPS 访问前先访问 `/trust` 下载并信任 CA（`web/src/router/index.ts` 的 `/trust` 路由）。开了 WebRTC 才可用。
- `false`：只监听 `:8080`，无证书信任问题，但 WebRTC 不可用、视频走 HLS。
- 改完同步 `mediamtx.publicScheme` 与 `mediamtx.encryption`（§4.1）。

### Q5. RTMP 推流地址端口写 1935 还是 1936？

看部署方式：**Docker Compose 是 1936**（compose 映射 + `deploy/config.yaml` `portRtmp: 1936`），**裸跑是 1935**（`mediamtx/mediamtx.yml` `rtmpAddress: :1935` + `server/config.yaml` `portRtmp: 1935`）。publish 模式回显的推流地址端口直接取 `mediamtx.portRtmp`，所以随部署方式变化。

### Q6. 文档说支持 HTTP-FLV，为什么卡片里没这个选项？

MediaMTX **原生不支持 HTTP-FLV**（官方拒绝实现）。`mediamtx.flvBase` 留空时后端不产 flv 地址，前端自然不显示；只有另配 SRS / ZLMediaKit / nginx-http-flv 并填 `flvBase` 才出现。默认降级目标是 **HLS**（§4.3）。见 [`../note/07-视频流接入.md`](../note/07-视频流接入.md)。

### Q7. 卡片没法拖拽/缩放？

确认处于**编辑模式**（快捷键 `E`）；缩放要拖卡片**右下角**手柄（hover 才显示）；若确实失效多为前端产物未更新——改过 `web/` 必须 `make build-web` + `make build`（或 `./start.sh` 重建）并强刷浏览器。见 [`../note/03-前端框架与布局.md`](../note/03-前端框架与布局.md)。

### Q8. 顶栏显示「后端离线」/ 图像卡不刷新？

- 后端离线：`curl http://127.0.0.1:8080/healthz`，再 `docker compose logs monitorall --tail 50`（裸跑 `tail logs/monitorall.log`）。后端重启后前端自动重连并恢复全部订阅，无需刷新。
- 图像不刷新：图像通道默认限流 **10Hz**（`bus.imageMaxHz`），可在渲染器配置里调「最大帧率」（1–30）。

### Q9. 能不能回放历史数据？

**后端能，前端暂时没有入口。** 回放适配器与 `/api/v1/imports` 三个接口都已实现，但前端向导只有 video/ros/http 三类，`web/src` 里搜不到任何回放 UI。目前只能用 curl 走 REST（§5.4）。

### Q10. 8080 被别的进程占了怎么办？

临时：`MONITORALL_SERVER_HTTPADDR=:9080 ./bin/monitorall`（环境变量优先级最高）；长期：改 `server.httpAddr`。

---

## 10. 附录

### 10.1 上线配置 checklist

| # | 项 | 检查方法 |
| --- | --- | --- |
| 1 | `server.lanHost` / `mediamtx.publicHost` == 浏览器访问看板用的 IP | `hostname -I` 比对；`deploy/start.sh` 会自动告警 |
| 2 | `mediamtx.apiBase`：Docker `http://mediamtx:9997`，裸跑 `http://127.0.0.1:9997` | 填反会报 `lookup mediamtx` 或连不上 |
| 3 | `mediamtx.portRtmp` 与真实 MediaMTX 一致（Docker 1936 / 裸跑 1935） | 影响推流地址回显 |
| 4 | `server.tlsEnabled` 与 `mediamtx.publicScheme`、`mediamtx.encryption` 三者自洽 | 纯 HTTP：`false` + `http` + `never` |
| 5 | 地图卡：`web.amapKey` + `web.amapSecurityCode` 都填且配对 | 缺安全密钥 → 切暗色无反应 |
| 6 | MediaMTX 侧 `api: true` 且 api 权限 `ips: []` | `curl :9997/v3/paths/list` 返回 200 |
| 7 | 数据库路径固定用同一种启动方式，别混用 | 见 §4.8 |
| 8 | 备份 `data/` 目录（含 `secret.key`） | 密钥丢失 → 已存密码无法解密 |

### 10.2 相关文档

| 文档 | 内容 |
| --- | --- |
| `docs/DEVELOPMENT.md` | 开发者文档：代码结构、构建、测试、改造注意点 |
| `README.md` | 快速开始与部署速查 |
| [`../note/README.md`](../note/README.md) | 踩坑笔记索引（含两条被推翻的旧结论） |

### 10.3 当前版本的能力边界

以下在界面上找不到入口属正常：

- **离线回放的前端 UI**（后端已实现，只能走 API，见 §5.4）
- 看板导入 / 导出、只读分享链接
- 卡片复制、卡片最大化、右键菜单
- 告警规则引擎与通知
- 多用户登录与权限
- 更多渲染器（数显、状态灯、进度条、日志流、直方图、雷达图、点云）——注意 `sensor_msgs/LaserScan`→`histogram`、`rosgraph_msgs/Log`→`log-stream` 的映射已存在（`server/internal/adapter/ros/mapping.go:43-44`），但对应渲染器尚未实现，这类通道请用「原始数据」卡片查看
- 自定义渲染器插件
