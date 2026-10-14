/**
 * 节点位置（v3 M3）：geo 通道实时驱动 + 静态兜底 + stale 判定。
 *
 * 语义（A10，裁决）：
 *   - 从未收到合法坐标 → 用兜底坐标显示，节点置灰标 stale
 *   - 收到过坐标后再断流 → **保留最后已知位置，不跳回兜底**
 *     （跳回会让节点在丢数瞬间"瞬移"，容易误导判断 —— 这是最容易踩的坑）
 *
 * ⚠️ 坐标转换唯一入口是 `utils/geo.ts`（C-05），本文件只做"取数 + 状态机 + 写入 store"。
 *
 * 订阅语义（C15，与出视口隐藏相反）：
 *   节点通道**始终订阅**，不因出视口退订 —— 否则回到视野时位置是陈的。
 */
import { computed, onBeforeUnmount, toValue, watch, type ComputedRef, type MaybeRefOrGetter } from 'vue'
import { geoPosToLngLat, useGeoPosition } from '@/composables/useGeoPosition'
import { getWsClient } from '@/ws/client'
import { useMapBoardStore } from '@/stores/mapBoard'
import { MAP_STALE_TIMEOUT_MS, MAP_TRAIL_DEFAULT } from '@/utils/constants'
import type { MapNodeView } from '@/types/mapboard'
import type { LngLat } from '@/utils/geo'

export interface NodePositionApi {
  /** 当前应显示的经纬度（GCJ-02）；命令式读，不进响应式。 */
  lngLatOf(): LngLat | undefined
  /** 位置版本号：变了就该重算一次投影（P-14 的触发源）。 */
  version: ComputedRef<number>
  stale: ComputedRef<boolean>
}

