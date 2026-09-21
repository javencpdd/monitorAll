package media

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/config"
)

// ————————————————— MediaMTX HTTP API v3 客户端 —————————————————

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

// Client 抽象 MediaMTX API，便于用 mock 做单测（不依赖真实 MediaMTX）。
type Client interface {
	// PathsList 列出路径；path 为空表示全部。
	PathsList(ctx context.Context, path string) ([]MediaMTXPath, error)
	// PathAdd 注册一个路径（source 为 RTMP 拉流地址或 publisher）。
	PathAdd(ctx context.Context, path string, source string) error
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
func (c *httpClient) PathAdd(ctx context.Context, path string, source string) error {
	endpoint := c.base + "/v3/config/paths/add/" + path
	payload := map[string]any{"source": source, "sourceOnDemand": true}
	body, err := json.Marshal(payload)
	if err != nil {
		return apiError(err, "序列化请求体")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return apiError(err, "构造请求")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return apiError(err, "paths/add")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		// 路径已存在视为成功：重连/重启/多数据源共用同一路径时会重复注册，
		// MediaMTX 返回 400 "path already exists"，此时配置已在，不应按失败处理
		//（否则适配器反复重试并刷 WARN，且可能让状态推导误判）。
		if resp.StatusCode == http.StatusBadRequest && strings.Contains(string(respBody), "already exists") {
			return nil
		}
		return apperr.Newf(apperr.MediaMTXUnreachable, "MediaMTX paths/add 返回 %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
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
