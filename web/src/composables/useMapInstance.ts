/**
 * 单例地图宿主（v3 M3）。
 *
 * 为什么必须收敛成单例（架构 §3.1）：
 *   现状每张地图卡各建一个 `AMap.Map`。N 张卡会同时占用 N 个 WebGL 上下文
 *   （浏览器上限通常 8~16，超出后最旧的被强制丢弃 → "某几张卡变黑且无报错"），
 *   Key 配额与瓦片流量 N 倍，`window.__maMap` 诊断出口被最后一张覆盖。
 *   MapBoard 语义上就是"一张底图 + 覆盖物"，单例是唯一正确解。
 *
 * ⛔ 三条硬规则（违反任一条都会产生静默故障）：
 *   R1 无 Key / SDK 加载失败 → **不创建任何 AMap 实例**，直接走 VirtualProjector 降级（C5/C-10）。
 *   R2 async 竞态：每次 ensure 取自增 token；await 返回后若 token 已变 → 立刻 destroy 新实例并返回
 *      （否则 unmount 先于 resolve 时实例泄漏 —— 这是现状 MapRenderer 的真实隐患）。
 *   R3 Promise 复用与 token 守卫是**正交**的两件事：token 管"过期废弃"，Promise 复用管"重复创建"。
 *      只做前者会在 Vue Router 复用组件实例时反复重建 WebGL 上下文，与做单例的初衷自相矛盾。
 *
 * ⚠️ 组件复用不触发 onBeforeUnmount（vue-router 对同路由记录的 params 变化复用实例），
 *    所以切看板 A→B 必须**显式 reset**（见 MapBoardView 的 watch(boardId)）。
 */
import { onBeforeUnmount, ref, shallowRef, type Ref, type ShallowRef } from 'vue'
import {
  AmapProjector,
  VirtualProjector,
  type Projector,
  type ProjectableMap,
  type Size,
} from '@/utils/projector'
import { loadAMap, type AMapNamespace, type AMapMap } from '@/utils/amapLoader'
import { useUiStore } from '@/stores/ui'
import { bump, setFact } from '@/utils/diag'

export type MapStatus = 'idle' | 'loading' | 'ready' | 'error' | 'fallback'

export interface MapInstanceApi {
  projector: ShallowRef<Projector | null>
  /** 底层 AMap 实例（虚拟模式下为 null）。仅用于样式切换/卫星图层等高德专属能力。 */
  amap: ShallowRef<AMapMap | null>
  /**
   * AMap 命名空间（虚拟模式下为 null）。
   *
   * 暴露它是为了构造 `TileLayer.Satellite` —— 卫星图**不是 mapStyle**，官方 mapStyle 列表里
   * 没有 satellite，传给 `setMapStyle` 会被**静默忽略并回落标准样式**（"选了卫星却没变化"）。
   * 必须用独立瓦片图层实现。
   */
  ns: ShallowRef<AMapNamespace | null>
  status: Ref<MapStatus>
  error: Ref<string>
  /** 幂等：同容器直接返回已有投影器；并发调用共享同一个 Promise。 */
  ensure(container: HTMLElement, size: Size, view: { center: [number, number]; zoom: number }): Promise<Projector | null>
  /** 切换看板时显式复位（复用实例，不销毁 WebGL 上下文）。 */
  reset(): void
  /** 卸载时彻底销毁。 */
  destroy(): void
  /** 容器尺寸变化时同步给虚拟投影器（高德侧自带 resize 处理）。 */
  setSize(size: Size): void
}

/* ————————————————— 模块级单例状态 ————————————————— */

const projector = shallowRef<Projector | null>(null)
const amap = shallowRef<AMapMap | null>(null)
// 模块级引用改名 amapNs：`ensure()` 内部有个同名局部变量 `ns`，同名会造成自我赋值（TS 会报错）
const amapNs = shallowRef<AMapNamespace | null>(null)
const status = ref<MapStatus>('idle')
const error = ref('')

let token = 0
let pending: Promise<Projector | null> | null = null
let pendingContainer: HTMLElement | null = null
let currentSize: Size = { w: 0, h: 0 }
let refCount = 0

function destroyAmap(): void {
  const map = amap.value
  amap.value = null
  amapNs.value = null
  if (map) {
    try {
      map.destroy()
    } catch {
      // destroy 抛错不能阻断业务流程：宁可留一个待回收的实例，也不要让页面崩掉。
      // 但也不能静默 —— bump 一个计数，让 HUD 能看到。
      bump('mapDestroyErrors')
    }
  }
}

