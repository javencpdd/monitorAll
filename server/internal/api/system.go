package api

import (
	"context"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/model"
)

// ————————————————— 系统接口（架构 §7.3） —————————————————

// MediaMTXRuntime 为前端修正播放地址所需的 MediaMTX 运行期信息。
//
// embedded 为真时 MediaMTX 与后端同机，「浏览器能打开页面的 host」必然也能直连 MediaMTX，
// 前端可据此把播放地址里的 host 替换成当前页面 host；publicHost 只是后端启动时探测到的
// 第一个私有 IPv4（多网卡 / VPN 场景常是浏览器不可达的地址，如 10.42.0.1）。
type MediaMTXRuntime struct {
	Embedded     bool   `json:"embedded"`
	PublicHost   string `json:"publicHost"`
	PublicScheme string `json:"publicScheme"`
	PortHLS      int    `json:"portHls"`
	PortWebRTC   int    `json:"portWebrtc"`
}

// BrandingRuntime 为前端首屏渲染顶栏所需的品牌信息。
//
// 为什么要在 runtime 里下发、而不是让前端再调一次 /api/v1/settings：
// runtime 是首屏第一个请求，晚一步拿到就会出现"先闪一下默认的 MonitorAll，
// 再变成自定义名"的闪烁。品牌名是用户一定会改的项，这个闪烁每次刷新都可见。
type BrandingRuntime struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	LogoURL  string `json:"logoUrl"`
}

// WeatherRuntime 为前端首屏决定是否渲染天气挂件所需的最小信息。
//
// 只下发 enabled 与 provider：坐标 / 缓存 / 超时一律不下发 —— 坐标属于部署位置信息，
// 且将来换需要 Key 的服务商时，任何多余字段都是潜在的泄露面。
type WeatherRuntime struct {
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider"`
}

// RuntimeConfig 为前端启动所需的运行期配置。
type RuntimeConfig struct {
	AMapKey          string          `json:"amapKey"`
	AMapSecurityCode string          `json:"amapSecurityCode"`
	AMapForceWebGL   bool            `json:"amapForceWebGL"`
	WSUrl            string          `json:"wsUrl"`
	ProtocolVersion  int             `json:"protocolVersion"`
	SecureContext    bool            `json:"secureContext"`
	VideoBackends    []string        `json:"videoBackends"`
	LanHosts         []string        `json:"lanHosts"`
	MediaMTX         MediaMTXRuntime `json:"mediaMtx"`
	ServerTimeMs     int64           `json:"serverTimeMs"`
	Branding         BrandingRuntime `json:"branding"`
	Weather          WeatherRuntime  `json:"weather"`
	// Version / Commit 为二进制身份，供前端「关于」分组做排障（与启动横幅同源）。
	//
	// 只给这两项、【不给 dataDir】：本端点无鉴权，dataDir 会泄露服务端文件系统路径，
	// 是三者里唯一提供新信息的；而"我跑的是哪一版"这个排障问题，version + commit 已经够定位
	// （两个不同版本的二进制长得一样，回滚时靠 commit 认人，见 config.GitCommit 的注释）。
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// HealthzResp 为健康检查响应。
type HealthzResp struct {
	OK         bool  `json:"ok"`
	Goroutines int   `json:"goroutines"`
	UptimeSec  int64 `json:"uptimeSec"`
	WSConns    int   `json:"wsConns"`
}

// LANResp 为局域网地址响应。
type LANResp struct {
	Hosts      []string `json:"hosts"`
	HTTPPort   int      `json:"httpPort"`
	HTTPSPort  int      `json:"httpsPort"`
	TLSEnabled bool     `json:"tlsEnabled"`
}

// healthz 返回进程健康信息（goroutines 用于泄漏巡检，G7）。
func (d *Deps) healthz(c *gin.Context) {
	conns := 0
	if d.Hub != nil {
		conns = d.Hub.ConnCount()
	}
	c.JSON(http.StatusOK, HealthzResp{
		OK:         true,
		Goroutines: runtime.NumGoroutine(),
		UptimeSec:  int64(time.Since(d.StartAt).Seconds()),
		WSConns:    conns,
	})
}

// goroutines 仅返回 goroutine 计数（冒烟脚本轮询用）。
func (d *Deps) goroutines(c *gin.Context) {
	OK(c, map[string]any{"goroutines": runtime.NumGoroutine()})
}

// readyz 检查 store 与 mediamtx 是否就绪。
func (d *Deps) readyz(c *gin.Context) {
	ready := true
	detail := map[string]any{"store": false, "mediamtx": false}

	if d.Store != nil {
		if err := d.Store.Ping(c.Request.Context()); err == nil {
			detail["store"] = true
		} else {
			ready = false
		}
	}
	if d.MTX != nil {
		ctx, cancel := context.WithTimeout(c.Request.Context(), d.readyTimeout())
		defer cancel()
		if err := d.MTX.Health(ctx); err == nil {
			detail["mediamtx"] = true
		} else {
			// MediaMTX 不可达不阻断整体就绪（视频只是能力之一）
			detail["mediamtxError"] = err.Error()
		}
	}
	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}
	c.JSON(status, map[string]any{"ready": ready, "detail": detail})
}

