<script setup lang="ts">
/**
 * 顶部工具栏（PRD §5.1 / §5.2）：
 * 看板切换 · 编辑模式 · 新建卡片 · 保存（含未保存标记） · 连接状态 · 主题切换。
 */
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { NButton, NInput, NModal, NRadioButton, NRadioGroup, NSelect, NSpace, NTag, useDialog } from 'naive-ui'
import { useDashboardStore } from '@/stores/dashboard'
import { useMapBoardStore } from '@/stores/mapBoard'
import { useSettingsStore } from '@/stores/settings'
import { useUiStore } from '@/stores/ui'
import { TOP_BAR_HEIGHT } from '@/utils/constants'
import { formatDuration } from '@/utils/format'
import { useDashboardSave } from '@/composables/useDashboardSave'
import { useCardCreate } from '@/composables/useCardCreate'
import WeatherBadge from '@/components/layout/WeatherBadge.vue'
import SettingsDrawer from '@/components/config/SettingsDrawer.vue'

const router = useRouter()
const dashboardStore = useDashboardStore()
const mapBoardStore = useMapBoardStore()
const settings = useSettingsStore()
const ui = useUiStore()
const dialog = useDialog()
const { openCreate: openCardCreate } = useCardCreate()
const { dirty, saving, save } = useDashboardSave()

/* ——— 品牌（来自全局设置，不再是硬编码） ——— */

/** 项目名：留空回落内置默认名，保证顶栏永远不会出现空白品牌区。 */
const brandTitle = computed<string>(() => settings.brandTitle)
const brandSubtitle = computed<string>(() => settings.brandSubtitle)
/** Logo 地址统一走 store 的 logoSrc（含"上传后缓存破坏"处理，见 stores/settings.ts）。 */
const logoSrc = computed<string>(() => settings.logoSrc)

/**
 * Logo 图片加载失败标志。
 *
 * 为什么必须显式兜底：logoUrl 可能指向后端「当前 Logo」端点，而该端点在没有 Logo 时
 * 返回 404 —— 不处理就会显示一张破图（浏览器默认的裂图图标），
 * 用户只会觉得"系统坏了"，而不是"我没配 Logo"。
 */
const logoBroken = ref(false)
watch(logoSrc, () => {
  logoBroken.value = false
})

const settingsOpen = ref(false)

/**
 * 构建标记：从入口 chunk 的 URL 里取内容哈希（`/assets/index-<hash>.js` → `<hash>`）。
 *
 * 为什么需要它：Go 的前端产物是编译期 go:embed 进二进制的，
 * **重启服务不会重载已经打开的浏览器标签页**。于是"我明明重启了服务，
 * 为什么还是旧界面"会反复发生，而用户无从判断自己跑的是哪一版。
 * 把这个哈希显示在品牌区（悬停可见），一眼就能对齐"服务端版本 vs 浏览器版本"。
 * 零配置：`import.meta.url` 就是入口模块自己的 URL。
 */
const buildTag = computed<string>(() => {
  try {
    const name = new URL(import.meta.url).pathname.split('/').pop() ?? ''
    const m = /index-([A-Za-z0-9_-]+)\.js$/.exec(name)
    return m?.[1] ? m[1] : name
  } catch {
    return 'dev'
  }
})

/** 当前是否在地图看板（决定编辑模式与新建卡片走哪个 store）。 */
const isMapBoard = computed<boolean>(() => dashboardStore.current?.kind === 'map')

/**
 * 下拉里用 ▦ / ◈ 前缀区分两种看板。
 *
 * 为什么不用分组选项：naive-ui 的 NSelect 没有 group 选项结构，
 * 为此引入自定义渲染反而更脆。前缀 + 排序（网格在前、地图在后）已足够清晰。
 */
const boardOptions = computed(() =>
  [...dashboardStore.dashboards]
    .sort((a, b) => {
      const ka = a.kind === 'map' ? 1 : 0
      const kb = b.kind === 'map' ? 1 : 0
      return ka - kb
    })
    .map((d) => ({
      label: `${d.kind === 'map' ? '◈' : '▦'} ${d.name}`,
      value: d.id,
    })),
)
const currentId = computed(() => dashboardStore.current?.id)

const renameOpen = ref(false)
const renameValue = ref('')
const creating = ref(false)
const createKind = ref<'grid' | 'map'>('grid')

/**
 * 切换看板。
 *
 * 🚨 历史缺陷（H9/H14）：原实现是 `window.history.replaceState(null,'','/dashboard/'+id)`
 * —— 绕过 vue-router 且硬编码路径。加了 /mapboard 之后，从地图看板切到网格看板会写出
 * `/dashboard/B` 的URL，但**画面仍是 MapBoardView**（router 没收到通知），
 * 用户一刷新就突变。这属于"URL 与实际渲染组件分裂"的真实状态 bug。
 * 现在改为 router.replace，并按 kind 分派 store。
 */
