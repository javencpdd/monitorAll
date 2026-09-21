package adapter

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/bus"
	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/logx"
	"github.com/monitorall/monitorall/internal/model"
)

// ————————————————— Manager —————————————————

// Manager 管理全部数据源适配器的生命周期：引用计数、指数退避重连、健康探活、状态广播。
// goroutine 治理遵循 G1–G7：每 entry 一个 ctx + WaitGroup，一个常驻循环 + 单 Timer 复用。
type Manager struct {
	cfg   *config.Config
	bus   *bus.Bus
	store DataSourceLoader
	log   *slog.Logger

	mu      sync.Mutex
	entries map[string]*entry
}

// NewManager 构造适配器管理器。
func NewManager(cfg *config.Config, b *bus.Bus, st DataSourceLoader) *Manager {
	return &Manager{
		cfg:     cfg,
		bus:     b,
		store:   st,
		log:     logx.With("module", "adapter"),
		entries: make(map[string]*entry),
	}
}

// entry 为单个数据源的运行态。
type entry struct {
	ds      *model.DataSource
	adapter Adapter
	log     *slog.Logger

	mu       sync.Mutex
	refCount map[string]int          // channelID → 订阅数（跨 WS 连接累加）
	active   map[string]ChannelSpec  // 已 StartChannel 的通道
	state    model.Status
	lastErr  string
	nextAt   time.Time
	attempt  int
	started  bool
	closed   bool

	connCtx    context.Context
	connCancel context.CancelFunc
	wg         sync.WaitGroup
	closeOnce  sync.Once
	retryTimer *time.Timer
}

// ————————————————— 引用计数与生命周期 —————————————————

// Subscribe 增加某通道的订阅引用计数；首个订阅触发建连（BE-03）。
func (m *Manager) Subscribe(ds *model.DataSource, ch *model.Channel) error {
	if ds == nil || ch == nil {
		return apperr.New(apperr.InvalidParam, "数据源或通道为空")
	}
	e := m.ensureEntry(ds)

	spec := SpecFromChannel(ch)
	e.mu.Lock()
	e.refCount[ch.ID]++
	e.active[ch.ID] = spec
	needStart := !e.started
	if needStart {
		e.started = true
	}
	state := e.state
	e.mu.Unlock()

	if needStart {
		return m.startEntry(e)
	}
	// 已在线：直接补一条 StartChannel（幂等）
	if state == model.StatusOnline {
		ctx, cancel := context.WithTimeout(e.connCtx, m.timeout())
		defer cancel()
		if err := e.adapter.StartChannel(ctx, ch.ID, spec, m.emit(e)); err != nil {
			m.log.Warn("启动通道失败", "channelId", ch.ID, "err", err)
			m.publishChannelStatus(ch.ID, model.StatusError, err.Error(), 0, 0)
			return apperr.Wrap(err, apperr.AdapterError, "启动通道失败")
		}
		m.publishChannelStatus(ch.ID, model.StatusOnline, "", 0, 0)
	}
	return nil
}

// Unsubscribe 减少引用计数；归零时停止通道，全部归零则释放底层连接。
func (m *Manager) Unsubscribe(ds *model.DataSource, ch *model.Channel) error {
	if ds == nil || ch == nil {
		return apperr.New(apperr.InvalidParam, "数据源或通道为空")
	}
	m.mu.Lock()
	e, ok := m.entries[ds.ID]
	m.mu.Unlock()
	if !ok {
		return nil
	}

	e.mu.Lock()
	e.refCount[ch.ID]--
	if e.refCount[ch.ID] <= 0 {
		delete(e.refCount, ch.ID)
		delete(e.active, ch.ID)
	}
	remaining := len(e.refCount)
	e.mu.Unlock()

	if e.adapter != nil {
		if err := e.adapter.StopChannel(ch.ID); err != nil {
			m.log.Warn("停止通道失败", "channelId", ch.ID, "err", err)
		}
	}
	if remaining == 0 {
		m.releaseEntry(ds.ID)
	}
	return nil
}

