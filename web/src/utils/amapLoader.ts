/**
 * 高德地图 JS API 2.0 单例加载器（合规红线 1：唯一地图 SDK 加载入口）。
 *
 * - Key 通过后端 `/api/v1/system/runtime` 下发（配置项 `web.amapKey`），也可用构建期 `VITE_AMAP_KEY` 兜底。
 * - Key 为空时返回 null，**绝不降级到任何境外底图**（非合规境外地图服务一律禁止接入）。
 * - 全局只加载一次，多张地图卡片共享同一个 AMap 命名空间。
 */
import { AMAP_API_VERSION, AMAP_PLUGINS } from './constants'

/** 地图实例的最小可用抽象（避免业务组件依赖具体的 AMap 类型定义）。 */
export interface AMapMap {
  setZoomAndCenter(zoom: number, center: [number, number]): void
  setCenter(center: [number, number]): void
  setZoom(zoom: number): void
  setMapStyle(style: string): void
  setFitView(overlays?: unknown[]): void
  add(overlay: unknown | unknown[]): void
  remove(overlay: unknown | unknown[]): void
  clearMap(): void
  destroy(): void
  getZoom(): number
}

export interface AMapPolyline {
  setPath(path: [number, number][]): void
  setOptions(options: Record<string, unknown>): void
  getPath(): [number, number][]
}

export interface AMapMarker {
  setPosition(position: [number, number]): void
  setAngle(angle: number): void
  setIcon(icon: unknown): void
  setLabel(label: unknown): void
  setzIndex?(z: number): void
}

export interface AMapInfoWindow {
  open(map: AMapMap, position: [number, number]): void
  close(): void
  setContent(content: string): void
}

export interface AMapTileLayer {
  show(): void
  hide(): void
}

/** AMap 命名空间的最小结构化描述。 */
export interface AMapNamespace {
  Map: new (container: HTMLElement | string, options: Record<string, unknown>) => AMapMap
  Polyline: new (options: Record<string, unknown>) => AMapPolyline
  Marker: new (options: Record<string, unknown>) => AMapMarker
  InfoWindow: new (options: Record<string, unknown>) => AMapInfoWindow
  Pixel: new (x: number, y: number) => unknown
  Size: new (w: number, h: number) => unknown
  Icon: new (options: Record<string, unknown>) => unknown
  /** 卫星等瓦片图层（JSAPI 2.0 内置；卫星图不是 mapStyle，是独立图层）。 */
  TileLayer?: {
    Satellite?: new (options?: Record<string, unknown>) => AMapTileLayer
    RoadNet?: new (options?: Record<string, unknown>) => AMapTileLayer
  }
}

/** 加载错误信息。 */
export interface AMapLoadFailure {
  message: string
}

let loadPromise: Promise<AMapNamespace | null> | null = null
let cachedKey: string | null = null

function readBuildTimeKey(): string {
  return import.meta.env.VITE_AMAP_KEY ?? ''
}

/**
 * 加载高德 JS API。
 * @param key 高德 Key；空串一律返回 null（并给出明确原因，交由 UI 展示配置指引）
 * @param securityCode 高德安全密钥 securityJsCode。2021-12-02 之后申请的 Key 必须配置，
 *        否则样式切换（setMapStyle 暗色等）等能力会**静默失败**回落默认样式。
 *        必须在 JSAPI 脚本加载之前挂到 window._AMapSecurityConfig（官方 Loader 要求）。
 * @param forceWebGL 强制启用 WebGL 绘制。JSAPI 默认以 failIfMajorPerformanceCaveat 取 WebGL 上下文，
 *        无 GPU / 软件渲染 / 手机 WebView 会被判为"性能不足"而不启用 WebGL，
 *        控制台报"浏览器版本过低"，且 mapStyle / setMapStyle 一律不生效（卫星等纯瓦片图层不受影响）。
 *        置 true 即高德官方给出的解法：脚本加载前置 window.forceWebGL / forceWebGLBaseRender。
 */
export function loadAMap(key: string, securityCode = '', forceWebGL = true): Promise<AMapNamespace | null> {
  const effectiveKey = key.length > 0 ? key : readBuildTimeKey()
  if (effectiveKey.length === 0) {
    return Promise.resolve(null)
  }
  const cacheId = `${effectiveKey}|${securityCode}`
  if (loadPromise && cachedKey === cacheId) return loadPromise
  cachedKey = cacheId
  if (securityCode.length > 0) {
    ;(window as unknown as { _AMapSecurityConfig?: { securityJsCode: string } })._AMapSecurityConfig = {
      securityJsCode: securityCode,
    }
  }
  if (forceWebGL) {
    const w = window as unknown as { forceWebGL?: boolean; forceWebGLBaseRender?: boolean }
    w.forceWebGL = true
    // 2.0 增强版开关：一并置上，覆盖基础底图渲染路径
    w.forceWebGLBaseRender = true
  }
  loadPromise = import('@amap/amap-jsapi-loader')
    .then((mod) => {
      const loader = (mod.default ?? mod) as {
        load: (opts: Record<string, unknown>) => Promise<unknown>
      }
      return loader.load({
        key: effectiveKey,
        version: AMAP_API_VERSION,
        plugins: AMAP_PLUGINS,
      })
    })
    .then((amap) => amap as AMapNamespace)
    .catch((err: unknown) => {
      // 加载失败（无外网 / Key 无效）：重置单例以便重试，错误交由调用方展示
      loadPromise = null
      cachedKey = null
      console.warn('[amap] 高德 JS API 加载失败：', err)
      return null
    })
  return loadPromise
}

/** 是否允许抽样调用 AMap.convertFrom 做交叉校验（默认关闭，仅开发环境）。 */
export function crossCheckEnabled(): boolean {
  return import.meta.env.VITE_AMAP_CROSSCHECK === '1'
}
