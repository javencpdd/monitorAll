package media

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/config"
)

// TestParseRTMPURL 校验 RTMP 地址解析与默认端口、非法输入的错误码。
func TestParseRTMPURL(t *testing.T) {
	info, err := ParseRTMPURL("rtmp://172.31.68.227:1936/live/lite3")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if info.Host != "172.31.68.227" || info.Port != 1936 || info.Path != "live/lite3" {
		t.Fatalf("解析结果错误: %+v", info)
	}
	if info.HostPort() != "172.31.68.227:1936" {
		t.Fatalf("HostPort 错误: %s", info.HostPort())
	}
	// 缺省端口 1935
	info2, err := ParseRTMPURL("rtmp://172.31.68.227/live/lite3")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if info2.Port != config.DefaultRTMPPort {
		t.Fatalf("缺省端口应为 1935: %d", info2.Port)
	}
	// 非法输入 → 40002
	for _, bad := range []string{"", "http://x/live/a", "rtmp://host", "abc"} {
		if _, err := ParseRTMPURL(bad); err == nil {
			t.Fatalf("%q 应解析失败", bad)
		} else if apperr.CodeOf(err) != apperr.InvalidURL {
			t.Fatalf("%q 错误码应为 40002，实际 %d", bad, apperr.CodeOf(err))
		}
	}
}

// TestBuildPlayURLs 校验三路绝对地址编排；flvBase 为空时不产 flv 键（D1）。
func TestBuildPlayURLs(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.TLSEnabled = false
	cfg.Server.LANHost = "172.31.68.100"
	cfg.MediaMTX.FLVBase = ""
	urls := BuildPlayURLs(cfg, "", "live/lite3")
	if urls[config.VideoProtocolWebRTC] != "http://172.31.68.100:8889/live/lite3/whep" {
		t.Fatalf("webrtc 地址错误: %s", urls[config.VideoProtocolWebRTC])
	}
	if urls[config.VideoProtocolHLS] != "http://172.31.68.100:8888/live/lite3/index.m3u8" {
		t.Fatalf("hls 地址错误: %s", urls[config.VideoProtocolHLS])
	}
	if _, ok := urls[config.VideoProtocolFLV]; ok {
		t.Fatal("flvBase 为空时不应产出 flv 地址")
	}
	// 配置 flvBase 后出现 flv
	cfg.MediaMTX.FLVBase = "http://172.31.68.9:8081/live/"
	urls = BuildPlayURLs(cfg, "", "live/lite3")
	if urls[config.VideoProtocolFLV] != "http://172.31.68.9:8081/live/lite3.flv" {
		t.Fatalf("flv 地址错误: %s", urls[config.VideoProtocolFLV])
	}
	// 启用 TLS 时 publicScheme=auto 应为 https
	cfg.Server.TLSEnabled = true
	urls = BuildPlayURLs(cfg, "", "live/lite3")
	if urls[config.VideoProtocolWebRTC] != "https://172.31.68.100:8889/live/lite3/whep" {
		t.Fatalf("auto scheme 应为 https: %s", urls[config.VideoProtocolWebRTC])
	}
}

// TestPickProtocol 校验协议降级顺序：webrtc → flv（若配置）→ hls。
func TestPickProtocol(t *testing.T) {
	only := map[string]string{"hls": "http://x/index.m3u8"}
	if got := PickProtocol("webrtc", only); got != "hls" {
		t.Fatalf("首选不可用时降级到 hls: %s", got)
	}
	all := map[string]string{"webrtc": "w", "hls": "h"}
	if got := PickProtocol("webrtc", all); got != "webrtc" {
		t.Fatalf("首选可用时应选 webrtc: %s", got)
	}
}