// runtime 返回前端所需的运行期配置。
func (d *Deps) runtime(c *gin.Context) {
	backends := []string{config.VideoProtocolWebRTC, config.VideoProtocolHLS}
	if d.Cfg.MediaMTX.FLVBase != "" {
		backends = append(backends, config.VideoProtocolFLV)
	}
	hosts := d.LANHosts
	if len(hosts) == 0 && d.Cfg.Server.LANHost != "" {
		hosts = []string{d.Cfg.Server.LANHost}
	}
	// 品牌与天气开关走同一套「DB > yaml > 默认」合并逻辑（见 settings.go）。
	// 读取失败不能让整个 runtime 挂掉：那是前端首屏的第一个请求，
	// 挂掉等于整个页面打不开，而品牌信息失败只应该退化成默认值。
	branding, weatherRT := d.runtimeBranding(c)
	OK(c, RuntimeConfig{
		AMapKey:          d.Cfg.Web.AMapKey,
		AMapSecurityCode: d.Cfg.Web.AMapSecurityCode,
		AMapForceWebGL:   d.Cfg.Web.AMapForceWebGL,
		WSUrl:            d.wsURL(c),
		ProtocolVersion:  config.WSProtocolVersion,
		SecureContext:    d.Cfg.Server.TLSEnabled,
		VideoBackends:    backends,
		LanHosts:         hosts,
		MediaMTX:         d.mediaMTXRuntime(),
		ServerTimeMs:     model.NowMs(),
		Branding:         branding,
		Weather:          weatherRT,
		Version:          config.Version,
		Commit:           config.GitCommit,
	})
}

// runtimeBranding 组装 runtime 里的品牌与天气段（失败时回落配置值，绝不返回空对象）。
func (d *Deps) runtimeBranding(c *gin.Context) (BrandingRuntime, WeatherRuntime) {
	fromCfg := BrandingRuntime{
		Title:    d.Cfg.Branding.Title,
		Subtitle: d.Cfg.Branding.Subtitle,
		LogoURL:  d.Cfg.Branding.LogoURL,
	}
	weatherFromCfg := WeatherRuntime{
		Enabled:  d.Cfg.Weather.Enabled,
		Provider: d.Cfg.Weather.Provider,
	}
	if d.Store == nil {
		return fromCfg, weatherFromCfg
	}
	p, err := d.loadSettings(c)
	if err != nil {
		d.Log.Warn("读取全局设置失败，runtime 已回落配置文件", "err", err)
		return fromCfg, weatherFromCfg
	}
	return BrandingRuntime{
			Title:    p.Branding.Title,
			Subtitle: p.Branding.Subtitle,
			LogoURL:  p.Branding.LogoURL,
		}, WeatherRuntime{
			Enabled:  p.Weather.Enabled,
			Provider: p.Weather.Provider,
		}
}

