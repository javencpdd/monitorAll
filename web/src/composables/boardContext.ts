/**
 * BoardContext：卡片宿主的「所在容器」契约（解耦 CardHost ↔ dashboardStore）。
 *
 * 背景（基线 H3）：`CardHost.vue` 原先直读 `dashboardStore.editMode` 与
 * `dashboardStore.removeCard`，导致渲染器体系无法被地图看板复用。
 *
 * ⭐ 关键：**缺省实现**让 `inject` 不到时自动回落到 `dashboardStore`，
 *    因此网格看板路径 **零回归**——不 provide 时行为与改造前 100% 一致。
 *
 * ⚠️ 命名警告（note/03 §3.1）：`useBoardContext()` 的返回值**不要**命名为
 *    `BoardContext`（大写开头会被模板当成组件）。请统一用 `board`。
 */
import { computed, inject, provide, type ComputedRef, type InjectionKey, type Ref } from 'vue'
import { useDashboardStore } from '@/stores/dashboard'
import type { Card, UpdateCardReq } from '@/types'

/** 几何项（grid：格单位；map：容器 px）。 */
export interface GeometryItem {
  id: string
  x: number
  y: number
  w: number
  h: number
}

export interface BoardContext {
  /** 容器种类：网格 / 地图。 */
  kind: 'grid' | 'map'
  editMode: Ref<boolean>
  selectedCardId: Ref<string | undefined>
  selectCard(id?: string): void
  patchCard(id: string, body: UpdateCardReq): Promise<Card>
  removeCard(id: string): Promise<void>
  /** 保存几何（grid → /layout；map → /geometry 串行合并队列）。 */
  saveGeometry(items: GeometryItem[]): Promise<void>
  /** 该容器是否允许绑定某渲染器（地图看板禁止 map，C11）。 */
  allowsRenderer?(type: string): boolean
}

export const BoardContextKey: InjectionKey<BoardContext> = Symbol('ma:board-context')

export function provideBoardContext(ctx: BoardContext): void {
  provide(BoardContextKey, ctx)
}

/**
 * 取当前容器的 BoardContext；无 provide 时返回 dashboardStore 缺省实现。
 */
export function useBoardContext(): BoardContext {
  const ctx = inject(BoardContextKey, null)
  if (ctx) return ctx
  const ds = useDashboardStore()
  return {
    kind: 'grid',
    editMode: computed(() => ds.editMode),
    selectedCardId: computed(() => ds.selectedCardId),
    selectCard: (id?: string) => ds.selectCard(id),
    patchCard: (id: string, body: UpdateCardReq) => ds.patchCard(id, body),
    removeCard: (id: string) => ds.removeCard(id),
    saveGeometry: async (items: GeometryItem[]) => {
      await ds.saveLayoutNow(items.map((i) => ({ id: i.id, x: i.x, y: i.y, w: i.w, h: i.h })))
    },
  }
}

/**
 * 只读用法：只关心 editMode 的组件（如渲染器）用它，避免误改容器状态。
 */
export function useBoardEditMode(): ComputedRef<boolean> {
  const board = useBoardContext()
  return computed(() => board.editMode.value)
}
