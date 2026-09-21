// Package bus 实现帧总线：通道级 Broker（订阅集合、seq 分配、环形缓冲、状态）、
// 令牌桶限流 + 合并窗口最新值覆盖 + 丢弃积压帧，以及背压事件广播。
// 核心约束（BE-04 / G6）：Publish 永不阻塞，emit 回调不会拖慢适配器。
package bus

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/model"
)

// ————————————————— 对外事件 —————————————————

// ChannelStatusEvent 为通道状态变更事件（对应 WS op=channelStatus）。
type ChannelStatusEvent struct {
	ChannelID   string        `json:"channelId"`
	Status      model.Status  `json:"status"`
	LastError   string        `json:"lastError,omitempty"`
	LastFrameAt int64         `json:"lastFrameAt,omitempty"`
	LatencyMs   int64         `json:"latencyMs,omitempty"`
}

// SourceStatusEvent 为数据源状态变更事件（对应 WS op=sourceStatus）。
type SourceStatusEvent struct {
	DataSourceID  string       `json:"dataSourceId"`
	Status        model.Status `json:"status"`
	LastError     string       `json:"lastError,omitempty"`
	ReconnectInMs int64        `json:"reconnectInMs,omitempty"`
	NextRetryAt   int64        `json:"nextRetryAt,omitempty"`
}

// BackpressureEvent 为丢帧通知事件（对应 WS op=backpressure，每通道每秒最多 1 条）。
type BackpressureEvent struct {
	ChannelID string `json:"channelId"`
	Dropped   uint64 `json:"dropped"`
	WindowMs  int64  `json:"windowMs"`
}

// FrameSink 为帧消费者（WS 连接、样例帧采集器等）。
// 实现必须保证 OnFrame 永不阻塞，否则会拖慢适配器（G6）。
type FrameSink interface {
	// ID 返回消费者唯一标识。
	ID() string
	// OnFrame 投递一帧；实现必须非阻塞。
	OnFrame(f model.Frame)
}

// Sink 为总线向外部广播的出口（由 WS Hub 实现）。
type Sink interface {
	DispatchFrames(batch []model.Frame)
	DispatchChannelStatus(ev ChannelStatusEvent)
	DispatchSourceStatus(ev SourceStatusEvent)
	DispatchBackpressure(ev BackpressureEvent)
}

// ————————————————— Bus —————————————————

// Bus 管理全部通道 Broker 与订阅关系。
type Bus struct {
	cfg *config.BusConfig

	mu        sync.RWMutex
	brokers   map[string]*Broker
	sinks     map[string]FrameSink
	startedAt time.Time

	sink      Sink
	sinkMu    sync.RWMutex

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New 构造一个总线。
func New(cfg *config.BusConfig) *Bus {
	if cfg == nil {
		d := config.DefaultConfig()
		cfg = &d.Bus
	}
	return &Bus{
		cfg:       cfg,
		brokers:   make(map[string]*Broker),
		sinks:     make(map[string]FrameSink),
		startedAt: time.Now(),
	}
}

// SetSink 设置广播出口（WS Hub）。
func (b *Bus) SetSink(s Sink) {
	b.sinkMu.Lock()
	b.sink = s
	b.sinkMu.Unlock()
}

// getSink 读取当前广播出口。
func (b *Bus) getSink() Sink {
	b.sinkMu.RLock()
	defer b.sinkMu.RUnlock()
	return b.sink
}

// Start 启动背压通知协程（唯一常驻 goroutine，ctx 取消即退出）。
func (b *Bus) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	b.cancel = cancel
	interval := time.Duration(b.cfg.BackpressureNotifyMs) * time.Millisecond
	if interval <= 0 {
		interval = time.Duration(config.DefaultBackpressureNotifyMs) * time.Millisecond
	}
	b.wg.Add(1)
	go b.notifyLoop(ctx, interval)
}

// Stop 停止背压通知协程。
func (b *Bus) Stop() {
	if b.cancel != nil {
		b.cancel()
	}
	b.wg.Wait()
}

// notifyLoop 每 backpressureNotifyMs 扫描各通道的丢帧计数并广播。
func (b *Bus) notifyLoop(ctx context.Context, interval time.Duration) {
	defer b.wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.flushBackpressure(interval.Milliseconds())
		}
	}
}

