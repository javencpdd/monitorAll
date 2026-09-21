// Package ws 实现数据面 WebSocket：单连接多路复用、订阅引用计数、批量推帧、
// 心跳与背压丢批。消息结构与架构文档 §6 逐字一致。
package ws

import (
	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/model"
)

// ————————————————— op 名称常量（禁止散落裸字符串） —————————————————

// 客户端 → 服务端。
const (
	OpHello       = "hello"
	OpSubscribe   = "subscribe"
	OpUnsubscribe = "unsubscribe"
	OpPong        = "pong"
	OpSetRate     = "setRate" // [EXT] P1：MVP 接收但不生效
)

// 服务端 → 客户端。
const (
	OpWelcome        = "welcome"
	OpSubscribed     = "subscribed"
	OpUnsubscribed   = "unsubscribed"
	OpFrames         = "frames"
	OpChannelStatus  = "channelStatus"
	OpSourceStatus   = "sourceStatus"
	OpBackpressure   = "backpressure"
	OpPing           = "ping"
	OpError          = "error"
)

// WS 错误码字符串（op=error 的 code 字段）。
const (
	WSErrProtocolMismatch = "WS_PROTOCOL_MISMATCH"
	WSErrChannelNotFound  = "CHANNEL_NOT_FOUND"
	WSErrBadRequest       = "BAD_REQUEST"
	WSErrInternal         = "INTERNAL"
	WSErrHelloTimeout     = "WS_HELLO_TIMEOUT"
	WSErrAdapter          = "ADAPTER_ERROR"
)

// ClientMsg 为客户端上行消息（联合结构，按 op 解析）。
type ClientMsg struct {
	Op         string   `json:"op"`
	Protocol   int      `json:"protocol,omitempty"`
	ClientID   string   `json:"clientId,omitempty"`
	ChannelIDs []string `json:"channelIds,omitempty"`
	Ref        string   `json:"ref,omitempty"`
	T          int64    `json:"t,omitempty"`
	ChannelID  string   `json:"channelId,omitempty"`
	Hz         float64  `json:"hz,omitempty"`
}

// SubscribeResult 为 subscribe 的逐通道结果。
type SubscribeResult struct {
	ChannelID string       `json:"channelId"`
	OK        bool         `json:"ok"`
	Status    model.Status `json:"status"`
	Error     string       `json:"error,omitempty"`
}

// ServerMsg 为服务端下行消息（联合结构，未使用字段因 omitempty 不出现）。
type ServerMsg struct {
	Op                 string             `json:"op"`
	ConnID             string             `json:"connId,omitempty"`
	Protocol           int                `json:"protocol,omitempty"`
	ServerTimeMs       int64              `json:"serverTimeMs,omitempty"`
	HeartbeatIntervalMs int               `json:"heartbeatIntervalMs,omitempty"`
	Ref                string             `json:"ref,omitempty"`
	Results            []SubscribeResult  `json:"results,omitempty"`
	ChannelIDs         []string           `json:"channelIds,omitempty"`
	Items              []model.Frame      `json:"items,omitempty"`
	Ts                 int64              `json:"ts,omitempty"`
	ChannelID          string             `json:"channelId,omitempty"`
	Status             model.Status       `json:"status,omitempty"`
	LastError          string             `json:"lastError,omitempty"`
	LastFrameAt        int64              `json:"lastFrameAt,omitempty"`
	LatencyMs          int64              `json:"latencyMs,omitempty"`
	DataSourceID       string             `json:"dataSourceId,omitempty"`
	ReconnectInMs      int64              `json:"reconnectInMs,omitempty"`
	NextRetryAt        int64              `json:"nextRetryAt,omitempty"`
	Dropped            uint64             `json:"dropped,omitempty"`
	WindowMs           int64              `json:"windowMs,omitempty"`
	T                  int64              `json:"t,omitempty"`
	Code               string             `json:"code,omitempty"`
	Message            string             `json:"message,omitempty"`
}

// NewWelcome 构造 welcome 消息。
func NewWelcome(connID string, serverTimeMs int64, heartbeatMs int) ServerMsg {
	if heartbeatMs <= 0 {
		heartbeatMs = config.DefaultPingIntervalMs
	}
	return ServerMsg{
		Op:                  OpWelcome,
		ConnID:              connID,
		Protocol:            config.WSProtocolVersion,
		ServerTimeMs:        serverTimeMs,
		HeartbeatIntervalMs: heartbeatMs,
	}
}

// NewFrames 构造批量帧消息。
func NewFrames(items []model.Frame) ServerMsg {
	if items == nil {
		items = []model.Frame{}
	}
	return ServerMsg{Op: OpFrames, Items: items, Ts: model.NowMs()}
}

// NewChannelStatus 构造通道状态消息。
func NewChannelStatus(channelID string, status model.Status, lastError string, lastFrameAt, latencyMs int64) ServerMsg {
	return ServerMsg{
		Op:          OpChannelStatus,
		ChannelID:   channelID,
		Status:      status,
		LastError:   lastError,
		LastFrameAt: lastFrameAt,
		LatencyMs:   latencyMs,
	}
}

// NewSourceStatus 构造数据源状态消息（含重连倒计时）。
func NewSourceStatus(dataSourceID string, status model.Status, lastError string, reconnectInMs, nextRetryAt int64) ServerMsg {
	return ServerMsg{
		Op:            OpSourceStatus,
		DataSourceID:  dataSourceID,
		Status:        status,
		LastError:     lastError,
		ReconnectInMs: reconnectInMs,
		NextRetryAt:   nextRetryAt,
	}
}

// NewBackpressure 构造丢帧通知消息。
func NewBackpressure(channelID string, dropped uint64, windowMs int64) ServerMsg {
	return ServerMsg{Op: OpBackpressure, ChannelID: channelID, Dropped: dropped, WindowMs: windowMs}
}

// NewPing 构造心跳消息。
func NewPing(t int64) ServerMsg { return ServerMsg{Op: OpPing, T: t} }

// NewError 构造错误消息。
func NewError(code, message, ref string) ServerMsg {
	return ServerMsg{Op: OpError, Code: code, Message: message, Ref: ref}
}

// ErrorCodeOf 把业务错误码映射为 WS 错误字符串。
func ErrorCodeOf(code apperr.Code) string {
	switch code {
	case apperr.NotFound:
		return WSErrChannelNotFound
	case apperr.ChannelUnavailable:
		return WSErrChannelNotFound
	case apperr.BadRequest, apperr.InvalidParam, apperr.InvalidURL, apperr.UnsupportedType:
		return WSErrBadRequest
	case apperr.AdapterError, apperr.SourceConnectFailed, apperr.MediaMTXUnreachable:
		return WSErrAdapter
	default:
		return WSErrInternal
	}
}
