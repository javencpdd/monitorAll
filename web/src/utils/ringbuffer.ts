/**
 * 定容环形缓冲区。
 *
 * 用于 frameStore 的通道历史：写入 O(1)，达到容量上限后自动覆盖最旧数据，
 * 因此长时间高频运行不会产生内存增长，也不会有数组搬移的 GC 抖动（ARCH §12.2 ST-07）。
 */
export class RingBuffer<T> {
  private readonly items: (T | undefined)[]
  private head = 0
  private count = 0

  constructor(private readonly capacity: number) {
    if (capacity <= 0) throw new Error('RingBuffer capacity must be positive')
    this.items = new Array<T | undefined>(capacity)
  }

  /** 当前已写入元素个数（≤ capacity）。 */
  get size(): number {
    return this.count
  }

  /** 是否已写满一轮。 */
  get full(): boolean {
    return this.count === this.capacity
  }

  get limit(): number {
    return this.capacity
  }

  /** 写入一个元素；写满后覆盖最旧的元素。 */
  push(item: T): void {
    this.items[this.head] = item
    this.head = (this.head + 1) % this.capacity
    if (this.count < this.capacity) this.count += 1
  }

  /** 读取第 i 个最旧元素（0 = 最旧）。越界返回 undefined。 */
  at(index: number): T | undefined {
    if (index < 0 || index >= this.count) return undefined
    const start = (this.head - this.count + this.capacity) % this.capacity
    return this.items[(start + index) % this.capacity]
  }

  /** 最新写入的元素。 */
  last(): T | undefined {
    return this.count === 0 ? undefined : this.items[(this.head - 1 + this.capacity) % this.capacity]
  }

  /** 导出为按时间从旧到新的数组。 */
  toArray(): T[] {
    const out: T[] = []
    const start = (this.head - this.count + this.capacity) % this.capacity
    for (let i = 0; i < this.count; i += 1) {
      const item = this.items[(start + i) % this.capacity]
      if (item !== undefined) out.push(item)
    }
    return out
  }

  /** 取最后 n 条（按时间从旧到新）。 */
  tail(n: number): T[] {
    const take = Math.min(n, this.count)
    if (take <= 0) return []
    const out: T[] = []
    for (let i = this.count - take; i < this.count; i += 1) {
      const item = this.at(i)
      if (item !== undefined) out.push(item)
    }
    return out
  }

  /** 按最旧优先顺序遍历，回调返回 false 可提前中断。 */
  forEach(visitor: (item: T, index: number) => boolean | void): void {
    const start = (this.head - this.count + this.capacity) % this.capacity
    for (let i = 0; i < this.count; i += 1) {
      const item = this.items[(start + i) % this.capacity]
      if (item === undefined) continue
      if (visitor(item, i) === false) break
    }
  }

  clear(): void {
    this.items.fill(undefined)
    this.head = 0
    this.count = 0
  }
}
