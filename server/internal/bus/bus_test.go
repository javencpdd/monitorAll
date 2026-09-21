package bus

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/model"
)

// TestLimiterTokenBucket 校验令牌桶：以远高于 maxHz 的频率灌入，实际投递 ≈ maxHz×时长。
func TestLimiterTokenBucket(t *testing.T) {
	const maxHz = 20.0
	l := NewLimiter(maxHz)
	if l.Interval() != time.Duration(float64(time.Second)/maxHz) {
		t.Fatalf("间隔计算错误: %v", l.Interval())
	}
	start := time.Now()
	delivered := 0
	total := 0
	// 模拟 5s 内以 1000Hz 灌入（用时间推进代替真实 sleep，保证单测快速稳定）
	step := time.Millisecond // 1000Hz
	now := start
	for i := 0; i < 5000; i++ {
		total++
		if _, ok := l.Allow(now, model.Frame{}); ok {
			delivered++
		}
		now = now.Add(step)
	}
	want := int(maxHz * 5) // 100
	if delivered < want-2 || delivered > want+2 {
		t.Fatalf("投递数量不符: got %d want ≈%d (total=%d)", delivered, want, total)
	}
	if l.Dropped() != uint64(total-delivered) {
		t.Fatalf("丢帧计数错误: got %d want %d", l.Dropped(), total-delivered)
	}
}

// TestLimiterLastWriteWins 校验合并窗口内最新值覆盖旧值。
func TestLimiterLastWriteWins(t *testing.T) {
	l := NewLimiter(10) // 100ms 间隔
	base := time.Now()
	if _, ok := l.Allow(base, model.Frame{Seq: 1}); !ok {
		t.Fatal("首帧应放行")
	}
	// 窗口内的后续帧被丢弃，pending 只保留最后一帧
	for i := 2; i <= 5; i++ {
		if _, ok := l.Allow(base.Add(time.Duration(i)*time.Millisecond), model.Frame{Seq: uint64(i)}); ok {
			t.Fatal("窗口内不应放行")
		}
	}
	p := l.Pending()
	if p == nil || p.Seq != 5 {
		t.Fatalf("pending 应保留最新帧: %+v", p)
	}
	// 到点后放行新帧
	if _, ok := l.Allow(base.Add(100*time.Millisecond), model.Frame{Seq: 6}); !ok {
		t.Fatal("到达间隔后应放行")
	}
}

// TestLimiterNoLimit 校验 maxHz<=0 表示不限流。
func TestLimiterNoLimit(t *testing.T) {
	l := NewLimiter(0)
	now := time.Now()
	for i := 0; i < 100; i++ {
		if _, ok := l.Allow(now, model.Frame{}); !ok {
			t.Fatal("不限流时应全部放行")
		}
	}
	if l.Dropped() != 0 {
		t.Fatal("不限流时不应丢帧")
	}
}

// TestLimiterSetMaxHz 校验运行期调整频率。
func TestLimiterSetMaxHz(t *testing.T) {
	l := NewLimiter(10)
	l.SetMaxHz(50)
	if l.MaxHz() != 50 {
		t.Fatalf("调整失败: %v", l.MaxHz())
	}
	if l.Interval() != 20*time.Millisecond {
		t.Fatalf("间隔未同步: %v", l.Interval())
	}
}

// ————————————————— 订阅引用计数 —————————————————

// memSink 为测试用的帧消费者。
type memSink struct {
	id    string
	mu    sync.Mutex
	items []model.Frame
}

func (m *memSink) ID() string { return m.id }

func (m *memSink) OnFrame(f model.Frame) {
	m.mu.Lock()
	m.items = append(m.items, f)
	m.mu.Unlock()
}

func (m *memSink) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.items)
}

