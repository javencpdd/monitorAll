package ws

import (
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
	"github.com/monitorall/monitorall/internal/adapter"
	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/bus"
	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/logx"
	"github.com/monitorall/monitorall/internal/model"
	"github.com/monitorall/monitorall/internal/store"
)

// upgrader 为 WS 升级器（校验 origin 放宽以适配局域网多地址访问）。
var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// Hub 管理全部 WS 连接与其订阅关系，是 bus.Sink 的实现者。
type Hub struct {
	cfg   *config.Config
	bus   *bus.Bus
	adm   *adapter.Manager
	store *store.SQLite
	log   *slog.Logger

	mu           sync.RWMutex
	conns        map[string]*Conn
	channelConns map[string]map[string]struct{} // channelID → connID 集合
	connChannels map[string]map[string]struct{} // connID → channelID 集合
	channelSrc   map[string]string              // channelID → dataSourceID

	seq atomic.Uint64
}

// NewHub 构造 Hub。
func NewHub(b *bus.Bus, adm *adapter.Manager, st *store.SQLite, cfg *config.Config) *Hub {
	return &Hub{
		cfg:          cfg,
		bus:          b,
		adm:          adm,
		store:        st,
		log:          logx.With("module", "ws"),
		conns:        make(map[string]*Conn),
		channelConns: make(map[string]map[string]struct{}),
		connChannels: make(map[string]map[string]struct{}),
		channelSrc:   make(map[string]string),
	}
}

// HandleWS 处理 WebSocket 握手与连接生命周期。
func (h *Hub) HandleWS(w http.ResponseWriter, r *http.Request) {
	proto := r.URL.Query().Get("protocol")
	if proto != "" {
		if n, err := strconv.Atoi(proto); err == nil && n != config.WSProtocolVersion {
			// 版本不匹配：升级后发 error 再关闭
			wsConn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			c := newConn(h, wsConn, "c_rejected")
			c.Send(NewError(WSErrProtocolMismatch,
				"协议版本不匹配，服务端为 "+strconv.Itoa(config.WSProtocolVersion), ""))
			c.Close()
			return
		}
	}
	wsConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.log.Warn("WS 升级失败", "err", err)
		return
	}
	id := "c_" + strconv.FormatUint(h.seq.Add(1), 36)
	c := newConn(h, wsConn, id)
	h.register(c)
	c.Serve()
	h.unregister(id)
}

// register 登记连接并注册为总线消费者。
func (h *Hub) register(c *Conn) {
	h.mu.Lock()
	h.conns[c.id] = c
	h.connChannels[c.id] = make(map[string]struct{})
	h.mu.Unlock()
	h.bus.RegisterSink(c)
	h.log.Info("WS 连接建立", "connId", c.id)
}

// unregister 清理连接：释放该连接持有的全部订阅（引用计数 -1）。
func (h *Hub) unregister(connID string) {
	h.mu.Lock()
	chIDs := make([]string, 0)
	if set, ok := h.connChannels[connID]; ok {
		for chID := range set {
			chIDs = append(chIDs, chID)
		}
	}
	delete(h.conns, connID)
	delete(h.connChannels, connID)
	h.mu.Unlock()

	h.bus.UnregisterSink(connID)
	h.Unsubscribe(connID, chIDs)
	h.log.Info("WS 连接关闭，订阅已释放", "connId", connID, "channels", len(chIDs))
}

// Subscribe 以连接为单位订阅通道（Set 语义，幂等），成功时触发适配器引用计数 +1。
func (h *Hub) Subscribe(connID string, channelIDs []string) []SubscribeResult {
	results := make([]SubscribeResult, 0, len(channelIDs))
	for _, chID := range channelIDs {
		ch, ds, err := h.resolve(chID)
		if err != nil {
			ae := apperr.FromError(err)
			results = append(results, SubscribeResult{ChannelID: chID, OK: false, Error: ae.Message})
			continue
		}
		// 先确保总线 Broker 存在（否则适配器首帧会被丢弃），再做适配器引用计数 + 首订阅建连
		h.bus.EnsureChannel(chID, ch.PayloadType, ch.RateLimitHz)
		if err := h.adm.Subscribe(ds, ch); err != nil {
			ae := apperr.FromError(err)
			results = append(results, SubscribeResult{
				ChannelID: chID, OK: false, Status: model.StatusError, Error: ae.Message,
			})
			continue
		}
		h.bus.Subscribe(chID, connID)

		h.mu.Lock()
		if _, ok := h.channelConns[chID]; !ok {
			h.channelConns[chID] = make(map[string]struct{})
		}
		h.channelConns[chID][connID] = struct{}{}
		if _, ok := h.connChannels[connID]; !ok {
			h.connChannels[connID] = make(map[string]struct{})
		}
		h.connChannels[connID][chID] = struct{}{}
		h.channelSrc[chID] = ds.ID
		h.mu.Unlock()

		st := h.adm.ChannelStatus(chID)
		results = append(results, SubscribeResult{ChannelID: chID, OK: true, Status: st})

		// 补发最近一帧，避免卡片长时间空白
		if f, ok := h.bus.LatestFrame(chID); ok {
			h.deliverToConn(connID, *f)
		}
	}
	return results
}

