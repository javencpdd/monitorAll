// Package weather 提供「当日天气」的【后端代理】查询。
//
// 为什么必须由后端代理、不能让浏览器直连第三方（两个各自独立的硬性理由）：
//
//  1. 混合内容（mixed content）：页面是 https（本服务默认 tlsEnabled=true），
//     而第三方天气接口若只给 http，浏览器会按混合内容静默拦截 —— 不是报错，
//     是请求根本发不出去，前端只能看到一个永远空白的挂件，排障时极难定位。
//     服务端出网不受这条限制。
//  2. 密钥不能下发到前端：open-meteo 当前免 Key，但换高德 / 和风这类需要 Key 的
//     服务商时，Key 必须留在后端。如果一开始就把"浏览器直连"做成架构，
//     换服务商那天就等于把 Key 公开发布，属于不可逆的凭据泄露。
//
// 失败语义（本包最重要的一条约定）：**绝不返回空白或空对象**。
// 拿不到新鲜数据时，有旧数据就返回旧数据并置 stale=true；没有则返回 ok=false
// 加明确的 reason 与中文 message，让前端能区分"暂时拿不到"和"没配置"。
package weather

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/monitorall/monitorall/internal/model"
)

// ————————————————— 常量 —————————————————

// ProviderOpenMeteo 为当前唯一支持的服务商（免 Key）。
const ProviderOpenMeteo = "open-meteo"

// 失败原因（前端据此决定显示文案：隐藏 / 提示配坐标 / 提示暂时不可用）。
const (
	// ReasonDisabled 为天气开关关闭（HTTP 仍 200，前端据此隐藏挂件）。
	ReasonDisabled = "disabled"
	// ReasonCoordsInvalid 为坐标未填或越界。
	ReasonCoordsInvalid = "coords_invalid"
	// ReasonProviderUnreachable 为网络层失败（超时 / DNS / 连接被拒）。
	ReasonProviderUnreachable = "provider_unreachable"
	// ReasonProviderRejected 为第三方返回非 2xx 或响应无法解析。
	ReasonProviderRejected = "provider_rejected"
	// ReasonProviderUnsupported 为配置里的服务商不在已注册列表内。
	//
	// 规格里只列了上面三条；这一条是追加：provider 非法与"第三方拒绝我"是两类
	// 完全不同的问题（前者改配置即可，后者要等对方恢复），混成一个 reason
	// 会让前端只能显示一句笼统的"天气不可用"。
	ReasonProviderUnsupported = "provider_unsupported"
)

const (
	// defaultTimeoutMs 为未显式配置时的出网超时。
	defaultTimeoutMs = 5000
	// defaultRefreshMin 为未显式配置时的缓存分钟数。
	defaultRefreshMin = 10
	// maxResponseBytes 为第三方响应体上限（防止异常响应把内存打爆）。
	maxResponseBytes = 1 << 16 // 64KB
	// coordPrecision 为缓存键的坐标精度（4 位小数 ≈ 11 米，足以区分不同站点，
	// 又能避免前端反复微调坐标导致缓存永远不命中）。
	coordPrecision = 4
)

// 两类出网失败，供调用方（与测试）区分"网络不通"和"对方拒绝"。
var (
	// ErrUnreachable 为网络层失败。
	ErrUnreachable = errors.New("weather provider unreachable")
	// ErrRejected 为第三方返回非 2xx 或响应无法解析。
	ErrRejected = errors.New("weather provider rejected")
)

