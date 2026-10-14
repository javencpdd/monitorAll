<script setup lang="ts">
/**
 * MapBoardSurface：底图容器 + 单例地图宿主 + overlay 兄弟层（v3 M3）。
 *
 * ⛔ 结构上唯一的硬规则：**底图与overlay 是兄弟节点，不是父子**。
 *   父子关系下卡片上的 pointerdown 会冒泡进 AMap 的 canvas，
 *   表现为"拖卡片时地图也跟着平移"（架构 R2）。兄弟 + pointer-events 分层是根治办法。
 *
 * 降级（C5 / C-10）：未配置高德 Key 时 useMapInstance 返回 VirtualProjector，
 * 本组件渲染"无底图模式"提示 + 坐标数值表，**绝不切换任何境外底图**。
 */
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import MapNodeLayer from '@/components/mapboard/MapNodeLayer.vue'
import MapCardLayer from '@/components/mapboard/MapCardLayer.vue'
import MapFallbackBoard from '@/components/mapboard/MapFallbackBoard.vue'
import MapCoordMenu from '@/components/mapboard/MapCoordMenu.vue'
import MapPreviewDot from '@/components/mapboard/MapPreviewDot.vue'
import { provideMapRuntime, useMapProjection, type MapRuntime } from '@/composables/useMapProjection'
import { useMapInstance } from '@/composables/useMapInstance'
import { useViewportCull } from '@/composables/useViewportCull'
import { useMapBoardStore } from '@/stores/mapBoard'
import { useDatasourceStore } from '@/stores/datasource'
import { describeCrs, fromGCJ02, type LngLat } from '@/utils/geo'
import type { CRS } from '@/types'

/** 坐标标准三选一（与节点表单保持一致）。 */
const CRS_VALUES: readonly CRS[] = ['WGS-84', 'GCJ-02', 'BD-09']
import type { Size } from '@/utils/projector'
import type { AMapTileLayer } from '@/utils/amapLoader'
import { setFact } from '@/utils/diag'

const store = useMapBoardStore()
const datasource = useDatasourceStore()

const containerEl = ref<HTMLElement | null>(null)
/**
 * 整个地图舞台。
 *
 * ⚠️ 尺寸与取点都必须以它为准，**不能**用 containerEl：后者是无底图降级时
 * `v-show=false`（display:none）的元素，`getBoundingClientRect()` 恒为 0×0 ——
 * 会让虚拟投影器尺寸为 0，节点与卡片全部算不出位置。
 */
const surfaceEl = ref<HTMLElement | null>(null)
const size = shallowRef<Size>({ w: 0, h: 0 })

const map = useMapInstance()
const projection = useMapProjection(map.projector)
const cull = useViewportCull()

/*
 * 表单预览点（`store.previewPoint`）已**移出** runtime，见下面 runtime 说明与
 * `stores/mapBoard.ts` 的字段注释：它的写方是兄弟组件 `MapNodeInspector`，
 * 而 runtime 只对本组件子树生效 —— 放这里会让节点面板 inject 失败并在 setup 抛异常。
 */

const runtime: MapRuntime = {
  projector: map.projector,
  projection,
  cull,
  size,
  ready: computed(() => map.projector.value !== null),
}
provideMapRuntime(runtime)

/** 无 Key 降级时为 true（虚拟投影模式）。 */
const isVirtual = computed<boolean>(() => map.projector.value?.kind === 'virtual')

/* ————————————————— 底图样式切换（标准 / 暗色 / 卫星） —————————————————
 *
 * ⚠️ 卫星**不是 mapStyle**：高德官方 mapStyle 列表里没有 satellite，把它传给
 * `setMapStyle` 会被**静默忽略并回落标准样式**（"选了卫星却没变化"，不报错）。
 * 卫星必须用独立的 `TileLayer.Satellite` 叠加图层实现 —— 与 MapRenderer 里
 * 既有实现保持同一套做法，避免两处各写一份。
 */
const SATELLITE_STYLE = 'amap://styles/satellite'

interface MapStyleOption {
  value: string
  label: string
  title: string
}

const MAP_STYLE_OPTIONS: readonly MapStyleOption[] = [
  { value: 'amap://styles/normal', label: '标准', title: '标准路网底图' },
  { value: 'amap://styles/dark', label: '暗色', title: '暗色底图（监控场景更不刺眼）' },
  { value: SATELLITE_STYLE, label: '卫星', title: '卫星影像（独立瓦片图层）' },
]

const satLayer = shallowRef<AMapTileLayer | null>(null)

