<script setup lang="ts">
/**
 * RD-07 视频播放器（T-28 / 决策 D1 / ARCH §11.3）。
 *
 * 降级链：WebRTC(WHEP) 首选 → 不可用时自动降级 HLS（hls.js）；
 *         仅当后端返回了 urls.flv（外部 FLV 网关）时才启用 mpegts.js。
 * 降级后每 retryPreferredSec 后台探测首选协议，恢复后自动切回；
 * 支持手动「强制切换协议」；像素流浏览器直连 MediaMTX，**不过 Go 后端**。
 */
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import type { CardVisualStatus, RendererProps, VideoPayload } from '@/types'
import type { VideoBackend, VideoProtocol } from '@/utils/video'
import {
  availableProtocols,
  createBackend,
  fallbackOrder,
  isPlayableSecureContext,
  negotiateTimeoutMs,
  probePlayable,
  resolvePlayback,
  rewritePlayUrlsHost,
} from '@/utils/video'
import { useFrameFeed } from '@/composables/useFrameFeed'
import { useCardVisibility } from '@/composables/cardVisibility'
import { useUiStore } from '@/stores/ui'
import { copyText } from '@/utils/clipboard'
import { maskUrlCredentials } from '@/utils/redact'
import {
  MAP_VIDEO_SHED_T1_MS,
  MAP_VIDEO_SHED_T2_MS,
  VIDEO_LATENCY_HINT,
  VIDEO_PROTOCOL_LABEL,
  VIDEO_RETRY_PREFERRED_DEFAULT_SEC,
} from '@/utils/constants'
import EmptyState from '@/components/base/EmptyState.vue'

const props = defineProps<RendererProps>()
const emit = defineEmits<{ status: [status: CardVisualStatus] }>()

const ui = useUiStore()
const { latest } = useFrameFeed(() => props.card.channelId)

const videoEl = ref<HTMLVideoElement | null>(null)
const backend = shallowRef<VideoBackend | null>(null)
const activeProtocol = ref<VideoProtocol | undefined>(undefined)
const degraded = ref(false)
const failure = ref('')
const loading = ref(false)
/** 用户锁定的协议（undefined = 跟随自动策略）。 */
const lockedProtocol = ref<VideoProtocol | undefined>(undefined)

const preferred = computed<VideoProtocol>(() => (props.config.preferredProtocol as VideoProtocol) ?? 'webrtc')
const autoDowngrade = computed<boolean>(() => props.config.autoDowngrade !== false)
const autoplay = computed<boolean>(() => props.config.autoplay !== false)
const muted = computed<boolean>(() => props.config.muted !== false)
const showControls = computed<boolean>(() => props.config.showControls !== false)
const retryPreferredSec = computed<number>(() =>
  Number(props.config.retryPreferredSec ?? VIDEO_RETRY_PREFERRED_DEFAULT_SEC),
)
const audio = computed<boolean>(() => (props.config.audio as boolean | undefined) === true)

const payload = computed<VideoPayload | undefined>(() => latest.value?.payload as VideoPayload | undefined)

/**
 * 失败态说明文案：优先展示后端下发的人话诊断（区分「MediaMTX API 不通 /
 * 拉流上游未就绪 / 等待推流端连接」三种异常），没有 detail 才回落通用文案。
 *
 * 为什么值得后端专门下发：MediaMTX 的管理 API 只回 ready=false、不带原因，
 * 真实错误（如 dial tcp ...: connection refused）只存在于 MediaMTX 日志里；
 * 没有这个字段时，用户看到「HLS 协商超时」完全不知道该查推流端还是查本机服务。
 */
const failureDetail = computed<string>(
  () => payload.value?.detail || '可尝试手动切换协议，或检查推流端与 MediaMTX 是否在线',
)

/**
 * publish（接收推流）模式下推流方应推送的地址。
 * 后端用 BuildPublishURLs 生成（publicHost 回落 lanHost）；原样展示不打码 ——
 * 它就是要被复制给推流端的那一个地址（与 DataSourceDetailDrawer 同口径）。
 */
