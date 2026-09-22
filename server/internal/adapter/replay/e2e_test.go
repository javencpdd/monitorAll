package replay_test

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/monitorall/monitorall/internal/adapter"
	"github.com/monitorall/monitorall/internal/api"
	"github.com/monitorall/monitorall/internal/bus"
	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/model"
	"github.com/monitorall/monitorall/internal/store"
)

// 示例轨迹数据：NDJSON，含 pose.latitude / pose.longitude，时间字段 t（ms，间隔 100ms）。
const sampleTrack = `{"t":1700000000000,"pose":{"latitude":31.2304,"longitude":121.4737,"heading":12.5}}
{"t":1700000000100,"pose":{"latitude":31.2309,"longitude":121.4742,"heading":15.0}}
{"t":1700000000200,"pose":{"latitude":31.2314,"longitude":121.4747,"heading":18.0}}
{"t":1700000000300,"pose":{"latitude":31.2319,"longitude":121.4752,"heading":21.0}}
{"t":1700000000400,"pose":{"latitude":31.2324,"longitude":121.4757,"heading":24.0}}
`

// apiResp 为统一响应体的解码容器。
type apiResp struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// frameSink 收集总线推来的帧（非阻塞）。
type frameSink struct {
	id string
	ch chan model.Frame
}

func (s *frameSink) ID() string { return s.id }
func (s *frameSink) OnFrame(f model.Frame) {
	select {
	case s.ch <- f:
	default:
	}
}

