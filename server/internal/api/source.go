package api

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/monitorall/monitorall/internal/adapter"
	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/crypto"
	"github.com/monitorall/monitorall/internal/media"
	"github.com/monitorall/monitorall/internal/model"
)

// ————————————————— 请求/响应结构体（架构 §7.3） —————————————————

// CreateDataSourceReq 为创建/测试数据源的请求体。
type CreateDataSourceReq struct {
	Name       string             `json:"name" binding:"required"`
	Kind       model.DataSourceKind `json:"kind" binding:"required,oneof=video ros http"`
	Protocol   model.Protocol     `json:"protocol" binding:"required"`
	ConnParams map[string]any     `json:"connParams" binding:"required"`
	SecretKeys []string           `json:"secretKeys,omitempty"`
}

// UpdateDataSourceReq 为更新数据源的请求体（字段可缺省）。
type UpdateDataSourceReq struct {
	Name       string             `json:"name,omitempty"`
	Kind       model.DataSourceKind `json:"kind,omitempty"`
	Protocol   model.Protocol     `json:"protocol,omitempty"`
	ConnParams map[string]any     `json:"connParams,omitempty"`
	SecretKeys []string           `json:"secretKeys,omitempty"`
	RetryPolicy *model.RetryPolicy `json:"retryPolicy,omitempty"`
	Enabled    *bool              `json:"enabled,omitempty"`
}

// TestResult 为测试连接的返回体（D4：MVP 必须实现）。
type TestResult struct {
	OK          bool               `json:"ok"`
	PayloadType model.PayloadType  `json:"payloadType,omitempty"`
	Sample      *model.Frame       `json:"sample,omitempty"`
	Channels    []string           `json:"channels,omitempty"`
	Error       string             `json:"error,omitempty"`
	Hint        string             `json:"hint,omitempty"`
	LatencyMs   int64              `json:"latencyMs"`
}

// CreateChannelReq 为手工添加通道的请求体。
type CreateChannelReq struct {
	Name        string            `json:"name" binding:"required"`
	PayloadType model.PayloadType `json:"payloadType,omitempty"`
	Meta        map[string]any    `json:"meta,omitempty"`
	RateLimitHz float64           `json:"rateLimitHz,omitempty"`
}

// ————————————————— 数据源 CRUD —————————————————

// listDataSources 返回全部数据源（含运行时状态与脱敏后的连接参数）。
func (d *Deps) listDataSources(c *gin.Context) {
	list, err := d.Store.ListDataSources()
	if err != nil {
		Fail(c, err)
		return
	}
	for i := range list {
		d.decorateSource(&list[i])
	}
	OK(c, list)
}

// getDataSource 读取单个数据源。
func (d *Deps) getDataSource(c *gin.Context) {
	ds, err := d.Store.GetDataSource(c.Param("id"))
	if err != nil {
		Fail(c, err)
		return
	}
	d.decorateSource(ds)
	OK(c, ds)
}

// decorateSource 用内存运行态覆盖库内状态（运行态不落盘）。
func (d *Deps) decorateSource(ds *model.DataSource) {
	if ds == nil || d.Adm == nil {
		return
	}
	st, lastErr, _, _ := d.Adm.SourceStatus(ds.ID)
	if st != "" && st != model.StatusIdle {
		ds.Status = st
		ds.LastError = lastErr
	}
	if ds.SecretKeys == nil {
		ds.SecretKeys = []string{}
	}
	// 出参一律脱敏（conn_params 入库时已是 ***，此处再兜一层）
	ds.ConnParams = crypto.MaskConnParams(ds.ConnParams, ds.SecretKeys)
}

