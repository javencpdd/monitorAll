/**
 * 卡片状态推导 + 订阅生命周期（ARCH §10.3 / ST-01 / ST-02）。
 *
 * 订阅引用计数：本 composable 负责 subscribe/unsubscribe，
 * 同一 channelId 被多张卡片订阅时 WS 侧引用计数累加，归零才真正退订。
 */
import { computed, onBeforeUnmount, ref, toValue, watch } from 'vue'
import type { MaybeRefOrGetter } from 'vue'
import type { Card, CardVisualStatus } from '@/types'
import { getWsClient } from '@/ws/client'
import { useDatasourceStore } from '@/stores/datasource'
import { useFrameStore } from '@/stores/frame'
import { FIRST_FRAME_WAIT_MS } from '@/utils/constants'

export interface CardStatusInfo {
  status: CardVisualStatus
  text: string
  lastError?: string
  /** 重连倒计时（ms），来自 sourceStatus.reconnectInMs。 */
  reconnectInMs?: number
  nextRetryAt?: number
  /** 是否已收到过帧。 */
  hasFrames: boolean
}

const STATUS_LABEL: Record<CardVisualStatus, string> = {
  unconfigured: '选择数据开始',
  loading: '等待数据…',
  waiting: '未收到数据',
  online: '在线',
  idle: '未连接',
  connecting: '连接中',
  reconnecting: '正在重连',
  error: '错误',
  degraded: '已降级',
  offline: '离线',
  renderError: '渲染出错',
}

export interface UseCardStatusOptions {
  /** 渲染器上报的运行时状态覆盖（如视频降级），优先级最高。 */
  override?: MaybeRefOrGetter<CardVisualStatus | undefined>
}

export function useCardStatus(card: MaybeRefOrGetter<Card | undefined>, options: UseCardStatusOptions = {}) {
  const datasourceStore = useDatasourceStore()
  const frameStore = useFrameStore()
  const ws = getWsClient()

  /** 开始订阅的时间戳（0 = 未订阅），用于首帧超时判定。 */
  const subscribedSince = ref<number>(0)
  /** 心跳计时：驱动 <3s / ≥3s 的骨架屏切换。 */
  const tick = ref<number>(Date.now())
  let ticker = 0

  function startTicker(): void {
    if (ticker !== 0) return
    ticker = window.setInterval(() => {
      tick.value = Date.now()
    }, 500)
  }
  function stopTicker(): void {
    if (ticker !== 0) {
      window.clearInterval(ticker)
      ticker = 0
    }
  }

  const channelId = computed<string | undefined>(() => toValue(card)?.channelId)
  const hasFrames = computed<boolean>(() => (channelId.value ? (frameStore.version[channelId.value] ?? 0) > 0 : false))

  const channel = computed(() => datasourceStore.getChannel(channelId.value))
  const source = computed(() => datasourceStore.getSource(channel.value?.dataSourceId))
  const sourceRuntime = computed(() => datasourceStore.getSourceRuntime(channel.value?.dataSourceId))

  const info = computed<CardStatusInfo>(() => {
    const override = toValue(options.override)
    if (override) {
      return { status: override, text: STATUS_LABEL[override], hasFrames: hasFrames.value }
    }
    const id = channelId.value
    if (!id) return { status: 'unconfigured', text: STATUS_LABEL.unconfigured, hasFrames: false }

    const ch = channel.value
    const srcStatus = sourceRuntime.value?.status ?? source.value?.status ?? 'idle'
    const chStatus = ch?.status ?? 'idle'
    const lastError = ch?.lastError ?? source.value?.lastError ?? sourceRuntime.value?.lastError

    if (srcStatus === 'error' || chStatus === 'error') {
      return {
        status: 'error',
        text: lastError || STATUS_LABEL.error,
        hasFrames: hasFrames.value,
        ...(lastError ? { lastError } : {}),
      }
    }
    if (srcStatus === 'reconnecting' || chStatus === 'reconnecting') {
      return {
        status: 'reconnecting',
        text: STATUS_LABEL.reconnecting,
        hasFrames: hasFrames.value,
        ...(sourceRuntime.value?.reconnectInMs !== undefined
          ? { reconnectInMs: sourceRuntime.value.reconnectInMs }
          : {}),
        ...(sourceRuntime.value?.nextRetryAt !== undefined ? { nextRetryAt: sourceRuntime.value.nextRetryAt } : {}),
        ...(lastError ? { lastError } : {}),
      }
    }
    if (srcStatus === 'offline' || chStatus === 'offline') {
      return { status: 'offline', text: STATUS_LABEL.offline, hasFrames: hasFrames.value, ...(lastError ? { lastError } : {}) }
    }
    if (hasFrames.value) {
      const online = srcStatus === 'online' || srcStatus === 'connecting' || srcStatus === 'idle'
      return {
        status: online ? 'online' : srcStatus,
        text: STATUS_LABEL[online ? 'online' : srcStatus],
        hasFrames: true,
      }
    }
    // 已订阅但未收到首帧
    if (subscribedSince.value === 0) {
      return { status: 'loading', text: STATUS_LABEL.loading, hasFrames: false }
    }
    const waited = tick.value - subscribedSince.value
    if (waited < FIRST_FRAME_WAIT_MS) {
      return { status: 'loading', text: STATUS_LABEL.loading, hasFrames: false }
    }
    return {
      status: 'waiting',
      text: STATUS_LABEL.waiting,
      hasFrames: false,
      ...(lastError ? { lastError } : {}),
    }
  })

  // —— 订阅生命周期 ——
  function subscribe(): void {
    const id = channelId.value
    if (!id) return
    ws.subscribe([id])
    subscribedSince.value = Date.now()
    startTicker()
  }

  function unsubscribe(): void {
    const id = channelId.value
    if (id) ws.unsubscribe([id])
    subscribedSince.value = 0
    stopTicker()
  }

  watch(
    channelId,
    (next, prev) => {
      if (prev) ws.unsubscribe([prev])
      if (next) {
        ws.subscribe([next])
        subscribedSince.value = Date.now()
        startTicker()
      } else {
        subscribedSince.value = 0
        stopTicker()
      }
    },
    { immediate: true },
  )

  onBeforeUnmount(() => {
    const id = channelId.value
    if (id) ws.unsubscribe([id])
    stopTicker()
  })

  return { info, hasFrames, subscribedSince, resubscribe: subscribe, release: unsubscribe }
}