// Snapshot 为一次天气快照（同时是 GET /api/v1/weather 的响应体）。
//
// ok=true 时 temp/unit/code/text/wind/windUnit 有效；ok=false 时只有 reason/message 有效。
// 之所以不用指针 + omitempty：字段全在且恒有值，前端就不需要处理"字段可能不存在"，
// 也不会出现"请求成功但 temp 是 undefined"这种静默失败。
type Snapshot struct {
	OK bool `json:"ok"`

	Temp     float64 `json:"temp"`
	Unit     string  `json:"unit"`
	Code     int     `json:"code"`
	Text     string  `json:"text"`
	Wind     float64 `json:"wind"`
	WindUnit string  `json:"windUnit"`

	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	Provider string  `json:"provider"`

	// FetchedAtMs 为数据实际取得时刻（stale 数据为旧时刻，前端可显示"更新于 xx"）。
	FetchedAtMs int64 `json:"fetchedAtMs"`
	// Stale 为真表示这是缓存里的旧数据（出网失败时的兜底）。
	Stale bool `json:"stale"`

	// Reason / Message 仅在 ok=false 时有意义。
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// Reading 为服务商返回的一次原始观测（不含坐标与缓存信息）。
type Reading struct {
	Temp     float64
	Unit     string
	Code     int
	Wind     float64
	WindUnit string
}

// Provider 为天气服务商接口。
type Provider interface {
	// Name 返回服务商标识（与 config.WeatherConfig.Provider 对应）。
	Name() string
	// Fetch 取一次观测；失败时应包装 ErrUnreachable 或 ErrRejected。
	Fetch(ctx context.Context, lat, lon float64) (*Reading, error)
}

// Options 为一次查询的参数（每次调用可不同：设置改了坐标立刻生效）。
type Options struct {
	Provider   string
	Lat        float64
	Lon        float64
	RefreshMin int
	TimeoutMs  int
}

// ————————————————— Service —————————————————

// Service 为天气查询服务：内存缓存 + 同键单飞（singleflight）+ 失败兜底。
type Service struct {
	mu       sync.Mutex
	cache    map[string]cacheEntry
	inflight map[string]*call

	providers map[string]Provider
	client    *http.Client
	// now 可注入，供测试推进时间验证 TTL（生产恒为 time.Now）。
	now func() time.Time
}

// cacheEntry 为一条缓存；过期后【不删除】，留作出网失败时的兜底数据源。
type cacheEntry struct {
	snapshot *Snapshot
	expireAt time.Time
}

// call 为一次进行中的出网请求（同键单飞用）。
type call struct {
	done chan struct{}
	snap *Snapshot
	err  error
}

// NewService 构造一个天气服务。timeoutMs 用于 http.Client 级别的硬超时
// （每次请求还会按 Options.TimeoutMs 再套一层 context 超时）。
func NewService(timeoutMs int) *Service {
	if timeoutMs <= 0 {
		timeoutMs = defaultTimeoutMs
	}
	client := &http.Client{
		Timeout: time.Duration(timeoutMs) * time.Millisecond,
		Transport: &http.Transport{
			MaxIdleConns:        4,
			MaxIdleConnsPerHost: 2,
			IdleConnTimeout:     30 * time.Second,
		},
	}
	s := &Service{
		cache:     make(map[string]cacheEntry),
		inflight:  make(map[string]*call),
		providers: make(map[string]Provider),
		client:    client,
		now:       time.Now,
	}
	s.Register(NewOpenMeteo(client, ""))
	return s
}

// Register 注册（或替换）一个服务商。
func (s *Service) Register(p Provider) {
	if p == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.providers[p.Name()] = p
}

// Get 取一次天气快照，【永远返回非 nil】。
//
// 流程：校验坐标 → 查服务商 → 命中新鲜缓存直接返回 → 单飞出网 →
// 成功写缓存返回 / 失败回落到陈旧缓存（stale=true）/ 再失败返回 ok=false + reason。
func (s *Service) Get(ctx context.Context, o Options) *Snapshot {
	if ctx == nil {
		ctx = context.Background()
	}
	if !(o.Lat >= -90 && o.Lat <= 90 && o.Lon >= -180 && o.Lon <= 180) {
		return failure(o, ReasonCoordsInvalid,
			"天气坐标超出合法范围（纬度 -90~90，经度 -180~180）")
	}
	if o.Lat == 0 && o.Lon == 0 {
		// 与 config.WeatherConfig.CoordsSet 同一口径：(0,0) = 未填写。
		return failure(o, ReasonCoordsInvalid, "尚未填写天气坐标，请先在全局设置里填写经纬度")
	}

	p := s.provider(o.Provider)
	if p == nil {
		return failure(o, ReasonProviderUnsupported,
			fmt.Sprintf("不支持的天气服务商 %q，可用：%s", o.Provider, ProviderOpenMeteo))
	}

	key := cacheKey(o.Provider, o.Lat, o.Lon)
	ttl := time.Duration(positiveOr(o.RefreshMin, defaultRefreshMin)) * time.Minute

	// —— 新鲜缓存命中 ——
	s.mu.Lock()
	now := s.now()
	if e, ok := s.cache[key]; ok && now.Before(e.expireAt) {
		s.mu.Unlock()
		out := *e.snapshot
		out.Stale = false
		return &out
	}
	s.mu.Unlock()

	// —— 出网（同键单飞：并发的 N 个请求只打一次第三方）——
	snap, err := s.fetchShared(ctx, key, ttl, func(ctx context.Context) (*Snapshot, error) {
		return s.fetchOne(ctx, p, o)
	})
	if err == nil && snap != nil {
		return snap
	}

	// —— 失败兜底：有旧数据就用旧数据 ——
	s.mu.Lock()
	e, ok := s.cache[key]
	s.mu.Unlock()
	if ok && e.snapshot != nil {
		out := *e.snapshot
		out.OK = true
		out.Stale = true
		out.Reason = ""
		out.Message = ""
		return &out
	}
	return failure(o, reasonOf(err), messageOf(err))
}

// fetchOne 真正打一次第三方并组装快照。
func (s *Service) fetchOne(ctx context.Context, p Provider, o Options) (*Snapshot, error) {
	timeout := time.Duration(positiveOr(o.TimeoutMs, defaultTimeoutMs)) * time.Millisecond
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	reading, err := p.Fetch(reqCtx, o.Lat, o.Lon)
	if err != nil {
		return nil, err
	}
	if reading == nil {
		return nil, fmt.Errorf("%w: 服务商返回空数据", ErrRejected)
	}
	unit := reading.Unit
	if unit == "" {
		unit = "°C"
	}
	windUnit := reading.WindUnit
	if windUnit == "" {
		windUnit = "km/h"
	}
	return &Snapshot{
		OK:          true,
		Temp:        reading.Temp,
		Unit:        unit,
		Code:        reading.Code,
		Text:        WMOText(reading.Code),
		Wind:        reading.Wind,
		WindUnit:    windUnit,
		Lat:         o.Lat,
		Lon:         o.Lon,
		Provider:    p.Name(),
		FetchedAtMs: model.NowMs(),
	}, nil
}

// fetchShared 同键单飞：同一个 key 的并发请求共享一次上游调用。
//
// 为什么需要：顶栏天气是所有页面都要渲染的，刷新时若有 10 个页面同时打开，
// 就是 10 次出网；第三方按 IP 限流时会被打爆，表现为"刷新一下天气就没了"。
// 自己实现而不用 golang.org/x/sync/singleflight：后者不在 go.mod 里，
// 为一个 20 行能写完的机制加依赖不划算（离线构建环境也会多一个拉取点）。
func (s *Service) fetchShared(ctx context.Context, key string, ttl time.Duration,
	do func(context.Context) (*Snapshot, error)) (*Snapshot, error) {
	s.mu.Lock()
	if c, ok := s.inflight[key]; ok {
		s.mu.Unlock()
		select {
		case <-c.done:
			return c.snap, c.err
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: %v", ErrUnreachable, ctx.Err())
		}
	}
	c := &call{done: make(chan struct{})}
	s.inflight[key] = c
	s.mu.Unlock()

	snap, err := do(ctx)

	s.mu.Lock()
	delete(s.inflight, key)
	if err == nil && snap != nil {
		s.cache[key] = cacheEntry{snapshot: snap, expireAt: s.now().Add(ttl)}
	}
	s.mu.Unlock()

	c.snap, c.err = snap, err
	close(c.done)
	return snap, err
}

// provider 查找已注册的服务商。
func (s *Service) provider(name string) Provider {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.providers[name]
}

// cacheKey 用「服务商 + 4 位小数坐标」做缓存键。
func cacheKey(provider string, lat, lon float64) string {
	return provider + "@" +
		strconv.FormatFloat(lat, 'f', coordPrecision, 64) + "," +
		strconv.FormatFloat(lon, 'f', coordPrecision, 64)
}

// failure 构造一条失败快照（不带上一次的 provider 信息时用）。
func failure(o Options, reason, message string) *Snapshot {
	return &Snapshot{
		OK:      false,
		Lat:     o.Lat,
		Lon:     o.Lon,
		Reason:  reason,
		Message: message,
	}
}

// reasonOf 把出网错误映射为对前端可见的 reason。
func reasonOf(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrRejected):
		return ReasonProviderRejected
	case errors.Is(err, ErrUnreachable):
		return ReasonProviderUnreachable
	default:
		// 兜底：宁可归为"连不上"（可重试），也不要归为"对方拒绝"误导用户去查配置。
		return ReasonProviderUnreachable
	}
}