// createDataSource 创建数据源并按类型自动创建默认通道。
func (d *Deps) createDataSource(c *gin.Context) {
	var req CreateDataSourceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, apperr.Wrap(err, apperr.BadRequest, "请求体解析失败"))
		return
	}
	if err := validateProtocol(req.Kind, req.Protocol); err != nil {
		Fail(c, err)
		return
	}
	if err := validateConnParams(req.Kind, req.ConnParams); err != nil {
		Fail(c, err)
		return
	}

	ds := model.NewDataSource(req.Name, req.Kind, req.Protocol, req.ConnParams)
	ds.SecretKeys = crypto.MergeSecretKeys(req.SecretKeys, req.Kind)
	if err := d.Store.CreateDataSource(ds); err != nil {
		Fail(c, err)
		return
	}
	channels, err := d.autoCreateChannels(c.Request.Context(), ds)
	if err != nil {
		// 通道创建失败不回滚数据源，返回已创建结果 + 警告日志
		d.Log.Warn("自动创建通道失败", "dataSourceId", ds.ID, "err", err)
	}
	d.decorateSource(ds)
	resp := struct {
		model.DataSource
		Channels []model.Channel `json:"channels"`
	}{*ds, channels}
	Created(c, resp)
}

// autoCreateChannels 按数据源类型自动创建默认通道（video 1 个 / http 1 个 / ros 按 topics）。
func (d *Deps) autoCreateChannels(ctx context.Context, ds *model.DataSource) ([]model.Channel, error) {
	specs := make([]adapter.ChannelSpec, 0, 4)
	// 用真实连接参数构造临时适配器（不建连），仅用于发现通道
	realParams, err := d.Store.ConnParamsFor(ds.ID)
	if err != nil {
		return nil, err
	}
	dsCopy := *ds
	dsCopy.ConnParams = realParams
	ad, err := adapter.New(ds.Kind, &dsCopy, d.Log.With("stage", "create"))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(config.DefaultTimeoutMs)*time.Millisecond)
	defer cancel()
	discovered, err := ad.ListChannels(ctx)
	_ = ad.Close()
	if err != nil || len(discovered) == 0 {
		// 发现失败时按类型兜底一个通道名，保证用户仍可手工配置
		discovered = fallbackSpecs(ds, realParams)
	}
	specs = append(specs, discovered...)

	out := make([]model.Channel, 0, len(specs))
	for _, spec := range specs {
		ch := model.NewChannel(ds.ID, spec.Name, spec.PayloadType, spec.Meta)
		ch.RateLimitHz = spec.DefaultHz
		if err := d.Store.CreateChannel(ch); err != nil {
			d.Log.Warn("创建通道失败", "name", spec.Name, "err", err)
			continue
		}
		out = append(out, *ch)
	}
	return out, nil
}

// fallbackSpecs 在通道发现失败时给出兜底通道（ROS 手工填 topic 是 P0 主路径）。
func fallbackSpecs(ds *model.DataSource, params map[string]any) []adapter.ChannelSpec {
	switch ds.Kind {
	case model.KindVideo:
		vp, _ := adapter.ParamsTo[model.VideoConnParams](params)
		info, err := media.ParseRTMPURL(vp.RtmpURL)
		name := "live"
		if err == nil {
			name = info.Path
		}
		return []adapter.ChannelSpec{{
			Name: name, PayloadType: model.PayloadVideoStream,
			Meta: map[string]any{model.MetaRtmpURL: vp.RtmpURL},
		}}
	case model.KindHTTP:
		hp, _ := adapter.ParamsTo[model.HTTPConnParams](params)
		return []adapter.ChannelSpec{{
			Name: channelNameOf(hp.URL), PayloadType: model.PayloadJSON,
			Meta: map[string]any{model.MetaHTTPMethod: hp.Method, model.MetaJSONPath: hp.JSONPath},
		}}
	default:
		rp, _ := adapter.ParamsTo[model.ROSConnParams](params)
		specs := make([]adapter.ChannelSpec, 0, len(rp.Topics))
		for _, t := range rp.Topics {
			specs = append(specs, adapter.ChannelSpec{Name: t, PayloadType: model.PayloadJSON})
		}
		return specs
	}
}

