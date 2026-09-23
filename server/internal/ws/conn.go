package ws

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/logx"
	"github.com/monitorall/monitorall/internal/model"
)

// Conn 为一条 WebSocket 连接：读泵解析上行消息，写泵负责批量发送与心跳，
// 输出队列满时丢弃最旧的一批并计数（慢客户端隔离，不影响其它连接）。
type Conn struct {
	hub *Hub
	ws  *websocket.Conn
	id  string
	log *slog.Logger

	// send 为待发送的批次队列（容量 connQueueDepth）。
	send chan []model.Frame
	// ctrl 为控制类消息队列（status/backpressure/error），优先于帧。
	ctrl chan ServerMsg

	mu           sync.Mutex
	pending      []model.Frame
	droppedByCh  map[string]uint64

	closeOnce sync.Once
	closed    atomic.Bool
	closeErr  atomic.Value

	lastPongAt  atomic.Int64
	missedPong  atomic.Int32
	helloDone   atomic.Bool
}

// newConn 构造一条连接（读/写泵由 Serve 启动）。
func newConn(hub *Hub, ws *websocket.Conn, id string) *Conn {
	depth := hub.cfg.Bus.ConnQueueDepth
	if depth <= 0 {
		depth = config.DefaultConnQueueDepth
	}
	return &Conn{
		hub:         hub,
		ws:          ws,
		id:          id,
		log:         logx.With("module", "ws"),
		send:        make(chan []model.Frame, depth),
		ctrl:        make(chan ServerMsg, depth),
		pending:     make([]model.Frame, 0, hub.cfg.Bus.FrameBatchMax),
		droppedByCh: make(map[string]uint64),
	}
}

// ID 返回连接标识（实现 bus.FrameSink）。
func (c *Conn) ID() string { return c.id }

// OnFrame 接收一帧并入批；永不阻塞（实现 bus.FrameSink）。
func (c *Conn) OnFrame(f model.Frame) {
	if c.closed.Load() {
		return
	}
	c.mu.Lock()
	c.pending = append(c.pending, f)
	needFlush := len(c.pending) >= c.hub.cfg.Bus.FrameBatchMax
	c.mu.Unlock()
	if needFlush {
		c.flush()
	}
}

// Enqueue 与 OnFrame 等价，供 Hub 内部调用。
func (c *Conn) Enqueue(f model.Frame) { c.OnFrame(f) }

// Send 发送一条控制消息（非阻塞）。
func (c *Conn) Send(msg ServerMsg) {
	if c.closed.Load() {
		return
	}
	select {
	case c.ctrl <- msg:
	default:
		// 控制队列满：丢弃最旧一条再写入，保证状态新鲜
		select {
		case <-c.ctrl:
		default:
		}
		select {
		case c.ctrl <- msg:
		default:
		}
	}
}

// flush 把累积的批次推入发送队列；队列满时丢弃最旧批次并计数。
func (c *Conn) flush() {
	c.mu.Lock()
	if len(c.pending) == 0 {
		c.mu.Unlock()
		return
	}
	batch := make([]model.Frame, len(c.pending))
	copy(batch, c.pending)
	c.pending = c.pending[:0]
	c.mu.Unlock()

	select {
	case c.send <- batch:
		return
	default:
	}

	// 队列满：丢弃最旧的一批（保留新鲜度），统计丢帧并通知前端
	var droppedBatch []model.Frame
	select {
	case droppedBatch = <-c.send:
	default:
	}
	select {
	case c.send <- batch:
	default:
		// 极端情况下仍写不进：直接丢弃当前批次
		droppedBatch = append(droppedBatch, batch...)
	}
	c.mu.Lock()
	for _, f := range droppedBatch {
		c.droppedByCh[f.ChannelID]++
	}
	snapshot := make(map[string]uint64, len(c.droppedByCh))
	for k, v := range c.droppedByCh {
		snapshot[k] = v
	}
	c.mu.Unlock()

	windowMs := int64(c.hub.cfg.Bus.BackpressureNotifyMs)
	if windowMs <= 0 {
		windowMs = int64(config.DefaultBackpressureNotifyMs)
	}
	for chID, n := range snapshot {
		c.Send(NewBackpressure(chID, n, windowMs))
	}
}

// Serve 启动读泵与写泵，任一退出即关闭连接并等待另一泵退出（防 goroutine 泄漏）。
func (c *Conn) Serve() {
	done := make(chan struct{})
	go c.writePump(done)
	c.readPump()
	c.Close() // 触发写泵退出
	<-done    // 等待写泵退出，保证 WaitGroup 归零
}

