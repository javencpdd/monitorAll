<script setup lang="ts">
/**
 * 左侧数据源面板（PRD §5.1 / §5.2）：
 * 按数据源类型分组的树形结构 DataSource → Channel，四态角标，可拖拽到画布建档。
 */
import { computed, ref, watch } from 'vue'
import { NButton, NInput, useDialog } from 'naive-ui'
import { useDatasourceStore } from '@/stores/datasource'
import { useUiStore } from '@/stores/ui'
import { KIND_TEXT, PAYLOAD_TYPE_TEXT, SOURCE_PANEL_WIDTH } from '@/utils/constants'
import type { ChannelRuntime, DataSource } from '@/types'
import StatusBadge from './StatusBadge.vue'
import DataSourceDetailDrawer from '@/components/config/DataSourceDetailDrawer.vue'
import { useCardCreate } from '@/composables/useCardCreate'

const datasourceStore = useDatasourceStore()
const { openCreate: openCardCreate } = useCardCreate()
const ui = useUiStore()
const dialog = useDialog()

const props = withDefaults(defineProps<{ readonly?: boolean }>(), { readonly: false })

const keyword = ref('')
const collapsed = ref<Record<string, boolean>>({})
const editing = ref<Record<string, boolean>>({})

/* ——— 整栏折叠（窄屏 / 想给画布腾地方时用）——— */
const PANEL_FOLD_KEY = 'monitorall.sourcePanelCollapsed'
const panelCollapsed = ref<boolean>(
  (() => {
    try {
      return localStorage.getItem(PANEL_FOLD_KEY) === '1'
    } catch {
      return false
    }
  })(),
)

function togglePanel(): void {
  panelCollapsed.value = !panelCollapsed.value
}

watch(panelCollapsed, (v) => {
  try {
    localStorage.setItem(PANEL_FOLD_KEY, v ? '1' : '0')
  } catch {
    /* localStorage 不可用（隐私模式等）时仅退化为不记忆 */
  }
})

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

/**
 * 给某个通道建一张卡片。
 *
 * ⭐ 这行代码直接对应一个真实困惑：「左边的新建数据源和顶栏的新建卡片是不是一样的？」
 * 答案是**不一样**，但以前看不出来：
 *   - 「新建数据源」只建立**连接**（rosbridge 地址 / RTMP 地址 / HTTP 接口），建完画面上不出现任何东西；
 *   - 要把它显示出来，得在这棵树里找到它下面的**通道**再变成卡片；
 *   而"变成卡片"的办法当时只有一个 —— **把它拖到画布上**，
 *   且唯一的说明藏在 `title` 里（`拖拽到画布建档`），几乎不可能被发现。
 * 于是用户只能回顶栏点「新建卡片」，两个入口在观感上就重合了。
 *
 * 这里给每个通道一个**显式可见**的「＋」，与拖拽等价，但不需要先发现那个隐藏手势。
 */
function addCardFor(channel: ChannelRuntime): void {
  openCardCreate(channel.id)
}

async function removeChannel(sourceId: string, channel: ChannelRuntime): Promise<void> {
  try {
    await datasourceStore.removeChannel(sourceId, channel.id)
    ui.success('通道已删除')
  } catch (err) {
    ui.handleError(err, '删除失败')
  }
}

/**
 * 重新拉取数据源列表（「加载失败」态的重试出口）。
 *
 * 没有它时，用户遇到加载失败只能刷新整页；而失败原因（后端未就绪 / 网络抖动）
 * 往往是可恢复的。失败仍走 `ui.handleError`，保证用户再次看到原因。
 */
function retryLoadSources(): void {
  datasourceStore.loadSources().catch((err) => {
    ui.handleError(err, '加载数据源列表失败')
  })
}

/* ——————————————— 只读详情抽屉 ———————————————
 * 详情抽屉的开关用**组件局部 ref** 管理（与 SettingsDrawer 在 TopBar 里的做法一致），
 * 不塞进 ui store —— 它是左侧面板的内部交互，不是全局状态。
 */
const detailSourceId = ref<string | undefined>(undefined)

function openDetail(source: DataSource): void {
  detailSourceId.value = source.id
}

/**
 * 详情抽屉里点了「删除」→ 复用已有的 `confirmDeleteSource`（同一套确认弹窗与 API 调用），
 * 先关抽屉再走确认，避免抽屉开着而数据源已被删的视觉不一致。
 */
function onDetailDelete(source: DataSource): void {
  detailSourceId.value = undefined
  confirmDeleteSource(source)
}
</script>

