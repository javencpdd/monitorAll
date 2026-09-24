<script setup lang="ts">
/**
 * 顶部全局连接横幅（OP-01 / ST-04）：
 * 后端离线或 WS 重连中时展示，含重连倒计时与后端 LAN 访问信息。
 */
import { computed, ref } from 'vue'
import { useUiStore } from '@/stores/ui'
import { formatDuration } from '@/utils/format'

const ui = useUiStore()

/** 倒计时心跳。 */
const now = ref(Date.now())
window.setInterval(() => {
  now.value = Date.now()
}, 500)

const visible = computed<boolean>(() => {
  const phase = ui.connPhase
  return phase === 'reconnecting' || phase === 'closed' || phase === 'idle'
})

const remainingMs = computed<number>(() => {
  const at = ui.wsNextRetryAt
  if (!at) return 0
  return Math.max(0, at - now.value)
})

const detail = computed<string>(() => {
  if (ui.wsLastError) return ui.wsLastError
  return '后端不可达，正在尝试重连'
})
</script>

<template>
  <div v-if="visible" class="ma-banner">
    <span class="ma-banner__dot"></span>
    <span class="ma-banner__text">
      {{ ui.connPhase === 'closed' ? '与后端的连接已断开' : '正在重连后端…' }}
      <span v-if="remainingMs > 0">（{{ formatDuration(remainingMs) }}）</span>
    </span>
    <span class="ma-banner__detail ma-text-xs ma-text-2 ma-ellipsis">{{ detail }}</span>
  </div>
</template>

<style scoped>
/*
 * 悬浮样式：absolute 于 .ma-view__body 顶部。
 * 原来它参与 flex 文档流，26px 的显隐会把整个画布推上推下 —— 这是「界面突然跳一下」的主因。
 * 现在只覆盖画布顶部约 26px，不挤占任何布局。
 */
.ma-banner {
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  z-index: 60;
  display: flex;
  align-items: center;
  gap: 8px;
  height: 26px;
  padding: 0 12px;
  background: color-mix(in srgb, var(--ma-status-reconnecting) 16%, var(--ma-bg-card));
  border-bottom: 1px solid color-mix(in srgb, var(--ma-status-reconnecting) 30%, var(--ma-border));
  box-shadow: 0 2px 6px color-mix(in srgb, #000 18%, transparent);
  color: var(--ma-text-1);
  font-size: var(--ma-font-sm);
}

.ma-banner__dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--ma-status-reconnecting);
  animation: ma-banner-pulse 1.2s ease-in-out infinite;
}

.ma-banner__text {
  flex: none;
}

.ma-banner__detail {
  flex: 1 1 auto;
  min-width: 0;
}

@keyframes ma-banner-pulse {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.25;
  }
}
</style>
