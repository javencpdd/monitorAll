/**
 * 拖拽 / resize（v3 M4，自研 pointer events）。
 *
 * 为什么不用 grid-layout-plus 或现成拖拽库（选型已否决，理由见架构 §3.4）：
 *   卡片位置由**外部**（地图投影 + 节点位置）驱动，第三方组件内部持有 rect
 *   会形成"外部每帧写 transform ↔ 内部回写 rect"的双向写入 —— 这正是
 *   note/03 §3.3 那次把浏览器卡死的自激振荡模式。
 *
 * 与地图手势不冲突的六条机制（缺一条就会"拖卡片时地图跟着跑"）：
 *   M1 overlay 是 map 容器的**兄弟节点** → 卡片事件不会冒泡进 AMap canvas
 *   M2 overlay 根 `pointer-events:none`，仅卡片与把手 `auto`
 *   M3 把手 pointerdown 做 stopPropagation + preventDefault + setPointerCapture
 *   M4 卡片/把手 `touch-action:none`
 *   M5 拖拽期间 pause 该卡片的外部同步（由 MapCardShell 调 projection.pause）
 *   M6 可滚动子区 `overscroll-behavior: contain`
 */
import { ref, type Ref } from 'vue'
import { CARD_BORDER_PX, MAP_DRAG_THRESHOLD_PX, MAP_EDGE_SNAP_PX } from '@/utils/constants'

export interface DragRect {
  x: number
  y: number
  w: number
  h: number
}

export interface DragResizeOptions {
  /** 拖拽把手（标题栏）。 */
  grip: () => HTMLElement | null
  /** resize 手柄（右下角）。 */
  handle: () => HTMLElement | null
  /** 读取当前矩形（容器 px，绝对坐标）。 */
  readRect: () => DragRect
  /** 拖拽/resize 中的实时预览（命令式写视觉，不发请求）。 */
  onPreview: (r: DragRect) => void
  /** 松手提交（进本地草稿 → debounce → 保存队列）。 */
  onCommit: (r: DragRect) => void
  /** 容器尺寸（做边界钳制）。 */
  bounds: () => { w: number; h: number }
  min?: { w: number; h: number }
  /**
   * 锁定宽高比（如视频卡**内容区** 16:9）；宽度为主轴。返回 undefined = 自由 resize。
   *
   * ⚠️ 这里锁的是**内容区**比例，而拖拽改的是**外框**尺寸 —— 所以必须同时提供
   * `chromeH`，否则外框高度会被当成内容高度，视频区被压扁且不报错。
   */
  lockAspect?: () => number | undefined
  /**
   * 锁定宽高比时的高度附加量（外框 chrome：header + footer + 边框×2）。
   *
   * ⚠️ 必须加：`lockAspect` 锁的是**内容区**比例（视频播放区 352×198），
   * 而拖拽改的是**外框**高度。漏掉 chrome 会让外框高度等于内容高度，
   * 视频区被压扁、16:9 失真（且不报错）。
   * 传 `CARD_CHROME_H(showFooter)` 的结果（U-6 单一真源）。
   */
  chromeH?: () => number
  /** 查看模式下禁用。 */
  disabled?: () => boolean
  /**
   * 开始交互（用于暂停外部同步）。
   *
   * ⚠️ 本回调是**无条件**在 pointerdown 时调用的，因此 `onEnd` 也必须无条件在
   * pointerup/pointercancel 时调用 —— 两者必须成对，差一个就会让调用方永久停在"暂停"态。
   * （这正是评审发现的 P0：grip 路径只调了 onStart 没调 onEnd，卡片拖一次后永久冻结。）
   */
  onStart?: () => void
  /** 交互结束（恢复外部同步）。**无论是否真的产生位移都会被调用。** */
  onEnd?: () => void
}

export interface DragResizeApi {
  dragging: Ref<boolean>
  resizing: Ref<boolean>
  /** 挂到把手（标题栏）上。 */
  onGrip: (e: PointerEvent) => void
  /** 挂到 resize 手柄上。 */
  onHandle: (e: PointerEvent) => void
}

