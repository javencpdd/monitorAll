<script setup lang="ts">
/**
 * 单卡浮层外壳（v3 M4）：定位 / 拖拽 / resize / 可见性。
 *
 * ⛔ 定位真源：几何草稿在 mapBoardStore（容器 px 相对节点锚点的偏移），
 *    最终位置 = projector.lngLatToContainer(节点) + 偏移，**只在 rAF 里命令式写 transform**。
 *    绝不能把坐标写进 ref —— 20 卡 × 60Hz 走响应式 = patch 风暴。
 *
 * ⛔ 防回环（note/03 §3.3）：草稿只能由"用户拖拽/resize 提交"或"首次装载"写入，
 *    投影结果**绝不回写**草稿。拖拽期间 pause(id)，松手 resume(id) 由 rAF 重算。
 *
 * ⚠️ 命名（note/03 §3.1）：标签 <card-host> 已在用；本组件标签 <map-card-shell>，
 *    变量不能叫 mapCardShell。
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import CardHost from '@/components/cards/CardHost.vue'
import { provideCardVisibility, type VisibilityReason } from '@/composables/cardVisibility'
import { useMapRuntime } from '@/composables/useMapProjection'
import { useNodePosition } from '@/composables/useNodePosition'
import { useDragResize, type DragRect } from '@/composables/useDragResize'
import { useWillChange } from '@/composables/useWillChange'
import { useMapBoardStore } from '@/stores/mapBoard'
import type { MapNodeView } from '@/types/mapboard'
import type { Card } from '@/types'
import type { LngLat } from '@/utils/geo'
import {
  CARD_CHROME_H,
  MAP_CASCADE_DX,
  MAP_CASCADE_DY,
  MAP_CARD_DEFAULT_H,
  MAP_CARD_DEFAULT_W,
  MAP_CARD_GRIP_H,
  MAP_CARD_VIDEO_CONTENT_H,
  MAP_CARD_VIDEO_W,
  MAP_VISIBLE_NONVIDEO_SOFT_LIMIT,
  MAP_VISIBLE_VIDEO_HARD_LIMIT,
} from '@/utils/constants'

const props = defineProps<{
  card: Card
  node: MapNodeView
  index: number
  /** 被同屏配额拦下：渲染占位卡，不挂载渲染器（不占下行/GPU）。 */
  blocked?: boolean
}>()
const emit = defineEmits<{ (e: 'close'): void }>()

const store = useMapBoardStore()
const runtime = useMapRuntime()

const shellEl = ref<HTMLElement | null>(null)
const gripEl = ref<HTMLElement | null>(null)
const handleEl = ref<HTMLElement | null>(null)

const { lngLatOf } = useNodePosition(() => props.node)

/** 默认尺寸按渲染器类型给（C14）；视频卡用 354×256（播放区 352×198 严格 16:9）。 */
/** 页脚是否显示（chrome 高度随它变，所以 CARD_CHROME_H 必须是函数）。 */
const showFooter = computed<boolean>(() => props.card.displayOptions?.showFooter !== false)

/**
 * 编辑态标题栏占用的高度。
 *
 * ⚠️ 它必须计入"内容区之外的高度"：grip 是 shell 内的 flow 元素（不是浮层），
 * 会把 CardFrame 连同视频内容区一起往下挤 24px。漏算的后果是**编辑态下视频内容区
 * 只有 174px 而不是 198px**，严格 16:9 失效（`object-fit: contain` 只做信箱化、不畸变，
 * 所以肉眼很难发现 —— 典型的静默偏差）。
 */
const gripH = computed<number>(() => (store.editMode && !blocked.value ? MAP_CARD_GRIP_H : 0))

/**
 * 内容区之外的全部高度 = 编辑态 grip + CardFrame chrome（U-6 单一真源）。
 * 默认尺寸与 resize 换算共用它，两边不会各算一套。
 */
const chromeH = computed<number>(() => gripH.value + CARD_CHROME_H(showFooter.value))

/** 占位卡没有 grip（也不该被拖动）。 */
const blocked = computed<boolean>(() => props.blocked === true)