// messageOf 生成面向用户的中文提示。
func messageOf(err error) string {
	switch reasonOf(err) {
	case ReasonProviderRejected:
		return "天气服务商拒绝了本次请求，请稍后重试或更换服务商"
	default:
		return "暂时无法获取天气数据（网络不可达或超时），请稍后重试"
	}
}

// positiveOr 返回正数，否则回落默认值。
func positiveOr(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

// ————————————————— open-meteo 实现 —————————————————

// openMeteoBaseURL 为 open-meteo 免费接口（免 Key，允许服务端直连）。
const openMeteoBaseURL = "https://api.open-meteo.com/v1/forecast"

// openMeteo 为 open-meteo 服务商实现。
type openMeteo struct {
	name    string
	baseURL string
	client  *http.Client
}

// NewOpenMeteo 构造 open-meteo provider；baseURL 留空用官方地址（测试可注入 httptest 服务器）。
func NewOpenMeteo(client *http.Client, baseURL string) Provider {
	if client == nil {
		client = &http.Client{Timeout: defaultTimeoutMs * time.Millisecond}
	}
	if baseURL == "" {
		baseURL = openMeteoBaseURL
	}
	return &openMeteo{name: ProviderOpenMeteo, baseURL: baseURL, client: client}
}

// Name 返回服务商标识。
func (p *openMeteo) Name() string { return p.name }

// openMeteoResp 为 open-meteo 响应中本服务用到的字段（其余字段忽略）。
type openMeteoResp struct {
	Current struct {
		Temperature2m float64 `json:"temperature_2m"`
		WeatherCode   int     `json:"weather_code"`
		WindSpeed10m  float64 `json:"wind_speed_10m"`
	} `json:"current"`
	CurrentUnits struct {
		Temperature2m string `json:"temperature_2m"`
		WindSpeed10m  string `json:"wind_speed_10m"`
	} `json:"current_units"`
	// open-meteo 出错时是 HTTP 200 + {"error":true,"reason":"..."}，必须显式识别，
	// 否则会把"坐标非法"的响应解析成 0℃ / code 0（显示为晴天），属于静默失败。
	Error  bool   `json:"error"`
	Reason string `json:"reason"`
}

// Fetch 请求 open-meteo 当前天气。
func (p *openMeteo) Fetch(ctx context.Context, lat, lon float64) (*Reading, error) {
	u := fmt.Sprintf("%s?latitude=%s&longitude=%s&current=temperature_2m,weather_code,wind_speed_10m&timezone=auto",
		p.baseURL,
		strconv.FormatFloat(lat, 'f', -1, 64),
		strconv.FormatFloat(lon, 'f', -1, 64))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: 构造请求失败: %v", ErrUnreachable, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()

	// 限流读取：第三方若返回异常巨大的响应，不能让它把进程内存打爆
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: 读取响应失败: %v", ErrUnreachable, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: HTTP %d", ErrRejected, resp.StatusCode)
	}
	var out openMeteoResp
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("%w: 响应解析失败: %v", ErrRejected, err)
	}
	if out.Error {
		reason := out.Reason
		if reason == "" {
			reason = "未提供原因"
		}
		return nil, fmt.Errorf("%w: %s", ErrRejected, reason)
	}
	return &Reading{
		Temp:     out.Current.Temperature2m,
		Unit:     out.CurrentUnits.Temperature2m,
		Code:     out.Current.WeatherCode,
		Wind:     out.Current.WindSpeed10m,
		WindUnit: out.CurrentUnits.WindSpeed10m,
	}, nil
}

