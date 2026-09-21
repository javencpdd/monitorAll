<script setup lang="ts">
/**
 * RD-02 表格渲染器（T-25）：
 * - table payload：直接用 columns/rows
 * - json / time_series_sample：自动从最近一帧拍平生成列
 * 支持列选择、排序、行数上限（>上限截断并提示）。
 */
import { computed, ref } from 'vue'
import type { RendererProps, TablePayload } from '@/types'
import { useFrameFeed } from '@/composables/useFrameFeed'
import { flatten } from '@/utils/jsonpath'
import { formatValue, inferType } from '@/utils/format'
import EmptyState from '@/components/base/EmptyState.vue'

const props = defineProps<RendererProps>()

const { latest, getBuffer } = useFrameFeed(() => props.card.channelId)

const maxRows = computed<number>(() => Number(props.config.maxRows ?? 1000))
const columnConfig = computed<string[]>(() =>
  Array.isArray(props.config.columns) ? (props.config.columns as string[]) : [],
)
const sortable = computed<boolean>(() => props.config.sortable !== false)
const freezeFirst = computed<boolean>(() => props.config.freezeFirst === true)

const sortKey = ref<string | undefined>(undefined)
const sortDesc = ref(false)

/** 从各种 payload 归一化为 {columns, rows}。 */
const table = computed<{ columns: string[]; types: Record<string, string>; rows: unknown[][] }>(() => {
  const payload = latest.value?.payload as TablePayload | undefined
  if (payload && Array.isArray(payload.columns) && Array.isArray(payload.rows)) {
    const columns = payload.columns.map((c) => c.key)
    const types: Record<string, string> = {}
    for (const c of payload.columns) types[c.key] = c.type
    return { columns, types, rows: payload.rows }
  }
  // 兜底：把对象的深层字段拍平成一行
  const flat = flatten(latest.value?.payload ?? {}, { maxDepth: 6 })
  if (flat.length === 0) return { columns: [], types: {}, rows: [] }
  const columns = flat.map((f) => f.path)
  const types: Record<string, string> = {}
  const row: unknown[] = []
  for (const f of flat) {
    types[f.path] = f.type
    row.push(f.value)
  }
  return { columns, types, rows: [row] }
})

const visibleColumns = computed<string[]>(() => {
  const all = table.value.columns
  const wanted = columnConfig.value.filter((c) => all.includes(c))
  return wanted.length > 0 ? wanted : all
})

const truncatedInfo = computed<{ truncated: boolean; total: number }>(() => {
  const payload = latest.value?.payload as TablePayload | undefined
  const total = payload?.total ?? table.value.rows.length
  return { truncated: payload?.truncated === true || table.value.rows.length > maxRows.value, total }
})

const rows = computed<unknown[][]>(() => {
  const shown = table.value.rows.slice(0, maxRows.value)
  if (!sortKey.value || !sortable.value) return shown
  const col = visibleColumns.value.indexOf(sortKey.value)
  if (col < 0) return shown
  const desc = sortDesc.value
  return [...shown].sort((a, b) => {
    const av = a[col]
    const bv = b[col]
    if (typeof av === 'number' && typeof bv === 'number') return desc ? bv - av : av - bv
    const as = String(av ?? '')
    const bs = String(bv ?? '')
    return desc ? bs.localeCompare(as) : as.localeCompare(bs)
  })
})

function sortBy(col: string): void {
  if (!sortable.value) return
  if (sortKey.value === col) {
    if (!sortDesc.value) sortDesc.value = true
    else {
      sortDesc.value = false
      sortKey.value = undefined
    }
    return
  }
  sortKey.value = col
  sortDesc.value = false
}

function cellType(col: string): string {
  return table.value.types[col] ?? 'string'
}

function historyCount(): number {
  return getBuffer()?.size ?? 0
}
</script>

<template>
  <div class="ma-table">
    <template v-if="visibleColumns.length > 0">
      <div class="ma-table__scroll ma-scroll-y">
        <table class="ma-table__el">
          <thead>
            <tr>
              <th
                v-for="(col, idx) in visibleColumns"
                :key="col"
                :class="{ 'is-frozen': freezeFirst && idx === 0, 'is-num': cellType(col) === 'number' }"
                @click="sortBy(col)"
              >
                <span class="ma-ellipsis">{{ col }}</span>
                <span v-if="sortKey === col" class="ma-table__sort">{{ sortDesc ? '▼' : '▲' }}</span>
              </th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(row, r) in rows" :key="r">
              <td
                v-for="(col, idx) in visibleColumns"
                :key="`${r}-${col}`"
                :class="{ 'is-frozen': freezeFirst && idx === 0, 'is-num': ['number', 'boolean', 'null'].includes(inferType(row[visibleColumns.indexOf(col)])) }"
              >
                {{ formatValue(row[visibleColumns.indexOf(col)]) }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <div class="ma-table__foot ma-text-xs ma-text-3">
        显示 {{ rows.length }} / {{ truncatedInfo.total }} 行 · 历史缓冲 {{ historyCount() }} 帧
        <span v-if="truncatedInfo.truncated" class="ma-table__warn">（已按上限截断，可调大最大行数）</span>
      </div>
    </template>
    <empty-state v-else compact icon="empty" title="暂无表格数据" description="等待首帧或检查 JSONPath 配置" />
  </div>
</template>

<style scoped>
.ma-table {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.ma-table__scroll {
  flex: 1 1 auto;
  min-height: 0;
  overflow: auto;
}

.ma-table__el {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--ma-font-xs);
  font-family: 'JetBrains Mono', monospace;
}

.ma-table__el th,
.ma-table__el td {
  padding: 3px 6px;
  text-align: left;
  border-bottom: 1px solid var(--ma-border);
  white-space: nowrap;
  max-width: 220px;
  overflow: hidden;
  text-overflow: ellipsis;
}

.ma-table__el th {
  position: sticky;
  top: 0;
  z-index: 2;
  background: var(--ma-bg-elevated);
  color: var(--ma-text-2);
  font-weight: 500;
  cursor: pointer;
  user-select: none;
}

.ma-table__el th.is-frozen,
.ma-table__el td.is-frozen {
  position: sticky;
  left: 0;
  z-index: 1;
  background: var(--ma-bg-card);
}

.ma-table__el td.is-num,
.ma-table__el th.is-num {
  text-align: right;
  color: var(--ma-accent);
}

.ma-table__el tbody tr:hover td {
  background: var(--ma-bg-hover);
}

.ma-table__sort {
  margin-left: 3px;
  color: var(--ma-accent);
}

.ma-table__foot {
  flex: none;
  padding: 2px 6px;
  border-top: 1px solid var(--ma-border);
}

.ma-table__warn {
  color: var(--ma-status-reconnecting);
}
</style>
