/**
 * 地理位姿数据源（从 `MapRenderer.vue` 抽出，节点与地图卡**共用同一份**）。
 *
 * 抽出前这些逻辑（字段路径解析 / `root.` 前缀兼容 / 阈值节流 / 尾迹采集）
 * 只存在于 `MapRenderer.vue` 内部，地图看板的节点无法复用，必然复制一份 → 行为漂移。
 *
 * 合规红线：本文件**不做**坐标转换，只解析出原始坐标；转换唯一入口是 `utils/geo.ts`（C-05）。
 * 调用方拿到的 `Pos` 携带 `sourceCRS`，何时转换由调用方决定。
 *
 * 通道差异（note/04 §4.5 / §4.6）：
 *   - `geo_pose`：lat/lon 直读，无需字段路径（ROS NavSatFix 由后端归一化）
 *   - `json` / `table`：必须配 latPath/lonPath，且后端把原始响应包成 `{root: ...}`
 */
import { computed, type ComputedRef, type MaybeRefOrGetter, type Ref } from 'vue'
import { onBeforeUnmount, ref, shallowRef, toValue, watch } from 'vue'
import type { Frame } from '@/types'
import type { CRS } from '@/types'
import type { RingBuffer } from '@/utils/ringbuffer'
import { isValidLngLat, toGCJ02, type LngLat } from '@/utils/geo'
import { getByPath } from '@/utils/jsonpath'
import { useFrameFeed } from '@/composables/useFrameFeed'
import { MAP_STALE_TIMEOUT_MS } from '@/utils/constants'

/** 一个坐标系的**来源**：`frame` = 帧内自带（后端盖章）／`declared` = 调用方声明值兜底。 */
export type CrsSource = 'frame' | 'declared'

/** 解析出的原始位姿（未转换坐标系）。 */
export interface GeoPos {
  lat: number
  lon: number
  /** 朝向（度，正北为 0，顺时针）。 */
  yaw?: number
  /** 数据产生时刻（ms）。 */
  t: number
  /** **实际用来解读这一帧**的坐标系（帧内盖章优先，回落声明值）。 */
  sourceCRS: CRS
  /** 上面的 `sourceCRS` 是从哪来的 —— UI 需要如实展示"按什么在解读"，别猜。 */
  crsSource: CrsSource
}

export interface GeoPositionOptions {
  channelId: MaybeRefOrGetter<string | undefined>
  /**
   * 声明坐标系（节点 `fallbackCrs` / 卡片 renderConfig）。
   *
   * ⚠️ 它现在是**兜底值**，不再是"唯一生效来源"：`extractGeoPos` 会先看帧内
   * `payload.crs`（后端逐帧盖章，见 `model/mapboard.go:134` / `model/payload.go:67`，
   * 后端视其为"坐标语义的唯一权威来源"），**只有在帧内缺失或非法时**才回落到这里。
   * 判定逻辑集中在 `resolveFrameCrs` 一处，两个解析分支共用。
   */
  sourceCRS: MaybeRefOrGetter<CRS>
  latPath?: MaybeRefOrGetter<string>
  lonPath?: MaybeRefOrGetter<string>
  yawPath?: MaybeRefOrGetter<string>
  /** true 表示 yaw 单位是弧度，需要转成度。 */
  yawIsRad?: MaybeRefOrGetter<boolean>
  /** 尾迹点数（0 = 只要最新点）。 */
  trailLength?: MaybeRefOrGetter<number>
  /** stale 判定阈值。 */
  staleTimeoutMs?: MaybeRefOrGetter<number>
}

export interface GeoPositionSource {
  /** 最新一帧解析出的位姿（已随 version 更新）。 */
  latest: Ref<GeoPos | undefined>
  /** 帧版本号：唯一响应式触发源。 */
  version: ComputedRef<number>
  /** 超过 staleTimeoutMs 未更新 → true。 */
  stale: ComputedRef<boolean>
  sourceCRS: ComputedRef<CRS>
  /** 命令式读历史缓冲（尾迹 / 图表专用）。 */
  getBuffer: () => RingBuffer<Frame> | undefined
  /** 取最近 n 个合法点（**原始坐标系**，按时间升序）。 */
  trail: (n: number) => GeoPos[]
}

