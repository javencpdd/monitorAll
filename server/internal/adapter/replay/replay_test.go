package replay

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monitorall/monitorall/internal/adapter"
	"github.com/monitorall/monitorall/internal/bus"
	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/logx"
	"github.com/monitorall/monitorall/internal/model"
)

// testLogger 返回一个测试用日志器。
func testLogger() *slog.Logger { return logx.With("module", "replay_test") }

// testEnv 装配一个最小的运行时环境（临时 dataDir + 真实总线）。
func testEnv(t *testing.T) (*config.Config, *bus.Bus) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Server.DataDir = t.TempDir()
	b := bus.New(&cfg.Bus)
	adapter.Setup(&adapter.Env{Cfg: cfg, Bus: b, Store: nil})
	return cfg, b
}

// writeFile 在临时目录写一个回放文件。
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("写测试文件失败: %v", err)
	}
	return p
}

// ————————————————— 解析器：三种文件形态 —————————————————

func TestIterateRecordsForms(t *testing.T) {
	ndjson := "{\"a\":1}\n{\"a\":2}\n{\"a\":3}\n"
	array := "[{\"a\":1},{\"a\":2},{\"a\":3}]"
	single := "{\n  \"a\": 1\n}"
	pretty := "[\n  {\"a\":1},\n  {\"a\":2}\n]"

	cases := []struct {
		name string
		in   string
		want int
	}{
		{"ndjson", ndjson, 3},
		{"array", array, 3},
		{"single_object", single, 1},
		{"pretty_array", pretty, 2},
	}
	for _, tc := range cases {
		n := 0
		err := iterateRecords(strings.NewReader(tc.in), func(v any) error {
			m, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("%s: 记录不是对象: %T", tc.name, v)
			}
			if _, ok := m["a"]; !ok {
				t.Fatalf("%s: 缺少字段 a: %v", tc.name, m)
			}
			n++
			return nil
		})
		if err != nil {
			t.Fatalf("%s: iterateRecords 失败: %v", tc.name, err)
		}
		if n != tc.want {
			t.Fatalf("%s: 期望 %d 条记录，实际 %d", tc.name, tc.want, n)
		}
	}
}

// ————————————————— 时间戳归一化 —————————————————

func TestNormalizeTs(t *testing.T) {
	cases := []struct {
		in   float64
		want int64
	}{
		{1758000000, 1758000000000},          // 秒
		{1758000000123, 1758000000123},       // 毫秒
		{1758000000123456, 1758000000123},    // 微秒
		{1758000000123456789, 1758000000123}, // 纳秒
	}
	for _, tc := range cases {
		if got := normalizeTs(tc.in); got != tc.want {
			t.Fatalf("normalizeTs(%v) = %d, 期望 %d", tc.in, got, tc.want)
		}
	}
}

// ————————————————— 经纬度识别 —————————————————

func TestInferPayloadTypeGeo(t *testing.T) {
	geo := map[string]any{"pose": map[string]any{
		"latitude":  31.2304,
		"longitude": 121.4737,
		"altitude":  12.5,
	}}
	if pt := inferPayloadType(geo); pt != model.PayloadGeoPose {
		t.Fatalf("嵌套 lat/lon 应判为 geo_pose，实际 %s", pt)
	}
	plain := map[string]any{"x": 1.0, "y": 2.0}
	if pt := inferPayloadType(plain); pt != model.PayloadTimeSeries {
		t.Fatalf("全数值对象应为 time_series_sample，实际 %s", pt)
	}
}

// ————————————————— ScanFile 统计 —————————————————

func TestScanFile(t *testing.T) {
	_, _ = testEnv(t)
	dir := t.TempDir()
	p := writeFile(t, dir, "track.jsonl",
		"{\"t\":1700000000000,\"pose\":{\"latitude\":31.1,\"longitude\":121.1}}\n"+
			"{\"t\":1700000001000,\"pose\":{\"latitude\":31.2,\"longitude\":121.2}}\n"+
			"{\"t\":1700000002000,\"pose\":{\"latitude\":31.3,\"longitude\":121.3}}\n")
	n, first, last, err := ScanFile(p, "t")
	if err != nil {
		t.Fatalf("ScanFile 失败: %v", err)
	}
	if n != 3 || first != 1700000000000 || last != 1700000002000 {
		t.Fatalf("ScanFile = (%d,%d,%d)，期望 (3,1700000000000,1700000002000)", n, first, last)
	}
}

// ————————————————— 端到端：StartChannel 推帧 —————————————————

