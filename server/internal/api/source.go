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
	"github.com/monitorall/monitorall/internal/logx"
	"github.com/monitorall/monitorall/internal/media"
	"github.com/monitorall/monitorall/internal/model"
)

// ————————————————— 请求/响应结构体（架构 §7.3） —————————————————

// CreateDataSourceReq 为创建/测试数据源的请求体。
type CreateDataSourceReq struct {
	Name       string               `json:"name" binding:"required"`
	Kind       model.DataSourceKind `json:"kind" binding:"required,oneof=video ros http replay"`
	Protocol   model.Protocol       `json:"protocol" binding:"required"`
	ConnParams map[string]any       `json:"connParams" binding:"required"`
	SecretKeys []string             `json:"secretKeys,omitempty"`
}

// UpdateDataSourceReq 为更新数据源的请求体（字段可缺省）。
type UpdateDataSourceReq struct {
	Name        string               `json:"name,omitempty"`
	Kind        model.DataSourceKind `json:"kind,omitempty"`
	Protocol    model.Protocol       `json:"protocol,omitempty"`
	ConnParams  map[string]any       `json:"connParams,omitempty"`
	SecretKeys  []string             `json:"secretKeys,omitempty"`
	RetryPolicy *model.RetryPolicy   `json:"retryPolicy,omitempty"`
	Enabled     *bool                `json:"enabled,omitempty"`
}

