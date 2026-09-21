/**
 * JSONPath 求值 + 字段树枚举。
 * 供 CD-09 字段点选器（FieldPicker）与渲染器的字段映射使用（ARCH §9.1-FieldPicker / T-23）。
 */
import type { FieldHint } from '@/types'
import { inferType } from './format'

/** 候选字段树节点。 */
export interface FieldNode {
  /** 完整 JSONPath（点分，数组用 [i]）。 */
  path: string
  /** 末级键名。 */
  key: string
  type: 'number' | 'string' | 'boolean' | 'object' | 'array' | 'null'
  /** 叶子节点的示例值（用于预览）。 */
  sample?: unknown
  unit?: string
  children?: FieldNode[]
}

/** 字段来源：最近的样例帧。 */
export interface FieldSource {
  payload?: unknown
  schemaHint?: Record<string, FieldHint>
}

/** 把 "a.b[0].c" 解析为 ['a','b','0','c']。 */
export function parsePathParts(path: string): string[] {
  return path
    .replace(/\[(\d+)\]/g, '.$1')
    .split('.')
    .map((p) => p.trim())
    .filter((p) => p.length > 0)
}

/** 按 JSONPath 取值；路径不存在返回 undefined。 */
export function getByPath(root: unknown, path: string): unknown {
  if (!path) return root
  let cur: unknown = root
  for (const part of parsePathParts(path)) {
    if (cur === null || cur === undefined) return undefined
    if (Array.isArray(cur)) {
      const idx = Number(part)
      if (!Number.isInteger(idx)) return undefined
      cur = cur[idx]
    } else if (typeof cur === 'object') {
      cur = (cur as Record<string, unknown>)[part]
    } else {
      return undefined
    }
  }
  return cur
}

/** 按路径取数值；非数值返回 NaN。 */
export function getNumberByPath(root: unknown, path: string): number {
  const v = getByPath(root, path)
  if (typeof v === 'number') return v
  if (typeof v === 'boolean') return v ? 1 : 0
  if (typeof v === 'string') {
    const n = Number(v)
    return Number.isFinite(n) ? n : Number.NaN
  }
  return Number.NaN
}

/** 是否已配置有效路径（区分 0 / '' 这类假值）。 */
export function hasPath(path: string | undefined): path is string {
  return typeof path === 'string' && path.length > 0
}

/**
 * 从 payload 拍平成一行行 {path, value}，只到 maxDepth 层。
 * 数组只取前 maxArrayItems 个元素作为代表。
 */
export function flatten(
  value: unknown,
  opts: { prefix?: string; maxDepth?: number; maxArrayItems?: number } = {},
): { path: string; value: unknown; type: FieldNode['type'] }[] {
  const prefix = opts.prefix ?? ''
  const maxDepth = opts.maxDepth ?? 12
  const maxArrayItems = opts.maxArrayItems ?? 3
  const out: { path: string; value: unknown; type: FieldNode['type'] }[] = []

  const walk = (cur: unknown, path: string, depth: number): void => {
    const type = inferType(cur)
    if (type !== 'object' && type !== 'array') {
      out.push({ path, value: cur, type })
      return
    }
    if (depth >= maxDepth) {
      out.push({ path, value: cur, type })
      return
    }
    if (Array.isArray(cur)) {
      const take = Math.min(cur.length, maxArrayItems)
      if (take === 0) {
        out.push({ path, value: cur, type: 'array' })
        return
      }
      for (let i = 0; i < take; i += 1) {
        walk(cur[i], `${path}[${i}]`, depth + 1)
      }
      return
    }
    const obj = cur as Record<string, unknown>
    const keys = Object.keys(obj)
    if (keys.length === 0) {
      out.push({ path, value: cur, type })
      return
    }
    for (const key of keys) {
      const childPath = path ? `${path}.${key}` : key
      walk(obj[key], childPath, depth + 1)
    }
  }

  walk(value, prefix, 0)
  return out
}

/**
 * 生成字段候选树。
 * - 有 schemaHint 时优先用 hint（后端已给出类型与单位，权威来源）
 * - hint 缺失的路径从 payload 兜底（ROS 未知消息由后端反射生成 hint，此处再兜一层）
 */
export function buildFieldTree(source: FieldSource, opts: { maxDepth?: number } = {}): FieldNode[] {
  const maxDepth = opts.maxDepth ?? 8
  const hintPaths = source.schemaHint ? Object.keys(source.schemaHint) : []
  if (hintPaths.length === 0) {
    return buildFromPayload(source.payload, maxDepth)
  }
  const roots: FieldNode[] = []
  for (const path of hintPaths.sort()) {
    const hint = source.schemaHint?.[path]
    const value = source.payload === undefined ? undefined : getByPath(source.payload, path)
    insertNode(roots, parsePathParts(path), {
      type: hint?.type ?? inferType(value),
      sample: value,
      unit: hint?.unit,
    })
  }
  // hint 未覆盖的深层字段，从 payload 补齐（仅补标量叶子，避免树爆炸）
  const covered = new Set(hintPaths)
  for (const flat of flatten(source.payload, { maxDepth })) {
    if (covered.has(flat.path)) continue
    if (flat.type === 'object' || flat.type === 'array') continue
    insertNode(roots, parsePathParts(flat.path), { type: flat.type, sample: flat.value })
  }
  return roots
}

/** 兜底：纯 payload 生成树。 */
export function buildFromPayload(payload: unknown, maxDepth = 8): FieldNode[] {
  const roots: FieldNode[] = []
  for (const flat of flatten(payload, { maxDepth })) {
    insertNode(roots, parsePathParts(flat.path), { type: flat.type, sample: flat.value })
  }
  return roots
}

/** 把一个 path 插入树中（自动补中间层）。 */
function insertNode(
  roots: FieldNode[],
  parts: string[],
  leaf: { type: FieldNode['type']; sample?: unknown; unit?: string },
): void {
  if (parts.length === 0) return
  const key = parts[0] as string
  const isLeaf = parts.length === 1
  let node = roots.find((n) => n.key === key)
  if (!node) {
    node = {
      path: key,
      key,
      type: isLeaf ? leaf.type : 'object',
      ...(isLeaf && leaf.sample !== undefined ? { sample: leaf.sample } : {}),
      ...(isLeaf && leaf.unit ? { unit: leaf.unit } : {}),
    }
    roots.push(node)
  }
  if (isLeaf) {
    node.type = leaf.type
    if (leaf.sample !== undefined) node.sample = leaf.sample
    if (leaf.unit) node.unit = leaf.unit
    return
  }
  if (!node.children) node.children = []
  const childPath = parts.slice(1)
  insertNode(node.children, childPath, leaf)
  const child = node.children.find((n) => n.key === (childPath[0] as string))
  if (child) {
    child.path = `${node.path}.${childPath[0]}`
  }
}