/** 默认字段候选（note/04 §4.5：留空时按顺序尝试）。 */
const DEFAULT_LAT_CANDIDATES = ['latitude', 'lat', 'gps.lat', 'pose.position.lat']
const DEFAULT_LON_CANDIDATES = ['longitude', 'lon', 'lng', 'gps.lon', 'gps.lng', 'pose.position.lon']

/**
 * JSON 通道的 payload 被后端包成 `{root: 原始响应}`（model.JSONPayload），
 * 用户填路径时几乎必然漏掉 `root.` 前缀 —— 这里两种都试，命中即用。
 */
function firstNumberAt(payload: Record<string, unknown>, paths: readonly string[]): number | undefined {
  for (const path of paths) {
    for (const p of [path, `root.${path}`]) {
      const value = getByPath(payload, p)
      if (typeof value === 'number' && Number.isFinite(value)) return value
    }
  }
  return undefined
}

/** 三个合法坐标系（与后端 `model.CRS` 的取值一致）。 */
const CRS_VALUES: readonly string[] = ['WGS-84', 'GCJ-02', 'BD-09']

function isKnownCRS(v: string): v is CRS {
  return CRS_VALUES.includes(v)
}

/**
 * 决定"这一帧按哪个坐标系解读" —— **唯一判定点**（两个解析分支共用，不许各写一份）。
 *
 * 规则（裁决 a）：帧内 `payload.crs` 合法 → 用它（`frame`）；否则回落声明值（`declared`）。
 *
 * 容错是硬要求：`payload.crs` 缺失、空串、未知字符串、非字符串 → **一律回落**。
 * 不抛异常、也不"猜"一个值 —— 猜错的表现是地图上静默平移 300~700 米，本项目最不能
 * 容忍的就是这类无声偏差。json/table 通道、以及 http 拉 JSON 的场景本就**不带 crs**，
 * 回落到声明值是**正常路径**，不是异常。
 */
export function resolveFrameCrs(
  payloadCRS: unknown,
  declared: CRS,
): { crs: CRS; source: CrsSource } {
  if (typeof payloadCRS === 'string') {
    const v = payloadCRS.trim()
    if (isKnownCRS(v)) return { crs: v, source: 'frame' }
  }
  return { crs: declared, source: 'declared' }
}

/** 从一帧里解析位姿；解析不出返回 null。 */
export function extractGeoPos(
  frame: { payload: unknown; publishedTs: number } | undefined,
  opts: { sourceCRS: CRS; latPath: string; lonPath: string; yawPath: string; yawIsRad: boolean },
): GeoPos | null {
  if (!frame) return null
  const payload = frame.payload as Record<string, unknown> | undefined
  if (!payload) return null

  // 帧内 crs 优先、非法/缺失回落声明值。两个分支共用这一次判定。
  const { crs: frameCRS, source: crsSource } = resolveFrameCrs(payload.crs, opts.sourceCRS)

  // ① geo_pose：lat/lon 直读
  const directLat = typeof payload.lat === 'number' ? payload.lat : Number.NaN
  const directLon = typeof payload.lon === 'number' ? payload.lon : Number.NaN
  if (Number.isFinite(directLat) && Number.isFinite(directLon)) {
    return {
      lat: directLat,
      lon: directLon,
      ...(typeof payload.yawDeg === 'number' ? { yaw: payload.yawDeg } : {}),
      t: typeof payload.t === 'number' ? payload.t : frame.publishedTs,
      sourceCRS: frameCRS,
      crsSource,
    }
  }

  // ② json / table：按字段路径取（点分与斜杠均可，见 utils/jsonpath）
  const latCandidates = opts.latPath ? [opts.latPath] : DEFAULT_LAT_CANDIDATES
  const lonCandidates = opts.lonPath ? [opts.lonPath] : DEFAULT_LON_CANDIDATES
  const lat = firstNumberAt(payload, latCandidates)
  const lon = firstNumberAt(payload, lonCandidates)
  if (lat === undefined || lon === undefined) return null

  const rawYaw = opts.yawPath ? firstNumberAt(payload, [opts.yawPath]) : undefined
  const yaw = rawYaw === undefined ? undefined : opts.yawIsRad ? (rawYaw * 180) / Math.PI : rawYaw
  return {
    lat,
    lon,
    t: frame.publishedTs,
    sourceCRS: frameCRS,
    crsSource,
    ...(yaw !== undefined ? { yaw } : {}),
  }
}

