# MonitorAll 开发文档（从代码倒推）

> 本文件由代码倒推而成，**代码是唯一事实来源**。凡标注 `文件:行号` 的断言均可在仓库对应位置核实；未标注的概述性文字仅作上下文衔接。
> 早期的过程性文档 `docs/ARCHITECTURE.md`、`docs/PRD.md`、`docs/TASKS.md` 因写于开发之前、多处结论已被实测推翻，**已从仓库删除**；其中仍然有效的部分已并入本文（见 §13）。需要找回原文可 `git checkout <commit> -- docs/PRD.md`。

---

## 1. 文档定位与阅读路径

本文档面向**二次开发 / 排障 / 接手维护**场景，是仓库唯一的契据来源。它不重复产品宣讲，只描述"代码里到底是什么"。

建议阅读路径：

| 读者目标 | 优先章节 |
|---------|---------|
| 快速上手跑起来 | §5 构建/运行/测试 → §3 目录树 → §12 配置表 |
| 理解数据如何从源到屏 | §6 核心数据模型 → §7 WS 协议 → §9 渲染器系统 |
| 接入新数据源类型 | §10 四类适配器 → §6 模型 → §11 视频链路 |
| 排查视频/降级问题 | §11 视频播放链路 → §13 已知偏差 |
| 对接 REST / 错误码 | §8 REST API 与错误码 |
| 改前端渲染/字段映射 | §9 渲染器系统 → `web/src/renderers/manifests.ts` |

> 说明：早期 PRD 中的**合规红线**（局域网、数据不出内网、像素不回源后端）与**非功能目标**（可拖拽看板、7 类渲染器）仍然有效，已在 §2、§11 用代码落实，此处不再复述产品话术。

---

## 2. 项目定位与硬性约束

**定位**：局域网（LAN）多源、异构监控聚合看板。后端 Go（Gin + gorilla/websocket），前端 Vue3 + Vite + TS + Naive UI，媒体面由 MediaMTX 承载，浏览器直连 MediaMTX，后端只搬运“信令/元数据/归一化帧”。

**硬性约束（均有代码支撑）**：

| 约束 | 代码依据 | 说明 |
|------|---------|------|
| 像素不回源后端 | `web/src/utils/video.ts:4-9`；`server/internal/media/url.go:107-133` | 视频流由浏览器直连 MediaMTX；后端只下发 `urls{webrtc,hls,flv}` |
| 后端只搬运归一化帧 | `server/internal/model/frame.go:22-31` | 统一 `Frame` 契约，所有源归一化后入总线 |
| 坐标系后端只盖章不转换 | `server/internal/model/common.go:64-83`；`server/internal/adapter/ros/mapping.go:110-132` | `GeoPosePayload.CRS` 由后端赋默认值 `WGS-84`（`DefaultCRS`），前端 `gcoord` 离线转 GCJ-02 |
| 密钥脱敏 | `server/internal/crypto/secret.go:23,171-182,259-301` | API 出参敏感字段置 `***`，入库 AES-256-GCM |
| 局域网部署 | `deploy/docker-compose.yml`；`server/config.yaml`（`apiBase=http://127.0.0.1:9997`） | 默认 `tlsEnabled` 后端为 `true`（`config.go:286`），但部署覆盖为 `false`（`deploy/config.yaml:16`） |
| 前端离线坐标转换 | `web/src/utils/geo.ts:31-37` | 用 `gcoord` 做 WGS-84→GCJ-02，绝不上调 `AMap.convertFrom`（避免联网） |

**版本基线**：`Version="1.0.0"`（`server/internal/config/config.go:19`）；WS 协议版本 `WSProtocolVersion=1`（`config.go:28`）。

---

## 3. 真实仓库目录树

以下为当前仓库实际存在的目录与关键文件（已确认存在，含 `server/cmd/monitorall/main.go` 与 `server/config.example.yaml`，旧 ARCHITECTURE 树说它们“不存在”是错的）：

