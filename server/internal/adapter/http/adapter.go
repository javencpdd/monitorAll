// Package http 实现 HTTP 轮询数据源适配器：定时轮询 + gjson JSONPath 提取 +
// payloadType 推断 + 归一化成统一 Frame（架构 §8.5）。
package http

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"log/slog"
	nethttp "net/http"
	"strings"
	"sync"
	"time"

	"github.com/monitorall/monitorall/internal/adapter"
	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/model"
	"github.com/tidwall/gjson"
)

// init 注册 HTTP 轮询适配器工厂。
func init() {
	adapter.Register(model.KindHTTP, func(ds *model.DataSource, log *slog.Logger) (adapter.Adapter, error) {
		return New(ds, log)
	})
}

// Adapter 为 HTTP 轮询适配器。
type Adapter struct {
	ds     *model.DataSource
	log    *slog.Logger
	cfg    *config.Config
	params model.HTTPConnParams
	client *nethttp.Client

	mu       sync.Mutex
	started  bool
	lastErr  string
	failures int
	channels map[string]*pollChannel

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// pollChannel 为一个轮询通道的运行态。
type pollChannel struct {
	id         string
	spec       adapter.ChannelSpec
	emit       adapter.Emit
	params     model.HTTPConnParams
	cancel     context.CancelFunc
	mu         sync.Mutex
	inflight   bool
	inferredPT model.PayloadType
}

// New 构造 HTTP 轮询适配器。
func New(ds *model.DataSource, log *slog.Logger) (*Adapter, error) {
	if ds == nil {
		return nil, apperr.New(apperr.InvalidParam, "数据源为空")
	}
	cfg := config.DefaultConfig()
	if env := adapter.EnvOf(); env != nil && env.Cfg != nil {
		cfg = env.Cfg
	}
	params, err := adapter.ParamsTo[model.HTTPConnParams](ds.ConnParams)
	if err != nil {
		return nil, err
	}
	if params.URL == "" {
		return nil, apperr.New(apperr.InvalidURL, "url 不能为空")
	}
	if params.Method == "" {
		params.Method = nethttp.MethodGet
	}
	params.Method = strings.ToUpper(params.Method)
	if params.Method != nethttp.MethodGet && params.Method != nethttp.MethodPost {
		return nil, apperr.Newf(apperr.InvalidParam, "不支持的请求方法: %s", params.Method)
	}
	if params.IntervalMs <= 0 {
		params.IntervalMs = defaultIntervalMs
	}
	if params.IntervalMs < cfg.HTTPPoll.MinIntervalMs {
		params.IntervalMs = cfg.HTTPPoll.MinIntervalMs
	}
	if params.TimeoutMs <= 0 {
		params.TimeoutMs = cfg.HTTPPoll.DefaultTimeoutMs
	}

	transport := &nethttp.Transport{}
	if params.InsecureTLS {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // 局域网自签证书场景由用户显式声明
	}
	return &Adapter{
		ds:       ds,
		log:      log,
		cfg:      cfg,
		params:   params,
		client:   &nethttp.Client{Transport: transport},
		channels: make(map[string]*pollChannel),
	}, nil
}

// defaultIntervalMs 为默认轮询周期。
const defaultIntervalMs = 1000

// DataSourceID 返回数据源 ID。
func (a *Adapter) DataSourceID() string { return a.ds.ID }

// Start 标记启动（HTTP 无长连接，实际工作在各通道的轮询协程）；幂等。
func (a *Adapter) Start(ctx context.Context) error {
	a.mu.Lock()
	if a.started {
		a.mu.Unlock()
		return nil
	}
	a.started = true
	a.ctx, a.cancel = context.WithCancel(context.Background())
	a.failures = 0
	a.mu.Unlock()
	return nil
}

// Health 轻量探活：发一次 HEAD/GET（失败累计到阈值才由 Manager 判定重连）。
func (a *Adapter) Health(ctx context.Context) adapter.Health {
	start := time.Now()
	reqCtx, cancel := context.WithTimeout(ctx, a.timeout())
	defer cancel()
	req, err := nethttp.NewRequestWithContext(reqCtx, a.params.Method, a.params.URL, nil)
	if err != nil {
		return adapter.Health{OK: false, Detail: err.Error()}
	}
	for k, v := range a.params.Headers {
		req.Header.Set(k, v)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		a.mu.Lock()
		a.lastErr = err.Error()
		a.mu.Unlock()
		return adapter.Health{OK: false, LatencyMs: time.Since(start).Milliseconds(), Detail: err.Error()}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	ok := resp.StatusCode < 400
	a.mu.Lock()
	a.lastErr = ""
	a.mu.Unlock()
	return adapter.Health{OK: ok, LatencyMs: time.Since(start).Milliseconds(), Detail: resp.Status}
}

// ListChannels 返回该 HTTP 源的通道（1 个：URL 的 path 部分）。
func (a *Adapter) ListChannels(ctx context.Context) ([]adapter.ChannelSpec, error) {
	name := channelNameOf(a.params.URL)
	spec := adapter.ChannelSpec{
		Name:        name,
		PayloadType: model.PayloadJSON,
		Meta: map[string]any{
			model.MetaHTTPMethod: a.params.Method,
			model.MetaJSONPath:   a.params.JSONPath,
		},
	}
	return []adapter.ChannelSpec{spec}, nil
}

// channelNameOf 从 URL 取 path 作为通道名（如 /api/stat）。
func channelNameOf(rawURL string) string {
	idx := strings.Index(rawURL, "://")
	rest := rawURL
	if idx >= 0 {
		rest = rawURL[idx+3:]
	}
	if slash := strings.Index(rest, "/"); slash >= 0 {
		path := rest[slash:]
		if q := strings.IndexAny(path, "?#"); q >= 0 {
			path = path[:q]
		}
		if path != "" {
			return path
		}
	}
	return "/"
}

// StartChannel 启动该通道的定时轮询（幂等）。
func (a *Adapter) StartChannel(ctx context.Context, channelID string, spec adapter.ChannelSpec, emit adapter.Emit) error {
	a.mu.Lock()
	if _, ok := a.channels[channelID]; ok {
		a.mu.Unlock()
		return nil
	}
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	chCtx, cancel := context.WithCancel(parent)
	ch := &pollChannel{
		id:         channelID,
		spec:       spec,
		emit:       emit,
		params:     a.params,
		cancel:     cancel,
		inferredPT: spec.PayloadType,
	}
	a.channels[channelID] = ch
	a.mu.Unlock()

	interval := time.Duration(ch.params.IntervalMs) * time.Millisecond
	if interval <= 0 {
		interval = time.Duration(defaultIntervalMs) * time.Millisecond
	}
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				a.log.Error("HTTP 轮询 panic", "channelId", channelID, "panic", r)
			}
		}()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		a.pollOnce(chCtx, ch)
		for {
			select {
			case <-chCtx.Done():
				return
			case <-ticker.C:
				a.pollOnce(chCtx, ch)
			}
		}
	}()
	return nil
}

