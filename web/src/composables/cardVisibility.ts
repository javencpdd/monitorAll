/**
 * CardVisibility：渲染器可感知的「当前是否可见」（v3 视口剔除 / 降载）。
 *
 * 由 `MapCardShell` provide；渲染器（VideoRenderer / LineChartRenderer）inject 后
 * 自行决定降载行为，渲染器不感知容器种类。
 *
 * ⭐ 缺省 `{ visible: true, reason: 'none' }`：网格看板与配置抽屉预览
 *    inject 不到时行为与改造前完全一致（零回归）。
 */
import { inject, provide, type InjectionKey, type Ref } from 'vue'
import { ref } from 'vue'

export type VisibilityReason = 'viewport' | 'quota' | 'user' | 'none'

export interface CardVisibility {
  visible: Ref<boolean>
  reason: Ref<VisibilityReason>
  /**
   * 彻底出视界的起始时刻 t0（ms epoch）；在视野内为 0。
   *
   * 渲染器（尤其视频）必须用它来排降载计时，而不是从 `visible` 翻 false 的那一刻
   * 重新起算 —— C4 明确要求"所有计时自 t0 起算，不串行叠加"。
   * 从 visible 起算会白等一个 grace（1s），实际断流时刻变成 t0+2s / t0+11s。
   */
  outOfViewSince: Ref<number>
}

export const CardVisibilityKey: InjectionKey<CardVisibility> = Symbol('ma:card-visibility')

export function provideCardVisibility(vis: CardVisibility): void {
  provide(CardVisibilityKey, vis)
}

/** 缺省实现：永远可见（网格路径 / 抽屉预览），t0 恒为 0。 */
export function useCardVisibility(): CardVisibility {
  const injected = inject(CardVisibilityKey, null)
  if (injected) return injected
  return {
    visible: ref(true),
    reason: ref<VisibilityReason>('none'),
    outOfViewSince: ref(0),
  }
}