export function useGeoPosition(opts: GeoPositionOptions): GeoPositionSource {
  const feed = useFrameFeed(() => toValue(opts.channelId))
  const latest = shallowRef<GeoPos | undefined>(undefined)
  const lastAt = ref(0)
  const now = ref(Date.now())

  const sourceCRS = computed<CRS>(() => toValue(opts.sourceCRS) ?? 'WGS-84')

  function readOptions() {
    return {
      sourceCRS: sourceCRS.value,
      latPath: String(toValue(opts.latPath) ?? '').trim(),
      lonPath: String(toValue(opts.lonPath) ?? '').trim(),
      yawPath: String(toValue(opts.yawPath) ?? '').trim(),
      yawIsRad: toValue(opts.yawIsRad) !== false,
    }
  }

  function refresh(): void {
    const pos = extractGeoPos(feed.latest.value, readOptions())
    if (pos && isValidLngLat(pos.lon, pos.lat)) {
      latest.value = pos
      lastAt.value = Date.now()
    }
  }

  const stop = watch(
    () => feed.version.value,
    () => {
      refresh()
    },
    { flush: 'sync' },
  )

  // 心跳：驱动 stale 判定（与 useCardStatus 的 500ms tick 同思路）
  let ticker = 0
  function startTicker(): void {
    if (ticker !== 0) return
    ticker = window.setInterval(() => {
      now.value = Date.now()
    }, 500)
  }
  startTicker()

  const stale = computed<boolean>(() => {
    void now.value
    if (lastAt.value === 0) return true
    const timeout = Number(toValue(opts.staleTimeoutMs) ?? MAP_STALE_TIMEOUT_MS)
    return Date.now() - lastAt.value > timeout
  })

  function trail(n: number): GeoPos[] {
    const buffer = feed.getBuffer()
    if (!buffer) {
      const only = latest.value
      return only ? [only] : []
    }
    const take = Math.max(0, n)
    const frames = take > 0 ? buffer.tail(Math.min(take, buffer.limit)) : buffer.tail(1)
    const o = readOptions()
    const out: GeoPos[] = []
    for (const frame of frames) {
      const pos = extractGeoPos(frame, o)
      if (pos && isValidLngLat(pos.lon, pos.lat)) out.push(pos)
    }
    return out
  }

  onBeforeUnmount(() => {
    stop()
    if (ticker !== 0) {
      window.clearInterval(ticker)
      ticker = 0
    }
  })

  return {
    latest,
    version: feed.version,
    stale,
    sourceCRS,
    getBuffer: feed.getBuffer,
    trail,
  }
}

/**
 * 便捷：把 Pos 转成 GCJ-02 的 [lon, lat]（转换仍在 geo.ts 完成）。
 *
 * ⚠️ 必须转换后再投影：高德底图是 GCJ-02，源数据是 WGS-84（GPS）时直接投影会偏 300~500 米。
 * 之前这里只返回原始 [lon,lat] 却没转换 —— 注释说"转成 GCJ-02"、实现却原样返回，是典型的
 * "文档与实现漂移"，已在联调中暴露为"节点落到马路对面"。
 */
export function geoPosToLngLat(pos: GeoPos): LngLat {
  return toGCJ02(pos.lon, pos.lat, pos.sourceCRS)
}
