/**
 * 地图看板 Store（v3）：节点、视角、几何草稿、选中、可见性、诊断计数。
 *
 * ⭐ 边界（C1/C2「内核不可复制」）：
 *   - **不复制**卡片 CRUD / revision / 订阅引用计数 —— 全部复用 `dashboardStore`
 *   - 本 store 只装「地图独有」的东西：nodes、view、几何草稿、选中、可见性
 *
 * 🚨 性能铁律（与 frameStore 同源）：
 *   - `nodeRuntime` / `geomDraft` / `dirtySet` / `pendingGeom` 全部**非响应式**
 *   - 高频坐标不进 ref；只有状态跃迁（stale / 选中 / 列表变化）才走响应式
 *
 * 🚨 防回环（note/03 §3.3）：几何真源是本地草稿，外部只能「装载」不能「驱动」；
 *    `PUT /geometry` 的响应**绝不回写**本地状态（见 enqueueGeometry / flushGeometry）。
 */
import { defineStore } from 'pinia'
import { computed, markRaw, ref, shallowRef } from 'vue'
import type { Card, CRS } from '@/types'
import type {
  CreateMapNodeReq,
  MapGeometryItem,
  MapNode,
  MapNodeView,
  MapViewConfig,
  UpdateMapNodeReq,
} from '@/types/mapboard'
import { DEFAULT_MAP_VIEW, parseMapView } from '@/types/mapboard'
import * as api from '@/api/endpoints'
import { AppError } from '@/api/client'
import { MAP_GEOM_SAVE_DEBOUNCE_MS, MAP_MAX_NODES } from '@/utils/constants'
import { useDashboardStore } from '@/stores/dashboard'
import { useUiStore } from '@/stores/ui'
import type { CrsSource } from '@/composables/useGeoPosition'
import { bump, sample } from '@/utils/diag'
import { toGCJ02 } from '@/utils/geo'
import type { LngLat } from '@/utils/geo'

/** 节点运行时（高频，非响应式）。 */
export interface NodeRuntime {
  lng: number
  lat: number
  yaw?: number
  /** 是否曾收到过合法坐标（A10：一旦为 true 不再回退到兜底）。 */
  everReceived: boolean
  updatedAt: number
}

