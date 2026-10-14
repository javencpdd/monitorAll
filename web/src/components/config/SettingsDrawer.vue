<script setup lang="ts">
/**
 * 全局设置抽屉：品牌（项目名 / 副标题 / Logo）+ 天气（开关 / 坐标 / 刷新）+ 关于（只读排障信息）。
 *
 * 外观沿用 CardConfigDrawer 的 `.ma-drawer` 体系（同一套 CSS 变量与滚动规则），
 * 但**整体 position:fixed 铺满视口高度**：它是全局设置，语义上不属于某个看板的舞台，
 * 挂在 TopBar 里可以只改一处就同时覆盖网格/地图两个视图。
 *
 * 关键交互约束（都是"静默失败"高发区）：
 *   - 坐标非法 / 间隔越界 → **保存按钮禁用 + 红字**，绝不允许"保存成功但其实没生效"
 *   - Logo 上传先在前端校验类型与大小并给出明确原因，不等后端 413
 *   - 保存失败把中文错误画在抽屉里，而不是只弹一个会消失的 toast
 *   - 「在地图上点选」在没有高德 Key 时禁用 + 解释，不做"点了没反应"
 */
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useSettingsStore } from '@/stores/settings'
import { useUiStore } from '@/stores/ui'
import { loadAMap, type AMapMap } from '@/utils/amapLoader'
import { isValidLatLonRange, toGCJ02, fromGCJ02 } from '@/utils/geo'
import { clamp, formatBytes, formatNumber, formatRelative } from '@/utils/format'
import {
  BRAND_SUBTITLE_MAX,
  BRAND_TITLE_MAX,
  DEFAULT_BRAND_SUBTITLE,
  DEFAULT_BRAND_TITLE,
  GEOLOCATE_TIMEOUT_MS,
  LOGO_ACCEPT_ATTR,
  LOGO_ACCEPT_MIME,
  LOGO_MAX_BYTES,
  SETTINGS_DRAWER_WIDTH,
  WEATHER_LABEL_MAX,
  WEATHER_REFRESH_DEFAULT_MIN,
  WEATHER_REFRESH_MAX,
  WEATHER_REFRESH_MIN,
  WEATHER_PROVIDER_DEFAULT,
} from '@/utils/constants'
import type { SettingsPayload } from '@/types'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const settings = useSettingsStore()
const ui = useUiStore()

/* ——————————————— 草稿（打开时从 store 拷贝，取消即丢弃） ——————————————— */

const title = ref('')
const subtitle = ref('')
const logoUrl = ref('')

const enabled = ref(false)
/**
 * 坐标用**字符串**草稿而非 number：`v-model.number` 在输入为空时回填空串，
 * 类型与运行时不一致，校验很容易写出漏洞。字符串 + parseFloat 是全类型安全的组合。
 */
const latRaw = ref('')
const lonRaw = ref('')
const label = ref('')
const refreshRaw = ref(String(WEATHER_REFRESH_DEFAULT_MIN))

/*
 * UI 提示位提前声明（在下面那个 immediate watcher 之前）。
 *
 * 原因：immediate watcher 会在 setup 期间**同步**执行，若它引用的 ref 声明在其后，
 * 就会踩到 TDZ（const 未初始化，访问抛 ReferenceError）—— 而现在的守卫只是
 * 「open=false 就 early return」，多改一行代码就会真的炸在挂载阶段。
 * 把状态声明全部前置，这类"顺序敏感"的隐患就不存在了。
 */
const logoHint = ref('')
/**
 * 提示的语义（成功 / 失败）。
 *
 * 用显式布尔而不是"看文案里有没有'失败'两个字"来决定颜色 —— 后者会随着文案调整
 * 悄悄失效（改了个措辞，红字变灰字），是最典型的脆弱耦合。
 */
const logoHintError = ref(false)
const geoHint = ref('')
const geoHintError = ref(false)
const mapError = ref('')
const previewBroken = ref(false)

function setLogoHint(text: string, isError = false): void {
  logoHint.value = text
  logoHintError.value = isError
}

function setGeoHint(text: string, isError = false): void {
  geoHint.value = text
  geoHintError.value = isError
}

const latNum = computed<number>(() => Number.parseFloat(latRaw.value))
const lonNum = computed<number>(() => Number.parseFloat(lonRaw.value))
const refreshNum = computed<number>(() => Number.parseInt(refreshRaw.value, 10))

watch(
  () => props.open,
  (isOpen) => {
    if (!isOpen) return
    title.value = settings.branding.title
    subtitle.value = settings.branding.subtitle
    logoUrl.value = settings.branding.logoUrl
    enabled.value = settings.weather.enabled
    latRaw.value = String(settings.weather.lat)
    lonRaw.value = String(settings.weather.lon)
    label.value = settings.weather.label
    refreshRaw.value = String(settings.weather.refreshMin)
    setLogoHint('')
    setGeoHint('')
    mapError.value = ''
    previewBroken.value = false
    // 尚未成功读取过设置 → 每次打开都重试一次。
    // 否则一次失败的 GET 会让"保存"永久禁用，而用户除了刷新页面别无他法。
    if (!settings.loaded) void settings.load()
  },
  { immediate: true },
)

