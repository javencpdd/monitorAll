<script setup lang="ts">
/**
 * 节点面板（v3 M4）：列表 + 新建 + 删除。
 *
 * 删除默认「解绑保留」卡片（C10），可切「一并删除」—— 删卡不可恢复，
 * 不允许无声处理（PRD F-33）。
 */
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useMapBoardStore } from '@/stores/mapBoard'
import { toGCJ02 } from '@/utils/geo'
import type { CRS } from '@/types'
import type { MapNodeView } from '@/types/mapboard'
import { useDatasourceStore } from '@/stores/datasource'
import { useUiStore } from '@/stores/ui'
import { useDialog } from 'naive-ui'
import { MAP_MAX_NODES } from '@/utils/constants'

const store = useMapBoardStore()
const datasource = useDatasourceStore()
/*
 * ⛔ 这里**不能** inject 画布运行时（`@/composables/useMapProjection` 的 MapRuntime）：
 * 本组件是 `MapBoardSurface` 的**兄弟**节点（同在 `MapBoardView` 的 `.ma-view__stage` 里），
 * 而 runtime 由 surface 用 provide 提供、**只对它的子树生效**。inject 失败会在 `setup()`
 * 抛异常并被 Vue 吞掉 —— 症状就是"节点面板静默消失、界面无任何提示"
 * （本项目里真实发生过一整版，详见 `useMapProjection.ts` 的 MapRuntime 文档）。
 * 跨组件的预览点状态因此改放 `mapBoardStore.previewPoint`（store 是单例，天然跨组件）。
 */
const ui = useUiStore()
const dialog = useDialog()

/* ——— 整栏折叠（想给地图腾地方时用；与左侧数据源面板同一套写法与存储键）——— */
const PANEL_FOLD_KEY = 'monitorall.nodePanelCollapsed'
const panelCollapsed = ref<boolean>(
  (() => {
    try {
      return localStorage.getItem(PANEL_FOLD_KEY) === '1'
    } catch {
      return false
    }
  })(),
)

function togglePanel(): void {
  panelCollapsed.value = !panelCollapsed.value
}

watch(panelCollapsed, (v) => {
  try {
    localStorage.setItem(PANEL_FOLD_KEY, v ? '1' : '0')
  } catch {
    /* localStorage 不可用（隐私模式等）时仅退化为不记忆 */
  }
})

const name = ref('')
const channelId = ref<string>('')
/**
 * 兜底经纬度。
 *
 * ⚠️ 类型写的是 `number | null`，但**运行时可能不是**：`<input type="number" v-model.number>`
 * 在输入框被清空时把 `''` 写回模型（Vue 的 `looseToNumber('')` 原样返回空串），
 * 类型系统在这里是被 `.number` 修饰符的签名骗过去的。
 * 所以**任何读取都必须先过 `normNum`**（下面），否则空串会一路走到请求体里
 * → 后端 JSON 反序列化失败 → 400「请求体解析失败」。
 */
const lat = ref<number | null>(null)
const lon = ref<number | null>(null)

/**
 * 数值归一化：把输入框可能产生的所有"非数"统一压成 `null`。
 *
 * 覆盖：`''`（清空）、`null`、`undefined`、`NaN`、以及任何解析不出有限数的输入。
 * 语义上 `null` = "用户没填"，与"填了 0"严格区分（0 是合法坐标）。
 */
function normNum(v: unknown): number | null {
  if (v === null || v === undefined || v === '') return null
  const n = typeof v === 'number' ? v : Number(v)
  return Number.isFinite(n) ? n : null
}

/**
 * 兜底坐标格式化（列表展示用）。
 *
 * `MapNodeView.fallbackLat/Lon` 是 `number | null` —— 纯静态节点允许没有坐标。
 * 模板里裸写 `.toFixed(5)` 在 null 上会**抛异常**，直接打断节点列表渲染。
 * 缺失显示 `—`，与"坐标为 0"明确区分开（0 是真值，`—` 是"没填"）。
 */
function fmtCoord(v: number | null | undefined, digits = 5): string {
  return v == null || !Number.isFinite(v) ? '—' : v.toFixed(digits)
}
const latPath = ref('')
const lonPath = ref('')

