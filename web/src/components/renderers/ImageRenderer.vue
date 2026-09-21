<script setup lang="ts">
/**
 * RD-05 图像渲染器（T-25）：保持宽高比、时间戳水印、maxFps 限流。
 * base64 → 直接用 data URI（每帧替换 src），URL 直接喂 src；
 * 超过 maxFps 的帧被丢弃，避免 30fps 的 ROS 图像把浏览器打满。
 */
import { computed, ref, watch } from 'vue'
import type { CSSProperties } from 'vue'
import type { ImagePayload, RendererProps } from '@/types'
import { useFrameFeed } from '@/composables/useFrameFeed'
import { formatClock } from '@/utils/format'
import EmptyState from '@/components/base/EmptyState.vue'

const props = defineProps<RendererProps>()

const { latest } = useFrameFeed(() => props.card.channelId)

const fit = computed<string>(() => String(props.config.fit ?? 'contain'))
const showTimestamp = computed<boolean>(() => props.config.showTimestamp !== false)
const maxFps = computed<number>(() => Number(props.config.maxFps ?? 10))

const displaySrc = ref<string>('')
const stamp = ref<number>(0)
const dims = ref<{ w: number; h: number } | undefined>(undefined)
let lastPaint = 0

/** 按 maxFps 节流更新 src（第三层高频防护）。 */
watch(latest, (frame) => {
  if (!frame) return
  const now = performance.now()
  const minInterval = 1000 / Math.max(1, maxFps.value)
  if (now - lastPaint < minInterval) return
  const payload = frame.payload as ImagePayload | undefined
  if (!payload) return
  lastPaint = now
  if (payload.encoding === 'url') {
    if (displaySrc.value !== payload.data) displaySrc.value = payload.data
  } else {
    // R3：base64 不带 data: 前缀，由前端拼接
    const next = `data:${payload.mime};base64,${payload.data}`
    if (displaySrc.value !== next) displaySrc.value = next
  }
  stamp.value = payload.stampMs ?? frame.publishedTs
  if (payload.width && payload.height) dims.value = { w: payload.width, h: payload.height }
})

const objectFit = computed<CSSProperties>(() => ({
  objectFit: (fit.value === 'stretch' ? 'fill' : fit.value) as CSSProperties['objectFit'],
}))
const stampText = computed<string>(() => (stamp.value > 0 ? formatClock(stamp.value) : ''))
const dimText = computed<string>(() => (dims.value ? `${dims.value.w}×${dims.value.h}` : ''))
</script>

<template>
  <div class="ma-image">
    <img v-if="displaySrc" class="ma-image__img" :src="displaySrc" :style="objectFit" alt="实时画面" />
    <empty-state v-else compact icon="empty" title="等待图像帧" description="尚未收到该通道的图像数据" />

    <div v-if="displaySrc && showTimestamp" class="ma-image__stamp ma-text-xs">
      {{ stampText }}
      <span v-if="dimText" class="ma-image__dim">· {{ dimText }}</span>
    </div>
  </div>
</template>

<style scoped>
.ma-image {
  position: relative;
  width: 100%;
  height: 100%;
  background: #000;
  overflow: hidden;
}

.ma-image__img {
  width: 100%;
  height: 100%;
  display: block;
}

.ma-image__stamp {
  position: absolute;
  right: 6px;
  bottom: 6px;
  padding: 1px 5px;
  border-radius: 3px;
  background: rgba(0, 0, 0, 0.55);
  color: rgba(255, 255, 255, 0.86);
  font-family: 'JetBrains Mono', monospace;
  pointer-events: none;
}

.ma-image__dim {
  opacity: 0.7;
}
</style>
