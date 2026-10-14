package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/logx"
)

// ————————————————— MediaMTX HTTP API v3 客户端 —————————————————

// SourcePublisher 为 MediaMTX 的被动接收推流源标识。
// 该模式下 MediaMTX 不主动去拉流，而是等待远端（WHIP / RTMP）推进来。
const SourcePublisher = "publisher"

// mediaMTXPathRe 为 MediaMTX 路径名的合法形态：若干由 [A-Za-z0-9_.-] 组成、
// 以单个 '/' 分隔的段（如 cam1、live/cam1、live/m20pro-f）。
// 与 MediaMTX 自身的约束一致（其报错原文：can contain only alphanumeric
// characters, underscore, dot, minus, slash）。
var mediaMTXPathRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*$`)

// ValidateMediaMTXPath 校验 MediaMTX 路径名，返回去首尾空格后的值。
//
// 为什么必须校验：用户会把完整流地址填进 mediaMtxPath
// （如 rtmp://172.31.68.227:1936/live/m20pro-f，正确值只是 live/m20pro-f）。
// 该值会被直接拼进播放地址与 /v3/config/paths/add/<path>，MediaMTX 回 500
// "invalid path name"，前端只看到「HLS 协商超时」——根因被完全掩盖，排查成本极高。
// 在写入入口就拒绝，别让脏数据落库。
//
// 空值不报错：mediaMtxPath 允许留空（此时由流地址自动推导），是否必填由调用方判断。
//
// 错误码复用 apperr.InvalidParam（40001 参数校验失败）而非 InvalidURL（40002）：
// 这里校验的不是 URL 而是路径名，且 InvalidURL 的错误语义会被前端归类成“地址填错”
// 去做 URL 层面的提示；本错误需要引导用户把地址改成路径名，归类到参数校验更准。
func ValidateMediaMTXPath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", nil
	}
	if mediaMTXPathRe.MatchString(p) {
		return p, nil
	}
	return "", apperr.Newf(apperr.InvalidParam,
		"MediaMTX 路径只需流名（如 live/cam1），不能填完整地址；当前值 %q 请改为 %q", p, suggestMediaMTXPath(p))
}

// suggestMediaMTXPath 从误填的值里尽量救出一个合法路径，用于给出可行动的错误提示。
// 救不出来时返回通用示例，避免把非法值原样推荐回去。
func suggestMediaMTXPath(raw string) string {
	s := strings.TrimSpace(raw)
	// 完整地址（rtmp://host:1936/live/x、rtsp://user:pw@host/.../101）→ 取主机后的路径部分
	if i := strings.Index(s, "://"); i >= 0 {
		rest := s[i+3:]
		if j := strings.Index(rest, "/"); j >= 0 {
			s = rest[j+1:]
		}
	}
	// 去掉查询串与片段
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	s = strings.Trim(s, "/")
	// 折叠连续斜杠（live//cam → live/cam）
	for strings.Contains(s, "//") {
		s = strings.ReplaceAll(s, "//", "/")
	}
	if !mediaMTXPathRe.MatchString(s) {
		return "live/cam1"
	}
	return s
}

// MediaMTXPath 描述 MediaMTX 的一个路径与其健康状态。
type MediaMTXPath struct {
	Name        string   `json:"name"`
	ConfName    string   `json:"confName,omitempty"`
	Source      string   `json:"source,omitempty"`
	Ready       bool     `json:"ready"`
	SourceReady bool     `json:"sourceReady"`
	ReadyTime   string   `json:"readyTime,omitempty"`
	Tracks      []string `json:"tracks,omitempty"`
	Readers     int      `json:"readers"`
	BytesSent   int64    `json:"bytesSent"`
}

// PathOption 为 PathAdd 的可选参数（变参形式，不影响既有调用）。
type PathOption func(map[string]any)

// WithRTSPTransport 指定 RTSP 拉流的传输方式（tcp / udp / automatic）。
// 摄像头在 UDP 下易花屏或连不上，RTSP 源默认走 tcp。
func WithRTSPTransport(v string) PathOption {
	return func(m map[string]any) {
		if v != "" {
			m["rtspTransport"] = v
		}
	}
}

// Client 抽象 MediaMTX API，便于用 mock 做单测（不依赖真实 MediaMTX）。
type Client interface {
	// PathsList 列出路径；path 为空表示全部。
	PathsList(ctx context.Context, path string) ([]MediaMTXPath, error)
	// PathAdd 注册一个路径（source 为 RTMP/RTSP 拉流地址或 publisher）；
	// opts 可附加 rtspTransport 等可选字段。
	// PathAdd 注册路径；已存在且 source 一致时不动，source 不一致时先删后建。
	PathAdd(ctx context.Context, path string, source string, opts ...PathOption) error
	// Health 探测 API 可达性。
	Health(ctx context.Context) error
}

// httpClient 是 Client 的默认 HTTP 实现。
type httpClient struct {
	base string
	hc   *http.Client
}

// NewClient 构造 MediaMTX API 客户端。
func NewClient(cfg config.MediaMTXConfig) Client {
	base := strings.TrimRight(cfg.APIBase, "/")
	if base == "" {
		base = config.DefaultMediaMTXAPIBase
	}
	return &httpClient{base: base, hc: &http.Client{Timeout: time.Duration(config.DefaultTimeoutMs) * time.Millisecond}}
}

// apiError 把传输层错误统一转成 apperr.MediaMTXUnreachable。
func apiError(err error, action string) error {
	return apperr.Wrap(err, apperr.MediaMTXUnreachable, fmt.Sprintf("MediaMTX %s 失败: %v", action, err))
}

// pathsListResponse 为 v3/paths/list 的响应体。
type pathsListResponse struct {
	ItemCount int    `json:"itemCount"`
	Items     []item `json:"items"`
}

// item 为 paths/list 中的单项（字段较多，只解析需要的）。
type item struct {
	Name      string         `json:"name"`
	ConfName  string         `json:"confName"`
	Source    map[string]any `json:"source"`
	Ready     bool           `json:"ready"`
	ReadyTime string         `json:"readyTime"`
	Tracks    []string       `json:"tracks"`
	BytesSent int64          `json:"bytesSent"`
	Readers   []any          `json:"readers"`
}

// PathsList 查询路径列表。
func (c *httpClient) PathsList(ctx context.Context, path string) ([]MediaMTXPath, error) {
	endpoint := c.base + "/v3/paths/list"
	if path != "" {
		endpoint += "?path=" + path
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, apiError(err, "构造请求")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, apiError(err, "paths/list")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, apiError(err, "读取响应")
	}
	if resp.StatusCode >= 300 {
		return nil, apperr.Newf(apperr.MediaMTXUnreachable, "MediaMTX paths/list 返回 %d: %s", resp.StatusCode, string(body))
	}
	var out pathsListResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, apiError(err, "解析响应")
	}
	res := make([]MediaMTXPath, 0, len(out.Items))
	for _, it := range out.Items {
		res = append(res, MediaMTXPath{
			Name:        it.Name,
			ConfName:    it.ConfName,
			Source:      sourceString(it.Source),
			Ready:       it.Ready,
			SourceReady: it.Ready,
			ReadyTime:   it.ReadyTime,
			Tracks:      it.Tracks,
			Readers:     len(it.Readers),
			BytesSent:   it.BytesSent,
		})
	}
	return res, nil
}

// PathAdd 注册路径到 MediaMTX（embedded 模式编排用）。
//
// “路径已存在”不再一律当作成功。为什么改：
// MediaMTX 的路径配置会残留 —— 数据源删掉后配置仍在库里。若残留配置的 source 与本次
// 期望不同（publish ↔ pull，或上游地址变了），旧配置会一直生效：实测一条路径早先以
// publisher 注册过，改成拉流模式后 paths/add 被 400 "already exists" 一口吞掉，
// 配置永远是 publisher → 永远等不到推流 → HLS 404，且日志里全是成功，无从排查。
//
// 现在的策略：
//   - 已存在且 source 相同 → 原样返回 nil（多数据源共用同一路径时避免互相打架，
//     也避免重连/重启时反复重建路径导致播放中断）；
//   - 已存在且 source 不同 → 先 DELETE 再重新 ADD，并打 WARN；
//   - 重建任一步失败 → 返回错误，绝不静默成功。
func (c *httpClient) PathAdd(ctx context.Context, path string, source string, opts ...PathOption) error {
	exists, err := c.pathAddOnce(ctx, path, source, opts...)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	cur, err := c.pathGetSource(ctx, path)
	if err != nil {
		return err
	}
	if cur == source {
		// 配置已是期望值：不动它。多源共用同一路径时，两个源的 source 相同，
		// 谁都不该去删对方正在用的路径。
		return nil
	}
	// source 可能内联凭据（rtsp://admin:pw@host/...），日志必须脱敏
	slog.Warn("MediaMTX 路径已存在但 source 已变更，已重建路径配置",
		"path", path, "oldSource", redactSource(cur), "newSource", redactSource(source))
	if err := c.pathDelete(ctx, path); err != nil {
		return apperr.Wrap(err, apperr.MediaMTXUnreachable,
			fmt.Sprintf("MediaMTX 重建路径 %s 时删除旧配置失败", path))
	}
	// 删除后再 add 仍报已存在，说明 MediaMTX 侧没删掉（比如配置被静态文件锁住），
	// 这时必须报错而不是当作成功，否则又回到“静默失败”的老问题。
	if again, err := c.pathAddOnce(ctx, path, source, opts...); err != nil {
		return err
	} else if again {
		return apperr.Newf(apperr.MediaMTXUnreachable,
			"MediaMTX 路径 %s 删除后仍报已存在，重建失败", path)
	}
	return nil
}

// pathAddOnce 执行一次 POST /v3/config/paths/add/<path>。
// 返回 exists=true 表示 MediaMTX 以 400 "already exists" 拒绝（该路径的配置已存在）。
func (c *httpClient) pathAddOnce(ctx context.Context, path string, source string, opts ...PathOption) (bool, error) {
	endpoint := c.base + "/v3/config/paths/add/" + path
	// sourceOnDemand 只对“主动去拉”的源有意义；publisher 是被动等远端推流，
	// MediaMTX 会直接拒绝该组合："'sourceOnDemand' is useless when source is 'publisher'"，
	// 导致 publish 模式注册失败（实测：false 或省略均 200，true 400）。
	payload := map[string]any{"source": source, "sourceOnDemand": source != SourcePublisher}
	for _, opt := range opts {
		if opt != nil {
			opt(payload)
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return false, apiError(err, "序列化请求体")
	}
	code, respBody, err := c.do(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return false, err
	}
	if code >= 300 {
		if code == http.StatusBadRequest && strings.Contains(string(respBody), "already exists") {
			return true, nil
		}
		return false, apperr.Newf(apperr.MediaMTXUnreachable, "MediaMTX paths/add 返回 %d: %s", code, string(respBody))
	}
	return false, nil
}

// pathGetSource 读取已存在路径当前的 source（publisher 时为 "publisher"，拉流时为完整 URL）。
func (c *httpClient) pathGetSource(ctx context.Context, path string) (string, error) {
	endpoint := c.base + "/v3/config/paths/get/" + path
	code, respBody, err := c.do(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	if code >= 300 {
		return "", apperr.Newf(apperr.MediaMTXUnreachable, "MediaMTX paths/get 返回 %d: %s", code, string(respBody))
	}
	var conf struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal(respBody, &conf); err != nil {
		return "", apiError(err, "解析 paths/get 响应")
	}
	return conf.Source, nil
}

// pathDelete 删除路径配置（DELETE /v3/config/paths/delete/<path>）。
//
// 必须是 DELETE 方法：实测 MediaMTX（本机 9997）对
// `POST /v3/config/paths/delete/live/x` 返回 404 "404 page not found"（路由根本不匹配），
// 只有 `DELETE /v3/config/paths/delete/live/x` 才返回 200 {"status":"ok"}。
// 用 POST 会静默删不掉 —— 后续 add 仍报 already exists。
//
// 幂等：目标已不存在（404 path not found）时视为删除成功。
func (c *httpClient) pathDelete(ctx context.Context, path string) error {
	endpoint := c.base + "/v3/config/paths/delete/" + path
	code, respBody, err := c.do(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	if code >= 300 {
		if strings.Contains(strings.ToLower(string(respBody)), "not found") {
			return nil
		}
		return apperr.Newf(apperr.MediaMTXUnreachable, "MediaMTX paths/delete 返回 %d: %s", code, string(respBody))
	}
	return nil
}

// redactSource 把 source 脱敏后用于日志：publisher 不是 URL（RedactURL 会把它判成
// "解析不了" 而输出占位符，反而误导），原样输出；拉流地址可能内联
// rtsp://admin:pw@host/... 凭据，必须走 RedactURL。
func redactSource(s string) string {
	if s == SourcePublisher {
		return s
	}
	return logx.RedactURL(s)
}

// do 发起一次 MediaMTX API 请求并返回状态码与响应体（不判状态，语义交给调用方）。
//
// endpoint 直接拼接路径段（不做 url.PathEscape）：MediaMTX 的路径名本身含 '/'
// （如 live/m20pro-f），escape 成 live%2Fm20pro-f 会被当成单段路径而 404。
// MediaMTX 对路径名的字符集有硬约束（仅字母数字、下划线、点、减号、斜杠），
// 所以直接拼接不会产生歧义字符；非法字符由 ValidateMediaMTXPath 在入口拦掉。
func (c *httpClient) do(ctx context.Context, method, endpoint string, body []byte) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, rdr)
	if err != nil {
		return 0, nil, apiError(err, "构造请求")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, apiError(err, method+" "+endpoint)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, apiError(err, "读取响应")
	}
	return resp.StatusCode, respBody, nil
}

// Health 探测 API 是否可达。
func (c *httpClient) Health(ctx context.Context) error {
	_, err := c.PathsList(ctx, "")
	if err != nil {
		return err
	}
	return nil
}

// sourceString 把 source 对象转成可读字符串（type 优先）。
func sourceString(src map[string]any) string {
	if len(src) == 0 {
		return ""
	}
	if t, ok := src["type"].(string); ok {
		if id, ok := src["id"].(string); ok && id != "" {
			return t + ":" + id
		}
		return t
	}
	b, err := json.Marshal(src)
	if err != nil {
		return ""
	}
	return string(b)
}
