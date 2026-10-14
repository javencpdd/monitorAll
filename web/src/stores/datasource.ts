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
  /**
   * `loadSources()` 的并发去重句柄。
   *
   * 为什么需要：加了启动期调用点后，`main.ts`（启动）与视图侧的
   * `ensureAllChannels()`（自愈补拉）可能在同一次会话里**同时**触发。
   * 不加去重会打出两个并发请求、后一个结果覆盖前一个，且 `loading` 会被提前置回。
   */
  let inflightLoadSources: Promise<void> | null = null

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

  /**
   * 从服务端拉取**全部**数据源列表。
   *
   * ⚠️ 本函数是全前端唯一把服务端已有数据源灌进 `sources` 的地方。
   * 一旦没有调用点，`sources` 就恒为空 —— 而 `ensureAllChannels()` 按 sources 逐个拉通道，
   * sources 为空时它会「成功地什么都不做」，于是左侧面板 / 通道下拉 / 位置通道选择**全部为空**，
   * 且接口 200、控制台无报错。这是本项目最典型的静默失败形态（已由 mount-check 守卫钉住调用点）。
   *
   * 并发去重：同一次会话里被多处同时调用时只发一个请求（见 `inflightLoadSources`）。
   * 失败语义：写入 `error`（供 SourcePanel 三态展示）后**继续向上抛**，让调用方能提示用户。
   */
  async function loadSources(): Promise<void> {
    if (inflightLoadSources) return inflightLoadSources
    const p = (async () => {
      loading.value = true
      try {
        sources.value = await api.listDataSources()
        error.value = ''
      } catch (err) {
        // 静默失败是本项目头号敌人：失败原因必须落到 error，UI 才能如实显示"加载失败"而非"没有数据源"。
        error.value = err instanceof Error ? err.message : String(err)
        throw err
      } finally {
        loading.value = false
        inflightLoadSources = null
      }
    })()
    inflightLoadSources = p
    return p
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

  /**
   * 批量确保每个数据源的通道都已加载（看板订阅前调用）。
   *
   * ⚠️ **自愈**：本函数按 `sources` 逐个拉通道，若 `sources` 为空，
   * `Promise.all([])` 会「成功地什么都不做」—— 静默失败。
   * 启动时那次 `loadSources()` 若因后端尚未就绪而失败，这里就是唯一的补救机会：
   * 因此列表为空时先补拉一次，再按拉到的 sources 加载通道。
   */
  async function ensureAllChannels(): Promise<void> {
    if (sources.value.length === 0) await loadSources()
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