// ensureEntry 获取或创建数据源运行态。
func (m *Manager) ensureEntry(ds *model.DataSource) *entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[ds.ID]; ok {
		e.ds = ds
		return e
	}
	e := &entry{
		ds:       ds,
		refCount: make(map[string]int),
		active:   make(map[string]ChannelSpec),
		state:    model.StatusIdle,
		log:      m.log.With(slog.String("dataSourceId", ds.ID)),
	}
	m.entries[ds.ID] = e
	return e
}

// startEntry 创建适配器并启动常驻循环。
func (m *Manager) startEntry(e *entry) error {
	ad, err := New(e.ds.Kind, e.ds, e.log)
	if err != nil {
		e.mu.Lock()
		e.state = model.StatusError
		e.lastErr = err.Error()
		e.started = false
		e.mu.Unlock()
		m.publishSourceStatus(e, 0, 0)
		return err
	}
	e.adapter = ad
	ctx, cancel := context.WithCancel(context.Background())
	e.connCtx = ctx
	e.connCancel = cancel
	e.retryTimer = time.NewTimer(time.Hour)
	if !e.retryTimer.Stop() {
		select {
		case <-e.retryTimer.C:
		default:
		}
	}
	e.wg.Add(1)
	go m.loop(e)
	return nil
}

// releaseEntry 释放数据源：取消 ctx、等待 goroutine 归零、关闭适配器。
func (m *Manager) releaseEntry(dsID string) {
	m.mu.Lock()
	e, ok := m.entries[dsID]
	if ok {
		delete(m.entries, dsID)
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	e.closeOnce.Do(func() {
		e.mu.Lock()
		e.closed = true
		e.state = model.StatusIdle
		e.mu.Unlock()

		if e.connCancel != nil {
			e.connCancel()
		}
		// G2：WaitGroup 等待必须带超时保护，避免卡住 API
		done := make(chan struct{})
		go func() {
			e.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Duration(config.AdapterCloseTimeoutMs) * time.Millisecond):
			e.log.Error("等待适配器 goroutine 退出超时", "timeoutMs", config.AdapterCloseTimeoutMs)
		}
		if e.adapter != nil {
			if err := e.adapter.Close(); err != nil {
				e.log.Warn("关闭适配器失败", "err", err)
			}
		}
		if e.retryTimer != nil {
			e.retryTimer.Stop()
		}
	})
	m.publishSourceStatus(e, 0, 0)
}

// CloseAll 释放全部数据源（优雅退出时调用）。
func (m *Manager) CloseAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.entries))
	for id := range m.entries {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.releaseEntry(id)
	}
}

// ————————————————— 常驻循环：连接 / 健康 / 退避重连 —————————————————

// loop 为 entry 的唯一常驻 goroutine（G3/G4/G5：单循环 + 单 Timer）。
func (m *Manager) loop(e *entry) {
	defer e.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("适配器循环 panic", "panic", r)
			m.setState(e, model.StatusError, "adapter panic")
		}
	}()

	ctx := e.connCtx
	interval := time.Duration(m.cfg.Adapter.HealthIntervalMs) * time.Millisecond
	if interval <= 0 {
		interval = time.Duration(config.DefaultHealthIntervalMs) * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if ctx.Err() != nil {
			return
		}
		if err := m.connectOnce(ctx, e); err != nil {
			delay := m.scheduleRetry(e)
			if delay <= 0 || !m.waitRetry(ctx, e, delay) {
				return
			}
			continue
		}
		if m.healthLoop(ctx, e, ticker) {
			return
		}
		if ctx.Err() != nil {
			return
		}
		delay := m.scheduleRetry(e)
		if delay <= 0 || !m.waitRetry(ctx, e, delay) {
			return
		}
	}
}

