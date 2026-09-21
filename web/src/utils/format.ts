/**
 * 数值 / 时间 / 单位格式化统一出口。
 * 渲染器与 UI 一律走这里，禁止各自 toFixed。
 */

/** 按小数位格式化数字，非有限值返回占位符。 */
export function formatNumber(value: number | undefined, decimals = 2): string {
  if (value === undefined || !Number.isFinite(value)) return '--'
  return value.toFixed(clampDecimals(decimals))
}

/** 带单位的数值展示：单位为空时只给数值。 */
export function formatWithUnit(value: number | undefined, unit: string | undefined, decimals = 2): string {
  const num = formatNumber(value, decimals)
  if (!unit) return num
  return `${num} ${unit}`
}

export function clampDecimals(decimals: number | undefined): number {
  if (decimals === undefined || !Number.isFinite(decimals)) return 2
  return Math.min(6, Math.max(0, Math.trunc(decimals)))
}

/** 千分位整数（计数类场景）。 */
export function formatThousands(value: number | undefined): string {
  if (value === undefined || !Number.isFinite(value)) return '--'
  return Math.trunc(value).toLocaleString('zh-CN')
}

/** 毫秒时间戳 → HH:MM:SS.mmm。 */
export function formatClock(ts: number | undefined): string {
  if (ts === undefined || !Number.isFinite(ts) || ts <= 0) return '--:--:--'
  const d = new Date(ts)
  const pad = (n: number, len = 2): string => String(n).padStart(len, '0')
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}`
}

/** 毫秒时间戳 → HH:MM:SS（秒级，图例/坐标用）。 */
export function formatTimeShort(ts: number | undefined): string {
  if (ts === undefined || !Number.isFinite(ts) || ts <= 0) return '--:--:--'
  const d = new Date(ts)
  const pad = (n: number): string => String(n).padStart(2, '0')
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

/** 毫秒间隔 → 人类可读时长（1.2s / 340ms）。 */
export function formatDuration(ms: number | undefined): string {
  if (ms === undefined || !Number.isFinite(ms)) return '--'
  if (Math.abs(ms) >= 1000) return `${(ms / 1000).toFixed(1)}s`
  return `${Math.trunc(ms)}ms`
}

/** 距今多久（3s 前）。 */
export function formatRelative(ts: number | undefined, now = Date.now()): string {
  if (ts === undefined || ts <= 0) return '从未'
  const diff = now - ts
  if (diff < 0) return '刚刚'
  return `${formatDuration(diff)}前`
}

/** 字节数 → KB/MB。 */
export function formatBytes(bytes: number | undefined): string {
  if (bytes === undefined || !Number.isFinite(bytes)) return '--'
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(2)} MB`
}

/** 频率 → 保留 1 位的 Hz 文案。 */
export function formatHz(hz: number | undefined): string {
  if (hz === undefined || !Number.isFinite(hz)) return '-- Hz'
  return `${hz.toFixed(1)} Hz`
}

/** 百分比。 */
export function formatPercent(ratio: number | undefined, decimals = 0): string {
  if (ratio === undefined || !Number.isFinite(ratio)) return '--'
  return `${(ratio * 100).toFixed(decimals)}%`
}

/** 把任意值安全地转成展示字符串。 */
export function formatValue(value: unknown, decimals = 2): string {
  if (value === null || value === undefined) return ''
  if (typeof value === 'number') return formatNumber(value, decimals)
  if (typeof value === 'boolean') return value ? 'true' : 'false'
  if (typeof value === 'string') return value
  try {
    return JSON.stringify(value)
  } catch {
    return String(value)
  }
}

/** 推断运行时的值类型，用于 JSON 树与表格列的类型标注。 */
export function inferType(value: unknown): 'number' | 'string' | 'boolean' | 'object' | 'array' | 'null' {
  if (value === null) return 'null'
  if (Array.isArray(value)) return 'array'
  if (typeof value === 'number') return 'number'
  if (typeof value === 'boolean') return 'boolean'
  if (typeof value === 'string') return 'string'
  return 'object'
}

export function clamp(value: number, min: number, max: number): number {
  if (!Number.isFinite(value)) return min
  return Math.min(max, Math.max(min, value))
}