```
monitorAll/
├── README.md
├── server/                          # Go 后端
│   ├── go.mod
│   ├── Makefile                     # 构建脚本（注意：在 server/ 下，非根目录）
│   ├── config.yaml                  # 裸机运行配置
│   ├── config.example.yaml          # 配置样例（真实存在）
│   ├── cmd/monitorall/main.go       # 入口（真实存在，约 163 行）
│   └── internal/
│       ├── config/config.go         # 全量配置与默认值
│       ├── model/                   # common/source/frame/payload/board/ids
│       ├── api/                     # router/source/channel/dashboard/import/system/mediamtx
│       ├── ws/                      # protocol/conn/hub
│       ├── bus/                     # 帧总线与限流
│       ├── adapter/                 # video / ros / http / replay
│       ├── media/                   # url/mediamtx（MediaMTX 交互）
│       ├── store/                   # SQLite schema 与读写
│       └── crypto/                  # 密钥脱敏与加解密
├── web/                             # Vue3 前端
│   ├── package.json
│   └── src/
│       ├── types/index.ts           # 前端模型与枚举
│       ├── renderers/manifests.ts   # 7 个渲染器 Manifest
│       ├── stores/  api/  components/renderers/*.vue  utils/
│       └── utils/video.ts  jsonpath.ts  geo.ts  amapLoader.ts
├── deploy/
│   ├── docker-compose.yml           # monitorall + mediamtx
│   └── config.yaml                  # 容器内配置
├── scripts/
│   ├── start-local.sh               # 裸机一键起（MediaMTX + 后端）
│   └── smoke.sh                     # 冒烟测试
└── mediamtx/                        # MediaMTX 配置/挂载
```

> 数据库 schema 版本：`store/schema.go:14` 的 `CurrentSchemaVersion=2`（库结构版本），与 `config.go:22` 的 `CurrentSchemaVersion=1`（config YAML 的 `schemaVersion` 字段，默认 0 时回落）是**两个不同维度**，不要混淆。

---

## 4. 技术栈与依赖

**后端（节选自 `server/go.mod` 关键项）**：

| 类别 | 选型 | 用途 |
|------|------|------|
| Web 框架 | `gin-gonic/gin` | REST |
| WS | `gorilla/websocket` | 数据面 |
| 数据库 | `modernc.org/sqlite`（CGO-free） | 元数据/看板/导入记录 |
| JSON 处理 | `tidwall/gjson` | HTTP 轮询 JSONPath 提取 |
| 加解密 | 标准 `crypto/aes` + `crypto/cipher` | AES-256-GCM 密钥保全 |
| 构建 | `Makefile`（`CGO_ENABLED=0`） | 便于无 C 工具链分发 |

**前端（`web/package.json`）**：

| 库 | 版本范围 | 用途 |
|----|---------|------|
| vue | `^3.5.13` | 框架 |
| vite | `^6.0.7` | 构建（dev `vue-tsc ^2.2.0`、`typescript ^5.7.3`） |
| naive-ui | `^2.45.0` | 组件库 |
| pinia | `^3.0.1` | 状态 |
| vue-router | `^4.5.0` | 路由 |
| grid-layout-plus | `^1.1.1` | 看板拖拽（**锁 ^1.x**，v2 仍 beta） |
| echarts | `^5.6.0` | 折线/仪表 |
| hls.js | `^1.6.0` | HLS 播放 |
| mpegts.js | `^1.8.0` | FLV 播放（仅 flvBase 存在时） |
| gcoord | `^1.0.7` | WGS-84→GCJ-02 离线转换 |
| 高德 JS API | 2.0（运行时由 `amapLoader.ts` 注入） | 地图底图 |

> 运行环境要求：Node `>=20`（`web/package.json` `engines`）。

---

## 5. 构建 / 运行 / 测试

**后端构建**（在 `server/` 下，非根目录）：

| 动作 | 命令 | 依据 |
|------|------|------|
| 编译二进制 | `make build`（`CGO_ENABLED=0`，`CMD_DIR=./cmd/monitorall`） | `server/Makefile:16,24` |
| 构建前端并拷贝 | `make build-web`（`npm install && npm run build` → 拷贝至 `internal/web/dist`） | `server/Makefile:19,66-70` |
| 跑测试 | `make test` / `go test ./...` | `server/Makefile` |

**运行方式**：

| 方式 | 命令 | 说明 |
|------|------|------|
| 裸机一键 | `scripts/start-local.sh` | 启动本地 MediaMTX + 后端 |
| 容器 | `docker compose -f deploy/docker-compose.yml up` | monitorall + mediamtx 双服务 |
| 冒烟 | `scripts/smoke.sh` | 校验 `healthz` / `dashboards` / 前端可访问 |

**前端**：`cd web && npm install && npm run dev`（开发）；`npm run build`（产出 `dist`，被后端 `make build-web` 收编）。

