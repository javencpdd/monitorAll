<script setup lang="ts">
/**
 * 顶栏天气挂件。
 *
 * 五种状态全部显式处置，**任何情况下都不留空白**：
 *   - 关闭（enabled=false 或 reason='disabled'）→ 完全不渲染，不留占位
 *   - 首次拉取中 →「天气…」（带 tooltip 说明正在获取）
 *   - 正常 → `{label 或 当前位置} {temp}{unit} {text}`
 *   - stale → 视觉降灰 + tooltip 标注「数据可能已过期 · 更新于 xx前」
 *   - 失败（ok=false 或请求异常）→「天气不可用」+ tooltip 里的中文原因，**绝不显示空白**
 */
import { computed } from 'vue'
import { useSettingsStore } from '@/stores/settings'
import { formatNumber, formatRelative } from '@/utils/format'

const settings = useSettingsStore()

const snap = computed(() => settings.weatherSnapshot)
const enabled = computed<boolean>(() => settings.weather.enabled)

/**
 * 是否渲染。
 *
 * `reason === 'disabled'` 是「后端说开关其实没开」—— 与本地 enabled 不一致时以后端为准，
 * 此时**整体不渲染**（而不是渲染一个"不可用"），因为用户视角里天气就是没开。
 */
const visible = computed<boolean>(() => {
  if (!enabled.value) return false
  const s = snap.value
  if (s && !s.ok && s.reason === 'disabled') return false
  return true
})

/** 拉取中：既没快照也没错误。 */
const pending = computed<boolean>(() => snap.value === null && settings.weatherError.length === 0)

/** 失败：后端明确 ok=false，或这次请求本身挂了。 */
const failed = computed<boolean>(() => {
  const s = snap.value
  if (s) return !s.ok
  return settings.weatherError.length > 0
})

const stale = computed<boolean>(() => snap.value?.stale === true)

/** 显示名：空则「当前位置」。 */
const label = computed<string>(() => settings.weather.label.trim() || '当前位置')

/** 主文案。失败/拉取中都给明确文字，绝不空字符串。 */
const text = computed<string>(() => {
  if (pending.value) return '天气…'
  const s = snap.value
  if (!s || !s.ok) return '天气不可用'
  const parts: string[] = [label.value]
  if (s.temp !== undefined) parts.push(`${formatNumber(s.temp, 1)}${s.unit ?? ''}`)
  if (s.text) parts.push(s.text)
  return parts.join(' ')
})

const tooltip = computed<string>(() => {
  const s = snap.value
  // 请求失败（网络/超时/非 0 code）：给原因 + 重试入口提示
  if (!s) {
    return settings.weatherError.length > 0
      ? `${settings.weatherError}（可在「设置 · 天气」里点「立即刷新」重试）`
      : '正在获取天气…'
  }
  // 后端明确不可用：原文展示它的中文说明，比自己编一句更可信
  if (!s.ok) {
    const base = s.message || '天气不可用'
    return s.reason ? `${base}（${s.reason}）` : base
  }
  const lines = [
    `${label.value} · ${formatNumber(s.lat, 4)}, ${formatNumber(s.lon, 4)}`,
    `数据来源：${s.provider ?? '-'}`,
  ]
  if (s.wind !== undefined) lines.push(`风：${formatNumber(s.wind, 1)}${s.windUnit ?? ''}`)
  const at = s.fetchedAtMs ? formatRelative(s.fetchedAtMs) : ''
  lines.push(s.stale ? `数据可能已过期 · 更新于 ${at || '未知'}` : `更新于 ${at || '未知'}`)
  return lines.join('\n')
})
</script>

<template>
  <span
    v-if="visible"
    class="ma-weather"
    :class="{ 'is-stale': stale, 'is-failed': failed, 'is-pending': pending }"
    :title="tooltip"
  >
    <span class="ma-weather__icon" aria-hidden="true">{{ failed ? '⚠' : '☁' }}</span>
    <span class="ma-weather__text">{{ text }}</span>
  </span>
</template>

<style scoped>
/*
 * 顶栏空间紧张：挂件必须"可收缩"—— 内容再长也不能把右侧按钮挤出视口。
 * min-width:0 + ellipsis 是这里的底线（顶栏已有 6 个控件 + 本挂件 + 齿轮）。
 */
.ma-weather {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  min-width: 0;
  max-width: 190px;
  padding: 1px 7px;
  border: 1px solid var(--ma-border);
  border-radius: 999px;
  background: var(--ma-bg-base);
  color: var(--ma-text-2);
  font-size: var(--ma-font-xs);
  line-height: 16px;
  white-space: nowrap;
  cursor: help;
}

.ma-weather__icon {
  flex: none;
  color: var(--ma-accent);
  font-size: 10px;
}

.ma-weather__text {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* stale：数据可能已过期 —— 降灰但**保留文字**（降灰不是消失） */
.ma-weather.is-stale {
  opacity: 0.62;
}

.ma-weather.is-stale .ma-weather__icon {
  color: var(--ma-text-3);
}

/* 失败态：明确可见，绝不留空 */
.ma-weather.is-failed {
  border-color: var(--ma-status-error);
  color: var(--ma-status-error);
}

.ma-weather.is-failed .ma-weather__icon {
  color: var(--ma-status-error);
}

/* 拉取中：不刺眼，但要能看出有东西在加载 */
.ma-weather.is-pending {
  opacity: 0.75;
}
</style>