// mediaMTXRuntime 汇总前端修正播放地址所需的 MediaMTX 信息。
// publicHost 的回退链必须与 media.BuildPlayURLs 一致（publicHost → lanHost → 127.0.0.1），
// 否则前端拿到的 host 与实际下发 URL 里的 host 不一致。
func (d *Deps) mediaMTXRuntime() MediaMTXRuntime {
	host := d.Cfg.MediaMTX.PublicHost
	if host == "" {
		host = d.Cfg.Server.LANHost
	}
	if host == "" {
		host = "127.0.0.1"
	}
	portHLS := d.Cfg.MediaMTX.PortHLS
	if portHLS <= 0 {
		portHLS = config.DefaultPortHLS
	}
	portWebRTC := d.Cfg.MediaMTX.PortWebRTC
	if portWebRTC <= 0 {
		portWebRTC = config.DefaultPortWebRTC
	}
	return MediaMTXRuntime{
		Embedded:     d.Cfg.MediaMTX.Mode == config.MediaMTXModeEmbedded,
		PublicHost:   host,
		PublicScheme: d.Cfg.PublicScheme(),
		PortHLS:      portHLS,
		PortWebRTC:   portWebRTC,
	}
}

// lan 返回局域网可访问地址与端口（OP-01）。
func (d *Deps) lan(c *gin.Context) {
	OK(c, LANResp{
		Hosts:      d.LANHosts,
		HTTPPort:   portOf(d.Cfg.Server.HTTPAddr),
		HTTPSPort:  portOf(d.Cfg.Server.HTTPSAddr),
		TLSEnabled: d.Cfg.Server.TLSEnabled,
	})
}

// downloadCert 下载自签 CA 证书（供浏览器信任，T-31）。
func (d *Deps) downloadCert(c *gin.Context) {
	if d.Cert == nil || d.Cert.CAPath == "" {
		Fail(c, apperr.New(apperr.NotFound, "尚未生成证书（可能未启用 TLS）"))
		return
	}
	data, err := os.ReadFile(d.Cert.CAPath)
	if err != nil {
		Fail(c, apperr.Wrap(err, apperr.NotFound, "读取证书失败"))
		return
	}
	c.Header("Content-Disposition", "attachment; filename=monitorall-ca.crt")
	c.Data(http.StatusOK, "application/x-x509-ca-cert", data)
}

// wsURL 构造前端连接 WS 的绝对地址（与请求同源，避免协议猜测）。
func (d *Deps) wsURL(c *gin.Context) string {
	scheme := "ws"
	if d.isSecureRequest(c) {
		scheme = "wss"
	}
	host := c.Request.Host
	if host == "" {
		if len(d.LANHosts) > 0 {
			host = d.LANHosts[0] + ":" + strconv.Itoa(portOf(d.Cfg.Server.HTTPAddr))
		} else {
			host = "127.0.0.1:" + strconv.Itoa(portOf(d.Cfg.Server.HTTPAddr))
		}
	}
	return scheme + "://" + host + config.WSPath + "?protocol=" + strconv.Itoa(config.WSProtocolVersion)
}

// isSecureRequest 判断当前请求是否走 TLS。
func (d *Deps) isSecureRequest(c *gin.Context) bool {
	if c.Request.TLS != nil {
		return true
	}
	if c.GetHeader("X-Forwarded-Proto") == "https" {
		return true
	}
	return d.Cfg.Server.TLSEnabled && c.Request.URL.Scheme == "https"
}

// readyTimeout 返回就绪检查的超时。
func (d *Deps) readyTimeout() time.Duration {
	t := d.Cfg.HTTPPoll.DefaultTimeoutMs
	if t <= 0 {
		t = config.DefaultTimeoutMs
	}
	return time.Duration(t) * time.Millisecond
}
