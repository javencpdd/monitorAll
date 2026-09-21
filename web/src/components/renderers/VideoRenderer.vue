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
import { availableProtocols, createBackend, fallbackOrder, probeWebrtc, resolvePlayback } from '@/utils/video'
import { useFrameFeed } from '@/composables/useFrameFeed'
import { useUiStore } from '@/stores/ui'
import {
  VIDEO_LATENCY_HINT,
  VIDEO_NEGOTIATE_TIMEOUT_MS,
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
const playOptions = computed(() =>
  availableProtocols(payload.value).filter((p) => ui.videoBackends.includes(p)),
)
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
  const url = frame.urls[protocol]
  if (!url) {
    failure.value = `后端未提供 ${VIDEO_PROTOCOL_LABEL[protocol]} 播放地址`
    return
  }
  teardown()
  loading.value = true
  failure.value = ''
  const next = createBackend(protocol)
  try {
    // 协商超时保护：超时即视为失败，进入降级链下一个协议
    await Promise.race([
      next.attach(el, url, { audio: audio.value }),
      new Promise<never>((_, reject) =>
        window.setTimeout(() => reject(new Error(`${VIDEO_PROTOCOL_LABEL[protocol]} 协商超时`)), VIDEO_NEGOTIATE_TIMEOUT_MS),
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
  const target = payload.value?.urls[preferred.value]
  if (!target) return
  probeTimer = window.setInterval(async () => {
    const url = payload.value?.urls[preferred.value]
    if (!url) return
    const ok = await probeWebrtc(url, VIDEO_NEGOTIATE_TIMEOUT_MS)
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
  const next = resolvePlayback(payload.value, lockedProtocol.value ?? preferred.value, playOptions.value)
  // 地址没变（只是状态帧刷新）→ 不重启播放，避免画面抖动
  if (next && next.protocol === activeProtocol.value) return
  activeProtocol.value = undefined
  stopProbe()
  void startPlayback()
})

onMounted(() => {
  void startPlayback()
})

onBeforeUnmount(() => {
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
      <empty-state icon="waiting" :title="failure" description="可尝试手动切换协议，或检查推流端与 MediaMTX 是否在线">
        <template #action>
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
</style>
