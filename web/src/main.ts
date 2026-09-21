import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import './styles/variables.css'
import './styles/global.css'
import { useDatasourceStore } from '@/stores/datasource'
import { useDashboardStore } from '@/stores/dashboard'
import { useFrameStore } from '@/stores/frame'
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
useDatasourceStore()
useDashboardStore()
const uiStore = useUiStore()

void uiStore.bootstrap()

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

app.use(router)
app.mount('#app')