const defaults = computed<{ w: number; h: number; lock: number }>(() => {
  if (props.card.rendererType === 'video') {
    // 外框高 = 播放区高 + chrome。此前硬编码 +58，与 U-6「chrome 单一真源」冲突，
    // 且关掉页脚后尺寸会算错 24px（不报错，视频区被拉伸）。
    return { w: MAP_CARD_VIDEO_W, h: MAP_CARD_VIDEO_CONTENT_H + chromeH.value, lock: 16 / 9 }
  }
  return { w: MAP_CARD_DEFAULT_W, h: MAP_CARD_DEFAULT_H, lock: 0 }
})

/**
 * 几何：草稿优先；没有草稿时用默认值 + 级联偏移（避免多卡完全重叠）。
 *
 * ⚠️ 必须显式依赖 `store.geomVersion`：几何真源 `geomDraft` 是 markRaw（非响应式，性能要求），
 * 直接读它永远不会让 computed 失效 —— 表现是"resize 松手弹回旧尺寸"、
 * "下次拖拽先跳回原位"（评审 F3 已复现）。
 */
const geom = computed<DragRect>(() => {
  void store.geomVersion
  const g = store.geometryOf(props.card.id)
  if (g && g.w > 0 && g.h > 0) return { x: g.x, y: g.y, w: g.w, h: g.h }
  return {
    x: defaults.value.w + MAP_CASCADE_DX * props.index,
    y: -defaults.value.h - MAP_CASCADE_DY,
    w: defaults.value.w,
    h: defaults.value.h,
  }
})

/* ——— 可见性（出视口隐藏 + grace） ——— */
const visible = ref(true)
const reason = ref<VisibilityReason>('none')
/** 出视界的 t0（ms epoch，0 = 在视野内）。渲染器（视频）据此排降载计时（C4）。 */
const outOfViewSince = ref(0)
provideCardVisibility({ visible, reason, outOfViewSince })

let cullTimer = 0
function evaluateVisibility(): void {
  const p = runtime.projector.value
  const ll: LngLat | undefined = lngLatOf()
  if (!p || !ll) return
  const base = p.lngLatToContainer(ll)
  const rect = { x: base.x + geom.value.x, y: base.y + geom.value.y, w: geom.value.w, h: geom.value.h }
  const next = runtime.cull.evaluate(props.card.id, rect, runtime.size.value)
  if (next !== visible.value) {
    visible.value = next
    reason.value = next ? 'user' : 'viewport'
  }
  // t0 要在"还没隐藏"时就开始对外可见 —— 视频的阶段一/阶段二都自它起算
  const t0 = runtime.cull.outOfViewSince(props.card.id)
  if (t0 !== outOfViewSince.value) outOfViewSince.value = t0
}

/* ——— 定位同步池注册 ——— */
let unregister: (() => void) | null = null
function anchor(): LngLat | undefined {
  return lngLatOf()
}

function startCull(): void {
  if (cullTimer) return
  // 200ms 足够跟上人的平移节奏，又不至于让 20 张卡做无谓的矩形相交
  cullTimer = window.setInterval(evaluateVisibility, 200)
  evaluateVisibility()
}

function stopCull(): void {
  if (cullTimer) {
    window.clearInterval(cullTimer)
    cullTimer = 0
  }
}

/**
 * C26 开发期断言：卡片 chrome 高度必须与 `CARD_CHROME_H` 一致。
 *
 * ⚠️ 本组件结构是 `外壳(.mb-card) > body > CardHost > CardFrame(.ma-card)`，
 * 所以不能拿 `shell.offsetHeight - shellBody.offsetHeight` 去比 —— 那个差值里混了
 * 外壳自有的边框与编辑态 grip。真正要守的是**内层 CardFrame** 的 chrome：
 * `frame.offsetHeight - frameBody.offsetHeight === CARD_CHROME_H`。
 * 主题改了边框宽度 / 页脚高度而常量没跟着改时，这里立刻发声（否则是静默偏差）。
 */
