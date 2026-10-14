package weather

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ————————————————— 测试脚手架 —————————————————

// fakeClock 是可推进的时钟（验证缓存 TTL 必须能"让时间过去"，靠 sleep 会让测试又慢又脆）。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newTestService 用 httptest 服务器替换真实第三方，并接管时钟。
func newTestService(t *testing.T, url string) *Service {
	t.Helper()
	s := NewService(2000)
	s.client = &http.Client{Timeout: 2 * time.Second}
	if url != "" {
		s.Register(NewOpenMeteo(s.client, url))
	}
	return s
}

// openMeteoBody 造一份 open-meteo 正常响应。
func openMeteoBody(temp float64, code int, wind float64) string {
	return `{"current":{"temperature_2m":` + strconv.FormatFloat(temp, 'f', -1, 64) +
		`,"weather_code":` + strconv.Itoa(code) +
		`,"wind_speed_10m":` + strconv.FormatFloat(wind, 'f', -1, 64) +
		`},"current_units":{"temperature_2m":"°C","wind_speed_10m":"km/h"}}`
}

// opts 默认的查询参数（深圳南山）。
func opts() Options {
	return Options{Provider: ProviderOpenMeteo, Lat: 22.53, Lon: 113.93, RefreshMin: 10, TimeoutMs: 2000}
}

// ————————————————— WMO 映射 —————————————————

// TestWMOTextCoversAllCodes 覆盖规格里点名的全部天气代码，且【不允许出现空串】。
func TestWMOTextCoversAllCodes(t *testing.T) {
	want := map[int]string{
		0: "晴",
		1: "", 2: "", 3: "", // 1-3 = 多云/阴，具体文案不锁死，但必须非空
		45: "", 48: "",
		51: "", 53: "", 55: "",
		56: "", 57: "",
		61: "", 63: "", 65: "",
		66: "", 67: "",
		71: "", 73: "", 75: "",
		77: "",
		80: "", 81: "", 82: "",
		85: "", 86: "",
		95: "",
		96: "", 99: "",
	}
	for code := range want {
		if got := WMOText(code); got == "" {
			t.Fatalf("WMO code %d 的中文描述为空（前端会渲染出空白挂件）", code)
		}
	}
	if WMOText(0) != "晴" {
		t.Fatalf("code 0 应为「晴」，实际 %q", WMOText(0))
	}
	// 语义抽查：雨 / 雪 / 雷必须能看出类别
	for code, must := range map[int]string{63: "雨", 73: "雪", 95: "雷", 45: "雾", 82: "雨"} {
		if got := WMOText(code); !strings.Contains(got, must) {
			t.Fatalf("code %d 的描述 %q 应包含 %q", code, got, must)
		}
	}
	// 未知代码同样不能为空
	if got := WMOText(999); got == "" {
		t.Fatal("未知代码的描述不应为空")
	} else if !strings.Contains(got, "999") {
		t.Fatalf("未知代码应把代码本身带上，便于补映射表：%q", got)
	}
}

// ————————————————— 解析与缓存 —————————————————

// TestFetchParsesOpenMeteo 校验第三方响应被正确解析成中文快照。
func TestFetchParsesOpenMeteo(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		// 顺带校验请求真的带上了坐标与 current 字段（拼错参数会拿到 200 + 空数据）
		q := r.URL.Query()
		if q.Get("latitude") == "" || q.Get("longitude") == "" {
			t.Errorf("请求缺少经纬度: %s", r.URL.RawQuery)
		}
		if !strings.Contains(q.Get("current"), "weather_code") {
			t.Errorf("请求缺少 current=...weather_code: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(openMeteoBody(26.5, 63, 12.3)))
	}))
	defer srv.Close()

	s := newTestService(t, srv.URL)
	snap := s.Get(context.Background(), opts())
	if !snap.OK {
		t.Fatalf("应查询成功，实际: %+v", snap)
	}
	if snap.Temp != 26.5 || snap.Code != 63 || snap.Wind != 12.3 {
		t.Fatalf("解析结果错误: %+v", snap)
	}
	if snap.Unit != "°C" || snap.WindUnit != "km/h" {
		t.Fatalf("单位未透传: unit=%q windUnit=%q", snap.Unit, snap.WindUnit)
	}
	if snap.Text != WMOText(63) || !strings.Contains(snap.Text, "雨") {
		t.Fatalf("天气描述错误: %q", snap.Text)
	}
	if snap.FetchedAtMs == 0 {
		t.Fatal("FetchedAtMs 为 0（前端无法显示「更新于」）")
	}
	if snap.Stale {
		t.Fatal("首次查询不应标记 stale")
	}
}

