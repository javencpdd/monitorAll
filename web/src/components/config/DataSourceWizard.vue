<script setup lang="ts">
/**
 * 数据源新建向导（DS-01 ~ DS-07）：Step1 选类型 → Step2 填参数 + 测试连接 → 落库。
 *
 * 约定：
 *   - ROS 必须选 ROS1 / ROS2（切换版本后表单字段不变，决策 D5）
 *   - RTMP / WS / HTTP URL 实时校验，非法立即红字且禁用保存（ARCH 40002）
 *   - 测试连接 ≤5s 返回；失败展示原因 + 修复建议并停留在 Step2
 *   - PUT 时收到 '***' 表示保持原值——本向导为新建，不涉及；此处仅展示脱敏占位说明
 */
import { computed, ref, watch } from 'vue'
import type { DataSourceKind, PayloadType, Protocol, TestResult } from '@/types'
import { useDatasourceStore } from '@/stores/datasource'
import { useUiStore } from '@/stores/ui'
import { AppError } from '@/api/client'
import {
  STREAM_SCHEME_RE,
  HTTP_URL_RE,
  KIND_TEXT,
  MEDIA_MTX_PATH_RE,
  normalizeMediaMtxPath,
  PAYLOAD_TYPE_TEXT,
  STREAM_URL_RE,
  TEST_CONNECT_TIMEOUT_MS,
  VIDEO_PROTOCOL_LABEL,
  WS_URL_RE,
} from '@/utils/constants'
import { readPublishUrls } from '@/utils/publishUrls'
import { copyText } from '@/utils/clipboard'
import { maskUrlCredentials } from '@/utils/redact'

const datasourceStore = useDatasourceStore()
const ui = useUiStore()

const open = computed<boolean>(() => ui.sourceWizard.open)
const step = ref<1 | 2>(1)

const kind = ref<DataSourceKind>('ros')
const name = ref<string>('')
const rosVersion = ref<'ros1' | 'ros2'>('ros2')
/** 流地址（rtmp / rtmps / rtsp / rtsps 均可）。 */
const rtmpUrl = ref<string>('')
const preferredProtocol = ref<'webrtc' | 'hls' | 'flv'>('webrtc')
const audio = ref<boolean>(false)
/** 接入模式：pull 拉远端流（默认）| publish 接收远端 WHIP / RTMP 推流。 */
const videoMode = ref<'pull' | 'publish'>('pull')
/** RTSP 拉流传输方式，默认 tcp。 */
const rtspTransport = ref<'tcp' | 'udp' | 'automatic'>('tcp')
/** MediaMTX 路径；publish 模式必填。 */
const mediaMtxPath = ref<string>('')
const bridgeUrl = ref<string>('ws://172.31.68.227:9090')
const domainId = ref<number>(0)
const topicsText = ref<string>('')
const httpUrl = ref<string>('http://')
const httpMethod = ref<'GET' | 'POST'>('GET')
const intervalMs = ref<number>(1000)
const jsonPath = ref<string>('')
const timeoutMs = ref<number>(3000)
const httpBody = ref<string>('')

/**
 * 从误填的值里尽量救出一个合法路径。
 *
 * ⚠️ 与后端 `server/internal/media/mediamtx.go` 的 `suggestMediaMTXPath` **同逻辑**：
 * 两边给出的"该改成什么"必须是同一个值，否则用户照前端提示改完仍被后端拒
 * （又一次"同一条数据两套规则"）。步骤对齐后端：
 *   ① 含 `://` → 取主机后的路径部分；② 去掉 `?` / `#` 之后的部分；
 *   ③ trim 首尾 `/`；④ 折叠连续 `//`；⑤ 仍不合法则返回 `''`。
 *
 * 返回 `''` 而不是回落示例值：让调用方决定提示里展示什么，
 * 也避免把非法值（或 URL 清空时）原样推荐回去。
 */