/**
 * 本节点**声明**的坐标系（提交到 `fallbackCrs`）。
 *
 * 默认跟随**看板级声明**（`view.sourceCRS`，由右下角徽标设置）—— 这样"统一坐标标准"
 * 只需要在一个地方设一次，新节点自动沿用；个别来源不同的节点仍可单独覆盖。
 *
 * ⚠️ 它同时管两件事，但**优先级不同**：(1) 兜底坐标的换算标准（永远用它）；
 * (2) 通道帧在**帧内不带合法 crs 时**的兜底声明。
 * 帧内带了 crs 的话，实时数据一律按帧解读 —— 这是裁决 a 的结论，见 resolveFrameCrs。
 */
const crs = ref<CRS>(store.view.sourceCRS)

/** 坐标系选项：三选一，够覆盖国内全部常见来源。 */
const CRS_OPTIONS: ReadonlyArray<{ value: CRS; label: string }> = [
  { value: 'WGS-84', label: 'WGS-84（GPS/飞控原始）' },
  { value: 'GCJ-02', label: 'GCJ-02（高德/腾讯）' },
  { value: 'BD-09', label: 'BD-09（百度）' },
]

/** 正在编辑的节点 id；undefined = 新建模式。 */
const editingId = ref<string | undefined>(undefined)
const editing = computed(() => (editingId.value ? store.nodeById(editingId.value) : undefined))

/**
 * 当前表单是否"绑了位置通道"（新建看选择框，编辑看被编辑节点）。
 *
 * ⚠️ 它**只**用来决定文案措辞 / 是否显示落点预览，**不再**表示"这个值就是解析标准" ——
 * 帧内 crs 优先之后，声明值只在帧内没带 crs 时才参与解读（裁决 a，2026-02）。
 */
const hasPositionChannel = computed<boolean>(() => {
  if (editingId.value) {
    const n = editing.value
    return !!n?.channelId && !n.channelMissing
  }
  return channelId.value !== ''
})

/**
 * 编辑"位置来自通道、又没填兜底坐标"的节点时，落点预览不可用。
 *
 * 这种情况必须明说：节点位置由通道数据驱动，这里的表单改不到它 ——
 * 设备离线时看不到任何变化，用户很容易误判成"改了没反应"。
 */
const previewUnavailable = computed<boolean>(
  () => !!editingId.value && hasPositionChannel.value && !hasCoords.value,
)

/** 表单复位（新建成功后 / 取消编辑时调用）。 */
function resetForm(): void {
  name.value = ''
  channelId.value = ''
  lat.value = null
  lon.value = null
  latPath.value = ''
  lonPath.value = ''
  crs.value = store.view.sourceCRS
  store.setPreviewPoint(null)
}

/**
 * 进入编辑模式，把该节点当前值装进表单。
 *
 * ⚠️ **只填表单，不发请求**：保存必须由用户显式点「保存修改」。
 * "点一行就把线上配置改了"是不可接受的意外行为。
 */
function startEdit(id: string): void {
  const n = store.nodeById(id)
  if (!n) return
  editingId.value = id
  name.value = n.name
  channelId.value = n.channelId || ''
  lat.value = normNum(n.fallbackLat)
  lon.value = normNum(n.fallbackLon)
  crs.value = (n.fallbackCrs as CRS) || store.view.sourceCRS
  latPath.value = n.latPath || ''
  lonPath.value = n.lonPath || ''
}

/**
 * 点节点行 = 选中 + **进入编辑态**（缺陷 1 的修复）。
 *
 * 用户报的症状：点了节点行以为在编辑，但表单仍是「添加节点」状态 ——
 * 于是"改一下"的动作直接变成"又建了一个节点"。
 * 现在点行就进编辑态（`startEdit` 只填表单、不落库），并且编辑态在视觉上无法忽略
 * （标题栏 + 行高亮 + 提交按钮文案变化），从源头消除"我以为在改、其实在建"。
 */
function onRowClick(id: string): void {
  store.selectNode(id)
  startEdit(id)
}

function cancelEdit(): void {
  editingId.value = undefined
  resetForm()
}

/**
 * 该节点名下的卡片数。
 *
 * B3 用：节点"未定位"时地图不渲染它，草稿锚在它上面的卡片也无处可放 ——
 * 必须把"因此被隐藏了几张卡"明确告诉用户，否则卡片就是**静默消失**。
 */