const publishUrl = computed<string>(() => payload.value?.publishUrl ?? '')

/** 拉流模式的上游地址：RTSP 常带内联凭据（rtsp://admin:pw@host/...），展示前必须脱敏。 */
const maskedSourceUrl = computed<string>(() => {
  const raw = payload.value?.sourceUrl ?? ''
  return raw ? maskUrlCredentials(raw) : ''
})

async function copy(text: string): Promise<void> {
  if (!text) return
  const ok = await copyText(text)
  if (!ok) failure.value = '复制失败，请手动选中地址复制'
}

/**
 * 实际用于播放的地址表。
 *
 * embedded 模式下把后端下发地址的 host 校正为「当前页面的 host」：后端只能按网卡字典序
 * 探测 publicHost（实测是 VPN 网卡 10.42.0.1），跨主机访问时不可达，hls.js 连 m3u8 都拿不到。
 * external 模式（MediaMTX 在别的主机）绝不改写 —— 那会把地址指向页面所在主机的错误端口。
 */
const playUrls = computed<Partial<Record<VideoProtocol, string>>>(() => {
  const urls = payload.value?.urls ?? {}
  if (ui.runtime?.mediaMtx?.embedded !== true) return urls
  return rewritePlayUrlsHost(urls, window.location.hostname)
})

const playOptions = computed(() => {
  const list = availableProtocols(payload.value).filter((p) => ui.videoBackends.includes(p))
  // WebRTC 需要安全上下文：http 且非本机访问时 WHEP 用不了，提前剔除候选，
  // 免得每张卡都白等一个完整超时窗口才降级（note/07 §7.6 的口径，此前该函数无人调用）。
  if (isPlayableSecureContext()) return list
  return list.filter((p) => p !== 'webrtc')
})
const candidateOrder = computed<VideoProtocol[]>(() => fallbackOrder(preferred.value, playOptions.value))

let probeTimer = 0

/** 关闭并释放当前后端。 */
function teardown(): void {
  backend.value?.destroy()
  backend.value = null
  if (videoEl.value) {
    videoEl.value.pause()
    videoEl.value.srcObject = null
    videoEl.value.removeAttribute('src')
    videoEl.value.load()
  }
}

async function play(protocol: VideoProtocol): Promise<void> {
  const el = videoEl.value
  const frame = payload.value
  if (!el || !frame) return
  const url = playUrls.value[protocol]
  if (!url) {
    failure.value = `后端未提供 ${VIDEO_PROTOCOL_LABEL[protocol]} 播放地址`
    return
  }
  teardown()
  loading.value = true
  failure.value = ''
  const next = createBackend(protocol)
  // 按协议取超时预算：HLS 给 12s（MediaMTX on-demand 冷启动实测 ~8s），其余沿用 3s
  const budget = negotiateTimeoutMs(protocol)
  try {
    // 协商超时保护：超时即视为失败，进入降级链下一个协议
    await Promise.race([
      next.attach(el, url, { audio: audio.value }),
      new Promise<never>((_, reject) =>
        window.setTimeout(() => reject(new Error(`${VIDEO_PROTOCOL_LABEL[protocol]} 协商超时`)), budget),
      ),
    ])
    backend.value = next
    el.muted = muted.value
    el.autoplay = autoplay.value
    if (autoplay.value) await el.play().catch(() => undefined)
    activeProtocol.value = protocol
    degraded.value = protocol !== preferred.value
    emit('status', degraded.value ? 'degraded' : 'online')
    startProbe()
  } catch (err) {
    next.destroy()
    failure.value = err instanceof Error ? err.message : '播放失败'
    throw err
  } finally {
    loading.value = false
  }
}

/** 按降级顺序依次尝试播放。 */
async function startPlayback(): Promise<void> {
  const order = lockedProtocol.value ? [lockedProtocol.value] : candidateOrder.value
  if (order.length === 0) {
    failure.value = payload.value?.ready === false ? '流尚未就绪，等待推流端上线' : '没有可用的播放地址'
    emit('status', 'waiting')
    return
  }
  for (const protocol of order) {
    try {
      await play(protocol)
      return
    } catch {
      // 关闭自动降级时只尝试首个协议；否则继续尝试下一个降级目标
      if (!autoDowngrade.value) break
    }
  }
  if (activeProtocol.value === undefined) {
    emit('status', 'error')
  }
}

