package video

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/media"
	"github.com/monitorall/monitorall/internal/model"
)

// recordClient 记录 PathAdd 实际收到的参数（用于链路自证）。
type recordClient struct {
	path       string
	source     string
	extra      map[string]any
	addCalled  bool
	listedPath string

	// readyPtr 为可配置的 PathsList 返回值 Ready；nil = 沿用旧行为（恒 true），
	// 这样既有用例一行都不用改。指向 false 用于模拟「路径在但没收到流」。
	readyPtr *bool
	// listErr 非空时 PathsList 直接返回该错误（模拟 MediaMTX / 管理 API 不通）。
	listErr error
}

// setReady 让 PathsList 返回指定的 Ready 值（不调用则恒为 true，保持旧行为）。
func (r *recordClient) setReady(v bool) *recordClient {
	r.readyPtr = &v
	return r
}

// setListErr 让 PathsList 返回错误（模拟 MediaMTX 服务未运行 / API 不通）。
func (r *recordClient) setListErr(err error) *recordClient {
	r.listErr = err
	return r
}

func (r *recordClient) PathsList(ctx context.Context, path string) ([]media.MediaMTXPath, error) {
	r.listedPath = path
	if r.listErr != nil {
		return nil, r.listErr
	}
	ready := true
	if r.readyPtr != nil {
		ready = *r.readyPtr
	}
	return []media.MediaMTXPath{{Name: path, Ready: ready, Readers: 1}}, nil
}

func (r *recordClient) PathAdd(ctx context.Context, path string, source string, opts ...media.PathOption) error {
	r.addCalled = true
	r.path = path
	r.source = source
	r.extra = map[string]any{}
	for _, opt := range opts {
		if opt != nil {
			opt(r.extra)
		}
	}
	return nil
}

func (r *recordClient) Health(ctx context.Context) error { return nil }

func newAdapter(t *testing.T, params map[string]any) (*Adapter, *recordClient) {
	t.Helper()
	ds := model.NewDataSource("hik", model.KindVideo, model.ProtoRTSP, params)
	a, err := New(ds, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("构造适配器失败: %v", err)
	}
	rc := &recordClient{}
	a.SetClient(rc)
	return a, rc
}

// TestStartRTSPCamera 走完「建视频数据源 → 适配器 Start → MediaMTX PathAdd」链路，
// 自证 RTSP 摄像头地址被完整（含用户名密码）交给 MediaMTX，且不会被误判成本机推流。
func TestStartRTSPCamera(t *testing.T) {
	const raw = "rtsp://admin:your-password@192.168.2.69:554/Streaming/Channels/101"
	a, rc := newAdapter(t, map[string]any{"rtmpUrl": raw})
	defer func() { _ = a.Close() }()

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	fmt.Println("=== RTSP 拉流链路 ===")
	fmt.Println("输入 rtmpUrl:", raw)
	fmt.Println("PathAdd path       :", rc.path)
	fmt.Println("PathAdd source     :", rc.source)
	fmt.Println("PathAdd rtspTransport:", rc.extra["rtspTransport"])
	fmt.Println("isLocalPublish     :", a.isLocalPublish())

	if !rc.addCalled {
		t.Fatal("PathAdd 未被调用")
	}
	if rc.path != "Streaming/Channels/101" {
		t.Fatalf("path 错误: %q", rc.path)
	}
	if rc.source != raw {
		t.Fatalf("source 必须是完整原始 RTSP URL（含凭据）: %q", rc.source)
	}
	if rc.extra["rtspTransport"] != config.RTSPTransportTCP {
		t.Fatalf("RTSP 默认应走 tcp: %+v", rc.extra)
	}
	if a.isLocalPublish() {
		t.Fatal("RTSP 地址不应被判定为本机推流")
	}

	specs, err := a.ListChannels(context.Background())
	if err != nil {
		t.Fatalf("ListChannels 失败: %v", err)
	}
	if len(specs) != 1 || specs[0].Name != "Streaming/Channels/101" {
		t.Fatalf("通道名错误: %+v", specs)
	}
	fmt.Println("ListChannels name  :", specs[0].Name)
	fmt.Println("ListChannels meta  :", specs[0].Meta)
	if specs[0].Meta[model.MetaRtmpURL] != raw {
		t.Fatalf("meta 源地址错误: %+v", specs[0].Meta)
	}
	if specs[0].Meta[model.MetaVideoMode] != config.VideoModePull {
		t.Fatalf("meta 接入模式错误: %+v", specs[0].Meta)
	}
	if _, ok := specs[0].Meta[model.MetaPublishURL]; ok {
		t.Fatalf("pull 模式不应产出推流地址: %+v", specs[0].Meta)
	}

	// 状态帧的 SourceURL 也应保留凭据（轮询协程首帧异步产出，这里等一帧）
	gotCh := make(chan string, 1)
	_ = a.StartChannel(context.Background(), specs[0].Name, specs[0],
		func(channelID string, f model.Frame) {
			var p model.VideoPayload
			if err := f.Decode(&p); err == nil {
				select {
				case gotCh <- p.SourceURL:
				default:
				}
			}
		})
	var got string
	select {
	case got = <-gotCh:
	case <-time.After(3 * time.Second):
		t.Fatal("未收到视频状态帧")
	}
	fmt.Println("帧 SourceURL       :", got)
	if got != raw {
		t.Fatalf("帧 SourceURL 错误: %q", got)
	}
}