**测试约定**：后端单测位于各 `internal/**/*_test.go`；前端以 `vue-tsc` 类型检查 + 组件级测试为主。具体用例随包分布，本文不虚构清单。

---

## 6. 核心数据模型

### 6.1 统一帧 `Frame`

所有数据源最终归一化为 `Frame` 入总线（`server/internal/model/frame.go:22-31`）：

| 字段 | 类型 | 说明 |
|------|------|------|
| `channelId` | string | 通道 ID（`ch_` 前缀，`ids.go:17-24`） |
| `seq` | int64 | 单调递增序号 |
| `publishedTs` | int64 | 源端产生时间（ms） |
| `ingestedTs` | int64 | 后端入库时间（ms） |
| `payloadType` | PayloadType | 见 6.2 |
| `payload` | any | 具体载荷（见 6.3） |
| `schemaHint` | string | 可选 schema 提示 |
| `sizeBytes` | int | 序列化大小 |

> 所有时间字段为 `int64` 毫秒；总线延迟 `latencyMs = ingestedTs - publishedTs`（`bus/bus.go:472`）。`NewFrame` 构造器在 `frame.go:41-58`。

### 6.2 载荷类型 `PayloadType`（`common.go:9-23`）

| 值 | 含义 | 扩展标记 |
|----|------|---------|
| `video_stream` | 视频信令（非像素） | — |
| `image` | 单帧图片（base64/url） | — |
| `scalar` | 单数值 | — |
| `time_series_sample` | 时序采样点 | — |
| `geo_pose` | 地理位姿 | — |
| `table` | 表格（≤1000 行） | — |
| `json` | 任意 JSON | — |
| `log_line` | 日志行 | [EXT] P1 |
| `state_enum` | 状态枚举 | [EXT] P1 |
| `histogram_bins` | 直方图 | [EXT] P1 |
| `point_cloud` | 点云 | [EXT] P2 |

**自动推断**：`InferPayloadType`（`frame.go:184-211`）按数组→`table`、全数值数组→`time_series_sample`、数值→`scalar`、其余→`json` 推断（主要用于 HTTP 轮询源）。

### 6.3 各载荷结构（`payload.go`）

| 载荷 | 关键字段 | 位置 |
|------|---------|------|
| `VideoPayload` | `protocol`/`state`/`urls{webrtc,hls,flv}`/`sourceUrl`/`path`/`degradedFrom` | `payload.go:6-18` |
| `ImagePayload` | `mime`/`encoding(base64\|url)`/`data`/`width`/`height` | `payload.go:28-35` |
| `ScalarPayload` | `value`/`unit` | `payload.go:44-51` |
| `TimeSeriesPayload` | `metric`/`samples[]` | `payload.go:54-58` |
| `GeoPosePayload` | `lat`/`lon`/`crs`（后端盖章） | `payload.go:61-71` |
| `TablePayload` | `columns[]`/`rows[][]`（上限 `MaxTableRows=1000`） | `payload.go:81-86,94` |

### 6.4 状态机 `Status`（`common.go:27-37`）

共 **7** 态（旧 PRD 写 4 态，已过时）：

`idle` → `connecting` → `online` ⇄ `reconnecting`；异常 `error`；视频降级 `degraded`；回放结束/断连 `offline`。

### 6.5 数据源与通道（`source.go`）

| 结构 | 关键字段 | 位置 |
|------|---------|------|
| `DataSource` | `id`(ds_)/`kind`/`protocol`/`connParams`/`meta` | `source.go:14-29` |
| `VideoConnParams` | `mode`(pull\|publish)/`rtspTransport`/`url` | `source.go:32-44` |
| `ROSConnParams` | `masterUri`/`topicFilters` | `source.go:47-56` |
| `HTTPConnParams` | `url`/`intervalMs`/`jsonPath` | `source.go:59-68` |
| `ReplayConnParams` | `fileRef`/`loop` | `source.go:71-77` |
| `Channel` | `id`(ch_)/`sourceId`/`payloadType`/`spec` | `source.go:80-93` |

> 元信息键常量见 `source.go:105-144`（如 `MetaIsLocalPublish`/`MetaPublishURL`/`MetaPublishRTMPURL`）。

### 6.6 看板/卡片（`board.go`、`ids.go`）

