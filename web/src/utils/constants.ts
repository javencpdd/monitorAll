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
/**
 * 数据源详情抽屉宽度（只读）。
 * 比卡片抽屉（360）略宽：配置值是「标签 + 长 URL / 长路径」，太窄会频繁折行。
 */
export const DATA_SOURCE_DETAIL_WIDTH = 380

/* ——— 视频（ARCH §2.3 / §11.3） ——— */
export const VIDEO_NEGOTIATE_TIMEOUT_MS = 3000
/**
 * HLS 单独的协商超时预算。
 *
 * 为什么比通用值大 4 倍：MediaMTX 的 on-demand 拉流源是「收到第一个请求才开始拉上游」，
 * 实测完全冷启动到 ready 约 8 秒（源刚空闲时约 2 秒）。而 hls.js 在首帧到达前 attach 不会
 * resolve，3 秒窗口必然把它误判成「HLS 协商超时」——流其实马上就好了。
 * WebRTC/FLV 仍用 3 秒：WHEP 是同步 HTTP 协商，慢就是真的连不上，拖长只会让用户干等。
 */
export const VIDEO_HLS_TIMEOUT_MS = 12000
export const VIDEO_RETRY_PREFERRED_DEFAULT_SEC = 30
/**
 * MediaMTX 路径（流名）的合法规则。
 *
 * ⚠️ **必须与后端逐字一致**：本正则与 `server/internal/media/mediamtx.go` 的
 * `mediaMTXPathRe` 是同一条规则的两个副本，由 `scripts/source-guards.mjs` 守卫 P 比对。
 * 任何一侧单独改动都会造成"前端放行、后端 40001 拒绝"的静默不一致 —— 这正是本项目
 * 反复踩的坑。改之前先看守卫，改完必须同步两侧。
 *
 * 规则来源：MediaMTX 拒绝非法路径时返回
 *   `invalid path name: can contain only alphanumeric characters, underscore, dot, minus, slash`
 * 即只允许 `A-Za-z0-9`、下划线、点、减号，并以 `/` 分段：
 *   - 不允许 scheme / `?` / `#` / 空格 / 冒号
 *   - 不允许前导斜杠、尾斜杠、连续斜杠（`live//cam`）
 *
 * ⚠️ 前端必须自己拦：一旦把整条地址（`rtmp://host:1936/live/x`）填进这个字段，
 * 播放地址会拼成 `/rtmp://host:1936/live/x/index.m3u8`，MediaMTX 返回 500，
 * 前端只看到「HLS 协商超时」—— 真正的错误（路径名非法）被完全掩盖。
 */
/**
 * MediaMTX 路径名的合法形态。
 *
 * ⚠️ **必须与后端 `mediaMTXPathRe` 逐字等价**：
 * `server/internal/media/mediamtx.go:30` 是 `^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*$`。
 *
 * 曾经这里写成 `/^[A-Za-z0-9_.\-/]+$/`（把 `/` 丢进字符类），后果是
 * `/live/cam1`、`live//cam1` 都能骗过前端，却被后端 40001 拒掉；而 Step 2 上
 * formError 不显示、错误只靠 toast 一闪而过 —— 用户的体感是"点了创建数据源
 * 完全没反应"。守卫 Q 断言两边口径一致，改松必然红。
 */
export const MEDIA_MTX_PATH_RE = /^[A-Za-z0-9_.-]+(\/[A-Za-z0-9_.-]+)*$/

/**
 * 归一化 MediaMTX 路径：去首尾空白与首尾斜杠。
 *
 * `/live/cam1` → `live/cam1`。前导斜杠是很自然的手误（从地址栏复制的习惯），
 * 不该让用户对着报错猜；后端给通道名做的就是同一个 Trim（`source.go:192`），
 * 这里提前到前端，保证"校验用的值"与"提交的值"与"通道名"三者一致。
 */
