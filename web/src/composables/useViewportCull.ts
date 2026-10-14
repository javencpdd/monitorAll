/**
 * 视口剔除（v3 M4）：节点出可视范围 → 卡片隐藏；回来 → 恢复。
 *
 * 两条防抖，方向不对称（这是有意的）：
 *   - 隐藏方向：hysteresis 64px **且** grace MAP_CULL_GRACE_MS(1000ms)
 *     → 节点在视口边缘来回蹭时不会反复拉流/断流（P-06：60s 内切换 0 次）
 *   - 恢复方向：**立即**（0ms）
 *     → 因为恢复延迟有硬指标（非视频 ≤200ms、视频 ≤1.5s），若恢复也等 1s 直接超标
 *
 * ⛔ 判定只能用「投影后的容器 px vs 容器矩形」（U-5），**禁止 map.getBounds().contains()**：
 *    无 Key 的 VirtualProjector 下没有这个 API，用了就等于把降级路径写死。
 */
import { MAP_CULL_GRACE_MS, MAP_CULL_HYSTERESIS_PX } from '@/utils/constants'
import type { Size } from '@/utils/projector'
import { bump } from '@/utils/diag'

export interface Rect {
  x: number
  y: number
  w: number
  h: number
}

interface CullState {
  visible: boolean
  /** 已安排隐藏的计时器（0 = 无）。 */
  hideTimer: number
  /** grace 到期时的复核句柄。 */
  pending: number
  /**
   * 彻底出视界的起始时刻 t0（ms epoch）；仍在视野内时为 0。
   *
   * C4 要求"所有降载计时都自 t0 起算，不串行叠加"——视频的两阶段断流
   * 必须读这个 t0，而不是从 `visible` 翻 false 的那一刻重新计时。
   * 后者会让实际时刻变成 t0+1s+1s / t0+1s+10s，白等 1 秒。
   */
  outSince: number
}

export interface ViewportCullApi {
  /**
   * 判定一条目的可见性（命令式，可在每次同步时调用）。
   * @param id稳定标识（卡片 id）
   * @param rect 当前容器 px 矩形；undefined = 还没定位，跳过判定
   * @returns 该条目"当前应当"的可见态
   */
  evaluate(id: string, rect: Rect | undefined, container: Size): boolean
  /** 立即强制某条目可见（用户手动展开时用，跳过 grace）。 */
  forceVisible(id: string): void
  /** 该条目彻底出视界的起始时刻 t0（ms epoch）；在视野内返回 0。 */
  outOfViewSince(id: string): number
  /** 隐藏/显示切换次数（P-06 观测：稳定态下应恒为 0）。 */
  flips(): number
  /** 清空所有计时器（卸载 / 切看板）。 */
  clear(): void
}

/** 进入严格（pad=0），退出宽松（pad=64）。 */
function intersects(r: Rect, size: Size, pad: number): boolean {
  return (
    r.x + r.w > -pad && r.x < size.w + pad && r.y + r.h > -pad && r.y < size.h + pad
  )
}

export function useViewportCull(): ViewportCullApi {
  const states = new Map<string, CullState>()
  let flipCount = 0

  function stateOf(id: string): CullState {
    let s = states.get(id)
    if (!s) {
      s = { visible: true, hideTimer: 0, pending: 0, outSince: 0 }
      states.set(id, s)
    }
    return s
  }

  function setVisible(id: string, next: boolean): void {
    const s = stateOf(id)
    if (s.visible === next) return
    s.visible = next
    flipCount += 1
    bump('cullFlips')
  }

  /** 回到视野：计时器**清零**（不是暂停续算），t0 也一并清掉。 */
  function resetTimers(s: CullState): void {
    if (s.hideTimer) {
      window.clearTimeout(s.hideTimer)
      s.hideTimer = 0
    }
    if (s.pending) {
      window.clearTimeout(s.pending)
      s.pending = 0
    }
    s.outSince = 0
  }

  function evaluate(id: string, rect: Rect | undefined, container: Size): boolean {
    const s = stateOf(id)
    if (!rect || container.w <= 0 || container.h <= 0) return s.visible

    const insideStrict = intersects(rect, container, 0)
    const insideLoose = intersects(rect, container, MAP_CULL_HYSTERESIS_PX)

    if (s.visible) {
      if (insideStrict || insideLoose) {
        // 迟滞带内也视为"还在"，把 t0 清掉 —— 用户只是轻微移出，不该触发任何降载
        resetTimers(s)
        return true
      }
      // 彻底出界 → 记录 t0 并起 grace 计时（t0 只记一次，后续不动）
      if (!s.hideTimer) {
        s.outSince = Date.now()
        s.hideTimer = window.setTimeout(() => {
          s.hideTimer = 0
          if (!insideLoose) setVisible(id, false)
        }, MAP_CULL_GRACE_MS)
      }
      return true
    }

    // 隐藏态：必须真的进视野才恢复（严格）
    if (insideStrict) {
      resetTimers(s)
      setVisible(id, true)
      return true
    }
    return false
  }

  function forceVisible(id: string): void {
    const s = stateOf(id)
    resetTimers(s)
    setVisible(id, true)
  }

  function outOfViewSince(id: string): number {
    return states.get(id)?.outSince ?? 0
  }

  function clear(): void {
    for (const s of states.values()) resetTimers(s)
    states.clear()
  }

  return { evaluate, forceVisible, outOfViewSince, flips: () => flipCount, clear }
}