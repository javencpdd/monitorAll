/**
 * v3 MapBoard（地图看板）前端类型。
 *
 * 字段与后端 Go struct 的 json tag 逐字对齐（camelCase、时间毫秒 number）。
 * 契约来源：后端 `/api/v1/mapboards/*` 实测响应 + 裁决 C1/C2/C7/C8。
 */
import type { CRS, DashboardWithCards, PayloadType } from '@/types'

/** 看板种类：`grid` 网格（v2 既有）/ `map` 地图看板（v3 新增）。 */
export type BoardKind = 'grid' | 'map'

/**
 * 卡片相对节点的锚定模式。
 *
 * ⚠️ 只有 `node` 一种：跟随节点（x/y 解释为相对节点投影点的偏移）。
 *
 * 曾经还有 `'free'`（节点被删后由服务端写入，表示"已脱离节点"），已移除：
 *   1. 双真源 —— "未锚定"已由 `nodeId = ''` 完整表达，再引入 anchor_mode
 *      就是同一事实两种表示，此后每个读方都要回答"两者冲突时以谁为准"；
 *   2. 粘滞 bug —— 会产生 `node_id='n_x'` 且 `anchor_mode='free'` 的自相矛盾行；
 *   3. 零消费方。
 * 后端为兼容存量数据仍**可能**回传 `free`（store 层保留标记），前端不读、不写、不分支。
 */
export type AnchorMode = 'node'

/**
 * 卡片在地图看板中的几何（**容器 px**，与网格 `layout` 的格单位语义隔离，C7）。
 * 缺失时用 `EMPTY_MAP_GEOM` 兜底，由调用方按渲染器类型推导实际尺寸。
 *
 * `v` 是契约版本（当前 1），**由后端盖章写入，前端不送**：
 * `PUT /mapboards/:id/geometry` 的服务端实现直接用 `model.MapGeometryVersion` 覆盖 V，
 * 请求体里带 v 也不会被采纳。前端只在读侧接收，为将来的 v2「双读单写」留口。
 */
export interface MapGeometry {
  /** 契约版本（后端盖章；读侧字段，写请求不含）。 */
  v?: number
  x: number
  y: number
  w: number
  h: number
}

/** 几何缺失时的兜底（全 0 表示「由前端按渲染器默认值推导」）。 */
export const EMPTY_MAP_GEOM: MapGeometry = { x: 0, y: 0, w: 0, h: 0 }

/**
 * 后端返回的几何是否**已初始化**。
 *
 * 未初始化在接口上表现为**字段缺失**（`store` 层对 `map_geom = '{}'` 不回填），
 * 所以这里只判"字段在不在 + 有没有尺寸"，不依赖 `v`。
 */
export function isMapGeomInitialized(g: MapGeometry | undefined | null): boolean {
  return !!g && (g.w > 0 || g.h > 0)
}

/** 地图节点（落库实体）。 */
export interface MapNode {
  id: string
  boardId: string
  /** 绑定的位置通道；空串 = 纯静态节点（此时 fallbackLat/Lon 必填）。 */
  channelId: string
  name: string
  /**
   * 兜底纬度 / 经度。
   *
   * ⚠️ **可空，且 null ≠ 0**：后端用 NULL 表达"没有兜底坐标"，`(0, 0)` 是
   * 几内亚湾上一个**合法**坐标。拿 0 当哨兵会让没配兜底的节点被画到非洲西海岸
   * （后端 DDL 注释原文：「不能用 0 做哨兵」）。
   * 纯静态节点（无通道）必填；绑了通道的节点可以没有。
   */
  fallbackLat: number | null
  fallbackLon: number | null
  /**
   * 兜底坐标的坐标系（后端列名 `fallback_crs`）—— 同时是**帧内无 crs 时的声明兜底值**。
   *
   * ⚠️ 解析优先级（口径差已裁决并已实现，2026-02 · 裁决 a）：
   *   1. **帧内 `payload.crs` 合法** → 以帧为准（后端逐帧盖章，`model/payload.go` 的
   *      `GeoPosePayload.CRS`，后端注释称其为"坐标语义的唯一权威来源"）；
   *   2. 帧内缺失 / 空串 / 未知值 / 非字符串 → 回落到这个字段。
   *   判定集中在 `useGeoPosition.resolveFrameCrs`（唯一实现点，两个解析分支共用）。
   *   所以它的名字是**如实的**："fallback" 既指兜底坐标，也指帧内没带 crs 时的兜底声明。
   *   改它**不会**再改变"帧里已带坐标系"那些数据的解读方式 —— 这是本次修复的目的。
   */
  fallbackCrs: CRS
  /** 通道的 payload 类型（后端回传，前端仅用于展示与校验提示）。 */
  payloadType: PayloadType
  /** JSON / table 通道必填；geo_pose 免填（后端已归一化）。 */
  latPath: string
  lonPath: string
  // ⛔ 已删除 yawPath / yawUnit：节点是"圆点 + 名字"，不画朝向。
  // 后端列仍保留（删列要迁移，风险大于收益），但前端不读不写。
  //
  // 卡片侧朝向不受影响，但**不经过 useGeoPosition**（此处原先的注释写错了）：
  // MapRenderer 自己读 `config.yawPath` / `config.yawUnit` 算角度
  // （见 MapRenderer.vue 第 41-42 行取值、第 119-120 行换算），是独立的实现路径。
  // 而 `useGeoPosition` 的 yaw 支持（GeoPositionOptions.yawPath / yawIsRad，两者都是可选）
  // 现在唯一的使用者是 useNodePosition —— 本类型删字段后它已不再传这两个参数，
  // 因此 useGeoPosition 的 yaw 代码目前没有调用方；保留它是为了不扩大本次改动范围。
  /** 纯静态节点标记（无通道）。 */
  static: boolean
  createdAt: number
  updatedAt: number
}