// TestStartLocalRTMPPublish 校验本机 RTMP 推流仍识别为 publisher，而 RTSP 一律不是。
func TestStartLocalRTMPPublish(t *testing.T) {
	cases := []struct {
		name   string
		url    string
		source string
	}{
		{"本机 RTMP 推流", "rtmp://127.0.0.1:1936/live/lite3", "publisher"},
		{"RTSP 即使端口相同也不是推流", "rtsp://127.0.0.1:1936/live/lite3", "rtsp://127.0.0.1:1936/live/lite3"},
	}
	for _, c := range cases {
		a, rc := newAdapter(t, map[string]any{"rtmpUrl": c.url})
		if err := a.Start(context.Background()); err != nil {
			t.Fatalf("%s: Start 失败: %v", c.name, err)
		}
		fmt.Printf("=== %s ===\n  source=%s isLocalPublish=%v\n", c.name, rc.source, a.isLocalPublish())
		if rc.source != c.source {
			t.Fatalf("%s: source 期望 %q，实际 %q", c.name, c.source, rc.source)
		}
		_ = a.Close()
	}
}

// TestStartPublishMode 校验 publish（接收推流）模式：source 固定 publisher，并给出推流地址。
func TestStartPublishMode(t *testing.T) {
	a, rc := newAdapter(t, map[string]any{
		"mode":         "publish",
		"mediaMtxPath": "live/whip1",
	})
	defer func() { _ = a.Close() }()
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	specs, err := a.ListChannels(context.Background())
	if err != nil {
		t.Fatalf("ListChannels 失败: %v", err)
	}
	fmt.Println("=== publish（接收推流）模式 ===")
	fmt.Println("PathAdd path   :", rc.path)
	fmt.Println("PathAdd source :", rc.source)
	fmt.Println("meta           :", specs[0].Meta)
	if rc.source != "publisher" {
		t.Fatalf("publish 模式 source 应为 publisher: %q", rc.source)
	}
	cfg := config.DefaultConfig()
	wantWHIP := cfg.PublicScheme() + "://127.0.0.1:8889/live/whip1/whip"
	if specs[0].Meta[model.MetaPublishURL] != wantWHIP {
		t.Fatalf("WHIP 推流地址错误: %v", specs[0].Meta[model.MetaPublishURL])
	}
	if specs[0].Meta[model.MetaPublishRTMPURL] != "rtmp://127.0.0.1:1936/live/whip1" {
		t.Fatalf("RTMP 推流地址错误: %v", specs[0].Meta[model.MetaPublishRTMPURL])
	}
}

