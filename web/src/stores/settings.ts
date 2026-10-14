/**
 * 全局设置 Store：品牌（项目名 / 副标题 / Logo）+ 当日天气（开关 / 坐标 / 挂件快照）。
 *
 * 三条设计约束（都是踩过的坑）：
 *  1. **错误处理必须显式**。任何请求失败都要把后端中文 message（或网络兜底文案）写进
 *     `error` / `weatherError`，绝不静默留空 —— 「点了没反应」是最难排查的故障形态。
 *  2. **首屏去闪**。GET /settings 是异步的，而品牌名在首帧就要渲染。因此启动早期先吃
 *     `runtime.branding`（与 WS 同批下发，比 /settings 早），拿到权威值后再覆盖。
 *  3. **区分「后端明确不可用」与「这次请求失败」**。GET /weather 在开关关闭/坐标非法/上游挂掉时
 *     依然返回 200 + `ok:false`，那是结论不是错误；只有网络/超时/非 0 code 才算错误。
 *     两者在 UI 上的处置完全不同（前者展示原因，后者给重试入口）。
 */
import { defineStore } from 'pinia'
import { computed, ref, watch } from 'vue'
import * as api from '@/api/endpoints'
import { toMessage } from '@/api/client'
import type {
  BrandingPayload,
  SettingsPayload,
  WeatherSettingsPayload,
  WeatherSnapshot,
} from '@/types'
import { useUiStore } from '@/stores/ui'
import { useDashboardStore } from '@/stores/dashboard'
import {
  DEFAULT_BRAND_SUBTITLE,
  DEFAULT_BRAND_TITLE,
  DEFAULT_MAP_CENTER,
  WEATHER_PROVIDER_DEFAULT,
  WEATHER_REFRESH_DEFAULT_MIN,
  WEATHER_REFRESH_MAX,
  WEATHER_REFRESH_MIN,
} from '@/utils/constants'
import { clamp } from '@/utils/format'

/** 品牌默认值：title 允许为空（用户清空即回落），但 store 内始终保存真实值。 */
function defaultBranding(): BrandingPayload {
  return { title: DEFAULT_BRAND_TITLE, subtitle: DEFAULT_BRAND_SUBTITLE, logoUrl: '' }
}

/**
 * 天气默认值：开关**默认关闭**（出网行为必须由用户显式开启）；
 * 坐标给一个合法值（北京），否则用户一开开关就撞上 coords_invalid 的失败态。
 */
function defaultWeather(): WeatherSettingsPayload {
  return {
    enabled: false,
    provider: WEATHER_PROVIDER_DEFAULT,
    lat: DEFAULT_MAP_CENTER[1],
    lon: DEFAULT_MAP_CENTER[0],
    label: '',
    refreshMin: WEATHER_REFRESH_DEFAULT_MIN,
  }
}

/**
 * 归一化后端返回值。
 *
 * 为什么不做「空 title → 默认名」的回填：副标题留空是合法需求，而项目名留空要在
 * **保存时**明确提示用户「将回落为 MonitorAll」。若在读侧悄悄回填，用户会看到
 * 自己没填过的字，且无从得知这是回落值。显示侧统一走 `brandTitle` computed 兜底。
 */
function normalizeBranding(input: Partial<BrandingPayload> | null | undefined): BrandingPayload {
  const base = defaultBranding()
  if (!input) return base
  return {
    title: typeof input.title === 'string' ? input.title : base.title,
    subtitle: typeof input.subtitle === 'string' ? input.subtitle : '',
    logoUrl: typeof input.logoUrl === 'string' ? input.logoUrl : '',
  }
}

function normalizeWeather(input: Partial<WeatherSettingsPayload> | null | undefined): WeatherSettingsPayload {
  const base = defaultWeather()
  if (!input) return base
  const lat = Number(input.lat)
  const lon = Number(input.lon)
  const refresh = Number(input.refreshMin)
  return {
    enabled: input.enabled === true,
    provider: typeof input.provider === 'string' && input.provider.length > 0 ? input.provider : base.provider,
    lat: Number.isFinite(lat) ? lat : base.lat,
    lon: Number.isFinite(lon) ? lon : base.lon,
    label: typeof input.label === 'string' ? input.label : '',
    refreshMin: Number.isFinite(refresh)
      ? clamp(Math.trunc(refresh), WEATHER_REFRESH_MIN, WEATHER_REFRESH_MAX)
      : base.refreshMin,
  }
}

