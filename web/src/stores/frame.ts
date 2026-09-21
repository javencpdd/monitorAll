/**
 * 帧数据主干 Store（ARCH §10.1 / §10.2）。
 *
 * 🚨 性能铁律：
 *   F1 `buffers` 与 `latest` 用 markRaw 包成普通 Map，**绝不能**放进 reactive/ref，
 *      否则每帧触发深层代理 → CPU 爆炸。
 *   F2 `version[channelId]++` 是唯一的响应式触发器；组件用 watch 监听它。
 *   F3 组件卸载不清理 buffer（重挂载可续接）；仅在切换看板时用 prune() 清理未引用的 buffer。
 *   F4 单卡片缓冲上限 = manifest.capabilities.maxPoints（默认 1200）。
 *   F6 渲染器通过 getBuffer(channelId) **命令式**读取历史，不走响应式。
 */
import { defineStore } from 'pinia'
import { computed, markRaw, ref } from 'vue'
import type { Frame, PayloadType, Status } from '@/types'
import { getWsClient } from '@/ws/client'
import { DEFAULT_RING_CAPACITY, STATS_FLUSH_INTERVAL_MS } from '@/utils/constants'
import { RingBuffer } from '@/utils/ringbuffer'
import { scheduleBatch } from '@/composables/useRafBatch'

/** 通道运行统计（低频更新，约 1Hz）。 */
export interface ChannelStats {
  lastFrameAt: number
  /** ingestedTs - publishedTs 的滑动均值。 */
  latencyMs: number
  /** 实测到达帧率。 */
  hz: number
  /** 累计丢帧（来自 backpressure 消息）。 */
  dropped: number
  received: number
  payloadType: PayloadType
  lastSizeBytes: number
  status: Status
  lastError?: string
}

interface RateWindow {
  startTs: number
  count: number
}