function cardCountOf(nodeId: string): number {
  return (store.cardsByNode.get(nodeId) ?? []).length
}

/**
 * 节点**实际生效**的坐标系与来源（裁决 a 之后必须显示这个，而不是 `fallbackCrs`）。
 *
 * - 帧内带了合法 crs → `frame`，界面标「帧」；
 * - 否则回落到节点声明值 → `declared`，界面标「声明」；
 * - 没有任何帧记录（纯静态节点 / 还没收到帧）→ 生效值就是节点自身的声明值，标「声明」。
 */
function crsInfo(n: MapNodeView): { crs: CRS; source: 'frame' | 'declared' } {
  return store.nodeCrsSource[n.id] ?? { crs: n.fallbackCrs, source: 'declared' }
}

/**
 * 归一化后的当前坐标（**唯一读数入口**）。
 *
 * 所有 lat/lon 的消费点一律读这两个 computed，绝不直接读 `lat.value` ——
 * 否则 `''` 会绕过类型系统混进请求体（见上面 ref 声明的说明）。
 */
const latNum = computed<number | null>(() => normNum(lat.value))
const lonNum = computed<number | null>(() => normNum(lon.value))

/** 用户确实是"手上有一份坐标"时才写入预览，空值不要在地图上乱画。 */
const hasCoords = computed<boolean>(() => {
  const a = latNum.value
  const o = lonNum.value
  return a !== null && o !== null && !(a === 0 && o === 0)
})
const submitting = ref(false)

/** 可作位置源的通道（白名单由后端裁定，前端只做友好过滤）。 */
const positionChannels = computed(() =>
  datasource.allChannels.filter((c) => {
    const pt = c.payloadType
    return pt === 'geo_pose' || pt === 'json' || pt === 'table'
  }),
)

/** 选中的通道；json/table 类型必须额外给经纬度字段路径。 */
const selectedChannel = computed(() => positionChannels.value.find((c) => c.id === channelId.value))
const needsPath = computed<boolean>(
  () => selectedChannel.value?.payloadType === 'json' || selectedChannel.value?.payloadType === 'table',
)

/**
 * 经纬度本地校验。
 *
 * 为什么必须有：后端只回一句「兜底纬度超出 [-90, 90]」的 40001，
 * 而**"左右两栏填反"是这里最高频的错误** —— 用户从设备文档/地图复制坐标时
 * 拿到的顺序常常是「经度在前」（113.87, 22.90），填进「纬度」栏就会 >90。
 * 在本地把它变成一句看得懂的提示，比让用户对着 40001 猜要有效得多。
 */
const coordError = computed<string>(() => {
  const laNum = latNum.value
  const loNum = lonNum.value
  if (laNum === null && loNum === null) return ''
  if (laNum !== null && Math.abs(laNum) > 90) {
    // 中国境内纬度 18~54、经度 73~135，所以"纬度>90 而经度<=90"只可能是填反了
    if (loNum !== null && Math.abs(loNum) <= 90) {
      return '看起来左右填反了：左边是纬度、右边是经度（中国境内纬度约 18~54）'
    }
    return '纬度应在 -90~90 之间'
  }
  if (loNum !== null && Math.abs(loNum) > 180) return '经度应在 -180~180 之间'
  if (laNum === 0 && loNum === 0) return '经纬度不能都是 0（GPS 未定位）'
  return ''
})

/** 编辑"绑定通道"的节点时，把原有兜底坐标**清空**了 —— 用来显示"清空 ≠ 删除"的说明。 */
const fallbackCleared = computed<boolean>(() => {
  const n = editing.value
  if (!editingId.value || !n) return false
  const had = n.fallbackLat != null && n.fallbackLon != null
  return had && latNum.value === null && lonNum.value === null
})

/**
 * 按钮不可用时的**原因**（空串 = 可以提交）。
 *
 * ⭐ 为什么要有它：按钮 `disabled` 时点击**不触发任何事件、也不报错**，
 * 用户看到的就是"点了没反应"—— 这是本轮用户反馈的核心症状之一。
 * 把"为什么不能提交"写成一句话显示在按钮上方，静默失败就变成了看得见的拒绝。
 */