// ————————————————— 视频异常诊断下发（Detail / PublishURL） —————————————————

// captureStatus 调一次 emitStatus 并取回它产出的 VideoPayload。
// emit 是同步回调，不需要像 StartChannel 那样等协程首帧。
func captureStatus(t *testing.T, a *Adapter, client media.Client,
	params model.VideoConnParams, path string, info *media.StreamInfo) model.VideoPayload {
	t.Helper()
	var got model.VideoPayload
	called := false
	emit := func(_ string, f model.Frame) {
		called = true
		if err := f.Decode(&got); err != nil {
			t.Fatalf("解码视频帧失败: %v", err)
		}
	}
	a.emitStatus(context.Background(), "ch-1", client, params, path, info, emit)
	if !called {
		t.Fatal("emitStatus 未产出帧")
	}
	return got
}

// mustInfo 解析流地址，失败即让用例失败。
func mustInfo(t *testing.T, raw string) *media.StreamInfo {
	t.Helper()
	info, err := media.ParseStreamURL(raw)
	if err != nil {
		t.Fatalf("解析 %q 失败: %v", raw, err)
	}
	return info
}

// TestEmitStatusAPIFailure 管理 API 调不通 → Detail 必须点明 MediaMTX 与管理地址，
// 且带上底层错误；pull 模式不应给推流地址。
func TestEmitStatusAPIFailure(t *testing.T) {
	a, rc := newAdapter(t, map[string]any{"rtmpUrl": "rtmp://172.31.68.227:1936/live/lite3"})
	defer func() { _ = a.Close() }()
	rc.setListErr(errors.New("connection refused"))
	info := mustInfo(t, "rtmp://172.31.68.227:1936/live/lite3")

	got := captureStatus(t, a, rc,
		model.VideoConnParams{RtmpURL: info.Raw, PreferredProtocol: config.VideoProtocolWebRTC},
		"live/lite3", info)
	if !strings.Contains(got.Detail, "MediaMTX") {
		t.Fatalf("Detail 应点明 MediaMTX: %q", got.Detail)
	}
	if !strings.Contains(got.Detail, "connection refused") {
		t.Fatalf("Detail 应带上底层错误: %q", got.Detail)
	}
	if got.State != model.VideoStateNotReady {
		t.Fatalf("API 失败应为 not_ready: %q", got.State)
	}
	if got.PublishURL != "" {
		t.Fatalf("pull 模式不应给推流地址: %q", got.PublishURL)
	}
}

// TestEmitStatusPullNotReady 拉流模式没收到流 → Detail 必须说清是「上游拉不到」，
// 并引导去查推流方；同时严禁把带凭据的源地址塞进 Detail。
func TestEmitStatusPullNotReady(t *testing.T) {
	a, rc := newAdapter(t, map[string]any{"rtmpUrl": "rtsp://admin:your-password@192.168.2.69:554/Streaming/Channels/101"})
	defer func() { _ = a.Close() }()
	rc.setReady(false)
	info := mustInfo(t, "rtsp://admin:your-password@192.168.2.69:554/Streaming/Channels/101")

	got := captureStatus(t, a, rc, model.VideoConnParams{RtmpURL: info.Raw}, "Streaming/Channels/101", info)
	if got.Detail == "" {
		t.Fatal("拉不到流时 Detail 不应为空")
	}
	if !strings.Contains(got.Detail, "拉取") && !strings.Contains(got.Detail, "上游") {
		t.Fatalf("Detail 应说明是上游拉取问题: %q", got.Detail)
	}
	// 凭据泄露硬要求：Detail 经 WS 推给所有订阅该通道的浏览器
	if strings.Contains(got.Detail, "your-password") {
		t.Fatalf("Detail 泄露了源地址明文口令: %q", got.Detail)
	}
	// 具体地址仍由 SourceURL 提供（前端负责脱敏展示），Detail 保持泛化
	if !strings.Contains(got.SourceURL, "your-password") {
		t.Fatalf("SourceURL 仍应保留原始地址: %q", got.SourceURL)
	}
	if got.PublishURL != "" {
		t.Fatalf("pull 模式不应给推流地址: %q", got.PublishURL)
	}
}

