// Package config 负责 MonitorAll 后端全部配置的加载：默认值填充、yaml 解析、
// 环境变量覆盖（MONITORALL_*）。所有默认常量集中在本文件，禁止在别处写裸魔法值。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ————————————————— 全局常量 —————————————————

// Version 为后端版本号，启动横幅与 /api/v1/system/runtime 使用。
const Version = "1.0.0"

// GitCommit 为构建时的 git 提交号（含 dirty 后缀），由 Makefile 通过 ldflags 注入：
//
//	-X github.com/monitorall/monitorall/internal/config.GitCommit=...
//
// 为什么需要：两个不同版本的二进制长得一模一样，回滚时无法确认机器上跑的是哪一版。
// "可回滚"的前提是"能认出自己在哪"（部署侧 R2/R3 之外第三个会让回滚失败的隐蔽原因）。
//
// 未注入时保持 "unknown"，而不是报错或假装是干净版本 —— 解压出来的源码包没有 .git，
// 构建必须照常成功（见 Makefile 里的 $(shell ...) 惰性求值）。
var GitCommit = "unknown"

// CurrentConfigSchemaVersion 为【配置文件格式】版本（config.yaml 的 schemaVersion 字段）。
//
// ⚠️ 它与【数据库结构】版本 version.SchemaVersion 是两个不同概念，不要互相赋值或互相校验。
// 历史问题（H9）：此前两者都叫 CurrentSchemaVersion 且各写一份（本文件=1、store=2），
// 已实际漂移，导致启动横幅打印错误的 "schema v1"。现按语义拆开，数据库版本统一收敛到
// internal/version.SchemaVersion，横幅改为打印数据库版本。
const CurrentConfigSchemaVersion = 1

// EnvPrefix 为环境变量覆盖的前缀。
const EnvPrefix = "MONITORALL_"

// WSProtocolVersion 为前后端 WebSocket 协议版本号（不匹配即拒绝）。
const WSProtocolVersion = 1

// ————————————————— 默认端口与地址 —————————————————

const (
	DefaultHTTPAddr          = ":8080"
	DefaultHTTPSAddr         = ":8443"
	DefaultCertDir           = "./data/cert"
	DefaultDataDir           = "./data"
	DefaultSecretKeyFile     = "./data/secret.key"
	DefaultReadTimeoutMs     = 15000
	DefaultWriteTimeoutMs    = 15000
	DefaultShutdownTimeoutMs = 10000
)

// ————————————————— MediaMTX 模式与协议常量 —————————————————