async function switchBoard(id: string): Promise<void> {
  // 列表里找不到就**不要猜**。
  // 之前的写法是 `target?.kind === 'map' ? 'map' : 'grid'` —— 列表没加载时 target 为 undefined，
  // 于是任何看板都被当成网格，跳到 /dashboard/<地图看板id>，由网格视图渲染一块空页面。
  // 这正是"刷新后自动变更到一个不存在的看板"的来源之一：**静默跳到错误的视图**。
  const target = dashboardStore.dashboards.find((d) => d.id === id)
  if (!target) {
    ui.warning('看板列表尚未就绪，请稍后再试（或刷新页面）')
    return
  }
  const kind = target.kind === 'map' ? 'map' : 'grid'
  await router.replace({ name: kind === 'map' ? 'mapboard' : 'dashboard', params: { id } })
}

function openCreate(): void {
  renameValue.value = ''
  creating.value = true
  // 默认按当前所在看板的类型，減少误建
  createKind.value = isMapBoard.value ? 'map' : 'grid'
  renameOpen.value = true
}

function openRename(): void {
  renameValue.value = dashboardStore.current?.name ?? ''
  creating.value = false
  renameOpen.value = true
}

async function confirmRename(): Promise<void> {
  const name = renameValue.value.trim()
  if (!name) return
  try {
    if (creating.value) {
      const board = await dashboardStore.create(name, createKind.value)
      renameOpen.value = false
      ui.success(createKind.value === 'map' ? '地图看板已创建' : '看板已创建')
      await dashboardStore.loadList()
      await switchBoard(board.id)
    } else {
      const id = dashboardStore.current?.id
      if (id) {
        await dashboardStore.rename(id, name)
        renameOpen.value = false
        ui.success('已重命名')
      }
    }
  } catch (err) {
    ui.handleError(err, '看板操作失败')
  }
}

function confirmDelete(): void {
  const id = dashboardStore.current?.id
  if (!id) return
  dialog.warning({
    title: '删除看板',
    content: `确定删除「${dashboardStore.current?.name ?? ''}」？该操作不可恢复。`,
    positiveText: '删除',
    negativeText: '取消',
    onPositiveClick: async () => {
      try {
        await dashboardStore.remove(id)
        const first = dashboardStore.dashboards[0]?.id
        if (first) await switchBoard(first)
        ui.success('看板已删除')
      } catch (err) {
        ui.handleError(err, '删除失败')
      }
    },
  })
}

/**
 * 新建卡片。
 *
 * 规则全部收敛在 `useCardCreate`：网格看板直接建；地图看板必须先选中节点，
 * 否则会造出无锚点的孤儿卡（不渲染也不订阅）。这里不再自己拼参数，
 * 免得和左侧面板、画布拖放这几处入口的规则各自漂移。
 */
function newCard(): void {
  openCardCreate()
}

function toggleEdit(): void {
  // 编辑模式按容器分派：地图看板走 mapBoardStore，网格走 dashboardStore
  if (isMapBoard.value) mapBoardStore.setEditMode(!mapBoardStore.editMode)
  else dashboardStore.setEditMode(!dashboardStore.editMode)
}

const editOn = computed<boolean>(() =>
  isMapBoard.value ? mapBoardStore.editMode : dashboardStore.editMode,
)

const connLabel = computed(() => {
  switch (ui.connPhase) {
    case 'open':
      return `后端在线 ${formatDuration(ui.wsLatencyMs)}`
    case 'connecting':
      return '正在连接后端…'
    case 'reconnecting':
      return '重连中…'
    default:
      return '后端离线'
  }
})

const connType = computed<'success' | 'warning' | 'error' | 'default'>(() => {
  switch (ui.connPhase) {
    case 'open':
      return 'success'
    case 'connecting':
    case 'reconnecting':
      return 'warning'
    default:
      return 'error'
  }
})
</script>

