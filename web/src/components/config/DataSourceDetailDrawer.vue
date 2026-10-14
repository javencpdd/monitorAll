<script setup lang="ts">
/**
 * 数据源详情抽屉（**只读**）。
 *
 * 背景：数据源一旦创建，界面上以前**完全看不到它的配置** ——
 * 看不到自己填过的接入模式 / 流地址 / MediaMTX 路径，改不了只能删了重建，
 * 这正是用户建出 18 个同名重复数据源的根因。
 *
 * 约束（team-lead 裁定）：
 *   - **只读**：没有任何输入框，也不提供编辑/保存 —— 需要改就删了重建。
 *     不在这里处理 `PUT /datasources/:id` 的 secret 回填语义（后端不会把 `***` 回写），那是个坑。
 *   - 删除**不在这里实现**：只 `emit('delete')`，由 `SourcePanel` 复用已有的
 *     `confirmDeleteSource(source)`（避免出现第二套删除确认 / API 调用）。
 *   - 脱敏：流地址 / HTTP 地址可能内联凭据（后端不处理 URL userinfo），展示前必须过
 *     `maskUrlCredentials`。**但推流地址（publish）不打码** —— 它是要被原样复制给推流端的，
 *     打码会让用户复制到一条坏地址。
 *
 * 外观沿用 `SettingsDrawer` 的 `.ma-set__*` 体系（只读字段行 label + static 值 + 分隔段），
 * 不新造一套样式。
 */
import { computed, onBeforeUnmount, watch } from 'vue'
import { useDatasourceStore } from '@/stores/datasource'
import { useDashboardStore } from '@/stores/dashboard'
import { useUiStore } from '@/stores/ui'
import {
  DATA_SOURCE_DETAIL_WIDTH,
  KIND_TEXT,
  PAYLOAD_TYPE_TEXT,
  PROTOCOL_TEXT,
  STATUS_TEXT,
  VIDEO_PROTOCOL_LABEL,
} from '@/utils/constants'
import { formatClock, formatRelative } from '@/utils/format'
import { maskUrlCredentials } from '@/utils/redact'
import { readPublishUrls } from '@/utils/publishUrls'
import { copyText } from '@/utils/clipboard'
import StatusBadge from '@/components/layout/StatusBadge.vue'
import type { ChannelRuntime, DataSource } from '@/types'

const props = defineProps<{ sourceId?: string }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'delete', source: DataSource): void }>()

const datasourceStore = useDatasourceStore()
const dashboardStore = useDashboardStore()
const ui = useUiStore()

const open = computed<boolean>(() => Boolean(props.sourceId))
/** 按 id 实时查 store：状态/通道变化自动反映，删掉后自动变 undefined。 */
const source = computed<DataSource | undefined>(() =>
  props.sourceId ? datasourceStore.sources.find((s) => s.id === props.sourceId) : undefined,
)

/* ——————————————— 安全读取 connParams ———————————————
 * `connParams` 的类型是 `Record<string, unknown>`（服务端出参，键值后端可扩展）。
 * 展示层用类型守卫逐字段读取，**不做 as 断言**：既满足"无 any 无强转"的合规要求，
 * 又对字段缺失 / 类型不符天然容错（读到什么显示什么，读不到显示占位）。
 */
function str(v: unknown): string {
  return typeof v === 'string' ? v : ''
}
function num(v: unknown): number | undefined {
  return typeof v === 'number' && Number.isFinite(v) ? v : undefined
}
function strArr(v: unknown): string[] {
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : []
}

const params = computed<Record<string, unknown>>(() => source.value?.connParams ?? {})

const isVideo = computed<boolean>(() => source.value?.kind === 'video')
const isRos = computed<boolean>(() => source.value?.kind === 'ros')
const isHttp = computed<boolean>(() => source.value?.kind === 'http')

