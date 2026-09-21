<script setup lang="ts">
/**
 * RD-01 原始数据 JSON 树（T-25）：任意嵌套可折叠、类型标注、深度控制、节点数上限。
 * payloadType=json 时取 payload.root，其余类型直接展示 payload。
 */
import { computed, ref, watch } from 'vue'
import type { RendererProps } from '@/types'
import { useFrameFeed } from '@/composables/useFrameFeed'
import { JSON_TREE_MAX_DEPTH } from '@/utils/constants'
import { formatValue } from '@/utils/format'
import EmptyState from '@/components/base/EmptyState.vue'

const props = defineProps<RendererProps>()

const { latest, version } = useFrameFeed(() => props.card.channelId)

const root = computed<unknown>(() => {
  const payload = latest.value?.payload as Record<string, unknown> | undefined
  if (payload && typeof payload === 'object' && 'root' in payload) {
    return (payload as { root: unknown }).root
  }
  return payload
})

const maxNodes = computed<number>(() => Number(props.config.maxNodes ?? 5000))
const defaultDepth = computed<number>(() => Number(props.config.defaultExpandDepth ?? 3))
const showType = computed<boolean>(() => props.config.showType !== false)

interface Row {
  key: string
  path: string
  depth: number
  value: unknown
  type: 'number' | 'string' | 'boolean' | 'object' | 'array' | 'null'
  childCount: number
  expandable: boolean
}

const expanded = ref<Record<string, boolean>>({})
const truncated = ref(false)

function rowType(entry: Entry): Row['type'] {
  if (entry.value === null) return 'null'
  if (Array.isArray(entry.value)) return 'array'
  if (typeof entry.value === 'object') return 'object'
  if (typeof entry.value === 'number') return 'number'
  if (typeof entry.value === 'boolean') return 'boolean'
  return 'string'
}

interface Entry {
  key: string
  path: string
  value: unknown
}

function entriesOf(value: unknown, path: string): Entry[] {
  if (Array.isArray(value)) {
    return value.map((item, i) => ({ key: String(i), path: path ? `${path}[${i}]` : `[${i}]`, value: item }))
  }
  if (value !== null && typeof value === 'object') {
    return Object.entries(value as Record<string, unknown>).map(([k, v]) => ({
      key: k,
      path: path ? `${path}.${k}` : k,
      value: v,
    }))
  }
  return []
}

/** 按展开状态拍平成行（避免递归组件），并施加节点数上限。 */
const rows = computed<Row[]>(() => {
  truncated.value = false
  const out: Row[] = []
  let count = 0
  const stack: { entry: Entry; depth: number }[] = entriesOf(root.value, '').map((entry) => ({ entry, depth: 0 }))
  // 逆序入栈保证按原始顺序输出
  stack.reverse()
  let guard = 0
  while (stack.length > 0) {
    guard += 1
    if (guard > JSON_TREE_MAX_DEPTH * maxNodes.value + 1000) break
    const item = stack.pop()
    if (!item) break
    const { entry, depth } = item
    count += 1
    if (count > maxNodes.value) {
      truncated.value = true
      break
    }
    const type = rowType(entry)
    const children = type === 'object' || type === 'array' ? entriesOf(entry.value, entry.path) : []
    const expandable = children.length > 0 && depth < JSON_TREE_MAX_DEPTH
    out.push({
      key: entry.key,
      path: entry.path,
      depth,
      value: entry.value,
      type,
      childCount: children.length,
      expandable,
    })
    if (expandable && expanded.value[entry.path]) {
      for (let i = children.length - 1; i >= 0; i -= 1) {
        stack.push({ entry: children[i] as Entry, depth: depth + 1 })
      }
    }
  }
  return out
})

function isExpanded(path: string): boolean {
  const explicit = expanded.value[path]
  if (explicit !== undefined) return explicit
  return (path.split(/[.[]/).length - 1) < defaultDepth.value
}

function toggle(path: string): void {
  expanded.value[path] = !isExpanded(path)
}

/** 数据大幅变化时重置展开状态，避免残留失效路径。 */
watch(version, () => {
  if (Object.keys(expanded.value).length > maxNodes.value) expanded.value = {}
})

function preview(row: Row): string {
  if (row.expandable) return row.type === 'array' ? `Array(${row.childCount})` : `{${row.childCount}}`
  return formatValue(row.value)
}

const TYPE_CLASS: Record<Row['type'], string> = {
  number: 'num',
  string: 'str',
  boolean: 'bool',
  object: 'obj',
  array: 'arr',
  null: 'nul',
}
</script>

<template>
  <div class="ma-jtree ma-scroll-y">
    <template v-if="rows.length > 0">
      <div
        v-for="row in rows"
        :key="row.path"
        class="ma-jtree__row"
        :style="{ paddingLeft: `${row.depth * 12 + 4}px` }"
        @click="row.expandable && toggle(row.path)"
      >
        <span class="ma-jtree__caret">
          {{ row.expandable ? (isExpanded(row.path) ? '▾' : '▸') : '·' }}
        </span>
        <span class="ma-jtree__key ma-ellipsis" :title="row.path">{{ row.key }}</span>
        <span v-if="showType" class="ma-jtree__type" :class="`is-${TYPE_CLASS[row.type]}`">{{ TYPE_CLASS[row.type] }}</span>
        <span class="ma-jtree__val ma-ellipsis">{{ preview(row) }}</span>
      </div>
      <div v-if="truncated" class="ma-jtree__trunc ma-text-xs ma-text-3">
        节点数超过 {{ maxNodes }}，已截断显示（可在配置里调大上限）
      </div>
    </template>
    <empty-state v-else compact icon="empty" title="等待数据" description="尚未收到该通道的帧" />
  </div>
</template>

<style scoped>
.ma-jtree {
  height: 100%;
  padding: 4px 0;
  font-family: 'JetBrains Mono', monospace;
  font-size: var(--ma-font-xs);
}

.ma-jtree__row {
  display: flex;
  align-items: center;
  gap: 4px;
  height: 20px;
  padding-right: 6px;
  cursor: default;
}

.ma-jtree__row:hover {
  background: var(--ma-bg-hover);
}

.ma-jtree__caret {
  width: 11px;
  flex: none;
  color: var(--ma-text-3);
  font-size: 9px;
}

.ma-jtree__key {
  flex: 0 1 auto;
  min-width: 30px;
  max-width: 45%;
  color: var(--ma-text-1);
}

.ma-jtree__type {
  flex: none;
  padding: 0 3px;
  border-radius: 3px;
  border: 1px solid var(--ma-border-strong);
  color: var(--ma-text-3);
  transform: scale(0.9);
}

.ma-jtree__type.is-num {
  color: var(--ma-accent);
  border-color: color-mix(in srgb, var(--ma-accent) 40%, transparent);
}

.ma-jtree__type.is-str {
  color: var(--ma-status-connecting);
  border-color: color-mix(in srgb, var(--ma-status-connecting) 40%, transparent);
}

.ma-jtree__type.is-bool {
  color: var(--ma-status-reconnecting);
  border-color: color-mix(in srgb, var(--ma-status-reconnecting) 40%, transparent);
}

.ma-jtree__val {
  flex: 1 1 auto;
  min-width: 0;
  text-align: right;
  color: var(--ma-text-2);
}

.ma-jtree__trunc {
  padding: 4px 8px;
}
</style>
