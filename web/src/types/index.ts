/**
 * MonitorAll 前端业务类型总表。
 *
 * 🚨 契约来源：docs/ARCHITECTURE.md §4（核心数据模型）与 §5（统一 Frame 契约）。
 * 字段名与 Go struct 的 json tag 逐一对齐，禁止自行改写：
 *   - 全 camelCase
 *   - 时间统一毫秒 number（int64 ms），禁止秒、禁止 RFC3339 字符串
 *   - 可选字段用 `?:`（对应 Go 的 omitempty），不用 null 表达未设置
 */
import type { Component } from 'vue'

/* ——————————————————————————————
 * 4.1 枚举
 * —————————————————————————————— */

/** payload 归一化类型（ARCH §5.2）。 */
export type PayloadType =
  | 'video_stream'
  | 'image'
  | 'scalar'
  | 'time_series_sample'
  | 'geo_pose'
  | 'table'
  | 'json'
  // [EXT] P1/P2 扩展点，仅保留类型
  | 'log_line'
  | 'state_enum'
  | 'histogram_bins'
  | 'point_cloud'

/** 数据源 / 通道运行状态（ARCH §4.1）。 */
export type Status =
  | 'idle'
  | 'connecting'
  | 'online'
  | 'reconnecting'
  | 'error'
  | 'degraded'
  | 'offline'

export type DataSourceKind = 'video' | 'ros' | 'http'

export type Protocol = 'rtmp' | 'rtsp' | 'ros1' | 'ros2' | 'http-poll'

/** 坐标系（合规相关，见 ARCH §13）。 */
export type CRS = 'WGS-84' | 'GCJ-02' | 'BD-09'

/**
 * 卡片呈现状态（前端派生，不落库、不参与 WS/REST 契约）。
 * 在 ARCH §10.3 的基础上，额外覆盖视频降级态。
 */
export type CardVisualStatus = Status | 'unconfigured' | 'loading' | 'waiting' | 'renderError'

/* ——————————————————————————————
 * 4.2 DataSource + Channel
 * —————————————————————————————— */

export interface RetryPolicy {
  baseMs: number
  maxMs: number
  jitter: number
  maxRetry: number
}

export interface DataSource {
  id: string
  name: string
  kind: DataSourceKind
  protocol: Protocol
  /** 敏感键在出参中已被后端替换为 '***'。 */
  connParams: Record<string, unknown>
  secretKeys: string[]
  retryPolicy?: RetryPolicy
  status: Status
  lastError?: string
  lastOkAt?: number
  enabled: boolean
  createdAt: number
  updatedAt: number
  schemaVersion: number
}

export interface Channel {
  id: string
  dataSourceId: string
  name: string
  payloadType: PayloadType
  meta?: Record<string, unknown>
  /** 0 = 使用后端全局默认（BE-04）。 */
  rateLimitHz: number
  status: Status
  lastError?: string
  lastFrameAt?: number
  lastSeq: number
  /** ingestedTs - publishedTs 的滑动均值。 */
  latencyMs?: number
  createdAt: number
}

/** 运行时视图（不落盘，仅 REST/WS 返回）。 */
export interface ChannelRuntime extends Channel {
  refCount: number
  frameRateHz: number
  dropped: number
}

export interface VideoConnParams {
  /** 流地址，支持 rtmp / rtmps / rtsp / rtsps；publish 模式可留空。 */
  rtmpUrl: string
  /** MediaMTX 路径；留空时后端自动从流地址解析。publish 模式必填。 */
  mediaMtxPath?: string
  preferredProtocol?: 'webrtc' | 'hls' | 'flv'
  audio: boolean
  /** 接入模式：pull（默认，主动拉远端流）| publish（接收远端 WHIP / RTMP 推流）。 */
  mode?: 'pull' | 'publish'
  /** RTSP 拉流传输方式：tcp（默认，UDP 在摄像头场景易花屏）| udp | automatic。 */
  rtspTransport?: 'tcp' | 'udp' | 'automatic'
}

export interface ROSConnParams {
  version: 'ros1' | 'ros2'
  bridgeUrl: string
  nodeName?: string
  masterUri?: string
  domainId?: number
  topics?: string[]
  topicTypes?: Record<string, string>
}

export interface HTTPConnParams {
  url: string
  method: 'GET' | 'POST'
  intervalMs: number
  headers?: Record<string, string>
  body?: string
  jsonPath?: string
  timeoutMs: number
  insecureTls: boolean
}

/* ——————————————————————————————
 * 4.3 Card + Dashboard
 * —————————————————————————————— */

export interface Layout {
  x: number
  y: number
  w: number
  h: number
  minW: number
  minH: number
}

export interface DisplayOptions {
  showFooter: boolean
  decimals: number
  footerFields: string[]
}

