package media

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/config"
)

// ————————————————— 播放地址模板（常量集中，禁止散落裸字符串） —————————————————

const (
	// WHEPSuffix 为 WebRTC WHEP 端点后缀。
	WHEPSuffix = "/whep"
	// HLSSuffix 为 HLS 播放列表后缀。
	HLSSuffix = "/index.m3u8"
	// FLVSuffix 为 HTTP-FLV 地址后缀。
	FLVSuffix = ".flv"
)

// RTMPInfo 为解析后的 RTMP 地址。
type RTMPInfo struct {
	// Scheme 为 rtmp / rtmps。
	Scheme string `json:"scheme"`
	// Host 为主机名或 IP。
	Host string `json:"host"`
	// Port 为端口（缺省 1935）。
	Port int `json:"port"`
	// Path 为 MediaMTX 的路径，如 live/lite3。
	Path string `json:"path"`
	// Raw 为原始 URL。
	Raw string `json:"raw"`
}

// ParseRTMPURL 解析 RTMP 地址：scheme ∈ {rtmp, rtmps}、path 非空、端口缺省 1935。
// 非法地址返回 apperr.InvalidURL。
func ParseRTMPURL(raw string) (*RTMPInfo, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, apperr.New(apperr.InvalidURL, "rtmpUrl 不能为空")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.InvalidURL, "RTMP 地址解析失败")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "rtmp" && scheme != "rtmps" {
		return nil, apperr.Newf(apperr.InvalidURL, "RTMP 地址协议必须是 rtmp/rtmps，当前为 %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, apperr.New(apperr.InvalidURL, "RTMP 地址缺少主机")
	}
	path := strings.Trim(u.Path, "/")
	if path == "" {
		return nil, apperr.New(apperr.InvalidURL, "RTMP 地址缺少路径，如 rtmp://host:1936/live/lite3")
	}
	port := config.DefaultRTMPPort
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			return nil, apperr.Newf(apperr.InvalidURL, "RTMP 端口非法: %s", p)
		}
		port = n
	}
	return &RTMPInfo{Scheme: scheme, Host: u.Hostname(), Port: port, Path: path, Raw: raw}, nil
}

// HostPort 返回 host:port 形式。
func (r *RTMPInfo) HostPort() string {
	return r.Host + ":" + strconv.Itoa(r.Port)
}

// BuildPlayURLs 按 publicScheme/publicHost/port 生成 webrtc / hls / flv 三路绝对地址。
// flvBase 为空时不产 flv 键（D1：MediaMTX 无原生 HTTP-FLV，FLV 需外部网关）。
func BuildPlayURLs(cfg *config.Config, publicHost, path string) map[string]string {
	urls := make(map[string]string, 3)
	if cfg == nil || path == "" {
		return urls
	}
	host := publicHost
	if host == "" {
		host = cfg.Server.LANHost
	}
	if host == "" {
		host = "127.0.0.1"
	}
	scheme := cfg.PublicScheme()
	base := scheme + "://" + host

	urls[config.VideoProtocolWebRTC] = base + ":" + strconv.Itoa(cfg.MediaMTX.PortWebRTC) + "/" + path + WHEPSuffix
	urls[config.VideoProtocolHLS] = base + ":" + strconv.Itoa(cfg.MediaMTX.PortHLS) + "/" + path + HLSSuffix
	if flv := strings.TrimSpace(cfg.MediaMTX.FLVBase); flv != "" {
		// FLV 网关（SRS / ZLMediaKit / nginx-http-flv）地址形如 /app/stream.flv：
		// app 已包含在 flvBase 中，这里取 MediaMTX path 的最后一段作为 stream 名。
		stream := path
		if i := strings.LastIndex(path, "/"); i >= 0 {
			stream = path[i+1:]
		}
		urls[config.VideoProtocolFLV] = strings.TrimRight(flv, "/") + "/" + stream + FLVSuffix
	}
	return urls
}

// PickProtocol 按首选协议与可用地址集合挑选实际协议：首选不可用时依次降级。
// 降级顺序：webrtc → flv（若配置）→ hls。
func PickProtocol(preferred string, urls map[string]string) string {
	order := []string{config.VideoProtocolWebRTC, config.VideoProtocolFLV, config.VideoProtocolHLS}
	// 首选置于队首
	ordered := make([]string, 0, len(order))
	if preferred != "" {
		ordered = append(ordered, preferred)
	}
	for _, p := range order {
		if p != preferred {
			ordered = append(ordered, p)
		}
	}
	for _, p := range ordered {
		if u, ok := urls[p]; ok && u != "" {
			return p
		}
	}
	return config.VideoProtocolHLS
}
