<script setup lang="ts">
/**
 * JSONPath 字段点选器（CD-09）：由最近一帧的 schemaHint + payload 生成候选树，
 * 支持点选深层字段并显示当前值预览。
 */
import { computed, ref, watch } from 'vue'
import type { FieldHint } from '@/types'
import { buildFieldTree, getByPath, getNumberByPath } from '@/utils/jsonpath'
import type { FieldNode } from '@/utils/jsonpath'
import { formatValue } from '@/utils/format'
import EmptyState from '@/components/base/EmptyState.vue'

const props = withDefaults(
  defineProps<{
    hint?: Record<string, FieldHint> | undefined
    payload?: unknown
    accept?: 'number' | 'any'
    multiple?: boolean
    max?: number
    modelValue?: string | string[] | undefined
  }>(),
  { hint: undefined, payload: undefined, accept: 'number', multiple: false, max: undefined, modelValue: undefined },
)

const emit = defineEmits<{ 'update:modelValue': [value: string | string[] | undefined] }>()

const expanded = ref<Record<string, boolean>>({})

interface Row {
  node: FieldNode
  depth: number
}

const roots = computed<FieldNode[]>(() => buildFieldTree({ payload: props.payload, schemaHint: props.hint }))

/** 按展开状态拍平成一维行（避免递归组件）。 */
const rows = computed<Row[]>(() => {
  const out: Row[] = []
  const walk = (nodes: FieldNode[], depth: number): void => {
    for (const node of nodes) {
      out.push({ node, depth })
      if (node.children && node.children.length > 0 && expanded.value[node.path]) {
        walk(node.children, depth + 1)
      }
    }
  }
  walk(roots.value, 0)
  return out
})

const selected = computed<string[]>(() => {
  if (Array.isArray(props.modelValue)) return props.modelValue
  if (typeof props.modelValue === 'string' && props.modelValue.length > 0) return [props.modelValue]
  return []
})

function selectable(node: FieldNode): boolean {
  if (props.accept === 'any') return true
  if (node.type === 'number') return true
  if (node.children && node.children.length > 0) return false
  return node.sample !== undefined && typeof getNumericSample(node) === 'number'
}

function getNumericSample(node: FieldNode): number {
  if (typeof node.sample === 'number') return node.sample
  if (props.payload === undefined) return Number.NaN
  return getNumberByPath(props.payload, node.path)
}

function previewOf(node: FieldNode): string {
  if (node.sample !== undefined) return formatValue(node.sample)
  if (props.payload !== undefined) return formatValue(getByPath(props.payload, node.path))
  return '--'
}

function isSelected(path: string): boolean {
  return selected.value.includes(path)
}

function toggleExpand(path: string): void {
  expanded.value[path] = !expanded.value[path]
}

function toggleSelect(node: FieldNode): void {
  if (!selectable(node)) {
    if (node.children && node.children.length > 0) {
      toggleExpand(node.path)
    }
    return
  }
  if (!props.multiple) {
    emit('update:modelValue', isSelected(node.path) ? undefined : node.path)
    return
  }
  const next = selected.value.includes(node.path)
    ? selected.value.filter((p) => p !== node.path)
    : [...selected.value, node.path]
  const limited = props.max && next.length > props.max ? next.slice(next.length - props.max) : next
  emit('update:modelValue', limited)
}

/** 首次拿到样例帧时，默认展开前两层。 */
watch(
  roots,
  (list) => {
    for (const node of list) {
      if (!(node.path in expanded.value) && node.children && node.children.length > 0) {
        expanded.value[node.path] = true
      }
    }
  },
  { immediate: true },
)

function expandAll(): void {
  const walk = (nodes: FieldNode[]): void => {
    for (const node of nodes) {
      if (node.children && node.children.length > 0) {
        expanded.value[node.path] = true
        walk(node.children)
      }
    }
  }
  walk(roots.value)
}

function collapseAll(): void {
  expanded.value = {}
}

const typeTag: Record<FieldNode['type'], string> = {
  number: 'num',
  string: 'str',
  boolean: 'bool',
  object: 'obj',
  array: 'arr',
  null: 'null',
}
</script>

