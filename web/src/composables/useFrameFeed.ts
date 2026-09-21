/**
 * 把某个通道的帧数据流接到组件上（ARCH §10.2 消费者端）。
 *
 * 组件侧只做两件事：
 *   1. watch version（唯一响应式触发器）
 *   2. 需要历史时命令式 getBuffer()，不放进响应式
 */
import type { ComputedRef, MaybeRefOrGetter, Ref } from 'vue'
import { computed, onBeforeUnmount, shallowRef, toValue, watch } from 'vue'
import type { Frame } from '@/types'
import type { RingBuffer } from '@/utils/ringbuffer'
import type { ChannelStats } from '@/stores/frame'
import { useFrameStore } from '@/stores/frame'
import { DEFAULT_RING_CAPACITY } from '@/utils/constants'

export interface FrameFeedOptions {
  /** 期望的环形缓冲容量（取「已有容量」与「期望容量」的较大值）。 */
  capacity?: MaybeRefOrGetter<number | undefined>
}

export interface FrameFeed {
  /** 最近一帧（随 version 变化更新）。 */
  latest: Ref<Frame | undefined>
  /** 帧版本号：唯一响应式触发源。 */
  version: ComputedRef<number>
  /** 命令式读取历史缓冲（折线图/地图专用）。 */
  getBuffer: () => RingBuffer<Frame> | undefined
  /** 通道运行统计。 */
  stats: ComputedRef<ChannelStats | undefined>
  /** 清空该通道历史。 */
  clear: () => void
}

export function useFrameFeed(channelId: MaybeRefOrGetter<string | undefined>, options: FrameFeedOptions = {}): FrameFeed {
  const frameStore = useFrameStore()
  const latest = shallowRef<Frame | undefined>(toValue(channelId) ? frameStore.getLatest(toValue(channelId)) : undefined)

  const version = computed<number>(() => {
    const id = toValue(channelId)
    if (!id) return 0
    return frameStore.version[id] ?? 0
  })

  const stats = computed<ChannelStats | undefined>(() => frameStore.getStats(toValue(channelId)))

  const getBuffer = (): RingBuffer<Frame> | undefined => {
    const id = toValue(channelId)
    if (!id) return undefined
    const capacity = toValue(options.capacity)
    return frameStore.ensureBuffer(id, Math.max(DEFAULT_RING_CAPACITY, capacity ?? 0))
  }

  const clear = (): void => {
    const id = toValue(channelId)
    if (id) frameStore.clearChannel(id)
  }

  const stop = watch(
    version,
    () => {
      latest.value = frameStore.getLatest(toValue(channelId))
    },
    { flush: 'sync' },
  )

  watch(
    () => toValue(channelId),
    (id) => {
      latest.value = id ? frameStore.getLatest(id) : undefined
      if (id) getBuffer()
    },
    { immediate: true },
  )

  onBeforeUnmount(stop)

  return { latest, version, getBuffer, stats, clear }
}
