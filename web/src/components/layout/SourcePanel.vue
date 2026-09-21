<script setup lang="ts">
/**
 * 左侧数据源面板（PRD §5.1 / §5.2）：
 * 按数据源类型分组的树形结构 DataSource → Channel，四态角标，可拖拽到画布建档。
 */
import { computed, ref } from 'vue'
import { NButton, NInput, useDialog } from 'naive-ui'
import { useDatasourceStore } from '@/stores/datasource'
import { useUiStore } from '@/stores/ui'
import { KIND_TEXT, PAYLOAD_TYPE_TEXT, SOURCE_PANEL_WIDTH } from '@/utils/constants'
import type { ChannelRuntime, DataSource } from '@/types'
import StatusBadge from './StatusBadge.vue'

const datasourceStore = useDatasourceStore()
const ui = useUiStore()
const dialog = useDialog()

const props = withDefaults(defineProps<{ readonly?: boolean }>(), { readonly: false })

const keyword = ref('')
const collapsed = ref<Record<string, boolean>>({})
const editing = ref<Record<string, boolean>>({})

function toggle(key: string): void {
  collapsed.value[key] = !collapsed.value[key]
}

function isCollapsed(key: string): boolean {
  return collapsed.value[key] === true
}

function toggleChannel(sourceId: string): void {
  editing.value[sourceId] = !editing.value[sourceId]
}

const groups = computed(() => {
  const kw = keyword.value.trim().toLowerCase()
  const sourceList: DataSource[] = datasourceStore.sources
  return (['video', 'ros', 'http'] as const)
    .map((kind) => {
      const sources = sourceList
        .filter((s) => s.kind === kind)
        .filter((s) => !kw || s.name.toLowerCase().includes(kw) || s.protocol.toLowerCase().includes(kw))
        .map((source) => {
          const channels = (datasourceStore.channelsBySource[source.id] ?? []).filter(
            (c) => !kw || c.name.toLowerCase().includes(kw),
          )
          return { source, channels }
        })
        .filter((item) => item.channels.length > 0 || item.source.name.toLowerCase().includes(kw))
      return { kind, sources }
    })
    .filter((g) => g.sources.length > 0)
})

function sourceStatusOf(source: DataSource) {
  return datasourceStore.getSourceRuntime(source.id)?.status ?? source.status
}

function onDragStart(e: DragEvent, channel: ChannelRuntime): void {
  if (!e.dataTransfer) return
  e.dataTransfer.setData('text/plain', channel.id)
  e.dataTransfer.effectAllowed = 'copy'
}

function confirmDeleteSource(source: DataSource): void {
  dialog.warning({
    title: '删除数据源',
    content: `确定删除「${source.name}」？其下通道与引用它们的卡片都会被清理。`,
    positiveText: '删除',
    negativeText: '取消',
    onPositiveClick: async () => {
      try {
        await datasourceStore.removeSource(source.id)
        ui.success('数据源已删除')
      } catch (err) {
        ui.handleError(err, '删除失败')
      }
    },
  })
}

async function removeChannel(sourceId: string, channel: ChannelRuntime): Promise<void> {
  try {
    await datasourceStore.removeChannel(sourceId, channel.id)
    ui.success('通道已删除')
  } catch (err) {
    ui.handleError(err, '删除失败')
  }
}
</script>

