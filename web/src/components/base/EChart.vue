<script setup lang="ts">
/**
 * ECharts 实例托管（base 适配层）。
 *
 * 性能约束（ARCH §12.1）：
 *   - 按需引入图表与组件，不整包引入
 *   - setOption 节流 10Hz，animation:false 由调用方在 option 中声明
 *   - ResizeObserver 自适应，unmount 必须 dispose
 */
import { onBeforeUnmount, onMounted, shallowRef, useTemplateRef, watch } from 'vue'
import * as echarts from 'echarts/core'
import { GaugeChart, LineChart } from 'echarts/charts'
import { DataZoomComponent, GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import type { EChartsCoreOption } from 'echarts/core'
import { ECHARTS_THROTTLE_MS } from '@/utils/constants'

echarts.use([
  LineChart,
  GaugeChart,
  GridComponent,
  TooltipComponent,
  LegendComponent,
  DataZoomComponent,
  CanvasRenderer,
])

const props = withDefaults(defineProps<{ option: EChartsCoreOption; autoresize?: boolean }>(), {
  autoresize: true,
})

const hostRef = useTemplateRef<HTMLDivElement>('host')
const chart = shallowRef<echarts.ECharts | null>(null)
let observer: ResizeObserver | null = null
let throttleTimer = 0
let dirty = true

function render(immediate: boolean): void {
  if (!chart.value) return
  dirty = true
  if (immediate) {
    window.clearTimeout(throttleTimer)
    if (dirty) {
      chart.value.setOption(props.option, { lazyUpdate: false })
      dirty = false
    }
    return
  }
  if (throttleTimer !== 0) return
  throttleTimer = window.setTimeout(() => {
    throttleTimer = 0
    if (dirty && chart.value) {
      chart.value.setOption(props.option, { lazyUpdate: false })
      dirty = false
    }
  }, ECHARTS_THROTTLE_MS)
}

function resize(): void {
  chart.value?.resize()
}

onMounted(() => {
  if (!hostRef.value) return
  chart.value = echarts.init(hostRef.value, undefined, { renderer: 'canvas' })
  chart.value.setOption(props.option)
  dirty = false
  if (props.autoresize && typeof ResizeObserver !== 'undefined') {
    observer = new ResizeObserver(() => resize())
    observer.observe(hostRef.value)
  }
})

watch(() => props.option, () => render(false))

onBeforeUnmount(() => {
  if (throttleTimer !== 0) window.clearTimeout(throttleTimer)
  observer?.disconnect()
  observer = null
  chart.value?.dispose()
  chart.value = null
})

defineExpose({ chart, resize })
</script>

<template>
  <div ref="host" class="ma-echart"></div>
</template>

<style scoped>
.ma-echart {
  width: 100%;
  height: 100%;
  min-height: 0;
}
</style>