// Unsubscribe 取消订阅并释放适配器引用计数。
func (h *Hub) Unsubscribe(connID string, channelIDs []string) {
	for _, chID := range channelIDs {
		h.bus.Unsubscribe(chID, connID)
		h.mu.Lock()
		if set, ok := h.channelConns[chID]; ok {
			delete(set, connID)
			if len(set) == 0 {
				delete(h.channelConns, chID)
			}
		}
		if set, ok := h.connChannels[connID]; ok {
			delete(set, chID)
		}
		h.mu.Unlock()

		ch, ds, err := h.resolve(chID)
		if err != nil {
			continue
		}
		if err := h.adm.Unsubscribe(ds, ch); err != nil {
			h.log.Warn("释放适配器订阅失败", "channelId", chID, "err", err)
		}
	}
}

// resolve 查询通道与其数据源（不存在返回 apperr.NotFound）。
func (h *Hub) resolve(chID string) (*model.Channel, *model.DataSource, error) {
	if h.store == nil {
		return nil, nil, apperr.New(apperr.Internal, "store 未初始化")
	}
	ch, err := h.store.GetChannel(chID)
	if err != nil {
		return nil, nil, err
	}
	ds, err := h.store.GetDataSource(ch.DataSourceID)
	if err != nil {
		return nil, nil, err
	}
	return ch, ds, nil
}

// ————————————————— bus.Sink 实现 —————————————————

// DispatchFrames 把帧分发给订阅了对应通道的连接（单帧也走批量容器）。
func (h *Hub) DispatchFrames(batch []model.Frame) {
	for _, f := range batch {
		h.mu.RLock()
		connIDs := make([]string, 0, 4)
		for id := range h.channelConns[f.ChannelID] {
			connIDs = append(connIDs, id)
		}
		h.mu.RUnlock()
		for _, id := range connIDs {
			h.deliverToConn(id, f)
		}
	}
}

// deliverToConn 向指定连接投递一帧（连接不存在时静默丢弃）。
func (h *Hub) deliverToConn(connID string, f model.Frame) {
	h.mu.RLock()
	c, ok := h.conns[connID]
	h.mu.RUnlock()
	if !ok || c == nil {
		return
	}
	c.Enqueue(f)
}

// DispatchChannelStatus 向订阅该通道的连接广播状态。
func (h *Hub) DispatchChannelStatus(ev bus.ChannelStatusEvent) {
	msg := NewChannelStatus(ev.ChannelID, ev.Status, ev.LastError, ev.LastFrameAt, ev.LatencyMs)
	h.broadcastToChannel(ev.ChannelID, msg)
}

// DispatchSourceStatus 向订阅该数据源任一通道的连接广播状态；无人订阅时广播给全部连接。
func (h *Hub) DispatchSourceStatus(ev bus.SourceStatusEvent) {
	msg := NewSourceStatus(ev.DataSourceID, ev.Status, ev.LastError, ev.ReconnectInMs, ev.NextRetryAt)
	targets := make([]string, 0, 4)
	h.mu.RLock()
	for chID, dsID := range h.channelSrc {
		if dsID != ev.DataSourceID {
			continue
		}
		for id := range h.channelConns[chID] {
			targets = append(targets, id)
		}
	}
	if len(targets) == 0 {
		for id := range h.conns {
			targets = append(targets, id)
		}
	}
	h.mu.RUnlock()
	seen := make(map[string]struct{}, len(targets))
	for _, id := range targets {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		h.sendToConn(id, msg)
	}
}

// DispatchBackpressure 向订阅该通道的连接广播丢帧通知。
func (h *Hub) DispatchBackpressure(ev bus.BackpressureEvent) {
	h.broadcastToChannel(ev.ChannelID, NewBackpressure(ev.ChannelID, ev.Dropped, ev.WindowMs))
}

// broadcastToChannel 向某通道的全部订阅连接广播一条控制消息。
func (h *Hub) broadcastToChannel(chID string, msg ServerMsg) {
	h.mu.RLock()
	ids := make([]string, 0, 4)
	for id := range h.channelConns[chID] {
		ids = append(ids, id)
	}
	h.mu.RUnlock()
	for _, id := range ids {
		h.sendToConn(id, msg)
	}
}

// sendToConn 向指定连接发送控制消息。
func (h *Hub) sendToConn(connID string, msg ServerMsg) {
	h.mu.RLock()
	c, ok := h.conns[connID]
	h.mu.RUnlock()
	if !ok || c == nil {
		return
	}
	c.Send(msg)
}

// ConnCount 返回当前连接数（/healthz 使用）。
func (h *Hub) ConnCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns)
}

// Close 关闭全部连接（优雅退出时调用）。
func (h *Hub) Close() {
	h.mu.Lock()
	ids := make([]string, 0, len(h.conns))
	for id := range h.conns {
		ids = append(ids, id)
	}
	h.mu.Unlock()
	for _, id := range ids {
		h.unregister(id)
	}
}