<template>
  <aside class="ma-panel" :style="{ width: `${SOURCE_PANEL_WIDTH}px` }">
    <div class="ma-panel__head">
      <n-input v-model:value="keyword" size="small" placeholder="搜索数据源 / 通道" clearable />
    </div>

    <div class="ma-panel__body ma-scroll-y">
      <div v-for="group in groups" :key="group.kind" class="ma-panel__group">
        <div class="ma-panel__kind">{{ KIND_TEXT[group.kind] }}</div>
        <div v-for="item in group.sources" :key="item.source.id" class="ma-panel__source">
          <div class="ma-panel__source-row">
            <button class="ma-panel__caret" @click="toggle(item.source.id)">
              {{ isCollapsed(item.source.id) ? '▸' : '▾' }}
            </button>
            <status-badge :status="sourceStatusOf(item.source)" size="small" :tip="item.source.lastError" />
            <span class="ma-panel__source-name ma-ellipsis" :title="item.source.name">{{ item.source.name }}</span>
            <span class="ma-panel__proto ma-text-xs ma-text-3">{{ item.source.protocol }}</span>
            <button
              v-if="!props.readonly"
              class="ma-panel__icon"
              title="删除数据源"
              @click="confirmDeleteSource(item.source)"
            >
              ✕
            </button>
          </div>
          <div v-if="!isCollapsed(item.source.id)" class="ma-panel__channels">
            <div
              v-for="channel in item.channels"
              :key="channel.id"
              class="ma-panel__channel"
              draggable="true"
              :title="`${channel.name} · ${PAYLOAD_TYPE_TEXT[channel.payloadType]}｜拖拽到画布建档`"
              @dragstart="onDragStart($event, channel)"
            >
              <button class="ma-panel__caret ma-panel__caret--leaf">·</button>
              <span class="ma-panel__ch-name ma-ellipsis">{{ channel.name }}</span>
              <span class="ma-panel__ch-type ma-text-xs ma-text-3">{{ PAYLOAD_TYPE_TEXT[channel.payloadType] }}</span>
              <button
                v-if="editing[item.source.id]"
                class="ma-panel__icon"
                title="删除通道"
                @click="removeChannel(item.source.id, channel)"
              >
                ✕
              </button>
            </div>
            <div class="ma-panel__ch-actions">
              <button class="ma-panel__link" @click="toggleChannel(item.source.id)">
                {{ editing[item.source.id] ? '完成' : '管理通道' }}
              </button>
            </div>
            <div v-if="item.channels.length === 0" class="ma-panel__empty ma-text-xs ma-text-3">暂无通道</div>
          </div>
        </div>
      </div>

      <div v-if="groups.length === 0" class="ma-panel__none ma-text-sm ma-text-3">
        暂无数据源，点击下方按钮新建
      </div>
    </div>

    <div class="ma-panel__foot">
      <n-button size="small" block secondary @click="ui.openSourceWizard()">+ 新建数据源</n-button>
    </div>
  </aside>
</template>

<style scoped>
.ma-panel {
  display: flex;
  flex-direction: column;
  flex: none;
  background: var(--ma-bg-card);
  border-right: 1px solid var(--ma-border);
  min-height: 0;
}

.ma-panel__head {
  padding: 8px;
  border-bottom: 1px solid var(--ma-border);
}

.ma-panel__body {
  flex: 1 1 auto;
  padding: 6px 4px 12px;
  min-height: 0;
}

.ma-panel__group {
  margin-bottom: 8px;
}

.ma-panel__kind {
  padding: 4px 8px;
  font-size: var(--ma-font-xs);
  color: var(--ma-text-3);
  letter-spacing: 0.6px;
}

.ma-panel__source-row,
.ma-panel__channel {
  display: flex;
  align-items: center;
  gap: 4px;
  height: 26px;
  padding: 0 6px;
  border-radius: var(--ma-radius);
  cursor: default;
}

.ma-panel__channel {
  padding-left: 18px;
  cursor: grab;
}

.ma-panel__source-row:hover,
.ma-panel__channel:hover {
  background: var(--ma-bg-hover);
}

.ma-panel__caret {
  width: 14px;
  border: none;
  background: none;
  color: var(--ma-text-3);
  cursor: pointer;
  padding: 0;
  font-size: 10px;
}

.ma-panel__caret--leaf {
  cursor: default;
}

.ma-panel__source-name,
.ma-panel__ch-name {
  flex: 1 1 auto;
  min-width: 0;
  font-size: var(--ma-font-sm);
  color: var(--ma-text-1);
}

.ma-panel__proto,
.ma-panel__ch-type {
  flex: none;
}

.ma-panel__icon {
  flex: none;
  border: none;
  background: none;
  color: var(--ma-text-3);
  cursor: pointer;
  padding: 0 2px;
  opacity: 0;
}

.ma-panel__source-row:hover .ma-panel__icon,
.ma-panel__channel:hover .ma-panel__icon {
  opacity: 1;
}

.ma-panel__icon:hover {
  color: var(--ma-status-error);
}

.ma-panel__ch-actions {
  padding-left: 18px;
}

.ma-panel__link {
  border: none;
  background: none;
  color: var(--ma-text-3);
  font-size: var(--ma-font-xs);
  cursor: pointer;
  padding: 2px 0;
}

.ma-panel__link:hover {
  color: var(--ma-accent);
}

.ma-panel__empty {
  padding: 2px 0 2px 26px;
}

.ma-panel__none {
  padding: 16px 12px;
  text-align: center;
}

.ma-panel__foot {
  padding: 8px;
  border-top: 1px solid var(--ma-border);
}
</style>
