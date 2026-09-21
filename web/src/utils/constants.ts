/**
 * 🚨 全前端唯一常量源。
 * 任何文件出现裸数字都应先检查这里；确需新增的常量一律加在本文件并注明来源。
 */
import type { Status, DataSourceKind, PayloadType, Protocol, CRS } from '@/types'

/* ——— 网格布局（ARCH §1.8 / TASKS §1.7） ——— */
export const GRID_COLS = 12
export const ROW_HEIGHT = 30
export const MARGIN: [number, number] = [12, 12]
export const MIN_CARD_W = 2
export const MIN_CARD_H = 2
/** 新建卡片的默认落位尺寸（用于自动排布兜底）。 */
export const DEFAULT_CARD_W = 4
export const DEFAULT_CARD_H = 4

/* ——— 环形缓冲与内存保护（ARCH §10.2 / §12.2） ——— */
export const DEFAULT_RING_CAPACITY = 1200
/** 单看板卡片软上限，超过仅提示不阻断（ARCH F5）。 */
export const CARD_SOFT_LIMIT = 40

/* ——— WebSocket（ARCH §6.1） ——— */
export const WS_PROTOCOL_VERSION = 1
export const WS_RECONNECT_BASE_MS = 1000
export const WS_RECONNECT_MAX_MS = 30000
export const WS_RECONNECT_JITTER = 0.2
/** 服务端要求连接后必须在此时间内发 hello。 */
export const WS_HELLO_DEADLINE_MS = 5000
/** 客户端多久未收到任何消息判定为僵死连接。 */
export const WS_STALE_TIMEOUT_MS = 45000
/** 收到 ping 后的回包时限（服务端 3s 内未收到 pong 判定失败）。 */
export const WS_PONG_TIMEOUT_MS = 3000
export const WS_PATH = '/api/v1/ws'

/** WS 关闭码：正常关闭 / 客户端判定僵死后主动关闭。 */
export const WS_CLOSE_NORMAL = 1000
export const WS_CLOSE_STALE = 4000

/* ——— REST ——— */
export const API_PREFIX = '/api/v1'
export const API_TIMEOUT_MS = 10000
/** ARCH §7.3：GET /channels/{id}/sample?timeoutMs */
export const SAMPLE_TIMEOUT_MS = 3000
/** 数据源测试连接的前端等待上限（ARCH T-22 要求 ≤5s 出结果）。 */
export const TEST_CONNECT_TIMEOUT_MS = 5000

/* ——— 刷新与节流（ARCH §10.2 三层高频防护） ——— */
/** 同通道在此窗口内只保留最后一帧（对应后端 frameBatchMs）。 */
export const FRAME_PENDING_WINDOW_MS = 20
/** ECharts setOption 节流间隔 → 10Hz。 */
export const ECHARTS_THROTTLE_MS = 100
/** 地图轨迹更新节流间隔 → 10Hz。 */
export const MAP_THROTTLE_MS = 100
/** 通道级运行统计（Hz/延迟）刷新间隔。 */
export const STATS_FLUSH_INTERVAL_MS = 1000

/* ——— 卡片状态推导（ARCH §10.3） ——— */
export const FIRST_FRAME_WAIT_MS = 3000
export const CARD_HEADER_H = 32
export const CARD_FOOTER_H = 24
export const TOP_BAR_HEIGHT = 48
export const SOURCE_PANEL_WIDTH = 260
export const CARD_DRAWER_WIDTH = 360

/* ——— 视频（ARCH §2.3 / §11.3） ——— */
export const VIDEO_NEGOTIATE_TIMEOUT_MS = 3000
export const VIDEO_RETRY_PREFERRED_DEFAULT_SEC = 30
export const VIDEO_PROTOCOL_LABEL: Record<'webrtc' | 'hls' | 'flv', string> = {
  webrtc: 'WebRTC',
  hls: 'HLS',
  flv: 'HTTP-FLV',
}
/** 各降级目标的典型延迟提示文案。 */
export const VIDEO_LATENCY_HINT: Record<'webrtc' | 'hls' | 'flv', string> = {
  webrtc: '≤0.5s',
  hls: '2-3s',
  flv: '1-2s',
}
export const VIDEO_MAX_MSE_BUFFER_SEC = 3

