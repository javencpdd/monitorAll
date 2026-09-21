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
 */
export function toGCJ02Batch(points: readonly LngLat[], from: CRS): LngLat[] {
  if (from === MAP_TARGET_CRS) return points.map(([lon, lat]) => [lon, lat] as LngLat)
  return points.map(([lon, lat]) => toGCJ02(lon, lat, from))
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