/** 按目标样式增删卫星图层；非卫星样式不动图层。 */
function syncSatelliteLayer(style: string): void {
  const inst = map.amap.value
  const ns = map.ns.value
  if (!inst || !ns) return
  const want = style === SATELLITE_STYLE
  const Ctor = ns.TileLayer?.Satellite
  if (want && !satLayer.value && Ctor) {
    const layer = new Ctor()
    satLayer.value = layer
    inst.add(layer)
  } else if (!want && satLayer.value) {
    inst.remove(satLayer.value)
    satLayer.value = null
  }
  if (want && !Ctor) {
    // SDK 未提供 Satellite 构造器：明确告警而不是让用户面对"点了没反应"
    setFact('mapStyleError', 'Satellite 图层不可用')
  }
}

/** 把 store 里的样式落到地图实例上（幂等，可反复调用）。 */
function applyMapStyle(): void {
  const inst = map.amap.value
  if (!inst) return
  const style = store.view.mapStyle
  syncSatelliteLayer(style)
  if (style !== SATELLITE_STYLE) inst.setMapStyle(style)
  setFact('mapStyle', style)
}

/** 用户点选样式：先落本地（立即生效），再持久化到 globalConfig。 */
function pickMapStyle(value: string): void {
  if (store.view.mapStyle === value) return
  store.setView({ mapStyle: value })
  applyMapStyle()
  // 视角/样式属低频结构性变更，走 revision CAS 持久化。
  // 后端 `cards` 已改为可选，不传即不触碰卡片（此前不传会被当成"全量替换为空"→ 卡片被删光）。
  void store.saveView()
}

/**
 * 改看板级坐标标准。
 *
 * 「统一坐标标准」就落在这里：设一次，**新建节点默认沿用**（节点表单的坐标系初值取它），
 * 投影时统一换算成 GCJ-02。个别来源不同的节点仍可在表单里单独覆盖。
 * ⚠️ 它只影响"如何解读新数据"，**不会改写已有节点已存的值** ——
 *    已有节点按各自声明的 `fallbackCrs` 解读，改这里不会让它们动。
 */
function onPickSourceCrs(e: Event): void {
  const v = (e.target as HTMLSelectElement).value as CRS
  if (store.view.sourceCRS === v) return
  store.setView({ sourceCRS: v })
  void store.saveView()
}

/* ————————————————— 右键取点（复制经纬度） ————————————————— */

interface PickState {
  /** 相对舞台的像素位置，用于摆放面板。 */
  x: number
  y: number
  /** 地图坐标（GCJ-02），投影器的原生输出。 */
  gcj: LngLat
  /** 换算到看板声明的源坐标系后的值 —— 用户填设备用的就是这一个。 */
  src: LngLat
}

const pick = ref<PickState | null>(null)

function closePick(): void {
  pick.value = null
}

/**
 * 右键取点。
 *
 * ⚠️ 只在底图/空白处生效：卡片、样式切换、取点面板自身的右键**保留浏览器默认菜单**
 *    （用户在卡片上右键可能想用系统菜单，抢掉会很难受）。
 * ⚠️ 坐标一律经投影器 + `utils/geo.ts` 得出，本组件不做任何换算数学（合规 C-05）。
 */
function onContextMenu(e: MouseEvent): void {
  const t = e.target as HTMLElement | null
  if (
    t?.closest('.mb-card') ||
    t?.closest('.mb-style') ||
    t?.closest('.ma-pick') ||
    t?.closest('.mb-fallback__table') ||
    t?.closest('.mb-fallback__toggle')
  ) {
    return
  }
  const proj = map.projector.value
  const box = surfaceEl.value
  if (!proj || !box) return
  const rect = box.getBoundingClientRect()
  const q = { x: e.clientX - rect.left, y: e.clientY - rect.top }
  const gcj = proj.containerToLngLat(q)
  if (!Number.isFinite(gcj[0]) || !Number.isFinite(gcj[1])) return
  pick.value = {
    x: q.x,
    y: q.y,
    gcj,
    // 源坐标系取看板声明值（与节点表单里填的是同一套），这样复制出来的值可直接粘贴使用
    src: fromGCJ02(gcj[0], gcj[1], store.view.sourceCRS),
  }
}

/** 坐标系声明常驻（C-04 合规硬约束：有 Key / 无 Key 两种形态都要显示）。 */
const crsText = computed<string>(() => describeCrs(store.view.sourceCRS))

const geoChannels = computed(() =>
  datasource.allChannels.filter((ch) => ch.payloadType === 'geo_pose' || ch.payloadType === 'json' || ch.payloadType === 'table'),
)

