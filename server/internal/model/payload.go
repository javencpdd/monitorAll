package model

// ————————————————— 各 payloadType 的 payload 结构（架构 §5.3，逐字实现） —————————————————

// VideoPayload 为 video_stream 的载荷：只含地址与状态，不含像素。
type VideoPayload struct {
	Protocol       string            `json:"protocol"`                 // webrtc | hls | flv
	State          string            `json:"state"`                    // ready | not_ready | degraded
	DegradedFrom   string            `json:"degradedFrom,omitempty"`   // 从 webrtc 降级时 = "webrtc"
	DegradedReason string            `json:"degradedReason,omitempty"`
	URLs           map[string]string `json:"urls"`                     // {"webrtc":"...","hls":"...","flv":"..."} 只含有值的
	SourceURL      string            `json:"sourceUrl"`                // rtmp://172.31.68.227:1936/live/lite3
	Path           string            `json:"path"`                     // live/lite3
	Ready          bool              `json:"ready"`
	Readers        int               `json:"readers"`
	BytesPerSec    int64             `json:"bytesPerSec,omitempty"`
	RetryInSec     int               `json:"retryInSec,omitempty"`     // 降级后重试首选协议的倒计时
}

// VideoPayload 状态常量。
const (
	VideoStateReady    = "ready"
	VideoStateNotReady = "not_ready"
	VideoStateDegraded = "degraded"
)

// ImagePayload 为 image 的载荷（base64 不带 data: 前缀，mime 单独给）。
type ImagePayload struct {
	Mime     string `json:"mime"`               // image/jpeg | image/png
	Encoding string `json:"encoding"`           // base64 | url
	Data     string `json:"data"`               // base64 串 或 可访问 URL
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	StampMs  int64  `json:"stampMs,omitempty"`  // 图像采集时刻（来自 msg header.stamp）
}

// 图像编码方式常量。
const (
	ImageEncodingBase64 = "base64"
	ImageEncodingURL    = "url"
)

// ScalarPayload 为 scalar 的载荷。
type ScalarPayload struct {
	Value   float64 `json:"value"`
	Unit    string  `json:"unit,omitempty"`
	Min     float64 `json:"min,omitempty"`     // 已知量程，未知则省略
	Max     float64 `json:"max,omitempty"`
	Label   string  `json:"label,omitempty"`   // 字段语义名，如 "voltage"
	RawPath string  `json:"rawPath,omitempty"` // 从 json 挑字段时的 JSONPath
}

// TimeSeriesPayload 为 time_series_sample 的载荷。
type TimeSeriesPayload struct {
	T      int64              `json:"t"`                // 采样时刻 ms（优先用 msg header.stamp）
	Fields map[string]float64 `json:"fields"`           // 扁平化后的数值字段 → 值
	Units  map[string]string  `json:"units,omitempty"`
}

// GeoPosePayload 为 geo_pose 的载荷。crs 字段是合规关键，由后端盖章，绝不改写坐标值。
type GeoPosePayload struct {
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	Alt      float64 `json:"alt,omitempty"`
	YawDeg   float64 `json:"yawDeg,omitempty"`   // 朝向，正北为 0，顺时针
	Speed    float64 `json:"speed,omitempty"`    // m/s
	CRS      CRS     `json:"crs"`                // 源坐标系，默认 WGS-84
	Accuracy float64 `json:"accuracy,omitempty"` // m
	T        int64   `json:"t"`
	Label    string  `json:"label,omitempty"`
}

// TableColumn 描述表格一列。
type TableColumn struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Type  string `json:"type"` // number|string|boolean
}

// TablePayload 为 table 的载荷。
type TablePayload struct {
	Columns   []TableColumn `json:"columns"`
	Rows      [][]any       `json:"rows"`
	Total     int           `json:"total"`
	Truncated bool          `json:"truncated"`
}

// JSONPayload 为 json（兜底）的载荷。
type JSONPayload struct {
	Root any `json:"root"`
}

// MaxTableRows 为表格载荷的默认截断行数上限（常量集中，禁止魔法值）。
const MaxTableRows = 1000