/* ——————————————— 校验 ——————————————— */

const latOk = computed<boolean>(() => Number.isFinite(latNum.value) && Math.abs(latNum.value) <= 90)
const lonOk = computed<boolean>(() => Number.isFinite(lonNum.value) && Math.abs(lonNum.value) <= 180)
/** 值域校验交给 geo.ts（唯一坐标工具出口），不在这里另写一份边界。 */
const coordsOk = computed<boolean>(() => isValidLatLonRange(latNum.value, lonNum.value))
const refreshOk = computed<boolean>(
  () =>
    Number.isFinite(refreshNum.value) &&
    refreshNum.value >= WEATHER_REFRESH_MIN &&
    refreshNum.value <= WEATHER_REFRESH_MAX,
)

/**
 * (0,0) 且开关为开 —— 后端 `validateWeather` 明确拒绝这一组合。
 *
 * 为什么单独判：后端坐标是 float64 非指针，"没填"与"填了 0"不可区分，
 * 因此它只能把 (0,0) 当作"未设置"来拦（见 server/internal/api/settings.go 的
 * `if w.Enabled && w.Lat == 0 && w.Lon == 0`）。前端必须**同样**拦掉，
 * 否则用户会觉得"保存成功"，实际吃 400，而且完全不知道 0/0 为什么不行。
 * 注意 (0,0) 在值域上合法（几内亚湾真实坐标），所以这是业务规则不是范围错误。
 */
const zeroCoordsWithWeatherOn = computed<boolean>(
  () => enabled.value && latNum.value === 0 && lonNum.value === 0,
)

/**
 * 坐标错误文案。
 *
 * 除了"哪里错"，还必须写清"不修会怎样"——用户不知道后端会 400，
 * 只会以为"保存按钮坏了"。这条提示就是要把「保存按钮灰着」的原因说透。
 */
const coordError = computed<string>(() => {
  if (latOk.value && lonOk.value && !zeroCoordsWithWeatherOn.value) return ''
  const bad: string[] = []
  if (!latOk.value) bad.push(`纬度需在 -90 ~ 90${latRaw.value.trim() ? `（当前 ${latRaw.value}）` : '（未填）'}`)
  if (!lonOk.value) bad.push(`经度需在 -180 ~ 180${lonRaw.value.trim() ? `（当前 ${lonRaw.value}）` : '（未填）'}`)
  if (zeroCoordsWithWeatherOn.value) bad.push('开启天气时不能使用 0, 0（后端会把它当作"未设置坐标"，无法区分真实坐标）')
  return `${bad.join('；')}。不修正的话保存会被后端拒绝。`
})

/**
 * 实际会被提交的刷新间隔（分钟）。
 *
 * 它同时是「出网告知」里那个 N 与 `buildPayload` 的取值来源 —— **两处必须同源**，
 * 否则会出现"提示里说每 10 分钟请求一次、实际提交 120"这种又一次的"显示值 ≠ 生效值"。
 * NaN（留空/填非数字）先换成默认值再夹到边界，绝不让它穿到 JSON 里变成 null
 * （Go 侧 int 收到 null 会留零值，被后端夹成 1）。
 */
const refreshEffective = computed<number>(() =>
  clamp(
    Number.isFinite(refreshNum.value)
      ? Math.trunc(refreshNum.value)
      : WEATHER_REFRESH_DEFAULT_MIN,
    WEATHER_REFRESH_MIN,
    WEATHER_REFRESH_MAX,
  ),
)

/**
 * 刷新间隔提示文案。
 *
 * `refreshHint` 要回答的是"我填的这个值最后会变成什么"：
 * 后端对越界值是**静默夹取**（999 → 120），数据能保存，但用户看到的会是 999、
 * 实际生效的是 120 —— "看到的值 ≠ 生效的值"正是本项目反复踩的坑。
 * 所以夹取必须**可见**：保存时按边界值提交，并在提交前把具体数字讲清楚。
 *
 * ⚠️ 但它**不能拦全局保存**。`PUT /settings` 是整段写，若因为这一项禁用保存按钮，
 * 用户会因为填错刷新间隔而存不了品牌标题 —— 一个字段挡住无关字段，
 * 与坐标那条是同一类陷阱（见 canSave 的注释）。
 */
const refreshHint = computed<string>(() => {
  if (refreshOk.value) return ''
  const n = refreshNum.value
  if (!Number.isFinite(n)) {
    return (
      `请填写 ${WEATHER_REFRESH_MIN} ~ ${WEATHER_REFRESH_MAX} 之间的整数分钟数` +
      `（当前为「${refreshRaw.value.trim() || '空'}」）。` +
      `留空或填非数字会按 ${WEATHER_REFRESH_DEFAULT_MIN} 分钟提交。`
    )
  }
  // 把"会被夹成什么"算成具体数字给用户看（999 → 120），而不是含糊地说"会被夹取"
  const clamped = clamp(Math.trunc(n), WEATHER_REFRESH_MIN, WEATHER_REFRESH_MAX)
  return (
    `刷新间隔需在 ${WEATHER_REFRESH_MIN} ~ ${WEATHER_REFRESH_MAX} 分钟。` +
    `当前填 ${n}，保存时会按 ${clamped} 分钟提交，保存后输入框会显示实际生效值。` +
    `这里不拦你保存 —— 填错这一项不应该影响品牌等其它字段。`
  )
})

