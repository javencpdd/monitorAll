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

app.use(router)
app.mount('#app')
