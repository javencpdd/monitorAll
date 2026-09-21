<script setup lang="ts">
/**
 * 通道选择器（DS-05 / CD-01）：支持「复用已有通道」与「手工新增 ROS topic」。
 * ROS2 rosapi 服务名不一致（决策 D5），因此主路径是手工填 topic；
 * 若后端返回了话题列表则一并展示为可选项。
 */
import { computed, ref } from 'vue'
import { useDatasourceStore } from '@/stores/datasource'
import { useUiStore } from '@/stores/ui'
import { PAYLOAD_TYPE_TEXT } from '@/utils/constants'

const props = withDefaults(defineProps<{ modelValue?: string | undefined }>(), { modelValue: undefined })

const emit = defineEmits<{ 'update:modelValue': [value: string] }>()

const datasourceStore = useDatasourceStore()
const ui = useUiStore()

const keyword = ref('')
const adding = ref(false)
const newTopic = ref('')
const newSourceId = ref<string | undefined>(undefined)
const submitting = ref(false)

const sources = computed(() => datasourceStore.sources)

const grouped = computed(() => {
  const kw = keyword.value.trim().toLowerCase()
  return sources.value
    .map((source) => ({
      source,
      channels: (datasourceStore.channelsBySource[source.id] ?? []).filter(
        (c) => !kw || c.name.toLowerCase().includes(kw),
      ),
    }))
    .filter((item) => item.channels.length > 0)
})

const selectedLabel = computed<string>(() => {
  const ch = datasourceStore.getChannel(props.modelValue)
  if (!ch) return ''
  return `${ch.name} · ${PAYLOAD_TYPE_TEXT[ch.payloadType]}`
})

async function addTopic(): Promise<void> {
  const sourceId = newSourceId.value ?? sources.value.find((s) => s.kind === 'ros')?.id
  if (!sourceId || newTopic.value.trim().length === 0) return
  submitting.value = true
  try {
    const channel = await datasourceStore.addChannel(sourceId, { name: newTopic.value.trim() })
    emit('update:modelValue', channel.id)
    newTopic.value = ''
    adding.value = false
    ui.success('通道已添加')
  } catch (err) {
    ui.handleError(err, '新增通道失败')
  } finally {
    submitting.value = false
  }
}

async function discover(): Promise<void> {
  await Promise.all(
    sources.value.map((s) => datasourceStore.loadChannels(s.id, true).catch(() => undefined)),
  )
  ui.info('已尝试从数据源发现通道')
}

function openWizard(): void {
  ui.openSourceWizard()
}
</script>

<template>
  <div class="ma-chsel">
    <div class="ma-chsel__bar">
      <input v-model="keyword" class="ma-chsel__search" placeholder="搜索通道" />
      <button class="ma-chsel__link" @click="discover">发现通道</button>
      <button class="ma-chsel__link" @click="adding = !adding">{{ adding ? '取消' : '+ 手工新增' }}</button>
    </div>

    <div v-if="adding" class="ma-chsel__new">
      <select v-model="newSourceId" class="ma-chsel__select">
        <option v-for="s in sources" :key="s.id" :value="s.id">{{ s.name }}（{{ s.protocol }}）</option>
      </select>
      <input v-model="newTopic" class="ma-chsel__topic" placeholder="ROS topic，如 /imu/data" />
      <button class="ma-chsel__btn" :disabled="submitting" @click="addTopic">添加</button>
    </div>

    <div class="ma-chsel__list ma-scroll-y">
      <div v-for="item in grouped" :key="item.source.id" class="ma-chsel__src">
        <div class="ma-chsel__src-name ma-text-xs ma-text-3">{{ item.source.name }}</div>
        <button
          v-for="ch in item.channels"
          :key="ch.id"
          class="ma-chsel__item"
          :class="{ 'ma-chsel__item--active': modelValue === ch.id }"
          @click="emit('update:modelValue', ch.id)"
        >
          <span class="ma-chsel__ch-name ma-ellipsis">{{ ch.name }}</span>
          <span class="ma-chsel__ch-type ma-text-xs ma-text-3">{{ PAYLOAD_TYPE_TEXT[ch.payloadType] }}</span>
        </button>
      </div>
      <div v-if="grouped.length === 0" class="ma-chsel__none ma-text-sm ma-text-3">
        还没有通道，点击右上角「+ 手工新增」或
        <button class="ma-chsel__link" @click="openWizard">新建数据源</button>
      </div>
    </div>

    <div v-if="selectedLabel" class="ma-chsel__current ma-text-xs ma-text-2">已选：{{ selectedLabel }}</div>
  </div>
</template>

<style scoped>
.ma-chsel {
  display: flex;
  flex-direction: column;
  gap: 6px;
  width: 100%;
  min-width: 0;
}

.ma-chsel__bar {
  display: flex;
  align-items: center;
  gap: 6px;
}

.ma-chsel__search,
.ma-chsel__select,
.ma-chsel__topic {
  height: 26px;
  padding: 0 6px;
  border: 1px solid var(--ma-border);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-base);
  color: var(--ma-text-1);
  font-size: var(--ma-font-sm);
  min-width: 0;
}

.ma-chsel__search {
  flex: 1 1 auto;
}

.ma-chsel__select {
  flex: 0 1 40%;
}

.ma-chsel__topic {
  flex: 1 1 auto;
}

.ma-chsel__link {
  flex: none;
  border: none;
  background: none;
  color: var(--ma-text-2);
  font-size: var(--ma-font-xs);
  cursor: pointer;
  padding: 0 2px;
}

.ma-chsel__link:hover {
  color: var(--ma-accent);
}

.ma-chsel__new {
  display: flex;
  gap: 4px;
}

.ma-chsel__btn {
  flex: none;
  padding: 0 8px;
  border: 1px solid var(--ma-border-strong);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-elevated);
  color: var(--ma-text-1);
  cursor: pointer;
  font-size: var(--ma-font-xs);
}

.ma-chsel__btn:hover {
  color: var(--ma-accent);
  border-color: var(--ma-accent);
}

.ma-chsel__list {
  max-height: 240px;
  border: 1px solid var(--ma-border);
  border-radius: var(--ma-radius);
  padding: 4px;
  background: var(--ma-bg-base);
}

.ma-chsel__src-name {
  padding: 4px 4px 2px;
}

.ma-chsel__item {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  height: 24px;
  padding: 0 6px;
  border: none;
  border-radius: 4px;
  background: none;
  color: var(--ma-text-1);
  cursor: pointer;
  text-align: left;
}

.ma-chsel__item:hover {
  background: var(--ma-bg-hover);
}

.ma-chsel__item--active {
  background: var(--ma-accent-dim);
  color: var(--ma-accent);
}

.ma-chsel__ch-name {
  flex: 1 1 auto;
  min-width: 0;
  font-size: var(--ma-font-sm);
}

.ma-chsel__ch-type {
  flex: none;
}

.ma-chsel__none {
  padding: 12px 8px;
  text-align: center;
}

.ma-chsel__current {
  color: var(--ma-text-2);
}
</style>
