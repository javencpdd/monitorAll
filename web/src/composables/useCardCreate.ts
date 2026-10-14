/**
 * 统一的「新建卡片」出口。
 *
 * ⭐ 为什么必须收敛成一个处地方：
 * 项目里能触发建卡的入口有好几个 —— 顶栏「+ 新建卡片」、左侧数据源面板的通道、
 * 画布上的拖放（把通道拖进来）、以及网格空态的「+」。
 * 而「地图看板上的卡片必须挂在节点上」这条规则对所有入口都成立：
 * **少传一个 nodeId 就会造出未锚定孤儿卡** —— 它不渲染也不订阅，
 * 用户看到的是"我建了卡片但图上没有"，接口则直接 40001 拒绝。
 *
 * 规则只要写漏一处，那个入口就会安静地失败。
 * 所以这里把"要不要有 nodeId、卡片数软上限、抽屉参数怎么拼"全部收进来，
 * 入口只负责调用 `openCreate(channelId?)`。
 */
import { computed } from 'vue'
import { useDashboardStore } from '@/stores/dashboard'
import { useMapBoardStore } from '@/stores/mapBoard'
import { useUiStore } from '@/stores/ui'
import { CARD_SOFT_LIMIT } from '@/utils/constants'

export function useCardCreate() {
  const dashboardStore = useDashboardStore()
  const mapBoardStore = useMapBoardStore()
  const ui = useUiStore()

  const isMapBoard = computed<boolean>(() => dashboardStore.current?.kind === 'map')

  /**
   * 打开「新建卡片」抽屉。
   *
   * @param channelId 可选，预选要展示的通道（左侧点某个通道、或拖某个通道进来时用它）
   * @returns 是否真的打开了；`false` 表示被拦下，且**已经给出可见提示**
   *          （调用方不需要再自己补提示）
   */
  function openCreate(channelId?: string): boolean {
    if (isMapBoard.value) {
      // 地图看板：卡片必须挂在节点上，否则是孤儿卡（不渲染、不订阅）。
      // 先要一个节点，而不是让用户建出一张永远看不见的卡。
      const nodeId = mapBoardStore.selectedNodeId
      if (!nodeId) {
        ui.warning(
          '地图看板上的卡片要挂在节点上：请先在右侧「节点」面板点一下目标节点，再新建卡片',
        )
        return false
      }
      ui.openCardDrawer(undefined, 'create', channelId, nodeId)
      return true
    }

    if (dashboardStore.cards.length >= CARD_SOFT_LIMIT) {
      ui.warning(`卡片数已达 ${dashboardStore.cards.length} 张，卡片较多可能影响流畅度`)
    }
    ui.openCardDrawer(undefined, 'create', channelId)
    return true
  }

  return { isMapBoard, openCreate }
}
