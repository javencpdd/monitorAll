<script setup lang="ts">
/**
 * 卡片错误边界（ARCH §12.2 故障隔离第一层）。
 * 渲染器抛异常时只把该卡降级为错误卡，其余卡片持续刷新，页面不白屏。
 */
import { onErrorCaptured, ref } from 'vue'
import EmptyState from '@/components/base/EmptyState.vue'

const failed = ref<string>('')
const renderKey = ref(0)

onErrorCaptured((err): boolean => {
  failed.value = err instanceof Error ? err.message : String(err)
  // 阻止继续向上冒泡，避免整页崩溃
  return false
})

function retry(): void {
  failed.value = ''
  renderKey.value += 1
}
</script>

<template>
  <div v-if="!failed" :key="renderKey" class="ma-boundary">
    <slot />
  </div>
  <empty-state
    v-else
    icon="error"
    title="此卡片渲染出错"
    :description="`其余卡片不受影响。${failed}`"
  >
    <template #action>
      <button class="ma-boundary__btn" @click="retry">重试</button>
    </template>
  </empty-state>
</template>

<style scoped>
.ma-boundary {
  width: 100%;
  height: 100%;
  min-height: 0;
}

.ma-boundary__btn {
  padding: 4px 12px;
  border: 1px solid var(--ma-border-strong);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-elevated);
  color: var(--ma-text-1);
  cursor: pointer;
  font-size: var(--ma-font-sm);
}

.ma-boundary__btn:hover {
  border-color: var(--ma-accent);
  color: var(--ma-accent);
}
</style>
