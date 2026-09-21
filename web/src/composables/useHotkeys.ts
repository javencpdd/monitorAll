/**
 * 全局快捷键（CD-08）：Ctrl+S 保存、Esc 关闭抽屉/退出编辑、E 切换编辑模式。
 * 输入框获得焦点时自动让行，避免与表单输入冲突。
 */
import { onBeforeUnmount, onMounted } from 'vue'
import type { Ref } from 'vue'

export interface HotkeyOptions {
  onSave?: () => void
  onEscape?: () => void
  onToggleEdit?: () => void
  /** 额外禁用条件（如抽屉打开中）。 */
  disabled?: Ref<boolean>
}

function isEditableTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false
  const tag = target.tagName
  return (
    tag === 'INPUT' ||
    tag === 'TEXTAREA' ||
    tag === 'SELECT' ||
    target.isContentEditable ||
    target.getAttribute('role') === 'textbox'
  )
}

export function useHotkeys(options: HotkeyOptions): void {
  function onKeyDown(e: KeyboardEvent): void {
    if (options.disabled?.value) return
    if (isEditableTarget(e.target)) return
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
      e.preventDefault()
      options.onSave?.()
      return
    }
    if (e.key === 'Escape') {
      options.onEscape?.()
      return
    }
    if (!e.ctrlKey && !e.metaKey && !e.altKey && e.key.toLowerCase() === 'e') {
      options.onToggleEdit?.()
    }
  }

  onMounted(() => window.addEventListener('keydown', onKeyDown))
  onBeforeUnmount(() => window.removeEventListener('keydown', onKeyDown))
}
