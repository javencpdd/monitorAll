package model

// ————————————————— 数据源与通道 —————————————————

// RetryPolicy 为重连退避策略。
type RetryPolicy struct {
	BaseMs   int     `json:"baseMs"`   // 默认 1000
	MaxMs    int     `json:"maxMs"`    // 默认 30000
	Jitter   float64 `json:"jitter"`   // 默认 0.2
	MaxRetry int     `json:"maxRetry"` // -1 = 无限，默认 -1
}

// DataSource 为数据源实体（落盘）。
type DataSource struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Kind          DataSourceKind `json:"kind"`
	Protocol      Protocol       `json:"protocol"`
	ConnParams    map[string]any `json:"connParams"`  // 敏感键已脱敏为 "***"
	SecretKeys    []string       `json:"secretKeys"`  // 声明哪些 connParams 键是敏感字段
	RetryPolicy   *RetryPolicy   `json:"retryPolicy,omitempty"`
	Status        Status         `json:"status"`
	LastError     string         `json:"lastError,omitempty"`
	LastOKAt      int64          `json:"lastOkAt,omitempty"`
	Enabled       bool           `json:"enabled"`
	CreatedAt     int64          `json:"createdAt"`
	UpdatedAt     int64          `json:"updatedAt"`
	SchemaVersion int            `json:"schemaVersion"`
}

// VideoConnParams 为视频类数据源的连接参数。
type VideoConnParams struct {
	RtmpURL           string `json:"rtmpUrl"`                     // rtmp://172.31.68.227:1936/live/lite3
	MediaMTXPath      string `json:"mediaMtxPath,omitempty"`      // 自动解析：live/lite3
	PreferredProtocol string `json:"preferredProtocol,omitempty"` // webrtc | hls | flv，默认 webrtc
	Audio             bool   `json:"audio"`
}

// ROSConnParams 为 ROS 类数据源的连接参数。
type ROSConnParams struct {
	Version    string            `json:"version"`              // ros1 | ros2
	BridgeURL  string            `json:"bridgeUrl"`            // ws://172.31.68.227:9090
	NodeName   string            `json:"nodeName,omitempty"`
	MasterURI  string            `json:"masterUri,omitempty"`  // ROS1 可选 http://host:11311
	DomainID   int               `json:"domainId,omitempty"`   // ROS2 可选
	Topics     []string          `json:"topics,omitempty"`     // 手工填写的 topic 列表
	TopicTypes map[string]string `json:"topicTypes,omitempty"` // topic → msgType（可空，运行时推断）
	CRS        string            `json:"crs,omitempty"`        // 数据源坐标系声明，默认 WGS-84
}

// HTTPConnParams 为 HTTP 轮询类数据源的连接参数。
type HTTPConnParams struct {
	URL         string            `json:"url"`
	Method      string            `json:"method"`            // GET | POST
	IntervalMs  int               `json:"intervalMs"`        // 默认 1000，下限 100
	Headers     map[string]string `json:"headers,omitempty"` // 敏感：Authorization
	Body        string            `json:"body,omitempty"`
	JSONPath    string            `json:"jsonPath,omitempty"`
	TimeoutMs   int               `json:"timeoutMs"`         // 默认 3000
	InsecureTLS bool              `json:"insecureTls"`
}

// Channel 为通道实体（落盘）。运行态字段（status/lastFrameAt/lastSeq/latencyMs）不落盘。
type Channel struct {
	ID           string         `json:"id"`
	DataSourceID string         `json:"dataSourceId"`
	Name         string         `json:"name"` // live/lite3 | /odom | /api/stat
	PayloadType  PayloadType    `json:"payloadType"`
	Meta         map[string]any `json:"meta,omitempty"` // rosMsgType / videoCodec / httpMethod ...
	RateLimitHz  float64        `json:"rateLimitHz"`     // 0 = 用全局默认；BE-04
	Status       Status         `json:"status"`
	LastError    string         `json:"lastError,omitempty"`
	LastFrameAt  int64          `json:"lastFrameAt,omitempty"`
	LastSeq      uint64         `json:"lastSeq"`
	LatencyMs    int64          `json:"latencyMs,omitempty"` // ingestedTs - publishedTs 滑动均值
	CreatedAt    int64          `json:"createdAt"`
}

// ChannelRuntime 为通道运行态视图（不落盘，仅 API/WS 返回）。
type ChannelRuntime struct {
	Channel
	RefCount    int     `json:"refCount"`    // 订阅引用计数
	FrameRateHz float64 `json:"frameRateHz"`
	Dropped     uint64  `json:"dropped"`     // 累计丢帧数
}

// ————————————————— 通道 meta 键常量（禁止散落魔法字符串） —————————————————

const (
	// MetaRosMsgType 为 ROS 通道的消息类型。
	MetaRosMsgType = "rosMsgType"
	// MetaRosVersion 为 ROS 版本（ros1 / ros2），值取自 Protocol。
	MetaRosVersion = "rosVersion"
	// MetaRtmpURL 为视频通道的 RTMP 源地址。
	MetaRtmpURL = "rtmpUrl"
	// MetaMediaMTXPath 为视频通道的 MediaMTX path。
	MetaMediaMTXPath = "mediaMtxPath"
	// MetaIsLocalPublish 标记该 RTMP 是否推给本机 MediaMTX。
	MetaIsLocalPublish = "isLocalPublish"
	// MetaHTTPMethod 为 HTTP 通道的请求方法。
	MetaHTTPMethod = "httpMethod"
	// MetaJSONPath 为 HTTP 通道的提取路径。
	MetaJSONPath = "jsonPath"
	// MetaCRS 为数据源声明的坐标系。
	MetaCRS = "crs"
	// MetaVideoCodec 为视频编码（由 MediaMTX 上报）。
	MetaVideoCodec = "videoCodec"
)

// NewDataSource 构造一个带缺省值的数据源。
func NewDataSource(name string, kind DataSourceKind, proto Protocol, params map[string]any) *DataSource {
	now := NowMs()
	if params == nil {
		params = map[string]any{}
	}
	return &DataSource{
		ID:            NewDataSourceID(),
		Name:          name,
		Kind:          kind,
		Protocol:      proto,
		ConnParams:    params,
		SecretKeys:    []string{},
		Status:        StatusIdle,
		Enabled:       true,
		CreatedAt:     now,
		UpdatedAt:     now,
		SchemaVersion: CurrentSchemaVersionForEntity,
	}
}

// NewChannel 构造一个带缺省值的通道。
func NewChannel(dsID, name string, pt PayloadType, meta map[string]any) *Channel {
	return &Channel{
		ID:           NewChannelID(),
		DataSourceID: dsID,
		Name:         name,
		PayloadType:  pt,
		Meta:         meta,
		RateLimitHz:  0,
		Status:       StatusIdle,
		CreatedAt:    NowMs(),
	}
}

// CurrentSchemaVersionForEntity 为持久化实体的 schemaVersion。
const CurrentSchemaVersionForEntity = 1
