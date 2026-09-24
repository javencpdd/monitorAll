/**
 * UI Store（ARCH §10.1）：主题、抽屉/向导开关、全局连接状态、错误日志、运行时配置。
 *
 * TASKS §1.4：组件内不 try/catch 业务错误，统一由 pushError 记录 + 提示。
 */
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import type { ThemeKind } from '@/styles/theme'
import type { AppErrorEntry } from '@/api/client'
import type { LanInfo, RuntimeConfig } from '@/types'
import * as api from '@/api/endpoints'
import { MAX_ERROR_LOG, TOAST_DURATION_MS } from '@/utils/constants'
import { getWsClient } from '@/ws/client'
import { themeRootClass } from '@/styles/theme'

export interface ToastItem {
  key: number
  type: 'success' | 'info' | 'warning' | 'error'
  message: string
}

export const useUiStore = defineStore('ui', () => {
  const themeKind = ref<ThemeKind>('dark')
  /** 后端下发的运行时配置（含高德 Key / WS 地址 / 可用视频后端）。 */
  const runtime = ref<RuntimeConfig | null>(null)
  const lan = ref<LanInfo | null>(null)
  const cardDrawer = ref<{ open: boolean; cardId?: string; mode: 'create' | 'edit'; channelId?: string }>({
    open: false,
    cardId: undefined,
    mode: 'edit',
  })
  const sourceWizard = ref<{ open: boolean; step: number; preselectKind?: string }>({ open: false, step: 1 })
  const errors = ref<AppErrorEntry[]>([])
  const toasts = ref<ToastItem[]>([])
  const booted = ref(false)

  const ws = getWsClient()

  /** 全局 WS 连接阶段。 */
  const connPhase = computed(() => ws.phase.value)
  /** WS 心跳测得的单向延迟。 */
  const wsLatencyMs = computed(() => ws.latencyMs.value)
  /** 下一次重连时间戳（0 表示未安排）。 */
  const wsNextRetryAt = computed(() => ws.nextRetryAt.value)
  const wsLastError = computed(() => ws.lastError.value)

  /** 高德 Key：优先运行时下发，其次构建期环境变量（ARCH D7）。 */
  const amapKey = computed<string>(() => runtime.value?.amapKey ?? import.meta.env.VITE_AMAP_KEY ?? '')
  /** 高德安全密钥 securityJsCode：2021-12-02 后申请的 Key 必须配合使用，否则样式切换等能力静默失败。 */
  const amapSecurityCode = computed<string>(() => runtime.value?.amapSecurityCode ?? '')
  /** 强制启用 WebGL 绘制：默认开（未下发时也开），否则无 GPU/手机 WebView 环境下样式一律不生效。 */
  const amapForceWebGL = computed<boolean>(() => runtime.value?.amapForceWebGL !== false)
  /** 地图是否可用（无 Key 时地图卡降级为坐标列表，绝不换境外底图）。 */
  const amapReady = computed<boolean>(() => amapKey.value.length > 0)
  /** 后端允许的视频后端（默认 webrtc + hls，D1）。 */
  const videoBackends = computed<('webrtc' | 'hls' | 'flv')[]>(() =>
    runtime.value?.videoBackends?.length ? runtime.value.videoBackends : ['webrtc', 'hls'],
  )

  function setTheme(kind: ThemeKind): void {
    themeKind.value = kind
    const root = document.documentElement
    root.classList.remove('ma-theme-dark', 'ma-theme-light')
    root.classList.add(themeRootClass(kind))
  }

  function toggleTheme(): void {
    setTheme(themeKind.value === 'dark' ? 'light' : 'dark')
  }

  /** 记录错误（上限 MAX_ERROR_LOG，超出丢弃最旧）。 */
  function pushError(code: number, message: string, traceId?: string): void {
    errors.value.push({ code, message, ...(traceId ? { traceId } : {}), at: Date.now() })
    if (errors.value.length > MAX_ERROR_LOG) errors.value.shift()
  }

  function notify(type: ToastItem['type'], message: string): void {
    const key = Date.now() + Math.random()
    toasts.value.push({ key, type, message })
    window.setTimeout(() => {
      toasts.value = toasts.value.filter((t) => t.key !== key)
    }, TOAST_DURATION_MS)
  }

  function success(message: string): void {
    notify('success', message)
  }
  function info(message: string): void {
    notify('info', message)
  }
  function warning(message: string): void {
    notify('warning', message)
  }
  function error(message: string): void {
    notify('error', message)
  }

  /** 把未知异常统一转成提示 + 错误日志。 */
  function handleError(err: unknown, fallback = '操作失败'): void {
    if (err && typeof err === 'object' && 'code' in err && 'message' in err) {
      const e = err as AppErrorEntry & { traceId?: string }
      pushError(Number(e.code) || -1, String(e.message) || fallback, e.traceId)
      error(String(e.message) || fallback)
      return
    }
    const message = err instanceof Error ? err.message : fallback
    pushError(-1, message)
    error(message)
  }

  async function loadRuntime(): Promise<RuntimeConfig> {
    const cfg = await api.getRuntime()
    runtime.value = cfg
    return cfg
  }

  async function loadLan(): Promise<LanInfo> {
    lan.value = await api.getLan()
    return lan.value
  }

  /** 建立 WS 连接（整个前端只有这一条）。 */
  function connectWs(): void {
    if (connPhase.value === 'open' || connPhase.value === 'connecting') return
    ws.connect(runtime.value?.wsUrl)
  }

  function disconnectWs(): void {
    ws.disconnect()
  }

  function openCardDrawer(cardId?: string, mode: 'create' | 'edit' = 'edit', channelId?: string): void {
    cardDrawer.value = { open: true, cardId, mode, ...(channelId ? { channelId } : {}) }
  }
  function closeCardDrawer(): void {
    cardDrawer.value = { open: false, cardId: undefined, mode: 'edit' }
  }

  function openSourceWizard(preselectKind?: string): void {
    sourceWizard.value = { open: true, step: 1, ...(preselectKind ? { preselectKind } : {}) }
  }
  function setWizardStep(step: number): void {
    sourceWizard.value = { ...sourceWizard.value, step }
  }
  function closeSourceWizard(): void {
    sourceWizard.value = { open: false, step: 1 }
  }

  /** 应用启动：加载运行时 → 建立 WS → 恢复/初始化主题。 */
  async function bootstrap(): Promise<void> {
    if (booted.value) return
    booted.value = true
    setTheme(themeKind.value)
    try {
      await loadRuntime()
    } catch (err) {
      pushError(-1, `拉取运行时配置失败：${err instanceof Error ? err.message : '未知错误'}`)
    }
    try {
      await loadLan()
    } catch {
      // LAN 信息失败不影响主流程
    }
    connectWs()
  }

  return {
    themeKind,
    runtime,
    lan,
    cardDrawer,
    sourceWizard,
    errors,
    toasts,
    booted,
    connPhase,
    wsLatencyMs,
    wsNextRetryAt,
    wsLastError,
    amapKey,
    amapSecurityCode,
    amapForceWebGL,
    amapReady,
    videoBackends,
    setTheme,
    toggleTheme,
    pushError,
    notify,
    success,
    info,
    warning,
    error,
    handleError,
    loadRuntime,
    loadLan,
    connectWs,
    disconnectWs,
    openCardDrawer,
    closeCardDrawer,
    openSourceWizard,
    setWizardStep,
    closeSourceWizard,
    bootstrap,
  }
})
