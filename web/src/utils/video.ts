/**
 * 视频播放后端抽象：WebRTC(WHEP) → HLS → FLV 三实现 + 降级编排（ARCH §2.3 / §11.3，决策 D1）。
 *
 * 关键事实：MediaMTX 原生不支持 HTTP-FLV（官方 issue #1460 拒绝实现），
 * 因此**默认降级目标为 HLS（mpegts variant）+ hls.js**；
 * `flv` 仅在后端下发了 `flvBase`（外部 FLV 网关）且 `urls.flv` 存在时才启用。
 *
 * 像素流从浏览器直连 MediaMTX，**绝不经过 Go 后端**。
 */
import type { VideoPayload } from '@/types'
import { VIDEO_MAX_MSE_BUFFER_SEC } from './constants'

export type VideoProtocol = 'webrtc' | 'hls' | 'flv'

export interface VideoBackendOptions {
  /** 是否请求音轨（WebRTC transceiver 数量）。 */
  audio?: boolean
}

export interface VideoBackend {
  readonly protocol: VideoProtocol
  /** 绑定并播放；失败时 throw，由调用方决定降级。 */
  attach(el: HTMLVideoElement, url: string, opts?: VideoBackendOptions): Promise<void>
  /** 解除 media 绑定，但保留可复用状态。 */
  detach(): void
  /** 彻底释放底层资源（unmount 必须调用）。 */
  destroy(): void
}

/** mpegts.js 的最小结构化描述（避免业务层依赖其庞杂类型）。 */
interface MpegtsPlayer {
  attachMediaElement(el: HTMLMediaElement): void
  load(): void
  destroy(): void
  play(): Promise<void> | void
  on(event: string, cb: (payload: unknown) => void): void
}
interface MpegtsStatic {
  createPlayer(opts: Record<string, unknown>, config?: Record<string, unknown>): MpegtsPlayer
  isSupported(): boolean
}

/** Hls.js 的最小结构化描述。 */
interface HlsInstance {
  attachMedia(el: HTMLMediaElement): void
  loadSource(url: string): void
  destroy(): void
  on(event: string, cb: (event: string, data: unknown) => void): void
}
interface HlsStaticStatic {
  new (config: Record<string, unknown>): HlsInstance
  isSupported(): boolean
}

/* ——————————————— ① WebRTC（原生 WHEP，无第三方依赖） ——————————————— */

class WebRTCBackend implements VideoBackend {
  readonly protocol: VideoProtocol = 'webrtc'
  private pc: RTCPeerConnection | null = null
  private stream: MediaStream | null = null

  async attach(el: HTMLVideoElement, url: string, opts: VideoBackendOptions = {}): Promise<void> {
    this.destroy()
    const pc = new RTCPeerConnection({ iceServers: [] })
    pc.addTransceiver('video', { direction: 'recvonly' })
    if (opts.audio) pc.addTransceiver('audio', { direction: 'recvonly' })
    this.pc = pc

    // 用一条稳定的 MediaStream 汇聚多条 track，避免 ontrack 多次触发导致 srcObject 抖动
    const stream = new MediaStream()
    this.stream = stream
    pc.ontrack = (ev: RTCTrackEvent): void => {
      stream.addTrack(ev.track)
      el.srcObject = stream
      void el.play().catch(() => undefined)
    }
    // 记录协商失败原因，供上层在超时窗口内快速判定降级
    pc.onconnectionstatechange = (): void => {
      if (pc.connectionState === 'failed') {
        console.warn('[video] WebRTC ICE 连接失败')
      }
    }

    const offer = await pc.createOffer()
    await pc.setLocalDescription(offer)
    // 等 ICE 收集完成再发 offer，否则缺少 candidate 会导致协商失败
    await waitIceGatheringComplete(pc)

    const res = await fetch(url, {
      method: 'POST',
      mode: 'cors',
      headers: { 'Content-Type': 'application/sdp' },
      body: pc.localDescription?.sdp ?? offer.sdp ?? '',
    })
    if (!res.ok) {
      throw new Error(`WHEP 协商失败 HTTP ${res.status}`)
    }
    const answer = await res.text()
    await pc.setRemoteDescription({ type: 'answer', sdp: answer })
  }

  detach(): void {
    if (this.stream) {
      for (const track of this.stream.getTracks()) {
        track.stop()
      }
      this.stream = null
    }
  }

  destroy(): void {
    this.detach()
    if (this.pc) {
      this.pc.ontrack = null
      this.pc.onconnectionstatechange = null
      this.pc.close()
      this.pc = null
    }
  }
}

/** 等待 ICE 收集结束（最多 timeoutMs，超时则放弃等待但仍继续协商）。 */
function waitIceGatheringComplete(pc: RTCPeerConnection, timeoutMs = 2000): Promise<void> {
  if (pc.iceGatheringState === 'complete') return Promise.resolve()
  return new Promise<void>((resolve) => {
    let settled = false
    const done = (): void => {
      if (settled) return
      settled = true
      pc.removeEventListener('icegatheringstatechange', check)
      window.clearTimeout(timer)
      resolve()
    }
    const check = (): void => {
      if (pc.iceGatheringState === 'complete') done()
    }
    const timer = window.setTimeout(done, timeoutMs)
    pc.addEventListener('icegatheringstatechange', check)
  })
}

/* ——————————————— ② HLS（hls.js，MVP 默认降级目标） ——————————————— */

class HlsBackend implements VideoBackend {
  readonly protocol: VideoProtocol = 'hls'
  private hls: HlsInstance | null = null

