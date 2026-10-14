<script setup lang="ts">
/**
 * 节点层（v3 M3）：渲染全部节点。
 *
 * 轨迹尾迹（SVG polyline）本版**未做** —— 它在PRD 里是 Could 优先级，
 * 而 N×100 点的重绘会与 P-07 帧预算冲突。等浮层闭环稳定后再补，避免一次上太多变量。
 */
import { useMapBoardStore } from '@/stores/mapBoard'
import MapNodeItem from '@/components/mapboard/MapNodeItem.vue'

const store = useMapBoardStore()

function onSelect(id: string): void {
  store.selectNode(id)
  // 切换该节点的卡片展开/收起（可同时开多个节点的卡，配额在 MapCardLayer 裁剪）
  store.toggleNodeCards(id)
}
</script>

<template>
  <div class="mb-node-layer">
    <map-node-item
      v-for="node in store.nodes"
      :key="node.id"
      :node="node"
      @select="onSelect"
    />
  </div>
</template>

<style scoped>
.mb-node-layer {
  position: absolute;
  inset: 0;
  /* 整层不吃事件，只有 .mb-node 本体命中（见 MapNodeItem 的 pointer-events: auto） */
  pointer-events: none;
}
</style>