// connectOnce 建连并恢复全部活动通道。
func (m *Manager) connectOnce(ctx context.Context, e *entry) error {
	m.setState(e, model.StatusConnecting, "")
	if m.store != nil {
		params, err := m.store.ConnParamsFor(e.ds.ID)
		if err == nil && params != nil {
			e.mu.Lock()
			e.ds.ConnParams = params
			e.mu.Unlock()
		}
	}
	startCtx, cancel := context.WithTimeout(ctx, m.timeout())
	defer cancel()
	if err := e.adapter.Start(startCtx); err != nil {
		m.setState(e, model.StatusReconnecting, err.Error())
		return err
	}
	m.startActiveChannels(ctx, e)
	m.setState(e, model.StatusOnline, "")
	return nil
}

// startActiveChannels 遍历活动通道逐个 StartChannel（幂等）。
func (m *Manager) startActiveChannels(ctx context.Context, e *entry) {
	e.mu.Lock()
	specs := make(map[string]ChannelSpec, len(e.active))
	for k, v := range e.active {
		specs[k] = v
	}
	e.mu.Unlock()
	emitter := m.emit(e)
	for chID, spec := range specs {
		cctx, cancel := context.WithTimeout(ctx, m.timeout())
		if err := e.adapter.StartChannel(cctx, chID, spec, emitter); err != nil {
			e.log.Warn("启动通道失败", "channelId", chID, "err", err)
			m.publishChannelStatus(chID, model.StatusError, err.Error(), 0, 0)
		} else {
			m.publishChannelStatus(chID, model.StatusOnline, "", 0, 0)
		}
		cancel()
	}
}

// healthLoop 在线期间的健康探活；返回 true 表示应退出外层循环。
func (m *Manager) healthLoop(ctx context.Context, e *entry, ticker *time.Ticker) bool {
	for {
		select {
		case <-ctx.Done():
			return true
		case <-ticker.C:
			hctx, cancel := context.WithTimeout(ctx, m.timeout())
			h := e.adapter.Health(hctx)
			cancel()
			if h.OK {
				e.mu.Lock()
				if e.state != model.StatusOnline {
					e.state = model.StatusOnline
					e.lastErr = ""
				}
				e.ds.LastOKAt = model.NowMs()
				e.mu.Unlock()
				continue
			}
			detail := h.Detail
			if detail == "" {
				detail = "health check failed"
			}
			m.setState(e, model.StatusReconnecting, detail)
			return false
		}
	}
}

// waitRetry 等待退避时间；ctx 取消返回 false。
func (m *Manager) waitRetry(ctx context.Context, e *entry, d time.Duration) bool {
	t := e.retryTimer
	if t == nil {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(d):
			return true
		}
	}
	// G3：单 Timer 复用，Stop 后 Reset，禁止每次新建 goroutine
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// scheduleRetry 计算下次退避时长并广播状态；返回 <=0 表示不再重试（进入 error）。
func (m *Manager) scheduleRetry(e *entry) time.Duration {
	base := time.Duration(m.cfg.Adapter.ReconnectBaseMs) * time.Millisecond
	max := time.Duration(m.cfg.Adapter.ReconnectMaxMs) * time.Millisecond
	jitter := m.cfg.Adapter.ReconnectJitter
	if base <= 0 {
		base = time.Duration(config.DefaultReconnectBaseMs) * time.Millisecond
	}
	if max <= 0 {
		max = time.Duration(config.DefaultReconnectMaxMs) * time.Millisecond
	}
	if jitter <= 0 {
		jitter = config.DefaultReconnectJitter
	}

	e.mu.Lock()
	n := e.attempt
	e.attempt++
	maxRetry := m.cfg.Adapter.ReconnectMaxRetry
	if e.ds != nil && e.ds.RetryPolicy != nil {
		if e.ds.RetryPolicy.BaseMs > 0 {
			base = time.Duration(e.ds.RetryPolicy.BaseMs) * time.Millisecond
		}
		if e.ds.RetryPolicy.MaxMs > 0 {
			max = time.Duration(e.ds.RetryPolicy.MaxMs) * time.Millisecond
		}
		if e.ds.RetryPolicy.Jitter > 0 {
			jitter = e.ds.RetryPolicy.Jitter
		}
		if e.ds.RetryPolicy.MaxRetry != 0 {
			maxRetry = e.ds.RetryPolicy.MaxRetry
		}
	}
	e.mu.Unlock()

	if maxRetry >= 0 && e.attempt > maxRetry {
		m.setState(e, model.StatusError, "重连次数超过上限")
		return 0
	}
	d := NextDelay(base, max, jitter, n)
	e.mu.Lock()
	e.nextAt = time.Now().Add(d)
	e.mu.Unlock()
	m.publishSourceStatus(e, d.Milliseconds(), model.NowMs()+d.Milliseconds())
	return d
}

