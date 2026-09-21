<script setup lang="ts">
/**
 * RD-03 仪表渲染器（RD-03 / T-26）：
 * 量程/单位/小数位可配，红黄绿阈值区段着色，超量程显示满量程并标红，脚注显示 MIN/MAX。
 * ECharts 按需引入 + animation:false，setOption 由 base/EChart.vue 节流至 10Hz。
 */
import { computed, ref, watch } from 'vue'
import type { RendererProps, ScalarPayload, ThresholdBand } from '@/types'
import { useFrameFeed } from '@/composables/useFrameFeed'
import { getNumberByPath, hasPath } from '@/utils/jsonpath'
import { cssColor } from '@/styles/theme'
import { useUiStore } from '@/stores/ui'
import { clamp, clampDecimals, formatClock, formatNumber } from '@/utils/format'
import EChart from '@/components/base/EChart.vue'
import EmptyState from '@/components/base/EmptyState.vue'

const props = defineProps<RendererProps>()
const ui = useUiStore()

const { latest } = useFrameFeed(() => props.card.channelId)

const min = computed<number>(() => Number(props.config.min ?? 0))
const max = computed<number>(() => Number(props.config.max ?? 30))
const decimals = computed<number>(() => clampDecimals(Number(props.config.decimals ?? 2)))
const showPointer = computed<boolean>(() => props.config.showPointer !== false)
const showMinMax = computed<boolean>(() => props.config.showMinMax !== false)
const bands = computed<ThresholdBand[]>(() =>
  Array.isArray(props.config.bands) ? (props.config.bands as ThresholdBand[]) : [],
)

/** 从各类型 payload 中取出当前值。 */
const value = computed<number>(() => {
  const frame = latest.value
  if (!frame) return Number.NaN
  const payload = frame.payload as Record<string, unknown> | undefined
  if (!payload) return Number.NaN
  if (frame.payloadType === 'scalar') {
    return Number((payload as unknown as ScalarPayload).value)
  }
  const field = typeof props.config.field === 'string' ? props.config.field : ''
  if (frame.payloadType === 'time_series_sample') {
    const fields = payload.fields as Record<string, number> | undefined
    if (fields && field in fields) return Number(fields[field])
    return firstNumeric(fields)
  }
  if (hasPath(field)) return getNumberByPath(payload, field)
  return firstNumeric(payload as Record<string, number>)
})

function firstNumeric(source: Record<string, number> | undefined): number {
  if (!source) return Number.NaN
  for (const key of Object.keys(source)) {
    const v = source[key]
    if (typeof v === 'number' && Number.isFinite(v)) return v
  }
  return Number.NaN
}

/** MIN/MAX 跟踪（O(1) 增量更新）。 */
const observedMin = ref<number>(Number.NaN)
const observedMax = ref<number>(Number.NaN)
const lastStamp = ref<number>(0)

watch(value, (v) => {
  if (!Number.isFinite(v)) return
  observedMin.value = Number.isFinite(observedMin.value) ? Math.min(observedMin.value, v) : v
  observedMax.value = Number.isFinite(observedMax.value) ? Math.max(observedMax.value, v) : v
  lastStamp.value = latest.value?.ingestedTs ?? Date.now()
})

const unit = computed<string>(() => {
  const frame = latest.value
  if (!frame) return props.card.unit ?? ''
  const payload = frame.payload as ScalarPayload | undefined
  return props.card.unit || payload?.unit || ''
})

const clampedValue = computed<number>(() => {
  if (!Number.isFinite(value.value)) return min.value
  return clamp(value.value, min.value, max.value)
})

const outOfRange = computed<boolean>(
  () => Number.isFinite(value.value) && (value.value < min.value || value.value > max.value),
)

/**
 * Canvas 渲染必须拿到具体色值（ECharts 不解析 var(--*)）；
 * 依赖 ui.themeKind，保证换肤后重新解析。
 */
const palette = computed(() => {
  void ui.themeKind
  return {
    text1: cssColor('--ma-text-1', '#e5e5ea'),
    text3: cssColor('--ma-text-3', '#6b6b76'),
    border: cssColor('--ma-border-strong', '#3a3a45'),
    error: cssColor('--ma-status-error', '#e5534b'),
  }
})

const axisColors = computed<[number, string][]>(() => {
  const span = max.value - min.value
  if (span <= 0 || bands.value.length === 0) return [[1, palette.value.border]]
  const stops: [number, string][] = []
  let prev = 0
  for (const band of bands.value) {
    const fromRatio = clamp((band.from - min.value) / span, 0, 1)
    const toRatio = clamp((band.to - min.value) / span, 0, 1)
    if (toRatio <= prev) continue
    if (fromRatio > prev) stops.push([fromRatio, palette.value.border])
    stops.push([toRatio, band.color])
    prev = toRatio
  }
  if (prev < 1) stops.push([1, palette.value.border])
  return stops
})

const option = computed(() => ({
  animation: false,
  backgroundColor: 'transparent',
  series: [
    {
      type: 'gauge',
      min: min.value,
      max: max.value,
      startAngle: 210,
      endAngle: -30,
      radius: '92%',
      center: ['50%', '58%'],
      splitNumber: 5,
      axisLine: { lineStyle: { width: 10, color: axisColors.value } },
      pointer: showPointer.value
        ? {
            icon: 'path://M2,0 L2,-60 L6,-60 L6,0 Z',
            width: 5,
            length: '64%',
            offsetCenter: [0, '0%'],
            itemStyle: { color: outOfRange.value ? palette.value.error : palette.value.text1 },
          }
        : { show: false },
      axisTick: { distance: -10, length: 4, lineStyle: { color: palette.value.text3, width: 1 } },
      splitLine: { distance: -10, length: 8, lineStyle: { color: palette.value.text3, width: 1.5 } },
      axisLabel: { distance: 12, color: palette.value.text3, fontSize: 9 },
      title: { show: unit.value.length > 0, offsetCenter: [0, '32%'], color: palette.value.text3, fontSize: 11 },
      detail: {
        valueAnimation: false,
        offsetCenter: [0, '2%'],
        fontSize: 24,
        fontWeight: 600,
        color: outOfRange.value ? palette.value.error : palette.value.text1,
        formatter: (v: number) => `${v.toFixed(decimals.value)}${unit.value ? ` ${unit.value}` : ''}`,
      },
      data: [{ value: clampedValue.value, name: unit.value }],
    },
  ],
}))
</script>

<template>
  <div class="ma-gauge">
    <template v-if="Number.isFinite(value)">
      <div class="ma-gauge__chart">
        <e-chart :option="option" />
      </div>
      <div class="ma-gauge__meta ma-text-xs ma-text-3">
        <span v-if="showMinMax">MIN {{ formatNumber(observedMin, decimals) }} · MAX {{ formatNumber(observedMax, decimals) }}</span>
        <span v-if="outOfRange" class="ma-gauge__warn">超量程（{{ formatNumber(value, decimals) }}）</span>
        <span class="ma-gauge__stamp">{{ formatClock(lastStamp) }}</span>
      </div>
    </template>
    <empty-state v-else compact icon="waiting" title="等待数值" description="未取到可解析的数值字段" />
  </div>
</template>

<style scoped>
.ma-gauge {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.ma-gauge__chart {
  flex: 1 1 auto;
  min-height: 0;
}

.ma-gauge__meta {
  flex: none;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 0 8px 2px;
  justify-content: center;
}

.ma-gauge__warn {
  color: var(--ma-status-error);
}

.ma-gauge__stamp {
  opacity: 0.66;
}
</style>