export const useFrameStore = defineStore('frame', () => {
  /** ① 环形缓冲：定容历史，非响应式。 */
  const buffers = markRaw(new Map<string, RingBuffer<Frame>>())
  /** ② 最新帧：非响应式，配合 version 使用。 */
  const latest = markRaw(new Map<string, Frame>())

  /** 响应式触发器：每次真正把最新帧推给 UI 时自增。 */
  const version = ref<Record<string, number>>({})
  /** 响应式触发器：每次写入缓冲（含历史）时自增；历史型渲染器监听它。 */
  const stats = ref<Record<string, ChannelStats>>({})

  // —— 非响应式的内部计数器 ——
  const pending = new Map<string, Frame>()
  const rateWindows = new Map<string, RateWindow>()
  const capacities = new Map<string, number>()

  /** WS 注册句柄（store 只ref计数一次） */
  let disposeHandlers: (() => void) | null = null

  const ws = getWsClient()

  /** 已缓冲的通道数（调试/自检用）。 */
  const bufferedChannels = computed<number>(() => buffers.size)

  function registerHandlers(): void {
    if (disposeHandlers) return
    disposeHandlers = ws.addHandlers({
      frames: (msg) => onFrames(msg.items),
      backpressure: (msg) => onBackpressure(msg.channelId, msg.dropped),
      channelStatus: (msg) => onChannelStatus(msg.channelId, msg.status, msg.lastError, msg.latencyMs),
    })
  }

  /** 收到一批帧：写缓冲 + 同通道保留最后一帧 + rAF 合并刷新。 */
  function onFrames(items: readonly Frame[]): void {
    if (items.length === 0) return
    for (const frame of items) {
      ensureBuffer(frame.channelId)
      buffers.get(frame.channelId)?.push(frame)
      latest.set(frame.channelId, frame)
      pending.set(frame.channelId, frame)
      accumulateRate(frame.channelId, frame.ingestedTs)
      // 每个通道每帧最多一次响应式刷新（第二层防护）
      scheduleBatch(frame.channelId, () => {
        const curv = version.value[frame.channelId] ?? 0
        version.value[frame.channelId] = curv + 1
      })
    }
    flushStats(false)
  }

  /** 累积到达计数用于估算实际帧率。 */
  function accumulateRate(channelId: string, ts: number): void {
    const win = rateWindows.get(channelId)
    if (!win) {
      rateWindows.set(channelId, { startTs: ts, count: 1 })
      return
    }
    win.count += 1
  }

  /** 把非响应式计数器安全地写入响应式 stats（限频 1Hz）。 */
  let lastStatsFlush = 0
  function flushStats(force: boolean): void {
    const now = Date.now()
    if (!force && now - lastStatsFlush < STATS_FLUSH_INTERVAL_MS) return
    if (now - lastStatsFlush < 0) return
    for (const [channelId, win] of rateWindows) {
      const elapsed = now - win.startTs
      if (elapsed <= 0) continue
      const hz = (win.count * 1000) / elapsed
      const frame = latest.get(channelId)
      const prev = stats.value[channelId]
      stats.value[channelId] = {
        lastFrameAt: frame?.ingestedTs ?? prev?.lastFrameAt ?? 0,
        latencyMs: frame ? frame.ingestedTs - frame.publishedTs : prev?.latencyMs ?? 0,
        hz,
        dropped: prev?.dropped ?? 0,
        received: (prev?.received ?? 0) + win.count,
        payloadType: frame?.payloadType ?? prev?.payloadType ?? 'json',
        lastSizeBytes: frame?.sizeBytes ?? prev?.lastSizeBytes ?? 0,
        status: prev?.status ?? 'online',
        ...(prev?.lastError ? { lastError: prev.lastError } : {}),
      }
      win.startTs = now
      win.count = 0
    }
    lastStatsFlush = now
  }

  /** 服务端丢帧通知（ARCH §6.3 backpressure）。 */
  function onBackpressure(channelId: string, dropped: number): void {
    const prev = stats.value[channelId]
    if (prev) {
      stats.value[channelId] = { ...prev, dropped: prev.dropped + dropped }
    } else {
      ensureStats(channelId)
      const cur = stats.value[channelId]
      if (cur) stats.value[channelId] = { ...cur, dropped: dropped }
    }
  }

  /** 服务端通道状态。 */
  function onChannelStatus(channelId: string, status: Status, lastError?: string, latencyMs?: number): void {
    ensureStats(channelId)
    const prev = stats.value[channelId]
    if (!prev) return
    stats.value[channelId] = {
      ...prev,
      status,
      ...(lastError ? { lastError } : {}),
      ...(latencyMs !== undefined ? { latencyMs } : {}),
    }
  }

  function ensureStats(channelId: string): void {
    if (stats.value[channelId]) return
    stats.value[channelId] = {
      lastFrameAt: 0,
      latencyMs: 0,
      hz: 0,
      dropped: 0,
      received: 0,
      payloadType: 'json',
      lastSizeBytes: 0,
      status: 'idle',
    }
  }

  /** 确保缓冲存在（默认容量 1200；渲染器可按 manifest.maxPoints 申请更大容量）。 */
  function ensureBuffer(channelId: string, capacity = DEFAULT_RING_CAPACITY): RingBuffer<Frame> {
    const existing = buffers.get(channelId)
    const want = Math.max(1, Math.trunc(capacity))
    if (existing) {
      if (want > existing.limit) {
        // 升容：保留尾部历史，避免图表断线
        const upgraded = new RingBuffer<Frame>(want)
        for (const item of existing.toArray()) upgraded.push(item)
        buffers.set(channelId, upgraded)
        capacities.set(channelId, want)
        return upgraded
      }
      return existing
    }
    const buf = new RingBuffer<Frame>(want)
    buffers.set(channelId, buf)
    capacities.set(channelId, want)
    return buf
  }

  /** F6：命令式读取历史缓冲（渲染器专用）。 */
  function getBuffer(channelId: string | undefined): RingBuffer<Frame> | undefined {
    if (!channelId) return undefined
    return buffers.get(channelId)
  }

  /** 命令式读取最新帧。 */
  function getLatest(channelId: string | undefined): Frame | undefined {
    if (!channelId) return undefined
    return latest.get(channelId)
  }

  /** 通道运行统计。 */
  function getStats(channelId: string | undefined): ChannelStats | undefined {
    if (!channelId) return undefined
    return stats.value[channelId]
  }

  /** 某通道是否收到过数据。 */
  function hasReceived(channelId: string | undefined): boolean {
    return channelId ? latest.has(channelId) : false
  }

  /**
   * 切换看板时清理未被引用的 buffer（ST-07 内存保护）。
   * @param keepChannelIds 当前仍需保留的通道集合
   */
  function prune(keepChannelIds: readonly string[]): void {
    const keep = new Set(keepChannelIds)
    for (const key of Array.from(buffers.keys())) {
      if (!keep.has(key)) buffers.delete(key)
    }
    for (const key of Array.from(latest.keys())) {
      if (!keep.has(key)) latest.delete(key)
    }
    for (const key of Array.from(pending.keys())) {
      if (!keep.has(key)) pending.delete(key)
    }
    for (const key of Array.from(rateWindows.keys())) {
      if (!keep.has(key)) rateWindows.delete(key)
    }
    for (const key of Array.from(capacities.keys())) {
      if (!keep.has(key)) capacities.delete(key)
    }
    // stats 属于低频小对象，仅清理长期无数据的通道，避免面板闪动
    for (const key of Object.keys(stats.value)) {
      if (!keep.has(key)) {
        const v = version.value[key]
        if (v === undefined) {
          delete stats.value[key]
        }
      }
    }
  }

  /** 丢弃某通道全部历史（折线图「清空」按钮）。 */
  function clearChannel(channelId: string): void {
    buffers.get(channelId)?.clear()
  }

  /** 释放 WS 处理器（仅测试/HMR 用）。 */
  function dispose(): void {
    disposeHandlers?.()
    disposeHandlers = null
  }

  registerHandlers()

  return {
    version,
    stats,
    bufferedChannels,
    onFrames,
    ensureBuffer,
    getBuffer,
    getLatest,
    getStats,
    hasReceived,
    prune,
    clearChannel,
    flushStats,
    dispose,
  }
})