// pollOnce 执行一次轮询；上一请求未完成时跳过本次 tick（超时不叠加，防雪崩）。
func (a *Adapter) pollOnce(ctx context.Context, ch *pollChannel) {
	ch.mu.Lock()
	if ch.inflight {
		ch.mu.Unlock()
		return
	}
	ch.inflight = true
	ch.mu.Unlock()
	defer func() {
		ch.mu.Lock()
		ch.inflight = false
		ch.mu.Unlock()
	}()

	reqCtx, cancel := context.WithTimeout(ctx, a.timeout())
	defer cancel()

	var body io.Reader
	if ch.params.Body != "" {
		body = strings.NewReader(ch.params.Body)
	}
	req, err := nethttp.NewRequestWithContext(reqCtx, ch.params.Method, ch.params.URL, body)
	if err != nil {
		a.recordFailure(err.Error())
		return
	}
	for k, v := range ch.params.Headers {
		req.Header.Set(k, v)
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := a.client.Do(req)
	if err != nil {
		a.recordFailure(err.Error())
		return
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		a.recordFailure(err.Error())
		return
	}
	if resp.StatusCode >= 400 {
		a.recordFailure("HTTP " + resp.Status)
		return
	}
	a.resetFailures()
	a.normalizeAndEmit(ch, data)
}

// recordFailure 累计失败次数（连续达到阈值才置数据源重连态）。
func (a *Adapter) recordFailure(msg string) {
	a.mu.Lock()
	a.failures++
	a.lastErr = msg
	threshold := a.cfg.HTTPPoll.FailureThreshold
	a.mu.Unlock()
	if threshold <= 0 {
		threshold = config.DefaultFailureThreshold
	}
	a.log.Warn("HTTP 轮询失败", "url", a.params.URL, "err", msg)
}

// resetFailures 成功后清零失败计数（HTTP 偶发失败不应闪状态）。
func (a *Adapter) resetFailures() {
	a.mu.Lock()
	a.failures = 0
	a.lastErr = ""
	a.mu.Unlock()
}

// FailureCount 返回连续失败次数（供 Manager 判定是否置 reconnecting）。
func (a *Adapter) FailureCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.failures
}

