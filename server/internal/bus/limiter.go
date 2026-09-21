package bus

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/monitorall/monitorall/internal/model"
)

// Limiter 为通道级限流器：令牌桶限制 maxHz，超限帧进入合并窗口，
// 窗口内**最新值覆盖旧值**（last-write-wins），窗口结束时只下发 1 帧。
// 全部操作非阻塞，供 bus.Publish 直接调用（BE-04）。
type Limiter struct {
	maxHz    float64
	interval time.Duration
	mu       sync.Mutex
	lastSent time.Time

	pending   atomic.Value // *model.Frame，合并窗口内待发帧
	dropped   atomic.Uint64
	delivered atomic.Uint64
}

// NewLimiter 构造限流器；maxHz <= 0 表示不限流。
func NewLimiter(maxHz float64) *Limiter {
	l := &Limiter{}
	l.SetMaxHz(maxHz)
	return l
}

// SetMaxHz 调整限流频率并重算间隔。
func (l *Limiter) SetMaxHz(hz float64) {
	if hz <= 0 {
		l.mu.Lock()
		l.maxHz = 0
		l.interval = 0
		l.mu.Unlock()
		return
	}
	l.mu.Lock()
	l.maxHz = hz
	l.interval = time.Duration(float64(time.Second) / hz)
	l.mu.Unlock()
}

// MaxHz 返回当前限流频率。
func (l *Limiter) MaxHz() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.maxHz
}

// Interval 返回最小发送间隔。
func (l *Limiter) Interval() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.interval
}

// Allow 决定是否允许立即下发该帧。
// 允许：返回 (frame, true) 并刷新 lastSent；否则把帧放入合并窗口（覆盖旧值）
// 并累计 dropped，返回 (zero, false)。
func (l *Limiter) Allow(now time.Time, f model.Frame) (model.Frame, bool) {
	l.mu.Lock()
	interval := l.interval
	lastSent := l.lastSent
	if interval <= 0 {
		l.lastSent = now
		l.mu.Unlock()
		l.delivered.Add(1)
		return f, true
	}
	if lastSent.IsZero() || now.Sub(lastSent) >= interval {
		l.lastSent = now
		l.mu.Unlock()
		l.delivered.Add(1)
		return f, true
	}
	l.mu.Unlock()

	// 未到间隔：最新值覆盖，旧值计为丢弃
	l.pending.Store(&f)
	l.dropped.Add(1)
	return model.Frame{}, false
}

// Pending 取出合并窗口中的待发帧（没有则返回 nil）。
func (l *Limiter) Pending() *model.Frame {
	v := l.pending.Load()
	if v == nil {
		return nil
	}
	f, ok := v.(*model.Frame)
	if !ok || f == nil {
		return nil
	}
	return f
}

// Dropped 返回累计丢弃帧数。
func (l *Limiter) Dropped() uint64 { return l.dropped.Load() }

// Delivered 返回累计下发帧数。
func (l *Limiter) Delivered() uint64 { return l.delivered.Load() }
