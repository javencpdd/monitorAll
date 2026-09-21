<script setup lang="ts">
/**
 * 主视图（PRD §5.1）：TopBar + ConnectionBanner + SourcePanel + Canvas + 抽屉/向导。
 * 路由参数变化时切换看板；URL 与看板保持同步。
 */
import { computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useDashboardStore } from '@/stores/dashboard'
import { useDatasourceStore } from '@/stores/datasource'
import { useUiStore } from '@/stores/ui'
import { useHotkeys } from '@/composables/useHotkeys'
import { CARD_SOFT_LIMIT } from '@/utils/constants'
import TopBar from '@/components/layout/TopBar.vue'
import ConnectionBanner from '@/components/layout/ConnectionBanner.vue'
import SourcePanel from '@/components/layout/SourcePanel.vue'
import DashboardCanvas from '@/components/layout/DashboardCanvas.vue'
import CardConfigDrawer from '@/components/config/CardConfigDrawer.vue'
import DataSourceWizard from '@/components/config/DataSourceWizard.vue'

const route = useRoute()
const router = useRouter()
const dashboardStore = useDashboardStore()
const datasourceStore = useDatasourceStore()
const ui = useUiStore()

const currentId = computed<string | undefined>(() => (route.params.id as string | undefined) ?? undefined)

async function ensureBoard(): Promise<void> {
  const target = await dashboardStore.ensureSelected(currentId.value)
  if (target && target !== currentId.value) {
    await router.replace({ name: 'dashboard', params: { id: target } })
  }
  datasourceStore.ensureAllChannels().catch(() => undefined)
}

onMounted(() => {
  void ensureBoard()
})

watch(currentId, () => {
  void ensureBoard()
})

useHotkeys({
  onSave: () => void dashboardStore.save().catch(() => undefined),
  onEscape: () => {
    if (ui.cardDrawer.open) ui.closeCardDrawer()
    else if (ui.sourceWizard.open) ui.closeSourceWizard()
    else if (dashboardStore.editMode) dashboardStore.setEditMode(false)
  },
  onToggleEdit: () => dashboardStore.setEditMode(!dashboardStore.editMode),
})

watch(
  () => dashboardStore.cards.length,
  (count) => {
    if (count > CARD_SOFT_LIMIT) ui.warning(`卡片数已达 ${count} 张，卡片较多可能影响流畅度`)
  },
)
</script>

<template>
  <div class="ma-view">
    <top-bar />
    <connection-banner />
    <div class="ma-view__main">
      <source-panel />
      <div class="ma-view__stage">
        <dashboard-canvas />
        <card-config-drawer />
        <data-source-wizard />
      </div>
    </div>
  </div>
</template>

<style scoped>
.ma-view {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.ma-view__main {
  display: flex;
  flex: 1 1 auto;
  min-height: 0;
}

.ma-view__stage {
  position: relative;
  flex: 1 1 auto;
  min-width: 0;
  min-height: 0;
  display: flex;
  overflow: hidden;
}
</style>
