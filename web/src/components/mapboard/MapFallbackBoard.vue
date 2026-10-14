<script setup lang="ts">
/**
 * 无底图降级板（v3 M5，C5）。
 *
 * 未配置高德 Key 时**不创建任何 AMap 实例**，用自研 Web Mercator 虚拟投影器，
 * 节点相对位置、卡片跟随、拖拽、出视野隐藏全部照常可用。
 *
 * ⛔ 合规红线（C-02 / C-10）：
 *   - 绝不切换任何境外底图（本文件不包含任何瓦片 URL）
 *   - 未配 Key 时不得尝试加载高德 JS API（零高德域名请求）
 *   - 坐标系声明常驻（C-04）
 */
import { computed, ref } from 'vue'
import { useMapRuntime } from '@/composables/useMapProjection'
import { useMapBoardStore } from '@/stores/mapBoard'
import { VirtualProjector } from '@/utils/projector'
import { describeCrs } from '@/utils/geo'
import { useUiStore } from '@/stores/ui'

const store = useMapBoardStore()
const ui = useUiStore()
const runtime = useMapRuntime()

const showTable = ref(true)
const crsText = computed<string>(() => describeCrs(store.view.sourceCRS))

/**
 * 兜底坐标格式化。
 *
 * 为什么必须包一层：`MapNodeView.fallbackLat/Lon` 的类型是 `number | null`
 * （纯静态节点允许只填名字、不填坐标），模板里裸写 `.toFixed(6)` 在坐标为 null 时
 * 会**抛异常**并打断整个 `v-for` 的渲染 —— 表现是"面板整块白掉"，
 * 而不是"显示了一个难看的 0.000000"。缺失时显示 `—`。
 */
function fmtCoord(v: number | null | undefined, digits = 6): string {
  return v == null || !Number.isFinite(v) ? '—' : v.toFixed(digits)
}

/** 虚拟投影器专属手势：拖拽平移 + 滚轮缩放（无惯性、无旋转 —— U-1）。 */
function onPointerDown(e: PointerEvent): void {
  const p = runtime.projector.value
  if (!(p instanceof VirtualProjector)) return
  const el = e.currentTarget as HTMLElement
  el.setPointerCapture?.(e.pointerId)
  let lastX = e.clientX
  let lastY = e.clientY
  const onMove = (ev: PointerEvent): void => {
    p.panBy(ev.clientX - lastX, ev.clientY - lastY)
    lastX = ev.clientX
    lastY = ev.clientY
  }
  const onUp = (): void => {
    window.removeEventListener('pointermove', onMove)
    window.removeEventListener('pointerup', onUp)
  }
  window.addEventListener('pointermove', onMove)
  window.addEventListener('pointerup', onUp)
}

function onWheel(e: WheelEvent): void {
  const p = runtime.projector.value
  if (!(p instanceof VirtualProjector)) return
  e.preventDefault()
  const rect = (e.currentTarget as HTMLElement).getBoundingClientRect()
  p.zoomAt({ x: e.clientX - rect.left, y: e.clientY - rect.top }, e.deltaY < 0 ? 1.2 : 1 / 1.2)
}
</script>

<template>
  <div class="mb-fallback" @pointerdown="onPointerDown" @wheel="onWheel">
    <div class="mb-fallback__banner">
      <strong>无底图模式</strong>
      <span class="ma-text-3">未配置高德 Key，地图按相对位置示意 —— 节点、卡片、拖拽与出视野隐藏均可用</span>
      <a class="ma-text-xs" :href="ui.amapKey ? 'https://lbs.amap.com/' : 'javascript:void(0)'" target="_blank" rel="noreferrer">
        配置 server/config.yaml 的 web.amapKey 后重启即可显示底图
      </a>
    </div>

    <!-- 节点坐标数值表：没有底图时，这是用户判断"谁在哪"的唯一依据 -->
    <div v-if="showTable" class="mb-fallback__table">
      <div class="mb-fallback__table-head">
        <span>节点</span>
        <span>经度</span>
        <span>纬度</span>
        <span>状态</span>
      </div>
      <button
        v-for="n in store.nodes"
        :key="n.id"
        class="mb-fallback__row"
        type="button"
        @pointerdown.stop
        @click.stop="store.selectNode(n.id)"
      >
        <span>{{ n.name }}</span>
        <span class="ma-text-3">{{ fmtCoord(n.fallbackLon) }}</span>
        <span class="ma-text-3">{{ fmtCoord(n.fallbackLat) }}</span>
        <span class="ma-text-3">{{ n.channelMissing ? '通道丢失' : store.isNodeUnplaced(n.id) ? '未定位' : store.nodeSummary[n.id]?.stale ? '无数据' : '实时' }}</span>
      </button>
      <p v-if="store.nodes.length === 0" class="mb-fallback__empty">还没有节点</p>
    </div>

    <button class="mb-fallback__toggle" type="button" @pointerdown.stop @click.stop="showTable = !showTable">
      {{ showTable ? '收起坐标表' : '展开坐标表' }}
    </button>

    <div class="mb-fallback__crs">{{ crsText }}</div>
  </div>
</template>

<style scoped>
.mb-fallback {
  position: absolute;
  inset: 0;
  z-index: 20;
  pointer-events: auto;
  background: var(--ma-bg-base);
  touch-action: none;
}

.mb-fallback__banner {
  position: absolute;
  top: 8px;
  left: 8px;
  right: 8px;
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
  padding: 6px 10px;
  border: 1px solid var(--ma-border);
  border-radius: var(--ma-radius);
  background: color-mix(in srgb, var(--ma-bg-card) 88%, transparent);
  font-size: var(--ma-font-xs);
}

.mb-fallback__table {
  position: absolute;
  left: 8px;
  bottom: 34px;
  max-width: 420px;
  max-height: 45%;
  overflow: auto;
  border: 1px solid var(--ma-border);
  border-radius: var(--ma-radius);
  background: color-mix(in srgb, var(--ma-bg-card) 92%, transparent);
  font-size: var(--ma-font-xs);
}

.mb-fallback__table-head,
.mb-fallback__row {
  display: grid;
  grid-template-columns: 1.4fr 1fr 1fr 0.8fr;
  gap: 6px;
  width: 100%;
  padding: 4px 8px;
  text-align: left;
}

.mb-fallback__table-head {
  border-bottom: 1px solid var(--ma-border);
  color: var(--ma-text-3);
}

.mb-fallback__row {
  border: none;
  background: transparent;
  color: var(--ma-text-1);
  cursor: pointer;
}

.mb-fallback__row:hover {
  background: var(--ma-bg-elevated);
}

.mb-fallback__empty {
  margin: 0;
  padding: 8px;
  color: var(--ma-text-3);
}

.mb-fallback__toggle {
  position: absolute;
  left: 8px;
  bottom: 8px;
  padding: 2px 8px;
  border: 1px solid var(--ma-border);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-elevated);
  color: var(--ma-text-2);
  cursor: pointer;
  font-size: var(--ma-font-xs);
}

.mb-fallback__crs {
  position: absolute;
  right: 8px;
  bottom: 8px;
  padding: 2px 8px;
  border: 1px solid var(--ma-border);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-elevated);
  color: var(--ma-text-3);
  font-size: var(--ma-font-xs);
}
</style>