  async attach(el: HTMLVideoElement, url: string): Promise<void> {
    this.destroy()
    const mod = await import('hls.js')
    const Hls = mod.default as unknown as HlsStaticStatic
    if (!Hls.isSupported()) {
      // Safari 原生 HLS：直接喂 src，不属于降级失败
      el.src = url
      await el.play().catch(() => undefined)
      return
    }
    const hls = new Hls({
      enableWorker: true,
      lowLatencyMode: true,
      // 直播低时延：缓冲追赶到目标秒数，控制在 1–3s
      liveSyncDuration: VIDEO_MAX_MSE_BUFFER_SEC,
      maxBufferLength: VIDEO_MAX_MSE_BUFFER_SEC * 2,
      maxMaxBufferLength: VIDEO_MAX_MSE_BUFFER_SEC * 4,
      backBufferLength: 10,
    })
    this.hls = hls
    hls.on('hlsError', (_event: string, data: unknown) => {
      // 仅告警：由调用方的重试/降级编排决定是否切换
      console.warn('[video] hls.js 播放错误：', data)
    })
    hls.attachMedia(el)
    hls.loadSource(url)
    await el.play().catch(() => undefined)
  }

  detach(): void {
    // hls.js 无轻量 detach：由 destroy 统一释放
  }

  destroy(): void {
    if (this.hls) {
      this.hls.destroy()
      this.hls = null
    }
  }
}

/* ——————————————— ③ FLV（mpegts.js，仅 flvBase 可用时启用） ——————————————— */

class FlvBackend implements VideoBackend {
  readonly protocol: VideoProtocol = 'flv'
  private player: MpegtsPlayer | null = null

  async attach(el: HTMLVideoElement, url: string): Promise<void> {
    this.destroy()
    const mod = await import('mpegts.js')
    const mpegts = (mod.default ?? mod) as unknown as MpegtsStatic
    if (!mpegts.isSupported()) {
      throw new Error('当前浏览器不支持 MSE，无法播放 FLV')
    }
    const player = mpegts.createPlayer({ type: 'flv', url, isLive: true }, {
      enableWorker: false,
      liveBufferLatencyChasing: true,
      liveBufferLatencyMaxLatency: VIDEO_MAX_MSE_BUFFER_SEC,
      liveBufferLatencyMinRemain: 1,
    })
    this.player = player
    player.on('error', (payload: unknown) => {
      console.warn('[video] mpegts.js 播放错误：', payload)
    })
    player.attachMediaElement(el)
    player.load()
    await el.play().catch(() => undefined)
  }

  detach(): void {
    // mpegts 无轻量 detach：由 destroy 统一释放
  }

  destroy(): void {
    if (this.player) {
      this.player.destroy()
      this.player = null
    }
  }
}

/** 工厂：按协议创建后端实例。 */
export function createBackend(protocol: VideoProtocol): VideoBackend {
  switch (protocol) {
    case 'hls':
      return new HlsBackend()
    case 'flv':
      return new FlvBackend()
    case 'webrtc':
    default:
      return new WebRTCBackend()
  }
}

/** 该 payload 下实际可用的 URL（按协议）。 */
export function availableProtocols(payload: VideoPayload | undefined): VideoProtocol[] {
  if (!payload) return []
  const out: VideoProtocol[] = []
  for (const p of ['webrtc', 'hls', 'flv'] as const) {
    const url = payload.urls[p]
    if (typeof url === 'string' && url.length > 0) out.push(p)
  }
  return out
}

/**
 * 计算降级顺序：首选协议在前，其后按 webrtc → hls → flv 的固定优先串。
 * 只会包含实际有 URL 的协议。
 */
export function fallbackOrder(preferred: VideoProtocol, available: readonly VideoProtocol[]): VideoProtocol[] {
  const base: VideoProtocol[] = ['webrtc', 'hls', 'flv']
  const ordered: VideoProtocol[] = []
  const push = (p: VideoProtocol): void => {
    if (available.includes(p) && !ordered.includes(p)) ordered.push(p)
  }
  push(preferred)
  for (const p of base) push(p)
  return ordered
}

/** 解析实际播放计划：返回首个有地址且被后端允许的协议。 */
export function resolvePlayback(
  payload: VideoPayload | undefined,
  preferred: VideoProtocol,
  allowedBackends: readonly VideoProtocol[],
): { protocol: VideoProtocol; url: string } | null {
  const available = availableProtocols(payload).filter((p) => allowedBackends.includes(p))
  const order = fallbackOrder(preferred, available)
  for (const protocol of order) {
    const url = payload?.urls[protocol]
    if (typeof url === 'string' && url.length > 0) return { protocol, url }
  }
  return null
}

/**
 * 后台探测 WHEP 是否恢复可用（成功后可切回首选协议，ARCH §11.3）。
 * 只做协商，不 attach 到任何 video 元素，不产生画面。
 */
export async function probeWebrtc(url: string, timeoutMs: number): Promise<boolean> {
  const pc = new RTCPeerConnection({ iceServers: [] })
  try {
    pc.addTransceiver('video', { direction: 'recvonly' })
    const offer = await pc.createOffer()
    await pc.setLocalDescription(offer)
    const controller = new AbortController()
    const timer = window.setTimeout(() => controller.abort(), timeoutMs)
    try {
      const res = await fetch(url, {
        method: 'POST',
        mode: 'cors',
        headers: { 'Content-Type': 'application/sdp' },
        body: offer.sdp ?? '',
        signal: controller.signal,
      })
      return res.ok
    } finally {
      window.clearTimeout(timer)
    }
  } catch {
    return false
  } finally {
    pc.close()
  }
}

/** 是否为安全上下文（WebRTC 在 http 非 localhost 下不可用）。 */
export function isPlayableSecureContext(): boolean {
  return window.isSecureContext
}
