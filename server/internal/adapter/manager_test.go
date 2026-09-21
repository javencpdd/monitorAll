package adapter

import (
	"context"
	"log/slog"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/bus"
	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/model"
)

// ————————————————— mock 适配器（不依赖真实 rosbridge / MediaMTX / HTTP）—————————————————

// mockAdapter 为测试用适配器：Start 成功、Health 可切换、StartChannel 定时产帧。
type mockAdapter struct {
	id       string
	startErr error
	healthOK bool

	mu       sync.Mutex
	started  int
	closed   int
	channels map[string]bool
	emitFn   Emit
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

func newMockAdapter(ds *model.DataSource, log *slog.Logger) (Adapter, error) {
	return &mockAdapter{id: ds.ID, healthOK: true, channels: map[string]bool{}}, nil
}

func (m *mockAdapter) DataSourceID() string { return m.id }

func (m *mockAdapter) Start(ctx context.Context) error {
	m.mu.Lock()
	m.started++
	err := m.startErr
	m.mu.Unlock()
	_ = ctx
	return err
}

func (m *mockAdapter) Close() error {
	m.mu.Lock()
	m.closed++
	m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
	return nil
}

func (m *mockAdapter) Health(ctx context.Context) Health {
	m.mu.Lock()
	ok := m.healthOK
	m.mu.Unlock()
	return Health{OK: ok, LatencyMs: 1, Detail: "mock"}
}

func (m *mockAdapter) ListChannels(ctx context.Context) ([]ChannelSpec, error) {
	return []ChannelSpec{{Name: "/mock", PayloadType: model.PayloadScalar}}, nil
}

func (m *mockAdapter) StartChannel(ctx context.Context, channelID string, spec ChannelSpec, emit Emit) error {
	m.mu.Lock()
	m.channels[channelID] = true
	m.emitFn = emit
	first := len(m.channels) == 1
	m.mu.Unlock()
	if first {
		// 仅第一个通道起一个产帧协程，避免测试里 goroutine 泛滥
		ctx2, cancel := context.WithCancel(context.Background())
		m.cancel = cancel
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx2.Done():
					return
				case <-ticker.C:
					f, _ := model.NewFrame(channelID, model.PayloadScalar, model.ScalarPayload{Value: 1}, 0)
					emit(channelID, f)
				}
			}
		}()
	}
	return nil
}

func (m *mockAdapter) StopChannel(channelID string) error {
	m.mu.Lock()
	delete(m.channels, channelID)
	m.mu.Unlock()
	return nil
}

// mockStore 为测试用的持久化读取实现。
type mockStore struct {
	mu     sync.Mutex
	srcs   map[string]*model.DataSource
	chs    map[string]*model.Channel
	params map[string]map[string]any
}

func (s *mockStore) GetDataSource(id string) (*model.DataSource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ds, ok := s.srcs[id]
	if !ok {
		return nil, errNotFound()
	}
	cp := *ds
	return &cp, nil
}

func (s *mockStore) GetChannel(id string) (*model.Channel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.chs[id]
	if !ok {
		return nil, errNotFound()
	}
	cp := *ch
	return &cp, nil
}

func (s *mockStore) ListChannelsBySource(dsID string) ([]model.Channel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []model.Channel{}
	for _, ch := range s.chs {
		if ch.DataSourceID == dsID {
			out = append(out, *ch)
		}
	}
	return out, nil
}

func (s *mockStore) ConnParamsFor(id string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.params[id]; ok {
		return p, nil
	}
	return map[string]any{}, nil
}

// errNotFound 返回「资源不存在」错误（测试用，错误码 40004）。
func errNotFound() error { return apperr.New(apperr.NotFound, "not found") }

// ————————————————— 指数退避 —————————————————

