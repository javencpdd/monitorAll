/**
 * 🚩 全项目唯一允许做坐标系转换的模块（合规红线，ARCH §13）。
 *
 * 三条硬约束：
 *   1. 转换一律在前端离线完成，使用 gcoord，**不调用** AMap.convertFrom（有配额且依赖外网）。
 *   2. 除本文件外，任何组件/工具不得自行实现 WGS-84 ↔ GCJ-02 换算（自检：`grep -rn gcoord web/src` 只命中本文件）。
 *   3. Frame payload 中的原始坐标永不被改写，转换只发生在渲染那一刻。
 */
import gcoord from 'gcoord'
import type { CRS } from '@/types'
import { MAP_TARGET_CRS } from './constants'

/** 经纬度坐标对 [lon, lat]。 */
export type LngLat = [number, number]

/** gcoord 坐标系参数类型（避免依赖其内部命名空间导出）。 */
type GcoordCRS = Parameters<typeof gcoord.transform>[1]

/** 高德需要的 CRS 常量映射。 */
const CRS_MAP: Record<Exclude<CRS, 'GCJ-02'>, GcoordCRS> = {
  'WGS-84': gcoord.WGS84,
  'BD-09': gcoord.BD09,
}

/**
 * 将单个坐标转换为 GCJ-02。
 * @param lon 经度
 * @param lat 纬度
 * @param from 输入坐标系（用户显式声明或 payload.crs 盖章）
 */
export function toGCJ02(lon: number, lat: number, from: CRS): LngLat {
  if (!Number.isFinite(lon) || !Number.isFinite(lat)) return [lon, lat]
  if (from === MAP_TARGET_CRS) return [lon, lat]
  const srcCrs = CRS_MAP[from as Exclude<CRS, 'GCJ-02'>] ?? gcoord.WGS84
  const [outLon, outLat] = gcoord.transform([lon, lat], srcCrs, gcoord.GCJ02) as LngLat
  return [outLon, outLat]
}

/**
 * 批量转换（地图轨迹用）。
 * 10Hz × 200 点的量级下，纯计算开销可忽略；这里只做入参过滤，不做有损抽稀（抽稀由渲染器负责）。
 *
 * ⚠️ **慎用：它假定整批点来自同一个坐标系**（只有一个 `from`）。
 *
 * 自「帧内 payload.crs 优先」落地后（判定集中在 `useGeoPosition.resolveFrameCrs`），
 * 同一条轨迹里的点**可能分别来自带 crs 的帧和不带 crs 的帧** —— 此时整批套一个值是错的，
 * 会让轨迹里的一部分点静默偏 300~700 米。
 *
 * 正确做法是**逐点**用该点自己那帧的生效坐标系：`points.map(p => toGCJ02(p.lon, p.lat, p.sourceCRS))`
 * （`GeoPos.sourceCRS` / 卡片侧 `Pos.sourceCRS` 都已带生效值与来源标记）。
 *
 * 目前 `src/` 内已无调用方，保留导出仅为兼容；**新代码请优先逐点转换**。
 */
export function toGCJ02Batch(points: readonly LngLat[], from: CRS): LngLat[] {
  if (from === MAP_TARGET_CRS) return points.map(([lon, lat]) => [lon, lat] as LngLat)
  return points.map(([lon, lat]) => toGCJ02(lon, lat, from))
}

/**
 * GCJ-02 → 目标坐标系（`toGCJ02` 的逆）。
 *
 * 用途：用户在**地图上取点**时，取到的是底图坐标（GCJ-02），
 * 但要把这个点填进飞控/GPS 设备（通常是 WGS-84）就必须转回去。
 * 不转的后果是**偏 300~500 米**，而且看起来"像是设备不准"，极难排查。
 *
 * ⚠️ 与 `toGCJ02` 一样，本函数是全项目唯一允许做换算的地方（合规 C-05）：
 *    自检 `grep -rn gcoord web/src` 应只命中本文件。
 */
export function fromGCJ02(lon: number, lat: number, to: CRS): LngLat {
  if (!Number.isFinite(lon) || !Number.isFinite(lat)) return [lon, lat]
  if (to === MAP_TARGET_CRS) return [lon, lat]
  const dstCrs = CRS_MAP[to as Exclude<CRS, 'GCJ-02'>] ?? gcoord.WGS84
  const [outLon, outLat] = gcoord.transform([lon, lat], gcoord.GCJ02, dstCrs) as LngLat
  return [outLon, outLat]
}

/** 卡片脚注/悬浮面板文案：明确告知当前生效坐标系与换算来源。 */
export function describeCrs(source: CRS): string {
  if (source === MAP_TARGET_CRS) return 'GCJ-02（源数据已是 GCJ-02）'
  return `GCJ-02（源 ${source}，已离线换算）`
}

/** 判断坐标是否合法经纬度（用于丢掉 GPS 未定位时的 0,0）。 */
export function isValidLngLat(lon: number, lat: number): boolean {
  return Number.isFinite(lon) && Number.isFinite(lat) && Math.abs(lon) > 1e-6 && Math.abs(lat) > 1e-6
}

/**
 * 经纬度**值域**校验：纬度 [-90,90]、经度 [-180,180]。
 *
 * 与 `isValidLngLat` 的区别：后者额外排除 (0,0)（那是 GPS 未定位的哨兵值，用于渲染剔除），
 * 但 (0,0) 在值域上**合法**（几内亚湾的真实坐标）。表单校验必须用本函数，
 * 否则用户填 0/0 会被判非法且看不到任何解释。
 */
export function isValidLatLonRange(lat: number, lon: number): boolean {
  return (
    Number.isFinite(lat) &&
    Number.isFinite(lon) &&
    lat >= -90 &&
    lat <= 90 &&
    lon >= -180 &&
    lon <= 180
  )
}