- ID 前缀：`ds_/ch_/cd_/db_/tmp_/im_`（`ids.go:17-24`）。
- `Layout`/`Card`/`Dashboard` 结构见 `board.go`。
- `NowMs()` 统一时间源（`ids.go:70`）。

---

## 7. WebSocket 协议

端点：`WSPath="/api/v1/ws"`（`config.go:160`），与 REST 同端口。

**握手**：客户端首帧须 `op=hello` 且 `protocol == WSProtocolVersion`（`=1`），否则服务端回 `WS_PROTOCOL_MISMATCH` 并断开（`protocol.go:37`；`ws/conn.go` `handleHello`；`ws/hub.go:60-86`）。握手等待超时 `HelloTimeoutMs=5000`（`config.go:164`）。

**客户端→服务端 op**（`protocol.go:15-19`）：

| op | 含义 |
|----|------|
| `hello` | 握手，带 `protocol`/`clientId` |
| `subscribe` | 订阅 `channelIds[]` |
| `unsubscribe` | 取消订阅 |
| `pong` | 响应 ping |
| `setRate` | [EXT] P1：接收但不生效（`protocol.go:19`） |

**服务端→客户端 op**（`protocol.go:24-32`）：

| op | 含义 |
|----|------|
| `welcome` | 握手成功，带 `protocol` |
| `subscribed`/`unsubscribed` | 订阅确认 |
| `frames` | 批量帧（`ServerMsg.Items`） |
| `channelStatus`/`sourceStatus` | 状态推送 |
| `backpressure` | 反压（`dropped`/`windowMs`） |
| `ping` | 心跳 |
| `error` | 错误（`code`/`message`/`ref`） |

**消息结构**（`protocol.go:46-90`）：`ClientMsg{op,protocol,clientId,channelIds,ref,t,channelId,hz}`；`ServerMsg{op,protocol,items,ts,channelId,...}`。

**心跳与存活**（`ws/conn.go`）：`readPump`（`conn.go:160-213`）；存活截止 `aliveDeadline = pingIntervalMs * (MaxMissedPong+1)`（`conn.go:224-230`）。参数：`PingIntervalMs=15000`（`config.go:122`）、`PongTimeoutMs=3000`（`config.go:166`）、`MaxMissedPong=3`（`config.go:168`）。

**多路复用与反压**：单连接多通道；`hub.Subscribe` 走 `EnsureChannel→adm.Subscribe→bus.Subscribe`（`hub.go:117-158`）；`DispatchFrames` 批量下发（`hub.go:205-217`）。

---

## 8. REST API 与错误码

### 8.1 路由表（`api/router.go:73-126`）

前缀 `APIPrefix="/api/v1"`（`config.go:162`）。

| 方法 | 路径 | 处理 |
|------|------|------|
| GET | `/healthz`、`/healthz/goroutines`、`/readyz` | 健康检查 |
| GET | `/api/v1/ws` | WS 升级 |
| GET/POST | `/api/v1/datasources` | 列表/创建 |
| POST | `/api/v1/datasources/test` | 连通性测试 |
| GET/PUT/DELETE | `/api/v1/datasources/:id` | 详情/改/删 |
| GET/POST | `/api/v1/datasources/:id/channels` | 通道列表/创建 |
| GET/DELETE | `/api/v1/channels/:id` | 通道详情/删 |
| GET | `/api/v1/channels/:id/sample` | 抽样 |
| GET/POST | `/api/v1/dashboards` | 看板列表/创建 |
| GET/PUT/DELETE | `/api/v1/dashboards/:id` | 看板详情/改/删 |
| PUT | `/api/v1/dashboards/:id/layout` | 布局 |
| GET/POST | `/api/v1/dashboards/:id/cards` | 卡片列表/创建 |
| PUT/DELETE | `/api/v1/cards/:id` | 卡片改/删 |
| GET | `/api/v1/mediamtx/paths` | MediaMTX 路径列表 |
| GET/POST/DELETE | `/api/v1/imports`(`/:id`) | 离线导入列表/上传/删 |
| GET | `/api/v1/system/runtime`、`/lan`、`/cert` | 运行时配置/局域网地址/证书下载 |

**创建数据源请求**：`CreateDataSourceReq`（`api/source.go:22-28`），`kind` 取值 `video|ros|http|replay`（oneof）。出参经 `decorateSource` 对密钥脱敏（`api/source.go:87-101`）。