// TestNextDelayBackoff 校验退避算法：base*2^n，上限 max，带 ±jitter。
func TestNextDelayBackoff(t *testing.T) {
	base := 1 * time.Second
	max := 30 * time.Second
	jitter := 0.0 // 先测无抖动
	for n := 0; n <= 4; n++ {
		got := NextDelay(base, max, jitter, n)
		want := base * time.Duration(1<<uint(n))
		if got != want {
			t.Fatalf("n=%d 退避错误: got %v want %v", n, got, want)
		}
	}
	// 超过上限应裁剪：base*2^5 = 32s > max(30s)
	if got := NextDelay(base, max, 0, 5); got != max {
		t.Fatalf("应被裁剪到 max: got %v", got)
	}
	if got := NextDelay(base, max, 0, 20); got != max {
		t.Fatalf("应被裁剪到 max: got %v", got)
	}
	// 抖动范围：±20%
	for i := 0; i < 200; i++ {
		got := NextDelay(base, max, config.DefaultReconnectJitter, 1)
		if got < 1600*time.Millisecond || got > 2400*time.Millisecond {
			t.Fatalf("抖动超出 ±20%%: %v", got)
		}
	}
	// 异常入参兜底
	if got := NextDelay(0, 0, 0, 3); got <= 0 || got > max {
		t.Fatalf("异常入参未兜底: %v", got)
	}
}

// ————————————————— 引用计数与连接复用（BE-03） —————————————————