const submitBlockReason = computed<string>(() => {
  if (submitting.value) return ''
  if (name.value.trim().length === 0) return '请先填写节点名'
  if (coordError.value !== '') return coordError.value
  // 只填一半的坐标一定是无效的：要么两个都填、要么两个都空（避免发一个"半个坐标"上去）
  if ((latNum.value === null) !== (lonNum.value === null)) {
    return '兜底经纬度要么都填、要么都留空（只填一个是无效坐标）'
  }
  /*
   * 「纯静态节点必须有经纬度」—— **新建与编辑都要拦**。
   *
   * 缺陷 2 的现场：编辑一个静态节点时把坐标清空再保存。旧代码这里带着
   * `!editingId.value &&` 前缀，编辑态直接跳过校验；同时清空后的值是 `''`（不是 null），
   * 于是 `fallbackLat: ''` 被送进请求体 → 后端 JSON 解析失败 400。
   * 现在：编辑态同样校验（静态节点清空坐标 = 节点无处可放），且读数已归一化。
   */
  if (channelId.value === '') {
    if (latNum.value === null || lonNum.value === null) {
      return '纯静态节点必须有经纬度；或把「位置通道」改绑到一个位置通道'
    }
  }
  if (needsPath.value && (latPath.value.trim() === '' || lonPath.value.trim() === '')) {
    return '该通道是 JSON / 表格类型，必须填写经纬度字段路径'
  }
  return ''
})

/** 可用性唯一的判据就是"没有阻塞原因"，避免两处逻辑各写一份而漂移。 */
const canSubmit = computed<boolean>(() => submitBlockReason.value === '')

/** 落点预览文本（GCJ-02 实际值），供用户肉眼核对标准。 */
const previewText = computed<string>(() => {
  const a = latNum.value
  const o = lonNum.value
  if (a === null || o === null || (a === 0 && o === 0)) return ''
  const [gl, ga] = toGCJ02(o, a, crs.value)
  return `${gl.toFixed(6)}, ${ga.toFixed(6)}`
})

/**
 * 把预览点写进 store → 地图上出现一个虚线圈（由 `MapPreviewDot` 画）。
 *
 * ⭐ 这是"我的坐标到底是什么标准"唯一可靠的判据：
 *   境内 WGS-84 与 GCJ-02 只差 300~700 米，**光看数字分辨不出来**；
 *   但画到高德底图上，选错标准会明显偏出，肉眼立刻可见 —— 把"猜"变成"看"。
 */
watch([lat, lon, crs], () => {
  const a = latNum.value
  const o = lonNum.value
  if (a === null || o === null || (a === 0 && o === 0)) {
    store.setPreviewPoint(null)
    return
  }
  store.setPreviewPoint(toGCJ02(o, a, crs.value))
})

onBeforeUnmount(() => {
  // 离开面板必须清掉预览，否则地图上会留一个"幽灵圈"
  store.setPreviewPoint(null)
})

async function submit(): Promise<void> {
  if (!canSubmit.value || submitting.value) return
  submitting.value = true
  /*
   * 归一化后组装坐标字段 —— 这是**唯一**把坐标写进请求体的地方。
   * 只填一半 / 全空 → 整个字段不出现（后端补丁语义：省略 = 不改动）。
   * 静态节点全空的情况已被 submitBlockReason 拦下，走不到这里。
   */
  const la = latNum.value
  const lo = lonNum.value
  const coordsBody: { fallbackLat?: number; fallbackLon?: number } = {}
  if (la !== null && lo !== null) {
    coordsBody.fallbackLat = la
    coordsBody.fallbackLon = lo
  }
  const editId = editingId.value
  try {
    if (editId) {
      // 编辑：只送可改字段（channelId 不在这里改 —— 换绑是另一件事，涉及订阅重建）
      await store.updateNode(editId, {
        name: name.value.trim(),
        fallbackCrs: crs.value,
        ...coordsBody,
        ...(needsPath.value ? { latPath: latPath.value.trim(), lonPath: lonPath.value.trim() } : {}),
      })
      ui.success('节点已更新')
      cancelEdit()
    } else {
      await store.createNode({
        name: name.value.trim(),
        ...(channelId.value ? { channelId: channelId.value } : {}),
        ...coordsBody,
        // 声明坐标系 —— 后端按它换算成 GCJ-02 投影到高德底图上
        fallbackCrs: crs.value,
        ...(needsPath.value ? { latPath: latPath.value.trim(), lonPath: lonPath.value.trim() } : {}),
      })
      ui.success('节点已添加')
      resetForm()
    }
  } catch (err) {
    // 40001 会带 detail（哪个字段不合法），这里原样交给全局错误出口显示
    ui.handleError(err, editId ? '更新节点失败' : '添加节点失败')
  } finally {
    submitting.value = false
  }
}