### 8.2 错误码（`apperr/apperr.go:17-52`）

统一业务码（`Code`），HTTP 状态映射见 `httpStatusMap`（`apperr.go:55-72`）。

| 业务码 | 值 | HTTP | 含义 |
|--------|----|------|------|
| OK | 0 | 200 | 成功 |
| BadRequest | 40000 | 400 | 报文/JSON 解析失败 |
| InvalidParam | 40001 | 400 | 参数非法 |
| InvalidURL | 40002 | 400 | URL 非法 |
| UnsupportedType | 40003 | 400 | 类型不支持 |
| NotFound | 40004 | 404 | 资源不存在 |
| RevisionConflict | 40009 | 409 | 看板 revision 冲突（并发覆盖） |
| RendererIncompatible | 40010 | 400 | 渲染器不兼容 |
| SourceConnectFailed | 50001 | 500 | 数据源连接失败 |
| AdapterError | 50002 | 500 | 适配器错误 |
| MediaMTXUnreachable | 50003 | 500 | MediaMTX 不可达 |
| SampleTimeout | 50004 | 500 | 抽样超时 |
| StoreError | 50010 | 500 | 存储错误 |
| SecretDecryptFailed | 50011 | 500 | 密钥解密失败 |
| ChannelUnavailable | 50301 | 503 | 通道不可用 |
| Internal | 50900 | 500 | 未分类内部错误 |

> 错误响应体形如 `{"code":40000,"message":"bad request","ref":"..."}`（`apperr.go:77-91`）。

### 8.3 离线导入端点（`/api/v1/imports`）

| 约束 | 值 | 位置 |
|------|----|------|
| 单文件大小上限 | `maxImportSizeBytes = 200 << 20`（200MB） | `import.go:18-19` |
| 超限返回 | `InvalidParam`（40001） | `import.go:48-49` |
| 允许扩展名 | 仅 `.json` / `.jsonl` | `import.go:53-54` |
| 文件落盘 | `<dataDir>/imports/<rel_path>`；库表 `imports` 只记相对路径与解析统计 | `store/schema.go:90-104` |

### 8.4 创建数据源请求示例

请求体 `CreateDataSourceReq`（`source.go:22-28`）：

```json
{
  "name": "camera-1",
  "kind": "video",          // oneof: video | ros | http | replay（source.go:24）
  "protocol": "rtmp",
  "connParams": { "url": "rtmp://...", "mode": "pull" }
}
```

- `kind` 与 `protocol` 必须匹配，否则 `validateProtocol` 拒绝（`source.go:110,615-637`；匹配矩阵见 `source.go:617-637`）。
- 响应经 `decorateSource` 对密钥脱敏（`source.go:87-101`）；敏感键由 `MergeSecretKeys` 合并（`source.go:120,267`）。
- 创建时即尝试 `adapter.New` 探测通道，`ListChannels` 失败则走 `fallbackSpecs` 软降级（`source.go:148,368`）。

---

## 9. 渲染器系统

前端通过 **Manifest 注册表**声明渲染器能消费哪种 `payloadType`、从帧里取哪些字段。共 **7** 个 P0 渲染器（`web/src/renderers/manifests.ts`）：

| 渲染器 | Manifest | 适配 payloadType |
|--------|---------|-----------------|
| 仪表盘 | `gaugeManifest` (`manifests.ts:17`) | `scalar` |
| 折线图 | `lineChartManifest` (`manifests.ts:69`) | `time_series_sample` |
| 地图 | `mapManifest` (`manifests.ts:146`) | `geo_pose` |
| JSON 树 | `jsonTreeManifest` (`manifests.ts:233`) | `json` |
| 表格 | `tableManifest` (`manifests.ts:251`) | `table` |
| 图片 | `imageManifest` (`manifests.ts:270`) | `image` |
| 视频 | `videoManifest` (`manifests.ts:298`) | `video_stream` |

注册集合 `P0_MANIFESTS`（`manifests.ts:337-345`）。`RendererManifest` 类型定义见 `web/src/types/index.ts:528-542`。

**字段路径规则**：支持点（`.`）或斜杠（`/`）分隔；可省略 `root.` 前缀（`mapManifest` 帮助文案 `manifests.ts:177-200`）。解析实现：`web/src/utils/jsonpath.ts:30-55`（`parsePathParts` 支持点+斜杠；`getByPath` 取值）。