function assertChrome(): void {
  if (!import.meta.env.DEV) return
  const shell = shellEl.value
  if (!shell) return
  const frame = shell.querySelector<HTMLElement>('.ma-card')
  const frameBody = shell.querySelector<HTMLElement>('.ma-card__body')
  if (!frame || !frameBody) return // 占位卡没有 CardFrame，跳过
  const actual = frame.offsetHeight - frameBody.offsetHeight
  const expect = CARD_CHROME_H(showFooter.value)
  if (actual !== expect) {
    console.warn(
      `[MapBoard] chrome 高度不一致：CardFrame 实测 ${actual}px ≠ CARD_CHROME_H ${expect}px` +
        `（card=${props.card.id}，检查 --ma-border 宽度 / CARD_HEADER_H / CARD_FOOTER_H）`,
    )
  }
}

onMounted(() => {
  // ⚠️ 占位卡（blocked）**也必须注册投影池**。
  //
  // 之前这里写成 `if (inert()) return`，把注册和轮询一起跳过了 —— 后果有两个，
  // 都是静默的：
  //   1) 初始就被配额拦下的卡不会定位，全部堆在容器左上角 (0,0)，
  //      而不是出现在各自节点旁边，与"看得见的拒绝"意图相悖；
  //   2) 组件实例会随配额变化被复用（MapCardLayer 的 key=card.id），
  //      blocked → unblocked 时 onMounted 不再执行 → 该卡**永久失去定位与剔除**，
  //      停在左上角、不跟随地图与 GPS。
  // 真正需要跳过的是剔除轮询（占位卡不加载媒体，不需要判可见性），也就是下面这个 startCull。
  unregister = runtime.projection.register(`card:${props.card.id}`, {
    el: shellEl.value,
    anchor,
    offset: () => ({ x: geom.value.x, y: geom.value.y }),
    size: () => ({ w: geom.value.w, h: geom.value.h }),
  })
  if (!blocked.value) startCull()
  // 子组件（CardHost）先于本组件 mounted，此时 CardFrame 已在 DOM 里
  assertChrome()
})

// 配额解除/生效时必须补上或停掉轮询 —— 只在 onMounted 判一次会让状态跃迁失效。
watch(
  () => props.blocked,
  (isBlocked) => {
    if (isBlocked) stopCull()
    else startCull()
  },
)

onBeforeUnmount(() => {
  stopCull()
  unregister?.()
  unregister = null
})

/* ——— 拖拽 / resize ——— */
/**
 * 拖拽开始时捕获的「节点锚点 → 容器」基准（容器绝对坐标）。
 *
 * ⚠️ 为什么需要它：`geom.value` 的 x/y 是**相对节点锚点的偏移**，而 `useDragResize`
 * 的 `bounds` 给的是**容器尺寸**。以前直接把偏移量喂给 `clampToBounds`，等于拿
 * 容器的原点去夹一个相对量 —— 结果 x≥0、y≥0，卡片被锁死在节点的右下方：
 * 放不到左边、也放不到上边（用户实际遇到的问题）。
 * 现在 `readRect` 一律返回**容器绝对坐标**（与 bounds 同一空间），
 * `commit` 时再减掉这个基准换回相对量。
 * 基准在 `readRect`（= 拖拽开始瞬间）捕获一次，避免拖拽途中节点因 GPS 更新
 * 而让夹取边界漂移。
 */
let dragBase = { x: 0, y: 0 }

function anchorBase(): { x: number; y: number } {
  const p = runtime.projector.value
  const ll = anchor()
  return p && ll ? p.lngLatToContainer(ll) : { x: 0, y: 0 }
}

function readRect(): DragRect {
  const base = anchorBase()
  dragBase = base
  return { x: base.x + geom.value.x, y: base.y + geom.value.y, w: geom.value.w, h: geom.value.h }
}

function preview(r: DragRect): void {
  // 命令式写视觉，不发请求（松手才进草稿 → debounce → 保存队列）
  const el = shellEl.value
  if (!el) return
  // r 已是容器绝对坐标（见 dragBase 的说明），直接写即可
  el.style.transform = `translate3d(${r.x.toFixed(2)}px, ${r.y.toFixed(2)}px, 0)`
  el.style.width = `${r.w}px`
  el.style.height = `${r.h}px`
}