// updateDataSource 更新数据源（敏感值 *** 表示保持原值）。
func (d *Deps) updateDataSource(c *gin.Context) {
	var req UpdateDataSourceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, apperr.Wrap(err, apperr.BadRequest, "请求体解析失败"))
		return
	}
	ds, err := d.Store.GetDataSource(c.Param("id"))
	if err != nil {
		Fail(c, err)
		return
	}
	if req.Name != "" {
		ds.Name = req.Name
	}
	if req.Kind != "" && req.Kind != ds.Kind {
		ds.Kind = req.Kind
	}
	if req.Protocol != "" {
		if err := validateProtocol(ds.Kind, req.Protocol); err != nil {
			Fail(c, err)
			return
		}
		if req.Protocol != ds.Protocol {
			// 改协议/版本 → 断开重建（释放旧适配器）
			d.releaseSourceSubscriptions(ds.ID)
		}
		ds.Protocol = req.Protocol
	}
	if req.ConnParams != nil {
		if err := validateConnParams(ds.Kind, req.ConnParams); err != nil {
			Fail(c, err)
			return
		}
		ds.ConnParams = req.ConnParams
	}
	if req.SecretKeys != nil {
		ds.SecretKeys = crypto.MergeSecretKeys(req.SecretKeys, ds.Kind)
	}
	if req.RetryPolicy != nil {
		ds.RetryPolicy = req.RetryPolicy
	}
	if req.Enabled != nil {
		ds.Enabled = *req.Enabled
	}
	if err := d.Store.UpdateDataSource(ds); err != nil {
		Fail(c, err)
		return
	}
	d.decorateSource(ds)
	OK(c, ds)
}

// deleteDataSource 删除数据源，级联清理通道、卡片与适配器订阅。
func (d *Deps) deleteDataSource(c *gin.Context) {
	id := c.Param("id")
	d.releaseSourceSubscriptions(id)
	if err := d.Store.DeleteDataSource(id); err != nil {
		Fail(c, err)
		return
	}
	OK(c, map[string]any{"deleted": true})
}

// releaseSourceSubscriptions 释放某数据源在适配器上的全部订阅。
func (d *Deps) releaseSourceSubscriptions(dsID string) {
	if d.Adm == nil || d.Store == nil {
		return
	}
	chs, err := d.Store.ListChannelsBySource(dsID)
	if err != nil {
		return
	}
	ds, err := d.Store.GetDataSource(dsID)
	if err != nil {
		return
	}
	for i := range chs {
		if err := d.Adm.Unsubscribe(ds, &chs[i]); err != nil {
			d.Log.Warn("释放订阅失败", "channelId", chs[i].ID, "err", err)
		}
	}
}

