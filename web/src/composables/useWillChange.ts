/**
 * 合成层预算（v3，MAP_WILL_CHANGE_MAX 落地）。
 *
 * 为什么需要：`will-change: transform`（以及 `translate3d`）会让浏览器把元素**永久**提升为
 * 独立合成层。本项目上限是 20 节点 + 同屏最多 10 卡（4 视频 + 6 其它）——
 * 40 个层常驻会让显存/内存曲线随看板规模线性上涨，且在低端 Android 上表现为
 * "什么都没干，GPU 内存就满了"。所以**常驻** `will-change` 必须封顶。
 *
 * 语义：
 *   - 全局共享一个额度池（节点与卡片**共用** MAP_WILL_CHANGE_MAX，不是各 8）；
 *   - 按权重排序取前 N：正在拖拽/resize > 选中 > 有卡展开 > 其它；
 *   - 权重依赖的响应式状态一变就重排（watchEffect 自动收集依赖）；
 *   - 卸载即归还额度。
 *
 * ⚠️ 本模块**只控制常驻 will-change**，不改同步池的 `translate3d`（U-2 硬要求）。
 *    平移过程中浏览器给出的临时提升是瞬时的、会自动回收；这里砍掉的是"常驻不吃不喝"的那部分。
 */
import { onBeforeUnmount, ref, watchEffect, type Ref } from 'vue'
import { MAP_WILL_CHANGE_MAX } from '@/utils/constants'

interface Slot {
  /** 越大越优先拿到额度（读取响应式状态 → 变化时重排）。 */
  weight: () => number
  granted: Ref<boolean>
}

const slots = new Map<string, Slot>()

function recompute(): void {
  const ordered = [...slots.values()].sort((a, b) => b.weight() - a.weight())
  ordered.forEach((slot, i) => {
    slot.granted.value = i < MAP_WILL_CHANGE_MAX
  })
}

/**
 * 申请一个常驻合成层额度。
 *
 * @param key 全局唯一（必须与同步池 id 同前缀：`node:xxx` / `card:xxx`），同一 key 复用同一 ref。
 * @param weight 优先级取值函数；**必须同步读取响应式状态**，否则不会自动重排。
 * @returns 是否拿到额度（响应式）；直接绑到 class/style 即可。
 */
export function useWillChange(key: string, weight: () => number): Readonly<Ref<boolean>> {
  let slot = slots.get(key)
  if (!slot) {
    slot = { weight, granted: ref(false) }
    slots.set(key, slot)
  } else {
    // 组件重建（key 不变、实例换新）时替换取值函数，避免闭包指向已卸载实例
    slot.weight = weight
  }
  const current = slot

  // 权重依赖的任何响应式状态一变就重排（拖拽起止 / 选中切换 / 开卡）
  watchEffect(() => {
    void weight()
    recompute()
  })
  recompute()

  onBeforeUnmount(() => {
    slots.delete(key)
    recompute()
  })

  return current.granted
}

/** 当前拿到额度的数量（诊断用）。 */
export function willChangeGrantedCount(): number {
  let n = 0
  for (const slot of slots.values()) if (slot.granted.value) n += 1
  return n
}
