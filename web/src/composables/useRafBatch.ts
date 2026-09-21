/**
 * rAF 批量刷新调度器（全局单例）── 前端三层高频防护的第二层（ARCH §10.2）。
 *
 * 同一 key（通常是 channelId）在两次浏览器帧之间只允许保留最后一次任务，
 * 保证「每通道每帧最多 1 次响应式更新」，避免高频数据打爆渲染。
 */
type Task = () => void

const pending = new Map<string, Task>()
let rafHandle = 0

function flushAll(): void {
  rafHandle = 0
  if (pending.size === 0) return
  // 快照后清空，避免任务内部再次 schedule 造成同帧递归
  const tasks = Array.from(pending.values())
  pending.clear()
  for (const task of tasks) {
    try {
      task()
    } catch (err) {
      console.error('[rafBatch] 任务执行异常：', err)
    }
  }
}

/** 注册一次批量任务（同 key 覆盖上一次，只保留最新值）。 */
export function scheduleBatch(key: string, task: Task): void {
  pending.set(key, task)
  if (rafHandle === 0) {
    rafHandle = window.requestAnimationFrame(flushAll)
  }
}

/** 立即执行指定 key 的待办任务（页面隐藏/卸载前调用，避免丢失最后一帧）。 */
export function flushBatch(key?: string): void {
  if (key === undefined) {
    if (rafHandle !== 0) window.cancelAnimationFrame(rafHandle)
    flushAll()
    return
  }
  const task = pending.get(key)
  if (!task) return
  pending.delete(key)
  task()
}

/** 当前待执行任务数（调试用）。 */
export function pendingCount(): number {
  return pending.size
}