// TestUploadCreateAndReplay 走完整链路：
// 上传 JSON → 建 replay 数据源 → 订阅通道 → 经总线收到回放帧。
func TestUploadCreateAndReplay(t *testing.T) {
	dir := t.TempDir()

	// ——— 1. 打开数据库（自动执行 v1+v2 迁移，建出 imports 表）———
	st, err := store.Open(config.StoreConfig{DSN: filepath.Join(dir, "test.db")}, "")
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer func() { _ = st.Close() }()

	// ——— 2. 装配运行时（与 main.go 一致）———
	cfg := config.DefaultConfig()
	cfg.Server.DataDir = dir
	b := bus.New(&cfg.Bus)
	adapter.Setup(&adapter.Env{Cfg: cfg, Bus: b, Store: st})
	adm := adapter.NewManager(cfg, b, st)
	defer adm.CloseAll()

	r, _, err := api.NewRouter(cfg, st, adm, b, nil, nil, nil)
	if err != nil {
		t.Fatalf("构建路由失败: %v", err)
	}

	// ——— 3. multipart 上传 ———
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "robot-track.jsonl")
	if err != nil {
		t.Fatalf("构造表单失败: %v", err)
	}
	if _, err := io.WriteString(part, sampleTrack); err != nil {
		t.Fatalf("写入表单失败: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("关闭表单失败: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, config.APIPrefix+"/imports?timePath=t", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("上传期望 201，实际 %d，body=%s", w.Code, w.Body.String())
	}
	var upResp apiResp
	if err := json.Unmarshal(w.Body.Bytes(), &upResp); err != nil {
		t.Fatalf("解析上传响应失败: %v", err)
	}
	var im model.Import
	if err := json.Unmarshal(upResp.Data, &im); err != nil {
		t.Fatalf("解析导入记录失败: %v", err)
	}
	t.Logf("上传成功: id=%s file=%s relPath=%s frames=%d firstTs=%d lastTs=%d",
		im.ID, im.FileName, im.RelPath, im.FrameCount, im.FirstTs, im.LastTs)
	if im.FrameCount != 5 || im.FirstTs != 1700000000000 || im.LastTs != 1700000000400 {
		t.Fatalf("导入统计错误: frames=%d firstTs=%d lastTs=%d", im.FrameCount, im.FirstTs, im.LastTs)
	}
	// 文件必须真实落盘到 data/imports 下
	if _, err := os.Stat(filepath.Join(dir, im.RelPath)); err != nil {
		t.Fatalf("导入文件未落盘: %v", err)
	}

	// ——— 4. 建 replay 数据源（自动创建通道）———
	createBody, _ := json.Marshal(map[string]any{
		"name":       "机器人轨迹回放",
		"kind":       "replay",
		"protocol":   "file-replay",
		"connParams": map[string]any{"fileId": im.ID, "speed": 1.0, "loop": false, "timePath": "t"},
	})
	req = httptest.NewRequest(http.MethodPost, config.APIPrefix+"/datasources", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("建数据源期望 201，实际 %d，body=%s", w.Code, w.Body.String())
	}
	var dsResp apiResp
	if err := json.Unmarshal(w.Body.Bytes(), &dsResp); err != nil {
		t.Fatalf("解析建源响应失败: %v", err)
	}
	var created struct {
		model.DataSource
		Channels []model.Channel `json:"channels"`
	}
	if err := json.Unmarshal(dsResp.Data, &created); err != nil {
		t.Fatalf("解析数据源失败: %v", err)
	}
	if len(created.Channels) != 1 {
		t.Fatalf("期望自动创建 1 个通道，实际 %d", len(created.Channels))
	}
	ch := created.Channels[0]
	t.Logf("数据源: id=%s kind=%s protocol=%s", created.ID, created.Kind, created.Protocol)
	t.Logf("通道: id=%s name=%s payloadType=%s rateLimitHz=%v", ch.ID, ch.Name, ch.PayloadType, ch.RateLimitHz)
	if ch.PayloadType != model.PayloadGeoPose {
		t.Fatalf("期望通道 payloadType=geo_pose（地图轨迹），实际 %s", ch.PayloadType)
	}

	// ——— 5. 订阅并收帧（等价于前端 WS subscribe）———
	ds, err := st.GetDataSource(created.ID)
	if err != nil {
		t.Fatalf("读取数据源失败: %v", err)
	}
	sk := &frameSink{id: "sink_test", ch: make(chan model.Frame, 64)}
	b.EnsureChannel(ch.ID, ch.PayloadType, ch.RateLimitHz)
	b.RegisterSink(sk)
	b.Subscribe(ch.ID, sk.ID())
	if err := adm.Subscribe(ds, &ch); err != nil {
		t.Fatalf("订阅失败: %v", err)
	}

	frames := make([]model.Frame, 0, 5)
	deadline := time.After(6 * time.Second)
	for len(frames) < 5 {
		select {
		case f := <-sk.ch:
			frames = append(frames, f)
		case <-deadline:
			t.Fatalf("超时：只收到 %d 帧（期望 5 帧）", len(frames))
		}
	}
	_ = adm.Unsubscribe(ds, &ch)

	// ——— 6. 断言 ———
	t.Logf("收到 %d 帧，经总线投递（限流后未削帧）", len(frames))
	for i, f := range frames {
		var gp model.GeoPosePayload
		if err := f.Decode(&gp); err != nil {
			t.Fatalf("第 %d 帧解码失败: %v", i, err)
		}
		if f.IngestedTs != f.PublishedTs {
			t.Fatalf("第 %d 帧 IngestedTs(%d) != PublishedTs(%d)，latencyMs 会错乱", i, f.IngestedTs, f.PublishedTs)
		}
		t.Logf("帧%d: seq=%d ts=%d lat=%.4f lon=%.4f yaw=%.1f crs=%s size=%dB",
			i, f.Seq, f.PublishedTs, gp.Lat, gp.Lon, gp.YawDeg, gp.CRS, f.SizeBytes)
	}
	if frames[0].PublishedTs != 1700000000000 || frames[4].PublishedTs != 1700000000400 {
		t.Fatalf("首末帧时间戳错误: first=%d last=%d", frames[0].PublishedTs, frames[4].PublishedTs)
	}
	// seq 必须单调递增（总线分配），前端据此做丢帧检测
	for i := 1; i < len(frames); i++ {
		if frames[i].Seq <= frames[i-1].Seq {
			t.Fatalf("seq 未递增: %d -> %d", frames[i-1].Seq, frames[i].Seq)
		}
	}
	t.Logf("总线统计: %+v", b.Stats(ch.ID))
}