const (
	// MediaMTXModeEmbedded 表示由 docker-compose 自带的 MediaMTX 容器提供服务。
	MediaMTXModeEmbedded = "embedded"
	// MediaMTXModeExternal 表示使用外部已部署的 MediaMTX。
	MediaMTXModeExternal = "external"

	// PublicSchemeAuto 表示跟随后端是否启用 TLS 自动选择 http/https。
	PublicSchemeAuto = "auto"
	// PublicSchemeHTTP 强制 http。
	PublicSchemeHTTP = "http"
	// PublicSchemeHTTPS 强制 https。
	PublicSchemeHTTPS = "https"

	// EncryptionAuto 表示仅当后端启用 TLS 时才打开 MediaMTX 加密。
	EncryptionAuto = "auto"
	// EncryptionNever 从不加密。
	EncryptionNever = "never"
	// EncryptionAlways 总是加密。
	EncryptionAlways = "always"

	// VideoProtocolWebRTC 为首选视频协议（WHEP）。
	VideoProtocolWebRTC = "webrtc"
	// VideoProtocolHLS 为默认降级视频协议。
	VideoProtocolHLS = "hls"
	// VideoProtocolFLV 为可选降级协议，仅在配置 flvBase 时启用。
	VideoProtocolFLV = "flv"

	// PublishProtocolWHIP 为接收推流地址类型：WebRTC WHIP。
	PublishProtocolWHIP = "whip"
	// PublishProtocolRTMP 为接收推流地址类型：RTMP。
	PublishProtocolRTMP = "rtmp"

	// VideoModePull 为视频接入模式：主动拉取远端流（默认，rtmp/rtsp 均适用）。
	VideoModePull = "pull"
	// VideoModePublish 为视频接入模式：接收远端推流（WHIP / RTMP），路径 source 固定 publisher。
	VideoModePublish = "publish"

	// RTSPTransportTCP 为 RTSP 拉流传输方式：TCP（默认；UDP 在摄像头场景下易花屏或连不上）。
	RTSPTransportTCP = "tcp"
	// RTSPTransportUDP 为 RTSP 拉流传输方式：UDP。
	RTSPTransportUDP = "udp"
	// RTSPTransportAuto 为 RTSP 拉流传输方式：自动协商。
	RTSPTransportAuto = "automatic"

	DefaultMediaMTXMode         = MediaMTXModeEmbedded
	DefaultMediaMTXAPIBase      = "http://mediamtx:9997"
	DefaultMediaMTXPublicHost   = ""
	DefaultMediaMTXPublicScheme = PublicSchemeAuto
	DefaultPortRTMP             = 1936
	DefaultPortHLS              = 8888
	DefaultPortWebRTC           = 8889
	DefaultMediaMTXEncryption   = EncryptionAuto
	DefaultMediaMTXFLVBase      = ""

	// DefaultRTMPPort 为 RTMP URL 未显式给端口时的缺省值。
	DefaultRTMPPort = 1935
	// DefaultRTSPPort 为 RTSP URL 未显式给端口时的缺省值（海康/大华等摄像头默认 554）。
	DefaultRTSPPort = 554

	// MediaMTXPathPollMs 为视频适配器的路径状态轮询周期。
	MediaMTXPathPollMs = 2000

	// DefaultRetryPreferredSec 为降级后重试首选视频协议的默认间隔（秒）。
	DefaultRetryPreferredSec = 30
)

// ————————————————— 总线默认参数 —————————————————

const (
	DefaultMaxHz                = 20
	DefaultImageMaxHz           = 10
	DefaultVideoStatusHz        = 0.5
	DefaultFrameBatchMs         = 20
	DefaultFrameBatchMax        = 64
	DefaultConnQueueDepth       = 256
	DefaultRingCapacity         = 1200
	DefaultBackpressureNotifyMs = 1000
	DefaultPingIntervalMs       = 15000
	DefaultWriteWaitMs          = 3000
)

// ————————————————— 适配器默认参数 —————————————————

const (
	DefaultHealthIntervalMs  = 3000
	DefaultReconnectBaseMs   = 1000
	DefaultReconnectMaxMs    = 30000
	DefaultReconnectJitter   = 0.2
	DefaultReconnectMaxRetry = -1 // -1 = 无限重试
	DefaultSampleTimeoutMs   = 3000
)

// ————————————————— HTTP 轮询默认参数 —————————————————

const (
	DefaultMinIntervalMs    = 100
	DefaultTimeoutMs        = 3000
	DefaultFailureThreshold = 3
)

// ————————————————— 品牌（顶栏）默认值 —————————————————

const (
	// DefaultBrandingTitle 为顶栏主标题默认值（也是 Title 被清空时的回落值）。
	DefaultBrandingTitle = "MonitorAll"
	// DefaultBrandingSubtitle 为顶栏副标题默认值。
	DefaultBrandingSubtitle = "多源异构监控看板"
	// DefaultBrandingLogoURL 为默认无 Logo（前端回落成纯文字）。
	DefaultBrandingLogoURL = ""

	// BrandingTitleMaxRunes / SubtitleMaxRunes / LabelMaxRunes 为文本长度上限（按「字符」计，
	// 不是字节 —— 中文一个字 3 字节，按字节算会让中文用户只能填 13 个字）。
	BrandingTitleMaxRunes    = 40
	BrandingSubtitleMaxRunes = 60
	WeatherLabelMaxRunes     = 40

	// BrandingLogoPrefix 为后端自有 Logo 端点的前缀：LogoURL 要么指向外部 http(s)，
	// 要么指向本端点。javascript: / data: 这类 scheme 一律拒绝（存储型 XSS）。
	BrandingLogoPrefix = "/api/v1/system/branding/logo"
)

// ————————————————— 天气默认值与白名单 —————————————————

