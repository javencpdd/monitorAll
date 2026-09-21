package ros

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/monitorall/monitorall/internal/adapter"
	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/model"
)

// init 注册 ROS 适配器工厂（ROS1/ROS2 共用，差异在 diff.go）。
func init() {
	adapter.Register(model.KindROS, func(ds *model.DataSource, log *slog.Logger) (adapter.Adapter, error) {
		return New(ds, log)
	})
}

// Adapter 为 ROS 数据源适配器：一个 DataSource = 一条到 rosbridge 的 WS 连接。
type Adapter struct {
	ds      *model.DataSource
	log     *slog.Logger
	cfg     *config.Config
	params  model.ROSConnParams
	version string

	mu       sync.Mutex
	client   *Client
	started  bool
	lastErr  string
	channels map[string]*rosChannel
	crs      model.CRS

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// rosChannel 为一个已订阅话题的运行态。
type rosChannel struct {
	id        string
	spec      adapter.ChannelSpec
	emit      adapter.Emit
	msgType   string
	mu        sync.Mutex
	lastFrame int64
	lastErr   string
	cancel    context.CancelFunc
}

// markFrame 记录已收到帧的时刻。
func (ch *rosChannel) markFrame(ts int64) {
	ch.mu.Lock()
	ch.lastFrame = ts
	ch.mu.Unlock()
}

// hasFrame 判断是否已收到过帧。
func (ch *rosChannel) hasFrame() bool {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return ch.lastFrame > 0
}

// setError 记录通道错误。
func (ch *rosChannel) setError(msg string) {
	ch.mu.Lock()
	ch.lastErr = msg
	ch.mu.Unlock()
}

// New 构造 ROS 适配器。
func New(ds *model.DataSource, log *slog.Logger) (*Adapter, error) {
	if ds == nil {
		return nil, apperr.New(apperr.InvalidParam, "数据源为空")
	}
	cfg := config.DefaultConfig()
	if env := adapter.EnvOf(); env != nil && env.Cfg != nil {
		cfg = env.Cfg
	}
	params, err := adapter.ParamsTo[model.ROSConnParams](ds.ConnParams)
	if err != nil {
		return nil, err
	}
	if params.BridgeURL == "" {
		return nil, apperr.New(apperr.InvalidURL, "bridgeUrl 不能为空")
	}
	return &Adapter{
		ds:       ds,
		log:      log,
		cfg:      cfg,
		params:   params,
		version:  VersionOf(params.Version),
		crs:      model.ParseCRS(params.CRS),
		channels: make(map[string]*rosChannel),
	}, nil
}

// DataSourceID 返回数据源 ID。
func (a *Adapter) DataSourceID() string { return a.ds.ID }

// Start 建立到 rosbridge 的连接；幂等。
func (a *Adapter) Start(ctx context.Context) error {
	a.mu.Lock()
	if a.started && a.client != nil && a.client.IsConnected() {
		a.mu.Unlock()
		return nil
	}
	a.ctx, a.cancel = context.WithCancel(context.Background())
	client := NewClient(a.params.BridgeURL, a.version, a.log)
	a.client = client
	a.started = true
	a.mu.Unlock()

	if err := client.Connect(ctx); err != nil {
		a.mu.Lock()
		a.lastErr = err.Error()
		a.started = false
		a.mu.Unlock()
		return apperr.Wrap(err, apperr.SourceConnectFailed, "连接 rosbridge 失败")
	}
	a.mu.Lock()
	a.lastErr = ""
	a.mu.Unlock()
	return nil
}

// Health 探活：连接是否仍然可用。
func (a *Adapter) Health(ctx context.Context) adapter.Health {
	start := time.Now()
	a.mu.Lock()
	client := a.client
	a.mu.Unlock()
	if client == nil || !client.IsConnected() {
		return adapter.Health{OK: false, Detail: "rosbridge 未连接"}
	}
	detail := client.LastError()
	if detail != "" {
		return adapter.Health{OK: false, LatencyMs: time.Since(start).Milliseconds(), Detail: detail}
	}
	return adapter.Health{OK: true, LatencyMs: time.Since(start).Milliseconds(), Detail: "ok"}
}

// ListChannels 发现通道：优先用手工填写的 topics，其次尝试 rosapi（软失败）。
func (a *Adapter) ListChannels(ctx context.Context) ([]adapter.ChannelSpec, error) {
	specs := make([]adapter.ChannelSpec, 0, 4)
	types := map[string]string{}
	for k, v := range a.params.TopicTypes {
		types[k] = NormalizeMsgType(v)
	}

	// rosapi 发现：任一失败都软降级，不阻塞主路径（D5）
	if a.client != nil && a.client.IsConnected() {
		if discovered, err := a.client.DiscoverTopicTypes(ctx); err == nil {
			for k, v := range discovered {
				types[k] = v
			}
		} else {
			a.log.Debug("rosapi 话题类型发现失败，降级为手工填写", "err", err)
		}
	}

	topics := a.params.Topics
	if len(topics) == 0 {
		for t := range types {
			topics = append(topics, t)
		}
	}
	sort.Strings(topics)

	for _, topic := range topics {
		msgType := types[topic]
		pt := model.PayloadJSON
		if msgType != "" {
			pt = PayloadTypeFor(msgType)
		}
		specs = append(specs, adapter.ChannelSpec{
			Name:        topic,
			PayloadType: pt,
			Meta: map[string]any{
				model.MetaRosMsgType: msgType,
				model.MetaRosVersion: a.version,
				model.MetaCRS:        string(a.crs),
			},
		})
	}
	return specs, nil
}

// StartChannel 订阅话题并注册归一化处理器；幂等。
func (a *Adapter) StartChannel(ctx context.Context, channelID string, spec adapter.ChannelSpec, emit adapter.Emit) error {
	a.mu.Lock()
	if _, ok := a.channels[channelID]; ok {
		a.mu.Unlock()
		return nil
	}
	client := a.client
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	chCtx, cancel := context.WithCancel(parent)
	msgType := ""
	if spec.Meta != nil {
		if v, ok := spec.Meta[model.MetaRosMsgType].(string); ok {
			msgType = v
		}
	}
	ch := &rosChannel{id: channelID, spec: spec, emit: emit, msgType: NormalizeMsgType(msgType), cancel: cancel}
	a.channels[channelID] = ch
	version := a.version
	crs := a.crs
	a.mu.Unlock()

	if client == nil {
		return apperr.New(apperr.AdapterError, "rosbridge 客户端未初始化")
	}

	pt := spec.PayloadType
	if pt == "" {
		pt = PayloadTypeFor(msgType)
	}
	maxHz := spec.DefaultHz
	opts := SubscribeOptionsFor(version, pt, maxHz)

	if err := client.Subscribe(spec.Name, opts); err != nil {
		return apperr.Wrap(err, apperr.AdapterError, "订阅话题失败")
	}

	client.OnMessage(spec.Name, func(msg map[string]any) {
		a.handleMessage(ch, msg, crs)
	})

	// ROS2 静默无数据：统一超时判定（差异点 5 的运行时落地）
	a.wg.Add(1)
	go a.watchNoData(chCtx, ch, client)
	return nil
}

// watchNoData 订阅后 NoDataTimeoutMs 内无帧 → 标记 reconnecting；10s 后重试一次 Subscribe。
func (a *Adapter) watchNoData(ctx context.Context, ch *rosChannel, client *Client) {
	defer a.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			a.log.Error("ROS 无数据巡检 panic", "panic", r)
		}
	}()
	timer := time.NewTimer(time.Duration(NoDataTimeoutMs) * time.Millisecond)
	defer timer.Stop()
	retryTicker := time.NewTicker(time.Duration(NoDataRetryDelayMs) * time.Millisecond)
	defer retryTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if !ch.hasFrame() {
				a.log.Warn("订阅后未收到数据", "topic", ch.spec.Name, "timeoutMs", NoDataTimeoutMs)
				ch.setError(NoDataErrorMessage)
			}
		case <-retryTicker.C:
			if !ch.hasFrame() {
				opts := SubscribeOptionsFor(a.version, ch.spec.PayloadType, ch.spec.DefaultHz)
				if err := client.Subscribe(ch.spec.Name, opts); err != nil {
					a.log.Warn("重试订阅话题失败", "topic", ch.spec.Name, "err", err)
				}
			}
		}
	}
}

