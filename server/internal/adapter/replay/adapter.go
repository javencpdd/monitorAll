// Package replay 实现离线 JSON 文件回放适配器：把用户导入的机器人记录文件
// 按原始时间戳节奏重新播出来，经总线推帧，前端可像实时数据源一样订阅。
//
// 与实时适配器的三点关键差异（架构 §8.5 HTTP 适配器为原型）：
//  1. 无外部连接 —— Health 恒为 OK，绝不用 Health=false 表达"播完了"，
//     否则 Manager（manager.go:349）会置 reconnecting 引发重连风暴。
//  2. 回放时钟 —— 按相邻帧时间戳差 / speed 推进，首帧立即投递。
//  3. 时间戳 —— 帧的 IngestedTs 必须覆写为 PublishedTs，否则 bus.go:472
//     算出的 latencyMs 是"历史时间戳 vs 当前时间"的荒谬差值。
package replay

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/monitorall/monitorall/internal/adapter"
	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/bus"
	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/model"
)

// 回放节奏相关常量（集中定义，禁止散落魔法值）。
const (
	// defaultSpeed 为默认倍速。
	defaultSpeed = 1.0
	// defaultIntervalMs 为文件无时间戳时的默认帧间隔（10Hz）。
	defaultIntervalMs = 100
	// minIntervalMs 为最小帧间隔，防止 0/负间隔导致 Timer 忙转。
	minIntervalMs = 1
	// maxIntervalMs 为单帧最大等待，防止时间戳跳变把回放卡死。
	maxIntervalMs = 10_000
	// maxDefaultHz 为建议限流上限（写入 ChannelSpec.DefaultHz）。
	maxDefaultHz = 60
	// replayHzHeadroom 为限流余量倍数，避免令牌桶间隔正好卡在帧间隔上导致削帧。
	replayHzHeadroom = 2.0
)

// init 注册离线回放适配器工厂。
func init() {
	adapter.Register(model.KindReplay, func(ds *model.DataSource, log *slog.Logger) (adapter.Adapter, error) {
		return New(ds, log)
	})
}

// importLookup 由持久化层（*store.SQLite）实现：把 fileId 解析为导入记录。
// 用窄接口做类型断言，避免 adapter 包反向依赖 store 包。
type importLookup interface {
	GetImport(id string) (*model.Import, error)
}

// Adapter 为离线文件回放适配器。
type Adapter struct {
	ds       *model.DataSource
	log      *slog.Logger
	params   model.ReplayConnParams
	filePath string       // 解析后的绝对磁盘路径
	meta     model.Import // 导入记录（可能为零值，此时按默认节奏回放）

	mu       sync.Mutex
	started  bool
	lastErr  string
	channels map[string]*replayChannel

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// replayChannel 为一个回放通道的运行态。
type replayChannel struct {
	id     string
	spec   adapter.ChannelSpec
	emit   adapter.Emit
	params model.ReplayConnParams
	cancel context.CancelFunc
}

// New 构造离线回放适配器。
func New(ds *model.DataSource, log *slog.Logger) (*Adapter, error) {
	if ds == nil {
		return nil, apperr.New(apperr.InvalidParam, "数据源为空")
	}
	params, err := adapter.ParamsTo[model.ReplayConnParams](ds.ConnParams)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(params.FileID) == "" {
		return nil, apperr.New(apperr.InvalidParam, "fileId 不能为空")
	}
	if params.Speed <= 0 {
		params.Speed = defaultSpeed
	}

	a := &Adapter{
		ds:       ds,
		log:      log,
		params:   params,
		channels: make(map[string]*replayChannel),
	}
	a.filePath, a.meta = resolveImportPath(params.FileID)
	if _, statErr := os.Stat(a.filePath); statErr != nil {
		return nil, apperr.Newf(apperr.InvalidParam, "导入文件不存在或不可读: %s", a.filePath)
	}
	return a, nil
}

// resolveImportPath 把 fileId 解析成绝对路径：优先查 imports 表拿 relPath，
// 查不到时把 fileId 本身当作相对路径（便于测试与手工配置）。
func resolveImportPath(fileID string) (string, model.Import) {
	var (
		relPath = fileID
		meta    model.Import
	)
	dataDir := config.DefaultDataDir
	if env := adapter.EnvOf(); env != nil {
		if env.Cfg != nil && strings.TrimSpace(env.Cfg.Server.DataDir) != "" {
			dataDir = env.Cfg.Server.DataDir
		}
		if env.Store != nil {
			if lk, ok := env.Store.(importLookup); ok {
				if imp, err := lk.GetImport(fileID); err == nil && imp != nil {
					relPath, meta = imp.RelPath, *imp
				}
			}
		}
	}
	abs := relPath
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(dataDir, relPath)
	}
	return filepath.Clean(abs), meta
}

