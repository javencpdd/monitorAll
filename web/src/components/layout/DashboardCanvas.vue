<script setup lang="ts">
/**
 * 网格画布（T-21）：grid-layout-plus 12 列栅格 + 拖拽/resize + 从面板拖入建档。
 *
 * 约定（ARCH §1.8 / TASKS §1.8）：
 *   - grid item 的 `i` 直接等于 card.id，并用 :key="card.id" 保证 diff 稳定
 *   - 查看模式禁用拖拽/resize；编辑模式才显示虚线框与缩放手柄
 *   - 布局变更后 → dashboardStore.saveLayoutNow（轻量保存，901 冲突独立处理）
 */
import { computed, onMounted, onUpdated, ref, watch } from 'vue'
import type { Layout } from 'grid-layout-plus'
import { GridItem, GridLayout } from 'grid-layout-plus'
import { useDashboardStore } from '@/stores/dashboard'
import { useUiStore } from '@/stores/ui'
import { GRID_COLS, MARGIN, MIN_CARD_H, MIN_CARD_W, ROW_HEIGHT } from '@/utils/constants'
import EmptyState from '@/components/base/EmptyState.vue'
import CardHost from '@/components/cards/CardHost.vue'
import type { Card } from '@/types'

/** 单个网格项的几何（从库导出类型推导，避免依赖未导出的 LayoutItem）。 */
type LayoutItem = Layout[number]

const dashboardStore = useDashboardStore()
const ui = useUiStore()

/**
 * 网格几何的本地真源。
 *
 * 为什么不用 computed 从 store 派生：
 *  1) grid-item 用【自己的 x/y/w/h props】渲染（库内部 _e() 只在 resizing 期间用
 *     resizing.width/height 覆盖），松手后立刻回落到 props。若 props 来自 store，
 *     而 store 要等 layout-updated → 保存 → 回流才更新，视觉上就会「回弹」。
 *  2) 库在 `:layout` prop 变化时会再次 emit layout-updated（其内部 layoutUpdate
 *     末尾固定 emit），若每次回写都替换数组，就会形成「保存→新引用→再 emit」死循环。
 *
 * 因此：几何由本地 ref 持有（库原地修改即等于更新 props，松手不回弹），
 * store 仅作为持久化目标，且值相同就不替换数组（prop 引用不变 → 不再二次 emit）。
 */
function buildLayout(): Layout {
  return dashboardStore.cards.map((card) => ({
    i: card.id,
    x: card.layout.x,
    y: card.layout.y,
    w: card.layout.w,
    h: card.layout.h,
    minW: card.layout.minW || MIN_CARD_W,
    minH: card.layout.minH || MIN_CARD_H,
  }))
}

const layoutRef = ref<Layout>(buildLayout())

/** store → 本地：仅当几何真的不同才整体替换，避免拖拽/缩放过程中被覆盖。 */
watch(
  () => dashboardStore.cards,
  () => {
    const next = buildLayout()
    const cur = layoutRef.value
    const same =
      next.length === cur.length &&
      next.every((n, i) => {
        const c = cur[i]
        return !!c && c.i === n.i && c.x === n.x && c.y === n.y && c.w === n.w && c.h === n.h
      })
    if (!same) layoutRef.value = next
  },
  { deep: true },
)

/**
 * 卡片 → 几何的查询表：优先取本地真源，缺失时回落到 store 里的布局。
 * 用查询表而不是「按 layoutRef 遍历卡片」，可避免 id 对不上时把 undefined 传给
 * CardHost（CardHost 不在错误边界内，会整块渲染失败 → 一张卡都不显示）。
 */
const geoMap = computed<Map<string, LayoutItem>>(() => {
  const map = new Map<string, LayoutItem>()
  for (const card of dashboardStore.cards) {
    const g = layoutRef.value.find((it) => String(it.i) === card.id)
    map.set(
      card.id,
      g ?? {
        i: card.id,
        x: card.layout.x,
        y: card.layout.y,
        w: card.layout.w,
        h: card.layout.h,
        minW: card.layout.minW || MIN_CARD_W,
        minH: card.layout.minH || MIN_CARD_H,
      },
    )
  }
  return map
})