export function useNodePosition(node: MaybeRefOrGetter<MapNodeView>): NodePositionApi {
  const store = useMapBoardStore()

  const source = useGeoPosition({
    channelId: () => {
      const n = toValue(node)
      // 通道已删除的脏引用：立刻置 undefined，不要去订阅一个不存在的 id
      if (!n || n.channelMissing) return undefined
      return n.channelId || undefined
    },
    // ⚠️ 这是**声明值 / 兜底值**，不再是"唯一生效来源"：
    // `extractGeoPos` 先用帧内 `payload.crs`（后端盖章），仅当帧内缺失/非法才回落到这里
    // （判定集中在 `resolveFrameCrs`）。界面上那句"兜底坐标标准（帧内已带坐标系时以帧为准）"
    // 说的就是这件事，两处文案必须一致。
    sourceCRS: () => toValue(node)?.fallbackCrs ?? 'WGS-84',
    latPath: () => toValue(node)?.latPath ?? '',
    lonPath: () => toValue(node)?.lonPath ?? '',
    // ⛔ 不再传 yawPath / yawIsRad：节点是"圆点 + 名字"，**设计上不画朝向**。
    // MapNode 的 yawPath/yawUnit 已随之删除（types/mapboard.ts 有说明）。
    // useGeoPosition 本身仍保留 yaw 能力（两个选项都是可选的），只是这里不再使用；
    // 卡片侧的朝向是 MapRenderer 自己读 config.yawPath 实现的，与本文件无关。
    trailLength: () => MAP_TRAIL_DEFAULT,
    staleTimeoutMs: () => MAP_STALE_TIMEOUT_MS,
  })

  const stale = computed<boolean>(() => {
    const n = toValue(node)
    if (!n) return true
    if (!n.channelId || n.channelMissing) return true
    return source.stale.value
  })

  const version = computed<number>(() => source.version.value)

  /** 把当前位置写入 store（唯一的写入点）。 */
  function push(): void {
    const n = toValue(node)
    if (!n) return
    const pos = source.latest.value
    if (pos) {
      const [lng, lat] = geoPosToLngLat(pos)
      store.setNodePosition(n.id, lng, lat, pos.yaw)
      // 如实记录"这一帧按什么坐标系解读的、值从哪来" → 面板据此显示 `WGS-84·帧` / `WGS-84·声明`
      store.setNodeCrsSource(n.id, pos.sourceCRS, pos.crsSource)
      store.setNodeStaleSummary(n.id, source.stale.value, pos.t)
      const trail = source.trail(MAP_TRAIL_DEFAULT)
      if (trail.length > 1) {
        store.setNodeTrail(
          n.id,
          trail.map((p) => {
            const [lng2, lat2] = geoPosToLngLat(p)
            return { lng: lng2, lat: lat2 }
          }),
        )
      }
    } else {
      store.markNodeUninitialized(n.id)
      store.setNodeStaleSummary(n.id, true, 0)
    }
  }

  // 首次装载 + 节点配置变化时推一次（兜底坐标在这里落地）
  watch(
    () => [toValue(node)?.id, toValue(node)?.channelId, toValue(node)?.fallbackLat, toValue(node)?.fallbackLon],
    () => push(),
    { immediate: true },
  )

  // 帧到达时推（version 是 useGeoPosition 提供的唯一响应式触发源）
  watch(version, () => push())

  // stale 是**定时判定**的（没有新帧就不会触发 watch），所以要自己开一个 ticker
  let timer = 0
  function startStaleTicker(): void {
    window.clearInterval(timer)
    timer = window.setInterval(() => {
      const n = toValue(node)
      if (!n) return
      const s = source.stale.value
      const prev = store.nodeSummary[n.id]
      if (!prev || prev.stale !== s) {
        store.setNodeStaleSummary(n.id, s, prev?.lastUpdateAt ?? 0)
      }
    }, 1000)
  }
  startStaleTicker()

  // 节点通道订阅：始终订阅，出视口不退订（C15）。
  //
  // ⚠️ 换绑通道时必须**先退订旧值再订阅新值**：只订不退会让旧通道的引用计数永久 +1，
  // 表现为"已经改绑了，旧通道还在推流"（白耗带宽，且排障时看不到任何报错）。
  //
  // ⚠️ 卸载时也**不能拿节点当前的 channelId 去退订**：节点在本组件生命周期内可能已换绑，
  // 甚至 channelMissing 变 true（watch 已退订过），再按当前值退一次就是**重复退订** ——
  // 若另一个组件（如另一个节点）仍持有该通道，引用计数会被一并减掉，对方静默断流。
  // 唯一可靠做法：记录"本组件实际订阅的那个 id"，订阅与退订都以它为准。
  const ws = getWsClient()
  let subscribedId = ''
  watch(
    () => {
      const n = toValue(node)
      return n && !n.channelMissing ? n.channelId : ''
    },
    (next) => {
      if (subscribedId && subscribedId !== next) ws.unsubscribe([subscribedId])
      subscribedId = next
      if (next) ws.subscribe([next])
    },
    { immediate: true },
  )

  onBeforeUnmount(() => {
    window.clearInterval(timer)
    if (subscribedId) ws.unsubscribe([subscribedId])
    subscribedId = ''
  })

  function lngLatOf(): LngLat | undefined {
    const n = toValue(node)
    if (!n) return undefined
    const rt = store.positionOf(n.id)
    if (rt) return [rt.lng, rt.lat]
    /*
     * 必须显式判空，只靠 Number.isFinite 是**不够**的：
     *   1. 类型层面：`Number.isFinite(number: unknown): boolean` 不是类型守卫，
     *      它不会把 `number | null` 收窄成 `number`，下面的元组会因 null 报类型错误；
     *   2. 语义层面：兜底坐标缺失是**正常状态**（纯静态节点可只填名字），
     *      显式写出来才能让"这里可能没有坐标"在代码里看得见，而不是被一个布尔判断糊过去。
     * 保留 Number.isFinite 是为了同时挡掉 NaN / Infinity（后端不会发，但契约外输入要防）。
     */
    const { fallbackLat, fallbackLon } = n
    if (
      fallbackLat != null &&
      fallbackLon != null &&
      Number.isFinite(fallbackLat) &&
      Number.isFinite(fallbackLon)
    ) {
      return [fallbackLon, fallbackLat]
    }
    return undefined
  }

  return { lngLatOf, version, stale }
}