<template>
  <header class="ma-topbar" :style="{ height: `${TOP_BAR_HEIGHT}px` }">
    <div class="ma-topbar__brand">
      <img
        v-if="logoSrc && !logoBroken"
        :src="logoSrc"
        class="ma-topbar__logo-img"
        :alt="brandTitle"
        @error="logoBroken = true"
      />
      <span v-else class="ma-topbar__logo">{{ brandTitle }}</span>
      <span v-if="brandSubtitle" class="ma-topbar__sub">{{ brandSubtitle }}</span>
      <span class="ma-topbar__build" :title="`前端构建版本 ${buildTag}（用于确认浏览器是否加载了最新产物）`">
        {{ buildTag.slice(0, 7) }}
      </span>
    </div>

    <n-space align="center" :size="8" class="ma-topbar__board">
      <n-select
        :value="currentId"
        :options="boardOptions"
        size="small"
        class="ma-topbar__select"
        @update:value="(v: string) => switchBoard(v)"
      />
      <n-button size="tiny" quaternary @click="openCreate">新建</n-button>
      <n-button size="tiny" quaternary :disabled="!currentId" @click="openRename">重命名</n-button>
      <n-button size="tiny" quaternary type="error" :disabled="!currentId" @click="confirmDelete">删除</n-button>
    </n-space>

    <n-space align="center" :size="8" class="ma-topbar__ops">
      <n-button size="small" :type="editOn ? 'primary' : 'default'" @click="toggleEdit">
        {{ editOn ? '◉ 编辑中' : '编辑模式' }}
      </n-button>
      <n-button size="small" secondary :title="isMapBoard ? '地图看板上卡片挂在节点上，需先选中一个节点' : '新建一张监控卡片'" @click="newCard">
        {{ isMapBoard ? '+ 挂卡片到节点' : '+ 新建卡片' }}
      </n-button>
      <n-button size="small" type="primary" :loading="saving" @click="save">
        保存
        <span v-if="dirty" class="ma-topbar__dot" title="有未保存改动"></span>
      </n-button>
    </n-space>

    <n-space align="center" :size="8" class="ma-topbar__status">
      <weather-badge />
      <n-tag size="small" :type="connType" round>{{ connLabel }}</n-tag>
      <n-button size="tiny" quaternary title="切换主题" @click="ui.toggleTheme()">
        {{ ui.themeKind === 'dark' ? '◐' : '◑' }}
      </n-button>
      <n-button size="tiny" quaternary title="证书信任指引" @click="$router.push('/trust')">⛨</n-button>
      <n-button size="tiny" quaternary title="全局设置（品牌 / 天气）" @click="settingsOpen = true">⚙</n-button>
    </n-space>

    <n-modal v-model:show="renameOpen" preset="dialog" :title="creating ? '新建看板' : '重命名看板'">
      <n-space vertical :size="10">
        <n-radio-group v-if="creating" v-model:value="createKind" size="small">
          <n-radio-button value="grid">▦ 网格看板</n-radio-button>
          <n-radio-button value="map">◈ 地图看板</n-radio-button>
        </n-radio-group>
        <n-input v-model:value="renameValue" placeholder="看板名称" maxlength="40" @keydown.enter="confirmRename" />
        <p v-if="creating && createKind === 'map'" class="ma-text-xs ma-text-3">
          地图看板以地图为底图：节点绑定位置通道，卡片跟随节点，点击节点展开。
        </p>
      </n-space>
      <template #action>
        <n-space justify="end">
          <n-button size="small" quaternary @click="renameOpen = false">取消</n-button>
          <n-button size="small" type="primary" @click="confirmRename">确定</n-button>
        </n-space>
      </template>
    </n-modal>

    <!--
      设置抽屉挂在顶栏里：它是全局能力，挂 TopBar 一处即可覆盖网格/地图两个视图，
      不必在两个 View 各挂一遍（也就不会出现"某个视图忘了挂"的遗漏）。
      抽屉自身 position:fixed，不参与顶栏的 flex 排布。
    -->
    <settings-drawer :open="settingsOpen" @close="settingsOpen = false" />
  </header>
</template>

<style scoped>
.ma-topbar {
  display: flex;
  align-items: center;
  gap: 16px;
  padding: 0 12px;
  background: var(--ma-bg-card);
  border-bottom: 1px solid var(--ma-border);
  flex: none;
}

.ma-topbar__brand {
  display: flex;
  align-items: center; /* Logo 是图片，按 baseline 对齐会和文字错开 */
  gap: 8px;
  min-width: 168px;
  max-width: 320px;
}

/*
 * Logo 图片：只限高不限宽（宽度由图片自身比例决定），
 * 并强制 max-width —— 否则一张超宽图会把看板下拉框挤出视口。
 */
.ma-topbar__logo-img {
  max-height: 22px;
  max-width: 140px;
  object-fit: contain;
}

.ma-topbar__logo {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 15px;
  font-weight: 700;
  color: var(--ma-accent);
  letter-spacing: 0.4px;
}

.ma-topbar__sub {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: var(--ma-font-xs);
  color: var(--ma-text-3);
}

/* 前端构建版本标记：用于快速判断浏览器是否加载了最新产物 */
.ma-topbar__build {
  padding: 0 4px;
  border: 1px solid var(--ma-border);
  border-radius: 3px;
  color: var(--ma-text-3);
  cursor: help;
  font-size: 10px;
  font-family: var(--ma-font-mono, monospace);
  line-height: 14px;
}

.ma-topbar__select {
  min-width: 180px;
  max-width: 240px;
}

.ma-topbar__ops,
.ma-topbar__status {
  margin-left: auto;
}

.ma-topbar__ops {
  margin-left: 12px;
}

.ma-topbar__dot {
  display: inline-block;
  width: 6px;
  height: 6px;
  margin-left: 4px;
  border-radius: 50%;
  background: var(--ma-status-reconnecting);
  vertical-align: middle;
}
</style>