// DataSourceID 返回数据源 ID。
func (a *Adapter) DataSourceID() string { return a.ds.ID }

// Start 标记启动（回放无外部连接，实际工作在各通道的协程）；幂等。
func (a *Adapter) Start(ctx context.Context) error {
	a.mu.Lock()
	if a.started {
		a.mu.Unlock()
		return nil
	}
	a.started = true
	a.ctx, a.cancel = context.WithCancel(context.Background())
	a.mu.Unlock()
	return nil
}

// Health 恒返回 OK：回放是本地文件读取，不存在"连接断开"语义。
// 播完由 StartChannel 协程发 channelStatus(idle) 表达，不走重连路径。
func (a *Adapter) Health(ctx context.Context) adapter.Health {
	return adapter.Health{OK: true, LatencyMs: 0, Detail: "replay"}
}

// ListChannels 返回该回放源的通道（1 个，名字由文件名派生）。
// 读取首条记录推断 payloadType；失败返回 error，由上层 fallbackSpecs 软降级。
func (a *Adapter) ListChannels(ctx context.Context) ([]adapter.ChannelSpec, error) {
	pt, err := a.peekPayloadType()
	if err != nil {
		return nil, err
	}
	meta := map[string]any{
		model.MetaReplayFileID:     a.params.FileID,
		model.MetaReplaySpeed:      a.params.Speed,
		model.MetaReplayLoop:       a.params.Loop,
		model.MetaReplayFrameCount: a.meta.FrameCount,
	}
	if a.params.TimePath != "" {
		meta[model.MetaReplayTimePath] = a.params.TimePath
	}
	if a.params.JSONPath != "" {
		meta[model.MetaReplayJSONPath] = a.params.JSONPath
	}
	return []adapter.ChannelSpec{{
		Name:        channelNameOf(a.filePath),
		PayloadType: pt,
		Meta:        meta,
		DefaultHz:   a.estimatedHz(),
	}}, nil
}

// peekPayloadType 只解析首条记录用于类型推断（不全量扫描）。
func (a *Adapter) peekPayloadType() (model.PayloadType, error) {
	f, err := os.Open(a.filePath)
	if err != nil {
		return "", apperr.Wrap(err, apperr.AdapterError, "打开导入文件失败")
	}
	defer f.Close()
	pt := model.PayloadJSON
	got := false
	err = iterateRecords(f, func(v any) error {
		if val, ok := pick(v, a.params.JSONPath); ok {
			v = val
		}
		pt = inferPayloadType(v)
		got = true
		return errStop
	})
	if err != nil && err != errStop {
		return "", apperr.Wrap(err, apperr.AdapterError, "解析导入文件失败")
	}
	if !got {
		return "", apperr.New(apperr.AdapterError, "导入文件为空或无有效记录")
	}
	return pt, nil
}

// errStop 用于提前终止遍历（不是错误）。
var errStop = errStopType{}

type errStopType struct{}

func (errStopType) Error() string { return "stop iteration" }