// readPump 解析上行消息：hello / subscribe / unsubscribe / pong / setRate。
func (c *Conn) readPump() {
	defer func() {
		if r := recover(); r != nil {
			c.log.Error("读泵 panic", "connId", c.id, "panic", r)
		}
	}()
	helloTimeout := time.Duration(config.HelloTimeoutMs) * time.Millisecond
	_ = c.ws.SetReadDeadline(time.Now().Add(helloTimeout))
	// 握手完成后的读超时（见 aliveDeadline：心跳周期 × (MaxMissedPong+1)）。
	aliveDeadline := c.aliveDeadline()
	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				c.log.Warn("读消息失败", "connId", c.id, "err", err)
			}
			return
		}
		var msg ClientMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			c.Send(NewError(WSErrBadRequest, "JSON 解析失败", ""))
			continue
		}
		// 已握手：每读到一条上行消息就顺延读超时。pong 是客户端唯一的心跳回应，
		// 若只在 hello 时设一次，连接仍会在固定时长后被掐断（只是循环变慢）。
		if c.helloDone.Load() {
			_ = c.ws.SetReadDeadline(time.Now().Add(aliveDeadline))
		}
		switch msg.Op {
		case OpHello:
			c.handleHello(&msg)
			// hello 之后取消握手超时，改由心跳维持
			_ = c.ws.SetReadDeadline(time.Now().Add(aliveDeadline))
		case OpSubscribe:
			if !c.helloDone.Load() {
				c.Send(NewError(WSErrBadRequest, "请先发送 hello", msg.Ref))
				continue
			}
			results := c.hub.Subscribe(c.id, msg.ChannelIDs)
			c.Send(ServerMsg{Op: OpSubscribed, Ref: msg.Ref, Results: results})
		case OpUnsubscribe:
			c.hub.Unsubscribe(c.id, msg.ChannelIDs)
			c.Send(ServerMsg{Op: OpUnsubscribed, Ref: msg.Ref, ChannelIDs: msg.ChannelIDs})
		case OpPong:
			c.lastPongAt.Store(msg.T)
			c.missedPong.Store(0)
		case OpSetRate:
			// [EXT] P1：MVP 接收但不生效，直接回 ack 避免前端报错
			c.Send(ServerMsg{Op: OpUnsubscribed, Ref: msg.Ref})
		default:
			c.Send(NewError(WSErrBadRequest, "未知 op: "+msg.Op, msg.Ref))
		}
	}
}

// aliveDeadline 返回握手完成后的读超时：心跳周期 × (允许连续丢失 pong 次数 + 1)。
//
// 关键约束：读超时必须【大于】服务端心跳周期。心跳是服务端主动发的，
// 客户端只在收到 ping 后才回 pong；若读超时短于心跳周期，客户端还没等到
// 第一个 ping（也就无从回 pong）就会被读超时判死，表现为连接按固定秒数
// 断开并重连的死循环。
//
// 历史 bug：这里曾用 PongTimeoutMs(3s)×(MaxMissedPong+1)=12s，而
// pingIntervalMs 默认 15s（12s < 15s），导致每条连接精确存活 12s 后断开。
func (c *Conn) aliveDeadline() time.Duration {
	ping := time.Duration(c.hub.cfg.Bus.PingIntervalMs) * time.Millisecond
	if ping <= 0 {
		ping = time.Duration(config.DefaultPingIntervalMs) * time.Millisecond
	}
	return ping * time.Duration(config.MaxMissedPong+1)
}

// handleHello 校验协议版本并回复 welcome。
func (c *Conn) handleHello(msg *ClientMsg) {
	if msg.Protocol != config.WSProtocolVersion {
		c.Send(NewError(WSErrProtocolMismatch,
			"协议版本不匹配，服务端为 "+strconv.Itoa(config.WSProtocolVersion), msg.Ref))
		c.Close()
		return
	}
	c.helloDone.Store(true)
	c.lastPongAt.Store(model.NowMs())
	c.Send(NewWelcome(c.id, model.NowMs(), c.hub.cfg.Bus.PingIntervalMs))
	c.log.Info("连接就绪", "connId", c.id, "clientId", msg.ClientID)
}

// writePump 负责批量写帧、发控制消息、心跳与超时关闭。
func (c *Conn) writePump(done chan struct{}) {
	defer close(done)
	defer func() {
		if r := recover(); r != nil {
			c.log.Error("写泵 panic", "connId", c.id, "panic", r)
		}
	}()

	batchInterval := time.Duration(c.hub.cfg.Bus.FrameBatchMs) * time.Millisecond
	if batchInterval <= 0 {
		batchInterval = time.Duration(config.DefaultFrameBatchMs) * time.Millisecond
	}
	pingInterval := time.Duration(c.hub.cfg.Bus.PingIntervalMs) * time.Millisecond
	if pingInterval <= 0 {
		pingInterval = time.Duration(config.DefaultPingIntervalMs) * time.Millisecond
	}
	writeWait := time.Duration(c.hub.cfg.Bus.WriteWaitMs) * time.Millisecond
	if writeWait <= 0 {
		writeWait = time.Duration(config.DefaultWriteWaitMs) * time.Millisecond
	}

	batchTicker := time.NewTicker(batchInterval)
	defer batchTicker.Stop()
	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	for {
		select {
		case <-batchTicker.C:
			c.flush()
		case batch := <-c.send:
			if !c.writeJSON(NewFrames(batch), writeWait) {
				return
			}
		case msg := <-c.ctrl:
			if !c.writeJSON(msg, writeWait) {
				return
			}
		case <-pingTicker.C:
			if c.missedPong.Load() >= int32(config.MaxMissedPong) {
				c.log.Warn("连续丢失 pong，关闭连接", "connId", c.id)
				return
			}
			c.missedPong.Add(1)
			if !c.writeJSON(NewPing(model.NowMs()), writeWait) {
				return
			}
		}
		if c.closed.Load() {
			return
		}
	}
}

// writeJSON 写一条消息并设置写超时；失败返回 false。
func (c *Conn) writeJSON(msg ServerMsg, writeWait time.Duration) bool {
	if err := c.ws.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return false
	}
	if err := c.ws.WriteJSON(msg); err != nil {
		c.log.Warn("写消息失败", "connId", c.id, "err", err)
		return false
	}
	return true
}

// Close 关闭连接（可重复调用）。
func (c *Conn) Close() {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		_ = c.ws.Close()
	})
}

