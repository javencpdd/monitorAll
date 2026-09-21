<script setup lang="ts">
/** 卡片加载骨架屏（PRD §5.9 等待首帧）。 */
withDefaults(defineProps<{ rows?: number; hint?: string }>(), { rows: 3, hint: '等待数据…' })
</script>

<template>
  <div class="ma-skeleton">
    <div class="ma-skeleton__bars">
      <div v-for="i in rows" :key="i" class="ma-skeleton__bar" :style="{ width: `${100 - i * 12}%` }"></div>
    </div>
    <div class="ma-skeleton__hint ma-text-xs ma-text-3">{{ hint }}</div>
  </div>
</template>

<style scoped>
.ma-skeleton {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 10px;
  height: 100%;
  padding: 12px;
}

.ma-skeleton__bars {
  width: 100%;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.ma-skeleton__bar {
  height: 8px;
  border-radius: 4px;
  background: linear-gradient(90deg, var(--ma-border) 25%, var(--ma-bg-hover) 37%, var(--ma-border) 63%);
  background-size: 400% 100%;
  animation: ma-skeleton-shimmer 1.4s ease infinite;
}

.ma-skeleton__hint {
  animation: ma-skeleton-pulse 1.6s ease-in-out infinite;
}

@keyframes ma-skeleton-shimmer {
  0% {
    background-position: 100% 50%;
  }
  100% {
    background-position: 0 50%;
  }
}

@keyframes ma-skeleton-pulse {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.4;
  }
}
</style>