/** 真错误（不是数字）用红字；仅越界用警告色 —— 它是"会被调整"，不是"不能保存"。 */
const refreshHintError = computed<boolean>(() => !Number.isFinite(refreshNum.value))

/**
 * 是否允许保存。
 *
 * 只有两类问题够格**拦住全局**，因为它们是"存进去的数据就是错的"：
 *  1. `settings.loaded` —— **设置尚未成功读取过一次时禁止保存**。否则 load() 失败后
 *     抽屉里的草稿是前端默认值（品牌名兜底、坐标北京），用户只改个副标题点保存，
 *     就会把服务器上已有的品牌与坐标整段覆盖成前端默认值 —— 这是最恶劣的一类静默数据损坏。
 *  2. 坐标 —— 后端 `validateWeather` 对 lat/lon 的区间检查**不受 enabled 影响**，
 *     所以前端也不能只在开启时校验；若用户在开启状态填了 999 再把开关关掉，
 *     被隐藏的非法值一样会让保存吃 400。同理 (0,0) + 开启天气是后端明确拒绝的业务规则。
 *
 * 其余字段（刷新间隔、长度类、格式类）一律**字段级提示 + 允许保存**：
 * 一个字段填错不该挡住无关字段的保存。
 */
const canSave = computed<boolean>(
  () =>
    !settings.saving &&
    settings.loaded &&
    coordsOk.value &&
    !zeroCoordsWithWeatherOn.value,
)

/* ——————————————— 品牌预览 ——————————————— */

const previewTitle = computed<string>(() => title.value.trim() || DEFAULT_BRAND_TITLE)
const previewSubtitle = computed<string>(() => subtitle.value.trim())
/**
 * 预览用 Logo 地址。
 *
 * 草稿与已保存值一致时复用 store 的 `logoSrc`（它带"上传后缓存破坏"参数），
 * 保证预览看到的就是顶栏真正会加载的那张图；草稿被改过则直接用草稿原值。
 */
const previewLogo = computed<string>(() => {
  const raw = logoUrl.value.trim()
  if (raw === '') return ''
  return raw === settings.branding.logoUrl ? settings.logoSrc : raw
})

watch(previewLogo, () => {
  previewBroken.value = false
})

/* ——————————————— Logo ——————————————— */

const fileInput = ref<HTMLInputElement | null>(null)

function pickFile(): void {
  fileInput.value?.click()
}

async function onFilePicked(e: Event): Promise<void> {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  // 立刻清空：否则连续选同一个文件不会触发 change，"重传一次"看起来没反应
  input.value = ''
  if (!file) return
  if (!LOGO_ACCEPT_MIME.includes(file.type)) {
    setLogoHint(`仅支持 png / jpg / webp，当前文件是 ${file.type || '未知类型'}`, true)
    return
  }
  if (file.size > LOGO_MAX_BYTES) {
    setLogoHint(`Logo 不能超过 ${formatBytes(LOGO_MAX_BYTES)}，当前 ${formatBytes(file.size)}`, true)
    return
  }
  setLogoHint('')
  const url = await settings.uploadLogo(file)
  if (url) {
    logoUrl.value = url
    setLogoHint(`已上传（${formatBytes(file.size)}），保存后生效`)
  } else {
    setLogoHint(settings.error || '上传失败', true)
  }
}

async function clearLogo(): Promise<void> {
  // 本地草稿先清：即使 DELETE 失败，用户看到的也是"我要清掉它"的意图
  const had = logoUrl.value.trim().length > 0
  logoUrl.value = ''
  if (!had) {
    setLogoHint('')
    return
  }
  const ok = await settings.removeLogo()
  if (ok) setLogoHint('已清除后端 Logo，保存后生效')
  else setLogoHint(settings.error || '清除失败', true)
}

function restoreDefaultBrand(): void {
  title.value = ''
  subtitle.value = DEFAULT_BRAND_SUBTITLE
  logoUrl.value = ''
  setLogoHint(`已恢复默认（项目名留空将显示「${DEFAULT_BRAND_TITLE}」），保存后生效`)
}

/* ——————————————— 坐标：地图点选 ——————————————— */

const mapOpen = ref(false)
const mapBusy = ref(false)
const mapEl = ref<HTMLElement | null>(null)
let mapInstance: AMapMap | null = null

/** 无高德 Key 时点选按钮禁用 —— 合规红线：绝不降级到任何境外底图。 */
const mapAvailable = computed<boolean>(() => ui.amapKey.length > 0)

function destroyMap(): void {
  const m = mapInstance
  mapInstance = null
  if (!m) return
  try {
    m.destroy()
  } catch {
    // destroy 抛错不能阻断关闭流程
  }
}