const (
	// WeatherProviderOpenMeteo 为当前唯一支持的天气服务商（免 Key、允许服务端直连）。
	WeatherProviderOpenMeteo = "open-meteo"

	// DefaultWeatherEnabled 为默认关闭：开启后会向第三方发请求，属于"改变了对外行为"，
	// 必须显式开启而不是默认开。
	DefaultWeatherEnabled = false
	// DefaultWeatherRefreshMin 为天气缓存分钟数。
	DefaultWeatherRefreshMin = 10
	// DefaultWeatherTimeoutMs 为请求第三方天气接口的超时。
	DefaultWeatherTimeoutMs = 5000

	// WeatherRefreshMinMin / WeatherRefreshMinMax 为缓存分钟数的夹取区间。
	WeatherRefreshMinMin = 1
	WeatherRefreshMinMax = 120
)

// WeatherProviders 为允许的服务商白名单（PUT /api/v1/settings 按此校验，禁止任意填）。
var WeatherProviders = []string{WeatherProviderOpenMeteo}

// IsWeatherProviderAllowed 判断服务商是否在白名单内。
func IsWeatherProviderAllowed(p string) bool {
	for _, v := range WeatherProviders {
		if v == p {
			return true
		}
	}
	return false
}

// ————————————————— Web / 日志默认值 —————————————————

const (
	DefaultTheme      = "dark"
	DefaultLogLevel   = "info"
	DefaultLogFormat  = "json"
	DefaultLogFile    = ""
	DefaultMaxSizeMb  = 50
	DefaultMaxBackups = 7
)

// ————————————————— WebSocket 常量 —————————————————

const (
	// WSPath 为 WebSocket 端点路径（与 REST 同端口）。
	WSPath = "/api/v1/ws"
	// APIPrefix 为 REST 前缀。
	APIPrefix = "/api/v1"
	// HelloTimeoutMs 为握手后等待 hello 的超时。
	HelloTimeoutMs = 5000
	// PongTimeoutMs 为等待 pong 的超时。
	PongTimeoutMs = 3000
	// MaxMissedPong 为允许连续丢失的 pong 次数。
	MaxMissedPong = 3
	// DefaultDashboardName 为首次启动自动创建的看板名（D8）。
	DefaultDashboardName = "默认看板"
	// AdapterCloseTimeoutMs 为关闭适配器时 WaitGroup 的超时保护（G2）。
	AdapterCloseTimeoutMs = 10000
)

// Config 是全部配置的根结构，yaml 键名与 config.example.yaml 严格一致。
type Config struct {
	// SchemaVersion 为【配置文件格式】版本，与数据库结构版本 version.SchemaVersion 无关。
	SchemaVersion int            `yaml:"schemaVersion"`
	Server        ServerConfig   `yaml:"server"`
	Store         StoreConfig    `yaml:"store"`
	MediaMTX      MediaMTXConfig `yaml:"mediamtx"`
	Bus           BusConfig      `yaml:"bus"`
	Adapter       AdapterConfig  `yaml:"adapter"`
	HTTPPoll      HTTPPollConfig `yaml:"httpPoll"`
	Web           WebConfig      `yaml:"web"`
	Log           LogConfig      `yaml:"log"`
	// Branding 为顶栏品牌（标题/副标题/Logo）。运行期可在 DB 里覆盖，见 internal/api/settings.go。
	Branding BrandingConfig `yaml:"branding"`
	// Weather 为顶栏天气挂件配置。默认关闭，开启后由后端代理查询（浏览器不直连第三方）。
	Weather WeatherConfig `yaml:"weather"`
}

// BrandingConfig 为顶栏品牌配置。
type BrandingConfig struct {
	Title    string `yaml:"title"`
	Subtitle string `yaml:"subtitle"`
	LogoURL  string `yaml:"logoUrl"`
}