const FALLBACK_GEO: LayoutItem = { i: '', x: 0, y: 0, w: MIN_CARD_W, h: MIN_CARD_H, minW: MIN_CARD_W, minH: MIN_CARD_H }
function geoOf(card: Card): LayoutItem {
  return geoMap.value.get(card.id) ?? FALLBACK_GEO
}

const editMode = computed<boolean>(() => dashboardStore.editMode)
const dragOver = ref(false)

/** 判定布局是否真的变化（防止 store 回流 → 再 emit → 无限循环）。 */
function isLayoutChanged(next: Layout): boolean {
  const cards = dashboardStore.cards
  if (next.length !== cards.length) return true
  return next.some((item) => {
    const card = cards.find((c) => c.id === String(item.i))
    if (!card) return true
    const l = card.layout
    return l.x !== item.x || l.y !== item.y || l.w !== item.w || l.h !== item.h
  })
}

function onLayoutUpdated(next: Layout): void {
  if (!editMode.value) return
  if (!isLayoutChanged(next)) return // 坐标未变：不回写，掐断回环
  const patches = next.map((item) => ({
    id: String(item.i),
    x: item.x,
    y: item.y,
    w: item.w,
    h: item.h,
  }))
  void dashboardStore.saveLayoutNow(patches).catch(() => undefined)

  // 【关键：松手即定型】
  // 库是【原地修改】我们传进去的 layout 数组（b(): F.w = U），不产生新引用，
  // 因此父组件 watch(:layout) 不会触发 → 不会 emit updateWidth/compact →
  // 子项的 _e()（唯一从 props 重算几何的入口，由 compact→gt()→_e() 驱动）不会执行，
  // 子项内部几何 Q/le 仍是拖动前的旧值 → 松手回弹。
  // 用库回传的最终几何重建一个新数组（新引用），强制触发上述同步链路。
  layoutRef.value = next.map((i) => ({ ...i }))
}

/**
 * 从数据源面板拖入：直接打开「新建卡片」抽屉并预选该通道（CD-01 三步向导）。
 */
function onDrop(e: DragEvent): void {
  dragOver.value = false
  if (!editMode.value) return
  const channelId = e.dataTransfer?.getData('text/plain')
  if (!channelId) return
  ui.openCardDrawer(undefined, 'create', channelId)
}

function onDragOver(e: DragEvent): void {
  if (!editMode.value) return
  e.preventDefault()
  if (e.dataTransfer) e.dataTransfer.dropEffect = 'copy'
  dragOver.value = true
}

function onDragLeave(): void {
  dragOver.value = false
}

function openCreateWizard(): void {
  ui.openCardDrawer(undefined, 'create')
}

/** 【临时诊断】定位「卡片建了却不显示」：把 store 与 DOM 的真实状态亮在画布上，定位后删除。 */
const canvasEl = ref<HTMLElement | null>(null)
const domInfo = ref('（探测中）')

function probeDom(): void {
  const el = canvasEl.value
  if (!el) return
  const layout = el.querySelector('.vgl-layout') as HTMLElement | null
  const items = el.querySelectorAll('.vgl-item')
  const first = items[0] as HTMLElement | undefined
  domInfo.value =
    `容器=${layout ? `${layout.offsetWidth}x${layout.offsetHeight}` : '无'}` +
    ` 项数=${items.length}` +
    ` 首项=${first ? `${first.offsetWidth}x${first.offsetHeight}` : '无'}` +
    ` 首项样式=${first ? (first.getAttribute('style') || '').slice(0, 90) : '无'}`
}

onMounted(() => {
  // 挂载后布局尺寸可能要等一拍才稳定，多探几次
  for (const ms of [300, 1000, 2500]) window.setTimeout(probeDom, ms)
})
onUpdated(probeDom)

const dbg = computed(
  () =>
    `[诊断] 看板=${dashboardStore.current?.name ?? '未加载'} · 卡片=${dashboardStore.cards.length}张` +
    ` · 加载中=${dashboardStore.loading ? '是' : '否'} · 错误=${dashboardStore.error || '无'}` +
    ` || DOM: ${domInfo.value}`,
)
</script>