// TestResult 为测试连接的返回体（D4：MVP 必须实现）。
type TestResult struct {
	OK          bool              `json:"ok"`
	PayloadType model.PayloadType `json:"payloadType,omitempty"`
	Sample      *model.Frame      `json:"sample,omitempty"`
	Channels    []string          `json:"channels,omitempty"`
	Error       string            `json:"error,omitempty"`
	Hint        string            `json:"hint,omitempty"`
	LatencyMs   int64             `json:"latencyMs"`
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
	// 同路径冲突检测：MediaMTX 一个路径只能对应一路流，重名会静默顶掉前一个。
	// 必须在落库前拦，落库后适配器已经把路径注册进 MediaMTX，伤害就发生了。
	if err := d.checkMediaMTXPathConflictIn("", req.Kind, req.ConnParams); err != nil {
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
		name := "live"
		meta := map[string]any{model.MetaVideoMode: normalizeVideoMode(vp.Mode)}
		if strings.TrimSpace(vp.RtmpURL) != "" {
			meta[model.MetaRtmpURL] = strings.TrimSpace(vp.RtmpURL)
			if info, err := media.ParseStreamURL(vp.RtmpURL); err == nil {
				name = info.Path
				meta[model.MetaRtmpURL] = info.Raw
				if info.IsRTSP() {
					meta[model.MetaRTSPTransport] = config.RTSPTransportTCP
				}
			}
		}
		if p := strings.Trim(strings.TrimSpace(vp.MediaMTXPath), "/"); p != "" {
			name = p
		}
		meta[model.MetaMediaMTXPath] = name
		return []adapter.ChannelSpec{{
			Name: name, PayloadType: model.PayloadVideoStream, Meta: meta,
		}}
	case model.KindHTTP:
		hp, _ := adapter.ParamsTo[model.HTTPConnParams](params)
		return []adapter.ChannelSpec{{
			Name: channelNameOf(hp.URL), PayloadType: model.PayloadJSON,
			Meta: map[string]any{model.MetaHTTPMethod: hp.Method, model.MetaJSONPath: hp.JSONPath},
		}}
	case model.KindReplay:
		rp, _ := adapter.ParamsTo[model.ReplayConnParams](params)
		name := rp.FileID
		if name == "" {
			name = "replay"
		}
		return []adapter.ChannelSpec{{
			Name:        name,
			PayloadType: model.PayloadJSON,
			Meta: map[string]any{
				model.MetaReplayFileID: rp.FileID,
				model.MetaReplaySpeed:  rp.Speed,
				model.MetaReplayLoop:   rp.Loop,
			},
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
		// 排除自己（ds.ID）：否则改名字以外的小字段也会把自己判成冲突
		if err := d.checkMediaMTXPathConflictIn(ds.ID, ds.Kind, req.ConnParams); err != nil {
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
		return "请检查地址格式：视频为 rtmp://host:1936/live/xxx 或 rtsp://user:pass@host:554/Streaming/Channels/101，ROS 为 ws://host:9090，HTTP 为 http://host/api"
	case apperr.SourceConnectFailed:
		switch kind {
		case model.KindROS:
			return "请确认 rosbridge 已启动（ROS1: roslaunch rosbridge_server rosbridge_websocket.launch；ROS2: ros2 launch rosbridge_server rosbridge_websocket_launch.xml）"
		case model.KindVideo:
			return "请确认 MediaMTX 已启动，且 RTSP/RTMP 源端口可达（海康摄像头 RTSP 默认 554；建议传输方式选 TCP）"
		default:
			return "请确认目标服务已启动且局域网可访问"
		}
	case apperr.SampleTimeout:
		return "已连上但未收到数据：请检查话题名/路径是否正确，或数据是否在持续发布"
	default:
		return "请检查连接参数与网络连通性"
	}
}

// normalizeVideoMode 归一化视频接入模式，缺省 pull。
func normalizeVideoMode(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), config.VideoModePublish) {
		return config.VideoModePublish
	}
	return config.VideoModePull
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

// ————————————————— MediaMTX 同路径冲突检测 —————————————————

// VideoPathRef 描述一个 video 数据源占用的 MediaMTX 路径与它的上游身份。
type VideoPathRef struct {
	// ID 为数据源 ID（更新时用于排除自己）。
	ID string
	// Name 为数据源名称，报错时用来点名是谁占着。
	Name string
	// Path 为归一化后的 MediaMTX 路径（已 trim 空白与首尾斜杠）。
	Path string
	// Upstream 为上游身份：publish 或推给本机的 RTMP → "publisher"；
	// 拉流 → trim 后的完整流地址（含 RTSP 内联凭据）。
	Upstream string
}

// checkMediaMTXPathConflict 在已有数据源里找出与 incoming 同路径但上游不同的那个；
// 没有冲突返回 nil。
//
// 为什么必须拦：MediaMTX 里「一个路径 = 一条流」，且 mediamtx.yml 开着
// overridePublisher: true —— 重名时后来者会**静默顶掉**前者（实测：两个 ffmpeg 推同一
// 路径，前一个直接 Broken pipe 退出，后来的接管画面，前端毫无提示）。
// 系统此前没有任何唯一性约束（store 层无索引，create/update 也不查重）。
//
// 判定规则的关键是「不能一律拦截」：
//   - 同路径 + 同上游 → 放行。同一路相机被多个看板/多张卡复用是合法的，
//     一律拦截会误伤，而且这种复用本来就是设计内的。
//   - 同路径 + 不同上游 → 拦截。两路不同的流抢同一个路径，必然互相顶掉。
func checkMediaMTXPathConflict(existing []VideoPathRef, incoming VideoPathRef) *VideoPathRef {
	if incoming.Path == "" {
		return nil
	}
	for i := range existing {
		// 排除自己：更新数据源时 incoming.ID 是它自己的 ID，不排除的话
		// 改个名字以外的小字段就会把自己判成跟自己冲突，任何更新都失败。
		if incoming.ID != "" && existing[i].ID == incoming.ID {
			continue
		}
		if existing[i].Path == incoming.Path && existing[i].Upstream != incoming.Upstream {
			return &existing[i]
		}
	}
	return nil
}

// pathConflictError 构造同路径冲突的报错：点名占用方、说清两边上游、给出可改的名字。
// 只给「路径非法」不给「怎么改」等于把问题抛回给用户，这里必须给建议值。
func pathConflictError(incoming VideoPathRef, conflict *VideoPathRef) error {
	return apperr.Newf(apperr.InvalidParam,
		"MediaMTX 路径 %q 已被数据源「%s」占用，且两者上游不同（当前：%s，已存在：%s）。"+
			"同一路径只能对应一路流，重名会让后推的设备静默顶掉前一个。请改用唯一路径，如 %s 或 live/<设备>-<机位>",
		conflict.Path, conflict.Name,
		displayUpstream(incoming.Upstream), displayUpstream(conflict.Upstream),
		suggestAltPath(conflict.Path))
}

// displayUpstream 把上游身份渲染进面向用户的报错文案。
// 拉流地址常带内联凭据（rtsp://admin:pw@host/...），原样回显等于把口令塞进前端弹窗
// 与接口响应里，必须脱敏（本项目真实发生过口令进日志/进仓库的事故）。
func displayUpstream(u string) string {
	switch u {
	case "":
		return "未知"
	case media.SourcePublisher:
		return u
	default:
		return logx.RedactURL(u)
	}
}

// suggestAltPath 给出一个简单的备选路径（live/cam1 → live/cam1-b）。
func suggestAltPath(path string) string {
	if path == "" {
		return "live/cam1"
	}
	return path + "-b"
}

// normalizeVideoPath 归一化 MediaMTX 路径用于比较：trim 空白与首尾斜杠。
// 与 fallbackSpecs 里的 trim 逻辑保持一致 —— /live/cam1 与 live/cam1 是同一条路径。
func normalizeVideoPath(v string) string {
	return strings.Trim(strings.TrimSpace(v), "/")
}

// videoPathRef 从连接参数构造路径引用；非 video 或路径为空时返回 false（调用方跳过）。
func videoPathRef(id, name string, params map[string]any, cfg *config.Config) (VideoPathRef, bool) {
	vp, err := adapter.ParamsTo[model.VideoConnParams](params)
	if err != nil {
		return VideoPathRef{}, false
	}
	path := normalizeVideoPath(vp.MediaMTXPath)
	if path == "" {
		return VideoPathRef{}, false
	}
	return VideoPathRef{ID: id, Name: name, Path: path, Upstream: videoUpstream(cfg, vp.Mode, vp.RtmpURL)}, true
}

// videoUpstream 计算 video 数据源的上游身份。
//
// publish 模式，或地址是推给本机 MediaMTX 的 RTMP → "publisher"（MediaMTX 侧就是
// 被动等远端推流，不管是谁推的都算同一个上游）。其余为 trim 空白后的完整流地址：
// 只 trim、不改大小写、不做 host 归一化 —— RTSP 常带内联凭据，逐字相同才算同一上游。
func videoUpstream(cfg *config.Config, mode, rtmpURL string) string {
	if normalizeVideoMode(mode) == config.VideoModePublish {
		return media.SourcePublisher
	}
	u := strings.TrimSpace(rtmpURL)
	if u == "" {
		// 无上游地址（存量脏数据才会出现）：身份未知，两个都未知视为同一上游
		return ""
	}
	if cfg != nil && media.IsLocalPublishURL(u, cfg.MediaMTX.PortRTMP, cfg.Server.LANHost) {
		return media.SourcePublisher
	}
	return u
}

// checkMediaMTXPathConflictIn 查库并做同路径冲突检测：命中返回可行动的 40001 错误。
//
// excludeID 为被更新的数据源自身 ID —— 必须排除自己，否则改个名字以外的小字段
// 就会把自己判成跟自己冲突，导致任何更新都失败。
func (d *Deps) checkMediaMTXPathConflictIn(excludeID string, kind model.DataSourceKind, params map[string]any) error {
	if kind != model.KindVideo || d.Store == nil {
		return nil
	}
	incoming, ok := videoPathRef(excludeID, "", params, d.Cfg)
	if !ok {
		return nil // 非 video 或 mediaMtxPath 为空 → 不查
	}
	refs, err := d.existingVideoPathRefs(excludeID)
	if err != nil {
		return err
	}
	if conflict := checkMediaMTXPathConflict(refs, incoming); conflict != nil {
		return pathConflictError(incoming, conflict)
	}
	return nil
}

// existingVideoPathRefs 取出库里其它 video 数据源的路径引用（排除 excludeID）。
//
// 用 ConnParamsFor 而不是 ListDataSources 直接返回的 connParams：后者里被声明为敏感
// 字段的键是 ***，拿 *** 去比对上游会把两路不同的流误判成同一个上游。
func (d *Deps) existingVideoPathRefs(excludeID string) ([]VideoPathRef, error) {
	list, err := d.Store.ListDataSources()
	if err != nil {
		return nil, err
	}
	out := make([]VideoPathRef, 0, len(list))
	for i := range list {
		if list[i].ID == excludeID || list[i].Kind != model.KindVideo {
			continue
		}
		params, err := d.Store.ConnParamsFor(list[i].ID)
		if err != nil || params == nil {
			continue // 单个源取不出真实参数时不阻塞创建，跳过即可
		}
		if r, ok := videoPathRef(list[i].ID, list[i].Name, params, d.Cfg); ok {
			out = append(out, r)
		}
	}
	return out, nil
}

// ————————————————— 校验 —————————————————

// validateProtocol 校验 kind 与 protocol 的匹配关系。
func validateProtocol(kind model.DataSourceKind, proto model.Protocol) error {
	switch kind {
	case model.KindVideo:
		if proto != model.ProtoRTMP && proto != model.ProtoRTSP {
			return apperr.Newf(apperr.UnsupportedType, "视频数据源仅支持 rtmp / rtsp，当前为 %s", proto)
		}
	case model.KindROS:
		if proto != model.ProtoROS1 && proto != model.ProtoROS2 {
			return apperr.Newf(apperr.UnsupportedType, "ROS 数据源协议必须是 ros1 或 ros2，当前为 %s", proto)
		}
	case model.KindHTTP:
		if proto != model.ProtoHTTPPoll {
			return apperr.Newf(apperr.UnsupportedType, "HTTP 数据源协议必须是 http-poll，当前为 %s", proto)
		}
	case model.KindReplay:
		if proto != model.ProtoFileReplay {
			return apperr.Newf(apperr.UnsupportedType, "离线回放数据源协议必须是 file-replay，当前为 %s", proto)
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
		// mediaMtxPath 只要填了就必须是合法路径名（不是完整地址）。
		// 填成 rtmp://host:1936/live/x 会让 MediaMTX 以 500 "invalid path name" 拒绝，
		// 前端只会显示「HLS 协商超时」，根因完全被掩盖（真实事故）。入口直接拒绝。
		if _, err := media.ValidateMediaMTXPath(vp.MediaMTXPath); err != nil {
			return err
		}
		if strings.TrimSpace(vp.RtmpURL) == "" {
			// 接收推流（publish）模式没有源地址，但必须给出 MediaMTX 路径
			if normalizeVideoMode(vp.Mode) == config.VideoModePublish &&
				strings.TrimSpace(vp.MediaMTXPath) != "" {
				return nil
			}
			return apperr.New(apperr.InvalidURL, "视频数据源缺少流地址（rtmp://… 或 rtsp://…）；接收推流模式请填写 mediaMtxPath")
		}
		if _, err := media.ParseStreamURL(vp.RtmpURL); err != nil {
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
	case model.KindReplay:
		rp, err := adapter.ParamsTo[model.ReplayConnParams](params)
		if err != nil {
			return err
		}
		if strings.TrimSpace(rp.FileID) == "" {
			return apperr.New(apperr.InvalidParam, "fileId 不能为空")
		}
		if rp.Speed < 0 {
			return apperr.Newf(apperr.InvalidParam, "倍速不能为负数，当前为 %v", rp.Speed)
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