function commit(r: DragRect): void {
  // 绝对坐标 → 相对节点锚点的偏移（后端存的是后者）
  store.commitGeometry(props.card.id, r.x - dragBase.x, r.y - dragBase.y, r.w, r.h)
}

/**
 * pause / resume 严格成对，且**统一由 useDragResize 的 onStart/onEnd 驱动**。
 *
 * 评审发现的 P0/P1 根因就在这里：此前 resume 只挂在 resize 把手的一个包装函数上，
 * grip 路径有 pause 无 resume → 拖动一次后卡片永久"冻结"（不再跟随地图与 GPS）；
 * 而 resize 路径又在注册监听后**同步**resume，等于暂停从未生效。
 * 现在两者共用同一对回调，不可能再出现不对称。
 */
const { dragging, resizing, onGrip, onHandle } = useDragResize({
  grip: () => gripEl.value,
  handle: () => handleEl.value,
  readRect,
  onPreview: preview,
  onCommit: commit,
  bounds: () => runtime.size.value,
  min: { w: 200, h: 140 },
  lockAspect: () => (store.editMode ? defaults.value.lock || undefined : undefined),
  // 内容区比例 → 外框高度的换算必须带上 chrome，否则视频区被压扁（U-6 单一真源）
  chromeH: () => chromeH.value,
  disabled: () => !store.editMode || blocked.value,
  onStart: () => runtime.projection.pause(`card:${props.card.id}`),
  onEnd: () => runtime.projection.resume(`card:${props.card.id}`),
})

/**
 * 常驻合成层额度（MAP_WILL_CHANGE_MAX）：**只有"热"卡片**才挂 `will-change`。
 *
 * 20 节点 + 同屏 10 卡全部常驻 will-change = 30 个永久合成层，显存随看板规模线性上涨。
 * 这里按优先级抢全局额度：拖拽/resize 中 > 选中 > 其它。
 * ⚠️ 必须放在 useDragResize 之后 —— 权重函数会同步读取 `dragging/resizing`，
 *    提前声明会命中 TDZ（Cannot access 'dragging' before initialization）。
 */
const hot = useWillChange(`card:${props.card.id}`, () => {
  if (dragging.value || resizing.value) return 1000
  if (store.selectedCardId === props.card.id) return 100
  return 1
})
</script>

<template>
  <div
    v-show="visible"
    ref="shellEl"
    class="mb-card"
    :class="{
      'is-selected': store.selectedCardId === card.id,
      'is-dragging': dragging || resizing,
      'is-hot': hot,
      'is-blocked': blocked,
    }"
  >
    <div
      v-if="store.editMode && !blocked"
      ref="gripEl"
      class="mb-card__grip"
      :style="{ height: `${MAP_CARD_GRIP_H}px` }"
      @pointerdown="onGrip"
    >
      <span class="mb-card__title">{{ card.title || '卡片' }}</span>
      <button class="mb-card__close" type="button" @pointerdown.stop @click.stop="emit('close')">×</button>
    </div>

    <div class="mb-card__body">
      <!-- 配额拦截：不挂载 CardHost，因此不订阅、不解码、不占下行 -->
      <div v-if="blocked" class="mb-card__blocked">
        <span class="mb-card__blocked-title">已达同屏上限</span>
        <span class="ma-text-xs ma-text-3">
          同屏最多 {{ MAP_VISIBLE_VIDEO_HARD_LIMIT }} 张视频卡 /
          {{ MAP_VISIBLE_NONVIDEO_SOFT_LIMIT }} 张其它卡。
          想集中看这张，先关掉一张同类卡。
        </span>
        <button class="mb-card__blocked-btn" type="button" @pointerdown.stop @click.stop="emit('close')">
          关闭这张
        </button>
      </div>
      <card-host v-else :card="card" />
    </div>

    <div
      v-if="store.editMode && !blocked"
      ref="handleEl"
      class="mb-card__handle"
      @pointerdown="onHandle"
    ></div>
  </div>
</template>

