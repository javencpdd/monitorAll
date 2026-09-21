<script setup lang="ts">
/**
 * 卡片配置抽屉（CD-01/02/03/07/09）：三步向导 —— 选通道 → 选渲染器 → 配置 + 实时预览。
 *
 * 关键点：
 *   - 选通道后自动拉取样例帧（D4），用 payloadType 过滤渲染器并驱动字段点选
 *   - 切换渲染器时保留可复用配置（标题/单位）
 *   - 抽屉内实时预览复用真实渲染器组件（临时 UI id `tmp_preview`，绝不落库）
 */
import { computed, ref, watch } from 'vue'
import type { Card, Frame, PayloadType } from '@/types'
import { getChannelSample } from '@/api/endpoints'
import { AppError } from '@/api/client'
import { useDashboardStore } from '@/stores/dashboard'
import { useDatasourceStore } from '@/stores/datasource'
import { useUiStore } from '@/stores/ui'
import { getWsClient } from '@/ws/client'
import { getManifest, mergeConfig, resolveComponent } from '@/renderers'
import { CARD_DRAWER_WIDTH, PREVIEW_CARD_ID } from '@/utils/constants'
import ChannelSelect from './ChannelSelect.vue'
import RendererSelectList from './RendererSelectList.vue'
import SchemaForm from './SchemaForm.vue'
import EmptyState from '@/components/base/EmptyState.vue'

const dashboardStore = useDashboardStore()
const datasourceStore = useDatasourceStore()
const ui = useUiStore()
const ws = getWsClient()

const open = computed<boolean>(() => ui.cardDrawer.open)
const mode = computed<'create' | 'edit'>(() => ui.cardDrawer.mode)
const editingCard = computed<Card | undefined>(() =>
  ui.cardDrawer.cardId ? dashboardStore.cards.find((c) => c.id === ui.cardDrawer.cardId) : undefined,
)

const step = ref<1 | 2 | 3>(1)
const channelId = ref<string | undefined>(undefined)
const rendererType = ref<string>('json-tree')
const title = ref<string>('')
const unit = ref<string>('')
const showFooter = ref<boolean>(true)
const decimals = ref<number>(2)
const renderConfig = ref<Record<string, unknown>>({})
const sampleFrame = ref<Frame | undefined>(undefined)
const sampleLoading = ref(false)
const sampleError = ref('')
const applying = ref(false)

/** 编辑模式初始化 */
watch(
  () => [open.value, ui.cardDrawer.cardId] as const,
  ([isOpen, cardId]) => {
    if (!isOpen) return
    if (cardId && editingCard.value) {
      const card = editingCard.value
      channelId.value = card.channelId
      rendererType.value = card.rendererType
      title.value = card.title
      unit.value = card.unit ?? ''
      showFooter.value = card.displayOptions.showFooter
      decimals.value = card.displayOptions.decimals
      renderConfig.value = mergeConfig(card.rendererType, card.renderConfig)
      step.value = 3
      void loadSample()
    } else {
      channelId.value = ui.cardDrawer.channelId
      rendererType.value = 'json-tree'
      title.value = ''
      unit.value = ''
      showFooter.value = true
      decimals.value = 2
      renderConfig.value = {}
      sampleFrame.value = undefined
      sampleError.value = ''
      step.value = channelId.value ? 2 : 1
      if (channelId.value) void loadSample()
    }
  },
  { immediate: true },
)

/** 预览需要真实数据：手工订阅该通道（引用计数会自动累加/释放）。 */
watch(
  channelId,
  (next, prev) => {
    if (prev) ws.unsubscribe([prev])
    if (next) ws.subscribe([next])
  },
  { immediate: true },
)

const channel = computed(() => datasourceStore.getChannel(channelId.value))
const payloadType = computed<PayloadType | undefined>(
  () => sampleFrame.value?.payloadType ?? channel.value?.payloadType,
)
const manifest = computed(() => getManifest(rendererType.value))
const previewComponent = computed(() => resolveComponent(rendererType.value))

/** 预览用的临时卡片对象（id 带 tmp_ 前缀，禁止落库）。 */
const previewCard = computed<Card>(() => {
  const layout = dashboardStore.nextFreeLayout(rendererType.value)
  return {
    id: PREVIEW_CARD_ID,
    dashboardId: dashboardStore.current?.id ?? '',
    channelId: channelId.value ?? '',
    rendererType: rendererType.value,
    title: title.value || manifest.value?.displayName || '预览',
    ...(unit.value ? { unit: unit.value } : {}),
    renderConfig: renderConfig.value,
    displayOptions: { showFooter: false, decimals: decimals.value, footerFields: [] },
    layout,
    createdAt: 0,
    updatedAt: 0,
  }
})

async function loadSample(): Promise<void> {
  const id = channelId.value
  if (!id) return
  sampleLoading.value = true
  sampleError.value = ''
  try {
    sampleFrame.value = await getChannelSample(id)
  } catch (err) {
    sampleFrame.value = undefined
    sampleError.value =
      err instanceof AppError && err.code === 50004 ? '3 秒内未收到数据，可先按类型选渲染器' : '样例帧拉取失败'
  } finally {
    sampleLoading.value = false
  }
}

