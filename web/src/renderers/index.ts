/**
 * 渲染器注册表（ARCH §9.2）。
 *
 * 唯一注册入口 registerRenderer；manifest 自带懒加载 component，
 * P2 插件化时只需新增 manifests 项 + 一个 .vue，不改任何平台代码。
 */
import type { Component } from 'vue'
import { defineAsyncComponent } from 'vue'
import type { PayloadType, RendererManifest } from '@/types'
import { PAYLOAD_TYPE_TEXT } from '@/utils/constants'
import { P0_MANIFESTS } from './manifests'

const registry = new Map<string, RendererManifest>()
const componentCache = new Map<string, Component>()

export interface FilterResult {
  recommended: RendererManifest[]
  compatible: RendererManifest[]
  incompatible: { manifest: RendererManifest; reason: string }[]
}

/** 注册一个渲染器（同 type 覆盖）。 */
export function registerRenderer(m: RendererManifest): void {
  registry.set(m.type, m)
}

/** 取 manifest。 */
export function getManifest(type: string | undefined): RendererManifest | undefined {
  if (!type) return undefined
  return registry.get(type)
}

/** 全部已注册渲染器。 */
export function allRenderers(): RendererManifest[] {
  return Array.from(registry.values())
}

/**
 * CD-07：按 payloadType 把渲染器分成 推荐 / 兼容 / 不兼容 三段。
 * 未识别类型（undefined）时全部归到「兼容」，由用户自行选择。
 */
export function filterByPayloadType(pt: PayloadType | undefined): FilterResult {
  const result: FilterResult = { recommended: [], compatible: [], incompatible: [] }
  for (const manifest of registry.values()) {
    if (!pt) {
      result.compatible.push(manifest)
      continue
    }
    if (manifest.recommended.includes(pt)) {
      result.recommended.push(manifest)
    } else if (manifest.accepts.includes(pt)) {
      result.compatible.push(manifest)
    } else {
      result.incompatible.push({
        manifest,
        reason: `${manifest.displayName} 不接受 ${PAYLOAD_TYPE_TEXT[pt]} 类型数据`,
      })
    }
  }
  return result
}

/**
 * 合并默认配置：新增字段自动补默认值，未知字段保留（兼容旧版本看板数据）。
 * 缺失取 defaultConfig[field.key]，再缺失取 configSchema[field].default。
 */
export function mergeConfig(type: string, saved: Record<string, unknown> | undefined): Record<string, unknown> {
  const manifest = registry.get(type)
  if (!manifest) return { ...(saved ?? {}) }
  const merged: Record<string, unknown> = { ...manifest.defaultConfig }
  for (const field of manifest.configSchema) {
    if (merged[field.key] === undefined) merged[field.key] = field.default
  }
  if (saved) {
    for (const [key, value] of Object.entries(saved)) {
      if (value === undefined) continue
      merged[key] = value
    }
  }
  return merged
}

/** 取渲染器组件（异步包一层并缓存，避免重复创建）。 */
export function resolveComponent(type: string | undefined): Component | undefined {
  const manifest = getManifest(type)
  if (!manifest) return undefined
  const cached = componentCache.get(manifest.type)
  if (cached) return cached
  const comp = defineAsyncComponent({
    loader: manifest.component,
    // 加载失败时由 CardErrorBoundary 兜底，这里不做 UI
    timeout: 10000,
  })
  componentCache.set(manifest.type, comp)
  return comp
}

/** 该渲染器是否需要历史数据（折线/地图）。 */
export function needsHistory(type: string | undefined): boolean {
  return getManifest(type)?.capabilities.needsHistory === true
}

/** 该渲染器是否需要高德 Key。 */
export function requiresAMap(type: string | undefined): boolean {
  return getManifest(type)?.capabilities.requiresAMap === true
}

/** 缓冲容量建议值。 */
export function maxPointsOf(type: string | undefined, fallback: number): number {
  return getManifest(type)?.capabilities.maxPoints ?? fallback
}

// 注册全部 P0 渲染器
for (const manifest of P0_MANIFESTS) {
  registerRenderer(manifest)
}
