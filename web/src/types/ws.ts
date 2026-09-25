/**
 * WebSocket 消息契约（契约来源：docs/DEVELOPMENT.md §7）。
 *
 * 🚨 op 名称、字段名必须与后端 `server/internal/ws/protocol.go` 逐字一致。
 * 端点：ws(s)://{host}:{port}/api/v1/ws?protocol=1
 */
import type { Frame, Status } from './index'

/** 客户端 → 服务端。 */
export type ClientMessage =
  | { op: 'hello'; protocol: number; clientId?: string }
  | { op: 'subscribe'; channelIds: string[]; ref: string }
  | { op: 'unsubscribe'; channelIds: string[]; ref: string }
  | { op: 'pong'; t: number }
  // [EXT] P1，MVP 前端不发该消息，保留类型以便服务端兼容
  | { op: 'setRate'; channelId: string; hz: number; ref: string }

/** 服务端 → 客户端。 */
export type ServerMessage =
  | {
      op: 'welcome'
      connId: string
      protocol: number
      serverTimeMs: number
      heartbeatIntervalMs: number
      pingIntervalMs?: number
    }
  | {
      op: 'subscribed'
      ref: string
      results: { channelId: string; ok: boolean; status: Status; error?: string }[]
    }
  | { op: 'unsubscribed'; ref: string; channelIds: string[] }
  /** ts = 服务端发送时刻；单条推送也用 frames（items 长度 1）。 */
  | { op: 'frames'; items: Frame[]; ts: number }
  | {
      op: 'channelStatus'
      channelId: string
      status: Status
      lastError?: string
      lastFrameAt?: number
      latencyMs?: number
    }
  | {
      op: 'sourceStatus'
      dataSourceId: string
      status: Status
      lastError?: string
      /** 驱动 UI 的「重连中（3s）」倒计时。 */
      reconnectInMs?: number
      nextRetryAt?: number
    }
  /** 服务端因该连接积压而丢批，节流至 1 条/秒/通道。 */
  | { op: 'backpressure'; channelId: string; dropped: number; windowMs: number }
  | { op: 'ping'; t: number }
  | { op: 'error'; code: string; message: string; ref?: string }

export type ServerOp = ServerMessage['op']
export type ClientOp = ClientMessage['op']