async function openMapPicker(): Promise<void> {
  if (!mapAvailable.value) return
  mapOpen.value = true
  mapError.value = ''
  mapBusy.value = true
  await nextTick()
  const el = mapEl.value
  if (!el) {
    mapBusy.value = false
    mapError.value = '地图容器尚未就绪，请重试'
    return
  }
  const ns = await loadAMap(ui.amapKey, ui.amapSecurityCode, ui.amapForceWebGL)
  if (!ns) {
    mapBusy.value = false
    mapError.value = '高德地图加载失败：Key 无效或当前无外网，请手动填写坐标'
    return
  }
  destroyMap()
  // 当前坐标（WGS-84）→ GCJ-02 才能正确落到高德底图上
  const [cLon, cLat] = toGCJ02(lonNum.value, latNum.value, 'WGS-84')
  const center: [number, number] = [
    Number.isFinite(cLon) ? cLon : 116.397428,
    Number.isFinite(cLat) ? cLat : 39.90923,
  ]
  mapInstance = new ns.Map(el, {
    center,
    zoom: 12,
    viewMode: '2D',
    resizeEnable: true,
  })
  mapInstance.on?.('click', (e: unknown) => {
    const ll = (e as { lnglat?: unknown })?.lnglat as
      | { lng?: number; lat?: number; getLng?: () => number; getLat?: () => number }
      | undefined
    const lng = typeof ll?.getLng === 'function' ? ll.getLng() : ll?.lng
    const lat = typeof ll?.getLat === 'function' ? ll.getLat() : ll?.lat
    if (typeof lng !== 'number' || typeof lat !== 'number') return
    // ⚠️ 高德返回的是 GCJ-02，回填给后端必须是 WGS-84，否则偏 300~500 米
    const [wLon, wLat] = fromGCJ02(lng, lat, 'WGS-84')
    lonRaw.value = wLon.toFixed(6)
    latRaw.value = wLat.toFixed(6)
  })
  mapBusy.value = false
}

function closeMapPicker(): void {
  mapOpen.value = false
  destroyMap()
  mapError.value = ''
}

/* ——————————————— 坐标：浏览器定位 ——————————————— */

const geoBusy = ref(false)

function useBrowserLocation(): void {
  setGeoHint('')
  if (typeof navigator === 'undefined' || !navigator.geolocation) {
    setGeoHint('当前浏览器不支持定位，请手动填写坐标', true)
    return
  }
  // 非安全上下文（http 且非 localhost）下浏览器直接拒绝 geolocation，
  // 不提前说明的话用户只会看到"点了没反应"。
  if (!window.isSecureContext) {
    setGeoHint('浏览器定位仅在 HTTPS 或 localhost 下可用（当前非安全上下文），请手动填写坐标', true)
    return
  }
  geoBusy.value = true
  navigator.geolocation.getCurrentPosition(
    (pos) => {
      geoBusy.value = false
      latRaw.value = pos.coords.latitude.toFixed(6)
      lonRaw.value = pos.coords.longitude.toFixed(6)
      setGeoHint('已填入浏览器定位结果（WGS-84）')
    },
    (err) => {
      geoBusy.value = false
      setGeoHint(
        err.code === err.PERMISSION_DENIED
          ? '定位权限被拒绝：请在浏览器地址栏的站点设置里允许定位，或手动填写坐标'
          : `定位失败：${err.message}（可手动填写坐标）`,
        true,
      )
    },
    { enableHighAccuracy: false, timeout: GEOLOCATE_TIMEOUT_MS, maximumAge: 60_000 },
  )
}

/* ——————————————— 天气：立即刷新 / 上次更新 ——————————————— */

const snap = computed(() => settings.weatherSnapshot)
const lastUpdatedText = computed<string>(() =>
  snap.value?.fetchedAtMs ? formatRelative(snap.value.fetchedAtMs) : '尚未获取',
)
const weatherStatusText = computed<string>(() => {
  if (!enabled.value) return '已关闭'
  if (settings.weatherError) return `请求失败：${settings.weatherError}`
  const s = snap.value
  if (!s) return '获取中…'
  if (!s.ok) return `不可用：${s.message || s.reason || '未知原因'}`
  return s.stale ? '可用（数据可能已过期）' : '正常'
})
/** 状态是否属于"要看一眼"的级别（请求失败 / 后端明确不可用）。 */
const weatherStatusError = computed<boolean>(() => {
  if (!enabled.value) return false
  if (settings.weatherError) return true
  const s = snap.value
  return s !== null && !s.ok
})

/**
 * 草稿坐标与已保存坐标不一致。
 *
 * 「立即刷新」按**已保存**的设置拉取（GET /weather 不带 query，坐标在服务端）——
 * 若偷偷把草稿回写进 store 再刷新，用户点「取消」后草稿会残留在 store 里继续影响轮询，
 * 这是一处很难发现的状态泄漏。所以这里改成"不一致就明确告知"，而不是悄悄改行为。
 */
const coordsDirty = computed<boolean>(
  () =>
    enabled.value &&
    (Math.abs(latNum.value - settings.weather.lat) > 1e-6 ||
      Math.abs(lonNum.value - settings.weather.lon) > 1e-6),
)

async function refreshNow(): Promise<void> {
  await settings.loadWeather()
}

