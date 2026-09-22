// Package video 实现视频数据源适配器：只做 MediaMTX 路径编排与状态查询，
// 产出 video_stream 帧（仅地址与状态，绝不含像素；像素由浏览器直连 MediaMTX）。
package video

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/monitorall/monitorall/internal/adapter"
	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/media"
	"github.com/monitorall/monitorall/internal/model"
)

// init 注册视频适配器工厂。
func init() {
	adapter.Register(model.KindVideo, func(ds *model.DataSource, log *slog.Logger) (adapter.Adapter, error) {
		return New(ds, log)
	})
}

// 视频源状态常量（写入 VideoPayload.State）。
const (
	stateReady    = model.VideoStateReady
	stateNotReady = model.VideoStateNotReady
	stateDegraded = model.VideoStateDegraded
)

// Adapter 为视频数据源适配器（一个 DataSource = 一条 MediaMTX 路径，
// 源可以是 RTMP 也可以是 RTSP 摄像头，或 publisher 接收远端推流）。
type Adapter struct {
	ds     *model.DataSource
	log    *slog.Logger
	cfg    *config.Config
	client media.Client

	mu       sync.Mutex
	info     *media.StreamInfo // pull 模式非空；publish 模式可为空
	path     string            // MediaMTX 路径：解析自流地址或 mediaMtxPath
	mode     string            // pull（拉流）| publish（接收推流）
	params   model.VideoConnParams
	started  bool
	lastErr  string
	channels map[string]*videoChannel

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// videoChannel 为一个通道的轮询协程句柄。
type videoChannel struct {
	id     string
	spec   adapter.ChannelSpec
	emit   adapter.Emit
	cancel context.CancelFunc
}

// New 构造视频适配器。
func New(ds *model.DataSource, log *slog.Logger) (*Adapter, error) {
	if ds == nil {
		return nil, apperr.New(apperr.InvalidParam, "数据源为空")
	}
	cfg := config.DefaultConfig()
	if env := adapter.EnvOf(); env != nil && env.Cfg != nil {
		cfg = env.Cfg
	}
	a := &Adapter{
		ds:       ds,
		log:      log,
		cfg:      cfg,
		client:   media.NewClient(cfg.MediaMTX),
		channels: make(map[string]*videoChannel),
	}
	return a, nil
}

// SetClient 注入 MediaMTX 客户端（单测用 mock 替换真实 HTTP 依赖）。
func (a *Adapter) SetClient(c media.Client) {
	a.mu.Lock()
	a.client = c
	a.mu.Unlock()
}

// DataSourceID 返回数据源 ID。
func (a *Adapter) DataSourceID() string { return a.ds.ID }

// Start 解析流地址（rtmp/rtsp）、注册路径到 MediaMTX 并启动轮询上下文；幂等。
//
// source 规则：publish 模式或本机 RTMP 推流用 publisher；其余一律把完整原始 URL
// （保留 rtsp://user:pass@host:554/... 的凭据）交给 MediaMTX 自行拉流。
func (a *Adapter) Start(ctx context.Context) error {
	params, err := adapter.ParamsTo[model.VideoConnParams](a.ds.ConnParams)
	if err != nil {
		return err
	}
	mode := normalizeMode(params.Mode)
	var info *media.StreamInfo
	if strings.TrimSpace(params.RtmpURL) != "" {
		info, err = media.ParseStreamURL(params.RtmpURL)
		if err != nil {
			return err
		}
	}
	path := streamPath(params, info)
	if path == "" {
		return apperr.New(apperr.InvalidParam, "视频数据源缺少路径：请填写流地址或 mediaMtxPath")
	}
	if params.PreferredProtocol == "" {
		params.PreferredProtocol = config.VideoProtocolWebRTC
	}
	if params.MediaMTXPath == "" {
		params.MediaMTXPath = path
	}

	a.mu.Lock()
	a.params = params
	a.info = info
	a.path = path
	a.mode = mode
	if a.started {
		a.mu.Unlock()
		return nil
	}
	a.started = true
	a.ctx, a.cancel = context.WithCancel(context.Background())
	a.mu.Unlock()

	// embedded 模式：把路径注册进 MediaMTX（远端 rtmp/rtsp 用拉流，本机推流用 publisher）
	if a.cfg != nil && a.cfg.MediaMTX.Mode == config.MediaMTXModeEmbedded && a.client != nil {
		source := a.sourceFor()
		var opts []media.PathOption
		if mode == config.VideoModePull && info != nil && info.IsRTSP() {
			// RTSP 摄像头在 UDP 下易花屏或连不上，默认强制 TCP
			opts = append(opts, media.WithRTSPTransport(rtspTransportOf(params.RTSPTransport)))
		}
		regCtx, cancel := context.WithTimeout(ctx, time.Duration(config.DefaultTimeoutMs)*time.Millisecond)
		defer cancel()
		if err := a.client.PathAdd(regCtx, path, source, opts...); err != nil {
			// 注册失败不致命（路径可能已存在或 API 只读），记录后继续
			a.log.Warn("注册 MediaMTX 路径失败", "path", path, "err", err)
		}
	}
	a.log.Info("视频适配器已启动", "path", path, "mode", mode, "source", a.sourceFor())
	return nil
}

// sourceFor 计算注册给 MediaMTX 的 source：publish 模式或本机推流为 publisher，
// 否则为完整原始 URL（含 RTSP 用户名密码）。
func (a *Adapter) sourceFor() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.mode == config.VideoModePublish || a.isLocalPublishLocked() {
		return "publisher"
	}
	if a.info != nil {
		return a.info.Raw
	}
	return ""
}