watch(channelId, (id) => {
  sampleFrame.value = undefined
  if (id) void loadSample()
})

/** 切换渲染器：重建配置（保留标题/单位），缺失字段自动补默认。 */
watch(rendererType, (type) => {
  const base = mergeConfig(type, undefined)
  const reusable: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(renderConfig.value)) {
    if (key in base) reusable[key] = value
  }
  renderConfig.value = { ...base, ...reusable }
})

function pickRenderer(type: string): void {
  rendererType.value = type
  step.value = 3
  if (!title.value) title.value = datasourceStore.getChannel(channelId.value)?.name ?? ''
}

async function apply(): Promise<void> {
  if (!channelId.value) return
  applying.value = true
  try {
    const displayOptions = { showFooter: showFooter.value, decimals: decimals.value, footerFields: [] }
    if (mode.value === 'edit' && editingCard.value) {
      const updated = await dashboardStore.patchCard(editingCard.value.id, {
        title: title.value,
        unit: unit.value,
        rendererType: rendererType.value,
        channelId: channelId.value,
        renderConfig: renderConfig.value,
        displayOptions,
      })
      ui.success('卡片已更新')
      void updated
    } else {
      await dashboardStore.addCard({
        channelId: channelId.value,
        rendererType: rendererType.value,
        title: title.value || datasourceStore.getChannel(channelId.value)?.name || '新卡片',
        ...(unit.value ? { unit: unit.value } : {}),
        renderConfig: renderConfig.value,
        displayOptions,
        layout: dashboardStore.nextFreeLayout(rendererType.value),
      })
      ui.success('卡片已添加')
    }
    ui.closeCardDrawer()
  } catch (err) {
    ui.handleError(err, '保存卡片失败')
  } finally {
    applying.value = false
  }
}

function close(): void {
  ui.closeCardDrawer()
}

const hintPayload = computed<unknown>(() => sampleFrame.value?.payload)
const hintMap = computed(() => sampleFrame.value?.schemaHint)
</script>

<template>
  <transition name="ma-drawer-fade">
    <aside v-if="open" class="ma-drawer" :style="{ width: `${CARD_DRAWER_WIDTH}px` }">
      <header class="ma-drawer__head">
        <span class="ma-drawer__title">{{ mode === 'edit' ? '编辑卡片' : '新建卡片' }}</span>
        <button class="ma-drawer__close" title="关闭" @click="close">✕</button>
      </header>

      <div class="ma-drawer__steps">
        <span :class="{ 'is-active': step === 1 }">① 选择通道</span>
        <span :class="{ 'is-active': step === 2 }">② 渲染器</span>
        <span :class="{ 'is-active': step === 3 }">③ 配置</span>
      </div>

      <div class="ma-drawer__body ma-scroll-y">
        <!-- Step1 -->
        <template v-if="step === 1">
          <channel-select v-model="channelId" />
        </template>

        <!-- Step2 -->
        <template v-else-if="step === 2">
          <div class="ma-drawer__sample">
            <span v-if="sampleLoading" class="ma-text-xs ma-text-3">正在拉取样例帧…</span>
            <span v-else-if="sampleError" class="ma-text-xs ma-status-warn">{{ sampleError }}</span>
            <span v-else-if="sampleFrame" class="ma-text-xs ma-text-2">
              已识别数据类型：{{ sampleFrame.payloadType }}（{{ sampleFrame.sizeBytes }} B）
            </span>
            <span v-else class="ma-text-xs ma-text-3">未取到样例帧，将按通道声明类型过滤</span>
            <button class="ma-drawer__link" @click="loadSample">重新拉取</button>
          </div>
          <renderer-select-list v-model="rendererType" :payload-type="payloadType" @update:model-value="pickRenderer" />
        </template>

        <!-- Step3 -->
        <template v-else>
          <div class="ma-drawer__field">
            <label class="ma-drawer__label">标题</label>
            <input v-model="title" class="ma-drawer__input" placeholder="卡片标题" />
          </div>
          <div class="ma-drawer__field">
            <label class="ma-drawer__label">单位</label>
            <input v-model="unit" class="ma-drawer__input" placeholder="如 V / m·s⁻¹" />
          </div>
          <div class="ma-drawer__field">
            <label class="ma-drawer__label">显示脚注</label>
            <button class="ma-drawer__toggle" :class="{ 'is-on': showFooter }" @click="showFooter = !showFooter">
              {{ showFooter ? '开' : '关' }}
            </button>
          </div>
          <div class="ma-drawer__field">
            <label class="ma-drawer__label">小数位</label>
            <input v-model.number="decimals" class="ma-drawer__input" type="number" min="0" max="6" step="1" />
          </div>

          <div class="ma-drawer__sep">渲染器配置（{{ manifest?.displayName ?? rendererType }}）</div>
          <schema-form
            v-model="renderConfig"
            :schema="manifest?.configSchema ?? []"
            :hint="hintMap"
            :sample-payload="hintPayload"
          />

          <div class="ma-drawer__sep">实时预览</div>
          <div class="ma-drawer__preview">
            <component
              :is="previewComponent"
              v-if="previewComponent && channelId"
              :key="`${rendererType}-${channelId}`"
              :card="previewCard"
              :config="renderConfig"
            />
            <empty-state v-else compact icon="config" title="请先选择通道" />
          </div>
        </template>
      </div>

      <footer class="ma-drawer__foot">
        <button class="ma-drawer__btn" :disabled="step === 1" @click="step = step === 3 ? 2 : 1">上一步</button>
        <div class="ma-drawer__grow"></div>
        <button
          v-if="step === 1"
          class="ma-drawer__btn ma-drawer__btn--primary"
          :disabled="!channelId"
          @click="step = 2"
        >
          下一步
        </button>
        <button
          v-else-if="step === 2"
          class="ma-drawer__btn ma-drawer__btn--primary"
          :disabled="!rendererType"
          @click="step = 3"
        >
          下一步
        </button>
        <button v-else class="ma-drawer__btn ma-drawer__btn--primary" :disabled="applying" @click="apply">
          {{ mode === 'edit' ? '应用更改' : '添加到看板' }}
        </button>
      </footer>
    </aside>
  </transition>