/** 降级后台探测首选协议是否恢复。 */
function startProbe(): void {
  stopProbe()
  if (!degraded.value || !autoDowngrade.value || lockedProtocol.value) return
  const target = playUrls.value[preferred.value]
  if (!target) return
  probeTimer = window.setInterval(async () => {
    const url = playUrls.value[preferred.value]
    if (!url) return
    // 探测**当前配置的首选协议**，不是写死的 webrtc：用户手动把首选设成 HLS 后，
    // 硬编码 webrtc 会让回切探测永久失效（note/07 §7.6）。
    const ok = await probePlayable(preferred.value, url, negotiateTimeoutMs(preferred.value))
    if (ok) {
      stopProbe()
      try {
        await play(preferred.value)
      } catch {
        // 探测通过但实播失败：保持降级态，下一轮再试
        startProbe()
      }
    }
  }, retryPreferredSec.value * 1000)
}

function stopProbe(): void {
  if (probeTimer !== 0) {
    window.clearInterval(probeTimer)
    probeTimer = 0
  }
}

/** 帧里地址的整体签名（换源/换路径时会变化）。 */
const urlsSignature = computed<string>(() => {
  const frame = payload.value
  if (!frame) return ''
  return `${frame.path}|${frame.sourceUrl}|${frame.urls.webrtc ?? ''}|${frame.urls.hls ?? ''}|${frame.urls.flv ?? ''}`
})

watch(urlsSignature, () => {
  // 已降载：不立刻重建（否则出视野期间地址刷新会把刚断掉的流又拉起来），
  // 但要记下"地址变过"——恢复时才知道必须重建而不是续播旧连接。
  if (shedStage.value > 0) {
    pendingUrlRefresh = true
    return
  }
  const next = resolvePlayback(payload.value, lockedProtocol.value ?? preferred.value, playOptions.value)
  // 地址没变（只是状态帧刷新）→ 不重启播放，避免画面抖动
  if (next && next.protocol === activeProtocol.value) return
  activeProtocol.value = undefined
  stopProbe()
  void startPlayback()
})

/* ————————————————— 出视野两阶段降载（C4 / P-04 / P-05） —————————————————
 *
 * 目的：20 张视频卡同时解码会让 GPU 与下行带宽双双打满，而用户其实只看得见其中几张。
 *
 * ⏱ 计时基准：**全部自 t0（彻底出视界那一刻）起算，不串行叠加**。
 *   t0 由 useViewportCull 持有、经 CardVisibility 透传。若改成"从 visible 翻 false 起算"，
 *   因为 visible 本身已在 t0+grace(1s) 才翻转，实际断流时刻会变成 t0+2s / t0+11s —— 白等一秒。
 *
 * 两阶段（对 WHEP 而言）：
 *   阶段一 t0+1s ：el.pause() —— 停渲染解码，但**保留下行**，回视野时直接 play，秒回
 *   阶段二 t0+10s：pc.close()  —— 真正关闭 PeerConnection，下行流量归零
 * 对 HLS/FLV 只有一个有效阶段：destroy() 即断流（它们本来就不具备"暂停但保持连接"的语义）。
 */
const visibility = useCardVisibility()
/** 0 = 未降载；1 = 阶段一；2 = 阶段二。 */
const shedStage = ref(0)

let shedT1 = 0
let shedT2 = 0
/**
 * 降载期间地址是否变更过。
 *
 * 评审 F5：出视野 → 阶段一（WHEP `pause()`，连接还活着）→ 推流端重启换了地址 →
 * urlsSignature 变了但被上面的 early-return 丢弃。回视野时若是直接 `play()` 旧连接，
 * 画面会指向已经死掉的流（黑屏），且签名没再变所以永远不重建。这个标记让它必须重建。
 */
let pendingUrlRefresh = false