// TestCacheHitWithinTTL 缓存窗口内不重复出网（TTL = RefreshMin 分钟）。
func TestCacheHitWithinTTL(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(openMeteoBody(20, 0, 5)))
	}))
	defer srv.Close()

	clock := &fakeClock{t: time.Now()}
	s := newTestService(t, srv.URL)
	s.now = clock.now

	first := s.Get(context.Background(), opts())
	clock.advance(9 * time.Minute) // 仍在 10 分钟窗口内
	second := s.Get(context.Background(), opts())

	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("缓存窗口内应只出网 1 次，实际 %d 次", n)
	}
	if second.FetchedAtMs != first.FetchedAtMs {
		t.Fatal("第二次应命中缓存（FetchedAtMs 应相同）")
	}
	if second.Stale {
		t.Fatal("缓存命中不应标记 stale")
	}
}

// TestCacheExpiredRefetches 窗口过后必须重新出网（否则天气会永远停在首次取值）。
func TestCacheExpiredRefetches(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(openMeteoBody(20, 0, 5)))
	}))
	defer srv.Close()

	clock := &fakeClock{t: time.Now()}
	s := newTestService(t, srv.URL)
	s.now = clock.now

	_ = s.Get(context.Background(), opts())
	clock.advance(11 * time.Minute)
	_ = s.Get(context.Background(), opts())

	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Fatalf("缓存过期后应重新出网，实际共 %d 次", n)
	}
}

// ————————————————— 失败语义 —————————————————

// TestStaleFallbackWhenProviderFails 出网失败但有旧数据时：返回旧数据 + stale=true。
//
// 这是本包最重要的一条行为：顶栏不能因为第三方抖一下就空白。
func TestStaleFallbackWhenProviderFails(t *testing.T) {
	var mode int32 // 0=正常 1=500
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&mode) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(openMeteoBody(18.5, 61, 3)))
	}))
	defer srv.Close()

	clock := &fakeClock{t: time.Now()}
	s := newTestService(t, srv.URL)
	s.now = clock.now

	first := s.Get(context.Background(), opts())
	if !first.OK {
		t.Fatalf("首次应成功: %+v", first)
	}

	atomic.StoreInt32(&mode, 1)
	clock.advance(11 * time.Minute) // 让缓存过期，强制出网

	got := s.Get(context.Background(), opts())
	if !got.OK {
		t.Fatalf("有旧数据时出网失败不应返回失败，实际: %+v", got)
	}
	if !got.Stale {
		t.Fatal("应标记 stale=true（前端据此显示「数据可能不是最新」）")
	}
	if got.Temp != 18.5 || got.Code != 61 {
		t.Fatalf("应回落到旧数据，实际: %+v", got)
	}
	if got.FetchedAtMs != first.FetchedAtMs {
		t.Fatal("stale 数据的 FetchedAtMs 应保持旧时刻（否则前端显示的更新时间是假的）")
	}
}

// TestFailureWithoutCacheReturnsExplicitReason 无缓存且出网失败：ok=false + 明确 reason/message。
func TestFailureWithoutCacheReturnsExplicitReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	s := newTestService(t, srv.URL)
	got := s.Get(context.Background(), opts())
	if got == nil {
		t.Fatal("Get 永远不应返回 nil")
	}
	if got.OK {
		t.Fatalf("出网失败时 ok 应为 false: %+v", got)
	}
	if got.Reason != ReasonProviderRejected {
		t.Fatalf("reason 应为 %s，实际 %q", ReasonProviderRejected, got.Reason)
	}
	if got.Message == "" {
		t.Fatal("message 不能为空（前端需要一个可显示的中文提示）")
	}
}

// TestUnreachableReason 网络层失败（连接被拒）必须归为 provider_unreachable。
func TestUnreachableReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // 端口随即释放，连接必失败

	s := newTestService(t, url)
	got := s.Get(context.Background(), opts())
	if got.OK {
		t.Fatalf("连不上时 ok 应为 false: %+v", got)
	}
	if got.Reason != ReasonProviderUnreachable {
		t.Fatalf("reason 应为 %s，实际 %q（message=%q）", ReasonProviderUnreachable, got.Reason, got.Message)
	}
	if got.Message == "" {
		t.Fatal("message 不能为空")
	}
}