/* ——————————————— 关于（只读排障信息） ——————————————— */

/**
 * 构建标识：`v1.0.0 · 50e7f16-dirty`。
 *
 * `-dirty` 后缀由 Makefile 在"工作区有未提交改动"时追加，是判断
 * "我现在跑的到底是不是干净构建"的唯一线索 —— **绝不能截断或隐藏**，
 * 否则排障时会拿着一个看起来正常的 hash 去对不上的代码里找问题。
 * 两者任一缺失就只显示存在的那一个，都缺失则整行不渲染。
 */
const buildLabel = computed<string>(() => {
  const r = ui.runtime
  if (!r) return ''
  const version = (r.version ?? '').trim()
  const commit = (r.commit ?? '').trim()
  return [version, commit].filter((s) => s.length > 0).join(' · ')
})

/**
 * 「关于」= 只读排障信息。
 *
 * 两条硬规则（team-lead 裁定）：
 *   1. **值为空的那一行整行不渲染**，不是渲染成「数据目录：」这种只有标签的空行。
 *      `dataDir` 后端**明确不下发**（/system/runtime 无鉴权，暴露服务端文件系统路径
 *      等于把部署结构告诉任何能打开页面的人；"跑的哪一版"用 version+commit 已足够定位），
 *      所以那一行必须恒不出现 —— 但类型上保留 `dataDir?`，将来有别的用途时自动生效。
 *   2. 三项全空 → 整组隐藏，不显示空标题。
 */
const aboutRows = computed<{ label: string; value: string }[]>(() => {
  const r = ui.runtime
  if (!r) return []
  const rows: { label: string; value: string }[] = []
  if (buildLabel.value) rows.push({ label: '构建', value: buildLabel.value })
  const dataDir = (r.dataDir ?? '').trim()
  if (dataDir) rows.push({ label: '数据目录', value: dataDir })
  if (rows.length === 0) return []
  rows.push({ label: '协议版本', value: String(r.protocolVersion) })
  return rows
})
const showAbout = computed<boolean>(() => aboutRows.value.length > 0)

/* ——————————————— 保存 / 关闭 ——————————————— */

function buildPayload(): SettingsPayload {
  return {
    branding: {
      title: title.value.trim(),
      subtitle: subtitle.value.trim(),
      logoUrl: logoUrl.value.trim(),
    },
    weather: {
      enabled: enabled.value,
      provider: settings.weather.provider || WEATHER_PROVIDER_DEFAULT,
      lat: latNum.value,
      lon: lonNum.value,
      label: label.value.trim(),
      // 留空/填非数字时 refreshNum 是 NaN，而 NaN 会一路穿过 clamp 仍是 NaN，
      // JSON 序列化成 null，Go 侧 int 收到 null 会留零值 → 被后端夹成 1 分钟。
      // 取值统一走 refreshEffective：它是"出网告知里的那个 N"的同一来源。
      refreshMin: refreshEffective.value,
    },
  }
}

async function save(): Promise<void> {
  if (!canSave.value) return
  if (!title.value.trim()) {
    // 清空项目名是**允许**的（回落默认名），但必须明确告知，不能悄悄改
    ui.info(`项目名留空，将显示默认名称「${DEFAULT_BRAND_TITLE}」`)
  }
  const ok = await settings.save(buildPayload())
  if (!ok) return // 错误已写进 settings.error，画在抽屉里
  ui.success('全局设置已保存')
  emit('close')
}

function close(): void {
  closeMapPicker()
  emit('close')
}

/* Esc 关闭（与卡片抽屉一致的直觉；输入框内不拦，避免打断输入） */
function onKeyDown(e: KeyboardEvent): void {
  if (!props.open || e.key !== 'Escape') return
  if (mapOpen.value) {
    closeMapPicker()
    return
  }
  const t = e.target
  if (t instanceof HTMLElement && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA')) return
  close()
}

watch(
  () => props.open,
  (isOpen) => {
    if (isOpen) window.addEventListener('keydown', onKeyDown)
    else {
      window.removeEventListener('keydown', onKeyDown)
      closeMapPicker()
    }
  },
  { immediate: true },
)

onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKeyDown)
  destroyMap()
})
</script>