> 注意：前端 `types/index.ts` 的 `DataSourceKind`（`index.ts:41`）与 `Protocol`（`index.ts:43`）**未包含** `replay` / `file-replay`，而后端 `common.go:48,61` 已支持——这是前端枚举滞后（见 §13）。

---

## 10. 四类适配器

统一适配器接口驱动 `ListChannels` / `StartChannel` / `Health` / `Stop`。四类如下：

### 10.1 video（`adapter/video/video.go`）
- 拉流（pull）或接收推流（publish）：`mode`（`source.go:32-44`）。
- 注册到 MediaMTX 的 `source`：`publish` 模式或“本机 RTMP 推流”用 `publisher`，其余用原始 URL（`sourceFor()` `video.go:155-165`）。
- **本机推流判定** `isLocalPublishLocked`（`video.go:176-189`）：仅当 `IsRTMP()` 且端口等于 `PortRTMP` 且 host 为 `127.0.0.1/localhost/lanHost`。
- RTSP 强制 TCP：默认 `RTSPTransportTCP="tcp"`（`config.go:83`），非法值回落 TCP（`video.go:211-217`），强制赋值处在 `video.go:139-140`。
- 推流地址回显 `WHIP`/`RTMP` 写入通道 meta（`video.go:285-295`）。

### 10.2 ros（`adapter/ros/*`）
- ROS1/ROS2 **唯一收敛点**在 `diff.go:11-18`（`NormalizeMsgType` `diff.go:44-50`）；服务调用参数 ROS1 用数组、ROS2 用对象（`BuildServiceCall` `diff.go:147-155`）。
- 消息→载荷映射表 `msgTypeMapping`（`mapping.go:27-45`）。
- `BuildGeoPose` 仅盖章 `CRS`，**不做坐标转换**（`mapping.go:110-132`）。
- 无数据超时 `NoDataTimeoutMs=3000`（`server/internal/adapter/ros/diff.go:310`）；`watchNoData` 触发状态（`ros/adapter.go:245-275`）；`ListChannels` 对 rosapi 失败软降级（`ros/adapter.go:149-192`）。

### 10.3 http（`adapter/http/adapter.go`）
- 默认轮询周期 `defaultIntervalMs=1000`（`http/adapter.go:109`）。
- 单通道 = URL 的 path 部分（`ListChannels` `http/adapter.go:157-168`）。
- 用 `gjson` 按 `jsonPath` 提取并推断 `payloadType`（`extract` `http/adapter.go:340-360`）。

### 10.4 replay（`adapter/replay/adapter.go`）
- **`Health` 恒返回 OK**（`replay/adapter.go:158-160`）：本地文件回放无“连接断开”语义，避免重连风暴；播完由协程发 `channelStatus(idle)`（`publishIdle` `replay/adapter.go:417-427`）。
- 导入文件解析：`iterateRecords` 支持数组 / NDJSON / 对象（`parse.go:23-36`）；时间戳量级归一（`normalizeTs` `parse.go:187-198`）；`inferPayloadType` 含 geo 探测（`parse.go:264-271`）。
- 修复：`IngestedTs = PublishedTs`（回放无网络延迟，`adapter.go` 对应逻辑）。

---

## 11. 视频播放链路

**关键事实**（代码证实，旧 PRD 描述错误）：**MediaMTX 原生不支持 HTTP-FLV**（官方 issue #1460 拒绝实现），因此**默认降级目标为 HLS（mpegts 变体）+ hls.js**；`flv` 仅在后端下发了 `flvBase`（外部 FLV 网关，如 SRS/ZLMediaKit/nginx-http-flv）且 `urls.flv` 存在时启用（`web/src/utils/video.ts:4-9`；`server/internal/media/url.go:107-133`）。

**后端地址生成**：
- `ParseStreamURL` 解析 `rtmp/rtmps/rtsp/rtsps` 与默认端口（`url.go:63-95`；`DefaultRTMPPort=1935` `config.go:100`、`DefaultRTSPPort=554` `config.go:102`）。
- `BuildPlayURLs` 按 `publicHost`→`lanHost`→`127.0.0.1` 回退生成 `webrtc/hls/flv` 三路绝对地址；`flv` 仅当 `FLVBase` 非空才产出（`url.go:109-133`）。
- `BuildPublishURLs` 生成 `WHIP + RTMP` 推流地址（`url.go:139-154`）。

**降级选择**：