/* 视频 */
const videoMode = computed<'pull' | 'publish'>(() => (str(params.value.mode) === 'publish' ? 'publish' : 'pull'))
const streamUrl = computed<string>(() => str(params.value.rtmpUrl))
const streamFamily = computed<'rtsp' | 'rtmp'>(() => (/^rtsps?:\/\//i.test(streamUrl.value) ? 'rtsp' : 'rtmp'))
const preferredProtocol = computed<string>(() => {
  const v = str(params.value.preferredProtocol)
  return v === 'hls' || v === 'flv' ? VIDEO_PROTOCOL_LABEL[v] : VIDEO_PROTOCOL_LABEL.webrtc
})
const mediaMtxPath = computed<string>(() => str(params.value.mediaMtxPath))
const audioOn = computed<boolean>(() => params.value.audio === true)
const rtspTransport = computed<string>(() => {
  const v = str(params.value.rtspTransport)
  return v === 'udp' || v === 'automatic' ? v : 'tcp'
})

/* ROS */
const rosVersion = computed<'ros1' | 'ros2'>(() => (str(params.value.version) === 'ros1' ? 'ros1' : 'ros2'))
const bridgeUrl = computed<string>(() => str(params.value.bridgeUrl))
const domainId = computed<number | undefined>(() => num(params.value.domainId))
const topics = computed<string[]>(() => strArr(params.value.topics))

/* HTTP */
const httpMethod = computed<'GET' | 'POST'>(() => (str(params.value.method) === 'POST' ? 'POST' : 'GET'))
const intervalMs = computed<number | undefined>(() => num(params.value.intervalMs))
const timeoutMs = computed<number | undefined>(() => num(params.value.timeoutMs))
const jsonPath = computed<string>(() => str(params.value.jsonPath))
const httpBody = computed<string>(() => str(params.value.body))

/* ——————————————— 派生展示 ——————————————— */

const statusText = computed<string>(() => (source.value ? STATUS_TEXT[source.value.status] : ''))
const isPublish = computed<boolean>(() => isVideo.value && videoMode.value === 'publish')
const rtspPull = computed<boolean>(() => isVideo.value && streamFamily.value === 'rtsp' && videoMode.value === 'pull')

const channels = computed<ChannelRuntime[]>(() =>
  source.value ? (datasourceStore.channelsBySource[source.value.id] ?? []) : [],
)
const publish = computed(() => readPublishUrls(channels.value))

/**
 * 某通道被几张卡片引用。
 *
 * 后端 `refCount` 是权威来源（订阅管理器计数，跨看板）；当前看板的 `cards` 是即时补充。
 * 取两者较大值：任一来源说"有引用"，就不该显示"未被引用"。
 */
function cardRefs(ch: ChannelRuntime): number {
  const inBoard = dashboardStore.cards.filter((c) => c.channelId === ch.id).length
  return Math.max(ch.refCount, inBoard)
}

function clock(ts: number | undefined): string {
  return ts ? formatClock(ts) : '—'
}

async function copy(text: string): Promise<void> {
  if (!text) return
  const ok = await copyText(text)
  if (ok) ui.success('已复制到剪贴板')
  else ui.warning('复制失败，请手动选中复制')
}

/** 删除只上报意图，确认流程与 API 调用由 SourcePanel 复用既有实现。 */
function requestDelete(): void {
  const s = source.value
  if (!s) return
  emit('delete', s)
}

function close(): void {
  emit('close')
}

function onKeyDown(e: KeyboardEvent): void {
  if (!open.value || e.key !== 'Escape') return
  const t = e.target
  if (t instanceof HTMLElement && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA')) return
  close()
}

watch(
  open,
  (isOpen) => {
    if (isOpen) window.addEventListener('keydown', onKeyDown)
    else window.removeEventListener('keydown', onKeyDown)
  },
  { immediate: true },
)
onBeforeUnmount(() => window.removeEventListener('keydown', onKeyDown))
</script>

<template>
  <template v-if="open && source">
    <div class="ma-detail__mask" @click="close"></div>
    <aside class="ma-detail" :style="{ width: `${DATA_SOURCE_DETAIL_WIDTH}px` }">
      <header class="ma-detail__head">
        <span class="ma-detail__title">数据源详情</span>
        <button class="ma-detail__close" title="关闭（Esc）" @click="close">✕</button>
      </header>

      <div class="ma-detail__body">
        <!-- ——— 基本信息 ——— -->
        <div class="ma-detail__sep">基本信息</div>

        <div class="ma-detail__field">
          <label class="ma-detail__label">名称</label>
          <span class="ma-detail__static">{{ source.name }}</span>
        </div>
        <div class="ma-detail__field">
          <label class="ma-detail__label">类型</label>
          <span class="ma-detail__static">{{ KIND_TEXT[source.kind] }}</span>
        </div>
        <div class="ma-detail__field">
          <label class="ma-detail__label">协议</label>
          <span class="ma-detail__static">{{ PROTOCOL_TEXT[source.protocol] }}</span>
        </div>
        <div class="ma-detail__field">
          <label class="ma-detail__label">启用状态</label>
          <span class="ma-detail__static" :class="{ 'is-off': !source.enabled }">
            {{ source.enabled ? '已启用' : '已停用' }}
          </span>
        </div>
        <div class="ma-detail__field">
          <label class="ma-detail__label">运行状态</label>
          <status-badge :status="source.status" :text="statusText" :tip="source.lastError" size="small" />
        </div>
        <div v-if="source.lastError" class="ma-detail__field">
          <label class="ma-detail__label">最后错误</label>
          <span class="ma-detail__static is-error">{{ source.lastError }}</span>
        </div>
        <div class="ma-detail__field">
          <label class="ma-detail__label">最后成功</label>
          <span class="ma-detail__static">{{ source.lastOkAt ? formatRelative(source.lastOkAt) : '—' }}</span>
        </div>
        <div class="ma-detail__field">
          <label class="ma-detail__label">创建时间</label>
          <span class="ma-detail__static">{{ clock(source.createdAt) }}</span>
        </div>
        <div class="ma-detail__field">
          <label class="ma-detail__label">更新时间</label>
          <span class="ma-detail__static">{{ clock(source.updatedAt) }}</span>
        </div>

        <!-- ——— 视频配置 ——— -->
        <template v-if="isVideo">
          <div class="ma-detail__sep">视频配置</div>

          <div class="ma-detail__field">
            <label class="ma-detail__label">接入模式</label>
            <span class="ma-detail__static">
              {{ isPublish ? '接收推流（远端 WHIP / RTMP 推给我）' : '拉流（我主动去拉远端流）' }}
            </span>
          </div>
          <div class="ma-detail__field">
            <label class="ma-detail__label">流地址</label>
            <!-- 可能内联凭据（如 rtsp://user:pw@host/...），必须脱敏后再显示 -->
            <span class="ma-detail__static ma-detail__static--mono">
              {{
                str(params.rtmpUrl)
                  ? maskUrlCredentials(str(params.rtmpUrl))
                  : isPublish
                    ? '——（推流模式无需填写）'
                    : '未填'
              }}
            </span>
          </div>
          <div class="ma-detail__field">
            <label class="ma-detail__label">MediaMTX 路径</label>
            <span class="ma-detail__static ma-detail__static--mono">
              {{ mediaMtxPath || '未填 · 将由后端从流地址自动解析' }}
            </span>
          </div>
          <div class="ma-detail__field">
            <label class="ma-detail__label">首选协议</label>
            <span class="ma-detail__static">{{ preferredProtocol }}</span>
          </div>
          <div class="ma-detail__field">
            <label class="ma-detail__label">携带音频</label>
            <span class="ma-detail__static">{{ audioOn ? '是' : '否' }}</span>
          </div>
          <div v-if="rtspPull" class="ma-detail__field">
            <label class="ma-detail__label">RTSP 传输</label>
            <span class="ma-detail__static">
              {{ rtspTransport === 'udp' ? 'UDP' : rtspTransport === 'automatic' ? '自动协商' : 'TCP' }}
            </span>
          </div>

          <!-- publish 模式：推流地址（原样复制，不打码） -->
          <template v-if="isPublish">
            <div class="ma-detail__sep ma-detail__sep--sub">推流地址</div>
            <p class="ma-detail__note">把这两条地址给推流端；此模式不需要填流地址。</p>
            <div class="ma-detail__pubrow">
              <span class="ma-detail__pubtag">WHIP</span>
              <code class="ma-detail__puburl">{{ publish.whip || '未返回' }}</code>
              <button class="ma-detail__btn" :disabled="!publish.whip" @click="copy(publish.whip)">复制</button>
            </div>
            <div class="ma-detail__pubrow">
              <span class="ma-detail__pubtag">RTMP</span>
              <code class="ma-detail__puburl">{{ publish.rtmp || '未返回' }}</code>
              <button class="ma-detail__btn" :disabled="!publish.rtmp" @click="copy(publish.rtmp)">复制</button>
            </div>
            <p class="ma-detail__note">
              WHIP 给浏览器 / OBS 等 WebRTC 推流端，RTMP 给 ffmpeg / GStreamer。
            </p>
          </template>
        </template>

        <!-- ——— ROS 配置 ——— -->
        <template v-else-if="isRos">
          <div class="ma-detail__sep">ROS 配置</div>

          <div class="ma-detail__field">
            <label class="ma-detail__label">版本</label>
            <span class="ma-detail__static">{{ rosVersion === 'ros1' ? 'ROS1' : 'ROS2' }}</span>
          </div>
          <div class="ma-detail__field">
            <label class="ma-detail__label">rosbridge 地址</label>
            <span class="ma-detail__static ma-detail__static--mono">{{ bridgeUrl || '未填' }}</span>
          </div>
          <div v-if="rosVersion === 'ros2'" class="ma-detail__field">
            <label class="ma-detail__label">Domain ID</label>
            <span class="ma-detail__static">{{ domainId ?? '—' }}</span>
          </div>
          <div class="ma-detail__field">
            <label class="ma-detail__label">话题列表</label>
            <span class="ma-detail__static ma-detail__static--mono">
              {{ topics.length > 0 ? topics.join('、') : '未填' }}
            </span>
          </div>
        </template>

        <!-- ——— HTTP 配置 ——— -->
        <template v-else-if="isHttp">
          <div class="ma-detail__sep">HTTP 配置</div>

          <div class="ma-detail__field">
            <label class="ma-detail__label">接口地址</label>
            <!-- 地址可能内联凭据，必须脱敏后再显示 -->
            <span class="ma-detail__static ma-detail__static--mono">
              {{ str(params.url) ? maskUrlCredentials(str(params.url)) : '未填' }}
            </span>
          </div>
          <div class="ma-detail__field">
            <label class="ma-detail__label">方法</label>
            <span class="ma-detail__static">{{ httpMethod }}</span>
          </div>
          <div class="ma-detail__field">
            <label class="ma-detail__label">轮询间隔</label>
            <span class="ma-detail__static">{{ intervalMs !== undefined ? `${intervalMs} ms` : '—' }}</span>
          </div>
          <div class="ma-detail__field">
            <label class="ma-detail__label">超时</label>
            <span class="ma-detail__static">{{ timeoutMs !== undefined ? `${timeoutMs} ms` : '—' }}</span>
          </div>
          <div class="ma-detail__field">
            <label class="ma-detail__label">JSONPath</label>
            <span class="ma-detail__static ma-detail__static--mono">
              {{ jsonPath || '（留空：取整个响应）' }}
            </span>
          </div>
          <div v-if="httpMethod === 'POST'" class="ma-detail__field">
            <label class="ma-detail__label">请求体</label>
            <span class="ma-detail__static ma-detail__static--mono">{{ httpBody || '—' }}</span>
          </div>
        </template>

        <!-- ——— 通道 ——— -->
        <div class="ma-detail__sep">通道（{{ channels.length }}）</div>
        <p v-if="channels.length === 0" class="ma-detail__note">暂无通道</p>
        <div v-for="ch in channels" :key="ch.id" class="ma-detail__ch">
          <span class="ma-detail__ch-name ma-ellipsis" :title="ch.name">{{ ch.name }}</span>
          <span class="ma-detail__ch-type ma-text-xs ma-text-3">{{ PAYLOAD_TYPE_TEXT[ch.payloadType] }}</span>
          <span class="ma-detail__ch-refs ma-text-xs">
            {{ cardRefs(ch) > 0 ? `被 ${cardRefs(ch)} 张卡片引用` : '未被引用' }}
          </span>
        </div>
      </div>

      <footer class="ma-detail__foot">
        <p class="ma-detail__note ma-detail__note--ro">本面板为只读；需要修改请删除后重新创建。</p>
        <button class="ma-detail__btn ma-detail__btn--danger" @click="requestDelete">删除此数据源</button>
      </footer>
    </aside>
  </template>
</template>

<style scoped>
/*
 * 只读详情抽屉：外观沿用 SettingsDrawer 的 `.ma-set` 体系
 * （同一套 CSS 变量、field/label/static/sep/note 行模式），只把前缀换成 `.ma-detail`
 * 以避免与设置抽屉的类名混淆。
 */
.ma-detail {
  position: fixed;
  top: 0;
  right: 0;
  bottom: 0;
  z-index: 400; /* 与设置抽屉同级：高于卡片抽屉(300)与地图徽标(200) */
  display: flex;
  flex-direction: column;
  background: var(--ma-bg-elevated);
  border-left: 1px solid var(--ma-border);
  box-shadow: var(--ma-shadow-pop);
}

.ma-detail__mask {
  position: fixed;
  inset: 0;
  z-index: 399;
  background: rgba(0, 0, 0, 0.34);
}

.ma-detail__head {
  display: flex;
  align-items: center;
  height: 40px;
  padding: 0 12px;
  border-bottom: 1px solid var(--ma-border);
}

.ma-detail__title {
  flex: 1 1 auto;
  font-size: var(--ma-font-lg);
}

.ma-detail__close {
  border: none;
  background: none;
  color: var(--ma-text-3);
  cursor: pointer;
}

/* 滚动主体：flex-basis:0 + 子项不收缩，否则内容超高时容器被压扁而非出现滚动条 */
.ma-detail__body {
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

.ma-detail__body > * {
  flex-shrink: 0;
}

.ma-detail__sep {
  margin-top: 6px;
  padding-top: 8px;
  border-top: 1px solid var(--ma-border);
  font-size: var(--ma-font-md);
  color: var(--ma-text-1);
}

.ma-detail__sep--sub {
  font-size: var(--ma-font-sm);
  color: var(--ma-text-2);
}

.ma-detail__field {
  display: flex;
  align-items: flex-start;
  gap: 8px;
}

.ma-detail__label {
  flex: none;
  width: 88px;
  font-size: var(--ma-font-sm);
  color: var(--ma-text-2);
}

.ma-detail__static {
  flex: 1 1 auto;
  min-width: 0;
  font-size: var(--ma-font-sm);
  color: var(--ma-text-1);
  word-break: break-all;
  line-height: 1.5;
}

.ma-detail__static--mono {
  font-family: 'JetBrains Mono', 'SFMono-Regular', Menlo, Consolas, monospace;
}

.ma-detail__static.is-error {
  color: var(--ma-status-error);
}

.ma-detail__static.is-off {
  color: var(--ma-text-3);
}

.ma-detail__note {
  margin: 0;
  font-size: var(--ma-font-xs);
  color: var(--ma-text-3);
  line-height: 1.5;
}

.ma-detail__note--ro {
  margin-bottom: 8px;
}

/* 推流地址行 */
.ma-detail__pubrow {
  display: flex;
  align-items: center;
  gap: 6px;
}

.ma-detail__pubtag {
  flex: none;
  width: 44px;
  font-size: var(--ma-font-xs);
  color: var(--ma-accent);
}

.ma-detail__puburl {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  font-family: 'JetBrains Mono', monospace;
  font-size: var(--ma-font-xs);
  color: var(--ma-text-1);
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 通道行 */
.ma-detail__ch {
  display: flex;
  align-items: center;
  gap: 8px;
  height: 26px;
  padding: 0 8px;
  border-radius: var(--ma-radius);
  background: var(--ma-bg-base);
}

.ma-detail__ch-name {
  flex: 1 1 auto;
  min-width: 0;
  font-size: var(--ma-font-sm);
  color: var(--ma-text-1);
}

.ma-detail__ch-type,
.ma-detail__ch-refs {
  flex: none;
}

.ma-detail__foot {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 12px;
  border-top: 1px solid var(--ma-border);
}

.ma-detail__foot .ma-detail__note {
  flex: 1 1 auto;
}

.ma-detail__btn {
  flex: none;
  padding: 3px 12px;
  border: 1px solid var(--ma-border-strong);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-base);
  color: var(--ma-text-1);
  cursor: pointer;
  font-size: var(--ma-font-sm);
}

.ma-detail__btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}

.ma-detail__btn--danger {
  border-color: var(--ma-status-error);
  color: var(--ma-status-error);
}
</style>