<template>
  <template v-if="open">
    <div class="ma-set__mask" @click="close"></div>
    <aside class="ma-set" :style="{ width: `${SETTINGS_DRAWER_WIDTH}px` }">
      <header class="ma-set__head">
        <span class="ma-set__title">全局设置</span>
        <button class="ma-set__close" title="关闭（Esc）" @click="close">✕</button>
      </header>

      <div class="ma-set__body">
        <!-- 错误横幅：保存/上传/读取的失败原因一律画在这里，不只用 toast -->
        <div v-if="settings.error" class="ma-set__err">
          <span class="ma-set__err-text">{{ settings.error }}</span>
          <button class="ma-set__err-x" title="忽略" @click="settings.error = ''">✕</button>
        </div>
        <div v-else-if="settings.loading" class="ma-set__hint">正在读取全局设置…</div>

        <!-- ——— 品牌 ——— -->
        <div class="ma-set__sep">品牌</div>

        <div class="ma-set__field">
          <label class="ma-set__label">项目名</label>
          <input
            v-model="title"
            class="ma-set__input"
            :maxlength="BRAND_TITLE_MAX"
            placeholder="留空则显示 MonitorAll"
          />
        </div>
        <p class="ma-set__note">
          留空会回落为「{{ DEFAULT_BRAND_TITLE }}」；最多 {{ BRAND_TITLE_MAX }} 字。
        </p>

        <div class="ma-set__field">
          <label class="ma-set__label">副标题</label>
          <input
            v-model="subtitle"
            class="ma-set__input"
            :maxlength="BRAND_SUBTITLE_MAX"
            placeholder="可留空"
          />
        </div>

        <div class="ma-set__sep ma-set__sep--sub">Logo</div>
        <div class="ma-set__field">
          <label class="ma-set__label">图片地址</label>
          <input v-model="logoUrl" class="ma-set__input" placeholder="留空则显示项目名文字" />
        </div>
        <div class="ma-set__row">
          <button class="ma-set__btn" :disabled="settings.uploading" @click="pickFile">
            {{ settings.uploading ? '上传中…' : '上传图片' }}
          </button>
          <button class="ma-set__btn" :disabled="settings.uploading" @click="clearLogo">清除 Logo</button>
          <button class="ma-set__btn" @click="restoreDefaultBrand">恢复默认</button>
        </div>
        <input
          ref="fileInput"
          class="ma-set__file"
          type="file"
          :accept="LOGO_ACCEPT_ATTR"
          @change="onFilePicked"
        />
        <p class="ma-set__note">
          仅支持 png / jpg / webp，不超过 {{ formatBytes(LOGO_MAX_BYTES) }}。
        </p>
        <p v-if="logoHint" class="ma-set__note" :class="{ 'is-error': logoHintError }">
          {{ logoHint }}
        </p>

        <div class="ma-set__logo-preview">
          <img
            v-if="previewLogo && !previewBroken"
            :src="previewLogo"
            class="ma-set__logo-img"
            alt="当前 Logo"
            @error="previewBroken = true"
          />
          <span v-else class="ma-set__logo-empty">{{ previewLogo ? '图片加载失败，将显示文字' : '未设置 Logo' }}</span>
        </div>

        <div class="ma-set__sep ma-set__sep--sub">顶栏预览</div>
        <div class="ma-set__preview">
          <img
            v-if="previewLogo && !previewBroken"
            :src="previewLogo"
            class="ma-set__preview-img"
            :alt="previewTitle"
            @error="previewBroken = true"
          />
          <span v-else class="ma-set__preview-title">{{ previewTitle }}</span>
          <span v-if="previewSubtitle" class="ma-set__preview-sub">{{ previewSubtitle }}</span>
        </div>

        <!-- ——— 天气 ——— -->
        <div class="ma-set__sep">天气</div>

        <div class="ma-set__field">
          <label class="ma-set__label">显示天气</label>
          <button class="ma-set__toggle" :class="{ 'is-on': enabled }" @click="enabled = !enabled">
            {{ enabled ? '开' : '关' }}
          </button>
        </div>

        <p v-if="enabled" class="ma-set__warn">
          ⚠ 开启后，服务器将向 <code>api.open-meteo.com</code> 每
          {{ refreshEffective }} 分钟发送一次请求，
          内容为经纬度，不含其它数据。
        </p>
        <p v-else class="ma-set__note">
          关闭时顶栏不显示天气挂件，服务器也不会向外部发起任何天气请求。
        </p>

        <!--
          坐标/显示名/间隔**不随开关隐藏**。
          原因：后端的 lat/lon 区间校验不受 enabled 影响 —— 若把字段藏起来，
          用户可以在开启状态填入非法值、再关掉开关，此时非法值不可见却仍会让保存吃 400。
          字段常显，非法状态就永远可见、可解释。
        -->
        <div class="ma-set__field">
          <label class="ma-set__label">纬度</label>
          <input v-model="latRaw" class="ma-set__input" inputmode="decimal" placeholder="如 22.5431" />
        </div>
        <div class="ma-set__field">
          <label class="ma-set__label">经度</label>
          <input v-model="lonRaw" class="ma-set__input" inputmode="decimal" placeholder="如 114.0579" />
        </div>
        <p v-if="coordError" class="ma-set__note is-error">{{ coordError }}</p>
        <p v-else class="ma-set__note">
          当前坐标 {{ formatNumber(latNum, 4) }}, {{ formatNumber(lonNum, 4) }}（WGS-84）
        </p>

        <div class="ma-set__row">
          <button
            class="ma-set__btn"
            :disabled="!mapAvailable"
            :title="mapAvailable ? '在地图上点选取点（自动转 WGS-84）' : '未配置高德 Key，请手动填写坐标'"
            @click="openMapPicker"
          >
            在地图上点选
          </button>
          <button class="ma-set__btn" :disabled="geoBusy" @click="useBrowserLocation">
            {{ geoBusy ? '定位中…' : '用浏览器定位' }}
          </button>
        </div>
        <p v-if="!mapAvailable" class="ma-set__note is-error">
          未配置高德 Key，「地图点选」不可用，请手动填写坐标。
        </p>
        <p v-if="geoHint" class="ma-set__note" :class="{ 'is-error': geoHintError }">
          {{ geoHint }}
        </p>

        <div class="ma-set__field">
          <label class="ma-set__label">显示名</label>
          <input
            v-model="label"
            class="ma-set__input"
            :maxlength="WEATHER_LABEL_MAX"
            placeholder="留空则显示「当前位置」"
          />
        </div>

        <div class="ma-set__field">
          <label class="ma-set__label">刷新间隔</label>
          <input
            v-model="refreshRaw"
            class="ma-set__input"
            type="number"
            :min="WEATHER_REFRESH_MIN"
            :max="WEATHER_REFRESH_MAX"
            step="1"
          />
          <span class="ma-set__unit">分钟</span>
        </div>
        <p v-if="refreshHint" class="ma-set__note" :class="{ 'is-error': refreshHintError, 'is-warn': !refreshHintError }">
          {{ refreshHint }}
        </p>
        <p v-else class="ma-set__note">范围 {{ WEATHER_REFRESH_MIN }} ~ {{ WEATHER_REFRESH_MAX }} 分钟。</p>

        <div class="ma-set__field">
          <label class="ma-set__label">数据来源</label>
          <span class="ma-set__static">{{ settings.weather.provider || WEATHER_PROVIDER_DEFAULT }}</span>
        </div>

        <template v-if="enabled">
          <div class="ma-set__row">
            <button class="ma-set__btn" @click="refreshNow">立即刷新</button>
            <span class="ma-set__static">上次更新：{{ lastUpdatedText }}</span>
          </div>
          <p class="ma-set__note" :class="{ 'is-error': weatherStatusError }">
            状态：{{ weatherStatusText }}
          </p>
          <p v-if="coordsDirty" class="ma-set__note is-error">
            坐标有未保存的改动：「立即刷新」按已保存的设置拉取，请先点「保存」。
          </p>
        </template>

        <!-- ——— 关于 ——— -->
        <template v-if="showAbout">
          <div class="ma-set__sep">关于</div>
          <div v-for="row in aboutRows" :key="row.label" class="ma-set__field">
            <label class="ma-set__label">{{ row.label }}</label>
            <span class="ma-set__static ma-set__static--mono">{{ row.value }}</span>
          </div>
        </template>
      </div>

      <footer class="ma-set__foot">
        <span v-if="!settings.loaded" class="ma-set__foot-hint">
          未成功读取服务器上的当前设置，已禁用保存以避免覆盖现有配置。请关闭抽屉后重新打开以重试。
        </span>
        <span v-else-if="!canSave" class="ma-set__foot-hint">请先修正上面的错误</span>
        <div class="ma-set__grow"></div>
        <button class="ma-set__btn" @click="close">取消</button>
        <button class="ma-set__btn ma-set__btn--primary" :disabled="!canSave" @click="save">
          {{ settings.saving ? '保存中…' : '保存' }}
        </button>
      </footer>

      <!-- 地图点选：覆盖在抽屉之上（抽屉本身已 fixed，这里再抬一层） -->
      <div v-if="mapOpen" class="ma-set__map">
        <header class="ma-set__map-head">
          <span>点击地图取点（自动换算为 WGS-84）</span>
          <button class="ma-set__close" title="关闭" @click="closeMapPicker">✕</button>
        </header>
        <div ref="mapEl" class="ma-set__map-canvas"></div>
        <p v-if="mapError" class="ma-set__note is-error">{{ mapError }}</p>
        <p v-else-if="mapBusy" class="ma-set__note">正在加载地图…</p>
        <p v-else class="ma-set__note">当前：{{ formatNumber(latNum, 6) }}, {{ formatNumber(lonNum, 6) }}</p>
      </div>
    </aside>
  </template>