<template>
  <div
    ref="canvasEl"
    class="ma-canvas"
    :class="{ 'ma-canvas--dragging': dragOver }"
    @dragover="onDragOver"
    @dragleave="onDragLeave"
    @drop="onDrop"
  >
    <div class="ma-canvas__dbg">{{ dbg }}</div>
    <grid-layout
      v-if="dashboardStore.cards.length > 0"
      :layout="layoutRef"
      :col-num="GRID_COLS"
      :row-height="ROW_HEIGHT"
      :margin="MARGIN"
      :is-draggable="editMode"
      :is-resizable="editMode"
      :vertical-compact="true"
      :use-css-transforms="true"
      @layout-updated="onLayoutUpdated"
    >
      <grid-item
        v-for="card in dashboardStore.cards"
        :key="card.id"
        :i="card.id"
        :x="geoOf(card).x"
        :y="geoOf(card).y"
        :w="geoOf(card).w"
        :h="geoOf(card).h"
        :min-w="geoOf(card).minW"
        :min-h="geoOf(card).minH"
        :is-draggable="editMode"
        :is-resizable="editMode"
      >
        <card-host :card="card" />
      </grid-item>
    </grid-layout>

    <empty-state v-else icon="empty" title="看板还没有卡片">
      <template #action>
        <!-- 加载失败时把原因显示出来：否则「有卡片却不显示」会表现为静默空白 -->
        <span v-if="dashboardStore.error" class="ma-canvas__err">
          加载失败：{{ dashboardStore.error }}
        </span>
        <span class="ma-canvas__hint ma-text-xs ma-text-3">
          {{ editMode ? '从左侧数据源面板拖入通道，或点击顶栏「+ 新建卡片」' : '切换到编辑模式后添加卡片' }}
        </span>
        <br />
        <button v-if="editMode" class="ma-canvas__btn" @click="openCreateWizard">+ 新建卡片</button>
      </template>
    </empty-state>
  </div>
</template>

<style scoped>
.ma-canvas {
  position: relative;
  flex: 1 1 auto;
  min-width: 0;
  min-height: 0;
  overflow-y: auto;
  overflow-x: hidden;
  padding: 4px;
}

.ma-canvas--dragging {
  outline: 1px dashed var(--ma-accent);
  outline-offset: -4px;
}

.ma-canvas__hint {
  display: block;
}

.ma-canvas__dbg {
  position: absolute;
  top: 2px;
  right: 10px;
  z-index: 30;
  pointer-events: none;
  font-size: 11px;
  color: var(--ma-text-3, #8a8f98);
  opacity: 0.85;
}

.ma-canvas__err {
  display: block;
  margin-bottom: 8px;
  color: var(--ma-status-error, #e5534b);
  font-size: var(--ma-font-sm);
}

.ma-canvas__btn {
  margin-top: 8px;
  padding: 4px 12px;
  border: 1px solid var(--ma-border-strong);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-elevated);
  color: var(--ma-text-1);
  cursor: pointer;
  font-size: var(--ma-font-sm);
}

.ma-canvas__btn:hover {
  border-color: var(--ma-accent);
  color: var(--ma-accent);
}

/* grid-layout-plus 基础样式兜底（该包未内置 CSS 文件） */
:deep(.vgl-item) {
  position: relative;
  box-sizing: border-box;
  touch-action: none;
}

:deep(.vgl-item--placeholder) {
  background: var(--ma-accent);
  opacity: 0.2;
  border-radius: var(--ma-radius);
}

:deep(.vgl-item__resizer) {
  position: absolute;
  width: 14px;
  height: 14px;
  right: 2px;
  bottom: 2px;
  padding: 0 2px 2px 0;
  cursor: se-resize;
  background: linear-gradient(135deg, transparent 50%, var(--ma-border-strong) 50%) no-repeat bottom right;
  opacity: 0;
  transition: opacity 0.12s ease;
  z-index: 3;
}

:deep(.vgl-item:hover .vgl-item__resizer) {
  opacity: 1;
}

:deep(.vgl-item--resizing),
:deep(.vgl-item--dragging) {
  z-index: 12;
  opacity: 0.92;
}
</style>
