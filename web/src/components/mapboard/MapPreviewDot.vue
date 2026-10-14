<script setup lang="ts">
/**
 * 表单预览点（v3）：把「节点表单里正在填的坐标」画在地图上。
 *
 * 为什么需要它 —— 这是回答"我这份坐标是什么坐标系"**唯一可靠**的办法：
 *   WGS-84 与 GCJ-02 在中国境内只差 300~700 米，比两个不同地点的距离还小，
 *   因此**光看数字分辨不出来**。但一旦把它画到高德底图上，选错标准就会明显偏出，
 *   肉眼立刻可见 —— 把"猜"变成了"看"。
 *
 * 实现上复用同步池（与节点/卡片同源同帧），不新建一套定位逻辑：
 * `anchor()` 返回 undefined 时同步池会跳过该条目，所以无需为此增删注册。
 *
 * ⚠️ 预览点的真源是 **`mapBoardStore.previewPoint`**（不是 runtime）：
 * 写它的是兄弟组件 `MapNodeInspector`，而 runtime 只对 `MapBoardSurface` 子树有效 ——
 * 放在 runtime 里会让节点面板 inject 失败、在 setup 抛异常并静默消失。
 * 本组件**在子树内**，所以照旧用 runtime 做投影。
 */
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useMapRuntime } from '@/composables/useMapProjection'
import { useMapBoardStore } from '@/stores/mapBoard'
import type { LngLat } from '@/utils/geo'

const store = useMapBoardStore()
const runtime = useMapRuntime()
const el = ref<HTMLElement | null>(null)

let unregister: (() => void) | null = null

function anchor(): LngLat | undefined {
  return store.previewPoint ?? undefined
}

onMounted(() => {
  unregister = runtime.projection.register('preview:form', {
    el: el.value,
    anchor,
    offset: () => ({ x: 0, y: 0 }),
    size: () => ({ w: 0, h: 0 }),
  })
})

onBeforeUnmount(() => {
  unregister?.()
  unregister = null
})
</script>

<template>
  <div ref="el" class="mb-preview" title="表单中坐标的落点预览">
    <span class="mb-preview__ring"></span>
    <span class="mb-preview__dot"></span>
  </div>
</template>

<style scoped>
.mb-preview {
  position: absolute;
  top: 0;
  left: 0;
  z-index: 3;
  /* 只做视觉标记，不参与任何交互（免得挡住底图右键取点） */
  pointer-events: none;
  will-change: transform;
}

.mb-preview__ring {
  position: absolute;
  top: -14px;
  left: -14px;
  width: 28px;
  height: 28px;
  border: 2px dashed var(--ma-accent);
  border-radius: 50%;
  animation: mb-preview-spin 6s linear infinite;
}

.mb-preview__dot {
  position: absolute;
  top: -4px;
  left: -4px;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--ma-accent);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--ma-accent) 25%, transparent);
}

@keyframes mb-preview-spin {
  to {
    transform: rotate(360deg);
  }
}

@media (prefers-reduced-motion: reduce) {
  .mb-preview__ring {
    animation: none;
  }
}
</style>
