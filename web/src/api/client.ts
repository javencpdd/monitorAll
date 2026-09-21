/**
 * REST / JSON over HTTP。
 *
 * 统一响应约定（ARCH §7.1）：`{ code, message, data, traceId? }`，失败时 data 为 null。
 * 约定（TASKS §1.4）：
 *   - 本文件负责解包并把 code≠0 抛成 AppError
 *   - 组件内不 try/catch 业务错误，统一由调用方 → uiStore.pushError
 */
import { API_TIMEOUT_MS } from '@/utils/constants'

export interface ApiResponse<T> {
  code: number
  message: string
  data: T | null
  traceId?: string
}

/** 业务错误：携带后端错误码与 traceId，便于对照 server 日志。 */
export class AppError extends Error {
  readonly code: number
  readonly traceId?: string
  /** 可选：后端给出的修复建议或字段级提示。 */
  readonly hint?: string

  constructor(code: number, message: string, traceId?: string, hint?: string) {
    super(message)
    this.name = 'AppError'
    this.code = code
    if (traceId) this.traceId = traceId
    if (hint) this.hint = hint
  }
}

export interface AppErrorEntry {
  code: number
  message: string
  traceId?: string
  at: number
}

export type HttpMethod = 'GET' | 'POST' | 'PUT' | 'DELETE'

export interface RequestOptions {
  method?: HttpMethod
  body?: unknown
  query?: Record<string, string | number | boolean | undefined>
  timeoutMs?: number
  signal?: AbortSignal
}

function buildUrl(path: string, query?: RequestOptions['query']): string {
  if (!query) return path
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined) continue
    search.set(key, String(value))
  }
  const qs = search.toString()
  return qs.length > 0 ? `${path}?${qs}` : path
}

function extractTraceId(res: Response): string | undefined {
  const id = res.headers.get('X-Request-Id')
  return id && id.length > 0 ? id : undefined
}

/**
 * 统一请求入口。
 * - 超时受 AbortSignal 控制（默认 API_TIMEOUT_MS）
 * - HTTP 非 2xx：尝试解析响应体取 code/message，解析失败则退化为 HTTP 状态码
 * - code≠0（含 2xx 携带错误码）：抛 AppError
 */
export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const controller = new AbortController()
  const timeoutMs = options.timeoutMs ?? API_TIMEOUT_MS
  const timer = window.setTimeout(() => controller.abort(), timeoutMs)
  const onAbort = (): void => controller.abort()
  if (options.signal) {
    if (options.signal.aborted) controller.abort()
    else options.signal.addEventListener('abort', onAbort)
  }

  try {
    const init: RequestInit = {
      method: options.method ?? 'GET',
      credentials: 'same-origin',
      signal: controller.signal,
    }
    if (options.body !== undefined && options.method !== 'GET') {
      init.headers = { 'Content-Type': 'application/json' }
      init.body = JSON.stringify(options.body)
    }
    const res = await fetch(buildUrl(path, options.query), init)
    const traceId = extractTraceId(res)

    let json: ApiResponse<T> | null = null
    try {
      json = (await res.json()) as ApiResponse<T>
    } catch {
      json = null
    }

    if (!json) {
      throw new AppError(res.status, `响应解析失败（HTTP ${res.status}）`, traceId)
    }
    if (json.code !== 0) {
      throw new AppError(json.code, json.message || '请求失败', traceId ?? json.traceId)
    }
    if (!res.ok) {
      throw new AppError(res.status, json.message || `HTTP ${res.status}`, traceId)
    }
    // 后端保证成功时 data 存在；Give-and-take：null 时按 never 处理不可达路径
    return json.data as T
  } catch (err) {
    if (err instanceof AppError) throw err
    if (err instanceof DOMException && err.name === 'AbortError') {
      throw new AppError(-1, `请求超时（${timeoutMs}ms）`)
    }
    throw new AppError(-2, err instanceof Error ? err.message : '网络异常')
  } finally {
    window.clearTimeout(timer)
    if (options.signal) options.signal.removeEventListener('abort', onAbort)
  }
}

/** 下载二进制资源（如自签证书），返回 Blob。 */
export async function download(path: string, timeoutMs = API_TIMEOUT_MS): Promise<Blob> {
  const controller = new AbortController()
  const timer = window.setTimeout(() => controller.abort(), timeoutMs)
  try {
    const res = await fetch(path, { signal: controller.signal, credentials: 'same-origin' })
    if (!res.ok) throw new AppError(res.status, `下载失败（HTTP ${res.status}）`)
    return await res.blob()
  } catch (err) {
    if (err instanceof AppError) throw err
    throw new AppError(-2, err instanceof Error ? err.message : '下载失败')
  } finally {
    window.clearTimeout(timer)
  }
}

/** 把未知异常归一化为可读文案（供图层展示）。 */
export function toMessage(err: unknown): string {
  if (err instanceof AppError) return err.message
  if (err instanceof Error) return err.message
  return String(err)
}
