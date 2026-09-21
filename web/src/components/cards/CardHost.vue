<script setup lang="ts">
/**
 * 卡片宿主（T-21）：订阅管理 → 状态推导 → 三态切换 → 渲染器分发。
 *
 * 三态：骨架屏（等待首帧 <3s）/ 等待提示（≥3s 无数据）/ 重连遮罩（CardFrame 负责）。
 * 渲染器由 manifest.component 懒加载并包在 ErrorBoundary 内，单卡异常不影响整页。
 */
import { computed, ref } from 'vue'
import type { Card, CardVisualStatus } from '@/types'
import { getManifest, maxPointsOf, mergeConfig, resolveComponent } from '@/renderers'
import { DEFAULT_RING_CAPACITY } from '@/utils/constants'
import { useCardStatus } from '@/composables/useCardStatus'
import { useFrameStore } from '@/stores/frame'
import { useDashboardStore } from '@/stores/dashboard'
import { useDatasourceStore } from '@/stores/datasource'
import { useUiStore } from '@/stores/ui'
import { useDialog } from 'naive-ui'
import CardFrame from '@/components/layout/CardFrame.vue'
import CardErrorBoundary from './CardErrorBoundary.vue'
import CardSkeleton from './CardSkeleton.vue'
import EmptyState from '@/components/base/EmptyState.vue'

const props = defineProps<{ card: Card }>()

const frameStore = useFrameStore()
const dashboardStore = useDashboardStore()
const datasourceStore = useDatasourceStore()
const ui = useUiStore()
const dialog = useDialog()

/** 渲染器可上报的运行时状态（如视频降级），优先级高于自动推导。 */
const runtimeStatus = ref<CardVisualStatus | undefined>(undefined)
const { info, hasFrames } = useCardStatus(() => props.card, { override: () => runtimeStatus.value })

const editMode = computed<boolean>(() => dashboardStore.editMode)
const channel = computed(() => datasourceStore.getChannel(props.card.channelId))
const sourceLabel = computed<string>(() => {
  const source = datasourceStore.getSource(channel.value?.dataSourceId)
  return source ? `${source.name}${channel.value?.name ?? ''}` : ''
})
const manifest = computed(() => getManifest(props.card.rendererType))
const config = computed<Record<string, unknown>>(() => mergeConfig(props.card.rendererType, props.card.renderConfig))
const rendererComponent = computed(() => resolveComponent(props.card.rendererType))
const neededPoints = computed<number>(() => maxPointsOf(props.card.rendererType, DEFAULT_RING_CAPACITY))
const stats = computed(() => frameStore.getStats(props.card.channelId))

/** 历史型渲染器订阅前先确保缓冲容量。 */
if (neededPoints.value > 0) {
  frameStore.ensureBuffer(props.card.channelId, neededPoints.value)
}

function onEdit(): void {
  ui.openCardDrawer(props.card.id, 'edit')
}

function confirmRemove(): void {
  dialog.warning({
    title: '删除卡片',
    content: `确定删除卡片「${props.card.title}」？`,
    positiveText: '删除',
    negativeText: '取消',
    onPositiveClick: async () => {
      try {
        await dashboardStore.removeCard(props.card.id)
        ui.success('卡片已删除')
      } catch (err) {
        ui.handleError(err, '删除失败')
      }
    },
  })
}
</script>

<template>
  <card-frame
    :card="card"
    :status="info"
    :stats="stats"
    :source-label="sourceLabel"
    :edit-mode="editMode"
    @edit="onEdit"
    @remove="confirmRemove"
  >
    <card-error-boundary>
      <!-- 三态：未收到首帧 -->
      <card-skeleton v-if="info.status === 'loading'" />
      <empty-state
        v-else-if="info.status === 'waiting'"
        icon="waiting"
        title="未收到数据"
        description="请检查话题名是否正确、数据源是否在线"
      />
      <empty-state
        v-else-if="info.status === 'unconfigured'"
        icon="config"
        title="选择数据开始"
        description="点击右上角齿轮，为该卡片选择数据源通道"
      >
        <template #action>
          <button class="ma-host__btn" @click="onEdit">配置卡片</button>
        </template>
      </empty-state>
      <!-- 正常渲染：渲染器分发 -->
      <component
        :is="rendererComponent"
        v-else-if="rendererComponent && hasFrames"
        :card="card"
        :config="config"
        @status="(s: CardVisualStatus) => (runtimeStatus = s)"
      />
      <empty-state
        v-else
        icon="empty"
        :title="manifest ? '等待数据…' : '渲染器缺失'"
        :description="manifest ? '' : `未注册的渲染器类型：${card.rendererType}`"
      />
    </card-error-boundary>
  </card-frame>
</template>

<style scoped>
.ma-host__btn {
  padding: 4px 12px;
  border: 1px solid var(--ma-border-strong);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-elevated);
  color: var(--ma-text-1);
  cursor: pointer;
  font-size: var(--ma-font-sm);
}

.ma-host__btn:hover {
  border-color: var(--ma-accent);
  color: var(--ma-accent);
}
</style>