// testDataSource 测试连接（不落库），返回识别到的 payloadType 与一帧样例（D4）。
func (d *Deps) testDataSource(c *gin.Context) {
	var req CreateDataSourceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, apperr.Wrap(err, apperr.BadRequest, "请求体解析失败"))
		return
	}
	if err := validateProtocol(req.Kind, req.Protocol); err != nil {
		Fail(c, err)
		return
	}
	if err := validateConnParams(req.Kind, req.ConnParams); err != nil {
		Fail(c, err)
		return
	}

	start := time.Now()
	ds := model.NewDataSource(req.Name, req.Kind, req.Protocol, req.ConnParams)
	ds.SecretKeys = crypto.MergeSecretKeys(req.SecretKeys, req.Kind)

	result := TestResult{LatencyMs: 0}
	ad, err := adapter.New(ds.Kind, ds, d.Log.With("stage", "test"))
	if err != nil {
		result.Error = err.Error()
		result.Hint = "请检查数据源类型与连接参数"
		OK(c, result)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(),
		time.Duration(d.Cfg.Adapter.SampleTimeoutMs+config.DefaultTimeoutMs)*time.Millisecond)
	defer cancel()

	if err := ad.Start(ctx); err != nil {
		_ = ad.Close()
		ae := apperr.FromError(err)
		result.Error = ae.Message
		result.Hint = hintFor(ds.Kind, ae.Code)
		result.LatencyMs = time.Since(start).Milliseconds()
		OK(c, result)
		return
	}
	defer func() { _ = ad.Close() }()

	specs, err := ad.ListChannels(ctx)
	if err != nil {
		// 发现失败软降级：返回已识别信息，不置错误态（D5）
		d.Log.Debug("测试连接时通道发现失败", "err", err)
	}
	channels := make([]string, 0, len(specs))
	for _, s := range specs {
		channels = append(channels, s.Name)
	}
	result.Channels = channels
	if len(specs) == 0 {
		specs = fallbackSpecs(ds, ds.ConnParams)
		for _, s := range specs {
			result.Channels = append(result.Channels, s.Name)
		}
	}

	if len(specs) > 0 {
		timeout := time.Duration(d.Cfg.Adapter.SampleTimeoutMs) * time.Millisecond
		f, err := d.Adm.Sample(ctx, ds, specs[0], timeout)
		if err != nil {
			ae := apperr.FromError(err)
			result.Error = ae.Message
			result.Hint = hintFor(ds.Kind, ae.Code)
		} else {
			result.OK = true
			result.Sample = f
			result.PayloadType = f.PayloadType
			result.LatencyMs = time.Since(start).Milliseconds()
			if f.PublishedTs > 0 && f.IngestedTs >= f.PublishedTs {
				result.LatencyMs = f.IngestedTs - f.PublishedTs
			}
		}
	}
	OK(c, result)
}

// hintFor 按数据源类型与错误码给出修复建议。
func hintFor(kind model.DataSourceKind, code apperr.Code) string {
	switch code {
	case apperr.InvalidURL:
		return "请检查地址格式：视频为 rtmp://host:1936/live/xxx，ROS 为 ws://host:9090，HTTP 为 http://host/api"
	case apperr.SourceConnectFailed:
		switch kind {
		case model.KindROS:
			return "请确认 rosbridge 已启动（ROS1: roslaunch rosbridge_server rosbridge_websocket.launch；ROS2: ros2 launch rosbridge_server rosbridge_websocket_launch.xml）"
		case model.KindVideo:
			return "请确认 MediaMTX 已启动且 RTMP 端口可达"
		default:
			return "请确认目标服务已启动且局域网可访问"
		}
	case apperr.SampleTimeout:
		return "已连上但未收到数据：请检查话题名/路径是否正确，或数据是否在持续发布"
	default:
		return "请检查连接参数与网络连通性"
	}
}

// ————————————————— 通道 —————————————————

// listChannels 列出数据源下的通道；discover=1 时尝试从源发现（软失败降级）。
func (d *Deps) listChannels(c *gin.Context) {
	dsID := c.Param("id")
	ds, err := d.Store.GetDataSource(dsID)
	if err != nil {
		Fail(c, err)
		return
	}
	discover := c.Query("discover") == "1"

	persisted, err := d.Store.ListChannelsBySource(dsID)
	if err != nil {
		Fail(c, err)
		return
	}
	byName := make(map[string]model.Channel, len(persisted))
	for _, ch := range persisted {
		byName[ch.Name] = ch
	}

	if discover {
		specs, err := d.discoverChannelSpecs(ds)
		if err == nil {
			for _, spec := range specs {
				if _, ok := byName[spec.Name]; ok {
					continue
				}
				ch := model.NewChannel(dsID, spec.Name, spec.PayloadType, spec.Meta)
				ch.RateLimitHz = spec.DefaultHz
				if err := d.Store.CreateChannel(ch); err == nil {
					persisted = append(persisted, *ch)
					byName[spec.Name] = *ch
				}
			}
		} else {
			// D5：rosapi 失败软降级为返回已持久化通道
			d.Log.Debug("通道发现失败，返回已持久化通道", "dataSourceId", dsID, "err", err)
		}
	}

	out := make([]model.ChannelRuntime, 0, len(persisted))
	for i := range persisted {
		out = append(out, d.channelRuntime(&persisted[i]))
	}
	OK(c, out)
}