async function initMap(): Promise<void> {
  const el = containerEl.value
  const box = surfaceEl.value
  if (!el || !box) return
  const rect = box.getBoundingClientRect()
  const next: Size = { w: Math.round(rect.width), h: Math.round(rect.height) }
  size.value = next
  store.projectorKind = 'amap'
  await map.ensure(el, next, { center: store.view.center, zoom: store.view.zoom })
  store.mapStatus = map.status.value
}

/* ——— 容器尺寸：ResizeObserver 写入缓存，帧内绝不读 DOM（U-2） ——— */
let ro: ResizeObserver | null = null
function observeSize(el: HTMLElement): void {
  ro = new ResizeObserver((entries) => {
    const box = entries[0]?.contentRect
    if (!box) return
    const next: Size = { w: Math.round(box.width), h: Math.round(box.height) }
    if (next.w === size.value.w && next.h === size.value.h) return
    size.value = next
    map.setSize(next)
    projection.requestSync('resize')
  })
  ro.observe(el)
}

/* ——— 地图事件 → 同步 ——— */
let disposers: Array<() => void> = []
function bindProjector(): void {
  disposers.forEach((d) => d())
  disposers = []
  const p = map.projector.value
  if (!p) return
  // 底图一动，之前取的点在屏幕上就不在原处了 → 必须关掉面板，
  // 否则用户会照着旧面板上的数字去填，而那是移动前的坐标（静默给错值）。
  disposers.push(
    p.on('move', () => {
      closePick()
      projection.requestSync('map')
    }),
  )
  disposers.push(
    p.on('zoom', () => {
      closePick()
      projection.requestSync('map')
    }),
  )
  disposers.push(
    p.on('resize', () => {
      closePick()
      projection.requestSync('resize')
    }),
  )
}

watch(map.projector, () => {
  bindProjector()
  store.mapStatus = map.status.value
  setFact('mapStatus', map.status.value)
  // 实例刚建好：把已保存的样式落上去（否则新建地图永远停在高德默认样式，
  // "我保存了暗色，下次打开又是亮的"）
  applyMapStyle()
  projection.requestSync('map')
})

// 样式来源可能不是本地点击（如从 globalConfig 装载、或用户在别处改过）→ 统一在这里落到实例
watch(() => store.view.mapStyle, () => applyMapStyle())

// 表单预览点变化 → 驱动一次同步（与节点位置共用同一条 rAF 通路）
// ⚠️ 用 getter 形式：`store.previewPoint` 在 Pinia setup store 上已被解包成值，
// 直接传会当成"监听一个常量"，永远不会触发。
watch(() => store.previewPoint, () => projection.requestSync('node'))

/* ——— 节点位置变化 → 同步（nodeVersion 由 store 在坐标真变化时递增） ——— */
watch(
  () => store.nodeVersion,
  () => projection.requestSync('node'),
)

/* ——— 切看板：显式复位（vue-router 复用实例，不会触发 unmount） ——— */
watch(
  () => store.boardId,
  async () => {
    closePick()
    projection.clear()
    cull.clear()
    map.reset()
    store.mapStatus = 'idle'
    await initMap()
  },
)

onMounted(async () => {
  if (surfaceEl.value) observeSize(surfaceEl.value)
  // 只有 boardId 已就绪才自己初始化。若还空着（首屏时 hydrate 还没回来），
  // 交给下面的 watch(boardId) 触发 —— 否则会建两次实例，白闪一次。
  if (store.boardId) {
    await initMap()
    bindProjector()
  }
})

onBeforeUnmount(() => {
  ro?.disconnect()
  ro = null
  disposers.forEach((d) => d())
  disposers = []
  // 先摘掉卫星图层再销毁实例：图层是从我们这里 add 进去的，由我们负责 remove
  const inst = map.amap.value
  if (inst && satLayer.value) {
    try {
      inst.remove(satLayer.value)
    } catch {
      /* 实例可能已在销毁流程中，忽略 */
    }
    satLayer.value = null
  }
  projection.clear()
  cull.clear()
})

defineExpose({ cullText: crsText })
</script>