</template>

<style scoped>
/* 全局设置：铺满视口高度（fixed），不依赖任何祖先的定位上下文 */
.ma-set {
  position: fixed;
  top: 0;
  right: 0;
  bottom: 0;
  z-index: 400; /* 高于卡片抽屉(300)与地图徽标(200) */
  display: flex;
  flex-direction: column;
  background: var(--ma-bg-elevated);
  border-left: 1px solid var(--ma-border);
  box-shadow: var(--ma-shadow-pop);
}

.ma-set__mask {
  position: fixed;
  inset: 0;
  z-index: 399;
  background: rgba(0, 0, 0, 0.34);
}

.ma-set__head {
  display: flex;
  align-items: center;
  height: 40px;
  padding: 0 12px;
  border-bottom: 1px solid var(--ma-border);
}

.ma-set__title {
  flex: 1 1 auto;
  font-size: var(--ma-font-lg);
}

.ma-set__close {
  border: none;
  background: none;
  color: var(--ma-text-3);
  cursor: pointer;
}

/*
 * 滚动主体：与 CardConfigDrawer 同一套规则 ——
 * flex-basis:0 + 子项不收缩，否则内容超高时容器会被"压扁"而不是出现滚动条。
 */
.ma-set__body {
  flex: 1 1 0;
  min-height: 0;
  overflow-y: auto;
  overflow-x: hidden;
  scrollbar-gutter: stable;
  overscroll-behavior: contain;
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.ma-set__body > * {
  flex-shrink: 0;
}

.ma-set__sep {
  margin-top: 6px;
  padding-top: 8px;
  border-top: 1px solid var(--ma-border);
  font-size: var(--ma-font-md);
  color: var(--ma-text-1);
}

.ma-set__sep--sub {
  font-size: var(--ma-font-sm);
  color: var(--ma-text-2);
}

.ma-set__field {
  display: flex;
  align-items: center;
  gap: 8px;
}

.ma-set__label {
  flex: none;
  width: 68px;
  font-size: var(--ma-font-sm);
  color: var(--ma-text-2);
}

.ma-set__input {
  flex: 1 1 auto;
  min-width: 0;
  height: 26px;
  padding: 0 6px;
  border: 1px solid var(--ma-border);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-base);
  color: var(--ma-text-1);
  font-size: var(--ma-font-sm);
}

