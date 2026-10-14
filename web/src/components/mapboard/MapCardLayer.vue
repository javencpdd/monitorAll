<script setup lang="ts">
/**
 * 卡片浮层容器（v3 M4）：多卡同开 + 配额裁剪。
 *
 * 配额（架构 A4，防止"20 张视频卡同时解码把 GPU 打满"）：
 *   - 视频卡 **硬上限 4**
 *   - 非视频卡 **软上限 6**
 * 超出配额的卡渲染为**占位卡**（不挂载渲染器、不占下行），而不是直接不渲染 ——
 * 用户点了节点却什么都没出现会以为功能坏了；占位卡把"静默失败"变成"看得见的拒绝"。
 *
 * ⚠️ 命名（note/03 §3.1）：标签 <map-card-shell>，变量不能叫 mapCardShell。
 */
import { computed } from 'vue'
import MapCardShell from '@/components/mapboard/MapCardShell.vue'
import { useMapBoardStore } from '@/stores/mapBoard'
import { useDashboardStore } from '@/stores/dashboard'
import { MAP_VISIBLE_NONVIDEO_SOFT_LIMIT, MAP_VISIBLE_VIDEO_HARD_LIMIT } from '@/utils/constants'
import type { MapNodeView } from '@/types/mapboard'
import type { Card } from '@/types'

const store = useMapBoardStore()
const dashboard = useDashboardStore()

interface Placed {
  card: Card
  node: MapNodeView
  /** 同节点内的序号，用于级联偏移（避免多张卡完全重叠）。 */
  index: number
  /** 被配额拦下 → 渲染占位卡。 */
  blocked: boolean
}

const placed = computed<Placed[]>(() => {
  const out: Placed[] = []
  let video = 0
  let other = 0
  for (const id of store.openCardIds) {
    const card = dashboard.cards.find((c) => c.id === id)
    if (!card?.nodeId) continue
    const node = store.nodeById(card.nodeId)
    if (!node) continue
    // 节点「未定位」→ 卡片没有锚点可依（projector 会跳过写入），渲染出来只会全部堆在
    // 容器左上角。这里直接不渲染，**数量由节点面板明确披露**（"已隐藏该节点的 N 张卡片"），
    // 不做静默消失。
    if (store.isNodeUnplaced(card.nodeId)) continue
    const siblings = store.cardsByNode.get(card.nodeId) ?? []
    const idx = siblings.findIndex((c) => c.id === card.id)
    const index = idx < 0 ? 0 : idx
    if (card.rendererType === 'video') {
      if (video >= MAP_VISIBLE_VIDEO_HARD_LIMIT) {
        out.push({ card, node, index, blocked: true })
        continue
      }
      video += 1
    } else {
      if (other >= MAP_VISIBLE_NONVIDEO_SOFT_LIMIT) {
        out.push({ card, node, index, blocked: true })
        continue
      }
      other += 1
    }
    out.push({ card, node, index, blocked: false })
  }
  return out
})
</script>

<template>
  <div class="mb-card-layer">
    <map-card-shell
      v-for="item in placed"
      :key="item.card.id"
      :card="item.card"
      :node="item.node"
      :index="item.index"
      :blocked="item.blocked"
      @close="store.closeCard(item.card.id)"
    />
  </div>
</template>

<style scoped>
.mb-card-layer {
  position: absolute;
  inset: 0;
  pointer-events: none;
}
</style>