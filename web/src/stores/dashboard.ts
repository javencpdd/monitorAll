/**
 * 看板 Store（ARCH §10.1）：当前看板、卡片、布局、编辑模式、脏标记、保存。
 *
 * DB-04 乐观锁：保存时携带 revision，后端返回 40009 表示看板已被其它端修改，
 * 前端提示「看板已被修改，请刷新后重试」，**不做自动合并**。
 */
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import type { Card, CreateCardReq, Dashboard, DashboardWithCards, Layout, LayoutPatchItem, UpdateCardReq } from '@/types'
import * as api from '@/api/endpoints'
import { AppError } from '@/api/client'
import { DEFAULT_CARD_H, DEFAULT_CARD_W, MIN_CARD_H, MIN_CARD_W } from '@/utils/constants'
import { getManifest } from '@/renderers'
import { useFrameStore } from './frame'

export const useDashboardStore = defineStore('dashboard', () => {
  const dashboards = ref<Dashboard[]>([])
  const current = ref<DashboardWithCards | null>(null)
  const editMode = ref(false)
  /** 存在未保存改动（顶栏圆点 + beforeunload + Ctrl+S）。 */
  const dirty = ref(false)
  const selectedCardId = ref<string | undefined>(undefined)
  const saving = ref(false)
  const loading = ref(false)
  const error = ref('')
  /** 最近一次保存冲突提示（DB-04）。 */
  const revisionConflict = ref(false)

  const cards = computed<Card[]>(() => current.value?.cards ?? [])
  const selectedCard = computed<Card | undefined>(() => cards.value.find((c) => c.id === selectedCardId.value))
  const cardIds = computed<string[]>(() => cards.value.map((c) => c.id))
  const channelIds = computed<string[]>(() => Array.from(new Set(cards.value.map((c) => c.channelId))))

  function touch(): void {
    dirty.value = true
  }

  async function loadList(): Promise<Dashboard[]> {
    dashboards.value = await api.listDashboards()
    return dashboards.value
  }

  async function select(id: string): Promise<void> {
    loading.value = true
    try {
      current.value = await api.getDashboard(id)
      dirty.value = false
      revisionConflict.value = false
      error.value = ''
    } finally {
      loading.value = false
    }
  }

  /** D8：无看板时取第一个（后端保证已创建「默认看板」）。 */
  /**
   * 确保有一个选中的看板；未指定 id 时自动挑一个。
   *
   * ⚠️ `preferKind` 不是花哨选项，而是修一个真实缺陷：
   * 原来这里无条件兜底 `dashboards[0]`。当列表里第一个是**地图看板**时，
   * 从 `/` 或刷新进入会被选中并由**网格视图**渲染 —— 地图看板没有网格卡片，
   * 页面就是空的，用户会认为"这个看板不存在"。
   * 现在：只有同类看板一个都没有时，才退化成第一个（再由视图层的 kind 守卫转走）。
   */
  async function ensureSelected(id?: string, preferKind: 'grid' | 'map' = 'grid'): Promise<string | undefined> {
    await loadList()
    const explicit = id && dashboards.value.some((d) => d.id === id) ? id : undefined
    const target =
      explicit ??
      dashboards.value.find((d) => (d.kind ?? 'grid') === preferKind)?.id ??
      dashboards.value[0]?.id
    if (!target) return undefined
    await select(target)
    return target
  }

  async function create(name: string, kind: 'grid' | 'map' = 'grid'): Promise<DashboardWithCards> {
    const created = await api.createDashboard({ name, kind })
    dashboards.value.push(stripCards(created))
    current.value = created
    dirty.value = false
    return created
  }

  async function rename(id: string, name: string): Promise<void> {
    const updated = await api.updateDashboard(id, { name, revision: currentRevision(id) })
    applyDashboard(updated)
  }

  async function remove(id: string): Promise<void> {
    await api.deleteDashboard(id)
    dashboards.value = dashboards.value.filter((d) => d.id !== id)
    if (current.value?.id === id) {
      current.value = null
      dirty.value = false
    }
  }

  async function addCard(req: CreateCardReq): Promise<Card> {
    const id = current.value?.id
    if (!id) throw new AppError(-1, '尚未选中看板')
    const card = await api.createCard(id, req)
    if (current.value) current.value.cards.push(card)
    dirty.value = false
    return card
  }

  /**
   * 局部更新一张卡片（补丁语义）。
   *
   * ⚠️ **解绑节点必须显式送 `{ nodeId: '' }`**，不能靠"不传 nodeId"：
   * 后端把字段缺失 / nil 视为"不修改"，所以 `patchCard(id, { title: 'x' })` 之后
   * 节点归属**保持不变**；只有 `patchCard(id, { nodeId: '' })` 才真正解绑。
   *
   * 现状核对（E）：全仓 grep `nodeId` 写入点，**没有任何一处送过空串**
   * （`CardConfigDrawer` / `useCardCreate` / `ui.openCardDrawer` 全是 `nodeId ? { nodeId } : {}`，
   * 空值直接省略）。也就是说前端**当前没有"解绑"路径**，未锚定卡由「删除节点」的服务端级联产生。
   * 这里只是把语义钉在唯一入口上：将来要加解绑 UI，就从这个函数显式传 `''`，别绕。
   */
  async function patchCard(cardId: string, body: UpdateCardReq): Promise<Card> {
    const updated = await api.updateCard(cardId, body)
    replaceCard(updated)
    return updated
  }

  async function removeCard(cardId: string): Promise<void> {
    await api.deleteCard(cardId)
    if (current.value) {
      current.value.cards = current.value.cards.filter((c) => c.id !== cardId)
    }
    if (selectedCardId.value === cardId) selectedCardId.value = undefined
  }

  /** 本地立即更新（拖拽 / 抽屉应用），并置脏等待 Ctrl+S 落库。 */
  function replaceCard(updated: Card): void {
    if (!current.value) return
    const idx = current.value.cards.findIndex((c) => c.id === updated.id)
    if (idx >= 0) current.value.cards[idx] = updated
    else current.value.cards.push(updated)
    touch()
  }

  /** 布局变更：更新本地后立即轻量落库（拖拽后高频调用）。 */
  async function saveLayoutNow(layouts: LayoutPatchItem[]): Promise<void> {
    const board = current.value
    if (!board) return
    const map = new Map(layouts.map((l) => [l.id, l]))
    // 坐标完全未变化时直接返回：不替换 cards 数组。
    // 若替换，layout computed 会产生新引用传回 <grid-layout>，
    // 组件内部会再次 emit layout-updated → 无限循环 → 浏览器卡死。
    const changed = layouts.some((l) => {
      const c = board.cards.find((x) => x.id === l.id)
      return !c || c.layout.x !== l.x || c.layout.y !== l.y || c.layout.w !== l.w || c.layout.h !== l.h
    })
    if (!changed) return
    board.cards = board.cards.map((c) => {
      const patch = map.get(c.id)
      if (!patch) return c
      return { ...c, layout: { ...c.layout, x: patch.x, y: patch.y, w: patch.w, h: patch.h } }
    })
    touch()
    try {
      const updated = await api.saveLayout(board.id, board.revision, layouts)
      board.revision = updated.revision
      dirty.value = false
      revisionConflict.value = false
    } catch (err) {
      error.value = err instanceof AppError ? err.message : '布局保存失败'
      throw err
    }
  }

  /** 全量保存（Ctrl+S）：cards + 布局一次 PUT。 */
  async function save(): Promise<void> {
    const board = current.value
    if (!board) return
    saving.value = true
    try {
      const updated = await api.updateDashboard(board.id, {
        revision: board.revision,
        cards: board.cards,
        globalConfig: board.globalConfig,
      })
      applyDashboard(updated)
      dirty.value = false
      revisionConflict.value = false
      error.value = ''
    } catch (err) {
      if (err instanceof AppError && err.code === 40009) {
        revisionConflict.value = true
        error.value = '看板已被修改，请刷新后重试'
      } else {
        error.value = err instanceof AppError ? err.message : '保存失败'
      }
      throw err
    } finally {
      saving.value = false
    }
  }

  function applyDashboard(withCards: DashboardWithCards): void {
    current.value = withCards
    const idx = dashboards.value.findIndex((d) => d.id === withCards.id)
    if (idx >= 0) dashboards.value[idx] = stripCards(withCards)
    else dashboards.value.push(stripCards(withCards))
  }

  function currentRevision(id: string): number {
    return current.value?.id === id ? current.value.revision : dashboards.value.find((d) => d.id === id)?.revision ?? 1
  }

  function stripCards(withCards: DashboardWithCards): Dashboard {
    const { cards: _omit, ...rest } = withCards
    return rest
  }

  /** 计算新建卡片的落位：接在当前最低行下方，尺寸取渲染器 manifest 默认值。 */
  function nextFreeLayout(rendererType: string): Layout {
    const dl = getManifest(rendererType)?.defaultLayout
    return {
      x: 0,
      y: nextRow(),
      w: dl?.w ?? DEFAULT_CARD_W,
      h: dl?.h ?? DEFAULT_CARD_H,
      minW: dl?.minW ?? MIN_CARD_W,
      minH: dl?.minH ?? MIN_CARD_H,
    }
  }

  function nextRow(): number {
    return cards.value.reduce((max, c) => Math.max(max, c.layout.y + c.layout.h), 0)
  }

  function setEditMode(value: boolean): void {
    editMode.value = value
  }

  function selectCard(cardId: string | undefined): void {
    selectedCardId.value = cardId
  }

  /** 切换看板后释放不再被引用的帧缓冲（ST-07）。 */
  function pruneFrameBuffers(): void {
    if (!current.value) return
    useFrameStore().prune(channelIds.value)
  }

  return {
    dashboards,
    current,
    editMode,
    dirty,
    saving,
    loading,
    error,
    revisionConflict,
    selectedCardId,
    cards,
    selectedCard,
    cardIds,
    channelIds,
    loadList,
    select,
    ensureSelected,
    create,
    rename,
    remove,
    addCard,
    patchCard,
    removeCard,
    replaceCard,
    saveLayoutNow,
    save,
    setEditMode,
    selectCard,
    touch,
    nextFreeLayout,
    pruneFrameBuffers,
  }
})
