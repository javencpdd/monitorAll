/**
 * WebSocket 客户端：单连接多路复用 + 心跳 + 指数退避重连 + 重连后重放订阅表。
 * 契约来源：docs/ARCHITECTURE.md §6。
 *
 * 生命周期要点：
 *   - 打开后 5s 内必须发 hello，否则服务端关闭（因此我们一开就发）
 *   - 服务端主动 ping，客户端必须 3s 内回 pong
 *   - 客户端 45s 未收到任何消息 → 判定僵死并主动重连
 *   - 重连成功后一次性重放本地订阅表（subscribe 携带全部 channelIds）
 *   - 整个前端只有 1 条 WS 连接（40 卡片也只 1 条，ST-07 自检项）
 */
import { ref } from 'vue'
import type { ClientMessage, ServerMessage } from '@/types/ws'
import {
  WS_CLOSE_NORMAL,
  WS_CLOSE_STALE,
  WS_PROTOCOL_VERSION,
  WS_RECONNECT_BASE_MS,
  WS_RECONNECT_JITTER,
  WS_RECONNECT_MAX_MS,
  WS_STALE_TIMEOUT_MS,
} from '@/utils/constants'

export type ConnPhase = 'idle' | 'connecting' | 'open' | 'reconnecting' | 'closed'

/** 服务端消息订阅回调集合（全部可选，未注册的消息被安全忽略）。 */
export interface MessageHandlers {
  welcome?: (msg: Extract<ServerMessage, { op: 'welcome' }>) => void
  subscribed?: (msg: Extract<ServerMessage, { op: 'subscribed' }>) => void
  unsubscribed?: (msg: Extract<ServerMessage, { op: 'unsubscribed' }>) => void
  frames?: (msg: Extract<ServerMessage, { op: 'frames' }>) => void
  channelStatus?: (msg: Extract<ServerMessage, { op: 'channelStatus' }>) => void
  sourceStatus?: (msg: Extract<ServerMessage, { op: 'sourceStatus' }>) => void
  backpressure?: (msg: Extract<ServerMessage, { op: 'backpressure' }>) => void
  error?: (msg: Extract<ServerMessage, { op: 'error' }>) => void
}

function randomRef(): string {
  return `r${Date.now().toString(36)}${Math.floor(Math.random() * 1e6).toString(36)}`
}

function randomClientId(): string {
  return `web-${Math.random().toString(36).slice(2, 10)}`
}

/** 退避时长：base*2^n，上限 max，叠加 ±jitter。 */
export function backoffDelay(attempt: number, baseMs: number, maxMs: number, jitter: number): number {
  const raw = Math.min(maxMs, baseMs * Math.pow(2, Math.max(0, attempt - 1)))
  const delta = jitter > 0 ? raw * jitter * (Math.random() * 2 - 1) : 0
  return Math.max(baseMs * 0.5, raw + delta)
}

/** 相对/绝对 WS 地址归一化（同 REST 端口）。 */
export function resolveWsUrl(raw: string | undefined, fallbackPath: string): string {
  const withProtocol = ensureProtocolQuery(fallbackPath)
  if (!raw || raw.length === 0) {
    const scheme = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    return `${scheme}//${window.location.host}${withProtocol}`
  }
  if (/^wss?:\/\//i.test(raw)) {
    const url = new URL(raw)
    if (!url.searchParams.has('protocol')) {
      url.searchParams.set('protocol', String(WS_PROTOCOL_VERSION))
    }
    return url.toString()
  }
  const base = raw.includes('?') ? `${raw}&protocol=${WS_PROTOCOL_VERSION}` : `${raw}?protocol=${WS_PROTOCOL_VERSION}`
  if (base.startsWith('//')) {
    const scheme = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    return `${scheme}${base}`
  }
  const scheme = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${scheme}//${window.location.host}${base.startsWith('/') ? base : `/${base}`}`
}

function ensureProtocolQuery(path: string): string {
  return path.includes('?') ? path : `${path}?protocol=${WS_PROTOCOL_VERSION}`
}