// flushBackpressure 遍历有丢帧的通道，广播一次并清零窗口计数。
func (b *Bus) flushBackpressure(windowMs int64) {
	sink := b.getSink()
	b.mu.RLock()
	brokers := make([]*Broker, 0, len(b.brokers))
	for _, br := range b.brokers {
		brokers = append(brokers, br)
	}
	b.mu.RUnlock()

	for _, br := range brokers {
		dropped := br.takeWindowDropped()
		if dropped == 0 {
			continue
		}
		ev := BackpressureEvent{ChannelID: br.ID, Dropped: dropped, WindowMs: windowMs}
		if sink != nil {
			sink.DispatchBackpressure(ev)
		}
	}
}

// ————————————————— Broker 生命周期 —————————————————

// EnsureChannel 保证某通道存在 Broker（幂等），返回 Broker。
func (b *Bus) EnsureChannel(chID string, pt model.PayloadType, maxHz float64) *Broker {
	if maxHz <= 0 {
		maxHz = b.defaultHz(pt)
	}
	b.mu.Lock()
	br, ok := b.brokers[chID]
	if !ok {
		br = newBroker(chID, pt, maxHz, b.cfg.RingCapacity)
		b.brokers[chID] = br
	}
	br.SetPayloadType(pt)
	if maxHz > 0 {
		br.SetMaxHz(maxHz)
	}
	b.mu.Unlock()
	return br
}

// defaultHz 按 payloadType 返回默认限流频率。
func (b *Bus) defaultHz(pt model.PayloadType) float64 {
	switch pt {
	case model.PayloadImage:
		return b.cfg.ImageMaxHz
	case model.PayloadVideoStream:
		return b.cfg.VideoStatusHz
	default:
		return b.cfg.DefaultMaxHz
	}
}

// Broker 返回通道的 Broker，不存在返回 nil。
func (b *Bus) Broker(chID string) *Broker {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.brokers[chID]
}

// RemoveChannel 移除通道 Broker（删除通道时调用）。
func (b *Bus) RemoveChannel(chID string) {
	b.mu.Lock()
	delete(b.brokers, chID)
	b.mu.Unlock()
}

// ————————————————— 订阅关系 —————————————————

// RegisterSink 注册一个消费者。
func (b *Bus) RegisterSink(s FrameSink) {
	if s == nil {
		return
	}
	b.mu.Lock()
	b.sinks[s.ID()] = s
	b.mu.Unlock()
}

// UnregisterSink 注销消费者，并清理其在全部通道上的订阅。
func (b *Bus) UnregisterSink(id string) {
	b.mu.Lock()
	delete(b.sinks, id)
	brokers := make([]*Broker, 0, len(b.brokers))
	for _, br := range b.brokers {
		brokers = append(brokers, br)
	}
	b.mu.Unlock()
	for _, br := range brokers {
		br.removeSubscriber(id)
	}
}

// Subscribe 让 sinkID 订阅某通道（幂等，Set 语义）。
func (b *Bus) Subscribe(chID, sinkID string) {
	br := b.Broker(chID)
	if br == nil {
		return
	}
	br.addSubscriber(sinkID)
}

// Unsubscribe 取消订阅；返回剩余订阅数。
func (b *Bus) Unsubscribe(chID, sinkID string) int {
	br := b.Broker(chID)
	if br == nil {
		return 0
	}
	return br.removeSubscriber(sinkID)
}

// RefCount 返回某通道的订阅引用计数（以 WS 连接为单位）。
func (b *Bus) RefCount(chID string) int {
	br := b.Broker(chID)
	if br == nil {
		return 0
	}
	return br.subscriberCount()
}

// ————————————————— Publish（永不阻塞） —————————————————

// Publish 把一帧投递到通道：分配 seq、写入环形缓冲、经限流器决定是否下发。
// 任何情况下都不阻塞调用方（G6）。
func (b *Bus) Publish(chID string, f model.Frame) {
	br := b.Broker(chID)
	if br == nil {
		// 无人订阅的通道直接丢弃，避免内存增长
		return
	}
	br.publish(f, b.deliverFrames)
}

// PublishWithEnsure 在通道 Broker 缺失时先创建再投递（样例帧等场景使用）。
func (b *Bus) PublishWithEnsure(chID string, pt model.PayloadType, f model.Frame) {
	br := b.EnsureChannel(chID, pt, 0)
	br.publish(f, b.deliverFrames)
}

// deliverFrames 把一帧分发给该通道的全部订阅者（回调形式，避免持锁调用外部代码）。
func (b *Bus) deliverFrames(sinkIDs []string, f model.Frame) {
	b.mu.RLock()
	targets := make([]FrameSink, 0, len(sinkIDs))
	for _, id := range sinkIDs {
		if s, ok := b.sinks[id]; ok {
			targets = append(targets, s)
		}
	}
	b.mu.RUnlock()
	for _, s := range targets {
		s.OnFrame(f)
	}
}

