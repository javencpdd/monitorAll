<script setup lang="ts">
/**
 * 单个地图节点（v3 M3）：DOM 定位 + 点击展开其卡片。
 *
 * 为什么是 DOM 而不是 `AMap.Marker`（方案 U，架构 v1.2 裁决）：
 *   Marker 由高德自重定位（零滞后），而卡片浮层由我们的 rAF 池驱动（必有滞后）。
 *   两者不同源 → 惯性平移时"节点跑到新位置、卡片还在旧位置"的相对漂移。
 *   统一成 DOM + 同一个 Projector 后，相对偏移恒等于初始偏移（P-14 ≤2px 可达标）。
 *
 * ⚠️ 命名（note/03 §3.1）：组件标签 <map-node-item>，变量绝不能叫 mapNodeItem。
 * 生产构建下同名变量会**静默吞掉整棵子树且不报错**。
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useMapRuntime } from '@/composables/useMapProjection'
import { useNodePosition } from '@/composables/useNodePosition'
import { useWillChange } from '@/composables/useWillChange'
import { useMapBoardStore } from '@/stores/mapBoard'
import type { MapNodeView } from '@/types/mapboard'

const props = defineProps<{ node: MapNodeView }>()
const emit = defineEmits<{ (e: 'select', id: string): void }>()

const store = useMapBoardStore()
const runtime = useMapRuntime()

const el = ref<HTMLElement | null>(null)
const { lngLatOf, stale } = useNodePosition(() => props.node)

/** stale 直接取自 useNodePosition（它内部已有 3s 超时判定 + 1Hz ticker）。 */
const isStale = stale
const selected = computed<boolean>(() => store.selectedNodeId === props.node.id)
/**
 * 「未定位」：从未收到通道坐标、又没有可用兜底坐标 → 地图上**不画**这个节点。
 *
 * ⚠️ 用 `v-show` 而不是 `v-if`：组件必须**保持挂载**。
 * `useNodePosition` 在这里订阅通道（设备上线后坐标才会来），一旦卸载就退订 ——
 * 节点将永远停在"未定位"，再也等不到第一帧。所以只藏 DOM，不撤组件。
 */
const unplaced = computed<boolean>(() => store.isNodeUnplaced(props.node.id))

/**
 * 常驻合成层额度（`MAP_WILL_CHANGE_MAX` 全局共享 8 格，节点与卡片**同一个池子**）。
 *
 * 为什么节点必须参与：节点是每帧跟随 GPS 移动的元素，之前无条件挂着 `will-change: transform`；
 * 改成额度池后如果节点不接进来，`.mb-node.is-hot` 就是永不匹配的死代码，
 * 节点反而**彻底失去**常驻提升 —— 把一个省显存的优化做成了掉帧退化。
 *
 * 权重必须**同步**读取响应式源（`useWillChange` 用 `watchEffect` 收集依赖），
 * 否则状态变了不会重排。这里读的都是同步响应式：`selectedNodeId` / `openCardIds` / `cardsByNode`。
 */
const hot = useWillChange(`node:${props.node.id}`, () => {
  if (store.selectedNodeId === props.node.id) return 100
  const open = store.openCardIds
  if (open.length > 0) {
    const cards = store.cardsByNode.get(props.node.id)
    if (cards?.some((c) => open.includes(c.id))) return 10
  }
  return 1
})

function offset() {
  return { x: 0, y: 0 }
}
function size() {
  return { w: 0, h: 0 }
}

let unregister: (() => void) | null = null

onMounted(() => {
  unregister = runtime.projection.register(`node:${props.node.id}`, {
    el: el.value,
    anchor: lngLatOf,
    offset,
    size,
  })
})

// 节点 id 变化（理论上不会变，但保险）→ 重新注册
watch(
  () => props.node.id,
  () => {
    unregister?.()
    unregister = runtime.projection.register(`node:${props.node.id}`, {
      el: el.value,
      anchor: lngLatOf,
      offset,
      size,
    })
  },
)

onBeforeUnmount(() => {
  unregister?.()
  unregister = null
})

function onClick(): void {
  emit('select', props.node.id)
}
</script>

<template>
  <div
    ref="el"
    v-show="!unplaced"
    class="mb-node"
    :class="{
      'is-stale': isStale,
      'is-selected': selected,
      'is-missing': node.channelMissing,
      'is-hot': hot,
    }"
    :title="`${node.name}${isStale ? '（无数据，显示兜底位置）' : ''}`"
    @pointerdown.stop
    @click.stop="onClick"
  >
    <span class="mb-node__dot"></span>
    <span class="mb-node__label">{{ node.name }}</span>
    <span v-if="isStale" class="mb-node__badge" title="位置通道无数据，显示的是最后已知/兜底位置">!</span>
  </div>
</template>

<style scoped>
/*
 * 节点层 z-index 2 < 卡片层 10 < 选中 50 < 放大 150（架构 v1.3 修订表）。
 * 这层顺序由我们自己管（方案 U 下不再由高德托管），必须显性化。
 */
.mb-node {
  position: absolute;
  top: 0;
  left: 0;
  z-index: 2;
  display: flex;
  align-items: center;
  gap: 6px;
  /* 只让节点本体吃事件，空白处透传给地图 */
  pointer-events: auto;
  cursor: pointer;
  /* 节点的绝对坐标由 transform 写，这里只做以自身为中心的偏移 */
  margin: -10px 0 0 -10px;
  /* 常驻 will-change 由合成层额度（MAP_WILL_CHANGE_MAX）决定，见 .is-hot */
  touch-action: none;
}

/* 抢到合成层额度（选中 / 有卡展开）的节点才常驻 will-change */
.mb-node.is-hot {
  will-change: transform;
}

.mb-node__dot {
  width: 14px;
  height: 14px;
  border: 2px solid var(--ma-accent);
  border-radius: 50%;
  background: var(--ma-accent);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--ma-accent) 25%, transparent);
  flex: none;
}

.mb-node__label {
  padding: 1px 6px;
  border-radius: var(--ma-radius);
  background: color-mix(in srgb, var(--ma-bg-base) 78%, transparent);
  color: var(--ma-text-1);
  font-size: var(--ma-font-xs);
  white-space: nowrap;
}

/* stale：置灰但**保留最后已知位置**，不跳回兜底（A10） */
.mb-node.is-stale .mb-node__dot {
  border-color: var(--ma-text-3);
  background: transparent;
  box-shadow: none;
}

.mb-node.is-stale .mb-node__label {
  color: var(--ma-text-3);
}

.mb-node__badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 14px;
  height: 14px;
  border-radius: 50%;
  background: var(--ma-status-warn, var(--ma-text-3));
  color: #000;
  font-size: 10px;
  font-weight: 700;
}

/* 脏引用（通道已删）：明确区别于 stale —— 这个需要人工重新绑定 */
.mb-node.is-missing .mb-node__dot {
  border-color: var(--ma-status-error, #e5484d);
  border-style: dashed;
}

.mb-node.is-selected .mb-node__dot {
  box-shadow: 0 0 0 5px color-mix(in srgb, var(--ma-accent) 30%, transparent);
}
</style>