function clearShedTimers(): void {
  if (shedT1) {
    window.clearTimeout(shedT1)
    shedT1 = 0
  }
  if (shedT2) {
    window.clearTimeout(shedT2)
    shedT2 = 0
  }
}

function shedStageOne(): void {
  shedStage.value = 1
  if (activeProtocol.value === 'webrtc') {
    videoEl.value?.pause()
    return
  }
  backend.value?.destroy()
  backend.value = null
}

function shedStageTwo(): void {
  shedStage.value = 2
  teardown()
}

function restoreFromShed(): void {
  const stage = shedStage.value
  shedStage.value = 0
  if (stage === 0) return
  if (pendingUrlRefresh) {
    // 降载期间地址变过：旧连接已无意义，必须走完整重建
    pendingUrlRefresh = false
    activeProtocol.value = undefined
    void startPlayback()
    return
  }
  if (stage === 1 && activeProtocol.value === 'webrtc' && videoEl.value) {
    void videoEl.value.play().catch(() => undefined)
    return
  }
  // 阶段一（非 WHEP，已 destroy）或阶段二：需要重新走完整播放链
  void startPlayback()
}

watch(
  () => [visibility.visible.value, visibility.outOfViewSince.value] as const,
  ([vis, t0]) => {
    clearShedTimers()
    if (vis) {
      restoreFromShed()
      return
    }
    const now = Date.now()
    const base = t0 > 0 ? t0 : now
    shedT1 = window.setTimeout(shedStageOne, Math.max(0, base + MAP_VIDEO_SHED_T1_MS - now))
    shedT2 = window.setTimeout(shedStageTwo, Math.max(0, base + MAP_VIDEO_SHED_T2_MS - now))
  },
  { immediate: true },
)

onMounted(() => {
  void startPlayback()
})

onBeforeUnmount(() => {
  clearShedTimers()
  stopProbe()
  teardown()
})

function togglePlay(): void {
  const el = videoEl.value
  if (!el) return
  if (el.paused) void el.play().catch(() => undefined)
  else el.pause()
}

function toggleMute(): void {
  const el = videoEl.value
  if (!el) return
  el.muted = !el.muted
}

function setVolume(volume: number): void {
  const el = videoEl.value
  if (!el) return
  el.volume = Math.min(1, Math.max(0, volume))
  if (el.volume > 0) el.muted = false
}

function requestFullscreen(): void {
  void videoEl.value?.requestFullscreen().catch(() => undefined)
}

function forceProtocol(protocol: VideoProtocol): void {
  lockedProtocol.value = protocol
  stopProbe()
  activeProtocol.value = undefined
  void startPlayback()
}

function retryNow(): void {
  stopProbe()
  activeProtocol.value = undefined
  void startPlayback()
}

const protocolLabel = computed<string>(() => {
  const p = activeProtocol.value
  if (!p) return loading.value ? '协商中…' : '未播放'
  return VIDEO_PROTOCOL_LABEL[p]
})
</script>

