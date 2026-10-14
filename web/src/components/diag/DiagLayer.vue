<script setup lang="ts">
/**
 * 诊断层（v3，H6 收敛）—— `utils/diag.ts` 的**唯一读取出口**。
 *
 * 取代 `DashboardCanvas.vue` 里那段「临时诊断角标 + probeDom 三连 setTimeout」：
 *   - 旧实现每次组件更新都查 DOM（`onUpdated(probeDom)`），是真实性能负债，且是临时代码；
 *   - 本层只在**被打开时**按 DIAG_REFRESH_MS 主动拉 snapshot，与业务渲染完全解耦。
 *
 * 开关（默认 off → 生产零 DOM、零开销）：
 *   - URL：`?diag=1`（minimal）/ `?diag=2`（verbose）
 *   - 快捷键：Ctrl/Cmd + Shift + D 循环 off → minimal → verbose → off（写 localStorage 持久化）
 *
 * ⚠️ 类名一律 `ma-diag__*`。历史那套画布临时角标类名（`ma` + `-canvas` + `__dbg`）**不得**再出现 ——
 *    它在构建产物里出现即视为回归（见验收 grep）。
 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { DIAG_REFRESH_MS } from '@/utils/constants'
import {
  diagLevel,
  registerSource,
  setDiagLevel,
  snapshot,
  type DiagLevel,
  type DiagSnapshot,
  type DiagValue,
} from '@/utils/diag'
import { willChangeGrantedCount } from '@/composables/useWillChange'

const props = withDefaults(defineProps<{ title?: string }>(), { title: '看板' })

const level = ref<DiagLevel>(diagLevel())
const snap = ref<DiagSnapshot | null>(null)

const rows = computed<Array<[string, DiagValue]>>(() => {
  const values = snap.value?.values
  if (!values) return []
  return Object.entries(values).sort((a, b) => a[0].localeCompare(b[0]))
})

const summary = computed<string>(() => {
  const values = snap.value?.values
  if (!values) return '（采集…）'
  const pick = ['mapMode', 'mapStatus', 'projErr', 'projErrors', 'syncFrameMs.ema', 'syncHz', 'layer.granted']
  const parts: string[] = []
  for (const key of pick) {
    const v = values[key]
    if (v !== undefined) parts.push(`${key}=${v}`)
  }
  return parts.join(' · ') || `${rows.value.length} 项`
})

let timer = 0
function pull(): void {
  snap.value = snapshot()
}

function start(): void {
  window.clearInterval(timer)
  if (level.value === 'off') {
    snap.value = null
    return
  }
  pull()
  timer = window.setInterval(pull, DIAG_REFRESH_MS)
}

function cycle(): void {
  const next: DiagLevel = level.value === 'off' ? 'minimal' : level.value === 'minimal' ? 'verbose' : 'off'
  level.value = next
  setDiagLevel(next)
  start()
}

function onKey(e: KeyboardEvent): void {
  if ((e.ctrlKey || e.metaKey) && e.shiftKey && (e.key === 'D' || e.key === 'd')) {
    e.preventDefault()
    cycle()
  }
}

/**
 * 合成层额度用量（MAP_WILL_CHANGE_MAX 落地自证）：
 * 打开 HUD 看到 `layer.granted ≤ 8` 就说明 will-change 封顶生效。
 */
let unregisterSource: (() => void) | null = null

onMounted(() => {
  unregisterSource = registerSource('layer', () => ({ granted: willChangeGrantedCount() }))
  window.addEventListener('keydown', onKey)
  start()
})

onBeforeUnmount(() => {
  window.clearInterval(timer)
  window.removeEventListener('keydown', onKey)
  unregisterSource?.()
  unregisterSource = null
})
</script>

<template>
  <div v-if="level !== 'off'" class="ma-diag" :class="`ma-diag--${level}`">
    <div class="ma-diag__head">
      <span class="ma-diag__title">诊断 · {{ props.title }} · {{ level }}</span>
      <button class="ma-diag__btn" type="button" @click="cycle">切换</button>
    </div>
    <template v-if="level === 'minimal'">
      <div class="ma-diag__line">{{ summary }}</div>
    </template>
    <template v-else>
      <table class="ma-diag__table">
        <tbody>
          <tr v-for="row in rows" :key="row[0]">
            <td class="ma-diag__key">{{ row[0] }}</td>
            <td class="ma-diag__val">{{ row[1] }}</td>
          </tr>
        </tbody>
      </table>
    </template>
  </div>
</template>

<style scoped>
.ma-diag {
  position: absolute;
  top: 6px;
  right: 10px;
  z-index: 200;
  max-width: 46%;
  max-height: 60%;
  overflow: auto;
  padding: 6px 8px;
  border: 1px solid var(--ma-border-strong, var(--ma-border));
  border-radius: var(--ma-radius);
  background: color-mix(in srgb, var(--ma-bg-base) 88%, transparent);
  color: var(--ma-text-2);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 11px;
  line-height: 1.5;
  pointer-events: auto;
}

.ma-diag__head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 4px;
}

.ma-diag__title {
  flex: 1 1 auto;
  color: var(--ma-text-1);
  white-space: nowrap;
}

.ma-diag__btn {
  flex: none;
  padding: 0 6px;
  border: 1px solid var(--ma-border);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-elevated);
  color: var(--ma-text-3);
  cursor: pointer;
  font-size: 10px;
}

.ma-diag__line {
  white-space: normal;
  word-break: break-all;
}

.ma-diag__table {
  border-collapse: collapse;
}

.ma-diag__key {
  padding-right: 10px;
  color: var(--ma-text-3);
  white-space: nowrap;
}

.ma-diag__val {
  color: var(--ma-text-1);
  white-space: nowrap;
}
</style>
