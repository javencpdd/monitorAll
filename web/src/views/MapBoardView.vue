<script setup lang="ts">
/**
 * 地图看板视图（v3 M3/M4）：TopBar + 数据源面板 + 地图舞台 + 节点面板。
 *
 * 与网格看板共用 TopBar / ConnectionBanner / SourcePanel / 配置抽屉 / 数据源向导 ——
 * 只有"舞台"内部换成 MapBoardSurface（内核不复制，C2）。
 */
import { computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useDashboardStore } from '@/stores/dashboard'
import { useDatasourceStore } from '@/stores/datasource'
import { useMapBoardStore } from '@/stores/mapBoard'
import { useUiStore } from '@/stores/ui'
import { useHotkeys } from '@/composables/useHotkeys'
import { provideBoardContext } from '@/composables/boardContext'
import TopBar from '@/components/layout/TopBar.vue'
import ConnectionBanner from '@/components/layout/ConnectionBanner.vue'
import SourcePanel from '@/components/layout/SourcePanel.vue'
import MapBoardSurface from '@/components/mapboard/MapBoardSurface.vue'
import MapNodeInspector from '@/components/mapboard/MapNodeInspector.vue'
import CardConfigDrawer from '@/components/config/CardConfigDrawer.vue'
import DataSourceWizard from '@/components/config/DataSourceWizard.vue'
import DiagLayer from '@/components/diag/DiagLayer.vue'
import EmptyState from '@/components/base/EmptyState.vue'

const route = useRoute()
const router = useRouter()
const dashboardStore = useDashboardStore()
const datasourceStore = useDatasourceStore()
const mapBoard = useMapBoardStore()
const ui = useUiStore()

const currentId = computed<string | undefined>(() => (route.params.id as string | undefined) ?? undefined)

/**
 * 容器契约：地图看板 provide 自己的实现。
 * 渲染器与 CardHost inject 到这里 → 同一套渲染器直接复用，零复制。
 */
provideBoardContext({
  kind: 'map',
  editMode: computed(() => mapBoard.editMode),
  selectedCardId: computed(() => mapBoard.selectedCardId),
  selectCard: (id?: string) => mapBoard.selectCard(id),
  patchCard: (id, body) => dashboardStore.patchCard(id, body),
  removeCard: (id) => dashboardStore.removeCard(id),
  saveGeometry: async () => {
    // 几何走 mapBoardStore 的串行合并队列（不进 revision 域，C7）
    await mapBoard.flushGeometry()
  },
  // C11：地图看板内禁止绑定 map 渲染器（会创建第二个 AMap 实例）
  allowsRenderer: (type: string) => type !== 'map',
})

async function ensureBoard(): Promise<void> {
  const id = currentId.value
  if (!id) {
    // 未指定 id：地图看板必须有 id（不能像网格那样落到"默认看板"）
    await router.replace({ name: 'dashboard' })
    return
  }
  try {
    await mapBoard.hydrate(id)
  } catch (err) {
    ui.handleError(err, '地图看板加载失败')
    return
  }
  // kind 守卫：手输 /mapboard/<网格板id> 时前端就地纠正，用户不会撞到 40003
  if (dashboardStore.current?.kind !== 'map') {
    await router.replace({ name: 'dashboard', params: { id } })
    return
  }
  // ⚠️ 必须加载看板列表：顶栏的看板切换下拉与"◈/▦"前缀都读 dashboardStore.dashboards。
  // 不加载的话，在地图看板上那个下拉是**空的** —— 用户没法从这里切走，也看不到自己在哪里。
  dashboardStore.loadList().catch(() => undefined)
  datasourceStore.ensureAllChannels().catch(() => undefined)
}

onMounted(() => {
  void ensureBoard()
})

watch(currentId, () => {
  void ensureBoard()
})

useHotkeys({
  onEscape: () => {
    if (ui.cardDrawer.open) ui.closeCardDrawer()
    else if (ui.sourceWizard.open) ui.closeSourceWizard()
    else if (mapBoard.openCardIds.length > 0) mapBoard.closeAllCards()
    else if (mapBoard.selectedNodeId) mapBoard.selectNode(undefined)
    else if (mapBoard.editMode) mapBoard.setEditMode(false)
  },
  onToggleEdit: () => mapBoard.setEditMode(!mapBoard.editMode),
})
</script>

<template>
  <div class="ma-view">
    <top-bar />
    <div class="ma-view__body">
      <connection-banner />
      <div class="ma-view__main">
        <source-panel />
        <div class="ma-view__stage">
          <map-board-surface v-if="mapBoard.error === ''" />
          <empty-state
            v-else
            icon="error"
            title="地图看板加载失败"
            :description="mapBoard.error"
          >
            <template #action>
              <button class="ma-view__retry" type="button" @click="ensureBoard">重试</button>
            </template>
          </empty-state>
          <map-node-inspector />
          <card-config-drawer />
          <data-source-wizard />
          <!-- H6：正式可开关诊断层（默认 off，URL ?diag=1/2 或 Ctrl+Shift+D） -->
          <diag-layer title="地图看板" />
        </div>
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

.ma-view__body {
  position: relative;
  display: flex;
  flex-direction: column;
  flex: 1 1 auto;
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

.ma-view__retry {
  padding: 4px 12px;
  border: 1px solid var(--ma-accent);
  border-radius: var(--ma-radius);
  background: var(--ma-accent-dim);
  color: var(--ma-accent);
  cursor: pointer;
}
</style>