export interface Card {
  id: string
  dashboardId: string
  channelId: string
  rendererType: string
  title: string
  unit?: string
  /** 由 RendererManifest.configSchema 驱动。 */
  renderConfig: Record<string, unknown>
  displayOptions: DisplayOptions
  layout: Layout
  createdAt: number
  updatedAt: number
}

export interface Dashboard {
  id: string
  name: string
  /** 乐观锁，每次保存 +1。 */
  revision: number
  schemaVersion: number
  gridCols: number
  rowHeight: number
  margin: [number, number]
  globalConfig: Record<string, unknown>
  createdAt: number
  updatedAt: number
}

export interface DashboardWithCards extends Dashboard {
  cards: Card[]
}

/* ——————————————————————————————
 * 5.1 Frame 外壳
 * —————————————————————————————— */

export interface FieldHint {
  type: 'number' | 'string' | 'boolean' | 'object' | 'array'
  unit?: string
  /** JSONPath，如 twist.linear.x。 */
  path: string
}

export interface Frame<T = unknown> {
  channelId: string
  seq: number
  /** 数据产生时刻（ms）；无法获取时 = ingestedTs。 */
  publishedTs: number
  /** 后端接收时刻（ms）。 */
  ingestedTs: number
  payloadType: PayloadType
  payload: T
  /** path → hint，驱动字段映射 UI。 */
  schemaHint?: Record<string, FieldHint>
  sizeBytes: number
}

/* ——————————————————————————————
 * 5.3 payload 具体结构
 * —————————————————————————————— */

/** ① video_stream —— 只有地址与状态，不含像素。 */
export interface VideoPayload {
  protocol: 'webrtc' | 'hls' | 'flv'
  state: 'ready' | 'not_ready' | 'degraded'
  degradedFrom?: string
  degradedReason?: string
  /** 只含有值的键。 */
  urls: Partial<Record<'webrtc' | 'hls' | 'flv', string>>
  sourceUrl: string
  path: string
  ready: boolean
  readers: number
  bytesPerSec?: number
  /** 降级后重试首选协议的倒计时（秒）。 */
  retryInSec?: number
}

/** ② image */
export interface ImagePayload {
  mime: string
  encoding: 'base64' | 'url'
  /** base64 串（不加 data: 前缀）或可访问 URL。 */
  data: string
  width?: number
  height?: number
  stampMs?: number
}

/** ③ scalar */
export interface ScalarPayload {
  value: number
  unit?: string
  min?: number
  max?: number
  label?: string
  rawPath?: string
}

/** ④ time_series_sample */
export interface TimeSeriesPayload {
  /** 采样时刻 ms（优先用 msg header.stamp）。 */
  t: number
  /** 扁平化的点分路径 → 数值。 */
  fields: Record<string, number>
  units?: Record<string, string>
}

/** ⑤ geo_pose —— crs 由后端盖章，坐标值永不改写，转换在前端完成。 */
export interface GeoPosePayload {
  lat: number
  lon: number
  alt?: number
  /** 朝向，正北为 0，顺时针。 */
  yawDeg?: number
  /** m/s */
  speed?: number
  crs: CRS
  /** m */
  accuracy?: number
  t: number
  label?: string
}

export interface TableColumn {
  key: string
  title: string
  type: 'number' | 'string' | 'boolean'
}

/** ⑥ table */
export interface TablePayload {
  columns: TableColumn[]
  rows: unknown[][]
  total: number
  truncated: boolean
}

/** ⑦ json（兜底） */
export interface JSONPayload {
  root: unknown
}

export type PayloadOf<T extends PayloadType> = T extends 'video_stream'
  ? VideoPayload
  : T extends 'image'
    ? ImagePayload
    : T extends 'scalar'
      ? ScalarPayload
      : T extends 'time_series_sample'
        ? TimeSeriesPayload
        : T extends 'geo_pose'
          ? GeoPosePayload
          : T extends 'table'
            ? TablePayload
            : T extends 'json'
              ? JSONPayload
              : unknown

export type FrameOf<T extends PayloadType> = Frame<PayloadOf<T>>

/* ——————————————————————————————
 * REST 请求体 / 响应（ARCH §7.3）
 * —————————————————————————————— */

export interface CreateDataSourceReq {
  name: string
  kind: DataSourceKind
  protocol: Protocol
  connParams: Record<string, unknown>
}

export interface UpdateDataSourceReq {
  name?: string
  protocol?: Protocol
  connParams?: Record<string, unknown>
  retryPolicy?: RetryPolicy
  enabled?: boolean
}

export interface TestResult {
  ok: boolean
  payloadType?: PayloadType
  sample?: Frame
  channels?: string[]
  error?: string
  hint?: string
  latencyMs: number
}

export interface CreateChannelReq {
  name: string
  payloadType?: PayloadType
  meta?: Record<string, unknown>
}