// isLocalPublish 判断该地址是否推给本机 MediaMTX。
func (a *Adapter) isLocalPublish() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.isLocalPublishLocked()
}

// isLocalPublishLocked 判断该地址是否是推给本机 MediaMTX 的 RTMP 推流：
// 仅 rtmp/rtmps 可能是推流（RTSP 一律 false），且端口与主机均指向本机。
func (a *Adapter) isLocalPublishLocked() bool {
	if a.cfg == nil || a.info == nil || a.mode == config.VideoModePublish {
		return false
	}
	if !a.info.IsRTMP() {
		return false
	}
	if a.info.Port != a.cfg.MediaMTX.PortRTMP {
		return false
	}
	host := a.info.Host
	return host == "127.0.0.1" || host == "localhost" ||
		(a.cfg.Server.LANHost != "" && host == a.cfg.Server.LANHost)
}

// streamPath 计算 MediaMTX 路径：mediaMtxPath 优先，否则取自流地址。
func streamPath(params model.VideoConnParams, info *media.StreamInfo) string {
	if p := strings.Trim(strings.TrimSpace(params.MediaMTXPath), "/"); p != "" {
		return p
	}
	if info != nil {
		return info.Path
	}
	return ""
}

// normalizeMode 归一化接入模式，缺省 pull。
func normalizeMode(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), config.VideoModePublish) {
		return config.VideoModePublish
	}
	return config.VideoModePull
}

// rtspTransportOf 归一化 RTSP 传输方式，非法值回落 TCP。
func rtspTransportOf(v string) string {
	t := strings.ToLower(strings.TrimSpace(v))
	switch t {
	case config.RTSPTransportTCP, config.RTSPTransportUDP, config.RTSPTransportAuto:
		return t
	default:
		return config.RTSPTransportTCP
	}
}

// publicHost 返回对外的 MediaMTX 主机名（用于拼推流地址）。
func (a *Adapter) publicHost() string {
	if a.cfg != nil {
		return a.cfg.MediaMTX.PublicHost
	}
	return ""
}

// Health 探测 MediaMTX API 可达性（路径未 ready 不算失败，由帧的 state 表达）。
func (a *Adapter) Health(ctx context.Context) adapter.Health {
	start := time.Now()
	a.mu.Lock()
	client := a.client
	path := a.path
	a.mu.Unlock()
	if client == nil {
		return adapter.Health{OK: false, Detail: "MediaMTX 客户端未初始化"}
	}
	if _, err := client.PathsList(ctx, path); err != nil {
		a.mu.Lock()
		a.lastErr = err.Error()
		a.mu.Unlock()
		return adapter.Health{OK: false, LatencyMs: time.Since(start).Milliseconds(), Detail: err.Error()}
	}
	return adapter.Health{OK: true, LatencyMs: time.Since(start).Milliseconds(), Detail: "ok"}
}

// ListChannels 返回该视频源的通道（固定 1 个：MediaMTX path）。
func (a *Adapter) ListChannels(ctx context.Context) ([]adapter.ChannelSpec, error) {
	a.mu.Lock()
	info := a.info
	path := a.path
	mode := a.mode
	transport := a.params.RTSPTransport
	a.mu.Unlock()
	if path == "" {
		params, err := adapter.ParamsTo[model.VideoConnParams](a.ds.ConnParams)
		if err != nil {
			return nil, err
		}
		mode = normalizeMode(params.Mode)
		transport = params.RTSPTransport
		if strings.TrimSpace(params.RtmpURL) != "" {
			info, err = media.ParseStreamURL(params.RtmpURL)
			if err != nil {
				return nil, err
			}
		}
		path = streamPath(params, info)
		if path == "" {
			return nil, apperr.New(apperr.InvalidParam, "视频数据源缺少路径：请填写流地址或 mediaMtxPath")
		}
	}
	meta := map[string]any{
		model.MetaMediaMTXPath:   path,
		model.MetaVideoMode:      mode,
		model.MetaIsLocalPublish: a.isLocalPublish(),
	}
	if info != nil {
		meta[model.MetaRtmpURL] = info.Raw
		if info.IsRTSP() {
			meta[model.MetaRTSPTransport] = rtspTransportOf(transport)
		}
	}
	if mode == config.VideoModePublish {
		// publish 模式：把可推流地址放进通道 meta，供前端展示给用户复制
		for k, v := range media.BuildPublishURLs(a.cfg, a.publicHost(), path) {
			switch k {
			case config.PublishProtocolWHIP:
				meta[model.MetaPublishURL] = v
			case config.PublishProtocolRTMP:
				meta[model.MetaPublishRTMPURL] = v
			}
		}
	}
	spec := adapter.ChannelSpec{
		Name:        path,
		PayloadType: model.PayloadVideoStream,
		Meta:        meta,
		DefaultHz:   a.videoStatusHz(),
	}
	return []adapter.ChannelSpec{spec}, nil
}