// TestEmitStatusPublishNotReady publish 模式没人推流 → Detail 提示核对推流地址与流名，
// 且必须给出推流方该推的 RTMP 地址（用户原话：有提示报错吗，例如请检查推流方是否推流到本机 IP）。
func TestEmitStatusPublishNotReady(t *testing.T) {
	a, rc := newAdapter(t, map[string]any{"mode": "publish", "mediaMtxPath": "live/whip1"})
	defer func() { _ = a.Close() }()
	rc.setReady(false)

	// 白盒指定配置，断言 BuildPublishURLs 用的 host / 端口
	cfg := config.DefaultConfig()
	cfg.MediaMTX.PortRTMP = 1936
	cfg.Server.LANHost = "192.168.1.50"
	a.cfg = cfg

	got := captureStatus(t, a, rc, model.VideoConnParams{Mode: config.VideoModePublish}, "live/whip1", nil)
	if !strings.Contains(got.Detail, "推流") {
		t.Fatalf("publish 模式 Detail 应提示推流: %q", got.Detail)
	}
	want := "rtmp://192.168.1.50:1936/live/whip1"
	if got.PublishURL != want {
		t.Fatalf("PublishURL 应为 %q，实际 %q", want, got.PublishURL)
	}
}

// TestEmitStatusReadyDetailEmpty ready=true 时 Detail 必须为空。
// 同时覆盖「首选协议不可用触发 degraded」的分支：degraded 是播放质量问题，
// 不是「没画面」，Detail 不能被覆盖成异常文案。
func TestEmitStatusReadyDetailEmpty(t *testing.T) {
	a, rc := newAdapter(t, map[string]any{"rtmpUrl": "rtmp://172.31.68.227:1936/live/lite3"})
	defer func() { _ = a.Close() }()
	info := mustInfo(t, "rtmp://172.31.68.227:1936/live/lite3")

	// preferred 填 flv：urls 里没有 flv → 协议降级 → State=degraded
	got := captureStatus(t, a, rc,
		model.VideoConnParams{RtmpURL: info.Raw, PreferredProtocol: config.VideoProtocolFLV},
		"live/lite3", info)
	if !got.Ready {
		t.Fatal("mock 默认应 ready")
	}
	if got.State != model.VideoStateDegraded {
		t.Fatalf("应为 degraded，实际 %q", got.State)
	}
	if got.Detail != "" {
		t.Fatalf("ready 时 Detail 必须为空（degraded 也不例外）: %q", got.Detail)
	}
	if got.PublishURL != "" {
		t.Fatalf("pull 模式不应给推流地址: %q", got.PublishURL)
	}
}

// TestEmitStatusClientNil MediaMTX 客户端未初始化 → Detail 明确说「未初始化」，
// 而不是含糊的「未收到流」（这是后端自身编排问题，用户无法自查）。
func TestEmitStatusClientNil(t *testing.T) {
	a, _ := newAdapter(t, map[string]any{"rtmpUrl": "rtmp://172.31.68.227:1936/live/lite3"})
	defer func() { _ = a.Close() }()
	info := mustInfo(t, "rtmp://172.31.68.227:1936/live/lite3")

	got := captureStatus(t, a, nil, model.VideoConnParams{RtmpURL: info.Raw}, "live/lite3", info)
	if !strings.Contains(got.Detail, "未初始化") {
		t.Fatalf("client 为空时 Detail 应说明未初始化: %q", got.Detail)
	}
}
