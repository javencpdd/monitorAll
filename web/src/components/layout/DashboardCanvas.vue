<script setup lang="ts">
/**
 * 网格画布（T-21）：grid-layout-plus 12 列栅格 + 拖拽/resize + 从面板拖入建档。
 *
 * 约定（ARCH §1.8 / TASKS §1.8）：
 *   - grid item 的 `i` 直接等于 card.id，并用 :key="card.id" 保证 diff 稳定
 *   - 查看模式禁用拖拽/resize；编辑模式才显示虚线框与缩放手柄
 *   - 布局变更后 → dashboardStore.saveLayoutNow（轻量保存，901 冲突独立处理）
 */
import { computed, ref } from 'vue'
import type { Layout } from 'grid-layout-plus'
import { GridItem, GridLayout } from 'grid-layout-plus'
import { useDashboardStore } from '@/stores/dashboard'
import { useUiStore } from '@/stores/ui'
import { GRID_COLS, MARGIN, MIN_CARD_H, MIN_CARD_W, ROW_HEIGHT } from '@/utils/constants'
import EmptyState from '@/components/base/EmptyState.vue'
import CardHost from '@/components/cards/CardHost.vue'

const dashboardStore = useDashboardStore()
const ui = useUiStore()

/** grid-layout-plus 的受控布局（元素含 i,x,y,w,h,minW,minH）。 */
const layout = computed<Layout>(() =>
  dashboardStore.cards.map((card) => ({
    i: card.id,
    x: card.layout.x,
    y: card.layout.y,
    w: card.layout.w,
    h: card.layout.h,
    minW: card.layout.minW || MIN_CARD_W,
    minH: card.layout.minH || MIN_CARD_H,
  })),
)

const editMode = computed<boolean>(() => dashboardStore.editMode)
const dragOver = ref(false)

function onLayoutUpdated(next: Layout): void {
  if (!editMode.value) return
  const patches = next.map((item) => ({
    id: String(item.i),
    x: item.x,
    y: item.y,
    w: item.w,
    h: item.h,
  }))
  void dashboardStore.saveLayoutNow(patches).catch(() => undefined)
}

/** 从数据源面板拖入：直接打开「新建卡片」抽屉并预选该通道（CD-01 三步向导）。 */
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
</script>

<template>
  <div
    class="ma-canvas"
    :class="{ 'ma-canvas--dragging': dragOver }"
    @dragover="onDragOver"
    @dragleave="onDragLeave"
    @drop="onDrop"
  >
    <grid-layout
      v-if="layout.length > 0"
      :layout="layout"
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
        :x="card.layout.x"
        :y="card.layout.y"
        :w="card.layout.w"
        :h="card.layout.h"
        :min-w="card.layout.minW || MIN_CARD_W"
        :min-h="card.layout.minH || MIN_CARD_H"
      >
        <card-host :card="card" />
      </grid-item>
    </grid-layout>

    <empty-state v-else icon="empty" title="看板还没有卡片">
      <template #action>
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
  box-sizing: border-box;
  touch-action: none;
}

:deep(.vgl-item--placeholder) {
  background: var(--ma-accent);
  opacity: 0.2;
  border-radius: var(--ma-radius);
}

:deep(.vgl-item__resizer) {
  width: 14px;
  height: 14px;
  right: 2px;
  bottom: 2px;
  padding: 0 2px 2px 0;
  cursor: se-resize;
  background: linear-gradient(135deg, transparent 50%, var(--ma-border-strong) 50%) no-repeat bottom right;
  opacity: 0;
  transition: opacity 0.12s ease;
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
