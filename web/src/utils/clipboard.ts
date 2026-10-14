/**
 * 剪贴板写入（含非安全上下文兜底）。
 *
 * ⚠️ 为什么不能只用 `navigator.clipboard.writeText`：
 *   该 API 要求**安全上下文**（HTTPS 或 localhost）。本项目默认就是
 *   `http://<局域网IP>:8080` 这种非安全上下文 —— 在那里 `navigator.clipboard`
 *   直接是 `undefined`，调用会抛 TypeError。如果只写这一条路径，
 *   「复制」按钮会**静默失效**（没有任何报错，用户以为点了没反应）。
 *
 * 兜底用经典的 `textarea + execCommand('copy')`：虽已废弃，但在非安全上下文里
 * 仍是唯一可用的同步复制手段，且主流浏览器目前都还支持。
 */
export async function copyText(text: string): Promise<boolean> {
  // ① 首选：异步剪贴板 API（HTTPS / localhost）
  try {
    if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // 权限被拒 / 非聚焦文档 → 落到下面的兜底，而不是直接失败
  }

  // ② 兜底：非安全上下文
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    // 必须留在文档流内且可见性可控：display:none 的元素无法被 select()
    ta.setAttribute('readonly', '')
    ta.style.position = 'fixed'
    ta.style.top = '0'
    ta.style.left = '0'
    ta.style.width = '1px'
    ta.style.height = '1px'
    ta.style.padding = '0'
    ta.style.border = 'none'
    ta.style.outline = 'none'
    ta.style.boxShadow = 'none'
    ta.style.background = 'transparent'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.focus()
    ta.select()
    ta.setSelectionRange(0, text.length)
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    return ok
  } catch {
    return false
  }
}

/** 把经纬度格式化成固定 6 位小数（≈0.1m 精度，够用且便于粘贴）。 */
export function formatLngLat(lon: number, lat: number): string {
  return `${lon.toFixed(6)}, ${lat.toFixed(6)}`
}