// TestManagerRefCountAndReuse 校验 3 张卡订阅同一通道只有 1 个底层适配器实例。
func TestManagerRefCountAndReuse(t *testing.T) {
	Register(model.KindVideo, newMockAdapter) // 测试内覆盖注册，避免依赖真实适配器
	cfg := config.DefaultConfig()
	cfg.Adapter.HealthIntervalMs = 50
	b := bus.New(&cfg.Bus)
	st := &mockStore{srcs: map[string]*model.DataSource{}, chs: map[string]*model.Channel{}}
	m := NewManager(cfg, b, st)

	ds := model.NewDataSource("mock", model.KindVideo, model.ProtoRTMP, map[string]any{"rtmpUrl": "rtmp://x/live/a"})
	st.srcs[ds.ID] = ds
	ch := model.NewChannel(ds.ID, "/odom", model.PayloadScalar, nil)
	st.chs[ch.ID] = ch

	// 3 次订阅（模拟 3 张卡 / 3 个 WS 连接）
	for i := 0; i < 3; i++ {
		if err := m.Subscribe(ds, ch); err != nil {
			t.Fatalf("第 %d 次订阅失败: %v", i, err)
		}
	}
	e, _ := m.entryForTest(ds.ID)
	if e == nil {
		t.Fatal("未创建运行态")
	}
	// 等待异步建连完成（Connect 在常驻循环里执行）
	startCount := 0
	deadlineConn := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadlineConn) {
		e.mu.Lock()
		if e.adapter != nil {
			startCount = e.adapter.(*mockAdapter).startedCount()
		}
		e.mu.Unlock()
		if startCount >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.mu.Lock()
	rc := e.refCount[ch.ID]
	e.mu.Unlock()
	if rc != 3 {
		t.Fatalf("引用计数应为 3，实际 %d", rc)
	}
	if startCount != 1 {
		t.Fatalf("底层连接应只建立 1 次，实际 %d", startCount)
	}

	// 释放 3 次后应关闭适配器
	for i := 0; i < 3; i++ {
		if err := m.Unsubscribe(ds, ch); err != nil {
			t.Fatalf("第 %d 次退订失败: %v", i, err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := m.entryForTest(ds.ID); !ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := m.entryForTest(ds.ID); ok {
		t.Fatal("引用计数归零后应释放运行态")
	}
	m.CloseAll()
}

// TestManagerGoroutineNoLeak 校验创建+删除 100 个数据源后 goroutine 不泄漏（G7）。
func TestManagerGoroutineNoLeak(t *testing.T) {
	Register(model.KindVideo, newMockAdapter)
	cfg := config.DefaultConfig()
	cfg.Adapter.HealthIntervalMs = 30
	b := bus.New(&cfg.Bus)
	st := &mockStore{srcs: map[string]*model.DataSource{}, chs: map[string]*model.Channel{}}
	m := NewManager(cfg, b, st)

	base := goroutineCount()
	for i := 0; i < 100; i++ {
		ds := model.NewDataSource("mock", model.KindVideo, model.ProtoRTMP, map[string]any{"rtmpUrl": "rtmp://x/live/a"})
		st.srcs[ds.ID] = ds
		ch := model.NewChannel(ds.ID, "/t", model.PayloadScalar, nil)
		st.chs[ch.ID] = ch
		if err := m.Subscribe(ds, ch); err != nil {
			t.Fatalf("订阅失败: %v", err)
		}
		if err := m.Unsubscribe(ds, ch); err != nil {
			t.Fatalf("退订失败: %v", err)
		}
	}
	m.CloseAll()
	// 给调度器一点时间回收
	for i := 0; i < 50; i++ {
		if goroutineCount() <= base+10 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := goroutineCount(); got > base+10 {
		t.Fatalf("goroutine 泄漏: base=%d now=%d", base, got)
	}
}

// TestManagerSampleTimeout 校验样例帧超时返回 50004（D4）。
func TestManagerSampleTimeout(t *testing.T) {
	Register(model.KindVideo, func(ds *model.DataSource, log *slog.Logger) (Adapter, error) {
		return &silentAdapter{id: ds.ID}, nil
	})
	cfg := config.DefaultConfig()
	b := bus.New(&cfg.Bus)
	st := &mockStore{srcs: map[string]*model.DataSource{}, chs: map[string]*model.Channel{}}
	m := NewManager(cfg, b, st)

	ds := model.NewDataSource("silent", model.KindVideo, model.ProtoRTMP, map[string]any{"rtmpUrl": "rtmp://x/live/a"})
	if _, err := m.Sample(context.Background(), ds, ChannelSpec{Name: "/x"}, 200*time.Millisecond); err == nil {
		t.Fatal("无数据时应返回错误")
	} else if apperr.CodeOf(err) != apperr.SampleTimeout {
		t.Fatalf("错误码应为 50004，实际 %d", apperr.CodeOf(err))
	}
}

// TestManagerSampleOK 校验样例帧采集成功路径。
func TestManagerSampleOK(t *testing.T) {
	Register(model.KindVideo, newMockAdapter)
	cfg := config.DefaultConfig()
	b := bus.New(&cfg.Bus)
	st := &mockStore{srcs: map[string]*model.DataSource{}, chs: map[string]*model.Channel{}}
	m := NewManager(cfg, b, st)

	ds := model.NewDataSource("mock", model.KindVideo, model.ProtoRTMP, map[string]any{"rtmpUrl": "rtmp://x/live/a"})
	f, err := m.Sample(context.Background(), ds, ChannelSpec{Name: "/mock", PayloadType: model.PayloadScalar}, 2*time.Second)
	if err != nil {
		t.Fatalf("采集样例帧失败: %v", err)
	}
	if f.PayloadType != model.PayloadScalar {
		t.Fatalf("样例帧类型错误: %s", f.PayloadType)
	}
	if f.Seq == 0 {
		t.Fatal("样例帧应带 seq")
	}
}

// silentAdapter 为「连上但永不产帧」的适配器（用于超时测试）。
type silentAdapter struct {
	id string
}

func (s *silentAdapter) DataSourceID() string                     { return s.id }
func (s *silentAdapter) Start(ctx context.Context) error          { return nil }
func (s *silentAdapter) Close() error                             { return nil }
func (s *silentAdapter) Health(ctx context.Context) Health        { return Health{OK: true} }

func (s *silentAdapter) StopChannel(channelID string) error       { return nil }
func (s *silentAdapter) ListChannels(ctx context.Context) ([]ChannelSpec, error) {
	return []ChannelSpec{{Name: "/x", PayloadType: model.PayloadJSON}}, nil
}
func (s *silentAdapter) StartChannel(ctx context.Context, channelID string, spec ChannelSpec, emit Emit) error {
	return nil // 静默：永不产帧
}

// goroutineCount 返回当前 goroutine 数量。
func goroutineCount() int { return runtime.NumGoroutine() }

// startedCount 返回 mock 适配器被 Start 的次数。
func (m *mockAdapter) startedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.started
}

// entryForTest 返回内部运行态（仅测试用）。
func (m *Manager) entryForTest(id string) (*entry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	return e, ok
}