// TestOpenMeteoErrorFlagIsRejected open-meteo 用 HTTP 200 + {"error":true} 报错，
// 不识别就会把响应解析成 0℃ / 晴 —— 典型的静默失败。
func TestOpenMeteoErrorFlagIsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"error":true,"reason":"Latitude must be in range of -90 to 90°."}`))
	}))
	defer srv.Close()

	s := newTestService(t, srv.URL)
	got := s.Get(context.Background(), opts())
	if got.OK {
		t.Fatalf("error=true 的响应不应被当成成功: %+v", got)
	}
	if got.Reason != ReasonProviderRejected {
		t.Fatalf("reason 应为 %s，实际 %q", ReasonProviderRejected, got.Reason)
	}
	if got.Temp != 0 || got.Text != "" {
		t.Fatalf("失败时不应携带观测数据: temp=%v text=%q", got.Temp, got.Text)
	}
}

// TestCoordsInvalid 坐标未填 / 越界必须显式失败，且根本不出网。
func TestCoordsInvalid(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(openMeteoBody(20, 0, 5)))
	}))
	defer srv.Close()
	s := newTestService(t, srv.URL)

	cases := map[string]Options{
		"未填(0,0)": {Provider: ProviderOpenMeteo, Lat: 0, Lon: 0, RefreshMin: 10, TimeoutMs: 1000},
		"纬度越界":    {Provider: ProviderOpenMeteo, Lat: 91, Lon: 0, RefreshMin: 10, TimeoutMs: 1000},
		"经度越界":    {Provider: ProviderOpenMeteo, Lat: 0, Lon: -181, RefreshMin: 10, TimeoutMs: 1000},
	}
	for name, o := range cases {
		got := s.Get(context.Background(), o)
		if got.OK || got.Reason != ReasonCoordsInvalid || got.Message == "" {
			t.Fatalf("%s 应返回 %s 失败，实际: %+v", name, ReasonCoordsInvalid, got)
		}
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("坐标非法时不应出网，实际出了 %d 次", n)
	}
}

// TestUnsupportedProvider 未注册的 provider 必须显式失败（不能回落到默认 provider，
// 否则配置写错会拿到"看起来正常但地点不对"的数据）。
func TestUnsupportedProvider(t *testing.T) {
	s := newTestService(t, "")
	got := s.Get(context.Background(), Options{Provider: "amap", Lat: 22.5, Lon: 113.9, RefreshMin: 10, TimeoutMs: 1000})
	if got.OK || got.Reason != ReasonProviderUnsupported {
		t.Fatalf("应返回 %s，实际: %+v", ReasonProviderUnsupported, got)
	}
}

// ————————————————— 并发 —————————————————

// TestSingleflightDedupesConcurrent 同键并发只打一次第三方（顶栏被多页面同时请求时的保护）。
func TestSingleflightDedupesConcurrent(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		time.Sleep(150 * time.Millisecond) // 拉长窗口，确保并发真的重叠
		_, _ = w.Write([]byte(openMeteoBody(21, 2, 7)))
	}))
	defer srv.Close()

	s := newTestService(t, srv.URL)

	var wg sync.WaitGroup
	results := make([]*Snapshot, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = s.Get(context.Background(), opts())
		}(i)
	}
	wg.Wait()

	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("20 个并发请求应合并成 1 次出网，实际 %d 次（第三方会被打爆并限流）", n)
	}
	for i, r := range results {
		if r == nil || !r.OK || r.Temp != 21 {
			t.Fatalf("第 %d 个并发结果异常: %+v", i, r)
		}
	}
}

// TestTimeoutIsRespected 超时必须生效（第三方挂住时不能把请求拖到天荒地老）。
func TestTimeoutIsRespected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(700 * time.Millisecond) // httptest.Close 会等它结束，别睡太久
		_, _ = w.Write([]byte(openMeteoBody(20, 0, 5)))
	}))
	defer srv.Close()

	s := newTestService(t, srv.URL)
	o := opts()
	o.TimeoutMs = 150
	start := time.Now()
	got := s.Get(context.Background(), o)
	if got.OK {
		t.Fatalf("超时应返回失败: %+v", got)
	}
	if got.Reason != ReasonProviderUnreachable {
		t.Fatalf("超时 reason 应为 %s，实际 %q", ReasonProviderUnreachable, got.Reason)
	}
	if elapsed := time.Since(start); elapsed > 600*time.Millisecond {
		t.Fatalf("超时未生效，耗时 %v（应在 150ms 左右返回）", elapsed)
	}
}
