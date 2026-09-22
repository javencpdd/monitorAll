package video

import (
	"context"
	"fmt"
	"io"
	"log/slog"
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
}

func (r *recordClient) PathsList(ctx context.Context, path string) ([]media.MediaMTXPath, error) {
	r.listedPath = path
	return []media.MediaMTXPath{{Name: path, Ready: true, Readers: 1}}, nil
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
	const raw = "rtsp://admin:okwy1688@192.168.2.69:554/Streaming/Channels/101"
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