// NextDelay 计算第 n 次重连的退避时长：min(base*2^n, max) * (1 ± jitter)。
func NextDelay(base, max time.Duration, jitter float64, n int) time.Duration {
	if base <= 0 {
		base = time.Duration(config.DefaultReconnectBaseMs) * time.Millisecond
	}
	if max <= 0 || max < base {
		max = time.Duration(config.DefaultReconnectMaxMs) * time.Millisecond
	}
	shift := uint(n)
	if shift > 20 {
		shift = 20 // 防溢出
	}
	d := base * time.Duration(1<<shift)
	if d > max || d <= 0 {
		d = max
	}
	f := 1.0 + (rand.Float64()*2-1)*jitter
	out := time.Duration(float64(d) * f)
	if out < 0 {
		out = base
	}
	return out
}

// timeout 返回单次操作的默认超时。
func (m *Manager) timeout() time.Duration {
	t := m.cfg.Adapter.SampleTimeoutMs
	if t <= 0 {
		t = config.DefaultSampleTimeoutMs
	}
	return time.Duration(t) * time.Millisecond
}

// emit 构造该数据源的投递回调（永不阻塞，总线内部自带限流与丢弃）。
func (m *Manager) emit(e *entry) Emit {
	return func(channelID string, f model.Frame) {
		if m.bus == nil {
			return
		}
		m.bus.Publish(channelID, f)
	}
}

// ————————————————— 状态读写与广播 —————————————————

// setState 更新数据源状态并广播。
func (m *Manager) setState(e *entry, st model.Status, lastErr string) {
	e.mu.Lock()
	e.state = st
	e.lastErr = lastErr
	e.ds.Status = st
	e.ds.LastError = lastErr
	if st == model.StatusOnline {
		e.ds.LastOKAt = model.NowMs()
		e.attempt = 0
	}
	e.mu.Unlock()

	for _, chID := range e.activeChannelIDs() {
		m.publishChannelStatus(chID, st, lastErr, 0, 0)
	}
	m.publishSourceStatus(e, 0, 0)
}

// activeChannelIDs 返回当前活动通道 ID 列表。
func (e *entry) activeChannelIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.active))
	for id := range e.active {
		out = append(out, id)
	}
	return out
}

// publishSourceStatus 广播数据源状态（含重连倒计时）。
func (m *Manager) publishSourceStatus(e *entry, reconnectInMs, nextRetryAt int64) {
	if m.bus == nil {
		return
	}
	e.mu.Lock()
	ev := bus.SourceStatusEvent{
		DataSourceID:  e.ds.ID,
		Status:        e.state,
		LastError:     e.lastErr,
		ReconnectInMs: reconnectInMs,
		NextRetryAt:   nextRetryAt,
	}
	e.mu.Unlock()
	m.bus.PublishSourceStatus(ev)
}

// publishChannelStatus 广播通道状态。
func (m *Manager) publishChannelStatus(chID string, st model.Status, lastErr string, lastFrameAt, latencyMs int64) {
	if m.bus == nil {
		return
	}
	m.bus.PublishChannelStatus(bus.ChannelStatusEvent{
		ChannelID:   chID,
		Status:      st,
		LastError:   lastErr,
		LastFrameAt: lastFrameAt,
		LatencyMs:   latencyMs,
	})
}

// ChannelStatus 返回某通道当前的运行态状态（供 subscribed ack 使用）。
func (m *Manager) ChannelStatus(chID string) model.Status {
	if m.bus == nil {
		return model.StatusIdle
	}
	st := m.bus.Stats(chID)
	if st.Status == "" {
		return model.StatusConnecting
	}
	return st.Status
}

