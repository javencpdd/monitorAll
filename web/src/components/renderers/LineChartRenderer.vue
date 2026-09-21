<script setup lang="ts">
/**
 * RD-04 XY 折线图（RD-04 / T-26）：
 * 多字段映射（图例即开关）、时间窗口默认 60s、Y 轴自动/固定、暂停、清空、CSV 导出。
 *
 * 性能（ARCH §12.1）：
 *   - 从 markRaw 的环形缓冲命令式读历史，不走响应式
 *   - animation:false + sampling:lttb + large 系列
 *   - 数据重算节流 10Hz；setOption 由 base/EChart.vue 再做 10Hz 节流
 */
import { computed, ref, watch } from 'vue'
import type { RendererProps, TimeSeriesPayload } from '@/types'
import { useFrameFeed } from '@/composables/useFrameFeed'
import { CHART_SERIES_COLORS, DEFAULT_WINDOW_SEC, ECHARTS_THROTTLE_MS, LINE_MAX_FIELDS } from '@/utils/constants'
import { cssColor } from '@/styles/theme'
import { useUiStore } from '@/stores/ui'
import { downloadFile, safeFilename, toCSV } from '@/utils/csv'
import { clampDecimals, formatTimeShort } from '@/utils/format'
import EChart from '@/components/base/EChart.vue'
import EmptyState from '@/components/base/EmptyState.vue'

const props = defineProps<RendererProps>()
const ui = useUiStore()

const maxPoints = computed<number>(() => Number(props.config.maxPoints ?? 1200))
const { latest, version, getBuffer, clear } = useFrameFeed(() => props.card.channelId, {
  capacity: () => maxPoints.value,
})

const windowSec = computed<number>(() => Number(props.config.windowSec ?? DEFAULT_WINDOW_SEC))
const yAxisMode = computed<'auto' | 'fixed'>(() => (props.config.yAxisMode === 'fixed' ? 'fixed' : 'auto'))
const yMin = computed<number>(() => Number(props.config.yMin ?? 0))
const yMax = computed<number>(() => Number(props.config.yMax ?? 1))
const sampling = computed<'lttb' | 'none'>(() => (props.config.sampling === 'none' ? 'none' : 'lttb'))
const showLegend = computed<boolean>(() => props.config.showLegend !== false)

/** 已选字段（来自 multi-field 配置；留空则自动取前 N 个数值字段）。 */
const configuredFields = computed<string[]>(() =>
  Array.isArray(props.config.fields) ? (props.config.fields as string[]).slice(0, LINE_MAX_FIELDS) : [],
)

const paused = ref(false)
/** 图例关闭的字段集合。 */
const hiddenFields = ref<string[]>([])

function frameFields(frame: { payloadType: string; payload: unknown }): Record<string, number> {
  const payload = frame.payload as Record<string, unknown> | undefined
  if (!payload) return {}
  if (frame.payloadType === 'time_series_sample') {
    return (payload.fields as Record<string, number> | undefined) ?? {}
  }
  if (frame.payloadType === 'scalar') {
    const value = payload.value
    return typeof value === 'number' ? { value } : {}
  }
  if (frame.payloadType === 'geo_pose') {
    // 地理位姿也可画成折线（经纬度/高度/速度）
    const out: Record<string, number> = {}
    for (const key of ['lat', 'lon', 'alt', 'yawDeg', 'speed']) {
      if (typeof payload[key] === 'number') out[key] = payload[key] as number
    }
    return out
  }
  const flat: Record<string, number> = {}
  const walk = (value: unknown, path: string, depth: number): void => {
    if (depth > 4) return
    if (typeof value === 'number' && Number.isFinite(value)) {
      flat[path] = value
      return
    }
    if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
      for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
        walk(v, path ? `${path}.${k}` : k, depth + 1)
      }
    }
  }
  walk(payload, '', 0)
  return flat
}

/** 最近一帧里出现的数值字段名（用于自动选字段）。 */
const availableFields = computed<string[]>(() => Object.keys(frameFields(latest.value ?? { payloadType: 'json', payload: undefined })))