<template>
  <div class="ma-picker">
    <div class="ma-picker__bar">
      <span class="ma-text-xs ma-text-3">已选 {{ selected.length }} 项</span>
      <div class="ma-picker__grow"></div>
      <button class="ma-picker__link" @click="expandAll">展开全部</button>
      <button class="ma-picker__link" @click="collapseAll">折叠</button>
    </div>
    <div class="ma-picker__tree ma-scroll-y">
      <template v-if="rows.length > 0">
        <div
          v-for="row in rows"
          :key="row.node.path"
          class="ma-picker__row"
          :class="{ 'ma-picker__row--disabled': !selectable(row.node) }"
          :style="{ paddingLeft: `${row.depth * 12 + 4}px` }"
          @click="toggleSelect(row.node)"
        >
          <button
            v-if="row.node.children && row.node.children.length > 0"
            class="ma-picker__caret"
            @click.stop="toggleExpand(row.node.path)"
          >
            {{ expanded[row.node.path] ? '▾' : '▸' }}
          </button>
          <span v-else class="ma-picker__caret ma-picker__caret--leaf">·</span>
          <input
            v-if="multiple"
            class="ma-picker__check"
            type="checkbox"
            :checked="isSelected(row.node.path)"
            @click.stop
            @change="toggleSelect(row.node)"
          />
          <span class="ma-picker__key ma-ellipsis" :title="row.node.path">{{ row.node.key }}</span>
          <span class="ma-picker__type ma-text-xs">{{ typeTag[row.node.type] }}</span>
          <span class="ma-picker__val ma-text-xs ma-text-3 ma-ellipsis">{{ previewOf(row.node) }}</span>
        </div>
      </template>
      <empty-state
        v-else
        compact
        icon="waiting"
        title="暂无可点选字段"
        description="先在下方拉取样例帧，或确认数据源正在推送数据"
      />
    </div>
  </div>
</template>

<style scoped>
.ma-picker {
  display: flex;
  flex-direction: column;
  gap: 4px;
  width: 100%;
  min-height: 0;
}

.ma-picker__bar {
  display: flex;
  align-items: center;
  gap: 8px;
}

.ma-picker__grow {
  flex: 1 1 auto;
}

.ma-picker__link {
  border: none;
  background: none;
  color: var(--ma-text-3);
  font-size: var(--ma-font-xs);
  cursor: pointer;
  padding: 0;
}

.ma-picker__link:hover {
  color: var(--ma-accent);
}

.ma-picker__tree {
  max-height: 220px;
  min-height: 60px;
  border: 1px solid var(--ma-border);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-base);
  padding: 3px;
}

.ma-picker__row {
  display: flex;
  align-items: center;
  gap: 4px;
  height: 22px;
  padding-right: 6px;
  border-radius: 4px;
  cursor: pointer;
  font-family: 'JetBrains Mono', monospace;
  font-size: var(--ma-font-xs);
}

.ma-picker__row:hover {
  background: var(--ma-bg-hover);
}

.ma-picker__row--disabled .ma-picker__key {
  color: var(--ma-text-3);
}

.ma-picker__caret {
  width: 12px;
  border: none;
  background: none;
  color: var(--ma-text-3);
  cursor: pointer;
  padding: 0;
  font-size: 9px;
}

.ma-picker__caret--leaf {
  cursor: default;
}

.ma-picker__check {
  width: 12px;
  height: 12px;
  accent-color: var(--ma-accent);
  cursor: pointer;
}

.ma-picker__key {
  flex: 0 1 auto;
  min-width: 40px;
  max-width: 45%;
  color: var(--ma-text-1);
}

.ma-picker__type {
  flex: none;
  padding: 0 3px;
  border: 1px solid var(--ma-border-strong);
  border-radius: 3px;
  color: var(--ma-text-3);
  transform: scale(0.92);
}

.ma-picker__val {
  flex: 1 1 auto;
  text-align: right;
  min-width: 0;
}
</style>