func TestStartChannelPushesFrames(t *testing.T) {
	cfg, _ := testEnv(t)
	dir := cfg.Server.DataDir
	p := writeFile(t, dir, "robot.jsonl",
		"{\"t\":1700000000000,\"pose\":{\"latitude\":31.1,\"longitude\":121.1,\"heading\":10}}\n"+
			"{\"t\":1700000000050,\"pose\":{\"latitude\":31.2,\"longitude\":121.2,\"heading\":20}}\n"+
			"{\"t\":1700000000100,\"pose\":{\"latitude\":31.3,\"longitude\":121.3,\"heading\":30}}\n"+
			"{\"t\":1700000000150,\"pose\":{\"latitude\":31.4,\"longitude\":121.4,\"heading\":40}}\n")

	ds := model.NewDataSource("回放测试", model.KindReplay, model.ProtoFileReplay,
		map[string]any{"fileId": p, "speed": 4.0, "timePath": "t"})

	ad, err := adapter.New(model.KindReplay, ds, testLogger())
	if err != nil {
		t.Fatalf("创建适配器失败: %v", err)
	}
	defer func() { _ = ad.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ad.Start(ctx); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	specs, err := ad.ListChannels(ctx)
	if err != nil {
		t.Fatalf("ListChannels 失败: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("期望 1 个通道，实际 %d", len(specs))
	}
	if specs[0].PayloadType != model.PayloadGeoPose {
		t.Fatalf("期望 payloadType=geo_pose，实际 %s", specs[0].PayloadType)
	}
	t.Logf("通道: name=%s payloadType=%s defaultHz=%v", specs[0].Name, specs[0].PayloadType, specs[0].DefaultHz)

	got := make(chan model.Frame, 32)
	if err := ad.StartChannel(ctx, "ch_test", specs[0], func(channelID string, f model.Frame) {
		got <- f
	}); err != nil {
		t.Fatalf("StartChannel 失败: %v", err)
	}

	deadline := time.After(4 * time.Second)
	frames := make([]model.Frame, 0, 4)
	for len(frames) < 4 {
		select {
		case f := <-got:
			frames = append(frames, f)
		case <-deadline:
			t.Fatalf("超时：只收到 %d 帧（期望 4 帧）", len(frames))
		}
	}
	_ = ad.StopChannel("ch_test")

	if len(frames) != 4 {
		t.Fatalf("期望 4 帧，实际 %d", len(frames))
	}
	for i, f := range frames {
		// 关键断言：回放帧的 IngestedTs 必须等于 PublishedTs，否则 latencyMs 会错乱
		if f.IngestedTs != f.PublishedTs {
			t.Fatalf("第 %d 帧 IngestedTs(%d) != PublishedTs(%d)", i, f.IngestedTs, f.PublishedTs)
		}
		if f.PayloadType != model.PayloadGeoPose {
			t.Fatalf("第 %d 帧 payloadType=%s，期望 geo_pose", i, f.PayloadType)
		}
		var gp model.GeoPosePayload
		if err := f.Decode(&gp); err != nil {
			t.Fatalf("第 %d 帧解码失败: %v", i, err)
		}
		t.Logf("帧%d: ts=%d lat=%.4f lon=%.4f yaw=%.1f crs=%s seq=%d ingestedTs==publishedTs=%v",
			i, f.PublishedTs, gp.Lat, gp.Lon, gp.YawDeg, gp.CRS, f.Seq, f.IngestedTs == f.PublishedTs)
	}
	if frames[0].PublishedTs != 1700000000000 || frames[3].PublishedTs != 1700000000150 {
		t.Fatalf("首末帧时间戳错误: first=%d last=%d", frames[0].PublishedTs, frames[3].PublishedTs)
	}
	if frames[0].IngestedTs-frames[0].PublishedTs != 0 {
		t.Fatalf("latency 应为 0，实际 %d", frames[0].IngestedTs-frames[0].PublishedTs)
	}
}

// ————————————————— 无时间戳文件：虚拟时钟推进 —————————————————

func TestReplayWithoutTimestamp(t *testing.T) {
	cfg, _ := testEnv(t)
	dir := cfg.Server.DataDir
	p := writeFile(t, dir, "notime.json", `[{"v":1},{"v":2},{"v":3}]`)

	ds := model.NewDataSource("无时间戳", model.KindReplay, model.ProtoFileReplay,
		map[string]any{"fileId": p, "speed": 20.0})
	ad, err := adapter.New(model.KindReplay, ds, testLogger())
	if err != nil {
		t.Fatalf("创建适配器失败: %v", err)
	}
	defer func() { _ = ad.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = ad.Start(ctx)
	specs, err := ad.ListChannels(ctx)
	if err != nil {
		t.Fatalf("ListChannels 失败: %v", err)
	}

	got := make(chan model.Frame, 16)
	_ = ad.StartChannel(ctx, "ch_notime", specs[0], func(id string, f model.Frame) { got <- f })
	deadline := time.After(4 * time.Second)
	frames := make([]model.Frame, 0, 3)
	for len(frames) < 3 {
		select {
		case f := <-got:
			frames = append(frames, f)
		case <-deadline:
			t.Fatalf("超时：只收到 %d 帧", len(frames))
		}
	}
	_ = ad.StopChannel("ch_notime")
	// 虚拟时钟必须严格递增，否则时间轴回放会乱序
	for i := 1; i < len(frames); i++ {
		if frames[i].PublishedTs <= frames[i-1].PublishedTs {
			t.Fatalf("第 %d 帧时间戳未递增: %d <= %d", i, frames[i].PublishedTs, frames[i-1].PublishedTs)
		}
	}
	t.Logf("无时间戳回放首末帧: %d → %d（虚拟时钟递增，间隔由 fixedIntervalMs 决定）",
		frames[0].PublishedTs, frames[len(frames)-1].PublishedTs)
}

// ————————————————— 文件缺失 / Health 语义 —————————————————

func TestMissingFileAndHealth(t *testing.T) {
	cfg, _ := testEnv(t)
	ds := model.NewDataSource("缺失", model.KindReplay, model.ProtoFileReplay,
		map[string]any{"fileId": filepath.Join(cfg.Server.DataDir, "nope.json")})
	if _, err := adapter.New(model.KindReplay, ds, testLogger()); err == nil {
		t.Fatal("文件不存在时应返回错误")
	}

	p := writeFile(t, cfg.Server.DataDir, "ok.json", `{"a":1}`)
	ds2 := model.NewDataSource("正常", model.KindReplay, model.ProtoFileReplay, map[string]any{"fileId": p})
	ad, err := adapter.New(model.KindReplay, ds2, testLogger())
	if err != nil {
		t.Fatalf("创建适配器失败: %v", err)
	}
	defer func() { _ = ad.Close() }()
	h := ad.Health(context.Background())
	if !h.OK {
		t.Fatalf("Health 必须恒为 OK（否则 Manager 会置 reconnecting），实际 %+v", h)
	}
}

// ————————————————— 辅助 —————————————————
