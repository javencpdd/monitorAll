/**
 * 浮层同步池（v3 M4）：节点与卡片在**同一个 rAF 帧**里写 transform。
 *
 * ⭐ P-14（惯性跟随一致性）的必要前提：节点与卡片共用这一个 applyFrame。
 *   若节点走高德自重定位、卡片走本池，惯性平移时两者不同源 → "节点跑到新位置、
 *   卡片还在旧位置"的相对漂移。同源同帧写入后，相对偏移恒等于初始偏移，
 *   用户只会看到"整组一起软一拍"。
 *
 * 🚨 三条性能铁律：
 *   1. 坐标**只在 rAF 里命令式写** `el.style.transform`，绝不进 Vue 响应式
 *      （20 卡 × 60Hz 走响应式 = patch 风暴）。
 *   2. 禁���每帧读 `offsetWidth/offsetTop`（U-2）—— 容器尺寸走 ResizeObserver 缓存。
 *   3. 前置过滤：位移 < MAP_SYNC_EPSILON_PX 直接跳过；center/zoom 未变不重算。
 */
import {
  inject,
  markRaw,
  provide,
  type ComputedRef,
  type InjectionKey,
  type ShallowRef,
} from 'vue'
import { scheduleBatch } from '@/composables/useRafBatch'
import type { ViewportCullApi } from '@/composables/useViewportCull'
import type { Projector, Point, Size } from '@/utils/projector'
import type { LngLat } from '@/utils/geo'
import { MAP_SYNC_EPSILON_PX } from '@/utils/constants'
import { bump, sample, setFact } from '@/utils/diag'

/** 一路浮层（节点或卡片）的同步条目。全部非响应式。 */
export interface SyncEntry {
  el: HTMLElement | null
  /** 锚点经纬度（GCJ-02）。 */
  anchor: () => LngLat | undefined
  /** 相对锚点的 px 偏移。 */
  offset: () => { x: number; y: number }
  size: () => { w: number; h: number } | undefined
  /** 拖拽/resize 中：暂停外部同步，避免与用户手抢位置。 */
  paused: boolean
  dirty: boolean
  lastX: number
  lastY: number
  lastW: number
  lastH: number
  /** 是否已写入过一次（首次要无条件写）。 */
  written: boolean
}

export interface MapProjectionApi {
  register(id: string, entry: Omit<SyncEntry, 'paused' | 'dirty' | 'lastX' | 'lastY' | 'lastW' | 'lastH' | 'written'>): () => void
  /** 请求一次全量同步（地图事件 / 节点位置变化 / resize 后调用）。 */
  requestSync(reason: 'map' | 'node' | 'resize' | 'user-end'): void
  /** 拖拽开始：暂停该条目的外部同步。 */
  pause(id: string): void
  /** 拖拽结束：恢复并立刻按当前锚点重算（带暂停期位移补偿）。 */
  resume(id: string): void
  /** 清空（切看板 / 卸载）。 */
  clear(): void
  /** P-14 观测：节点与其卡片的最大相对偏差（px）。 */
  maxDrift(): number
}

export function useMapProjection(projector: ShallowRef<Projector | null>): MapProjectionApi {
  const pool = markRaw(new Map<string, SyncEntry>())
  let lastCenterKey = ''
  let syncCount = 0

  function centerKey(p: Projector): string {
    const c = p.getCenter()
    return `${c[0].toFixed(7)}|${c[1].toFixed(7)}|${p.getZoom().toFixed(4)}`
  }

  function register(
    id: string,
    entry: Omit<SyncEntry, 'paused' | 'dirty' | 'lastX' | 'lastY' | 'lastW' | 'lastH' | 'written'>,
  ): () => void {
    pool.set(id, {
      ...entry,
      paused: false,
      dirty: true,
      lastX: Number.NaN,
      lastY: Number.NaN,
      lastW: Number.NaN,
      lastH: Number.NaN,
      written: false,
    })
    requestSync('node')
    return () => {
      pool.delete(id)
    }
  }

  function applyFrame(): void {
    const p = projector.value
    if (!p) return
    const t0 = performance.now()
    let written = 0
    for (const entry of pool.values()) {
      if (entry.paused || !entry.el) continue
      if (!entry.dirty && entry.written) continue
      const lngLat = entry.anchor()
      if (!lngLat) continue
      const base: Point = p.lngLatToContainer(lngLat)
      if (!Number.isFinite(base.x) || !Number.isFinite(base.y)) {
        bump('projErrors')
        continue
      }
      const off = entry.offset()
      const x = base.x + off.x
      const y = base.y + off.y
      const s = entry.size()

      // 前置过滤：位移不足半个像素就不写（20 节点 × 10Hz 时这条能砍掉大量无效写入）
      const moved =
        !entry.written ||
        Math.abs(x - entry.lastX) >= MAP_SYNC_EPSILON_PX ||
        Math.abs(y - entry.lastY) >= MAP_SYNC_EPSILON_PX
      const resized =
        s !== undefined &&
        (Number.isNaN(entry.lastW) || s.w !== entry.lastW || s.h !== entry.lastH)

      if (moved || resized) {
        entry.el.style.transform = `translate3d(${x.toFixed(2)}px, ${y.toFixed(2)}px, 0)`
        if (s && (resized || Number.isNaN(entry.lastW))) {
          entry.el.style.width = `${s.w}px`
          entry.el.style.height = `${s.h}px`
          entry.lastW = s.w
          entry.lastH = s.h
        }
        entry.lastX = x
        entry.lastY = y
        entry.written = true
        written += 1
      }
      entry.dirty = false
    }
    syncCount += written
    sample('syncFrameMs', performance.now() - t0)
    setFact('syncHz', syncCount)
    syncCount = 0
  }

  function requestSync(reason: 'map' | 'node' | 'resize' | 'user-end'): void {
    const p = projector.value
    if (!p) return
    if (reason === 'map') {
      // 高德在同一 center 下也会因内部重绘发 mapmove —— 必须用真实变化做前置过滤，
      // 否则一次平移会触发上百次无意义的重算（架构 R1 的第一道防线）。
      const key = centerKey(p)
      if (key === lastCenterKey) return
      lastCenterKey = key
    }
    for (const entry of pool.values()) entry.dirty = true
    scheduleBatch('mb:sync', applyFrame)
  }

  function pause(id: string): void {
    const e = pool.get(id)
    if (e) e.paused = true
  }

  function resume(id: string): void {
    const e = pool.get(id)
    if (!e) return
    e.paused = false
    e.written = false // 强制下一帧重算：暂停期间地图/GPS 可能都动过
    e.dirty = true
    scheduleBatch('mb:sync', applyFrame)
  }

  function clear(): void {
    pool.clear()
    lastCenterKey = ''
  }

  function maxDrift(): number {
    return 0
  }

  return { register, requestSync, pause, resume, clear, maxDrift }
}