<style scoped>
.mb-card {
  position: absolute;
  top: 0;
  left: 0;
  /* 卡片层 10 > 节点层 2（U-4：卡片放大/全屏时不得被节点压住） */
  z-index: 10;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  /*
   * 方案 A（team-lead 裁决）：外壳**不再画边框/阴影** —— 视觉 chrome 由内层 CardFrame 统一负责。
   *
   * 为什么必须去掉：本组件是 `外壳 > body > CardHost > CardFrame` 三层嵌套，
   * 外壳自己那条 1px 会与 CardFrame 的 1px 叠成 2px 硬边（观感"糊了一圈"），
   * 且在全局 box-sizing:border-box 下**吃掉 2px 几何** ——
   * 视频卡外框 354×256 时播放区会变成 350×196，而不是冻结口径 C14/C24 的 352×198（严格 16:9）。
   */
  border: none;
  border-radius: var(--ma-radius);
  background: var(--ma-bg-card);
  /* 阴影同样交给 CardFrame（--ma-shadow-card），消除双重阴影 */
  pointer-events: auto;
  /* 常驻合成层不在这里无条件声明：见 useWillChange（MAP_WILL_CHANGE_MAX 封顶） */
  /* M4：禁掉浏览器原生手势，否则触屏滚动会抢走拖拽 */
  touch-action: none;
}

/* 抢到合成层额度（拖拽/resize 中、或被选中）的卡片才常驻 will-change */
.mb-card.is-hot {
  will-change: transform;
}

/*
 * 占位卡（超同屏配额）：内部**没有** CardFrame，所以轮廓与阴影必须由外壳自己补回 ——
 * 否则它只剩一个透明容器，在深色地图上完全看不出"这里其实有一张卡"。
 */
.mb-card.is-blocked {
  border: 1px solid var(--ma-border);
  box-shadow: var(--mb-card-shadow, 0 8px 24px rgb(0 0 0 / 35%));
}

/*
 * 选中态改用 outline：**outline 不参与布局**，不会把去掉的 1px 边框以别的形式加回来
 * （零几何回归）。负 offset 让选中线画在盒子内侧，视觉上贴合卡片边界。
 */
.mb-card.is-selected {
  z-index: 50;
  outline: 1px solid var(--ma-accent);
  outline-offset: -1px;
}

.mb-card.is-dragging {
  z-index: 100;
}

.mb-card__grip {
  display: flex;
  align-items: center;
  justify-content: space-between;
  /* 高度走内联 style（MAP_CARD_GRIP_H 单一真源，与 C26 断言口径一致） */
  padding: 0 6px;
  border-bottom: 1px solid var(--ma-border);
  background: var(--ma-bg-elevated);
  cursor: move;
  flex: none;
  touch-action: none;
}

.mb-card__title {
  overflow: hidden;
  color: var(--ma-text-2);
  font-size: var(--ma-font-xs);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.mb-card__close {
  border: none;
  background: transparent;
  color: var(--ma-text-3);
  cursor: pointer;
  font-size: 14px;
  line-height: 1;
  padding: 0 2px;
}

.mb-card__close:hover {
  color: var(--ma-status-error, #e5484d);
}

.mb-card__body {
  flex: 1;
  min-height: 0;
  /* M6：可滚动子区到底不把滚动链传给地图 */
  overflow: auto;
  overscroll-behavior: contain;
}

.mb-card__blocked {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 6px;
  height: 100%;
  padding: 12px;
  text-align: center;
}

.mb-card__blocked-title {
  color: var(--ma-status-warn, var(--ma-text-2));
  font-size: var(--ma-font-sm);
}

.mb-card__blocked-btn {
  padding: 3px 10px;
  border: 1px solid var(--ma-border-strong, var(--ma-border));
  border-radius: var(--ma-radius);
  background: var(--ma-bg-elevated);
  color: var(--ma-text-1);
  cursor: pointer;
  font-size: var(--ma-font-xs);
}

.mb-card__handle {
  position: absolute;
  right: 0;
  bottom: 0;
  width: 14px;
  height: 14px;
  cursor: nwse-resize;
  background: linear-gradient(
    135deg,
    transparent 0 50%,
    var(--ma-text-3) 50% 60%,
    transparent 60% 72%,
    var(--ma-text-3) 72% 82%,
    transparent 82%
  );
  touch-action: none;
}
</style>