/**
 * 删除节点（带结果反馈与错误处理）。
 *
 * ⚠️ 之前这里写成 `void store.removeNode(...)`：不 await、不 try/catch、不提示。
 * 失败时是一个**未捕获的 Promise 拒绝** —— 界面上什么都不发生，用户只能看到
 * "点了没反应"，而且无从判断是没删掉还是删掉了但列表没刷新。
 * 现在成功给 toast、失败走统一错误出口。
 */
async function doRemove(id: string, nodeName: string, cascade: 'unbind' | 'delete'): Promise<void> {
  try {
    await store.removeNode(id, cascade)
    ui.success(cascade === 'delete' ? `已删除节点「${nodeName}」及其全部卡片` : `已删除节点「${nodeName}」`)
    // 删掉的正是正在编辑的那个 → 退出编辑态，否则表单会指向一个已不存在的节点
    if (editingId.value === id) cancelEdit()
  } catch (err) {
    ui.handleError(err, '删除节点失败')
  }
}

function askDelete(id: string, nodeName: string, cardCount: number): void {
  if (cardCount === 0) {
    void doRemove(id, nodeName, 'unbind')
    return
  }
  dialog.warning({
    title: '删除节点',
    content: `节点「${nodeName}」下有 ${cardCount} 张卡片。默认解绑保留（卡片转为「未锚定」，可重新绑定到其它节点）；也可一并删除。`,
    positiveText: '解绑保留',
    negativeText: '一并删除',
    // 返回 Promise → naive-ui 会等它完成再关弹窗，删除失败时弹窗不会"假装成功"
    onPositiveClick: () => doRemove(id, nodeName, 'unbind'),
    onNegativeClick: () => doRemove(id, nodeName, 'delete'),
  })
}
</script>