<template>
  <div class="ma-video">
    <video
      ref="videoEl"
      class="ma-video__el"
      playsinline
      :muted="muted"
      :autoplay="autoplay"
      @dblclick="requestFullscreen"
    ></video>

    <!-- 错误 / 等待 -->
    <div v-if="failure && !activeProtocol" class="ma-video__stub">
      <empty-state icon="waiting" :title="failure" :description="failureDetail">
        <template #action>
          <!-- publish 模式：把「推流方应该推到哪」直接摆到卡片上，省得推流方猜 IP/端口/流名 -->
          <div v-if="publishUrl" class="ma-video__diag">
            <span class="ma-text-xs">请推流至</span>
            <code class="ma-video__url">{{ publishUrl }}</code>
            <button class="ma-video__btn" @click="copy(publishUrl)">复制地址</button>
          </div>
          <!-- 拉流模式：展示正在拉取的上游（脱敏），用户可直观核对该机器是否在推流 -->
          <div v-else-if="maskedSourceUrl" class="ma-video__diag">
            <span class="ma-text-xs">正在拉取</span>
            <code class="ma-video__url">{{ maskedSourceUrl }}</code>
          </div>
          <button class="ma-video__btn" @click="retryNow">重试</button>
        </template>
      </empty-state>
    </div>

    <!-- 悬浮控制条 -->
    <div v-if="showControls && activeProtocol" class="ma-video__bar">
      <button class="ma-video__ctrl" title="播放/暂停" @click="togglePlay">▶/⏸</button>
      <button class="ma-video__ctrl" title="静音" @click="toggleMute">🔊</button>
      <input
        class="ma-video__vol"
        type="range"
        min="0"
        max="1"
        step="0.05"
        :value="videoEl?.volume ?? 1"
        @input="setVolume(Number(($event.target as HTMLInputElement).value))"
      />
      <select
        class="ma-video__sel"
        :value="lockedProtocol ?? activeProtocol"
        title="强制切换协议"
        @change="forceProtocol(($event.target as HTMLSelectElement).value as VideoProtocol)"
      >
        <option v-for="p in candidateOrder" :key="p" :value="p">{{ VIDEO_PROTOCOL_LABEL[p] }}</option>
      </select>
      <span class="ma-video__tag" :class="{ 'is-degraded': degraded }">
        {{ protocolLabel }}<span v-if="degraded">（降级 {{ VIDEO_LATENCY_HINT[activeProtocol ?? 'hls'] }}）</span>
      </span>
      <div class="ma-video__grow"></div>
      <span class="ma-video__meta ma-text-xs">重试 ⟳ {{ retryPreferredSec }}s</span>
      <button class="ma-video__ctrl" title="全屏" @click="requestFullscreen">⛶</button>
    </div>
  </div>
</template>

<style scoped>
.ma-video {
  position: relative;
  width: 100%;
  height: 100%;
  background: #000;
  overflow: hidden;
}

.ma-video__el {
  width: 100%;
  height: 100%;
  object-fit: contain;
  display: block;
  background: #000;
}

.ma-video__stub {
  position: absolute;
  inset: 0;
  background: var(--ma-bg-card);
}

.ma-video__bar {
  position: absolute;
  left: 0;
  right: 0;
  bottom: 0;
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 6px;
  background: linear-gradient(to top, rgba(0, 0, 0, 0.72), rgba(0, 0, 0, 0));
  opacity: 0;
  transition: opacity 0.16s ease;
}

.ma-video:hover .ma-video__bar,
.ma-video__bar:focus-within {
  opacity: 1;
}

.ma-video__ctrl {
  border: none;
  background: rgba(255, 255, 255, 0.12);
  color: #fff;
  border-radius: 4px;
  cursor: pointer;
  padding: 2px 6px;
  font-size: 11px;
}

.ma-video__ctrl:hover {
  background: rgba(255, 255, 255, 0.24);
}

.ma-video__vol {
  width: 64px;
  accent-color: var(--ma-accent);
}

.ma-video__sel,
.ma-video__tag {
  height: 20px;
  padding: 0 4px;
  border-radius: 4px;
  font-size: 11px;
}

.ma-video__sel {
  border: 1px solid rgba(255, 255, 255, 0.24);
  background: rgba(0, 0, 0, 0.42);
  color: #fff;
}

.ma-video__tag {
  display: inline-flex;
  align-items: center;
  color: var(--ma-accent);
  background: rgba(0, 0, 0, 0.42);
}

.ma-video__tag.is-degraded {
  color: var(--ma-status-degraded);
}

.ma-video__grow {
  flex: 1 1 auto;
}

.ma-video__meta {
  color: rgba(255, 255, 255, 0.66);
}

/* 失败态诊断行：推流地址 / 上游地址 + 复制按钮 */
.ma-video__diag {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  max-width: 100%;
  margin-bottom: 6px;
  color: var(--ma-text-3);
}

.ma-video__url {
  overflow: hidden;
  max-width: 58%;
  padding: 2px 6px;
  border-radius: 4px;
  background: rgba(127, 127, 127, 0.16);
  font-size: var(--ma-font-xs);
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
