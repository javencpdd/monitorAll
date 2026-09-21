// Package adapter 定义数据源适配器接口与注册表，以及负责生命周期、
// 引用计数、指数退避重连与 goroutine 治理的 Manager（架构 §8）。
package adapter

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/bus"
	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/logx"
	"github.com/monitorall/monitorall/internal/model"
)

// ————————————————— 接口定义 —————————————————

// Health 为适配器探活结果。
type Health struct {
	OK        bool   `json:"ok"`
	LatencyMs int64  `json:"latencyMs"`
	Detail    string `json:"detail,omitempty"`
}

// ChannelSpec 描述适配器可供给的一个通道。
type ChannelSpec struct {
	Name        string            `json:"name"`
	PayloadType model.PayloadType `json:"payloadType"`
	Meta        map[string]any    `json:"meta,omitempty"`
	DefaultHz   float64           `json:"defaultHz,omitempty"` // 建议限流，0 = 用全局默认
}

// Emit 是适配器向总线投递 Frame 的回调；必须非阻塞（内部自带限流与丢弃）。
type Emit func(channelID string, f model.Frame)

// Adapter = 一个 DataSource 实例的「连接管理器」。
// 生命周期：New → Start（由 Manager 在首订阅时调用）→ Channels/Subscribe → Close。
type Adapter interface {
	// DataSourceID 返回所属数据源 ID。
	DataSourceID() string

	// Start 建立底层连接；幂等。失败返回 error，由 Manager 进入退避重连。
	Start(ctx context.Context) error

	// Close 释放底层连接与其派生 goroutine；必须可重复调用，且保证 WaitGroup 归零。
	Close() error

	// Health 轻量探活（供重连判定与状态显示），超时由 ctx 控制。
	Health(ctx context.Context) Health

	// ListChannels 发现该源下可用通道；发现失败应返回已持久化通道 + error（软失败）。
	ListChannels(ctx context.Context) ([]ChannelSpec, error)

	// StartChannel 让适配器开始为某通道产数据（通过 emit 投递）。幂等。
	StartChannel(ctx context.Context, channelID string, spec ChannelSpec, emit Emit) error

	// StopChannel 停止该通道的数据产出；不关闭底层连接。幂等。
	StopChannel(channelID string) error
}

// Factory 由 Registry 按 kind 分派。
type Factory func(ds *model.DataSource, log *slog.Logger) (Adapter, error)

// ————————————————— 运行时依赖（进程启动时注入一次） —————————————————

// DataSourceLoader 为 Manager/适配器读取持久化数据的能力契约。
type DataSourceLoader interface {
	GetDataSource(id string) (*model.DataSource, error)
	GetChannel(id string) (*model.Channel, error)
	ListChannelsBySource(dataSourceID string) ([]model.Channel, error)
	ConnParamsFor(id string) (map[string]any, error)
}

// Env 为适配器工厂需要的运行时环境（配置 / 总线 / 持久化读取）。
type Env struct {
	Cfg   *config.Config
	Bus   *bus.Bus
	Store DataSourceLoader
}

var (
	envMu   sync.RWMutex
	envInst *Env
)

// Setup 在进程启动时注入运行时环境（只调用一次）。
func Setup(e *Env) {
	envMu.Lock()
	envInst = e
	envMu.Unlock()
}

// EnvOf 返回当前运行时环境（未 Setup 时返回 nil）。
func EnvOf() *Env {
	envMu.RLock()
	defer envMu.RUnlock()
	return envInst
}

// ————————————————— 注册表 —————————————————

var (
	registryMu sync.RWMutex
	registry   = map[model.DataSourceKind]Factory{}
)

// Register 注册某类数据源的适配器工厂（由各适配器包在 init() 中调用）。
func Register(kind model.DataSourceKind, f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[kind] = f
}

// New 按 kind 创建适配器实例。
func New(kind model.DataSourceKind, ds *model.DataSource, log *slog.Logger) (Adapter, error) {
	registryMu.RLock()
	f, ok := registry[kind]
	registryMu.RUnlock()
	if !ok {
		return nil, apperr.Newf(apperr.UnsupportedType, "未注册的数据源类型: %s", kind)
	}
	if log == nil {
		log = logx.With("module", string(kind))
	}
	return f(ds, log)
}

// RegisteredKinds 返回已注册的数据源类型。
func RegisteredKinds() []model.DataSourceKind {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]model.DataSourceKind, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	return out
}

// SpecFromChannel 把持久化通道转换为 ChannelSpec。
func SpecFromChannel(ch *model.Channel) ChannelSpec {
	meta := ch.Meta
	if meta == nil {
		meta = map[string]any{}
	}
	return ChannelSpec{
		Name:        ch.Name,
		PayloadType: ch.PayloadType,
		Meta:        meta,
		DefaultHz:   ch.RateLimitHz,
	}
}

// ParamsTo 把 connParams（map[string]any）按目标结构体解码，字段缺失时保留零值。
// 各适配器统一用它避免重复写反射代码。
func ParamsTo[T any](params map[string]any) (T, error) {
	var out T
	if len(params) == 0 {
		return out, nil
	}
	b, err := json.Marshal(params)
	if err != nil {
		return out, apperr.Wrap(err, apperr.InvalidParam, "连接参数序列化失败")
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, apperr.Wrap(err, apperr.InvalidParam, "连接参数解析失败")
	}
	return out, nil
}