// WeatherConfig 为天气挂件配置。
type WeatherConfig struct {
	// Enabled 为总开关，默认 false —— 开启后会向第三方发请求，必须显式开启。
	Enabled bool `yaml:"enabled"`
	// Provider 为服务商，必须是 WeatherProviders 白名单之一。
	Provider string `yaml:"provider"`
	// Lat / Lon 为 WGS-84 坐标（与 map_nodes.fallback_lat/lon 同一口径）。
	Lat float64 `yaml:"lat"`
	Lon float64 `yaml:"lon"`
	// Label 为可选显示名（如"深圳南山"），留空则前端只显示温度。
	Label string `yaml:"label"`
	// RefreshMin 为缓存分钟数（同坐标同服务商在此窗口内不重复出网）。
	RefreshMin int `yaml:"refreshMin"`
	// TimeoutMs 为请求第三方接口的超时（含连接与读取）。
	TimeoutMs int `yaml:"timeoutMs"`
}

// CoordsSet 判断坐标是否已填写。
//
// (0,0) 视为「未设置」：它是几内亚湾里的一个合法坐标，但作为"用户忘了填"的默认值远更常见，
// 且 UI 需要据此提示"请先填写坐标"。因此这里用 0 做哨兵 —— 真正要看几内亚湾天气的用户不存在。
// 注意这与 map_nodes 的口径不同（那边 (0,0) 是合法坐标、用 NULL 表达"无兜底"）：
// 全局天气只有一个坐标、没有"空"的 SQL 表达，只能靠哨兵值。
func (w WeatherConfig) CoordsSet() bool { return w.Lat != 0 || w.Lon != 0 }

// CoordsValid 判断坐标是否落在合法经纬度区间内。
func (w WeatherConfig) CoordsValid() bool {
	return w.Lat >= -90 && w.Lat <= 90 && w.Lon >= -180 && w.Lon <= 180
}

// ServerConfig 为 HTTP/HTTPS 双监听与数据目录相关配置。
type ServerConfig struct {
	HTTPAddr          string `yaml:"httpAddr"`
	HTTPSAddr         string `yaml:"httpsAddr"`
	TLSEnabled        bool   `yaml:"tlsEnabled"`
	AutoCert          bool   `yaml:"autoCert"`
	CertDir           string `yaml:"certDir"`
	LANHost           string `yaml:"lanHost"`
	DataDir           string `yaml:"dataDir"`
	SecretKeyFile     string `yaml:"secretKeyFile"`
	ReadTimeoutMs     int    `yaml:"readTimeoutMs"`
	WriteTimeoutMs    int    `yaml:"writeTimeoutMs"`
	ShutdownTimeoutMs int    `yaml:"shutdownTimeoutMs"`
}

// StoreConfig 为 SQLite 相关配置。
type StoreConfig struct {
	DSN string `yaml:"dsn"`
	WAL bool   `yaml:"wal"`
}

// MediaMTXConfig 为 MediaMTX 编排相关配置。
type MediaMTXConfig struct {
	Mode         string `yaml:"mode"`
	APIBase      string `yaml:"apiBase"`
	PublicHost   string `yaml:"publicHost"`
	PublicScheme string `yaml:"publicScheme"`
	PortRTMP     int    `yaml:"portRtmp"`
	PortHLS      int    `yaml:"portHls"`
	PortWebRTC   int    `yaml:"portWebrtc"`
	Encryption   string `yaml:"encryption"`
	FLVBase      string `yaml:"flvBase"`
}

// BusConfig 为帧总线与限流相关配置。
type BusConfig struct {
	DefaultMaxHz         float64 `yaml:"defaultMaxHz"`
	ImageMaxHz           float64 `yaml:"imageMaxHz"`
	VideoStatusHz        float64 `yaml:"videoStatusHz"`
	FrameBatchMs         int     `yaml:"frameBatchMs"`
	FrameBatchMax        int     `yaml:"frameBatchMax"`
	ConnQueueDepth       int     `yaml:"connQueueDepth"`
	RingCapacity         int     `yaml:"ringCapacity"`
	BackpressureNotifyMs int     `yaml:"backpressureNotifyMs"`
	PingIntervalMs       int     `yaml:"pingIntervalMs"`
	WriteWaitMs          int     `yaml:"writeWaitMs"`
}

