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
import { computed, ref } from 'vue'
import type { DataSourceKind, PayloadType, Protocol, TestResult } from '@/types'
import { useDatasourceStore } from '@/stores/datasource'
import { useUiStore } from '@/stores/ui'
import { AppError } from '@/api/client'
import { HTTP_URL_RE, PAYLOAD_TYPE_TEXT, RTMP_URL_RE, TEST_CONNECT_TIMEOUT_MS, WS_URL_RE } from '@/utils/constants'

const datasourceStore = useDatasourceStore()
const ui = useUiStore()

const open = computed<boolean>(() => ui.sourceWizard.open)
const step = ref<1 | 2>(1)

const kind = ref<DataSourceKind>('ros')
const name = ref<string>('')
const rosVersion = ref<'ros1' | 'ros2'>('ros2')
const rtmpUrl = ref<string>('')
const preferredProtocol = ref<'webrtc' | 'hls' | 'flv'>('webrtc')
const audio = ref<boolean>(false)
const bridgeUrl = ref<string>('ws://172.31.68.227:9090')
const domainId = ref<number>(0)
const topicsText = ref<string>('')
const httpUrl = ref<string>('http://')
const httpMethod = ref<'GET' | 'POST'>('GET')
const intervalMs = ref<number>(1000)
const jsonPath = ref<string>('')
const timeoutMs = ref<number>(3000)
const httpBody = ref<string>('')

const testing = ref(false)
const creating = ref(false)
const testResult = ref<TestResult | undefined>(undefined)

const KINDS: { value: DataSourceKind; label: string; desc: string }[] = [
  { value: 'video', label: '视频流（RTMP）', desc: '经 MediaMTX 转封装为 WebRTC/HLS，像素不过后端' },
  { value: 'ros', label: 'ROS 话题', desc: '经 rosbridge 订阅 ROS1 / ROS2 话题' },
  { value: 'http', label: 'HTTP 轮询接口', desc: '按间隔轮询自建遥测服务并提取字段' },
]

const protocol = computed<Protocol>(() => {
  if (kind.value === 'video') return 'rtmp'
  if (kind.value === 'ros') return rosVersion.value === 'ros1' ? 'ros1' : 'ros2'
  return 'http-poll'
})

/** 实时 URL 校验：非法立即红字。 */
const urlError = computed<string>(() => {
  if (kind.value === 'video') {
    if (rtmpUrl.value.length === 0) return ''
    return RTMP_URL_RE.test(rtmpUrl.value.trim()) ? '' : 'RTMP 地址格式应为 rtmp://host:1936/path'
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
  if (kind.value === 'video' && rtmpUrl.value.trim().length === 0) return '请填写 RTMP 地址'
  if (kind.value === 'ros' && bridgeUrl.value.trim().length === 0) return '请填写 rosbridge 地址'
  if (kind.value === 'http' && httpUrl.value.trim().length === 0) return '请填写接口地址'
  if (urlError.value) return urlError.value
  return ''
})

const connParams = computed<Record<string, unknown>>(() => {
  if (kind.value === 'video') {
    return { rtmpUrl: rtmpUrl.value.trim(), preferredProtocol: preferredProtocol.value, audio: audio.value }
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

async function submit(): Promise<void> {
  if (formError.value) {
    ui.warning(formError.value)
    return
  }
  creating.value = true
  try {
    await datasourceStore.createSource({
      name: name.value.trim(),
      kind: kind.value,
      protocol: protocol.value,
      connParams: connParams.value,
    })
    ui.success('数据源已创建')
    ui.closeSourceWizard()
    step.value = 1
    name.value = ''
    testResult.value = undefined
  } catch (err) {
    ui.handleError(err, '创建数据源失败')
  } finally {
    creating.value = false
  }
}

function pickKind(next: DataSourceKind): void {
  kind.value = next
  step.value = 2
  testResult.value = undefined
  if (name.value.length === 0) {
    const preset: Record<DataSourceKind, string> = { video: 'lite3 相机', ros: 'ROS遥测', http: 'HTTP接口' }
    name.value = preset[next]
  }
}

function close(): void {
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
        <!-- Step1：类型 -->
        <div v-if="step === 1" class="ma-wizard__kinds">
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

        <!-- Step2：参数 -->
        <div v-else class="ma-wizard__form">
          <div class="ma-wizard__row">
            <label class="ma-wizard__label">名称</label>
            <input v-model="name" class="ma-wizard__input" placeholder="数据源名称" />
          </div>

          <!-- 视频 -->
          <template v-if="kind === 'video'">
            <div class="ma-wizard__row">
              <label class="ma-wizard__label">RTMP 地址</label>
              <input v-model="rtmpUrl" class="ma-wizard__input" placeholder="rtmp://172.31.68.227:1936/live/lite3" />
            </div>
            <div class="ma-wizard__row">
              <label class="ma-wizard__label">首选协议</label>
              <select v-model="preferredProtocol" class="ma-wizard__input">
                <option value="webrtc">WebRTC（默认，≤0.5s）</option>
                <option value="hls">HLS（2-3s）</option>
                <option value="flv">HTTP-FLV（需外部网关）</option>
              </select>
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
        </div>
      </div>

      <footer class="ma-wizard__foot">
        <span v-if="formError && step === 2" class="ma-wizard__error ma-text-xs">{{ formError }}</span>
        <div class="ma-wizard__grow"></div>
        <button v-if="step === 2" class="ma-wizard__btn" @click="step = 1">上一步</button>
        <button v-if="step === 2" class="ma-wizard__btn" :disabled="testing" @click="test">
          {{ testing ? '测试中…' : '测试连接' }}
        </button>
        <button
          class="ma-wizard__btn ma-wizard__btn--primary"
          :disabled="creating || (step === 2 && Boolean(formError))"
          @click="step === 1 ? (step = 2) : submit()"
        >
          {{ creating ? '创建中…' : step === 1 ? '下一步' : '创建数据源' }}
        </button>
      </footer>
    </div>
  </div>
</template>

<style scoped>
.ma-wizard-mask {
  position: absolute;
  inset: 0;
  z-index: 40;
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