// SinksOf 返回某通道的全部订阅者（供 Hub 定向广播状态事件）。
func (b *Bus) SinksOf(chID string) []string {
	br := b.Broker(chID)
	if br == nil {
		return nil
	}
	return br.snapshotSubs()
}

// LatestFrame 返回通道最近一帧。
func (b *Bus) LatestFrame(chID string) (*model.Frame, bool) {
	br := b.Broker(chID)
	if br == nil {
		return nil, false
	}
	return br.latestFrame()
}

// Stats 返回通道运行态统计。
func (b *Bus) Stats(chID string) BrokerStats {
	br := b.Broker(chID)
	if br == nil {
		return BrokerStats{}
	}
	return br.stats()
}

// ————————————————— 状态广播 —————————————————

// PublishChannelStatus 广播通道状态（WS op=channelStatus）。
func (b *Bus) PublishChannelStatus(ev ChannelStatusEvent) {
	br := b.Broker(ev.ChannelID)
	if br != nil {
		br.setStatus(ev.Status, ev.LastError, ev.LastFrameAt, ev.LatencyMs)
	}
	if sink := b.getSink(); sink != nil {
		sink.DispatchChannelStatus(ev)
	}
}

// PublishSourceStatus 广播数据源状态（WS op=sourceStatus）。
func (b *Bus) PublishSourceStatus(ev SourceStatusEvent) {
	if sink := b.getSink(); sink != nil {
		sink.DispatchSourceStatus(ev)
	}
}

// ————————————————— Broker —————————————————

// BrokerStats 为通道运行态统计（供 ChannelRuntime 使用）。
type BrokerStats struct {
	RefCount    int           `json:"refCount"`
	FrameRateHz float64       `json:"frameRateHz"`
	Dropped     uint64        `json:"dropped"`
	LastFrameAt int64         `json:"lastFrameAt"`
	LastSeq     uint64        `json:"lastSeq"`
	LatencyMs   int64         `json:"latencyMs"`
	Status      model.Status  `json:"status"`
	LastError   string        `json:"lastError"`
}

// Broker 为单通道的帧集散地。
type Broker struct {
	ID string

	mu             sync.RWMutex
	subs           map[string]struct{}
	limiter        *Limiter
	ring           *ringBuffer
	payloadType    model.PayloadType
	status         model.Status
	lastError      string
	lastFrameAt    int64
	latencyMs      int64
	totalDropped   atomic.Uint64
	windowDropped  atomic.Uint64
	deliveredCount atomic.Uint64
	rateWindowStart atomic.Int64
	rateWindowCount atomic.Uint64
	frameRateHz    atomic.Uint64 // 存 Hz*100 避免浮点原子操作
}

// newBroker 构造一个通道 Broker。
func newBroker(id string, pt model.PayloadType, maxHz float64, capacity int) *Broker {
	if capacity <= 0 {
		capacity = config.DefaultRingCapacity
	}
	b := &Broker{
		ID:          id,
		subs:        make(map[string]struct{}),
		limiter:     NewLimiter(maxHz),
		ring:        newRingBuffer(capacity),
		payloadType: pt,
		status:      model.StatusIdle,
	}
	b.rateWindowStart.Store(time.Now().UnixMilli())
	return b
}

// SetMaxHz 调整限流频率。
func (br *Broker) SetMaxHz(hz float64) {
	br.mu.Lock()
	br.limiter.SetMaxHz(hz)
	br.mu.Unlock()
}

// SetPayloadType 设置通道 payloadType（HTTP 适配器推断后回填）。
func (br *Broker) SetPayloadType(pt model.PayloadType) {
	br.mu.Lock()
	br.payloadType = pt
	br.mu.Unlock()
}

// addSubscriber 增加订阅者（幂等）。
func (br *Broker) addSubscriber(sinkID string) {
	br.mu.Lock()
	br.subs[sinkID] = struct{}{}
	br.mu.Unlock()
}

// removeSubscriber 移除订阅者，返回剩余数量。
func (br *Broker) removeSubscriber(sinkID string) int {
	br.mu.Lock()
	delete(br.subs, sinkID)
	n := len(br.subs)
	br.mu.Unlock()
	return n
}

// subscriberCount 返回订阅者数量。
func (br *Broker) subscriberCount() int {
	br.mu.RLock()
	defer br.mu.RUnlock()
	return len(br.subs)
}