// videoStatusHz 返回视频状态帧频率。
func (a *Adapter) videoStatusHz() float64 {
	if a.cfg != nil && a.cfg.Bus.VideoStatusHz > 0 {
		return a.cfg.Bus.VideoStatusHz
	}
	return config.DefaultVideoStatusHz
}

// StartChannel 启动该通道的状态轮询协程（幂等）。
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
	a.channels[channelID] = &videoChannel{id: channelID, spec: spec, emit: emit, cancel: cancel}
	client := a.client
	params := a.params
	info := a.info
	path := a.path
	a.mu.Unlock()

	interval := time.Duration(config.MediaMTXPathPollMs) * time.Millisecond
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				a.log.Error("视频轮询 panic", "channelId", channelID, "panic", r)
			}
		}()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		a.emitStatus(chCtx, channelID, client, params, path, info, emit)
		for {
			select {
			case <-chCtx.Done():
				return
			case <-ticker.C:
				a.emitStatus(chCtx, channelID, client, params, path, info, emit)
			}
		}
	}()
	return nil
}

// emitStatus 查询一次路径状态并产出一帧 video_stream（只含地址与状态，无像素）。
func (a *Adapter) emitStatus(ctx context.Context, channelID string, client media.Client,
	params model.VideoConnParams, path string, info *media.StreamInfo, emit adapter.Emit) {
	if emit == nil || path == "" {
		return
	}
	ready := false
	readers := 0
	var bytesPerSec int64
	apiOK := true

	if client != nil {
		qCtx, cancel := context.WithTimeout(ctx, time.Duration(config.DefaultTimeoutMs)*time.Millisecond)
		paths, err := client.PathsList(qCtx, path)
		cancel()
		if err != nil {
			apiOK = false
			a.mu.Lock()
			a.lastErr = err.Error()
			a.mu.Unlock()
			a.log.Warn("查询 MediaMTX 路径失败", "path", path, "err", err)
		} else if len(paths) > 0 {
			p := paths[0]
			ready = p.Ready
			readers = p.Readers
			bytesPerSec = p.BytesSent
		}
	}

	state := stateNotReady
	if !apiOK {
		state = stateNotReady
	} else if ready {
		state = stateReady
	}

	publicHost := ""
	if a.cfg != nil {
		publicHost = a.cfg.MediaMTX.PublicHost
	}
	urls := media.BuildPlayURLs(a.cfg, publicHost, path)
	protocol := media.PickProtocol(params.PreferredProtocol, urls)
	if _, ok := urls[config.VideoProtocolWebRTC]; !ok {
		protocol = config.VideoProtocolHLS
	}

	sourceURL := ""
	if info != nil {
		sourceURL = info.Raw
	}
	payload := model.VideoPayload{
		Protocol:    protocol,
		State:       state,
		URLs:        urls,
		SourceURL:   sourceURL,
		Path:        path,
		Ready:       ready,
		Readers:     readers,
		BytesPerSec: bytesPerSec,
	}
	if protocol != params.PreferredProtocol {
		// 首选协议不可用时给出降级标记（前端据此显示角标）
		payload.DegradedFrom = params.PreferredProtocol
		payload.DegradedReason = "preferred protocol unavailable"
		payload.State = stateDegraded
		payload.RetryInSec = config.DefaultRetryPreferredSec // 默认 30s 后重试首选协议
	}
	f, err := model.NewFrame(channelID, model.PayloadVideoStream, payload, model.NowMs())
	if err != nil {
		a.log.Error("构造视频帧失败", "err", err)
		return
	}
	emit(channelID, f)
}

// StopChannel 停止该通道的轮询（幂等）。
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

// Close 释放全部协程与资源；可重复调用，带 10s 超时保护（G2）。
func (a *Adapter) Close() error {
	a.mu.Lock()
	ids := make([]string, 0, len(a.channels))
	for id := range a.channels {
		ids = append(ids, id)
	}
	a.mu.Unlock()
	for _, id := range ids {
		_ = a.StopChannel(id)
	}
	a.mu.Lock()
	cancel := a.cancel
	a.cancel = nil
	a.started = false
	a.mu.Unlock()
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
		a.log.Error("等待视频适配器 goroutine 退出超时")
	}
	return nil
}

// LastError 返回最近一次错误（诊断用）。
func (a *Adapter) LastError() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastErr
}

// String 返回适配器可读描述（日志用）。
func (a *Adapter) String() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.info == nil {
		if a.path != "" {
			return "video://" + a.path
		}
		return "video://" + a.ds.ID
	}
	return "video://" + a.info.Host + ":" + strconv.Itoa(a.info.Port) + "/" + a.path
}
