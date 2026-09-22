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
	// RtmpURL 为流地址，支持 rtmp/rtmps/rtsp/rtsps：
	// rtmp://172.31.68.227:1936/live/lite3、rtsp://admin:pwd@192.168.2.69:554/Streaming/Channels/101
	// publish（接收推流）模式下可留空，此时必须给出 MediaMTXPath。
	RtmpURL           string `json:"rtmpUrl"`
	MediaMTXPath      string `json:"mediaMtxPath,omitempty"`      // MediaMTX 路径；留空时自动从流地址解析
	PreferredProtocol string `json:"preferredProtocol,omitempty"` // webrtc | hls | flv，默认 webrtc
	Audio             bool   `json:"audio"`
	// Mode 为接入模式：pull（默认，主动拉流）| publish（接收远端 WHIP/RTMP 推流）。
	Mode string `json:"mode,omitempty"`
	// RTSPTransport 为 RTSP 拉流传输方式：tcp（默认）| udp | automatic。
	RTSPTransport string `json:"rtspTransport,omitempty"`
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

// ReplayConnParams 为离线文件回放类数据源的连接参数。
type ReplayConnParams struct {
	FileID   string  `json:"fileId"`             // imports 表主键，指向 data/imports 下的文件
	Speed    float64 `json:"speed"`              // 倍速，默认 1；<=0 时按 1
	Loop     bool    `json:"loop"`               // 播完是否从头再来
	TimePath string  `json:"timePath,omitempty"` // 帧时间戳字段路径（点分）；取不到则用记录序号推进
	JSONPath string  `json:"jsonPath,omitempty"` // 从每条记录里再取子路径
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
	// MetaIsLocalPublish 标记该地址是否推给本机 MediaMTX（RTSP 恒为 false）。
	MetaIsLocalPublish = "isLocalPublish"
	// MetaVideoMode 为视频接入模式（pull / publish）。
	MetaVideoMode = "videoMode"
	// MetaPublishURL 为 publish 模式下的 WebRTC WHIP 推流地址。
	MetaPublishURL = "publishUrl"
	// MetaPublishRTMPURL 为 publish 模式下的 RTMP 推流地址。
	MetaPublishRTMPURL = "publishRtmpUrl"
	// MetaRTSPTransport 为 RTSP 拉流的传输方式（tcp / udp / automatic）。
	MetaRTSPTransport = "rtspTransport"
	// MetaHTTPMethod 为 HTTP 通道的请求方法。
	MetaHTTPMethod = "httpMethod"
	// MetaJSONPath 为 HTTP 通道的提取路径。
	MetaJSONPath = "jsonPath"
	// MetaCRS 为数据源声明的坐标系。
	MetaCRS = "crs"
	// MetaVideoCodec 为视频编码（由 MediaMTX 上报）。
	MetaVideoCodec = "videoCodec"
	// MetaReplayFileID 为回放通道对应的导入文件 ID。
	MetaReplayFileID = "replayFileId"
	// MetaReplaySpeed 为回放倍速。
	MetaReplaySpeed = "replaySpeed"
	// MetaReplayLoop 标记回放是否循环。
	MetaReplayLoop = "replayLoop"
	// MetaReplayTimePath 为回放帧时间戳字段路径。
	MetaReplayTimePath = "replayTimePath"
	// MetaReplayJSONPath 为回放记录内的提取子路径。
	MetaReplayJSONPath = "replayJsonPath"
	// MetaReplayFrameCount 为导入文件解析出的总帧数。
	MetaReplayFrameCount = "replayFrameCount"
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