// TestEnsureCertificate 校验自签证书生成：文件齐全、SAN 含 LAN IP、可重复调用。
func TestEnsureCertificate(t *testing.T) {
	dir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Server.TLSEnabled = true
	cfg.Server.AutoCert = true
	cfg.Server.CertDir = dir

	info, err := EnsureCertificate(cfg, []string{"172.31.68.100", "192.168.1.5"})
	if err != nil {
		t.Fatalf("生成证书失败: %v", err)
	}
	if !info.Generated {
		t.Fatal("首次调用应标记为已生成")
	}
	for _, p := range []string{info.CertPath, info.KeyPath, info.CAPath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("证书文件缺失: %s (%v)", p, err)
		}
	}
	// SAN 必须包含 LAN IP 与 localhost
	for _, want := range []string{"172.31.68.100", "192.168.1.5", "localhost", "127.0.0.1", ContainerHostname} {
		found := false
		for _, s := range info.SANs {
			if s == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("SAN 缺少 %s: %+v", want, info.SANs)
		}
	}
	// 证书可被解析且含 IP SAN
	data, err := os.ReadFile(info.CertPath)
	if err != nil {
		t.Fatalf("读取证书失败: %v", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("PEM 解析失败")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("解析证书失败: %v", err)
	}
	if len(cert.IPAddresses) == 0 {
		t.Fatal("证书缺少 IP SAN（Chrome 会报 COMMON_NAME_INVALID）")
	}
	// 重复调用应复用已有证书
	info2, err := EnsureCertificate(cfg, []string{"172.31.68.100"})
	if err != nil {
		t.Fatalf("重复调用失败: %v", err)
	}
	if info2.Generated {
		t.Fatal("重复调用不应重新签发")
	}
	// 关闭 TLS 时不生成
	cfg.Server.TLSEnabled = false
	info3, err := EnsureCertificate(cfg, nil)
	if err != nil {
		t.Fatalf("关闭 TLS 时不应报错: %v", err)
	}
	if info3.Enabled {
		t.Fatal("关闭 TLS 时应标记 Enabled=false")
	}
}

// TestBuildSANs 校验 SAN 去重。
func TestBuildSANs(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.LANHost = "172.31.68.100"
	sans := BuildSANs(cfg, []string{"172.31.68.100", "10.0.0.2", "localhost"})
	seen := map[string]int{}
	for _, s := range sans {
		seen[s]++
	}
	for k, v := range seen {
		if v != 1 {
			t.Fatalf("SAN 重复: %s", k)
		}
	}
	if seen["10.0.0.2"] != 1 || seen["localhost"] != 1 {
		t.Fatalf("SAN 内容缺失: %+v", sans)
	}
}

// ————————————————— MediaMTX 客户端 mock —————————————————

// mockClient 为 MediaMTX API 的 mock 实现（不依赖真实 MediaMTX）。
type mockClient struct {
	paths      []MediaMTXPath
	listErr    error
	added      map[string]string
	addedExtra map[string]map[string]any
}

func (m *mockClient) PathsList(ctx context.Context, path string) ([]MediaMTXPath, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	if path == "" {
		return m.paths, nil
	}
	for _, p := range m.paths {
		if p.Name == path {
			return []MediaMTXPath{p}, nil
		}
	}
	return []MediaMTXPath{}, nil
}

func (m *mockClient) PathAdd(ctx context.Context, path string, source string, opts ...PathOption) error {
	if m.added == nil {
		m.added = map[string]string{}
	}
	m.added[path] = source
	if m.addedExtra == nil {
		m.addedExtra = map[string]map[string]any{}
	}
	extra := map[string]any{}
	for _, opt := range opts {
		if opt != nil {
			opt(extra)
		}
	}
	m.addedExtra[path] = extra
	return nil
}

func (m *mockClient) Health(ctx context.Context) error { return m.listErr }

// TestMockClientContract 校验 Client 接口可被 mock 实现（保证适配器可脱离真实依赖测试）。
func TestMockClientContract(t *testing.T) {
	var c Client = &mockClient{paths: []MediaMTXPath{{Name: "live/lite3", Ready: true, Readers: 2}}}
	paths, err := c.PathsList(context.Background(), "live/lite3")
	if err != nil || len(paths) != 1 || !paths[0].Ready {
		t.Fatalf("mock 客户端行为异常: %v %+v", err, paths)
	}
	if err := c.PathAdd(context.Background(), "live/lite3", "publisher"); err != nil {
		t.Fatalf("PathAdd 失败: %v", err)
	}
	if got := c.(*mockClient).added["live/lite3"]; got != "publisher" {
		t.Fatalf("PathAdd 未记录: %s", got)
	}
	if err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health 失败: %v", err)
	}
}

// TestNewClientBase 校验客户端默认地址兜底。
func TestNewClientBase(t *testing.T) {
	c := NewClient(config.MediaMTXConfig{APIBase: "http://mediamtx:9997/"})
	if c == nil {
		t.Fatal("客户端不应为 nil")
	}
	// 无服务端时调用应返回 50003
	if err := c.Health(context.Background()); err == nil {
		t.Fatal("无 MediaMTX 时应返回错误")
	} else if apperr.CodeOf(err) != apperr.MediaMTXUnreachable {
		t.Fatalf("错误码应为 50003，实际 %d", apperr.CodeOf(err))
	}
}