// AdapterConfig 为适配器生命周期与重连相关配置。
type AdapterConfig struct {
	HealthIntervalMs  int     `yaml:"healthIntervalMs"`
	ReconnectBaseMs   int     `yaml:"reconnectBaseMs"`
	ReconnectMaxMs    int     `yaml:"reconnectMaxMs"`
	ReconnectJitter   float64 `yaml:"reconnectJitter"`
	ReconnectMaxRetry int     `yaml:"reconnectMaxRetry"`
	SampleTimeoutMs   int     `yaml:"sampleTimeoutMs"`
}

// HTTPPollConfig 为 HTTP 轮询适配器的默认参数。
type HTTPPollConfig struct {
	MinIntervalMs    int `yaml:"minIntervalMs"`
	DefaultTimeoutMs int `yaml:"defaultTimeoutMs"`
	FailureThreshold int `yaml:"failureThreshold"`
}

// WebConfig 为前端运行所需配置。
type WebConfig struct {
	AMapKey string `yaml:"amapKey"`
	// AMapSecurityCode 为高德「安全密钥 securityJsCode」。
	// 2021-12-02 之后申请的 Key 必须配合安全密钥使用，否则样式服务等能力会静默失败
	// （表现为 setMapStyle 切换暗色/自定义样式不生效，底图始终回落标准样式）。
	// 在高德控制台「应用管理 → 我的应用」里与 Key 一同展示。
	AMapSecurityCode string `yaml:"amapSecurityCode"`
	// AMapForceWebGL 控制是否在加载 JSAPI 前设置 window.forceWebGL / forceWebGLBaseRender。
	// JSAPI 默认以 failIfMajorPerformanceCaveat 取 WebGL 上下文：无 GPU / 软件渲染 / 部分手机 WebView
	// 会被判定为"性能不足"从而**不启用 WebGL 绘制**，控制台报"浏览器版本过低"，
	// 表现为底图能出、但 mapStyle/setMapStyle 一律不生效（卫星图层这类纯瓦片不受影响）。
	// 置为 true 可强制启用 WebGL 渲染（高德官方给出的解法）。
	AMapForceWebGL bool   `yaml:"amapForceWebGL"`
	Theme          string `yaml:"theme"`
}

// LogConfig 为日志配置。
type LogConfig struct {
	Level      string `yaml:"level"`
	Format     string `yaml:"format"`
	File       string `yaml:"file"`
	MaxSizeMb  int    `yaml:"maxSizeMb"`
	MaxBackups int    `yaml:"maxBackups"`
}

