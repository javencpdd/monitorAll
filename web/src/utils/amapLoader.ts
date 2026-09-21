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

/** AMap 命名空间的最小结构化描述。 */
export interface AMapNamespace {
  Map: new (container: HTMLElement | string, options: Record<string, unknown>) => AMapMap
  Polyline: new (options: Record<string, unknown>) => AMapPolyline
  Marker: new (options: Record<string, unknown>) => AMapMarker
  InfoWindow: new (options: Record<string, unknown>) => AMapInfoWindow
  Pixel: new (x: number, y: number) => unknown
  Size: new (w: number, h: number) => unknown
  Icon: new (options: Record<string, unknown>) => unknown
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
 */
export function loadAMap(key: string): Promise<AMapNamespace | null> {
  const effectiveKey = key.length > 0 ? key : readBuildTimeKey()
  if (effectiveKey.length === 0) {
    return Promise.resolve(null)
  }
  if (loadPromise && cachedKey === effectiveKey) return loadPromise
  cachedKey = effectiveKey
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