function suggestMediaMTXPath(raw: string): string {
  let s = raw.trim()
  if (s.length === 0) return ''
  // 完整地址（rtmp://host:1936/live/x、rtsp://user:pw@host/.../101）→ 取主机后的路径部分
  if (s.includes('://')) {
    const rest = s.slice(s.indexOf('://') + 3)
    const j = rest.indexOf('/')
    if (j >= 0) s = rest.slice(j + 1)
  }
  // 去掉查询串与片段
  const q = s.search(/[?#]/)
  if (q >= 0) s = s.slice(0, q)
  // trim 首尾斜杠 → 折叠连续斜杠（live//cam → live/cam）
  s = s.replace(/^\/+|\/+$/g, '')
  s = s.replace(/\/{2,}/g, '/')
  return MEDIA_MTX_PATH_RE.test(s) ? s : ''
}

/**
 * MediaMTX 路径格式校验（白名单，与后端 `mediaMTXPathRe` 同源）。
 *
 * 事故背景（用户真实踩到）：把**整条 RTMP 地址**填进了「MediaMTX 路径」
 * （`rtmp://172.31.68.227:1936/live/m20pro-f`，正确值只是 `live/m20pro-f`）。
 * 后果是通道名变成整条地址、播放地址拼成 `/rtmp://…/index.m3u8`，
 * MediaMTX 返回 500 `invalid path name`，而前端只报「HLS 协商超时」——
 * 提示完全误导，用户会去查网络与 MediaMTX 是否在线，而根因是路径名非法。
 *
 * 因此用**白名单**兜住整类字符，并对能救回的值给出**可执行的修正建议**。
 * 报错文案与后端 `ValidateMediaMTXPath` 保持一致，用户从前端就看到该改成什么。
 *
 * ⚠️ 先归一化（去首尾空白与首尾斜杠）再校验：`/live/cam1` 是自然手误，
 * 归一化成 `live/cam1` 直接放行；后端给通道名做的也是同一个 Trim
 * （`source.go:192`）。若跳过归一化，前导斜杠会骗过前端、被后端 40001 拒。
 */
function pathError(path: string): string {
  const v = normalizeMediaMtxPath(path)
  if (v.length === 0) return ''
  if (MEDIA_MTX_PATH_RE.test(v)) return ''
  const suggested = suggestMediaMTXPath(v)
  if (suggested.length > 0) {
    return `MediaMTX 路径只需流名（如 live/cam1），不能填完整地址；当前值 "${v}" 请改为 "${suggested}"`
  }
  return 'MediaMTX 路径只能是字母数字、点、下划线、减号与斜杠组成的分段（如 live/cam1），不能带 scheme、? #、空格或连续斜杠'
}

/**
 * 归一化后的路径 —— **校验、提交、摘要三处共用这一个值**，
 * 保证"用户看到的内容"与"真正发出去的请求"与"后端存进库的值"三者一致。
 */
const mtxPathNorm = computed<string>(() => normalizeMediaMtxPath(mediaMtxPath.value))

/** 当前手填路径的格式错误（空 = 合法或为空）。 */
const mtxPathError = computed<string>(() => pathError(mediaMtxPath.value))

/**
 * MediaMTX 路径留空时，自动从流地址提取。
 *
 * 「流地址」和「MediaMTX 路径」并不是两份信息：`rtmp://host:1936/live/lite3`
 * 的路径部分就是 `live/lite3`。让用户填两遍只会让人怀疑"是不是重复了"——
 * 这个怀疑是对的。所以：手填优先，留空则自动提取，两个模式（拉流/接收推流）都生效。
 *
 * ⚠️ 关键：只有"非空**且合法**"才保留手填值。
 * 旧写法是"非空就不覆盖"，结果是：用户一旦误填了整条地址（非法值），
 * 它就被永久保护住 —— 之后无论怎么改流地址都纠正不过来，
 * 而这个非法值正是 MediaMTX 报 `invalid path name` 的元凶。
 */
watch(rtmpUrl, (url) => {
  if (kind.value !== 'video') return
  // 「非空且合法」才保留手填值；非法值（如误填整条地址）必须能被自动提取覆盖，
  // 否则它会被永久保护住，之后无论怎么改流地址都纠正不过来。
  if (mtxPathNorm.value.length > 0 && mtxPathError.value === '') return
  // 与 `pathError` 的建议值同源（同一个 `suggestMediaMTXPath`）：
  // 自动提取与"该改成什么"共用一套逻辑，不会出现两个不同的结果。
  const suggested = suggestMediaMTXPath(url.trim())
  if (suggested.length > 0) mediaMtxPath.value = suggested
})

const testing = ref(false)
const creating = ref(false)
const testResult = ref<TestResult | undefined>(undefined)
/** publish 模式创建成功后回显的推流地址（WHIP / RTMP）。 */
const publishUrls = ref<{ whip: string; rtmp: string }>({ whip: '', rtmp: '' })
const showPublish = ref<boolean>(false)

const KINDS: { value: DataSourceKind; label: string; desc: string }[] = [
  { value: 'video', label: '视频流（RTMP / RTSP）', desc: 'RTMP 推流或 RTSP 摄像头，经 MediaMTX 转封装为 WebRTC/HLS' },
  { value: 'ros', label: 'ROS 话题', desc: '经 rosbridge 订阅 ROS1 / ROS2 话题' },
  { value: 'http', label: 'HTTP 轮询接口', desc: '按间隔轮询自建遥测服务并提取字段' },
]

/** 从流地址解析出的协议族：rtsp 系 / rtmp 系 / 未识别。 */
const streamFamily = computed<'rtsp' | 'rtmp' | ''>(() => {
  const m = STREAM_SCHEME_RE.exec(rtmpUrl.value.trim())
  if (!m) return ''
  return m[1].toLowerCase().startsWith('rtsp') ? 'rtsp' : 'rtmp'
})

const isPublish = computed<boolean>(() => kind.value === 'video' && videoMode.value === 'publish')

const protocol = computed<Protocol>(() => {
  if (kind.value === 'video') return streamFamily.value === 'rtsp' ? 'rtsp' : 'rtmp'
  if (kind.value === 'ros') return rosVersion.value === 'ros1' ? 'ros1' : 'ros2'
  return 'http-poll'
})

/** 实时 URL 校验：非法立即红字。 */
const urlError = computed<string>(() => {
  if (kind.value === 'video') {
    const v = rtmpUrl.value.trim()
    if (v.length === 0) return ''
    return STREAM_URL_RE.test(v)
      ? ''
      : '流地址格式应为 rtmp://host:1936/live/x 或 rtsp://user:pass@host:554/Streaming/Channels/101'
  }
  if (kind.value === 'ros') {
    if (bridgeUrl.value.length === 0) return ''
    return WS_URL_RE.test(bridgeUrl.value.trim()) ? '' : 'rosbridge 地址应为 ws://host:9090'
  }
  if (httpUrl.value.length === 0) return ''
  return HTTP_URL_RE.test(httpUrl.value.trim()) ? '' : '地址应以 http:// 或 https:// 开头'
})

const formError = computed<string>(() => {
  if (name.value.trim().length === 0) return '请填写数据源名称'
  if (kind.value === 'video') {
    // publish 模式由远端推流进来，没有可拉的源地址，路径才是必填项
    if (isPublish.value) {
      if (mediaMtxPath.value.trim().length === 0) return '接收推流模式必须填写 MediaMTX 路径，如 live/whip1'
    } else if (rtmpUrl.value.trim().length === 0) {
      return '请填写流地址（rtmp:// 或 rtsp://）'
    }
    // 路径格式（白名单）：非空才校验。非法值必须拦在创建之前，
    // 否则落库后 MediaMTX 报 invalid path name，而前端只显示「HLS 协商超时」。
    if (mtxPathError.value) return mtxPathError.value
  }
  if (kind.value === 'ros' && bridgeUrl.value.trim().length === 0) return '请填写 rosbridge 地址'
  if (kind.value === 'http' && httpUrl.value.trim().length === 0) return '请填写接口地址'
  if (urlError.value) return urlError.value
  return ''
})

const connParams = computed<Record<string, unknown>>(() => {
  if (kind.value === 'video') {
    const url = rtmpUrl.value.trim()
    const params: Record<string, unknown> = {
      // ⚠️ 推流模式（publish）下流地址**完全不参与行为**，绝不能原样落库：
      // 否则会留下"推流模式 + 一条拉流地址"这种语义矛盾的配置（用户真实踩到过 ——
      // 他把外部 SRS 的拉流地址填进了推流模式，库里就多了一条误导后人的脏配置）。
      // 因此这里**条件展开**：推流模式或地址为空时不下发该键。
      // 注意：输入框里的值**保留、不主动清空**（用户来回切换 pull/publish 时不丢数据），
      // 只是不提交 —— "填了但不生效"与"看不到就丢了"是两回事。
      ...(isPublish.value || url === '' ? {} : { rtmpUrl: url }),
      preferredProtocol: preferredProtocol.value,
      audio: audio.value,
      mode: videoMode.value,
    }
    if (mtxPathNorm.value.length > 0) params.mediaMtxPath = mtxPathNorm.value
    // RTSP 摄像头默认走 TCP 更稳（UDP 易花屏/连不上），仅 RTSP 源下发该字段
    if (streamFamily.value === 'rtsp' && !isPublish.value) params.rtspTransport = rtspTransport.value
    return params
  }
  if (kind.value === 'ros') {
    const topics = topicsText.value
      .split(/[\n,]/)
      .map((t) => t.trim())
      .filter((t) => t.length > 0)
    return {
      version: rosVersion.value,
      bridgeUrl: bridgeUrl.value.trim(),
      ...(rosVersion.value === 'ros2' ? { domainId: domainId.value } : {}),
      ...(topics.length > 0 ? { topics } : {}),
    }
  }
  const params: Record<string, unknown> = {
    url: httpUrl.value.trim(),
    method: httpMethod.value,
    intervalMs: clampInterval(intervalMs.value),
    timeoutMs: timeoutMs.value,
    insecureTls: false,
  }
  if (jsonPath.value.trim().length > 0) params.jsonPath = jsonPath.value.trim()
  if (httpMethod.value === 'POST' && httpBody.value.trim().length > 0) params.body = httpBody.value
  return params
})

function clampInterval(value: number): number {
  const n = Number.isFinite(value) ? Math.trunc(value) : 1000
  return Math.max(100, n)
}

/** 整数展示（`v-model.number` 在清空时会回填空串，`Number.isFinite` 兜住）。 */
function intText(value: number, unit = ''): string {
  return Number.isFinite(value) ? `${Math.trunc(value)}${unit}` : '—'
}

interface SummaryRow {
  label: string
  value: string
  mono?: boolean
}

/**
 * Step 2 只读摘要的行数据。
 *
 * 字段集与只读详情抽屉（`DataSourceDetailDrawer`）保持一致 —— 两处都在回答同一个问题
 * "我到底配了什么"。**刻意不抽公共组件**：那份抽屉刚验收过，为 DRY 去重构它的风险
 * 大于收益；一份"标签 + 值"的行结构重复是划算的（team-lead 明确要求别动抽屉）。
 *
 * 值来源是**草稿 refs**（即马上要提交的内容），不是已落库的 `connParams` ——
 * 摘要必须反映"你即将创建的东西"，而不是"你以前建过的东西"。
 */
const summaryRows = computed<SummaryRow[]>(() => {
  const rows: SummaryRow[] = [
    { label: '类型', value: KIND_TEXT[kind.value] },
    { label: '名称', value: name.value.trim() || '（未填）' },
  ]
  if (kind.value === 'video') {
    rows.push({
      label: '接入模式',
      value: isPublish.value ? '接收推流（远端 WHIP / RTMP 推给我）' : '拉流（我主动去拉远端流）',
    })
    if (isPublish.value) {
      // 与"推流模式不显示流地址"一致：摘要里也不给一条不参与行为的地址
      rows.push({ label: '流地址', value: '接收推流 · 请使用下方推流地址' })
    } else {
      rows.push({
        label: '流地址',
        value: rtmpUrl.value.trim() ? maskUrlCredentials(rtmpUrl.value.trim()) : '（未填）',
        mono: true,
      })
    }
    rows.push({
      label: 'MediaMTX 路径',
      value: mtxPathNorm.value || '未填 · 将由后端从流地址自动解析',
      mono: true,
    })
    rows.push({ label: '首选协议', value: VIDEO_PROTOCOL_LABEL[preferredProtocol.value] })
    rows.push({ label: '携带音频', value: audio.value ? '是' : '否' })
    if (streamFamily.value === 'rtsp' && !isPublish.value) {
      rows.push({
        label: 'RTSP 传输',
        value: rtspTransport.value === 'udp' ? 'UDP' : rtspTransport.value === 'automatic' ? '自动协商' : 'TCP',
      })
    }
  } else if (kind.value === 'ros') {
    rows.push({ label: '版本', value: rosVersion.value === 'ros1' ? 'ROS1' : 'ROS2' })
    rows.push({ label: 'rosbridge 地址', value: bridgeUrl.value.trim() || '（未填）', mono: true })
    if (rosVersion.value === 'ros2') rows.push({ label: 'Domain ID', value: intText(domainId.value) })
    const topics = topicsText.value
      .split(/[\n,]/)
      .map((t) => t.trim())
      .filter((t) => t.length > 0)
    rows.push({ label: '话题', value: topics.length > 0 ? topics.join('、') : '（未填）', mono: true })
  } else {
    rows.push({
      label: '接口地址',
      value: httpUrl.value.trim() ? maskUrlCredentials(httpUrl.value.trim()) : '（未填）',
      mono: true,
    })
    rows.push({ label: '方法', value: httpMethod.value })
    rows.push({ label: '轮询间隔', value: intText(clampInterval(intervalMs.value), ' ms') })
    rows.push({ label: '超时', value: intText(timeoutMs.value, ' ms') })
    rows.push({ label: 'JSONPath', value: jsonPath.value.trim() || '（留空：取整个响应）', mono: true })
    if (httpMethod.value === 'POST') {
      rows.push({ label: '请求体', value: httpBody.value.trim() || '—', mono: true })
    }
  }
  return rows
})

async function test(): Promise<void> {
  if (formError.value) {
    ui.warning(formError.value)
    return
  }
  testing.value = true
  testResult.value = undefined
  try {
    const result = await Promise.race([
      datasourceStore.testSource({
        name: name.value.trim(),
        kind: kind.value,
        protocol: protocol.value,
        connParams: connParams.value,
      }),
      new Promise<never>((_, reject) =>
        window.setTimeout(
          () => reject(new AppError(-1, `测试连接超时（${TEST_CONNECT_TIMEOUT_MS / 1000}s）`)),
          TEST_CONNECT_TIMEOUT_MS,
        ),
      ),
    ])
    testResult.value = result
    if (result.ok) ui.success(`连接成功，识别类型 ${describeType(result.payloadType)}`)
    else ui.warning(result.error || '连接失败')
  } catch (err) {
    ui.handleError(err, '测试连接失败')
  } finally {
    testing.value = false
  }
}

function describeType(pt: PayloadType | undefined): string {
  return pt ? PAYLOAD_TYPE_TEXT[pt] : '未识别'
}

async function copy(text: string): Promise<void> {
  if (!text) return
  const ok = await copyText(text)
  if (ok) ui.success('已复制到剪贴板')
  else ui.warning('复制失败，请手动选中复制')
}

function resetForm(): void {
  step.value = 1
  name.value = ''
  rtmpUrl.value = ''
  mediaMtxPath.value = ''
  videoMode.value = 'pull'
  rtspTransport.value = 'tcp'
  testResult.value = undefined
  publishUrls.value = { whip: '', rtmp: '' }
  showPublish.value = false
}

async function submit(): Promise<void> {
  if (formError.value) {
    ui.warning(formError.value)
    return
  }
  creating.value = true
  try {
    const created = await datasourceStore.createSource({
      name: name.value.trim(),
      kind: kind.value,
      protocol: protocol.value,
      connParams: connParams.value,
    })
    // publish 模式：落库后把推流地址留在页面上供复制，用户点“完成”才关闭
    if (isPublish.value) {
      publishUrls.value = readPublishUrls(datasourceStore.channelsBySource[created.id] ?? [])
      showPublish.value = true
      ui.success('数据源已创建，请复制下方推流地址')
      return
    }
    // ⚠️ 必须说清楚「接下来做什么」：只说"已创建"会让人误以为画面上该出现东西了。
    // 建数据源只是接通了一根管子，还要把它下面的通道变成卡片才会显示出来。
    ui.success('数据源已创建：在左侧展开它，给下面的通道点「＋」即可显示')
    resetForm()
    ui.closeSourceWizard()
  } catch (err) {
    ui.handleError(err, '创建数据源失败')
  } finally {
    creating.value = false
  }
}

function pickKind(next: DataSourceKind): void {
  // ⚠️ 只切换类型，**不再跳到 Step 2**：Step 1 现在是"选类型 + 填参数"的完整表单，
  // Step 2 是只读摘要。若这里仍跳步，用户会在 Step 1 刚点完类型就被推走、看不到表单。
  kind.value = next
  testResult.value = undefined
  showPublish.value = false
  if (name.value.length === 0) {
    const preset: Record<DataSourceKind, string> = { video: 'lite3 相机', ros: 'ROS遥测', http: 'HTTP接口' }
    name.value = preset[next]
  }
}

function close(): void {
  resetForm()
  ui.closeSourceWizard()
}
</script>

<template>
  <div v-if="open" class="ma-wizard-mask">
    <div class="ma-wizard">
      <header class="ma-wizard__head">
        <span class="ma-wizard__title">新建数据源 · Step {{ step }}</span>
        <button class="ma-wizard__close" @click="close">✕</button>
      </header>

      <div class="ma-wizard__body ma-scroll-y">
        <!-- Step1：选类型 + 填参数（可编辑） -->
        <div v-if="step === 1" class="ma-wizard__step1">
          <div class="ma-wizard__kinds">
            <button
              v-for="k in KINDS"
              :key="k.value"
              class="ma-wizard__kind"
              :class="{ 'is-active': kind === k.value }"
              @click="pickKind(k.value)"
            >
              <span class="ma-wizard__kind-name">{{ k.label }}</span>
              <span class="ma-wizard__kind-desc ma-text-xs ma-text-3">{{ k.desc }}</span>
            </button>
          </div>

          <div class="ma-wizard__form">
            <div class="ma-wizard__row">
              <label class="ma-wizard__label">名称</label>
              <input v-model="name" class="ma-wizard__input" placeholder="数据源名称" />
            </div>

            <!-- 视频 -->
            <template v-if="kind === 'video'">
              <div class="ma-wizard__row">
                <label class="ma-wizard__label">接入模式</label>
                <select v-model="videoMode" class="ma-wizard__input">
                  <option value="pull">拉流（去摄像头 / 推流端主动拉）</option>
                  <option value="publish">接收推流（远端 WHIP / RTMP 推给我）</option>
                </select>
              </div>

              <!--
                ⚠️ 推流模式（publish）下**不渲染流地址输入行**：
                该模式下流地址完全不参与行为，留着它只会诱导用户填一条无意义的地址
                （用户真实踩过：把外部 SRS 的拉流地址填进了推流模式，库里留下语义矛盾的配置）。
                原位给一行说明，告诉用户"接下来该做什么"；且解析提示 / RTSP 传输行一并包在
                `!isPublish` 里，避免留下与 URL 无关的孤零零空行。
              -->
              <template v-if="!isPublish">
                <div class="ma-wizard__row">
                  <label class="ma-wizard__label">流地址</label>
                  <input
                    v-model="rtmpUrl"
                    class="ma-wizard__input"
                    placeholder="rtsp://admin:your-password@192.168.1.64:554/Streaming/Channels/101"
                  />
                </div>
                <div v-if="streamFamily" class="ma-wizard__note ma-text-xs ma-text-3">
                  已识别为 <b>{{ streamFamily === 'rtsp' ? 'RTSP' : 'RTMP' }}</b>
                  <template v-if="streamFamily === 'rtsp'">
                    · 默认 554 端口，URL 内的账号密码会原样交给 MediaMTX 拉流
                  </template>
                  <template v-else>· 默认 1935 端口</template>
                </div>
                <div v-if="streamFamily === 'rtsp'" class="ma-wizard__row">
                  <label class="ma-wizard__label">RTSP 传输</label>
                  <select v-model="rtspTransport" class="ma-wizard__input">
                    <option value="tcp">TCP（默认，不易花屏）</option>
                    <option value="udp">UDP（延迟略低，易丢包）</option>
                    <option value="automatic">自动协商</option>
                  </select>
                </div>
              </template>
              <div v-else class="ma-wizard__note ma-text-xs ma-text-3">
                接收推流模式不需要流地址 —— 请把创建后给出的 WHIP / RTMP 推流地址填进推流端。
              </div>

              <div class="ma-wizard__row">
                <label class="ma-wizard__label">MediaMTX 路径</label>
                <input
                  v-model="mediaMtxPath"
                  class="ma-wizard__input"
                  :placeholder="isPublish ? '必填，只需流名，如 live/whip1' : '留空则自动取自流地址，如 live/cam1'"
                  @blur="mediaMtxPath = mtxPathNorm"
                />
              </div>
              <!-- 实时红字：非法就当场说清怎么改，不要等提交才发现 -->
              <div v-if="mtxPathError" class="ma-wizard__error ma-text-xs">{{ mtxPathError }}</div>
              <div class="ma-wizard__note ma-text-xs ma-text-3">
                <template v-if="isPublish">
                  接收推流模式下**必须**填写路径 —— 推流地址就挂在这条路径上。
                </template>
                <template v-else>
                  「MediaMTX 路径」与上面流地址是同一信息的路径部分，通常留空即可自动提取。
                </template>
                <br />
                ⚠️ 只需流名（如 <code>live/cam1</code>），
                <b>不要带 <code>rtmp://</code> 等协议前缀与主机</b> ——
                填成整条地址会让 MediaMTX 报 <code>invalid path name</code>，画面表现为播放不出来。
              </div>
              <div class="ma-wizard__row">
                <label class="ma-wizard__label">首选协议</label>
                <select v-model="preferredProtocol" class="ma-wizard__input">
                  <option value="webrtc">WebRTC（默认，≤0.5s）</option>
                  <option value="hls">HLS（2-3s）</option>
                  <option value="flv">HTTP-FLV（需外部网关）</option>
                </select>
              </div>
              <div class="ma-wizard__note ma-text-xs ma-text-3">
                WebRTC 需要安全上下文：跨主机用 http 访问看板时会被浏览器拦截，此时请选 HLS。
              </div>
              <div class="ma-wizard__row">
                <label class="ma-wizard__label">携带音频</label>
                <button class="ma-wizard__toggle" :class="{ 'is-on': audio }" @click="audio = !audio">
                  {{ audio ? '开' : '关' }}
                </button>
              </div>
            </template>

            <!-- ROS -->
            <template v-else-if="kind === 'ros'">
              <div class="ma-wizard__row">
                <label class="ma-wizard__label">ROS 版本</label>
                <select v-model="rosVersion" class="ma-wizard__input">
                  <option value="ros1">ROS1（rosbridge to master）</option>
                  <option value="ros2">ROS2（rosbridge + domainId）</option>
                </select>
              </div>
              <div class="ma-wizard__row">
                <label class="ma-wizard__label">rosbridge 地址</label>
                <input v-model="bridgeUrl" class="ma-wizard__input" placeholder="ws://172.31.68.227:9090" />
              </div>
              <div v-if="rosVersion === 'ros2'" class="ma-wizard__row">
                <label class="ma-wizard__label">Domain ID</label>
                <input v-model.number="domainId" class="ma-wizard__input" type="number" step="1" />
              </div>
              <div class="ma-wizard__row ma-wizard__row--col">
                <label class="ma-wizard__label">话题列表（逗号或换行分隔）</label>
                <textarea
                  v-model="topicsText"
                  class="ma-wizard__textarea"
                  placeholder="/odom&#10;/imu/data&#10;/battery"
                ></textarea>
                <span class="ma-text-xs ma-text-3">
                  rosapi 话题发现在 ROS2 各发行版服务名不一致，MVP 以手工填写为主路径
                </span>
              </div>
            </template>

            <!-- HTTP -->
            <template v-else>
              <div class="ma-wizard__row">
                <label class="ma-wizard__label">接口地址</label>
                <input v-model="httpUrl" class="ma-wizard__input" placeholder="http://172.31.68.9:8080/api/stat" />
              </div>
              <div class="ma-wizard__row">
                <label class="ma-wizard__label">方法</label>
                <select v-model="httpMethod" class="ma-wizard__input">
                  <option value="GET">GET</option>
                  <option value="POST">POST</option>
                </select>
              </div>
              <div class="ma-wizard__row">
                <label class="ma-wizard__label">轮询间隔(ms)</label>
                <input v-model.number="intervalMs" class="ma-wizard__input" type="number" min="100" step="100" />
              </div>
              <div class="ma-wizard__row">
                <label class="ma-wizard__label">JSONPath</label>
                <input v-model="jsonPath" class="ma-wizard__input" placeholder="留空取整个响应，如 data.items" />
              </div>
              <div class="ma-wizard__row">
                <label class="ma-wizard__label">超时(ms)</label>
                <input v-model.number="timeoutMs" class="ma-wizard__input" type="number" min="200" step="100" />
              </div>
              <div v-if="httpMethod === 'POST'" class="ma-wizard__row ma-wizard__row--col">
                <label class="ma-wizard__label">请求体</label>
                <textarea v-model="httpBody" class="ma-wizard__textarea" placeholder='{"cmd":"stat"}'></textarea>
              </div>
            </template>

            <div v-if="urlError" class="ma-wizard__error ma-text-xs">{{ urlError }}</div>
          </div>
        </div>

        <!--
          Step2：**只读摘要**（不再渲染任何表单字段）。
          以前 Step 2 是同一套表单，用户会以为"这步要把刚才填的再填一遍"。
          现在它是"确认页"：只读行列表 + 测试结果 + 创建后的推流地址。
        -->
        <div v-else class="ma-wizard__review">
          <div class="ma-wizard__summary">
            <div v-for="row in summaryRows" :key="row.label" class="ma-wizard__srow">
              <span class="ma-wizard__slabel">{{ row.label }}</span>
              <span class="ma-wizard__svalue" :class="{ 'is-mono': row.mono }">{{ row.value }}</span>
            </div>
          </div>
          <div class="ma-wizard__note ma-text-xs ma-text-3">
            以上为只读摘要；需要修改请点「上一步」。
          </div>

          <div v-if="testResult" class="ma-wizard__result">
            <span :class="testResult.ok ? 'is-ok' : 'is-bad'" class="ma-text-sm">
              {{ testResult.ok ? `连接成功 · ${describeType(testResult.payloadType)} · ${testResult.latencyMs}ms` : '连接失败' }}
            </span>
            <div v-if="!testResult.ok" class="ma-text-xs">
              <div class="ma-wizard__err-msg">{{ testResult.error }}</div>
              <div v-if="testResult.hint" class="ma-wizard__hint">修复建议：{{ testResult.hint }}</div>
            </div>
            <div v-else-if="testResult.channels && testResult.channels.length > 0" class="ma-text-xs ma-text-3">
              发现通道：{{ testResult.channels.join('、') }}
            </div>
          </div>

          <!-- publish 模式创建成功后回显推流地址 -->
          <div v-if="showPublish" class="ma-wizard__publish">
            <div class="ma-text-sm">推流地址（复制给推流端）</div>
            <div class="ma-wizard__pubrow">
              <span class="ma-wizard__pubtag">WHIP</span>
              <code class="ma-wizard__puburl">{{ publishUrls.whip || '未返回' }}</code>
              <button class="ma-wizard__btn" :disabled="!publishUrls.whip" @click="copy(publishUrls.whip)">
                复制
              </button>
            </div>
            <div class="ma-wizard__pubrow">
              <span class="ma-wizard__pubtag">RTMP</span>
              <code class="ma-wizard__puburl">{{ publishUrls.rtmp || '未返回' }}</code>
              <button class="ma-wizard__btn" :disabled="!publishUrls.rtmp" @click="copy(publishUrls.rtmp)">
                复制
              </button>
            </div>
            <div class="ma-text-xs ma-text-3">
              WHIP 给浏览器 / OBS 等 WebRTC 推流端，RTMP 给 ffmpeg / GStreamer。
              纯 HTTP 环境下浏览器推流可能被安全策略拦截，此时改用 RTMP 或给后端启用 HTTPS。
            </div>
          </div>
        </div>
      </div>

      <footer class="ma-wizard__foot">
        <!-- Step 1 就把拦截原因说清楚：否则用户只看到「下一步」灰着，不知道差什么 -->
        <span v-if="formError && step === 1" class="ma-wizard__error ma-text-xs">{{ formError }}</span>
        <div class="ma-wizard__grow"></div>
        <button v-if="step === 2 && !showPublish" class="ma-wizard__btn" @click="step = 1">上一步</button>
        <button v-if="step === 2 && !showPublish" class="ma-wizard__btn" :disabled="testing" @click="test">
          {{ testing ? '测试中…' : '测试连接' }}
        </button>
        <button
          class="ma-wizard__btn ma-wizard__btn--primary"
          :disabled="creating || (!showPublish && Boolean(formError))"
          @click="step === 1 ? (step = 2) : showPublish ? close() : submit()"
        >
          {{
            creating
              ? '创建中…'
              : showPublish
                ? '完成'
                : step === 1
                  ? '下一步'
                  : '创建数据源'
          }}
        </button>
      </footer>
    </div>
  </div>
</template>

<style scoped>
.ma-wizard-mask {
  position: absolute;
  inset: 0;
  /* 与其它右侧抽屉同级(300)：压过地图徽标(200) */
  z-index: 300;
  display: flex;
  align-items: center;
  justify-content: center;
  background: color-mix(in srgb, var(--ma-bg-base) 62%, transparent);
}

.ma-wizard {
  display: flex;
  flex-direction: column;
  width: 560px;
  max-width: 92%;
  max-height: 84%;
  background: var(--ma-bg-elevated);
  border: 1px solid var(--ma-border);
  border-radius: var(--ma-radius);
  box-shadow: var(--ma-shadow-pop);
}

.ma-wizard__head {
  display: flex;
  align-items: center;
  height: 40px;
  padding: 0 12px;
  border-bottom: 1px solid var(--ma-border);
}

.ma-wizard__title {
  flex: 1 1 auto;
  font-size: var(--ma-font-lg);
}

.ma-wizard__close {
  border: none;
  background: none;
  color: var(--ma-text-3);
  cursor: pointer;
}

.ma-wizard__body {
  flex: 1 1 auto;
  min-height: 0;
  padding: 12px;
}

.ma-wizard__kinds {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

/* Step1 = 类型 + 表单；两者之间留出间距 */
.ma-wizard__step1 {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

/* Step2 = 只读摘要 + 结果/推流地址 */
.ma-wizard__review {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.ma-wizard__summary {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 8px;
  border: 1px solid var(--ma-border);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-base);
}

.ma-wizard__srow {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 3px 0;
}

.ma-wizard__slabel {
  flex: none;
  width: 88px;
  font-size: var(--ma-font-sm);
  color: var(--ma-text-2);
}

.ma-wizard__svalue {
  flex: 1 1 auto;
  min-width: 0;
  font-size: var(--ma-font-sm);
  color: var(--ma-text-1);
  word-break: break-all;
  line-height: 1.5;
}

.ma-wizard__svalue.is-mono {
  font-family: 'JetBrains Mono', monospace;
}

.ma-wizard__kind {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 2px;
  padding: 10px 12px;
  border: 1px solid var(--ma-border);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-base);
  color: var(--ma-text-1);
  cursor: pointer;
  text-align: left;
}

.ma-wizard__kind:hover {
  border-color: var(--ma-border-strong);
}

.ma-wizard__kind.is-active {
  border-color: var(--ma-accent);
  background: var(--ma-accent-dim);
}

.ma-wizard__kind-name {
  font-size: var(--ma-font-md);
}

.ma-wizard__form {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.ma-wizard__row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.ma-wizard__row--col {
  flex-direction: column;
  align-items: stretch;
  gap: 4px;
}

.ma-wizard__label {
  flex: none;
  width: 96px;
  font-size: var(--ma-font-sm);
  color: var(--ma-text-2);
}

.ma-wizard__row--col .ma-wizard__label {
  width: auto;
}

.ma-wizard__input,
.ma-wizard__textarea {
  flex: 1 1 auto;
  min-width: 0;
  height: 28px;
  padding: 0 6px;
  border: 1px solid var(--ma-border);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-base);
  color: var(--ma-text-1);
  font-size: var(--ma-font-sm);
}

.ma-wizard__textarea {
  height: 72px;
  padding: 6px;
  resize: vertical;
  font-family: 'JetBrains Mono', monospace;
}

.ma-wizard__input:focus,
.ma-wizard__textarea:focus {
  outline: none;
  border-color: var(--ma-accent);
}

.ma-wizard__toggle {
  padding: 2px 12px;
  border: 1px solid var(--ma-border-strong);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-base);
  color: var(--ma-text-2);
  cursor: pointer;
  font-size: var(--ma-font-xs);
}

.ma-wizard__toggle.is-on {
  border-color: var(--ma-accent);
  color: var(--ma-accent);
}

.ma-wizard__error {
  color: var(--ma-status-error);
}

.ma-wizard__result {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 8px;
  border: 1px solid var(--ma-border);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-base);
}

.ma-wizard__result .is-ok {
  color: var(--ma-status-online);
}

.ma-wizard__result .is-bad {
  color: var(--ma-status-error);
}

.ma-wizard__err-msg {
  color: var(--ma-text-1);
}

/* 表单内补充说明：与 label 同宽缩进，紧贴上一行 */
.ma-wizard__note {
  margin: -4px 0 0 104px;
}

/* publish 模式回显推流地址 */
.ma-wizard__publish {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 8px;
  border: 1px solid var(--ma-border);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-base);
}

.ma-wizard__pubrow {
  display: flex;
  align-items: center;
  gap: 6px;
}

.ma-wizard__pubtag {
  flex: none;
  width: 44px;
  font-size: var(--ma-font-xs);
  color: var(--ma-accent);
}

.ma-wizard__puburl {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  font-family: 'JetBrains Mono', monospace;
  font-size: var(--ma-font-xs);
  color: var(--ma-text-1);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ma-wizard__hint {
  color: var(--ma-status-reconnecting);
}

.ma-wizard__foot {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 12px;
  border-top: 1px solid var(--ma-border);
}

.ma-wizard__grow {
  flex: 1 1 auto;
}

.ma-wizard__btn {
  padding: 4px 14px;
  border: 1px solid var(--ma-border-strong);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-base);
  color: var(--ma-text-1);
  cursor: pointer;
  font-size: var(--ma-font-sm);
}

.ma-wizard__btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}

.ma-wizard__btn--primary {
  border-color: var(--ma-accent);
  background: var(--ma-accent-dim);
  color: var(--ma-accent);
}
</style>