// SourceStatus 返回数据源状态、最近错误、重连剩余毫秒与下次重试时刻。
func (m *Manager) SourceStatus(dsID string) (model.Status, string, int64, int64) {
	m.mu.Lock()
	e, ok := m.entries[dsID]
	m.mu.Unlock()
	if !ok {
		return model.StatusIdle, "", 0, 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var reconnectInMs int64
	if !e.nextAt.IsZero() {
		reconnectInMs = time.Until(e.nextAt).Milliseconds()
		if reconnectInMs < 0 {
			reconnectInMs = 0
		}
	}
	return e.state, e.lastErr, reconnectInMs, e.nextAt.UnixMilli()
}

// ————————————————— 样例帧（D4） —————————————————

// sampleSink 为一次性帧采集器（实现 bus.FrameSink，读通道容量 1，永不阻塞）。
type sampleSink struct {
	id string
	ch chan model.Frame
}

// ID 返回采集器标识。
func (s *sampleSink) ID() string { return s.id }

// OnFrame 收集一帧；通道满时丢弃（永不阻塞）。
func (s *sampleSink) OnFrame(f model.Frame) {
	select {
	case s.ch <- f:
	default:
		// 已有未取走的帧，保持最新：取出旧的再写入
		select {
		case <-s.ch:
		default:
		}
		select {
		case s.ch <- f:
		default:
		}
	}
}

// Sample 临时建连、订阅单个通道并等待首帧，用于 /datasources/test 与 /channels/{id}/sample。
// 超时返回 apperr.SampleTimeout。
func (m *Manager) Sample(ctx context.Context, ds *model.DataSource, spec ChannelSpec, timeout time.Duration) (*model.Frame, error) {
	if ds == nil {
		return nil, apperr.New(apperr.InvalidParam, "数据源为空")
	}
	if timeout <= 0 {
		timeout = m.timeout()
	}
	if m.bus == nil {
		return nil, apperr.New(apperr.Internal, "总线未初始化")
	}

	tempChID := model.NewTempID()
	broker := m.bus.EnsureChannel(tempChID, spec.PayloadType, 0)
	collector := &sampleSink{id: "sample_" + tempChID, ch: make(chan model.Frame, 1)}
	m.bus.RegisterSink(collector)
	m.bus.Subscribe(tempChID, collector.ID())
	defer func() {
		m.bus.Unsubscribe(tempChID, collector.ID())
		m.bus.UnregisterSink(collector.ID())
		m.bus.RemoveChannel(tempChID)
		_ = broker
	}()

	ad, err := New(ds.Kind, ds, m.log.With(slog.String("mode", "sample")))
	if err != nil {
		return nil, err
	}
	sampleCtx, cancel := context.WithTimeout(ctx, timeout+m.timeout())
	defer cancel()

	if err := ad.Start(sampleCtx); err != nil {
		_ = ad.Close()
		return nil, apperr.Wrap(err, apperr.SourceConnectFailed, "连接数据源失败")
	}
	emit := func(channelID string, f model.Frame) {
		m.bus.Publish(tempChID, f)
	}
	if err := ad.StartChannel(sampleCtx, tempChID, spec, emit); err != nil {
		_ = ad.Close()
		return nil, apperr.Wrap(err, apperr.AdapterError, "订阅通道失败")
	}
	defer func() {
		_ = ad.StopChannel(tempChID)
		_ = ad.Close()
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case f := <-collector.ch:
		return &f, nil
	case <-timer.C:
		// 兜底：总线里可能已有一帧（限流器合并窗口或刚好错过）
		if f, ok := m.bus.LatestFrame(tempChID); ok {
			return f, nil
		}
		return nil, apperr.New(apperr.SampleTimeout, "等待样例帧超时，未收到数据")
	case <-ctx.Done():
		return nil, apperr.Wrap(errors.New("context canceled"), apperr.SampleTimeout, "样例帧采集被取消")
	}
}