// handleMessage 把一条 ROS msg 归一化成 Frame 并投递。
func (a *Adapter) handleMessage(ch *rosChannel, msg map[string]any, crs model.CRS) {
	msgType := ch.msgType
	if msgType == "" {
		msgType = NormalizeMsgType(guessMsgType(msg))
		ch.msgType = msgType
	}
	pt := PayloadTypeFor(msgType)
	stamp := model.StampToMs(msg["header"])
	if stamp <= 0 {
		stamp = model.NowMs()
	}

	var (
		payload any
		hints   map[string]model.FieldHint
	)
	switch pt {
	case model.PayloadScalar:
		m, _ := Lookup(msgType)
		payload = BuildScalar(msg, m)
	case model.PayloadTimeSeries:
		ts := BuildTimeSeries(msg, stamp)
		payload = ts
		if len(ts.Fields) == 0 {
			// 无数值字段时降级为 json，避免前端拿到空图表
			jp, jh := BuildJSON(msg)
			payload, hints, pt = jp, jh, model.PayloadJSON
		}
	case model.PayloadGeoPose:
		payload = BuildGeoPose(msg, crs, stamp, ch.spec.Name)
	case model.PayloadImage:
		payload = BuildImage(msg, stamp)
	case model.PayloadHistogram, model.PayloadLogLine:
		jp, jh := BuildJSON(msg)
		payload, hints = jp, jh
	default:
		jp, jh := BuildJSON(msg)
		payload, hints = jp, jh
	}

	f, err := model.NewFrame(ch.id, pt, payload, stamp)
	if err != nil {
		a.log.Error("构造帧失败", "topic", ch.spec.Name, "err", err)
		return
	}
	if hints != nil {
		f.SchemaHint = hints
	}
	ch.markFrame(f.IngestedTs)
	ch.emit(ch.id, f)
}