// ————————————————— WMO weather_code → 中文描述 —————————————————

// wmoText 为 WMO 4677 天气代码到中文描述的映射（覆盖 open-meteo 全部取值）。
var wmoText = map[int]string{
	0: "晴",
	1: "晴间多云", 2: "多云", 3: "阴",
	45: "雾", 48: "雾凇",
	51: "毛毛雨（弱）", 53: "毛毛雨", 55: "毛毛雨（强）",
	56: "冻雨（弱）", 57: "冻雨",
	61: "小雨", 63: "中雨", 65: "大雨",
	66: "冻雨（弱）", 67: "冻雨（强）",
	71: "小雪", 73: "中雪", 75: "大雪",
	77: "米雪",
	80: "阵雨", 81: "阵雨（中）", 82: "强阵雨",
	85: "阵雪", 86: "强阵雪",
	95: "雷雨",
	96: "雷暴伴冰雹", 99: "雷暴伴强冰雹",
}

// WMOText 把 WMO 天气代码转成中文描述。
//
// 未知代码【绝不返回空串】—— 空串会让前端渲染出一个"温度正常但天气栏空白"的挂件，
// 看起来像 CSS bug。带代码返回至少让人知道要去补映射表。
func WMOText(code int) string {
	if t, ok := wmoText[code]; ok && t != "" {
		return t
	}
	return fmt.Sprintf("未知天气（代码 %d）", code)
}