| 实现 | 降级顺序 | 兜底 | 位置 |
|------|---------|------|------|
| 后端 `PickProtocol` | `webrtc → flv(若配置) → hls` | `hls` | `url.go:157-176` |
| 前端 `fallbackOrder` | `webrtc → hls → flv` | — | `video.ts:257-266` |

> ⚠️ **前后端降级顺序存在分歧**：后端在有 FLV 时优先 FLV 而非 HLS；前端 `fallbackOrder` 优先 HLS 再 FLV。实际生效还受 `runtime` 下发的 `allowedBackends`（webrtc+hls，配 `flvBase` 时加 flv，`api/system.go:97-117`）约束。详见 §13。

**播放器**：`web/src/utils/video.ts` 三套后端——WebRTC（首选、`isSecureContext` 限制 `video.ts:315-317`）、HLS（`hls.js`，默认降级）、FLV（`mpegts.js`，仅 `flvBase` 可用时）。`VideoPayload` 结构见 §6.3。

**像素路径**：浏览器直连 MediaMTX（WebRTC/WHEP 或 HLS/FLV），**不经过 Go 后端**；后端仅下发信令与 `urls`（`payload.go:6-18`）。

---

## 12. 配置项完整参考表

配置默认值集中在 `server/internal/config/config.go`（禁止别处写裸魔法值，`config.go:2`）。环境变量覆盖前缀 `MONITORALL_`（`config.go:25`）。

### 12.1 服务与媒体

| 配置键 | 默认值 | 位置 | 说明 |
|--------|--------|------|------|
| `Version` | `1.0.0` | `config.go:19` | 版本常量 |
| `WSProtocolVersion` | `1` | `config.go:28` | WS 协议版本 |
| `WSPath` | `/api/v1/ws` | `config.go:160` | WS 端点 |
| `APIPrefix` | `/api/v1` | `config.go:162` | REST 前缀 |
| `MediaMTX.APIBase` | `http://mediamtx:9997` | `config.go:90,302` | MediaMTX API |
| `MediaMTX.PortRTMP` | `1936` | `config.go:93,305` | RTMP 端口（容器） |
| `MediaMTX.PortHLS` | `8888` | `config.go:94,306` | HLS 端口 |
| `MediaMTX.PortWebRTC` | `8889` | `config.go:95,307` | WebRTC 端口 |
| `DefaultRTMPPort` | `1935` | `config.go:100` | RTMP URL 缺省端口 |
| `DefaultRTSPPort` | `554` | `config.go:102` | RTSP URL 缺省端口 |
| `RTSPTransportTCP` | `tcp` | `config.go:83` | RTSP 拉流传输 |
| `DefaultRetryPreferredSec` | `30` | `config.go:108` | 降级后重试首选视频协议间隔(s) |
| `TLS.Enabled`（默认） | `true` | `config.go:286` | 部署覆盖为 `false`（`deploy/config.yaml:16`） |
| `AMapForceWebGL` | `true` | `config.go:340` | 强制高德 WebGL 渲染 |

### 12.2 总线与心跳

| 配置键 | 默认值 | 位置 |
|--------|--------|------|
| `Bus.DefaultMaxHz` | `20` | `config.go:114,312` |
| `Bus.ImageMaxHz` | `10` | `config.go:115,313` |
| `Bus.VideoStatusHz` | `0.5` | `config.go:116,314` |
| `Bus.PingIntervalMs` | `15000` | `config.go:122,320` |
| `HelloTimeoutMs` | `5000` | `config.go:164` |
| `PongTimeoutMs` | `3000` | `config.go:166` |
| `MaxMissedPong` | `3` | `config.go:168` |

> 限流：`defaultHz` 按 `payloadType` 返回 `ImageMaxHz`(image)/`VideoStatusHz`(video)/`DefaultMaxHz`(其余)（`bus/bus.go:185-194`）。

### 12.3 派生/辅助方法

| 方法 | 行为 | 位置 |
|------|------|------|
| `Config.PublicScheme()` | 返回公开 scheme（http/https） | `config.go:447-459` |
| `Config.MediaMTXEncryption()` | MediaMTX 是否加密 | `config.go:462-471` |
| `applyEnv` | `MONITORALL_*` 递归覆盖 | `config.go:473` |

### 12.4 数据库