<template>
  <aside v-if="panelCollapsed" class="mb-inspector mb-inspector--collapsed">
    <button class="mb-inspector__fold" type="button" title="展开节点面板" @click="togglePanel">»</button>
  </aside>
  <aside v-else class="mb-inspector">
    <header class="mb-inspector__head">
      <span>节点</span>
      <span class="ma-inspector__head-right">
        <span class="ma-text-3">{{ store.nodeCount }}/{{ MAP_MAX_NODES }}</span>
        <button class="mb-inspector__fold" type="button" title="收起节点面板" @click="togglePanel">«</button>
      </span>
    </header>

    <ul class="mb-inspector__list">
      <li
        v-for="n in store.nodes"
        :key="n.id"
        class="mb-inspector__item"
        :class="{
          'is-active': store.selectedNodeId === n.id,
          'is-editing': editingId === n.id,
          'is-stale': store.nodeSummary[n.id]?.stale,
        }"
        @click="onRowClick(n.id)"
      >
        <div class="mb-inspector__row">
          <span class="mb-inspector__name">{{ n.name }}</span>
          <span class="mb-inspector__acts">
            <button
              class="mb-inspector__act"
              type="button"
              title="编辑该节点的名称与兜底坐标标准"
              @click.stop="startEdit(n.id)"
            >
              编辑
            </button>
            <button class="mb-inspector__del" type="button" @click.stop="askDelete(n.id, n.name, cardCountOf(n.id))">
              删除
            </button>
          </span>
        </div>
        <div class="ma-text-xs ma-text-3">
          <template v-if="n.channelMissing">
            通道已丢失，请重新绑定
          </template>
          <template v-else-if="n.channelId">
            {{ n.channelName || n.channelId }} ·
            <b class="mb-inspector__crs-tag">{{ crsInfo(n).crs }}</b>
            <!-- 来源必须标出来：帧内盖章的值与声明值可能不同，只显示数字会误导 -->
            <span
              class="mb-inspector__crs-src"
              :title="
                crsInfo(n).source === 'frame'
                  ? '帧内自带坐标系（后端盖章），实时数据按它解读'
                  : '帧内未带坐标系，按节点声明值解读'
              "
            >·{{ crsInfo(n).source === 'frame' ? '帧' : '声明' }}</span>
            <span v-if="n.rateLimitHz > 0"> · {{ n.rateLimitHz }}Hz</span>
          </template>
          <template v-else>
            静态 ·
            <b class="mb-inspector__crs-tag">{{ n.fallbackCrs }}</b>
            · {{ fmtCoord(n.fallbackLat) }}, {{ fmtCoord(n.fallbackLon) }}
          </template>
        </div>
        <!--
          「未定位」（B3）：节点没有可用坐标 → 地图上完全不画它。
          必须在这里把原因和影响面说清楚：节点"不见了"和卡片"少了"都不能是静默的。
        -->
        <p v-if="store.isNodeUnplaced(n.id)" class="mb-inspector__unplaced">
          <b>未定位</b> ·
          {{ n.channelId ? '位置通道尚未送来有效坐标，且未配兜底坐标' : '未填兜底坐标' }}，地图上不显示该节点<span
            v-if="cardCountOf(n.id) > 0"
          >，其名下 {{ cardCountOf(n.id) }} 张卡片也一并隐藏</span>。点「编辑」补一组兜底坐标，或等位置通道上线。
        </p>
      </li>
      <li v-if="store.nodes.length === 0" class="ma-text-xs ma-text-3">还没有节点</li>
    </ul>

    <div v-if="store.orphanCards.length > 0" class="mb-inspector__orphan">
      <span class="ma-text-xs">未锚定卡片（{{ store.orphanCards.length }}）</span>
      <span class="ma-text-xs ma-text-3">不渲染、不占用通道订阅</span>
    </div>

    <footer class="mb-inspector__form">
      <div v-if="editingId" class="mb-inspector__editing">
        <span>正在编辑：{{ editing?.name ?? '' }}（保存将覆盖该节点）</span>
        <button class="mb-inspector__act" type="button" @click="cancelEdit">取消</button>
      </div>
      <input v-model="name" class="mb-inspector__input" placeholder="节点名，如 1 号车" />
      <!-- 编辑态不允许换绑通道：换绑要重建订阅与卡片归属，属于另一个操作 -->
      <select v-model="channelId" class="mb-inspector__input" :disabled="!!editingId">
        <option value="">纯静态节点（不绑通道）</option>
        <option v-for="c in positionChannels" :key="c.id" :value="c.id">
          {{ c.name }}（{{ c.payloadType }}）
        </option>
      </select>
      <div class="mb-inspector__crswrap">
        <label class="ma-text-xs ma-text-3">
          {{ hasPositionChannel ? '兜底坐标标准（通道帧自带坐标系时以帧为准）' : '兜底坐标标准' }}
        </label>
        <select v-model="crs" class="mb-inspector__input">
          <option v-for="c in CRS_OPTIONS" :key="c.value" :value="c.value">{{ c.label }}</option>
        </select>
      </div>

      <div class="mb-inspector__coords">
        <input v-model.number="lat" class="mb-inspector__input" type="number" step="0.000001" placeholder="兜底纬度（左）" />
        <input v-model.number="lon" class="mb-inspector__input" type="number" step="0.000001" placeholder="兜底经度（右）" />
      </div>

      <p v-if="previewText" class="mb-inspector__preview">
        图上落点：<code>{{ previewText }}</code>
      </p>
      <p v-else-if="previewUnavailable" class="ma-text-xs ma-text-3">
        该节点位置来自通道实时数据，这里的表单改不到它；而且**通道帧自带坐标系时以帧为准**，
        这个标准只对"帧内没带坐标系"的数据生效。设备离线时看不出变化。
        想先预览落点，可临时填一组兜底坐标。
      </p>
      <!--
        清空语义（缺陷 2 的配套说明）：
        绑了通道的节点，清空经纬度**不等于删除** —— 后端补丁语义把"字段缺失"当"不改动"，
        且当前契约没有显式的"清除位"。既然做不到，就必须说清楚，而不是让用户以为删掉了。
      -->
      <p v-if="fallbackCleared" class="mb-inspector__warn">
        已清空经纬度：保存后**不会删除**原有兜底坐标（后端补丁语义：省略即不改动），本次只是不修改它。
        要换掉一组旧坐标，请直接填入新值。
      </p>

      <details class="mb-inspector__help">
        <summary>我这份坐标是什么标准？</summary>
        <ul>
          <li>高德 / 腾讯地图复制 → <b>GCJ-02</b></li>
          <li>百度地图复制 → <b>BD-09</b></li>
          <li>手机 GPS / 飞控 / ROS 话题 / 设备手册 → <b>WGS-84</b></li>
          <li>中国境外：三者<b>相同</b>（偏移只在中国境内生效）</li>
        </ul>
        <p>
          ⚠️ 境内<b>光看数字分辨不出来</b> —— 偏移只有 300~700 米，比两个地点的距离还小。
          选错就偏半条街。**看地图上的虚线圈**：位置不对，换一个标准即可。
        </p>
      </details>
      <template v-if="needsPath">
        <div class="mb-inspector__coords">
          <input v-model="latPath" class="mb-inspector__input" placeholder="纬度字段路径，如 latitude" />
          <input v-model="lonPath" class="mb-inspector__input" placeholder="经度字段路径，如 longitude" />
        </div>
        <p class="ma-text-xs ma-text-3">
          JSON 里的字段路径。设备返回 <code>{"latitude":30.1,"longitude":120.2}</code> 就填
          <code>latitude</code> / <code>longitude</code>。
        </p>
      </template>
      <p v-if="coordError" class="mb-inspector__err">{{ coordError }}</p>
      <p v-if="!channelId" class="ma-text-xs ma-text-3">纯静态节点必须填兜底坐标，否则它在地图上无处可放。</p>
      <p v-if="submitBlockReason" class="mb-inspector__err">{{ submitBlockReason }}</p>
      <button class="mb-inspector__submit" type="button" :disabled="!canSubmit || submitting" @click="submit">
        {{ submitting ? '保存中…' : editingId ? '保存修改' : '添加节点' }}
      </button>
    </footer>
  </aside>
