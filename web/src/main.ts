import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import './styles/variables.css'
import './styles/global.css'
import { useDatasourceStore } from '@/stores/datasource'
import { useDashboardStore } from '@/stores/dashboard'
import { useFrameStore } from '@/stores/frame'
import { useSettingsStore } from '@/stores/settings'
import { useUiStore } from '@/stores/ui'

/**
 * 入口：createApp + Pinia + Router。
 * 🚨 必须先实例化 frameStore/datasourceStore/dashboardStore，
 * 再让 uiStore.bootstrap() 建立 WS —— 保证 WS 消息处理器在连接前注册完毕。
 */
const app = createApp(App)
app.use(createPinia())

// 顺序敏感：帧处理器先注册，避免首帧丢失
useFrameStore()
const datasourceStore = useDatasourceStore()
useDashboardStore()
const uiStore = useUiStore()
/**
 * 全局设置：品牌（顶栏/标题）与天气挂件。
 *
 * 顺序说明：与 bootstrap 并行即可，不必 await —— store 内部会 watch
 * `ui.runtime.branding`，runtime 一到就把品牌顶上去（首屏不闪默认名），
 * GET /settings 返回后再以权威值覆盖，并顺带启动天气轮询。
 */
const settingsStore = useSettingsStore()

void uiStore.bootstrap()
void settingsStore.load()

/**
 * 数据源列表：左侧面板（SourcePanel）、通道下拉（ChannelSelect）、
 * 位置通道选择（MapNodeInspector）**全部**依赖它。
 *
 * ⚠️ store 里的 `loadSources()` 此前在 store 之外**零调用点** —— 于是 sources 恒为空，
 * 三处 UI 全空、接口却是 200、控制台无任何报错（用户症状：「20 个数据源只看到 1 个」，
 * 那 1 个还是本次会话手动新建的）。这里显式发起加载。
 *
 * 不 `await`（不阻塞挂载），但**必须 `catch` 并显式告知用户**：
 * 拉不到列表却静默，界面表现就是"面板是空的"，用户会误以为没有数据源而反复新建。
 * 放在 bootstrap 之后：与 `settingsStore.load()` 同一批，且此时 uiStore 已可发提示。
 */
void datasourceStore.loadSources().catch((err) => {
  uiStore.error(`加载数据源列表失败：${err instanceof Error ? err.message : String(err)}`)
})

// 运行时错误可视化：现场环境（局域网看板常无 devtools）里，未捕获异常若只落 console，
// 会表现为「界面空白 / 卡片不显示」却无从排查。这里把它同时打到 console 与界面提示。
window.addEventListener('error', (e) => {
  console.error('[global error]', e.error ?? e.message)
  uiStore.error(`运行时错误：${e.message}`)
})
window.addEventListener('unhandledrejection', (e) => {
  const reason = e.reason as { message?: string } | string | undefined
  console.error('[unhandled rejection]', reason)
  uiStore.error(`未处理的异步异常：${typeof reason === 'string' ? reason : (reason?.message ?? String(reason))}`)
})

/**
 * Vue **组件内部**异常的统一出口。
 *
 * ⚠️ 为什么必须有（上面两个 window 监听救不了这类问题）：
 *   Vue 的 setup / render 异常会被它内部的 `handleError` 捕获，只落 `console.error`，
 *   **不会**变成 window `error` 事件 —— 两个监听一个都收不到。
 *   症状是「组件静默消失，界面没有任何提示」：本项目的节点面板就这样隐身穿了一整个
 *   版本（`useMapRuntime()` 在兄弟组件里 inject 失败 → `setup()` 抛异常 → 面板从未挂载）。
 *
 * 去重限流：同一段消息 10 秒内只提示一次 —— 渲染错误每帧都会重抛，不加限流会把通知
 * 刷成风暴，用户既看不清内容也点不完，比不提示更糟。
 */
const VUE_ERROR_DEDUP_MS = 10_000
const lastVueErrorAt = new Map<string, number>()
app.config.errorHandler = (err, _instance, info) => {
  console.error('[vue error]', info, err)
  const message = err instanceof Error ? err.message : String(err)
  const key = `${info}:${message}`
  const now = Date.now()
  if (now - (lastVueErrorAt.get(key) ?? 0) < VUE_ERROR_DEDUP_MS) return
  // 防止"每次消息都不同"的异常把 Map 撑大（罕见，但代价极低）
  if (lastVueErrorAt.size > 64) lastVueErrorAt.clear()
  lastVueErrorAt.set(key, now)
  // 带上 `info`（setup / render function / …），直接指向出错环节，省掉"猜是哪儿挂了"
  uiStore.error(`Vue 组件错误（${info}）：${message}`)
}

app.use(router)
app.mount('#app')
