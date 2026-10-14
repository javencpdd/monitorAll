/**
 * 投影器：经纬度 ⇄ 容器像素（v3 MapBoard 几何地基）。
 *
 * ⛔⛔ 硬前提（U-1，架构 v1.3 追加的技术性理由）
 * ------------------------------------------------------------------
 * 本文件全部数学只适用于 **2D 俯视、无旋转、无倾斜、无 3D 楼块** 的视图
 * （与现有 `MapRenderer` 的 `viewMode:'2D'` 一致）。
 *
 * 一旦启用 pitch / rotate / 3D，投影就变成带透视的仿射变换，本文件的
 * Web Mercator 公式立刻失效。而失效的表现是**静默**的：不报错、不崩溃，
 * 只是「浮层整组相对底图偏移」。这正是本项目最难排查的失败模式
 * （见 note/README「静默失败是本项目最大的敌人」）。
 *
 * 👉 新增任何视角能力（旋转/倾斜/楼块）之前，必须先改本文件。
 *
 * 两个实现共用同一份 Mercator 数学：
 *   - `AmapProjector`  —— 有高德 Key：底图瓦片由 AMap 绘制，投影由本文件自算
 *   - `VirtualProjector` —— 无 Key 降级（C5）：自算投影 + 自绘平移/缩放手势，
 *                            **不引入任何第三方地图/瓦片依赖**
 *
 * 为什么自算而不是调 `map.lngLatToContainer()`：
 *   `lngLatToContainer` 在 `mapmove` 期间的返回时机不保证与事件同步，20 张
 *   卡片浮层会出现「拖影」。自算可保证节点与卡片**同源同帧**写入（P-14）。
 *   代价是可能与高德瓦片存在固定或 scale 型偏差 → 由 `calibrate()` 兜住。
 *
 * ⚠️ 本文件只做**投影**，绝不做坐标系转换；转换唯一入口是 `utils/geo.ts`（C-05）。
 */
import type { LngLat } from '@/utils/geo'

export interface Point {
  x: number
  y: number
}

export interface Size {
  w: number
  h: number
}

export type ProjectorEvent = 'move' | 'zoom' | 'resize'

export interface Projector {
  readonly kind: 'amap' | 'virtual'
  lngLatToContainer(p: LngLat): Point
  containerToLngLat(q: Point): LngLat
  getCenter(): LngLat
  setCenter(c: LngLat): void
  getZoom(): number
  setZoom(z: number): void
  getSize(): Size
  on(type: ProjectorEvent, cb: () => void): () => void
  destroy(): void
}

/* ——————————————— 共享的 Web Mercator 数学 ——————————————— */

/** 标准瓦片尺寸（slippy map / EPSG:3857）。 */
const TILE_SIZE = 256
/** 地球赤道周长（米），用于 getResolution 交叉校验。 */
const EARTH_CIRCUMFERENCE_M = 40075016.685578488
/** 纬度上限（Mercator 在两极发散）。 */
const MAX_LAT = 85.05112878

/** 某 zoom 下的世界像素宽度。 */
export function worldPxAtZoom(zoom: number): number {
  return TILE_SIZE * Math.pow(2, zoom)
}

/** 经纬度 → 世界像素（原点在左上角，范围 [0, worldPx)）。 */
export function toWorld(lngLat: LngLat, worldPx: number): Point {
  const [lng, lat] = lngLat
  const clampedLat = Math.max(-MAX_LAT, Math.min(MAX_LAT, lat))
  const sin = Math.sin((clampedLat * Math.PI) / 180)
  return {
    x: ((lng + 180) / 360) * worldPx,
    y: (0.5 - Math.log((1 + sin) / (1 - sin)) / (4 * Math.PI)) * worldPx,
  }
}

/** 世界像素 → 经纬度。 */
export function fromWorld(p: Point, worldPx: number): LngLat {
  const lng = (p.x / worldPx) * 360 - 180
  const n = Math.PI * (1 - (2 * p.y) / worldPx)
  const lat = (180 / Math.PI) * Math.atan(Math.sinh(n))
  return [lng, lat]
}

/**
 * 经纬度 → 容器像素（**共享主路径**）。
 *
 * 参考点语义：`center` 恒映射到容器中心 `(w/2, h/2)`。
 * 若底图给容器设了 padding，该假设会破 —— 由 `calibrate()` 的常量偏移补偿兜住。
 */
export function projectToContainer(lngLat: LngLat, center: LngLat, zoom: number, size: Size): Point {
  const worldPx = worldPxAtZoom(zoom)
  const target = toWorld(lngLat, worldPx)
  const origin = toWorld(center, worldPx)
  // 经度环绕：取与原点同周期的最近表示，避免跨 180° 时整屏跳变
  let dx = target.x - origin.x
  const half = worldPx / 2
  if (dx > half) dx -= worldPx
  else if (dx < -half) dx += worldPx
  return { x: dx + size.w / 2, y: target.y - origin.y + size.h / 2 }
}