// snapshotSubs 拷贝当前订阅者集合，避免持锁回调（防死锁）。
func (br *Broker) snapshotSubs() []string {
	br.mu.RLock()
	out := make([]string, 0, len(br.subs))
	for id := range br.subs {
		out = append(out, id)
	}
	br.mu.RUnlock()
	return out
}

// setStatus 更新通道运行态。
func (br *Broker) setStatus(st model.Status, lastErr string, lastFrameAt, latencyMs int64) {
	br.mu.Lock()
	br.status = st
	br.lastError = lastErr
	if lastFrameAt > 0 {
		br.lastFrameAt = lastFrameAt
	}
	if latencyMs > 0 {
		br.latencyMs = latencyMs
	}
	br.mu.Unlock()
}

// deliverFn 为投递回调：把一帧分发给一组订阅者。
type deliverFn func(sinkIDs []string, f model.Frame)

// publish 处理一帧：seq 分配 → 环形缓冲 → 限流 → 投递。
func (br *Broker) publish(f model.Frame, deliver deliverFn) {
	now := time.Now()
	br.mu.Lock()
	seq := br.ring.nextSeq()
	f.Seq = seq
	f.ChannelID = br.ID
	f.PayloadType = br.payloadType
	f.SizeBytes = len(f.Payload)
	br.ring.push(f)
	br.lastFrameAt = f.IngestedTs
	br.latencyMs = f.IngestedTs - f.PublishedTs
	br.lastError = ""
	allowed := false
	_, allowed = br.limiter.Allow(now, f)
	br.mu.Unlock()

	if !allowed {
		br.totalDropped.Add(1)
		br.windowDropped.Add(1)
		return
	}

	br.deliveredCount.Add(1)
	br.rateWindowCount.Add(1)
	br.updateRate(now)

	ids := br.snapshotSubs()
	if len(ids) == 0 {
		return
	}
	// 单帧也走批量容器，保持 WS 只有 frames 一种帧消息类型
	deliver(ids, f)
}

// updateRate 每秒重算一次实际帧率。
func (br *Broker) updateRate(now time.Time) {
	startMs := br.rateWindowStart.Load()
	elapsed := now.UnixMilli() - startMs
	if elapsed < 1000 {
		return
	}
	count := br.rateWindowCount.Swap(0)
	hz := float64(count) * 1000.0 / float64(elapsed)
	br.frameRateHz.Store(uint64(hz * 100))
	br.rateWindowStart.Store(now.UnixMilli())
}

// takeWindowDropped 取出并清零窗口丢帧计数。
func (br *Broker) takeWindowDropped() uint64 {
	return br.windowDropped.Swap(0)
}

// latestFrame 返回最近一帧。
func (br *Broker) latestFrame() (*model.Frame, bool) {
	return br.ring.latest()
}

// stats 汇总运行态。
func (br *Broker) stats() BrokerStats {
	br.mu.RLock()
	status := br.status
	lastErr := br.lastError
	lastFrameAt := br.lastFrameAt
	latencyMs := br.latencyMs
	br.mu.RUnlock()
	return BrokerStats{
		RefCount:    br.subscriberCount(),
		FrameRateHz: float64(br.frameRateHz.Load()) / 100.0,
		Dropped:     br.totalDropped.Load(),
		LastFrameAt: lastFrameAt,
		LastSeq:     br.ring.lastSeq(),
		LatencyMs:   latencyMs,
		Status:      status,
		LastError:   lastErr,
	}
}

// ————————————————— 环形缓冲 —————————————————

// ringBuffer 为定容环形缓冲（O(1) 写入，覆盖最旧数据）。
type ringBuffer struct {
	mu      sync.Mutex
	buf     []model.Frame
	cap     int
	size    int
	writeAt int
	seq     uint64
}

// newRingBuffer 构造定容环形缓冲。
func newRingBuffer(capacity int) *ringBuffer {
	return &ringBuffer{buf: make([]model.Frame, capacity), cap: capacity}
}

// nextSeq 分配下一个序号（通道内单调递增）。
func (r *ringBuffer) nextSeq() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	return r.seq
}

// lastSeq 返回当前最大序号。
func (r *ringBuffer) lastSeq() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seq
}

// push 写入一帧（满则覆盖最旧）。
func (r *ringBuffer) push(f model.Frame) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.writeAt] = f
	r.writeAt = (r.writeAt + 1) % r.cap
	if r.size < r.cap {
		r.size++
	}
}

// latest 返回最近一帧。
func (r *ringBuffer) latest() (*model.Frame, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size == 0 {
		return nil, false
	}
	idx := (r.writeAt - 1 + r.cap) % r.cap
	f := r.buf[idx]
	return &f, true
}