export function normalizeMediaMtxPath(path: string): string {
  return path.trim().replace(/^\/+/, '').replace(/\/+$/, '')
}

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

/* ——— 全局设置（品牌 + 天气） ——— */
/**
 * 设置抽屉宽度：比卡片抽屉（CARD_DRAWER_WIDTH=360）宽。
 * 品牌/天气/关于三组内容更长，360 会把「刷新间隔」挤到需要横向滚动。
 */
export const SETTINGS_DRAWER_WIDTH = 420
/** 品牌默认值：后端未下发、或用户清空项目名之后的回落值。 */
export const DEFAULT_BRAND_TITLE = 'MonitorAll'
export const DEFAULT_BRAND_SUBTITLE = '多源异构监控看板'
/** 文本长度上限必须与后端逐字一致，否则用户能输入超长值、保存时才吃 400。
 *  对应 server/internal/config/config.go：BrandingTitleMaxRunes / SubtitleMaxRunes / WeatherLabelMaxRunes。 */
export const BRAND_TITLE_MAX = 40
export const BRAND_SUBTITLE_MAX = 60
export const WEATHER_LABEL_MAX = 40
/** Logo 上传限制：类型与大小（前端先拦一次，别等后端 413 才报错）。 */
export const LOGO_MAX_BYTES = 200 * 1024
export const LOGO_ACCEPT_MIME: string[] = ['image/png', 'image/jpeg', 'image/webp']
export const LOGO_ACCEPT_ATTR = 'image/png,image/jpeg,image/webp'
/**
 * 天气刷新间隔允许范围与默认值（分钟）。
 * 与后端 config.go 的 WeatherRefreshMinMin/Max、DefaultWeatherRefreshMin 对齐；
 * 后端对越界值是**夹取**而非报错，前端则明确拦截并解释（不让用户看到自己填的值被悄悄改掉）。
 */
export const WEATHER_REFRESH_MIN = 1
export const WEATHER_REFRESH_MAX = 120
export const WEATHER_REFRESH_DEFAULT_MIN = 10
export const WEATHER_PROVIDER_DEFAULT = 'open-meteo'
/** 地理定位（浏览器）等待上限。 */
export const GEOLOCATE_TIMEOUT_MS = 10000

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
/**
 * 流媒体地址：rtmp / rtmps / rtsp / rtsps。
 * 允许 `user:pass@` 凭据段——RTSP 摄像头（海康/大华）普遍要求 URL 内带账号密码，
 * MediaMTX 拉流时需要原样保留，故校验正则必须放行。
 */
export const STREAM_URL_RE = /^(?:rtmp|rtmps|rtsp|rtsps):\/\/(?:[^\s:@/]+(?::[^\s@/]*)?@)?[^\s/]+(?::\d+)?\/[^\s]+$/i
/** 提取流地址协议（返回小写 rtmp/rtmps/rtsp/rtsps），非法返回空串。 */
export const STREAM_SCHEME_RE = /^(rtmp|rtmps|rtsp|rtsps):\/\//i
export const WS_URL_RE = /^wss?:\/\/[^\s]+$/i
export const HTTP_URL_RE = /^https?:\/\/[^\s]+$/i

/* ——— v3 MapBoard（只新增，不改动上面任何既有值） ——— */

/** 卡片边框宽度（px）。chrome 高度的组成项之一，见 CARD_CHROME_H。 */
export const CARD_BORDER_PX = 1

/**
 * ⭐ chrome 高度单一真源（U-6）：必须是**函数**而不是常量，因为页脚可关。
 *
 * 现状曾存在双真源（JS 常量 `CARD_HEADER_H/CARD_FOOTER_H` 与 CSS 变量
 * `--ma-card-header-h/--ma-card-footer-h` 各写一份），MapBoard 计算外框时
 * 若取到不一致的值会产生「卡片外框高度差 24px、视频区被压扁」的静默偏差。
 * 自此所有外框高度计算一律走本函数。
 */