// StopChannel 取消订阅并清理处理器（幂等）。
func (a *Adapter) StopChannel(channelID string) error {
	a.mu.Lock()
	ch, ok := a.channels[channelID]
	if ok {
		delete(a.channels, channelID)
	}
	client := a.client
	a.mu.Unlock()
	if !ok || ch == nil {
		return nil
	}
	if ch.cancel != nil {
		ch.cancel()
	}
	if client != nil {
		_ = client.Unsubscribe(ch.spec.Name)
		client.ClearMessage(ch.spec.Name)
	}
	return nil
}

// Close 关闭 rosbridge 连接并等待派生 goroutine 退出（带超时保护）。
func (a *Adapter) Close() error {
	a.mu.Lock()
	ids := make([]string, 0, len(a.channels))
	for id := range a.channels {
		ids = append(ids, id)
	}
	client := a.client
	a.client = nil
	cancel := a.cancel
	a.cancel = nil
	a.started = false
	a.mu.Unlock()

	for _, id := range ids {
		_ = a.StopChannel(id)
	}
	if cancel != nil {
		cancel()
	}
	if client != nil {
		if err := client.Close(); err != nil {
			a.log.Debug("关闭 rosbridge 连接返回错误", "err", err)
		}
	}
	done := make(chan struct{})
	go func() {
		a.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Duration(config.AdapterCloseTimeoutMs) * time.Millisecond):
		a.log.Error("等待 ROS 适配器 goroutine 退出超时")
	}
	return nil
}

// guessMsgType 从 msg 结构猜测消息类型（无 topicTypes 时的兜底）。
func guessMsgType(msg map[string]any) string {
	keys := keySet(msg)
	switch {
	case keys["latitude"] && keys["longitude"]:
		return "sensor_msgs/NavSatFix"
	case keys["data"] && !keys["header"]:
		return "std_msgs/Float64"
	case keys["format"] && keys["data"]:
		return "sensor_msgs/CompressedImage"
	case keys["encoding"] && keys["width"] && keys["height"]:
		return "sensor_msgs/Image"
	case keys["twist"] && keys["pose"]:
		return "nav_msgs/Odometry"
	case keys["angular_velocity"] && keys["linear_acceleration"]:
		return "sensor_msgs/Imu"
	case keys["voltage"]:
		return "sensor_msgs/BatteryState"
	default:
		return ""
	}
}

// keySet 把 map 的键转成集合。
func keySet(m map[string]any) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