// TestCertFilePermissions 校验证书目录创建与文件路径拼接。
func TestCertFilePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cert")
	cfg := config.DefaultConfig()
	cfg.Server.CertDir = dir
	cfg.Server.TLSEnabled = true
	info, err := EnsureCertificate(cfg, []string{"10.0.0.7"})
	if err != nil {
		t.Fatalf("生成证书失败: %v", err)
	}
	if info.CertPath != filepath.Join(dir, CertFileName) {
		t.Fatalf("证书路径错误: %s", info.CertPath)
	}
	if info.Fingerprint == "" {
		t.Fatal("应返回证书指纹")
	}
}

// TestParseStreamURLRTSP 校验 RTSP 摄像头地址解析：默认端口 554、保留用户名密码、多段路径。
func TestParseStreamURLRTSP(t *testing.T) {
	const raw = "rtsp://admin:okwy1688@192.168.2.69:554/Streaming/Channels/101"
	info, err := ParseStreamURL(raw)
	if err != nil {
		t.Fatalf("RTSP 解析失败: %v", err)
	}
	if info.Scheme != "rtsp" || info.Host != "192.168.2.69" || info.Port != 554 {
		t.Fatalf("解析结果错误: %+v", info)
	}
	if info.Path != "Streaming/Channels/101" {
		t.Fatalf("路径应为多段: %q", info.Path)
	}
	if info.Raw != raw {
		t.Fatalf("必须保留原始 URL（含用户名密码）: %q", info.Raw)
	}
	if !info.IsRTSP() || info.IsRTMP() {
		t.Fatalf("协议判定错误: %+v", info)
	}
	// 不显式给端口时缺省 554
	info2, err := ParseStreamURL("rtsp://192.168.2.69/Streaming/Channels/102")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if info2.Port != config.DefaultRTSPPort {
		t.Fatalf("RTSP 缺省端口应为 554: %d", info2.Port)
	}
	// RTMP 仍是 1935
	info3, err := ParseStreamURL("rtmp://172.31.68.227/live/lite3")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if info3.Port != config.DefaultRTMPPort || !info3.IsRTMP() {
		t.Fatalf("RTMP 解析错误: %+v", info3)
	}
	// rtsps 也应放行
	if _, err := ParseStreamURL("rtsps://cam.local/stream/1"); err != nil {
		t.Fatalf("rtsps 应放行: %v", err)
	}
	// 非法协议仍返回 40002
	for _, bad := range []string{"", "http://x/live/a", "rtsp://host", "rtsp://host:abc/a"} {
		if _, err := ParseStreamURL(bad); err == nil {
			t.Fatalf("%q 应解析失败", bad)
		} else if apperr.CodeOf(err) != apperr.InvalidURL {
			t.Fatalf("%q 错误码应为 40002，实际 %d", bad, apperr.CodeOf(err))
		}
	}
}

// TestBuildPublishURLs 校验 publish 模式的 WHIP / RTMP 推流地址。
func TestBuildPublishURLs(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.TLSEnabled = false
	cfg.Server.LANHost = "172.31.68.100"
	urls := BuildPublishURLs(cfg, "", "live/lite3")
	if urls[config.PublishProtocolWHIP] != "http://172.31.68.100:8889/live/lite3/whip" {
		t.Fatalf("whip 地址错误: %s", urls[config.PublishProtocolWHIP])
	}
	if urls[config.PublishProtocolRTMP] != "rtmp://172.31.68.100:1936/live/lite3" {
		t.Fatalf("rtmp 推流地址错误: %s", urls[config.PublishProtocolRTMP])
	}
}

// TestPathAddRTSPTransport 校验 PathAdd 的可选参数会带上 rtspTransport。
func TestPathAddRTSPTransport(t *testing.T) {
	var c Client = &mockClient{}
	if err := c.PathAdd(context.Background(), "cam/101", "rtsp://admin:pwd@192.168.2.69:554/Streaming/Channels/101",
		WithRTSPTransport(config.RTSPTransportTCP)); err != nil {
		t.Fatalf("PathAdd 失败: %v", err)
	}
	extra := c.(*mockClient).addedExtra["cam/101"]
	if extra["rtspTransport"] != config.RTSPTransportTCP {
		t.Fatalf("rtspTransport 未写入: %+v", extra)
	}
	// 不带可选参数时不应出现该字段（保持既有调用行为不变）
	_ = c.PathAdd(context.Background(), "cam/102", "publisher")
	if _, ok := c.(*mockClient).addedExtra["cam/102"]["rtspTransport"]; ok {
		t.Fatal("未传可选参数时不应写入 rtspTransport")
	}
}