export const useSettingsStore = defineStore('settings', () => {
  const ui = useUiStore()
  const dashboardStore = useDashboardStore()

  const branding = ref<BrandingPayload>(defaultBranding())
  const weather = ref<WeatherSettingsPayload>(defaultWeather())
  /** GET /settings 进行中。 */
  const loading = ref(false)
  /** PUT /settings 进行中（抽屉保存按钮禁用）。 */
  const saving = ref(false)
  /** Logo 上传/清除进行中。 */
  const uploading = ref(false)
  /** 设置读写失败原因（中文，来自后端 message 或网络兜底）。空串 = 无错误。 */
  const error = ref('')
  /**
   * Logo 版本号：每次上传/清除 +1，用于给**稳定路径**的 Logo 加缓存破坏参数。
   *
   * 为什么需要：Logo 若由后端固定端点提供（如 `/api/v1/system/branding/logo`），
   * 上传新图后 URL 不变，浏览器命中强缓存继续显示旧图 —— 用户会认为"上传没生效"。
   * 见下面 `logoSrc` 的收窄条件（只处理本服务相对路径，绝不篡改外部 URL）。
   */
  const logoVersion = ref(0)
  /**
   * 天气**请求**失败原因。
   *
   * 与 `error` 分开是刻意的：两者挂在完全不同的按钮上（保存 vs 立即刷新），
   * 共用一个字段会出现「保存失败后刷新一下天气，错误提示就消失了」的错乱归因。
   */
  const weatherError = ref('')
  const weatherSnapshot = ref<WeatherSnapshot | null>(null)
  /** GET /settings 是否已成功过：成功前允许 runtime 下发的品牌覆盖默认值。 */
  const loaded = ref(false)

  let timer = 0

  /** 顶栏 / 文档标题展示用的项目名（空 → 内置默认名）。 */
  const brandTitle = computed<string>(() => branding.value.title.trim() || DEFAULT_BRAND_TITLE)
  /** 副标题：允许为空，为空时不渲染、标题也不带分隔符。 */
  const brandSubtitle = computed<string>(() => branding.value.subtitle.trim())

  /**
   * 顶栏实际使用的 Logo 地址（含缓存破坏参数）。
   *
   * 收窄到**只在需要时**动 URL：仅当「刚上传/清除过」且「URL 是本服务相对路径、且自带查询串」时
   * 追加 `?v=N`。外部 URL（用户手填的图床）一律原样使用 —— 往别人的 URL 上挂参数是明确有害的。
   * 后端若已返回带版本号的地址（推荐），本逻辑天然不生效。
   */
  const logoSrc = computed<string>(() => {
    const raw = branding.value.logoUrl.trim()
    if (logoVersion.value === 0 || raw === '' || raw.includes('?')) return raw
    return raw.startsWith('/') ? `${raw}?v=${logoVersion.value}` : raw
  })

  /** 刷新间隔（分钟夹在 1~120 之后再转毫秒）。 */
  const refreshIntervalMs = computed<number>(
    () => clamp(weather.value.refreshMin, WEATHER_REFRESH_MIN, WEATHER_REFRESH_MAX) * 60_000,
  )

  function applyDocumentTitle(): void {
    document.title = brandSubtitle.value
      ? `${brandTitle.value} · ${brandSubtitle.value}`
      : brandTitle.value
  }

  /**
   * 首屏去闪：runtime 比 GET /settings 早到，先用它把顶栏和标签页标题顶上去。
   * `loaded` 之后不再覆盖 —— 否则后端 runtime 刷新会把用户刚保存的品牌改回去。
   */
  watch(
    () => ui.runtime?.branding,
    (b) => {
      if (!b || loaded.value) return
      branding.value = normalizeBranding(b)
      applyDocumentTitle()
    },
    { immediate: true },
  )

  /** 品牌变化 → 立即改写标签页标题（含保存后）。 */
  watch([brandTitle, brandSubtitle], applyDocumentTitle)

  async function load(): Promise<void> {
    loading.value = true
    try {
      const data = await api.getSettings()
      branding.value = normalizeBranding(data?.branding)
      weather.value = normalizeWeather(data?.weather)
      loaded.value = true
      error.value = ''
      applyDocumentTitle()
      restartTimer()
    } catch (err) {
      // 显式：绝不静默。后端 message 优先，网络异常走 toMessage 的原始文案。
      error.value = toMessage(err) || '读取全局设置失败'
      ui.pushError(-1, `读取全局设置失败：${error.value}`)
    } finally {
      loading.value = false
    }
  }

  /** 整体写回。成功返回 true；失败会把中文原因写进 `error`（供抽屉内展示），不抛。 */
  async function save(next: SettingsPayload): Promise<boolean> {
    saving.value = true
    error.value = ''
    try {
      const data = await api.saveSettings(next)
      // 后端可能回空 data（有的实现只回 {code:0}），此时以本次提交值为准。
      branding.value = normalizeBranding(data?.branding ?? next.branding)
      weather.value = normalizeWeather(data?.weather ?? next.weather)
      loaded.value = true
      applyDocumentTitle()
      restartTimer()
      return true
    } catch (err) {
      error.value = toMessage(err) || '保存全局设置失败'
      return false
    } finally {
      saving.value = false
    }
  }

  async function uploadLogo(file: File): Promise<string> {
    uploading.value = true
    error.value = ''
    try {
      const resp = await api.uploadLogo(file)
      const url = typeof resp?.logoUrl === 'string' ? resp.logoUrl : ''
      if (!url) {
        error.value = '上传成功但后端未返回 Logo 地址'
        return ''
      }
      branding.value = { ...branding.value, logoUrl: url }
      logoVersion.value += 1
      return url
    } catch (err) {
      error.value = toMessage(err) || 'Logo 上传失败'
      return ''
    } finally {
      uploading.value = false
    }
  }

  async function removeLogo(): Promise<boolean> {
    uploading.value = true
    error.value = ''
    try {
      await api.deleteLogo()
      branding.value = { ...branding.value, logoUrl: '' }
      logoVersion.value += 1
      return true
    } catch (err) {
      error.value = toMessage(err) || '清除 Logo 失败'
      return false
    } finally {
      uploading.value = false
    }
  }

  /**
   * 正在途中的天气请求。
   *
   * 去重的必要性：`load()`/`save()` 会显式 restartTimer()，而它同时也会被
   * 「开关/间隔变化」的 watcher 触发一次 —— 同一次设置变更会打两次 GET /weather。
   * 定时轮询恰好撞上手动刷新时同理。这里做 promise 级去重，重复调用共享同一个请求。
   */
  let inflight: Promise<void> | null = null

  async function doLoadWeather(): Promise<void> {
    try {
      const snap = await api.getWeather()
      weatherSnapshot.value = snap
      // ok=false 是结论不是错误：原因由 snap.message 承载，此处清空"请求失败"文案，
      // 否则上一次网络失败的残留文案会和本次的明确原因叠在一起，用户看到两个矛盾的说法。
      weatherError.value = ''
    } catch (err) {
      weatherSnapshot.value = null
      weatherError.value = toMessage(err) || '天气数据拉取失败'
    }
  }

  /**
   * 拉一次天气快照。
   *
   * 开关关闭时直接清空（不发请求、不留旧快照），避免出现「关了开关天气还在」。
   */
  function loadWeather(): Promise<void> {
    if (!weather.value.enabled) {
      weatherSnapshot.value = null
      weatherError.value = ''
      return Promise.resolve()
    }
    if (inflight) return inflight
    inflight = doLoadWeather().finally(() => {
      inflight = null
    })
    return inflight
  }

  function stopTimer(): void {
    if (timer !== 0) {
      window.clearInterval(timer)
      timer = 0
    }
  }

  /** 按当前开关与间隔重建轮询（先立刻拉一次，避免等满一个周期才有数据）。 */
  function restartTimer(): void {
    stopTimer()
    if (!weather.value.enabled) {
      weatherSnapshot.value = null
      weatherError.value = ''
      return
    }
    void loadWeather()
    timer = window.setInterval(() => void loadWeather(), refreshIntervalMs.value)
  }

  // 开关 / 间隔变化 → 重建轮询。两个 watcher 合并成一个，避免改间隔时先清后建两次。
  watch(
    [() => weather.value.enabled, refreshIntervalMs],
    () => restartTimer(),
  )

  // 切换看板后立刻刷一次：看板切换往往伴随长时间停留，等满一个周期会看到过期数据。
  watch(
    () => dashboardStore.current?.id,
    () => {
      if (weather.value.enabled) void loadWeather()
    },
  )

  function dispose(): void {
    stopTimer()
  }

  return {
    branding,
    weather,
    loading,
    saving,
    uploading,
    error,
    weatherError,
    weatherSnapshot,
    loaded,
    brandTitle,
    brandSubtitle,
    logoSrc,
    refreshIntervalMs,
    load,
    save,
    uploadLogo,
    removeLogo,
    loadWeather,
    restartTimer,
    dispose,
  }
})