/** 容器像素 → 经纬度。 */
export function unprojectFromContainer(q: Point, center: LngLat, zoom: number, size: Size): LngLat {
  const worldPx = worldPxAtZoom(zoom)
  const origin = toWorld(center, worldPx)
  return fromWorld({ x: origin.x + (q.x - size.w / 2), y: origin.y + (q.y - size.h / 2) }, worldPx)
}

/** 当前缩放下的比例尺：米 / 像素（用于无 Key 降级界面的比例尺条）。 */
export function metersPerPixel(lat: number, zoom: number): number {
  const worldPx = worldPxAtZoom(zoom)
  return (EARTH_CIRCUMFERENCE_M * Math.cos((lat * Math.PI) / 180)) / worldPx
}

/* ——————————————— 事件发射器（两个实现共用） ——————————————— */

class Emitter {
  private handlers: Record<ProjectorEvent, Set<() => void>> = {
    move: new Set(),
    zoom: new Set(),
    resize: new Set(),
  }

  on(type: ProjectorEvent, cb: () => void): () => void {
    this.handlers[type].add(cb)
    return () => {
      this.handlers[type].delete(cb)
    }
  }

  emit(type: ProjectorEvent): void {
    for (const cb of this.handlers[type]) {
      try {
        cb()
      } catch (err) {
        console.error('[projector] 事件处理器异常：', err)
      }
    }
  }

  clear(): void {
    this.handlers.move.clear()
    this.handlers.zoom.clear()
    this.handlers.resize.clear()
  }
}

/* ——————————————— ① 有 Key：AMap 底图 + 自算投影 ——————————————— */

/** AMap.Map 的最小可用子集（自算投影只需要这几个能力）。 */
export interface ProjectableMap {
  getCenter(): { lng: number; lat: number } | [number, number] | undefined
  getZoom(): number
  /** 可选：m/px，用于交叉校验（能力探测，不存在则跳过）。 */
  getResolution?: () => number
  on?(event: string, cb: () => void): unknown
  off?(event: string, cb: () => void): unknown
}

export interface AmapProjectorOptions {
  /** 容器尺寸由外部提供（避免投影器自己读 DOM）。 */
  sizeOf: () => Size
  /**
   * 校准探针：静止时取一次高德自己的容器坐标，用于发现常量偏移。
   * 只在 moveend/zoomend 调用，频率极低。
   */
  amapContainerOf?: (lngLat: LngLat) => Point | undefined
  /** 诊断回调（偏差计数与 scale 不一致告警）。 */
  onDrift?: (info: { dx: number; dy: number; scaleMismatch: boolean }) => void
}

export class AmapProjector implements Projector {
  readonly kind = 'amap' as const
  private emitter = new Emitter()
  private disposers: Array<() => void> = []
  /** 常量偏移补偿（由 calibrate 得出，随 zoom 变化则不清零但会告警）。 */
  private offset: Point = { x: 0, y: 0 }
  private lastZoom = Number.NaN
  private lastResolution: number | undefined

  constructor(
    private map: ProjectableMap,
    private options: AmapProjectorOptions,
  ) {
    const bind = (event: string, type: ProjectorEvent): void => {
      const fn = (): void => this.emitter.emit(type)
      this.map.on?.(event, fn)
      this.disposers.push(() => this.map.off?.(event, fn))
    }
    bind('mapmove', 'move')
    bind('moveend', 'move')
    bind('zoomchange', 'zoom')
    bind('zoomend', 'zoom')
    bind('resize', 'resize')
  }

  private center(): LngLat {
    const c = this.map.getCenter()
    if (!c) return [116.397428, 39.90923]
    if (Array.isArray(c)) {
      const [lng, lat] = c
      return [Number(lng) || 0, Number(lat) || 0]
    }
    return [Number(c.lng) || 0, Number(c.lat) || 0]
  }

  getCenter(): LngLat {
    return this.center()
  }

  setCenter(c: LngLat): void {
    // 自算投影不持有 center；转发给底图即可
    const anyMap = this.map as unknown as { setCenter?: (c: [number, number]) => void }
    anyMap.setCenter?.([c[0], c[1]])
  }

  getZoom(): number {
    return this.map.getZoom()
  }

  setZoom(z: number): void {
    const anyMap = this.map as unknown as { setZoom?: (z: number) => void }
    anyMap.setZoom?.(z)
  }

  getSize(): Size {
    return this.options.sizeOf()
  }

  lngLatToContainer(p: LngLat): Point {
    const q = projectToContainer(p, this.center(), this.getZoom(), this.getSize())
    return { x: q.x + this.offset.x, y: q.y + this.offset.y }
  }

  containerToLngLat(q: Point): LngLat {
    return unprojectFromContainer(
      { x: q.x - this.offset.x, y: q.y - this.offset.y },
      this.center(),
      this.getZoom(),
      this.getSize(),
    )
  }