class WebSocketClient {
  /** 连接阶段（响应式，供顶部横幅与状态条消费）。 */
  readonly phase = ref<ConnPhase>('idle')
  /** 最近一次 ping→pong 估算的单向延迟（ms）。 */
  readonly latencyMs = ref<number>(0)
  /** 服务端时间与本地时间的差值（ms），用于校正 publishedTs。 */
  readonly clockOffsetMs = ref<number>(0)
  /** 下一次自动重连的时间戳（0 表示未安排），驱动 UI 倒计时。 */
  readonly nextRetryAt = ref<number>(0)
  /** 连接断开/失败原因。 */
  readonly lastError = ref<string>('')

  private socket: WebSocket | null = null
  private handlers: MessageHandlers[] = []
  /** 本地订阅表：channelId → 引用计数（扁平 synchronous，重连时整体重放）。 */
  private readonly subscriptions = new Map<string, number>()
  private attempt = 0
  private lastRecvAt = 0
  private watchdogTimer = 0
  private retryTimer = 0
  private manualClose = false
  private url = ''
  private clientId = randomClientId()

  /** 注册消息处理器，返回注销函数。 */
  addHandlers(set: MessageHandlers): () => void {
    this.handlers.push(set)
    return () => {
      const idx = this.handlers.indexOf(set)
      if (idx >= 0) this.handlers.splice(idx, 1)
    }
  }

  /** 当前是否可发消息。 */
  get connected(): boolean {
    return this.socket?.readyState === WebSocket.OPEN
  }

  /** 连接 WS；重复调用会先断开旧连接。url 为空时按当前页面地址推导。 */
  connect(rawUrl?: string): void {
    this.url = resolveWsUrl(rawUrl, '/api/v1/ws')
    this.manualClose = false
    this.openSocket()
  }

  /** 主动断开（不再重连）。 */
  disconnect(): void {
    this.manualClose = true
    this.clearWatchdog()
    this.clearRetry()
    this.nextRetryAt.value = 0
    if (this.socket) {
      this.socket.close(WS_CLOSE_NORMAL, 'client closed')
      this.socket = null
    }
    this.phase.value = 'closed'
  }

  /** 引用计数 +1 后按需发送 subscribe。 */
  subscribe(channelIds: string[]): void {
    const needSend: string[] = []
    for (const id of channelIds) {
      const cur = this.subscriptions.get(id) ?? 0
      if (cur === 0) needSend.push(id)
      this.subscriptions.set(id, cur + 1)
    }
    if (needSend.length > 0 && this.connected) {
      this.send({ op: 'subscribe', channelIds: needSend, ref: randomRef() })
    }
  }

  /** 引用计数 -1，归零时才真正 unsubscribe。 */
  unsubscribe(channelIds: string[]): void {
    const toRelease: string[] = []
    for (const id of channelIds) {
      const cur = this.subscriptions.get(id) ?? 0
      if (cur <= 1) {
        this.subscriptions.delete(id)
        toRelease.push(id)
      } else {
        this.subscriptions.set(id, cur - 1)
      }
    }
    if (toRelease.length > 0 && this.connected) {
      this.send({ op: 'unsubscribe', channelIds: toRelease, ref: randomRef() })
    }
  }

  /** 当前订阅中的 channelId 列表。 */
  subscribedIds(): string[] {
    return Array.from(this.subscriptions.keys())
  }

  private openSocket(): void {
    this.clearRetry()
    if (this.socket) {
      this.socket.onopen = null
      this.socket.onmessage = null
      this.socket.onclose = null
      this.socket.onerror = null
      this.socket.close(1000, 'reconnect')
      this.socket = null
    }
    this.phase.value = this.attempt === 0 ? 'connecting' : 'reconnecting'
    let socket: WebSocket
    try {
      socket = new WebSocket(this.url)
    } catch (err) {
      this.lastError.value = err instanceof Error ? err.message : '创建连接失败'
      this.scheduleReconnect()
      return
    }
    this.socket = socket

    socket.onopen = (): void => {
      this.attempt = 0
      this.lastRecvAt = Date.now()
      this.nextRetryAt.value = 0
      this.phase.value = 'open'
      this.lastError.value = ''
      // 必须在 hello 截止时间前完成握手（ARCH §6.1）
      window.setTimeout(() => {
        if (socket.readyState === WebSocket.OPEN) {
          this.send({ op: 'hello', protocol: WS_PROTOCOL_VERSION, clientId: this.clientId })
        }
      }, 0)
      this.startWatchdog()
      // 重连成功后重放订阅表：一次性携带全部 channelId（Set 语义，后端幂等）
      const ids = this.subscribedIds()
      if (ids.length > 0) {
        this.send({ op: 'subscribe', channelIds: ids, ref: randomRef() })
      }
    }

    socket.onmessage = (ev: MessageEvent<string>): void => {
      this.lastRecvAt = Date.now()
      let msg: ServerMessage
      try {
        msg = JSON.parse(ev.data) as ServerMessage
      } catch {
        console.warn('[ws] 收到非法 JSON 消息')
        return
      }
      this.dispatch(msg)
    }

    socket.onerror = (): void => {
      this.lastError.value = 'WebSocket 连接错误'
    }

    socket.onclose = (ev: CloseEvent): void => {
      this.socket = null
      this.clearWatchdog()
      if (this.manualClose) {
        this.phase.value = 'closed'
        return
      }
      this.lastError.value = ev.reason || `连接已关闭（${ev.code}）`
      this.scheduleReconnect()
    }
  }