// discoverChannelSpecs 用适配器发现通道（不建长连接，发现后即关闭）。
func (d *Deps) discoverChannelSpecs(ds *model.DataSource) ([]adapter.ChannelSpec, error) {
	realParams, err := d.Store.ConnParamsFor(ds.ID)
	if err != nil {
		return nil, err
	}
	dsCopy := *ds
	dsCopy.ConnParams = realParams
	ad, err := adapter.New(ds.Kind, &dsCopy, d.Log.With("stage", "discover"))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.DefaultTimeoutMs)*time.Millisecond)
	defer cancel()
	if err := ad.Start(ctx); err != nil {
		_ = ad.Close()
		return nil, err
	}
	specs, err := ad.ListChannels(ctx)
	_ = ad.Close()
	return specs, err
}

// channelRuntime 组装通道运行态（refCount / frameRateHz / dropped）。
func (d *Deps) channelRuntime(ch *model.Channel) model.ChannelRuntime {
	rt := model.ChannelRuntime{Channel: *ch}
	if d.Bus == nil {
		return rt
	}
	st := d.Bus.Stats(ch.ID)
	rt.RefCount = st.RefCount
	rt.FrameRateHz = st.FrameRateHz
	rt.Dropped = st.Dropped
	if st.Status != "" {
		rt.Status = st.Status
	} else if d.Adm != nil {
		rt.Status = d.Adm.ChannelStatus(ch.ID)
	}
	rt.LastFrameAt = st.LastFrameAt
	rt.LastSeq = st.LastSeq
	rt.LatencyMs = st.LatencyMs
	if st.LastError != "" {
		rt.LastError = st.LastError
	}
	return rt
}

// createChannel 手工添加通道（ROS 手工填 topic 是 P0 主路径）。
func (d *Deps) createChannel(c *gin.Context) {
	dsID := c.Param("id")
	if _, err := d.Store.GetDataSource(dsID); err != nil {
		Fail(c, err)
		return
	}
	var req CreateChannelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, apperr.Wrap(err, apperr.BadRequest, "请求体解析失败"))
		return
	}
	ch := model.NewChannel(dsID, req.Name, req.PayloadType, req.Meta)
	ch.RateLimitHz = req.RateLimitHz
	if ch.PayloadType == "" {
		ch.PayloadType = model.PayloadJSON
	}
	if err := d.Store.CreateChannel(ch); err != nil {
		Fail(c, err)
		return
	}
	Created(c, ch)
}

// getChannel 读取通道运行态。
func (d *Deps) getChannel(c *gin.Context) {
	ch, err := d.Store.GetChannel(c.Param("id"))
	if err != nil {
		Fail(c, err)
		return
	}
	OK(c, d.channelRuntime(ch))
}

// sampleChannel 拉取通道样例帧（卡片向导 Step2 用）；3s 无数据 → 50004（D4）。
func (d *Deps) sampleChannel(c *gin.Context) {
	ch, err := d.Store.GetChannel(c.Param("id"))
	if err != nil {
		Fail(c, err)
		return
	}
	ds, err := d.Store.GetDataSource(ch.DataSourceID)
	if err != nil {
		Fail(c, err)
		return
	}
	realParams, err := d.Store.ConnParamsFor(ds.ID)
	if err != nil {
		Fail(c, err)
		return
	}
	ds.ConnParams = realParams

	timeoutMs := int64(d.Cfg.Adapter.SampleTimeoutMs)
	if v := c.Query("timeoutMs"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			timeoutMs = n
		}
	}
	spec := adapter.SpecFromChannel(ch)
	ctx, cancel := context.WithTimeout(c.Request.Context(),
		time.Duration(timeoutMs+int64(config.DefaultTimeoutMs))*time.Millisecond)
	defer cancel()

	f, err := d.Adm.Sample(ctx, ds, spec, time.Duration(timeoutMs)*time.Millisecond)
	if err != nil {
		Fail(c, err)
		return
	}
	OK(c, f)
}