export function useDragResize(opts: DragResizeOptions): DragResizeApi {
  const dragging = ref(false)
  const resizing = ref(false)

  function clampToBounds(r: DragRect): DragRect {
    const b = opts.bounds()
    const min = opts.min ?? { w: 80, h: 80 }
    const w = Math.max(min.w, Math.min(r.w, b.w))
    const h = Math.max(min.h, Math.min(r.h, b.h))
    let x = r.x
    let y = r.y
    x = Math.max(0, Math.min(x, Math.max(0, b.w - w)))
    y = Math.max(0, Math.min(y, Math.max(0, b.h - h)))
    // 贴边吸附：距边 < 8px 时直接对齐（A3）
    if (x > 0 && x < MAP_EDGE_SNAP_PX) x = 0
    if (y > 0 && y < MAP_EDGE_SNAP_PX) y = 0
    if (b.w - (x + w) > 0 && b.w - (x + w) < MAP_EDGE_SNAP_PX) x = b.w - w
    if (b.h - (y + h) > 0 && b.h - (y + h) < MAP_EDGE_SNAP_PX) y = b.h - h
    return { x: Math.round(x), y: Math.round(y), w: Math.round(w), h: Math.round(h) }
  }

  function beginDrag(e: PointerEvent): void {
    if (opts.disabled?.()) return
    // M3：阻断冒泡 + 阻止默认 + 捕获指针，三者缺一都会出问题
    e.stopPropagation()
    e.preventDefault()
    const grip = opts.grip()
    try {
      grip?.setPointerCapture(e.pointerId)
    } catch {
      // 老 WebView 可能不支持 setPointerCapture：降级为 document 级监听（见 bindMove）
    }

    const startRect = opts.readRect()
    const startX = e.clientX
    const startY = e.clientY
    let moved = false
    dragging.value = true
    opts.onStart?.()

    const onMove = (ev: PointerEvent): void => {
      const dx = ev.clientX - startX
      const dy = ev.clientY - startY
      if (!moved) {
        // 3px 阈值：区分「点击」与「拖拽」，避免误触
        if (Math.abs(dx) < MAP_DRAG_THRESHOLD_PX && Math.abs(dy) < MAP_DRAG_THRESHOLD_PX) return
        moved = true
      }
      ev.stopPropagation()
      opts.onPreview(clampToBounds({ ...startRect, x: startRect.x + dx, y: startRect.y + dy }))
    }

    const onUp = (ev: PointerEvent): void => {
      ev.stopPropagation()
      grip?.releasePointerCapture?.(ev.pointerId)
      detach()
      dragging.value = false
      const cancelled = ev.type === 'pointercancel'
      if (moved && !cancelled) {
        const dx = ev.clientX - startX
        const dy = ev.clientY - startY
        opts.onCommit(clampToBounds({ ...startRect, x: startRect.x + dx, y: startRect.y + dy }))
      }
      // 顺序不能反：先 commit（草稿落定）再 onEnd（恢复同步）。
      // 反过来会让 rAF 立刻用【旧草稿】把卡片写回原位，用户看到"松手弹回"。
      opts.onEnd?.()
    }

    function detach(): void {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
      window.removeEventListener('pointercancel', onUp)
    }

    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
    window.addEventListener('pointercancel', onUp)
  }

  function beginResize(e: PointerEvent): void {
    if (opts.disabled?.()) return
    e.stopPropagation()
    e.preventDefault()
    const handle = opts.handle()
    try {
      handle?.setPointerCapture(e.pointerId)
    } catch {
      /* 同上：降级为 window 监听 */
    }

    const startRect = opts.readRect()
    const startX = e.clientX
    const aspect = opts.lockAspect?.()
    let moved = false
    resizing.value = true
    opts.onStart?.()

    const onMove = (ev: PointerEvent): void => {
      const dx = ev.clientX - startX
      if (!moved) {
        if (Math.abs(dx) < MAP_DRAG_THRESHOLD_PX) return
        moved = true
      }
      ev.stopPropagation()
      let w = startRect.w + dx
      if (aspect && aspect > 0) {
        // 以【宽度】为主轴：lockAspect 是**内容区**比例（视频播放区），
        // 外框高度 = 内容高度 + chrome，漏 chrome 会把视频区压扁且不报错。
        const contentW = Math.max(1, w - CARD_BORDER_PX * 2)
        w = contentW + CARD_BORDER_PX * 2
        const h = Math.round(contentW / aspect) + (opts.chromeH?.() ?? 0)
        opts.onPreview(clampToBounds({ x: startRect.x, y: startRect.y, w, h }))
        return
      }
      opts.onPreview(clampToBounds({ ...startRect, w }))
    }

    const onUp = (ev: PointerEvent): void => {
      ev.stopPropagation()
      handle?.releasePointerCapture?.(ev.pointerId)
      detach()
      resizing.value = false
      const cancelled = ev.type === 'pointercancel'
      if (moved && !cancelled) {
        const dx = ev.clientX - startX
        const next = clampToBounds({ ...startRect, w: startRect.w + dx })
        if (aspect && aspect > 0) {
          const contentW = Math.max(1, next.w - CARD_BORDER_PX * 2)
          next.h = Math.round(contentW / aspect) + (opts.chromeH?.() ?? 0)
        }
        opts.onCommit(clampToBounds(next))
      }
      opts.onEnd?.()
    }

    function detach(): void {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
      window.removeEventListener('pointercancel', onUp)
    }

    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
    window.addEventListener('pointercancel', onUp)
  }

  return {
    dragging,
    resizing,
    onGrip: beginDrag,
    onHandle: beginResize,
  }
}