function destroyProjector(): void {
  projector.value?.destroy()
  projector.value = null
}

/** 彻底销毁（卸载路径）。 */
function destroy(): void {
  token += 1 // 让所有在途 ensure 失效
  pending = null
  pendingContainer = null
  destroyProjector()
  destroyAmap()
  status.value = 'idle'
  error.value = ''
}

/** 复位但不销毁 WebGL 上下文（切看板路径，R3）。 */
function reset(): void {
  token += 1
  pending = null
  pendingContainer = null
  destroyProjector()
  destroyAmap()
  status.value = 'idle'
  error.value = ''
}

function makeVirtual(view: { center: [number, number]; zoom: number }): Projector {
  const p = new VirtualProjector(view.center, view.zoom, currentSize)
  projector.value = p
  status.value = 'fallback'
  setFact('mapMode', 'virtual')
  setFact('projErrors', 0)
  return p
}

async function ensure(
  container: HTMLElement,
  size: Size,
  view: { center: [number, number]; zoom: number },
): Promise<Projector | null> {
  currentSize = { ...size }

  // R3：Promise 复用 —— 同一容器并发调用共享一个 Promise，绝不重复创建。
  if (pending && pendingContainer === container) return pending

  const my = ++token
  status.value = 'loading'
  const task = (async (): Promise<Projector | null> => {
    const ui = useUiStore()
    const key = ui.amapKey

    // R1：无 Key 直接降级，**不碰 AMapLoader**（合规 C-10：零高德请求）。
    if (!ui.amapReady || key.length === 0) {
      if (my !== token) return null // 过期实例无权写状态
      return makeVirtual(view)
    }

    let ns
    try {
      ns = await loadAMap(key, ui.amapSecurityCode, ui.amapForceWebGL)
    } catch (err) {
      if (my !== token) return null
      error.value = err instanceof Error ? err.message : '高德 SDK 加载失败'
      status.value = 'fallback'
      return makeVirtual(view)
    }
    if (my !== token) return null // 过期：下面创建实例后再 destroy
    if (!ns) {
      error.value = '高德 Key 无效或 SDK 加载失败'
      status.value = 'fallback'
      setFact('mapError', error.value)
      return makeVirtual(view)
    }

    const map = new ns.Map(container, {
      center: view.center,
      zoom: view.zoom,
      viewMode: '2D', // U-1：禁旋转/倾斜是自算投影成立的前提
      resizeEnable: true,
    })
    if (my !== token) {
      // R2：卸载/切看板已经发生 —— 立刻销毁刚建出来的实例，否则 WebGL 上下文泄漏
      try {
        map.destroy()
      } catch {
        bump('mapDestroyErrors')
      }
      return null
    }
    amap.value = map
    amapNs.value = ns
    const p = new AmapProjector(map as unknown as ProjectableMap, {
      sizeOf: () => currentSize,
      amapContainerOf: (lngLat) => map.lngLatToContainer?.(lngLat),
      onDrift: (info) => {
        setFact('projDrift', Math.round(Math.hypot(info.dx, info.dy) * 100) / 100)
        if (info.scaleMismatch) bump('projScaleMismatch')
      },
    })
    projector.value = p
    status.value = 'ready'
    setFact('mapMode', 'amap')
    setFact('amapInstances', 1)
    setFact('mapError', '')
    bump('mapCreates')
    return p
  })()

  pending = task
  pendingContainer = container
  try {
    return await task
  } finally {
    if (pending === task) {
      pending = null
      pendingContainer = null
    }
  }
}

function setSize(size: Size): void {
  if (size.w === currentSize.w && size.h === currentSize.h) return
  currentSize = { ...size }
  const p = projector.value
  if (p instanceof VirtualProjector) p.setSize(currentSize)
}

export function useMapInstance(): MapInstanceApi {
  refCount += 1
  onBeforeUnmount(() => {
    refCount -= 1
    // 最后一个使用者离开才销毁 —— 多组件共用单例时提前销毁会把别人的地图弄黑。
    if (refCount <= 0) destroy()
  })

  return { projector, amap, ns: amapNs, status, error, ensure, reset, destroy, setSize }
}