/** 节点视图（列表接口额外带的运行时信息）。 */
export interface MapNodeView extends MapNode {
  /** 绑定的通道已不存在（孤儿引用）。 */
  channelMissing: boolean
  channelName: string
  rateLimitHz: number
  /** 后端建议的限频值（H8：默认 10Hz）。 */
  recommendedRateLimitHz: number
  /** 是否配置了兜底坐标。 */
  hasFallback: boolean
}

export interface CreateMapNodeReq {
  /**
   * 看板 revision（乐观锁）。
   *
   * ⚠️ **必填，不是可选的**。后端把「节点写入」与「看板 revision 自增」放在**同一个事务**里，
   * 用 `WHERE revision = ?` 做 CAS，所以没有这个字段会直接 40001
   * （`store/mapnode.go` 的 `bumpMapBoardRevision` 要求 `expect > 0`）。
   * 调用方应传 `dashboardStore.current.revision`；成功后**必须**用响应里的
   * `MapNodeMutationResp.revision` 回写本地，否则下一次操作会 40009。
   * （这两条曾经都漏了，导致「点添加节点毫无反应」。）
   */
  revision: number
  name: string
  channelId?: string
  fallbackLat?: number
  fallbackLon?: number
  fallbackCrs?: CRS
  latPath?: string
  lonPath?: string
  // yawPath / yawUnit 已从节点契约删除（节点不画朝向），见 MapNode 同名注释。
}

/**
 * 节点变更请求（**补丁语义**）。
 *
 * ⚠️ 后端 `mergeMapNodePatch` 是"没提 ≠ 要清空"：空串 / nil 一律视为未提供、沿用旧值。
 * 所以想**清空**某个字段时不能靠"省略"（省略 = 不动），也不能靠发 0（0 是合法坐标/长度）。
 * 后果：兜底坐标一旦写入就无法通过本接口清空 —— 这是已知缺口，改它需要后端加显式清除位。
 */
export type UpdateMapNodeReq = Partial<CreateMapNodeReq>

/** 节点变更响应（级联语义：unbind 解绑卡片 / delete 级联删卡）。 */
export interface MapNodeMutationResp {
  node: MapNode
  revision: number
  unbound: number
  deleted: number
}

/**
 * 几何保存项（`PUT /mapboards/:id/geometry`）。
 *
 * ⚠️ **不含 v**：契约版本由服务端盖章（`V: model.MapGeometryVersion`），
 * 请求体里送 v 不会被采纳 —— 前端别自作主张加字段，加了也是死字段。
 */
export interface MapGeometryItem {
  id: string
  x: number
  y: number
  w: number
  h: number
}

/** 几何保存响应：脱离 revision 域（C7），只回计数。 */
export interface MapGeometryResp {
  saved: number
  skipped: number
  updatedAt: number
}

/** 地图看板视角配置（存 `dashboards.globalConfig`，C6）。 */
export interface MapViewConfig {
  /** [lng, lat]，GCJ-02。 */
  center: [number, number]
  zoom: number
  /** 高德 mapStyle；'amap://styles/satellite' 为哨兵值，走独立图层。 */
  mapStyle: string
  /** 节点坐标的输入坐标系声明（C-04 常驻展示）。 */
  sourceCRS: CRS
}

export const DEFAULT_MAP_VIEW: MapViewConfig = {
  // 默认中心：用户工作场地（深圳），新建地图看板后第一眼就能看到自己所在地，不用手动拖。
  center: [113.87849626322088, 22.90987355103576],
  zoom: 15,
  mapStyle: 'amap://styles/dark',
  sourceCRS: 'WGS-84',
}

/** 地图看板聚合体（`GET /mapboards/:id`）：看板 + 卡片 + 节点视图。 */
export interface MapBoardDetail extends DashboardWithCards {
  nodes: MapNodeView[]
  /** 列表接口额外带的节点计数（聚合接口也可能带）。 */
  nodeCount?: number
}

/** 从 globalConfig（JSON blob）安全解析视角配置。 */
export function parseMapView(raw: Record<string, unknown> | undefined): MapViewConfig {
  if (!raw) return { ...DEFAULT_MAP_VIEW }
  const center = raw.center
  const parsed: MapViewConfig = { ...DEFAULT_MAP_VIEW }
  if (Array.isArray(center) && center.length === 2) {
    const lng = Number(center[0])
    const lat = Number(center[1])
    if (Number.isFinite(lng) && Number.isFinite(lat)) parsed.center = [lng, lat]
  }
  const zoom = Number(raw.zoom)
  if (Number.isFinite(zoom) && zoom >= 3 && zoom <= 20) parsed.zoom = zoom
  if (typeof raw.mapStyle === 'string' && raw.mapStyle.length > 0) parsed.mapStyle = raw.mapStyle
  if (raw.sourceCRS === 'WGS-84' || raw.sourceCRS === 'GCJ-02' || raw.sourceCRS === 'BD-09') {
    parsed.sourceCRS = raw.sourceCRS
  }
  return parsed
}