// deleteChannel 删除通道并释放订阅。
func (d *Deps) deleteChannel(c *gin.Context) {
	ch, err := d.Store.GetChannel(c.Param("id"))
	if err != nil {
		Fail(c, err)
		return
	}
	ds, err := d.Store.GetDataSource(ch.DataSourceID)
	if err == nil && d.Adm != nil {
		_ = d.Adm.Unsubscribe(ds, ch)
	}
	if d.Bus != nil {
		d.Bus.RemoveChannel(ch.ID)
	}
	if err := d.Store.DeleteChannel(ch.ID); err != nil {
		Fail(c, err)
		return
	}
	OK(c, map[string]any{"deleted": true})
}

// ————————————————— 校验 —————————————————

// validateProtocol 校验 kind 与 protocol 的匹配关系。
func validateProtocol(kind model.DataSourceKind, proto model.Protocol) error {
	switch kind {
	case model.KindVideo:
		if proto != model.ProtoRTMP && proto != model.ProtoRTSP {
			return apperr.Newf(apperr.UnsupportedType, "视频数据源仅支持 rtmp（rtsp 为 P1），当前为 %s", proto)
		}
	case model.KindROS:
		if proto != model.ProtoROS1 && proto != model.ProtoROS2 {
			return apperr.Newf(apperr.UnsupportedType, "ROS 数据源协议必须是 ros1 或 ros2，当前为 %s", proto)
		}
	case model.KindHTTP:
		if proto != model.ProtoHTTPPoll {
			return apperr.Newf(apperr.UnsupportedType, "HTTP 数据源协议必须是 http-poll，当前为 %s", proto)
		}
	default:
		return apperr.Newf(apperr.UnsupportedType, "不支持的数据源类型: %s", kind)
	}
	return nil
}

// validateConnParams 校验连接参数的关键字段（URL 合法性 → 40002）。
func validateConnParams(kind model.DataSourceKind, params map[string]any) error {
	if len(params) == 0 {
		return apperr.New(apperr.InvalidParam, "connParams 不能为空")
	}
	switch kind {
	case model.KindVideo:
		vp, err := adapter.ParamsTo[model.VideoConnParams](params)
		if err != nil {
			return err
		}
		if _, err := media.ParseRTMPURL(vp.RtmpURL); err != nil {
			return err
		}
	case model.KindROS:
		rp, err := adapter.ParamsTo[model.ROSConnParams](params)
		if err != nil {
			return err
		}
		if strings.TrimSpace(rp.BridgeURL) == "" {
			return apperr.New(apperr.InvalidParam, "bridgeUrl 不能为空")
		}
		if err := validateWSURL(rp.BridgeURL); err != nil {
			return err
		}
	case model.KindHTTP:
		hp, err := adapter.ParamsTo[model.HTTPConnParams](params)
		if err != nil {
			return err
		}
		if strings.TrimSpace(hp.URL) == "" {
			return apperr.New(apperr.InvalidParam, "url 不能为空")
		}
		if _, err := url.ParseRequestURI(hp.URL); err != nil {
			return apperr.Wrap(err, apperr.InvalidURL, "HTTP 地址非法")
		}
	}
	return nil
}

// validateWSURL 校验 rosbridge 地址（ws/wss）。
func validateWSURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return apperr.Wrap(err, apperr.InvalidURL, "rosbridge 地址解析失败")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "ws" && scheme != "wss" {
		return apperr.Newf(apperr.InvalidURL, "rosbridge 地址协议必须是 ws/wss，当前为 %q", u.Scheme)
	}
	if u.Host == "" {
		return apperr.New(apperr.InvalidURL, "rosbridge 地址缺少主机")
	}
	return nil
}

// channelNameOf 从 URL 提取 path 作为默认通道名（与 http 适配器保持一致）。
func channelNameOf(rawURL string) string {
	rest := rawURL
	if idx := strings.Index(rest, "://"); idx >= 0 {
		rest = rest[idx+3:]
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