</template>

<style scoped>
.ma-drawer {
  position: absolute;
  top: 0;
  right: 0;
  bottom: 0;
  z-index: 30;
  display: flex;
  flex-direction: column;
  background: var(--ma-bg-elevated);
  border-left: 1px solid var(--ma-border);
  box-shadow: var(--ma-shadow-pop);
}

.ma-drawer__head {
  display: flex;
  align-items: center;
  height: 40px;
  padding: 0 12px;
  border-bottom: 1px solid var(--ma-border);
}

.ma-drawer__title {
  flex: 1 1 auto;
  font-size: var(--ma-font-lg);
}

.ma-drawer__close {
  border: none;
  background: none;
  color: var(--ma-text-3);
  cursor: pointer;
}

.ma-drawer__steps {
  display: flex;
  gap: 12px;
  padding: 8px 12px;
  font-size: var(--ma-font-xs);
  color: var(--ma-text-3);
  border-bottom: 1px solid var(--ma-border);
}

.ma-drawer__steps .is-active {
  color: var(--ma-accent);
}

.ma-drawer__body {
  flex: 1 1 auto;
  min-height: 0;
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.ma-drawer__field {
  display: flex;
  align-items: center;
  gap: 8px;
}

.ma-drawer__label {
  flex: none;
  width: 68px;
  font-size: var(--ma-font-sm);
  color: var(--ma-text-2);
}

.ma-drawer__input {
  flex: 1 1 auto;
  min-width: 0;
  height: 26px;
  padding: 0 6px;
  border: 1px solid var(--ma-border);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-base);
  color: var(--ma-text-1);
  font-size: var(--ma-font-sm);
}

.ma-drawer__input:focus {
  outline: none;
  border-color: var(--ma-accent);
}

.ma-drawer__toggle {
  padding: 2px 10px;
  border: 1px solid var(--ma-border-strong);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-base);
  color: var(--ma-text-2);
  cursor: pointer;
  font-size: var(--ma-font-xs);
}

.ma-drawer__toggle.is-on {
  border-color: var(--ma-accent);
  color: var(--ma-accent);
}

.ma-drawer__sep {
  margin-top: 4px;
  padding-top: 8px;
  border-top: 1px solid var(--ma-border);
  font-size: var(--ma-font-xs);
  color: var(--ma-text-3);
}

.ma-drawer__sample {
  display: flex;
  align-items: center;
  gap: 8px;
}

.ma-drawer__link {
  margin-left: auto;
  border: none;
  background: none;
  color: var(--ma-text-2);
  font-size: var(--ma-font-xs);
  cursor: pointer;
}

.ma-drawer__link:hover {
  color: var(--ma-accent);
}

.ma-drawer__preview {
  height: 200px;
  border: 1px dashed var(--ma-border-strong);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-base);
  overflow: hidden;
}

.ma-drawer__foot {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 12px;
  border-top: 1px solid var(--ma-border);
}

.ma-drawer__grow {
  flex: 1 1 auto;
}

.ma-drawer__btn {
  padding: 4px 14px;
  border: 1px solid var(--ma-border-strong);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-base);
  color: var(--ma-text-1);
  cursor: pointer;
  font-size: var(--ma-font-sm);
}

.ma-drawer__btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}

.ma-drawer__btn--primary {
  border-color: var(--ma-accent);
  background: var(--ma-accent-dim);
  color: var(--ma-accent);
}

.ma-status-warn {
  color: var(--ma-status-reconnecting);
}

.ma-drawer-fade-enter-active,
.ma-drawer-fade-leave-active {
  transition: transform 0.18s ease, opacity 0.18s ease;
}

.ma-drawer-fade-enter-from,
.ma-drawer-fade-leave-to {
  transform: translateX(16px);
  opacity: 0;
}
</style>
