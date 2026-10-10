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
	})
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