| 项 | 值 | 位置 |
|----|----|------|
| 库结构版本 `CurrentSchemaVersion` | `2` | `store/schema.go:14` |
| 表 | `meta/data_sources/channels/dashboards/cards/secret_meta` + `imports`(v2) | `store/schema.go:17-104` |

---

## 13. 已知偏差与坑速查

本节记录“旧文档/前端与当前代码不符”或“易踩坑”的点，均附代码依据。

| # | 偏差/坑 | 旧说法 | 代码事实 | 位置 |
|---|--------|--------|---------|------|
| 1 | 视频降级 | PRD 称“MediaMTX 输出 WebRTC + HTTP-FLV 双 fallback” | MediaMTX 原生不支持 HTTP-FLV；默认降级 HLS；FLV 仅外部网关 | `video.ts:4-9`；`url.go:107-133`；PRD L251/324/372/467/593/793 |
| 2 | 状态枚举 | PRD 写 4 态状态机 | 实际 7 态：`idle/connecting/online/reconnecting/error/degraded/offline` | `common.go:27-37`；PRD L309/465 |
| 3 | 数据源种类 | 部分文档/前端缺 replay | 后端已支持 `replay`+`file-replay` | `common.go:48,61`；`types/index.ts:41,43` |
| 4 | 前端枚举滞后 | — | `DataSourceKind` 缺 `replay`、`Protocol` 缺 `file-replay` | `types/index.ts:41,43` |
| 5 | 降级顺序分歧 | — | 后端 `webrtc→flv→hls`，前端 `webrtc→hls→flv`（FLV 优先级相反） | `url.go:157-176`；`video.ts:257-266` |
| 6 | 目录树过时 | 旧 ARCHITECTURE 称 `cmd/monitorall/main.go`、`config.example.yaml` 不存在 | 二者均真实存在 | `server/cmd/monitorall/main.go`；`server/config.example.yaml` |
| 7 | 坐标系 | 易误以为后端转换 | 后端只盖章 `CRS`（默认 `WGS-84`），前端 `gcoord` 离线转 GCJ-02，绝不上调 `AMap.convertFrom` | `common.go:64-83`；`geo.ts:31-37` |
| 8 | 像素路径 | 易误以为后端转发视频 | 浏览器直连 MediaMTX，后端只下发信令/urls | `payload.go:6-18`；`url.go:109-133` |
| 9 | 密钥处理 | — | API 出参敏感字段置 `***`（`MaskedValue`），入库 AES-256-GCM | `secret.go:23,259-301` |
| 10 | 回放 Health | 易误触发重连风暴 | `Health` 恒 OK，播完发 `idle` 不走重连 | `replay/adapter.go:158-160,417-427` |
| 11 | Schema 版本 | 易混淆 | `config.go:22` 的 `CurrentSchemaVersion=1`（config YAML 维度）≠ `store/schema.go:14` 的 `=2`（DB 维度） | `config.go:22`；`store/schema.go:14` |
| 12 | Makefile 位置 | 易误以为在根目录 | 实际在 `server/Makefile` | `server/Makefile` |
| 13 | TLS 默认值 | 易误以为关闭 | 后端默认 `true`，部署 `deploy/config.yaml:16` 覆盖为 `false` | `config.go:286`；`deploy/config.yaml:16` |
| 14 | 表行上限 | — | `TablePayload` 上限 `MaxTableRows=1000` | `payload.go:94` |
| 15 | setRate 不生效 | — | WS `setRate` 接收但不生效（[EXT] P1） | `protocol.go:19` |

---

### 附：快速索引（按文件）

- 配置与默认值：`server/internal/config/config.go`
- 模型/枚举：`server/internal/model/{common,source,frame,payload,board,ids}.go`
- WS：`server/internal/ws/{protocol,conn,hub}.go`
- REST：`server/internal/api/{router,source,system,mediamtx,import}.go`
- 总线/限流：`server/internal/bus/bus.go`
- 适配器：`server/internal/adapter/{video,ros,http,replay}/*`
- 媒体：`server/internal/media/{url,mediamtx}.go`
- 存储：`server/internal/store/schema.go`
- 密钥：`server/internal/crypto/secret.go`
- 前端枚举/Manifest：`web/src/types/index.ts`、`web/src/renderers/manifests.ts`
- 前端工具：`web/src/utils/{video,jsonpath,geo,amapLoader}.ts`
- 部署/脚本：`deploy/*`、`scripts/{start-local,smoke}.sh`
