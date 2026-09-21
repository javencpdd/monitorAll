/**
 * CSV 导出（RD-04 折线图导出，T-25）。
 * 导出内容与图上可见数据一致：包含表头、转义双引号、BOM（Excel 中文友好）。
 */
import { formatValue } from './format'

const CSV_BOM = '\uFEFF'

/** 转义 CSV 单元格：含分隔符/引号/换行时加双引号并转义内部引号。 */
export function escapeCell(value: unknown, decimals = 6): string {
  const text = typeof value === 'number' ? String(value) : formatValue(value, decimals)
  if (text.includes(',') || text.includes('"') || text.includes('\n') || text.includes('\r')) {
    return `"${text.replace(/"/g, '""')}"`
  }
  return text
}

/** 二维数组 → CSV 文本（含表头）。 */
export function toCSV(headers: string[], rows: unknown[][]): string {
  const lines: string[] = [headers.map((h) => escapeCell(h)).join(',')]
  for (const row of rows) {
    lines.push(row.map((cell) => escapeCell(cell)).join(','))
  }
  return CSV_BOM + lines.join('\r\n')
}

/** 触发浏览器下载。 */
export function downloadFile(filename: string, content: string, mime = 'text/csv;charset=utf-8'): void {
  const blob = new Blob([content], { type: mime })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.style.display = 'none'
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  // 释放 Blob，避免长时间运行下的内存堆积
  window.setTimeout(() => URL.revokeObjectURL(url), 0)
}

/** 文件名安全化：去掉 Windows/macOS 不允许的字符。 */
export function safeFilename(name: string): string {
  return name.replace(/[\\/:*?"<>|]+/g, '_').trim() || 'export'
}