const activeFields = computed<string[]>(() => {
  const fields = configuredFields.value.length > 0 ? configuredFields.value : availableFields.value.slice(0, LINE_MAX_FIELDS)
  return fields.filter((f) => !hiddenFields.value.includes(f))
})

function frameTime(frame: { payloadType: string; payload: unknown; publishedTs: number }): number {
  if (frame.payloadType === 'time_series_sample') {
    const t = (frame.payload as TimeSeriesPayload | undefined)?.t
    if (typeof t === 'number' && t > 0) return t
  }
  return frame.publishedTs
}

interface Snapshot {
  series: Record<string, [number, number][]>
  fields: string[]
  count: number
}

/** 命令式读取缓冲并按窗口裁剪（不走响应式）。 */
function collect(): Snapshot {
  const buffer = getBuffer()
  const series: Record<string, [number, number][]> = {}
  const fields = new Set<string>()
  if (!buffer) return { series, fields: [], count: 0 }
  const cutoff = Date.now() - windowSec.value * 1000
  let count = 0
  buffer.forEach((frame) => {
    const t = frameTime(frame)
    if (t < cutoff) return
    count += 1
    const values = frameFields(frame)
    for (const key of Object.keys(values)) {
      fields.add(key)
      if (!series[key]) series[key] = []
      const v = values[key]
      if (v !== undefined) series[key].push([t, v])
    }
  })
  return { series, fields: Array.from(fields), count }
}

const snapshot = ref<Snapshot>({ series: {}, fields: [], count: 0 })
let lastCollect = 0

function refresh(force: boolean): void {
  if (paused.value) return
  const now = performance.now()
  if (!force && now - lastCollect < ECHARTS_THROTTLE_MS) return
  lastCollect = now
  snapshot.value = collect()
}

watch(version, () => refresh(false))
watch([windowSec, () => props.config.fields], () => refresh(true))

const palette = computed<string[]>(() => {
  void ui.themeKind
  const fallbacks = ['#63e2b7', '#63a0e5', '#f2c037', '#f2a037', '#e5534b', '#7cecc5', '#9a9aa6', '#6b6b76']
  return CHART_SERIES_COLORS.map((name, i) => cssColor(name, fallbacks[i] ?? '#63e2b7'))
})

const axisColor = computed<string>(() => {
  void ui.themeKind
  return cssColor('--ma-text-3', '#6b6b76')
})
const splitColor = computed<string>(() => {
  void ui.themeKind
  return cssColor('--ma-border', '#2c2c34')
})
const tooltipBg = computed<string>(() => {
  void ui.themeKind
  return cssColor('--ma-bg-elevated', '#1f1f24')
})

const option = computed(() => {
  const colors = palette.value
  const fields = activeFields.value
  return {
    animation: false,
    backgroundColor: 'transparent',
    color: colors,
    grid: { left: 44, right: 12, top: 12, bottom: showLegend.value ? (fields.length > 4 ? 44 : 28) : 18 },
    tooltip: {
      trigger: 'axis',
      backgroundColor: tooltipBg.value,
      borderColor: splitColor.value,
      textStyle: { color: axisColor.value, fontSize: 11 },
      axisPointer: { lineStyle: { color: splitColor.value } },
    },
    legend: {
      show: showLegend.value && fields.length > 0,
      bottom: 0,
      left: 4,
      itemWidth: 10,
      itemHeight: 6,
      textStyle: { color: axisColor.value, fontSize: 10 },
      data: fields,
    },
    xAxis: {
      type: 'time',
      axisLine: { lineStyle: { color: splitColor.value } },
      axisTick: { show: false },
      axisLabel: { color: axisColor.value, fontSize: 10, formatter: (v: number) => formatTimeShort(v) },
      splitLine: { show: false },
    },
    yAxis: {
      type: 'value',
      scale: true,
      min: yAxisMode.value === 'fixed' ? yMin.value : undefined,
      max: yAxisMode.value === 'fixed' ? yMax.value : undefined,
      axisLine: { show: false },
      axisLabel: { color: axisColor.value, fontSize: 10 },
      splitLine: { lineStyle: { color: splitColor.value, type: 'dashed' } },
    },
    series: fields.map((field, idx) => ({
      type: 'line',
      name: field,
      showSymbol: false,
      smooth: false,
      sampling: sampling.value,
      large: true,
      largeThreshold: 500,
      lineStyle: { width: 1.4, color: colors[idx % colors.length] },
      data: snapshot.value.series[field] ?? [],
    })),
  }
})