.ma-set__input:focus {
  outline: none;
  border-color: var(--ma-accent);
}

.ma-set__unit {
  flex: none;
  font-size: var(--ma-font-xs);
  color: var(--ma-text-3);
}

.ma-set__static {
  font-size: var(--ma-font-sm);
  color: var(--ma-text-2);
}

.ma-set__static--mono {
  font-family: 'JetBrains Mono', 'SFMono-Regular', Menlo, Consolas, monospace;
  word-break: break-all;
}

.ma-set__row {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.ma-set__btn {
  padding: 3px 12px;
  border: 1px solid var(--ma-border-strong);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-base);
  color: var(--ma-text-1);
  cursor: pointer;
  font-size: var(--ma-font-sm);
}

.ma-set__btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}

.ma-set__btn--primary {
  border-color: var(--ma-accent);
  background: var(--ma-accent-dim);
  color: var(--ma-accent);
}

.ma-set__toggle {
  padding: 2px 12px;
  border: 1px solid var(--ma-border-strong);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-base);
  color: var(--ma-text-2);
  cursor: pointer;
  font-size: var(--ma-font-xs);
}

.ma-set__toggle.is-on {
  border-color: var(--ma-accent);
  color: var(--ma-accent);
}

.ma-set__note {
  margin: 0;
  font-size: var(--ma-font-xs);
  color: var(--ma-text-3);
  line-height: 1.5;
}

.ma-set__note.is-error {
  color: var(--ma-status-error);
}

/* 警告色：值会被调整但**不阻止保存**，与 is-error（真错误、阻止保存）区分开。
   同色系与下方「出网告知」的 .ma-set__warn 保持一致。 */
.ma-set__note.is-warn {
  color: var(--ma-status-reconnecting);
}

/* 出网告知：必须显眼，不能混在普通说明里 */
.ma-set__warn {
  margin: 0;
  padding: 6px 8px;
  border: 1px solid var(--ma-status-reconnecting);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-base);
  color: var(--ma-status-reconnecting);
  font-size: var(--ma-font-xs);
  line-height: 1.55;
}

.ma-set__hint {
  font-size: var(--ma-font-xs);
  color: var(--ma-text-3);
}

.ma-set__err {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 6px 8px;
  border: 1px solid var(--ma-status-error);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-base);
}

.ma-set__err-text {
  flex: 1 1 auto;
  color: var(--ma-status-error);
  font-size: var(--ma-font-sm);
  line-height: 1.5;
  word-break: break-all;
}

.ma-set__err-x {
  flex: none;
  border: none;
  background: none;
  color: var(--ma-text-3);
  cursor: pointer;
}

.ma-set__file {
  display: none;
}

.ma-set__logo-preview,
.ma-set__preview {
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 40px;
  padding: 6px 8px;
  border: 1px dashed var(--ma-border-strong);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-base);
}

.ma-set__logo-img {
  max-height: 32px;
  max-width: 160px;
  object-fit: contain;
}

.ma-set__logo-empty,
.ma-set__preview-sub {
  font-size: var(--ma-font-xs);
  color: var(--ma-text-3);
}

.ma-set__preview-img {
  max-height: 20px;
  max-width: 120px;
  object-fit: contain;
}

.ma-set__preview-title {
  font-size: 15px;
  font-weight: 700;
  color: var(--ma-accent);
  letter-spacing: 0.4px;
}

.ma-set__foot {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 12px;
  border-top: 1px solid var(--ma-border);
}

.ma-set__grow {
  flex: 1 1 auto;
}

.ma-set__foot-hint {
  font-size: var(--ma-font-xs);
  color: var(--ma-status-error);
}

/* 地图点选层 */
.ma-set__map {
  position: absolute;
  inset: 0;
  z-index: 10;
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 10px 12px;
  background: var(--ma-bg-elevated);
}

.ma-set__map-head {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: var(--ma-font-sm);
  color: var(--ma-text-2);
}

.ma-set__map-head span {
  flex: 1 1 auto;
}

.ma-set__map-canvas {
  flex: 1 1 auto;
  min-height: 0;
  border: 1px solid var(--ma-border);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-base);
}
</style>