// DefaultConfig 返回全量默认配置（不含环境变量覆盖）。
func DefaultConfig() *Config {
	return &Config{
		SchemaVersion: CurrentConfigSchemaVersion,
		Server: ServerConfig{
			HTTPAddr:          DefaultHTTPAddr,
			HTTPSAddr:         DefaultHTTPSAddr,
			TLSEnabled:        true,
			AutoCert:          true,
			CertDir:           DefaultCertDir,
			LANHost:           "",
			DataDir:           DefaultDataDir,
			SecretKeyFile:     DefaultSecretKeyFile,
			ReadTimeoutMs:     DefaultReadTimeoutMs,
			WriteTimeoutMs:    DefaultWriteTimeoutMs,
			ShutdownTimeoutMs: DefaultShutdownTimeoutMs,
		},
		Store: StoreConfig{
			DSN: "./data/monitorall.db",
			WAL: true,
		},
		MediaMTX: MediaMTXConfig{
			Mode:         DefaultMediaMTXMode,
			APIBase:      DefaultMediaMTXAPIBase,
			PublicHost:   DefaultMediaMTXPublicHost,
			PublicScheme: DefaultMediaMTXPublicScheme,
			PortRTMP:     DefaultPortRTMP,
			PortHLS:      DefaultPortHLS,
			PortWebRTC:   DefaultPortWebRTC,
			Encryption:   DefaultMediaMTXEncryption,
			FLVBase:      DefaultMediaMTXFLVBase,
		},
		Bus: BusConfig{
			DefaultMaxHz:         DefaultMaxHz,
			ImageMaxHz:           DefaultImageMaxHz,
			VideoStatusHz:        DefaultVideoStatusHz,
			FrameBatchMs:         DefaultFrameBatchMs,
			FrameBatchMax:        DefaultFrameBatchMax,
			ConnQueueDepth:       DefaultConnQueueDepth,
			RingCapacity:         DefaultRingCapacity,
			BackpressureNotifyMs: DefaultBackpressureNotifyMs,
			PingIntervalMs:       DefaultPingIntervalMs,
			WriteWaitMs:          DefaultWriteWaitMs,
		},
		Adapter: AdapterConfig{
			HealthIntervalMs:  DefaultHealthIntervalMs,
			ReconnectBaseMs:   DefaultReconnectBaseMs,
			ReconnectMaxMs:    DefaultReconnectMaxMs,
			ReconnectJitter:   DefaultReconnectJitter,
			ReconnectMaxRetry: DefaultReconnectMaxRetry,
			SampleTimeoutMs:   DefaultSampleTimeoutMs,
		},
		HTTPPoll: HTTPPollConfig{
			MinIntervalMs:    DefaultMinIntervalMs,
			DefaultTimeoutMs: DefaultTimeoutMs,
			FailureThreshold: DefaultFailureThreshold,
		},
		Web: WebConfig{
			AMapKey:          "",
			AMapSecurityCode: "",
			// 默认开启：绝大多数"样式不生效"的环境问题都由未启用 WebGL 绘制导致。
			AMapForceWebGL: true,
			Theme:          DefaultTheme,
		},
		Log: LogConfig{
			Level:      DefaultLogLevel,
			Format:     DefaultLogFormat,
			File:       DefaultLogFile,
			MaxSizeMb:  DefaultMaxSizeMb,
			MaxBackups: DefaultMaxBackups,
		},
		Branding: BrandingConfig{
			Title:    DefaultBrandingTitle,
			Subtitle: DefaultBrandingSubtitle,
			LogoURL:  DefaultBrandingLogoURL,
		},
		Weather: WeatherConfig{
			Enabled:    DefaultWeatherEnabled,
			Provider:   WeatherProviderOpenMeteo,
			Lat:        0,
			Lon:        0,
			Label:      "",
			RefreshMin: DefaultWeatherRefreshMin,
			TimeoutMs:  DefaultWeatherTimeoutMs,
		},
	}
}