export function CARD_CHROME_H(showFooter: boolean): number {
  return CARD_HEADER_H + (showFooter ? CARD_FOOTER_H : 0) + CARD_BORDER_PX * 2
}

/** 视口剔除迟滞：退出方向外扩的像素（进入方向严格、退出方向宽松）。 */
export const MAP_CULL_HYSTERESIS_PX = 64
/**
 * grace period（visible→hidden 的防抖等待）。
 * 终裁值 1000ms：一次「瞥一眼别处再回来」的平移通常 >1s，一次误触/惯性抖动通常 <300ms。
 * 注意：hidden→visible 方向【不加 grace】，恢复必须立即，否则 P-04/P-05 超标。
 */
export const MAP_CULL_GRACE_MS = 1000
/**
 * 视频降载阶段一 / 阶段二。两者均自「出视野」t0 起算，**不串行叠加**。
 * T2=10000 与 MediaMTX sourceOnDemand「无读者 10s 停止拉上游」对齐：
 * 超过 10s 上游本来就没了，继续维持 peer 只是为一个已经死了的流付带宽。
 */
export const MAP_VIDEO_SHED_T1_MS = 1000
export const MAP_VIDEO_SHED_T2_MS = 10000
/** 节点位置 stale 判定阈值（A9）。 */
export const MAP_STALE_TIMEOUT_MS = 3000
/** 几何保存 debounce（拖拽结束后）。 */
export const MAP_GEOM_SAVE_DEBOUNCE_MS = 300
/** 节点投影位移小于该值则跳过该卡的同步（过滤微抖动）。 */
export const MAP_SYNC_EPSILON_PX = 0.5
/** 节点位置更新节流（与地图轨迹同一档 10Hz）。 */
export const MAP_NODE_THROTTLE_MS = MAP_THROTTLE_MS

/* —— 卡片默认外框尺寸（C9 + 订正：外框口径） —— */
export const MAP_CARD_DEFAULT_W = 320
export const MAP_CARD_DEFAULT_H = 220
/** 视频卡外框宽（播放内容区 352×198 锁 16:9，外框高由 CARD_CHROME_H 推导）。 */
export const MAP_CARD_VIDEO_W = 354
export const MAP_CARD_VIDEO_CONTENT_H = 198
/**
 * 地图卡编辑态拖拽把手（标题栏）高度。
 *
 * ⚠️ 这是**外壳自有**的 chrome，与 CardFrame 的 header/footer 不是一回事（后者在 body 内）。
 * 必须与 `.mb-card__grip` 的 CSS 高度一致，否则 C26 的开发期断言会误报 ——
 * 因此模板里用内联 style 写它，不做 CSS/JS 双真源。
 */
export const MAP_CARD_GRIP_H = 24

/** 同节点多卡的级联偏移（C8）。 */
export const MAP_CASCADE_DX = 20
export const MAP_CASCADE_DY = 16
/** 同屏上限（A4）：非视频软上限只提示，视频硬上限拒绝打开。 */
export const MAP_VISIBLE_NONVIDEO_SOFT_LIMIT = 6
export const MAP_VISIBLE_VIDEO_HARD_LIMIT = 4
export const MAP_MAX_NODES = 20
/** 拖拽贴边吸附阈值（A3）。 */
export const MAP_EDGE_SNAP_PX = 8
/** 位移小于该值不判定为拖拽（防误触）。 */
export const MAP_DRAG_THRESHOLD_PX = 3
/** will-change 上限：合成层数量必须封顶，否则 20 卡会吃掉大量显存。 */
export const MAP_WILL_CHANGE_MAX = 8
/** 节点轨迹默认点数。 */
export const MAP_TRAIL_DEFAULT = 60

/** 诊断层快照刷新间隔（诊断数据全程不进响应式，靠定时拉取与业务渲染解耦）。 */
export const DIAG_REFRESH_MS = 500