  private dispatch(msg: ServerMessage): void {
    switch (msg.op) {
      case 'welcome': {
        this.clockOffsetMs.value = msg.serverTimeMs - Date.now()
        for (const set of this.handlers) set.welcome?.(msg)
        break
      }
      case 'ping': {
        // 服务端要求 3s 内回 pong
        this.send({ op: 'pong', t: msg.t })
        this.latencyMs.value = Math.max(0, Date.now() - msg.t)
        break
      }
      case 'frames':
        for (const set of this.handlers) set.frames?.(msg)
        break
      case 'channelStatus':
        for (const set of this.handlers) set.channelStatus?.(msg)
        break
      case 'sourceStatus':
        for (const set of this.handlers) set.sourceStatus?.(msg)
        break
      case 'backpressure':
        for (const set of this.handlers) set.backpressure?.(msg)
        break
      case 'subscribed':
        for (const set of this.handlers) set.subscribed?.(msg)
        break
      case 'unsubscribed':
        for (const set of this.handlers) set.unsubscribed?.(msg)
        break
      case 'error':
        console.warn(`[ws] 服务端错误 ${msg.code}：${msg.message}`)
        for (const set of this.handlers) set.error?.(msg)
        break
      default:
        break
    }
  }

  private send(msg: ClientMessage): void {
    if (!this.socket || this.socket.readyState !== WebSocket.OPEN) return
    try {
      this.socket.send(JSON.stringify(msg))
    } catch (err) {
      console.warn('[ws] 发送失败：', err)
    }
  }

  /** 心跳看门狗：超时未收到任何消息则主动重连。 */
  private startWatchdog(): void {
    this.clearWatchdog()
    this.watchdogTimer = window.setInterval(() => {
      if (Date.now() - this.lastRecvAt > WS_STALE_TIMEOUT_MS) {
        console.warn('[ws] 长时间未收到消息，主动重连')
        this.lastError.value = '心跳超时'
        this.socket?.close(WS_CLOSE_STALE, 'stale connection')
      }
    }, Math.floor(WS_STALE_TIMEOUT_MS / 3))
  }

  private clearWatchdog(): void {
    if (this.watchdogTimer) {
      window.clearInterval(this.watchdogTimer)
      this.watchdogTimer = 0
    }
  }

  private clearRetry(): void {
    if (this.retryTimer) {
      window.clearTimeout(this.retryTimer)
      this.retryTimer = 0
    }
  }

  private scheduleReconnect(): void {
    if (this.manualClose) return
    this.clearRetry()
    this.attempt += 1
    const delay = backoffDelay(this.attempt, WS_RECONNECT_BASE_MS, WS_RECONNECT_MAX_MS, WS_RECONNECT_JITTER)
    this.nextRetryAt.value = Date.now() + delay
    this.phase.value = this.attempt === 1 ? 'connecting' : 'reconnecting'
    this.retryTimer = window.setTimeout(() => {
      this.retryTimer = 0
      if (this.manualClose) return
      this.openSocket()
    }, delay)
  }
}

let singleton: WebSocketClient | null = null

/** 取得 WS 客户端单例（全前端唯一连接）。 */
export function getWsClient(): WebSocketClient {
  if (!singleton) singleton = new WebSocketClient()
  return singleton
}

export type { WebSocketClient }