// TestBusSubscribeRefCount 校验以连接为单位的订阅引用计数与幂等性。
func TestBusSubscribeRefCount(t *testing.T) {
	b := New(testBusConfig())
	b.Start(testCtx())
	defer b.Stop()

	s1 := &memSink{id: "conn-1"}
	s2 := &memSink{id: "conn-2"}
	b.RegisterSink(s1)
	b.RegisterSink(s2)

	br := b.EnsureChannel("ch_a", model.PayloadScalar, 0)
	_ = br
	b.Subscribe("ch_a", s1.ID())
	b.Subscribe("ch_a", s1.ID()) // 重复订阅幂等
	if got := b.RefCount("ch_a"); got != 1 {
		t.Fatalf("重复订阅应幂等: got %d want 1", got)
	}
	b.Subscribe("ch_a", s2.ID())
	if got := b.RefCount("ch_a"); got != 2 {
		t.Fatalf("引用计数错误: got %d want 2", got)
	}
	// 一帧应广播给两个订阅者
	b.Publish("ch_a", model.Frame{ChannelID: "ch_a", PayloadType: model.PayloadScalar})
	if s1.count() != 1 || s2.count() != 1 {
		t.Fatalf("帧未广播给全部订阅者: %d / %d", s1.count(), s2.count())
	}
	// 取消订阅后不再收到（先等过限流间隔，确保帧会被投递）
	b.Unsubscribe("ch_a", s1.ID())
	time.Sleep(60 * time.Millisecond)
	b.Publish("ch_a", model.Frame{ChannelID: "ch_a", PayloadType: model.PayloadScalar})
	if s1.count() != 1 {
		t.Fatalf("取消订阅后不应再收到帧: %d", s1.count())
	}
	if s2.count() != 2 {
		t.Fatalf("其它订阅者不应受影响: %d", s2.count())
	}
	// 注销 sink 应清理其订阅
	b.UnregisterSink(s2.ID())
	if got := b.RefCount("ch_a"); got != 0 {
		t.Fatalf("注销后引用计数应归零: %d", got)
	}
}

// TestBusPublishNeverBlocks 校验高频灌入时 Publish 永不阻塞（G6）。
func TestBusPublishNeverBlocks(t *testing.T) {
	b := New(testBusConfig())
	b.Start(testCtx())
	defer b.Stop()
	s := &memSink{id: "conn-1"}
	b.RegisterSink(s)
	b.EnsureChannel("ch_fast", model.PayloadImage, 10)
	b.Subscribe("ch_fast", s.ID())

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20000; i++ {
			b.Publish("ch_fast", model.Frame{ChannelID: "ch_fast", PayloadType: model.PayloadImage})
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Publish 发生阻塞（G6 违约）")
	}
	if st := b.Stats("ch_fast"); st.Dropped == 0 {
		t.Fatal("高频灌入应产生丢帧计数")
	}
}

// TestBusRingBufferCapacity 校验环形缓冲定容且可读取最新帧。
func TestBusRingBufferCapacity(t *testing.T) {
	cfg := testBusConfig()
	cfg.RingCapacity = 8
	b := New(cfg)
	b.EnsureChannel("ch_ring", model.PayloadScalar, 0)
	for i := 0; i < 100; i++ {
		b.Publish("ch_ring", model.Frame{ChannelID: "ch_ring", PayloadType: model.PayloadScalar})
	}
	st := b.Stats("ch_ring")
	if st.LastSeq != 100 {
		t.Fatalf("seq 应单调递增到 100: %d", st.LastSeq)
	}
	f, ok := b.LatestFrame("ch_ring")
	if !ok || f.Seq != 100 {
		t.Fatalf("最新帧错误: %+v", f)
	}
}

// testBusConfig 返回测试用总线配置（限流放宽，便于断言投递行为）。
func testBusConfig() *config.BusConfig {
	cfg := &config.BusConfig{}
	cfg.DefaultMaxHz = 20
	cfg.ImageMaxHz = 10
	cfg.VideoStatusHz = 0.5
	cfg.RingCapacity = 1200
	cfg.BackpressureNotifyMs = 1000
	return cfg
}

// testCtx 返回测试用上下文。
func testCtx() context.Context { return context.Background() }