// estimatedHz 由导入记录的时间跨度估算帧率，作为 ChannelSpec.DefaultHz（即通道限流上限）。
// 返回 0 表示交给总线全局默认（bus.defaultMaxHz）。
//
// 注意：这里刻意留了 2 倍余量（replayHzHeadroom）。若直接取实际帧率，
// 令牌桶间隔会正好等于帧间隔，抖动即导致约一半的帧被判为超限丢弃；
// 留余量后限流只作为兜底，正常回放不会被削帧。
func (a *Adapter) estimatedHz() float64 {
	if a.meta.FrameCount > 1 && a.meta.LastTs > a.meta.FirstTs {
		hz := float64(a.meta.FrameCount-1) * 1000.0 / float64(a.meta.LastTs-a.meta.FirstTs)
		hz *= replayHzHeadroom
		if hz > maxDefaultHz {
			hz = maxDefaultHz
		}
		if hz < 1 {
			hz = 1
		}
		return hz
	}
	return 0
}

// fixedIntervalMs 返回"文件无时间戳"时使用的固定帧间隔。
func (a *Adapter) fixedIntervalMs() int64 {
	if hz := a.estimatedHz(); hz > 0 {
		d := int64(1000.0 / hz)
		if d < minIntervalMs {
			d = minIntervalMs
		}
		return d
	}
	return defaultIntervalMs
}

// StartChannel 启动该通道的回放（幂等）。首帧立即投递，其后按帧间时间差推进。
func (a *Adapter) StartChannel(ctx context.Context, channelID string, spec adapter.ChannelSpec, emit adapter.Emit) error {
	a.mu.Lock()
	if _, ok := a.channels[channelID]; ok {
		a.mu.Unlock()
		return nil
	}
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	chCtx, cancel := context.WithCancel(parent)
	a.channels[channelID] = &replayChannel{
		id:     channelID,
		spec:   spec,
		emit:   emit,
		params: a.params,
		cancel: cancel,
	}
	a.mu.Unlock()

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				a.log.Error("回放协程 panic", "channelId", channelID, "panic", r)
			}
		}()
		a.run(chCtx, channelID, spec, emit)
	}()
	return nil
}

// run 回放主循环：逐条解码 → 计算虚拟时钟 → 构造帧 → emit → 按 delta/speed 等待。
func (a *Adapter) run(ctx context.Context, channelID string, spec adapter.ChannelSpec, emit adapter.Emit) {
	idx := 0
	prevTs := int64(0)
	emitted := 0
	// 虚拟时钟基准：优先用导入记录的首帧时间戳，否则用本次启动时刻。
	baseTs := a.meta.FirstTs
	if baseTs <= 0 {
		baseTs = model.NowMs()
	}
	fixed := a.fixedIntervalMs()

	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}

	for {
		if ctx.Err() != nil {
			return
		}
		f, err := os.Open(a.filePath)
		if err != nil {
			a.recordErr(err.Error())
			return
		}
		loopErr := a.playOnce(ctx, f, channelID, spec, emit, &idx, &prevTs, &emitted, baseTs, fixed, timer)
		_ = f.Close()
		if loopErr != nil {
			a.recordErr(loopErr.Error())
			return
		}
		if !a.params.Loop {
			// 播完：发 idle 状态，绝不用 Health=false 表达（避免重连风暴）
			a.publishIdle(channelID)
			a.log.Info("回放结束", "channelId", channelID, "frames", emitted)
			return
		}
		a.log.Debug("回放循环，从头再来", "channelId", channelID, "frames", emitted)
	}
}

