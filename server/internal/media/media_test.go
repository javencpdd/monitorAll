package media

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

// TestPickProtocol 校验协议降级顺序：webrtc → hls → flv（C12 / H4，与前端 utils/video.ts 一致）。
func TestPickProtocol(t *testing.T) {
	only := map[string]string{"hls": "http://x/index.m3u8"}
	if got := PickProtocol("webrtc", only); got != "hls" {
		t.Fatalf("首选不可用时降级到 hls: %s", got)
	}
	all := map[string]string{"webrtc": "w", "hls": "h"}
	if got := PickProtocol("webrtc", all); got != "webrtc" {
		t.Fatalf("首选可用时应选 webrtc: %s", got)
	}
	// 锁住新顺序的关键用例：webrtc 缺失、hls 与 flv 都在时必须是 hls。
	// 改动前（webrtc → flv → hls）这里会返回 flv —— FLV 需要额外网关，本项目常态化不可用。
	noWebrtc := map[string]string{"hls": "h", "flv": "f"}
	if got := PickProtocol("webrtc", noWebrtc); got != "hls" {
		t.Fatalf("webrtc 不可用时应降级到 hls（不是 flv）: %s", got)
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
	const raw = "rtsp://admin:your-password@192.168.2.69:554/Streaming/Channels/101"
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

// ————————————————— mediaMtxPath 格式校验 —————————————————

// TestValidateMediaMTXPath 表驱动校验 MediaMTX 路径名：
// 合法路径放行，完整地址 / 连续斜杠 / 查询串 / 空格一律拒绝（40001）。
func TestValidateMediaMTXPath(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string // 期望返回的路径（合法时）；wantErr 为真时忽略
		wantErr bool
	}{
		{name: "两段路径", in: "live/cam1", want: "live/cam1"},
		{name: "单段路径", in: "cam1", want: "cam1"},
		{name: "含减号", in: "live/m20pro-f", want: "live/m20pro-f"},
		{name: "含点与下划线", in: "live/cam_1.2", want: "live/cam_1.2"},
		{name: "多段路径", in: "Streaming/Channels/101", want: "Streaming/Channels/101"},
		{name: "首尾空格被裁掉", in: "  live/cam1  ", want: "live/cam1"},
		{name: "空值放行", in: "", want: ""},
		{name: "纯空格按空值处理", in: "   ", want: ""},

		{name: "完整 RTMP 地址", in: "rtmp://172.31.68.227:1936/live/m20pro-f", wantErr: true},
		{name: "完整 RTSP 地址", in: "rtsp://admin:pwd@192.168.2.69:554/Streaming/Channels/101", wantErr: true},
		{name: "单斜杠的 rtmp 前缀", in: "rtmp:/172.31.68.227/live/x", wantErr: true},
		{name: "http 前缀", in: "http://host/live/a", wantErr: true},
		{name: "https 前缀", in: "https://host/live/a", wantErr: true},
		{name: "连续斜杠", in: "live//cam", wantErr: true},
		{name: "带查询串", in: "live/cam?x=1", wantErr: true},
		{name: "带片段", in: "live/cam#frag", wantErr: true},
		{name: "含空格", in: "live/cam 1", wantErr: true},
		{name: "含冒号端口", in: "live/cam:1936", wantErr: true},
		{name: "以斜杠开头", in: "/live/cam1", wantErr: true},
		{name: "以斜杠结尾", in: "live/cam1/", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateMediaMTXPath(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("输入 %q 应被拒绝", tc.in)
				}
				if apperr.CodeOf(err) != apperr.InvalidParam {
					t.Fatalf("输入 %q 错误码应为 40001，实际 %d", tc.in, apperr.CodeOf(err))
				}
				return
			}
			if err != nil {
				t.Fatalf("输入 %q 不应被拒绝: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("输入 %q 返回 %q，期望 %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestValidateMediaMTXPathMessage 校验报错信息可行动：
// 必须给出「当前值 → 建议值」，让运维一眼知道该怎么改，而不是甩一句“路径非法”。
func TestValidateMediaMTXPathMessage(t *testing.T) {
	_, err := ValidateMediaMTXPath("rtmp://172.31.68.227:1936/live/m20pro-f")
	if err == nil {
		t.Fatal("完整地址应被拒绝")
	}
	msg := err.Error()
	for _, want := range []string{
		`rtmp://172.31.68.227:1936/live/m20pro-f`, // 原样回显当前值
		`"live/m20pro-f"`,                         // 给出可直接使用的建议值
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("报错信息缺少 %s: %s", want, msg)
		}
	}
	// 连续斜杠 / 查询串也要能救出建议值
	if _, err := ValidateMediaMTXPath("live//cam"); err == nil || !strings.Contains(err.Error(), `"live/cam"`) {
		t.Fatalf("live//cam 的建议值应为 live/cam: %v", err)
	}
	if _, err := ValidateMediaMTXPath("live/cam?x=1"); err == nil || !strings.Contains(err.Error(), `"live/cam"`) {
		t.Fatalf("live/cam?x=1 的建议值应为 live/cam: %v", err)
	}
}

// ————————————————— PathAdd：已存在路径的重建 —————————————————

// seqStep 为一次预置的 HTTP 响应。
type seqStep struct {
	code int
	body string
}

// seqTransport 按配置顺序回放响应，并记录每次请求的 method + path + body。
//
// 为什么不用 mockClient：mockClient 实现的是 Client 接口（PathsList/PathAdd/Health），
// 而「已存在时读 source → 比对 → delete → 重新 add」是 httpClient 内部的 MediaMTX
// 配置编排，有意不进 Client 接口（否则 adapter/video 的 recordClient 等实现者都要补
// 两个与它们无关的方法）。所以在 HTTP 传输层打桩：既能表达「先 400 再 200」的响应序列，
// 又能顺带断言三个 endpoint 的真实 URL 形态（路径段里的 '/' 不能被 escape）。
type seqTransport struct {
	steps  []seqStep
	calls  []string // "METHOD /path"
	bodies []string // 请求体（GET 为空串）
}

func (t *seqTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var body string
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	}
	t.calls = append(t.calls, r.Method+" "+r.URL.Path)
	t.bodies = append(t.bodies, body)

	step := seqStep{code: http.StatusOK}
	if len(t.steps) > 0 {
		step = t.steps[0]
		t.steps = t.steps[1:]
	}
	header := http.Header{}
	if step.body != "" {
		header.Set("Content-Type", "application/json")
	}
	return &http.Response{
		StatusCode: step.code,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(step.body)),
		Request:    r,
	}, nil
}

// newSeqClient 构造一个按 steps 回放响应的 httpClient。
func newSeqClient(steps ...seqStep) (*httpClient, *seqTransport) {
	tr := &seqTransport{steps: steps}
	return &httpClient{base: "http://mtx.test", hc: &http.Client{Transport: tr}}, tr
}

// alreadyExists 为 MediaMTX 对「路径已存在」的真实响应。
var alreadyExists = seqStep{code: http.StatusBadRequest, body: `{"error":"path already exists"}`}

// confWithSource 为 paths/get 的响应体（source 为字符串）。
func confWithSource(src string) seqStep {
	return seqStep{code: http.StatusOK, body: `{"name":"live/cam1","source":"` + src + `"}`}
}

// pathAddOK 为 paths/add 的成功响应。
var pathAddOK = seqStep{code: http.StatusOK, body: `{}`}

// TestPathAddAlreadyExistsSameSource 已存在且 source 一致 → 不删除、不重建，返回 nil。
// 这是「多数据源共用同一路径 / 重连重启重复注册」必须保持的行为。
func TestPathAddAlreadyExistsSameSource(t *testing.T) {
	c, tr := newSeqClient(alreadyExists, confWithSource(SourcePublisher))
	if err := c.PathAdd(context.Background(), "live/cam1", SourcePublisher); err != nil {
		t.Fatalf("source 一致时应返回 nil: %v", err)
	}
	want := []string{
		"POST /v3/config/paths/add/live/cam1",
		"GET /v3/config/paths/get/live/cam1",
	}
	if got := strings.Join(tr.calls, "|"); got != strings.Join(want, "|") {
		t.Fatalf("调用序列不符: %s", got)
	}
}

// TestPathAddAlreadyExistsSourceChanged 已存在且 source 变更 → 先 delete 再 add，返回 nil。
//
// 这条是本次修复的核心：旧实现把 400 already exists 一律当成功，
// 导致 publish→pull 变更后路径配置永远停在旧的 publisher（HLS 404）。
func TestPathAddAlreadyExistsSourceChanged(t *testing.T) {
	const newSource = "rtmp://172.31.68.227:1936/live/cam1"
	c, tr := newSeqClient(alreadyExists, confWithSource(SourcePublisher), pathAddOK, pathAddOK)
	if err := c.PathAdd(context.Background(), "live/cam1", newSource); err != nil {
		t.Fatalf("重建成功时应返回 nil: %v", err)
	}
	want := []string{
		"POST /v3/config/paths/add/live/cam1",
		"GET /v3/config/paths/get/live/cam1",
		"DELETE /v3/config/paths/delete/live/cam1",
		"POST /v3/config/paths/add/live/cam1",
	}
	if got := strings.Join(tr.calls, "|"); got != strings.Join(want, "|") {
		t.Fatalf("调用序列不符（应为 add→get→delete→add）: %s", got)
	}
	// 重建时提交给 MediaMTX 的必须是新的 source，且带 sourceOnDemand=true（拉流源）
	last := tr.bodies[len(tr.bodies)-1]
	if !strings.Contains(last, newSource) {
		t.Fatalf("重建请求未带新 source: %s", last)
	}
	if !strings.Contains(last, `"sourceOnDemand":true`) {
		t.Fatalf("拉流源应带 sourceOnDemand=true: %s", last)
	}
	// publisher 时不能带 sourceOnDemand=true（MediaMTX 会 400）
	c2, tr2 := newSeqClient(pathAddOK)
	if err := c2.PathAdd(context.Background(), "live/cam1", SourcePublisher); err != nil {
		t.Fatalf("PathAdd 失败: %v", err)
	}
	if strings.Contains(tr2.bodies[0], `"sourceOnDemand":true`) {
		t.Fatalf("publisher 不应带 sourceOnDemand=true: %s", tr2.bodies[0])
	}
}

// TestPathAddRebuildDeleteFails 重建时 delete 失败 → 必须返回错误，不能静默成功。
func TestPathAddRebuildDeleteFails(t *testing.T) {
	c, tr := newSeqClient(alreadyExists, confWithSource(SourcePublisher),
		seqStep{code: http.StatusInternalServerError, body: `{"error":"boom"}`})
	err := c.PathAdd(context.Background(), "live/cam1", "rtsp://cam/101")
	if err == nil {
		t.Fatal("delete 失败时应返回错误")
	}
	if apperr.CodeOf(err) != apperr.MediaMTXUnreachable {
		t.Fatalf("错误码应为 50003，实际 %d", apperr.CodeOf(err))
	}
	// delete 失败后不应再尝试 add
	for _, call := range tr.calls {
		if call == "POST /v3/config/paths/add/live/cam1" && len(tr.calls) > 3 {
			t.Fatalf("delete 失败后不应继续 add: %v", tr.calls)
		}
	}
	_ = tr
}

// TestPathAddRebuildGetFails 读不到现有配置 → 无法判断是否需要重建，返回错误而非假装成功。
func TestPathAddRebuildGetFails(t *testing.T) {
	c, _ := newSeqClient(alreadyExists, seqStep{code: http.StatusNotFound, body: `{"error":"path configuration not found"}`})
	if err := c.PathAdd(context.Background(), "live/cam1", "publisher"); err == nil {
		t.Fatal("paths/get 失败时应返回错误")
	}
}

// TestPathAddFirstTime 首次注册成功 → 只发一次 add，不触发 get/delete。
func TestPathAddFirstTime(t *testing.T) {
	c, tr := newSeqClient(pathAddOK)
	if err := c.PathAdd(context.Background(), "live/cam1", SourcePublisher); err != nil {
		t.Fatalf("首次 PathAdd 失败: %v", err)
	}
	if len(tr.calls) != 1 || tr.calls[0] != "POST /v3/config/paths/add/live/cam1" {
		t.Fatalf("首次注册只应调用一次 add: %v", tr.calls)
	}
}

// TestPathAddDeleteIdempotent 删除时目标已不存在（404 not found）→ 视为删除成功，继续重建。
func TestPathAddDeleteIdempotent(t *testing.T) {
	c, _ := newSeqClient(alreadyExists, confWithSource(SourcePublisher),
		seqStep{code: http.StatusNotFound, body: `{"error":"path not found"}`}, pathAddOK)
	if err := c.PathAdd(context.Background(), "live/cam1", "rtmp://cam/1"); err != nil {
		t.Fatalf("delete 幂等失败时应继续重建: %v", err)
	}
}
