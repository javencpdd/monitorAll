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
	// WHIPSuffix 为 WebRTC WHIP 推流端点后缀（publish 模式接收远端推流）。
	WHIPSuffix = "/whip"
	// HLSSuffix 为 HLS 播放列表后缀。
	HLSSuffix = "/index.m3u8"
	// FLVSuffix 为 HTTP-FLV 地址后缀。
	FLVSuffix = ".flv"
)

// StreamInfo 为解析后的流媒体地址（rtmp / rtmps / rtsp / rtsps）。
type StreamInfo struct {
	// Scheme 为 rtmp / rtmps / rtsp / rtsps。
	Scheme string `json:"scheme"`
	// Host 为主机名或 IP。
	Host string `json:"host"`
	// Port 为端口（rtmp 缺省 1935，rtsp 缺省 554）。
	Port int `json:"port"`
	// Path 为 MediaMTX 的路径，如 live/lite3 或 Streaming/Channels/101。
	Path string `json:"path"`
	// Raw 为原始 URL（保留用户名密码：MediaMTX 拉 RTSP 摄像头流需要凭据）。
	Raw string `json:"raw"`
}

// RTMPInfo 为 StreamInfo 的兼容别名（历史命名，等价于通用流地址信息）。
type RTMPInfo = StreamInfo

// IsRTSP 判断是否为 RTSP 系协议（rtsp / rtsps）。
func (r *StreamInfo) IsRTSP() bool {
	return r != nil && (r.Scheme == "rtsp" || r.Scheme == "rtsps")
}

// IsRTMP 判断是否为 RTMP 系协议（rtmp / rtmps）。
func (r *StreamInfo) IsRTMP() bool {
	return r != nil && (r.Scheme == "rtmp" || r.Scheme == "rtmps")
}

// IsLocalPublishURL 判断该流地址是否是「推给本机 MediaMTX」的 RTMP 推流：
// 仅 rtmp / rtmps 可能是推流（RTSP 一律 false），且端口与主机都指向本机。
//
// 抽到 media 包是因为两处要用同一口径：adapter/video 决定注册给 MediaMTX 的 source
// 是不是 publisher，api 层做同路径冲突检测时判断两个源的上游身份是否相同。
// 各写一份必然漂移 —— 漂移的后果是「同一个流被判成两个上游」，误报或漏报冲突。
func IsLocalPublishURL(raw string, rtmpPort int, lanHost string) bool {
	info, err := ParseStreamURL(raw)
	if err != nil || !info.IsRTMP() {
		return false
	}
	if info.Port != rtmpPort {
		return false
	}
	host := info.Host
	return host == "127.0.0.1" || host == "localhost" || (lanHost != "" && host == lanHost)
}

// defaultPortFor 按协议返回缺省端口（RTSP 554 / RTMP 1935）。
func defaultPortFor(scheme string) int {
	if scheme == "rtsp" || scheme == "rtsps" {
		return config.DefaultRTSPPort
	}
	return config.DefaultRTMPPort
}

// ParseStreamURL 解析流媒体地址：scheme ∈ {rtmp, rtmps, rtsp, rtsps}、path 非空、
// 端口缺省按协议取值（RTSP 554 / RTMP 1935）；用户名密码原样保留在 Raw 中。
// 非法地址返回 apperr.InvalidURL。
func ParseStreamURL(raw string) (*StreamInfo, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, apperr.New(apperr.InvalidURL, "流地址不能为空")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.InvalidURL, "流地址解析失败")
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "rtmp", "rtmps", "rtsp", "rtsps":
	default:
		return nil, apperr.Newf(apperr.InvalidURL, "流地址协议必须是 rtmp/rtmps/rtsp/rtsps，当前为 %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, apperr.New(apperr.InvalidURL, "流地址缺少主机")
	}
	// 路径允许多段：RTSP 摄像头常见 Streaming/Channels/101
	path := strings.Trim(u.Path, "/")
	if path == "" {
		return nil, apperr.New(apperr.InvalidURL, "流地址缺少路径，如 rtmp://host:1936/live/lite3 或 rtsp://user:pass@host:554/Streaming/Channels/101")
	}
	port := defaultPortFor(scheme)
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			return nil, apperr.Newf(apperr.InvalidURL, "流地址端口非法: %s", p)
		}
		port = n
	}
	return &StreamInfo{Scheme: scheme, Host: u.Hostname(), Port: port, Path: path, Raw: raw}, nil
}

// ParseRTMPURL 为 ParseStreamURL 的兼容包装（保留历史命名，现同样支持 rtsp/rtsps）。
func ParseRTMPURL(raw string) (*RTMPInfo, error) {
	return ParseStreamURL(raw)
}

// HostPort 返回 host:port 形式。
func (r *StreamInfo) HostPort() string {
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

// BuildPublishURLs 生成 publish 模式（接收远端推流）的推流地址：WHIP 与 RTMP。
func BuildPublishURLs(cfg *config.Config, publicHost, path string) map[string]string {
	urls := make(map[string]string, 2)
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
	urls[config.PublishProtocolWHIP] = cfg.PublicScheme() + "://" + host + ":" + strconv.Itoa(cfg.MediaMTX.PortWebRTC) + "/" + path + WHIPSuffix
	urls[config.PublishProtocolRTMP] = "rtmp://" + host + ":" + strconv.Itoa(cfg.MediaMTX.PortRTMP) + "/" + path
	return urls
}

// PickProtocol 按首选协议与可用地址集合挑选实际协议：首选不可用时依次降级。
//
// 降级顺序：webrtc → hls → flv。
// ⚠️ 必须与前端 web/src/utils/video.ts 的 fallbackOrder 保持一致（C12 / H4）。
// 此前后端是 webrtc → flv → hls，与前端不一致：FLV 需要额外的 HTTP-FLV 网关（本项目未部署），
// 一旦 webrtc 不可用，后端会挑一个常态化不可用的 flv，而前端以为会降到 hls。
//
// 注意：这是一次【语义一致性修复，不是修一个正在发生的线上 bug】。
// BuildPlayURLs 无条件生成 webrtc 与 hls，且 webrtc 永远在 urls 里，因此降级分支当前实际不可达；
// 只有当 webrtc 变成条件生成时两版才会出现真实分歧。
func PickProtocol(preferred string, urls map[string]string) string {
	order := []string{config.VideoProtocolWebRTC, config.VideoProtocolHLS, config.VideoProtocolFLV}
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