// normalizeAndEmit 提取、推断 payloadType 并投递一帧。
func (a *Adapter) normalizeAndEmit(ch *pollChannel, data []byte) {
	value, pt := a.extract(ch, data)
	f, hints, err := buildFrame(ch.id, pt, value, model.NowMs())
	if err != nil {
		a.log.Error("构造 HTTP 帧失败", "err", err)
		return
	}
	if pt != ch.inferredPT {
		// 首次推断结果写入通道并广播 channelStatus（架构 §8.5）
		ch.mu.Lock()
		ch.inferredPT = pt
		ch.mu.Unlock()
	}
	if hints != nil {
		f.SchemaHint = hints
	}
	ch.emit(ch.id, f)
}

// extract 用 gjson 提取并按 R4 推断 payloadType。
func (a *Adapter) extract(ch *pollChannel, data []byte) (any, model.PayloadType) {
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return nil, model.PayloadJSON
	}
	if ch.params.JSONPath != "" {
		res := gjson.GetBytes(data, ch.params.JSONPath)
		if !res.Exists() {
			return map[string]any{}, model.PayloadJSON
		}
		return decodeGJSON(res), model.InferPayloadType(decodeGJSON(res))
	}
	var v any
	if raw[0] == '[' || raw[0] == '{' {
		_ = json.Unmarshal(data, &v)
	} else {
		v = raw
	}
	return v, model.InferPayloadType(v)
}

// decodeGJSON 把 gjson 结果转成 Go 原生值。
func decodeGJSON(res gjson.Result) any {
	switch {
	case res.IsArray():
		out := make([]any, 0)
		for _, item := range res.Array() {
			out = append(out, decodeGJSON(item))
		}
		return out
	case res.IsObject():
		out := map[string]any{}
		res.ForEach(func(key, value gjson.Result) bool {
			out[key.String()] = decodeGJSON(value)
			return true
		})
		return out
	case res.Type == gjson.Number:
		return res.Float()
	case res.Type == gjson.True:
		return true
	case res.Type == gjson.False:
		return false
	case res.Type == gjson.Null:
		return nil
	default:
		return res.String()
	}
}