</template>

<style scoped>
.mb-inspector {
  display: flex;
  flex-direction: column;
  width: 260px;
  height: 100%;
  border-left: 1px solid var(--ma-border);
  background: var(--ma-bg-panel, var(--ma-bg-base));
  flex: none;
  /* 相对定位 + 抬高层级，确保永远浮在地图之上（不被底图盖住） */
  position: relative;
  /* 与卡片抽屉同级(300)：都必须压过地图徽标(200)，否则被挡 */
  z-index: 300;
}

/* 整栏折叠：只留一条细竖条（与左侧数据源面板的 .ma-panel--collapsed 同规格） */
.mb-inspector--collapsed {
  width: 30px;
  align-items: center;
  padding-top: 8px;
}

.mb-inspector__fold {
  border: none;
  background: transparent;
  color: var(--ma-text-3);
  cursor: pointer;
  font-size: var(--ma-font-xs);
  line-height: 1;
  padding: 2px 4px;
  flex: none;
}

.mb-inspector__fold:hover {
  color: var(--ma-accent);
}

.ma-inspector__head-right {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.mb-inspector__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 10px;
  border-bottom: 1px solid var(--ma-border);
  font-size: var(--ma-font-sm);
}

.mb-inspector__list {
  flex: 1;
  margin: 0;
  padding: 0;
  overflow: auto;
  list-style: none;
}

.mb-inspector__item {
  padding: 6px 10px;
  border-bottom: 1px solid var(--ma-border);
  cursor: pointer;
}

.mb-inspector__item:hover {
  background: var(--ma-bg-elevated);
}

.mb-inspector__item.is-active {
  background: var(--ma-accent-dim);
}

/*
 * 正在编辑的行：左侧强调条 + 底色。
 * 与面板底部的「正在编辑：xxx」标题栏同色系 → 两处一起把"你现在在改这一行"钉死，
 * 避免"以为在新建、其实在改"或反之。
 */
.mb-inspector__item.is-editing {
  background: var(--ma-accent-dim);
  box-shadow: inset 3px 0 0 0 var(--ma-accent);
}

/* 「未定位」说明：节点/卡片被隐藏的原因与影响面，不用红色（不是错误，是缺数据） */
.mb-inspector__unplaced {
  margin: 4px 0 0;
  color: var(--ma-status-warn, var(--ma-text-2));
  font-size: var(--ma-font-xs);
  line-height: 1.4;
}

.mb-inspector__unplaced b {
  color: var(--ma-status-warn, var(--ma-text-1));
}

