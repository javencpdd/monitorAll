// Package ros 实现 ROS1/ROS2 数据源适配器：Go 侧作为 rosbridge WebSocket 客户端
// 订阅话题并把 msg 归一化成统一 Frame。ROS1 与 ROS2 的全部差异只允许出现在 diff.go。
package ros

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/monitorall/monitorall/internal/config"
)

// ————————————————— 底层 WebSocket 抽象（便于 mock 单测） —————————————————

// WSConn 抽象底层 WebSocket 连接能力，便于用 mock 实现单测（不依赖真实 rosbridge）。
type WSConn interface {
	// ReadMessage 读取一条消息（类型、负载、错误）。
	ReadMessage() (int, []byte, error)
	// WriteJSON 写一条 JSON 消息。
	WriteJSON(v any) error
	// Close 关闭连接。
	Close() error
}

// DialFunc 为拨号函数类型。
type DialFunc func(ctx context.Context, url string) (WSConn, error)

// defaultDial 为真实拨号实现（gorilla/websocket）。
func defaultDial(ctx context.Context, url string) (WSConn, error) {
	d := websocket.Dialer{
		HandshakeTimeout: time.Duration(config.DefaultTimeoutMs) * time.Millisecond,
		Proxy:            http.ProxyFromEnvironment,
	}
	c, _, err := d.DialContext(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// dialWS 为可替换的拨号入口（单测中替换为 mock）。
var dialWS DialFunc = defaultDial

// SetDialer 替换拨号实现（仅供单测使用）。
func SetDialer(f DialFunc) {
	if f == nil {
		return
	}
	dialWS = f
}

// ————————————————— rosbridge 报文常量 —————————————————

const (
	// OpSubscribe 为订阅报文 op。
	OpSubscribe = "subscribe"
	// OpUnsubscribe 为取消订阅报文 op。
	OpUnsubscribe = "unsubscribe"
	// OpPublish 为 rosbridge 下发数据的 op。
	OpPublish = "publish"
	// OpCallService 为调用服务的 op。
	OpCallService = "call_service"
	// OpServiceResponse 为服务响应 op。
	OpServiceResponse = "service_response"
	// OpStatus 为 rosbridge 状态/错误 op。
	OpStatus = "status"
)

// ————————————————— 客户端 —————————————————

// SubscribeOptions 为订阅附加参数（throttle_rate / queue_length / compression）。
type SubscribeOptions struct {
	Type         string `json:"-"`          // msgType，由 diff.go 决定是否填写
	ThrottleRate int    `json:"-"`          // ms，= 1000/maxHz
	QueueLength  int    `json:"-"`          // 队列长度，默认 1（丢弃积压）
	FragmentSize int    `json:"-"`          // 分片大小，0 = 不限制
	Compression  string `json:"-"`          // none | jpeg | cbor-raw
}

// MessageHandler 为话题消息处理器。
type MessageHandler func(msg map[string]any)

// Client 为一个 DataSource 到 rosbridge 的 WebSocket 连接（多话题复用同一条连接）。
type Client struct {
	url     string
	version string
	log     *slog.Logger

	mu       sync.Mutex
	conn     WSConn
	handlers map[string]MessageHandler
	pending  map[string]chan map[string]any // service → 响应通道
	closed   bool
	lastErr  string

	writeMu sync.Mutex
	wg      sync.WaitGroup
	done    chan struct{}
}

// NewClient 构造 rosbridge 客户端（尚未建连）。
func NewClient(url, version string, log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	return &Client{
		url:      url,
		version:  version,
		log:      log,
		handlers: make(map[string]MessageHandler),
		pending:  make(map[string]chan map[string]any),
	}
}

// URL 返回 rosbridge 地址。
func (c *Client) URL() string { return c.url }

// IsConnected 返回是否已建连。
func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil && !c.closed
}

// LastError 返回最近一次错误。
func (c *Client) LastError() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr
}

// Connect 建立 WebSocket 连接并启动读循环；幂等（已连接则直接返回）。
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("rosbridge 客户端已关闭")
	}
	if c.conn != nil {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	conn, err := dialWS(ctx, c.url)
	if err != nil {
		c.mu.Lock()
		c.lastErr = err.Error()
		c.mu.Unlock()
		return err
	}
	c.mu.Lock()
	c.conn = conn
	c.done = make(chan struct{})
	c.mu.Unlock()

	c.wg.Add(1)
	go c.readLoop()
	c.log.Info("rosbridge 已连接", "url", c.url, "version", c.version)
	return nil
}