/* ——— 图表 / 表格 / JSON 树默认值 ——— */
export const DEFAULT_WINDOW_SEC = '60'
export const LINE_MAX_FIELDS = 8
export const DEFAULT_MAX_ROWS = 1000
export const DEFAULT_MAX_NODES = 5000
export const DEFAULT_EXPAND_DEPTH = 3
export const DEFAULT_DECIMALS = 2
export const MAX_EXPORT_ROWS = 100000
/** JSON 树单个容器的安全深度，超出后折叠。 */
export const JSON_TREE_MAX_DEPTH = 12

/* ——— 地图（ARCH §13） ——— */
export const MAP_DEFAULT_ZOOM = 17
export const MAP_DEFAULT_TRAIL = 200
/** 无数据时的默认地图中心（北京，仅作为初始视角，不代表业务含义）。 */
export const DEFAULT_MAP_CENTER: [number, number] = [116.397428, 39.90923]
export const AMAP_API_VERSION = '2.0'
export const AMAP_PLUGINS: string[] = []
/** 转换后的目标坐标系恒为高德所需的 GCJ-02。 */
export const MAP_TARGET_CRS: CRS = 'GCJ-02'

/* ——— UI ——— */
export const MAX_ERROR_LOG = 100
export const TOAST_DURATION_MS = 3500
export const DEFAULT_FOOTER_FIELDS: string[] = ['lastUpdate', 'rate', 'latency', 'source']
export const STATUS_TEXT: Record<Status, string> = {
  idle: '未连接',
  connecting: '连接中',
  online: '在线',
  reconnecting: '重连中',
  error: '错误',
  degraded: '已降级',
  offline: '离线',
}
export const STATUS_COLORS: Record<Status, string> = {
  idle: 'var(--ma-status-idle)',
  connecting: 'var(--ma-status-connecting)',
  online: 'var(--ma-status-online)',
  reconnecting: 'var(--ma-status-reconnecting)',
  error: 'var(--ma-status-error)',
  degraded: 'var(--ma-status-degraded)',
  offline: 'var(--ma-status-offline)',
}
export const KIND_TEXT: Record<DataSourceKind, string> = {
  video: '视频源',
  ros: 'ROS',
  http: 'HTTP 接口',
}
export const PROTOCOL_TEXT: Record<Protocol, string> = {
  rtmp: 'RTMP',
  rtsp: 'RTSP',
  ros1: 'ROS1',
  ros2: 'ROS2',
  'http-poll': 'HTTP 轮询',
}
export const PAYLOAD_TYPE_TEXT: Record<PayloadType, string> = {
  video_stream: '视频流状态',
  image: '图像',
  scalar: '标量',
  time_series_sample: '时序样本',
  geo_pose: '地理位姿',
  table: '表格',
  json: 'JSON',
  log_line: '日志行',
  state_enum: '状态枚举',
  histogram_bins: '直方图',
  point_cloud: '点云',
}
export const SECURE_FIELD_MASK = '***'
/** 折线图系列配色（CSS 变量名，渲染时由 cssColor 解析为具体色值）。 */
export const CHART_SERIES_COLORS: string[] = [
  '--ma-accent',
  '--ma-status-connecting',
  '--ma-status-reconnecting',
  '--ma-status-degraded',
  '--ma-status-error',
  '--ma-accent-hover',
  '--ma-text-2',
  '--ma-status-offline',
]
export const TMP_ID_PREFIX = 'tmp_'
/** 临时 UI id（禁止落库），见 TASKS §1.2。 */
export const PREVIEW_CARD_ID = `${TMP_ID_PREFIX}preview`

/* ——— URL 正则（T-22 实时 URL 校验） ——— */
export const RTMP_URL_RE = /^rtmp:\/\/[^\s/]+(?::\d+)?\/[^\s]+$/i
export const WS_URL_RE = /^wss?:\/\/[^\s]+$/i
export const HTTP_URL_RE = /^https?:\/\/[^\s]+$/i