<template>
  <aside
    class="ma-panel"
    :class="{ 'ma-panel--collapsed': panelCollapsed }"
    :style="panelCollapsed ? undefined : { width: `${SOURCE_PANEL_WIDTH}px` }"
  >
    <!-- 折叠态：只留一个展开按钮的细条 -->
    <button
      v-if="panelCollapsed"
      class="ma-panel__fold"
      title="展开数据源面板"
      @click="togglePanel"
    >»</button>

    <template v-else>
      <div class="ma-panel__head">
        <n-input v-model:value="keyword" size="small" placeholder="搜索数据源 / 通道" clearable />
        <button class="ma-panel__fold" title="收起数据源面板" @click="togglePanel">«</button>
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
              class="ma-panel__icon ma-panel__detail"
              title="查看配置详情（只读）"
              @click.stop="openDetail(item.source)"
            >
              ⓘ
            </button>
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
                class="ma-panel__icon ma-panel__add"
                title="把这个通道画成一张卡片（也可以直接拖到画布上）"
                @click.stop="addCardFor(channel)"
              >
                ＋
              </button>
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

      <!--
        空列表三态：**绝不能撒谎**。
        「加载失败」和「确实没有数据源」在观感上以前都是同一句"暂无数据源"，
        于是用户以为库里真的没有、就去重复新建（库里 18 个同名重复源就是这么来的）。
      -->
      <div v-if="groups.length === 0" class="ma-panel__none ma-text-sm ma-text-3">
        <span v-if="datasourceStore.loading">加载中…</span>
        <template v-else-if="datasourceStore.error">
          <p class="ma-panel__none-err">加载数据源失败：{{ datasourceStore.error }}</p>
          <n-button size="tiny" secondary :loading="datasourceStore.loading" @click="retryLoadSources">
            重试
          </n-button>
        </template>
        <span v-else-if="keyword.trim()">无匹配的数据源</span>
        <span v-else>暂无数据源，点击下方按钮新建</span>
      </div>
    </div>

    <div class="ma-panel__foot">
      <!--
        三层关系的图例：直接回答「新建数据源和新建卡片是不是一样的」。
        没有这句说明时，用户只能靠猜 —— 而这三层在界面上都叫「新建」。
      -->
      <p class="ma-panel__legend ma-text-xs ma-text-3">
        数据源＝<b>连接</b>（ROS / RTMP / HTTP 地址）<br />
        通道＝其中<b>一路数据</b> · 卡片＝把它<b>画出来</b>
      </p>
      <n-button size="small" block secondary title="只建立连接，画面上不会直接出现东西；建好后给它的通道点「＋」即可显示" @click="ui.openSourceWizard()">
        + 新建数据源
      </n-button>
    </div>
    </template>

    <!-- 只读详情抽屉（自身 fixed 定位，挂在面板内即可，不参与面板布局） -->
    <data-source-detail-drawer
      :source-id="detailSourceId"
      @close="detailSourceId = undefined"
      @delete="onDetailDelete"
    />
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
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 8px;
  border-bottom: 1px solid var(--ma-border);
}

.ma-panel__head :deep(.n-input) {
  flex: 1 1 auto;
  min-width: 0;
}

/* 折叠/展开按钮：展开时在搜索框右侧，折叠时独占整条细栏 */
.ma-panel__fold {
  flex: none;
  width: 22px;
  height: 22px;
  padding: 0;
  border: 1px solid var(--ma-border);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-base);
  color: var(--ma-text-3);
  cursor: pointer;
  font-size: 12px;
  line-height: 1;
}

.ma-panel__fold:hover {
  color: var(--ma-accent);
  border-color: var(--ma-accent);
}

/* 折叠态：30px 细条，只显示展开按钮 */
.ma-panel--collapsed {
  width: 30px;
  align-items: center;
  padding-top: 8px;
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

/* 「＋建档」与「✕删除」同在一行，用主色区分，避免一眼看成同一个删除动作 */
.ma-panel__add {
  color: var(--ma-accent);
  font-size: 13px;
  line-height: 1;
}

.ma-panel__add:hover {
  filter: brightness(1.2);
}

.ma-panel__icon:hover {
  color: var(--ma-status-error);
}

/*
 * 详情入口：**常显**（不是 hover 才出现）。
 * "看不到已建数据源的配置"正是本次要解决的问题，入口若藏在 hover 里，
 * 可发现性等于零。放在 `.ma-panel__icon:hover` 之后以覆盖其红色 hover（同特异性、后者生效）。
 */
.ma-panel__detail {
  opacity: 1;
  color: var(--ma-text-2);
}

.ma-panel__detail:hover {
  color: var(--ma-accent);
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

.ma-panel__legend {
  margin: 0 0 6px;
  line-height: 1.6;
}

.ma-panel__legend b {
  color: var(--ma-text-2);
  font-weight: 600;
}
</style>