// buildFrame 按推断类型构造对应 payload 与 schemaHint。
func buildFrame(channelID string, pt model.PayloadType, value any, ts int64) (model.Frame, map[string]model.FieldHint, error) {
	switch pt {
	case model.PayloadScalar:
		f, _ := model.ParseFloat64(value)
		fr, err := model.NewFrame(channelID, pt, model.ScalarPayload{Value: f}, ts)
		return fr, nil, err
	case model.PayloadTimeSeries:
		m, _ := value.(map[string]any)
		fields, hints := model.FlattenNumericFields(m, "")
		fr, err := model.NewFrame(channelID, pt, model.TimeSeriesPayload{T: ts, Fields: fields}, ts)
		return fr, hints, err
	case model.PayloadTable:
		arr, _ := value.([]any)
		fr, err := model.NewFrame(channelID, pt, buildTable(arr), ts)
		return fr, model.BuildSchemaHint(value, ""), err
	default:
		fr, err := model.NewFrame(channelID, model.PayloadJSON, model.JSONPayload{Root: value}, ts)
		return fr, model.BuildSchemaHint(value, ""), err
	}
}

// buildTable 把数组转成表格载荷（列由首元素的键推断，超出上限截断）。
func buildTable(rows []any) model.TablePayload {
	truncated := false
	if len(rows) > model.MaxTableRows {
		rows = rows[:model.MaxTableRows]
		truncated = true
	}
	columns := make([]model.TableColumn, 0)
	seen := map[string]bool{}
	if len(rows) > 0 {
		if m, ok := rows[0].(map[string]any); ok {
			for _, k := range sortedKeysOf(m) {
				seen[k] = true
				columns = append(columns, model.TableColumn{Key: k, Title: k, Type: columnTypeOf(m[k])})
			}
		}
	}
	out := make([][]any, 0, len(rows))
	for _, r := range rows {
		if m, ok := r.(map[string]any); ok {
			row := make([]any, 0, len(columns))
			for _, c := range columns {
				row = append(row, m[c.Key])
			}
			out = append(out, row)
		} else {
			out = append(out, []any{r})
		}
	}
	if len(columns) == 0 && len(rows) > 0 {
		columns = append(columns, model.TableColumn{Key: "value", Title: "value", Type: "string"})
	}
	_ = seen
	return model.TablePayload{Columns: columns, Rows: out, Total: len(rows), Truncated: truncated}
}

// columnTypeOf 推断列类型。
func columnTypeOf(v any) string {
	switch v.(type) {
	case bool:
		return "boolean"
	case nil, string:
		return "string"
	default:
		if _, ok := model.ParseFloat64(v); ok {
			return "number"
		}
		return "string"
	}
}

// sortedKeysOf 返回 map 的排序键。
func sortedKeysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// StopChannel 停止轮询（幂等）。
func (a *Adapter) StopChannel(channelID string) error {
	a.mu.Lock()
	ch, ok := a.channels[channelID]
	if ok {
		delete(a.channels, channelID)
	}
	a.mu.Unlock()
	if ok && ch != nil && ch.cancel != nil {
		ch.cancel()
	}
	return nil
}

// Close 停止全部轮询并等待退出（带超时保护）。
func (a *Adapter) Close() error {
	a.mu.Lock()
	ids := make([]string, 0, len(a.channels))
	for id := range a.channels {
		ids = append(ids, id)
	}
	cancel := a.cancel
	a.cancel = nil
	a.started = false
	a.mu.Unlock()
	for _, id := range ids {
		_ = a.StopChannel(id)
	}
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		a.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Duration(config.AdapterCloseTimeoutMs) * time.Millisecond):
		a.log.Error("等待 HTTP 适配器 goroutine 退出超时")
	}
	return nil
}

// timeout 返回单次请求超时。
func (a *Adapter) timeout() time.Duration {
	t := a.params.TimeoutMs
	if t <= 0 {
		t = config.DefaultTimeoutMs
	}
	return time.Duration(t) * time.Millisecond
}

// LastError 返回最近一次错误。
func (a *Adapter) LastError() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastErr
}
