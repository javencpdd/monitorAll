<script setup lang="ts">
/**
 * 卡片外壳（PRD §5.3 + T-21）：
 * 头部 32px（状态角标 / 标题 / 单位 / 操作图标）、渲染区撑满、脚注 24px（可开关）。
 * 编辑态：虚线外框 + 操作图标；查看态：隐藏装饰。
 * 重连遮罩：灰化 + 「连接已断开，正在重连（Ns）」。
 */
import { computed } from 'vue'
import type { Card } from '@/types'
import type { CardStatusInfo } from '@/composables/useCardStatus'
import { CARD_FOOTER_H, CARD_HEADER_H, DEFAULT_DECIMALS, DEFAULT_FOOTER_FIELDS } from '@/utils/constants'
import { formatClock, formatDuration, formatHz } from '@/utils/format'
import type { ChannelStats } from '@/stores/frame'
import StatusBadge from './StatusBadge.vue'

const props = withDefaults(
  defineProps<{
    card: Card
    status: CardStatusInfo
    stats?: ChannelStats | undefined
    sourceLabel?: string
    editMode?: boolean
    /** 渲染区默认由 CardFrame 负责遮罩；部分渲染器（视频）可自行处理。 */
    showMask?: boolean
  }>(),
  { stats: undefined, sourceLabel: '', editMode: false, showMask: true },
)

const emit = defineEmits<{ edit: []; remove: [] }>()

const footerEnabled = computed<boolean>(() => props.card.displayOptions?.showFooter !== false)
const footerFields = computed<string[]>(() => props.card.displayOptions?.footerFields ?? DEFAULT_FOOTER_FIELDS)
const decimals = computed<number>(() => props.card.displayOptions?.decimals ?? DEFAULT_DECIMALS)

const footerParts = computed<{ label: string; value: string }[]>(() => {
  const parts: { label: string; value: string }[] = []
  for (const field of footerFields.value) {
    switch (field) {
      case 'lastUpdate':
        parts.push({ label: '最后更新', value: formatClock(props.stats?.lastFrameAt) })
        break
      case 'rate':
        parts.push({ label: '更新', value: formatHz(props.stats?.hz) })
        break
      case 'latency':
        parts.push({ label: '端到端', value: formatDuration(props.stats?.latencyMs) })
        break
      case 'source':
        parts.push({ label: '源', value: props.sourceLabel })
        break
      default:
        break
    }
  }
  if ((props.stats?.dropped ?? 0) > 0) {
    parts.push({ label: '丢帧', value: String(props.stats?.dropped ?? 0) })
  }
  return parts
})

const showReconnectMask = computed<boolean>(
  () => props.showMask && (props.status.status === 'reconnecting' || props.status.status === 'offline'),
)

const maskText = computed<string>(() => {
  const sec = props.status.reconnectInMs ? Math.max(1, Math.ceil(props.status.reconnectInMs / 1000)) : 0
  if (props.status.status === 'offline') return '数据源离线'
  return sec > 0 ? `连接已断开，正在重连（${sec}s）` : '连接已断开，正在重连…'
})
</script>

<template>
  <section class="ma-card" :class="{ 'ma-card--editing': editMode }">
    <header class="ma-card__head" :style="{ height: `${CARD_HEADER_H}px` }">
      <status-badge
        :status="status.status"
        :text="status.text"
        :tip="status.lastError"
        :reconnect-in-ms="status.reconnectInMs"
        size="small"
      />
      <span class="ma-card__title ma-ellipsis" :title="card.title">{{ card.title }}</span>
      <span v-if="card.unit" class="ma-card__unit ma-text-xs ma-text-3">{{ card.unit }}</span>
      <div class="ma-card__spacer"></div>
      <slot name="tools"></slot>
      <template v-if="editMode">
        <button class="ma-card__icon" title="配置" @click="emit('edit')">⚙</button>
        <button class="ma-card__icon ma-card__icon--danger" title="删除" @click="emit('remove')">✕</button>
      </template>
    </header>

    <div class="ma-card__body">
      <slot></slot>
      <div v-if="showReconnectMask" class="ma-card__mask">
        <div class="ma-card__mask-text ma-text-sm">{{ maskText }}</div>
      </div>
    </div>

    <footer
      v-if="footerEnabled"
      class="ma-card__footer ma-text-xs ma-text-3"
      :style="{ height: `${CARD_FOOTER_H}px` }"
    >
      <span v-for="part in footerParts" :key="part.label" class="ma-card__foot-item">
        {{ part.label }} {{ part.value }}
      </span>
      <div class="ma-card__foot-extra">
        <slot name="footer"></slot>
      </div>
      <span class="ma-card__decimals ma-text-xs">小数位 {{ decimals }}</span>
    </footer>
  </section>
</template>

<style scoped>
.ma-card {
  display: flex;
  flex-direction: column;
  width: 100%;
  height: 100%;
  min-height: 0;
  overflow: hidden;
  background: var(--ma-bg-card);
  border: 1px solid var(--ma-border);
  border-radius: var(--ma-radius);
  box-shadow: var(--ma-shadow-card);
}

.ma-card__head {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 0 6px 0 8px;
  flex: none;
  border-bottom: 1px solid var(--ma-border);
  background: color-mix(in srgb, var(--ma-bg-elevated) 60%, transparent);
}

.ma-card__title {
  flex: 0 1 auto;
  min-width: 0;
  font-size: var(--ma-font-sm);
  color: var(--ma-text-1);
}

.ma-card__unit {
  flex: none;
}

.ma-card__spacer {
  flex: 1 1 auto;
}

.ma-card__icon {
  flex: none;
  border: none;
  background: none;
  color: var(--ma-text-3);
  cursor: pointer;
  padding: 0 3px;
  line-height: 1;
}

.ma-card__icon:hover {
  color: var(--ma-accent);
}

.ma-card__icon--danger:hover {
  color: var(--ma-status-error);
}

.ma-card__body {
  position: relative;
  flex: 1 1 auto;
  min-height: 0;
  overflow: hidden;
}

.ma-card__mask {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  background: color-mix(in srgb, var(--ma-bg-base) 62%, transparent);
  backdrop-filter: grayscale(0.6);
}

.ma-card__mask-text {
  padding: 4px 10px;
  border: 1px solid var(--ma-border-strong);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-elevated);
  color: var(--ma-text-2);
}

.ma-card__footer {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 0 8px;
  flex: none;
  border-top: 1px solid var(--ma-border);
  white-space: nowrap;
  overflow: hidden;
}

.ma-card__foot-item {
  flex: none;
}

.ma-card__foot-extra {
  flex: 1 1 auto;
  min-width: 0;
  text-align: right;
  overflow: hidden;
}

.ma-card__decimals {
  flex: none;
  opacity: 0.5;
}
</style>