// Load 从 path 加载配置；path 为空时依次尝试 ./config.yaml、./data/config.yaml，
// 都不存在则使用纯默认配置（不报错，便于首次启动）。
// 优先级：环境变量 > yaml > 默认值。
func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	candidates := make([]string, 0, 3)
	if path != "" {
		candidates = append(candidates, path)
	} else {
		candidates = append(candidates, "config.yaml", filepath.Join(DefaultDataDir, "config.yaml"))
	}

	loaded := false
	for _, c := range candidates {
		data, err := os.ReadFile(c)
		if err != nil {
			continue
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("解析配置文件 %s 失败: %w", c, err)
		}
		loaded = true
		break
	}
	_ = loaded

	// 结构合法性兜底：yaml 里显式给了空串时回落到默认值
	cfg.applyDefaults()
	if err := applyEnv(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyDefaults 把零值字段回填为默认值（yaml 中未出现的字段）。
func (c *Config) applyDefaults() {
	def := DefaultConfig()
	if c.SchemaVersion == 0 {
		c.SchemaVersion = def.SchemaVersion
	}
	c.Server.HTTPAddr = firstNonEmpty(c.Server.HTTPAddr, def.Server.HTTPAddr)
	c.Server.HTTPSAddr = firstNonEmpty(c.Server.HTTPSAddr, def.Server.HTTPSAddr)
	c.Server.CertDir = firstNonEmpty(c.Server.CertDir, def.Server.CertDir)
	c.Server.DataDir = firstNonEmpty(c.Server.DataDir, def.Server.DataDir)
	c.Server.SecretKeyFile = firstNonEmpty(c.Server.SecretKeyFile, def.Server.SecretKeyFile)
	c.Server.ReadTimeoutMs = firstPositive(c.Server.ReadTimeoutMs, def.Server.ReadTimeoutMs)
	c.Server.WriteTimeoutMs = firstPositive(c.Server.WriteTimeoutMs, def.Server.WriteTimeoutMs)
	c.Server.ShutdownTimeoutMs = firstPositive(c.Server.ShutdownTimeoutMs, def.Server.ShutdownTimeoutMs)

	c.Store.DSN = firstNonEmpty(c.Store.DSN, def.Store.DSN)

	c.MediaMTX.Mode = firstNonEmpty(c.MediaMTX.Mode, def.MediaMTX.Mode)
	c.MediaMTX.APIBase = firstNonEmpty(c.MediaMTX.APIBase, def.MediaMTX.APIBase)
	c.MediaMTX.PublicScheme = firstNonEmpty(c.MediaMTX.PublicScheme, def.MediaMTX.PublicScheme)
	c.MediaMTX.PortRTMP = firstPositive(c.MediaMTX.PortRTMP, def.MediaMTX.PortRTMP)
	c.MediaMTX.PortHLS = firstPositive(c.MediaMTX.PortHLS, def.MediaMTX.PortHLS)
	c.MediaMTX.PortWebRTC = firstPositive(c.MediaMTX.PortWebRTC, def.MediaMTX.PortWebRTC)
	c.MediaMTX.Encryption = firstNonEmpty(c.MediaMTX.Encryption, def.MediaMTX.Encryption)

	c.Bus.DefaultMaxHz = firstPositiveF(c.Bus.DefaultMaxHz, def.Bus.DefaultMaxHz)
	c.Bus.ImageMaxHz = firstPositiveF(c.Bus.ImageMaxHz, def.Bus.ImageMaxHz)
	c.Bus.VideoStatusHz = firstPositiveF(c.Bus.VideoStatusHz, def.Bus.VideoStatusHz)
	c.Bus.FrameBatchMs = firstPositive(c.Bus.FrameBatchMs, def.Bus.FrameBatchMs)
	c.Bus.FrameBatchMax = firstPositive(c.Bus.FrameBatchMax, def.Bus.FrameBatchMax)
	c.Bus.ConnQueueDepth = firstPositive(c.Bus.ConnQueueDepth, def.Bus.ConnQueueDepth)
	c.Bus.RingCapacity = firstPositive(c.Bus.RingCapacity, def.Bus.RingCapacity)
	c.Bus.BackpressureNotifyMs = firstPositive(c.Bus.BackpressureNotifyMs, def.Bus.BackpressureNotifyMs)
	c.Bus.PingIntervalMs = firstPositive(c.Bus.PingIntervalMs, def.Bus.PingIntervalMs)
	c.Bus.WriteWaitMs = firstPositive(c.Bus.WriteWaitMs, def.Bus.WriteWaitMs)

	c.Adapter.HealthIntervalMs = firstPositive(c.Adapter.HealthIntervalMs, def.Adapter.HealthIntervalMs)
	c.Adapter.ReconnectBaseMs = firstPositive(c.Adapter.ReconnectBaseMs, def.Adapter.ReconnectBaseMs)
	c.Adapter.ReconnectMaxMs = firstPositive(c.Adapter.ReconnectMaxMs, def.Adapter.ReconnectMaxMs)
	if c.Adapter.ReconnectJitter == 0 {
		c.Adapter.ReconnectJitter = def.Adapter.ReconnectJitter
	}
	if c.Adapter.ReconnectMaxRetry == 0 {
		c.Adapter.ReconnectMaxRetry = def.Adapter.ReconnectMaxRetry
	}
	c.Adapter.SampleTimeoutMs = firstPositive(c.Adapter.SampleTimeoutMs, def.Adapter.SampleTimeoutMs)

	c.HTTPPoll.MinIntervalMs = firstPositive(c.HTTPPoll.MinIntervalMs, def.HTTPPoll.MinIntervalMs)
	c.HTTPPoll.DefaultTimeoutMs = firstPositive(c.HTTPPoll.DefaultTimeoutMs, def.HTTPPoll.DefaultTimeoutMs)
	c.HTTPPoll.FailureThreshold = firstPositive(c.HTTPPoll.FailureThreshold, def.HTTPPoll.FailureThreshold)

	c.Web.Theme = firstNonEmpty(c.Web.Theme, def.Web.Theme)
	c.Log.Level = firstNonEmpty(c.Log.Level, def.Log.Level)
	c.Log.Format = firstNonEmpty(c.Log.Format, def.Log.Format)
	c.Log.MaxSizeMb = firstPositive(c.Log.MaxSizeMb, def.Log.MaxSizeMb)
	c.Log.MaxBackups = firstPositive(c.Log.MaxBackups, def.Log.MaxBackups)

	c.Branding.Title = firstNonEmpty(c.Branding.Title, def.Branding.Title)
	c.Branding.Subtitle = firstNonEmpty(c.Branding.Subtitle, def.Branding.Subtitle)
	// LogoURL 故意走 firstNonEmpty：yaml 里显式留空与"不写"等价，都表示无 Logo。
	c.Branding.LogoURL = firstNonEmpty(c.Branding.LogoURL, def.Branding.LogoURL)

	c.Weather.Provider = firstNonEmpty(c.Weather.Provider, def.Weather.Provider)
	c.Weather.RefreshMin = firstPositive(c.Weather.RefreshMin, def.Weather.RefreshMin)
	c.Weather.TimeoutMs = firstPositive(c.Weather.TimeoutMs, def.Weather.TimeoutMs)
}

// PublicScheme 依据 publicScheme 与后端是否启用 TLS 计算最终 scheme。
func (c *Config) PublicScheme() string {
	switch c.MediaMTX.PublicScheme {
	case PublicSchemeHTTP:
		return PublicSchemeHTTP
	case PublicSchemeHTTPS:
		return PublicSchemeHTTPS
	default:
		if c.Server.TLSEnabled {
			return PublicSchemeHTTPS
		}
		return PublicSchemeHTTP
	}
}

// MediaMTXEncryption 依据 encryption 配置与后端 TLS 状态返回是否启用 MediaMTX 加密。
func (c *Config) MediaMTXEncryption() bool {
	switch c.MediaMTX.Encryption {
	case EncryptionAlways:
		return true
	case EncryptionNever:
		return false
	default:
		return c.Server.TLSEnabled
	}
}

// applyEnv 递归遍历 Config，按 MONITORALL_<路径大写下划线> 覆盖字段。
func applyEnv(cfg *Config) error {
	v := reflect.ValueOf(cfg).Elem()
	return walkEnv(v, "")
}

// walkEnv 深度优先遍历结构体字段并应用环境变量。
func walkEnv(v reflect.Value, prefix string) error {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" { // 未导出
			continue
		}
		tag := field.Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		envName := EnvPrefix + strings.ToUpper(prefix+name)
		fv := v.Field(i)
		if fv.Kind() == reflect.Struct {
			if err := walkEnv(fv, prefix+name+"_"); err != nil {
				return err
			}
			continue
		}
		raw, ok := os.LookupEnv(envName)
		if !ok {
			continue
		}
		if err := setByKind(fv, raw); err != nil {
			return fmt.Errorf("环境变量 %s 解析失败: %w", envName, err)
		}
	}
	return nil
}