.mb-inspector__item.is-stale .mb-inspector__name {
  color: var(--ma-text-3);
}

.mb-inspector__row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.mb-inspector__name {
  font-size: var(--ma-font-sm);
  color: var(--ma-text-1);
}

.mb-inspector__acts {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: none;
}

/*
 * 行内动作按钮（编辑 / 取消）。
 *
 * ⭐ 必须**看得见边界**：用户报"点节点行不进编辑态"的现场，行里的「编辑」是一个
 * 无边框的灰色小字，和普通文本几乎一样 —— 用户压根不知道那是个可点的按钮。
 * 加边框 + 底色后，"这里能点"变成一眼可见的事实。
 */
.mb-inspector__act {
  padding: 1px 6px;
  border: 1px solid var(--ma-border-strong, var(--ma-border));
  border-radius: var(--ma-radius);
  background: var(--ma-bg-elevated);
  color: var(--ma-text-2);
  cursor: pointer;
  font-size: var(--ma-font-xs);
  line-height: 1.6;
}

.mb-inspector__act:hover {
  border-color: var(--ma-accent);
  color: var(--ma-accent);
}

/* 每个节点行都亮出它自己的坐标标准 —— 直接回答"这个节点是按什么标准解读的" */
.mb-inspector__crs-tag {
  color: var(--ma-accent);
  font-weight: 600;
}

/* 来源标记（帧 / 声明）：与坐标系值同级可读，但不能抢过值本身 */
.mb-inspector__crs-src {
  color: var(--ma-status-warn, var(--ma-text-3));
}

.mb-inspector__editing {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 4px 6px;
  border: 1px solid var(--ma-accent);
  border-radius: var(--ma-radius);
  background: var(--ma-accent-dim);
  color: var(--ma-accent);
  font-size: var(--ma-font-xs);
}

.mb-inspector__del {
  border: none;
  background: transparent;
  color: var(--ma-text-3);
  cursor: pointer;
  font-size: var(--ma-font-xs);
}

.mb-inspector__del:hover {
  color: var(--ma-status-error, #e5484d);
}

.mb-inspector__orphan {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 6px 10px;
  border-top: 1px solid var(--ma-border);
}

.mb-inspector__form {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 8px 10px;
  border-top: 1px solid var(--ma-border);
}

.mb-inspector__crswrap {
  display: flex;
  flex-direction: column;
  gap: 3px;
}

.mb-inspector__preview {
  margin: 0;
  color: var(--ma-text-3);
  font-size: 10px;
  line-height: 1.4;
}

.mb-inspector__preview code {
  color: var(--ma-accent);
  font-family: var(--ma-font-mono, monospace);
}

.mb-inspector__help {
  margin: 2px 0 0;
  color: var(--ma-text-3);
  font-size: 10px;
  line-height: 1.5;
}

.mb-inspector__help summary {
  cursor: pointer;
  color: var(--ma-text-2);
}

.mb-inspector__help ul {
  margin: 4px 0 4px;
  padding-left: 16px;
}

.mb-inspector__help p {
  margin: 4px 0 0;
}

.mb-inspector__help b {
  color: var(--ma-text-2);
}

.mb-inspector__coords {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 6px;
}

.mb-inspector__input {
  width: 100%;
  padding: 4px 6px;
  border: 1px solid var(--ma-border);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-card);
  color: var(--ma-text-1);
  font-size: var(--ma-font-xs);
}

.mb-inspector__submit {
  padding: 5px 10px;
  border: 1px solid var(--ma-accent);
  border-radius: var(--ma-radius);
  background: var(--ma-accent-dim);
  color: var(--ma-accent);
  cursor: pointer;
  font-size: var(--ma-font-sm);
}

.mb-inspector__submit:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

/* 本地校验提示：把后端 40001 提前变成看得懂的一句话 */
.mb-inspector__err {
  margin: 0;
  color: var(--ma-status-error, #e5484d);
  font-size: var(--ma-font-xs);
  line-height: 1.4;
}

/* 语义提醒（非错误）：如"清空 ≠ 删除" */
.mb-inspector__warn {
  margin: 0;
  color: var(--ma-status-warn, var(--ma-text-2));
  font-size: var(--ma-font-xs);
  line-height: 1.4;
}
</style>