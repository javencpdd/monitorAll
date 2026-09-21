package api

import (
	"context"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/media"
)

// MediaMTXPathView 为 /mediamtx/paths 的返回项（含健康状态）。
type MediaMTXPathView struct {
	Name     string   `json:"name"`
	Ready    bool     `json:"ready"`
	Readers  int      `json:"readers"`
	BytesSent int64   `json:"bytesSent"`
	Tracks   []string `json:"tracks,omitempty"`
	Source   string   `json:"source,omitempty"`
	// WebrtcURL / HlsURL / FlvURL 为该路径编排出的播放地址（前端直连，不过后端）。
	WebrtcURL string `json:"webrtcUrl,omitempty"`
	HlsURL    string `json:"hlsUrl,omitempty"`
	FlvURL    string `json:"flvUrl,omitempty"`
}

// mediamtxPaths 查询 MediaMTX 路径与健康（embedded / external 同一套 API）。
func (d *Deps) mediamtxPaths(c *gin.Context) {
	path := c.Query("path")
	timeout := time.Duration(d.Cfg.HTTPPoll.DefaultTimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = time.Duration(config.DefaultTimeoutMs) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
	defer cancel()

	paths, err := d.MTX.PathsList(ctx, path)
	if err != nil {
		// MediaMTX 不可达 → 50003
		Fail(c, err)
		return
	}
	publicHost := d.Cfg.MediaMTX.PublicHost
	if publicHost == "" {
		publicHost = d.effectiveHost(c)
	}
	out := make([]MediaMTXPathView, 0, len(paths))
	for _, p := range paths {
		urls := media.BuildPlayURLs(d.Cfg, publicHost, p.Name)
		out = append(out, MediaMTXPathView{
			Name:      p.Name,
			Ready:     p.Ready,
			Readers:   p.Readers,
			BytesSent: p.BytesSent,
			Tracks:    p.Tracks,
			Source:    p.Source,
			WebrtcURL: urls[config.VideoProtocolWebRTC],
			HlsURL:    urls[config.VideoProtocolHLS],
			FlvURL:    urls[config.VideoProtocolFLV],
		})
	}
	OK(c, out)
}

// effectiveHost 推断前端可直连的 MediaMTX 主机：优先请求头 Host，其次 LAN 地址，最后 localhost。
func (d *Deps) effectiveHost(c *gin.Context) string {
	if host := c.Request.Host; host != "" {
		if idx := indexOf(host, ":"); idx > 0 {
			return host[:idx]
		}
		return host
	}
	if len(d.LANHosts) > 0 {
		return d.LANHosts[0]
	}
	return "127.0.0.1"
}

// indexOf 返回子串首次出现位置，未出现返回 -1（避免引入 strings 的小工具噪音）。
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// portOf 从监听地址中提取端口（形如 ":8080" 或 "0.0.0.0:8080"）。
func portOf(addr string) int {
	idx := indexOf(addr, ":")
	if idx < 0 || idx+1 >= len(addr) {
		return 0
	}
	n, err := strconv.Atoi(addr[idx+1:])
	if err != nil {
		return 0
	}
	return n
}