/** 导出 CSV：与图上可见数据完全一致。 */
function exportCsv(): void {
  const fields = activeFields.value
  if (fields.length === 0) {
    ui.warning('暂无可导出的数据')
    return
  }
  const seriesSnap = snapshot.value.series
  const timeIndex = new Map<number, Record<string, number | undefined>>()
  for (const field of fields) {
    for (const [t, v] of seriesSnap[field] ?? []) {
      const row = timeIndex.get(t) ?? {}
      row[field] = v
      timeIndex.set(t, row)
    }
  }
  const times = Array.from(timeIndex.keys()).sort((a, b) => a - b)
  const decimalFallback = clampDecimals(4)
  const rows = times.map((t) => {
    const row = timeIndex.get(t) ?? {}
    return [
      new Date(t).toISOString(),
      t,
      ...fields.map((f) => (typeof row[f] === 'number' ? (row[f] as number).toFixed(decimalFallback) : '')),
    ]
  })
  const csv = toCSV(['isoTime', 'tsMs', ...fields], rows)
  downloadFile(`${safeFilename(props.card.title || 'chart')}.csv`, csv)
  ui.success(`已导出 ${rows.length} 行`)
}

function togglePause(): void {
  paused.value = !paused.value
  if (!paused.value) refresh(true)
}

function clearHistory(): void {
  clear()
  snapshot.value = { series: {}, fields: [], count: 0 }
}
</script>

<template>
  <div class="ma-line">
    <div class="ma-line__toolbar">
      <button class="ma-line__btn" :class="{ 'is-on': paused }" title="暂停/继续" @click="togglePause">
        {{ paused ? '▶ 继续' : '⏸ 暂停' }}
      </button>
      <button class="ma-line__btn" title="清空缓冲" @click="clearHistory">⟲ 清空</button>
      <button class="ma-line__btn" title="导出 CSV" @click="exportCsv">⟱ CSV</button>
      <span class="ma-line__info ma-text-xs ma-text-3">
        窗口 {{ windowSec }}s · {{ snapshot.count }} 点
      </span>
    </div>

    <div class="ma-line__chart">
      <e-chart v-if="activeFields.length > 0" :option="option" />
      <empty-state v-else compact icon="waiting" title="等待数据" description="选择至少一个数值字段，或等待首帧到达" />
    </div>

    <div v-if="snapshot.fields.length > activeFields.length" class="ma-line__hidden ma-text-xs ma-text-3">
      已隐藏：{{ snapshot.fields.filter((f) => hiddenFields.includes(f)).join('、') }}
    </div>
  </div>
</template>

<style scoped>
.ma-line {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.ma-line__toolbar {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 2px 6px;
  flex: none;
}

.ma-line__btn {
  padding: 1px 6px;
  border: 1px solid var(--ma-border);
  border-radius: 4px;
  background: var(--ma-bg-base);
  color: var(--ma-text-2);
  cursor: pointer;
  font-size: var(--ma-font-xs);
}

.ma-line__btn:hover {
  color: var(--ma-accent);
  border-color: var(--ma-accent);
}

.ma-line__btn.is-on {
  color: var(--ma-status-reconnecting);
  border-color: var(--ma-status-reconnecting);
}

.ma-line__info {
  margin-left: auto;
}

.ma-line__chart {
  flex: 1 1 auto;
  min-height: 0;
}

.ma-line__hidden {
  flex: none;
  padding: 0 6px 2px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