export const useMapBoardStore = defineStore('mapBoard', () => {
  /* ——— 响应式（低频 / 结构性） ——— */
  const boardId = ref('')
  const nodes = ref<MapNodeView[]>([])
  const loading = ref(false)
  const error = ref('')
  const editMode = ref(false)
  const selectedNodeId = ref<string | undefined>(undefined)
  const selectedCardId = ref<string | undefined>(undefined)
  const view = ref<MapViewConfig>({ ...DEFAULT_MAP_VIEW })
  /** 投影器种类：amap（有 Key）/ virtual（无 Key 降级）。 */
  const projectorKind = ref<'amap' | 'virtual'>('amap')
  /** 底图状态：idle / loading / ready / error / fallback。 */
  const mapStatus = ref<'idle' | 'loading' | 'ready' | 'error' | 'fallback'>('idle')
  /** 1Hz 刷新的节点状态摘要（给 UI，不含高频坐标）。 */
  const nodeSummary = ref<Record<string, { stale: boolean; lastUpdateAt: number }>>({})
  /**
   * 「未定位」节点 id 集合（**未定位 = 从未收到通道坐标 且 没有可用兜底坐标**）。
   *
   * ⭐ 为什么必须是显式状态，而不是像以前那样往 `nodeRuntime` 里塞一个 `(0,0)`：
   *   1. `(0, 0)` 是几内亚湾上一个**合法坐标**，不是哨兵（后端 DDL 注释原文：
   *      "不能用 0 做哨兵"）—— 伪造它会让节点被画到非洲西海岸；
   *   2. "无处可放"与"在 (0,0)"是两件事，前者必须让用户看见（面板徽标 + 影响面），
   *      后者是一个正常的落点。用一个假的坐标表达"没有坐标"，信息就丢了。
   *
   * 它是**低频状态跃迁**（与 stale 同级），所以可以走响应式：
   * 高频坐标仍然只在 `nodeRuntime`（markRaw）里流转，两者分工不混。
   */
  const unplacedNodeIds = ref<Set<string>>(new Set())
  /**
   * 每个节点**实际生效**的坐标系及其来源（`frame` = 帧内盖章 / `declared` = 声明兜底）。
   *
   * 为什么需要它：帧内 crs 优先之后，节点行再显示 `fallbackCrs` 就会**误导用户** ——
   * 显示的值与真正用来解读的值可能不是同一个。必须如实标出来源。
   *
   * 低频 + 状态跃迁性质（只在解析结果与上一帧不同时写一次），所以走响应式 ——
   * 与 `unplacedNodeIds` 同一思路；高频坐标仍只在 `nodeRuntime`(markRaw) 里流转。
   */
  const nodeCrsSource = ref<Record<string, { crs: CRS; source: CrsSource }>>({})
  /** 节点位置版本号：任何节点投影位置变化时递增，驱动一次 rAF 同步。 */
  const nodeVersion = ref(0)
  /**
   * 几何版本号：`commitGeometry` / 装载草稿时递增。
   *
   * 为什么需要它：几何真源 `geomDraft` 是 `markRaw(Map)`（非响应式，这是性能铁律），
   * 因此任何 `computed(() => store.geometryOf(id))` 都**不会**在写入后失效 ——
   * 会一直读到旧矩形。评审复现：resize 松手后卡片"弹回"旧尺寸、下次拖拽先跳回原位。
   * 用一个低频计数器把"非响应式真源"的变更显式暴露给响应式消费者，代价只有拖拽结束那一刻。
   */
  const geomVersion = ref(0)
  /**
   * 当前展开（渲染浮层）的卡片 id 集合。
   *
   * 与 `selectedCardId` 的分工：selectedCardId 只是"焦点卡"（画个高亮边），
   * openCardIds 才决定**渲染哪些浮层**。两者分开是因为"点了某张卡"与"看得到几张卡"
   * 是两件事 —— 混用一个变量会变成"点第二张卡就把第一张关了"。
   */
  const openCardIds = ref<string[]>([])

  /**
   * 表单预览点（GCJ-02）；null = 不显示。
   *
   * 用途：用户在**节点表单**里填经纬度时，把"这个坐标会落在哪"**画在图上**。
   * 这是回答"我这份坐标是什么标准"唯一可靠的办法 ——
   * 坐标系选错时标记会明显偏出（境内 300~700 米），肉眼立刻可见；
   * 而仅靠数字是**分辨不出来**的（偏移量比两个地点的距离还小）。
   *
   * ⚠️ 为什么挂在 store 而不是 `MapRuntime`（踩过的坑，别再挪回去）：
   * 写它的是 `MapNodeInspector`，读它的是 `MapPreviewDot`，两者是**兄弟组件** ——
   * 而 `MapRuntime` 是 `MapBoardSurface` 用 provide 提供的，**只对它的子树生效**。
   * 兄弟组件 `useMapRuntime()` 会 inject 失败并在 `setup()` 里抛异常，
   * 表现为"这个面板静默消失、界面无任何提示"（节点面板就这样隐身穿了一整个版本）。
   * store 是跨组件的单例，天然适合这种"表单驱动、跨组件、低频"的状态。
   */
  const previewPoint = shallowRef<LngLat | null>(null)

  /** 写入/清空表单预览点（`null` = 收起预览圈）。 */
  function setPreviewPoint(v: LngLat | null): void {
    previewPoint.value = v
  }

  /* ——— 非响应式（高频） ——— */
  const nodeRuntime = markRaw(new Map<string, NodeRuntime>())
  /** 几何草稿（容器 px，取整后存储）——唯一真源。 */
  const geomDraft = markRaw(new Map<string, { x: number; y: number; w: number; h: number }>())
  /** 待保存集合。 */
  const dirtySet = new Set<string>()
  /** 节点尾迹（原始经纬度，升序）。 */
  const nodeTrail = markRaw(new Map<string, Array<{ lng: number; lat: number }>>())

  /** 挂载到节点的卡片列表（nodeId → cards）。 */
  const cardsByNode = computed<Map<string, Card[]>>(() => {
    const dashboard = useDashboardStore()
    const map = new Map<string, Card[]>()
    for (const card of dashboard.cards) {
      const nodeId = card.nodeId
      if (!nodeId) continue
      const list = map.get(nodeId)
      if (list) list.push(card)
      else map.set(nodeId, [card])
    }
    return map
  })

  /** 未挂载到任何节点的「孤儿卡」（C15：必须释放订阅）。 */
  const orphanCards = computed<Card[]>(() => {
    const dashboard = useDashboardStore()
    const known = new Set(nodes.value.map((n) => n.id))
    return dashboard.cards.filter((c) => !c.nodeId || !known.has(c.nodeId))
  })

  const nodeCount = computed<number>(() => nodes.value.length)
  const canAddNode = computed<boolean>(() => nodes.value.length < MAP_MAX_NODES)

  function nodeById(id: string | undefined): MapNodeView | undefined {
    if (!id) return undefined
    return nodes.value.find((n) => n.id === id)
  }

  /* ——— 装载 ——— */

  /**
   * 装载整个看板（卡片走 dashboardStore，节点走 /mapboards/:id）。
   * @returns 实际装载的 boardId；若期间切走了会返回不一致，调用方据此丢弃（hydrate 守卫）
   */
  async function hydrate(id: string): Promise<string> {
    loading.value = true
    error.value = ''
    boardId.value = id
    try {
      const detail = await api.getMapBoard(id)
      // hydrate 守卫：响应回来时若已切到别的看板则丢弃（与地图 token 是两个独立守卫）
      if (boardId.value !== id) return boardId.value
      const dashboard = useDashboardStore()
      dashboard.current = detail
      nodes.value = detail.nodes ?? []
      view.value = parseMapView(detail.globalConfig)
      nodeRuntime.clear()
      geomDraft.clear()
      nodeTrail.clear()
      dirtySet.clear()
      unplacedNodeIds.value = new Set()
      nodeCrsSource.value = {}
      previewPoint.value = null
      // 切看板必须清空浮层展开态：否则会带着上一块板的卡片 id，渲染出"幽灵卡"
      openCardIds.value = []
      selectedCardId.value = undefined
      selectedNodeId.value = undefined
      loadGeometryFromCards(dashboard.cards)
      return id
    } catch (err) {
      if (boardId.value !== id) return boardId.value
      error.value = err instanceof AppError ? err.message : '地图看板加载失败'
      throw err
    } finally {
      if (boardId.value === id) loading.value = false
    }
  }

  /** 从卡片的 mapGeom 装载草稿（只在首加载 / 节点增删 / 切换看板时调用）。 */
  function loadGeometryFromCards(cards: readonly Card[]): void {
    for (const card of cards) {
      const g = card.mapGeom
      if (!g || (g.x === 0 && g.y === 0 && g.w === 0 && g.h === 0)) continue
      geomDraft.set(card.id, { x: g.x, y: g.y, w: g.w, h: g.h })
    }
    geomVersion.value += 1
  }

  async function reloadNodes(): Promise<void> {
    const id = boardId.value
    if (!id) return
    const list = await api.listMapNodes(id)
    if (boardId.value !== id) return
    nodes.value = list
  }

  /**
   * 把后端在同一事务里自增后的 revision 同步回本地看板。
   *
   * ⚠️ 节点变更（增/改/删）都会让 `dashboards.revision +1`。不回写的话本地很快变陈旧，
   * 下一次任何带 CAS 的写操作都会 40009。这是「节点建了一个之后再也建不了第二个」的根因。
   */
  function syncRevision(next: number): void {
    const dashboard = useDashboardStore()
    if (dashboard.current && Number.isFinite(next) && next > 0) {
      dashboard.current.revision = next
    }
  }

  /**
   * 建节点。
   *
   * 参数用 `Omit<…, 'revision'>`：乐观锁是**本 store 的内部事务细节**，
   * 调用方（节点面板）不该也不需要知道它 —— 由这里统一从 dashboardStore 取当前值注入，
   * 并在成功后回写后端自增后的新值。这样"忘传 revision"在类型层面就不可能发生。
   */
  async function createNode(body: Omit<CreateMapNodeReq, 'revision'>): Promise<MapNodeView> {
    const id = boardId.value
    if (!id) throw new AppError(-1, '尚未选中地图看板')
    const dashboard = useDashboardStore()
    const resp = await api.createMapNode(id, {
      ...body,
      // 乐观锁：不带上必然 40001（后端要求 expect > 0）
      revision: dashboard.current?.revision ?? 0,
    })
    if (boardId.value !== id) throw new AppError(-1, '看板已切换，放弃本次结果')
    syncRevision(resp.revision)
    nodes.value = [...nodes.value, viewOf(resp.node)]
    bump('nodeCreates')
    return nodes.value[nodes.value.length - 1] as MapNodeView
  }

  async function updateNode(nodeId: string, body: Omit<UpdateMapNodeReq, 'revision'>): Promise<void> {
    const id = boardId.value
    if (!id) return
    const dashboard = useDashboardStore()
    const resp = await api.updateMapNode(id, nodeId, {
      ...body,
      revision: dashboard.current?.revision ?? 0,
    })
    if (boardId.value !== id) return
    syncRevision(resp.revision)
    replaceNode(viewOf(resp.node))
  }

  async function removeNode(nodeId: string, cascade: 'unbind' | 'delete' = 'unbind'): Promise<void> {
    const id = boardId.value
    if (!id) return
    const dashboard = useDashboardStore()
    const revision = dashboard.current?.revision ?? 0
    const resp = await api.deleteMapNode(id, nodeId, revision, cascade)
    if (boardId.value !== id) return
    syncRevision(resp.revision)
    nodes.value = nodes.value.filter((n) => n.id !== nodeId)
    nodeRuntime.delete(nodeId)
    nodeTrail.delete(nodeId)
    setNodeUnplaced(nodeId, false)
    dropNodeCrsSource(nodeId)
    if (selectedNodeId.value === nodeId) selectedNodeId.value = undefined
    if (cascade === 'delete') {
      for (const card of cardsByNode.value.get(nodeId) ?? []) {
        geomDraft.delete(card.id)
      }
      await dashboard.select(id)
    }
  }

  function replaceNode(node: MapNodeView): void {
    const idx = nodes.value.findIndex((n) => n.id === node.id)
    if (idx >= 0) nodes.value[idx] = node
    else nodes.value.push(node)
  }

  /** MapNode → MapNodeView（本地新建时后端可能不返回运行时字段）。 */
  function viewOf(node: MapNode): MapNodeView {
    const anyNode = node as MapNodeView
    return {
      ...node,
      channelMissing: anyNode.channelMissing ?? false,
      channelName: anyNode.channelName ?? '',
      rateLimitHz: anyNode.rateLimitHz ?? 0,
      recommendedRateLimitHz: anyNode.recommendedRateLimitHz ?? 0,
      hasFallback:
        anyNode.hasFallback ??
        (Number.isFinite(node.fallbackLat) && Number.isFinite(node.fallbackLon)),
    }
  }

  /* ——— 节点运行时写入（由 MapNodeItem 调用） ——— */

  /**
   * 标记 / 解除「未定位」。
   *
   * 整体替换 Set（不走原地 mutate）—— 与 `openCardIds` 等既有写法一致，
   * 避免依赖 Vue 对 Set 的深层代理行为，生命周期内只有"状态真的翻转"才写一次。
   */
  function setNodeUnplaced(nodeId: string, unplaced: boolean): void {
    if (unplacedNodeIds.value.has(nodeId) === unplaced) return
    const next = new Set(unplacedNodeIds.value)
    if (unplaced) next.add(nodeId)
    else next.delete(nodeId)
    unplacedNodeIds.value = next
  }

  /** 该节点是否「未定位」→ 地图层不渲染它（面板会说明原因与影响面）。 */
  function isNodeUnplaced(nodeId: string): boolean {
    return unplacedNodeIds.value.has(nodeId)
  }

  /**
   * 记录某节点**实际生效**的坐标系与来源（低频；只在真的变化时写一次）。
   * 由 `useNodePosition.push()` 在拿到帧时调用。
   */
  function setNodeCrsSource(nodeId: string, crs: CRS, source: CrsSource): void {
    const prev = nodeCrsSource.value[nodeId]
    if (prev && prev.crs === crs && prev.source === source) return
    nodeCrsSource.value = { ...nodeCrsSource.value, [nodeId]: { crs, source } }
  }

  /** 丢弃某节点的坐标系来源记录（节点被删除时）。 */
  function dropNodeCrsSource(nodeId: string): void {
    if (!(nodeId in nodeCrsSource.value)) return
    const next = { ...nodeCrsSource.value }
    delete next[nodeId]
    nodeCrsSource.value = next
  }

  /**
   * 写入节点位置。只在位置**真的变化**时才 bump nodeVersion，
   * 避免 20 节点 ×10Hz 的无效 rAF 同步。
   */
  function setNodePosition(nodeId: string, lng: number, lat: number, yaw?: number): void {
    if (!Number.isFinite(lng) || !Number.isFinite(lat)) return
    // 拿到合法实时坐标 → 解除「未定位」（走出隐藏、开始跟随）
    setNodeUnplaced(nodeId, false)
    const prev = nodeRuntime.get(nodeId)
    const moved =
      !prev || Math.abs(prev.lng - lng) > 1e-9 || Math.abs(prev.lat - lat) > 1e-9
    nodeRuntime.set(nodeId, {
      lng,
      lat,
      ...(yaw !== undefined ? { yaw } : {}),
      everReceived: true,
      updatedAt: Date.now(),
    })
    if (moved) nodeVersion.value += 1
  }

  /**
   * 从未收到过合法坐标时：有兜底坐标就用它（+stale），没有就标记「未定位」。
   *
   * ⚠️ 这里以前是 `Number.isFinite(node.fallbackLon) ? … : 0` —— 缺兜底坐标时
   * **静默伪造 `(0, 0)`**，节点被画到几内亚湾（后端明确说过 0 不是哨兵）。
   * 现在拆成两条互斥的路径，任何一种"没有坐标"的情形都不再产生假位置。
   */
  function markNodeUninitialized(nodeId: string): void {
    if (nodeRuntime.has(nodeId)) return
    const node = nodeById(nodeId)
    const lon = node?.fallbackLon
    const lat = node?.fallbackLat
    // 缺兜底坐标（null/undefined）、或后端判定为"未定位"的 (0,0) → 都算没有可用坐标。
    if (
      lon == null ||
      lat == null ||
      !Number.isFinite(lon) ||
      !Number.isFinite(lat) ||
      (lon === 0 && lat === 0)
    ) {
      setNodeUnplaced(nodeId, true)
      return
    }
    setNodeUnplaced(nodeId, false)
    // 注意顺序：lng ← fallbackLon，lat ← fallbackLat（别写反，写反会静默偏移到地球另一端）。
    // 兜底坐标也是 WGS-84（默认）→ 必须转成 GCJ-02 再落运行时，否则与实时点/高德底图错开数百米。
    const [lng, latG] = toGCJ02(lon, lat, (node?.fallbackCrs as CRS) || 'WGS-84')
    nodeRuntime.set(nodeId, {
      lng,
      lat: latG,
      everReceived: false,
      updatedAt: 0,
    })
  }

  function setNodeStaleSummary(nodeId: string, stale: boolean, lastUpdateAt: number): void {
    const prev = nodeSummary.value[nodeId]
    if (prev && prev.stale === stale && prev.lastUpdateAt === lastUpdateAt) return
    nodeSummary.value = { ...nodeSummary.value, [nodeId]: { stale, lastUpdateAt } }
  }

  function setNodeTrail(nodeId: string, points: Array<{ lng: number; lat: number }>): void {
    nodeTrail.set(nodeId, points)
  }

  function positionOf(nodeId: string): NodeRuntime | undefined {
    return nodeRuntime.get(nodeId)
  }

  /* ——— 几何：本地草稿 → debounce → 串行合并队列 ——— */

  let saveTimer = 0
  let inFlight = false
  const pendingGeom = new Map<string, MapGeometryItem>()

  function isGeomEqual(
    a: { x: number; y: number; w: number; h: number } | undefined,
    x: number,
    y: number,
    w: number,
    h: number,
  ): boolean {
    return !!a && a.x === x && a.y === y && a.w === w && a.h === h
  }

  /**
   * 提交一次几何变更（拖拽/resize 结束时调用）。
   * **值相同直接 return**：不入队、不发请求 —— 这是防回环的第一道闸（R2/R3）。
   */
  function commitGeometry(id: string, x: number, y: number, w: number, h: number): void {
    const nx = Math.round(x)
    const ny = Math.round(y)
    const nw = Math.round(w)
    const nh = Math.round(h)
    if (isGeomEqual(geomDraft.get(id), nx, ny, nw, nh)) return
    geomDraft.set(id, { x: nx, y: ny, w: nw, h: nh })
    geomVersion.value += 1
    dirtySet.add(id)
    scheduleGeometrySave()
  }

  function scheduleGeometrySave(): void {
    window.clearTimeout(saveTimer)
    saveTimer = window.setTimeout(() => {
      void flushGeometry()
    }, MAP_GEOM_SAVE_DEBOUNCE_MS)
  }

  /**
   * 串行合并队列：in-flight 期间的新草稿只进 pending，响应后合并再发；**永不并发**。
   *
   * 🚨 响应**绝不回写** geomDraft（R1）—— 这是断掉
   * 「回写 → 新引用 → 重渲染 → 再保存」自激振荡的核心。
   */
  async function flushGeometry(): Promise<void> {
    if (inFlight) return
    if (pendingGeom.size === 0 && dirtySet.size === 0) return
    for (const id of dirtySet) {
      const g = geomDraft.get(id)
      if (g) pendingGeom.set(id, { id, x: g.x, y: g.y, w: g.w, h: g.h })
    }
    dirtySet.clear()
    if (pendingGeom.size === 0) return

    const id = boardId.value
    if (!id) return
    inFlight = true
    const items = Array.from(pendingGeom.values())
    pendingGeom.clear()
    const started = performance.now()
    try {
      await api.saveMapGeometry(id, items)
      bump('geomSaves')
      sample('geomSaveMs', performance.now() - started)
    } catch (err) {
      // 网络错重试 1 次；40009 不重试（几何端点脱离 revision，理论上不会命中）
      const code = err instanceof AppError ? err.code : -1
      if (code !== 40009) {
        try {
          await api.saveMapGeometry(id, items)
          bump('geomSaves')
        } catch (retryErr) {
          bump('geomErrors')
          useUiStore().handleError(retryErr, '卡片位置保存失败，将在下次操作后重试')
        }
      } else {
        bump('geomErrors')
      }
    } finally {
      inFlight = false
      if (pendingGeom.size > 0 || dirtySet.size > 0) void flushGeometry()
    }
  }

  function geometryOf(cardId: string): { x: number; y: number; w: number; h: number } | undefined {
    return geomDraft.get(cardId)
  }

  /* ——— 视角（存 globalConfig，C6：低频，走 revision CAS）——— */

  async function saveView(): Promise<void> {
    const id = boardId.value
    if (!id) return
    const dashboard = useDashboardStore()
    const revision = dashboard.current?.revision ?? 0
    const globalConfig: Record<string, unknown> = {
      ...(dashboard.current?.globalConfig ?? {}),
      center: view.value.center,
      zoom: view.value.zoom,
      mapStyle: view.value.mapStyle,
      sourceCRS: view.value.sourceCRS,
    }
    try {
      const updated = await api.updateMapBoard(id, { revision, globalConfig })
      if (boardId.value !== id) return
      dashboard.current = updated
      bump('viewSaves')
    } catch (err) {
      useUiStore().handleError(err, '视角保存失败')
    }
  }

  function setView(patch: Partial<MapViewConfig>): void {
    view.value = { ...view.value, ...patch }
  }

  /* ——— 选中 / 编辑模式 ——— */

  function selectNode(id: string | undefined): void {
    selectedNodeId.value = id
  }
  function selectCard(id: string | undefined): void {
    selectedCardId.value = id
  }

  /* ——— 浮层展开 / 收起（多卡同开） ——— */

  function closeCard(id: string): void {
    openCardIds.value = openCardIds.value.filter((x) => x !== id)
    if (selectedCardId.value === id) selectedCardId.value = undefined
  }

  function closeAllCards(): void {
    openCardIds.value = []
    selectedCardId.value = undefined
  }

  /**
   * 点节点：该节点的卡片「全开」或「全关」（切换语义）。
   *
   * 判定必须用 `every`（**全部已开**才全关），不能用 `some`。
   * 用 `some` 时：节点有 c1/c2，关掉 c1 后 anyOpen(c2)=true → 走"全关"分支，
   * 用户再点节点本想把剩下的打开，结果 c2 也被关掉 —— 表现为"点了反而全没了"。
   */
  function toggleNodeCards(nodeId: string): void {
    const cards = cardsByNode.value.get(nodeId) ?? []
    if (cards.length === 0) return
    const ids = cards.map((c) => c.id)
    const allOpen = ids.every((id) => openCardIds.value.includes(id))
    if (allOpen) {
      const drop = new Set(ids)
      openCardIds.value = openCardIds.value.filter((x) => !drop.has(x))
      if (selectedCardId.value && drop.has(selectedCardId.value)) selectedCardId.value = undefined
      return
    }
    const set = new Set(openCardIds.value)
    for (const id of ids) set.add(id)
    openCardIds.value = [...set]
    selectedCardId.value = ids[0]
  }

  function setEditMode(value: boolean): void {
    editMode.value = value
  }

  /** 路由离开 / 切看板时复位（配合 useMapInstance 的显式 reset）。 */
  function reset(): void {
    window.clearTimeout(saveTimer)
    saveTimer = 0
    inFlight = false
    pendingGeom.clear()
    dirtySet.clear()
    geomDraft.clear()
    nodeRuntime.clear()
    nodeTrail.clear()
    nodeSummary.value = {}
    unplacedNodeIds.value = new Set()
    nodeCrsSource.value = {}
    previewPoint.value = null
    nodes.value = []
    selectedNodeId.value = undefined
    selectedCardId.value = undefined
    openCardIds.value = []
    error.value = ''
    boardId.value = ''
  }

  return {
    boardId,
    nodes,
    loading,
    error,
    editMode,
    selectedNodeId,
    selectedCardId,
    view,
    projectorKind,
    mapStatus,
    nodeSummary,
    nodeVersion,
    geomVersion,
    cardsByNode,
    orphanCards,
    nodeCount,
    canAddNode,
    nodeById,
    hydrate,
    reloadNodes,
    createNode,
    updateNode,
    removeNode,
    setNodePosition,
    markNodeUninitialized,
    isNodeUnplaced,
    setNodeCrsSource,
    nodeCrsSource,
    previewPoint,
    setPreviewPoint,
    setNodeStaleSummary,
    setNodeTrail,
    positionOf,
    commitGeometry,
    flushGeometry,
    geometryOf,
    saveView,
    setView,
    selectNode,
    selectCard,
    openCardIds,
    toggleNodeCards,
    closeCard,
    closeAllCards,
    setEditMode,
    reset,
  }
})
