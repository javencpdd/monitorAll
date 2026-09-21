/**
 * 数据源 + 通道 Store（ARCH §10.1）。
 *
 * 负责：数据源/通道的 CRUD 调用、CI/CD 状态订阅（sourceStatus / channelStatus）。
 * 状态更新**全部来自 WS**，不在此处自行推导。
 */
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import type {
  ChannelRuntime,
  CreateChannelReq,
  CreateDataSourceReq,
  DataSource,
  DataSourceKind,
  PayloadType,
  Status,
  TestResult,
  UpdateDataSourceReq,
} from '@/types'
import * as api from '@/api/endpoints'
import { getWsClient } from '@/ws/client'

export interface SourceRuntime {
  status: Status
  lastError?: string
  reconnectInMs?: number
  nextRetryAt?: number
}

export const useDatasourceStore = defineStore('datasource', () => {
  const sources = ref<DataSource[]>([])
  const channelsBySource = ref<Record<string, ChannelRuntime[]>>({})
  /** 数据源运行时状态（来自 WS sourceStatus）。 */
  const sourceRuntime = ref<Record<string, SourceRuntime>>({})
  /** 加载态。 */
  const loading = ref(false)
  /** 最近一次错误文案。 */
  const error = ref('')

  let disposeHandlers: (() => void) | null = null

  const allChannels = computed<ChannelRuntime[]>(() => {
    const out: ChannelRuntime[] = []
    for (const list of Object.values(channelsBySource.value)) out.push(...list)
    return out
  })

  const channelById = computed<Record<string, ChannelRuntime>>(() => {
    const map: Record<string, ChannelRuntime> = {}
    for (const ch of allChannels.value) map[ch.id] = ch
    return map
  })

  const sourcesByKind = computed<Record<DataSourceKind, DataSource[]>>(() => {
    const grouped: Record<DataSourceKind, DataSource[]> = { video: [], ros: [], http: [] }
    for (const s of sources.value) grouped[s.kind].push(s)
    return grouped
  })

  function registerHandlers(): void {
    if (disposeHandlers) return
    disposeHandlers = getWsClient().addHandlers({
      sourceStatus: (msg) => {
        const prev = sourceRuntime.value[msg.dataSourceId]
        sourceRuntime.value[msg.dataSourceId] = {
          status: msg.status,
          ...(msg.lastError ? { lastError: msg.lastError } : {}),
          ...(msg.reconnectInMs !== undefined ? { reconnectInMs: msg.reconnectInMs } : {}),
          ...(msg.nextRetryAt !== undefined ? { nextRetryAt: msg.nextRetryAt } : {}),
          ...(prev && msg.lastError === undefined ? { lastError: prev.lastError } : {}),
        }
        upsertLocalStatus(msg.dataSourceId, msg.status, msg.lastError)
      },
      channelStatus: (msg) => {
        for (const list of Object.values(channelsBySource.value)) {
          const ch = list.find((c) => c.id === msg.channelId)
          if (ch) {
            ch.status = msg.status
            if (msg.lastError !== undefined) ch.lastError = msg.lastError
            if (msg.lastFrameAt !== undefined) ch.lastFrameAt = msg.lastFrameAt
            if (msg.latencyMs !== undefined) ch.latencyMs = msg.latencyMs
          }
        }
      },
    })
  }

  /** 同步更新 sources 列表里的 status（面板使用同一份数据）。 */
  function upsertLocalStatus(id: string, status: Status, lastError?: string): void {
    const src = sources.value.find((s) => s.id === id)
    if (!src) return
    src.status = status
    if (lastError !== undefined) src.lastError = lastError
  }

  async function loadSources(): Promise<void> {
    loading.value = true
    try {
      sources.value = await api.listDataSources()
      error.value = ''
    } finally {
      loading.value = false
    }
  }

  async function loadChannels(dataSourceId: string, discover = false): Promise<ChannelRuntime[]> {
    const list = await api.listChannels(dataSourceId, discover)
    channelsBySource.value[dataSourceId] = list
    return list
  }

  /** 建立数据源后立刻加载其通道（自动创建的 channel）。 */
  async function createSource(body: CreateDataSourceReq): Promise<DataSource> {
    const created = await api.createDataSource(body)
    sources.value.push(created)
    await loadChannels(created.id)
    return created
  }

  async function updateSource(id: string, body: UpdateDataSourceReq): Promise<DataSource> {
    const updated = await api.updateDataSource(id, body)
    const idx = sources.value.findIndex((s) => s.id === id)
    if (idx >= 0) sources.value[idx] = updated
    return updated
  }

  async function removeSource(id: string): Promise<void> {
    await api.deleteDataSource(id)
    sources.value = sources.value.filter((s) => s.id !== id)
    delete channelsBySource.value[id]
    delete sourceRuntime.value[id]
  }

  async function addChannel(dataSourceId: string, body: CreateChannelReq): Promise<ChannelRuntime> {
    const created = await api.createChannel(dataSourceId, body)
    const list = channelsBySource.value[dataSourceId] ?? []
    const runtime: ChannelRuntime = {
      ...created,
      refCount: 0,
      frameRateHz: 0,
      dropped: 0,
    }
    channelsBySource.value[dataSourceId] = [...list, runtime]
    return runtime
  }

  async function removeChannel(dataSourceId: string, channelId: string): Promise<void> {
    await api.deleteChannel(channelId)
    const list = channelsBySource.value[dataSourceId] ?? []
    channelsBySource.value[dataSourceId] = list.filter((c) => c.id !== channelId)
  }

  /** 批量确保每个数据源的通道都已加载（看板订阅前调用）。 */
  async function ensureAllChannels(): Promise<void> {
    await Promise.all(sources.value.map((s) => loadChannels(s.id)))
  }

  function getChannel(channelId: string | undefined): ChannelRuntime | undefined {
    if (!channelId) return undefined
    return channelById.value[channelId]
  }

  function getSource(dataSourceId: string | undefined): DataSource | undefined {
    if (!dataSourceId) return undefined
    return sources.value.find((s) => s.id === dataSourceId)
  }

  function getSourceRuntime(dataSourceId: string | undefined): SourceRuntime | undefined {
    if (!dataSourceId) return undefined
    return sourceRuntime.value[dataSourceId]
  }

  /** 推断某通道的 payloadType（卡片向导在拿不到样例帧时的兜底）。 */
  function guessPayloadType(channelId: string | undefined): PayloadType | undefined {
    return getChannel(channelId)?.payloadType
  }

  /** 供向导使用：测试连接。 */
  function testSource(body: CreateDataSourceReq): Promise<TestResult> {
    return api.testDataSource(body)
  }

  function dispose(): void {
    disposeHandlers?.()
    disposeHandlers = null
  }

  registerHandlers()

  return {
    sources,
    channelsBySource,
    sourceRuntime,
    loading,
    error,
    allChannels,
    channelById,
    sourcesByKind,
    loadSources,
    loadChannels,
    createSource,
    updateSource,
    removeSource,
    addChannel,
    removeChannel,
    ensureAllChannels,
    getChannel,
    getSource,
    getSourceRuntime,
    guessPayloadType,
    testSource,
    dispose,
  }
})