// setByKind 把字符串按字段类型写入。
func setByKind(fv reflect.Value, raw string) error {
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return err
		}
		fv.SetBool(b)
	case reflect.Int, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return err
		}
		fv.SetInt(n)
	case reflect.Float64:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return err
		}
		fv.SetFloat(f)
	default:
		return fmt.Errorf("不支持的类型 %s", fv.Kind())
	}
	return nil
}

func firstNonEmpty(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func firstPositive(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

func firstPositiveF(v, def float64) float64 {
	if v <= 0 {
		return def
	}
	return v
}

// EnsureDirs 创建运行时所需目录（数据目录 / 证书目录 / 数据库与日志所在目录）。
func EnsureDirs(cfg *Config) error {
	dirs := []string{
		cfg.Server.DataDir,
		filepath.Join(cfg.Server.DataDir, "imports"),
		filepath.Join(cfg.Server.DataDir, "branding"),
		cfg.Server.CertDir,
		filepath.Dir(cfg.Store.DSN),
		filepath.Dir(cfg.Server.SecretKeyFile),
	}
	if cfg.Log.File != "" {
		dirs = append(dirs, filepath.Dir(cfg.Log.File))
	}
	for _, d := range dirs {
		if d == "" || d == "." {
			continue
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("创建目录 %s 失败: %w", d, err)
		}
	}
	return nil
}