export type FooterFieldKey = 'lastUpdate' | 'rate' | 'latency' | 'source'

export interface CreateCardReq {
  channelId: string
  rendererType: string
  title: string
  unit?: string
  renderConfig: Record<string, unknown>
  displayOptions?: DisplayOptions
  layout: Layout
}

export interface UpdateCardReq {
  title?: string
  unit?: string
  rendererType?: string
  channelId?: string
  renderConfig?: Record<string, unknown>
  displayOptions?: DisplayOptions
  layout?: Layout
}

export interface CreateDashboardReq {
  name: string
  gridCols?: number
}

export interface UpdateDashboardReq {
  name?: string
  revision: number
  cards?: Card[]
  globalConfig?: Record<string, unknown>
}

export interface LayoutPatchItem {
  id: string
  x: number
  y: number
  w: number
  h: number
}

export interface SaveLayoutReq {
  revision: number
  layouts: LayoutPatchItem[]
}

export interface MediaMTXPath {
  name: string
  source?: string
  ready: boolean
  readers: number
  bytesPerSec?: number
  tracks?: string[]
}

export interface RuntimeConfig {
  /** 空串 = 未配置，地图渲染器显示配置提示。 */
  amapKey: string
  /** 高德安全密钥 securityJsCode；2021-12-02 后申请的 Key 必填，缺失时样式切换静默失败。 */
  amapSecurityCode: string
  /** 是否强制启用 WebGL 绘制（无 GPU/软件渲染/手机 WebView 必须开，否则样式不生效）。 */
  amapForceWebGL: boolean
  wsUrl: string
  protocolVersion: number
  secureContext: boolean
  videoBackends: ('webrtc' | 'hls' | 'flv')[]
  lanHosts: string[]
  /** 前端据此校正时钟差。 */
  serverTimeMs: number
}

export interface LanInfo {
  hosts: string[]
  httpPort: number
  httpsPort: number
  tlsEnabled: boolean
}

export interface HealthResp {
  ok: boolean
  goroutines: number
  uptimeSec: number
}

export interface ReadyResp {
  ready: boolean
}

export interface DeletedResp {
  deleted: boolean
}

/* ——————————————————————————————
 * ARCH §9.1 渲染器 manifest 契约
 * —————————————————————————————— */

export type ConfigField =
  | {
      key: string
      label: string
      type: 'number'
      min?: number
      max?: number
      step?: number
      unit?: string
      default: number
      help?: string
    }
  | {
      key: string
      label: string
      type: 'slider'
      min: number
      max: number
      step: number
      default: number
      help?: string
    }
  | { key: string; label: string; type: 'text'; placeholder?: string; default: string; help?: string }
  | {
      key: string
      label: string
      type: 'select'
      options: { label: string; value: string }[]
      default: string
      help?: string
    }
  | { key: string; label: string; type: 'switch'; default: boolean; help?: string }
  | { key: string; label: string; type: 'color'; default: string; help?: string }
  // CD-09：从最近一帧的 schemaHint 生成候选树的点选器
  | {
      key: string
      label: string
      type: 'field-picker'
      accept: 'number' | 'any'
      payloadTypes: PayloadType[]
      default?: string
      help?: string
    }
  // 多选字段：折线图 Y 轴字段、表格列选择
  | {
      key: string
      label: string
      type: 'multi-field'
      accept: 'number' | 'any'
      max?: number
      default?: string[]
      help?: string
    }
  // 阈值区段（本次用于仪表弧线配色）
  | { key: string; label: string; type: 'threshold-list'; unit?: string; default: ThresholdBand[]; help?: string }

export interface ThresholdBand {
  from: number
  to: number
  color: string
  label?: string
}

export interface RendererCapabilities {
  /** 需要环形缓冲历史（折线/地图）。 */
  needsHistory?: boolean
  supportsPause?: boolean
  supportsExport?: boolean
  /** 环形缓冲容量，默认 1200。 */
  maxPoints?: number
  /** 需要高德 key。 */
  requiresAMap?: boolean
}

export interface RendererManifest {
  type: string
  displayName: string
  icon: string
  /** 声明能吃的 payloadType。 */
  accepts: PayloadType[]
  /** 在这些类型上打「推荐」标签并置顶。 */
  recommended: PayloadType[]
  configSchema: ConfigField[]
  defaultConfig: Record<string, unknown>
  defaultLayout: { w: number; h: number; minW: number; minH: number }
  capabilities: RendererCapabilities
  /** 异步组件（为 P2 插件化预留）。 */
  component: () => Promise<Component>
}

/** 渲染器统一 props：由 CardHost / 配置抽屉预览共用。 */
export interface RendererProps {
  card: Card
  /** 已与 manifest.defaultConfig 合并后的最终配置。 */
  config: Record<string, unknown>
}
