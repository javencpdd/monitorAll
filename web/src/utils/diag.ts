/**
 * 诊断总线（v3）—— 取代 `DashboardCanvas.vue` 里那套临时诊断角标。
 *
 * 三条设计约束：
 *  1. **全程不进响应式**：计数器是裸对象，不触发任何组件更新。
 *     HUD 靠 `setInterval` 主动 snapshot，因此「诊断」与「业务渲染」完全解耦。
 *     （旧实现的 `onUpdated(probeDom)` 每次更新都查 DOM，是真实的性能负债。）
 *  2. **单一出口**：全项目所有计数只走本文件。P-14 无论要求内存计数还是落盘，
 *     都只改这里，组件零改动。
 *  3. **默认不可见**：由 `DiagLayer` 按开关决定是否渲染；off 档时 DOM 里不存在诊断节点。
 */

export type DiagValue = number | string

export interface DiagSnapshot {
  at: number
  values: Record<string, DiagValue>
}

type Counter = { n: number }
type Sampler = { ema: number; max: number }

const counters: Record<string, Counter> = {}
const samplers: Record<string, Sampler> = {}
const sources = new Map<string, () => Record<string, DiagValue>>()
/** 一次性事实（如能力探测结论），写入后不再变。 */
const facts: Record<string, DiagValue> = {}

/** 计数 +n（默认 1）。 */
export function bump(key: string, n = 1): void {
  const c = counters[key]
  if (c) c.n += n
  else counters[key] = { n }
}

/** 读取计数（不清零）。 */
export function countOf(key: string): number {
  return counters[key]?.n ?? 0
}

/**
 * 耗时采样（ms）。保留 EMA 与最大值，够用来发现「单帧同步是否超预算」。
 * 不存全量样本，避免诊断自身制造内存压力（P-10）。
 */
export function sample(key: string, ms: number): void {
  const s = samplers[key]
  if (!s) {
    samplers[key] = { ema: ms, max: ms }
    return
  }
  s.ema = s.ema * 0.8 + ms * 0.2
  if (ms > s.max) s.max = ms
}

/** 记录一次性事实（能力探测结论等）。 */
export function setFact(key: string, value: DiagValue): void {
  facts[key] = value
}

/** 注册一个数据源（返回注销函数）。 */
export function registerSource(id: string, fn: () => Record<string, DiagValue>): () => void {
  sources.set(id, fn)
  return () => {
    sources.delete(id)
  }
}

/** 生成一份快照（供 HUD 定时拉取）。 */
export function snapshot(): DiagSnapshot {
  const values: Record<string, DiagValue> = { ...facts }
  for (const [key, c] of Object.entries(counters)) values[key] = c.n
  for (const [key, s] of Object.entries(samplers)) {
    values[`${key}.ema`] = Math.round(s.ema * 100) / 100
    values[`${key}.max`] = Math.round(s.max * 100) / 100
  }
  for (const [id, fn] of sources) {
    try {
      for (const [k, v] of Object.entries(fn())) values[`${id}.${k}`] = v
    } catch {
      values[`${id}.error`] = 'ERR'
    }
  }
  return { at: Date.now(), values }
}

/** 清空（HMR / 测试用）。 */
export function resetDiag(): void {
  for (const key of Object.keys(counters)) delete counters[key]
  for (const key of Object.keys(samplers)) delete samplers[key]
  for (const key of Object.keys(facts)) delete facts[key]
  sources.clear()
}

/* ——— 诊断开关（off | minimal | verbose）——— */

export type DiagLevel = 'off' | 'minimal' | 'verbose'

const STORAGE_KEY = 'ma.diag'
const READERS: Array<() => DiagLevel | null> = [readFromQuery, readFromStorage]
let override: DiagLevel | null = null

function readFromQuery(): DiagLevel | null {
  if (typeof window === 'undefined') return null
  const q = new URLSearchParams(window.location.search).get('diag')
  return q === '1' ? 'minimal' : q === '2' ? 'verbose' : null
}

function readFromStorage(): DiagLevel | null {
  if (typeof window === 'undefined') return null
  try {
    const v = window.localStorage.getItem(STORAGE_KEY)
    return v === 'off' || v === 'minimal' || v === 'verbose' ? v : null
  } catch {
    return null
  }
}

/** 当前诊断档位（默认 off：生产零开销）。 */
export function diagLevel(): DiagLevel {
  if (override) return override
  for (const read of READERS) {
    const v = read()
    if (v) return v
  }
  return 'off'
}

export function setDiagLevel(level: DiagLevel | null): void {
  override = level
  if (typeof window === 'undefined' || level === null) return
  try {
    window.localStorage.setItem(STORAGE_KEY, level)
  } catch {
    // 隐私模式下 localStorage 不可写，忽略
  }
}