// readLoop 读取并分发 rosbridge 报文。
func (c *Client) readLoop() {
	defer c.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			c.log.Error("rosbridge 读循环 panic", "panic", r)
		}
	}()
	for {
		c.mu.Lock()
		conn := c.conn
		done := c.done
		c.mu.Unlock()
		if conn == nil {
			return
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			c.mu.Lock()
			c.lastErr = err.Error()
			c.closed = true
			c.mu.Unlock()
			if done != nil {
				close(done)
			}
			c.log.Warn("rosbridge 连接断开", "url", c.url, "err", err)
			return
		}
		c.dispatch(data)
	}
}

// dispatch 分发一条 rosbridge 报文。
func (c *Client) dispatch(data []byte) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		c.log.Debug("忽略非 JSON 报文", "err", err)
		return
	}
	op, _ := raw["op"].(string)
	switch op {
	case OpPublish:
		topic, _ := raw["topic"].(string)
		msg, _ := raw["msg"].(map[string]any)
		if topic == "" || msg == nil {
			return
		}
		c.mu.Lock()
		h := c.handlers[topic]
		c.mu.Unlock()
		if h != nil {
			h(msg)
		}
	case OpServiceResponse:
		service, _ := raw["service"].(string)
		values, _ := raw["values"].(map[string]any)
		c.mu.Lock()
		ch := c.pending[service]
		if ch != nil {
			delete(c.pending, service)
		}
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- values:
			default:
			}
			close(ch)
		}
	case OpStatus:
		level, _ := raw["level"].(string)
		msg, _ := raw["msg"].(string)
		if level == "error" {
			c.mu.Lock()
			c.lastErr = msg
			c.mu.Unlock()
			c.log.Warn("rosbridge 返回错误状态", "msg", msg)
		}
	default:
		// 其它 op（如 set_level / 心跳）忽略
	}
}

// OnMessage 注册话题处理器（重复注册覆盖）。
func (c *Client) OnMessage(topic string, h MessageHandler) {
	c.mu.Lock()
	c.handlers[topic] = h
	c.mu.Unlock()
}

// ClearMessage 移除话题处理器。
func (c *Client) ClearMessage(topic string) {
	c.mu.Lock()
	delete(c.handlers, topic)
	c.mu.Unlock()
}

// Subscribe 订阅话题；报文构造走 diff.go（ROS1 type 必填 / ROS2 可选）。
func (c *Client) Subscribe(topic string, opts SubscribeOptions) error {
	msg := BuildSubscribeMessage(c.version, topic, opts.Type, opts)
	return c.writeJSON(msg)
}

// Unsubscribe 取消订阅。
func (c *Client) Unsubscribe(topic string) error {
	msg := map[string]any{"op": OpUnsubscribe, "topic": topic}
	return c.writeJSON(msg)
}

// CallService 调用 rosbridge 的 rosapi 服务；失败返回 error（调用方按软失败处理）。
func (c *Client) CallService(ctx context.Context, service string, args any) (map[string]any, error) {
	msg := BuildServiceCall(c.version, service, args)
	ch := make(chan map[string]any, 1)
	c.mu.Lock()
	c.pending[service] = ch
	c.mu.Unlock()

	if err := c.writeJSON(msg); err != nil {
		c.mu.Lock()
		delete(c.pending, service)
		c.mu.Unlock()
		return nil, err
	}
	select {
	case values := <-ch:
		return values, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, service)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

// writeJSON 线程安全地写一条报文。
func (c *Client) writeJSON(v any) error {
	c.mu.Lock()
	conn := c.conn
	closed := c.closed
	c.mu.Unlock()
	if conn == nil || closed {
		return errors.New("rosbridge 未连接")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.WriteJSON(v)
}

// Close 关闭连接并等待读循环退出；可重复调用。
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed && c.conn == nil {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()

	var err error
	if conn != nil {
		err = conn.Close()
	}
	c.wg.Wait()
	return err
}