<template>
  <div
    ref="surfaceEl"
    class="mb-surface"
    @contextmenu.prevent="onContextMenu"
    @click="closePick"
  >
    <!-- 底图容器与 overlay 是兄弟节点：卡片事件不会冒泡进高德 canvas -->
    <div v-show="!isVirtual" ref="containerEl" class="mb-map"></div>

    <div class="mb-overlay">
      <map-node-layer v-if="!isVirtual" />
      <map-preview-dot v-if="!isVirtual && store.previewPoint" />
      <map-card-layer v-if="!isVirtual" />
      <map-fallback-board v-else :error="map.error.value" />
    </div>

    <!-- 底图样式切换（仅高德模式；虚拟模式没有底图可切） -->
    <div v-if="!isVirtual" class="mb-style">
      <button
        v-for="opt in MAP_STYLE_OPTIONS"
        :key="opt.value"
        class="mb-style__btn"
        :class="{ 'is-active': store.view.mapStyle === opt.value }"
        type="button"
        :title="opt.title"
        @click="pickMapStyle(opt.value)"
      >
        {{ opt.label }}
      </button>
    </div>

    <!-- 右键取点面板（放在 overlay 之外，避免被 .mb-overlay 的 pointer-events:none 影响） -->
    <map-coord-menu
      v-if="pick"
      :x="pick.x"
      :y="pick.y"
      :bounds="size"
      :src="pick.src"
      :src-crs="store.view.sourceCRS"
      :gcj="pick.gcj"
      @close="closePick"
    />

    <!-- 常驻坐标系声明（C-04）+ 看板级坐标标准 -->
    <div class="mb-crs" :title="crsText">
      <span class="mb-crs__label">坐标标准</span>
      <select class="mb-crs__sel" :value="store.view.sourceCRS" @change="onPickSourceCrs">
        <option v-for="c in CRS_VALUES" :key="c" :value="c">{{ c }}</option>
      </select>
      <span class="mb-crs__arrow">→ GCJ-02</span>
    </div>

    <!-- 空看板提示 -->
    <div v-if="store.nodes.length === 0 && !store.loading" class="mb-empty">
      <p class="mb-empty__title">还没有节点</p>
      <p class="mb-empty__hint">
        在右侧「节点」面板点「添加节点」，或从左侧数据源拖一个
        {{ geoChannels.length > 0 ? '位置通道' : '（当前没有 geo 位置通道，可建静态节点）' }}过来
      </p>
    </div>
  </div>
</template>

<style scoped>
.mb-surface {
  /* 关键：必须是 in-flow 的 flex 子项（flex:1），而不是 absolute inset:0。
     absolute 会把右侧节点面板整个盖住 —— 面板"存在但不可见、点不到"，
     正是用户"找不到添加节点入口"的根因（静默失败的一种）。 */
  position: relative;
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  background: var(--ma-bg-base);
}

.mb-map {
  position: absolute;
  inset: 0;
  z-index: 0;
}

.mb-overlay {
  position: absolute;
  inset: 0;
  /* M2：overlay 整体不吃事件，只有点到卡片/把手才命中 → 地图手势不打折扣 */
  z-index: 10;
  pointer-events: none;
  overflow: hidden;
}

.mb-crs {
  position: absolute;
  right: 8px;
  bottom: 8px;
  z-index: 200;
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 2px 8px;
  border: 1px solid var(--ma-border);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-elevated);
  color: var(--ma-text-3);
  font-size: var(--ma-font-xs);
  /* 注意：这里**不能**再写 pointer-events:none —— 它现在是一个可操作的选择器，
     写 none 会让它看起来存在却永远点不动（静默失效）。 */
}

.mb-crs__label,
.mb-crs__arrow {
  pointer-events: none;
}

.mb-crs__sel {
  border: none;
  background: transparent;
  color: var(--ma-accent);
  cursor: pointer;
  font-size: var(--ma-font-xs);
  font-family: inherit;
  padding: 0;
}

.mb-crs__sel option {
  background: var(--ma-bg-card);
  color: var(--ma-text-1);
}

/* 底图样式切换：置于地图左上角（图层控制器的惯用位置）。
   z-index 必须高于 .mb-overlay(10)，否则会被卡片层盖住点不到。 */
.mb-style {
  position: absolute;
  top: 8px;
  left: 8px;
  z-index: 200;
  display: flex;
  gap: 2px;
  padding: 2px;
  border: 1px solid var(--ma-border);
  border-radius: var(--ma-radius);
  background: color-mix(in srgb, var(--ma-bg-card) 90%, transparent);
}

.mb-style__btn {
  padding: 2px 10px;
  border: none;
  border-radius: var(--ma-radius);
  background: transparent;
  color: var(--ma-text-2);
  cursor: pointer;
  font-size: var(--ma-font-xs);
}

.mb-style__btn:hover {
  background: var(--ma-bg-elevated);
  color: var(--ma-text-1);
}

.mb-style__btn.is-active {
  background: var(--ma-accent-dim);
  color: var(--ma-accent);
}

.mb-empty {
  position: absolute;
  top: 24%;
  left: 0;
  right: 0;
  z-index: 200;
  text-align: center;
  pointer-events: none;
}

.mb-empty__title {
  margin: 0 0 6px;
  color: var(--ma-text-2);
  font-size: var(--ma-font-lg);
}

.mb-empty__hint {
  margin: 0;
  color: var(--ma-text-3);
  font-size: var(--ma-font-sm);
}
</style>