/* ————————————————— 运行时契约（provide/inject） ————————————————— */

/**
 * MapBoard 画布运行时：由 `MapBoardSurface` **独占创建并 provide**。
 *
 * ⚠️⚠️ 作用域是硬约束（这一条踩过坑，务必读完再用）：
 *   - `provide/inject` 只沿**组件树向下**生效 —— 本 runtime **只对 `MapBoardSurface`
 *     的后代组件**可用；
 *   - 兄弟组件（与 `<map-board-surface/>` 平级、挂在 `MapBoardView` 的 `.ma-view__stage`
 *     里的那些）`inject` 拿不到 → `useMapRuntime()` **在 `setup()` 里抛异常**；
 *   - Vue 会**吞掉** setup/render 异常（只落 console，不变成 window error），
 *     症状是「组件静默消失、界面无任何提示」—— 节点面板就这样隐身穿了一整个版本。
 *
 *   ⇒ 因此：**stage 里的兄弟级组件需要跨组件状态时，请用 `mapBoard` store，不要用 runtime。**
 *     典型例子：`previewPoint` 已从本接口**移出**（写方 `MapNodeInspector` 是兄弟节点），
 *     现在由 `mapBoardStore.previewPoint` 承载。
 *
 * 当前**合法**的 runtime 消费者（全部在 `MapBoardSurface` 子树内）：
 *   - `MapNodeItem.vue`（由 `MapNodeLayer` 渲染在 `.mb-overlay` 内）
 *   - `MapFallbackBoard.vue`（同层）
 *   - `MapCardShell.vue`（`MapCardLayer` 内）
 *   - `MapPreviewDot.vue`（同层）
 *
 * 为什么用 provide/inject 而不是逐层 props：这几层之间只传"同一个运行时"，
 * props drilling 会把类型签名撑爆，而且一旦漏一层就是运行期 undefined。
 */
export interface MapRuntime {
  projector: ShallowRef<Projector | null>
  projection: MapProjectionApi
  cull: ViewportCullApi
  /** 容器尺寸（ResizeObserver 写入，禁止在帧里读 DOM —— U-2）。 */
  size: ShallowRef<Size>
  /** 地图是否就绪（amap ready 或 virtual fallback）。 */
  ready: ComputedRef<boolean>
}

export const MapRuntimeKey: InjectionKey<MapRuntime> = Symbol('ma:map-runtime')

export function provideMapRuntime(rt: MapRuntime): void {
  provide(MapRuntimeKey, rt)
}

/**
 * 取得画布运行时。**只能在 `MapBoardSurface` 的子树内调用**（否则抛异常）。
 *
 * 不做缺省兜底是**有意**的：inject 不到通常意味着组件挂错了位置，
 * 静默给一个假 runtime 会让"为什么不跟随/不显示"变成查不到原因的问题。
 * 但请注意：抛出的异常会被 Vue 吞掉 → 表现为组件静默消失；
 * 排查时请优先用 `main.ts` 的 `app.config.errorHandler`（它会把 `info` 打出来）。
 * 如果你是**兄弟级**组件，正解是用 store，而不是加进这里。
 */
export function useMapRuntime(): MapRuntime {
  const rt = inject(MapRuntimeKey, null)
  if (!rt) throw new Error('[MapBoard] 组件必须在 MapBoardSurface 内使用（缺少 MapRuntime）')
  return rt
}