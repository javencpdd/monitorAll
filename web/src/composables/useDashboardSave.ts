/**
 * 看板保存编排（DB-03 / DB-04）：脏标记、Ctrl+S、离开确认、revision 冲突提示。
 */
import { computed, onBeforeUnmount, onMounted } from 'vue'
import type { ComputedRef } from 'vue'
import { AppError } from '@/api/client'
import { useDashboardStore } from '@/stores/dashboard'
import { useUiStore } from '@/stores/ui'

export interface DashboardSaveHandle {
  dirty: ComputedRef<boolean>
  saving: ComputedRef<boolean>
  save: () => Promise<void>
}

export function useDashboardSave(): DashboardSaveHandle {
  const dashboardStore = useDashboardStore()
  const ui = useUiStore()

  const dirty = computed(() => dashboardStore.dirty)
  const saving = computed(() => dashboardStore.saving)

  async function save(): Promise<void> {
    if (!dashboardStore.current) return
    try {
      await dashboardStore.save()
      ui.success('看板已保存')
    } catch (err) {
      if (err instanceof AppError) {
        ui.error(err.code === 40009 ? '看板已被修改，请刷新后重试' : err.message)
      } else {
        ui.error('保存失败')
      }
    }
  }

  function onBeforeLeave(e: BeforeUnloadEvent): void {
    if (!dirty.value) return
    e.preventDefault()
    // Chrome 需要设置 returnValue 才会弹原生确认框
    e.returnValue = ''
  }

  function onKeyDown(e: KeyboardEvent): void {
    const isSave = (e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's'
    if (!isSave) return
    e.preventDefault()
    void save()
  }

  onMounted(() => {
    window.addEventListener('beforeunload', onBeforeLeave)
    window.addEventListener('keydown', onKeyDown)
  })

  onBeforeUnmount(() => {
    window.removeEventListener('beforeunload', onBeforeLeave)
    window.removeEventListener('keydown', onKeyDown)
  })

  return { dirty, saving, save }
}