// playOnce 完整播一遍文件；返回解析过程中遇到的错误（nil 表示正常播完或被取消）。
func (a *Adapter) playOnce(ctx context.Context, f *os.File, channelID string, spec adapter.ChannelSpec,
	emit adapter.Emit, idx *int, prevTs *int64, emitted *int, baseTs, fixed int64, timer *time.Timer) error {

	var decodeErr error
	err := iterateRecords(f, func(raw any) error {
		if ctx.Err() != nil {
			return errStop
		}
		value := raw
		if v, ok := pick(raw, a.params.JSONPath); ok {
			value = v
		}
		// 时间戳：优先 timePath 指定字段，其次整条记录的 ts 类字段
		ts := int64(0)
		if a.params.TimePath != "" {
			if v, ok := pick(raw, a.params.TimePath); ok {
				ts = extractTs(v)
			}
		}
		if ts == 0 {
			ts = extractTs(raw)
		}
		if ts == 0 {
			ts = baseTs + int64(*idx)*fixed
		}

		// 首帧立即投递；其后按与上一帧的时间差推进回放时钟
		if *emitted > 0 {
			delta := ts - *prevTs
			if delta <= 0 {
				delta = fixed
			}
			d := time.Duration(float64(delta)/a.params.Speed) * time.Millisecond
			if d < minIntervalMs*time.Millisecond {
				d = minIntervalMs * time.Millisecond
			}
			if d > maxIntervalMs*time.Millisecond {
				d = maxIntervalMs * time.Millisecond
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(d)
			select {
			case <-ctx.Done():
				return errStop
			case <-timer.C:
			}
			if ctx.Err() != nil {
				return errStop
			}
		}

		payload, hints := buildPayload(spec.PayloadType, value, ts)
		fr, ferr := model.NewFrame(channelID, spec.PayloadType, payload, ts)
		if ferr != nil {
			return ferr
		}
		// 关键修正：回放帧的"接收时刻"对齐虚拟回放时钟，
		// 否则 bus.go:472 的 latencyMs = ingested - published 会是历史时间与当前的巨大差值
		fr.IngestedTs = fr.PublishedTs
		if len(hints) > 0 {
			fr.SchemaHint = hints
		}
		emit(channelID, fr)

		*idx++
		*prevTs = ts
		*emitted++
		return nil
	})
	if err != nil && err != errStop {
		decodeErr = err
	}
	return decodeErr
}

// publishIdle 播完时广播 idle 状态（不置 error，不触发重连）。
func (a *Adapter) publishIdle(channelID string) {
	env := adapter.EnvOf()
	if env == nil || env.Bus == nil {
		return
	}
	env.Bus.PublishChannelStatus(bus.ChannelStatusEvent{
		ChannelID:   channelID,
		Status:      model.StatusIdle,
		LastFrameAt: model.NowMs(),
	})
}

// recordErr 记录最近错误。
func (a *Adapter) recordErr(msg string) {
	a.mu.Lock()
	a.lastErr = msg
	a.mu.Unlock()
	a.log.Warn("回放失败", "file", a.filePath, "err", msg)
}

// StopChannel 停止该通道的回放（幂等）。
func (a *Adapter) StopChannel(channelID string) error {
	a.mu.Lock()
	ch, ok := a.channels[channelID]
	if ok {
		delete(a.channels, channelID)
	}
	a.mu.Unlock()
	if ok && ch != nil && ch.cancel != nil {
		ch.cancel()
	}
	return nil
}

// Close 停止全部回放并等待退出（带超时保护，可重复调用）。
func (a *Adapter) Close() error {
	a.mu.Lock()
	ids := make([]string, 0, len(a.channels))
	for id := range a.channels {
		ids = append(ids, id)
	}
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
	done := make(chan struct{})
	go func() {
		a.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Duration(config.AdapterCloseTimeoutMs) * time.Millisecond):
		a.log.Error("等待回放适配器 goroutine 退出超时")
	}
	return nil
}

// LastError 返回最近一次错误（非接口方法，供排查使用）。
func (a *Adapter) LastError() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastErr
}

// channelNameOf 由文件路径派生通道名（去扩展名）。
func channelNameOf(path string) string {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	if ext != "" {
		base = base[:len(base)-len(ext)]
	}
	if base == "" || base == "." || base == "/" {
		return "/"
	}
	return base
}