  /**
   * 静止时校准（moveend / zoomend 调用）。
   *
   * 两级响应：
   *   ① 常量偏移（容器 padding、center 与容器中心不重合）→ 自动补偿
   *   ② 随 zoom 变化的 scale 型误差 → **只告警不自动补偿**（自动补偿会引入
   *      新的不确定行为；v3 交由 `?forceAmapProj=1` 取证后人工处理）
   */
  calibrate(): void {
    const probe = this.options.amapContainerOf
    if (!probe) return
    const center = this.center()
    const amap = probe(center)
    if (!amap) return
    const size = this.getSize()
    // 自算约定：center 映射到容器中心
    const drift = { x: amap.x - size.w / 2, y: amap.y - size.h / 2 }
    if (!Number.isFinite(drift.x) || !Number.isFinite(drift.y)) return

    // scale 型检测：连续两次不同 zoom 下 drift 明显不同 → 判定为 scale 误差
    const zoom = this.getZoom()
    let scaleMismatch = false
    if (Number.isFinite(this.lastZoom) && Math.abs(zoom - this.lastZoom) > 0.01) {
      const moved = Math.hypot(drift.x - this.offset.x, drift.y - this.offset.y)
      if (moved > 2) scaleMismatch = true
    }
    this.lastZoom = zoom

    if (!scaleMismatch) {
      this.offset = { x: drift.x, y: drift.y }
    }

    // 机会式 scale 交叉校验：不同 zoom 下 resolution 之比应等于 2^Δz
    const res = this.map.getResolution?.()
    if (typeof res === 'number' && Number.isFinite(res) && res > 0) {
      if (this.lastResolution !== undefined) {
        const expected = Math.pow(2, zoom - this.lastZoomForRes)
        const actual = this.lastResolution / res
        if (Number.isFinite(expected) && expected > 0 && Math.abs(actual - expected) / expected > 0.005) {
          scaleMismatch = true
        }
      }
      this.lastResolution = res
      this.lastZoomForRes = zoom
    }

    if (scaleMismatch || Math.hypot(drift.x, drift.y) > 2) {
      this.options.onDrift?.({ dx: drift.x, dy: drift.y, scaleMismatch })
    }
  }

  private lastZoomForRes = Number.NaN

  on(type: ProjectorEvent, cb: () => void): () => void {
    return this.emitter.on(type, cb)
  }

  destroy(): void {
    for (const d of this.disposers) d()
    this.disposers = []
    this.emitter.clear()
  }
}

/* ——————————————— ② 无 Key：虚拟投影器（C5，零第三方依赖） ——————————————— */

export class VirtualProjector implements Projector {
  readonly kind = 'virtual' as const
  private emitter = new Emitter()
  private centerLngLat: LngLat
  private zoom: number
  private size: Size

  constructor(center: LngLat, zoom: number, size: Size) {
    this.centerLngLat = [center[0], center[1]]
    this.zoom = zoom
    this.size = { w: size.w, h: size.h }
  }

  getCenter(): LngLat {
    return [this.centerLngLat[0], this.centerLngLat[1]]
  }

  setCenter(c: LngLat): void {
    this.centerLngLat = [c[0], c[1]]
    this.emitter.emit('move')
  }

  getZoom(): number {
    return this.zoom
  }

  setZoom(z: number): void {
    const next = Math.max(3, Math.min(20, z))
    if (next === this.zoom) return
    this.zoom = next
    this.emitter.emit('zoom')
  }

  getSize(): Size {
    return { w: this.size.w, h: this.size.h }
  }

  setSize(size: Size): void {
    if (size.w === this.size.w && size.h === this.size.h) return
    this.size = { w: size.w, h: size.h }
    this.emitter.emit('resize')
  }

  lngLatToContainer(p: LngLat): Point {
    return projectToContainer(p, this.centerLngLat, this.zoom, this.size)
  }

  containerToLngLat(q: Point): LngLat {
    return unprojectFromContainer(q, this.centerLngLat, this.zoom, this.size)
  }

  /** 拖拽平移（容器像素增量）。 */
  panBy(dx: number, dy: number): void {
    const next = this.containerToLngLat({ x: this.size.w / 2 - dx, y: this.size.h / 2 - dy })
    this.centerLngLat = [next[0], next[1]]
    this.emitter.emit('move')
  }

  /** 以某点为锚缩放（factor>0 放大），保持锚点在屏幕上的位置不变。 */
  zoomAt(anchor: Point, factor: number): void {
    const before = this.containerToLngLat(anchor)
    this.setZoom(this.zoom + Math.log2(factor))
    const worldPx = worldPxAtZoom(this.zoom)
    const anchorBefore = toWorld(before, worldPx)
    const anchorAfter = toWorld(this.containerToLngLat(anchor), worldPx)
    const origin = toWorld(this.centerLngLat, worldPx)
    // 把 center 反向平移「锚点漂移量」
    this.centerLngLat = fromWorld(
      { x: origin.x + (anchorBefore.x - anchorAfter.x), y: origin.y + (anchorBefore.y - anchorAfter.y) },
      worldPx,
    )
    this.emitter.emit('move')
  }

  on(type: ProjectorEvent, cb: () => void): () => void {
    return this.emitter.on(type, cb)
  }

  destroy(): void {
    this.emitter.clear()
  }
}
