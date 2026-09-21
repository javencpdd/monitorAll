import type { GlobalThemeOverrides } from 'naive-ui'
import { darkTheme } from 'naive-ui'

/**
 * Naive UI 主题适配层。
 * themeOverrides 全部从 CSS 变量取值（variables.css 是唯一颜色来源），
 * 组件与此处都不得硬编码十六进制色值。
 */

/** 主题种类。MVP 默认暗色，亮色为 P1 扩展点但不得写死暗色。 */
export type ThemeKind = 'dark' | 'light'

/** CSS 变量名 → Naive 主题覆盖层键名的公共色映射。 */
const COMMON_COLOR_MAP = {
  primaryColor: '--ma-accent',
  primaryColorHover: '--ma-accent-hover',
  primaryColorSuppl: '--ma-accent-hover',
  errorColor: '--ma-status-error',
  warningColor: '--ma-status-reconnecting',
  successColor: '--ma-status-online',
  infoColor: '--ma-status-connecting',
  textColorBase: '--ma-text-1',
  textColor1: '--ma-text-1',
  textColor2: '--ma-text-2',
  textColor3: '--ma-text-3',
  borderColor: '--ma-border',
  baseColor: '--ma-bg-card',
  bodyColor: '--ma-bg-base',
  cardColor: '--ma-bg-card',
  modalColor: '--ma-bg-elevated',
  popoverColor: '--ma-bg-elevated',
  tableHeaderColor: '--ma-bg-elevated',
  inputColor: '--ma-bg-elevated',
  dividerColor: '--ma-border',
  codeColor: '--ma-bg-elevated',
  borderRadius: '--ma-radius',
} as const

/** 读取 CSS 变量当前值；读取失败时回退到 fallback。 */
export function readCssVar(name: string, fallback: string): string {
  if (typeof window === 'undefined') return fallback
  const value = window.getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  return value.length > 0 ? value : fallback
}

/**
 * 取一个可用于 Canvas 绘制的**具体颜色**。
 * ECharts 走 Canvas 渲染无法解析 `var(--ma-*)`，因此必须在此把 CSS 变量解析成实际色值。
 * 调用方需把 ui.themeKind 作为响应式依赖，才能在换肤后重算。
 */
export function cssColor(name: string, fallback: string): string {
  return readCssVar(name, fallback)
}

/** 按映射表构建 common 段覆盖层。 */
function buildCommon(): Record<string, string> {
  const common: Record<string, string> = {}
  for (const [key, cssVar] of Object.entries(COMMON_COLOR_MAP)) {
    common[key] = readCssVar(cssVar, '')
  }
  return common
}

/** 生成 Naive UI themeOverrides（每次调用重新读取 CSS 变量，支持运行时换肤）。 */
export function buildThemeOverrides(): GlobalThemeOverrides {
  return {
    common: buildCommon(),
    Card: {
      borderRadius: readCssVar('--ma-radius', '6px'),
      borderColor: readCssVar('--ma-border', '#2c2c34'),
      color: readCssVar('--ma-bg-card', '#18181c'),
    },
    Layout: {
      headerColor: readCssVar('--ma-bg-card', '#18181c'),
      siderColor: readCssVar('--ma-bg-card', '#18181c'),
      footerColor: readCssVar('--ma-bg-card', '#18181c'),
    },
  } as GlobalThemeOverrides
}

/** 取得指定主题对象（MVP 仅暗色；保留亮色分支作为 P1 扩展点）。 */
export function resolveNaiveTheme(kind: ThemeKind) {
  return kind === 'light' ? undefined : darkTheme
}

/** 主题种类 → 根节点 class（驱动 CSS 变量切换）。 */
export function themeRootClass(kind: ThemeKind): string {
  return kind === 'light' ? 'ma-theme-light' : 'ma-theme-dark'
}
