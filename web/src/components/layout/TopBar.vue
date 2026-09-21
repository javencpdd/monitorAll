<script setup lang="ts">
/**
 * 顶部工具栏（PRD §5.1 / §5.2）：
 * 看板切换 · 编辑模式 · 新建卡片 · 保存（含未保存标记） · 连接状态 · 主题切换。
 */
import { computed, ref } from 'vue'
import { NButton, NInput, NModal, NSelect, NSpace, NTag, useDialog } from 'naive-ui'
import { useDashboardStore } from '@/stores/dashboard'
import { useUiStore } from '@/stores/ui'
import { CARD_SOFT_LIMIT, TOP_BAR_HEIGHT } from '@/utils/constants'
import { formatDuration } from '@/utils/format'
import { useDashboardSave } from '@/composables/useDashboardSave'

const dashboardStore = useDashboardStore()
const ui = useUiStore()
const dialog = useDialog()
const { dirty, saving, save } = useDashboardSave()

const boardOptions = computed(() =>
  dashboardStore.dashboards.map((d) => ({ label: d.name, value: d.id })),
)
const currentId = computed(() => dashboardStore.current?.id)

const renameOpen = ref(false)
const renameValue = ref('')
const creating = ref(false)

async function switchBoard(id: string): Promise<void> {
  await dashboardStore.select(id)
  window.history.replaceState(null, '', `/dashboard/${encodeURIComponent(id)}`)
}

function openCreate(): void {
  renameValue.value = ''
  creating.value = true
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
      const board = await dashboardStore.create(name)
      renameOpen.value = false
      ui.success('看板已创建')
      await switchBoard(board.id)
      await dashboardStore.loadList()
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

function newCard(): void {
  if (dashboardStore.cards.length >= CARD_SOFT_LIMIT) {
    ui.warning(`卡片数已达 ${dashboardStore.cards.length} 张，卡片较多可能影响流畅度`)
  }
  ui.openCardDrawer(undefined, 'create')
}

function toggleEdit(): void {
  dashboardStore.setEditMode(!dashboardStore.editMode)
}

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
      <span class="ma-topbar__logo">MonitorAll</span>
      <span class="ma-topbar__sub">多源异构监控看板</span>
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
      <n-button size="small" :type="dashboardStore.editMode ? 'primary' : 'default'" @click="toggleEdit">
        {{ dashboardStore.editMode ? '◉ 编辑中' : '编辑模式' }}
      </n-button>
      <n-button size="small" secondary @click="newCard">+ 新建卡片</n-button>
      <n-button size="small" type="primary" :loading="saving" @click="save">
        保存
        <span v-if="dirty" class="ma-topbar__dot" title="有未保存改动"></span>
      </n-button>
    </n-space>

    <n-space align="center" :size="8" class="ma-topbar__status">
      <n-tag size="small" :type="connType" round>{{ connLabel }}</n-tag>
      <n-button size="tiny" quaternary title="切换主题" @click="ui.toggleTheme()">
        {{ ui.themeKind === 'dark' ? '◐' : '◑' }}
      </n-button>
      <n-button size="tiny" quaternary title="证书信任指引" @click="$router.push('/trust')">⛨</n-button>
    </n-space>

    <n-modal v-model:show="renameOpen" preset="dialog" :title="creating ? '新建看板' : '重命名看板'">
      <n-input v-model:value="renameValue" placeholder="看板名称" maxlength="40" @keydown.enter="confirmRename" />
      <template #action>
        <n-space justify="end">
          <n-button size="small" quaternary @click="renameOpen = false">取消</n-button>
          <n-button size="small" type="primary" @click="confirmRename">确定</n-button>
        </n-space>
      </template>
    </n-modal>
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
  align-items: baseline;
  gap: 8px;
  min-width: 168px;
}

.ma-topbar__logo {
  font-size: 15px;
  font-weight: 700;
  color: var(--ma-accent);
  letter-spacing: 0.4px;
}

.ma-topbar__sub {
  font-size: var(--ma-font-xs);
  color: var(--ma-text-3);
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
