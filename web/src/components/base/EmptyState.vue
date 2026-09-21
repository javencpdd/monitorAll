<script setup lang="ts">
/**
 * 空态 / 错误态占位（ARCH §5.9）。
 * 统一的占位外观：图标 + 主文案 + 说明 + 可选操作。
 */
withDefaults(
  defineProps<{
    title?: string
    description?: string
    /** 语义化图标名，决定内联 svg。 */
    icon?: 'empty' | 'waiting' | 'error' | 'config' | 'map'
    compact?: boolean
  }>(),
  { title: '暂无数据', description: '', icon: 'empty', compact: false },
)
</script>

<template>
  <div class="ma-empty" :class="{ 'ma-empty--compact': compact }">
    <svg class="ma-empty__icon" viewBox="0 0 48 48" aria-hidden="true">
      <template v-if="icon === 'error'">
        <circle cx="24" cy="24" r="19" fill="none" stroke="var(--ma-status-error)" stroke-width="2.5" />
        <path d="M24 14v14" stroke="var(--ma-status-error)" stroke-width="2.5" stroke-linecap="round" />
        <circle cx="24" cy="33" r="1.6" fill="var(--ma-status-error)" />
      </template>
      <template v-else-if="icon === 'waiting'">
        <circle cx="24" cy="24" r="19" fill="none" stroke="var(--ma-status-reconnecting)" stroke-width="2.5" />
        <path d="M24 13v11l8 5" stroke="var(--ma-status-reconnecting)" stroke-width="2.5" stroke-linecap="round" fill="none" />
      </template>
      <template v-else-if="icon === 'config'">
        <rect x="7" y="12" width="34" height="24" rx="3" fill="none" stroke="var(--ma-accent)" stroke-width="2.5" />
        <path d="M17 12v-4h14v4M17 36v4h14v-4" stroke="var(--ma-accent)" stroke-width="2.5" fill="none" />
        <circle cx="24" cy="24" r="4" fill="none" stroke="var(--ma-accent)" stroke-width="2.5" />
      </template>
      <template v-else-if="icon === 'map'">
        <path d="M24 6l14 7v22l-14 7L10 35V13z" fill="none" stroke="var(--ma-accent)" stroke-width="2.5" />
        <path d="M24 6v30M10 13l14 7 14-7" fill="none" stroke="var(--ma-accent)" stroke-width="2" />
      </template>
      <template v-else>
        <rect x="8" y="12" width="32" height="24" rx="3" fill="none" stroke="var(--ma-text-3)" stroke-width="2.5" />
        <path d="M8 21h32M16 29h16" stroke="var(--ma-text-3)" stroke-width="2.5" stroke-linecap="round" />
      </template>
    </svg>
    <div class="ma-empty__title">{{ title }}</div>
    <div v-if="description" class="ma-empty__desc">{{ description }}</div>
    <div v-if="$slots.action" class="ma-empty__action">
      <slot name="action" />
    </div>
    <div v-if="$slots.default" class="ma-empty__extra">
      <slot />
    </div>
  </div>
</template>

<style scoped>
.ma-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 6px;
  height: 100%;
  min-height: 80px;
  padding: 16px;
  text-align: center;
  color: var(--ma-text-3);
}

.ma-empty--compact {
  min-height: 48px;
  padding: 8px;
  gap: 4px;
}

.ma-empty__icon {
  width: 40px;
  height: 40px;
  opacity: 0.9;
}

.ma-empty--compact .ma-empty__icon {
  width: 28px;
  height: 28px;
}

.ma-empty__title {
  font-size: var(--ma-font-md);
  color: var(--ma-text-2);
}

.ma-empty__desc {
  font-size: var(--ma-font-sm);
  color: var(--ma-text-3);
  max-width: 42ch;
  line-height: 1.45;
}

.ma-empty__action,
.ma-empty__extra {
  margin-top: 4px;
}
</style>
